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
	"compress/gzip"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

// archiveFile is one entry to pack.
type archiveFile struct {
	Name string
	Body string
}

// tarBytes packs an archive the way `tar cf` would.
func tarBytes(t *testing.T, files ...archiveFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.Name, Mode: 0o600, Size: int64(len(f.Body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write([]byte(f.Body)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return out.Bytes()
}

// A submit description that transfers the script beside it, which is
// the shape the drop point exists for: work no template describes,
// carrying its own executable.
const ownSubmit = "executable = run.sh\n" +
	"arguments = hello\n" +
	"request_memory = 64\n" +
	"queue\n"

// drop uploads an archive the way a client does -- open, write, close
// -- and returns the status entry that came of it.
func drop(t *testing.T, f *triggerFS, name string, content []byte) uploadStatus {
	t.Helper()
	w, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: "/submit/" + name})
	if err != nil {
		t.Fatalf("opening /submit/%s: %v", name, err)
	}
	if _, err := w.WriteAt(content, 0); err != nil {
		t.Fatalf("writing /submit/%s: %v", name, err)
	}
	// The commit point. A close error is the refusal, which is also
	// recorded in status/ -- the test reads it there.
	if c, ok := w.(io.Closer); ok {
		_ = c.Close()
	} else {
		t.Fatal("the drop point's writer cannot be closed, so nothing would ever submit")
	}
	st, ok := f.statusFor(dropScope, name)
	if !ok {
		t.Fatalf("no status entry for /submit/%s", name)
	}
	return st
}

// TestDroppingAnArchiveSubmitsIt is the capability: a submit
// description and the files it names, in one archive, submitted as the
// account that dropped it.
func TestDroppingAnArchiveSubmitsIt(t *testing.T) {
	f := newTriggerFS(t)
	content := tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: ownSubmit},
		archiveFile{Name: "run.sh", Body: "#!/bin/sh\necho hi\n"})

	st := drop(t, f, "run.tar", content)
	if st.State != "submitted" {
		t.Fatalf("state = %q (%s), want submitted", st.State, st.Err)
	}
	if st.Cluster == 0 {
		t.Error("no cluster in the status entry")
	}

	call, ok := f.sub.lastArchive()
	if !ok {
		t.Fatal("nothing reached the submitter")
	}
	if call.Submit != ownSubmit {
		t.Errorf("submit description = %q, want the one in the archive", call.Submit)
	}
	// The whole archive goes to the spool, not just the submit file:
	// the executable is in it, and that is what a template cannot do.
	if len(call.Files) != 2 || call.Files[0] != SubmitFileName || call.Files[1] != "run.sh" {
		t.Errorf("files = %v, want both entries", call.Files)
	}
	if call.Size != int64(len(content)) {
		t.Errorf("source size = %d, want the archive's %d", call.Size, len(content))
	}
}

// TestAGzippedArchiveIsAccepted: `tar czf` is what people actually
// have, and the spool reads a plain tar -- so the gunzip happens here.
// Decided by the magic bytes, so a gzipped file named .tar still works.
func TestAGzippedArchiveIsAccepted(t *testing.T) {
	f := newTriggerFS(t)
	plain := tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: ownSubmit},
		archiveFile{Name: "run.sh", Body: "#!/bin/sh\n"})

	st := drop(t, f, "run.tar", gzipBytes(t, plain))
	if st.State != "submitted" {
		t.Fatalf("state = %q (%s), want submitted", st.State, st.Err)
	}
	call, _ := f.sub.lastArchive()
	if call.Submit != ownSubmit {
		t.Errorf("submit description = %q, want the one in the archive", call.Submit)
	}
	// What reaches the spool is the decompressed tar, because the
	// schedd does not gunzip.
	if call.Size != int64(len(plain)) {
		t.Errorf("source size = %d, want the decompressed %d", call.Size, len(plain))
	}
}

// TestAnArchiveWithNoSubmitFileIsRefused, and the refusal says what to
// do: with no job.sub there is nothing to submit, and the one error
// string a close reply carries is rendered by most clients as "could
// not close file".
func TestAnArchiveWithNoSubmitFileIsRefused(t *testing.T) {
	f := newTriggerFS(t)
	st := drop(t, f, "nothing.tar", tarBytes(t, archiveFile{Name: "run.sh", Body: "#!/bin/sh\n"}))

	if st.State != "refused" {
		t.Fatalf("state = %q, want refused", st.State)
	}
	if !strings.Contains(st.Err, SubmitFileName) {
		t.Errorf("error = %q, want it to name %s", st.Err, SubmitFileName)
	}
	if f.sub.archiveCount() != 0 {
		t.Error("an archive with nothing to submit reached the submitter")
	}
}

