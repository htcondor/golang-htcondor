package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"time"

	"github.com/PelicanPlatform/classad/classad"
	"github.com/PelicanPlatform/classad/collections"
	"github.com/PelicanPlatform/classad/collections/vm"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/logging"
)

// handleCollectorWatch streams collector ad changes to the client as Server-Sent
// Events. Query parameters:
//
//	type       - the ad type to watch (default "StartdAd")
//	constraint - an optional ClassAd match expression (e.g. State == "Claimed")
//
// Resumption uses the standard SSE Last-Event-ID header, which carries the last
// cursor the client received. Each event is emitted as:
//
//	event: reset|upsert|delete|synced|resync|goingaway
//	id:    <base64 cursor>              (on synced and live events)
//	data:  {"key":"<base64>","ad":{...}} (ad present only on upsert)
func (h *Handler) handleCollectorWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, release, ok := h.authenticateStream(w, r)
	if !ok {
		return
	}
	defer release()
	col := h.getCollector()
	if col == nil {
		h.writeError(w, http.StatusServiceUnavailable, "no collector configured")
		return
	}

	adType := r.URL.Query().Get("type")
	if adType == "" {
		adType = "StartdAd"
	}
	constraint := r.URL.Query().Get("constraint")
	cursor := cursorFromRequest(r)

	// This stream sends whole ads -- there is no projection to clamp --
	// so under a restricted policy each one is filtered on the way out.
	privileged := h.seesAllJobs(ctx, r, jobScopeRead)
	if err := h.collectorWatchRefusal(privileged, adType, constraint); err != nil {
		h.writeError(w, http.StatusForbidden, err.Error())
		return
	}

	flusher, ok := sseSetup(w)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, "server does not support streaming")
		return
	}
	defer h.streamOpened()()

	// The same ceiling the job watch takes. stream_limit.go's cap already
	// reasons about every stream as one class -- "for up to half an hour"
	// -- and that was true only of the job watch; this one had no maximum
	// age at all. EventSource reconnects on its own and resumes from
	// Last-Event-ID, so ending costs one re-subscribe.
	ctx, cancel := context.WithTimeout(ctx, jobWatchMaxAge)
	defer cancel()

	events, err := col.WatchAds(ctx, adType, constraint, cursor)
	if err != nil {
		// Headers are already sent; report the failure as an SSE error event.
		_ = writeWatchSSE(w, flusher, "error", "", nil, nil, err.Error())
		return
	}

	// Re-read the privilege as the stream runs, rather than trusting the
	// boolean the open produced.
	//
	// privileged decides both the gates above and FilterClassAd on every
	// event, so a stream opened while the caller could read the pool keeps
	// emitting unredacted ads after that stops being true. The sharpest
	// case is a browser session whose read scope comes from armed
	// superuser mode, which self-disarms after 30 minutes: the open
	// stream carries RemoteOwner, RemoteUser, RemoteProject and
	// GlobalJobId past the arm, past an explicit disarm, and past logout,
	// where a fresh request would be filtered or refused. Group removal
	// and an expired session do the same.
	//
	// Nothing is paid for under the default open mode, where the policy
	// can neither restrict nor refuse anyone and the privilege decides
	// nothing.
	recheck := h.poolQueryPolicy.Restricts(false) || h.poolQueryPolicy.Refuses(false)
	recheckEvery := h.jobWatchRecheck
	if recheckEvery <= 0 {
		recheckEvery = jobWatchRecheckInterval
	}
	lastCheck := time.Now()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if recheck && time.Since(lastCheck) >= recheckEvery {
				lastCheck = time.Now()
				var err error
				privileged, err = h.collectorWatchRecheck(ctx, r, adType, constraint)
				if err != nil {
					h.logger.Info(logging.DestinationSecurity,
						"Ending a collector watch: the caller may no longer read the pool",
						"ad_type", adType, "remote_addr", r.RemoteAddr, "reason", err.Error())
					_ = writeWatchSSE(w, flusher, "error", "", nil, nil,
						"your privilege to read this stream has lapsed; reconnect")
					return
				}
			}
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			_ = flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			ad := h.poolQueryPolicy.FilterClassAd(privileged, adType, ev.Ad)
			if err := writeWatchSSE(w, flusher, sseKind(ev.Kind.String()), watchKeyString(ev.Key), ad, ev.Cursor, ""); err != nil {
				return
			}
		}
	}
}

