package docsmeta

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests check the real documentation tree, not a fixture. They
// are what makes the tree the single source of navigation: a page
// cannot be added without front matter, a link cannot rot unnoticed,
// and nav.json cannot fall behind what is on disk.

const docsRoot = "../../docs"

func loadTree(t *testing.T) []Page {
	t.Helper()
	pages, err := Load(docsRoot)
	if err != nil {
		t.Fatalf("loading %s: %v", docsRoot, err)
	}
	if len(pages) == 0 {
		t.Fatalf("no pages found under %s", docsRoot)
	}
	return pages
}

// bodyOf returns a markdown file's content with any front matter
// removed, so headings and links are counted from the prose alone.
func bodyOf(path string) ([]byte, error) {
	content, err := os.ReadFile(path) //nolint:gosec // G304: path comes from the docs tree
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(content, []byte("---\n")) {
		return content, nil
	}
	rest := content[len("---\n"):]
	i := bytes.Index(rest, []byte("\n---\n"))
	if i < 0 {
		return content, nil
	}
	return rest[i+len("\n---\n"):], nil
}

func TestTreeOrderIsUnambiguous(t *testing.T) {
	seen := map[Audience]map[int]string{}
	for _, p := range loadTree(t) {
		if seen[p.Audience] == nil {
			seen[p.Audience] = map[int]string{}
		}
		if other, dup := seen[p.Audience][p.Order]; dup {
			t.Errorf("%s and %s both claim order %d in the %s guide",
				p.Path, other, p.Order, p.Audience)
		}
		seen[p.Audience][p.Order] = p.Path
	}
}

// The navigation shows the front matter title; the page shows its H1.
// When they drift the reader is told two different things, so they have
// to agree.
func TestTreeTitleMatchesHeading(t *testing.T) {
	for _, p := range loadTree(t) {
		body, err := bodyOf(filepath.Join(docsRoot, p.Path))
		if err != nil {
			t.Fatalf("%s: %v", p.Path, err)
		}
		var h1 string
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "# ") {
				h1 = strings.TrimSpace(line[2:])
				break
			}
		}
		if h1 == "" {
			t.Errorf("%s: no H1", p.Path)
			continue
		}
		if h1 != p.Title {
			t.Errorf("%s: front matter title %q but H1 %q", p.Path, p.Title, h1)
		}
	}
}

func TestTreeLinksResolve(t *testing.T) {
	files := []string{"README.md"}
	for _, p := range loadTree(t) {
		files = append(files, p.Path)
	}

	anchorCache := map[string]map[string]bool{}
	anchorsFor := func(path string) map[string]bool {
		if a, ok := anchorCache[path]; ok {
			return a
		}
		body, err := bodyOf(path)
		if err != nil {
			anchorCache[path] = nil
			return nil
		}
		a := Anchors(body)
		anchorCache[path] = a
		return a
	}

	for _, rel := range files {
		full := filepath.Join(docsRoot, rel)
		body, err := bodyOf(full)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, link := range Links(body) {
			where := fmt.Sprintf("%s:%d", rel, link.Line)

			if link.Target == "" { // same-page anchor
				if link.Anchor != "" && !anchorsFor(full)[link.Anchor] {
					t.Errorf("%s: no heading for #%s on this page", where, link.Anchor)
				}
				continue
			}

			target := filepath.Join(filepath.Dir(full), link.Target)
			info, err := os.Stat(target)
			if err != nil {
				t.Errorf("%s: link to %s does not exist", where, link.Target)
				continue
			}
			if info.IsDir() || link.Anchor == "" || !strings.HasSuffix(target, ".md") {
				continue
			}
			if a := anchorsFor(target); a != nil && !a[link.Anchor] {
				t.Errorf("%s: %s has no heading for #%s", where, link.Target, link.Anchor)
			}
		}
	}
}

// nav.json is generated. Regenerate it with `make docs-nav`.
func TestTreeNavManifestIsCurrent(t *testing.T) {
	want, err := BuildNav(loadTree(t)).JSON()
	if err != nil {
		t.Fatalf("building nav: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(docsRoot, "nav.json"))
	if err != nil {
		t.Fatalf("reading nav.json: %v (run `make docs-nav`)", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("docs/nav.json is out of date; run `make docs-nav`")
	}
}
