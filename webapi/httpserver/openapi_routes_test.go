package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// routedServerForContract stands up a server with every surface on, so
// the route table is the full one. A surface left off would make the
// contract below pass by omission.
func routedServerForContract(t *testing.T) *Server {
	t.Helper()
	srv, err := NewServer(Config{
		Logger:         newTestLogger(t),
		ScheddName:     "ap.example",
		ScheddAddr:     "127.0.0.1:1",
		EnableMCP:      true,
		EnableIDP:      true,
		OAuth2DBPath:   t.TempDir() + "/oauth2.db",
		OAuth2Issuer:   "http://localhost:8080",
		SigningKeyPath: writeTestSigningKey(t),
		TrustDomain:    "test.domain",
		UIDDomain:      "d",
		SessionTTL:     time.Hour,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.setupRoutes()
	if len(srv.routePatterns) < 60 {
		t.Fatalf("only %d routes recorded; the contract would prove little", len(srv.routePatterns))
	}
	return srv
}

// openAPIPaths is every path the served document declares, as the
// absolute path a client would request: the single server URL followed
// by the path template.
func openAPIPaths(t *testing.T) []string {
	t.Helper()
	var doc struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPISchema, &doc); err != nil {
		t.Fatalf("parsing the OpenAPI document: %v", err)
	}
	if len(doc.Servers) != 1 {
		t.Fatalf("expected exactly one server entry, got %d; this test joins paths to it",
			len(doc.Servers))
	}
	base := strings.TrimSuffix(doc.Servers[0].URL, "/")
	out := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		out = append(out, base+p)
	}
	return out
}

// pathParam matches an OpenAPI path template parameter.
var pathParam = regexp.MustCompile(`\{[^/}]+\}`)

// concrete turns a path template into a URL a mux can be asked about.
// The value is deliberately boring: a handler is never reached, only
// looked up, so it only has to be a legal path segment.
func concrete(template string) string {
	return pathParam.ReplaceAllString(template, "x")
}

// Every endpoint the document promises has to be an endpoint the server
// routes. A documented path that nothing serves is worse than an
// undocumented one: a client writes against it and gets the catch-all.
//
// This asks the server's own mux rather than comparing strings, so it
// is right about Go's pattern precedence -- including the trailing
// slash prefix patterns, which no string comparison gets right.
func TestEveryDocumentedPathIsRouted(t *testing.T) {
	srv := routedServerForContract(t)
	for _, p := range openAPIPaths(t) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, concrete(p), nil)
		_, pattern := srv.mux.Handler(req)
		// The SPA (or welcome page) is registered at "/", so it answers
		// for anything unrouted. Matching it means nothing more
		// specific exists.
		if pattern == "" || pattern == "/" {
			t.Errorf("the OpenAPI document declares %s, which no route serves: "+
				"a client following the document would reach the catch-all. Remove the "+
				"path from openapi.json, or register the route.", p)
		}
	}
}

// And the other direction: an API endpoint the server routes and the
// document does not declare is an endpoint nobody outside this repo can
// find.
//
// Confined to /api/v1/, because that prefix is what the document
// describes. The web UI's own endpoints, the OAuth2 and MCP machinery
// and the branding routes are deliberately outside it.
func TestEveryRoutedAPIPathIsDocumented(t *testing.T) {
	srv := routedServerForContract(t)

	documented := map[string]bool{}
	for _, p := range openAPIPaths(t) {
		documented[concrete(p)] = true
	}
	// Which routes turned out to be covered, and which not, so the
	// inventory below can be checked in both directions.
	documentedRoutes := map[string]bool{}
	undocumented := map[string]bool{}

	for _, raw := range srv.routePatterns {
		// Go 1.22 patterns may carry a method and a host.
		pattern := raw
		if i := strings.LastIndex(pattern, " "); i >= 0 {
			pattern = pattern[i+1:]
		}
		if !strings.HasPrefix(pattern, "/api/v1/") {
			continue
		}
		if coveredByDocument(pattern, documented) {
			documentedRoutes[pattern] = true
			continue
		}
		undocumented[pattern] = true
	}

	known := map[string]bool{}
	for _, p := range undocumentedAPIRoutes {
		known[p] = true
	}
	for p := range undocumented {
		if !known[p] {
			t.Errorf("%s is routed under /api/v1/ and the OpenAPI document declares nothing "+
				"that reaches it. Add the path to openapi.json, or -- if it is not meant to "+
				"be part of the published API -- add it to undocumentedAPIRoutes.", p)
		}
	}
	for p := range known {
		if !undocumented[p] && !documentedRoutes[p] {
			t.Errorf("%s is listed in undocumentedAPIRoutes but the server no longer "+
				"registers it; remove the entry.", p)
		}
		if documentedRoutes[p] {
			t.Errorf("%s is listed in undocumentedAPIRoutes and the OpenAPI document now "+
				"declares it; remove the entry.", p)
		}
	}
}

// coveredByDocument reports whether some documented path reaches this
// pattern. An exact pattern has to be documented exactly; a pattern
// ending in a slash is a subtree, and any documented path inside it
// counts, since that is what the subtree exists to serve.
func coveredByDocument(pattern string, documented map[string]bool) bool {
	if strings.HasSuffix(pattern, "/") {
		for p := range documented {
			if strings.HasPrefix(p, pattern) {
				return true
			}
		}
		// A subtree may also be documented as the path without its
		// trailing slash.
		return documented[strings.TrimSuffix(pattern, "/")]
	}
	if documented[concrete(pattern)] {
		return true
	}
	// An exact route may sit under a documented subtree's template,
	// where the document names the collection and the route names one
	// member.
	return false
}

