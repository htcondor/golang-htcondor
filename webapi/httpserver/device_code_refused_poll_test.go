package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/ory/fosite"
)

// pendingDeviceCode issues a device code to deviceIssuedClient and leaves it
// unapproved, which is the state a client spends almost all of its polling in.
func pendingDeviceCode(t *testing.T, server *Server) string {
	t.Helper()
	ctx := context.Background()

	storage := server.oauth2Provider.GetStorage()
	client, err := storage.GetClient(ctx, deviceIssuedClient)
	if err != nil {
		t.Fatalf("load client %s: %v", deviceIssuedClient, err)
	}
	handler := NewDeviceCodeHandler(storage, server.oauth2Provider.config)
	resp, err := handler.HandleDeviceAuthorizationRequest(ctx, client, []string{"mcp:read"}, "")
	if err != nil {
		t.Fatalf("device authorization for %s: %v", deviceIssuedClient, err)
	}
	return resp.DeviceCode
}

// Refusing a poll must leave the code exactly as it was found.
//
// A device code is a bearer secret presented under whatever client_id its
// holder chooses, so the binding check refuses polls that reach the endpoint
// in the ordinary way. If that refusal consumes the code, the refusal is
// itself the damage: the rightful client's next poll is told the code is
// already used, and the user_code cannot be re-approved either, because
// ApproveDeviceCodeSession matches only a pending row. The person at the
// browser has to start the whole flow over, which is precisely what an
// attacker holding a leaked code would want if tokens are out of reach.
func TestARefusedPollDoesNotSpendTheCode(t *testing.T) {
	server, _ := newProvenanceServer(t)
	secret := insertConfidentialClient(t, server, deviceIssuedClient,
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
	if got := oauthErrorField(t, rec); got != "invalid_grant" {
		t.Fatalf("foreign poll: error = %q, want invalid_grant", got)
	}

	// Asserted on the row rather than only through a second poll, because
	// the poll pacing refuses first and would hide a spent code behind a
	// slow_down.
	if got := deviceCodeStatus(t, server, code); got != "approved" {
		t.Errorf("after a refused poll the code is %q, want approved", got)
	}

	// The same code, now redeemed by the client it belongs to.
	rec = postDeviceCodeGrant(t, server, url.Values{
		"grant_type":    {deviceCodeGrant},
		"device_code":   {code},
		"client_id":     {deviceIssuedClient},
		"client_secret": {secret},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("the issuing client was refused after a foreign poll: status %d: %s",
			rec.Code, rec.Body.String())
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode token body: %v", err)
	}
	if body.AccessToken == "" {
		t.Error("no access token in the response")
	}
}

// The same ordering fault, one write earlier and cheaper to exercise: a
// refused poll must not count against the rightful client's poll pacing.
//
// MarkDeviceCodePolled is keyed on the code alone, so a foreign poller running
// at the minimum gap can hold the rightful client at slow_down indefinitely --
// against a still-pending code, with no approval needed and nothing consumed.
// A binding check that runs only before the invalidation does not reach this;
// it has to run before the first write.
func TestARefusedPollDoesNotStarveTheRightfulClient(t *testing.T) {
	server, _ := newProvenanceServer(t)
	secret := insertConfidentialClient(t, server, deviceIssuedClient,
		[]string{deviceCodeGrant}, []string{"mcp:read"})
	otherSecret := insertConfidentialClient(t, server, "device-other",
		[]string{deviceCodeGrant}, []string{"mcp:read"})

	code := pendingDeviceCode(t, server)

	rec := postDeviceCodeGrant(t, server, url.Values{
		"grant_type":    {deviceCodeGrant},
		"device_code":   {code},
		"client_id":     {"device-other"},
		"client_secret": {otherSecret},
	})
	if got := oauthErrorField(t, rec); got != "invalid_grant" {
		t.Fatalf("foreign poll: error = %q, want invalid_grant", got)
	}

	// The rightful client's first poll. It is pending, not too frequent:
	// nothing it did has used up its pacing budget.
	rec = postDeviceCodeGrant(t, server, url.Values{
		"grant_type":    {deviceCodeGrant},
		"device_code":   {code},
		"client_id":     {deviceIssuedClient},
		"client_secret": {secret},
	})
	if got := oauthErrorField(t, rec); got != "authorization_pending" {
		t.Errorf("rightful first poll: error = %q, want authorization_pending", got)
	}
}

// The deprecated entry point keeps its original behaviour: no binding check,
// and the code spent on the way out. Callers outside this repo may have built
// their own check around it, and silently moving the refusal inside would
// change what their code does.
func TestHandleDeviceAccessRequestStillSkipsTheBinding(t *testing.T) {
	server, _ := newProvenanceServer(t)
	insertConfidentialClient(t, server, deviceIssuedClient,
		[]string{deviceCodeGrant}, []string{"mcp:read"})

	code := approvedDeviceCode(t, server)
	handler := NewDeviceCodeHandler(server.oauth2Provider.GetStorage(), server.oauth2Provider.config)

	//nolint:staticcheck // SA1019: pinning the deprecated behaviour is the point.
	request, err := handler.HandleDeviceAccessRequest(context.Background(), code, &fosite.DefaultSession{})
	if err != nil {
		t.Fatalf("deprecated entry point refused a valid code: %v", err)
	}
	if got := request.GetClient().GetID(); got != deviceIssuedClient {
		t.Errorf("client = %q, want %q", got, deviceIssuedClient)
	}
}

// deviceCodeStatus reads the stored status of a device code.
func deviceCodeStatus(t *testing.T, server *Server, deviceCode string) string {
	t.Helper()
	var status string
	err := server.oauth2Provider.GetStorage().db.QueryRowContext(context.Background(),
		`SELECT status FROM oauth2_device_codes WHERE device_code = ?`,
		sessionKey(deviceCode)).Scan(&status)
	if err != nil {
		t.Fatalf("read device code status: %v", err)
	}
	return status
}
