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
// Bounded in hours rather than days. A webhook reports that something
// happened, and a report that arrives a day late is usually worse than one
// that never arrives -- it drives automation on a state that has moved on.
// The budget below is roughly six hours, which covers a receiver's deploy or
// a short outage and not much more.
const (
	// MaxAttempts is how many times one delivery is tried before it is
	// abandoned.
	MaxAttempts = 10
	// BaseBackoff is the wait after the first failure.
	BaseBackoff = 30 * time.Second
	// MaxBackoff caps the growth.
	MaxBackoff = time.Hour
	// JitterFraction spreads retries either side of the computed delay, so
	// a receiver coming back up is not hit by every pending delivery in
	// the same instant.
	JitterFraction = 0.2
	// DeliveryTimeout bounds one attempt.
	DeliveryTimeout = 15 * time.Second
	// MaxErrorBody is how much of a refusal's body is kept to show the
	// user. Enough for a provider's error message, not enough to turn the
	// deliveries table into a log of somebody else's HTML.
	MaxErrorBody = 512
	// MaxConsecutiveFailures is how many abandoned deliveries in a row
	// disable a hook. A hook is disabled rather than left failing
	// silently: silence is the one outcome somebody reads as "nothing has
	// happened yet".
	MaxConsecutiveFailures = 5
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

	WebhookID    string `json:"webhook_id"`
	WebhookLabel string `json:"webhook_label,omitempty"`

	Event      string    `json:"event"`
	OccurredAt time.Time `json:"occurred_at"`
	// Test marks a delivery sent by the "test this configuration" button
	// rather than by something happening. It travels the same path as a
	// real one, so the only way a receiver can tell is this flag.
	Test bool `json:"test,omitempty"`

	Job *JobPayload `json:"job,omitempty"`

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

// RenderCappedPayload builds the rolled-up body for a hook over the ceiling.
func RenderCappedPayload(h *Hook, e jobwatch.Event, jobs, limit int, key string, at time.Time) ([]byte, error) {
	p := Payload{
		IdempotencyKey:    key,
		DeliverySemantics: "at-least-once",
		WebhookID:         h.ID,
		WebhookLabel:      h.Label,
		Event:             string(e),
		OccurredAt:        at.UTC(),
		Scope:             string(h.Scope),
		Subject:           h.subject(),
		Capped: &CappedSummary{
			Jobs:  jobs,
			Limit: limit,
			Note: fmt.Sprintf("%d jobs reached %q at once, over this server's per-webhook limit of %d. "+
				"No per-job deliveries were sent for them and the webhook has been disabled.",
				jobs, string(e), limit),
		},
	}
	return json.Marshal(p)
}

// Delivery is one enqueued POST.
type Delivery struct {
	ID      string
	HookID  string
	Owner   string
	Event   jobwatch.Event
	Job     *jobwatch.JobID
	Payload []byte

	State       string
	Attempts    int
	NextAttempt time.Time
	LastStatus  int
	LastError   string
}

// DeliveryKey is the idempotency key for a real event.
//
// Deterministic in (hook, event, job), which is what makes the deliveries
// table its own dedup ledger: the evaluator inserts with OR IGNORE, so a
// pass that sees the same job in the same state again cannot enqueue a second
// POST, however many passes run.
func DeliveryKey(hookID string, e jobwatch.Event, job jobwatch.JobID) string {
	schedd := job.Schedd
	if schedd == "" {
		schedd = "-"
	}
	return strings.Join([]string{hookID, string(e), schedd,
		strconv.FormatInt(job.Cluster, 10), strconv.FormatInt(job.Proc, 10)}, ".")
}

// Enqueue records a delivery to be made, and reports whether it was new.
//
// A key that is already present is left alone and reported as not new: that
// is the dedup, and it is a primary-key conflict rather than a read followed
// by a write, so two passes racing cannot both win.
func (s *Store) Enqueue(ctx context.Context, hookID, owner string, e jobwatch.Event, job *jobwatch.JobID, payload []byte, key string) (bool, error) {
	var cluster, proc, schedd any
	if job != nil {
		cluster, proc, schedd = job.Cluster, job.Proc, job.Schedd
	} else {
		schedd = ""
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO webhook_deliveries
			(id, webhook_id, owner, event, job_cluster, job_proc, schedd,
			 payload_json, state, next_attempt_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key, hookID, owner, string(e), cluster, proc, schedd,
		string(payload), StatePending, s.now().UTC(), s.now().UTC())
	if err != nil {
		return false, fmt.Errorf("enqueueing a delivery: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ClaimDue returns up to limit deliveries whose next attempt is due.
//
// Claimed by pushing next_attempt_at forward before returning them, so two
// workers -- or a worker and its own next tick -- cannot pick up the same
// row. The push is the attempt timeout plus a margin: if the process dies
// mid-attempt the row becomes due again on its own rather than needing a
// lease to expire.
func (s *Store) ClaimDue(ctx context.Context, limit int) ([]*Delivery, error) {
	now := s.now().UTC()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, webhook_id, owner, event, job_cluster, job_proc, schedd,
		       payload_json, attempts
		  FROM webhook_deliveries
		 WHERE state = ? AND next_attempt_at <= ?
		 ORDER BY next_attempt_at
		 LIMIT ?`, StatePending, now, limit)
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
		if err := rows.Scan(&d.ID, &d.HookID, &d.Owner, &event, &cluster, &proc,
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

// Delivered records a success and clears the hook's failure count.
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
		UPDATE webhooks
		   SET last_delivery_at = ?, last_status = ?, last_error = '',
		       consecutive_failures = 0
		 WHERE id = ?`, now, status, d.HookID)
	return err
}

// Retry records a failed attempt that will be tried again.
func (s *Store) Retry(ctx context.Context, d *Delivery, status int, cause string) error {
	attempts := d.Attempts + 1
	next := s.now().UTC().Add(Backoff(attempts))
	if _, err := s.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		   SET attempts = ?, next_attempt_at = ?, last_status = ?, last_error = ?
		 WHERE id = ?`, attempts, next, status, truncate(cause, MaxErrorBody), d.ID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhooks SET last_delivery_at = ?, last_status = ?, last_error = ? WHERE id = ?`,
		s.now().UTC(), status, truncate(cause, MaxErrorBody), d.HookID)
	return err
}

// Abandon gives up on a delivery, and disables the hook once enough
// deliveries in a row have been given up on.
//
// Returns whether the hook was disabled, so the caller can say so once rather
// than every pass.
func (s *Store) Abandon(ctx context.Context, d *Delivery, status int, reason string) (disabled bool, err error) {
	now := s.now().UTC()
	reason = truncate(reason, MaxErrorBody)
	if _, err := s.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		   SET state = ?, attempts = attempts + 1, last_status = ?,
		       last_error = ?, abandoned_reason = ?
		 WHERE id = ?`, StateAbandoned, status, reason, reason, d.ID); err != nil {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE webhooks
		   SET last_delivery_at = ?, last_status = ?, last_error = ?,
		       consecutive_failures = consecutive_failures + 1
		 WHERE id = ?`, now, status, reason, d.HookID); err != nil {
		return false, err
	}

	var failures int
	if err := s.db.QueryRowContext(ctx,
		`SELECT consecutive_failures FROM webhooks WHERE id = ?`, d.HookID).Scan(&failures); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if failures < MaxConsecutiveFailures {
		return false, nil
	}
	why := fmt.Sprintf("disabled after %d deliveries in a row were given up on; the last said: %s",
		failures, reason)
	if err := s.Disable(ctx, d.HookID, why); err != nil {
		return false, err
	}
	return true, nil
}

// RecordTest stores the outcome of a test delivery, apart from the record of
// real traffic so neither overwrites the other.
func (s *Store) RecordTest(ctx context.Context, hookID string, status int, cause string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhooks SET last_test_at = ?, last_test_status = ?, last_test_error = ?
		 WHERE id = ?`, s.now().UTC(), status, truncate(cause, MaxErrorBody), hookID)
	return err
}

// PruneDeliveries removes finished deliveries older than before, and reports
// how many went.
//
// Pending rows are never pruned by age: a pending row is either due or
// backing off, and both are states the worker is responsible for ending.
func (s *Store) PruneDeliveries(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM webhook_deliveries WHERE state != ? AND created_at < ?`,
		StatePending, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecentDeliveries returns the latest deliveries for one of an owner's hooks,
// newest first, for the UI's "is this working" panel.
func (s *Store) RecentDeliveries(ctx context.Context, owner, hookID string, limit int) ([]*Delivery, error) {
	if _, err := s.Get(ctx, owner, hookID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, webhook_id, owner, event, job_cluster, job_proc, schedd,
		       state, attempts, last_status, last_error
		  FROM webhook_deliveries
		 WHERE webhook_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`, hookID, limit)
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
		if err := rows.Scan(&d.ID, &d.HookID, &d.Owner, &event, &cluster, &proc, &schedd,
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
func (s *Sender) Post(ctx context.Context, destination string, secret []byte, key string, payload []byte, event jobwatch.Event, hookID string) Attempt {
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
	req.Header.Set(HookHeader, hookID)
	req.Header.Set(SignatureHeader, Sign(secret, key, payload, time.Now()))

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

// RenderPayload builds the body for a delivery.
func RenderPayload(h *Hook, e jobwatch.Event, job *JobPayload, key string, at time.Time, test bool) ([]byte, error) {
	p := Payload{
		IdempotencyKey:    key,
		DeliverySemantics: "at-least-once",
		WebhookID:         h.ID,
		WebhookLabel:      h.Label,
		Event:             string(e),
		OccurredAt:        at.UTC(),
		Test:              test,
		Job:               job,
		Scope:             string(h.Scope),
		Subject:           h.subject(),
	}
	return json.Marshal(p)
}

// subject renders what the hook watches, for the payload.
func (h *Hook) subject() string {
	switch h.Scope {
	case ScopeJob:
		return fmt.Sprintf("%d.%d", h.Job.Cluster, h.Job.Proc)
	case ScopeBatch:
		if h.BatchName != "" {
			return h.BatchName
		}
		return h.BatchID
	default:
		return h.Owner
	}
}

// truncate shortens a string for storage, marking that it was cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
