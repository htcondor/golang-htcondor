package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/connectors"
	"github.com/htcondor/golang-htcondor/webapi/connectors/mockgoogle"
	"github.com/htcondor/golang-htcondor/webapi/httpserver/appdb"
)

// The sweep's decision per job -- append, account for, or leave alone --
// against a real store and a stand-in provider. The schedd and the
// sandbox are the injected seams, because what the queue said is an
// input to the decision rather than part of it.

type sweepFixture struct {
	g     *mockgoogle.Server
	store *connectors.Store
	w     *outputSweeper
	// state is what the queue will say.
	state clusterState
	// files is each proc's result file contents; a proc absent here has
	// no readable result.
	files map[int]string
	// reads counts result reads per proc, so a test can assert a job
	// was not read twice.
	reads map[int]int
	// appendFails makes the next append fail, for the retry path.
	appendFails bool
	// accessToken is a token the stand-in will accept.
	accessToken string
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	db, err := appdb.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("appdb.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := appdb.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	g := mockgoogle.New(t, mockgoogle.Config{
		ClientID:     "1234.apps.googleusercontent.com",
		ClientSecret: "s3cret",
		RedirectURI:  "http://example.test/cb",
	})

	f := &sweepFixture{
		g:           g,
		store:       connectors.NewStore(db, nil),
		files:       map[int]string{},
		reads:       map[int]int{},
		accessToken: g.MintAccessToken("alice", "https://www.googleapis.com/auth/drive.file"),
	}
	f.w = &outputSweeper{
		store:  f.store,
		sheets: &connectors.Sheets{BaseURL: g.SheetsBaseURL(), Sleep: func(time.Duration) {}},
		cluster: func(context.Context, connectors.OutputBinding) (clusterState, error) {
			return f.state, nil
		},
		result: func(_ context.Context, _ connectors.OutputBinding, proc int) (io.ReadCloser, error) {
			f.reads[proc]++
			body, ok := f.files[proc]
			if !ok {
				return nil, errors.New("no such file")
			}
			return io.NopCloser(strings.NewReader(body)), nil
		},
		token: func(context.Context, connectors.OutputBinding) (string, error) {
			if f.appendFails {
				// Rig the provider rather than the token: the append is
				// what has to fail, after the headers are read.
				f.g.FailNext(500, 500, 500, 500, 500)
				f.appendFails = false
			}
			return f.accessToken, nil
		},
	}
	return f
}

func (f *sweepFixture) bind(t *testing.T) connectors.OutputBinding {
	t.Helper()
	b := connectors.OutputBinding{
		Subject:   "alice",
		Cluster:   12,
		Connector: connectors.MustParse("builtin:gdrive"),
		FileID:    "SHEET",
		Tab:       "results",
		JobFile:   "result.json",
	}
	if err := f.store.PutOutputBinding(t.Context(), b); err != nil {
		t.Fatalf("PutOutputBinding: %v", err)
	}
	return b
}

func (f *sweepFixture) sheet(header ...string) {
	rows := [][]string{header}
	f.g.PickSheet("SHEET", "results.gsheet",
		mockgoogle.MockTab{Title: "results", Rows: rows})
}

// A finished job's records land under the sheet's own headings.
func TestSweepAppendsAFinishedJob(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample", "score")
	b := f.bind(t)
	f.state = clusterState{Completed: []int{0}, Present: true}
	f.files[0] = `{"sample":"HG002","score":0.91}`

	if err := f.w.sweepBinding(t.Context(), b); err != nil {
		t.Fatalf("sweepBinding: %v", err)
	}

	got := f.g.SheetRows("SHEET", "results")
	if len(got) != 2 {
		t.Fatalf("the tab has %d rows, want the header plus one: %v", len(got), got)
	}
	if strings.Join(got[1], ",") != "HG002,0.91" {
		t.Errorf("appended row = %v, want [HG002 0.91]", got[1])
	}
}

// A job already accounted for is not appended again. This is the whole
// at-most-once property: values.append has no idempotency key, so a
// second pass that re-read and re-appended would leave two identical
// rows nobody can tell apart.
func TestSweepDoesNotAppendTwice(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample")
	b := f.bind(t)
	f.state = clusterState{Completed: []int{0}, Present: true}
	f.files[0] = `{"sample":"a"}`

	for range 3 {
		if err := f.w.sweepBinding(t.Context(), b); err != nil {
			t.Fatalf("sweepBinding: %v", err)
		}
	}
	got := f.g.SheetRows("SHEET", "results")
	if len(got) != 2 {
		t.Errorf("three sweeps left %d rows, want the header plus one: %v", len(got), got)
	}
	if f.reads[0] != 1 {
		t.Errorf("the job's result was read %d times, want 1", f.reads[0])
	}
}

