//go:build integration

// Copyright 2026 Morgridge Institute for Research
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/sftpjobs"
	"github.com/htcondor/golang-htcondor/webapi/templates"
)

// The +jobs service driven by OpenSSH's own sftp(1).
//
// Every other test of this service speaks to it through pkg/sftp -- the
// same library the server uses, in the same process. That settles the
// protocol but not the product: what a user actually types is `sftp`,
// and OpenSSH's client is a different implementation making different
// choices. It stats before it opens, pipelines reads, pages a readdir,
// sends an FSETSTAT after a put, and resolves "." by asking the server
// for a realpath -- and it refuses to connect at all over anything the
// gateway gets wrong about certificates, host keys or the subsystem
// request.
//
// None of that is reachable from in-process tests, and all of it is
// between the user and the feature. So this one forks the real binary,
// talks to a real gateway over a real socket with a real certificate,
// and submits to a real schedd.
func TestSFTPCLIDrivesTheJobsService(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (forks a real HTCondor and a real sftp)")
	}
	for _, bin := range []string{"condor_master", "sftp"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH; skipping", bin)
		}
	}

	harness := htcondor.SetupCondorHarness(t)
	if err := harness.WaitForDaemons(); err != nil {
		t.Fatalf("daemons: %v", err)
	}
	if err := harness.WaitForStartd(45 * time.Second); err != nil {
		t.Fatalf("startd never reported in: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	collector := htcondor.NewCollector(harness.GetCollectorAddr())
	location, err := collector.LocateDaemon(ctx, "Schedd", "")
	if err != nil {
		t.Fatalf("locate schedd: %v", err)
	}
	schedd := htcondor.NewSchedd(location.Name, location.Address)

	// The account the pool authenticates. The gateway compares the
	// authenticated user against the account it submits for, so the
	// certificate has to name this same person.
	account := currentUser(t)

	// An htcondordb mirroring this schedd, when one can be run. It is
	// what /outputs is built from, so with no mirror the read half of
	// the service is unavailable and this test asserts that instead.
	mirrored := false
	if bin, ok := optionalHTCondorDB(); ok {
		startScheddSyncMirror(t, harness, bin, t.TempDir())
		mirrored = true
	} else {
		t.Log("no htcondordb binary; /outputs will be checked for its refusal, not its contents")
	}

	workDir := t.TempDir()
	catalog := stageTemplateCatalog(t, workDir)
	bufferDir := filepath.Join(workDir, "spoolbuf")
	if err := os.MkdirAll(bufferDir, 0o700); err != nil {
		t.Fatalf("buffer dir: %v", err)
	}

	listenAddr, issuerURL := reserveAddr(t)
	server, err := NewServer(Config{
		ListenAddr:               listenAddr,
		OAuth2Issuer:             issuerURL,
		ScheddName:               location.Name,
		ScheddAddr:               location.Address,
		UserHeader:               "X-Test-User",
		UserHeaderTrustAnyUnsafe: true,
		SigningKeyPath:           harness.GetSigningKeyPath(),
		TrustDomain:              harness.GetTrustDomain(),
		UIDDomain:                harness.GetTrustDomain(),
		OAuth2DBPath:             filepath.Join(harness.GetSpoolDir(), "oauth2-sftpcli.db"),
		KEKFilePath:              writeKEK(t, workDir),
		// The gateway's device flow needs an OAuth2 provider, and
		// that is what stands one up -- a gateway without it is a
		// startup error, not a degraded gateway.
		EnableMCP:            true,
		SSHGatewayAddress:    "127.0.0.1:0",
		SSHGatewayPublicHost: "127.0.0.1",
		TemplateGlobalPath:   catalog,
		SpoolBufferDir:       bufferDir,
		// Mirror routing needs both: the collector to discover the
		// database, and the HTCondor config for the SEC_* knobs it
		// authenticates with. Without them dbMirror.Enabled() is false
		// and /outputs refuses before it looks for anything.
		Collector:      htcondor.NewCollector(harness.GetCollectorAddr()),
		HTCondorConfig: mirrorClientConfig(harness),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	startErr := make(chan error, 1)
	go func() { startErr <- server.Start() }()
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()

	gwAddr := waitForGateway(t, server, startErr)
	gwPort, err := portOf(gwAddr)
	if err != nil {
		t.Fatalf("gateway address %q: %v", gwAddr, err)
	}

	keyPath := filepath.Join(workDir, "id_ed25519")
	issueCertFor(t, ctx, issuerURL, account, keyPath)

	// A CSV for the catalog's template, and a tarball carrying its own
	// job.sub, staged where the batch file can reach them.
	csvPath := filepath.Join(workDir, "rows.csv")
	if err := os.WriteFile(csvPath, []byte("sample\nalpha\nbeta\n"), 0o600); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	tarPath := filepath.Join(workDir, "own.tar")
	if err := os.WriteFile(tarPath, ownSubmissionTar(t), 0o600); err != nil {
		t.Fatalf("write tar: %v", err)
	}

	before := clusterIDs(t, htcondor.WithAuthenticatedUser(ctx, account), schedd)

	// One session doing what the docs tell somebody to do. Batch mode
	// so a failure is an exit code rather than a hang, and -v left off
	// so the transcript is the protocol's answers rather than OpenSSH's
	// own commentary.
	script := strings.Join([]string{
		"ls -l /",
		"ls -l /templates",
		"ls -l /templates/spooled-wrapper",
		"ls -l /submit",
		"get /submit/README " + filepath.Join(workDir, "drop-readme"),
		"put " + csvPath + " /templates/spooled-wrapper/submit/rows.csv",
		"put " + tarPath + " /submit/own.tar",
		"ls -l /templates/spooled-wrapper/status",
		"ls -l /submit/status",
		"get /templates/spooled-wrapper/status/rows.csv " + filepath.Join(workDir, "csv-status"),
		"get /submit/status/own.tar " + filepath.Join(workDir, "tar-status"),
		"",
	}, "\n")
	batchPath := filepath.Join(workDir, "batch.sftp")
	if err := os.WriteFile(batchPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write batch: %v", err)
	}

	out, sftpErr := runSFTP(t, ctx, gwPort, keyPath, batchPath)
	t.Logf("sftp exit=%v transcript:\n%s", sftpErr, out)
	if sftpErr != nil {
		harness.PrintScheddLog()
		t.Fatalf("the sftp session failed: %v", sftpErr)
	}

	// The tree the client saw. Asserted on the client's own rendering,
	// because a directory the server offers but the client cannot list
	// is not a directory anybody can use.
	for _, want := range []string{"outputs", "templates", "submit"} {
		if !strings.Contains(out, want) {
			t.Errorf("the root listing did not mention %q:\n%s", want, out)
		}
	}
	if readme := readFile(t, filepath.Join(workDir, "drop-readme")); !strings.Contains(readme, "job.sub") {
		t.Errorf("the drop README does not name job.sub:\n%s", readme)
	}

	// Both drops submitted. The status file is the service's own
	// account of what happened, which is the only thing a client ever
	// sees about a refusal.
	csvStatus := readFile(t, filepath.Join(workDir, "csv-status"))
	if !strings.Contains(csvStatus, "state: submitted") {
		t.Errorf("the CSV was not submitted:\n%s", csvStatus)
	}
	if !strings.Contains(csvStatus, "rows: 2") {
		t.Errorf("the CSV's row count is wrong:\n%s", csvStatus)
	}
	tarStatus := readFile(t, filepath.Join(workDir, "tar-status"))
	if !strings.Contains(tarStatus, "state: submitted") {
		t.Errorf("the tarball was not submitted:\n%s", tarStatus)
	}

	// Two submissions, so two new clusters, and neither still held for
	// input: a spool that never completed leaves HoldReasonCode 16,
	// which looks exactly like success from the client's side.
	authCtx := htcondor.WithAuthenticatedUser(ctx, account)
	fresh := newClusters(before, clusterIDs(t, authCtx, schedd))
	if len(fresh) != 2 {
		harness.PrintScheddLog()
		t.Fatalf("expected 2 new clusters, got %v", fresh)
	}
	// Sorted, because newClusters walks a map. The CSV went first in
	// the batch script, so it holds the lower id.
	sort.Ints(fresh)
	csvCluster, tarCluster := fresh[0], fresh[1]

	// The spool hold has to clear before the exit codes mean anything,
	// and it does not clear when the transfer returns: the schedd
	// receives the files on a worker thread and commits them -- renaming
	// the sandbox into place and releasing the job -- in a reaper
	// afterwards. So a SpoolJobFiles call that returned nil leaves every
	// proc on HoldReasonCode 16 for a moment, and waitAllExited treats
	// any hold as final. Reading one tick too early fails here with
	// "Spooling input data files", which looks exactly like the bug this
	// test exists to catch.
	waitSpoolReleased(t, authCtx, schedd, csvCluster, 2, time.Minute)
	waitSpoolReleased(t, authCtx, schedd, tarCluster, 1, time.Minute)

	// The exit codes are the real assertion in both cases: 17 is
	// reached only by running the script the template library spooled,
	// and 23 only by running the one that arrived inside the tarball.
	// A sandbox missing either leaves /bin/sh to exit 127.
	waitAllExited(t, authCtx, schedd, csvCluster, 2, 17)
	waitAllExited(t, authCtx, schedd, tarCluster, 1, 23)

	if mirrored {
		// If this ever fails, give the server a logger with
		// DestinationHTTP at VerbosityDebug and read it: the reason a
		// listing was not served from the mirror is recorded THERE and
		// nowhere else. FS.readableJobs logs it through Logf, which the
		// gateway wires to logger.Debug, so at default verbosity it is
		// not emitted at all -- and the client only ever prints
		// "Failure". The last cause found this way was the mirror
		// refusing the caller's IDTOKEN because it offered FS only.
		readJobAdOverSFTP(t, ctx, gwPort, keyPath, workDir, csvCluster)
		return
	}

	// What /outputs does on an access point with no htcondordb.
	//
	// The job directories come from the mirror, and the service answers
	// its absence by SAYING so rather than serving an empty directory --
	// "you have no jobs" and "this access point cannot tell you" are
	// different answers. Asserted because an empty listing would look
	// like success.
	outBatch := filepath.Join(workDir, "outputs.sftp")
	if err := os.WriteFile(outBatch, []byte("ls -l /outputs\n"), 0o600); err != nil {
		t.Fatalf("write batch: %v", err)
	}
	outOut, outErr := runSFTP(t, ctx, gwPort, keyPath, outBatch)
	t.Logf("sftp (outputs) exit=%v transcript:\n%s", outErr, outOut)
	if outErr == nil {
		t.Errorf("listing /outputs with no mirror succeeded; an empty directory here "+
			"would read as \"you have no finished jobs\":\n%s", outOut)
	}
	// Only that it failed, NOT what it said -- and that is a finding
	// rather than a weak assertion. ReadableJobs returns a sentence
	// naming the missing mirror, and this client never shows it:
	// OpenSSH renders a readdir failure from the status CODE alone
	// (fx2txt), so the user sees `remote readdir("/outputs/"):
	// Failure`. A pkg/sftp client does surface the message, which is
	// why no in-process test could have caught this.
	if !strings.Contains(outOut, "readdir") {
		t.Errorf("expected a readdir failure for /outputs:\n%s", outOut)
	}
}

// readJobAdOverSFTP fetches a finished job's ad and its output directory
// with the real client, from the restructured /outputs tree.
//
// Polled rather than read once: the mirror tails the schedd's log and
// advertises to the collector on an interval, so the job directory
// appears a little after the job finishes. A single attempt would be a
// race whose failure -- "not found" -- is also what a genuinely broken
// listing returns.
func readJobAdOverSFTP(t *testing.T, ctx context.Context, port, keyPath, workDir string, cluster int) {
	t.Helper()
	jobDir := fmt.Sprintf("/outputs/%d.0", cluster)
	adPath := filepath.Join(workDir, "job.ad")
	batch := filepath.Join(workDir, "ads.sftp")
	script := fmt.Sprintf("ls -l %s\nget %s/job.ad %s\n", jobDir, jobDir, adPath)
	if err := os.WriteFile(batch, []byte(script), 0o600); err != nil {
		t.Fatalf("write batch: %v", err)
	}

	var out string
	var err error
	for deadline := time.Now().Add(2 * time.Minute); ; {
		out, err = runSFTP(t, ctx, port, keyPath, batch)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never became readable; last transcript:\n%s", jobDir, out)
		}
		time.Sleep(3 * time.Second)
	}
	t.Logf("sftp (ads) transcript:\n%s", out)

	// The job directory holds the ad and a directory, and NOT data.tar:
	// listing that would cost a transfer of the whole sandbox, which is
	// the expense the data/ level exists to avoid.
	for _, want := range []string{sftpjobs.JobAdFileName, "data"} {
		if !strings.Contains(out, want) {
			t.Errorf("the job directory listing does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "data.tar") {
		t.Errorf("data.tar is listed; it is addressable but must not be listed:\n%s", out)
	}

	ad := readFile(t, adPath)
	if !strings.Contains(ad, fmt.Sprintf("ClusterId = %d", cluster)) {
		t.Errorf("the ad does not name cluster %d:\n%s", cluster, ad)
	}
	for _, attr := range []string{"Owner", "JobStatus"} {
		if !strings.Contains(ad, attr) {
			t.Errorf("the ad has no %s:\n%s", attr, ad)
		}
	}
}

// waitSpoolReleased waits for a spooling submission's input hold to
// clear, and fails on any other hold.
//
// HoldReasonCode 16 is the one hold that is expected here and the one
// that clears on its own, so it is the only one worth waiting through:
// every other code means the submission is broken, and waiting on those
// would turn a clear failure into a timeout.
func waitSpoolReleased(t *testing.T, ctx context.Context, schedd *htcondor.Schedd,
	cluster, procs int, within time.Duration) {
	t.Helper()
	constraint := fmt.Sprintf("ClusterId == %d", cluster)
	projection := []string{"ProcId", "JobStatus", "HoldReasonCode", "HoldReason"}

	for deadline := time.Now().Add(within); time.Now().Before(deadline); {
		ads, _, err := schedd.QueryWithOptions(ctx, constraint,
			&htcondor.QueryOptions{Projection: projection, Limit: -1})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		spooling := 0
		for _, ad := range ads {
			if status, _ := ad.EvaluateAttrInt("JobStatus"); status != 5 {
				continue
			}
			code, _ := ad.EvaluateAttrInt("HoldReasonCode")
			if code == 16 {
				spooling++
				continue
			}
			proc, _ := ad.EvaluateAttrInt("ProcId")
			reason, _ := ad.EvaluateAttrString("HoldReason")
			t.Fatalf("%d.%d went on hold (%d): %s", cluster, proc, code, reason)
		}
		if len(ads) == procs && spooling == 0 {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("cluster %d is still held for input %s after its spool returned", cluster, within)
}

// runSFTP forks OpenSSH's client against the gateway.
//
// Every option here exists to keep the run from depending on the
// machine it runs on: no user config, no known_hosts, no agent, no
// password prompt, and only the certificate this test issued.
func runSFTP(t *testing.T, ctx context.Context, port, keyPath, batchPath string) (string, error) {
	t.Helper()
	//nolint:gosec // fixed argv; the binary is resolved by LookPath in the caller
	cmd := exec.CommandContext(ctx, "sftp",
		"-F", "/dev/null",
		"-P", port,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "BatchMode=yes",
		"-o", "PreferredAuthentications=publickey",
		"-o", "LogLevel=ERROR",
		"-o", "User=+jobs",
		"-i", keyPath,
		"-b", batchPath,
		"127.0.0.1",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// issueCertFor mints a certificate for a fresh key from the real
// endpoint, leaving both where ssh(1) looks for them.
func issueCertFor(t *testing.T, ctx context.Context, issuerURL, account, keyPath string) {
	t.Helper()
	pubLine := writeUserKey(t, keyPath)

	body, err := json.Marshal(map[string]string{"public_key": pubLine})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		issuerURL+"/api/v1/ssh/certificate", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("certificate request: %v", err)
	}
	req.Header.Set("X-Test-User", account)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("certificate endpoint returned %s: %s", resp.Status, raw)
	}
	var issued struct {
		Certificate string `json:"certificate"`
		Principal   string `json:"principal"`
	}
	if err := json.Unmarshal(raw, &issued); err != nil {
		t.Fatalf("decode certificate: %v", err)
	}
	if issued.Principal != account {
		t.Fatalf("certificate principal = %q, want %q", issued.Principal, account)
	}
	if err := os.WriteFile(keyPath+"-cert.pub", []byte(issued.Certificate+"\n"), 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
}

// stageTemplateCatalog writes an operator catalog holding the wrapper
// template, so the templates tree the client walks is the real library
// loaded from a real file rather than one injected into a struct.
func stageTemplateCatalog(t *testing.T, dir string) string {
	t.Helper()
	doc := struct {
		Templates []templates.Template `yaml:"templates"`
	}{Templates: []templates.Template{wrapperTemplateIntegration()}}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	path := filepath.Join(dir, "templates.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return path
}

// ownSubmissionTar is a submission that describes itself: a job.sub and
// the script it names, which is the whole point of the drop point.
//
// Exit 23 is the assertion, reached only by running the script that
// came in the tarball under the name job.sub asked for.
func ownSubmissionTar(t *testing.T) []byte {
	t.Helper()
	return tarArchive(t, map[string]string{
		"job.sub": "universe = vanilla\n" +
			"executable = /bin/sh\n" +
			"transfer_executable = false\n" +
			"transfer_input_files = own.sh\n" +
			"arguments = own.sh\n" +
			"output = own.out\n" +
			"error = own.err\n" +
			"request_cpus = 1\n" +
			"request_memory = 64\n" +
			"request_disk = 64\n" +
			"queue\n",
		"own.sh": "#!/bin/sh\necho dropped-over-sftp\nexit 23\n",
	})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	//nolint:gosec // paths are this test's own t.TempDir() files
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
