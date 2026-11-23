package httpserver

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PelicanPlatform/classad/classad"

	"github.com/htcondor/golang-htcondor/config"
	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/connectors"
	"github.com/htcondor/golang-htcondor/webapi/httpserver/appdb"
	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
	"github.com/htcondor/golang-htcondor/webapi/ownerscope"
)

// What the sweep is allowed to ask for and allowed to do, as opposed to
// what it decides (connectors_output_test.go). The queue constraint and
// the on/off switch are the two places where a mistake would not look
// like a bug in anybody's spreadsheet.

// The constraint a binding turns into must not match another user's job,
// whatever the binding holds.
func TestOutputClusterConstraintConfinesToTheOwner(t *testing.T) {
	mine := mustAd(t, `[User = "alice@d"; Owner = "alice"; ClusterId = 12; ProcId = 0]`)
	otherUser := mustAd(t, `[User = "bob@d"; Owner = "bob"; ClusterId = 12; ProcId = 0]`)
	otherCluster := mustAd(t, `[User = "alice@d"; Owner = "alice"; ClusterId = 13; ProcId = 0]`)

	for _, tc := range []struct {
		name string
		b    connectors.OutputBinding
	}{
		{"local account", connectors.OutputBinding{Subject: "alice", Cluster: 12}},
		{"qualified subject", connectors.OutputBinding{Subject: "alice@d", Cluster: 12}},
		{"placementd account", connectors.OutputBinding{
			Subject: "alice@d", Cluster: 12, UserScoped: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := outputClusterConstraint(tc.b)
			if err != nil {
				t.Fatalf("outputClusterConstraint: %v", err)
			}
			expr, err := classad.ParseExpr(got)
			if err != nil {
				t.Fatalf("produced unparseable %q", got)
			}
			if !adMatches(expr, mine) {
				t.Errorf("%q does not match the binding's own job", got)
			}
			if adMatches(expr, otherUser) {
				t.Errorf("%q matches another user's cluster 12", got)
			}
			if adMatches(expr, otherCluster) {
				t.Errorf("%q matches a different cluster", got)
			}
		})
	}
}

// A subject that looks like ClassAd syntax stays a string literal. The
// clause is built by ownerscope, which quotes it; this pins that it
// keeps being built that way rather than formatted by hand.
func TestOutputClusterConstraintQuotesTheSubject(t *testing.T) {
	b := connectors.OutputBinding{Subject: `a" || true || "`, Cluster: 12}
	got, err := outputClusterConstraint(b)
	if err != nil {
		t.Fatalf("outputClusterConstraint: %v", err)
	}
	expr, err := classad.ParseExpr(got)
	if err != nil {
		t.Fatalf("produced unparseable %q", got)
	}
	other := mustAd(t, `[User = "bob@d"; Owner = "bob"; ClusterId = 12; ProcId = 0]`)
	if adMatches(expr, other) {
		t.Errorf("%q matches another user's job; the subject escaped its literal", got)
	}
}

// Unset leaves the sweep off. This is the assertion that matters most in
// the file: what it starts writes to somebody's Google account while
// they are not present, and an upgrade must not begin doing that.
func TestOutputSweepIsOffUnlessConfigured(t *testing.T) {
	for _, tc := range []struct {
		name  string
		param string
		want  time.Duration
	}{
		{"unset", "", 0},
		{"blank", "   ", 0},
		{"zero", "0", 0},
		{"no unit", "300", 0},
		{"nonsense", "soon", 0},
		{"negative", "-5m", 0},
		{"a cadence", "5m", 5 * time.Minute},
		{"clamped", "2s", time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{logger: testLogger(t)}
			params := config.NewEmpty()
			if tc.param != "" {
				params.Set("HTTP_API_CONNECTOR_OUTPUT_SWEEP_INTERVAL", tc.param)
			}
			if got := h.outputSweepIntervalFor(params); got != tc.want {
				t.Errorf("interval for %q = %v, want %v", tc.param, got, tc.want)
			}
		})
	}
}

// A nil configuration is off rather than a panic: the same
// nil-in-an-interface care connectorConfigFor takes.
func TestOutputSweepIntervalWithoutConfig(t *testing.T) {
	h := &Handler{logger: testLogger(t)}
	if got := h.outputSweepIntervalFor(nil); got != 0 {
		t.Errorf("interval with no config = %v, want 0", got)
	}
}

