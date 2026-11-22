package httpserver

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/htcondor/golang-htcondor/logging"
)

// The user's own OAuth2 clients: /api/v1/integrations.
//
// These are the inbound half of a connector -- an outside service holding a
// credential that lets it act as the user here -- as distinct from the
// connectors a user grants this server on THEIR accounts elsewhere. Same
// word in the UI, opposite directions, so the API paths are kept apart.
//
// SESSION ONLY, and that is the point rather than an omission. Every handler
// here authenticates from the browser session cookie and nothing else, so a
// bearer token cannot reach them: a client able to mint clients could widen
// its own reach without anybody consenting to the wider one, and a token
// stolen from a Zap would be enough to mint a replacement for itself. Minting
// a credential is a thing a person does while signed in.

// Integration is one of the caller's clients as the UI sees it. There is no
// secret field, by the same reasoning as connectors.Stored: a struct with one
// is a struct that ends up in a response the first time somebody adds an
// endpoint. The secret exists once, in the reply to the creation that minted
// it.
type Integration struct {
	ID           string     `json:"id"`
	Name         string     `json:"name,omitempty"`
	RedirectURIs []string   `json:"redirect_uris,omitempty"`
	GrantTypes   []string   `json:"grant_types,omitempty"`
	Scopes       []string   `json:"scopes,omitempty"`
	Public       bool       `json:"public"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	// HasSecret says a secret exists, without being one. A confidential
	// client always has one; it is shown only at creation, so the UI needs
	// to say "a secret exists and you cannot see it again" rather than
	// leaving a blank field that reads as "no secret".
	HasSecret bool `json:"has_secret"`
}

// integrationCreateRequest is the POST body. Everything is optional except
// the redirect URIs that the chosen grants actually need.
type integrationCreateRequest struct {
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
	Scopes       []string `json:"scopes"`
	Public       bool     `json:"public"`
}

// integrationCaller resolves the signed-in person, or writes the refusal and
// reports false.
//
// An unnamed caller is refused rather than treated as the owner of every
// client with no owner. That is the fail-closed half of the identity model:
// this server does not verify bearer JWT signatures, so a name is only a
// name once something has vouched for it -- and here that something is the
// session store, which issued the cookie itself.
func (h *Handler) integrationCaller(w http.ResponseWriter, r *http.Request) (*SessionData, bool) {
	session, ok := h.getSessionFromRequest(r)
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "Sign-in required")
		return nil, false
	}
	if session.Username == "" {
		h.writeError(w, http.StatusForbidden, "This session has no local account, so it cannot own an integration")
		return nil, false
	}
	if h.oauth2Provider == nil {
		h.writeError(w, http.StatusServiceUnavailable, "OAuth2 is not configured on this server")
		return nil, false
	}
	return session, true
}

// handleIntegrations serves GET (list) and POST (create) on
// /api/v1/integrations.
func (h *Handler) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleListIntegrations(w, r)
	case http.MethodPost:
		h.handleCreateIntegration(w, r)
	default:
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleListIntegrations lists the caller's own clients, newest first.
func (h *Handler) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	session, ok := h.integrationCaller(w, r)
	if !ok {
		return
	}

	// So a client that obtained a token moments ago does not read as never
	// used, which is the first thing somebody checks after wiring one up.
	h.clientUsage.FlushNow(r.Context())

	rows, err := h.oauth2Provider.GetStorage().GetDB().QueryContext(r.Context(),
		`SELECT id, redirect_uris, grant_types, scopes, public, created_at,
		        client_name, last_used_at, client_secret
		   FROM oauth2_clients
		  WHERE owner = ?
		  ORDER BY created_at DESC`, session.Username)
	if err != nil {
		h.logger.Error(logging.DestinationHTTP, "Failed to list integrations",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Failed to list integrations")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []Integration{}
	for rows.Next() {
		var (
			it                           Integration
			redirectURIs, grants, scopes string
			public                       int
			name, secret                 string
			lastUsed                     sql.NullTime
		)
		if err := rows.Scan(&it.ID, &redirectURIs, &grants, &scopes, &public,
			&it.CreatedAt, &name, &lastUsed, &secret); err != nil {
			h.logger.Warn(logging.DestinationHTTP, "Skipping malformed client row",
				"owner", session.Username, "error", err)
			continue
		}
		it.RedirectURIs = decodeStringList(redirectURIs)
		it.GrantTypes = decodeStringList(grants)
		it.Scopes = decodeStringList(scopes)
		it.Public = public != 0
		it.Name = name
		it.HasSecret = secret != ""
		if lastUsed.Valid {
			t := lastUsed.Time
			it.LastUsedAt = &t
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error(logging.DestinationHTTP, "Failed to read integration rows",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Failed to list integrations")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"integrations": out,
		// Alongside rather than per-client: every client is configured
		// with the same endpoints, and the page needs them whether or not
		// the caller holds any.
		"endpoints": h.oauth2ClientEndpoints(),
	})
}

// handleCreateIntegration creates a client the caller owns.
//
// Two bounds are applied here that the admin path does not apply, and both
// are about what a person may mint for themselves:
//
//   - the grants are restricted to the flows with a person on the other end
//     (selfRegistrableGrantTypes), the same set dynamic registration allows.
//     client_credentials and token exchange act with nobody present, which is
//     a decision about the deployment rather than about one user's jobs.
//   - the scope list is intersected with what this caller could consent to,
//     so a client cannot be registered for a scope its creator has no claim
//     to. Consent narrows it again later; this stops the list a client
//     advertises from being an aspiration.
func (h *Handler) handleCreateIntegration(w http.ResponseWriter, r *http.Request) {
	session, ok := h.integrationCaller(w, r)
	if !ok {
		return
	}

	if !h.requireJSONRequest(w, r) {
		return
	}
	setBodyLimit(w, r, 64<<10)
	var req integrationCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		h.writeError(w, http.StatusBadRequest, "A name is required, so you can tell your integrations apart")
		return
	}

	db := h.oauth2Provider.GetStorage().GetDB()
	var held int
	if err := db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM oauth2_clients WHERE owner = ?`, session.Username).Scan(&held); err != nil {
		h.logger.Error(logging.DestinationHTTP, "Failed to count integrations",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Failed to create the integration")
		return
	}
	if held >= maxClientsPerOwner {
		h.writeError(w, http.StatusConflict,
			"You already hold the maximum number of integrations. Delete one to create another.")
		return
	}

	grants := req.GrantTypes
	if len(grants) == 0 {
		grants = defaultClientGrantTypes
	}
	for _, g := range grants {
		if !selfRegistrableGrantTypes[g] {
			h.writeError(w, http.StatusForbidden,
				"The grant type "+g+" acts with no user present, so an administrator has to provision it")
			return
		}
	}

	requested := req.Scopes
	if len(requested) == 0 {
		requested = defaultClientScopes
	}
	if err := validateClientScopes(requested); err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// What this caller could consent to today. offline_access is not a
	// permission over anything -- it asks for a refresh token -- and the
	// group policy has no opinion on it, so it is carried through rather
	// than filtered out; without it on the client no refresh token is ever
	// issued and an unattended integration cannot work at all.
	allowed := h.getScopesForGroups(session.Groups, requested)
	scopes := make([]string, 0, len(requested))
	for _, s := range requested {
		if s == "offline_access" || containsString(allowed, s) {
			scopes = append(scopes, s)
		}
	}
	if len(scopes) == 0 {
		h.writeError(w, http.StatusForbidden,
			"None of the requested scopes are available to your account")
		return
	}

	created, err := h.createOAuth2Client(r.Context(), clientCreateSpec{
		Name:         strings.TrimSpace(req.Name),
		RedirectURIs: req.RedirectURIs,
		GrantTypes:   grants,
		Scopes:       scopes,
		Public:       req.Public,
		Owner:        session.Username,
		Origin:       ClientOriginUser,
	})
	if err != nil {
		h.logger.Warn(logging.DestinationHTTP, "Refused to create an integration",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.logger.Info(logging.DestinationHTTP, "User created an OAuth2 integration",
		"owner", session.Username, "client_id", created.ID, "name", req.Name,
		"grant_types", strings.Join(grants, ","), "scopes", strings.Join(scopes, ","))

	// The secret appears here and never again. The UI says so, because a
	// caller that closes the dialog without copying it has to replace the
	// client, not recover it.
	resp := map[string]any{
		"id":            created.ID,
		"name":          strings.TrimSpace(req.Name),
		"redirect_uris": req.RedirectURIs,
		"grant_types":   grants,
		"scopes":        scopes,
		"public":        req.Public,
	}
	if created.Secret != "" {
		resp["client_secret"] = created.Secret
		resp["secret_shown_once"] = true
	}
	resp["endpoints"] = h.oauth2ClientEndpoints()
	h.writeJSON(w, http.StatusCreated, resp)
}

// handleIntegrationItem serves DELETE /api/v1/integrations/{id}.
func (h *Handler) handleIntegrationItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, ok := h.integrationCaller(w, r)
	if !ok {
		return
	}
	clientID := strings.TrimPrefix(r.URL.Path, "/api/v1/integrations/")
	if clientID == "" || strings.Contains(clientID, "/") {
		h.writeError(w, http.StatusBadRequest, "Invalid integration id")
		return
	}

	// Scoped to the owner inside the statement, so somebody else's client
	// is not found rather than refused: a 403 here would confirm that a
	// client id exists, which is the one thing an id alone should not tell
	// a caller who does not hold it.
	existed, err := deleteOAuth2Client(r.Context(), h.oauth2Provider.GetStorage().GetDB(),
		h.logger, clientID, session.Username)
	if err != nil {
		h.logger.Error(logging.DestinationHTTP, "Failed to delete an integration",
			"owner", session.Username, "client_id", clientID, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Failed to delete the integration")
		return
	}
	if !existed {
		h.writeError(w, http.StatusNotFound, "No such integration")
		return
	}
	h.logger.Info(logging.DestinationHTTP, "User deleted an OAuth2 integration",
		"owner", session.Username, "client_id", clientID)
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": clientID})
}
