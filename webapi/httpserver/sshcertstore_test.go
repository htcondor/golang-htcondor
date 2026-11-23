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
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newCertStore(t *testing.T) *sshCertStore {
	t.Helper()
	return &sshCertStore{db: newTestDB(t, filepath.Join(t.TempDir(), "app.db"))}
}

// mustRecord writes one certificate. The lifetime is fixed at an hour:
// what these tests vary is WHEN it was issued, which is what decides
// whether it reads as outstanding, expired or prunable.
func (s *sshCertStore) mustRecord(t *testing.T, serial uint64, owner string, issued time.Time) {
	t.Helper()
	const ttl = time.Hour
	err := s.record(t.Context(), sshCertRecord{
		Serial:         certSerial(serial),
		KeyFingerprint: "SHA256:fake",
		KeyType:        "ssh-ed25519",
		Comment:        owner + "@laptop",
		IssuedAt:       issued,
		ValidBefore:    issued.Add(ttl),
	}, owner)
	if err != nil {
		t.Fatalf("record %d: %v", serial, err)
	}
}

// The round trip the whole feature rests on: issue, see it, withdraw
// it, and have the gateway's question answer differently afterwards.
func TestCertificateRecordRoundTrip(t *testing.T) {
	s := newCertStore(t)
	s.mustRecord(t, 42, "alice", time.Now().UTC())

	got, err := s.list(t.Context(), "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d certificates, want 1", len(got))
	}
	if got[0].Serial != 42 || got[0].RevokedAt != nil {
		t.Fatalf("listed %+v, want serial 42 and not revoked", got[0])
	}

	if revoked, err := s.revoked(t.Context(), 42); err != nil || revoked {
		t.Fatalf("revoked before revoking = %v, %v", revoked, err)
	}
	if ok, err := s.revoke(t.Context(), 42, "alice"); err != nil || !ok {
		t.Fatalf("revoke = %v, %v", ok, err)
	}
	if revoked, err := s.revoked(t.Context(), 42); err != nil || !revoked {
		t.Fatalf("revoked after revoking = %v, %v", revoked, err)
	}

	got, err = s.list(t.Context(), "alice")
	if err != nil || len(got) != 1 || got[0].RevokedAt == nil {
		t.Fatalf("after revoking, list = %+v, %v; the row should remain and be marked", got, err)
	}
}

// The scoping, which is the whole authorization model for this table.
//
// A serial travels in the clear inside every certificate, so an
// unscoped UPDATE would let anyone holding one cut off its owner.
func TestCertificateRevokeIsScopedToTheOwner(t *testing.T) {
	s := newCertStore(t)
	s.mustRecord(t, 7, "alice", time.Now().UTC())
	// Mallory has a certificate of their own, so the listing below is
	// narrowed to the right owner rather than merely empty.
	s.mustRecord(t, 8, "mallory", time.Now().UTC())

	if ok, err := s.revoke(t.Context(), 7, "mallory"); err != nil || ok {
		t.Fatalf("mallory revoked alice's certificate: %v, %v", ok, err)
	}
	if revoked, err := s.revoked(t.Context(), 7); err != nil || revoked {
		t.Fatalf("alice's certificate was revoked by someone else: %v, %v", revoked, err)
	}
	got, err := s.list(t.Context(), "mallory")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Serial != 8 {
		t.Fatalf("mallory sees %+v, want only their own certificate", got)
	}
}

// Revoking twice reports the second as a no-op, which is what lets the
// handler answer 404 for "already gone" instead of claiming it just did
// something.
func TestCertificateRevokeIsNotRepeatable(t *testing.T) {
	s := newCertStore(t)
	s.mustRecord(t, 9, "alice", time.Now().UTC())
	if ok, _ := s.revoke(t.Context(), 9, "alice"); !ok {
		t.Fatal("first revoke did nothing")
	}
	if ok, err := s.revoke(t.Context(), 9, "alice"); err != nil || ok {
		t.Fatalf("second revoke = %v, %v; want false", ok, err)
	}
}

// A serial with no row is not revoked.
//
// Certificates issued before this table existed have no row, and so do
// certificates from a different deployment sharing a CA. Refusing those
// is a defensible policy and not the one implemented: it would lock out
// every certificate in circulation the moment this shipped.
func TestUnknownSerialIsNotRevoked(t *testing.T) {
	s := newCertStore(t)
	if revoked, err := s.revoked(t.Context(), 123456); err != nil || revoked {
		t.Fatalf("an unrecorded serial read as revoked: %v, %v", revoked, err)
	}
}

