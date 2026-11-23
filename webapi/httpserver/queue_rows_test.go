package httpserver

import (
	"strings"
	"testing"

	htcondor "github.com/htcondor/golang-htcondor"
)

// composeQueueRows and the rules it enforces.
//
// The test that matters most is the round trip: the point of carrying
// rows as data is that a cell can hold a space and a comma, and the
// only way to know it does is to put the composed file through the
// submit parser and read the value back off the job ad.

const rowBody = "executable = /bin/true\narguments = \"[$(sample)][$(note)]\"\n"

// A cell with a space AND a comma reaches the job ad intact.
//
// Neither is expressible in the form this replaced: whitespace and
// commas are both field separators in an item row, and nothing quotes.
// US is what makes it possible, and this is the assertion that US is
// actually being used -- a space-joined row would split into four
// fields here and the values would be wrong rather than the parse
// failing.
func TestQueueRowsCarryASpaceAndAComma(t *testing.T) {
	text, err := composeQueueRows(rowBody,
		[]string{"sample", "note"},
		[][]string{
			{"HG002 run 3", "first, second"},
			{"HG003", "plain"},
		})
	if err != nil {
		t.Fatalf("composeQueueRows: %v", err)
	}

	sf, err := htcondor.ParseSubmitFile(strings.NewReader(text))
	if err != nil {
		t.Fatalf("the composed file did not parse: %v\n%s", err, text)
	}
	res, err := sf.Submit(42)
	if err != nil {
		t.Fatalf("Submit: %v\n%s", err, text)
	}
	if len(res.ProcAds) != 2 {
		t.Fatalf("got %d procs, want 2", len(res.ProcAds))
	}
	want := []string{"[HG002 run 3][first, second]", "[HG003][plain]"}
	for i, ad := range res.ProcAds {
		got, _ := ad.EvaluateAttrString("Args")
		if got == "" {
			got, _ = ad.EvaluateAttrString("Arguments")
		}
		if got != want[i] {
			t.Errorf("proc %d arguments = %q, want %q", i, got, want[i])
		}
	}
}

// The separator is in the text this builds, and only there. A caller
// never sends it and a page never shows it, so this pins that the
// composition is what introduces it.
func TestQueueRowsJoinWithTheUnitSeparator(t *testing.T) {
	text, err := composeQueueRows(rowBody,
		[]string{"sample", "note"},
		[][]string{{"a", "b"}})
	if err != nil {
		t.Fatalf("composeQueueRows: %v", err)
	}
	if !strings.Contains(text, "a"+htcondor.UnitSeparator+"b") {
		t.Errorf("the row is not US-joined:\n%q", text)
	}
	if strings.Contains(text, "a b") {
		t.Errorf("the row looks space-joined, which splits into two columns:\n%q", text)
	}
}

// An empty trailing cell is a blank spreadsheet cell, which is ordinary
// and must survive as an empty value rather than collapsing the row.
func TestQueueRowsKeepAnEmptyTrailingCell(t *testing.T) {
	text, err := composeQueueRows(rowBody,
		[]string{"sample", "note"},
		[][]string{{"a", ""}})
	if err != nil {
		t.Fatalf("composeQueueRows: %v", err)
	}
	sf, err := htcondor.ParseSubmitFile(strings.NewReader(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := sf.Submit(42)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(res.ProcAds) != 1 {
		t.Fatalf("got %d procs, want 1", len(res.ProcAds))
	}
	got, _ := res.ProcAds[0].EvaluateAttrString("Args")
	if got == "" {
		got, _ = res.ProcAds[0].EvaluateAttrString("Arguments")
	}
	if got != "[a][]" {
		t.Errorf("arguments = %q, want \"[a][]\"", got)
	}
}

// What is refused, and why each one is not merely tidiness.
func TestQueueRowsRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		vars    []string
		rows    [][]string
		wantHas string
	}{
		{
			"no columns",
			nil, [][]string{{"a"}}, "queue_vars is empty",
		},
		{
			"no rows",
			[]string{"sample"}, nil, "queue_rows is empty",
		},
		{
			"a column name that is not a variable",
			[]string{"two words"}, [][]string{{"a"}}, "not a submit variable name",
		},
		{
			// Submit macros are case-insensitive, so these are one
			// variable and the second silently wins.
			"two columns differing only in case",
			[]string{"sample", "SAMPLE"}, [][]string{{"a", "b"}}, "one submit variable",
		},
		{
			"a short row",
			[]string{"a", "b"}, [][]string{{"x"}}, "shift every column",
		},
		{
			"a line break in a cell",
			[]string{"a"}, [][]string{{"x\ny"}}, "line break",
		},
		{
			// The invisible one: a literal US adds a column and looks
			// identical in every display there is.
			"a unit separator in a cell",
			[]string{"a", "b"}, [][]string{{"x" + htcondor.UnitSeparator + "y", "z"}},
			"unit separator",
		},
		{
			"a macro reference in a cell",
			[]string{"a"}, [][]string{{"$(executable)"}}, "expand",
		},
		{
			// Accepted and then silently altered is worse than refused:
			// HTCondor trims each field at the split point, so the job
			// would run on a value the spreadsheet does not show.
			"trailing whitespace in a cell",
			[]string{"a"}, [][]string{{"x "}}, "trim off",
		},
		{
			"a row that is empty everywhere",
			[]string{"a", "b"}, [][]string{{"", ""}}, "empty in every column",
		},
		{
			"a row that starts with a comment marker",
			[]string{"a"}, [][]string{{"#x"}}, "dropped as a comment",
		},
		{
			// The injection: the row ends the item list and the rest of
			// the file is read as submit commands.
			"a row that closes the item list",
			[]string{"a"}, [][]string{{")"}}, "end the job list early",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := composeQueueRows(rowBody, tc.vars, tc.rows)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.wantHas) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantHas)
			}
		})
	}
}

// A refusal names the cell, because the message goes back to somebody
// looking at a spreadsheet who needs to find it.
func TestQueueRowsRefusalNamesTheCell(t *testing.T) {
	_, err := composeQueueRows(rowBody,
		[]string{"sample", "note"},
		[][]string{
			{"ok", "fine"},
			{"ok", "bad$value"},
		})
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"row 2", `"note"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// The body keeps its own content and gains exactly one queue statement.
func TestQueueRowsAppendOneQueueStatement(t *testing.T) {
	text, err := composeQueueRows("executable = /bin/true\n",
		[]string{"a"}, [][]string{{"x"}, {"y"}})
	if err != nil {
		t.Fatalf("composeQueueRows: %v", err)
	}
	if n := strings.Count(text, "queue "); n != 1 {
		t.Errorf("the composed file has %d queue statements, want 1:\n%s", n, text)
	}
	if !strings.Contains(text, "executable = /bin/true") {
		t.Errorf("the body was lost:\n%s", text)
	}
	if !strings.HasSuffix(text, ")\n") {
		t.Errorf("the item list is not closed:\n%s", text)
	}
}
