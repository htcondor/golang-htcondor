---
title: Site submit policy
audience: operator
order: 76
---

# Site submit policy

Two settings let an access point add submit-file lines to every
submission, so a requirement a user cannot reasonably know about is met
centrally instead of being asked of every user and every template. Part
of the [HTTP API + MCP server](index.md) documentation.

## The problem it solves

Some schedds refuse jobs for site-specific reasons, and the refusal
arrives at commit time as an opaque transaction failure. One real
example — a schedd that will not accept a job whose `log =` resolves
outside the submitter's home directory:

```
500: Job submission failed: failed to commit transaction: CommitTransaction
failed: Job event logs produced by 'log =' in the submit file ... must be
written inside your home directory ... (error code 22)
```

Nothing a user reads tells them that in advance. Fix it once, here.

## The two settings

| | Applies |
| --- | --- |
| `HTTP_API_SUBMIT_FILE_DEFAULTS` | only where the submit file is **silent** — a user who sets the same command keeps their own value |
| `HTTP_API_SUBMIT_FILE_OVERRIDES` | **regardless** of what the submit file says |

Use defaults for a sensible starting point. Use overrides for what is
not the user's to opt out of.

Both are written as HTCondor configuration, so a multi-line value needs
that syntax:

```condor
HTTP_API_SUBMIT_FILE_OVERRIDES = @=end
  log = /home/$ENV(USER)/htcondor-jobs.log
@end
```

Setting neither leaves submit files **byte-for-byte unchanged** — not
even a stray marker comment.

## How the two differ, mechanically

Neither setting parses submit-file syntax. It does not need to: submit
commands are macro assignments evaluated when `queue` is reached, so
**the last assignment before `queue` is the effective one.**

- **Defaults are prepended**, so anything the user writes later beats
  them.
- **Overrides are spliced in just before `queue`**, so they beat
  everything above.

Both blocks are wrapped in marker comments naming the setting they came
from, so somebody looking at a generated submit file can see where its
extra lines originated.

### Custom attributes are refused, not overridden

A submit file that sets a **custom attribute** — `+ProjectName` or
`MY.ProjectName` — that the site's overrides also set is **rejected
with 400**:

> the submit file sets job attribute ProjectName, which this access
> point's site policy sets for every job; remove that line

That is not inconsistency. A `+Attr` line is not a macro assignment; it
goes straight to the job ad, so the splice cannot beat it by being
later. Silently letting the user's value win would make the override a
suggestion, and silently dropping theirs would be worse. Refusing says
what happened and whose line to remove.

The scan that finds those names is a line scan, not a parse, because
the text may be a DAG node description `condor_submit` will read
later. That bounds it: a submit file assembling an attribute name
through macro expansion defeats it. **Treat this as closing an easy
mistake, not as enforcing a boundary** — a determined user is not
stopped by it, and the schedd's own ACLs are where a boundary belongs.

## Where it applies

Every submission through this server: `POST /api/v1/jobs`, the
templates and submit pages, interactive terminals, JupyterLab, and MCP
`submit_job`.

**Workflows get it twice.** A DAG submitted through `POST
/api/v1/jobs/dag` or MCP `submit_dag` receives the policy on the
DAGMan manager job **and** on every node description it carries, inline
blocks included. DAGMan submits the node jobs itself, on the access
point, long after the request has returned — so the submission is the
last chance site policy has to reach them.

## Trust model

Operator-only configuration, spliced in **verbatim**: no whitelist, no
quoting, no validation. This is the same trust model as
`HTTP_API_INTERACTIVE_EXTRA_SUBMIT` and
`HTTP_API_BUILD_EXTRA_SUBMIT`.

It is the operator's hook into job admission policy, **equivalent in
privilege to writing the schedd's `site_local` config**. Anything that
can set these settings can run arbitrary submit commands as every user
of this access point.

Both are read at startup, so a change needs a restart.

## Where to go next

- [Interactive sessions and apps](interactive.md) — `HTTP_API_INTERACTIVE_EXTRA_SUBMIT`, the same hook for interactive jobs
- [Configuration reference](config.md) — the full text of each setting
