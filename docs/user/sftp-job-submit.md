---
title: Submitting jobs over SFTP
audience: user
order: 30
---

# Submitting jobs over SFTP

A walkthrough of the `+jobs` file service: how to send work to an access
point with nothing but an SFTP client, and how to get the output back.
The service itself, and how an administrator turns it on, is described in
[SSH gateway](../operator/ssh-gateway.md); this page is the user's side of it,
start to finish.

You need no account on the access point, no HTCondor installed locally, and
no tool beyond the `sftp` your machine already has.

## Before you start

Three things, from whoever runs the access point:

| | Example |
| --- | --- |
| the host | `ap.example.edu` |
| the gateway port | `2222` |
| that `+jobs` is enabled | `sftp -P 2222 +jobs@ap.example.edu` connects |

Connect:

```sh
sftp -P 2222 +jobs@ap.example.edu
```

The first connection asks you to approve in a browser:

```
Approve at https://ap.example.edu/mcp/oauth2/device/verify?user_code=WDJB-MJHT (code WDJB-MJHT), then press Enter:
```

Open the link, check that the code matches the one in your terminal, approve,
and press Enter. The username `+jobs` carries no identity — the browser
approval does — so it is the same for everybody. For scripts and cron jobs,
where nobody is there to approve, get a certificate instead: see
[Certificates, for scripts and for not approving every
connection](ssh.md#certificates-for-scripts-and-for-not-approving-every-connection).

What you land in is not a shell. It is a small tree:

```
sftp> ls
outputs    submit    templates
```

| | |
| --- | --- |
| `outputs/` | your finished jobs, and their output |
| `submit/` | drop an archive holding a `job.sub` — **this page's walkthrough** |
| `templates/` | drop a CSV on a submit description this site already offers |

## Which of the two ways in

Both submit jobs. They differ in who writes the submit description.

| | `templates/` | `submit/` |
| --- | --- | --- |
| who writes the submit description | the site, or you, once, in the web UI | you, in the archive |
| what you upload | a CSV, one job per row | a tar archive |
| can carry your own executable | no | **yes** |
| varies per job | the CSV's columns | `queue` in your `job.sub` |
| good for | running known work over your data | work nothing here describes yet |

If a template exists for what you are doing, use it — a CSV is less to get
right. The rest of this page does `submit/` first, because it is the one that
answers "I have a program and I want it run".

## Walkthrough: your own job, start to finish

Two files, one upload, one download. Everything below is literal; substitute
your own host and port.

### 1. Write the work

```sh
mkdir hello && cd hello
cat > hello.sh <<'EOF'
#!/bin/sh
echo "ran on $(hostname) at $(date)"
echo "argument was: $1"
EOF
chmod +x hello.sh
```

`chmod +x` matters: the archive carries the mode bit, and this file is about
to be the job's executable.

### 2. Describe it

```sh
cat > job.sub <<'EOF'
universe = vanilla
executable = hello.sh
arguments = world

output = hello.out
error = hello.err

request_cpus = 1
request_memory = 128M
request_disk = 100M

queue
EOF
```

This is an ordinary HTCondor submit description, with two things to know:

- **`executable = hello.sh` is a file in the archive.** HTCondor transfers the
  executable by default, so `hello.sh` has to be packed alongside `job.sub` —
  and it is. A `/bin/sh` or a path inside a container image is not transferred
  and does not need packing.
- **The file must be named `job.sub`**, at the top level of the archive. The
  service finds the submit description by that name and nothing else.

No `log =` line. A user log is written on the access point, not here, so it is
not the way to follow a job over this surface — reading `job.ad` is, as step 6
does. Setting one is not an error; it is just not useful.

### 3. Pack it

```sh
tar czf hello.tar.gz job.sub hello.sh
```

`job.sub` has to be at the **top level** of the archive: pack the files, not
the directory holding them. From one level up, `tar czf hello.tar.gz hello/`
produces `hello/job.sub`, which is refused. Plain `tar` and gzipped `tar` are
both accepted, decided by the file's first bytes rather
than by its name, so `.tar`, `.tgz` and no extension at all work the same.

The archive's name is yours to choose. It is how you will find the receipt.

### 4. Drop it

```sh
$ sftp -P 2222 +jobs@ap.example.edu
sftp> put hello.tar.gz /submit/
Uploading hello.tar.gz to /submit/hello.tar.gz
hello.tar.gz                                  100%  412     8.7KB/s   00:00
```

**The submission happens when your client closes the file**, not while it
uploads. An interrupted transfer submits nothing at all — there is no
half-submitted state to clean up.

The upload itself says almost nothing about what happened next, and that is a
limit of the protocol rather than of this service: an SFTP close carries one
error string, which most clients render as "could not close file" and discard
the rest. So the answer is written to a file instead.

### 5. Read the receipt

```sh
sftp> ls /submit/status
hello.tar.gz
sftp> get /submit/status/hello.tar.gz
```

```
state: submitted
at: 2026-11-23T17:04:11Z
files: 2
cluster: 4217
digest: 9f2b1c0e…
```

| field | what it says |
| --- | --- |
| `state` | `submitted`, `refused`, `duplicate` or `incomplete` |
| `at` | when, in UTC |
| `files` | how many files were in the archive |
| `cluster` | **the cluster id your jobs got** — this is what you came for |
| `digest` | the SHA-256 of the archive, which is what the duplicate check compares |
| `error` | present on `refused`, `duplicate` and `incomplete`, and says why |

The four states:

- **`submitted`** — the jobs exist. `cluster` names them.
- **`refused`** — nothing was submitted, and `error` says what to fix. Every
  refusal lands here, including per-file ones.
- **`duplicate`** — these exact bytes were already submitted within the last
  hour, and `cluster` names that earlier submission. This absorbs a retry: a
  dropped connection and a second `put` do not double your jobs. After an
  hour the same archive is a new submission, because deliberately running the
  same work twice is your decision.
- **`incomplete`** — the upload did not finish. Nothing was submitted; drop it
  again.

Status entries outlive your connection, which is the case that produces them:
`sftp -b` and `scp` close the channel when the command ends, so the answer is
read by a later session. They are kept for a week and held in memory, so a
restart of the API server forgets them. The durable record is the cluster
itself.

### 6. Wait for it

`outputs/` lists your **completed** jobs, so the job appearing there is the
signal that it is done:

```sh
sftp> ls /outputs
4217.0
```

While it is still running it is not in that listing — but its ad is readable
by name the whole time, which is how you check on it:

```sh
sftp> get /outputs/4217.0/job.ad
sftp> !grep -E '^(JobStatus|HoldReason) ' job.ad
JobStatus = 2
```

`JobStatus` is `1` idle, `2` running, `4` completed, `5` held. A held job has
a `HoldReason` saying why.

### 7. Fetch the output

```sh
sftp> ls /outputs/4217.0
data    job.ad
sftp> get -r /outputs/4217.0/data
sftp> !cat data/hello.out
ran on e1234.chtc.wisc.edu at Sun Nov 23 17:09:02 UTC 2026
argument was: world
```

| | |
| --- | --- |
| `data/` | the output sandbox, as files |
| `job.ad` | the job's ClassAd — exit code, memory used, where and when it ran |
| `data.tar` | the same sandbox as one archive; **reachable by name, not listed** |

`job.ad` answers what the output alone cannot:

```sh
sftp> !grep -E '^(ExitCode|RemoteHost|MemoryUsage|CompletionDate) ' job.ad
```

For a whole directory at once, outside an interactive session, `rsync` and
`scp` both work:

```sh
rsync -av -e 'ssh -p 2222' +jobs@ap.example.edu:outputs/4217.0/data/ ./results/
```

Output stays retrievable for **10 days** after the job completes, and then the
job leaves the queue and its spool is gone. (That window is `LeaveJobInQueue`,
which your `job.sub` may set to something else with `leave_in_queue`.)

### 8. The whole thing, as one script

Nothing above needs an interactive session. `sftp -b` runs a batch file and
exits non-zero if any command fails, which is what makes this scriptable:

```sh
#!/bin/sh
set -e
HOST=+jobs@ap.example.edu
PORT=2222

tar czf hello.tar.gz job.sub hello.sh

sftp -P "$PORT" -b - "$HOST" <<'EOF'
put hello.tar.gz /submit/
get /submit/status/hello.tar.gz receipt.txt
EOF

cat receipt.txt
grep -q '^state: submitted' receipt.txt || { echo "not submitted"; exit 1; }
cluster=$(awk '/^cluster:/ {print $2}' receipt.txt)

# Wait for it to finish: the job appears in /outputs when it completes.
until sftp -P "$PORT" -b - "$HOST" <<EOF | grep -q "^$cluster\.0\$"
ls -1 /outputs
EOF
do
    sleep 60
done

sftp -P "$PORT" -b - "$HOST" <<EOF
get -r /outputs/$cluster.0/data
get /outputs/$cluster.0/job.ad
EOF
```

With a certificate installed there is nothing interactive left in it, so it
runs from cron. Without one, the first `sftp` prompts for a browser approval
and the rest reuse the session.

## Walkthrough: many jobs from one archive

The same archive, with a different `queue` statement. HTCondor's inline item
form lists the values in the submit description itself:

```
universe = vanilla
executable = hello.sh
arguments = $(sample)

output = $(sample).out
error = $(sample).err

request_cpus = 1
request_memory = 128M
request_disk = 100M

queue sample from ((
HG001
HG002
HG003
))
```

Three jobs, `4218.0` through `4218.2`, each with its own `data/`. `queue 10`
works too, as does `queue sample, reads from (( … ))` for several variables
per row.

**`queue … from <file>` does not work**, and neither do `include`, `include
command` or `$ENV()`. Your submit description is parsed without access to the
access point — the same as one posted to `/api/v1/jobs` — so a directive that
reads a file would read the API server's filesystem, and one that runs a
command would run it as the API server. Use the inline form above.

The archive is spooled **once per proc**, because HTCondor spools input per
job. A 50 MB archive queued 100 times is 5 GB, and the server's upload ceiling
(`HTTP_API_SPOOL_MAX_UPLOAD_BYTES`, 1000 MiB by default) applies to that
total. Over it, the submission is refused with its jobs withdrawn rather than
left half-spooled. Large shared input belongs in a URL — an `https://` or
`osdf://` in `transfer_input_files`, which the execute node fetches itself and
which therefore does not go in the archive at all.

## Walkthrough: a template and a CSV

When the site already describes the work, you supply only the data.

```sh
$ sftp -P 2222 +jobs@ap.example.edu
sftp> ls /templates
blast    align    hello-world
sftp> get /templates/blast/README
```

The `README` is generated from the template, and the part to read is its
columns:

```
blast
=====

Run blastn against a reference database.

Drop a CSV into submit/ with these columns, in any order:

  sample               the sample's identifier
  reads                a URL to the reads file

One job per row. For example:

  sample,reads
  value-of-sample,value-of-reads

What happened to an upload is in status/ under the same name.
Output comes back under /outputs/<cluster>.<proc>/data/ once the jobs
finish, beside a job.ad saying what each one did.
```

So:

```sh
cat > runs.csv <<'EOF'
sample,reads
HG001,https://data.example.edu/HG001.fq.gz
HG002,https://data.example.edu/HG002.fq.gz
EOF
```

```sh
sftp> put runs.csv /templates/blast/submit/
sftp> get /templates/blast/status/runs.csv
```

```
state: submitted
template: blast
at: 2026-11-23T17:31:02Z
rows: 2
cluster: 4219
digest: 3a81…
```

From here it is identical to the walkthrough above: wait for the ids in
`/outputs`, fetch `data/`.

The rules a CSV answers to:

- **The header names the template's columns**, in any order. A header that
  misspells one, omits one or adds one is refused naming the difference —
  because the alternative is a submission where `$(sample)` expands to
  nothing: the right number of jobs, doing the wrong work.
- **One row is one job.** A row that is blank, or whose text starts with `#`,
  is **refused** rather than skipped: HTCondor's itemdata would drop it, and a
  job quietly vanishing is worse than an upload that says no.
- **Values are refused rather than altered.** A value containing `$`, a comma,
  a line break or a NUL is rejected naming the row and the column, and so is a
  row that starts with `)` — it would end the job list early, after which the
  rest of the file is read as submit commands and the jobs that run are not
  the ones listed. Each of these would otherwise make the submit parser do
  something other than what the spreadsheet says.
- **A column can name an input**, if the template says `transfer_input_files =
  $(reads)`. A row naming a URL is fine. A row naming a plain file is not —
  there is no submitting machine for the access point to upload it from — and
  because that is only knowable once the row is filled in, the whole cluster
  is **withdrawn** and `status/` says so.

Which templates appear is the library's answer: the built-ins, the operator's
catalog, your own saved templates, and any another user shared. Not every
template that works on the web **Submit** page can be driven by a dropped CSV;
where one cannot, its `README` says so at the top, before the columns. The
reasons are in [Which templates can be used
here](files.md#which-templates-can-be-used-here).

Every job carries `SFTPJobTemplate = "<id>"`, so `condor_q -af
SFTPJobTemplate` says which template a cluster came from.

## When it goes wrong

The receipt in `status/` is the first place to look, always. It exists because
the protocol gives the client one error string and most clients throw it away.

| What you see | What happened | What to do |
| --- | --- | --- |
| `Couldn't close file: Failure` | the submission was refused | `get /submit/status/<name>` — the reason is in `error:` |
| `error: the archive has no job.sub in it` | the submit description is missing or misnamed | name it exactly `job.sub` |
| `error: the archive has job.sub at mydir/job.sub, but it has to be at the top level` | you packed a directory | `tar czf x.tar.gz job.sub run.sh`, not the directory above it |
| `error: job.sub asks for hello.sh, which the archive does not carry` | a name in `transfer_input_files`, or the executable, is not in the archive | pack it, or make it a URL |
| `error: job.sub does not parse` | a syntax error in the submit description — an unterminated `queue … from ((` is the usual one | the message names the line |
| `state: duplicate` | the same bytes within the hour | nothing — your jobs exist, under the cluster named |
| `state: incomplete` | the transfer died mid-upload | drop it again |
| `outputs/` holds only `UNAVAILABLE.txt` | the listing could not be built | read the file; it says why, and naming a job directly still works |
| the job is nowhere in `/outputs` | it has not completed, or it is more than ten days old | read `/outputs/<id>/job.ad` for its `JobStatus` |
| the job is held on `HoldReasonCode 16` | it is waiting for input that is not coming | a file is missing from the archive; resubmit a complete one |
| a `get` is slow the first time | the sandbox is transferred whole, once | expected; later reads of the same job are cached |

## What this surface cannot do

Named here so you can stop looking for them:

- **No shell.** `+jobs` serves files; `ssh <cluster>.<proc>@gateway` is how you
  get into a running job.
- **No reading a file the submission needs from the access point.** `include`,
  `include command`, `$ENV()` and `queue … from <file>` are all refused for
  the same reason: there is no submitting machine here.
- **No partial submission.** One archive is the unit. A submit description is
  useless without the files it names, so there is no way to upload them
  separately.
- **No writes into `outputs/`.** It is read-only, and a write is refused with a
  reason rather than a bare failure.
- **No jobs older than the queue.** Once a completed job leaves the queue its
  spool is gone, so its output is unreachable here. Ten days by default.
