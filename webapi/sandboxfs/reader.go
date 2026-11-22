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
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
)

// tarBlock is the tar format's block size. Bodies are padded up to it
// and the archive ends with two zero blocks.
const tarBlock = 64 * 8

// ErrNotRegular is returned when a name exists in the sandbox but is not
// a regular file.
var ErrNotRegular = errors.New("sandboxfs: not a regular file")

// Open returns one file from a job's sandbox.
//
// A cached file comes back as an *os.File, so it seeks and supports
// ReadAt; a caller wanting random access should assert for io.ReaderAt
// and fall back to copying when the assertion fails. Otherwise the
// sandbox is streamed, and the reader is sequential only -- which is the
// honest shape, because the sandbox arrives once, in order.
//
// A miss spills the whole job into the byte cache on its way past, so
// the SECOND read of a job costs nothing even when it is of a different
// file. That is the point: a client walking a sandbox file by file would
// otherwise pay a full pass per file, since a spooled sandbox has no
// ranged read.
func (s *Store) Open(ctx context.Context, job Job, name string) (io.ReadCloser, error) {
	key := entryKey{job: job, name: path.Clean(name)}
	l, err := s.Layout(ctx, job)
	if err != nil {
		return nil, err
	}
	e, ok := l.Lookup(key.name)
	if !ok {
		return nil, fmt.Errorf("sandboxfs: %q in job %s: %w", name, key.job, fs.ErrNotExist)
	}
	if e.Dir {
		return nil, fmt.Errorf("sandboxfs: %q in job %s: %w", name, key.job, ErrNotRegular)
	}

	if f, hit := s.data.Open(key); hit {
		return f, nil
	}

	// One pass that fills the cache, after which the wanted file is
	// there. Done even though only one file was asked for, because the
	// pass costs the same either way and the alternative is paying it
	// again for the next name.
	if s.data.enabled() && e.Size <= s.data.budget {
		if err := s.materialize(ctx, job, l); err != nil {
			return nil, err
		}
		if f, hit := s.data.Open(key); hit {
			return f, nil
		}
		// Evicted between filling and opening, or never cacheable.
		// Falling through streams it, which is the miss path working.
	}
	return s.streamEntry(ctx, job, e)
}

// Materialize streams a job's sandbox once and spills what fits into the
// byte cache. Reads of that job are served from disk afterwards.
//
// Worth calling when a client is about to read a sandbox rather than one
// file of it -- entering a directory, say -- so the pass happens once and
// deliberately instead of once per file by accident.
func (s *Store) Materialize(ctx context.Context, job Job) error {
	l, err := s.Layout(ctx, job)
	if err != nil {
		return err
	}
	return s.materialize(ctx, job, l)
}

// materialize does one pass, caching every regular file that fits.
//
// Errors on individual entries are not fatal: a file that could not be
// staged is simply not cached, and reading it later streams instead.
// Only a failure of the pass itself is returned.
func (s *Store) materialize(ctx context.Context, job Job, l *Layout) error {
	if !s.data.enabled() || s.cachedWhole(job, l) {
		return nil
	}
	_, err, _ := s.fill.Do(job.cacheKey(), func() (any, error) {
		// Re-checked inside the flight: a caller that waited behind
		// another's fill has nothing left to do.
		if s.cachedWhole(job, l) {
			return nil, nil
		}
		pr, pw := io.Pipe()
		go func() {
			_ = pw.CloseWithError(s.fetcher.FetchSandbox(ctx, job, pw, s.gate(job)))
		}()
		defer func() { _ = pr.Close() }()

		tr := tar.NewReader(pr)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			if err != nil {
				_ = pr.CloseWithError(err)
				return nil, fmt.Errorf("sandboxfs: reading job %s: %w", job, err)
			}
			if h.Typeflag != tar.TypeReg {
				continue
			}
			name, nameErr := cleanEntryName(h.Name)
			if nameErr != nil || name == "" {
				continue
			}
			// The declared size has to agree with the layout, or a
			// reader of the cached copy would get bytes that do not
			// match the size every stat reported.
			if e, ok := l.Lookup(name); !ok || e.Size != h.Size {
				continue
			}
			s.spill(entryKey{job: job, name: name}, h.Size, tr)
		}
	})
	return err
}

// cachedWhole reports whether every entry worth caching already is.
//
// Without this, filling a job whose bytes are all present still costs a
// full pass -- which is what listing a second directory inside one
// sandbox would do, once per listing.
//
// An entry the cache would refuse anyway does not count as missing, or
// a sandbox holding one file over the budget would re-fetch forever.
func (s *Store) cachedWhole(job Job, l *Layout) bool {
	if !s.data.enabled() {
		return false
	}
	for _, e := range l.Entries {
		if e.Dir || e.Size > s.data.budget {
			continue
		}
		if !s.data.has(entryKey{job: job, name: e.Name}) {
			return false
		}
	}
	return true
}

