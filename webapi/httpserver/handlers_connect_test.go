package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bbockelm/golang-htcondor/config"
	"github.com/bbockelm/golang-htcondor/webapi/connectors"
	"github.com/bbockelm/golang-htcondor/webapi/connectors/mockgoogle"
	"github.com/bbockelm/golang-htcondor/webapi/httpserver/appdb"
)

// The /api/v1/connect endpoints, driven the way a browser drives them.
//
// The property under test throughout is the one the design turns on:
// the entry point carries NO AUTHORITY. It is a fixed, printable URL,
// and without an authenticated browser session it yields a login
// redirect rather than a credential.

type connectFixture struct {
	h        *Handler
	google   *mockgoogle.Server
	srv      *httptest.Server
	secretFP string
}

// newConnectFixtureWithPicker is newConnectFixture with the file
// chooser asked for, which is what widens the page's CSP.
func newConnectFixtureWithPicker(t *testing.T, groups string) *connectFixture {
	t.Helper()
	return newConnectFixtureOpts(t, groups, true)
}

func newConnectFixture(t *testing.T, groups string) *connectFixture {
	t.Helper()
	return newConnectFixtureOpts(t, groups, false)
}

// newConnectFixtureWithTwoPickers configures two choosers for the same
// provider, which is what a site with two labs' credentials looks like.
func newConnectFixtureWithTwoPickers(t *testing.T, groups string) *connectFixture {
	t.Helper()
	f := newConnectFixtureOpts(t, groups, true)
	cfg, err := config.NewFromReader(strings.NewReader(strings.Join([]string{
		"HTTP_API_CONNECTORS = builtin:gdrive",
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_GROUPS = " + groups,
		"HTTP_API_PICKERS = drive, drive-two",
		"HTTP_API_PICKER_DRIVE_PROVIDER = gdrive",
		"HTTP_API_PICKER_DRIVE_CONNECTOR = builtin:gdrive",
		"HTTP_API_PICKER_DRIVE_API_KEY = k1",
		"HTTP_API_PICKER_DRIVE_TWO_PROVIDER = gdrive",
		"HTTP_API_PICKER_DRIVE_TWO_CONNECTOR = builtin:gdrive",
		"HTTP_API_PICKER_DRIVE_TWO_API_KEY = k2",
	}, "\n")))
	if err != nil {
		t.Fatalf("parse the test configuration: %v", err)
	}
	f.h.SetConnectorConfig(cfg)
	return f
}

// pickerLines configures a chooser, or nothing. A chooser is its own
// entity, not a flag on the connector: it is a provider's, it borrows
// the connector's credential, and it carries a developer key that is
// not a credential's to hold.
func pickerLines(on bool) string {
	if !on {
		return ""
	}
	return strings.Join([]string{
		"HTTP_API_PICKERS = drive",
		"HTTP_API_PICKER_DRIVE_PROVIDER = gdrive",
		"HTTP_API_PICKER_DRIVE_CONNECTOR = builtin:gdrive",
		"HTTP_API_PICKER_DRIVE_API_KEY = test-developer-key",
	}, "\n")
}

