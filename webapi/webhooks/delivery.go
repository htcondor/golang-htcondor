package webhooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/internal/publicdial"
	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
)

// Delivery states.
const (
	// StatePending is waiting for its first or next attempt.
	StatePending = "pending"
	// StateDelivered means the receiver answered 2xx.
	StateDelivered = "delivered"
	// StateAbandoned means we stopped trying. Deliberately distinct from
	// delivered: "we gave up" is a different thing to report than "it
	// arrived", and collapsing them is how a user comes to believe an
	// event reached somewhere it did not.
	StateAbandoned = "abandoned"
)

// Retry policy.
//
// A destination that fails is NOT given up on and NOT switched off. It backs
// off to a ceiling and keeps trying, because the failure this feature
// actually meets is a receiver that is down for a while and then comes back,
// and a hook that disabled itself comes back only if somebody happens to
// revisit a page they have no reason to look at. Silence that repairs itself
// beats silence that needs an administrator.
//
// What makes that affordable is that the backoff is per ENDPOINT and only
// its oldest pending delivery is attempted while it is failing (see
// Store.ClaimDue). A receiver that has been down for a week sees one request
// per ceiling interval, not one per queued event.
const (
	// BaseBackoff is the wait after the first failure.
	BaseBackoff = 30 * time.Second
	// MaxBackoff is the ceiling the growth reaches and stays at. Fifteen
	// minutes: slow enough to be no load on a receiver that is down,
	// quick enough that one that comes back is noticed while the person
	// who fixed it is still at the keyboard.
	MaxBackoff = 15 * time.Minute
	// JitterFraction spreads retries either side of the computed delay, so
	// several endpoints that failed together do not come back in the same
	// instant.
	JitterFraction = 0.2
	// DeliveryTimeout bounds one attempt.
	DeliveryTimeout = 15 * time.Second
	// MaxErrorBody is how much of a refusal's body is kept to show the
	// user. Enough for a provider's error message, not enough to turn the
	// deliveries table into a log of somebody else's HTML.
	MaxErrorBody = 512
	// MaxDeliveryAge is how long a delivery may stay pending before it is
	// abandoned unsent.
	//
	// The one bound that remains, and it is about the REPORT rather than
	// the receiver: a webhook says something happened, and past a point a
	// report is not late but wrong -- it drives automation on a state
	// that has moved on by days. It also keeps the queue finite for a
	// destination that is never coming back.
	MaxDeliveryAge = 14 * 24 * time.Hour
	// MaxPendingPerEndpoint bounds one destination's backlog.
	//
	// Without it an owner subscription on a busy account pointed at a
	// dead receiver accumulates a row per event for a fortnight. At the
	// ceiling the evaluator stops enqueuing for that endpoint and records
	// that it did, because events dropped for a full queue are events the
	// receiver will never hear about and that must not be discovered as
	// a gap.
	MaxPendingPerEndpoint = 1000
)

// Backoff is how long to wait before attempt n+1, given n failures so far.
//
// Exponential from BaseBackoff, capped, then jittered. The jitter is applied
// last and symmetrically, so the cap is a cap on the expectation rather than
// on the draw -- which matters only in that a retry may land slightly after
// MaxBackoff, and nothing depends on it not doing so.
func Backoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := BaseBackoff
	for i := 1; i < failures && d < MaxBackoff; i++ {
		d *= 2
	}
	if d > MaxBackoff {
		d = MaxBackoff
	}
	spread := float64(d) * JitterFraction
	// rand.Float64 is fine here: this is load spreading, not a secret.
	delta := (rand.Float64()*2 - 1) * spread //nolint:gosec // G404: jitter, not a credential
	return time.Duration(float64(d) + delta)
}

