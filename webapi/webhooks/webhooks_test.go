package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/httpserver/appdb"
	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
)

// newStore opens a migrated application database. Opening it here also
// exercises migration 0042 on every run.
func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := appdb.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("appdb.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := appdb.Migrate(context.Background(), db); err != nil {
		t.Fatalf("appdb.Migrate: %v", err)
	}
	// No sealer: a deployment with no KEK stores in the clear, which the
	// row records, and the store's own round trip has to work either way.
	// The sealed path has its own test below.
	return NewStore(db, nil)
}

// ownerSub is a subscription to everything the owner runs.
func ownerSub(owner, label, endpointID string) *Subscription {
	return &Subscription{
		EndpointID: endpointID, Owner: owner, Label: label, Scope: ScopeOwner,
		Events: []jobwatch.Event{jobwatch.EventFailed},
	}
}

// newEndpoint registers a destination and returns it with its secret.
func newEndpoint(t *testing.T, s *Store, owner, label, dest string) (*Endpoint, []byte) {
	t.Helper()
	ep, secret, err := s.CreateEndpoint(context.Background(),
		&Endpoint{Owner: owner, Label: label}, dest)
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	return ep, secret
}

// --- the model -----------------------------------------------------------

func TestEndpointValidateRejectsWhatCannotWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		ep   *Endpoint
		want string
	}{
		{"no label", &Endpoint{Owner: "alice", Payload: PayloadSummary}, "name is required"},
		{"no owner", &Endpoint{Label: "x", Payload: PayloadSummary}, "must have an owner"},
		{"unknown payload mode", &Endpoint{Owner: "alice", Label: "x", Payload: "everything"},
			"unknown payload mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ep.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %+v", tc.ep)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateRejectsWhatCannotWork(t *testing.T) {
	const ep = "wh_0000"
	for _, tc := range []struct {
		name string
		hook *Subscription
		want string
	}{
		{"no destination", &Subscription{Owner: "alice", Scope: ScopeOwner,
			Events: []jobwatch.Event{jobwatch.EventDone}}, "must name the destination"},
		{"unknown scope", &Subscription{EndpointID: ep, Owner: "alice", Label: "x", Scope: "galaxy",
			Events: []jobwatch.Event{jobwatch.EventDone}}, "unknown scope"},
		{"no events", &Subscription{EndpointID: ep, Owner: "alice", Label: "x", Scope: ScopeOwner},
			"at least one event"},
		{"duplicate event", &Subscription{EndpointID: ep, Owner: "alice", Label: "x", Scope: ScopeOwner,
			Events: []jobwatch.Event{jobwatch.EventDone, jobwatch.EventDone}}, "duplicate event"},
		// Custom is a jobwatch event and deliberately not a webhook
		// event: it has no terminal resolution, so it would fire only
		// for jobs still in the queue and a subscription matching
		// nothing would look like a quiet pool.
		{"custom event", &Subscription{EndpointID: ep, Owner: "alice", Label: "x", Scope: ScopeOwner,
			Events: []jobwatch.Event{jobwatch.EventCustom}}, "unsupported event"},
		{"job hook with no job", &Subscription{EndpointID: ep, Owner: "alice", Label: "x", Scope: ScopeJob,
			Events: []jobwatch.Event{jobwatch.EventDone}}, "needs a cluster id"},
		{"batch hook with no batch", &Subscription{EndpointID: ep, Owner: "alice", Label: "x", Scope: ScopeBatch,
			Events: []jobwatch.Event{jobwatch.EventDone}}, "needs a batch"},
		{"owner hook naming a job", &Subscription{EndpointID: ep, Owner: "alice", Label: "x", Scope: ScopeOwner,
			Job: jobwatch.JobID{Cluster: 5}, Events: []jobwatch.Event{jobwatch.EventDone}},
			"names no job or batch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.hook.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %+v", tc.hook)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateURL(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		ok        bool
	}{
		{"https", "https://hooks.example.org/abc", true},
		{"with query", "https://hooks.example.org/abc?token=xyz", true},
		// A webhook carries a job's details; there is no deployment where
		// sending that in clear is what somebody meant.
		{"http", "http://hooks.example.org/abc", false},
		{"http on loopback", "http://127.0.0.1:9000/abc", false},
		{"relative", "/abc", false},
		{"no host", "https:///abc", false},
		// Logged by everything it passes through, and replayed on every
		// retry.
		{"credentials in url", "https://user:pass@hooks.example.org/abc", false},
		{"empty", "", false},
		{"whitespace", " https://hooks.example.org/abc", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateURL(tc.url)
			if tc.ok && err != nil {
				t.Fatalf("ValidateURL(%q) = %v, want nil", tc.url, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("ValidateURL(%q) = nil, want an error", tc.url)
			}
		})
	}
}

// TestHintForHidesThePath is the point of the hint: for a Slack or Zapier
// destination the path IS the secret, so a display that showed any of it
// would narrow a guess.
func TestHintForHidesThePath(t *testing.T) {
	const raw = "https://hooks.slack.com/services/T0000/B0000/XXXXXXXXXXXXXXXXXXXXXXXX"
	got := HintFor(raw)
	if !strings.Contains(got, "hooks.slack.com") {
		t.Errorf("hint %q does not name the host, which is what it is for", got)
	}
	for _, secret := range []string{"T0000", "B0000", "XXXXXXXXXXXXXXXXXXXXXXXX", "services"} {
		if strings.Contains(got, secret) {
			t.Errorf("hint %q leaks part of the path (%q)", got, secret)
		}
	}
}

// --- signing -------------------------------------------------------------

func TestSignRoundTrip(t *testing.T) {
	secret := []byte("a-test-signing-key")
	body := []byte(`{"event":"failed"}`)
	now := time.Now()
	header := Sign(secret, "key-1", body, now)

	if err := VerifySignature(secret, "key-1", body, header, now); err != nil {
		t.Fatalf("a signature this package produced did not verify: %v", err)
	}

	for _, tc := range []struct {
		name            string
		secret          []byte
		key             string
		body            []byte
		at              time.Time
		wantErrContains string
	}{
		{"tampered body", secret, "key-1", []byte(`{"event":"succeeded"}`), now, "does not match"},
		// The key is signed as well as the body, so a delivery's signature
		// cannot be lifted onto another delivery.
		{"different key", secret, "key-2", body, now, "does not match"},
		{"wrong secret", []byte("not-the-key"), "key-1", body, now, "does not match"},
		// A signature over the body alone would be replayable forever, and
		// replaying "your job failed" is not harmless when it drives
		// somebody's automation.
		{"stale timestamp", secret, "key-1", body, now.Add(2 * SignatureTolerance), "away from now"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifySignature(tc.secret, tc.key, tc.body, header, tc.at)
			if err == nil {
				t.Fatalf("VerifySignature accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErrContains)
			}
		})
	}
}

func TestVerifyRejectsMalformedHeader(t *testing.T) {
	for _, header := range []string{"", "v1=abc", "t=123", "nonsense", "t=notanumber,v1=abc"} {
		if err := VerifySignature([]byte("k"), "key", nil, header, time.Now()); err == nil {
			t.Errorf("VerifySignature accepted header %q", header)
		}
	}
}

// --- retry policy --------------------------------------------------------

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	// Jitter makes each draw inexact, so compare against the bound rather
	// than an exact value.
	upper := func(d time.Duration) time.Duration {
		return d + time.Duration(float64(d)*JitterFraction) + time.Second
	}
	first := Backoff(1)
	if first <= 0 || first > upper(BaseBackoff) {
		t.Errorf("Backoff(1) = %s, want about %s", first, BaseBackoff)
	}
	if third := Backoff(3); third <= first {
		t.Errorf("Backoff(3) = %s is not longer than Backoff(1) = %s", third, first)
	}
	for _, n := range []int{10, 50, 1000} {
		if got := Backoff(n); got > upper(MaxBackoff) {
			t.Errorf("Backoff(%d) = %s, want at most about %s", n, got, MaxBackoff)
		}
	}
}

// TestRetryableStatus is the classification that stops a delivery loop
// livelocking against somebody else's 404 handler.
func TestRetryableStatus(t *testing.T) {
	for _, tc := range []struct {
		code int
		want bool
	}{
		{408, true}, {429, true},
		{500, true}, {502, true}, {503, true}, {599, true},
		// A statement about the request, which does not change between
		// attempts.
		{400, false}, {401, false}, {403, false}, {404, false}, {410, false}, {422, false},
		// Reaches the classifier only because redirects are not followed,
		// which makes it a misconfigured destination.
		{301, false}, {302, false}, {307, false},
	} {
		if got := retryableStatus(tc.code); got != tc.want {
			t.Errorf("retryableStatus(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

// --- the store -----------------------------------------------------------

func TestCreateAndRead(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const dest = "https://hooks.example.org/abc/secret-path"

	ep, secret := newEndpoint(t, s, "alice", "Slack", dest)
	if len(secret) != SecretBytes {
		t.Errorf("secret is %d bytes, want %d", len(secret), SecretBytes)
	}
	if ep.ID == "" {
		t.Fatal("CreateEndpoint returned no id")
	}
	// A destination has no deadline. It is a place, not a thing being
	// watched, and nothing about it goes stale.
	if ep.Payload != PayloadSummary {
		t.Errorf("payload mode defaulted to %q, want %q", ep.Payload, PayloadSummary)
	}

	sub, err := s.CreateSubscription(ctx, ownerSub("alice", "everything", ep.ID))
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	// An owner subscription has no natural end, so it gets a deadline at
	// creation.
	if sub.ExpiresAt == nil {
		t.Error("an owner subscription was created with no deadline")
	}

	got, err := s.GetEndpoint(ctx, "alice", ep.ID)
	if err != nil {
		t.Fatalf("GetEndpoint: %v", err)
	}
	if got.Label != "Slack" {
		t.Errorf("read back %+v", got)
	}
	if !got.HasDestination {
		t.Error("HasDestination is false for an endpoint with one")
	}
	if strings.Contains(got.DestinationHint, "secret-path") {
		t.Errorf("the hint leaks the path: %q", got.DestinationHint)
	}

	gotSub, err := s.GetSubscription(ctx, "alice", sub.ID)
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if gotSub.Scope != ScopeOwner || gotSub.EndpointID != ep.ID {
		t.Errorf("read back %+v", gotSub)
	}

	// Somebody else's is not found, rather than refused: a 403 would
	// confirm the id exists.
	if _, err := s.GetEndpoint(ctx, "bob", ep.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetEndpoint as another owner = %v, want ErrNotFound", err)
	}
	if _, err := s.GetSubscription(ctx, "bob", sub.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSubscription as another owner = %v, want ErrNotFound", err)
	}
	if eps, err := s.EndpointsForOwner(ctx, "bob"); err != nil || len(eps) != 0 {
		t.Errorf("EndpointsForOwner(bob) = %v, %v; want none", eps, err)
	}

	// A subscription cannot point at somebody else's destination, and the
	// refusal does not confirm that the id exists.
	if _, err := s.CreateSubscription(ctx, ownerSub("bob", "borrowed", ep.ID)); err == nil {
		t.Error("bob subscribed through alice's destination")
	}

	// Destination is the only way to the value, and it does return it.
	back, backSecret, err := s.Destination(ctx, ep.ID)
	if err != nil {
		t.Fatalf("Destination: %v", err)
	}
	if back != dest {
		t.Errorf("destination round trip = %q, want %q", back, dest)
	}
	if string(backSecret) != string(secret) {
		t.Error("the signing secret did not survive the round trip")
	}
}

// TestOneSecretManySubscriptions is the whole point of the split: watching a
// second thing through a receiver must not mint a second key, because the
// key is shown once and re-copying it is where people go wrong.
func TestOneSecretManySubscriptions(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ep, secret := newEndpoint(t, s, "alice", "Zap", "https://hooks.example.org/x")

	for i, sub := range []*Subscription{
		{EndpointID: ep.ID, Owner: "alice", Scope: ScopeJob,
			Job: jobwatch.JobID{Cluster: 1, Proc: 0}, Events: []jobwatch.Event{jobwatch.EventDone}},
		{EndpointID: ep.ID, Owner: "alice", Scope: ScopeJob,
			Job: jobwatch.JobID{Cluster: 2, Proc: 0}, Events: []jobwatch.Event{jobwatch.EventHeld}},
		ownerSub("alice", "all of them", ep.ID),
	} {
		if _, err := s.CreateSubscription(ctx, sub); err != nil {
			t.Fatalf("CreateSubscription %d: %v", i, err)
		}
	}

	// One destination, one key, still the one that was handed over.
	eps, err := s.EndpointsForOwner(ctx, "alice")
	if err != nil {
		t.Fatalf("EndpointsForOwner: %v", err)
	}
	if len(eps) != 1 {
		t.Fatalf("three subscriptions produced %d destinations, want 1", len(eps))
	}
	_, back, err := s.Destination(ctx, ep.ID)
	if err != nil {
		t.Fatalf("Destination: %v", err)
	}
	if string(back) != string(secret) {
		t.Error("the destination's signing key changed when subscriptions were added")
	}

	subs, err := s.SubscriptionsForOwner(ctx, "alice")
	if err != nil || len(subs) != 3 {
		t.Fatalf("SubscriptionsForOwner = %d, %v; want 3", len(subs), err)
	}
}

func TestCreateSealsWhenAKeyIsConfigured(t *testing.T) {
	s := newStore(t)
	s.sealer = fakeSealer{}
	ctx := context.Background()
	const dest = "https://hooks.example.org/sealed"

	h, _ := newEndpoint(t, s, "alice", "Sealed", dest)
	// Stored sealed: the raw column must not contain the URL.
	var raw []byte
	if err := s.db.QueryRowContext(ctx,
		`SELECT url_data FROM webhook_endpoints WHERE id = ?`, h.ID).Scan(&raw); err != nil {
		t.Fatalf("read url_data: %v", err)
	}
	if strings.Contains(string(raw), "hooks.example.org") {
		t.Error("the destination was stored in the clear although a sealer was configured")
	}
	back, _, err := s.Destination(ctx, h.ID)
	if err != nil || back != dest {
		t.Fatalf("Destination = %q, %v; want the original URL", back, err)
	}

	// A server that has lost its key refuses rather than treating
	// ciphertext as the value, which would POST it to whatever host
	// happened to be in it.
	s.sealer = nil
	if _, _, err := s.Destination(ctx, h.ID); !errors.Is(err, ErrNoKEK) {
		t.Errorf("Destination with no sealer = %v, want ErrNoKEK", err)
	}
}

func TestCreateEnforcesQuota(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < MaxEndpointsPerOwner; i++ {
		if _, _, err := s.CreateEndpoint(ctx,
			&Endpoint{Owner: "alice", Label: "h"}, "https://hooks.example.org/x"); err != nil {
			t.Fatalf("CreateEndpoint %d: %v", i, err)
		}
	}
	if _, _, err := s.CreateEndpoint(ctx,
		&Endpoint{Owner: "alice", Label: "one too many"}, "https://hooks.example.org/x"); err == nil {
		t.Fatal("CreateEndpoint past the quota succeeded")
	}
	// Somebody else's quota is their own.
	if _, _, err := s.CreateEndpoint(ctx,
		&Endpoint{Owner: "bob", Label: "mine"}, "https://hooks.example.org/x"); err != nil {
		t.Errorf("bob was refused for alice's destinations: %v", err)
	}

	// Subscriptions have their own, larger bound, and hitting the
	// destination ceiling does not consume it.
	ep, _ := newEndpoint(t, s, "carol", "Zap", "https://hooks.example.org/x")
	for i := 0; i < MaxSubscriptionsPerOwner; i++ {
		if _, err := s.CreateSubscription(ctx, ownerSub("carol", "s", ep.ID)); err != nil {
			t.Fatalf("CreateSubscription %d: %v", i, err)
		}
	}
	if _, err := s.CreateSubscription(ctx, ownerSub("carol", "one too many", ep.ID)); err == nil {
		t.Error("CreateSubscription past the quota succeeded")
	}
}

func TestDeleteIsOwnerScopedAndTakesDeliveries(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ep, _ := newEndpoint(t, s, "alice", "Slack", "https://hooks.example.org/x")
	sub, err := s.CreateSubscription(ctx, ownerSub("alice", "all", ep.ID))
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	job := jobwatch.JobID{Cluster: 7, Proc: 0}
	key := DeliveryKey(ep.ID, jobwatch.EventFailed, job)
	if _, err := s.Enqueue(ctx, ep.ID, "alice", jobwatch.EventFailed, &job, []byte("{}"), key); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := s.DeleteEndpoint(ctx, "bob", ep.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteEndpoint as another owner = %v, want ErrNotFound", err)
	}
	if err := s.DeleteEndpoint(ctx, "alice", ep.ID); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM webhook_deliveries WHERE endpoint_id = ?`, ep.ID).Scan(&n); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if n != 0 {
		t.Errorf("%d deliveries outlived their destination", n)
	}
	// The subscriptions go with it. Explicitly, not by the declared
	// cascade: this database does not enable PRAGMA foreign_keys, so
	// the cascade never fires and a subscription left behind would keep
	// being evaluated against a destination that no longer exists.
	if _, err := s.GetSubscription(ctx, "alice", sub.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a subscription outlived its destination: %v", err)
	}
}

// TestDeleteSubscriptionKeepsTheDestination is the other half of the split:
// stopping watching one job must not throw away the receiver's key.
func TestDeleteSubscriptionKeepsTheDestination(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ep, secret := newEndpoint(t, s, "alice", "Zap", "https://hooks.example.org/x")
	sub, err := s.CreateSubscription(ctx, ownerSub("alice", "all", ep.ID))
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if err := s.DeleteSubscription(ctx, "alice", sub.ID); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}
	if _, err := s.GetEndpoint(ctx, "alice", ep.ID); err != nil {
		t.Fatalf("the destination went with its last subscription: %v", err)
	}
	_, back, err := s.Destination(ctx, ep.ID)
	if err != nil || string(back) != string(secret) {
		t.Error("the signing key did not survive deleting a subscription")
	}
}

// TestEnqueueDeduplicates is the idempotency guarantee. The key is the
// primary key, so a pass that sees the same job in the same state again
// cannot enqueue a second POST.
func TestEnqueueDeduplicates(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	h, _ := newEndpoint(t, s, "alice", "Slack", "https://hooks.example.org/x")
	job := jobwatch.JobID{Cluster: 12, Proc: 3}
	key := DeliveryKey(h.ID, jobwatch.EventFailed, job)

	first, err := s.Enqueue(ctx, h.ID, "alice", jobwatch.EventFailed, &job, []byte("{}"), key)
	if err != nil || !first {
		t.Fatalf("first Enqueue = %v, %v; want new", first, err)
	}
	second, err := s.Enqueue(ctx, h.ID, "alice", jobwatch.EventFailed, &job, []byte("{}"), key)
	if err != nil {
		t.Fatalf("second Enqueue: %v", err)
	}
	if second {
		t.Error("the same event enqueued twice")
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM webhook_deliveries WHERE endpoint_id = ?`, h.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("%d delivery rows, want 1", n)
	}

	// A different event for the same job IS a different delivery.
	otherKey := DeliveryKey(h.ID, jobwatch.EventDone, job)
	if ok, err := s.Enqueue(ctx, h.ID, "alice", jobwatch.EventDone, &job, []byte("{}"), otherKey); err != nil || !ok {
		t.Errorf("a different event was deduplicated against the first: %v, %v", ok, err)
	}
}

func TestClaimDueSkipsFuture(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	h, _ := newEndpoint(t, s, "alice", "Slack", "https://hooks.example.org/x")
	job := jobwatch.JobID{Cluster: 1, Proc: 0}
	key := DeliveryKey(h.ID, jobwatch.EventDone, job)
	if _, err := s.Enqueue(ctx, h.ID, "alice", jobwatch.EventDone, &job, []byte("{}"), key); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	due, err := s.ClaimDue(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("claimed %d, want 1", len(due))
	}
	// Claiming pushes the next attempt forward, so a second pass -- or a
	// second worker -- does not pick up the same row.
	again, err := s.ClaimDue(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimDue again: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("claimed the same delivery twice (%d rows)", len(again))
	}
}

func TestRenderPayloadCarriesWhatAReceiverNeeds(t *testing.T) {
	ep := &Endpoint{ID: "wh_1", Label: "Slack", Owner: "alice", Payload: PayloadSummary}
	sub := &Subscription{ID: "ws_1", EndpointID: ep.ID, Scope: ScopeJob,
		Job: jobwatch.JobID{Cluster: 42, Proc: 1}, Owner: "alice",
		Events: []jobwatch.Event{jobwatch.EventFailed}}
	key := DeliveryKey(ep.ID, jobwatch.EventFailed, sub.Job)
	body, err := RenderPayload(ep, sub, jobwatch.EventFailed,
		&JobPayload{Cluster: 42, Proc: 1, ID: "42.1", Status: "Held"}, nil, key, time.Now(), false)
	if err != nil {
		t.Fatalf("RenderPayload: %v", err)
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("the payload is not valid JSON: %v", err)
	}
	if p.IdempotencyKey != key {
		t.Errorf("idempotency_key = %q, want %q", p.IdempotencyKey, key)
	}
	// A receiver author reads the payload and may never read the
	// documentation, so the contract is in the body.
	if p.DeliverySemantics != "at-least-once" {
		t.Errorf("delivery_semantics = %q", p.DeliverySemantics)
	}
	if p.Event != "failed" || p.Subject != "42.1" || p.Scope != "job" {
		t.Errorf("payload describes the wrong thing: %+v", p)
	}
	if p.Job == nil || p.Job.Status != "Held" {
		t.Errorf("the job was not carried: %+v", p.Job)
	}
	// A summary endpoint carries no ad, and says nothing about one: the
	// field is absent, not explained.
	if p.JobAd != nil || p.JobAdOmitted != "" {
		t.Errorf("a summary payload mentioned the ad: ad=%v omitted=%q", p.JobAd, p.JobAdOmitted)
	}
}

// TestFullPayloadCarriesTheAdBesideTheSummary pins the shape of the opt-in:
// the projection a receiver already maps stays exactly where it was.
func TestFullPayloadCarriesTheAdBesideTheSummary(t *testing.T) {
	ep := &Endpoint{ID: "wh_1", Label: "Zap", Owner: "alice", Payload: PayloadFull}
	sub := &Subscription{ID: "ws_1", EndpointID: ep.ID, Scope: ScopeOwner, Owner: "alice",
		Events: []jobwatch.Event{jobwatch.EventHeld}}
	ad := map[string]any{"ClusterId": 42, "ProcId": 1, "JobStatus": 5, "Owner": "alice"}
	body, err := RenderPayload(ep, sub, jobwatch.EventHeld,
		&JobPayload{Cluster: 42, Proc: 1, ID: "42.1", Status: "Held"}, ad, "k", time.Now(), false)
	if err != nil {
		t.Fatalf("RenderPayload: %v", err)
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Job == nil || p.Job.ID != "42.1" {
		t.Error("the summary was dropped when the whole ad was added")
	}
	if p.JobAd == nil {
		t.Fatal("a full-payload endpoint carried no ad")
	}
	// The ad keeps the ClassAd spelling a receiver will look for, not a
	// lower-cased or renamed one.
	if _, ok := p.JobAd["ClusterId"]; !ok {
		t.Errorf("the ad's attribute names were rewritten: %v", p.JobAd)
	}
}

// TestFullPayloadExplainsAMissingAd covers the case a receiver cannot
// otherwise distinguish: no ad, versus an ad with nothing in it.
func TestFullPayloadExplainsAMissingAd(t *testing.T) {
	ep := &Endpoint{ID: "wh_1", Owner: "alice", Label: "Zap", Payload: PayloadFull}
	sub := &Subscription{EndpointID: ep.ID, Owner: "alice", Scope: ScopeOwner,
		Events: []jobwatch.Event{jobwatch.EventDone}}

	body, err := RenderPayload(ep, sub, jobwatch.EventDone, nil, nil, "k", time.Now(), false)
	if err != nil {
		t.Fatalf("RenderPayload: %v", err)
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.JobAdOmitted == "" {
		t.Error("a full-payload delivery with no ad said nothing about why")
	}

	// An ad over the size limit is left out with its size named, not
	// truncated: half an ad is not a smaller ad, it is a malformed one.
	big := map[string]any{"Arguments": strings.Repeat("x", MaxAdBytes+1)}
	body, err = RenderPayload(ep, sub, jobwatch.EventDone, nil, big, "k2", time.Now(), false)
	if err != nil {
		t.Fatalf("RenderPayload: %v", err)
	}
	var q Payload
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if q.JobAd != nil {
		t.Error("an oversized ad was sent anyway")
	}
	if !strings.Contains(q.JobAdOmitted, "limit") {
		t.Errorf("the oversized ad was not explained: %q", q.JobAdOmitted)
	}
}

// TestDeliveryKeyIsStable guards the thing retries depend on.
func TestDeliveryKeyIsStable(t *testing.T) {
	job := jobwatch.JobID{Cluster: 5, Proc: 2, Schedd: "ap1"}
	a := DeliveryKey("wh_1", jobwatch.EventDone, job)
	b := DeliveryKey("wh_1", jobwatch.EventDone, job)
	if a != b {
		t.Fatalf("the same event produced two keys: %q and %q", a, b)
	}
	// An access point is part of a job's identity: the same cluster.proc on
	// two APs is two jobs.
	other := DeliveryKey("wh_1", jobwatch.EventDone,
		jobwatch.JobID{Cluster: 5, Proc: 2, Schedd: "ap2"})
	if a == other {
		t.Error("two access points' jobs share a delivery key")
	}
}

// fakeSealer is a sealer that does enough to tell sealed from plaintext
// without a key schedule: it reverses the bytes and hands back a marker DEK.
type fakeSealer struct{}

func (fakeSealer) Seal(plaintext []byte) ([]byte, []byte, error) {
	out := make([]byte, len(plaintext))
	for i, b := range plaintext {
		out[len(plaintext)-1-i] = b
	}
	return out, []byte("dek"), nil
}

func (fakeSealer) Open(data, dek []byte) ([]byte, error) {
	if string(dek) != "dek" {
		return nil, ErrNoKEK
	}
	out := make([]byte, len(data))
	for i, b := range data {
		out[len(data)-1-i] = b
	}
	return out, nil
}
