-- +goose Up
-- +goose StatementBegin

-- Whether this connector offers a browser file chooser.
--
-- Its own setting rather than a consequence of the connector existing,
-- because the chooser is the only part that widens the page's
-- Content-Security-Policy. A site using a connector purely to name job
-- inputs -- ids coming from a site app, a script, or a catalogue --
-- never opens a chooser, and should not carry script-src and frame-src
-- entries for one.
--
-- Default 0: a policy is widened for a feature in use, never on the
-- chance that one might be. Off is also the discoverable failure --
-- an operator who wanted the chooser finds the setting -- where ON by
-- default is the invisible one, a wider policy nobody asked for and
-- nobody notices.
ALTER TABLE connectors ADD COLUMN picker BOOLEAN NOT NULL DEFAULT 0;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE connectors DROP COLUMN picker;

-- +goose StatementEnd
