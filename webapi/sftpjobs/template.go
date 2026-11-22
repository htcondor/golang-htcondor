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
	"fmt"
	"io/fs"

	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// A template is a submit description somebody drops a CSV onto, and it
// is the same template the web submit page and /api/v1/templates
// offer: templates.Template, from the one library.
//
// That reuse is the point. A template is authored once -- as a built-in,
// in the operator's catalog, or saved by a user -- and every surface
// offers it. The library already answers the two questions this tree
// asks, with the access rules attached: All(owner) is what somebody may
// use, Get(id, owner) resolves one id for them.
//
// The tree spells a template as one directory level:
//
//	/templates/hello-world/
//
// which is the library's own id, the same string the REST endpoints and
// the submit page use. There is no second namespace: who may write a
// template is Source and Owner on the template, and who may see it is
// Visibility, both decided in the library.

// TemplateSource is the part of *templates.Library this package needs.
//
// An interface only so a test can stand in a small catalog without a
// SQLite store; *templates.Library satisfies it as written, and nothing
// else is expected to implement it.
type TemplateSource interface {
	// All lists the templates account may use: their own, any another
	// user shared, the operator's catalog and the built-ins.
	All(owner string) []templates.Template
	// Get resolves one id for account. The library's precedence applies:
	// the caller's own first, then operator and built-in, then another
	// user's shared template.
	Get(id, owner string) (templates.Template, bool)
}

// Submitter turns a template and a CSV into one cluster of jobs.
type Submitter interface {
	// SubmitRows submits sub against t on account's behalf and returns
	// the cluster id.
	SubmitRows(ctx context.Context, account string, t templates.Template, sub *Submission) (int, error)
}

// ErrNoTemplate is what a lookup returns for a template an account
// cannot use, whether or not it exists.
//
// One error for both cases on purpose: a listing would not have shown
// them the template either, so "no such path" is the whole truth
// available to them.
var ErrNoTemplate = fmt.Errorf("sftpjobs: no such template: %w", fs.ErrNotExist)

// visibleTemplates is what the tree lists: one id per entry.
//
// All concatenates its sources rather than resolving them, so one id can
// appear twice -- a user's own template and another user's shared one of
// the same name, or a legacy row shadowing a built-in. A directory
// cannot hold two entries of one name, so the duplicates are dropped
// here.
//
// Only the id is used, which is why nothing is resolved at this point:
// WHICH template an id names is Get's business, and the tree asks it
// when somebody opens the directory rather than when it lists the level
// above. Resolving here as well would be a lookup per entry whose answer
// is thrown away.
func visibleTemplates(src TemplateSource, account string) []templates.Template {
	all := src.All(account)
	out := make([]templates.Template, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, t := range all {
		if t.ID == "" {
			continue
		}
		if _, dup := seen[t.ID]; dup {
			continue
		}
		seen[t.ID] = struct{}{}
		out = append(out, t)
	}
	return out
}

// lookupTemplate resolves one id, or reports a path that is not there.
func lookupTemplate(src TemplateSource, account, id string) (templates.Template, error) {
	if src == nil {
		return templates.Template{}, ErrNoTemplate
	}
	t, ok := src.Get(id, account)
	if !ok {
		return templates.Template{}, fmt.Errorf("%w: %s", ErrNoTemplate, id)
	}
	return t, nil
}