// The /api/v1/ routes the server registers and the OpenAPI document
// does not declare, split by whether anything ELSE documents them.
//
// This was one flat list called undocumentedAPIRoutes, whose comment
// said most of these "serve the web console rather than a published
// API". Cross-referencing it against docs/ says otherwise: 29 of the
// 41 are described in the documentation tree, several of them at
// length -- /api/v1/ping has a full paragraph in developer/api.md.
// They are not undocumented. They are missing from the SPEC while the
// prose tells people to use them, which is a different problem with a
// different fix, and the single name hid that.
//
// Neither list is a set of approvals. Both are frozen, so a route
// added under /api/v1/ without a path in the document fails
// TestEveryRoutedAPIPathIsDocumented, and one that gets documented
// without being struck from here fails too.

// routesDescribedInProseOnly: documented in docs/, absent from
// openapi.json. A client generated from the document cannot call an
// endpoint the documentation tells its reader to use -- so each of
// these is either a path to add to openapi.json or a paragraph to
// stop promising.
var routesDescribedInProseOnly = []string{
	"/api/v1/admin/api-keys",
	"/api/v1/admin/api-keys/",
	"/api/v1/admin/condor-config",
	"/api/v1/admin/connectors",
	"/api/v1/admin/connectors/",
	"/api/v1/admin/logs",
	"/api/v1/admin/oauth2/revoke",
	"/api/v1/admin/oauth2/tokens",
	"/api/v1/admin/reservations",
	"/api/v1/admin/reservations/",
	"/api/v1/admin/reservations/windows",
	"/api/v1/admin/usage",
	"/api/v1/chat",
	"/api/v1/chat/info",
	"/api/v1/chat/scope",
	"/api/v1/collector/pool-summary",
	"/api/v1/collector/watch",
	"/api/v1/dashboard",
	"/api/v1/interactive/terminal",
	"/api/v1/issues",
	"/api/v1/jobs/watch",
	"/api/v1/ping",
	"/api/v1/schedd/ping",
	"/api/v1/share/input",
	"/api/v1/share/output",
	"/api/v1/share/watch",
	"/api/v1/templates",
	"/api/v1/templates/",
	"/api/v1/webui/branding",
}

// routesWithNoDocumentation: nothing in docs/ names these. Most look
// like the web console's own surface -- the activity stream, the
// calendar, the file-chooser list, logout -- plus four admin
// endpoints that the admin pages call and no page explains.
//
// This is the list where "is the document meant to describe them?" is
// still an open question, and it is twelve routes rather than
// forty-one.
var routesWithNoDocumentation = []string{
	"/api/v1/admin/oauth2/tokens/revoke",
	"/api/v1/admin/oauth2/tokens/scopes",
	"/api/v1/admin/reservations/health",
	"/api/v1/admin/site-apps",
	"/api/v1/admin/site-apps/",
	"/api/v1/admin/superuser",
	"/api/v1/auth/logout",
	"/api/v1/calendar",
	"/api/v1/dashboard/activity",
	"/api/v1/dashboard/activity/stream",
	"/api/v1/pickers",
	"/api/v1/watches/",
}

// undocumentedAPIRoutes is the two lists together, which is what the
// contract check above compares against.
var undocumentedAPIRoutes = append(append([]string{},
	routesDescribedInProseOnly...), routesWithNoDocumentation...)

// The split above is only worth having if it stays true. A route that
// somebody documents has to move lists, and one whose paragraph is
// deleted has to move back -- otherwise this becomes the flat list it
// replaced, with two names.
//
// It matches on the route followed by a non-path character, not on a
// substring. /api/v1/dashboard is a prefix of /api/v1/dashboard/activity,
// and a plain substring search reports the activity stream as documented
// because a page mentions the dashboard. That misclassified one route on
// the first pass; it is the same prefix trap as HTTP_API_LISTEN inside
// HTTP_API_LISTEN_ADDR.
func TestRouteInventoryClassificationIsAccurate(t *testing.T) {
	tree := docsTreeText(t)
	if len(tree) == 0 {
		t.Fatal("read no documentation; this check would pass vacuously")
	}

	for _, route := range routesDescribedInProseOnly {
		if where := pagesNaming(tree, route); len(where) == 0 {
			t.Errorf("%s is in routesDescribedInProseOnly and no page under docs/ names it. "+
				"If its documentation was removed, move it to routesWithNoDocumentation.", route)
		}
	}
	for _, route := range routesWithNoDocumentation {
		if where := pagesNaming(tree, route); len(where) > 0 {
			sort.Strings(where)
			t.Errorf("%s is in routesWithNoDocumentation but %s names it. Move it to "+
				"routesDescribedInProseOnly -- or, better, add it to openapi.json and "+
				"strike it from both.", route, strings.Join(where, ", "))
		}
	}
}

// docsTreeText reads every Markdown page in the documentation tree,
// keyed by a path for error messages.
func docsTreeText(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join("..", "..", "docs")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		b, err := os.ReadFile(path) //nolint:gosec // G304: path comes from walking docs/
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("reading the documentation tree: %v", err)
	}
	return out
}

// pagesNaming returns the pages that name this route as a whole route
// rather than as the prefix of a longer one.
func pagesNaming(tree map[string]string, route string) []string {
	needle := strings.TrimSuffix(route, "/")
	var out []string
	for page, text := range tree {
		for i := 0; ; {
			j := strings.Index(text[i:], needle)
			if j < 0 {
				break
			}
			end := i + j + len(needle)
			i = end
			if end < len(text) {
				c := text[end]
				// Another path segment, or a longer name: not this route.
				if c == '/' || c == '-' || c == '_' ||
					(c >= '0' && c <= '9') ||
					(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					continue
				}
			}
			out = append(out, page)
			break
		}
	}
	return out
}
