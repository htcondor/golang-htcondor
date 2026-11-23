// Copyright 2026 Morgridge Institute for Research
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// sshCertStore is the record of which SSH certificates this access point
// has issued, and which of them the owner has since withdrawn.
//
// It exists so a certificate can be revoked. Everything else it carries
// -- the fingerprint, the key comment, the issue time -- is there so the
// person deciding whether to revoke one can tell their laptop's
// certificate from the one they minted for a cron job eight months ago.
//
// See migrations/0045_ssh_certificates.sql for why none of this is
// sealed: a certificate is public by construction.
type sshCertStore struct {
	db *sql.DB
}

// certSerial is a certificate serial on the wire.
//
// A STRING in JSON, and that is not decoration. The serial is 63 bits of
// randomness; JavaScript's only number type carries 53, so a serial sent
// as a JSON number arrives in the browser rounded -- and the browser
// sends the rounded value back to the revoke endpoint, where it matches
// nothing. The Revoke button would then fail for most certificates and
// work for a few, which is the worst available failure mode.
//
// Unmarshalling accepts a number too, so a hand-written client or an
// older test is not refused over the quoting.
type certSerial uint64

func (c certSerial) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatUint(uint64(c), 10) + `"`), nil
}

func (c *certSerial) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" {
		return errors.New("a certificate serial is required")
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return fmt.Errorf("%q is not a certificate serial", s)
	}
	*c = certSerial(v)
	return nil
}

// sshCertRecord is one issued certificate as the owner sees it.
type sshCertRecord struct {
	Serial         certSerial `json:"serial"`
	KeyFingerprint string     `json:"key_fingerprint"`
	KeyType        string     `json:"key_type"`
	Comment        string     `json:"comment,omitempty"`
	IssuedAt       time.Time  `json:"issued_at"`
	ValidBefore    time.Time  `json:"valid_before"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

// sshCertCommentMax bounds the free text taken off a pasted public key.
//
// The comment is whatever was at the end of the authorized_keys line, so
// it is attacker-chosen in the sense that the account's own owner chose
// it. Bounded because an unbounded string lands in a table cell and in a
// log line, not because it is dangerous.
const sshCertCommentMax = 128

// sshCertRetention is how long a row outlives the certificate.
//
// Kept for a while after expiry so "what did I issue, and when?" has an
// answer past the end of the last certificate's life -- the question
// somebody asks after losing a machine. Past that the row answers
// nothing the gateway or the user needs: an expired certificate is
// refused on its own dates, whether or not a row exists.
const sshCertRetention = 90 * 24 * time.Hour

// errNoCertStore is returned when the server has no database to record
// issuance in. It is a configuration state, not a failure of the
// request, and the caller decides what to do about it.
var errNoCertStore = errors.New("httpserver: no database for SSH certificate records")

// record writes one issued certificate.
//
// Called after the certificate is signed and before it is returned. A
// failure here must fail the REQUEST rather than be logged past: a
// certificate that was handed out but not recorded is one that cannot be
// revoked, and the user would have no way to know that theirs is the one
// the Revoke button does not reach.
func (s *sshCertStore) record(ctx context.Context, rec sshCertRecord, owner string) error {
	if s == nil || s.db == nil {
		return errNoCertStore
	}
	if len(rec.Comment) > sshCertCommentMax {
		rec.Comment = rec.Comment[:sshCertCommentMax]
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ssh_certificates
		   (serial, owner, key_fingerprint, key_type, comment, issued_at, valid_before)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		//nolint:gosec // masked to 63 bits by sshCertSerial; see that function.
		int64(rec.Serial), owner, rec.KeyFingerprint, rec.KeyType, rec.Comment,
		rec.IssuedAt.UTC(), rec.ValidBefore.UTC())
	return err
}

// list returns one owner's certificates, newest first.
//
// Expired rows are included while they are within the retention window.
// They are not noise: the common reason to open this list is to work out
// what a machine was holding, and a certificate that expired yesterday
// is part of that answer.
func (s *sshCertStore) list(ctx context.Context, owner string) ([]sshCertRecord, error) {
	if s == nil || s.db == nil {
		return nil, errNoCertStore
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT serial, key_fingerprint, key_type, comment, issued_at, valid_before, revoked_at
		   FROM ssh_certificates
		  WHERE owner = ? AND issued_at > ?
		  ORDER BY issued_at DESC`,
		owner, time.Now().UTC().Add(-sshCertRetention))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []sshCertRecord{}
	for rows.Next() {
		var (
			serial  int64
			rec     sshCertRecord
			revoked sql.NullTime
		)
		if err := rows.Scan(&serial, &rec.KeyFingerprint, &rec.KeyType, &rec.Comment,
			&rec.IssuedAt, &rec.ValidBefore, &revoked); err != nil {
			return nil, err
		}
		rec.Serial = certSerial(serial) //nolint:gosec // written from a 63-bit value
		if revoked.Valid {
			when := revoked.Time.UTC()
			rec.RevokedAt = &when
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// revoke withdraws one certificate on its owner's say-so.
//
// Scoped to the owner inside the UPDATE rather than checked beforehand,
// for the reason handleGrantRevoke gives: a check followed by an
// unscoped write can be raced, and an unscoped write here would let
// anyone with a serial -- which travels in the clear inside every
// certificate -- cut off somebody else's access.
//
// Already-revoked rows are not touched again, so the reported count
// distinguishes "this revoked something" from "this was already gone".
func (s *sshCertStore) revoke(ctx context.Context, serial uint64, owner string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errNoCertStore
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE ssh_certificates SET revoked_at = ?
		  WHERE serial = ? AND owner = ? AND revoked_at IS NULL`,
		time.Now().UTC(), int64(serial), owner) //nolint:gosec // 63-bit serial
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// revoked reports whether this serial has been withdrawn.
//
// The gateway's question, asked once per certificate presented. A serial
// with NO row is not revoked: certificates issued before this table
// existed have none, and neither does one from a different deployment
// sharing a CA. Refusing those would be a defensible policy and is not
// the one implemented, because it would lock out every certificate in
// circulation at upgrade time.
func (s *sshCertStore) revoked(ctx context.Context, serial uint64) (bool, error) {
	if s == nil || s.db == nil {
		return false, errNoCertStore
	}
	var revokedAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT revoked_at FROM ssh_certificates WHERE serial = ?`,
		int64(serial)). //nolint:gosec // 63-bit serial
		Scan(&revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return revokedAt.Valid, nil
}

// prune drops rows past the retention window.
//
// Run on issuance rather than on a timer: the table only grows when a
// certificate is issued, so that is the moment it is worth tidying, and
// a sweep goroutine would be more machinery than a table this size
// deserves. A failure is not worth failing the request over -- the
// certificate is already signed and the only cost is an old row.
func (s *sshCertStore) prune(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errNoCertStore
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM ssh_certificates WHERE issued_at <= ?`,
		time.Now().UTC().Add(-sshCertRetention))
	return err
}
