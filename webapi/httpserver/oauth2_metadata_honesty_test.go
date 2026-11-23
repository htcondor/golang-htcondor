package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// A discovery document is a promise. These tests hold each list in one to
// what the server will actually do when a client takes it at face value.
//
// The failure they exist to prevent is specific and was live:
// code_challenge_methods_supported advertised "plain" from the commit that
// first added the document, while EnablePKCEPlainChallengeMethod was never
// set, so fosite refused it. fosite checks the method in
// HandleAuthorizeEndpointRequest, which runs when the authorization
// response is built -- after consent. A client that believed the list
// therefore took the user through sign-in and approval before landing on
// `error=invalid_request: Clients must use code_challenge_method=S256,
// plain is not allowed.`
//
// Which is why these do not compare the document against a second copy of
// the same constant. Each one reads the advertised list and then exercises
// the path, so a method that is advertised has to survive the flow.

// metadataDoc fetches and decodes a discovery document from its handler.
func metadataDoc(t *testing.T, h func(http.ResponseWriter, *http.Request), path string) map[string]any {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return doc
}

// strs pulls a JSON string array out of a decoded document.
func strs(t *testing.T, doc map[string]any, field string) []string {
	t.Helper()
	raw, ok := doc[field]
	if !ok {
		t.Fatalf("metadata has no %s", field)
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s is %T, want an array", field, raw)
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s contains %T, want strings", field, v)
		}
		out = append(out, s)
	}
	return out
}

// pkceChallenge builds a challenge for the named method from a verifier.
func pkceChallenge(t *testing.T, method, verifier string) string {
	t.Helper()
	switch method {
	case "plain":
		return verifier
	case "S256":
		sum := sha256.Sum256([]byte(verifier))
		return base64.RawURLEncoding.EncodeToString(sum[:])
	default:
		t.Fatalf("no challenge known for advertised method %q -- if the server really supports it, teach this helper", method)
		return ""
	}
}

// authorizeWithPKCE drives authorize and consent with a code challenge in
// the named method, and reports the redirect the server finally sent.
//
// It goes all the way through consent on purpose: that is where fosite
// checks the challenge method, so a test that stopped at
// NewAuthorizeRequest would pass for a method the server refuses.
func authorizeWithPKCE(t *testing.T, f *reauthFixture, method, challenge string) *url.URL {
	t.Helper()
	ctx := context.Background()
	authorizeReq := httptest.NewRequestWithContext(ctx, http.MethodGet, "/mcp/oauth2/authorize", nil)
	authorizeReq.URL.RawQuery = url.Values{
		"response_type":         {"code"},
		"client_id":             {f.clientID},
		"redirect_uri":          {"http://localhost:8080/callback"},
		"scope":                 {"mcp:read"},
		"state":                 {"teststate1234567890"},
		"code_challenge":        {challenge},
		"code_challenge_method": {method},
	}.Encode()
	ar, err := f.server.oauth2Provider.GetProvider().NewAuthorizeRequest(ctx, authorizeReq)
	if err != nil {
		t.Fatalf("NewAuthorizeRequest with code_challenge_method=%s: %v", method, err)
	}
	state, err := f.server.oauth2StateStore.GenerateState()
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	f.server.oauth2StateStore.StoreWithUsername(state, ar, "", "pkceuser", nil)

	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp/oauth2/consent",
		strings.NewReader(url.Values{
			"state":          {state},
			"action":         {"approve"},
			"scope":          {"mcp:read"},
			consentCSRFField: {f.server.consentCSRFToken("pkceuser", state)},
		}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.server.handleOAuth2Consent(w, req)
	if w.Code != http.StatusFound && w.Code != http.StatusSeeOther {
		t.Fatalf("consent with code_challenge_method=%s returned %d, want a redirect: %s",
			method, w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parsing the post-consent redirect: %v", err)
	}
	return loc
}

// Every method the document offers has to get a client all the way to an
// authorization code.
func TestEveryAdvertisedPKCEMethodSurvivesConsent(t *testing.T) {
	f := newReauthFixture(t, Config{})
	defer func() { _ = f.server.oauth2Provider.Close() }()

	methods := strs(t, metadataDoc(t, f.server.handleOAuth2Metadata,
		"/.well-known/oauth-authorization-server"), "code_challenge_methods_supported")

	// Without this the loop below passes by being empty, which is how a
	// test like this quietly stops testing anything.
	if !slices.Contains(methods, "S256") {
		t.Fatalf("code_challenge_methods_supported = %v, must offer S256: it is the method "+
			"fosite accepts and the only one PKCE-using clients can rely on", methods)
	}

	const verifier = "a-verifier-long-enough-to-satisfy-rfc-7636-43ch"
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			loc := authorizeWithPKCE(t, f, method, pkceChallenge(t, method, verifier))
			if errCode := loc.Query().Get("error"); errCode != "" {
				t.Errorf("code_challenge_method=%s is advertised but refused after the user consented: "+
					"%s: %s", method, errCode, loc.Query().Get("error_description"))
			}
			if loc.Query().Get("code") == "" {
				t.Errorf("code_challenge_method=%s is advertised but produced no authorization code (redirect %q)",
					method, loc)
			}
		})
	}
}

