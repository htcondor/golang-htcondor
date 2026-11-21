package connectors

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func makeTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	for _, name := range names {
		body := files[name]
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

func readTar(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("reading the merged archive: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = string(body)
	}
}

func spoolFile(name, body string) SpoolFile {
	return SpoolFile{
		Name: name,
		Size: int64(len(body)),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(body)), nil
		},
	}
}

// Both halves end up in one archive. Two tars cannot be concatenated --
// each has its own trailer -- so the user's entries are copied across.
func TestMergeCarriesBothHalves(t *testing.T) {
	user := makeTar(t, map[string]string{"run.sh": "#!/bin/sh\n", "local.dat": "local"})

	var out bytes.Buffer
	err := MergeSpoolTar(&out, bytes.NewReader(user), []SpoolFile{
		spoolFile("remote.csv", "a,b,c\n"),
	})
	if err != nil {
		t.Fatalf("MergeSpoolTar: %v", err)
	}

	got := readTar(t, out.Bytes())
	want := map[string]string{
		"run.sh":     "#!/bin/sh\n",
		"local.dat":  "local",
		"remote.csv": "a,b,c\n",
	}
	if len(got) != len(want) {
		t.Fatalf("merged archive holds %v, want %v", got, want)
	}
	for name, body := range want {
		if got[name] != body {
			t.Errorf("%s = %q, want %q", name, got[name], body)
		}
	}
}

func TestMergeWithNoExtrasIsAPassthrough(t *testing.T) {
	user := makeTar(t, map[string]string{"run.sh": "x"})
	var out bytes.Buffer
	if err := MergeSpoolTar(&out, bytes.NewReader(user), nil); err != nil {
		t.Fatal(err)
	}
	if got := readTar(t, out.Bytes()); len(got) != 1 || got["run.sh"] != "x" {
		t.Errorf("merged = %v", got)
	}
}

func TestMergeWithNoUploadStillSpoolsTheConnectorFiles(t *testing.T) {
	// A job whose only inputs are connector files uploads an empty
	// archive, and the spool still has to contain them.
	empty := makeTar(t, nil)
	var out bytes.Buffer
	if err := MergeSpoolTar(&out, bytes.NewReader(empty), []SpoolFile{
		spoolFile("remote.csv", "data"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := readTar(t, out.Bytes()); got["remote.csv"] != "data" {
		t.Errorf("merged = %v", got)
	}
}

// A name in both halves is an error, not an overwrite: whichever is
// written last would win, and a job running on the wrong input is worse
// than one that does not start.
func TestMergeRefusesAnUploadedNameThatCollides(t *testing.T) {
	user := makeTar(t, map[string]string{"data.csv": "the user's"})

	var out bytes.Buffer
	err := MergeSpoolTar(&out, bytes.NewReader(user), []SpoolFile{
		spoolFile("data.csv", "the connector's"),
	})
	if err == nil {
		t.Fatal("a collision between an upload and a connector file was accepted")
	}
	if !strings.Contains(err.Error(), "data.csv") {
		t.Errorf("error = %q, want it to name the file", err)
	}
}

func TestMergeRefusesTwoExtrasWithOneName(t *testing.T) {
	var out bytes.Buffer
	err := MergeSpoolTar(&out, bytes.NewReader(makeTar(t, nil)), []SpoolFile{
		spoolFile("x.csv", "one"),
		spoolFile("x.csv", "two"),
	})
	if err == nil {
		t.Fatal("two connector files with one name were accepted")
	}
}

// tar declares a size up front, so a provider sending fewer bytes than
// it advertised would otherwise produce an entry that looks complete
// and is not.
func TestMergeCatchesAShortProvider(t *testing.T) {
	short := SpoolFile{
		Name: "truncated.csv",
		Size: 100, // claimed
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("only a little")), nil
		},
	}

	var out bytes.Buffer
	err := MergeSpoolTar(&out, bytes.NewReader(makeTar(t, nil)), []SpoolFile{short})
	if err == nil {
		t.Fatal("a short read produced an archive instead of an error")
	}
	if !strings.Contains(err.Error(), "declared") {
		t.Errorf("error = %q, want it to say the size did not match", err)
	}
}

func TestMergeReportsAFetchThatFails(t *testing.T) {
	failing := SpoolFile{
		Name: "gone.csv",
		Size: 4,
		Open: func() (io.ReadCloser, error) { return nil, io.ErrUnexpectedEOF },
	}
	var out bytes.Buffer
	err := MergeSpoolTar(&out, bytes.NewReader(makeTar(t, nil)), []SpoolFile{failing})
	if err == nil {
		t.Fatal("a failed fetch produced an archive")
	}
	if !strings.Contains(err.Error(), "gone.csv") {
		t.Errorf("error = %q, want it to name the file", err)
	}
}

// One submission produces one archive, whatever order the fetches
// finished in.
func TestMergeOrdersExtrasDeterministically(t *testing.T) {
	first := order(t, []SpoolFile{spoolFile("b.csv", "b"), spoolFile("a.csv", "a")})
	second := order(t, []SpoolFile{spoolFile("a.csv", "a"), spoolFile("b.csv", "b")})

	if first != second {
		t.Errorf("entry order depends on fetch order: %v vs %v", first, second)
	}
}

func order(t *testing.T, extra []SpoolFile) string {
	t.Helper()
	var out bytes.Buffer
	if err := MergeSpoolTar(&out, bytes.NewReader(makeTar(t, nil)), extra); err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(bytes.NewReader(out.Bytes()))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
	return strings.Join(names, ",")
}

// --- the attribute ----------------------------------------------------

func TestInputRefsRoundTripThroughTheJobAttribute(t *testing.T) {
	in := "transfer_input_files = connector://svc/A/a.csv, connector://svc/B/b.csv\nqueue\n"
	_, refs, err := SplitSpoolInputs(in)
	if err != nil {
		t.Fatal(err)
	}

	encoded := EncodeInputRefs(refs)
	back, err := DecodeInputRefs(encoded)
	if err != nil {
		t.Fatalf("DecodeInputRefs(%q): %v", encoded, err)
	}
	if len(back) != len(refs) {
		t.Fatalf("decoded %d refs from %d", len(back), len(refs))
	}
	for i := range refs {
		if back[i] != refs[i] {
			t.Errorf("[%d] = %+v, want %+v", i, back[i], refs[i])
		}
	}
}

func TestRecordInputRefsAddsTheAttribute(t *testing.T) {
	_, refs, err := SplitSpoolInputs("transfer_input_files = connector://svc/A/a.csv\nqueue\n")
	if err != nil {
		t.Fatal(err)
	}
	out := RecordInputRefs("executable = run.sh\nqueue\n", refs)

	if !strings.Contains(out, "+"+InputAttribute+" =") {
		t.Fatalf("the attribute is missing:\n%s", out)
	}
	// Before any queue statement, so it applies to every one of them.
	if strings.Index(out, "+"+InputAttribute) > strings.Index(out, "queue") {
		t.Error("the attribute was placed after a queue statement")
	}
	if !strings.Contains(out, "executable = run.sh") {
		t.Error("the original submit file was lost")
	}
}

func TestRecordInputRefsWithNoneChangesNothing(t *testing.T) {
	in := "executable = run.sh\nqueue\n"
	if got := RecordInputRefs(in, nil); got != in {
		t.Errorf("a file with no refs was modified: %q", got)
	}
}
