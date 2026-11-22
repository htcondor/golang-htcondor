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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateRefID(t *testing.T) {
	for _, tc := range []struct {
		ref  TemplateRef
		want string
	}{
		{TemplateRef{Namespace: NamespaceSystem, Name: "blast"}, "system:blast"},
		{TemplateRef{Namespace: NamespaceUser, Owner: "alice", Name: "myjob"}, "user:alice:myjob"},
	} {
		if got := tc.ref.ID(); got != tc.want {
			t.Errorf("ID() = %q, want %q", got, tc.want)
		}
	}
}

func TestTemplateRefValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  TemplateRef
		ok   bool
	}{
		{"system", TemplateRef{Namespace: NamespaceSystem, Name: "blast"}, true},
		{"user", TemplateRef{Namespace: NamespaceUser, Owner: "alice", Name: "myjob"}, true},
		{"dots and dashes", TemplateRef{Namespace: NamespaceSystem, Name: "a.b-c_1"}, true},
		{"system with an owner", TemplateRef{Namespace: NamespaceSystem, Owner: "alice", Name: "x"}, false},
		{"user without an owner", TemplateRef{Namespace: NamespaceUser, Name: "x"}, false},
		{"unknown namespace", TemplateRef{Namespace: "other", Name: "x"}, false},
		{"no name", TemplateRef{Namespace: NamespaceSystem}, false},
		{"traversal", TemplateRef{Namespace: NamespaceSystem, Name: ".."}, false},
		{"separator", TemplateRef{Namespace: NamespaceSystem, Name: "a/b"}, false},
		{"leading dash", TemplateRef{Namespace: NamespaceSystem, Name: "-rf"}, false},
		{"space", TemplateRef{Namespace: NamespaceSystem, Name: "a b"}, false},
		{"colon", TemplateRef{Namespace: NamespaceSystem, Name: "a:b"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Valid()
			if tc.ok && err != nil {
				t.Errorf("Valid() = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Error("Valid() accepted it")
			}
		})
	}
}

// TestRefForAccountKeepsUserTemplatesPrivate: the tree shows
// /templates/user/<you>/ and nothing else, so another account's
// template is a path that does not exist rather than a refusal to
// explain.
func TestRefForAccountKeepsUserTemplatesPrivate(t *testing.T) {
	mine := TemplateRef{Namespace: NamespaceUser, Owner: "alice", Name: "x"}
	if err := refForAccount("alice", mine); err != nil {
		t.Errorf("own template refused: %v", err)
	}
	theirs := TemplateRef{Namespace: NamespaceUser, Owner: "bob", Name: "x"}
	err := refForAccount("alice", theirs)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("another account's template = %v, want it to wrap fs.ErrNotExist", err)
	}
	// A system template belongs to nobody and is available to everybody.
	if err := refForAccount("alice", TemplateRef{Namespace: NamespaceSystem, Name: "blast"}); err != nil {
		t.Errorf("system template refused: %v", err)
	}
}

func stageTemplate(t *testing.T, dir, name, submit, readme string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if submit != "" {
		if err := os.WriteFile(filepath.Join(d, SubmitFileName), []byte(submit), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	if readme != "" {
		if err := os.WriteFile(filepath.Join(d, ReadmeFileName), []byte(readme), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
}

func TestDirTemplatesListsAndReads(t *testing.T) {
	dir := t.TempDir()
	stageTemplate(t, dir, "blast", "executable = /usr/bin/blastn\nqueue sample from\n", "columns: sample\n")
	stageTemplate(t, dir, "align", "executable = /usr/bin/bwa\nqueue sample from\n", "")
	// Skipped: no job.sub.
	stageTemplate(t, dir, "halfdone", "", "just a readme\n")
	// Skipped: not a directory.
	if err := os.WriteFile(filepath.Join(dir, "loose.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	src := DirTemplates{Dir: dir}
	refs, err := src.Templates(context.Background(), "alice")
	if err != nil {
		t.Fatalf("Templates: %v", err)
	}
	var names []string
	for _, r := range refs {
		if r.Namespace != NamespaceSystem {
			t.Errorf("%s is not in the system namespace", r)
		}
		names = append(names, r.Name)
	}
	if len(names) != 2 || names[0] != "align" || names[1] != "blast" {
		t.Fatalf("templates = %v, want [align blast] sorted", names)
	}

	got, err := src.Template(context.Background(), "alice",
		TemplateRef{Namespace: NamespaceSystem, Name: "blast"})
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	if !strings.Contains(got.Submit, "blastn") {
		t.Errorf("submit text = %q", got.Submit)
	}
	if !strings.Contains(got.Readme, "columns") {
		t.Errorf("readme = %q", got.Readme)
	}
	// A template without a README is fine.
	got, err = src.Template(context.Background(), "alice",
		TemplateRef{Namespace: NamespaceSystem, Name: "align"})
	if err != nil {
		t.Fatalf("Template: %v", err)
	}
	if got.Readme != "" {
		t.Errorf("readme = %q, want empty", got.Readme)
	}
}

func TestDirTemplatesMissingThings(t *testing.T) {
	dir := t.TempDir()
	stageTemplate(t, dir, "blast", "executable = /bin/true\n", "")
	src := DirTemplates{Dir: dir}
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		ref  TemplateRef
	}{
		{"unknown name", TemplateRef{Namespace: NamespaceSystem, Name: "nope"}},
		{"a user template", TemplateRef{Namespace: NamespaceUser, Owner: "alice", Name: "blast"}},
		{"traversal in the name", TemplateRef{Namespace: NamespaceSystem, Name: "../blast"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := src.Template(ctx, "alice", tc.ref); err == nil {
				t.Fatal("accepted")
			} else if !errors.Is(err, fs.ErrNotExist) && tc.name != "traversal in the name" {
				t.Errorf("error = %v, want it to wrap fs.ErrNotExist", err)
			}
		})
	}
}

// TestDirTemplatesEmptyAndAbsent: a configured directory that does not
// exist yet is the normal state before an operator stages the first
// template, not a failure that should break every listing.
func TestDirTemplatesEmptyAndAbsent(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ name, dir string }{
		{"not configured", ""},
		{"does not exist", filepath.Join(t.TempDir(), "never", "made")},
		{"empty", t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := DirTemplates{Dir: tc.dir}.Templates(ctx, "alice")
			if err != nil {
				t.Fatalf("Templates: %v", err)
			}
			if len(refs) != 0 {
				t.Errorf("got %d templates, want none", len(refs))
			}
		})
	}
}

// TestDirTemplatesRefusesAnOversizedSubmitFile: truncating one would
// submit something other than what the operator wrote.
func TestDirTemplatesRefusesAnOversizedSubmitFile(t *testing.T) {
	dir := t.TempDir()
	stageTemplate(t, dir, "huge", strings.Repeat("x = 1\n", maxTemplateFileBytes/3), "")

	_, err := DirTemplates{Dir: dir}.Template(context.Background(), "alice",
		TemplateRef{Namespace: NamespaceSystem, Name: "huge"})
	if err == nil {
		t.Fatal("accepted an oversized submit description")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("error = %v, want a size refusal", err)
	}
}
