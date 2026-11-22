package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
			Hook:      req.Header.Get(EndpointHeader),
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

// fixture is a store, an endpoint pointed at a TLS receiver with one
// subscription feeding it, and a dispatcher whose sender trusts it.
type fixture struct {
	store *Store
	ep    *Endpoint
	sub   *Subscription
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
	ep, _, err := store.CreateEndpoint(context.Background(),
		&Endpoint{Owner: "alice", Label: "Receiver"}, srv.URL)
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	s := &Subscription{EndpointID: ep.ID, Owner: "alice", Label: "watch", Scope: scope, Events: events}
	if scope == ScopeJob {
		s.Job = jobwatch.JobID{Cluster: 42, Proc: 1}
	}
	if scope == ScopeBatch {
		s.BatchID, s.BatchName = "42", "nightly"
	}
	sub, err := store.CreateSubscription(context.Background(), s)
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	sender := NewSender()
	// The real client refuses loopback, which is what TestSenderRefusesNonPublic
	// asserts. Everything else has to be able to reach a test server.
	sender.client = srv.Client()

	disp := NewDispatcher(store, sender, func(string, ...any) {})
	return &fixture{store: store, ep: ep, sub: sub, disp: disp, recv: recv, srv: srv}
}

// enqueue puts one delivery on the queue for cluster.0.
//
// The event is the fixture's own first one rather than an argument: every
// caller wants the event its subscription was created for, and passing it
// again only gave two places to disagree.
func (f *fixture) enqueue(t *testing.T, cluster int64) string {
	t.Helper()
	e := f.sub.Events[0]
	job := jobwatch.JobID{Cluster: cluster, Proc: 0}
	key := DeliveryKey(f.ep.ID, e, job)
	payload, err := RenderPayload(f.ep, f.sub, e,
		&JobPayload{Cluster: cluster, ID: "x", Owner: "alice"}, nil, key, time.Now(), false)
	if err != nil {
		t.Fatalf("RenderPayload: %v", err)
	}
	if _, err := f.store.Enqueue(context.Background(), f.ep.ID, "alice", e, &job, payload, key); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return key
}

// reread returns the endpoint as stored.
func (f *fixture) reread(t *testing.T) *Endpoint {
	t.Helper()
	ep, err := f.store.GetEndpoint(context.Background(), "alice", f.ep.ID)
	if err != nil {
		t.Fatalf("GetEndpoint: %v", err)
	}
	return ep
}

