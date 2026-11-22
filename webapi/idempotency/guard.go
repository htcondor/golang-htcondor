package idempotency

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/PelicanPlatform/classad/classad"

	"github.com/htcondor/golang-htcondor/webapi/submitpolicy"
)

// keyContextKey carries the key a request supplied down to the submit
// path.
//
// A context value rather than an argument because the submit path is
// shared: every surface that submits a job funnels into one function per
// server, reached from a dozen handlers, and threading an extra
// parameter through all of them would be a parameter most of them pass
// empty. The surfaces that can carry a key are the ones that set it.
type keyContextKey struct{}

// WithKey returns ctx carrying key for the submission it covers. An
// empty key returns ctx unchanged, so a surface need not branch.
func WithKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, keyContextKey{}, key)
}

// KeyFrom returns the key ctx carries, if any.
func KeyFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(keyContextKey{}).(string)
	return v, ok && v != ""
}

// Guard enforces idempotency keys for one server. A nil *Guard enforces
// nothing and accepts every submission, so a deployment that never
// configured one needs no branch at the call sites.
type Guard struct {
	cfg  Config
	look Lookups
	now  func() time.Time

	mu   sync.Mutex
	held map[slot]*entry
}

// slot identifies a claim. The account is part of it because keys are
// per-account; see the package comment.
type slot struct {
	actor string
	key   string
}

// entry is a held key: in flight, or settled until expires.
type entry struct {
	// inFlight is true while a submission holding this slot has not yet
	// reported its outcome.
	inFlight bool
	// jobID is the cluster the settled submission created, "N.0".
	jobID string
	// expires is when a settled entry stops being consulted. It is the
	// zero time while in flight, which never expires -- an abandoned
	// submission releases its slot explicitly.
	expires time.Time
}

// NewGuard returns a Guard. look may be nil only with Config.Mode
// ModeOff.
func NewGuard(cfg Config, look Lookups) *Guard {
	return &Guard{cfg: cfg, look: look, now: time.Now, held: make(map[slot]*entry)}
}

// Mode reports the mode this Guard enforces, resolving ModeAuto against
// whether a history source is configured. A nil Guard reports ModeOff.
func (g *Guard) Mode() Mode {
	if g == nil {
		return ModeOff
	}
	if g.cfg.Mode != ModeAuto {
		return g.cfg.Mode
	}
	if g.look != nil && g.look.HistoryConfigured() {
		return ModeFull
	}
	return ModeQueue
}

// Begin checks the idempotency key ctx carries, claims it, and returns
// the submit file with the key stamped onto the job ad.
//
// With no key in ctx it returns a nil Claim and submitFile unchanged --
// except that it still refuses a submit file setting the key attribute
// itself, which is the one check that has to run on every submission
// rather than only on keyed ones. A submitter who could set the
// attribute directly would be writing keys nothing enforced.
//
// On any error nothing is claimed. On success the caller MUST report the
// outcome: Commit with the cluster id, or Abandon. Both are safe on a
// nil Claim and after the other, so
//
//	claim, file, err := g.Begin(ctx, actor, file)
//	if err != nil { return err }
//	defer claim.Abandon()
//
// is the whole of the contract, with Commit on the success path.
func (g *Guard) Begin(ctx context.Context, actor, submitFile string) (*Claim, string, error) {
	key, keyed := KeyFrom(ctx)
	if g == nil {
		// Nothing enforces keys here. A submit file that sets the
		// attribute is then just a custom attribute, as it was before
		// this package existed, and a request that supplied a key is
		// refused below only when a Guard exists to refuse it -- a
		// server with no Guard has no surface that reads the header.
		if keyed {
			return nil, "", &KeyError{Reason: "this access point does not enforce idempotency keys"}
		}
		return nil, submitFile, nil
	}
	if err := claimedAttributeRefusal(submitFile); err != nil {
		return nil, "", err
	}
	if !keyed {
		return nil, submitFile, nil
	}
	if g.Mode() == ModeOff {
		return nil, "", &KeyError{Reason: "this access point does not enforce idempotency keys " +
			"(HTTP_API_IDEMPOTENCY_KEY_CHECK = NONE), so a request carrying one is refused rather " +
			"than accepted unchecked"}
	}
	if err := ValidateKey(key); err != nil {
		return nil, "", err
	}

	constraint, err := Constraint(ctx, actor, key)
	if err != nil {
		return nil, "", &KeyError{Reason: fmt.Sprintf("an idempotency key needs an authenticated account to belong to: %v", err)}
	}

	// Claim first, so that two submissions arriving together cannot both
	// pass the lookups below.
	claim, err := g.take(actor, key)
	if err != nil {
		return nil, "", err
	}
	if err := g.lookup(ctx, key, constraint); err != nil {
		claim.Abandon()
		return nil, "", err
	}
	return claim, submitpolicy.InsertBeforeQueue(submitFile, SubmitLine(key)), nil
}

