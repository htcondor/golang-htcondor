package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"
)

// deviceIssuedClient is the client every code in this file is issued to; the
// point under test is which client redeems it, not which issued it.
const deviceIssuedClient = "device-issued"

// approvedDeviceCode issues a device code to deviceIssuedClient and approves it
// for alice, returning the code a client would then poll with.
func approvedDeviceCode(t *testing.T, server *Server) string {
	t.Helper()
	const (
		clientID = deviceIssuedClient
		subject  = "alice"
	)
	ctx := context.Background()

	storage := server.oauth2Provider.GetStorage()
	client, err := storage.GetClient(ctx, clientID)
	if err != nil {
		t.Fatalf("load client %s: %v", clientID, err)
	}

	handler := NewDeviceCodeHandler(storage, server.oauth2Provider.config)
	resp, err := handler.HandleDeviceAuthorizationRequest(ctx, client, []string{"mcp:read"}, "")
	if err != nil {
		t.Fatalf("device authorization for %s: %v", clientID, err)
	}
	if err := storage.ApproveDeviceCodeSession(ctx, resp.UserCode,
		subject, &fosite.DefaultSession{Subject: subject}); err != nil {
		t.Fatalf("approve device code: %v", err)
	}
	return resp.DeviceCode
}

func postDeviceCodeGrant(t *testing.T, server *Server, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/mcp/oauth2/token",
		strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.handleOAuth2Token(rec, r)
	return rec
}

func oauthErrorField(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

// A device code is a bearer secret, so holding one must not be enough: RFC 8628
// 3.5 requires it to have been issued to the client redeeming it. This flow
// does not pass through fosite's NewAccessRequest, so neither the client
// authentication nor the binding comes for free.
func TestDeviceCodeRefusedForAnotherClient(t *testing.T) {
	server, _ := newProvenanceServer(t)
	insertConfidentialClient(t, server, deviceIssuedClient,
		[]string{deviceCodeGrant}, []string{"mcp:read"})
	otherSecret := insertConfidentialClient(t, server, "device-other",
		[]string{deviceCodeGrant}, []string{"mcp:read"})

	code := approvedDeviceCode(t, server)

	rec := postDeviceCodeGrant(t, server, url.Values{
		"grant_type":    {deviceCodeGrant},
		"device_code":   {code},
		"client_id":     {"device-other"},
		"client_secret": {otherSecret},
	})

	if rec.Code == http.StatusOK {
		t.Fatalf("a client the code was not issued to was given tokens: %s", rec.Body.String())
	}
	if got := oauthErrorField(t, rec); got != "invalid_grant" {
		t.Errorf("error = %q, want invalid_grant (RFC 8628 3.5)", got)
	}
	if strings.Contains(rec.Body.String(), "access_token") {
		t.Error("the refusal body carries an access token")
	}
}

// Presenting no client at all must not work either: before this, device_code
// was the only field the handler read. RFC 8628 3.4 requires client_id.
func TestDeviceCodeRefusedWithNoClient(t *testing.T) {
	server, _ := newProvenanceServer(t)
	insertConfidentialClient(t, server, deviceIssuedClient,
		[]string{deviceCodeGrant}, []string{"mcp:read"})

	code := approvedDeviceCode(t, server)

	rec := postDeviceCodeGrant(t, server, url.Values{
		"grant_type":  {deviceCodeGrant},
		"device_code": {code},
	})

	if rec.Code == http.StatusOK {
		t.Fatalf("an unauthenticated caller was given tokens: %s", rec.Body.String())
	}
	if got := oauthErrorField(t, rec); got != "invalid_request" {
		t.Errorf("error = %q, want invalid_request", got)
	}
}

// The positive control: the client the code was issued to still gets its
// tokens. Without this the two refusals above are satisfied by a handler that
// refuses everything.
func TestDeviceCodeAcceptedForTheIssuingClient(t *testing.T) {
	server, _ := newProvenanceServer(t)
	secret := insertConfidentialClient(t, server, deviceIssuedClient,
		[]string{deviceCodeGrant}, []string{"mcp:read"})

	code := approvedDeviceCode(t, server)

	rec := postDeviceCodeGrant(t, server, url.Values{
		"grant_type":    {deviceCodeGrant},
		"device_code":   {code},
		"client_id":     {deviceIssuedClient},
		"client_secret": {secret},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("the issuing client was refused: status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode token body: %v", err)
	}
	if body.AccessToken == "" {
		t.Error("no access token in the response")
	}
}

// A client that presents only client_id -- what the shipped CLI does, and what
// dynamic registration produces for token_endpoint_auth_method "none" -- must
// still be able to redeem its own code. Binding must not become an
// authentication requirement by accident.
func TestDeviceCodeAcceptedWithClientIDAlone(t *testing.T) {
	server, _ := newProvenanceServer(t)
	insertConfidentialClient(t, server, deviceIssuedClient,
		[]string{deviceCodeGrant}, []string{"mcp:read"})

	code := approvedDeviceCode(t, server)

	rec := postDeviceCodeGrant(t, server, url.Values{
		"grant_type":  {deviceCodeGrant},
		"device_code": {code},
		"client_id":   {deviceIssuedClient},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("client_id alone was refused: status %d: %s", rec.Code, rec.Body.String())
	}
}
