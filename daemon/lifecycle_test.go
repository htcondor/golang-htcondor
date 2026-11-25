package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/config"
)

// newLocalListener returns a bound loopback TCP listener for the lifecycle tests.
func newLocalListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// TestServeListenersServesAllAndDrains verifies ServeListeners runs the handler on every
// listener (both reachable) and that a graceful Shutdown drains all of them, returning nil.
func TestServeListenersServesAllAndDrains(t *testing.T) {
	d := newTestDaemon(t)
	ln1, ln2 := newLocalListener(t), newLocalListener(t)

	var started atomic.Int32
	entered := make(chan struct{}, 2)
	serve := func(ctx context.Context, ln net.Listener) error {
		started.Add(1)
		entered <- struct{}{}
		go func() { <-ctx.Done(); _ = ln.Close() }() // unblock Accept on shutdown
		for {
			c, err := ln.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// The only thing that closes this listener is the ctx.Done() goroutine
				// above, so an Accept error reporting a closed listener means shutdown is
				// in flight -- even if ctx.Err() isn't yet observable on this goroutine, or
				// the close races the accept syscall so the kernel reports EBADF ("bad file
				// descriptor") instead of Go's net.ErrClosed. Treat any closed-listener
				// error as a graceful stop rather than a serve failure.
				if errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EBADF) {
					return nil
				}
				return err
			}
			_ = c.Close() // prove reachability, then drop
		}
	}
	served := make(chan error, 1)
	go func() { served <- d.ServeListeners(context.Background(), serve, ln1, ln2) }()

	// Wait until the handler has actually been entered on both listeners before
	// asserting the count. The contract under test is "serve runs on every listener,
	// and Shutdown drains them all cleanly"; the per-listener entry signal proves the
	// handler was invoked on each. We deliberately do NOT dial the listeners to prove
	// reachability: a bound listener's reachability is stdlib behavior, not
	// ServeListeners's, and an external dial races ServeListeners's
	// cancel-all-on-shutdown -- which closes every listener -- so the dial can observe
	// "connection refused" through no fault of the code under test (an arm64 flake).
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("serve entered on %d listeners, want 2", started.Load())
		}
	}
	if got := started.Load(); got != 2 {
		t.Fatalf("serve started on %d listeners, want 2", got)
	}

	d.Shutdown()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("ServeListeners returned %v after Shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeListeners did not return after Shutdown")
	}
}

// TestServeListenersReturnsFirstError verifies that when one serve loop fails, ServeListeners
// cancels the rest and surfaces that error.
func TestServeListenersReturnsFirstError(t *testing.T) {
	d := newTestDaemon(t)
	ln1, ln2 := newLocalListener(t), newLocalListener(t)
	defer func() { _ = ln1.Close() }()
	defer func() { _ = ln2.Close() }()

	boom := errors.New("boom")
	serve := func(ctx context.Context, ln net.Listener) error {
		if ln == ln1 {
			return boom // one listener fails immediately
		}
		<-ctx.Done() // the other must be cancelled by ServeListeners
		return ctx.Err()
	}
	err := d.ServeListeners(context.Background(), serve, ln1, ln2)
	if !errors.Is(err, boom) {
		t.Fatalf("ServeListeners returned %v, want %v", err, boom)
	}
}

// TestServeListenersRequiresListener verifies the no-listener guard.
func TestServeListenersRequiresListener(t *testing.T) {
	d := newTestDaemon(t)
	if err := d.ServeListeners(context.Background(), func(context.Context, net.Listener) error { return nil }); err == nil {
		t.Fatal("ServeListeners with no listeners returned nil, want an error")
	}
}

// newTestDaemon builds a Daemon with an empty config and a logger, suitable for
// exercising the lifecycle methods without touching condor_master.
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	// A temp CONDOR_CONFIG so Reconfigure()'s config.New() reload succeeds.
	t.Setenv("CONDOR_CONFIG", "/dev/null")
	d, err := New(Options{Subsys: "TESTDAEMON", Config: config.NewEmpty()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestDaemonShutdownStopsServe(t *testing.T) {
	d := newTestDaemon(t)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	served := make(chan error, 1)
	go func() {
		served <- d.Serve(context.Background(), ln, func(ctx context.Context, _ net.Listener) error {
			<-ctx.Done() // serve until shutdown cancels us
			return nil
		})
	}()

	// Shutdown should make Serve return nil (graceful), like SIGTERM.
	d.Shutdown()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve returned %v after Shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}

	// Shutdown is idempotent.
	d.Shutdown()
}

func TestMasterGoneDecision(t *testing.T) {
	const master = 4242
	alive := func() bool { return true }
	dead := func() bool { return false }

	cases := []struct {
		name         string
		originalPPID int
		currentPPID  int
		aliveFn      func() bool
		want         bool
	}{
		{"same ppid, master alive: keep running", master, master, alive, false},
		{"ppid changed (reparented): shut down", master, 1, alive, true},
		{"ppid changed to non-1 subreaper: shut down", master, 9999, alive, true},
		{"same ppid but master pid ESRCH: shut down", master, master, dead, true},
		{"same ppid, nil probe: keep running", master, master, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := masterGone(tc.originalPPID, tc.currentPPID, tc.aliveFn); got != tc.want {
				t.Errorf("masterGone(%d,%d,...) = %v, want %v",
					tc.originalPPID, tc.currentPPID, got, tc.want)
			}
		})
	}
}

