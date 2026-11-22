-- +goose Up
-- +goose StatementBegin

-- Split a webhook into a DESTINATION and the SUBSCRIPTIONS that post to
-- it.
--
-- 0042 made those one row, so watching a second job meant registering a
-- second destination: pasting the capability URL again and copying a
-- second signing secret, for the same receiver. The secret is shown
-- exactly once, so "set it up again" is the most error-prone moment the
-- feature has, and the old shape demanded it per subscription rather
-- than per receiver.
--
-- The split also fixes delivery health, which was never a property of a
-- subscription. A destination that is down is down for everything
-- pointed at it: under 0042 one hook would be disabled for it while the
-- others kept posting to the same dead URL on their own schedules.
--
-- This DROPS the 0042 tables rather than migrating them. Webhooks have
-- never been in a tagged release, so the only rows anywhere are on
-- development access points, and a conversion that will run exactly
-- nowhere in production is a liability rather than a kindness: it is
-- code nobody will test and everybody will read.

DROP INDEX IF EXISTS webhook_deliveries_hook;
DROP INDEX IF EXISTS webhook_deliveries_due;
DROP TABLE IF EXISTS webhook_deliveries;
DROP INDEX IF EXISTS webhooks_owner;
DROP INDEX IF EXISTS webhooks_live;
DROP TABLE IF EXISTS webhooks;

