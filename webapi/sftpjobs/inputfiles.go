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
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing/fstest"

	"github.com/PelicanPlatform/classad/classad"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// A template's input files, and how they reach the job.
//
// A template may ship files -- a wrapper script, a config, a small
// reference list -- which the library stores with it. The web submit
// page delivers them by POSTing them to /api/v1/jobs/{id}/input after
// the submission. A dropped CSV has no second call, so this service
// delivers them the other way a spooling submission can: it spools
// them, from memory, in the same call that releases the jobs from their
// spooling hold.
//
// The rule that makes that work is the same one in two places, applied
// to two different things:
//
//   - ValidateTemplate compares the template's input files against the
//     names the template's own submit body asks for. That is text, so
//     it is answerable when somebody reads the README, before an upload.
//   - PlanSpool compares them against the names the SUBMITTED jobs ask
//     for, which is the schedd's own allow-set and the authority. A
//     macro the CSV fills in is invisible to the first check and
//     resolved in this one.
//
// Neither is a restatement of the other's pattern: both call
// htcondor.SpoolInputAllowSet, the one function that decides which
// names a spool may carry, and differ only in the ad they hand it.

// columnSentinel is a value no real row would hold, substituted for
// every column to find out whether the transfer list depends on one.
//
// Not a URL and not a path, so it survives into the allow-set the same
// way a filename would.
const columnSentinel = "zzColumnValuezz"

// templateSpoolNames is what a spool of this template would be allowed
// to carry, as the spooler itself computes it, plus whether the answer
// depends on the CSV.
//
// The names come from an ad built with no row values, which is what
// makes them a statement about the TEMPLATE: a macro the CSV fills in
// expands to nothing and drops out of the list, so an input named
// $(reads) is not reported as a file the template fails to ship. Those
// surface in PlanSpool instead, where the row has been substituted.
//
// byColumn is the same question asked with every column filled in. When
// that adds a name, the transfer list is the CSV's to decide and this
// level cannot tell a shipped-but-unasked-for file from one a row will
// ask for -- `transfer_input_files = $(script)` against a template
// shipping two scripts is a template whose CSV chooses, and refusing it
// here would refuse something that works.
//
// Only meaningful once ValidateTemplate has established
// transfer_executable = false; otherwise the executable's basename is
// in here too, and it is not a file the library ships.
func templateSpoolNames(t templates.Template) (names []string, byColumn bool, err error) {
	sf, err := htcondor.ParseSubmitFile(strings.NewReader(t.Contents))
	if err != nil {
		return nil, false, fmt.Errorf("the submit description does not parse: %w", err)
	}
	ad, err := sf.MakeJobAd(htcondor.JobID{Cluster: 1, Proc: 0}, nil)
	if err != nil {
		return nil, false, fmt.Errorf("the submit description does not produce a job: %w", err)
	}
	names = htcondor.SpoolInputAllowSet(ad)

	if len(t.Columns) == 0 {
		return names, false, nil
	}
	filled := make(map[string]string, len(t.Columns))
	for _, c := range t.Columns {
		filled[c.Name] = columnSentinel
	}
	withRow, err := sf.MakeJobAd(htcondor.JobID{Cluster: 1, Proc: 0}, filled)
	if err != nil {
		// Not fatal: a body that cannot be built with values filled in
		// will say so at submit time, where the values are real. All
		// that is lost here is knowing whether a column drives the
		// list, and the safe answer to that is "it might".
		return names, true, nil
	}
	literal := make(map[string]bool, len(names))
	for _, n := range names {
		literal[n] = true
	}
	for _, n := range htcondor.SpoolInputAllowSet(withRow) {
		if !literal[n] {
			return names, true, nil
		}
	}
	return names, false, nil
}

