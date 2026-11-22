// Package webhooks delivers an outside service a POST when something
// happens to a user's jobs.
//
// It is the outbound half of an integration whose inbound half is an OAuth2
// client: the client lets a service act as the user HERE, a webhook tells the
// service what happened here. Both are the user's own to create and to
// revoke.
//
// Three decisions shape everything below.
//
// The ASSOCIATION LIVES IN THIS DATABASE and never on the job. A job ad is
// public -- anyone who may read the queue reads every attribute -- and for
// Slack, Zapier, Teams and most of the rest, possession of the destination
// URL is the authorization to post to it. An attribute on the ad would hand
// that to every user of the access point.
//
// The EVENT VOCABULARY IS JOBWATCH'S, not a parallel one. It is a closed set
// for a reason that applies here identically: a finished job is DESTROYED
// from the queue by the schedd, so a hook written as "JobStatus == 4" would
// never fire, and the failure is invisible -- indistinguishable from work
// still running. Sharing the vocabulary makes that trap unsayable in both
// places, and means the evaluator can reuse the machinery that resolves
// terminal events across the queue/history boundary.
//
// DELIVERY IS AT LEAST ONCE, and the receiver is told so. Every delivery
// carries an idempotency key that is stable across retries, and that key is
// also the primary key of the row that records the delivery -- so the ledger
// that prevents a second enqueue and the queue the worker claims from are the
// same table, rather than two things to keep consistent.
package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
)

// Scope says what a hook watches.
type Scope string

const (
	// ScopeJob is one job. It ends when that job does.
	ScopeJob Scope = "job"
	// ScopeBatch is a batch as the jobs page means one: a DAG and its
	// nodes, a named batch across however many clusters, or a lone
	// cluster. It ends when the whole batch has.
	ScopeBatch Scope = "batch"
	// ScopeOwner is every job of the owner, now and later. It has no
	// natural end, so it carries a TTL and is renewed by being edited.
	ScopeOwner Scope = "owner"
)

// Valid reports whether s is a scope this server implements.
func (s Scope) Valid() bool {
	switch s {
	case ScopeJob, ScopeBatch, ScopeOwner:
		return true
	}
	return false
}

// Terminable reports whether a scope can be finished with -- whether there is
// a point after which it cannot produce another event. A job and a batch both
// end; an owner does not.
func (s Scope) Terminable() bool { return s == ScopeJob || s == ScopeBatch }

// SupportedEvents is what a hook may subscribe to.
//
// jobwatch's vocabulary minus "custom". A custom event is a ClassAd
// expression evaluated against the ad, which is a fine thing for a caller
// that is waiting on an answer and a poor one for a standing subscription: it
// has no terminal resolution, so it only ever fires for jobs still in the
// queue, and a hook whose expression quietly matches nothing is
// indistinguishable from a quiet pool. Left out until there is a reason
// better than symmetry.
var SupportedEvents = []jobwatch.Event{
	jobwatch.EventDone,
	jobwatch.EventSucceeded,
	jobwatch.EventFailed,
	jobwatch.EventHeld,
	jobwatch.EventRunning,
}

// EventSupported reports whether a hook may subscribe to e.
func EventSupported(e jobwatch.Event) bool { return slices.Contains(SupportedEvents, e) }

// MaxEventsPerHook bounds one hook's subscription. There are only as many
// events as SupportedEvents holds, so this only rejects a list padded with
// duplicates -- which Validate catches anyway, one name at a time.
var MaxEventsPerHook = len(SupportedEvents)

const (
	// MaxLabelLen bounds the user's name for a hook.
	MaxLabelLen = 128
	// MaxURLLen bounds a destination. Capability URLs are long; this is
	// well past the longest any provider issues and short enough that the
	// column cannot be used as a data store.
	MaxURLLen = 2048
	// SecretBytes is the entropy in a hook's signing key.
	SecretBytes = 32
)

