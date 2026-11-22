package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/webapi/idempotency"
)

// TestIdempotencyHeaderPresentButEmptyIsRefused is the case that decides
// whether this feature can be relied on.
//
// A client that sets the header from a variable that turned out empty
// asked for the guarantee and would not get it. Treating that as "no
// header" submits the job unguarded and reports success, so the client
// learns nothing; refusing it is a 400 the client can see and fix.
func TestIdempotencyHeaderHandling(t *testing.T) {
	cases := []struct {
		name    string
		headers []string
		setFlag bool // was the header set at all
		wantKey string
		wantErr bool
	}{
		{name: "absent", headers: nil},
		{name: "ordinary", headers: []string{"batch-7"}, setFlag: true, wantKey: "batch-7"},
		{name: "present but empty", headers: []string{""}, setFlag: true, wantErr: true},
		{name: "repeated with one value", headers: []string{"batch-7", "batch-7"}, setFlag: true, wantKey: "batch-7"},
		{name: "repeated with two values", headers: []string{"batch-7", "batch-8"}, setFlag: true, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/jobs", nil)
			for _, v := range c.headers {
				r.Header.Add(IdempotencyKeyHeader, v)
			}
			ctx, err := withIdempotencyKey(context.Background(), r)
			if c.wantErr {
				var bad *idempotency.KeyError
				if !errors.As(err, &bad) {
					t.Fatalf("err = %v, want a KeyError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("withIdempotencyKey: %v", err)
			}
			got, ok := idempotency.KeyFrom(ctx)
			if c.wantKey == "" {
				if ok {
					t.Errorf("a key %q was put on the context for a request with none", got)
				}
				return
			}
			if !ok || got != c.wantKey {
				t.Errorf("key = %q (present %v), want %q", got, ok, c.wantKey)
			}
		})
	}
}

// TestIdempotencyFailureStatuses pins each refusal to its status.
//
// The three mean three different things for a client to do, and a client
// that cannot tell them apart cannot use the feature: 409 means the work
// is already queued (the retry succeeded in the sense that matters), 400
// means the request is wrong and retrying will not help, and 503 means
// the check could not be made and the same request may well work later.
// They are also easy to collapse by accident -- every one of them is an
// error value reaching one writer.
func TestIdempotencyFailureStatuses(t *testing.T) {
	h := &Handler{logger: testLogger(t)}

	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"a duplicate", &idempotency.DuplicateError{Key: "batch-7", JobID: "42.0", Where: "is still in the queue"}, http.StatusConflict},
		{"an unverifiable key", &idempotency.UnverifiableError{Key: "batch-7", Err: errors.New("mirror down")}, http.StatusServiceUnavailable},
		{"a bad key", &idempotency.KeyError{Reason: "the idempotency key is empty"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if !h.writeIdempotencyFailure(w, c.err) {
				t.Fatal("writeIdempotencyFailure did not recognize the error")
			}
			if w.Code != c.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, c.wantStatus)
			}
		})
	}

	// An unrelated error must pass through untouched, or every other
	// submit failure would be answered as an idempotency problem.
	w := httptest.NewRecorder()
	if h.writeIdempotencyFailure(w, errors.New("the schedd refused the transaction")) {
		t.Error("an unrelated submit error was answered as an idempotency refusal")
	}
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("an unrelated error was written anyway: status %d, body %q", w.Code, w.Body.String())
	}
}

// TestDuplicateResponseNamesTheHoldingJob: a 409 whose body does not say
// which job holds the key leaves the client with nothing to do -- it
// cannot look the work up and cannot safely resubmit.
func TestDuplicateResponseNamesTheHoldingJob(t *testing.T) {
	h := &Handler{logger: testLogger(t)}
	w := httptest.NewRecorder()
	h.writeIdempotencyFailure(w, &idempotency.DuplicateError{
		Key: "batch-7", JobID: "42.0", Where: "is still in the queue",
	})

	var body struct {
		Error string `json:"error"`
		Key   string `json:"idempotency_key"`
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the 409 body is not JSON: %v (%s)", err, w.Body.String())
	}
	if body.JobID != "42.0" {
		t.Errorf("job_id = %q, want 42.0", body.JobID)
	}
	if body.Key != "batch-7" {
		t.Errorf("idempotency_key = %q, want batch-7", body.Key)
	}
	if body.Error == "" {
		t.Error("the 409 body carries no message")
	}
}

