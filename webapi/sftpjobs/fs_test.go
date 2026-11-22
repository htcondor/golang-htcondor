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
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/pkg/sftp"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
)

const testAccount = "alice"

// fakeSandbox serves one fixed sandbox as a tar, recording the Jobs it
// was asked for so a test can check the account travelled with them.
type fakeSandbox struct {
	files map[string][]byte
	dirs  []string

	calls atomic.Int64
	mu    chan struct{} // capacity-1 lock, so asked is race-free
	asked []sandboxfs.Job
}

func newFakeSandbox(files map[string][]byte, dirs ...string) *fakeSandbox {
	return &fakeSandbox{files: files, dirs: dirs, mu: make(chan struct{}, 1)}
}

func (f *fakeSandbox) FetchSandbox(_ context.Context, job sandboxfs.Job, w io.Writer,
	onPreamble func(int64, bool) error) error {
	f.calls.Add(1)
	f.mu <- struct{}{}
	f.asked = append(f.asked, job)
	<-f.mu

	var total int64
	for _, b := range f.files {
		total += int64(len(b))
	}
	if onPreamble != nil {
		if err := onPreamble(total, true); err != nil {
			return err
		}
	}
	tw := tar.NewWriter(w)
	for _, d := range f.dirs {
		if err := tw.WriteHeader(&tar.Header{Name: d + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(f.files))
	for n := range f.files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body := f.files[n]
		if err := tw.WriteHeader(&tar.Header{
			Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
	}
	return tw.Close()
}

func (f *fakeSandbox) jobs() []sandboxfs.Job {
	f.mu <- struct{}{}
	defer func() { <-f.mu }()
	return append([]sandboxfs.Job(nil), f.asked...)
}

type fixedJobs []JobRef

func (j fixedJobs) ReadableJobs(context.Context) ([]JobRef, error) { return j, nil }

func sampleFiles() map[string][]byte {
	return map[string][]byte{
		"stdout":         []byte("hello from the job\n"),
		"result.csv":     bytes.Repeat([]byte("a,b,c\n"), 500),
		"sub/nested.dat": bytes.Repeat([]byte("n"), 1234),
		"sub/deep/x.bin": bytes.Repeat([]byte("d"), 77),
	}
}

type testFS struct {
	*FS
	sandbox *fakeSandbox
	store   *sandboxfs.Store
}

func newTestFS(t *testing.T, cache bool, jobs JobLister) *testFS {
	t.Helper()
	return newTestFSWith(t, newFakeSandbox(sampleFiles(), "sub"), cache, jobs)
}

func newTestFSWith(t *testing.T, sb *fakeSandbox, cache bool, jobs JobLister) *testFS {
	t.Helper()
	cfg := sandboxfs.Config{Logf: t.Logf}
	if cache {
		cfg.CacheDir = t.TempDir()
		cfg.CacheBytes = 1 << 20
	}
	store, err := sandboxfs.New(sb, cfg)
	if err != nil {
		t.Fatalf("sandboxfs.New: %v", err)
	}
	f, err := New(Config{
		Store:   store,
		Account: testAccount,
		Context: htcondor.WithAuthenticatedUser(context.Background(), testAccount),
		Jobs:    jobs,
		Logf:    t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testFS{FS: f, sandbox: sb, store: store}
}

func listNames(t *testing.T, f *FS, p string) []string {
	t.Helper()
	l, err := f.Filelist(&sftp.Request{Method: "List", Filepath: p})
	if err != nil {
		t.Fatalf("Filelist(%q): %v", p, err)
	}
	var names []string
	buf := make([]os.FileInfo, 8)
	for off := int64(0); ; {
		n, err := l.ListAt(buf, off)
		for _, fi := range buf[:n] {
			name := fi.Name()
			if fi.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		off += int64(n)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("ListAt(%q): %v", p, err)
			}
			break
		}
	}
	return names
}

func statOne(t *testing.T, f *FS, p string) (os.FileInfo, error) {
	t.Helper()
	l, err := f.Filelist(&sftp.Request{Method: "Stat", Filepath: p})
	if err != nil {
		return nil, err
	}
	buf := make([]os.FileInfo, 1)
	n, err := l.ListAt(buf, 0)
	if n == 0 {
		if err == nil || errors.Is(err, io.EOF) {
			return nil, fs.ErrNotExist
		}
		return nil, err
	}
	return buf[0], nil
}

func TestListRootAndOutputs(t *testing.T) {
	f := newTestFS(t, false, fixedJobs{{Cluster: 12, Proc: 0}, {Cluster: 7, Proc: 3}})

	if got := listNames(t, f.FS, "/"); len(got) != 1 || got[0] != "outputs/" {
		t.Errorf("/ = %v, want [outputs/]", got)
	}
	got := listNames(t, f.FS, "/outputs")
	want := []string{"7.3/", "12.0/"}
	if len(got) != len(want) {
		t.Fatalf("/outputs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("/outputs = %v, want %v", got, want)
			break
		}
	}
	// Listing jobs must not read any sandbox.
	if n := f.sandbox.calls.Load(); n != 0 {
		t.Errorf("listing /outputs read %d sandboxes, want 0", n)
	}
}

// TestOutputsDoesNotListArchives is the deliberate omission: listing an
// archive means knowing its length, and that needs the job's layout --
// so an ls of /outputs would drain every sandbox to answer.
func TestOutputsDoesNotListArchives(t *testing.T) {
	f := newTestFS(t, false, fixedJobs{{Cluster: 12, Proc: 0}})
	for _, name := range listNames(t, f.FS, "/outputs") {
		if strings.HasSuffix(name, ".tar") {
			t.Errorf("/outputs listed the archive %q", name)
		}
	}
	// It is still reachable by name.
	fi, err := statOne(t, f.FS, "/outputs/12.0/data.tar")
	if err != nil {
		t.Fatalf("stat of the archive: %v", err)
	}
	if fi.Size() == 0 || fi.IsDir() {
		t.Errorf("archive stat = size %d dir %v, want a non-empty file", fi.Size(), fi.IsDir())
	}
}

func TestListJobDirectory(t *testing.T) {
	f := newTestFS(t, true, fixedJobs{{Cluster: 12, Proc: 0}})

	got := listNames(t, f.FS, "/outputs/12.0/data")
	want := []string{"result.csv", "stdout", "sub/"}
	if len(got) != len(want) {
		t.Fatalf("job directory = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("job directory = %v, want %v", got, want)
		}
	}
	// One pass: Prepare records the layout and fills the cache together.
	if n := f.sandbox.calls.Load(); n != 1 {
		t.Errorf("listing a job directory cost %d passes, want 1", n)
	}

	// Nested levels, including one the tar carried no directory header
	// for.
	if got := listNames(t, f.FS, "/outputs/12.0/data/sub"); len(got) != 2 ||
		got[0] != "deep/" || got[1] != "nested.dat" {
		t.Errorf("sub = %v, want [deep/ nested.dat]", got)
	}
	if got := listNames(t, f.FS, "/outputs/12.0/data/sub/deep"); len(got) != 1 || got[0] != "x.bin" {
		t.Errorf("sub/deep = %v, want [x.bin]", got)
	}
	if n := f.sandbox.calls.Load(); n != 1 {
		t.Errorf("listing nested directories cost %d passes in total, want 1", n)
	}
}

func TestStatEntries(t *testing.T) {
	f := newTestFS(t, true, fixedJobs{{Cluster: 12, Proc: 0}})
	files := sampleFiles()

	// A job directory is answered without reading the sandbox: a client
	// stats a directory before listing it.
	fi, err := statOne(t, f.FS, "/outputs/12.0")
	if err != nil {
		t.Fatalf("stat job dir: %v", err)
	}
	if !fi.IsDir() {
		t.Error("the job directory did not stat as a directory")
	}
	if n := f.sandbox.calls.Load(); n != 0 {
		t.Errorf("stat of a job directory read %d sandboxes, want 0", n)
	}

	for name, body := range files {
		fi, err := statOne(t, f.FS, "/outputs/12.0/data/"+name)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if fi.Size() != int64(len(body)) {
			t.Errorf("stat %s = %d bytes, want %d", name, fi.Size(), len(body))
		}
		if fi.IsDir() {
			t.Errorf("stat %s says directory", name)
		}
	}
	if fi, err := statOne(t, f.FS, "/outputs/12.0/data/sub"); err != nil || !fi.IsDir() {
		t.Errorf("stat of a sandbox directory = %v, %v", fi, err)
	}
	if _, err := statOne(t, f.FS, "/outputs/12.0/data/nope"); err == nil {
		t.Error("stat of a missing name succeeded")
	}
}

// TestStatUnknownJob: with a lister wired up the answer is accurate
// rather than an empty directory that only fails when listed.
func TestStatUnknownJob(t *testing.T) {
	f := newTestFS(t, false, fixedJobs{{Cluster: 12, Proc: 0}})
	if _, err := statOne(t, f.FS, "/outputs/999.0"); err == nil {
		t.Error("stat of a job the caller cannot read succeeded")
	}
}

// TestWithoutAListerJobsAreStillReachable: addressing a job by id is the
// contract; the listing is a convenience.
func TestWithoutAListerJobsAreStillReachable(t *testing.T) {
	f := newTestFS(t, true, nil)
	if got := listNames(t, f.FS, "/outputs"); len(got) != 0 {
		t.Errorf("/outputs = %v, want empty without a lister", got)
	}
	if got := listNames(t, f.FS, "/outputs/12.0/data"); len(got) == 0 {
		t.Error("a job named directly was not readable without a lister")
	}
}

func TestReadFileFromCacheIsSeekable(t *testing.T) {
	f := newTestFS(t, true, fixedJobs{{Cluster: 12, Proc: 0}})
	// Fill the cache first, as a listing would.
	listNames(t, f.FS, "/outputs/12.0/data")

	ra, err := f.Fileread(&sftp.Request{Method: "Get", Filepath: "/outputs/12.0/data/stdout"})
	if err != nil {
		t.Fatalf("Fileread: %v", err)
	}
	// A cache hit must come through unwrapped: wrapping a real file in
	// the forward-only window would make a seekable read one-way.
	if _, ok := ra.(*os.File); !ok {
		t.Errorf("a cached read came back as %T, want *os.File", ra)
	}
	want := sampleFiles()["stdout"]
	buf := make([]byte, len(want))
	if _, err := ra.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(buf, want) {
		t.Errorf("read %q, want %q", buf, want)
	}
	// Backwards is fine on a real file.
	if _, err := ra.ReadAt(buf[:5], 0); err != nil && !errors.Is(err, io.EOF) {
		t.Errorf("backwards ReadAt on a cached file: %v", err)
	}
}

func TestReadFileWithoutACacheStreams(t *testing.T) {
	f := newTestFS(t, false, fixedJobs{{Cluster: 12, Proc: 0}})
	ra, err := f.Fileread(&sftp.Request{Method: "Get", Filepath: "/outputs/12.0/data/result.csv"})
	if err != nil {
		t.Fatalf("Fileread: %v", err)
	}
	if _, ok := ra.(*os.File); ok {
		t.Error("a streamed read came back as a file")
	}
	want := sampleFiles()["result.csv"]
	got := make([]byte, 0, len(want))
	buf := make([]byte, 512)
	for off := 0; off < len(want); off += len(buf) {
		n, err := ra.ReadAt(buf, int64(off))
		got = append(got, buf[:n]...)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt(%d): %v", off, err)
		}
	}
	if !bytes.Equal(got, want) {
		t.Errorf("streamed %d bytes, want %d", len(got), len(want))
	}
}

// TestReadArchiveMatchesItsStattedSize is the whole point of the exact
// tar sizing: a client is told the length before it reads.
func TestReadArchiveMatchesItsStattedSize(t *testing.T) {
	f := newTestFS(t, false, fixedJobs{{Cluster: 12, Proc: 0}})

	fi, err := statOne(t, f.FS, "/outputs/12.0/data.tar")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	ra, err := f.Fileread(&sftp.Request{Method: "Get", Filepath: "/outputs/12.0/data.tar"})
	if err != nil {
		t.Fatalf("Fileread: %v", err)
	}
	body := make([]byte, 0, fi.Size())
	buf := make([]byte, 32*1024)
	for off := int64(0); ; {
		n, err := ra.ReadAt(buf, off)
		body = append(body, buf[:n]...)
		off += int64(n)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt(%d): %v", off, err)
			}
			break
		}
	}
	if int64(len(body)) != fi.Size() {
		t.Fatalf("archive is %d bytes, stat said %d", len(body), fi.Size())
	}
	// And it unpacks to the sandbox.
	want := sampleFiles()
	tr := tar.NewReader(bytes.NewReader(body))
	seen := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("reading the archive: %v", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		got, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %q: %v", h.Name, err)
		}
		if !bytes.Equal(got, want[h.Name]) {
			t.Errorf("%q: %d bytes, want %d", h.Name, len(got), len(want[h.Name]))
		}
		seen++
	}
	if seen != len(want) {
		t.Errorf("archive held %d files, want %d", seen, len(want))
	}
}

