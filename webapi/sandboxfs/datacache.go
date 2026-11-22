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
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dataCache holds sandbox file bytes on disk.
//
// Nothing's correctness depends on it. Every read has a miss path --
// streaming the sandbox again -- and the miss path is the ordinary one,
// which is what lets everything here be careless in the ways that
// matter:
//
//   - The evictor deletes by least-recent use, whenever it likes, with
//     no knowledge of readers and nothing to coordinate against.
//   - Eviction is unlink, so a reader that already has the file open
//     finishes from its own descriptor and the blocks go when the last
//     one closes. The kernel does that refcounting; there is none here.
//   - A reader is safe the moment its open succeeds, and a failed open
//     is a plain ENOENT, which is a miss. That is the entire race.
//   - `rm -rf` on the directory while the server runs is a supported
//     operation, not a race.
//
// Bytes are counted so a budget can be enforced, but the count is a
// target: an unlinked file a reader still holds occupies blocks that
// appear in no directory and are subtracted here as soon as the name
// goes. So df and du will disagree with this and with each other, by a
// bounded amount, and that is expected rather than a leak.
type dataCache struct {
	dir    string
	budget int64
	logf   func(string, ...any)

	// swept closes when the background delete of a previous run's
	// directory has finished. Only tests wait for it.
	swept chan struct{}

	mu    sync.Mutex
	index map[entryKey]*list.Element
	lru   *list.List // front = most recently used; values are *dataEntry
	bytes int64
}

type entryKey struct {
	job  Job
	name string
}

type dataEntry struct {
	key  entryKey
	size int64
}

// jobKeyFields is a compile-time reminder that an entryKey carries the
// account: dropping it would make the cache cross-account.
var _ = Job{}.Account

// stagingDir holds partial writes. A leading dot keeps it out of the
// job-directory scan, and keeping it out of the job directories entirely
// means a staged name can never collide with a sandbox entry -- a
// sandbox may legitimately contain a file called ".tmp-whatever".
const stagingDir = ".staging"

// newDataCache prepares dir and discards whatever was in it.
//
// Discarded rather than reconciled against an index, because rebuilding
// the index would cost a stat of every file to learn sizes that are
// already in each job's layout, to save re-fetching bytes that are
// cheap to re-fetch. Nothing here is worth carrying across a restart.
//
// A budget of zero or less disables the cache: Stage never accepts and
// Open never hits, so every read streams.
func newDataCache(dir string, budget int64, logf func(string, ...any)) (*dataCache, error) {
	c := &dataCache{
		dir:    dir,
		budget: budget,
		logf:   logf,
		index:  map[entryKey]*list.Element{},
		lru:    list.New(),
	}

	// Cleared whatever the budget is, and BEFORE the disabled-cache
	// return: an operator who fills the cache and then turns it off would
	// otherwise orphan the whole directory, with nothing left running
	// that would ever reclaim it.
	if err := c.reset(); err != nil {
		if budget <= 0 {
			// Nothing is about to be served from it, so this is a
			// failure to reclaim space, not a reason to refuse to start.
			if logf != nil {
				logf("sandboxfs: could not clear the disabled cache directory %q: %v", dir, err)
			}
			return c, nil
		}
		return nil, err
	}
	if budget <= 0 {
		return c, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, stagingDir), 0o700); err != nil {
		return nil, fmt.Errorf("sandboxfs: preparing the cache directory: %w", err)
	}
	return c, nil
}

// asideSuffix marks a directory renamed out of the way and awaiting
// deletion.
const asideSuffix = ".discarding-"

// reset clears whatever a previous run left.
//
// Nothing here survives a restart by design: rebuilding the index would
// mean stat-ing every file to learn sizes each job's layout already has,
// in order to save re-fetching bytes that are cheap to re-fetch. A crash
// can leave only a committed file or a staging file, and a rename is
// atomic, so there is never a half-published entry to recognise.
//
// The directory is renamed aside and deleted behind the server rather
// than removed in place, because removing a cache of millions of files
// takes minutes and nothing can be served during them. The rename is
// what makes the directory gone as far as serving is concerned; the
// delete is bookkeeping.
func (c *dataCache) reset() error {
	if c.dir == "" {
		return nil
	}

	aside := c.dir + asideSuffix + strconv.FormatInt(time.Now().UnixNano(), 36)
	switch err := os.Rename(c.dir, aside); {
	case err == nil, errors.Is(err, fs.ErrNotExist):
		// Renamed, or there was nothing there.
	default:
		// Rename fails when the directory is a mount point, which is a
		// perfectly ordinary way to deploy a cache. Fall back to the
		// slow path rather than refusing to run there.
		if c.logf != nil {
			c.logf("sandboxfs: could not rename %q aside (%v); clearing it in place", c.dir, err)
		}
		if err := os.RemoveAll(c.dir); err != nil {
			return fmt.Errorf("sandboxfs: clearing the cache directory: %w", err)
		}
	}

	c.swept = make(chan struct{})
	go func() {
		defer close(c.swept)
		c.sweepAside()
	}()
	return nil
}

