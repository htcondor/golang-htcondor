package idempotency

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PelicanPlatform/classad/classad"

	htcondor "github.com/htcondor/golang-htcondor"
)

// --- a Lookups that really evaluates the constraint -------------------
//
// The fake matches by EVALUATING the constraint the Guard built against
// the ads it holds, rather than by comparing keys itself. That is the
// whole value of it: a fake that compared keys would agree with the
// Guard about case folding and owner scoping no matter what the
// constraint said, and the constraint is the part that has to be right
// -- it is what a schedd and an htcondordb mirror are actually handed.

type fakeLookups struct {
	queue, history []*classad.ClassAd

	queueErr, historyErr error
	noHistory            bool

	// recorded for the assertions that care which lookup ran.
	queueCalls, historyCalls int
	lastQueue, lastHistory   string
}

func (f *fakeLookups) match(ads []*classad.ClassAd, constraint string, limit int) ([]*classad.ClassAd, error) {
	expr, err := classad.ParseExpr(constraint)
	if err != nil {
		return nil, fmt.Errorf("the Guard built a constraint that does not parse: %q: %w", constraint, err)
	}
	var out []*classad.ClassAd
	for _, ad := range ads {
		// Evaluated with the ad as BOTH self and target: a job
		// constraint names the job's own attributes.
		if v := ad.EvaluateExprWithTarget(expr, ad); v.IsBool() {
			if b, _ := v.BoolValue(); b {
				out = append(out, ad)
				if limit > 0 && len(out) >= limit {
					break
				}
			}
		}
	}
	return out, nil
}

func (f *fakeLookups) Queue(_ context.Context, constraint string, _ []string, limit int) ([]*classad.ClassAd, error) {
	f.queueCalls++
	f.lastQueue = constraint
	if f.queueErr != nil {
		return nil, f.queueErr
	}
	return f.match(f.queue, constraint, limit)
}

func (f *fakeLookups) History(_ context.Context, constraint string, _ []string, limit int) ([]*classad.ClassAd, error) {
	f.historyCalls++
	f.lastHistory = constraint
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	if f.noHistory {
		return nil, ErrHistoryUnavailable
	}
	return f.match(f.history, constraint, limit)
}

func (f *fakeLookups) HistoryConfigured() bool { return !f.noHistory }

// jobAd builds a job ad the way a submitted job's ad reads back.
func jobAd(t *testing.T, cluster, proc int, owner, key string) *classad.ClassAd {
	t.Helper()
	ad := classad.New()
	ad.InsertAttr("ClusterId", int64(cluster))
	ad.InsertAttr("ProcId", int64(proc))
	ad.InsertAttrString("Owner", owner)
	if key != "" {
		ad.InsertAttrString(Attr, key)
	}
	return ad
}

const simpleSubmit = "executable = /bin/true\nqueue\n"

func guardFor(cfg Config, look Lookups) *Guard {
	if cfg.Mode == ModeAuto && look == nil {
		cfg.Mode = ModeOff
	}
	return NewGuard(cfg, look)
}

// keyedCtx is a context carrying key for actor "alice".
func keyedCtx(key string) context.Context {
	return WithKey(context.Background(), key)
}

// --- the happy path --------------------------------------------------

func TestNoKeyChangesNothing(t *testing.T) {
	f := &fakeLookups{}
	g := guardFor(Config{Mode: ModeFull}, f)

	claim, out, err := g.Begin(context.Background(), "alice", simpleSubmit)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if claim != nil {
		t.Error("an unkeyed submission claimed something")
	}
	if out != simpleSubmit {
		t.Errorf("an unkeyed submission's submit file changed:\n%s", out)
	}
	if f.queueCalls != 0 || f.historyCalls != 0 {
		t.Errorf("an unkeyed submission read the queue %d and the history %d times; both should be 0",
			f.queueCalls, f.historyCalls)
	}
}