// The other half: "plain" is left out because it is refused, not because
// somebody trimmed a list. If plain is ever enabled, this fails and says to
// advertise it.
func TestPlainPKCEIsRefusedAndSoIsNotAdvertised(t *testing.T) {
	f := newReauthFixture(t, Config{})
	defer func() { _ = f.server.oauth2Provider.Close() }()

	const verifier = "a-verifier-long-enough-to-satisfy-rfc-7636-43ch"
	loc := authorizeWithPKCE(t, f, "plain", verifier)
	refused := loc.Query().Get("error") != ""

	methods := strs(t, metadataDoc(t, f.server.handleOAuth2Metadata,
		"/.well-known/oauth-authorization-server"), "code_challenge_methods_supported")
	advertised := slices.Contains(methods, "plain")

	switch {
	case refused && advertised:
		t.Errorf("plain is advertised and refused (%s); the refusal lands after consent, so drop it from the list",
			loc.Query().Get("error_description"))
	case !refused && !advertised:
		t.Errorf("plain now works but is not advertised; add it to code_challenge_methods_supported, " +
			"or leave EnablePKCEPlainChallengeMethod false")
	}
}

// A public client authenticates with "none", and a public client can exist
// whether or not CIMD is on. The method used to be advertised only
// alongside CIMD, so turning CIMD off told public clients their only method
// was unsupported.
func TestNoneAuthMethodIsAdvertisedWhateverCIMDSays(t *testing.T) {
	for _, cimd := range []bool{false, true} {
		name := "cimd-off"
		if cimd {
			name = "cimd-on"
		}
		t.Run(name, func(t *testing.T) {
			cfg := newTestConfig(t)
			cfg.EnableMCP = true
			cfg.OAuth2Issuer = "http://localhost:8080"
			cfg.MCPCIMDEnabled = cimd
			server, err := NewServer(cfg)
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			defer func() { _ = server.oauth2Provider.Close() }()

			doc := metadataDoc(t, server.handleOAuth2Metadata,
				"/.well-known/oauth-authorization-server")
			methods := strs(t, doc, "token_endpoint_auth_methods_supported")
			if !slices.Contains(methods, "none") {
				t.Errorf("token_endpoint_auth_methods_supported = %v with CIMD=%v; "+
					"a public client has only \"none\", and one can be created "+
					"through POST /api/v1/integrations or DCR regardless of CIMD",
					methods, cimd)
			}
			// CIMD still governs its own advertisement, which is the
			// thing it actually describes.
			_, claimsCIMD := doc["client_id_metadata_document_supported"]
			if claimsCIMD != cimd {
				t.Errorf("client_id_metadata_document_supported present=%v, want %v", claimsCIMD, cimd)
			}
		})
	}
}

// RFC 8414's pointer to developer documentation. Someone wiring up a
// third-party integration needs to find the endpoint that names the
// signed-in user; without this they have to be told where to look.
func TestServiceDocumentationPointsSomewhereServed(t *testing.T) {
	f := newReauthFixture(t, Config{})
	defer func() { _ = f.server.oauth2Provider.Close() }()

	doc := metadataDoc(t, f.server.handleOAuth2Metadata,
		"/.well-known/oauth-authorization-server")
	raw, ok := doc["service_documentation"]
	if !ok {
		t.Fatal("metadata has no service_documentation")
	}
	got, _ := raw.(string)
	issuer, _ := doc["issuer"].(string)
	if issuer == "" || !strings.HasPrefix(got, issuer) {
		t.Fatalf("service_documentation = %q, want a URL under the issuer %q", got, issuer)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("service_documentation %q does not parse: %v", got, err)
	}
	// It has to be a path this server routes, or it is a dead link.
	if u.Path != "/docs" {
		t.Errorf("service_documentation path = %q; /docs is the self-hosted API reference", u.Path)
	}
}

// Nothing here signs a resource response, so the protected-resource
// document must not name an algorithm for doing it. It used to claim RS256,
// which is the internal IdP's ID-token key and unrelated to this resource.
func TestProtectedResourceDoesNotClaimToSignResponses(t *testing.T) {
	f := newReauthFixture(t, Config{})
	defer func() { _ = f.server.oauth2Provider.Close() }()

	doc := metadataDoc(t, f.server.handleOAuth2ProtectedResourceMetadata,
		"/.well-known/oauth-protected-resource")
	if v, ok := doc["resource_signing_alg_values_supported"]; ok {
		t.Errorf("protected-resource metadata advertises resource_signing_alg_values_supported=%v, "+
			"but responses are plain JSON and nothing signs them", v)
	}
	// The fields that do describe this resource are still there.
	for _, field := range []string{"resource", "authorization_servers", "bearer_methods_supported"} {
		if _, ok := doc[field]; !ok {
			t.Errorf("protected-resource metadata lost %s", field)
		}
	}
}
