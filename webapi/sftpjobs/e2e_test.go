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
	"bytes"
	"errors"
	"io"
	"net"
	"sort"
	"testing"

	"github.com/pkg/sftp"

	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
)

// dial runs the handlers behind a real request server and returns a real
// client. Everything about how a client actually behaves -- pipelined
// reads, a readdir in pages, a stat before every open -- comes from the
// library rather than from this test's idea of it.
func dial(t *testing.T, f *testFS) *sftp.Client {
	t.Helper()
	c1, c2 := net.Pipe()
	srv := sftp.NewRequestServer(c2, f.Handlers())
	go func() {
		if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
			t.Logf("serve ended: %v", err)
		}
	}()
	client, err := sftp.NewClientPipe(c1, c1)
	if err != nil {
		t.Fatalf("NewClientPipe: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = srv.Close()
	})
	return client
}

// dialFS is dial for a view built elsewhere -- the trigger tests build
// one with a submitter, which newTestFS does not.
func dialFS(t *testing.T, f *FS) *sftp.Client {
	t.Helper()
	c1, c2 := net.Pipe()
	srv := sftp.NewRequestServer(c2, f.Handlers())
	go func() {
		if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
			t.Logf("serve ended: %v", err)
		}
	}()
	client, err := sftp.NewClientPipe(c1, c1)
	if err != nil {
		t.Fatalf("NewClientPipe: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = srv.Close()
	})
	return client
}

// TestClientDropsAnArchive is the upload path through a real client,
// which is the only way to find out what a real client does around a
// write: pkg/sftp's own Create/Write/Close, with the FSETSTAT it sends
// after writing -- against handlers that refuse every Filecmd. A direct
// call to Filewrite skips all of it, so every other upload test here
// would pass on a surface no client could actually put a file on.
func TestClientDropsAnArchive(t *testing.T) {
	f := newTriggerFS(t)
	c := dialFS(t, f.FS)

	content := tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: ownSubmit},
		archiveFile{Name: "run.sh", Body: "#!/bin/sh\nexit 0\n"})

	w, err := c.Create("/submit/run.tar")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// The commit point, and the only place a refusal can be reported.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read the outcome back the way somebody would.
	st, err := c.Open("/submit/status/run.tar")
	if err != nil {
		t.Fatalf("opening the status entry: %v", err)
	}
	defer func() { _ = st.Close() }()
	text, err := io.ReadAll(st)
	if err != nil {
		t.Fatalf("reading the status entry: %v", err)
	}
	if !bytes.Contains(text, []byte("state: submitted")) {
		t.Errorf("status = %q, want a submitted state", text)
	}
	if f.sub.archiveCount() != 1 {
		t.Errorf("the submitter saw %d archives, want 1", f.sub.archiveCount())
	}
}

// TestClientDropsACSV is the same for a template's submit directory:
// the CSV path had no real-client upload test either.
func TestClientDropsACSV(t *testing.T) {
	f := newTriggerFS(t)
	c := dialFS(t, f.FS)

	w, err := c.Create(blastDir + "/submit/runs.csv")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("sample,reads\nHG001,a.fastq\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if f.sub.count() != 1 {
		t.Fatalf("the submitter saw %d CSVs, want 1", f.sub.count())
	}
}

func TestClientWalksTheTree(t *testing.T) {
	f := newTestFS(t, true, fixedJobs{{Cluster: 12, Proc: 0}, {Cluster: 7, Proc: 3}})
	c := dial(t, f)

	top, err := c.ReadDir("/")
	if err != nil {
		t.Fatalf("ReadDir(/): %v", err)
	}
	if len(top) != 1 || top[0].Name() != outputsDir || !top[0].IsDir() {
		t.Fatalf("ReadDir(/) = %v", top)
	}

	jobs, err := c.ReadDir("/outputs")
	if err != nil {
		t.Fatalf("ReadDir(/outputs): %v", err)
	}
	var names []string
	for _, fi := range jobs {
		if !fi.IsDir() {
			t.Errorf("/outputs holds the non-directory %q", fi.Name())
		}
		names = append(names, fi.Name())
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "12.0" || names[1] != "7.3" {
		t.Fatalf("/outputs = %v, want [12.0 7.3]", names)
	}

	want := sampleFiles()
	entries, err := c.ReadDir("/outputs/12.0/data")
	if err != nil {
		t.Fatalf("ReadDir(/outputs/12.0/data): %v", err)
	}
	for _, fi := range entries {
		if fi.IsDir() {
			continue
		}
		if got, ok := want[fi.Name()]; !ok {
			t.Errorf("unexpected entry %q", fi.Name())
		} else if fi.Size() != int64(len(got)) {
			t.Errorf("%q is %d bytes, want %d", fi.Name(), fi.Size(), len(got))
		}
	}
}

func TestClientReadsEveryFile(t *testing.T) {
	f := newTestFS(t, true, fixedJobs{{Cluster: 12, Proc: 0}})
	c := dial(t, f)

	for name, want := range sampleFiles() {
		p := "/outputs/12.0/data/" + name
		fi, err := c.Stat(p)
		if err != nil {
			t.Fatalf("Stat(%s): %v", p, err)
		}
		if fi.Size() != int64(len(want)) {
			t.Errorf("Stat(%s) = %d, want %d", p, fi.Size(), len(want))
		}

		rf, err := c.Open(p)
		if err != nil {
			t.Fatalf("Open(%s): %v", p, err)
		}
		got, err := io.ReadAll(rf)
		_ = rf.Close()
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: read %d bytes, want %d", p, len(got), len(want))
		}
	}
}

