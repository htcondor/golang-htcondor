package webhooks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/PelicanPlatform/classad/classad"

	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
)

// What the evaluator decides, given one read of an owner's jobs.

// Job statuses, as the schedd writes them.
const (
	statusIdle      = 1
	statusRunningJS = 2
	statusRemoved   = 3
	statusCompleted = 4
	statusHeldJS    = 5
)

// fakeSource is a jobwatch.Source over fixed ads.
type fakeSource struct {
	queue     []*classad.ClassAd
	history   []*classad.ClassAd
	truncated bool
	queueErr  error
	histErr   error
	// queueCalls counts reads, to show that one pass reads an owner once
	// however many hooks they hold.
	queueCalls int
}

func (f *fakeSource) Queue(_ context.Context, _ string, _ []string, _ int, yield func(*classad.ClassAd)) (bool, error) {
	f.queueCalls++
	if f.queueErr != nil {
		return false, f.queueErr
	}
	for _, ad := range f.queue {
		yield(ad)
	}
	return f.truncated, nil
}

func (f *fakeSource) History(_ context.Context, _ string, _ []string, _ time.Time, _ int, yield func(*classad.ClassAd)) error {
	if f.histErr != nil {
		return f.histErr
	}
	for _, ad := range f.history {
		yield(ad)
	}
	return nil
}

func jobAd(cluster, proc, status int64) *classad.ClassAd {
	ad := classad.New()
	ad.InsertAttr("ClusterId", cluster)
	ad.InsertAttr("ProcId", proc)
	ad.InsertAttr("JobStatus", status)
	ad.InsertAttrString("Owner", "alice")
	ad.InsertAttrString("Cmd", "/bin/work")
	return ad
}

// finishedAd is a terminal record for job 42.0, which is what a history row
// and a completed ad still in the queue both are.
//
// The job is the helper's rather than the caller's: 42.0 is the job every
// test here hooks, and the only thing that varies between them is how it
// ended. The same shape as jobwatch's own historyAd, for the same reason.
func finishedAd(exitCode int64) *classad.ClassAd {
	ad := jobAd(42, 0, statusCompleted)
	ad.InsertAttr("ExitCode", exitCode)
	ad.InsertAttrBool("ExitBySignal", false)
	ad.InsertAttr("JobStartDate", int64(1700000000))
	return ad
}

// heldBatchAd is a held job carrying a batch name, which is the only state
// the batch-selection tests look at: they are about which jobs a hook
// selects, not about what state selects them.
func heldBatchAd(cluster, proc int64, batch string) *classad.ClassAd {
	ad := jobAd(cluster, proc, statusHeldJS)
	ad.InsertAttrString("JobBatchName", batch)
	return ad
}

// evalFixture is a store, a source and an evaluator over them.
type evalFixture struct {
	store *Store
	src   *fakeSource
	eval  *Evaluator
}

func newEvalFixture(t *testing.T) *evalFixture {
	t.Helper()
	store := newStore(t)
	src := &fakeSource{}
	eval := NewEvaluator(store, src, func(string, ...any) {})
	return &evalFixture{store: store, src: src, eval: eval}
}

// hook creates a live hook.
func (f *evalFixture) hook(t *testing.T, h *Hook) *Hook {
	t.Helper()
	out, _, err := f.store.Create(context.Background(), h, "https://hooks.example.org/x")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return out
}

