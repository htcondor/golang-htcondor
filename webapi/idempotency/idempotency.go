// Package idempotency refuses a job submission whose idempotency key has
// already been used.
//
// The problem it solves is the one every submit API has: a client POSTs a
// submission, the connection drops before the response arrives, and the
// client cannot tell whether the jobs were queued. Retrying risks running
// the work twice; not retrying risks never running it. An idempotency key
// lets the client retry safely -- the second attempt carries the same key
// and is refused with the job id the first one created.
//
// # Where a key may already be
//
// A key is used up by a job that exists anywhere in its life, so three
// places are consulted, in increasing cost:
//
//  1. this process's claims (below), which also serialize two concurrent
//     submissions of the same key;
//  2. the live queue, read from the schedd itself;
//  3. the completed-job history, read from a synchronized htcondordb
//     mirror.
//
// The live queue is read from the SCHEDD and not from the mirror's jobs
// table, for two reasons that both bite exactly the case this feature is
// for. The mirror lags the schedd by seconds and a retry arrives within
// seconds, so a mirrored read is at its least reliable precisely when it
// is asked. And a Go-submitted job keeps its attributes on the CLUSTER
// ad, so a mirrored proc row carries the key only through scheddsync's
// proc-to-cluster chaining, where the schedd merges the chain itself
// before answering any query.
//
// # Claims, and the gap they cover
//
// A key this process has just submitted is held in memory for
// Config.Retention. That is not a cache: between the schedd writing a
// job's history record and htcondordb ingesting it, the key is in
// NEITHER the queue nor the history, and a retry is as likely to land in
// that window as anywhere, since retries follow by seconds. The claim is
// what covers it.
//
// The claim is also the lock. Two submissions carrying one key, arriving
// together, would both pass a check made against the queue alone; the
// first to claim wins and the second is refused as a duplicate.
//
// # What bounds the guarantee
//
// Claims live in this process, so they do not survive a restart and are
// not shared between replicas of this server. Behind a load balancer, two
// simultaneous retries landing on two replicas can both pass -- the queue
// and history checks still apply to both, so the window is the ingest gap
// above, not the whole history. A deployment that needs the guarantee to
// hold across replicas wants the check in the schedd, not here.
//
// Keys are scoped to the submitting account, so one account cannot use up
// another's keys. This differs from enforcing them access-point-wide, and
// deliberately: a shared key space lets any user burn any other user's
// keys, and a retry is by definition the same credential retrying.
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PelicanPlatform/classad/classad"

	"github.com/htcondor/golang-htcondor/webapi/ownerscope"
	"github.com/htcondor/golang-htcondor/webapi/submitpolicy"
)

// Attr is the job attribute a submission's key is stored in.
//
// It goes on the job ad as a custom attribute, so it lands on the CLUSTER
// ad (condor_submit folds proc 0's attributes into the parent) and
// sPrintAd flattens it into the history record. One attribute name
// therefore answers for both the live queue and the history, and the
// lookup need not know whether a cluster or a proc declared it.
//
// It is spelled exactly as htcondordb's scheddsync.IdempotencyKeyAttr,
// which is what that side indexes. The two live in different modules, so
// nothing but this comment holds them together; see KeyClause.
const Attr = "IdempotencyKey"

// MaxKeyLen bounds a key in bytes. A key is stored on every job ad that
// carries it and indexed in the history, and it identifies a client's
// retry -- a UUID is 36 bytes and the longest plausible structured key is
// a small multiple of that.
const MaxKeyLen = 1024

// Mode is how much of the above a deployment enforces.
type Mode int

const (
	// ModeAuto is ModeFull where a history mirror is configured and
	// ModeQueue where none is. It is the default.
	ModeAuto Mode = iota
	// ModeOff enforces nothing. A submission that carries a key anyway
	// is REFUSED rather than accepted unchecked: a client that asked for
	// this guarantee and silently did not get it is worse off than one
	// told the access point does not offer it.
	ModeOff
	// ModeQueue consults this process's claims and the live queue, and
	// not the history. The guarantee is correspondingly narrower: a key
	// used by a job that has since left the queue, longer ago than
	// Retention, reads as unused.
	ModeQueue
	// ModeFull consults the history as well, and REFUSES a submission
	// whose history check cannot be completed. Failing closed is the
	// point: a check that passes because it could not run is not a
	// check.
	ModeFull
)

