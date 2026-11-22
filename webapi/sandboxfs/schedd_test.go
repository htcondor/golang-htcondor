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
	"io"
	"io/fs"
	"strings"
	"testing"

	htcondor "github.com/htcondor/golang-htcondor"
)

// TestScheddFetcherChecksTheAccount is the seam the whole account keying
// rests on: the credential comes from the context and the account is
// half the cache key, so a Job naming one account on a context
// authenticated as another would file one person's sandbox under the
// other's name and serve it to them afterwards.
//
// A refusal has to happen before the schedd is touched, or the mismatch
// would already have been acted on.
func TestScheddFetcherChecksTheAccount(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ctxUser    string
		account    string
		wantErrHas string
	}{
		{"matching", "alice", "alice", ""},
		{"mismatched", "alice", "bob", "authenticated as"},
		{"no user on the context", "", "alice", "no authenticated user"},
		{"no account on the job", "alice", "", "no account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialed := false
			f := ScheddFetcher{
				Schedd: func(context.Context, Job) (*htcondor.Schedd, error) {
					dialed = true
					return nil, errors.New("reached the schedd")
				},
				Scope: func(context.Context, Job) (string, error) { return "true", nil },
			}
			ctx := context.Background()
			if tc.ctxUser != "" {
				ctx = htcondor.WithAuthenticatedUser(ctx, tc.ctxUser)
			}
			err := f.FetchSandbox(ctx, Job{Account: tc.account, Cluster: 1}, io.Discard, nil)

			if tc.wantErrHas == "" {
				// Only the schedd stub should have stopped it.
				if !dialed {
					t.Fatal("a matching account did not reach the schedd")
				}
				return
			}
			if dialed {
				t.Error("the schedd was reached despite a bad account")
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErrHas) {
				t.Fatalf("error = %v, want one mentioning %q", err, tc.wantErrHas)
			}
		})
	}
}

func TestScheddFetcherNeedsItsHooks(t *testing.T) {
	ctx := htcondor.WithAuthenticatedUser(context.Background(), "alice")
	job := Job{Account: "alice", Cluster: 1}

	for _, tc := range []struct {
		name string
		f    ScheddFetcher
	}{
		{"no hooks", ScheddFetcher{}},
		{"no Scope", ScheddFetcher{Schedd: func(context.Context, Job) (*htcondor.Schedd, error) { return nil, nil }}},
		{"no Schedd", ScheddFetcher{Scope: func(context.Context, Job) (string, error) { return "true", nil }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.f.FetchSandbox(ctx, job, io.Discard, nil); err == nil {
				t.Error("FetchSandbox ran without its hooks")
			}
		})
	}
}

// TestCheckJobCount: a constraint matching nothing is a valid empty
// archive as far as the protocol goes, so without this a job that does
// not exist -- or is not the caller's -- reads as a job with no output.
func TestCheckJobCount(t *testing.T) {
	job := Job{Account: "alice", Cluster: 7, Proc: 2}

	err := checkJobCount(job, 0)
	if !errors.Is(err, ErrNoSuchJob) {
		t.Errorf("count 0 = %v, want ErrNoSuchJob", err)
	}
	// A file surface reports it as a missing path without unwrapping
	// anything of ours.
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("count 0 = %v, want it to wrap fs.ErrNotExist", err)
	}
	if err == nil || !strings.Contains(err.Error(), "7.2") {
		t.Errorf("count 0 = %v, want it to name the job", err)
	}

	if err := checkJobCount(job, 1); err != nil {
		t.Errorf("count 1 = %v, want nil", err)
	}

	// More than one job and the download prefixes names with
	// cluster.proc, so the layout would describe files under names none
	// of them has.
	if err := checkJobCount(job, 2); !errors.Is(err, ErrAmbiguousJob) {
		t.Errorf("count 2 = %v, want ErrAmbiguousJob", err)
	}
}
