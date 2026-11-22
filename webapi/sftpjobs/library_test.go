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
	"io/fs"
	"strings"
	"testing"

	"github.com/pkg/sftp"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// userTemplate is a saved template, owned and optionally shared.
func userTemplate(id, owner string, vis templates.Visibility) templates.Template {
	t := blastTemplate()
	t.ID = id
	t.Name = id
	t.Owner = owner
	t.Source = templates.SourceUser
	t.Visibility = vis
	t.Contents = "executable = /usr/bin/" + id + "\ntransfer_executable = false\n" +
		"arguments = $(sample) $(reads)\n"
	return t
}

// newFSWithCatalog builds testAccount's view over a catalog. The owner
// names in the catalogs below are written out, so a template owned by
// the caller and one owned by somebody else read differently at a
// glance.
func newFSWithCatalog(t *testing.T, cat fakeCatalog) *FS {
	t.Helper()
	account := testAccount
	store, err := sandboxfs.New(newFakeSandbox(nil), sandboxfs.Config{Logf: t.Logf})
	if err != nil {
		t.Fatalf("sandboxfs.New: %v", err)
	}
	f, err := New(Config{
		Store:     store,
		Account:   account,
		Context:   htcondor.WithAuthenticatedUser(context.Background(), account),
		Templates: cat,
		Submitter: &recordingSubmitter{},
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

// TestTheTreeOffersWhatTheLibraryOffers is the point of using the
// shared library: a template saved on the web submit page shows up here
// without anything being configured for this service, and another
// user's private one does not.
func TestTheTreeOffersWhatTheLibraryOffers(t *testing.T) {
	cat := fakeCatalog{
		public: []templates.Template{blastTemplate()},
		user: []templates.Template{
			userTemplate("mine", "alice", templates.VisibilityPrivate),
			userTemplate("theirs", "bob", templates.VisibilityPrivate),
			userTemplate("ours", "bob", templates.VisibilityShared),
		},
	}
	f := newFSWithCatalog(t, cat)

	got := listNames(t, f, "/templates")
	want := []string{"blast/", "mine/", "ours/"}
	if len(got) != len(want) {
		t.Fatalf("/templates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("/templates = %v, want %v", got, want)
		}
	}

	// And bob's private one is not reachable by naming it either.
	_, err := statOne(t, f, "/templates/theirs")
	if err == nil {
		t.Fatal("another user's private template was reachable by name")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want one wrapping fs.ErrNotExist", err)
	}
}

// TestListingAndOpeningAgree is the trap in All: it concatenates its
// sources without resolving them, so one id can appear twice -- the
// caller's own template and another user's shared one of the same name.
// A directory cannot hold two entries of one name, and whichever the
// listing shows has to be the one that opens.
func TestListingAndOpeningAgree(t *testing.T) {
	cat := fakeCatalog{
		user: []templates.Template{
			userTemplate("shadowed", "bob", templates.VisibilityShared),
			userTemplate("shadowed", "alice", templates.VisibilityPrivate),
		},
	}
	f := newFSWithCatalog(t, cat)

	if got := listNames(t, f, "/templates"); len(got) != 1 || got[0] != "shadowed/" {
		t.Fatalf("/templates = %v, want one entry", got)
	}
	// The library resolves the caller's own first, so that is the body
	// the tree must serve.
	text := readStatus(t, f, "/templates/shadowed/"+SubmitFileName)
	if !strings.Contains(text, "/usr/bin/shadowed") {
		t.Errorf("job.sub = %q", text)
	}
	if got, _ := f.cfg.Templates.Get("shadowed", "alice"); got.Owner != "alice" {
		t.Errorf("the library resolves owner %q, so the tree is serving somebody else's", got.Owner)
	}
}

// TestTemplateWithNoColumnsIsRefusedBeforeTheUpload: validation at open
// rather than after the bytes arrive. A client that uploads a megabyte
// and is then told the template cannot take a CSV has wasted the
// transfer, and a GUI client renders the close error as "could not close
// file" and nothing more.
func TestTemplateWithNoColumnsIsRefusedBeforeTheUpload(t *testing.T) {
	bare := blastTemplate()
	bare.ID = "nocols"
	bare.Columns = nil
	f := newFSWithCatalog(t, fakeCatalog{public: []templates.Template{bare}})

	_, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: "/templates/nocols/submit/x.csv"})
	if err == nil {
		t.Fatal("a template with no columns accepted an upload")
	}
	if !strings.Contains(err.Error(), "no columns") {
		t.Errorf("error = %v, want it to name the reason", err)
	}
}

// TestARefusalIsReadableBeforeTheUpload: a template this service
// cannot submit has to say so where somebody reads it, not in status/
// after a CSV has been uploaded and turned into nothing.
//
// The case here is a template shipping a file its submit description
// never asks for -- see TestAFileNobodyAsksForIsRefused for why that is
// refused rather than tolerated.
func TestARefusalIsReadableBeforeTheUpload(t *testing.T) {
	withFiles := blastTemplate()
	withFiles.InputFiles = []templates.InputFile{{Name: "run.sh", Content: []byte("#!/bin/sh\n")}}

	err := ValidateTemplate(withFiles)
	if err == nil {
		t.Fatal("a template shipping a file nothing asks for was accepted")
	}
	if !strings.Contains(err.Error(), "run.sh") {
		t.Errorf("error = %v, want it to name the file", err)
	}

	readme := readmeText(withFiles)
	if !strings.Contains(readme, "cannot be submitted") || !strings.Contains(readme, "run.sh") {
		t.Errorf("README = %q, want the refusal in it", readme)
	}

	// And the drop point is closed, so the refusal costs no transfer.
	if _, err := openDropPoint(t, withFiles); err == nil {
		t.Error("an upload was accepted for a template that cannot be submitted")
	}
}

// openDropPoint opens a template's submit/ drop point, which is where
// an upload is refused before its first byte.
func openDropPoint(t *testing.T, tmpl templates.Template) (any, error) {
	t.Helper()
	f := newFSWithCatalog(t, fakeCatalog{public: []templates.Template{tmpl}})
	return f.Filewrite(&sftp.Request{Method: "Put", Filepath: "/templates/" + tmpl.ID + "/submit/x.csv"})
}

// TestReadmeDescribesTheColumns: the README is generated from the
// template, which is the reason it cannot drift from the macros the
// body expands.
func TestReadmeDescribesTheColumns(t *testing.T) {
	readme := readmeText(blastTemplate())
	for _, want := range []string{
		"BLAST a read set",     // Name
		"Aligns one read file", // Description
		"the sample's name",    // Column description
		"sample,reads",         // the header to use
		"status/",              // where the outcome goes
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README does not mention %q:\n%s", want, readme)
		}
	}
}

