# Documentation site

## Status

In progress. Stages 1a and 1b are built, stage 2 is most of the way
through its conversion, and stage 3 is done out of order because it
depended on nothing. Each stage below is meant to land on its own and
be useful before the next one starts.

Stage 1a, as built: `docs/` is three audience directories, every page
carries `title`/`audience`/`order` front matter, `docs/nav.json` is
generated from the tree by `make docs-nav`, and `make docs-check`
validates front matter, heading/title agreement, every relative link
and anchor, and the freshness of the manifest. `internal/docsmeta` is
the parser and manifest builder; it takes no new dependency.

The goal is official documentation for this repo that is better than
`README.md`, split by audience, with the content written so it can later
be folded into the HTCondor manual rather than rewritten for it.

The starting point is not the README. It is `docs/`, which already holds
6,018 lines of good prose that nothing renders and nothing links to.

## What the conversion turned up

Stage 2 is the stage that touches code rather than prose, and most of
what it found was not in this note's reasoning. Recorded because each
one is a trap for the next person converting a setting.

**A knob had to be readable through an interface, not a Config.** The
first consumer outside `main.go` was `webapi/connectors`, which takes a
narrow `Params` interface rather than the whole `*config.Config`, so
`Get(*Config)` did not compile against it. `Get` takes a
`config.Source` now. Reporting a malformed value needs more than that
interface, so it is an unexported method only `*Config` implements.

**A new primitive guessing at semantics the old code had pinned down is
the real risk of a mechanical migration.** `ParseList` split on
whitespace as well as commas, because that looked like a reasonable
reading of "a list". `splitList`, which it replaced, splits on commas
only, and every setting of that shape is documented as
comma-separated -- so the conversion would have silently turned
`HTTP_API_PICKERS = my picker` into two choosers. The same class caught
`BoolKnob`, which fell back to the default for a value like `falsey`
while `Config.GetBool` resolves it to `false`, following
`param_boolean`. Both are now checked against the thing they replace
rather than against what seemed sensible.

**The comma-or-space prediction came true immediately.** OAuth2 scopes
are space-delimited by RFC 6749 and read with `strings.Fields`, so they
needed `WordsKnob`. The note said "a setting documented as
comma-or-space needs a kind of its own"; it was the next setting
converted.

**Reading a value is not enough; some callers need to know it was
set.** Connector and site-app settings overlay a shipped definition,
where an absent list keeps the shipped one and an explicitly empty list
means none. Every kind gained `Lookup`.

**Two size parsers disagree, which blocks a conversion that looks
trivial.** `HTTP_API_SPOOL_MAX_UPLOAD_BYTES` is the `BytesKnob` case
the note describes, but `spool.ParseSize` requires the number first and
so rejects a negative, while `config.ParseBytes` parses one and then
rejects it as non-positive. Swapping them would change which
configurations are accepted. They have to be reconciled first.

**Declaring a reload scope found three places where the daemon and the
documentation disagreed.** The TLS certificate and key were declared
`reconfig` and are read at startup; the connector and picker
enumerating parameters were generated as `startup` and are re-read
wholesale on every reconfigure; and eight declared settings appeared in
`reconfigParams` nowhere at all, which by that file's own reasoning
means a change to them does nothing and nothing says so. A test now
checks the declaration against the table in both directions.

**Three parsers in this codebase disagree about what "true" is, and
converting a name is how you find out which one a setting was using.**
`BoolKnob` mirrors HTCondor's `param_boolean`, which takes `true`, `t`,
`1` and a ClassAd expression -- and not `yes` or `on`. `featureOffered`
uses `strconv.ParseBool`, which takes neither of those either but
refuses to start on anything else. `isTruthyConfigValue` takes `yes`
and `on` and quietly returns false for the rest. Reading a setting with
the wrong one of the three is a silent behaviour change in whichever
direction that setting happened to be written: `HTTP_API_USER_HEADER_TRUST_ANY`
was narrowed by exactly this and described in its own commit message as
a widening, and the identical mistake on the SSH lockout was caught by
an existing test within seconds. The rule that came out of it is that a
boolean's accepted spellings are something to read out of the old code
before converting it, never something to assume from the type.

