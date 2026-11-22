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
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// awkward is a sandbox shaped to catch tar-sizing mistakes: an empty
// file, one exactly a block long, one a byte over, and a name far too
// long for a plain ustar header.
func awkward() map[string][]byte {
	return map[string][]byte{
		"empty":          {},
		"exact":          bytes.Repeat([]byte("x"), tarBlock),
		"over":           bytes.Repeat([]byte("y"), tarBlock+1),
		"short":          []byte("hi"),
		"sub/nested.dat": bytes.Repeat([]byte("z"), 3000),
		strings.Repeat("long-name-", 15) + ".dat": []byte("pax"),
	}
}

// TestTarSizeIsExact is the oracle for the sizing approach: measure each
// header through a real tar.Writer, then account the body's padding.
// A client is told this number before it reads, and both a number too
// small and one too large break real clients, so "close" is no use.
func TestTarSizeIsExact(t *testing.T) {
	files := awkward()
	s := newStore(t, newFakeFetcher(files, "sub"), Config{})
	ctx := context.Background()

	want, err := s.TarSize(ctx, tjob(1, 0))
	if err != nil {
		t.Fatalf("TarSize: %v", err)
	}
	r, size, err := s.OpenTar(ctx, tjob(1, 0))
	if err != nil {
		t.Fatalf("OpenTar: %v", err)
	}
	defer func() { _ = r.Close() }()
	if size != want {
		t.Errorf("OpenTar reported %d, TarSize said %d", size, want)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading the tar: %v", err)
	}
	if int64(len(body)) != want {
		t.Fatalf("streamed %d bytes, declared %d", len(body), want)
	}

	// And it is a real tar carrying the real sandbox.
	got := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(body))
	dirs := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("reading back the tar: %v", err)
		}
		if h.Typeflag == tar.TypeDir {
			dirs++
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %q: %v", h.Name, err)
		}
		got[h.Name] = b
	}
	if dirs != 1 {
		t.Errorf("tar carried %d directories, want 1", dirs)
	}
	if len(got) != len(files) {
		t.Errorf("tar carried %d files, want %d", len(got), len(files))
	}
	for name, want := range files {
		if !bytes.Equal(got[name], want) {
			t.Errorf("%q: got %d bytes, want %d", name, len(got[name]), len(want))
		}
	}
}

// TestTarSizeIsStableAcrossCalls: the size is computed from the cached
// layout, so asking twice must not produce two answers.
func TestTarSizeIsStableAcrossCalls(t *testing.T) {
	s := newStore(t, newFakeFetcher(awkward(), "sub"), Config{})
	ctx := context.Background()
	first, err := s.TarSize(ctx, tjob(1, 0))
	if err != nil {
		t.Fatalf("TarSize: %v", err)
	}
	second, err := s.TarSize(ctx, tjob(1, 0))
	if err != nil {
		t.Fatalf("TarSize again: %v", err)
	}
	if first != second {
		t.Errorf("TarSize = %d then %d", first, second)
	}
}

