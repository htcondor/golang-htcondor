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
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The user-facing half of SSH certificate revocation: /ssh/certificates
// and /ssh/certificates/revoke, which is what the Connectors page draws
// its table from.

type sshCertFixture struct{ *reauthFixture }

func newSSHCertFixture(t *testing.T) *sshCertFixture {
	t.Helper()
	f := newReauthFixture(t, Config{})
	t.Cleanup(func() { _ = f.server.oauth2Provider.Close() })
	return &sshCertFixture{f}
}

func (f *sshCertFixture) issue(t *testing.T, owner string, serial uint64) {
	t.Helper()
	now := time.Now().UTC()
	err := f.server.sshCerts().record(t.Context(), sshCertRecord{
		Serial:         certSerial(serial),
		KeyFingerprint: "SHA256:" + owner,
		KeyType:        "ssh-ed25519",
		Comment:        owner + "@laptop",
		IssuedAt:       now,
		ValidBefore:    now.Add(12 * time.Hour),
	}, owner)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
}

func (f *sshCertFixture) listAs(t *testing.T, username string) []sshCertRecord {
	t.Helper()
	rec := httptest.NewRecorder()
	f.server.handleSSHCertificates(rec, userRequest(t, f.server, username,
		[]string{"condor-users"}, http.MethodGet, "/api/v1/ssh/certificates", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list as %s: status %d: %s", username, rec.Code, rec.Body.String())
	}
	var out struct {
		Certificates []sshCertRecord `json:"certificates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	return out.Certificates
}

func (f *sshCertFixture) revokeAs(t *testing.T, username string, serial uint64) int {
	t.Helper()
	body := `{"serial":"` + strconv.FormatUint(serial, 10) + `"}`
	rec := httptest.NewRecorder()
	f.server.handleSSHCertificateRevoke(rec, userRequest(t, f.server, username,
		[]string{"condor-users"}, http.MethodPost, "/api/v1/ssh/certificates/revoke", body))
	return rec.Code
}

// What a user sees is their own certificates and nobody else's.
func TestSSHCertificateListingIsPerUser(t *testing.T) {
	f := newSSHCertFixture(t)
	f.issue(t, "alice", 11)
	f.issue(t, "alice", 12)
	f.issue(t, "bob", 13)

	alice := f.listAs(t, "alice")
	if len(alice) != 2 {
		t.Fatalf("alice sees %d certificates, want 2: %+v", len(alice), alice)
	}
	for _, c := range alice {
		if c.Serial == 13 {
			t.Fatal("alice can see bob's certificate")
		}
	}
	if bob := f.listAs(t, "bob"); len(bob) != 1 || bob[0].Serial != 13 {
		t.Fatalf("bob sees %+v, want only serial 13", bob)
	}
}

// Revoking marks the row and the gateway's own question changes answer.
//
// Asserted through sshCerts().revoked rather than through the listing,
// because that is the function the SSH gateway calls -- a handler that
// marked a row the gateway did not consult would pass a listing test
// and let the login through.
func TestSSHCertificateRevokeStopsTheGatewayAdmittingIt(t *testing.T) {
	f := newSSHCertFixture(t)
	f.issue(t, "alice", 21)

	if revoked, err := f.server.sshCerts().revoked(t.Context(), 21); err != nil || revoked {
		t.Fatalf("revoked before = %v, %v", revoked, err)
	}
	if code := f.revokeAs(t, "alice", 21); code != http.StatusOK {
		t.Fatalf("revoke: status %d", code)
	}
	if revoked, err := f.server.sshCerts().revoked(t.Context(), 21); err != nil || !revoked {
		t.Fatalf("after revoking, the gateway would still admit it: %v, %v", revoked, err)
	}
}

// A serial travels in the clear inside every certificate, so holding
// one must not be enough to cut off the person it was issued to.
func TestSSHCertificateRevokeRefusesSomebodyElsesSerial(t *testing.T) {
	f := newSSHCertFixture(t)
	f.issue(t, "alice", 31)

	if code := f.revokeAs(t, "mallory", 31); code != http.StatusNotFound {
		t.Fatalf("mallory revoking alice's certificate: status %d, want 404", code)
	}
	if revoked, err := f.server.sshCerts().revoked(t.Context(), 31); err != nil || revoked {
		t.Fatalf("alice's certificate was revoked by mallory: %v, %v", revoked, err)
	}
}

// Unknown, already-revoked and somebody-else's all answer the same, so
// the endpoint cannot be used to probe which serials exist.
func TestSSHCertificateRevokeIsOpaque(t *testing.T) {
	f := newSSHCertFixture(t)
	f.issue(t, "alice", 41)
	if code := f.revokeAs(t, "alice", 41); code != http.StatusOK {
		t.Fatalf("first revoke: status %d", code)
	}
	for name, serial := range map[string]uint64{
		"already revoked": 41,
		"never issued":    999,
	} {
		if code := f.revokeAs(t, "alice", serial); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, code)
		}
	}
}

// Both endpoints are session-only, like the grants listing. A bearer
// token that can enumerate and revoke the credentials beside it is a
// stolen token that locks its owner out.
func TestSSHCertificateEndpointsRequireASession(t *testing.T) {
	f := newSSHCertFixture(t)
	for name, call := range map[string]http.HandlerFunc{
		"list":   f.server.handleSSHCertificates,
		"revoke": f.server.handleSSHCertificateRevoke,
	} {
		rec := httptest.NewRecorder()
		method := http.MethodGet
		if name == "revoke" {
			method = http.MethodPost
		}
		req := httptest.NewRequestWithContext(t.Context(), method,
			"/api/v1/ssh/certificates", nil)
		call(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session: status %d, want 401", name, rec.Code)
		}
	}
}

// The serial the issuer hands back is the one the revoke endpoint
// takes. These are two handlers in different files and nothing but a
// test holds them to the same spelling.
func TestIssuedSerialRevokesTheCertificate(t *testing.T) {
	h := sshCertHandler(t, true)
	rec := postCert(t, h, "alice", `{"public_key":"`+userPublicKey(t)+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue: status %d: %s", rec.Code, rec.Body.String())
	}
	var resp sshCertResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Serial == 0 {
		t.Fatal("the issued certificate carries no serial, so nothing can revoke it")
	}
	if ok, err := h.sshCerts().revoke(t.Context(), uint64(resp.Serial), "alice"); err != nil || !ok {
		t.Fatalf("revoking the serial the issuer returned: %v, %v", ok, err)
	}
}

// A session username may carry a realm; the stored owner never does.
//
// Issuance writes ownerFromActor(<actor>), which strips one. If the
// listing did not strip the same way, "alice@EXAMPLE.ORG" would open
// this page, see nothing, and conclude they had no certificates out --
// on exactly the deployments that use realms, and nowhere else.
func TestSSHCertificateListingIgnoresTheRealm(t *testing.T) {
	f := newSSHCertFixture(t)
	f.issue(t, "alice", 51)

	got := f.listAs(t, "alice@EXAMPLE.ORG")
	if len(got) != 1 || got[0].Serial != 51 {
		t.Fatalf("a realm-qualified session sees %+v, want the certificate issued to alice", got)
	}
	if code := f.revokeAs(t, "alice@EXAMPLE.ORG", 51); code != http.StatusOK {
		t.Fatalf("revoking as a realm-qualified session: status %d", code)
	}
}

// A certificate that cannot be recorded is not issued.
//
// The alternative -- log it and hand it over anyway -- produces a
// working credential that appears in no list and that Revoke cannot
// reach, with nothing telling its holder that theirs is the one which
// cannot be withdrawn.
func TestCertificateIsNotIssuedWhenItCannotBeRecorded(t *testing.T) {
	h := sshCertHandler(t, true)
	// The database the record would go to, shut under the handler. A
	// closed handle is the cheapest honest stand-in for "the write
	// failed"; what matters is the response, not the cause.
	if err := h.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	rec := postCert(t, h, "alice", `{"public_key":"`+userPublicKey(t)+`"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "ssh-ed25519-cert") {
		t.Error("a certificate was handed out although it could not be recorded")
	}
}
