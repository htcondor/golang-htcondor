package daemon

import (
	"net"
	"strings"
	"testing"

	"github.com/PelicanPlatform/classad/classad"
	"github.com/htcondor/golang-htcondor/config"
)

// A daemon that is not behind a shared port advertises the address of the socket it
// actually bound. Before AdvertisedAddress existed this returned nothing, so PublishAd
// left MyAddress off the ad and every daemon filled it in again by hand.
func TestAdvertisedAddressFallsBackToTheListener(t *testing.T) {
	cfg := config.NewEmpty()
	cfg.Set("HOST_ALIAS", "ap40.chtc.wisc.edu")
	d := testDaemon(t, cfg)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	d.commandListener = ln

	got := d.AdvertisedAddress()
	want := ln.Addr().String() + "?alias=ap40.chtc.wisc.edu"
	if got != want {
		t.Errorf("AdvertisedAddress() = %q, want %q", got, want)
	}
}

func TestAdvertisedAddressWithoutAListener(t *testing.T) {
	d := testDaemon(t, config.NewEmpty())
	if got := d.AdvertisedAddress(); got != "" {
		t.Errorf("AdvertisedAddress() = %q, want %q", got, "")
	}
}

// The shared-port sinful wins over the listener, whose address behind a shared port is
// a Unix socket path nobody can dial.
func TestAdvertisedAddressPrefersTheSharedPortSinful(t *testing.T) {
	cfg := config.NewEmpty()
	cfg.Set("HOST_ALIAS", "ap40.chtc.wisc.edu")
	d := testDaemon(t, cfg)
	d.sharedPortName = "http_api"
	d.sharedPortAdvertised = "131.169.223.39:9618?addrs=131.169.223.39-9618&alias=bird.desy.de&noUDP&sock=http_api"

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	d.commandListener = ln

	// The shared-port server's alias is kept; HOST_ALIAS does not override it.
	want := "131.169.223.39:9618?addrs=131.169.223.39-9618&alias=bird.desy.de&noUDP&sock=http_api"
	if got := d.AdvertisedAddress(); got != want {
		t.Errorf("AdvertisedAddress() = %q, want %q", got, want)
	}
}

