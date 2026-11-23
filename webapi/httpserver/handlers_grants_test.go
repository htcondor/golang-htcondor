package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A user's own grants: what they can see, and what they can cut off.
//
// The property these exist for is the scoping. Two users share a client --
// that is what a client is for -- and a Disconnect that withdrew the
// client's grants for everybody who uses it would be a user-facing button
// with an operator's blast radius.

// grantFixture is a server with tokens already in it.
type grantFixture struct{ *reauthFixture }

func newGrantFixture(t *testing.T) *grantFixture {
	t.Helper()
	f := newReauthFixture(t, Config{})
	t.Cleanup(func() { _ = f.server.oauth2Provider.Close() })
	return &grantFixture{f}
}

// issueToken writes one live token row, the way the token endpoint would.
//
// Written straight to the table rather than driven through a full
// authorization: what is under test is the listing and the revocation, and
// a code exchange per row would make the setup the test.
func (f *grantFixture) issueToken(t *testing.T, table, clientID, subject, requestID, scopes string, requested time.Time) {
	t.Helper()
	db := f.server.oauth2Provider.GetStorage().GetDB()
	_, err := db.ExecContext(t.Context(),
		"INSERT INTO "+table+ //nolint:gosec // G202: fixed table name from the caller
			" (signature, request_id, client_id, subject, scopes, granted_scopes, "+
			"session_data, form_data, active, requested_at, expires_at) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)",
		"sig-"+table+"-"+requestID, requestID, clientID, subject, scopes, scopes,
		"{}", "", requested, requested.Add(time.Hour))
	if err != nil {
		t.Fatalf("insert into %s: %v", table, err)
	}
}