-- A destination: where deliveries go, and the key that proves they came
-- from here. Registered once per receiver.
CREATE TABLE webhook_endpoints (
    id TEXT PRIMARY KEY,

    -- Whose endpoint this is. Every read filters on it: the evaluator
    -- reads job ads as the daemon, with none of the schedd's per-caller
    -- ACL behind it, so this column is the only thing confining a
    -- destination to the person who registered it.
    owner TEXT NOT NULL,
    -- The user's own name for the receiver ("my Zap", "#jobs").
    label TEXT NOT NULL DEFAULT '',

    -- The destination URL, sealed with the envelope scheme every other
    -- long-lived secret here uses (appdb/seal). Treated as a secret and
    -- not as an address: for Slack, Zapier, Teams and most of the rest,
    -- possession of the URL IS the authorization to post.
    --
    -- dek empty means the value is plaintext, written by a server with
    -- no KEK configured. A non-empty dek on a server with no KEK is
    -- refused rather than guessed at.
    url_data BLOB NOT NULL,
    url_dek BLOB NOT NULL DEFAULT x'',

    -- The HMAC key a receiver verifies the signature with, sealed the
    -- same way. Per endpoint, which is the point of this table: one
    -- receiver, one key, however many subscriptions feed it.
    secret_data BLOB NOT NULL,
    secret_dek BLOB NOT NULL DEFAULT x'',

    -- The PREVIOUS signing key, kept alive for a grace period after a
    -- rotation so a receiver can be updated without dropping anything.
    --
    -- While this is live a delivery carries TWO signatures, one per key,
    -- and the receiver matches whichever it holds. The alternative --
    -- swapping the key outright -- loses deliveries rather than delaying
    -- them: a receiver that rejects a signature answers 4xx, and a 4xx
    -- that is not 408 or 429 is a permanent refusal here, so those
    -- deliveries are abandoned rather than retried once the receiver is
    -- fixed.
    --
    -- NULL expiry means there is no previous key. A row whose expiry has
    -- passed is treated as having none; the columns are cleared lazily
    -- rather than by a sweep, since nothing reads them once expired.
    prev_secret_data BLOB,
    prev_secret_dek BLOB NOT NULL DEFAULT x'',
    prev_secret_expires_at TIMESTAMP,

    -- 'summary' or 'full'. Summary is the small fixed projection;
    -- full adds the job's whole ad beside it. On the ENDPOINT rather
    -- than the subscription so that one receiver always gets one shape
    -- -- a Zap maps fields once -- and so two subscriptions selecting
    -- the same job and event can collapse into a single delivery,
    -- which they could not if their bodies might differ.
    payload_mode TEXT NOT NULL DEFAULT 'summary',

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- DELIVERY HEALTH, which belongs to the destination ------------
    -- Deliveries given up on in a row. Reset by any success.
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    -- When this endpoint may next be tried. A failing destination is
    -- NOT disabled: it backs off exponentially to a ceiling and keeps
    -- trying, because the common case is a receiver that is down for a
    -- while and comes back, and a disabled hook comes back only if
    -- somebody notices a page they had no reason to revisit.
    backoff_until TIMESTAMP,

    last_delivery_at TIMESTAMP,
    last_status INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    -- The last TEST, kept apart from real traffic so "I pressed Test
    -- and it worked" does not overwrite the record of the deliveries
    -- that matter, nor the reverse.
    last_test_at TIMESTAMP,
    last_test_status INTEGER,
    last_test_error TEXT NOT NULL DEFAULT '',

    -- When this endpoint's pending backlog last hit the ceiling, and
    -- how many were queued then. Recorded rather than acted on
    -- silently: a receiver that has been down for a week has events
    -- missing, and that has to be visible.
    backlog_at TIMESTAMP,
    backlog_depth INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX webhook_endpoints_owner ON webhook_endpoints(owner);

-- What is watched, and which events. Points at an endpoint.
CREATE TABLE webhook_subscriptions (
    id TEXT PRIMARY KEY,
    -- The destination this feeds. The REFERENCES clause documents the
    -- relationship; it does NOT enforce it, because SQLite checks
    -- foreign keys only when PRAGMA foreign_keys is on and this
    -- database never turns it on. Store.DeleteEndpoint deletes these
    -- rows itself, and must keep doing so.
    endpoint_id TEXT NOT NULL
        REFERENCES webhook_endpoints(id) ON DELETE CASCADE,

    -- Denormalised from the endpoint so every read can filter on it
    -- without a join, for the same reason the endpoint carries it.
    owner TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',

    -- WHAT IS WATCHED ------------------------------------------------
    -- 'job', 'batch' or 'owner'. Text rather than an integer so a
    -- stored row is readable, and so a value from a future version
    -- fails loudly on load instead of aliasing onto a valid scope.
    scope TEXT NOT NULL,
    -- scope='job': the one job. NULL otherwise.
    job_cluster INTEGER,
    job_proc INTEGER,
    -- The access point, lower-cased, on a server fronting several.
    schedd TEXT NOT NULL DEFAULT '',
    -- scope='batch': the batch, as batches.go defines one. Stored as
    -- the id AND the name because a batch is DERIVED from job ads
    -- rather than stored: the id is what the UI links to, and the name
    -- is what still selects the jobs after the id's cluster has left
    -- the queue.
    batch_id TEXT NOT NULL DEFAULT '',
    batch_name TEXT NOT NULL DEFAULT '',

    -- JSON array from jobwatch's closed vocabulary (done, succeeded,
    -- failed, held, running). Deliberately the SAME vocabulary as the
    -- watch API rather than a parallel one.
    events_json TEXT NOT NULL,

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- When this subscription stops being evaluated. For a job or batch
    -- it is set once the selection can no longer produce events; an
    -- owner subscription has no natural end, so it carries a TTL from
    -- the start and is renewed by being edited.
    expires_at TIMESTAMP,

    -- Every job this subscription has ever selected, range-encoded
    -- exactly as job_watches.tracked_json is. Load-bearing, not a
    -- cache: a finished job is DESTROYED from the queue, so "it was
    -- here and now it is gone" is the evidence that a terminal event
    -- happened at all.
    tracked_json TEXT NOT NULL DEFAULT '[]',

    -- When this selection last exceeded the fan-out ceiling, and how
    -- many jobs were in scope then.
    capped_at TIMESTAMP,
    capped_jobs INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX webhook_subscriptions_owner ON webhook_subscriptions(owner);
CREATE INDEX webhook_subscriptions_endpoint ON webhook_subscriptions(endpoint_id);
-- The evaluator's sweep: every subscription still live.
CREATE INDEX webhook_subscriptions_live ON webhook_subscriptions(expires_at);

-- One delivery, which is also the record that it has been decided on.
--
-- The primary key IS the idempotency key, and that is the whole design:
-- the evaluator INSERTs OR IGNOREs, so a (endpoint, event, job) that has
-- already been enqueued cannot be enqueued twice however many passes
-- see it -- NOR by however many subscriptions select it. Two
-- subscriptions on one endpoint that both match a job now collapse into
-- one POST, which is why payload_mode lives on the endpoint.
CREATE TABLE webhook_deliveries (
    -- Deterministic for a real event:
    -- <endpoint>.<event>.<schedd>.<cluster>.<proc>. Sent to the
    -- receiver as Idempotency-Key and in the payload. A test delivery
    -- gets a random one: pressing Test twice means two tests.
    id TEXT PRIMARY KEY,
    endpoint_id TEXT NOT NULL,
    -- Denormalised from the endpoint so the worker can log and meter
    -- per user without a join, and so a delivery outlives the endpoint
    -- it came from for as long as it takes to report it.
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
    --
    -- Nothing is abandoned for failing any more -- a destination backs
    -- off instead of being given up on. What still abandons is a
    -- request the destination REFUSED (a 4xx that is not 408 or 429),
    -- which says the request is wrong rather than the receiver absent,
    -- and one that aged out of the retention window without landing.
    state TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    -- When the next attempt may run. The worker claims on this, so a
    -- backoff is a timestamp rather than a sleeping goroutine that a
    -- restart would lose. The ENDPOINT's backoff gates this as well:
    -- while a destination is failing only its oldest pending delivery
    -- is attempted, so a backlog does not become a burst.
    next_attempt_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delivered_at TIMESTAMP,
    last_status INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    abandoned_reason TEXT NOT NULL DEFAULT ''
);

-- The worker's claim: what is due, oldest first.
CREATE INDEX webhook_deliveries_due
    ON webhook_deliveries(next_attempt_at) WHERE state = 'pending';
-- The UI's per-endpoint history, and the cascade when one is deleted.
CREATE INDEX webhook_deliveries_endpoint
    ON webhook_deliveries(endpoint_id, created_at);
-- Counting one endpoint's backlog, which the evaluator does per pass.
CREATE INDEX webhook_deliveries_pending
    ON webhook_deliveries(endpoint_id) WHERE state = 'pending';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS webhook_deliveries_pending;
DROP INDEX IF EXISTS webhook_deliveries_endpoint;
DROP INDEX IF EXISTS webhook_deliveries_due;
DROP TABLE IF EXISTS webhook_deliveries;
DROP INDEX IF EXISTS webhook_subscriptions_live;
DROP INDEX IF EXISTS webhook_subscriptions_endpoint;
DROP INDEX IF EXISTS webhook_subscriptions_owner;
DROP TABLE IF EXISTS webhook_subscriptions;
DROP INDEX IF EXISTS webhook_endpoints_owner;
DROP TABLE IF EXISTS webhook_endpoints;

-- +goose StatementEnd
