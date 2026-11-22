package webui

import (
	"bytes"
	_ "embed"
	"fmt"
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// The site's policy pages: a privacy notice and terms of use.
//
// A site that offers accounts to the public needs somewhere to say what
// it does with people's data and on what terms they may use it. Two
// things want those pages at a stable, unauthenticated URL on this
// server's own domain: a person reading the footer, and an OAuth2
// provider's review of an application that asks for their users' data,
// which will not approve one without them.
//
// Neither page exists unless the operator configures it. A policy page
// is a legal statement about a particular deployment, and a plausible
// default that nobody has read is worse than no page at all: it would
// make promises on an operator's behalf that their site may not keep.
// So an unconfigured server has no pages and no footer links, and the
// routes 404 -- which, as with the branding assets, is a clearer answer
// to an operator checking their paths than a route that is not there.
//
// What this package ships is a reviewed template for each page, served
// only when the operator asks for it by name (see DefaultSource). They
// are starting points, written to be accurate for an unmodified
// deployment, and both carry bracketed placeholders their operator is
// expected to replace.
//
// # Trust
//
// The markdown is operator-supplied configuration, read from a path in
// the configuration file. It is trusted to the same degree as the site
// stylesheet, which is to say entirely -- an operator who can set
// HTTP_API_WEBUI_CUSTOM_CSS can already do more to a page than markdown
// can. The one thing worth stopping is raw HTML, because a policy
// document is the kind of text that gets pasted in from a lawyer, a
// vendor template or another institution's site, and script arriving
// that way would run same-origin without anybody choosing it. Goldmark
// is configured without its Unsafe option, so embedded HTML is dropped
// rather than rendered.

const (
	// PrivacyPath and TermsPath are where the pages are served. They
	// are fixed, so that an operator who has configured a policy has
	// one URL to give out -- to an OAuth2 provider's review form, say
	// -- whether the text is served from here or lives elsewhere.
	PrivacyPath = "/privacy"
	// TermsPath is the other of the pair.
	TermsPath = "/terms"

	// DefaultSource is the value of HTTP_API_WEBUI_PRIVACY_FILE or
	// HTTP_API_WEBUI_TERMS_FILE that selects the template this package
	// ships, instead of a path on disk.
	//
	// Opt-in rather than a fallback for an unreadable path: a site that
	// meant to serve its own policy and mistyped the path should find a
	// 404 and a warning in the log, not a template published under its
	// name.
	DefaultSource = "default"
)

//go:embed resources/default_privacy.md
var defaultPrivacyMarkdown []byte

//go:embed resources/default_terms.md
var defaultTermsMarkdown []byte

// DefaultPrivacyMarkdown and DefaultTermsMarkdown return the shipped
// templates. Copies, because the caller renders them and the embedded
// bytes are shared.
func DefaultPrivacyMarkdown() []byte { return bytes.Clone(defaultPrivacyMarkdown) }

// DefaultTermsMarkdown is the shipped terms-of-use template.
func DefaultTermsMarkdown() []byte { return bytes.Clone(defaultTermsMarkdown) }

// PolicyURLOK reports whether a configured policy URL is one this UI
// will redirect to or put in an href: site-relative, or absolute
// http(s). Same rule, and the same reasons, as a configured logo.
func PolicyURLOK(raw string) bool { return assetURLOK(raw) }

// PolicyPage is one rendered policy document.
type PolicyPage struct {
	// Title heads the page and names the browser tab, e.g. "Privacy
	// Notice". The document's own first heading is kept as well; this
	// is what the tab and the back-link say.
	Title string
	// SiteName is the deployment's name, for the tab title and the
	// link back to the application.
	SiteName string
	// Markdown is the document, as configured.
	Markdown []byte
	// CustomCSS links the site stylesheet, so a deployment that has
	// recoloured the UI gets a policy page that matches it.
	CustomCSS bool
	// Rev busts the stylesheet's cache, as elsewhere.
	Rev uint64
}

// RenderPolicyPage renders one policy document as a standalone HTML page.
//
// Standalone, and not a route in the single-page app, for three reasons:
// the page has to render for a reader with no JavaScript (an OAuth2
// provider's reviewer may be one, and so is a crawler); it has to work
// before anybody has signed in; and it is the one part of this UI whose
// exact text matters legally, which is an argument for the server
// emitting it directly from the configured bytes rather than through a
// client-side renderer.
func RenderPolicyPage(p PolicyPage) ([]byte, error) {
	var body bytes.Buffer
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	if err := md.Convert(p.Markdown, &body); err != nil {
		return nil, fmt.Errorf("rendering the policy document: %w", err)
	}

	site := strings.TrimSpace(p.SiteName)
	if site == "" {
		site = DefaultSiteName
	}
	title := strings.TrimSpace(p.Title)

	var out bytes.Buffer
	out.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n")
	out.WriteString(`<meta charset="utf-8"/>` + "\n")
	out.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1"/>` + "\n")
	fmt.Fprintf(&out, "<title>%s &middot; %s</title>\n",
		html.EscapeString(title), html.EscapeString(site))
	out.WriteString("<style>\n" + policyCSS + "</style>\n")
	if p.CustomCSS {
		fmt.Fprintf(&out, "<link rel=\"stylesheet\" href=\"%s?rev=%d\"/>\n", CSSPath, p.Rev)
	}
	out.WriteString("</head>\n<body>\n")
	fmt.Fprintf(&out, "<header class=\"policy-header\"><a href=\"/\">&larr; %s</a></header>\n",
		html.EscapeString(site))
	out.WriteString("<main class=\"policy\">\n")
	out.Write(body.Bytes())
	out.WriteString("</main>\n</body>\n</html>\n")
	return out.Bytes(), nil
}

// policyCSS styles the standalone page. Deliberately small and
// self-contained: this page is served to readers who are not signed in
// and may have no relationship with the site, so it should load and
// render on its own without the application's bundle.
const policyCSS = `:root{color-scheme:light}
*{box-sizing:border-box}
body{margin:0;background:#fff;color:#1f2937;
  font-family:system-ui,-apple-system,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
  font-size:16px;line-height:1.65}
.policy-header{border-bottom:1px solid #e5e7eb;padding:1rem}
.policy-header a{color:#6b7280;text-decoration:none;font-size:.875rem}
.policy-header a:hover{color:#1f2937}
main.policy{max-width:46rem;margin:0 auto;padding:2.5rem 1.25rem 4rem}
main.policy h1{font-size:1.875rem;line-height:1.25;margin:0 0 1.5rem}
main.policy h2{font-size:1.25rem;margin:2.5rem 0 .75rem;
  padding-top:1.25rem;border-top:1px solid #f3f4f6}
main.policy h3{font-size:1.05rem;margin:1.75rem 0 .5rem}
main.policy p,main.policy li{margin:0 0 1rem}
main.policy ul,main.policy ol{padding-left:1.5rem}
main.policy li>ul,main.policy li>ol{margin-top:.5rem}
main.policy a{color:#1d4ed8}
main.policy code{background:#f3f4f6;border-radius:3px;padding:.1em .35em;font-size:.9em}
main.policy blockquote{margin:0 0 1rem;padding-left:1rem;
  border-left:3px solid #e5e7eb;color:#4b5563}
main.policy table{border-collapse:collapse;margin:0 0 1rem;width:100%}
main.policy th,main.policy td{border:1px solid #e5e7eb;padding:.4rem .6rem;text-align:left}
main.policy em{color:#4b5563}
main.policy hr{border:0;border-top:1px solid #e5e7eb;margin:2rem 0}
@media (max-width:480px){main.policy{padding:1.5rem 1rem 3rem}}
`

// placeholderRe is unreviewed-template detection: the templates mark
// what an operator must supply with a bracketed run of capitals, like
// [CONTACT EMAIL]. See FindPlaceholders.
var placeholderMarkers = []string{"[BRACKETED]", "[CONTACT EMAIL]", "[DATE]",
	"[OPERATING ORGANIZATION]", "[RETENTION PERIOD]", "[SECURITY CONTACT]"}

// FindPlaceholders reports the template placeholders still present in a
// policy document.
//
// Publishing a template with [CONTACT EMAIL] still in it is a mistake
// that nobody notices, because the page renders perfectly and the
// operator who configured it already knows what it was meant to say.
// The server cannot refuse to serve the document -- an operator may
// have reasons, and a missing privacy notice is its own problem -- so
// it says so in the log instead.
func FindPlaceholders(doc []byte) []string {
	var found []string
	for _, m := range placeholderMarkers {
		if bytes.Contains(doc, []byte(m)) {
			found = append(found, m)
		}
	}
	return found
}
