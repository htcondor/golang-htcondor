-- +goose Up
-- +goose StatementBegin

-- Undo 0039: a chooser is not a property of a connector.
--
-- 0039 added connectors.picker as a boolean, and that was the wrong
-- shape. Google's Picker needs a developer key and an app id, which
-- belong to a Google Cloud project rather than to a credential, and a
-- boolean on the connector had nowhere to put them. A chooser is a
-- PROVIDER's, it borrows a connector's credential, and two connectors
-- for the same provider share one chooser and one set of
-- Content-Security-Policy origins.
--
-- Choosers are configured in their own right now (HTTP_API_PICKERS),
-- so this column has no reader. Dropped rather than left in place: a
-- column nothing writes is a question for whoever next reads the
-- schema.
--
-- A second migration rather than an edit to 0039, because 0039 may
-- already have been applied, and a changed migration leaves a database
-- that no longer matches the file that built it.
ALTER TABLE connectors DROP COLUMN picker;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE connectors ADD COLUMN picker BOOLEAN NOT NULL DEFAULT 0;

-- +goose StatementEnd
