-- +goose Up
-- +goose StatementBegin

-- Where a submission's results go: the spreadsheet a user nominated at
-- submit time, and which of its jobs have already been added to it.
--
-- Two tables, because the authorization and the progress have different
-- lifetimes. The binding is written once, by a request with a browser
-- session behind it; the progress is written repeatedly by a background
-- worker that has no session at all. Keeping them apart is what makes the
-- second one's authority checkable: the worker may only write where a
-- binding says, and it cannot create a binding.

-- One submission's output destination.
--
-- This row IS the authorization. A connector credential is otherwise
-- reachable only from the browser session that owns it -- by design, because
-- a server-side worker acting on a user's Drive without them present is a
-- different kind of power from a page doing it while they watch. At job
-- completion there is no session, so the worker uses the owner's stored
-- refresh token; what keeps that from being a general capability is that it
-- may act only on a (file_id, tab) some session already nominated, for a
-- cluster that session submitted.
CREATE TABLE connector_output_bindings (
    -- Our user, as the session names them -- the same choice
    -- connector_credentials makes, so a lookup here and a lookup there
    -- agree about who this is.
    subject      TEXT NOT NULL,
    -- Which access point, in multi-AP mode. Empty is the home one. Part of
    -- the key because cluster ids are only unique per schedd, and merging
    -- two access points' cluster 12 would append one submission's results
    -- under another's binding.
    schedd       TEXT NOT NULL DEFAULT '',
    cluster      INTEGER NOT NULL,

    -- The connector whose credential writes the rows. Stored rather than
    -- inferred, because a user may have connected more than one account and
    -- the one that holds this spreadsheet is the one that picked it.
    connector    TEXT NOT NULL,
    -- The spreadsheet and the tab. Together these are the whole of what the
    -- worker is permitted to touch.
    file_id      TEXT NOT NULL,
    tab          TEXT NOT NULL,
    -- The file in each job's sandbox that holds its result. One name for
    -- the whole cluster: every job of a submission runs the same program.
    job_file     TEXT NOT NULL,

    created_at   TIMESTAMP NOT NULL,
    -- Set when there is nothing left to append -- every job accounted for,
    -- or the cluster has left the queue. A retired binding authorizes
    -- nothing, which is what stops a cluster id reused by a later
    -- submission from inheriting this one's destination.
    retired_at   TIMESTAMP,
    PRIMARY KEY (subject, schedd, cluster)
);

-- The worker's sweep reads open bindings and nothing else, so that is the
-- index. Partial, because a deployment's history is mostly retired rows and
-- the open ones are the working set.
CREATE INDEX idx_connector_output_bindings_open
    ON connector_output_bindings (retired_at)
    WHERE retired_at IS NULL;

-- Which jobs have been added already.
--
-- The row is what makes appending at-most-once per job. values.append has no
-- idempotency key of its own -- it inserts wherever the table currently ends
-- -- so a worker that retried without this would add a job's results twice,
-- and the duplicate is indistinguishable from a real second record.
--
-- Written AFTER the append succeeds. The other order would lose a job's
-- results on a crash between the two, and a missing row is recoverable by
-- hand where a duplicate is not: nobody can tell which of two identical rows
-- was the spurious one.
CREATE TABLE connector_output_appended (
    subject      TEXT NOT NULL,
    schedd       TEXT NOT NULL DEFAULT '',
    cluster      INTEGER NOT NULL,
    proc         INTEGER NOT NULL,

    appended_at  TIMESTAMP NOT NULL,
    -- How many rows this job contributed. Zero is a real answer: a job may
    -- legitimately produce no records, and that is not the same as not
    -- having been looked at.
    row_count    INTEGER NOT NULL DEFAULT 0,
    -- Why this job contributed nothing, when it did not. A job whose result
    -- file was missing or unreadable is accounted for rather than retried
    -- for ever, and the reason is what the person gets told.
    note         TEXT NOT NULL DEFAULT '',

    PRIMARY KEY (subject, schedd, cluster, proc)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE connector_output_appended;
DROP INDEX IF EXISTS idx_connector_output_bindings_open;
DROP TABLE connector_output_bindings;

-- +goose StatementEnd
