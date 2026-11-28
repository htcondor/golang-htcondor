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

// What an unconfigured gate answers, gate by gate.
//
// This is the one property the _GROUPS/_USERS settings do not share, and
// the one whose two answers are indistinguishable at a call site: every
// gate is read with allows(). An opt-in restriction that answered
// "nobody" would lock every user out of /mcp and the web interface on
// every deployment that configured no groups, which is most of them; a
// cross-user entitlement that answered "everybody" would hand every
// authenticated caller every user's jobs.
//
// So the answer is a property of the set, fixed by the constructor that
// built it, and this is the test that fails -- by name, and saying which
// direction -- if one of them is built the other way.
func TestUnconfiguredGatePolarity(t *testing.T) {
	// Nothing configured: every one of these settings is unset, which
	// is the shape of a deployment that has never read this page.
	h := newClaimTestHandler(t, HandlerConfig{})

	for _, tc := range []struct {
		gate string
		set  *principalSet
		// open says an empty set admits everybody. False means it
		// admits nobody.
		open bool
		// why names what breaks if the answer is the other one.
		why string
	}{
		{
			gate: "HTTP_API_MCP_ACCESS_GROUPS/_USERS",
			set:  h.mcpAccessGroups,
			open: true,
			why:  "nobody could reach /mcp on a deployment that configured no groups",
		},
		{
			gate: "HTTP_API_MCP_READ_GROUPS/_USERS",
			set:  h.mcpReadGroups,
			open: true,
			why:  "mcp:read would be granted to nobody where no read gate is configured",
		},
		{
			gate: "HTTP_API_MCP_WRITE_GROUPS/_USERS",
			set:  h.mcpWriteGroups,
			open: true,
			why:  "mcp:write and condor:/WRITE would be granted to nobody where no write gate is configured",
		},
		{
			gate: "HTTP_API_WEBUI_ACCESS_GROUPS/_USERS",
			set:  h.webuiAccessGroups,
			open: true,
			why:  "nobody could log in to the web interface on a deployment that configured no groups",
		},
		{
			gate: "HTTP_API_ANYUSER_READ_GROUPS/_USERS",
			set:  h.anyUserRead,
			open: false,
			why:  "every authenticated caller could request anyuser:read and read every user's jobs",
		},
		{
			gate: "HTTP_API_ANYUSER_WRITE_GROUPS/_USERS",
			set:  h.anyUserWrite,
			open: false,
			why:  "every authenticated caller could request anyuser:write and act on every user's jobs",
		},
	} {
		t.Run(tc.gate, func(t *testing.T) {
			if tc.set == nil {
				t.Fatalf("%s: NewHandler built no set for this gate, so it refuses everybody", tc.gate)
			}
			if tc.set.configured() {
				t.Fatalf("%s: reports itself configured although nothing was set", tc.gate)
			}
			// A caller carrying no groups at all, which is the shape a
			// token-sourced deployment produces, and one carrying some.
			for _, groups := range [][]string{nil, {"some-group"}} {
				got := tc.set.allows("alice@example.org", groups)
				switch {
				case tc.open && !got:
					t.Errorf("%s: an unconfigured gate REFUSED a caller (groups %v). "+
						"Unset must mean no requirement, not nobody -- otherwise %s. "+
						"Build it with newOptInPrincipalSet.", tc.gate, groups, tc.why)
				case !tc.open && got:
					t.Errorf("%s: an unconfigured gate ADMITTED a caller (groups %v). "+
						"Unset must mean nobody, not everybody -- otherwise %s. "+
						"Build it with newPrincipalSet.", tc.gate, groups, tc.why)
				}
			}
		})
	}
}

// The same question asked through the decisions those sets feed, so a
// gate wired to the wrong set fails here even where its polarity is
// right.
func TestUnconfiguredDeploymentGatesNothing(t *testing.T) {
	h := newClaimTestHandler(t, HandlerConfig{})
	const actor = "alice@example.org"
	groups := []string{"some-group"}

	if err := h.validateGroupAccess(actor, groups); err != nil {
		t.Errorf("/mcp refused a caller on a deployment that configured no access groups: %v", err)
	}
	if err := h.validateWebUIAccess(actor, groups); err != nil {
		t.Errorf("the web interface refused a caller on a deployment that configured no access groups: %v", err)
	}
	granted := h.getScopesForGroups(actor, groups, []string{"mcp:read", "mcp:write"})
	for _, scope := range []string{"mcp:read", "mcp:write"} {
		if !slices.Contains(granted, scope) {
			t.Errorf("%s was refused on a deployment that configured no groups; granted = %v", scope, granted)
		}
	}
	// And the cross-user pair, which answers the other way, on the same
	// Handler -- so a change that made every gate agree fails here.
	granted = h.getScopesForGroups(actor, groups, []string{scopeAnyUserRead, scopeAnyUserWrite})
	if len(granted) != 0 {
		t.Errorf("a cross-user entitlement was granted although nothing configured one: %v", granted)
	}
}