// TestMasterMonitorDisabledStandalone verifies the monitor never fires for a
// standalone daemon (UnderMaster false): Serve stays up until Shutdown, even
// though a standalone daemon's parent (the test process) is not the master.
func TestMasterMonitorDisabledStandalone(t *testing.T) {
	d := newTestDaemon(t)
	if d.UnderMaster() {
		t.Skip("test daemon unexpectedly under master")
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	served := make(chan error, 1)
	go func() {
		served <- d.Serve(context.Background(), ln, func(ctx context.Context, _ net.Listener) error {
			<-ctx.Done()
			return nil
		})
	}()

	// Give any (erroneously enabled) monitor more than a poll interval to fire.
	select {
	case err := <-served:
		t.Fatalf("Serve returned early (%v); monitor fired while standalone", err)
	case <-time.After(masterPollInterval + 500*time.Millisecond):
	}

	d.Shutdown()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve returned %v after Shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
}

func TestDaemonReconfigureRunsCallbacks(t *testing.T) {
	d := newTestDaemon(t)
	var calls int32
	d.OnReconfig(func(*config.Config) { atomic.AddInt32(&calls, 1) })

	d.Reconfigure()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("OnReconfig called %d times after Reconfigure, want 1", got)
	}
}

// TestReconfigureDropsCachedCredentials: a reconfig has to make the next
// credential read go back to disk.
//
// HTCondor has nothing to drop -- it builds a fresh SSL_CTX inside each
// authenticate() call and re-reads a token signing key per verification --
// so the cache this package shares with cedar is an optimisation over
// that, and until it was invalidated on SIGHUP it was a correctness
// change: bytes frozen at first read for the life of the process.
//
// On a certificate that is fail-closed. On a signing key it is fail-open:
// after SEC_PASSWORD_DIRECTORY/POOL is regenerated in response to a
// suspected compromise, the daemon went on honouring tokens signed with
// the retired key until it restarted.
//
// Driven through Reconfigure and the process-wide cache that
// GetSecurityConfig actually hands to cedar, because that is the wiring
// under test -- credential_reader_test.go calls Reload on a cache of its
// own, which passes whether or not anything is wired.
func TestReconfigureDropsCachedCredentials(t *testing.T) {
	// A path unique to this run: the cache is a package-level singleton
	// shared by every test in the process, so a collision could satisfy
	// the assertion by accident.
	path := filepath.Join(t.TempDir(), "signing-key")
	if err := os.WriteFile(path, []byte("first-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := newTestDaemon(t)
	sc, err := htcondor.GetSecurityConfig(d.Config(), 60000, "CLIENT")
	if err != nil {
		t.Fatalf("GetSecurityConfig: %v", err)
	}
	if sc.Credentials == nil {
		t.Fatal("no credential reader was wired, so there is nothing to invalidate")
	}

	got, err := sc.Credentials.ReadCredential(path)
	if err != nil {
		t.Fatalf("priming the cache: %v", err)
	}
	if string(got) != "first-key" {
		t.Fatalf("first read = %q, want %q", got, "first-key")
	}

	// Rotate it in place, which is what regenerating a pool password does.
	if err := os.WriteFile(path, []byte("second-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := sc.Credentials.ReadCredential(path); err != nil {
		t.Fatalf("cached read: %v", err)
	} else if string(got) != "first-key" {
		t.Fatalf("read after rotation = %q, want the cached %q -- "+
			"the cache is not caching, so this test proves nothing", got, "first-key")
	}

	d.Reconfigure()

	got, err = sc.Credentials.ReadCredential(path)
	if err != nil {
		t.Fatalf("read after reconfigure: %v", err)
	}
	if string(got) != "second-key" {
		t.Errorf("read after reconfigure = %q, want %q: the rotated key is still being served",
			got, "second-key")
	}
}
