package webhooks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
)

// Dispatcher sends the deliveries the evaluator enqueued.
//
// It is a claim-and-send loop over the deliveries table rather than a queue
// in memory, so a restart loses nothing: a delivery that was in flight when
// the process died becomes due again on its own, and an at-least-once
// contract is exactly what makes that safe to do.
type Dispatcher struct {
	store  *Store
	sender *Sender
	logf   func(string, ...any)

	// Interval is how often the loop looks for due deliveries. It bounds
	// the latency of a first attempt, so it is short; the work it finds is
	// usually none.
	Interval time.Duration
	// Batch is how many deliveries one pass claims.
	Batch int
	// Concurrency is how many DESTINATIONS are posted to at once.
	//
	// Per destination rather than per delivery, and one delivery at a time
	// within a destination. A receiver that takes fifteen seconds to
	// answer would otherwise occupy the whole pool with one hook's
	// backlog, and a hook's own events would arrive out of order.
	Concurrency int
}

// Dispatcher defaults. The interval is the first-attempt latency, and a few
// seconds of it is imperceptible next to the evaluator's own period.
const (
	DefaultDispatchInterval = 5 * time.Second
	DefaultDispatchBatch    = 64
	DefaultConcurrency      = 4
)

// NewDispatcher returns a dispatcher with the default pacing.
func NewDispatcher(store *Store, sender *Sender, logf func(string, ...any)) *Dispatcher {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Dispatcher{
		store:       store,
		sender:      sender,
		logf:        logf,
		Interval:    DefaultDispatchInterval,
		Batch:       DefaultDispatchBatch,
		Concurrency: DefaultConcurrency,
	}
}

// Stats is what one pass did.
type Stats struct {
	Claimed   int
	Delivered int
	Retrying  int
	Abandoned int
	Disabled  int
}

// Run sends due deliveries until ctx is done.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.Interval)
	defer ticker.Stop()
	for {
		// Send first: a process that has just started may be holding
		// deliveries that came due during the restart.
		if st, err := d.Pass(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			d.logf("webhook delivery pass failed", "error", err)
		} else if st.Delivered > 0 || st.Abandoned > 0 || st.Disabled > 0 {
			d.logf("webhook deliveries sent",
				"delivered", st.Delivered, "retrying", st.Retrying,
				"abandoned", st.Abandoned, "disabled", st.Disabled)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Pass claims the due deliveries and sends them.
func (d *Dispatcher) Pass(ctx context.Context) (Stats, error) {
	var st Stats
	due, err := d.store.ClaimDue(ctx, d.Batch)
	if err != nil {
		return st, fmt.Errorf("claiming due deliveries: %w", err)
	}
	st.Claimed = len(due)
	if len(due) == 0 {
		return st, nil
	}

	// Grouped by hook, so one destination is posted to serially.
	byHook := make(map[string][]*Delivery, len(due))
	order := make([]string, 0, len(due))
	for _, dl := range due {
		if _, seen := byHook[dl.HookID]; !seen {
			order = append(order, dl.HookID)
		}
		byHook[dl.HookID] = append(byHook[dl.HookID], dl)
	}

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		sem  = make(chan struct{}, d.Concurrency)
		stat = &st
	)
	for _, hookID := range order {
		deliveries := byHook[hookID]
		wg.Add(1)
		go func(hookID string, deliveries []*Delivery) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			// One unseal per hook rather than per delivery: it is the
			// same destination and the same key for all of them.
			destination, secret, err := d.store.Destination(ctx, hookID)
			if err != nil {
				// A hook deleted between the claim and now, or one this
				// server cannot unseal. Neither is the delivery's fault
				// and neither is fixed by trying again soon.
				for _, dl := range deliveries {
					d.finishUnsendable(ctx, dl, err, stat, &mu)
				}
				return
			}
			for _, dl := range deliveries {
				if ctx.Err() != nil {
					return
				}
				d.send(ctx, dl, destination, secret, stat, &mu)
			}
		}(hookID, deliveries)
	}
	wg.Wait()
	return st, nil
}

// finishUnsendable deals with a delivery whose hook cannot be read.
func (d *Dispatcher) finishUnsendable(ctx context.Context, dl *Delivery, cause error, st *Stats, mu *sync.Mutex) {
	if errors.Is(cause, ErrNotFound) {
		// The hook is gone; so should its pending deliveries be. Deleting
		// the hook already removes them, so this is the race, not the
		// normal path.
		if _, err := d.store.Abandon(ctx, dl, 0, "the webhook was deleted before this could be delivered"); err != nil {
			d.logf("could not abandon a delivery for a deleted webhook", "delivery", dl.ID, "error", err)
		}
		mu.Lock()
		st.Abandoned++
		mu.Unlock()
		return
	}
	disabled, err := d.store.Abandon(ctx, dl, 0, cause.Error())
	if err != nil {
		d.logf("could not abandon an undeliverable delivery", "delivery", dl.ID, "error", err)
	}
	mu.Lock()
	st.Abandoned++
	if disabled {
		st.Disabled++
	}
	mu.Unlock()
}