**The scope test has a blind spot exactly where the scope was wrong a
second time.** `TestReconfigScopesMatchTheKnobs` walks `knobs.All`,
which by construction excludes a family's per-instance settings --
they have no name until an instance does. All fourteen connector and
picker per-instance settings were declared `startup`, and
`SetConnectorConfig` replaces the whole source from a freshly loaded
configuration on every reconfigure, so every one of them is installed
by a reconfigure. The declaration was right for the parameter that
names the instances and wrong for each setting underneath it, and the
test that exists to catch precisely that could not see them. Nothing
surfaced it until the reference began printing the scope for settings
the test does not cover; printing a claim to an operator is a check
that no test in this repo was performing.

**The cheap drift test is weaker than this note first claimed.**
`HTTP_API_SPOOL_BUFFER_DIR` is read in exactly one place,
`runDemoMode`, so a check that matches parameter names against the code
passes on it while the ordinary server ignores the setting entirely. A
name-matching test is still worth having, but it is not a substitute
for the registry; it cannot see where the name is read.

## The problem

Four separate things are wrong, and only the first is "we need to write
more documentation".

**The documentation we have is invisible.** `docs/` is 17 files: one
developer-facing (`library.md`) and sixteen operator-facing
(`server-*.md`). It is readable only by browsing the repo on GitHub.
There is no index, no navigation, no front matter, no build, and nothing
serves it to the people who are supposed to read it.

**There is no user guide.** Both audiences currently covered are
privileged ones: a Go developer calling the library, and an operator
running the daemon. The person who submits jobs through the web UI, the
MCP chat assistant or the REST API -- and who administers nothing -- has
no document addressed to them.

**The reference material drifts, and nothing catches it.** The
configuration reference is a hand-maintained Markdown table. The OpenAPI
schema is a 5,953-line JSON document hand-written inside a Go backtick
string literal in `webapi/httpserver/openapi.go`. Neither is checked
against the code. The audit below found this has already cost us a dead
operator knob.

**Nothing is aimed at the eventual merge.** The stated goal is to reuse
this content in the HTCondor manual, which is Sphinx RST with custom
roles. If we author in a format and with conventions that cannot survive
that trip, we will write everything twice.

## What exists already (measured)

Measured against `603d57f6` (2026-11-24), on top of `origin/main`
`bcbf79fe`. The `docs/` row describes the tree as it was before stage
1a restructured it, since that is what motivated the work.

| Asset | State |
| --- | --- |
| `docs/` | 17 files, 6,018 lines. Good prose. No front matter, no nav, not served, not built. |
| `README.md` | 104 lines. |
| Next.js app | `webapi/frontend/`, Next 16, static export to `out/`. `react-markdown` and `remark-gfm` are already dependencies. Playwright configured, with a demo-mode e2e harness. |
| OpenAPI | `webapi/httpserver/openapi.go`, 5,953 lines of JSON in a Go string constant. No `.yaml`/`.json` artifact on disk. |
| Config knobs | No registry. 245 distinct `HTTP_API_*` string literals across the Go sources. |
| `webapi/condordocs` | Embeds *upstream* HTCondor RST for the MCP doc tools. Unrelated to this repo's own docs, but the precedent for shipping docs inside the binary. |

### The knob audit

Method: `HTTP_API_[A-Z0-9_]*` string literals in Go sources under
`webapi/` and `htcondor-api/`, against the same pattern in backticks in
`docs/*.md`.

- 245 distinct knob names appear in the code.
- 159 distinct knob names appear in the docs.
- 87 appear in code but in no document. Discounting per-site instance
  names that only exist as examples (`HTTP_API_PICKER_LAB_A_*`,
  `HTTP_API_SITE_APP_SEQQC_*`, `HTTP_API_CONNECTOR_SITE_*`) and bare
  prefixes, about **52 are real, undocumented settings**. They include
  `HTTP_API_TLS_CERT`, `HTTP_API_TLS_KEY`, `HTTP_API_TLS_CA_CERT`, the
  whole `HTTP_API_OAUTH2_*` external-IdP group, the
  `HTTP_API_IDEMPOTENCY_KEY_*` group, `HTTP_API_READ_TIMEOUT` and
  `HTTP_API_IDLE_TIMEOUT`. These are not obscure.
- 1 appears in the docs but in no code, and it is a worked example
  rather than a claim that a setting exists.

