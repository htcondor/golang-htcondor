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
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeFetcher writes a fixed sandbox as a tar, after offering the
// advertised size to the gate exactly as a real sender does: the
// preamble first, then the files, so a refusal costs no bytes.
type fakeFetcher struct {
	files map[string][]byte
	dirs  []string
	// advertise overrides the summed size; advertiseKnown false models a
	// sender that declared nothing.
	advertise      int64
	advertiseKnown bool

	calls atomic.Int64
	// bytesSent counts payload bytes actually written, so a test can
	// assert a refusal moved none.
	bytesSent atomic.Int64

	// block, when non-nil, is waited on before the preamble, so a test
	// can hold a fetch open and pile callers behind it. entered closes
	// once a fetch has started, which is what tells the test the flight
	// is registered and later callers will join it rather than race it.
	block   chan struct{}
	entered chan struct{}
	once    sync.Once

	mu sync.Mutex
}

func newFakeFetcher(files map[string][]byte, dirs ...string) *fakeFetcher {
	f := &fakeFetcher{files: files, dirs: dirs, advertiseKnown: true}
	for _, b := range files {
		f.advertise += int64(len(b))
	}
	return f
}

func (f *fakeFetcher) FetchSandbox(_ context.Context, _ Job, w io.Writer,
	onPreamble func(int64, bool) error) error {
	f.calls.Add(1)
	if f.entered != nil {
		f.once.Do(func() { close(f.entered) })
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	advertise, advertiseKnown := f.advertise, f.advertiseKnown
	f.mu.Unlock()
	if onPreamble != nil {
		if err := onPreamble(advertise, advertiseKnown); err != nil {
			return err
		}
	}
	f.mu.Lock()
	files := f.files
	f.mu.Unlock()

	tw := tar.NewWriter(w)
	for _, d := range f.dirs {
		if err := tw.WriteHeader(&tar.Header{
			Name: d + "/", Typeflag: tar.TypeDir, Mode: 0o755,
		}); err != nil {
			return err
		}
	}
	for _, name := range sortedKeys(files) {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
		f.bytesSent.Add(int64(len(body)))
	}
	return tw.Close()
}

// setFiles replaces the sandbox between passes, so a test can make the
// wire disagree with a layout already built.
func (f *fakeFetcher) setFiles(files map[string][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files = files
	f.advertise = 0
	for _, b := range files {
		f.advertise += int64(len(b))
	}
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Small and fixed in these tests; insertion order does not matter,
	// only determinism.
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// tjob is the job every test reads, under one account. The account is
// part of the cache key, so it has to be spelled somewhere.
func tjob(cluster, proc int) Job {
	return Job{Account: "tester", Cluster: cluster, Proc: proc}
}

// newStore builds a Store, failing the test rather than returning an
// error at every call site.
func newStore(t *testing.T, f Fetcher, cfg Config) *Store {
	t.Helper()
	if cfg.Logf == nil {
		cfg.Logf = t.Logf
	}
	s, err := New(f, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func sandbox(n int) map[string][]byte {
	m := map[string][]byte{}
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("f%03d.dat", i)] = bytes.Repeat([]byte("x"), i+1)
	}
	return m
}

// TestLayoutRecordsNamesAndSizes is the basic contract: the layout
// describes the sandbox and holds none of it.
func TestLayoutRecordsNamesAndSizes(t *testing.T) {
	f := newFakeFetcher(map[string][]byte{
		"out.txt":     []byte("hello"),
		"sub/big.dat": bytes.Repeat([]byte("ab"), 1000),
	}, "sub")
	s := newStore(t, f, Config{Logf: t.Logf})

	l, err := s.Layout(context.Background(), tjob(12, 0))
	if err != nil {
		t.Fatalf("Layout: %v", err)
	}
	if len(l.Entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(l.Entries), l.Entries)
	}
	e, ok := l.Lookup("out.txt")
	if !ok {
		t.Fatal(`no entry "out.txt"`)
	}
	if e.Size != 5 || e.Dir || e.Mode != 0o644 {
		t.Errorf("out.txt = %+v, want size 5, mode 644, not a dir", e)
	}
	if d, ok := l.Lookup("sub"); !ok || !d.Dir || d.Size != 0 {
		t.Errorf(`sub = %+v (found %v), want a directory of size 0`, d, ok)
	}
	if want := int64(2005); l.Bytes() != want {
		t.Errorf("Bytes() = %d, want %d", l.Bytes(), want)
	}
	if !l.AdvertisedKnown || l.Advertised != 2005 {
		t.Errorf("advertised = (%d, %v), want (2005, true)", l.Advertised, l.AdvertisedKnown)
	}
}

// TestLayoutIsCached asserts the second ask costs no pass. A layout that
// re-fetched would make every stat a full-bandwidth drain, which is the
// whole thing this exists to avoid.
func TestLayoutIsCached(t *testing.T) {
	f := newFakeFetcher(sandbox(3))
	s := newStore(t, f, Config{Logf: t.Logf})

	for i := 0; i < 4; i++ {
		if _, err := s.Layout(context.Background(), tjob(7, 1)); err != nil {
			t.Fatalf("Layout %d: %v", i, err)
		}
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("fetched %d times, want 1", got)
	}

	s.Forget(tjob(7, 1))
	if _, err := s.Layout(context.Background(), tjob(7, 1)); err != nil {
		t.Fatalf("Layout after Forget: %v", err)
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("fetched %d times after Forget, want 2", got)
	}
}

// TestLayoutSharesOnePass asserts concurrent askers share one drain.
func TestLayoutSharesOnePass(t *testing.T) {
	f := newFakeFetcher(sandbox(5))
	f.block = make(chan struct{})
	f.entered = make(chan struct{})
	s := newStore(t, f, Config{Logf: t.Logf})

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	ask := func() {
		defer wg.Done()
		_, err := s.Layout(context.Background(), tjob(9, 0))
		errs <- err
	}

	// One caller first, and not until its fetch has actually started do
	// the rest arrive -- otherwise they could reach singleflight before
	// the first registered, and two passes would be a legal outcome of a
	// test meant to prove there is one.
	wg.Add(1)
	go ask()
	<-f.entered
	for i := 1; i < callers; i++ {
		wg.Add(1)
		go ask()
	}
	close(f.block)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Layout: %v", err)
		}
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("fetched %d times for %d concurrent callers, want 1", got, callers)
	}
}

