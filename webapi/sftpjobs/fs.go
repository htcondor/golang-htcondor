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
	"context"
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

	"github.com/pkg/sftp"

	"github.com/htcondor/golang-htcondor/webapi/sandboxfs"
)

// JobRef is one job a caller may read.
type JobRef struct {
	Cluster, Proc int
	// Completed is shown as the job directory's modification time, and
	// orders the listing. A zero value is fine.
	Completed time.Time
}

// JobLister reports which jobs a caller may read.
//
// A hook, because "which jobs" is not one question: a finished job stays
// in the queue for days and then moves to history, and neither source
// alone is the answer. Whoever wires this up knows which of the two --
// or the mirror -- to ask.
type JobLister interface {
	ReadableJobs(ctx context.Context) ([]JobRef, error)
}

// Config describes one account's view.
type Config struct {
	// Store reads sandboxes.
	Store *sandboxfs.Store

	// Account is whose view this is. Every path resolves against it, so
	// nothing a client sends can reach another account's job.
	Account string

	// Context carries the account's credential to the Store.
	//
	// Taken here rather than from each sftp.Request because the request
	// server builds those from a context.Background of its own, which
	// carries no identity. This one is the channel's: it is cancelled
	// when the channel goes away, which is what ends an abandoned
	// transfer.
	Context context.Context

	// Jobs lists the account's readable jobs. Optional: without it
	// /outputs lists nothing, and a job is still reachable by naming it.
	// Addressing a job by id is the contract here; the listing is a
	// convenience.
	Jobs JobLister

	// JobAds reads one job's ad, which is what job.ad serves. Optional
	// in the same way: without it a job directory holds its output and
	// no ad.
	JobAds JobAdReader

	// Templates and Submitter enable the templates tree: dropping a CSV
	// into a template's submit/ directory submits one job per row. With
	// either unset the tree is absent, and the file service serves job
	// output only.
	//
	// Templates is the shared library -- the same templates the web
	// submit page and /api/v1/templates offer -- so *templates.Library
	// goes here.
	Templates TemplateSource
	Submitter Submitter

	// Limits bound a dropped CSV. The zero value takes the package
	// defaults.
	//
	// Per-service rather than per-template: a template says what its
	// columns are, and how large a file this access point will accept is
	// the operator's business, not the template author's.
	Limits CSVLimits

	// SpoolBufferDir is where a dropped archive is buffered, and is the
	// operator's HTTP_API_SPOOL_BUFFER_DIR. Empty holds it in memory
	// instead, which is why it also decides the ceiling: see
	// MaxArchiveBytes.
	SpoolBufferDir string

	// MaxArchiveBytes is the largest archive /submit accepts. Zero
	// takes Limits.MaxBytes -- the limit this service already applies
	// to an upload -- which is the right ceiling for an archive held in
	// memory and a small one for a buffer directory that could take a
	// gigabyte.
	MaxArchiveBytes int64

	// Window is the retention for a streamed read. Zero takes
	// sandboxfs.DefaultWindow.
	Window int

	// Uploads records what happened to each dropped file and which
	// digests were submitted recently. Shared across every connection
	// when the caller supplies one, which is the point of it: see
	// UploadJournal. Nil gives this view a private journal, which is
	// right for a library caller serving one session and wrong for a
	// server.
	Uploads *UploadJournal

	// Logf, if set, receives debug logging.
	Logf func(format string, args ...any)

	// Warnf, if set, receives the handful of events an operator needs
	// to see: a job listing this service could not build, and an upload
	// it refused.
	//
	// Separate from Logf because one level cannot serve both. Logf
	// traces every listing and every stat, so a server that wants it at
	// all wants it at debug -- and that is where the reason a user's
	// upload was refused used to go too, which is the same as nowhere.
	// Unset sends these to Logf instead, so a caller with one hook
	// still gets them.
	Warnf func(format string, args ...any)
}

// FS serves one account's jobs.
//
// One per channel, so the account is fixed and nothing a client sends
// can reach another's. What an upload became outlives the channel --
// that lives in the journal, which the server shares across connections
// -- while the ad cache does not, because an ad is cheap to fetch again
// and a stale one would misreport a job.
type FS struct {
	cfg Config
	// journal is cfg.Uploads, or a private one when the caller gave
	// none. Never nil after New.
	journal *UploadJournal

	mu sync.Mutex
	// adCache holds rendered job ads for the channel's lifetime; see
	// jobAdText.
	adCache map[[2]int]string
	// notice is the rendered /outputs notice, held so the size a client
	// stats matches the bytes it goes on to read; see noticeFor.
	notice string
}

