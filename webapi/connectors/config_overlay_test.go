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
