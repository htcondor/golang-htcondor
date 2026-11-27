---
title: Submitting and watching a job
audience: user
order: 12
---

# Submitting and watching a job

The access point's REST API, from the point of view of someone running
jobs on it. The web console and the chat assistant do these same things
through the same endpoints, so what is written here is also what they
are doing on your behalf.

Every example uses a bearer token. How you get one depends on how your
site signs people in; the console's own pages will mint an API key for
scripts, and your administrator can tell you which method this access
point uses.

```bash
AP=https://ap.example.edu
AUTH="Authorization: Bearer $TOKEN"
```

## Submitting takes two steps

You send a submit file, and then you send the files it names.

```bash
curl -s -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"submit_file": "executable = analyze.py\ntransfer_input_files = data.csv\noutput = out.txt\nerror = err.txt\nqueue"}' \
  "$AP/api/v1/jobs"
```

```json
{"cluster_id": 412, "job_ids": ["412.0"]}
```

The job now exists and is **held**, waiting for its files. Send them as
a tar:

```bash
tar cf - analyze.py data.csv | curl -s -X PUT -H "$AUTH" \
  --data-binary @- "$AP/api/v1/jobs/412/input"
```

It starts on its own once the upload completes. Giving a bare cluster id
rather than `412.0` uploads to every job in that cluster still waiting,
which is what you want when one `queue 500` submitted five hundred of
them.

The two steps exist because the access point is not your machine. The
job's files have to get there somehow, and the queue is where the job
waits while they do.

### What belongs in the tar

Not what you might assume. The set of names the job is waiting for is:

- `transfer_input_files`, **minus any URLs** in it. A URL is fetched by
  the machine that runs the job, not uploaded from here, so putting it
  in the tar does nothing.
- **the executable**, under its bare filename — `analyze.py` even if
  the submit file said `/home/you/project/analyze.py`. Unless you set
  `transfer_executable = false`, or the executable is a system path
  like `/bin/sleep`, which every execute machine already has.
- **the stdin file**, if your submit file has an `input = ...` line.

Everything goes in flat, under its bare name. The sandbox is one
directory, so `data/sample.csv` arrives as `sample.csv`.

> **The schedd accepts those names and silently drops anything else.**
> A file you put in the tar but did not name in the submit file is not
> rejected — it is discarded, the upload reports success, and the job
> runs without it. When a job fails on a missing input it had, this is
> usually why.

If you want to see the exact list rather than work it out, ask: the
upload-URL tool in the chat assistant prints it, and a job's ClassAd
carries `TransferInput`.

### When there is nothing to send

A job with no input files, no stdin file, and a system executable has an
empty sandbox. It is **released at submit** and runs without you
uploading anything — the hold never happens.

```bash
# Runs immediately. There is no second step.
curl -s -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"submit_file": "executable = /bin/sleep\narguments = 60\nqueue"}' \
  "$AP/api/v1/jobs"
```

This is worth knowing in both directions. A job you expected to upload
to may already be running, and a job you expected to run may be sitting
held because its executable is yours rather than the system's.

### Many jobs from a table

Send the rows as rows rather than writing them into the submit file:

```bash
curl -s -H "$AUTH" -H 'Content-Type: application/json' -d '{
  "submit_file": "executable = analyze.py\narguments = $(sample) $(threshold)\nqueue",
  "queue_vars": ["sample", "threshold"],
  "queue_rows": [["a.csv", "0.5"], ["b.csv", "0.9"]]
}' "$AP/api/v1/jobs"
```

HTCondor splits a queue item row on whitespace **and** commas, with no
quoting, so a value containing either cannot be written into submit
text at all. Sent this way it is encoded correctly. The submit file
must not already carry its own `queue` line when you do this.

## Watching it

```bash
curl -s -H "$AUTH" "$AP/api/v1/jobs/412.0"
```

The reply is the job's ClassAd. `JobStatus` is the number you want:

| | |
| --- | --- |
| 1 | Idle — queued, waiting for a machine |
| 2 | Running |
| 3 | Removed |
| 4 | Completed |
| 5 | Held — something needs your attention, or it is waiting for its input |
| 6 | Transferring output |
| 7 | Suspended |

Attributes that are expressions rather than values arrive as
`/Expr(...)/` strings — `request_memory` is often one, since the
submit file may write it as a rule rather than a number. Retrieving a
single job adds an `evaluated` member carrying what `RequestCpus`,
`RequestMemory`, `RequestDisk` and `RequestGPUs` each come to for that
job, so you can read the number without a ClassAd evaluator of your
own. A request that does not evaluate to a number is left out rather
than guessed at, and the raw attribute is still there either way.