// collectorWatchRefusal reports why a caller whose cross-user read
// privilege is `privileged` may not be served this collector watch, or
// nil if they may.
//
// One predicate for the open and for the re-check inside the stream, so
// a running stream is held to exactly what a fresh request would get.
// Keeping the two in step by hand is what let the stream outlive the
// decision that admitted it.
func (h *Handler) collectorWatchRefusal(privileged bool, adType, constraint string) error {
	if h.poolQueryPolicy.Refuses(privileged) {
		return errors.New("Pool queries are restricted to administrators at this site")
	}
	if err := h.poolQueryPolicy.CheckAdType(privileged, adType); err != nil {
		return err
	}
	return h.poolQueryPolicy.CheckConstraint(privileged, adType, constraint)
}

// collectorWatchRecheck re-reads the caller's cross-user read privilege
// and reports the value a running stream should carry on with, plus why
// it must end if it must.
//
// The privilege is read again rather than carried from the open: that
// boolean decides FilterClassAd on every event, so a stream opened while
// the caller could read the pool goes on emitting unredacted ads after
// that stops being true. A privilege that merely narrowed needs no
// disconnect -- the new value makes FilterClassAd redact from the next
// event on, which is what a fresh request would get. Only a caller who
// may no longer read the stream at all is sent away.
func (h *Handler) collectorWatchRecheck(ctx context.Context, r *http.Request, adType, constraint string) (bool, error) {
	privileged := h.seesAllJobs(ctx, r, jobScopeRead)
	return privileged, h.collectorWatchRefusal(privileged, adType, constraint)
}

// watchAllJobsScope returns a context that is cancelled when a caller
// admitted to everyone's jobs may no longer read them.
//
// A stream's owner scope is decided once, at open, and then baked into
// the constraint or the feed subscription it runs on -- so unlike the
// collector watch, which re-filters each ad, there is nothing to narrow
// after the fact. Ending the stream is the narrowing. The browser's
// EventSource reconnects on its own, and the new request is scoped
// afresh, so a caller who still has the privilege loses nothing but a
// re-subscribe and one who has lost it gets their own jobs from then on.
//
// Only a stream opened for everyone's jobs can lapse this way: a caller
// confined to their own has nothing to lose, so they get ctx back
// unchanged and pay nothing. The privilege lapses in several ordinary
// ways -- armed superuser mode self-disarms after 30 minutes or is
// disarmed explicitly, a browser session expires or logs out, a group
// membership is withdrawn, an API key's entitlement is revoked -- and
// none of them reaches a stream that asked once.
func (h *Handler) watchAllJobsScope(ctx context.Context, r *http.Request, all bool) context.Context {
	if !all {
		return ctx
	}
	interval := h.jobWatchRecheck
	if interval <= 0 {
		interval = jobWatchRecheckInterval
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !h.seesAllJobs(ctx, r, jobScopeRead) {
					h.logger.Info(logging.DestinationSecurity,
						"Ending a stream opened for every owner's jobs: the caller may no longer read them",
						"remote_addr", r.RemoteAddr)
					return
				}
			}
		}
	}()
	return ctx
}

