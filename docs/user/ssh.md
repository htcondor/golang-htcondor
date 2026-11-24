---
title: Shell access over SSH
audience: user
order: 20
---

# Shell access over SSH

This access point runs an SSH gateway: `ssh` to it, approve the login in
a browser, and you land in a shell inside one of your own running jobs.
You need no account on the access point itself.

Your site has to have switched it on and will tell you the address —
what that involves is [SSH gateway](../operator/ssh-gateway.md) in the
operator guide. Reading and writing files, and submitting work, is
[Files and jobs over SFTP](files.md).

## What the username selects

The username carries no identity — the OAuth2 grant does — so it names the
target instead:

| `ssh <this>@gateway` | reaches |
| --- | --- |
| `12345.0`, or `12345` | that job, proc 0 if omitted |
| `+work` | your interactive session called `work`, **started if you have none** |
| `+jobs` | the **file service**: your finished jobs' output over SFTP |
| anything else, including a bare `ssh gateway` | your **default** session |

The `+` is required to name a session, and a bare `ssh gateway` reaches the
same `default` session from every machine you own. That is the point: the
username is whatever your local machine calls you, and your laptop login has
nothing to do with your HTCondor session — reading it as one would give you a
different session from a laptop, a login node and a container.

`+` is used because a POSIX username cannot contain it, so no local login can
be mistaken for an explicit request. A word like `session-` would need an
escape hatch for the account actually called `session-manager`; this needs
none.

It also reaches a session whose name looks like a job id, which is otherwise
unreachable because job ids are tried first: `ssh +12345.0@gateway`.

`+jobs` is **reserved**: a session cannot be created with that name, so the
service can never shadow somebody's shell. The reservation is
case-insensitive, and it covers every spelling that would derive the same name
— `+-jobs` reaches the service rather than asking for a session that cannot
exist. A bare `ssh jobs@gateway` is unaffected and still means your default
session, because the prefix is what makes a request explicit.

While a newly created session waits in the queue, the terminal shows what it is
waiting for and how long it has been waiting. Ctrl-C stops waiting; it does
**not** remove the job, so reconnecting picks the session up once it starts.

## Certificates, for scripts and for not approving every connection

`BatchMode=yes` refuses keyboard-interactive outright, so a script cannot use
the device flow at all — and nobody wants a browser prompt per connection
either. A certificate is obtained once and then works until it expires, or
until its owner withdraws it.

```bash
# The CA, so your client trusts the gateway's host key.
curl -H "Authorization: Bearer $TOKEN" https://ap.example.edu/api/v1/ssh/ca

# A certificate for a key you already have. lifetime_seconds is
# optional, defaults to 12 hours, and is clamped to the 7-day cap
# rather than refused.
curl -X POST -H "Authorization: Bearer $TOKEN" \
     -d "{\"public_key\": \"$(cat ~/.ssh/id_ed25519.pub)\", \"lifetime_seconds\": 604800}" \
     https://ap.example.edu/api/v1/ssh/certificate
```

Save the `certificate` field as `~/.ssh/id_ed25519-cert.pub`, beside the private
key; `ssh` finds it on its own. Add the `known_hosts_line` to `~/.ssh/known_hosts`
and the gateway's host key verifies without pinning it by hand: the gateway
presents a host certificate signed by the same CA, so trusting the CA is enough.

Certificates last 12 hours by default and never more than 7 days.

**Every issued certificate is now recorded, and its owner can withdraw it.** The
record is held in the access point's application database (`ssh_certificates`),
and the gateway consults it on every certificate login, so a withdrawn one stops
working at once rather than at its expiry. Users see their own certificates — and
the Revoke button — on the web UI's **Connectors** page; the same listing is at
`GET /api/v1/ssh/certificates`, and withdrawal at
`POST /api/v1/ssh/certificates/revoke` with `{"serial": "..."}`. Both are
**session-only**: a bearer token that could enumerate and revoke the credentials
beside it is a stolen token that locks its owner out.

Where the certificate comes from, what it is valid for, and why it works
the way it does is [Certificates](../operator/ssh-gateway.md#certificates)
in the operator guide.

## Limits worth knowing

- **`scp` and `sftp` work**, and so does anything else that asks for a
  subsystem: the gateway forwards the request to the job, whose sshd serves its
  own `Subsystem` directive. This was documented as impossible, on the belief
  that the forced command in `condor_ssh_to_job_shell_setup` turns a subsystem
  request into `eval sftp`. It does not — a probe against a real job gets a
  genuine `SSH_FXP_VERSION` reply from a real `sftp-server`.
- **The device flow needs somewhere to prompt.** `BatchMode=yes` declines
  keyboard-interactive outright, and a client with no terminal has nowhere to
  show the code; both want a certificate instead. A remote terminal is not
  required — `ssh -T` is fine, as long as the *local* end can prompt.
- **Ten sessions per job.** The sshd HTCondor starts uses OpenSSH's default
  `MaxSessions`, and every terminal for one job shares it. Port and socket
  forwards do not count against it.
- Each `ssh` is its own device authorization, so each asks for approval.
- **Port forwards reach whatever the job can.** `ssh -L` and socket
  forwards are dialled from inside the job, as `condor_ssh_to_job -L`
  does, to any host and port or socket path the job could reach itself.
  They carry the user's own authority over their own job, not the
  server's.