// Multi-AP mode refuses to start the sweep. scheddFor there fails closed
// without a routed request's caller credential, on purpose; a background
// sweep has no request, and the answer is to not run rather than to find
// a way round that check.
func TestOutputSweepRefusesMultiAP(t *testing.T) {
	h, logged := sweepStartHandler(t)
	h.multi = &multiAPMode{}
	h.connectorOutputSweepInterval = time.Minute
	// A real store, so the "no database" refusal cannot stand in for
	// the one under test: without this the guard could be deleted and
	// the test would still pass on the other message.
	h.connectorStored = connectors.NewStore(openTestAppDB(t), nil)

	startAndStop(t, h)

	if got := logged(); !strings.Contains(got, "multi-AP") {
		t.Errorf("multi-AP mode did not say why it refused; log: %s", got)
	} else if strings.Contains(got, "Started the connector output sweep") {
		t.Errorf("multi-AP mode started the sweep anyway; log: %s", got)
	}
}

// With no interval nothing starts and nothing is said about it: an
// operator who has not turned this on should not see it mentioned.
func TestOutputSweepSilentWhenOff(t *testing.T) {
	h, logged := sweepStartHandler(t)

	startAndStop(t, h)

	if got := logged(); got != "" {
		t.Errorf("a disabled sweep logged %q", got)
	}
}

// Enabled with no database is refused too, and says which half is
// missing: the bindings live in the application database, so without one
// there is nothing the sweep could read.
func TestOutputSweepRefusesWithoutADatabase(t *testing.T) {
	h, logged := sweepStartHandler(t)
	h.connectorOutputSweepInterval = time.Minute

	startAndStop(t, h)

	if !strings.Contains(logged(), "database") {
		t.Errorf("no database did not explain itself; log: %s", logged())
	}
}

// startAndStop runs the start decision and then makes sure nothing it
// may have started is still running.
//
// Never h.wg.Wait() on its own: a sweep that DID start runs until its
// context is done, so waiting without cancelling turns "it started when
// it should not have" into a test that hangs instead of one that fails.
// Cancelling first makes the wait bounded whichever way the decision
// went, and the log is what the assertion reads.
func startAndStop(t *testing.T, h *Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	h.startConnectorOutputSweeper(ctx)
	cancel()

	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the sweep goroutine did not stop when its context was cancelled")
	}
}

