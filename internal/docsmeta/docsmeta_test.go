package docsmeta

import "testing"

func TestParseFrontMatter(t *testing.T) {
	const page = "---\ntitle: SSH gateway\naudience: operator\norder: 70\n---\n\n# SSH gateway\n\nbody\n"
	p, body, err := ParseFrontMatter("operator/ssh-gateway.md", []byte(page))
	if err != nil {
		t.Fatalf("ParseFrontMatter: %v", err)
	}
	if p.Title != "SSH gateway" || p.Audience != Operator || p.Order != 70 {
		t.Errorf("parsed %+v", p)
	}
	if p.URL != "/docs/operator/ssh-gateway" {
		t.Errorf("URL = %q", p.URL)
	}
	if string(body) != "\n# SSH gateway\n\nbody\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontMatterRejects(t *testing.T) {
	cases := map[string]string{
		"no front matter":    "# Title\n",
		"unterminated":       "---\ntitle: x\n",
		"unknown key":        "---\ntitle: x\naudience: operator\norder: 1\nflavour: salty\n---\n",
		"unknown audience":   "---\ntitle: x\naudience: wizard\norder: 1\n---\n",
		"missing title":      "---\naudience: operator\norder: 1\n---\n",
		"order not a number": "---\ntitle: x\naudience: operator\norder: soon\n---\n",
		"order zero":         "---\ntitle: x\naudience: operator\norder: 0\n---\n",
		"repeated key":       "---\ntitle: x\ntitle: y\naudience: operator\norder: 1\n---\n",
	}
	for name, content := range cases {
		if _, _, err := ParseFrontMatter("operator/p.md", []byte(content)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// A page whose audience disagrees with the directory it sits in is the
// mistake that would put it in the wrong guide while still validating.
func TestParseFrontMatterAudienceMatchesDirectory(t *testing.T) {
	const page = "---\ntitle: x\naudience: user\norder: 1\n---\n"
	if _, _, err := ParseFrontMatter("operator/x.md", []byte(page)); err == nil {
		t.Error("expected a mismatch between audience and directory to fail")
	}
}

func TestPageURL(t *testing.T) {
	for in, want := range map[string]string{
		"user/index.md":        "/docs/user",
		"operator/index.md":    "/docs/operator",
		"operator/auth.md":     "/docs/operator/auth",
		"developer/library.md": "/docs/developer/library",
	} {
		if got := pageURL(in); got != want {
			t.Errorf("pageURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"What the username selects":  "what-the-username-selects",
		"Token exchange (RFC 8693)":  "token-exchange-rfc-8693",
		"The `HTTP_API_LOG` setting": "the-http_api_log-setting",
		"A user's own integrations":  "a-users-own-integrations",
		"MCP is not a sandbox":       "mcp-is-not-a-sandbox",
		"See [the docs](a/b.md) now": "see-the-docs-now",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnchorsDisambiguatesRepeats(t *testing.T) {
	body := []byte("## Knobs\n\ntext\n\n## Knobs\n\ntext\n")
	a := Anchors(body)
	if !a["knobs"] || !a["knobs-1"] {
		t.Errorf("anchors = %v", a)
	}
}

// A heading inside a fenced block is not a heading, and a link inside
// one is not a link. Both appear in these docs often enough to matter.
func TestFencedContentIgnored(t *testing.T) {
	body := []byte("```\n# Not a heading\n[x](nowhere.md)\n```\n\n## Real\n")
	if a := Anchors(body); a["not-a-heading"] || !a["real"] {
		t.Errorf("anchors = %v", a)
	}
	if l := Links(body); len(l) != 0 {
		t.Errorf("links = %+v", l)
	}
}

func TestBuildNavOmitsEmptyGuidesAndSortsByOrder(t *testing.T) {
	nav := BuildNav([]Page{
		{Path: "operator/b.md", Audience: Operator, Order: 20},
		{Path: "operator/a.md", Audience: Operator, Order: 10},
	})
	if len(nav.Guides) != 1 || nav.Guides[0].Audience != Operator {
		t.Fatalf("guides = %+v", nav.Guides)
	}
	if nav.Guides[0].Pages[0].Path != "operator/a.md" {
		t.Errorf("pages out of order: %+v", nav.Guides[0].Pages)
	}
}

// A long page makes a link target out of a bold lead-in with an
// explicit anchor. docs/operator/multi-ap.md does exactly this.
func TestAnchorsFindsExplicitAndMySTIDs(t *testing.T) {
	body := []byte(
		"<a id=\"two-hubs\"></a>**Two hubs.** text\n\n" +
			"<a name='old-spelling'></a>\n\n" +
			"## Settling {#settle}\n")
	a := Anchors(body)
	for _, want := range []string{"two-hubs", "old-spelling", "settle"} {
		if !a[want] {
			t.Errorf("anchor %q not found in %v", want, a)
		}
	}
	if a["settling-settle"] {
		t.Error("the MyST id should replace the slug, not join it")
	}
}
