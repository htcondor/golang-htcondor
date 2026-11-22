-- +goose Up
-- +goose StatementBegin

-- Outbound webhooks: tell an outside service when something happens to
-- a user's jobs.
--
-- The association lives here rather than on the job because a job ad is
-- PUBLIC -- anyone who may read the queue reads every attribute -- and a
-- webhook destination is a capability URL for several of the services
-- people will point one at. Putting it in the ad would hand the ability
-- to post into somebody's Slack to every user of the access point.
--
-- This is the other half of a connector. The clients in oauth2_clients
-- let an outside service act as the user HERE; a webhook tells an
-- outside service what happened here. Same integration, opposite
-- direction.

CREATE TABLE webhooks (
    -- Opaque id, handed back to the caller and appearing in payloads.
    id TEXT PRIMARY KEY,

    -- Whose hook this is. Every read filters on it: the evaluator reads
    -- job ads as the daemon, with none of the schedd's per-caller ACL
    -- behind it, so this column is the only thing confining a hook to
    -- the person who registered it. The same reasoning as
    -- job_watches.owner, and the same consequence if it is forgotten.
    owner TEXT NOT NULL,
    -- The user's own name for it, echoed back so somebody holding
    -- several can tell them apart.
    label TEXT NOT NULL DEFAULT '',

    -- WHAT IS WATCHED ------------------------------------------------
    -- 'job', 'batch' or 'owner'. Text rather than an integer so a
    -- stored row is readable, and so a value from a future version
    -- fails loudly on load instead of aliasing onto a valid scope.
    scope TEXT NOT NULL,
    -- scope='job': the one job. NULL otherwise.
    job_cluster INTEGER,
    job_proc INTEGER,
    -- The access point, lower-cased, on a server fronting several. Empty
    -- on a single-schedd server, exactly as jobwatch.JobID records it.
    schedd TEXT NOT NULL DEFAULT '',
    -- scope='batch': the batch, as batches.go defines one -- a DAG's
    -- root cluster, a named batch's smallest cluster, or a lone cluster.
    -- Stored as the id AND the name because a batch is DERIVED from job
    -- ads rather than stored: the id is what the UI links to, and the
    -- name is what still selects the jobs after the id's cluster has
    -- left the queue.
    batch_id TEXT NOT NULL DEFAULT '',
    batch_name TEXT NOT NULL DEFAULT '',

    -- WHICH EVENTS ---------------------------------------------------
    -- JSON array from jobwatch's closed vocabulary (done, succeeded,
    -- failed, held, running). Deliberately the SAME vocabulary as the
    -- watch API rather than a parallel one: "JobStatus == 4" is what
    -- somebody writes when asked to notify on completion, and it never
    -- fires, because the schedd destroys the ad instead of changing it.
    -- One vocabulary means that trap is unsayable in both places.
    events_json TEXT NOT NULL,

    -- WHERE IT GOES --------------------------------------------------
    -- The destination URL, sealed with the envelope scheme every other
    -- long-lived secret here uses (appdb/seal). It is treated as a
    -- secret and not as an address: for Slack, Zapier, Teams and most
    -- of the rest, possession of the URL IS the authorization to post.
    --
    -- dek empty means the value is plaintext, written by a server with
    -- no KEK configured. A non-empty dek on a server with no KEK is
    -- refused rather than guessed at -- the same rule as the upstream
    -- refresh tokens, for the same reason: handing ciphertext to a
    -- provider as though it were the value is worse than an error.
    url_data BLOB NOT NULL,
    url_dek BLOB NOT NULL DEFAULT x'',

    -- The per-hook HMAC key a receiver verifies the signature with,
    -- sealed the same way. Per-hook rather than per-server so that
    -- rotating one hook's key, or leaking it, is confined to that hook.
    secret_data BLOB NOT NULL,
    secret_dek BLOB NOT NULL DEFAULT x'',

    -- STATE ----------------------------------------------------------
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- When this hook stops being evaluated. For a job or batch hook it
    -- is set once the selection can no longer produce events -- every
    -- job ended and the grace period for a late history record elapsed
    -- -- rather than from any archive or retention clock, which measure
    -- something else (and, for EnteredHistoryTime, measure two
    -- different things depending on how the row arrived). An owner hook
    -- has no natural end, so it carries a TTL from the start and is
    -- renewed by being edited.
    expires_at TIMESTAMP,

    -- Set when delivery was given up on, with the reason shown to the
    -- user. A hook is disabled rather than silently dropped: silence is
    -- the one outcome somebody reads as "nothing has happened yet".
    disabled_at TIMESTAMP,
    disabled_reason TEXT NOT NULL DEFAULT '',
    -- Consecutive permanently-failing deliveries. Reset by a success.
    consecutive_failures INTEGER NOT NULL DEFAULT 0,

    -- Every job this hook has ever selected, range-encoded exactly as
    -- job_watches.tracked_json is. Load-bearing, not a cache: a
    -- finished job is DESTROYED from the queue, so "it was here and now
    -- it is gone" is the evidence that a terminal event happened at
    -- all, and per-job JSON for a 100k-job cluster costs megabytes a
    -- pass where ranges cost tens of bytes.
    tracked_json TEXT NOT NULL DEFAULT '[]',

    -- When the selection last exceeded the per-hook fan-out ceiling.
    -- Recorded rather than acted on silently: a hook that would have
    -- posted ten thousand times has to say so, or the missing
    -- deliveries look like events that never happened.
    capped_at TIMESTAMP,
    -- How many jobs were in scope when that happened.
    capped_jobs INTEGER NOT NULL DEFAULT 0,

    -- The last delivery attempt, for the UI's "is this working" line.
    last_delivery_at TIMESTAMP,
    last_status INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    -- The last TEST delivery, kept apart from real traffic so that
    -- "I pressed Test and it worked" does not overwrite the record of
    -- the deliveries that matter, nor the reverse.
    last_test_at TIMESTAMP,
    last_test_status INTEGER,
    last_test_error TEXT NOT NULL DEFAULT ''
);

