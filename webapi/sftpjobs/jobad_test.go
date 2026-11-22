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

package sftpjobs

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PelicanPlatform/classad/classad"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
)

// fakeAds stands in for the mirror, counting reads so a test can tell
// what a listing cost.
type fakeAds struct {
	reads atomic.Int64
	err   error
	ad    *classad.ClassAd
}

func (a *fakeAds) JobAd(_ context.Context, cluster, proc int) (*classad.ClassAd, error) {
	a.reads.Add(1)
	if a.err != nil {
		return nil, a.err
	}
	if a.ad != nil {
		return a.ad, nil
	}
	ad := classad.New()
	_ = ad.Set("ClusterId", int64(cluster))
	_ = ad.Set("ProcId", int64(proc))
	_ = ad.Set("Owner", "alice")
	_ = ad.Set("JobStatus", int64(4))
	_ = ad.Set("ExitCode", int64(0))
	_ = ad.Set("Cmd", "/bin/echo")
	_ = ad.Set("MemoryUsage", int64(37))
	_ = ad.Set("RemoteWallClockTime", 12.5)
	return ad, nil
}

// newAdFS is a view with a sandbox and an ad source.
func newAdFS(t *testing.T, ads JobAdReader) (*FS, *fakeSandbox) {
	t.Helper()
	sb := newFakeSandbox(sampleFiles(), "sub")
	store, err := sandboxfs.New(sb, sandboxfs.Config{Logf: t.Logf})
	if err != nil {
		t.Fatalf("sandboxfs.New: %v", err)
	}
	f, err := New(Config{
		Store:   store,
		Account: testAccount,
		Context: htcondor.WithAuthenticatedUser(context.Background(), testAccount),
		Jobs:    fixedJobs{{Cluster: 12, Proc: 0}},
		JobAds:  ads,
		Logf:    t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f, sb
}

// TestAJobListingCostsNoSandbox is the reason the output moved under
// data/.
//
// A spooled sandbox can only be read whole, so putting a size next to a
// file's name costs a transfer of the entire sandbox. With the files one
// level down, `ls -lh` of a job shows a directory and an ad and reads
// nothing: the unavoidable pass happens when somebody asks for the
// output, which is also when they are expecting to wait.
func TestAJobListingCostsNoSandbox(t *testing.T) {
	ads := &fakeAds{}
	f, sb := newAdFS(t, ads)

	got := listNames(t, f, "/outputs/12.0")
	want := []string{"data/", JobAdFileName}
	if len(got) != len(want) {
		t.Fatalf("job directory = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("job directory = %v, want %v", got, want)
		}
	}
	if jobs := sb.jobs(); len(jobs) != 0 {
		t.Errorf("listing a job read %d sandboxes, want 0", len(jobs))
	}

	// And the level that does cost one says so by reading it.
	listNames(t, f, "/outputs/12.0/data")
	if jobs := sb.jobs(); len(jobs) == 0 {
		t.Error("listing the output read no sandbox, so it cannot have sized anything")
	}
}

// TestTheAdIsReadAtMostOncePerJob: a client stats a file, lists the
// directory that showed it, then opens it -- three questions about one
// ad, which is three mirror queries without the cache.
func TestTheAdIsReadAtMostOncePerJob(t *testing.T) {
	ads := &fakeAds{}
	f, _ := newAdFS(t, ads)

	listNames(t, f, "/outputs/12.0")
	if _, err := statOne(t, f, "/outputs/12.0/"+JobAdFileName); err != nil {
		t.Fatalf("stat job.ad: %v", err)
	}
	text := readStatus(t, f, "/outputs/12.0/"+JobAdFileName)
	if !strings.Contains(text, "ClusterId = 12") {
		t.Errorf("job.ad = %q", text)
	}
	if n := ads.reads.Load(); n != 1 {
		t.Errorf("the ad was read %d times for one job, want 1", n)
	}
}

// TestTheAdIsOldClassAdText: the format HTCondor's own starter writes
// as .job.ad and `condor_q -l` prints, so somebody who has seen either
// can read this.
func TestTheAdIsOldClassAdText(t *testing.T) {
	f, _ := newAdFS(t, &fakeAds{})
	text := readStatus(t, f, "/outputs/12.0/"+JobAdFileName)

	for _, want := range []string{
		"ClusterId = 12",
		"ProcId = 0",
		`Owner = "alice"`,
		"ExitCode = 0",
		"MemoryUsage = 37",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("job.ad does not carry %q:\n%s", want, text)
		}
	}
	// Sorted, and one attribute per line, which is what makes it
	// greppable.
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) < 5 {
		t.Fatalf("job.ad has %d lines:\n%s", len(lines), text)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i-1] > lines[i] {
			t.Errorf("job.ad is not sorted: %q comes before %q", lines[i-1], lines[i])
			break
		}
	}
	// Ends in a newline, or cat runs the last attribute into the shell
	// prompt.
	if !strings.HasSuffix(text, "\n") {
		t.Error("job.ad does not end in a newline")
	}
}

