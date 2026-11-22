package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/jobwatch"
)

// Sealer envelope-encrypts a stored value. *appdb/seal.Sealer satisfies it.
//
// An interface, and a two-method one, so this package does not reach into the
// HTTP server to get at a concrete type -- and so a test can store without a
// key schedule.
type Sealer interface {
	Seal(plaintext []byte) (data, wrappedDEK []byte, err error)
	Open(data, wrappedDEK []byte) ([]byte, error)
}

// ErrNotFound is "no such hook, for this owner". The two cases are
// deliberately one error: telling a caller that an id exists but is somebody
// else's is the one thing an id alone should not reveal.
var ErrNotFound = errors.New("no such webhook")

// ErrNoKEK is returned when a stored value is sealed and this server has no
// key to open it.
//
// Refusing beats guessing, the same choice the upstream refresh tokens and
// the connectors make: the row was written by a server with a KEK and this
// one has none, so the bytes are unreadable, and treating them as plaintext
// would POST ciphertext to whatever host happened to be in them.
var ErrNoKEK = errors.New("the stored webhook destination is sealed but no KEK is configured (HTTP_API_KEK_FILE)")

// DefaultOwnerTTL is how long an owner-scoped hook lives before it has to be
// renewed.
//
// An owner hook has no natural end -- there is always another job -- so
// without one it would outlive the person's interest in it and keep posting
// to a URL nobody reads. Long enough not to be a chore, short enough that an
// abandoned one stops.
const DefaultOwnerTTL = 90 * 24 * time.Hour

// MaxHooksPerOwner bounds how many one person may hold.
const MaxHooksPerOwner = 25

// Store persists hooks and their deliveries.
//
// Every method that reads on a caller's behalf takes an owner and filters on
// it rather than leaving that to the caller. The evaluator reads job ads as
// the daemon, so the owner column is the only thing confining a hook's
// deliveries to the person who registered it, and a forgotten filter here is
// a cross-user leak of a capability URL.
type Store struct {
	db     *sql.DB
	sealer Sealer
	now    func() time.Time
}

// NewStore wraps an already-migrated application database. sealer may be nil,
// in which case destinations are stored in the clear -- which is what a
// deployment with no KEK configured already does with every other secret it
// holds, and is recorded in the row so a later read knows which it is.
func NewStore(db *sql.DB, sealer Sealer) *Store {
	return &Store{db: db, sealer: sealer, now: time.Now}
}

// seal prepares a value for storage.
//
// With no sealer the value is stored in the clear and the DEK is EMPTY but
// not nil: the columns are NOT NULL, and a nil []byte binds as SQL NULL
// rather than as a zero-length blob. An empty DEK is what open reads as "this
// was written in the clear".
func (s *Store) seal(plain []byte) (data, dek []byte, err error) {
	if s.sealer == nil {
		return plain, []byte{}, nil
	}
	return s.sealer.Seal(plain)
}

// open reads a stored value back. An empty dek means the value was written in
// the clear.
func (s *Store) open(data, dek []byte) ([]byte, error) {
	if len(dek) == 0 {
		return data, nil
	}
	if s.sealer == nil {
		return nil, ErrNoKEK
	}
	return s.sealer.Open(data, dek)
}

