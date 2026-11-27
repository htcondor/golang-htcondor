---
title: Connected accounts
audience: operator
order: 75
---

# Connected accounts

A **connector** lets a user grant this access point access to a
third-party account of theirs, so a job can read their files out of it
— and so a submission can write its results back. Part of the
[HTTP API + MCP server](index.md) documentation; the user's side is
[Connecting an account](../user/connectors.md).

Nothing here is on by default. An access point with no
`HTTP_API_CONNECTORS` offers nothing and stores nothing.

## What is built, and what is not

The design distinguishes three bootstraps by **who holds the OAuth2
client registration**, and only one of them exists.

| `KIND` | State |
| --- | --- |
| `oauth2` | **Built.** This server is the OAuth2 client: it runs the authorization-code flow, stores a per-user refresh token, and renews. |
| `credd` | **Not built.** Configuring one answers 501 naming the kind, rather than redirecting nowhere. |
| `vault` | **Not built.** Same. |

`design_notes/CONNECTORS.md` has the analysis of all three, and is
worth reading before building either of the other two: they look like
one mechanism from outside and are not.

The rest of this page is about `oauth2`, which is the only kind you can
configure today.

## The shipped catalogue

Four providers ship with their endpoints already filled in, taken from
HTCondor's own parameter defaults so the two cannot disagree:

| Id | Default scopes | Transfer plugin |
| --- | --- | --- |
| `builtin:gdrive` | `drive.file` — per-file, granted by the user picking a file | `gdrive` |
| `builtin:box` | — | `box` |
| `builtin:onedrive` | — | `onedrive` |
| `builtin:dropbox` | — | none; spool only |

A connector id is `<namespace>:<name>`. `builtin:` names one of the
above; `site:` is your own registration against a provider, which needs
its `_AUTHORIZATION_URL` and `_TOKEN_URL` given explicitly, or a `_KIND`
naming a shipped definition to borrow them from.

`builtin:gdrive` and `site:gdrive` are deliberately different
connectors with different credentials: the namespace is part of the
credd service name, so two registrations against one provider cannot
land on the same stored credential.

## Configuring one

```
HTTP_API_CONNECTORS = builtin:gdrive

HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_ID          = ....apps.googleusercontent.com
HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_SECRET_FILE = /etc/condor/gdrive-secret
HTTP_API_CONNECTOR_BUILTIN_GDRIVE_GROUPS             = smith_lab, jones_lab
```

