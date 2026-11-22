//go:build integration

package httpserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/config"
)

// Helpers for tests that run real htcondordb daemons: a single-AP
// mirror, or the spokes and hub of a federation.
//
// They need an htcondordb binary, which CI builds from the version
// pinned in .github/tools (so Dependabot bumps it and the tests start
// exercising each new release) and passes as HTCONDORDB_BINARY. Without
// one they skip, because a developer without htcondordb checked out
// should not be blocked by it; with HTCONDORDB_BINARY set to something
// unusable they fail, so a CI job that meant to run them cannot skip.

// htcondordbBinary locates the daemon, or skips.
func htcondordbBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("HTCONDORDB_BINARY"); bin != "" {
		if _, err := os.Stat(bin); err != nil { //nolint:gosec // G703: a path the test's own environment names
			// Set but wrong is a mistake worth failing on: a CI job that
			// meant to run this must not silently skip it.
			t.Fatalf("HTCONDORDB_BINARY=%s is not usable: %v", bin, err)
		}
		return bin
	}
	bin, err := exec.LookPath("htcondordb")
	if err != nil {
		t.Skip("htcondordb not found (set HTCONDORDB_BINARY or put it on PATH); skipping the mirror integration test")
	}
	return bin
}

// optionalHTCondorDB locates the daemon without deciding what to do
// about its absence.
//
// htcondordbBinary skips the whole test, which is right for a test that
// is ABOUT the mirror. A test whose subject is something else, and for
// which the mirror only adds a branch, needs to ask instead -- and must
// still fail on HTCONDORDB_BINARY set to something unusable, so a CI job
// that meant to cover the mirror branch cannot quietly take the other
// one.
func optionalHTCondorDB() (string, bool) {
	if bin := os.Getenv("HTCONDORDB_BINARY"); bin != "" {
		// Returned whether or not it stats: startHTCondorDB will fail
		// on it loudly, which is the behaviour htcondordbBinary has for
		// the same mistake.
		return bin, true
	}
	bin, err := exec.LookPath("htcondordb")
	return bin, err == nil
}

// mirrorClientConfig is the HTCondor configuration a mirror client
// authenticates with: FS throughout, since every process in these tests
// is the same user.
func mirrorClientConfig(h *htcondor.CondorTestHarness) *config.Config {
	cfg := config.NewEmpty()
	cfg.Set("SEC_DEFAULT_AUTHENTICATION_METHODS", "FS")
	cfg.Set("SEC_CLIENT_AUTHENTICATION_METHODS", "FS")
	cfg.Set("UID_DOMAIN", h.GetTrustDomain())
	cfg.Set("TRUST_DOMAIN", h.GetTrustDomain())
	return cfg
}

// htcondordbProc is one htcondordb daemon run outside condor_master,
// advertising to a harness collector. It can be stopped and started
// again over the same database directory, which is how a test makes a
// daemon go away and come back.
type htcondordbProc struct {
	t        *testing.T
	bin      string
	dir      string
	cfgPath  string
	addrFile string

	cmd    *exec.Cmd
	stderr *os.File
	done   chan struct{}
	// runs counts starts, so each run's stderr goes to its own file.
	runs int
}

// startHTCondorDB writes a config for an htcondordb in dir and starts
// it, returning once it has published an address. extra is appended to
// the generated configuration.
func startHTCondorDB(t *testing.T, h *htcondor.CondorTestHarness, bin, dir, extra string) *htcondordbProc {
	t.Helper()

	p := &htcondordbProc{
		t: t, bin: bin, dir: dir,
		cfgPath:  filepath.Join(dir, "condor_config"),
		addrFile: filepath.Join(dir, "addr"),
	}
	logDir := filepath.Join(dir, "log")
	for _, d := range []string{logDir, filepath.Join(dir, "db")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	// FS authentication throughout: every process is this test user, so
	// there is no token to mint and nothing to distribute. A test that
	// wants the token path too overrides the methods in extra.
	cfg := fmt.Sprintf(`
CONDOR_HOST = 127.0.0.1
COLLECTOR_HOST = %s
UID_DOMAIN = %s
TRUST_DOMAIN = %s
SEC_PASSWORD_DIRECTORY = %s
SEC_DEFAULT_AUTHENTICATION = REQUIRED
SEC_DEFAULT_AUTHENTICATION_METHODS = FS
SEC_DEFAULT_INTEGRITY = REQUIRED
SEC_DEFAULT_ENCRYPTION = OPTIONAL
ALLOW_DAEMON = *
ALLOW_WRITE = *
ALLOW_READ = *
ALLOW_ADMINISTRATOR = *
LOG = %s
HTCONDORDB_DIR = %s
HTCONDORDB_ADDRESS_FILE = %s
HTCONDORDB_ADVERTISE = true
UPDATE_INTERVAL = 5
%s
`, h.GetCollectorAddr(), h.GetTrustDomain(), h.GetTrustDomain(), h.GetPasswordDir(),
		logDir, filepath.Join(dir, "db"), p.addrFile, extra)
	if err := os.WriteFile(p.cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		p.Stop()
		// The daemon's own output is the only account of why a failure
		// happened on the far side of the connection.
		if t.Failed() {
			p.dumpLogs()
		}
	})
	p.Start()
	return p
}

