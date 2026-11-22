package httpserver

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/webapi/submitpolicy"
)

// The policy is worth nothing if a submit surface bypasses it. This
// walks the SubmitRemote call sites and asserts each one runs its submit
// file through the policy first.
//
// A source-level check rather than a behavioural one because the
// alternative is standing up a schedd per surface; what actually goes
// wrong here is someone adding a fourth submit path and not knowing the
// policy exists, which is precisely what this catches.
//
// The file list is DISCOVERED, not written down. It was written down,
// and the next submit path to arrive -- the SFTP file service -- called
// SubmitRemote from a file the list did not name, so the one test whose
// whole job was to catch that passed. A hand-maintained list of the
// places a rule applies is a list that stops being the places the rule
// applies.
func TestEverySubmitSiteAppliesThePolicy(t *testing.T) {
	files := submittingFiles(t)
	// A floor, not a count: this is a tripwire for a discovery that has
	// stopped discovering, which is the failure that makes every
	// assertion below vacuous. The package had seven such files when
	// this was written.
	if len(files) < 5 {
		t.Fatalf("found only %d files that submit (%v), which is fewer than this package has: "+
			"the discovery is broken, and a broken discovery passes everything",
			len(files), sortedFileNames(files))
	}
	for name, src := range files {
		for _, line := range strings.Split(src, "\n") {
			if !submitsOnThisLine(line) {
				continue
			}
			if !appliesThePolicy(line, src) {
				t.Errorf("%s submits without applying the site policy:\n\t%s\n"+
					"every submit path must go through submitJob, or submitPolicy.Apply and then "+
					"submitApplied -- otherwise a site's requirements, the credentials a job "+
					"needs, the access point's request defaults and the idempotency key all "+
					"silently do not apply to it", name, strings.TrimSpace(line))
			}
		}
	}
}

// appliesThePolicy reports whether a submitting line has the policy
// applied to what it sends.
//
// Four spellings count, and they are the four the package uses: the
// policy applied on the line itself, a file that applied it into
// `applied` just above, submitJob (the wrapper that applies it for
// you), and a file that hands the policy to something else that
// applies it -- which is what the DAG path does, because dagman.Prepare
// applies it so that a dry run can show the submit file that will
// really be sent.
//
// That last one is weaker than the others and deliberately so: it
// proves the file knows the policy exists, not that this line honours
// it. What this test is for is a new submit path written by somebody
// who does not know there is a policy at all, and a file naming it in
// working code is not that.
func appliesThePolicy(line, src string) bool {
	switch {
	case strings.Contains(line, "submitPolicy.Apply("):
		return true
	case strings.Contains(line, ".submitJob("):
		return true
	case strings.Contains(line, "(ctx, applied)") &&
		strings.Contains(src, "applied, err := s.submitPolicy.Apply("):
		return true
	}
	return mentionsThePolicy(src)
}

// mentionsThePolicy reports a reference to submitPolicy in working
// code, comments excluded -- a comment claiming the policy is applied
// elsewhere is not evidence that it is.
func mentionsThePolicy(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		if strings.Contains(line, "submitPolicy") {
			return true
		}
	}
	return false
}

// The handler must carry the configured policy through to the embedded
// MCP server, or agent submissions would skip site policy that every
// other surface honours.
func TestMCPServerInheritsTheSubmitPolicy(t *testing.T) {
	// Whitespace-collapsed, because the field's alignment in the struct
	// literal is gofmt's business: adding a longer field name beside it
	// re-indents this line, and a test that failed for that would be
	// reporting the policy as unwired when it was not.
	src := strings.Join(strings.Fields(mustRead(t, "handler.go")), " ")
	if !strings.Contains(src, "SubmitPolicy: h.submitPolicy,") {
		t.Error("the MCP server is built without the submit policy; agent submissions would bypass it")
	}
}

// An unconfigured deployment must be entirely unaffected.
func TestHandlerWithNoPolicyLeavesSubmitFilesAlone(t *testing.T) {
	h := &Handler{}
	const file = "executable = /bin/true\nlog = job.log\nqueue\n"
	if got, err := h.submitPolicy.Apply(file); err != nil || got != file {
		t.Errorf("a handler with no configured policy changed the submit file:\n%s", got)
	}
}

// And a configured one reaches the submit file with both halves in
// character: the default yields to the user, the override does not.
func TestHandlerPolicyAppliesBothHalves(t *testing.T) {
	h := &Handler{submitPolicy: submitpolicy.Policy{
		Defaults:  "request_memory = 512",
		Overrides: "log = /home/alice/forced.log",
	}}
	got, err := h.submitPolicy.Apply("executable = /bin/true\nrequest_memory = 2048\nlog = /tmp/nope.log\nqueue\n")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !strings.Contains(got, "request_memory = 2048") {
		t.Error("the user's request_memory was lost")
	}
	if strings.Index(got, "/home/alice/forced.log") < strings.Index(got, "queue") &&
		strings.Index(got, "/home/alice/forced.log") < strings.LastIndex(got, "/tmp/nope.log") {
		t.Error("the override does not come after the user's log line, so it would not win")
	}
	if !strings.Contains(got, "/home/alice/forced.log") {
		t.Error("the override is missing")
	}
}

// submittingFiles is every source file in this package that submits a
// job, by any of the three doors: SubmitRemote itself, submitApplied
// (SubmitRemote for a file the policy has already been applied to), or
// submitJob (which applies it and calls submitApplied).
//
// submitJob sites pass the check below trivially. They are counted
// anyway, because the census is what makes the floor in the caller
// mean anything -- and because a change to what submitJob guarantees
// should bring every one of them back into view.
//
// Tests are excluded -- a test may submit a fixture without a site
// policy on purpose -- and so is the file that defines the doors.
func submittingFiles(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading this package's directory: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == submitDoorFile {
			continue
		}
		src := mustRead(t, name)
		for _, line := range strings.Split(src, "\n") {
			if submitsOnThisLine(line) {
				out[name] = src
				break
			}
		}
	}
	return out
}

// sortedFileNames names the files found, for a failure somebody has to
// act on.
func sortedFileNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// submitDoorFile defines submitJob and submitApplied themselves, whose
// own call to SubmitRemote is the one that must not be rewritten.
const submitDoorFile = "required_creds.go"

// submitsOnThisLine reports a line that actually submits, as opposed to
// one that talks about it or declares it.
//
// The leading dot is what separates a CALL from a method signature in
// an interface. Without it this reported jupyterScheddOps -- which
// declares SubmitRemote and submits nothing -- and that false positive
// had already cost something: the interface was moved into a file of
// its own to get out of the way of this test, with a comment saying so.
func submitsOnThisLine(line string) bool {
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	for _, door := range []string{".SubmitRemote(", ".submitApplied(", ".submitJob("} {
		if strings.Contains(line, door) {
			return true
		}
	}
	return false
}

// mustRead loads a source file from this package's directory.
func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name) //nolint:gosec // G304: a fixed source filename from this test, not user input
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}