// TestClientDownloadsAStreamedArchive is the real test of the streaming
// window: with no byte cache the archive is generated as it is read, and
// WriteTo issues many concurrent range requests -- which is the access
// pattern SequentialReaderAt exists to survive and the one this test
// would otherwise only be guessing at.
func TestClientDownloadsAStreamedArchive(t *testing.T) {
	f := newTestFS(t, false, fixedJobs{{Cluster: 12, Proc: 0}}) // no cache: streams
	c := dial(t, f)

	const p = "/outputs/12.0/data.tar"
	fi, err := c.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	rf, err := c.Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rf.Close() }()

	// WriteTo is the concurrent path; io.ReadAll would be sequential and
	// would not exercise it.
	var buf bytes.Buffer
	n, err := rf.WriteTo(&buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("WriteTo: %v", err)
	}
	if n != fi.Size() {
		t.Errorf("downloaded %d bytes, stat said %d", n, fi.Size())
	}
	if int64(buf.Len()) != fi.Size() {
		t.Errorf("buffered %d bytes, stat said %d", buf.Len(), fi.Size())
	}
}

// TestClientDownloadsALargeStreamedFile pushes a single file past the
// client's concurrent-request window, so the offsets in flight span more
// than one packet's worth.
func TestClientDownloadsALargeStreamedFile(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), 512*1024) // 8 MiB
	sb := newFakeSandbox(map[string][]byte{"big.bin": big})
	f := newTestFSWith(t, sb, false, fixedJobs{{Cluster: 5, Proc: 0}})
	c := dial(t, f)

	rf, err := c.Open("/outputs/5.0/data/big.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rf.Close() }()

	var buf bytes.Buffer
	if _, err := rf.WriteTo(&buf); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("WriteTo: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), big) {
		t.Errorf("downloaded %d bytes, want %d (equal=%v)",
			buf.Len(), len(big), bytes.Equal(buf.Bytes(), big))
	}
}

// TestStreamingWindowHasHeadroom pins the margin the default carries.
// A quarter of it must still serve a real client, so the default is not
// sitting on the edge of what pkg/sftp's own concurrency needs -- which
// is where a window sized by reasoning about the server rather than
// measuring the client ends up.
func TestStreamingWindowHasHeadroom(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), 512*1024) // 8 MiB
	sb := newFakeSandbox(map[string][]byte{"big.bin": big})
	f := newTestFSWith(t, sb, false, fixedJobs{{Cluster: 5, Proc: 0}})
	f.cfg.Window = sandboxfs.DefaultWindow / 4
	c := dial(t, f)

	rf, err := c.Open("/outputs/5.0/data/big.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rf.Close() }()
	var buf bytes.Buffer
	if _, err := rf.WriteTo(&buf); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("a quarter of DefaultWindow could not serve a real client: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), big) {
		t.Errorf("downloaded %d bytes, want %d", buf.Len(), len(big))
	}
}

func TestClientCannotWrite(t *testing.T) {
	f := newTestFS(t, true, fixedJobs{{Cluster: 12, Proc: 0}})
	c := dial(t, f)

	if _, err := c.Create("/outputs/12.0/data/new.txt"); err == nil {
		t.Error("Create succeeded on a read-only surface")
	}
	if err := c.Mkdir("/outputs/12.0/data/newdir"); err == nil {
		t.Error("Mkdir succeeded")
	}
	if err := c.Remove("/outputs/12.0/data/stdout"); err == nil {
		t.Error("Remove succeeded")
	}
	// And the surface still works afterwards.
	if _, err := c.ReadDir("/outputs/12.0/data"); err != nil {
		t.Errorf("ReadDir after refused writes: %v", err)
	}
}

func TestClientStatOfAMissingPath(t *testing.T) {
	f := newTestFS(t, true, fixedJobs{{Cluster: 12, Proc: 0}})
	c := dial(t, f)
	for _, p := range []string{"/nope", "/outputs/999.0", "/outputs/12.0/data/nope"} {
		if _, err := c.Stat(p); err == nil {
			t.Errorf("Stat(%q) succeeded", p)
		}
	}
}
