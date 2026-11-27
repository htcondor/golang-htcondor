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
	"slices"
	"testing"
)

// The rule that makes these settings safe to add: an unconfigured
// cross-user entitlement belongs to nobody. groupSet.allows answers the
// opposite way for the opt-in gates beside it, so getting this wrong
// would hand every authenticated caller every user's jobs on a server
// that had configured nothing -- which is most of them.
func TestUnconfiguredPrincipalSetAllowsNobody(t *testing.T) {
	p := newPrincipalSet("", "")
	if p.configured() {
		t.Error("an empty set reports itself configured")
	}
	if p.allows("alice@example.org", []string{"anybody", "admins"}) {
		t.Error("an empty set allowed somebody")
	}
	// Including a caller with no groups at all, which is the shape a
	// token-sourced deployment produces.
	if p.allows("alice@example.org", nil) {
		t.Error("an empty set allowed a caller carrying no groups")
	}
}

// The user half is the reason principalSet exists: with
// HTTP_API_GROUP_SOURCE = token and no groups claim, the group half can
// decide nothing, and naming one person must not require inventing a
// directory group for them.
func TestUserHalfDecidesWithoutGroups(t *testing.T) {
	p := newPrincipalSet("", "alice@example.org")
	if !p.configured() {
		t.Error("a set naming only users reports itself unconfigured, so it would be skipped entirely")
	}
	if !p.allows("alice@example.org", nil) {
		t.Error("the named user was refused although no group was required")
	}
	if p.allows("bob@example.org", nil) {
		t.Error("a user nobody named was allowed")
	}
}

// Identity comparison is authz.SameUser, not == and not EqualFold.
// Each case here is a mistake an operator can make in a config file,
// and the asymmetry in the last two is deliberate: it is the trap
// MCP_ADMIN_USERS set, and the knob documentation describes it.
func TestUserHalfComparesIdentitiesLikeHTCondor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured string
		actor      string
		want       bool
	}{
		{"exact", "alice@example.org", "alice@example.org", true},
		{"domain case folds", "alice@Example.ORG", "alice@example.org", true},
		{"user part does not fold", "Alice@example.org", "alice@example.org", false},
		{"a bare name does not match a qualified one", "alice", "alice@example.org", false},
		{"a qualified name does not match a bare one", "alice@example.org", "alice", false},
		{"bare matches bare", "alice", "alice", true},
		{"a different user", "alice@example.org", "mallory@example.org", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPrincipalSet("", tc.configured)
			if got := p.allows(tc.actor, nil); got != tc.want {
				t.Errorf("%q against configured %q: got %v, want %v",
					tc.actor, tc.configured, got, tc.want)
			}
		})
	}
}

// A caller with no asserted identity must not match a user entry.
// authz.SameUser("", "") is true, so an empty entry beside an empty
// actor would grant a cross-user entitlement to a caller who proved
// nothing.
//
// The list is stored directly rather than parsed, because
// parseGroupList drops empty entries today and the guard is therefore
// unreachable through setUsers. That is exactly why this test writes
// around the parser: the property has to survive a change to how lists
// are read, and a test that went through the parser would pass whether
// the guard existed or not.
func TestEmptyActorMatchesNoUserEntry(t *testing.T) {
	p := newPrincipalSet("", "")
	entries := []string{""}
	p.users.Store(&entries)

	if p.allows("", nil) {
		t.Error("an empty actor matched an empty user entry")
	}
	if p.allows("alice@example.org", nil) {
		t.Error("a set holding only an empty entry allowed a named user")
	}
}

// The parser is what makes the guard above unreachable today. Stated as
// its own check so that a change to it is a failure here rather than a
// silent promotion of dead code to load-bearing code.
func TestConfiguredUserListNeverHoldsAnEmptyEntry(t *testing.T) {
	for _, raw := range []string{"", ",", " , ", "alice@example.org,", `["","alice@example.org"]`, "[]"} {
		p := newPrincipalSet("", raw)
		for _, u := range p.userList() {
			if u == "" {
				t.Errorf("parsing %q produced an empty user entry", raw)
			}
		}
	}
}

// Either half alone decides, and either half alone counts as
// configured.
func TestEitherHalfGrants(t *testing.T) {
	p := newPrincipalSet("condor-admins", "alice@example.org")
	if !p.allows("nobody@example.org", []string{"condor-admins"}) {
		t.Error("group membership did not grant")
	}
	if !p.allows("alice@example.org", []string{"unrelated"}) {
		t.Error("being named did not grant")
	}
	if p.allows("mallory@example.org", []string{"unrelated"}) {
		t.Error("neither half matched and it granted anyway")
	}
}

