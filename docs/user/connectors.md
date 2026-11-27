---
title: Connecting an account
audience: user
order: 35
---

# Connecting an account

Some access points can read files out of your Google Drive, or write a
job's results into one of your spreadsheets. To let one do that, you
**connect** the account: you sign in to the provider, and it tells the
access point that this particular access point may act on your behalf,
for a narrow set of files.

This is the one thing in this guide that is not really about HTCondor.
It is a decision about an account that is yours and not the access
point's, so it is worth reading before you click through it.

## What you are granting

When you connect an account you are telling the provider that the
**access point** may use your account — not a job, not a person, and
not just for this afternoon. Specifically:

- **It is a standing grant.** It outlives the browser session you made
  it in. Signing out of the access point does not end it, and closing
  the tab does not end it. It lasts until you take it back, at either
  end.
- **It is yours alone.** The grant is recorded against your username
  and nobody else can use it. There is no way to ask the access point
  to use somebody else's connected account, by design: the connector is
  named in the request, the user never is.
- **It is as narrow as the provider allows.** For Google Drive the
  access point asks for `drive.file`, which is per-file: it can see a
  file *you picked* and nothing else in your Drive. A provider without
  such a scope grants more, and the provider's own consent screen is
  where you find out what.

What it is not: a login. A connected account does not let anyone sign
in as you, and the access point never sees your password.

## Connecting one

In the web console, each account your site offers has a **Connect**
button. It sends you to the provider, you approve there, and the
provider sends you back.

Two things about that flow are worth knowing.

**It only works in a browser.** The connect endpoints read your browser
session and nothing else — not a bearer token, not an API key. That is
deliberate: a browser is the only thing that can complete the
provider's sign-in, and treating an API token as a login here would
mean handing your Drive credential to whatever was holding the token.

**The link itself is harmless.** A connect address is fixed,
per-connector, and carries no authority at all: without a signed-in
browser it gives you a login page rather than a credential. So it is
safe for a tool, a log line or a page like this one to print one — and
equally, being sent one does not mean anything has been granted.

## Taking it back

There are **two halves**, and the access point owns only one of them.

**At the access point**, disconnect — the button beside the connector,
or:

```bash
curl -s -X DELETE -b "$COOKIEJAR" "$AP/api/v1/connect/builtin:gdrive"
```

This forgets the access point's copy of your credential. It is
idempotent: disconnecting something you never connected is a success,
not an error, so a double-click is not a failure.

**At the provider**, revoke. Google, Box, OneDrive and Dropbox each
have a page listing the applications you have granted access to, and
removing it there is what ends the provider's side of the grant.
**Disconnecting at the access point does not do that for you**, and
cannot.

If you are disconnecting because something is wrong rather than because
you are finished, do both — and do the provider's side first.

## Using it in a job

A connected account's files are named in `transfer_input_files` with a
URL that says which credential and which provider:

```
transfer_input_files = webapi-builtin-gdrive+gdrive://1A2b3C4d/data.csv
```

- `webapi-builtin-gdrive` is the connector — which credential to use.
  Your site's prefix may be something other than `webapi`.
- `gdrive` is the provider's protocol.
- `1A2b3C4d` is the provider's id for the file.
- `data.csv` is what the file will be called in the job's directory.
  The last path component is the name, so that is how you choose it.

You do not normally type this. The submit page's file chooser writes it
for you — and for Drive, picking the file in the provider's own chooser
is *what grants access to that file*.

The access point fetches the file and puts it in the job's input, so
**your credential does not travel to the machine running the job**. The
job sees an ordinary file. (A site can be configured the other way, so
that the execute machine fetches; ask yours if it matters to you.)

To see what a job pulled:

```bash
condor_q -af ConnectorInputs
```

### Results into a spreadsheet

A submission can nominate a spreadsheet for its results. Each job's
result file is appended to a tab as that job finishes:

```json
{
  "submit_file": "...",
  "output_sheet": {
    "connector": "builtin:gdrive",
    "file_id": "1X2y3Z",
    "tab": "Runs",
    "job_file": "result.json"
  }
}
```

`job_file` is a file in each job's own directory — a name, not a path —
holding JSON, or CSV with a header row.

Rows are fitted to the tab's **existing header row**: a column your job
did not produce is left blank, and a field the tab has no column for is
dropped. So the tab needs its header row before you submit, and it is
the header that decides what gets recorded.

Each job is appended at most once. The tab, and the fact that each job
really transfers `job_file` back, are both checked *before* the jobs
are queued — so a destination that cannot work is a rejected submission
rather than a batch of jobs that quietly deliver nothing.

Your site has to have results delivery switched on for this, and it is
not available on a multi-access-point front end.

## When a connector is not offered to you

Each account a site offers can be restricted to particular groups. If
you are not in one, the connector tells you it is "not available to
you" and no more than that — deliberately, since who *may* use it is
not yours to know. Ask your administrator.

A connector that was working and now asks you to connect again usually
means the provider has stopped renewing the grant: you may have revoked
it, you may have changed your password, or it may simply have aged out.
Connecting again is the fix.

## Where to go next

- [Submitting and watching a job](jobs.md) — where `transfer_input_files` goes
- [Connected accounts](../operator/connectors.md) — the operator's side: which accounts a site offers, and how it configures them