// Hook is one subscription.
//
// URL and Secret are present only in memory and only where they are needed:
// a Hook read for a listing has neither, and there is no field combination
// that produces them by accident. See Store.Destination.
type Hook struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Label string `json:"label"`

	Scope Scope `json:"scope"`
	// Job is the subject for ScopeJob.
	Job jobwatch.JobID `json:"job,omitempty"`
	// BatchID and BatchName are the subject for ScopeBatch.
	//
	// Both, because a batch is derived from job ads rather than stored: the
	// id is what a UI links to, and the name is what still selects the jobs
	// once the id's cluster has left the queue.
	BatchID   string `json:"batch_id,omitempty"`
	BatchName string `json:"batch_name,omitempty"`

	Events []jobwatch.Event `json:"events"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	DisabledAt     *time.Time `json:"disabled_at,omitempty"`
	DisabledReason string     `json:"disabled_reason,omitempty"`
	// ConsecutiveFailures counts deliveries given up on in a row. A
	// success resets it.
	ConsecutiveFailures int `json:"consecutive_failures"`

	// CappedAt is when this hook's selection last exceeded the fan-out
	// ceiling, and CappedJobs how many jobs were in scope then. Surfaced
	// because the deliveries that did not happen would otherwise look like
	// events that did not happen.
	CappedAt   *time.Time `json:"capped_at,omitempty"`
	CappedJobs int        `json:"capped_jobs,omitempty"`

	LastDeliveryAt *time.Time `json:"last_delivery_at,omitempty"`
	LastStatus     int        `json:"last_status,omitempty"`
	LastError      string     `json:"last_error,omitempty"`

	LastTestAt     *time.Time `json:"last_test_at,omitempty"`
	LastTestStatus int        `json:"last_test_status,omitempty"`
	LastTestError  string     `json:"last_test_error,omitempty"`

	// HasDestination says a destination is stored, without being one --
	// the same shape connectors.Stored uses for a client secret, and for
	// the same reason: a struct with the value in it ends up in a JSON
	// response the first time somebody adds an endpoint.
	HasDestination bool `json:"has_destination"`
	// DestinationHint is the destination with everything identifying
	// removed but its host, so a user can tell two hooks apart without the
	// page handing the capability back. See HintFor.
	DestinationHint string `json:"destination_hint,omitempty"`
}

// Live reports whether a hook should still be evaluated.
func (h *Hook) Live(now time.Time) bool {
	if h.DisabledAt != nil {
		return false
	}
	if h.ExpiresAt != nil && !h.ExpiresAt.After(now) {
		return false
	}
	return true
}

// HintFor renders a destination for display: scheme, host, and how long the
// rest was.
//
// Not a prefix of the path, which is the obvious thing and the wrong one. For
// a Slack or Zapier hook the path IS the secret, so showing even its first
// characters narrows a guess; and the host alone is what somebody needs to
// tell "my Slack hook" from "my Zapier hook", which is the only question this
// display has to answer.
func HintFor(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	rest := len(raw) - len(u.Scheme) - len("://") - len(u.Host)
	if rest < 0 {
		rest = 0
	}
	return fmt.Sprintf("%s://%s/… (%d more characters)", u.Scheme, u.Host, rest)
}

// ValidateURL checks a destination.
//
// https only, and no credentials in the URL. http is refused even on
// loopback: a webhook posts a job's details, including attributes the owner
// may consider private, and there is no deployment where sending that in
// clear is what somebody meant. Whether the address is reachable at all is
// decided later and elsewhere, at dial time, because a name that resolves
// publicly now can resolve to loopback a moment later (see internal/publicdial).
func ValidateURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("a destination URL is required")
	}
	if len(raw) > MaxURLLen {
		return fmt.Errorf("the destination URL must be at most %d characters", MaxURLLen)
	}
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("the destination URL has surrounding whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("the destination URL does not parse: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("the destination URL must use https")
	}
	if u.Host == "" {
		return fmt.Errorf("the destination URL has no host")
	}
	if u.User != nil {
		// A password in a URL is logged by everything it passes through,
		// and this one would be stored and replayed on every retry.
		return fmt.Errorf("the destination URL must not contain credentials")
	}
	return nil
}

// Validate checks a hook as the user described it, before any secret is
// minted or anything is stored.
func (h *Hook) Validate() error {
	if h.Owner == "" {
		return fmt.Errorf("a hook must have an owner")
	}
	if strings.TrimSpace(h.Label) == "" {
		return fmt.Errorf("a name is required, so you can tell your webhooks apart")
	}
	if len(h.Label) > MaxLabelLen {
		return fmt.Errorf("the name must be at most %d characters", MaxLabelLen)
	}
	if !h.Scope.Valid() {
		return fmt.Errorf("unknown scope %q", h.Scope)
	}
	switch h.Scope {
	case ScopeJob:
		if h.Job.Cluster <= 0 {
			return fmt.Errorf("a job hook needs a cluster id")
		}
		if h.Job.Proc < 0 {
			return fmt.Errorf("a job hook needs a proc id")
		}
	case ScopeBatch:
		if h.BatchID == "" && h.BatchName == "" {
			return fmt.Errorf("a batch hook needs a batch")
		}
	case ScopeOwner:
		if h.Job.Cluster != 0 || h.BatchID != "" || h.BatchName != "" {
			return fmt.Errorf("an owner hook names no job or batch")
		}
	}
	if len(h.Events) == 0 {
		return fmt.Errorf("at least one event is required")
	}
	if len(h.Events) > MaxEventsPerHook {
		return fmt.Errorf("at most %d events", MaxEventsPerHook)
	}
	seen := make(map[jobwatch.Event]bool, len(h.Events))
	for _, e := range h.Events {
		if !EventSupported(e) {
			return fmt.Errorf("unsupported event %q (%s)", e, EventNames())
		}
		if seen[e] {
			return fmt.Errorf("duplicate event %q", e)
		}
		seen[e] = true
	}
	return nil
}

// EventNames lists the supported events, for an error message.
func EventNames() string {
	names := make([]string, 0, len(SupportedEvents))
	for _, e := range SupportedEvents {
		names = append(names, string(e))
	}
	return strings.Join(names, ", ")
}

// NewSecret mints a hook's signing key.
func NewSecret() ([]byte, error) {
	b := make([]byte, SecretBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating a signing secret: %w", err)
	}
	return b, nil
}

// newID mints a hook or delivery id.
func newID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating an id: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

// The signature scheme.
//
// A receiver cannot tell a delivery this server sent from one anybody else
// posted to the same URL, because the URL is all it takes to post there. The
// signature is what makes the difference checkable, and it covers the
// TIMESTAMP and the KEY as well as the body -- a signature over the body
// alone is replayable forever, and replaying "your job failed" is not
// harmless when it drives somebody's automation.
const (
	// SignatureHeader carries the signature.
	SignatureHeader = "X-HTCondor-Signature"
	// IdempotencyHeader carries the key a receiver deduplicates on.
	IdempotencyHeader = "Idempotency-Key"
	// EventHeader names the event, so a receiver can route without
	// parsing the body.
	EventHeader = "X-HTCondor-Event"
	// HookHeader names the hook, for a receiver serving several.
	HookHeader = "X-HTCondor-Webhook"
	// SignatureTolerance is how old a timestamp a receiver should accept.
	// Documented here because the receiver is the one that enforces it,
	// and a scheme whose tolerance is unstated gets implemented as
	// "ignore the timestamp".
	SignatureTolerance = 5 * time.Minute
)

// Sign returns the SignatureHeader value for a delivery.
//
// The signed string is "<unix>.<key>.<body>" and the header is
// "t=<unix>,v1=<hex>", which is the shape most receivers already have code
// for. v1 names the scheme so a second one can be added without a receiver
// having to guess which it is looking at.
func Sign(secret []byte, key string, body []byte, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write([]byte(key))
	mac.Write([]byte("."))
	mac.Write(body)
	return fmt.Sprintf("t=%s,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

// VerifySignature checks a header this package produced. It exists so the
// scheme has one implementation that is exercised by tests rather than only
// described in documentation a receiver author may read differently.
func VerifySignature(secret []byte, key string, body []byte, header string, now time.Time) error {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch name {
		case "t":
			ts = value
		case "v1":
			sig = value
		}
	}
	if ts == "" || sig == "" {
		return fmt.Errorf("the signature header is missing t= or v1=")
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("the signature timestamp does not parse: %w", err)
	}
	at := time.Unix(secs, 0)
	if d := now.Sub(at); d > SignatureTolerance || d < -SignatureTolerance {
		return fmt.Errorf("the signature timestamp is %s away from now", d.Round(time.Second))
	}
	want := Sign(secret, key, body, at)
	// Constant time, over the whole header value: comparing the hex with
	// == leaks where the first difference is, which is enough to forge one
	// byte at a time.
	if !hmac.Equal([]byte(want), []byte(header)) {
		return fmt.Errorf("the signature does not match")
	}
	return nil
}