// TestASubmitFileInSubdirectoryIsNamed: `tar czf run.tar.gz mydir` is
// the mistake everybody makes once, and "no job.sub in it" is a
// baffling answer when the file is plainly there.
func TestASubmitFileInSubdirectoryIsNamed(t *testing.T) {
	f := newTriggerFS(t)
	st := drop(t, f, "nested.tar", tarBytes(t,
		archiveFile{Name: "mydir/" + SubmitFileName, Body: ownSubmit},
		archiveFile{Name: "mydir/run.sh", Body: "#!/bin/sh\n"}))

	if st.State != "refused" {
		t.Fatalf("state = %q, want refused", st.State)
	}
	for _, want := range []string{"mydir/" + SubmitFileName, "top level"} {
		if !strings.Contains(st.Err, want) {
			t.Errorf("error = %q, want it to mention %q", st.Err, want)
		}
	}
}

// TestAnArchiveMissingAnInputFileIsRefused: the tar spool drops a name
// it was not given, in silence, and the job leaves its hold and fails
// at run time on a missing file. That is a submission that looks
// successful at every step except the job's, so it is refused before
// anything is submitted.
func TestAnArchiveMissingAnInputFileIsRefused(t *testing.T) {
	f := newTriggerFS(t)
	st := drop(t, f, "incomplete.tar", tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: "executable = /bin/sh\n" +
			"transfer_executable = false\n" +
			"transfer_input_files = data.csv, helper.py\n" +
			"queue\n"},
		archiveFile{Name: "data.csv", Body: "a,b\n"}))

	if st.State != "refused" {
		t.Fatalf("state = %q, want refused", st.State)
	}
	if !strings.Contains(st.Err, "helper.py") {
		t.Errorf("error = %q, want it to name the missing file", st.Err)
	}
	if strings.Contains(st.Err, "data.csv, helper.py") {
		t.Errorf("error = %q, want only the missing name reported as missing", st.Err)
	}
	if f.sub.archiveCount() != 0 {
		t.Error("an archive missing an input file reached the submitter")
	}
}

// TestAURLInputNeedsNoFileInTheArchive: the execute node fetches it,
// which is how a dropped archive stays small while the job's input does
// not.
func TestAURLInputNeedsNoFileInTheArchive(t *testing.T) {
	f := newTriggerFS(t)
	st := drop(t, f, "urls.tar", tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: "executable = /bin/sh\n" +
			"transfer_executable = false\n" +
			"transfer_input_files = https://example.com/big.tgz\n" +
			"queue\n"}))

	if st.State != "submitted" {
		t.Fatalf("state = %q (%s), want submitted", st.State, st.Err)
	}
}

// TestAMalformedSubmitFileIsRefusedHere rather than at the schedd: the
// parser's own message names the line, and getting it without a round
// trip is the difference between a readable refusal and a schedd error.
func TestAMalformedSubmitFileIsRefusedHere(t *testing.T) {
	f := newTriggerFS(t)
	st := drop(t, f, "bad.tar", tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: "executable = /bin/true\nqueue 1 2 3 4\n"}))

	if st.State != "refused" {
		t.Fatalf("state = %q, want refused", st.State)
	}
	if !strings.Contains(st.Err, "does not parse") {
		t.Errorf("error = %q, want it to say the description does not parse", st.Err)
	}
	if f.sub.archiveCount() != 0 {
		t.Error("a submit description that does not parse reached the submitter")
	}
}

