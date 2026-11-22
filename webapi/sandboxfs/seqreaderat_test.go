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
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"testing"
)

// pattern is a stream whose every byte is a function of its offset, so
// any read can be checked against where it claims to come from. A stream
// of repeated bytes would pass while serving the wrong offsets.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}

type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }

func seqOver(data []byte, window int) *SequentialReaderAt {
	return NewSequentialReaderAt(nopCloser{bytes.NewReader(data)}, window)
}

// TestSeqReaderAtForwardReads is the ordinary case: an sftp get walks
// the file in packet-sized pieces.
func TestSeqReaderAtForwardReads(t *testing.T) {
	data := pattern(300 * 1024)
	s := seqOver(data, 64*1024)
	defer func() { _ = s.Close() }()

	const chunk = 32 * 1024
	got := make([]byte, 0, len(data))
	for off := 0; ; off += chunk {
		buf := make([]byte, chunk)
		n, err := s.ReadAt(buf, int64(off))
		got = append(got, buf[:n]...)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt(%d): %v", off, err)
			}
			break
		}
	}
	if !bytes.Equal(got, data) {
		t.Errorf("reassembled %d bytes, want %d (equal=%v)", len(got), len(data), bytes.Equal(got, data))
	}
}

// TestSeqReaderAtOutOfOrderWithinWindow: the request server dispatches
// reads to concurrent workers, so they do not arrive in order. Anything
// still inside the window has to be answerable.
func TestSeqReaderAtOutOfOrderWithinWindow(t *testing.T) {
	data := pattern(200 * 1024)
	s := seqOver(data, 128*1024)
	defer func() { _ = s.Close() }()

	// Jump ahead first, then come back for what the stream passed.
	for _, off := range []int64{96 * 1024, 32 * 1024, 64 * 1024, 0, 128 * 1024} {
		buf := make([]byte, 4096)
		if _, err := s.ReadAt(buf, off); err != nil {
			t.Fatalf("ReadAt(%d): %v", off, err)
		}
		if !bytes.Equal(buf, data[off:off+4096]) {
			t.Errorf("ReadAt(%d) returned the wrong bytes", off)
		}
	}
}

// TestSeqReaderAtConcurrentReads is the shape the request server
// actually produces: several workers reading a window of offsets at
// once.
func TestSeqReaderAtConcurrentReads(t *testing.T) {
	data := pattern(4 << 20)
	s := seqOver(data, DefaultWindow)
	defer func() { _ = s.Close() }()

	const (
		workers = 8
		chunk   = 32 * 1024
	)
	var wg sync.WaitGroup
	errs := make(chan error, 256)
	// Offsets handed out in order, read concurrently, exactly as the
	// server's worker pool does.
	next := int64(0)
	var mu sync.Mutex
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				off := next
				next += chunk
				mu.Unlock()
				if off >= int64(len(data)) {
					return
				}
				buf := make([]byte, chunk)
				n, err := s.ReadAt(buf, off)
				if err != nil && !errors.Is(err, io.EOF) {
					errs <- fmt.Errorf("ReadAt(%d): %w", off, err)
					return
				}
				want := data[off:min(off+chunk, int64(len(data)))]
				if !bytes.Equal(buf[:n], want) {
					errs <- fmt.Errorf("ReadAt(%d) returned %d wrong bytes", off, n)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestSeqReaderAtRefusesReadBehind: restarting the sandbox transfer to
// satisfy a backwards seek would turn one odd read into a second
// full-bandwidth pass, silently. It is reported instead.
func TestSeqReaderAtRefusesReadBehind(t *testing.T) {
	data := pattern(1 << 20)
	s := seqOver(data, 64*1024)
	defer func() { _ = s.Close() }()

	buf := make([]byte, 4096)
	if _, err := s.ReadAt(buf, 900*1024); err != nil {
		t.Fatalf("forward ReadAt: %v", err)
	}
	_, err := s.ReadAt(buf, 0)
	if !errors.Is(err, ErrReadBehind) {
		t.Fatalf("ReadAt(0) after advancing = %v, want ErrReadBehind", err)
	}
	// And the reader still works forwards afterwards.
	if _, err := s.ReadAt(buf, 1000*1024); err != nil {
		t.Errorf("ReadAt after a refused one: %v", err)
	}
}

// TestSeqReaderAtWindowStaysBounded is the reason any of this exists
// rather than buffering the stream.
func TestSeqReaderAtWindowStaysBounded(t *testing.T) {
	const window = 64 * 1024
	data := pattern(8 << 20)
	s := seqOver(data, window)
	defer func() { _ = s.Close() }()

	buf := make([]byte, 32*1024)
	for off := int64(0); off < int64(len(data)); off += int64(len(buf)) {
		if _, err := s.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt(%d): %v", off, err)
		}
		// The head never advances past what a read asked for, so
		// retention is the window plus at most this read's own span --
		// and never grows with the stream.
		if got := s.retained(); got > window+len(buf) {
			t.Fatalf("retained %d bytes at offset %d, window is %d", got, off, window)
		}
	}
}

// TestSeqReaderAtReadLargerThanWindow: a single read bigger than the
// retention must not come back short, because a short read that is not
// EOF loses its data at the SFTP layer.
func TestSeqReaderAtReadLargerThanWindow(t *testing.T) {
	data := pattern(512 * 1024)
	s := seqOver(data, 8*1024)
	defer func() { _ = s.Close() }()

	buf := make([]byte, 256*1024)
	n, err := s.ReadAt(buf, 0)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != len(buf) || !bytes.Equal(buf, data[:len(buf)]) {
		t.Errorf("read %d bytes, want %d of the right ones", n, len(buf))
	}
}

// TestSeqReaderAtEOF covers the two ends: a final partial read keeps its
// data by reporting io.EOF, and a read past the end returns nothing.
func TestSeqReaderAtEOF(t *testing.T) {
	data := pattern(1000)
	s := seqOver(data, 4096)
	defer func() { _ = s.Close() }()

	buf := make([]byte, 400)
	if n, err := s.ReadAt(buf, 800); n != 200 || !errors.Is(err, io.EOF) {
		t.Errorf("final read = (%d, %v), want (200, io.EOF)", n, err)
	} else if !bytes.Equal(buf[:n], data[800:]) {
		t.Error("the final read returned the wrong bytes")
	}
	if n, err := s.ReadAt(buf, 1000); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("read at the end = (%d, %v), want (0, io.EOF)", n, err)
	}
	if n, err := s.ReadAt(buf, 5000); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("read past the end = (%d, %v), want (0, io.EOF)", n, err)
	}
}

