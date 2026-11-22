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
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// The two files served inside a template's directory. Both are
// generated: job.sub is the template's submit description, README
// describes its columns. Neither is a file on disk anywhere.
const (
	SubmitFileName = "job.sub"
	ReadmeFileName = "README"
)

// templatesEnabled reports whether this view has a templates tree.
func (f *FS) templatesEnabled() bool {
	return f.cfg.Templates != nil && f.cfg.Submitter != nil
}

// listTemplates answers a directory listing in the templates tree.
func (f *FS) listTemplates(r ref, p string) (listerAt, error) {
	if !f.templatesEnabled() {
		return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}

	switch r.kind {
	case kindTemplates:
		// One directory per template the caller may use, whoever wrote
		// it. An empty listing says "no templates here", while a missing
		// /templates says "this access point has none at all", and those
		// are different answers.
		visible := visibleTemplates(f.cfg.Templates, f.cfg.Account)
		out := make(listerAt, 0, len(visible))
		for _, t := range visible {
			out = append(out, dirInfo(t.ID, time.Time{}))
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
		return out, nil

	case kindTemplate:
		tmpl, err := lookupTemplate(f.cfg.Templates, f.cfg.Account, r.tid)
		if err != nil {
			return nil, err
		}
		out := listerAt{
			fileInfo{name: SubmitFileName, size: int64(len(submitText(tmpl))), mode: 0o444},
			fileInfo{name: ReadmeFileName, size: int64(len(readmeText(tmpl))), mode: 0o444},
			dirInfo(submitDirName, time.Time{}),
			dirInfo(statusDirName, time.Time{}),
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
		return out, nil

	case kindSubmitDir:
		// Write-only by design. Listing back what was dropped would mean
		// keeping it, and the file has already become jobs -- what
		// happened to it is in status/.
		if _, err := lookupTemplate(f.cfg.Templates, f.cfg.Account, r.tid); err != nil {
			return nil, err
		}
		return listerAt{}, nil

	case kindStatusDir:
		if _, err := lookupTemplate(f.cfg.Templates, f.cfg.Account, r.tid); err != nil {
			return nil, err
		}
		names := f.statusNames(r.tid)
		sort.Strings(names)
		out := make(listerAt, 0, len(names))
		for _, name := range names {
			st, ok := f.statusFor(r.tid, name)
			if !ok {
				continue
			}
			text := st.Text()
			out = append(out, fileInfo{
				name: name, size: int64(len(text)), mode: 0o444, mod: st.At,
			})
		}
		return out, nil

	case kindTemplateFile, kindSubmitFile, kindStatusFile:
		return nil, &os.PathError{Op: "readdir", Path: p, Err: syscall.ENOTDIR}
	}
	return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
}

// statTemplates answers a stat in the templates tree.
func (f *FS) statTemplates(r ref, p string) (listerAt, error) {
	if !f.templatesEnabled() {
		return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}

	switch r.kind {
	case kindTemplates:
		return listerAt{dirInfo(templatesDir, time.Time{})}, nil

	case kindTemplate, kindSubmitDir, kindStatusDir:
		// Each one exists exactly when the template does, which is the
		// one thing worth asking the library.
		if _, err := lookupTemplate(f.cfg.Templates, f.cfg.Account, r.tid); err != nil {
			return nil, err
		}
		name := r.tid
		switch r.kind {
		case kindSubmitDir:
			name = submitDirName
		case kindStatusDir:
			name = statusDirName
		}
		return listerAt{dirInfo(name, time.Time{})}, nil

	case kindTemplateFile:
		text, err := f.templateFileText(r)
		if err != nil {
			return nil, err
		}
		return listerAt{fileInfo{name: r.name, size: int64(len(text)), mode: 0o444}}, nil

	case kindSubmitFile:
		// Nothing is kept, so there is nothing to stat. A client that
		// stats before writing gets ENOENT, which is what it would get
		// for any new file.
		return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}

	case kindStatusFile:
		st, ok := f.statusFor(r.tid, r.name)
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

// templateFileText reads job.sub or README.
func (f *FS) templateFileText(r ref) (string, error) {
	tmpl, err := lookupTemplate(f.cfg.Templates, f.cfg.Account, r.tid)
	if err != nil {
		return "", err
	}
	switch r.name {
	case SubmitFileName:
		return submitText(tmpl), nil
	case ReadmeFileName:
		return readmeText(tmpl), nil
	}
	return "", fmt.Errorf("%s: %w", r.name, fs.ErrNotExist)
}

// submitText is the template's submit description, newline-terminated.
//
// Served without the queue statement the submission appends, which is
// how the template is stored: see ValidateTemplate for why a template
// carrying its own is refused.
func submitText(t templates.Template) string {
	body := strings.TrimRight(t.Contents, "\n")
	if body == "" {
		return ""
	}
	return body + "\n"
}

// readmeText describes the template, from the template.
//
// Synthesized rather than stored, because everything worth saying is
// already structured: the name, the description, and the columns with
// their own descriptions. A hand-written file beside the submit
// description would be a second place for the column list to drift from
// the macros the body actually expands.
//
// It also carries the refusal a drop would hit, so somebody reads it
// here instead of discovering it in status/ after uploading.
func readmeText(t templates.Template) string {
	var b strings.Builder
	name := t.Name
	if name == "" {
		name = t.ID
	}
	fmt.Fprintf(&b, "%s\n", name)
	fmt.Fprintf(&b, "%s\n\n", strings.Repeat("=", len(name)))
	if t.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(t.Description))
	}

	if err := ValidateTemplate(t); err != nil {
		fmt.Fprintf(&b, "This template cannot be submitted by dropping a CSV here:\n\n  %s\n\n", err)
		return b.String()
	}

	cols := templates.ColumnNames(t.Columns)
	fmt.Fprintf(&b, "Drop a CSV into submit/ with these columns, in any order:\n\n")
	for _, c := range t.Columns {
		if c.Description != "" {
			fmt.Fprintf(&b, "  %-20s %s\n", c.Name, c.Description)
		} else {
			fmt.Fprintf(&b, "  %s\n", c.Name)
		}
	}
	fmt.Fprintf(&b, "\nOne job per row. For example:\n\n")
	fmt.Fprintf(&b, "  %s\n", strings.Join(cols, ","))
	row := make([]string, len(cols))
	for i, c := range cols {
		row[i] = "value-of-" + c
	}
	fmt.Fprintf(&b, "  %s\n\n", strings.Join(row, ","))
	fmt.Fprintf(&b, "What happened to an upload is in status/ under the same name.\n")
	fmt.Fprintf(&b, "Output comes back under /%s once the jobs finish.\n", outputsDir)
	return b.String()
}

// readTemplates answers a read in the templates tree.
func (f *FS) readTemplates(r ref, p string) (io.ReaderAt, error) {
	if !f.templatesEnabled() {
		return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}
	switch r.kind {
	case kindTemplateFile:
		text, err := f.templateFileText(r)
		if err != nil {
			return nil, err
		}
		return bytes.NewReader([]byte(text)), nil
	case kindStatusFile:
		st, ok := f.statusFor(r.tid, r.name)
		if !ok {
			return nil, &os.PathError{Op: "open", Path: p, Err: syscall.ENOENT}
		}
		return bytes.NewReader([]byte(st.Text())), nil
	case kindSubmitFile:
		// Write-only: the file became jobs and was not kept.
		return nil, &os.PathError{Op: "open", Path: p, Err: syscall.ENOENT}
	}
	return nil, &os.PathError{Op: "open", Path: p, Err: syscall.EISDIR}
}

// writeTemplates answers a write in the templates tree. Only a drop into
// submit/ is accepted.
func (f *FS) writeTemplates(r ref, p string) (io.WriterAt, error) {
	if !f.templatesEnabled() || r.kind != kindSubmitFile {
		return nil, &os.PathError{Op: "write", Path: p, Err: syscall.EROFS}
	}
	// Checked before a byte is accepted: uploading a megabyte only to be
	// told the template does not exist, or that it cannot take a CSV at
	// all, is a worse answer than refusing the open.
	tmpl, err := lookupTemplate(f.cfg.Templates, f.cfg.Account, r.tid)
	if err != nil {
		return nil, err
	}
	if err := ValidateTemplate(tmpl); err != nil {
		return nil, err
	}
	limits := f.cfg.Limits.withDefaults()
	return &upload{
		fs:   f,
		tid:  r.tid,
		name: r.name,
		max:  limits.MaxBytes + 1,
	}, nil
}
