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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PelicanPlatform/classad/classad"

	htcondor "github.com/htcondor/golang-htcondor"
)

// A tarball drop is a submission nobody wrote a template for.
//
// The templates tree answers "run this known work over my data". This
// answers the other half: somebody who has a submit file and the files
// it names, and wants them run. They put both in one archive, drop it
// here, and what happens to it appears under status/ -- the same
// contract as a CSV, because it is the same thing from the client's
// side: one file in, one answer back.
//
// The archive is the unit, and that is the design. A submit description
// is useless without the files it names, so uploading them separately
// would mean holding a half-submission somewhere and guessing when it
// was complete. One archive closing cleanly is unambiguous.

// SubmitFileName doubles as the name a dropped archive must carry: the
// same job.sub a template serves, so what you read out of a template
// directory is what you put into an archive.

// MaxSubmitFileBytes caps the job.sub inside an archive. A submit
// description is a page of text; a megabyte of it is a mistake or an
// attack, and either way reading it all to find that out is the thing
// to avoid.
const MaxSubmitFileBytes = 1 << 20

// dropScope keys a tarball drop's status entry in the status map it
// shares with the templates tree.
//
// Not a template id, and cannot collide with one: the library's ids
// match ^[a-z0-9][a-z0-9-]{0,63}$, so nothing with a '+' is reachable
// as a template.
const dropScope = "+submit"

// TarballSubmission is one dropped archive, ready to submit.
type TarballSubmission struct {
	// Submit is the job.sub found in the archive, verbatim.
	Submit string
	// Files are every entry in the archive, cleaned, so a caller can
	// tell which of the names the jobs ask for are actually in it.
	Files []string
	// Source is the archive, readable once per proc.
	Source TarballSource
}

// TarballSource is the archive as the spool consumes it: the same bytes
// as many times as the cluster has procs. webapi/spool.Source is
// satisfied by this, which is what the submitter passes to a fan-out.
//
// Valid only for the duration of the SubmitTarball call. What backs it
// is a buffer file this service deletes on the way out, so a submitter
// that kept it would be holding a path that no longer exists.
type TarballSource interface {
	Reader() (io.ReadCloser, error)
	Size() int64
	Close() error
}

// dropEnabled reports whether this view has a tarball drop point.
//
// A submitter is all it takes. Unlike the templates tree there is no
// library to consult -- the submission describes itself -- so the drop
// point is there whenever this service can submit at all.
func (f *FS) dropEnabled() bool { return f.cfg.Submitter != nil }

// dropReadmeText explains the one thing somebody has to get right.
func (f *FS) dropReadmeText() string {
	var b strings.Builder
	b.WriteString("Submit a job of your own\n")
	b.WriteString("========================\n\n")
	fmt.Fprintf(&b, "Put a submit description named %s, and every file it needs, in one\n", SubmitFileName)
	b.WriteString("archive, and drop the archive here:\n\n")
	fmt.Fprintf(&b, "  tar czf run.tar.gz %s run.sh data.csv\n", SubmitFileName)
	b.WriteString("  sftp> put run.tar.gz /submit/\n")
	fmt.Fprintf(&b, "  sftp> get /%s/%s/run.tar.gz\n\n", submitDir, statusDirName)
	fmt.Fprintf(&b, "%s must be at the top level of the archive. Plain tar and gzipped\n", SubmitFileName)
	b.WriteString("tar are both accepted; the name does not matter.\n\n")
	b.WriteString("Every file your submit description transfers has to be in the archive,\n")
	b.WriteString("under the name it asks for -- including the executable, unless it is\n")
	b.WriteString("already on the execute node. A name the jobs ask for and the archive\n")
	b.WriteString("does not carry is refused rather than submitted, because the jobs would\n")
	b.WriteString("otherwise run without the file. Anything the submit description does not\n")
	b.WriteString("ask for is ignored, so packing a whole directory is fine.\n\n")
	b.WriteString("A URL in transfer_input_files is fetched by the execute node and does\n")
	b.WriteString("not belong in the archive.\n\n")
	fmt.Fprintf(&b, "The archive may be up to %d bytes.\n", f.dropLimit())
	b.WriteString("Your submit description is parsed without access to this host, so\n")
	b.WriteString("include, include command, $ENV() and `queue ... from <file>` do not\n")
	b.WriteString("work. Use the inline form: queue var from ((row\\nrow)).\n\n")
	b.WriteString("The submission happens when your client closes the file. An interrupted\n")
	b.WriteString("transfer submits nothing, and the same archive dropped twice is one\n")
	b.WriteString("submission.\n\n")
	fmt.Fprintf(&b, "Output comes back under /%s once the jobs finish.\n", outputsDir)
	return b.String()
}

