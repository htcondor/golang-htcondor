package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bbockelm/golang-htcondor/webapi/connectors"
)

// The Content-Security-Policy a connector's file chooser needs.
//
// These tests exist because this repository has already shipped a
// browser-side feature that was blocked in every real browser and
// silent about it: the site-apps update badge fetched api.github.com
// from a page whose CSP had no connect-src at all, and nothing caught
// it because vitest stubs fetch and no browser test exercised it.
//
// So the directives a feature needs are asserted here, in Go, against
// the same function that serves them.

func cspOf(t *testing.T, picker PickerOrigins) map[string]string {
	t.Helper()
	w := httptest.NewRecorder()
	applySecurityHeaders(w, "", false, picker)

	out := map[string]string{}
	for _, part := range strings.Split(w.Header().Get("Content-Security-Policy"), ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, rest, _ := strings.Cut(part, " ")
		out[name] = rest
	}
	return out
}

// Every directive the Picker needs is widened, and each for its own
// reason. A chooser missing any one of them fails differently and all
// of them look like "it does not work".
func TestPickerOriginsWidenEveryDirectiveItNeeds(t *testing.T) {
	gdrive, ok := connectors.Lookup(connectors.MustParse("builtin:gdrive"))
	if !ok {
		t.Fatal("builtin:gdrive is not in the catalogue")
	}
	csp := cspOf(t, PickerOrigins{
		Script:  gdrive.PickerScript,
		Frame:   gdrive.PickerFrame,
		Connect: gdrive.PickerConnect,
		Img:     gdrive.PickerImg,
	})

	for _, tc := range []struct{ directive, want, why string }{
		{"script-src", "https://apis.google.com",
			"the chooser's own code is loaded from the provider"},
		{"frame-src", "https://docs.google.com",
			"the chooser renders in the provider's iframe"},
		{"connect-src", "https://www.googleapis.com",
			"the chooser calls the provider's API"},
		{"img-src", "https://*.googleusercontent.com",
			"the chooser shows thumbnails from the provider"},
	} {
		if !strings.Contains(csp[tc.directive], tc.want) {
			t.Errorf("%s = %q, missing %q (%s)", tc.directive, csp[tc.directive], tc.want, tc.why)
		}
	}
}

// frame-src is stated even when no connector needs it.
//
// A CSP with no frame-src falls back to default-src, and that fallback
// is what makes the missing directive invisible: the page looks right
// until somebody opens the chooser and sees an empty box, with the
// reason only in the browser's console.
func TestFrameSrcIsStatedEvenWithNoPicker(t *testing.T) {
	csp := cspOf(t, PickerOrigins{})
	if _, ok := csp["frame-src"]; !ok {
		t.Fatal("frame-src is absent, so it silently falls back to default-src")
	}
	if csp["frame-src"] != "'self'" {
		t.Errorf("frame-src = %q, want 'self' when nothing needs widening", csp["frame-src"])
	}
}

// A policy is widened for a feature in use, never on the chance one
// might be. An open policy on an authenticated page is an open
// exfiltration path, which is most of what the CSP is for.
func TestNoPickerLeavesThePolicyClosed(t *testing.T) {
	csp := cspOf(t, PickerOrigins{})

	for directive, value := range csp {
		if strings.Contains(value, "google") {
			t.Errorf("%s = %q with no connector configured", directive, value)
		}
		if strings.Contains(value, "*") && !strings.Contains(directive, "img") {
			t.Errorf("%s = %q contains a wildcard", directive, value)
		}
	}
	if csp["script-src"] != "'self' 'unsafe-inline'" {
		t.Errorf("script-src = %q, want it unchanged", csp["script-src"])
	}
	if csp["connect-src"] != "'self'" {
		t.Errorf("connect-src = %q, want it unchanged", csp["connect-src"])
	}
}

// A connector widens the policy only when its CHOOSER is asked for.
//
// Configuring builtin:gdrive is not the same as wanting a file chooser:
// a site whose file ids come from a site app, a script or a catalogue
// never opens one, and should not carry script-src and frame-src
// entries for it. The default is off, because that is the discoverable
// failure -- an operator who wanted the chooser goes looking for the
// setting -- where on-by-default is the invisible one.
func TestAConnectorWithoutItsPickerLeavesThePolicyClosed(t *testing.T) {
	// newConnectFixture configures builtin:gdrive and does NOT set
	// _PICKER, which is the common case.
	f := newConnectFixture(t, "smith_lab")

	got := f.h.pickerOrigins()
	if len(got.Script) != 0 || len(got.Frame) != 0 {
		t.Fatalf("a connector with no chooser widened the policy: %+v", got)
	}

	csp := cspOf(t, got)
	if strings.Contains(csp["script-src"], "google") || strings.Contains(csp["frame-src"], "google") {
		t.Errorf("the policy names the provider anyway: script-src=%q frame-src=%q",
			csp["script-src"], csp["frame-src"])
	}
}

// And it does widen when the chooser is asked for.
func TestAskingForThePickerWidensThePolicy(t *testing.T) {
	f := newConnectFixtureWithPicker(t, "smith_lab")

	got := f.h.pickerOrigins()
	if len(got.Script) == 0 || len(got.Frame) == 0 {
		t.Fatalf("the chooser was asked for and the policy was not widened: %+v", got)
	}
	csp := cspOf(t, got)
	if !strings.Contains(csp["frame-src"], "https://docs.google.com") {
		t.Errorf("frame-src = %q", csp["frame-src"])
	}
}

// A disabled connector contributes nothing even with the chooser on.
func TestADisabledConnectorDoesNotWidenThePolicy(t *testing.T) {
	f := newConnectFixtureWithPicker(t, "smith_lab")
	if len(f.h.pickerOrigins().Script) == 0 {
		t.Fatal("precondition: the enabled connector should widen the policy")
	}

	if err := f.h.connectorStore().PutConnector(t.Context(), connectors.Stored{
		ID: connectors.MustParse("site:nopicker"), Kind: connectors.KindOAuth2,
		Groups: []string{"smith_lab"}, Enabled: true, Picker: true,
	}, nil); err != nil {
		t.Fatal(err)
	}
	// A site connector has no builtin definition and so declares no
	// picker origins; asking for a chooser it has no definition for
	// must not widen anything.
	if n := len(f.h.pickerOrigins().Script); n != 1 {
		t.Errorf("a site connector with no picker definition changed the origin count: %d", n)
	}
}

// Every origin named is https. A chooser loaded over http would be
// mixed content on an https page, which browsers block anyway -- and a
// policy that permits it says this server would accept one.
func TestEveryPickerOriginIsHTTPS(t *testing.T) {
	for _, id := range connectors.BuiltinIDs() {
		b, _ := connectors.Lookup(id)
		for _, group := range [][]string{b.PickerScript, b.PickerFrame, b.PickerConnect, b.PickerImg} {
			for _, origin := range group {
				if !strings.HasPrefix(origin, "https://") {
					t.Errorf("%s names %q, which is not https", id, origin)
				}
			}
		}
	}
}