// New returns an FS for one authenticated account.
func New(cfg Config) (*FS, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("sftpjobs: no sandbox store")
	case cfg.Account == "":
		return nil, errors.New("sftpjobs: no account")
	case cfg.Context == nil:
		return nil, errors.New("sftpjobs: no context")
	}
	journal := cfg.Uploads
	if journal == nil {
		journal = NewUploadJournal()
	}
	return &FS{cfg: cfg, journal: journal}, nil
}

// Handlers is the request-server handler set.
//
// Every slot is filled, including the ones that refuse: an unset slot
// makes the request server answer "unexpected request" for a whole
// class of operation, which tells a user nothing about why their upload
// did not work.
func (f *FS) Handlers() sftp.Handlers {
	return sftp.Handlers{FileGet: f, FilePut: f, FileCmd: f, FileList: f}
}

func (f *FS) logf(format string, args ...any) {
	if f.cfg.Logf != nil {
		f.cfg.Logf(format, args...)
	}
}

// warnf reports something an operator should see. It falls back to
// Logf, so a caller that wired only one hook still gets it.
func (f *FS) warnf(format string, args ...any) {
	if f.cfg.Warnf != nil {
		f.cfg.Warnf(format, args...)
		return
	}
	f.logf(format, args...)
}

// job is the store's handle for a reference, bound to this account.
func (f *FS) job(r ref) sandboxfs.Job {
	return sandboxfs.Job{Account: f.cfg.Account, Cluster: r.cluster, Proc: r.proc}
}

// Fileread implements sftp.FileReader.
func (f *FS) Fileread(req *sftp.Request) (io.ReaderAt, error) {
	r, err := parsePath(req.Filepath)
	if err != nil {
		return nil, err
	}
	ctx := f.cfg.Context

	switch r.kind {
	case kindJobAd:
		text, err := f.jobAdText(ctx, r)
		if err != nil {
			f.logf("sftpjobs: reading the ad for %s: %v", jobDirName(r.cluster, r.proc), err)
			return nil, &os.PathError{Op: "open", Path: req.Filepath, Err: syscall.ENOENT}
		}
		return bytes.NewReader([]byte(text)), nil

	case kindJobFile:
		rc, err := f.cfg.Store.Open(ctx, f.job(r), r.name)
		if err != nil {
			return nil, err
		}
		// A cache hit is an *os.File and already does random access;
		// only a streamed read needs the window, and wrapping a file in
		// it would turn a seekable read into a forward-only one.
		if ra, ok := rc.(io.ReaderAt); ok {
			return ra, nil
		}
		return sandboxfs.NewSequentialReaderAt(rc, f.cfg.Window), nil

	case kindJobTar:
		rc, _, err := f.cfg.Store.OpenTar(ctx, f.job(r))
		if err != nil {
			return nil, err
		}
		return sandboxfs.NewSequentialReaderAt(rc, f.cfg.Window), nil

	case kindOutputsNotice:
		_, err := f.readableJobs(ctx)
		if err == nil {
			// Gone as soon as the listing works, so a client that
			// fetched it once and kept the name is told it is not there
			// rather than handed a stale complaint.
			return nil, &os.PathError{Op: "open", Path: req.Filepath, Err: syscall.ENOENT}
		}
		return bytes.NewReader([]byte(f.noticeFor(err))), nil

	case kindRoot, kindOutputs, kindJobDir, kindJobData:
		return nil, &os.PathError{Op: "open", Path: req.Filepath, Err: syscall.EISDIR}
	}
	if r.kind.inDrop() {
		return f.readDrop(r, req.Filepath)
	}
	if r.kind.inTemplates() {
		return f.readTemplates(r, req.Filepath)
	}
	return nil, fmt.Errorf("%q: %w", req.Filepath, fs.ErrNotExist)
}

