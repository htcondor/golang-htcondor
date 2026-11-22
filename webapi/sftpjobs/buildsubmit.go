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
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// BuildSubmit turns a template and a CSV into one submit file.
//
// The rows go into the text, as an inline `queue VARS from ((...))`
// block. That is safe only because ParseCSV has already refused every
// value that would mean something to the submit parser -- "))", "$", a
// line break, a comma, a leading "#" -- and it is the single gate for
// that. Nothing here re-checks; the rows arrive already validated, and
// the only reason this is written down is so that moving the validation
// is understood to be moving the gate.
//
// The same shape the web submit page builds, from the same template:
// see buildSubmitFile in frontend/src/app/submit/page.tsx.
func BuildSubmit(t templates.Template, sub *Submission) (string, error) {
	if sub == nil || len(sub.Rows) == 0 {
		return "", errors.New("there are no rows to submit")
	}
	if err := ValidateTemplate(t); err != nil {
		return "", err
	}
	if err := checkColumns(t, sub.Vars); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(t.Contents, "\n"))
	b.WriteString("\n\n")
	// Recorded on every job, so somebody looking at a job ad can tell
	// where it came from. A library id, which validateTemplate has
	// already held to idPattern, not user text.
	fmt.Fprintf(&b, "+%s = \"%s\"\n", AttrTemplate, t.ID)
	fmt.Fprintf(&b, "queue %s from ((\n", strings.Join(sub.Vars, ", "))
	for _, row := range sub.Rows {
		b.WriteString(row)
		b.WriteString("\n")
	}
	b.WriteString("))\n")
	return b.String(), nil
}

// AttrTemplate is stamped on every job a template submits.
const AttrTemplate = "SFTPJobTemplate"

// checkColumns holds a CSV's header to the template's declared columns.
//
// The template says which macros its body expands, so a header is
// checkable rather than merely usable, and checking it is worth doing:
// a misspelled column submits jobs in which $(sample) expands to
// nothing, which is a successful submission of the wrong work. Order is
// free, because the queue statement is written in the header's order.
func checkColumns(t templates.Template, vars []string) error {
	want := make(map[string]string, len(t.Columns))
	for _, c := range t.Columns {
		want[strings.ToLower(c.Name)] = c.Name
	}
	have := make(map[string]struct{}, len(vars))
	var unknown []string
	for _, v := range vars {
		key := strings.ToLower(v)
		if _, ok := want[key]; !ok {
			unknown = append(unknown, v)
			continue
		}
		have[key] = struct{}{}
	}
	var missing []string
	for key, name := range want {
		if _, ok := have[key]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(unknown) == 0 && len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the CSV header does not match template %s, whose columns are %s",
		t.ID, strings.Join(templates.ColumnNames(t.Columns), ", "))
	if len(missing) > 0 {
		fmt.Fprintf(&b, "; missing: %s", strings.Join(missing, ", "))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, "; not a column of this template: %s", strings.Join(unknown, ", "))
	}
	return errors.New(b.String())
}

// queueLine finds a queue statement anywhere in a submit description.
var queueLine = regexp.MustCompile(`(?im)^\s*queue\b`)

// transferExecutableFalse finds the setting a template must carry.
var transferExecutableFalse = regexp.MustCompile(
	`(?im)^\s*transfer_executable\s*=\s*(false|0|f|no)\s*$`)

// ValidateTemplate checks a template can actually produce running jobs
// through THIS surface.
//
// A template is shared with the web submit page, which can do things
// this one cannot, so a template the page handles fine may be refused
// here. Each rule exists because the alternative is a submission that
// looks accepted and then does nothing, which is the worst outcome
// available.
//
// A template must declare columns. The CSV's header names them and the
// rows fill them; a template with none has nothing for a CSV to vary,
// and the submit page's answer for that case -- a flat `queue N` -- is
// not something a dropped file expresses.
//
// A template must NOT carry its own queue statement. The trigger appends
// one with the CSV's columns, and two would submit the template's own
// idea of the work alongside the CSV's. (The submit page appends one
// too, so a template in the library should not have one anyway.)
//
// A template MUST set transfer_executable = false. Output read through
// this service comes from the schedd's spool, which means the submission
// has to be a spooling one -- that is also what sets leave_in_queue, and
// a job that leaves the queue has no spool left to read. But a spooling
// submission whose executable needs transferring is parked on
// HoldReasonCode 16 waiting for an upload that a CSV never carries, and
// it waits forever. With the executable already on the execute node the
// spool is empty and the job is released at once.
//
// A template's InputFiles cannot be delivered here. The submit page
// posts them to /api/v1/jobs/{id}/input after submitting; an SFTP drop
// has no second call, and spooling them would need the files to reach
// the schedd with the submission. Refused rather than silently dropped,
// because a job whose script never arrived fails in a way that looks
// like the script is wrong.
//
// The same reasoning applies to spooled INPUT named in the body: a CSV
// carries no files, so a template's inputs have to be reachable from the
// execute node -- a URL, or shared storage. That is not checked here,
// because transfer_input_files naming a URL is both legitimate and
// common, and refusing every input would refuse most real templates. It
// is in the documentation instead.
func ValidateTemplate(t templates.Template) error {
	if strings.TrimSpace(t.Contents) == "" {
		return fmt.Errorf("template %s has no submit description", t.ID)
	}
	if len(t.Columns) == 0 {
		return fmt.Errorf("template %s declares no columns, so a CSV has nothing to fill in; "+
			"it can be submitted from the web page but not by dropping a file here", t.ID)
	}
	if loc := queueLine.FindStringIndex(t.Contents); loc != nil {
		return fmt.Errorf("template %s has its own queue statement; "+
			"the CSV's columns supply one, so %s must not", t.ID, SubmitFileName)
	}
	if !transferExecutableFalse.MatchString(t.Contents) {
		return fmt.Errorf("template %s must set `transfer_executable = false`: "+
			"output is read from the schedd's spool, so the submission spools, and a spooling "+
			"job whose executable needs transferring waits on HoldReasonCode 16 for an upload "+
			"a CSV never carries. Put the executable on the execute node or in the container",
			t.ID)
	}
	if len(t.InputFiles) > 0 {
		names := make([]string, 0, len(t.InputFiles))
		for _, f := range t.InputFiles {
			names = append(names, f.Name)
		}
		return fmt.Errorf("template %s ships input files (%s), which this service cannot deliver: "+
			"use the web submit page for this template, or one whose inputs are reachable from "+
			"the execute node", t.ID, strings.Join(names, ", "))
	}
	return nil
}
