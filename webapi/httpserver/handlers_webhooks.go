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

// endpointCreateRequest is the POST body for a destination.
type endpointCreateRequest struct {
	Label string `json:"label"`
	// URL is the destination. Write-only: no endpoint returns it.
	URL string `json:"url"`
	// Payload is "summary" or "full". Empty means summary.
	Payload string `json:"payload"`
}

// endpointUpdateRequest is the PATCH body. Pointers so the handler can tell
// "set to empty" from "not provided" and touch only what was sent.
type endpointUpdateRequest struct {
	Label   *string `json:"label"`
	URL     *string `json:"url"`
	Payload *string `json:"payload"`
}

// subscriptionCreateRequest is the POST body for what to watch.
type subscriptionCreateRequest struct {
	EndpointID string `json:"endpoint_id"`
	Label      string `json:"label"`
	Scope      string `json:"scope"`
	// Job names the subject for scope "job".
	Cluster int64  `json:"cluster_id"`
	Proc    int64  `json:"proc_id"`
	Schedd  string `json:"schedd"`
	// Batch names the subject for scope "batch".
	BatchID   string `json:"batch_id"`
	BatchName string `json:"batch_name"`

	Events []string `json:"events"`
}

// subscriptionUpdateRequest is the PATCH body.
type subscriptionUpdateRequest struct {
	Label  *string  `json:"label"`
	Events []string `json:"events"`
}

// handleWebhooks serves GET (list) and POST (create) on /api/v1/webhooks,
// which are DESTINATIONS.
func (h *Handler) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleListEndpoints(w, r)
	case http.MethodPost:
		h.handleCreateEndpoint(w, r)
	default:
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleListEndpoints lists the caller's destinations, newest first.
func (h *Handler) handleListEndpoints(w http.ResponseWriter, r *http.Request) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	eps, err := h.webhooks.EndpointsForOwner(r.Context(), session.Username)
	if err != nil {
		h.logger.Error(logging.DestinationHTTP, "Failed to list webhook destinations",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Failed to list webhooks")
		return
	}
	if eps == nil {
		eps = []*webhooks.Endpoint{}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"webhooks": eps,
		// What a subscription may ask for, so the page offers exactly
		// what the server implements rather than a list someone
		// remembered to keep in step.
		"events":        webhooks.SupportedEvents,
		"payload_modes": []string{string(webhooks.PayloadSummary), string(webhooks.PayloadFull)},
	})
}

// handleCreateEndpoint registers a destination.
//
// This is the only call that returns a signing secret, and the only one that
// has to: a receiver is configured once, with one key, however many things
// the user later decides to watch through it.
func (h *Handler) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	if !h.requireJSONRequest(w, r) {
		return
	}
	setBodyLimit(w, r, 16<<10)
	var req endpointCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	mode := webhooks.PayloadMode(strings.TrimSpace(req.Payload))
	if mode == "" {
		mode = webhooks.PayloadSummary
	}
	ep := &webhooks.Endpoint{
		Owner:   session.Username,
		Label:   strings.TrimSpace(req.Label),
		Payload: mode,
	}

	created, secret, err := h.webhooks.CreateEndpoint(r.Context(), ep, strings.TrimSpace(req.URL))
	if err != nil {
		h.logger.Warn(logging.DestinationHTTP, "Refused to create a webhook destination",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.logger.Info(logging.DestinationHTTP, "User created a webhook destination",
		"owner", session.Username, "webhook", created.ID, "payload", string(created.Payload))

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
// top to bottom than spread over several registrations.
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
			h.handleGetEndpoint(w, r, id)
		case http.MethodPatch:
			h.handleUpdateEndpoint(w, r, id)
		case http.MethodDelete:
			h.handleDeleteEndpoint(w, r, id)
		default:
			h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	case "test":
		h.handleTestEndpoint(w, r, id)
	case "deliveries":
		h.handleEndpointDeliveries(w, r, id)
	case "subscriptions":
		h.handleEndpointSubscriptions(w, r, id)
	default:
		h.writeError(w, http.StatusNotFound, "No such webhook action")
	}
}

func (h *Handler) handleGetEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	ep, err := h.webhooks.GetEndpoint(r.Context(), session.Username, id)
	if h.webhookError(w, err) {
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"webhook": ep})
}

func (h *Handler) handleUpdateEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	if !h.requireJSONRequest(w, r) {
		return
	}
	setBodyLimit(w, r, 16<<10)
	var req endpointUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	var dest *string
	if req.URL != nil {
		trimmed := strings.TrimSpace(*req.URL)
		dest = &trimmed
	}
	var mode *webhooks.PayloadMode
	if req.Payload != nil {
		m := webhooks.PayloadMode(strings.TrimSpace(*req.Payload))
		mode = &m
	}

	ep, err := h.webhooks.UpdateEndpoint(r.Context(), session.Username, id, req.Label, mode, dest)
	if errors.Is(err, webhooks.ErrNotFound) {
		h.writeError(w, http.StatusNotFound, "No such webhook")
		return
	}
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"webhook": ep})
}

