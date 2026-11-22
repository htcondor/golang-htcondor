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
	"strings"
	"sync"
	"time"
)

// An upload commits when the client closes it cleanly.
//
// SFTP never says how long a file will be, so "all the bytes arrived"
// is not something a server can check. What it CAN tell apart is a
// client that finished from one that vanished: the request server calls
// Close() alone on an explicit SSH_FXP_CLOSE, and calls
// TransferError(err) FIRST and then Close() when the connection drops or
// the client crashes. So a writer implementing both knows which
// happened, and only the first submits.
//
// This is why the commit is a clean close rather than a rename into
// place, which was the obvious design: `sftp put` writes the final name
// directly and never renames, so a rename rule would have fired for
// rsync and for almost nothing else.
type upload struct {
	fs   *FS
	tref TemplateRef
	name string

	mu sync.Mutex
	// buf holds the file. Bounded by the template's byte limit plus one,
	// which is enough to know it was exceeded.
	buf []byte
	max int64
	// failed is set by TransferError, and is what stops Close from
	// submitting a file that never finished arriving.
	failed error
	closed bool
}

// WriteAt implements io.WriterAt.
//
// Offsets are honoured rather than assumed sequential: a client may
// write out of order, and assuming otherwise would silently reorder
// somebody's rows.
func (u *upload) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("sftpjobs: negative write offset %d", off)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return 0, errors.New("sftpjobs: write after close")
	}

	end := off + int64(len(p))
	if end > u.max {
		// Refused here rather than at parse time, so a client uploading
		// something enormous is stopped while it is still uploading.
		u.failed = fmt.Errorf("the file is larger than the %d byte limit", u.max-1)
		return 0, u.failed
	}
	if int64(len(u.buf)) < end {
		grown := make([]byte, end)
		copy(grown, u.buf)
		u.buf = grown
	}
	copy(u.buf[off:], p)
	return len(p), nil
}

// TransferError implements sftp.TransferError. It is called before
// Close when the transfer did not finish.
func (u *upload) TransferError(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failed == nil {
		u.failed = err
	}
}

// Close is the commit point.
func (u *upload) Close() error {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return nil
	}
	u.closed = true
	content, failed := u.buf, u.failed
	u.buf = nil
	u.mu.Unlock()

	if failed != nil {
		// Recorded rather than dropped silently: somebody whose upload
		// died deserves to find out from the status directory rather
		// than by noticing no jobs ran.
		u.fs.putStatus(u.tref, u.name, uploadStatus{
			State:    "incomplete",
			Template: u.tref.ID(),
			Err:      fmt.Sprintf("the upload did not finish: %v", failed),
		})
		return nil
	}
	return u.fs.acceptCSV(u.tref, u.name, content)
}

// uploadStatus is what a status entry says.
type uploadStatus struct {
	// State is "submitted", "refused", "incomplete" or "duplicate".
	State    string
	Template string
	Rows     int
	Cluster  int
	Digest   string
	Err      string
	At       time.Time
}

// Text renders a status entry for somebody reading it with cat.
func (s uploadStatus) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "state: %s\n", s.State)
	fmt.Fprintf(&b, "template: %s\n", s.Template)
	if !s.At.IsZero() {
		fmt.Fprintf(&b, "at: %s\n", s.At.UTC().Format(time.RFC3339))
	}
	if s.Rows > 0 {
		fmt.Fprintf(&b, "rows: %d\n", s.Rows)
	}
	if s.Cluster > 0 {
		fmt.Fprintf(&b, "cluster: %d\n", s.Cluster)
	}
	if s.Digest != "" {
		fmt.Fprintf(&b, "digest: %s\n", s.Digest)
	}
	if s.Err != "" {
		fmt.Fprintf(&b, "error: %s\n", s.Err)
	}
	return b.String()
}

// acceptCSV parses a finished upload and submits it.
//
// Every outcome is written to the status directory, including every
// refusal: the SFTP close reply carries one error string, which a GUI
// client renders as "could not close file" and nothing more. A
// per-row complaint has to be readable somewhere, and that somewhere is
// a file the client can fetch.
func (f *FS) acceptCSV(tref TemplateRef, name string, content []byte) error {
	ctx := f.cfg.Context
	digest := sha256.Sum256(content)
	hexDigest := hex.EncodeToString(digest[:])

	fail := func(err error) error {
		f.putStatus(tref, name, uploadStatus{
			State: "refused", Template: tref.ID(), Digest: hexDigest,
			Err: err.Error(), At: time.Now(),
		})
		return err
	}

	if f.cfg.Templates == nil || f.cfg.Submitter == nil {
		return fail(errors.New("this access point does not accept job templates"))
	}
	tmpl, err := f.cfg.Templates.Template(ctx, f.cfg.Account, tref)
	if err != nil {
		return fail(err)
	}

	// The same bytes dropped twice are one submission. Without this a
	// retried upload -- a client that reconnects, or somebody who runs
	// the same command again -- silently doubles the jobs.
	if cluster, ok := f.seenUpload(tref, hexDigest); ok {
		f.putStatus(tref, name, uploadStatus{
			State: "duplicate", Template: tref.ID(), Cluster: cluster,
			Digest: hexDigest, At: time.Now(),
			Err: fmt.Sprintf("these exact bytes were already submitted as cluster %d", cluster),
		})
		return nil
	}

	sub, err := ParseCSV(bytes.NewReader(content), tmpl.Limits)
	if err != nil {
		return fail(err)
	}

	cluster, err := f.cfg.Submitter.SubmitRows(ctx, f.cfg.Account, tmpl, sub)
	if err != nil {
		return fail(err)
	}

	f.rememberUpload(tref, hexDigest, cluster)
	f.putStatus(tref, name, uploadStatus{
		State: "submitted", Template: tref.ID(), Rows: len(sub.Rows),
		Cluster: cluster, Digest: hexDigest, At: time.Now(),
	})
	f.logf("sftpjobs: %s submitted %d jobs as cluster %d from %s/%s",
		f.cfg.Account, len(sub.Rows), cluster, tref.ID(), name)
	return nil
}

// statusKey identifies one status entry.
type statusKey struct{ template, name string }

// putStatus records an outcome.
func (f *FS) putStatus(tref TemplateRef, name string, st uploadStatus) {
	if st.At.IsZero() {
		st.At = time.Now()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status == nil {
		f.status = map[statusKey]uploadStatus{}
	}
	f.status[statusKey{tref.ID(), name}] = st
}

// statusFor reads one back.
func (f *FS) statusFor(tref TemplateRef, name string) (uploadStatus, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.status[statusKey{tref.ID(), name}]
	return st, ok
}

// statusNames lists the entries for one template.
func (f *FS) statusNames(tref TemplateRef) []string {
	id := tref.ID()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.status))
	for k := range f.status {
		if k.template == id {
			out = append(out, k.name)
		}
	}
	return out
}

// seenUpload reports a digest already submitted in this session.
func (f *FS) seenUpload(tref TemplateRef, digest string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cluster, ok := f.submitted[statusKey{tref.ID(), digest}]
	return cluster, ok
}

func (f *FS) rememberUpload(tref TemplateRef, digest string, cluster int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submitted == nil {
		f.submitted = map[statusKey]int{}
	}
	f.submitted[statusKey{tref.ID(), digest}] = cluster
}
