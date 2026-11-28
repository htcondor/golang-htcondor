//go:build libcondor_utils

package fuzzsubmit

import (
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/fuzz/submit/oracle"
)

// organizedOutputCases are the shapes the organized output commands take: the
// base directory alone, the pattern alone, the two together, the flag that
// turns them on and the one that turns them off, output_directory beside
// them, and a pattern that climbs out of the base.
//
// The sweep cannot reach these against an older oracle -- the generator takes
// its keywords from the oracle's own table, so a keyword that HTCondor does
// not have yet is never written -- which is why generated/outputdirectory.md
// names this test instead.
var organizedOutputCases = []string{
	"organized_output_directory = withbase\nqueue\n",
	"organized_output = true\nqueue\n",
	"organized_output_pattern = p/$(ProcId)\nqueue\n",
	"organized_output_directory = b\norganized_output_pattern = p/$(ProcId)\nqueue\n",
	"organized_output = false\norganized_output_directory = b\nqueue\n",
	"output_directory = od\norganized_output_directory = b\nqueue\n",
	"organized_output_directory = b\nqueue 2\n",
	"batch_name = bn\norganized_output_directory = b\nqueue\n",
	"organized_output_directory = b\norganized_output_pattern = sub/deep\nqueue\n",
}

// TestOrganizedOutputMatchesOracle pins what the two parsers do with the
// organized output commands, on whichever HTCondor is linked.
//
// Against an oracle that has HTCONDOR-3618 the two agree, attribute for
// attribute. Against one that predates it the C++ side does not know the
// commands -- it reads them as ordinary submit macros and writes nothing --
// while this parser still writes OutputDirectory, which is the recorded
// divergence and the direction keywordsAheadOfOracle allows.
//
// Stating both halves is the point: a test that only ran against the newer
// oracle would pass just as well if this parser had quietly stopped writing
// the attribute at all.
func TestOrganizedOutputMatchesOracle(t *testing.T) {
	requireOracle(t)
	hasFeature := oracleHas["HTCONDOR-3618"]

	for _, tail := range organizedOutputCases {
		src := "executable = /bin/sleep\nuniverse = vanilla\n" + tail
		t.Run(firstLine(tail), func(t *testing.T) {
			cpp := oracle.MakeAds(src, oracle.ModeAds, 4)
			got := GoMakeAds(src)
			if !cpp.OK || !got.OK {
				t.Fatalf("one side refused the file: cppOK=%v (%s) goOK=%v (%s)",
					cpp.OK, strings.TrimSpace(cpp.Err), got.OK, got.Err)
			}
			cppAds, err := ParseCanon(cpp.Ads)
			if err != nil {
				t.Fatalf("parsing the oracle's ads: %v", err)
			}
			fresh, _ := Compare(cppAds, got.Ads)
			if hasFeature {
				for _, d := range fresh {
					t.Errorf("%s", d.String())
				}
				return
			}
			// The older oracle: every difference must be the one
			// attribute, and it must be Go that has it.
			for _, d := range fresh {
				if !strings.EqualFold(d.Attr, "OutputDirectory") {
					t.Errorf("%s", d.String())
				}
			}
		})
	}
}

// TestOrganizedOutputRefusesAnEscapingPattern pins the one organized output
// input both parsers reject: a pattern that climbs out of the directory it is
// joined to. Letting it through would put a job's output somewhere the
// submitter did not name.
func TestOrganizedOutputRefusesAnEscapingPattern(t *testing.T) {
	requireOracle(t)
	if !oracleHas["HTCONDOR-3618"] {
		t.Skip("this oracle predates HTCONDOR-3618 and has no organized output to refuse")
	}
	src := "executable = /bin/sleep\nuniverse = vanilla\n" +
		"organized_output_directory = b\norganized_output_pattern = ../esc\nqueue\n"
	if cpp := oracle.MakeAds(src, oracle.ModeAds, 4); cpp.OK {
		t.Error("the oracle accepted a pattern that climbs out of the base directory")
	}
	if got := GoMakeAds(src); got.OK {
		t.Error("this parser accepted a pattern that climbs out of the base directory")
	}
}
