package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// What the admin connectors page needs in order to stop asking an operator
// to type things the server already knows.
//
// Two endpoints, and both exist because the form used to be a row of empty
// text boxes:
//
//   - catalog: the shipped built-in definitions. connectors.BuiltinIDs has
//     carried the comment "for the admin page's picker" since it was
//     written, with nothing serving it, so the page asked for an id like
//     `builtin:gdrive` to be typed -- a namespace prefix, a provider name
//     and a colon, all of which the server can check only after the fact.
//   - discover: an issuer's own metadata. An operator reading a provider's
//     documentation for its authorization and token endpoints is
//     transcribing two URLs that the provider publishes in a machine
//     readable document; a typo in either produces a connector that fails
//     at consent time, in the user's browser, long after the form said
//     "Saved".
//
// Discovery runs HERE and not in the browser. Two reasons, either enough:
// the page's Content-Security-Policy has no connect-src and so inherits
// default-src, which stops a cross-origin fetch dead; and it is this
// server, not the operator's laptop, that has to be able to reach the
// provider for the connector to work at all. A discovery that succeeds in
// the browser and fails in the daemon is worse than no discovery.

// AdminConnectorCatalogEntry is one shipped definition as the admin page's
// picker needs it.
//
// Friendly names are deliberately absent: they live in the frontend's
// connectorNames.ts, which the file chooser and the user-facing connectors
// page already read without having fetched anything. This carries the
// things only the server knows -- what ships, what a provider can do, and
// whether this access point has already configured it.
type AdminConnectorCatalogEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	// DefaultScopes is what the shipped definition asks for, so the page
	// can say what connecting will request rather than leaving it blank.
	DefaultScopes []string `json:"default_scopes,omitempty"`
	// TransferMethod is the file transfer plugin that can use this
	// credential, empty when none ships. Empty means direct transfer is
	// not a choice the operator has, which is why the form hides it.
	TransferMethod string `json:"transfer_method,omitempty"`
	// HasPicker reports that a browser file chooser ships for this
	// provider. A chooser is configured in its own right, under
	// HTTP_API_PICKERS, and is not a property of the connector -- but
	// whether one exists at all is a fact about the provider, and the
	// form is where an operator wonders about it.
	HasPicker bool `json:"has_picker"`
	// PickerNeedsAPIKey reports that the chooser will not run without a
	// developer key, which is the part an operator cannot discover by
	// trying it: the chooser renders and then refuses.
	PickerNeedsAPIKey bool `json:"picker_needs_api_key,omitempty"`
	// Configured reports that this access point already has this
	// connector, so the picker can say so instead of offering a second
	// one that the save would refuse.
	Configured bool `json:"configured"`
	// ConfiguredOrigin says where that existing one came from, so the
	// page can distinguish "edit it below" from "change the
	// configuration and run condor_reconfig".
	ConfiguredOrigin string `json:"configured_origin,omitempty"`
}

// adminConnectorCatalog serves GET /api/v1/admin/connectors/catalog.
//
// The request is unused: the catalogue is what this build ships plus what
// this server has configured, neither of which varies by caller.
func (s *Handler) adminConnectorCatalog(w http.ResponseWriter, _ *http.Request) {
	origins := map[string]string{}
	for _, d := range s.connectorDefinitions() {
		origins[d.ID.String()] = d.Origin
	}

	out := []AdminConnectorCatalogEntry{}
	for _, id := range connectors.BuiltinIDs() {
		def, ok := connectors.Lookup(id)
		if !ok {
			// Unreachable: BuiltinIDs is derived from the same map.
			// Skipped rather than asserted, because a half-listed
			// catalogue is a worse answer than a short one.
			continue
		}
		entry := AdminConnectorCatalogEntry{
			ID:             id.String(),
			Name:           id.Name,
			Kind:           string(def.Kind),
			Summary:        def.Summary,
			DefaultScopes:  def.DefaultScopes,
			TransferMethod: def.TransferMethod,
		}
		if p, ok := connectors.LookupPickerProvider(id.Name); ok {
			entry.HasPicker = true
			entry.PickerNeedsAPIKey = p.NeedsAPIKey
		}
		if origin, found := origins[id.String()]; found {
			entry.Configured = true
			entry.ConfiguredOrigin = origin
		}
		out = append(out, entry)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"builtins": out})
}