// Issue #896 end to end: the MyAddress a Go daemon puts on its collector ad must carry
// the alias an SSL client verifies the host certificate against.
func TestPublishAdMyAddressCarriesTheAlias(t *testing.T) {
	cfg := config.NewEmpty()
	cfg.Set("HOST_ALIAS", "bird-htc-sched22.desy.de")
	d := testDaemon(t, cfg)
	d.sharedPortName = "http_api"
	d.sharedPortAdvertised = "131.169.223.39:9618?addrs=131.169.223.39-9618+[2001-638-700-10df--1-27]-9618&noUDP&sock=http_api"

	ad := classad.New()
	d.PublishAd(ad)

	got, ok := ad.EvaluateAttrString("MyAddress")
	if !ok {
		t.Fatal("ad has no MyAddress")
	}
	for _, want := range []string{
		"alias=bird-htc-sched22.desy.de",
		"addrs=131.169.223.39-9618+[2001-638-700-10df--1-27]-9618",
		"noUDP",
		"sock=http_api",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("MyAddress = %q, missing %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "<") || !strings.HasSuffix(got, ">") {
		t.Errorf("MyAddress = %q, want it bracketed exactly once", got)
	}
	if strings.HasPrefix(got, "<<") {
		t.Errorf("MyAddress = %q, double-bracketed", got)
	}
}

// withAlias is how WithHostAlias renders an address for the configured
// alias, so a test can say what it expects without restating that rule.
func withAlias(cfg *config.Config, addr string) string {
	if alias := configGet(cfg, "HOST_ALIAS"); alias != "" {
		return addr + "?alias=" + alias
	}
	return addr
}

// TestAdvertisedAddressRewritesAWildcardBind: a daemon that bound the
// wildcard must not publish it.
//
// BIND_ALL_INTERFACES defaults to true, so a wildcard bind is the
// ordinary case off the shared port -- both for the command socket
// inherited from condor_master and for a caller's own fallback bind --
// and Addr() is then "[::]:PORT" or "0.0.0.0:PORT". Publishing that
// verbatim put MyAddress = "<[::]:PORT?alias=...>" in the collector ad,
// which no peer can dial: it reaches its own loopback or is refused. C++
// substitutes the host's local IP in condor_getsockname_ex before the
// sinful is built, and lets TCP_FORWARDING_HOST override it.
func TestAdvertisedAddressRewritesAWildcardBind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		network string
		bind    string
		knobs   map[string]string
		want    string
	}{
		{
			name:    "IPv4 wildcard takes the detected IPv4 address",
			network: "tcp4",
			bind:    "0.0.0.0:0",
			knobs:   map[string]string{"IPV4_ADDRESS": "192.0.2.10"},
			want:    "192.0.2.10",
		},
		{
			name:    "TCP_FORWARDING_HOST wins, as it does in get_sinful_public",
			network: "tcp4",
			bind:    "0.0.0.0:0",
			knobs:   map[string]string{"TCP_FORWARDING_HOST": "gw.example.org", "IPV4_ADDRESS": "192.0.2.10"},
			want:    "gw.example.org",
		},
		{
			// An IPv6 wildcard must not become an IPv4 literal, or an
			// IPv6-only peer has nothing to dial.
			name:    "IPv6 wildcard keeps the family",
			network: "tcp6",
			bind:    "[::]:0",
			knobs:   map[string]string{"IPV6_ADDRESS": "2001:db8::10", "IPV4_ADDRESS": "192.0.2.10"},
			want:    "2001:db8::10",
		},
		{
			// IP_ADDRESS is whichever family the machine prefers, so it
			// has to be family-checked like any other candidate.
			name:    "an IPv4 wildcard does not take an IPv6 IP_ADDRESS",
			network: "tcp4",
			bind:    "0.0.0.0:0",
			knobs: map[string]string{
				"IPV4_ADDRESS": "", "IPV6_ADDRESS": "", "IP_ADDRESS": "2001:db8::10",
				"FULL_HOSTNAME": "ep1.chtc.wisc.edu",
			},
			want: "ep1.chtc.wisc.edu",
		},
		{
			name:    "FULL_HOSTNAME is the last resort",
			network: "tcp4",
			bind:    "0.0.0.0:0",
			knobs: map[string]string{
				"IPV4_ADDRESS": "", "IPV6_ADDRESS": "", "IP_ADDRESS": "",
				"FULL_HOSTNAME": "ep1.chtc.wisc.edu",
			},
			want: "ep1.chtc.wisc.edu",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.NewEmpty()
			for k, v := range tc.knobs {
				cfg.Set(k, v)
			}
			d := testDaemon(t, cfg)

			var lc net.ListenConfig
			ln, err := lc.Listen(t.Context(), tc.network, tc.bind)
			if err != nil {
				t.Skipf("cannot bind %s here: %v", tc.bind, err)
			}
			defer func() { _ = ln.Close() }()
			d.commandListener = ln

			_, port, err := net.SplitHostPort(ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			want := withAlias(cfg, net.JoinHostPort(tc.want, port))
			if got := d.AdvertisedAddress(); got != want {
				t.Errorf("AdvertisedAddress() = %q, want %q", got, want)
			}
		})
	}
}

// A concrete bind address is the operator's or the OS's answer and is
// left alone -- the de-wildcarding must not rewrite a loopback or
// pinned-interface daemon into something it did not ask for.
func TestAdvertisedAddressLeavesAConcreteBindAlone(t *testing.T) {
	cfg := config.NewEmpty()
	cfg.Set("IPV4_ADDRESS", "192.0.2.10")
	cfg.Set("TCP_FORWARDING_HOST", "gw.example.org")
	d := testDaemon(t, cfg)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	d.commandListener = ln

	want := withAlias(cfg, ln.Addr().String())
	if got := d.AdvertisedAddress(); got != want {
		t.Errorf("AdvertisedAddress() = %q, want the bound address %q", got, want)
	}
}

// With nothing to substitute, the address is published as it was: a
// wrong address is bad, an invented one is worse.
func TestAdvertisedAddressKeepsTheWildcardWhenNothingIsKnown(t *testing.T) {
	cfg := config.NewEmpty()
	for _, k := range []string{"TCP_FORWARDING_HOST", "IPV4_ADDRESS", "IPV6_ADDRESS", "IP_ADDRESS", "FULL_HOSTNAME"} {
		cfg.Set(k, "")
	}
	d := testDaemon(t, cfg)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	d.commandListener = ln

	want := withAlias(cfg, ln.Addr().String())
	if got := d.AdvertisedAddress(); got != want {
		t.Errorf("AdvertisedAddress() = %q, want the bound address %q unchanged", got, want)
	}
}
