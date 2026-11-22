# Outbound webhooks

A user can ask this access point to POST to a URL when their jobs change
state — into a Zap, a Slack channel, a ticketing system, or anything else
that accepts a webhook. They set a **destination** up once on the
**Connectors** page (or through `/api/v1/webhooks`), then add as many
**subscriptions** to it as they like (`/api/v1/webhook-subscriptions`) without
configuring the receiver again.

This is the outbound half of an integration. The inbound half is an OAuth2
client, which lets an outside service act *as* the user here; see
[Authentication and authorization](server-auth.md). The two are opposite
directions and the UI labels them as such, because revoking the wrong one is
the easy mistake.

## For administrators

Nothing has to be configured. The feature is on by default and needs no
credential, no outbound proxy setting and no schema beyond the application
database.

| Setting | Default | Meaning |
| --- | --- | --- |
| `HTTP_API_WEBHOOKS` | `true` | Whether webhooks are offered at all. With `false` the endpoints answer 503 and neither the evaluator nor the delivery worker runs. |
| `HTTP_API_WEBHOOK_FANOUT_LIMIT` | `500` | The most jobs one subscription may deliver for in a single pass. Past it it sends one summary a day and withholds the per-job deliveries; see [Fan-out](#fan-out). |
| `HTTP_API_KEK_FILE` | unset | Not webhook-specific, but it is what encrypts the stored destinations. Without it they are stored in the clear, as every other secret on such a deployment is. |

What an operator should know about it:

- **A destination is restricted to a routable public `https` address**, and
  that is checked when the connection is made rather than when the URL is
  saved. A name that resolves publicly at save time and to `127.0.0.1` a
  moment later is the DNS-rebinding hole, and checking at dial time closes
  it. Loopback, RFC 1918, link-local (including the cloud instance-metadata
  address), carrier-grade NAT and multicast are all refused. Redirects are
  not followed.
- **A user can only ever subscribe to their own jobs.** The evaluator reads
  one owner's queue at a time and the owner column on the row is what scopes
  it; there is no expression a user supplies that reaches a backend query.
- **Destinations are treated as secrets**, because for most providers
  possession of the URL is the authorization to post to it. No endpoint
  returns one. The admin pages do not show them either. What the UI shows is
  the host and the length of the rest.
- **Egress.** This is the one feature that makes the daemon open connections
  to arbitrary (public) hosts a user named. On an access point with no egress,
  set `HTTP_API_WEBHOOKS = false` rather than leaving users a page whose every
  delivery times out.
- **Cost.** One sweep per 30 seconds reads each owner's jobs once however many
  subscriptions they hold — the same read the job-watch evaluator does, through
  the same source — and the change stream makes a delivery prompt rather than
  waiting for the sweep. Deliveries are sent by one worker with a bounded
  pool, at most one in flight per destination.

  One thing makes that read more expensive: a destination set to send **whole
  job ads** forces its owner's sweep to read *every* attribute instead of the
  twenty-odd the evaluator needs. The cost falls on that owner's passes only,
  and only while such a destination exists.
- **A failing receiver is never switched off.** It backs off to a fifteen-minute
  ceiling and keeps trying, and while it is failing only its oldest queued
  delivery is attempted — so a receiver that has been down for a day costs it
  four requests an hour, not one per queued event. Deliveries are dropped only
  by age (14 days) or when one destination's queue reaches 1000 pending, which
  is recorded on the destination rather than passing silently. Nothing here
  needs an operator.
- **Retention.** Settled deliveries are kept 14 days for the user's own "is
  this working" panel, then swept hourly with the rest of the housekeeping.

## For users

A notification has two halves, and they are set up separately.

A **destination** is where deliveries go: an `https` URL, plus a signing
secret this server mints for it. You set one up once per receiver.

A **subscription** says what to tell it about: one job, one batch, or all of
your jobs, and which events. You can add as many as you like to one
destination, and adding one asks for no URL and no secret.

They are separate because the secret is shown **once**. When one webhook meant
one destination *and* one subscription, watching a second job meant pasting
the capability URL again and copying a second secret for the same receiver —
which is the most error-prone moment the feature has, repeated for no reason.
It also means a receiver that is down is down for everything pointed at it,
which is the truth, rather than for whichever hook happened to notice.

### Destinations

**Where to post** must be an `https` URL. It is stored encrypted where the
site has a key configured, and it is never shown back to you — if you lose it,
replace it. Replacing it does **not** change the signing secret: the receiver
is the same receiver, and re-keying it would mean reconfiguring everything
that feeds it because one thing moved.

**How much to send** is the destination's choice, so one receiver always sees
one shape:

| Mode | Body |
| --- | --- |
| `summary` (default) | A small fixed projection: job id, owner, batch, status, exit code, hold reason, command. |
| `full` | The same projection, **plus** `job_ad`: the job's entire ClassAd. |

`full` is opt-in because it is a disclosure decision. A job ad carries hundreds
of attributes — arguments, file paths, environment names, anything you set as a
custom attribute — and it is sent to a third party. Left automatic, every new
attribute the schedd learns would become a new disclosure decided by nobody.
The summary stays where it is when you turn it on, so a receiver written
against it keeps working. An ad over 128 KB is left out with `job_ad_omitted`
saying why, rather than truncated: half an ad is not a smaller ad.

You may hold **10 destinations**.

### Subscriptions

**What it watches** is one job, one batch, or all of your jobs. A job or batch
subscription ends when its subject does — there is nothing left to report —
and an "all my jobs" subscription lasts 90 days and is renewed whenever you
edit it. You may hold **25**.

**Which events**, from a fixed list:

| Event | Fires when |
| --- | --- |
| `done` | The job left the queue, whatever the outcome. |
| `succeeded` | It finished with exit code 0 and was not killed. |
| `failed` | It exited non-zero, was killed by a signal, or was removed. |
| `held` | It went on hold. This is the state that needs somebody to act. |
| `running` | It started executing. |

These are the same events the job-watch tools use, deliberately. Note what is
*not* on the list: there is no way to say "JobStatus == 4". That is the obvious
way to ask for "finished" and it would never fire, because a completed job is
deleted from the queue by the schedd rather than left in that state to be
noticed.

Two things to expect:

- **A new subscription fires for state that already exists.** Add one for
  `held` and every job of yours already on hold is reported at once. For a job
  or a batch that is usually what you wanted; for "all my jobs" on an account
  with a lot of held work it is a burst, bounded by the fan-out ceiling.
- **Two subscriptions to one destination do not double up.** A delivery is
  identified by (destination, event, job), so if "all my jobs" and "this job"
  both match, the receiver is told once.

Deleting a **subscription** leaves the destination and its secret alone.
Deleting a **destination** takes every subscription feeding it, and everything
queued for it, with it.

### Testing one

The **Test** button sends a real delivery to a **destination**: the same
signature, the same headers, the same connection policy, with `"test": true`
in the body. It reports what your receiver answered, including the first part
of its error message. Nothing about it is simulated, which is the point — a
test that bypassed the delivery path would tell you the wrong thing.

It names one fixed event (`running`) and describes a job consistent with it,
because a destination may have several subscriptions or none yet, and picking
one of them to speak for would make the test's shape depend on which. A
destination set to send whole ads gets a (synthetic) ad in its test, so the
thing that was turned on is the thing that is exercised.

A test neither sets nor clears a backoff. It says what your receiver answered
just now; whether the real queue may flow again is decided by a real delivery.

### When delivery fails

Your receiver is never switched off for failing. It **backs off**: the wait
after each failure grows from 30 seconds, doubling, to a ceiling of **fifteen
minutes**, and then stays there for as long as it takes. One success clears it
completely.

While a destination is failing, only its **oldest** queued delivery is
attempted. That is what makes "keep trying forever" affordable in both
directions: a receiver that is still down gets one request per interval
instead of one per queued event, and one that has just come back is not handed
its entire backlog in the same instant.

Two things still end a single delivery early:

- A **permanent refusal** — any 4xx except 408 and 429 — is not retried at
  all. A 404 or a 401 is a statement about the request, and the request will
  be identical next time; retrying it would only delay telling you. The
  destination still backs off, because a URL answering 404 is a
  misconfiguration and without it every queued delivery for it would be
  refused in one burst.
- A **redirect** counts as a misconfiguration. A webhook destination has to be
  a final URL.

And two bounds keep a destination that never comes back from queueing without
limit:

- A delivery that has been pending for **14 days** is abandoned unsent. Past
  that a report is not late but wrong: it describes a state that moved on days
  ago.
- A destination with **1000 deliveries** already queued stops accepting new
  ones, and says so on the page. Those events are gone; fix the receiver or
  narrow what you send there.

An earlier version disabled a webhook after five give-ups. It no longer does:
the failure this feature actually meets is a receiver that is down for a while
and comes back, and a disabled hook comes back only if somebody happens to
revisit a page they had no reason to look at. Silence that repairs itself beats
silence that needs an administrator.

### Fan-out

A subscription posts once per job. An "all my jobs" subscription over a cluster
of ten thousand would post ten thousand times, which is a denial of service
against your own receiver, so there is a ceiling (500 jobs by default).

One that goes over it sends a single summary delivery saying how many jobs
reached the event, and withholds the per-job ones. It is **not** switched off:
it keeps being evaluated, says so again at most once a day while it stays over
the ceiling, and resumes per-job deliveries on its own if the selection drops
back under it. Narrow it to a job or a batch if you want the detail.

## For whoever writes the receiver

A delivery is a `POST` with a JSON body and these headers:

| Header | Contents |
| --- | --- |
| `Idempotency-Key` | Stable across every retry of this delivery, and unique per (webhook, event, job). |
| `X-HTCondor-Event` | The event name, so you can route without parsing the body. |
| `X-HTCondor-Webhook` | The DESTINATION's id, for a receiver serving several. Not the subscription's: two subscriptions that both select a job produce one delivery. |
| `X-HTCondor-Signature` | `t=<unix>,v1=<hex>` — see below. |

**Delivery is at least once.** A receiver that may act twice on one event
should deduplicate on `Idempotency-Key`. The body says so too, in
`delivery_semantics`, because a receiver author reads the body and may never
read this page.

**Answer 2xx to accept.** Anything else is treated as described above: 408,
429 and 5xx are retried; every other 4xx is permanent and ends the delivery.
Answer quickly — the attempt times out after 15 seconds — and do the work
afterwards.

### Verifying the signature

The `X-HTCondor-Signature` header is `t=<unix timestamp>,v1=<hex>`, where the
hex is `HMAC-SHA256(secret, "<t>.<idempotency key>.<raw body>")`. The secret is
shown once, when the destination is created, and is the same for everything
that destination receives.

**The secret is displayed hex-encoded and must be decoded before use.** The key
is the 32 raw bytes it decodes to, not the 64-character string you were shown.
This is worth stating because getting it wrong fails silently: HMAC accepts any
key, so the wrong one still produces a well-formed 64-character signature that
simply never matches, with nothing in either system to say why.

```python
import hashlib, hmac

def verify(secret_shown_once, headers, raw_body):
    t, v1 = (p.split("=", 1)[1] for p in headers["X-HTCondor-Signature"].split(","))
    signed = f"{t}.{headers['Idempotency-Key']}.".encode() + raw_body
    want = hmac.new(bytes.fromhex(secret_shown_once), signed, hashlib.sha256)
    return hmac.compare_digest(want.hexdigest(), v1)
```

Note `raw_body` is bytes, joined to the prefix rather than interpolated: sign
what arrived on the wire, not a re-serialization of it. A framework that parses
JSON for you and hands back a dict has already lost the exact bytes, and
re-encoding that dict will reorder or re-space it and fail to match.

The timestamp and the key are inside the signed string on purpose. A signature
over the body alone can be replayed for ever, and replaying "your job failed"
is not harmless when it drives somebody's automation. Reject a timestamp more
than five minutes from your own clock, and compare the signature in constant
time.

### The body

```json
{
  "idempotency_key": "wh_0f3c….failed.-.4242.0",
  "delivery_semantics": "at-least-once",
  "webhook_id": "wh_0f3c…",
  "webhook_label": "Slack #jobs",
  "event": "failed",
  "occurred_at": "2026-11-21T22:14:03Z",
  "scope": "batch",
  "subject": "nightly",
  "job": {
    "cluster_id": 4242,
    "proc_id": 0,
    "id": "4242.0",
    "owner": "alice",
    "batch_name": "nightly",
    "status": "Held",
    "exit_code": 1,
    "hold_reason": "Error from slot1@node17: no such file",
    "command": "/home/alice/run.sh"
  }
}
```

`job` is a small, fixed projection and not the job ad. A job ad has hundreds of
attributes, some of them large and some of them the user's own text; posting
all of it to a third party would make every new attribute a new disclosure
decided by nobody.

A destination whose owner set it to send **whole ads** carries `job_ad`
**beside** `job`, not instead of it — so code written against the projection
keeps working when somebody turns that on:

```json
"job_ad": { "ClusterId": 4242, "ProcId": 0, "JobStatus": 5, "Cmd": "/home/alice/run.sh", "…": "…" }
```

The attribute names and value shapes are the ad's own, exactly as
`GET /api/v1/jobs/{id}` returns them, so a receiver that already reads this
server's job JSON sees one encoding rather than two. When a full-ad delivery
has no ad — the job had already left the queue, or the ad was over 128 KB —
`job_ad` is absent and `job_ad_omitted` says which. Branch on that rather than
on a missing key, or "no ad" is indistinguishable from "no such attribute".

`job` is absent on two kinds of delivery: a test of a destination, and the
summary a subscription sends when it exceeds the fan-out ceiling — which
instead carries:

```json
"capped": { "jobs": 10000, "limit": 500, "note": "…" }
```

## What is not supported

- **Rotating a signing secret.** Replace the destination to get a new one, and
  re-point its subscriptions.
- **Custom events.** The job-watch tools accept a ClassAd expression; a
  subscription does not. An expression has no terminal resolution, so it would only ever
  fire for jobs still in the queue, and a webhook whose expression matches
  nothing is indistinguishable from a quiet pool.
- **Per-attribute payloads.** Summary or the whole ad; there is no way to ask
  for a chosen list of attributes.
- **Destinations inside the cluster.** See the dial policy above. There is no
  allowlist knob; a site that needs one should say so.
