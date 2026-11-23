package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Reading a Google Sheet with a connector's credential.
//
// A separate client from Drive because a Sheet is not a file with bytes
// in it. Drive can export one, but only as the FIRST tab's CSV, which
// is the wrong answer for a workbook with several tabs and a silent one
// -- the export succeeds and the rows are from a tab nobody chose. The
// Sheets API names the tabs and returns cells as JSON, so the tab is
// the caller's choice and there is no CSV quoting between a cell and
// the value it held.
//
// Scope: the connector's drive.file is enough. It grants per-file
// access to what the user picked, and the Sheets API honours it for
// those files, so reading a picked Sheet needs no wider grant than
// reading a picked CSV already does.

// SheetMIMEType is what Drive calls a Google Sheet. A file that is not
// one has no tabs, and saying so beats relaying an API error about a
// range.
const SheetMIMEType = "application/vnd.google-apps.spreadsheet"

// Tab is one worksheet inside a spreadsheet.
type Tab struct {
	// ID is Google's sheetId. It survives a rename, which the title
	// does not, so it is the stable way to refer to a tab.
	ID int64
	// Title is what the tab is called, and what A1 notation needs.
	Title string
	// Rows and Cols are the grid's extent, not how much of it is
	// filled. A new tab reports 1000 x 26 with nothing in it.
	Rows int64
	Cols int64
}

// Sheets reads spreadsheets.
type Sheets struct {
	// BaseURL is the Sheets API root. Empty means the real one; a test
	// points it at a stand-in.
	BaseURL string
	// Client is the HTTP client. Nil means the default.
	Client *http.Client
	// Sleep is how the backoff waits; nil means time.Sleep.
	Sleep func(time.Duration)
}

func (s *Sheets) base() string {
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return "https://sheets.googleapis.com/v4"
}

func (s *Sheets) transport() apiTransport {
	return apiTransport{base: s.base(), client: s.Client, sleep: s.Sleep}
}