func TestUnusedKeyIsClaimedAndStamped(t *testing.T) {
	f := &fakeLookups{}
	g := guardFor(Config{Mode: ModeFull}, f)

	claim, out, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if claim == nil {
		t.Fatal("no claim for an unused key")
	}
	if f.queueCalls != 1 || f.historyCalls != 1 {
		t.Errorf("queue reads %d, history reads %d; want 1 and 1", f.queueCalls, f.historyCalls)
	}
	if !strings.Contains(out, `MY.IdempotencyKey = "batch-7"`) {
		t.Errorf("the key was not stamped into the submit file:\n%s", out)
	}
	if g.Held() != 1 {
		t.Errorf("Held() = %d, want 1", g.Held())
	}
}

// TestTheStampedKeyLandsOnTheJobAdAndTheConstraintFindsIt closes the loop
// between the two places a key is quoted.
//
// The key goes into a submit file as a ClassAd string literal and into a
// query constraint as the same literal, and the two are built by
// different code. If they ever disagree about a character, the job
// carries one string and the lookup asks for another -- and the feature
// reports every key as unused while looking entirely healthy. So this
// runs the real submit engine over the real stamped line, reads the
// attribute back off the real job ad, and then evaluates the real
// constraint against it.
func TestTheStampedKeyLandsOnTheJobAdAndTheConstraintFindsIt(t *testing.T) {
	keys := []string{
		"batch-7",
		"550e8400-e29b-41d4-a716-446655440000",
		`key "with" quotes`,
		"key with spaces",
		"ключ-日本語-🙂",
		"key=with=equals",
		"key#with#hash",
		"a'b`c|d&e;f",
		"key:with/slashes.and-dashes_and+plus",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			// The submit file the Guard would produce.
			g := guardFor(Config{Mode: ModeQueue}, &fakeLookups{})
			_, submitFile, err := g.Begin(keyedCtx(key), "alice", simpleSubmit)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}

			// What condor_submit's engine makes of it.
			sf, err := htcondor.ParseSubmitFile(strings.NewReader(submitFile))
			if err != nil {
				t.Fatalf("the stamped submit file does not parse: %v\n%s", err, submitFile)
			}
			ad, err := sf.MakeJobAd(htcondor.JobID{Cluster: 1, Proc: 0}, nil)
			if err != nil {
				t.Fatalf("the stamped submit file does not build a job ad: %v\n%s", err, submitFile)
			}
			got, ok := ad.EvaluateAttrString(Attr)
			if !ok {
				t.Fatalf("the job ad has no %s at all; the stamped line went nowhere:\n%s", Attr, submitFile)
			}
			if got != key {
				t.Fatalf("the job ad carries %q, the request asked for %q", got, key)
			}

			// And the constraint the lookups are given finds that ad.
			ad.InsertAttrString("Owner", "alice")
			cons, err := Constraint(context.Background(), "alice", key)
			if err != nil {
				t.Fatal(err)
			}
			expr, err := classad.ParseExpr(cons)
			if err != nil {
				t.Fatalf("the constraint does not parse: %q: %v", cons, err)
			}
			v := ad.EvaluateExprWithTarget(expr, ad)
			b, _ := v.BoolValue()
			if !v.IsBool() || !b {
				t.Errorf("the constraint does not match the job it stamped\nkey:        %q\nconstraint: %s\nad:         %s",
					key, cons, ad.String())
			}
		})
	}
}

// --- the refusals ----------------------------------------------------

func TestKeyHeldByAJobInTheQueue(t *testing.T) {
	f := &fakeLookups{queue: []*classad.ClassAd{jobAd(t, 42, 0, "alice", "batch-7")}}
	g := guardFor(Config{Mode: ModeFull}, f)

	_, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	var dup *DuplicateError
	if !errors.As(err, &dup) {
		t.Fatalf("err = %v, want a DuplicateError", err)
	}
	if dup.JobID != "42.0" {
		t.Errorf("JobID = %q, want 42.0", dup.JobID)
	}
	if !strings.Contains(dup.Error(), "still in the queue") {
		t.Errorf("message does not say where the key is: %s", dup.Error())
	}
	// The history is not worth reading once the queue has answered.
	if f.historyCalls != 0 {
		t.Errorf("the history was read %d times after the queue matched", f.historyCalls)
	}
	if g.Held() != 0 {
		t.Errorf("a refused submission left %d claims behind", g.Held())
	}
}