// sweepStartHandler is a handler just complete enough to decide whether
// to start the sweep, plus a reader for what it logged.
func sweepStartHandler(t *testing.T) (*Handler, func() string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sweep.log")
	logger, err := logging.New(&logging.Config{
		OutputPath:        path,
		DefaultLevel:      logging.VerbosityDebug,
		SkipGlobalInstall: true,
	})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return &Handler{logger: logger}, func() string {
		//nolint:gosec // path is this test's own t.TempDir() file
		b, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// openTestAppDB is a migrated application database.
func openTestAppDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := appdb.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("appdb.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := appdb.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func adMatches(expr *classad.Expr, ad *classad.ClassAd) bool {
	v, err := expr.Eval(ad).BoolValue()
	return err == nil && v
}

// Whether a binding may still spend its credential, given what could be
// learned about its owner's groups.
//
// The interesting case is the last one. Refusing when nothing can
// answer was the first implementation, and the integration test showed
// what it costs: liveGroupsFor answers only when this machine sources
// groups AND the identity is in UID_DOMAIN, so an access point whose
// groups come from an identity provider would never sweep anything, for
// anybody. A check that can be made is still made.
func TestConnectorStillPermitted(t *testing.T) {
	restricted := connectors.Definition{
		ID:     connectors.MustParse("builtin:gdrive"),
		Groups: []string{"lab"},
	}
	open := connectors.Definition{
		ID:     connectors.MustParse("builtin:gdrive"),
		Groups: []string{"*"},
	}

	for _, tc := range []struct {
		name     string
		def      connectors.Definition
		groups   []string
		answered bool
		want     bool
		note     string
	}{
		{
			name: "open to everyone asks nothing",
			def:  open, groups: nil, answered: false,
			want: true, note: "",
		},
		{
			name: "still a member",
			def:  restricted, groups: []string{"lab", "other"}, answered: true,
			want: true, note: "",
		},
		{
			// The half that matters: a snapshot taken at submit time
			// would still say yes here.
			name: "no longer a member",
			def:  restricted, groups: []string{"other"}, answered: true,
			want: false, note: "no longer in a group",
		},
		{
			name: "in no groups at all is a real answer",
			def:  restricted, groups: nil, answered: true,
			want: false, note: "no longer in a group",
		},
		{
			// "Could not ask" is not evidence of removal.
			name: "nothing could report membership",
			def:  restricted, groups: nil, answered: false,
			want: true, note: "cannot read the owner's membership",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, note := connectorStillPermitted(tc.def, tc.groups, tc.answered)
			if allowed != tc.want {
				t.Errorf("allowed = %v, want %v (note %q)", allowed, tc.want, note)
			}
			if tc.note == "" {
				if note != "" {
					t.Errorf("note = %q, want none", note)
				}
				return
			}
			if !strings.Contains(note, tc.note) {
				t.Errorf("note = %q, want it to mention %q", note, tc.note)
			}
		})
	}
}

// A connector open to everyone is not asked about groups at all, so a
// deployment that configures none never pays for a lookup.
func TestConnectorGroupCheckNeeded(t *testing.T) {
	id := connectors.MustParse("builtin:gdrive")
	if connectorGroupCheckNeeded(connectors.Definition{ID: id, Groups: []string{"*"}}) {
		t.Error("a wildcard connector asked about groups")
	}
	if !connectorGroupCheckNeeded(connectors.Definition{ID: id, Groups: []string{"lab"}}) {
		t.Error("a restricted connector did not ask about groups")
	}
	// Permits returns false for an empty group list, so a connector
	// with none configured is usable by nobody -- and the check has to
	// happen for that to be what occurs.
	if !connectorGroupCheckNeeded(connectors.Definition{ID: id}) {
		t.Error("a connector with no groups skipped the check, so it would be usable by anyone")
	}
}

// Whether a change-stream event about an owner's jobs reaches a binding.
//
// This is the test for the failure that hides: the stream carries only
// the job ad's Owner (the local part), bindings are keyed by the
// session's subject (which may be qualified), and a placementd binding
// is scoped by User, which the event does not carry at all. A strict
// comparison never fires for any of those -- and because the periodic
// sweep covers them, it looks exactly like the feature working, just
// always at sweep latency. Nothing else in the suite would notice.
func TestBindingMatchesOwner(t *testing.T) {
	local := connectors.OutputBinding{Subject: "alice"}
	qualified := connectors.OutputBinding{Subject: "alice@example.org"}
	placement := connectors.OutputBinding{Subject: "alice@idp.example", UserScoped: true}

	for _, tc := range []struct {
		name    string
		b       connectors.OutputBinding
		evOwner string
		want    bool
	}{
		{"local subject, local owner", local, "alice", true},
		{"qualified subject, local owner", qualified, "alice", true},
		{"placementd subject, local owner", placement, "alice", true},
		{"qualified subject, qualified owner", qualified, "alice@example.org", true},
		// Case is not identity on a Unix account, but a schedd and a
		// session can disagree about it, and a missed nudge is the more
		// expensive mistake here.
		{"case differs", qualified, "ALICE", true},

		{"somebody else", qualified, "bob", false},
		{"a prefix is not a match", qualified, "alic", false},
		{"a longer name is not a match", local, "alice2", false},
		// An event with no owner cannot be routed anywhere. The nudger
		// drops these before asking, but the match must not claim them.
		{"no owner", qualified, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bindingMatchesOwner(tc.b, tc.evOwner); got != tc.want {
				t.Errorf("bindingMatchesOwner(%q, %q) = %v, want %v",
					tc.b.Subject, tc.evOwner, got, tc.want)
			}
		})
	}
}

// The owner derivation the nudge match uses is the same one the queue
// constraint uses. Two derivations would be two things that can
// disagree, and the way a disagreement shows is a nudge that never
// fires while the sweep quietly covers for it.
func TestBindingOwnerAttrMatchesTheConstraint(t *testing.T) {
	for _, b := range []connectors.OutputBinding{
		{Subject: "alice", Cluster: 1},
		{Subject: "alice@example.org", Cluster: 1},
		{Subject: "alice@idp.example", Cluster: 1, UserScoped: true},
	} {
		attr, owner := bindingOwnerAttr(b)
		got, err := outputClusterConstraint(b)
		if err != nil {
			t.Fatalf("outputClusterConstraint(%+v): %v", b, err)
		}
		want := attr + " == " + ownerscope.StringLit(owner)
		if !strings.Contains(got, want) {
			t.Errorf("constraint for %+v is %q, which does not carry %q", b, got, want)
		}
		// And the derivation it reports is one the match accepts, or the
		// nudge could never fire for a binding the constraint can read.
		if !bindingMatchesOwner(b, owner) {
			t.Errorf("%+v is scoped to %q but the nudge match rejects that owner", b, owner)
		}
	}
}

