package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
)

// What one delivery attempt does, and what the dispatcher makes of it.
//
// The receiver is a TLS test server, which gets through ValidateURL (https)
// and through the sender's own client because the test replaces that client
// with one that trusts the test certificate. The real client refuses loopback
// outright, which is itself a test below: a functional test of delivery
// cannot go through the dial guard, since httptest binds loopback.

// receiver is a test destination that records what arrived.
type receiver struct {
	mu       sync.Mutex
	requests []recorded
	status   int
	body     string
}

type recorded struct {
	Body      []byte
	Key       string
	Signature string
	Event     string
	Hook      string
}

func (r *receiver) status200() { r.mu.Lock(); r.status = 200; r.mu.Unlock() }

func (r *receiver) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.requests = append(r.requests, recorded{
			Body:      body,
			Key:       req.Header.Get(IdempotencyHeader),
			Signature: req.Header.Get(SignatureHeader),
			Event:     req.Header.Get(EventHeader),
			Hook:      req.Header.Get(HookHeader),
		})
		status, msg := r.status, r.body
		r.mu.Unlock()
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
		if msg != "" {
			_, _ = w.Write([]byte(msg))
		}
	}
}

func (r *receiver) seen() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.requests...)
}

// fixture is a store, a hook pointed at a TLS receiver, and a dispatcher
// whose sender trusts it.
type fixture struct {
	store *Store
	hook  *Hook
	disp  *Dispatcher
	recv  *receiver
	srv   *httptest.Server
}

func newFixture(t *testing.T, scope Scope, events ...jobwatch.Event) *fixture {
	t.Helper()
	if len(events) == 0 {
		events = []jobwatch.Event{jobwatch.EventFailed}
	}
	recv := &receiver{}
	srv := httptest.NewTLSServer(recv.handler())
	t.Cleanup(srv.Close)

	store := newStore(t)
	h := &Hook{Owner: "alice", Label: "Receiver", Scope: scope, Events: events}
	if scope == ScopeJob {
		h.Job = jobwatch.JobID{Cluster: 42, Proc: 1}
	}
	if scope == ScopeBatch {
		h.BatchID, h.BatchName = "42", "nightly"
	}
	hook, _, err := store.Create(context.Background(), h, srv.URL)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sender := NewSender()
	// The real client refuses loopback, which is what TestSenderRefusesNonPublic
	// asserts. Everything else has to be able to reach a test server.
	sender.client = srv.Client()

	disp := NewDispatcher(store, sender, func(string, ...any) {})
	return &fixture{store: store, hook: hook, disp: disp, recv: recv, srv: srv}
}

// enqueue puts one delivery on the queue for cluster.0.
//
// The event is the fixture's own first one rather than an argument: every
// caller wants the event its hook was created for, and passing it again only
// gave two places to disagree.
func (f *fixture) enqueue(t *testing.T, cluster int64) string {
	t.Helper()
	e := f.hook.Events[0]
	job := jobwatch.JobID{Cluster: cluster, Proc: 0}
	key := DeliveryKey(f.hook.ID, e, job)
	payload, err := RenderPayload(f.hook, e,
		&JobPayload{Cluster: cluster, ID: "x", Owner: "alice"}, key, time.Now(), false)
	if err != nil {
		t.Fatalf("RenderPayload: %v", err)
	}
	if _, err := f.store.Enqueue(context.Background(), f.hook.ID, "alice", e, &job, payload, key); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return key
}