// JobPayload is what a delivery says about a job.
//
// A deliberately small, named projection rather than the ad. A job ad has
// hundreds of attributes, some of them large and some of them the user's
// own text; posting all of it to a third party would make every new
// attribute a new disclosure, decided by nobody.
type JobPayload struct {
	Cluster int64  `json:"cluster_id"`
	Proc    int64  `json:"proc_id"`
	ID      string `json:"id"`
	Schedd  string `json:"schedd,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Batch   string `json:"batch_name,omitempty"`
	Status  string `json:"status,omitempty"`
	// ExitCode is set for a job that ran to completion.
	ExitCode *int64 `json:"exit_code,omitempty"`
	// HoldReason is set for a held job, which is the state a hook exists
	// to tell somebody about.
	HoldReason string `json:"hold_reason,omitempty"`
	// Command is the executable, which is how a person recognises which
	// of their jobs this is.
	Command string `json:"command,omitempty"`
}

// Payload is the body of a delivery.
type Payload struct {
	// IdempotencyKey is stable across every retry of this delivery, and is
	// also sent as the Idempotency-Key header. A receiver that may act
	// twice on one event should deduplicate on it.
	IdempotencyKey string `json:"idempotency_key"`
	// Delivery is at least once. Said in the payload because a receiver
	// author reads the payload and may never read the documentation.
	DeliverySemantics string `json:"delivery_semantics"`

	// EndpointID and EndpointLabel name the DESTINATION, not the
	// subscription that selected this job. The JSON keys stay
	// "webhook_id"/"webhook_label" because that is what a receiver calls
	// the thing it is serving: one URL, one key, one webhook.
	EndpointID    string `json:"webhook_id"`
	EndpointLabel string `json:"webhook_label,omitempty"`

	Event      string    `json:"event"`
	OccurredAt time.Time `json:"occurred_at"`
	// Test marks a delivery sent by the "test this configuration" button
	// rather than by something happening. It travels the same path as a
	// real one, so the only way a receiver can tell is this flag.
	Test bool `json:"test,omitempty"`

	Job *JobPayload `json:"job,omitempty"`

	// JobAd is the job's whole ClassAd, present only on an endpoint set
	// to PayloadFull. It sits BESIDE Job rather than replacing it, so a
	// receiver written against the projection keeps working when
	// somebody turns this on.
	JobAd map[string]any `json:"job_ad,omitempty"`
	// JobAdOmitted says why a full-ad delivery has no JobAd -- the ad
	// was over MaxAdBytes, or it was not available when the event
	// resolved. Said rather than left blank: a receiver that branches on
	// the ad needs to tell "no ad" from "no such attribute", and a
	// silently missing object reads as the latter.
	JobAdOmitted string `json:"job_ad_omitted,omitempty"`

	// Capped is set on the one rolled-up delivery a hook sends when its
	// selection exceeded the fan-out ceiling. It is the only delivery with
	// no job: there were too many to name, which is what it says.
	Capped *CappedSummary `json:"capped,omitempty"`

	// Scope and Subject describe what the hook watches, so a receiver
	// serving several can route without holding its own table.
	Scope   string `json:"scope"`
	Subject string `json:"subject,omitempty"`
}

// CappedSummary tells a receiver that deliveries were withheld, and how many.
//
// Sent rather than silently dropping them: a delivery that never arrives
// looks exactly like an event that never happened, and the whole reason the
// ceiling exists is that the alternative was posting to the receiver tens of
// thousands of times.
type CappedSummary struct {
	Jobs  int    `json:"jobs"`
	Limit int    `json:"limit"`
	Note  string `json:"note"`
}

// RenderCappedPayload builds the rolled-up body for a subscription over the
// ceiling.
func RenderCappedPayload(ep *Endpoint, sub *Subscription, e jobwatch.Event, jobs, limit int, key string, at time.Time) ([]byte, error) {
	p := Payload{
		IdempotencyKey:    key,
		DeliverySemantics: "at-least-once",
		EndpointID:        ep.ID,
		EndpointLabel:     ep.Label,
		Event:             string(e),
		OccurredAt:        at.UTC(),
		Scope:             string(sub.Scope),
		Subject:           sub.subject(),
		Capped: &CappedSummary{
			Jobs:  jobs,
			Limit: limit,
			Note: fmt.Sprintf("%d jobs reached %q at once, over this server's per-subscription limit of %d. "+
				"No per-job deliveries were sent for them. Narrow the subscription to a job or a batch, "+
				"or point it at a receiver that expects a summary.",
				jobs, string(e), limit),
		},
	}
	return json.Marshal(p)
}

// Delivery is one enqueued POST.
type Delivery struct {
	ID         string
	EndpointID string
	Owner      string
	Event      jobwatch.Event
	Job        *jobwatch.JobID
	Payload    []byte

	State       string
	Attempts    int
	NextAttempt time.Time
	LastStatus  int
	LastError   string
}

// DeliveryKey is the idempotency key for a real event.
//
// Deterministic in (ENDPOINT, event, job), which does two things. It makes
// the deliveries table its own dedup ledger: the evaluator inserts with OR
// IGNORE, so a pass that sees the same job in the same state again cannot
// enqueue a second POST, however many passes run. And because the endpoint
// rather than the subscription is in the key, two subscriptions on one
// destination that both select a job collapse into ONE delivery -- a user
// watching both "my jobs" and "this job" gets told once. That is only sound
// because the payload shape is a property of the endpoint; if two
// subscriptions could ask for different bodies, the second would silently
// lose to the first.
func DeliveryKey(endpointID string, e jobwatch.Event, job jobwatch.JobID) string {
	schedd := job.Schedd
	if schedd == "" {
		schedd = "-"
	}
	return strings.Join([]string{endpointID, string(e), schedd,
		strconv.FormatInt(job.Cluster, 10), strconv.FormatInt(job.Proc, 10)}, ".")
}

// Enqueue records a delivery to be made, and reports whether it was new.
//
// A key that is already present is left alone and reported as not new: that
// is the dedup, and it is a primary-key conflict rather than a read followed
// by a write, so two passes racing cannot both win.
func (s *Store) Enqueue(ctx context.Context, endpointID, owner string, e jobwatch.Event, job *jobwatch.JobID, payload []byte, key string) (bool, error) {
	var cluster, proc, schedd any
	if job != nil {
		cluster, proc, schedd = job.Cluster, job.Proc, job.Schedd
	} else {
		schedd = ""
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO webhook_deliveries
			(id, endpoint_id, owner, event, job_cluster, job_proc, schedd,
			 payload_json, state, next_attempt_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key, endpointID, owner, string(e), cluster, proc, schedd,
		string(payload), StatePending, s.now().UTC(), s.now().UTC())
	if err != nil {
		return false, fmt.Errorf("enqueueing a delivery: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ClaimDue returns up to limit deliveries whose next attempt is due.
//
// Two gates, not one. A delivery must be due on its own clock, AND its
// endpoint must not be backing off. On top of that, while an endpoint is
// failing only its OLDEST pending delivery is returned: that one acts as the
// probe, and the rest of the backlog waits behind it. Without this, a
// receiver that has been down for a day comes back to its entire backlog
// arriving at the moment the backoff expires, which is the one thing a
// struggling receiver cannot take -- and a receiver that is still down gets
// hit once per queued event instead of once per interval.
//
// Claimed by pushing next_attempt_at forward before returning them, so two
// workers -- or a worker and its own next tick -- cannot pick up the same
// row. The push is the attempt timeout plus a margin: if the process dies
// mid-attempt the row becomes due again on its own rather than needing a
// lease to expire.
func (s *Store) ClaimDue(ctx context.Context, limit int) ([]*Delivery, error) {
	now := s.now().UTC()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, endpoint_id, owner, event, job_cluster, job_proc, schedd,
		       payload_json, attempts
		  FROM (
		        SELECT d.id, d.endpoint_id, d.owner, d.event, d.job_cluster,
		               d.job_proc, d.schedd, d.payload_json, d.attempts,
		               d.next_attempt_at,
		               e.consecutive_failures AS failures,
		               ROW_NUMBER() OVER (PARTITION BY d.endpoint_id
		                                  ORDER BY d.created_at) AS rn
		          FROM webhook_deliveries d
		          JOIN webhook_endpoints e ON e.id = d.endpoint_id
		         WHERE d.state = ?
		           AND d.next_attempt_at <= ?
		           AND (e.backoff_until IS NULL OR e.backoff_until <= ?)
		       )
		 WHERE rn = 1 OR failures = 0
		 ORDER BY next_attempt_at
		 LIMIT ?`, StatePending, now, now, limit)
	if err != nil {
		return nil, err
	}
	var out []*Delivery
	for rows.Next() {
		var (
			d             Delivery
			event         string
			cluster, proc sql.NullInt64
			schedd        string
		)
		var payload string
		if err := rows.Scan(&d.ID, &d.EndpointID, &d.Owner, &event, &cluster, &proc,
			&schedd, &payload, &d.Attempts); err != nil {
			_ = rows.Close()
			return nil, err
		}
		d.Event = jobwatch.Event(event)
		d.Payload = []byte(payload)
		if cluster.Valid {
			d.Job = &jobwatch.JobID{Cluster: cluster.Int64, Proc: proc.Int64, Schedd: schedd}
		}
		out = append(out, &d)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	for _, d := range out {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE webhook_deliveries SET next_attempt_at = ? WHERE id = ?`,
			now.Add(DeliveryTimeout+30*time.Second), d.ID); err != nil {
			return out, fmt.Errorf("claiming delivery %s: %w", d.ID, err)
		}
	}
	return out, nil
}

// Delivered records a success, and clears the endpoint's backoff.
//
// One success is enough to clear it completely rather than decaying it: the
// receiver answered, so whatever was wrong is over, and a destination that
// is flapping will set it again on the next failure.
func (s *Store) Delivered(ctx context.Context, d *Delivery, status int) error {
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		   SET state = ?, attempts = attempts + 1, delivered_at = ?,
		       last_status = ?, last_error = ''
		 WHERE id = ?`, StateDelivered, now, status, d.ID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhook_endpoints
		   SET last_delivery_at = ?, last_status = ?, last_error = '',
		       consecutive_failures = 0, backoff_until = NULL
		 WHERE id = ?`, now, status, d.EndpointID)
	return err
}

// backOff records a failure against an endpoint and returns when it may next
// be posted to.
//
// The counter and the timestamp are set together because they are one fact:
// how long this destination has been failing is the whole input to how long
// to wait. Nothing here can disable an endpoint -- there is no column for it
// any more.
func (s *Store) backOff(ctx context.Context, endpointID string, status int, cause string, at time.Time) (time.Time, error) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE webhook_endpoints
		   SET consecutive_failures = consecutive_failures + 1,
		       last_delivery_at = ?, last_status = ?, last_error = ?
		 WHERE id = ?`, at, status, truncate(cause), endpointID); err != nil {
		return time.Time{}, err
	}
	var failures int
	if err := s.db.QueryRowContext(ctx,
		`SELECT consecutive_failures FROM webhook_endpoints WHERE id = ?`,
		endpointID).Scan(&failures); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The endpoint went away under us. Nothing to back off.
			return at, nil
		}
		return time.Time{}, err
	}
	until := at.Add(Backoff(failures))
	if _, err := s.db.ExecContext(ctx,
		`UPDATE webhook_endpoints SET backoff_until = ? WHERE id = ?`,
		until, endpointID); err != nil {
		return time.Time{}, err
	}
	return until, nil
}

