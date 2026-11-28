package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A knob is a configuration setting this program knows about: its name,
// its type, its default and whether a reconfigure picks it up.
//
// The point of the type is what it refuses. Nothing outside a generated
// knob table can write a Knob down, so a read can only name a setting
// that is declared -- and a declaration carries the documentation, so a
// setting cannot exist in the code and be missing from the reference.
// Get(string) stays for what this package is otherwise for: reading
// arbitrary HTCondor configuration, which has no such table.
//
// The Define* constructors below are exported because generated tables
// live in other modules (webapi/knobs, say) and cannot reach unexported
// fields. They are for generated code; hand-written callers use the
// generated identifiers.

// Source is what a knob reads from. *Config is one, and so is any
// other type that answers a lookup the same way -- several packages
// take a narrow parameter interface rather than the whole Config, and a
// knob has to be readable through those too.
type Source interface {
	Get(key string) (string, bool)
}

// knobErrorSource is a Source that can be told about a malformed value.
// The method is unexported, so only *Config implements it: a narrow
// test fake simply gets no reports.
type knobErrorSource interface {
	reportKnobError(k Knob, raw string, err error)
}

// Scope says when a change to a setting takes effect.
type Scope int

const (
	// Startup means the value is read once and a change needs a
	// restart.
	Startup Scope = iota
	// Reconfig means a reconfigure installs the new value on the
	// running server.
	Reconfig
)

// String renders a scope the way the configuration reference spells it.
func (s Scope) String() string {
	if s == Reconfig {
		return "reconfig"
	}
	return "startup"
}

// Knob is what every typed knob carries: identity, documentation and
// reload scope. It is embedded, not used directly.
type Knob struct {
	name  string
	doc   string
	scope Scope
}

// Name is the configuration parameter name, e.g. HTTP_API_READ_TIMEOUT.
func (k Knob) Name() string { return k.name }

// Doc is the one-paragraph description from the knob table, which is
// also what the configuration reference prints.
func (k Knob) Doc() string { return k.doc }

// Scope reports whether a reconfigure picks this setting up.
func (k Knob) Scope() Scope { return k.scope }

// raw looks the setting up through the ordinary scoped lookup, so a
// knob read obeys subsystem and -local-name scoping exactly as
// Get(string) does. Reimplementing the lookup here is the obvious way
// for the two to drift, so it is not reimplemented.
func (k Knob) raw(src Source) (string, bool) {
	if src == nil {
		return "", false
	}
	// A typed nil *Config reaches here as a non-nil interface.
	if c, isConfig := src.(*Config); isConfig && c == nil {
		return "", false
	}
	v, ok := src.Get(k.name)
	if !ok {
		return "", false
	}
	if v = strings.TrimSpace(v); v == "" {
		return "", false
	}
	return v, true
}

// malformed reports a value that is set but cannot be read as its type.
// The default applies either way; this only decides whether anyone is
// told. A silent fallback is how a typo in a timeout goes unnoticed
// until someone wonders why the setting does nothing.
func (k Knob) malformed(src Source, raw string, err error) {
	if r, ok := src.(knobErrorSource); ok {
		r.reportKnobError(k, raw, err)
	}
}

// KnobErrorFunc is told about a setting whose value could not be read
// as its type. The default has already been applied by the time it is
// called.
type KnobErrorFunc func(k Knob, raw string, err error)

// SetKnobErrorHandler installs the handler for malformed values. A
// daemon points it at its logger once, instead of every knob read
// carrying its own parse-and-warn.
func (c *Config) SetKnobErrorHandler(f KnobErrorFunc) { c.knobErr = f }

// reportKnobError satisfies knobErrorSource.
func (c *Config) reportKnobError(k Knob, raw string, err error) {
	if c != nil && c.knobErr != nil {
		c.knobErr(k, raw, err)
	}
}

// StringKnob is a setting read as text.
type StringKnob struct {
	Knob
	def string
}

// DefineString declares a string setting. For generated knob tables.
func DefineString(name, def string, scope Scope, doc string) StringKnob {
	return StringKnob{Knob{name, doc, scope}, def}
}

// Get returns the configured value, or the default when it is unset or
// empty.
func (k StringKnob) Get(c Source) string {
	if v, ok := k.raw(c); ok {
		return v
	}
	return k.def
}