func newConnectFixtureOpts(t *testing.T, groups string, picker bool) *connectFixture {
	t.Helper()

	db, err := appdb.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("appdb.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := appdb.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	h := &Handler{db: db, logger: testLogger(t)}
	h.sessionStore = createTestSessionStore(t, time.Hour)
	// No sealer: a deployment without HTTP_API_KEK_FILE still has to
	// work, and it is the shape most tests run in.
	h.connectorStored = connectors.NewStore(db, nil)
	h.connectorFlows = connectors.NewFlow()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/connect":
			h.handleConnectCollection(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/v1/connect/"):
			h.handleConnectItem(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	h.httpBaseURL = srv.URL

	redirect := srv.URL + "/api/v1/connect/builtin:gdrive/callback"
	g := mockgoogle.New(t, mockgoogle.Config{
		ClientID:     "1234.apps.googleusercontent.com",
		ClientSecret: "s3cret",
		RedirectURI:  redirect,
	})

	secretFP := filepath.Join(t.TempDir(), "gdrive.secret")
	if err := os.WriteFile(secretFP, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Written as a configuration FILE rather than injected, so the
	// parameter names here are the ones an operator would type.
	cfg, err := config.NewFromReader(strings.NewReader(strings.Join([]string{
		"HTTP_API_CONNECTORS = builtin:gdrive",
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_ID = 1234.apps.googleusercontent.com",
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_SECRET_FILE = " + secretFP,
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_GROUPS = " + groups,
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_AUTHORIZATION_URL = " + g.AuthorizationURL(),
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_TOKEN_URL = " + g.TokenURL(),
		pickerLines(picker),
	}, "\n")))
	if err != nil {
		t.Fatalf("parse the test configuration: %v", err)
	}
	h.SetConnectorConfig(cfg)

	return &connectFixture{h: h, google: g, srv: srv, secretFP: secretFP}
}

// login makes a real session and returns a client carrying its cookie.
//
// The real SessionStore rather than a stub, because what is under test
// includes which callers count as signed in.
func (f *connectFixture) login(t *testing.T, user string, groups ...string) *http.Client {
	t.Helper()
	id, _, err := f.h.sessionStore.Create(user, groups)
	if err != nil {
		t.Fatalf("create a session: %v", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: sessionCookieName, Value: id, Path: "/"}}) //nolint:gosec // G124: a test cookie on an httptest server
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// authedRequest is a request carrying c's session cookie, for calling
// a handler's internals directly rather than over HTTP.
func (f *connectFixture) authedRequest(t *testing.T, c *http.Client) *http.Request {
	t.Helper()
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
		f.srv.URL+"/api/v1/jobs/1.0/input", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range c.Jar.Cookies(u) {
		req.AddCookie(ck)
	}
	return req
}

func anonymous() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func (f *connectFixture) get(t *testing.T, c *http.Client, path string, accept string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// --- the entry point carries no authority -----------------------------

// The whole reason the URL is safe to print: without a session it hands
// out a login page, not a credential.
func TestConnectEntryPointGivesNoCredentialWithoutASession(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")

	resp := f.get(t, anonymous(), "/api/v1/connect/builtin:gdrive", "text/html")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d, want a redirect to log in", resp.StatusCode)
	}
	loc, err := resp.Location()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc.Path, "/login") {
		t.Fatalf("redirected to %v, want /login", loc)
	}
	// And emphatically not to the provider.
	if strings.Contains(loc.String(), "oauth2") || loc.Query().Get("client_id") != "" {
		t.Fatalf("an anonymous caller was sent to the provider: %v", loc)
	}
}

// A non-browser gets 401 rather than a redirect. Sending an API client
// to an HTML login page produces a 200 full of markup, which is the
// least diagnosable failure available.
func TestConnectRefusesANonBrowserWithAStatus(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")

	resp := f.get(t, anonymous(), "/api/v1/connect/builtin:gdrive", "application/json")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", resp.StatusCode)
	}
}

// --- groups -----------------------------------------------------------

func TestConnectorOutsideYourGroupsIsRefused(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "mallory", "other_lab")

	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive", "application/json")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

// A connector a caller may not use is not listed. Otherwise the page
// offers a button that will refuse.
func TestListingOmitsConnectorsYouMayNotUse(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")

	in := f.login(t, "alice", "smith_lab")
	resp := f.get(t, in, "/api/v1/connect", "application/json")
	defer func() { _ = resp.Body.Close() }()
	var listed []ConnectorStatus
	decode(t, resp, &listed)
	if len(listed) != 1 || listed[0].ID != "builtin:gdrive" {
		t.Fatalf("a member saw %+v, want builtin:gdrive", listed)
	}
	if listed[0].ConnectURL != "/api/v1/connect/builtin:gdrive" {
		t.Errorf("ConnectURL = %q", listed[0].ConnectURL)
	}
	if listed[0].Connected {
		t.Error("reported as connected before connecting")
	}

	out := f.login(t, "mallory", "other_lab")
	resp2 := f.get(t, out, "/api/v1/connect", "application/json")
	defer func() { _ = resp2.Body.Close() }()
	var listed2 []ConnectorStatus
	decode(t, resp2, &listed2)
	if len(listed2) != 0 {
		t.Errorf("a non-member saw %+v", listed2)
	}
}

// Empty groups means nobody, not everyone.
func TestNoGroupsConfiguredMeansNobody(t *testing.T) {
	f := newConnectFixture(t, "")
	c := f.login(t, "alice", "smith_lab")

	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive", "application/json")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403 when no groups are configured", resp.StatusCode)
	}
}

// --- the flow ---------------------------------------------------------

