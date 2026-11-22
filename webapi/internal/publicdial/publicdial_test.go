package publicdial

import (
	"net"
	"testing"
	"time"
)

// The predicate is pure so it can be tested directly, which matters because
// anything built on this cannot be tested THROUGH the guard: httptest binds
// loopback, so a functional test has to replace the client.
func TestDisallowedIP(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
		why  string
	}{
		{"8.8.8.8", false, "a routable public address"},
		{"1.1.1.1", false, "a routable public address"},
		{"2606:4700:4700::1111", false, "a routable public v6 address"},

		{"127.0.0.1", true, "loopback"},
		{"::1", true, "v6 loopback"},
		{"10.0.0.5", true, "RFC 1918"},
		{"172.16.0.1", true, "RFC 1918"},
		{"192.168.1.1", true, "RFC 1918"},
		{"fd00::1", true, "v6 unique local, which IsPrivate covers"},
		{"0.0.0.0", true, "unspecified"},
		{"::", true, "v6 unspecified"},
		// The one a cloud deployment actually has to fear: the instance
		// metadata service hands out credentials to anything that asks.
		{"169.254.169.254", true, "link-local, and the metadata service"},
		{"fe80::1", true, "v6 link-local"},
		{"224.0.0.1", true, "multicast"},
		{"ff02::1", true, "v6 multicast"},
		// Not covered by IsPrivate, which is why it is listed separately
		// in the implementation.
		{"100.64.0.1", true, "RFC 6598 carrier-grade NAT"},
		{"100.127.255.255", true, "the top of the RFC 6598 range"},
		// Just outside it, so the mask is not too wide.
		{"100.63.255.255", false, "below the RFC 6598 range"},
		{"100.128.0.1", false, "above the RFC 6598 range"},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("%q does not parse as an IP", tc.ip)
			}
			if got := DisallowedIP(ip); got != tc.want {
				t.Errorf("DisallowedIP(%s) = %v, want %v (%s)", tc.ip, got, tc.want, tc.why)
			}
		})
	}
}

// A nil IP is what a failed parse looks like, and the safe answer is to
// refuse it rather than to let it through as "not private".
func TestDisallowedIPRefusesNil(t *testing.T) {
	if !DisallowedIP(nil) {
		t.Error("DisallowedIP(nil) = false; an unparseable address must not be dialled")
	}
}

func TestClientRefusesRedirects(t *testing.T) {
	c := Client(0)
	if c.CheckRedirect == nil {
		t.Fatal("the client follows redirects; a redirect is the classic SSRF pivot")
	}
	if err := c.CheckRedirect(nil, nil); err == nil {
		t.Error("CheckRedirect allowed a redirect")
	}
}

// TestClientTimeoutNeverUnlimited: a guarded client with no timeout is still
// a footgun, because the address is public but is one somebody else chose,
// and a host that accepts and then says nothing holds the caller.
func TestClientTimeoutNeverUnlimited(t *testing.T) {
	for _, d := range []int64{0, -1} {
		c := Client(time.Duration(d))
		if c.Timeout <= 0 {
			t.Errorf("Client(%d).Timeout = %s; want a bounded default", d, c.Timeout)
		}
	}
	if got := Client(3 * time.Second).Timeout; got != 3*time.Second {
		t.Errorf("Client(3s).Timeout = %s, want 3s", got)
	}
	if got := Dialer(0).Timeout; got <= 0 {
		t.Errorf("Dialer(0).Timeout = %s; want a bounded default", got)
	}
}
