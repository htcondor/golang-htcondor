// Package authz implements HTCondor's host/user authorization: the
// ALLOW_<PERM>/DENY_<PERM> policy enforced by the C++ IpVerify class. It gives a
// Go HTCondor daemon the same per-command authorization decisions as the C++
// daemons, modeled on src/condor_io/condor_ipverify.cpp and the matchers in
// stl_string_utils.cpp / condor_netaddr.cpp.
//
// Glob patterns are anchored at the string boundaries: "*.example.com" matches a
// host whose name ends in ".example.com", and "user@*" matches an identity that
// begins with "user@". As in HTCondor, hostname-based rules are matched against
// the peer's resolved name, so they are only as trustworthy as the pool's DNS.
package authz

import (
	"net"
	"strings"
)

// matchWildcard reports whether str matches a single-'*' glob pattern, used for
// host and user matching (based on HTCondor's matches_withwildcard_impl). The
// pattern may contain at most one meaningful '*' (a second '*' is only honored
// when it is a distinct trailing star). anycase selects case-insensitive
// comparison.
//
// Semantics, anchored at the string boundaries:
//   - no '*'            -> exact compare.
//   - "*xyz"            -> str ends with "xyz".
//   - "xyz*"            -> str begins with "xyz".
//   - "ab*cd"           -> str begins with "ab" AND ends with "cd".
//   - a trailing '*' on the matchend (e.g. "ab*cd*") -> str begins with "ab"
//     and contains "cd" after the prefix (prefix-of-suffix).
func matchWildcard(pattern, str string, anycase bool) bool {
	i := strings.IndexByte(pattern, '*')
	if i < 0 {
		return eq(pattern, str, anycase)
	}

	// Determine matchstart / matchend / prefix.
	var matchstart, matchend string
	prefix := false
	switch {
	case i == 0:
		matchstart = ""
		matchend = pattern[1:]
	case i == len(pattern)-1:
		// '*' solely at the end.
		matchstart = pattern[:i]
		matchend = ""
		prefix = true
	default:
		// '*' in the middle.
		matchstart = pattern[:i]
		matchend = pattern[i+1:]
	}
	// A trailing '*' on matchend means "prefix of the remainder".
	if strings.HasSuffix(matchend, "*") {
		matchend = matchend[:len(matchend)-1]
		prefix = true
	}

	rest := str
	if matchstart != "" {
		if !hasPrefix(rest, matchstart, anycase) {
			return false
		}
		// Consume min(len(matchstart), len(rest)) like the C++.
		n := len(matchstart)
		if n > len(rest) {
			n = len(rest)
		}
		rest = rest[n:]
	}
	if matchend != "" {
		if prefix {
			// matchend need only appear somewhere in the remainder.
			return indexAnycase(rest, matchend, anycase) >= 0
		}
		// Suffix-anchored: matchend must end str.
		return hasSuffix(rest, matchend, anycase)
	}
	return true
}

// matchUserList reports whether user matches any pattern in list (case-insensitive
// glob), mirroring contains_anycase_withwildcard.
func matchUserList(list []string, user string) bool {
	for _, p := range list {
		if matchWildcard(p, user, true) {
			return true
		}
	}
	return false
}

// matchNetwork reports whether ip matches a network pattern, mirroring
// matches_withnetwork / condor_netaddr::from_net_string+match. Supported forms:
//
//   - "*" or "*/*"          -> matches everything.
//   - "a.b.c.d/N"           -> CIDR (IPv4 or IPv6).
//   - "a.b.c.d/m.m.m.m"     -> IPv4 dotted netmask.
//   - "a.b.c.*", "a.b.*"    -> IPv4 octet wildcard on a byte boundary.
//   - "2001:db8:1234:*"     -> IPv6 group wildcard on a 16-bit boundary.
//   - "{:local_ips:}"       -> any address of this machine.
//   - "a.b.c.d" / "::1"     -> a bare address (host route).
//
// It returns false (no match) for unparseable patterns; callers treat a
// non-matching host pattern as simply "this entry's host part did not match".
func matchNetwork(pattern string, ip net.IP) bool {
	if pattern == "*" || pattern == "*/*" {
		return true
	}
	if ip == nil {
		return false
	}

	// "{:local_ips:}" stands for any address this machine holds. It is an
	// alias rather than a network, and matches_withnetwork resolves it
	// before the pattern ever reaches the network parser
	// (condor_sockaddr.cpp), so resolve it in the same place: the parser
	// below would read the braces as an unparseable address and refuse
	// every peer.
	if strings.EqualFold(pattern, "{:local_ips:}") {
		return addrIsLocal(ip)
	}

	// CIDR or dotted-netmask form.
	if slash := strings.IndexByte(pattern, '/'); slash >= 0 {
		base := pattern[:slash]
		maskPart := pattern[slash+1:]
		baseIP := net.ParseIP(base)
		if baseIP == nil {
			return false
		}
		// "/N" prefix length.
		if n, ok := parseUint(maskPart); ok {
			var bits uint64 = 32
			if baseIP.To4() == nil {
				bits = 128
			}
			if n > bits {
				return false
			}
			_, ipnet, err := net.ParseCIDR(base + "/" + maskPart)
			if err != nil {
				return false
			}
			return ipnet.Contains(ip)
		}
		// IPv4 dotted netmask "a.b.c.d/m.m.m.m".
		m := net.ParseIP(maskPart)
		if m4 := m.To4(); m4 != nil && baseIP.To4() != nil {
			mask := net.IPMask(m4)
			return baseIP.Mask(mask).Equal(ip.Mask(mask))
		}
		return false
	}

	// Wildcard on a boundary: "a.b.c.*" for IPv4, "2001:db8:1234:*" for
	// IPv6. from_net_string splits these two by whether the pattern holds
	// a colon, and so does this: an IPv6 pattern read as an IPv4 one
	// matches nothing at all.
	if strings.Contains(pattern, "*") {
		if strings.IndexByte(pattern, ':') >= 0 {
			base, bits, ok := ipv6GroupWildcard(pattern)
			if !ok {
				return false
			}
			// condor_netaddr::match refuses a target of the other
			// family before it compares any bits.
			ip16 := ip.To16()
			if ip16 == nil || ip.To4() != nil {
				return false
			}
			mask := net.CIDRMask(bits, 128)
			return base.Mask(mask).Equal(ip16.Mask(mask))
		}
		mask, base, ok := ipv4OctetWildcard(pattern)
		if !ok {
			return false
		}
		ip4 := ip.To4()
		if ip4 == nil {
			return false
		}
		return base.Mask(mask).Equal(ip4.Mask(mask))
	}

	// Bare address: host route (/32 or /128).
	pip := net.ParseIP(pattern)
	if pip == nil {
		return false
	}
	return pip.Equal(ip)
}

