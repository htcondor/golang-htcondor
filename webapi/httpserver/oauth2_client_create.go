package httpserver

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/ory/fosite"
	"golang.org/x/crypto/bcrypt"

	"github.com/htcondor/golang-htcondor/logging"
)

// Creating an OAuth2 client on purpose, rather than by self-registration.
//
// /mcp/oauth2/register (RFC 7591) already lets an app register itself, and
// for an app that implements discovery that is the better path: nobody has to
// copy an id and a secret anywhere. It is not the whole story, though, and
// the two gaps are different in kind.
//
// An operator needs to create one because the grants that act with no user
// present -- client_credentials, token exchange -- are deliberately not
// self-registrable (see selfRegistrableGrantTypes). A client holding either
// is a decision about the deployment, so there has to be a place where a
// human makes it.
//
// A user needs to create one because plenty of services that would consume
// this API do not speak dynamic registration at all: they ask for a client
// id, a client secret and a redirect URI to be pasted into a form. Without a
// way to mint those, "integrate this access point with the thing I already
// use" requires an administrator for what is really the user's own decision
// about their own jobs.
//
// What a user-created client may do is bounded twice over, and both bounds
// are here rather than at consent:
//
//   - its grant types, to the flows with a person on the other end. A user
//     cannot mint something that acts while they are not there.
//   - its scope list, to what the creator could themselves consent to. The
//     consent screen bounds it again at authorization time, which is the
//     bound that actually matters; this one keeps a client from being
//     *registered* for a scope its creator has no claim to, so the list a
//     client advertises is not a wish.

// supportedClientScopes is every scope a client may be registered for.
//
// Hoisted out of the dynamic registration handler, which is where it used to
// live as a local map. Three paths now create clients and all three have to
// agree on what a client may ask for; a second copy of this set is a second
// answer, and the one that drifts is whichever path was added later.
//
// Being registered for a scope is not holding it. What a grant ends up with
// is decided at consent by getScopesForGroups, which consults the user's
// groups and is free to grant less. A scope here that the policy never
// grants simply never appears in a token.
var supportedClientScopes = map[string]bool{
	"openid":         true,
	"profile":        true,
	"email":          true,
	"offline_access": true,
	"mcp:read":       true,
	"mcp:write":      true,
	// Requestable, but only granted to members of the configured group --
	// see getScopesForGroups. A client asking for them without the group
	// membership simply does not receive them.
	"mcp:admin":       true,
	"mcp:superuser":   true,
	scopeAnyUserRead:  true,
	scopeAnyUserWrite: true,
	// READ and WRITE only: they are the condor levels a token from this
	// server can carry (mapCondorScopesToAuthz).
	condorScopeRead:  true,
	condorScopeWrite: true,
}

const (
	// maxClientsPerOwner bounds how many integrations one person may hold.
	// Generous for a human managing their own, and low enough that the
	// client table cannot be filled from a session.
	maxClientsPerOwner = 20
	// maxClientNameLen bounds the display name. It is shown on the consent
	// screen, so it is a label and not a description.
	maxClientNameLen = 128
	// maxRedirectURIsPerClient bounds the registration. A service needs one
	// or two; a list longer than this is a mistake or a probe.
	maxRedirectURIsPerClient = 10
	// clientSecretBytes is the entropy in a generated client secret, in
	// bytes before hex expansion.
	clientSecretBytes = 32
)

// defaultClientGrantTypes is what a client gets when it names none: the
// browser flow and the refresh that keeps it alive.
var defaultClientGrantTypes = []string{authorizationCodeGrant, "refresh_token"}

// defaultClientScopes is what a client gets when it names none.
var defaultClientScopes = []string{"openid", "profile", "email", "offline_access", "mcp:read", "mcp:write"}

