// Copyright 2026 Morgridge Institute for Research
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sftpjobs

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A CSV dropped into a template becomes one job per row.
//
// The header IS the contract: it names the submit variables the
// template's own `queue` statement reads, so a template referring to
// $(sample) and $(reads) is driven by a CSV headed `sample,reads`. No
// separate mapping file, and nothing to keep in step with the submit
// description.
//
// Everything below refuses rather than repairs. A value that cannot be
// carried through HTCondor's itemdata unchanged is a mistake worth
// reporting, and quietly altering it means somebody's job runs on a
// different input than their spreadsheet says. Three of these rules
// exist because the submit parser would otherwise accept the file and
// do something else entirely:
//
//   - A row whose first non-blank character is ")" ENDS the bracketed
//     itemdata block, and everything after it is read as submit-file
//     text. Measured against the real parser: a clean three-row CSV
//     queues three jobs, while the same CSV with a ")" row either fails
//     with a parse error naming lines nobody wrote, or queues a
//     DIFFERENT set of jobs than the spreadsheet describes. Which of the
//     two depends on the value, so neither is something to let through.
//
//     It is the position that matters, not the character: a ")" anywhere
//     but the start of a row is ordinary item text, which is why this is
//     a row rule beside the "#" rule rather than a search of every
//     value. Pinned in fuzz/submit/differential_test.go against the real
//     SubmitHash.
//   - A row starting with "#", or blank after trimming, is dropped as a
//     comment -- so a job silently disappears rather than failing.
//   - A value containing "$" is macro-expanded when the submit file
//     uses it.
//
// The rest follow from itemdata being line-oriented and unquoted: a
// newline splits one row into two, and a separator inside a value adds
// a column.

// CSVLimits bounds one upload. A zero field takes its default.
type CSVLimits struct {
	// MaxRows is the most jobs one CSV may create.
	MaxRows int
	// MaxBytes is the largest CSV accepted.
	MaxBytes int64
	// MaxColumns bounds the header.
	MaxColumns int
	// MaxValueLen bounds one cell.
	MaxValueLen int
}

// Defaults for CSVLimits.
const (
	DefaultMaxRows     = 10000
	DefaultMaxBytes    = 8 << 20
	DefaultMaxColumns  = 64
	DefaultMaxValueLen = 1024
)

func (l CSVLimits) withDefaults() CSVLimits {
	if l.MaxRows <= 0 {
		l.MaxRows = DefaultMaxRows
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultMaxBytes
	}
	if l.MaxColumns <= 0 {
		l.MaxColumns = DefaultMaxColumns
	}
	if l.MaxValueLen <= 0 {
		l.MaxValueLen = DefaultMaxValueLen
	}
	return l
}

// Submission is a CSV ready to become one cluster of jobs.
type Submission struct {
	// Vars are the header's names, in order: the submit variables the
	// template's queue statement reads.
	Vars []string
	// Rows are itemdata lines in wire form, one per job, columns joined
	// by the separator HTCondor splits them on.
	Rows []string
}

// itemdataSeparator joins a row's columns.
//
// Comma, because HTCondor splits an itemdata row on commas when it finds
// any and on whitespace otherwise -- so a value with a space in it works
// only if commas are the separator. A value containing a comma is
// refused: unquoted itemdata cannot express one, which is a limit of the
// mechanism rather than of this check.
const itemdataSeparator = ","

