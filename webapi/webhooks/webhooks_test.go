package webhooks

import (
	"context"
	"encoding/json"
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

func ownerHook(owner, label string) *Hook {
	return &Hook{
		Owner: owner, Label: label, Scope: ScopeOwner,
		Events: []jobwatch.Event{jobwatch.EventFailed},
	}
}

// --- the model -----------------------------------------------------------

func TestValidateRejectsWhatCannotWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		hook *Hook
		want string
	}{
		{"no label", &Hook{Owner: "alice", Scope: ScopeOwner,
			Events: []jobwatch.Event{jobwatch.EventDone}}, "name is required"},
		{"unknown scope", &Hook{Owner: "alice", Label: "x", Scope: "galaxy",
			Events: []jobwatch.Event{jobwatch.EventDone}}, "unknown scope"},
		{"no events", &Hook{Owner: "alice", Label: "x", Scope: ScopeOwner}, "at least one event"},
		{"duplicate event", &Hook{Owner: "alice", Label: "x", Scope: ScopeOwner,
			Events: []jobwatch.Event{jobwatch.EventDone, jobwatch.EventDone}}, "duplicate event"},
		// Custom is a jobwatch event and deliberately not a hook event: it
		// has no terminal resolution, so it would fire only for jobs still
		// in the queue and a hook matching nothing would look like a quiet
		// pool.
		{"custom event", &Hook{Owner: "alice", Label: "x", Scope: ScopeOwner,
			Events: []jobwatch.Event{jobwatch.EventCustom}}, "unsupported event"},
		{"job hook with no job", &Hook{Owner: "alice", Label: "x", Scope: ScopeJob,
			Events: []jobwatch.Event{jobwatch.EventDone}}, "needs a cluster id"},
		{"batch hook with no batch", &Hook{Owner: "alice", Label: "x", Scope: ScopeBatch,
			Events: []jobwatch.Event{jobwatch.EventDone}}, "needs a batch"},
		{"owner hook naming a job", &Hook{Owner: "alice", Label: "x", Scope: ScopeOwner,
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

	h, secret, err := s.Create(ctx, ownerHook("alice", "Slack"), dest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(secret) != SecretBytes {
		t.Errorf("secret is %d bytes, want %d", len(secret), SecretBytes)
	}
	if h.ID == "" {
		t.Fatal("Create returned no id")
	}
	// An owner hook has no natural end, so it gets a deadline at creation.
	if h.ExpiresAt == nil {
		t.Error("an owner hook was created with no deadline")
	}

	got, err := s.Get(ctx, "alice", h.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Label != "Slack" || got.Scope != ScopeOwner {
		t.Errorf("read back %+v", got)
	}
	if !got.HasDestination {
		t.Error("HasDestination is false for a hook with one")
	}
	if strings.Contains(got.DestinationHint, "secret-path") {
		t.Errorf("the hint leaks the path: %q", got.DestinationHint)
	}

	// Somebody else's hook is not found, rather than refused: a 403 would
	// confirm the id exists.
	if _, err := s.Get(ctx, "bob", h.ID); err != ErrNotFound {
		t.Errorf("Get as another owner = %v, want ErrNotFound", err)
	}
	if hooks, err := s.ForOwner(ctx, "bob"); err != nil || len(hooks) != 0 {
		t.Errorf("ForOwner(bob) = %v, %v; want no hooks", hooks, err)
	}

	// Destination is the only way to the value, and it does return it.
	back, backSecret, err := s.Destination(ctx, h.ID)
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

func TestCreateSealsWhenAKeyIsConfigured(t *testing.T) {
	s := newStore(t)
	s.sealer = fakeSealer{}
	ctx := context.Background()
	const dest = "https://hooks.example.org/sealed"

	h, _, err := s.Create(ctx, ownerHook("alice", "Sealed"), dest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Stored sealed: the raw column must not contain the URL.
	var raw []byte
	if err := s.db.QueryRowContext(ctx,
		`SELECT url_data FROM webhooks WHERE id = ?`, h.ID).Scan(&raw); err != nil {
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
	if _, _, err := s.Destination(ctx, h.ID); err != ErrNoKEK {
		t.Errorf("Destination with no sealer = %v, want ErrNoKEK", err)
	}
}

func TestCreateEnforcesQuota(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < MaxHooksPerOwner; i++ {
		if _, _, err := s.Create(ctx, ownerHook("alice", "h"), "https://hooks.example.org/x"); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	if _, _, err := s.Create(ctx, ownerHook("alice", "one too many"), "https://hooks.example.org/x"); err == nil {
		t.Fatal("Create past the quota succeeded")
	}
	// Somebody else's quota is their own.
	if _, _, err := s.Create(ctx, ownerHook("bob", "mine"), "https://hooks.example.org/x"); err != nil {
		t.Errorf("bob was refused for alice's hooks: %v", err)
	}
}

func TestDeleteIsOwnerScopedAndTakesDeliveries(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	h, _, err := s.Create(ctx, ownerHook("alice", "Slack"), "https://hooks.example.org/x")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	job := jobwatch.JobID{Cluster: 7, Proc: 0}
	key := DeliveryKey(h.ID, jobwatch.EventFailed, job)
	if _, err := s.Enqueue(ctx, h.ID, "alice", jobwatch.EventFailed, &job, []byte("{}"), key); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if err := s.Delete(ctx, "bob", h.ID); err != ErrNotFound {
		t.Errorf("Delete as another owner = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "alice", h.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM webhook_deliveries WHERE webhook_id = ?`, h.ID).Scan(&n); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if n != 0 {
		t.Errorf("%d deliveries outlived their hook", n)
	}
}

// TestEnqueueDeduplicates is the idempotency guarantee. The key is the
// primary key, so a pass that sees the same job in the same state again
// cannot enqueue a second POST.
func TestEnqueueDeduplicates(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	h, _, err := s.Create(ctx, ownerHook("alice", "Slack"), "https://hooks.example.org/x")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
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
		`SELECT COUNT(*) FROM webhook_deliveries WHERE webhook_id = ?`, h.ID).Scan(&n); err != nil {
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
	h, _, err := s.Create(ctx, ownerHook("alice", "Slack"), "https://hooks.example.org/x")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
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
	h := &Hook{ID: "wh_1", Label: "Slack", Scope: ScopeJob,
		Job: jobwatch.JobID{Cluster: 42, Proc: 1}, Owner: "alice",
		Events: []jobwatch.Event{jobwatch.EventFailed}}
	key := DeliveryKey(h.ID, jobwatch.EventFailed, h.Job)
	body, err := RenderPayload(h, jobwatch.EventFailed,
		&JobPayload{Cluster: 42, Proc: 1, ID: "42.1", Status: "Held"}, key, time.Now(), false)
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