The expensive case is the knob that is read somewhere, but not where it
is documented to work. `HTTP_API_SPOOL_BUFFER_DIR` is documented for
operators in `operator/ssh-gateway.md` and `user/files.md`. The only
`config.Get` for it in the tree is at `main.go:3115`, inside
`runDemoMode`. The ordinary startup path never reads it: the field it
feeds, `SpoolBufferDir`, is plumbed through `httpserver` and `sftpjobs`
and is otherwise set only by tests. An operator who sets the documented
knob on a real deployment gets the in-memory path, a much smaller
ceiling than they asked for, and no warning in the log.

This is the shape of drift that costs the most, because it defeats the
obvious check. Grepping for the name finds a hit, so the knob looks
wired; only reading the call site shows that the one caller is demo
mode. A name-matching drift test would pass on it. A registry that the
server must read through would not, because the production path would
have to name the knob to use it.

That bug is the argument for the whole configuration-generation stage,
and it is already in the tree.

## Decisions

### One content tree, audience-split, task-shaped

Three top-level audiences, as proposed: **user guide**, **operator
guide**, **developer and API reference**. The existing files map onto
the last two almost unchanged.

Two rules beyond the split:

*The file layout is the URL.* No separate route table to keep in sync
with the directory.

*Pages are tasks, not subsystems.* The current files are per-subsystem
and some have grown past the point where a reader can find anything:
`docs/operator/ssh-gateway.md` is 637 lines and is really about five
different things. Splitting on the way in is cheaper than splitting
later, once links exist.

Front matter on every page: `title`, `audience`, `order`, `since`. A nav
manifest derived from the tree, not hand-written.

### MyST-flavored Markdown, not RST

This is the decision that determines whether the reuse goal survives.

Sphinx ingests MyST Markdown natively through `myst-parser`, and the
Next.js app already has the Markdown toolchain installed. Authoring in
MyST means the same files feed both renderers, and the eventual move
into the HTCondor manual is a move, not a translation.

Keep the custom roles from `docs/STYLE.md` to a short whitelist --
`:macro:`, `:subcom:`, `:ad-attr:`, `:tool:`, `:classad-function:`.
They are link generators and nothing more, so a small remark plugin
handles them on the Next.js side while Sphinx handles them natively.
Everything outside that whitelist stays plain CommonMark + GFM, so the
content degrades gracefully if the Sphinx move never happens.

Authoring in RST and rendering it in Next.js is the inverse of this and
is considerably more painful; see rejected alternatives.

### Config knobs: YAML is the source, the type system is the enforcement

A YAML file that merely sits next to Go code calling `config.Get("...")`
does not constrain anything, and drifts the way the Markdown table
drifted. What closes the hole is making the generated identifier the
only thing a read will accept. Then the YAML is load-bearing, because a
knob that is not in it cannot be named.

**The knob type.** `config` grows an opaque type whose fields are
unexported:

```go
package config

// Knob names a configuration setting. Values come from generated
// tables; the zero value is invalid.
type Knob struct {
	name  string
	def   string
	scope Scope // Startup or Reconfig
}

func (k Knob) Name() string { return k.name }
```

No exported constructor and no exported fields: outside the generating
package a `Knob` cannot be written down. `cfg.Get(knob)` then accepts
nothing a generator did not produce, and `cfg.Get("HTTP_API_NEW_THING")`
stops compiling.

**Typed accessors, not a bare Get.** The YAML already has to carry a
type and a default for the documentation, so generate them into the
accessor rather than restating them at each call site:

```go
addr := knobs.ListenAddr.Get(cfg)              // string
mcp  := knobs.EnableMCP.Get(cfg)               // bool, default applied
ivl  := knobs.RuntimeEstimateInterval.Get(cfg) // time.Duration
cap  := knobs.SpoolMaxUploadBytes.Get(cfg)     // int64, parses "1000M"
grps := knobs.WebUIAccessGroup.Get(cfg)        // []string, comma or JSON
```

A handful of concrete knob types -- `StringKnob`, `BoolKnob`,
`DurationKnob`, `BytesKnob`, `PathKnob`, `ListKnob` -- each embedding the
opaque base. Not generics; `config.Get[string]` at every call site buys
nothing.

This also retires parsing that is hand-written once per knob today.
`HTTP_API_SPOOL_MAX_UPLOAD_BYTES` carries its own "zero, negative or
unreadable means warn and use the default" logic; a `BytesKnob` does
that once for every knob of that shape, and the documented default and
the applied default become the same literal.