// inputFileNames are the names a template ships, sorted.
func inputFileNames(t templates.Template) []string {
	out := make([]string, 0, len(t.InputFiles))
	for _, f := range t.InputFiles {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// inputBytes is the total size of what a template ships. One job's
// worth: every proc of the cluster is spooled the same files.
func inputBytes(t templates.Template) int64 {
	var n int64
	for _, f := range t.InputFiles {
		n += int64(len(f.Content))
	}
	return n
}

// matchInputFiles compares the names a spool may carry against the
// names the template ships, and reports the two ways they can differ.
//
// Both are failures of the same kind -- a submission that is accepted
// and then does not do the work:
//
//   - A name the job asks for that nothing ships is a file the job will
//     wait for forever. The spooler stats every name in the allow-set
//     and fails the whole job on the first one it cannot open, so the
//     jobs stay in their spooling hold. Always reported.
//   - A file the template ships that nothing asks for is dropped in
//     silence on its way to the schedd, and the job runs without it.
//     That is the worse of the two, because it looks like a success.
//
// reportUnused is for the second one, and only the template-level check
// sets it. It is a question about how the template was WRITTEN, and the
// template is the only place it has a single answer: when a column
// drives the transfer list, each row uses a different one of the
// shipped files, and the ones a given row does not name are not that
// row's problem -- they are outside its allow-set, so they are not sent
// either way. Asking it per submission would refuse a template whose
// CSV chooses, and templateSpoolNames says when that is the case.
func matchInputFiles(t templates.Template, allow []string, reportUnused bool) error {
	ships := make(map[string]bool, len(t.InputFiles))
	for _, f := range t.InputFiles {
		ships[f.Name] = true
	}
	wanted := make(map[string]bool, len(allow))
	var missing []string
	for _, name := range allow {
		wanted[name] = true
		if !ships[name] {
			missing = append(missing, name)
		}
	}
	var unused []string
	if reportUnused {
		for _, name := range inputFileNames(t) {
			if !wanted[name] {
				unused = append(unused, name)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("the submit description asks for %s, which nothing here can deliver: "+
			"a dropped CSV carries no files, so an input file has to ship with the template or be "+
			"a URL the execute node fetches itself", strings.Join(missing, ", "))
	}
	if len(unused) > 0 {
		return fmt.Errorf("the template ships %s, which the submit description never asks for: "+
			"name it in transfer_input_files, or the job runs without it",
			strings.Join(unused, ", "))
	}
	return nil
}

// SpoolPlan is what a submitted cluster still needs before it can run.
//
// A zero plan means nothing does: the jobs asked for no files they
// cannot fetch themselves, and SubmitRemote has already released them.
type SpoolPlan struct {
	// Files is the filesystem to spool from, or nil when there is
	// nothing to spool.
	Files fs.FS
	// Names are the files it will carry, sorted, for the log line.
	Names []string
}

// Empty reports a plan with nothing to spool.
func (p SpoolPlan) Empty() bool { return p.Files == nil }

// PlanSpool decides what to send a submitted cluster, from the jobs
// themselves.
//
// procAds are what SubmitRemote returned, so the allow-set is the
// schedd's: every macro is expanded, every URL is already dropped, and
// a job with nothing to spool contributes nothing. The union across
// procs is what gets sent, because the whole cluster spools in one call
// and the files are the same for every row.
//
// An error here is a cluster that must not be left in the queue: the
// jobs are held for input that is not coming.
func PlanSpool(t templates.Template, procAds []*classad.ClassAd) (SpoolPlan, error) {
	seen := map[string]bool{}
	var allow []string
	for _, ad := range procAds {
		for _, name := range htcondor.SpoolInputAllowSet(ad) {
			if !seen[name] {
				seen[name] = true
				allow = append(allow, name)
			}
		}
	}
	sort.Strings(allow)
	// The submitted jobs' own allow-set, so a name missing from it is
	// exact and fatal. A shipped file this cluster does not ask for is
	// not: see matchInputFiles.
	if err := matchInputFiles(t, allow, false); err != nil {
		return SpoolPlan{}, err
	}
	if len(allow) == 0 {
		return SpoolPlan{}, nil
	}

	// From memory, not from a staging directory: there is no on-disk
	// state to clean up when a submission fails partway through, and
	// the bytes are already in hand -- the library holds them.
	stage := fstest.MapFS{}
	for _, f := range t.InputFiles {
		stage[f.Name] = &fstest.MapFile{Data: f.Content, Mode: 0o600}
	}
	return SpoolPlan{Files: stage, Names: allow}, nil
}

// CheckSpoolVolume refuses a submission that would spool more than the
// access point allows.
//
// Every proc is spooled the template's files, so the transfer is the
// files times the rows -- a 1 MiB wrapper script and ten thousand rows
// is ten gigabytes, which a CSV small enough to drop over SFTP can ask
// for without anybody noticing. Checked before the submission rather
// than after, because the alternative is withdrawing a cluster that was
// accepted.
//
// maxBytes is the operator's ceiling for one upload (the same
// HTTP_API_SPOOL_MAX_UPLOAD_BYTES the REST spool endpoints answer to);
// zero or less means no ceiling.
func CheckSpoolVolume(t templates.Template, rows int, maxBytes int64) error {
	per := inputBytes(t)
	if per == 0 || maxBytes <= 0 || rows <= 0 {
		return nil
	}
	total := per * int64(rows)
	if total <= maxBytes {
		return nil
	}
	return fmt.Errorf("this template ships %d bytes of input files, and %d rows would spool "+
		"%d bytes in all, over this access point's %d byte limit: submit fewer rows at a time, "+
		"or use a template whose inputs are URLs the execute nodes fetch themselves",
		per, rows, total, maxBytes)
}