// AdminConnectorDiscoveryRequest names an issuer to ask.
type AdminConnectorDiscoveryRequest struct {
	Issuer string `json:"issuer"`
}

// AdminConnectorDiscovery is what an issuer published about itself.
type AdminConnectorDiscovery struct {
	// Issuer as the document states it, which is not always what was
	// typed -- see the mismatch warning below.
	Issuer string `json:"issuer,omitempty"`
	// Document is the URL that answered, so an operator who gets a
	// surprising result can go and read it.
	Document         string   `json:"document"`
	AuthorizationURL string   `json:"authorization_url,omitempty"`
	TokenURL         string   `json:"token_url,omitempty"`
	ScopesSupported  []string `json:"scopes_supported,omitempty"`
	// Warnings are things the document says that will bite later. Not
	// errors: every one of them describes a provider that answered, and
	// an operator may know better than the document does.
	Warnings []string `json:"warnings,omitempty"`
}

// discoveryTimeout bounds the whole probe, not each request. An operator
// is waiting on a form button.
const discoveryTimeout = 10 * time.Second

// discoveryMaxBytes caps a metadata document. Real ones are a few KiB;
// this is room for a generous provider and not for a stream.
const discoveryMaxBytes = 1 << 20

// adminConnectorDiscover serves POST /api/v1/admin/connectors/discover.
func (s *Handler) adminConnectorDiscover(w http.ResponseWriter, r *http.Request) {
	var req AdminConnectorDiscoveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}
	issuer, err := normalizeIssuer(req.Issuer)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), discoveryTimeout)
	defer cancel()

	result, err := discoverIssuer(ctx, s.discoveryClient(), issuer)
	if err != nil {
		// 502, not 500: this server is fine and the upstream did not
		// answer usefully. The distinction matters to whoever reads the
		// log, and the message is the operator's only clue about which
		// of the two to go and fix.
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// discoveryClient is the client used to fetch metadata documents.
//
// Redirects are followed only within the same host. A redirect that leaves
// the issuer's host is how a discovery fetch becomes a way to make this
// daemon talk to something else, and nothing legitimate needs it: an
// issuer that publishes its metadata elsewhere publishes the document, not
// a hop.
func (s *Handler) discoveryClient() *http.Client {
	return &http.Client{
		Timeout: discoveryTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Hostname() != via[0].URL.Hostname() {
				return fmt.Errorf("refusing a redirect from %s to another host (%s)",
					via[0].URL.Hostname(), req.URL.Hostname())
			}
			return nil
		},
	}
}

// normalizeIssuer parses what was typed into an issuer URL, or says why it
// is not one.
//
// https is required away from loopback. The client secret this connector
// will carry goes to the token endpoint named by this document, so a
// cleartext issuer is a cleartext secret later -- and the operator typing
// it would have no reason to connect the two. Loopback is allowed because
// that is where a test provider runs.
func normalizeIssuer(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("an issuer is required")
	}
	// A bare hostname is what people type. Assume https rather than
	// refusing, since https is what we are about to insist on anyway.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q is not a URL: %w", raw, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%q names no host", raw)
	}
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf(
			"the issuer must be https (got %q); the client secret for this connector "+
				"will be sent to the token endpoint this document names", u.Scheme)
	}
	// Query and fragment are not part of an issuer identifier, and
	// carrying them into the well-known path would ask for a document
	// nobody publishes.
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

// isLoopbackHost reports whether a hostname is this machine.
func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// discoveryCandidates lists the URLs to try, in the order a real provider
// is most likely to answer.
//
// Three, because two specifications disagree about where the document
// lives and providers have picked both:
//
//   - OpenID Connect Discovery appends the well-known path to the issuer,
//     which is what Google and Microsoft serve.
//   - RFC 8414 INSERTS it between host and path, which is what Ory,
//     Keycloak realms and anything built on them serve.
//   - RFC 8414's path appended anyway, which several servers also serve
//     because everybody tries it.
//
// For an issuer with no path the first and third are the same URL; the
// duplicate is dropped so a failing provider is not reported twice.
func discoveryCandidates(issuer *url.URL) []string {
	base := *issuer
	base.Path = ""
	withPath := func(p string) string {
		u := base
		u.Path = p
		return u.String()
	}

	out := []string{withPath(issuer.Path + "/.well-known/openid-configuration")}
	if issuer.Path != "" {
		out = append(out, withPath("/.well-known/oauth-authorization-server"+issuer.Path))
	}
	appended := withPath(issuer.Path + "/.well-known/oauth-authorization-server")
	for _, have := range out {
		if have == appended {
			return out
		}
	}
	return append(out, appended)
}

