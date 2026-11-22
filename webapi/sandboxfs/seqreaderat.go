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
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrReadBehind is returned for a read at an offset the stream has
// already passed and no longer retains.
//
// It is the one thing a streamed read cannot do, and it is reported
// rather than papered over: restarting the sandbox transfer to satisfy a
// backwards seek would turn one odd read into a second full-bandwidth
// pass, silently. A caller that needs arbitrary random access should read
// from the byte cache instead.
var ErrReadBehind = errors.New("sandboxfs: read behind the streamed window")

// DefaultWindow is how much of a stream a SequentialReaderAt retains.
//
// Sized from how the SFTP request server actually reads: reads go to
// SftpServerWorkerCount (8) concurrent workers, each handling at most
// maxTxPacket (32 KiB by default), so the offsets in flight at any
// moment span a few hundred kilobytes. Megabytes of retention leaves
// room for a client with a larger packet size or a straggling worker
// without making the window a memory concern.
const DefaultWindow = 4 << 20

// SequentialReaderAt adapts a sequential stream to io.ReaderAt for
// clients that read forwards.
//
// The sandbox arrives once, in order, with no ranged read -- but a file
// protocol asks for offsets, concurrently. This is what bridges the two
// without buffering the whole stream: bytes already delivered are kept
// for a window behind the head, a read ahead of the head advances the
// stream (discarding what it passes), and a read behind the window is
// refused.
//
// Safe for concurrent use: reads serialise, which is what makes
// "advance the stream" a coherent operation at all.
type SequentialReaderAt struct {
	window  int
	scratch []byte

	mu  sync.Mutex
	src io.Reader
	rc  io.Closer
	// buf holds the bytes ending at pos, so buf[0] is at pos-len(buf).
	buf []byte
	pos int64
	err error
}

// NewSequentialReaderAt wraps rc. A window of zero takes DefaultWindow.
//
// Closing the result closes rc, which is what ends the transfer behind
// it.
func NewSequentialReaderAt(rc io.ReadCloser, window int) *SequentialReaderAt {
	if window <= 0 {
		window = DefaultWindow
	}
	return &SequentialReaderAt{
		window:  window,
		scratch: make([]byte, 256*1024),
		src:     rc,
		rc:      rc,
	}
}

// ReadAt implements io.ReaderAt.
//
// A short read is reported as io.EOF rather than any other error,
// because that is the only short read the SFTP request server keeps the
// data from: anything else becomes a status packet and the bytes are
// dropped.
func (s *SequentialReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("sandboxfs: negative read offset %d", off)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if start := s.pos - int64(len(s.buf)); off < start {
		return 0, fmt.Errorf("%w: offset %d is %d bytes behind", ErrReadBehind, off, start-off)
	}

	// Retain at least this call's own span, so a single read larger than
	// the window still works rather than coming back short.
	keep := int64(s.window)
	if int64(len(p)) > keep {
		keep = int64(len(p))
	}

	need := off + int64(len(p))
	for s.pos < need && s.err == nil {
		// Only what this read needs, never a bufferful. Overshooting
		// advances the head past the read that asked for it and evicts
		// that much more of the window, so a client reading 4 KiB at a
		// time would get a window shorter than the one configured --
		// and it waits for bytes nobody asked for yet.
		want := need - s.pos
		if want > int64(len(s.scratch)) {
			want = int64(len(s.scratch))
		}
		n, err := s.src.Read(s.scratch[:want])
		if n > 0 {
			s.buf = append(s.buf, s.scratch[:n]...)
			s.pos += int64(n)
			s.trim(off, keep)
		}
		if err != nil {
			s.err = err
		}
	}

	start := s.pos - int64(len(s.buf))
	if off >= s.pos {
		// Nothing at this offset: either the stream ended before it or
		// it ended here.
		if s.err != nil && !errors.Is(s.err, io.EOF) {
			return 0, s.err
		}
		return 0, io.EOF
	}
	n := copy(p, s.buf[off-start:])
	if n < len(p) {
		if s.err != nil && !errors.Is(s.err, io.EOF) {
			return n, s.err
		}
		return n, io.EOF
	}
	return n, nil
}

// trim drops the front of the buffer once it is longer than the
// retention, without dropping anything at or after lowest, which the
// read in progress still needs.
//
// The bytes are copied down rather than the slice resliced. Both stay
// bounded -- reslicing eats the capacity and append then reallocates --
// but reslicing pays a fresh allocation and a full copy every time that
// happens, repeatedly, for the length of a sandbox download. Copying
// down keeps one array for the life of the reader.
func (s *SequentialReaderAt) trim(lowest, keep int64) {
	over := int64(len(s.buf)) - keep
	if over <= 0 {
		return
	}
	start := s.pos - int64(len(s.buf))
	if cut := lowest - start; cut < over {
		over = cut
	}
	if over <= 0 {
		return
	}
	s.buf = append(s.buf[:0], s.buf[over:]...)
}

// Close closes the underlying reader, ending the transfer behind it.
func (s *SequentialReaderAt) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rc == nil {
		return nil
	}
	rc := s.rc
	s.rc = nil
	return rc.Close()
}

// retained reports the buffered byte count, for tests asserting the
// window stays bounded.
func (s *SequentialReaderAt) retained() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf)
}
