package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// The admin surface. The property that matters most here is that the
// client secret never comes back out, in any form.

// admin signs in an administrator. One name, because nothing here
// depends on which administrator is acting.
func (f *connectFixture) admin(t *testing.T) *http.Client {
	t.Helper()
	f.h.webuiAdminGroups = newGroupSet("web-admins")
	return f.login(t, "root", "web-admins", "smith_lab")
}

func (f *connectFixture) adminCall(t *testing.T, c *http.Client, method, path string, body any) *http.Response {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, f.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// adminFixture serves the admin routes too.
func newAdminFixture(t *testing.T, groups string) *connectFixture {
	t.Helper()
	f := newConnectFixture(t, groups)
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/admin/connectors"):
			f.h.handleAdminConnectors(w, r)
		case r.URL.Path == "/api/v1/connect":
			f.h.handleConnectCollection(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/v1/connect/"):
			f.h.handleConnectItem(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	return f
}

func TestAdminConnectorsNeedsAdmin(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	// A signed-in user who is not an administrator.
	f.h.webuiAdminGroups = newGroupSet("web-admins")
	c := f.login(t, "alice", "smith_lab")

	resp := f.adminCall(t, c, http.MethodGet, "/api/v1/admin/connectors", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403 for a non-administrator", resp.StatusCode)
	}
}

// The listing reports where a connector came from, because that is the
// difference between a change that takes effect now and one that needs
// condor_reconfig.
func TestAdminListingReportsProvenance(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)

	resp := f.adminCall(t, c, http.MethodGet, "/api/v1/admin/connectors", nil)
	var rows []AdminConnector
	decode(t, resp, &rows)
	defer func() { _ = resp.Body.Close() }()

	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the configured connector", rows)
	}
	row := rows[0]
	if row.ID != "builtin:gdrive" {
		t.Errorf("ID = %q", row.ID)
	}
	if row.StoredHere || row.Editable {
		t.Error("a configuration-declared connector was reported as editable here")
	}
	if row.Origin == "" {
		t.Error("no origin reported")
	}
	// The path is configuration and public; the bytes are not.
	if row.SecretFile == "" {
		t.Error("the secret file path was not reported")
	}
	if !row.SecretSet {
		t.Error("SecretSet is false though a secret file is configured")
	}
}

// The secret itself never comes back, in any form.
func TestAdminNeverRendersTheSecret(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)

	// Store one, so there is something that could leak.
	resp := f.adminCall(t, c, http.MethodPost, "/api/v1/admin/connectors",
		AdminConnectorRequest{ //nolint:gosec // G101: a test value, not a credential
			ID: "site:cryoem", Kind: "oauth2", ClientID: "cid",
			Secret: strPtr("top-secret-value"), AuthURL: "https://x/auth",
			TokenURL: "https://x/token", Groups: []string{"smith_lab"},
		})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST status %d", resp.StatusCode)
	}

	listing := f.adminCall(t, c, http.MethodGet, "/api/v1/admin/connectors", nil)
	defer func() { _ = listing.Body.Close() }()
	body := readBody(t, listing)

	if strings.Contains(body, "top-secret-value") {
		t.Fatal("the client secret came back in the listing")
	}
	// Not masked, not by length either -- the response says only that
	// one is set and when it changed.
	if strings.Contains(body, "****") || strings.Contains(body, "top-secret") {
		t.Fatalf("a form of the secret came back: %s", body)
	}
	if !strings.Contains(body, `"secret_set":true`) {
		t.Errorf("SecretSet was not reported: %s", body)
	}
}

// Absent leaves the stored secret alone; explicit empty clears it. A
// form that round-tripped an absent field as "" would wipe a working
// connector's secret on an unrelated edit.
func TestAdminEditWithoutASecretKeepsIt(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)
	id := connectors.MustParse("site:cryoem")

	post := func(secret *string, clientID string) {
		t.Helper()
		resp := f.adminCall(t, c, http.MethodPost, "/api/v1/admin/connectors",
			AdminConnectorRequest{ //nolint:gosec // G101: a test value, not a credential
				ID: "site:cryoem", Kind: "oauth2", ClientID: clientID,
				Secret: secret, AuthURL: "https://x/auth", TokenURL: "https://x/token",
				Groups: []string{"smith_lab"},
			})
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST status %d", resp.StatusCode)
		}
	}

	post(strPtr("original"), "cid-1")
	post(nil, "cid-2") // an unrelated edit

	got, err := f.h.connectorStore().ClientSecret(t.Context(), id)
	if err != nil {
		t.Fatalf("the secret was lost by an edit that did not mention it: %v", err)
	}
	if string(got) != "original" {
		t.Errorf("secret = %q, want it unchanged", got)
	}
}

// A connector the configuration declares is refused rather than stored:
// the stored row would lose, and writing one that never takes effect is
// worse than saying no.
func TestAdminRefusesToShadowAConfiguredConnector(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)

	resp := f.adminCall(t, c, http.MethodPost, "/api/v1/admin/connectors",
		AdminConnectorRequest{ID: "builtin:gdrive", Kind: "oauth2", Groups: []string{"x"}})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409", resp.StatusCode)
	}
	if !strings.Contains(readBody(t, resp), "condor_reconfig") {
		t.Error("the refusal does not say where to change it instead")
	}
}

func TestAdminCannotDeleteAConfiguredConnector(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)

	resp := f.adminCall(t, c, http.MethodDelete, "/api/v1/admin/connectors/builtin:gdrive", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status %d, want 409", resp.StatusCode)
	}
}