// dropLimit is how large an archive this service accepts.
func (f *FS) dropLimit() int64 {
	if f.cfg.MaxArchiveBytes > 0 {
		return f.cfg.MaxArchiveBytes
	}
	// Without a buffer directory the archive is held in memory, so the
	// ceiling is the one the service already applies to an upload
	// rather than a new and much larger number.
	return f.cfg.Limits.withDefaults().MaxBytes
}

// listDrop answers a listing under /submit.
func (f *FS) listDrop(r ref, p string) (listerAt, error) {
	if !f.dropEnabled() {
		return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}
	switch r.kind {
	case kindDrop:
		// Write-only, like a template's submit/: what was dropped
		// became jobs and was not kept, and what happened to it is in
		// status/.
		return listerAt{
			fileInfo{name: ReadmeFileName, size: int64(len(f.dropReadmeText())), mode: 0o444},
			dirInfo(statusDirName, time.Time{}),
		}, nil

	case kindDropStatusDir:
		names := f.statusNames(dropScope)
		sort.Strings(names)
		out := make(listerAt, 0, len(names))
		for _, name := range names {
			st, ok := f.statusFor(dropScope, name)
			if !ok {
				continue
			}
			text := st.Text()
			out = append(out, fileInfo{name: name, size: int64(len(text)), mode: 0o444, mod: st.At})
		}
		return out, nil

	case kindDropReadme, kindDropFile, kindDropStatusFile:
		return nil, &os.PathError{Op: "readdir", Path: p, Err: syscall.ENOTDIR}
	}
	return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
}

// statDrop answers a stat under /submit.
func (f *FS) statDrop(r ref, p string) (listerAt, error) {
	if !f.dropEnabled() {
		return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}
	switch r.kind {
	case kindDrop:
		return listerAt{dirInfo(submitDir, time.Time{})}, nil
	case kindDropStatusDir:
		return listerAt{dirInfo(statusDirName, time.Time{})}, nil
	case kindDropReadme:
		return listerAt{fileInfo{
			name: ReadmeFileName, size: int64(len(f.dropReadmeText())), mode: 0o444,
		}}, nil
	case kindDropFile:
		// Nothing is kept, so there is nothing to stat; a client that
		// stats before writing gets what it would for any new file.
		return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}
	case kindDropStatusFile:
		st, ok := f.statusFor(dropScope, r.name)
		if !ok {
			return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}
		}
		text := st.Text()
		return listerAt{fileInfo{
			name: r.name, size: int64(len(text)), mode: 0o444, mod: st.At,
		}}, nil
	}
	return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
}

// readDrop answers a read under /submit.
func (f *FS) readDrop(r ref, p string) (io.ReaderAt, error) {
	if !f.dropEnabled() {
		return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}
	switch r.kind {
	case kindDropReadme:
		return bytes.NewReader([]byte(f.dropReadmeText())), nil
	case kindDropStatusFile:
		st, ok := f.statusFor(dropScope, r.name)
		if !ok {
			return nil, &os.PathError{Op: "open", Path: p, Err: syscall.ENOENT}
		}
		return bytes.NewReader([]byte(st.Text())), nil
	case kindDropFile:
		// Write-only: the archive became jobs and was not kept.
		return nil, &os.PathError{Op: "open", Path: p, Err: syscall.ENOENT}
	}
	return nil, &os.PathError{Op: "open", Path: p, Err: syscall.EISDIR}
}

// writeDrop accepts an archive.
func (f *FS) writeDrop(r ref, p string) (io.WriterAt, error) {
	if !f.dropEnabled() {
		return nil, &os.PathError{Op: "write", Path: p, Err: syscall.EROFS}
	}
	sink, err := newTarSink(f.cfg.SpoolBufferDir, f.dropLimit())
	if err != nil {
		return nil, err
	}
	return &archiveUpload{fs: f, name: r.name, sink: sink}, nil
}

// archiveUpload is one dropped archive in flight.
//
// The same commit rule as a dropped CSV, for the same reason: the
// request server calls TransferError before Close when a transfer did
// not finish, so a clean Close -- and only a clean Close -- means the
// bytes are all here. See the upload type for the full argument.
type archiveUpload struct {
	fs   *FS
	name string

	// mu guards the sink as well as the fields below: pkg/sftp calls
	// WriteAt from eight workers at once, and the sink keeps the
	// archive's length and (in memory) grows a slice. Its own lock
	// rather than the FS's, which somebody reading status/ holds.
	mu     sync.Mutex
	sink   *tarSink
	failed error
	closed bool
}

