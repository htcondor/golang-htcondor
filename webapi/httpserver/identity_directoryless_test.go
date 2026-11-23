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
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/htcondor/golang-htcondor/droppriv"
	"github.com/htcondor/golang-htcondor/idmap"
	"github.com/htcondor/golang-htcondor/logging"
)

// These cover the state that produced an outage with no symptom: a
// container whose SSSD never started, whose account enumeration therefore
// SUCCEEDS over the image's own /etc/passwd, and whose index then maps
// nobody while Degraded() is nil and the count is non-zero. See
// localIdentity.directorylessIndex.

// fakeAccounts is an enumerator that reports its own provenance, which is
// the one thing the real one cannot be made to fake off Linux.
type fakeAccounts struct {
	accounts      []idmap.Account
	fromDirectory bool
	err           error
}

func (f *fakeAccounts) Name() string { return "fake" }

func (f *fakeAccounts) Enumerate(ctx context.Context) ([]idmap.Account, error) {
	a, _, err := f.EnumerateWithProvenance(ctx)
	return a, err
}

func (f *fakeAccounts) EnumerateWithProvenance(_ context.Context) ([]idmap.Account, bool, error) {
	return f.accounts, f.fromDirectory, f.err
}

// fakeGecos verifies from the same list, so a hit is believed.
type fakeGecos struct{ accounts []idmap.Account }

func (f fakeGecos) GecosOf(_ context.Context, username string) (string, error) {
	for _, a := range f.accounts {
		if a.Username == username {
			return a.Gecos, nil
		}
	}
	return "", errors.New("no such account")
}

// writeNSSwitch points droppriv at an nsswitch.conf with the given passwd
// line for the duration of the test.
//
// The path is process-global and choosing it also resets the memoised
// group lookup, so it is put back afterwards. Leaving it pointing at a
// t.TempDir that cleanup has since removed would quietly change how
// every later test in this binary resolves groups.
func writeNSSwitch(t *testing.T, passwdLine string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nsswitch.conf")
	if err := os.WriteFile(path, []byte("# fixture\n"+passwdLine+"\ngroup: files\n"), 0o600); err != nil {
		t.Fatalf("writing nsswitch fixture: %v", err)
	}
	droppriv.SetNSSSwitchPath(path)
	// droppriv exports no getter, so this restores the package default
	// rather than whatever was there -- which is the same thing unless
	// another test starts overriding it too.
	t.Cleanup(func() { droppriv.SetNSSSwitchPath("/etc/nsswitch.conf") })
}

// localIdentityWith builds one around a fake enumerator, bypassing
// newLocalIdentity because that always reads a real account database.
func localIdentityWith(t *testing.T, enum *fakeAccounts) *localIdentity {
	t.Helper()
	l := &localIdentity{logger: testLogger(t), refreshEvery: time.Minute}
	l.resolver = idmap.New(enum, fakeGecos{accounts: enum.accounts},
		idmap.WithTTL(time.Minute),
		idmap.WithStrategies(idmap.StrategyGecos))
	if err := l.resolver.Refresh(context.Background()); err != nil {
		t.Fatalf("building the index: %v", err)
	}
	return l
}

var fixtureAccounts = []idmap.Account{
	{Username: "root", UID: 0, Gecos: "root"},
	{Username: "condor", UID: 64, Gecos: "condor"},
}

func TestDirectorylessIndexIsFlaggedWhenNSSwitchNamesSSS(t *testing.T) {
	writeNSSwitch(t, "passwd: files sss")
	l := localIdentityWith(t, &fakeAccounts{accounts: fixtureAccounts, fromDirectory: false})

	if accounts, _, _ := l.resolver.Stats(); accounts != len(fixtureAccounts) {
		t.Fatalf("index holds %d accounts, want %d", accounts, len(fixtureAccounts))
	}
	// The whole point: nothing else reports a problem.
	if err := l.resolver.Degraded(); err != nil {
		t.Fatalf("Degraded() = %v, want nil: this case must not look degraded", err)
	}
	if l.resolver.CoversDirectory() {
		t.Fatal("CoversDirectory() = true, want false for an enumeration with no directory")
	}
	if !l.directorylessIndex() {
		t.Fatal("directorylessIndex() = false; a local-only index with sss declared must be flagged")
	}
}

func TestDirectorylessIndexIsSilentWithNoDirectoryConfigured(t *testing.T) {
	writeNSSwitch(t, "passwd: files")
	l := localIdentityWith(t, &fakeAccounts{accounts: fixtureAccounts, fromDirectory: false})

	if droppriv.DirectoryDeclaredForAccounts() {
		t.Fatal("DirectoryDeclaredForAccounts() = true for 'passwd: files'")
	}
	if l.directorylessIndex() {
		t.Fatal("directorylessIndex() = true on a host with no directory; that host is healthy")
	}
}

func TestDirectorylessIndexIsSilentWhenTheDirectoryAnswered(t *testing.T) {
	writeNSSwitch(t, "passwd: files sss")
	l := localIdentityWith(t, &fakeAccounts{accounts: fixtureAccounts, fromDirectory: true})

	if !l.resolver.CoversDirectory() {
		t.Fatal("CoversDirectory() = false after an enumeration a directory contributed to")
	}
	if l.directorylessIndex() {
		t.Fatal("directorylessIndex() = true although a directory contributed")
	}
}

// An operator-named passwd file is the whole account database by design,
// so no directory is consulted and false provenance means nothing is
// wrong. Warning here would fire forever on a correct deployment.
func TestDirectorylessIndexIsSilentForAnExplicitPasswdFile(t *testing.T) {
	writeNSSwitch(t, "passwd: files sss")
	l := localIdentityWith(t, &fakeAccounts{accounts: fixtureAccounts, fromDirectory: false})
	l.explicitPasswdFile = true

	if l.directorylessIndex() {
		t.Fatal("directorylessIndex() = true with an operator-named passwd file")
	}
}

