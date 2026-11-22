package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// The chooser endpoint and the adapter it serves.

func (f *connectFixture) servePickers(t *testing.T) {
	t.Helper()
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/pickers":
			f.h.handlePickers(w, r)
		case strings.HasPrefix(r.URL.Path, "/picker/"):
			f.h.handlePickerScript(w, r)
		case r.URL.Path == "/api/v1/connect":
			f.h.handleConnectCollection(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/v1/connect/"):
			f.h.handleConnectItem(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func TestPickersListsAConfiguredChooser(t *testing.T) {
	f := newConnectFixtureWithPicker(t, "smith_lab")
	f.servePickers(t)
	c := f.login(t, "alice", "smith_lab")

	resp := f.get(t, c, "/api/v1/pickers", "application/json")
	defer func() { _ = resp.Body.Close() }()

	var got []Picker
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("pickers = %+v, want one", got)
	}
	p := got[0]
	if p.Provider != "gdrive" || p.Connector != "builtin:gdrive" {
		t.Errorf("picker = %+v", p)
	}
	// The page builds file references from these without knowing how
	// the service name is composed.
	if p.Service != "webapi-builtin-gdrive" {
		t.Errorf("Service = %q", p.Service)
	}
	if p.Method != "gdrive2" {
		t.Errorf("Method = %q", p.Method)
	}
	if p.Script != "/picker/gdrive.js" {
		t.Errorf("Script = %q", p.Script)
	}
	if p.APIKey == "" {
		t.Error("the developer key was not passed to the page; the chooser lists nothing without it")
	}
}

// A reference built here is one SplitSpoolInputs parses. One place
// decides the spelling, so the page cannot drift from the parser.
func TestFileReferenceRoundTripsThroughTheParser(t *testing.T) {
	p := Picker{Service: "webapi-builtin-gdrive", Method: "gdrive2"}
	ref := p.FileReference("FILEID", "data.csv")

	parsed, err := parseRefForTest(ref)
	if err != nil {
		t.Fatalf("the submit parser refused a reference this page builds: %v", err)
	}
	if parsed.Service != p.Service || parsed.FileID != "FILEID" || parsed.Name != "data.csv" {
		t.Errorf("parsed %+v from %q", parsed, ref)
	}
}

// A chooser nobody may use is not listed: the alternative is a button
// that opens a chooser and then refuses.
func TestPickersOmitsChoosersOutsideYourGroups(t *testing.T) {
	f := newConnectFixtureWithPicker(t, "smith_lab")
	f.servePickers(t)
	c := f.login(t, "mallory", "other_lab")

	resp := f.get(t, c, "/api/v1/pickers", "application/json")
	defer func() { _ = resp.Body.Close() }()
	var got []Picker
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if len(got) != 0 {
		t.Errorf("pickers = %+v, want none for a non-member", got)
	}
}

// A misconfigured chooser is not offered. Offering one is how a person
// meets the failure instead of the operator who can fix it.
func TestAMisconfiguredChooserIsNotOffered(t *testing.T) {
	f := newConnectFixtureNoPickerKey(t, "smith_lab")
	f.servePickers(t)
	c := f.login(t, "alice", "smith_lab")

	resp := f.get(t, c, "/api/v1/pickers", "application/json")
	defer func() { _ = resp.Body.Close() }()
	var got []Picker
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if len(got) != 0 {
		t.Errorf("a chooser with no developer key was offered: %+v", got)
	}
}

func TestPickersNeedsASession(t *testing.T) {
	f := newConnectFixtureWithPicker(t, "smith_lab")
	f.servePickers(t)

	resp := f.get(t, anonymous(), "/api/v1/pickers", "application/json")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", resp.StatusCode)
	}
}

// --- the adapter ------------------------------------------------------

func TestAdapterIsServedForAKnownProvider(t *testing.T) {
	f := newConnectFixtureWithPicker(t, "smith_lab")
	f.servePickers(t)
	c := f.login(t, "alice", "smith_lab")

	resp := f.get(t, c, "/picker/gdrive.js", "")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := readBody(t, resp)
	// The contract the page depends on.
	if !strings.Contains(body, "window.htcondorPicker") {
		t.Error("the adapter does not define the chooser contract")
	}
	// And it is the adapter that reaches the provider, which is why
	// script-src names that origin rather than the page needing it.
	if !strings.Contains(body, "https://apis.google.com") {
		t.Error("the adapter does not load the provider's script")
	}
}

// The path is not a way to read the embedded filesystem.
func TestAdapterPathIsNotATraversal(t *testing.T) {
	f := newConnectFixtureWithPicker(t, "smith_lab")
	f.servePickers(t)
	c := f.login(t, "alice", "smith_lab")

	for _, p := range []string{
		"/picker/nosuch.js",
		"/picker/gdrive",
		"/picker/",
		"/picker/..%2f..%2fgo.mod",
	} {
		resp := f.get(t, c, p, "")
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s returned 200", p)
		}
	}
}

