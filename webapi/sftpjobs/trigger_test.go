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

package sftpjobs

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// recordingSubmitter stands in for the schedd.
type recordingSubmitter struct {
	mu      sync.Mutex
	calls   []*Submission
	tmpls   []templates.Template
	next    int
	failErr error
}

func (s *recordingSubmitter) SubmitRows(_ context.Context, _ string, t templates.Template, sub *Submission) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		return 0, s.failErr
	}
	s.calls = append(s.calls, sub)
	s.tmpls = append(s.tmpls, t)
	s.next += 100
	return s.next, nil
}

func (s *recordingSubmitter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *recordingSubmitter) last() (*Submission, templates.Template) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return nil, templates.Template{}
	}
	return s.calls[len(s.calls)-1], s.tmpls[len(s.tmpls)-1]
}

type triggerFS struct {
	*FS
	sub *recordingSubmitter
}

func newTriggerFS(t *testing.T) *triggerFS {
	t.Helper()
	store, err := sandboxfs.New(newFakeSandbox(nil), sandboxfs.Config{Logf: t.Logf})
	if err != nil {
		t.Fatalf("sandboxfs.New: %v", err)
	}
	sub := &recordingSubmitter{}
	f, err := New(Config{
		Store:     store,
		Account:   testAccount,
		Context:   htcondor.WithAuthenticatedUser(context.Background(), testAccount),
		Templates: fakeCatalog{public: []templates.Template{blastTemplate()}},
		Submitter: sub,
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &triggerFS{FS: f, sub: sub}
}

const blastDir = "/templates/blast"

// TestTemplateTreeShape walks the tree a client sees.
func TestTemplateTreeShape(t *testing.T) {
	f := newTriggerFS(t)

	if got := listNames(t, f.FS, "/"); len(got) != 2 {
		t.Errorf("/ = %v, want outputs and templates", got)
	}
	// One directory per template, named by its library id -- the same id
	// the web submit page and /api/v1/templates use.
	if got := listNames(t, f.FS, "/templates"); len(got) != 1 || got[0] != "blast/" {
		t.Errorf("/templates = %v, want [blast/]", got)
	}
	want := []string{"README", "job.sub", "status/", "submit/"}
	got := listNames(t, f.FS, blastDir)
	if len(got) != len(want) {
		t.Fatalf("template dir = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("template dir = %v, want %v", got, want)
		}
	}
	// submit/ is write-only, so it lists nothing.
	if got := listNames(t, f.FS, blastDir+"/submit"); len(got) != 0 {
		t.Errorf("submit/ = %v, want empty", got)
	}
}

func TestTemplateFilesAreReadable(t *testing.T) {
	f := newTriggerFS(t)
	for _, tc := range []struct{ name, wants string }{
		{"job.sub", "blastn"},
		{"README", "sample"},
	} {
		fi, err := statOne(t, f.FS, blastDir+"/"+tc.name)
		if err != nil {
			t.Fatalf("stat %s: %v", tc.name, err)
		}
		if fi.Size() == 0 {
			t.Errorf("%s stats as empty", tc.name)
		}
		ra, err := f.Fileread(&sftp.Request{Method: "Get", Filepath: blastDir + "/" + tc.name})
		if err != nil {
			t.Fatalf("read %s: %v", tc.name, err)
		}
		buf := make([]byte, fi.Size())
		if _, err := ra.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt %s: %v", tc.name, err)
		}
		if !strings.Contains(string(buf), tc.wants) {
			t.Errorf("%s = %q, want it to mention %q", tc.name, buf, tc.wants)
		}
	}
}

// writeCSV drives the handler the way the request server does: open,
// write, close. Close is the commit point.
func writeCSV(t *testing.T, f *FS, path, body string) error {
	t.Helper()
	w, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: path})
	if err != nil {
		return err
	}
	if _, err := w.WriteAt([]byte(body), 0); err != nil {
		return err
	}
	c, ok := w.(io.Closer)
	if !ok {
		t.Fatal("the upload is not an io.Closer, so it can never commit")
	}
	return c.Close()
}

// TestCSVTriggerSubmits is the feature: drop a CSV, get one job per row.
func TestCSVTriggerSubmits(t *testing.T) {
	f := newTriggerFS(t)
	csv := "sample,reads\nHG001,a.fastq\nHG002,b.fastq\nHG003,c.fastq\n"

	if err := writeCSV(t, f.FS, blastDir+"/submit/run1.csv", csv); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if f.sub.count() != 1 {
		t.Fatalf("submitted %d times, want 1", f.sub.count())
	}
	sub, tmpl := f.sub.last()
	if tmpl.ID != "blast" {
		t.Errorf("submitted against %q", tmpl.ID)
	}
	if len(sub.Vars) != 2 || sub.Vars[0] != "sample" {
		t.Errorf("vars = %v", sub.Vars)
	}
	if len(sub.Rows) != 3 {
		t.Errorf("rows = %v, want 3", sub.Rows)
	}

	// And the outcome is readable.
	st := readStatus(t, f.FS, blastDir+"/status/run1.csv")
	for _, want := range []string{"state: submitted", "rows: 3", "cluster: 100", "template: blast"} {
		if !strings.Contains(st, want) {
			t.Errorf("status %q does not contain %q", st, want)
		}
	}
}

func readStatus(t *testing.T, f *FS, path string) string {
	t.Helper()
	fi, err := statOne(t, f, path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	ra, err := f.Fileread(&sftp.Request{Method: "Get", Filepath: path})
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	buf := make([]byte, fi.Size())
	if _, err := ra.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt %s: %v", path, err)
	}
	return string(buf)
}

