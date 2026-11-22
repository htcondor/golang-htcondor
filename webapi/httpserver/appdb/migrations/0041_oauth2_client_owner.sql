-- +goose Up
-- +goose StatementBegin

-- Who a client belongs to, for the clients a user creates themselves.
--
-- Until now every client was the deployment's: seeded at startup, or
-- registered dynamically by an app that reached /mcp/oauth2/register
-- (RFC 7591) and belonged to nobody in particular. Neither needs an
-- owner, because neither is managed by a person through a UI -- the
-- admin pages act on all of them and the registration endpoint acts on
-- none.
--
-- A user creating their own integration changes that. The client is
-- theirs to list, show a secret for once, and delete, and nobody
-- else's: without a column saying whose it is, "my integrations" is a
-- query that cannot be written and the delete endpoint has no answer to
-- "may this caller remove this client".
--
-- This is an authorization column, not a label. It is the only thing
-- confining a user's integration list and the per-client delete to its
-- owner, so a write that fails must fail the creation (see
-- createOAuth2Client) rather than leave a client nobody can reach.
--
-- Empty means the deployment's own: every row written before this, plus
-- everything seeded or dynamically registered after it. An empty owner
-- is never a match -- the user-facing endpoints refuse an unnamed
-- caller before they query -- so an operator's client cannot be claimed
-- by a user who happens to have no name.
ALTER TABLE oauth2_clients ADD COLUMN owner TEXT NOT NULL DEFAULT '';

-- The listing is always "this one person's clients", which is the only
-- query this column exists to serve. Partial, because the rows with no
-- owner are the majority and are never selected by it.
CREATE INDEX oauth2_clients_owner ON oauth2_clients(owner) WHERE owner != '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS oauth2_clients_owner;
ALTER TABLE oauth2_clients DROP COLUMN owner;

-- +goose StatementEnd