**Scoping must not be reimplemented.** `Get` resolves
`<SUBSYS>.<LOCALNAME>.<KEY>` before looking a name up. `Knob.Get(cfg)`
has to route through that same path. It is the obvious place for a quiet
divergence, so it is worth saying out loud.

**Aliases and deprecation belong in the YAML.** `HTTP_API_LISTEN` is an
accepted alias for `HTTP_API_LISTEN_ADDR`, and the warning for it is
hand-written in `main.go`. An `aliases:` list generates both the lookup
and the warning, and the reference page says so without anyone
remembering to write it down.

### Templated knobs: generate a function, not a pattern

Three knob families take an instance name in the middle of the
parameter -- pickers, connectors and site apps. They are not an edge
case to bolt on afterwards; they are roughly a third of the
undocumented list.

The sketch `config.Get(config.Build("HTTP_API_PICKER_LAB_A_*", "FOO"))`
has the right shape but keeps a string at the call site, and a typo in
the pattern still compiles. Instead let a templated YAML entry generate
a *function*:

```yaml
family: picker
prefix: HTTP_API_PICKER_
enumerated_by: HTTP_API_PICKERS
settings:
  - { name: PROVIDER,  type: string, doc: "..." }
  - { name: API_KEY,   type: string, doc: "...", secret: true }
  - { name: CONNECTOR, type: string, doc: "..." }
```

```go
func PickerAPIKey(instance string) config.StringKnob
func PickerProvider(instance string) config.StringKnob

key := knobs.PickerAPIKey("LAB_A").Get(cfg)
```

The template exists once, in the YAML. The instance is the only free
variable at a call site. Nothing constructs a `Knob` by hand, so the
internal type never leaks in the sense that matters: it is a value you
receive, never one you build.

This refactor has a target already in the tree, and it fixes a bug on
the way. Three families each carry their own key builder --
`connectors.ID.ConfigKey` (through `MacroID`),
`connectors.PickerConfigKey`, and `siteapps.configKey` -- and all three
apply the same rule: uppercase the instance, turn `-` into `_`.
Connectors and site apps both guard the collision that rule creates,
where `lab-a` and `lab_a` resolve to one parameter name. **Pickers do
not.** One generated constructor per family makes the rule, and its
guard, exist once.

Generate the enumerating knob alongside it, since a family and its
listing are related only by convention today:

```go
func Pickers(cfg *config.Config) []string   // reads HTTP_API_PICKERS
```

**On avoiding a leaked internal type.** Worth being precise about what
would leak. Keeping `config.Knob` exported but unconstructable is
enough: callers hold values of a type they cannot forge, which is the
property we want, and an exported opaque type costs nothing. Hiding it
entirely -- exposing only `knobs.PickerAPIKey("LAB_A").Get(cfg)` with no
nameable intermediate -- would force every generated knob into a single
package and stop a knob being passed around as a value, which the
reconfigure table needs to do. Export the type; withhold the
constructor.

**Enforcement is a lint, and a small one.** `Get(string)` has to stay:
`config` is a general HTCondor configuration reader, and the library,
`condor_config_val` and arbitrary macro lookups all need it. So scope
the invariant to the server: *no string literal reaches `.Get(` under
`webapi/`*. That is checkable in a few lines, and it is reachable,
because the ~50 stock HTCondor knobs `webapi` reads (`COLLECTOR_HOST`,
`UID_DOMAIN`, `SPOOL`, `SEC_TOKEN_POOL_SIGNING_KEY_FILE`) can be
generated too -- from the `param_info.in` snapshot this repo already
vendors into the generated `config/param_defaults.go`. One generator,
two inputs: upstream's `param_info.in` for stock knobs, our YAML for
`HTTP_API_*`.

**Migration.** 395 `.Get("` call sites under `webapi/`. Not one pull
request, and not by hand:

1. Generate the table. It compiles beside the existing string calls and
   changes no behaviour.
2. Rewrite mechanically, one package at a time.
3. Turn the lint on per-directory, with an allowlist that only shrinks,
   so a finished package cannot regress behind you.

The knobs with no YAML entry are exactly the undocumented ones, so the
migration is the documentation pass. That is the forcing function this
stage exists for.

### OpenAPI: a file in the repo, not a Go string literal

Hand-authoring an OpenAPI spec is fine. Hand-authoring it inside a Go
backtick string is what guarantees drift, and we have already been
bitten once by a stale vendored copy of this spec.

