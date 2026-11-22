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
	"io"

	"github.com/pkg/sftp"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
	"github.com/htcondor/golang-htcondor/webapi/sftpjobs"
	"github.com/htcondor/golang-htcondor/webapi/sshgateway"
)

// Defaults for the +jobs file service, used when the operator sets no
// number. Both are deliberately finite: see sandboxfs for why the
// sandbox ceiling matters more than the disk budget.
const (
	defaultSandboxCacheMB = 4096
	defaultSandboxMaxGB   = 8
)

// newSandboxStore builds the store the +jobs file service reads through.
//
// Nothing here is per-caller: the store is shared, and what keeps one
// account's output away from another is that every read names its
// account, which is half the cache key. The credential still comes from
// the channel's context, and ScheddFetcher refuses a job whose account
// is not the one that context authenticated.
func (h *Handler) newSandboxStore() (*sandboxfs.Store, error) {
	if h.multi != nil {
		// A path here is "<cluster>.<proc>" and names no access point,
		// so there is nothing to route on. Refused rather than guessed:
		// serving the wrong AP's job of the same id is worse than not
		// serving it.
		return nil, errors.New("the +jobs file service does not support a multi-access-point gateway")
	}

	cacheMB := h.sandboxCacheMB
	if cacheMB == 0 {
		cacheMB = defaultSandboxCacheMB
	}
	maxGB := h.sandboxMaxGB
	if maxGB == 0 {
		maxGB = defaultSandboxMaxGB
	}

	cfg := sandboxfs.Config{
		CacheDir:        h.sandboxCacheDir,
		MaxSandboxBytes: int64(maxGB) << 30,
		Logf: func(format string, args ...any) {
			h.logger.Debug(logging.DestinationHTTP, fmt.Sprintf(format, args...))
		},
	}
	if h.sandboxCacheDir == "" {
		// No directory means no byte cache: every read streams, which is
		// correct and slower.
		cfg.CacheBytes = 0
	} else {
		cfg.CacheBytes = int64(cacheMB) << 20
	}
	if h.sandboxMaxGB < 0 {
		cfg.MaxSandboxBytes = -1
	}
	if h.sandboxCacheMB < 0 {
		cfg.CacheBytes = 0
	}

	fetcher := sandboxfs.ScheddFetcher{
		Schedd: func(context.Context, sandboxfs.Job) (*htcondor.Schedd, error) {
			if sd := h.getSchedd(); sd != nil {
				return sd, nil
			}
			return nil, errors.New("no schedd is configured")
		},
		// The caller's own jobs, and only those. Deliberately not the
		// HTTP owner scope: superuser mode and project-lead widening are
		// both driven by an HTTP request, and there is none here, so
		// reusing that machinery would mean inventing the inputs it
		// reads.
		Scope: func(_ context.Context, job sandboxfs.Job) (string, error) {
			if job.Account == "" {
				return "", errors.New("no account to scope a sandbox read to")
			}
			return fmtSandboxScope(job), nil
		},
	}
	return sandboxfs.New(fetcher, cfg)
}

// sshGatewaySubsystem serves a service target's subsystem request.
//
// The context is the channel's: it carries the caller's HTCondor
// credential and names them, and it is cancelled when the channel goes
// away, which is what ends an abandoned transfer.
func (h *Handler) sshGatewaySubsystem(ctx context.Context, req sshgateway.ServiceRequest) error {
	if req.Service != sftpJobsService {
		return fmt.Errorf("+%s is not a service this access point offers", req.Service)
	}
	if req.Subsystem != "sftp" {
		return fmt.Errorf("+%s serves the sftp subsystem, not %q", req.Service, req.Subsystem)
	}
	if h.sandboxStore == nil {
		return errors.New("the job file service is not available on this access point")
	}

	fsys, err := sftpjobs.New(sftpjobs.Config{
		Store:   h.sandboxStore,
		Account: req.Account,
		Context: ctx,
		Jobs:    mirrorJobLister{h: h, account: req.Account},
		// Both or neither: without a submitter the tree would offer a
		// drop point that cannot submit.
		Templates: h.jobTemplates(),
		Submitter: h.jobTemplateSubmitter(),
		Logf: func(format string, args ...any) {
			h.logger.Debug(logging.DestinationHTTP, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		return err
	}

	h.logger.Info(logging.DestinationHTTP, "Serving the job file service",
		"account", req.Account, "service", req.Service)

	srv := sftp.NewRequestServer(req.Stream, fsys.Handlers())
	defer func() { _ = srv.Close() }()
	if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// jobTemplates is the template source, or nil when none is configured.
func (h *Handler) jobTemplates() sftpjobs.TemplateSource {
	if h.jobTemplateDir == "" {
		return nil
	}
	return sftpjobs.DirTemplates{Dir: h.jobTemplateDir}
}

// jobTemplateSubmitter pairs with it. Returned nil together with the
// source, so the tree is absent rather than half-present.
func (h *Handler) jobTemplateSubmitter() sftpjobs.Submitter {
	if h.jobTemplateDir == "" {
		return nil
	}
	return templateSubmitter{h: h}
}

// fmtSandboxScope is the constraint for one account's own job.
//
// The account is quoted as a ClassAd string literal rather than
// interpolated: it reaches here from an OAuth2 claim, and an account
// carrying a quote would otherwise end the literal early and leave the
// rest of it as expression text.
func fmtSandboxScope(job sandboxfs.Job) string {
	return fmt.Sprintf("ClusterId == %d && ProcId == %d && User == %s",
		job.Cluster, job.Proc, classadStringLit(job.Account))
}

// sftpJobsService is the service name the SSH username reserves.
// Mirrors interactive's reserved set, which is what makes a session of
// this name impossible rather than shadowed.
const sftpJobsService = "jobs"
