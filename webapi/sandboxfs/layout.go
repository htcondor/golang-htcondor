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

// Package sandboxfs projects a finished job's sandbox as files.
//
// The protocols on either side want opposite things. A spooled sandbox
// can only be read with TRANSFER_DATA, which streams the whole thing
// once, in order, with each file's size arriving just ahead of its
// bytes. A file protocol wants to stat one name, seek in another and
// resume a third. Something has to bridge that, and what bridges it here
// is the LAYOUT: every entry's name, mode and size, with no bytes at
// all.
//
// With a layout in hand every stat and every directory listing is
// answered without touching the network, which is the only way a client
// that stats everything it lists can be served at all. The bytes are
// then streamed on demand.
//
// The layout is therefore the load-bearing cache and the one worth
// holding: cheap to keep -- a few tens of bytes an entry -- and
// expensive to lose, because rebuilding it costs a full-bandwidth pass
// over the sandbox even though it keeps none of it. A byte cache is the
// opposite and is governed separately.
package sandboxfs

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Entry is one name in a job's sandbox.
type Entry struct {
	// Name is sandbox-relative and slash-separated, with no leading
	// slash and no "." or ".." component.
	Name string
	// Mode is the Unix permission bits.
	Mode int64
	// Size is the file size in bytes, and is 0 for a directory.
	Size int64
	// Dir reports a directory rather than a regular file.
	Dir bool
}

// Layout is everything a job's sandbox contains except its bytes.
type Layout struct {
	// Entries are in the order the sandbox streamed them, which is also
	// the order a sequential read must take them in.
	Entries []Entry

	// Advertised is the total the sender declared in the transfer
	// preamble, and AdvertisedKnown whether it declared one at all.
	//
	// Kept beside the entries rather than replacing their sum because
	// the two answer different questions: this one is available before
	// any byte moves, which is what a size limit needs, while the sum is
	// only known once the layout has been built.
	Advertised      int64
	AdvertisedKnown bool
}

// Bytes is the sum of the entry sizes.
func (l *Layout) Bytes() int64 {
	var n int64
	for _, e := range l.Entries {
		n += e.Size
	}
	return n
}

// Lookup finds an entry by name.
func (l *Layout) Lookup(name string) (Entry, bool) {
	for _, e := range l.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// ErrTooManyEntries is returned when a sandbox has more names than the
// configured limit. A sandbox of a hundred thousand files is a different
// problem from a large one and is not served here.
var ErrTooManyEntries = errors.New("sandboxfs: sandbox has too many entries")

// recordLayout reads a sandbox tar and records what it describes,
// keeping no bytes: tar.Reader.Next skips each body on its way to the
// next header.
//
// maxEntries of 0 means no limit.
func recordLayout(r io.Reader, maxEntries int) (*Layout, error) {
	l := &Layout{}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return l, nil
		}
		if err != nil {
			return nil, fmt.Errorf("sandboxfs: reading the sandbox: %w", err)
		}

		var dir bool
		switch h.Typeflag {
		case tar.TypeReg:
		case tar.TypeDir:
			dir = true
		default:
			// A sandbox tar carries files and directories. Anything
			// else -- a symlink, a device, a hard link -- is skipped
			// rather than projected, because a name whose meaning
			// depends on being resolved somewhere else is not a name
			// this can serve honestly.
			continue
		}

		name, err := cleanEntryName(h.Name)
		if err != nil {
			return nil, err
		}
		if name == "" {
			continue
		}
		if maxEntries > 0 && len(l.Entries) >= maxEntries {
			return nil, fmt.Errorf("%w (limit %d)", ErrTooManyEntries, maxEntries)
		}

		size := h.Size
		if dir {
			size = 0
		}
		l.Entries = append(l.Entries, Entry{
			Name: name,
			Mode: h.Mode,
			Size: size,
			Dir:  dir,
		})
	}
}

// cleanEntryName rejects a name rather than repairing it.
//
// Repairing would silently change which file a caller is asking for, and
// there is no reading of an absolute path or a ".." component in a
// sandbox entry that is better served by guessing. The schedd-side sink
// guards this too; a second opinion costs a string compare, and this one
// is reading what a remote sent.
func cleanEntryName(name string) (string, error) {
	n := strings.TrimSuffix(name, "/")
	if n == "" || n == "." {
		return "", nil
	}
	if strings.HasPrefix(n, "/") || strings.Contains(n, `\`) || strings.ContainsRune(n, 0) {
		return "", fmt.Errorf("sandboxfs: refusing sandbox entry %q", name)
	}
	if cleaned := path.Clean(n); cleaned != n {
		return "", fmt.Errorf("sandboxfs: refusing sandbox entry %q", name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." || part == "." || part == "" {
			return "", fmt.Errorf("sandboxfs: refusing sandbox entry %q", name)
		}
	}
	return n, nil
}