// sseSetup writes the SSE response headers and returns the flusher. It reports
// false if the writer cannot stream.
func sseSetup(w http.ResponseWriter) (*http.ResponseController, bool) {
	// http.NewResponseController rather than a w.(http.Flusher) assertion.
	// Some routes are served through a status-capturing wrapper, which
	// implements Unwrap precisely so the controller can find the real
	// writer; asserting Flusher on the wrapper fails and the endpoint
	// reports that the server cannot stream when it can.
	//
	// Headers first: flushing is what commits them, so probing for the
	// capability before setting Content-Type would send the response as
	// whatever the default is and EventSource would discard the stream.
	flusher := http.NewResponseController(w)
	// Drop the server's write deadline for this response. WriteTimeout is
	// set once for the whole write, not per write, so a stream that
	// outlives it dies mid-flight: the first write past the deadline
	// fails and the handler unwinds. Before this, every SSE stream here
	// was capped at WriteTimeout (30s by default) and survived only
	// because EventSource silently reconnects -- which costs a
	// re-subscribe and a fresh snapshot every half minute, and hides the
	// truncation from anyone reading the code.
	//
	// An error is not fatal: a writer that cannot take a deadline simply
	// keeps the server's, which is the behaviour we had.
	_ = flusher.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": connected\n\n")) // flush headers; EventSource goes "open"
	if err := flusher.Flush(); err != nil {
		return nil, false
	}
	return flusher, true
}

// writeWatchSSE writes one watch event as an SSE frame. A non-empty cursor is
// emitted as the id: (the resume token); errMsg, if set, is included in data.
func writeWatchSSE(w http.ResponseWriter, flusher *http.ResponseController, event, key string, ad *classad.ClassAd, cursor []byte, errMsg string) error {
	var b strings.Builder
	if len(cursor) > 0 {
		fmt.Fprintf(&b, "id: %s\n", base64.StdEncoding.EncodeToString(cursor))
	}
	payload := map[string]any{}
	if key != "" {
		payload["key"] = key
	}
	if ad != nil {
		payload["ad"] = ad // ClassAd.MarshalJSON
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil // skip an unencodable event rather than tear down the stream
	}
	fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", event, data)
	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	_ = flusher.Flush()
	return nil
}

// sseKind maps a watch Kind name ("Upsert", ...) to its lower-case SSE event name.
func sseKind(kindName string) string { return strings.ToLower(kindName) }

// watchKeyString renders a watch key as a stable, JSON-safe identifier the
// client uses to correlate upserts and deletes. A clean printable-ASCII key
// (e.g. a job's "cluster.proc") passes through as-is; a key with a NUL or other
// non-printable byte (e.g. the collector's composite Name\0Address) is base64ed.
func watchKeyString(key string) string {
	if key == "" {
		return ""
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return base64.StdEncoding.EncodeToString([]byte(key))
		}
	}
	return key
}

// collectionsKind maps a collections.WatchKind to its SSE event name.
func collectionsKind(k collections.WatchKind) string {
	switch k {
	case collections.WatchUpsert:
		return "upsert"
	case collections.WatchDelete:
		return "delete"
	case collections.WatchReset:
		return "reset"
	case collections.WatchSynced:
		return "synced"
	case collections.WatchResync:
		return "resync"
	default:
		return "unknown"
	}
}

