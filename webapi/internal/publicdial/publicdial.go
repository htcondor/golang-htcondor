// Package publicdial builds HTTP clients that will only talk to routable
// public addresses.
//
// It exists for the requests this server makes to a URL somebody else chose:
// a client-metadata document named by a client_id, a webhook destination a
// user typed in. Such a URL is a request to make this daemon fetch something,
// and a daemon inside a cluster can reach a great deal that its caller
// cannot -- other pods, the schedd's own ports, a cloud instance-metadata
// service handing out credentials.
//
// The check is on the IP at DIAL time, never on the hostname. Resolving the
// name first and then dialling it again is two lookups, and the gap between
// them is the DNS-rebinding hole: a name that answered with a public address
// when it was validated can answer with 127.0.0.1 a moment later, when it is
// actually connected to. Control hooks into the dial of the address that will
// be used, so there is no second lookup to disagree with.
package publicdial

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// DisallowedIP reports whether an address must not be dialled: anything that
// is not a routable public host.
//
// Pure, so it can be unit-tested directly -- which matters because the
// happy-path tests of anything built on this cannot go through the guarded
// dialer at all, since httptest binds loopback.
func DisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	// 100.64.0.0/10 (RFC 6598 carrier-grade NAT) is not covered by IsPrivate.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 0x40 {
		return true
	}
	return false
}

// ErrBlocked is the dial refusal, worded for somebody reading it in a
// delivery error rather than for a log.
type ErrBlocked struct{ Addr string }

func (e *ErrBlocked) Error() string {
	return fmt.Sprintf("refusing to connect to %s: not a routable public address", e.Addr)
}

// DefaultTimeout is used when a caller asks for none.
//
// A guarded client with no timeout is still a footgun: the address is public
// but it is one somebody else chose, and a host that accepts a connection and
// then says nothing holds the caller for as long as it likes. There is no
// caller here that wants to wait indefinitely, so zero means this rather than
// forever.
const DefaultTimeout = 15 * time.Second

// Dialer returns a dialer that refuses non-public addresses.
func Dialer(timeout time.Duration) *net.Dialer {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &net.Dialer{
		Timeout: timeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("refusing to connect to %q: %w", address, err)
			}
			if DisallowedIP(net.ParseIP(host)) {
				return &ErrBlocked{Addr: host}
			}
			return nil
		},
	}
}

// Client returns an HTTP client that dials public addresses only, follows no
// redirects, and gives up after timeout.
//
// Redirects are refused rather than re-checked. A redirect is the classic SSRF
// pivot -- the first hop passes the guard and points at the thing the guard
// exists to protect -- and although Control would catch the second dial too,
// a destination that answers with a redirect is a destination that was
// configured wrong. Saying so beats following it.
func Client(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           Dialer(timeout).DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("redirects are not followed")
		},
	}
}
