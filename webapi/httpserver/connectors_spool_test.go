package httpserver

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/PelicanPlatform/classad/classad"

	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// Merging a job's connector files into its upload.
//
// The property throughout: a connector file and the user's upload
// reach the spool in ONE stream, because SpoolJobFilesFromTar releases
// the job when it finishes and a file left out is a job running with a
// partial sandbox.

func adWithRefs(t *testing.T, refs string) *classad.ClassAd {
	t.Helper()
	ad := classad.New()
	if refs != "" {
		if err := ad.Set(connectors.InputAttribute, refs); err != nil {
			t.Fatal(err)
		}
	}
	return ad
}

func userTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func entriesOf(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("reading the merged archive: %v", err)
		}
		body, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = string(body)
	}
}

// --- the submit half --------------------------------------------------

func TestPrepareRewritesAndRecords(t *testing.T) {
	h := &Handler{}
	in := strings.Join([]string{
		"executable = run.sh",
		"transfer_input_files = local.dat, webapi-builtin-gdrive+gdrive2://FILEID/data.csv",
		"queue",
	}, "\n")

	out, err := h.prepareConnectorInputs(in)
	if err != nil {
		t.Fatalf("prepareConnectorInputs: %v", err)
	}

	// Left in place, the reference becomes a Requirements clause
	// nothing satisfies and the job never matches anything.
	if strings.Contains(out, "connector://") && !strings.Contains(out, "+ConnectorInputs") {
		t.Error("the reference survived in transfer_input_files")
	}
	if !strings.Contains(out, "transfer_input_files = local.dat, data.csv") {
		t.Errorf("rewritten submit file:\n%s", out)
	}
	if !strings.Contains(out, "+"+connectors.InputAttribute) {
		t.Errorf("the refs were not recorded on the job:\n%s", out)
	}
}

func TestPrepareLeavesAnOrdinarySubmitAlone(t *testing.T) {
	h := &Handler{}
	in := "executable = run.sh\ntransfer_input_files = a.dat\nqueue\n"
	out, err := h.prepareConnectorInputs(in)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Errorf("an ordinary submit file was modified:\n%s", out)
	}
}

// Spool mode cannot serve an output destination: that needs a plugin on
// the execute node. Refusing beats leaving the URL in and letting the
// job never match.
func TestPrepareRefusesAConnectorOutput(t *testing.T) {
	h := &Handler{}
	in := strings.Join([]string{
		"transfer_input_files = svc+gdrive2://A/in.csv",
		`transfer_output_remaps = "r.dat = svc+gdrive2://FOLDER/r.dat"`,
		"queue",
	}, "\n")

	if _, err := h.prepareConnectorInputs(in); err == nil {
		t.Fatal("a connector output destination was accepted in spool mode")
	}
}

// --- reading the refs back --------------------------------------------

func TestConnectorRefsOfReadsTheAttribute(t *testing.T) {
	ad := adWithRefs(t, "svc+gdrive2://A/a.csv svc+gdrive2://B/b.csv")
	refs, err := connectorRefsOf(ad)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].FileID != "A" || refs[1].Name != "b.csv" {
		t.Errorf("refs = %+v", refs)
	}
}

func TestConnectorRefsOfIsEmptyForAnOrdinaryJob(t *testing.T) {
	refs, err := connectorRefsOf(adWithRefs(t, ""))
	if err != nil || len(refs) != 0 {
		t.Errorf("refs = %+v, err = %v", refs, err)
	}
	if refs, err := connectorRefsOf(nil); err != nil || refs != nil {
		t.Errorf("a nil ad gave refs = %+v, err = %v", refs, err)
	}
}

func TestAnUnreadableAttributeIsAnError(t *testing.T) {
	if _, err := connectorRefsOf(adWithRefs(t, "not-a-reference")); err == nil {
		t.Fatal("an unreadable ConnectorInputs was accepted")
	}
}

// --- the upload half --------------------------------------------------

// A job with no connector files gets its upload back untouched, with no
// pipe and no goroutine in the way.
func TestUploadReaderIsAPassthroughForAnOrdinaryJob(t *testing.T) {
	h := &Handler{}
	upload := bytes.NewReader(userTar(t, map[string]string{"run.sh": "x"}))

	got, err := h.connectorUploadReader(t.Context(), nil, adWithRefs(t, ""), upload)
	if err != nil {
		t.Fatal(err)
	}
	if got != io.Reader(upload) {
		t.Error("an ordinary job's upload was wrapped")
	}
}

