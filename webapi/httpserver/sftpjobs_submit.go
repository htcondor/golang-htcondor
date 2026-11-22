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
	"strings"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/sftpjobs"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// templateSubmitter submits a CSV's rows against a template.
type templateSubmitter struct{ h *Handler }

// SubmitRows implements sftpjobs.Submitter.
//
// SubmitRemote, not Submit, and that is load-bearing. Output read
// through the file service comes out of the schedd's spool, so the
// submission has to be a spooling one -- which is also what sets
// LeaveJobInQueue, and a job that leaves the queue has no spool left to
// read. Submit does neither.
//
// The cost of that choice is the spool hold: a spooling submission whose
// executable needs transferring parks on HoldReasonCode 16 waiting for
// an upload a CSV never carries. sftpjobs.ValidateTemplate refuses such
// a template before any of this runs, which is the only reason this is
// safe to call.
//
// What the hold is good for is the template's own input files. The web
// submit page delivers those with a second call, to
// /api/v1/jobs/{id}/input; a dropped CSV has no second call, so they go
// the way a spooling submission takes files -- spooled out of memory,
// in the one call that also ends the hold. Jobs that ask for no files
// are released by SubmitRemote itself and never reach that code.
func (s templateSubmitter) SubmitRows(ctx context.Context, account string,
	t templates.Template, sub *sftpjobs.Submission) (int, error) {
	// First, and before anything is built or dialed. The context is the
	// SSH channel's: it carries the caller's credential, so the schedd
	// attributes the jobs to whoever that is. Submitting as somebody
	// else would charge them to the wrong person, and that is worth
	// refusing whether or not a schedd happens to be configured.
	if who := htcondor.GetAuthenticatedUserFromContext(ctx); who != account {
		return 0, fmt.Errorf("asked to submit as %q on a context authenticated as %q", account, who)
	}

	text, err := sftpjobs.BuildSubmit(t, sub)
	if err != nil {
		return 0, err
	}
	// Before the submission, because the only other place to find this
	// out is after a cluster has been accepted: every proc is spooled
	// the template's files, so a small CSV can ask for a very large
	// transfer. The ceiling is the operator's for any one upload.
	if err := sftpjobs.CheckSpoolVolume(t, len(sub.Rows), s.h.spoolLimits().MaxVolume); err != nil {
		return 0, err
	}
	schedd := s.h.getSchedd()
	if schedd == nil {
		return 0, errors.New("no schedd is configured")
	}

	cluster, procs, err := schedd.SubmitRemote(ctx, text)
	if err != nil {
		return 0, err
	}

	// From here on the jobs exist, so a failure has to take them with
	// it: they are held for input, and input is what is failing.
	plan, err := sftpjobs.PlanSpool(t, procs)
	if err != nil {
		s.withdrawCluster(ctx, cluster, "input files cannot be delivered")
		return 0, fmt.Errorf("%w; the %d jobs were withdrawn", err, len(procs))
	}
	if !plan.Empty() {
		if err := schedd.SpoolJobFilesFromFS(ctx, procs, plan.Files); err != nil {
			s.withdrawCluster(ctx, cluster, "spooling the template's input files failed")
			return 0, fmt.Errorf("the schedd accepted the submission but spooling %s failed: %w; "+
				"the %d jobs were withdrawn", strings.Join(plan.Names, ", "), err, len(procs))
		}
	}

	s.h.logger.Info(logging.DestinationHTTP, "Submitted a job template",
		"account", account, "template", t.ID,
		"cluster", cluster, "rows", len(sub.Rows), "procs", len(procs),
		"spooled", strings.Join(plan.Names, ","))
	return cluster, nil
}

// withdrawCluster removes a cluster this submission can no longer
// finish.
//
// A job held on HoldReasonCode 16 for input that is not coming is worse
// than no job at all: it sits in the queue forever, it is not obviously
// broken to anybody looking at it, and the person who dropped the CSV
// has been told the submission failed. Removing it is best-effort --
// the submission is already being reported as failed, and a second
// error about the cleanup would bury the first.
func (s templateSubmitter) withdrawCluster(ctx context.Context, cluster int, why string) {
	constraint := fmt.Sprintf("ClusterId == %d", cluster)
	if _, err := s.h.getSchedd().RemoveJobs(ctx, constraint, why); err != nil {
		s.h.logger.Warn(logging.DestinationHTTP, "withdrawing a template submission failed",
			"cluster", cluster, "reason", why, "error", err)
	}
}
