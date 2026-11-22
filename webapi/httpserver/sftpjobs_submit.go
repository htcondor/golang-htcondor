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

	"github.com/PelicanPlatform/classad/classad"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/sftpjobs"
	"github.com/htcondor/golang-htcondor/webapi/spool"
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

// SubmitTarball implements sftpjobs.Submitter.
//
// The same spooling submission as a template's, with the submit
// description coming out of the archive instead of the library -- and
// with one difference that matters: the archive can carry the
// executable, so transfer_executable may be true here. That is the
// whole point of the drop point. A template cannot do it, because a CSV
// carries no files.
//
// The archive goes to every proc. HTCondor spools input per job, so a
// `queue 20` submission needs the same tar sent twenty times, which is
// what webapi/spool's fan-out is for -- the same code the REST
// cluster-upload endpoint uses, with the same limits and the same
// per-proc accounting.
func (s templateSubmitter) SubmitTarball(ctx context.Context, account string,
	sub *sftpjobs.TarballSubmission) (int, error) {
	// As in SubmitRows, and first for the same reason.
	if who := htcondor.GetAuthenticatedUserFromContext(ctx); who != account {
		return 0, fmt.Errorf("asked to submit as %q on a context authenticated as %q", account, who)
	}
	if sub == nil || sub.Source == nil {
		return 0, errors.New("there is no archive to submit")
	}
	schedd := s.h.getSchedd()
	if schedd == nil {
		return 0, errors.New("no schedd is configured")
	}

	cluster, procs, err := schedd.SubmitRemote(ctx, sub.Submit)
	if err != nil {
		return 0, err
	}

	// From here on the jobs exist, so every failure takes them with it.
	if err := sftpjobs.ArchiveMissing(procs, sub.Files); err != nil {
		s.withdrawCluster(ctx, cluster, "the archive does not carry the jobs' input files")
		return 0, fmt.Errorf("%w; the %d jobs were withdrawn", err, len(procs))
	}

	src, ok := sub.Source.(spool.Source)
	if !ok {
		s.withdrawCluster(ctx, cluster, "the archive cannot be read back")
		return 0, fmt.Errorf("the archive cannot be spooled; the %d jobs were withdrawn", len(procs))
	}

	// Jobs that need no spool at all -- every input a URL, nothing to
	// transfer -- were released by SubmitRemote and must not be spooled
	// to: the schedd has nothing waiting for them.
	awaiting := procsAwaitingInput(procs)
	if len(awaiting) == 0 {
		s.h.logger.Info(logging.DestinationHTTP, "Submitted a dropped archive",
			"account", account, "cluster", cluster, "procs", len(procs), "spooled", 0)
		return cluster, nil
	}

	lim := s.h.spoolLimits()
	attempt, planned, err := spool.Plan(awaiting, src.Size(), lim)
	if err != nil {
		s.withdrawCluster(ctx, cluster, "the archive is too large to spool to these jobs")
		return 0, fmt.Errorf("%w; the %d jobs were withdrawn", err, len(procs))
	}
	if planned.Capped {
		// Partial progress is the right answer for a REST caller, who
		// can call again with the same tar. It is the wrong one here:
		// the archive is gone when this returns, so the procs left
		// behind would hold forever.
		s.withdrawCluster(ctx, cluster, "more jobs than one upload may spool")
		return 0, fmt.Errorf("this submission needs %d procs spooled, more than one upload may "+
			"serve (%d, or %d bytes in all); submit fewer jobs at a time, or use URLs in "+
			"transfer_input_files; the %d jobs were withdrawn",
			len(awaiting), lim.MaxProcs, lim.MaxVolume, len(procs))
	}

	res := spool.FanOut(ctx, attempt, src, lim, schedd.SpoolJobFilesFromTar)
	if n := res.Remaining(); n > 0 {
		s.withdrawCluster(ctx, cluster, "spooling the archive failed")
		return 0, fmt.Errorf("the schedd accepted the submission but %d of %d jobs could not be "+
			"spooled (%v); the %d jobs were withdrawn",
			n, len(awaiting), res.Failed, len(procs))
	}

	s.h.logger.Info(logging.DestinationHTTP, "Submitted a dropped archive",
		"account", account, "cluster", cluster, "procs", len(procs),
		"spooled", len(res.Spooled), "bytes", src.Size(), "files", len(sub.Files))
	return cluster, nil
}

// procsAwaitingInput is the procs still held for input spooling.
//
// SubmitRemote releases the ones with nothing to spool itself, and a
// spool call aimed at a released job fails -- so the fan-out has to be
// told which jobs are actually waiting rather than being handed the
// whole cluster.
func procsAwaitingInput(procs []*classad.ClassAd) []*classad.ClassAd {
	out := make([]*classad.ClassAd, 0, len(procs))
	for _, ad := range procs {
		if len(htcondor.SpoolInputAllowSet(ad)) > 0 {
			out = append(out, ad)
		}
	}
	return out
}