-- The evaluator's sweep: every hook still live.
CREATE INDEX webhooks_live ON webhooks(expires_at) WHERE disabled_at IS NULL;
-- A user's own list, and the evaluator's grouping, are both by owner.
CREATE INDEX webhooks_owner ON webhooks(owner);

-- One delivery, which is also the record that it has been decided on.
--
-- The primary key IS the idempotency key, and that is the whole design:
-- the evaluator INSERTs OR IGNOREs, so a (hook, event, job) that has
-- already been enqueued cannot be enqueued twice however many passes
-- see it. The dedup ledger and the delivery queue are the same table
-- because keeping them apart means keeping two things consistent.
CREATE TABLE webhook_deliveries (
    -- Deterministic for a real event: <hook>.<event>.<schedd>.<cluster>.<proc>.
    -- Sent to the receiver as Idempotency-Key and in the payload, so a
    -- consumer that gets a retry can recognise it. A test delivery gets
    -- a random one: pressing Test twice means two tests.
    id TEXT PRIMARY KEY,
    webhook_id TEXT NOT NULL,
    -- Denormalised from webhooks.owner so the worker can log and meter
    -- per user without a join, and so a delivery outlives the hook it
    -- came from for as long as it takes to report it.
    owner TEXT NOT NULL,

    event TEXT NOT NULL,
    -- The job this is about. NULL for a test delivery and for a
    -- rolled-up one.
    job_cluster INTEGER,
    job_proc INTEGER,
    schedd TEXT NOT NULL DEFAULT '',

    -- The body as it will be sent, rendered once at enqueue. Rendering
    -- it per attempt would mean a retry carrying different bytes from
    -- the attempt it is retrying, which breaks both the signature and
    -- the receiver's idempotency.
    payload_json TEXT NOT NULL,

    -- 'pending', 'delivered' or 'abandoned'. Abandoned is terminal and
    -- deliberately distinguished from delivered: "we stopped trying" is
    -- a different thing to report than "it arrived".
    state TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    -- When the next attempt may run. The worker claims on this, so a
    -- backoff is a timestamp rather than a sleeping goroutine that a
    -- restart would lose.
    next_attempt_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at TIMESTAMP,
    last_status INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    -- Why it was abandoned: a permanent refusal, or the attempt budget
    -- running out. Shown to the user, who otherwise cannot tell a
    -- misconfigured URL from an endpoint that was down all afternoon.
    abandoned_reason TEXT NOT NULL DEFAULT ''
);

-- The worker's claim: what is due, oldest first.
CREATE INDEX webhook_deliveries_due
    ON webhook_deliveries(next_attempt_at) WHERE state = 'pending';
-- The UI's per-hook history, and the cascade when a hook is deleted.
CREATE INDEX webhook_deliveries_hook
    ON webhook_deliveries(webhook_id, created_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS webhook_deliveries_hook;
DROP INDEX IF EXISTS webhook_deliveries_due;
DROP TABLE IF EXISTS webhook_deliveries;
DROP INDEX IF EXISTS webhooks_owner;
DROP INDEX IF EXISTS webhooks_live;
DROP TABLE IF EXISTS webhooks;

-- +goose StatementEnd
