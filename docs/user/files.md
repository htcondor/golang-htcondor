---
title: Files and jobs over SFTP
audience: user
order: 30
---

# Files and jobs over SFTP

`ssh +jobs@gateway` is not a shell. It serves your finished jobs' output
over SFTP and takes new work in the same way, so any SFTP client you
already have can read results, start a batch of jobs from a CSV, or
submit a job of your own.

Reaching the gateway in the first place is
[Shell access over SSH](ssh.md); what a site has to configure is
[SSH gateway](../operator/ssh-gateway.md).

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
the [htcondordb mirror](../operator/config.md). That is not a subset of what you
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
