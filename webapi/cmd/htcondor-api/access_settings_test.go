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

package main

import (
	"testing"
)

// The four access gates resolve their superseded spellings the way the
// two cross-user entitlements do, and the way a reconfigure does.
//
// A daemon restarted on a file and one that reloaded it must come up
// with the same policy; the reconfigure half is
// TestReconfigAppliesTheSupersededAccessSettings in reconfig_test.go.
func TestStartupResolvesSupersededAccessSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		// want is keyed by the setting whose value we expect.
		wantAccess, wantRead, wantWrite, wantWebUI string
	}{
		{
			name: "neither spelling",
		},
		{
			name: "only the superseded spelling",
			body: "HTTP_API_MCP_ACCESS_GROUP = old-login\n" +
				"HTTP_API_MCP_READ_GROUP = old-readers\n" +
				"HTTP_API_MCP_WRITE_GROUP = old-writers\n" +
				"HTTP_API_WEBUI_ACCESS_GROUP = old-browsers\n",
			wantAccess: "old-login",
			wantRead:   "old-readers",
			wantWrite:  "old-writers",
			wantWebUI:  "old-browsers",
		},
		{
			name: "only the current spelling",
			body: "HTTP_API_MCP_ACCESS_GROUPS = login\n" +
				"HTTP_API_MCP_READ_GROUPS = readers\n" +
				"HTTP_API_MCP_WRITE_GROUPS = writers\n" +
				"HTTP_API_WEBUI_ACCESS_GROUPS = browsers\n",
			wantAccess: "login",
			wantRead:   "readers",
			wantWrite:  "writers",
			wantWebUI:  "browsers",
		},
		{
			name: "both: the current spelling wins, so narrowing a gate is not undone",
			body: "HTTP_API_MCP_ACCESS_GROUP = everyone\nHTTP_API_MCP_ACCESS_GROUPS = a-few\n" +
				"HTTP_API_MCP_READ_GROUP = everyone\nHTTP_API_MCP_READ_GROUPS = fewer\n" +
				"HTTP_API_MCP_WRITE_GROUP = everyone\nHTTP_API_MCP_WRITE_GROUPS = fewest\n" +
				"HTTP_API_WEBUI_ACCESS_GROUP = everyone\nHTTP_API_WEBUI_ACCESS_GROUPS = some\n",
			wantAccess: "a-few",
			wantRead:   "fewer",
			wantWrite:  "fewest",
			wantWebUI:  "some",
		},
		{
			name:       "an explicitly empty current spelling still falls back, as an unset one does",
			body:       "HTTP_API_MCP_ACCESS_GROUP = old-login\nHTTP_API_MCP_ACCESS_GROUPS =\n",
			wantAccess: "old-login",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configFrom(t, tc.body)
			logger := testLogger(t)

			var got mcpConfig
			loadAccessControlGroups(cfg, &got, logger)
			webuiGroups, _ := loadWebUIAccessSettings(cfg, logger)

			for _, c := range []struct {
				setting   string
				got, want string
			}{
				{"HTTP_API_MCP_ACCESS_GROUPS", got.mcpAccessGroups, tc.wantAccess},
				{"HTTP_API_MCP_READ_GROUPS", got.mcpReadGroups, tc.wantRead},
				{"HTTP_API_MCP_WRITE_GROUPS", got.mcpWriteGroups, tc.wantWrite},
				{"HTTP_API_WEBUI_ACCESS_GROUPS", webuiGroups, tc.wantWebUI},
			} {
				if c.got != c.want {
					t.Errorf("%s resolved to %q, want %q", c.setting, c.got, c.want)
				}
			}
		})
	}
}

// The user halves are read straight through. They are new, so there is
// no older spelling to fall back to, and a fallback invented for one
// would be a restriction -- or an exemption from one -- that nobody
// configured.
func TestStartupReadsTheAccessUserHalves(t *testing.T) {
	cfg := configFrom(t,
		"HTTP_API_MCP_ACCESS_USERS = alice@example.org\n"+
			"HTTP_API_MCP_READ_USERS = bob@example.org\n"+
			"HTTP_API_MCP_WRITE_USERS = carol@example.org\n"+
			"HTTP_API_WEBUI_ACCESS_USERS = dave@example.org\n"+
			// Set, and must not leak into any user half.
			"HTTP_API_MCP_ACCESS_GROUP = old-login\n")
	logger := testLogger(t)

	var got mcpConfig
	loadAccessControlGroups(cfg, &got, logger)
	_, webuiUsers := loadWebUIAccessSettings(cfg, logger)

	for _, c := range []struct {
		setting   string
		got, want string
	}{
		{"HTTP_API_MCP_ACCESS_USERS", got.mcpAccessUsers, "alice@example.org"},
		{"HTTP_API_MCP_READ_USERS", got.mcpReadUsers, "bob@example.org"},
		{"HTTP_API_MCP_WRITE_USERS", got.mcpWriteUsers, "carol@example.org"},
		{"HTTP_API_WEBUI_ACCESS_USERS", webuiUsers, "dave@example.org"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.setting, c.got, c.want)
		}
	}
	if got.mcpAccessGroups != "old-login" {
		t.Errorf("access groups = %q; the superseded group setting is still honored", got.mcpAccessGroups)
	}
}
