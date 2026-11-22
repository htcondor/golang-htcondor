package appdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The application database is shared by every subsystem in the server,
// and until it ran in WAL mode with more than one connection, a single
// slow write delayed everything else that touched it -- including
// validating a browser session, which is why page loads stalled behind
// unrelated writes.

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenUsesWAL(t *testing.T) {
	db := openTemp(t)
	var mode string
	if err := db.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

// TestEveryConnectionGetsThePragmas: journal_mode lives in the file, but
// busy_timeout is per connection, so a pool that opens a second one must
// configure it too. Checked across more connections than the pool holds
// idle by default, which is where this would otherwise break.
func TestEveryConnectionGetsThePragmas(t *testing.T) {
	db := openTemp(t)
	// Bounded, so that a pool too small to hand out this many fails the
	// test instead of blocking on the next one for ever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conns := make([]*sql.Conn, 0, maxOpenConns)
	for i := 0; i < maxOpenConns; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			for _, open := range conns {
				_ = open.Close()
			}
			t.Fatalf("could not take connection %d of %d from the pool: %v", i+1, maxOpenConns, err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		var timeout int
		if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
			t.Fatalf("conn %d busy_timeout: %v", i, err)
		}
		if timeout < 5000 {
			t.Errorf("conn %d busy_timeout = %d, want at least 5000", i, timeout)
		}
		var sync int
		if err := c.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&sync); err != nil {
			t.Fatalf("conn %d synchronous: %v", i, err)
		}
		// 2 is FULL. Durability is deliberately unchanged by the WAL
		// switch; dropping to NORMAL is an operator's decision.
		if sync != 2 {
			t.Errorf("conn %d synchronous = %d, want 2 (FULL)", i, sync)
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// TestForeignKeysStayOff pins a thing the pragmas must NOT change.
//
// Several stores declare ON DELETE CASCADE and delete the dependent rows
// themselves, because enforcement has always been off. Turning it on
// here would make those cascades start firing for the first time.
func TestForeignKeysStayOff(t *testing.T) {
	db := openTemp(t)
	var on int
	if err := db.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if on != 0 {
		t.Errorf("foreign_keys = %d; enabling it would start firing cascades that stores already do by hand", on)
	}
}

// TestAReadDoesNotWaitForAWrite is the behaviour the change exists for.
//
// A deliberately slow write is held open while a read runs. Under the
// old single-connection pool the read could not start until the write
// finished; in WAL mode with a pool it proceeds against the last
// committed snapshot.
func TestAReadDoesNotWaitForAWrite(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t (k, v) VALUES (1, 'before')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO t (k, v) VALUES (2, 'during')`); err != nil {
		t.Fatalf("write in tx: %v", err)
	}

	// The write is open and uncommitted. A read must still complete.
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		var v string
		if err := db.QueryRowContext(ctx, `SELECT v FROM t WHERE k = 1`).Scan(&v); err != nil {
			done <- -1
			return
		}
		done <- time.Since(start)
	}()

	select {
	case d := <-done:
		if d < 0 {
			t.Fatal("the read failed while a write was open")
		}
		if d > 2*time.Second {
			t.Errorf("the read took %s while a write was open; it is still queuing behind it", d)
		}
	case <-time.After(5 * time.Second):
		_ = tx.Rollback()
		t.Fatal("a read blocked on an open write: the pool is still serializing everything")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestConcurrentWritersAllLand: SQLite still has one writer, so the
// point is that the others WAIT rather than failing with "database is
// locked". That is what the busy timeout and _txlock=immediate buy.
func TestConcurrentWritersAllLand(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t (k INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				errs <- err
				return
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO t (k) VALUES (?)`, k); err != nil {
				_ = tx.Rollback()
				errs <- err
				return
			}
			errs <- tx.Commit()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent writer failed instead of waiting: %v", err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != writers {
		t.Errorf("%d rows, want %d: a writer was lost", n, writers)
	}
}

// TestAPathWithAQuestionMarkStillOpens: the DSN carries the pragmas as a
// query string, and the driver splits on the first "?" -- which, for
// such a path, is the operator's. It has to open anyway.
func TestAPathWithAQuestionMarkStillOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "odd?name.db")
	var warned []string
	db, err := OpenWithWarnings(path, func(msg string, _ ...any) { warned = append(warned, msg) })
	if err != nil {
		t.Fatalf("OpenWithWarnings: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE t (k INTEGER)`); err != nil {
		t.Fatalf("the database is not usable: %v", err)
	}
	// And it says why it is slower than the others, rather than being
	// quietly different.
	if len(warned) == 0 {
		t.Error("a path that cannot carry pragmas warned about nothing")
	}
}