// Retry records a failed attempt that will be tried again.
//
// The delivery's next attempt is the ENDPOINT's backoff, not a schedule of
// its own. Per-delivery backoff was the old shape and it does not survive
// the split: a thousand queued deliveries each retrying on their own timer
// is a thousand requests an interval at a receiver that is already down.
func (s *Store) Retry(ctx context.Context, d *Delivery, status int, cause string) error {
	now := s.now().UTC()
	until, err := s.backOff(ctx, d.EndpointID, status, cause, now)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		   SET attempts = ?, next_attempt_at = ?, last_status = ?, last_error = ?
		 WHERE id = ?`, d.Attempts+1, until, status, truncate(cause), d.ID)
	return err
}

// Abandon gives up on ONE delivery. It never gives up on the endpoint.
//
// Two things reach here, and only two. A destination that REFUSED the
// request -- a 4xx that is not 408 or 429 -- which says the request is wrong
// rather than the receiver absent, and will be just as wrong next time. And
// a delivery that aged past MaxDeliveryAge without landing.
//
// countsAsFailure says whether the endpoint should back off as well. A
// refusal does: a URL answering 404 is a misconfigured destination, and
// without a backoff every queued delivery for it would be refused in one
// burst. A delivery that is merely too old does not: the endpoint's health
// is whatever its recent attempts say it is.
func (s *Store) Abandon(ctx context.Context, d *Delivery, status int, reason string, countsAsFailure bool) error {
	now := s.now().UTC()
	reason = truncate(reason)
	if _, err := s.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		   SET state = ?, attempts = attempts + 1, last_status = ?,
		       last_error = ?, abandoned_reason = ?
		 WHERE id = ?`, StateAbandoned, status, reason, reason, d.ID); err != nil {
		return err
	}
	if !countsAsFailure {
		return nil
	}
	_, err := s.backOff(ctx, d.EndpointID, status, reason, now)
	return err
}

