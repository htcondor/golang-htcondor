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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/htcondor/golang-htcondor/webapi/jobssh"
)

// serviceServer is a gateway whose only job is the service path.
//
// Transport is nil and Resolve fails the test: a service is served by
// the gateway process, so anything reaching into the pool means the
// service branch was not taken.
func serviceServer(t *testing.T, hook ServiceSubsystem) *Server {
	t.Helper()
	return &Server{
		Resolve: func(context.Context, string, Target, func(string)) (jobssh.Key, error) {
			t.Error("a service target reached the job resolver")
			return jobssh.Key{}, errors.New("should not be called")
		},
		Subsystem: hook,
	}
}

// openSubsystem opens a raw session channel and asks for a subsystem.
//
// Deliberately not ssh.Session: RequestSubsystem does not mark the
// session started, so Session.Wait returns "session not started" and the
// Stderr field is never copied. A raw channel is also what an sftp
// client does, and it lets a test see the exit status.
func openSubsystem(t *testing.T, c *ssh.Client, name string) (ssh.Channel, <-chan *ssh.Request) {
	t.Helper()
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("OpenChannel: %v", err)
	}
	ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{name}))
	if err != nil {
		t.Fatalf("subsystem request: %v", err)
	}
	if !ok {
		t.Fatal("the subsystem request was refused")
	}
	return ch, reqs
}

// exitStatus reads the channel's requests until it sees one.
func exitStatus(t *testing.T, reqs <-chan *ssh.Request) (uint32, bool) {
	t.Helper()
	for req := range reqs {
		if req.Type != "exit-status" {
			continue
		}
		var p struct{ Status uint32 }
		if err := ssh.Unmarshal(req.Payload, &p); err != nil {
			t.Fatalf("exit-status payload: %v", err)
		}
		return p.Status, true
	}
	return 0, false
}

