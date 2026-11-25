package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
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
		req := httptest.NewRequest(http.MethodGet, concrete(p), nil)
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

// undocumentedAPIRoutes is an inventory, not a set of approvals.
//
// These are the /api/v1/ routes the server registers and the OpenAPI
// document does not declare: about a third of the surface under that
// prefix. Most of them serve the web console rather than a published
// API -- the admin pages, the dashboard, the chat assistant, the submit
// templates, the file choosers -- and several are streaming endpoints
// that the document has no shape for. Whether the document is meant to
// describe them at all is a decision nobody has recorded, so this list
// records the question instead of answering it.
//
// What it buys in the meantime is the thing the contract is for: the
// set is frozen. A route added under /api/v1/ without a path in the
// document fails TestEveryRoutedAPIPathIsDocumented, and so does one
// that gets documented without being struck from here.
var undocumentedAPIRoutes = []string{
	"/api/v1/admin/api-keys",
	"/api/v1/admin/api-keys/",
	"/api/v1/admin/condor-config",
	"/api/v1/admin/connectors",
	"/api/v1/admin/connectors/",
	"/api/v1/admin/logs",
	"/api/v1/admin/oauth2/revoke",
	"/api/v1/admin/oauth2/tokens",
	"/api/v1/admin/oauth2/tokens/revoke",
	"/api/v1/admin/oauth2/tokens/scopes",
	"/api/v1/admin/reservations",
	"/api/v1/admin/reservations/",
	"/api/v1/admin/reservations/health",
	"/api/v1/admin/reservations/windows",
	"/api/v1/admin/site-apps",
	"/api/v1/admin/site-apps/",
	"/api/v1/admin/superuser",
	"/api/v1/admin/usage",
	"/api/v1/auth/logout",
	"/api/v1/calendar",
	"/api/v1/chat",
	"/api/v1/chat/info",
	"/api/v1/chat/scope",
	"/api/v1/collector/pool-summary",
	"/api/v1/collector/watch",
	"/api/v1/dashboard",
	"/api/v1/dashboard/activity",
	"/api/v1/dashboard/activity/stream",
	"/api/v1/interactive/terminal",
	"/api/v1/issues",
	"/api/v1/jobs/watch",
	"/api/v1/pickers",
	"/api/v1/ping",
	"/api/v1/schedd/ping",
	"/api/v1/share/input",
	"/api/v1/share/output",
	"/api/v1/share/watch",
	"/api/v1/templates",
	"/api/v1/templates/",
	"/api/v1/watches/",
	"/api/v1/webui/branding",
}