// grantsFor lists one user's grants through the handler.
func (f *grantFixture) grantsFor(t *testing.T, username string) []Grant {
	t.Helper()
	rec := httptest.NewRecorder()
	f.server.handleGrants(rec, userRequest(t, f.server, username, []string{"condor-users"},
		http.MethodGet, "/api/v1/oauth2/grants", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list grants for %s: status %d: %s", username, rec.Code, rec.Body.String())
	}
	var out struct {
		Grants []Grant `json:"grants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode grants: %v: %s", err, rec.Body.String())
	}
	return out.Grants
}

// revokeAs disconnects a client as one user, returning the status.
func (f *grantFixture) revokeAs(t *testing.T, username, clientID string) (int, map[string]any) {
	t.Helper()
	body := `{"client_id":` + mustJSON(t, clientID) + `}`
	rec := httptest.NewRecorder()
	f.server.handleGrantRevoke(rec, userRequest(t, f.server, username, []string{"condor-users"},
		http.MethodPost, "/api/v1/oauth2/grants/revoke", body))
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

// An app that registered itself appears nowhere a user can see it. That is
// the gap this endpoint exists to close, so it is the first thing tested:
// the client was never created through /api/v1/integrations and has no
// owner, and the grant still shows up for the person it acts as.
func TestGrantsShowAnAppTheUserNeverRegistered(t *testing.T) {
	f := newGrantFixture(t)
	f.issueToken(t, "oauth2_access_tokens", "self-registered", "alice", "req-1",
		"openid mcp:read", time.Now().UTC().Add(-time.Minute))
	f.issueToken(t, "oauth2_refresh_tokens", "self-registered", "alice", "req-1",
		"openid mcp:read", time.Now().UTC().Add(-time.Minute))

	grants := f.grantsFor(t, "alice")
	if len(grants) != 1 {
		t.Fatalf("got %d grants, want 1: %+v", len(grants), grants)
	}
	g := grants[0]
	if g.ClientID != "self-registered" {
		t.Errorf("client_id = %q", g.ClientID)
	}
	if g.OwnedByCaller {
		t.Error("owned_by_caller is true for a client the user never registered")
	}
	// Two token rows, one authorization: the pairing is what a person
	// decided, and counting it as two would read as two sign-ins.
	if g.Grants != 1 {
		t.Errorf("grants = %d, want 1 (one authorization, two tokens)", g.Grants)
	}
	if g.Tokens != 2 {
		t.Errorf("tokens = %d, want 2", g.Tokens)
	}
	if !g.HasRefresh {
		t.Error("has_refresh is false although a refresh token is live")
	}
	if len(g.Scopes) != 2 || g.Scopes[0] != "openid" || g.Scopes[1] != "mcp:read" {
		t.Errorf("scopes = %v, want [openid mcp:read] in the authorization's order", g.Scopes)
	}
}

// The scoping. One client, two users, and each sees only their own hold on
// it.
func TestGrantsAreScopedToTheCaller(t *testing.T) {
	f := newGrantFixture(t)
	now := time.Now().UTC()
	f.issueToken(t, "oauth2_access_tokens", "shared-app", "alice", "req-alice", "mcp:read", now)
	f.issueToken(t, "oauth2_access_tokens", "shared-app", "bob", "req-bob", "mcp:write", now)

	alice := f.grantsFor(t, "alice")
	if len(alice) != 1 || alice[0].Scopes[0] != "mcp:read" {
		t.Fatalf("alice sees %+v, want only her own mcp:read grant", alice)
	}
	bob := f.grantsFor(t, "bob")
	if len(bob) != 1 || bob[0].Scopes[0] != "mcp:write" {
		t.Fatalf("bob sees %+v, want only his own mcp:write grant", bob)
	}
}

// Disconnecting is scoped the same way. This is the one that would be a
// real incident: alice withdrawing her own access to a shared client must
// not take bob's with it.
func TestRevokingAGrantLeavesOtherUsersAlone(t *testing.T) {
	f := newGrantFixture(t)
	now := time.Now().UTC()
	f.issueToken(t, "oauth2_access_tokens", "shared-app", "alice", "req-alice", "mcp:read", now)
	f.issueToken(t, "oauth2_refresh_tokens", "shared-app", "alice", "req-alice", "mcp:read", now)
	f.issueToken(t, "oauth2_access_tokens", "shared-app", "bob", "req-bob", "mcp:read", now)

	status, body := f.revokeAs(t, "alice", "shared-app")
	if status != http.StatusOK {
		t.Fatalf("revoke status %d: %v", status, body)
	}
	// Both of alice's rows, and only hers.
	if n, _ := body["revoked"].(float64); n != 2 {
		t.Errorf("revoked = %v, want 2 (her access and refresh tokens)", body["revoked"])
	}
	if got := f.grantsFor(t, "alice"); len(got) != 0 {
		t.Errorf("alice still holds %+v after disconnecting", got)
	}
	if got := f.grantsFor(t, "bob"); len(got) != 1 {
		t.Fatalf("bob's grant on the same client went with alice's: %+v", got)
	}
}

// The refresh token is the half that matters. Revoking only the access
// token would let the client mint another within the hour, so the pairing
// goes together -- and a test that only counted rows would pass while
// leaving the refresh token live.
func TestRevokingTakesTheRefreshTokenWithIt(t *testing.T) {
	f := newGrantFixture(t)
	now := time.Now().UTC()
	f.issueToken(t, "oauth2_access_tokens", "app", "alice", "req-1", "mcp:read", now)
	f.issueToken(t, "oauth2_refresh_tokens", "app", "alice", "req-1", "mcp:read", now)

	if _, body := f.revokeAs(t, "alice", "app"); body == nil {
		t.Fatal("no response body")
	}

	db := f.server.oauth2Provider.GetStorage().GetDB()
	for _, table := range []string{"oauth2_access_tokens", "oauth2_refresh_tokens"} {
		var live int
		err := db.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM "+table+" WHERE client_id = 'app' AND subject = 'alice' AND active = 1"). //nolint:gosec // G202: fixed table name
			Scan(&live)
		if err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if live != 0 {
			t.Errorf("%s still has %d live row(s) for alice", table, live)
		}
	}
}

// An expired or already-revoked token is not something to offer a
// Disconnect button for: it grants nothing, and listing it invites
// somebody to withdraw access that is already gone and conclude the page
// is lying when nothing changes.
func TestGrantsLeaveOutWhatIsAlreadyDead(t *testing.T) {
	f := newGrantFixture(t)
	db := f.server.oauth2Provider.GetStorage().GetDB()
	past := time.Now().UTC().Add(-2 * time.Hour)

	// Expired: requested two hours ago, so issueToken's +1h expiry is
	// already behind us.
	f.issueToken(t, "oauth2_access_tokens", "expired-app", "alice", "req-old", "mcp:read", past)
	// Revoked, but still live by the clock.
	f.issueToken(t, "oauth2_access_tokens", "revoked-app", "alice", "req-rev", "mcp:read", time.Now().UTC())
	if _, err := db.ExecContext(t.Context(),
		"UPDATE oauth2_access_tokens SET active = 0 WHERE request_id = 'req-rev'"); err != nil {
		t.Fatal(err)
	}
	// One that really is live, so the test cannot pass by returning
	// nothing at all.
	f.issueToken(t, "oauth2_access_tokens", "live-app", "alice", "req-live", "mcp:read", time.Now().UTC())

	grants := f.grantsFor(t, "alice")
	if len(grants) != 1 || grants[0].ClientID != "live-app" {
		t.Fatalf("got %+v, want only live-app", grants)
	}
}

// A client the user registered themselves is marked as such, because the
// two need different sentences: one is theirs to delete on this page, the
// other is somebody else's app to be shut out of.
func TestGrantsMarkTheCallersOwnClients(t *testing.T) {
	f := newGrantFixture(t)
	status, created := createIntegration(t, f.server, "alice", []string{"condor-users"},
		`{"name":"My script","redirect_uris":["http://localhost:9999/cb"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create integration: %d: %v", status, created)
	}
	clientID, _ := created["id"].(string)
	f.issueToken(t, "oauth2_access_tokens", clientID, "alice", "req-own", "mcp:read", time.Now().UTC())

	grants := f.grantsFor(t, "alice")
	if len(grants) != 1 {
		t.Fatalf("got %d grants, want 1", len(grants))
	}
	if !grants[0].OwnedByCaller {
		t.Error("owned_by_caller is false for a client the caller registered on this page")
	}
	if grants[0].ClientName != "My script" {
		t.Errorf("client_name = %q, want the name the user gave it", grants[0].ClientName)
	}
}

// Session only, like the rest of the integrations surface: a token that
// could enumerate and revoke its own siblings is a stolen token that locks
// the owner out.
func TestGrantsRefuseAnUnauthenticatedCaller(t *testing.T) {
	f := newGrantFixture(t)
	for _, tc := range []struct {
		name    string
		serve   func(http.ResponseWriter, *http.Request)
		method  string
		target  string
		payload string
	}{
		{"list", f.server.handleGrants, http.MethodGet, "/api/v1/oauth2/grants", ""},
		{"revoke", f.server.handleGrantRevoke, http.MethodPost,
			"/api/v1/oauth2/grants/revoke", `{"client_id":"app"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r *http.Request
			if tc.payload == "" {
				r = httptest.NewRequestWithContext(t.Context(), tc.method, tc.target, nil)
			} else {
				r = httptest.NewRequestWithContext(t.Context(), tc.method, tc.target,
					strings.NewReader(tc.payload))
				r.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			tc.serve(rec, r)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status %d, want 401: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// The routes are documented, and the schema with them. /integrations and
// /webhooks are in the spec, so the third thing on the same page being
// absent is a gap somebody integrating against this would hit.
func TestGrantsAreDocumented(t *testing.T) {
	var doc struct {
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(openAPISchema), &doc); err != nil {
		t.Fatalf("openapi does not parse: %v", err)
	}
	for _, path := range []string{"/oauth2/grants", "/oauth2/grants/revoke"} {
		if _, ok := doc.Paths[path]; !ok {
			t.Errorf("%s missing from the OpenAPI paths", path)
		}
	}
	if _, ok := doc.Components.Schemas["Grant"]; !ok {
		t.Error("the Grant schema is missing, so the listing's shape is undocumented")
	}
}
