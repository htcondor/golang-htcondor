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
	"io/fs"
	"strings"
)

// A template is a submit description somebody drops a CSV onto.
//
// Templates live in two namespaces, and the distinction is who may write
// them rather than who may use them:
//
//	system:blast          published by the access point, read-only
//	user:alice:myjob      alice's own
//
// Those are the identifiers -- what appears in a log, in config, and on
// the submitted job. The SFTP tree spells the same thing as directory
// levels:
//
//	/templates/system/blast/
//	/templates/user/alice/myjob/
//
// Two spellings for one thing, mapped in one place. The path form is
// what the tree uses because a ":" in a path component breaks Windows
// clients mirroring remote names onto local files, and is awkward in
// every shell; the identifier form is what everything else uses because
// it is one string.

// Namespaces.
const (
	// NamespaceSystem holds templates the access point publishes.
	NamespaceSystem = "system"
	// NamespaceUser holds an account's own.
	NamespaceUser = "user"
)

// TemplateRef names a template.
type TemplateRef struct {
	// Namespace is NamespaceSystem or NamespaceUser.
	Namespace string
	// Owner is the account a user template belongs to, and is empty for
	// a system template.
	Owner string
	// Name is the template's own name.
	Name string
}

// ID is the identifier form: "system:blast", "user:alice:myjob".
func (r TemplateRef) ID() string {
	if r.Namespace == NamespaceUser {
		return NamespaceUser + ":" + r.Owner + ":" + r.Name
	}
	return r.Namespace + ":" + r.Name
}

func (r TemplateRef) String() string { return r.ID() }

// Valid reports whether the ref is one this package will act on.
func (r TemplateRef) Valid() error {
	switch r.Namespace {
	case NamespaceSystem:
		if r.Owner != "" {
			return fmt.Errorf("a %s template has no owner, but this one names %q",
				NamespaceSystem, r.Owner)
		}
	case NamespaceUser:
		if err := checkTemplateName(r.Owner); err != nil {
			return fmt.Errorf("owner %q: %w", r.Owner, err)
		}
	default:
		return fmt.Errorf("%q is not a template namespace; use %q or %q",
			r.Namespace, NamespaceSystem, NamespaceUser)
	}
	return checkTemplateName(r.Name)
}

// checkTemplateName accepts a name that is one safe path component and
// one usable identifier piece.
//
// Refused rather than mapped to something safe: a name is how somebody
// addresses their template, and two names folding onto one would let one
// template answer for another.
func checkTemplateName(name string) error {
	if name == "" {
		return errors.New("the name is empty")
	}
	if len(name) > 64 {
		return errors.New("the name is longer than 64 characters")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%q is not a name", name)
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("%q contains %q; use letters, digits, '.', '_' or '-'", name, r)
		}
	}
	if strings.HasPrefix(name, "-") {
		// A leading dash reaches a command line as an option.
		return fmt.Errorf("%q starts with a dash", name)
	}
	return nil
}

// Template is a submit description and what goes with it.
type Template struct {
	Ref TemplateRef
	// Submit is the submit description's text. It is expected to end in
	// a `queue <vars> from` statement whose variables a dropped CSV's
	// header names.
	Submit string
	// Readme, when the template has one, is served beside it so somebody
	// can read what the CSV's columns are meant to be before guessing.
	Readme string
	// Limits bound a CSV dropped on this template. The zero value takes
	// the package defaults.
	Limits CSVLimits
}

// TemplateSource lists and reads templates.
//
// A hook, because where templates live is a deployment's choice: a
// directory the operator stages, a table, a git checkout. The account is
// passed so a source can answer "what may this person use" rather than
// "what exists", which is the question the tree asks.
type TemplateSource interface {
	// Templates lists what account may use: the system templates plus
	// that account's own.
	Templates(ctx context.Context, account string) ([]TemplateRef, error)
	// Template reads one. A ref naming a template the account may not
	// use returns an error wrapping fs.ErrNotExist, so the tree reports
	// a missing path rather than a refusal -- which is also all a
	// listing would have shown them.
	Template(ctx context.Context, account string, ref TemplateRef) (Template, error)
}

// Submitter turns a template and a CSV into one cluster of jobs.
type Submitter interface {
	// SubmitRows submits sub against t on account's behalf and returns
	// the cluster id.
	SubmitRows(ctx context.Context, account string, t Template, sub *Submission) (int, error)
}

// ErrNoTemplate is what a source returns for a template an account
// cannot use, whether or not it exists.
var ErrNoTemplate = fmt.Errorf("sftpjobs: no such template: %w", fs.ErrNotExist)

// refForAccount narrows a ref to what the caller may name.
//
// A user template is only ever the CALLER's own: the tree shows
// /templates/user/<account>/ and nothing else, so a path naming another
// account is not a refusal to explain but a path that does not exist.
// Enforced here rather than in the source, so a source cannot forget.
func refForAccount(account string, ref TemplateRef) error {
	if err := ref.Valid(); err != nil {
		return err
	}
	if ref.Namespace == NamespaceUser && ref.Owner != account {
		return fmt.Errorf("%w: %s", ErrNoTemplate, ref)
	}
	return nil
}