// TestOversizedSandboxRefusedBeforeAnyBytes is the point of gating on the
// preamble: the refusal has to cost nothing, or the limit would be paid
// for in full every time it fired.
func TestOversizedSandboxRefusedBeforeAnyBytes(t *testing.T) {
	f := newFakeFetcher(sandbox(4))
	f.advertise = 1 << 20
	s := newStore(t, f, Config{MaxSandboxBytes: 1024, Logf: t.Logf})

	_, err := s.Layout(context.Background(), tjob(3, 2))
	var tooLarge *TooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("Layout error = %v, want a TooLargeError", err)
	}
	if tooLarge.Size != 1<<20 || tooLarge.Limit != 1024 {
		t.Errorf("= %+v, want size 1MiB over a 1024 limit", tooLarge)
	}
	if tooLarge.Cluster != 3 || tooLarge.Proc != 2 {
		t.Errorf("= job %d.%d, want 3.2", tooLarge.Cluster, tooLarge.Proc)
	}
	if got := f.bytesSent.Load(); got != 0 {
		t.Errorf("a refused sandbox moved %d bytes, want 0", got)
	}
	// And nothing was cached, so a later ask with a bigger limit works.
	if got := s.CachedEntries(); got != 0 {
		t.Errorf("cache holds %d entries after a refusal, want 0", got)
	}
}

// TestUnadvertisedSizeRefusedUnlessAllowed covers the case a size limit
// cannot judge. Zero-with-nothing-advertised is not an empty sandbox.
func TestUnadvertisedSizeRefusedUnlessAllowed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve bool
	}{
		{"refused by default", false},
		{"served when allowed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeFetcher(sandbox(2))
			f.advertiseKnown = false
			f.advertise = 0
			s := newStore(t, f, Config{MaxSandboxBytes: 1 << 20, ServeUnadvertisedSize: tc.serve, Logf: t.Logf})

			l, err := s.Layout(context.Background(), tjob(1, 0))
			var unknown *SizeUnknownError
			switch {
			case tc.serve:
				if err != nil {
					t.Fatalf("Layout: %v", err)
				}
				if len(l.Entries) != 2 {
					t.Errorf("got %d entries, want 2", len(l.Entries))
				}
				if l.AdvertisedKnown {
					t.Error("AdvertisedKnown is true although the sender advertised nothing")
				}
			default:
				if !errors.As(err, &unknown) {
					t.Fatalf("Layout error = %v, want a SizeUnknownError", err)
				}
				if got := f.bytesSent.Load(); got != 0 {
					t.Errorf("a refused sandbox moved %d bytes, want 0", got)
				}
			}
		})
	}
}

// TestNoLimitServesAnySize asserts a negative limit disables the gate,
// including for an unadvertised size, since there is then nothing to
// judge against.
func TestNoLimitServesAnySize(t *testing.T) {
	f := newFakeFetcher(sandbox(2))
	f.advertise = 1 << 50
	s := newStore(t, f, Config{MaxSandboxBytes: -1, Logf: t.Logf})
	if _, err := s.Layout(context.Background(), tjob(1, 0)); err != nil {
		t.Fatalf("Layout: %v", err)
	}
}

// TestTooManyEntriesRefused covers the other shape of too-big: a sandbox
// whose size is fine but whose name count is not.
func TestTooManyEntriesRefused(t *testing.T) {
	f := newFakeFetcher(sandbox(50))
	s := newStore(t, f, Config{MaxEntriesPerJob: 10, Logf: t.Logf})

	_, err := s.Layout(context.Background(), tjob(1, 0))
	if !errors.Is(err, ErrTooManyEntries) {
		t.Fatalf("Layout error = %v, want ErrTooManyEntries", err)
	}
	if got := s.CachedEntries(); got != 0 {
		t.Errorf("cache holds %d entries after a refusal, want 0", got)
	}
}

