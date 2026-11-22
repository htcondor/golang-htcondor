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
	"path/filepath"
	"sort"
	"strings"
)

// Names inside a template directory.
const (
	// SubmitFileName is the submit description.
	SubmitFileName = "job.sub"
	// ReadmeFileName, when present, is served beside it.
	ReadmeFileName = "README"
)

// maxTemplateFileBytes bounds what is read from a template directory. A
// submit description is a page of text; anything of this size is a
// mistake in the staging directory, not a template.
const maxTemplateFileBytes = 1 << 20

// DirTemplates serves system templates from a directory the operator
// stages:
//
//	<Dir>/blast/job.sub
//	<Dir>/blast/README
//
// Read-only, and only the system namespace. A user's own templates need
// a writable surface, which is a separate thing; until there is one, a
// source can be composed around this.
type DirTemplates struct {
	// Dir holds one subdirectory per template. Empty means no system
	// templates, which is not an error.
	Dir string
	// Limits bound a CSV dropped on any of these.
	Limits CSVLimits
}

// Templates implements TemplateSource.
//
// A subdirectory without a job.sub is skipped rather than reported: an
// operator mid-edit, or a stray directory, should not make the whole
// listing fail for everybody.
func (d DirTemplates) Templates(_ context.Context, _ string) ([]TemplateRef, error) {
	if d.Dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A configured directory that does not exist yet is an
			// empty set of templates, not a failure: it is the normal
			// state before an operator stages the first one.
			return nil, nil
		}
		return nil, fmt.Errorf("sftpjobs: reading the template directory: %w", err)
	}

	out := make([]TemplateRef, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if checkTemplateName(name) != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(d.Dir, name, SubmitFileName)); err != nil {
			continue
		}
		out = append(out, TemplateRef{Namespace: NamespaceSystem, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Template implements TemplateSource.
func (d DirTemplates) Template(_ context.Context, account string, ref TemplateRef) (Template, error) {
	if err := refForAccount(account, ref); err != nil {
		return Template{}, err
	}
	if ref.Namespace != NamespaceSystem || d.Dir == "" {
		return Template{}, fmt.Errorf("%w: %s", ErrNoTemplate, ref)
	}

	// ref.Name has been through checkTemplateName, so it is one path
	// component with no separator and no "..". The join is still checked
	// below, because this is where a name becomes a real path.
	dir := filepath.Join(d.Dir, ref.Name)
	if !strings.HasPrefix(dir, d.Dir+string(filepath.Separator)) {
		return Template{}, fmt.Errorf("%w: %s", ErrNoTemplate, ref)
	}

	submit, err := readTemplateFile(filepath.Join(dir, SubmitFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Template{}, fmt.Errorf("%w: %s", ErrNoTemplate, ref)
		}
		return Template{}, err
	}

	t := Template{Ref: ref, Submit: submit, Limits: d.Limits}
	// A README is optional and its absence is not a problem.
	if readme, err := readTemplateFile(filepath.Join(dir, ReadmeFileName)); err == nil {
		t.Readme = readme
	}
	return t, nil
}

// readTemplateFile reads one bounded text file.
func readTemplateFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path is built from a validated template name
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	// One byte past the cap, so going over is detectable rather than
	// silently truncating a submit description -- which would submit
	// something other than what the operator wrote.
	b, err := io.ReadAll(io.LimitReader(f, maxTemplateFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("sftpjobs: reading %s: %w", filepath.Base(path), err)
	}
	if len(b) > maxTemplateFileBytes {
		return "", fmt.Errorf("sftpjobs: %s is larger than %d bytes",
			filepath.Base(path), maxTemplateFileBytes)
	}
	return string(b), nil
}
