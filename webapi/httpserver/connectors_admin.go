package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// The admin side of connectors: /api/v1/admin/connectors.
//
// Modelled on the site-apps admin surface, including its origin /
// stored_here / editable triple, because an operator who has learned
// that page has learned this one. The one thing site apps does not have
// is a secret, and that is the part with its own rules below.
//
// Not to be confused with /api/v1/admin/clients, which is the opposite
// direction: those are applications connecting TO this server's
// identity provider. These are accounts this server connects to.

// AdminConnector is one connector as the admin listing reports it.
type AdminConnector struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Origin says which decision published it: a configuration
	// parameter, or that it is stored here.
	Origin string `json:"origin"`
	// StoredHere is whether these values are this server's own record
	// rather than the HTCondor configuration. It is the difference
	// between a change that takes effect now and one that needs
	// condor_reconfig.
	StoredHere bool `json:"stored_here"`
	// Editable is whether a PUT will be accepted for it.
	Editable bool `json:"editable"`
	// Shadowed reports a STORED connector the configuration also
	// declares, and therefore overrides.
	//
	// Reported rather than hidden: a stored row that silently loses is
	// an operator editing a form to no effect and finding out from a
	// user.
	Shadowed bool `json:"shadowed,omitempty"`

	Enabled bool `json:"enabled"`
	// Picker is whether this connector offers a browser file chooser.
	// It is reported because it is the only setting that widens the
	// page's Content-Security-Policy.
	Picker   bool     `json:"picker"`
	Groups   []string `json:"groups"`
	Scopes   []string `json:"scopes"`
	Transfer string   `json:"transfer"`

	ClientID string `json:"client_id,omitempty"`
	// SecretSet reports that a secret is held, without being one. The
	// secret itself is never rendered: not in full, not masked, not by
	// length.
	SecretSet       bool       `json:"secret_set"`
	SecretUpdatedAt *time.Time `json:"secret_updated_at,omitempty"`
	// SecretFile is the path the configuration names. The path is
	// configuration and public; the bytes are on disk and are not.
	SecretFile string `json:"secret_file,omitempty"`

	// Users and LastUsedAt answer the question an operator actually
	// has: is anyone using this?
	Users      int        `json:"users"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`

	// TransferMethod is the HTCondor plugin method that can use this
	// credential in direct mode, empty when none ships.
	TransferMethod string `json:"transfer_method,omitempty"`
}

// AdminConnectorRequest creates or edits a stored connector.
//
// Secret is a POINTER so that absent and empty are different things:
// absent leaves the stored secret alone, and an explicit "" clears it.
// A form that round-tripped an absent field as "" would wipe a working
// connector's secret on an unrelated edit, and nobody would attribute
// that failure to the edit that caused it.
type AdminConnectorRequest struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	ClientID string   `json:"client_id"`
	Secret   *string  `json:"client_secret,omitempty"`
	AuthURL  string   `json:"authorization_url"`
	TokenURL string   `json:"token_url"`
	Scopes   []string `json:"scopes"`
	Groups   []string `json:"groups"`
	Transfer string   `json:"transfer"`
	Enabled  *bool    `json:"enabled,omitempty"`
}