// take claims the slot for actor's key, or reports who holds it.
func (g *Guard) take(actor, key string) (*Claim, error) {
	s := slot{actor: actor, key: key}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	if e, ok := g.held[s]; ok {
		where := "was submitted by this server"
		if e.inFlight {
			where = "is being submitted right now"
		}
		return nil, &DuplicateError{Key: key, JobID: e.jobID, Where: where}
	}
	g.held[s] = &entry{inFlight: true}
	return &Claim{g: g, slot: s}, nil
}

// lookup consults the queue and, in ModeFull, the history.
func (g *Guard) lookup(ctx context.Context, key, constraint string) error {
	ctx, cancel := context.WithTimeout(ctx, g.cfg.timeout())
	defer cancel()

	attrs := []string{"ClusterId", "ProcId"}

	ads, err := g.look.Queue(ctx, constraint, attrs, 1)
	if err != nil {
		// The live queue is the schedd, and a submission whose schedd
		// cannot be queried is not going to be submitted either. Fail
		// closed here as in ModeFull: reporting "unused" because the
		// check could not run is the one answer that silently breaks
		// the promise.
		return &UnverifiableError{Key: key, Err: fmt.Errorf("reading the job queue: %w", err)}
	}
	if len(ads) > 0 {
		return &DuplicateError{Key: key, JobID: jobIDOf(ads[0]), Where: "is still in the queue"}
	}

	if g.Mode() != ModeFull {
		return nil
	}

	hcons := constraint
	if w := g.cfg.HistoryWindow; w > 0 {
		hcons = historyFloor(constraint, g.now().Add(-w))
	}
	ads, err = g.look.History(ctx, hcons, attrs, 1)
	if err != nil {
		return &UnverifiableError{Key: key, Err: fmt.Errorf("reading the completed-job history: %w", err)}
	}
	if len(ads) > 0 {
		return &DuplicateError{Key: key, JobID: jobIDOf(ads[0]), Where: "has left the queue"}
	}
	return nil
}

// jobIDOf renders an ad's job id, or "" when it carries neither half.
//
// The ad came from a projection, so an id this cannot read means the
// projection and this function disagree -- not that the match was not a
// match. The caller still refuses the submission; it just cannot say
// which job holds the key.
func jobIDOf(ad *classad.ClassAd) string {
	if ad == nil {
		return ""
	}
	cluster, cok := ad.EvaluateAttrInt("ClusterId")
	if !cok {
		return ""
	}
	// A CLUSTER ad is a match too, and carries ProcId -1: the key lives
	// on the cluster ad, and a submission whose procs have not
	// materialized yet has nothing else to match. Name it by its
	// cluster, which is the id a client would use.
	proc, pok := ad.EvaluateAttrInt("ProcId")
	if !pok || proc < 0 {
		return fmt.Sprintf("%d", cluster)
	}
	return fmt.Sprintf("%d.%d", cluster, proc)
}

// expireLocked drops settled claims whose retention has run out.
//
// Swept on use rather than by a timer: the map holds one entry per keyed
// submission in the retention window, every path that touches it sweeps
// first, and a server submitting nothing has nothing to expire.
func (g *Guard) expireLocked() {
	now := g.now()
	for s, e := range g.held {
		if !e.inFlight && !e.expires.After(now) {
			delete(g.held, s)
		}
	}
}

// Claim is a held idempotency key. Its methods are safe on a nil
// receiver and after either has been called.
type Claim struct {
	g    *Guard
	slot slot
	done bool
}

// Commit records that the claim's submission created cluster. The key
// stays claimed for Config.Retention, which is what covers the window
// between the job leaving the queue and the history mirror carrying it.
func (c *Claim) Commit(cluster int) {
	if c == nil || c.done {
		return
	}
	c.done = true
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	if e, ok := c.g.held[c.slot]; ok {
		e.inFlight = false
		e.jobID = fmt.Sprintf("%d.0", cluster)
		e.expires = c.g.now().Add(c.g.cfg.retention())
	}
}

// Abandon releases the claim: the submission did not happen, so the key
// is unused and the next attempt must be free to use it. Calling it
// after Commit does nothing, which is what lets a caller defer it
// unconditionally.
func (c *Claim) Abandon() {
	if c == nil || c.done {
		return
	}
	c.done = true
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	delete(c.g.held, c.slot)
}

// Key is the key this claim holds, for logging.
func (c *Claim) Key() string {
	if c == nil {
		return ""
	}
	return c.slot.key
}

// Held reports how many keys are claimed, for the server's own ad and
// for tests.
func (g *Guard) Held() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.expireLocked()
	return len(g.held)
}