// handleDeleteEndpoint removes a destination, everything watching through it
// and everything queued for it.
func (h *Handler) handleDeleteEndpoint(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	// Scoped to the owner inside the statement, so somebody else's
	// destination is not found rather than refused.
	if h.webhookError(w, h.webhooks.DeleteEndpoint(r.Context(), session.Username, id)) {
		return
	}
	h.logger.Info(logging.DestinationHTTP, "User deleted a webhook destination",
		"owner", session.Username, "webhook", id)
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

// handleTestEndpoint sends one real delivery and reports what happened.
//
// Synchronous on purpose. Somebody is watching the result, and the answer is
// only useful while they are: a queued test whose outcome appears later is a
// test they have to go and look for.
func (h *Handler) handleTestEndpoint(w http.ResponseWriter, r *http.Request, id string) {
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
	ep, err := h.webhooks.GetEndpoint(r.Context(), session.Username, id)
	if h.webhookError(w, err) {
		return
	}

	attempt, err := webhooks.SendTest(r.Context(), h.webhooks, h.webhookSender, ep)
	if err != nil {
		// A destination that cannot be read at all -- no KEK on this
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

// handleEndpointDeliveries lists what this destination has recently tried to
// send, so a user can see whether it is working without reading a server log.
func (h *Handler) handleEndpointDeliveries(w http.ResponseWriter, r *http.Request, id string) {
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

// handleEndpointSubscriptions lists what feeds one destination.
func (h *Handler) handleEndpointSubscriptions(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	// Through the endpoint, so an id that is not the caller's is "no such
	// webhook" rather than an empty list -- which would say it exists.
	if _, err := h.webhooks.GetEndpoint(r.Context(), session.Username, id); h.webhookError(w, err) {
		return
	}
	all, err := h.webhooks.SubscriptionsForOwner(r.Context(), session.Username)
	if h.webhookError(w, err) {
		return
	}
	out := make([]*webhooks.Subscription, 0, len(all))
	for _, sub := range all {
		if sub.EndpointID == id {
			out = append(out, sub)
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

// handleWebhookSubscriptions serves GET (list) and POST (create) on
// /api/v1/webhook-subscriptions.
func (h *Handler) handleWebhookSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleListSubscriptions(w, r)
	case http.MethodPost:
		h.handleCreateSubscription(w, r)
	default:
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (h *Handler) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	subs, err := h.webhooks.SubscriptionsForOwner(r.Context(), session.Username)
	if err != nil {
		h.logger.Error(logging.DestinationHTTP, "Failed to list webhook subscriptions",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusInternalServerError, "Failed to list webhook subscriptions")
		return
	}
	if subs == nil {
		subs = []*webhooks.Subscription{}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"subscriptions": subs,
		"events":        webhooks.SupportedEvents,
	})
}

// handleCreateSubscription starts watching something, through a destination
// that already exists.
//
// No secret is returned and none is minted: that is the point of the split.
func (h *Handler) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	if !h.requireJSONRequest(w, r) {
		return
	}
	setBodyLimit(w, r, 16<<10)
	var req subscriptionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	events, err := parseWebhookEvents(req.Events)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub := &webhooks.Subscription{
		EndpointID: strings.TrimSpace(req.EndpointID),
		Owner:      session.Username,
		Label:      strings.TrimSpace(req.Label),
		Scope:      webhooks.Scope(req.Scope),
		Job:        jobwatch.JobID{Cluster: req.Cluster, Proc: req.Proc, Schedd: strings.ToLower(req.Schedd)},
		BatchID:    req.BatchID,
		BatchName:  req.BatchName,
		Events:     events,
	}

	created, err := h.webhooks.CreateSubscription(r.Context(), sub)
	if err != nil {
		h.logger.Warn(logging.DestinationHTTP, "Refused to create a webhook subscription",
			"owner", session.Username, "error", err)
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.logger.Info(logging.DestinationHTTP, "User created a webhook subscription",
		"owner", session.Username, "subscription", created.ID, "webhook", created.EndpointID,
		"scope", string(created.Scope), "events", strings.Join(req.Events, ","))
	h.writeJSON(w, http.StatusCreated, map[string]any{"subscription": created})
}

// handleWebhookSubscriptionItem serves /api/v1/webhook-subscriptions/{id}.
func (h *Handler) handleWebhookSubscriptionItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/webhook-subscriptions/")
	if id == "" || strings.Contains(id, "/") {
		h.writeError(w, http.StatusBadRequest, "Invalid subscription id")
		return
	}
	session, ok := h.webhookCaller(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		sub, err := h.webhooks.GetSubscription(r.Context(), session.Username, id)
		if h.webhookError(w, err) {
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]any{"subscription": sub})
	case http.MethodPatch:
		if !h.requireJSONRequest(w, r) {
			return
		}
		setBodyLimit(w, r, 16<<10)
		var req subscriptionUpdateRequest
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
		sub, err := h.webhooks.UpdateSubscription(r.Context(), session.Username, id, req.Label, events)
		if errors.Is(err, webhooks.ErrNotFound) {
			h.writeError(w, http.StatusNotFound, "No such subscription")
			return
		}
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.writeJSON(w, http.StatusOK, map[string]any{"subscription": sub})
	case http.MethodDelete:
		if h.webhookError(w, h.webhooks.DeleteSubscription(r.Context(), session.Username, id)) {
			return
		}
		h.logger.Info(logging.DestinationHTTP, "User deleted a webhook subscription",
			"owner", session.Username, "subscription", id)
		h.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
	default:
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
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