// Naming anybody turns an opt-in restriction ON for everybody else.
// This is the half of the polarity rule an operator meets first: adding
// one group to HTTP_API_MCP_ACCESS_GROUPS locks out everyone not in it.
func TestConfiguredGateRefusesEverybodyElse(t *testing.T) {
	h := newClaimTestHandler(t, HandlerConfig{
		MCPAccessGroups:   "mcp-users",
		WebUIAccessGroups: "web-users",
	})
	const stranger = "mallory@example.org"

	if err := h.validateGroupAccess(stranger, []string{"other"}); err == nil {
		t.Error("/mcp admitted a caller in none of its configured groups")
	}
	if err := h.validateWebUIAccess(stranger, []string{"other"}); err == nil {
		t.Error("the web interface admitted a caller in none of its configured groups")
	}
	if err := h.validateGroupAccess("bob@example.org", []string{"mcp-users"}); err != nil {
		t.Errorf("/mcp refused a member of its own group: %v", err)
	}
}

// The user half decides each gate on its own, with no groups in play.
//
// This is the case the group half cannot serve: HTTP_API_GROUP_SOURCE =
// token with a token that carries no groups, which is the default and
// the only option in a container. A gate whose user half was dropped
// would still pass every group test above.
func TestUserHalfDecidesEachGate(t *testing.T) {
	const named = "alice@example.org"
	const stranger = "mallory@example.org"

	t.Run("HTTP_API_MCP_ACCESS_USERS", func(t *testing.T) {
		h := newClaimTestHandler(t, HandlerConfig{MCPAccessUsers: named})
		if err := h.validateGroupAccess(named, nil); err != nil {
			t.Errorf("the named user was refused /mcp although no group was required: %v", err)
		}
		if err := h.validateGroupAccess(stranger, nil); err == nil {
			t.Error("naming a user did not restrict /mcp to the people named")
		}
	})

	t.Run("HTTP_API_WEBUI_ACCESS_USERS", func(t *testing.T) {
		h := newClaimTestHandler(t, HandlerConfig{WebUIAccessUsers: named})
		if err := h.validateWebUIAccess(named, nil); err != nil {
			t.Errorf("the named user was refused the web interface: %v", err)
		}
		if err := h.validateWebUIAccess(stranger, nil); err == nil {
			t.Error("naming a user did not restrict the web interface to the people named")
		}
	})

	t.Run("HTTP_API_MCP_READ_USERS", func(t *testing.T) {
		h := newClaimTestHandler(t, HandlerConfig{MCPReadUsers: named})
		if got := h.getScopesForGroups(named, nil, []string{"mcp:read"}); !slices.Contains(got, "mcp:read") {
			t.Errorf("the named user was refused mcp:read; granted = %v", got)
		}
		if got := h.getScopesForGroups(stranger, nil, []string{"mcp:read"}); slices.Contains(got, "mcp:read") {
			t.Errorf("naming a read user did not restrict mcp:read; granted = %v", got)
		}
	})

	t.Run("HTTP_API_MCP_WRITE_USERS", func(t *testing.T) {
		h := newClaimTestHandler(t, HandlerConfig{MCPWriteUsers: named})
		if got := h.getScopesForGroups(named, nil, []string{"mcp:write"}); !slices.Contains(got, "mcp:write") {
			t.Errorf("the named user was refused mcp:write; granted = %v", got)
		}
		if got := h.getScopesForGroups(stranger, nil, []string{"mcp:write"}); slices.Contains(got, "mcp:write") {
			t.Errorf("naming a write user did not restrict mcp:write; granted = %v", got)
		}
		// condor:/WRITE mints the same authorization, so it has to
		// follow the same list rather than offering the power under
		// the other name.
		if got := h.getScopesForGroups(stranger, nil, []string{condorScopeWrite}); slices.Contains(got, condorScopeWrite) {
			t.Errorf("condor:/WRITE escaped the write user list; granted = %v", got)
		}
	})
}

