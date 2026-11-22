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
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func testCache(t *testing.T, budget int64) *dataCache {
	t.Helper()
	c, err := newDataCache(t.TempDir(), budget, t.Logf)
	if err != nil {
		t.Fatalf("newDataCache: %v", err)
	}
	return c
}

func key(cluster, proc int, name string) entryKey {
	return entryKey{job: tjob(cluster, proc), name: name}
}

// put stages and commits an entry, failing the test on any error.
func put(t *testing.T, c *dataCache, k entryKey, body []byte) {
	t.Helper()
	s, ok, err := c.Stage(k, int64(len(body)))
	if err != nil {
		t.Fatalf("Stage %s: %v", k.name, err)
	}
	if !ok {
		t.Fatalf("Stage %s refused", k.name)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Write(body); err != nil {
		t.Fatalf("Write %s: %v", k.name, err)
	}
	if err := s.Commit(); err != nil {
		t.Fatalf("Commit %s: %v", k.name, err)
	}
}

func readAll(t *testing.T, f *os.File) []byte {
	t.Helper()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return b
}

func TestDataCacheRoundTrip(t *testing.T) {
	c := testCache(t, 1<<20)
	body := []byte("the payload")
	k := key(12, 0, "sub/out.txt")
	put(t, c, k, body)

	f, ok := c.Open(k)
	if !ok {
		t.Fatal("Open missed a committed entry")
	}
	defer func() { _ = f.Close() }()
	if got := readAll(t, f); !bytes.Equal(got, body) {
		t.Errorf("read %q, want %q", got, body)
	}
	if c.Bytes() != int64(len(body)) || c.Len() != 1 {
		t.Errorf("cache holds %d bytes in %d entries, want %d in 1", c.Bytes(), c.Len(), len(body))
	}
}

func TestDataCacheMissIsNotAnError(t *testing.T) {
	c := testCache(t, 1<<20)
	if f, ok := c.Open(key(1, 0, "nope")); ok {
		_ = f.Close()
		t.Fatal("Open hit an entry that was never cached")
	}
}

// TestDataCacheShortWriteIsNotPublished: a visible name has to mean a
// whole file, or every later reader gets a truncation nobody reports.
func TestDataCacheShortWriteIsNotPublished(t *testing.T) {
	c := testCache(t, 1<<20)
	k := key(5, 0, "cut.dat")

	s, ok, err := c.Stage(k, 100)
	if err != nil || !ok {
		t.Fatalf("Stage: %v (ok=%v)", err, ok)
	}
	if _, err := s.Write([]byte("only a few")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Commit(); err == nil {
		t.Fatal("Commit published a short write")
	}
	if f, ok := c.Open(k); ok {
		_ = f.Close()
		t.Error("a short write became visible anyway")
	}
	if c.Bytes() != 0 || c.Len() != 0 {
		t.Errorf("cache accounts %d bytes in %d entries after a refused commit", c.Bytes(), c.Len())
	}
	// And nothing is left behind in staging.
	staged, err := os.ReadDir(filepath.Join(c.dir, stagingDir))
	if err != nil {
		t.Fatalf("ReadDir staging: %v", err)
	}
	if len(staged) != 0 {
		t.Errorf("staging holds %d files after a refused commit", len(staged))
	}
}

func TestDataCacheAbandonedStageLeavesNothing(t *testing.T) {
	c := testCache(t, 1<<20)
	s, ok, err := c.Stage(key(6, 0, "gone.dat"), 4)
	if err != nil || !ok {
		t.Fatalf("Stage: %v (ok=%v)", err, ok)
	}
	if _, err := s.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if f, ok := c.Open(key(6, 0, "gone.dat")); ok {
		_ = f.Close()
		t.Error("an abandoned stage became visible")
	}
	// Close after Close is a no-op, so `defer Close()` beside a Commit
	// is safe.
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestDataCacheEvictionKeepsOpenReaders is the property the whole
// eviction policy rests on: because an evicted name is unlinked and an
// open descriptor keeps the blocks, the evictor can delete whatever it
// likes whenever it likes without coordinating with anybody. If this
// failed, every reader would need a refcount or a lock.
func TestDataCacheEvictionKeepsOpenReaders(t *testing.T) {
	c := testCache(t, 1<<20)
	body := bytes.Repeat([]byte("payload-"), 4096)
	k := key(8, 1, "big.dat")
	put(t, c, k, body)

	f, ok := c.Open(k)
	if !ok {
		t.Fatal("Open missed a committed entry")
	}
	defer func() { _ = f.Close() }()

	// Read a little, then evict out from under the reader.
	head := make([]byte, 16)
	if _, err := io.ReadFull(f, head); err != nil {
		t.Fatalf("first read: %v", err)
	}
	c.Evict(k)

	// The name is gone...
	if f2, ok := c.Open(k); ok {
		_ = f2.Close()
		t.Error("Open hit an evicted entry")
	}
	// ...and the open reader still finishes the whole file.
	rest := readAll(t, f)
	got := make([]byte, 0, len(head)+len(rest))
	got = append(got, head...)
	got = append(got, rest...)
	if !bytes.Equal(got, body) {
		t.Errorf("reader got %d bytes after eviction, want %d", len(got), len(body))
	}
	if c.Bytes() != 0 {
		t.Errorf("cache accounts %d bytes after evicting its only entry", c.Bytes())
	}
}

// TestDataCacheEvictsLeastRecentlyUsed also asserts Open counts as use:
// a cache that only aged on write would evict exactly the file being
// read.
func TestDataCacheEvictsLeastRecentlyUsed(t *testing.T) {
	const each = 100
	c := testCache(t, 3*each)
	body := bytes.Repeat([]byte("x"), each)

	a, b, d := key(1, 0, "a"), key(1, 0, "b"), key(1, 0, "d")
	put(t, c, a, body)
	put(t, c, b, body)

	// Touch a, so b is now the least recently used.
	f, ok := c.Open(a)
	if !ok {
		t.Fatal("Open missed a")
	}
	_ = f.Close()

	put(t, c, d, body)
	put(t, c, key(1, 0, "e"), body) // one over the budget

	if _, ok := c.Open(b); ok {
		t.Error("the least recently used entry survived")
	}
	if _, ok := c.Open(a); !ok {
		t.Error("a recently read entry was evicted")
	}
	if c.Bytes() > 3*each {
		t.Errorf("cache holds %d bytes, over the %d budget", c.Bytes(), 3*each)
	}
}

// TestDataCacheRefusesWhatCannotFit: a file bigger than the whole cache
// is never cached, because admitting it would evict everything to hold
// one thing. The caller streams it instead, which is why this is ok=false
// and not an error.
func TestDataCacheRefusesWhatCannotFit(t *testing.T) {
	c := testCache(t, 1024)
	s, ok, err := c.Stage(key(1, 0, "huge.dat"), 4096)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if ok {
		_ = s.Close()
		t.Fatal("Stage accepted a file larger than the budget")
	}
	if s != nil {
		t.Error("Stage returned a staged file along with ok=false")
	}
}

func TestDataCacheDisabled(t *testing.T) {
	for _, budget := range []int64{0, -1} {
		c, err := newDataCache(filepath.Join(t.TempDir(), "unused"), budget, t.Logf)
		if err != nil {
			t.Fatalf("newDataCache(%d): %v", budget, err)
		}
		if _, ok, err := c.Stage(key(1, 0, "x"), 1); ok || err != nil {
			t.Errorf("budget %d: Stage ok=%v err=%v, want refused", budget, ok, err)
		}
		if _, ok := c.Open(key(1, 0, "x")); ok {
			t.Errorf("budget %d: Open hit", budget)
		}
		if c.Bytes() != 0 || c.Len() != 0 {
			t.Errorf("budget %d: cache accounts %d bytes in %d entries", budget, c.Bytes(), c.Len())
		}
	}
}

// TestDataCacheStartsEmpty: whatever a previous run left is discarded
// rather than reconciled.
func TestDataCacheStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "9.0", "left-over.dat")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c, err := newDataCache(dir, 1<<20, t.Logf)
	if err != nil {
		t.Fatalf("newDataCache: %v", err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("a file from a previous run survived")
	}
	if c.Len() != 0 {
		t.Errorf("cache believes it holds %d entries at startup", c.Len())
	}
}

// TestDataCacheSurvivesDirectoryRemoval: `rm -rf` on the cache directory
// while the server runs is documented as supported, so it has to be. A
// read then misses and a later write recreates what it needs.
func TestDataCacheSurvivesDirectoryRemoval(t *testing.T) {
	c := testCache(t, 1<<20)
	k := key(4, 0, "a/b/c.dat")
	put(t, c, k, []byte("before"))

	if err := os.RemoveAll(c.dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if f, ok := c.Open(k); ok {
		_ = f.Close()
		t.Error("Open hit an entry whose file an operator deleted")
	}

	// And the cache keeps working.
	k2 := key(4, 0, "after.dat")
	put(t, c, k2, []byte("after"))
	f, ok := c.Open(k2)
	if !ok {
		t.Fatal("the cache did not recover from having its directory removed")
	}
	defer func() { _ = f.Close() }()
	if got := readAll(t, f); string(got) != "after" {
		t.Errorf("read %q, want \"after\"", got)
	}
}

func TestDataCacheEvictJob(t *testing.T) {
	c := testCache(t, 1<<20)
	body := []byte("x")
	put(t, c, key(1, 0, "a"), body)
	put(t, c, key(1, 0, "sub/b"), body)
	put(t, c, key(2, 0, "a"), body)

	c.EvictJob(tjob(1, 0))

	for _, k := range []entryKey{key(1, 0, "a"), key(1, 0, "sub/b")} {
		if f, ok := c.Open(k); ok {
			_ = f.Close()
			t.Errorf("1.0/%s survived EvictJob", k.name)
		}
	}
	if _, ok := c.Open(key(2, 0, "a")); !ok {
		t.Error("EvictJob took another job's entry")
	}
	if c.Bytes() != 1 || c.Len() != 1 {
		t.Errorf("cache holds %d bytes in %d entries, want 1 in 1", c.Bytes(), c.Len())
	}
}

// TestDataCacheRefusesEscapingNames: the layout already refuses these,
// and this is the second place that would have to be wrong for a sandbox
// entry to name a file outside its own cache directory.
func TestDataCacheRefusesEscapingNames(t *testing.T) {
	c := testCache(t, 1<<20)
	for _, name := range []string{"../escape", "a/../../escape", "/abs"} {
		if _, err := c.pathFor(key(1, 0, name)); err == nil {
			t.Errorf("pathFor(%q) was accepted", name)
		}
	}
}

// TestDataCacheReplacesAnEntry asserts a re-commit does not double-count.
func TestDataCacheReplacesAnEntry(t *testing.T) {
	c := testCache(t, 1<<20)
	k := key(1, 0, "same.dat")
	put(t, c, k, []byte("first"))
	put(t, c, k, []byte("second"))

	if c.Len() != 1 {
		t.Errorf("cache holds %d entries after re-committing one name, want 1", c.Len())
	}
	if want := int64(len("second")); c.Bytes() != want {
		t.Errorf("cache accounts %d bytes, want %d", c.Bytes(), want)
	}
	f, ok := c.Open(k)
	if !ok {
		t.Fatal("Open missed the replaced entry")
	}
	defer func() { _ = f.Close() }()
	if got := readAll(t, f); string(got) != "second" {
		t.Errorf("read %q, want \"second\"", got)
	}
}

// staleTree writes a cache directory as a previous run would have left
// it, and returns one file's path.
func staleTree(t *testing.T, dir string) string {
	t.Helper()
	for _, rel := range []string{
		"alice/9.0/out.dat",
		"alice/9.0/sub/deep.dat",
		"bob/1.2/out.dat",
		stagingDir + "/e-halfwritten",
	} {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll %q: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatalf("WriteFile %q: %v", path, err)
		}
	}
	return filepath.Join(dir, "alice/9.0/out.dat")
}

// TestCacheClearedAfterACrash is the restart contract: a crash can leave
// a committed file or a staging file, and neither survives.
func TestCacheClearedAfterACrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sandbox")
	committed := staleTree(t, dir)

	c, err := newDataCache(dir, 1<<20, t.Logf)
	if err != nil {
		t.Fatalf("newDataCache: %v", err)
	}
	c.waitSwept()

	if _, err := os.Stat(committed); err == nil {
		t.Error("a committed file from the crashed run survived")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// Only the staging directory, freshly made.
	if len(entries) != 1 || entries[0].Name() != stagingDir {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("cache directory holds %v, want only %q", names, stagingDir)
	}
	if c.Len() != 0 || c.Bytes() != 0 {
		t.Errorf("cache believes it holds %d entries / %d bytes", c.Len(), c.Bytes())
	}
}

// TestDisabledCacheStillReclaims is the bug this was written for: the
// budget check used to sit above the cleanup, so turning the cache off
// orphaned everything a previous run had cached, with nothing left
// running that would ever reclaim it.
func TestDisabledCacheStillReclaims(t *testing.T) {
	for _, budget := range []int64{0, -1} {
		dir := filepath.Join(t.TempDir(), "sandbox")
		committed := staleTree(t, dir)

		c, err := newDataCache(dir, budget, t.Logf)
		if err != nil {
			t.Fatalf("newDataCache(%d): %v", budget, err)
		}
		c.waitSwept()

		if _, err := os.Stat(committed); err == nil {
			t.Errorf("budget %d: a previous run's cache survived being disabled", budget)
		}
		// And nothing is recreated, since nothing will be served.
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("budget %d: the cache directory was recreated although the cache is off", budget)
		}
	}
}

// TestCacheIsUsableBeforeTheDeleteFinishes: the directory is renamed
// aside so startup does not wait on deleting a cache of millions of
// files. Asserted by using the cache with the sweep still outstanding.
func TestCacheIsUsableBeforeTheDeleteFinishes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sandbox")
	staleTree(t, dir)

	c, err := newDataCache(dir, 1<<20, t.Logf)
	if err != nil {
		t.Fatalf("newDataCache: %v", err)
	}
	// Deliberately before waitSwept.
	k := key(1, 0, "fresh.dat")
	put(t, c, k, []byte("fresh"))
	f, ok := c.Open(k)
	if !ok {
		t.Fatal("the cache was not usable before the background delete finished")
	}
	_ = f.Close()

	c.waitSwept()
	if _, err := os.Stat(filepath.Join(dir, "alice")); err == nil {
		t.Error("a previous run's account directory survived the sweep")
	}
}

