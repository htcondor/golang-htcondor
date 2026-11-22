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

package sandboxfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	htcondor "github.com/htcondor/golang-htcondor"
)

// ErrNoSuchJob is returned when the constraint matched no job, which is
// how both "there is no such job" and "it is not yours" arrive: the
// schedd answers a constraint, not a question about one job.
//
// Wraps fs.ErrNotExist so a file surface can report it as a missing path
// without unwrapping anything.
var ErrNoSuchJob = fmt.Errorf("sandboxfs: no such job: %w", fs.ErrNotExist)

// ErrAmbiguousJob is returned when the constraint matched more than one
// job. The sandbox download prefixes names with "cluster.proc" as soon
// as it is sending more than one job, so the layout would describe files
// under names none of them has.
var ErrAmbiguousJob = errors.New("sandboxfs: the constraint matched more than one job")

// ScheddFetcher reads sandboxes through a schedd.
type ScheddFetcher struct {
	// Schedd returns the schedd holding a job.
	Schedd func(ctx context.Context, job Job) (*htcondor.Schedd, error)

	// Scope returns the ClassAd constraint for one job's sandbox,
	// already narrowed to what the caller may read.
	//
	// A hook rather than a constraint built here, because which jobs an
	// account may read is the API server's policy -- superuser mode,
	// project leads -- and a second implementation of it in this package
	// could only ever disagree with the first.
	Scope func(ctx context.Context, job Job) (string, error)
}

// FetchSandbox implements Fetcher.
func (f ScheddFetcher) FetchSandbox(ctx context.Context, job Job, w io.Writer,
	onPreamble func(size int64, known bool) error) error {
	// The account is half the cache key, and the credential comes from
	// the context -- so a Job naming one account on a context
	// authenticated as another would file one person's sandbox under the
	// other's name, and serve it to them afterwards. Nothing upstream
	// should get this wrong; checking means it cannot.
	who := htcondor.GetAuthenticatedUserFromContext(ctx)
	switch {
	case who == "":
		return errors.New("sandboxfs: the context names no authenticated user")
	case job.Account == "":
		return errors.New("sandboxfs: refusing to read a sandbox for no account")
	case who != job.Account:
		return fmt.Errorf("sandboxfs: asked to read job %s as %q on a context authenticated as %q",
			job, job.Account, who)
	}

	if f.Schedd == nil || f.Scope == nil {
		return errors.New("sandboxfs: ScheddFetcher needs both Schedd and Scope")
	}
	schedd, err := f.Schedd(ctx, job)
	if err != nil {
		return err
	}
	if schedd == nil {
		return fmt.Errorf("sandboxfs: no schedd for job %s", job)
	}
	constraint, err := f.Scope(ctx, job)
	if err != nil {
		return err
	}

	opts := htcondor.SandboxOptions{
		OnPreamble: onPreamble,
		OnJobCount: func(count int) error { return checkJobCount(job, count) },
	}
	return <-schedd.ReceiveJobSandboxWithOptions(ctx, constraint, w, opts)
}

// checkJobCount turns the number of jobs a constraint matched into an
// error, or nil for the one case that can be served.
func checkJobCount(job Job, count int) error {
	switch {
	case count == 0:
		// Zero matches is a valid, empty archive as far as the protocol
		// is concerned, so without this a missing job -- or one that is
		// not the caller's -- would read as a job with no output.
		return fmt.Errorf("%w: %s", ErrNoSuchJob, job)
	case count > 1:
		return fmt.Errorf("%w: %s matched %d", ErrAmbiguousJob, job, count)
	}
	return nil
}
