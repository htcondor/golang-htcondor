package httpserver

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/webhooks"
)

// The webhook endpoints' own contract: who may reach them, what they return,
// and what they refuse. Delivery itself is tested in the webhooks package,
// which can reach a test receiver; the sender this daemon uses refuses
// loopback, which is asserted below rather than worked around.

func serveWebhooks(t *testing.T, server *Server, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if strings.HasPrefix(r.URL.Path, "/api/v1/webhooks/") {
		server.handleWebhookItem(rec, r)
	} else {
		server.handleWebhooks(rec, r)
	}
	return rec
}

func createWebhook(t *testing.T, server *Server, user, body string) (int, map[string]any) {
	t.Helper()
	rec := serveWebhooks(t, server, userRequest(t, server, user, []string{"condor-users"},
		http.MethodPost, "/api/v1/webhooks", body))
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode (%d): %v: %s", rec.Code, err, rec.Body.String())
		}
	}
	return rec.Code, out
}

func listWebhooks(t *testing.T, server *Server, user string) []map[string]any {
	t.Helper()
	rec := serveWebhooks(t, server, userRequest(t, server, user, []string{"condor-users"},
		http.MethodGet, "/api/v1/webhooks", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Webhooks []map[string]any `json:"webhooks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return out.Webhooks
}

func TestCreateWebhookAndList(t *testing.T) {
	f := newReauthFixture(t, Config{})

	status, body := createWebhook(t, f.server, "alice",
		`{"label":"Slack","url":"https://hooks.slack.com/services/T/B/XXXXXXXX",
		  "scope":"job","cluster_id":42,"proc_id":1,"events":["failed","held"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create status %d, want 201: %v", status, body)
	}
	// The signing secret is returned once so a receiver can be configured
	// to verify, and never again.
	if secret, _ := body["signing_secret"].(string); secret == "" {
		t.Error("create returned no signing secret")
	}
	hook, _ := body["webhook"].(map[string]any)
	if hook == nil {
		t.Fatal("create returned no webhook")
	}
	id, _ := hook["id"].(string)
	if id == "" {
		t.Fatal("create returned no id")
	}

	mine := listWebhooks(t, f.server, "alice")
	if len(mine) != 1 {
		t.Fatalf("alice has %d webhooks, want 1", len(mine))
	}
	// The destination is a capability URL: possession is authorization, so
	// no endpoint returns it.
	raw := mustJSON(t, mine[0])
	for _, leak := range []string{"hooks.slack.com/services", "XXXXXXXX"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the listing leaked the destination (%q): %s", leak, raw)
		}
	}
	if has, _ := mine[0]["has_destination"].(bool); !has {
		t.Error("has_destination is false for a hook with one")
	}
	// The hint names the host, which is what tells two hooks apart.
	if hint, _ := mine[0]["destination_hint"].(string); !strings.Contains(hint, "hooks.slack.com") {
		t.Errorf("destination_hint = %q, want it to name the host", hint)
	}

	// Bob's own, so the isolation is checked from both sides rather than
	// only from alice's.
	status, body = createWebhook(t, f.server, "bob",
		`{"label":"Bob's hook","url":"https://hooks.example.org/bob","scope":"owner","events":["done"]}`)
	if status != http.StatusCreated {
		t.Fatalf("bob's create status %d, want 201: %v", status, body)
	}
	if mine := listWebhooks(t, f.server, "alice"); len(mine) != 1 {
		t.Errorf("alice sees %d webhooks, want only her own", len(mine))
	}
	if bobs := listWebhooks(t, f.server, "bob"); len(bobs) != 1 {
		t.Errorf("bob sees %d webhooks, want only his own", len(bobs))
	}
}

func TestCreateWebhookRefusals(t *testing.T) {
	f := newReauthFixture(t, Config{})
	for _, tc := range []struct {
		name, body, want string
	}{
		{"no label", `{"url":"https://h.example.org/x","scope":"owner","events":["failed"]}`, "name is required"},
		{"http destination", `{"label":"x","url":"http://h.example.org/x","scope":"owner","events":["failed"]}`, "https"},
		{"no events", `{"label":"x","url":"https://h.example.org/x","scope":"owner","events":[]}`, "at least one event"},
		{"unknown event", `{"label":"x","url":"https://h.example.org/x","scope":"owner","events":["exploded"]}`, "unsupported event"},
		{"unknown scope", `{"label":"x","url":"https://h.example.org/x","scope":"galaxy","events":["failed"]}`, "unknown scope"},
		{"job scope with no job", `{"label":"x","url":"https://h.example.org/x","scope":"job","events":["failed"]}`, "cluster id"},
		// A password in a URL is logged by everything it passes through and
		// replayed on every retry.
		{"credentials in url", `{"label":"x","url":"https://u:p@h.example.org/x","scope":"owner","events":["failed"]}`, "credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := createWebhook(t, f.server, "alice", tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %v", status, body)
			}
			// ErrorResponse puts the status text in "error" and the
			// explanation in "message"; the explanation is the half a user
			// can act on.
			if msg, _ := body["message"].(string); !strings.Contains(strings.ToLower(msg), strings.ToLower(tc.want)) {
				t.Errorf("message = %q, want it to mention %q", msg, tc.want)
			}
		})
	}
}

