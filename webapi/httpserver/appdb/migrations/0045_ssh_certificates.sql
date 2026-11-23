-- +goose Up
-- +goose StatementBegin

-- The SSH user certificates this access point has issued.
--
-- Until now it issued and forgot. That was defensible while the maximum
-- lifetime was twelve hours -- the comment in handlers_sshcert.go said
-- so outright: "its lifetime IS the control, and that is the reason not
-- to make it generous". The trouble is that the lifetime being the only
-- control is also what made it impossible to hand anyone a credential
-- for an unattended job, so the feature was unusable for exactly the
-- case that wants a certificate rather than a browser prompt.
--
-- This table is the other control, and the two change together: a
-- certificate can be withdrawn before it expires, so a week-long one is
-- no longer a week-long hole if a laptop is lost.
--
-- It is a record of ISSUANCE, not of the credential. There is nothing
-- secret in it: the serial, the account, and the fingerprint of a public
-- key. The certificate itself is public data that sits in ~/.ssh and is
-- handed out over HTTP, and the private half never reaches this server
-- at all. So, unlike ssh_gateway_keys next door, nothing here is sealed.
CREATE TABLE ssh_certificates (
    -- The certificate's serial, which is what the gateway has to match
    -- when it decides whether to admit one. Issued random rather than
    -- counted; see sshCertSerial, which now masks to 63 bits so the
    -- value survives SQLite's signed INTEGER without wrapping negative.
    serial INTEGER PRIMARY KEY,

    -- The principal the certificate names, which is also the only
    -- person who may list or revoke it. Every read and every write
    -- filters on this column: it is the whole of the authorization
    -- model for this table.
    owner TEXT NOT NULL,

    -- SHA256:... of the public key the certificate was issued over, and
    -- the key's type. Shown to the user because the serial identifies
    -- the certificate to the SERVER while the fingerprint is what
    -- `ssh-keygen -lf ~/.ssh/id_ed25519.pub` prints on their own
    -- machine -- which is how somebody with three laptops tells the
    -- rows apart.
    key_fingerprint TEXT NOT NULL,
    key_type TEXT NOT NULL,

    -- The trailing comment from the authorized_keys line they pasted,
    -- usually "you@laptop". Free text the user chose, truncated on the
    -- way in, and the most human label this row will ever have.
    comment TEXT NOT NULL DEFAULT '',

    issued_at TIMESTAMP NOT NULL,
    valid_before TIMESTAMP NOT NULL,

    -- When the user withdrew it. NULL means they have not. A revoked
    -- row is kept rather than deleted: the gateway has to know the
    -- serial is refused, and a deleted row reads as "never issued",
    -- which is the one answer that would let it back in.
    revoked_at TIMESTAMP
);

-- The user's own listing, newest first, and the pruning sweep.
CREATE INDEX ssh_certificates_owner ON ssh_certificates(owner, issued_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Dropping this table un-revokes every withdrawn certificate that has
-- not yet expired: the gateway refuses a serial because a row says so,
-- and no row means no refusal. Roll back only with that in mind.
DROP INDEX IF EXISTS ssh_certificates_owner;
DROP TABLE IF EXISTS ssh_certificates;

-- +goose StatementEnd
