package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// Taking a connected account back.
//
// The store could always forget a grant; nothing exposed it, so a user who
// connected an account had no way to undo that from any page. The property
// under test is the one the rest of these endpoints turn on: the identity is
// the SESSION's, so a request can only ever forget the caller's own
// credential.

// del sends an authenticated DELETE.
func (f *connectFixture) del(t *testing.T, c *http.Client, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

// grant stores a credential for one user, standing in for a completed
// provider flow.
func (f *connectFixture) grant(t *testing.T, user string) {
	t.Helper()
	id, err := connectors.ParseID("builtin:gdrive")
	if err != nil {
		t.Fatal(err)
	}
	err = f.h.connectorStore().PutCredential(t.Context(), connectors.Credential{
		Subject:         user,
		Connector:       id,
		ProviderSubject: user + "@example.org",
		GrantedScopes:   []string{"https://www.googleapis.com/auth/drive.file"},
		ObtainedAt:      time.Now().UTC(),
	}, []byte("a-refresh-token"))
	if err != nil {
		t.Fatalf("store a credential for %s: %v", user, err)
	}
}

// connected reports what the collection says about builtin:gdrive for this
// client.
func (f *connectFixture) connected(t *testing.T, c *http.Client) bool {
	t.Helper()
	resp := f.get(t, c, "/api/v1/connect", "application/json")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/connect: status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var list []struct {
		ID        string `json:"id"`
		Connected bool   `json:"connected"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	for _, e := range list {
		if e.ID == "builtin:gdrive" {
			return e.Connected
		}
	}
	t.Fatalf("builtin:gdrive is not in the listing: %s", body)
	return false
}

func TestDisconnectForgetsTheCallersGrant(t *testing.T) {
	f := newConnectFixture(t, "condor-users")
	c := f.login(t, "alice", "condor-users")

	f.grant(t, "alice")
	if !f.connected(t, c) {
		t.Fatal("the listing does not show alice connected after a credential was stored")
	}

	resp := f.del(t, c, "/api/v1/connect/builtin:gdrive")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("disconnect: status %d: %s", resp.StatusCode, body)
	}
	if f.connected(t, c) {
		t.Error("the listing still shows alice connected after disconnecting")
	}
}

// TestDisconnectTouchesOnlyTheCaller is the important one: the connector id is
// in the path and the user never is, so there is no request shape that forgets
// somebody else's credential.
func TestDisconnectTouchesOnlyTheCaller(t *testing.T) {
	f := newConnectFixture(t, "condor-users")
	alice := f.login(t, "alice", "condor-users")
	bob := f.login(t, "bob", "condor-users")

	f.grant(t, "alice")
	f.grant(t, "bob")

	resp := f.del(t, alice, "/api/v1/connect/builtin:gdrive")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice's disconnect: status %d", resp.StatusCode)
	}

	if f.connected(t, alice) {
		t.Error("alice is still connected")
	}
	if !f.connected(t, bob) {
		t.Error("alice's disconnect forgot bob's credential")
	}
}

// TestDisconnectIsIdempotent: the caller asked for it to be gone and it is
// gone. A 404 would make a double-click look like a failure.
func TestDisconnectIsIdempotent(t *testing.T) {
	f := newConnectFixture(t, "condor-users")
	c := f.login(t, "alice", "condor-users")

	for i := 0; i < 2; i++ {
		resp := f.del(t, c, "/api/v1/connect/builtin:gdrive")
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("disconnect %d of a connector that was never connected: status %d", i, resp.StatusCode)
		}
	}
}

// TestDisconnectHonoursTheSameGatesAsConnecting: the group check and the
// unknown-connector check are shared with the GET paths, so a caller outside
// the groups learns only that it is not available to them.
func TestDisconnectHonoursTheSameGatesAsConnecting(t *testing.T) {
	f := newConnectFixture(t, "drive-users")
	outsider := f.login(t, "carol", "condor-users")

	resp := f.del(t, outsider, "/api/v1/connect/builtin:gdrive")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a connector outside the caller's groups: status %d, want 403", resp.StatusCode)
	}

	member := f.login(t, "dave", "drive-users")
	resp = f.del(t, member, "/api/v1/connect/nosuch:thing")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown connector: status %d, want 404", resp.StatusCode)
	}
}

// TestDisconnectRefusesOtherMethods keeps the per-connector path to the verbs
// it implements, so a typo does not silently begin a flow.
func TestDisconnectRefusesOtherMethods(t *testing.T) {
	f := newConnectFixture(t, "condor-users")
	c := f.login(t, "alice", "condor-users")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		f.srv.URL+"/api/v1/connect/builtin:gdrive", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST to a connector: status %d, want 405", resp.StatusCode)
	}

	// DELETE on a sub-path is not a disconnect either.
	resp = f.del(t, c, "/api/v1/connect/builtin:gdrive/callback")
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("DELETE on /callback was treated as a disconnect")
	}
}
