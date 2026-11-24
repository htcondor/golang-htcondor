package knobs_test

import (
	"os"
	"os/exec"
	"path/filepath"
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
