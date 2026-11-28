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

package httpserver

import (
	"strings"
	"sync/atomic"

	"github.com/htcondor/golang-htcondor/authz"
)

// principalSet names who passes a gate, by group or by name: the
// HTTP_API_<X>_GROUPS and HTTP_API_<X>_USERS pair.
//
// The gate is either an entitlement somebody may request
// (anyuser:read, anyuser:write) or a surface they may reach (/mcp, the
// web interface). The two differ only in what an empty set means, which
// is the section below.
//
// Two halves rather than one list because they answer different
// questions. A group list asks what a caller is a member of; a user
// list asks who the caller is. Stuffing an identity into a group set
// would make "alice" match a group called alice, which is a different
// statement than the operator wrote.
//
// Why a user half exists at all: with HTTP_API_GROUP_SOURCE = token
// and no groups claim, the group half cannot decide anything, and a
// deployment should not have to invent a directory group to name one
// person. See design_notes/ENTITLEMENT_VOCABULARY.md.
//
// Membership confers the ability to REQUEST the entitlement, never the
// entitlement itself: a credential holds a scope only by asking for it
// and being granted it. This is what separates these settings from
// MCP_ADMIN_USERS, which hands its power out directly.
//
// # What an EMPTY set answers
//
// The one thing these settings do not agree on, and the one that has to
// be impossible to get wrong. Two kinds of gate are configured this way:
//
//   - a cross-user ENTITLEMENT (anyuser:read, anyuser:write), where
//     "nobody configured" has to mean nobody. Unset is off.
//   - an opt-in RESTRICTION (who may reach /mcp, who may read or write
//     through it, who may open the web interface), where unset has
//     always meant "no requirement" -- that is, everybody. Most
//     deployments configure none of them, and answering "nobody" there
//     would lock every user out of the surface on upgrade.
//
// The answer is therefore a property of the SET, fixed by the
// constructor that built it, not a choice the caller makes: newPrincipalSet
// closes an empty set and newOptInPrincipalSet opens one. A call site
// writes allows() and cannot pick the wrong polarity, because it has no
// polarity to pick -- which is deliberate. A second method beside allows()
// would read identically at every one of the dozen call sites and mean the
// opposite at half of them.
//
// Deny is the unnamed default, so a gate added without thinking about this
// refuses rather than admits.
type principalSet struct {
	groups *groupSet
	// users is swapped whole, for the reason groupSet documents: a
	// reconfigure rewrites it from another goroutine while logins read
	// it, and a reader must see one list or the other.
	users atomic.Pointer[[]string]
	// openWhenUnconfigured is what an empty set answers. Immutable
	// after construction: a reconfigure changes who is named, never
	// what naming nobody means.
	openWhenUnconfigured bool
}

// newPrincipalSet builds a set naming who may request an entitlement.
// An empty one allows NOBODY; see the type comment.
func newPrincipalSet(groups, users string) *principalSet {
	p := &principalSet{groups: newGroupSet(groups)}
	p.setUsers(users)
	return p
}

// newOptInPrincipalSet builds a set that RESTRICTS a surface everyone
// may otherwise reach. An empty one allows EVERYBODY, because unset
// means no requirement was configured rather than that nobody passes.
//
// Spelled out at the two kinds of gate rather than defaulted, so that
// the open answer is always somebody's decision.
func newOptInPrincipalSet(groups, users string) *principalSet {
	p := newPrincipalSet(groups, users)
	p.openWhenUnconfigured = true
	return p
}

// setGroups replaces the group half. Safe during requests.
func (p *principalSet) setGroups(raw string) { p.groups.set(raw) }

// setUsers replaces the user half. Safe during requests.
//
// Parsed the same way as a group list, so an operator who has learned
// that a bracketed value is a JSON array does not have to learn a
// second rule for the setting beside it.
func (p *principalSet) setUsers(raw string) {
	users := parseGroupList(raw)
	p.users.Store(&users)
}

// userList returns the configured identities.
func (p *principalSet) userList() []string {
	if p == nil {
		return nil
	}
	u := p.users.Load()
	if u == nil {
		return nil
	}
	return *u
}

// configured reports whether EITHER half names anybody.
//
// Either, not both: a deployment that names only users has configured
// the gate, and must not read as unconfigured -- an unconfigured set is
// the one whose answer is the polarity above, so getting this wrong
// would silently ignore a list the operator wrote.
//
// Nil-safe: a set nothing built names nobody.
func (p *principalSet) configured() bool {
	if p == nil {
		return false
	}
	return p.groups.configured() || len(p.userList()) > 0
}

// allows reports whether this caller passes the gate: a member of a
// named group, OR a named user.
//
// An empty set answers openWhenUnconfigured -- see the type comment.
// Both halves are read once, before either is consulted, so a
// reconfigure in between cannot make "is anything configured" and "does
// it name this caller" disagree and produce an answer neither the old
// configuration nor the new one gives.
//
// A nil set names nobody and has no polarity to consult, so it refuses.
// Nothing builds one in a running server; a Handler assembled without
// one is a bug, and refusing is how it shows up.
//
// Identity is compared with authz.SameUser rather than == or
// EqualFold, so "alice@Example.ORG" in a config file matches the
// lower-cased domain HTCondor reports -- and a bare "alice" matches
// only a bare "alice", never "alice@uid.domain". That asymmetry is
// deliberate and is the trap MCP_ADMIN_USERS set; it is documented at
// the knob so it is read rather than discovered.
func (p *principalSet) allows(actor string, userGroups []string) bool {
	if p == nil {
		return false
	}
	groups, users := p.groups.list(), p.userList()
	if len(groups) == 0 && len(users) == 0 {
		return p.openWhenUnconfigured
	}
	if groupListMatches(groups, userGroups) {
		return true
	}
	if actor == "" {
		return false
	}
	for _, u := range users {
		if authz.SameUser(actor, u) {
			return true
		}
	}
	return false
}

// String renders both halves for logs and operator-facing messages.
func (p *principalSet) String() string {
	if p == nil {
		return ""
	}
	var parts []string
	if g := p.groups.String(); g != "" {
		parts = append(parts, "groups: "+g)
	}
	if u := p.userList(); len(u) > 0 {
		parts = append(parts, "users: "+strings.Join(u, ", "))
	}
	return strings.Join(parts, "; ")
}