// reread returns the hook as stored.
func (f *fixture) reread(t *testing.T) *Hook {
	t.Helper()
	h, err := f.store.Get(context.Background(), "alice", f.hook.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return h
}

// deliveryRow returns one delivery's state.
func (f *fixture) deliveryRow(t *testing.T, key string) (state string, attempts int, next time.Time) {
	t.Helper()
	if err := f.store.db.QueryRowContext(context.Background(),
		`SELECT state, attempts, next_attempt_at FROM webhook_deliveries WHERE id = ?`, key).
		Scan(&state, &attempts, &next); err != nil {
		t.Fatalf("read delivery %s: %v", key, err)
	}
	return state, attempts, next
}

func TestDeliverySucceeds(t *testing.T) {
	f := newFixture(t, ScopeJob)
	f.recv.status200()
	key := f.enqueue(t, 42)

	st, err := f.disp.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if st.Delivered != 1 {
		t.Fatalf("delivered %d, want 1 (%+v)", st.Delivered, st)
	}
	state, attempts, _ := f.deliveryRow(t, key)
	if state != StateDelivered || attempts != 1 {
		t.Errorf("delivery is %s after %d attempts, want %s after 1", state, attempts, StateDelivered)
	}

	seen := f.recv.seen()
	if len(seen) != 1 {
		t.Fatalf("the receiver saw %d requests, want 1", len(seen))
	}
	got := seen[0]
	if got.Key != key {
		t.Errorf("Idempotency-Key = %q, want %q", got.Key, key)
	}
	if got.Event != "failed" {
		t.Errorf("X-HTCondor-Event = %q, want failed", got.Event)
	}
	if got.Hook != f.hook.ID {
		t.Errorf("X-HTCondor-Webhook = %q, want %q", got.Hook, f.hook.ID)
	}
	// The signature has to verify at the receiver with the secret the
	// creation handed out -- that is the whole point of sending one.
	_, secret, err := f.store.Destination(context.Background(), f.hook.ID)
	if err != nil {
		t.Fatalf("Destination: %v", err)
	}
	if err := VerifySignature(secret, got.Key, got.Body, got.Signature, time.Now()); err != nil {
		t.Errorf("the delivered signature did not verify: %v", err)
	}

	h := f.reread(t)
	if h.LastStatus != 200 || h.LastError != "" {
		t.Errorf("hook records %d %q after a success", h.LastStatus, h.LastError)
	}
	if h.LastDeliveryAt == nil {
		t.Error("a delivered hook has no last_delivery_at")
	}
}

func TestDeliveryRetriesOnServerError(t *testing.T) {
	f := newFixture(t, ScopeJob)
	f.recv.mu.Lock()
	f.recv.status, f.recv.body = http.StatusBadGateway, "upstream down"
	f.recv.mu.Unlock()
	key := f.enqueue(t, 42)

	before := time.Now()
	st, err := f.disp.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if st.Retrying != 1 || st.Abandoned != 0 {
		t.Fatalf("got %+v, want one retry and no abandonment", st)
	}
	state, attempts, next := f.deliveryRow(t, key)
	if state != StatePending || attempts != 1 {
		t.Errorf("delivery is %s after %d attempts, want %s after 1", state, attempts, StatePending)
	}
	// Backed off rather than retried immediately, and the backoff is a
	// stored timestamp so a restart does not lose it.
	if !next.After(before) {
		t.Errorf("next attempt is %s, not in the future", next)
	}
	h := f.reread(t)
	if h.LastStatus != http.StatusBadGateway {
		t.Errorf("hook last_status = %d, want 502", h.LastStatus)
	}
	if h.DisabledAt != nil {
		t.Error("a hook was disabled for one retryable failure")
	}
}

// TestDeliveryAbandonsPermanentRefusal is the anti-livelock property: a
// statement about the request does not change between attempts, so spending
// the attempt budget on it only delays telling the user.
func TestDeliveryAbandonsPermanentRefusal(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newFixture(t, ScopeJob)
			f.recv.mu.Lock()
			f.recv.status, f.recv.body = status, "no such hook"
			f.recv.mu.Unlock()
			key := f.enqueue(t, 42)

			st, err := f.disp.Pass(context.Background())
			if err != nil {
				t.Fatalf("Pass: %v", err)
			}
			if st.Abandoned != 1 || st.Retrying != 0 {
				t.Fatalf("got %+v, want one abandonment and no retry", st)
			}
			state, attempts, _ := f.deliveryRow(t, key)
			if state != StateAbandoned {
				t.Errorf("delivery is %s, want %s", state, StateAbandoned)
			}
			if attempts != 1 {
				t.Errorf("a permanent refusal was attempted %d times, want 1", attempts)
			}
			h := f.reread(t)
			if h.ConsecutiveFailures != 1 {
				t.Errorf("consecutive_failures = %d, want 1", h.ConsecutiveFailures)
			}
			if h.LastError == "" {
				t.Error("the hook records no reason for the user to read")
			}
		})
	}
}

