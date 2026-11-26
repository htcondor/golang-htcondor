package authz

import (
	"net"
	"testing"
)

// mapConfig is a trivial ConfigGetter for tests.
type mapConfig map[string]string

func (m mapConfig) Get(k string) (string, bool) { v, ok := m[k]; return v, ok }

// newTestPolicy builds a Policy with DNS resolution stubbed out so tests are
// hermetic. fwd maps hostname->IPs, rev maps IP-string->hostnames.
func newTestPolicy(t *testing.T, cfg mapConfig, fwd map[string][]string, rev map[string][]string) *Policy {
	t.Helper()
	p, err := NewPolicy(cfg, "CCB")
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	p.forwardResolve = func(h string) []string { return fwd[h] }
	p.reverseResolve = func(ip net.IP) []string {
		if ip == nil {
			return nil
		}
		return rev[ip.String()]
	}
	// Rebuild tables now that forwardResolve is set (fill_table ran in NewPolicy
	// with the default resolver). Re-run buildEntry for affected perms.
	for _, perm := range allPerms {
		p.entries[perm] = p.buildEntry(cfg, perm)
	}
	return p
}

func ip(s string) net.IP { return net.ParseIP(s) }

func TestVerifyBehaviorOptimizations(t *testing.T) {
	// DAEMON with no ALLOW -> deny everyone; READ with no config -> use-table
	// (default-deny posture, since no ALLOW present and READ tables empty).
	p := newTestPolicy(t, mapConfig{}, nil, nil)
	if p.Verify(PermDaemon, ip("10.0.0.1"), "condor@pool") {
		t.Error("DAEMON with no ALLOW must deny")
	}

	// ALLOW_DAEMON = * -> allow anyone.
	p = newTestPolicy(t, mapConfig{"ALLOW_DAEMON": "*"}, nil, nil)
	if !p.Verify(PermDaemon, ip("10.0.0.1"), "condor@pool") {
		t.Error("ALLOW_DAEMON=* must allow anyone")
	}

	// ALLOW_DAEMON=* with DENY_DAEMON=10.0.0.5 -> only-denies.
	p = newTestPolicy(t, mapConfig{"ALLOW_DAEMON": "*", "DENY_DAEMON": "10.0.0.5"}, nil, nil)
	if p.Verify(PermDaemon, ip("10.0.0.5"), "condor@pool") {
		t.Error("DENY_DAEMON must deny the listed IP")
	}
	if !p.Verify(PermDaemon, ip("10.0.0.6"), "condor@pool") {
		t.Error("only-denies must allow the unlisted IP")
	}
}

func TestVerifyUseTable(t *testing.T) {
	cfg := mapConfig{
		"ALLOW_DAEMON": "condor@pool.example/192.168.0.0/16, schedd@*/10.0.0.0/8",
		"DENY_DAEMON":  "*/192.168.5.0/24",
	}
	p := newTestPolicy(t, cfg, nil, nil)

	if !p.Verify(PermDaemon, ip("192.168.1.1"), "condor@pool.example") {
		t.Error("listed user from listed subnet should be allowed")
	}
	if p.Verify(PermDaemon, ip("192.168.1.1"), "mallory@evil.example") {
		t.Error("unlisted user should be denied")
	}
	if p.Verify(PermDaemon, ip("172.16.0.1"), "condor@pool.example") {
		t.Error("listed user from a non-listed subnet should be denied")
	}
	// Deny subnet wins even for an otherwise-allowed user.
	if p.Verify(PermDaemon, ip("192.168.5.5"), "condor@pool.example") {
		t.Error("deny subnet must override allow")
	}
	// Second allow rule: schedd from 10.x.
	if !p.Verify(PermDaemon, ip("10.1.2.3"), "schedd@some.host") {
		t.Error("second allow rule should match")
	}
}

func TestVerifyHierarchyImplication(t *testing.T) {
	// Only WRITE is configured; READ should be granted via implication, since
	// granting WRITE implies READ (DirectlyImpliedBy(READ) includes WRITE).
	cfg := mapConfig{"ALLOW_WRITE": "condor@pool/192.168.0.0/16"}
	p := newTestPolicy(t, cfg, nil, nil)

	if !p.Verify(PermWrite, ip("192.168.1.1"), "condor@pool") {
		t.Error("WRITE should be allowed directly")
	}
	if !p.Verify(PermRead, ip("192.168.1.1"), "condor@pool") {
		t.Error("READ should be implied by WRITE")
	}
	if p.Verify(PermRead, ip("192.168.1.1"), "other@pool") {
		t.Error("READ should not be granted to a user who has neither READ nor WRITE")
	}
}

func TestVerifyPoolUsernameEquivalent(t *testing.T) {
	// A rule for condor@pool should also admit condor_pool@pool and vice versa.
	cfg := mapConfig{"ALLOW_DAEMON": "condor@pool/*"}
	p := newTestPolicy(t, cfg, nil, nil)
	if !p.Verify(PermDaemon, ip("1.2.3.4"), "condor_pool@pool") {
		t.Error("condor_pool@ should be accepted as equivalent to condor@")
	}
}

