package connectors

import (
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// driveHarness connects a user and hands back a Drive client pointed at
// the mock, with the backoff's sleep replaced so a retry test does not
// actually wait.
type driveHarness struct {
	*harness
	drive  *Drive
	token  string
	waited []time.Duration
}

func newDriveHarness(t *testing.T) *driveHarness {
	t.Helper()
	h := newHarness(t)
	g := h.connect(t, "alice")
	dh := &driveHarness{harness: h, token: g.AccessToken}
	dh.drive = &Drive{
		BaseURL: h.google.DriveBaseURL(),
		Sleep:   func(d time.Duration) { dh.waited = append(dh.waited, d) },
	}
	return dh
}

func TestDriveReadsAPickedFile(t *testing.T) {
	h := newDriveHarness(t)
	h.google.PickFile("FILEID", "data.csv", []byte("a,b,c\n"))

	rc, err := h.drive.Open(t.Context(), h.token, "FILEID")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a,b,c\n" {
		t.Errorf("contents = %q", got)
	}
}

func TestDriveStatReadsNameAndSize(t *testing.T) {
	h := newDriveHarness(t)
	h.google.PickFile("FILEID", "data.csv", []byte("a,b,c\n"))

	f, err := h.drive.Stat(t.Context(), h.token, "FILEID")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if f.Name != "data.csv" {
		t.Errorf("Name = %q, want data.csv", f.Name)
	}
	if f.ID != "FILEID" {
		t.Errorf("ID = %q", f.ID)
	}
	// Drive sends size as a string; decoding it is what lets the spool
	// path refuse an oversized file before spending the bandwidth.
	if f.Size != int64(len("a,b,c\n")) {
		t.Errorf("Size = %d, want %d", f.Size, len("a,b,c\n"))
	}
}

// A file the user never picked reads as not-granted, not as missing.
// Under drive.file the remedy is specific and unobvious: pick it, not
// be given permission to it.
func TestDriveUnpickedFileIsNotGranted(t *testing.T) {
	h := newDriveHarness(t)

	_, err := h.drive.Open(t.Context(), h.token, "NEVERPICKED")
	if !errors.Is(err, ErrNotGranted) {
		t.Fatalf("err = %v, want ErrNotGranted", err)
	}
	if got := err.Error(); !containsAll(got, "drive.file", "picks") {
		t.Errorf("error = %q, want it to explain the scope", got)
	}
}

func TestDriveRetriesARateLimit(t *testing.T) {
	h := newDriveHarness(t)
	h.google.PickFile("FILEID", "data.csv", []byte("x"))
	h.google.FailNext(http.StatusTooManyRequests, http.StatusServiceUnavailable)

	rc, err := h.drive.Open(t.Context(), h.token, "FILEID")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = rc.Close()

	if len(h.waited) != 2 {
		t.Errorf("waited %d times, want 2 (one per retried failure)", len(h.waited))
	}
	for i, d := range h.waited {
		if d <= 0 {
			t.Errorf("wait %d was %v", i, d)
		}
	}
}

func TestDriveGivesUpAfterTheAttemptBudget(t *testing.T) {
	h := newDriveHarness(t)
	h.google.PickFile("FILEID", "data.csv", []byte("x"))
	for range DriveMaxAttempts {
		h.google.FailNext(http.StatusServiceUnavailable)
	}

	if _, err := h.drive.Open(t.Context(), h.token, "FILEID"); err == nil {
		t.Fatal("reported success after exhausting the retries")
	}
	if len(h.waited) != DriveMaxAttempts-1 {
		t.Errorf("waited %d times, want %d", len(h.waited), DriveMaxAttempts-1)
	}
}

// A 404 is not retried: picking a file is a thing a person does, and
// retrying cannot make it have happened.
func TestDriveDoesNotRetryNotGranted(t *testing.T) {
	h := newDriveHarness(t)

	if _, err := h.drive.Open(t.Context(), h.token, "NEVERPICKED"); err == nil {
		t.Fatal("expected an error")
	}
	if len(h.waited) != 0 {
		t.Errorf("retried a 404 %d times; picking is not something a retry can achieve", len(h.waited))
	}
}

func TestDriveRefusesAnUnknownToken(t *testing.T) {
	h := newDriveHarness(t)
	h.google.PickFile("FILEID", "data.csv", []byte("x"))

	_, err := h.drive.Open(t.Context(), h.token+"-tampered", "FILEID")
	if err == nil {
		t.Fatal("a tampered token read a file")
	}
	if errors.Is(err, ErrNotGranted) {
		t.Errorf("a bad token was reported as an ungranted file: %v", err)
	}
}

// A failed request leaves no body open. A leaked connection per retry
// is the kind of thing that only shows up under load.
func TestDriveClosesBodiesOnFailure(t *testing.T) {
	h := newDriveHarness(t)
	h.google.FailNext(http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	h.google.PickFile("FILEID", "f", []byte("x"))

	rc, err := h.drive.Open(t.Context(), h.token, "FILEID")
	if err != nil {
		t.Fatal(err)
	}
	// If the retried responses had been left open the transport would
	// not reuse the connection; the observable part here is simply that
	// the successful one is readable and closable exactly once.
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Retry-After is obeyed. A client that ignores it backs off on its own
// schedule rather than the provider's, which is how a rate limit gets
// extended rather than ridden out.
func TestDriveObeysRetryAfter(t *testing.T) {
	h := newDriveHarness(t)
	h.google.PickFile("FILEID", "f", []byte("x"))
	h.google.SetHeader("Retry-After", "7")
	h.google.FailNext(http.StatusTooManyRequests)

	rc, err := h.drive.Open(t.Context(), h.token, "FILEID")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = rc.Close()

	if len(h.waited) != 1 {
		t.Fatalf("waited %d times, want 1", len(h.waited))
	}
	if h.waited[0] != 7*time.Second {
		t.Errorf("waited %v, want the 7s the provider asked for", h.waited[0])
	}
}