// spill caches one entry's bytes, giving up quietly on any problem: the
// cache is best-effort by construction, and a failure here only means a
// later read streams.
func (s *Store) spill(key entryKey, size int64, r io.Reader) {
	st, ok, err := s.data.Stage(key, size)
	if err != nil || !ok {
		if err != nil {
			s.cfg.logf("sandboxfs: staging %s/%s: %v", key.job, key.name, err)
		}
		return
	}
	defer func() { _ = st.Close() }()
	if _, err := io.CopyN(st, r, size); err != nil {
		s.cfg.logf("sandboxfs: caching %s/%s: %v", key.job, key.name, err)
		return
	}
	if err := st.Commit(); err != nil {
		s.cfg.logf("sandboxfs: publishing %s/%s: %v", key.job, key.name, err)
	}
}

// streamEntry hands back one entry's bytes straight off the wire.
//
// The pass runs for as long as the caller reads and is torn down by
// Close, so a caller that stops early does not leave a transfer running.
func (s *Store) streamEntry(ctx context.Context, job Job, want Entry) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		_ = pw.CloseWithError(s.fetcher.FetchSandbox(ctx, job, pw, s.gate(job)))
	}()

	tr := tar.NewReader(pr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			_ = pr.Close()
			return nil, fmt.Errorf("sandboxfs: %q vanished from job %s: %w", want.Name, job, fs.ErrNotExist)
		}
		if err != nil {
			_ = pr.CloseWithError(err)
			return nil, fmt.Errorf("sandboxfs: reading job %s: %w", job, err)
		}
		name, nameErr := cleanEntryName(h.Name)
		if nameErr != nil || name != want.Name || h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Size != want.Size {
			// The size every stat reported is already wrong, and
			// nothing good follows from serving a different length
			// quietly.
			_ = pr.Close()
			return nil, fmt.Errorf("sandboxfs: %s/%s is %d bytes on the wire, %d in the layout",
				job, name, h.Size, want.Size)
		}
		return &streamReader{body: io.LimitReader(tr, h.Size), closer: pr}, nil
	}
}

// streamReader is one entry's body, with the transfer behind it.
type streamReader struct {
	body   io.Reader
	closer *io.PipeReader
}

func (r *streamReader) Read(p []byte) (int, error) { return r.body.Read(p) }

func (r *streamReader) Close() error {
	// CloseWithError rather than Close so the sender's pending write
	// fails instead of blocking when the caller stopped early.
	return r.closer.CloseWithError(errors.New("sandboxfs: reader closed"))
}

// gate is the size limit as a preamble hook, shared by every pass.
func (s *Store) gate(job Job) func(int64, bool) error {
	return func(size int64, known bool) error {
		limit := s.cfg.maxSandboxBytes()
		if limit <= 0 {
			return nil
		}
		if !known {
			if s.cfg.ServeUnadvertisedSize {
				return nil
			}
			return &SizeUnknownError{Cluster: job.Cluster, Proc: job.Proc}
		}
		if size > limit {
			return &TooLargeError{Cluster: job.Cluster, Proc: job.Proc, Size: size, Limit: limit}
		}
		return nil
	}
}

// TarSize is the exact length in bytes of what OpenTar will stream.
//
// Exact rather than estimated, because a file protocol asks for a size
// before it reads and mishandles a wrong one in every direction: a
// client told nothing often reports an empty file, and one told too much
// waits for bytes that never come.
//
// Measured by writing each entry's header through a real tar.Writer into
// a counter, rather than by reimplementing the format's rules about when
// a long name needs an extended header. The body is then accounted
// analytically, since its padding is the one part of tar that is simple.
func (s *Store) TarSize(ctx context.Context, job Job) (int64, error) {
	l, err := s.Layout(ctx, job)
	if err != nil {
		return 0, err
	}
	return tarSize(l)
}

func tarSize(l *Layout) (int64, error) {
	var total int64
	for _, e := range l.Entries {
		var count countingWriter
		// A fresh writer per header: tar.Writer carries no state between
		// entries except the body it is waiting for, so one header's
		// encoded length does not depend on its neighbours -- and this
		// way there is no pending body to satisfy.
		tw := tar.NewWriter(&count)
		if err := tw.WriteHeader(headerFor(e)); err != nil {
			return 0, fmt.Errorf("sandboxfs: sizing %q: %w", e.Name, err)
		}
		total += int64(count) + roundUpBlock(e.Size)
	}
	// tar.Writer.Close ends the archive with two zero blocks.
	return total + 2*tarBlock, nil
}

