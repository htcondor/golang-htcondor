package connectors

import (
	"strings"
	"testing"
)

// Nothing constrains a chooser's name, and the fold to a parameter name
// turns hyphens into underscores, so "lab-a" and "lab_a" read exactly
// the same parameters. Before this was caught, both appeared on the
// submit page with identical configuration and nothing said why.
func TestPickersReportNameCollision(t *testing.T) {
	src := NewConfigSource(fakeParams{
		"HTTP_API_PICKERS":                "lab-a, lab_a, lab-b",
		"HTTP_API_PICKER_LAB_A_PROVIDER":  "gdrive",
		"HTTP_API_PICKER_LAB_A_CONNECTOR": "builtin:gdrive",
		"HTTP_API_PICKER_LAB_A_API_KEY":   "key",
		"HTTP_API_PICKER_LAB_B_PROVIDER":  "gdrive",
		"HTTP_API_PICKER_LAB_B_CONNECTOR": "builtin:gdrive",
		"HTTP_API_PICKER_LAB_B_API_KEY":   "key",
	})

	byName := map[string]Picker{}
	for _, p := range src.Pickers() {
		byName[p.Name] = p
	}
	if len(byName) != 3 {
		t.Fatalf("got %d choosers, want 3: %v", len(byName), byName)
	}

	for _, name := range []string{"lab-a", "lab_a"} {
		probs := byName[name].Problems()
		if len(probs) != 1 {
			t.Errorf("%s: problems = %v, want exactly the collision", name, probs)
			continue
		}
		if !strings.Contains(probs[0], "HTTP_API_PICKER_LAB_A_") {
			t.Errorf("%s: %q does not name the shared parameter", name, probs[0])
		}
		other := "lab_a"
		if name == "lab_a" {
			other = "lab-a"
		}
		if !strings.Contains(probs[0], other) {
			t.Errorf("%s: %q does not name the other chooser", name, probs[0])
		}
	}

	// The chooser that does not collide is unaffected.
	if probs := byName["lab-b"].Problems(); len(probs) != 0 {
		t.Errorf("lab-b: problems = %v, want none", probs)
	}
}

// A name written twice is one chooser, not a collision.
func TestPickersDedupeExactRepeat(t *testing.T) {
	src := NewConfigSource(fakeParams{
		"HTTP_API_PICKERS":                "drive, drive",
		"HTTP_API_PICKER_DRIVE_PROVIDER":  "gdrive",
		"HTTP_API_PICKER_DRIVE_CONNECTOR": "builtin:gdrive",
		"HTTP_API_PICKER_DRIVE_API_KEY":   "key",
	})
	ps := src.Pickers()
	if len(ps) != 1 {
		t.Fatalf("got %d choosers, want 1", len(ps))
	}
	if probs := ps[0].Problems(); len(probs) != 0 {
		t.Errorf("problems = %v, want none", probs)
	}
}

// The list is comma-separated, not whitespace-separated: a chooser
// named with a space in it stays one name.
func TestPickerListSplitsOnCommasOnly(t *testing.T) {
	src := NewConfigSource(fakeParams{"HTTP_API_PICKERS": "my picker"})
	ps := src.Pickers()
	if len(ps) != 1 || ps[0].Name != "my picker" {
		t.Errorf("choosers = %+v", ps)
	}
}
