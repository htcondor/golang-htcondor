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
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/PelicanPlatform/classad/classad"
	"github.com/htcondor/cedar/stream"
)

// TestSpoolWireNameFollowsTheScheddRewrite pins the name a spooled input
// is sent under against the rewrite the schedd applies to its own copy
// of the job ad as it commits the spool.
//
// The oracle is qmgmt.cpp rewriteSpooledJobAd, which reduces each
// TransferInput entry to condor_basename and repoints Iwd at the spool
// directory, keeping the path only for a RELATIVE entry when the job set
// PreserveRelativePaths. C++ DoUpload sends the matching name
// (file_transfer.cpp: "Strip any path information; if we wanted to keep
// it, it would have been set in dest_dir", and dest_dir is set only in
// the preserve case).
//
// Sending the entry verbatim -- which this package did -- agrees with
// the schedd only for the preserve minority. For the default it writes
// <spool>/subdir/input3.txt while the committed ad names input3.txt in
// <spool>: the upload succeeds, because the receiver creates the parent
// directory, the spooling hold clears, and the job is held at execute
// time on an input that is present under a name nothing references.
func TestSpoolWireNameFollowsTheScheddRewrite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		entry    string
		preserve bool // PreserveRelativePaths, and whether it is set at all
		setPres  bool
		want     string
	}{
		{name: "bare basename is unchanged", entry: "input.txt", want: "input.txt"},
		{
			name:  "relative entry with a directory component is flattened",
			entry: "subdir/input3.txt", want: "input3.txt",
		},
		{
			name:  "deeper relative path is flattened too",
			entry: "a/b/c/input.txt", want: "input.txt",
		},
		{
			name:  "absolute entry is flattened",
			entry: "/home/alice/data/input.txt", want: "input.txt",
		},
		{
			name:  "PreserveRelativePaths = false is the default behaviour",
			entry: "subdir/input3.txt", preserve: false, setPres: true, want: "input3.txt",
		},
		{
			name:  "PreserveRelativePaths keeps a relative path",
			entry: "subdir/input3.txt", preserve: true, setPres: true, want: "subdir/input3.txt",
		},
		{
			// C++ gates the preserve branch on !fullpath(entry), so an
			// absolute entry is flattened even with the knob set.
			name:  "PreserveRelativePaths does not apply to an absolute entry",
			entry: "/home/alice/data/input.txt", preserve: true, setPres: true, want: "input.txt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ad := classad.New()
			if tc.setPres {
				_ = ad.Set("PreserveRelativePaths", tc.preserve)
			}
			if got := spoolWireName(ad, tc.entry); got != tc.want {
				t.Errorf("spoolWireName(%q) = %q, want %q", tc.entry, got, tc.want)
			}
		})
	}
}

// TestSpoolAllowSetKeepsSubmitNamesAndFlattensWireNames holds the two
// halves of the allow-set apart: it is keyed by the name the submitter
// supplies a file under -- the TransferInput entry as written, which is
// also the name it carries in an uploaded tar -- and maps to the name
// the transfer sends it on as.
//
// Collapsing the two is what SpoolInputAllowSet's callers would notice:
// they tell a person redeeming an upload URL which names to use, and
// that has to be the submit-file spelling.
func TestSpoolAllowSetKeepsSubmitNamesAndFlattensWireNames(t *testing.T) {
	ad := classad.New()
	_ = ad.Set("TransferInput", "input.txt, subdir/input3.txt, /home/alice/abs.txt")
	_ = ad.Set("TransferExecutable", false)
	_ = ad.Set("In", "/home/alice/data.in")

	set, urls := inputFilesAndURLs(ad)
	if len(urls) != 0 {
		t.Errorf("urls = %v, want none", urls)
	}
	want := map[string]string{
		"input.txt":           "input.txt",
		"subdir/input3.txt":   "input3.txt",
		"/home/alice/abs.txt": "abs.txt",
		"data.in":             "data.in",
	}
	if len(set) != len(want) {
		t.Fatalf("allow-set = %v, want %v", set, want)
	}
	for supplied, wire := range want {
		got, ok := set[supplied]
		if !ok {
			t.Errorf("allow-set is missing %q", supplied)
			continue
		}
		if got != wire {
			t.Errorf("allow-set[%q] = %q, want wire name %q", supplied, got, wire)
		}
	}

	// And the uploader-facing answer is still the submit spelling.
	allow := SpoolInputAllowSet(ad)
	wantAllow := []string{"/home/alice/abs.txt", "data.in", "input.txt", "subdir/input3.txt"}
	if !equalStringSlices(allow, wantAllow) {
		t.Errorf("SpoolInputAllowSet = %v, want %v", allow, wantAllow)
	}
}

// sendTarWithAllowMap is sendTarToSink for an allow-set whose supplied
// names and wire names differ.
func sendTarWithAllowMap(t *testing.T, allowed map[string]string, tarBytes []byte) []byte {
	t.Helper()
	c1, c2 := net.Pipe()
	_ = c1.SetDeadline(time.Now().Add(300 * time.Millisecond))
	var sent bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&sent, c2)
	}()

	id := procID{cluster: 1, proc: 0}
	info := map[procID]*jobInfo{id: {inputFiles: allowed, jobID: id}}
	_ = (&Schedd{}).sendJobFilesFromTar(context.Background(), stream.NewStream(c1),
		bytes.NewReader(tarBytes), info, []procID{id}, true)
	_ = c1.Close()
	wg.Wait()
	_ = c2.Close()
	return sent.Bytes()
}

// TestSpoolTarSendsTheFlattenedName is the end-to-end form of the above:
// a tar naming subdir/input3.txt must reach the schedd as input3.txt,
// and must not ask it to create a subdir that nothing will be written
// into.
func TestSpoolTarSendsTheFlattenedName(t *testing.T) {
	entries := []tarEntry{
		{name: "subdir/", dir: true},
		{name: "subdir/input3.txt", data: "three"},
	}

	sent := sendTarWithAllowMap(t, map[string]string{"subdir/input3.txt": "input3.txt"},
		buildTar(t, entries))
	if !bytes.Contains(sent, []byte("input3.txt")) {
		t.Error("the file was not sent at all")
	}
	if bytes.Contains(sent, []byte("subdir")) {
		t.Error("the directory-qualified name reached the schedd; it names input3.txt in the spool root")
	}

	// With PreserveRelativePaths the wire name keeps its path, and the
	// directory the file goes in is created.
	sent = sendTarWithAllowMap(t, map[string]string{"subdir/input3.txt": "subdir/input3.txt"},
		buildTar(t, entries))
	if !bytes.Contains(sent, []byte("subdir/input3.txt")) {
		t.Error("the preserved relative path did not reach the schedd")
	}
}
