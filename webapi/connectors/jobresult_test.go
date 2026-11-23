package connectors

import (
	"strings"
	"testing"
)

const resultLimit = 1 << 20

// The shapes a job can plausibly emit, all of which someone will.
func TestParseJobResultShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []ResultRow
	}{
		{
			"one JSON object",
			`{"sample":"HG002","score":0.91}`,
			[]ResultRow{{"sample": "HG002", "score": "0.91"}},
		},
		{
			"an array of JSON objects",
			`[{"sample":"a","score":1},{"sample":"b","score":2}]`,
			[]ResultRow{
				{"sample": "a", "score": "1"},
				{"sample": "b", "score": "2"},
			},
		},
		{
			"a JSON table whose first row names the columns",
			`[["sample","score"],["a",1],["b",2]]`,
			[]ResultRow{
				{"sample": "a", "score": "1"},
				{"sample": "b", "score": "2"},
			},
		},
		{
			"CSV",
			"sample,score\na,1\nb,2\n",
			[]ResultRow{
				{"sample": "a", "score": "1"},
				{"sample": "b", "score": "2"},
			},
		},
		{
			// A cell with a comma is ordinary in CSV and must survive
			// the quoting the format exists for.
			"CSV with a quoted comma",
			"sample,note\na,\"first, second\"\n",
			[]ResultRow{{"sample": "a", "note": "first, second"}},
		},
		{
			// A short row is a gap, not a shift.
			"a short CSV row",
			"sample,score,note\na,1\n",
			[]ResultRow{{"sample": "a", "score": "1", "note": ""}},
		},
		{
			"a null is an empty cell",
			`{"sample":"a","note":null}`,
			[]ResultRow{{"sample": "a", "note": ""}},
		},
		{
			// %v would put map[a:1] in somebody's spreadsheet.
			"a nested value is rendered back as JSON",
			`{"sample":"a","extra":{"k":1}}`,
			[]ResultRow{{"sample": "a", "extra": `{"k":1}`}},
		},
		{
			// A big integer must not come back in exponent form: the
			// value is an identifier as often as it is a number.
			"a large integer keeps its digits",
			`{"id":100000000000000000000}`,
			[]ResultRow{{"id": "100000000000000000000"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseJobResult(strings.NewReader(tc.text), resultLimit)
			if err != nil {
				t.Fatalf("ParseJobResult: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows, want %d: %v", len(got), len(tc.want), got)
			}
			for i := range got {
				if len(got[i]) != len(tc.want[i]) {
					t.Errorf("row %d has %d fields, want %d: %v", i, len(got[i]), len(tc.want[i]), got[i])
				}
				for k, want := range tc.want[i] {
					if got[i][k] != want {
						t.Errorf("row %d field %q = %q, want %q", i, k, got[i][k], want)
					}
				}
			}
		})
	}
}

func TestParseJobResultRefusals(t *testing.T) {
	for _, tc := range []struct{ name, text, wantHas string }{
		{"an empty file", "   \n", "empty"},
		// Dispatch is on the first character, so a bare scalar goes to
		// the CSV reader and is refused as a header with no rows under
		// it. Pinned because the alternative -- sniffing -- would read
		// a legitimate one-column CSV header as a JSON string.
		{"a bare scalar", `42`, "no rows under them"},
		{"an empty JSON array", `[]`, "empty JSON array"},
		{"an array of plain values", `["a","b"]`, "needs objects"},
		{"objects after a first row", `[["a"],{"a":1}]`, "mixes rows and objects"},
		{"a nameless column", "sample,,score\na,b,c\n", "has no name"},
		{"a header with no rows", "sample,score\n", "no rows under them"},
		{"broken JSON", `{"a":`, "not readable as JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseJobResult(strings.NewReader(tc.text), resultLimit)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.wantHas) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantHas)
			}
		})
	}
}

// The bound is on this side, so a job that writes a gigabyte fails here
// rather than in the memory of the server appending it.
func TestParseJobResultIsBounded(t *testing.T) {
	big := `{"a":"` + strings.Repeat("x", 500) + `"}`
	if _, err := ParseJobResult(strings.NewReader(big), 100); err == nil {
		t.Error("accepted output past the limit")
	}
	// And exactly at the limit is accepted, because off-by-one here is
	// a job that fails for no reason the person can see.
	exact := `{"a":"b"}`
	if _, err := ParseJobResult(strings.NewReader(exact), int64(len(exact))); err != nil {
		t.Errorf("refused output exactly at the limit: %v", err)
	}
}

// FitToHeaders: the sheet's header row decides, in both directions.
func TestFitToHeaders(t *testing.T) {
	headers := []string{"sample", "score", "note"}
	rows := []ResultRow{
		{"sample": "a", "score": "1", "note": "ok"},
		// A column the job did not produce is blank, not shifted.
		{"sample": "b", "note": "later"},
		// A field the sheet has no column for is left out, and the row
		// keeps its shape.
		{"sample": "c", "score": "3", "elapsed": "12s", "host": "ep1"},
	}

	got, unused := FitToHeaders(headers, rows)
	want := [][]string{
		{"a", "1", "ok"},
		{"b", "", "later"},
		{"c", "3", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if len(got[i]) != len(headers) {
			t.Errorf("row %d has %d cells, want %d", i, len(got[i]), len(headers))
			continue
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Errorf("row %d cell %d = %q, want %q", i, j, got[i][j], want[i][j])
			}
		}
	}
	// The dropped names come back, so the caller can say a column of
	// results went nowhere. Silence here is the failure this is most
	// likely to have.
	if strings.Join(unused, ",") != "elapsed,host" {
		t.Errorf("unused = %v, want [elapsed host] in order", unused)
	}
}

// A header is a label somebody typed, so matching ignores case.
func TestFitToHeadersIgnoresCase(t *testing.T) {
	got, unused := FitToHeaders([]string{"Sample", "Score"},
		[]ResultRow{{"sample": "a", "SCORE": "1"}})
	if len(unused) != 0 {
		t.Errorf("unused = %v, want none", unused)
	}
	if len(got) != 1 || got[0][0] != "a" || got[0][1] != "1" {
		t.Errorf("rows = %v, want [[a 1]]", got)
	}
}

// A sheet with no header row yet: every field is unused, and the rows
// come out empty rather than inventing columns. Worth pinning, because
// the tempting behaviour -- write the fields in whatever order -- would
// put results under the wrong headings the moment somebody adds one.
func TestFitToHeadersWithNoHeaders(t *testing.T) {
	got, unused := FitToHeaders(nil, []ResultRow{{"sample": "a"}})
	if len(got) != 1 || len(got[0]) != 0 {
		t.Errorf("rows = %v, want one empty row", got)
	}
	if strings.Join(unused, ",") != "sample" {
		t.Errorf("unused = %v, want [sample]", unused)
	}
}