// Every adapter shipped corresponds to a registered provider.
//
// This is what the allowlist in handlePickerScript is really for.
// embed.FS already refuses traversal and the suffix check refuses the
// rest, so the allowlist's only effect is on a file that exists in
// pickerjs/ without a provider to configure it -- an adapter nobody
// can reach, which is either dead weight or a provider somebody forgot
// to register.
func TestEveryShippedAdapterHasARegisteredProvider(t *testing.T) {
	entries, err := pickerAdapters.ReadDir("pickerjs")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no adapters are shipped at all")
	}
	for _, e := range entries {
		provider := strings.TrimSuffix(e.Name(), ".js")
		if provider == e.Name() {
			t.Errorf("pickerjs/%s is not a .js adapter", e.Name())
			continue
		}
		if _, ok := connectors.LookupPickerProvider(provider); !ok {
			t.Errorf("pickerjs/%s ships an adapter for %q, which is not a registered "+
				"provider -- nothing can configure a chooser that uses it",
				e.Name(), provider)
		}
	}
}

// And every registered provider has an adapter, or a chooser an
// operator can configure loads nothing.
func TestEveryRegisteredProviderHasAnAdapter(t *testing.T) {
	for _, name := range connectors.PickerProviderNames() {
		if _, err := pickerAdapters.ReadFile("pickerjs/" + name + ".js"); err != nil {
			t.Errorf("provider %q is registered but ships no adapter: %v", name, err)
		}
	}
}

// Disabling a connector takes its chooser off the submit page.
//
// The chooser borrows that connector's credential and has no other way
// to show anybody's files, so a disabled connector's chooser is a button
// that opens and then cannot list anything. Found by mutation: removing
// the Enabled check left every test passing.
func TestADisabledConnectorsChooserIsNotOffered(t *testing.T) {
	f := newConnectFixtureFull(t, "smith_lab", true, false)
	f.servePickers(t)
	c := f.login(t, "alice", "smith_lab")

	resp := f.get(t, c, "/api/v1/pickers", "application/json")
	defer func() { _ = resp.Body.Close() }()
	var got []Picker
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("pickers = %+v, want none while the connector is disabled", got)
	}
}

// A connector open to everybody offers its chooser to everybody.
//
// The group list is closed by default -- an empty one permits nobody --
// so `*` is the only way to say "anyone who can log in". Worth its own
// case because the two are one character apart and the wrong one fails
// silently: the chooser simply is not there.
func TestAnUnrestrictedChoosersIsOfferedToAnyone(t *testing.T) {
	f := newConnectFixtureWithPicker(t, "*")
	f.servePickers(t)
	c := f.login(t, "nobody_in_particular", "no_lab_at_all")

	resp := f.get(t, c, "/api/v1/pickers", "application/json")
	defer func() { _ = resp.Body.Close() }()
	var got []Picker
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("pickers = %+v, want the one chooser", got)
	}
}
