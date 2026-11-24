// Package docsmeta reads the front matter on the pages under docs/ and
// turns the tree into a navigation manifest.
//
// The schema is three required fields -- title, audience, order -- plus
// two optional ones, since and description. It is parsed here by hand
// rather than with a YAML library on purpose: this package sits in the
// published library module, and a documentation manifest is not worth a
// dependency carried by every program that imports it.
//
// The tree is the structure. Directory names are audiences, file names
// are URLs, and nothing is listed anywhere by hand -- which is what
// keeps a page from existing without appearing in the navigation.
package docsmeta

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Audience is the guide a page belongs to. The values are directory
// names under docs/ and appear in URLs, so they are part of the
// contract with whatever renders the tree.
type Audience string

const (
	// User is the guide for people who submit and watch jobs.
	User Audience = "user"
	// Operator is the guide for people who run the server for a site.
	Operator Audience = "operator"
	// Developer is the guide for people who call the Go library or the
	// REST API.
	Developer Audience = "developer"
)

// Audiences is every guide, in reading order: someone who submits jobs,
// then someone who runs the server, then someone who writes against it.
var Audiences = []Audience{User, Operator, Developer}

// Title is what a guide is called in the navigation.
func (a Audience) Title() string {
	switch a {
	case User:
		return "User guide"
	case Operator:
		return "Operator guide"
	case Developer:
		return "Developer and API reference"
	}
	return string(a)
}

// Valid reports whether a is a guide the tree knows about.
func (a Audience) Valid() bool {
	for _, k := range Audiences {
		if k == a {
			return true
		}
	}
	return false
}

// Page is one documentation page: its front matter plus where it lives.
type Page struct {
	// Path is slash-separated and relative to the docs root, e.g.
	// "operator/auth.md".
	Path string `json:"path"`
	// URL is the path a renderer serves the page at, with the .md and
	// any trailing "index" removed.
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Audience    Audience `json:"audience"`
	Order       int      `json:"order"`
	Since       string   `json:"since,omitempty"`
	Description string   `json:"description,omitempty"`
}

// Guide is one audience's pages, in order.
type Guide struct {
	Audience Audience `json:"audience"`
	Title    string   `json:"title"`
	Pages    []Page   `json:"pages"`
}

// Nav is the generated manifest. It is written to docs/nav.json and is
// the only thing a renderer needs to build navigation.
type Nav struct {
	Guides []Guide `json:"guides"`
}

// skipFile is a directory landing page for browsing the tree on GitHub,
// not a page in any guide. It carries no front matter.
const skipFile = "README.md"

// ParseFrontMatter splits a page into its front matter and its body.
// path is used only for error messages and to derive Path and URL.
func ParseFrontMatter(docPath string, content []byte) (Page, []byte, error) {
	p := Page{Path: docPath, URL: pageURL(docPath)}
	text := string(content)
	if !strings.HasPrefix(text, "---\n") {
		return p, nil, fmt.Errorf("%s: no front matter (file must start with ---)", docPath)
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return p, nil, fmt.Errorf("%s: front matter is not terminated by ---", docPath)
	}
	block, body := rest[:end], rest[end+len("\n---\n"):]

	seen := map[string]bool{}
	for i, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return p, nil, fmt.Errorf("%s: front matter line %d is not key: value", docPath, i+2)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if seen[key] {
			return p, nil, fmt.Errorf("%s: front matter repeats %q", docPath, key)
		}
		seen[key] = true
		switch key {
		case "title":
			p.Title = value
		case "audience":
			p.Audience = Audience(value)
		case "order":
			n, err := strconv.Atoi(value)
			if err != nil {
				return p, nil, fmt.Errorf("%s: order %q is not a number", docPath, value)
			}
			p.Order = n
		case "since":
			p.Since = value
		case "description":
			p.Description = value
		default:
			return p, nil, fmt.Errorf("%s: unknown front matter key %q", docPath, key)
		}
	}
	if p.Title == "" {
		return p, nil, fmt.Errorf("%s: front matter has no title", docPath)
	}
	if !p.Audience.Valid() {
		return p, nil, fmt.Errorf("%s: unknown audience %q", docPath, p.Audience)
	}
	if p.Order <= 0 {
		return p, nil, fmt.Errorf("%s: order must be a positive number", docPath)
	}
	if want := Audience(path.Dir(docPath)); want != p.Audience {
		return p, nil, fmt.Errorf("%s: audience %q does not match its directory", docPath, p.Audience)
	}
	return p, []byte(body), nil
}