// TestOpenAPIDocumentsTheIdempotencyKey keeps the schema honest about
// this feature.
//
// The schema is a hand-maintained JSON string literal, so a client's
// only description of the header -- and of the two statuses it
// introduces that no other submit failure uses -- drifts silently. A 409
// a client has never been told about is indistinguishable to it from a
// bug.
func TestOpenAPIDocumentsTheIdempotencyKey(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(openAPISchema), &doc); err != nil {
		t.Fatalf("the OpenAPI schema is not valid JSON: %v", err)
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("the schema has no paths object")
	}
	jobs, ok := paths["/jobs"].(map[string]any)
	if !ok {
		t.Fatal("the schema does not describe /jobs")
	}
	post, ok := jobs["post"].(map[string]any)
	if !ok {
		t.Fatal("the schema does not describe POST /jobs")
	}

	params, _ := post["parameters"].([]any)
	found := false
	for _, p := range params {
		m, _ := p.(map[string]any)
		if m != nil && m["name"] == IdempotencyKeyHeader {
			found = true
			if m["in"] != "header" {
				t.Errorf("%s is documented in %v, not in the header", IdempotencyKeyHeader, m["in"])
			}
		}
	}
	if !found {
		t.Errorf("POST /jobs does not document the %s header", IdempotencyKeyHeader)
	}

	resp, _ := post["responses"].(map[string]any)
	for _, code := range []string{"409", "503"} {
		if _, ok := resp[code]; !ok {
			t.Errorf("POST /jobs does not document a %s response, which only a keyed "+
				"submission can produce", code)
		}
	}
}

// The Guard is worth nothing if a submit surface bypasses it, and the
// three tests below are source-level for the reason
// TestEverySubmitSiteAppliesThePolicy is: the alternative is standing up
// a schedd and a mirror per surface, and what actually goes wrong is
// someone adding a submit path, or reordering a constructor, without
// knowing this exists.

// TestBothSubmitDoorsCheckTheKey covers the two "one door" functions --
// this package's and the MCP server's. A surface that reaches a schedd
// without passing through one of them is not checked at all, and a
// submission that is silently not checked is the failure this feature
// exists to prevent.
func TestBothSubmitDoorsCheckTheKey(t *testing.T) {
	doors := map[string]string{
		"httpserver.submitApplied": mustRead(t, "required_creds.go"),
		"mcpserver.submitApplied":  mustRead(t, "../mcpserver/server.go"),
	}
	for name, src := range doors {
		if !strings.Contains(src, "idempotency.Begin(") {
			t.Errorf("%s does not call the idempotency Guard; submissions through it carry "+
				"a key that nothing enforces", name)
		}
		// Commit, not just Begin: a door that claims a key and never
		// settles it leaves every successful submission's key in flight
		// forever, so the client's own next submission is refused.
		if !strings.Contains(src, "claim.Commit(") {
			t.Errorf("%s never commits its claim; a submitted key would stay in flight", name)
		}
		if !strings.Contains(src, "defer claim.Abandon()") {
			t.Errorf("%s does not defer Abandon; a failed submission would hold its key "+
				"for the whole retention and lock the client out of a retry", name)
		}
	}
}

// TestMCPServerInheritsTheIdempotencyGuard: the agent surface must share
// THIS handler's Guard, not get its own and not get none. Its own would
// mean a key used by a POST and then by submit_job passes twice, because
// the claims that cover the queue-to-history gap would be in two
// separate maps.
func TestMCPServerInheritsTheIdempotencyGuard(t *testing.T) {
	// Whitespace-collapsed, as the submit-policy twin does, because the
	// field's alignment is gofmt's business.
	src := strings.Join(strings.Fields(mustRead(t, "handler.go")), " ")
	if !strings.Contains(src, "Idempotency: h.idempotency,") {
		t.Error("the MCP server is built without the idempotency Guard; agent submissions " +
			"would carry a key nothing enforces")
	}
}

// TestTheGuardIsBuiltBeforeTheMCPServer pins an ordering that fails
// silently.
//
// Both happen inside NewHandler. If the Guard's assignment ever moves
// below the MCP server's construction, the MCP server is handed a nil
// Guard -- which is legal, nil-safe, and enforces nothing. Every test
// here would still pass and agent submissions would quietly stop being
// checked.
func TestTheGuardIsBuiltBeforeTheMCPServer(t *testing.T) {
	src := mustRead(t, "handler.go")
	built := strings.Index(src, "h.idempotency = idempotency.NewGuard(")
	if built < 0 {
		t.Fatal("NewHandler no longer builds an idempotency Guard")
	}
	// Both offsets into the SAME string, so they are comparable; the
	// whitespace-collapsed copy the test above uses is a different
	// coordinate system.
	handed := strings.Index(src, "h.idempotency,")
	if handed < 0 {
		t.Fatal("could not find where the Guard is handed to the MCP server")
	}
	if built > handed {
		t.Error("the idempotency Guard is built AFTER it is handed to the MCP server, " +
			"so the MCP server gets a nil Guard and enforces nothing")
	}
	// Guard against both markers resolving to one line, which would make
	// the comparison above trivially true.
	if built == handed {
		t.Fatal("the two markers found the same position; this test is not measuring an order")
	}
}
