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

package htcondor

import (
	"strings"
	"testing"

	"github.com/PelicanPlatform/classad/classad"
)

// TestURLOnlyInputNeedsNoSpool pins which submissions SubmitRemote
// leaves in the spooling hold waiting for an upload.
//
// A job whose transfer_input_files are all URLs has nothing anyone can
// upload: the execute node fetches them itself, and the spooler's own
// allow-set leaves them out for that reason. Counting them as input to
// spool left such a job held on HoldReasonCode 16 until somebody
// noticed -- no caller could release it, because every spool call
// answered "every entry in TransferInput is a URL".
//
// The pairing with SpoolInputAllowSet is the invariant worth holding:
// a job is left waiting for a spool exactly when a spool has something
// to send it.
func TestURLOnlyInputNeedsNoSpool(t *testing.T) {
	for _, tc := range []struct {
		name          string
		submit        string
		needsSpooling bool
		wantAllow     []string
	}{
		{
			name:          "no inputs at all",
			submit:        "executable = /bin/true\ntransfer_executable = false\nqueue\n",
			needsSpooling: false,
		},
		{
			name:          "one URL",
			submit:        "executable = /bin/true\ntransfer_executable = false\ntransfer_input_files = https://example.com/d.tgz\nqueue\n",
			needsSpooling: false,
		},
		{
			name: "several URLs, several schemes",
			submit: "executable = /bin/true\ntransfer_executable = false\n" +
				"transfer_input_files = https://example.com/a, osdf:///b, s3://c/d\nqueue\n",
			needsSpooling: false,
		},
		{
			name:          "a file among the URLs",
			submit:        "executable = /bin/true\ntransfer_executable = false\ntransfer_input_files = https://example.com/a, run.sh\nqueue\n",
			needsSpooling: true,
			wantAllow:     []string{"run.sh"},
		},
		{
			name:          "the executable itself",
			submit:        "executable = run.sh\nqueue\n",
			needsSpooling: true,
			wantAllow:     []string{"run.sh"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sf, err := ParseSubmitFile(strings.NewReader(tc.submit))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			ad, err := sf.MakeJobAd(JobID{Cluster: 1, Proc: 0}, nil)
			if err != nil {
				t.Fatalf("MakeJobAd: %v", err)
			}
			if got := configureRemoteJobAd(ad, false); got != tc.needsSpooling {
				t.Errorf("needsSpooling = %v, want %v", got, tc.needsSpooling)
			}
			allow := SpoolInputAllowSet(ad)
			if len(allow) != len(tc.wantAllow) {
				t.Fatalf("allow-set = %v, want %v", allow, tc.wantAllow)
			}
			for i := range allow {
				if allow[i] != tc.wantAllow[i] {
					t.Fatalf("allow-set = %v, want %v", allow, tc.wantAllow)
				}
			}
			// The invariant: held for spooling iff a spool has
			// something to send.
			if (len(allow) > 0) != tc.needsSpooling {
				t.Errorf("allow-set %v and needsSpooling %v disagree", allow, tc.needsSpooling)
			}
		})
	}
}

// TestTransferInputHasFileIgnoresEmptyEntries: an unset macro expands
// to nothing, so "a, $(b), c" reaches the ad with an empty entry in the
// middle. An empty name is not a file to spool, and treating it as one
// would put a job that needs no spool into the spooling hold.
func TestTransferInputHasFileIgnoresEmptyEntries(t *testing.T) {
	ad := classad.New()
	_ = ad.Set("TransferInput", "https://example.com/a, , ")
	if transferInputHasFile(ad) {
		t.Error("an empty list entry was taken for a file to spool")
	}
	_ = ad.Set("TransferInput", "")
	if transferInputHasFile(ad) {
		t.Error("an empty TransferInput was taken for a file to spool")
	}
}