// pageURL turns "operator/auth.md" into "/docs/operator/auth" and
// "user/index.md" into "/docs/user".
func pageURL(docPath string) string {
	u := "/docs/" + strings.TrimSuffix(docPath, ".md")
	return strings.TrimSuffix(u, "/index")
}

// Load reads every page under root, which is the docs directory.
func Load(root string) ([]Page, error) {
	var pages []Page
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") || d.Name() == skipFile {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(p) //nolint:gosec // G304: p comes from walking the docs tree
		if err != nil {
			return err
		}
		page, _, err := ParseFrontMatter(filepath.ToSlash(rel), content)
		if err != nil {
			return err
		}
		pages = append(pages, page)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })
	return pages, nil
}

// BuildNav groups pages into guides. An audience with no pages is left
// out rather than rendered empty.
func BuildNav(pages []Page) Nav {
	var nav Nav
	for _, a := range Audiences {
		var in []Page
		for _, p := range pages {
			if p.Audience == a {
				in = append(in, p)
			}
		}
		if len(in) == 0 {
			continue
		}
		sort.Slice(in, func(i, j int) bool {
			if in[i].Order != in[j].Order {
				return in[i].Order < in[j].Order
			}
			return in[i].Path < in[j].Path
		})
		nav.Guides = append(nav.Guides, Guide{Audience: a, Title: a.Title(), Pages: in})
	}
	return nav
}

// JSON renders the manifest as it is written to docs/nav.json: indented,
// with a trailing newline, so a regeneration shows up as a clean diff.
func (n Nav) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Link is one relative link found in a page body.
type Link struct {
	Target string // path as written, without the anchor; "" for a same-page link
	Anchor string // without the leading #; "" when there is none
	Line   int    // 1-based, counted from the start of the body
}

var (
	linkRe     = regexp.MustCompile(`\]\(([^)\s]+?)(#[^)\s]*)?\)`)
	headingRe  = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)
	fenceRe    = regexp.MustCompile("^\\s*(```|~~~)")
	schemeRe   = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)
	linkTextRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	// An explicit anchor: <a id="x"></a> or <a name="x"></a>, which a
	// page uses to make a target out of something that is not a
	// heading.
	explicitRe = regexp.MustCompile(`<a\s+(?:id|name)\s*=\s*["']([^"']+)["']`)
	// A MyST heading attribute: ## Two hubs {#two-hubs}. The id wins
	// over the slug, as it does in Sphinx.
	headingIDRe = regexp.MustCompile(`\s*\{#([A-Za-z0-9_-]+)\}\s*$`)
)

// Links returns the relative links in a body, skipping fenced code and
// anything with a scheme or a leading slash.
func Links(body []byte) []Link {
	var out []Link
	inFence := false
	for i, line := range strings.Split(string(body), "\n") {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, m := range linkRe.FindAllStringSubmatch(line, -1) {
			target, anchor := m[1], strings.TrimPrefix(m[2], "#")
			if strings.HasPrefix(target, "#") {
				target, anchor = "", strings.TrimPrefix(target, "#")
			}
			if schemeRe.MatchString(target) || strings.HasPrefix(target, "/") {
				continue
			}
			out = append(out, Link{Target: target, Anchor: anchor, Line: i + 1})
		}
	}
	return out
}

// Anchors returns the fragment identifiers a body defines: headings,
// slugged the way GitHub slugs them -- including the -1, -2 suffixes
// that disambiguate repeats -- plus any explicit <a id> anchor and any
// MyST {#id} heading attribute.
//
// Explicit anchors count because a long page uses them to make a target
// out of a bold lead-in that is not a heading. They are a sign the page
// wants a heading there, but they are not a broken link.
func Anchors(body []byte) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	inFence := false
	for _, line := range strings.Split(string(body), "\n") {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, e := range explicitRe.FindAllStringSubmatch(line, -1) {
			out[e[1]] = true
		}
		m := headingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := m[2]
		if id := headingIDRe.FindStringSubmatch(text); id != nil {
			out[id[1]] = true
			text = headingIDRe.ReplaceAllString(text, "")
		}
		s := Slug(text)
		if s == "" {
			continue
		}
		if n := seen[s]; n > 0 {
			seen[s] = n + 1
			s = fmt.Sprintf("%s-%d", s, n)
		} else {
			seen[s] = 1
		}
		out[s] = true
	}
	return out
}

// Slug turns heading text into a fragment identifier the way GitHub
// does: link syntax and inline code reduced to their text, letters and
// digits kept, spaces turned into hyphens, everything else dropped.
func Slug(heading string) string {
	s := linkTextRe.ReplaceAllString(heading, "$1")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}
