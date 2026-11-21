package connectors

import (
	"strings"
	"testing"
)

func TestParseInputURL(t *testing.T) {
	tests := []struct {
		raw     string
		want    InputRef
		wantErr string
	}{
		{
			raw:  "connector://webapi_builtin_gdrive/FILEID/data.csv",
			want: InputRef{Service: "webapi_builtin_gdrive", FileID: "FILEID", Name: "data.csv"},
		},
		{
			raw:  "connector://api2_site_cryoem/1a2b-3c/run.tar.gz",
			want: InputRef{Service: "api2_site_cryoem", FileID: "1a2b-3c", Name: "run.tar.gz"},
		},

		{raw: "connector://svc/FILEID", wantErr: "does not name a file"},
		{raw: "connector://svc/FILEID/", wantErr: "does not name a file"},
		{raw: "connector://svc/a/b/c.txt", wantErr: "does not name a file"},
		{raw: "connector:///FILEID/data.csv", wantErr: "names no connector"},
		{raw: "gdrive://svc/FILEID/data.csv", wantErr: "not a connector:// reference"},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseInputURL(tc.raw)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseInputURL(%q) = %+v, want an error", tc.raw, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseInputURL(%q): %v", tc.raw, err)
			}
			tc.want.Raw = tc.raw
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The service name has no colon in it, which is the reason the
// connector id is not used here directly: url.Parse reads what follows
// a colon in an authority as a port.
func TestAColonInTheAuthorityWouldNotSurviveParsing(t *testing.T) {
	if _, err := ParseInputURL("connector://builtin:gdrive/FILEID/data.csv"); err == nil {
		t.Fatal("an id with a colon parsed; the service name is used precisely because it has none")
	}
}

// --- splitting --------------------------------------------------------

func TestSplitRewritesTheReferenceToItsName(t *testing.T) {
	in := strings.Join([]string{
		"executable = run.sh",
		"transfer_input_files = connector://webapi_builtin_gdrive/FILEID/data.csv",
		"queue",
	}, "\n")

	out, refs, err := SplitSpoolInputs(in)
	if err != nil {
		t.Fatalf("SplitSpoolInputs: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %+v, want one", refs)
	}
	if refs[0].FileID != "FILEID" || refs[0].Name != "data.csv" {
		t.Errorf("ref = %+v", refs[0])
	}
	if !strings.Contains(out, "transfer_input_files = data.csv") {
		t.Errorf("rewritten file:\n%s", out)
	}
	// Nothing advertises a `connector` transfer method, so a reference
	// left in place becomes a Requirements clause nothing satisfies and
	// the job never matches.
	if strings.Contains(out, "connector://") {
		t.Error("a connector reference survived the rewrite")
	}
}

