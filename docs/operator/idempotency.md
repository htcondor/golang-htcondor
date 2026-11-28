---
title: Idempotent submission
audience: operator
order: 77
---

# Idempotent submission

A client POSTs a submission, the connection drops before the response
arrives, and the client cannot tell whether the jobs were queued.
Retrying may run the work twice; not retrying may never run it at all.
An **idempotency key** removes the choice. Part of the
[HTTP API + MCP server](index.md) documentation.

## What a client does

The client picks a key, sends it, and reuses it verbatim on every retry
of that submission:

```bash
curl -H 'Idempotency-Key: nightly-2026-11-22' \
     -H "Authorization: Bearer $TOKEN" \
     -d @submit.json https://ap.example.edu/api/v1/jobs
```

A second attempt with the same key is refused:

```json
409 Conflict
{"error": "idempotency key \"nightly-2026-11-22\" is already in use by job 4312.0, which is still in the queue",
 "idempotency_key": "nightly-2026-11-22",
 "job_id": "4312.0"}
```

**A 409 is not a failure.** It means the work is queued, which is what
asking for idempotency was for. The client reads `job_id` instead of
submitting again.

Three statuses, three different things to do, which is why they are not
collapsed:

| | |
| --- | --- |
| **409** | already queued — the retry succeeded in the sense the client cares about |
| **400** | the request is wrong; retrying it will not help |
| **503** | the check could not be made; the same request may work later |

The key is stored on the job as `IdempotencyKey`, so it is visible in
`condor_q -af IdempotencyKey` and queryable like any other attribute.

It applies to every surface that submits through this server: `POST
/api/v1/jobs` via the header, and MCP `submit_job` via an
`idempotency_key` argument — offered only where the check is enabled.

### What a key may contain

Up to 1024 bytes of UTF-8, no control characters, and **no backslash or
dollar sign**. Those two are refused rather than escaped, and the reason
is worth knowing because it looks arbitrary:

The key lands in a submit-file value, which is macro-expanded before it
is read. A `$` would be a macro reference — `$(Cluster)` in a key
becomes the cluster number, and **`$ENV(NAME)` would read this server's
environment into the job ad**. The documented `$(DOLLAR)` escape covers
one dollar sign but not two adjacent ones, and a backslash has no
unambiguous spelling in that layer at all. A client that needs either
can hex or base64 its key.

A submit file that sets `IdempotencyKey` itself is refused too: the key
has to be one the server was given, or there is nothing to check it
against.

An `Idempotency-Key` header that is empty, or sent more than once with
different values, is **refused rather than submitted unguarded** — a
client that asked for the guarantee should not silently not get it.

## What a site chooses

```
HTTP_API_IDEMPOTENCY_KEY_CHECK = AUTO
```

| Mode | Checks |
| --- | --- |
| `FULL` | the live queue **and** the completed-job history |
| `QUEUE` | only the live queue, so a key whose job left the queue longer ago than the retention reads as unused |
| `AUTO` | *(the default)* `FULL` where an htcondordb mirror is configured, `QUEUE` where none is |
| `NONE` | enforces nothing |

`OFF` and `FALSE` read as `NONE`, `TRUE` as `FULL`; anything else is
reported and `AUTO` applies. The startup log names the mode **in
force**, resolved rather than as configured, which is the line to read
when `AUTO` is set and you want to know which way it went.

**`NONE` refuses a request that sends the header**, with 400, rather
than accepting it unguarded. A client that asked for this guarantee and
silently did not get it is worse off than one told the access point
does not offer it.

**`FULL` fails closed.** If the history cannot be read, a keyed
submission is refused with 503 rather than accepted — a check that
passes because it could not run is not a check. Unkeyed submissions are
never affected.

### The other three settings

| Setting | Default | |
| --- | --- | --- |
| `HTTP_API_IDEMPOTENCY_KEY_RETENTION` | 3600 | seconds a key this server submitted stays claimed in memory |
| `HTTP_API_IDEMPOTENCY_KEY_TIMEOUT` | 10 | seconds the two lookups get between them |
| `HTTP_API_IDEMPOTENCY_KEY_HISTORY_WINDOW` | unset | seconds of history to search; unset reads all of it |

The **retention** covers a real gap. The live queue is read from the
schedd rather than the mirror — the mirror lags by seconds and a retry
arrives within seconds, so a mirrored read would be least trustworthy
exactly when it is asked — while the history is read from the mirror,
where `IdempotencyKey` is indexed so the lookup does not scan. Between
the schedd writing a job's history record and the mirror ingesting it,
the key is in neither place. Retention wants to be longer than that
gap.

The **timeout** bounds how long a keyed submission waits, since it
waits on both lookups.

The **history window** trades the guarantee for the cost of the read: a
key reused after the window reads as unused. Unset reads the whole
history, which is what the index is there for, so set it only where
that read is measurably too expensive.

All four are **restart-only**, and deliberately. The claims live in
this server's memory, and rebuilding the guard on reconfigure would
drop every key claimed in the retention window — turning a
`condor_reconfig` into a window in which a retry is accepted twice.

## What bounds the guarantee

Worth being explicit about, because an operator may be promising
something to users.

**Claims are in memory.** They do not survive a restart and are not
shared between replicas behind a load balancer. The queue and history
checks still apply in both cases, so what is exposed is the ingest gap
above — not the whole history.

**Multi-AP mode narrows the queue half.** It reads only the access
point a submission was routed to, so a retry that assignment sends
elsewhere relies on the claim and the history alone.

**Keys are scoped to the account.** One account cannot use up another's
keys, and a retry is by definition the same credential retrying.

A deployment that needs the guarantee to hold across replicas or across
access points wants the check in the schedd rather than here.

## Where to go next

- [API surface](../developer/api.md) — the submission endpoints this applies to
- [Configuration reference](config.md) — the full text of each setting
