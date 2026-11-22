package webhooks

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PelicanPlatform/classad/classad"

	"github.com/htcondor/golang-htcondor/webapi/batches"
	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
)

// Deciding what to deliver.
//
// The shape follows jobwatch's evaluator, because the problem is the same
// one: read each owner's jobs ONCE per pass and fold every interested
// subscription over that read. A hook per query would make the cost scale
// with the number of hooks, and a user with ten hooks would be ten times the
// load of a user with one for no extra information.
//
// What differs is that a hook is STANDING where a watch is one-shot. A watch
// fires once and stops being evaluated; a hook keeps going for the life of
// its subject and fires once PER JOB. So there is no fired_at here, and
// nothing to re-arm: what stops a job being notified about twice is that the
// delivery's idempotency key is the primary key of the row recording it, so
// the second enqueue is a no-op in the database rather than a decision in
// this file.

// EvalAttrs is the projection one pass reads.
//
// Generous on purpose, the same reasoning as jobwatch.BaseAttrs: a projected
// read that omitted one of these would not error -- the attribute would
// evaluate to UNDEFINED and the comparison would quietly be false -- so a
// missing attribute costs a hook that never fires, which is invisible, while
// an extra one costs bytes.
var EvalAttrs = func() []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{jobwatch.BaseAttrs, batches.Attrs} {
		for _, a := range list {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}()

// Evaluator finds the events live hooks are waiting for and enqueues a
// delivery for each.
type Evaluator struct {
	store *Store
	src   jobwatch.Source
	logf  func(string, ...any)

	// Period is how often a sweep runs when nothing nudges it.
	Period time.Duration
	// FanoutCap is the most jobs one hook may deliver for.
	//
	// A hook over a cluster of a hundred thousand would otherwise post a
	// hundred thousand times, which is a denial of service against the
	// receiver carried out on the user's behalf. Past the cap the hook
	// sends one rolled-up delivery saying what happened and how big it was,
	// and is then disabled with a reason -- a hook that would fan out that
	// far is misconfigured, and telling the user is better than either
	// flooding the receiver or dropping the events in silence.
	FanoutCap int
	// owners serializes passes per owner; see evalOnce.
	owners evalOnce
	// HistoryWindow is how far back a pass reads finished jobs. It only has
	// to cover the gap since the previous pass, with room for a late
	// history row; reading further costs time and finds nothing new,
	// because anything older has already been enqueued and deduplicated.
	HistoryWindow time.Duration

	now func() time.Time
}

// Evaluator defaults.
const (
	// DefaultPeriod matches jobwatch's sweep. The change feed is what makes
	// a notification prompt; this is the floor under it.
	DefaultPeriod = 30 * time.Second
	// DefaultFanoutCap is deliberately well above a normal batch and well
	// below a large cluster.
	DefaultFanoutCap = 500
	// DefaultHistoryWindow covers a pass plus a generous late history row.
	DefaultHistoryWindow = 30 * time.Minute
)

// NewEvaluator returns an evaluator with the default pacing.
func NewEvaluator(store *Store, src jobwatch.Source, logf func(string, ...any)) *Evaluator {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Evaluator{
		store:         store,
		src:           src,
		logf:          logf,
		Period:        DefaultPeriod,
		FanoutCap:     DefaultFanoutCap,
		HistoryWindow: DefaultHistoryWindow,
		now:           time.Now,
	}
}

// EvalStats is what one pass did.
type EvalStats struct {
	Hooks    int
	Owners   int
	Enqueued int
	Expired  int
	Capped   int
	Errors   int
}

// Run sweeps until ctx is done.
func (e *Evaluator) Run(ctx context.Context) {
	ticker := time.NewTicker(e.Period)
	defer ticker.Stop()
	for {
		if st, err := e.Pass(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			e.logf("webhook evaluation pass failed", "error", err)
		} else if st.Enqueued > 0 || st.Errors > 0 || st.Capped > 0 {
			e.logf("webhook events found", "hooks", st.Hooks, "enqueued", st.Enqueued,
				"expired", st.Expired, "capped", st.Capped, "errors", st.Errors)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Pass reads every live hook, groups by owner, and evaluates each owner once.
//
// One owner's failure does not abandon the pass. Hooks are independent, and
// the likeliest cause of a failure is a per-owner authorization problem
// rather than a systemic one -- which is no reason to stop answering
// everybody else's.
func (e *Evaluator) Pass(ctx context.Context) (EvalStats, error) {
	var st EvalStats
	live, err := e.store.Live(ctx)
	if err != nil {
		return st, fmt.Errorf("listing live webhooks: %w", err)
	}
	st.Hooks = len(live)
	if len(live) == 0 {
		return st, nil
	}

	byOwner := make(map[string][]*Hook, 4)
	for _, h := range live {
		byOwner[h.Owner] = append(byOwner[h.Owner], h)
	}
	st.Owners = len(byOwner)

	for owner, hooks := range byOwner {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		got, err := e.passOwner(ctx, owner, hooks)
		st.Enqueued += got.Enqueued
		st.Expired += got.Expired
		st.Capped += got.Capped
		if err != nil {
			st.Errors++
			e.logf("evaluating webhooks failed for one owner", "owner", owner, "error", err)
		}
	}
	return st, nil
}

// CheckOwner evaluates one owner's hooks now, for the change-stream nudge.
func (e *Evaluator) CheckOwner(ctx context.Context, owner string) (int, error) {
	hooks, err := e.store.Live(ctx)
	if err != nil {
		return 0, err
	}
	var mine []*Hook
	for _, h := range hooks {
		if h.Owner == owner {
			mine = append(mine, h)
		}
	}
	if len(mine) == 0 {
		return 0, nil
	}
	st, err := e.passOwner(ctx, owner, mine)
	return st.Enqueued, err
}

// hookState is what one pass learns about one hook.
type hookState struct {
	hook *Hook
	// inScope is every job this hook selects, seen this pass or remembered
	// from a previous one.
	inScope map[jobwatch.JobID]bool
	// ended is the subset known to have finished: a terminal ad, a history
	// row, or absence from a complete read.
	ended map[jobwatch.JobID]bool
	// fire is the deliveries this pass decided on.
	fire []pending
	// over is set when the hook's selection exceeded the fan-out cap.
	over bool
}

// pending is one delivery to enqueue.
type pending struct {
	event jobwatch.Event
	job   jobwatch.JobID
	// payload is the job as the delivery will describe it. Rendered while
	// the ad is in hand, because the ad is not kept.
	payload *JobPayload
}

// passOwner evaluates one owner's hooks against one read of their jobs.
func (e *Evaluator) passOwner(ctx context.Context, owner string, hooks []*Hook) (EvalStats, error) {
	// One pass per owner at a time. A sweep and a change-stream nudge can
	// both decide to look at the same owner, and two passes would read the
	// same jobs and race to save the tracked set -- the loser's write
	// forgetting what the winner learned.
	lock := e.owners.lock(owner)
	lock.Lock()
	defer lock.Unlock()

	var st EvalStats
	now := e.now().UTC()

	states := make([]*hookState, 0, len(hooks))
	for _, h := range hooks {
		s := &hookState{
			hook:    h,
			inScope: make(map[jobwatch.JobID]bool, len(h.Tracked)+8),
			ended:   make(map[jobwatch.JobID]bool, 8),
		}
		// The remembered set is in scope before anything arrives: a cluster
		// that has fully drained would otherwise look like an empty
		// selection, and absence would have nothing to be the absence of.
		for _, id := range h.Tracked {
			s.inScope[id] = true
		}
		states = append(states, s)
	}

	// seen is every job observed in this pass's queue read, and endedNow
	// every job observed terminal. Absence is judged against both.
	seen := make(map[jobwatch.JobID]bool, 128)
	endedNow := make(map[jobwatch.JobID]bool, 32)

	fold := func(ad *classad.ClassAd, obs jobwatch.Observation) {
		fields := batches.FieldsFromAd(ad)
		if fields.ClusterID == nil || fields.ProcID == nil {
			return
		}
		id := jobwatch.JobID{Cluster: *fields.ClusterID, Proc: *fields.ProcID,
			Schedd: scheddOf(ad)}
		if obs == jobwatch.AsTerminal {
			endedNow[id] = true
		} else {
			seen[id] = true
		}

		var payload *JobPayload
		for _, s := range states {
			if !selects(s.hook, &fields, id) {
				continue
			}
			s.inScope[id] = true
			if obs == jobwatch.AsTerminal {
				s.ended[id] = true
			}
			for _, ev := range s.hook.Events {
				if !jobwatch.Satisfies(ev, ad, obs) {
					continue
				}
				if payload == nil {
					// Rendered once per ad and shared by every hook that
					// wants it: the ad is not kept past this call, and
					// rendering per hook would read the same attributes
					// again for the same answer.
					payload = jobPayload(ad, &fields, id)
				}
				s.fire = append(s.fire, pending{event: ev, job: id, payload: payload})
			}
		}
	}

	truncated, err := e.src.Queue(ctx, owner, EvalAttrs, jobwatch.QueueLimit, func(ad *classad.ClassAd) {
		obs := jobwatch.InQueue
		if jobwatch.TerminalAd(ad) {
			// A terminal ad still in the queue is as authoritative as a
			// history row. HTCondor -- notably OSPool access points --
			// leaves completed jobs in job_queue.log with JobStatus 4
			// rather than archiving them at once, so a hook that only
			// resolved terminal events from the history read would never
			// fire for such a job although the queue plainly shows it done.
			obs = jobwatch.AsTerminal
		}
		fold(ad, obs)
	})
	if err != nil {
		return st, fmt.Errorf("reading %s's queue: %w", owner, err)
	}

	// History resolves the outcome of jobs that have already gone. Its
	// failure is not fatal: without it the terminal events still fire from
	// the queue and from absence, which is the same degradation jobwatch
	// accepts when the mirror is down.
	since := now.Add(-e.HistoryWindow)
	if err := e.src.History(ctx, owner, EvalAttrs, since, jobwatch.HistoryLimit,
		func(ad *classad.ClassAd) { fold(ad, jobwatch.AsTerminal) }); err != nil {
		e.logf("reading finished jobs for webhooks failed; terminal events will come from the queue and from absence",
			"owner", owner, "error", err)
	}

	for _, s := range states {
		// Absence. A job this hook has seen before, missing from a COMPLETE
		// queue read and not explained by a terminal record, has left the
		// queue -- which is the only evidence there is for a job whose
		// history row never arrives. A truncated read proves nothing: the
		// job may simply be in the part that was not returned.
		if !truncated {
			for id := range s.inScope {
				if seen[id] || endedNow[id] || s.ended[id] {
					continue
				}
				s.ended[id] = true
				// Only "done" can be concluded from absence. Whether it
				// succeeded or failed is in the ad that is no longer there,
				// and inventing an answer is worse than not having one --
				// a wrongly reported failure is acted on.
				if slices.Contains(s.hook.Events, jobwatch.EventDone) {
					s.fire = append(s.fire, pending{event: jobwatch.EventDone, job: id})
				}
			}
		}

		got, err := e.commit(ctx, s, now)
		st.Enqueued += got.Enqueued
		st.Expired += got.Expired
		st.Capped += got.Capped
		if err != nil {
			return st, err
		}
	}
	return st, nil
}

// commit enqueues what a hook's pass decided, applies the fan-out cap, saves
// the tracked set and ends the hook if its subject has.
func (e *Evaluator) commit(ctx context.Context, s *hookState, now time.Time) (EvalStats, error) {
	var st EvalStats
	h := s.hook

	if len(s.fire) > e.FanoutCap {
		// One rolled-up delivery saying what happened, then disabled. The
		// alternative to saying something is deliveries that never arrive,
		// which look exactly like events that never happened.
		st.Capped++
		if err := e.store.NoteCapped(ctx, h.ID, len(s.fire)); err != nil {
			return st, err
		}
		n, err := e.enqueueRollup(ctx, h, s.fire, now)
		if err != nil {
			return st, err
		}
		st.Enqueued += n
		why := fmt.Sprintf("disabled: %d jobs matched at once, over the limit of %d. "+
			"Narrow this webhook to one job or one batch, or point it at a service that expects a summary.",
			len(s.fire), e.FanoutCap)
		if err := e.store.Disable(ctx, h.ID, why); err != nil {
			return st, err
		}
		return st, nil
	}

	for _, p := range s.fire {
		key := DeliveryKey(h.ID, p.event, p.job)
		payload, err := RenderPayload(h, p.event, p.payload, key, now, false)
		if err != nil {
			return st, fmt.Errorf("rendering a delivery for %s: %w", h.ID, err)
		}
		job := p.job
		fresh, err := e.store.Enqueue(ctx, h.ID, h.Owner, p.event, &job, payload, key)
		if err != nil {
			return st, err
		}
		if fresh {
			st.Enqueued++
		}
	}

	tracked := make([]jobwatch.JobID, 0, len(s.inScope))
	for id := range s.inScope {
		tracked = append(tracked, id)
	}
	sort.Slice(tracked, func(i, j int) bool {
		if tracked[i].Cluster != tracked[j].Cluster {
			return tracked[i].Cluster < tracked[j].Cluster
		}
		if tracked[i].Proc != tracked[j].Proc {
			return tracked[i].Proc < tracked[j].Proc
		}
		return tracked[i].Schedd < tracked[j].Schedd
	})
	if err := e.store.SaveTracked(ctx, h.ID, tracked); err != nil {
		return st, err
	}

	// A job or batch hook ends when its subject can no longer produce an
	// event: every job it has ever seen has finished. Not from any archive
	// or retention clock -- those measure when a record was written, which
	// for EnteredHistoryTime means two different things depending on how
	// the row arrived.
	//
	// A hook that has never seen a job is NOT finished. That is the normal
	// state of a hook created before the jobs were submitted, and expiring
	// it would be expiring the common case. The backstop deadline set at
	// creation covers the hook whose subject never appears.
	if h.Scope.Terminable() && len(s.inScope) > 0 && len(s.ended) == len(s.inScope) {
		// Not immediately: a succeeded/failed hook is still waiting for the
		// history row that says which, and that row lands seconds after the
		// job goes in a healthy pool. The same grace jobwatch gives the
		// same question.
		if err := e.store.Expire(ctx, h.ID, now.Add(jobwatch.UnresolvedOutcomeGrace)); err != nil {
			return st, err
		}
		st.Expired++
	}
	return st, nil
}

// enqueueRollup sends one delivery for a hook that fanned out too far.
//
// Keyed per (hook, event) with no job, so it is sent once: the hook is
// disabled immediately afterwards, and a second rolled-up delivery for the
// same event would be the same news.
func (e *Evaluator) enqueueRollup(ctx context.Context, h *Hook, fire []pending, now time.Time) (int, error) {
	counts := map[jobwatch.Event]int{}
	for _, p := range fire {
		counts[p.event]++
	}
	var sent int
	for _, ev := range h.Events {
		n := counts[ev]
		if n == 0 {
			continue
		}
		key := h.ID + "." + string(ev) + ".capped"
		payload, err := RenderCappedPayload(h, ev, n, e.FanoutCap, key, now)
		if err != nil {
			return sent, err
		}
		fresh, err := e.store.Enqueue(ctx, h.ID, h.Owner, ev, nil, payload, key)
		if err != nil {
			return sent, err
		}
		if fresh {
			sent++
		}
	}
	return sent, nil
}

// selects reports whether a hook covers this job.
//
// The owner is not re-checked: the queue read is already one owner's, which
// is the property jobwatch.Source exists to guarantee -- a caller's selector
// never reaches a backend query, so there is no expression to escape the
// owner scoping.
func selects(h *Hook, f *batches.Fields, id jobwatch.JobID) bool {
	switch h.Scope {
	case ScopeOwner:
		return true
	case ScopeJob:
		return id.Cluster == h.Job.Cluster && id.Proc == h.Job.Proc &&
			strings.EqualFold(id.Schedd, h.Job.Schedd)
	case ScopeBatch:
		want, ok := h.batchKey()
		if !ok {
			return false
		}
		got, ok := batches.GroupKey(f)
		return ok && got == want
	}
	return false
}

// batchKey is the grouping key of the batch this hook watches.
//
// Built by asking the batches package rather than by assembling the key here:
// what makes two jobs the same batch is that package's rule (a DAG by its
// name, any other name by owner and name, else by cluster), and a second
// spelling of it would be the one that drifts when the rule changes.
func (h *Hook) batchKey() (string, bool) {
	cluster := parseCluster(h.BatchID)
	f := batches.Fields{Owner: h.Owner, JobBatchName: h.BatchName}
	if cluster != nil {
		f.ClusterID = cluster
	} else {
		// GroupKey needs a cluster to answer at all, and for a named batch
		// or a DAG the value does not enter the key. Zero is a placeholder
		// for exactly that case.
		zero := int64(0)
		f.ClusterID = &zero
	}
	return batches.GroupKey(&f)
}

// jobPayload renders the part of a delivery that describes the job.
func jobPayload(ad *classad.ClassAd, f *batches.Fields, id jobwatch.JobID) *JobPayload {
	p := &JobPayload{
		Cluster: id.Cluster,
		Proc:    id.Proc,
		ID:      fmt.Sprintf("%d.%d", id.Cluster, id.Proc),
		Schedd:  id.Schedd,
		Owner:   f.Owner,
		Batch:   f.JobBatchName,
		Command: f.Cmd,
		Status:  string(f.Status()),
	}
	if v, ok := ad.EvaluateAttrNumber("ExitCode"); ok {
		code := int64(v)
		p.ExitCode = &code
	}
	if s, ok := ad.EvaluateAttrString("HoldReason"); ok {
		p.HoldReason = s
	}
	return p
}

// scheddOf reads the access point a job is on, lower-cased, as
// jobwatch.JobID records it. Empty on a server fronting one schedd, where
// every job is on it.
func scheddOf(ad *classad.ClassAd) string {
	for _, attr := range []string{"ScheddName", "GlobalJobId"} {
		s, ok := ad.EvaluateAttrString(attr)
		if !ok || s == "" {
			continue
		}
		if attr == "GlobalJobId" {
			// "<schedd>#<cluster>.<proc>#<timestamp>".
			if i := strings.Index(s, "#"); i > 0 {
				s = s[:i]
			} else {
				continue
			}
		}
		return strings.ToLower(s)
	}
	return ""
}

// parseCluster reads a batch id that is a cluster number, nil for one that is
// not.
func parseCluster(s string) *int64 {
	if s == "" {
		return nil
	}
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return nil
	}
	return &n
}

// evalOnce guards against two passes for one owner running at the same time,
// which the nudge makes possible: a sweep and a nudge can both decide to look
// at the same owner, and both would read the same jobs and race to enqueue
// and to save the tracked set.
type evalOnce struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (o *evalOnce) lock(owner string) *sync.Mutex {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.locks == nil {
		o.locks = map[string]*sync.Mutex{}
	}
	m, ok := o.locks[owner]
	if !ok {
		m = &sync.Mutex{}
		o.locks[owner] = m
	}
	return m
}