// queued returns the deliveries sitting on the queue for a hook.
func (f *evalFixture) queued(t *testing.T, hookID string) []Delivery {
	t.Helper()
	rows, err := f.store.db.QueryContext(context.Background(),
		`SELECT id, event, job_cluster, job_proc, payload_json
		   FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id`, hookID)
	if err != nil {
		t.Fatalf("query deliveries: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Delivery
	for rows.Next() {
		var (
			d             Delivery
			event         string
			cluster, proc *int64
			payload       string
		)
		if err := rows.Scan(&d.ID, &event, &cluster, &proc, &payload); err != nil {
			t.Fatalf("scan: %v", err)
		}
		d.Event = jobwatch.Event(event)
		d.Payload = []byte(payload)
		if cluster != nil {
			d.Job = &jobwatch.JobID{Cluster: *cluster, Proc: *proc}
		}
		out = append(out, d)
	}
	return out
}

func (f *evalFixture) pass(t *testing.T) EvalStats {
	t.Helper()
	st, err := f.eval.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	return st
}

func jobHook(cluster, proc int64, events ...jobwatch.Event) *Hook {
	return &Hook{Owner: "alice", Label: "job hook", Scope: ScopeJob,
		Job: jobwatch.JobID{Cluster: cluster, Proc: proc}, Events: events}
}

// --- firing --------------------------------------------------------------

func TestTerminalAdInQueueFires(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventSucceeded, jobwatch.EventDone))
	// A completed job still in the queue, which is what an OSPool access
	// point leaves in job_queue.log rather than archiving at once. A hook
	// that only resolved terminal events from history would never fire.
	f.src.queue = []*classad.ClassAd{finishedAd(0)}

	st := f.pass(t)
	if st.Enqueued != 2 {
		t.Fatalf("enqueued %d, want 2 (succeeded and done): %+v", st.Enqueued, st)
	}
	got := f.queued(t, h.ID)
	events := map[jobwatch.Event]bool{}
	for _, d := range got {
		events[d.Event] = true
		if d.Job == nil || d.Job.Cluster != 42 {
			t.Errorf("delivery %s names job %+v", d.ID, d.Job)
		}
	}
	if !events[jobwatch.EventSucceeded] || !events[jobwatch.EventDone] {
		t.Errorf("fired %v, want succeeded and done", events)
	}
}

func TestHistoryRowFiresFailed(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventFailed))
	f.src.history = []*classad.ClassAd{finishedAd(1)}

	if st := f.pass(t); st.Enqueued != 1 {
		t.Fatalf("enqueued %d, want 1: %+v", st.Enqueued, st)
	}
	got := f.queued(t, h.ID)
	if len(got) != 1 || got[0].Event != jobwatch.EventFailed {
		t.Fatalf("fired %+v, want one failed", got)
	}
	var p Payload
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Job == nil || p.Job.ExitCode == nil || *p.Job.ExitCode != 1 {
		t.Errorf("the payload does not carry the exit code: %+v", p.Job)
	}
	if p.Job.Command != "/bin/work" {
		t.Errorf("the payload does not carry the command: %q", p.Job.Command)
	}
}

func TestHeldJobFires(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventHeld))
	ad := jobAd(42, 0, statusHeldJS)
	ad.InsertAttrString("HoldReason", "no matching slots")
	f.src.queue = []*classad.ClassAd{ad}

	if st := f.pass(t); st.Enqueued != 1 {
		t.Fatalf("enqueued %d, want 1", st.Enqueued)
	}
	got := f.queued(t, h.ID)
	var p Payload
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The hold reason is the whole reason somebody wants to be told.
	if p.Job == nil || p.Job.HoldReason != "no matching slots" {
		t.Errorf("the payload does not carry the hold reason: %+v", p.Job)
	}
}

func TestIdleJobFiresNothing(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventDone, jobwatch.EventFailed, jobwatch.EventRunning))
	f.src.queue = []*classad.ClassAd{jobAd(42, 0, statusIdle)}

	if st := f.pass(t); st.Enqueued != 0 {
		t.Fatalf("an idle job fired %d deliveries", st.Enqueued)
	}
	if got := f.queued(t, h.ID); len(got) != 0 {
		t.Errorf("queued %+v for an idle job", got)
	}
}

// TestRepeatedPassesDoNotRedeliver is the idempotency guarantee seen from the
// outside: the state persists, so every pass sees the same satisfied job.
func TestRepeatedPassesDoNotRedeliver(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventHeld))
	f.src.queue = []*classad.ClassAd{jobAd(42, 0, statusHeldJS)}

	first := f.pass(t)
	second := f.pass(t)
	third := f.pass(t)
	if first.Enqueued != 1 {
		t.Fatalf("first pass enqueued %d, want 1", first.Enqueued)
	}
	if second.Enqueued != 0 || third.Enqueued != 0 {
		t.Errorf("a job still held re-delivered: %d then %d", second.Enqueued, third.Enqueued)
	}
	if got := f.queued(t, h.ID); len(got) != 1 {
		t.Errorf("%d delivery rows after three passes, want 1", len(got))
	}
}

// --- absence -------------------------------------------------------------