// The whole point: both halves reach the spool in one stream.
func TestUploadReaderMergesTheConnectorFile(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")
	f.connectAs(t, c)
	f.h.connectorDriveBaseURL = f.google.DriveBaseURL()
	f.google.PickFile("FILEID", "data.csv", []byte("a,b,c\n"))

	req := f.authedRequest(t, c)
	ad := adWithRefs(t, "webapi-builtin-gdrive+gdrive2://FILEID/data.csv")
	upload := bytes.NewReader(userTar(t, map[string]string{"run.sh": "#!/bin/sh\n"}))

	merged, err := f.h.connectorUploadReader(t.Context(), req, ad, upload)
	if err != nil {
		t.Fatalf("connectorUploadReader: %v", err)
	}

	got := entriesOf(t, merged)
	if got["run.sh"] != "#!/bin/sh\n" {
		t.Errorf("the upload was lost: %v", got)
	}
	if got["data.csv"] != "a,b,c\n" {
		t.Errorf("the connector file did not reach the spool: %v", got)
	}
}

// A user removed from the group between submit and upload gets no
// files. Membership is checked at USE, against current membership --
// that is the half that makes removing somebody take something away.
func TestUploadReaderChecksGroupsAtUseNotAtSubmit(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")
	f.connectAs(t, c)
	f.h.connectorDriveBaseURL = f.google.DriveBaseURL()
	f.google.PickFile("FILEID", "data.csv", []byte("a,b,c\n"))

	// Same user, now outside the group.
	out := f.login(t, "alice", "other_lab")
	req := f.authedRequest(t, out)

	ad := adWithRefs(t, "webapi-builtin-gdrive+gdrive2://FILEID/data.csv")
	_, err := f.h.connectorUploadReader(t.Context(), req,
		ad, bytes.NewReader(userTar(t, nil)))
	if err == nil {
		t.Fatal("a user outside the connector's groups was served its files")
	}
	if !strings.Contains(err.Error(), "not available to you") {
		t.Errorf("err = %v", err)
	}
}

func TestUploadReaderReportsAnUnknownConnector(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")
	req := f.authedRequest(t, c)

	ad := adWithRefs(t, "nosuch-service+gdrive2://FILEID/data.csv")
	_, err := f.h.connectorUploadReader(t.Context(), req, ad,
		bytes.NewReader(userTar(t, nil)))
	if err == nil {
		t.Fatal("a reference to an unconfigured connector was accepted")
	}
	if !strings.Contains(err.Error(), "does not configure") {
		t.Errorf("err = %v", err)
	}
}

func TestUploadReaderReportsAnUnconnectedUser(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab") // never connected
	req := f.authedRequest(t, c)

	ad := adWithRefs(t, "webapi-builtin-gdrive+gdrive2://FILEID/data.csv")
	_, err := f.h.connectorUploadReader(t.Context(), req, ad,
		bytes.NewReader(userTar(t, nil)))
	if err == nil {
		t.Fatal("a user who never connected was served files")
	}
	if !strings.Contains(err.Error(), "/api/v1/connect/") {
		t.Errorf("err = %q, want it to say where to connect", err)
	}
}

// A file the user never picked fails the upload rather than producing a
// spool that is missing it.
func TestUploadReaderFailsOnAFileThatWasNeverPicked(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")
	f.connectAs(t, c)
	f.h.connectorDriveBaseURL = f.google.DriveBaseURL()
	// Deliberately not picked.

	req := f.authedRequest(t, c)
	ad := adWithRefs(t, "webapi-builtin-gdrive+gdrive2://NEVERPICKED/data.csv")
	_, err := f.h.connectorUploadReader(t.Context(), req, ad,
		bytes.NewReader(userTar(t, nil)))
	if err == nil {
		t.Fatal("a file that was never picked produced a reader anyway")
	}
	if !strings.Contains(err.Error(), "picks") {
		t.Errorf("err = %q, want the drive.file explanation", err)
	}
}

// Using a connector's files counts as use, same as the Picker token.
func TestSpoolingRecordsConnectorUse(t *testing.T) {
	f := newConnectFixture(t, "smith_lab")
	c := f.login(t, "alice", "smith_lab")
	f.connectAs(t, c)
	f.h.connectorDriveBaseURL = f.google.DriveBaseURL()
	f.google.PickFile("FILEID", "data.csv", []byte("x"))

	id := connectors.MustParse("builtin:gdrive")
	req := f.authedRequest(t, c)
	ad := adWithRefs(t, "webapi-builtin-gdrive+gdrive2://FILEID/data.csv")
	merged, err := f.h.connectorUploadReader(t.Context(), req, ad,
		bytes.NewReader(userTar(t, nil)))
	if err != nil {
		t.Fatal(err)
	}
	_ = entriesOf(t, merged)

	if _, lastUsed, _ := f.h.connectorStore().CountCredentials(t.Context(), id); lastUsed.IsZero() {
		t.Error("spooling a connector file did not count as use")
	}
}