// WriteAt implements io.WriterAt.
func (u *archiveUpload) WriteAt(p []byte, off int64) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return 0, errors.New("sftpjobs: write after close")
	}
	n, err := u.sink.WriteAt(p, off)
	if err != nil {
		// Recorded here as well as returned, so the close that follows
		// does not submit a partial archive.
		u.failed = err
	}
	return n, err
}

// TransferError implements sftp.TransferError.
func (u *archiveUpload) TransferError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failed == nil {
		u.failed = err
	}
}

// Close is the commit point.
func (u *archiveUpload) Close() error {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return nil
	}
	u.closed = true
	failed := u.failed
	u.mu.Unlock()

	if failed != nil {
		_ = u.sink.Close()
		u.fs.putStatus(dropScope, u.name, uploadStatus{
			State: "incomplete",
			Err:   fmt.Sprintf("the upload did not finish: %v", failed),
		})
		return nil
	}
	return u.fs.acceptTarball(u.name, u.sink)
}

// acceptTarball turns a finished archive into a submission.
//
// Every outcome is written to status/, including every refusal: the
// close reply carries one error string, which a GUI client renders as
// "could not close file" and nothing more.
func (f *FS) acceptTarball(name string, upload *tarSink) error {
	defer func() {
		// The archive is the submitter's to read while it spools and
		// nobody's afterwards.
		_ = upload.Close()
	}()

	fail := func(err error) error {
		f.putStatus(dropScope, name, uploadStatus{
			State: "refused", Err: err.Error(), At: time.Now(),
		})
		return err
	}

	if f.cfg.Submitter == nil {
		return fail(errors.New("this access point does not accept submissions here"))
	}

	// Decompressed first, so everything after this reads a plain tar:
	// the spool does not gunzip, and the limit has to apply to what
	// will be spooled rather than to what arrived.
	sink, err := decompressed(upload, f.cfg.SpoolBufferDir)
	if err != nil {
		return fail(err)
	}
	if sink != upload {
		defer func() { _ = sink.Close() }()
	}

	digest, err := sink.digest()
	if err != nil {
		return fail(err)
	}
	if cluster, ok := f.seenUpload(dropScope, digest); ok {
		f.putStatus(dropScope, name, uploadStatus{
			State: "duplicate", Cluster: cluster, Digest: digest, At: time.Now(),
			Err: fmt.Sprintf("these exact bytes were already submitted as cluster %d", cluster),
		})
		return nil
	}

	sub, err := readTarball(sink)
	if err != nil {
		return fail(err)
	}

	// Parsed before anything is submitted, for two answers a schedd
	// round trip would only delay: whether the description is usable at
	// all, and whether the files it names are in the archive.
	if err := checkArchiveCovers(sub); err != nil {
		return fail(err)
	}

	cluster, err := f.cfg.Submitter.SubmitTarball(f.cfg.Context, f.cfg.Account, sub)
	if err != nil {
		return fail(err)
	}

	f.rememberUpload(dropScope, digest, cluster)
	f.putStatus(dropScope, name, uploadStatus{
		State: "submitted", Cluster: cluster, Digest: digest, At: time.Now(),
		Files: len(sub.Files),
	})
	f.logf("sftpjobs: %s submitted cluster %d from %s/%s", f.cfg.Account, cluster, submitDir, name)
	return nil
}

// gzipMagic is the first two bytes of a gzip stream.
var gzipMagic = []byte{0x1f, 0x8b}

// decompressed returns the archive as the plain tar the spool reads:
// the sink itself when it already is one, or a NEW sink holding the
// inflated bytes.
//
// A new one, and not the same sink refilled, because the decompressor
// reads the sink while the result is written. Refilling it worked for a
// small archive and nothing else: gzip reads ahead into a 4 KiB buffer,
// so anything that fit was already in memory before the first byte went
// back, while a larger one had the file truncated out from under the
// reader and came back "gzip: invalid header" -- a refusal naming
// nothing the client had done wrong.
//
// Gzip is decided by the first two bytes rather than by the file name,
// because the name is the client's and says nothing reliable: somebody
// who writes `put run.tar` over a gzipped file should still have it
// work.
func decompressed(sink *tarSink, dir string) (*tarSink, error) {
	head, err := sinkHead(sink, len(gzipMagic))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(head, gzipMagic) {
		return sink, nil
	}

	r, err := sink.Reader()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("the archive looks gzipped but does not decompress: %w", err)
	}
	defer func() { _ = gz.Close() }()

	// The same ceiling as the upload: a small gzip can expand to an
	// arbitrarily large tar, and what the limit has to bound is what
	// will be spooled.
	out, err := newTarSink(dir, sink.max)
	if err != nil {
		return nil, err
	}
	if err := out.Fill(gz); err != nil {
		_ = out.Close()
		return nil, err
	}
	return out, nil
}

