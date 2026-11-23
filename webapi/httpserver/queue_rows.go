package httpserver

import (
	"fmt"
	"regexp"
	"strings"

	htcondor "github.com/htcondor/golang-htcondor"
)

// Per-job parameter rows, carried as data rather than as submit text.
//
// A table of parameters used to reach this server already written into
// a `queue COLS from (...)` block by the browser, which meant the
// browser had to know how HTCondor splits a row. It does not split the
// way anything else does: whitespace AND commas separate fields, and
// nothing quotes, so a cell holding "two words" became two columns. The
// page tried to quote such a cell, which produced a file that queued
// jobs with a stray double-quote in them.
//
// So the rows come as rows now, and this builds the block. The join is
// the ASCII unit separator, which HTCondor autodetects as the only
// separator for a row that contains one (htcondor.UnitSeparator), and
// that is what lets a cell hold a space or a comma at all.
//
// The separator never leaves this server. The browser sends and
// displays a table; the submit text with US bytes in it exists between
// this function and the parser and nowhere else. That is deliberate: a
// control character in a document a person reads, downloads, diffs and
// hand-edits is invisible until they break a row with it.

// queueStatementRE finds a queue statement anywhere in a submit file.
// Used to refuse a body that already has one when rows are also sent:
// two queue statements submit two clusters' worth of work from one
// request, and the second would be the one nobody asked for.
var queueStatementRE = regexp.MustCompile(`(?im)^\s*queue\b`)

// queueVarName is HTCondor's rule for a submit variable name.
var queueVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// composeQueueRows appends a `queue VARS from (rows)` block to a submit
// file body.
//
// Everything it refuses, it refuses by naming the row and the column,
// because the message goes back to somebody looking at a spreadsheet
// who needs to find the cell rather than learn the rule.
func composeQueueRows(body string, vars []string, rows [][]string) (string, error) {
	if len(vars) == 0 {
		return "", fmt.Errorf("queue_vars is empty, so there is nothing to name the columns")
	}
	seen := make(map[string]int, len(vars))
	for i, v := range vars {
		if !queueVarName.MatchString(v) {
			return "", fmt.Errorf("queue_vars[%d] (%q) is not a submit variable name: "+
				"letters, digits and underscore, not starting with a digit", i, v)
		}
		// Submit macros are case-insensitive, so two columns differing
		// only in case are one variable and the second would silently
		// win.
		key := strings.ToLower(v)
		if prev, dup := seen[key]; dup {
			return "", fmt.Errorf("queue_vars[%d] and queue_vars[%d] are both %q, "+
				"which is one submit variable", prev, i, v)
		}
		seen[key] = i
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("queue_rows is empty, so there are no jobs to queue")
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\nqueue ")
	b.WriteString(strings.Join(vars, ", "))
	b.WriteString(" from (\n")
	for i, row := range rows {
		if err := checkQueueRow(row, vars, i); err != nil {
			return "", err
		}
		b.WriteString("  ")
		b.WriteString(strings.Join(row, htcondor.UnitSeparator))
		b.WriteString("\n")
	}
	b.WriteString(")\n")
	return b.String(), nil
}

// checkQueueRow refuses a row the submit parser would read as something
// other than this row.
func checkQueueRow(row []string, vars []string, idx int) error {
	// Row numbers are 1-based and count the header the way a
	// spreadsheet does, so row 1 here is the first row of DATA. Saying
	// "row 1" for the first job is what the person sees.
	where := func(col int) string {
		return fmt.Sprintf("row %d, column %q", idx+1, vars[col])
	}

	if len(row) != len(vars) {
		return fmt.Errorf("row %d has %d values but there are %d columns; "+
			"a short row would shift every column after the gap",
			idx+1, len(row), len(vars))
	}

	empty := true
	for col, cell := range row {
		if cell != "" {
			empty = false
		}
		switch {
		case strings.ContainsAny(cell, "\n\r"):
			return fmt.Errorf("%s contains a line break, which would split the row in two",
				where(col))
		case strings.ContainsRune(cell, 0):
			return fmt.Errorf("%s contains a NUL byte", where(col))
		case strings.Contains(cell, htcondor.UnitSeparator):
			// The one that would be invisible: a literal US in a cell
			// adds a column, and the value looks unchanged in every
			// display there is.
			return fmt.Errorf("%s contains an ASCII unit separator (0x1F), "+
				"which is the character separating one column from the next", where(col))
		case strings.Contains(cell, "$"):
			return fmt.Errorf("%s contains \"$\", which the submit file would expand "+
				"as a macro rather than use as text", where(col))
		case cell != strings.TrimSpace(cell):
			// HTCondor trims each field at the split point, so this
			// would be accepted and then silently altered -- which is
			// worse than being refused, because the job runs on a value
			// the spreadsheet does not show.
			return fmt.Errorf("%s has leading or trailing whitespace, "+
				"which HTCondor would trim off rather than keep", where(col))
		}
	}
	if empty {
		return fmt.Errorf("row %d is empty in every column, which queues a job with no "+
			"parameters rather than no job", idx+1)
	}

	// The row's first cell starts the LINE, so these two decide whether
	// the line is a row at all.
	switch {
	case strings.HasPrefix(row[0], "#"):
		return fmt.Errorf("%s starts with \"#\", which is dropped as a comment "+
			"rather than queued", where(0))
	case strings.HasPrefix(row[0], ")"):
		return fmt.Errorf("%s starts with \")\", which would end the job list early: "+
			"the rest of the file is then read as submit commands, and the jobs that "+
			"run are not the ones listed here", where(0))
	}
	return nil
}