// Filelist implements sftp.FileLister, for both a directory listing and
// a single stat.
func (f *FS) Filelist(req *sftp.Request) (sftp.ListerAt, error) {
	r, err := parsePath(req.Filepath)
	if err != nil {
		return nil, err
	}
	switch req.Method {
	case "List":
		return f.list(r, req.Filepath)
	case "Stat", "Lstat":
		return f.stat(r, req.Filepath)
	}
	return nil, fmt.Errorf("sftpjobs: unsupported method %q", req.Method)
}

func (f *FS) list(r ref, p string) (sftp.ListerAt, error) {
	ctx := f.cfg.Context
	switch r.kind {
	case kindRoot:
		out := listerAt{dirInfo(outputsDir, time.Time{})}
		if f.templatesEnabled() {
			out = append(out, dirInfo(templatesDir, time.Time{}))
		}
		if f.dropEnabled() {
			out = append(out, dirInfo(submitDir, time.Time{}))
		}
		return out, nil

	case kindOutputs:
		refs, err := f.readableJobs(ctx)
		if err != nil {
			// A listing holding the reason, rather than a failed
			// readdir the client would render as "Failure"; see
			// OutputsNoticeName.
			text := f.noticeFor(err)
			return listerAt{fileInfo{
				name: OutputsNoticeName, size: int64(len(text)), mode: 0o444,
			}}, nil
		}
		out := make(listerAt, 0, len(refs))
		for _, j := range refs {
			// Directories only. The archive is not listed; see the
			// package comment.
			out = append(out, dirInfo(jobDirName(j.Cluster, j.Proc), j.Completed))
		}
		return out, nil

	case kindJobDir:
		// No sandbox fetch here, which is the point of the level: data/
		// is a directory and needs no size, and the ad is one query. An
		// `ls -lh` of a job therefore costs nothing like an `ls -lh` of
		// its output, and the unavoidable pass over the sandbox happens
		// when somebody asks for the files.
		out := listerAt{dirInfo(dataDirName, time.Time{})}
		if text, err := f.jobAdText(ctx, r); err != nil {
			// Shown as absent rather than failing the listing: the
			// output is still there to fetch, and the ad is a
			// convenience like the /outputs listing itself.
			f.logf("sftpjobs: no ad for %s: %v", jobDirName(r.cluster, r.proc), err)
		} else {
			out = append(out, fileInfo{
				name: JobAdFileName, size: int64(len(text)), mode: 0o444,
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
		return out, nil

	case kindJobData:
		// Prepare rather than Layout: a client that lists the output is
		// about to read it, and doing both in one pass is the
		// difference between one drain of the sandbox and two.
		layout, err := f.cfg.Store.Prepare(ctx, f.job(r))
		if err != nil {
			return nil, err
		}
		return f.listLayout(layout, ""), nil

	case kindJobFile:
		// A directory inside the sandbox.
		layout, err := f.cfg.Store.Prepare(ctx, f.job(r))
		if err != nil {
			return nil, err
		}
		if e, ok := layout.Lookup(r.name); ok && !e.Dir {
			return nil, &os.PathError{Op: "readdir", Path: p, Err: syscall.ENOTDIR}
		}
		return f.listLayout(layout, r.name), nil

	case kindJobAd, kindJobTar, kindOutputsNotice:
		return nil, &os.PathError{Op: "readdir", Path: p, Err: syscall.ENOTDIR}
	}
	if r.kind.inDrop() {
		return f.listDrop(r, p)
	}
	if r.kind.inTemplates() {
		return f.listTemplates(r, p)
	}
	return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
}

// listLayout returns the entries directly inside dir ("" for the
// sandbox root).
func (f *FS) listLayout(l *sandboxfs.Layout, dir string) listerAt {
	seen := map[string]bool{}
	out := listerAt{}
	for _, e := range l.Entries {
		rest, ok := childOf(e.Name, dir)
		if !ok {
			continue
		}
		name, nested := splitFirst(rest)
		if seen[name] {
			continue
		}
		seen[name] = true
		switch {
		case nested:
			// An entry deeper than this level implies the directory
			// between, which a sandbox tar does not always carry its own
			// header for.
			out = append(out, dirInfo(name, time.Time{}))
		case e.Dir:
			out = append(out, dirInfo(name, time.Time{}))
		default:
			out = append(out, fileInfo{name: name, size: e.Size, mode: permBits(e.Mode)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func (f *FS) stat(r ref, p string) (sftp.ListerAt, error) {
	ctx := f.cfg.Context
	switch r.kind {
	case kindRoot:
		return listerAt{dirInfo("/", time.Time{})}, nil

	case kindOutputs:
		return listerAt{dirInfo(outputsDir, time.Time{})}, nil

	case kindOutputsNotice:
		_, err := f.readableJobs(ctx)
		if err == nil {
			return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}
		}
		text := f.noticeFor(err)
		return listerAt{fileInfo{
			name: OutputsNoticeName, size: int64(len(text)), mode: 0o444,
		}}, nil

	case kindJobDir:
		// Answered without reading the sandbox: a directory needs no
		// size, and a client stats a directory before listing it. With a
		// lister available the answer is accurate; without one any
		// well-formed id stats as a directory and the failure arrives at
		// the listing instead.
		if f.cfg.Jobs == nil {
			return listerAt{dirInfo(jobDirName(r.cluster, r.proc), time.Time{})}, nil
		}
		refs, err := f.readableJobs(ctx)
		if err != nil {
			// A lister that cannot answer is treated as one that is not
			// there. Naming a job is the contract here and the listing
			// is the convenience, and what keeps another account's job
			// out of reach is the store's scope, not this check --
			// which is why answering it is safe. Without this a broken
			// job index would also stop somebody fetching output they
			// can name, which is the half that still works.
			return listerAt{dirInfo(jobDirName(r.cluster, r.proc), time.Time{})}, nil
		}
		for _, j := range refs {
			if j.Cluster == r.cluster && j.Proc == r.proc {
				return listerAt{dirInfo(jobDirName(j.Cluster, j.Proc), j.Completed)}, nil
			}
		}
		return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}

	case kindJobData:
		// A directory, so no size and nothing to fetch. Whether the job
		// exists is answered by the listing or the read that follows,
		// the same way kindJobDir answers it without a lister.
		return listerAt{dirInfo(dataDirName, time.Time{})}, nil

	case kindJobAd:
		text, err := f.jobAdText(ctx, r)
		if err != nil {
			return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}
		}
		return listerAt{fileInfo{
			name: JobAdFileName, size: int64(len(text)), mode: 0o444,
		}}, nil

	case kindJobFile:
		layout, err := f.cfg.Store.Layout(ctx, f.job(r))
		if err != nil {
			return nil, err
		}
		if e, ok := layout.Lookup(r.name); ok {
			if e.Dir {
				return listerAt{dirInfo(path.Base(e.Name), time.Time{})}, nil
			}
			return listerAt{fileInfo{
				name: path.Base(e.Name),
				size: e.Size,
				mode: permBits(e.Mode),
			}}, nil
		}
		// An implied directory: something in the layout lives under it.
		for _, e := range layout.Entries {
			if _, ok := childOf(e.Name, r.name); ok {
				return listerAt{dirInfo(path.Base(r.name), time.Time{})}, nil
			}
		}
		return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}

	case kindJobTar:
		size, err := f.cfg.Store.TarSize(ctx, f.job(r))
		if err != nil {
			return nil, err
		}
		return listerAt{fileInfo{name: dataTarName, size: size, mode: 0o444}}, nil
	}
	if r.kind.inDrop() {
		return f.statDrop(r, p)
	}
	if r.kind.inTemplates() {
		return f.statTemplates(r, p)
	}
	return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
}

// noticeFor renders the /outputs notice, once per session.
//
// Held rather than rebuilt because a client stats a file before opening
// it: if the next attempt failed differently the size it was given
// would not match the bytes it then read.
func (f *FS) noticeFor(err error) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notice == "" {
		f.notice = outputsNoticeText(err)
	}
	return f.notice
}

// outputsNoticeText is what somebody reads when the listing failed.
//
// Written for the user, not the operator: it says whose problem this is,
// what still works, and what to pass on. The reason itself comes from
// whoever wired up the lister, so it is quoted rather than interpreted.
func outputsNoticeText(err error) string {
	var b strings.Builder
	b.WriteString("The list of your finished jobs is not available\n")
	b.WriteString("==============================================\n\n")
	fmt.Fprintf(&b, "  %v\n\n", err)
	b.WriteString("That is why this directory holds nothing else. It is this access\n")
	b.WriteString("point's problem rather than yours: nothing has happened to your jobs\n")
	b.WriteString("or to their output.\n\n")
	b.WriteString("A job you can name is still reachable, because fetching one job's\n")
	b.WriteString("output does not go through this listing:\n\n")
	fmt.Fprintf(&b, "  sftp> get -r /%s/1234.0/%s\n", outputsDir, dataDirName)
	fmt.Fprintf(&b, "  sftp> get /%s/1234.0/%s\n\n", outputsDir, JobAdFileName)
	b.WriteString("Cluster and proc ids come from condor_q, condor_history, or the web\n")
	b.WriteString("interface for this access point.\n\n")
	b.WriteString("If you did not expect this, send the indented line above to whoever\n")
	b.WriteString("runs this access point. It is the whole diagnosis.\n")
	return b.String()
}

func (f *FS) readableJobs(ctx context.Context) ([]JobRef, error) {
	if f.cfg.Jobs == nil {
		return nil, nil
	}
	refs, err := f.cfg.Jobs.ReadableJobs(ctx)
	if err != nil {
		// Warn, not debug: this is a user being told their job list is
		// unavailable, and the reason exists nowhere else on the server.
		// It was debug-level once, which meant a deployment that had
		// never turned debug logging on had no record of the refusal at
		// all -- and the user's own client had printed only "Failure".
		f.warnf("sftpjobs: listing jobs for %s: %v", f.cfg.Account, err)
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Cluster != refs[j].Cluster {
			return refs[i].Cluster < refs[j].Cluster
		}
		return refs[i].Proc < refs[j].Proc
	})
	return refs, nil
}

// Filewrite implements sftp.FileWriter.
//
// Refused with a reason rather than left unset: an unset handler makes
// the server answer "unexpected request", which tells somebody whose
// upload failed nothing at all.
func (f *FS) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	r, err := parsePath(req.Filepath)
	if err != nil {
		return nil, err
	}
	switch r.kind {
	case kindSubmitFile:
		return f.writeTemplates(r, req.Filepath)
	case kindDropFile:
		return f.writeDrop(r, req.Filepath)
	}
	return nil, &os.PathError{Op: "write", Path: req.Filepath, Err: syscall.EROFS}
}

// Filecmd implements sftp.FileCmder.
func (f *FS) Filecmd(req *sftp.Request) error {
	return &os.PathError{Op: cmdOp(req.Method), Path: req.Filepath, Err: syscall.EROFS}
}

func cmdOp(method string) string {
	if method == "" {
		return "cmd"
	}
	return method
}

// childOf reports the part of name below dir, and whether it is below it
// at all. dir of "" is the sandbox root.
func childOf(name, dir string) (string, bool) {
	if dir == "" {
		return name, true
	}
	if len(name) <= len(dir)+1 || name[:len(dir)] != dir || name[len(dir)] != '/' {
		return "", false
	}
	return name[len(dir)+1:], true
}

// splitFirst returns the first component of rest and whether anything
// follows it.
func splitFirst(rest string) (string, bool) {
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[:i], true
		}
	}
	return rest, false
}

