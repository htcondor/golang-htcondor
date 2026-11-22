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
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"syscall"
	"time"
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
	ctx := f.cfg.Context

	switch r.kind {
	case kindTemplates:
		// Both namespaces always appear, even with nothing in them: an
		// empty directory says "no templates here", while a missing one
		// says "this access point has no templates at all", and those
		// are different answers.
		return listerAt{
			dirInfo(NamespaceSystem, time.Time{}),
			dirInfo(NamespaceUser, time.Time{}),
		}, nil

	case kindNamespace:
		if r.tref.Namespace == NamespaceUser {
			// Only ever your own, so the level below has exactly one
			// entry. Listing other accounts would be a directory of
			// names nobody may open.
			return listerAt{dirInfo(f.cfg.Account, time.Time{})}, nil
		}
		return f.templateDirs(ctx, NamespaceSystem, "")

	case kindUserOwner:
		if r.tref.Owner != f.cfg.Account {
			return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
		}
		return f.templateDirs(ctx, NamespaceUser, f.cfg.Account)

	case kindTemplate:
		tmpl, err := f.cfg.Templates.Template(ctx, f.cfg.Account, r.tref)
		if err != nil {
			return nil, err
		}
		out := listerAt{
			fileInfo{name: SubmitFileName, size: int64(len(tmpl.Submit)), mode: 0o444},
			dirInfo(submitDirName, time.Time{}),
			dirInfo(statusDirName, time.Time{}),
		}
		if tmpl.Readme != "" {
			out = append(out, fileInfo{
				name: ReadmeFileName, size: int64(len(tmpl.Readme)), mode: 0o444,
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
		return out, nil

	case kindSubmitDir:
		// Write-only by design. Listing back what was dropped would mean
		// keeping it, and the file has already become jobs -- what
		// happened to it is in status/.
		if _, err := f.cfg.Templates.Template(ctx, f.cfg.Account, r.tref); err != nil {
			return nil, err
		}
		return listerAt{}, nil

	case kindStatusDir:
		if _, err := f.cfg.Templates.Template(ctx, f.cfg.Account, r.tref); err != nil {
			return nil, err
		}
		names := f.statusNames(r.tref)
		sort.Strings(names)
		out := make(listerAt, 0, len(names))
		for _, name := range names {
			st, ok := f.statusFor(r.tref, name)
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

// templateDirs lists one namespace's templates as directories.
func (f *FS) templateDirs(ctx context.Context, namespace, owner string) (listerAt, error) {
	refs, err := f.cfg.Templates.Templates(ctx, f.cfg.Account)
	if err != nil {
		return nil, err
	}
	out := listerAt{}
	for _, tr := range refs {
		if tr.Namespace != namespace || tr.Owner != owner {
			continue
		}
		out = append(out, dirInfo(tr.Name, time.Time{}))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// statTemplates answers a stat in the templates tree.
func (f *FS) statTemplates(r ref, p string) (listerAt, error) {
	if !f.templatesEnabled() {
		return nil, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}
	ctx := f.cfg.Context

	switch r.kind {
	case kindTemplates:
		return listerAt{dirInfo(templatesDir, time.Time{})}, nil
	case kindNamespace:
		return listerAt{dirInfo(r.tref.Namespace, time.Time{})}, nil
	case kindUserOwner:
		if r.tref.Owner != f.cfg.Account {
			return nil, &os.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}
		}
		return listerAt{dirInfo(r.tref.Owner, time.Time{})}, nil

	case kindTemplate, kindSubmitDir, kindStatusDir:
		// Each one exists exactly when the template does, which is the
		// one thing worth asking the source.
		if _, err := f.cfg.Templates.Template(ctx, f.cfg.Account, r.tref); err != nil {
			return nil, err
		}
		name := r.tref.Name
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
		st, ok := f.statusFor(r.tref, r.name)
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
	tmpl, err := f.cfg.Templates.Template(f.cfg.Context, f.cfg.Account, r.tref)
	if err != nil {
		return "", err
	}
	switch r.name {
	case SubmitFileName:
		return tmpl.Submit, nil
	case ReadmeFileName:
		if tmpl.Readme == "" {
			return "", fmt.Errorf("%s: %w", ReadmeFileName, fs.ErrNotExist)
		}
		return tmpl.Readme, nil
	}
	return "", fmt.Errorf("%s: %w", r.name, fs.ErrNotExist)
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
		st, ok := f.statusFor(r.tref, r.name)
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
	// told the template does not exist is a worse answer than refusing
	// the open.
	tmpl, err := f.cfg.Templates.Template(f.cfg.Context, f.cfg.Account, r.tref)
	if err != nil {
		return nil, err
	}
	limits := tmpl.Limits.withDefaults()
	return &upload{
		fs:   f,
		tref: r.tref,
		name: r.name,
		max:  limits.MaxBytes + 1,
	}, nil
}