// The web interface falls back to the MCP gate only when NOTHING names
// anybody in its own pair. A deployment that names only users there has
// configured the gate, and must not be widened back to the MCP one.
func TestWebUIUserHalfDetachesItFromTheMCPGate(t *testing.T) {
	h := newClaimTestHandler(t, HandlerConfig{
		MCPAccessGroups:  "mcp-users",
		WebUIAccessUsers: "alice@example.org",
	})

	if err := h.validateWebUIAccess("alice@example.org", nil); err != nil {
		t.Errorf("the named user was refused the web interface: %v", err)
	}
	if err := h.validateWebUIAccess("bob@example.org", []string{"mcp-users"}); err == nil {
		t.Error("the web interface still answered to the MCP group although its own user list names somebody")
	}
}

// A reconfigure must install each half in the half it names.
//
// The two are parsed identically and stored side by side, so crossing
// them compiles, runs, and is wrong in a way that only shows up for a
// deployment that uses one of them: a user list installed as a group
// list refuses the person it names and admits anybody who happens to
// hold a group by that name, and a group list installed as a user list
// matches nobody at all.
func TestSettersInstallTheHalfTheyName(t *testing.T) {
	const named = "alice@example.org"
	const stranger = "mallory@example.org"

	for _, tc := range []struct {
		gate                string
		setGroups, setUsers func(*Handler, string)
		// decide reports whether this caller passes the gate these
		// setters feed.
		decide func(*Handler, string, []string) bool
	}{
		{
			gate:      "HTTP_API_MCP_ACCESS",
			setGroups: (*Handler).SetMCPAccessGroups,
			setUsers:  (*Handler).SetMCPAccessUsers,
			decide:    func(h *Handler, a string, g []string) bool { return h.validateGroupAccess(a, g) == nil },
		},
		{
			gate:      "HTTP_API_WEBUI_ACCESS",
			setGroups: (*Handler).SetWebUIAccessGroups,
			setUsers:  (*Handler).SetWebUIAccessUsers,
			decide:    func(h *Handler, a string, g []string) bool { return h.validateWebUIAccess(a, g) == nil },
		},
		{
			gate:      "HTTP_API_MCP_READ",
			setGroups: (*Handler).SetMCPReadGroups,
			setUsers:  (*Handler).SetMCPReadUsers,
			decide: func(h *Handler, a string, g []string) bool {
				return slices.Contains(h.getScopesForGroups(a, g, []string{"mcp:read"}), "mcp:read")
			},
		},
		{
			gate:      "HTTP_API_MCP_WRITE",
			setGroups: (*Handler).SetMCPWriteGroups,
			setUsers:  (*Handler).SetMCPWriteUsers,
			decide: func(h *Handler, a string, g []string) bool {
				return slices.Contains(h.getScopesForGroups(a, g, []string{"mcp:write"}), "mcp:write")
			},
		},
	} {
		t.Run(tc.gate+"_USERS", func(t *testing.T) {
			h := newClaimTestHandler(t, HandlerConfig{})
			tc.setUsers(h, named)

			if !tc.decide(h, named, nil) {
				t.Errorf("%s_USERS: the named user was refused after a reconfigure. The value "+
					"was installed as a group list, which matches a GROUP by that name rather "+
					"than the person.", tc.gate)
			}
			if tc.decide(h, stranger, nil) {
				t.Errorf("%s_USERS: a reconfigure that named somebody did not restrict the gate", tc.gate)
			}
			if tc.decide(h, stranger, []string{named}) {
				t.Errorf("%s_USERS: a caller holding a GROUP named %q passed; the user list was "+
					"installed as a group list", tc.gate, named)
			}
		})

		t.Run(tc.gate+"_GROUPS", func(t *testing.T) {
			h := newClaimTestHandler(t, HandlerConfig{})
			tc.setGroups(h, "login")

			if !tc.decide(h, named, []string{"login"}) {
				t.Errorf("%s_GROUPS: a member of the configured group was refused after a "+
					"reconfigure. The value was installed as a user list, which matches "+
					"nobody.", tc.gate)
			}
			if tc.decide(h, named, []string{"other"}) {
				t.Errorf("%s_GROUPS: a reconfigure that named a group did not restrict the gate", tc.gate)
			}
		})
	}
}
