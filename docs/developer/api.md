---
title: API surface
audience: developer
order: 20
---

# API surface

Endpoint groupings. The full reference — request and response shapes —
is in [httpserver/README.md](../../webapi/httpserver/README.md) and the
OpenAPI document at `/openapi.json`. Part of the
[HTTP API + MCP server](../operator/index.md) documentation.


**Jobs** (authenticated, owner-scoped)

Owner-scoped means another user's job answers 404, not 403: these
endpoints do not report whether a job they will not show you exists. A
session with superuser mode armed reaches past that; the action is
re-authenticated as the job's owner and audited.

Submitting and the sandbox:
- `POST /api/v1/jobs` — submit a job. Input spooling is always on, so a
  job with anything to receive is submitted **held** and runs once its
  sandbox arrives. The sandbox is `transfer_input_files` (minus URLs,
  which the execute node fetches itself), plus the executable by
  basename unless `transfer_executable` is false or it is a system path
  such as `/bin/sleep`, plus the stdin file if one is named. A job with
  nothing to receive is released at submit. **The schedd accepts only
  those names and silently drops anything else in the tar.**
- `PUT /api/v1/jobs/{id}/input` — upload the sandbox as a tar. A bare
  cluster id spools every proc of the cluster still waiting.
- `POST /api/v1/jobs/{id}/input/multipart` — the same as form-data.
- `POST /api/v1/jobs/dag` — submit a DAGMan workflow. Its sandbox is
  written once, so everything the workflow will ever need goes in this
  one call.

Reading:
- `GET /api/v1/jobs` — list with `constraint` / `projection`
- `GET /api/v1/jobs/{id}` — retrieve one. Expression-valued attributes
  arrive as `/Expr(...)/`; an `evaluated` member carries the numbers
  `RequestCpus`, `RequestMemory`, `RequestDisk` and `RequestGPUs`
  evaluate to, so a request written as an expression can be read
  without a ClassAd evaluator.
- `GET /api/v1/jobs/{id}/stdout`, `/stderr` — the job's streams
- `GET /api/v1/jobs/{id}/output` — download the output sandbox as a tar
- `GET /api/v1/jobs/{id}/files/{filename}` — one file out of it
- `GET /api/v1/jobs/{id}/dag` — a workflow's structure and node states
- `GET /api/v1/batches/{batchId}/jobs` — a batch's jobs, queued and finished
- `GET /api/v1/jobs/archive`, `/jobs/epochs`, `/jobs/transfers` — history

Acting:
- `DELETE /api/v1/jobs/{id}` — remove, as `condor_rm` does
- `PATCH /api/v1/jobs/{id}` — set attributes, as `condor_qedit` does. A
  bare JSON string value is taken as a **pre-encoded ClassAd
  expression**; send `{"type": "string", "value": "..."}` to have a
  value quoted for you. Protected and immutable attributes are refused.
- `POST /api/v1/jobs/{id}/hold`, `/release` — one job
- `POST /api/v1/jobs/hold`, `/jobs/release` — by constraint
- `GET /api/v1/jobs/{id}/ssh` — a session inside a running job; see
  [Shell access over SSH](../user/ssh.md)

Estimating:
- `GET /api/v1/jobs/{id}/runtime-estimate` — how long the job is likely
  to run; see [Runtime estimates](../operator/estimates.md#runtime-estimates)
- `POST /api/v1/jobs/runtime-estimates` — the same for up to 5000 jobs at
  once; see [Runtime estimates](../operator/estimates.md#runtime-estimates)
- `GET /api/v1/jobs/{id}/similar` — the finished jobs behind an estimate

**Pool** (authenticated; every signed-in user)
- `GET /api/v1/pool/drain-estimates` — when each execute node would be
  empty if drained now; see [Drain estimates](../operator/estimates.md#drain-estimates)

**Templates** (browser-submission UI)
- `GET /api/v1/templates` — list
- `POST /api/v1/templates` — save a custom template
- `DELETE /api/v1/templates/{id}` — remove

**Health & metrics**
- `GET /healthz` — liveness, always 200 if the process is up
- `GET /api/v1/ping` — schedd + collector probe, **as the caller**. Needs a
  credential and answers 401 without one: it reports an identity, an
  authentication method, a session id and the daemon's valid commands, and
  unauthenticated it answered for the service account. Use `/readyz` for an
  unauthenticated health check — it reads the periodic pinger's cached
  result and is what the Kubernetes probes use. Without a credential it
  reports each daemon's status only; an admin session or an API key with
  the `metrics` scope also gets the names, addresses and error text behind
  them.
- `GET /api/v1/schedd/ping`, `GET /api/v1/collector/ping` — the same, one
  daemon each
- `GET /metrics` — Prometheus exposition (requires the `metrics`
  scope unless `HTTP_API_METRICS_PUBLIC=true`)


**Admin** (gated on `WebUIAdminGroup` membership)
- `/api/v1/admin/oauth2/clients`, `/api/v1/admin/oauth2/tokens`
- `/api/v1/admin/api-keys`, `/api/v1/admin/api-keys/{key_id}`
- `/api/v1/admin/logs`, `/api/v1/admin/condor-config`
- `GET /api/v1/admin/usage` — call statistics by tool or endpoint, user
  and client. `?kind=mcp|rest` picks the kind (default `mcp`);
  `?name=`, `?user=`, `?client=` narrow them. See
  [Usage statistics](../operator/metrics.md#usage-statistics)

**Placement** — see [Placement](../operator/placement.md#api-endpoints).

**Chat assistant** — see
[Agents](../operator/agents.md#chat-assistant).

**MCP** (when enabled via `HTTP_API_ENABLE_MCP=true`)
- The OAuth2 endpoints do not depend on this; see
  [When OAuth2 is served](../operator/auth.md#when-oauth2-is-served).
- The full MCP surface is exposed at `/mcp/*` for cooperative
  agents. The standalone `htcondor-mcp` binary speaks the same MCP
  protocol over stdio. See [mcpserver/README.md](../../webapi/mcpserver/README.md).

## Example calls

```bash
# Submit a job (Bearer token from your HTCondor IDP or external OIDC).
curl -X POST http://localhost:8080/api/v1/jobs \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"submit_file":"executable=/bin/echo\narguments=Hello\nqueue"}'

# List your jobs.
curl 'http://localhost:8080/api/v1/jobs?constraint=Owner=="alice"' \
  -H "Authorization: Bearer $TOKEN"

# Get health. /readyz needs no credential; /api/v1/ping pings as you.
curl http://localhost:8080/readyz
curl http://localhost:8080/api/v1/ping \
  -H "Authorization: Bearer $TOKEN"

# Scrape metrics with an API key (mint via /admin/api-keys).
curl http://localhost:8080/metrics \
  -H "Authorization: Bearer htca-v1-...."
```
