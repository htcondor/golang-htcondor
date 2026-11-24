package htcondor

import (
	"fmt"
	"testing"
)

// QuoteClassAdString and fmt.Sprintf("%q") agree on far more than they differ
// on: both escape a quote and a backslash the same way, and %q leaves printable
// non-ASCII alone. They part company on NON-PRINTABLE bytes, which %q renders
// as \x01 / \u2028 -- escapes ClassAd's old-format parser does not read back
// as the original character.
//
// This test exists to record where the line actually falls, so the constraint
// builders keep using the ClassAd quoter without anyone believing %q is broken
// for ordinary names, and without anyone "simplifying" it back.
func TestQuoteClassAdStringAgainstGoQuoting(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", `alice@example.org`, `"alice@example.org"`},
		{"quote", `al"ice`, `"al\"ice"`},
		{"backslash", `dom\ain`, `"dom\\ain"`},
		// %q renders these as é and \x01; ClassAd wants the bytes.
		{"non-ascii", "josé@example.org", `"josé@example.org"`},
		{"control byte", "a\x01b", "\"a\x01b\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuoteClassAdString(tc.in); got != tc.want {
				t.Errorf("QuoteClassAdString(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}

	// Where they agree, say so: an ordinary name is quoted identically, which
	// is why the %q call sites this replaces were not producing wrong
	// constraints for real owners.
	for _, in := range []string{"alice@example.org", "josé@example.org", `al"ice`, `dom\ain`} {
		if got, goq := QuoteClassAdString(in), fmt.Sprintf("%q", in); got != goq {
			t.Errorf("QuoteClassAdString(%q) = %s but %%q gives %s; expected agreement here", in, got, goq)
		}
	}

	// Where they diverge, pin it: a non-printable byte is the case the
	// ClassAd quoter is needed for.
	for _, in := range []string{"a\x01b", "line\u2028break"} {
		if got, goq := QuoteClassAdString(in), fmt.Sprintf("%q", in); got == goq {
			t.Errorf("QuoteClassAdString(%q) == %%q output %s; the two must differ on a non-printable", in, goq)
		}
	}
}
