---
title: SSH gateway
audience: operator
order: 70
---

# SSH gateway

An SSH port on the API server that authenticates with the OAuth2 device
flow and drops the caller into an HTCondor job — so users get an
interactive terminal without an account on the access point. Part of the
[HTTP API + MCP server](index.md) documentation.

## Turning it on

```
HTTP_API_SSH_GATEWAY_ADDRESS = :2222
HTTP_API_SSH_HOST_KEY_FILE = /etc/condor/htcondor-api/ssh_host_key
```

The gateway does not start where the site allows nothing to reach into
a running job ([`HTTP_API_SSH_TO_JOB =
false`](interactive.md#sites-that-forbid-condor_ssh_to_job)) --
a listener that accepts connections only to refuse them is worse than
no listener, because it answers on the port and so looks configured.
The address can stay set; it is logged and ignored.

A user connects with a stock `ssh` and sees:

```
$ ssh 12345.0@ap.example.edu -p 2222
Approve at https://ap.example.edu/mcp/oauth2/device/verify?user_code=WDJB-MJHT (code WDJB-MJHT), then press Enter:
```

Approve in the browser, press Enter, and the session opens. Clients that
render the longer RFC 4256 instruction show the URL and code on their own
lines above that prompt; OpenSSH on Linux shows only the prompt, which is why
everything needed to act is repeated there.

No client configuration, no key to distribute: the prompt is an ordinary
RFC 4256 keyboard-interactive challenge, which every SSH client already
renders. Approving in the browser lets the session continue by itself.

## What the username selects

The username carries no identity — the OAuth2 grant does — so it names the
target instead:

| `ssh <this>@gateway` | reaches |
| --- | --- |
| `12345.0`, or `12345` | that job, proc 0 if omitted |
| `+work` | your interactive session called `work`, **started if you have none** |
| `+jobs` | the **file service**: your finished jobs' output over SFTP |
| anything else, including a bare `ssh gateway` | your **default** session |

The `+` is required to name a session, and a bare `ssh gateway` reaches the
same `default` session from every machine you own. That is the point: the
username is whatever your local machine calls you, and your laptop login has
nothing to do with your HTCondor session — reading it as one would give you a
different session from a laptop, a login node and a container.

`+` is used because a POSIX username cannot contain it, so no local login can
be mistaken for an explicit request. A word like `session-` would need an
escape hatch for the account actually called `session-manager`; this needs
none.

It also reaches a session whose name looks like a job id, which is otherwise
unreachable because job ids are tried first: `ssh +12345.0@gateway`.

`+jobs` is **reserved**: a session cannot be created with that name, so the
service can never shadow somebody's shell. The reservation is
case-insensitive, and it covers every spelling that would derive the same name
— `+-jobs` reaches the service rather than asking for a session that cannot
exist. A bare `ssh jobs@gateway` is unaffected and still means your default
session, because the prefix is what makes a request explicit.

While a newly created session waits in the queue, the terminal shows what it is
waiting for and how long it has been waiting. Ctrl-C stops waiting; it does
**not** remove the job, so reconnecting picks the session up once it starts.

## The file service

`ssh +jobs@gateway` is not a shell — it serves your finished jobs' output over
SFTP, and takes work in the same way, so any client you already have reaches
it:

```sh
sftp -P 2222 +jobs@ap.example.edu
# sftp> ls -lh outputs                       # your jobs; reads nothing
# sftp> get outputs/12345.0/job.ad           # what the job was and did
# sftp> get -r outputs/12345.0/data          # its output
rsync -av -e 'ssh -p 2222' +jobs@ap.example.edu:outputs/12345.0/data/ ./results/
```

The tree is small:

| Path | What it is |
| --- | --- |
| `outputs/` | one directory per completed job still in the queue (see below) |
| `outputs/12345.0/` | that job: its ad and its output |
| `outputs/12345.0/job.ad` | the job's ClassAd, as `condor_q -l` would print it |
| `outputs/12345.0/data/` | its output sandbox, as files |
| `outputs/12345.0/data.tar` | the same sandbox as one archive |
| `outputs/UNAVAILABLE.txt` | present only when the listing cannot be built, and says why |
| `templates/` | submit descriptions to drop a CSV on (see [Job templates](#job-templates)) |
| `submit/` | drop a tarball holding a `job.sub` (see [Submitting a job of your own](#submitting-a-job-of-your-own)) |

**The output sits one level down, under `data/`, and that is what keeps a
listing cheap.** A spooled sandbox can only be read whole, so putting a size
next to a file's name costs a transfer of the entire sandbox. With the files
under `data/`, a job directory holds a directory (no size) and an ad (one
query) — so `ls -lh outputs` and `ls -lh outputs/12345.0` are both cheap, and
the one unavoidable pass over the sandbox happens when you ask for the output,
which is when you expect to wait.

`data.tar` is reachable by name but does **not** appear in a listing, on
purpose twice over. Listing it would mean knowing its length, which needs the
job's file list — the very transfer the level above avoids. And listed beside
`data/`, `get -r` on the job directory would fetch every file twice, once
loose and once inside the archive.

`job.ad` is the job's ClassAd, rendered the way HTCondor writes one: one
`Attr = value` per line, sorted, which is the format the starter writes into
the execute directory as `.job.ad` and the one `condor_q -l` prints. It
answers what output alone cannot — how much memory the job used, what it
exited with, when and where it ran, what it was told to run, and which
template or dropped archive it came from. It exposes nothing the REST API
does not: the same scoped mirror read serves whole ads to `/api/v1/jobs`. A
job submitted with an `Idempotency-Key` carries it here as `IdempotencyKey`,
which is how you find out which key a job already used.

It needs the htcondordb mirror, like the `outputs/` listing itself. Without
one a job directory holds its output and no ad.

Asking for a shell here gets an explanation rather than a bare failure. The
surface is read-only; writes are refused with a reason.

`outputs/` lists your **completed jobs that are still in the queue**, read from
the [htcondordb mirror](config.md). That is not a subset of what you
might expect — it is the whole set that can be served. Output is read with
`TRANSFER_DATA`, which matches against the queue, so once a job leaves the
queue its spool is gone and there is nothing left to transfer. A completed job
in the history is correctly absent, because asking for its files would fail.

`LeaveJobInQueue` is therefore not an obstacle here but the window: it is
exactly how long output stays retrievable.

The listing needs the mirror, and says so rather than showing an empty
directory when it cannot be built — "you have no jobs" and "this access point
cannot tell you" are different answers. It says so in a file: `outputs/` then
holds nothing but `UNAVAILABLE.txt`, which gives the reason and what still
works. That is not decoration. A failed `readdir` reaches an OpenSSH client as
`remote readdir("/outputs/"): Failure` and nothing else, because `sftp(1)`
renders the status code and discards the server's message, so a file is the
only way the explanation arrives. The same reason is logged at warning level
for whoever runs the access point.

The listing is also served only once the schedd has accepted you at READ, the
level of the query the mirror stands in for; that check is cached, so it costs
one handshake per interval rather than one per `ls`. A job is still reachable
by naming it either way, including while the listing is unavailable — the
listing is the convenience, and what keeps you to your own jobs is the scope
of the sandbox read rather than the listing.

### What it costs

A spooled sandbox can only be read whole — there is no ranged read, and a
file's size arrives just ahead of its bytes. So the first time you enter a job
directory, the whole sandbox is transferred once: that pass both learns the
file list and fills the cache, after which every read of that job is local.
Without `HTTP_API_SANDBOX_CACHE_DIR` there is no cache, and each file read
transfers the sandbox again.

That one unavoidable pass is what `HTTP_API_SANDBOX_MAX_GB` protects. A job
over the limit is refused before any byte moves, because the total size is
advertised before the first file.

A download that streams — an archive, or any read without a cache — must be
read **forwards**. A client that seeks backwards more than a few megabytes is
told so rather than quietly triggering a second full transfer. Ordinary
clients read forwards.

## Job templates

Where this access point offers job templates, the file service offers them
too: drop a CSV into one and it becomes a cluster of jobs, one per row.

These are the same templates the web **Submit** page and
`/api/v1/templates` offer — built-in ones, whatever
`HTTP_API_TEMPLATE_GLOBAL_PATH` adds, and the ones users save for
themselves. Nothing is configured separately for the file service, and a
template authored once is offered everywhere. An access point with the
templates feature disabled has no `templates/` directory here either.

```sh
sftp -P 2222 +jobs@ap.example.edu
# sftp> cd templates/blast
# sftp> get README              # what the columns are
# sftp> put runs.csv submit/    # one job per row
# sftp> get status/runs.csv     # what happened
```

| Path | What it is |
| --- | --- |
| `templates/<id>/` | one directory per template you can use, named by its template id |
| `.../job.sub` | the submit description |
| `.../README` | generated: the template's name, description, columns and any files it ships |
| `.../submit/` | drop a CSV here; write-only |
| `.../status/` | one entry per upload |

Which templates appear is the library's answer, not this service's: the
built-ins, the operator's catalog, your own saved templates, and any another
user shared. Somebody else's private template is not listed and is not
reachable by naming it.

**The CSV's header names the template's columns.** A template whose columns
are `sample` and `reads` — the same `$(sample)` and `$(reads)` its body
expands — is driven by a CSV headed `sample,reads`, in any order. A header
that misspells a column, omits one or adds one is refused naming the
difference, because the alternative is a submission in which `$(sample)`
expands to nothing: the right number of jobs, doing the wrong work. The
`README` lists the columns, so there is no separate mapping file to keep in
step.

The upload is submitted when your client closes the file cleanly. An
interrupted transfer is recorded as `incomplete` and submits nothing, and the
same bytes dropped twice within an hour are one submission — so a retry does
not double your jobs, whether or not it reconnects first. After that hour the
same file is a new submission, because deliberately running the same work
again is your decision rather than the server's.

Every outcome, including every refusal, is written to `status/`, because the
one error string a close reply carries is rendered by most clients as "could
not close file" and nothing more. Those entries outlive your connection, which
matters for exactly the case that produces them: `sftp -b` and `scp` close the
channel when the command ends, so the answer is read by a later session. They
are held in memory for a week, so a restart of the API server forgets them;
the durable record of what was submitted is the cluster itself, which carries
the template and the content digest as job attributes.

Values are refused rather than altered: a value containing `$`, `))`, a comma,
a line break, or a row that is blank or starts with `#` is rejected naming the
row and the column. Each of those would otherwise make the submit parser do
something other than what the spreadsheet says.

### Which templates can be used here

A template is shared with the web submit page, which can do things this
surface cannot, so a template that works there may be refused here. The
`README` says so, and so does `status/` if you drop a file anyway. These
requirements are enforced before an upload is accepted:

- **Columns are required.** The CSV's header names them and its rows fill
  them. A template with none has nothing for a CSV to vary; the submit page's
  answer for that case — "run N copies" — is not something a dropped file
  expresses.
- **No `queue` statement.** The CSV's columns supply one; two would submit the
  template's own idea of the work alongside the CSV's. The submit page appends
  one too, so a template in the library should not carry one anyway.
- **`transfer_executable = false` is required.** Output is read from the
  schedd's spool, so the submission spools — which is also what keeps the job
  in the queue long enough to fetch. But a spooling job whose executable needs
  transferring waits on `HoldReasonCode 16` for an upload a CSV never carries,
  and waits forever. Put the executable on the execute node or in a container.
- **The template's input files and the files its body asks for must be the
  same set.** A template may ship files with it — a wrapper script, a small
  config — which the library stores alongside the submit description. Those
  are delivered: they are spooled to the schedd, from memory, in the same
  call that releases the jobs from their spooling hold. (The web submit page
  delivers them with a second call, to `/api/v1/jobs/{id}/input`; a dropped
  CSV has no second call.) Each one therefore has to be a name the body asks
  for in `transfer_input_files`, and each name the body asks for has to be
  one the template ships. A file nothing asks for is dropped silently on its
  way to the schedd and the job runs without it; a name nothing ships leaves
  the jobs waiting on `HoldReasonCode 16` for an upload that is not coming.

A **URL** in `transfer_input_files` is outside that set and needs no delivery:
the execute node fetches it. That is how a template delivers input too large
to ship with it — an `https://` or `osdf://` path, or an object store. A
plain path is not: `transfer_input_files` means "upload this from the
submitting machine", and here there is no submitting machine to upload from.

A column can name an input too — `transfer_input_files = $(reads)` — and then
what each row names decides. A row naming a URL is fine; a row naming a plain
file cannot be delivered, and because that is only knowable once the row is
filled in, the whole cluster is **withdrawn** rather than left holding. The
refusal in `status/` names the row's file and says the jobs were removed.

How much a CSV may spool is bounded: the template's files are sent once per
row, so `HTTP_API_SPOOL_MAX_UPLOAD_BYTES` (1000 MiB by default, the same
ceiling the REST upload endpoints answer to) applies to the files times the
rows. Over it, the submission is refused before anything is submitted, naming
the total.

Every job carries `SFTPJobTemplate = "<id>"`, so `condor_q -af
SFTPJobTemplate` says which template a cluster came from.

## Submitting a job of your own

A template answers "run this known work over my data". `/submit/` answers the
other half: you have a submit description and the files it names, and you want
them run.

Put a submit description called `job.sub`, and every file it needs, in one
archive, and drop the archive in:

```sh
sftp -P 2222 +jobs@ap.example.edu
# sftp> get /submit/README           # the contract, in full
# sftp> put run.tar.gz /submit/
# sftp> get /submit/status/run.tar.gz
```

| Path | What it is |
| --- | --- |
| `submit/` | drop an archive here; write-only |
| `submit/README` | the contract |
| `submit/status/` | one entry per archive |

`job.sub` must be at the **top level** of the archive — `tar czf run.tar.gz
job.sub run.sh`, not `tar czf run.tar.gz mydir`. Plain and gzipped tar are
both accepted, decided by the file's first bytes rather than its name. One
archive is the unit: a submit description is useless without the files it
names, so there is no way to upload them separately and no half-submission to
wonder about.

This is **more** than a template can do, in the way that matters most: the
archive can carry the **executable**, so `transfer_executable` may be true and
the job can run a program of yours. A dropped CSV carries no files, which is
why a template cannot.

Every file the submit description transfers has to be in the archive under the
name it asks for. A name the jobs ask for that the archive does not carry is
**refused** rather than submitted — HTCondor's tar spool drops an unexpected
name in silence, so the jobs would leave their hold and fail at run time on a
missing file, which looks like a success at every step except the job's.
Anything the archive carries that the submit description does not ask for is
ignored, so packing a whole directory is fine. A URL in
`transfer_input_files` is fetched by the execute node and does not belong in
the archive.

`queue N` works, and so does the inline item form:

```
queue sample from ((
HG001
HG002
))
```

The archive is spooled **once per proc** — HTCondor spools input per job — so
`HTTP_API_SPOOL_MAX_UPLOAD_BYTES` bounds the archive times the procs, exactly
as it does for the REST upload endpoints. A submission needing more procs than
one upload may serve is refused with its jobs withdrawn rather than left
half-spooled.

Your submit description is parsed **without access to the access point**, the
same as one posted to `/api/v1/jobs`. `include`, `include command`, `$ENV()`
and `queue ... from <file>` therefore do not work — the first two would run as
the API server, and the last would read its filesystem. Use the inline item
form above.

The submission happens when your client closes the file, so an interrupted
transfer submits nothing and the same archive dropped twice is one submission.
Nothing is submitted until the archive is complete and checked, and if the
spool fails after the jobs exist they are withdrawn: there is no outcome where
jobs sit waiting for input that is not coming.

### What it costs the access point

With `HTTP_API_SPOOL_BUFFER_DIR` set, an archive is buffered to a file there
and nothing but the write in flight is held in memory; the ceiling is
`HTTP_API_SPOOL_MAX_UPLOAD_BYTES`. **Without** it the archive is held in
memory, and the ceiling is the much smaller limit the file service already
applies to an upload. Set the buffer directory if you want people submitting
archives of any size.

`/submit/` needs no configuration of its own and is there whenever the file
service is: a dropped archive carries its own submit description, so unlike
`templates/` there is no catalog to enable.

## The approval screen

Where the web UI is built into the binary, the approval link opens a screen
for the workspace the `ssh` command asked for rather than a generic "allow
this device" page. The `ssh` client sends the workspace name along with the
device authorization, and the verification endpoint redirects to
`/ssh/approve`.

If you already have a workspace by that name, the screen says what it is
doing — queued, running, or held and why — and offers the sign-in alone.
If you do not, it shows the same resource form as the **Interactive** page
(CPUs, memory, disk, GPUs, extra submit lines), opened on what
`HTTP_API_SSH_GATEWAY_SESSION_*` would have submitted. One button creates the
workspace **and** approves the sign-in. Nothing is submitted if you refuse,
and nothing is approved if the submission fails.

The browser submits the job, as you, before the approval is recorded, so the
gateway finds the workspace waiting and attaches to it. Creating on demand
from the gateway remains the fallback for a deployment with no web UI and for
an approval that created nothing; it cannot produce a second job, because the
first one to submit a given name wins and the other attaches to it.

The code is shown large at the top of the screen. It is the check that
catches a login somebody else started: a code that matches nothing in your
terminal is not yours to approve.

A deployment without the web UI keeps the server-rendered consent page, and so
does every device code that is not an SSH login.

## Host key and CA key

Both are long-lived and both are dangerous to lose: replacing a host key trips
`StrictHostKeyChecking` for every user at once, and replacing the CA key
invalidates every certificate it signed. They come from, in order:

1. `HTTP_API_SSH_HOST_KEY_FILE` / `HTTP_API_SSH_CA_KEY_FILE` — a path to an
   OpenSSH private key you staged (`ssh-keygen -t ed25519 -N '' -f <path>`).
   World-readable is refused; group-readable is fine, because a kubelet
   `fsGroup` mount turns 0400 into 0440. Passphrase-protected keys are refused
   with an error saying how to strip it.
2. Otherwise the daemon generates one and keeps it **sealed in the application
   database**, which requires `HTTP_API_KEK_FILE`.

There is deliberately no setting carrying the key bytes themselves:
`/proc/<pid>/environ` and crash dumps both leak the environment, and HTCondor
configuration is public to anyone who can run `condor_config_val`.

A stored key that cannot be decrypted is a **startup error**, never silently
replaced — the usual cause is a swapped `HTTP_API_KEK_FILE`, which is
recoverable, while a new host key is indistinguishable from an attack. Enabling
the gateway with neither a key file nor a KEK is also a startup error rather
than a port that quietly is not there.

**Running more than one replica?** Use the key file. A database-minted key
belongs to one database, so replicas would present different host keys.

## Knobs

| Knob | Purpose |
| --- | --- |
| `HTTP_API_SSH_GATEWAY_ADDRESS` | Where to listen, e.g. `:2222`. Empty disables the gateway. |
| `HTTP_API_SSH_GATEWAY_ISSUER` | OAuth2 issuer the device flow runs against. Defaults to the server's own issuer; set it when this process cannot reach its own public URL. |
| `HTTP_API_SSH_HOST_KEY_FILE` | Host key clients pin. Generated and sealed in the DB when unset. |
| `HTTP_API_SSH_CA_KEY_FILE` | CA key for signing user certificates. Same treatment; without it certificates are unavailable and the device flow still works. |
| `HTTP_API_SSH_GATEWAY_HOST` | The name(s) users actually `ssh` to, e.g. `ap.example.edu`, comma-separated. It is what the web UI prints as the `ssh` command, **and** what the host certificate and published `known_hosts` line are narrowed to — so list every name and alias in use. It cannot be derived, because the listen address is usually not what anybody types: a container listening on `:2222` sits behind a service publishing 22 on another address. Unset means no command is printed, and host verification falls back to a `*` pattern. |
| `HTTP_API_SSH_GATEWAY_LOCKOUT_THRESHOLD` | Failed logins one host may accumulate within the window before it is locked out. Default 10. |
| `HTTP_API_SSH_GATEWAY_LOCKOUT_NET_THRESHOLD` | The same budget for the surrounding network, shared by every host in it. Defaults to four times the host threshold. |
| `HTTP_API_SSH_GATEWAY_LOCKOUT_WINDOW` | How long failures are remembered. Needs a unit: `10m`. |
| `HTTP_API_SSH_GATEWAY_LOCKOUT_TIME` | How long the first lockout lasts; each subsequent one doubles. Default `15m`. |
| `HTTP_API_SSH_GATEWAY_LOCKOUT_MAX_TIME` | Where the doubling stops. Default `24h`. |
| `HTTP_API_SSH_GATEWAY_LOCKOUT_TRUSTED` | Addresses and CIDR blocks never counted and never locked out, separated by commas or spaces. A typo here is a startup error. |
| `HTTP_API_SSH_GATEWAY_LOCKOUT_DISABLE` | `true` turns the lockout off. |
| `HTTP_API_SSH_GATEWAY_SESSION_CPUS` | CPUs a session created on demand requests. Default 1. |
| `HTTP_API_SSH_GATEWAY_SESSION_MEMORY_MB` | Memory for the same. Default 1024. |
| `HTTP_API_SSH_GATEWAY_SESSION_DISK_MB` | Disk for the same. Default 8192 — a VS Code Remote server does not fit in less. |
| `HTTP_API_SANDBOX_CACHE_DIR` | Where `+jobs` keeps output it has already fetched. Unset means no cache: every read fetches the sandbox again. Must be real local storage with room for the budget below — on `tmpfs` the "cache" is memory the kernel cannot reclaim. Its contents are discarded at every start and may be deleted by hand while the server runs. |
| `HTTP_API_SANDBOX_CACHE_MB` | That cache's budget in megabytes. Default 4096. Negative disables the cache. |
| `HTTP_API_SANDBOX_MAX_GB` | Largest job sandbox `+jobs` will serve. Default 8. Negative means no limit. |
| `HTTP_API_SPOOL_BUFFER_DIR` | Where an archive dropped in `/submit/` is buffered, shared with the REST upload endpoints. Unset holds it in memory under a much smaller ceiling; see [What it costs the access point](#what-it-costs-the-access-point). |
| `HTTP_API_SPOOL_MAX_UPLOAD_BYTES` | Ceiling for one upload that ends in a spool, shared with the REST endpoints. Default 1000 MiB. It bounds a dropped archive times the procs it is spooled to, and a template's input files times the rows of a CSV. |

The gateway needs OAuth2, which is served whenever the web UI or MCP is
enabled (see [When OAuth2 is served](auth.md#when-oauth2-is-served)), since the device
flow is how it authenticates. Shell access needs no new scope: the schedd
registers `GET_JOB_CONNECT_INFO` at `WRITE`, so `condor:/WRITE` covers it.

If logins fail with *"could not start the login flow"*, the gateway cannot reach
its own device endpoint. It checks once shortly after startup and logs the URL
it tried — the usual cause is an unset `HTTP_API_OAUTH2_ISSUER`, which leaves
the default `http://localhost:8080` pointing at nothing.

## Locking out what keeps knocking

A public SSH port attracts traffic from people who are not users, so
the gateway counts failed logins per source and refuses new connections
from one that fails too often -- fail2ban's idea, without the log
scraping. It is on by default.

Counting happens at two granularities, because one is not enough. Per
host alone is free to evade over IPv6, where one allocation holds more
addresses than anyone could enumerate; per network alone would let a
single bad machine on a campus lock out the campus. So a host has its
own budget and the network around it has a larger shared one, and
tripping either is enough. A host is a `/32` on IPv4 and a `/64` on
IPv6, since one machine there routinely has several addresses; a
network is a `/24` and a `/48`. Once a host is locked out its further
failures stop counting anywhere, so it cannot spend its neighbours'
budget on their behalf.

Not every failure weighs the same:

| What happened | Weight |
| --- | --- |
| A login that began and was not finished -- the code expired, the browser said no | 1 |
| A certificate this deployment's CA did not sign, or one that did not verify | 1 |
| A **self-signed** certificate | 5 |
| Added when a failed login asked for a name a scanner works through -- `root`, `admin`, `ubuntu` | 3 |
| Authentication by a method this gateway never advertised -- password, GSSAPI | locked out at once |
| A refusal that is the gateway's own doing -- at its concurrency cap, issuer unreachable | 0 |
| `none` and `publickey` failures | 0 |

The last two rows are the ones that matter in practice. `none` is how
every SSH client asks what the server offers, and a client with a full
agent offers every key it holds before its certificate -- counting
either would lock out the people this is meant to protect. And an
issuer outage must not be mistaken for an attack, or a degraded gateway
locks out everybody who tried to reach it during the outage.

Asking for password authentication, on the other hand, is unambiguous:
this gateway never offers it, so no real client asks. That is the
strongest signal there is, and it is what most scanners trip on.

The username is a weaker one and is only ever added to a login that
*already failed*. `ssh root@gateway` is not by itself evidence of
anything here: an unprefixed username means "my default session", so
that is exactly what a real person gets from a container, and a real
person finishes the login. An explicit `+root` is somebody naming a
session and is never counted.

A lockout lasts `15m`, and doubles each time the same source comes back
for another, up to a day. Knocking while locked out does not extend it.

**Two caveats worth knowing.** The list lives in memory, so a restart
clears it and each replica keeps its own -- a restarted gateway starts
counting again rather than staying protected. And it counts whatever
address the connection arrives from, which behind a load balancer that
does not preserve the client address is the balancer: on Kubernetes
that means `externalTrafficPolicy: Local` on the gateway's Service, or
one bad client locks out everyone.

Put the office and the monitoring host in
`HTTP_API_SSH_GATEWAY_LOCKOUT_TRUSTED` so testing the gateway cannot
lock you out of it.

## Behind a load balancer

The gateway has to see each client's own address. It terminates the TCP
connection itself and does not accept the PROXY protocol, so nothing
upstream can tell it who the client is: pass the connection through at
layer 4 with the source address intact (`externalTrafficPolicy: Local`
on a Kubernetes Service, or no SNAT on the path). Behind anything that
rewrites the source -- a TCP proxy, SNAT, a NAT gateway -- every client
arrives from the same address, and everything counted per source is
counted for the whole site at once:

- At most four logins may be waiting at the approval prompt from one
  address. The fifth person to connect is refused until one of those
  finishes or its device code expires.
- The failure budget is shared. One user's abandoned logins lock
  everyone out, for `15m` the first time and up to a day.

Listing the balancer in `HTTP_API_SSH_GATEWAY_LOCKOUT_TRUSTED` stops the
lockouts by turning them off for every client behind it, and leaves the
four-login limit in place.

## Certificates, for scripts and for not approving every connection

`BatchMode=yes` refuses keyboard-interactive outright, so a script cannot use
the device flow at all — and nobody wants a browser prompt per connection
either. A certificate is obtained once and then works until it expires, or
until its owner withdraws it.

The gateway signs them with a CA key resolved exactly like the host key:
`HTTP_API_SSH_CA_KEY_FILE`, else generated and sealed in the database. Without
either, certificates are simply unavailable and the device flow still works —
losing convenience, not access.

That response also carries `gateway_host` and `gateway_port` when
`HTTP_API_SSH_GATEWAY_HOST` names one, so a client can find the gateway without
being told where it is. Both are omitted when it is unset: the listen address is
not the answer, since a container on `:2222` usually sits behind a service
publishing 22 somewhere else, and a guess would send clients to a closed port.
The port is reported only when the operator wrote one — `ap.example.edu:2222` —
and a client should read its absence as the SSH default rather than as an
instruction.

The common deployment is worth spelling out. A container binds
`HTTP_API_SSH_GATEWAY_ADDRESS = :2222` and a service publishes it as
`ap.example.edu` on port 22. Set `HTTP_API_SSH_GATEWAY_HOST = ap.example.edu`
and nothing else: the port is not 2222 as far as any client is concerned, and
the listen address is never consulted for what is advertised. Write
`ap.example.edu:8022` only if that is the port users really type.

Leaving it unset is logged as a warning at startup, because what it costs is
otherwise silent: no address is advertised, so every client has to be told by
hand; the host certificate is issued for every name rather than this one; and
the web UI prints no `ssh` command for a session.

```bash
# The CA, so your client trusts the gateway's host key.
curl -H "Authorization: Bearer $TOKEN" https://ap.example.edu/api/v1/ssh/ca

# A certificate for a key you already have. lifetime_seconds is
# optional, defaults to 12 hours, and is clamped to the 7-day cap
# rather than refused.
curl -X POST -H "Authorization: Bearer $TOKEN" \
     -d "{\"public_key\": \"$(cat ~/.ssh/id_ed25519.pub)\", \"lifetime_seconds\": 604800}" \
     https://ap.example.edu/api/v1/ssh/certificate
```

Save the `certificate` field as `~/.ssh/id_ed25519-cert.pub`, beside the private
key; `ssh` finds it on its own. Add the `known_hosts_line` to `~/.ssh/known_hosts`
and the gateway's host key verifies without pinning it by hand: the gateway
presents a host certificate signed by the same CA, so trusting the CA is enough.

Set `HTTP_API_SSH_GATEWAY_HOST` to the name users reach the gateway by, and both
the certificate's principals and the published `known_hosts_line` narrow to it.
That is worth doing: a line reading `@cert-authority * <ca>` tells the client to
trust this CA for **any** host, so naming yours confines what a stolen CA key
could vouch for. Several names are comma-separated, and a port is tolerated and
ignored — list every name and alias in use, because one left out stops verifying.

Left unset, the certificate lists no principals and the line uses `*`. A server
that does not know the names it is reached by cannot do better: it binds `:2222`
behind whatever the operator put in front of it, and a certificate naming the
wrong name fails closed for everyone.

Each configured name appears in the line twice, as `name` and `[name]:*`, because
OpenSSH looks a host up as `[name]:port` for any port but 22 — and this gateway
defaults to 2222.

The bare host key is still offered alongside it, so a client that pinned the key
before certificates existed keeps connecting and notices nothing. A client that
holds the CA line negotiates the certificate instead; OpenSSH prefers whichever
algorithm it already knows something about for that host.

Certificates last 12 hours by default and never more than 7 days.

The cap used to be a day, because the lifetime was the only control there was:
nothing recorded which certificates existed, so expiry was the only way one ever
stopped working. That made the feature unusable for the case it was built for —
a job that runs unattended cannot go back to a browser every twelve hours, so
anything automated was pushed onto a credential with no expiry at all, which is
strictly worse.

**Every issued certificate is now recorded, and its owner can withdraw it.** The
record is held in the access point's application database (`ssh_certificates`),
and the gateway consults it on every certificate login, so a withdrawn one stops
working at once rather than at its expiry. Users see their own certificates — and
the Revoke button — on the web UI's **Connectors** page; the same listing is at
`GET /api/v1/ssh/certificates`, and withdrawal at
`POST /api/v1/ssh/certificates/revoke` with `{"serial": "..."}`. Both are
**session-only**: a bearer token that could enumerate and revoke the credentials
beside it is a stolen token that locks its owner out.

Two consequences worth knowing:

- **The revocation check fails closed.** If the database cannot answer, the login
  is refused rather than allowed. A check that fails open is one an attacker only
  has to break rather than beat, and the store is a local database in the same
  process — an error there means the server is already unwell.
- **A certificate with no record is not refused.** Certificates issued before this
  existed have no row, and neither does one from another deployment sharing a CA.
  Refusing those is defensible and is not what happens, because it would have
  locked out every certificate in circulation at upgrade time.

If the record cannot be written, no certificate is issued: handing one over
unrecorded produces a working credential that appears in no list and that Revoke
cannot reach, with nothing telling its holder that theirs is the one which cannot
be withdrawn.

Three properties worth knowing, because they differ from how OpenSSH
certificates usually work:

- **The principal is the account, and the request cannot choose it.** The name
  signed is the one the access point resolved for the caller.
- **The username is not checked against the principal.** On this gateway the
  username names the job to reach, so requiring a match would mean a
  certificate per job.
- **Bare public keys are never accepted**, only certificates. A key on its own
  carries no identity and no expiry, so accepting one would mean this server
  keeping a list of whose key is whose — and a list that never forgets a
  compromised key.

## Limits worth knowing

- **`scp` and `sftp` work**, and so does anything else that asks for a
  subsystem: the gateway forwards the request to the job, whose sshd serves its
  own `Subsystem` directive. This was documented as impossible, on the belief
  that the forced command in `condor_ssh_to_job_shell_setup` turns a subsystem
  request into `eval sftp`. It does not — a probe against a real job gets a
  genuine `SSH_FXP_VERSION` reply from a real `sftp-server`.
- **The device flow needs somewhere to prompt.** `BatchMode=yes` declines
  keyboard-interactive outright, and a client with no terminal has nowhere to
  show the code; both want a certificate instead. A remote terminal is not
  required — `ssh -T` is fine, as long as the *local* end can prompt.
- **Ten sessions per job.** The sshd HTCondor starts uses OpenSSH's default
  `MaxSessions`, and every terminal for one job shares it. Port and socket
  forwards do not count against it.
- Each `ssh` is its own device authorization, so each asks for approval.
- **Port forwards reach whatever the job can.** `ssh -L` and socket
  forwards are dialled from inside the job, as `condor_ssh_to_job -L`
  does, to any host and port or socket path the job could reach itself.
  They carry the user's own authority over their own job, not the
  server's.
