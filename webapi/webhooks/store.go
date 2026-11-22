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

// ErrNotFound is "no such endpoint or subscription, for this owner". The two cases are
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

// DefaultOwnerTTL is how long an owner-scoped subscription lives before it
// has to be renewed.
//
// An owner subscription has no natural end -- there is always another job -- so
// without one it would outlive the person's interest in it and keep posting
// to a URL nobody reads. Long enough not to be a chore, short enough that an
// abandoned one stops.
const DefaultOwnerTTL = 90 * 24 * time.Hour

// TerminableBackstopTTL is the outer bound on a job or batch subscription.
//
// Such a subscription normally ends when its subject does, which the evaluator
// decides. This is for the ones whose subject never shows up -- a hook on a
// cluster that was never submitted, or on a batch name nobody used -- which
// would otherwise be swept through on every pass for ever.
const TerminableBackstopTTL = 30 * 24 * time.Hour

// Per-owner bounds.
//
// Two numbers rather than one, because the split made them different
// questions. A destination costs a stored secret and a URL somebody has to
// paste; a subscription costs a row and a share of an evaluator pass. People
// have a handful of receivers and may want to watch rather more things.
const (
	// MaxEndpointsPerOwner bounds how many destinations one person holds.
	MaxEndpointsPerOwner = 10
	// MaxSubscriptionsPerOwner bounds how many things they watch.
	MaxSubscriptionsPerOwner = 25
)

// Store persists destinations, subscriptions and deliveries.
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

// CreateEndpoint stores a destination and returns it with its id and secret
// filled in. destination is sealed; the returned Endpoint does not carry it.
//
// The secret is returned because this is the only time it is available: a
// receiver needs it to verify signatures, and nothing can recover it from the
// row afterwards except the delivery path itself. It is asked for ONCE per
// receiver -- that is what this table is for.
func (s *Store) CreateEndpoint(ctx context.Context, ep *Endpoint, destination string) (*Endpoint, []byte, error) {
	if ep.Payload == "" {
		ep.Payload = PayloadSummary
	}
	if err := ep.Validate(); err != nil {
		return nil, nil, err
	}
	if err := ValidateURL(destination); err != nil {
		return nil, nil, err
	}

	var held int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM webhook_endpoints WHERE owner = ?`, ep.Owner).Scan(&held); err != nil {
		return nil, nil, fmt.Errorf("counting destinations: %w", err)
	}
	if held >= MaxEndpointsPerOwner {
		return nil, nil, fmt.Errorf("you already hold the maximum of %d destinations; delete one to add another", MaxEndpointsPerOwner)
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

	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_endpoints (id, owner, label, payload_mode,
		                               url_data, url_dek, secret_data, secret_dek,
		                               created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, ep.Owner, ep.Label, string(ep.Payload),
		urlData, urlDEK, secretData, secretDEK, now, now,
	); err != nil {
		return nil, nil, fmt.Errorf("storing the destination: %w", err)
	}

	out := *ep
	out.ID = id
	out.CreatedAt = now
	out.UpdatedAt = now
	out.HasDestination = true
	out.DestinationHint = HintFor(destination)
	return &out, secret, nil
}

// endpointColumns is the select list every endpoint scan shares, so a column
// added to one query cannot be missing from another.
const endpointColumns = `id, owner, label, payload_mode, created_at, updated_at,
	consecutive_failures, backoff_until,
	last_delivery_at, last_status, last_error,
	last_test_at, last_test_status, last_test_error,
	backlog_at, backlog_depth, prev_secret_expires_at, url_data, url_dek`

// scanEndpoint reads one row. The destination is read only far enough to say
// whether there is one and what host it names; the value itself is handed
// back only by Destination.
func (s *Store) scanEndpoint(sc interface{ Scan(...any) error }) (*Endpoint, error) {
	var (
		ep             Endpoint
		mode           string
		backoffUntil   sql.NullTime
		lastDelivery   sql.NullTime
		lastStatus     sql.NullInt64
		lastTestAt     sql.NullTime
		lastTestStatus sql.NullInt64
		backlogAt      sql.NullTime
		prevExpires    sql.NullTime
		urlData        []byte
		urlDEK         []byte
	)
	if err := sc.Scan(&ep.ID, &ep.Owner, &ep.Label, &mode, &ep.CreatedAt, &ep.UpdatedAt,
		&ep.ConsecutiveFailures, &backoffUntil,
		&lastDelivery, &lastStatus, &ep.LastError,
		&lastTestAt, &lastTestStatus, &ep.LastTestError,
		&backlogAt, &ep.BacklogDepth, &prevExpires, &urlData, &urlDEK); err != nil {
		return nil, err
	}
	ep.Payload = PayloadMode(mode)
	setTime := func(dst **time.Time, v sql.NullTime) {
		if v.Valid {
			t := v.Time
			*dst = &t
		}
	}
	setTime(&ep.BackoffUntil, backoffUntil)
	setTime(&ep.LastDeliveryAt, lastDelivery)
	setTime(&ep.LastTestAt, lastTestAt)
	setTime(&ep.BacklogAt, backlogAt)
	// Only while it is still live. An expired previous key is no
	// different from never having rotated, and showing a date in the
	// past would read as "still accepted".
	if prevExpires.Valid && prevExpires.Time.After(s.now().UTC()) {
		t := prevExpires.Time
		ep.PrevSecretExpiresAt = &t
	}
	ep.LastStatus = int(lastStatus.Int64)
	ep.LastTestStatus = int(lastTestStatus.Int64)

	ep.HasDestination = len(urlData) > 0
	// The hint needs the value, so it is computed here and the value is
	// dropped. An unopenable row still reports that a destination exists,
	// which is the honest answer: there is one, and this server cannot
	// read it.
	if plain, err := s.open(urlData, urlDEK); err == nil {
		ep.DestinationHint = HintFor(string(plain))
	}
	return &ep, nil
}

// EndpointsForOwner returns one person's destinations, newest first.
func (s *Store) EndpointsForOwner(ctx context.Context, owner string) ([]*Endpoint, error) {
	if owner == "" {
		return nil, fmt.Errorf("an owner is required")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+endpointColumns+` FROM webhook_endpoints WHERE owner = ? ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Endpoint
	for rows.Next() {
		ep, err := s.scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// GetEndpoint returns one of an owner's destinations.
func (s *Store) GetEndpoint(ctx context.Context, owner, id string) (*Endpoint, error) {
	if owner == "" || id == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+endpointColumns+` FROM webhook_endpoints WHERE id = ? AND owner = ?`, id, owner)
	ep, err := s.scanEndpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return ep, err
}

// endpointByID returns a destination without an owner filter, for the
// delivery path, which has already established whose work it is doing.
func (s *Store) endpointByID(ctx context.Context, id string) (*Endpoint, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+endpointColumns+` FROM webhook_endpoints WHERE id = ?`, id)
	ep, err := s.scanEndpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return ep, err
}

