// Code generated from knobs.yaml by go generate; DO NOT EDIT.

package knobs

import (
	"time"

	"github.com/htcondor/golang-htcondor/config"
)

// IdleTimeout is HTTP_API_IDLE_TIMEOUT.
//
// How long a keep-alive connection may sit idle before the server
// closes it, as a Go duration.
var IdleTimeout = config.DefineDuration("HTTP_API_IDLE_TIMEOUT", time.Duration(120000000000) /* 120s */, config.Startup,
	"How long a keep-alive connection may sit idle before the server closes it, as a Go duration.")

// ReadTimeout is HTTP_API_READ_TIMEOUT.
//
// How long a request may take to arrive in full, as a Go duration. A
// value that cannot be parsed is logged and the default applies.
var ReadTimeout = config.DefineDuration("HTTP_API_READ_TIMEOUT", time.Duration(30000000000) /* 30s */, config.Startup,
	"How long a request may take to arrive in full, as a Go duration. A value that cannot be parsed is logged and the default applies.")

// TLSCACert is HTTP_API_TLS_CA_CERT.
//
// CA bundle used to verify client certificates. Unset asks for no
// client certificate.
var TLSCACert = config.DefineString("HTTP_API_TLS_CA_CERT", "", config.Reconfig,
	"CA bundle used to verify client certificates. Unset asks for no client certificate.")

// TLSCert is HTTP_API_TLS_CERT.
//
// PEM certificate for serving HTTPS directly, rather than behind a
// terminating proxy. Needs HTTP_API_TLS_KEY; neither alone enables
// TLS.
var TLSCert = config.DefineString("HTTP_API_TLS_CERT", "", config.Reconfig,
	"PEM certificate for serving HTTPS directly, rather than behind a terminating proxy. Needs HTTP_API_TLS_KEY; neither alone enables TLS.")

// TLSKey is HTTP_API_TLS_KEY.
//
// Private key for HTTP_API_TLS_CERT. Keep it readable only by the
// account the server runs as.
var TLSKey = config.DefineString("HTTP_API_TLS_KEY", "", config.Reconfig,
	"Private key for HTTP_API_TLS_CERT. Keep it readable only by the account the server runs as.")

// WriteTimeout is HTTP_API_WRITE_TIMEOUT.
//
// How long a response may take to write, as a Go duration. This bounds
// a slow client, so it also bounds a deliberately long poll; see the
// note on streaming endpoints before lowering it.
var WriteTimeout = config.DefineDuration("HTTP_API_WRITE_TIMEOUT", time.Duration(30000000000) /* 30s */, config.Startup,
	"How long a response may take to write, as a Go duration. This bounds a slow client, so it also bounds a deliberately long poll; see the note on streaming endpoints before lowering it.")

// ConnectorSettings names the Connector settings of one instance.
//
// An external account users may connect, so a job can read their files
// from it. HTTP_API_CONNECTORS names the connectors offered; each is
// configured by the settings below with its id folded in, e.g.
// HTTP_API_CONNECTOR_BUILTIN_GDRIVE_GROUPS for builtin:gdrive.
type ConnectorSettings struct{ id string }

// Connector returns the settings of one Connector instance. The name is
// folded into the parameter form here, once, so no caller repeats it.
func Connector(instance string) ConnectorSettings {
	return ConnectorSettings{config.InstanceID(instance)}
}

// ConnectorInstances is the parameter naming every Connector that exists.
var ConnectorInstances = config.DefineList("HTTP_API_CONNECTORS", nil, config.Startup,
	"Names the Connector instances that exist. An external account users may connect, so a job can read their files from it. HTTP_API_CONNECTORS names the connectors offered; each is configured by the settings below with its id folded in, e.g. HTTP_API_CONNECTOR_BUILTIN_GDRIVE_GROUPS for builtin:gdrive.")

// Kind is HTTP_API_CONNECTOR_<instance>_KIND.
//
// Which shipped connector definition to use, for an id that is not
// itself one.
func (s ConnectorSettings) Kind() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_KIND", "", config.Startup,
		"Which shipped connector definition to use, for an id that is not itself one.")
}

// ClientID is HTTP_API_CONNECTOR_<instance>_CLIENT_ID.
//
// The OAuth2 client id this access point registered with the provider.
func (s ConnectorSettings) ClientID() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_CLIENT_ID", "", config.Startup,
		"The OAuth2 client id this access point registered with the provider.")
}