// Serials are 63-bit values, and SQLite's INTEGER is SIGNED.
//
// A serial above 2^63 stored in that column comes back negative, and a
// lookup by the original unsigned value then finds nothing -- a Revoke
// button that silently fails on about half of all certificates. This
// pins both halves: sshCertSerial stays inside the range, and a value
// at the top of it round-trips.
func TestCertificateSerialsSurviveTheDatabase(t *testing.T) {
	for i := 0; i < 200; i++ {
		serial, err := sshCertSerial()
		if err != nil {
			t.Fatalf("sshCertSerial: %v", err)
		}
		if serial > math.MaxInt64 {
			t.Fatalf("serial %d exceeds what a signed INTEGER column holds", serial)
		}
	}

	s := newCertStore(t)
	const big = uint64(math.MaxInt64)
	s.mustRecord(t, big, "alice", time.Now().UTC())
	if ok, err := s.revoke(t.Context(), big, "alice"); err != nil || !ok {
		t.Fatalf("revoking the largest storable serial = %v, %v", ok, err)
	}
	if revoked, err := s.revoked(t.Context(), big); err != nil || !revoked {
		t.Fatalf("the largest storable serial did not round-trip: %v, %v", revoked, err)
	}
}

// A serial sent as a JSON NUMBER arrives in a browser rounded, because
// JavaScript carries 53 bits. The browser then posts the rounded value
// back to the revoke endpoint, where it matches nothing.
func TestCertificateSerialIsAStringOnTheWire(t *testing.T) {
	const serial = uint64(9007199254740993) // 2^53 + 1: the first casualty
	b, err := json.Marshal(sshCertRecord{Serial: certSerial(serial)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `"serial":"9007199254740993"`; !strings.Contains(string(b), want) {
		t.Fatalf("marshalled %s, want it to contain %s", b, want)
	}

	var back sshCertRevokeRequest
	if err := json.Unmarshal([]byte(`{"serial":"9007199254740993"}`), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if uint64(back.Serial) != serial {
		t.Fatalf("round-tripped to %d, want %d", back.Serial, serial)
	}
	// A bare number is still accepted, so a hand-written client is not
	// refused over the quoting.
	if err := json.Unmarshal([]byte(`{"serial":42}`), &back); err != nil || back.Serial != 42 {
		t.Fatalf("a numeric serial was refused: %v, %v", back.Serial, err)
	}
}

// Old rows are dropped; rows that still matter are not.
func TestCertificateRecordsArePruned(t *testing.T) {
	s := newCertStore(t)
	now := time.Now().UTC()
	s.mustRecord(t, 1, "alice", now.Add(-sshCertRetention-24*time.Hour))
	s.mustRecord(t, 2, "alice", now.Add(-time.Hour))

	if err := s.prune(t.Context()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	got, err := s.list(t.Context(), "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Serial != 2 {
		t.Fatalf("after pruning, list = %+v; want only serial 2", got)
	}
}

// An expired-but-recent certificate stays in the listing. The common
// reason to open it is to work out what a lost machine was holding, and
// one that expired yesterday is part of that answer.
func TestExpiredCertificatesStayListedUntilPruned(t *testing.T) {
	s := newCertStore(t)
	s.mustRecord(t, 3, "alice", time.Now().UTC().Add(-48*time.Hour))
	got, err := s.list(t.Context(), "alice")
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %+v, %v; an expired certificate should still be shown", got, err)
	}
}

// A handler with no database issues nothing rather than issuing
// something unrevocable. Pinned because the nil case is easy to make
// "work" by accident.
func TestCertificateStoreWithoutADatabaseRefuses(t *testing.T) {
	var s *sshCertStore
	if err := s.record(t.Context(), sshCertRecord{}, "alice"); err == nil {
		t.Error("record with no database succeeded")
	}
	if _, err := s.list(t.Context(), "alice"); err == nil {
		t.Error("list with no database succeeded")
	}
	if _, err := s.revoke(t.Context(), 1, "alice"); err == nil {
		t.Error("revoke with no database succeeded")
	}
	if _, err := s.revoked(t.Context(), 1); err == nil {
		t.Error("the revocation check with no database answered")
	}
}