func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "NONE"
	case ModeQueue:
		return "QUEUE"
	case ModeFull:
		return "FULL"
	default:
		return "AUTO"
	}
}

// ParseMode reads a Mode from its configured spelling.
func ParseMode(s string) (Mode, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "", "AUTO":
		return ModeAuto, nil
	case "NONE", "OFF", "FALSE":
		return ModeOff, nil
	case "QUEUE":
		return ModeQueue, nil
	case "FULL", "TRUE":
		return ModeFull, nil
	}
	return ModeAuto, fmt.Errorf("%q is not one of NONE, QUEUE, FULL or AUTO", s)
}

// Config is a Guard's operator configuration. The zero value is
// ModeAuto with the defaults below.
type Config struct {
	Mode Mode

	// Retention is how long a submitted key stays claimed in this
	// process. It needs to exceed the time between the schedd writing a
	// history record and the mirror ingesting it, which is the window
	// claims exist to cover. Zero means DefaultRetention.
	Retention time.Duration

	// Timeout bounds the queue and history lookups together. A
	// submission waits on them, so this is also the longest a keyed
	// submission can be delayed by the check. Zero means
	// DefaultTimeout.
	Timeout time.Duration

	// HistoryWindow, when non-zero, limits the history lookup to
	// records that entered the history within it, as a floor on
	// EnteredHistoryTime. It trades the guarantee for the cost of the
	// read: a key reused after the window reads as unused. Zero
	// consults the whole history, which is what the index is for.
	HistoryWindow time.Duration
}

// Defaults for Config's durations.
const (
	DefaultRetention = time.Hour
	DefaultTimeout   = 10 * time.Second
)

func (c Config) retention() time.Duration {
	if c.Retention <= 0 {
		return DefaultRetention
	}
	return c.Retention
}

