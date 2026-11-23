package connectors

import (
	"errors"
	"testing"
)

// The binding is the authorization, so what it does and does not
// authorize is the thing to pin.

func testBinding() OutputBinding {
	return OutputBinding{
		Subject:   "alice",
		Cluster:   12,
		Connector: MustParse("builtin:gdrive"),
		FileID:    "SHEET",
		Tab:       "results",
		JobFile:   "result.json",
	}
}

func TestOutputBindingRoundTrip(t *testing.T) {
	s, _ := newTestStore(t, false)
	ctx := t.Context()

	if err := s.PutOutputBinding(ctx, testBinding()); err != nil {
		t.Fatalf("PutOutputBinding: %v", err)
	}
	got, err := s.OutputBindingFor(ctx, "alice", "", 12)
	if err != nil {
		t.Fatalf("OutputBindingFor: %v", err)
	}
	if got.FileID != "SHEET" || got.Tab != "results" || got.JobFile != "result.json" {
		t.Errorf("binding = %+v, want the one that was put", got)
	}
	if got.Connector.String() != "builtin:gdrive" {
		t.Errorf("connector = %q", got.Connector.String())
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at was not set")
	}
}

// Nothing nominated this cluster, so nothing authorizes writing for it.
// The error is distinguishable, because "no binding" is a refusal and a
// database failure is not.
func TestOutputBindingAbsenceIsItsOwnError(t *testing.T) {
	s, _ := newTestStore(t, false)
	_, err := s.OutputBindingFor(t.Context(), "alice", "", 99)
	if !errors.Is(err, ErrNoBinding) {
		t.Errorf("error = %v, want ErrNoBinding", err)
	}
}

