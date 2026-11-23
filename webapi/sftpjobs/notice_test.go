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
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
)

// brokenJobs is a lister that cannot answer -- no job index configured,
// or one that refused this caller, which are the two ways this happens
// in a deployment.
type brokenJobs struct{ err error }

func (b brokenJobs) ReadableJobs(context.Context) ([]JobRef, error) { return nil, b.err }

// warnings records what the service asked an operator to see.
type warnings struct {
	mu   sync.Mutex
	msgs []string
}

func (w *warnings) hook(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, fmt.Sprintf(format, args...))
}

func (w *warnings) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.msgs...)
}

// noticeFS is a view whose job listing fails, with the operator's
// warnings captured.
func noticeFS(t *testing.T, err error) (*FS, *warnings) {
	t.Helper()
	store, serr := sandboxfs.New(newFakeSandbox(sampleFiles(), "sub"),
		sandboxfs.Config{Logf: t.Logf})
	if serr != nil {
		t.Fatalf("sandboxfs.New: %v", serr)
	}
	w := &warnings{}
	f, ferr := New(Config{
		Store:   store,
		Account: testAccount,
		Context: htcondor.WithAuthenticatedUser(context.Background(), testAccount),
		Jobs:    brokenJobs{err: err},
		Logf:    t.Logf,
		Warnf:   w.hook,
	})
	if ferr != nil {
		t.Fatalf("New: %v", ferr)
	}
	return f, w
}

// TestABrokenJobListingExplainsItself is the finding this covers: a
// failed readdir reaches the user as the word "Failure" and nothing
// else, because sftp(1) prints the status code and discards the server's
// message. So the listing succeeds and carries the reason as a file.
func TestABrokenJobListingExplainsItself(t *testing.T) {
	const reason = "no htcondordb mirror is configured, so this listing cannot be built"
	f, warned := noticeFS(t, errors.New(reason))
	client := dialFS(t, f)

	entries, err := client.ReadDir("/outputs")
	if err != nil {
		// The whole point: a client must not be told "Failure" here.
		t.Fatalf("ReadDir(/outputs) failed, so the reason reached nobody: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != OutputsNoticeName {
		var got []string
		for _, e := range entries {
			got = append(got, e.Name())
		}
		t.Fatalf("/outputs = %v, want just %s", got, OutputsNoticeName)
	}
	if entries[0].IsDir() {
		t.Error("the notice is a directory, so nobody can read it")
	}

	rc, err := client.Open("/outputs/" + OutputsNoticeName)
	if err != nil {
		t.Fatalf("opening the notice: %v", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading the notice: %v", err)
	}
	text := string(body)

	// The reason, verbatim: an operator's own words are the diagnosis,
	// and this file paraphrasing them would lose it.
	if !strings.Contains(text, reason) {
		t.Errorf("the notice does not carry the reason:\n%s", text)
	}
	// And what still works, because it does: a job the user can name is
	// reachable without the listing.
	if !strings.Contains(text, "/outputs/1234.0/data") {
		t.Errorf("the notice does not say a job can still be fetched:\n%s", text)
	}
	// The size the client was given has to match what it read, or a
	// client that trusts the stat truncates the file.
	if int64(len(body)) != entries[0].Size() {
		t.Errorf("the notice stats as %d bytes and reads as %d", entries[0].Size(), len(body))
	}

	// The operator's half. This was debug-only once, which on a
	// deployment with debug logging off meant the reason existed
	// nowhere at all.
	var found bool
	for _, m := range warned.all() {
		if strings.Contains(m, reason) {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning carried the reason; the operator has nothing to go on: %v", warned.all())
	}
}

// TestTheNoticeIsAbsentWhenTheListingWorks keeps the notice from
// becoming furniture: a file that is always there is one nobody reads.
func TestTheNoticeIsAbsentWhenTheListingWorks(t *testing.T) {
	f := newTestFS(t, false, fixedJobs{{Cluster: 1, Proc: 0}})
	client := dialFS(t, f.FS)

	entries, err := client.ReadDir("/outputs")
	if err != nil {
		t.Fatalf("ReadDir(/outputs): %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "1.0" {
		var got []string
		for _, e := range entries {
			got = append(got, e.Name())
		}
		t.Fatalf("/outputs = %v, want just the job", got)
	}
	if _, err := client.Stat("/outputs/" + OutputsNoticeName); err == nil {
		t.Error("the notice exists while the listing works")
	}
	if _, err := client.Open("/outputs/" + OutputsNoticeName); err == nil {
		t.Error("the notice is readable while the listing works")
	}
}

// TestAJobIsStillReachableWhileTheListingIsBroken covers the other half
// of the notice's claim. A lister that cannot answer is treated as one
// that is not there, so output somebody can name still comes back --
// the listing is the convenience, and the store's scope, not the
// lister, is what keeps another account's job out of reach.
func TestAJobIsStillReachableWhileTheListingIsBroken(t *testing.T) {
	f, _ := noticeFS(t, errors.New("the job index is down"))
	client := dialFS(t, f)

	if _, err := client.Stat("/outputs/7.2"); err != nil {
		t.Fatalf("stat of a named job: %v", err)
	}
	entries, err := client.ReadDir("/outputs/7.2/data")
	if err != nil {
		t.Fatalf("listing a named job's output: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("a named job's output listed nothing")
	}
	rc, err := client.Open("/outputs/7.2/data/stdout")
	if err != nil {
		t.Fatalf("opening a named job's file: %v", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading a named job's file: %v", err)
	}
	if string(body) != "hello from the job\n" {
		t.Errorf("stdout = %q", body)
	}
}

// TestTheNoticeIsNotADirectory checks the shapes a client will try on
// it, since it appears in a listing a recursive get walks.
func TestTheNoticeIsNotADirectory(t *testing.T) {
	f, _ := noticeFS(t, errors.New("the job index is down"))
	client := dialFS(t, f)

	if _, err := client.ReadDir("/outputs/" + OutputsNoticeName); err == nil {
		t.Error("the notice listed as a directory")
	}
	// Nothing lives under it.
	if _, err := client.Stat("/outputs/" + OutputsNoticeName + "/x"); err == nil {
		t.Error("a path under the notice exists")
	}
}
