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
	"io/fs"
	"strings"
	"testing"

	"github.com/PelicanPlatform/classad/classad"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// wrapperTemplate ships a script and asks for it, which is the shape
// the submit page's input_files are for: the work is a few lines of
// shell around a program the execute node already has.
func wrapperTemplate() templates.Template {
	t := blastTemplate()
	t.ID = "wrapped"
	t.Contents = "executable = /bin/sh\n" +
		"transfer_executable = false\n" +
		"transfer_input_files = run.sh\n" +
		"arguments = run.sh $(reads) $(sample)\n"
	t.InputFiles = []templates.InputFile{
		{Name: "run.sh", Content: []byte("#!/bin/sh\nexec blastn -query \"$1\" -out \"$2.out\"\n")},
	}
	return t
}

// jobAdsFor builds the ads SubmitRemote would return for these rows.
//
// Through the real parser rather than hand-built, because the thing
// under test is which names the SPOOLER will expect, and that is
// computed from Cmd, TransferExecutable and a TransferInput the parser
// writes after expanding every macro in it.
func jobAdsFor(t *testing.T, tmpl templates.Template, rows ...map[string]string) []*classad.ClassAd {
	t.Helper()
	sf, err := htcondor.ParseSubmitFile(strings.NewReader(tmpl.Contents + "\nqueue\n"))
	if err != nil {
		t.Fatalf("ParseSubmitFile: %v", err)
	}
	out := make([]*classad.ClassAd, 0, len(rows))
	for i, vars := range rows {
		ad, err := sf.MakeJobAd(htcondor.JobID{Cluster: 7, Proc: i}, vars)
		if err != nil {
			t.Fatalf("MakeJobAd: %v", err)
		}
		out = append(out, ad)
	}
	return out
}

// TestAShippedWrapperIsAcceptedAndSpooled is the capability: a template
// whose work is a script the library stores can be submitted by
// dropping a CSV, with the script delivered out of memory in the call
// that releases the jobs.
func TestAShippedWrapperIsAcceptedAndSpooled(t *testing.T) {
	tmpl := wrapperTemplate()
	if err := ValidateTemplate(tmpl); err != nil {
		t.Fatalf("a template shipping the file it asks for was refused: %v", err)
	}

	ads := jobAdsFor(t, tmpl,
		map[string]string{"sample": "HG001", "reads": "a.fastq"},
		map[string]string{"sample": "HG002", "reads": "b.fastq"})
	plan, err := PlanSpool(tmpl, ads)
	if err != nil {
		t.Fatalf("PlanSpool: %v", err)
	}
	if plan.Empty() {
		t.Fatal("nothing would be spooled, so the jobs would wait for run.sh forever")
	}
	if len(plan.Names) != 1 || plan.Names[0] != "run.sh" {
		t.Fatalf("plan carries %v, want [run.sh]", plan.Names)
	}
	// The bytes, not just the name: a plan that named the file and
	// carried an empty one would spool successfully and run nothing.
	got, err := fs.ReadFile(plan.Files, "run.sh")
	if err != nil {
		t.Fatalf("reading run.sh from the plan: %v", err)
	}
	if string(got) != string(tmpl.InputFiles[0].Content) {
		t.Errorf("run.sh = %q, want the template's own content", got)
	}
}

// TestAFileNobodyAsksForIsRefused: a name outside the spool's allow-set
// is dropped in silence on its way to the schedd, so the job runs
// without the file and reports success at every step. That is the worst
// of the available failures, and the template is refused for it.
func TestAFileNobodyAsksForIsRefused(t *testing.T) {
	tmpl := wrapperTemplate()
	tmpl.InputFiles = append(tmpl.InputFiles,
		templates.InputFile{Name: "unused.cfg", Content: []byte("k=v\n")})

	err := ValidateTemplate(tmpl)
	if err == nil {
		t.Fatal("a template shipping a file the submit description ignores was accepted")
	}
	if !strings.Contains(err.Error(), "unused.cfg") {
		t.Errorf("error = %v, want it to name the file", err)
	}
	if !strings.Contains(err.Error(), "transfer_input_files") {
		t.Errorf("error = %v, want it to say how to fix the template", err)
	}
}