The full text of every setting is in
[Configuration](config.md#http_api_connector_instance_). Four points
that are easy to get wrong:

- **`_GROUPS` unset means nobody, not everybody.** A connector with no
  groups cannot be connected by anyone, exactly as a site app with no
  groups is not published — an empty value is far more often a mistake
  than a deliberate "nobody". Write `*` if you mean every signed-in
  user. The admin test reports an empty list as a problem, because a
  connector nobody can reach looks identical to a broken one from
  outside.

  (The configuration reference said the opposite of this until it was
  checked against the code while writing this page. If you configured
  a connector against that advice, it has been offered to nobody.)
- **A connector whose client secret cannot be read is still listed.**
  It fails when somebody clicks Connect, with a 503 naming it. So a
  secret the daemon cannot read produces a broken button rather than a
  missing one — check a new connector with the admin **test** verb
  rather than waiting for a user to find it. (The configuration
  reference claimed the opposite until this page was written.)

  The secret may come from the file this setting names or from one
  stored through the admin pages; the file wins where both exist, and
  either is re-read on each use, so rotating a secret at an unchanged
  path needs no restart.
- **`_ENABLED` defaults to true.** Turn a connector off with
  `_ENABLED = false` rather than by deleting its configuration:
  deleting it leaves the stored grants behind with nothing to name
  them.
- **Group membership is re-read on every use**, never snapshotted when
  the account was connected. A snapshot would catch you changing the
  policy and miss a user leaving the group, which is the half that
  matters for taking access away.
- **The client registration is yours, not the project's.** You register
  an OAuth2 client with the provider for *this* access point, with this
  access point's callback URL. A registration copied from somewhere
  else will fail at the redirect.

## Where the credentials live

Refresh tokens are stored in the server's application database, in
`connector_credentials`, keyed by user and connector.

**Seal them.** With `HTTP_API_KEK_FILE` set, stored values are
encrypted under that key; without it they sit in the database in
plaintext, as every other secret on such a deployment does, and
startup says so. A database holding users' Google refresh tokens is
worth the one `openssl rand -hex 32`.

`HTTP_API_CONNECTOR_PREFIX` (default `webapi`) is the prefix of the
credd service names — the `webapi-builtin-gdrive` in a submit file's
`webapi-builtin-gdrive+gdrive://...`. Change it only to keep two
servers sharing one credd from seeing each other's credentials;
changing it on a running deployment invalidates every submit file that
spells the old name.

## How a job gets a file

The submitter writes one thing and the access point decides what
happens to it:

```
transfer_input_files = webapi-builtin-gdrive+gdrive://1A2b3C4d/data.csv
```

`<service>+<method>://<file-id>/<name>`. The scheme names the provider's
protocol, the part before `+` names the credential, and the last path
component is the file's name in the sandbox — which is where HTCondor
takes it from.

`_TRANSFER` chooses what the access point does with that:

- **`spool`** — the default, and the one to use. This server fetches
  the file and rewrites the reference to a bare name, so **no
  third-party token reaches an execute node**. Needs nothing deployed
  anywhere.
- **`direct`** — the credential goes to the credd and the transfer
  plugin on the execute node fetches. Avoids moving bytes twice, at the
  cost of the user's token sitting on an execute machine. No plugin
  ships for any builtin today, and the admin usability check reports
  that as a problem rather than letting it fail at run time.

Because the submitter writes the same URL either way, a submit file
moves between modes without being rewritten.

The reference is recorded on the job ad as `ConnectorInputs`, so
`condor_q -af ConnectorInputs` says what a job pulled. That is
deliberate: it is durable, it survives a restart of this server, and it
is visible, where a private table here would be none of those.

## File choosers

A chooser lets a user pick a file out of their connected account on the
submit page, rather than typing a file id — and for Drive, picking the
file is *what grants access to it*, since `drive.file` is per-file.

```
HTTP_API_PICKERS = gdrive
HTTP_API_PICKER_GDRIVE_CONNECTOR = builtin:gdrive
HTTP_API_PICKER_GDRIVE_API_KEY   = ...
HTTP_API_PICKER_GDRIVE_APP_ID    = ...
```

A chooser with no `_CONNECTOR` is not offered: it has no credential to
show files with.

## Results into a spreadsheet

A submission may nominate a spreadsheet; each job's result file is
appended to a tab as it finishes. This needs
`HTTP_API_CONNECTOR_OUTPUT_SWEEP_INTERVAL` set — unset leaves the sweep
off and the feature unavailable — and it is **not available in multi-AP
mode**.

The interval is read at startup and the running ticker does not re-read
it, so a change needs a restart even though the rest of the connector
configuration reloads. Anything under a minute is raised to one with a
message: sweeping faster asks the queue and the provider about the same
finished jobs more often than they can change, and a provider's rate
limit is a real consequence.

## Administering one

The admin pages under `/api/v1/admin/connectors` carry the catalogue,
per-connector configuration, the users who have connected each one, and
two verbs worth distinguishing:

- **test** reports whether a connector is usable and why not: no client
  id, no authorization or token endpoint, no groups (*"so nobody may
  use it"* — a connector nobody can reach looks identical to a broken
  one from outside), an unreadable client secret, or direct transfer
  with no plugin.
- **revoke** forgets *every* user's grant on a connector while leaving
  the connector configured. **This is what to do when a client secret
  leaks or is rotated**: the stored refresh tokens were issued to the
  old registration and every user has to consent again. It is a
  different verb from deleting the connector on purpose.

Note what revoking does not do. It forgets this server's copy; it does
not revoke anything at the provider. After a leak, the provider's own
revocation is the half that matters, and it is not reachable from here.

## What a user can and cannot do

Worth stating because it bounds the blast radius:

- The connect endpoints accept **only a browser session cookie** —
  never a bearer token or an API key. A browser is the only thing that
  can complete the provider's flow, and accepting a token would hand a
  user's third-party credential to whatever held it.
- **The connector id is in the path; the user never is.** There is no
  parameter that could name somebody else, which is what keeps this
  from being a privilege-escalation surface.
- The connect entry point **carries no authority**. A fixed
  per-connector URL yields a login redirect without a session, so it is
  safe to print in a tool result, a log or this page.
- A user outside a connector's groups is told it is *not available to
  them*, and not who it is available to.
- A user can disconnect their own grant at any time, idempotently. Tell
  them to revoke at the provider too; that half is theirs and this
  server cannot do it for them.
