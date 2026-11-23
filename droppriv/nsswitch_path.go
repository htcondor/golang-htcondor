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

package droppriv

import (
	"bufio"
	"os"
	"slices"
	"strings"
	"sync"
)

// One nsswitch.conf location, shared by every lookup in this package.
//
// It lives in an untagged file on purpose. The passwd chain needs it only
// without cgo, the group chain needs it on every platform without cgo, and
// tests need to point all of them at a fixture. Declaring it per build tag
// meant the same path existed under some builds and not others.
var (
	nssSwitchPathMu sync.RWMutex
	nssSwitchPath   = "/etc/nsswitch.conf"
)

// SetNSSSwitchPath sets the path to nsswitch.conf. Intended for testing.
//
// It also resets the memoised group lookup, since that is chosen from this
// file on first use and would otherwise keep answering from the old one.
func SetNSSSwitchPath(path string) {
	nssSwitchPathMu.Lock()
	nssSwitchPath = path
	nssSwitchPathMu.Unlock()
	resetDefaultGroupLookup()
}

// nsswitchPath returns the configured nsswitch.conf location.
//
// Read on every build by DirectoryDeclaredForAccounts, and by the passwd
// and group chains without cgo. With cgo those chains go through glibc,
// which reads this file itself.
func nsswitchPath() string {
	nssSwitchPathMu.RLock()
	defer nssSwitchPathMu.RUnlock()
	return nssSwitchPath
}

// DirectoryDeclaredForAccounts reports whether nsswitch.conf names sss on
// the passwd line -- that is, whether this host is configured to learn
// about accounts from a directory as well as from /etc/passwd.
//
// It answers the question an empty-looking account index raises: is this
// a host that simply has no directory, or one that has a directory and
// got nothing out of it? Only the second is somebody's fault, and the
// difference is not visible from the enumeration result, which is
// identical either way.
//
// A missing or unreadable nsswitch.conf reports false. Claiming a
// directory on the strength of a file we could not read would turn a
// macOS laptop, where the file does not exist, into a permanent warning.
func DirectoryDeclaredForAccounts() bool {
	methods, err := ParseNSSwitchDB(nsswitchPath(), "passwd")
	if err != nil {
		return false
	}
	return slices.Contains(methods, NSSSwitchMethodSSS)
}

// countDeclaredMethods counts every method on a database's line,
// including the ones ParseNSSwitchDB drops because this package cannot
// speak them. The difference between this and what came back is exactly
// "what the administrator configured that we cannot consult".
//
//nolint:unused // Used in the nocgo build
func countDeclaredMethods(path, database string) int {
	f, err := os.Open(path) //nolint:gosec // operator configuration
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != database {
			continue
		}
		n := 0
		for _, f := range strings.Fields(rest) {
			// Skip action specifiers like [NOTFOUND=return].
			if strings.HasPrefix(f, "[") {
				continue
			}
			n++
		}
		return n
	}
	return 0
}
