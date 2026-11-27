package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A user's own OAuth2 clients: what they may mint, what they may see, and
// what they may remove.

// userRequest builds a request carrying an ordinary user's web session, with
// the JSON content type the mutating endpoints require.
func userRequest(t *testing.T, server *Server, username string, groups []string, method, target, body string) *http.Request {
	t.Helper()
	sid, _, err := server.sessionStore.Create(username, groups)
	if err != nil {
		t.Fatalf("session create: %v", err)
	}
	var r *http.Request
	if body == "" {
		r = httptest.NewRequestWithContext(t.Context(), method, target, nil)
	} else {
		r = httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid}) //nolint:gosec // test cookie
	return r
}

// serveIntegrations dispatches to whichever of the two handlers the path
// names, the way the mux does, and returns the recorder.
func serveIntegrations(t *testing.T, server *Server, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if strings.HasPrefix(r.URL.Path, "/api/v1/integrations/") {
		server.handleIntegrationItem(rec, r)
	} else {
		server.handleIntegrations(rec, r)
	}
	return rec
}

// createIntegration posts a creation and returns status and decoded body.
func createIntegration(t *testing.T, server *Server, username string, groups []string, body string) (int, map[string]any) {
	t.Helper()
	rec := serveIntegrations(t, server, userRequest(t, server, username, groups,
		http.MethodPost, "/api/v1/integrations", body))
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode create response (%d): %v: %s", rec.Code, err, rec.Body.String())
		}
	}
	return rec.Code, out
}

