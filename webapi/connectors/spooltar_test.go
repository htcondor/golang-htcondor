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
		Name:      name,
		Size:      int64(len(body)),
		SizeKnown: true,
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
		Name:      "truncated.csv",
		Size:      100, // claimed
		SizeKnown: true,
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
		Name:      "gone.csv",
		Size:      4,
		SizeKnown: true,
		Open:      func() (io.ReadCloser, error) { return nil, io.ErrUnexpectedEOF },
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
	in := "transfer_input_files = svc+gdrive2://A/a.csv, svc+gdrive2://B/b.csv\nqueue\n"
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
	_, refs, err := SplitSpoolInputs("transfer_input_files = svc+gdrive2://A/a.csv\nqueue\n")
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

// --- the pipe discipline ----------------------------------------------

// A failure must reach the reader AS a failure.
//
// tar.Writer.Close appends a valid end-of-archive marker to whatever
// has been written, so a failure followed by a plain close hands the
// reader a well-formed archive holding a PREFIX of the files. The
// schedd accepts that and releases the jobs, and the error the handler
// reports afterwards arrives too late -- every proc of the cluster is
// already running with a partial sandbox. This repository has paid for
// that once; see writeMultipartTar.
func TestPipeFailureReachesTheReaderAsAFailure(t *testing.T) {
	user := makeTar(t, map[string]string{"run.sh": "#!/bin/sh\n"})
	failing := SpoolFile{
		Name: "never.csv", Size: 4, SizeKnown: true,
		Open: func() (io.ReadCloser, error) { return nil, io.ErrUnexpectedEOF },
	}

	pr, pw := io.Pipe()
	go func() {
		_ = MergeSpoolTarToPipe(pw, bytes.NewReader(user), []SpoolFile{failing})
	}()

	// Read it the way the schedd would.
	tr := tar.NewReader(pr)
	var readErr error
	for {
		_, err := tr.Next()
		if err != nil {
			readErr = err
			break
		}
		// Bounded, so a mutation that produced an endless stream
		// would fail the test rather than hang it.
		if _, err := io.Copy(io.Discard, io.LimitReader(tr, 1<<20)); err != nil {
			readErr = err
			break
		}
	}

	if readErr == nil || errors.Is(readErr, io.EOF) {
		t.Fatalf("the reader saw %v -- a clean end of archive. It would accept a "+
			"partial sandbox and release the jobs.", readErr)
	}
}

// And a success closes cleanly, so the reader sees a whole archive.
func TestPipeSuccessClosesCleanly(t *testing.T) {
	user := makeTar(t, map[string]string{"run.sh": "#!/bin/sh\n"})

	pr, pw := io.Pipe()
	go func() {
		_ = MergeSpoolTarToPipe(pw, bytes.NewReader(user), []SpoolFile{
			spoolFile("remote.csv", "a,b,c\n"),
		})
	}()

	got := map[string]string{}
	tr := tar.NewReader(pr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("reading: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		got[hdr.Name] = string(body)
	}
	if len(got) != 2 || got["remote.csv"] != "a,b,c\n" || got["run.sh"] != "#!/bin/sh\n" {
		t.Errorf("merged = %v", got)
	}
}

// --- streaming --------------------------------------------------------

// Nothing is read into memory before it is written out. A connector
// file arrives as a stream and leaves as one; the whole point of the
// spool path is that a large input never lands on this server's disk
// or in its heap.
func TestMergeStreamsRatherThanBuffering(t *testing.T) {
	const size = 4 << 20 // bigger than any sane buffer the code might hold

	var peak int64
	counting := &countingReader{r: repeating('x', size), peak: &peak}

	big := SpoolFile{
		Name: "big.dat", Size: size, SizeKnown: true,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(counting), nil },
	}

	// A writer that counts how much has been written by the time each
	// read happens. If the merge buffered the file, every byte would be
	// read before any was written.
	var written int64
	w := &countingWriter{written: &written, onWrite: func() {
		counting.writtenSoFar = &written
	}}

	if err := MergeSpoolTar(w, bytes.NewReader(makeTar(t, nil)), []SpoolFile{big}); err != nil {
		t.Fatal(err)
	}

	if counting.maxUnwritten >= size {
		t.Errorf("the whole file (%d bytes) was read before anything was written; "+
			"this path must stream", size)
	}
	if written < size {
		t.Errorf("wrote %d bytes of a %d byte file", written, size)
	}
}

type countingReader struct {
	r            io.Reader
	read         int64
	writtenSoFar *int64
	maxUnwritten int64
	peak         *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	var w int64
	if c.writtenSoFar != nil {
		w = *c.writtenSoFar
	}
	if gap := c.read - w; gap > c.maxUnwritten {
		c.maxUnwritten = gap
	}
	return n, err
}

type countingWriter struct {
	written *int64
	onWrite func()
}

func (c *countingWriter) Write(p []byte) (int, error) {
	*c.written += int64(len(p))
	if c.onWrite != nil {
		c.onWrite()
	}
	return len(p), nil
}

func repeating(b byte, n int) io.Reader {
	return io.LimitReader(byteRepeater{b}, int64(n))
}

type byteRepeater struct{ b byte }

func (r byteRepeater) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

// --- unknown size -----------------------------------------------------

// Drive reports no size for its own document types. tar needs the size
// before the body, so such a file cannot be streamed into one -- and
// the alternatives are buffering it, which this path exists to avoid,
// or writing a header that lies.
func TestAFileWithNoReportedSizeIsRefusedWithADiagnosis(t *testing.T) {
	unknown := SpoolFile{
		Name: "notes", Size: 0, SizeKnown: false,
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("content after all")), nil
		},
	}
	var out bytes.Buffer
	err := MergeSpoolTar(&out, bytes.NewReader(makeTar(t, nil)), []SpoolFile{unknown})
	if err == nil {
		t.Fatal("a file with no reported size produced an archive")
	}
	// "archive/tar: write too long" is what this used to say, which
	// names neither the cause nor the remedy.
	if !strings.Contains(err.Error(), "Docs") {
		t.Errorf("error = %q, want it to name the likely cause", err)
	}
}

// A genuinely empty file is not the same as one of unknown size.
func TestAnEmptyFileWithAKnownSizeIsFine(t *testing.T) {
	empty := SpoolFile{
		Name: "empty.txt", Size: 0, SizeKnown: true,
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		},
	}
	var out bytes.Buffer
	if err := MergeSpoolTar(&out, bytes.NewReader(makeTar(t, nil)), []SpoolFile{empty}); err != nil {
		t.Fatalf("an empty file was refused: %v", err)
	}
	if got := readTar(t, out.Bytes()); got["empty.txt"] != "" {
		t.Errorf("merged = %v", got)
	}
}