// TestHostAccessIsRefusedInADroppedSubmitFile: the submit description
// arrived over the network and is parsed with no access to this host,
// so `include command` -- which runs a shell command -- cannot run as
// the daemon. Pinned here because this surface is where somebody else's
// text reaches that parser.
func TestHostAccessIsRefusedInADroppedSubmitFile(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"include a file", "executable = /bin/true\ninclude : /etc/hosts\nqueue\n"},
		{"run a command", "executable = /bin/true\ninclude command : /bin/echo x\nqueue\n"},
		{"read an item file", "executable = /bin/true\nqueue s from /etc/hosts\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTriggerFS(t)
			st := drop(t, f, "host.tar", tarBytes(t, archiveFile{Name: SubmitFileName, Body: tc.body}))
			if st.State != "refused" {
				t.Fatalf("state = %q, want refused", st.State)
			}
			if !strings.Contains(st.Err, "local host") {
				t.Errorf("error = %q, want the parser's reason", st.Err)
			}
			if f.sub.archiveCount() != 0 {
				t.Error("it reached the submitter")
			}
		})
	}
}

// TestTheSameArchiveTwiceIsOneSubmission: a client that reconnects, or
// somebody who runs the same command again, must not double the jobs.
func TestTheSameArchiveTwiceIsOneSubmission(t *testing.T) {
	f := newTriggerFS(t)
	content := tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: ownSubmit},
		archiveFile{Name: "run.sh", Body: "#!/bin/sh\n"})

	first := drop(t, f, "run.tar", content)
	if first.State != "submitted" {
		t.Fatalf("state = %q (%s), want submitted", first.State, first.Err)
	}
	// Under another name, so it is the bytes that are recognised.
	second := drop(t, f, "again.tar", content)
	if second.State != "duplicate" {
		t.Fatalf("state = %q, want duplicate", second.State)
	}
	if second.Cluster != first.Cluster {
		t.Errorf("duplicate names cluster %d, want the first submission's %d",
			second.Cluster, first.Cluster)
	}
	if f.sub.archiveCount() != 1 {
		t.Errorf("the submitter was called %d times for one archive", f.sub.archiveCount())
	}
}

// TestAnInterruptedUploadSubmitsNothing: SFTP never says how long a
// file will be, so a clean close is the only thing that distinguishes a
// client that finished from one that vanished.
func TestAnInterruptedUploadSubmitsNothing(t *testing.T) {
	f := newTriggerFS(t)
	w, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: "/submit/half.tar"})
	if err != nil {
		t.Fatalf("Filewrite: %v", err)
	}
	content := tarBytes(t, archiveFile{Name: SubmitFileName, Body: ownSubmit})
	if _, err := w.WriteAt(content[:len(content)/2], 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	// What the request server does when the connection drops: the
	// error first, then the close.
	w.(interface{ TransferError(error) }).TransferError(io.ErrUnexpectedEOF)
	_ = w.(io.Closer).Close()

	st, ok := f.statusFor(dropScope, "half.tar")
	if !ok {
		t.Fatal("no status entry for an interrupted upload")
	}
	if st.State != "incomplete" {
		t.Errorf("state = %q, want incomplete", st.State)
	}
	if f.sub.archiveCount() != 0 {
		t.Error("an interrupted upload was submitted")
	}
}

// TestAnOversizeArchiveIsRefusedWhileItUploads, not after: a client
// sending something enormous should be stopped while it is still
// sending.
func TestAnOversizeArchiveIsRefusedWhileItUploads(t *testing.T) {
	f := newTriggerFS(t)
	f.cfg.MaxArchiveBytes = 1024

	w, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: "/submit/big.tar"})
	if err != nil {
		t.Fatalf("Filewrite: %v", err)
	}
	if _, err := w.WriteAt(make([]byte, 2048), 0); err == nil {
		t.Fatal("a write past the limit was accepted")
	}
	_ = w.(io.Closer).Close()

	st, _ := f.statusFor(dropScope, "big.tar")
	if st.State != "incomplete" {
		t.Errorf("state = %q, want incomplete", st.State)
	}
	if f.sub.archiveCount() != 0 {
		t.Error("an oversize archive was submitted")
	}
}

