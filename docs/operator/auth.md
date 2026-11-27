---
title: Authentication and authorization
audience: operator
order: 20
---

# Authentication and authorization

Who may reach this server, who the server thinks they are, and what they
may do once they are in. Part of the [HTTP API + MCP server](index.md)
documentation.

## Authentication

Three authentication channels, in precedence order:

1. **API key** — `Authorization: Bearer htca-v1-{key_id}-{secret}`.
   Admin-mintable bearer tokens for non-interactive callers
   (Prometheus, scripts, CI). Scopes gate access: `metrics` for
   `/metrics`, `condor:/READ` and `condor:/WRITE` for the routes that
   reach HTCondor. A key acts as its creator only if it carries a
   `condor:/*` scope; a `metrics`-only key is refused everywhere but
   `/metrics` and `/readyz`. Mint via the admin UI at
   `/admin/api-keys`, or `POST /api/v1/admin/api-keys`. A key may
   also carry the `anyuser:read` entitlement; see
   [Service keys and entitlements](#service-keys-and-entitlements).
2. **OAuth2 / OIDC** — for browsers and MCP clients. The server
   embeds its own IDP (`/idp/*`) and also accepts upstream OIDC
   providers via `HTTP_API_OAUTH2_*` config. An access token this
   server issued is held to its granted scopes on the REST routes that
   reach HTCondor, as an API key is: `condor:/READ` for reads and
   `condor:/WRITE` for writes, or, on a grant with no `condor:/*`
   scope, `mcp:read` / `mcp:write`. The job and Jupyter proxies, job
   SSH and `/warm` run code in the job and need WRITE whatever the
   method. A token granted none of these is refused.
3. **Schedd JWT / pool token** — for `condor_*`-tool–style clients
   that already hold a valid HTCondor token.

A separate browser-session cookie is used for the SPA UI; it sits
on top of (2) — the IDP issues the token; the cookie carries the
session.

## Service keys and entitlements

Most API-key scopes name a door: `metrics` is `/metrics`, `condor:/READ`
is the routes that query HTCondor. The `anyuser:*` scopes are different.
They name whose records the key may reach once inside -- every user's,
rather than its creator's -- which makes them claims about a **person**,
not about a key. People lose entitlements, so these are not simply
stored and trusted forever.

There are two, and neither implies the other, exactly as `condor:/READ`
and `condor:/WRITE` do not:

| Scope | Grants |
| --- | --- |
| `anyuser:read` | Read every user's records. |
| `anyuser:write` | Act on every user's records: remove, hold, release, edit. |

Everything below applies to both.

### Who may request one

Neither entitlement is granted to anybody until you say who may ask for
it. Four settings, a group list and a user list per entitlement:

| Entitlement | Groups | Users |
| --- | --- | --- |
| `anyuser:read` | `HTTP_API_ANYUSER_READ_GROUPS` | `HTTP_API_ANYUSER_READ_USERS` |
| `anyuser:write` | `HTTP_API_ANYUSER_WRITE_GROUPS` | `HTTP_API_ANYUSER_WRITE_USERS` |

Either list alone is enough, and both are comma-separated. **Unset means
nobody** — the opposite of the access gates elsewhere in this file,
where an unset group means "no requirement". A cross-user privilege with
nothing configured has to belong to no one.

The user list exists because the group list cannot always decide. With
`HTTP_API_GROUP_SOURCE = token` and a token that carries no groups —
the default, and the only option in a container — there is nothing for
a group check to read, and naming one person should not require
inventing a directory group for them.

Identities are compared the way HTCondor compares two users: the domain
case-insensitively, the part before the `@` exactly, and **a bare name
matches only a bare name**. On an access point that reports
fully-qualified users, `alice` matches nothing and `alice@example.org`
is what you want. This is the single most common way to write a user
list that silently grants nobody anything.

Being on one of these lists is permission to **ask** for the scope. It
is not the scope. A credential carries an entitlement only by requesting
it and being granted it — at consent for an OAuth2 client, at mint for a
key. `MCP_ADMIN_USERS` is the one setting in this area that does not
work that way: it hands the read tier directly to a caller whose
credential states no scopes at all. It is deprecated for that reason,
and these settings are not its replacement in meaning.

A key is minted in one of two models, and the difference between them is
how that claim is kept honest.

**An ordinary key** belongs to the administrator who minted it and
tracks them. Its entitlement is re-checked on use against whatever live
source the deployment has:

- With `HTTP_API_GROUP_SOURCE = system` (or `file:`), membership is read
  from the account database on each request. A creator who leaves the
  group loses the entitlement at once; the key keeps its other scopes
  and sees its creator's own records.
- With `HTTP_API_GROUP_SOURCE = token` -- the default, and the only
  option in a container -- there is nothing to re-read. Those groups
  came from a token presented at a login that is over. The entitlement
  is then honoured only while a successful check is recent, bounded by
  `HTTP_API_API_KEY_ENTITLEMENT_TTL` (default one hour), and lapses
  after that. The key goes on working; it just stops seeing past its
  creator's own records.

An unreachable account database is silence rather than a denial: it
leaves the entitlement in place for the rest of the window instead of
revoking everybody at once.

**A service key** (`"service": true` at mint) belongs to an automation
rather than to a person. Its entitlement is granted once, at mint --
the only moment the minter's entitlement can be proven, since they are
then holding a live session -- and held for the life of the key.
Nothing re-checks it in between, because an automation has no next
login at which to re-prove anything.

That is why a service key carrying an entitlement **must** set
`expires_at`, capped by `HTTP_API_SERVICE_KEY_MAX_LIFETIME` (default one
year). Re-minting is how the entitlement is re-attested, and the expiry
is what makes somebody do it. Revoke one early by deleting it, as with
any key.

Granting a `anyuser:*` scope requires holding it: an administrator cannot
mint a key more entitled than they are.

Which model to pick is a question about revocation, not convenience. A
credential that should stop working when its holder changes roles is an
ordinary key. A credential that belongs to a service and should be
rotated on a schedule is a service key.

## When OAuth2 is served

The OAuth2 authorization server — its metadata at `/.well-known/oauth-authorization-server`, the
`/oauth2/*` endpoints (authorize, token, device authorization and
verification, introspect, revoke, register, the SSO callback) and the
admin client and token pages — is served whenever the **web UI or MCP**
is enabled -- unless MCP has withdrawn it, for which see [MCP without
OAuth2](#mcp-without-oauth2). `HTTP_API_ENABLE_MCP` gates only MCP: with it off, a server
with the web UI still serves all of the above, but not `/mcp`, not the
`/mcp/oauth2/*` spelling of the endpoints, not the protected-resource
documents for `/mcp`, and does not advertise `mcp:read` / `mcp:write`.
The web UI counts as enabled when it is compiled in (the
`embed_frontend` build). The endpoints are advertised under
`/mcp/oauth2/` only on a server whose only surface is MCP; both
spellings are routed wherever MCP is on.

## MCP without OAuth2

Some deployments never use it. An MCP gateway in front of this server can
forward a per-user HTCondor IDTOKEN as `Authorization: Bearer` on every
request, which `mcpAuthContext` passes to the schedd to validate — the
pod needs no pool signing key and mints nothing. On such a server the
OAuth2 endpoints are surface with nothing behind them: a login reaching
them is refused, because refusing it is the only honest answer.

`HTTP_API_MCP_OAUTH2 = false` says so. MCP is served exactly as before
for a caller that forwards a token, and everything that describes a login
is withdrawn:

- The authorization server is not built (unless the web UI needs it), so
  `/.well-known/oauth-authorization-server`, `/oauth2/*` and the admin
  client and token pages are not served.
- The `/mcp/oauth2/*` spelling, the `/mcp/` callback and device
  verification pages, and the RFC 9728 protected-resource documents for
  `/mcp` and `/mcp/message` are not routed.
- An OAuth2 token presented to `/mcp` is refused with 401, so the setting
  means the same thing on a server that also serves the web UI as on one
  that does not.
- The startup warning about having no way to mint a per-caller credential
  is not logged: it describes a login this server no longer offers.

A request with no credential at all is still refused. The web UI, which
needs OAuth2 to log anybody in, is unaffected: with the UI compiled in,
the authorization server is still built and still serves `/oauth2/*`.

The default is `true` — every deployment served OAuth2 before this
setting existed. It is read at startup; changing it needs a restart.

## Local identity mapping

By default a session's identity is the subject claim from the token, and
group membership is whatever the token asserts. That suits a container,
which holds no account database. On a host where this server runs under
`condor_master` beside a real one, either half can come from the system
instead. The two switches are independent.

**Subject to local account.** `HTTP_API_IDENTITY_MAP` names an ordered
list of strategies; the first to answer wins.

| Knob | Default | Effect |
| --- | --- | --- |
| `HTTP_API_IDENTITY_MAP` | unset (no mapping) | Comma-separated strategy list, e.g. `gecos,username`. `gecos` matches the token subject against accounts' GECOS names; `username` treats the subject as a login name; `placementd` asks condor_placementd (see [placementd as an identity mapping](placement.md#placementd-as-an-identity-mapping)). An unparseable list makes the server refuse to start rather than fall back to an unmapped identity. |
| `HTTP_API_IDENTITY_MAP_PASSWD_FILE` | system default | Read accounts from this file instead of `/etc/passwd`. Both the index and the re-check use it, so it is self-consistent. |
| `HTTP_API_IDENTITY_MAP_TTL` | `5m` | How long the account index and group answers are reused. The index is rebuilt lazily on the first login after it expires. |
| `HTTP_API_IDENTITY_MAP_STRIP_DOMAIN` | `false` | Also try the local part of a scoped subject: `bockelman@wisc.edu` matches a GECOS of `bockelman`. The full subject is tried first, so this only ever adds a fallback. |

Once mapping is on, the session's subject becomes the **local account
name** -- the name HTCondor knows -- so owner-scoping matches actual job
ownership. The asserted subject is logged beside it.

Three behaviours worth knowing before turning this on:

- **A mapping failure denies the login.** There is no fallback to the
  token's claim and no bypass list: the weaker basis must not engage
  exactly when the stronger one breaks.
- **Ambiguity refuses rather than guesses.** If two accounts claim the
  same identity, neither is chosen. Shadowed pairs are named in the
  startup log, so an ordering like `gecos,username` is a choice rather
  than a surprise.
- **`gecos` matches the GECOS *name*, not the raw field** -- everything
  up to the first comma, which is what `os/user` reports on every
  platform. For `tannenba:x:20013:20013:tatannen:...` the value matched
  is `tatannen`; for `...:Tannenbaum, Todd,,:...` it is `Tannenbaum`.

**GECOS can be user-writable.** `chfn` lets a user change their own
full name where `/etc/login.defs` allows it (`CHFN_RESTRICT`; check the
host, not the distribution's reputation). There, a local user who sets
their name to someone else's subject either makes that subject
ambiguous, so its owner cannot log in, or -- when the owner's account
carries no matching GECOS -- becomes the account it maps to. On such a
host set `CHFN_RESTRICT rwh` (room and phone numbers only) in
`/etc/login.defs`. Accounts served by SSSD or another directory cannot
be changed this way. Where token subjects are login names, put
`username` first (`username,gecos`): a subject that is somebody's login
name then never consults GECOS at all. The shadowed pairs in the
startup log are worth watching either way.

An ePPN is scoped (`bockelman@wisc.edu`) and a GECOS or login name
usually is not, so the two cannot match without
`HTTP_API_IDENTITY_MAP_STRIP_DOMAIN`.

It strips **whatever** domain the token carried, so
`bockelman@wisc.edu` and `bockelman@elsewhere.example` reach the same
account. Which providers may issue such a token is decided by
[`HTTP_API_OAUTH2_REQUIREMENTS`](#local-identity-mapping), not here --
enable this only alongside one. The server logs a warning at startup if
stripping is on with no requirements expression configured.

The index is built from the passwd file **and** from SSSD, when an SSSD
socket is reachable. A directory-backed deployment therefore needs no
local copy of its accounts -- a container with only its own `/etc/passwd`
still indexes the whole directory, provided the SSSD domain sets
`enumerate = true` (off by default, and discouraged for very large
directories). A local entry wins a name collision, since that is the one
`os/user` resolves.

Naming a file with `HTTP_API_IDENTITY_MAP_PASSWD_FILE` turns the merge
off: the file becomes the whole account database. That keeps the index and
the re-check reading the same source, which is what makes an
operator-supplied file self-consistent.

Every candidate the index produces is re-checked by name against the live
database before it is believed, and that lookup *does* reach a directory.
So an incomplete index can fail to find somebody, but cannot promote
anybody.

**When nobody can log in, read the startup log first.** The index reports
what it found, and the failure modes each have their own line:

| Line | Means |
| --- | --- |
| `Indexed accounts by GECOS for identity mapping` `accounts=N` | The index in force. Compare `N` against the directory you expect. |
| `The account database enumerated to nothing` | Nothing at all was listed; if accounts live in a directory, SSSD needs `enumerate = true`. |
| `The account index is incomplete: a directory could not be read` | The directory was tried and failed. Retried on the TTL. |
| `The account index covers no directory accounts, but nsswitch.conf lists sss for passwd` | Nothing *failed*, and no directory was reached either -- so the index holds only the local accounts and every directory identity is refused. |

The last one is the one with no other symptom. A container whose SSSD
never started enumerates its own image's `/etc/passwd`, succeeds, and
builds a plausible-looking index of a dozen accounts that maps nobody:
the count is not zero, nothing was degraded, and the only other evidence
is users who cannot sign in. It is reported only where
`nsswitch.conf` names `sss` on the `passwd` line -- a host that genuinely
has no directory is not warned about -- and not when
`HTTP_API_IDENTITY_MAP_PASSWD_FILE` names the database, where no
directory is consulted by design.

Where a mapping does fail, the refusal says which failure it was. With
placement onboarding enabled the person is given a
[placement-only session](placement.md) instead of a refusal page,
and the cause is then only in the log, on the
`Created a placement-only session` line's `reason` field.

**Group membership.** `HTTP_API_GROUP_SOURCE` decides where groups come
from, independently of the mapping above. It takes a comma-separated list;
whitespace around each entry is ignored.

| Knob | Default | Effect |
| --- | --- | --- |
| `HTTP_API_GROUP_SOURCE` | `token` | Comma-separated list of `token`, `system` (or `unix`), and `file:<path>`. |
| `HTTP_API_API_KEY_ENTITLEMENT_TTL` | `1h` | How long an ordinary API key keeps an entitlement that nothing has been able to confirm. Does not apply to a service key. |
| `HTTP_API_SERVICE_KEY_MAX_LIFETIME` | `8760h` | The furthest out a service key carrying an entitlement may set `expires_at`. |

| Source | Reads |
| --- | --- |
| `token` | The token's groups claim. Correct for a container, which holds no account database. |
| `system` | Unix groups for the mapped account, following this host's `nsswitch.conf`. |
| `file:<path>` | An `/etc/group`-format file. |

Several **local** sources are a union, the way glibc merges NSS services --
an account can hold directory groups and hand-maintained ones at once, and
stopping at the first source that answers would drop half of somebody's
membership:

```
HTTP_API_GROUP_SOURCE = system, file:/etc/htcondor-api/groups
```

`token` cannot be combined with a local source; the server refuses to start
if it is. They are different trust bases -- one is what the identity
provider asserted, the other is what this machine's account database says
-- and unioning them would let a provider add a caller to any group the
local policy checks.

**`file:<path>`** exists for memberships the directory does not carry, such
as a staff or admin group that is not in LDAP. The format is `group(5)`,
deliberately, so `getent group <name>` output can be pasted in unchanged:

```
chtc_staff:*:40388:ckoch5,aowen4,bbockelm
ap2001-login:*:40428:bbockelm,qwang377
```

Only the group name and the member list are used; the password and gid
fields are ignored. Like `/etc/group` itself it lists supplementary members
only. The file is re-read when it changes, so an edit takes effect without
restarting the daemon. A file that cannot be read marks the answer
*possibly incomplete* rather than reporting that nobody is in any group --
the difference matters, because the latter is an authorization decision.

With `system`, membership is re-read on every refresh, so a user removed
from a group upstream stops passing without waiting for their refresh
token to lapse (see [Refresh-grant
re-authorization](#refresh-grant-re-authorization)). A read that could
not consult every configured NSS service is treated as *possibly
incomplete*: the login proceeds, because that is what `id` would report,
but it never revokes an existing grant -- an outage must not log out
everyone whose token happens to refresh during it.

Group resolution follows this host's `nsswitch.conf`. A build with cgo
resolves through `getgrouplist(3)`, so every configured service is
consulted; a build without cgo speaks `files` and `sss` natively and
marks the answer incomplete if the line names anything else.

**Which logins are accepted.** `HTTP_API_OAUTH2_REQUIREMENTS` is a
ClassAd expression evaluated against the token's claims, the same way a
schedd evaluates a job's `Requirements` against a machine ad.

| Knob | Default | Effect |
| --- | --- | --- |
| `HTTP_API_OAUTH2_REQUIREMENTS` | unset (accept any) | ClassAd expression over the claims. The login proceeds only if it evaluates to `true`. |

The claims become the ad: a string stays a string, a JSON array becomes a
list, a nested object becomes a nested ad addressable as `outer.inner`,
and `null` becomes `UNDEFINED`. A claim name that is not a bare
identifier is still available through the quoted-attribute syntax, as in
`'urn:oid:1.3.6.1'`.

```
# Only this campus's IDP.
HTTP_API_OAUTH2_REQUIREMENTS = idp == "https://login.wisc.edu/idp/shibboleth"

# ...and only members, who authenticated with MFA.
HTTP_API_OAUTH2_REQUIREMENTS = idp == "https://login.wisc.edu/idp/shibboleth" && \
                               regexp("MEMBER@wisc.edu", affiliation) && \
                               acr == "https://refeds.org/profile/mfa"

# ...or by assurance level, which arrives as a list.
HTTP_API_OAUTH2_REQUIREMENTS = member("https://refeds.org/assurance/IAP/low", eduPersonAssurance)
```

This expresses what an issuer check cannot: a federation such as CILogon
fronts many institutions behind one issuer, so `iss` does not answer
"which campus".

Two properties worth knowing:

- **It fails closed.** Only `true` admits a login. `UNDEFINED`, an error,
  and a non-boolean result all refuse. So a policy naming a claim the IDP
  stopped sending -- or misspelling one -- denies everybody rather than
  silently admitting everybody the moment it stopped meaning anything.
- **A malformed expression stops the daemon at startup**, rather than
  failing at the first person's login.

A refused login gets 403, and the log records the expression and which
claims the IDP returned -- names only, since the values identify the user
and the path is reachable unauthenticated.

**Which claim carries the username.** Some providers put the login-ish
value somewhere other than `sub`.

| Knob | Default | Effect |
| --- | --- | --- |
| `HTTP_API_OAUTH2_USERNAME_CLAIM` | `sub` | Claim read as the asserted subject, e.g. `eppn` or `preferred_username`. This is the value identity mapping receives. |

## Client addresses behind a proxy

A forwarded header is a claim made by whoever connected, and anyone can make one. This server therefore reads `X-Forwarded-For` and `X-Real-IP` **only** from peers named in `HTTP_API_TRUSTED_PROXIES`:

```
HTTP_API_TRUSTED_PROXIES = 10.42.0.0/16, 192.168.1.10
```

Unset, no forwarded header is believed and the peer address is what appears in the access log. Behind an ingress that is the ingress -- which is honest, since the server genuinely does not know who is behind it, rather than a value the caller chose.

Where the proxy's address cannot be known in advance -- a Kubernetes ingress gets a fresh pod address on every restart -- trust everything:

```
HTTP_API_TRUSTED_PROXIES = 0.0.0.0/0, ::/0
```

That is a deliberate choice to believe the header, spoofable and all, in exchange for seeing past the ingress. Sound only where nothing but the ingress can reach the server's port; if the pod is directly reachable, any caller can write what it likes into the log.

Configured, the `X-Forwarded-For` chain is walked from the **right**, discarding hops that are themselves trusted proxies; the first address that is not one is the client. Taking the leftmost entry is the common shortcut and is wrong exactly when it matters: proxies *append*, so a caller that sends its own `X-Forwarded-For` puts a value of its choosing at the front of the list, ahead of what the ingress observed.

This is separate from `HTTP_API_USER_HEADER_TRUSTED_PROXIES`, which decides who may assert an *identity* -- a much larger grant than being believed about an address, and deliberately not the same list.

## Authorization groups

Four knobs decide who may do what. Each takes a **comma-separated list**
and admits a user holding **any** of the named groups; surrounding
whitespace is trimmed, and an empty value means "no group required".
Matching is case-insensitive.

| Knob | Gates |
| --- | --- |
| `HTTP_API_WEBUI_ACCESS_GROUP` | Logging in to the web interface. Unset falls back to `HTTP_API_MCP_ACCESS_GROUP`. |
| `HTTP_API_MCP_ACCESS_GROUP` | Driving this access point through MCP. |
| `HTTP_API_WEBUI_ADMIN_GROUP` | The admin pages. Unset disables the admin UI. |
| `HTTP_API_SUPERUSER_GROUP` | Superuser mode (see below). Unset disables the feature. |

`HTTP_API_MCP_READ_GROUP` and `HTTP_API_MCP_WRITE_GROUP` narrow MCP
further, and take lists on the same terms. The write group also decides
`condor:/WRITE`: a user it denies `mcp:write` is denied that too.

### Names the comma rule cannot spell

A name may contain **spaces**, which directory-sourced names often do.
Write it as it is, with no quoting -- HTCondor keeps quotation marks as
part of a value, so quoting it creates a group nobody is in:

```
HTTP_API_MCP_ACCESS_GROUP = CO:COU:CMS Connect COU:members:active
```

A value that begins with `[` is read as a **JSON array of strings**
instead, which is how to name a group whose own name contains a comma,
or one that begins or ends with a space:

```
HTTP_API_MCP_ACCESS_GROUP = ["CO:COU:CMS Connect COU:members:active", "Lab, Smith"]
```

Names inside the array are taken verbatim -- nothing is trimmed -- and
an empty array means what an empty value means: no group required. A
value that opens with `[` but is not a valid JSON array falls back to
the comma rule, where it yields names that match nobody; it never reads
as "no group required". The same form is accepted wherever this server
reads a group list, including `HTTP_API_SITE_APP_<ID>_GROUPS`.

Web interface access and MCP access are **separate**. They were one knob,
so granting somebody the browser necessarily granted them MCP; a site can
now let its staff open the pages without also letting them drive the AP
through an agent:

```
HTTP_API_MCP_ACCESS_GROUP   = ap2001-login
HTTP_API_WEBUI_ACCESS_GROUP = ap2001-login, chtc_staff
HTTP_API_WEBUI_ADMIN_GROUP  = chtc_staff
HTTP_API_SUPERUSER_GROUP    = chtc_admin
```

`HTTP_API_WEBUI_ACCESS_GROUP` falls back to `HTTP_API_MCP_ACCESS_GROUP`
when unset, so a deployment that was using the MCP group to gate the
browser keeps that behaviour rather than being silently opened.

All of these are re-read on **`condor_reconfig` / SIGHUP** -- each is only
a membership test on a request, with nothing built from it. A login
already granted keeps its grant until it refreshes, at which point the
policy is re-run against the new lists, so tightening a group takes effect
on the next refresh rather than mid-request. One exception:
`HTTP_API_SUPERUSER_GROUP` can be *changed* or *emptied* live, but cannot
switch the feature **on**, because superuser mode builds its signing
identity at startup; the log says so if you try.

A browser refused by these gets an HTML page naming the groups to ask for,
rather than the API's JSON error body.

## Superuser mode

An administrator can act on another user's jobs *as that user*: remove,
hold, release, tail output, and ssh-to-job. Off unless configured.

| Config | Default | Effect |
| --- | --- | --- |
| `HTTP_API_SUPERUSER_GROUP` | *(unset)* | Group(s) whose members may use superuser mode. Unset disables the feature. |
| `HTTP_API_SUPERUSER_FALLBACK_IDENTITY` | `condor@$(UID_DOMAIN)` | Identity used when the operator is not themselves a usable queue superuser. Only needs setting where the schedd does not run as `condor`. |

Two things must both be true or the feature stays off, and the server logs
which one is missing:

- the group above is set, or [project leads](#project-leads) are configured, **and**
- a pool signing key is configured — the server mints the credential it acts
  under, so without a key there is nothing to act with.

`HTTP_API_SUPERUSER_GROUP` is deliberately **not**
`HTTP_API_WEBUI_ADMIN_GROUP`. That group means "may read the admin pages";
this one means "may act as anyone on this access point". Point it at your
web-admin group if you want them to match — the difference is that it is then
a decision rather than a side effect.

**Using it.** A permitted operator turns the mode on from the sidebar and
confirms. A red banner then appears on every page, cannot be dismissed, and
counts down; the mode turns itself off after 30 minutes, and a server restart
turns it off for everyone. Actions on the operator's *own* jobs are unaffected
and are not treated as impersonation. Membership is re-checked on every action:
an armed operator who has since left `HTTP_API_SUPERUSER_GROUP` (and leads no
project) gets a 403 on their next action, and the mode is turned off.

**Job applications are closed while the mode is on.** VS Code, JupyterLab and
anything else served through `/api/v1/jobs/{id}/proxy/` or
`/api/v1/jupyter/instances/{id}/proxy/` are served from this server's own
origin, so script the job sends back runs in the browser with the viewer's
session. While the session is armed -- as a superuser or as a project lead --
those routes, and `/api/v1/jobs/{id}/warm`, answer 403 for every job,
including the operator's own. Every request is checked, so turning the mode on
with an app already open refuses that app's next request and its next
WebSocket upgrade. A connection already established before the mode was
turned on is not closed, and script already loaded in an open app tab keeps
running: close app tabs before turning the mode on.

**Which identity acts.** If the operator is themselves listed in the schedd's
`QUEUE_SUPER_USERS`, the server authenticates as them, and the schedd's own
log records

```
QmgmtSetEffectiveOwner real=<operator> ... effective to <owner>
```

Otherwise it falls back to `condor@$(UID_DOMAIN)`, or to
`HTTP_API_SUPERUSER_FALLBACK_IDENTITY` where that is set. The default suits a
schedd running as the `condor` user, because HTCondor recognises the daemon's
own OS user as the condor identity — but only when it is not a personal
condor, where that check is disabled entirely. Point the knob at whoever the
pool runs as in that case.

Being listed in `QUEUE_SUPER_USERS` is necessary but not sufficient: the
schedd resolves the caller to a user record *first* and refuses one it cannot
resolve, so an operator who has never submitted a job cannot act. That is
checked when the mode is armed, and the operator is told to run
`condor_qusers -add <user>` rather than left with an action that silently does
nothing. The set is read from the
schedd with `DC_CONFIG_VAL` at startup and every 15 minutes — never per
action — so adding an operator to `QUEUE_SUPER_USERS` takes effect within one
refresh.

**What gets recorded.** Every action is logged with both identities. Hold,
release and remove additionally write the operator's name into the job's
reason attribute, which persists into history and is visible to the job's
owner:

```
Removed by alice@example.org via the web UI (superuser mode, acting for bob@example.org) (by user condor@example.org)
```

The trailing `(by user ...)` is appended by the schedd itself. When the
operator is a queue superuser it names them; when the fallback identity is
used it names `condor`, and the leading text is then the only record in the
job ad of which human acted.

**Bulk actions** are split by job owner and performed once per owner, each
under its own impersonation, so every job acted on belongs to the identity the
server authenticated as. A constraint spanning more than 25 owners is refused
rather than fanned out.

## Project leads

Superuser mode confined to one project's jobs. A *project* is the job ad's
`ProjectName`. A lead of project P may turn on the mode exactly as a superuser
does, and while it is on may act on other users' jobs whose `ProjectName` is P
-- and on no others. `HTTP_API_SUPERUSER_GROUP` does not need to be set.

| Config | Default | Effect |
| --- | --- | --- |
| `HTTP_API_PROJECT_LEADS_FILE` | *(unset)* | File naming each project's leads (format below). |
| `HTTP_API_PROJECT_LEADS_GROUP` | *(unset)* | Group-name pattern containing `{project}`: members of the group it names for P lead P. |

The file has one project per line, then its leads, separated by whitespace or
commas. A lead starting with `%` is a group (the sudoers convention); anything
else is a username. `#` starts a comment anywhere on a line, so a project or
lead whose name contains `#` cannot be configured.

```
# project     leads
Physics       alice, bob
CS101         %cs101-tas carol
```

With `HTTP_API_PROJECT_LEADS_GROUP = {project}-leads`, members of
`Physics-leads` lead `Physics`, with no file entry needed. Both sources can be
used together; a session leads the union. The pattern needs fixed text around
`{project}`; a bare `{project}` is refused.

> **The pattern trusts whoever names groups.** Anyone who can create a group
> called `<P>-leads` in your identity provider (or `HTTP_API_GROUP_SOURCE`)
> becomes a lead of project P. Where users can create their own groups, use
> the file instead.

Whether a job is in a project is decided by the schedd, evaluating
`ProjectName == "<project>"` against the whole job ad; ClassAd `==` ignores
case for ASCII letters only. A bare username (`bob`) matches that name in any
domain; one with a domain (`bob@other.org`) matches only that identity, which
includes a bare session name that becomes it once `UID_DOMAIN` is appended (as
after local identity mapping), and never the same name in another domain. Both
compare case-insensitively. Groups are the session's, from
`HTTP_API_GROUP_SOURCE`, matched case-insensitively. A project name containing
a quote, a backslash or a control character is ignored.

**What a lead can do**, while the mode is on: hold, release and remove another
user's job in a project they lead, tail its output, and ssh to it. Bulk hold
and release reach the lead's own jobs plus their projects' jobs, whatever the
constraint says. A job with no `ProjectName`, or one in another project, is
refused with a 403, and so is a job that does not exist (with the same
message, so a lead cannot probe for jobs) and a job owned by a queue superuser
or by the fallback identity. Queue superusers are matched by name without the
domain, against both the job's `Owner` and its `User`, so
`alice@other.org` in `QUEUE_SUPER_USERS` protects every job owned by `alice`;
with `QUEUE_SUPER_USERS` unset, the schedd's default of `root` and `condor`
counts. Until the server has read `QUEUE_SUPER_USERS` from the schedd at
least once, every action a lead takes on another user's job is refused ("the
schedd's queue superusers have not been read"); the read is retried every few
seconds until it succeeds, so this lasts only while the schedd is
unreachable. Each refusal is logged to the security log. ssh-to-job gives the
lead the member's sandbox, including any credentials in it. Hold and release
reasons name the project:

```
Held by alice@example.org via the web UI (project lead for Physics, acting for bob@example.org) (by user condor@example.org)
```

**What a lead cannot do:** open any job application while the mode is on,
the same as a superuser (see [Superuser mode](#superuser-mode)).

**Reading.** Without turning the mode on, a lead may list (and open) their own
jobs plus their projects' jobs by choosing *Everyone* on the jobs page, the way
an admin may list all jobs. The same scope covers the job's live status and its
match analysis, including `?source=archive` for a finished job. That read
scope needs no signing key. For now the job log, DAG, sandbox and output
panels stay owner-only for leads.

**Revoking.** Leadership is re-checked on every action, not when the mode was
turned on: removing a lead from the file takes effect within a few seconds, or
immediately on `condor_reconfig`, and a lead who loses all their projects has
the mode turned off on their next action. A job watch a lead opened on a
project's job is re-checked every minute and ends once they can no longer read
the job. A session that turned the mode on as
a lead stays project-scoped for that arm even if its user is later added to
`HTTP_API_SUPERUSER_GROUP`. Group membership is the session's as of login,
for leads as for `HTTP_API_SUPERUSER_GROUP`.

**Prerequisites** are superuser mode's: a pool signing key and, where the
schedd does not run as `condor`, `HTTP_API_SUPERUSER_FALLBACK_IDENTITY`. Leads
are normally not in `QUEUE_SUPER_USERS`, so they act through the fallback
identity, and the reason text and this server's audit log are the record of
which lead acted. An unreadable leads file is logged and grants nobody
anything; it does not stop the server. Both settings are re-read on
`condor_reconfig`, but like `HTTP_API_SUPERUSER_GROUP` they cannot switch
superuser mode on in a daemon that started with it off.

## Refresh-grant re-authorization

A refresh grant re-runs the authorization decision rather than
replaying it, so an entitlement someone loses stops applying without
waiting for their refresh token to lapse. Three knobs:

| Config | Default | Effect |
| --- | --- | --- |
| `HTTP_API_OAUTH2_MAX_GRANT_LIFETIME` | `720h` (30d) | How long a grant may go **unused** before it must be consented again. Measured from the grant's last use, not from consent: a client that refreshes inside the window keeps its grant indefinitely, which is what lets an unattended integration keep working. An abandoned grant still expires. What bounds a grant in active use is the revocation oracles below, which run on every refresh. Requires a unit suffix. |
| `HTTP_API_OAUTH2_REVOCATION_ORACLES` | `schedd-userrec` | Comma-separated oracle list consulted on each refresh. |
| `HTTP_API_OAUTH2_REFRESH_TOKEN_LIFESPAN` | `720h` (30d) | Unchanged: how long one refresh token lives. Every refresh resets it, which is why the cap above exists. |

Recognized oracles:

- `schedd-userrec` (default) — honors `condor_qusers -disable <user>
  -reason "..."`, revoking the grant and surfacing the reason. Reads
  the schedd's per-user record at READ authorization; disabling
  requires ADMINISTRATOR, so the API server can observe the decision
  without being able to make it. A user with **no** record is treated
  as unknown, not denied, because the schedd creates records lazily on
  first submit.
- `schedd-userrec-strict` — as above, but a user with no record is
  revoked. Correct only where every user is provisioned up front with
  `condor_qusers -add <user>`, which creates a record without the user
  submitting anything. On an ordinary pool this locks out every new
  user.
- `schedd-acl` — probes `ALLOW_READ` / `ALLOW_WRITE` with
  `DC_SEC_QUERY` and strips refused scopes. Never revokes outright: it
  cannot see identity, and reports nothing useful where those ACLs are
  wildcards. Costs up to two extra schedd round trips per refresh.
- `none` — disable all oracles. The grant lifetime cap still applies.
  This spelling exists because an unset config value and one set to
  blank are indistinguishable, and both mean "use the defaults"; there
  has to be a way to say "none" out loud.

Oracles fail open: a schedd that is unreachable, or that has no record
of the user, yields no opinion rather than a revocation, so a daemon
outage does not log everyone out.

Note the group list an oracle-free deployment relies on is captured at
consent, so re-running group policy catches an operator changing
`MCP_WRITE_GROUP` but not a user being removed from that group
upstream. Removals are caught by the oracles, by
`POST /api/v1/admin/oauth2/revoke`, and ultimately by the lifetime cap.

## This is an OAuth2 server, not an OpenID Connect provider

Worth stating plainly, because the endpoints look like they should be one and
the difference is only visible when something fails.

The server implements **RFC 8414** (OAuth2 authorization server metadata) at
`/.well-known/oauth-authorization-server`. It does **not** implement OpenID
Connect. Concretely:

- No `id_token` is ever issued, whatever scopes were granted.
- There is no `userinfo_endpoint`, and none is advertised.
- There is no `/.well-known/openid-configuration`; it answers as any unknown
  path does.
- No `jwks_uri` is published and no JWKS is served.

The `openid` scope is still **accepted**, and appears in `scopes_supported`.
It grants nothing — the consent screen says so — and it is accepted rather
than refused because a great many clients request it as a matter of course,
and rejecting it would fail their registration for no gain.

This was once the other way round: the server advertised `id_token` response
types and signing algorithms while publishing no `jwks_uri` and serving no
keys. A client that believed the advertisement had no way to verify what it
was handed, and authlib — which OpenWebUI uses — failed the whole login with
`Missing "jwks_uri" in metadata`. Nothing here ever consumed the `id_token`,
so the smaller advertisement is the honest one.

**If you need an identity endpoint**, for example to configure a Zapier app's
connection test, use `GET /api/v1/whoami`, which answers
`{"authenticated": true, "user": "..."}` for the bearer token presented.

Restoring OIDC means adding `compose.OpenIDConnectExplicitFactory` back to the
fosite composition **and** publishing a `jwks_uri` with the keys behind it, the
way the internal IDP does. Doing the first without the second is the state
described above.

## The discovery document is a promise

Every list in `/.well-known/oauth-authorization-server` is something a client
will go and do. The same mistake as the OIDC one above is easy to make in each
of them, and it was: a value sat in a list for years while the server refused
it.

**`code_challenge_methods_supported` is `S256` and nothing else.** `plain` was
advertised from the commit that first added this document, and fosite never
accepted it — `EnablePKCEPlainChallengeMethod` is left false in
`oauth2_provider.go`, which is the right setting. The cost was not a cosmetic
inaccuracy. fosite checks the challenge method while building the
authorization *response*, which happens after the consent screen, so a client
that believed the list signed the user in, showed them the approval page,
took their click, and only then redirected to

```
error=invalid_request
Clients must use code_challenge_method=S256, plain is not allowed.
```

**`token_endpoint_auth_methods_supported` always includes `none`.** That is
how a public client authenticates — it holds no secret, and PKCE binds the
code to it instead (`EnforcePKCEForPublicClients`). `none` used to be
advertised only when CIMD was enabled, as though CIMD were what made public
clients possible. It is one way to arrive as one, not the only way: a user
creates a public client with `POST /api/v1/integrations` and a `public` flag,
and DCR registers them too, both regardless of CIMD. With
`HTTP_API_MCP_CIMD = false` the document told a public client that its only
method was unsupported.

**`service_documentation` points at `/docs`**, the self-hosted API reference.
RFC 8414 has the field for exactly the person wiring up a third-party
integration, who needs `GET /api/v1/whoami` and otherwise has to be told where
to look.

**`resource_signing_alg_values_supported` is absent** from the
protected-resource document. RFC 9728 means by it the algorithms this resource
signs its own responses with; responses here are plain JSON and nothing signs
them. It claimed `RS256`, which is the internal IdP's ID-token key and has
nothing to do with this resource.

**The internal IdP advertises `code` alone.** It listed all seven
response-type combinations while `idp_provider.go` composes the two explicit
factories and no implicit or hybrid one, so the other six failed at the same
late stage — and it advertised no `code_challenge_methods_supported` at all
despite composing PKCE, leaving clients unable to discover it.

`oauth2_metadata_honesty_test.go` and `idp_metadata_honesty_test.go` hold
these together. They do not compare a document against a second copy of the
same constant: each reads the advertised list and then exercises the path, so
a value that is advertised has to survive the flow a client would actually
take.

## MCP OAuth2 clients

Clients of the MCP OAuth2 endpoint can register and authenticate several ways.
The admin UI at `/admin/clients` lists every client, creates one, and lets an
operator edit its permitted grant types (and, for `client_credentials`, its
service identity); other fields are read-only.

Every client records how it came to exist, in its `origin`: `dynamic`
registered itself, `seeded` was created by the server at startup, `admin` was
created by an operator, and `user` was created by a user as one of their own
integrations. That is not only a label for the list -- the sweep that deletes
unused clients acts on `dynamic` alone, because a client somebody created on
purpose has an owner who expects to find it where they left it, and "has
obtained no token yet" is the normal state of an integration that has been set
up but not yet signed in to.

- **Dynamic Client Registration (DCR, RFC 7591)** — `POST /oauth2/register`
  (also `/mcp/oauth2/register` wherever MCP is on).
  A client registers itself and gets a confidential `client_id` + secret. It
  may declare only the `authorization_code`, `refresh_token` and device-code
  grants and the `code` response type; anything else is refused with
  `invalid_client_metadata`. `client_credentials` and token exchange are
  enabled by an operator in the admin UI, never by self-registration.
  Registration is unauthenticated, so it is limited to 30 per hour from one
  address, and a registered client that has obtained no token within a week
  is deleted unless an operator has annotated it or changed its grants.
  `HTTP_API_MCP_DCR=false` turns registration off.
- **Client ID Metadata Document (CIMD)** — instead of registering, a client
  identifies itself by an `https://` URL as its `client_id`; the server fetches
  a client-metadata document from that URL and treats it as a **public** client
  (no secret, PKCE required). This is where the MCP spec is heading, away from
  DCR. The fetch is hardened against SSRF (https-only, private/link-local/cloud-
  metadata addresses refused, no redirects, timeout + size cap), and the
  document must be self-consistent (its `client_id` equals the URL). On by
  default; see the knobs below to disable or restrict it. **In production,
  set `HTTP_API_MCP_CIMD_ALLOWED_HOSTS`** to the hosts of the clients you
  expect: unset, any caller can make this server fetch from any public host
  (each source address is limited to 20 uncached lookups a minute).
- **Created by an operator** — `POST /api/v1/admin/oauth2/clients`, or the
  **Create client** form on `/admin/clients`. For the clients that cannot
  register themselves: `client_credentials` and token exchange act with no
  user present and are refused to a self-registration, so an operator
  provisions those here. Also for a service whose vendor wants an id and a
  secret pasted into a form rather than a discovery document. An operator may
  ask for anything the server implements. The secret is returned in the
  creation response and nowhere else.
- **Created by a user** — see the next section.
- **`client_credentials`** — a confidential client acting as itself, with no end
  user. Enable the grant in the admin UI and set a **service identity**: that
  identity becomes the subject of the short-lived HTCondor IDTOKEN minted for
  the client's requests, which the schedd then authorizes via `ALLOW_<LEVEL>`
  like any other principal. A client with no service identity configured is
  refused a `client_credentials` token rather than minting one for an ambiguous
  subject. Not available to public clients.

## A user's own integrations

A user connects an outside service to their own account from `/connectors`,
which mints an OAuth2 client they own (`POST /api/v1/integrations`). This is
for a service that asks for a client ID and secret to be pasted into a form;
anything that speaks dynamic registration does not need one.

The direction is worth stating, because the page deliberately carries both
senses of "connector" and they are opposites. An app connected here holds a
credential that lets it act **as the user, on this access point**. A connector
in the other sense is access the user grants **this server, on their account
somewhere else**.

What a user may mint for themselves is bounded twice, and neither bound is the
consent screen:

- **Grant types** are restricted to the flows with a person on the other end
  (`authorization_code`, `refresh_token`, device code) -- the same set
  self-registration allows. `client_credentials` and token exchange act with
  nobody present, which is a decision about the deployment rather than about
  one user's jobs, so an operator provisions those.
- **Scopes** are intersected with what the creator could consent to at the
  time, so a client cannot be *registered* for a scope its creator has no
  claim to. Consent narrows it again at authorization; this keeps the list a
  client advertises from being an aspiration. `offline_access` is carried
  through regardless -- it is not a permission over anything, and without it
  no refresh token is ever issued and an unattended integration stops working
  at the first token expiry.

Redirect URIs must be `https`, or `http` on loopback: an operator's client may
use a custom scheme, a user's may not. A user holds at most 20 integrations.
Deleting one removes every token issued under it, so whatever was using it
stops immediately.

These endpoints are authenticated by the **browser session only** and are not
reachable with a bearer token. A client able to mint clients could widen its
own reach without anybody consenting to the wider one, and a token stolen from
an integration would be enough to mint a replacement for itself. Minting a
credential is something a person does while signed in.

## Token exchange (RFC 8693)

The MCP OAuth2 endpoint supports the token-exchange grant
(`urn:ietf:params:oauth:grant-type:token-exchange`), so a gateway or agent can
trade one token for another rather than re-running an interactive flow. It is
**opt-in per client**: enable the `token_exchange` grant on a confidential
client in the admin UI (`/admin/clients`). Exchange is always **delegation** —
the issued token acts as the subject but records the exchanging client as its
actor — and **scope-down only**: the result can never exceed the subject's
authorization.

Two kinds of `subject_token` are accepted:

- **A token this server issued** (`subject_token_type` =
  `urn:ietf:params:oauth:token-type:access_token`): the result acts as that
  token's subject, bounded by that token's granted scopes. No configuration
  needed. The subject's grant is re-checked first exactly as a refresh would
  be (lifetime cap, group policy, revocation oracles), and the result stays
  bound to it: revoking or narrowing that grant, or its ending, applies to
  the exchanged token too, and the exchanged token expires no later than the
  grant's lifetime cap. A token that was itself obtained by exchange cannot
  be exchanged again.
- **A JWT from a trusted external issuer** (`subject_token_type` =
  `urn:ietf:params:oauth:token-type:jwt` or `…:id_token`): accepted only when
  the issuer is listed in `HTTP_API_MCP_TOKEN_EXCHANGE_ISSUERS`. The token's
  signature is verified against the issuer's JWKS (RS256/ES256), and its
  `iss`/`aud`/`exp`/`nbf` are checked. The local identity is namespaced as
  `<sub>@<identity_domain>` (so two issuers cannot collide), and the result is
  bounded by the issuer's `allowed_scopes` **and** the exchanging client's own
  scopes, then by the same group policy and revocation oracles a login gets
  (with `HTTP_API_GROUP_SOURCE=system`, groups come from the mapped account,
  not the token).

`HTTP_API_MCP_TOKEN_EXCHANGE_ISSUERS` is a JSON array; unset disables external
exchange (the our-own-token path still works). Each entry:

```
HTTP_API_MCP_TOKEN_EXCHANGE_ISSUERS = [ \
  {"issuer":"https://idp.example.org", \
   "jwks_uri":"https://idp.example.org/.well-known/jwks.json", \
   "audience":"htcondor-mcp", \
   "identity_domain":"idp.example.org", \
   "allowed_scopes":["condor:/READ","mcp:read"]} ]
```

| Field | Meaning |
| --- | --- |
| `issuer` | Exact `iss` the token must carry. Required. |
| `jwks_uri` | HTTPS URL of the issuer's signing keys (fetched with SSRF protections, cached). Required. |
| `audience` | Value the token's `aud` must include. Required. |
| `identity_domain` | Local identity is `<sub>@this`. Defaults to the issuer's host. |
| `allowed_scopes` | Ceiling of scopes a token from this issuer may obtain. |

## Requests from other sites

Every state-changing request -- anything that is not GET, HEAD, OPTIONS or
TRACE -- must come from this site. If it carries an `Origin` header naming
somewhere else, it is refused with 403 before it reaches a route.

A request with no `Origin` is allowed: `curl`, scripts and the MCP stdio
client do not send one, and a browser cannot suppress it on a cross-site
request. A request carrying an `Authorization` header is also allowed,
because a browser does not attach one by itself -- a request that has one
was built by code that already held the credential, which is not the
situation this is about. A forged header simply fails to authenticate.

**Why it is not just cookies.** Session cookies are `SameSite=Lax`, which
already blocks a cross-site POST, so cookie deployments were never
exposed. But `SameSite` governs cookies, and `HTTP_API_USER_HEADER`
authenticates with a header a trusted proxy attaches to *every* request a
browser makes through it, cross-site included. In that mode, without this
check, a page on any site could submit, hold or remove a reader's jobs.

`HTTP_API_BASE_URL` is accepted alongside the request's own `Host`, so a
deployment whose SPA is served from a different origin keeps working.
