package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/bbockelm/golang-htcondor/config"
	"github.com/bbockelm/golang-htcondor/logging"
	"github.com/bbockelm/golang-htcondor/webapi/connectors"
)

// Wiring the connectors package into the server: where a definition
// comes from, and which of the two sources wins.
//
// Nothing is snapshotted here. The config source holds the
// *config.Config and reads on each lookup, as siteapps.ConfigPolicy
// does, so there is one copy of the answer and no second one to go
// stale; a reconfigure replaces the whole source. See
// SetConnectorConfig.

// newConnectors builds the connector machinery at startup.
func (s *Handler) newConnectors(params *config.Config) {
	s.connectorConfig = connectorConfigFor(params)
	s.connectorStored = connectors.NewStore(s.db, s.sealer)
	s.connectorFlows = connectors.NewFlow()
}

// connectorConfigFor builds the configuration-backed source.
//
// The nil check is load-bearing, exactly as it is in
// siteapps.configPolicyFor: a nil *config.Config placed in an interface
// makes an interface that is NOT nil, so every lookup would call a
// method on a nil pointer rather than finding nothing.
func connectorConfigFor(params *config.Config) *connectors.ConfigSource {
	if params == nil {
		return connectors.NewConfigSource(nil)
	}
	return connectors.NewConfigSource(params)
}

// SetConnectorConfig installs connector policy from a freshly-loaded
// configuration.
//
// Called on EVERY reconfigure rather than when a parameter is seen to
// change, for the reason site apps is: a connector's definition is
// spread over a parameter per connector per setting, and declaring a
// new connector adds names nothing was watching. A diff over the names
// that existed before cannot see a name that did not.
//
// The client secret is re-read by whoever uses it rather than cached
// here, so a rotated secret at an unchanged path takes effect too --
// the path is in the configuration and the bytes are not, so no diff
// could see that edit either.
func (s *Handler) SetConnectorConfig(params *config.Config) {
	s.connectorConfig = connectorConfigFor(params)
	if dups := connectors.MacroCollision(s.connectorConfig.IDs()); len(dups) > 0 {
		for macro, ids := range dups {
			s.logger.Error(logging.DestinationHTTP,
				"two connectors share one configuration parameter name; neither is usable",
				"parameter", macro, "connectors", fmt.Sprint(ids))
		}
	}
}

func (s *Handler) connectorStore() *connectors.Store {
	if s == nil {
		return nil
	}
	return s.connectorStored
}

func (s *Handler) connectorFlow() *connectors.Flow {
	if s.connectorFlows == nil {
		s.connectorFlows = connectors.NewFlow()
	}
	return s.connectorFlows
}

// connectorDefinition resolves one connector from whichever source
// declares it.
//
// THE CONFIGURATION WINS. A stored row for an id the configuration also
// declares is shadowed, and the admin page reports it as shadowed
// rather than hiding it -- a row that silently loses is an operator
// editing a form to no effect and finding out from a user.
func (s *Handler) connectorDefinition(id connectors.ID) (connectors.Definition, bool) {
	if d, ok := s.connectorConfig.Definition(id); ok {
		return d, true
	}
	store := s.connectorStore()
	if store == nil {
		return connectors.Definition{}, false
	}
	stored, found, err := store.Connector(context.Background(), id)
	if err != nil || !found {
		return connectors.Definition{}, false
	}
	return s.definitionFromStored(stored), true
}

// connectorDefinitions lists every connector either source declares,
// configuration first, by id.
func (s *Handler) connectorDefinitions() []connectors.Definition {
	byID := map[string]connectors.Definition{}

	if store := s.connectorStore(); store != nil {
		rows, err := store.Connectors(context.Background())
		if err != nil {
			s.logger.Warn(logging.DestinationHTTP, "could not list stored connectors", "error", err)
		}
		for _, row := range rows {
			byID[row.ID.String()] = s.definitionFromStored(row)
		}
	}
	// Second, so a configured connector overwrites a stored one of the
	// same id rather than the other way round.
	for _, id := range s.connectorConfig.IDs() {
		if d, ok := s.connectorConfig.Definition(id); ok {
			byID[id.String()] = d
		}
	}

	out := make([]connectors.Definition, 0, len(byID))
	for _, d := range byID {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID.String() < out[j].ID.String() })
	return out
}