// TestServiceSubsystemGetsTheChannel is the whole point: a client that
// asks for a subsystem on "+jobs" is handed to the hook, with the
// authenticated account rather than the username -- which here names the
// service.
func TestServiceSubsystemGetsTheChannel(t *testing.T) {
	type got struct {
		account, service, subsystem string
	}
	seen := make(chan got, 1)

	srv := serviceServer(t, func(_ context.Context, req ServiceRequest) error {
		seen <- got{req.Account, req.Service, req.Subsystem}
		// Echo one line, so the test knows the stream is really the
		// channel and not a stub.
		line, err := bufio.NewReader(req.Stream).ReadString('\n')
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(req.Stream, "echo: %s", line)
		return err
	})

	c := gatewayClient(t, gatewayWithServer(t, srv), "+jobs")
	ch, reqs := openSubsystem(t, c, "sftp")
	defer ch.Close()

	if _, err := io.WriteString(ch, "hello\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	g := <-seen
	if g.account != "bbockelm" {
		t.Errorf("hook saw account %q, want the authenticated one", g.account)
	}
	if g.service != "jobs" {
		t.Errorf("hook saw service %q, want %q", g.service, "jobs")
	}
	if g.subsystem != "sftp" {
		t.Errorf("hook saw subsystem %q, want %q", g.subsystem, "sftp")
	}

	reply, err := bufio.NewReader(ch).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if reply != "echo: hello\n" {
		t.Errorf("read %q, want %q", reply, "echo: hello\n")
	}
	if status, ok := exitStatus(t, reqs); !ok || status != 0 {
		t.Errorf("exit status = %d (sent=%v), want 0", status, ok)
	}
}

// TestServiceWithoutAHookIsRejected: an access point that offers no
// service refuses the channel outright, which is decidable without
// asking anything.
func TestServiceWithoutAHookIsRejected(t *testing.T) {
	srv := &Server{Resolve: func(context.Context, string, Target, func(string)) (jobssh.Key, error) {
		t.Error("a service target reached the job resolver")
		return jobssh.Key{}, errors.New("should not be called")
	}}

	c := gatewayClient(t, gatewayWithServer(t, srv), "+jobs")
	_, err := c.NewSession()
	if err == nil {
		t.Fatal("a session opened on a service this gateway does not serve")
	}
	if !strings.Contains(err.Error(), "+jobs") {
		t.Errorf("rejection does not name the service: %v", err)
	}
}

// TestServiceRefusesAShellWithAnExplanation: somebody who types `ssh
// +jobs@gateway` has misunderstood what they connected to, and saying so
// is more use than a bare failure -- which most clients print as
// "administratively prohibited".
func TestServiceRefusesAShellWithAnExplanation(t *testing.T) {
	for _, how := range []string{"shell", "exec"} {
		t.Run(how, func(t *testing.T) {
			srv := serviceServer(t, func(context.Context, ServiceRequest) error {
				t.Error("a shell request reached the subsystem hook")
				return nil
			})
			c := gatewayClient(t, gatewayWithServer(t, srv), "+jobs")
			sess, err := c.NewSession()
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			defer sess.Close()

			var stderr strings.Builder
			sess.Stderr = &stderr

			if how == "shell" {
				err = sess.Shell()
			} else {
				err = sess.Start("id")
			}
			if err != nil {
				t.Fatalf("%s: %v", how, err)
			}
			// A non-zero exit, as an exited command would give.
			if err := sess.Wait(); err == nil {
				t.Error("a shell on a file service exited zero")
			}
			msg := stderr.String()
			for _, want := range []string{"file service", "sftp"} {
				if !strings.Contains(msg, want) {
					t.Errorf("stderr %q does not mention %q", msg, want)
				}
			}
		})
	}
}

// TestServiceHookErrorEndsTheChannel: a failure inside the service has
// to reach the user and set a non-zero status, or an sftp client reports
// a mysterious clean disconnect.
func TestServiceHookErrorEndsTheChannel(t *testing.T) {
	srv := serviceServer(t, func(context.Context, ServiceRequest) error {
		return errors.New("the store is on fire")
	})

	c := gatewayClient(t, gatewayWithServer(t, srv), "+jobs")
	ch, reqs := openSubsystem(t, c, "sftp")
	defer ch.Close()

	msg, err := io.ReadAll(ch.Stderr())
	if err != nil {
		t.Fatalf("reading stderr: %v", err)
	}
	if !strings.Contains(string(msg), "the store is on fire") {
		t.Errorf("stderr %q does not carry the reason", msg)
	}
	status, ok := exitStatus(t, reqs)
	if !ok {
		t.Fatal("no exit status was sent; an sftp client reports that as a clean disconnect")
	}
	if status == 0 {
		t.Error("a failed service exited zero")
	}
}

// TestServiceTolerateHarmlessRequests: an ordinary client sends pty-req
// and env before anything else, and refusing them makes it warn about
// something that does not matter on a channel that will never run a
// terminal.
func TestServiceToleratesHarmlessRequests(t *testing.T) {
	srv := serviceServer(t, func(_ context.Context, req ServiceRequest) error {
		_, err := io.WriteString(req.Stream, "served\n")
		return err
	})

	c := gatewayClient(t, gatewayWithServer(t, srv), "+jobs")
	sess, err := c.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// An ordinary client sends these before anything else; both must be
	// accepted or it warns about something harmless.
	if err := sess.Setenv("LANG", "C"); err != nil {
		t.Errorf("Setenv: %v", err)
	}
	if err := sess.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Errorf("RequestPty: %v", err)
	}
	_ = sess.Close()

	// And the subsystem still works on a fresh channel.
	ch, _ := openSubsystem(t, c, "sftp")
	defer ch.Close()
	line, err := bufio.NewReader(ch).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if line != "served\n" {
		t.Errorf("read %q, want %q", line, "served\n")
	}
}

// TestNonServiceTargetsStillResolve guards the branch itself: adding the
// service path must not have diverted a job or a session target.
func TestNonServiceTargetsStillResolve(t *testing.T) {
	resolved := make(chan Target, 1)
	srv := &Server{
		Transport: &fakeTransport{},
		Resolve: func(_ context.Context, _ string, target Target, _ func(string)) (jobssh.Key, error) {
			resolved <- target
			return jobssh.Key{Cluster: 5, Proc: 0}, nil
		},
		Subsystem: func(context.Context, ServiceRequest) error {
			t.Error("a job target reached the subsystem hook")
			return nil
		},
	}
	addr := gatewayWithServer(t, srv)

	for _, user := range []string{"5.0", "+work", "bbockelm"} {
		c := gatewayClient(t, addr, user)
		sess, err := c.NewSession()
		if err != nil {
			t.Fatalf("NewSession(%s): %v", user, err)
		}
		_ = sess.Shell()
		select {
		case target := <-resolved:
			if target.IsService() {
				t.Errorf("%q resolved as a service", user)
			}
		case <-context.Background().Done():
		}
		_ = sess.Close()
		_ = c.Close()
	}
}