func TestKeyHeldByAJobInTheHistory(t *testing.T) {
	f := &fakeLookups{history: []*classad.ClassAd{jobAd(t, 7, 3, "alice", "batch-7")}}
	g := guardFor(Config{Mode: ModeFull}, f)

	_, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	var dup *DuplicateError
	if !errors.As(err, &dup) {
		t.Fatalf("err = %v, want a DuplicateError", err)
	}
	if dup.JobID != "7.3" {
		t.Errorf("JobID = %q, want 7.3", dup.JobID)
	}
	if !strings.Contains(dup.Error(), "has left the queue") {
		t.Errorf("message does not say where the key is: %s", dup.Error())
	}
}

// TestClusterAdMatchIsNamedByItsCluster covers a late-materialization
// factory: the cluster ad carries the key and ProcId -1, and naming the
// holder "9.-1" would be a job id no client could use.
func TestClusterAdMatchIsNamedByItsCluster(t *testing.T) {
	f := &fakeLookups{queue: []*classad.ClassAd{jobAd(t, 9, -1, "alice", "batch-7")}}
	g := guardFor(Config{Mode: ModeQueue}, f)

	_, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	var dup *DuplicateError
	if !errors.As(err, &dup) {
		t.Fatalf("err = %v, want a DuplicateError", err)
	}
	if dup.JobID != "9" {
		t.Errorf("JobID = %q, want 9", dup.JobID)
	}
}

// TestKeysAreCaseSensitive is the test that fails if the =?= half of the
// constraint is ever dropped in favour of the == the index wants.
func TestKeysAreCaseSensitive(t *testing.T) {
	f := &fakeLookups{queue: []*classad.ClassAd{jobAd(t, 1, 0, "alice", "Batch-7")}}
	g := guardFor(Config{Mode: ModeQueue}, f)

	claim, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	if err != nil {
		t.Fatalf("\"batch-7\" was refused by a job holding \"Batch-7\": %v", err)
	}
	if claim == nil {
		t.Fatal("no claim")
	}
	// And the exact spelling IS refused, so the above is not merely a
	// constraint that matches nothing.
	if _, _, err := g.Begin(keyedCtx("Batch-7"), "alice", simpleSubmit); err == nil {
		t.Error(`"Batch-7" was accepted although a job holds exactly that`)
	}
}

// TestKeysAreScopedToTheAccount is the DoS the owner clause prevents: one
// account must not be able to use up another's keys.
func TestKeysAreScopedToTheAccount(t *testing.T) {
	f := &fakeLookups{queue: []*classad.ClassAd{
		jobAd(t, 1, 0, "alice", "nightly"),
		jobAd(t, 2, 0, "bob", "weekly"),
	}}
	g := guardFor(Config{Mode: ModeQueue}, f)

	// Neither account's key reaches the other's. Both directions, because
	// a constraint that simply matched nothing would pass one of them.
	if _, _, err := g.Begin(keyedCtx("nightly"), "bob", simpleSubmit); err != nil {
		t.Errorf("bob was refused by alice's key: %v", err)
	}
	if _, _, err := g.Begin(keyedCtx("weekly"), "carol", simpleSubmit); err != nil {
		t.Errorf("carol was refused by bob's key: %v", err)
	}
	// And each account's own key IS refused, so the above is not a
	// constraint that matches nothing at all.
	if _, _, err := g.Begin(keyedCtx("nightly"), "alice", simpleSubmit); err == nil {
		t.Error("alice reused her own key and was allowed")
	}
	if _, _, err := g.Begin(keyedCtx("weekly"), "bob", simpleSubmit); err == nil {
		t.Error("bob reused his own key and was allowed")
	}
}

// --- fail closed -----------------------------------------------------

func TestUnreadableHistoryRefusesTheSubmission(t *testing.T) {
	f := &fakeLookups{historyErr: errors.New("connection refused")}
	g := guardFor(Config{Mode: ModeFull}, f)

	_, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	var unver *UnverifiableError
	if !errors.As(err, &unver) {
		t.Fatalf("err = %v, want an UnverifiableError -- a check that could not run must not pass", err)
	}
	if g.Held() != 0 {
		t.Errorf("a refused submission left %d claims behind", g.Held())
	}
}