// RecordTest stores the outcome of a test delivery, apart from the record of
// real traffic so neither overwrites the other.
func (s *Store) RecordTest(ctx context.Context, endpointID string, status int, cause string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhook_endpoints SET last_test_at = ?, last_test_status = ?, last_test_error = ?
		 WHERE id = ?`, s.now().UTC(), status, truncate(cause), endpointID)
	return err
}

// PruneDeliveries removes settled deliveries older than before, and reports
// how many went.
func (s *Store) PruneDeliveries(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM webhook_deliveries WHERE state != ? AND created_at < ?`,
		StatePending, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ExpireStale abandons deliveries that have been pending longer than
// MaxDeliveryAge, and reports how many.
//
// This is the only thing that ends a pending delivery for a destination that
// never answers, now that nothing is disabled and no attempt budget runs
// out. It is also what clears deliveries orphaned by an endpoint that was
// deleted mid-flight, which the claim query cannot see at all.
func (s *Store) ExpireStale(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		   SET state = ?, abandoned_reason = ?
		 WHERE state = ? AND created_at < ?`,
		StateAbandoned,
		fmt.Sprintf("never delivered within %s of the event; a report this late describes a state that has moved on",
			MaxDeliveryAge.Round(time.Hour)),
		StatePending, now.Add(-MaxDeliveryAge))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PendingCount is how many deliveries are queued for an endpoint.
//
// The evaluator asks before enqueuing, so that a destination which has been
// down for days does not accumulate without bound. See
// MaxPendingPerEndpoint.
func (s *Store) PendingCount(ctx context.Context, endpointID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM webhook_deliveries WHERE endpoint_id = ? AND state = ?`,
		endpointID, StatePending).Scan(&n)
	return n, err
}

