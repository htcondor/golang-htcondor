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
	defer cancel()

	collector := htcondor.NewCollector(harness.GetCollectorAddr())
	location, err := collector.LocateDaemon(ctx, "Schedd", "")
	if err != nil {
		t.Fatalf("locate schedd: %v", err)
	}

	user := localOwner(t)

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
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = server.Shutdown(sctx)
	}()

	// The provider stand-in, and the one file the job asks for.
	g := mockgoogle.New(t, mockgoogle.Config{
		ClientID:     "1234.apps.googleusercontent.com",
		ClientSecret: "s3cret",
		RedirectURI:  base + "/api/v1/connect/builtin:gdrive/callback",
	})
	const fileBody = "a,b,c\n"
	g.PickFile("FILEID", "data.csv", []byte(fileBody))

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

	// The submit under test: every input file comes from the connector.
	submitFile := strings.Join([]string{
		"executable = /bin/cat",
		"transfer_executable = false",
		"arguments = data.csv",
		"transfer_input_files = webapi-builtin-gdrive+gdrive2://FILEID/data.csv",
		"should_transfer_files = YES",
		"when_to_transfer_output = ON_EXIT",
		// Keeps the job in the queue once released, so the spool can be read.
		"requirements = false",
		"queue",
		"",
	}, "\n")

	body, err := json.Marshal(JobSubmitRequest{SubmitFile: submitFile})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/jobs",
		strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
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
	if len(submitted.Warnings) > 0 {
		t.Logf("submit warnings: %v", submitted.Warnings)
	}
	t.Logf("submitted cluster %d", submitted.ClusterID)

	schedd := htcondor.NewSchedd(location.Name, location.Address)
	constraint := fmt.Sprintf("ClusterId == %d", submitted.ClusterID)

	// The assertion: the job leaves the spooling hold. Before the fix it
	// stayed in it for ever.
	var last string
	released := false
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); {
		ads, _, err := schedd.QueryWithOptions(ctx, constraint, &htcondor.QueryOptions{
			Projection: []string{"JobStatus", "HoldReasonCode", "HoldReason", "Iwd"},
		})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(ads) == 0 {
			t.Fatal("the job left the queue; it should be idle on requirements = false")
		}
		status, _ := ads[0].EvaluateAttrInt("JobStatus")
		code, _ := ads[0].EvaluateAttrInt("HoldReasonCode")
		reason, _ := ads[0].EvaluateAttrString("HoldReason")
		last = fmt.Sprintf("JobStatus=%d HoldReasonCode=%d HoldReason=%q", status, code, reason)
		if status != 5 {
			released = true
			iwd, _ := ads[0].EvaluateAttrString("Iwd")
			assertSpooled(t, iwd, "data.csv", fileBody)
			break
		}
		if code != 16 {
			t.Fatalf("the job is held for something else: %s", last)
		}
		time.Sleep(time.Second)
	}
	if !released {
		t.Fatalf("the job never left the spooling hold: %s "+
			"(this is the bug: nothing fetched its connector file)", last)
	}
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