func TestNoHistoryConfiguredRefusesInFullMode(t *testing.T) {
	f := &fakeLookups{noHistory: true}
	g := guardFor(Config{Mode: ModeFull}, f)

	_, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	var unver *UnverifiableError
	if !errors.As(err, &unver) {
		t.Fatalf("err = %v, want an UnverifiableError", err)
	}
	if !errors.Is(err, ErrHistoryUnavailable) {
		t.Errorf("the error does not say the history is unavailable: %v", err)
	}
}

func TestUnreadableQueueRefusesTheSubmission(t *testing.T) {
	f := &fakeLookups{queueErr: errors.New("schedd is not answering")}
	g := guardFor(Config{Mode: ModeQueue}, f)

	_, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	var unver *UnverifiableError
	if !errors.As(err, &unver) {
		t.Fatalf("err = %v, want an UnverifiableError", err)
	}
}

// --- the modes -------------------------------------------------------

// TestQueueModeDoesNotReadTheHistory pins the difference between the two
// modes. Without it a Guard that always read the history would pass
// every other test here.
func TestQueueModeDoesNotReadTheHistory(t *testing.T) {
	f := &fakeLookups{history: []*classad.ClassAd{jobAd(t, 7, 0, "alice", "batch-7")}}
	g := guardFor(Config{Mode: ModeQueue}, f)

	if _, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit); err != nil {
		t.Errorf("QUEUE mode consulted the history: %v", err)
	}
	if f.historyCalls != 0 {
		t.Errorf("the history was read %d times in QUEUE mode", f.historyCalls)
	}
}

func TestAutoResolvesAgainstWhetherHistoryIsConfigured(t *testing.T) {
	withHistory := NewGuard(Config{Mode: ModeAuto}, &fakeLookups{})
	if got := withHistory.Mode(); got != ModeFull {
		t.Errorf("AUTO with a history source = %v, want FULL", got)
	}
	without := NewGuard(Config{Mode: ModeAuto}, &fakeLookups{noHistory: true})
	if got := without.Mode(); got != ModeQueue {
		t.Errorf("AUTO with no history source = %v, want QUEUE", got)
	}
}

func TestOffModeRefusesAKeyRatherThanIgnoringIt(t *testing.T) {
	g := NewGuard(Config{Mode: ModeOff}, &fakeLookups{})
	_, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	var bad *KeyError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want a KeyError: a key that is silently not enforced is the worst outcome", err)
	}
	// An unkeyed submission still goes through untouched.
	if _, out, err := g.Begin(context.Background(), "alice", simpleSubmit); err != nil || out != simpleSubmit {
		t.Errorf("an unkeyed submission in NONE mode: err=%v, file changed=%v", err, out != simpleSubmit)
	}
}

func TestNilGuardAcceptsUnkeyedAndRefusesKeyed(t *testing.T) {
	var g *Guard
	if _, out, err := g.Begin(context.Background(), "alice", simpleSubmit); err != nil || out != simpleSubmit {
		t.Errorf("a nil Guard changed an unkeyed submission: err=%v", err)
	}
	if _, _, err := g.Begin(keyedCtx("k"), "alice", simpleSubmit); err == nil {
		t.Error("a nil Guard accepted a keyed submission")
	}
	if g.Mode() != ModeOff || g.Held() != 0 {
		t.Error("a nil Guard should report ModeOff and hold nothing")
	}
}

// --- claims ----------------------------------------------------------

func TestTwoSubmissionsOfOneKeyCannotBothPass(t *testing.T) {
	f := &fakeLookups{}
	g := guardFor(Config{Mode: ModeQueue}, f)

	first, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	if err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	// The first has not reported its outcome: the queue cannot know
	// about it yet, which is exactly the race the claim covers.
	_, _, err = g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	var dup *DuplicateError
	if !errors.As(err, &dup) {
		t.Fatalf("the second submission of an in-flight key: err = %v, want a DuplicateError", err)
	}
	if !strings.Contains(dup.Error(), "right now") {
		t.Errorf("the message does not say the first is in flight: %s", dup.Error())
	}
	first.Commit(42)

	// Settled, and still held: this is the window between the job
	// leaving the queue and the mirror carrying it.
	_, _, err = g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	if !errors.As(err, &dup) {
		t.Fatalf("after Commit: err = %v, want a DuplicateError", err)
	}
	if dup.JobID != "42.0" {
		t.Errorf("JobID = %q, want 42.0", dup.JobID)
	}
}