// Group membership folds case, as it does everywhere else in this
// package: Unix and directory group names do not reliably agree on it.
func TestGroupHalfFoldsCase(t *testing.T) {
	if !newPrincipalSet("Condor-Admins", "").allows("", []string{"condor-admins"}) {
		t.Error("group membership did not fold case")
	}
}

// A JSON array spells a group name containing a comma. The user half
// parses the same way, so an operator does not have to learn a second
// rule for the setting beside the one they just learned.
func TestBothHalvesAcceptAJSONArray(t *testing.T) {
	p := newPrincipalSet(`["a,b","c"]`, `["alice@example.org","bob@example.org"]`)
	if !p.allows("", []string{"a,b"}) {
		t.Error("the group half did not read a JSON array")
	}
	if !p.allows("bob@example.org", nil) {
		t.Error("the user half did not read a JSON array")
	}
}

// Each half is replaced independently, so setting one on reconfigure
// does not silently clear the other.
func TestSettingOneHalfLeavesTheOther(t *testing.T) {
	p := newPrincipalSet("condor-admins", "alice@example.org")
	p.setGroups("pool-admins")
	if !p.allows("alice@example.org", nil) {
		t.Error("replacing the group half dropped the user half")
	}
	if p.allows("", []string{"condor-admins"}) {
		t.Error("the old group list survived its replacement")
	}
	p.setUsers("bob@example.org")
	if !p.allows("", []string{"pool-admins"}) {
		t.Error("replacing the user half dropped the group half")
	}
	if p.allows("alice@example.org", nil) {
		t.Error("the old user list survived its replacement")
	}
}

// Emptying a half revokes what it granted, which is what an operator
// expects of a privilege they just removed somebody from.
func TestEmptyingAHalfRevokes(t *testing.T) {
	p := newPrincipalSet("condor-admins", "alice@example.org")
	p.setUsers("")
	if p.allows("alice@example.org", nil) {
		t.Error("a user kept the privilege after being removed from the list")
	}
	p.setGroups("")
	if p.configured() {
		t.Error("a set with both halves emptied still reports itself configured")
	}
	if p.allows("alice@example.org", []string{"condor-admins"}) {
		t.Error("an emptied set still grants")
	}
}

// The two entitlements are independent in both directions. Neither
// implies the other: seeing every job and being able to remove every
// job are different privileges, and this is the whole reason there are
// two pairs of settings rather than one.
func TestReadAndWriteEntitlementsDoNotImplyEachOther(t *testing.T) {
	h := &Handler{
		anyUserRead:  newPrincipalSet("readers", ""),
		anyUserWrite: newPrincipalSet("writers", ""),
	}
	reader := []string{"readers"}
	writer := []string{"writers"}

	if got := h.getScopesForGroups("", reader, []string{scopeAnyUserWrite}); slices.Contains(got, scopeAnyUserWrite) {
		t.Error("the read group was granted anyuser:write")
	}
	if got := h.getScopesForGroups("", writer, []string{scopeAnyUserRead}); slices.Contains(got, scopeAnyUserRead) {
		t.Error("the write group was granted anyuser:read")
	}
	if got := h.getScopesForGroups("", reader, []string{scopeAnyUserRead}); !slices.Contains(got, scopeAnyUserRead) {
		t.Error("the read group was refused anyuser:read")
	}
	if got := h.getScopesForGroups("", writer, []string{scopeAnyUserWrite}); !slices.Contains(got, scopeAnyUserWrite) {
		t.Error("the write group was refused anyuser:write")
	}
}

// The older spellings answer to the same policy. An obsolete name must
// not be a second, laxer door to the privilege it names -- which is
// exactly what it would become if the switch in getScopesForGroups kept
// a case pointing at a set the new settings no longer feed.
func TestObsoleteScopeSpellingsAnswerToTheNewSettings(t *testing.T) {
	h := &Handler{
		anyUserRead:  newPrincipalSet("", "alice@example.org"),
		anyUserWrite: newPrincipalSet("", "bob@example.org"),
	}
	for _, tc := range []struct {
		scope string
		actor string
	}{
		{"mcp:admin", "alice@example.org"},
		{scopeAnyUserRead, "alice@example.org"},
		{"mcp:superuser", "bob@example.org"},
		{scopeAnyUserWrite, "bob@example.org"},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			if got := h.getScopesForGroups(tc.actor, nil, []string{tc.scope}); !slices.Contains(got, tc.scope) {
				t.Errorf("%s was refused to the user the new setting names", tc.scope)
			}
			if got := h.getScopesForGroups("mallory@example.org", nil, []string{tc.scope}); slices.Contains(got, tc.scope) {
				t.Errorf("%s was granted to a user no setting names", tc.scope)
			}
		})
	}
}