// TestAbsenceFiresDone is the evidence that exists for a job whose history
// row never arrives: it was here, the read is complete, and it is gone.
func TestAbsenceFiresDone(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventDone))

	// Seen running, so the hook tracks it.
	f.src.queue = []*classad.ClassAd{jobAd(42, 0, statusRunningJS)}
	if st := f.pass(t); st.Enqueued != 0 {
		t.Fatalf("a running job fired a done delivery")
	}

	// Gone from a complete read, with no history row to explain it.
	f.src.queue = nil
	if st := f.pass(t); st.Enqueued != 1 {
		t.Fatalf("enqueued %d on absence, want 1", st.Enqueued)
	}
	got := f.queued(t, h.ID)
	if len(got) != 1 || got[0].Event != jobwatch.EventDone {
		t.Fatalf("fired %+v, want one done", got)
	}
}

// TestAbsenceDoesNotFireSucceededOrFailed: whether it worked is in the ad
// that is no longer there, and inventing an answer is worse than not having
// one -- a wrongly reported failure gets acted on.
func TestAbsenceDoesNotFireSucceededOrFailed(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventSucceeded, jobwatch.EventFailed))

	f.src.queue = []*classad.ClassAd{jobAd(42, 0, statusRunningJS)}
	f.pass(t)
	f.src.queue = nil
	f.pass(t)

	if got := f.queued(t, h.ID); len(got) != 0 {
		t.Errorf("absence produced %+v; it cannot say how a job ended", got)
	}
}

// TestTruncatedReadProvesNothing: absent from part of the queue is not absent
// from the queue.
func TestTruncatedReadProvesNothing(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventDone))

	f.src.queue = []*classad.ClassAd{jobAd(42, 0, statusRunningJS)}
	f.pass(t)

	f.src.queue = nil
	f.src.truncated = true
	if st := f.pass(t); st.Enqueued != 0 {
		t.Fatalf("a truncated read fired %d done deliveries", st.Enqueued)
	}
	if got := f.queued(t, h.ID); len(got) != 0 {
		t.Errorf("queued %+v from a truncated read", got)
	}

	// Complete again: now absence means something.
	f.src.truncated = false
	if st := f.pass(t); st.Enqueued != 1 {
		t.Errorf("enqueued %d once the read was complete, want 1", st.Enqueued)
	}
}

// --- selection -----------------------------------------------------------

func TestJobHookIgnoresOtherJobs(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 1, jobwatch.EventHeld))
	f.src.queue = []*classad.ClassAd{
		jobAd(42, 0, statusHeldJS), // same cluster, different proc
		jobAd(43, 1, statusHeldJS), // different cluster
		jobAd(42, 1, statusHeldJS), // the one
	}
	if st := f.pass(t); st.Enqueued != 1 {
		t.Fatalf("enqueued %d, want exactly the one job", st.Enqueued)
	}
	got := f.queued(t, h.ID)
	if len(got) != 1 || got[0].Job.Proc != 1 || got[0].Job.Cluster != 42 {
		t.Errorf("fired for %+v, want 42.1", got)
	}
}

func TestBatchHookSelectsItsBatch(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, &Hook{Owner: "alice", Label: "nightly", Scope: ScopeBatch,
		BatchID: "100", BatchName: "nightly", Events: []jobwatch.Event{jobwatch.EventHeld}})
	f.src.queue = []*classad.ClassAd{
		heldBatchAd(100, 0, "nightly"),
		heldBatchAd(100, 1, "nightly"),
		heldBatchAd(101, 0, "other"),
		jobAd(102, 0, statusHeldJS), // no batch name at all
	}
	if st := f.pass(t); st.Enqueued != 2 {
		t.Fatalf("enqueued %d, want the 2 jobs of the batch", st.Enqueued)
	}
	for _, d := range f.queued(t, h.ID) {
		if d.Job.Cluster != 100 {
			t.Errorf("fired for %+v, which is not in the batch", d.Job)
		}
	}
}

func TestClusterBatchHookSelectsByCluster(t *testing.T) {
	f := newEvalFixture(t)
	// A lone cluster is a batch too, identified by its id with no name.
	h := f.hook(t, &Hook{Owner: "alice", Label: "cluster 7", Scope: ScopeBatch,
		BatchID: "7", Events: []jobwatch.Event{jobwatch.EventHeld}})
	f.src.queue = []*classad.ClassAd{
		jobAd(7, 0, statusHeldJS),
		jobAd(7, 1, statusHeldJS),
		jobAd(8, 0, statusHeldJS),
	}
	if st := f.pass(t); st.Enqueued != 2 {
		t.Fatalf("enqueued %d, want 2", st.Enqueued)
	}
	for _, d := range f.queued(t, h.ID) {
		if d.Job.Cluster != 7 {
			t.Errorf("fired for cluster %d, want 7", d.Job.Cluster)
		}
	}
}