// A binding belongs to one user. Another user's lookup of the same
// cluster finds nothing -- the subject is part of the key, not a filter
// applied afterwards.
func TestOutputBindingIsPerSubject(t *testing.T) {
	s, _ := newTestStore(t, false)
	ctx := t.Context()
	if err := s.PutOutputBinding(ctx, testBinding()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutputBindingFor(ctx, "bob", "", 12); !errors.Is(err, ErrNoBinding) {
		t.Errorf("another user found alice's binding: %v", err)
	}
}

// And to one access point. Cluster ids are unique only per schedd, so
// merging them would append one submission's results under another's
// destination.
func TestOutputBindingIsPerSchedd(t *testing.T) {
	s, _ := newTestStore(t, false)
	ctx := t.Context()
	b := testBinding()
	b.Schedd = "ap1"
	if err := s.PutOutputBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutputBindingFor(ctx, "alice", "ap2", 12); !errors.Is(err, ErrNoBinding) {
		t.Errorf("a different access point found this binding: %v", err)
	}
	if _, err := s.OutputBindingFor(ctx, "alice", "ap1", 12); err != nil {
		t.Errorf("the right access point did not: %v", err)
	}
}

// Retiring ends the authority. This is the whole mechanism that stops a
// cluster id reused by a later submission from inheriting this one's
// destination.
func TestRetiredBindingAuthorizesNothing(t *testing.T) {
	s, _ := newTestStore(t, false)
	ctx := t.Context()
	if err := s.PutOutputBinding(ctx, testBinding()); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireOutputBinding(ctx, "alice", "", 12); err != nil {
		t.Fatalf("RetireOutputBinding: %v", err)
	}
	if _, err := s.OutputBindingFor(ctx, "alice", "", 12); !errors.Is(err, ErrNoBinding) {
		t.Errorf("a retired binding still authorizes: %v", err)
	}
	open, err := s.OpenOutputBindings(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Errorf("a retired binding is still in the worker's view: %v", open)
	}
}

// Re-binding the same cluster replaces the destination and un-retires
// it, because a resubmission under a reused cluster id is a new
// decision and must not inherit the old one.
func TestPutOutputBindingReplaces(t *testing.T) {
	s, _ := newTestStore(t, false)
	ctx := t.Context()
	if err := s.PutOutputBinding(ctx, testBinding()); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireOutputBinding(ctx, "alice", "", 12); err != nil {
		t.Fatal(err)
	}
	next := testBinding()
	next.FileID = "OTHER"
	next.Tab = "second"
	if err := s.PutOutputBinding(ctx, next); err != nil {
		t.Fatal(err)
	}
	got, err := s.OutputBindingFor(ctx, "alice", "", 12)
	if err != nil {
		t.Fatalf("the replacement did not un-retire the binding: %v", err)
	}
	if got.FileID != "OTHER" || got.Tab != "second" {
		t.Errorf("binding = %+v, want the replacement", got)
	}
}

// An incomplete binding is refused rather than stored. A row missing
// its tab would be an authorization for a destination that cannot be
// written to, which is worse than no row at all.
func TestPutOutputBindingRefusesAnIncompleteOne(t *testing.T) {
	s, _ := newTestStore(t, false)
	for _, tc := range []struct {
		name   string
		mutate func(*OutputBinding)
	}{
		{"no subject", func(b *OutputBinding) { b.Subject = "" }},
		{"no spreadsheet", func(b *OutputBinding) { b.FileID = "" }},
		{"no tab", func(b *OutputBinding) { b.Tab = "" }},
		{"no result file", func(b *OutputBinding) { b.JobFile = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := testBinding()
			tc.mutate(&b)
			if err := s.PutOutputBinding(t.Context(), b); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// At-most-once per job: the first account stands, and a retry does not
// overwrite it. values.append has no idempotency key, so a job added
// twice leaves two identical rows that nobody can tell apart.
func TestMarkAppendedIsAtMostOnce(t *testing.T) {
	s, _ := newTestStore(t, false)
	ctx := t.Context()
	if err := s.PutOutputBinding(ctx, testBinding()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAppended(ctx, "alice", "", 12,
		AppendedJob{Proc: 0, Rows: 3}); err != nil {
		t.Fatal(err)
	}
	// A second call for the same proc: a retry that should not have
	// happened.
	if err := s.MarkAppended(ctx, "alice", "", 12,
		AppendedJob{Proc: 0, Rows: 99, Note: "again"}); err != nil {
		t.Fatalf("the retry errored rather than being ignored: %v", err)
	}
	got, err := s.AppendedJobs(ctx, "alice", "", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d accounted jobs, want 1", len(got))
	}
	if got[0].Rows != 3 || got[0].Note != "" {
		t.Errorf("the retry overwrote the first account: %+v", got[0])
	}
}

// A job that produced nothing is still accounted for. Zero rows and
// "not looked at yet" must be distinguishable, or a job with no records
// is retried for ever.
func TestMarkAppendedRecordsAZeroRowJob(t *testing.T) {
	s, _ := newTestStore(t, false)
	ctx := t.Context()
	if err := s.PutOutputBinding(ctx, testBinding()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAppended(ctx, "alice", "", 12,
		AppendedJob{Proc: 7, Rows: 0, Note: "no result file in the sandbox"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.AppendedJobs(ctx, "alice", "", 12)
	if err != nil {
		t.Fatal(err)
	}
	j, ok := got[7]
	if !ok {
		t.Fatal("the job is not accounted for")
	}
	if j.Rows != 0 || j.Note == "" {
		t.Errorf("job = %+v, want zero rows and a reason", j)
	}
}

// The worker's view is only the open bindings, oldest first.
func TestOpenOutputBindingsAreOldestFirst(t *testing.T) {
	s, _ := newTestStore(t, false)
	ctx := t.Context()
	for _, cluster := range []int{5, 6, 7} {
		b := testBinding()
		b.Cluster = cluster
		if err := s.PutOutputBinding(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RetireOutputBinding(ctx, "alice", "", 6); err != nil {
		t.Fatal(err)
	}
	open, err := s.OpenOutputBindings(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 2 {
		t.Fatalf("got %d open bindings, want 2: %+v", len(open), open)
	}
	for _, b := range open {
		if b.Cluster == 6 {
			t.Error("a retired binding is in the worker's view")
		}
	}
}
