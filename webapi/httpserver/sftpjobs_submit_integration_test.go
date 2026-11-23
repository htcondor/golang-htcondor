//go:build integration

// Copyright 2026 Morgridge Institute for Research
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package httpserver

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/idempotency"
	"github.com/htcondor/golang-htcondor/webapi/sftpjobs"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// What a dropped CSV does to a real schedd, for the one case no unit
// test can answer: whether the template's own input files actually
// reach the job.
//
// The unit tests settle which names a spool may carry and what is
// refused. What they cannot settle is the part that only a schedd
// knows -- that a submission held on HoldReasonCode 16 is released by
// the spool rather than by something else, that a file spooled out of
// an in-memory filesystem lands in the sandbox under the name
// transfer_input_files asked for, and that the job then runs it. Each
// of those failed silently in a way that looks like success from
// inside this process: the submit returns a cluster id either way.

// sftpSubmitEnv is one pool, shared by the subtests. Standing up a
// second HTCondor for the withdrawal case would double the wall clock
// and cover nothing the first does not.
type sftpSubmitEnv struct {
	schedd  *htcondor.Schedd
	account string
	ctx     context.Context
	sub     templateSubmitter
	harness *htcondor.CondorTestHarness
}

func setupSFTPSubmit(t *testing.T) *sftpSubmitEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test (forks a real HTCondor)")
	}
	if _, err := exec.LookPath("condor_master"); err != nil {
		t.Skip("condor_master not in PATH")
	}

	harness := htcondor.SetupCondorHarness(t)
	if err := harness.WaitForDaemons(); err != nil {
		t.Fatalf("daemons failed to start: %v", err)
	}
	if err := harness.WaitForStartd(45 * time.Second); err != nil {
		t.Fatalf("startd never reported in: %v", err)
	}

	collector := htcondor.NewCollector(harness.GetCollectorAddr())
	loc, err := collector.LocateDaemon(context.Background(), "Schedd", "")
	if err != nil {
		t.Fatalf("locating the schedd: %v", err)
	}
	schedd := htcondor.NewSchedd(loc.Name, loc.Address)

	// Submitted as the user running the test, which is also who the
	// pool authenticates: the gateway's own account check compares the
	// context's authenticated user with the account it was asked to
	// submit for, so the two have to be the same name here.
	account := currentUser(t)
	ctx := htcondor.WithAuthenticatedUser(context.Background(), account)

	return &sftpSubmitEnv{
		schedd:  schedd,
		account: account,
		ctx:     ctx,
		sub:     templateSubmitter{h: &Handler{logger: testLogger(t), schedd: schedd}},
		harness: harness,
	}
}

// wrapperTemplateIntegration ships the script it runs, which is the
// shape templates.InputFiles exists for: a few lines of shell around a
// program the execute node already has.
//
// The script's exit code is the assertion. 17 is reached only by
// running the file that was spooled; a sandbox without it leaves
// /bin/sh to exit 127, and a file spooled empty or under the wrong name
// exits 0 or 126. One number separates "delivered and ran" from every
// way of not delivering it.
func wrapperTemplateIntegration() templates.Template {
	return templates.Template{
		ID:          "spooled-wrapper",
		Name:        "A template that ships its own script",
		Description: "Runs a wrapper the library stores with the template.",
		Columns:     []templates.Column{{Name: "sample", Description: "the sample's name"}},
		Contents: "universe = vanilla\n" +
			"executable = /bin/sh\n" +
			"transfer_executable = false\n" +
			"transfer_input_files = run.sh\n" +
			"arguments = run.sh $(sample)\n" +
			"output = out.$(sample)\n" +
			"error = err.$(sample)\n" +
			"request_cpus = 1\n" +
			"request_memory = 64\n" +
			"request_disk = 64\n",
		InputFiles: []templates.InputFile{{
			Name:    "run.sh",
			Content: []byte("#!/bin/sh\necho \"ran for $1\"\nexit 17\n"),
		}},
		Source: templates.SourceBuiltin,
	}
}

