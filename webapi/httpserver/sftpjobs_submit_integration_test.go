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
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	htcondor "github.com/htcondor/golang-htcondor"
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