Move the schema to `openapi.yaml` in the repo, `go:embed` it, and serve
it from the same endpoint as today. The API reference page then renders
from the spec with Scalar or Redoc -- one component, pointed at a URL.

Contract-testing the handlers against the spec is the follow-on, not a
prerequisite. Getting the spec out of the string literal is most of the
value, because it makes the spec reviewable in a diff.

### Rendering: in-product first, public site second

The Next.js + rendered-Markdown precedent at
`portal.osg-htc.org/documentation/` is a real reason to use this stack:
it is what the OSG team already maintains.

But the highest-value rendering target is not a standalone public site.
It is **in-product documentation**: a `/docs/[...slug]` route inside the
existing `webapi/frontend/` app. That gets us three things a separate
site cannot:

- Users read documentation matched to the version actually deployed at
  their site, not to whatever is on `main`.
- Documentation inherits the site's branding, like every other page.
- The chat assistant can cite this repo's own documentation the way it
  already cites upstream HTCondor docs through `webapi/condordocs`.

The public site is then the same content with a different shell. One
content tree, two renderers, no duplicated prose.

One known trap: static export plus a dynamic route needs
`generateStaticParams`. A docs slug route is precisely that case, so it
works, but the params generator has to be written and will be the first
thing that breaks if the content tree moves.

### Always-latest, with `since:` front matter

This repo is pre-1.0 and moving fast, and official-looking documentation
makes the surface read as frozen to anyone who finds it.

Do **not** build a versioned documentation matrix (`docs/v0.26/...`).
Render always-latest, carry a `since:` field in front matter, and show a
version badge on anything recent. Version accuracy matters most to
someone running a specific deployment, and the in-product copy already
pins itself to the version they are running. Adding a version matrix
later is easy; unwinding one is not.

### Screenshots: Playwright against the demo pool, regenerated on release

`webapi/frontend/` already has `playwright.config.ts`, an `e2e/` suite
and a demo-mode harness. Add a `docs-shots` Playwright project whose
only job is to drive the demo pool with fixed data and write PNGs to a
known path.

Three constraints, learned the hard way:

- Screenshot specs break every time a UI panel moves. Keep them few and
  the selectors semantic.
- Regenerate on release, not per-PR, and never let them gate a merge, or
  they become the standing reason the branch is red.
- Alt text has to be hand-written. A screenshot with no alt text is dead
  weight now and unusable in the HTCondor manual later.

This is the most fragile automation of the three and the least
load-bearing, which is why it is last in the plan.

## Rejected alternatives

**A YAML knob file read by convention, with `Get(string)` left in
place.** This was the first draft of this note's recommendation, and it
was wrong in both directions. A YAML that does not constrain the code
drifts exactly as the Markdown table did; but a YAML whose generated
identifiers are the *only* thing a read accepts constrains it
completely, and is better than a hand-written Go registry because the
documentation and the accessor come from one declaration. The rejected
thing is the convention, not the YAML.

**A hand-written `param.Register(...)` registry in Go.** Equivalent
enforcement, but it puts the prose documentation inside Go source, where
it is harder to review as prose and cannot be the input to anything
else. The YAML is the better authoring surface for a page a human reads.

**Authoring in RST to match the HTCondor manual.** Optimizes for a merge
that has not been scheduled, at the cost of every day until it happens:
RST in Next.js is a bad experience, and the in-product docs are the near
term win. MyST reaches the same destination without the tax.

**A standalone public documentation site first.** Slower to the
users who most need it, and it cannot do version-matched or
site-branded documentation at all. Build the in-product route first; the
public site is a second shell over content that already exists by then.

**Generating the knob reference by grepping `config.Get` permanently.**
Fine as a stepping stone and as a CI check. Not a destination: a grep
cannot recover a default, a type or a reload scope, so the generated
page would still need hand-written columns, which is where we are now.

**Versioned documentation from the start.** A build matrix, a version
switcher and a backport policy, for a pre-1.0 project whose in-product
docs are already version-pinned by construction.

## Staged plan

Each stage is independently landable.

**1a. Content model.** *Done.* Front matter, audience directories, the
generated nav manifest and the validating test; the 17 existing files
moved in verbatim. No prose changed.