// listerAt is an ordered answer to one listing or stat.
type listerAt []os.FileInfo

func (l listerAt) ListAt(out []os.FileInfo, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("sftpjobs: negative listing offset")
	}
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(out, l[off:])
	if int64(n)+off >= int64(len(l)) {
		return n, io.EOF
	}
	return n, nil
}

// fileInfo is one entry. Times are left zero: a spooled sandbox tar
// carries the sender's mtimes, which are the execute node's clock, and a
// wrong time is worse than an obviously absent one.
type fileInfo struct {
	name string
	size int64
	mode fs.FileMode
	mod  time.Time
}

func dirInfo(name string, mod time.Time) fileInfo {
	return fileInfo{name: name, mode: fs.ModeDir | 0o555, mod: mod}
}

// permBits is a layout entry's permission bits.
//
// The mask comes before the conversion, not after: Mode arrives from the
// wire as an int64, and a value wider than fs.FileMode would wrap on the
// way in rather than be narrowed by a later .Perm().
func permBits(mode int64) fs.FileMode {
	return fs.FileMode(mode & 0o777)
}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return f.size }
func (f fileInfo) Mode() fs.FileMode  { return f.mode }
func (f fileInfo) ModTime() time.Time { return f.mod }
func (f fileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fileInfo) Sys() any           { return nil }