// A job that is still running is not touched. Only what the queue calls
// finished is read.
func TestSweepLeavesUnfinishedJobsAlone(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample")
	b := f.bind(t)
	f.state = clusterState{Completed: nil, Unfinished: 1, Present: true}
	f.files[0] = `{"sample":"a"}`

	if err := f.w.sweepBinding(t.Context(), b); err != nil {
		t.Fatalf("sweepBinding: %v", err)
	}
	if n := len(f.g.SheetRows("SHEET", "results")); n != 1 {
		t.Errorf("the tab has %d rows, want just the header", n)
	}
	if f.reads[0] != 0 {
		t.Error("an unfinished job's result was read")
	}
	// And the binding is still open: the jobs have not run yet.
	if _, err := f.store.OutputBindingFor(t.Context(), "alice", "", 12); err != nil {
		t.Errorf("the binding was retired while jobs were still to come: %v", err)
	}
}

// A job whose result file is missing or unreadable is accounted for
// with the reason, not retried for ever. It will not become readable.
func TestSweepAccountsForAnUnreadableResult(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample")
	b := f.bind(t)
	f.state = clusterState{Completed: []int{0, 1}, Present: true}
	f.files[0] = `{"sample":"a"}`
	// proc 1 has no file at all.

	if err := f.w.sweepBinding(t.Context(), b); err != nil {
		t.Fatalf("sweepBinding: %v", err)
	}
	done, err := f.store.AppendedJobs(t.Context(), "alice", "", 12)
	if err != nil {
		t.Fatal(err)
	}
	j, ok := done[1]
	if !ok {
		t.Fatal("the job with no result file is not accounted for, so it will be retried for ever")
	}
	if j.Rows != 0 || j.Note == "" {
		t.Errorf("job 1 = %+v, want zero rows and a reason", j)
	}
	if !strings.Contains(j.Note, "result.json") {
		t.Errorf("the reason does not name the file: %q", j.Note)
	}
	// The readable one still went in.
	if n := len(f.g.SheetRows("SHEET", "results")); n != 2 {
		t.Errorf("the tab has %d rows, want the header plus the one good job", n)
	}
}

// A failed append marks nothing, so the next sweep tries again. An
// append that failed added nothing, which is the one case where
// retrying cannot duplicate.
func TestSweepRetriesAFailedAppend(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample")
	b := f.bind(t)
	f.state = clusterState{Completed: []int{0}, Present: true}
	f.files[0] = `{"sample":"a"}`
	f.appendFails = true

	if err := f.w.sweepBinding(t.Context(), b); err == nil {
		t.Fatal("a failed append was reported as success")
	}
	done, err := f.store.AppendedJobs(t.Context(), "alice", "", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("a job was accounted for although its append failed: %+v", done)
	}

	// The next pass succeeds and the row lands exactly once.
	if err := f.w.sweepBinding(t.Context(), b); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if n := len(f.g.SheetRows("SHEET", "results")); n != 2 {
		t.Errorf("after the retry the tab has %d rows, want the header plus one", n)
	}
}

// A cluster that has left the queue will never produce another result,
// so its binding is finished -- which is what stops a reused cluster id
// from inheriting this destination.
func TestSweepRetiresAFinishedSubmission(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample")
	b := f.bind(t)
	f.state = clusterState{Completed: []int{0}, Present: false}
	f.files[0] = `{"sample":"a"}`

	if err := f.w.sweepBinding(t.Context(), b); err != nil {
		t.Fatalf("sweepBinding: %v", err)
	}
	if _, err := f.store.OutputBindingFor(t.Context(), "alice", "", 12); !errors.Is(err, connectors.ErrNoBinding) {
		t.Errorf("the binding outlived the submission: %v", err)
	}
}

