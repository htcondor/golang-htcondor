package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"golang.org/x/crypto/bcrypt"
)

// The same rule as oauth2_metadata_honesty_test.go, applied to the other
// discovery document this server publishes: the internal IdP's. The helpers
// metadataDoc and strs live there.

// The internal IdP composes the explicit (code) flows and nothing else, so
// the code flow is all it may advertise. It listed all seven response-type
// combinations; the six it cannot serve failed the same way plain PKCE did,
// after the consent screen.
func TestIDPAdvertisesOnlyTheFlowsItComposes(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.EnableIDP = true
	cfg.IDPIssuer = "http://localhost:8080"
	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = server.idpProvider.Close() }()

	doc := metadataDoc(t, server.handleIDPMetadata, "/idp/.well-known/openid-configuration")

	if got := strs(t, doc, "response_types_supported"); !slices.Equal(got, []string{"code"}) {
		t.Errorf("response_types_supported = %v, want [code]: idp_provider.go composes "+
			"OAuth2AuthorizeExplicitFactory and OpenIDConnectExplicitFactory, "+
			"neither an implicit nor a hybrid factory", got)
	}
	if got := strs(t, doc, "response_modes_supported"); !slices.Equal(got, []string{"query"}) {
		t.Errorf("response_modes_supported = %v, want [query]: no token ever rides this "+
			"redirect, so there is nothing for the fragment to keep out of the query", got)
	}
	if got := strs(t, doc, "code_challenge_methods_supported"); !slices.Equal(got, []string{"S256"}) {
		t.Errorf("code_challenge_methods_supported = %v, want [S256]", got)
	}

	// And the enforcement half: register a client that claims an implicit
	// response type and watch fosite refuse it anyway.
	hashed, err := bcrypt.GenerateFromPassword([]byte("idp-secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	ctx := context.Background()
	if err := server.idpProvider.storage.CreateClient(ctx, &fosite.DefaultClient{
		ID:            "implicit-hopeful",
		Secret:        hashed,
		RedirectURIs:  []string{"http://localhost:8080/callback"},
		ResponseTypes: []string{"code", "token", "id_token"},
		GrantTypes:    []string{"authorization_code", "implicit"},
		Scopes:        []string{"openid", "profile", "email"},
	}); err != nil {
		t.Fatalf("CreateClient: %v", err)
	}

	// The refusal has to be looked for where it happens. NewAuthorizeRequest
	// checks the response type against the CLIENT's registered list and
	// accepts "token" for a client that claims it; it is NewAuthorizeResponse
	// that finds no composed handler willing to serve it. That is the same
	// late stage as the PKCE check, and in the IdP's handler it is reached
	// after the user has already logged in.
	for _, responseType := range []string{"token", "id_token", "code token", "code id_token"} {
		t.Run(strings.ReplaceAll(responseType, " ", "_"), func(t *testing.T) {
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/idp/authorize", nil)
			req.URL.RawQuery = url.Values{
				"response_type": {responseType},
				"client_id":     {"implicit-hopeful"},
				"redirect_uri":  {"http://localhost:8080/callback"},
				"scope":         {"openid"},
				"state":         {"teststate1234567890"},
				"nonce":         {"noncevalue1234567890"},
			}.Encode()
			ar, err := server.idpProvider.oauth2.NewAuthorizeRequest(ctx, req)
			if err != nil {
				return // refused outright, which is the point
			}
			ar.GrantScope("openid")
			if _, err := server.idpProvider.oauth2.NewAuthorizeResponse(
				ctx, ar, DefaultIDPSession("idpuser")); err == nil {
				t.Errorf("response_type=%s produced an authorization response; if a factory "+
					"for it has been composed, advertise it in response_types_supported",
					responseType)
			}
		})
	}
}
