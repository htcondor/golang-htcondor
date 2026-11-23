package connectors

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Reading a job's declared output file and fitting it to a
// spreadsheet's header row.
//
// Two halves, both pure, because this is the part where a job's results
// become somebody's data and getting it wrong is silent: a column read
// into the wrong place looks exactly like a column read into the right
// one.

// ResultRow is one record a job produced, keyed by the name the job
// gave each field.
type ResultRow map[string]string

// ParseJobResult reads a job's output file.
//
// JSON or CSV, which between them is what a script can emit without a
// library. Three JSON shapes are accepted because all three are what
// people actually write: one object (a job that produced one record),
// an array of objects (a job that produced several), and an array of
// arrays with the first row as names (a job that wrote a table).
//
// Bounded: a job can write a gigabyte, and the point of failure should
// be here rather than in the memory of the server appending it.
func ParseJobResult(r io.Reader, maxBytes int64) ([]ResultRow, error) {
	lr := &io.LimitedReader{R: r, N: maxBytes + 1}
	raw, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("reading the job's output: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("the job's output is larger than the %d bytes this "+
			"access point will read back into a spreadsheet", maxBytes)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("the job's output file is empty")
	}

	// The first non-blank character decides. A JSON document starts
	// with { or [; anything else is read as CSV, including a bare
	// header row, which is what a sniffing heuristic would get wrong.
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return parseJSONResult(trimmed)
	}
	return parseCSVResult(trimmed)
}

func parseJSONResult(text string) ([]ResultRow, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	// Numbers as json.Number, so 1 stays "1" rather than becoming "1"
	// via a float that renders 1e21 as 1e+21.
	dec.UseNumber()

	var any1 any
	if err := dec.Decode(&any1); err != nil {
		return nil, fmt.Errorf("the job's output is not readable as JSON: %w", err)
	}

	switch v := any1.(type) {
	case map[string]any:
		return []ResultRow{objectToRow(v)}, nil
	case []any:
		if len(v) == 0 {
			return nil, fmt.Errorf("the job's output is an empty JSON array, " +
				"so there is nothing to add")
		}
		switch v[0].(type) {
		case map[string]any:
			out := make([]ResultRow, 0, len(v))
			for i, item := range v {
				obj, ok := item.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("element %d of the job's output is not an "+
						"object, though the first one is", i)
				}
				out = append(out, objectToRow(obj))
			}
			return out, nil
		case []any:
			return tableToRows(v)
		default:
			return nil, fmt.Errorf("the job's output is a JSON array of plain values; " +
				"it needs objects, or a first row of column names")
		}
	default:
		// Not reachable through ParseJobResult, which sends anything
		// not starting with { or [ to the CSV reader. Here so the type
		// switch has an answer rather than a fallthrough.
		return nil, fmt.Errorf("the job's output is JSON, but not a record " +
			"or a list of them")
	}
}

// tableToRows reads an array of arrays whose first row names the
// columns -- a job that wrote a table rather than records.
func tableToRows(rows []any) ([]ResultRow, error) {
	header, ok := rows[0].([]any)
	if !ok {
		return nil, fmt.Errorf("the job's output has a first row that is not a list of names")
	}
	names := make([]string, 0, len(header))
	for i, h := range header {
		name := strings.TrimSpace(scalarToString(h))
		if name == "" {
			return nil, fmt.Errorf("column %d of the job's output has no name", i+1)
		}
		names = append(names, name)
	}
	out := make([]ResultRow, 0, len(rows)-1)
	for _, raw := range rows[1:] {
		cells, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("the job's output mixes rows and objects")
		}
		row := ResultRow{}
		for i, name := range names {
			if i < len(cells) {
				row[name] = scalarToString(cells[i])
			} else {
				row[name] = ""
			}
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the job's output has column names but no rows under them")
	}
	return out, nil
}

func parseCSVResult(text string) ([]ResultRow, error) {
	cr := csv.NewReader(strings.NewReader(text))
	cr.FieldsPerRecord = -1
	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("the job's output is not readable as CSV: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("the job's output file is empty")
	}
	names := make([]string, 0, len(records[0]))
	for i, h := range records[0] {
		name := strings.TrimSpace(h)
		if name == "" {
			return nil, fmt.Errorf("column %d of the job's output has no name", i+1)
		}
		names = append(names, name)
	}
	out := make([]ResultRow, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := ResultRow{}
		for i, name := range names {
			if i < len(rec) {
				row[name] = rec[i]
			} else {
				row[name] = ""
			}
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the job's output has column names but no rows under them")
	}
	return out, nil
}

func objectToRow(obj map[string]any) ResultRow {
	row := ResultRow{}
	for k, v := range obj {
		row[k] = scalarToString(v)
	}
	return row
}

// scalarToString renders one JSON value as a cell.
//
// A nested object or array has no cell form, so it is rendered back as
// JSON rather than as Go's %v -- "map[a:1]" in somebody's spreadsheet
// is worse than {"a":1}, which at least says what it was.
func scalarToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
		return fmt.Sprint(t)
	}
}

// FitToHeaders turns a job's records into rows for a spreadsheet whose
// header row is headers.
//
// The sheet's headers are the contract. A column the job did not
// produce is left blank; a field the sheet has no column for is left
// out. Neither changes the sheet's shape, which is the point: the
// person who set the spreadsheet up decided what it holds, and jobs
// running later do not get to re-decide it.
//
// Matching is case-insensitive on the header name, because a header is
// a label somebody typed and "Sample" and "sample" are the same column
// to everyone except a string comparison.
//
// The names left out come back so the caller can say so. Dropping a
// field silently is the failure this is most likely to have: the append
// succeeds, the row looks right, and a column of results is missing.
func FitToHeaders(headers []string, rows []ResultRow) (out [][]string, unused []string) {
	index := make(map[string]int, len(headers))
	for i, h := range headers {
		index[strings.ToLower(strings.TrimSpace(h))] = i
	}

	seenUnused := map[string]bool{}
	out = make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, len(headers))
		for k, v := range row {
			i, ok := index[strings.ToLower(strings.TrimSpace(k))]
			if !ok {
				if !seenUnused[k] {
					seenUnused[k] = true
					unused = append(unused, k)
				}
				continue
			}
			cells[i] = v
		}
		out = append(out, cells)
	}
	// Map iteration order is not stable, and a warning that names the
	// same fields in a different order every time reads as a different
	// warning.
	sort.Strings(unused)
	return out, unused
}