func TestDirectoriesAreNotReadable(t *testing.T) {
	f := newTestFS(t, false, fixedJobs{{Cluster: 12, Proc: 0}})
	for _, p := range []string{"/", "/outputs", "/outputs/12.0", "/outputs/12.0/data"} {
		_, err := f.Fileread(&sftp.Request{Method: "Get", Filepath: p})
		if !errors.Is(err, syscall.EISDIR) {
			t.Errorf("Fileread(%q) = %v, want EISDIR", p, err)
		}
	}
}

// TestWritesAreRefusedWithAReason: an unset handler makes the request
// server answer "unexpected request", which tells somebody whose upload
// failed nothing at all.
func TestWritesAreRefusedWithAReason(t *testing.T) {
	f := newTestFS(t, false, nil)
	// A path that EXISTS and cannot be written: read-only.
	_, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: "/outputs/12.0/data/stdout"})
	if !errors.Is(err, syscall.EROFS) {
		t.Errorf("Filewrite of an existing file = %v, want EROFS", err)
	}
	// A path that does not exist: say so, rather than calling it
	// read-only. "/outputs/x" names no job.
	if _, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: "/outputs/x"}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Filewrite of a nonexistent path = %v, want fs.ErrNotExist", err)
	}
	for _, m := range []string{"Mkdir", "Remove", "Rename", "Setstat", "Rmdir"} {
		if err := f.Filecmd(&sftp.Request{Method: m, Filepath: "/outputs/x"}); !errors.Is(err, syscall.EROFS) {
			t.Errorf("Filecmd(%s) = %v, want EROFS", m, err)
		}
	}
}

