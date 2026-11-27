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

// The startup read and the reconfigure path must resolve the superseded
// spelling identically. If they disagree, a daemon that reloads its
// configuration ends up running a different policy from one restarted
// on the same file -- a difference nothing reports and nobody would
// think to look for.
//
// The reconfigure half is TestReconfigAppliesASupersededGroupSetting
// and its two neighbours in reconfig_test.go; this is the startup half,
// case for case.
func TestStartupResolvesSupersededGroupSettings(t *testing.T) {
	for _, tc := range []struct {
		name                string
		body                string
		wantRead, wantWrite string
	}{
		{
			name:      "neither set",
			body:      "",
			wantRead:  "",
			wantWrite: "",
		},
		{
			name:      "only the superseded spelling",
			body:      "HTTP_API_MCP_ADMIN_GROUP = old-admins\nHTTP_API_MCP_SUPERUSER_GROUP = old-supers\n",
			wantRead:  "old-admins",
			wantWrite: "old-supers",
		},
		{
			name:      "only the current spelling",
			body:      "HTTP_API_ANYUSER_READ_GROUPS = new-admins\nHTTP_API_ANYUSER_WRITE_GROUPS = new-supers\n",
			wantRead:  "new-admins",
			wantWrite: "new-supers",
		},
		{
			name: "both: the current spelling wins, so narrowing the privilege is not undone",
			body: "HTTP_API_MCP_ADMIN_GROUP = everyone\nHTTP_API_ANYUSER_READ_GROUPS = a-few\n" +
				"HTTP_API_MCP_SUPERUSER_GROUP = everyone\nHTTP_API_ANYUSER_WRITE_GROUPS = fewer\n",
			wantRead:  "a-few",
			wantWrite: "fewer",
		},
		{
			name:      "an explicitly empty current spelling still falls back, as an unset one does",
			body:      "HTTP_API_MCP_ADMIN_GROUP = old-admins\nHTTP_API_ANYUSER_READ_GROUPS =\n",
			wantRead:  "old-admins",
			wantWrite: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got mcpConfig
			loadAccessControlGroups(configFrom(t, tc.body), &got, testLogger(t))
			if got.anyUserReadGroups != tc.wantRead {
				t.Errorf("read groups = %q, want %q", got.anyUserReadGroups, tc.wantRead)
			}
			if got.anyUserWriteGroups != tc.wantWrite {
				t.Errorf("write groups = %q, want %q", got.anyUserWriteGroups, tc.wantWrite)
			}
		})
	}
}

// The user halves are read straight through: they are new, so there is
// no older spelling to fall back to, and a fallback invented for them
// would be a privilege nobody configured.
func TestStartupReadsTheUserHalves(t *testing.T) {
	var got mcpConfig
	loadAccessControlGroups(configFrom(t,
		"HTTP_API_ANYUSER_READ_USERS = alice@example.org\n"+
			"HTTP_API_ANYUSER_WRITE_USERS = bob@example.org\n"+
			// Set, and must not leak into the user halves.
			"HTTP_API_MCP_ADMIN_GROUP = old-admins\n"), &got, testLogger(t))

	if got.anyUserReadUsers != "alice@example.org" {
		t.Errorf("read users = %q", got.anyUserReadUsers)
	}
	if got.anyUserWriteUsers != "bob@example.org" {
		t.Errorf("write users = %q", got.anyUserWriteUsers)
	}
	if got.anyUserReadGroups != "old-admins" {
		t.Errorf("read groups = %q; the superseded group setting is still honored", got.anyUserReadGroups)
	}
}
