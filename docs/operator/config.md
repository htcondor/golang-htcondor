---
title: Configuration reference
audience: operator
order: 30
---

# Configuration reference

Every `HTTP_API_*` setting, with the sections that explain them. Part of
the [HTTP API + MCP server](index.md) documentation.

Settings live in HTCondor config (`condor_config_val`-readable) and are
prefixed `HTTP_API_*`. The handful an access point usually has to set is
on the [server page](index.md#configure-it).

## The htcondordb mirror

Job-queue routing to an [htcondordb](index.md#further-reading) mirror needs no
configuration in the common case, **including when this server runs on a
different host or pod from the mirror and the schedd**: discovery goes
through the pool's collector, which spans hosts. The local address file is
only a fallback for a co-located mirror whose ad is not getting through.
With neither a collector nor `HTTP_API_DBMIRROR_ADDRESS`, routing is simply
off rather than failing — an address file that resolves on every host would
otherwise switch it on for deployments that never asked.

## Settings

Generated from the table the server declares its settings in, so the
type, the default and what it takes to install a change are the ones the
code uses rather than a second copy of them. Alphabetical by parameter.

<!-- BEGIN GENERATED SETTINGS: edit webapi/knobs/knobs.yaml, then run `make knobs` -->

### `HTTP_API_BUILD_DEFAULT_CPUS`

**Whole number.** Unset requests 8 CPUs. Changing it needs a restart.

CPUs a container build requests when the caller does not say. A value
that is not a whole number, or is negative, is reported and ignored
rather than refusing to start: the rest of the server has nothing to
do with container builds, and a typo in a build setting should not
take the API down.

### `HTTP_API_BUILD_DEFAULT_DISK_MB`

**Whole number.** Unset requests 30720 MiB (30 GiB). Changing it needs a restart.

Disk in MiB a container build requests when the caller does not say.
Ignored with a report when it is not a non-negative whole number, as
above.

### `HTTP_API_BUILD_DEFAULT_MEMORY_MB`

**Whole number.** Unset requests 16384 MiB (16 GiB). Changing it needs a restart.

Memory in MiB a container build requests when the caller does not say.
Ignored with a report when it is not a non-negative whole number, as
above.

### `HTTP_API_BUILD_EXTRA_SUBMIT`

**String.** Unset by default. Changing it needs a restart.

Extra submit-file lines added to every container build job, for
whatever else a site's build slots need.

### `HTTP_API_BUILD_MAX_CPUS`

**Whole number.** Unset leaves CPUs uncapped. Changing it needs a restart.

Most CPUs one container build may request; a larger request is clamped
to it rather than refused. Worth setting, because build slots are
large -- CHTC's are 128-core, 256 GB machines -- and an agent asking
for plenty should not be able to claim a whole one.

### `HTTP_API_BUILD_MAX_DISK_MB`

**Whole number.** Unset leaves disk uncapped. Changing it needs a restart.

Most disk in MiB one container build may request, clamped rather than
refused, for the reason above.

### `HTTP_API_BUILD_MAX_MEMORY_MB`

**Whole number.** Unset leaves memory uncapped. Changing it needs a restart.

Most memory in MiB one container build may request, clamped rather
than refused, for the reason above.

### `HTTP_API_BUILD_REQUIREMENTS`

**String.** Unset by default. Changing it needs a restart.

ClassAd expression selecting the slots a container build may run on,
for a site where no schedd transform does it. A site picks its build
machines by its own convention and the tool cannot guess it, so an
unconfigured server submits an ordinary job and relies on the caller
naming a destination.

### `HTTP_API_BUILD_STAGING_BASE`

**String.** Unset by default. Changing it needs a restart.

Where built images are published when the caller gives a bare name.
Unset means every call has to name its own destination. It may be a
template: {user} expands to the authenticated caller and {initial} to
the first letter of that name, so a per-user staging area can be
written osdf:///chtc/staging/{initial}/{user}. A base with no
placeholder is used verbatim.

### `HTTP_API_CONNECTORS`

**Comma-separated list.** Unset by default. A change is installed by `condor_reconfig`.

Names the external accounts this access point offers to connect. Each
is then configured by the `HTTP_API_CONNECTOR_<instance>_*` settings.

### `HTTP_API_CONNECTOR_<instance>_*`

An external account users may connect, so a job can read their files
from it. `HTTP_API_CONNECTORS` names the connectors offered; each is
configured by the settings below with its id folded in, e.g.
`HTTP_API_CONNECTOR_BUILTIN_GDRIVE_GROUPS` for builtin:gdrive.

#### `HTTP_API_CONNECTOR_<instance>_AUTHORIZATION_URL`

**String.** Unset by default. A change is installed by `condor_reconfig`.

The provider's authorization endpoint, for a connector that is not a
shipped one.

#### `HTTP_API_CONNECTOR_<instance>_CLIENT_ID`

**String.** Unset by default. A change is installed by `condor_reconfig`.

The OAuth2 client id this access point registered with the provider.

#### `HTTP_API_CONNECTOR_<instance>_CLIENT_SECRET_FILE`

**String.** Unset by default. A change is installed by `condor_reconfig`.

File holding the OAuth2 client secret. A connector with no readable
secret is refused rather than offered. Holds or names a credential:
keep it readable only by the account the server runs as.

#### `HTTP_API_CONNECTOR_<instance>_ENABLED`

**Boolean.** Default `true`. A change is installed by `condor_reconfig`.

Whether the connector is offered at all. Default true, so a connector
is turned off by setting this rather than by removing its
configuration.

#### `HTTP_API_CONNECTOR_<instance>_GROUPS`

**Comma-separated list.** Unset by default. A change is installed by `condor_reconfig`.

Group(s) whose members may connect this account. Unset offers it to
everyone who may sign in.

#### `HTTP_API_CONNECTOR_<instance>_KIND`

**String.** Unset by default. A change is installed by `condor_reconfig`.

Which shipped connector definition to use, for an id that is not
itself one.

#### `HTTP_API_CONNECTOR_<instance>_SCOPES`

**Comma-separated list.** Unset by default. A change is installed by `condor_reconfig`.

The scopes requested when a user connects this account.

#### `HTTP_API_CONNECTOR_<instance>_TOKEN_URL`

**String.** Unset by default. A change is installed by `condor_reconfig`.

The provider's token endpoint, for a connector that is not a shipped
one.

#### `HTTP_API_CONNECTOR_<instance>_TRANSFER`

**String.** Unset by default. A change is installed by `condor_reconfig`.

How a job fetches from this account, where the connector supports more
than one way.

### `HTTP_API_CONNECTOR_OUTPUT_SWEEP_INTERVAL`

**Duration.** Unset leaves the sweep off. Changing it needs a restart.

How often finished jobs are swept for output to deliver to a connected
account, as a duration with a unit. A bare number is refused rather
than guessed at, and anything under a minute is raised to one with a
message: sweeping faster asks the queue and the provider about the
same finished jobs more often than they can change, and a provider's
rate limit is a real consequence. Read at startup; the running ticker
does not re-read it, so a change needs a restart even though the rest
of the connector configuration reloads.

### `HTTP_API_CONNECTOR_PREFIX`

**String.** Default `webapi`. Changing it needs a restart.

The prefix of the credd service names this server stores
connected-account credentials under. Change it only to keep two
servers sharing one credd from seeing each other's credentials.

### `HTTP_API_ENABLE_IDP`

**Boolean.** Default `false`. Changing it needs a restart.

Run the built-in identity provider, so this server signs users in
itself instead of sending them to an external one. Off by default. The
provider shares the unified application database; the separate
`HTTP_API_IDP_DB_PATH` it once had is no longer read.

### `HTTP_API_IDEMPOTENCY_KEY_CHECK`

**String.** Unset is AUTO. Changing it needs a restart.

How much of the idempotency-key guarantee this access point enforces.
NONE does not check at all; QUEUE checks the live job queue; FULL
checks the queue and the mirrored history, which is what makes a key
reused after its job finished still a duplicate; AUTO is FULL where an
htcondordb mirror is configured and QUEUE where none is. OFF and FALSE
read as NONE, TRUE as FULL. Anything else is reported and AUTO
applies.

### `HTTP_API_IDEMPOTENCY_KEY_HISTORY_WINDOW`

**Whole number.** Unset consults the whole history. Changing it needs a restart.

How far back, in whole seconds, the history lookup reads, as a floor
on EnteredHistoryTime. It trades the guarantee for the cost of the
read: a key reused after the window reads as unused. Unset consults
the whole history, which is what the index is there for, so set this
only where that read is measurably too expensive.

### `HTTP_API_IDEMPOTENCY_KEY_RETENTION`

**Whole number.** Unset keeps a key claimed for one hour. Changing it needs a restart.

How long, in whole seconds, a key this server submitted stays claimed
in memory. It covers the window in which a finished job is in neither
the live queue nor the mirrored history yet, so it wants to be longer
than the gap between the schedd writing a job out and the mirror
carrying it. A value that is not a non-negative whole number is
reported and the default applies.

### `HTTP_API_IDEMPOTENCY_KEY_TIMEOUT`

**Whole number.** Unset bounds the lookups at 10 seconds. Changing it needs a restart.

How long, in whole seconds, the queue and history lookups get between
them. A keyed submission waits on them, so this is also the longest
the check can delay one. Reported and defaulted the same way as above
when it is not a non-negative whole number.

### `HTTP_API_IDLE_TIMEOUT`

**Duration.** Default `120s`. Changing it needs a restart.

How long a keep-alive connection may sit idle before the server closes
it, as a Go duration.

### `HTTP_API_IDP_ACCESS_TOKEN_LIFESPAN`

**Duration.** Unset issues access tokens that live one hour. Changing it needs a restart.

How long an access token the built-in identity provider issues stays
valid, as a Go duration. A value that is not a duration, or is not
positive, refuses to start rather than being quietly ignored -- a
token lifetime is not something to get wrong silently. Read only when
`HTTP_API_ENABLE_IDP` is on.

### `HTTP_API_IDP_ISSUER`

**String.** Unset by default. Changing it needs a restart.

The issuer the built-in identity provider names in its own documents.
Unset derives it the same way `HTTP_API_OAUTH2_ISSUER` is derived,
from the externally-visible URL. Read only when `HTTP_API_ENABLE_IDP`
is on.

### `HTTP_API_IDP_REFRESH_TOKEN_LIFESPAN`

**Duration.** Unset issues refresh tokens that live 30 days. Changing it needs a restart.

How long a refresh token the built-in identity provider issues stays
valid. It has to be at least the access token lifespan, or the
provider refuses to start: a refresh token that dies first makes every
refresh fail after the access token expires. Read only when
`HTTP_API_ENABLE_IDP` is on, and refused the same way as above when it
is not a positive duration.

### `HTTP_API_INTERACTIVE_EXTRA_SUBMIT`

**String.** Unset by default. Changing it needs a restart.

Extra submit-file lines added to every interactive session job --
terminal, VS Code and JupyterLab. Operator-only configuration, spliced
in as written, as with the submit-file settings above.

### `HTTP_API_JOB_QUEUE_LOG`

**String.** Unset uses JOB_QUEUE_LOG when that file is actually present. Changing it needs a restart.

The schedd's job queue log, tailed to serve the job change stream, or
the word none to switch the stream off. Unset falls back to
JOB_QUEUE_LOG where the file is there, which is the case when this
server shares a host with its schedd; where it does not -- an API
server in its own container, the usual deployment -- there is nothing
to tail and that is not a misconfiguration. Named explicitly and
absent is an operator error, and is reported as one.

### `HTTP_API_JUPYTER_WORK_DIR`

**String.** Unset uses a directory under the system temporary directory. Changing it needs a restart.

Where the helper binary and token files for JupyterLab submissions are
staged. The helper itself is built into this binary, so nothing has to
be found on disk; this is only where it is written out to be
transferred.

### `HTTP_API_LOG`

**String.** Unset logs to stdout. Changing it needs a restart.

Where this daemon writes its log: a file, or the words stdout or
stderr. A named file that cannot be written falls back to stdout with
a message -- except under condor_master, where stdout is discarded and
there would be nowhere to report anything, so the daemon refuses to
start and the reason goes to the master's log instead.

### `HTTP_API_MCP_BASE_URL`

**String.** Unset by default. Changing it needs a restart.

The externally-visible base URL of the MCP listener, for a deployment
that serves MCP on an address of its own. Unset, MCP is reached at
`HTTP_API_BASE_URL` like everything else.

### `HTTP_API_MCP_LISTEN_ADDR`

**String.** Unset by default. Changing it needs a restart.

A second address to serve MCP on, separate from
`HTTP_API_LISTEN_ADDR`. Unset serves MCP on the one listener, which is
what almost every deployment wants; this is for separating the agent
surface onto its own port or interface.

### `HTTP_API_OAUTH2_ACCESS_TOKEN_LIFESPAN`

**Duration.** Unset issues access tokens that live one hour. Changing it needs a restart.

How long an access token this server issues as an OAuth2 authorization
server stays valid, as a Go duration. Refused the same way as the
identity provider's when it is not a positive duration.

### `HTTP_API_OAUTH2_AUTH_URL`

**String.** Unset by default. Changing it needs a restart.

The provider's authorization endpoint, used when discovery is not
configured or did not answer.

### `HTTP_API_OAUTH2_CLIENT_ID`

**String.** Unset by default. Changing it needs a restart.

The client id this server registered with that identity provider.

### `HTTP_API_OAUTH2_CLIENT_SECRET_FILE`

**String.** Unset by default. Changing it needs a restart.

File holding the client secret for that registration. Read as root
where the ordinary read is denied, since a secret staged on an access
point is usually root-owned and this daemon has dropped privilege by
then. An unreadable file is a warning and no secret, not a refusal to
start. Holds or names a credential: keep it readable only by the
account the server runs as.

### `HTTP_API_OAUTH2_DB_PATH`

**String.** Unset by default. Changing it needs a restart.

Deprecated and ignored. The application database is
`HTTP_API_DB_PATH`; the legacy oauth.db schema conflicts with the
unified migration, so setting this is reported and has no effect.

### `HTTP_API_OAUTH2_GROUPS_CLAIM`

**String.** Default `groups`. Changing it needs a restart.

Which claim in the token carries the user's groups, for the settings
that gate access on group membership.

### `HTTP_API_OAUTH2_IDP`

**String.** Unset by default. Changing it needs a restart.

An external identity provider to sign users in through, as the base
URL of its OIDC discovery document. Discovery supplies the
authorization, token and userinfo endpoints; the explicit
`HTTP_API_OAUTH2_*`_URL settings are the fallback when it fails, which
is logged as a warning rather than refused.

### `HTTP_API_OAUTH2_ISSUER`

**String.** Unset by default. Changing it needs a restart.

The issuer this server names in its own OAuth2 documents. Unset, it is
derived from the externally-visible URL, assuming https.

### `HTTP_API_OAUTH2_MAX_GRANT_LIFETIME`

**Duration.** Unset lets a grant go 30 days unused. Changing it needs a restart.

How long a grant may go unused before the user has to consent again.
Measured from the grant's last use rather than from consent, so a
client that refreshes inside the window keeps its grant indefinitely,
which is what lets an unattended integration keep working; an
abandoned grant still expires. What bounds a grant in active use is
the revocation oracles, not this.

See [Refresh-grant
re-authorization](auth.md#refresh-grant-re-authorization).

### `HTTP_API_OAUTH2_REDIRECT_URL`

**String.** Unset by default. Changing it needs a restart.

Where the provider sends the user back to, which has to be a redirect
URI registered with it. Unset, it is the issuer followed by this
server's callback path, which is the right answer whenever the issuer
is how users reach it: /mcp/oauth2/callback on a server whose only
surface is MCP, /oauth2/callback everywhere else. Both paths are
served wherever MCP is on, so an authorization started before an
upgrade still lands; but what has to be registered at the provider is
the URI this server sends, so a build that gains the web UI needs
/oauth2/callback added there -- or this set, to keep sending the old
one.

### `HTTP_API_OAUTH2_REFRESH_TOKEN_LIFESPAN`

**Duration.** Unset issues refresh tokens that live 30 days. Changing it needs a restart.

How long one refresh token this server issues stays valid. Every
refresh resets it, which is why `HTTP_API_OAUTH2_MAX_GRANT_LIFETIME`
exists: without a cap, a client that keeps refreshing keeps its grant
forever.

See [Refresh-grant
re-authorization](auth.md#refresh-grant-re-authorization).

### `HTTP_API_OAUTH2_REQUIREMENTS`

**String.** Unset by default. Changing it needs a restart.

Login policy as a ClassAd expression over the token's claims; a login
proceeds only where it evaluates true. It is parsed at startup and a
malformed one stops the server, rather than failing at somebody's
first login.

See [Local identity mapping](auth.md#local-identity-mapping).

### `HTTP_API_OAUTH2_SCOPES`

**Space-separated list.** Unset by default. Changing it needs a restart.

Scopes requested at sign-in, space separated as RFC 6749 writes them,
e.g. "openid profile email org.cilogon.userinfo". Unset uses the
built-in defaults. Every scope has to be approved for this client at
the provider: CILogon rejects an unapproved scope outright, where it
used to ignore it silently, and that refusal fails every login.
offline_access is added to whatever is set here unless
`HTTP_API_UPSTREAM_REFRESH` is off; that one this server adds itself,
so a refusal of it is absorbed rather than failing logins.

### `HTTP_API_OAUTH2_TOKEN_URL`

**String.** Unset by default. Changing it needs a restart.

The provider's token endpoint, used when discovery is not configured
or did not answer.

### `HTTP_API_OAUTH2_USERINFO_URL`

**String.** Unset by default. Changing it needs a restart.

The provider's userinfo endpoint, used when discovery is not
configured or did not answer.

### `HTTP_API_OAUTH2_USERNAME_CLAIM`

**String.** Unset by default. Changing it needs a restart.

Which claim names the account that owns the jobs, e.g. eppn, where the
provider's subject is not it. Unset uses the subject.

See [Local identity mapping](auth.md#local-identity-mapping).

### `HTTP_API_PICKERS`

**Comma-separated list.** Unset by default. A change is installed by `condor_reconfig`.

Names the file choosers this access point offers. Each is then
configured by the `HTTP_API_PICKER_<instance>_*` settings.

### `HTTP_API_PICKER_<instance>_*`

A file chooser the submit page offers, so a user can pick input files
out of a connected account instead of uploading them.
`HTTP_API_PICKERS` names the choosers that exist; a chooser is
configured by the settings below with its name folded in, e.g.
`HTTP_API_PICKER_LAB_A_API_KEY` for the chooser named lab-a.

#### `HTTP_API_PICKER_<instance>_API_KEY`

**String.** Unset by default. A change is installed by `condor_reconfig`.

The provider's developer key, where its chooser needs one to run in
the browser. Holds or names a credential: keep it readable only by the
account the server runs as.

#### `HTTP_API_PICKER_<instance>_APP_ID`

**String.** Unset by default. A change is installed by `condor_reconfig`.

The provider's application id, where its chooser needs one alongside
the developer key.

#### `HTTP_API_PICKER_<instance>_CONNECTOR`

**String.** Unset by default. A change is installed by `condor_reconfig`.

The connector whose credential the chooser shows files with. Without
one the chooser is not offered.

#### `HTTP_API_PICKER_<instance>_ORIGIN`

**String.** Unset by default. A change is installed by `condor_reconfig`.

The origin the chooser is loaded from, when it is not the provider's
default.

#### `HTTP_API_PICKER_<instance>_PROVIDER`

**String.** Unset by default. A change is installed by `condor_reconfig`.

Which shipped chooser to run. Defaults to the chooser's own name, so
the common case needs one line.

### `HTTP_API_PING_INTERVAL`

**Duration.** Default `1m`. Changing it needs a restart.

How often the daemon pings the pool to decide what /readyz reports, as
a duration with a unit. A bare 0 switches the ping off and is the one
value exempt from the unit rule, since there is no ambiguity in zero.
Switching it off is what a deployment holding no local HTCondor
credential needs: where every request authenticates with a forwarded
token and the daemon itself has nothing to ping with, the ping can
only fail and drag /readyz down with it. A value with no unit, or one
that does not parse, refuses to start rather than being guessed at --
5 could be seconds or minutes.

### `HTTP_API_READ_TIMEOUT`

**Duration.** Default `30s`. Changing it needs a restart.

How long a request may take to arrive in full, as a Go duration. A
value that cannot be parsed is logged and the default applies.

### `HTTP_API_SITE_APP_<instance>_*`

A lab's own web interface, published to the people it belongs to. Each
bundle in `HTTP_API_SITE_APPS_DIR` is configured by the settings below
with its id folded in, e.g. `HTTP_API_SITE_APP_CRYOEM_GROUPS` for the
bundle cryoem. GROUPS is what declares an app: a bundle nobody has
given groups to is not published.

The instances are discovered rather than listed in a parameter, so there is no setting that names them.

#### `HTTP_API_SITE_APP_<instance>_DESCRIPTION`

**String.** Unset by default. A change is installed by `condor_reconfig`.

One line about the app, shown beside its name.

#### `HTTP_API_SITE_APP_<instance>_ENABLED`

**Boolean.** Default `true`. A change is installed by `condor_reconfig`.

Whether the app is served. Default true when the setting is present at
all, so an app is turned off by setting this to false rather than by
removing its groups.

#### `HTTP_API_SITE_APP_<instance>_GROUPS`

**Comma-separated list.** Unset by default. A change is installed by `condor_reconfig`.

Group(s) whose members this app is published to. An app with none is
not published at all -- an empty value is far more often a mistake
than a deliberate "nobody".

#### `HTTP_API_SITE_APP_<instance>_HOME`

**Boolean.** Default `false`. A change is installed by `condor_reconfig`.

Whether this app is what its people land on instead of the dashboard.

#### `HTTP_API_SITE_APP_<instance>_ICON`

**String.** Unset by default. A change is installed by `condor_reconfig`.

The icon shown beside the app's name.

#### `HTTP_API_SITE_APP_<instance>_NAME`

**String.** Unset by default. A change is installed by `condor_reconfig`.

What the app is called in the navigation, instead of its bundle id.

### `HTTP_API_SPOOL_BUFFER_DIR`

**String.** Unset by default. Changing it needs a restart.

Where an archive dropped in the SSH gateway's /submit/, or posted to
the REST upload endpoints, is buffered while it is unpacked. Unset
holds it in memory instead, under a much smaller ceiling.

### `HTTP_API_SPOOL_MAX_UPLOAD_BYTES`

**Size.** Unset caps one upload at 1000 MiB. Changing it needs a restart.

The most bytes one upload command may transfer: a PUT or POST to a
job's input, a redeemed upload URL, or an upload_job_input tool call.
A cluster-wide upload counts the archive once per proc it serves, so a
10 MiB archive spooled to 50 procs is 500 MiB against this and not 10.
Written as a byte count or a size -- 1000M, 2G, 1000MiB all work, and
units are powers of 1024. A cluster-wide call that cannot fit spools
what it can and reports the remainder, so the limit costs an extra
call rather than failing; a single-proc upload over it is refused. A
value that is zero, negative or unreadable is reported and the default
applies: the cap can be raised but not turned off, because reading a
typo as "no ceiling" would turn one bad line into an unbounded
fan-out, which is the thing the limit is for.

### `HTTP_API_SUBMIT_FILE_DEFAULTS`

**String.** Unset by default. Changing it needs a restart.

Submit-file lines added to every submission, but only where the submit
file is silent, so a user who sets the same command keeps their own
value. Written as HTCondor configuration, so several lines need that
syntax for a multi-line value. Operator-only configuration, spliced in
as written.

### `HTTP_API_SUBMIT_FILE_OVERRIDES`

**String.** Unset by default. Changing it needs a restart.

Submit-file lines added to every submission that win over whatever the
submit file says. For requirements that are not the user's to opt out
of -- an access point that will not have a log written outside the
home directory, say. Otherwise as above: HTCondor configuration
syntax, operator-only, spliced in as written.

### `HTTP_API_TLS_CA_CERT`

**String.** Unset by default. Changing it needs a restart.

CA bundle used to verify client certificates. Unset asks for no client
certificate.

### `HTTP_API_TLS_CERT`

**String.** Unset by default. Changing it needs a restart.

PEM certificate for serving HTTPS directly, rather than behind a
terminating proxy. Needs `HTTP_API_TLS_KEY`; neither alone enables
TLS.

### `HTTP_API_TLS_KEY`

**String.** Unset by default. Changing it needs a restart.

Private key for `HTTP_API_TLS_CERT`. Keep it readable only by the
account the server runs as.

### `HTTP_API_TOKEN_RETENTION`

**String.** Unset keeps dead token rows for 90 days. Changing it needs a restart.

How long rows for dead OAuth2 tokens are kept before being swept, as a
duration with a unit, or off (equivalently 0) to keep them forever. A
bare number is refused rather than read as nanoseconds, which is what
parsing "90" would otherwise mean and is never what anybody intends;
so is anything else that does not parse, and the server refuses to
start rather than sweeping on a figure nobody chose. Only rows that
are already dead -- revoked or expired -- are ever removed, because a
live grant is somebody's session whatever its age.

### `HTTP_API_USER_HEADER_TRUST_ANY`

**Boolean.** Default `false`. Changing it needs a restart.

Accept the user header from any source address. For demonstration and
testing only: with it on, anything that can reach this server can
claim to be any user, so it undoes authentication rather than relaxing
it. Off by default, and the server logs a warning for as long as it is
on. Leave it off anywhere a real person has an account.

### `HTTP_API_WRITE_TIMEOUT`

**Duration.** Default `30s`. Changing it needs a restart.

How long a response may take to write, as a Go duration. This bounds a
slow client, so it also bounds a deliberately long poll; see the note
on streaming endpoints before lowering it.

<!-- END GENERATED SETTINGS -->

## More settings

Settings the table does not carry yet, documented by hand. The same
prose, without the type, the default and what a change takes stated for
each one.

| Knob | Purpose |
| --- | --- |
| `HTTP_API_LISTEN_ADDR` | Listen address; default `:8080`. Bound in addition to the shared-port endpoint under `condor_master`. `-listen` on the command line overrides it. |
| `HTTP_API_BASE_URL` | Externally-visible URL for OAuth2 redirects + share links. |
| `HTTP_API_DB_PATH` | Unified SQLite DB (sessions, OAuth2, IDP, templates, API keys). Default `$(LOCAL_DIR)/htcondor-api.db`. Runs in **WAL** mode, so expect `-wal` and `-shm` files beside it; back up or copy all three together, and put it on a filesystem with working locking (a local disk) rather than NFS. See [the database](index.md#the-database). |
| `HTTP_API_SIGNING_KEY` | Pool signing key for minting per-request tokens. Defaults to `SEC_TOKEN_POOL_SIGNING_KEY_FILE`. |
| `HTTP_API_KEK_FILE` | Path to the master Key Encryption Key (32 raw bytes or 64-char hex; mode 0600/0400). Generate with `openssl rand -hex 32 > <path> && chmod 0600 <path>`. |
| `HTTP_API_WEBUI_ACCESS_GROUP` | Comma-separated group(s) permitted to log in to the web interface (or a JSON array of names, for names holding a comma). Unset falls back to `HTTP_API_MCP_ACCESS_GROUP`. Reloaded on SIGHUP. See [Authorization groups](auth.md#authorization-groups). |
| `HTTP_API_WEBUI_ADMIN_GROUP` | Comma-separated group(s) whose members can reach the admin pages. Unset disables the admin UI. Reloaded on SIGHUP. |
| `HTTP_API_WEBUI_SITE_NAME` / `HTTP_API_WEBUI_SITE_TAGLINE` | What the pages call this access point. Reloaded on SIGHUP. See [Web UI branding](branding.md). |
| `HTTP_API_WEBUI_FOOTER_LOGOS` | JSON array of `{"src", "alt", "href"}` for the footer's institutional band. Unset keeps the shipped CHTC/Morgridge/UW band; `[]` removes it. Reloaded on SIGHUP. |
| `HTTP_API_WEBUI_BRANDING_DIR` | Directory of the site's images, served read-only under `/branding/`. Reloaded on SIGHUP. |
| `HTTP_API_WEBUI_CUSTOM_CSS` | Stylesheet served at `/branding.css` and loaded after the app's own, so `--color-brand-*` can be redefined. Reloaded on SIGHUP. |
| `HTTP_API_WEBUI_FAVICON` | URL for the browser tab icon. Reloaded on SIGHUP. |
| `HTTP_API_WEBUI_PRIVACY_FILE` / `HTTP_API_WEBUI_TERMS_FILE` | Markdown policy documents, served at `/privacy` and `/terms`. The literal `default` serves the shipped template. Unset serves no page and puts no link in the footer. Reloaded on SIGHUP. See [Policy pages](branding.md#policy-pages). |
| `HTTP_API_WEBUI_PRIVACY_URL` / `HTTP_API_WEBUI_TERMS_URL` | Where those paths redirect instead, for a site whose policy lives elsewhere. The file wins when both are set. Reloaded on SIGHUP. |
| `HTTP_API_SITE_APPS_DIR` | Directory of `<id>.zip` site-app bundles and their `<id>.yaml` policy files. Unset disables site apps entirely. See [Site apps](site-apps.md). |
| `HTTP_API_SITE_APPS_RELOAD_INTERVAL` | How often that directory is rescanned, as a Go duration. Default one minute. |
| `HTTP_API_WEBHOOKS` | Whether users may set up outbound webhooks. Default true; `false` makes the endpoints answer 503 and runs neither the evaluator nor the delivery worker. See [Outbound webhooks](webhooks.md). |
| `HTTP_API_WEBHOOK_FANOUT_LIMIT` | The most jobs one webhook subscription may deliver for in a single pass. Default 500; past it it sends one summary a day and withholds the per-job deliveries until the selection drops back under the ceiling. |
| `HTTP_API_WEBUI_APP_ONLY_GROUP` | Comma-separated group(s) whose members land in their site app rather than the dashboard, with the console navigation collapsed. Presentation only; it restricts nothing. Reloaded on SIGHUP. See [Site apps](site-apps.md#app-only-sessions). |
| `HTTP_API_PROJECT_LEADS_FILE` | File naming each project's leads, who may use superuser mode on jobs whose `ProjectName` they lead. Re-read when it changes and on SIGHUP. See [Project leads](auth.md#project-leads). |
| `HTTP_API_PROJECT_LEADS_GROUP` | Group-name pattern containing `{project}`, e.g. `{project}-leads`; members of the group it names for a project lead that project. Reloaded on SIGHUP. See [Project leads](auth.md#project-leads). |
| `HTTP_API_METRICS_PUBLIC` | `true` to disable the API-key gate on `/metrics`. Default off — Prometheus must present an API key with the `metrics` scope. |
| `HTTP_API_TOOL_STATS_FLUSH_INTERVAL` | How often the usage counters are written to the application database (e.g. `5m`). Default `5m`; they are also written at shutdown. Governs both kinds. See [Usage statistics](metrics.md#usage-statistics). |
| `HTTP_API_TOOL_STATS_MAX_LABEL_VALUES` | Cap on distinct values per `/metrics` label for usage statistics. Default `1000`; past it the quietest values render as `other`, while the database keeps them all. |
| `HTTP_API_UTILIZATION_MAX_JOBS` | Most finished jobs one Utilization analysis (`GET /api/v1/utilization`) may read; a larger `limit` is clamped to it. Default `1000000`. An analysis holds about 250 bytes per job while it runs, so the default is about a quarter of a gigabyte at peak; the cached result is the answer, not the jobs. Read at startup. |
| `HTTP_API_RUNTIME_ESTIMATE_INTERVAL` | How often [runtime estimates](estimates.md#runtime-estimates) read newly completed jobs from the htcondordb mirror (a duration, e.g. `5m`). Default `5m`; `0` turns runtime estimates off entirely. Without a mirror they are off whatever this says. Read at startup. |
| `HTTP_API_RUNTIME_ESTIMATE_BACKFILL` | How much job history runtime estimates read at startup, and how long a group of similar jobs is remembered without a new completion. Default `168h`. At most 500,000 of the newest records are read; the log says when that cap is hit. Read at startup. |
| `HTTP_API_TOOL_STATS_MAX_SERIES` | Cap on distinct call combinations held in memory and in the database, per kind. Default `10000`; past it new combinations fold into `other`. This is what bounds memory and disk. |
| `HTTP_API_REST_STATS` | `false` turns off the counting of REST API requests, keeping the MCP counting. Default on. See [REST API requests](metrics.md#rest-api-requests). |
| `HTTP_API_TOOL_STATS_OMIT_USER` | `true` renders every `/metrics` user label as `omitted`, for a site that does not want account names on a scrapeable endpoint. The database still records them. See [Usernames on /metrics](metrics.md#usernames-on-metrics). |
| `HTTP_API_ENABLE_MCP` | Enable the MCP endpoint (`/mcp`), the `/mcp/oauth2/*` spelling of the OAuth2 endpoints, and the `mcp:read`/`mcp:write` scopes in OAuth2 metadata. Required by the chat assistant. OAuth2 itself is served whenever the web UI **or** MCP is enabled, so a web UI with this off still logs people in and serves the device flow. |
| `HTTP_API_MCP_OAUTH2` | `false` serves MCP with the forwarded-HTCondor-token path alone: no OAuth2 authorize, token or registration endpoint, no protected-resource document for `/mcp`, no `/mcp/oauth2/*` spelling, and an OAuth2 token presented to `/mcp` is refused. Default `true`. For a deployment behind a gateway that forwards a per-user IDTOKEN on every request and holds no pool signing key. A web UI keeps its own OAuth2 regardless. Read at startup; changing it needs a restart. See [MCP without OAuth2](auth.md#mcp-without-oauth2). |
| `HTTP_API_MCP_TOKEN_EXCHANGE_ISSUERS` | JSON array of trusted external issuers for RFC 8693 token exchange (see [Token exchange](auth.md#token-exchange-rfc-8693)). Unset disables external exchange. |
| `HTTP_API_MCP_CIMD` | Resolve an `https://` MCP `client_id` as a Client ID Metadata Document (a public client). Default `true`; set `false` to require DCR. See [MCP OAuth2 clients](auth.md#mcp-oauth2-clients). |
| `HTTP_API_MCP_CIMD_ALLOWED_HOSTS` | Comma/space list of host or `.domain` patterns a CIMD `client_id` may point at. Empty = any host (SSRF guards still apply). Recommended for production: without it, unauthenticated callers choose which public hosts this server fetches from. |
| `HTTP_API_MCP_DCR` | Accept dynamic client registration (RFC 7591) at `/oauth2/register` (and `/mcp/oauth2/register` where MCP is on). Default `true`; set `false` when every client is seeded, operator-provisioned or CIMD. Registrations are then refused with 403 and the endpoint is not advertised. See [MCP OAuth2 clients](auth.md#mcp-oauth2-clients). |
| `HTTP_API_MCP_WATCH_MAX_WAIT` | Cap on how long the MCP `check_watches`/`watch_jobs` tools may block in-call before returning (a duration, e.g. `15s`). This number is advertised to the agent in the tool schema, so it has to be one the whole path can deliver, not just this server. Unset = `45s`, or the value derived from `HTTP_API_MCP_MAX_REQUEST_DURATION` where that is tighter. 45s is not a server limit — the server will hold a request for 14m30s by default — it is what an MCP client is observed to wait for before abandoning the call, which returns *nothing at all* to the agent rather than a timeout it could act on. **Set this lower when a gateway or proxy with a tighter timeout sits in front of this server**; set it higher only when you know the clients reaching this deployment are configured for a longer tool timeout. |
| `HTTP_API_CREDD_ADDRESS` | Single-AP mode only (refused in multi-AP mode, where each access point's schedd names its own). Pins the credd this server talks to (a sinful, e.g. `<10.0.0.5:9618?sock=credd>`), overriding discovery. Normally unnecessary: the credd is read from the schedd itself, which publishes its own credd as `CredDIpAddr` in its ad and address file — the same binding `DCSchedd::getCreddAddress` uses. Set this when fronting a remote schedd that does not advertise it, since a credd that is not the submitting schedd's accepts credentials and leaves jobs held anyway. |
| `HTTP_API_REQUIRED_CREDENTIALS` | OAuth service credentials that must exist before a job may be submitted (comma- or space-separated, e.g. `scitokens`). Some access points hold every job submitted without them, whatever the job actually uses. Each submit path checks the caller's credentials first and stores a placeholder for any that is missing, so a person using the web UI does not get a held job for a reason unrelated to what they asked for; a real credential obtained later through the OAuth flow replaces the placeholder. Presence is cached per user for 5 minutes. Best effort: if the credd is unavailable or refuses, the submit still proceeds and the reason is logged. Unset = nothing is required. |
| `HTTP_API_MCP_MAX_REQUEST_DURATION` | Hard stop on an MCP request that is still making progress (a duration, e.g. `5m`). Unset = 15m. While an MCP request runs its write deadline is moved forward, so `HTTP_API_WRITE_TIMEOUT` — which is an absolute deadline meant for ordinary replies — does not decide how long a deliberately waiting tool may wait. A request that stops making progress is still cut off on its last window; this is the backstop for one that never finishes at all. |
| `HTTP_API_ADVERTISE` | Advertise this API server to the collector (a `HTCondorAPI` ad: endpoint, schedd, mirror health, versions). Default `true`; `daemon.Advertise` is a no-op without `COLLECTOR_HOST`, so this only matters to opt out when a collector is configured. |
| `HTTP_API_DBMIRROR_NAME` | Pin job-queue routing to the htcondordb mirror advertising this `Name`. Set it when more than one mirror advertises to the pool: nothing in the ad says which schedd each one mirrors, so without a pin the freshest is chosen, which is a guess. |
| `HTTP_API_DBMIRROR_ADDRESS` | Dial this sinful string instead of the mirror's advertised `MyAddress`, for one reachable only over NAT, a tunnel, or a Kubernetes Service. Freshness still comes from the collector ad — this changes where to connect, not whether the mirror is current enough to trust — and it applies whether the mirror was found through the collector or not. |
| `HTTP_API_DBMIRROR_REQUIRED` | Never fall back to the schedd. A read the mirror cannot serve fails instead of becoming load on the access point you were trying to protect. See [the routing notes](../../webapi/httpserver/README.md#configuration) for which failures are 503 and which are 400. |
| `HTTP_API_DBMIRROR_MAX_LIMIT` | Most records one mirror-served response carries when the request asks for whole records. Default 2000. A larger match set is not an error: the response sets `has_more`, and the rest is fetched a page at a time. Raise it for a reporting client that would rather make one big request than many small ones. |
| `HTTP_API_DBMIRROR_MAX_PROJECTED_LIMIT` | The same ceiling for a request that names a `projection`. Default 10000, higher because a projected row is a few hundred bytes rather than tens of kilobytes. |
| `HTTP_API_SCHEDD_CONSTRAINT` | ScheddAd constraint selecting the access points of [multi-AP mode](multi-ap.md). Unset = one schedd. Read at startup. |
| `HTTP_API_HUB_NAME` / `HTTP_API_HUB_ADDRESS` | Name the federation hubs (one `Name`, or several separated by commas or spaces), or dial one at this sinful string, instead of discovering every `HTCondorDB` ad that carries `FederationConstraint`. The address goes with at most one name. Multi-AP mode only. Read at startup. |
| `HTTP_API_HUB_FAIL_AFTER` | With [two hubs](multi-ap.md#two-hubs), how many `federation_sources` reads of the active hub must fail in a row before reads move to another. Default 2. Read at startup. |
| `HTTP_API_HUB_SWITCH_SECONDS` | With two hubs, how long another hub must hold more fresh access points before reads move to it, and the least time between two such switches. Default 120. Read at startup. |
| `HTTP_API_HUB_SWITCH_MARGIN` | With two hubs, how many more fresh access points another hub must hold to count as fresher. Default 1. Read at startup. |
| `HTTP_API_HUB_SETTLE_SECONDS` | With two hubs, how long after a switch the new hub's access points are reported `settling` and a by-id miss on it is asked of the access point. Default 90. Read at startup. |
| `HTTP_API_MULTI_AP_STALE` | `include` (default) or `exclude` a degraded access point's rows in list reads. Either way it is named in `sources.degraded`. |
| `HTTP_API_JOB_ID_CODEC` | Text form of a job id in URLs and SSH gateway usernames. Default and only built-in: `at` (`123.0@ap40.example.org`). |
| `HTTP_API_BULK_FRESH_SECONDS` | Opt in to letting a multi-AP bulk action skip an access point on the hub's word that nothing there matches, when the hub's copy is at most this many seconds behind. Default 0: every access point is contacted. |
| `HTTP_API_AP_ASSIGNMENT` | Multi-AP mode: ordered chain of [AP assignment](multi-ap.md#ap-assignment) backends, `static`, `mapfile` and `rest`, separated by commas or spaces. Default `static`. An unknown name refuses to start. Read at startup. |
| `HTTP_API_AP_ASSIGNMENT_DEFAULT` | Access point for a user no backend has an answer for, and the one `static` assigns. Unset = such a user cannot submit (403). |
| `HTTP_API_AP_ASSIGNMENT_MAPFILE` | The `mapfile` backend's file. Re-read when it changes and on reconfigure; one that does not parse at startup refuses to start, and a bad edit keeps the last good map. |
| `HTTP_API_AP_ASSIGNMENT_REST_URL` | Base URL of the site's assignment service, for the `rest` backend; `/v1/assignment` is appended. `https` unless loopback. |
| `HTTP_API_AP_ASSIGNMENT_REST_TOKEN_FILE` | File whose contents are sent as `Authorization: Bearer` to the assignment service. Re-read when it changes. Unset = no header. |
| `HTTP_API_AP_ASSIGNMENT_STALE_SECONDS` | How long a user's last known good assignment is used while a backend is failing. Default 3600; `0` never uses one. |
| `HTTP_API_DBMIRROR_TOKEN_SUBJECT` | Identity the token this server presents to the mirror asserts. Default `condor@<trust domain>`. Set it when the mirror authorizes a different name. |
| `HTTP_API_IDENTITY_MAP` | Map the token subject to a local account (`gecos,username`). See [Local identity mapping](auth.md#local-identity-mapping). |
| `HTTP_API_IDENTITY_MAP_PASSWD_FILE` | Account file backing the mapping index. Default `/etc/passwd`. |
| `HTTP_API_IDENTITY_MAP_TTL` | How long the account index and group answers are reused. Default `5m`. |
| `HTTP_API_GROUP_SOURCE` | Comma-separated: `token` (default), `system`, and/or `file:<path>`. Local sources are unioned; `token` may not be mixed with them. See [Local identity mapping](auth.md#local-identity-mapping). |
| `HTTP_API_UPSTREAM_REFRESH` | `auto` (default), `on` or `off`. Whether to ask the provider for `offline_access` and keep the refresh token it returns, which is what lets group membership be re-checked between logins where groups come from the token. `on` is `auto` plus a warning per login when the provider returns no refresh token. `off` neither asks nor stores. |
| `HTTP_API_IDENTITY_MAP_STRIP_DOMAIN` | `true` to also match the local part of a scoped subject. Pair with `HTTP_API_OAUTH2_REQUIREMENTS`. |
| `HTTP_API_DAGMAN_PATH` | Absolute path to `condor_dagman` on the **access point**, for the `submit_dag` tool. **Normally unnecessary — discovered from the schedd's `BIN`; set it only if that discovery is refused.** The server asks the schedd for `BIN` once (a READ-level `condor_config_val` query) and uses `$(BIN)/condor_dagman`; this knob overrides that, and `/usr/bin/condor_dagman` is the fallback when neither is available. `condor_submit_dag` normally finds the binary with `which` on the submitting machine, which is no help here: this server submits to a schedd it shares no filesystem with. |
| `HTTP_API_DAGMAN_ENVIRONMENT` | Extra environment for the DAGMan manager job, as whitespace-separated `KEY=VALUE` pairs. The schedd gives a scheduler-universe job only the environment its ad carries, and `getenv` cannot help because it would capture *this server's* environment rather than the access point's. `PATH` is set for you (PRE/POST scripts need it). Set `CONDOR_CONFIG` here if the access point keeps its configuration somewhere other than the default. A DAG's own `ENV SET` pairs are merged in too, underneath this: what an operator configures here wins, so a workflow cannot redirect `CONDOR_CONFIG` or `BEARER_TOKEN_FILE` by writing one line of DAG. `ENV GET` is reported as a warning and not honoured — it copies variables from the submitting process's environment, which here is this server's, not the access point's. |
| `HTTP_API_MCP_SKILLS_DIR` | Directory of site-authored Markdown skills to publish to agents. Re-read periodically and on SIGHUP. See [Site skills](agents.md#site-skills). |
| `HTTP_API_MCP_SKILLS_RELOAD_INTERVAL` | How often to re-read `HTTP_API_MCP_SKILLS_DIR` so a checkout updated underneath the daemon is noticed without a reconfigure (a duration, e.g. `1m`). Default `5m`; `0` disables the poll and leaves reloads to `condor_reconfig`. The check is stat-only until something actually changes. Read at startup. |
| `HTTP_API_JUPYTER_MAX_LIFETIME_SEC` | Wall-clock ceiling on a JupyterLab session, from when it starts running. Enforced by the schedd (`periodic_remove`), so it holds whatever the sandbox or the browser are doing. Default 28800 (8h); `0` disables. |
| `HTTP_API_JUPYTER_KERNEL_IDLE_SEC` | How long a JupyterLab kernel may sit without executing before it is culled and the server shuts down. Measured from kernel execution, not HTTP traffic, so a tab polling in the background does not look busy. Default 3600; `0` disables. |
| `HTTP_API_JUPYTER_START_GRACE_SEC` | How long a JupyterLab job may wait to start executing, from when it is submitted. Past it the schedd removes the job (`periodic_remove`, with the remove reason "JupyterLab session did not start within …"), and the token it would have connected with expires 10 minutes later, so a job never starts with a token that is already dead. A job that ran and went back to the queue (evicted, or held and released) is removed too, as "JupyterLab session was interrupted and cannot restart": it would rerun with a token its first run spent. Default 14400 (4h). Cannot be turned off: `0`, negative or unreadable values fall back to the default, and values above 604800 (7 days) are clamped, each with a warning. |
| `HTTP_API_JUPYTER_RECONNECT_GRACE_SEC` | How long a session whose tunnel dropped is kept for its helper to dial back. Meanwhile the session stays listed and its proxy answers 503 with `Retry-After`; past it the session is closed and a late redial is refused, which ends the job. Default 300. Cannot be turned off: `0`, negative or unreadable values fall back to the default, and values above 3600 are clamped, each with a warning. |
| `HTTP_API_SSH_TO_JOB` | Allow anything to reach into a running job. Default `true`. `false` withdraws the SSH gateway, the job page's terminal, the job proxy and the VS Code and Terminal applications, and the MCP tools that run commands in a job; JupyterLab, job output and the live tail are unaffected. Read at startup; changing it needs a restart. See [Sites that forbid condor_ssh_to_job](interactive.md#sites-that-forbid-condor_ssh_to_job). |
| `HTTP_API_INTERACTIVE_JUPYTER` | Offer JupyterLab sessions. Default `true`. `false` refuses new ones; sessions already running keep working so they drain. Applied on SIGHUP. See [Which applications this access point offers](interactive.md#which-applications-this-access-point-offers). |
| `HTTP_API_INTERACTIVE_VSCODE` | Offer VS Code sessions. Default `true`, and otherwise as above. |
| `HTTP_API_INTERACTIVE_TERMINAL` | Offer terminal sessions. Default `true`, and otherwise as above. Also withdraws the MCP `interactive_session_start` tool, which starts the same thing. |
| `HTTP_API_VSCODE_IMAGE` | Container image a VS Code session runs. Any transfer scheme works, so a `.sif` staged on OSDF can be named directly. Set, it wins outright and a caller cannot override it; unset, a caller may name an image, which grants nothing new since they can already submit a job with any image. Defaults to a published code-server image. See [VS Code sessions](interactive.md#vs-code-sessions). |
| `HTTP_API_MCP_READ_ONLY` | Serve MCP with no tool that changes anything, for every caller whatever their scopes. Default `false`. `mcp:write` is neither advertised nor granted while it is on. Applied on SIGHUP. See [A read-only access point](agents.md#a-read-only-access-point). |
| `HTTP_API_MCP_CONFIRM_SUBMIT_OVER` | Ask the person running the client to confirm before `submit_job` submits more than this many jobs. Default `0`, which is off. Needs a client that can present the question: one that cannot — including any client older than MCP protocol 2026-07-28 — proceeds unasked, logged at `WARN`. This is a prompt, not a limit. Applied on SIGHUP. See [Asking a person before a bulk operation](agents.md#asking-a-person-before-a-bulk-operation). |
| `HTTP_API_MCP_CONFIRM_REMOVE_OVER` | The same, for the number of jobs `remove_jobs` would remove. Default `0`, off. Counting the matches costs one extra queue query per gated call. |
| `HTTP_API_MCP_DISABLED_TOOLS` | MCP tools this access point does not offer, as glob patterns separated by commas or whitespace. Applied on SIGHUP. See [Disabling tools](agents.md#disabling-tools). |
| `HTTP_API_MCP_REDACT_SECRETS` | Mask credential-shaped text in MCP tool results. Default `true`. Applied on SIGHUP. See [Masking credentials in tool results](agents.md#masking-credentials-in-tool-results). |
| `HTTP_API_MCP_REDACT_DISABLED_RULES` | Masking rules to turn off, as glob patterns over rule ids separated by commas or whitespace. Applied on SIGHUP. |
| `HTTP_API_TRUSTED_PROXIES` | Comma-separated CIDRs (or bare addresses) whose `X-Forwarded-For` / `X-Real-IP` are honored when recording a client address. Unset means none are, and the peer address is logged. |
| `HTTP_API_LLM_API_KEY_FILE` | Path to a file with the LLM API key. Enables the chat assistant. The file must not be world-accessible: 0600, 0400, or 0440 (what a Kubernetes secret volume is under `fsGroup`). |
| `HTTP_API_LLM_API_URL` | Override the upstream Anthropic Messages endpoint (proxy / gateway). The full URL of the `/v1/messages` route. |
| `HTTP_API_LLM_MODEL` | Override the default Claude model. With a gateway, the model name as the gateway knows it. |
| `HTTP_API_LLM_AUTH_SCHEME` | How the key is sent: `x-api-key` (default; what api.anthropic.com reads) or `bearer` (`Authorization: Bearer`, what an OpenAI-style gateway fronting the Messages API, such as Envoy AI Gateway or LiteLLM, authenticates). |
| `HTTP_API_LLM_OPERATOR_INSTRUCTIONS_FILE` | Site-policy text appended to every chat system prompt, last, so it outranks the standard prompts. Re-read on `condor_reconfig`, whether or not the path changed; a read that fails keeps the previous text. Like the API key file, it must not be world-accessible. |
| `PRIVATE_NETWORK_NAME` | This host's private network name. When it equals a daemon's advertised `PrivNet`, that daemon is dialed directly instead of through its CCB broker. Defaults to `$(FULL_HOSTNAME)`, as in C++. See [Reaching jobs behind CCB](networking.md). |
| `HOST_ALIAS` | The host name a client verifies this server's SSL certificate against, published as `alias=` on the advertised address. Defaults to `$(FULL_HOSTNAME)`, as in C++. Behind a shared port the address itself carries only the shared-port server's IP, and HTCondor host certificates have no IP SANs, so without the alias SSL authentication from another host fails the certificate check. Set it only when the certificate names something else — a load balancer, a CNAME. |
| `HTTP_API_SHARED_PORT` | Open an inbound HTCondor port (a port or `host:port`, e.g. `9618`) so this server can be reached by execute nodes behind a Condor Connection Broker. Off by default. See [Reaching jobs behind CCB](networking.md). |
| `HTTP_API_SHARED_PORT_ADDRESS` | The `host` or `host:port` execute nodes should dial to reach that port, when it differs from what this process binds — behind NAT, a container port map, or a Kubernetes Service. Defaults to `TCP_FORWARDING_HOST`, else `FULL_HOSTNAME`, paired with the listen port. |
| `HTTP_API_CCB_STREAMING` | Allow asking the broker to relay a CCB connection instead of being dialed back. Default: on unless running under `condor_master`. With `HTTP_API_SHARED_PORT` set this is the fallback, not the first choice. |
| `HTTP_API_CCB_LEARNED_TTL` | How long what this server learned about a broker (whether execute nodes can reach it, whether the broker can relay) is reused before being rechecked. Default `15m`. |

## Rate limiting

Schedd and collector rate limits apply to this server too, since it
reuses the library:

```
SCHEDD_QUERY_RATE_LIMIT          = 10
SCHEDD_QUERY_PER_USER_RATE_LIMIT = 5
COLLECTOR_QUERY_RATE_LIMIT          = 20
COLLECTOR_QUERY_PER_USER_RATE_LIMIT = 10
```

See [design_notes/RATE_LIMITING.md](../../design_notes/RATE_LIMITING.md)
for full semantics.
