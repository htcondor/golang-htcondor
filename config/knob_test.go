package config

import (
	"testing"
	"time"
)

func knobConfig(t *testing.T, kv map[string]string) *Config {
	t.Helper()
	c := NewEmpty()
	for k, v := range kv {
		c.Set(k, v)
	}
	return c
}

func TestKnobDefaults(t *testing.T) {
	c := knobConfig(t, nil)
	if got := DefineString("S", "fallback", Startup, "").Get(c); got != "fallback" {
		t.Errorf("string = %q", got)
	}
	if got := DefineBool("B", true, Startup, "").Get(c); !got {
		t.Error("bool should fall back to true")
	}
	if got := DefineDuration("D", 30*time.Second, Startup, "").Get(c); got != 30*time.Second {
		t.Errorf("duration = %v", got)
	}
	if got := DefineBytes("Z", 1<<20, Startup, "").Get(c); got != 1<<20 {
		t.Errorf("bytes = %d", got)
	}
	if got := DefineList("L", []string{"a"}, Startup, "").Get(c); len(got) != 1 || got[0] != "a" {
		t.Errorf("list = %v", got)
	}
}

// A setting present but empty is not a value. HTCondor configuration is
// full of "KNOB =" written to mean "unset".
func TestKnobEmptyValueIsUnset(t *testing.T) {
	c := knobConfig(t, map[string]string{"S": "   "})
	if got := DefineString("S", "fallback", Startup, "").Get(c); got != "fallback" {
		t.Errorf("string = %q, want the default", got)
	}
}

func TestKnobReadsConfiguredValues(t *testing.T) {
	c := knobConfig(t, map[string]string{
		"S": "set", "B": "false", "D": "5m", "Z": "2G", "L": "a, b, c",
	})
	if got := DefineString("S", "x", Startup, "").Get(c); got != "set" {
		t.Errorf("string = %q", got)
	}
	if got := DefineBool("B", true, Startup, "").Get(c); got {
		t.Error("bool should be false")
	}
	if got := DefineDuration("D", time.Second, Startup, "").Get(c); got != 5*time.Minute {
		t.Errorf("duration = %v", got)
	}
	if got := DefineBytes("Z", 1, Startup, "").Get(c); got != 2<<30 {
		t.Errorf("bytes = %d", got)
	}
	if got := DefineList("L", nil, Startup, "").Get(c); len(got) != 3 {
		t.Errorf("list = %v", got)
	}
}

// Whitespace is not a separator: these settings are documented as
// comma-separated, and a name may contain a space.
func TestListKnobDoesNotSplitOnSpaces(t *testing.T) {
	c := knobConfig(t, map[string]string{"L": "my picker, other"})
	got := DefineList("L", nil, Startup, "").Get(c)
	if len(got) != 2 || got[0] != "my picker" {
		t.Errorf("list = %q", got)
	}
}

// A name holding a comma is why the JSON spelling exists.
func TestListKnobJSONSpelling(t *testing.T) {
	c := knobConfig(t, map[string]string{"L": `["one, with comma", "two"]`})
	got := DefineList("L", nil, Startup, "").Get(c)
	if len(got) != 2 || got[0] != "one, with comma" {
		t.Errorf("list = %v", got)
	}
}

// An empty list is a real answer -- "nobody" -- and must not be read as
// "unset, so use the default".
func TestListKnobEmptyJSONIsNotTheDefault(t *testing.T) {
	c := knobConfig(t, map[string]string{"L": "[]"})
	if got := DefineList("L", []string{"default"}, Startup, "").Get(c); len(got) != 0 {
		t.Errorf("list = %v, want empty", got)
	}
}

// The default must not be aliased to the caller: one caller appending
// to what Get returned would otherwise change what the next one sees.
func TestListKnobDefaultIsCopied(t *testing.T) {
	c := knobConfig(t, nil)
	k := DefineList("L", []string{"a"}, Startup, "")
	first := k.Get(c)
	first[0] = "mutated"
	if second := k.Get(c); second[0] != "a" {
		t.Errorf("default was aliased: %v", second)
	}
}