// TestAnUnreadableAdLeavesTheOutput: the ad is a convenience, like the
// /outputs listing. An access point that cannot answer for it must
// still serve what the job produced.
func TestAnUnreadableAdLeavesTheOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		ads  JobAdReader
	}{
		{"no reader at all", nil},
		{"a reader that fails", &fakeAds{err: context.DeadlineExceeded}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newAdFS(t, tc.ads)

			got := listNames(t, f, "/outputs/12.0")
			if len(got) != 1 || got[0] != "data/" {
				t.Errorf("job directory = %v, want just the output", got)
			}
			if _, err := statOne(t, f, "/outputs/12.0/"+JobAdFileName); err == nil {
				t.Error("job.ad stats as present with no ad behind it")
			}
			// The output is untouched by any of that.
			if names := listNames(t, f, "/outputs/12.0/data"); len(names) == 0 {
				t.Error("the output is not listed either")
			}
		})
	}
}

// TestTheArchiveIsNotListedBesideTheOutput: `get -r` on a job directory
// would otherwise fetch every file twice, once loose under data/ and
// once inside data.tar -- and sizing the archive for the listing would
// cost the sandbox transfer the level above exists to avoid.
func TestTheArchiveIsNotListedBesideTheOutput(t *testing.T) {
	f, _ := newAdFS(t, &fakeAds{})
	for _, name := range listNames(t, f, "/outputs/12.0") {
		if strings.HasSuffix(name, ".tar") {
			t.Errorf("the archive is listed in the job directory as %q", name)
		}
	}
	// Addressable all the same.
	if _, err := statOne(t, f, "/outputs/12.0/data.tar"); err != nil {
		t.Errorf("the archive is not reachable by name: %v", err)
	}
}

// TestClientReadsTheWholeAd through a real client, because a size and a
// body that disagree do not fail: an SFTP client reads the length the
// listing gave it and stops, so an ad reported shorter than it is comes
// back truncated and looks like a complete file. The per-channel cache
// is what keeps the two answers the same, and this is what would notice
// if it stopped.
func TestClientReadsTheWholeAd(t *testing.T) {
	f, _ := newAdFS(t, &fakeAds{})
	c := dialFS(t, f)

	path := "/outputs/12.0/" + JobAdFileName
	fi, err := c.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	rc, err := c.Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if int64(len(body)) != fi.Size() {
		t.Errorf("read %d bytes for a file stat'd at %d: a client would truncate here",
			len(body), fi.Size())
	}
	if !strings.Contains(string(body), "ClusterId = 12") {
		t.Errorf("job.ad = %q", body)
	}

	// And the job directory a client sees.
	entries, err := c.ReadDir("/outputs/12.0")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var sawAd, sawData bool
	for _, e := range entries {
		switch {
		case e.Name() == JobAdFileName:
			sawAd = true
			if e.Size() != fi.Size() {
				t.Errorf("the listing says %d bytes, the stat says %d", e.Size(), fi.Size())
			}
		case e.Name() == dataDirName && e.IsDir():
			sawData = true
		default:
			t.Errorf("unexpected entry %q in a job directory", e.Name())
		}
	}
	if !sawAd || !sawData {
		t.Errorf("job directory = %v, want the ad and the output", entries)
	}
}