You get `evaluated` when you ask for one job, not when you list with a
projection: an expression evaluated against an ad missing the attribute
it refers to would quietly answer the wrong number.

To watch many jobs, list with a constraint instead of polling each one:

```bash
curl -s -H "$AUTH" --get --data-urlencode 'constraint=ClusterId == 412' \
  --data-urlencode 'projection=ClusterId,ProcId,JobStatus' \
  "$AP/api/v1/jobs"
```

Some access points also stream changes as Server-Sent Events at
`/api/v1/jobs/watch`, which saves polling entirely. It needs the access
point to be following its own queue, so not every deployment has it: if
yours does not, the endpoint says so with a 503 rather than hanging.

## Getting the output

**While it runs**, read the streams where they are being written, on
the machine running the job:

```bash
curl -s -H "$AUTH" "$AP/api/v1/jobs/412.0/peek?stream=stdout"
```

```json
{"stdout": {"text": "...the last 64 KB...", "offset": 1048576}}
```

That is `condor_tail`: the access point asks the running job's starter
for the bytes. Pass the `offset` you were given back as `stdout_offset`
on the next call to carry on from where you stopped rather than
re-reading the end. `stream` takes `stdout`, `stderr` or `both`, and
`max_bytes` raises the 64 KB default.

**After it finishes**, read the files themselves:

```bash
curl -s -H "$AUTH" "$AP/api/v1/jobs/412.0/stdout"
curl -s -H "$AUTH" "$AP/api/v1/jobs/412.0/stderr"
```

These two come out of the returned output sandbox, so they are the
whole file and they are not there until the job has transferred its
output back. Asking for them mid-run is the common mistake: `peek` is
the one that works then.

The whole output sandbox:

```bash
curl -s -H "$AUTH" "$AP/api/v1/jobs/412.0/output" | tar xf -
```

Or one file out of it, without downloading the rest:

```bash
curl -s -H "$AUTH" -O "$AP/api/v1/jobs/412.0/files/results.json"
```

## When it goes wrong

A held job carries `HoldReason` and `HoldReasonCode` in its ad, and the
reason is usually specific enough to act on:

```bash
curl -s -H "$AUTH" --get \
  --data-urlencode 'constraint=JobStatus == 5' \
  --data-urlencode 'projection=ClusterId,ProcId,HoldReason' \
  "$AP/api/v1/jobs"
```

Fix whatever it names, then release it:

```bash
curl -s -X POST -H "$AUTH" "$AP/api/v1/jobs/412.0/release"
```

Releasing a job whose cause you have not fixed just holds it again.

A job held **for its input** is a different thing and is not a failure:
`HoldReasonCode` is 16, and it is the normal state between the two
submit steps. Completing the upload is what clears it. If you see that
code on a job you thought was running, the sandbox never arrived.

## Changing your mind

```bash
# Stop it, queued or running
curl -s -X DELETE -H "$AUTH" "$AP/api/v1/jobs/412.0"

# Hold it without losing it
curl -s -X POST -H "$AUTH" "$AP/api/v1/jobs/412.0/hold"
```

To change an attribute of a queued job:

```bash
curl -s -X PATCH -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"attributes": {"RequestMemory": 4096}}' \
  "$AP/api/v1/jobs/412.0"
```

Numbers and booleans mean what they look like. **A bare string does
not** — it is passed through as a ClassAd expression, so
`{"Arguments": "hello"}` sets `Arguments` to the *expression* `hello`
rather than to the text. To set a string, say so:

```json
{"attributes": {"Arguments": {"type": "string", "value": "a, b"}}}
```

Attributes HTCondor protects, and those it will not let anyone change
after submission, are refused.

## These are your jobs only

Every endpoint here is scoped to you. Another person's job answers
**404**, not 403 — the API will not tell you whether a job you cannot
see exists. If you administer the access point there is a separate,
audited way to act across users; it is not on by default and it is not
what these endpoints do.

## Where to go next

- [Working with an AI assistant](assistant.md) — doing all of the above by asking
- [Shell access over SSH](ssh.md) — getting a terminal inside a running job
- [Files and jobs over SFTP](files.md) — the same work without an HTTP client
- [API surface](../developer/api.md) — the rest of the endpoints