// TestHookDisabledAfterRepeatedGiveUps: a hook is disabled rather than left
// failing quietly, because silence reads as "nothing has happened yet".
func TestHookDisabledAfterRepeatedGiveUps(t *testing.T) {
	f := newFixture(t, ScopeOwner)
	f.recv.mu.Lock()
	f.recv.status = http.StatusNotFound
	f.recv.mu.Unlock()

	for i := 0; i < MaxConsecutiveFailures; i++ {
		f.enqueue(t, int64(100+i))
	}
	st, err := f.disp.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if st.Abandoned != MaxConsecutiveFailures {
		t.Fatalf("abandoned %d, want %d", st.Abandoned, MaxConsecutiveFailures)
	}
	if st.Disabled != 1 {
		t.Errorf("disabled %d hooks, want 1", st.Disabled)
	}
	h := f.reread(t)
	if h.DisabledAt == nil {
		t.Fatal("the hook was not disabled after repeated give-ups")
	}
	if h.DisabledReason == "" {
		t.Error("the hook was disabled with no reason to show the user")
	}
	// A disabled hook is no longer evaluated.
	live, err := f.store.Live(context.Background())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	for _, l := range live {
		if l.ID == h.ID {
			t.Error("a disabled hook is still live")
		}
	}

	// And the user can put it back once they have fixed the destination.
	if err := f.store.Enable(context.Background(), "alice", h.ID); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	again := f.reread(t)
	if again.DisabledAt != nil || again.ConsecutiveFailures != 0 {
		t.Errorf("Enable left the hook disabled or still counting failures: %+v", again)
	}
}

// TestDeliveryGivesUpAfterMaxAttempts covers the budget, as distinct from a
// permanent refusal: the receiver is broken rather than refusing, and the
// retries do eventually stop.
func TestDeliveryGivesUpAfterMaxAttempts(t *testing.T) {
	f := newFixture(t, ScopeJob)
	f.recv.mu.Lock()
	f.recv.status = http.StatusServiceUnavailable
	f.recv.mu.Unlock()
	key := f.enqueue(t, 42)

	ctx := context.Background()
	for i := 0; i < MaxAttempts+2; i++ {
		// The backoff is a stored timestamp, so make the row due again
		// rather than waiting out a real delay.
		if _, err := f.store.db.ExecContext(ctx,
			`UPDATE webhook_deliveries SET next_attempt_at = ? WHERE id = ? AND state = ?`,
			time.Now().UTC().Add(-time.Second), key, StatePending); err != nil {
			t.Fatalf("make due: %v", err)
		}
		if _, err := f.disp.Pass(ctx); err != nil {
			t.Fatalf("Pass %d: %v", i, err)
		}
	}
	state, attempts, _ := f.deliveryRow(t, key)
	if state != StateAbandoned {
		t.Fatalf("delivery is %s after %d attempts, want it abandoned", state, attempts)
	}
	if attempts != MaxAttempts {
		t.Errorf("attempts = %d, want exactly MaxAttempts (%d)", attempts, MaxAttempts)
	}
}

