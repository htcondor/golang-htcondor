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
