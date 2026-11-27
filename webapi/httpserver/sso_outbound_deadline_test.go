package httpserver

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/htcondor/golang-htcondor/logging"
)

// Every client getHTTPClient hands out must be bounded.
//
// This client carries the SSO authorization-code exchange, every userinfo
// fetch and the upstream revocation refresh. Nothing downstream bounds them:
// the server's ReadTimeout and WriteTimeout do not cancel a handler's
// context, so an identity provider that accepts the connection and then
// stops answering holds the handler for as long as the peer keeps its own
// connection open. A sign-in hangs rather than failing into the
// "Could not complete the sign-in" page the error path already has.
//
// Asserted on the client rather than through a live stall because the real
// bound is 30 seconds, and a test that waits for it would cost more than it
// proves. The timeout field is the whole mechanism.
func TestEveryOutboundSSOClientHasADeadline(t *testing.T) {
	logger, err := logging.New(&logging.Config{OutputPath: "stderr"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	caFile := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(caFile, []byte("-- not a certificate --"), 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}

	cases := []struct {
		name string
		ca   string
	}{
		// The common deployment: no CA file, and what used to be a bare
		// http.DefaultClient.
		{name: "no CA file", ca: ""},
		// The CA branch builds its own Transport, so unlike
		// DefaultTransport it has no dial or handshake bound either
		// unless this sets them.
		{name: "CA file", ca: caFile},
		// A CA file that cannot be read falls back, and the fallback is
		// a client too.
		{name: "unreadable CA file", ca: t.TempDir() + "/missing.pem"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{logger: logger, tlsCACertFile: tc.ca}
			client := h.getHTTPClient()
			if client == nil {
				t.Fatal("no client")
			}
			if client.Timeout <= 0 {
				t.Errorf("client has no Timeout: an unanswering provider pins the caller")
			}
			if client == http.DefaultClient {
				t.Error("client is http.DefaultClient, which has no Timeout")
			}
			tr, ok := client.Transport.(*http.Transport)
			if !ok {
				// DefaultTransport is shared process-wide, so the bound
				// that matters on that path is Client.Timeout, checked
				// above.
				return
			}
			if tr.TLSHandshakeTimeout <= 0 {
				t.Error("transport has no TLSHandshakeTimeout")
			}
			if tr.ResponseHeaderTimeout <= 0 {
				t.Error("transport has no ResponseHeaderTimeout")
			}
			if tr.DialContext == nil {
				t.Error("transport has no DialContext, so the dial is unbounded")
			}
		})
	}
}

// A provider that hangs must become the same "no opinion" a provider that
// errors already is.
//
// Every oracle fails open, and this file's rationale says an oracle that
// cannot answer must not take the token endpoint down with it. That reasoning
// reaches an error; it never reaches a hang. The per-user floor does not damp
// it either: MarkChecked runs only after a successful refresh, so a hung
// provider never advances LastCheckedAt and every refresh by every affected
// user re-attempts for the length of the outage.
func TestUpstreamOracleDoesNotWaitForeverOnTheProvider(t *testing.T) {
	hang := func(ctx context.Context, _ string) (*oauth2.Token, error) {
		<-ctx.Done() // the provider that answers nothing
		return nil, ctx.Err()
	}
	o := upstreamOracleFor(t, hang, infoWith("idp-sub-alice"), "condor-users")
	o.timeout = 200 * time.Millisecond

	type result struct {
		decision ReauthDecision
		err      error
	}
	done := make(chan result, 1)
	// context.Background() deliberately: a test context with its own
	// deadline would pass whether or not the oracle has one.
	go func() {
		d, err := o.Check(context.Background(), "alice", []string{"mcp:read"})
		done <- result{d, err}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Error("a provider that never answered produced no error")
		}
		if got.decision.Status == UserStatusRevoked {
			t.Error("a timeout revoked the user; it must fail open")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Check never returned: a hanging provider stalls the refresh")
	}
}

// The second leg too: the refresh can succeed and the userinfo fetch hang.
// One deadline covers the whole conversation, so this must come back as well.
func TestUpstreamOracleDoesNotWaitForeverOnUserInfo(t *testing.T) {
	refreshOK := func(_ context.Context, _ string) (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "fresh"}, nil
	}
	hang := func(ctx context.Context, _ string) (*UserInfo, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	o := upstreamOracleFor(t, refreshOK, hang, "condor-users")
	o.timeout = 200 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := o.Check(context.Background(), "alice", []string{"mcp:read"})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("a userinfo endpoint that never answered produced no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Check never returned: a hanging userinfo fetch stalls the refresh")
	}
}