// clientCreateSpec is a client to be created, after the caller's own policy
// has been applied to it. Both the admin and the user path build one of
// these; what differs between them is what they allow into it, not what
// happens to it afterwards.
type clientCreateSpec struct {
	Name          string
	RedirectURIs  []string
	GrantTypes    []string
	ResponseTypes []string
	Scopes        []string
	// Public marks a client with no secret, for a native or browser app
	// that cannot keep one. It is refused the confidential-only grants.
	Public bool
	// Owner is the person whose client this is, or empty for the
	// deployment's own. It is an authorization column; see migration 0041.
	Owner  string
	Origin ClientOrigin
	// ServiceSubject is the identity a client_credentials token from this
	// client asserts. Operator-set only, and meaningless without that
	// grant.
	ServiceSubject string
	// Notes is the operator's annotation. Nothing reads it but the admin
	// list, which is the point: for a client created rather than
	// registered, it is where the reason it exists goes.
	Notes string
}

// needsRedirectURI reports whether a client with these grants has a browser
// flow at all. One that only acts as itself never redirects anywhere, so
// demanding a redirect URI from it would be asking for a URL that is never
// used.
func needsRedirectURI(grants []string) bool {
	for _, g := range grants {
		if g == authorizationCodeGrant || g == deviceCodeGrant || g == "refresh_token" {
			return true
		}
	}
	return false
}

// validateRedirectURIs checks a client's redirect targets. requireWeb demands
// https, or http on loopback -- the rule for a client a user registered,
// where a custom scheme would be a redirect this server cannot reason about.
//
// An operator is not held to that: a native client registered by a site may
// legitimately use its own scheme, and an operator pasting a redirect URI has
// already decided what it is for.
func validateRedirectURIs(uris []string, requireWeb bool) error {
	if len(uris) > maxRedirectURIsPerClient {
		return fmt.Errorf("at most %d redirect URIs", maxRedirectURIsPerClient)
	}
	for _, raw := range uris {
		if strings.TrimSpace(raw) != raw || raw == "" {
			return fmt.Errorf("redirect URI %q has surrounding whitespace or is empty", raw)
		}
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("redirect URI %q does not parse: %w", raw, err)
		}
		if !u.IsAbs() {
			return fmt.Errorf("redirect URI %q must be absolute", raw)
		}
		if u.Fragment != "" {
			return fmt.Errorf("redirect URI %q must not carry a fragment", raw)
		}
		if !requireWeb {
			continue
		}
		switch u.Scheme {
		case "https":
		case "http":
			// Loopback only. An http redirect anywhere else sends an
			// authorization code across the network in clear.
			if host := u.Hostname(); host != "localhost" && host != "127.0.0.1" && host != "::1" {
				return fmt.Errorf("redirect URI %q must use https (http is allowed only on loopback)", raw)
			}
		default:
			return fmt.Errorf("redirect URI %q must use https", raw)
		}
	}
	return nil
}

// validateClientScopes checks a scope list against what a client may be
// registered for at all.
func validateClientScopes(scopes []string) error {
	seen := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		if !supportedClientScopes[s] {
			return fmt.Errorf("unsupported scope %q", s)
		}
		if seen[s] {
			return fmt.Errorf("duplicate scope %q", s)
		}
		seen[s] = true
	}
	return nil
}

// createdClient is what a successful creation hands back. The secret is here
// and nowhere else: it is not stored in recoverable form and no later read
// can produce it, so a caller that loses it has to replace the client.
type createdClient struct {
	ID     string
	Secret string
}

