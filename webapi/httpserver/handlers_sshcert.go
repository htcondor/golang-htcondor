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
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/sshgateway"
)

// Certificate lifetimes.
//
// Twelve hours is still the default: a working day, long enough not to
// interrupt somebody who signed in this morning.
//
// The CAP is a week, and it used to be a day. The old limit was not a
// guess about how long a credential should live -- it was the whole of
// the revocation story. There was no CRL and no OCSP, nothing recorded
// which certificates existed, so expiry was the only way one ever
// stopped working and the cap had to be short enough to serve as one.
//
// That made the feature unusable for the case it was built for. A job
// that runs unattended cannot go back to a browser every twelve hours,
// so anything automated was pushed onto a credential with no expiry at
// all, which is strictly worse than a long-lived certificate that can
// be withdrawn.
//
// ssh_certificates (migration 0045) is the revocation story now, which
// is what buys the longer cap: every certificate is recorded, the
// gateway refuses a serial its owner has withdrawn, and a week-long
// certificate on a lost laptop is cut off in the time it takes to open
// the Connectors page. Raising this without that table would be the
// change this comment exists to prevent.
const (
	sshCertDefaultLifetime = 12 * time.Hour
	sshCertMaxLifetime     = 7 * 24 * time.Hour
)

// sshCAResponse describes the certificate authority to a client.
type sshCAResponse struct {
	// PublicKey is the authorized_keys form.
	PublicKey string `json:"public_key"`
	// KnownHostsLine is what to add to known_hosts so this client
	// trusts the gateway's host key without pinning it by hand.
	KnownHostsLine string `json:"known_hosts_line"`
	// Fingerprint is for comparing against what an operator published.
	Fingerprint string `json:"fingerprint"`

	// GatewayHost and GatewayPort say where to ssh, when the operator
	// configured HTTP_API_SSH_GATEWAY_HOST.
	//
	// Published so a client needs no configuration of its own. This
	// server already knows the name -- it is the host certificate's
	// principal and the pattern in KnownHostsLine -- and until now kept
	// it to itself, which left every client asking a human where the
	// gateway is. Omitted rather than guessed when unset: the listen
	// address is not the answer, since a container on :2222 sits behind
	// a service publishing 22 somewhere else.
	GatewayHost string `json:"gateway_host,omitempty"`
	GatewayPort int    `json:"gateway_port,omitempty"`
}

type sshCertRequest struct {
	// PublicKey is an authorized_keys line: the half that stays on the
	// caller's machine is never sent here.
	PublicKey string `json:"public_key"`
	// LifetimeSeconds may ask for LESS than the default. More is
	// clamped rather than refused, because a client asking for a week
	// wants a certificate, not an error.
	LifetimeSeconds int `json:"lifetime_seconds,omitempty"`
}

type sshCertResponse struct {
	// Certificate is the line to save as <key>-cert.pub, which ssh
	// picks up beside the private key on its own.
	Certificate string    `json:"certificate"`
	Principal   string    `json:"principal"`
	ValidBefore time.Time `json:"valid_before"`
	Fingerprint string    `json:"fingerprint"`
	// Serial identifies this certificate to the revoke endpoint. A
	// string on the wire; see certSerial.
	Serial certSerial `json:"serial"`
}

// handleSSHCA publishes the gateway's certificate authority.
//
// Public information by nature -- it is the key that verifies
// signatures, not one that makes them -- but still behind
// authentication, because the only reason to want it is to use this
// gateway.
func (s *Handler) handleSSHCA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	// The certificate exists to authenticate to the SSH gateway, which
	// does not run where nothing may reach into a job.
	if !s.requireSSHToJob(w) {
		return
	}
	if _, _, err := s.requireAuthentication(r); err != nil {
		s.writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}

	ca := s.sshCASigner
	if ca == nil {
		s.writeError(w, http.StatusServiceUnavailable,
			"This access point does not issue SSH certificates")
		return
	}

	pub := ca.PublicKey()
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	gatewayHost, gatewayPort := sshgateway.ParseGatewayAddress(s.sshGatewayPublicHost)
	s.writeJSON(w, http.StatusOK, sshCAResponse{
		PublicKey: authorized,
		// Narrowed to the operator's configured names when there are
		// any, which confines what this CA is trusted to vouch for on
		// the client. A wildcard otherwise: narrowing on a guess would
		// silently stop working the first time somebody uses an alias.
		KnownHostsLine: sshgateway.KnownHostsLine(pub, sshgateway.ParseHostNames(s.sshGatewayPublicHost)),
		Fingerprint:    ssh.FingerprintSHA256(pub),
		GatewayHost:    gatewayHost,
		GatewayPort:    gatewayPort,
	})
}

