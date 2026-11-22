package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// The /api/v1/connect endpoints: where a user grants this server access
// to a third-party account, so a job can use the credential.
//
// See design_notes/CONNECTORS.md. The property these handlers exist to
// preserve is that the entry point carries NO AUTHORITY: a fixed,
// per-connector URL is safe to print in a tool result, in a log or in
// documentation, because without an authenticated browser session it
// yields a login redirect rather than a credential.

// connectPathPrefix is the fixed part of every connector URL.
const connectPathPrefix = "/api/v1/connect"

// ConnectorStatus is one connector as the listing reports it.
type ConnectorStatus struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Connected is whether this caller has granted it.
	Connected bool `json:"connected"`
	// NeedsConsent is set when a grant exists but the provider will no
	// longer renew it, which is a different thing from never having
	// connected and wants a different sentence in a UI.
	NeedsConsent bool       `json:"needs_consent,omitempty"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	// ConnectURL is the fixed entry point. Safe to print.
	ConnectURL string `json:"connect_url"`
	// Transfer is spool or direct.
	Transfer string `json:"transfer"`
}

// connectCaller is the authenticated browser session, or nothing.
//
// ONLY the session cookie is consulted, deliberately, and for the same
// reason site apps consults only the cookie: a browser is the only
// thing that can complete any of these flows. A bearer token or an API
// key arriving here is a client doing something these endpoints were not
// designed for, and treating one as a login would hand a user's
// third-party credential to whatever held it.
func (h *Handler) connectCaller(r *http.Request) (*SessionData, bool) {
	session, ok := h.getSessionFromRequest(r)
	if !ok || session.Username == "" {
		return nil, false
	}
	return session, true
}

// requireConnectSession answers an unauthenticated caller and reports
// whether the handler should go on.
//
// A browser is sent to log in and come back; anything else gets a 401.
// The difference matters: redirecting an API client to an HTML login
// page produces a 200 full of markup, which is the least diagnosable
// failure available.
func (h *Handler) requireConnectSession(w http.ResponseWriter, r *http.Request) (*SessionData, bool) {
	if session, ok := h.connectCaller(r); ok {
		return session, true
	}
	if wantsHTML(r) {
		target := r.URL.Path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, "/login?return_to="+url.QueryEscape(target), http.StatusFound)
		return nil, false
	}
	h.writeError(w, http.StatusUnauthorized, "Sign in to use connectors")
	return nil, false
}

// wantsHTML reports whether this looks like a browser following a link
// rather than a program calling an API.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// handleConnectCollection answers GET /api/v1/connect.
//
// A connector this caller may not use is not listed at all. Otherwise
// the page offers a button that will refuse, which is a worse answer
// than not offering it.
func (h *Handler) handleConnectCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, ok := h.requireConnectSession(w, r)
	if !ok {
		return
	}

	defs := h.connectorDefinitions()
	out := make([]ConnectorStatus, 0, len(defs))
	for _, d := range defs {
		if !d.Enabled || !d.Permits(session.Groups) {
			continue
		}
		st := ConnectorStatus{
			ID:         d.ID.String(),
			Kind:       string(d.Kind),
			ConnectURL: connectURL(d.ID),
			Transfer:   string(d.Transfer),
		}
		if cred, found, err := h.connectorStore().Credential(r.Context(), session.Username, d.ID); err == nil && found {
			st.Connected = true
			if !cred.LastUsedAt.IsZero() {
				used := cred.LastUsedAt
				st.LastUsedAt = &used
			}
		}
		out = append(out, st)
	}
	h.writeJSON(w, http.StatusOK, out)
}

// handleConnectItem answers the per-connector paths:
//
//	GET    /api/v1/connect/{ns}:{name}           begin a flow
//	GET    /api/v1/connect/{ns}:{name}/callback  finish one
//	DELETE /api/v1/connect/{ns}:{name}           forget this user's grant
func (h *Handler) handleConnectItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, connectPathPrefix+"/")
	idPart, tail, _ := strings.Cut(rest, "/")

	// The connector id is in the path; the user NEVER is. The identity
	// comes from the session, and the moment a parameter could name a
	// user this becomes privilege escalation.
	id, err := connectors.ParseID(idPart)
	if err != nil {
		h.writeError(w, http.StatusNotFound, err.Error())
		return
	}

	session, ok := h.requireConnectSession(w, r)
	if !ok {
		return
	}

	def, found := h.connectorDefinition(id)
	if !found || !def.Enabled {
		h.writeError(w, http.StatusNotFound, fmt.Sprintf("No connector %q is configured", id))
		return
	}
	if !def.Permits(session.Groups) {
		// Not 403-with-detail: a caller outside the groups learns only
		// that it is not available to them, not who it IS available to.
		h.writeError(w, http.StatusForbidden,
			fmt.Sprintf("Connector %q is not available to you", id))
		return
	}

	switch tail {
	case "":
		if r.Method == http.MethodDelete {
			h.disconnect(w, r, session, def)
			return
		}
		h.beginConnect(w, r, session, def)
	case "callback":
		h.finishConnect(w, r, session, def)
	case "token":
		h.connectorToken(w, r, session, def)
	default:
		h.writeError(w, http.StatusNotFound, "Not found")
	}
}

// disconnect forgets this caller's grant on a connector.
//
// The store has always been able to do this -- DeleteCredential is what a
// revocation at the provider amounts to on our side -- and nothing exposed
// it, so a user who connected an account could not take it back from any
// page. Revoking at the provider is the other half and is theirs to do; this
// is the half we own.
//
// The identity is the session's, as everywhere else here: the connector id is
// in the path and the user never is. A parameter that could name a user would
// make this "forget somebody else's credential".
//
// Idempotent. A connector this caller has not connected answers 200 rather
// than 404: the caller asked for it to be gone and it is gone, and a 404
// would make a double-click look like a failure.
func (h *Handler) disconnect(w http.ResponseWriter, r *http.Request, session *SessionData, def connectors.Definition) {
	store := h.connectorStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Connectors are not available on this server")
		return
	}
	if err := store.DeleteCredential(r.Context(), session.Username, def.ID); err != nil {
		h.logger.Error(logging.DestinationHTTP, "could not forget a connector credential",
			"connector", def.ID.String(), "user", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Could not disconnect this account")
		return
	}
	h.logger.Info(logging.DestinationHTTP, "a user disconnected an account",
		"connector", def.ID.String(), "user", session.Username)
	h.writeJSON(w, http.StatusOK, map[string]string{
		"status":    "disconnected",
		"connector": def.ID.String(),
	})
}

// beginConnect sends the browser to the provider.
func (h *Handler) beginConnect(w http.ResponseWriter, r *http.Request, session *SessionData, def connectors.Definition) {
	if def.Kind != connectors.KindOAuth2 {
		// credd and vault connectors are designed for and not built.
		// Saying so beats a redirect to nowhere.
		h.writeError(w, http.StatusNotImplemented,
			fmt.Sprintf("Connector %q is of kind %q, which this server cannot yet bootstrap",
				def.ID, def.Kind))
		return
	}

	secret, err := h.connectorSecret(r, def)
	if err != nil {
		h.logger.Error(logging.DestinationHTTP, "connector has no usable client secret",
			"connector", def.ID.String(), "error", err)
		h.writeError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("Connector %q is not usable; its client secret could not be read", def.ID))
		return
	}

	authURL, err := h.connectorFlow().Begin(def, secret, session.Username, h.connectorRedirectURI(r, def.ID))
	if err != nil {
		h.logger.Error(logging.DestinationHTTP, "could not begin a connector flow",
			"connector", def.ID.String(), "user", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Could not start this authorization")
		return
	}

	// The 302 target comes from the connector's own definition, never
	// from a request parameter. This endpoint is shaped exactly like an
	// open redirect and must not be one; see
	// TestRedirectTargetCannotComeFromTheRequest, which drives four
	// likely parameter names at it.
	//
	//nolint:gosec // G710: authURL is built by Flow.Begin from the
	// connector's own AuthorizationURL, which comes from configuration
	// or the builtin catalogue. Nothing from this request reaches it;
	// taint analysis cannot see through Begin.
	http.Redirect(w, r, authURL, http.StatusFound)
}

// finishConnect completes a flow and stores the grant.
func (h *Handler) finishConnect(w http.ResponseWriter, r *http.Request, session *SessionData, def connectors.Definition) {
	q := r.URL.Query()

	// The provider reporting a refusal is the ordinary case of a person
	// clicking cancel, not an error to log loudly.
	if provErr := q.Get("error"); provErr != "" {
		h.writeError(w, http.StatusBadRequest,
			fmt.Sprintf("The provider did not authorize this: %s", provErr))
		return
	}

	secret, err := h.connectorSecret(r, def)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("Connector %q is not usable; its client secret could not be read", def.ID))
		return
	}

	grant, err := h.connectorFlow().Complete(r.Context(), def, secret, session.Username,
		q.Get("state"), q.Get("code"), h.connectorRedirectURI(r, def.ID))
	if err != nil {
		// The detail goes to the log. A caller presenting somebody
		// else's state learns only that it did not work.
		h.logger.Warn(logging.DestinationHTTP, "a connector authorization did not complete",
			"connector", def.ID.String(), "user", session.Username, "error", err)
		switch {
		case errors.Is(err, connectors.ErrNoRefreshToken):
			h.writeError(w, http.StatusBadGateway,
				"The provider returned no durable credential. Try again, and if it "+
					"recurs the connector's configuration needs checking.")
		default:
			h.writeError(w, http.StatusBadRequest,
				"This authorization could not be completed. Start it again.")
		}
		return
	}

	cred := connectors.Credential{
		Subject:         session.Username,
		Connector:       def.ID,
		ProviderSubject: grant.ProviderSubject,
		GrantedScopes:   grant.GrantedScopes,
	}
	if err := h.connectorStore().PutCredential(r.Context(), cred, []byte(grant.RefreshToken)); err != nil {
		h.logger.Error(logging.DestinationHTTP, "could not store a connector credential",
			"connector", def.ID.String(), "user", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Could not store this credential")
		return
	}

	h.logger.Info(logging.DestinationHTTP, "a user connected an account",
		"connector", def.ID.String(), "user", session.Username,
		"scopes", strings.Join(grant.GrantedScopes, " "))

	// A browser that followed a link gets taken somewhere; anything
	// else gets the fact.
	//
	// The connectors page, not the dashboard: this tab exists only to
	// authorize, and the dashboard says nothing about whether that
	// worked. The connectors page lists the account as connected, which
	// is the one thing the person wants to see before closing the tab.
	//
	// Deliberately a fixed path and not a return_to parameter. This
	// endpoint is shaped like an open redirect, and
	// TestRedirectTargetCannotComeFromTheRequest exists to keep it from
	// becoming one; the page that starts the flow opens it in a new tab
	// and keeps its own state, so nothing needs steering back.
	if wantsHTML(r) {
		http.Redirect(w, r, "/connectors?connected="+url.QueryEscape(def.ID.String()),
			http.StatusFound)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"connector": def.ID.String(),
		"connected": true,
		"scopes":    grant.GrantedScopes,
	})
}

// connectURL is the fixed entry point for a connector.
//
// A connector id is a valid URL on its own -- url.Parse("builtin:gdrive")
// gives scheme "builtin" -- so it is only ever a path SEGMENT, which is
// what this function exists to guarantee at the one place links are
// built.
func connectURL(id connectors.ID) string {
	return connectPathPrefix + "/" + id.String()
}

// connectorRedirectURI is where the provider sends the browser back.
//
// It has to be the same string at Begin and at Complete, and it has to
// match what is registered with the provider, so it is derived in one
// place from the server's configured base URL rather than from the
// request's Host -- a redirect_uri taken from a header is one a caller
// can choose.
func (h *Handler) connectorRedirectURI(r *http.Request, id connectors.ID) string {
	base := strings.TrimSuffix(h.publicBaseURLFor(r), "/")
	return base + connectURL(id) + "/callback"
}

// connectorToken answers GET /api/v1/connect/{ns}:{name}/token with a
// short-lived access token for the caller's own browser.
//
// This exists because Google's Picker runs client-side: the page itself
// has to hold a token to show a file chooser. It is the ONE place a
// third-party credential leaves this server, and it therefore carries
// the sharpest rule in the design.
//
// A browser session is the only thing that may ask. The shared gate
// above already refuses a caller with no session, but that is not quite
// enough here: a client could hold both a session cookie and a bearer
// token, and the useful reading of that is "a program, not a person's
// browser tab". So a request carrying its own Authorization is refused
// outright, whatever else it carries. The cost of being wrong in the
// other direction is handing a user's Google token to whatever asked.
func (h *Handler) connectorToken(w http.ResponseWriter, r *http.Request, session *SessionData, def connectors.Definition) {
	if r.Header.Get("Authorization") != "" {
		h.logger.Warn(logging.DestinationHTTP,
			"refused a connector token to a request carrying its own credential",
			"connector", def.ID.String(), "user", session.Username)
		h.writeError(w, http.StatusForbidden,
			"This endpoint serves a browser session only. A client holding its own "+
				"credential cannot ask for somebody's third-party token.")
		return
	}
	if def.Kind != connectors.KindOAuth2 {
		h.writeError(w, http.StatusNotImplemented,
			fmt.Sprintf("Connector %q is of kind %q and holds no token here", def.ID, def.Kind))
		return
	}

	store := h.connectorStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, "No credential store is configured")
		return
	}
	refresh, err := store.RefreshToken(r.Context(), session.Username, def.ID)
	if err != nil {
		// Not connected is the ordinary case and gets an answer the
		// page can act on, rather than an error it has to interpret.
		h.writeJSON(w, http.StatusConflict, map[string]any{
			"connector":   def.ID.String(),
			"connected":   false,
			"connect_url": connectURL(def.ID),
		})
		return
	}

	secret, err := h.connectorSecret(r, def)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("Connector %q is not usable; its client secret could not be read", def.ID))
		return
	}

	tok, err := h.connectorFlow().Refresh(r.Context(), def, secret, string(refresh))
	if err != nil {
		if errors.Is(err, connectors.ErrNeedsConsent) {
			// The grant is dead. Say so with somewhere to go, and
			// leave the row alone: a user may be outside the group
			// this week and back next, and destroying the token would
			// force a consent screen for what may be a temporary
			// thing.
			h.writeJSON(w, http.StatusConflict, map[string]any{
				"connector":     def.ID.String(),
				"connected":     false,
				"needs_consent": true,
				"connect_url":   connectURL(def.ID),
			})
			return
		}
		h.logger.Warn(logging.DestinationHTTP, "could not refresh a connector credential",
			"connector", def.ID.String(), "user", session.Username, "error", err)
		h.writeError(w, http.StatusBadGateway, "The provider could not be reached")
		return
	}

	if err := store.MarkUsed(r.Context(), session.Username, def.ID); err != nil {
		// Worth a line and not worth failing over: the token is good,
		// and the only loss is the freshness of a report.
		h.logger.Warn(logging.DestinationHTTP, "could not record connector use",
			"connector", def.ID.String(), "user", session.Username, "error", err)
	}

	// Logged because this is the egress point. The token is not in the
	// line; its existence is.
	h.logger.Info(logging.DestinationHTTP, "issued a connector token to a browser session",
		"connector", def.ID.String(), "user", session.Username)

	// No caching anywhere: this is a bearer credential with a short
	// life, and a proxy keeping a copy would hand it to the next
	// person through.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")

	expiresIn := 0
	if !tok.Expiry.IsZero() {
		if d := int(time.Until(tok.Expiry).Seconds()); d > 0 {
			expiresIn = d
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"access_token": tok.Token,
		"expires_in":   expiresIn,
		"connector":    def.ID.String(),
	})
}
