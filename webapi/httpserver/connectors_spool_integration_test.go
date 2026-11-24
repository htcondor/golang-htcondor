//go:build integration

package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PelicanPlatform/classad/classad"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/config"
	"github.com/htcondor/golang-htcondor/webapi/connectors/mockgoogle"
)

// A job whose ONLY input comes from a connector has to actually run.
//
// This is the end-to-end the unit tests cannot reach. The failure it
// guards against was invisible to every other kind of test: the submit
// returned 201, the submit file was correct, the reference was rewritten
// correctly, the job ad was correct -- and the job sat at "Spooling
// input data files" for ever, because the only code that fetches the
// file and releases the job ran on an upload the caller had no reason
// to make. Nothing but a real schedd shows that.
//
// `requirements = false` keeps the job in the queue after it is
// released, so the spool can be inspected; the fix is about getting OUT
// of the hold, not about matching.
//
// `transfer_executable = false` matters too: with it on, the schedd
// also waits for the executable, the connector files no longer account
// for every awaited input, and the server correctly declines to spool
// -- the caller's upload would be the thing that finishes it. That is
// the other half of the decision, and getting it wrong here would make
// this test pass for the wrong reason.
func TestConnectorOnlyJobIsReleasedIntegration(t *testing.T) {
	fix := setupConnectorFixture(t)
	const fileBody = "a,b,c\n"
	fix.google.PickFile("FILEID", "data.csv", []byte(fileBody))

	submitFile := strings.Join([]string{
		"executable = /bin/cat",
		"transfer_executable = false",
		"arguments = data.csv",
		"transfer_input_files = webapi-builtin-gdrive+gdrive2://FILEID/data.csv",
		"should_transfer_files = YES",
		"when_to_transfer_output = ON_EXIT",
		"output = run.out",
		"error = run.err",
		"log = run.log",
		// Keeps the job in the queue once released, so the spool can be read.
		"requirements = false",
		"queue",
		"",
	}, "\n")

	cluster := fix.submit(t, submitFile)
	ads := fix.awaitRelease(t, cluster, 1)

	iwd, _ := ads[0].EvaluateAttrString("Iwd")
	assertSpooled(t, iwd, "data.csv", fileBody)
}

// Per-job connector inputs: each job gets ITS file, and only its file.
//
// This is the submit file the web page's "From a connected account"
// table source builds -- one row per picked file, the reference in a
// `connector_input` queue variable that transfer_input_files names.
// Three separate things can go wrong with it, and only a real schedd
// shows any of them:
//
//  1. The reference is inside a `queue ... in (...)` item rather than on
//     an attribute line. If SplitSpoolInputs did not reach there, the
//     URL would survive into the job ad, the schedd would add a
//     transfer-plugin requirement nothing satisfies, and the jobs would
//     sit idle for ever with nothing to explain it.
//
//  2. Every proc's ad lists EVERY reference, because ConnectorInputs is
//     recorded once per submission. Without refsThisProcAwaits each
//     proc is handed all N files: N^2 fetches against the provider for
//     N files' worth of work. The download counts are the only surface
//     that shows it -- the sandboxes come out right either way, because
//     the schedd discards a file it was not waiting for.
//
//  3. Both procs have to leave the spooling hold, not just the first.
func TestPerJobConnectorInputsIntegration(t *testing.T) {
	fix := setupConnectorFixture(t)
	const bodyA = "first\n"
	const bodyB = "second,second\n"
	fix.google.PickFile("FILEA", "a.csv", []byte(bodyA))
	fix.google.PickFile("FILEB", "b.csv", []byte(bodyB))

	// Byte-for-byte what the submit page sends: buildConnectorTable puts
	// the file's name in the first template column and the reference in
	// connector_input, and buildSubmitFile (page.tsx) emits them as a
	// bracketed `queue COLS from (...)` block, two spaces of indent and
	// one row per line. Divergence here would make this test prove
	// something about a submit file nobody sends.
	submitFile := strings.Join([]string{
		"executable = /bin/cat",
		"transfer_executable = false",
		"arguments = $(connector_input)",
		"transfer_input_files = $(connector_input)",
		"should_transfer_files = YES",
		"when_to_transfer_output = ON_EXIT",
		"output = run-$(ProcId).out",
		"error = run-$(ProcId).err",
		"log = run.log",
		"requirements = false",
		"queue name, connector_input from (",
		"  a.csv webapi-builtin-gdrive+gdrive2://FILEA/a.csv",
		"  b.csv webapi-builtin-gdrive+gdrive2://FILEB/b.csv",
		")",
		"",
	}, "\n")

	cluster := fix.submit(t, submitFile)
	ads := fix.awaitRelease(t, cluster, 2)

	// Each proc's sandbox: its own file, with its own contents, and not
	// the other job's.
	byProc := map[int]*classad.ClassAd{}
	for _, ad := range ads {
		proc, _ := ad.EvaluateAttrInt("ProcId")
		byProc[int(proc)] = ad
	}
	want := map[int]struct {
		mine, body, notMine string
	}{
		0: {"a.csv", bodyA, "b.csv"},
		1: {"b.csv", bodyB, "a.csv"},
	}
	for proc, w := range want {
		ad, ok := byProc[proc]
		if !ok {
			t.Errorf("proc %d is not in the queue", proc)
			continue
		}
		iwd, _ := ad.EvaluateAttrString("Iwd")
		assertSpooled(t, iwd, w.mine, w.body)
		assertNotSpooled(t, iwd, w.notMine)
	}

	// The cost, not the result. Two files over two procs is one fetch
	// each; the un-narrowed version is two fetches each, and every other
	// assertion in this test passes in that version too.
	for _, id := range []string{"FILEA", "FILEB"} {
		if n := fix.google.Downloads(id); n != 1 {
			t.Errorf("%s was fetched %d times, want 1; each proc is fetching the "+
				"whole cluster's files", id, n)
		}
	}
}