// A submission whose jobs are all finished AND all accounted for is
// retired even though the cluster is still in the queue.
//
// Spooled jobs stay queued for ten days after they complete, so waiting
// for the cluster to leave would mean re-reading a finished
// submission's whole proc list every pass for ten days to learn
// nothing. For a batch of a hundred thousand that is the difference
// between one read and three thousand.
func TestSweepRetiresOnceEverythingIsAccountedFor(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample")
	b := f.bind(t)
	f.state = clusterState{Completed: []int{0}, Unfinished: 0, Present: true}
	f.files[0] = `{"sample":"a"}`

	if err := f.w.sweepBinding(t.Context(), b); err != nil {
		t.Fatalf("sweepBinding: %v", err)
	}
	if _, err := f.store.OutputBindingFor(t.Context(), "alice", "", 12); !errors.Is(err, connectors.ErrNoBinding) {
		t.Errorf("a finished submission's binding stayed open: %v", err)
	}
}

// A removed job is not waited for. It will never produce a result, and
// counting it as still to come would hold the binding open for the
// cluster's whole stay in the queue.
func TestSweepDoesNotWaitForRemovedJobs(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample")
	b := f.bind(t)
	// Proc 0 finished; proc 1 was removed, so the queue reports neither
	// a completion nor anything still to come.
	f.state = clusterState{Completed: []int{0}, Unfinished: 0, Present: true}
	f.files[0] = `{"sample":"a"}`

	if err := f.w.sweepBinding(t.Context(), b); err != nil {
		t.Fatalf("sweepBinding: %v", err)
	}
	if n := len(f.g.SheetRows("SHEET", "results")); n != 2 {
		t.Errorf("the tab has %d rows, want the header and one result", n)
	}
	if _, err := f.store.OutputBindingFor(t.Context(), "alice", "", 12); !errors.Is(err, connectors.ErrNoBinding) {
		t.Errorf("the binding waited for a removed job: %v", err)
	}
}

// A tab with no header row has nowhere to put a result. Nothing is
// accounted for, so adding a header makes the next sweep fill it --
// marking these jobs would be throwing their results away to stop a
// warning.
func TestSweepWaitsForAHeaderRow(t *testing.T) {
	f := newSweepFixture(t)
	f.g.PickSheet("SHEET", "results.gsheet", mockgoogle.MockTab{Title: "results"})
	b := f.bind(t)
	f.state = clusterState{Completed: []int{0}, Present: true}
	f.files[0] = `{"sample":"a"}`

	err := f.w.sweepBinding(t.Context(), b)
	if err == nil {
		t.Fatal("a tab with no header row was treated as ready")
	}
	// "no header row", not "header row": the read failure's own message
	// is "reading the header row of ...", so the looser check passed on
	// a 401 before the fixture minted a token the stand-in accepts.
	if !strings.Contains(err.Error(), "no header row") {
		t.Errorf("error = %v, want it to name the missing header row", err)
	}
	done, derr := f.store.AppendedJobs(t.Context(), "alice", "", 12)
	if derr != nil {
		t.Fatal(derr)
	}
	if len(done) != 0 {
		t.Errorf("a job was written off because the sheet had no header: %+v", done)
	}
}

// Fields the sheet has no column for are recorded against the job, so
// "a column of results went nowhere" is answerable afterwards rather
// than only visible in a log.
func TestSweepRecordsFieldsWithNoColumn(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample")
	b := f.bind(t)
	f.state = clusterState{Completed: []int{0}, Present: true}
	f.files[0] = `{"sample":"a","elapsed":"12s","host":"ep1"}`

	if err := f.w.sweepBinding(t.Context(), b); err != nil {
		t.Fatalf("sweepBinding: %v", err)
	}
	done, err := f.store.AppendedJobs(t.Context(), "alice", "", 12)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(done[0].Note, "elapsed") || !strings.Contains(done[0].Note, "host") {
		t.Errorf("note = %q, want it to name the fields with no column", done[0].Note)
	}
}

// Jobs go in in proc order, so a spreadsheet read top to bottom follows
// the submission rather than whatever order a query returned.
func TestSweepAppendsInProcOrder(t *testing.T) {
	f := newSweepFixture(t)
	f.sheet("sample")
	b := f.bind(t)
	f.state = clusterState{Completed: []int{2, 0, 1}, Present: true}
	for i := range 3 {
		f.files[i] = fmt.Sprintf(`{"sample":"p%d"}`, i)
	}

	if err := f.w.sweepBinding(t.Context(), b); err != nil {
		t.Fatalf("sweepBinding: %v", err)
	}
	got := f.g.SheetRows("SHEET", "results")
	want := []string{"sample", "p0", "p1", "p2"}
	if len(got) != len(want) {
		t.Fatalf("the tab has %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i][0] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i][0], want[i])
		}
	}
}
