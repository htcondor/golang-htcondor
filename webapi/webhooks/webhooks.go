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
// A DESTINATION AND A SUBSCRIPTION ARE SEPARATE THINGS. An Endpoint is
// where deliveries go and the key that proves they came from here; a
// Subscription is what is watched and which events. They were one row once,
// which meant watching a second job required registering the destination
// again -- pasting the capability URL a second time and copying a second
// signing secret, for the same receiver. A secret shown exactly once makes
// "set it up again" the most error-prone moment the feature has, so it is
// now asked for once per receiver rather than once per subscription. The
// split also puts delivery health where it belongs: a destination that is
// down is down for everything pointed at it.
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

// EventSupported reports whether a subscription may ask for e.
func EventSupported(e jobwatch.Event) bool { return slices.Contains(SupportedEvents, e) }

// MaxEventsPerSubscription bounds one subscription's event list. There are only as many
// events as SupportedEvents holds, so this only rejects a list padded with
// duplicates -- which Validate catches anyway, one name at a time.
var MaxEventsPerSubscription = len(SupportedEvents)

const (
	// MaxLabelLen bounds the user's name for an endpoint or a
	// subscription.
	MaxLabelLen = 128
	// MaxURLLen bounds a destination. Capability URLs are long; this is
	// well past the longest any provider issues and short enough that the
	// column cannot be used as a data store.
	MaxURLLen = 2048
	// SecretBytes is the entropy in an endpoint's signing key.
	SecretBytes = 32
)

// PayloadMode says how much of the job a delivery carries.
type PayloadMode string

const (
	// PayloadSummary is the small fixed projection: ids, owner, status,
	// exit code, hold reason, command. The default, and for most
	// receivers the whole of what they use.
	PayloadSummary PayloadMode = "summary"
	// PayloadFull adds the job's entire ClassAd beside that projection.
	//
	// Opt-in, and per endpoint, because it is a disclosure decision. A
	// job ad carries hundreds of attributes -- arguments, environment
	// names, file paths, whatever the submitter put in a custom
	// attribute -- and sending all of it to a third party should be
	// something somebody chose, not something that grew by one field
	// every time the schedd learned a new one.
	PayloadFull PayloadMode = "full"
)

// Valid reports whether m is a payload mode this server implements.
func (m PayloadMode) Valid() bool {
	return m == PayloadSummary || m == PayloadFull
}

// Endpoint is a destination: where deliveries go, and the key a receiver
// verifies them with. Registered once per receiver, and pointed at by any
// number of subscriptions.
//
// URL and Secret are not fields. They are present only in memory and only
// where they are needed: an Endpoint read for a listing has neither, and
// there is no field combination that produces them by accident. See
// Store.Destination.
type Endpoint struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Label string `json:"label"`

	// Payload is how much of the job every delivery to this endpoint
	// carries. On the endpoint rather than the subscription so that one
	// receiver always sees one shape -- a Zap maps its fields once --
	// and so two subscriptions that select the same job and event can
	// collapse into a single POST, which they could not do if their
	// bodies might differ.
	Payload PayloadMode `json:"payload"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// ConsecutiveFailures counts deliveries that failed in a row. Any
	// success resets it. It drives BackoffUntil and nothing else: an
	// endpoint is never disabled for failing.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// BackoffUntil is when this endpoint may next be posted to.
	//
	// A failing destination backs off; it is not switched off. The
	// common failure is a receiver that is down for a while and comes
	// back, and a disabled hook comes back only if somebody happens to
	// revisit a page they have no reason to look at. While an endpoint
	// is backing off, only its OLDEST pending delivery is attempted, so
	// a week-long outage costs the receiver one request per ceiling
	// interval rather than one per queued event.
	BackoffUntil *time.Time `json:"backoff_until,omitempty"`

	LastDeliveryAt *time.Time `json:"last_delivery_at,omitempty"`
	LastStatus     int        `json:"last_status,omitempty"`
	LastError      string     `json:"last_error,omitempty"`

	LastTestAt     *time.Time `json:"last_test_at,omitempty"`
	LastTestStatus int        `json:"last_test_status,omitempty"`
	LastTestError  string     `json:"last_test_error,omitempty"`

	// BacklogAt is when this endpoint's pending queue last hit
	// MaxPendingPerEndpoint, and BacklogDepth how deep it was. Surfaced
	// because events dropped for a full queue are events the receiver
	// will never hear about, and that is not something to discover from
	// a gap.
	BacklogAt    *time.Time `json:"backlog_at,omitempty"`
	BacklogDepth int        `json:"backlog_depth,omitempty"`

	// HasDestination says a destination is stored, without being one --
	// the same shape connectors.Stored uses for a client secret, and for
	// the same reason: a struct with the value in it ends up in a JSON
	// response the first time somebody adds an endpoint.
	HasDestination bool `json:"has_destination"`
	// DestinationHint is the destination with everything identifying
	// removed but its host, so a user can tell two endpoints apart
	// without the page handing the capability back. See HintFor.
	DestinationHint string `json:"destination_hint,omitempty"`
}

// Failing reports whether this endpoint is in backoff at t.
func (e *Endpoint) Failing(t time.Time) bool {
	return e.BackoffUntil != nil && e.BackoffUntil.After(t)
}

// Subscription is what is watched and which events, posting to an Endpoint.
type Subscription struct {
	ID         string `json:"id"`
	EndpointID string `json:"endpoint_id"`
	Owner      string `json:"owner"`
	Label      string `json:"label"`

	Scope Scope `json:"scope"`
	// Job is the subject for ScopeJob.
	Job jobwatch.JobID `json:"job,omitempty"`
	// BatchID and BatchName are the subject for ScopeBatch.
	//
	// Both, because a batch is derived from job ads rather than stored:
	// the id is what a UI links to, and the name is what still selects
	// the jobs once the id's cluster has left the queue.
	BatchID   string `json:"batch_id,omitempty"`
	BatchName string `json:"batch_name,omitempty"`

	Events []jobwatch.Event `json:"events"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// CappedAt is when this subscription's selection last exceeded the
	// fan-out ceiling, and CappedJobs how many jobs were in scope then.
	// Surfaced because the deliveries that did not happen would
	// otherwise look like events that did not happen.
	CappedAt   *time.Time `json:"capped_at,omitempty"`
	CappedJobs int        `json:"capped_jobs,omitempty"`

	// Tracked is every job this subscription has ever selected. It is
	// the subscription's memory and it is load-bearing, not a cache: a
	// job gone from a complete queue read with nothing to explain it has
	// finished, and that is the only evidence there is for a job whose
	// history row never arrives. Not serialized: it is the evaluator's
	// working state, and a 100k-job cluster is not a thing to put in an
	// API response.
	Tracked []jobwatch.JobID `json:"-"`
}

