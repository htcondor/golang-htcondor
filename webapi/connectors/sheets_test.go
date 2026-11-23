package connectors

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/connectors/mockgoogle"
)

// sheetsFixture is the shared OAuth harness with one connected account
// and a Sheets client pointed at the same stand-in, with the backoff's
// sleep replaced so a retry test does not actually wait.
type sheetsFixture struct {
	*harness
	g     *mockgoogle.Server
	s     *Sheets
	token string
}

func newSheetsFixture(t *testing.T) *sheetsFixture {
	t.Helper()
	h := newHarness(t)
	grant := h.connect(t, "alice")
	return &sheetsFixture{
		harness: h,
		g:       h.google,
		s: &Sheets{
			BaseURL: h.google.SheetsBaseURL(),
			Sleep:   func(time.Duration) {},
		},
		token: grant.AccessToken,
	}
}

// Tabs, in the order the workbook has them, because a submit page that
// lists them in map order shows a different list on every load.
func TestSheetsListsTabsInOrder(t *testing.T) {
	f := newSheetsFixture(t)
	f.g.PickSheet("SHEET", "params.gsheet",
		mockgoogle.MockTab{Title: "runs"},
		mockgoogle.MockTab{Title: "notes"},
		mockgoogle.MockTab{Title: "scratch"},
	)

	tabs, err := f.s.Tabs(t.Context(), f.token, "SHEET")
	if err != nil {
		t.Fatalf("Tabs: %v", err)
	}
	var got []string
	for _, tab := range tabs {
		got = append(got, tab.Title)
	}
	want := []string{"runs", "notes", "scratch"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tabs = %v, want %v", got, want)
	}
}

// A spreadsheet the user never picked is a 404, which under drive.file
// means "not granted" and not "missing". The remedy differs: pick the
// file, rather than go looking for it.
func TestSheetsUnpickedSpreadsheetIsNotGranted(t *testing.T) {
	f := newSheetsFixture(t)
	_, err := f.s.Tabs(t.Context(), f.token, "NEVER-PICKED")
	if !errors.Is(err, ErrNotGranted) {
		t.Errorf("error = %v, want ErrNotGranted", err)
	}
}

// Values are padded to a rectangle.
//
// Google omits trailing empty cells, so the second row below arrives
// one cell short. A caller indexing by column on an unpadded response
// reads the wrong column for that job -- and it is the LAST column that
// goes wrong, which is the one a table is most likely to have added
// recently.
func TestSheetsValuesArePaddedToARectangle(t *testing.T) {
	f := newSheetsFixture(t)
	f.g.PickSheet("SHEET", "params.gsheet", mockgoogle.MockTab{
		Title: "runs",
		Rows: [][]string{
			{"sample", "seed", "note"},
			{"a", "1"},
			{"b", "2", "third"},
		},
	})

	got, err := f.s.Values(t.Context(), f.token, "SHEET", "runs", 100)
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if got.Truncated {
		t.Error("Truncated is set for a tab well under the limit")
	}
	if len(got.Rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(got.Rows))
	}
	for i, row := range got.Rows {
		if len(row) != 3 {
			t.Errorf("row %d has %d cells, want 3 (padded): %v", i, len(row), row)
		}
	}
	if got.Rows[1][2] != "" {
		t.Errorf("the padded cell holds %q, want empty", got.Rows[1][2])
	}
	if got.Rows[2][2] != "third" {
		t.Errorf("row 2 column 2 = %q, want \"third\"", got.Rows[2][2])
	}
}

// Truncation is reported rather than silent. A table cut short submits
// fewer jobs than the spreadsheet lists, which looks like a success.
func TestSheetsReportsTruncation(t *testing.T) {
	f := newSheetsFixture(t)
	rows := [][]string{{"sample"}}
	for i := range 10 {
		rows = append(rows, []string{string(rune('a' + i))})
	}
	f.g.PickSheet("SHEET", "params.gsheet",
		mockgoogle.MockTab{Title: "runs", Rows: rows})

	got, err := f.s.Values(t.Context(), f.token, "SHEET", "runs", 4)
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if !got.Truncated {
		t.Error("Truncated is not set for a tab with more rows than the limit")
	}
	if len(got.Rows) != 4 {
		t.Errorf("got %d rows, want the limit of 4", len(got.Rows))
	}
}

// A tab that is exactly at the limit is not truncated. The off-by-one
// matters because it is the difference between "all your jobs" and a
// warning telling the user rows were dropped when none were.
func TestSheetsExactlyAtTheLimitIsNotTruncated(t *testing.T) {
	f := newSheetsFixture(t)
	f.g.PickSheet("SHEET", "params.gsheet", mockgoogle.MockTab{
		Title: "runs",
		Rows:  [][]string{{"sample"}, {"a"}, {"b"}},
	})

	got, err := f.s.Values(t.Context(), f.token, "SHEET", "runs", 3)
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if got.Truncated {
		t.Error("Truncated is set for a tab that ends exactly at the limit")
	}
	if len(got.Rows) != 3 {
		t.Errorf("got %d rows, want 3", len(got.Rows))
	}
}

