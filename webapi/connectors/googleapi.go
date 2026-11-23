package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// One HTTP transport for Google's APIs, shared by the Drive and Sheets
// clients in this package.
//
// Extracted rather than copied. The two clients differ only in their
// base URL and their paths; everything that makes a request to Google
// correct -- the retry set, the exponential backoff, honouring
// Retry-After, the jitter, and reading a 404 as "never granted" rather
// than "missing" -- is the same for both, and a second copy of it is a
// second place for those to drift apart.

// apiTransport makes authenticated requests to one Google API root.
type apiTransport struct {
	// base is the API root, with no trailing slash.
	base string
	// client is the HTTP client. Nil means the default.
	client *http.Client
	// sleep is how the backoff waits; nil means time.Sleep. A test
	// replaces it rather than waiting.
	sleep func(time.Duration)
	// attempts bounds one request. Zero means DriveMaxAttempts.
	attempts int
}

func (t apiTransport) httpClient() *http.Client {
	if t.client != nil {
		return t.client
	}
	return http.DefaultClient
}

func (t apiTransport) wait(dur time.Duration) {
	if t.sleep != nil {
		t.sleep(dur)
		return
	}
	time.Sleep(dur)
}

func (t apiTransport) maxAttempts() int {
	if t.attempts > 0 {
		return t.attempts
	}
	return DriveMaxAttempts
}

// get makes one GET, retrying what is worth retrying.
//
// On success the caller owns the body. On failure nothing is left open.
func (t apiTransport) get(ctx context.Context, token, path string) (*http.Response, error) {
	return t.send(ctx, http.MethodGet, token, path, "", nil)
}

// send makes one request, retrying what is worth retrying.
//
// The body is bytes rather than a reader because a retry needs to send
// it again, and a reader that has already been consumed cannot. Callers
// with a large body do not belong here.
func (t apiTransport) send(
	ctx context.Context,
	method, token, path, contentType string,
	body []byte,
) (*http.Response, error) {
	var last error
	for attempt := range t.maxAttempts() {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, t.base+path, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := t.httpClient().Do(req)
		if err != nil {
			last = err
			if !t.backoff(ctx, attempt, nil) {
				return nil, err
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		retryable := driveRetryStatuses[resp.StatusCode]
		err = googleError(resp)
		_ = resp.Body.Close()
		last = err
		if !retryable || !t.backoff(ctx, attempt, resp) {
			return nil, err
		}
	}
	return nil, last
}

// backoff waits before the next attempt and reports whether there
// should be one.
//
// Honours Retry-After when Google sends one; it knows better than the
// curve does. The jitter is not decoration: without it a cluster of
// procs retries in lockstep, which is how a rate limit becomes a
// self-sustaining one.
func (t apiTransport) backoff(ctx context.Context, attempt int, resp *http.Response) bool {
	if attempt >= t.maxAttempts()-1 {
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
	t.wait(wait)
	return ctx.Err() == nil
}

// googleError turns an API error body into something a hold reason or a
// browser message can carry. Google's messages are usually the actual
// reason, and surfacing them beats "403 Client Error".
func googleError(resp *http.Response) error {
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
	return fmt.Errorf("the Google API returned %d%s: %s", resp.StatusCode, reason, detail)
}
