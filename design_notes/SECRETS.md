# Secrets

## Status

**Proposal, nothing implemented.** Open questions are marked **[OPEN]**
and are the parts most worth arguing about.

The distribution half of this already exists and is load-bearing for
OAuth credentials today; what is missing is a store, a group gate, and
one deliberate privilege escalation. Drawing that line up front:

| Exists today | Proposed |
| --- | --- |
| A credential can be stored as a `.use` file with no `.top` and no credmon (`NeedRefresh=false`) | A secrets store in the server's own DB |
| `POST /api/v1/creds/service` already carries `refresh` and `user` | A group policy per secret, and the gate that reads it |
| Running jobs re-fetch credentials every `SEC_CREDENTIAL_REFRESH` | Issuance at submit, plus a re-evaluation sweep |
| `CRED_SUPER_USERS` lets one identity store on another's behalf | The one call site that uses the server's own credential to do it |
| `liveGroupsFor` answers group membership for any named account | Revocation semantics that are not "hold every job" |

## The problem

A site has values that jobs need and users should not have to hold:
a license server token, a shared read-only API key for a reference
dataset, an S3 key for a group bucket. They are not credentials in the
credmon sense. Nothing mints them, nothing refreshes them, and the
typical lifetime is "until someone changes it".

What they do have in common with credentials is delivery: the value has
to reach the inside of a job sandbox, on an execute node, without
passing through the job ad or the submit file.

The model is a Kubernetes secret: a named set of key/value pairs,
readable by a job that asks for it, scoped to a set of groups or to
every authenticated user of the API service, and propagated to running
jobs if it changes.

## A secret is a credential with no refresh token

The temptation is to write a credmon for this. Resist it. A credmon
exists to *mint* something that expires, and a secret has nothing to
mint; a secrets credmon would be a process whose job is to copy a file.

The shape already exists and is already named. From
`src/condor_utils/store_cred.cpp`, in the code that decides what a
query should look at:

> stored by the user. They don't have a `.top` file and should always
> have a `.use` file.

A credential with a `.use` and no `.top` is a first-class case: no
refresh token, no credmon kick, nothing to renew. That is the secret.

Three mechanics make it fit better than it has any right to:

- **The `.top`/`.use` choice is client-controlled.** `store_cred.cpp`
  reads `NeedRefresh` off the request ad; false writes `.use` directly.
- **The default is already right.** With `NeedRefresh` unset, the code
  falls back to `CredSorter`, and a service name no configured credmon
  claims sorts as unknown, which writes `.use`. A client that forgets
  the flag still gets a secret rather than a stalled `.top`.
- **A `.use` is JSON.** `ValidateOAuthCredential` requires valid JSON on
  the REST path, and a key/value map is a JSON object. The payload the
  Kubernetes model wants is the payload the existing validator already
  demands.

The job sees `$_CONDOR_CREDS/<name>.use` — a file, which is the half of
the Kubernetes analogy that comes for free. Environment variables are
also reachable, on the execute node and without putting anything in an
ad, but they are a layer on top of this one rather than an alternative
to it; see below.

## What the credential API already reaches

`POST /api/v1/creds/service` is already plumbed end to end for the
`.use`-only case. No code change is needed to write one:

| Layer | Carries |
| --- | --- |
| REST body | `{"refresh": false, "user": "alice", "service": "...", "credential": "<base64 or raw>"}` |
| `handlers_credd.go` | `req.Refresh *bool`, `req.User` |
| `credd_client.go` `PutServiceCred` | sets `NeedRefresh` on the command ad |
| `store_cred.cpp` | `NeedRefresh=false` → `<service>.use`, no `.top` |

`ValidateOAuthCredential` will accept a key/value JSON object as-is, and
`decodeCredential` takes base64 or a raw string.

What the API does **not** reach, and these are the gaps worth being
precise about:

- **It runs as the caller.** `credentialContext` builds the context with
  `requireAuthentication`, so the credential that arrives at the credd
  is the caller's. `CRED_SUPER_USERS` is checked against the socket
  owner (`store_cred.cpp`, and again in `credd.cpp` for check-creds), so
  passing `user` for somebody else works only when the *caller* is a
  cred super user. An ordinary user pushing a secret to another account
  is correctly refused today, and a secrets service pushing on a user's
  behalf is refused along with it.
- **There is no group gate on it.** Authentication plus
  `requireCondorScope` is the whole check. Nothing on this path knows
  what a group is.
- **The credd is a distribution point, not a source of truth.** It has
  no notion of a secret that exists independently of a user's copy of
  it, no way to enumerate who holds one, and no way to answer "what is
  the current value".
- **`GET /api/v1/creds/service/<service>/credential` reads the value
  back.** For a user's own OAuth token that is reasonable. For a site
  secret it means any holder can read the plaintext over HTTP, which may
  be fine (the job can read it anyway) or may not. **[OPEN]**

