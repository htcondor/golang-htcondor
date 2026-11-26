package htcondor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/htcondor/cedar/commands"
	"github.com/htcondor/golang-htcondor/config"
)

// writeToken drops a plausible token file and returns its path.
func writeToken(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("eyJhbGciOiJSUzI1NiJ9.e30.sig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSciTokenFileDiscovery: a client offering SCITOKENS must find its
// token where condor_auth_ssl looks for it.
//
// C++ reads SCITOKENS_FILE when set and otherwise calls
// htcondor::discover_token(), whose file arms are $BEARER_TOKEN_FILE,
// $XDG_RUNTIME_DIR/bt_u<euid> and /tmp/bt_u<euid>, in that order.
// Neither was wired here and cedar has no discovery of its own, so
// TokenFile was never set at all: a Go client could present a SciToken
// only if a caller injected one, or if one happened to be sitting loose
// in the IDTOKEN directory.
//
// The failure is fail-closed -- cedar's signalSetupFailure abandons just
// that method -- so the symptom is a connection with no SciToken
// identity, which then matches no ALLOW_ entry naming it.
func TestSciTokenFileDiscovery(t *testing.T) {
	dir := t.TempDir()
	scitokensFile := writeToken(t, dir, "scitokens.use")
	bearerFile := writeToken(t, dir, "bearer.use")
	xdgDir := t.TempDir()
	xdgFile := writeToken(t, xdgDir, fmt.Sprintf("bt_u%d", os.Geteuid()))

	for _, tc := range []struct {
		name    string
		knob    string
		env     map[string]string
		want    string
		wantAny bool
	}{
		{
			name: "SCITOKENS_FILE wins over everything",
			knob: scitokensFile,
			env:  map[string]string{"BEARER_TOKEN_FILE": bearerFile, "XDG_RUNTIME_DIR": xdgDir},
			want: scitokensFile,
		},
		{
			name: "BEARER_TOKEN_FILE when the knob is unset",
			env:  map[string]string{"BEARER_TOKEN_FILE": bearerFile, "XDG_RUNTIME_DIR": xdgDir},
			want: bearerFile,
		},
		{
			name: "XDG_RUNTIME_DIR/bt_u<euid> when neither is set",
			env:  map[string]string{"XDG_RUNTIME_DIR": xdgDir},
			want: xdgFile,
		},
		{
			// A BEARER_TOKEN_FILE naming nothing falls through rather
			// than pinning a path that cannot be read.
			name: "a missing BEARER_TOKEN_FILE falls through",
			env: map[string]string{
				"BEARER_TOKEN_FILE": filepath.Join(dir, "absent"),
				"XDG_RUNTIME_DIR":   xdgDir,
			},
			want: xdgFile,
		},
		{
			name: "nothing to find",
			env:  map[string]string{"XDG_RUNTIME_DIR": t.TempDir()},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Clear both so a developer's own environment cannot
			// decide the result.
			t.Setenv("BEARER_TOKEN_FILE", "")
			t.Setenv("XDG_RUNTIME_DIR", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg := config.NewEmpty()
			cfg.Set("SEC_DEFAULT_AUTHENTICATION_METHODS", "SCITOKENS")
			if tc.knob != "" {
				cfg.Set("SCITOKENS_FILE", tc.knob)
			}

			sc, err := GetSecurityConfig(cfg, commands.DC_NOP, "CLIENT")
			if err != nil {
				t.Fatalf("GetSecurityConfig: %v", err)
			}
			// /tmp/bt_u<euid> may exist on a developer's machine, so
			// the "nothing to find" case only asserts it is not one of
			// the fixtures.
			if tc.want == "" {
				if sc.TokenFile == scitokensFile || sc.TokenFile == bearerFile || sc.TokenFile == xdgFile {
					t.Errorf("TokenFile = %q, want none of the fixtures", sc.TokenFile)
				}
				return
			}
			if sc.TokenFile != tc.want {
				t.Errorf("TokenFile = %q, want %q", sc.TokenFile, tc.want)
			}
		})
	}
}

// TestIDTokensOnlyClientReadsNoBearerToken: TokenFile is shared with the
// IDTOKENS path, so the discovery is gated on SCITOKENS being offered. A
// client that only speaks IDTOKENS has no business reading a bearer
// token.
func TestIDTokensOnlyClientReadsNoBearerToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEARER_TOKEN_FILE", writeToken(t, dir, "bearer.use"))
	t.Setenv("XDG_RUNTIME_DIR", dir)

	cfg := config.NewEmpty()
	cfg.Set("SEC_DEFAULT_AUTHENTICATION_METHODS", "IDTOKENS")
	cfg.Set("SCITOKENS_FILE", writeToken(t, dir, "scitokens.use"))

	sc, err := GetSecurityConfig(cfg, commands.DC_NOP, "CLIENT")
	if err != nil {
		t.Fatalf("GetSecurityConfig: %v", err)
	}
	if sc.TokenFile != "" {
		t.Errorf("TokenFile = %q, want empty for an IDTOKENS-only client", sc.TokenFile)
	}
}

// TestSuppliedTokenStillClearsTokenFile: a caller that hands over a
// token has made the identity decision itself, and that path
// deliberately clears TokenFile/TokenDir. Discovery must not put one
// back.
func TestSuppliedTokenStillClearsTokenFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BEARER_TOKEN_FILE", writeToken(t, dir, "bearer.use"))

	cfg := config.NewEmpty()
	cfg.Set("SEC_DEFAULT_AUTHENTICATION_METHODS", "SCITOKENS")

	sc, err := NewClientSecurityConfigWithConfig(context.Background(), cfg,
		"a.token.value", "collector.example.org", int(commands.DC_NOP), "CLIENT", nil)
	if err != nil {
		t.Fatalf("NewClientSecurityConfigWithConfig: %v", err)
	}
	if sc.TokenFile != "" {
		t.Errorf("TokenFile = %q, want empty when the caller supplied a token", sc.TokenFile)
	}
	if sc.Token != "a.token.value" {
		t.Errorf("Token = %q, want the supplied one", sc.Token)
	}
}
