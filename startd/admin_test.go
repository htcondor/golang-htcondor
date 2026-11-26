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

package startd

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/PelicanPlatform/classad/classad"
	"github.com/htcondor/cedar/security"
)

// capability is a syntactically valid administrative capability, shaped
// the way DaemonCore mints one: "admin_<sinful>#<startup>#<seq>" then
// '#', the bracketed session info, and the hex key.
const capability = `admin_<127.0.0.1:9618>#1762500000#3#[Encryption="YES";Integrity="YES";CryptoMethods="AES";ValidCommands="1000,465"]abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789`

func adWith(attrs map[string]any) *classad.ClassAd {
	ad := classad.New()
	for k, v := range attrs {
		_ = ad.Set(k, v)
	}
	return ad
}

// TestNewAdminFromAdImportsTheCapability: the whole point of the
// capability is that it authorizes the command, so an ad that carries
// one must produce an Admin that uses it.
func TestNewAdminFromAdImportsTheCapability(t *testing.T) {
	ad := adWith(map[string]any{
		"MyAddress":         "<127.0.0.1:9618>",
		AdminCapabilityAttr: capability,
	})
	a, err := NewAdminFromAd(ad, true, nil)
	if err != nil {
		t.Fatalf("NewAdminFromAd: %v", err)
	}
	if !a.HasCapability() {
		t.Errorf("Admin did not import the capability")
	}
	if a.Addr() != "<127.0.0.1:9618>" {
		t.Errorf("Addr() = %q, want the ad's MyAddress", a.Addr())
	}
}

// TestNewAdminFromAdWithoutCapability: an ad with no capability is the
// ordinary case on a pool that has remote administration off, or on a
// query that was not authorized to receive private attributes. Which of
// those it was is not visible from here, so the caller decides whether
// falling back to its own credentials is acceptable.
func TestNewAdminFromAdWithoutCapability(t *testing.T) {
	ad := adWith(map[string]any{"MyAddress": "<127.0.0.1:9618>"})

	if _, err := NewAdminFromAd(ad, true, nil); !errors.Is(err, ErrNoAdminCapability) {
		t.Errorf("required capability: err = %v, want ErrNoAdminCapability", err)
	}

	a, err := NewAdminFromAd(ad, false, nil)
	if err != nil {
		t.Fatalf("optional capability: %v", err)
	}
	if a.HasCapability() {
		t.Errorf("Admin claims a capability it was never given")
	}
}

// TestNewAdminFromAdRejectsAnAdWithNoAddress: without MyAddress there is
// nothing to connect to, and the projection that forgets to ask for it
// is the likely cause. Failing is how that gets noticed.
func TestNewAdminFromAdRejectsAnAdWithNoAddress(t *testing.T) {
	ad := adWith(map[string]any{AdminCapabilityAttr: capability})
	if _, err := NewAdminFromAd(ad, false, nil); err == nil {
		t.Errorf("an ad with no MyAddress was accepted")
	}
}

// TestNewAdminFromAdRejectsAnUnusableCapability: a capability that is
// present but will not import must be an error rather than a quiet
// fallback to this process's own credentials. On a pool whose
// authorization lives in the collector, that fallback would refuse
// every command with nothing to say why.
func TestNewAdminFromAdRejectsAnUnusableCapability(t *testing.T) {
	ad := adWith(map[string]any{
		"MyAddress":         "<127.0.0.1:9618>",
		AdminCapabilityAttr: "not-a-capability",
	})
	_, err := NewAdminFromAd(ad, false, nil)
	if err == nil {
		t.Fatalf("a malformed capability was accepted")
	}
	if errors.Is(err, ErrNoAdminCapability) {
		t.Errorf("a malformed capability was reported as an absent one: %v", err)
	}
}

// TestCAResult maps the daemon's Result attribute onto errors. The
// authorization cases are singled out because an access point that may
// no longer administer its execute points goes on planning correctly
// and changing nothing -- the failure worth surfacing.
func TestCAResult(t *testing.T) {
	cases := []struct {
		name    string
		reply   *classad.ClassAd
		wantErr bool
		denied  bool
	}{
		{
			name:  "success",
			reply: adWith(map[string]any{"Result": "Success"}),
		},
		{
			name:    "failure with a reason",
			reply:   adWith(map[string]any{"Result": "Failure", "ErrorString": "no such slot"}),
			wantErr: true,
		},
		{
			name:    "not authorized",
			reply:   adWith(map[string]any{"Result": "NotAuthorized", "ErrorString": "denied"}),
			wantErr: true,
			denied:  true,
		},
		{
			name:    "not authenticated",
			reply:   adWith(map[string]any{"Result": "NotAuthenticated"}),
			wantErr: true,
			denied:  true,
		},
		{
			// A reply with no Result is not something to read
			// optimistically: it means the peer is not what we think.
			name:    "no Result attribute",
			reply:   adWith(map[string]any{"Version": "26.2.0"}),
			wantErr: true,
		},
		{
			name:    "no reply at all",
			reply:   nil,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := caResult(tc.reply, VerbUpdateMachineAd)
			if (err != nil) != tc.wantErr {
				t.Fatalf("caResult() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil {
				return
			}
			var ca *CAError
			if errors.As(err, &ca) {
				if ca.Denied() != tc.denied {
					t.Errorf("Denied() = %v, want %v", ca.Denied(), tc.denied)
				}
			} else if tc.denied {
				t.Errorf("want an authorization error, got %T: %v", err, err)
			}
		})
	}
}

