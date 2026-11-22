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
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// ServiceRequest is one subsystem request on a service target's channel.
type ServiceRequest struct {
	// Account is the authenticated caller, from the connection's
	// Permissions. Never conn.User(), which here names the service.
	Account string
	// Service is what the username asked for, lower case: "jobs".
	Service string
	// Subsystem is the subsystem name the client sent, e.g. "sftp".
	Subsystem string
	// Stream is the channel. The handler reads the client's bytes from
	// it and writes its own back; closing it is the gateway's business,
	// so a handler that returns leaves it open for the exit status.
	Stream io.ReadWriteCloser
}

// ServiceSubsystem serves one subsystem request on a service target.
//
// Returning an error ends the channel with a non-zero exit status and
// the error on stderr.
type ServiceSubsystem func(ctx context.Context, req ServiceRequest) error

// handleServiceSession serves a channel whose username named one of the
// gateway's own services rather than a job.
//
// Nothing is dialed into the pool here, which is the whole difference: a
// service is served by this process, so the channel is answered from
// here and there is no transport, no job and no sshd.
func (s *Server) handleServiceSession(ctx context.Context, account string, t Target, nch ssh.NewChannel) {
	if s.Subsystem == nil {
		// Decidable without asking anything, so it gets a proper
		// rejection rather than a message on an accepted channel.
		_ = nch.Reject(ssh.Prohibited,
			fmt.Sprintf("+%s is not served by this access point", t.Service))
		return
	}

	ch, reqs, err := nch.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for req := range reqs {
		switch req.Type {
		case "subsystem":
			var p struct{ Name string }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				replyTo(req, false)
				continue
			}
			replyTo(req, true)
			s.runService(ctx, ch, ServiceRequest{
				Account:   account,
				Service:   t.Service,
				Subsystem: p.Name,
				Stream:    channelStream{ch},
			})
			return

		case "shell", "exec":
			// A client that asks for a shell here has misunderstood
			// what it connected to, and saying so is more use than a
			// bare failure. The request is accepted so the message
			// arrives on the channel rather than as a protocol error
			// the client prints as "administratively prohibited".
			replyTo(req, true)
			s.failSession(ch, fmt.Sprintf(
				"+%s is a file service, not a shell. Connect with sftp or scp: "+
					"sftp -P <port> +%s@<gateway>", t.Service, t.Service), false)
			return

		case "pty-req", "env", "window-change", "signal":
			// Harmless on a channel that will never run a terminal, and
			// refusing makes ordinary clients print a warning about
			// something that does not matter.
			replyTo(req, true)

		default:
			replyTo(req, false)
		}
	}
}

// runService hands the channel to the subsystem handler and ends the
// channel the way an exited command would.
func (s *Server) runService(ctx context.Context, ch ssh.Channel, req ServiceRequest) {
	err := s.Subsystem(ctx, req)
	if err != nil {
		s.logf("A gateway service ended with an error",
			"account", req.Account, "service", req.Service,
			"subsystem", req.Subsystem, "error", err)
		s.failSession(ch, fmt.Sprintf("+%s: %v", req.Service, err), ctx.Err() != nil)
		return
	}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	_ = ch.CloseWrite()
}

// channelStream is a channel as a plain stream.
//
// Close shuts only the write half, because the channel itself is closed
// by handleServiceSession once the exit status has gone out. A handler
// that closed the whole channel would take the status with it, and the
// client would report the transfer as failed.
type channelStream struct{ ch ssh.Channel }

func (c channelStream) Read(p []byte) (int, error)  { return c.ch.Read(p) }
func (c channelStream) Write(p []byte) (int, error) { return c.ch.Write(p) }
func (c channelStream) Close() error                { return c.ch.CloseWrite() }