// The whole round trip: a signed-in user is sent to the provider,
// consents, comes back, and a credential lands in the store.
func TestWholeFlowStoresACredential(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")

	// 1. The entry point sends us to the provider.
	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive", "text/html")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d, want a redirect to the provider", resp.StatusCode)
	}
	authURL, err := resp.Location()
	if err != nil {
		t.Fatal(err)
	}
	if authURL.Query().Get("access_type") != "offline" {
		t.Errorf("access_type = %q", authURL.Query().Get("access_type"))
	}

	// 2. The provider sends us back.
	provResp, err := c.Get(authURL.String()) //nolint:noctx,bodyclose // followed immediately below
	if err != nil {
		t.Fatal(err)
	}
	_ = provResp.Body.Close()
	back, err := provResp.Location()
	if err != nil {
		t.Fatalf("the provider did not redirect back: %v", err)
	}

	// 3. The callback stores the grant.
	cb := f.get(t, c, back.Path+"?"+back.RawQuery, "application/json")
	defer func() { _ = cb.Body.Close() }()
	if cb.StatusCode != http.StatusOK {
		t.Fatalf("callback status %d", cb.StatusCode)
	}

	cred, found, err := f.h.connectorStore().Credential(t.Context(), "alice",
		connectors.MustParse("builtin:gdrive"))
	if err != nil || !found {
		t.Fatalf("no credential stored: found=%v err=%v", found, err)
	}
	if cred.Subject != "alice" {
		t.Errorf("credential filed under %q", cred.Subject)
	}
	tok, err := f.h.connectorStore().RefreshToken(t.Context(), "alice",
		connectors.MustParse("builtin:gdrive"))
	if err != nil || len(tok) == 0 {
		t.Errorf("no refresh token stored: %v", err)
	}

	// And the listing now says so.
	list := f.get(t, c, "/api/v1/connect", "application/json")
	defer func() { _ = list.Body.Close() }()
	var listed []ConnectorStatus
	decode(t, list, &listed)
	if len(listed) != 1 || !listed[0].Connected {
		t.Errorf("listing = %+v, want connected", listed)
	}
}

// A callback presented by a different session stores nothing. The state
// travels through a browser and a query string.
func TestCallbackFromAnotherSessionStoresNothing(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	alice := f.login(t, "alice", "smith_lab")
	mallory := f.login(t, "mallory", "smith_lab")

	resp := f.get(t, alice, "/api/v1/connect/builtin:gdrive", "text/html")
	_ = resp.Body.Close()
	authURL, _ := resp.Location()

	provResp, err := alice.Get(authURL.String()) //nolint:noctx,bodyclose // followed immediately below
	if err != nil {
		t.Fatal(err)
	}
	_ = provResp.Body.Close()
	back, _ := provResp.Location()

	cb := f.get(t, mallory, back.Path+"?"+back.RawQuery, "application/json")
	defer func() { _ = cb.Body.Close() }()
	if cb.StatusCode == http.StatusOK {
		t.Fatal("mallory completed alice's authorization")
	}

	for _, who := range []string{"alice", "mallory"} {
		if _, found, _ := f.h.connectorStore().Credential(t.Context(), who,
			connectors.MustParse("builtin:gdrive")); found {
			t.Errorf("a credential was stored for %q", who)
		}
	}
}

func TestProviderRefusalIsReportedNotStored(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	f.google.DenyAuthorization(true)
	c := f.login(t, "alice", "smith_lab")

	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive", "text/html")
	_ = resp.Body.Close()
	authURL, _ := resp.Location()

	provResp, err := c.Get(authURL.String()) //nolint:noctx,bodyclose // followed immediately below
	if err != nil {
		t.Fatal(err)
	}
	_ = provResp.Body.Close()
	back, _ := provResp.Location()

	cb := f.get(t, c, back.Path+"?"+back.RawQuery, "application/json")
	defer func() { _ = cb.Body.Close() }()
	if cb.StatusCode == http.StatusOK {
		t.Fatal("a refused authorization was reported as success")
	}
	// Assert the MESSAGE, not just the status. Without the error=
	// check the flow runs on to the exchange, fails there on the empty
	// code, and returns a status that looks the same -- so a test that
	// only reads the status passes while the refusal goes unnoticed.
	body := readBody(t, cb)
	if !strings.Contains(body, "access_denied") {
		t.Errorf("response = %q, want it to report the provider's refusal", body)
	}
	if _, found, _ := f.h.connectorStore().Credential(t.Context(), "alice",
		connectors.MustParse("builtin:gdrive")); found {
		t.Error("a credential was stored despite the refusal")
	}
}

