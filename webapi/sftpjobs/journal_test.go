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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// A session is one SFTP connection. Two of them over one journal is what
// a client that drops a file and reconnects looks like to this service,
// and is the shape both findings below live in.
func newSession(t *testing.T, account string, j *UploadJournal, sub Submitter) *FS {
	t.Helper()
	store, err := sandboxfs.New(newFakeSandbox(nil), sandboxfs.Config{Logf: t.Logf})
	if err != nil {
		t.Fatalf("sandboxfs.New: %v", err)
	}
	f, err := New(Config{
		Store:     store,
		Account:   account,
		Context:   htcondor.WithAuthenticatedUser(context.Background(), account),
		Templates: fakeCatalog{public: []templates.Template{blastTemplate()}},
		Submitter: sub,
		Uploads:   j,
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

// rewind ages every entry, so an expiry test does not sleep through a
// retention measured in days.
func rewind(j *UploadJournal, by time.Duration) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for k, st := range j.status {
		st.At = st.At.Add(-by)
		j.status[k] = st
	}
	for k, s := range j.submitted {
		s.at = s.at.Add(-by)
		j.submitted[k] = s
	}
}

const sampleCSV = "sample,reads\nHG001,a.fastq\nHG002,b.fastq\n"

// TestStatusSurvivesAReconnect is the finding: `sftp -b` puts the file
// and closes the channel, so the status entry explaining what happened
// used to go away with the session that produced it, and the next
// connection found an empty status/ directory.
func TestStatusSurvivesAReconnect(t *testing.T) {
	j, sub := NewUploadJournal(), &recordingSubmitter{}

	first := newSession(t, testAccount, j, sub)
	if err := writeCSV(t, first, blastDir+"/submit/run.csv", sampleCSV); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// A new connection, which is what a client that ran one command and
	// came back is.
	second := newSession(t, testAccount, j, sub)
	if got := listNames(t, second, blastDir+"/status"); len(got) != 1 || got[0] != "run.csv" {
		t.Fatalf("status/ in a new session = %v, want [run.csv]", got)
	}
	st, ok := second.statusFor("blast", "run.csv")
	if !ok {
		t.Fatal("no status entry in the second session")
	}
	if st.State != stateSubmitted || st.Rows != 2 {
		t.Errorf("status = %+v, want 2 submitted rows", st)
	}
	text := readStatus(t, second, blastDir+"/status/run.csv")
	if !strings.Contains(text, "state: "+stateSubmitted) {
		t.Errorf("status file = %q", text)
	}
}

// TestARetryOverANewConnectionIsOneSubmission is the other half. The
// same bytes dropped twice are one submission -- but deduplication used
// to be per-connection, and a client that lost its connection mid-put
// and ran the command again was a new connection, which is exactly the
// case the rule exists for.
func TestARetryOverANewConnectionIsOneSubmission(t *testing.T) {
	j, sub := NewUploadJournal(), &recordingSubmitter{}

	if err := writeCSV(t, newSession(t, testAccount, j, sub),
		blastDir+"/submit/run.csv", sampleCSV); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	// The retry: a new connection, and the client may well pick a new
	// name for it, because the bytes are what makes it a repeat.
	if err := writeCSV(t, newSession(t, testAccount, j, sub),
		blastDir+"/submit/run-again.csv", sampleCSV); err != nil {
		t.Fatalf("retry: %v", err)
	}

	if sub.count() != 1 {
		t.Fatalf("submitted %d times, want 1: the retry doubled the jobs", sub.count())
	}
	third := newSession(t, testAccount, j, sub)
	st, ok := third.statusFor("blast", "run-again.csv")
	if !ok {
		t.Fatal("the retry left no status entry")
	}
	if st.State != stateDuplicate {
		t.Errorf("retry state = %q, want %q", st.State, stateDuplicate)
	}
	if !strings.Contains(st.Err, "already submitted") {
		t.Errorf("the retry's status does not say why: %q", st.Err)
	}
}

// TestADeliberateRerunIsNotRefusedForever is why the dedup window is
// shorter than the retention. Absorbing a retry is minutes' work;
// refusing somebody's identical batch tomorrow, and calling their own
// file a duplicate, is not.
func TestADeliberateRerunIsNotRefusedForever(t *testing.T) {
	j, sub := NewUploadJournal(), &recordingSubmitter{}

	if err := writeCSV(t, newSession(t, testAccount, j, sub),
		blastDir+"/submit/run.csv", sampleCSV); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	// Past the dedup window, still well inside the retention.
	rewind(j, DefaultDedupWindow+time.Minute)

	session := newSession(t, testAccount, j, sub)
	if err := writeCSV(t, session, blastDir+"/submit/rerun.csv", sampleCSV); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if sub.count() != 2 {
		t.Fatalf("submitted %d times, want 2: the rerun was refused as a duplicate", sub.count())
	}
	// And the first upload's answer is still readable, because the
	// retention is the longer of the two.
	if _, ok := session.statusFor("blast", "run.csv"); !ok {
		t.Error("the first upload's status expired with the dedup window")
	}
}

// TestOneAccountCannotReadAnothersStatus is why the account is part of
// every key. One journal serves every connection, and without the
// account an upload named rows.csv would answer for somebody else's.
func TestOneAccountCannotReadAnothersStatus(t *testing.T) {
	j, sub := NewUploadJournal(), &recordingSubmitter{}

	alice := newSession(t, testAccount, j, sub)
	if err := writeCSV(t, alice, blastDir+"/submit/run.csv", sampleCSV); err != nil {
		t.Fatalf("upload: %v", err)
	}

	bob := newSession(t, "bob", j, sub)
	if got := listNames(t, bob, blastDir+"/status"); len(got) != 0 {
		t.Errorf("bob sees %v in status/", got)
	}
	if st, ok := bob.statusFor("blast", "run.csv"); ok {
		t.Errorf("bob read alice's status entry: %+v", st)
	}
	if _, err := bob.Fileread(&sftp.Request{
		Method: "Get", Filepath: blastDir + "/status/run.csv",
	}); err == nil {
		t.Error("bob fetched alice's status file")
	}
	// Alice still has hers.
	if _, ok := alice.statusFor("blast", "run.csv"); !ok {
		t.Error("alice lost her own status entry")
	}
}

// TestStatusExpires keeps the journal from being a leak: it is memory
// shared by every connection for the life of the server.
func TestStatusExpires(t *testing.T) {
	j := NewUploadJournal()
	j.put(testAccount, "blast", "old.csv", uploadStatus{State: stateSubmitted, At: time.Now()})

	rewind(j, DefaultUploadRetention+time.Minute)
	if st, ok := j.get(testAccount, "blast", "old.csv"); ok {
		t.Errorf("an entry past the retention is still readable: %+v", st)
	}
	if got := j.names(testAccount, "blast"); len(got) != 0 {
		t.Errorf("names = %v, want none", got)
	}
	// And the next write sweeps it out of the map rather than leaving it
	// to be filtered forever.
	j.put(testAccount, "blast", "new.csv", uploadStatus{State: stateSubmitted})
	j.mu.Lock()
	n := len(j.status)
	j.mu.Unlock()
	if n != 1 {
		t.Errorf("the journal holds %d entries, want 1", n)
	}
}

// TestTheBoundIsPerAccount: a bound meant to cap memory must not let one
// busy client evict everybody else's answers, which would be one account
// hiding another's refusals.
func TestTheBoundIsPerAccount(t *testing.T) {
	j := NewUploadJournal()
	j.perAccount = 2

	j.put("bob", "blast", "bob.csv", uploadStatus{State: stateSubmitted})
	for i, name := range []string{"first.csv", "second.csv", "third.csv"} {
		j.put(testAccount, "blast", name, uploadStatus{
			State: stateSubmitted,
			// Distinct, increasing, so "oldest" is unambiguous.
			At: time.Now().Add(time.Duration(i) * time.Second),
		})
	}

	got := j.names(testAccount, "blast")
	sort.Strings(got)
	want := []string{"second.csv", "third.csv"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("alice's entries = %v, want %v", got, want)
	}
	if got := j.names("bob", "blast"); len(got) != 1 {
		t.Errorf("bob's entries = %v, want his one to survive", got)
	}
}

// TestAPrivateJournalIsStillTheDefault: a library caller serving one
// session passes no journal, and must not get a nil map.
func TestAPrivateJournalIsStillTheDefault(t *testing.T) {
	f := newSession(t, testAccount, nil, &recordingSubmitter{})
	if f.journal == nil {
		t.Fatal("New left the journal nil, so any upload would panic")
	}
	if err := writeCSV(t, f, blastDir+"/submit/run.csv", sampleCSV); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, ok := f.statusFor("blast", "run.csv"); !ok {
		t.Error("a private journal did not record the upload")
	}
}
