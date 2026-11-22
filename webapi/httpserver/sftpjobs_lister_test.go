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
	"strings"
	"testing"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/sftpjobs"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// TestSFTPJobsConstraintIsTheQueuesCompletedJobs pins what this listing
// asks for, and the parenthesising that keeps a caller's scope inside
// the AND.
func TestSFTPJobsConstraintIsTheQueuesCompletedJobs(t *testing.T) {
	got := sftpJobsConstraint(`User == "alice"`)
	for _, want := range []string{`(User == "alice")`, "JobStatus == 4"} {
		if !strings.Contains(got, want) {
			t.Errorf("constraint %q does not contain %q", got, want)
		}
	}
	// A scope that would otherwise break out of the AND stays inside it.
	got = sftpJobsConstraint(`User == "a" || true`)
	if !strings.HasPrefix(got, `(User == "a" || true) &&`) {
		t.Errorf("constraint %q lets the scope escape the AND", got)
	}
}

// TestSFTPJobsFromRows: a row that names no job is skipped rather than
// failing the listing, and a job with no CompletionDate shows no time
// rather than 1970.
func TestSFTPJobsFromRows(t *testing.T) {
	rows := []string{
		`ClusterId = 12
ProcId = 0
CompletionDate = 1764000000`,
		`ClusterId = 7
ProcId = 3`, // accounted for later; no completion time yet
		`ClusterId = 9
ProcId = 1
CompletionDate = 0`, // present but unset
		`ProcId = 4`, // no cluster: not a job
		`this is not a classad at all`,
	}

	got := sftpJobsFromRows(rows)
	if len(got) != 3 {
		t.Fatalf("got %d jobs, want 3: %+v", len(got), got)
	}
	if got[0].Cluster != 12 || got[0].Proc != 0 {
		t.Errorf("first job = %d.%d, want 12.0", got[0].Cluster, got[0].Proc)
	}
	if got[0].Completed.IsZero() {
		t.Error("a job with a CompletionDate shows no time")
	}
	if got[0].Completed.Unix() != 1764000000 {
		t.Errorf("completion = %d, want 1764000000", got[0].Completed.Unix())
	}
	for _, j := range got[1:] {
		if !j.Completed.IsZero() {
			t.Errorf("job %d.%d has a completion time it was not given", j.Cluster, j.Proc)
		}
	}
}

// TestReadableJobsSaysWhyItCannotAnswer: "you have no jobs" and "this
// access point cannot tell you" are different answers, and showing an
// empty directory for the second is the one that misleads.
func TestReadableJobsSaysWhyItCannotAnswer(t *testing.T) {
	h := &Handler{logger: testLogger(t)} // no mirror configured
	l := mirrorJobLister{h: h, account: "alice"}

	jobs, err := l.ReadableJobs(context.Background())
	if err == nil {
		t.Fatal("a listing with no mirror configured succeeded")
	}
	if jobs != nil {
		t.Errorf("got %d jobs alongside the error", len(jobs))
	}
	if !strings.Contains(err.Error(), "mirror") {
		t.Errorf("error = %v, want it to name the mirror", err)
	}
}

// TestTheTemplatesTreeFollowsTheLibrary: the file service has no
// template configuration of its own, so an access point that offers
// templates on the web page offers the same ones here, and one with no
// library offers none.
//
// The submitter is deliberately not gated with it. A dropped archive
// carries its own submit description, so /submit needs a schedd and
// nothing else -- gating the submitter on the library would have taken
// the drop point away from every access point that curates no
// templates, which is most of them.
func TestTheTemplatesTreeFollowsTheLibrary(t *testing.T) {
	off := &Handler{logger: testLogger(t)}
	if off.jobTemplates() != nil {
		t.Error("templates are offered with no library built")
	}
	if off.jobSubmitter() == nil {
		t.Error("no submitter without a template library, so /submit would be absent")
	}
	lib, err := templates.NewLibrary(templates.LibraryConfig{})
	if err != nil {
		t.Fatalf("NewLibrary: %v", err)
	}
	on := &Handler{logger: testLogger(t), templateLibrary: lib}
	if on.jobTemplates() == nil || on.jobSubmitter() == nil {
		t.Error("a built library did not produce both halves")
	}
}

// TestTheArchiveLimitFollowsTheBufferDirectory: an archive with
// nowhere to spill is held in memory, so it must not be allowed the
// spool ceiling -- a gigabyte of resident upload per SSH channel.
func TestTheArchiveLimitFollowsTheBufferDirectory(t *testing.T) {
	inMemory := &Handler{logger: testLogger(t), spoolMaxUploadBytes: 1 << 30}
	if got := inMemory.archiveDropLimit(); got != 0 {
		t.Errorf("limit = %d with no buffer directory, want 0 so sftpjobs applies its own", got)
	}
	spilling := &Handler{logger: testLogger(t), spoolMaxUploadBytes: 1 << 30, spoolBufferDir: t.TempDir()}
	if got := spilling.archiveDropLimit(); got != 1<<30 {
		t.Errorf("limit = %d with a buffer directory, want the operator's spool ceiling", got)
	}
}

