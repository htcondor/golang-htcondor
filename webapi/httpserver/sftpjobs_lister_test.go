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