// handleJobsWatch streams job-ad changes -- from the schedd's job_queue.log
// mirror -- to the client as Server-Sent Events, using the same framing and
// resume semantics as handleCollectorWatch. Query parameter constraint (a ClassAd
// match expression, e.g. DAGManJobId == 42) delivers only matching jobs; a job
// that stops matching arrives as a delete.
func (h *Handler) handleJobsWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Both branches below hold a stream.
	ctx, release, ok := h.authenticateStream(w, r)
	if !ok {
		return
	}
	defer release()
	if h.multi != nil {
		// The hub's jobs stream, the caller's own jobs on every AP.
		h.streamJobsWatchFromMirror(ctx, w, r)
		return
	}
	if h.jobMirror == nil {
		// No local job_queue.log to tail, but this access point may
		// still be following its queue through the htcondordb mirror.
		// That mirror is the one most deployments actually have -- an
		// API server in its own container cannot read the schedd's
		// spool -- and following the queue is a large part of what it
		// is for. Refusing here while it sits there healthy was the
		// endpoint ignoring the answer it already had.
		if h.jobWatchFeed != nil && h.dbMirror.Enabled() {
			h.streamJobsWatchFromMirror(ctx, w, r)
			return
		}
		h.writeError(w, http.StatusServiceUnavailable,
			"this access point is not following its job queue: it has neither a readable job_queue.log "+
				"(HTTP_API_JOB_QUEUE_LOG) nor an htcondordb mirror")
		return
	}
	// This branch reads job_queue.log itself: no schedd sees the
	// request, so nothing but this clause keeps one user's ads from
	// another. Own jobs unless an administrator asks for everyone's,
	// as on the mirror branch.
	_, all := activityStreamScope(htcondor.GetAuthenticatedUserFromContext(ctx),
		r.URL.Query().Get("owned_by_me"), h.seesAllJobs(ctx, r, jobScopeRead))
	constraint, scopeErr := localReadOwnerScope(ctx, r.URL.Query().Get("constraint"), all)
	if errors.Is(scopeErr, errUnidentifiedCaller) {
		h.writeError(w, http.StatusUnauthorized, "authentication failed: "+scopeErr.Error())
		return
	}

	// Ceiling first, then the privilege re-check, so the stream ends on
	// whichever comes first.
	ctx, cancel := context.WithTimeout(ctx, jobWatchMaxAge)
	defer cancel() // stop the watch iterator when the handler returns
	ctx = h.watchAllJobsScope(ctx, r, all)

	cursor := cursorFromRequest(r)

	flusher, ok := sseSetup(w)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, "server does not support streaming")
		return
	}
	defer h.streamOpened()()

	if scopeErr != nil {
		_ = writeWatchSSE(w, flusher, "error", "", nil, nil, fmt.Sprintf("bad constraint: %v", scopeErr))
		return
	}
	seq, err := h.jobMirror.Collection().Watch(ctx, cursor)
	if err != nil {
		_ = writeWatchSSE(w, flusher, "error", "", nil, nil, err.Error())
		return
	}
	if strings.TrimSpace(constraint) != "" {
		q, err := vm.Parse(constraint)
		if err != nil {
			_ = writeWatchSSE(w, flusher, "error", "", nil, nil, fmt.Sprintf("bad constraint: %v", err))
			return
		}
		seq = collections.WatchFilter(seq, q.Matches)
	}

	streamCollectionEvents(ctx, w, r, flusher, seq)
}

// streamCollectionEvents pumps a collections watch sequence to the client as SSE
// frames until ctx or the request ends, servicing a heartbeat so idle proxies
// keep the connection. The pull iterator is fed through a channel so the loop can
// also select on the ticker and disconnect.
func streamCollectionEvents(ctx context.Context, w http.ResponseWriter, r *http.Request, flusher *http.ResponseController, seq iter.Seq[collections.WatchEvent]) {
	events := make(chan collections.WatchEvent, 64)
	go func() {
		defer close(events)
		for ev := range seq {
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			_ = flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if err := writeWatchSSE(w, flusher, collectionsKind(ev.Kind), watchKeyString(string(ev.Key)), ev.Ad, ev.Cursor, ""); err != nil {
				return
			}
		}
	}
}

// cursorFromRequest recovers a resume cursor from the SSE Last-Event-ID header
// (or a ?cursor= query param), which carries the base64 cursor of the last event
// the client saw. Returns nil (full replay) if absent or malformed.
func cursorFromRequest(r *http.Request) []byte {
	id := r.Header.Get("Last-Event-ID")
	if id == "" {
		id = r.URL.Query().Get("cursor")
	}
	if id == "" {
		return nil
	}
	c, err := base64.StdEncoding.DecodeString(id)
	if err != nil {
		return nil
	}
	return c
}