// Create stores a hook and returns it with its id, secret and deadline
// filled in. url is sealed; the returned Hook does not carry it.
//
// The secret is returned because it is the only time it is available: a
// receiver needs it to verify signatures, and nothing can recover it from the
// row afterwards except the delivery path itself.
func (s *Store) Create(ctx context.Context, h *Hook, destination string) (*Hook, []byte, error) {
	if err := h.Validate(); err != nil {
		return nil, nil, err
	}
	if err := ValidateURL(destination); err != nil {
		return nil, nil, err
	}

	var held int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM webhooks WHERE owner = ?`, h.Owner).Scan(&held); err != nil {
		return nil, nil, fmt.Errorf("counting webhooks: %w", err)
	}
	if held >= MaxHooksPerOwner {
		return nil, nil, fmt.Errorf("you already hold the maximum of %d webhooks; delete one to create another", MaxHooksPerOwner)
	}

	id, err := newID("wh_")
	if err != nil {
		return nil, nil, err
	}
	secret, err := NewSecret()
	if err != nil {
		return nil, nil, err
	}
	urlData, urlDEK, err := s.seal([]byte(destination))
	if err != nil {
		return nil, nil, fmt.Errorf("sealing the destination: %w", err)
	}
	secretData, secretDEK, err := s.seal(secret)
	if err != nil {
		return nil, nil, fmt.Errorf("sealing the signing secret: %w", err)
	}
	events, err := json.Marshal(h.Events)
	if err != nil {
		return nil, nil, err
	}

	now := s.now().UTC()
	var expires *time.Time
	if !h.Scope.Terminable() {
		// An owner hook gets a deadline now. A job or batch hook gets one
		// when its subject ends, which the evaluator decides.
		t := now.Add(DefaultOwnerTTL)
		expires = &t
	}

	var cluster, proc any
	if h.Scope == ScopeJob {
		cluster, proc = h.Job.Cluster, h.Job.Proc
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO webhooks (id, owner, label, scope, job_cluster, job_proc, schedd,
		                      batch_id, batch_name, events_json,
		                      url_data, url_dek, secret_data, secret_dek,
		                      created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, h.Owner, h.Label, string(h.Scope), cluster, proc, h.Job.Schedd,
		h.BatchID, h.BatchName, string(events),
		urlData, urlDEK, secretData, secretDEK,
		now, now, expires,
	); err != nil {
		return nil, nil, fmt.Errorf("storing the webhook: %w", err)
	}

	out := *h
	out.ID = id
	out.CreatedAt = now
	out.UpdatedAt = now
	out.ExpiresAt = expires
	out.HasDestination = true
	out.DestinationHint = HintFor(destination)
	return &out, secret, nil
}

// hookColumns is the select list every scan below shares, so a column added
// to one query cannot be missing from another.
const hookColumns = `id, owner, label, scope, job_cluster, job_proc, schedd,
	batch_id, batch_name, events_json, created_at, updated_at, expires_at,
	disabled_at, disabled_reason, consecutive_failures,
	capped_at, capped_jobs,
	last_delivery_at, last_status, last_error,
	last_test_at, last_test_status, last_test_error,
	url_data, url_dek`

// scanHook reads one row. The destination is read only far enough to say
// whether there is one and what host it names; the value itself is handed
// back only by Destination.
func (s *Store) scanHook(sc interface{ Scan(...any) error }) (*Hook, error) {
	var (
		h              Hook
		scope          string
		cluster, proc  sql.NullInt64
		eventsJSON     string
		expires        sql.NullTime
		disabledAt     sql.NullTime
		cappedAt       sql.NullTime
		lastDelivery   sql.NullTime
		lastStatus     sql.NullInt64
		lastTestAt     sql.NullTime
		lastTestStatus sql.NullInt64
		urlData        []byte
		urlDEK         []byte
	)
	if err := sc.Scan(&h.ID, &h.Owner, &h.Label, &scope, &cluster, &proc, &h.Job.Schedd,
		&h.BatchID, &h.BatchName, &eventsJSON, &h.CreatedAt, &h.UpdatedAt, &expires,
		&disabledAt, &h.DisabledReason, &h.ConsecutiveFailures,
		&cappedAt, &h.CappedJobs,
		&lastDelivery, &lastStatus, &h.LastError,
		&lastTestAt, &lastTestStatus, &h.LastTestError,
		&urlData, &urlDEK); err != nil {
		return nil, err
	}
	h.Scope = Scope(scope)
	if cluster.Valid {
		h.Job.Cluster = cluster.Int64
	}
	if proc.Valid {
		h.Job.Proc = proc.Int64
	}
	if eventsJSON != "" {
		if err := json.Unmarshal([]byte(eventsJSON), &h.Events); err != nil {
			// A row whose event list no longer parses is a load error, not
			// a hook that quietly subscribes to nothing.
			return nil, fmt.Errorf("webhook %s has a malformed event list: %w", h.ID, err)
		}
	}
	setTime := func(dst **time.Time, v sql.NullTime) {
		if v.Valid {
			t := v.Time
			*dst = &t
		}
	}
	setTime(&h.ExpiresAt, expires)
	setTime(&h.DisabledAt, disabledAt)
	setTime(&h.CappedAt, cappedAt)
	setTime(&h.LastDeliveryAt, lastDelivery)
	setTime(&h.LastTestAt, lastTestAt)
	h.LastStatus = int(lastStatus.Int64)
	h.LastTestStatus = int(lastTestStatus.Int64)

	h.HasDestination = len(urlData) > 0
	// The hint needs the value, so it is computed here and the value is
	// dropped. An unopenable row still reports that a destination exists,
	// which is the honest answer: there is one, and this server cannot read
	// it.
	if plain, err := s.open(urlData, urlDEK); err == nil {
		h.DestinationHint = HintFor(string(plain))
	}
	return &h, nil
}

// ForOwner returns one person's hooks, newest first.
func (s *Store) ForOwner(ctx context.Context, owner string) ([]*Hook, error) {
	if owner == "" {
		return nil, fmt.Errorf("an owner is required")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+hookColumns+` FROM webhooks WHERE owner = ? ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Hook
	for rows.Next() {
		h, err := s.scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Get returns one of an owner's hooks.
func (s *Store) Get(ctx context.Context, owner, id string) (*Hook, error) {
	if owner == "" || id == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+hookColumns+` FROM webhooks WHERE id = ? AND owner = ?`, id, owner)
	h, err := s.scanHook(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

// Live returns every hook that should still be evaluated, oldest first.
func (s *Store) Live(ctx context.Context) ([]*Hook, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+hookColumns+`
		   FROM webhooks
		  WHERE disabled_at IS NULL AND (expires_at IS NULL OR expires_at > ?)
		  ORDER BY created_at`, s.now().UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Hook
	for rows.Next() {
		h, err := s.scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Destination unseals a hook's URL and signing secret.
//
// Separate from every other read, and named for what it does, so that the
// value cannot travel in a struct that something later serializes. The only
// callers are the delivery worker and the test button.
func (s *Store) Destination(ctx context.Context, id string) (destination string, secret []byte, err error) {
	var urlData, urlDEK, secretData, secretDEK []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT url_data, url_dek, secret_data, secret_dek FROM webhooks WHERE id = ?`, id).
		Scan(&urlData, &urlDEK, &secretData, &secretDEK)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	plainURL, err := s.open(urlData, urlDEK)
	if err != nil {
		return "", nil, err
	}
	plainSecret, err := s.open(secretData, secretDEK)
	if err != nil {
		return "", nil, err
	}
	return string(plainURL), plainSecret, nil
}

// Update changes what a user may change: the name, the events, and the
// destination. A new destination is a new URL, not a rotation of the secret:
// the receiver is the same receiver.
//
// Any update renews an owner hook's deadline. Editing a hook is the clearest
// possible statement that somebody still wants it.
func (s *Store) Update(ctx context.Context, owner, id string, label *string, events []jobwatch.Event, destination *string) (*Hook, error) {
	h, err := s.Get(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if label != nil {
		h.Label = *label
	}
	if events != nil {
		h.Events = events
	}
	if err := h.Validate(); err != nil {
		return nil, err
	}
	eventsJSON, err := json.Marshal(h.Events)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()

	sets := `label = ?, events_json = ?, updated_at = ?`
	args := []any{h.Label, string(eventsJSON), now}

	if destination != nil {
		if err := ValidateURL(*destination); err != nil {
			return nil, err
		}
		data, dek, err := s.seal([]byte(*destination))
		if err != nil {
			return nil, fmt.Errorf("sealing the destination: %w", err)
		}
		sets += `, url_data = ?, url_dek = ?`
		args = append(args, data, dek)
		// A destination that has been replaced has not failed yet. Leaving
		// the old failure count would disable a working hook on its first
		// hiccup, for deliveries to a URL that is no longer configured.
		sets += `, consecutive_failures = 0, disabled_at = NULL, disabled_reason = ''`
	}
	if !h.Scope.Terminable() {
		t := now.Add(DefaultOwnerTTL)
		sets += `, expires_at = ?`
		args = append(args, t)
	}

	args = append(args, id, owner)
	res, err := s.db.ExecContext(ctx,
		`UPDATE webhooks SET `+sets+` WHERE id = ? AND owner = ?`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, owner, id)
}

// Delete removes a hook and its deliveries. owner may be empty for an
// administrative delete; a user's call always passes one, so somebody else's
// hook is not found rather than refused.
func (s *Store) Delete(ctx context.Context, owner, id string) error {
	query := `DELETE FROM webhooks WHERE id = ?`
	args := []any{id}
	if owner != "" {
		query += ` AND owner = ?`
		args = append(args, owner)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// After the hook, so a failure here leaves deliveries for a hook that
	// is gone -- which the worker skips -- rather than a live hook whose
	// pending deliveries vanished.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM webhook_deliveries WHERE webhook_id = ?`, id); err != nil {
		return fmt.Errorf("the webhook was deleted but its deliveries were not: %w", err)
	}
	return nil
}

// Disable stops a hook and records why, for the UI to show.
func (s *Store) Disable(ctx context.Context, id, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhooks SET disabled_at = ?, disabled_reason = ? WHERE id = ? AND disabled_at IS NULL`,
		s.now().UTC(), reason, id)
	return err
}

// Enable clears a disabled hook, for a user who has fixed the destination.
func (s *Store) Enable(ctx context.Context, owner, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE webhooks SET disabled_at = NULL, disabled_reason = '', consecutive_failures = 0
		  WHERE id = ? AND owner = ?`, id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Expire sets a terminable hook's deadline, which is how a job or batch hook
// ends: its subject can no longer produce an event.
func (s *Store) Expire(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhooks SET expires_at = ? WHERE id = ? AND expires_at IS NULL`, at, id)
	return err
}

// NoteCapped records that a hook's selection exceeded the fan-out ceiling.
func (s *Store) NoteCapped(ctx context.Context, id string, jobs int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhooks SET capped_at = ?, capped_jobs = ? WHERE id = ?`,
		s.now().UTC(), jobs, id)
	return err
}
