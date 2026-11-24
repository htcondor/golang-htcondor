package connectors

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/htcondor/golang-htcondor/config"
	"github.com/htcondor/golang-htcondor/webapi/knobs"
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
	// shared, when set, says this chooser's name folds onto the same
	// parameters as another's, which Problems reports on its own. It
	// cannot be worked out from one Picker, so Pickers fills it in.
	shared string
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
//
// The settings this package reads come from knobs.Picker, which spells
// its own names; this remains for a caller naming a setting that is not
// one of them. The fold from a chooser's name to the parameter form is
// config.InstanceID, shared with every other family.
func PickerConfigKey(name, setting string) string {
	return pickerConfigPrefix + config.InstanceID(name) + "_" + setting
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
	names := knobs.PickerInstances.Get(c.params)
	if len(names) == 0 {
		return nil
	}

	// Two names differing only by hyphen-versus-underscore fold onto
	// one set of parameters, so neither chooser can be configured on
	// its own: both would read the same connector and the same
	// developer key while presenting as two. Nothing constrains a
	// chooser's name, so unlike connectors this is reachable, and
	// until now it produced two identically configured choosers on the
	// submit page with no complaint anywhere.
	// Dedupe first: a name written twice is one chooser, and feeding
	// the repeat to the collision check would report it as two
	// choosers fighting over one set of parameters.
	distinct := make([]string, 0, len(names))
	listed := map[string]bool{}
	for _, n := range names {
		if !listed[n] {
			listed[n] = true
			distinct = append(distinct, n)
		}
	}
	names = distinct

	collisions := config.InstanceCollisions(names)
	shared := map[string]string{}
	for id, group := range collisions {
		for _, n := range group {
			shared[n] = fmt.Sprintf(
				"shares the configuration name %s%s_ with %s, so neither can be configured on its "+
					"own; rename one of them",
				pickerConfigPrefix, id, strings.Join(quoteEach(exclude(group, n)), " and "))
		}
	}

	var out []Picker
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true

		set := knobs.Picker(name)
		p := Picker{Name: name, shared: shared[name]}
		p.Provider = set.Provider().Get(c.params)
		if p.Provider == "" {
			// The name is the provider unless told otherwise, so the
			// common case is one line.
			p.Provider = name
		}
		if v := set.Connector().Get(c.params); v != "" {
			id, err := ParseID(v)
			if err != nil {
				continue
			}
			p.Connector = id
		}
		p.APIKey = set.APIKey().Get(c.params)
		p.AppID = set.AppID().Get(c.params)
		p.Origin = set.Origin().Get(c.params)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// exclude returns names without one of them, for naming the others in
// a collision message.
func exclude(names []string, drop string) []string {
	var out []string
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

// quoteEach quotes names for a message.
func quoteEach(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strconv.Quote(n))
	}
	return out
}

// Problems reports what would stop this chooser working, for the admin
// page's test action.
//
// A chooser that is wrong should say so when it is configured, not when
// somebody opens the submit page and sees nothing happen -- the browser
// console is the only other place the reason appears.
func (p Picker) Problems() []string {
	var out []string
	if p.shared != "" {
		// Reported first and on its own: with two choosers reading one
		// set of parameters, every other complaint about either is
		// downstream of that.
		return []string{p.shared}
	}
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
