package htcondor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMatchingIsNotReadAsAFile: which of `matching` and `from <file>` a
// statement is comes from the statement, not from whether the text
// happens to contain a glob metacharacter.
//
// `queue matching results` names a pattern with no metacharacter. It
// used to be opened and read as an itemdata file, so the job count
// became that file's line count and the loop variable took the file's
// contents instead of each matched name -- a submission about something
// else entirely, with nothing failing to say so.
func TestMatchingIsNotReadAsAFile(t *testing.T) {
	dir := t.TempDir()
	// A file whose NAME is the bare pattern, and whose contents would be
	// read as three items if the form were misread.
	if err := os.WriteFile(filepath.Join(dir, "results"),
		[]byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// And two files the pattern should actually match.
	for _, name := range []string{"results.a", "results.b"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	sf, err := ParseSubmitFileWithOptions(strings.NewReader(
		"executable = /bin/sleep\narguments = $(Item)\nqueue matching results\n"),
		SubmitParseOptions{AllowLocalAccess: true})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := sf.Submit(1)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// The bare pattern matches exactly the one file named "results", so
	// one job -- not the three lines inside it.
	if len(res.ProcAds) == 3 {
		t.Fatalf("the pattern was read as an itemdata file: %d jobs, one per line", len(res.ProcAds))
	}
	if len(res.ProcAds) != 1 {
		t.Fatalf("queued %d jobs, want 1 (the one name the pattern matches)", len(res.ProcAds))
	}
	expr, ok := res.ProcAds[0].Lookup("Args")
	if !ok {
		t.Fatal("no Args")
	}
	args, err := expr.Eval(nil).StringValue()
	if err != nil {
		t.Fatalf("Args is not a string: %v", err)
	}
	if args == "one" {
		t.Error("the loop variable took the file's first LINE, so the pattern was read as a file")
	}
	if args != "results" {
		t.Errorf("Args = %q, want the matched name %q", args, "results")
	}
}

// TestFromFileCutsColumnsLikeCondorSubmit: a `from <file>` row is cut by
// the same splitter as an `in` list, which is a port of
// SubmitForeachArgs::split_item.
//
// fileIterator had a splitter of its own, and it disagreed on ordinary
// rows: commas and whitespace are separators in the same pass, and the
// last variable takes everything left over rather than one field with
// the remainder discarded.
func TestFromFileCutsColumnsLikeCondorSubmit(t *testing.T) {
	for _, tc := range []struct {
		name string
		vars string
		row  string
		want map[string]string
	}{
		{
			// Both separators in one pass: three columns, not two.
			name: "a comma and a space are both separators",
			vars: "a, b, c",
			row:  "1, 2 3",
			want: map[string]string{"a": "1", "b": "2", "c": "3"},
		},
		{
			// The last variable keeps the rest, separators included.
			name: "the last variable takes the remainder",
			vars: "a, b",
			row:  "1 2 3 4",
			want: map[string]string{"a": "1", "b": "2 3 4"},
		},
		{
			name: "missing columns are empty, not absent",
			vars: "a, b, c",
			row:  "1 2",
			want: map[string]string{"a": "1", "b": "2", "c": ""},
		},
		{
			// A US byte turns whitespace splitting off, which is how a
			// value containing spaces is written.
			name: "an ASCII US separator is autodetected",
			vars: "a, b",
			row:  "one two" + UnitSeparator + "three four",
			want: map[string]string{"a": "one two", "b": "three four"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			items := filepath.Join(dir, "items.txt")
			if err := os.WriteFile(items, []byte(tc.row+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			sf, err := ParseSubmitFileWithOptions(strings.NewReader(
				"executable = /bin/sleep\n"+
					"arguments = [$(a)][$(b)][$(c)]\n"+
					"queue "+tc.vars+" from "+items+"\n"),
				SubmitParseOptions{AllowLocalAccess: true})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			res, err := sf.Submit(1)
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if len(res.ProcAds) != 1 {
				t.Fatalf("queued %d jobs, want 1", len(res.ProcAds))
			}
			expr, ok := res.ProcAds[0].Lookup("Args")
			if !ok {
				t.Fatal("no Args")
			}
			args, err := expr.Eval(nil).StringValue()
			if err != nil {
				t.Fatalf("Args is not a string: %v", err)
			}
			want := "[" + tc.want["a"] + "][" + tc.want["b"] + "][" + tc.want["c"] + "]"
			if args != want {
				t.Errorf("row %q cut to %q, want %q", tc.row, args, want)
			}
		})
	}
}
