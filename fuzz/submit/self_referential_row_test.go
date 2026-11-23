//go:build libcondor_utils

package fuzzsubmit

import "testing"

// The skip has to be narrow or it is a hole: every input it accepts is one
// the fuzzer stops comparing, and the attributes it would have compared are
// whatever that submit file happened to set.
//
// What it is for: `queue a, b from (\nx1 $(b)\n)` hangs SubmitHash -- the
// row supplies b and expands b at the same time -- so the oracle never
// returns and the worker dies holding an input nobody can reproduce. The
// cases below are what decide the edges of that.
func TestSelfReferentialItemRowIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		// What hangs.
		{"second column references its own var",
			"executable = a\nqueue a, b from (\nx1 $(b)\n)\n", true},
		{"single var references itself",
			"executable = a\nqueue a from (\n$(a)\n)\n", true},
		{"case does not matter",
			"executable = a\nQueue A from (\n$(a)\n)\n", true},
		{"a later row is enough",
			"executable = a\nqueue a from (\nx1\n$(a)\n)\n", true},

		// What must still be compared.
		{"an ordinary from list",
			"executable = a\nqueue a, b from (\nx1 y1\nx2 y2\n)\n", false},
		{"a reference to a variable the list does not bind",
			"executable = a\nqueue a, b from (\nx1 $(c)\n)\n", false},
		{"a reference outside the block",
			"executable = a\narguments = $(a)\nqueue a from (\nx1\n)\n", false},
		{"an in list, whose items are not rows",
			"executable = a\nqueue a, b in (x1, $(b))\n", false},
		{"a from file rather than a list",
			"executable = a\nqueue a from items.txt\n", false},
		{"no queue statement at all",
			"executable = a\n", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfReferentialItemRow(tc.src); got != tc.want {
				t.Errorf("selfReferentialItemRow(%q) = %v, want %v", tc.src, got, tc.want)
			}
		})
	}
}
