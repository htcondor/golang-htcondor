---
title: Interactive sessions
audience: user
order: 25
---

# Interactive sessions

A terminal, a JupyterLab notebook or a VS Code window, running on a
machine in the pool rather than on the access point. You start one from
**Interactive** in the web console, or through the API.

The thing to understand before anything else: **an interactive session
is a job.** It queues like a job, waits for a slot like a job, and is
charged like a job. Starting one is not like opening a terminal on a
login node — nothing happens the instant you ask, and what you get is a
share of a machine somebody else may also be waiting for.

That has three consequences worth knowing up front, and the rest of
this page is mostly those three.

## It waits, and then it is still not ready

A session goes through two waits, not one.

1. **Waiting for a slot.** The job is idle in the queue until the pool
   matches it to a machine. On a busy pool this is the long one.
2. **Starting.** The job is running, but the thing inside it — the
   notebook server, the editor server — has not finished starting and
   bound its socket yet. A few more seconds, sometimes more on a first
   run that has to pull a container image.

The API reflects this honestly, and the distinction costs something, so
the two reads differ:

```bash
# Lists your sessions. Does NOT check whether each is reachable --
# that would mean opening a connection into every job.
curl -s -H "$AUTH" "$AP/api/v1/apps"

# Reads one, and DOES probe. Reports "starting" until the server
# inside is actually listening.
curl -s -H "$AUTH" "$AP/api/v1/apps/{id}"
```

So a session the list calls `running` may not answer yet. If you are
polling for "can I open this", poll the single-session read, not the
list.

## It ends on a clock, not when you stop using it

Your site sets the limits; these are the defaults, and yours may be
shorter.

| What | Default | What it means for you |
| --- | --- | --- |
| Session lifetime | 8 hours | Wall clock from when the job starts running. The session goes away then, whatever you are doing in it. |
| Jupyter kernel idle | 1 hour | A kernel that has run nothing for this long is culled. |
| Waiting to start | 4 hours | A session that never gets a slot is given up on. |
| Reconnect grace | 5 minutes | If the connection drops, the session is held this long for it to come back. |

The lifetime ceiling is enforced by the scheduler, not by the browser,
so leaving the tab open does not extend it — and the idle cull measures
*kernel execution*, not HTTP traffic, so a notebook tab polling quietly
in the background does not count as busy. **Save your work as you go,
and treat the session as something that will end.**

If you need hours of compute, that is a batch job, not a session. A
session is for deciding what the batch job should be.

## Your site may not offer all three

Terminals, JupyterLab and VS Code are each switchable, and a site may
offer any subset. An application that is not offered simply has no
section on the **Interactive** page.

What a disabled application turns off is **starting a new session**.
Listing, reaching, and ending existing ones keep working — so if your
site switches something off while you have a session in it, you are not
locked out of your own work. Go and save it.

## Starting one

### Terminal

```bash
curl -s -X POST -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"cpus": 2, "memory_mb": 4096}' \
  "$AP/api/v1/interactive/terminal"
```

Everything in the body is optional. Alongside the resource fields you
can ask for GPUs (`gpus`, and the `gpus_minimum_*` constraints), and
pass extra submit commands as `submit_lines` — a `+ProjectName`, an
`environment` line. Submit commands that would redefine the session
itself, such as `executable` or `universe`, are rejected, and your
site's own policy is applied after yours.

Attach to it with the SSH gateway, if your site runs one:

```bash
ssh +<session-name>@ap.example.edu
```

Or through the API at `/api/v1/jobs/{id}/ssh`. The web console does the
latter for you and gives you a terminal in the page.

### JupyterLab

```bash
curl -s -X POST -H "$AUTH" "$AP/api/v1/jupyter/instances"
```

You reach it through the access point rather than connecting to the
execute machine directly: the job dials a tunnel back, and
`/api/v1/jupyter/instances/{id}/proxy/...` is the door. That is why a
notebook works from a machine that could never route to the execute
node.

There is a lifecycle event stream at
`/api/v1/jupyter/instances/{id}/events` if you want to know when it is
ready without polling.

### VS Code

```bash
curl -s -X POST -H "$AUTH" "$AP/api/v1/apps"
```

A container job running a VS Code server on a socket in its sandbox,
reached through the same job proxy. `POST` returns before the job has a
slot, so poll `GET /api/v1/apps/{id}` — the one that probes — until it
stops saying `starting`.

## Ending one

```bash
curl -s -X DELETE -H "$AUTH" "$AP/api/v1/apps/{id}"
```

That removes the job. Do it when you are finished rather than leaving
the session to time out: until it ends it is holding a slot somebody
else is queued for, and on a pool that charges, it is still charging.

## Where to go next

- [Submitting and watching a job](jobs.md) — for work that does not need you watching
- [Shell access over SSH](ssh.md) — a terminal inside a job you already submitted
- [Interactive sessions and apps](../operator/interactive.md) — the operator's side: which applications a site offers, and the limits it sets
