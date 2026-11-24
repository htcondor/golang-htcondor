---
title: Web UI branding
audience: operator
order: 90
---

# Web UI branding

Making the web interface say who runs this access point. Part of the
[HTTP API + MCP server](index.md) documentation.

The web interface ships with CHTC's own presentation: the pages are
called "HTCondor Access Point" and the footer carries the CHTC,
Morgridge and UW-Madison logos. On an access point somebody else runs,
that footer answers "who operates this service?" with the wrong
institution.

These settings change it without a rebuild. All of them are optional,
and an access point that sets none of them serves exactly the pages it
serves today -- the server injects nothing into the HTML at all.

| Knob | Effect |
| --- | --- |
| `HTTP_API_WEBUI_SITE_NAME` | The heading in the sidebar and the browser tab. Default `HTCondor`. |
| `HTTP_API_WEBUI_SITE_TAGLINE` | The line under the heading. Defaults to `Access Point`, and to nothing once `HTTP_API_WEBUI_SITE_NAME` is set -- a site that calls itself an analysis facility is not also an "Access Point" unless it says so. |
| `HTTP_API_WEBUI_FOOTER_LOGOS` | JSON array of `{"src", "alt", "href"}`, in order. Unset keeps the CHTC/Morgridge/UW band; `[]` removes it. |
| `HTTP_API_WEBUI_BRANDING_DIR` | A directory served read-only under `/branding/`: the site's logos and its favicon. |
| `HTTP_API_WEBUI_CUSTOM_CSS` | A stylesheet served at `/branding.css`, loaded after the app's own. |
| `HTTP_API_WEBUI_FAVICON` | URL for the browser tab icon, replacing the HTCondor mark. Normally a file in the branding directory. |
| `HTTP_API_WEBUI_PRIVACY_FILE` | A Markdown privacy notice, served at `/privacy`. The literal `default` serves the template this software ships. |
| `HTTP_API_WEBUI_PRIVACY_URL` | Where `/privacy` redirects instead, for a site whose privacy notice lives elsewhere. |
| `HTTP_API_WEBUI_TERMS_FILE` | A Markdown terms of use, served at `/terms`. `default` likewise. |
| `HTTP_API_WEBUI_TERMS_URL` | Where `/terms` redirects instead. |

All of them are applied by `condor_reconfig`, including the ones that
name files, so trying a logo out does not cost a restart.

A worked example:

```
HTTP_API_WEBUI_SITE_NAME    = UChicago ATLAS Analysis Facility
HTTP_API_WEBUI_BRANDING_DIR = /etc/condor/branding
HTTP_API_WEBUI_CUSTOM_CSS   = /etc/condor/branding/site.css
HTTP_API_WEBUI_FAVICON      = /branding/favicon.svg

HTTP_API_WEBUI_FOOTER_LOGOS @=json
  [
    {"src": "/branding/af.svg",  "alt": "UChicago ATLAS Analysis Facility", "href": "https://af.uchicago.edu/"},
    {"src": "/branding/uc.svg",  "alt": "University of Chicago"}
  ]
@json
```

`alt` is required: an unlabelled institutional logo tells somebody using
a screen reader nothing about who runs the service. `src` and `href`
must be site-relative paths or absolute `http`/`https` URLs. A list that
does not parse, or an entry that is not usable, leaves the previous
footer in place and says why in the log -- a typo in the logos should
cost a site its logos, not its web UI.

**Colours.** The palette is defined as CSS custom properties, so
redefining them in the custom stylesheet recolours the whole interface:

```css
:root {
  --color-brand-600: #800000;
  --color-brand-700: #6b0000;
}
```

The browser's content security policy on these pages is
`default-src 'self'`, so a stylesheet that pulls a web font from a
third-party host will not load it. Put fonts in the branding directory
and reference them under `/branding/`. Logos are the exception: the
origin of an absolute `src` or favicon URL is added to the policy's
`img-src`, so a logo served from the institution's own web server
works without anything further.

**What the branding directory will serve.** It is same-origin with the
whole web UI, so an `.html` or `.js` file left in it alongside the logos
would run with a signed-in user's session. Only images, fonts and
stylesheets are served from it (`.svg`, `.png`, `.jpg`, `.jpeg`, `.gif`,
`.webp`, `.avif`, `.ico`, `.woff`, `.woff2`, `.css`); anything else is a
404, as is anything reached by a symlink out of the directory.

**The HTCondor credit.** Where a site supplies its own footer logos --
including the empty list -- the band is followed by one small line
naming HTCondor and the Center for High Throughput Computing. Who runs
the service and who wrote the software are different questions, and
replacing the answer to the first does not erase the second. Sites that
leave the footer alone see no such line: the shipped band already says
it.

**For a separately-hosted SPA.** `GET /api/v1/webui/branding` returns
the same values as JSON, unauthenticated. The pages this server serves
carry them in their own HTML and never call it.

## Policy pages

A site that offers accounts to people outside its own staff generally
needs somewhere to say what it does with their data and on what terms
they may use the service. Two readers want those pages at a stable,
unauthenticated URL on this server's own domain: somebody reading the
footer, and the review process of an OAuth2 provider whose users' data
an access point asks for -- Google will not approve an application that
has no privacy policy and no terms of use.

```
HTTP_API_WEBUI_TERMS_FILE   = /etc/condor/terms.md
HTTP_API_WEBUI_PRIVACY_URL  = https://www.wisc.edu/privacy-notice/
```

Each page takes a file or a URL. The file is Markdown, rendered by the
server into a standalone page that needs no JavaScript and no session.
The URL form exists because the common case for a privacy notice is
that the institution already has one: a site should be able to point at
it rather than keep a second copy that drifts from the first. Either
way `/privacy` and `/terms` answer, so there is one address to give
out; with a file they render, with a URL they redirect. The footer link
points wherever the reader is really going.

**Nothing is served by default.** A policy page is a legal statement
about a particular deployment, and a plausible default that nobody has
read would make promises on an operator's behalf that their site may
not keep. An access point that configures neither setting has no pages
-- the routes 404 -- and no policy links in its footer.

What this software ships instead is a reviewed template for each page,
selected by setting the file to the literal `default`:

```
HTTP_API_WEBUI_PRIVACY_FILE = default
```

Both templates are starting points, written to be accurate for an
unmodified deployment, and both carry bracketed placeholders --
`[CONTACT EMAIL]`, `[RETENTION PERIOD]` and so on -- that an operator is
expected to replace. Copy them out of
`webapi/httpserver/webui/resources/`, edit them, and point the setting
at the result. The server warns in its log whenever the document it is
serving still contains a placeholder, because a half-finished policy
renders perfectly and the operator who configured it already knows what
it was meant to say.

**What the Markdown may contain.** It is trusted like the site
stylesheet, since it comes from the same configuration file -- with one
exception. Embedded raw HTML is dropped rather than rendered: a policy
document is the kind of text that gets pasted in from a lawyer, a
vendor template or another institution's site, and script arriving that
way would run same-origin without anybody choosing it.