// Lookup is Get, and also reports whether the setting was present.
func (k StringKnob) Lookup(c Source) (string, bool) {
	if v, found := k.raw(c); found {
		return v, true
	}
	return k.def, false
}

// Default is the value that applies when the setting is absent.
func (k StringKnob) Default() string { return k.def }

// BoolKnob is a setting read as a boolean, in any spelling HTCondor
// accepts.
type BoolKnob struct {
	Knob
	def bool
}

// DefineBool declares a boolean setting. For generated knob tables.
func DefineBool(name string, def bool, scope Scope, doc string) BoolKnob {
	return BoolKnob{Knob{name, doc, scope}, def}
}

// Get returns the configured value, or the default when it is unset or
// cannot be read as a boolean.
func (k BoolKnob) Get(c Source) bool {
	v, found := k.raw(c)
	if !found {
		return k.def
	}
	// Resolved exactly as Config.GetBool does, down to the case
	// param_boolean inherits: a value that is not a boolean but starts
	// with a literal ("falsey", "false # not a comment") takes that
	// literal's value, and only a value starting with none takes the
	// default. Two ways to read the same setting that disagree would
	// be worse than either.
	b, ok, literal := stringIsBooleanParam(v)
	if ok {
		return b
	}
	if !literal {
		b = k.def
	}
	k.malformed(c, v, fmt.Errorf("not a valid boolean; set it to True or False"))
	return b
}

// Lookup is Get, and also reports whether the setting was present. It
// is for a caller that must distinguish "set to the default" from
// "unset" -- overlaying operator settings on a shipped definition, say,
// where an explicitly empty list means "none" and an absent one means
// "keep the shipped ones".
func (k BoolKnob) Lookup(c Source) (bool, bool) {
	if _, found := k.raw(c); !found {
		return k.def, false
	}
	return k.Get(c), true
}

// Default is the value that applies when the setting is absent.
func (k BoolKnob) Default() bool { return k.def }

// DurationKnob is a setting read as a Go duration, e.g. 30s or 5m.
type DurationKnob struct {
	Knob
	def time.Duration
}

// DefineDuration declares a duration setting. For generated knob
// tables.
func DefineDuration(name string, def time.Duration, scope Scope, doc string) DurationKnob {
	return DurationKnob{Knob{name, doc, scope}, def}
}

// Get returns the configured value, or the default when it is unset or
// unparseable.
func (k DurationKnob) Get(c Source) time.Duration {
	v, ok := k.raw(c)
	if !ok {
		return k.def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		k.malformed(c, v, err)
		return k.def
	}
	return d
}

// Lookup is Get, and also reports whether the setting was present.
func (k DurationKnob) Lookup(c Source) (time.Duration, bool) {
	if _, found := k.raw(c); !found {
		return k.def, false
	}
	return k.Get(c), true
}

// Default is the value that applies when the setting is absent.
func (k DurationKnob) Default() time.Duration { return k.def }

// BytesKnob is a setting read as a size: a plain count, or a number
// with a unit (1000M, 2G). Units are powers of 1024, as elsewhere in
// HTCondor.
type BytesKnob struct {
	Knob
	def int64
}

// DefineBytes declares a size setting. For generated knob tables.
func DefineBytes(name string, def int64, scope Scope, doc string) BytesKnob {
	return BytesKnob{Knob{name, doc, scope}, def}
}

// Get returns the configured size, or the default when it is unset,
// unparseable or not positive. A size that is zero or negative is a
// mistake rather than an instruction to lift the limit, so the default
// applies and the handler hears about it.
func (k BytesKnob) Get(c Source) int64 {
	v, ok := k.raw(c)
	if !ok {
		return k.def
	}
	n, err := ParseBytes(v)
	if err != nil {
		k.malformed(c, v, err)
		return k.def
	}
	if n <= 0 {
		k.malformed(c, v, fmt.Errorf("size must be positive, got %d", n))
		return k.def
	}
	return n
}

// Lookup is Get, and also reports whether the setting was present.
func (k BytesKnob) Lookup(c Source) (int64, bool) {
	if _, found := k.raw(c); !found {
		return k.def, false
	}
	return k.Get(c), true
}

// Default is the value that applies when the setting is absent.
func (k BytesKnob) Default() int64 { return k.def }