// The nudge's entry point sweeps only the bindings belonging to the
// owner whose job moved, and finds them through the generous match
// rather than through a string equality the subject may not satisfy.
//
// The schedd is unreachable here, so sweeping fails per binding; what is
// under test is WHICH bindings it reaches, which the count reports and
// which a failing schedd does not change.
func TestSweepOwnerOutputSelectsByOwner(t *testing.T) {
	db := openTestAppDB(t)
	h := &Handler{logger: testLogger(t), db: db}
	h.connectorStored = connectors.NewStore(db, nil)

	put := func(subject string, cluster int, userScoped bool) {
		t.Helper()
		err := h.connectorStored.PutOutputBinding(t.Context(), connectors.OutputBinding{
			Subject:    subject,
			Cluster:    cluster,
			Connector:  connectors.MustParse("builtin:gdrive"),
			FileID:     "SHEET",
			Tab:        "results",
			JobFile:    "result.json",
			UserScoped: userScoped,
		})
		if err != nil {
			t.Fatalf("PutOutputBinding(%s/%d): %v", subject, cluster, err)
		}
	}
	// Three of alice's, spelled the three ways a subject can be, and one
	// of bob's that must never be swept for alice.
	put("alice", 1, false)
	put("alice@example.org", 2, false)
	put("alice@idp.example", 3, true)
	put("bob", 4, false)

	for _, tc := range []struct {
		owner string
		want  int
	}{
		{"alice", 3},
		{"bob", 1},
		{"carol", 0},
		// An event with no owner reaches nothing: the nudger drops these
		// before asking, and this is the second line of that defence.
		{"", 0},
	} {
		t.Run("owner="+tc.owner, func(t *testing.T) {
			got, err := h.sweepOwnerOutput(t.Context(), tc.owner)
			if err != nil {
				t.Fatalf("sweepOwnerOutput(%q): %v", tc.owner, err)
			}
			if got != tc.want {
				t.Errorf("swept %d bindings for %q, want %d", got, tc.owner, tc.want)
			}
		})
	}
}

// With no store there is nothing to sweep and nothing to fail about: the
// nudger must not be handed an error for a server that simply has this
// feature switched off.
func TestSweepOwnerOutputWithoutAStore(t *testing.T) {
	h := &Handler{logger: testLogger(t)}
	got, err := h.sweepOwnerOutput(t.Context(), "alice")
	if err != nil || got != 0 {
		t.Errorf("= (%d, %v), want (0, nil)", got, err)
	}
}

// The nudge does not start where it cannot work, and says nothing about
// a feature nobody turned on.
func TestOutputNudgeStartConditions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		set      func(*Handler)
		wantSaid bool
	}{
		{
			name: "off by default",
			set:  func(*Handler) {},
		},
		{
			// No mirror, no change stream. The periodic sweep is then
			// the whole mechanism -- the same degradation job watches
			// and webhooks take.
			name: "enabled but no feed",
			set: func(h *Handler) {
				h.connectorOutputSweepInterval = time.Minute
				h.connectorStored = connectors.NewStore(h.db, nil)
			},
		},
		{
			name: "enabled with a feed",
			set: func(h *Handler) {
				h.connectorOutputSweepInterval = time.Minute
				h.jobWatchFeed = jobwatch.NewFeed(nil)
				h.connectorStored = connectors.NewStore(h.db, nil)
			},
			wantSaid: true,
		},
		{
			// scheddFor fails closed in multi-AP mode, so neither path
			// runs there.
			name: "multi-AP",
			set: func(h *Handler) {
				h.connectorOutputSweepInterval = time.Minute
				h.jobWatchFeed = jobwatch.NewFeed(nil)
				h.multi = &multiAPMode{}
				h.connectorStored = connectors.NewStore(h.db, nil)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, logged := sweepStartHandler(t)
			h.db = openTestAppDB(t)
			tc.set(h)

			ctx, cancel := context.WithCancel(t.Context())
			h.startConnectorOutputNudge(ctx)
			cancel()

			done := make(chan struct{})
			go func() { h.wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the nudge goroutine did not stop when its context was cancelled")
			}

			said := strings.Contains(logged(), "as jobs finish")
			if said != tc.wantSaid {
				t.Errorf("started=%v, want %v; log: %s", said, tc.wantSaid, logged())
			}
		})
	}
}