// RecordBacklog notes that an endpoint's queue hit the ceiling.
func (s *Store) RecordBacklog(ctx context.Context, endpointID string, depth int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhook_endpoints SET backlog_at = ?, backlog_depth = ? WHERE id = ?`,
		s.now().UTC(), depth, endpointID)
	return err
}

// RecentDeliveries returns the latest deliveries for one of an owner's hooks,
// newest first, for the UI's "is this working" panel.
func (s *Store) RecentDeliveries(ctx context.Context, owner, endpointID string, limit int) ([]*Delivery, error) {
	if _, err := s.GetEndpoint(ctx, owner, endpointID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, endpoint_id, owner, event, job_cluster, job_proc, schedd,
		       state, attempts, last_status, last_error
		  FROM webhook_deliveries
		 WHERE endpoint_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`, endpointID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Delivery
	for rows.Next() {
		var (
			d             Delivery
			event         string
			cluster, proc sql.NullInt64
			schedd        string
			status        sql.NullInt64
		)
		if err := rows.Scan(&d.ID, &d.EndpointID, &d.Owner, &event, &cluster, &proc, &schedd,
			&d.State, &d.Attempts, &status, &d.LastError); err != nil {
			return nil, err
		}
		d.Event = jobwatch.Event(event)
		d.LastStatus = int(status.Int64)
		if cluster.Valid {
			d.Job = &jobwatch.JobID{Cluster: cluster.Int64, Proc: proc.Int64, Schedd: schedd}
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// Attempt is the outcome of one POST.
type Attempt struct {
	Status int
	// Cause is empty on success, and otherwise what to show the user.
	Cause string
	// Retryable says whether trying again could plausibly work. A
	// permanent refusal is not retried at all: without this distinction a
	// misconfigured URL is retried until the attempt budget runs out, on
	// every event, forever -- which is how a delivery loop becomes a
	// livelock against somebody else's 404 handler.
	Retryable bool
}

// OK reports a delivered attempt.
func (a Attempt) OK() bool { return a.Status >= 200 && a.Status < 300 }

// Sender posts deliveries.
//
// The client refuses non-public addresses at dial time and follows no
// redirects (see internal/publicdial): the destination is a URL a user typed,
// so a request to it is a request to make this daemon fetch something, and a
// daemon inside a cluster can reach a great deal its caller cannot.
type Sender struct {
	client *http.Client
	// UserAgent identifies this server to a receiver. Deliberately
	// generic: it says what is calling, not who is being served.
	UserAgent string
}

// NewSender returns a Sender with the delivery timeout and dial policy.
func NewSender() *Sender {
	return &Sender{
		client:    publicdial.Client(DeliveryTimeout),
		UserAgent: "htcondor-api-webhooks/1",
	}
}

// Post makes one attempt.
func (s *Sender) Post(ctx context.Context, destination string, secrets [][]byte, key string, payload []byte, event jobwatch.Event, endpointID string) Attempt {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination, bytes.NewReader(payload))
	if err != nil {
		// A URL that will not build a request will not build one next
		// time either.
		return Attempt{Cause: fmt.Sprintf("the destination is not usable: %v", err), Retryable: false}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.UserAgent)
	req.Header.Set(IdempotencyHeader, key)
	req.Header.Set(EventHeader, string(event))
	req.Header.Set(EndpointHeader, endpointID)
	req.Header.Set(SignatureHeader, SignWith(secrets, key, payload, time.Now()))

	resp, err := s.client.Do(req)
	if err != nil {
		// Transport failures are retryable as a class: a refused
		// connection, a timeout, a TLS handshake that failed are all
		// things that are routinely different a minute later. The one
		// exception is the dial guard, which is a decision about the
		// destination and will be the same decision every time.
		var blocked *publicdial.ErrBlocked
		if errors.As(err, &blocked) {
			return Attempt{Cause: blocked.Error(), Retryable: false}
		}
		return Attempt{Cause: fmt.Sprintf("could not reach the destination: %v", err), Retryable: true}
	}
	defer func() { _ = resp.Body.Close() }()
	// Read a bounded amount so a refusal can be explained, and drain
	// nothing further: the response is not interesting beyond its status.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, MaxErrorBody))

	a := Attempt{Status: resp.StatusCode}
	if a.OK() {
		return a
	}
	a.Cause = describeRefusal(resp, body)
	a.Retryable = retryableStatus(resp.StatusCode)
	return a
}

// retryableStatus classifies a response.
//
//   - 408 and 429 are the two the receiver is explicitly asking us to try
//     again; 429 in particular is a rate limit, and giving up on it would
//     discard an event because the receiver was busy.
//   - 5xx is the receiver being broken, which is usually temporary.
//   - every other 4xx is a statement about the request, and the request does
//     not change between attempts. Retrying a 404 or a 401 cannot succeed; it
//     only delays the moment the user is told.
//   - 3xx reaches here only because redirects are not followed, which makes
//     it a misconfigured destination rather than a transient fault.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return code >= 500 && code <= 599
}

// describeRefusal renders a non-2xx response for a human.
func describeRefusal(resp *http.Response, body []byte) string {
	msg := strings.TrimSpace(string(body))
	// One line: a provider's HTML error page is not worth storing, and a
	// multi-line value in a table cell is worth less.
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fmt.Sprintf("the destination redirected (%s); webhook destinations must be final URLs",
			resp.Status)
	}
	if msg == "" {
		return fmt.Sprintf("the destination answered %s", resp.Status)
	}
	return fmt.Sprintf("the destination answered %s: %s", resp.Status, msg)
}

