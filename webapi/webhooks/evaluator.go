package webhooks

import (
	"context"
	"encoding/json"
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
	Subscriptions int
	Owners        int
	Enqueued      int
	Expired       int
	Capped        int
	// Backlogged counts subscriptions whose events were dropped because
	// their destination's queue was already at MaxPendingPerEndpoint.
	Backlogged int
	Errors     int
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
		} else if st.Enqueued > 0 || st.Errors > 0 || st.Capped > 0 || st.Backlogged > 0 {
			e.logf("webhook events found", "subscriptions", st.Subscriptions,
				"enqueued", st.Enqueued, "expired", st.Expired, "capped", st.Capped,
				"backlogged", st.Backlogged, "errors", st.Errors)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Pass reads every live subscription, groups by owner, and evaluates each
// owner once.
//
// One owner's failure does not abandon the pass. Subscriptions are
// independent, and the likeliest cause of a failure is a per-owner
// authorization problem rather than a systemic one -- which is no reason to
// stop answering everybody else's.
func (e *Evaluator) Pass(ctx context.Context) (EvalStats, error) {
	var st EvalStats
	live, err := e.store.LiveSubscriptions(ctx)
	if err != nil {
		return st, fmt.Errorf("listing live webhook subscriptions: %w", err)
	}
	st.Subscriptions = len(live)
	if len(live) == 0 {
		return st, nil
	}

	byOwner := make(map[string][]*Subscription, 4)
	for _, sub := range live {
		byOwner[sub.Owner] = append(byOwner[sub.Owner], sub)
	}
	st.Owners = len(byOwner)

	for owner, subs := range byOwner {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		got, err := e.passOwner(ctx, owner, subs)
		st.Enqueued += got.Enqueued
		st.Expired += got.Expired
		st.Capped += got.Capped
		st.Backlogged += got.Backlogged
		if err != nil {
			st.Errors++
			e.logf("evaluating webhooks failed for one owner", "owner", owner, "error", err)
		}
	}
	return st, nil
}

// CheckOwner evaluates one owner's subscriptions now, for the change-stream
// nudge.
func (e *Evaluator) CheckOwner(ctx context.Context, owner string) (int, error) {
	subs, err := e.store.LiveSubscriptions(ctx)
	if err != nil {
		return 0, err
	}
	var mine []*Subscription
	for _, sub := range subs {
		if sub.Owner == owner {
			mine = append(mine, sub)
		}
	}
	if len(mine) == 0 {
		return 0, nil
	}
	st, err := e.passOwner(ctx, owner, mine)
	return st.Enqueued, err
}

// subState is what one pass learns about one subscription.
type subState struct {
	sub *Subscription
	// endpoint is where this subscription posts. Carried here because
	// the delivery's identity, its payload shape and its backlog ceiling
	// are all the endpoint's, and looking it up per firing would be the
	// same read many times.
	endpoint *Endpoint
	// inScope is every job this subscription selects, seen this pass or
	// remembered from a previous one.
	inScope map[jobwatch.JobID]bool
	// ended is the subset known to have finished: a terminal ad, a
	// history row, or absence from a complete read.
	ended map[jobwatch.JobID]bool
	// fire is the deliveries this pass decided on. Whether it went over
	// the fan-out cap is len(fire) against it, decided in commit -- there
	// is no flag, because a flag would be a second answer to a question
	// the slice already answers.
	fire []pending
}

// pending is one delivery to enqueue.
type pending struct {
	event jobwatch.Event
	job   jobwatch.JobID
	// payload is the job as the delivery will describe it. Rendered while
	// the ad is in hand, because the ad is not kept.
	payload *JobPayload
	// ad is the whole job ad, present only when some endpoint in this
	// pass asked for one. Rendered from the same ad at the same moment,
	// for the same reason.
	ad map[string]any
}

// passOwner evaluates one owner's subscriptions against one read of their
// jobs.
func (e *Evaluator) passOwner(ctx context.Context, owner string, subs []*Subscription) (EvalStats, error) {
	// One pass per owner at a time. A sweep and a change-stream nudge can
	// both decide to look at the same owner, and two passes would read the
	// same jobs and race to save the tracked set -- the loser's write
	// forgetting what the winner learned.
	lock := e.owners.lock(owner)
	lock.Lock()
	defer lock.Unlock()

	var st EvalStats
	now := e.now().UTC()

	states, wantFullAd := e.resolve(ctx, subs)
	if len(states) == 0 {
		return st, nil
	}

	// What to read. EvalAttrs is a projection of the twenty-odd
	// attributes the evaluator itself needs; a nil projection means EVERY
	// attribute, on both the mirror and the schedd. Asking for everything
	// is strictly more expensive, so it is done only when one of this
	// owner's endpoints is actually set to send whole ads -- the cost
	// follows the feature rather than falling on everybody.
	attrs := EvalAttrs
	if wantFullAd {
		attrs = nil
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
		var adMap map[string]any
		adRendered := false
		for _, s := range states {
			if !selects(s.sub, &fields, id) {
				continue
			}
			s.inScope[id] = true
			if obs == jobwatch.AsTerminal {
				s.ended[id] = true
			}
			for _, ev := range s.sub.Events {
				if !jobwatch.Satisfies(ev, ad, obs) {
					continue
				}
				if payload == nil {
					// Rendered once per ad and shared by every
					// subscription that wants it: the ad is not kept past
					// this call, and rendering per subscription would read
					// the same attributes again for the same answer.
					payload = jobPayload(ad, &fields, id)
				}
				if s.endpoint.Payload == PayloadFull && !adRendered {
					// Likewise once, and only if somebody asked. The
					// flag rather than a nil check because an ad that
					// does not render is a legitimate nil we should not
					// retry per subscription.
					adMap = renderAd(ad)
					adRendered = true
				}
				p := pending{event: ev, job: id, payload: payload}
				if s.endpoint.Payload == PayloadFull {
					p.ad = adMap
				}
				s.fire = append(s.fire, p)
			}
		}
	}

	truncated, err := e.src.Queue(ctx, owner, attrs, jobwatch.QueueLimit, func(ad *classad.ClassAd) {
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
	if err := e.src.History(ctx, owner, attrs, since, jobwatch.HistoryLimit,
		func(ad *classad.ClassAd) { fold(ad, jobwatch.AsTerminal) }); err != nil {
		e.logf("reading finished jobs for webhooks failed; terminal events will come from the queue and from absence",
			"owner", owner, "error", err)
	}

	for _, s := range states {
		// Absence. A job this subscription has seen before, missing from a COMPLETE
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
				if slices.Contains(s.sub.Events, jobwatch.EventDone) {
					s.fire = append(s.fire, pending{event: jobwatch.EventDone, job: id})
				}
			}
		}

		got, err := e.commit(ctx, s, now)
		st.Enqueued += got.Enqueued
		st.Expired += got.Expired
		st.Capped += got.Capped
		st.Backlogged += got.Backlogged
		if err != nil {
			return st, err
		}
	}
	return st, nil
}

// resolve pairs each subscription with its destination, and reports whether
// any of them wants whole job ads.
//
// The endpoints are cached across the owner's subscriptions because several
// of them usually point at the same receiver, and the endpoint decides both
// the payload shape and -- through that -- which attributes this pass has to
// read.
func (e *Evaluator) resolve(ctx context.Context, subs []*Subscription) (states []*subState, wantFullAd bool) {
	endpoints := make(map[string]*Endpoint, len(subs))
	states = make([]*subState, 0, len(subs))
	for _, sub := range subs {
		ep, ok := endpoints[sub.EndpointID]
		if !ok {
			var err error
			ep, err = e.store.endpointByID(ctx, sub.EndpointID)
			if err != nil {
				// A subscription whose endpoint has gone is a row the
				// delete should have taken. Skip it rather than failing
				// the owner's whole pass for one dangling reference.
				e.logf("skipping a webhook subscription whose destination is missing",
					"subscription", sub.ID, "endpoint", sub.EndpointID, "error", err)
				continue
			}
			endpoints[sub.EndpointID] = ep
		}
		if ep.Payload == PayloadFull {
			wantFullAd = true
		}
		s := &subState{
			sub:      sub,
			endpoint: ep,
			inScope:  make(map[jobwatch.JobID]bool, len(sub.Tracked)+8),
			ended:    make(map[jobwatch.JobID]bool, 8),
		}
		// The remembered set is in scope before anything arrives: a
		// cluster that has fully drained would otherwise look like an
		// empty selection, and absence would have nothing to be the
		// absence of.
		for _, id := range sub.Tracked {
			s.inScope[id] = true
		}
		states = append(states, s)
	}
	return states, wantFullAd
}

// commit enqueues what a subscription's pass decided, applies the fan-out
// cap and the endpoint's backlog ceiling, saves the tracked set, and ends the
// subscription if its subject has.
func (e *Evaluator) commit(ctx context.Context, s *subState, now time.Time) (EvalStats, error) {
	var st EvalStats
	sub := s.sub
	ep := s.endpoint

	if len(s.fire) > e.FanoutCap {
		// One rolled-up delivery saying what happened, and the per-job
		// ones withheld. The subscription is NOT switched off: nothing
		// here is any more, and leaving it live means a selection that
		// drops back under the ceiling resumes on its own. The rollup
		// key is deduplicated per day, so a selection that stays over
		// the cap says so once a day rather than every pass.
		st.Capped++
		if err := e.store.NoteCapped(ctx, sub.ID, len(s.fire)); err != nil {
			return st, err
		}
		n, err := e.enqueueRollup(ctx, ep, sub, s.fire, now)
		if err != nil {
			return st, err
		}
		st.Enqueued += n
		return st, e.saveProgress(ctx, s, now)
	}

	if len(s.fire) > 0 {
		// The endpoint's backlog, checked once for this subscription
		// rather than per delivery. A destination that has been down for
		// days would otherwise accumulate a row per event without bound,
		// and the rows are not free: each carries its rendered body.
		pending, err := e.store.PendingCount(ctx, ep.ID)
		if err != nil {
			return st, err
		}
		if pending >= MaxPendingPerEndpoint {
			st.Backlogged++
			if err := e.store.RecordBacklog(ctx, ep.ID, pending); err != nil {
				return st, err
			}
			e.logf("webhook destination has too much queued; events are being dropped",
				"endpoint", ep.ID, "pending", pending, "limit", MaxPendingPerEndpoint)
			return st, e.saveProgress(ctx, s, now)
		}
	}

	for _, p := range s.fire {
		// Keyed on the ENDPOINT, so two subscriptions of this owner that
		// both select this job and event produce one POST rather than
		// two. The second Enqueue is a primary-key conflict and reports
		// itself as not fresh.
		key := DeliveryKey(ep.ID, p.event, p.job)
		payload, err := RenderPayload(ep, sub, p.event, p.payload, p.ad, key, now, false)
		if err != nil {
			return st, fmt.Errorf("rendering a delivery for %s: %w", sub.ID, err)
		}
		job := p.job
		fresh, err := e.store.Enqueue(ctx, ep.ID, sub.Owner, p.event, &job, payload, key)
		if err != nil {
			return st, err
		}
		if fresh {
			st.Enqueued++
		}
	}

	if err := e.saveProgress(ctx, s, now); err != nil {
		return st, err
	}

	// A job or batch subscription ends when its subject can no longer
	// produce an event: every job it has ever seen has finished. Not from
	// any archive or retention clock -- those measure when a record was
	// written, which for EnteredHistoryTime means two different things
	// depending on how the row arrived.
	//
	// One that has never seen a job is NOT finished. That is the normal
	// state of a subscription created before the jobs were submitted, and
	// expiring it would be expiring the common case. The backstop
	// deadline set at creation covers the one whose subject never
	// appears.
	if sub.Scope.Terminable() && len(s.inScope) > 0 && len(s.ended) == len(s.inScope) {
		// Not immediately: a succeeded/failed subscription is still
		// waiting for the history row that says which, and that row lands
		// seconds after the job goes in a healthy pool. The same grace
		// jobwatch gives the same question.
		if err := e.store.Expire(ctx, sub.ID, now.Add(jobwatch.UnresolvedOutcomeGrace)); err != nil {
			return st, err
		}
		st.Expired++
	}
	return st, nil
}

// saveProgress stores what this subscription now knows about which jobs it
// has seen.
//
// Separated out because every exit from commit has to do it, including the
// ones that enqueued nothing: a pass that withheld deliveries still SAW
// those jobs, and forgetting that would make every one of them look new next
// pass -- which, once the backlog cleared, would deliver the lot.
func (e *Evaluator) saveProgress(ctx context.Context, s *subState, _ time.Time) error {
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
	return e.store.SaveTracked(ctx, s.sub.ID, tracked)
}

// enqueueRollup sends one delivery for a subscription that fanned out too
// far.
//
// Keyed per (endpoint, subscription, event, DAY) with no job. The day is in
// the key because nothing disables a capped subscription any more: without
// it, a selection that sits over the ceiling would say so once and then
// never again, and a fresh episode months later would be silent.
func (e *Evaluator) enqueueRollup(ctx context.Context, ep *Endpoint, sub *Subscription, fire []pending, now time.Time) (int, error) {
	counts := map[jobwatch.Event]int{}
	for _, p := range fire {
		counts[p.event]++
	}
	var sent int
	for _, ev := range sub.Events {
		n := counts[ev]
		if n == 0 {
			continue
		}
		key := ep.ID + "." + sub.ID + "." + string(ev) + ".capped." + now.Format("2006-01-02")
		payload, err := RenderCappedPayload(ep, sub, ev, n, e.FanoutCap, key, now)
		if err != nil {
			return sent, err
		}
		fresh, err := e.store.Enqueue(ctx, ep.ID, sub.Owner, ev, nil, payload, key)
		if err != nil {
			return sent, err
		}
		if fresh {
			sent++
		}
	}
	return sent, nil
}

// selects reports whether a subscription covers this job.
//
// The owner is not re-checked: the queue read is already one owner's, which
// is the property jobwatch.Source exists to guarantee -- a caller's selector
// never reaches a backend query, so there is no expression to escape the
// owner scoping.
func selects(sub *Subscription, f *batches.Fields, id jobwatch.JobID) bool {
	switch sub.Scope {
	case ScopeOwner:
		return true
	case ScopeJob:
		return id.Cluster == sub.Job.Cluster && id.Proc == sub.Job.Proc &&
			strings.EqualFold(id.Schedd, sub.Job.Schedd)
	case ScopeBatch:
		want, ok := sub.batchKey()
		if !ok {
			return false
		}
		got, ok := batches.GroupKey(f)
		return ok && got == want
	}
	return false
}

// batchKey is the grouping key of the batch this subscription watches.
//
// Built by asking the batches package rather than by assembling the key here:
// what makes two jobs the same batch is that package's rule (a DAG by its
// name, any other name by owner and name, else by cluster), and a second
// spelling of it would be the one that drifts when the rule changes.
func (sub *Subscription) batchKey() (string, bool) {
	cluster := parseCluster(sub.BatchID)
	f := batches.Fields{Owner: sub.Owner, JobBatchName: sub.BatchName}
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

// renderAd turns a job ad into the object a full-payload delivery carries.
//
// Through the ad's own JSON marshalling, so the attribute names and value
// shapes are exactly what GET /api/v1/jobs/{id} returns -- a receiver that
// already reads this server's job JSON sees one encoding, not two. Case is
// left as the ad stored it, which for HTCondor is the conventional
// ClusterId/JobStatus spelling a receiver will be looking for.
//
// A nil result means the ad did not render; RenderPayload says so in the
// body rather than leaving the field quietly absent.
func renderAd(ad *classad.ClassAd) map[string]any {
	if ad == nil {
		return nil
	}
	b, err := json.Marshal(ad)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
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