// TestOpenCachedFileSupportsRandomAccess: a cache hit has to seek, or
// every client that reads a file out of order is back to streaming it.
func TestOpenCachedFileSupportsRandomAccess(t *testing.T) {
	body := []byte("0123456789abcdef")
	s := newStore(t, newFakeFetcher(map[string][]byte{"f.dat": body}), Config{
		CacheDir:   t.TempDir(),
		CacheBytes: 1 << 20,
	})

	r, err := s.Open(context.Background(), tjob(1, 0), "f.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()

	ra, ok := r.(io.ReaderAt)
	if !ok {
		t.Fatalf("a cached file came back as %T, which is not an io.ReaderAt", r)
	}
	buf := make([]byte, 4)
	if _, err := ra.ReadAt(buf, 10); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "abcd" {
		t.Errorf("ReadAt(10) = %q, want \"abcd\"", buf)
	}
}

// TestOpenFillsTheCacheOnce is the reason a miss spills the whole job: a
// spooled sandbox has no ranged read, so a client walking it file by
// file would otherwise pay a full pass per file.
func TestOpenFillsTheCacheOnce(t *testing.T) {
	files := map[string][]byte{
		"a.dat": []byte("aaa"),
		"b.dat": []byte("bbbb"),
		"c.dat": []byte("ccccc"),
	}
	f := newFakeFetcher(files)
	s := newStore(t, f, Config{CacheDir: t.TempDir(), CacheBytes: 1 << 20})
	ctx := context.Background()

	// One pass builds the layout; one more fills the cache.
	r, err := s.Open(ctx, tjob(1, 0), "a.dat")
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	mustRead(t, r, files["a.dat"])
	after := f.calls.Load()
	if after != 2 {
		t.Fatalf("first Open cost %d passes, want 2 (layout, then fill)", after)
	}

	for _, name := range []string{"a.dat", "b.dat", "c.dat"} {
		r, err := s.Open(ctx, tjob(1, 0), name)
		if err != nil {
			t.Fatalf("Open %s: %v", name, err)
		}
		mustRead(t, r, files[name])
	}
	if got := f.calls.Load(); got != after {
		t.Errorf("reading three cached files cost %d more passes, want 0", got-after)
	}
}

// TestOpenStreamsWithoutACache asserts the miss path alone is correct,
// which is what everything else is allowed to rely on.
func TestOpenStreamsWithoutACache(t *testing.T) {
	files := map[string][]byte{"a.dat": bytes.Repeat([]byte("a"), 5000), "b.dat": []byte("b")}
	f := newFakeFetcher(files)
	s := newStore(t, f, Config{}) // no CacheBytes
	ctx := context.Background()

	for _, name := range []string{"b.dat", "a.dat", "b.dat"} {
		r, err := s.Open(ctx, tjob(1, 0), name)
		if err != nil {
			t.Fatalf("Open %s: %v", name, err)
		}
		mustRead(t, r, files[name])
		if _, ok := r.(io.ReaderAt); ok {
			t.Errorf("%s: a streamed read claims random access", name)
		}
	}
}

// TestOpenStreamsWhatTheCacheWillNotHold: a file bigger than the budget
// is served without being cached, rather than refused.
func TestOpenStreamsWhatTheCacheWillNotHold(t *testing.T) {
	big := bytes.Repeat([]byte("B"), 4096)
	f := newFakeFetcher(map[string][]byte{"big.dat": big, "small.dat": []byte("s")})
	s := newStore(t, f, Config{CacheDir: t.TempDir(), CacheBytes: 1024})
	ctx := context.Background()

	r, err := s.Open(ctx, tjob(1, 0), "big.dat")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustRead(t, r, big)
	if got := s.CachedBytes(); got > 1024 {
		t.Errorf("cache holds %d bytes, over the 1024 budget", got)
	}
}

func TestOpenMissingAndNotRegular(t *testing.T) {
	s := newStore(t, newFakeFetcher(map[string][]byte{"a.dat": []byte("a")}, "sub"), Config{})
	ctx := context.Background()

	if _, err := s.Open(ctx, tjob(1, 0), "nope.dat"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open of a missing name = %v, want fs.ErrNotExist", err)
	}
	if _, err := s.Open(ctx, tjob(1, 0), "sub"); !errors.Is(err, ErrNotRegular) {
		t.Errorf("Open of a directory = %v, want ErrNotRegular", err)
	}
}

// TestOpenRefusesASizeThatChanged: the size has already been reported to
// the client by every stat, and serving a different length quietly is
// the silent-truncation bug this exists to avoid.
func TestOpenRefusesASizeThatChanged(t *testing.T) {
	f := newFakeFetcher(map[string][]byte{"f.dat": []byte("original")})
	s := newStore(t, f, Config{}) // no cache, so the read streams
	ctx := context.Background()

	// Build the layout, then change the sandbox underneath it.
	if _, err := s.Layout(ctx, tjob(1, 0)); err != nil {
		t.Fatalf("Layout: %v", err)
	}
	f.setFiles(map[string][]byte{"f.dat": []byte("a different length entirely")})

	if _, err := s.Open(ctx, tjob(1, 0), "f.dat"); err == nil {
		t.Fatal("Open served a file whose size no longer matched the layout")
	} else if !strings.Contains(err.Error(), "layout") {
		t.Errorf("error = %v, want it to name the disagreement with the layout", err)
	}
}

// TestOpenTarRefusesASizeThatChanged is the same hazard one layer up:
// the archive's length is declared before any byte is written and cannot
// be revised, so a mismatch has to end the transfer.
func TestOpenTarRefusesASizeThatChanged(t *testing.T) {
	f := newFakeFetcher(map[string][]byte{"f.dat": []byte("original")})
	s := newStore(t, f, Config{})
	ctx := context.Background()

	size, err := s.TarSize(ctx, tjob(1, 0))
	if err != nil {
		t.Fatalf("TarSize: %v", err)
	}
	f.setFiles(map[string][]byte{"f.dat": []byte("a different length entirely")})

	r, declared, err := s.OpenTar(ctx, tjob(1, 0))
	if err != nil {
		t.Fatalf("OpenTar: %v", err)
	}
	defer func() { _ = r.Close() }()
	if declared != size {
		t.Errorf("OpenTar declared %d, TarSize said %d", declared, size)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("the tar streamed to completion despite a size change")
	}
}

// TestMaterializeFillsTheCache covers the explicit one-pass fill a caller
// does when a client is about to read a whole sandbox.
func TestMaterializeFillsTheCache(t *testing.T) {
	files := map[string][]byte{"a": []byte("aa"), "b": []byte("bbb")}
	f := newFakeFetcher(files)
	s := newStore(t, f, Config{CacheDir: t.TempDir(), CacheBytes: 1 << 20})
	ctx := context.Background()

	if err := s.Materialize(ctx, tjob(1, 0)); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if want := int64(5); s.CachedBytes() != want {
		t.Errorf("cache holds %d bytes, want %d", s.CachedBytes(), want)
	}
	passes := f.calls.Load()
	for name, body := range files {
		r, err := s.Open(ctx, tjob(1, 0), name)
		if err != nil {
			t.Fatalf("Open %s: %v", name, err)
		}
		mustRead(t, r, body)
		if _, ok := r.(*os.File); !ok {
			t.Errorf("%s came back as %T, want a cached file", name, r)
		}
	}
	if got := f.calls.Load(); got != passes {
		t.Errorf("reading a materialized sandbox cost %d passes", got-passes)
	}
}

// TestForgetDropsBytesToo: a job forgotten has to be forgotten in both
// caches, or a stale byte would outlive the layout that described it.
func TestForgetDropsBytesToo(t *testing.T) {
	f := newFakeFetcher(map[string][]byte{"a": []byte("aa")})
	s := newStore(t, f, Config{CacheDir: t.TempDir(), CacheBytes: 1 << 20})
	ctx := context.Background()

	if err := s.Materialize(ctx, tjob(1, 0)); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if s.CachedBytes() == 0 || s.CachedEntries() == 0 {
		t.Fatal("nothing was cached")
	}
	s.Forget(tjob(1, 0))
	if s.CachedBytes() != 0 {
		t.Errorf("byte cache holds %d bytes after Forget", s.CachedBytes())
	}
	if s.CachedEntries() != 0 {
		t.Errorf("layout cache holds %d entries after Forget", s.CachedEntries())
	}
}

// TestOversizedSandboxRefusesEveryRead: the gate guards every pass, not
// just the layout one, so a limit cannot be walked around by asking for
// a file instead of a listing.
func TestOversizedSandboxRefusesEveryRead(t *testing.T) {
	f := newFakeFetcher(map[string][]byte{"a": []byte("aa")})
	f.advertise = 1 << 30
	s := newStore(t, f, Config{MaxSandboxBytes: 1024})
	ctx := context.Background()

	var tooLarge *TooLargeError
	if _, err := s.Open(ctx, tjob(1, 0), "a"); !errors.As(err, &tooLarge) {
		t.Errorf("Open = %v, want a TooLargeError", err)
	}
	if _, _, err := s.OpenTar(ctx, tjob(1, 0)); !errors.As(err, &tooLarge) {
		t.Errorf("OpenTar = %v, want a TooLargeError", err)
	}
	if err := s.Materialize(ctx, tjob(1, 0)); !errors.As(err, &tooLarge) {
		t.Errorf("Materialize = %v, want a TooLargeError", err)
	}
}

// TestCloseEndsTheTransferEarly: a client that stops reading must not
// leave a sandbox transfer running behind it.
func TestCloseEndsTheTransferEarly(t *testing.T) {
	big := bytes.Repeat([]byte("B"), 1<<20)
	f := newFakeFetcher(map[string][]byte{"big.dat": big})
	s := newStore(t, f, Config{})
	ctx := context.Background()

	r, _, err := s.OpenTar(ctx, tjob(1, 0))
	if err != nil {
		t.Fatalf("OpenTar: %v", err)
	}
	head := make([]byte, 512)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Reading after Close reports the teardown rather than hanging.
	if _, err := r.Read(head); err == nil {
		t.Error("reading after Close succeeded")
	}
}

func mustRead(t *testing.T, r io.ReadCloser, want []byte) {
	t.Helper()
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, want %d", len(got), len(want))
	}
}