// The 302 target comes from the connector's own definition, never from
// a request parameter. This endpoint is shaped exactly like an open
// redirect, and a fixed URL that is safe to print stops being safe the
// moment a query string can steer it.
func TestRedirectTargetCannotComeFromTheRequest(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")

	evil := "https://evil.example.net/steal"
	for _, param := range []string{"next", "redirect_uri", "return_to", "url"} {
		resp := f.get(t, c,
			"/api/v1/connect/builtin:gdrive?"+param+"="+url.QueryEscape(evil), "text/html")
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s: status %d", param, resp.StatusCode)
		}
		loc, err := resp.Location()
		if err != nil {
			t.Fatalf("%s: %v", param, err)
		}
		if strings.Contains(loc.String(), "evil.example.net") {
			t.Fatalf("?%s= steered the redirect to %v", param, loc)
		}
		// It went to the provider, which is the only place it may go.
		if loc.Query().Get("client_id") == "" {
			t.Errorf("?%s=: redirected to %v, want the provider", param, loc)
		}
	}
}

// The callback's redirect_uri is derived from the server's configured
// base URL, not from a caller-chosen header, because a redirect_uri
// taken from Host is one a caller can pick.
func TestCallbackURIIgnoresAForgedHost(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		f.srv.URL+"/api/v1/connect/builtin:gdrive", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Forwarded-Host", "evil.example.net")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	loc, err := resp.Location()
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query().Get("redirect_uri"); strings.Contains(got, "evil.example.net") {
		t.Fatalf("redirect_uri = %q; a forged header chose where the provider sends the code", got)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// --- ids --------------------------------------------------------------

func TestUnknownConnectorIsNotFound(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")

	for _, path := range []string{
		"/api/v1/connect/builtin:nosuch",
		"/api/v1/connect/site:nosuch",
		"/api/v1/connect/gdrive",        // no namespace
		"/api/v1/connect/vendor:gdrive", // unknown namespace
	} {
		resp := f.get(t, c, path, "application/json")
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, resp.StatusCode)
		}
	}
}

// The connector is in the path; the user never is. A parameter naming
// one must not be honoured, or this becomes privilege escalation.
func TestAUserParameterIsNotHonoured(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")

	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive?user=bob", "text/html")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
	authURL, _ := resp.Location()
	provResp, err := c.Get(authURL.String()) //nolint:noctx,bodyclose // followed immediately below
	if err != nil {
		t.Fatal(err)
	}
	_ = provResp.Body.Close()
	back, _ := provResp.Location()

	cb := f.get(t, c, back.Path+"?"+back.RawQuery+"&user=bob", "application/json")
	_ = cb.Body.Close()

	if _, found, _ := f.h.connectorStore().Credential(t.Context(), "bob",
		connectors.MustParse("builtin:gdrive")); found {
		t.Fatal("a ?user= parameter filed the credential under another account")
	}
	if _, found, _ := f.h.connectorStore().Credential(t.Context(), "alice",
		connectors.MustParse("builtin:gdrive")); !found {
		t.Error("the credential was not filed under the session's own user")
	}
}

// decode reads a JSON body. It does NOT close it: the caller does,
// right where the response is obtained, so ownership is visible at the
// call site rather than buried one frame down.
func decode(t *testing.T, resp *http.Response, into any) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// --- the token endpoint -----------------------------------------------
//
// The one place a third-party credential leaves this server, so these
// are the tests that matter most in this file.

// connectAs runs a whole flow so the caller holds a stored grant.
func (f *connectFixture) connectAs(t *testing.T, c *http.Client) {
	t.Helper()
	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive", "text/html")
	_ = resp.Body.Close()
	authURL, err := resp.Location()
	if err != nil {
		t.Fatalf("no redirect to the provider: %v", err)
	}
	provResp, err := c.Get(authURL.String()) //nolint:noctx,bodyclose // followed immediately below
	if err != nil {
		t.Fatal(err)
	}
	_ = provResp.Body.Close()
	back, err := provResp.Location()
	if err != nil {
		t.Fatalf("the provider did not redirect back: %v", err)
	}
	cb := f.get(t, c, back.Path+"?"+back.RawQuery, "application/json")
	_ = cb.Body.Close()
	if cb.StatusCode != http.StatusOK {
		t.Fatalf("callback status %d", cb.StatusCode)
	}
}