// Malformed values fall back to the default *and* are reported. The
// silent half is what let a typo in a timeout go unnoticed.
func TestKnobMalformedValuesFallBackAndReport(t *testing.T) {
	cases := []struct {
		name string
		set  string
		get  func(*Config) any
		want any
	}{
		{"D", "half an hour", func(c *Config) any {
			return DefineDuration("D", time.Minute, Startup, "").Get(c)
		}, time.Minute},
		{"B", "maybe", func(c *Config) any {
			return DefineBool("B", true, Startup, "").Get(c)
		}, true},
		{"Z", "lots", func(c *Config) any {
			return DefineBytes("Z", int64(7), Startup, "").Get(c)
		}, int64(7)},
		{"Z", "-1", func(c *Config) any {
			return DefineBytes("Z", int64(7), Startup, "").Get(c)
		}, int64(7)},
		{"Z", "0", func(c *Config) any {
			return DefineBytes("Z", int64(7), Startup, "").Get(c)
		}, int64(7)},
	}
	for _, tc := range cases {
		c := knobConfig(t, map[string]string{tc.name: tc.set})
		var reported int
		var sawRaw string
		c.SetKnobErrorHandler(func(k Knob, raw string, err error) {
			reported++
			sawRaw = raw
			if k.Name() != tc.name || err == nil {
				t.Errorf("handler got %q / %v", k.Name(), err)
			}
		})
		if got := tc.get(c); got != tc.want {
			t.Errorf("%s=%q: got %v, want the default %v", tc.name, tc.set, got, tc.want)
		}
		if reported != 1 {
			t.Errorf("%s=%q: handler called %d times, want 1", tc.name, tc.set, reported)
		}
		if sawRaw != tc.set {
			t.Errorf("%s=%q: handler saw raw %q", tc.name, tc.set, sawRaw)
		}
	}
}

// An absent setting is not malformed; falling back must stay quiet or
// every default becomes a warning at startup.
func TestKnobUnsetDoesNotReport(t *testing.T) {
	c := knobConfig(t, nil)
	c.SetKnobErrorHandler(func(Knob, string, error) {
		t.Error("handler called for an unset knob")
	})
	DefineDuration("D", time.Minute, Startup, "").Get(c)
	DefineBytes("Z", 1, Startup, "").Get(c)
	DefineList("L", nil, Startup, "").Get(c)
}

// With no handler installed a malformed value must still fall back
// rather than panic: a library embedding this package installs none.
func TestKnobWithoutHandler(t *testing.T) {
	c := knobConfig(t, map[string]string{"D": "nope"})
	if got := DefineDuration("D", time.Minute, Startup, "").Get(c); got != time.Minute {
		t.Errorf("duration = %v", got)
	}
}

