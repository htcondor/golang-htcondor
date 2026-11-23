//go:build integration

package httpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PelicanPlatform/classad/classad"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/connectors"
	"github.com/htcondor/golang-htcondor/webapi/connectors/mockgoogle"
)

// The whole round trip: submit with a nominated spreadsheet, let the
// jobs actually run, sweep, and read the rows back.
//
// Every piece of this is covered on its own -- the parser, the fitting,
// the at-most-once accounting, the owner-scoped query -- and the seam
// between them is not. What only a real schedd can show:
//
//   - that a job's declared results file is where the sweep looks for
//     it, which depends on spooled output landing somewhere
//     ReceiveJobSandbox can read;
//   - that a completed job is still in the queue when the sweep asks,
//     which depends on SubmitRemote's LeaveJobInQueue and would fail
//     silently if that ever changed;
//   - that a token minted for the job's owner, rather than this
//     daemon's own credential, is accepted by the schedd for both the
//     query and the sandbox read.
//
// A unit test can be wrong about all three and still pass.

// writeResultScript puts a script in the test's own directory that
// writes one JSON record, and returns its path.
//
// A script rather than `/bin/sh -c`, because the quoting needed to get
// a JSON document through a submit file's arguments is its own source
// of failure and is not what this test is about.
func writeResultScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "emit.sh")
	body := "#!/bin/sh\n" +
		"printf '{\"sample\":\"%s\",\"value\":\"%s\"}\\n' \"$1\" \"$2\" > results.json\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // a test script that must be executable
		t.Fatal(err)
	}
	return path
}

// A batch's results reach the tab its submission nominated, fitted to
// that tab's own header row.
func TestResultsReachTheSpreadsheetIntegration(t *testing.T) {
	fix := setupConnectorFixture(t)
	// A header row with the columns in an order the jobs do not use, so
	// a test that passed by appending fields in emission order would
	// fail. "note" is a column no job produces and must stay blank.
	fix.google.PickSheet("SHEET", "results.gsheet", mockgoogle.MockTab{
		Title: "results",
		Rows:  [][]string{{"value", "note", "sample"}},
	})

	script := writeResultScript(t)
	submitFile := strings.Join([]string{
		"executable = " + script,
		// False, not true: an absolute path with transfer_executable on
		// makes the schedd await a spooled executable that nothing is
		// going to upload, and the jobs sit in the spooling hold. The
		// harness runs the startd on this machine, so the script is
		// already where the job needs it.
		"transfer_executable = false",
		"arguments = $(sample) $(value)",
		"should_transfer_files = YES",
		"when_to_transfer_output = ON_EXIT",
		"transfer_output_files = results.json",
		// Per-proc, or both jobs write the same file and only the last
		// to finish survives.
		"output = run-$(ProcId).out",
		"error = run-$(ProcId).err",
		"log = run.log",
		"queue sample, value from (",
		"HG002, 7",
		"HG003, 9",
		")",
		"",
	}, "\n")

	cluster := fix.submitWith(t, JobSubmitRequest{
		SubmitFile: submitFile,
		OutputSheet: &SubmitOutputSheet{
			Connector: "builtin:gdrive",
			FileID:    "SHEET",
			Tab:       "results",
			JobFile:   "results.json",
		},
	})

	fix.awaitCompleted(t, cluster, 2)

	// One pass, driven here rather than by the ticker, so the test does
	// not depend on when a timer happened to fire.
	//
	// Per binding rather than Sweep(), so a failure arrives as this
	// test's error message instead of a line in a log nobody reads.
	// Sweep's own job -- walking the bindings and not letting one
	// failure stop the others -- is covered by its unit tests.
	if swept := fix.sweepOnce(t); swept != 1 {
		t.Fatalf("the submission recorded %d open bindings, want exactly one", swept)
	}

	rows := fix.google.SheetRows("SHEET", "results")
	if len(rows) != 3 {
		t.Fatalf("the tab has %d rows, want the header and two results: %v", len(rows), rows)
	}
	// In proc order, and in the SHEET's column order -- value, note,
	// sample -- not the order the jobs emitted their fields in.
	want := [][]string{
		{"value", "note", "sample"},
		{"7", "", "HG002"},
		{"9", "", "HG003"},
	}
	for i, w := range want {
		if len(rows[i]) != len(w) {
			t.Errorf("row %d has %d cells, want %d: %v", i, len(rows[i]), len(w), rows[i])
			continue
		}
		for j := range w {
			if rows[i][j] != w[j] {
				t.Errorf("row %d column %q = %q, want %q", i, want[0][j], rows[i][j], w[j])
			}
		}
	}

	// The binding is retired: every job is accounted for and none can
	// still arrive, so there is nothing left to ask the queue about.
	// Without this the sweep would re-read a finished cluster every
	// pass for the ten days it stays in the queue.
	store := fix.server.connectorStore()
	if _, err := store.OutputBindingFor(fix.ctx, localOwner(t), "", cluster); err == nil {
		t.Error("the binding stayed open after every job was accounted for")
	}

	// A second pass over the SAME cluster must add nothing.
	//
	// Re-nominating the destination is what makes this check real.
	// Sweeping again after the retirement would be vacuous -- there
	// would be no open binding to sweep, and the test would pass
	// whether or not the accounting worked. Putting the binding back
	// (which un-retires it, as a resubmission would) gives the sweep
	// the same cluster with both jobs already recorded, which is
	// exactly the state a crash between appending and marking would
	// leave. values.append has no idempotency key, so the recorded
	// jobs are the only thing standing between this and two copies of
	// every result.
	if err := store.PutOutputBinding(fix.ctx, connectors.OutputBinding{
		Subject:   localOwner(t),
		Cluster:   cluster,
		Connector: connectors.MustParse("builtin:gdrive"),
		FileID:    "SHEET",
		Tab:       "results",
		JobFile:   "results.json",
	}); err != nil {
		t.Fatalf("re-nominating the destination: %v", err)
	}
	swept := fix.sweepOnce(t)
	if swept != 1 {
		t.Fatalf("the re-nominated binding was not swept (%d swept)", swept)
	}
	if again := fix.google.SheetRows("SHEET", "results"); len(again) != 3 {
		t.Errorf("a second sweep made it %d rows; both jobs were already accounted for: %v",
			len(again), again)
	}
}