// Start runs the daemon and waits for it to publish its address.
func (p *htcondordbProc) Start() {
	p.t.Helper()
	if p.cmd != nil {
		p.t.Fatalf("htcondordb in %s is already running", p.dir)
	}
	// A stale address file from the previous run would read as this run
	// being up before it is.
	_ = os.Remove(p.addrFile)

	p.runs++
	stderr, err := os.Create(filepath.Join(p.dir, fmt.Sprintf("stderr.%d.log", p.runs)))
	if err != nil {
		p.t.Fatal(err)
	}
	// Outside condor_master there is no shared-port endpoint, so the
	// daemon advertises its listener's own address -- with the default
	// ":0", the wildcard "<[::]:port>", which a federation hub rightly
	// refuses to pair with any schedd. Listen where everything else in
	// the harness does.
	// Not CommandContext: Stop ends it, with SIGTERM rather than a kill.
	cmd := exec.Command(p.bin, "-listen", "127.0.0.1:0") //nolint:gosec,noctx // test binary from HTCONDORDB_BINARY
	cmd.Env = append(os.Environ(), "CONDOR_CONFIG="+p.cfgPath)
	cmd.Stderr, cmd.Stdout = stderr, stderr
	if err := cmd.Start(); err != nil {
		p.t.Fatalf("starting htcondordb: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	p.cmd, p.stderr, p.done = cmd, stderr, done

	waitFor(p.t, "htcondordb to publish its address", 30*time.Second, func() bool {
		b, rerr := os.ReadFile(p.addrFile)
		return rerr == nil && len(strings.TrimSpace(string(b))) > 0
	})
}

// Stop shuts the daemon down the way condor_master would (SIGTERM, so
// it withdraws its collector ad) and waits for it to exit, killing it
// if it does not. A no-op when it is not running.
func (p *htcondordbProc) Stop() {
	if p.cmd == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		p.t.Logf("htcondordb in %s did not exit on SIGTERM; killing it", p.dir)
		_ = p.cmd.Process.Kill()
		<-p.done
	}
	_ = p.stderr.Close()
	p.cmd, p.stderr, p.done = nil, nil, nil
}

// Address returns the address the daemon published.
func (p *htcondordbProc) Address() string {
	b, _ := os.ReadFile(p.addrFile)
	first, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(first)
}

func (p *htcondordbProc) dumpLogs() {
	logs, _ := filepath.Glob(filepath.Join(p.dir, "stderr.*.log"))
	more, _ := filepath.Glob(filepath.Join(p.dir, "log", "*"))
	for _, f := range append(logs, more...) {
		b, err := os.ReadFile(f) //nolint:gosec // test log under the test's own dir
		if err != nil || len(b) == 0 {
			continue
		}
		p.t.Logf("=== %s ===\n%s", f, tail(b, 64*1024))
	}
}

// tail returns at most the last n bytes of b.
func tail(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}

// startMirror runs a plain htcondordb (no schedd sync) advertising to
// the harness collector, and returns once it has published an address.
func startMirror(t *testing.T, h *htcondor.CondorTestHarness, bin, dir string) {
	t.Helper()
	startHTCondorDB(t, h, bin, dir, "")
}

// startScheddSyncMirror runs an htcondordb that mirrors the harness
// schedd's own job_queue.log into a "jobs" table, rather than one a test
// seeds by hand.
//
// startMirror is enough wherever the rows are the test's own fixture. It
// is NOT enough when what is under test reads the jobs table: a fresh
// database has no such table at all -- schedd-sync is what creates it --
// so a listing built from it comes back empty, which reads as "this
// person has no jobs" rather than "nothing is mirroring them".
//
// HISTORY is cleared because the harness has no history file to tail and
// a sync pointed at a missing one logs on every pass; the queue is what
// a jobs table is built from.
func startScheddSyncMirror(t *testing.T, h *htcondor.CondorTestHarness, bin, dir string) *htcondordbProc {
	t.Helper()
	// htcondordb refuses schedd-sync as root on purpose: it reads
	// another daemon's files, and doing that with privilege is the
	// symlink hazard the refusal exists to prevent. Skipped rather than
	// failed so a root CI container does not report this as broken.
	if os.Geteuid() == 0 {
		t.Skip("htcondordb refuses schedd-sync as root; run this suite unprivileged")
	}
	// TOKEN as well as FS, which the base configuration's comment
	// anticipates. A caller arriving through the SSH gateway is credited
	// with a minted IDTOKEN rather than this process's filesystem
	// identity, so its client offers [TOKEN SCITOKENS SSL] and an
	// FS-only database answers DENIED -- "no compatible authentication
	// methods" -- which surfaces as an empty or unreadable /outputs
	// several layers up.
	return startHTCondorDB(t, h, bin, dir, fmt.Sprintf(
		"HTCONDORDB_SYNC_SCHEDD = true\n"+
			"HTCONDORDB_JOB_QUEUE_LOG = %s\n"+
			"HISTORY =\n"+
			"SEC_DEFAULT_AUTHENTICATION_METHODS = FS, TOKEN\n"+
			"SEC_TOKEN_POOL_SIGNING_KEY_FILE = %s\n",
		filepath.Join(h.GetSpoolDir(), "job_queue.log"), h.GetSigningKeyPath()))
}

func waitFor(t *testing.T, what string, limit time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", limit, what)
}