func TestTemplateInputFilesReachTheJob(t *testing.T) {
	t.Parallel()
	env := setupSFTPSubmit(t)

	t.Run("the shipped script runs", func(t *testing.T) {
		tmpl := wrapperTemplateIntegration()
		if err := sftpjobs.ValidateTemplate(tmpl); err != nil {
			t.Fatalf("the template is refused before any schedd is involved: %v", err)
		}

		// Through ParseCSV, so the rows are the ones a dropped file
		// would really produce.
		sub, err := sftpjobs.ParseCSV(strings.NewReader("sample\nHG001\nHG002\n"), sftpjobs.CSVLimits{})
		if err != nil {
			t.Fatalf("ParseCSV: %v", err)
		}

		ctx, cancel := context.WithTimeout(env.ctx, 5*time.Minute)
		defer cancel()

		cluster, err := env.sub.SubmitRows(ctx, env.account, tmpl, sub)
		if err != nil {
			env.harness.PrintScheddLog()
			t.Fatalf("SubmitRows: %v", err)
		}
		t.Logf("cluster %d, 2 procs", cluster)
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = env.schedd.RemoveJobs(c, fmt.Sprintf("ClusterId == %d", cluster), "test cleanup")
		}()

		// First the hold: a spool that did not happen leaves every proc
		// on HoldReasonCode 16, which is the failure this whole path
		// exists to avoid and the one that looks most like success.
		waitNoneHeld(t, ctx, env.schedd, cluster)

		// Then the exit code, from both procs.
		waitAllExited(t, ctx, env.schedd, cluster, 2, 17)
	})

	// The other half: a row that names a file nobody can deliver. The
	// template passes validation -- transfer_input_files is a column
	// there, and a column could name a URL -- so the cluster is already
	// in the queue when the spool is planned, and it has to be taken
	// back out. Left behind, those jobs sit on HoldReasonCode 16
	// forever while the person who dropped the CSV has been told the
	// submission failed.
	t.Run("an undeliverable row withdraws the cluster", func(t *testing.T) {
		tmpl := wrapperTemplateIntegration()
		tmpl.ID = "column-input"
		tmpl.Columns = append(tmpl.Columns, templates.Column{Name: "extra"})
		tmpl.Contents = strings.Replace(tmpl.Contents,
			"transfer_input_files = run.sh", "transfer_input_files = run.sh, $(extra)", 1)
		if err := sftpjobs.ValidateTemplate(tmpl); err != nil {
			t.Fatalf("a template whose input is a column was refused: %v", err)
		}

		sub, err := sftpjobs.ParseCSV(
			strings.NewReader("sample,extra\nHG001,nobody-ships-this.dat\n"), sftpjobs.CSVLimits{})
		if err != nil {
			t.Fatalf("ParseCSV: %v", err)
		}

		ctx, cancel := context.WithTimeout(env.ctx, 2*time.Minute)
		defer cancel()

		before := clusterIDs(t, ctx, env.schedd)
		_, err = env.sub.SubmitRows(ctx, env.account, tmpl, sub)
		if err == nil {
			t.Fatal("a row naming an undeliverable file was submitted")
		}
		if !strings.Contains(err.Error(), "nobody-ships-this.dat") {
			t.Errorf("error = %v, want it to name the file", err)
		}
		if !strings.Contains(err.Error(), "withdrawn") {
			t.Errorf("error = %v, want it to say the jobs were withdrawn", err)
		}

		// Nothing new left in the queue. Polled, because the removal is
		// a separate schedd transaction from the error this test
		// already has in hand.
		deadline := time.Now().Add(60 * time.Second)
		for {
			leftover := newClusters(before, clusterIDs(t, ctx, env.schedd))
			if len(leftover) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("clusters %v were left in the queue, held for input that is not coming", leftover)
			}
			time.Sleep(time.Second)
		}
	})
}

// waitAllExited holds a cluster to the bar that matters for a spooled
// input file: every proc finished, and finished by running the file.
func waitAllExited(t *testing.T, ctx context.Context, schedd *htcondor.Schedd,
	cluster, procs, wantExit int) {
	t.Helper()
	constraint := fmt.Sprintf("ClusterId == %d", cluster)
	projection := []string{"ProcId", "JobStatus", "ExitCode", "HoldReason", "HoldReasonCode"}

	for deadline := time.Now().Add(4 * time.Minute); time.Now().Before(deadline); {
		ads, _, err := schedd.QueryWithOptions(ctx, constraint,
			&htcondor.QueryOptions{Projection: projection, Limit: -1})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		done := 0
		for _, ad := range ads {
			proc, _ := ad.EvaluateAttrInt("ProcId")
			status, _ := ad.EvaluateAttrInt("JobStatus")
			switch status {
			case 5: // held, and a hold after spooling does not clear
				code, _ := ad.EvaluateAttrInt("HoldReasonCode")
				reason, _ := ad.EvaluateAttrString("HoldReason")
				t.Fatalf("%d.%d went on hold (%d): %s", cluster, proc, code, reason)
			case 4:
				code, has := ad.EvaluateAttrInt("ExitCode")
				if !has {
					t.Fatalf("%d.%d finished with no ExitCode", cluster, proc)
				}
				if int(code) != wantExit {
					// 127 is /bin/sh with no run.sh to read: the file
					// was never delivered.
					t.Fatalf("%d.%d exited %d, want %d (127 means the spooled script was not there)",
						cluster, proc, code, wantExit)
				}
				done++
			}
		}
		if done == procs {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("cluster %d did not finish its %d procs", cluster, procs)
}

// clusterIDs is every cluster in the queue, for before-and-after.
func clusterIDs(t *testing.T, ctx context.Context, schedd *htcondor.Schedd) map[int]bool {
	t.Helper()
	ads, _, err := schedd.QueryWithOptions(ctx, "true",
		&htcondor.QueryOptions{Projection: []string{"ClusterId"}, Limit: -1})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	out := map[int]bool{}
	for _, ad := range ads {
		if id, ok := ad.EvaluateAttrInt("ClusterId"); ok {
			out[int(id)] = true
		}
	}
	return out
}

func newClusters(before, after map[int]bool) []int {
	var out []int
	for id := range after {
		if !before[id] {
			out = append(out, id)
		}
	}
	return out
}

// archiveSource is a dropped archive as the submitter consumes it:
// readable once per proc. The real one is sftpjobs' own sink, which
// spills to the operator's buffer directory; here the bytes are in
// hand, and what matters is that every reader starts at the beginning.
type archiveSource []byte

func (a archiveSource) Reader() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(a)), nil
}
func (a archiveSource) Size() int64  { return int64(len(a)) }
func (a archiveSource) Close() error { return nil }