// definitionFromStored turns a database row into a definition, filling
// in whatever its builtin ships.
func (s *Handler) definitionFromStored(row connectors.Stored) connectors.Definition {
	d := connectors.Definition{
		ID:               row.ID,
		Kind:             row.Kind,
		ClientID:         row.ClientID,
		AuthorizationURL: row.AuthorizationURL,
		TokenURL:         row.TokenURL,
		Scopes:           row.Scopes,
		Groups:           row.Groups,
		Transfer:         row.Transfer,
		Enabled:          row.Enabled,
		Service:          row.ID.Service(s.connectorConfig.ServicePrefix()),
		Origin:           "stored",
	}
	if b, ok := connectors.Lookup(row.ID); ok {
		if d.AuthorizationURL == "" {
			d.AuthorizationURL = b.AuthorizationURL
		}
		if d.TokenURL == "" {
			d.TokenURL = b.TokenURL
		}
		if len(d.Scopes) == 0 {
			d.Scopes = b.DefaultScopes
		}
		if d.Kind == "" {
			d.Kind = b.Kind
		}
		d.AuthStyle = b.AuthStyle
	}
	return d
}

// connectorSecret reads a connector's client secret.
//
// From the file the configuration names, or from the sealed column for
// a stored one. Re-read on each use rather than cached, so a rotated
// secret takes effect without a restart.
func (s *Handler) connectorSecret(r *http.Request, d connectors.Definition) (string, error) {
	if d.ClientSecretFile != "" {
		b, err := d.ClientSecret()
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	store := s.connectorStore()
	if store == nil {
		return "", fmt.Errorf("connector %s has no stored client secret and no %s",
			d.ID, d.ID.ConfigKey("CLIENT_SECRET_FILE"))
	}
	b, err := store.ClientSecret(r.Context(), d.ID)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- the browser picker, and the policy that has to allow it ---------

// PickerOrigins is the set of origins a connector's file chooser needs
// the Content-Security-Policy to allow.
//
// Google's Picker is not a library this server can host: it loads from
// apis.google.com, renders its chooser in a docs.google.com IFRAME, and
// talks to googleapis.com. The page's CSP is default-src 'self', so
// every one of those is refused by the browser unless named -- and this
// repository has already shipped a browser-side feature that was
// blocked in every real browser and silent about it (the site-apps
// update badge). So the directives are widened here, from the
// connector's own definition, and asserted in a test.
type PickerOrigins struct {
	// Script is where the chooser's own code is loaded from.
	Script []string
	// Frame is where it renders. A CSP with no frame-src falls back to
	// default-src, so an iframe to another origin is blocked without
	// this and the chooser appears as an empty box.
	Frame []string
	// Connect is what it calls.
	Connect []string
	// Img is where its thumbnails come from.
	Img []string
}

// pickerOrigins returns the origins the configured connectors need.
//
// Only for connectors that are ENABLED and whose definition declares a
// picker. A policy is widened for a feature in use, never on the chance
// that one might be.
func (s *Handler) pickerOrigins() PickerOrigins {
	var out PickerOrigins
	seen := map[string]bool{}
	for _, d := range s.connectorDefinitions() {
		if !d.Enabled {
			continue
		}
		b, ok := connectors.Lookup(d.ID)
		if !ok || len(b.PickerScript) == 0 {
			continue
		}
		if seen[d.ID.String()] {
			continue
		}
		seen[d.ID.String()] = true
		out.Script = append(out.Script, b.PickerScript...)
		out.Frame = append(out.Frame, b.PickerFrame...)
		out.Connect = append(out.Connect, b.PickerConnect...)
		out.Img = append(out.Img, b.PickerImg...)
	}
	// A test points every one of these at its stand-in, so the policy
	// under test is the policy that ships rather than a relaxed one.
	if s.connectorPickerOverride != nil {
		return *s.connectorPickerOverride
	}
	return out
}
