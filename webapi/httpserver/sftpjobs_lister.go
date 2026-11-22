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
	"errors"
	"fmt"
	"time"

	"github.com/PelicanPlatform/classad/classad"

	"github.com/htcondor/golang-htcondor/webapi/dbmirror"
	"github.com/htcondor/golang-htcondor/webapi/sftpjobs"
)

// sftpJobsListLimit bounds one listing.
//
// A directory of ten thousand entries is already past what a client
// renders usefully, and the point of the limit is that an `ls` costs a
// bounded query rather than that the number is exactly right.
const sftpJobsListLimit = 10000

// mirrorJobLister lists a caller's readable jobs from the htcondordb
// mirror, for the +jobs file service.
type mirrorJobLister struct {
	h       *Handler
	account string
}

// ReadableJobs implements sftpjobs.JobLister.
//
// The QUEUE, not the history, and that is not a gap. A job's output is
// read with TRANSFER_DATA, which matches against the queue -- so once a
// job leaves it there is no spool left to transfer and nothing this
// surface could serve. LeaveJobInQueue is therefore not an obstacle
// here, it is the window: it is exactly how long output stays
// retrievable. A completed job in history is correctly absent, because
// asking for its files would fail.
//
// Completed jobs only. A held or running job has no finished output, and
// a removed one has nothing worth promising.
func (l mirrorJobLister) ReadableJobs(ctx context.Context) ([]sftpjobs.JobRef, error) {
	h := l.h
	if !h.dbMirror.Enabled() {
		// Said rather than shown as an empty directory: "you have no
		// jobs" and "this access point cannot tell you" are different
		// answers, and only one of them is worth acting on.
		return nil, errors.New("no htcondordb mirror is configured, so this listing cannot be built")
	}
	// The mirror bypasses the schedd handshake, so it answers for a
	// caller only once the schedd has accepted them at READ -- the level
	// of the query it is standing in for. Cached per account, so this
	// costs one handshake per caller per interval and not one per `ls`.
	if !h.mirrorReadVerifiedAsUser(ctx) {
		return nil, errMirrorNotVerified
	}
	info, err := h.dbMirror.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding the htcondordb mirror: %w", err)
	}
	if d := dbmirror.JobsDecision(info, ""); !d.Use {
		return nil, fmt.Errorf("the htcondordb mirror cannot answer this listing: %s", d.Note)
	}

	dbc, closer, scope, err := h.mirrorScope(ctx, l.account, true)
	if err != nil {
		return nil, err
	}
	defer closer()

	rows, err := dbc.QueryRawProject(ctx, "jobs", sftpJobsConstraint(scope),
		sftpJobsProjection, sftpJobsListLimit)
	if err != nil {
		return nil, fmt.Errorf("listing jobs from the htcondordb mirror: %w", err)
	}
	return sftpJobsFromRows(rows), nil
}

// sftpJobsProjection is the little this listing needs: an id to address
// the job by and a time to show on its directory.
var sftpJobsProjection = []string{"ClusterId", "ProcId", "CompletionDate"}

// sftpJobsConstraint narrows a listing to the caller's completed jobs.
//
// JobStatus 4 is Completed, written as the number the attribute holds;
// the name exists only in tooling. The caller's scope is parenthesised
// so it cannot escape the enclosing AND.
func sftpJobsConstraint(scope string) string {
	return fmt.Sprintf("(%s) && JobStatus == 4", scope)
}

// sftpJobsFromRows reads what the mirror returned.
//
// A row that cannot be parsed, or that names no job, is skipped rather
// than failing the listing: one unreadable row should not cost somebody
// the whole directory.
func sftpJobsFromRows(rows []string) []sftpjobs.JobRef {
	out := make([]sftpjobs.JobRef, 0, len(rows))
	for _, row := range rows {
		ad, perr := classad.ParseOld(row)
		if perr != nil {
			continue
		}
		cluster, ok := ad.EvaluateAttrInt("ClusterId")
		if !ok {
			continue
		}
		proc, ok := ad.EvaluateAttrInt("ProcId")
		if !ok {
			continue
		}
		ref := sftpjobs.JobRef{Cluster: int(cluster), Proc: int(proc)}
		// Absent on a job the schedd has not finished accounting for.
		// Left zero rather than set to the epoch, so the directory shows
		// no time instead of 1970.
		if done, ok := ad.EvaluateAttrInt("CompletionDate"); ok && done > 0 {
			ref.Completed = time.Unix(done, 0).UTC()
		}
		out = append(out, ref)
	}
	return out
}
