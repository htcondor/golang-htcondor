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
	"strings"
	"testing"

	htcondor "github.com/htcondor/golang-htcondor"
)

// TestBuildSubmitParsesAndQueuesOnePerRow drives the real submit parser:
// the generated text has to queue exactly as many jobs as the CSV has
// rows, with the variables bound.
func TestBuildSubmitParsesAndQueuesOnePerRow(t *testing.T) {
	sub, err := ParseCSV(strings.NewReader(
		"sample,reads\nHG001,a.fastq\nHG002,b.fastq\nHG003,c.fastq\n"), CSVLimits{})
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}

	text, err := BuildSubmit(goodTemplate(), sub)
	if err != nil {
		t.Fatalf("BuildSubmit: %v", err)
	}
	t.Logf("generated submit file:\n%s", text)

	sf, err := htcondor.ParseSubmitFile(strings.NewReader(text))
	if err != nil {
		t.Fatalf("the generated submit file does not parse: %v", err)
	}
	if got := sf.QueueCount(); got != 3 {
		t.Errorf("queues %d jobs, want 3 (one per row)", got)
	}

	// And the per-row variables reach the job ad.
	ad, err := sf.MakeJobAd(htcondor.JobID{Cluster: 1, Proc: 0},
		map[string]string{"sample": "HG001", "reads": "a.fastq"})
	if err != nil {
		t.Fatalf("MakeJobAd: %v", err)
	}
	// "Args", not "Arguments": that is what the parser writes for the
	// V1 argument syntax a template is most likely to use.
	args, _ := ad.EvaluateAttrString("Args")
	if !strings.Contains(args, "a.fastq") || !strings.Contains(args, "HG001.out") {
		t.Errorf("Args = %q, want the row's values substituted", args)
	}
	// The contract that keeps a spooling submission from parking on
	// HoldReasonCode 16.
	if v, ok := ad.EvaluateAttrBool("TransferExecutable"); !ok || v {
		t.Errorf("TransferExecutable = %v (set=%v), want false", v, ok)
	}
	// The template is recorded on the job, by the library id the web
	// page and /api/v1/templates use for the same template.
	if v, _ := ad.EvaluateAttrString(AttrTemplate); v != "blast" {
		t.Errorf("%s = %q, want %q", AttrTemplate, v, "blast")
	}
}

// TestValidateTemplateRequiresNoQueueStatement: two queue statements
// would submit the template's own idea of the work alongside the CSV's.
func TestValidateTemplateRequiresNoQueueStatement(t *testing.T) {
	for _, line := range []string{"queue", "queue 5", "QUEUE sample from", "  queue\n"} {
		tmpl := goodTemplate()
		tmpl.Contents += line + "\n"
		err := ValidateTemplate(tmpl)
		if err == nil {
			t.Errorf("a template containing %q was accepted", line)
			continue
		}
		if !strings.Contains(err.Error(), "queue statement") {
			t.Errorf("error for %q = %v", line, err)
		}
	}
}

// TestValidateTemplateRequiresTransferExecutableFalse is the trap worth
// a test of its own: output is read from the spool, so the submission
// spools, and a spooling job whose executable needs transferring waits
// on HoldReasonCode 16 forever for an upload a CSV never carries.
func TestValidateTemplateRequiresTransferExecutableFalse(t *testing.T) {
	bad := goodTemplate()
	bad.Contents = "executable = /usr/bin/blastn\n"
	err := ValidateTemplate(bad)
	if err == nil {
		t.Fatal("a template that would hold every job forever was accepted")
	}
	for _, want := range []string{"transfer_executable", "16"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}

	// The spellings HTCondor accepts for false.
	for _, v := range []string{"false", "FALSE", "0", "no", "F"} {
		tmpl := bad
		tmpl.Contents = "executable = /usr/bin/blastn\ntransfer_executable = " + v + "\n"
		if err := ValidateTemplate(tmpl); err != nil {
			t.Errorf("transfer_executable = %s was refused: %v", v, err)
		}
	}
	// And true is still refused.
	tmpl := bad
	tmpl.Contents = "executable = /x\ntransfer_executable = true\n"
	if err := ValidateTemplate(tmpl); err == nil {
		t.Error("transfer_executable = true was accepted")
	}
}

func TestBuildSubmitRefusesNothingToDo(t *testing.T) {
	if _, err := BuildSubmit(goodTemplate(), nil); err == nil {
		t.Error("accepted a nil submission")
	}
	if _, err := BuildSubmit(goodTemplate(), &Submission{Vars: []string{"a"}}); err == nil {
		t.Error("accepted a submission with no rows")
	}
	empty := goodTemplate()
	empty.Contents = "   \n"
	if err := ValidateTemplate(empty); err == nil {
		t.Error("accepted an empty submit description")
	}
}

// TestGeneratedSubmitSurvivesOrdinaryValues: a value with spaces is
// legitimate and must still parse, which is why the separator is a comma.
func TestGeneratedSubmitSurvivesOrdinaryValues(t *testing.T) {
	sub, err := ParseCSV(strings.NewReader(
		"sample,reads\nwith space,a file with spaces.fastq\n"), CSVLimits{})
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	text, err := BuildSubmit(goodTemplate(), sub)
	if err != nil {
		t.Fatalf("BuildSubmit: %v", err)
	}
	sf, err := htcondor.ParseSubmitFile(strings.NewReader(text))
	if err != nil {
		t.Fatalf("a row with spaces broke the submit file: %v", err)
	}
	if got := sf.QueueCount(); got != 1 {
		t.Errorf("queues %d jobs, want 1", got)
	}
}
