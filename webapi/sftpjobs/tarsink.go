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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// tarSink holds an uploaded archive until it is submitted, and then
// serves it to the spool as many times as the cluster needs.
//
// Why the bytes are held at all, rather than streamed to the schedd as
// they arrive, is worth writing down, because streaming looks obviously
// better and is not:
//
//   - The archive has to be READABLE MORE THAN ONCE. HTCondor spools
//     input per job, so a `queue 20` submission needs the same tar sent
//     twenty times -- see webapi/spool, whose Source interface exists
//     for exactly this. A stream can be replayed to one proc and no
//     more.
//   - The submission must not happen until the upload CLOSES CLEANLY.
//     SFTP never says how long a file will be, so a clean close is the
//     only thing that distinguishes a client that finished from one
//     that vanished. Streaming would mean creating the cluster first
//     and withdrawing it afterwards if the transfer died -- and a
//     truncated tar is a valid tar, so the half of it that arrived
//     would spool without complaint and release jobs whose files are
//     missing.
//
// Not safe for concurrent use: its owner serializes, because pkg/sftp
// writes from eight workers at once. See archiveUpload.
//
// What streaming WOULD have saved is memory, and that is what the spill
// is for: with a buffer directory configured the bytes go to a file,
// which is both a natural io.WriterAt -- so an out-of-order write from
// a pipelined client lands at its offset with no reordering window --
// and cheap to read back once per proc. Nothing is held in memory but
// the write in flight.
type tarSink struct {
	// buf holds the archive when no buffer directory is configured.
	buf []byte
	// file and path hold it when one is.
	file *os.File
	path string

	max int64
	n   int64 // the highest offset written, which is the size
}

// newTarSink opens a sink. dir is the operator's spool buffer
// directory; empty keeps the upload in memory, which is why the two
// come with different ceilings -- see Config.
func newTarSink(dir string, limit int64) (*tarSink, error) {
	if limit <= 0 {
		return nil, errors.New("sftpjobs: no size limit for an upload")
	}
	s := &tarSink{max: limit}
	if dir == "" {
		return s, nil
	}
	f, err := os.CreateTemp(dir, "sftpjobs-*.tar")
	if err != nil {
		return nil, fmt.Errorf("sftpjobs: opening a buffer file: %w", err)
	}
	s.file, s.path = f, f.Name()
	return s, nil
}

// WriteAt implements io.WriterAt.
//
// Offsets are honoured rather than assumed sequential: pkg/sftp hands
// write packets to eight workers at once, so a pipelined client's
// writes arrive concurrently and out of order. Assuming otherwise would
// silently shuffle somebody's archive.
func (s *tarSink) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("sftpjobs: negative write offset %d", off)
	}
	end := off + int64(len(p))
	if end > s.max {
		return 0, fmt.Errorf("the archive is larger than the %d byte limit", s.max)
	}
	if end > s.n {
		s.n = end
	}
	if s.file != nil {
		return s.file.WriteAt(p, off)
	}
	if int64(len(s.buf)) < end {
		grown := make([]byte, end)
		copy(grown, s.buf)
		s.buf = grown
	}
	return copy(s.buf[off:], p), nil
}

// Fill writes everything r yields into an empty sink, which is how a
// gzipped upload becomes the plain tar the spool reads.
//
// For a sink of its own, never the one r is reading from: see
// decompressed.
func (s *tarSink) Fill(r io.Reader) error {
	if s.n != 0 {
		return errors.New("sftpjobs: filling a sink that already holds something")
	}
	// One byte past the limit, so the limit is distinguishable from a
	// file that happens to end exactly on it.
	n, err := io.Copy(writerAtWriter{s: s}, io.LimitReader(r, s.max+1))
	if err != nil {
		return err
	}
	if n > s.max {
		return fmt.Errorf("the archive expands to more than the %d byte limit", s.max)
	}
	return nil
}

// writerAtWriter turns the sink's WriteAt into a sequential Writer for
// Fill's io.Copy.
type writerAtWriter struct{ s *tarSink }

func (w writerAtWriter) Write(p []byte) (int, error) {
	n, err := w.s.WriteAt(p, w.s.n)
	return n, err
}

// Reader implements spool.Source: a fresh reader positioned at the
// start, once per proc.
func (s *tarSink) Reader() (io.ReadCloser, error) {
	if s.file == nil {
		return io.NopCloser(bytes.NewReader(s.buf)), nil
	}
	// A second handle rather than a seek on the writer's: FanOut opens
	// one reader per proc and runs them at once, and they would
	// otherwise share a file offset.
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Size implements spool.Source.
func (s *tarSink) Size() int64 { return s.n }

// Close implements spool.Source. Safe to call twice, because the
// upload's own failure path and the submission's both end here.
func (s *tarSink) Close() error {
	if s.file == nil {
		s.buf = nil
		return nil
	}
	err := s.file.Close()
	if rmErr := os.Remove(s.path); rmErr != nil && !os.IsNotExist(rmErr) && err == nil {
		err = rmErr
	}
	s.file, s.path = nil, ""
	return err
}

// digest is the archive's content hash, for the duplicate check.
//
// Of the stored bytes, so a gzipped upload and the same archive sent
// uncompressed have the same digest: what was submitted is what the
// sink holds, not what arrived on the wire.
func (s *tarSink) digest() (string, error) {
	r, err := s.Reader()
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