// handleSSHCertificate signs a certificate for the caller.
//
// This is an identity-only endpoint: nothing downstream re-checks who
// asked, so the name it signs has to be one the schedd has vouched
// for. createAuthenticatedContext resolves it through the token cache
// or by asking the schedd, and leaves it EMPTY when it could not --
// which is why an empty name is refused here rather than treated as
// anonymous.
func (s *Handler) handleSSHCertificate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	// The certificate exists to authenticate to the SSH gateway, which
	// does not run where nothing may reach into a job.
	if !s.requireSSHToJob(w) {
		return
	}
	if s.refuseForPlacementdSession(w, r, "SSH certificates") {
		return
	}

	ctx, _, err := s.requireAuthentication(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	account := ownerFromActor(htcondor.GetAuthenticatedUserFromContext(ctx))
	if account == "" {
		// Fail closed. A certificate names an account; issuing one
		// without knowing the account is issuing it to anybody.
		s.writeError(w, http.StatusForbidden,
			"Your account could not be established, so no certificate can be issued")
		return
	}

	// A certificate buys sshGatewayScopes -- condor:/WRITE and a shell.
	// A caller holding less than that must not be able to trade up.
	//
	// The distinction that matters is scoped versus unscoped, not
	// present versus absent: an API key minted for HTTP-only scopes,
	// or a grant the user approved for condor:/READ, is a deliberate
	// restriction and issuing over it is an escalation. A browser
	// session carries no scopes at all because that route has no scope
	// model -- refusing those would refuse every human who came to
	// enroll.
	// `scoped` is false for a credential carrying NO scopes at all, so
	// the API-key marker is checked too. Without it a zero-scope key
	// slips past this gate entirely, arrives at the schedd with no
	// credential of its own, and GetSecurityConfigOrDefault falls back
	// to this daemon's configuration -- a queue superuser on a normal
	// access point. That is the same shape as the three gaps closed in
	// "close three authorization gaps on routes that reach HTCondor".
	if scopes, scoped := scopedCredential(ctx); scoped || AuthenticatedViaAPIKey(ctx) {
		if _, ok := scopes[sshCertRequiredScope]; !ok {
			s.logger.Info(logging.DestinationHTTP,
				"Refused an SSH certificate to an under-scoped credential",
				"account", account, "required", sshCertRequiredScope)
			s.writeError(w, http.StatusForbidden,
				"This credential is not authorized for "+sshCertRequiredScope+
					", which is what an SSH certificate grants")
			return
		}
	}

	ca := s.sshCASigner
	if ca == nil {
		s.writeError(w, http.StatusServiceUnavailable,
			"This access point does not issue SSH certificates")
		return
	}

	var req sshCertRequest
	setBodyLimit(w, r, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Could not read the request body")
		return
	}
	pub, keyComment, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(req.PublicKey)))
	if err != nil {
		s.writeError(w, http.StatusBadRequest,
			"public_key must be one line of authorized_keys, as in `ssh-ed25519 AAAA...`")
		return
	}
	if err := acceptableUserKey(pub); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, isCert := pub.(*ssh.Certificate); isCert {
		// Signing a certificate over a certificate produces something
		// nothing will accept, and the request means the caller sent
		// the wrong file.
		s.writeError(w, http.StatusBadRequest,
			"public_key is a certificate; send the public key it was issued for")
		return
	}

	// Clamped in SECONDS, before the multiply. time.Duration is
	// nanoseconds, so a large lifetime_seconds wraps -- and a wrapped
	// negative slips past a `> max` check entirely.
	lifetime := sshCertDefaultLifetime
	if req.LifetimeSeconds > 0 {
		secs := req.LifetimeSeconds
		if capSecs := int(sshCertMaxLifetime / time.Second); secs > capSecs {
			secs = capSecs
		}
		lifetime = time.Duration(secs) * time.Second
	}

	serial, err := sshCertSerial()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not issue a certificate")
		return
	}

	cert, err := sshgateway.SignUserCertificate(ca, pub, account, lifetime, serial)
	if err != nil {
		s.logger.Error(logging.DestinationHTTP, "Signing an SSH certificate failed",
			"account", account, "error", err)
		s.writeError(w, http.StatusInternalServerError, "Could not issue a certificate")
		return
	}

	validBefore := time.Unix(int64(cert.ValidBefore), 0).UTC() //nolint:gosec // set from a unix time moments ago
	record := sshCertRecord{
		Serial:         certSerial(serial),
		KeyFingerprint: ssh.FingerprintSHA256(pub),
		KeyType:        pub.Type(),
		Comment:        strings.TrimSpace(keyComment),
		IssuedAt:       time.Now().UTC(),
		ValidBefore:    validBefore,
	}

	// Recorded BEFORE the certificate is returned, and a failure here
	// fails the request.
	//
	// The alternative -- log it and hand the certificate over anyway --
	// produces a working credential that appears in no list and that the
	// Revoke button cannot reach, and nothing tells its holder that
	// theirs is the one that cannot be withdrawn. Losing a certificate
	// somebody has not got yet costs them a retry; the other way costs
	// them the only control they have over it.
	if err := s.sshCerts().record(r.Context(), record, sshCertOwner(account)); err != nil {
		s.logger.Error(logging.DestinationHTTP, "Recording an SSH certificate failed",
			"account", account, "serial", serial, "error", err)
		s.writeError(w, http.StatusInternalServerError,
			"The certificate could not be recorded, so none was issued. "+
				"It would not have been revocable.")
		return
	}
	// Best effort, and deliberately after the record: an old row left
	// behind costs nothing, and a failed tidy must not fail an issuance
	// that has already succeeded.
	if err := s.sshCerts().prune(r.Context()); err != nil {
		s.logger.Warn(logging.DestinationHTTP, "Pruning old SSH certificate records failed",
			"error", err)
	}

	s.logger.Info(logging.DestinationHTTP, "Issued an SSH certificate",
		"account", account, "serial", serial,
		"lifetime_seconds", int(lifetime.Seconds()),
		"key_fingerprint", record.KeyFingerprint)

	s.writeJSON(w, http.StatusOK, sshCertResponse{
		Certificate: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cert))),
		Principal:   account,
		ValidBefore: validBefore,
		Fingerprint: record.KeyFingerprint,
		Serial:      certSerial(serial),
	})
}

