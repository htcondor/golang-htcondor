package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// The documentation route's shape in the export, and what this handler
// has to do with it.
//
// The app prerenders docs/<audience>/<page> from the repository's own
// docs/ tree, which gives the export a layout no other route produces:
// docs/user.html and a docs/user/ directory beside it, because "user"
// is both a page and a parent. Rule 2 of ServeHTTP (try name+".html")
// is what serves both, and a change to the resolution order that
// preferred the directory would answer /docs/user with the SPA
// fallback instead -- a 200 showing the dashboard, which is the failure
// this pins.
func TestSPAHandlerServesDocumentationPages(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":          {Data: []byte("<html><body>dashboard</body></html>")},
		"docs/user.html":      {Data: []byte("<html><body>user guide</body></html>")},
		"docs/user/jobs.html": {Data: []byte("<html><body>submitting a job</body></html>")},
		"docs/operator.html":  {Data: []byte("<html><body>operator guide</body></html>")},
		"docs/user/jobs.txt":  {Data: []byte("flight payload")},
	}
	h := NewSPAHandlerFS(fsys)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
		return rec
	}

	for path, want := range map[string]string{
		"/docs/user":      "user guide",
		"/docs/user/jobs": "submitting a job",
		"/docs/operator":  "operator guide",
		// Next prefetches the route payload beside the page.
		"/docs/user/jobs.txt": "flight payload",
	} {
		rec := get(path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: served %q, wanted %q", path, rec.Body.String(), want)
		}
	}

	// A slug the export did not write falls through to the SPA, as every
	// unknown path does. Recorded rather than wished away: the app's
	// router has no documentation page for it either, so what a reader
	// sees is the dashboard, and the test that stops that happening to a
	// real page is on the other side, in the frontend's
	// generateStaticParams guard.
	if body := get("/docs/user/not-a-page").Body.String(); !strings.Contains(body, "dashboard") {
		t.Errorf("unknown documentation slug: served %q", body)
	}
}