// sinkHead reads the first n bytes, or fewer if that is all there is.
func sinkHead(sink *tarSink, n int) ([]byte, error) {
	r, err := sink.Reader()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	head := make([]byte, n)
	read, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return head[:read], nil
}

// readTarball finds the submit description and lists what else is here.
func readTarball(src TarballSource) (*TarballSubmission, error) {
	r, err := src.Reader()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()

	sub := &TarballSubmission{Source: src}
	var nested []string
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the archive does not read as a tar: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		// Cleaned, because this is matched against the names the jobs
		// ask for: "./run.sh" and "run.sh" are the same file, and the
		// schedd refuses a name that is not canonical anyway.
		clean := path.Clean(header.Name)
		if clean == "." || strings.HasPrefix(clean, "..") {
			continue
		}
		sub.Files = append(sub.Files, clean)

		if clean == SubmitFileName {
			if header.Size > MaxSubmitFileBytes {
				return nil, fmt.Errorf("%s is %d bytes, over the %d byte limit",
					SubmitFileName, header.Size, int64(MaxSubmitFileBytes))
			}
			text, err := io.ReadAll(io.LimitReader(tr, MaxSubmitFileBytes))
			if err != nil {
				return nil, fmt.Errorf("reading %s out of the archive: %w", SubmitFileName, err)
			}
			sub.Submit = string(text)
			continue
		}
		if path.Base(clean) == SubmitFileName {
			nested = append(nested, clean)
		}
	}

	if strings.TrimSpace(sub.Submit) == "" {
		if len(nested) > 0 {
			return nil, fmt.Errorf("the archive has %s at %s, but it has to be at the top level: "+
				"pack the files themselves, not the directory holding them",
				SubmitFileName, strings.Join(nested, ", "))
		}
		if sub.Submit != "" {
			return nil, fmt.Errorf("%s in the archive is empty", SubmitFileName)
		}
		return nil, fmt.Errorf("the archive has no %s in it, so there is nothing to submit",
			SubmitFileName)
	}
	sort.Strings(sub.Files)
	return sub, nil
}

// checkArchiveCovers refuses a submission whose files are not all here.
//
// The cost of not checking is specific to the tar spool: a name the job
// asks for that the archive does not carry is dropped in silence, the
// job leaves its spooling hold, and it fails at run time on a missing
// file -- so the submission looks like a success from every angle
// except the job's. (The filesystem spool stats its names and fails
// loudly instead; this one cannot, because a tar is a stream.)
//
// Asked here with no row values, which answers for a literal transfer
// list before a cluster exists. A list a queue statement's items fill
// in is answered again after the submission, where the values are real
// -- see ArchiveMissing.
func checkArchiveCovers(sub *TarballSubmission) error {
	sf, err := htcondor.ParseSubmitFile(strings.NewReader(sub.Submit))
	if err != nil {
		return fmt.Errorf("%s does not parse: %w", SubmitFileName, err)
	}
	ad, err := sf.MakeJobAd(htcondor.JobID{Cluster: 1, Proc: 0}, nil)
	if err != nil {
		return fmt.Errorf("%s does not describe a job: %w", SubmitFileName, err)
	}
	if missing := missingFromArchive(htcondor.SpoolInputAllowSet(ad), sub.Files); len(missing) > 0 {
		return archiveMissingError(missing, sub.Files)
	}
	return nil
}

// ArchiveMissing reports names the submitted jobs ask for that the
// archive does not carry.
//
// The submitter's half of checkArchiveCovers: procAds are the jobs the
// schedd made, so every queue item has been substituted and the
// allow-set is exact. An error here is a cluster that must come back
// out of the queue.
func ArchiveMissing(procAds []*classad.ClassAd, files []string) error {
	seen := map[string]bool{}
	var allow []string
	for _, ad := range procAds {
		for _, name := range htcondor.SpoolInputAllowSet(ad) {
			if !seen[name] {
				seen[name] = true
				allow = append(allow, name)
			}
		}
	}
	sort.Strings(allow)
	if missing := missingFromArchive(allow, files); len(missing) > 0 {
		return archiveMissingError(missing, files)
	}
	return nil
}

// missingFromArchive is the names in allow that files does not carry.
func missingFromArchive(allow, files []string) []string {
	have := make(map[string]bool, len(files))
	for _, f := range files {
		have[f] = true
	}
	var missing []string
	for _, name := range allow {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func archiveMissingError(missing, files []string) error {
	what := "the archive holds " + strings.Join(files, ", ")
	if len(files) == 0 {
		what = "the archive holds nothing else"
	}
	return fmt.Errorf("%s asks for %s, which the archive does not carry (%s): "+
		"add the files, or name a URL the execute node can fetch",
		SubmitFileName, strings.Join(missing, ", "), what)
}