// tarArchive packs entries the way `tar cf` would.
func tarArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		mode := int64(0o600)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o700
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	return buf.Bytes()
}

// What a dropped archive does to a real schedd.
//
// Two things here that no unit test can answer, and that the template
// path cannot do at all:
//
//   - The archive carries the EXECUTABLE. transfer_executable is true,
//     so the file the job runs is one the client packed, spooled into
//     the sandbox and then executed -- proved by its own exit code.
//   - `queue 2` means the same archive is spooled TWICE. HTCondor
//     spools per job, and a flat tar serves one proc per call, so this
//     is the fan-out's contract: both procs run the file, or one of
//     them exits 127 having never received it.
func TestDroppedArchiveRunsItsOwnExecutable(t *testing.T) {
	t.Parallel()
	env := setupSFTPSubmit(t)

	t.Run("the packed executable runs on every proc", func(t *testing.T) {
		archive := tarArchive(t, map[string]string{
			"job.sub": "universe = vanilla\n" +
				"executable = run.sh\n" +
				"arguments = packed\n" +
				"output = out.$(ProcId)\n" +
				"error = err.$(ProcId)\n" +
				"request_cpus = 1\n" +
				"request_memory = 64\n" +
				"request_disk = 64\n" +
				"queue 2\n",
			// 23 is reached only by running the packed file: a sandbox
			// without it leaves the starter with nothing to execute.
			"run.sh": "#!/bin/sh\necho \"ran as $1\"\nexit 23\n",
		})

		sub := &sftpjobs.TarballSubmission{
			Submit: submitTextFrom(t, archive),
			Files:  []string{"job.sub", "run.sh"},
			Source: archiveSource(archive),
		}

		ctx, cancel := context.WithTimeout(env.ctx, 5*time.Minute)
		defer cancel()

		cluster, err := env.sub.SubmitTarball(ctx, env.account, sub)
		if err != nil {
			env.harness.PrintScheddLog()
			t.Fatalf("SubmitTarball: %v", err)
		}
		t.Logf("cluster %d, 2 procs", cluster)
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = env.schedd.RemoveJobs(c, fmt.Sprintf("ClusterId == %d", cluster), "test cleanup")
		}()

		waitNoneHeld(t, ctx, env.schedd, cluster)
		waitAllExited(t, ctx, env.schedd, cluster, 2, 23)
	})

	// A queue item that names a file the archive does not carry. The
	// pre-submit check cannot see it -- $(extra) names nothing until
	// the item is substituted -- so the cluster is already in the queue
	// when the gap is found, and has to come back out. Left behind, the
	// procs would sit on HoldReasonCode 16 for an upload that is gone.
	//
	// This also pins the inline queue form working at all: a dropped
	// submit file is parsed without access to this host, so the
	// from-a-file form is refused and ((rows)) is what is left.
	t.Run("an item naming a file not in the archive withdraws the cluster", func(t *testing.T) {
		archive := tarArchive(t, map[string]string{
			"job.sub": "universe = vanilla\n" +
				"executable = /bin/true\n" +
				"transfer_executable = false\n" +
				"transfer_input_files = $(extra)\n" +
				"request_memory = 64\n" +
				"queue extra from (\nnobody-ships-this.dat\n)\n",
		})
		sub := &sftpjobs.TarballSubmission{
			Submit: submitTextFrom(t, archive),
			Files:  []string{"job.sub"},
			Source: archiveSource(archive),
		}

		ctx, cancel := context.WithTimeout(env.ctx, 2*time.Minute)
		defer cancel()

		before := clusterIDs(t, ctx, env.schedd)
		if _, err := env.sub.SubmitTarball(ctx, env.account, sub); err == nil {
			t.Fatal("an item naming a file the archive does not carry was submitted")
		} else if !strings.Contains(err.Error(), "nobody-ships-this.dat") {
			t.Errorf("error = %v, want it to name the file", err)
		} else if !strings.Contains(err.Error(), "withdrawn") {
			t.Errorf("error = %v, want it to say the jobs were withdrawn", err)
		}

		deadline := time.Now().Add(60 * time.Second)
		for {
			leftover := newClusters(before, clusterIDs(t, ctx, env.schedd))
			if len(leftover) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("clusters %v were left in the queue, held for input that is gone", leftover)
			}
			time.Sleep(time.Second)
		}
	})
}

