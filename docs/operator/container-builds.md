---
title: Container builds
audience: operator
order: 78
---

# Container builds

The `build_container` MCP tool submits an Apptainer build as a job on
your pool and publishes the finished `.sif` to object storage. A user —
usually through an agent — hands it a definition file or a Dockerfile
and a name; the tool supplies the submit attributes, the resource
requests, cache handling and the transfer of the result. Part of the
[HTTP API + MCP server](index.md) documentation.

There is no REST endpoint for this. It exists only as an MCP tool, so
everything here concerns a site that has MCP enabled.

## The tool is offered whether you configure it or not

This is the first thing to know, because the default is not "off".
`build_container` is in the catalogue whenever MCP is served. On a site
that has configured none of the settings below, a caller gets a build
job that is submitted as an ordinary job, matched to an ordinary slot,
and asked to publish to a destination they had to name themselves.

So there are two reasonable states, and the unconfigured middle is not
one of them:

- **Configure it.** At minimum `HTTP_API_BUILD_REQUIREMENTS`, so builds
  reach machines that can actually run them, and the caps below.
- **Withdraw it.** `HTTP_API_MCP_DISABLED_TOOLS = build_container`
  takes it out of the catalogue. See
  [Disabling tools](agents.md#disabling-tools).

## What a site has to decide

Three things, and the settings fall into those three groups.

### Which machines builds run on

```
# Both values are site conventions, not attributes HTCondor defines.
# Use whatever your build machines actually advertise.
HTTP_API_BUILD_REQUIREMENTS = HasApptainerBuild == true
HTTP_API_BUILD_EXTRA_SUBMIT = +IsBuildJob = true
                              keep_claim_idle = 300
```

`_REQUIREMENTS` is a ClassAd expression ANDed into the build job's
requirements. There is no way for the tool to guess it — a site picks
its build machines by its own convention — so an unconfigured server
submits an ordinary job and lets it land anywhere.

`_EXTRA_SUBMIT` is whatever else your build slots need — the attribute
a transform or a startd policy keys on, and anything operational, such
as `keep_claim_idle` so a run of builds reuses one claim instead of
re-matching for each.

A build needs more of the machine than people expect: it unpacks a root
filesystem, writes the image, and keeps a layer cache, all in scratch.

### How much one build may take

```
HTTP_API_BUILD_DEFAULT_CPUS      = 8
HTTP_API_BUILD_DEFAULT_MEMORY_MB = 16384
HTTP_API_BUILD_DEFAULT_DISK_MB   = 30720

HTTP_API_BUILD_MAX_CPUS      = 16
HTTP_API_BUILD_MAX_MEMORY_MB = 65536
HTTP_API_BUILD_MAX_DISK_MB   = 102400
```

The defaults shown are the built-in ones, used when a caller does not
say and you have not either: 8 CPUs, 16 GiB, 30 GiB of scratch.

**Set the caps.** Unset leaves each uncapped, and build slots tend to
be the largest machines a site has — CHTC's are 128-core with 256 GB —
so an agent asking for "plenty" can otherwise claim one whole. A
request above the cap is **clamped, not refused**: the build still
runs, because refusing would make an agent guess at a number you never
told it.

A malformed value in any of these six is reported and ignored rather
than refusing to start. The rest of the server has nothing to do with
container builds, and a typo in a build setting should not take the API
down.

### Where the images go

```
HTTP_API_BUILD_STAGING_BASE = osdf:///chtc/staging/{initial}/{user}
```

Unset means every call has to name its own full destination, which
pushes a site-specific path into every user's prompt. With a base set,
a caller gives a bare `name` like `py311.sif` and it is appended.

Two placeholders make a per-user staging area expressible:

| | |
| --- | --- |
| `{user}` | the authenticated caller |
| `{initial}` | the first letter of that name |

A base with no placeholder is used verbatim, which is what a shared
staging area wants.

**The destination directory must already exist.** Object stores do not
create one on write, and the job fails if it is missing — so if your
staging base is per-user, something has to make each user's directory
before their first build.

## What users and agents see

Worth knowing, because it shapes the support questions you get.

The tool **returns as soon as the job is submitted**, not when the
image is built: a real build takes minutes. The caller waits with
`watch_jobs`, then reads the build log from the job's stdout and
stderr.

A failed build **publishes nothing**, and so does a build whose
`verify` command fails — `verify` is a shell command run inside the
freshly built image, and a nonzero exit suppresses publication. That
matters more than it sounds: a published image reaches a shared path
where other people may pick it up, so an image that cannot run the
thing it was built for is worse than no image.

Either way the job names its stdout and stderr and HTCondor returns
them on a failing exit, so the failure is diagnosable without you
going to look.

A **Dockerfile** is built directly by Apptainer's `buildkit:`
bootstrap. Nothing translates between the two formats, so Dockerfile
semantics are whatever the builder on the execute node implements —
and that builder is not present at every site. Where it is missing the
job fails saying so, which is the right failure but is a support
question if your build machines do not have one.

## Where to go next

- [Agents: MCP, site skills, chat](agents.md) — enabling MCP, and withdrawing a tool
- [Configuration reference](config.md) — the full text of each setting