func TestVerifyHostnameResolution(t *testing.T) {
	// ALLOW lists a hostname; the entry expands to its IP (forward) and the IP's
	// reverse name must match the hostname pattern.
	cfg := mapConfig{"ALLOW_DAEMON": "worker.example"}
	fwd := map[string][]string{"worker.example": {"203.0.113.9"}}
	rev := map[string][]string{"198.51.100.7": {"other.example"}}
	p := newTestPolicy(t, cfg, fwd, rev)

	if !p.Verify(PermDaemon, ip("203.0.113.9"), "anyone@x") {
		t.Error("forward-resolved IP of the hostname entry should match")
	}
	if p.Verify(PermDaemon, ip("198.51.100.7"), "anyone@x") {
		t.Error("a different host should not match")
	}
}

func TestAuthorize(t *testing.T) {
	// A wide-open READ policy authorizes any peer at READ but not at DAEMON.
	p := newTestPolicy(t, mapConfig{"ALLOW_READ": "*"}, nil, nil)
	if !p.Authorize("READ", "127.0.0.1:5000", "someone@example.com") {
		t.Error("READ should be authorized under ALLOW_READ = *")
	}
	if p.Authorize("DAEMON", "127.0.0.1:5000", "someone@example.com") {
		t.Error("DAEMON should not be authorized without an ALLOW_DAEMON rule")
	}
	// ALLOW (special) is always authorized, and a bare host (no port) still parses.
	if !p.Authorize("ALLOW", "127.0.0.1", "") {
		t.Error("ALLOW must always authorize")
	}
}

// TestEmptyKnobDoesNotEndTheFallbackChain: a knob whose value is empty is
// not set, and the walk continues past it.
//
// SecMan::getSecSetting reads these through param(), which returns NULL for
// an empty raw value and for one that expands to empty, so the C++ chain
// cannot stop on an empty knob. Stopping on one voided the broader level
// silently, and in the DENY direction that admits exactly the peers the
// operator blocked.
func TestEmptyKnobDoesNotEndTheFallbackChain(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  mapConfig
	}{
		{
			// The shape `use SECURITY:recommended` leaves behind, with
			// the operator's own DENY_WRITE emptied out.
			name: "empty DENY_WRITE must not void DENY_DEFAULT",
			cfg: mapConfig{
				"ALLOW_WRITE":  "*",
				"DENY_WRITE":   "",
				"DENY_DEFAULT": "10.0.0.5",
			},
		},
		{
			// A knob referring to an undefined macro expands to empty,
			// which is the same case arrived at by accident.
			name: "empty DENY_WRITE_<SUBSYS> must not void DENY_DEFAULT",
			cfg: mapConfig{
				"ALLOW_WRITE":    "*",
				"DENY_WRITE_CCB": "",
				"DENY_DEFAULT":   "10.0.0.5",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPolicy(t, tc.cfg, nil, nil)
			if p.Verify(PermWrite, ip("10.0.0.5"), "someone@example.org") {
				t.Error("the peer DENY_DEFAULT names was allowed: an empty knob ended the chain")
			}
			if !p.Verify(PermWrite, ip("10.0.0.6"), "someone@example.org") {
				t.Error("a peer no DENY names was refused")
			}
		})
	}
}

// TestDenyByHostnameFiresAfterAnAllowByIP: an allow matched on the IP leg
// closes the ALLOW question, not the DENY question, so the hostname leg
// still runs.
//
// C++ keeps the two apart -- deny_resolved is the deny bit alone while
// allow_resolved is allow|deny -- and resolves hostnames while either is
// open. Skipping the leg after an IP allow silenced every DENY that only a
// resolved name can reach: a wildcard pattern is keyed literally and can
// never match on the IP leg.
func TestDenyByHostnameFiresAfterAnAllowByIP(t *testing.T) {
	cfg := mapConfig{
		"ALLOW_WRITE": "10.0.0.0/24",
		"DENY_WRITE":  "*.evil.example.org",
	}
	rev := map[string][]string{
		"10.0.0.7": {"host.evil.example.org"},
		"10.0.0.8": {"host.good.example.org"},
	}
	p := newTestPolicy(t, cfg, nil, rev)

	if p.Verify(PermWrite, ip("10.0.0.7"), "someone@example.org") {
		t.Error("a wildcard-hostname DENY did not fire for a peer the IP leg allowed")
	}
	if !p.Verify(PermWrite, ip("10.0.0.8"), "someone@example.org") {
		t.Error("a peer inside ALLOW_WRITE and outside DENY_WRITE was refused")
	}
}

// TestDenyOnALaterAliasOverridesAnEarlierAllow: the alias scan does not stop
// at the first allow. C++ walks every name get_hostname_with_alias returns,
// gating each lookup on its own mask, so a DENY on a later PTR alias still
// overrides an allow found on an earlier one.
func TestDenyOnALaterAliasOverridesAnEarlierAllow(t *testing.T) {
	cfg := mapConfig{
		"ALLOW_WRITE": "good.example.org",
		"DENY_WRITE":  "*.evil.example.org",
	}
	rev := map[string][]string{
		// The allowed name comes first on purpose: the bug was a break.
		"10.0.0.9": {"good.example.org", "host.evil.example.org"},
	}
	p := newTestPolicy(t, cfg, nil, rev)

	if p.Verify(PermWrite, ip("10.0.0.9"), "someone@example.org") {
		t.Error("a DENY on a later alias was abandoned after an allow matched an earlier one")
	}
}