func TestAbandonFreesTheKey(t *testing.T) {
	f := &fakeLookups{}
	g := guardFor(Config{Mode: ModeQueue}, f)

	claim, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	if err != nil {
		t.Fatal(err)
	}
	claim.Abandon()
	if g.Held() != 0 {
		t.Errorf("Abandon left %d claims", g.Held())
	}
	// A submission that failed must not lock its own client out.
	if _, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit); err != nil {
		t.Errorf("the key was still held after Abandon: %v", err)
	}
}

func TestAbandonAfterCommitKeepsTheClaim(t *testing.T) {
	g := guardFor(Config{Mode: ModeQueue}, &fakeLookups{})
	claim, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	if err != nil {
		t.Fatal(err)
	}
	claim.Commit(42)
	// This is the deferred Abandon on the success path; it must not
	// release what Commit settled, or every committed key would be free
	// the moment the handler returned.
	claim.Abandon()
	if g.Held() != 1 {
		t.Fatalf("Held() = %d after Commit then Abandon, want 1", g.Held())
	}
}

func TestAClaimExpiresAfterRetention(t *testing.T) {
	g := guardFor(Config{Mode: ModeQueue, Retention: time.Minute}, &fakeLookups{})
	now := time.Now()
	g.now = func() time.Time { return now }

	claim, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit)
	if err != nil {
		t.Fatal(err)
	}
	claim.Commit(42)

	now = now.Add(59 * time.Second)
	if _, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit); err == nil {
		t.Error("the claim expired before its retention was up")
	}
	now = now.Add(2 * time.Second)
	if _, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit); err != nil {
		t.Errorf("the claim outlived its retention: %v", err)
	}
}

// TestAnInFlightClaimDoesNotExpire: an in-flight entry has no expiry, and
// a sweep that treated its zero expires as "long past" would release a
// key out from under a submission still running.
func TestAnInFlightClaimDoesNotExpire(t *testing.T) {
	g := guardFor(Config{Mode: ModeQueue, Retention: time.Minute}, &fakeLookups{})
	now := time.Now()
	g.now = func() time.Time { return now }

	if _, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	if g.Held() != 1 {
		t.Fatalf("Held() = %d, want 1: an in-flight claim was swept", g.Held())
	}
}

func TestConcurrentBeginsClaimOnce(t *testing.T) {
	g := guardFor(Config{Mode: ModeQueue}, &fakeLookups{})

	const n = 32
	var wg sync.WaitGroup
	granted := make(chan *Claim, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit); err == nil {
				granted <- c
			}
		}()
	}
	wg.Wait()
	close(granted)
	if got := len(granted); got != 1 {
		t.Errorf("%d of %d concurrent submissions of one key were granted; want exactly 1", got, n)
	}
}

// --- the submit file -------------------------------------------------

func TestASubmitFileSettingTheKeyItselfIsRefused(t *testing.T) {
	g := guardFor(Config{Mode: ModeQueue}, &fakeLookups{})
	for _, line := range []string{
		`MY.IdempotencyKey = "mine"`,
		`my.idempotencykey = "mine"`,
		`+IdempotencyKey = "mine"`,
		`+idempotencykey = "mine"`,
	} {
		file := "executable = /bin/true\n" + line + "\nqueue\n"
		// Refused whether or not the request carried a key: a submitter
		// who could set the attribute directly would be writing keys
		// nothing checked, and a LATER properly keyed submission would
		// then be refused by one of them.
		for _, ctx := range []context.Context{context.Background(), keyedCtx("batch-7")} {
			_, _, err := g.Begin(ctx, "alice", file)
			var bad *KeyError
			if !errors.As(err, &bad) {
				t.Errorf("%q: err = %v, want a KeyError", line, err)
			}
		}
	}
}