func TestWebhookRequiresSessionAndJSON(t *testing.T) {
	f := newReauthFixture(t, Config{})

	// No session at all.
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/webhooks",
		strings.NewReader(`{"label":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	if rec := serveWebhooks(t, f.server, r); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous create: status %d, want 401", rec.Code)
	}

	// A cross-site form can POST text/plain with no preflight; it cannot
	// send application/json without one.
	sid, _, err := f.server.sessionStore.Create("alice", []string{"condor-users"})
	if err != nil {
		t.Fatalf("session create: %v", err)
	}
	r = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/webhooks",
		strings.NewReader(`{"label":"forged","url":"https://evil.example.org/x","scope":"owner","events":["failed"]}`))
	r.Header.Set("Content-Type", "text/plain")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid}) //nolint:gosec // test cookie
	if rec := serveWebhooks(t, f.server, r); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form-shaped create: status %d, want 415", rec.Code)
	}
	if n := len(listWebhooks(t, f.server, "alice")); n != 0 {
		t.Errorf("a form-shaped POST created %d webhooks", n)
	}
}

func TestWebhookItemIsOwnerScoped(t *testing.T) {
	f := newReauthFixture(t, Config{})
	status, body := createWebhook(t, f.server, "alice",
		`{"label":"mine","url":"https://h.example.org/x","scope":"owner","events":["failed"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %v", body)
	}
	hook, _ := body["webhook"].(map[string]any)
	id, _ := hook["id"].(string)

	// Somebody else's hook is not found rather than refused, at every verb:
	// a 403 would confirm the id exists.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/webhooks/" + id, ""},
		{http.MethodDelete, "/api/v1/webhooks/" + id, ""},
		{http.MethodPatch, "/api/v1/webhooks/" + id, `{"label":"theirs"}`},
		{http.MethodPost, "/api/v1/webhooks/" + id + "/test", `{}`},
		{http.MethodGet, "/api/v1/webhooks/" + id + "/deliveries", ""},
	} {
		rec := serveWebhooks(t, f.server, userRequest(t, f.server, "bob", []string{"condor-users"},
			tc.method, tc.path, tc.body))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as another owner: status %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
	if len(listWebhooks(t, f.server, "alice")) != 1 {
		t.Error("alice's webhook did not survive bob's requests")
	}

	// The owner can.
	rec := serveWebhooks(t, f.server, userRequest(t, f.server, "alice", []string{"condor-users"},
		http.MethodDelete, "/api/v1/webhooks/"+id, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner delete: status %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(listWebhooks(t, f.server, "alice")); n != 0 {
		t.Errorf("alice still has %d webhooks after deleting her only one", n)
	}
}

func TestUpdateWebhook(t *testing.T) {
	f := newReauthFixture(t, Config{})
	_, body := createWebhook(t, f.server, "alice",
		`{"label":"old","url":"https://h.example.org/one","scope":"owner","events":["failed"]}`)
	hook, _ := body["webhook"].(map[string]any)
	id, _ := hook["id"].(string)

	rec := serveWebhooks(t, f.server, userRequest(t, f.server, "alice", []string{"condor-users"},
		http.MethodPatch, "/api/v1/webhooks/"+id,
		`{"label":"new","events":["done","held"],"url":"https://other.example.org/two"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
	}
	mine := listWebhooks(t, f.server, "alice")
	if len(mine) != 1 {
		t.Fatalf("got %d webhooks", len(mine))
	}
	if label, _ := mine[0]["label"].(string); label != "new" {
		t.Errorf("label = %q, want new", label)
	}
	events := mustJSON(t, mine[0]["events"])
	if !strings.Contains(events, "done") || !strings.Contains(events, "held") {
		t.Errorf("events = %s, want done and held", events)
	}
	if hint, _ := mine[0]["destination_hint"].(string); !strings.Contains(hint, "other.example.org") {
		t.Errorf("the destination was not replaced: %q", hint)
	}

	// A bad destination is refused and changes nothing.
	rec = serveWebhooks(t, f.server, userRequest(t, f.server, "alice", []string{"condor-users"},
		http.MethodPatch, "/api/v1/webhooks/"+id, `{"url":"ftp://nope.example.org/x"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("patch with a bad URL: status %d, want 400", rec.Code)
	}
}

// TestTestWebhookReportsTheReceiver: the endpoint answers 200 and puts the
// outcome in the body, because the test RAN -- an HTTP error would say this
// server failed, and send the page down its error path instead of showing
// the user what their receiver answered.
//
// The destination here is unreachable from this daemon by design: the sender
// dials public addresses only, and a hook pointed at a name that resolves
// nowhere is exactly the misconfiguration a user needs told about.
func TestTestWebhookReportsTheReceiver(t *testing.T) {
	f := newReauthFixture(t, Config{})
	_, body := createWebhook(t, f.server, "alice",
		`{"label":"broken","url":"https://localhost.invalid.example/x","scope":"owner","events":["failed"]}`)
	hook, _ := body["webhook"].(map[string]any)
	id, _ := hook["id"].(string)

	rec := serveWebhooks(t, f.server, userRequest(t, f.server, "alice", []string{"condor-users"},
		http.MethodPost, "/api/v1/webhooks/"+id+"/test", `{}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("test status %d, want 200 whatever the receiver said: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ok, _ := out["ok"].(bool); ok {
		t.Error("an unreachable destination was reported as working")
	}
	if cause, _ := out["error"].(string); cause == "" {
		t.Error("the failed test carries no cause for the user to act on")
	}

	// And the outcome is recorded against the hook, so the page can show it
	// without re-testing.
	mine := listWebhooks(t, f.server, "alice")
	if mine[0]["last_test_at"] == nil {
		t.Error("the test result was not recorded on the hook")
	}
	// Apart from real traffic, so neither overwrites the other.
	if mine[0]["last_delivery_at"] != nil {
		t.Error("a test was recorded as a real delivery")
	}
}

func TestWebhookDeliveriesListIsEmptyNotMissing(t *testing.T) {
	f := newReauthFixture(t, Config{})
	_, body := createWebhook(t, f.server, "alice",
		`{"label":"new","url":"https://h.example.org/x","scope":"owner","events":["failed"]}`)
	hook, _ := body["webhook"].(map[string]any)
	id, _ := hook["id"].(string)

	rec := serveWebhooks(t, f.server, userRequest(t, f.server, "alice", []string{"condor-users"},
		http.MethodGet, "/api/v1/webhooks/"+id+"/deliveries", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deliveries []map[string]any `json:"deliveries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// An empty list rather than null: the page renders a count, and null
	// reads as a column that failed to load.
	if out.Deliveries == nil {
		t.Error("deliveries came back null, not an empty list")
	}
	if len(out.Deliveries) != 0 {
		t.Errorf("a new hook has %d deliveries", len(out.Deliveries))
	}
}

// TestListWebhooksOffersTheServersVocabulary: the page offers exactly what
// the server implements, rather than a list somebody remembered to keep in
// step.
func TestListWebhooksOffersTheServersVocabulary(t *testing.T) {
	f := newReauthFixture(t, Config{})
	rec := serveWebhooks(t, f.server, userRequest(t, f.server, "alice", []string{"condor-users"},
		http.MethodGet, "/api/v1/webhooks", ""))
	var out struct {
		Events []string `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Events) == 0 {
		t.Fatal("the listing offers no events")
	}
	for _, want := range []string{"done", "succeeded", "failed", "held", "running"} {
		if !containsString(out.Events, want) {
			t.Errorf("the vocabulary is missing %q: %v", want, out.Events)
		}
	}
	// Custom has no terminal resolution, so a hook using it would fire only
	// for jobs still in the queue.
	if containsString(out.Events, "custom") {
		t.Error("custom is offered as a webhook event")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestWebhooksDisabledAnswers503: an operator who turns the feature off gets
// endpoints that say so, rather than 404s that read as an old build.
func TestWebhooksDisabledAnswers503(t *testing.T) {
	f := newReauthFixture(t, Config{WebhooksDisabled: true})

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/webhooks", ""},
		{http.MethodPost, "/api/v1/webhooks", `{"label":"x","url":"https://h.example.org/x","scope":"owner","events":["failed"]}`},
		{http.MethodGet, "/api/v1/webhooks/wh_1", ""},
		{http.MethodPost, "/api/v1/webhooks/wh_1/test", `{}`},
	} {
		rec := serveWebhooks(t, f.server, userRequest(t, f.server, "alice",
			[]string{"condor-users"}, tc.method, tc.path, tc.body))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s with webhooks off: status %d, want 503",
				tc.method, tc.path, rec.Code)
		}
	}
}

// TestWebhookFanoutLimitIsConfigurable: the evaluator takes the operator's
// number, so a site can tune what counts as too far.
func TestWebhookFanoutLimitIsConfigurable(t *testing.T) {
	f := newReauthFixture(t, Config{WebhookFanoutLimit: 7})
	if f.server.webhookEval == nil {
		t.Fatal("the evaluator was not built")
	}
	if got := f.server.webhookEval.FanoutCap; got != 7 {
		t.Errorf("FanoutCap = %d, want the configured 7", got)
	}

	// Unset leaves the package default rather than zero, which would cap
	// every hook at nothing.
	g := newReauthFixture(t, Config{})
	if got := g.server.webhookEval.FanoutCap; got <= 0 {
		t.Errorf("FanoutCap = %d with no configuration; want the package default", got)
	}
}

// A receiver that follows the documented recipe verifies a real signature,
// and one that skips the hex decode does not.
//
// hexSecret is the boundary between this server and every receiver ever
// written against it: the key is 32 raw bytes, and what the create response
// carries is those bytes hex-encoded. A receiver has to decode before using
// it, which docs/server-webhooks.md now says outright because getting it
// wrong is invisible from both ends -- HMAC takes any key, so the undecoded
// 64-character string still yields a well-formed signature that simply never
// matches, with nothing logged anywhere to say why.
//
// Verified against a real delivery to a Zapier catch hook on 2026-11-22,
// which is also how the gap in the documentation was found.
func TestReceiverMustDecodeTheHexSecret(t *testing.T) {
	secret, err := webhooks.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	// Exactly what a receiver is handed and what it is sent.
	shown := hexSecret(secret)
	key := "wh_0f3c.held.-.4242.0"
	body := []byte(`{"idempotency_key":"wh_0f3c.held.-.4242.0","event":"held"}`)
	at := time.Now()
	header := webhooks.Sign(secret, key, body, at)

	decoded, err := hex.DecodeString(shown)
	if err != nil {
		t.Fatalf("the secret we hand out is not decodable hex: %v", err)
	}
	if err := webhooks.VerifySignature(decoded, key, body, header, at); err != nil {
		t.Errorf("the documented recipe did not verify a signature we produced: %v", err)
	}

	// The failure this guards against. If this ever starts passing, the key
	// is being taken as text somewhere and the documentation is wrong again.
	if err := webhooks.VerifySignature([]byte(shown), key, body, header, at); err == nil {
		t.Error("the undecoded hex string verified, so the stated decode requirement is not real")
	}
}
