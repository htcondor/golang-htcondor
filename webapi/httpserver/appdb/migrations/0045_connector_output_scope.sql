-- +goose Up
-- +goose StatementBegin

-- How the sweep names its binding's jobs in a queue constraint.
--
-- The sweep asks the queue which of a cluster's jobs have finished, and
-- that question has to be confined to the binding's own owner. Without
-- the clause, a binding for cluster 12 matches whatever cluster 12 is
-- now -- and cluster ids are reused. Retiring a binding when its cluster
-- leaves the queue makes that unlikely rather than impossible, and the
-- failure it would produce is one user's job results appended to another
-- user's spreadsheet. A clause costs nothing and closes it.
--
-- Which ATTRIBUTE names the owner is not the same for everyone: a
-- placementd account's jobs are scoped by User (the whole account,
-- domain included) and everybody else's by Owner (the local part the
-- schedd stores). The deciding fact is a property of the account, and
-- the request that creates the binding already knows it -- the session
-- was scoped by it. Recording the answer is faithful to what the
-- submitting session was confined to; recomputing it at sweep time
-- would be a second implementation that can disagree with the first.
--
-- A boolean rather than the attribute name or a clause: the sweep
-- chooses between two constants, so nothing read back out of this
-- column is ever spliced into an expression.
ALTER TABLE connector_output_bindings
    ADD COLUMN user_scoped BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE connector_output_bindings DROP COLUMN user_scoped;

-- +goose StatementEnd
