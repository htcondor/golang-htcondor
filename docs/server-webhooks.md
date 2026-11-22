# Outbound webhooks

A user can ask this access point to POST to a URL when their jobs change
state — into a Zap, a Slack channel, a ticketing system, or anything else
that accepts a webhook. They set one up on the **Connectors** page, or through
`/api/v1/webhooks`.

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
| `HTTP_API_WEBHOOK_FANOUT_LIMIT` | `500` | The most jobs one webhook may deliver for in a single pass. Past it the webhook sends one summary and is disabled; see [Fan-out](#fan-out). |
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
  webhooks they hold — the same read the job-watch evaluator does, through the
  same source — and the change stream makes a delivery prompt rather than
  waiting for the sweep. Deliveries are sent by one worker with a bounded
  pool, at most one in flight per destination.
- **Retention.** Settled deliveries are kept 14 days for the user's own "is
  this working" panel, then swept hourly with the rest of the housekeeping.

## For users

A webhook has three parts: what it watches, which events it wants, and where
to post.

**What it watches** is one job, one batch, or all of your jobs. A job or batch
webhook ends when its subject does — there is nothing left to report — and an
"all my jobs" webhook lasts 90 days and is renewed whenever you edit it.

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

**Where to post** must be an `https` URL. It is stored encrypted where the
site has a key configured, and it is never shown back to you — if you lose it,
replace it.

### Testing one

The **Test** button sends a real delivery: the same signature, the same
headers, the same connection policy, with `"test": true` in the body. It
reports what your receiver answered, including the first part of its error
message. Nothing about it is simulated, which is the point — a test that
bypassed the delivery path would tell you the wrong thing.

### When delivery fails

Each delivery is retried with an exponential backoff, for about six hours.
Two things end it sooner:

- A **permanent refusal** — any 4xx except 408 and 429 — is not retried at
  all. A 404 or a 401 is a statement about the request, and the request will
  be identical next time; retrying it would only delay telling you.
- A **redirect** counts as a misconfiguration. A webhook destination has to be
  a final URL.

After five deliveries in a row are given up on, the webhook is **disabled**
and the page says why. It is disabled rather than left quietly failing,
because silence is indistinguishable from "nothing has happened yet". Fix the
destination and press **Re-enable**.

### Fan-out

A webhook posts once per job. An "all my jobs" webhook over a cluster of ten
thousand would post ten thousand times, which is a denial of service against
your own receiver, so there is a ceiling (500 jobs by default). A webhook that
goes over it sends one summary delivery saying how many jobs reached the event,
and is then disabled: narrow it to a job or a batch, or point it at something
that expects a summary.

## For whoever writes the receiver

A delivery is a `POST` with a JSON body and these headers:

| Header | Contents |
| --- | --- |
| `Idempotency-Key` | Stable across every retry of this delivery, and unique per (webhook, event, job). |
| `X-HTCondor-Event` | The event name, so you can route without parsing the body. |
| `X-HTCondor-Webhook` | The webhook's id, for a receiver serving several. |
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
shown once, when the webhook is created.

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
decided by nobody. `job` is absent on two kinds of delivery: a test of a
webhook that watches a batch or an owner, and the summary a webhook sends when
it exceeds the fan-out ceiling — which instead carries:

```json
"capped": { "jobs": 10000, "limit": 500, "note": "…" }
```

## What is not supported

- **Custom events.** The job-watch tools accept a ClassAd expression; a webhook
  does not. An expression has no terminal resolution, so it would only ever
  fire for jobs still in the queue, and a webhook whose expression matches
  nothing is indistinguishable from a quiet pool.
- **Per-attribute payloads.** The projection above is the whole of it.
- **Destinations inside the cluster.** See the dial policy above. There is no
  allowlist knob; a site that needs one should say so.
