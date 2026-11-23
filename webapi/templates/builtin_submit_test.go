package templates_test

import (
	"strings"
	"testing"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// TestBuiltinTemplatesParseAndExpand walks every built-in batch
// template and proves it round-trips through the same path the web
// UI's /submit page exercises:
//
//  1. Take template.Contents (the submit-file body without queue).
//  2. Append `queue COLS from ((rows))` — the inline form the
//     frontend's buildSubmitFile() emits.
//  3. ParseSubmitFile must succeed (no goyacc "syntax error").
//  4. submitFile.Submit() must produce one proc per row.
//  5. Each row's variable substitutions must land in the expected
//     attribute (e.g. sleep's $(seconds) → Args).
//
// This is the regression-guard for the inline-`((...))` pre-processor
// in the core submit parser and for the built-in YAML staying
// parser-clean. It lives in the webapi module because that is where the
// templates library now resides; it imports the core htcondor package
// for the submit parser it is guarding.
func TestBuiltinTemplatesParseAndExpand(t *testing.T) {
	lib, err := templates.NewLibrary(templates.LibraryConfig{})
	if err != nil {
		t.Fatalf("templates.NewLibrary: %v", err)
	}
	t.Cleanup(func() { _ = lib.Close() })

	all := lib.All("")
	if len(all) == 0 {
		t.Fatalf("expected built-in templates, got 0")
	}

	// Sample row data per template id. Built-in templates have
	// well-known column names so the test can pre-populate sensible
	// values; new templates without an entry below get a single row
	// of placeholders so they still get covered (loosely).
	sampleRows := map[string][][]string{
		"hello-world": {{"alice"}, {"bob"}, {"carol"}},
		"sleep":       {{"5"}, {"30"}, {"60"}},
	}

	for _, tpl := range all {
		t.Run(tpl.ID, func(t *testing.T) {
			rows, ok := sampleRows[tpl.ID]
			if !ok {
				// Default coverage: one row of placeholders so we
				// still exercise the parse+expand path even for
				// templates the test hasn't been taught about yet.
				row := make([]string, len(tpl.Columns))
				for i := range row {
					row[i] = "x"
				}
				rows = [][]string{row}
			}

			body := buildBatchSubmitFile(tpl, rows)
			t.Logf("synthesized submit file:\n%s", body)

			sf, err := htcondor.ParseSubmitFile(strings.NewReader(body))
			if err != nil {
				t.Fatalf("ParseSubmitFile: %v\n--- body ---\n%s", err, body)
			}

			res, err := sf.Submit(42) // arbitrary cluster id
			if err != nil {
				t.Fatalf("submitFile.Submit: %v", err)
			}
			if len(res.ProcAds) != len(rows) {
				t.Fatalf("expected %d procs (one per row), got %d",
					len(rows), len(res.ProcAds))
			}

			// Spot-check the expected substitutions per template.
			switch tpl.ID {
			case "hello-world":
				// The submit parser converts HTCondor's "new"
				// arguments form back into the "old" Args quoting
				// (using `' '` between args). The starter decodes
				// that back to argv at job-run time. We only need
				// to assert the per-row substitution actually
				// landed in the attribute somewhere.
				for i, ad := range res.ProcAds {
					args, _ := ad.EvaluateAttrString("Args")
					if args == "" {
						args, _ = ad.EvaluateAttrString("Arguments")
					}
					if !strings.Contains(args, rows[i][0]) {
						t.Errorf("proc %d: arguments=%q does not contain row value %q",
							i, args, rows[i][0])
					}
				}
			case "sleep":
				for i, ad := range res.ProcAds {
					args, _ := ad.EvaluateAttrString("Args")
					if args != rows[i][0] {
						t.Errorf("proc %d: Args=%q want %q", i, args, rows[i][0])
					}
				}
			}
		})
	}
}

// buildBatchSubmitFile is the Go-side mirror of the frontend's
// buildSubmitFile: append a `queue cols from ((<rows>))` block to the
// template body. We intentionally produce the exact form the SPA
// emits so this test catches drift in either direction.
func buildBatchSubmitFile(tpl templates.Template, rows [][]string) string {
	body := strings.TrimRight(tpl.Contents, " \t\n")
	if len(tpl.Columns) == 0 {
		return body + "\nqueue\n"
	}
	var sb strings.Builder
	sb.WriteString(body)
	sb.WriteString("\nqueue ")
	sb.WriteString(strings.Join(templates.ColumnNames(tpl.Columns), ", "))
	sb.WriteString(" from ((\n")
	for _, r := range rows {
		sb.WriteString("  ")
		sb.WriteString(strings.Join(r, " "))
		sb.WriteByte('\n')
	}
	sb.WriteString("))\n")
	return sb.String()
}

// A row with SEVERAL columns, put through the real parser.
//
// TestBuiltinTemplatesParseAndExpand already guards the `((...))`
// form, but every built-in template it walks has exactly one column, so
// nothing in this repository had ever asked HTCondor what it makes of a
// row of several values. The two inline forms answer differently, and
// the difference is silent: both parse, both submit, and one of them
// submits a job per CELL with the variables unset.
//
// That is not hypothetical. buildTableQueue in
// frontend/src/lib/submitFile.ts emitted the `in` form for multi-column
// tables, and its unit test asserted that text and passed, because a
// test that compares a generated string to the string you expected
// cannot tell you HTCondor disagrees. This is the measurement that was
// missing.
//
// `in` takes ONE variable name and splits its list on whitespace as
// well as commas; only `from` reads a line as a row of columns. Checked
// against condor_submit 26.2.0, which agrees with the parser here.
func TestMultiColumnQueueFormsDisagree(t *testing.T) {
	const body = "executable = /bin/true\narguments = \"$(name) $(ref)\"\n"

	cases := []struct {
		name  string
		queue string
		procs int
	}{
		{
			name:  "from, which is what the UI emits",
			queue: "queue name, ref from ((\n  a.csv urlA\n  b.csv urlB\n))\n",
			procs: 2,
		},
		{
			// Pinned, not endorsed: this is the wrong answer, and it is
			// written down so that a change in it is visible and so
			// that nobody reaches for `in` again.
			name:  "in, which queues one job per cell",
			queue: "queue name, ref in (a.csv urlA, b.csv urlB)\n",
			procs: 4,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sf, err := htcondor.ParseSubmitFile(strings.NewReader(body + c.queue))
			if err != nil {
				t.Fatalf("ParseSubmitFile: %v", err)
			}
			res, err := sf.Submit(42)
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if len(res.ProcAds) != c.procs {
				t.Fatalf("got %d procs, want %d", len(res.ProcAds), c.procs)
			}
		})
	}

	// And the values land in the right job, which the count alone does
	// not say: two procs with the columns swapped would count the same.
	sf, err := htcondor.ParseSubmitFile(strings.NewReader(
		body + "queue name, ref from ((\n  a.csv urlA\n  b.csv urlB\n))\n"))
	if err != nil {
		t.Fatalf("ParseSubmitFile: %v", err)
	}
	res, err := sf.Submit(42)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	want := []string{"a.csv urlA", "b.csv urlB"}
	for i, ad := range res.ProcAds {
		args, _ := ad.EvaluateAttrString("Args")
		if args == "" {
			args, _ = ad.EvaluateAttrString("Arguments")
		}
		if !strings.Contains(args, want[i]) {
			t.Errorf("proc %d: arguments=%q does not hold %q; the row's columns did "+
				"not reach this job in order", i, args, want[i])
		}
	}
}
