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
	"os"
	"strings"
	"testing"
)

// The user-facing walkthrough, whose examples these tests extract and
// run through the real accept path.
//
// A tutorial is a promise that the commands in it work, and the one
// thing that reliably breaks a tutorial is the code moving underneath
// it. The examples are read out of the page rather than copied into
// this file, so a change to either side that leaves them disagreeing
// fails here instead of wasting somebody's afternoon.
const walkthroughDoc = "../../docs/user/sftp-job-submit.md"

func readWalkthrough(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(walkthroughDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", walkthroughDoc, err)
	}
	return string(raw)
}

// docHeredoc returns the body the page writes into name with
// `cat > name <<'EOF' ... EOF`.
func docHeredoc(t *testing.T, doc, name string) string {
	t.Helper()
	open := "cat > " + name + " <<'EOF'\n"
	i := strings.Index(doc, open)
	if i < 0 {
		t.Fatalf("%s no longer writes %s with a heredoc, so the walkthrough cannot be checked",
			walkthroughDoc, name)
	}
	rest := doc[i+len(open):]
	end := strings.Index(rest, "\nEOF\n")
	if end < 0 {
		t.Fatalf("the %s heredoc in %s is unterminated", name, walkthroughDoc)
	}
	return rest[:end+1]
}

// docFenced returns the first fenced block holding marker.
func docFenced(t *testing.T, doc, marker string) string {
	t.Helper()
	for rest := doc; ; {
		i := strings.Index(rest, "\n```")
		if i < 0 {
			break
		}
		rest = rest[i+4:]
		// Past the info string, to the block's first line.
		nl := strings.Index(rest, "\n")
		if nl < 0 {
			break
		}
		body := rest[nl+1:]
		end := strings.Index(body, "\n```")
		if end < 0 {
			break
		}
		block := body[:end+1]
		if strings.Contains(block, marker) {
			return block
		}
		rest = body[end:]
	}
	t.Fatalf("%s no longer has a fenced block containing %q", walkthroughDoc, marker)
	return ""
}

// docTarCommand returns the archive and the files the page packs into
// it.
func docTarCommand(t *testing.T, doc string) (string, []string) {
	t.Helper()
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		// The first one is the walkthrough's own; the later mentions
		// are prose about where job.sub has to sit.
		if !strings.HasPrefix(line, "tar czf ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "tar czf "))
		if len(fields) < 2 {
			t.Fatalf("the tar command in %s packs nothing: %q", walkthroughDoc, line)
		}
		return fields[0], fields[1:]
	}
	t.Fatalf("%s no longer packs an archive with tar czf", walkthroughDoc)
	return "", nil
}

// TestTheWalkthroughsOwnJobSubmits runs the page's worked example: the
// job.sub it tells somebody to write, the script it tells them to write
// beside it, packed by the command it gives, dropped where it says to
// drop it.
func TestTheWalkthroughsOwnJobSubmits(t *testing.T) {
	doc := readWalkthrough(t)
	bodies := map[string]string{
		"job.sub":  docHeredoc(t, doc, "job.sub"),
		"hello.sh": docHeredoc(t, doc, "hello.sh"),
	}
	archive, packed := docTarCommand(t, doc)

	files := make([]archiveFile, 0, len(packed))
	for _, name := range packed {
		body, ok := bodies[name]
		if !ok {
			t.Fatalf("the walkthrough packs %s but never writes it", name)
		}
		files = append(files, archiveFile{Name: name, Body: body})
	}

	f := newTriggerFS(t)
	st := drop(t, f, archive, gzipBytes(t, tarBytes(t, files...)))
	if st.State != stateSubmitted {
		t.Fatalf("the walkthrough's own example was not submitted: %s", st.Text())
	}
	if st.Cluster == 0 {
		t.Error("the status entry names no cluster, which is the one thing the page tells the reader to read out of it")
	}
	if st.Files != len(files) {
		t.Errorf("status says files: %d, the archive holds %d", st.Files, len(files))
	}

	// The executable the page ships is the point of the example: a
	// template could not carry it. If the submit description stopped
	// transferring it the page would still "work" and would no longer
	// be demonstrating anything.
	call, ok := f.sub.lastArchive()
	if !ok {
		t.Fatal("the submitter was never called, so nothing was submitted")
	}
	if call.Submit != bodies["job.sub"] {
		t.Errorf("the submitter got a different submit description than the page writes:\n%s", call.Submit)
	}
	if !strings.Contains(bodies["job.sub"], "executable = hello.sh") {
		t.Error("the walkthrough's job.sub no longer runs the script it ships")
	}
}

// TestTheWalkthroughsManyJobsExampleSubmits covers the second example,
// which is the first one with an inline queue statement -- the form the
// page tells people to use because `queue ... from <file>` cannot work
// here.
func TestTheWalkthroughsManyJobsExampleSubmits(t *testing.T) {
	doc := readWalkthrough(t)
	submit := docFenced(t, doc, "queue sample from ((")
	script := docHeredoc(t, doc, "hello.sh")

	f := newTriggerFS(t)
	st := drop(t, f, "many.tar", tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: submit},
		archiveFile{Name: "hello.sh", Body: script}))
	if st.State != stateSubmitted {
		t.Fatalf("the walkthrough's many-jobs example was not submitted: %s", st.Text())
	}
}