// The rest of the file is untouched. Replacing one token in place keeps
// comments, continuations and macros as the submitter wrote them, where
// regenerating from a parse would not.
func TestSplitLeavesTheRestOfTheFileAlone(t *testing.T) {
	in := strings.Join([]string{
		"# a comment nobody should lose",
		"MYMACRO = something",
		"executable = run.sh",
		"arguments = --in $(MYMACRO)",
		"transfer_input_files = local.dat, connector://svc/ID/remote.csv, other.dat",
		"+WantSomething = true",
		"queue 5",
	}, "\n")

	out, refs, err := SplitSpoolInputs(in)
	if err != nil {
		t.Fatalf("SplitSpoolInputs: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %+v", refs)
	}
	want := strings.Replace(in, "connector://svc/ID/remote.csv", "remote.csv", 1)
	if out != want {
		t.Errorf("rewritten file differs by more than the reference:\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

func TestSplitHandlesSeveralOnOneLine(t *testing.T) {
	in := "transfer_input_files = connector://svc/A/a.csv, connector://svc/B/b.csv\nqueue\n"

	out, refs, err := SplitSpoolInputs(in)
	if err != nil {
		t.Fatalf("SplitSpoolInputs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want two", refs)
	}
	if !strings.Contains(out, "a.csv, b.csv") {
		t.Errorf("rewritten: %q", out)
	}
}

func TestSplitLeavesAFileWithNoReferencesUnchanged(t *testing.T) {
	in := "executable = run.sh\ntransfer_input_files = a.dat\nqueue\n"
	out, refs, err := SplitSpoolInputs(in)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Error("a file with no connector references was modified")
	}
	if refs != nil {
		t.Errorf("refs = %+v, want none", refs)
	}
}

// The same file named twice is one fetch.
func TestSplitDedupesTheSameFile(t *testing.T) {
	in := "transfer_input_files = connector://svc/A/a.csv, connector://svc/A/a.csv\nqueue\n"
	_, refs, err := SplitSpoolInputs(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Errorf("refs = %+v, want one fetch for one file", refs)
	}
}

// --- collisions -------------------------------------------------------

// Two different connector files landing on one sandbox name means one
// overwrites the other, and which wins is whichever is fetched last. A
// job that silently runs on the wrong input is worse than one that does
// not start.
func TestTwoConnectorFilesCannotShareAName(t *testing.T) {
	in := "transfer_input_files = connector://svc/A/data.csv, connector://svc/B/data.csv\nqueue\n"

	_, _, err := SplitSpoolInputs(in)
	if err == nil {
		t.Fatal("two different files sharing a name were accepted")
	}
	if !strings.Contains(err.Error(), "data.csv") {
		t.Errorf("error = %q, want it to name the file", err)
	}
}

// The same collision arriving from the other direction.
func TestAConnectorFileCannotShadowAnOrdinaryInput(t *testing.T) {
	in := "transfer_input_files = data.csv, connector://svc/A/data.csv\nqueue\n"

	_, _, err := SplitSpoolInputs(in)
	if err == nil {
		t.Fatal("a connector file was allowed to shadow an ordinary input of the same name")
	}
	if !strings.Contains(err.Error(), "ordinary input file") {
		t.Errorf("error = %q", err)
	}
}

func TestACollisionIsRefusedNotRenamed(t *testing.T) {
	// A generated name would be one the submit file does not mention,
	// so the job would look for an input that is not there.
	in := "transfer_input_files = connector://svc/A/x.csv, connector://svc/B/x.csv\nqueue\n"
	out, refs, err := SplitSpoolInputs(in)
	if err == nil {
		t.Fatalf("accepted a collision, rewriting to %q with refs %+v", out, refs)
	}
	if out != "" || refs != nil {
		t.Error("a refused submission returned a partial rewrite")
	}
}

func TestAMalformedReferenceRefusesTheWholeSubmission(t *testing.T) {
	in := "transfer_input_files = connector://svc/NOFILENAME\nqueue\n"
	if _, _, err := SplitSpoolInputs(in); err == nil {
		t.Fatal("a malformed reference was accepted")
	}
}

// --- output -----------------------------------------------------------

// Spool mode cannot serve an output destination: that needs a transfer
// plugin on the execute node, which is direct mode. Saying so beats
// leaving the URL in place, where it becomes a Requirements clause
// nothing satisfies and the job never matches.
func TestOutputRefsAreFound(t *testing.T) {
	in := strings.Join([]string{
		"transfer_input_files = connector://svc/A/in.csv",
		`transfer_output_remaps = "result.dat = connector://svc/FOLDER/result.dat"`,
		"queue",
	}, "\n")

	got := OutputRefs(in)
	if len(got) != 1 {
		t.Fatalf("OutputRefs = %v, want the one in the remap", got)
	}
	if !strings.Contains(got[0], "FOLDER") {
		t.Errorf("OutputRefs = %v", got)
	}
}

func TestOutputRefsIgnoresInputs(t *testing.T) {
	in := "transfer_input_files = connector://svc/A/in.csv\nqueue\n"
	if got := OutputRefs(in); len(got) != 0 {
		t.Errorf("OutputRefs = %v, want none; an input is not an output", got)
	}
}

func TestOutputRefsFindsOutputDestination(t *testing.T) {
	in := "output_destination = connector://svc/FOLDER/out\nqueue\n"
	if got := OutputRefs(in); len(got) != 1 {
		t.Errorf("OutputRefs = %v, want the output_destination", got)
	}
}
