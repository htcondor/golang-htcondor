package connectors

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Where a submission's results go, and which of its jobs have been added
// already.
//
// The binding row IS the authorization for the output path. A connector
// credential is otherwise reachable only from the browser session that
// owns it; at job completion there is no session, so a worker uses the
// owner's stored refresh token. What keeps that from being a general
// capability is that it may act only on a destination some session
// already nominated, for a cluster that session submitted -- and this
// store is the only place a binding can come from.
//
// Nothing here talks to Google. It records a decision and reports what
// is still outstanding; appending is the caller's.

// OutputBinding is one submission's output destination.
type OutputBinding struct {
	// Subject is our user, as the session names them.
	Subject string
	// Schedd is the access point in multi-AP mode; empty is the home
	// one. Part of the identity because cluster ids are unique only per
	// schedd.
	Schedd  string
	Cluster int

	// Connector whose credential writes the rows.
	Connector ID
	// FileID and Tab are the whole of what a worker may touch.
	FileID string
	Tab    string
	// JobFile is the file in each job's sandbox holding its result.
	JobFile string
	// UserScoped is how the sweep names this binding's jobs when it asks
	// the queue about them: User == Subject when true (a placementd
	// account, whose jobs carry the whole account), Owner == the local
	// part when false. Recorded at creation from what the submitting
	// session was itself scoped by, rather than recomputed later from
	// the account -- two implementations of that question can disagree,
	// and the one that matters is the one the submission ran under.
	UserScoped bool

	CreatedAt time.Time
	// RetiredAt is set when nothing is left to append. A retired
	// binding authorizes nothing.
	RetiredAt *time.Time
}

// AppendedJob records that one job has been accounted for.
type AppendedJob struct {
	Proc int
	// Rows is how many this job contributed. Zero is a real answer: a
	// job may legitimately produce no records, which is not the same as
	// not having been looked at.
	Rows int
	// Note says why it contributed nothing, when it did not.
	Note       string
	AppendedAt time.Time
}

// ErrNoBinding means no open binding authorizes this destination.
var ErrNoBinding = errors.New("no output binding authorizes this spreadsheet for this cluster")