// TestAnInputNothingShipsIsRefused: the other direction. The spooler
// stats every name in the allow-set and fails the job on the first one
// it cannot open, so these jobs never leave their spooling hold.
func TestAnInputNothingShipsIsRefused(t *testing.T) {
	tmpl := wrapperTemplate()
	tmpl.Contents = strings.Replace(tmpl.Contents,
		"transfer_input_files = run.sh", "transfer_input_files = run.sh, reference.fa", 1)

	err := ValidateTemplate(tmpl)
	if err == nil {
		t.Fatal("a template asking for a file nothing can deliver was accepted")
	}
	if !strings.Contains(err.Error(), "reference.fa") {
		t.Errorf("error = %v, want it to name the file", err)
	}
	if strings.Contains(err.Error(), "run.sh") {
		t.Errorf("error = %v, want it to name only the file that is missing", err)
	}
}

// TestURLInputsNeedNoDelivery: a URL in transfer_input_files is fetched
// by the execute node, which is how a template delivers input too large
// to ship with it. Nothing to spool, and nothing to refuse.
func TestURLInputsNeedNoDelivery(t *testing.T) {
	tmpl := blastTemplate()
	tmpl.Contents += "transfer_input_files = https://example.com/reference.fa\n"

	if err := ValidateTemplate(tmpl); err != nil {
		t.Fatalf("a template whose inputs are URLs was refused: %v", err)
	}
	plan, err := PlanSpool(tmpl, jobAdsFor(t, tmpl, map[string]string{"sample": "A", "reads": "b"}))
	if err != nil {
		t.Fatalf("PlanSpool: %v", err)
	}
	if !plan.Empty() {
		t.Errorf("plan carries %v, want nothing: the execute node fetches a URL itself", plan.Names)
	}
}

// TestAMacroInputIsResolvedAtSubmitTime is why the check runs twice.
//
// `transfer_input_files = $(reads)` is a column the CSV fills in. At
// template level the macro expands to nothing and there is nothing to
// check -- a refusal there would refuse a legitimate template. What the
// row names is either a URL the execute node fetches or a file nobody
// can deliver, and only the submitted ad says which.
func TestAMacroInputIsResolvedAtSubmitTime(t *testing.T) {
	tmpl := blastTemplate()
	tmpl.Contents += "transfer_input_files = $(reads)\n"

	if err := ValidateTemplate(tmpl); err != nil {
		t.Fatalf("a template whose input is a column was refused: %v", err)
	}

	urlRow := jobAdsFor(t, tmpl, map[string]string{"sample": "A", "reads": "https://example.com/a.fastq"})
	plan, err := PlanSpool(tmpl, urlRow)
	if err != nil {
		t.Fatalf("a row naming a URL was refused: %v", err)
	}
	if !plan.Empty() {
		t.Errorf("plan carries %v, want nothing", plan.Names)
	}

	fileRow := jobAdsFor(t, tmpl, map[string]string{"sample": "A", "reads": "a.fastq"})
	if _, err := PlanSpool(tmpl, fileRow); err == nil {
		t.Error("a row naming a local file was accepted; those jobs would hold forever")
	} else if !strings.Contains(err.Error(), "a.fastq") {
		t.Errorf("error = %v, want it to name the row's file", err)
	}
}

// TestOneBadRowTakesTheWholeCluster: the cluster spools in one call, so
// a single row naming an undeliverable file is the whole submission's
// problem. Reported rather than spooling the rest, because a cluster
// where some procs run and others hold forever is harder to find than
// one that was refused.
func TestOneBadRowTakesTheWholeCluster(t *testing.T) {
	tmpl := blastTemplate()
	tmpl.Contents += "transfer_input_files = $(reads)\n"

	ads := jobAdsFor(t, tmpl,
		map[string]string{"sample": "A", "reads": "https://example.com/a.fastq"},
		map[string]string{"sample": "B", "reads": "local.fastq"},
		map[string]string{"sample": "C", "reads": "https://example.com/c.fastq"})
	if _, err := PlanSpool(tmpl, ads); err == nil {
		t.Fatal("a cluster with one undeliverable row was accepted")
	} else if !strings.Contains(err.Error(), "local.fastq") {
		t.Errorf("error = %v, want it to name the row that cannot be delivered", err)
	}
}