func TestTheKeyLandsBeforeEveryQueueStatement(t *testing.T) {
	g := guardFor(Config{Mode: ModeQueue}, &fakeLookups{})
	file := "executable = /bin/true\nqueue 2\narguments = again\nqueue 3\n"
	_, out, err := g.Begin(keyedCtx("k"), "alice", file)
	if err != nil {
		t.Fatal(err)
	}
	// Ahead of each queue statement, for the reason
	// submitpolicy.InsertBeforeQueue does it: a command reassigned
	// between two queue statements takes effect for the second.
	if got := strings.Count(out, "MY.IdempotencyKey"); got != 2 {
		t.Errorf("the key appears %d times, want 2 (one per queue statement):\n%s", got, out)
	}
	if idx, qidx := strings.Index(out, "MY.IdempotencyKey"), strings.Index(out, "queue"); idx > qidx {
		t.Errorf("the key is stamped after the first queue statement:\n%s", out)
	}
}

// --- keys ------------------------------------------------------------

func TestValidateKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		ok   bool
	}{
		{"ordinary", "batch-7", true},
		{"a uuid", "550e8400-e29b-41d4-a716-446655440000", true},
		{"at the limit", strings.Repeat("k", MaxKeyLen), true},
		{"unicode", "ключ-🙂", true},
		{"empty", "", false},
		{"one over the limit", strings.Repeat("k", MaxKeyLen+1), false},
		{"a newline", "bad\nkey", false},
		{"a backslash", `bad` + "\\" + `key`, false},
		{"a trailing backslash", `badkey` + "\\", false},
		{"a dollar sign", "bad$key", false},
		{"a macro reference", "$(Cluster)", false},
		{"a server env reference", "$ENV(HOME)", false},
		{"two dollar signs", "a$$b", false},
		{"a double quote", `has"quote`, true},
		{"a tab", "bad\tkey", false},
		{"a NUL", "bad\x00key", false},
		{"a DEL", "bad\x7fkey", false},
		{"invalid utf-8", "bad\xffkey", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateKey(c.key)
			if (err == nil) != c.ok {
				t.Errorf("ValidateKey(%q) = %v, want ok=%v", c.key, err, c.ok)
			}
		})
	}
}

// TestAnInvalidKeyIsRefusedBeforeAnythingIsRead: validation has to come
// ahead of the lookups, or a key with a control character in it would be
// quoted into a constraint and sent to a schedd.
func TestAnInvalidKeyIsRefusedBeforeAnythingIsRead(t *testing.T) {
	f := &fakeLookups{}
	g := guardFor(Config{Mode: ModeFull}, f)

	_, _, err := g.Begin(keyedCtx("bad\nkey"), "alice", simpleSubmit)
	var bad *KeyError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want a KeyError", err)
	}
	if f.queueCalls != 0 || f.historyCalls != 0 {
		t.Errorf("an invalid key was sent to the queue (%d) or the history (%d)", f.queueCalls, f.historyCalls)
	}
}

func TestAKeyNeedsAnAccount(t *testing.T) {
	g := guardFor(Config{Mode: ModeQueue}, &fakeLookups{})
	_, _, err := g.Begin(keyedCtx("batch-7"), "", simpleSubmit)
	var bad *KeyError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want a KeyError for an unauthenticated submission", err)
	}
}

func TestParseMode(t *testing.T) {
	// A slice, not a map: the point of several of these is the exact
	// spelling, including the leading space a config file leaves behind,
	// and a map keyed on them reads as a typo.
	cases := []struct {
		in   string
		want Mode
	}{
		{"", ModeAuto},
		{"auto", ModeAuto},
		{"AUTO", ModeAuto},
		{"none", ModeOff},
		{"NONE", ModeOff},
		{"queue", ModeQueue},
		{"QUEUE", ModeQueue},
		{"full", ModeFull},
		{"FULL", ModeFull},
		{"  FULL  ", ModeFull},
		{"\tQUEUE\n", ModeQueue},
	}
	for _, c := range cases {
		got, err := ParseMode(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseMode(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Error("ParseMode accepted a mode that does not exist")
	}
}

// --- the history window ----------------------------------------------

func TestHistoryWindowBoundsTheHistoryLookupOnly(t *testing.T) {
	f := &fakeLookups{}
	g := guardFor(Config{Mode: ModeFull, HistoryWindow: time.Hour}, f)
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }

	if _, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit); err != nil {
		t.Fatal(err)
	}
	wantFloor := fmt.Sprintf("EnteredHistoryTime >= %d", now.Add(-time.Hour).Unix())
	if !strings.Contains(f.lastHistory, wantFloor) {
		t.Errorf("the history constraint has no window floor\nwant it to contain: %s\ngot: %s", wantFloor, f.lastHistory)
	}
	// The queue is the live queue; a window would make no sense there
	// and would silently narrow it.
	if strings.Contains(f.lastQueue, "EnteredHistoryTime") {
		t.Errorf("the window was applied to the QUEUE lookup: %s", f.lastQueue)
	}
	// Both halves still parse: a floor spliced in wrongly would be
	// caught by the fake, which parses what it is given.
	if _, err := classad.ParseExpr(f.lastHistory); err != nil {
		t.Errorf("the windowed history constraint does not parse: %q: %v", f.lastHistory, err)
	}
}