// MaxAdBytes caps a full-ad payload's ad.
//
// A job ad is usually a few kilobytes, but nothing stops a submitter putting
// a large string in a custom attribute, and a delivery body is stored,
// signed, and retried. Past this the ad is left out with a reason rather
// than truncated: half an ad is not a smaller ad, it is a malformed one.
const MaxAdBytes = 128 * 1024

// RenderPayload builds the body for a delivery.
//
// ad may be nil, and is used only when the endpoint asks for PayloadFull.
func RenderPayload(ep *Endpoint, sub *Subscription, e jobwatch.Event, job *JobPayload, ad map[string]any, key string, at time.Time, test bool) ([]byte, error) {
	p := Payload{
		IdempotencyKey:    key,
		DeliverySemantics: "at-least-once",
		EndpointID:        ep.ID,
		EndpointLabel:     ep.Label,
		Event:             string(e),
		OccurredAt:        at.UTC(),
		Test:              test,
		Job:               job,
		Scope:             string(sub.Scope),
		Subject:           sub.subject(),
	}
	if ep.Payload == PayloadFull {
		switch n := approxAdBytes(ad); {
		case ad == nil:
			p.JobAdOmitted = "the job ad was not available when this event resolved; " +
				"a job that left the queue is described by its history record, which is not the ad"
		case n > MaxAdBytes:
			p.JobAdOmitted = fmt.Sprintf(
				"the job ad is about %d bytes, over this server's %d-byte limit for a delivery",
				n, MaxAdBytes)
		default:
			p.JobAd = ad
		}
	}
	return json.Marshal(p)
}

// approxAdBytes is how large an ad will be once encoded.
//
// Measured by encoding it, because the alternative is guessing from
// attribute count and the attribute that blows the limit is always the one
// long string.
func approxAdBytes(ad map[string]any) int {
	if ad == nil {
		return 0
	}
	b, err := json.Marshal(ad)
	if err != nil {
		// Unencodable is over any limit that matters: it cannot go in
		// the body at all.
		return MaxAdBytes + 1
	}
	return len(b)
}

// subject renders what the subscription watches, for the payload.
func (s *Subscription) subject() string {
	switch s.Scope {
	case ScopeJob:
		return fmt.Sprintf("%d.%d", s.Job.Cluster, s.Job.Proc)
	case ScopeBatch:
		if s.BatchName != "" {
			return s.BatchName
		}
		return s.BatchID
	default:
		return s.Owner
	}
}

// truncate shortens a string to MaxErrorBody for storage, marking that it was
// cut.
//
// The bound is the constant rather than a parameter: every caller stores into
// one of the error columns, which are all sized the same, and a parameter
// nothing varies is a bound in two places.
func truncate(s string) string {
	if len(s) <= MaxErrorBody {
		return s
	}
	return s[:MaxErrorBody-1] + "…"
}