// connectorFixture is a running schedd, an API server with one
// connector configured, a stand-in Google, and a browser session that
// has already connected an account to it.
type connectorFixture struct {
	ctx    context.Context
	base   string
	client *http.Client
	google *mockgoogle.Server
	schedd *htcondor.Schedd
	// server is the server under test, for the paths that have no HTTP
	// surface of their own -- the output sweep is driven by a ticker,
	// and a test wants to run one pass at a moment it chooses rather
	// than wait for one.
	server *Server
}

// connectorFixtureOpts asks for more than the default fixture.
type connectorFixtureOpts struct {
	// mirror runs an htcondordb schedd-sync mirror beside the pool and
	// points the server at it, which is what gives the server a job
	// watch feed -- and so a change-stream nudge -- at all.
	mirror bool
	// sweepInterval is HTTP_API_CONNECTOR_OUTPUT_SWEEP_INTERVAL, set
	// through the configuration rather than afterwards so the server
	// starts its sweep and nudge the way a real one does. Empty leaves
	// the feature off at startup, which is the default everywhere else.
	sweepInterval string
}

// setupConnectorFixture brings all of that up and registers its
// teardown.
func setupConnectorFixture(t *testing.T) *connectorFixture {
	t.Helper()
	return setupConnectorFixtureWith(t, connectorFixtureOpts{})
}

