//go:build libcondor_utils

package fuzzsubmit

import (
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/fuzz/submit/oracle"
)

// TestShellMatchesOracle pins what `shell` does to a job, which is more than
// it looks: SetExecutable writes Cmd = /bin/sh and clears TransferExecutable
// before it reads the executable keyword at all, and SetArguments builds
// -c <value> and returns before it reads arguments or arguments2.
//
// So one keyword decides three attributes and silences two others. The cases
// here are the ones where that matters -- shell beside an executable, beside
// arguments, beside BOTH spellings of arguments (which alone would be an
// error), with quoting in the value, and empty, which is not set at all and
// leaves the executable alone.
//
// The sweep reaches shell now that nonTableKeywords seeds it, but a sweep
// compares whatever the generator happened to write; these are the shapes
// worth stating outright.
func TestShellMatchesOracle(t *testing.T) {
	requireOracle(t)

	cases := map[string]string{
		"beside an executable":      "executable = /bin/sleep\nshell = /bin/bash -c true\n",
		"with no executable":        "shell = echo hi\n",
		"beside arguments":          "executable = /bin/sleep\nshell = echo hi\narguments = a b\n",
		"beside both arg spellings": "executable = /bin/sleep\nshell = x\narguments = p\narguments2 = q\n",
		"with quoting in the value": "shell = a 'b c' d\n",
		"empty, so not set at all":  "shell = \nexecutable = /bin/sleep\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			src := "universe = vanilla\n" + body + "queue\n"
			cpp := oracle.MakeAds(src, oracle.ModeAds, 2)
			got := GoMakeAds(src)
			if !cpp.OK || !got.OK {
				t.Fatalf("one side refused the file: cppOK=%v (%s) goOK=%v (%s)",
					cpp.OK, strings.TrimSpace(cpp.Err), got.OK, got.Err)
			}
			cppAds, err := ParseCanon(cpp.Ads)
			if err != nil {
				t.Fatalf("parsing the oracle's ads: %v", err)
			}
			for _, d := range mustCompare(cppAds, got.Ads) {
				t.Errorf("%s", d.String())
			}
		})
	}
}

// mustCompare is Compare with the known-divergence half dropped: these cases
// are meant to agree outright, so a difference excused elsewhere would still
// be one here.
func mustCompare(cpp, goAds Ads) []Divergence {
	fresh, _ := Compare(cpp, goAds)
	return fresh
}