// IntKnob is a setting read as a whole number: a count, a ceiling, or a
// size already denominated in some unit the setting's name states
// (HTTP_API_BUILD_MAX_MEMORY_MB is in MiB). It is deliberately not
// BytesKnob, which reads 2048 as 2048 bytes; a setting whose name ends
// in _MB means 2048 of them.
type IntKnob struct {
	Knob
	def int64
}

// DefineInt declares a whole-number setting. For generated knob tables.
func DefineInt(name string, def int64, scope Scope, doc string) IntKnob {
	return IntKnob{Knob{name, doc, scope}, def}
}

// Get returns the configured number, or the default when it is unset or
// unparseable.
//
// A negative number is returned as written rather than refused here:
// zero means "no limit" to some of these settings and "unset" to
// others, and which of those a negative value is closer to is the
// setting's business, not the type's. A caller that has no meaning for
// one checks after reading.
func (k IntKnob) Get(c Source) int64 {
	v, ok := k.raw(c)
	if !ok {
		return k.def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		// The parse error names strconv and the function, which is not
		// what an operator wants to read about their configuration.
		k.malformed(c, v, fmt.Errorf("not a whole number"))
		return k.def
	}
	return n
}

// Lookup is Get, and also reports whether the setting was present.
func (k IntKnob) Lookup(c Source) (int64, bool) {
	if _, found := k.raw(c); !found {
		return k.def, false
	}
	return k.Get(c), true
}

// Default is the value that applies when the setting is absent.
func (k IntKnob) Default() int64 { return k.def }

// ListKnob is a setting read the way HTCondor reads a list: separated
// by commas OR whitespace, or written as a JSON array.
//
// This is the default kind for a list, because it is upstream's
// default. HTCondor's split() takes delim=", \t\r\n"
// (condor_utils/stl_string_utils.h), so an operator who has learned
// that a condor list takes either separator is right about this server
// too. Upstream narrows to commas alone by passing an explicit
// delimiter, and so does this package: see CommaListKnob, which is for
// the values that can contain a space.
//
// A JSON array is accepted for a value no separator rule can express.
type ListKnob struct {
	Knob
	def []string
}

// DefineList declares a list setting. For generated knob tables.
func DefineList(name string, def []string, scope Scope, doc string) ListKnob {
	return ListKnob{Knob{name, doc, scope}, def}
}

// Get returns the configured list, or the default when it is unset. An
// empty list is a real answer -- "nobody" -- and is not the default.
func (k ListKnob) Get(c Source) []string {
	v, ok := k.raw(c)
	if !ok {
		return append([]string(nil), k.def...)
	}
	out, err := ParseList(v)
	if err != nil {
		k.malformed(c, v, err)
		return append([]string(nil), k.def...)
	}
	return out
}

// Lookup is Get, and also reports whether the setting was present. An
// explicitly empty list reports true with no entries, which is how a
// caller tells "none" from "nothing said".
func (k ListKnob) Lookup(c Source) ([]string, bool) {
	if _, found := k.raw(c); !found {
		return append([]string(nil), k.def...), false
	}
	return k.Get(c), true
}

// Default is the value that applies when the setting is absent.
func (k ListKnob) Default() []string { return append([]string(nil), k.def...) }

// CommaListKnob is a setting read as a list separated by commas only,
// or written as a JSON array.
//
// The exception to ListKnob, for a list whose VALUES may contain a
// space: a group name is the case it exists for. Reading "smith lab,
// jones lab" with whitespace as a separator would produce four groups
// that match nothing, and it would do it silently, because a group
// nobody is in is indistinguishable from a group that does not exist.
//
// Upstream does the same thing the same way round -- split() defaults
// to comma-or-whitespace and takes an explicit "," where a value can
// hold a space -- so this is the narrow kind on purpose, and a new
// list setting should reach for ListKnob first.
type CommaListKnob struct {
	Knob
	def []string
}

// DefineCommaList declares a comma-separated list setting. For
// generated knob tables.
func DefineCommaList(name string, def []string, scope Scope, doc string) CommaListKnob {
	return CommaListKnob{Knob{name, doc, scope}, def}
}

// Get returns the configured list, or the default when it is unset. An
// empty list is a real answer -- "nobody" -- and is not the default.
func (k CommaListKnob) Get(c Source) []string {
	v, ok := k.raw(c)
	if !ok {
		return append([]string(nil), k.def...)
	}
	out, err := ParseCommaList(v)
	if err != nil {
		k.malformed(c, v, err)
		return append([]string(nil), k.def...)
	}
	return out
}