// send makes one attempt and records what it means.
func (d *Dispatcher) send(ctx context.Context, dl *Delivery, destination string, secret []byte, st *Stats, mu *sync.Mutex) {
	a := d.sender.Post(ctx, destination, secret, dl.ID, dl.Payload, dl.Event, dl.HookID)

	mu.Lock()
	defer mu.Unlock()
	switch {
	case a.OK():
		if err := d.store.Delivered(ctx, dl, a.Status); err != nil {
			d.logf("could not record a delivered webhook", "delivery", dl.ID, "error", err)
		}
		st.Delivered++
	case !a.Retryable:
		// A permanent refusal is the user's to fix, so stop now and say
		// why. Spending the attempt budget on it would only delay the
		// moment they are told.
		disabled, err := d.store.Abandon(ctx, dl, a.Status, a.Cause)
		if err != nil {
			d.logf("could not abandon a refused delivery", "delivery", dl.ID, "error", err)
		}
		st.Abandoned++
		if disabled {
			st.Disabled++
		}
	case dl.Attempts+1 >= MaxAttempts:
		disabled, err := d.store.Abandon(ctx, dl, a.Status,
			fmt.Sprintf("gave up after %d attempts; the last said: %s", MaxAttempts, a.Cause))
		if err != nil {
			d.logf("could not abandon an exhausted delivery", "delivery", dl.ID, "error", err)
		}
		st.Abandoned++
		if disabled {
			st.Disabled++
		}
	default:
		if err := d.store.Retry(ctx, dl, a.Status, a.Cause); err != nil {
			d.logf("could not reschedule a delivery", "delivery", dl.ID, "error", err)
		}
		st.Retrying++
	}
}

// SendTest posts a test delivery for a hook and records the outcome.
//
// It goes through the real sender, the real signing and the real dial policy,
// and differs from a genuine delivery in exactly two ways: the payload is
// marked test, and it is not retried -- somebody is watching the result, and
// an answer in fifteen seconds is the point.
//
// Anything that bypassed the delivery path would test the wrong code. The
// failures worth catching here are a destination that refuses the signature,
// a URL that resolves somewhere this daemon will not dial, and a receiver
// that answers 404 -- none of which a simulated send would notice.
func SendTest(ctx context.Context, store *Store, sender *Sender, h *Hook) (Attempt, error) {
	destination, secret, err := store.Destination(ctx, h.ID)
	if err != nil {
		return Attempt{}, err
	}
	key, err := newID(h.ID + ".test.")
	if err != nil {
		return Attempt{}, err
	}
	// A test names an event because a receiver routes on one. The first
	// the hook subscribes to is the honest choice: it is what this hook
	// will actually send.
	event := jobwatch.EventDone
	if len(h.Events) > 0 {
		event = h.Events[0]
	}
	payload, err := RenderPayload(h, event, testJob(h), key, time.Now(), true)
	if err != nil {
		return Attempt{}, err
	}
	a := sender.Post(ctx, destination, secret, key, payload, event, h.ID)
	cause := a.Cause
	if a.OK() {
		cause = ""
	}
	if err := store.RecordTest(ctx, h.ID, a.Status, cause); err != nil {
		return a, fmt.Errorf("the test was sent but its result could not be recorded: %w", err)
	}
	return a, nil
}

// testJob is the job a test delivery describes.
//
// Filled in rather than omitted: a receiver's handler almost always reads the
// job, and a test whose payload is missing the field the handler needs tests
// that the handler can be reached and not that it works. The ids are the
// hook's own subject where it has one, so the shape matches what will arrive.
func testJob(h *Hook) *JobPayload {
	j := &JobPayload{
		Cluster: h.Job.Cluster,
		Proc:    h.Job.Proc,
		Schedd:  h.Job.Schedd,
		Owner:   h.Owner,
		Status:  "Completed",
		Command: "/bin/true",
		Batch:   h.BatchName,
	}
	if j.Cluster == 0 {
		// An owner or batch hook has no one job, so the numbers are
		// plainly not real ones.
		j.Cluster, j.Proc = 0, 0
	}
	j.ID = fmt.Sprintf("%d.%d", j.Cluster, j.Proc)
	zero := int64(0)
	j.ExitCode = &zero
	return j
}