// TestCSVHeaderMustMatchTheColumns: a misspelled column would otherwise
// submit jobs in which $(sample) expands to nothing, which is a
// successful submission of the wrong work.
func TestCSVHeaderMustMatchTheColumns(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		wants        []string
	}{
		{"a typo", "sample,read", []string{"missing", "reads", "not a column", "read"}},
		{"one missing", "sample", []string{"missing", "reads"}},
		{"one extra", "sample,reads,extra", []string{"not a column", "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub, err := ParseCSV(strings.NewReader(tc.header+"\nA,b,c\n"), CSVLimits{})
			if err != nil {
				// A row width mismatch is its own refusal, which is fine;
				// the header check is what this test is about.
				sub, err = ParseCSV(strings.NewReader(tc.header+"\n"+
					strings.TrimSuffix(strings.Repeat("v,", len(strings.Split(tc.header, ","))), ",")+
					"\n"), CSVLimits{})
				if err != nil {
					t.Fatalf("ParseCSV: %v", err)
				}
			}
			_, err = BuildSubmit(blastTemplate(), sub)
			if err == nil {
				t.Fatalf("header %q was accepted against columns sample, reads", tc.header)
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to mention %q", err, want)
				}
			}
		})
	}
}

// TestCSVHeaderOrderIsFree: the queue statement is written in the
// header's order, so a spreadsheet whose columns are in another order
// is not an error.
func TestCSVHeaderOrderIsFree(t *testing.T) {
	sub, err := ParseCSV(strings.NewReader("reads,sample\na.fastq,HG001\n"), CSVLimits{})
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	text, err := BuildSubmit(blastTemplate(), sub)
	if err != nil {
		t.Fatalf("a reordered header was refused: %v", err)
	}
	if !strings.Contains(text, "queue reads, sample from") {
		t.Errorf("queue statement does not follow the header:\n%s", text)
	}
	// And the values still land on the right macros.
	sf, err := htcondor.ParseSubmitFile(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ParseSubmitFile: %v", err)
	}
	if got := sf.QueueCount(); got != 1 {
		t.Fatalf("queues %d jobs, want 1", got)
	}
	ad, err := sf.MakeJobAd(htcondor.JobID{Cluster: 1, Proc: 0},
		map[string]string{"sample": "HG001", "reads": "a.fastq"})
	if err != nil {
		t.Fatalf("MakeJobAd: %v", err)
	}
	args, _ := ad.EvaluateAttrString("Args")
	if !strings.Contains(args, "a.fastq") || !strings.Contains(args, "HG001.out") {
		t.Errorf("Args = %q, want the row's values on the right macros", args)
	}
}

// TestColumnNamesAreCaseInsensitive: a submit macro is case-insensitive,
// so a header that differs only in case names the same column and must
// not be reported as unknown.
func TestColumnNamesAreCaseInsensitive(t *testing.T) {
	sub, err := ParseCSV(strings.NewReader("Sample,READS\nHG001,a.fastq\n"), CSVLimits{})
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	if _, err := BuildSubmit(blastTemplate(), sub); err != nil {
		t.Errorf("a header differing only in case was refused: %v", err)
	}
}