// TestCachesAreNotSharedBetweenAccounts is the reason Job carries one.
// A schedd refuses a sandbox to anybody but its owner, so each fetch is
// authorised -- but a cache keyed on the job id alone would hand what it
// fetched to whoever asked next.
func TestCachesAreNotSharedBetweenAccounts(t *testing.T) {
	body := []byte("someone else's output")
	f := newFakeFetcher(map[string][]byte{"secret.dat": body})
	s := newStore(t, f, Config{CacheDir: t.TempDir(), CacheBytes: 1 << 20})
	ctx := context.Background()

	alice := Job{Account: "alice", Cluster: 42, Proc: 0}
	bob := Job{Account: "bob", Cluster: 42, Proc: 0}

	if err := s.Materialize(ctx, alice); err != nil {
		t.Fatalf("Materialize for alice: %v", err)
	}
	passes := f.calls.Load()
	if passes == 0 {
		t.Fatal("alice's materialize did no pass")
	}

	// Bob asking for the same job id must do his own fetch, under his
	// own credential, rather than reading what alice cached.
	if _, err := s.Open(ctx, bob, "secret.dat"); err != nil {
		t.Fatalf("Open for bob: %v", err)
	}
	if f.calls.Load() <= passes {
		t.Error("bob was served from alice's cache without a fetch of his own")
	}

	// And alice's own reads still come from her cache.
	before := f.calls.Load()
	r, err := s.Open(ctx, alice, "secret.dat")
	if err != nil {
		t.Fatalf("Open for alice: %v", err)
	}
	mustRead(t, r, body)
	if f.calls.Load() != before {
		t.Error("alice's cached read cost a pass")
	}

	// Forgetting one account leaves the other alone.
	s.Forget(alice)
	before = f.calls.Load()
	if _, err := s.Open(ctx, bob, "secret.dat"); err != nil {
		t.Fatalf("Open for bob after forgetting alice: %v", err)
	}
	if f.calls.Load() != before {
		t.Error("forgetting alice dropped bob's cache too")
	}
}