// sshCertOwner is the one spelling of an account that the
// ssh_certificates table uses.
//
// Issuance writes ownerFromActor(<authenticated actor>), which strips a
// realm; the listing and the revoke arrive with a session username,
// which may or may not carry one depending on how the session was
// established. Putting both through the same function is what keeps a
// certificate issued as "alice@EXAMPLE.ORG" visible to the browser
// session of "alice" -- the alternative is a list that is silently
// empty for exactly the deployments that use realms.
func sshCertOwner(name string) string { return ownerFromActor(name) }

// sshCerts is the issuance record. Never nil: a Handler with no database
// returns a store whose methods all answer errNoCertStore, which is the
// state a deployment running without one is in and not a crash.
func (s *Handler) sshCerts() *sshCertStore {
	return &sshCertStore{db: s.db}
}

// handleSSHCertificates serves GET /api/v1/ssh/certificates: what this
// account has been issued, and what it has withdrawn.
//
// Session-only, like the grants listing and for the same reason. A
// bearer token that can enumerate and revoke the credentials beside it
// is a stolen token that locks its owner out.
func (s *Handler) handleSSHCertificates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if !s.requireSSHToJob(w) {
		return
	}
	session, ok := s.integrationCaller(w, r)
	if !ok {
		return
	}
	certs, err := s.sshCerts().list(r.Context(), sshCertOwner(session.Username))
	if err != nil {
		s.logger.Error(logging.DestinationHTTP, "Listing SSH certificates failed",
			"account", session.Username, "error", err)
		s.writeError(w, http.StatusInternalServerError, "Could not list certificates")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"certificates": certs})
}

