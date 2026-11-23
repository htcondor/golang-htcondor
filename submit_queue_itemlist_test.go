package htcondor

import (
	"strings"
	"testing"
)

// TestItemListIsExpandedBeforeItIsSplit pins that an inline `queue ... in (...)`
// list is expanded as one piece of text and cut afterwards, rather than cut
// first and expanded piece by piece. The two differ whenever expanding changes
// where the cuts fall, which the fuzzer found with `queue in(0 $())`: the
// empty macro was one item here and none under condor_submit.
//
// Every expectation was measured against HTCondor's own parser (SubmitHash).
func TestItemListIsExpandedBeforeItIsSplit(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{{
		name: "a macro expanding to nothing is no item",
		src:  "A=\nqueue in(0 $(A))",
		want: []string{`"0"`},
	}, {
		name: "an undefined macro is no item",
		src:  "queue in(0 $(nosuch))",
		want: []string{`"0"`},
	}, {
		name: "an empty reference is no item",
		src:  "queue in(0 $())",
		want: []string{`"0"`},
	}, {
		name: "an empty reference first is no item",
		src:  "queue in($() 0)",
		want: []string{`"0"`},
	}, {
		name: "every item empty leaves the one that is not",
		src:  "queue in($() $() z)",
		want: []string{`"z"`},
	}, {
		name: "a macro holding two words is two items",
		src:  "A=x y\nqueue in($(A) b)",
		want: []string{`"x"`, `"y"`, `"b"`},
	}, {
		name: "a macro holding a comma list is two items",
		src:  "A=x,y\nqueue in($(A) b)",
		want: []string{`"x"`, `"y"`, `"b"`},
	}, {
		name: "a macro holding one word is one item",
		src:  "A=x\nqueue in($(A) b)",
		want: []string{`"x"`, `"b"`},
	}, {
		name: "an empty macro inside a word splits nothing",
		src:  "queue in(a$()b c)",
		want: []string{`"ab"`, `"c"`},
	}, {
		name: "a list with no macros is unchanged",
		src:  "queue in(0 1)",
		want: []string{`"0"`, `"1"`},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := argsOf(t, "executable = /bin/sleep\narguments = $(item)\n"+c.src+"\n")
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("%q queued %v, want %v", c.src, got, c.want)
			}
		})
	}
}

// TestCyclicItemListIsRefused pins that an item list this parser cannot expand
// fails the submission rather than queueing a job with the macro still in it.
//
// `FOO = $(BAR)` and `BAR = $(FOO)` refer to each other, so expanding either
// never terminates; the expander gives up on a step budget and reports a
// cycle. Keeping the item as written let the submission through, and because
// every later expansion of that item hit the same cycle, the job ad carried
// the literal text `$(item)` as its arguments. Only the factory path reported
// it, from the digest.
func TestCyclicItemListIsRefused(t *testing.T) {
	const cycle = "executable = /bin/true\nFOO = $(BAR)\nBAR = $(FOO)\narguments = [$(item)]\n"
	for _, c := range []struct{ name, src string }{
		{"plain submit", cycle + "queue in($(FOO) b)\n"},
		{"with a factory", cycle + "max_materialize = 2\nqueue in($(FOO) b)\n"},
		{"the cycle alone in the list", cycle + "queue in($(FOO))\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseSubmitFile(strings.NewReader(c.src))
			if err == nil {
				t.Fatalf("a submit file whose item list does not expand was accepted")
			}
			if !strings.Contains(err.Error(), "does not terminate") {
				t.Errorf("error does not name the cycle: %v", err)
			}
		})
	}

	// A cycle the item list never reads is not this statement's problem.
	sf, err := ParseSubmitFile(strings.NewReader(cycle + "queue in(a b)\n"))
	if err != nil {
		t.Fatalf("an unreferenced cycle failed the submission: %v", err)
	}
	if got := argsOf(t, cycle+"queue in(a b)\n"); strings.Join(got, "|") != `"[a]"|"[b]"` {
		t.Errorf("unreferenced cycle changed the items: %v", got)
	}
	_ = sf
}