// TestTheWalkthroughsRefusalsAreTheRealOnes holds the troubleshooting
// table to what the service actually says.
//
// That table is the page's most load-bearing part -- somebody reads it
// with a failure in front of them -- and it is the part that rots
// silently, because a reworded refusal breaks no test and no build.
func TestTheWalkthroughsRefusalsAreTheRealOnes(t *testing.T) {
	doc := readWalkthrough(t)
	good := docHeredoc(t, doc, "job.sub")

	cases := []struct {
		name string
		// quote is the refusal as the page's table prints it, minus
		// the "error: " the status entry carries.
		quote string
		files []archiveFile
	}{
		{
			name:  "no submit description",
			quote: "the archive has no job.sub in it",
			files: []archiveFile{{Name: "hello.sh", Body: "#!/bin/sh\n"}},
		},
		{
			name:  "packed the directory",
			quote: "the archive has job.sub at mydir/job.sub, but it has to be at the top level",
			files: []archiveFile{{Name: "mydir/job.sub", Body: good}},
		},
		{
			name:  "left a file out",
			quote: "job.sub asks for hello.sh, which the archive does not carry",
			files: []archiveFile{{Name: SubmitFileName, Body: good}},
		},
		{
			name:  "a submit description that does not parse",
			quote: "job.sub does not parse",
			files: []archiveFile{{Name: SubmitFileName, Body: "executable = hello.sh\nqueue sample from ((\nHG001\n"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(doc, "`error: "+tc.quote+"`") {
				t.Fatalf("%s no longer quotes %q in its troubleshooting table", walkthroughDoc, tc.quote)
			}
			f := newTriggerFS(t)
			st := drop(t, f, "broken.tar", tarBytes(t, tc.files...))
			if st.State != stateRefused {
				t.Fatalf("state is %q, want refused:\n%s", st.State, st.Text())
			}
			if !strings.Contains(st.Err, tc.quote) {
				t.Errorf("the page tells the reader to expect\n  %s\nand the service says\n  %s",
					tc.quote, st.Err)
			}
		})
	}
}

// TestTheWalkthroughsCSVSubmits runs the template half of the page: the
// CSV it tells somebody to write, dropped on a template with the
// columns the page's README excerpt shows.
//
// The excerpt is a transcript of a generated file, which is the kind of
// thing that goes stale quietly -- a reader comparing it with their own
// README should find the same shape.
func TestTheWalkthroughsCSVSubmits(t *testing.T) {
	doc := readWalkthrough(t)
	csv := docHeredoc(t, doc, "runs.csv")

	f := newTriggerFS(t)
	if err := writeCSV(t, f.FS, blastDir+"/submit/runs.csv", csv); err != nil {
		t.Fatalf("the walkthrough's CSV was refused: %v", err)
	}
	st, ok := f.statusFor("blast", "runs.csv")
	if !ok {
		t.Fatal("no status entry, which is the file the page tells the reader to read")
	}
	if st.State != stateSubmitted {
		t.Fatalf("the walkthrough's CSV was not submitted:\n%s", st.Text())
	}
	// The page prints a receipt with two rows in it.
	if st.Rows != 2 {
		t.Errorf("status says rows: %d, the page's CSV has 2 data rows", st.Rows)
	}

	// The README excerpt the page shows is a transcript of this
	// generator, so the real one has to still say those things.
	readme := readmeText(blastTemplate())
	for _, want := range []string{
		"Drop a CSV into submit/ with these columns, in any order:",
		"One job per row. For example:",
		"value-of-sample,value-of-reads",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("the generated README no longer says %q, so the page's excerpt is stale:\n%s",
				want, readme)
		}
		if !strings.Contains(doc, want) {
			t.Errorf("%s no longer shows %q in its README excerpt", walkthroughDoc, want)
		}
	}
}

// TestTheWalkthroughsCSVRulesAreTheRealOnes holds the page's list of
// what a CSV may not contain to what the parser enforces.
//
// Worth pinning separately from the refusal table: these are the rules
// a reader checks their spreadsheet against before uploading, so a rule
// the page lists and the parser does not have -- or the reverse -- is a
// spreadsheet rejected or accepted against what the page promised.
func TestTheWalkthroughsCSVRulesAreTheRealOnes(t *testing.T) {
	doc := readWalkthrough(t)

	cases := []struct {
		name string
		csv  string
	}{
		{"a macro in a value", "sample,reads\nHG$001,r.fq\n"},
		{"a comma in a value", "sample,reads\n\"HG,001\",r.fq\n"},
		{"a line break in a value", "sample,reads\n\"HG\n001\",r.fq\n"},
		{"a row that closes the job list", "sample,reads\n)x,r.fq\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTriggerFS(t)
			err := writeCSV(t, f.FS, blastDir+"/submit/rules.csv", tc.csv)
			if err == nil {
				t.Fatalf("%s was accepted; the page says it is refused", tc.name)
			}
			t.Logf("refused with: %v", err)
			if f.sub.count() != 0 {
				t.Error("jobs were submitted anyway")
			}
		})
	}

	// The page's own wording, so a rule dropped from the list is caught
	// here rather than by somebody's rejected upload.
	for _, want := range []string{"`$`", "a comma", "a line break", "starts with `)`"} {
		if !strings.Contains(doc, want) {
			t.Errorf("%s no longer tells the reader about %s", walkthroughDoc, want)
		}
	}
}