**1b. Audience splits.** *Partly done.* `ssh-gateway.md` is split:
what it is like to use became `user/ssh.md` and `user/files.md`, and the
operator page keeps what a site switches on. Its certificates section was
the one genuinely interleaved piece and was divided paragraph by
paragraph -- obtaining and using a certificate on the user side, what
issuing them costs the access point on the operator side.

`agents.md` turned out **not** to split, which this note previously
assumed it would. Every section is written to the operator, "What to
hand a user" included: it tells an operator which address to give out,
not a user what to do with it. There is no user-facing prose in it to
move, so a user page about the chat assistant has to be written rather
than extracted. That is stage 4.

`multi-ap.md` was a different problem again, and not an audience one:
625 lines carrying three headings and nineteen bold lead-ins, one of
which needed an explicit `<a id>` to be linkable at all. It now has
twenty-nine headings; every lead-in that opened a paragraph is a
section, grouped under six of them. The explicit anchor is gone, since
`### Two hubs` slugs to the id it was standing in for. No prose moved:
the only words that changed are a trailing period dropped from each
label that became a heading.

**2. Knob YAML, generated accessors, lint.** The largest stage and the
one that pays for itself on landing: three real defects are waiting
(`HTTP_API_SPOOL_BUFFER_DIR` read only inside `runDemoMode`, so the
documented knob does nothing on a real deployment; ~52 real knobs
undocumented, including TLS and external-IdP settings; picker instance
names colliding unguarded), and it removes the duplicate reconfig table.
Substages, each landable: the `Knob` types and generator; the YAML for
one package plus its mechanical rewrite; the templated families; the
stock-knob generator from `param_info.in`; the lint, switched on
per-directory. A grep-based drift test can ship on day one and retires
when the lint covers everything.

*The generated reference has landed*, out of order with the rest of the
stage and deliberately so: it covered a fraction when it landed -- 22
declared settings of roughly 240 -- but from here every setting
converted documents itself.

Where that stands: 99 settings are declared and 111 names are still
read as string literals outside generated code. Of the settings the
server reads, **none is undocumented any more** -- the two names a
census still turns up, HTTP_API_WEBUI_PRIVACY and HTTP_API_WEBUI_TERMS,
are labels handed to resolvePolicy for its messages rather than
settings anything looks up, and the real pair is _FILE and _URL, both
of which the reference carries.

The lint this stage planned did not happen and is not going to. Two
tests do its work without a golangci-lint plugin and with failure
messages that name the fix: a declared setting is never also spelled by
hand, and everything not yet declared is a frozen inventory in
testdata/undeclared-settings.txt that fails in both directions.
Declaring a setting is therefore a two-line change, the table gaining an
entry and the inventory losing one, and nothing new can be added
stringly without a deliberate edit to a file whose header says not to.

**The conversion is worth doing for the bugs, not only for the
reference.** Across six batches, forty-three settings turned out to be
in reconfigParams nowhere, which by that file's own reasoning means a
change to them was neither applied nor reported: nine OAuth2 and
identity-provider settings, six of the last undocumented nine, every
one of the eighteen SSH gateway and sandbox settings, the four
JupyterLab limits, both MCP request deadlines, and four of the five
chat-assistant settings. The SSH group was picked for the batch
*because* a count said it held fifteen of the then seventy-one in that
state; the prediction was right and the test confirmed each one on
declaration. A batch that finds nothing has not happened yet. Left until the conversion was finished, the whole exercise
would have paid nothing out until the last setting moved. It writes
into a marked region of `docs/operator/config.md` rather than a page of
its own, so the prose no table can generate stays where it is, and the
settings not yet in the table keep their hand-written rows below it.
Three checks came with it: the region is current, every declared
setting has an entry, and no declared setting also has a hand-written
row -- that last one being what stops the two halves describing one
setting differently while they coexist. Converting those four rows
needed a `see:` field, because three of them carried a cross-reference
into another page and a paragraph that a Go doc comment also prints
cannot hold a relative Markdown link; the reference renders it as a
link that the documentation tests then check resolves, and the comment
gets the title alone.