// AdminConnectorUser is one person's grant, for the per-connector view.
type AdminConnectorUser struct {
	Subject    string     `json:"subject"`
	ObtainedAt time.Time  `json:"obtained_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// handleAdminConnectors dispatches every admin connector path.
func (s *Handler) handleAdminConnectors(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/connectors"), "/")

	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			s.adminListConnectors(w, r)
		case http.MethodPost:
			s.adminPutConnector(w, r, connectors.ID{})
		default:
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	idPart, action, _ := strings.Cut(rest, "/")
	id, err := connectors.ParseID(idPart)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}

	switch action {
	case "":
		if r.Method == http.MethodDelete {
			s.adminDeleteConnector(w, r, id)
			return
		}
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	case "config":
		if r.Method != http.MethodPut {
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.adminPutConnector(w, r, id)
	case "users":
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.adminConnectorUsers(w, r, id)
	case "revoke":
		if r.Method != http.MethodPost {
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.adminRevokeConnector(w, r, id)
	case "test":
		if r.Method != http.MethodPost {
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.adminTestConnector(w, r, id)
	default:
		s.writeError(w, http.StatusNotFound, "Not found")
	}
}

func (s *Handler) adminListConnectors(w http.ResponseWriter, r *http.Request) {
	configured := map[string]bool{}
	for _, id := range s.connectorConfig.IDs() {
		configured[id.String()] = true
	}

	out := []AdminConnector{}
	for _, d := range s.connectorDefinitions() {
		row := AdminConnector{
			ID:         d.ID.String(),
			Kind:       string(d.Kind),
			Origin:     d.Origin,
			StoredHere: d.Origin == "stored",
			Editable:   d.Origin == "stored",
			Enabled:    d.Enabled,
			Groups:     d.Groups,
			Scopes:     d.Scopes,
			Transfer:   string(d.Transfer),
			ClientID:   d.ClientID,
			SecretFile: d.ClientSecretFile,
		}
		if b, ok := connectors.Lookup(d.ID); ok {
			row.TransferMethod = b.TransferMethod
		}
		if row.StoredHere {
			if stored, found, err := s.connectorStore().Connector(r.Context(), d.ID); err == nil && found {
				row.SecretSet = stored.SecretSet
				if !stored.SecretUpdatedAt.IsZero() {
					when := stored.SecretUpdatedAt
					row.SecretUpdatedAt = &when
				}
			}
		} else {
			row.SecretSet = d.ClientSecretFile != ""
		}
		if store := s.connectorStore(); store != nil {
			users, lastUsed, err := store.CountCredentials(r.Context(), d.ID)
			if err == nil {
				row.Users = users
				if !lastUsed.IsZero() {
					when := lastUsed
					row.LastUsedAt = &when
				}
			}
		}
		out = append(out, row)
	}

	// A stored connector the configuration also declares is overridden
	// and says so, rather than vanishing from the page it was created
	// on.
	if store := s.connectorStore(); store != nil {
		rows, err := store.Connectors(r.Context())
		if err == nil {
			for _, row := range rows {
				if !configured[row.ID.String()] {
					continue
				}
				for i := range out {
					if out[i].ID == row.ID.String() {
						out[i].Shadowed = true
					}
				}
			}
		}
	}

	s.writeJSON(w, http.StatusOK, out)
}

// adminPutConnector creates or edits a stored connector.
//
// A connector the CONFIGURATION declares is refused rather than
// silently stored: the stored row would lose, and writing one on an
// operator's behalf that never takes effect is worse than saying no.
func (s *Handler) adminPutConnector(w http.ResponseWriter, r *http.Request, id connectors.ID) {
	var req AdminConnectorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}
	if id == (connectors.ID{}) {
		parsed, err := connectors.ParseID(req.ID)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		id = parsed
	}

	if _, declared := s.connectorConfig.Definition(id); declared {
		s.writeError(w, http.StatusConflict, fmt.Sprintf(
			"Connector %q is declared by this access point's configuration, which takes "+
				"precedence over anything stored here. Change it in the configuration and "+
				"run condor_reconfig.", id))
		return
	}

	store := s.connectorStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "No connector store is configured")
		return
	}

	kind := connectors.Kind(strings.ToLower(req.Kind))
	transfer := connectors.Transfer(strings.ToLower(req.Transfer))
	if b, ok := connectors.Lookup(id); ok {
		// A builtin supplies what the operator should not have to know.
		if kind == "" {
			kind = b.Kind
		}
		if len(req.Scopes) == 0 {
			req.Scopes = b.DefaultScopes
		}
	}
	if transfer == "" {
		transfer = connectors.TransferSpool
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	stored := connectors.Stored{
		ID:               id,
		Kind:             kind,
		ClientID:         req.ClientID,
		AuthorizationURL: req.AuthURL,
		TokenURL:         req.TokenURL,
		Scopes:           req.Scopes,
		Groups:           req.Groups,
		Transfer:         transfer,
		Enabled:          enabled,
	}

	// nil leaves the stored secret alone; a non-nil empty clears it.
	var secret []byte
	if req.Secret != nil {
		secret = []byte(*req.Secret)
	}

	if err := store.PutConnector(r.Context(), stored, secret); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logger.Info(logging.DestinationHTTP, "an administrator saved a connector",
		"connector", id.String(), "secret_changed", req.Secret != nil)

	s.writeJSON(w, http.StatusOK, map[string]any{"id": id.String(), "saved": true})
}

// adminDeleteConnector removes a stored connector and its credentials.
func (s *Handler) adminDeleteConnector(w http.ResponseWriter, r *http.Request, id connectors.ID) {
	if _, declared := s.connectorConfig.Definition(id); declared {
		s.writeError(w, http.StatusConflict, fmt.Sprintf(
			"Connector %q comes from this access point's configuration and cannot be "+
				"deleted here. Remove it from the configuration and run condor_reconfig.", id))
		return
	}
	store := s.connectorStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "No connector store is configured")
		return
	}

	credentials, err := store.DeleteConnector(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Info(logging.DestinationHTTP, "an administrator deleted a connector",
		"connector", id.String(), "credentials_destroyed", credentials)

	s.writeJSON(w, http.StatusOK, map[string]any{
		"id":      id.String(),
		"deleted": true,
		// Reported so a confirmation can say what was destroyed rather
		// than asking in the abstract.
		"credentials_destroyed": credentials,
	})
}

// adminRevokeConnector drops every grant, leaving the connector.
//
// What to do when a client secret has leaked or rotated: the stored
// refresh tokens were issued to the old registration and users have to
// consent again.
//
// Available on a connector the CONFIGURATION declares, deliberately.
// The configuration owns the definition; these credentials are this
// server's own state, and an operator who cannot revoke them here has
// to go and delete database rows by hand.
func (s *Handler) adminRevokeConnector(w http.ResponseWriter, r *http.Request, id connectors.ID) {
	store := s.connectorStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "No connector store is configured")
		return
	}
	n, err := store.RevokeConnector(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Info(logging.DestinationHTTP, "an administrator revoked a connector's credentials",
		"connector", id.String(), "credentials_revoked", n)

	s.writeJSON(w, http.StatusOK, map[string]any{
		"id": id.String(), "revoked": n,
	})
}

func (s *Handler) adminConnectorUsers(w http.ResponseWriter, r *http.Request, id connectors.ID) {
	store := s.connectorStore()
	if store == nil {
		s.writeJSON(w, http.StatusOK, []AdminConnectorUser{})
		return
	}
	creds, err := store.CredentialsFor(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]AdminConnectorUser, 0, len(creds))
	for _, c := range creds {
		row := AdminConnectorUser{Subject: c.Subject, ObtainedAt: c.ObtainedAt}
		if !c.LastUsedAt.IsZero() {
			when := c.LastUsedAt
			row.LastUsedAt = &when
		}
		out = append(out, row)
	}
	s.writeJSON(w, http.StatusOK, out)
}

// adminTestConnector answers "is this configured correctly" without
// needing anybody's grant.
//
// A connector that is wrong should say so when it is configured, not at
// some user's first attempt -- by which time the person meeting the
// failure is not the person who can fix it.
func (s *Handler) adminTestConnector(w http.ResponseWriter, r *http.Request, id connectors.ID) {
	def, found := s.connectorDefinition(id)
	if !found {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("No connector %q is configured", id))
		return
	}

	var problems []string
	if def.ClientID == "" {
		problems = append(problems, "no client id")
	}
	if def.AuthorizationURL == "" {
		problems = append(problems, "no authorization endpoint")
	}
	if def.TokenURL == "" {
		problems = append(problems, "no token endpoint")
	}
	if len(def.Groups) == 0 {
		// Not a configuration error so much as a connector nobody can
		// use, which looks identical to a broken one from outside.
		problems = append(problems, "no groups, so nobody may use it")
	}
	if _, err := s.connectorSecret(r.Context(), def); err != nil {
		problems = append(problems, fmt.Sprintf("the client secret could not be read: %v", err))
	}
	if def.Kind == connectors.KindOAuth2 && def.Transfer == connectors.TransferDirect {
		if b, ok := connectors.Lookup(def.ID); ok && b.TransferMethod == "" {
			problems = append(problems, fmt.Sprintf(
				"transfer is direct but no file transfer plugin ships for %s", def.ID))
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"id":       id.String(),
		"usable":   len(problems) == 0,
		"problems": problems,
	})
}