// TestInterruptedUploadDoesNotSubmit is the commit point: a client that
// vanishes gets TransferError before Close, and that upload must not
// become jobs.
func TestInterruptedUploadDoesNotSubmit(t *testing.T) {
	f := newTriggerFS(t)
	w, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: blastDir + "/submit/cut.csv"})
	if err != nil {
		t.Fatalf("Filewrite: %v", err)
	}
	// Half a file arrives...
	if _, err := w.WriteAt([]byte("sample,reads\nHG001,a.fastq\n"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	// ...and then the connection drops. This is the order the request
	// server uses.
	te, ok := w.(interface{ TransferError(error) })
	if !ok {
		t.Fatal("the upload does not implement TransferError, so it cannot tell a dropped transfer from a finished one")
	}
	te.TransferError(io.ErrUnexpectedEOF)
	if err := w.(io.Closer).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if f.sub.count() != 0 {
		t.Error("an interrupted upload was submitted")
	}
	// And it is reported rather than dropped silently.
	st := readStatus(t, f.FS, blastDir+"/status/cut.csv")
	if !strings.Contains(st, "state: incomplete") {
		t.Errorf("status %q does not report the interruption", st)
	}
}

// TestDuplicateUploadIsOneSubmission: a retried upload must not double
// the jobs.
func TestDuplicateUploadIsOneSubmission(t *testing.T) {
	f := newTriggerFS(t)
	csv := "sample,reads\nHG001,a.fastq\n"

	if err := writeCSV(t, f.FS, blastDir+"/submit/again.csv", csv); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	// Same bytes, even under a different name.
	if err := writeCSV(t, f.FS, blastDir+"/submit/again-2.csv", csv); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if f.sub.count() != 1 {
		t.Errorf("submitted %d times, want 1", f.sub.count())
	}
	st := readStatus(t, f.FS, blastDir+"/status/again-2.csv")
	for _, want := range []string{"state: duplicate", "cluster: 100"} {
		if !strings.Contains(st, want) {
			t.Errorf("status %q does not contain %q", st, want)
		}
	}

	// Different bytes do submit.
	if err := writeCSV(t, f.FS, blastDir+"/submit/other.csv",
		"sample,reads\nHG002,b.fastq\n"); err != nil {
		t.Fatalf("third upload: %v", err)
	}
	if f.sub.count() != 2 {
		t.Errorf("submitted %d times, want 2", f.sub.count())
	}
}

// TestRefusedCSVIsReportedInStatus: the close reply carries one error
// string, which a GUI renders as "could not close file". A per-row
// complaint has to be readable somewhere.
func TestRefusedCSVIsReportedInStatus(t *testing.T) {
	f := newTriggerFS(t)
	err := writeCSV(t, f.FS, blastDir+"/submit/bad.csv",
		"sample,reads\nHG001,a.fastq\nHG002,b))c\n")
	if err == nil {
		t.Fatal("a CSV with an injected row was accepted")
	}
	if f.sub.count() != 0 {
		t.Error("it was submitted anyway")
	}
	st := readStatus(t, f.FS, blastDir+"/status/bad.csv")
	for _, want := range []string{"state: refused", "row 3", "))"} {
		if !strings.Contains(st, want) {
			t.Errorf("status %q does not contain %q", st, want)
		}
	}
}

func TestSubmitFailureIsReported(t *testing.T) {
	f := newTriggerFS(t)
	f.sub.failErr = errors.New("the schedd said no")
	err := writeCSV(t, f.FS, blastDir+"/submit/x.csv", "sample,reads\nA,b\n")
	if err == nil {
		t.Fatal("a failed submission reported success")
	}
	st := readStatus(t, f.FS, blastDir+"/status/x.csv")
	if !strings.Contains(st, "the schedd said no") {
		t.Errorf("status %q does not carry the reason", st)
	}
}

func TestStatusDirectoryLists(t *testing.T) {
	f := newTriggerFS(t)
	for i, body := range []string{"sample,reads\nA,1\n", "sample,reads\nB,2\n"} {
		name := []string{"first.csv", "second.csv"}[i]
		if err := writeCSV(t, f.FS, blastDir+"/submit/"+name, body); err != nil {
			t.Fatalf("upload %s: %v", name, err)
		}
	}
	got := listNames(t, f.FS, blastDir+"/status")
	sort.Strings(got)
	if len(got) != 2 || got[0] != "first.csv" || got[1] != "second.csv" {
		t.Errorf("status/ = %v, want [first.csv second.csv]", got)
	}
}

// TestUploadsOnlyIntoSubmit keeps the writable surface to one directory.
func TestUploadsOnlyIntoSubmit(t *testing.T) {
	f := newTriggerFS(t)
	for _, p := range []string{
		blastDir + "/job.sub",
		blastDir + "/README",
		blastDir + "/status/x.csv",
		blastDir,
		"/templates/nope/submit/x.csv",
		"/templates/blast/submit/../../job.sub",
	} {
		if _, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: p}); err == nil {
			t.Errorf("Filewrite(%q) was accepted", p)
		}
	}
}

// TestWithoutATemplateSourceTheTreeIsAbsent: an access point that offers
// no templates has no templates directory, rather than an empty one that
// suggests something is misconfigured.
func TestWithoutATemplateSourceTheTreeIsAbsent(t *testing.T) {
	f := newTestFS(t, false, nil) // no Templates, no Submitter
	if got := listNames(t, f.FS, "/"); len(got) != 1 || got[0] != "outputs/" {
		t.Errorf("/ = %v, want just outputs/", got)
	}
	if _, err := statOne(t, f.FS, "/templates"); err == nil {
		t.Error("/templates exists without a template source")
	}
	if _, err := f.Filewrite(&sftp.Request{Method: "Put",
		Filepath: blastDir + "/submit/x.csv"}); err == nil {
		t.Error("an upload was accepted without a template source")
	}
}
