---
title: Working with an AI assistant
audience: user
order: 15
---

# Working with an AI assistant

This access point can be connected to an AI assistant — Claude, Copilot,
Cursor, Codex, or anything else that speaks the Model Context Protocol.
Once connected, the assistant can look at your jobs, submit new ones,
read logs and output, and work out why something is held or idle,
without you leaving the conversation.

## Connecting

Open `https://<your-access-point>/mcp` in a browser. You will not get an
error and you will not get anything an assistant could use: you get a
setup page with the address to copy and the exact steps for each client.
Those steps live on the server rather than in this guide so that they
stay current with the clients, which change their configuration formats
more often than anything here does.

Two things are worth knowing before you start.

**There is no token to paste.** You sign in with your usual account for
this access point, in a browser window the assistant opens for you.
Nothing is written into a configuration file that you then have to keep
secret.

**Claude on claude.ai connects from Anthropic's servers**, not from your
computer, so it can only reach an access point that is reachable from
the internet. Claude Code, VS Code, Cursor and Codex all connect from
wherever you are running them, so they work with an access point you can
reach over a VPN or inside a campus network.

## What it can do, and whose permissions it uses

The assistant acts **as you**. It can do what you can do and nothing
more: the same jobs, the same files, the same pool. It is not a service
account, and it does not gain you access to anything you did not already
have.

What it gets is roughly the set of things you would otherwise do by hand:

- look at the queue and at finished jobs, and read their output and logs
- submit jobs and DAGs, and upload the input files they need
- hold, release, edit and remove your own jobs
- run a command inside a running job, or start an interactive session
- ask why a job is idle, or what is wrong across all your jobs
- look at the pool: what machines exist, what is free, what would match

Your site decides which of these are offered. An access point may run
read-only, in which case the assistant can look at everything and change
nothing, and the tools that would change something are not offered
rather than refused. A site can also switch individual tools off. If
something described here is missing, that is a site decision and your
administrator can tell you about it.

## What to watch for

**The assistant acts quickly and at volume.** That is the point of it,
and it is also the thing to be careful about. A person submitting a
thousand jobs pauses somewhere around the point of typing the number. An
assistant does not: it will ask for what it estimates the work needs,
and several hundred CPU hours is not a figure it will stop and raise
with you. Nothing on this side reviews the size of a request — what
bounds it is your pool's own policy.

**Submitting a job is running code**, and it runs as you, on machines
your jobs reach. That is equally true when you submit by hand; what
changes is that the thing deciding what to run read it somewhere rather
than thinking of it. Treat "submit this" from an assistant the way you
would treat a script a colleague sent you.

**Your site may ask you to confirm bulk actions.** Some access points
are configured to stop and ask before an assistant submits or removes
more than a set number of jobs. If your client supports it you will see
the question; if it does not, the action proceeds and the server logs
that nobody was asked. Either way the number is your administrator's
choice, not a limit you can raise from the conversation.

**Check what it did, not just what it said.** The assistant reports its
own actions. `condor_q`, the web interface and the job's own log are the
record.

## When it goes wrong

**It cannot see your jobs.** You are signed in as somebody — perhaps not
the account that owns them. Ask the assistant who it is acting as; it
can tell you.

**A tool you expected is missing.** Either your site has switched it off
or the access point is read-only. Both are deliberate.

**A job it submitted is held.** The hold reason is the answer, and the
assistant can read it. Jobs held for a missing credential are the common
case on an access point that requires one — see your site's own
documentation for which credentials it expects.

**It keeps waiting and then gives up.** Watching jobs blocks in-call for
a bounded time and then returns. If your client gives up before the
server answers, the call returns nothing at all rather than a partial
answer — and the watch may have been registered all the same. Have the
assistant list your watches before it creates another one.

## The web interface instead

If your site has turned on the chat assistant in the web interface, you
can use it there without configuring anything: it is the same tools and
the same permissions, in a window that already knows who you are. The
difference is only where the conversation happens.

## Further reading

- [Shell access over SSH](ssh.md) — getting a terminal inside a running job.
- [Files and jobs over SFTP](files.md) — reading output and submitting without a client.
- [What your administrator sees](../operator/agents.md) — the same surface from the operating side, including what a site can switch off.
