package httpserver

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
	"github.com/htcondor/golang-htcondor/webapi/webhooks"
)

// The user's own outbound webhooks: /api/v1/webhooks.
//
// SESSION ONLY, like the integrations endpoints and for a reason of its own: a
// webhook destination is a capability URL. A token that could create one
// could make this server post a job's details to an address of the token
// holder's choosing, and a token that could READ one back would be enough to
// post there itself. So the destination is never returned by any endpoint,
// and every endpoint here authenticates from the browser session.

// webhookCaller resolves the signed-in person, or writes the refusal.
func (h *Handler) webhookCaller(w http.ResponseWriter, r *http.Request) (*SessionData, bool) {
	if h.webhooks == nil {
		h.writeError(w, http.StatusServiceUnavailable, "Webhooks are not available on this server")
		return nil, false
	}
	session, ok := h.getSessionFromRequest(r)
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "Sign-in required")
		return nil, false
	}
	if session.Username == "" {
		h.writeError(w, http.StatusForbidden, "This session has no local account, so it cannot own a webhook")
		return nil, false
	}
	return session, true
}

// webhookCreateRequest is the POST body.
type webhookCreateRequest struct {
	Label string `json:"label"`
	// URL is the destination. Write-only: no endpoint returns it.
	URL   string `json:"url"`
	Scope string `json:"scope"`
	// Job names the subject for scope "job".
	Cluster int64  `json:"cluster_id"`
	Proc    int64  `json:"proc_id"`
	Schedd  string `json:"schedd"`
	// Batch names the subject for scope "batch".
	BatchID   string `json:"batch_id"`
	BatchName string `json:"batch_name"`

	Events []string `json:"events"`
}

// webhookUpdateRequest is the PATCH body. Pointers so the handler can tell
// "set to empty" from "not provided" and touch only what was sent.
type webhookUpdateRequest struct {
	Label  *string  `json:"label"`
	URL    *string  `json:"url"`
	Events []string `json:"events"`
}

// handleWebhooks serves GET (list) and POST (create) on /api/v1/webhooks.
func (h *Handler) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleListWebhooks(w, r)
	case http.MethodPost:
		h.handleCreateWebhook(w, r)
	default:
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleListWebhooks lists the caller's hooks, newest first.
func (h *Handler) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	hooks, err := h.webhooks.ForOwner(r.Context(), session.Username)
	if err != nil {
		h.logger.Error(logging.DestinationHTTP, "Failed to list webhooks",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Failed to list webhooks")
		return
	}
	if hooks == nil {
		hooks = []*webhooks.Hook{}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"webhooks": hooks,
		// What a hook may subscribe to, so the page offers exactly what the
		// server implements rather than a list someone remembered to keep
		// in step.
		"events": webhooks.SupportedEvents,
	})
}

// handleCreateWebhook creates a hook the caller owns.
func (h *Handler) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	if !h.requireJSONRequest(w, r) {
		return
	}
	setBodyLimit(w, r, 16<<10)
	var req webhookCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	events, err := parseWebhookEvents(req.Events)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hook := &webhooks.Hook{
		Owner:     session.Username,
		Label:     strings.TrimSpace(req.Label),
		Scope:     webhooks.Scope(req.Scope),
		Job:       jobwatch.JobID{Cluster: req.Cluster, Proc: req.Proc, Schedd: strings.ToLower(req.Schedd)},
		BatchID:   req.BatchID,
		BatchName: req.BatchName,
		Events:    events,
	}

	created, secret, err := h.webhooks.Create(r.Context(), hook, strings.TrimSpace(req.URL))
	if err != nil {
		h.logger.Warn(logging.DestinationHTTP, "Refused to create a webhook",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.logger.Info(logging.DestinationHTTP, "User created a webhook",
		"owner", session.Username, "webhook", created.ID,
		"scope", string(created.Scope), "events", strings.Join(req.Events, ","))

	// The signing secret is returned once, so a receiver can be configured
	// to verify. Nothing recovers it afterwards.
	h.writeJSON(w, http.StatusCreated, map[string]any{
		"webhook":            created,
		"signing_secret":     hexSecret(secret),
		"secret_shown_once":  true,
		"signature_header":   webhooks.SignatureHeader,
		"idempotency_header": webhooks.IdempotencyHeader,
	})
}

// handleWebhookItem serves everything under /api/v1/webhooks/{id}.
//
// Routed on the trailing segment rather than with a router, matching the
// placement endpoints: the shapes are few and the dispatch is clearer read
// top to bottom than spread over four registrations.
func (h *Handler) handleWebhookItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/webhooks/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" || strings.Contains(action, "/") {
		h.writeError(w, http.StatusBadRequest, "Invalid webhook id")
		return
	}
	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			h.handleGetWebhook(w, r, id)
		case http.MethodPatch:
			h.handleUpdateWebhook(w, r, id)
		case http.MethodDelete:
			h.handleDeleteWebhook(w, r, id)
		default:
			h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	case "test":
		h.handleTestWebhook(w, r, id)
	case "enable":
		h.handleEnableWebhook(w, r, id)
	case "deliveries":
		h.handleWebhookDeliveries(w, r, id)
	default:
		h.writeError(w, http.StatusNotFound, "No such webhook action")
	}
}

func (h *Handler) handleGetWebhook(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	hook, err := h.webhooks.Get(r.Context(), session.Username, id)
	if h.webhookError(w, err) {
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"webhook": hook})
}

