package mcpserver

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// TestEveryToolIsNamedInTheGuide: a tool a model never reads about is a
// tool it will not reach for. tail_job_output and exec_in_job both shipped
// that way -- listed in tools/list, absent from the prose -- and the
// complaint that came back was that they did not exist. The instructions
// are capped at instructionsBudget and cannot name every tool, so the
// guide's "tools" topic does, and the instructions point at it.
//
// toolAnnotations is the list of every tool this package can declare
// (annotations_test.go keeps it complete), so the next tool added inherits
// the check, including the ones a bare server does not enable.
func TestEveryToolIsNamedInTheGuide(t *testing.T) {
	tools, ok := guideText("tools")
	if !ok {
		t.Fatal("the guide has no tools topic")
	}
	if len(toolAnnotations) == 0 {
		t.Fatal("no tools to check; this test would pass vacuously")
	}
	var missing []string
	for name := range toolAnnotations {
		if !strings.Contains(tools, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("never named in the guide's tools topic, so a model will not reach for them: %v", missing)
	}
}

// TestInstructionsNameEveryGuideTopic: the guide is only found through the
// instructions, so every topic has to be listed there.
func TestInstructionsNameEveryGuideTopic(t *testing.T) {
	instructions := defaultInstructions("test_schedd")
	if !strings.Contains(instructions, "doc_guide") {
		t.Fatal("the instructions do not name doc_guide")
	}
	for _, topic := range guideTopicNames() {
		if !strings.Contains(instructions, topic) {
			t.Errorf("the instructions do not list guide topic %q", topic)
		}
	}
}

// TestInstructionsFitTheBudget holds the built-in instructions to
// instructionsBudget, measured with the manual lookups offered (the longer
// variant) and a long access point name.
func TestInstructionsFitTheBudget(t *testing.T) {
	text := identityInstructions("login-node-04.cluster.example-university.edu") + genericInstructions(true)
	if len(text) > instructionsBudget {
		t.Errorf("built-in instructions are %d bytes, over the %d-byte budget; move detail into a guide topic",
			len(text), instructionsBudget)
	}
	t.Logf("built-in instructions: %d bytes", len(text))
}

// TestInstructionsOmitManualLookupsWhenNotEmbedded: a build without the
// HTCondor manual has no doc_search, and the text must not name it.
func TestInstructionsOmitManualLookupsWhenNotEmbedded(t *testing.T) {
	if strings.Contains(genericInstructions(false), "doc_search") {
		t.Error("instructions name doc_search in a build that does not offer it")
	}
	if !strings.Contains(genericInstructions(true), "doc_search") {
		t.Error("instructions do not name doc_search in a build that offers it")
	}
}

// The README is what docs/operator/agents.md and docs/operator/index.md
// send a human to for "the tool catalogue", and for a long time it
// promised one and listed 16 of 52: the per-tool sections were written
// as tools were added early on and then simply stopped being written.
// Nothing noticed, because a document that lists a subset is internally
// consistent -- there is no dangling reference to find.
//
// Same shape as TestEveryToolIsNamedInTheGuide above, and for the same
// reason: toolAnnotations is the complete list (annotations_test.go
// keeps it so), so the next tool added inherits this check whether or
// not a bare server would enable it.
//
// This does NOT check that the README describes a tool correctly, only
// that it names it. What a tool accepts is served live by tools/list,
// which is the one place that cannot drift.
func TestEveryToolIsNamedInTheREADME(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	if len(toolAnnotations) == 0 {
		t.Fatal("no tools to check; this test would pass vacuously")
	}
	var missing []string
	for name := range toolAnnotations {
		// Backticked, so a tool whose name happens to be a substring of
		// prose does not count as catalogued -- get_job appears inside
		// get_job_output and inside sentences about job retrieval.
		if !strings.Contains(readme, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("not named in webapi/mcpserver/README.md, which the operator documentation "+
			"calls the tool catalogue: %v\nAdd a row to one of its two tables.", missing)
	}
}
