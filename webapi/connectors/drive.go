package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
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

func (d *Drive) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return http.DefaultClient
}

func (d *Drive) sleep(dur time.Duration) {
	if d.Sleep != nil {
		d.Sleep(dur)
		return
	}
	time.Sleep(dur)
}

// Stat reads a file's name and size without its contents.
//
// Worth a call of its own: the spool path needs the name to put the
// file in the sandbox under, and the size to refuse something too big
// BEFORE spending the bandwidth on it.
func (d *Drive) Stat(ctx context.Context, token, fileID string) (DriveFile, error) {
	q := url.Values{
		"fields":            {"id,name,size"},
		"supportsAllDrives": {"true"},
	}
	resp, err := d.do(ctx, token, "/files/"+url.PathEscape(fileID)+"?"+q.Encode())
	if err != nil {
		return DriveFile{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Size string `json:"size"` // Drive sends this as a string
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return DriveFile{}, fmt.Errorf("reading the Drive metadata for %s: %w", fileID, err)
	}
	out := DriveFile{ID: body.ID, Name: body.Name}
	if body.Size != "" {
		out.Size, _ = strconv.ParseInt(body.Size, 10, 64)
	}
	if out.ID == "" {
		out.ID = fileID
	}
	return out, nil
}

// Open streams a file's contents. The caller closes the reader.
func (d *Drive) Open(ctx context.Context, token, fileID string) (io.ReadCloser, error) {
	q := url.Values{"alt": {"media"}, "supportsAllDrives": {"true"}}
	resp, err := d.do(ctx, token, "/files/"+url.PathEscape(fileID)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// do makes one Drive request, retrying what is worth retrying.
//
// On success the caller owns the body. On failure nothing is left open.
func (d *Drive) do(ctx context.Context, token, path string) (*http.Response, error) {
	var last error
	for attempt := range DriveMaxAttempts {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base()+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := d.client().Do(req)
		if err != nil {
			last = err
			if !d.backoff(ctx, attempt, nil) {
				return nil, err
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		retryable := driveRetryStatuses[resp.StatusCode]
		err = driveError(resp)
		_ = resp.Body.Close()
		last = err
		if !retryable || !d.backoff(ctx, attempt, resp) {
			return nil, err
		}
	}
	return nil, last
}

// backoff waits before the next attempt and reports whether there
// should be one.
//
// Honours Retry-After when Drive sends one; it knows better than the
// curve does. The jitter is not decoration: without it a cluster of
// procs retries in lockstep, which is how a rate limit becomes a
// self-sustaining one.
func (d *Drive) backoff(ctx context.Context, attempt int, resp *http.Response) bool {
	if attempt >= DriveMaxAttempts-1 {
		return false
	}
	wait := time.Duration(1<<attempt)*time.Second +
		time.Duration(rand.N(1000))*time.Millisecond //nolint:gosec // G404: jitter, not a secret
	if resp != nil {
		if v := resp.Header.Get("Retry-After"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil {
				wait = time.Duration(secs) * time.Second
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return false
	}
	d.sleep(wait)
	return ctx.Err() == nil
}

// driveError turns a Drive error body into something a hold reason can
// carry. Drive's messages are usually the actual reason, and surfacing
// them beats "403 Client Error".
func driveError(resp *http.Response) error {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	_ = json.Unmarshal(raw, &body)

	detail := body.Error.Message
	if detail == "" {
		detail = string(raw)
		if len(detail) > 200 {
			detail = detail[:200]
		}
	}

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotGranted, detail)
	}
	reason := ""
	if len(body.Error.Errors) > 0 {
		reason = " (" + body.Error.Errors[0].Reason + ")"
	}
	return fmt.Errorf("Drive returned %d%s: %s", resp.StatusCode, reason, detail)
}