// A nil Config is what a half-initialised daemon hands to a knob; it
// should read as "unset", not crash.
func TestKnobNilConfig(t *testing.T) {
	if got := DefineString("S", "d", Startup, "").Get(nil); got != "d" {
		t.Errorf("string = %q", got)
	}
}

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{
		"0": 0, "512": 512, "1K": 1 << 10, "1KB": 1 << 10,
		"1000M": 1000 << 20, "2G": 2 << 30, "1T": 1 << 40,
	} {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Errorf("ParseBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	// Surrounding whitespace is trimmed, as it is on every other value.
	if got, err := ParseBytes("  4m  "); err != nil || got != 4<<20 {
		t.Errorf("ParseBytes with padding = %d, %v", got, err)
	}
	for _, in := range []string{"", "lots", "1X", "9223372036854775807T"} {
		if _, err := ParseBytes(in); err == nil {
			t.Errorf("ParseBytes(%q) should have failed", in)
		}
	}
}

func TestScopeString(t *testing.T) {
	if Startup.String() != "startup" || Reconfig.String() != "reconfig" {
		t.Error("scope spellings are what the reference prints")
	}
}

func TestKnobCarriesItsDeclaration(t *testing.T) {
	k := DefineDuration("HTTP_API_READ_TIMEOUT", 30*time.Second, Reconfig, "How long a read may take.")
	if k.Name() != "HTTP_API_READ_TIMEOUT" || k.Scope() != Reconfig ||
		k.Doc() != "How long a read may take." || k.Default() != 30*time.Second {
		t.Errorf("declaration not carried: %+v", k)
	}
}

// A package that takes a narrow parameter interface rather than the
// whole Config must still be able to read a knob; webapi/connectors
// does exactly that.
func TestKnobReadsThroughANarrowSource(t *testing.T) {
	src := fakeSource{"S": "from the fake"}
	if got := DefineString("S", "default", Startup, "").Get(src); got != "from the fake" {
		t.Errorf("string = %q", got)
	}
	// A fake cannot be told about a malformed value, and must not be
	// asked to be: this should fall back quietly, not panic.
	if got := DefineDuration("D", time.Minute, Startup, "").Get(fakeSource{"D": "nope"}); got != time.Minute {
		t.Errorf("duration = %v", got)
	}
}

type fakeSource map[string]string

func (f fakeSource) Get(key string) (string, bool) { v, ok := f[key]; return v, ok }

// A typed nil *Config arrives as a non-nil Source, which is the shape
// that would panic if it were not checked for.
func TestKnobTypedNilConfig(t *testing.T) {
	var c *Config
	if got := DefineString("S", "d", Startup, "").Get(c); got != "d" {
		t.Errorf("string = %q", got)
	}
}

// A BoolKnob and Config.GetBool must resolve the same configuration the
// same way, including param_boolean's rule that a non-boolean starting
// with a literal takes that literal's value.
func TestBoolKnobMatchesGetBool(t *testing.T) {
	for _, def := range []bool{true, false} {
		for _, v := range []string{"true", "True", "1", "false", "0", "falsey",
			"false # not a comment", "truthy", "no", "yes", "maybe", "1 + 1 == 2"} {
			c := knobConfig(t, map[string]string{"B": v})
			want := c.GetBool("B", def)
			got := DefineBool("B", def, Startup, "").Get(c)
			if got != want {
				t.Errorf("B=%q def=%v: knob gave %v, GetBool gave %v", v, def, got, want)
			}
		}
	}
}

// Lookup is what tells "set to the default" from "unset"; overlaying
// operator settings on a shipped definition needs the difference.
func TestKnobLookupReportsPresence(t *testing.T) {
	set := knobConfig(t, map[string]string{"S": "x", "B": "true", "L": "[]", "D": "1s", "Z": "1K"})
	unset := knobConfig(t, nil)

	if _, ok := DefineString("S", "d", Startup, "").Lookup(set); !ok {
		t.Error("string: present but reported absent")
	}
	if v, ok := DefineString("S", "d", Startup, "").Lookup(unset); ok || v != "d" {
		t.Errorf("string: absent reported %q, %v", v, ok)
	}
	if _, ok := DefineBool("B", false, Startup, "").Lookup(set); !ok {
		t.Error("bool: present but reported absent")
	}
	if _, ok := DefineDuration("D", time.Minute, Startup, "").Lookup(set); !ok {
		t.Error("duration: present but reported absent")
	}
	if _, ok := DefineBytes("Z", 1, Startup, "").Lookup(set); !ok {
		t.Error("bytes: present but reported absent")
	}

	// An explicitly empty list is "none", and must not read as "unset".
	v, ok := DefineList("L", []string{"shipped"}, Startup, "").Lookup(set)
	if !ok || len(v) != 0 {
		t.Errorf("empty list: %q, %v; want present and empty", v, ok)
	}
	v, ok = DefineList("L", []string{"shipped"}, Startup, "").Lookup(unset)
	if ok || len(v) != 1 {
		t.Errorf("absent list: %q, %v; want absent and the default", v, ok)
	}
}

// OAuth2 scopes are space-delimited by RFC 6749, so reading them as a
// comma-separated list would give one scope with commas in it.
func TestWordsKnobSplitsOnWhitespace(t *testing.T) {
	c := knobConfig(t, map[string]string{"W": "openid profile  email\torg.cilogon.userinfo"})
	got := DefineWords("W", nil, Startup, "").Get(c)
	if len(got) != 4 || got[0] != "openid" || got[3] != "org.cilogon.userinfo" {
		t.Errorf("words = %q", got)
	}
	// A comma is part of a word here, not a separator -- which is what
	// makes this a different kind from ListKnob.
	if got := DefineWords("W", nil, Startup, "").Get(knobConfig(t, map[string]string{"W": "a,b"})); len(got) != 1 {
		t.Errorf("words = %q, want one word", got)
	}
	if got := DefineWords("W", []string{"d"}, Startup, "").Get(knobConfig(t, nil)); len(got) != 1 || got[0] != "d" {
		t.Errorf("default = %q", got)
	}
}
