package authz

import (
	"net"
	"testing"
)

// LEGACY_ALLOW_SEMANTICS selects DCpermissionHierarchy's pre-v9.0 fallback
// order, in which a DAEMON setting falls back to WRITE before DEFAULT. This
// chain resolves ALLOW_<perm>/DENY_<perm>; secSetting in the parent package
// resolves SEC_<perm>_*, and honoured the flag already. Resolving them by
// different orders meant a Go daemon authorized a DAEMON-level request
// differently from the C++ IpVerify reading the same configuration.
//
// The direction matters and is the reason this is a parity bug rather than a
// hole: with ALLOW_DAEMON unset and nothing to fall back to, buildEntry makes
// the entry denyAll (DAEMON is neither READ nor WRITE), so the Go daemon
// REFUSED daemon traffic the C++ one accepted through ALLOW_WRITE.
func TestLegacyAllowSemanticsFallsDaemonBackToWrite(t *testing.T) {
	const peer = "192.0.2.10"
	cfg := mapConfig{
		// ALLOW_DAEMON deliberately unset. ALLOW_DEFAULT too, which is the
		// common case: SECURITY:HOST_BASED sets neither.
		"ALLOW_WRITE":             peer,
		"LEGACY_ALLOW_SEMANTICS":  "TRUE",
		"USE_POOL_USERNAME_EQUIV": "FALSE",
	}
	p := newTestPolicy(t, cfg, nil, map[string][]string{peer: {"peer.example.org"}})

	if !p.Verify(PermDaemon, net.ParseIP(peer), "condor@pool") {
		t.Error("DAEMON refused under LEGACY_ALLOW_SEMANTICS; ALLOW_WRITE should answer for it")
	}
	// WRITE itself is unaffected either way.
	if !p.Verify(PermWrite, net.ParseIP(peer), "condor@pool") {
		t.Error("WRITE refused although ALLOW_WRITE names the peer")
	}
}

// Without the flag the modern order applies: DAEMON goes straight to DEFAULT,
// so ALLOW_WRITE does not answer for it. This is the control -- without it the
// test above is satisfied by a chain that always consults WRITE.
func TestModernSemanticsDoNotFallDaemonBackToWrite(t *testing.T) {
	const peer = "192.0.2.10"
	cfg := mapConfig{
		"ALLOW_WRITE":             peer,
		"USE_POOL_USERNAME_EQUIV": "FALSE",
	}
	p := newTestPolicy(t, cfg, nil, map[string][]string{peer: {"peer.example.org"}})

	if p.Verify(PermDaemon, net.ParseIP(peer), "condor@pool") {
		t.Error("DAEMON allowed from ALLOW_WRITE without LEGACY_ALLOW_SEMANTICS")
	}
	if !p.Verify(PermWrite, net.ParseIP(peer), "condor@pool") {
		t.Error("WRITE refused although ALLOW_WRITE names the peer")
	}
}