// Live reports whether a subscription should still be evaluated.
//
// There is no disabled state to consult. A subscription stops when its
// subject can no longer produce an event, or when its TTL runs out --
// never because its destination was unreachable, which is the endpoint's
// business and is a reason to wait rather than to stop.
func (s *Subscription) Live(now time.Time) bool {
	return s.ExpiresAt == nil || s.ExpiresAt.After(now)
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

// Validate checks an endpoint as the user described it, before any secret
// is minted or anything is stored.
func (e *Endpoint) Validate() error {
	if e.Owner == "" {
		return fmt.Errorf("an endpoint must have an owner")
	}
	if strings.TrimSpace(e.Label) == "" {
		return fmt.Errorf("a name is required, so you can tell your destinations apart")
	}
	if len(e.Label) > MaxLabelLen {
		return fmt.Errorf("the name must be at most %d characters", MaxLabelLen)
	}
	if !e.Payload.Valid() {
		return fmt.Errorf("unknown payload mode %q (%s, %s)",
			e.Payload, PayloadSummary, PayloadFull)
	}
	return nil
}

// Validate checks a subscription as the user described it.
//
// It does NOT check that EndpointID names a real endpoint, which is a
// question for the store -- and one it has to answer under the owner
// filter, since "is this a real id" and "is it yours" must not be
// answerable apart.
func (s *Subscription) Validate() error {
	if s.Owner == "" {
		return fmt.Errorf("a subscription must have an owner")
	}
	if s.EndpointID == "" {
		return fmt.Errorf("a subscription must name the destination it posts to")
	}
	if len(s.Label) > MaxLabelLen {
		return fmt.Errorf("the name must be at most %d characters", MaxLabelLen)
	}
	if !s.Scope.Valid() {
		return fmt.Errorf("unknown scope %q", s.Scope)
	}
	switch s.Scope {
	case ScopeJob:
		if s.Job.Cluster <= 0 {
			return fmt.Errorf("a job subscription needs a cluster id")
		}
		if s.Job.Proc < 0 {
			return fmt.Errorf("a job subscription needs a proc id")
		}
	case ScopeBatch:
		if s.BatchID == "" && s.BatchName == "" {
			return fmt.Errorf("a batch subscription needs a batch")
		}
	case ScopeOwner:
		if s.Job.Cluster != 0 || s.BatchID != "" || s.BatchName != "" {
			return fmt.Errorf("an owner subscription names no job or batch")
		}
	}
	if len(s.Events) == 0 {
		return fmt.Errorf("at least one event is required")
	}
	if len(s.Events) > MaxEventsPerSubscription {
		return fmt.Errorf("at most %d events", MaxEventsPerSubscription)
	}
	seen := make(map[jobwatch.Event]bool, len(s.Events))
	for _, e := range s.Events {
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

// NewSecret mints an endpoint's signing key.
func NewSecret() ([]byte, error) {
	b := make([]byte, SecretBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating a signing secret: %w", err)
	}
	return b, nil
}

// newID mints an endpoint, subscription or delivery id.
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
	// EndpointHeader names the endpoint, for a receiver serving
	// several. The name is historical: the header is unchanged so
	// receivers written against it keep working.
	EndpointHeader = "X-HTCondor-Webhook"
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
