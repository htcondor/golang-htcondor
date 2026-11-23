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
	"fmt"
	"sort"
	"sync"
	"time"
)

// UploadJournal remembers what became of each dropped file, for every
// account and every connection a server serves.
//
// An upload's outcome has to outlive the connection that produced it.
// This used to live on the FS, which is built per SFTP session, and two
// things followed that nobody would choose:
//
// A one-shot client never saw its own answer. `sftp -b` opens a channel,
// puts the file and closes, and the status entry explaining what
// happened went away with the channel. The next connection found an
// empty status/ directory and no way to learn why no jobs ran. For a
// refusal that is the only place the reason exists -- the close reply
// carries one string, and OpenSSH prints its own words instead.
//
// A retry double-submitted. Deduplication keyed on the content digest,
// so the same bytes dropped twice were one submission -- but only within
// one connection. A client that lost its connection mid-put and ran the
// command again was a new connection, which is precisely the case the
// rule exists for.
//
// So the journal is shared by every connection and keyed by account. It
// is still memory: a restart forgets, and the durable record of what was
// submitted is the cluster itself, which carries the template and the
// digest as job attributes.
type UploadJournal struct {
	mu        sync.Mutex
	status    map[journalKey]uploadStatus
	submitted map[journalKey]submittedUpload

	// retention is how long a status entry stays readable, dedup is how
	// long the same bytes count as a repeat, and perAccount bounds one
	// account's entries. Fields rather than constants so the tests can
	// drive the expiry without sleeping through it.
	retention  time.Duration
	dedup      time.Duration
	perAccount int
}

// journalKey identifies one entry: an account, a scope (a template id,
// or dropScope for a tarball) and a name, which is the dropped file's
// name for a status entry and the content digest for a submission.
//
// The account is part of the key because one journal serves every
// connection. Without it one account's upload named rows.csv would
// answer for another's, which is a disclosure, not a collision.
type journalKey struct{ account, scope, name string }

// submittedUpload is what deduplication remembers.
type submittedUpload struct {
	cluster int
	at      time.Time
}

// Defaults for a journal.
const (
	// DefaultUploadRetention is how long a status entry stays readable.
	// Long enough that somebody who dropped a file on Friday can still
	// find out on Monday why it was refused.
	DefaultUploadRetention = 7 * 24 * time.Hour

	// DefaultDedupWindow is how long the same bytes count as a repeat
	// rather than a new submission.
	//
	// Much shorter than the retention, and deliberately: the rule is
	// there to absorb a retry -- a reconnect, a client re-run after an
	// interrupted put -- which happens within minutes. Holding it for
	// the whole retention would instead refuse somebody deliberately
	// running the same batch again tomorrow, and tell them their own
	// file was a duplicate.
	DefaultDedupWindow = time.Hour

	// DefaultUploadEntries bounds one account's entries, so a client
	// dropping files in a loop cannot grow this without limit.
	DefaultUploadEntries = 256
)

// NewUploadJournal returns a journal with the default retention.
//
// One per server, not one per account: the account is part of every key.
func NewUploadJournal() *UploadJournal {
	return &UploadJournal{
		retention:  DefaultUploadRetention,
		dedup:      DefaultDedupWindow,
		perAccount: DefaultUploadEntries,
	}
}

// humanDuration prints a window the way a README should read, because
// the alternative is telling somebody their archive is a duplicate for
// "1h0m0s".
func humanDuration(d time.Duration) string {
	switch {
	case d == time.Hour:
		return "an hour"
	case d > time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d hours", d/time.Hour)
	case d == time.Minute:
		return "a minute"
	case d > time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", d/time.Minute)
	}
	return d.String()
}

// put records an outcome.
func (j *UploadJournal) put(account, scope, name string, st uploadStatus) {
	if st.At.IsZero() {
		// Stamped here rather than relying on the caller, because
		// everything below ages an entry by its At: an unstamped one
		// would read as older than the retention and be swept out
		// immediately, which looks exactly like the bug this whole type
		// exists to fix.
		st.At = time.Now()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status == nil {
		j.status = map[journalKey]uploadStatus{}
	}
	j.status[journalKey{account, scope, name}] = st
	j.expire(time.Now())
	j.bound(account)
}

// get reads one outcome back.
func (j *UploadJournal) get(account, scope, name string) (uploadStatus, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	st, ok := j.status[journalKey{account, scope, name}]
	if !ok || j.stale(st.At, j.retention, time.Now()) {
		return uploadStatus{}, false
	}
	return st, true
}

// names lists one scope's entries for one account.
func (j *UploadJournal) names(account, scope string) []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	out := make([]string, 0, len(j.status))
	for k, st := range j.status {
		if k.account == account && k.scope == scope && !j.stale(st.At, j.retention, now) {
			out = append(out, k.name)
		}
	}
	return out
}

// alreadySubmitted reports a digest submitted recently enough to count
// as a repeat.
func (j *UploadJournal) alreadySubmitted(account, scope, digest string) (int, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	s, ok := j.submitted[journalKey{account, scope, digest}]
	if !ok || j.stale(s.at, j.dedup, time.Now()) {
		return 0, false
	}
	return s.cluster, true
}

// rememberSubmitted records what a digest became.
func (j *UploadJournal) rememberSubmitted(account, scope, digest string, cluster int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.submitted == nil {
		j.submitted = map[journalKey]submittedUpload{}
	}
	j.submitted[journalKey{account, scope, digest}] = submittedUpload{
		cluster: cluster, at: time.Now(),
	}
	j.expire(time.Now())
}

// stale reports an entry past a window. A zero or non-positive window
// never expires, which is what the tests use to hold everything.
func (j *UploadJournal) stale(at time.Time, window time.Duration, now time.Time) bool {
	if window <= 0 {
		return false
	}
	return at.Before(now.Add(-window))
}

// expire drops what nobody can read any more. Caller holds the lock.
//
// Both maps, on every write: a submission record is kept for the dedup
// window but read through alreadySubmitted, so without this the map
// would hold every digest an account ever dropped, long past the point
// where any of them could still match.
func (j *UploadJournal) expire(now time.Time) {
	for k, st := range j.status {
		if j.stale(st.At, j.retention, now) {
			delete(j.status, k)
		}
	}
	for k, s := range j.submitted {
		if j.stale(s.at, j.dedup, now) {
			delete(j.submitted, k)
		}
	}
}

// bound keeps one account's status entries under the per-account limit,
// dropping its oldest first. Caller holds the lock.
//
// Per account rather than over the whole journal, so one busy client
// cannot evict everybody else's answers -- which would turn a bound
// meant to cap memory into one account hiding another's refusals.
func (j *UploadJournal) bound(account string) {
	if j.perAccount <= 0 {
		return
	}
	mine := make([]journalKey, 0, len(j.status))
	for k := range j.status {
		if k.account == account {
			mine = append(mine, k)
		}
	}
	if len(mine) <= j.perAccount {
		return
	}
	sort.Slice(mine, func(a, b int) bool {
		ta, tb := j.status[mine[a]].At, j.status[mine[b]].At
		if ta.Equal(tb) {
			// A stable tiebreak, so a test dropping entries that share a
			// timestamp sees a deterministic survivor.
			return mine[a].name < mine[b].name
		}
		return ta.Before(tb)
	})
	for _, k := range mine[:len(mine)-j.perAccount] {
		delete(j.status, k)
	}
}