// TestLayoutCacheEvictsByEntryCount asserts the budget is counted in
// entries across jobs, and that the least recently used job goes first.
func TestLayoutCacheEvictsByEntryCount(t *testing.T) {
	f := newFakeFetcher(sandbox(4))
	s := newStore(t, f, Config{MaxCachedEntries: 10, Logf: t.Logf})
	ctx := context.Background()

	// Three jobs of four entries each is twelve, over the ten budget.
	for _, proc := range []int{0, 1, 2} {
		if _, err := s.Layout(ctx, tjob(1, proc)); err != nil {
			t.Fatalf("Layout 1.%d: %v", proc, err)
		}
	}
	if got := s.CachedEntries(); got > 10 {
		t.Errorf("cache holds %d entries, over the 10 budget", got)
	}
	// 1.0 was the least recently used, so it is the one that went.
	before := f.calls.Load()
	if _, err := s.Layout(ctx, tjob(1, 2)); err != nil {
		t.Fatalf("Layout 1.2: %v", err)
	}
	if f.calls.Load() != before {
		t.Error("the most recently used layout was evicted")
	}
	if _, err := s.Layout(ctx, tjob(1, 0)); err != nil {
		t.Fatalf("Layout 1.0: %v", err)
	}
	if f.calls.Load() != before+1 {
		t.Error("the least recently used layout was not evicted")
	}
}

// TestOneOversizedLayoutIsStillCached: the budget is a target, not a
// guarantee. Evicting everything and remaining over it helps nobody.
func TestOneOversizedLayoutIsStillCached(t *testing.T) {
	f := newFakeFetcher(sandbox(20))
	s := newStore(t, f, Config{MaxCachedEntries: 5, Logf: t.Logf})

	if _, err := s.Layout(context.Background(), tjob(1, 0)); err != nil {
		t.Fatalf("Layout: %v", err)
	}
	if got := s.CachedEntries(); got != 20 {
		t.Fatalf("cache holds %d entries, want the 20 of the only layout", got)
	}
	if _, err := s.Layout(context.Background(), tjob(1, 0)); err != nil {
		t.Fatalf("Layout again: %v", err)
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("fetched %d times, want 1", got)
	}
}

// TestRefusedEntryNames asserts a name that cannot mean what it says is
// refused rather than repaired: repairing silently changes which file a
// caller asked for.
func TestRefusedEntryNames(t *testing.T) {
	for _, name := range []string{
		"/etc/passwd",
		"../escape",
		"sub/../../escape",
		`sub\win`,
		"./odd",
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := tw.WriteHeader(&tar.Header{
				Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1,
			}); err != nil {
				t.Fatalf("WriteHeader: %v", err)
			}
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if err := tw.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if _, err := recordLayout(&buf, 0, nil); err == nil {
				t.Fatalf("recordLayout accepted %q", name)
			}
		})
	}
}

// TestSkippedEntryTypes: a sandbox tar carries files and directories. A
// name whose meaning depends on resolving somewhere else is left out of
// the layout rather than projected.
func TestSkippedEntryTypes(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range []*tar.Header{
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777},
		{Name: "fifo", Typeflag: tar.TypeFifo, Mode: 0o644},
		{Name: "real.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("WriteHeader %q: %v", h.Name, err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte("hi")); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	l, err := recordLayout(&buf, 0, nil)
	if err != nil {
		t.Fatalf("recordLayout: %v", err)
	}
	if len(l.Entries) != 1 || l.Entries[0].Name != "real.txt" {
		t.Errorf("entries = %+v, want only real.txt", l.Entries)
	}
}

// TestFetchErrorPropagates: a transport failure is not an empty sandbox.
func TestFetchErrorPropagates(t *testing.T) {
	boom := errors.New("schedd went away")
	s := newStore(t, errFetcher{boom}, Config{Logf: t.Logf})
	if _, err := s.Layout(context.Background(), tjob(1, 0)); !errors.Is(err, boom) {
		t.Fatalf("Layout error = %v, want %v", err, boom)
	}
	if got := s.CachedEntries(); got != 0 {
		t.Errorf("cache holds %d entries after a failure, want 0", got)
	}
}

type errFetcher struct{ err error }

func (e errFetcher) FetchSandbox(_ context.Context, _ Job, _ io.Writer,
	onPreamble func(int64, bool) error) error {
	if onPreamble != nil {
		if err := onPreamble(10, true); err != nil {
			return err
		}
	}
	return e.err
}

// TestTruncatedSandboxIsAnError: a stream that stops mid-tar must not
// read as a shorter sandbox.
func TestTruncatedSandboxIsAnError(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "big.dat", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4096,
	}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tw.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Deliberately no Close: the trailer is missing and the body is cut.
	cut := buf.Bytes()[:600]

	if _, err := recordLayout(bytes.NewReader(cut), 0, nil); err == nil {
		t.Fatal("recordLayout accepted a truncated sandbox")
	} else if !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("error = %v, want it to name the sandbox", err)
	}
}