// ClientSecretFile is HTTP_API_CONNECTOR_<instance>_CLIENT_SECRET_FILE.
//
// File holding the OAuth2 client secret. A connector with no readable
// secret is refused rather than offered.
func (s ConnectorSettings) ClientSecretFile() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_CLIENT_SECRET_FILE", "", config.Startup,
		"File holding the OAuth2 client secret. A connector with no readable secret is refused rather than offered.")
}

// AuthorizationURL is HTTP_API_CONNECTOR_<instance>_AUTHORIZATION_URL.
//
// The provider's authorization endpoint, for a connector that is not a
// shipped one.
func (s ConnectorSettings) AuthorizationURL() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_AUTHORIZATION_URL", "", config.Startup,
		"The provider's authorization endpoint, for a connector that is not a shipped one.")
}

// TokenURL is HTTP_API_CONNECTOR_<instance>_TOKEN_URL.
//
// The provider's token endpoint, for a connector that is not a shipped
// one.
func (s ConnectorSettings) TokenURL() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_TOKEN_URL", "", config.Startup,
		"The provider's token endpoint, for a connector that is not a shipped one.")
}

// Scopes is HTTP_API_CONNECTOR_<instance>_SCOPES.
//
// The scopes requested when a user connects this account.
func (s ConnectorSettings) Scopes() config.ListKnob {
	return config.DefineList("HTTP_API_CONNECTOR_"+s.id+"_SCOPES", nil, config.Startup,
		"The scopes requested when a user connects this account.")
}

// Groups is HTTP_API_CONNECTOR_<instance>_GROUPS.
//
// Group(s) whose members may connect this account. Unset offers it to
// everyone who may sign in.
func (s ConnectorSettings) Groups() config.ListKnob {
	return config.DefineList("HTTP_API_CONNECTOR_"+s.id+"_GROUPS", nil, config.Startup,
		"Group(s) whose members may connect this account. Unset offers it to everyone who may sign in.")
}

// Transfer is HTTP_API_CONNECTOR_<instance>_TRANSFER.
//
// How a job fetches from this account, where the connector supports
// more than one way.
func (s ConnectorSettings) Transfer() config.StringKnob {
	return config.DefineString("HTTP_API_CONNECTOR_"+s.id+"_TRANSFER", "", config.Startup,
		"How a job fetches from this account, where the connector supports more than one way.")
}

// Enabled is HTTP_API_CONNECTOR_<instance>_ENABLED.
//
// Whether the connector is offered at all. Default true, so a
// connector is turned off by setting this rather than by removing its
// configuration.
func (s ConnectorSettings) Enabled() config.BoolKnob {
	return config.DefineBool("HTTP_API_CONNECTOR_"+s.id+"_ENABLED", true, config.Startup,
		"Whether the connector is offered at all. Default true, so a connector is turned off by setting this rather than by removing its configuration.")
}

// PickerSettings names the Picker settings of one instance.
//
// A file chooser the submit page offers, so a user can pick input
// files out of a connected account instead of uploading them.
// HTTP_API_PICKERS names the choosers that exist; a chooser is
// configured by the settings below with its name folded in, e.g.
// HTTP_API_PICKER_LAB_A_API_KEY for the chooser named lab-a.
type PickerSettings struct{ id string }

// Picker returns the settings of one Picker instance. The name is
// folded into the parameter form here, once, so no caller repeats it.
func Picker(instance string) PickerSettings {
	return PickerSettings{config.InstanceID(instance)}
}

// PickerInstances is the parameter naming every Picker that exists.
var PickerInstances = config.DefineList("HTTP_API_PICKERS", nil, config.Startup,
	"Names the Picker instances that exist. A file chooser the submit page offers, so a user can pick input files out of a connected account instead of uploading them. HTTP_API_PICKERS names the choosers that exist; a chooser is configured by the settings below with its name folded in, e.g. HTTP_API_PICKER_LAB_A_API_KEY for the chooser named lab-a.")

// Provider is HTTP_API_PICKER_<instance>_PROVIDER.
//
// Which shipped chooser to run. Defaults to the chooser's own name, so
// the common case needs one line.
func (s PickerSettings) Provider() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_PROVIDER", "", config.Startup,
		"Which shipped chooser to run. Defaults to the chooser's own name, so the common case needs one line.")
}

// Connector is HTTP_API_PICKER_<instance>_CONNECTOR.
//
// The connector whose credential the chooser shows files with. Without
// one the chooser is not offered.
func (s PickerSettings) Connector() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_CONNECTOR", "", config.Startup,
		"The connector whose credential the chooser shows files with. Without one the chooser is not offered.")
}

