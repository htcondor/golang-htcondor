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