// clearBackoff lets the next pass claim, without pretending a delivery
// succeeded. Tests that want several attempts in a row need this because a
// failing endpoint is deliberately not claimable again for minutes.
func (f *fixture) clearBackoff(t *testing.T) {
	t.Helper()
	if _, err := f.store.db.ExecContext(context.Background(),
		`UPDATE webhook_endpoints SET backoff_until = NULL WHERE id = ?`, f.ep.ID); err != nil {
		t.Fatalf("clear backoff: %v", err)
	}
	if _, err := f.store.db.ExecContext(context.Background(),
		`UPDATE webhook_deliveries SET next_attempt_at = ? WHERE endpoint_id = ?`,
		time.Now().UTC().Add(-time.Minute), f.ep.ID); err != nil {
		t.Fatalf("make due: %v", err)
	}
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
	if got.Hook != f.ep.ID {
		t.Errorf("X-HTCondor-Webhook = %q, want %q", got.Hook, f.ep.ID)
	}
	// The signature has to verify at the receiver with the secret the
	// creation handed out -- that is the whole point of sending one.
	_, secrets, err := f.store.Destination(context.Background(), f.ep.ID)
	if err != nil {
		t.Fatalf("Destination: %v", err)
	}
	if len(secrets) != 1 {
		t.Fatalf("an endpoint that has never rotated has %d live keys, want 1", len(secrets))
	}
	if err := VerifySignature(secrets[0], got.Key, got.Body, got.Signature, time.Now()); err != nil {
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
		t.Errorf("endpoint last_status = %d, want 502", h.LastStatus)
	}
	if h.ConsecutiveFailures != 1 {
		t.Errorf("consecutive_failures = %d, want 1", h.ConsecutiveFailures)
	}
	// The backoff is on the ENDPOINT, and the delivery's next attempt is
	// that backoff rather than a schedule of its own.
	if h.BackoffUntil == nil {
		t.Fatal("a failing endpoint recorded no backoff")
	}
	if !h.Failing(time.Now()) {
		t.Error("the endpoint does not report itself as backing off")
	}
	if next.Sub(*h.BackoffUntil).Abs() > time.Second {
		t.Errorf("the delivery's next attempt (%s) is not the endpoint's backoff (%s)",
			next, *h.BackoffUntil)
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

// TestEndpointIsNeverDisabled is the policy, stated as a test: a
// destination that keeps failing backs off and keeps its place in the queue.
// It is not switched off, because a switched-off hook comes back only if
// somebody happens to revisit a page they have no reason to look at.
func TestEndpointIsNeverDisabled(t *testing.T) {
	f := newFixture(t, ScopeOwner)
	f.recv.mu.Lock()
	f.recv.status = http.StatusServiceUnavailable
	f.recv.mu.Unlock()
	key := f.enqueue(t, 100)

	ctx := context.Background()
	var last time.Duration
	for i := 0; i < 12; i++ {
		f.clearBackoff(t)
		if _, err := f.disp.Pass(ctx); err != nil {
			t.Fatalf("Pass %d: %v", i, err)
		}
		ep := f.reread(t)
		if ep.BackoffUntil == nil {
			t.Fatalf("pass %d: no backoff recorded", i)
		}
		last = time.Until(*ep.BackoffUntil)
	}

	// Still pending after a dozen failures. Nothing gave up on it.
	state, attempts, _ := f.deliveryRow(t, key)
	if state != StatePending {
		t.Errorf("delivery is %s after %d attempts, want it still %s", state, attempts, StatePending)
	}
	if attempts < 12 {
		t.Errorf("attempts = %d, want at least 12; the retries stopped early", attempts)
	}

	// And the wait has grown to the ceiling and stayed there, rather than
	// growing without bound or staying at the floor. Jitter is +/-20%, so
	// the band is generous on purpose.
	if last < time.Duration(float64(MaxBackoff)*0.7) || last > time.Duration(float64(MaxBackoff)*1.3) {
		t.Errorf("settled backoff is %s, want about %s", last.Round(time.Second), MaxBackoff)
	}

	// The subscription is still live: an outage must not lose the events
	// that happen during it.
	live, err := f.store.LiveSubscriptions(ctx)
	if err != nil {
		t.Fatalf("LiveSubscriptions: %v", err)
	}
	if len(live) != 1 {
		t.Errorf("%d live subscriptions after a long outage, want 1", len(live))
	}

	// One success clears the whole thing.
	f.recv.status200()
	f.clearBackoff(t)
	if _, err := f.disp.Pass(ctx); err != nil {
		t.Fatalf("Pass after recovery: %v", err)
	}
	ep := f.reread(t)
	if ep.ConsecutiveFailures != 0 || ep.BackoffUntil != nil {
		t.Errorf("a success did not clear the backoff: %+v", ep)
	}
	if state, _, _ := f.deliveryRow(t, key); state != StateDelivered {
		t.Errorf("delivery is %s after the receiver came back, want %s", state, StateDelivered)
	}
}

// TestFailingEndpointProbesWithOneDelivery is what makes "never give up"
// affordable. A receiver that has been down for a day must not be hit once
// per queued event when its backoff expires, nor handed its whole backlog
// the instant it answers.
func TestFailingEndpointProbesWithOneDelivery(t *testing.T) {
	f := newFixture(t, ScopeOwner)
	f.recv.mu.Lock()
	f.recv.status = http.StatusServiceUnavailable
	f.recv.mu.Unlock()
	for i := int64(0); i < 5; i++ {
		f.enqueue(t, 200+i)
	}

	ctx := context.Background()
	// First pass: the endpoint has no failures yet, so everything due is
	// fair game and they are posted serially to the one destination.
	st, err := f.disp.Pass(ctx)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if st.Claimed != 5 {
		t.Fatalf("first pass claimed %d, want all 5", st.Claimed)
	}

	// Now the endpoint is failing. Every later pass may try exactly one,
	// however many are queued.
	for i := 0; i < 3; i++ {
		f.clearBackoff(t)
		st, err := f.disp.Pass(ctx)
		if err != nil {
			t.Fatalf("Pass %d: %v", i, err)
		}
		if st.Claimed != 1 {
			t.Errorf("pass %d claimed %d deliveries from a failing endpoint, want 1", i, st.Claimed)
		}
	}
}

// TestStaleDeliveryIsAbandoned is the one remaining bound. Nothing is
// disabled and no attempt budget runs out, so without this a destination
// that never comes back queues rows for ever.
func TestStaleDeliveryIsAbandoned(t *testing.T) {
	f := newFixture(t, ScopeJob)
	key := f.enqueue(t, 42)
	ctx := context.Background()

	// Not yet: a delivery inside the window is left alone.
	n, err := f.store.ExpireStale(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if n != 0 {
		t.Fatalf("expired %d fresh deliveries, want 0", n)
	}

	n, err = f.store.ExpireStale(ctx, time.Now().UTC().Add(MaxDeliveryAge+time.Hour))
	if err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if n != 1 {
		t.Fatalf("expired %d, want 1", n)
	}
	state, _, _ := f.deliveryRow(t, key)
	if state != StateAbandoned {
		t.Errorf("a delivery older than %s is %s, want %s", MaxDeliveryAge, state, StateAbandoned)
	}
	var reason string
	if err := f.store.db.QueryRowContext(ctx,
		`SELECT abandoned_reason FROM webhook_deliveries WHERE id = ?`, key).Scan(&reason); err != nil {
		t.Fatalf("read reason: %v", err)
	}
	if reason == "" {
		t.Error("a delivery was abandoned with no reason to show the user")
	}
}

func TestSendTestGoesThroughTheRealPath(t *testing.T) {
	f := newFixture(t, ScopeJob, jobwatch.EventHeld)
	f.recv.status200()

	a, err := SendTest(context.Background(), f.store, f.disp.sender, f.ep)
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
	// It names one fixed event, because a test belongs to the
	// DESTINATION and a destination may have several subscriptions or
	// none. A receiver routes on the event, so it has to be one.
	if p.Event != string(TestEvent) {
		t.Errorf("test event = %q, want %q", p.Event, TestEvent)
	}
	// A receiver's handler almost always reads the job, so a test payload
	// without one tests only that the handler can be reached.
	if p.Job == nil {
		t.Fatal("the test payload carries no job")
	}
	// And the job it describes must be CONSISTENT with the event it
	// names. The old test payload said "failed" beside an exit code of 0
	// and a status of Completed, which is a shape a receiver author maps
	// fields from and then cannot reproduce in production.
	if p.Job.Status != "Running" {
		t.Errorf("a %q test describes a job in state %q", p.Event, p.Job.Status)
	}
	if p.Job.ExitCode != nil {
		t.Errorf("a running job was given exit code %d", *p.Job.ExitCode)
	}
	if p.Job.HoldReason != "" {
		t.Errorf("a running job was given a hold reason: %q", p.Job.HoldReason)
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
		`SELECT COUNT(*) FROM webhook_deliveries WHERE endpoint_id = ?`, f.ep.ID).Scan(&pending); err != nil {
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

	a, err := SendTest(context.Background(), f.store, f.disp.sender, f.ep)
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
	a := sender.Post(context.Background(), srv.URL, [][]byte{[]byte("secret")}, "key", []byte("{}"),
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

// TestRotatedEndpointDeliversBothSignatures is the rotation contract where
// it actually matters: on the wire.
//
// A receiver updated to the new key and one still on the old must BOTH be
// able to verify the same delivery, or rotation is just a scheduled outage.
func TestRotatedEndpointDeliversBothSignatures(t *testing.T) {
	f := newFixture(t, ScopeJob)
	ctx := context.Background()

	_, before, err := f.store.Destination(ctx, f.ep.ID)
	if err != nil {
		t.Fatalf("Destination: %v", err)
	}
	oldKey := before[0]

	_, newKey, err := f.store.RotateSecret(ctx, "alice", f.ep.ID)
	if err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}

	f.enqueue(t, 42)
	if _, err := f.disp.Pass(ctx); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	seen := f.recv.seen()
	if len(seen) != 1 {
		t.Fatalf("the receiver saw %d requests, want 1", len(seen))
	}
	got := seen[0]

	if n := strings.Count(got.Signature, "v1="); n != 2 {
		t.Fatalf("a delivery during the grace period carries %d signatures, want 2: %s",
			n, got.Signature)
	}
	if err := VerifySignature(newKey, got.Key, got.Body, got.Signature, time.Now()); err != nil {
		t.Errorf("a receiver on the NEW key could not verify: %v", err)
	}
	if err := VerifySignature(oldKey, got.Key, got.Body, got.Signature, time.Now()); err != nil {
		t.Errorf("a receiver still on the OLD key could not verify: %v", err)
	}
}

// TestDeliveryAfterGracePeriodDropsTheOldSignature: once the window closes,
// the retired key is gone from the wire as well as from the store.
func TestDeliveryAfterGracePeriodDropsTheOldSignature(t *testing.T) {
	f := newFixture(t, ScopeJob)
	ctx := context.Background()

	_, before, err := f.store.Destination(ctx, f.ep.ID)
	if err != nil {
		t.Fatalf("Destination: %v", err)
	}
	oldKey := before[0]
	if _, _, err := f.store.RotateSecret(ctx, "alice", f.ep.ID); err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}

	f.store.now = func() time.Time {
		return time.Now().UTC().Add(SecretGracePeriod + time.Minute)
	}
	f.enqueue(t, 43)
	if _, err := f.disp.Pass(ctx); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	seen := f.recv.seen()
	if len(seen) != 1 {
		t.Fatalf("the receiver saw %d requests, want 1", len(seen))
	}
	got := seen[0]

	if n := strings.Count(got.Signature, "v1="); n != 1 {
		t.Errorf("a delivery after the grace period carries %d signatures, want 1: %s",
			n, got.Signature)
	}
	if err := VerifySignature(oldKey, got.Key, got.Body, got.Signature, time.Now()); err == nil {
		t.Error("the retired key still verifies a delivery sent after its deadline")
	}
}