// createOAuth2Client validates a spec, mints the credentials and stores the
// client. It is the single place a client row comes into existence outside
// dynamic registration and startup seeding.
//
// A public client gets no secret. Everything else gets one that is returned
// once and stored only as a bcrypt hash.
func (h *Handler) createOAuth2Client(ctx context.Context, spec clientCreateSpec) (*createdClient, error) {
	if h.oauth2Provider == nil {
		return nil, fmt.Errorf("OAuth2 provider is not configured")
	}
	if len(spec.Name) > maxClientNameLen {
		return nil, fmt.Errorf("name must be at most %d characters", maxClientNameLen)
	}
	if len(spec.ServiceSubject) > maxServiceSubjectLen {
		return nil, fmt.Errorf("service subject must be at most %d characters", maxServiceSubjectLen)
	}

	if len(spec.GrantTypes) == 0 {
		spec.GrantTypes = slices.Clone(defaultClientGrantTypes)
	}
	if len(spec.ResponseTypes) == 0 {
		spec.ResponseTypes = []string{"code"}
	}
	if len(spec.Scopes) == 0 {
		spec.Scopes = slices.Clone(defaultClientScopes)
	}

	if err := validateGrantTypes(spec.GrantTypes, spec.Public); err != nil {
		return nil, err
	}
	for _, rt := range spec.ResponseTypes {
		if rt != "code" {
			return nil, fmt.Errorf("unsupported response type %q", rt)
		}
	}
	if err := validateClientScopes(spec.Scopes); err != nil {
		return nil, err
	}
	if needsRedirectURI(spec.GrantTypes) && len(spec.RedirectURIs) == 0 {
		return nil, fmt.Errorf("at least one redirect URI is required for %s", strings.Join(spec.GrantTypes, ", "))
	}
	// A user's client is held to https; an operator's is not. See
	// validateRedirectURIs.
	if err := validateRedirectURIs(spec.RedirectURIs, spec.Owner != ""); err != nil {
		return nil, err
	}

	clientID := "client_" + generateRandomString(16)
	client := &fosite.DefaultClient{
		ID:            clientID,
		RedirectURIs:  spec.RedirectURIs,
		GrantTypes:    spec.GrantTypes,
		ResponseTypes: spec.ResponseTypes,
		Scopes:        spec.Scopes,
		Public:        spec.Public,
	}

	var secret string
	if !spec.Public {
		secret = generateRandomString(clientSecretBytes)
		hashed, err := bcrypt.GenerateFromPassword([]byte(secret), secretHashCost)
		if err != nil {
			return nil, fmt.Errorf("hashing the client secret: %w", err)
		}
		client.Secret = hashed
	}

	storage := h.oauth2Provider.GetStorage()
	if err := storage.CreateClient(ctx, client); err != nil {
		return nil, fmt.Errorf("storing the client: %w", err)
	}
	db := storage.GetDB()

	// The owner decides who may see and delete this client, so unlike the
	// provenance below it cannot be best-effort: a client whose owner write
	// failed is one its creator cannot list or remove, and which no admin
	// has any reason to look for. Undo the creation instead.
	if spec.Owner != "" {
		if _, err := db.ExecContext(ctx,
			`UPDATE oauth2_clients SET owner = ? WHERE id = ?`, spec.Owner, clientID); err != nil {
			if _, delErr := db.ExecContext(ctx, `DELETE FROM oauth2_clients WHERE id = ?`, clientID); delErr != nil {
				h.logger.Error(logging.DestinationHTTP,
					"Failed to remove a client whose owner could not be recorded",
					"client_id", clientID, "error", delErr)
			}
			return nil, fmt.Errorf("recording the client owner: %w", err)
		}
	}
	if spec.ServiceSubject != "" {
		if _, err := db.ExecContext(ctx,
			`UPDATE oauth2_clients SET service_subject = ? WHERE id = ?`,
			spec.ServiceSubject, clientID); err != nil {
			h.logger.Warn(logging.DestinationHTTP, "Failed to record client service subject",
				"client_id", clientID, "error", err)
		}
	}
	if spec.Notes != "" {
		if _, err := db.ExecContext(ctx,
			`UPDATE oauth2_clients SET notes = ? WHERE id = ?`,
			spec.Notes, clientID); err != nil {
			h.logger.Warn(logging.DestinationHTTP, "Failed to record client notes",
				"client_id", clientID, "error", err)
		}
	}

	// Provenance is a display field: losing it costs the admin list a label,
	// which is not worth failing a creation over.
	if err := setClientProvenance(ctx, db, clientID, spec.Name, spec.Origin); err != nil {
		h.logger.Warn(logging.DestinationHTTP, "Failed to record client provenance",
			"client_id", clientID, "error", err)
	}

	return &createdClient{ID: clientID, Secret: secret}, nil
}