// sshCertRevokeRequest names the certificate to withdraw.
type sshCertRevokeRequest struct {
	Serial certSerial `json:"serial"`
}

// handleSSHCertificateRevoke serves POST /api/v1/ssh/certificates/revoke.
//
// Withdrawing is not deleting: the row stays, marked, because the
// gateway refuses a serial BECAUSE a row says to. A deleted row reads as
// "never issued", which is the one answer that would let the certificate
// back in.
func (s *Handler) handleSSHCertificateRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if !s.requireSSHToJob(w) {
		return
	}
	session, ok := s.integrationCaller(w, r)
	if !ok {
		return
	}
	var req sshCertRevokeRequest
	setBodyLimit(w, r, 1<<12)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "serial is required")
		return
	}

	revoked, err := s.sshCerts().revoke(r.Context(), uint64(req.Serial), sshCertOwner(session.Username))
	if err != nil {
		s.logger.Error(logging.DestinationHTTP, "Revoking an SSH certificate failed",
			"account", session.Username, "serial", uint64(req.Serial), "error", err)
		s.writeError(w, http.StatusInternalServerError, "Could not revoke the certificate")
		return
	}
	if !revoked {
		// One answer for "not yours", "no such serial" and "already
		// revoked". Telling them apart would let anyone probe which
		// serials exist, and the three are the same thing to the
		// caller: it is not holding access now.
		s.writeError(w, http.StatusNotFound,
			"No certificate of yours with that serial is outstanding")
		return
	}

	// Info, not Debug: somebody cutting off a credential is the event an
	// incident gets reconstructed from, and this is the only record that
	// it was the owner's own doing.
	s.logger.Info(logging.DestinationHTTP, "User revoked an SSH certificate",
		"account", session.Username, "serial", uint64(req.Serial))

	s.writeJSON(w, http.StatusOK, map[string]any{
		"serial":  req.Serial,
		"revoked": true,
	})
}

// sshCertRequiredScope is what a certificate ends up granting, and so
// what a caller must already hold to be given one.
const sshCertRequiredScope = "condor:/WRITE"

// acceptableUserKey refuses key types a certificate should not be
// issued over.
//
// A certificate is public and long-lived, so a weak key under one is a
// third-party-forgeable credential for that account rather than merely
// the holder's own problem. ssh-dss is 1024-bit by construction and
// deprecated; RSA below 3072 is too short to sign a credential that
// names an account.
func acceptableUserKey(pub ssh.PublicKey) error {
	switch pub.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoSKED25519,
		ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoSKECDSA256:
		return nil
	case ssh.KeyAlgoRSA:
		ck, ok := pub.(ssh.CryptoPublicKey)
		if !ok {
			return errors.New("public_key: this RSA key cannot be inspected")
		}
		rk, ok := ck.CryptoPublicKey().(*rsa.PublicKey)
		if !ok {
			return errors.New("public_key: this RSA key cannot be inspected")
		}
		if rk.N.BitLen() < minRSABits {
			return fmt.Errorf("public_key: RSA keys must be at least %d bits, this one is %d",
				minRSABits, rk.N.BitLen())
		}
		return nil
	default:
		return fmt.Errorf("public_key: %s keys are not accepted; use ed25519, ecdsa, or RSA of at least %d bits",
			pub.Type(), minRSABits)
	}
}

// minRSABits is the floor for an RSA key a certificate is issued over.
const minRSABits = 3072

// sshCertSerial returns a random serial.
//
// Random rather than counted: a counter needs durable state to stay
// unique across restarts, and a 63-bit random value collides about as
// often as never.
//
// Masked to 63 bits so the value round-trips through SQLite, whose
// INTEGER is signed. A serial above 2^63 stored in that column comes
// back NEGATIVE, and a revocation lookup by the original unsigned value
// would then find nothing -- a Revoke button that silently does not
// revoke, on about half of all certificates. The bit is cheaper than
// the bug.
func sshCertSerial() (uint64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b[:]) &^ (1 << 63), nil
}
