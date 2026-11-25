package knobs_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/webapi/knobs"
)

// knobs_gen.go is generated. Regenerate it with `make knobs`.
func TestGeneratedFileIsCurrent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "knobs_gen.go")
	// G204: a fixed command; out is this test's own temp path.
	//nolint:gosec
	cmd := exec.CommandContext(t.Context(), "go", "run", "./generate.go", "-out", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the generator: %v\n%s", err, b)
	}
	want, err := os.ReadFile(out) //nolint:gosec // G304: out is this test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("knobs_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Error("knobs_gen.go is out of date; run `make knobs`")
	}
}

// The type only constrains anything while the Define* constructors stay
// confined to generated code. They are exported because the generated
// table lives in a different module from config, so nothing but this
// check keeps a hand-written call from declaring a knob that the YAML,
// and therefore the reference, never hears about.
func TestDefineIsConfinedToGeneratedCode(t *testing.T) {
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", "testdata", ".worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		// The generator names the constructors in string literals, and
		// a test may declare a throwaway knob to exercise a type. What
		// matters is that nothing the server actually reads is declared
		// outside the table.
		if strings.HasSuffix(path, "generate.go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // G304: path comes from walking the module
		if err != nil {
			return err
		}
		for _, ctor := range []string{"config.DefineString(", "config.DefineBool(",
			"config.DefineDuration(", "config.DefineBytes(", "config.DefineList("} {
			if strings.Contains(string(b), ctor) {
				t.Errorf("%s calls %s; declare the knob in knobs.yaml instead", path, ctor)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Every declared knob must carry what the reference prints.
func TestEveryKnobIsDocumented(t *testing.T) {
	if len(knobs.All) == 0 {
		t.Fatal("no knobs declared")
	}
	for _, k := range knobs.All {
		if !strings.HasPrefix(k.Name(), "HTTP_API_") {
			t.Errorf("%s: not an HTTP_API_ setting", k.Name())
		}
		if strings.TrimSpace(k.Doc()) == "" {
			t.Errorf("%s: no documentation", k.Name())
		}
	}
}

// A flag in the table that nothing consumes is a declaration with no
// effect, which is the defect this table exists to prevent. secret:
// true must reach what an operator reads.
func TestSecretSettingsSaySo(t *testing.T) {
	const marker = "Holds or names a credential"
	spec, err := os.ReadFile("knobs.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wantSecret := strings.Count(string(spec), "secret: true")
	if wantSecret == 0 {
		t.Skip("no setting is marked secret")
	}

	var got int
	for _, k := range knobs.All {
		if strings.Contains(k.Doc(), marker) {
			got++
		}
	}
	// Family settings are not in All, so count only the plain ones and
	// require at least one to carry the sentence; the generated file is
	// what proves the rest.
	if got == 0 {
		t.Errorf("%d settings are marked secret: true and none of knobs.All says so", wantSecret)
	}
	gen, err := os.ReadFile("knobs_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	// Twice per setting: once in the doc comment, once in the string
	// handed to Define.
	if n := strings.Count(string(gen), marker); n != wantSecret*2 {
		t.Errorf("the marker appears %d times for %d secret settings; want %d",
			n, wantSecret, wantSecret*2)
	}
}

// referencePath is the page whose generated region the knob table
// prints into.
const referencePath = "../../docs/operator/config.md"

// The reference is generated. Regenerate it with `make knobs`.
//
// Run against a copy rather than comparing a rendered region, so the
// marker handling is exercised too: a page whose markers were lost in
// an edit has to fail here rather than silently stop being updated.
func TestReferenceIsCurrent(t *testing.T) {
	want, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copyTo := filepath.Join(dir, "config.md")
	// G703: copyTo is a fixed basename under this test's own t.TempDir().
	//nolint:gosec
	if err := os.WriteFile(copyTo, want, 0o600); err != nil {
		t.Fatal(err)
	}
	// G204: a fixed command; the paths are this test's own temp files.
	//nolint:gosec
	cmd := exec.CommandContext(t.Context(), "go", "run", "./generate.go",
		"-out", filepath.Join(dir, "knobs_gen.go"), "-docs", copyTo)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the generator: %v\n%s", err, b)
	}
	got, err := os.ReadFile(copyTo) //nolint:gosec // G304: this test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s is out of date; run `make knobs`", referencePath)
	}
}

// Every declared setting has to reach the page, as its own entry. The
// generator writes them all, so this fails only if the generated
// region stopped being where the reference is -- which is the whole
// point of generating it.
func TestEveryKnobIsInTheReference(t *testing.T) {
	page, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(page)
	for _, k := range knobs.All {
		if !strings.Contains(body, "### `"+k.Name()+"`\n") {
			t.Errorf("%s is declared but has no entry in %s", k.Name(), referencePath)
		}
	}
}

// A setting documented in both halves of the page is two descriptions
// of one thing, which is what generating the reference is meant to
// end. A hand-written row for a declared setting is therefore an
// error: fold its prose into knobs.yaml and delete the row.
func TestNoKnobIsDocumentedTwice(t *testing.T) {
	page, err := os.ReadFile(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, k := range knobs.All {
		declared[k.Name()] = true
	}

	hand, err := handWrittenPart(string(page))
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`HTTP_API_[A-Z0-9_]+`)
	for _, line := range strings.Split(hand, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		// The first cell is what the row documents; a name in the
		// prose of some other row is a cross-reference, not a second
		// description.
		cell, _, _ := strings.Cut(strings.TrimPrefix(line, "| "), "|")
		for _, n := range name.FindAllString(cell, -1) {
			if declared[n] {
				t.Errorf("%s is declared in knobs.yaml and also has a hand-written row: "+
					"fold the row's prose into the table and delete the row", n)
			}
		}
	}
}

// handWrittenPart is the page with the generated region removed.
func handWrittenPart(body string) (string, error) {
	const begin = "<!-- BEGIN GENERATED SETTINGS"
	const end = "<!-- END GENERATED SETTINGS -->"
	i := strings.Index(body, begin)
	j := strings.Index(body, end)
	if i < 0 || j < i {
		return "", fmt.Errorf("%s has no generated region", referencePath)
	}
	return body[:i] + body[j+len(end):], nil
}
