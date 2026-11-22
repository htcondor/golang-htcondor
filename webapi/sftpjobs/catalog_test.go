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
	"testing"

	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// fakeCatalog stands in for *templates.Library.
//
// It reproduces the two library behaviours this package depends on and
// nothing else: All concatenates its sources without resolving them, so
// one id can appear twice, and Get applies the precedence -- the
// caller's own, then public, then another user's shared one. Those are
// the rules visibleTemplates is written against; a map would not have
// them.
type fakeCatalog struct {
	public []templates.Template // built-in or operator catalog
	user   []templates.Template // rows in the user store
}

func (c fakeCatalog) All(owner string) []templates.Template {
	out := make([]templates.Template, 0, len(c.public)+len(c.user))
	for _, t := range c.user {
		if t.Owner == owner || t.Visibility == templates.VisibilityShared {
			out = append(out, t)
		}
	}
	out = append(out, c.public...)
	return out
}

func (c fakeCatalog) Get(id, owner string) (templates.Template, bool) {
	var shared *templates.Template
	for i, t := range c.user {
		if t.ID != id {
			continue
		}
		if t.Owner == owner {
			return t, true
		}
		if t.Visibility == templates.VisibilityShared && shared == nil {
			shared = &c.user[i]
		}
	}
	for _, t := range c.public {
		if t.ID == id {
			return t, true
		}
	}
	if shared != nil {
		return *shared, true
	}
	return templates.Template{}, false
}

// blastTemplate is the fixture: a template this service will accept.
//
// Every rule in ValidateTemplate is satisfied deliberately -- declared
// columns, no queue statement of its own, transfer_executable = false,
// no input files -- because a fixture that failed validation would make
// the tree tests pass for the wrong reason.
func blastTemplate() templates.Template {
	return templates.Template{
		ID:          "blast",
		Name:        "BLAST a read set",
		Description: "Aligns one read file against the reference.",
		Columns: []templates.Column{
			{Name: "sample", Description: "the sample's name"},
			{Name: "reads", Description: "the read file, reachable from the execute node"},
		},
		Contents: "executable = /usr/bin/blastn\n" +
			"transfer_executable = false\n" +
			"arguments = -query $(reads) -out $(sample).out\n",
		Source: templates.SourceBuiltin,
	}
}

// goodTemplate is blastTemplate under the name the submit-building
// tests use.
func goodTemplate() templates.Template { return blastTemplate() }

// TestTheFixtureIsAcceptable guards the fixture itself: every tree test
// below assumes a drop on it would be accepted, and a fixture that
// quietly stopped validating would turn those into tests of the refusal
// path without saying so.
func TestTheFixtureIsAcceptable(t *testing.T) {
	if err := ValidateTemplate(blastTemplate()); err != nil {
		t.Fatalf("the fixture template is refused: %v", err)
	}
}