// requireJSONRequest refuses a state-changing request that did not announce a
// JSON body, and reports whether the caller may proceed.
//
// This is a CSRF guard, not a politeness check. Both creation endpoints
// authenticate from the browser session cookie, so without it a form POST
// from any site would mint a client in the victim's name: a cross-site form
// can send text/plain or urlencoded with no preflight at all, but it cannot
// set application/json without one, and a preflight from an origin this
// server does not allow is refused by cors().
//
// requireSameOrigin would be the stronger check and is deliberately not used:
// it admits only this host and HTTP_API_BASE_URL, which would break the
// separately-hosted SPA that the CORS allowlist exists to support. The
// DELETE route needs nothing extra, since its method forces a preflight on
// its own.
func (h *Handler) requireJSONRequest(w http.ResponseWriter, r *http.Request) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		h.writeError(w, http.StatusUnsupportedMediaType, "Send application/json")
		return false
	}
	return true
}

// clientEndpoints is what an outside service's setup form asks for, beside
// the id and the secret: where to send the user and where to redeem the code.
//
// Served rather than built in the browser because the paths are not fixed.
// The OAuth2 endpoints sit under /mcp/oauth2/ or /oauth2/ depending on
// whether MCP is enabled (see oauth2EndpointPath), and a page that guessed
// would hand out a URL that 404s on half the deployments.
type clientEndpoints struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	// Metadata is the discovery document, for a service that can read one
	// instead of being told each URL.
	Metadata string `json:"metadata_endpoint"`
}

// oauth2ClientEndpoints reports the URLs a client is configured with.
func (h *Handler) oauth2ClientEndpoints() clientEndpoints {
	issuer := ""
	if h.oauth2Provider != nil {
		issuer = h.oauth2Provider.config.AccessTokenIssuer
	}
	return clientEndpoints{
		Issuer:                issuer,
		AuthorizationEndpoint: issuer + h.oauth2EndpointPath("authorize"),
		TokenEndpoint:         issuer + h.oauth2EndpointPath("token"),
		Metadata:              issuer + "/.well-known/oauth-authorization-server",
	}
}

// clientTokenTables are every table holding rows that reference a client,
// in the order they must be cleared: nothing in this schema has a foreign
// key, so an orphan row outlives its client and fails a later GetClient.
var clientTokenTables = []string{
	"oauth2_access_tokens",
	"oauth2_refresh_tokens",
	"oauth2_authorization_codes",
	"oauth2_oidc_sessions",
	"oauth2_pkce_requests",
	"oauth2_device_codes",
}

// deleteOAuth2Client removes a client and everything issued to it, and
// reports whether the client existed. where narrows the delete beyond the id
// -- the user-facing path passes an owner, so a request for somebody else's
// client is indistinguishable from one for a client that is not there.
//
// Shared by the admin and the owner path on purpose. The cascade is the part
// that is easy to get wrong and easy to forget to copy.
func deleteOAuth2Client(ctx context.Context, db *sql.DB, logger *logging.Logger, clientID, owner string) (bool, error) {
	query := `DELETE FROM oauth2_clients WHERE id = ?`
	args := []any{clientID}
	if owner != "" {
		query += ` AND owner = ?`
		args = append(args, owner)
	}
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}

	// After the client itself, so a failure here leaves tokens for a client
	// that is gone -- which GetClient refuses -- rather than a live client
	// whose tokens were destroyed under it.
	for _, table := range clientTokenTables {
		// gosec G202: table comes from the fixed list above, never a request.
		if _, err := db.ExecContext(ctx,
			"DELETE FROM "+table+" WHERE client_id = ?", clientID); err != nil { //nolint:gosec
			logger.Warn(logging.DestinationHTTP, "Failed to clean up tokens for a deleted client",
				"table", table, "client_id", clientID, "error", err)
		}
	}
	return true, nil
}
