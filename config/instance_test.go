package config

import (
	"reflect"
	"testing"
)

func TestInstanceID(t *testing.T) {
	for in, want := range map[string]string{
		"lab-a": "LAB_A", "lab_a": "LAB_A", "LAB-A": "LAB_A",
		"gdrive": "GDRIVE", "builtin-gdrive": "BUILTIN_GDRIVE",
	} {
		if got := InstanceID(in); got != want {
			t.Errorf("InstanceID(%q) = %q, want %q", in, got, want)
		}
	}
}

// The case the fold creates: two names an operator would read as
// different that the configuration cannot tell apart.
func TestInstanceCollisions(t *testing.T) {
	got := InstanceCollisions([]string{"lab-a", "lab_a", "lab-b"})
	want := map[string][]string{"LAB_A": {"lab-a", "lab_a"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InstanceCollisions = %v, want %v", got, want)
	}
}

func TestInstanceCollisionsNoneWhenDistinct(t *testing.T) {
	if got := InstanceCollisions([]string{"a", "b", "c-d"}); len(got) != 0 {
		t.Errorf("InstanceCollisions = %v, want none", got)
	}
}

// An exact repeat is one name written twice, not two names colliding;
// a caller that dedupes first should see nothing here either way.
func TestInstanceCollisionsOnExactRepeat(t *testing.T) {
	got := InstanceCollisions([]string{"lab-a", "lab-a"})
	if len(got) != 1 || len(got["LAB_A"]) != 2 {
		t.Errorf("InstanceCollisions = %v", got)
	}
}