func (h *Handler) handleUpdateWebhook(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	if !h.requireJSONRequest(w, r) {
		return
	}
	setBodyLimit(w, r, 16<<10)
	var req webhookUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	var events []jobwatch.Event
	if req.Events != nil {
		parsed, err := parseWebhookEvents(req.Events)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		events = parsed
	}
	var dest *string
	if req.URL != nil {
		trimmed := strings.TrimSpace(*req.URL)
		dest = &trimmed
	}

	hook, err := h.webhooks.Update(r.Context(), session.Username, id, req.Label, events, dest)
	if errors.Is(err, webhooks.ErrNotFound) {
		h.writeError(w, http.StatusNotFound, "No such webhook")
		return
	}
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"webhook": hook})
}

func (h *Handler) handleDeleteWebhook(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	// Scoped to the owner inside the statement, so somebody else's hook is
	// not found rather than refused.
	if h.webhookError(w, h.webhooks.Delete(r.Context(), session.Username, id)) {
		return
	}
	h.logger.Info(logging.DestinationHTTP, "User deleted a webhook",
		"owner", session.Username, "webhook", id)
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

// handleTestWebhook sends one real delivery and reports what happened.
//
// Synchronous on purpose. Somebody is watching the result, and the answer is
// only useful while they are: a queued test whose outcome appears later is a
// test they have to go and look for.
func (h *Handler) handleTestWebhook(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	if !h.requireJSONRequest(w, r) {
		return
	}
	hook, err := h.webhooks.Get(r.Context(), session.Username, id)
	if h.webhookError(w, err) {
		return
	}

	attempt, err := webhooks.SendTest(r.Context(), h.webhooks, h.webhookSender, hook)
	if err != nil {
		// A hook whose destination cannot be read at all -- no KEK on this
		// server, or the row has gone. Not the receiver's fault and not
		// something a retry fixes.
		h.logger.Warn(logging.DestinationHTTP, "Could not send a webhook test",
			"owner", session.Username, "webhook", id, "error", err)
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.logger.Info(logging.DestinationHTTP, "Webhook test delivered",
		"owner", session.Username, "webhook", id, "status", attempt.Status)

	// 200 whatever the receiver said: the test ran, and what it found is the
	// body. An HTTP error here would say this server failed, which it did
	// not, and would send the SPA down its error path instead of showing
	// the user what their receiver answered.
	h.writeJSON(w, http.StatusOK, map[string]any{
		"ok":     attempt.OK(),
		"status": attempt.Status,
		"error":  attempt.Cause,
	})
}

func (h *Handler) handleEnableWebhook(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	if !h.requireJSONRequest(w, r) {
		return
	}
	if h.webhookError(w, h.webhooks.Enable(r.Context(), session.Username, id)) {
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "enabled", "id": id})
}

// handleWebhookDeliveries lists what this hook has recently tried to send, so
// a user can see whether it is working without reading a server log.
func (h *Handler) handleWebhookDeliveries(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	const limit = 50
	deliveries, err := h.webhooks.RecentDeliveries(r.Context(), session.Username, id, limit)
	if h.webhookError(w, err) {
		return
	}
	out := make([]map[string]any, 0, len(deliveries))
	for _, d := range deliveries {
		row := map[string]any{
			"id":       d.ID,
			"event":    string(d.Event),
			"state":    d.State,
			"attempts": d.Attempts,
			"status":   d.LastStatus,
			"error":    d.LastError,
		}
		if d.Job != nil {
			row["job"] = jobIDString(*d.Job)
		}
		out = append(out, row)
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"deliveries": out})
}

// webhookError writes the response for a store error and reports whether it
// did. ErrNotFound becomes 404 rather than 403: a 403 would confirm that an
// id exists to a caller who does not hold it.
func (h *Handler) webhookError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, webhooks.ErrNotFound):
		h.writeError(w, http.StatusNotFound, "No such webhook")
	case errors.Is(err, webhooks.ErrNoKEK):
		h.writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		h.logger.Error(logging.DestinationHTTP, "Webhook request failed", "error", err)
		h.writeError(w, http.StatusInternalServerError, "The request could not be completed")
	}
	return true
}

// parseWebhookEvents turns the request's event names into the vocabulary,
// naming what is available when one is not.
func parseWebhookEvents(names []string) ([]jobwatch.Event, error) {
	if len(names) == 0 {
		return nil, errors.New("at least one event is required (" + webhooks.EventNames() + ")")
	}
	out := make([]jobwatch.Event, 0, len(names))
	for _, n := range names {
		e := jobwatch.Event(strings.ToLower(strings.TrimSpace(n)))
		if !webhooks.EventSupported(e) {
			return nil, errors.New("unsupported event " + n + " (" + webhooks.EventNames() + ")")
		}
		out = append(out, e)
	}
	return out, nil
}

// jobIDString renders a job for display.
func jobIDString(id jobwatch.JobID) string {
	s := strconv.FormatInt(id.Cluster, 10) + "." + strconv.FormatInt(id.Proc, 10)
	if id.Schedd != "" {
		s += "@" + id.Schedd
	}
	return s
}

// hexSecret renders a signing secret for the one response that carries it.
// Hex rather than base64 so a receiver's own HMAC configuration, which is
// usually hex, takes it unchanged.
func hexSecret(b []byte) string { return hex.EncodeToString(b) }