So: the existing API reaches the `.use`-only mechanism completely, and
reaches none of the secret-ness. The work is above it.

## Propagation is already built

`JICShadow::refreshSandboxCredentialsOAuth` registers a timer for
`SEC_CREDENTIAL_REFRESH` (default 300 seconds), re-fetches from the
shadow, and rewrites the sandbox `.condor_creds` directory. The shadow
reads from `SEC_CREDENTIAL_DIRECTORY_OAUTH/<Owner>/` on each fetch.

Updating the file on the access point therefore reaches running jobs
within one refresh interval, with no new machinery and no push.

Two consequences to state rather than discover:

- **Rotation does not retract.** A job that has already read the old
  value keeps it. Changing a secret limits future exposure; it does not
  undo past exposure. This is inherent to handing a value to code you do
  not control, and no design fixes it.
- **A deleted file is a job hold, not a soft failure.** If the shadow
  cannot read the `.use`, `CONDOR_getcreds` sets `had_error` and calls
  `holdJob("Job credentials are not available")`. Revocation by deletion
  holds every running job that uses the secret. See below.

## Environment injection

The Kubernetes model exposes a secret as environment variables, not just
as a mounted file. That is possible here, in two places, and the
differences between them matter more than the feature does.

**`STARTER_JOB_ENVIRONMENT` is not one of them.** `Starter::GetJobEnv`
merges in a fixed order: the config value first, then the job ad's own
environment, then `PublishToEnv`. The job ad overrides the config, so a
user's `environment =` line silently wins over anything injected that
way — and the knob is global to the execute node besides, with no idea
which job or which secret it is serving. `PublishToEnv` is the layer
that wins, which is why `_CONDOR_CREDS` set there cannot be overridden.

**`USER_JOB_WRAPPER`** is the straightforward one. The wrapper is exec'd
in place of the job, inherits `_CONDOR_CREDS`, reads the `.use` file,
exports what it wants, and execs the real executable. The value never
enters a ClassAd, the policy stays on the execute node, and for
Apptainer the wrapper runs outside the container with the existing
`SINGULARITYENV_` handling deciding what crosses in.

**It does not run for the Docker universe.** `DockerProc::StartJob`
calls `GetJobEnv` and hands the result straight to `docker run`; no user
job wrapper is involved. So a wrapper-based scheme silently covers some
universes and not others, which is the worst property a security
mechanism can have.

**`HOOK_PREPARE_JOB`** is the one that covers everything.
`HookPrepareJobClient::hookExited` merges the hook's stdout ClassAd into
the starter's copy of the job ad (`job_ad->Update(updateAd)`) before
launch, so a hook that emits `Env` reaches the job in every universe,
Docker included. The starter publishes a constructed update ad rather
than the whole job ad and does not send `ATTR_JOB_ENVIRONMENT` back, so
the value stays on the execute node — **verify that before relying on
it**, because the whole scheme rests on it.

**The Docker caveat is real and is not about HTCondor.**
`add_env_to_args_for_docker` appends each variable as `-e VAR=value` on
the `docker run` **command line**. Anything injected into a Docker
universe job's environment is therefore visible in the process table of
the execute node to anyone who can run `ps`. For that universe,
environment injection is strictly more exposed than the file it came
from.

Recommendation: the file is the primitive, environment is an opt-in per
secret implemented by a prepare-job hook shipped with the server, and
the Docker universe either refuses it or warns loudly. **[OPEN]** which
of refuse-or-warn, and whether per-secret opt-in is the right
granularity or it should be per-request at submit.

None of this is a confidentiality boundary against the job itself — the
job can read the file regardless. What it changes is exposure to *other*
processes on the execute node and leakage into logs, and on that axis
the file wins everywhere and Docker's argv loses badly.

## Shape

**The store.** Secrets live in the API server's own database, sealed
under the master key with their own HKDF label, the way the identity
cookie and the SSH approval token are. Not a key minted at startup —
a secret that differs per replica and rotates on restart is not a
managed secret, and this is the third time that lesson would be
available to learn.

**The policy.** Mirror site apps exactly, including the reasoning:

```
HTTP_API_SECRET_<ID>_GROUPS = smith_lab, cryoem_users
HTTP_API_SECRET_<ID>_GROUPS = *
```

- Groups are read **only** from site-side configuration, never from
  anything the secret's author supplies.
- `*` means every authenticated user.
- An empty group list is an **error**, not "nobody". A secret that names
  no groups is far more likely to be a mistake than a deliberate
  withdrawal, and the mistake should be loud.

**Issuance.** Two points, because one is not enough:

1. *At submit*, when the server sees a job name a secret, it evaluates
   the submitter's groups and pushes the value into that user's cred
   directory before the job can need it. Nothing unentitled runs
   quietly: `CREDD_CHECK_CREDS` runs ahead of each submit and answers a
   missing credential with a URL to visit or an error, and a job that
   is submitted anyway is held by the shadow when the fetch fails. The
   refusal exists; what it *says* today is the problem, and that is the
   namespace question left open below.