// PutOutputBinding records where a submission's results go.
//
// Called with a browser session behind it, which is the point: this is
// the moment a user authorizes a destination, and it is the only moment
// at which one can be created.
//
// Replaces any existing row for the cluster. A cluster id can be reused
// by a later submission once the queue has forgotten the first, and a
// stale binding must not be what the new one inherits.
func (s *Store) PutOutputBinding(ctx context.Context, b OutputBinding) error {
	if !s.ready() {
		return fmt.Errorf("no application database is configured")
	}
	if b.Subject == "" || b.FileID == "" || b.Tab == "" || b.JobFile == "" {
		return fmt.Errorf("an output binding needs a subject, a spreadsheet, a tab and a file")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO connector_output_bindings
			(subject, schedd, cluster, connector, file_id, tab, job_file,
			 user_scoped, created_at, retired_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(subject, schedd, cluster) DO UPDATE SET
			connector = excluded.connector,
			file_id   = excluded.file_id,
			tab       = excluded.tab,
			job_file  = excluded.job_file,
			user_scoped = excluded.user_scoped,
			created_at = excluded.created_at,
			retired_at = NULL`,
		b.Subject, b.Schedd, b.Cluster, b.Connector.String(),
		b.FileID, b.Tab, b.JobFile, b.UserScoped, s.clock())
	if err != nil {
		return fmt.Errorf("recording the output binding for cluster %d: %w", b.Cluster, err)
	}
	return nil
}

// OpenOutputBindings lists the bindings with work possibly outstanding.
//
// The worker's whole view of what it may do. A retired binding is not
// returned, so retiring one is how its authority ends.
func (s *Store) OpenOutputBindings(ctx context.Context, limit int) ([]OutputBinding, error) {
	if !s.ready() {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT subject, schedd, cluster, connector, file_id, tab, job_file,
		       user_scoped, created_at
		  FROM connector_output_bindings
		 WHERE retired_at IS NULL
		 ORDER BY created_at
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing open output bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []OutputBinding
	for rows.Next() {
		var b OutputBinding
		var connector string
		if err := rows.Scan(&b.Subject, &b.Schedd, &b.Cluster, &connector,
			&b.FileID, &b.Tab, &b.JobFile, &b.UserScoped, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("reading an output binding: %w", err)
		}
		id, perr := ParseID(connector)
		if perr != nil {
			// A row naming a connector that no longer parses is not one
			// to act on, and not one to fail the whole sweep over.
			continue
		}
		b.Connector = id
		out = append(out, b)
	}
	return out, rows.Err()
}

// OutputBindingFor returns the open binding for one cluster.
//
// The authorization check, asked as a question rather than assumed: a
// caller about to write to a spreadsheet passes the destination it is
// about to use and gets ErrNoBinding if nothing nominated it.
func (s *Store) OutputBindingFor(
	ctx context.Context,
	subject, schedd string,
	cluster int,
) (OutputBinding, error) {
	if !s.ready() {
		return OutputBinding{}, fmt.Errorf("no application database is configured")
	}
	var b OutputBinding
	var connector string
	err := s.db.QueryRowContext(ctx, `
		SELECT subject, schedd, cluster, connector, file_id, tab, job_file,
		       user_scoped, created_at
		  FROM connector_output_bindings
		 WHERE subject = ? AND schedd = ? AND cluster = ? AND retired_at IS NULL`,
		subject, schedd, cluster).Scan(&b.Subject, &b.Schedd, &b.Cluster, &connector,
		&b.FileID, &b.Tab, &b.JobFile, &b.UserScoped, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OutputBinding{}, ErrNoBinding
	}
	if err != nil {
		return OutputBinding{}, fmt.Errorf("reading the output binding for cluster %d: %w", cluster, err)
	}
	id, perr := ParseID(connector)
	if perr != nil {
		return OutputBinding{}, fmt.Errorf("the output binding for cluster %d names connector %q, "+
			"which is not a connector id: %w", cluster, connector, perr)
	}
	b.Connector = id
	return b, nil
}

// RetireOutputBinding ends a binding's authority.
func (s *Store) RetireOutputBinding(ctx context.Context, subject, schedd string, cluster int) error {
	if !s.ready() {
		return fmt.Errorf("no application database is configured")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE connector_output_bindings
		   SET retired_at = ?
		 WHERE subject = ? AND schedd = ? AND cluster = ?`,
		s.clock(), subject, schedd, cluster)
	if err != nil {
		return fmt.Errorf("retiring the output binding for cluster %d: %w", cluster, err)
	}
	return nil
}

// MarkAppended records that one job has been accounted for.
//
// Written AFTER the append succeeds, deliberately. The other order
// loses a job's results on a crash between the two, and a missing row
// is recoverable by hand where a duplicate is not: values.append has no
// idempotency key, so a job added twice leaves two identical rows and
// nobody can say which was the spurious one.
//
// Insert-only on conflict: the first account of a job stands. A second
// call for the same proc is a retry that should not have happened, and
// overwriting would hide it.
func (s *Store) MarkAppended(
	ctx context.Context,
	subject, schedd string,
	cluster int,
	job AppendedJob,
) error {
	if !s.ready() {
		return fmt.Errorf("no application database is configured")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO connector_output_appended
			(subject, schedd, cluster, proc, appended_at, row_count, note)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(subject, schedd, cluster, proc) DO NOTHING`,
		subject, schedd, cluster, job.Proc, s.clock(), job.Rows, job.Note)
	if err != nil {
		return fmt.Errorf("recording job %d.%d as appended: %w", cluster, job.Proc, err)
	}
	return nil
}

// AppendedJobs reports which of a cluster's jobs are already accounted
// for, by proc.
func (s *Store) AppendedJobs(
	ctx context.Context,
	subject, schedd string,
	cluster int,
) (map[int]AppendedJob, error) {
	if !s.ready() {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT proc, appended_at, row_count, note
		  FROM connector_output_appended
		 WHERE subject = ? AND schedd = ? AND cluster = ?`,
		subject, schedd, cluster)
	if err != nil {
		return nil, fmt.Errorf("listing appended jobs for cluster %d: %w", cluster, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[int]AppendedJob{}
	for rows.Next() {
		var j AppendedJob
		if err := rows.Scan(&j.Proc, &j.AppendedAt, &j.Rows, &j.Note); err != nil {
			return nil, fmt.Errorf("reading an appended job: %w", err)
		}
		out[j.Proc] = j
	}
	return out, rows.Err()
}
