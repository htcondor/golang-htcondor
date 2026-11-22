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
		} else if st.Delivered > 0 || st.Abandoned > 0 {
			d.logf("webhook deliveries sent",
				"delivered", st.Delivered, "retrying", st.Retrying,
				"abandoned", st.Abandoned)
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

	// Grouped by endpoint, so one destination is posted to serially.
	byEndpoint := make(map[string][]*Delivery, len(due))
	order := make([]string, 0, len(due))
	for _, dl := range due {
		if _, seen := byEndpoint[dl.EndpointID]; !seen {
			order = append(order, dl.EndpointID)
		}
		byEndpoint[dl.EndpointID] = append(byEndpoint[dl.EndpointID], dl)
	}

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		sem  = make(chan struct{}, d.Concurrency)
		stat = &st
	)
	for _, endpointID := range order {
		deliveries := byEndpoint[endpointID]
		wg.Add(1)
		go func(endpointID string, deliveries []*Delivery) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			// One unseal per endpoint rather than per delivery: it is
			// the same destination and the same key for all of them.
			destination, secrets, err := d.store.Destination(ctx, endpointID)
			if err != nil {
				// An endpoint deleted between the claim and now, or one
				// this server cannot unseal. Neither is the delivery's
				// fault and neither is fixed by trying again soon.
				for _, dl := range deliveries {
					d.finishUnsendable(ctx, dl, err, stat, &mu)
				}
				return
			}
			for _, dl := range deliveries {
				if ctx.Err() != nil {
					return
				}
				d.send(ctx, dl, destination, secrets, stat, &mu)
			}
		}(endpointID, deliveries)
	}
	wg.Wait()
	return st, nil
}

// finishUnsendable deals with a delivery whose endpoint cannot be read.
func (d *Dispatcher) finishUnsendable(ctx context.Context, dl *Delivery, cause error, st *Stats, mu *sync.Mutex) {
	reason := cause.Error()
	if errors.Is(cause, ErrNotFound) {
		// The endpoint is gone; so should its pending deliveries be.
		// Deleting the endpoint already removes them, so this is the
		// race, not the normal path.
		reason = "the destination was deleted before this could be delivered"
	}
	// Not counted against the endpoint either way: there is no endpoint
	// to count it against, or none this server can read, and neither is
	// a statement about whether the receiver is up.
	if err := d.store.Abandon(ctx, dl, 0, reason, false); err != nil {
		d.logf("could not abandon an undeliverable delivery", "delivery", dl.ID, "error", err)
	}
	mu.Lock()
	st.Abandoned++
	mu.Unlock()
}

// send makes one attempt and records what it means.
//
// Three outcomes, where there used to be four. A delivery is no longer
// abandoned for running out of attempts: a destination that keeps failing
// backs off (see Store.Retry) and keeps its place in the queue until it
// either lands or ages out of MaxDeliveryAge.
func (d *Dispatcher) send(ctx context.Context, dl *Delivery, destination string, secrets [][]byte, st *Stats, mu *sync.Mutex) {
	a := d.sender.Post(ctx, destination, secrets, dl.ID, dl.Payload, dl.Event, dl.EndpointID)

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
		// why; the request will be identical next time. The ENDPOINT
		// still backs off, because a URL answering 404 is a
		// misconfigured destination and without it every delivery
		// queued for one would be refused in a single burst.
		if err := d.store.Abandon(ctx, dl, a.Status, a.Cause, true); err != nil {
			d.logf("could not abandon a refused delivery", "delivery", dl.ID, "error", err)
		}
		st.Abandoned++
	default:
		if err := d.store.Retry(ctx, dl, a.Status, a.Cause); err != nil {
			d.logf("could not reschedule a delivery", "delivery", dl.ID, "error", err)
		}
		st.Retrying++
	}
}

// SendTest posts a test delivery to an endpoint and records the outcome.
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
//
// A test does NOT clear a backoff and does not count as a failure. It says
// what the receiver answered right now; whether the queue may flow again is
// decided by a real delivery.
func SendTest(ctx context.Context, store *Store, sender *Sender, ep *Endpoint) (Attempt, error) {
	destination, secrets, err := store.Destination(ctx, ep.ID)
	if err != nil {
		return Attempt{}, err
	}
	key, err := newID(ep.ID + ".test.")
	if err != nil {
		return Attempt{}, err
	}
	// A test is about the DESTINATION, so it describes no subscription:
	// an endpoint may have several, or none yet, and picking one of them
	// to speak for would make the test's shape depend on which.
	sub := &Subscription{Owner: ep.Owner, Scope: ScopeOwner}
	payload, err := RenderPayload(ep, sub, TestEvent, testJob(ep), testAd(ep), key, time.Now(), true)
	if err != nil {
		return Attempt{}, err
	}
	a := sender.Post(ctx, destination, secrets, key, payload, TestEvent, ep.ID)
	cause := a.Cause
	if a.OK() {
		cause = ""
	}
	if err := store.RecordTest(ctx, ep.ID, a.Status, cause); err != nil {
		return a, fmt.Errorf("the test was sent but its result could not be recorded: %w", err)
	}
	return a, nil
}

// TestEvent is the event a test delivery names.
//
// "running" rather than a terminal event, and deliberately not whichever
// event happened to be first in some list: a test should describe a job that
// could plausibly be in the state the rest of the payload shows. The old
// behaviour named the subscription's first event, which produced bodies
// saying "failed" next to an exit code of 0 -- an inconsistency a receiver
// author maps fields from and then cannot reproduce.
const TestEvent = jobwatch.EventRunning

// testJob is the job a test delivery describes.
//
// Filled in rather than omitted: a receiver's handler almost always reads the
// job, and a test whose payload is missing the field the handler needs tests
// that the handler can be reached and not that it works. Internally
// consistent with TestEvent -- a running job, with no exit code and no hold
// reason, because it has neither.
func testJob(ep *Endpoint) *JobPayload {
	return &JobPayload{
		Cluster: 0,
		Proc:    0,
		ID:      "0.0",
		Owner:   ep.Owner,
		Status:  "Running",
		Command: "/bin/sleep",
	}
}

// testAd is the ad a full-payload endpoint's test carries.
//
// Small and obviously synthetic, but a real shape: an endpoint set to send
// whole ads should see an ad in its test, or the test does not exercise the
// thing that was turned on.
func testAd(ep *Endpoint) map[string]any {
	if ep.Payload != PayloadFull {
		return nil
	}
	return map[string]any{
		"ClusterId":   0,
		"ProcId":      0,
		"Owner":       ep.Owner,
		"JobStatus":   2,
		"Cmd":         "/bin/sleep",
		"Arguments":   "30",
		"RequestCpus": 1,
	}
}