// TestSeqReaderAtStreamErrorIsSticky: a transport failure is not an end
// of file, and must not be reported as one after the fact.
func TestSeqReaderAtStreamErrorIsSticky(t *testing.T) {
	boom := errors.New("transfer died")
	src := io.MultiReader(bytes.NewReader(pattern(100)), errReader{boom})
	s := NewSequentialReaderAt(nopCloser{src}, 4096)
	defer func() { _ = s.Close() }()

	buf := make([]byte, 400)
	n, err := s.ReadAt(buf, 0)
	if n != 100 {
		t.Errorf("read %d bytes before the failure, want 100", n)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("ReadAt = %v, want the transport error", err)
	}
	if _, err := s.ReadAt(buf, 100); !errors.Is(err, boom) {
		t.Errorf("a later read = %v, want the same error", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// TestSeqReaderAtCloseClosesTheStream: closing the handle is what ends
// the sandbox transfer behind it.
func TestSeqReaderAtCloseClosesTheStream(t *testing.T) {
	c := &countingCloser{Reader: bytes.NewReader(pattern(10))}
	s := NewSequentialReaderAt(c, 4096)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if c.closes != 1 {
		t.Errorf("underlying closed %d times, want 1", c.closes)
	}
}

type countingCloser struct {
	io.Reader
	closes int
}

func (c *countingCloser) Close() error { c.closes++; return nil }

// TestSeqReaderAtRandomForwardOffsets fuzzes the window bookkeeping with
// forward-but-irregular reads, which is what a client with a different
// packet size produces.
func TestSeqReaderAtRandomForwardOffsets(t *testing.T) {
	data := pattern(1 << 20)
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test input

	for trial := 0; trial < 20; trial++ {
		window := 1024 * (1 + rng.Intn(64))
		s := seqOver(data, window)
		off := int64(0)
		for off < int64(len(data)) {
			size := 1 + rng.Intn(48*1024)
			buf := make([]byte, size)
			n, err := s.ReadAt(buf, off)
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("trial %d window %d ReadAt(%d, %d): %v", trial, window, off, size, err)
			}
			if !bytes.Equal(buf[:n], data[off:off+int64(n)]) {
				t.Fatalf("trial %d window %d ReadAt(%d) returned the wrong bytes", trial, window, off)
			}
			// Advance by less than was read sometimes, so the next read
			// overlaps backwards into the window.
			step := int64(n)
			if n > 1 && rng.Intn(3) == 0 {
				step = int64(n / 2)
			}
			if step == 0 {
				break
			}
			off += step
		}
		_ = s.Close()
	}
}