func TestNoHistoryWindowMeansNoFloor(t *testing.T) {
	f := &fakeLookups{}
	g := guardFor(Config{Mode: ModeFull}, f)
	if _, _, err := g.Begin(keyedCtx("batch-7"), "alice", simpleSubmit); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.lastHistory, "EnteredHistoryTime") {
		t.Errorf("an unset window still bounded the history: %s", f.lastHistory)
	}
}

// --- the constraint shape --------------------------------------------

// TestKeyClauseAsksBothComparisons is the regression test for the bug
// that made the index useless: a categorical index is served for == and
// not for =?=, so the clause has to ask both. Dropping either one is
// invisible in behaviour -- the =?= alone is correct but unindexed, and
// the == alone is indexed but case-insensitive -- so it is asserted on
// the text.
//
// The matching plan assertion lives in htcondordb, where the index is
// defined, and is built from the same shape.
func TestKeyClauseAsksBothComparisons(t *testing.T) {
	got := KeyClause("batch-7")
	for _, want := range []string{
		`IdempotencyKey == "batch-7"`,
		`IdempotencyKey =?= "batch-7"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("KeyClause is missing %s\ngot: %s", want, got)
		}
	}
	// `is` is =?= to a parser, and the measured plan was measured on
	// =?=. Re-serializing the clause through the ClassAd parser turns
	// one into the other, so this pins the spelling that ships.
	if strings.Contains(got, " is ") {
		t.Errorf("the clause uses `is` rather than =?=; the index plan was measured on =?=\ngot: %s", got)
	}
}

func TestConstraintIsOwnerScoped(t *testing.T) {
	got, err := Constraint(context.Background(), "alice@example.com", "batch-7")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `Owner == "alice"`) {
		t.Errorf("the constraint is not scoped to the account's Owner\ngot: %s", got)
	}
	if _, err := classad.ParseExpr(got); err != nil {
		t.Errorf("the constraint does not parse: %q: %v", got, err)
	}
}

// TestAKeyCannotReachTheSubmitFileMacroExpander is the security half of
// ValidateKey's dollar-sign rule, named so that relaxing that rule fails
// a test that says why it exists.
//
// A submit-file line is macro-expanded before it is read as a value, and
// the expansion happens in THIS server's process: $ENV(NAME) reads the
// API server's own environment. A key stamped in verbatim would let any
// submitter choose what to read and then fetch it back off their job ad
// -- the server's environment is where its secrets live. The key never
// reaches the expander, because a key containing a dollar sign never
// reaches the submit file at all.
func TestAKeyCannotReachTheSubmitFileMacroExpander(t *testing.T) {
	f := &fakeLookups{}
	g := guardFor(Config{Mode: ModeFull}, f)

	for _, key := range []string{
		"$ENV(PATH)",
		"$ENV(HOME)",
		"prefix-$ENV(SECRET_TOKEN)-suffix",
		"$(Cluster)",
		"$(DOLLAR)",
		"$$(Memory)",
	} {
		_, out, err := g.Begin(keyedCtx(key), "alice", simpleSubmit)
		var bad *KeyError
		if !errors.As(err, &bad) {
			t.Errorf("key %q: err = %v, want a KeyError", key, err)
		}
		if out != "" {
			t.Errorf("key %q produced a submit file: %s", key, out)
		}
		if f.queueCalls != 0 {
			t.Errorf("key %q was carried into a lookup", key)
		}
	}
}