// sweepAside deletes every directory an earlier reset renamed out of the
// way, including ones a crash during a previous sweep left behind.
func (c *dataCache) sweepAside() {
	parent, base := filepath.Dir(c.dir), filepath.Base(c.dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && c.logf != nil {
			c.logf("sandboxfs: looking for discarded cache directories in %q: %v", parent, err)
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), base+asideSuffix) {
			continue
		}
		path := filepath.Join(parent, e.Name())
		if err := os.RemoveAll(path); err != nil && c.logf != nil {
			c.logf("sandboxfs: discarding %q: %v", path, err)
		}
	}
}

// waitSwept blocks until the background delete has finished. For tests;
// nothing in the serving path waits for it.
func (c *dataCache) waitSwept() {
	if c.swept != nil {
		<-c.swept
	}
}

// enabled reports whether anything is cached at all.
func (c *dataCache) enabled() bool { return c != nil && c.budget > 0 }

// pathFor is the on-disk name for an entry.
//
// The sandbox tree is mirrored rather than hashed so an operator can see
// what the cache is holding; for a budgeted resource, a directory of
// opaque names is a worse answer than a slightly longer path. The result
// is verified to be under the job's directory even though cleanEntryName
// has already refused anything with a ".." in it: this one builds a real
// filesystem path from a name a remote sent, and the check is a string
// compare.
func (c *dataCache) pathFor(key entryKey) (string, error) {
	// Checked on the NAME before joining, not only on the result.
	// filepath.Join cleans as it goes, so joining an absolute name
	// quietly relativises it and the joined path is then perfectly
	// inside the job directory -- a repair, where the whole point here
	// is to refuse. The prefix check below stays as the backstop for
	// anything this misses.
	if key.name == "" || strings.HasPrefix(key.name, "/") || filepath.IsAbs(key.name) {
		return "", fmt.Errorf("sandboxfs: refusing cache path for %q", key.name)
	}
	for _, part := range strings.Split(filepath.ToSlash(key.name), "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("sandboxfs: refusing cache path for %q", key.name)
		}
	}

	jobDir, err := c.jobDir(key.job)
	if err != nil {
		return "", err
	}
	full := filepath.Join(jobDir, filepath.FromSlash(key.name))
	if !strings.HasPrefix(full, jobDir+string(filepath.Separator)) {
		return "", fmt.Errorf("sandboxfs: refusing cache path for %q", key.name)
	}
	return full, nil
}

// Open returns an open file for a cached entry.
//
// Once this returns a file, eviction cannot take it away: the name may
// go but the descriptor keeps the blocks. A miss is reported as ok=false
// rather than an error, because a miss is the ordinary case.
func (c *dataCache) Open(key entryKey) (*os.File, bool) {
	if !c.enabled() {
		return nil, false
	}
	c.mu.Lock()
	el, known := c.index[key]
	if known {
		c.lru.MoveToFront(el)
	}
	path, err := c.pathFor(key)
	c.mu.Unlock()
	if !known || err != nil {
		return nil, false
	}

	// Opened outside the lock: this is the one slow call here, and a
	// file the evictor removed between the lookup and now simply misses.
	f, err := os.Open(path) //nolint:gosec // path is built from a refused-unless-clean entry name
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && c.logf != nil {
			c.logf("sandboxfs: opening cached %s/%s: %v", key.job, key.name, err)
		}
		return nil, false
	}
	return f, true
}

// staged is a cache entry being written. Commit publishes it; Close
// without Commit discards it.
//
// The rename IS the completeness marker, so there is no flag to keep and
// no partial file to recognise later: a visible name is a whole file by
// construction. A writer that dies leaves only a staging file, which the
// next start removes with the rest of the directory.
type staged struct {
	c    *dataCache
	key  entryKey
	size int64
	f    *os.File
	n    int64
	done bool
}

// Stage opens a staging file for an entry of the given size.
//
// ok is false when the entry will not be cached and the caller should
// simply stream it: the cache is off, or the file alone is over the
// budget. A file bigger than the whole cache is never cached, because
// admitting it would evict everything else to hold one thing -- the
// opposite of what the budget is for. (A layout over its own budget IS
// kept, because there is no streaming fallback for a layout.)
func (c *dataCache) Stage(key entryKey, size int64) (s *staged, ok bool, err error) {
	if !c.enabled() || size > c.budget {
		return nil, false, nil
	}
	path, err := c.pathFor(key)
	if err != nil {
		return nil, false, err
	}
	// Both directories are made here, not only at startup, because the
	// documented answer to a full cache directory is `rm -rf` on it
	// while the server runs -- which takes the staging directory too.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("sandboxfs: preparing a cache entry: %w", err)
	}
	staging := filepath.Join(c.dir, stagingDir)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, false, fmt.Errorf("sandboxfs: preparing the staging directory: %w", err)
	}
	f, err := os.CreateTemp(staging, "e-")
	if err != nil {
		return nil, false, fmt.Errorf("sandboxfs: staging a cache entry: %w", err)
	}
	return &staged{c: c, key: key, size: size, f: f}, true, nil
}