// Destination unseals an endpoint's URL and every signing key still in use.
//
// Separate from every other read, and named for what it does, so that the
// value cannot travel in a struct that something later serializes. The only
// callers are the delivery worker and the test button.
//
// secrets[0] is the current key. A second is present only during a
// rotation's grace period, and a delivery signs with all of them so a
// receiver matches whichever it has been updated to.
func (s *Store) Destination(ctx context.Context, id string) (destination string, secrets [][]byte, err error) {
	var (
		urlData, urlDEK       []byte
		secretData, secretDEK []byte
		prevData, prevDEK     []byte
		prevExpires           sql.NullTime
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT url_data, url_dek, secret_data, secret_dek,
		        prev_secret_data, prev_secret_dek, prev_secret_expires_at
		   FROM webhook_endpoints WHERE id = ?`, id).
		Scan(&urlData, &urlDEK, &secretData, &secretDEK,
			&prevData, &prevDEK, &prevExpires)
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
	secrets = [][]byte{plainSecret}

	if len(prevData) > 0 && prevExpires.Valid && prevExpires.Time.After(s.now().UTC()) {
		plainPrev, err := s.open(prevData, prevDEK)
		if err != nil {
			// The current key opened and this one did not, which
			// should not happen -- they were sealed by the same
			// server with the same schedule. Deliver signed with
			// the current key alone rather than failing the
			// delivery: a receiver that has already been updated
			// still verifies, and one that has not is no worse off
			// than if the grace period had ended.
			return string(plainURL), secrets, nil
		}
		secrets = append(secrets, plainPrev)
	}
	return string(plainURL), secrets, nil
}

// RotateSecret mints a new signing key and keeps the old one verifying for
// SecretGracePeriod.
//
// Returns the new key, which is the only time it is readable. The old key
// is NOT discarded at once: swapping outright would lose deliveries rather
// than delaying them, because a receiver that rejects a signature answers
// 4xx and a 4xx that is not 408 or 429 is a permanent refusal here.
//
// Rotating again inside the window replaces the previous key with the one
// just retired, which is what somebody rotating because a key LEAKED
// wants -- the compromised key stops being accepted immediately.
func (s *Store) RotateSecret(ctx context.Context, owner, id string) (*Endpoint, []byte, error) {
	ep, err := s.GetEndpoint(ctx, owner, id)
	if err != nil {
		return nil, nil, err
	}
	var currentData, currentDEK []byte
	if err := s.db.QueryRowContext(ctx,
		`SELECT secret_data, secret_dek FROM webhook_endpoints WHERE id = ?`, id).
		Scan(&currentData, &currentDEK); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}

	// A DEK read back from an unsealed row scans as nil, and a nil
	// []byte binds as SQL NULL rather than as a zero-length blob --
	// which the NOT NULL column refuses. The same trap seal() documents,
	// met here from the other direction: reading a value back out and
	// writing it to another column.
	if currentDEK == nil {
		currentDEK = []byte{}
	}

	secret, err := NewSecret()
	if err != nil {
		return nil, nil, err
	}
	secretData, secretDEK, err := s.seal(secret)
	if err != nil {
		return nil, nil, fmt.Errorf("sealing the new signing secret: %w", err)
	}

	now := s.now().UTC()
	until := now.Add(SecretGracePeriod)
	res, err := s.db.ExecContext(ctx, `
		UPDATE webhook_endpoints
		   SET secret_data = ?, secret_dek = ?,
		       prev_secret_data = ?, prev_secret_dek = ?,
		       prev_secret_expires_at = ?, updated_at = ?
		 WHERE id = ? AND owner = ?`,
		secretData, secretDEK, currentData, currentDEK, until, now, id, owner)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil, ErrNotFound
	}

	out, err := s.GetEndpoint(ctx, owner, id)
	if err != nil {
		return nil, nil, err
	}
	_ = ep
	return out, secret, nil
}

// UpdateEndpoint changes a destination's name, payload shape or URL.
//
// A new URL is a new URL, not a rotation of the secret: the receiver is the
// same receiver, and re-keying it would mean every subscription's receiver
// has to be reconfigured because one of them moved.
func (s *Store) UpdateEndpoint(ctx context.Context, owner, id string, label *string, payload *PayloadMode, destination *string) (*Endpoint, error) {
	ep, err := s.GetEndpoint(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if label != nil {
		ep.Label = *label
	}
	if payload != nil {
		ep.Payload = *payload
	}
	if err := ep.Validate(); err != nil {
		return nil, err
	}
	now := s.now().UTC()

	sets := `label = ?, payload_mode = ?, updated_at = ?`
	args := []any{ep.Label, string(ep.Payload), now}

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
		// A destination that has just been replaced has not failed yet.
		// Keeping the old backoff would make a freshly fixed URL wait
		// out a penalty earned by the one it replaced.
		sets += `, consecutive_failures = 0, backoff_until = NULL`
	}

	args = append(args, id, owner)
	res, err := s.db.ExecContext(ctx,
		`UPDATE webhook_endpoints SET `+sets+` WHERE id = ? AND owner = ?`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetEndpoint(ctx, owner, id)
}

// DeleteEndpoint removes a destination, every subscription feeding it and
// everything queued for it.
//
// owner may be empty for an administrative delete; a user's call always
// passes one, so somebody else's endpoint is not found rather than refused.
//
// The subscriptions go with it, which is worth being explicit about in a UI:
// deleting a receiver is not the same gesture as stopping watching one job,
// and the row counts differ.
func (s *Store) DeleteEndpoint(ctx context.Context, owner, id string) error {
	query := `DELETE FROM webhook_endpoints WHERE id = ?`
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
	// Subscriptions and deliveries go explicitly, NOT by the foreign
	// key's cascade. The schema declares ON DELETE CASCADE, but SQLite
	// enforces foreign keys only when PRAGMA foreign_keys is on, and
	// this application database never turns it on -- so the declaration
	// documents the relationship and enforces nothing. Relying on it
	// would leave a deleted destination's subscriptions behind, still
	// live, still evaluated, enqueuing for an endpoint that is gone.
	//
	// After the endpoint, so a failure here leaves rows pointing at an
	// endpoint that no longer exists -- which the claim query cannot see
	// and ExpireStale will settle -- rather than a live endpoint whose
	// subscriptions or queue vanished.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM webhook_subscriptions WHERE endpoint_id = ?`, id); err != nil {
		return fmt.Errorf("the destination was deleted but its subscriptions were not: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM webhook_deliveries WHERE endpoint_id = ?`, id); err != nil {
		return fmt.Errorf("the destination was deleted but its queued deliveries were not: %w", err)
	}
	return nil
}

// CreateSubscription stores what to watch, against an endpoint the same
// owner holds.
//
// The endpoint is looked up under the owner filter, so naming somebody
// else's destination is "no such destination" rather than a refusal -- an id
// alone must not reveal that it exists.
func (s *Store) CreateSubscription(ctx context.Context, sub *Subscription) (*Subscription, error) {
	if err := sub.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.GetEndpoint(ctx, sub.Owner, sub.EndpointID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("no such destination %q", sub.EndpointID)
		}
		return nil, err
	}

	var held int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM webhook_subscriptions WHERE owner = ?`, sub.Owner).Scan(&held); err != nil {
		return nil, fmt.Errorf("counting subscriptions: %w", err)
	}
	if held >= MaxSubscriptionsPerOwner {
		return nil, fmt.Errorf("you already hold the maximum of %d subscriptions; delete one to add another", MaxSubscriptionsPerOwner)
	}

	id, err := newID("ws_")
	if err != nil {
		return nil, err
	}
	events, err := json.Marshal(sub.Events)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	// Every subscription gets a deadline at creation. For an owner
	// subscription it is the only one it will have. For a job or batch
	// one it is a BACKSTOP: the evaluator moves it earlier once the
	// subject has ended, and the backstop is what stops a subscription
	// naming a job that is never submitted from being evaluated for ever.
	ttl := DefaultOwnerTTL
	if sub.Scope.Terminable() {
		ttl = TerminableBackstopTTL
	}
	expiresAt := now.Add(ttl)

	var cluster, proc any
	if sub.Scope == ScopeJob {
		cluster, proc = sub.Job.Cluster, sub.Job.Proc
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_subscriptions (id, endpoint_id, owner, label, scope,
		                                   job_cluster, job_proc, schedd,
		                                   batch_id, batch_name, events_json,
		                                   created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, sub.EndpointID, sub.Owner, sub.Label, string(sub.Scope),
		cluster, proc, sub.Job.Schedd, sub.BatchID, sub.BatchName, string(events),
		now, now, expiresAt,
	); err != nil {
		return nil, fmt.Errorf("storing the subscription: %w", err)
	}

	out := *sub
	out.ID = id
	out.CreatedAt = now
	out.UpdatedAt = now
	out.ExpiresAt = &expiresAt
	return &out, nil
}

// subscriptionColumns is the select list every subscription scan shares.
const subscriptionColumns = `id, endpoint_id, owner, label, scope,
	job_cluster, job_proc, schedd, batch_id, batch_name, events_json,
	created_at, updated_at, expires_at, capped_at, capped_jobs, tracked_json`

// scanSubscription reads one row.
func (s *Store) scanSubscription(sc interface{ Scan(...any) error }) (*Subscription, error) {
	var (
		sub           Subscription
		scope         string
		cluster, proc sql.NullInt64
		eventsJSON    string
		expires       sql.NullTime
		cappedAt      sql.NullTime
		trackedJSON   string
	)
	if err := sc.Scan(&sub.ID, &sub.EndpointID, &sub.Owner, &sub.Label, &scope,
		&cluster, &proc, &sub.Job.Schedd, &sub.BatchID, &sub.BatchName, &eventsJSON,
		&sub.CreatedAt, &sub.UpdatedAt, &expires, &cappedAt, &sub.CappedJobs,
		&trackedJSON); err != nil {
		return nil, err
	}
	if trackedJSON != "" {
		tracked, err := jobwatch.DecodeTracked(trackedJSON)
		if err != nil {
			// A tracked set that no longer decodes is a load error.
			// Reading it as empty would make every job the subscription
			// already knows about look new, and re-deliver the lot.
			return nil, fmt.Errorf("subscription %s has a malformed tracked set: %w", sub.ID, err)
		}
		sub.Tracked = tracked
	}
	sub.Scope = Scope(scope)
	if cluster.Valid {
		sub.Job.Cluster = cluster.Int64
	}
	if proc.Valid {
		sub.Job.Proc = proc.Int64
	}
	if eventsJSON != "" {
		if err := json.Unmarshal([]byte(eventsJSON), &sub.Events); err != nil {
			// A row whose event list no longer parses is a load error,
			// not a subscription that quietly watches nothing.
			return nil, fmt.Errorf("subscription %s has a malformed event list: %w", sub.ID, err)
		}
	}
	if expires.Valid {
		t := expires.Time
		sub.ExpiresAt = &t
	}
	if cappedAt.Valid {
		t := cappedAt.Time
		sub.CappedAt = &t
	}
	return &sub, nil
}

// SubscriptionsForOwner returns one person's subscriptions, newest first.
func (s *Store) SubscriptionsForOwner(ctx context.Context, owner string) ([]*Subscription, error) {
	if owner == "" {
		return nil, fmt.Errorf("an owner is required")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+subscriptionColumns+` FROM webhook_subscriptions WHERE owner = ? ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Subscription
	for rows.Next() {
		sub, err := s.scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// GetSubscription returns one of an owner's subscriptions.
func (s *Store) GetSubscription(ctx context.Context, owner, id string) (*Subscription, error) {
	if owner == "" || id == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+subscriptionColumns+` FROM webhook_subscriptions WHERE id = ? AND owner = ?`, id, owner)
	sub, err := s.scanSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sub, err
}

// LiveSubscriptions returns every subscription that should still be
// evaluated, oldest first.
//
// There is no disabled state to exclude. A destination that is failing still
// has its subscriptions evaluated and their deliveries enqueued -- the
// backoff is applied when something is SENT, not when an event is noticed.
// Stopping evaluation would lose the events that happened during an outage,
// which is exactly what a receiver coming back up wants to be told about.
func (s *Store) LiveSubscriptions(ctx context.Context) ([]*Subscription, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+subscriptionColumns+`
		   FROM webhook_subscriptions
		  WHERE expires_at IS NULL OR expires_at > ?
		  ORDER BY created_at`, s.now().UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Subscription
	for rows.Next() {
		sub, err := s.scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// UpdateSubscription changes a subscription's name and events.
//
// Not its endpoint and not its scope: either of those is a different
// subscription wearing the same id, and its tracked set -- which is what
// decides whether a job is new -- would describe the old one.
//
// Any update renews an owner subscription's deadline. Editing one is the
// clearest possible statement that somebody still wants it.
func (s *Store) UpdateSubscription(ctx context.Context, owner, id string, label *string, events []jobwatch.Event) (*Subscription, error) {
	sub, err := s.GetSubscription(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if label != nil {
		sub.Label = *label
	}
	if events != nil {
		sub.Events = events
	}
	if err := sub.Validate(); err != nil {
		return nil, err
	}
	eventsJSON, err := json.Marshal(sub.Events)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()

	sets := `label = ?, events_json = ?, updated_at = ?`
	args := []any{sub.Label, string(eventsJSON), now}
	if !sub.Scope.Terminable() {
		t := now.Add(DefaultOwnerTTL)
		sets += `, expires_at = ?`
		args = append(args, t)
	}

	args = append(args, id, owner)
	res, err := s.db.ExecContext(ctx,
		`UPDATE webhook_subscriptions SET `+sets+` WHERE id = ? AND owner = ?`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetSubscription(ctx, owner, id)
}

// DeleteSubscription stops watching. The destination stays, with its secret
// and whatever else feeds it.
func (s *Store) DeleteSubscription(ctx context.Context, owner, id string) error {
	query := `DELETE FROM webhook_subscriptions WHERE id = ?`
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
	return nil
}

// Expire brings a subscription's deadline forward, which is how a job or
// batch subscription ends: its subject can no longer produce an event.
//
// Forward only. One already due to expire sooner keeps the sooner date -- an
// evaluator pass should not be able to extend a subscription's life, only to
// end it early.
func (s *Store) Expire(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhook_subscriptions SET expires_at = ?
		  WHERE id = ? AND (expires_at IS NULL OR expires_at > ?)`, at, id, at)
	return err
}

// SaveTracked stores a subscription's tracked job set, range-encoded.
//
// Skipped when the set has not changed, which is the common case: the
// interesting passes are the ones where a job appeared or finished, and those
// are rare next to the passes that confirm nothing moved.
func (s *Store) SaveTracked(ctx context.Context, id string, ids []jobwatch.JobID) error {
	blob, err := jobwatch.EncodeTracked(ids)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE webhook_subscriptions SET tracked_json = ? WHERE id = ? AND tracked_json != ?`,
		blob, id, blob)
	return err
}

// NoteCapped records that a subscription's selection exceeded the fan-out
// ceiling.
func (s *Store) NoteCapped(ctx context.Context, id string, jobs int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhook_subscriptions SET capped_at = ?, capped_jobs = ? WHERE id = ?`,
		s.now().UTC(), jobs, id)
	return err
}