func TestOwnerHookSelectsEverything(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, &Hook{Owner: "alice", Label: "all mine", Scope: ScopeOwner,
		Events: []jobwatch.Event{jobwatch.EventHeld}})
	f.src.queue = []*classad.ClassAd{
		jobAd(1, 0, statusHeldJS),
		heldBatchAd(2, 0, "nightly"),
		jobAd(3, 0, statusIdle), // not held
	}
	if st := f.pass(t); st.Enqueued != 2 {
		t.Fatalf("enqueued %d, want the 2 held jobs", st.Enqueued)
	}
	if got := f.queued(t, h.ID); len(got) != 2 {
		t.Errorf("%d deliveries, want 2", len(got))
	}
}

// TestOnePassReadsEachOwnerOnce is the coalescing that makes the evaluator
// cheap: a user with several hooks is not several times the load.
func TestOnePassReadsEachOwnerOnce(t *testing.T) {
	f := newEvalFixture(t)
	for i := 0; i < 4; i++ {
		f.hook(t, jobHook(int64(40+i), 0, jobwatch.EventHeld))
	}
	f.src.queue = []*classad.ClassAd{jobAd(40, 0, statusHeldJS)}
	f.pass(t)
	if f.src.queueCalls != 1 {
		t.Errorf("one pass read the owner's queue %d times, want 1", f.src.queueCalls)
	}
}

// --- the fan-out ceiling -------------------------------------------------