// TestCopyAdLeavesTheCallerAlone: sendCA adds three protocol attributes
// to the ad it sends. Adding them to the caller's ad would mean a
// controller that reuses one update ad per tick accumulates them, and
// the startd merges Command into every slot ad it publishes.
func TestCopyAdLeavesTheCallerAlone(t *testing.T) {
	src := adWith(map[string]any{"ReservationDeadline": int64(1762500000)})
	before := strings.Join(src.GetAttributes(), ",")

	cp := copyAd(src)
	_ = cp.Set("Command", VerbUpdateMachineAd)

	if after := strings.Join(src.GetAttributes(), ","); after != before {
		t.Errorf("copyAd's source gained attributes: %q -> %q", before, after)
	}
	if v, ok := cp.EvaluateAttrInt("ReservationDeadline"); !ok || v != 1762500000 {
		t.Errorf("the copy lost the attribute it was made from")
	}
}

// TestVacateClaimRejectsAnEmptySlotName: the startd reads one string and
// looks up a resource by it. An empty name finds nothing and is logged
// and dropped, so it would read as success here.
func TestVacateClaimRejectsAnEmptySlotName(t *testing.T) {
	a := NewAdmin("<127.0.0.1:9618>", nil)
	if err := a.VacateClaim(t.Context(), "   ", false); err == nil {
		t.Errorf("an empty slot name was accepted")
	}
}

// TestCommandNumbers pins the wire integers against HTCondor's
// condor_commands.h. The CA one is the trap: CA_UPDATE_MACHINE_AD is
// 1009, but 1009 never goes on the wire -- it is the name of a verb
// carried inside the request ad, and the command sent is CA_AUTH_CMD.
func TestCommandNumbers(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"CA_AUTH_CMD", CAAuthCmd, 1000},
		{"CA_CMD", CACmd, 1200},
		{"VACATE_CLAIM", cmdVacateClaim, 465},
		{"VACATE_CLAIM_FAST", cmdVacateClaimFast, 475},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestConnectRefusesWithNoCapabilityAndNoSecurityConfig: an Admin with
// neither a capability session nor a SecurityConfig builder must fail
// before it dials, and must not reach for this process's own credentials
// on the caller's behalf.
//
// cedar performs no handshake at all when its Security is nil, and the
// handshake is what carries the command integer -- the real command rides
// inside the DC_AUTHENTICATE ad. So a nil config produces not a weakly
// authenticated command but no command: the peer reads the request ad's
// leading expression count as a command number and refuses, which reads
// to an operator like an authorization failure and sends them to
// ALLOW_ADMINISTRATOR for a handshake that never happened.
//
// Checked by watching the socket: a dial that happens at all is the
// failure, whatever the call returns.
func TestConnectRefusesWithNoCapabilityAndNoSecurityConfig(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // test listener
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	dialed := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		dialed <- struct{}{}
		_ = c.Close()
	}()

	addr := "<" + ln.Addr().String() + ">"
	for _, tc := range []struct {
		name string
		call func(a *Admin) error
	}{
		{"UpdateMachineAd", func(a *Admin) error {
			return a.UpdateMachineAd(context.Background(), adWith(map[string]any{"Foo": "bar"}))
		}},
		{"VacateClaim", func(a *Admin) error {
			return a.VacateClaim(context.Background(), "slot1@host", false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := NewAdminFromAd(adWith(map[string]any{"MyAddress": addr}), false, nil)
			if err != nil {
				t.Fatalf("NewAdminFromAd: %v", err)
			}
			err = tc.call(a)
			if err == nil {
				t.Fatal("a command with no credentials and no command number was sent anyway")
			}
			if !strings.Contains(err.Error(), "SecurityConfig") {
				t.Errorf("err = %v, want one naming the missing SecurityConfig", err)
			}
			select {
			case <-dialed:
				t.Error("the execute point was dialed before the missing credential was noticed")
			default:
			}
		})
	}
}

// TestConnectUsesAnInjectedSecurityConfig: the injection point is what
// makes the no-capability mode work, so it has to be consulted. The
// builder here refuses, which is enough to show the call reached it --
// and a caller's builder refusing (as the daemon-origin check does for a
// context acting for a user) must fail the command, not fall through to
// an unauthenticated one.
func TestConnectUsesAnInjectedSecurityConfig(t *testing.T) {
	sentinel := errors.New("builder refused")
	calls := 0
	a, err := NewAdminFromAd(adWith(map[string]any{"MyAddress": "<127.0.0.1:9618>"}), false,
		&AdminOptions{SecurityConfig: func(context.Context, int, string) (*security.SecurityConfig, error) {
			calls++
			return nil, sentinel
		}})
	if err != nil {
		t.Fatalf("NewAdminFromAd: %v", err)
	}

	err = a.UpdateMachineAd(context.Background(), adWith(map[string]any{"Foo": "bar"}))
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the builder's own error", err)
	}
	if calls != 1 {
		t.Errorf("builder called %d times, want 1", calls)
	}
}
