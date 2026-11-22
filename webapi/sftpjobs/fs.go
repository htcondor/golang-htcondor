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
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
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

	// Logf, if set, receives debug logging.
	Logf func(format string, args ...any)
}

// FS serves one account's jobs.
//
// One per channel, so the account is fixed and nothing a client sends
// can reach another's. The status and dedup maps live for the channel's
// lifetime: a client that uploads and then reads status/ in the same
// session sees its result, and one that reconnects does not. Persisting
// them would need storage this does not have; the submitted cluster
// carries the template and the digest as attributes, which is where a
// durable answer would come from.
type FS struct {
	cfg Config

	mu     sync.Mutex
	status map[statusKey]uploadStatus
	// submitted maps a template and a content digest to the cluster it
	// became, so the same bytes dropped twice are one submission.
	submitted map[statusKey]int
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
	return &FS{cfg: cfg}, nil
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

	case kindRoot, kindOutputs, kindJobDir:
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
			return nil, err
		}
		out := make(listerAt, 0, len(refs))
		for _, j := range refs {
			// Directories only. The archive is not listed; see the
			// package comment.
			out = append(out, dirInfo(jobDirName(j.Cluster, j.Proc), j.Completed))
		}
		return out, nil

	case kindJobDir:
		// Prepare rather than Layout: a client that lists a job
		// directory is about to read it, and doing both in one pass is
		// the difference between one drain of the sandbox and two.
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

	case kindJobTar:
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
			return nil, err
		}
		for _, j := range refs {
			if j.Cluster == r.cluster && j.Proc == r.proc {
				return listerAt{dirInfo(jobDirName(j.Cluster, j.Proc), j.Completed)}, nil
			}
		}
		return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}

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
		return listerAt{fileInfo{
			name: jobDirName(r.cluster, r.proc) + tarSuffix,
			size: size,
			mode: 0o444,
		}}, nil
	}
	if r.kind.inDrop() {
		return f.statDrop(r, p)
	}
	if r.kind.inTemplates() {
		return f.statTemplates(r, p)
	}
	return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
}

func (f *FS) readableJobs(ctx context.Context) ([]JobRef, error) {
	if f.cfg.Jobs == nil {
		return nil, nil
	}
	refs, err := f.cfg.Jobs.ReadableJobs(ctx)
	if err != nil {
		f.logf("sftpjobs: listing jobs for %s: %v", f.cfg.Account, err)
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