// A submission naming a results file the jobs do not transfer back is
// refused, before anything is queued.
//
// The submit-time check is the only moment anyone is watching: let this
// through and every job in the batch is accounted for with the same
// reason, which is a warning nobody can act on and a sandbox read per
// job per sweep.
func TestOutputSheetRefusesAFileThatWillNotComeBackIntegration(t *testing.T) {
	fix := setupConnectorFixture(t)
	fix.google.PickSheet("SHEET", "results.gsheet", mockgoogle.MockTab{
		Title: "results",
		Rows:  [][]string{{"sample"}},
	})

	submitFile := strings.Join([]string{
		"executable = /bin/true",
		"transfer_executable = false",
		"should_transfer_files = YES",
		"when_to_transfer_output = ON_EXIT",
		// Names something else entirely.
		"transfer_output_files = run.out",
		"output = run.out",
		"error = run.err",
		"log = run.log",
		"queue",
		"",
	}, "\n")

	status, body := fix.submitExpectingFailure(t, JobSubmitRequest{
		SubmitFile: submitFile,
		OutputSheet: &SubmitOutputSheet{
			Connector: "builtin:gdrive",
			FileID:    "SHEET",
			Tab:       "results",
			JobFile:   "results.json",
		},
	})
	if status != 400 {
		t.Fatalf("submit status %d, want 400; body: %s", status, body)
	}
	if !strings.Contains(body, "results.json") {
		t.Errorf("the refusal does not name the file: %s", body)
	}
}

