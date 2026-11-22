package httpserver

import (
	"context"
	"fmt"
	"time"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
	"github.com/htcondor/golang-htcondor/webapi/webhooks"
)

// Wiring the outbound webhooks into the daemon.
//
// The evaluator reads jobs through the SAME jobwatch.Source the watch
// evaluator uses -- the mirror where one is usable, the schedd for the live
// queue otherwise, and the federation hub in multi-AP mode. That is the
// point of reusing it: a hook and a watch over the same jobs must not be
// able to disagree about whether a job finished, and they cannot if they are
// reading the same thing through the same code.
//
// It also rides the same change stream. The periodic sweep is what makes a
// hook correct; the nudge is what makes it prompt.

// initWebhooks builds the webhook store, evaluator and dispatcher. src is the
// job source the watch evaluator was given.
//
// Called from NewHandler with the source already chosen, rather than building
// its own: a second source would be a second answer to "is this job still in
// the queue".
func (h *Handler) initWebhooks(cfg HandlerConfig, src jobwatch.Source) {
	if h.db == nil || cfg.WebhooksDisabled {
		// Nothing is built, so every endpoint answers 503 and neither loop
		// starts. A 503 that says webhooks are unavailable is a clearer
		// answer to somebody checking than a 404 that reads as an old build.
		return
	}
	// The sealer may be nil, which means this deployment has no KEK and
	// stores its secrets in the clear -- the store records which, so a
	// later read knows.
	h.webhooks = webhooks.NewStore(h.db, h.sealerOrNil())
	h.webhookSender = webhooks.NewSender()
	h.webhookEval = webhooks.NewEvaluator(h.webhooks, src,
		func(msg string, args ...any) { h.logger.Info(logging.DestinationHTTP, msg, args...) })
	if cfg.WebhookFanoutLimit > 0 {
		h.webhookEval.FanoutCap = cfg.WebhookFanoutLimit
	}
	h.webhookDispatch = webhooks.NewDispatcher(h.webhooks, h.webhookSender,
		func(msg string, args ...any) { h.logger.Info(logging.DestinationHTTP, msg, args...) })
}

// sealerOrNil returns the envelope sealer, or nil where none is configured.
//
// A method rather than the field, so the nil case is named at the one call
// site that has to think about it.
func (h *Handler) sealerOrNil() webhooks.Sealer {
	if h.sealer == nil {
		return nil
	}
	return h.sealer
}

// startWebhooks runs the evaluator and the dispatcher for the life of the
// handler.
//
// Unconditional, for the same reason the watch evaluator is: a webhook that
// is registered and never evaluated is the worst outcome available, because
// the user is told it is watching and then hears nothing -- which is
// indistinguishable from nothing having happened.
func (h *Handler) startWebhooks(ctx context.Context) {
	if h.webhookEval == nil || h.webhookDispatch == nil {
		return
	}
	// The sweep reads every user's jobs, owner-scoped by the evaluator
	// itself, as this daemon.
	evalCtx := htcondor.WithDaemonCredential(ctx, "webhook evaluation")
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.webhookEval.Run(evalCtx)
	}()
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.webhookDispatch.Run(ctx)
	}()
}

// startWebhookNudge re-evaluates an owner's hooks as soon as the change
// stream says one of their jobs moved, instead of waiting for the sweep.
//
// Strictly an accelerator, and never evidence: all it does is ask the
// evaluator to look now, and the answer still comes from reading the queue
// and the archive. A missed or dropped event costs latency, not correctness.
func (h *Handler) startWebhookNudge(ctx context.Context) {
	if h.jobWatchFeed == nil || h.webhookEval == nil {
		return
	}
	ctx = htcondor.WithDaemonCredential(ctx, "webhook evaluation")
	nudger := jobwatch.NewNudger(h.jobWatchFeed, h.webhookEval.CheckOwner,
		func(format string, args ...any) {
			h.logger.Debug(logging.DestinationHTTP, fmt.Sprintf(format, args...))
		})
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		nudger.Run(ctx)
	}()
}

// webhookDeliveryRetention is how long a finished delivery is kept.
//
// Long enough that a user who comes back on Monday can see what happened on
// Friday, short enough that the table is not an archive. Pending rows are
// never swept by age; the dispatcher is responsible for ending those.
const webhookDeliveryRetention = 14 * 24 * time.Hour

// pruneWebhookDeliveries drops delivery records that have been settled for
// longer than the retention window.
func (h *Handler) pruneWebhookDeliveries(ctx context.Context) (int64, error) {
	if h.webhooks == nil {
		return 0, nil
	}
	return h.webhooks.PruneDeliveries(ctx, time.Now().UTC().Add(-webhookDeliveryRetention))
}
