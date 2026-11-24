// Command docsnav regenerates docs/nav.json from the documentation
// tree. Run it with `make docs-nav`; `go test ./internal/docsmeta`
// fails when the committed manifest no longer matches the tree.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/htcondor/golang-htcondor/internal/docsmeta"
)

func main() {
	root := flag.String("docs", "docs", "documentation tree to read")
	out := flag.String("o", "", "manifest to write (default <docs>/nav.json)")
	flag.Parse()

	dest := *out
	if dest == "" {
		dest = filepath.Join(*root, "nav.json")
	}

	pages, err := docsmeta.Load(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docsnav:", err)
		os.Exit(1)
	}
	b, err := docsmeta.BuildNav(pages).JSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, "docsnav:", err)
		os.Exit(1)
	}
	// 0644: the manifest is committed, read by the docs build and
	// shipped in the package, so it is meant to be world-readable.
	if err := os.WriteFile(dest, b, 0o644); err != nil { //nolint:gosec // G306: a committed, published manifest
		fmt.Fprintln(os.Stderr, "docsnav:", err)
		os.Exit(1)
	}
	fmt.Printf("docsnav: %d pages -> %s\n", len(pages), dest)
}