*Not every undocumented setting wants documenting.* Some are
deliberately undocumented because they are obsolete and their use is to
be discouraged, which the table now says with `obsolete:`. Such a
setting is declared like any other -- the table is the only place a
name may be spelled -- and then left out of the reference entirely,
because documenting it, even as obsolete, is how somebody finds it and
decides to use it. The people who need to know are the ones who already
have it set, and `knobs.Obsolete` is what lets the daemon tell them.
Three things make the marker do work rather than describe an intention:
the generated declaration carries Go's own `Deprecated:` paragraph, so
staticcheck reports every read and the one place allowed to read it
carries a `//nolint` saying why; a test checks the name appears nowhere
under `docs/`; and `superseded_by`, where it is set, has to name a
setting some page does mention, since a pointer into nothing is worse
than no pointer. That last pair is why the check matches whole
parameter names -- `HTTP_API_LISTEN` is a prefix of the current
`HTTP_API_LISTEN_ADDR`, and a substring match read every page
documenting the current setting as documenting the obsolete one.

**3. OpenAPI out of the Go string.** *Done, ahead of stage 2's
completion, because it depended on nothing.* The document is
`webapi/httpserver/openapi.json`, embedded with `go:embed`;
`openapi.go` went from 5,953 lines to 230. What is served is unchanged,
which was checked rather than assumed: the handler already re-encoded
the document with sorted keys, so the old literal and the file have the
same canonical encoding. Three checks came with it -- the document is
OpenAPI-shaped, every internal `$ref` resolves, and the OAuth2 flow
object is where `setOpenAPIOAuth2URLs` walks to find it, that last one
being a silent no-op if any of those keys move.

**The route check landed** and this paragraph said otherwise for a
while: `openapi_routes_test.go` asks the server's own mux whether each
documented path is routed, and checks the other direction against a
frozen inventory of 41 `/api/v1/` routes in no OpenAPI path, mostly
web-console endpoints. Whether the document is meant to describe those
is a product question, not a documentation one; freezing the set is
what stops it growing while nobody decides.

**Structural validation landed too, and not as a meta-schema
validation.** Five classes, each one a thing that breaks a generated
client: operationIds present and unique, every `{placeholder}` in a
path template declared, every response described, every `required`
property defined, no orphan component schema. The meta-schema was
rejected deliberately -- it has to be vendored, it reports failures in
schema language rather than in terms of this document, and it would
miss the sharpest of the five, since two operations sharing an
operationId is spec-valid and still makes one endpoint uncallable.

All five passed on the document as it stood: 128 operations, zero
defects. They are a ratchet, like the undeclared-settings inventory,
and their value is in what they refuse next.

Still to do here: a rendered reference page, which is stage 6's
business rather than this one's.

**What the document was actually wrong about was its prose, not its
structure** -- see the openapi.json entry under "What the conversion
keeps finding in shipped artifacts" below. Worth stating because it
sets where to look next: the shapes are checkable and now checked, and
the sentences are not.

**4. The user guide.** *Started.* The genuinely missing audience.
Everything before this exists to make writing it, and keeping it
correct, cheap.

`user/assistant.md` is the first page written rather than moved, which
this note predicted it would have to be. It deliberately does not
repeat the per-client setup steps: the access point serves those live
at /mcp, and the clients change their configuration formats faster than
this guide changes, so a copy would be wrong within a release. What a
written page adds is what the served one cannot -- that the assistant
acts as the user and gains them nothing new, and the three things worth
care, which are that it works at a volume a person would pause at, that
submitting a job is running code whatever decided to submit it, and
that its own account of what it did is not the record.

Writing a user page has a failure mode the rest of this stage does not:
nothing compiles, so a plausible and wrong sentence survives. One did,
briefly -- a claim that retrying a timed-out watch is safe, when the
watch may already be registered and a retry would make a second. Every
capability the page names was checked against the tools the server
actually registers, and every limit against a setting a site really
has.

**5. Screenshots.**

## The largest remaining gap: connectors

`webapi/connectors` is a shipped feature with **no page in `docs/` at
all** -- not user, not operator. A user grants this server access to
their own Google account; their job's input can come out of Drive and
its results can be written to a Sheet; an administrator configures each
connector with a dozen settings and a per-connector group gate. The
only prose about it is `design_notes/CONNECTORS.md`, which is a design
note written for whoever builds the two bootstraps that are still
unbuilt, not for either audience.

It is the largest gap because of what it asks of the user. Every other
undocumented thing here is a mechanism; this one is a consent decision
about an account that is not ours, made by somebody who has to
understand what they are granting and how to take it back. That is the
one place prose is not a convenience.

