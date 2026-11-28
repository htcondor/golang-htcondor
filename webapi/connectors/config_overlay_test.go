package connectors

import "testing"

// A shipped definition supplies scopes; an operator setting overlays
// them. An absent SCOPES keeps the shipped ones, and an explicitly
// empty one means none -- the difference between "nothing said" and
// "nothing wanted", which a read that only reported a value could not
// express.
func TestConnectorScopesOverlay(t *testing.T) {
	shipped := func(t *testing.T) []string {
		t.Helper()
		def, ok := Lookup(ID{Namespace: "builtin", Name: "gdrive"})
		if !ok {
			t.Skip("no builtin:gdrive to overlay")
		}
		return def.DefaultScopes
	}
	base := shipped(t)
	if len(base) == 0 {
		t.Skip("builtin:gdrive ships no scopes to override")
	}

	cases := []struct {
		name  string
		extra map[string]string
		want  int
	}{
		{"absent keeps the shipped scopes", nil, len(base)},
		{"explicitly empty means none", map[string]string{
			"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_SCOPES": "[]"}, 0},
		{"a value replaces them", map[string]string{
			"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_SCOPES": "one, two"}, 2},
	}
	for _, tc := range cases {
		//nolint:gosec // G101: parameter names and a path, not credentials
		params := fakeParams{
			"HTTP_API_CONNECTORS":                                  "builtin:gdrive",
			"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_ID":          "id",
			"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_SECRET_FILE": "/dev/null",
		}
		for k, v := range tc.extra {
			params[k] = v
		}
		src := NewConfigSource(params)
		d, ok := src.Definition(ID{Namespace: "builtin", Name: "gdrive"})
		if !ok {
			t.Fatalf("%s: no definition", tc.name)
		}
		if len(d.Scopes) != tc.want {
			t.Errorf("%s: scopes = %q, want %d", tc.name, d.Scopes, tc.want)
		}
	}
}

// A provider documents its scopes space-separated, because RFC 6749
// delimits them that way, and an operator pastes them as written.
//
// This read comma-only until 2026-11-28, which made the pasted form one
// scope with spaces in it. It reached the provider correctly anyway --
// x/oauth2 joins Scopes with a space, so one element containing spaces
// and three elements produce the same scope parameter -- so nothing
// failed and nothing would have. The spelling is fixed because the next
// thing to read an individual scope would have been wrong, not because
// anything was visibly broken.
func TestConnectorScopesAcceptEitherSpelling(t *testing.T) {
	for _, raw := range []string{
		"openid profile email",
		"openid,profile,email",
		"openid, profile, email",
		`["openid","profile","email"]`,
	} {
		src := NewConfigSource(fakeParams{
			"HTTP_API_CONNECTORS":                                  "builtin:gdrive",
			"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_ID":          "id",
			"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_SECRET_FILE": "/dev/null",
			"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_SCOPES":             raw,
		})
		d, ok := src.Definition(ID{Namespace: "builtin", Name: "gdrive"})
		if !ok {
			t.Fatalf("%q: no definition", raw)
		}
		want := []string{"openid", "profile", "email"}
		if len(d.Scopes) != len(want) {
			t.Errorf("%q -> %q, want %q", raw, d.Scopes, want)
			continue
		}
		for i := range want {
			if d.Scopes[i] != want[i] {
				t.Errorf("%q -> %q, want %q", raw, d.Scopes, want)
				break
			}
		}
	}
}
