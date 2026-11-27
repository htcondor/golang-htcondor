package authz

import (
	"net"
	"testing"
)

// The IPv6 group wildcard, "2001:db8:1234:*".
//
// It is a documented way to write a security list (admin-manual/networking:
// "the prefix of a legal IPv6 address followed by ... an asterisk"), and
// condor_netaddr::from_net_string reads it as a prefix: the asterisk stands
// for the rest of the address and each colon contributes 16 mask bits. Every
// asterisk-bearing pattern used to go to the IPv4 octet parser, which wants a
// ".*" suffix, so an entry in this form matched no address at all -- turning
// a DENY into no restriction and an ALLOW into a lockout.
func TestIPv6GroupWildcardPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		addr    string
		want    bool
		why     string
	}{
		{"2001:db8:1234:*", "2001:db8:1234:5::7", true, "inside the /48"},
		{"2001:db8:1234:*", "2001:db8:1234::", true, "the prefix itself"},
		{"2001:db8:1234:*", "2001:db8:1235::1", false, "outside the /48"},
		{"2001:db8:*", "2001:db8:ffff::1", true, "two colons is a /32"},
		{"2001:db8:*", "2001:dba::1", false, "outside the /32"},
		// condor_netaddr::match compares the address family first.
		{"2001:db8:1234:*", "10.0.0.1", false, "an IPv4 peer cannot match an IPv6 network"},
		// The asterisk has to sit on a group boundary, so that the
		// parser never guesses how many groups were left out.
		{"2001:*:1234", "2001:db8:1234::1", false, "asterisk not after the last colon"},
		{"2001:db8:*extra", "2001:db8::1", false, "trailing text after the asterisk"},
		{"*:db8:1234:*", "2001:db8:1234::1", false, "a second asterisk"},
		// The prefix length comes from the colon count and nothing
		// else, so this degenerate pattern is ::/16 rather than an
		// error. Pinned because it is where a "count the groups"
		// rewrite would quietly disagree with the C++ parser.
		{":*", "::1", true, "one colon is a /16, and :: is the base"},
		{":*", "1::", false, "outside ::/16"},
	} {
		t.Run(tc.pattern+" vs "+tc.addr, func(t *testing.T) {
			got := matchNetwork(tc.pattern, net.ParseIP(tc.addr))
			if got != tc.want {
				t.Errorf("matchNetwork(%q, %q) = %v, want %v (%s)",
					tc.pattern, tc.addr, got, tc.want, tc.why)
			}
		})
	}
}

// A DENY written in the IPv6 form has to deny, which is the reason the gap
// mattered: under ALLOW_<PERM> = * the policy is only-denies, so a DENY that
// matches nothing admits the peer it names.
func TestIPv6GroupWildcardDeniesThroughThePolicy(t *testing.T) {
	p := newTestPolicy(t, mapConfig{
		"ALLOW_DAEMON": "*",
		"DENY_DAEMON":  "2001:db8:1234:*",
	}, nil, nil)

	if p.Verify(PermDaemon, ip("2001:db8:1234::5"), "condor@pool") {
		t.Error("a peer inside the denied prefix was admitted")
	}
	if !p.Verify(PermDaemon, ip("2001:db8:9999::5"), "condor@pool") {
		t.Error("a peer outside the denied prefix was refused")
	}
}

// "{:local_ips:}" stands for any address this machine holds.
//
// HTCondor's own documentation recommends it -- ALLOW_WRITE = */{:local_ips:}
// in the security manual, COLLECTOR.ALLOW_ADVERTISE_MASTER = */{:local_ips:}
// in the collector-tree one -- and matches_withnetwork resolves it before the
// network parser sees it. Without that case the braces reach the parser as an
// address, which fails, so the recommended ALLOW matched no peer and locked
// the machine out of its own daemon.
func TestLocalIPsPattern(t *testing.T) {
	loopback := net.ParseIP("127.0.0.1")
	if !matchNetwork("{:local_ips:}", loopback) {
		t.Error("the loopback address is not recognised as local")
	}
	// strcasecmp in the C++; a config file may spell it either way.
	if !matchNetwork("{:LOCAL_IPS:}", loopback) {
		t.Error("the pattern is case-sensitive, and the C++ compares it without case")
	}
	// TEST-NET-3 (RFC 5737), which is reserved for documentation and is
	// not assigned to an interface anywhere.
	if matchNetwork("{:local_ips:}", net.ParseIP("203.0.113.1")) {
		t.Error("an address this machine does not hold was called local")
	}
	if matchNetwork("{:local_ips:}", nil) {
		t.Error("a peer whose address would not parse was called local")
	}
}

// The documented recipe, end to end: a local client is admitted and a remote
// one is not.
func TestLocalIPsAdmitsTheLocalClient(t *testing.T) {
	p := newTestPolicy(t, mapConfig{"ALLOW_DAEMON": "*/{:local_ips:}"}, nil, nil)

	if !p.Verify(PermDaemon, ip("127.0.0.1"), "condor@pool") {
		t.Error("a client on this machine was refused by the documented recipe")
	}
	if p.Verify(PermDaemon, ip("203.0.113.1"), "condor@pool") {
		t.Error("a remote client was admitted by a local-only rule")
	}
}