// Delete reports what it destroyed, so a confirmation can say it rather
// than asking in the abstract.
func TestAdminDeleteReportsTheCredentialsItDestroys(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)
	id := connectors.MustParse("site:cryoem")

	resp := f.adminCall(t, c, http.MethodPost, "/api/v1/admin/connectors",
		AdminConnectorRequest{ID: "site:cryoem", Kind: "oauth2", //nolint:gosec // G101: test endpoints
			AuthURL: "https://x/a", TokenURL: "https://x/t", Groups: []string{"smith_lab"}})
	_ = resp.Body.Close()

	for _, who := range []string{"alice", "bob"} {
		if err := f.h.connectorStore().PutCredential(t.Context(),
			connectors.Credential{Subject: who, Connector: id}, []byte("r")); err != nil {
			t.Fatal(err)
		}
	}

	del := f.adminCall(t, c, http.MethodDelete, "/api/v1/admin/connectors/site:cryoem", nil)
	defer func() { _ = del.Body.Close() }()

	var body struct {
		CredentialsDestroyed int `json:"credentials_destroyed"`
	}
	if err := json.NewDecoder(del.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.CredentialsDestroyed != 2 {
		t.Errorf("credentials_destroyed = %d, want 2", body.CredentialsDestroyed)
	}
}

// Revoke stays available on a connector the CONFIGURATION declares. The
// configuration owns the definition; these credentials are this
// server's own state, and an operator who cannot revoke them here has
// to go and delete database rows by hand.
func TestAdminCanRevokeAConfiguredConnector(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)
	id := connectors.MustParse("builtin:gdrive")

	if err := f.h.connectorStore().PutCredential(t.Context(),
		connectors.Credential{Subject: "alice", Connector: id}, []byte("r")); err != nil {
		t.Fatal(err)
	}

	resp := f.adminCall(t, c, http.MethodPost, "/api/v1/admin/connectors/builtin:gdrive/revoke", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}

	if _, found, _ := f.h.connectorStore().Credential(t.Context(), "alice", id); found {
		t.Error("the grant survived revocation")
	}
	// And the connector itself is still there.
	if _, found := f.h.connectorDefinition(id); !found {
		t.Error("revoke removed the connector; that is delete's job")
	}
}

// Test answers "is this configured correctly" without needing anybody's
// grant, so a wrong connector says so when it is configured rather than
// at some user's first attempt.
func TestAdminTestReportsProblems(t *testing.T) {
	f := newAdminFixture(t, "")
	c := f.admin(t)

	resp := f.adminCall(t, c, http.MethodPost, "/api/v1/admin/connectors/builtin:gdrive/test", nil)
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Usable   bool     `json:"usable"`
		Problems []string `json:"problems"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Usable {
		t.Error("a connector nobody may use was reported as usable")
	}
	joined := strings.Join(body.Problems, "; ")
	if !strings.Contains(joined, "groups") {
		t.Errorf("problems = %v, want the empty group list named", body.Problems)
	}
}

func TestAdminTestPassesAWorkingConnector(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)

	resp := f.adminCall(t, c, http.MethodPost, "/api/v1/admin/connectors/builtin:gdrive/test", nil)
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Usable   bool     `json:"usable"`
		Problems []string `json:"problems"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Usable {
		t.Errorf("a configured connector was reported unusable: %v", body.Problems)
	}
}

func TestAdminUsersListsWhoConnected(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)
	id := connectors.MustParse("builtin:gdrive")

	for _, who := range []string{"alice", "bob"} {
		if err := f.h.connectorStore().PutCredential(t.Context(),
			connectors.Credential{Subject: who, Connector: id}, []byte("r")); err != nil {
			t.Fatal(err)
		}
	}

	resp := f.adminCall(t, c, http.MethodGet, "/api/v1/admin/connectors/builtin:gdrive/users", nil)
	var users []AdminConnectorUser
	decode(t, resp, &users)
	defer func() { _ = resp.Body.Close() }()

	if len(users) != 2 || users[0].Subject != "alice" {
		t.Errorf("users = %+v", users)
	}
}

// A stored connector the configuration also declares is reported as
// shadowed rather than hidden: a row that silently loses is an operator
// editing a form to no effect and finding out from a user.
func TestAdminReportsAShadowedStoredConnector(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	c := f.admin(t)

	// Store one directly, bypassing the handler's refusal, as a server
	// reconfigured to declare it afterwards would leave behind.
	if err := f.h.connectorStore().PutConnector(t.Context(), connectors.Stored{
		ID: connectors.MustParse("builtin:gdrive"), Kind: connectors.KindOAuth2,
		Groups: []string{"smith_lab"}, Enabled: true,
	}, nil); err != nil {
		t.Fatal(err)
	}

	resp := f.adminCall(t, c, http.MethodGet, "/api/v1/admin/connectors", nil)
	var rows []AdminConnector
	decode(t, resp, &rows)
	defer func() { _ = resp.Body.Close() }()

	var found bool
	for _, row := range rows {
		if row.ID == "builtin:gdrive" {
			found = true
			if !row.Shadowed {
				t.Error("a stored row the configuration overrides was not reported as shadowed")
			}
			if row.Origin == "stored" {
				t.Error("the shadowed row was reported as the one in force")
			}
		}
	}
	if !found {
		t.Error("the shadowed connector vanished from the listing")
	}
}

func strPtr(s string) *string { return &s }
