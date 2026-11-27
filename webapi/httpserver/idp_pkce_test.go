package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"

	"github.com/htcondor/golang-htcondor/logging"
)

// idpWithPublicClient brings up a server with the IDP enabled, one active
// user and one public client, and returns the session cookie that user's
// browser would carry.
func idpWithPublicClient(t *testing.T, redirectURI string) (*Server, *http.Cookie) {
	t.Helper()
	logger, err := logging.New(&logging.Config{OutputPath: "stderr"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	dbPath := t.TempDir() + "/idp_pkce.db"
	server, err := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		ScheddName:   "test-schedd",
		ScheddAddr:   "127.0.0.1:9618",
		Logger:       logger,
		OAuth2DBPath: dbPath,
		EnableIDP:    true,
		IDPDBPath:    dbPath,
		IDPIssuer:    "http://localhost:8080",
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	t.Cleanup(func() {
		if server.idpProvider != nil {
			_ = server.idpProvider.Close()
		}
	})

	ctx := context.Background()
	if err := server.idpProvider.storage.CreateUser(ctx, "pkceuser", "pkcepassword", "active"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	// Public and secretless, exactly as swagger-client is registered.
	client := &fosite.DefaultClient{
		ID:            "public-no-secret",
		Secret:        nil,
		RedirectURIs:  []string{redirectURI},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"},
		Scopes:        []string{"openid", "profile"},
		Public:        true,
	}
	if err := server.idpProvider.storage.CreateClient(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	form := url.Values{}
	form.Set("username", "pkceuser")
	form.Set("password", "pkcepassword")
	form.Set("redirect_uri", "/idp/authorize")
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/idp/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.handleIDPLogin(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Value != "" {
			return server, c
		}
	}
	t.Fatalf("login set no session cookie: %d %s", rec.Code, rec.Body.String())
	return nil, nil
}

// idpAuthorize drives /idp/authorize as a logged-in browser and reports the
// redirect the server sent.
func idpAuthorize(t *testing.T, server *Server, cookie *http.Cookie, query string) *url.URL {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/idp/authorize?"+query, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	server.handleIDPAuthorize(rec, req)

	loc := rec.Result().Header.Get("Location")
	if loc == "" {
		t.Fatalf("authorize sent no redirect: %d %s", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect %q: %v", loc, err)
	}
	return u
}

// A public client holds no secret, so fosite authenticates it on client_id
// alone and PKCE is the only thing binding the authorization code to the
// requester. The IDP auto-approves -- handleIDPAuthorize has no consent
// screen -- so nothing else stands behind the code either.
func TestIDPRefusesAPublicClientWithoutPKCE(t *testing.T) {
	const redirectURI = "http://localhost:8080/callback"
	server, cookie := idpWithPublicClient(t, redirectURI)

	got := idpAuthorize(t, server, cookie, url.Values{
		"client_id":     {"public-no-secret"},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"scope":         {"openid"},
		"state":         {"state-without-a-challenge"},
	}.Encode())

	if code := got.Query().Get("code"); code != "" {
		t.Fatalf("a public client with no code_challenge was given an authorization code: %s", got)
	}
	if e := got.Query().Get("error"); e != "invalid_request" {
		t.Errorf("error = %q, want invalid_request: %s", e, got)
	}
}

// The positive control: supplying a challenge still gets a code, so the
// refusal above is not satisfied by an IDP that refuses everyone.
func TestIDPIssuesToAPublicClientWithPKCE(t *testing.T) {
	const redirectURI = "http://localhost:8080/callback"
	const verifier = "pkce-positive-control-0123456789abcdefghijklmnop"
	server, cookie := idpWithPublicClient(t, redirectURI)

	got := idpAuthorize(t, server, cookie, url.Values{
		"client_id":             {"public-no-secret"},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid"},
		"state":                 {"state-with-a-challenge"},
		"code_challenge":        {pkceChallenge(t, "S256", verifier)},
		"code_challenge_method": {"S256"},
	}.Encode())

	if code := got.Query().Get("code"); code == "" {
		t.Fatalf("a public client that sent a challenge got no code: %s", got)
	}
}

// Both providers, asserted together. This is the third sibling of the
// PKCE-asymmetry class: the MCP provider set the flag, the IDP did not, and
// the IDP's own metadata advertised code_challenge_methods_supported while
// enforcing nothing. A provider added later without the flag should fail
// here rather than in review.
func TestEveryProviderEnforcesPKCEForPublicClients(t *testing.T) {
	idpServer, _ := idpWithPublicClient(t, "http://localhost:8080/callback")
	mcpServer := newMCPScopeServer(t, false)

	configs := map[string]*fosite.Config{}
	if idpServer.idpProvider != nil {
		configs["idp"] = idpServer.idpProvider.config
	}
	if mcpServer.oauth2Provider != nil {
		configs["oauth2"] = mcpServer.oauth2Provider.config
	}
	if len(configs) != 2 {
		t.Fatalf("precondition: built %d providers, want both", len(configs))
	}
	for name, cfg := range configs {
		if !cfg.GetEnforcePKCEForPublicClients(context.Background()) {
			t.Errorf("%s provider does not enforce PKCE for public clients", name)
		}
	}
}
