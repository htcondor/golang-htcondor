package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/webapi/connectors/mockgoogle"
)

// The spreadsheet reads behind /api/v1/connect/{id}/sheets/{fileID}.
//
// Reached through handleConnectItem, so the session, the connector and
// the caller's group membership are gated before any of this runs --
// handlers_connect_test.go covers that, and these cover what the route
// adds.

// sheetFixture is the connect fixture with a connected account and both
// Google roots pointed at the stand-in.
func newSheetFixture(t *testing.T) (*connectFixture, *http.Client) {
	t.Helper()
	f := newConnectFixture(t, "lab")
	f.h.connectorDriveBaseURL = f.google.DriveBaseURL()
	f.h.connectorSheetsBaseURL = f.google.SheetsBaseURL()
	c := f.login(t, "alice", "lab")
	f.connectAs(t, c)
	return f, c
}

func decodeSheet(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding the reply: %v", err)
	}
	return out
}

// The tab list, in workbook order, with the file's own name alongside
// it: the page shows "which spreadsheet" as well as "which tab".
func TestSheetRouteListsTabs(t *testing.T) {
	f, c := newSheetFixture(t)
	f.google.PickSheet("SHEET", "parameters.gsheet",
		mockgoogle.MockTab{Title: "runs"},
		mockgoogle.MockTab{Title: "archive"},
	)

	//nolint:bodyclose // closed by decodeSheet, or explicitly below
	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive/sheets/SHEET", "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body := decodeSheet(t, resp)

	if body["name"] != "parameters.gsheet" {
		t.Errorf("name = %v, want the spreadsheet's name", body["name"])
	}
	tabs, _ := body["tabs"].([]any)
	if len(tabs) != 2 {
		t.Fatalf("got %d tabs, want 2", len(tabs))
	}
	first, _ := tabs[0].(map[string]any)
	second, _ := tabs[1].(map[string]any)
	if first["title"] != "runs" || second["title"] != "archive" {
		t.Errorf("tabs = %v, want them in workbook order", tabs)
	}
}

// A file that is not a spreadsheet is refused by name and type, rather
// than relayed as whatever the Sheets API says about a range it could
// not parse.
func TestSheetRouteRefusesANonSpreadsheet(t *testing.T) {
	f, c := newSheetFixture(t)
	f.google.PickFile("CSV", "params.csv", []byte("sample\na\n"))

	//nolint:bodyclose // closed by decodeSheet, or explicitly below
	resp := f.get(t, c, "/api/v1/connect/builtin:gdrive/sheets/CSV", "application/json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	body := decodeSheet(t, resp)
	// writeError puts the status text in "error" and the detail in
	// "message"; the detail is the part a person can act on.
	msg := asString(body["message"])
	if msg == "" {
		t.Fatalf("no message in %v", body)
	}
	for _, want := range []string{"params.csv", "not a Google Sheet"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

// A tab's rows come back rectangular. The stand-in omits trailing empty
// cells exactly as Google does, so this is the padding the submit page
// depends on to index a column.
func TestSheetRouteReturnsRectangularRows(t *testing.T) {
	f, c := newSheetFixture(t)
	f.google.PickSheet("SHEET", "parameters.gsheet", mockgoogle.MockTab{
		Title: "runs",
		Rows: [][]string{
			{"sample", "seed", "note"},
			{"a", "1"},
			{"b", "2", "third"},
		},
	})

	//nolint:bodyclose // closed by decodeSheet, or explicitly below
	resp := f.get(t, c,
		"/api/v1/connect/builtin:gdrive/sheets/SHEET?tab=runs", "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body := decodeSheet(t, resp)

	if body["truncated"] != false {
		t.Errorf("truncated = %v, want false", body["truncated"])
	}
	rows, _ := body["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for i, raw := range rows {
		row, _ := raw.([]any)
		if len(row) != 3 {
			t.Errorf("row %d has %d cells, want 3: %v", i, len(row), row)
		}
	}
}

// maxRows is a ceiling the caller may lower and not raise. Otherwise
// the bound on one read belongs to whoever is asking.
func TestSheetRouteClampsMaxRows(t *testing.T) {
	f, c := newSheetFixture(t)
	f.google.PickSheet("SHEET", "parameters.gsheet", mockgoogle.MockTab{
		Title: "runs",
		Rows:  [][]string{{"sample"}, {"a"}, {"b"}},
	})

	//nolint:bodyclose // closed by decodeSheet, or explicitly below
	resp := f.get(t, c,
		"/api/v1/connect/builtin:gdrive/sheets/SHEET?tab=runs&maxRows=999999",
		"application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body := decodeSheet(t, resp)
	got, _ := body["maxRows"].(float64)
	if int(got) != sheetMaxRows {
		t.Errorf("maxRows = %v, want it clamped to %d", body["maxRows"], sheetMaxRows)
	}
}

// A spreadsheet the user never picked is 403, not 404.
//
// Under drive.file the provider says 404, but relaying that to the
// browser reads as "the spreadsheet is gone" when the truth is "you
// have not picked it" -- and those have different remedies.
func TestSheetRouteUnpickedIsForbiddenNotMissing(t *testing.T) {
	f, c := newSheetFixture(t)

	//nolint:bodyclose // closed by decodeSheet, or explicitly below
	resp := f.get(t, c,
		"/api/v1/connect/builtin:gdrive/sheets/NEVER-PICKED", "application/json")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
