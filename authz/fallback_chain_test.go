package authz

import (
	"slices"
	"testing"
)

// FallbackChain is the modern order only. HTCondor's aConfigNextLegacy, in
// which DAEMON falls back to WRITE, is deliberately not implemented here --
// see FallbackChain's doc comment for why.
func TestFallbackChainIsTheModernOrder(t *testing.T) {
	for _, tc := range []struct {
		perm Perm
		want []Perm
	}{
		// The one level the removed table differed on.
		{PermDaemon, []Perm{PermDaemon, PermDefault}},
		// The ADVERTISE_* levels reach DAEMON, so their chains would have
		// picked up WRITE too under the legacy order.
		{PermAdvertiseStartd, []Perm{PermAdvertiseStartd, PermDaemon, PermDefault}},
		{PermAdvertiseSchedd, []Perm{PermAdvertiseSchedd, PermDaemon, PermDefault}},
		{PermAdvertiseMaster, []Perm{PermAdvertiseMaster, PermDaemon, PermDefault}},
		// Unaffected either way.
		{PermWrite, []Perm{PermWrite, PermDefault}},
		{PermRead, []Perm{PermRead, PermDefault}},
		{PermDefault, []Perm{PermDefault}},
	} {
		t.Run(string(tc.perm), func(t *testing.T) {
			if got := FallbackChain(tc.perm); !slices.Equal(got, tc.want) {
				t.Errorf("FallbackChain(%s) = %v, want %v", tc.perm, got, tc.want)
			}
			if slices.Contains(FallbackChain(tc.perm), PermWrite) && tc.perm != PermWrite {
				t.Errorf("FallbackChain(%s) reaches WRITE; that is the legacy order", tc.perm)
			}
		})
	}
}

// The deprecated two-argument form keeps compiling and now ignores its
// argument. Pinned so the removal cannot be undone by quietly re-honouring it,
// and so a reader sees that true and false give the same chain.
func TestConfigChainIgnoresLegacy(t *testing.T) {
	for _, perm := range allPerms {
		modern := FallbackChain(perm)
		if got := ConfigChain(perm, false); !slices.Equal(got, modern) {
			t.Errorf("ConfigChain(%s, false) = %v, want %v", perm, got, modern)
		}
		if got := ConfigChain(perm, true); !slices.Equal(got, modern) {
			t.Errorf("ConfigChain(%s, true) = %v, want the modern chain %v", perm, got, modern)
		}
	}
}