// Lookup is Get, and also reports whether the setting was present.
func (k CommaListKnob) Lookup(c Source) ([]string, bool) {
	if _, found := k.raw(c); !found {
		return append([]string(nil), k.def...), false
	}
	return k.Get(c), true
}

// Default is the value that applies when the setting is absent.
func (k CommaListKnob) Default() []string { return append([]string(nil), k.def...) }

// WordsKnob is a setting read as a whitespace-separated list. OAuth2
// scopes are the case it exists for: RFC 6749 delimits them with
// spaces, so a comma-separated reading would be wrong and a site
// following the spec would get one scope with commas in it.
type WordsKnob struct {
	Knob
	def []string
}

// DefineWords declares a whitespace-separated list setting. For
// generated knob tables.
func DefineWords(name string, def []string, scope Scope, doc string) WordsKnob {
	return WordsKnob{Knob{name, doc, scope}, def}
}

// Get returns the configured words, or the default when unset.
func (k WordsKnob) Get(c Source) []string {
	v, ok := k.raw(c)
	if !ok {
		return append([]string(nil), k.def...)
	}
	out := strings.Fields(v)
	if len(out) == 0 {
		return append([]string(nil), k.def...)
	}
	return out
}

// Lookup is Get, and also reports whether the setting was present.
func (k WordsKnob) Lookup(c Source) ([]string, bool) {
	if _, found := k.raw(c); !found {
		return append([]string(nil), k.def...), false
	}
	return k.Get(c), true
}

// Default is the value that applies when the setting is absent.
func (k WordsKnob) Default() []string { return append([]string(nil), k.def...) }

// ParseBytes reads a byte count with an optional unit suffix. Units are
// powers of 1024: K, M, G, T, with an optional trailing B.
func ParseBytes(s string) (int64, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, fmt.Errorf("empty size")
	}
	// Both spellings of each unit mean the same power of 1024: a site
	// that writes MB means what one that writes MiB means, and
	// refusing one of them would be pedantry in a config file. The "I"
	// comes off only when a "B" did, so a bare "1000I" stays the
	// nonsense it is rather than becoming a byte count.
	t = strings.ToUpper(t)
	if cut, hadB := strings.CutSuffix(t, "B"); hadB {
		t = cut
		if cut, hadI := strings.CutSuffix(t, "I"); hadI {
			t = cut
		}
	}
	mult := int64(1)
	if t != "" {
		switch t[len(t)-1] {
		case 'K':
			mult, t = 1<<10, t[:len(t)-1]
		case 'M':
			mult, t = 1<<20, t[:len(t)-1]
		case 'G':
			mult, t = 1<<30, t[:len(t)-1]
		case 'T':
			mult, t = 1<<40, t[:len(t)-1]
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	if mult > 1 && (n > (1<<62)/mult || n < -(1<<62)/mult) {
		return 0, fmt.Errorf("%q overflows", s)
	}
	return n * mult, nil
}

// ParseList reads a list the way HTCondor does: a JSON array, or names
// separated by commas or whitespace. Empty entries are dropped, so a
// trailing comma is not an empty name.
//
// The separator set matches upstream's split() default, which is what
// makes "a, b" and "a b" mean the same thing here as they do in a
// condor configuration file.
func ParseList(s string) ([]string, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil, nil
	}
	if strings.HasPrefix(t, "[") {
		return parseJSONList(s)
	}
	return SplitConfigList(t), nil
}

// ParseCommaList reads a list separated by commas ONLY, or a JSON
// array. For values that may contain a space -- a group name, a
// display name -- where whitespace cannot be a separator.
//
// The narrow case, deliberately: upstream reaches for an explicit ","
// delimiter in the same situation rather than making it the default.
func ParseCommaList(s string) ([]string, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil, nil
	}
	if strings.HasPrefix(t, "[") {
		return parseJSONList(s)
	}
	var out []string
	for _, f := range strings.Split(t, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out, nil
}

// parseJSONList reads the JSON-array spelling both list kinds accept.
func parseJSONList(s string) ([]string, error) {
	var out []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), &out); err != nil {
		return nil, fmt.Errorf("%q is not a JSON array of strings: %w", s, err)
	}
	return out, nil
}
