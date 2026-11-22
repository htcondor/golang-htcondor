package httpserver

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/httpserver/webui"
)

// Serving the site's policy pages. What they are for, why they are off
// by default, and what is trusted about the markdown is in
// webui/policy.go; this file is the configuration and the two routes.
//
// Four settings, two per page:
//
//	HTTP_API_WEBUI_PRIVACY_FILE = /etc/htcondor-api/privacy.md
//	HTTP_API_WEBUI_PRIVACY_URL  = https://www.wisc.edu/privacy-notice/
//	HTTP_API_WEBUI_TERMS_FILE   = default
//	HTTP_API_WEBUI_TERMS_URL    = https://example.edu/terms
//
// The file wins when both are set, and the literal "default" selects
// the template this software ships. The URL form exists because the
// common case for a privacy notice is that the institution already has
// one: a site should be able to point at it rather than maintain a
// second copy that drifts from the first.
//
// Either way the local path answers, so there is one URL to give out:
// with a file it renders, with a URL it redirects. The footer link
// points wherever the reader is really going -- see Branding.PrivacyURL.
//
// Like the rest of the branding, all of it is reloadable with
// condor_reconfig, and the document is re-read on each request so an
// operator correcting a sentence sees it on a refresh.

// policyMaxBytes caps a policy document. It is read into memory on each
// request; these are tens of kilobytes of prose, and the cap is what
// stops a mistyped path from serving something that is not.
const policyMaxBytes = 1 << 20

// policyDoc is one resolved policy page.
type policyDoc struct {
	// file is the markdown to render, empty when there is none.
	file string
	// embedded is the shipped template, when the operator selected it
	// with webui.DefaultSource. Nil otherwise.
	embedded []byte
	// redirect is where this path sends readers, when the page is
	// configured as a URL rather than a document.
	redirect string
	// link is what the footer links to: the redirect when there is
	// one, otherwise this page's own path. Empty when the page is not
	// configured, which is what keeps the link out of the footer.
	link string
	// title heads the rendered page.
	title string
}

// configured reports whether this server answers the page's path.
func (d policyDoc) configured() bool {
	return d.file != "" || d.embedded != nil || d.redirect != ""
}

// resolvePolicy turns one page's two settings into a policyDoc,
// warning about anything it had to ignore.
//
// Precedence is file, then URL. An unreadable file does NOT fall
// through to the URL: the two say different things about where a site's
// policy lives, and quietly publishing the other one because the first
// had a typo is how a site ends up pointing at the wrong document. It
// warns and serves neither.
func (s *brandingState) resolvePolicy(prefix, file, rawURL, path string, def func() []byte) policyDoc {
	doc := policyDoc{title: policyTitle(path)}
	file = strings.TrimSpace(file)
	rawURL = strings.TrimSpace(rawURL)

	switch {
	case strings.EqualFold(file, webui.DefaultSource):
		doc.embedded = def()
		doc.link = path
	case file != "":
		if fi, err := os.Stat(file); err != nil || fi.IsDir() {
			s.warn(prefix+"_FILE is not a readable file; the page will not be served",
				"path", file, "error", err)
			return policyDoc{title: doc.title}
		}
		doc.file = file
		doc.link = path
	case rawURL != "":
		if !webui.PolicyURLOK(rawURL) {
			s.warn(prefix+"_URL is not a site-relative or http(s) URL; the page will not be served",
				"url", rawURL)
			return policyDoc{title: doc.title}
		}
		doc.redirect = rawURL
		doc.link = rawURL
	default:
		return doc
	}

	if file != "" && rawURL != "" {
		s.warn(prefix+"_FILE and "+prefix+"_URL are both set; serving the file and ignoring the URL",
			"path", file, "url", rawURL)
	}
	// An operator who published a template still saying [CONTACT EMAIL]
	// will not notice on their own: the page renders perfectly, and
	// they already know what it was meant to say.
	if body, err := s.policyMarkdown(doc); err == nil {
		if ph := webui.FindPlaceholders(body); len(ph) > 0 {
			s.warn(prefix+" still contains template placeholders that an operator is expected to replace",
				"placeholders", strings.Join(ph, " "))
		}
	}
	return doc
}

// policyTitle names a page for its tab and its heading.
func policyTitle(path string) string {
	if path == webui.TermsPath {
		return "Terms of Use"
	}
	return "Privacy Notice"
}

// policyMarkdown reads the document behind a policyDoc.
func (s *brandingState) policyMarkdown(doc policyDoc) ([]byte, error) {
	if doc.embedded != nil {
		return doc.embedded, nil
	}
	return readCapped(doc.file, policyMaxBytes)
}

// handlePrivacyPage serves webui.PrivacyPath.
func (s *Handler) handlePrivacyPage(w http.ResponseWriter, r *http.Request) {
	s.servePolicy(w, r, s.branding.snapshot().privacy)
}

// handleTermsPage serves webui.TermsPath.
func (s *Handler) handleTermsPage(w http.ResponseWriter, r *http.Request) {
	s.servePolicy(w, r, s.branding.snapshot().terms)
}

// servePolicy renders one policy page, or redirects to where the site
// keeps it.
//
// Unauthenticated, deliberately. These pages exist to be read by people
// deciding whether to use the service and by the reviewers of
// applications that integrate with it -- neither of whom has an account
// here, and both of whom would read a login page as the policy being
// missing.
func (s *Handler) servePolicy(w http.ResponseWriter, r *http.Request, doc policyDoc) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if !doc.configured() {
		http.NotFound(w, r)
		return
	}
	if doc.redirect != "" {
		// Found, not a permanent redirect: where a site keeps its
		// policy is a configuration setting, and a 301 would be
		// cached past the operator changing it.
		http.Redirect(w, r, doc.redirect, http.StatusFound)
		return
	}

	body, err := s.branding.policyMarkdown(doc)
	if err != nil {
		s.logger.Warn(logging.DestinationHTTP, "Could not read the site policy document",
			"page", doc.title, "path", doc.file, "error", err)
		http.NotFound(w, r)
		return
	}
	snap := s.branding.snapshot()
	page, err := webui.RenderPolicyPage(webui.PolicyPage{
		Title:     doc.title,
		SiteName:  snap.branding.SiteName,
		Markdown:  body,
		CustomCSS: snap.cssFile != "",
		Rev:       snap.branding.Rev,
	})
	if err != nil {
		s.logger.Warn(logging.DestinationHTTP, "Could not render the site policy document",
			"page", doc.title, "path", doc.file, "error", err)
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Re-read and re-rendered per request, for the same reason as the
	// stylesheet: an operator correcting a sentence should see it on a
	// refresh, not after a cache expiry they cannot see.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "policy.html", time.Time{}, bytes.NewReader(page))
}