// TestSweepClearsDirectoriesLeftByAnEarlierCrash: a crash DURING the
// background delete leaves a renamed-aside directory, which no later run
// would otherwise touch.
func TestSweepClearsDirectoriesLeftByAnEarlierCrash(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "sandbox")

	orphan := dir + asideSuffix + "fromacrash"
	if err := os.MkdirAll(filepath.Join(orphan, "alice", "1.0"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "alice", "1.0", "x"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Something that merely looks similar must be left alone.
	unrelated := filepath.Join(parent, "sandbox-other")
	if err := os.MkdirAll(unrelated, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	c, err := newDataCache(dir, 1<<20, t.Logf)
	if err != nil {
		t.Fatalf("newDataCache: %v", err)
	}
	c.waitSwept()

	if _, err := os.Stat(orphan); err == nil {
		t.Error("a directory left by a crash during an earlier sweep survived")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("an unrelated sibling directory was removed: %v", err)
	}
}

// TestFirstRunNeedsNoDirectory: nothing to clear is not an error.
func TestFirstRunNeedsNoDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never", "existed")
	c, err := newDataCache(dir, 1<<20, t.Logf)
	if err != nil {
		t.Fatalf("newDataCache: %v", err)
	}
	c.waitSwept()
	put(t, c, key(1, 0, "a.dat"), []byte("a"))
	if _, ok := c.Open(key(1, 0, "a.dat")); !ok {
		t.Error("a cache created from nothing does not work")
	}
}
