# Documentation

Three guides, by audience. Each directory is one guide; `nav.json` is
generated from the tree and is what a renderer reads.

| Guide | For | Start at |
| --- | --- | --- |
| [User guide](user/) | Submitting and watching jobs through this access point | [user/index.md](user/index.md) |
| [Operator guide](operator/) | Running the server for a site | [operator/index.md](operator/index.md) |
| [Developer and API reference](developer/) | Calling the Go library or the REST API | [developer/library.md](developer/library.md) |

Every page carries front matter — `title`, `audience`, `order` — which
`internal/docsmeta` turns into `nav.json`. `go test ./internal/docsmeta`
checks that the front matter is well-formed, that the ordering is
unambiguous, that every relative link and anchor resolves, and that
`nav.json` matches the tree.

Regenerate the manifest with `make docs-nav`.