func (c Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

// Lookups reads the two places outside this process a key may already
// be. Both return the matching job ads, newest first where the source
// has an order, projected onto attrs.
//
// They return ADS and not wire text on purpose. The mirror serves the
// live and the history table in different ClassAd serializations, and
// each parser reads the other's output as an empty ad with no error at
// all -- so whoever knows which op was called must do the parsing. That
// is the implementation, not this interface.
type Lookups interface {
	// Queue reads the live job queue.
	Queue(ctx context.Context, constraint string, attrs []string, limit int) ([]*classad.ClassAd, error)

	// History reads the completed-job history. It returns an error
	// wrapping ErrHistoryUnavailable when there is no history to read,
	// which ModeFull treats as a failed check rather than an empty one.
	History(ctx context.Context, constraint string, attrs []string, limit int) ([]*classad.ClassAd, error)

	// HistoryConfigured reports whether a history source is configured
	// at all. ModeAuto reads it once per submission to decide between
	// ModeFull and ModeQueue; it says nothing about whether the source
	// is reachable right now, which only a read can.
	HistoryConfigured() bool
}

// ErrHistoryUnavailable marks a history lookup that could not be made.
var ErrHistoryUnavailable = errors.New("no completed-job history is available")

// DuplicateError is the refusal this package exists for: the key is
// already used. An HTTP surface answers it with 409 Conflict.
type DuplicateError struct {
	// Key is the key the submission carried.
	Key string
	// JobID is the job holding it, "cluster.proc", or empty when the
	// holder is a claim this process made and has not yet learned the
	// cluster of.
	JobID string
	// Where is where it was found, for the message: "still in the
	// queue", "has left the queue", "was submitted by this server".
	Where string
}

func (e *DuplicateError) Error() string {
	if e.JobID == "" {
		return fmt.Sprintf("idempotency key %q is already in use: a submission carrying it %s", e.Key, e.Where)
	}
	return fmt.Sprintf("idempotency key %q is already in use by job %s, which %s", e.Key, e.JobID, e.Where)
}

// UnverifiableError is ModeFull failing closed: the key may be unused,
// and the check could not say so. An HTTP surface answers it with 503,
// because the request may well succeed when asked again.
type UnverifiableError struct {
	Key string
	Err error
}

func (e *UnverifiableError) Error() string {
	return fmt.Sprintf("cannot confirm idempotency key %q is unused, so the submission is refused: %v", e.Key, e.Err)
}

func (e *UnverifiableError) Unwrap() error { return e.Err }

// KeyError is a key, or a submit file, this package will not accept. It
// is the submitter's to fix, so an HTTP surface answers it with 400.
type KeyError struct{ Reason string }

func (e *KeyError) Error() string { return e.Reason }

// ValidateKey reports why key is unusable, or nil.
//
// The key travels through two quoting layers that do not share a rule: a
// submit-file value on its way onto the job ad (SubmitLine) and a ClassAd
// string literal on its way into the lookup constraint (KeyClause). What
// this refuses is what cannot be made to mean the same thing in both --
// and refusing is the point, because the alternative to an encoding that
// provably round-trips is one that corrupts the key quietly, and a
// corrupted key does not fail loudly: it reads as unused, and the retry
// runs the work twice.
//
// Each rejection was measured against the real submit engine rather than
// reasoned about:
//
//   - A CONTROL CHARACTER has no spelling in either layer.
//
//   - A BACKSLASH has none in a submit-file value. That parser unescapes
//     an escaped double quote and passes a doubled backslash through as
//     two characters, so there is no way to write one literal backslash
//     -- and a key ENDING in one would turn the closing quote into an
//     escaped quote and leave the string unterminated.
//
//   - A DOLLAR SIGN is macro-expanded before any of the line is read as
//     a value, and this is the one with teeth: $(Cluster) in a key
//     becomes the cluster number, and $ENV(NAME) reads THIS SERVER's
//     environment and writes it into a job attribute the submitter can
//     read straight back. The documented escape, $(DOLLAR), covers one
//     dollar sign and fails on two: the pair expands left to right, the
//     first yields a $ that turns the second into $$(DOLLAR), and
//     $$(...) is the machine-ad substitution the submit engine
//     deliberately leaves for the startd. An escape that works for one
//     and not two is worse than no escape.
//
//   - INVALID UTF-8 would store and query byte-for-byte and so would
//     work, but it cannot survive the JSON response that tells a client
//     which job holds its key.
//
// A double quote IS allowed: its escape is the one this layer honours,
// and the round-trip test covers it.
//
// None of this restricts a realistic key. A client picks an opaque token
// -- a UUID, a hash, a request id -- and anything it cannot spell here it
// can hex or base64.
func ValidateKey(key string) error {
	switch {
	case key == "":
		return &KeyError{Reason: "the idempotency key is empty"}
	case len(key) > MaxKeyLen:
		return &KeyError{Reason: fmt.Sprintf("the idempotency key is %d bytes, over the %d-byte limit", len(key), MaxKeyLen)}
	case !utf8.ValidString(key):
		return &KeyError{Reason: "the idempotency key is not valid UTF-8"}
	}
	for _, r := range key {
		switch {
		case r < 0x20 || r == 0x7f:
			return &KeyError{Reason: "the idempotency key contains a control character"}
		case r == '\\':
			return &KeyError{Reason: "the idempotency key contains a backslash, which a " +
				"submit-file value has no unambiguous spelling for"}
		case r == '$':
			return &KeyError{Reason: "the idempotency key contains a dollar sign, which a " +
				"submit-file value reads as a macro reference and cannot reliably escape"}
		}
	}
	return nil
}

// KeyClause is the constraint that finds the job holding key, and it is
// two comparisons where one would read more naturally:
//
//	IdempotencyKey == "<key>" && IdempotencyKey =?= "<key>"
//
// =?= alone is what we mean. ClassAd's == folds case on strings, so a
// client that chose "Batch-7" has not used up "batch-7". But htcondordb
// serves a categorical index for == and NOT for =?=: measured on an
// indexed archive, the =?= probe plans as a serial scan and the ==
// probe plans as indexed. A =?= on its own therefore reads every record
// in the history on every keyed submission, while the index the table
// pays for contributes nothing -- and it still returns the right answer,
// so nothing but a plan assertion notices.
//
// Both comparisons together get both properties: the folded == narrows
// through the index to the case-variants of the key, normally exactly
// one record, and the =?= then decides. An access point holding both
// "Batch-7" and "batch-7" pays one extra record to filter.
//
// The operators are written here literally and NOT passed through
// ownerscope.Balanced, which re-serializes =?= to its synonym `is`. The
// two are the same operator to a parser, but the plan measured above was
// measured on =?=, and this is not the place to find out whether the
// planner agrees about the other spelling.
//
// The index that makes this worth doing lives in htcondordb
// (cmd/htcondordb/scheddsync_indexes.go, categorical on Owner and
// IdempotencyKey) and is asserted there, against this same expression
// shape. The two are in different modules and nothing links them but
// that test, so a change to this clause belongs with a change to it.
func KeyClause(key string) string {
	lit := ownerscope.StringLit(key)
	return Attr + " == " + lit + " && " + Attr + " =?= " + lit
}

// Constraint is KeyClause confined to actor's own jobs.
//
// The owner clause comes from ownerscope so that this agrees with every
// other owner-scoped read in the service about the Owner-versus-User
// distinction: an account condor_placementd assigned is named by its
// full User, and confining it by Owner would reach another account's
// jobs.
func Constraint(ctx context.Context, actor, key string) (string, error) {
	clause, err := ownerscope.OwnerClause(ctx, actor)
	if err != nil {
		return "", err
	}
	return "(" + clause + ") && (" + KeyClause(key) + ")", nil
}

// historyFloor ANDs a lower bound on EnteredHistoryTime onto constraint.
func historyFloor(constraint string, floor time.Time) string {
	return "(" + constraint + ") && (EnteredHistoryTime >= " + fmt.Sprint(floor.Unix()) + ")"
}

// SubmitLine is the submit-file line that stamps key onto the job ad.
//
// MY. rather than +: the two are the same thing to condor_submit, and
// this spelling cannot be confused with an arithmetic continuation of
// the line above.
//
// The value is NOT quoted by ownerscope.StringLit, which is right for a
// ClassAd constraint and wrong here. That function doubles a backslash
// and this layer does not undouble one; it also leaves a dollar sign
// alone and this layer expands it. ValidateKey refuses both characters,
// which is what reduces the encoding to the single escape below rather
// than leaving it merely untested.
//
// TestTheStampedKeyLandsOnTheJobAdAndTheConstraintFindsIt runs the real
// submit engine over this line and then evaluates the real constraint
// against the resulting ad. That pairing is the only check that the two
// encodings agree; each looks correct on its own while disagreeing.
func SubmitLine(key string) string {
	quoted := strings.ReplaceAll(key, `"`, `\"`)
	return "MY." + Attr + ` = "` + quoted + "\"\n"
}

// claimedAttributeRefusal is the refusal for a submit file that sets the
// key attribute itself.
//
// Accepting it would be the quiet failure this whole package is against.
// The attribute is what the queue and the history are searched by, so a
// value this server did not check means a job carrying a key that was
// never enforced -- and then a LATER submission, properly keyed, is
// refused by it. The key comes from the request, where the server can
// see it.
func claimedAttributeRefusal(submitFile string) error {
	for _, name := range submitpolicy.CustomAttributeNames(submitFile) {
		if strings.EqualFold(name, Attr) {
			return &KeyError{Reason: fmt.Sprintf(
				"the submit file sets job attribute %s itself; an idempotency key is supplied "+
					"with the request (the Idempotency-Key header), not in the submit file, "+
					"because only a key this server is given can be checked", name)}
		}
	}
	return nil
}