// TestFanoutCapRollsUpAndDisables: a hook that would post thousands of times
// is misconfigured, and the honest outcome is to say so once rather than
// either flooding the receiver or dropping the events in silence.
func TestFanoutCapRollsUpAndDisables(t *testing.T) {
	f := newEvalFixture(t)
	f.eval.FanoutCap = 3
	h := f.hook(t, &Hook{Owner: "alice", Label: "everything", Scope: ScopeOwner,
		Events: []jobwatch.Event{jobwatch.EventHeld}})
	for i := int64(0); i < 10; i++ {
		f.src.queue = append(f.src.queue, jobAd(100+i, 0, statusHeldJS))
	}

	st := f.pass(t)
	if st.Capped != 1 {
		t.Fatalf("capped %d hooks, want 1: %+v", st.Capped, st)
	}
	got := f.queued(t, h.ID)
	if len(got) != 1 {
		t.Fatalf("%d deliveries, want one rolled-up: %+v", len(got), got)
	}
	if got[0].Job != nil {
		t.Errorf("the rolled-up delivery names a job (%+v); there were too many to name", got[0].Job)
	}
	var p Payload
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Capped == nil {
		t.Fatal("the rolled-up delivery does not say it was capped")
	}
	if p.Capped.Jobs != 10 || p.Capped.Limit != 3 {
		t.Errorf("summary = %+v, want 10 jobs over a limit of 3", p.Capped)
	}

	stored, err := f.store.Get(context.Background(), "alice", h.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.DisabledAt == nil {
		t.Error("a capped hook was left enabled, and would cap again every pass")
	}
	if stored.CappedJobs != 10 {
		t.Errorf("capped_jobs = %d, want 10", stored.CappedJobs)
	}
	// Disabled means no longer evaluated, so the next pass does nothing.
	if next := f.pass(t); next.Capped != 0 || next.Enqueued != 0 {
		t.Errorf("a disabled hook was evaluated again: %+v", next)
	}
}

func TestUnderTheCapDeliversPerJob(t *testing.T) {
	f := newEvalFixture(t)
	f.eval.FanoutCap = 10
	h := f.hook(t, &Hook{Owner: "alice", Label: "mine", Scope: ScopeOwner,
		Events: []jobwatch.Event{jobwatch.EventHeld}})
	for i := int64(0); i < 5; i++ {
		f.src.queue = append(f.src.queue, jobAd(200+i, 0, statusHeldJS))
	}
	if st := f.pass(t); st.Enqueued != 5 || st.Capped != 0 {
		t.Fatalf("got %+v, want 5 per-job deliveries and no cap", st)
	}
	for _, d := range f.queued(t, h.ID) {
		if d.Job == nil {
			t.Error("a per-job delivery names no job")
		}
	}
}

// --- expiry --------------------------------------------------------------

// TestTerminableHookEndsWithItsSubject: a job hook stops being evaluated once
// the job can no longer produce an event.
func TestTerminableHookEndsWithItsSubject(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventDone))
	ctx := context.Background()

	before, err := f.store.Get(ctx, "alice", h.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// A backstop deadline, well in the future: the hook is not ending
	// because of it.
	if before.ExpiresAt == nil {
		t.Fatal("a job hook was created with no backstop deadline")
	}
	if before.ExpiresAt.Before(time.Now().Add(24 * time.Hour)) {
		t.Fatalf("the backstop is %s, which is too soon to be a backstop", before.ExpiresAt)
	}

	f.src.queue = []*classad.ClassAd{finishedAd(0)}
	st := f.pass(t)
	if st.Expired != 1 {
		t.Fatalf("expired %d hooks, want 1: %+v", st.Expired, st)
	}
	after, err := f.store.Get(ctx, "alice", h.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Brought forward to the grace period, not to now: a succeeded/failed
	// hook is still waiting for the history row that says which.
	if !after.ExpiresAt.Before(*before.ExpiresAt) {
		t.Errorf("the deadline was not brought forward: %s", after.ExpiresAt)
	}
	if after.ExpiresAt.Before(time.Now()) {
		t.Errorf("the hook expired immediately (%s); the grace period is gone", after.ExpiresAt)
	}
}

// TestHookWithNoJobsYetDoesNotExpire is the common case for a hook created
// before the work is submitted. Expiring it would expire the normal state.
func TestHookWithNoJobsYetDoesNotExpire(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(999, 0, jobwatch.EventDone))
	// Nothing in the queue, nothing in history: the job has not been
	// submitted yet.
	if st := f.pass(t); st.Expired != 0 {
		t.Fatalf("expired %d hooks although nothing has been seen", st.Expired)
	}
	stored, err := f.store.Get(context.Background(), "alice", h.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ExpiresAt.Before(time.Now().Add(24 * time.Hour)) {
		t.Errorf("the deadline was brought forward to %s for a hook that has seen nothing", stored.ExpiresAt)
	}
}

func TestOwnerHookNeverEndsWithItsJobs(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, &Hook{Owner: "alice", Label: "all", Scope: ScopeOwner,
		Events: []jobwatch.Event{jobwatch.EventDone}})

	f.src.queue = []*classad.ClassAd{jobAd(1, 0, statusRunningJS)}
	f.pass(t)
	f.src.queue = nil
	// Everything the hook ever saw has now gone, which ends a job hook and
	// must not end this one: there is always another job.
	if st := f.pass(t); st.Expired != 0 {
		t.Fatalf("an owner hook expired with its jobs: %+v", st)
	}
	stored, err := f.store.Get(context.Background(), "alice", h.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ExpiresAt.Before(time.Now().Add(24 * time.Hour)) {
		t.Errorf("the owner hook's deadline moved to %s", stored.ExpiresAt)
	}
}

// --- degradation ---------------------------------------------------------

// TestHistoryFailureStillFiresFromTheQueue: without history the terminal
// events still come from the queue and from absence, which is the same
// degradation jobwatch accepts when the mirror is down.
func TestHistoryFailureStillFiresFromTheQueue(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventSucceeded))
	f.src.histErr = context.DeadlineExceeded
	f.src.queue = []*classad.ClassAd{finishedAd(0)}

	if st := f.pass(t); st.Enqueued != 1 {
		t.Fatalf("enqueued %d with history unavailable, want 1", st.Enqueued)
	}
	if got := f.queued(t, h.ID); len(got) != 1 {
		t.Errorf("%d deliveries, want 1", len(got))
	}
}

// TestQueueFailureIsReportedNotSwallowed: a pass that could not read the
// queue has learned nothing, and must not conclude that absent jobs finished.
func TestQueueFailureIsReportedNotSwallowed(t *testing.T) {
	f := newEvalFixture(t)
	h := f.hook(t, jobHook(42, 0, jobwatch.EventDone))

	f.src.queue = []*classad.ClassAd{jobAd(42, 0, statusRunningJS)}
	f.pass(t)

	f.src.queueErr = context.DeadlineExceeded
	st, err := f.eval.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass returned an error for one owner's failure: %v", err)
	}
	if st.Errors != 1 {
		t.Errorf("errors = %d, want 1", st.Errors)
	}
	if got := f.queued(t, h.ID); len(got) != 0 {
		t.Errorf("a failed queue read concluded that a job finished: %+v", got)
	}
}
