package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/webapi/httpserver/webui"
)

// writePolicy puts a markdown document on disk and returns its path.
func writePolicy(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// policyBranding is what the footer is told about the policy links.
func policyBranding(t *testing.T, s *Server) webui.Branding {
	t.Helper()
	rec := brandingGet(t, s, brandingPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", brandingPath, rec.Code)
	}
	var b webui.Branding
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return b
}

// The default posture. A policy page is a legal statement about a
// particular deployment, so an operator who has written none must not
// get one invented for them -- and the footer must not link to a page
// that does not exist.
func TestNoPolicyConfiguredMeansNoPageAndNoLink(t *testing.T) {
	s := brandingServer(t, nil)
	for _, path := range []string{webui.PrivacyPath, webui.TermsPath} {
		if rec := brandingGet(t, s, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d; an unconfigured policy page must 404", path, rec.Code)
		}
	}
	b := policyBranding(t, s)
	if b.PrivacyURL != "" || b.TermsURL != "" {
		t.Errorf("privacy_url = %q, terms_url = %q; both must be empty so the footer says nothing",
			b.PrivacyURL, b.TermsURL)
	}
}

func TestAConfiguredPolicyIsRendered(t *testing.T) {
	path := writePolicy(t, "# Privacy Notice\n\nWe keep *job records* for a year.\n")
	s := brandingServer(t, func(c *Config) { c.WebUIPrivacyFile = path })

	rec := brandingGet(t, s, webui.PrivacyPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", webui.PrivacyPath, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q; the page is read in a browser", ct)
	}
	body := rec.Body.String()
	// Rendered, not served as markdown: the asterisks are the tell.
	for _, want := range []string{"<h1>Privacy Notice</h1>", "<em>job records</em>", "<!doctype html>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "*job records*") {
		t.Error("the markdown was served unrendered")
	}
}

// The footer link is how anybody finds the page.
func TestAConfiguredPolicyIsLinkedFromTheFooter(t *testing.T) {
	s := brandingServer(t, func(c *Config) {
		c.WebUIPrivacyFile = writePolicy(t, "# Privacy\n")
		c.WebUITermsFile = writePolicy(t, "# Terms\n")
	})
	b := policyBranding(t, s)
	if b.PrivacyURL != webui.PrivacyPath {
		t.Errorf("privacy_url = %q, want %q", b.PrivacyURL, webui.PrivacyPath)
	}
	if b.TermsURL != webui.TermsPath {
		t.Errorf("terms_url = %q, want %q", b.TermsURL, webui.TermsPath)
	}
	// Otherwise the server serves the pages the export ships, and the
	// links never reach the footer.
	if b.IsDefault() {
		t.Error("a server with policy pages reports default branding, so nothing is injected")
	}
}

// The common case for a privacy notice: the institution already has
// one, and a site should point at it rather than keep a second copy.
func TestAPolicyURLRedirectsAndIsLinkedDirectly(t *testing.T) {
	const notice = "https://www.wisc.edu/privacy-notice/"
	s := brandingServer(t, func(c *Config) { c.WebUIPrivacyURL = notice })

	rec := brandingGet(t, s, webui.PrivacyPath)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET %s = %d, want a redirect", webui.PrivacyPath, rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != notice {
		t.Errorf("Location = %q, want %q", loc, notice)
	}
	// The footer shows where the reader is really going, rather than
	// bouncing them through this server.
	if got := policyBranding(t, s).PrivacyURL; got != notice {
		t.Errorf("privacy_url = %q, want the external notice %q", got, notice)
	}
}

// Both set is a contradiction, and the file is the more specific
// statement of the two.
func TestAPolicyFileWinsOverAPolicyURL(t *testing.T) {
	s := brandingServer(t, func(c *Config) {
		c.WebUIPrivacyFile = writePolicy(t, "# Ours\n")
		c.WebUIPrivacyURL = "https://example.edu/theirs"
	})
	rec := brandingGet(t, s, webui.PrivacyPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want the file to be served", webui.PrivacyPath, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<h1>Ours</h1>") {
		t.Error("the configured file was not the document served")
	}
}