// TestSpoolVolumeIsFilesTimesRows: the transfer is the template's files
// once per proc, so the bound has to be the product. A CSV small enough
// to drop over SFTP can otherwise ask for tens of gigabytes.
func TestSpoolVolumeIsFilesTimesRows(t *testing.T) {
	tmpl := wrapperTemplate()
	tmpl.InputFiles[0].Content = make([]byte, 1<<20) // 1 MiB

	const limit = 100 << 20 // 100 MiB
	if err := CheckSpoolVolume(tmpl, 50, limit); err != nil {
		t.Errorf("50 rows of a 1 MiB file was refused under a 100 MiB limit: %v", err)
	}
	err := CheckSpoolVolume(tmpl, 500, limit)
	if err == nil {
		t.Fatal("500 rows of a 1 MiB file was accepted under a 100 MiB limit")
	}
	for _, want := range []string{"500 rows", "fewer rows"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}

	// A template that ships nothing spools nothing, however many rows.
	if err := CheckSpoolVolume(blastTemplate(), 10_000_000, limit); err != nil {
		t.Errorf("a template with no input files was held to the volume limit: %v", err)
	}
}

// TestAMalformedBodyIsRefusedAtTheReadme: the body is parsed, and a job
// ad built from it, to work out which files the job asks for -- so a
// template that cannot produce a job is answerable where it is read
// rather than after a CSV has been uploaded.
func TestAMalformedBodyIsRefusedAtTheReadme(t *testing.T) {
	tmpl := blastTemplate()
	tmpl.Contents = "executable = /bin/true\ntransfer_executable = false\nrequest_memory = \"$(\n"

	err := ValidateTemplate(tmpl)
	if err == nil {
		t.Fatal("a template whose submit description does not parse was accepted")
	}
	if !strings.Contains(err.Error(), "the submit description does not") {
		t.Errorf("error = %v, want it to say the submit description is the problem", err)
	}
	if !strings.Contains(readmeText(tmpl), "cannot be submitted") {
		t.Errorf("README does not carry the refusal:\n%s", readmeText(tmpl))
	}
}

// TestTheReadmeNamesTheShippedFiles: a template may have been written
// by somebody else, and a wrapper script is part of what the job runs.
func TestTheReadmeNamesTheShippedFiles(t *testing.T) {
	readme := readmeText(wrapperTemplate())
	if !strings.Contains(readme, "run.sh") {
		t.Errorf("README does not name the shipped script:\n%s", readme)
	}
	if !strings.Contains(readme, "ship with the template") {
		t.Errorf("README does not say the file ships with the template:\n%s", readme)
	}
}

// TestTheCSVCanChooseWhichShippedFileToUse: a template may ship several
// files and let a column say which one a row runs. The template-level
// check cannot see that -- `transfer_input_files = $(script)` names
// nothing until a row fills it in -- so it does not report the others
// as files nobody asks for, and the submission sends only the one the
// row named.
func TestTheCSVCanChooseWhichShippedFileToUse(t *testing.T) {
	tmpl := blastTemplate()
	tmpl.ID = "chooser"
	tmpl.Columns = append(tmpl.Columns, templates.Column{Name: "script"})
	tmpl.Contents = "executable = /bin/sh\n" +
		"transfer_executable = false\n" +
		"transfer_input_files = $(script)\n" +
		"arguments = $(script) $(sample)\n"
	tmpl.InputFiles = []templates.InputFile{
		{Name: "fast.sh", Content: []byte("#!/bin/sh\nexit 0\n")},
		{Name: "slow.sh", Content: []byte("#!/bin/sh\nsleep 1\n")},
	}

	if err := ValidateTemplate(tmpl); err != nil {
		t.Fatalf("a template whose CSV chooses its script was refused: %v", err)
	}

	plan, err := PlanSpool(tmpl, jobAdsFor(t, tmpl,
		map[string]string{"sample": "A", "reads": "x", "script": "fast.sh"}))
	if err != nil {
		t.Fatalf("PlanSpool: %v", err)
	}
	if len(plan.Names) != 1 || plan.Names[0] != "fast.sh" {
		t.Fatalf("plan carries %v, want only the script the row named", plan.Names)
	}
	// slow.sh is in the staged filesystem and simply not asked for,
	// which is what keeps the next row free to ask for it.
	if _, err := fs.ReadFile(plan.Files, "slow.sh"); err != nil {
		t.Errorf("the other script is not staged: %v", err)
	}
}

// TestAShippedFileIsStillCheckedWhenTheListIsLiteral: the exemption
// above is for a transfer list a column drives, and nothing wider. A
// template whose list is written out is held to it.
func TestAShippedFileIsStillCheckedWhenTheListIsLiteral(t *testing.T) {
	tmpl := wrapperTemplate()
	tmpl.InputFiles = append(tmpl.InputFiles,
		templates.InputFile{Name: "stray.cfg", Content: []byte("k=v\n")})
	if err := ValidateTemplate(tmpl); err == nil {
		t.Fatal("a literal transfer list was let off the unused-file check")
	}
}