// Tabs lists a spreadsheet's worksheets, in the order they appear.
//
// Asked for separately from the values because the tab is a choice: a
// workbook with one tab needs no question, and one with several must
// not have the first picked silently on the user's behalf.
func (s *Sheets) Tabs(ctx context.Context, token, fileID string) ([]Tab, error) {
	q := url.Values{"fields": {"sheets.properties(sheetId,title,gridProperties)"}}
	resp, err := s.transport().get(ctx,
		token, "/spreadsheets/"+url.PathEscape(fileID)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Sheets []struct {
			Properties struct {
				SheetID        int64  `json:"sheetId"`
				Title          string `json:"title"`
				GridProperties struct {
					RowCount    int64 `json:"rowCount"`
					ColumnCount int64 `json:"columnCount"`
				} `json:"gridProperties"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("reading the tabs of spreadsheet %s: %w", fileID, err)
	}

	out := make([]Tab, 0, len(body.Sheets))
	for _, sh := range body.Sheets {
		out = append(out, Tab{
			ID:    sh.Properties.SheetID,
			Title: sh.Properties.Title,
			Rows:  sh.Properties.GridProperties.RowCount,
			Cols:  sh.Properties.GridProperties.ColumnCount,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("spreadsheet %s reports no tabs, which a spreadsheet always has; "+
			"it may not be a Google Sheet", fileID)
	}
	return out, nil
}

// SheetValues is what one tab held.
type SheetValues struct {
	// Rows are the cells, row-major, padded to a rectangle. Google
	// omits trailing empty cells, so an unpadded response has rows of
	// different lengths and a caller indexing by column reads the wrong
	// one.
	Rows [][]string
	// Truncated reports that the tab had more rows than the limit
	// allowed, so Rows is a prefix. Worth saying out loud: a silently
	// short table submits fewer jobs than the spreadsheet lists.
	Truncated bool
}

// Values reads a tab's cells, up to maxRows rows.
//
// The values are the FORMATTED ones, which is what the person looking
// at the spreadsheet sees. A date they see as 2026-01-31 arrives as
// that rather than as Google's serial number, and for a parameter table
// the displayed text is what they mean by the cell.
func (s *Sheets) Values(
	ctx context.Context,
	token, fileID, tab string,
	maxRows int,
) (SheetValues, error) {
	if maxRows <= 0 {
		return SheetValues{}, fmt.Errorf("maxRows must be positive, got %d", maxRows)
	}
	// One past the limit, so a full page is distinguishable from a tab
	// that happens to end exactly at it.
	rng := a1Range(tab, maxRows+1)
	resp, err := s.transport().get(ctx, token,
		"/spreadsheets/"+url.PathEscape(fileID)+"/values/"+url.PathEscape(rng))
	if err != nil {
		return SheetValues{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Values [][]any `json:"values"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return SheetValues{}, fmt.Errorf("reading %s of spreadsheet %s: %w", rng, fileID, err)
	}

	out := SheetValues{}
	if len(body.Values) > maxRows {
		body.Values = body.Values[:maxRows]
		out.Truncated = true
	}

	width := 0
	rows := make([][]string, 0, len(body.Values))
	for _, raw := range body.Values {
		row := make([]string, len(raw))
		for i, cell := range raw {
			row[i] = cellString(cell)
		}
		if len(row) > width {
			width = len(row)
		}
		rows = append(rows, row)
	}
	// Pad to a rectangle. Google omits trailing empties, so without
	// this a short row shifts every column after it for that job.
	for i := range rows {
		for len(rows[i]) < width {
			rows[i] = append(rows[i], "")
		}
	}
	out.Rows = rows
	return out, nil
}

// cellString renders one cell as the text it displays.
//
// The API sends JSON, so a formatted number still arrives as a JSON
// number when it looks like one. %v on a float64 would turn 1 into 1
// but 1e21 into 1e+21 and 0.1+0.2 into something nobody typed, so
// numbers go through strconv with -1 precision: the shortest string
// that reads back as the same float.
func cellString(cell any) string {
	switch v := cell.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// a1Range is `'Tab'!1:n` -- rows 1 to n, every column.
//
// The whole-row form rather than a column bound, because the tab's
// width is the spreadsheet's business and guessing a bound like Z would
// silently drop the 27th column. A1 quotes a title with single quotes
// and escapes an embedded one by doubling it, which titles do contain.
func a1Range(tab string, rows int) string {
	quoted := "'" + strings.ReplaceAll(tab, "'", "''") + "'"
	return quoted + "!1:" + strconv.Itoa(rows)
}

// Appending rows to a spreadsheet, for a job's declared output.
//
// values.append is one server-side call: Google finds the end of the
// table and inserts there, so two jobs finishing at the same moment do
// not have to agree about which row is next. That is the whole reason
// the result goes in this way rather than as a read-then-write of a
// range, which would need a lock nobody can hold across a job.

// AppendRows adds rows to the end of a tab's table.
//
// INSERT_ROWS rather than OVERWRITE: a sheet with anything below the
// table -- a total, a note, a second block -- would otherwise have it
// written over. Inserting is the choice that cannot destroy what it
// did not put there.
//
// RAW rather than USER_ENTERED: a result cell is data, not something
// the person is typing. USER_ENTERED would read "=cmd" as a formula
// and "1/2" as a date, which is a job's output silently becoming
// something else.
func (s *Sheets) AppendRows(
	ctx context.Context,
	token, fileID, tab string,
	rows [][]string,
) error {
	if len(rows) == 0 {
		return nil
	}
	body, err := json.Marshal(map[string]any{
		"range":          a1Range(tab, 1),
		"majorDimension": "ROWS",
		"values":         rows,
	})
	if err != nil {
		return fmt.Errorf("encoding %d rows for %s: %w", len(rows), fileID, err)
	}

	q := url.Values{
		"valueInputOption": {"RAW"},
		"insertDataOption": {"INSERT_ROWS"},
		// The reply would otherwise carry the whole updated range back,
		// which is of no use here and is the largest part of it.
		"includeValuesInResponse": {"false"},
	}
	path := "/spreadsheets/" + url.PathEscape(fileID) +
		"/values/" + url.PathEscape(a1Range(tab, 1)) + ":append?" + q.Encode()

	resp, err := s.transport().send(ctx, http.MethodPost, token, path,
		"application/json", body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