// A degraded build is already reported, and more precisely, by warmUp.
// Two warnings for one fault is how a log stops being read.
func TestDirectorylessIndexDefersToTheDegradedWarning(t *testing.T) {
	writeNSSwitch(t, "passwd: files sss")
	enum := &fakeAccounts{
		accounts:      fixtureAccounts,
		fromDirectory: false,
		err:           &droppriv.DirectoryError{Source: "sssd", Err: errors.New("socket is gone")},
	}
	l := localIdentityWith(t, enum)

	if l.resolver.Degraded() == nil {
		t.Fatal("Degraded() = nil, want the directory error; the fixture is wrong")
	}
	if l.directorylessIndex() {
		t.Fatal("directorylessIndex() = true on a degraded build, which warmUp already warns about")
	}
}

// An empty index has its own warning, which names the enumerate=true
// cause this one cannot.
func TestDirectorylessIndexDefersToTheEmptyIndexWarning(t *testing.T) {
	writeNSSwitch(t, "passwd: files sss")
	l := localIdentityWith(t, &fakeAccounts{accounts: nil, fromDirectory: false})

	if accounts, _, _ := l.resolver.Stats(); accounts != 0 {
		t.Fatalf("index holds %d accounts, want 0", accounts)
	}
	if l.directorylessIndex() {
		t.Fatal("directorylessIndex() = true on an empty index, which has its own warning")
	}
}

// Nothing is flagged where no subject mapping is configured: there is no
// index to be directoryless.
func TestDirectorylessIndexIsSilentWithoutAnIndex(t *testing.T) {
	writeNSSwitch(t, "passwd: files sss")
	var l *localIdentity
	if l.directorylessIndex() {
		t.Fatal("directorylessIndex() = true on a nil localIdentity")
	}
	l = &localIdentity{logger: testLogger(t)}
	if l.directorylessIndex() {
		t.Fatal("directorylessIndex() = true with no resolver")
	}
}

// warmUpLog runs warmUp against a buffered logger and returns what it
// logged. The warning is the whole deliverable here -- an operator
// reading the startup log is the only person who can act on this -- so it
// is asserted directly rather than through the predicate alone.
func warmUpLog(t *testing.T, enum *fakeAccounts) []logging.BufferEntry {
	t.Helper()
	buf := logging.NewBuffer(32, slog.LevelDebug)
	logger, err := logging.New(&logging.Config{
		OutputPath:   "stderr",
		DefaultLevel: logging.VerbosityInfo,
		DestinationLevels: map[logging.Destination]logging.Verbosity{
			logging.DestinationHTTP: logging.VerbosityInfo,
		},
	})
	if err != nil {
		t.Fatalf("logging.New: %v", err)
	}
	logging.AttachBuffer(logger, buf)

	l := &localIdentity{logger: logger}
	l.resolver = idmap.New(enum, fakeGecos{accounts: enum.accounts},
		idmap.WithTTL(time.Minute),
		idmap.WithStrategies(idmap.StrategyGecos))
	// refreshEvery stays zero so refreshLoop returns at once rather than
	// ticking through the rest of the test.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.warmUp(ctx)
	if !l.waitForRefreshStop(5 * time.Second) {
		t.Fatal("the refresh loop did not stop")
	}
	return buf.Entries(32)
}

func findEntry(entries []logging.BufferEntry, substr string) (logging.BufferEntry, bool) {
	for _, e := range entries {
		if strings.Contains(e.Message, substr) {
			return e, true
		}
	}
	return logging.BufferEntry{}, false
}

func TestWarmUpWarnsAboutADirectorylessIndex(t *testing.T) {
	writeNSSwitch(t, "passwd: files sss")
	entries := warmUpLog(t, &fakeAccounts{accounts: fixtureAccounts, fromDirectory: false})

	entry, ok := findEntry(entries, "covers no directory accounts")
	if !ok {
		t.Fatalf("warmUp logged nothing about a directoryless index; got %d entries", len(entries))
	}
	if entry.Level != "WARN" {
		t.Errorf("level = %q, want WARN: this refuses every directory login", entry.Level)
	}
	// The count is what tells an operator the index is the image's own
	// accounts rather than the directory's.
	if got, want := entry.Fields["indexed"], strconv.Itoa(len(fixtureAccounts)); got != want {
		t.Errorf("indexed = %q, want %q", got, want)
	}
	// Both halves of the fix have to be in the message, or the warning
	// sends the reader looking in the wrong place.
	for _, want := range []string{"nsswitch.conf", "enumerate = true"} {
		if !strings.Contains(entry.Message, want) {
			t.Errorf("the warning does not mention %q: %q", want, entry.Message)
		}
	}
	// It must not also claim a read failed, which is a different fault
	// with a different fix.
	if _, ok := findEntry(entries, "a directory could not be read"); ok {
		t.Error("warmUp also logged the degraded-read warning; nothing failed to read here")
	}
}

func TestWarmUpStaysQuietWhenTheDirectoryAnswered(t *testing.T) {
	writeNSSwitch(t, "passwd: files sss")
	entries := warmUpLog(t, &fakeAccounts{accounts: fixtureAccounts, fromDirectory: true})

	if entry, ok := findEntry(entries, "covers no directory accounts"); ok {
		t.Errorf("warmUp warned about a directory-backed index: %q", entry.Message)
	}
	if _, ok := findEntry(entries, "Indexed accounts by GECOS"); !ok {
		t.Error("warmUp did not log the index summary; the fixture is not exercising it")
	}
}