2. *On a sweep*, so a changed value reaches users who are not submitting
   anything right now, and so membership changes are noticed at all.

**The privileged call.** The push for another user is the one piece that
needs the server's own credential rather than the caller's, and the
audit that found twenty-two of these hazards is the reason to write it
down: this is the only secrets call site that may use the daemon
credential, it takes the target username as an explicit argument, and
the group decision happens before it, never inside it.

## Rules that exist to prevent a specific wrong answer

**A secret's presence proves nothing.** `store_cred` lets any user store
a service credential for *themselves* under any name, including the name
of a site secret, in their own directory. They can only fool themselves
— confidentiality is intact — but it means no job and no tool may treat
"secret X is present" as evidence of entitlement, and the server must be
prepared to overwrite and repair a user's copy. Reserve a name prefix
for site secrets so the collision is at least visible.

**A failed group lookup is not a revocation.** `liveGroupsFor` returns
`(groups, answered)` precisely because "this person is in no groups" and
"nothing could tell me" are different answers. The sweep must revoke
only on `answered=true` with the group absent. Treating `answered=false`
as a no turns an SSSD outage into mass deletion of `.use` files, which
by the hold path above turns into every job on the access point going on
hold. The tri-state exists; use it.

**Never let the value into a job ad, a submit file, or a log.** The job
names the secret; the value travels the credential path. This is the
whole reason to build on the cred directory rather than on attributes.

**Deletion is an operation with a blast radius, not a config change.**
It holds jobs. The UI should say how many before doing it.

## Revocation

The only immediate revocation the architecture offers is deleting the
`.use`, which holds jobs. That is blunt, but it is also honest and
auditable, and the alternatives are worse (a job that silently keeps
running with a secret it is no longer entitled to is the failure that
matters).

Proposed default is two-phase, with the operator able to skip to the
second:

1. **Withdraw.** Stop issuing to new jobs and stop refreshing. Running
   jobs keep what they hold and finish.
2. **Revoke.** Delete the `.use` files. Jobs holding the secret go on
   hold, which is the point.

**[OPEN]** Whether withdrawal should be time-bounded (revoke
automatically after N hours) or left to the operator, and whether a
"revoke" should also remove the secret from the store or only from the
users.

## Costs

- **One plaintext copy per user per secret** in
  `SEC_CREDENTIAL_DIRECTORY_OAUTH`, plus one per running sandbox on
  every execute node. A secret shared by every user of a large access
  point is a large number of copies.
- **An update is O(users) file writes.** For a pool-wide secret on a
  busy access point that is a real amount of I/O on every rotation.
- Neither is avoidable without a C++ change, because the shadow's fetch
  is hard-keyed on the job's `Owner` — it builds the path by joining the
  cred directory with the owner name, and there is no shared location it
  consults.

## Rejected

**A secrets credmon.** A daemon whose entire job is to copy a static
file, plus a `.top` that exists only to trigger it, plus a second place
for the group policy to live. The credmon is the minting half of the
architecture and a secret has nothing to mint.

**A shared credential directory** (`SEC_CREDENTIAL_DIRECTORY_SHARED`,
consulted by the shadow when the per-user file is absent). This is the
principled fix for the fan-out cost, and it is a change to the shadow
that moves an authorization decision into C++ on the access point. Worth
revisiting if fan-out ever actually hurts; a bad place to start.

**The value in the submit file's `environment`.** The one form of
environment injection that is genuinely wrong: it puts the plaintext in
the job ad, where `condor_q -l` and everything that reads an ad can see
it, and where it outlives the job. Environment injection done on the
execute node is a different proposition — see below.

**Putting the value in the job ad, encrypted.** Everything that reads a
job ad becomes a thing that holds secrets, and the ad outlives the job.

## [OPEN]

- **Is the value readable back over REST?** The holder can read it
  inside a job regardless, so refusing is partly theatre — but it is the
  difference between "a compromised session can exfiltrate every secret
  the user is entitled to in one request" and "it has to submit a job
  and wait". Leaning toward: not readable, with an explicit audited
  reveal for an admin.
- **One file or many?** A secret with N keys can be one `.use` holding a
  JSON object, or N services. One file is fewer moving parts and matches
  the validator; many files matches what a program expects to `cat`.
  Leaning toward one file plus a documented convention.
- **What does a direct `condor_submit` do** when a user names a secret
  they are not entitled to? Check-creds will create a `.top` and try to
  mint, which for a name no credmon claims surfaces as an OAuth prompt —
  a confusing refusal for something that is not an OAuth service. How
  the secret namespace is declared to the credd is what makes that
  failure clean, and it needs to be settled before anything ships.
- **Multi-AP.** A secret is per-access-point in this design because the
  credd is. A user who submits on two access points needs the push on
  both, which the sweep can do but the submit-time path cannot.