// TestSendTestGoesThroughTheRealPath: the button has to exercise the signing,
// the dial policy and the receiver's own handler, or it tests the wrong code.
func TestSendTestGoesThroughTheRealPath(t *testing.T) {
	f := newFixture(t, ScopeJob, jobwatch.EventHeld)
	f.recv.status200()

	a, err := SendTest(context.Background(), f.store, f.disp.sender, f.hook)
	if err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if !a.OK() {
		t.Fatalf("test delivery was not accepted: %+v", a)
	}
	seen := f.recv.seen()
	if len(seen) != 1 {
		t.Fatalf("the receiver saw %d requests, want 1", len(seen))
	}
	var p Payload
	if err := json.Unmarshal(seen[0].Body, &p); err != nil {
		t.Fatalf("decode test payload: %v", err)
	}
	// The only way a receiver can tell a test from the real thing.
	if !p.Test {
		t.Error("the test payload is not marked as a test")
	}
	// It names an event the hook actually subscribes to, because a receiver
	// routes on one.
	if p.Event != string(jobwatch.EventHeld) {
		t.Errorf("test event = %q, want held", p.Event)
	}
	// A receiver's handler almost always reads the job, so a test payload
	// without one tests only that the handler can be reached.
	if p.Job == nil {
		t.Error("the test payload carries no job")
	}
	// Recorded apart from real traffic, so neither overwrites the other.
	h := f.reread(t)
	if h.LastTestAt == nil || h.LastTestStatus != 200 {
		t.Errorf("the test result was not recorded: %+v", h)
	}
	if h.LastDeliveryAt != nil {
		t.Error("a test overwrote the record of real deliveries")
	}
	// A test is not retried and leaves nothing on the queue.
	var pending int
	if err := f.store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM webhook_deliveries WHERE webhook_id = ?`, f.hook.ID).Scan(&pending); err != nil {
		t.Fatalf("count: %v", err)
	}
	if pending != 0 {
		t.Errorf("a test delivery left %d rows on the queue", pending)
	}
}

func TestSendTestReportsARefusal(t *testing.T) {
	f := newFixture(t, ScopeJob)
	f.recv.mu.Lock()
	f.recv.status, f.recv.body = http.StatusForbidden, "bad token"
	f.recv.mu.Unlock()

	a, err := SendTest(context.Background(), f.store, f.disp.sender, f.hook)
	if err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if a.OK() {
		t.Fatal("a 403 was reported as a success")
	}
	// The user needs the receiver's own words; "it didn't work" is not
	// actionable.
	if a.Cause == "" {
		t.Error("the refusal carries no cause")
	}
	h := f.reread(t)
	if h.LastTestStatus != http.StatusForbidden || h.LastTestError == "" {
		t.Errorf("the refusal was not recorded: %+v", h)
	}
}

// TestSenderRefusesNonPublic asserts the dial guard is actually wired into
// the sender the daemon uses -- the one place the functional tests above
// deliberately bypass.
func TestSenderRefusesNonPublic(t *testing.T) {
	recv := &receiver{}
	srv := httptest.NewTLSServer(recv.handler())
	defer srv.Close()

	// The real sender, with its real client.
	sender := NewSender()
	a := sender.Post(context.Background(), srv.URL, []byte("secret"), "key", []byte("{}"),
		jobwatch.EventDone, "wh_1")
	if a.OK() {
		t.Fatal("the sender reached a loopback destination")
	}
	if a.Retryable {
		t.Error("a blocked address was reported as retryable; it is the same decision every time")
	}
	if len(recv.seen()) != 0 {
		t.Error("the request reached the receiver although the address is not public")
	}
}

// TestPruneDeliveriesKeepsPending: a pending row is the worker's
// responsibility, not the sweep's.
func TestPruneDeliveriesKeepsPending(t *testing.T) {
	f := newFixture(t, ScopeOwner)
	f.recv.status200()
	ctx := context.Background()

	done := f.enqueue(t, 1)
	if _, err := f.disp.Pass(ctx); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	pending := f.enqueue(t, 2)
	if _, err := f.store.db.ExecContext(ctx,
		`UPDATE webhook_deliveries SET created_at = ?`, time.Now().UTC().Add(-90*24*time.Hour)); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := f.store.PruneDeliveries(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneDeliveries: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1", n)
	}
	if state, _, _ := f.deliveryRow(t, pending); state != StatePending {
		t.Errorf("the pending delivery is %s; it should have survived", state)
	}
	var left int
	if err := f.store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM webhook_deliveries WHERE id = ?`, done).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Error("the delivered row was not pruned")
	}
}