// issuerMetadata is the handful of fields a connector needs out of an
// authorization server's metadata document.
type issuerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
	AuthMethodsSupported  []string `json:"token_endpoint_auth_methods_supported"`
}

// discoverIssuer fetches an issuer's metadata and reports what a connector
// can be built from it.
func discoverIssuer(ctx context.Context, client *http.Client, issuer *url.URL) (*AdminConnectorDiscovery, error) {
	var attempts []string
	for _, candidate := range discoveryCandidates(issuer) {
		doc, err := fetchIssuerMetadata(ctx, client, candidate)
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		return describeIssuer(issuer, candidate, doc), nil
	}
	return nil, fmt.Errorf("no authorization server metadata at %s. Tried %s",
		issuer, strings.Join(attempts, "; "))
}

// fetchIssuerMetadata gets and decodes one candidate URL.
func fetchIssuerMetadata(ctx context.Context, client *http.Client, u string) (*issuerMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	// A generic client name. The operator's identity is not this
	// provider's business, and a User-Agent is where it would leak.
	req.Header.Set("User-Agent", "htcondor-api/1")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, discoveryMaxBytes))
	if err != nil {
		return nil, err
	}
	var doc issuerMetadata
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the response is not a metadata document: %w", err)
	}
	// An HTML error page decodes as nothing useful rather than failing,
	// so require the one field that makes this a usable answer. Without
	// it there is nothing to put in the form.
	if doc.AuthorizationEndpoint == "" {
		return nil, errors.New("the document names no authorization_endpoint")
	}
	return &doc, nil
}

// describeIssuer turns a fetched document into the form's answer, with the
// things it says that will bite later.
func describeIssuer(issuer *url.URL, from string, doc *issuerMetadata) *AdminConnectorDiscovery {
	out := &AdminConnectorDiscovery{
		Issuer:           doc.Issuer,
		Document:         from,
		AuthorizationURL: doc.AuthorizationEndpoint,
		TokenURL:         doc.TokenEndpoint,
		ScopesSupported:  doc.ScopesSupported,
	}

	// RFC 8414 section 3.3 requires the document's issuer to equal the
	// one it was fetched for. A mismatch is how a document served from
	// the wrong place gets used, and it is also what a trailing-slash
	// typo looks like, so it is said rather than enforced.
	if doc.Issuer != "" && strings.TrimSuffix(doc.Issuer, "/") != issuer.String() {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"the document calls itself %q, not %q. Use the name it gives, or this provider "+
				"will refuse the authorization request", doc.Issuer, issuer))
	}
	if doc.TokenEndpoint == "" {
		out.Warnings = append(out.Warnings,
			"no token_endpoint, so an authorization code cannot be exchanged. "+
				"Fill it in below if the provider documents one")
	}
	for label, endpoint := range map[string]string{
		"authorization_endpoint": doc.AuthorizationEndpoint,
		"token_endpoint":         doc.TokenEndpoint,
	} {
		if endpoint == "" {
			continue
		}
		if u, err := url.Parse(endpoint); err == nil && u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"%s is %s, not https", label, u.Scheme))
		}
	}
	// We are a confidential client and send a secret. A provider that
	// accepts neither way of sending one will refuse every exchange,
	// and the refusal arrives at the token endpoint rather than here.
	if len(doc.AuthMethodsSupported) > 0 {
		secretOK := false
		for _, m := range doc.AuthMethodsSupported {
			if m == "client_secret_basic" || m == "client_secret_post" {
				secretOK = true
			}
		}
		if !secretOK {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"this provider accepts only %s at its token endpoint, and a connector "+
					"authenticates with a client secret",
				strings.Join(doc.AuthMethodsSupported, ", ")))
		}
	}
	return out
}