// TestTemplateSubmitterChecksTheAccount: the schedd attributes the jobs
// to whoever the context authenticated, so submitting as somebody else
// would charge them to the wrong person.
func TestTemplateSubmitterChecksTheAccount(t *testing.T) {
	s := templateSubmitter{h: &Handler{logger: testLogger(t)}}
	tmpl := templates.Template{
		ID:       "t",
		Columns:  []templates.Column{{Name: "x"}},
		Contents: "executable = /bin/true\ntransfer_executable = false\n",
	}
	sub := &sftpjobs.Submission{Vars: []string{"a"}, Rows: []string{"1"}}

	ctx := htcondor.WithAuthenticatedUser(context.Background(), "alice")
	if _, err := s.SubmitRows(ctx, "bob", tmpl, sub); err == nil {
		t.Error("submitted as bob on a context authenticated as alice")
	} else if !strings.Contains(err.Error(), "authenticated as") {
		t.Errorf("error = %v, want it to name the mismatch", err)
	}
}

// TestTemplateSubmitterRefusesABadTemplate: the validation runs before
// anything reaches the schedd, which is what keeps a template that would
// hold every job forever from being submitted once.
func TestTemplateSubmitterRefusesABadTemplate(t *testing.T) {
	s := templateSubmitter{h: &Handler{logger: testLogger(t)}}
	tmpl := templates.Template{
		ID:       "t",
		Columns:  []templates.Column{{Name: "x"}},
		Contents: "executable = /bin/true\n", // no transfer_executable = false
	}
	sub := &sftpjobs.Submission{Vars: []string{"a"}, Rows: []string{"1"}}
	ctx := htcondor.WithAuthenticatedUser(context.Background(), "alice")

	if _, err := s.SubmitRows(ctx, "alice", tmpl, sub); err == nil {
		t.Error("a template that would hold every job was submitted")
	} else if !strings.Contains(err.Error(), "transfer_executable") {
		t.Errorf("error = %v, want the template contract", err)
	}
}

// TestTemplateSubmitterRefusesAnOversizeSpool: every proc of the
// cluster is spooled the template's input files, so the transfer is the
// files times the rows -- which a CSV small enough to drop over SFTP
// can make very large. Refused before the submission, because the only
// other place to find out is after a cluster has been accepted.
//
// No schedd is configured here, and that is the assertion: the refusal
// lands before anything is dialed.
func TestTemplateSubmitterRefusesAnOversizeSpool(t *testing.T) {
	s := templateSubmitter{h: &Handler{logger: testLogger(t), spoolMaxUploadBytes: 1 << 20}}
	tmpl := templates.Template{
		ID:       "t",
		Columns:  []templates.Column{{Name: "x"}},
		Contents: "executable = /bin/true\ntransfer_executable = false\ntransfer_input_files = big.dat\n",
		InputFiles: []templates.InputFile{
			{Name: "big.dat", Content: make([]byte, 256<<10)}, // 256 KiB
		},
	}
	rows := make([]string, 64) // 64 x 256 KiB = 16 MiB, over the 1 MiB limit
	for i := range rows {
		rows[i] = "1"
	}
	sub := &sftpjobs.Submission{Vars: []string{"x"}, Rows: rows}
	ctx := htcondor.WithAuthenticatedUser(context.Background(), "alice")

	_, err := s.SubmitRows(ctx, "alice", tmpl, sub)
	if err == nil {
		t.Fatal("a submission that would spool 16 MiB under a 1 MiB limit was accepted")
	}
	if strings.Contains(err.Error(), "no schedd") {
		t.Fatalf("the volume check ran after the schedd lookup: %v", err)
	}
	if !strings.Contains(err.Error(), "fewer rows") {
		t.Errorf("error = %v, want it to say what to do instead", err)
	}

	// The same template with few enough rows gets past the check, and
	// then fails for the reason this test has deliberately left:
	// there is no schedd.
	sub.Rows = rows[:2]
	if _, err := s.SubmitRows(ctx, "alice", tmpl, sub); err == nil {
		t.Error("a submission with no schedd configured succeeded")
	} else if !strings.Contains(err.Error(), "no schedd") {
		t.Errorf("error = %v, want the schedd lookup to be what stopped it", err)
	}
}