// ipv4OctetWildcard parses "a.b.c.*", "a.b.*", "a.*" into a base address and the
// byte-boundary mask implied by the leading concrete octets; the trailing "*"
// wildcards all remaining octets (so "10.*" is 10.0.0.0/8). HTCondor requires
// the wildcard on a byte boundary, so the pattern must end in ".*" and have 1-3
// concrete leading octets.
func ipv4OctetWildcard(pattern string) (net.IPMask, net.IP, bool) {
	if !strings.HasSuffix(pattern, ".*") {
		return nil, nil, false
	}
	head := strings.TrimSuffix(pattern, ".*")
	if head == "" || strings.Contains(head, "*") {
		return nil, nil, false
	}
	octets := strings.Split(head, ".")
	if len(octets) < 1 || len(octets) > 3 {
		return nil, nil, false
	}
	var concrete []byte
	for _, p := range octets {
		n, ok := parseUint(p)
		if !ok || n > 255 {
			return nil, nil, false
		}
		concrete = append(concrete, byte(n))
	}
	// The concrete leading octets are exact (mask 0xff); the remaining octets
	// stay wildcarded (mask 0, base 0).
	base := make(net.IP, 4)
	mask := make(net.IPMask, 4)
	copy(base, concrete)
	copy(mask, []byte{0xff, 0xff, 0xff, 0xff}[:len(concrete)])
	return mask, base, true
}

// ipv6GroupWildcard parses "2001:db8:1234:*" into a base address and the prefix
// length implied by the concrete groups: each colon in the pattern contributes
// 16 bits, so that example is 2001:db8:1234::/48.
//
// Mirrors the IPv6 leg of condor_netaddr::from_net_string. The asterisk has to
// sit immediately after the last colon -- HTCondor wants the wildcard on a
// group boundary so it never has to guess how many groups the administrator
// meant to leave out -- and the address is parsed with the asterisk rewritten
// as a colon, which is what turns a prefix into a valid literal.
func ipv6GroupWildcard(pattern string) (net.IP, int, bool) {
	star := strings.IndexByte(pattern, '*')
	if star < 0 || strings.LastIndexByte(pattern, ':') != star-1 {
		return nil, 0, false
	}
	base := net.ParseIP(pattern[:star] + ":" + pattern[star+1:])
	if base == nil || base.To4() != nil {
		return nil, 0, false
	}
	bits := strings.Count(pattern, ":") * 16
	if bits > 128 {
		return nil, 0, false
	}
	return base, bits, true
}

// addrIsLocal reports whether ip is an address of this machine, by asking the
// kernel to bind a UDP socket to it -- the test C++ addr_is_local makes
// (condor_sockfunc.cpp).
//
// Binding rather than enumerating interfaces, so the two implementations agree
// wherever the kernel's answer and an interface list would not. A refused bind
// reads as "not local", which is the direction that denies rather than admits.
//
// The socket is only ever created for a security list that actually names
// {:local_ips:}, and only for the peer currently being authorized.
func addrIsLocal(ip net.IP) bool {
	network := "udp6"
	if ip.To4() != nil {
		network = "udp4"
	}
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// --- small case-aware string helpers (match the C++ str(case)cmp behavior) ---

func eq(a, b string, anycase bool) bool {
	if anycase {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func hasPrefix(s, p string, anycase bool) bool {
	if len(p) > len(s) {
		return false
	}
	return eq(s[:len(p)], p, anycase)
}

func hasSuffix(s, suf string, anycase bool) bool {
	if len(suf) > len(s) {
		return false
	}
	return eq(s[len(s)-len(suf):], suf, anycase)
}

func indexAnycase(s, sub string, anycase bool) int {
	if !anycase {
		return strings.Index(s, sub)
	}
	return strings.Index(strings.ToLower(s), strings.ToLower(sub))
}

func parseUint(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}
