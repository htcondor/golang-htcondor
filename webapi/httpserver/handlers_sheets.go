package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// Reading a connected account's spreadsheet, for the submit page's
// "parameters from a spreadsheet" table source.
//
//	GET /api/v1/connect/{ns}:{name}/sheets/{fileID}            -> the tabs
//	GET /api/v1/connect/{ns}:{name}/sheets/{fileID}?tab=runs    -> that tab's rows
//
// Here rather than in the browser, for the same reason the spool path
// fetches on this side: the connector's access token is minted and held
// by this server and never reaches a page. The browser knows a file id,
// which the Picker gave it, and nothing else.
//
// Reached through handleConnectItem, so the session, the connector's
// existence and the caller's group membership are all already checked
// by the time this runs.

// sheetMaxRows bounds one read.
//
// A spreadsheet can be a million rows; a submit page's table cannot,
// and neither can a person reviewing it before pressing submit. The
// read stops here and says it was cut, rather than returning a prefix
// that looks like the whole table -- a short table submits fewer jobs
// than the spreadsheet lists, which otherwise looks like success.
const sheetMaxRows = 5000

// connectorSheet answers both reads.
func (h *Handler) connectorSheet(
	w http.ResponseWriter,
	r *http.Request,
	session *SessionData,
	def connectors.Definition,
	fileID string,
) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if fileID == "" {
		h.writeError(w, http.StatusBadRequest, "No spreadsheet was named")
		return
	}

	ctx := r.Context()
	token, err := h.connectorAccessToken(ctx, r, def, session.Username)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	// Drive first, for the name and the type. Asking the Sheets API for
	// the tabs of a Doc gets an error about the file, not about the
	// mistake, and "that is a document, not a spreadsheet" is the thing
	// worth saying.
	drive := &connectors.Drive{BaseURL: h.connectorDriveBaseURL}
	meta, err := drive.Stat(ctx, token, fileID)
	if err != nil {
		h.writeError(w, sheetErrorStatus(err), err.Error())
		return
	}
	if meta.MIMEType != "" && meta.MIMEType != connectors.SheetMIMEType {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"%q is not a Google Sheet (%s). Pick a spreadsheet, or upload the file as a CSV.",
			meta.Name, meta.MIMEType))
		return
	}

	sheets := &connectors.Sheets{BaseURL: h.connectorSheetsBaseURL}

	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tabs, err := sheets.Tabs(ctx, token, fileID)
		if err != nil {
			h.writeError(w, sheetErrorStatus(err), err.Error())
			return
		}
		out := make([]map[string]any, 0, len(tabs))
		for _, t := range tabs {
			out = append(out, map[string]any{"id": t.ID, "title": t.Title})
		}
		h.writeJSON(w, http.StatusOK, map[string]any{
			"fileId": meta.ID,
			"name":   meta.Name,
			"tabs":   out,
		})
		return
	}

	maxRows := sheetMaxRows
	if v := r.URL.Query().Get("maxRows"); v != "" {
		n, cerr := strconv.Atoi(v)
		if cerr != nil || n < 1 {
			h.writeError(w, http.StatusBadRequest,
				"maxRows must be a positive whole number")
			return
		}
		// A caller may ask for less, never for more: the ceiling is this
		// server's, not theirs.
		maxRows = min(n, sheetMaxRows)
	}

	vals, err := sheets.Values(ctx, token, fileID, tab, maxRows)
	if err != nil {
		h.writeError(w, sheetErrorStatus(err), err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"fileId":    meta.ID,
		"name":      meta.Name,
		"tab":       tab,
		"rows":      vals.Rows,
		"truncated": vals.Truncated,
		"maxRows":   maxRows,
	})
}

// sheetErrorStatus maps a provider error to a status the browser can
// act on.
//
// ErrNotGranted is the one worth separating: under drive.file it
// arrives as a 404, but it means the user has not picked this file, and
// the remedy is to pick it rather than to go looking for it. A 404 to
// the browser would read as "the spreadsheet is gone".
func sheetErrorStatus(err error) int {
	if errors.Is(err, connectors.ErrNotGranted) {
		return http.StatusForbidden
	}
	return http.StatusBadGateway
}
