package httpserver

import (
	"embed"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/bbockelm/golang-htcondor/logging"
	"github.com/bbockelm/golang-htcondor/webapi/connectors"
)

// The browser file choosers this access point offers.
//
// A chooser is its own resource because it is its own thing: a
// provider's, borrowing a connector's credential, carrying a developer
// key that is not a credential's to hold. See connectors/picker.go.

//go:embed pickerjs/*.js
var pickerAdapters embed.FS

// Picker is one chooser as the page sees it.
type Picker struct {
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Connector string `json:"connector"`
	// Service is the credential half of a file reference, so the page
	// can build <service>+<method>://<id>/<name> without knowing how
	// that name is composed.
	Service string `json:"service"`
	Method  string `json:"method"`
	// Script is where to load the adapter from. This server's own by
	// default, which is why 'self' covers it; a test points it at a
	// stand-in so the page code under test is the page code that ships.
	Script string `json:"script"`
	// APIKey is the provider's developer key. Public by design -- it
	// lives in client-side JavaScript either way -- and not a secret
	// this server holds on anybody's behalf.
	APIKey string `json:"api_key,omitempty"`
	AppID  string `json:"app_id,omitempty"`
}

// handlePickers answers GET /api/v1/pickers.
//
// Session-only and group-gated, like the connect endpoints: a chooser
// a caller cannot use its connector for is not listed, because the
// alternative is a button that opens a chooser and then refuses.
func (s *Handler) handlePickers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, ok := s.requireConnectSession(w, r)
	if !ok {
		return
	}

	out := []Picker{}
	for _, p := range s.connectorConfig.Pickers() {
		if len(p.Problems()) > 0 {
			// A misconfigured chooser is not offered. Offering one is
			// how a person meets the failure instead of the operator.
			continue
		}
		def, ok := p.Def()
		if !ok {
			continue
		}
		conn, found := s.connectorDefinition(p.Connector)
		if !found || !conn.Enabled || !conn.Permits(session.Groups) {
			continue
		}
		out = append(out, Picker{
			Name:      p.Name,
			Provider:  p.Provider,
			Connector: p.Connector.String(),
			Service:   conn.Service,
			Method:    def.Method,
			Script:    s.pickerScriptURL(p),
			APIKey:    p.APIKey,
			AppID:     p.AppID,
		})
	}
	s.writeJSON(w, http.StatusOK, out)
}

// pickerScriptURL is where the page loads a chooser's adapter from.
func (s *Handler) pickerScriptURL(p connectors.Picker) string {
	if p.Origin != "" {
		return strings.TrimSuffix(p.Origin, "/") + "/picker/" + p.Provider + ".js"
	}
	return "/picker/" + p.Provider + ".js"
}

// handlePickerScript serves an adapter.
//
// From this server's own origin, which is what lets the page's
// script-src stay 'self' for the adapter itself; the adapter is what
// then loads the provider's script, and that is the origin the policy
// names.
func (s *Handler) handlePickerScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	name := path.Base(r.URL.Path)
	provider := strings.TrimSuffix(name, ".js")
	if provider == name || provider == "" || strings.ContainsAny(provider, "/.") {
		http.NotFound(w, r)
		return
	}

	// Only for a provider this server has a chooser for, so the path is
	// not a way to probe the embedded filesystem.
	if _, ok := connectors.LookupPickerProvider(provider); !ok {
		http.NotFound(w, r)
		return
	}
	body, err := pickerAdapters.ReadFile("pickerjs/" + provider + ".js")
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The bytes are an embedded adapter of ours, selected by a name
	// checked against the shipped catalogue above, served as JavaScript
	// with nosniff. Nothing from the request reaches the response body.
	if _, err := w.Write(body); err != nil { //nolint:gosec // G705: embedded asset, allowlisted name
		s.logger.Debug(logging.DestinationHTTP, "could not write the picker adapter",
			"provider", provider, "error", err)
	}
}

// FileReference builds the submit-file reference for a chosen file.
//
// Here rather than in the page, so one place decides the spelling and
// the page cannot drift from what SplitSpoolInputs parses.
func (p Picker) FileReference(fileID, name string) string {
	return fmt.Sprintf("%s+%s://%s/%s", p.Service, p.Method, fileID, name)
}