It wants two pages. A user page: what a connector is, what granting one
lets this access point do as you, that the grant outlives the browser
session, and how to revoke it. An operator page: the kinds, the group
gate, `transfer: spool` versus `direct` and why spool is the default
that keeps a third-party token off every execute node.

Neither is written here because both need their claims checked against
`handlers_connect.go` and the store the way the two user-guide pages
were, and that is a session's work rather than a paragraph's.

## What the conversion keeps finding in shipped artifacts

The knob work's record -- forty-three settings in `reconfigParams`
nowhere -- is the same shape as what documenting a surface turns up in
it, and two more came out of writing the user guide.

`DELETE /jobs/{jobId}` and `PATCH /jobs/{jobId}` were described in
`openapi.json` as "NOT YET IMPLEMENTED" with a single 501 response,
long after both handlers grew owner scoping, superuser impersonation
and an audit trail. A client generated from the document could not
remove or edit a job. Nothing caught it: the routes exist, so the
route-coverage check passed, and the document agreed with itself, so
every check inside it passed too. Both halves were wrong together, and
that is the general case -- a stale document is self-consistent.

The guard added with the fix does not read the handler, and says so.
Deciding whether a route is implemented means calling it, and these
need a credential and a schedd first. What it does is make the claim
expensive: an operation documented as unimplemented has to be on a
named list that cannot outlive what it excuses, and the list is empty.
Nobody chose to leave those two stale; they were never revisited, and
that is the half a check can close.

A user-guide draft written in the same pass said `GET
/jobs/{id}/stdout` worked "while it runs, and after". It does not -- it
reads the returned output sandbox, and `/peek` is the mid-run path --
and nothing would have failed. A prose page compiles nothing, so the
only defence is checking each claim against the code that implements
it, one at a time. That is slow and there is no substitute for it; what
makes it affordable is that the claims worth making are few.

## A setting hidden before its replacement exists

*Resolved.* The two MCP group settings now have replacements, so the
shape this section described has been chosen rather than left open; the
history is kept because the trap generalises.

`HTTP_API_MCP_ADMIN_GROUP` and `HTTP_API_MCP_SUPERUSER_GROUP` were
marked obsolete and left out of the documentation, which is what was
asked for. They were also the **only** source of the `mcp:admin`,
`mcp:superuser`, `anyuser:read` and `anyuser:write` scope grants, so an
operator needing an OAuth2 client to read across users had no
documented way to configure it -- and neither setting could carry a
`superseded_by`, because a pointer at a setting that would not restore
what they had is worse than no pointer.

The fix was the first of the two shapes: the scope grants now answer to
settings of their own,
`HTTP_API_ANYUSER_{READ,WRITE}_{GROUPS,USERS}`, which is step 3 of
`design_notes/ENTITLEMENT_VOCABULARY.md` for that pair. Both old
settings now point at a replacement that a page really documents, so
`TestObsoleteReplacementsAreDocumented` is checking a real pointer
rather than skipping.

**The generalisable part**: marking a setting obsolete is a claim that
something else does its job. Where nothing does, hiding it removes the
only documented way to configure a live capability, and the
`superseded_by` check cannot see that -- it verifies a pointer that was
set, not one that is missing. The order has to be replacement first,
marker second.

## Open questions

- Where does the public site live, and who operates it? The in-product
  route needs no answer to this; the public shell does.
- Does the user guide document the web UI, the MCP assistant and the
  REST API as one thing or three? They are three interfaces onto the
  same operations, and a single task-shaped page per operation with
  three tabs may serve better than three parallel guides.
- How much of `design_notes/` should become published documentation?
  Some of it is reasoning that belongs here; some is reference material
  that users and operators currently cannot find.
- Naming. What users see is "the HTCondor API server and web interface",
  not "golang-htcondor". Titles should use the former from the start,
  since they are what survives the merge.
- A design note describes intent at a moment and drifts from the code
  like anything else. Once stage 2 exists, the generated reference is
  authoritative for knobs and this note should stop restating counts.
- Where do the generated knob accessors live? `webapi/knobs` is the
  natural home for `HTTP_API_*`, but stock HTCondor knobs generated from
  `param_info.in` belong to the base module, so there will be two
  packages and call sites will import both.
- Does a `secret: true` knob (client secrets, API keys) need anything
  from the type system beyond documentation -- a `SecretKnob` whose
  value does not land in a log line by accident?