// TestEveryPathCarriesTheAccount: the FS is bound to one account, so no
// path a client sends can reach another's job.
func TestEveryPathCarriesTheAccount(t *testing.T) {
	f := newTestFS(t, true, fixedJobs{{Cluster: 12, Proc: 0}})
	listNames(t, f.FS, "/outputs/12.0/data")
	if _, err := statOne(t, f.FS, "/outputs/12.0/data.tar"); err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	jobs := f.sandbox.jobs()
	if len(jobs) == 0 {
		t.Fatal("no sandbox was read")
	}
	for _, j := range jobs {
		if j.Account != testAccount {
			t.Errorf("a sandbox was read as %q, want %q", j.Account, testAccount)
		}
	}
}

func TestBadPaths(t *testing.T) {
	f := newTestFS(t, false, fixedJobs{{Cluster: 12, Proc: 0}})
	for _, p := range []string{
		"/elsewhere",
		"/outputs/notajob",
		"/outputs/+5",
		"/outputs/ 5",
		"/outputs/5.",
		"/outputs/12.0/data.tar/inside",
		"/../etc/passwd",
		"/outputs/../../etc/passwd",
	} {
		if _, err := statOne(t, f.FS, p); err == nil {
			t.Errorf("stat(%q) succeeded", p)
		}
		if _, err := f.Fileread(&sftp.Request{Method: "Get", Filepath: p}); err == nil {
			t.Errorf("Fileread(%q) succeeded", p)
		}
	}
}

func TestNewRequiresItsPieces(t *testing.T) {
	store, err := sandboxfs.New(newFakeSandbox(nil), sandboxfs.Config{})
	if err != nil {
		t.Fatalf("sandboxfs.New: %v", err)
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"no store", Config{Account: "a", Context: ctx}},
		{"no account", Config{Store: store, Context: ctx}},
		{"no context", Config{Store: store, Account: "a"}},
	} {
		if _, err := New(tc.cfg); err == nil {
			t.Errorf("New with %s succeeded", tc.name)
		}
	}
}