// APIKey is HTTP_API_PICKER_<instance>_API_KEY.
//
// The provider's developer key, where its chooser needs one to run in
// the browser.
func (s PickerSettings) APIKey() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_API_KEY", "", config.Startup,
		"The provider's developer key, where its chooser needs one to run in the browser.")
}

// AppID is HTTP_API_PICKER_<instance>_APP_ID.
//
// The provider's application id, where its chooser needs one alongside
// the developer key.
func (s PickerSettings) AppID() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_APP_ID", "", config.Startup,
		"The provider's application id, where its chooser needs one alongside the developer key.")
}

// Origin is HTTP_API_PICKER_<instance>_ORIGIN.
//
// The origin the chooser is loaded from, when it is not the provider's
// default.
func (s PickerSettings) Origin() config.StringKnob {
	return config.DefineString("HTTP_API_PICKER_"+s.id+"_ORIGIN", "", config.Startup,
		"The origin the chooser is loaded from, when it is not the provider's default.")
}

// SiteAppSettings names the SiteApp settings of one instance.
//
// A lab's own web interface, published to the people it belongs to.
// Each bundle in HTTP_API_SITE_APPS_DIR is configured by the settings
// below with its id folded in, e.g. HTTP_API_SITE_APP_CRYOEM_GROUPS
// for the bundle cryoem. GROUPS is what declares an app: a bundle
// nobody has given groups to is not published.
type SiteAppSettings struct{ id string }

// SiteApp returns the settings of one SiteApp instance. The name is
// folded into the parameter form here, once, so no caller repeats it.
func SiteApp(instance string) SiteAppSettings {
	return SiteAppSettings{config.InstanceID(instance)}
}

// Groups is HTTP_API_SITE_APP_<instance>_GROUPS.
//
// Group(s) whose members this app is published to. An app with none is
// not published at all -- an empty value is far more often a mistake
// than a deliberate "nobody".
func (s SiteAppSettings) Groups() config.ListKnob {
	return config.DefineList("HTTP_API_SITE_APP_"+s.id+"_GROUPS", nil, config.Reconfig,
		"Group(s) whose members this app is published to. An app with none is not published at all -- an empty value is far more often a mistake than a deliberate \"nobody\".")
}

// Home is HTTP_API_SITE_APP_<instance>_HOME.
//
// Whether this app is what its people land on instead of the
// dashboard.
func (s SiteAppSettings) Home() config.BoolKnob {
	return config.DefineBool("HTTP_API_SITE_APP_"+s.id+"_HOME", false, config.Reconfig,
		"Whether this app is what its people land on instead of the dashboard.")
}

// Enabled is HTTP_API_SITE_APP_<instance>_ENABLED.
//
// Whether the app is served. Default true when the setting is present
// at all, so an app is turned off by setting this to false rather than
// by removing its groups.
func (s SiteAppSettings) Enabled() config.BoolKnob {
	return config.DefineBool("HTTP_API_SITE_APP_"+s.id+"_ENABLED", true, config.Reconfig,
		"Whether the app is served. Default true when the setting is present at all, so an app is turned off by setting this to false rather than by removing its groups.")
}

// Name is HTTP_API_SITE_APP_<instance>_NAME.
//
// What the app is called in the navigation, instead of its bundle id.
func (s SiteAppSettings) Name() config.StringKnob {
	return config.DefineString("HTTP_API_SITE_APP_"+s.id+"_NAME", "", config.Reconfig,
		"What the app is called in the navigation, instead of its bundle id.")
}

// Description is HTTP_API_SITE_APP_<instance>_DESCRIPTION.
//
// One line about the app, shown beside its name.
func (s SiteAppSettings) Description() config.StringKnob {
	return config.DefineString("HTTP_API_SITE_APP_"+s.id+"_DESCRIPTION", "", config.Reconfig,
		"One line about the app, shown beside its name.")
}

// Icon is HTTP_API_SITE_APP_<instance>_ICON.
//
// The icon shown beside the app's name.
func (s SiteAppSettings) Icon() config.StringKnob {
	return config.DefineString("HTTP_API_SITE_APP_"+s.id+"_ICON", "", config.Reconfig,
		"The icon shown beside the app's name.")
}

// All is every declared knob, for the configuration reference and
// for tests that check the table against the code.
var All = []config.Knob{
	IdleTimeout.Knob,
	ReadTimeout.Knob,
	TLSCACert.Knob,
	TLSCert.Knob,
	TLSKey.Knob,
	WriteTimeout.Knob,
}