// listIntegrations returns one user's own clients.
func listIntegrations(t *testing.T, server *Server, username string, groups []string) []map[string]any {
	t.Helper()
	rec := serveIntegrations(t, server, userRequest(t, server, username, groups,
		http.MethodGet, "/api/v1/integrations", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Integrations []map[string]any `json:"integrations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v: %s", err, rec.Body.String())
	}
	return out.Integrations
}

// TestUserCreatesAndListsIntegration is the happy path: a user mints a
// client, is shown its secret exactly once, and sees it in their own list and
// in nobody else's.
func TestUserCreatesAndListsIntegration(t *testing.T) {
	f := newReauthFixture(t, Config{})

	status, body := createIntegration(t, f.server, "alice", []string{"condor-users"},
		`{"name":"Zapier","redirect_uris":["https://hooks.zapier.com/callback"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create status %d, want 201: %v", status, body)
	}
	secret, _ := body["client_secret"].(string)
	if secret == "" {
		t.Fatal("a confidential client was created without returning a secret")
	}
	clientID, _ := body["id"].(string)
	if clientID == "" {
		t.Fatal("create returned no client id")
	}

	mine := listIntegrations(t, f.server, "alice", []string{"condor-users"})
	if len(mine) != 1 {
		t.Fatalf("alice sees %d integrations, want 1", len(mine))
	}
	if got, _ := mine[0]["id"].(string); got != clientID {
		t.Errorf("listed id = %q, want %q", got, clientID)
	}
	if got, _ := mine[0]["name"].(string); got != "Zapier" {
		t.Errorf("listed name = %q, want Zapier", got)
	}
	if has, _ := mine[0]["has_secret"].(bool); !has {
		t.Error("has_secret is false for a confidential client")
	}
	// The secret is returned once and never stored in recoverable form, so
	// no later read may produce it.
	if _, leaked := mine[0]["client_secret"]; leaked {
		t.Error("the listing returned a client secret")
	}

	// Bob's own integration, so the isolation is checked in both directions
	// rather than only from alice's side.
	status, body = createIntegration(t, f.server, "bob", []string{"condor-users"},
		`{"name":"Bob's thing","redirect_uris":["https://example.org/cb"]}`)
	if status != http.StatusCreated {
		t.Fatalf("bob's create status %d, want 201: %v", status, body)
	}
	bobID, _ := body["id"].(string)

	alices := listIntegrations(t, f.server, "alice", []string{"condor-users"})
	if len(alices) != 1 || alices[0]["id"] != clientID {
		t.Errorf("alice sees %d integrations, want only her own", len(alices))
	}
	bobs := listIntegrations(t, f.server, "bob", []string{"condor-users"})
	if len(bobs) != 1 || bobs[0]["id"] != bobID {
		t.Errorf("bob sees %d integrations, want only his own", len(bobs))
	}
}

// TestIntegrationRefusesServiceGrant covers the bound on what a user may mint
// for themselves: not a credential that acts while they are not there.
func TestIntegrationRefusesServiceGrant(t *testing.T) {
	f := newReauthFixture(t, Config{})

	for _, grant := range []string{"client_credentials", tokenExchangeGrantType} {
		t.Run(grant, func(t *testing.T) {
			status, body := createIntegration(t, f.server, "alice", []string{"condor-users"},
				fmt.Sprintf(`{"name":"svc","grant_types":[%q]}`, grant))
			if status != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %v", status, body)
			}
		})
	}
}

// TestIntegrationScopesBoundedByCreator covers the other bound: a client
// cannot be registered for a scope its creator has no claim to, so the list
// it advertises is not a wish.
func TestIntegrationScopesBoundedByCreator(t *testing.T) {
	f := newReauthFixture(t, Config{})
	// With an admin group configured and alice outside it, mcp:admin is a
	// scope the policy will not grant her.
	f.server.anyUserRead = newPrincipalSet("condor-admins", "")

	status, body := createIntegration(t, f.server, "alice", []string{"condor-users"},
		`{"name":"reach","redirect_uris":["https://example.org/cb"],"scopes":["mcp:read","mcp:admin"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create status %d, want 201: %v", status, body)
	}

	mine := listIntegrations(t, f.server, "alice", []string{"condor-users"})
	if len(mine) != 1 {
		t.Fatalf("got %d integrations, want 1", len(mine))
	}
	var scopes []string
	for _, s := range mine[0]["scopes"].([]any) {
		scopes = append(scopes, s.(string))
	}
	if containsString(scopes, "mcp:admin") {
		t.Errorf("client registered for mcp:admin by a creator who cannot hold it: %v", scopes)
	}
	if !containsString(scopes, "mcp:read") {
		t.Errorf("mcp:read was dropped: %v", scopes)
	}
}

// TestIntegrationDeleteIsOwnerScoped checks that somebody else's client is
// not found rather than refused -- a 403 would confirm the id exists.
func TestIntegrationDeleteIsOwnerScoped(t *testing.T) {
	f := newReauthFixture(t, Config{})

	status, body := createIntegration(t, f.server, "alice", []string{"condor-users"},
		`{"name":"mine","redirect_uris":["https://example.org/cb"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create status %d: %v", status, body)
	}
	clientID, _ := body["id"].(string)

	rec := serveIntegrations(t, f.server, userRequest(t, f.server, "bob", []string{"condor-users"},
		http.MethodDelete, "/api/v1/integrations/"+clientID, ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("bob deleting alice's client: status %d, want 404", rec.Code)
	}
	if len(listIntegrations(t, f.server, "alice", []string{"condor-users"})) != 1 {
		t.Fatal("alice's client did not survive bob's delete")
	}

	rec = serveIntegrations(t, f.server, userRequest(t, f.server, "alice", []string{"condor-users"},
		http.MethodDelete, "/api/v1/integrations/"+clientID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner delete: status %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(listIntegrations(t, f.server, "alice", []string{"condor-users"})); n != 0 {
		t.Errorf("alice still sees %d integrations after deleting her only one", n)
	}
}

// TestIntegrationCreateRequiresJSON is the CSRF guard. A cross-site form can
// POST text/plain with no preflight; it cannot send application/json without
// one.
func TestIntegrationCreateRequiresJSON(t *testing.T) {
	f := newReauthFixture(t, Config{})

	sid, _, err := f.server.sessionStore.Create("alice", []string{"condor-users"})
	if err != nil {
		t.Fatalf("session create: %v", err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/integrations",
		strings.NewReader(`{"name":"forged"}`))
	r.Header.Set("Content-Type", "text/plain")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid}) //nolint:gosec // test cookie

	rec := serveIntegrations(t, f.server, r)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d, want 415: %s", rec.Code, rec.Body.String())
	}
	if n := len(listIntegrations(t, f.server, "alice", []string{"condor-users"})); n != 0 {
		t.Errorf("a form-shaped POST created %d clients", n)
	}
}

// TestIntegrationRefusesAnonymous covers the fail-closed half of the identity
// model: no session, no client.
func TestIntegrationRefusesAnonymous(t *testing.T) {
	f := newReauthFixture(t, Config{})

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/integrations",
		strings.NewReader(`{"name":"nobody"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := serveIntegrations(t, f.server, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// TestIntegrationRejectsInsecureRedirect: an authorization code must not
// travel to an http URL that is not loopback.
func TestIntegrationRejectsInsecureRedirect(t *testing.T) {
	f := newReauthFixture(t, Config{})

	for _, tc := range []struct {
		name, uri string
		wantOK    bool
	}{
		{"plain http elsewhere", "http://example.org/cb", false},
		{"https", "https://example.org/cb", true},
		{"http on loopback", "http://localhost:3000/cb", true},
		{"custom scheme", "myapp://cb", false},
		{"relative", "/cb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := createIntegration(t, f.server, "alice", []string{"condor-users"},
				fmt.Sprintf(`{"name":%q,"redirect_uris":[%q]}`, tc.name, tc.uri))
			if tc.wantOK && status != http.StatusCreated {
				t.Fatalf("status %d, want 201: %v", status, body)
			}
			if !tc.wantOK && status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %v", status, body)
			}
		})
	}
}

// TestIntegrationQuota bounds how many clients one session can mint.
func TestIntegrationQuota(t *testing.T) {
	f := newReauthFixture(t, Config{})

	for i := 0; i < maxClientsPerOwner; i++ {
		status, body := createIntegration(t, f.server, "alice", []string{"condor-users"},
			fmt.Sprintf(`{"name":"n%d","redirect_uris":["https://example.org/cb"]}`, i))
		if status != http.StatusCreated {
			t.Fatalf("create %d: status %d: %v", i, status, body)
		}
	}
	status, body := createIntegration(t, f.server, "alice", []string{"condor-users"},
		`{"name":"one too many","redirect_uris":["https://example.org/cb"]}`)
	if status != http.StatusConflict {
		t.Fatalf("status %d, want 409: %v", status, body)
	}
}

// TestIntegrationPublicClientGetsNoSecret: a native or browser app that
// cannot keep a secret is not given one.
func TestIntegrationPublicClientGetsNoSecret(t *testing.T) {
	f := newReauthFixture(t, Config{})

	status, body := createIntegration(t, f.server, "alice", []string{"condor-users"},
		`{"name":"cli","public":true,"redirect_uris":["http://127.0.0.1:8111/cb"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create status %d: %v", status, body)
	}
	if _, has := body["client_secret"]; has {
		t.Error("a public client was issued a secret")
	}
	mine := listIntegrations(t, f.server, "alice", []string{"condor-users"})
	if has, _ := mine[0]["has_secret"].(bool); has {
		t.Error("has_secret is true for a public client")
	}
}

// TestAdminCreateClientServiceSubject: the admin path is the only one that
// can provision a grant acting with no user, and it refuses to provision one
// that would assert nobody.
func TestAdminCreateClientServiceSubject(t *testing.T) {
	f := newReauthFixture(t, Config{})

	rec := httptest.NewRecorder()
	f.server.handleAdminCreateClient(rec, adminPostRequest(t, f.server,
		"/api/v1/admin/oauth2/clients",
		`{"name":"no subject","grant_types":["client_credentials"]}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("client_credentials with no service subject: status %d, want 400: %s",
			rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	f.server.handleAdminCreateClient(rec, adminPostRequest(t, f.server,
		"/api/v1/admin/oauth2/clients",
		`{"name":"ops","grant_types":["client_credentials"],"service_subject":"ops@example.org"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create: status %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if secret, _ := body["client_secret"].(string); secret == "" {
		t.Error("a confidential service client was created without a secret")
	}
	// An operator's client belongs to the deployment, not to the operator,
	// so it must not appear in anybody's integration list.
	if n := len(listIntegrations(t, f.server, "root", []string{"condor-admins"})); n != 0 {
		t.Errorf("an admin-created client showed up as a user integration (%d)", n)
	}
}

// TestAdminCreateClientNeedsRedirectURI: a client with a browser flow and
// nowhere to redirect cannot work, so it is refused at creation.
func TestAdminCreateClientNeedsRedirectURI(t *testing.T) {
	f := newReauthFixture(t, Config{})

	rec := httptest.NewRecorder()
	f.server.handleAdminCreateClient(rec, adminPostRequest(t, f.server,
		"/api/v1/admin/oauth2/clients",
		`{"name":"browserless","grant_types":["authorization_code"]}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestPurgeUnusedClientsSparesCreated guards the origin filter in
// purgeUnusedClients.
//
// "Never obtained a token" is the normal state of an integration somebody
// has just created and not yet signed in to, so sweeping on use alone would
// delete a user's connected app out from under them a week after they set it
// up -- and it would look like the app had broken, not like the server had
// removed it.
func TestPurgeUnusedClientsSparesCreated(t *testing.T) {
	f := newReauthFixture(t, Config{})
	ctx := context.Background()
	db := f.server.oauth2Provider.GetStorage().GetDB()

	// One of each origin, none of which has ever obtained a token.
	userClient, err := f.server.createOAuth2Client(ctx, clientCreateSpec{
		Name: "mine", RedirectURIs: []string{"https://example.org/cb"},
		Owner: "alice", Origin: ClientOriginUser,
	})
	if err != nil {
		t.Fatalf("create user client: %v", err)
	}
	adminClient, err := f.server.createOAuth2Client(ctx, clientCreateSpec{
		Name: "theirs", RedirectURIs: []string{"https://example.org/cb"},
		Origin: ClientOriginAdmin,
	})
	if err != nil {
		t.Fatalf("create admin client: %v", err)
	}
	dynClient, err := f.server.createOAuth2Client(ctx, clientCreateSpec{
		Name: "churn", RedirectURIs: []string{"https://example.org/cb"},
		Origin: ClientOriginDynamic,
	})
	if err != nil {
		t.Fatalf("create dynamic client: %v", err)
	}

	// Age all three past the TTL. created_at is what the sweep reads.
	for _, id := range []string{userClient.ID, adminClient.ID, dynClient.ID} {
		if _, err := db.ExecContext(ctx,
			`UPDATE oauth2_clients SET created_at = datetime('now', '-30 days') WHERE id = ?`, id); err != nil {
			t.Fatalf("backdate %s: %v", id, err)
		}
	}

	if _, err := f.server.purgeUnusedClients(ctx); err != nil {
		t.Fatalf("purgeUnusedClients: %v", err)
	}

	exists := func(id string) bool {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM oauth2_clients WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", id, err)
		}
		return n > 0
	}
	if !exists(userClient.ID) {
		t.Error("the sweep deleted a user's own integration")
	}
	if !exists(adminClient.ID) {
		t.Error("the sweep deleted an operator-created client")
	}
	if exists(dynClient.ID) {
		t.Error("the sweep kept unused registration churn, which is what it is for")
	}
}