// OpenTar streams a job's sandbox as a tar of exactly TarSize bytes.
//
// Re-tarred here rather than passed through, so the bytes are the ones
// TarSize measured: the sender's own framing is not this format and its
// choices are not ours to predict. Nothing is buffered and nothing is
// cached -- a single sequential read is what streaming is for.
func (s *Store) OpenTar(ctx context.Context, job Job) (io.ReadCloser, int64, error) {
	l, err := s.Layout(ctx, job)
	if err != nil {
		return nil, 0, err
	}
	size, err := tarSize(l)
	if err != nil {
		return nil, 0, err
	}

	srcR, srcW := io.Pipe()
	go func() {
		_ = srcW.CloseWithError(s.fetcher.FetchSandbox(ctx, job, srcW, s.gate(job)))
	}()

	outR, outW := io.Pipe()
	go func() {
		err := retar(l, srcR, outW)
		_ = srcR.CloseWithError(err)
		_ = outW.CloseWithError(err)
	}()

	return &tarReader{body: outR, src: srcR}, size, nil
}

// retar copies the sandbox into a tar whose entries are exactly the ones
// the layout describes, in the layout's order.
func retar(l *Layout, src io.Reader, dst io.Writer) error {
	tw := tar.NewWriter(dst)
	tr := tar.NewReader(src)

	// Index the layout once; a sandbox of twenty thousand names would
	// otherwise be a linear scan per entry.
	want := make(map[string]Entry, len(l.Entries))
	for _, e := range l.Entries {
		want[e.Name] = e
	}
	seen := make(map[string]bool, len(l.Entries))

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("sandboxfs: reading the sandbox: %w", err)
		}
		name, nameErr := cleanEntryName(h.Name)
		if nameErr != nil || name == "" {
			continue
		}
		e, ok := want[name]
		if !ok || seen[name] {
			continue
		}
		if !e.Dir && h.Size != e.Size {
			// The length has already been declared to the client and
			// cannot be revised, so a mismatch ends the transfer rather
			// than producing an archive that does not match it.
			return fmt.Errorf("sandboxfs: %q is %d bytes on the wire, %d in the layout",
				name, h.Size, e.Size)
		}
		seen[name] = true
		if err := tw.WriteHeader(headerFor(e)); err != nil {
			return fmt.Errorf("sandboxfs: writing %q: %w", name, err)
		}
		if e.Dir {
			continue
		}
		if _, err := io.CopyN(tw, tr, e.Size); err != nil {
			return fmt.Errorf("sandboxfs: copying %q: %w", name, err)
		}
	}

	if len(seen) != len(l.Entries) {
		return fmt.Errorf("sandboxfs: the sandbox held %d of the %d entries the layout describes",
			len(seen), len(l.Entries))
	}
	return tw.Close()
}

// tarReader is the re-tarred archive, with both transfers behind it.
type tarReader struct {
	body *io.PipeReader
	src  *io.PipeReader
}

func (r *tarReader) Read(p []byte) (int, error) { return r.body.Read(p) }

func (r *tarReader) Close() error {
	err := errors.New("sandboxfs: reader closed")
	_ = r.src.CloseWithError(err)
	return r.body.CloseWithError(err)
}

// headerFor is the one place an Entry becomes a tar header, so TarSize
// measures what OpenTar writes. Any divergence between the two would
// show up as a client hanging on a short archive.
func headerFor(e Entry) *tar.Header {
	h := &tar.Header{
		Name:     e.Name,
		Mode:     e.Mode,
		Format:   tar.FormatPAX,
		Typeflag: tar.TypeReg,
		Size:     e.Size,
	}
	if e.Dir {
		h.Name += "/"
		h.Typeflag = tar.TypeDir
		h.Size = 0
	}
	return h
}

func roundUpBlock(n int64) int64 {
	if n <= 0 {
		return 0
	}
	if rem := n % tarBlock; rem != 0 {
		return n + tarBlock - rem
	}
	return n
}

// countingWriter counts bytes and keeps none.
type countingWriter int64

func (c *countingWriter) Write(p []byte) (int, error) {
	*c += countingWriter(len(p))
	return len(p), nil
}

// osFileIsReaderAt documents, by compiling, that a cache hit from Open
// supports random access. The SFTP layer asserts for it.
var _ io.ReaderAt = (*os.File)(nil)