// A mistyped path must not fall through to the URL. The two settings
// say different things about where a site's policy lives, and
// publishing the other one because the first had a typo is how a site
// ends up pointing at the wrong document.
func TestAnUnreadablePolicyFileServesNothingRatherThanTheURL(t *testing.T) {
	s := brandingServer(t, func(c *Config) {
		c.WebUIPrivacyFile = filepath.Join(t.TempDir(), "typo.md")
		c.WebUIPrivacyURL = "https://example.edu/theirs"
	})
	if rec := brandingGet(t, s, webui.PrivacyPath); rec.Code != http.StatusNotFound {
		t.Errorf("GET %s = %d (Location %q); want 404",
			webui.PrivacyPath, rec.Code, rec.Header().Get("Location"))
	}
	if got := policyBranding(t, s).PrivacyURL; got != "" {
		t.Errorf("privacy_url = %q; the footer must not link to a page that 404s", got)
	}
}

// The shipped templates, opted into by name.
func TestTheShippedTemplatesCanBeServed(t *testing.T) {
	s := brandingServer(t, func(c *Config) {
		c.WebUIPrivacyFile = webui.DefaultSource
		c.WebUITermsFile = webui.DefaultSource
	})
	for path, want := range map[string]string{
		webui.PrivacyPath: "Privacy Notice",
		webui.TermsPath:   "Terms of Use",
	} {
		rec := brandingGet(t, s, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s does not mention %q", path, want)
		}
	}
}

// Raw HTML is dropped rather than rendered. A policy document is the
// kind of text that gets pasted in from elsewhere, and script arriving
// that way would run same-origin without anybody choosing it.
func TestRawHTMLInAPolicyIsNotRendered(t *testing.T) {
	s := brandingServer(t, func(c *Config) {
		c.WebUIPrivacyFile = writePolicy(t,
			"# Notice\n\n<script>window.stolen=document.cookie</script>\n\nText.\n")
	})
	rec := brandingGet(t, s, webui.PrivacyPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", webui.PrivacyPath, rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<script>window.stolen") {
		t.Errorf("the document's raw <script> reached the page:\n%s", rec.Body.String())
	}
}

// Nobody has an account when they read these. A login redirect, or a
// 403, reads to a reader -- or to the reviewer of an application that
// integrates with this server -- as the policy being missing.
func TestPolicyPagesDoNotRequireASession(t *testing.T) {
	s := brandingServer(t, func(c *Config) {
		c.WebUIPrivacyFile = writePolicy(t, "# Privacy\n")
		c.WebUITermsFile = writePolicy(t, "# Terms\n")
	})
	for _, path := range []string{webui.PrivacyPath, webui.TermsPath} {
		// brandingGet carries no cookie and no token.
		if rec := brandingGet(t, s, path); rec.Code != http.StatusOK {
			t.Errorf("anonymous GET %s = %d; the page must be readable without signing in",
				path, rec.Code)
		}
	}
}

// A placement-only session reaches almost nothing, but the placement
// pages carry the same footer, so these two links have to work. The
// route walk in placement_session_gate_test.go skips the allowlist, so
// without this the widening is only subtracted, never checked.
func TestAPlacementOnlySessionMayReadThePolicies(t *testing.T) {
	for _, path := range []string{webui.PrivacyPath, webui.TermsPath} {
		if !placementOnlyAllowed(path, path) {
			t.Errorf("a placement-only session is refused %s", path)
		}
	}
}

func TestPolicyPagesRefuseWrites(t *testing.T) {
	s := brandingServer(t, func(c *Config) {
		c.WebUIPrivacyFile = writePolicy(t, "# Privacy\n")
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, webui.PrivacyPath, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST %s = %d, want 405", webui.PrivacyPath, rec.Code)
	}
}

// The placeholders exist so an operator cannot publish a template by
// accident without the server noticing; the templates must therefore
// actually carry the markers the check looks for.
func TestTheShippedTemplatesAreDetectablyUnfinished(t *testing.T) {
	for name, doc := range map[string][]byte{
		"privacy": webui.DefaultPrivacyMarkdown(),
		"terms":   webui.DefaultTermsMarkdown(),
	} {
		if ph := webui.FindPlaceholders(doc); len(ph) == 0 {
			t.Errorf("the %s template carries no placeholder markers, so publishing it unedited is silent", name)
		}
	}
}

func TestAFinishedPolicyHasNoPlaceholders(t *testing.T) {
	if ph := webui.FindPlaceholders([]byte("# Privacy\n\nWrite to privacy@example.edu.\n")); len(ph) != 0 {
		t.Errorf("a finished document reports placeholders %v", ph)
	}
}