// A tab with no header row is refused too: the header is the contract,
// so there is nowhere for a result to go.
func TestOutputSheetRefusesATabWithNoHeaderIntegration(t *testing.T) {
	fix := setupConnectorFixture(t)
	fix.google.PickSheet("SHEET", "results.gsheet", mockgoogle.MockTab{
		Title: "results",
		Rows:  nil,
	})

	submitFile := strings.Join([]string{
		"executable = /bin/true",
		"transfer_executable = false",
		"should_transfer_files = YES",
		"when_to_transfer_output = ON_EXIT",
		"output = run.out",
		"error = run.err",
		"log = run.log",
		"queue",
		"",
	}, "\n")

	status, body := fix.submitExpectingFailure(t, JobSubmitRequest{
		SubmitFile: submitFile,
		OutputSheet: &SubmitOutputSheet{
			Connector: "builtin:gdrive",
			FileID:    "SHEET",
			Tab:       "results",
			JobFile:   "results.json",
		},
	})
	if status != 400 {
		t.Fatalf("submit status %d, want 400; body: %s", status, body)
	}
	if !strings.Contains(body, "header row") {
		t.Errorf("the refusal does not say what is missing: %s", body)
	}
}

// sweepOnce runs one pass over every open binding and returns how many
// it swept, failing the test with the reason if one could not be swept.
//
// Returns the count rather than asserting it: "no open binding" is the
// right answer after a submission is finished and the wrong one before,
// and only the caller knows which it is looking at.
func (f *connectorFixture) sweepOnce(t *testing.T) int {
	t.Helper()
	w := f.server.newOutputSweeper()
	bindings, err := w.store.OpenOutputBindings(f.ctx, 0)
	if err != nil {
		t.Fatalf("listing open bindings: %v", err)
	}
	for _, b := range bindings {
		if err := w.sweepBinding(f.ctx, b); err != nil {
			t.Fatalf("sweeping cluster %d: %v", b.Cluster, err)
		}
	}
	return len(bindings)
}

// awaitCompleted waits for every proc of a cluster to reach Completed
// and still be in the queue, which is what the sweep needs.
//
// Reports the last status seen rather than timing out silently: a job
// that never ran and a job that ran and left look the same from a bare
// deadline, and they are different faults.
func (f *connectorFixture) awaitCompleted(t *testing.T, cluster, procs int) {
	t.Helper()
	constraint := fmt.Sprintf("ClusterId == %d", cluster)
	var last string
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); {
		ads, _, err := f.schedd.QueryWithOptions(f.ctx, constraint, &htcondor.QueryOptions{
			Limit: -1,
			Projection: []string{"ClusterId", "ProcId", "JobStatus",
				"HoldReasonCode", "HoldReason", "ExitCode"},
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		done, status := 0, []string{}
		for _, ad := range ads {
			proc, _ := ad.EvaluateAttrInt("ProcId")
			st, _ := ad.EvaluateAttrInt("JobStatus")
			status = append(status, fmt.Sprintf("proc %d: JobStatus=%d%s", proc, st, holdNote(ad)))
			switch st {
			case 4:
				done++
			case 5:
				// A held job will not finish on its own, and waiting
				// out the deadline for it hides what went wrong.
				code, _ := ad.EvaluateAttrInt("HoldReasonCode")
				reason, _ := ad.EvaluateAttrString("HoldReason")
				if code != 16 {
					t.Fatalf("proc %d is held: HoldReasonCode=%d HoldReason=%q", proc, code, reason)
				}
			}
		}
		last = strings.Join(status, "; ")
		if done == procs {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("the jobs did not all complete and stay in the queue: %s "+
		"(a cluster that completed and LEFT means LeaveJobInQueue is no longer "+
		"keeping spooled jobs, which the sweep depends on)", last)
}

func holdNote(ad *classad.ClassAd) string {
	reason, ok := ad.EvaluateAttrString("HoldReason")
	if !ok || reason == "" {
		return ""
	}
	return " HoldReason=" + strconv.Quote(reason)
}

// submitExpectingFailure posts a submission that should be refused and
// returns the status and body, so a test can check which refusal it
// was rather than only that there was one.
func (f *connectorFixture) submitExpectingFailure(
	t *testing.T,
	sr JobSubmitRequest,
) (int, string) {
	t.Helper()
	body, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, f.base+"/api/v1/jobs",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusCreated {
		t.Fatalf("the submission was accepted; it should have been refused: %s", got)
	}
	return resp.StatusCode, string(got)
}
