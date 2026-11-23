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

package sshgateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// Revocation is what makes a week-long certificate a reasonable thing
// to hand out. These tests are about the gateway half of that: a button
// in the web UI that marks a row without stopping a login is worse than
// no button, because the person who pressed it believes the machine is
// cut off.

// fakeRevocations answers from a fixed set, and records what it was
// asked so a test can tell "allowed because not revoked" apart from
// "allowed because nothing was checked".
type fakeRevocations struct {
	revoked map[uint64]bool
	err     error
	asked   atomic.Int64
}

func (f *fakeRevocations) CertificateRevoked(_ context.Context, serial uint64) (bool, error) {
	f.asked.Add(1)
	if f.err != nil {
		return false, f.err
	}
	return f.revoked[serial], nil
}

// The headline: a certificate whose owner withdrew it does not log in,
// though its signature is good and it has not expired.
func TestRevokedCertificateIsRefused(t *testing.T) {
	f := newCertFixture(t)
	rc := &fakeRevocations{revoked: map[uint64]bool{f.cert.Serial: true}}
	addr := certGatewayWithRevocations(t, &fakeTransport{}, f.ca.PublicKey(), rc)

	if _, err := certDial(t, addr, "12345.0", f.auth(t)); err == nil {
		t.Fatal("a revoked certificate logged in")
	}
	if rc.asked.Load() == 0 {
		t.Error("the gateway never asked whether the certificate was revoked")
	}
}

// And the other half, without which the test above passes on a gateway
// that refuses everything.
func TestUnrevokedCertificateStillLogsIn(t *testing.T) {
	f := newCertFixture(t)
	rc := &fakeRevocations{revoked: map[uint64]bool{f.cert.Serial + 1: true}}
	addr := certGatewayWithRevocations(t, &fakeTransport{}, f.ca.PublicKey(), rc)

	client, err := certDial(t, addr, "12345.0", f.auth(t))
	if err != nil {
		t.Fatalf("an outstanding certificate was refused: %v", err)
	}
	_ = client.Close()
	if rc.asked.Load() == 0 {
		t.Error("the gateway never consulted the revocation list")
	}
}

// A revocation check that cannot answer refuses the login.
//
// The alternative -- treat an error as "not revoked" -- turns the whole
// mechanism into one an attacker only has to BREAK rather than beat:
// knock the database over and every withdrawn certificate works again.
// The store behind this is a local database in the same process, so an
// error here means the server is already unwell, not that a network is
// slow.
func TestRevocationCheckFailsClosed(t *testing.T) {
	f := newCertFixture(t)
	rc := &fakeRevocations{err: errors.New("database is on fire")}
	addr := certGatewayWithRevocations(t, &fakeTransport{}, f.ca.PublicKey(), rc)

	if _, err := certDial(t, addr, "12345.0", f.auth(t)); err == nil {
		t.Fatal("a login succeeded while the revocation check was failing")
	}
}

// A deployment with no checker is the gateway as it was: certificates
// stop working when they expire and not before. Worth pinning, because
// the field is optional and a nil one must not panic in the callback
// that every single login goes through.
func TestNoRevocationCheckerIsAllowed(t *testing.T) {
	f := newCertFixture(t)
	addr := certGatewayWithRevocations(t, &fakeTransport{}, f.ca.PublicKey(), nil)

	client, err := certDial(t, addr, "12345.0", f.auth(t))
	if err != nil {
		t.Fatalf("certificate login without a revocation checker: %v", err)
	}
	_ = client.Close()
}

// The revocation check must come AFTER the signature check, so a
// forged certificate never reaches the database at all. An attacker who
// can make this server perform a query per offered key has a cheap
// amplifier; one who cannot is merely refused.
func TestForgedCertificateIsNotLookedUp(t *testing.T) {
	f := newCertFixture(t)
	other := testSigner(t) // a CA this gateway does not trust
	rc := &fakeRevocations{}
	addr := certGatewayWithRevocations(t, &fakeTransport{}, other.PublicKey(), rc)

	if _, err := certDial(t, addr, "12345.0", f.auth(t)); err == nil {
		t.Fatal("a certificate from an untrusted CA logged in")
	}
	if n := rc.asked.Load(); n != 0 {
		t.Errorf("the revocation store was queried %d times for a certificate that never verified", n)
	}
}
