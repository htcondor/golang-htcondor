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

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/sftpjobs"
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
func (s templateSubmitter) SubmitRows(ctx context.Context, account string,
	t sftpjobs.Template, sub *sftpjobs.Submission) (int, error) {
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
	schedd := s.h.getSchedd()
	if schedd == nil {
		return 0, errors.New("no schedd is configured")
	}

	cluster, procs, err := schedd.SubmitRemote(ctx, text)
	if err != nil {
		return 0, err
	}
	s.h.logger.Info(logging.DestinationHTTP, "Submitted a job template",
		"account", account, "template", t.Ref.ID(),
		"cluster", cluster, "rows", len(sub.Rows), "procs", len(procs))
	return cluster, nil
}