// submitTextFrom reads job.sub back out of an archive, so the text the
// submitter is given is the one the client packed rather than a second
// copy this test keeps in step by hand.
func submitTextFrom(t *testing.T, archive []byte) string {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("reading the archive: %v", err)
		}
		if h.Name == "job.sub" {
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("reading job.sub: %v", err)
			}
			return string(body)
		}
	}
	t.Fatal("the test archive has no job.sub in it")
	return ""
}

// TestADroppedFileGoesThroughTheIdempotencyGuard.
//
// The file service submits through submitJob, the door every other
// surface uses, rather than calling SubmitRemote itself -- and the
// reason to pin that with a test rather than a comment is that it was
// wrong: these two paths called SubmitRemote directly, which skipped
// the site's submit policy, the credentials a job needs, the access
// point's request defaults and this guard, all four invisibly.
//
// A key on the context is how every surface carries one (the REST
// header and the MCP argument both land there), so a key seen twice
// must be refused the second time, naming the job that used it.
// ModeQueue, because the live queue is all this needs: no mirror, no
// history.
func TestADroppedFileGoesThroughTheIdempotencyGuard(t *testing.T) {
	t.Parallel()
	env := setupSFTPSubmit(t)

	h := &Handler{logger: testLogger(t), schedd: env.schedd}
	h.idempotency = idempotency.NewGuard(
		idempotency.Config{Mode: idempotency.ModeQueue}, idemLookups{h: h})
	sub := templateSubmitter{h: h}

	tmpl := wrapperTemplateIntegration()
	rows, err := sftpjobs.ParseCSV(strings.NewReader("sample\nHG001\n"), sftpjobs.CSVLimits{})
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}

	ctx, cancel := context.WithTimeout(env.ctx, 3*time.Minute)
	defer cancel()
	keyed := idempotency.WithKey(ctx, "sftp-drop-"+fmt.Sprint(time.Now().UnixNano()))

	cluster, err := sub.SubmitRows(keyed, env.account, tmpl, rows)
	if err != nil {
		env.harness.PrintScheddLog()
		t.Fatalf("the first keyed submission failed: %v", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = env.schedd.RemoveJobs(c, fmt.Sprintf("ClusterId == %d", cluster), "test cleanup")
	}()

	// The key is stamped on the job, which is what makes it findable --
	// and what job.ad shows somebody debugging a refusal.
	ads, _, err := env.schedd.QueryWithOptions(ctx, fmt.Sprintf("ClusterId == %d", cluster),
		&htcondor.QueryOptions{Projection: []string{"ClusterId", idempotency.Attr}, Limit: -1})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(ads) == 0 {
		t.Fatal("the submitted cluster is not in the queue")
	}
	if got, ok := ads[0].EvaluateAttrString(idempotency.Attr); !ok || got == "" {
		t.Errorf("%s is not on the job, so nothing could match a retry", idempotency.Attr)
	}

	// The same key again, with a different CSV so nothing else could
	// account for the refusal.
	other, err := sftpjobs.ParseCSV(strings.NewReader("sample\nHG002\n"), sftpjobs.CSVLimits{})
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	_, err = sub.SubmitRows(keyed, env.account, tmpl, other)
	if err == nil {
		t.Fatal("a second submission with the same key was accepted")
	}
	if !strings.Contains(err.Error(), fmt.Sprint(cluster)) {
		t.Errorf("error = %v, want it to name job %d, which used the key", err, cluster)
	}

	// And a dropped archive goes through the same door.
	archive := tarArchive(t, map[string]string{
		"job.sub": "universe = vanilla\nexecutable = /bin/true\n" +
			"transfer_executable = false\nrequest_memory = 64\nqueue\n",
	})
	_, err = sub.SubmitTarball(keyed, env.account, &sftpjobs.TarballSubmission{
		Submit: submitTextFrom(t, archive),
		Files:  []string{"job.sub"},
		Source: archiveSource(archive),
	})
	if err == nil {
		t.Fatal("a dropped archive reusing the key was accepted")
	}
	if !strings.Contains(err.Error(), fmt.Sprint(cluster)) {
		t.Errorf("error = %v, want it to name job %d", err, cluster)
	}
}