// An empty tab is not an error. The API sends no "values" key at all
// for one, which a client decoding into a slice sees as nil.
func TestSheetsEmptyTabIsNotAnError(t *testing.T) {
	f := newSheetsFixture(t)
	f.g.PickSheet("SHEET", "params.gsheet", mockgoogle.MockTab{Title: "blank"})

	got, err := f.s.Values(t.Context(), f.token, "SHEET", "blank", 100)
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if len(got.Rows) != 0 {
		t.Errorf("got %d rows from an empty tab, want 0", len(got.Rows))
	}
}

// A tab title with a quote in it. A1 notation quotes titles with single
// quotes and escapes an embedded one by doubling it; getting that wrong
// is a range the API refuses, and "Unable to parse range" is not a
// message that points at the apostrophe in somebody's tab name.
func TestSheetsTabTitleWithAQuote(t *testing.T) {
	f := newSheetsFixture(t)
	f.g.PickSheet("SHEET", "params.gsheet", mockgoogle.MockTab{
		Title: "alice's runs",
		Rows:  [][]string{{"sample"}, {"a"}},
	})

	got, err := f.s.Values(t.Context(), f.token, "SHEET", "alice's runs", 100)
	if err != nil {
		t.Fatalf("Values on a title with a quote: %v", err)
	}
	if len(got.Rows) != 2 {
		t.Errorf("got %d rows, want 2", len(got.Rows))
	}
}

// Numbers arrive as JSON numbers, not strings, and must not come out as
// Go's default float rendering. A seed of 1 is "1", not "1e+00".
func TestSheetsRendersCellsAsDisplayedText(t *testing.T) {
	for _, tc := range []struct {
		name string
		cell any
		want string
	}{
		{"an integer", float64(1), "1"},
		{"a large integer", float64(1e21), "1000000000000000000000"},
		{"a fraction", 0.5, "0.5"},
		{"a boolean", true, "true"},
		{"an empty cell", nil, ""},
		{"text", "a b", "a b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cellString(tc.cell); got != tc.want {
				t.Errorf("cellString(%v) = %q, want %q", tc.cell, got, tc.want)
			}
		})
	}
}

// The retry path is the transport's, shared with Drive. Worth one case
// here so a future change to the Sheets client cannot quietly stop
// using it.
func TestSheetsRetriesARateLimit(t *testing.T) {
	f := newSheetsFixture(t)
	f.g.PickSheet("SHEET", "params.gsheet", mockgoogle.MockTab{
		Title: "runs",
		Rows:  [][]string{{"sample"}, {"a"}},
	})
	f.g.FailNext(http.StatusTooManyRequests)

	got, err := f.s.Values(t.Context(), f.token, "SHEET", "runs", 100)
	if err != nil {
		t.Fatalf("Values did not retry a 429: %v", err)
	}
	if len(got.Rows) != 2 {
		t.Errorf("got %d rows, want 2", len(got.Rows))
	}
}

// maxRows must be positive. Zero would ask for the range `1:1`, which
// is the header row alone -- a table with no jobs in it, reported as a
// success.
func TestSheetsRefusesANonPositiveLimit(t *testing.T) {
	f := newSheetsFixture(t)
	if _, err := f.s.Values(t.Context(), f.token, "SHEET", "runs", 0); err == nil {
		t.Error("Values accepted maxRows = 0")
	}
}

// Appending goes on the END of the tab, leaving what was there alone.
//
// One server-side call is the whole reason a result goes in this way:
// two jobs finishing at the same moment do not have to agree about
// which row is next.
func TestSheetsAppendsToTheEnd(t *testing.T) {
	f := newSheetsFixture(t)
	f.g.PickSheet("SHEET", "results.gsheet", mockgoogle.MockTab{
		Title: "runs",
		Rows: [][]string{
			{"sample", "score"},
			{"a", "1"},
		},
	})

	err := f.s.AppendRows(t.Context(), f.token, "SHEET", "runs",
		[][]string{{"b", "2"}, {"c", "3"}})
	if err != nil {
		t.Fatalf("AppendRows: %v", err)
	}

	got := f.g.SheetRows("SHEET", "runs")
	want := [][]string{
		{"sample", "score"},
		{"a", "1"},
		{"b", "2"},
		{"c", "3"},
	}
	if len(got) != len(want) {
		t.Fatalf("the tab has %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if strings.Join(got[i], ",") != strings.Join(want[i], ",") {
			t.Errorf("row %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// Appending nothing is not a request. A job that produced no records
// should leave the spreadsheet untouched rather than reach Google.
func TestSheetsAppendOfNothingIsNoRequest(t *testing.T) {
	f := newSheetsFixture(t)
	f.g.PickSheet("SHEET", "results.gsheet", mockgoogle.MockTab{Title: "runs"})
	f.g.FailNext(http.StatusInternalServerError)

	if err := f.s.AppendRows(t.Context(), f.token, "SHEET", "runs", nil); err != nil {
		t.Errorf("appending no rows made a request: %v", err)
	}
}

// A spreadsheet the user never picked cannot be written to either.
func TestSheetsAppendToUnpickedIsNotGranted(t *testing.T) {
	f := newSheetsFixture(t)
	err := f.s.AppendRows(t.Context(), f.token, "NEVER-PICKED", "runs",
		[][]string{{"a"}})
	if !errors.Is(err, ErrNotGranted) {
		t.Errorf("error = %v, want ErrNotGranted", err)
	}
}
