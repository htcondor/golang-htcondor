package httpserver

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/htcondor/golang-htcondor/logging"
)

// The caller's own OAuth2 grants: /api/v1/oauth2/grants.
//
// What an outside app is holding right now that lets it act as this
// person. Until this existed the only listing was /admin/tokens, which
// needs the admin role, so a user who connected an assistant to their
// account could not see the grant -- let alone withdraw it -- without
// asking an operator to do it for them. A grant is the user's to
// withdraw.
//
// Grouped by CLIENT rather than listed per token, because the thing a
// person decided was "let this app in", not "issue this refresh token".
// One app that has signed in three times holds three grants and reads as
// one row; disconnecting it withdraws all three, since leaving two behind
// would be a Disconnect button that does not disconnect.
//
// SESSION ONLY, for the same reason as /api/v1/integrations: a bearer
// token that could enumerate and revoke its own siblings is a stolen
// token that can lock the owner out.

// Grant is one app's hold on the caller's account.
type Grant struct {
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name,omitempty"`
	// OwnedByCaller reports that this is a client the caller registered
	// themselves, on the connectors page. The distinction is the whole
	// reason to show this list: an app that registered itself through
	// dynamic registration appears nowhere else the user can see.
	OwnedByCaller bool `json:"owned_by_caller"`
	// Scopes is the union of what is granted across this client's
	// grants, in the order the authorizations had them.
	Scopes []string `json:"scopes,omitempty"`
	// Grants is how many authorizations are behind this row. More than
	// one is normal -- re-authorizing mints another -- and is worth
	// showing, because it is also what a client stuck in a sign-in loop
	// looks like.
	Grants int `json:"grants"`
	// Tokens counts the live token rows, access and refresh together.
	Tokens int `json:"tokens"`
	// HasRefresh reports that at least one grant can mint new access
	// tokens without the user present. That is the difference between an
	// app that stops working on its own and one that does not.
	HasRefresh bool       `json:"has_refresh"`
	FirstSeen  time.Time  `json:"first_seen"`
	LastSeen   time.Time  `json:"last_seen"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// grantRow is one token row read out of the database.
type grantRow struct {
	clientID   string
	clientName string
	owner      string
	kind       string
	requestID  string
	scopes     string
	requested  time.Time
	expires    sql.NullTime
}

// handleGrants serves GET on /api/v1/oauth2/grants.
func (h *Handler) handleGrants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, ok := h.integrationCaller(w, r)
	if !ok {
		return
	}

	rows, err := h.readGrantRows(r, session.Username)
	if err != nil {
		h.logger.Error(logging.DestinationHTTP, "Failed to list grants",
			"subject", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Failed to list grants")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"grants": groupGrants(rows, session.Username),
	})
}

// readGrantRows reads every live token belonging to one subject.
//
// granted_scopes, not scopes, for the reason the admin listing gives: the
// `scopes` column is what the client ASKED for, and showing a refused
// privilege as granted is the one mistake this page must not make.
func (h *Handler) readGrantRows(r *http.Request, subject string) ([]grantRow, error) {
	db := h.oauth2Provider.GetStorage().GetDB()
	out := []grantRow{}
	for _, t := range []struct{ table, kind string }{
		{"oauth2_access_tokens", "access"},
		{"oauth2_refresh_tokens", "refresh"},
	} {
		// gosec G202: table comes from the fixed pair above.
		q := "SELECT t.client_id, COALESCE(c.client_name, ''), COALESCE(c.owner, ''), " + //nolint:gosec
			"t.request_id, t.granted_scopes, t.requested_at, t.expires_at " +
			"FROM " + t.table + " t LEFT JOIN oauth2_clients c ON c.id = t.client_id " +
			"WHERE t.subject = ? AND t.active != 0 " +
			"AND (t.expires_at IS NULL OR t.expires_at > ?)"
		rows, err := db.QueryContext(r.Context(), q, subject, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			g := grantRow{kind: t.kind}
			if err := rows.Scan(&g.clientID, &g.clientName, &g.owner,
				&g.requestID, &g.scopes, &g.requested, &g.expires); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, g)
		}
		cerr := rows.Err()
		_ = rows.Close()
		if cerr != nil {
			return nil, cerr
		}
	}
	return out, nil
}

// groupGrants collapses token rows into one entry per client.
func groupGrants(rows []grantRow, caller string) []Grant {
	byClient := map[string]*Grant{}
	requests := map[string]map[string]bool{}
	seenScopes := map[string]map[string]bool{}

	for _, row := range rows {
		g, ok := byClient[row.clientID]
		if !ok {
			g = &Grant{
				ClientID:      row.clientID,
				ClientName:    row.clientName,
				OwnedByCaller: row.owner != "" && row.owner == caller,
				FirstSeen:     row.requested,
				LastSeen:      row.requested,
			}
			byClient[row.clientID] = g
			requests[row.clientID] = map[string]bool{}
			seenScopes[row.clientID] = map[string]bool{}
		}
		g.Tokens++
		if row.kind == "refresh" {
			g.HasRefresh = true
		}
		if row.requestID != "" {
			requests[row.clientID][row.requestID] = true
		}
		if row.requested.Before(g.FirstSeen) {
			g.FirstSeen = row.requested
		}
		if row.requested.After(g.LastSeen) {
			g.LastSeen = row.requested
		}
		// The latest expiry across the client's live tokens: the soonest
		// would read as "access ends then" while a refresh token sits
		// behind it ready to mint more.
		if row.expires.Valid && (g.ExpiresAt == nil || row.expires.Time.After(*g.ExpiresAt)) {
			when := row.expires.Time
			g.ExpiresAt = &when
		}
		// decodeStringList, not a whitespace split.
		//
		// granted_scopes is written with json.Marshal (see
		// oauth2_storage.go), so the column holds ["openid","condor:/READ"]
		// and splitting it on spaces yields the chips ["openid" and
		// "condor:/READ"] -- brackets, quotes and all, straight onto the
		// user's Connectors page. decodeStringList parses the JSON and
		// falls back to splitting only when the row is not JSON, which is
		// what the admin token listing has always done with this same
		// column.
		for _, s := range decodeStringList(row.scopes) {
			if !seenScopes[row.clientID][s] {
				seenScopes[row.clientID][s] = true
				g.Scopes = append(g.Scopes, s)
			}
		}
	}

	out := make([]Grant, 0, len(byClient))
	for id, g := range byClient {
		g.Grants = len(requests[id])
		out = append(out, *g)
	}
	// Newest authorization first, which is the order somebody checking
	// "did that just connect?" is reading for.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].ClientID < out[j].ClientID
	})
	return out
}

// grantRevokeRequest names the app to cut off.
type grantRevokeRequest struct {
	ClientID string `json:"client_id"`
}

// handleGrantRevoke serves POST /api/v1/oauth2/grants/revoke.
//
// Scoped to the caller's own subject in the UPDATE itself rather than
// checked first. A check followed by an unscoped write is a check that
// can be raced, and more to the point an unscoped write is one typo away
// from revoking a client's grants for everybody who uses it.
func (h *Handler) handleGrantRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, ok := h.integrationCaller(w, r)
	if !ok {
		return
	}
	var req grantRevokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		h.writeError(w, http.StatusBadRequest, "client_id is required")
		return
	}

	db := h.oauth2Provider.GetStorage().GetDB()
	var revoked int64
	for _, table := range []string{"oauth2_access_tokens", "oauth2_refresh_tokens"} {
		// gosec G202: table comes from the fixed pair above.
		res, err := db.ExecContext(r.Context(),
			"UPDATE "+table+" SET active = 0 WHERE client_id = ? AND subject = ? AND active = 1", //nolint:gosec
			clientID, session.Username)
		if err != nil {
			h.logger.Error(logging.DestinationHTTP, "Failed to revoke grant",
				"client_id", clientID, "subject", session.Username, "error", err)
			h.writeError(w, http.StatusInternalServerError, "Failed to revoke the grant")
			return
		}
		if n, err := res.RowsAffected(); err == nil {
			revoked += n
		}
	}

	// Worth a log line at Info: a user cutting an app off is a security
	// event somebody may have to reconstruct later, and this is the only
	// record that it was the user's own doing rather than an operator's.
	h.logger.Info(logging.DestinationHTTP, "User revoked an app's grant",
		"client_id", clientID, "subject", session.Username, "tokens", revoked)

	h.writeJSON(w, http.StatusOK, map[string]any{
		"client_id": clientID,
		"revoked":   revoked,
	})
}