func (s *staged) Write(p []byte) (int, error) {
	n, err := s.f.Write(p)
	s.n += int64(n)
	return n, err
}

// Commit publishes the staged file under its real name and makes room
// for it.
//
// A short write is refused rather than published: the caller declared a
// size, and a visible name has to mean a whole file or every later
// reader of it is reading a truncation nobody will report.
func (s *staged) Commit() error {
	if s.done {
		return errors.New("sandboxfs: cache entry already finished")
	}
	s.done = true
	if s.n != s.size {
		_ = s.abort()
		return fmt.Errorf("sandboxfs: cache entry %s/%s got %d bytes, declared %d",
			s.key.job, s.key.name, s.n, s.size)
	}
	if err := s.f.Close(); err != nil {
		_ = os.Remove(s.f.Name())
		return fmt.Errorf("sandboxfs: finishing a cache entry: %w", err)
	}
	path, err := s.c.pathFor(s.key)
	if err != nil {
		_ = os.Remove(s.f.Name())
		return err
	}
	if err := os.Rename(s.f.Name(), path); err != nil {
		_ = os.Remove(s.f.Name())
		return fmt.Errorf("sandboxfs: publishing a cache entry: %w", err)
	}
	s.c.admit(s.key, s.size)
	return nil
}

// Close discards a staged file that was not committed. Calling it after
// Commit is a no-op, so `defer Close()` beside a Commit is safe.
func (s *staged) Close() error {
	if s.done {
		return nil
	}
	s.done = true
	return s.abort()
}

func (s *staged) abort() error {
	err := s.f.Close()
	if rmErr := os.Remove(s.f.Name()); err == nil && !errors.Is(rmErr, fs.ErrNotExist) {
		err = rmErr
	}
	return err
}

// admit records a published entry and evicts until the budget is met.
func (c *dataCache) admit(key entryKey, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
	c.index[key] = c.lru.PushFront(&dataEntry{key: key, size: size})
	c.bytes += size

	// Stops when there are no candidates left rather than when the
	// budget is met: with readers holding unlinked files the real usage
	// can stay above it, and unlinking everything would not change that.
	for c.bytes > c.budget && c.lru.Len() > 0 {
		back := c.lru.Back()
		if back == nil {
			break
		}
		c.evictLocked(back.Value.(*dataEntry).key)
	}
}

// Evict removes one entry by name. A reader holding it open keeps it.
func (c *dataCache) Evict(key entryKey) {
	if !c.enabled() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(key)
}

// jobDir is where one account's copy of one job lives.
//
// The account is a directory level of its own, so an operator can see
// whose bytes are held -- and so two accounts reading the same job keep
// separate files, which is what makes the cache safe to serve without
// an authorisation check.
//
// An account name is used verbatim when it is a plain token and replaced
// by a digest otherwise. The mapping has to be INJECTIVE: folding odd
// characters to dashes would be the usual answer and would let two
// distinct accounts land in one directory, which is a cross-account leak
// rather than a cosmetic problem. "=" is excluded from the safe set, so
// the two branches cannot collide either.
func (c *dataCache) jobDir(job Job) (string, error) {
	if job.Account == "" {
		return "", fmt.Errorf("sandboxfs: refusing a cache path with no account")
	}
	return filepath.Join(c.dir, accountDir(job.Account), job.String()), nil
}

func accountDir(account string) string {
	safe := len(account) <= 64
	for _, r := range account {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '@':
		default:
			safe = false
		}
	}
	if safe {
		return account
	}
	sum := sha256.Sum256([]byte(account))
	return "=" + hex.EncodeToString(sum[:16])
}

// EvictJob removes every cached entry of one job.
func (c *dataCache) EvictJob(job Job) {
	if !c.enabled() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.index {
		if key.job == job {
			c.evictLocked(key)
		}
	}
	// The job's now-empty directories are tidied best-effort; a failure
	// leaves an empty directory, which costs nothing and is removed with
	// the rest at the next start.
	if dir, err := c.jobDir(job); err == nil {
		_ = os.RemoveAll(dir)
	}
}

func (c *dataCache) evictLocked(key entryKey) {
	el, ok := c.index[key]
	if !ok {
		return
	}
	if path, err := c.pathFor(key); err == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) && c.logf != nil {
			c.logf("sandboxfs: evicting %s/%s: %v", key.job, key.name, err)
		}
	}
	c.bytes -= el.Value.(*dataEntry).size
	c.lru.Remove(el)
	delete(c.index, key)
}

// removeLocked forgets an entry without touching the filesystem, for
// replacing one whose file is about to be overwritten by a rename.
func (c *dataCache) removeLocked(key entryKey) {
	el, ok := c.index[key]
	if !ok {
		return
	}
	c.bytes -= el.Value.(*dataEntry).size
	c.lru.Remove(el)
	delete(c.index, key)
}

// Bytes is what the cache believes it is holding. See the type comment:
// a target, and an undercount while readers hold unlinked files.
func (c *dataCache) Bytes() int64 {
	if !c.enabled() {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// Len is the number of cached entries.
func (c *dataCache) Len() int {
	if !c.enabled() {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.index)
}
