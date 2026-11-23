package htcondor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bracketed item list is `from (` ... `)`, and the doubled-paren form
// this file used to be about is not a form at all.
//
// condor_submit anchors the queue parse on the `in`/`from` keyword and
// looks for a single `(` to decide the list is bracketed. A second `(`
// is therefore the first item's text, not part of any delimiter. This
// parser used to intercept `from ((...))` in a pre-pass and write the
// rows to a temp file, on the stated grounds that the doubled form was
// "what HTCondor's own condor_submit accepts" -- which measurement, when
// finally taken against SubmitHash 26.2.0 through the differential
// oracle, said otherwise: three procs on the C++ side for a two-row
// block, two here.
//
// These cases pin what the two parsers now agree on. The seeds that
// hold the agreement itself are in fuzz/submit/differential_test.go,
// where they run against the real SubmitHash.
func TestBracketedItemListCounts(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "one row per line",
			body: "executable = /bin/true\nqueue x from (\n  1\n  2\n)\n",
			want: 2,
		},
		{
			// The inner "(" is an item, so this is three rows: "(",
			// "1", "2". Exactly the extra job the pre-pass used to hide.
			name: "a doubled paren adds a row",
			body: "executable = /bin/true\nqueue x from ((\n  1\n  2\n))\n",
			want: 3,
		},
		{
			// A ")" that is not the first thing on its line is text.
			name: "close paren inside an item",
			body: "executable = /bin/true\nqueue x from (\n  a)b\n  c\n)\n",
			want: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sf, err := ParseSubmitFile(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("ParseSubmitFile: %v", err)
			}
			if got := sf.QueueCount(); got != tc.want {
				t.Errorf("QueueCount() = %d, want %d", got, tc.want)
			}
		})
	}
}

// An unterminated bracketed list is an error; an empty one is not.
//
// Both are SubmitHash's answers, and the second is the one the old
// pre-pass got wrong -- it refused `from ((\n))` with "no rows". The
// parser queues the count with the loop variable expanding to nothing,
// which the oracle confirms. Refusing a file HTCondor would have
// accepted is the same class of bug as accepting one it would have
// refused.
//
// Worth knowing where the agreement stops: `condor_submit` itself then
// declines to submit, with "has only empty queue commands -- no jobs
// queued". That check lives in condor_submit.V6/submit.cpp, in the tool
// and not in SubmitHash, so it is outside what this parser mirrors and
// outside what the oracle can see.
func TestBracketedItemListEdges(t *testing.T) {
	t.Run("unterminated is an error", func(t *testing.T) {
		_, err := ParseSubmitFile(strings.NewReader(
			"executable = /bin/true\nqueue x from (\n  1\n  2\n"))
		if err == nil {
			t.Fatal("expected an error for a list with no closing ')'")
		}
	})
	t.Run("empty is not an error", func(t *testing.T) {
		sf, err := ParseSubmitFile(strings.NewReader(
			"executable = /bin/true\nqueue x from (\n)\n"))
		if err != nil {
			t.Fatalf("an empty list is not an error in SubmitHash: %v", err)
		}
		if got := sf.QueueCount(); got != 1 {
			t.Errorf("QueueCount() = %d, want 1", got)
		}
	})
}

// TestParseErrorIncludesPosition confirms the line/col-aware Error()
// hook lights up. Pre-pre-fix, the error was a bare "syntax error"; the
// new error includes the line and column of the offending character.
// The input is a parameter name with a '-' in it, which HTCondor's
// is_valid_param_name() rejects and so does this parser -- an unquoted
// path after `from` no longer serves, because that is an item file's
// name now, exactly as condor_submit reads it.
func TestParseErrorIncludesPosition(t *testing.T) {
	body := "executable = /bin/true\na-b = 1\nqueue\n"
	_, err := ParseSubmitFile(strings.NewReader(body))
	if err == nil {
		t.Fatalf("expected parse error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "line ") {
		t.Errorf("expected line number in error, got: %s", msg)
	}
	if !strings.Contains(msg, "col ") {
		t.Errorf("expected column number in error, got: %s", msg)
	}
}

// An item file's name is taken verbatim after `from`, spaces included.
//
// This used to be pinned through the pre-pass's temp file, which was the
// only path in the tree that could contain a space. With the pre-pass
// gone the rule still holds and still needs a test, because a path with
// a space is the one spelling that cannot be quoted: quotes would become
// part of the name.
func TestItemFileNameWithSpaceIsVerbatim(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a dir with spaces")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	items := filepath.Join(dir, "rows.txt")
	if err := os.WriteFile(items, []byte("alpha, 100\nbeta, 200\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	raw := "executable = /bin/true\narguments = $(sample) $(events)\n" +
		"queue sample, events from " + items + "\n"

	// AllowLocalAccess, because this names a path on the parsing host --
	// which is exactly what the default refuses for a submit file that
	// arrived over an API.
	sf, err := ParseSubmitFileWithOptions(strings.NewReader(raw),
		SubmitParseOptions{AllowLocalAccess: true})
	if err != nil {
		t.Fatalf("an item file whose path contains a space must parse: %v", err)
	}
	if got := sf.QueueCount(); got != 2 {
		t.Errorf("QueueCount() = %d, want 2", got)
	}
	if got := sf.QueueVars(); len(got) != 2 || got[0] != "sample" || got[1] != "events" {
		t.Errorf("QueueVars() = %v, want [sample events]", got)
	}
}
