package connectors

import (
	"fmt"
	"sort"
	"strings"
)

// Pickers: the browser file choosers an access point offers.
//
// A picker is NOT a property of a connector, and an earlier version of
// this made it one. The giveaway was the developer key: Google's Picker
// needs setDeveloperKey and an app id, which belong to a Google Cloud
// project and are not a thing a credential has. A boolean on the
// connector had nowhere to put them.
//
// What a picker actually is:
//
//   - A PROVIDER's chooser. Google's picks Drive files. The origins it
//     needs in the page's Content-Security-Policy are the provider's,
//     so two connectors for the same provider share one chooser and
//     one set of origins.
//   - A thing that BORROWS a connector's credential, because a chooser
//     needs a token to show anybody's files.
//
// So a picker is configured in its own right and names the connector it
// authorizes with, rather than a connector carrying a flag. That also
// puts the Content-Security-Policy consequence where it belongs: the
// policy is a property of the PAGE, widened once per configured
// chooser, not once per credential that happens to exist.

// PickerProvider is a shipped chooser definition: what a provider's
// file chooser needs in order to run in a browser.
type PickerProvider struct {
	// Script, Frame, Connect and Img are the origins the page's
	// Content-Security-Policy must name. Each fails differently and
	// all of them look like "the chooser does not work":
	//
	//   - Script: the chooser's own code will not load.
	//   - Frame:  it loads and renders an empty box, because a CSP
	//             with no frame-src falls back to default-src.
	//   - Connect: it renders and lists nothing.
	//   - Img:    it lists files with broken thumbnails.
	//
	// This is the one part of the catalogue that CANNOT be verified
	// without a real browser, and a wrong origin fails exactly like a
	// missing one.
	Script  []string
	Frame   []string
	Connect []string
	Img     []string

	// Method is the transfer-plugin method a file chosen here is named
	// with, so the submit file says <service>+<method>://<id>/<name>.
	Method string

	// NeedsAPIKey reports that this provider's chooser will not run
	// without a developer key -- the setting that showed a picker is
	// not a connector flag.
	NeedsAPIKey bool

	// Summary is one line for the admin page.
	Summary string
}

// pickerProviders is the shipped catalogue, by provider name.
var pickerProviders = map[string]PickerProvider{
	"gdrive": {
		// The Picker loads from apis.google.com, renders its chooser in
		// a docs.google.com iframe, and calls googleapis.com.
		//
		// The API key an operator supplies here may be restricted by
		// HTTP referrer ONLY if this server sends a Referrer-Policy
		// that survives a cross-origin navigation. The chooser's iframe
		// URL carries the key, and that navigation is cross-origin, so
		// a policy of same-origin or no-referrer makes the browser send
		// no Referer and the provider rejects the key -- reported as
		// "The API developer key is invalid", which names the key and
		// not the referrer. See applySecurityHeaders, which sends
		// strict-origin-when-cross-origin for this reason.
		Script:      []string{"https://apis.google.com"},
		Frame:       []string{"https://docs.google.com"},
		Connect:     []string{"https://content.googleapis.com", "https://www.googleapis.com"},
		Img:         []string{"https://*.googleusercontent.com"},
		Method:      "gdrive2",
		NeedsAPIKey: true,
		Summary:     "Google Drive file chooser",
	},
}

// LookupPickerProvider returns a shipped chooser definition.
func LookupPickerProvider(name string) (PickerProvider, bool) {
	p, ok := pickerProviders[name]
	return p, ok
}

// PickerProviderNames lists the catalogue, for the admin page.
func PickerProviderNames() []string {
	out := make([]string, 0, len(pickerProviders))
	for name := range pickerProviders {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Picker is one configured chooser.
type Picker struct {
	// Name is what the operator called it, e.g. "drive".
	Name string
	// Provider selects the shipped definition.
	Provider string
	// Connector is whose credential it borrows. A chooser cannot show
	// anybody's files without a token.
	Connector ID
	// APIKey and AppID are the provider's, not the credential's. This
	// is the pair that does not fit on a connector.
	APIKey string
	AppID  string
	// Origin is where the page loads the chooser's adapter from. Empty
	// means this server's own; a test points it at a stand-in so the
	// production path and the tested path are the same path.
	Origin string
}

// Def returns the shipped definition behind this picker.
func (p Picker) Def() (PickerProvider, bool) { return LookupPickerProvider(p.Provider) }

// pickerConfigPrefix is the start of every picker parameter name.
const pickerConfigPrefix = "HTTP_API_PICKER_"

// PickerConfigKey is the full parameter name for one of a picker's
// settings.
func PickerConfigKey(name, setting string) string {
	return pickerConfigPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_" + setting
}

// Pickers returns the choosers the configuration declares, by name.
//
// Driven by HTTP_API_PICKERS rather than by scanning, so a half-written
// chooser is not picked up -- the listing is the operator saying this
// one is ready, the same rule connectors follow.
func (c *ConfigSource) Pickers() []Picker {
	if c == nil || c.params == nil {
		return nil
	}
	raw, ok := c.params.Get("HTTP_API_PICKERS")
	if !ok {
		return nil
	}

	var out []Picker
	seen := map[string]bool{}
	for _, name := range splitList(raw) {
		if seen[name] {
			continue
		}
		seen[name] = true

		p := Picker{Name: name}
		p.Provider, _ = c.pickerGet(name, "PROVIDER")
		if p.Provider == "" {
			// The name is the provider unless told otherwise, so the
			// common case is one line.
			p.Provider = name
		}
		if v, ok := c.pickerGet(name, "CONNECTOR"); ok {
			id, err := ParseID(v)
			if err != nil {
				continue
			}
			p.Connector = id
		}
		p.APIKey, _ = c.pickerGet(name, "API_KEY")
		p.AppID, _ = c.pickerGet(name, "APP_ID")
		p.Origin, _ = c.pickerGet(name, "ORIGIN")
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *ConfigSource) pickerGet(name, setting string) (string, bool) {
	v, ok := c.params.Get(PickerConfigKey(name, setting))
	if !ok {
		return "", false
	}
	if v = strings.TrimSpace(v); v == "" {
		return "", false
	}
	return v, true
}

// Problems reports what would stop this chooser working, for the admin
// page's test action.
//
// A chooser that is wrong should say so when it is configured, not when
// somebody opens the submit page and sees nothing happen -- the browser
// console is the only other place the reason appears.
func (p Picker) Problems() []string {
	var out []string
	def, ok := p.Def()
	if !ok {
		return []string{fmt.Sprintf("no chooser ships for the provider %q; known: %s",
			p.Provider, strings.Join(PickerProviderNames(), ", "))}
	}
	if p.Connector == (ID{}) {
		out = append(out, fmt.Sprintf("names no connector, so it has no credential to show files with (set %s)",
			PickerConfigKey(p.Name, "CONNECTOR")))
	}
	if def.NeedsAPIKey && p.APIKey == "" {
		out = append(out, fmt.Sprintf("this provider's chooser will not run without a developer key (set %s)",
			PickerConfigKey(p.Name, "API_KEY")))
	}
	return out
}
