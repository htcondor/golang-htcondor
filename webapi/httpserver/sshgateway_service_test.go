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

	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
	"github.com/htcondor/golang-htcondor/webapi/sshgateway"
)

// TestSandboxStoreDefaults: an operator who sets nothing still gets a
// finite ceiling, because the ceiling guards the one unavoidable
// full-bandwidth read rather than any disk.
func TestSandboxStoreDefaults(t *testing.T) {
	h := &Handler{logger: testLogger(t)}
	store, err := h.newSandboxStore()
	if err != nil {
		t.Fatalf("newSandboxStore: %v", err)
	}
	if store == nil {
		t.Fatal("no store")
	}
	// With no cache directory nothing is cached, so every read streams.
	if got := store.CachedBytes(); got != 0 {
		t.Errorf("a store with no cache directory accounts %d bytes", got)
	}
}

func TestSandboxStoreUsesItsCacheDirectory(t *testing.T) {
	h := &Handler{logger: testLogger(t), sandboxCacheDir: t.TempDir(), sandboxCacheMB: 1}
	if _, err := h.newSandboxStore(); err != nil {
		t.Fatalf("newSandboxStore: %v", err)
	}
}

// TestSandboxStoreRefusesMultiAP: a path in this tree is
// "<cluster>.<proc>" and names no access point, so there is nothing to
// route on. Serving the wrong AP's job of the same id is worse than not
// serving it.
func TestSandboxStoreRefusesMultiAP(t *testing.T) {
	h := &Handler{logger: testLogger(t), multi: &multiAPMode{}}
	if _, err := h.newSandboxStore(); err == nil {
		t.Error("a multi-access-point gateway built a sandbox store")
	}
}

// TestSubsystemRefusesWhatItDoesNotServe keeps the hook narrow: it is
// reached for any service name the username reserved, and for any
// subsystem a client asks for.
func TestSubsystemRefusesWhatItDoesNotServe(t *testing.T) {
	h := &Handler{logger: testLogger(t)}
	store, err := h.newSandboxStore()
	if err != nil {
		t.Fatalf("newSandboxStore: %v", err)
	}
	h.sandboxStore = store

	for _, tc := range []struct {
		name, service, subsystem, wantErrHas string
	}{
		{"unknown service", "widgets", "sftp", "not a service"},
		{"wrong subsystem", "jobs", "shell-session", "sftp subsystem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := h.sshGatewaySubsystem(context.Background(), sshgateway.ServiceRequest{
				Account:   "alice",
				Service:   tc.service,
				Subsystem: tc.subsystem,
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErrHas) {
				t.Errorf("error = %v, want one mentioning %q", err, tc.wantErrHas)
			}
		})
	}
}

// TestSubsystemWithoutAStoreIsRefused covers the degraded case: the
// gateway still runs for shells, and the file service says so.
func TestSubsystemWithoutAStoreIsRefused(t *testing.T) {
	h := &Handler{logger: testLogger(t)} // no sandboxStore
	err := h.sshGatewaySubsystem(context.Background(), sshgateway.ServiceRequest{
		Account: "alice", Service: "jobs", Subsystem: "sftp",
	})
	if err == nil {
		t.Fatal("the file service ran without a store")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("error = %v, want it to say the service is unavailable", err)
	}
}

// TestSandboxScopeConstraint pins the constraint text, including the
// quoting of an account that would otherwise break the expression.
func TestSandboxScopeConstraint(t *testing.T) {
	for _, tc := range []struct {
		account string
		want    string
	}{
		{"alice", `ClusterId == 12 && ProcId == 3 && User == "alice"`},
		{`odd"name`, `ClusterId == 12 && ProcId == 3 && User == "odd\"name"`},
	} {
		got := fmtSandboxScope(sandboxfs.Job{Account: tc.account, Cluster: 12, Proc: 3})
		if got != tc.want {
			t.Errorf("scope for %q =\n  %s\nwant\n  %s", tc.account, got, tc.want)
		}
	}
}
