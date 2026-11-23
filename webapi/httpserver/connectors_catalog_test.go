package httpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// The two things the admin form reads so an operator does not type them.

// catalog fetches the built-in catalogue as an administrator.
func (f *connectFixture) catalog(t *testing.T) []AdminConnectorCatalogEntry {
	t.Helper()
	resp := f.adminCall(t, f.admin(t), http.MethodGet, "/api/v1/admin/connectors/catalog", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("catalog status %d", resp.StatusCode)
	}
	var out struct {
		Builtins []AdminConnectorCatalogEntry `json:"builtins"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	return out.Builtins
}

// Every shipped definition is offered, and each entry carries an id the
// save endpoint will accept. The form's whole purpose is that nobody
// assembles `builtin:` + a name by hand, so an entry whose id does not
// parse is an entry that cannot be chosen.
func TestCatalogOffersEveryBuiltinWithAnIDThatParses(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	entries := f.catalog(t)

	if len(entries) != len(connectors.BuiltinIDs()) {
		t.Fatalf("catalogue has %d entries, the server ships %d",
			len(entries), len(connectors.BuiltinIDs()))
	}
	if len(entries) == 0 {
		t.Fatal("no built-ins at all, so the picker would be empty")
	}
	for _, e := range entries {
		id, err := connectors.ParseID(e.ID)
		if err != nil {
			t.Errorf("catalogue offers %q, which the save endpoint will refuse: %v", e.ID, err)
			continue
		}
		if id.Namespace != connectors.Builtin {
			t.Errorf("%s is in the built-in catalogue with namespace %q", e.ID, id.Namespace)
		}
		if e.Name != id.Name {
			t.Errorf("%s carries name %q", e.ID, e.Name)
		}
		if e.Summary == "" {
			t.Errorf("%s has no summary, so the picker shows a bare name", e.ID)
		}
	}
}

// Which provider has a file chooser, and which has a transfer plugin. Both
// are what the form uses to decide whether to show a control at all, so
// both have to be facts and not guesses -- and they are not the same fact:
// Dropbox has neither, Drive has both, and the rest have a plugin without
// a chooser.
func TestCatalogDistinguishesChoosersFromTransferPlugins(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	byID := map[string]AdminConnectorCatalogEntry{}
	for _, e := range f.catalog(t) {
		byID[e.ID] = e
	}

	drive, ok := byID["builtin:gdrive"]
	if !ok {
		t.Fatal("no builtin:gdrive in the catalogue")
	}
	if !drive.HasPicker {
		t.Error("builtin:gdrive has no chooser, but a Google Picker definition ships")
	}
	if !drive.PickerNeedsAPIKey {
		t.Error("Google's chooser needs a developer key and the catalogue does not say so")
	}
	if drive.TransferMethod == "" {
		t.Error("builtin:gdrive has no transfer method, so the form would hide Transfer for it")
	}

	drop, ok := byID["builtin:dropbox"]
	if !ok {
		t.Fatal("no builtin:dropbox in the catalogue")
	}
	if drop.HasPicker {
		t.Error("builtin:dropbox claims a chooser; none ships")
	}
	// The reason Transfer is hidden for it: direct mode needs a plugin
	// on the execute node and there is none, so the only working answer
	// is spool and the control is a choice with one option.
	if drop.TransferMethod != "" {
		t.Errorf("builtin:dropbox claims transfer method %q; HTCondor ships no plugin for it",
			drop.TransferMethod)
	}

	// At least one provider with a plugin and no chooser, so the two
	// flags cannot be the same field wearing two names.
	var pluginNoPicker []string
	for id, e := range byID {
		if e.TransferMethod != "" && !e.HasPicker {
			pluginNoPicker = append(pluginNoPicker, id)
		}
	}
	if len(pluginNoPicker) == 0 {
		t.Error("no provider has a transfer plugin without a chooser; " +
			"has_picker and transfer_method may have collapsed into one fact")
	}
}

// What is already configured says so, so the picker does not offer a
// second copy of something the save endpoint will refuse.
func TestCatalogMarksWhatIsAlreadyConfigured(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	for _, e := range f.catalog(t) {
		switch e.ID {
		case "builtin:gdrive":
			// The fixture declares this one in the configuration.
			if !e.Configured {
				t.Error("builtin:gdrive is configured and the catalogue does not say so")
			}
			if e.ConfiguredOrigin == "" {
				t.Error("builtin:gdrive is configured with no origin, so the page " +
					"cannot say whether it is editable here")
			}
		default:
			if e.Configured {
				t.Errorf("%s is marked configured; this fixture declares only gdrive", e.ID)
			}
		}
	}
}

// discover asks the endpoint about an issuer.
func (f *connectFixture) discover(t *testing.T, issuer string) (int, AdminConnectorDiscovery, string) {
	t.Helper()
	resp := f.adminCall(t, f.admin(t), http.MethodPost, "/api/v1/admin/connectors/discover",
		map[string]string{"issuer": issuer})
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read discover response: %v", err)
	}
	var out AdminConnectorDiscovery
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, string(raw)
}

// The happy path: an operator types an issuer and the endpoints arrive
// filled in, so the two URLs they would otherwise transcribe from a
// provider's documentation are not transcribed at all.
func TestDiscoverFillsTheEndpointsFromAnIssuer(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	// The document has to name itself, and that name is the server's own
	// URL, which is not known until it is listening -- hence the map
	// filled in after the fact rather than a literal.
	doc := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	doc["issuer"] = srv.URL
	doc["authorization_endpoint"] = srv.URL + "/oauth2/auth"
	doc["token_endpoint"] = srv.URL + "/oauth2/token"
	doc["scopes_supported"] = []string{"openid", "files.read"}
	doc["token_endpoint_auth_methods_supported"] = []string{"client_secret_post"}

	status, got, body := f.discover(t, srv.URL)
	if status != http.StatusOK {
		t.Fatalf("discover status %d: %s", status, body)
	}
	if got.AuthorizationURL != srv.URL+"/oauth2/auth" {
		t.Errorf("authorization_url = %q", got.AuthorizationURL)
	}
	if got.TokenURL != srv.URL+"/oauth2/token" {
		t.Errorf("token_url = %q", got.TokenURL)
	}
	if len(got.ScopesSupported) != 2 {
		t.Errorf("scopes_supported = %v", got.ScopesSupported)
	}
	if got.Document == "" {
		t.Error("no document URL, so an operator cannot go and read what answered")
	}
	if len(got.Warnings) != 0 {
		t.Errorf("a correct document produced warnings: %v", got.Warnings)
	}
}

// RFC 8414 puts the well-known path BEFORE the issuer's path, OIDC
// Discovery puts it after, and real providers serve both. An operator who
// pasted a Keycloak realm or an Ory project URL is in the first camp, so
// the probe has to try it.
func TestDiscoverFindsTheRFC8414PathForm(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	doc := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the inserted form is served. The appended form, which is
		// tried first, must 404 for this to be testing anything.
		if r.URL.Path != "/.well-known/oauth-authorization-server/realms/lab" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	issuer := srv.URL + "/realms/lab"
	doc["issuer"] = issuer
	doc["authorization_endpoint"] = issuer + "/protocol/openid-connect/auth"
	doc["token_endpoint"] = issuer + "/protocol/openid-connect/token"

	status, got, body := f.discover(t, issuer)
	if status != http.StatusOK {
		t.Fatalf("discover status %d: %s", status, body)
	}
	if !strings.HasSuffix(got.Document, "/.well-known/oauth-authorization-server/realms/lab") {
		t.Errorf("answered from %q, want the RFC 8414 inserted path", got.Document)
	}
	if got.TokenURL != doc["token_endpoint"] {
		t.Errorf("token_url = %q", got.TokenURL)
	}
}

// Things a document says that will fail later, somewhere an operator
// cannot see. Each of these is a connector that saves cleanly and breaks
// in a user's browser.
func TestDiscoverWarnsAboutWhatWillFailLater(t *testing.T) {
	cases := []struct {
		name  string
		doc   func(base string) map[string]any
		match string
	}{
		{
			// RFC 8414 section 3.3: the document's issuer must equal the
			// one it was fetched for. This is also what a trailing-slash
			// or wrong-tenant paste looks like.
			name: "another issuer",
			doc: func(base string) map[string]any {
				return map[string]any{
					"issuer":                 "https://somebody-else.example",
					"authorization_endpoint": base + "/auth",
					"token_endpoint":         base + "/token",
				}
			},
			match: "calls itself",
		},
		{
			// Without one there is nothing to exchange a code at, and
			// the form would save a connector that cannot complete.
			name: "no token endpoint",
			doc: func(base string) map[string]any {
				return map[string]any{"issuer": base, "authorization_endpoint": base + "/auth"}
			},
			match: "no token_endpoint",
		},
		{
			// A connector authenticates with a client secret. A provider
			// that takes neither way of sending one refuses every
			// exchange, at the token endpoint, hours later.
			name: "no way to send a secret",
			doc: func(base string) map[string]any {
				return map[string]any{
					"issuer":                                base,
					"authorization_endpoint":                base + "/auth",
					"token_endpoint":                        base + "/token",
					"token_endpoint_auth_methods_supported": []string{"private_key_jwt"},
				}
			},
			match: "client secret",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminFixture(t, "smith_lab")
			doc := map[string]any{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/.well-known/openid-configuration" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(doc)
			}))
			t.Cleanup(srv.Close)
			for k, v := range tc.doc(srv.URL) {
				doc[k] = v
			}

			status, got, body := f.discover(t, srv.URL)
			if status != http.StatusOK {
				t.Fatalf("discover status %d: %s", status, body)
			}
			if !strings.Contains(strings.Join(got.Warnings, " | "), tc.match) {
				t.Errorf("warnings %v do not mention %q", got.Warnings, tc.match)
			}
		})
	}
}

// http is refused away from loopback. The client secret for this connector
// goes to the token endpoint this document names, and an operator typing
// an issuer has no reason to connect the two -- so the refusal says it.
func TestDiscoverRefusesACleartextIssuer(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	status, _, body := f.discover(t, "http://provider.example")
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", status, body)
	}
	if !strings.Contains(body, "secret") {
		t.Errorf("the refusal does not say why http is a problem: %s", body)
	}
}

// A bare hostname is what gets typed. Assumed https rather than refused,
// since https is what the rule above insists on anyway.
func TestDiscoverAssumesHTTPSForABareHostname(t *testing.T) {
	u, err := normalizeIssuer("provider.example/tenant")
	if err != nil {
		t.Fatalf("normalizeIssuer: %v", err)
	}
	if u.Scheme != "https" || u.Host != "provider.example" || u.Path != "/tenant" {
		t.Errorf("normalized to %q", u)
	}
}

// A redirect off the issuer's host is how a discovery fetch becomes a way
// to make this daemon talk to something else. Nothing legitimate needs it.
func TestDiscoverWontFollowARedirectToAnotherHost(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"issuer":"x","authorization_endpoint":"https://x/auth"}`)
	}))
	t.Cleanup(elsewhere.Close)

	// 127.0.0.1 and localhost are the same machine and different hosts,
	// which is exactly the pair that makes this testable over loopback.
	target := strings.Replace(elsewhere.URL, "127.0.0.1", "localhost", 1)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+"/.well-known/openid-configuration", http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	status, _, body := f.discover(t, redirector.URL)
	if status != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 -- the redirect was followed: %s", status, body)
	}
	if !strings.Contains(body, "another host") {
		t.Errorf("the refusal does not name the reason: %s", body)
	}
}

// Nothing in here is reachable without the admin role.
func TestCatalogAndDiscoverNeedAdmin(t *testing.T) {
	f := newAdminFixture(t, "smith_lab")
	f.h.webuiAdminGroups = newGroupSet("web-admins")
	user := f.login(t, "alice", "smith_lab")

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/admin/connectors/catalog", nil},
		{http.MethodPost, "/api/v1/admin/connectors/discover", map[string]string{"issuer": "https://x"}},
	} {
		resp := f.adminCall(t, user, tc.method, tc.path, tc.body)
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("%s %s as a non-admin: status %d", tc.method, tc.path, code)
		}
	}
}