// TestAccountsDoNotShareOnePass: two accounts arriving together must not
// be merged by singleflight, because the pass runs with one credential
// and it would be whichever got there first.
func TestAccountsDoNotShareOnePass(t *testing.T) {
	f := newFakeFetcher(map[string][]byte{"a.dat": []byte("a")})
	f.block = make(chan struct{})
	f.entered = make(chan struct{})
	s := newStore(t, f, Config{})
	ctx := context.Background()

	done := make(chan error, 2)
	go func() { _, err := s.Layout(ctx, Job{Account: "alice", Cluster: 1}); done <- err }()
	<-f.entered
	go func() { _, err := s.Layout(ctx, Job{Account: "bob", Cluster: 1}); done <- err }()
	close(f.block)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("Layout: %v", err)
		}
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("two accounts cost %d passes, want 2", got)
	}
}

// TestAccountDirIsInjective: folding odd characters to dashes would be
// the usual answer and would let two distinct accounts share a cache
// directory, which is a cross-account leak rather than a cosmetic
// problem.
func TestAccountDirIsInjective(t *testing.T) {
	accounts := []string{
		"alice",
		"alice@example.org",
		"alice/../bob",
		"alice/bob",
		"alice\\bob",
		"alice bob",
		"=alice",
		strings.Repeat("a", 65),
		strings.Repeat("a", 66),
		"",
	}
	seen := map[string]string{}
	for _, a := range accounts {
		d := accountDir(a)
		if prev, dup := seen[d]; dup {
			t.Errorf("accounts %q and %q both map to %q", prev, a, d)
		}
		seen[d] = a
		if strings.ContainsAny(d, `/\`) || d == "." || d == ".." {
			t.Errorf("accountDir(%q) = %q, which is not one safe path element", a, d)
		}
	}
}

// TestCachePathNeedsAnAccount: an entry with no account has no directory
// to live in, and silently putting it somewhere shared is the bug this
// refuses.
func TestCachePathNeedsAnAccount(t *testing.T) {
	c := testCache(t, 1<<20)
	if _, err := c.pathFor(entryKey{job: Job{Cluster: 1}, name: "a"}); err == nil {
		t.Error("pathFor accepted an entry with no account")
	}
}

// TestAccountsGetSeparateCacheFiles checks the on-disk shape, so an
// operator can see whose bytes are held.
func TestAccountsGetSeparateCacheFiles(t *testing.T) {
	c := testCache(t, 1<<20)
	alice := entryKey{job: Job{Account: "alice", Cluster: 1}, name: "f.dat"}
	bob := entryKey{job: Job{Account: "bob", Cluster: 1}, name: "f.dat"}

	put(t, c, alice, []byte("alice's"))
	put(t, c, bob, []byte("bob's"))

	ap, err := c.pathFor(alice)
	if err != nil {
		t.Fatalf("pathFor alice: %v", err)
	}
	bp, err := c.pathFor(bob)
	if err != nil {
		t.Fatalf("pathFor bob: %v", err)
	}
	if ap == bp {
		t.Fatalf("both accounts share the path %q", ap)
	}
	for who, path := range map[string]string{"alice's": ap, "bob's": bp} {
		b, err := os.ReadFile(path) //nolint:gosec // test-built path
		if err != nil {
			t.Fatalf("reading %q: %v", path, err)
		}
		if string(b) != who {
			t.Errorf("%q holds %q, want %q", path, b, who)
		}
	}
}

// TestPrepareIsOnePass is the reason Prepare exists: Layout then
// Materialize does the same work twice, because the layout has to exist
// before anything knows what to cache.
func TestPrepareIsOnePass(t *testing.T) {
	files := map[string][]byte{"a.dat": []byte("aaa"), "b.dat": []byte("bbbb")}
	f := newFakeFetcher(files)
	s := newStore(t, f, Config{CacheDir: t.TempDir(), CacheBytes: 1 << 20})
	ctx := context.Background()
	job := tjob(3, 0)

	l, err := s.Prepare(ctx, job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("Prepare cost %d passes, want 1", got)
	}
	if len(l.Entries) != 2 {
		t.Errorf("layout has %d entries, want 2", len(l.Entries))
	}
	if want := int64(7); s.CachedBytes() != want {
		t.Errorf("cache holds %d bytes, want %d", s.CachedBytes(), want)
	}

	// And every file is readable from disk without another pass.
	for name, body := range files {
		r, err := s.Open(ctx, job, name)
		if err != nil {
			t.Fatalf("Open %s: %v", name, err)
		}
		if _, ok := r.(*os.File); !ok {
			t.Errorf("%s came back as %T, want a cached file", name, r)
		}
		mustRead(t, r, body)
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("reading a prepared sandbox cost %d passes in total, want 1", got)
	}
}

// TestPrepareFillsAnAlreadyKnownLayout: with the layout cached there is
// nothing to record, so this is the two-pass case by necessity.
func TestPrepareFillsAnAlreadyKnownLayout(t *testing.T) {
	f := newFakeFetcher(map[string][]byte{"a.dat": []byte("aaa")})
	s := newStore(t, f, Config{CacheDir: t.TempDir(), CacheBytes: 1 << 20})
	ctx := context.Background()
	job := tjob(3, 0)

	if _, err := s.Layout(ctx, job); err != nil {
		t.Fatalf("Layout: %v", err)
	}
	if s.CachedBytes() != 0 {
		t.Fatal("Layout cached bytes; it is supposed to keep none")
	}
	if _, err := s.Prepare(ctx, job); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if s.CachedBytes() == 0 {
		t.Error("Prepare did not fill the cache for a known layout")
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("took %d passes, want 2 (layout, then fill)", got)
	}
}

// TestPrepareWithoutACacheStillBuildsTheLayout: nothing to spill into is
// not an error, and the pass must not be wasted.
func TestPrepareWithoutACacheStillBuildsTheLayout(t *testing.T) {
	f := newFakeFetcher(map[string][]byte{"a.dat": []byte("aaa")})
	s := newStore(t, f, Config{}) // no cache
	l, err := s.Prepare(context.Background(), tjob(1, 0))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(l.Entries) != 1 {
		t.Errorf("layout has %d entries, want 1", len(l.Entries))
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("cost %d passes, want 1", got)
	}
}

// TestPrepareRefusesAnOversizedSandbox: the gate still guards the pass.
func TestPrepareRefusesAnOversizedSandbox(t *testing.T) {
	f := newFakeFetcher(map[string][]byte{"a.dat": []byte("aaa")})
	f.advertise = 1 << 30
	s := newStore(t, f, Config{MaxSandboxBytes: 1024, CacheDir: t.TempDir(), CacheBytes: 1 << 20})

	var tooLarge *TooLargeError
	if _, err := s.Prepare(context.Background(), tjob(1, 0)); !errors.As(err, &tooLarge) {
		t.Errorf("Prepare = %v, want a TooLargeError", err)
	}
	if s.CachedBytes() != 0 || s.CachedEntries() != 0 {
		t.Error("a refused Prepare cached something")
	}
}