func TestTokenEndpointServesAConnectedBrowser(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")
	f.connectAs(t, c)

	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive/token", "application/json")
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.AccessToken == "" {
		t.Fatal("no access token")
	}
	if !f.google.AccessTokenValid(body.AccessToken) {
		t.Error("the token is not one the provider issued")
	}
	if body.ExpiresIn <= 0 {
		t.Errorf("expires_in = %d, want the life of the token", body.ExpiresIn)
	}

	// A bearer credential with a short life must not be cached by
	// anything between here and the browser.
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// A request carrying its own Authorization is refused even with a valid
// session cookie. Holding both reads as "a program, not a person's
// browser tab", and the cost of being wrong the other way is handing a
// user's Google token to whatever asked.
func TestTokenEndpointRefusesACallerHoldingItsOwnCredential(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")
	f.connectAs(t, c)

	for _, auth := range []string{"Bearer some-token", "Basic dXNlcjpwdw==", "token abc"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			f.srv.URL+"/api/v1/connect/builtin:gdrive/token", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", auth)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body := readBody(t, resp)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%q: a caller with its own credential was given a user's token", auth)
		}
		if strings.Contains(body, "access_token") {
			t.Fatalf("%q: the response carried a token anyway: %s", auth, body)
		}
	}
}

func TestTokenEndpointRefusesWithoutASession(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	resp := f.get(t, anonymous(), "/api/v1/connect/builtin:gdrive/token", "application/json")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", resp.StatusCode)
	}
}

func TestTokenEndpointRefusesOutsideYourGroups(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "mallory", "other_lab")
	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive/token", "application/json")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", resp.StatusCode)
	}
}

// Not connected is an ordinary state, and the answer says where to go
// rather than being an error the page has to interpret.
func TestTokenEndpointTellsAnUnconnectedCallerWhereToGo(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")

	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive/token", "application/json")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409", resp.StatusCode)
	}
	var body struct {
		Connected  bool   `json:"connected"`
		ConnectURL string `json:"connect_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Connected || body.ConnectURL != "/api/v1/connect/builtin:gdrive" {
		t.Errorf("body = %+v, want connected false and the entry point", body)
	}
}

// A revoked grant is reported as needing consent, and the stored row is
// LEFT ALONE: a lapse is often temporary, and destroying the token
// forces a consent screen for what may be an afternoon's change.
func TestTokenEndpointReportsARevokedGrantWithoutDestroyingIt(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")
	f.connectAs(t, c)

	stored, err := f.h.connectorStore().RefreshToken(t.Context(), "alice",
		connectors.MustParse("builtin:gdrive"))
	if err != nil {
		t.Fatal(err)
	}
	f.google.Revoke(string(stored))

	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive/token", "application/json")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409", resp.StatusCode)
	}
	var body struct {
		NeedsConsent bool `json:"needs_consent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.NeedsConsent {
		t.Error("a revoked grant was not reported as needing consent")
	}

	if _, found, _ := f.h.connectorStore().Credential(t.Context(), "alice",
		connectors.MustParse("builtin:gdrive")); !found {
		t.Error("the stored credential was destroyed over a revocation")
	}
}

// Issuing a token counts as use, which is what lets a connector report
// whether anyone uses it and warn a user before a job holds.
func TestIssuingATokenRecordsUse(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")
	f.connectAs(t, c)

	id := connectors.MustParse("builtin:gdrive")
	if _, lastUsed, _ := f.h.connectorStore().CountCredentials(t.Context(), id); !lastUsed.IsZero() {
		t.Fatalf("last use was %v before any token was issued", lastUsed)
	}

	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive/token", "application/json")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	users, lastUsed, err := f.h.connectorStore().CountCredentials(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if users != 1 || lastUsed.IsZero() {
		t.Errorf("users=%d lastUsed=%v; issuing a token should count as use", users, lastUsed)
	}
}

// One user's token is never served to another.
func TestTokenEndpointNeverCrossesUsers(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	alice := f.login(t, "alice", "smith_lab")
	bob := f.login(t, "bob", "smith_lab")
	f.connectAs(t, alice)

	// bob is in the group but has not connected.
	resp := f.get(t, bob, "/api/v1/connect/builtin:gdrive/token", "application/json")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("bob was served a token: %s", readBody(t, resp))
	}
}