// setupConnectorFixtureWith is setupConnectorFixture with the parts a
// test may need switched on.
func setupConnectorFixtureWith(t *testing.T, opts connectorFixtureOpts) *connectorFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("condor_master"); err != nil {
		t.Skip("condor_master not in PATH")
	}

	harness := htcondor.SetupCondorHarness(t)
	if err := harness.WaitForDaemons(); err != nil {
		t.Fatalf("daemons failed to start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	collector := htcondor.NewCollector(harness.GetCollectorAddr())
	location, err := collector.LocateDaemon(ctx, "Schedd", "")
	if err != nil {
		t.Fatalf("locate schedd: %v", err)
	}

	user := localOwner(t)

	// The mirror, before the server, so the collector already has it to
	// discover. Its absence is not a skip here: every other assertion
	// in these tests holds without one, and only the nudge needs it.
	var htcCfg *config.Config
	if opts.mirror {
		bin, ok := optionalHTCondorDB()
		if !ok {
			t.Skip("no htcondordb binary; the change-stream nudge cannot be exercised without a mirror")
		}
		startScheddSyncMirror(t, harness, bin, t.TempDir())
		// Mirror routing needs both: the collector to discover the
		// database, and the HTCondor config for the SEC_* knobs it
		// authenticates with. Without them dbMirror.Enabled() is false
		// and no feed is ever started.
		htcCfg = mirrorClientConfig(harness)
	}
	if opts.sweepInterval != "" {
		if htcCfg == nil {
			htcCfg = config.NewEmpty()
		}
		htcCfg.Set("HTTP_API_CONNECTOR_OUTPUT_SWEEP_INTERVAL", opts.sweepInterval)
	}

	passwordsDir := filepath.Join(harness.GetSpoolDir(), "passwords.d")
	if err := os.MkdirAll(passwordsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	signingKeyPath := filepath.Join(passwordsDir, "POOL")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if err := os.WriteFile(signingKeyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(Config{
		ListenAddr:               "127.0.0.1:0",
		ScheddName:               location.Name,
		ScheddAddr:               location.Address,
		UserHeader:               "X-Test-User",
		UserHeaderTrustAnyUnsafe: true,
		SigningKeyPath:           signingKeyPath,
		TrustDomain:              "test.htcondor.org",
		UIDDomain:                "test.htcondor.org",
		OAuth2DBPath:             filepath.Join(harness.GetSpoolDir(), "oauth2.db"),
		HTCondorConfig:           htcCfg,
		Collector:                harnessCollector(harness, opts.mirror),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = server.Start() }()
	time.Sleep(500 * time.Millisecond)
	addr := server.GetAddr()
	if addr == "" {
		t.Fatal("server did not report an address")
	}
	base := "http://" + addr
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = server.Shutdown(sctx)
	})

	g := mockgoogle.New(t, mockgoogle.Config{
		ClientID:     "1234.apps.googleusercontent.com",
		ClientSecret: "s3cret",
		RedirectURI:  base + "/api/v1/connect/builtin:gdrive/callback",
	})

	secretFP := filepath.Join(t.TempDir(), "gdrive.secret")
	if err := os.WriteFile(secretFP, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.NewFromReader(strings.NewReader(strings.Join([]string{
		"HTTP_API_CONNECTORS = builtin:gdrive",
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_ID = 1234.apps.googleusercontent.com",
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_CLIENT_SECRET_FILE = " + secretFP,
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_GROUPS = lab",
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_AUTHORIZATION_URL = " + g.AuthorizationURL(),
		"HTTP_API_CONNECTOR_BUILTIN_GDRIVE_TOKEN_URL = " + g.TokenURL(),
	}, "\n")))
	if err != nil {
		t.Fatalf("parse the connector configuration: %v", err)
	}
	server.SetConnectorConfig(cfg)
	server.connectorDriveBaseURL = g.DriveBaseURL()
	server.connectorSheetsBaseURL = g.SheetsBaseURL()
	// Long enough that the ticker never fires during a test: the tests
	// run one pass themselves, at a moment they choose. Non-zero so the
	// submit path does not warn that nothing will be written, which is
	// also what a real deployment with this feature on looks like.
	//
	// Not when the configuration already said: a test that asked for an
	// interval wants the sweep and nudge the server started for itself,
	// and overwriting the field here would leave those goroutines
	// running on the old value while the field read a new one.
	if opts.sweepInterval == "" {
		server.connectorOutputSweepInterval = time.Hour
	}

	// A browser session: a connector credential is reachable only from
	// one, by design, so this is also the only caller that can submit a
	// job with connector inputs.
	sessionID, _, err := server.sessionStore.Create(user, []string{"lab"})
	if err != nil {
		t.Fatalf("create a session: %v", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	baseU, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(baseU, []*http.Cookie{
		//nolint:gosec // G124: a test cookie on a loopback server
		{Name: sessionCookieName, Value: sessionID, Path: "/"},
	})
	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	connectAccount(ctx, t, client, base)

	return &connectorFixture{
		ctx:    ctx,
		base:   base,
		client: client,
		google: g,
		schedd: htcondor.NewSchedd(location.Name, location.Address),
		server: server,
	}
}

// harnessCollector is the collector the server discovers the mirror
// through, and nil when there is no mirror to find: passing one without
// a database behind it would have the server retrying discovery for the
// whole test. Not "mirrorCollector", which metrics.go already uses for
// the Prometheus collector that reports mirror state.
func harnessCollector(h *htcondor.CondorTestHarness, mirror bool) *htcondor.Collector {
	if !mirror {
		return nil
	}
	return htcondor.NewCollector(h.GetCollectorAddr())
}

// submit posts a submit file and returns the cluster it created.
func (f *connectorFixture) submit(t *testing.T, submitFile string) int {
	t.Helper()
	return f.submitWith(t, JobSubmitRequest{SubmitFile: submitFile})
}

// submitWith is submit for a request that carries more than a submit
// file.
func (f *connectorFixture) submitWith(t *testing.T, sr JobSubmitRequest) int {
	t.Helper()
	body, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, f.base+"/api/v1/jobs",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	var submitted JobSubmitResponse
	dec := json.NewDecoder(resp.Body)
	_ = dec.Decode(&submitted)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("submit status %d", resp.StatusCode)
	}
	// A "stays held" warning is the server saying it could not fetch or
	// spool a job's connector files -- the failure this whole file is
	// about. Fail on it now rather than waiting out awaitRelease's
	// deadline for the same news. Other warnings are advice about the
	// submit file (no log, output to /dev/null) and are not this test's
	// business.
	for _, w := range submitted.Warnings {
		if strings.Contains(w, "stays held") {
			t.Fatalf("the server left a job held: %s", w)
		}
		t.Logf("submit warning: %s", w)
	}
	t.Logf("submitted cluster %d", submitted.ClusterID)
	return submitted.ClusterID
}

// awaitRelease waits for every proc of a cluster to leave the spooling
// hold and returns their ads.
//
// The failure it is written for is a job that never leaves, so the loop
// ends by reporting the last status seen rather than by timing out
// silently. A hold code other than 16 is a different fault and fails
// immediately: waiting out the deadline for it would hide what it was.
func (f *connectorFixture) awaitRelease(t *testing.T, cluster, procs int) []*classad.ClassAd {
	t.Helper()
	constraint := fmt.Sprintf("ClusterId == %d", cluster)
	var last string
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); {
		ads, _, err := f.schedd.QueryWithOptions(f.ctx, constraint, &htcondor.QueryOptions{
			Projection: []string{"ClusterId", "ProcId", "JobStatus",
				"HoldReasonCode", "HoldReason", "Iwd"},
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(ads) != procs {
			t.Fatalf("the queue holds %d of this cluster's %d jobs; "+
				"they should all be idle on requirements = false", len(ads), procs)
		}
		held := 0
		var status []string
		for _, ad := range ads {
			proc, _ := ad.EvaluateAttrInt("ProcId")
			st, _ := ad.EvaluateAttrInt("JobStatus")
			code, _ := ad.EvaluateAttrInt("HoldReasonCode")
			reason, _ := ad.EvaluateAttrString("HoldReason")
			status = append(status, fmt.Sprintf("proc %d: JobStatus=%d HoldReasonCode=%d HoldReason=%q",
				proc, st, code, reason))
			if st != 5 {
				continue
			}
			held++
			if code != 16 {
				t.Fatalf("proc %d is held for something else: HoldReasonCode=%d HoldReason=%q",
					proc, code, reason)
			}
		}
		last = strings.Join(status, "; ")
		if held == 0 {
			return ads
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("a job never left the spooling hold: %s "+
		"(this is the bug: nothing fetched its connector files)", last)
	return nil
}

// connectAccount completes the OAuth flow against the stand-in provider
// so the server holds a credential for this session's user.
func connectAccount(ctx context.Context, t *testing.T, c *http.Client, base string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/api/v1/connect/builtin:gdrive", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("begin the flow: %v", err)
	}
	_ = resp.Body.Close()
	authURL, err := resp.Location()
	if err != nil {
		t.Fatalf("no redirect to the provider (status %d): %v", resp.StatusCode, err)
	}

	areq, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	aresp, err := c.Do(areq)
	if err != nil {
		t.Fatalf("the provider refused: %v", err)
	}
	_ = aresp.Body.Close()
	back, err := aresp.Location()
	if err != nil {
		t.Fatalf("the provider did not redirect back: %v", err)
	}

	creq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+back.Path+"?"+back.RawQuery, nil)
	if err != nil {
		t.Fatal(err)
	}
	creq.Header.Set("Accept", "application/json")
	cresp, err := c.Do(creq)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer func() { _ = cresp.Body.Close() }()
	if cresp.StatusCode != http.StatusOK {
		t.Fatalf("callback status %d", cresp.StatusCode)
	}
}

// assertSpooled checks the connector file really landed, with its
// contents. Leaving the hold is necessary but not sufficient: a release
// with an empty sandbox would be a worse bug than the one being fixed.
func assertSpooled(t *testing.T, iwd, name, want string) {
	t.Helper()
	if iwd == "" {
		t.Error("the job ad has no Iwd, so the spool cannot be checked")
		return
	}
	path := filepath.Join(iwd, name)
	got, err := os.ReadFile(path) //nolint:gosec // G304: a path from the test's own harness
	if err != nil {
		t.Errorf("the job was released but %s is not in its spool: %v", name, err)
		return
	}
	if string(got) != want {
		t.Errorf("%s holds %q, want %q", name, string(got), want)
	}
}

// assertNotSpooled checks another job's file did NOT land in this one.
//
// This holds even without the per-proc narrowing, and that is worth
// knowing rather than discovering later: the awaited set is the
// schedd's allow list, so a spooled file it was not waiting for is
// discarded. The guard stays because it is the schedd's promise and not
// ours -- if that allow list ever widened, sending every proc the whole
// cluster's files would start filling sandboxes, and this is what would
// say so. The assertion that catches the narrowing itself is the
// download count.
func assertNotSpooled(t *testing.T, iwd, name string) {
	t.Helper()
	if iwd == "" {
		return
	}
	path := filepath.Join(iwd, name)
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s is in this job's spool, but it belongs to another job in the batch; "+
			"every proc is being sent the whole cluster's connector files", name)
	} else if !os.IsNotExist(err) {
		t.Errorf("cannot tell whether %s is in the spool: %v", name, err)
	}
}
