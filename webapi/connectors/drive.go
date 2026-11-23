package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Reading files from Google Drive with a connector's credential, for
// the spool transfer path: this server fetches the bytes and spools
// them as job input, so no third-party token reaches an execute node.
//
// The same shape as contrib/gdrive2_plugin, which does this on the
// execute node for the direct path, and for the same reasons: files are
// addressed by ID because drive.file withholds listing, a 404 means the
// user never picked the file rather than that it is missing, and Drive
// rate limits per user and per project so a cluster of procs is a
// thundering herd by construction.

// ErrNotGranted means Drive will not show us a file.
//
// Under drive.file a file the application was never granted does not
// exist as far as the application is concerned, so this arrives as a
// 404 rather than a 403. Worth its own error because the remedy is
// specific and unobvious: the user has to pick the file, not be given
// permission to it.
var ErrNotGranted = errors.New(
	"Drive will not show this file to this application; under the drive.file scope a file " +
		"becomes readable only when the user picks it")

// driveRetryStatuses are the ones worth trying again. 429 is Drive's
// rate limit, which it applies per user and per project.
var driveRetryStatuses = map[int]bool{
	http.StatusTooManyRequests:     true,
	http.StatusInternalServerError: true,
	http.StatusBadGateway:          true,
	http.StatusServiceUnavailable:  true,
	http.StatusGatewayTimeout:      true,
}

// DriveMaxAttempts bounds the retry of one request.
const DriveMaxAttempts = 5

// DriveFile is what a fetch found.
type DriveFile struct {
	ID   string
	Name string
	Size int64
	// SizeKnown reports that Drive gave a size. It does not for its own
	// document types, which have no bytes to download and must be
	// exported to a chosen format instead.
	SizeKnown bool
	// MIMEType is Drive's type for the file. Google's own document
	// types are named here (SheetMIMEType and friends) and are the ones
	// with no bytes, so this is how a caller tells "a spreadsheet" from
	// "a CSV file" before deciding how to read it.
	MIMEType string
}

// Drive reads files with one user's access token.
type Drive struct {
	// BaseURL is the Drive API root. Empty means the real one; a test
	// points it at a stand-in.
	BaseURL string
	// Client is the HTTP client. Nil means the default.
	Client *http.Client
	// Sleep is how the backoff waits; nil means time.Sleep. A test
	// replaces it rather than waiting.
	Sleep func(time.Duration)
}

func (d *Drive) base() string {
	if d.BaseURL != "" {
		return d.BaseURL
	}
	return "https://www.googleapis.com/drive/v3"
}

// transport is this client's view of the shared Google transport.
func (d *Drive) transport() apiTransport {
	return apiTransport{base: d.base(), client: d.Client, sleep: d.Sleep}
}

// Stat reads a file's name and size without its contents.
//
// Worth a call of its own: the spool path needs the name to put the
// file in the sandbox under, and the size to refuse something too big
// BEFORE spending the bandwidth on it.
func (d *Drive) Stat(ctx context.Context, token, fileID string) (DriveFile, error) {
	q := url.Values{
		"fields":            {"id,name,size,mimeType"},
		"supportsAllDrives": {"true"},
	}
	resp, err := d.transport().get(ctx, token, "/files/"+url.PathEscape(fileID)+"?"+q.Encode())
	if err != nil {
		return DriveFile{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Size     string `json:"size"` // Drive sends this as a string
		MIMEType string `json:"mimeType"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return DriveFile{}, fmt.Errorf("reading the Drive metadata for %s: %w", fileID, err)
	}
	out := DriveFile{ID: body.ID, Name: body.Name, MIMEType: body.MIMEType}
	if body.Size != "" {
		if n, err := strconv.ParseInt(body.Size, 10, 64); err == nil {
			out.Size, out.SizeKnown = n, true
		}
	}
	if out.ID == "" {
		out.ID = fileID
	}
	return out, nil
}

// Open streams a file's contents. The caller closes the reader.
func (d *Drive) Open(ctx context.Context, token, fileID string) (io.ReadCloser, error) {
	q := url.Values{"alt": {"media"}, "supportsAllDrives": {"true"}}
	resp, err := d.transport().get(ctx, token, "/files/"+url.PathEscape(fileID)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
