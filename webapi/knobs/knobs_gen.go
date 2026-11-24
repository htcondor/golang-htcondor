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