// TestTheDropPointIsWriteOnly: what was dropped became jobs and was not
// kept, so there is nothing to list or read back except the outcome.
func TestTheDropPointIsWriteOnly(t *testing.T) {
	f := newTriggerFS(t)
	content := tarBytes(t, archiveFile{Name: SubmitFileName, Body: ownSubmit},
		archiveFile{Name: "run.sh", Body: "#!/bin/sh\n"})
	drop(t, f, "run.tar", content)

	// /submit lists its README and status/, and nothing that was
	// uploaded.
	got := listNames(t, f.FS, "/submit")
	want := []string{"README", "status/"}
	if len(got) != len(want) {
		t.Fatalf("/submit = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("/submit = %v, want %v", got, want)
		}
	}
	if names := listNames(t, f.FS, "/submit/status"); len(names) != 1 || names[0] != "run.tar" {
		t.Errorf("/submit/status = %v, want [run.tar]", names)
	}
	// The outcome is readable under the name that was dropped.
	text := readStatus(t, f.FS, "/submit/status/run.tar")
	if !strings.Contains(text, "state: submitted") {
		t.Errorf("status = %q", text)
	}
	// The README is readable, because it is the only place the contract
	// is written down.
	readme := readStatus(t, f.FS, "/submit/README")
	for _, want := range []string{SubmitFileName, "top level", "tar czf"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README does not mention %q:\n%s", want, readme)
		}
	}
}

// TestSpillingToDiskHoldsNothingInMemory: with a buffer directory the
// archive goes to a file, which is both a natural WriterAt for an
// out-of-order client and what lets the limit be the operator's spool
// ceiling rather than what is safe to keep resident.
func TestSpillingToDiskHoldsNothingInMemory(t *testing.T) {
	f := newTriggerFS(t)
	f.cfg.SpoolBufferDir = t.TempDir()
	f.cfg.MaxArchiveBytes = 1 << 20

	content := tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: ownSubmit},
		archiveFile{Name: "run.sh", Body: "#!/bin/sh\n"})

	// Written in two pieces, second half first, which is what a
	// pipelined client's concurrent writes look like.
	w, err := f.Filewrite(&sftp.Request{Method: "Put", Filepath: "/submit/run.tar"})
	if err != nil {
		t.Fatalf("Filewrite: %v", err)
	}
	half := len(content) / 2
	if _, err := w.WriteAt(content[half:], int64(half)); err != nil {
		t.Fatalf("WriteAt (second half first): %v", err)
	}
	if _, err := w.WriteAt(content[:half], 0); err != nil {
		t.Fatalf("WriteAt (first half): %v", err)
	}
	_ = w.(io.Closer).Close()

	st, _ := f.statusFor(dropScope, "run.tar")
	if st.State != "submitted" {
		t.Fatalf("state = %q (%s), want submitted", st.State, st.Err)
	}
	call, _ := f.sub.lastArchive()
	if call.Size != int64(len(content)) {
		t.Errorf("source size = %d, want %d: the pieces were not reassembled", call.Size, len(content))
	}

	// And the buffer file is gone: one per upload would otherwise fill
	// the directory and then the disk.
	left, err := os.ReadDir(f.cfg.SpoolBufferDir)
	if err != nil {
		t.Fatalf("listing the buffer directory: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("the buffer directory still holds %d files", len(left))
	}
}

// TestAGzippedArchiveSpilledToDisk is the case the in-memory gzip test
// cannot reach: decompressing a spilled archive reads the buffer file
// while the plain tar is being written, so the two cannot be the same
// file.
func TestAGzippedArchiveSpilledToDisk(t *testing.T) {
	f := newTriggerFS(t)
	f.cfg.SpoolBufferDir = t.TempDir()
	f.cfg.MaxArchiveBytes = 1 << 20

	// Large enough that the decompressor cannot hold the whole
	// compressed stream in its own buffer: a small archive is read out
	// of gzip's 4 KiB window before the first byte is written back, so
	// it would pass even if the two were the same file.
	bulk := strings.Repeat("payload line to compress\n", 20000)
	plain := tarBytes(t,
		archiveFile{Name: SubmitFileName, Body: ownSubmit},
		archiveFile{Name: "run.sh", Body: "#!/bin/sh\necho hi\n"},
		archiveFile{Name: "data.txt", Body: bulk})

	st := drop(t, f, "run.tar.gz", gzipBytes(t, plain))
	if st.State != "submitted" {
		t.Fatalf("state = %q (%s), want submitted", st.State, st.Err)
	}
	call, ok := f.sub.lastArchive()
	if !ok {
		t.Fatal("nothing reached the submitter")
	}
	if call.Size != int64(len(plain)) {
		t.Errorf("source size = %d, want the decompressed %d", call.Size, len(plain))
	}
	if call.Submit != ownSubmit {
		t.Errorf("submit description = %q, want the one in the archive", call.Submit)
	}
}
