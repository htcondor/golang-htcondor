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
	"strings"
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
func BuildSubmit(t Template, sub *Submission) (string, error) {
	if sub == nil || len(sub.Rows) == 0 {
		return "", errors.New("there are no rows to submit")
	}
	if err := ValidateTemplate(t); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(t.Submit, "\n"))
	b.WriteString("\n\n")
	// Recorded on every job, so somebody looking at a job ad can tell
	// where it came from. A validated template id, not user text.
	fmt.Fprintf(&b, "+%s = \"%s\"\n", AttrTemplate, t.Ref.ID())
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

// queueLine finds a queue statement anywhere in a submit description.
var queueLine = regexp.MustCompile(`(?im)^\s*queue\b`)

// transferExecutableFalse finds the setting a template must carry.
var transferExecutableFalse = regexp.MustCompile(
	`(?im)^\s*transfer_executable\s*=\s*(false|0|f|no)\s*$`)

// ValidateTemplate checks a template can actually produce running jobs.
//
// Both rules exist because the alternative is a submission that looks
// accepted and then does nothing, which is the worst outcome available:
//
// A template must NOT carry its own queue statement. The trigger appends
// one with the CSV's columns, and two would submit the template's own
// idea of the work alongside the CSV's.
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
// The same reasoning applies to spooled INPUT: a CSV carries no files,
// so a template's inputs have to be reachable from the execute node --
// a URL, or shared storage. That is not checked here, because
// transfer_input_files naming a URL is both legitimate and common, and
// refusing every input would refuse most real templates. It is in the
// documentation instead.
func ValidateTemplate(t Template) error {
	if strings.TrimSpace(t.Submit) == "" {
		return fmt.Errorf("template %s has an empty %s", t.Ref, SubmitFileName)
	}
	if loc := queueLine.FindStringIndex(t.Submit); loc != nil {
		return fmt.Errorf("template %s has its own queue statement in %s; "+
			"the CSV's columns supply one, so %s must not",
			t.Ref, SubmitFileName, SubmitFileName)
	}
	if !transferExecutableFalse.MatchString(t.Submit) {
		return fmt.Errorf("template %s must set `transfer_executable = false` in %s: "+
			"output is read from the schedd's spool, so the submission spools, and a spooling "+
			"job whose executable needs transferring waits on HoldReasonCode 16 for an upload "+
			"a CSV never carries. Put the executable on the execute node or in the container",
			t.Ref, SubmitFileName)
	}
	return nil
}
