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

// principalSet names who may request an entitlement, by group or by
// name: the HTTP_API_<X>_GROUPS and HTTP_API_<X>_USERS pair.
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
type principalSet struct {
	groups *groupSet
	// users is swapped whole, for the reason groupSet documents: a
	// reconfigure rewrites it from another goroutine while logins read
	// it, and a reader must see one list or the other.
	users atomic.Pointer[[]string]
}

// newPrincipalSet builds a set from the two configured lists.
func newPrincipalSet(groups, users string) *principalSet {
	p := &principalSet{groups: newGroupSet(groups)}
	p.setUsers(users)
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
// the entitlement, and must not read as unconfigured -- for these
// settings "unconfigured" means nobody may request the scope, so
// getting this wrong would silently ignore a list the operator wrote.
func (p *principalSet) configured() bool {
	return p.groups.configured() || len(p.userList()) > 0
}

// allows reports whether this caller may request the entitlement:
// a member of a named group, OR a named user.
//
// An empty set allows NOBODY. These are cross-user privileges, where
// "nothing configured" has to mean nobody rather than everybody; this
// is groupSet.grants, not groupSet.allows.
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
	if p.groups.grants(userGroups) {
		return true
	}
	if actor == "" {
		return false
	}
	for _, u := range p.userList() {
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