// ParseCSV reads a dropped CSV.
//
// The error is written back to the person who uploaded the file, so it
// names the row and the column rather than only the rule.
func ParseCSV(r io.Reader, limits CSVLimits) (*Submission, error) {
	limits = limits.withDefaults()

	// Bounded before parsing, not after: a reader that would produce a
	// gigabyte of rows should not be parsed to find that out. One byte
	// past the limit is enough to tell.
	lr := &io.LimitedReader{R: r, N: limits.MaxBytes + 1}
	cr := csv.NewReader(lr)
	// Rows are checked against the header below, and this reader's own
	// arity error would name no column.
	cr.FieldsPerRecord = -1
	// A comment character would make a legitimate value starting with
	// "#" vanish at parse time instead of being refused.
	cr.Comment = 0

	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("this does not read as CSV: %w", err)
	}
	if lr.N == 0 {
		return nil, fmt.Errorf("the file is larger than the %d byte limit", limits.MaxBytes)
	}
	if len(records) == 0 {
		return nil, errors.New("the file is empty; the first row must name the submit variables")
	}

	vars, err := csvHeader(records[0], limits)
	if err != nil {
		return nil, err
	}
	body := records[1:]
	if len(body) == 0 {
		return nil, errors.New("the file has a header but no rows, so it would create no jobs")
	}
	if len(body) > limits.MaxRows {
		return nil, fmt.Errorf("%d rows is more than the %d this template accepts", len(body), limits.MaxRows)
	}

	rows := make([]string, 0, len(body))
	for i, rec := range body {
		line := i + 2 // 1-based, and the header is line 1
		if len(rec) != len(vars) {
			// Not padded or truncated to fit: a short row means a job
			// would run with an empty input it was never given.
			return nil, fmt.Errorf("row %d has %d values but the header names %d",
				line, len(rec), len(vars))
		}
		for col, v := range rec {
			if err := checkCSVValue(v, limits); err != nil {
				return nil, fmt.Errorf("row %d, column %q: %w", line, vars[col], err)
			}
		}
		row := strings.Join(rec, itemdataSeparator)
		if err := checkRow(row); err != nil {
			return nil, fmt.Errorf("row %d: %w", line, err)
		}
		rows = append(rows, row)
	}
	return &Submission{Vars: vars, Rows: rows}, nil
}

// csvHeader validates the names the first row gives.
func csvHeader(rec []string, limits CSVLimits) ([]string, error) {
	if len(rec) == 0 {
		return nil, errors.New("the header row is empty")
	}
	if len(rec) > limits.MaxColumns {
		return nil, fmt.Errorf("%d columns is more than the %d allowed", len(rec), limits.MaxColumns)
	}
	seen := map[string]int{}
	out := make([]string, 0, len(rec))
	for i, raw := range rec {
		// Surrounding space in a header is a spreadsheet artefact, not a
		// name; the name itself is then checked exactly.
		name := strings.TrimSpace(raw)
		if err := checkVarName(name); err != nil {
			return nil, fmt.Errorf("column %d (%q): %w", i+1, raw, err)
		}
		// Submit macro names are case-insensitive, so two columns
		// differing only in case would be one variable and the second
		// would silently win.
		key := strings.ToLower(name)
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("columns %d and %d are both named %q, which is one submit variable",
				prev+1, i+1, name)
		}
		seen[key] = i
		out = append(out, name)
	}
	return out, nil
}

// checkVarName accepts what a submit variable may be called.
func checkVarName(name string) error {
	if name == "" {
		return errors.New("a column needs a name, which becomes a submit variable")
	}
	if len(name) > 64 {
		return errors.New("the name is longer than 64 characters")
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return fmt.Errorf("%q is not usable as a submit variable name; use letters, digits and underscores, starting with a letter", name)
		}
	}
	return nil
}

// checkCSVValue refuses a value HTCondor's itemdata cannot carry
// unchanged. See the file comment for why each one matters.
func checkCSVValue(v string, limits CSVLimits) error {
	if len(v) > limits.MaxValueLen {
		return fmt.Errorf("the value is longer than the %d character limit", limits.MaxValueLen)
	}
	if strings.ContainsAny(v, "\n\r") {
		return errors.New("the value contains a line break, which would split the row in two")
	}
	if strings.ContainsRune(v, 0) {
		return errors.New("the value contains a NUL byte")
	}
	if strings.Contains(v, "$") {
		return errors.New(`the value contains "$", which the submit file would expand as a macro rather than use as text`)
	}
	if strings.Contains(v, itemdataSeparator) {
		return fmt.Errorf("the value contains %q, which separates one column from the next", itemdataSeparator)
	}
	return nil
}

// checkRow refuses a row that the submit parser would drop rather than
// use: one that is blank after trimming, or that reads as a comment.
//
// Applied to the JOINED row, because that is the text the parser sees --
// a single-column CSV whose value is "#x" produces the row "#x".
func checkRow(row string) error {
	trimmed := strings.TrimSpace(row)
	if trimmed == "" {
		return errors.New("the row is empty, and an empty row is dropped rather than queued")
	}
	if strings.HasPrefix(trimmed, "#") {
		return errors.New(`the row starts with "#", which is dropped as a comment rather than queued`)
	}
	if strings.HasPrefix(trimmed, ")") {
		return errors.New(`the row starts with ")", which would end the job list early: ` +
			`the rest of the file is then read as submit commands, and the jobs that run are not the ones listed here`)
	}
	return nil
}
