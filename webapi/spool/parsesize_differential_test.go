package spool

import (
	"testing"

	"github.com/htcondor/golang-htcondor/config"
)

// ParseSize and config.ParseBytes read the same thing out of the same
// configuration file, and HTTP_API_SPOOL_MAX_UPLOAD_BYTES is now read
// through the second where it used to be read through the first. Two
// parsers that disagree about one operator's value is the worst shape
// for that: the setting would be honoured before an upgrade and
// silently replaced by the default after it.
//
// So they are held to each other over everything an operator plausibly
// writes. Where they differ on purpose, the case says why.
func TestParseSizeAgreesWithParseBytes(t *testing.T) {
	agree := []string{
		"0", "1", "512", "1024",
		"100B", "100b",
		"4K", "4KB", "4KiB", "4kib",
		"1000M", "1000MB", "1000MiB",
		"2G", "2GB", "2GiB",
		"1T", "1TB", "1TiB",
		"  2G  ",
		// Both reject these, for reasons each words differently.
		"", "   ", "abc", "M", "GiB", "1.5G", "1000X", "1000Mi", "1e3",
		"99999999999999999999G",
	}
	for _, raw := range agree {
		a, aerr := ParseSize(raw)
		b, berr := config.ParseBytes(raw)
		switch {
		case aerr == nil && berr == nil:
			if a != b {
				t.Errorf("%q: ParseSize = %d but config.ParseBytes = %d", raw, a, b)
			}
		case aerr != nil && berr != nil:
			// Both refuse. The messages differ and that is fine.
		default:
			t.Errorf("%q: ParseSize err = %v, config.ParseBytes err = %v; one took it "+
				"and the other did not", raw, aerr, berr)
		}
	}
}

// The one deliberate difference. ParseSize reads the leading digits and
// so cannot see a sign at all; config.ParseBytes parses the whole
// number and hands back a negative, which BytesKnob then refuses as not
// positive. Both end at "the default applies", by different routes, so
// no operator sees a difference -- but the routes are different and a
// reader of either parser should know it.
func TestNegativeSizesEndTheSameWayByDifferentRoutes(t *testing.T) {
	const raw = "-1M"
	if _, err := ParseSize(raw); err == nil {
		t.Errorf("ParseSize(%q) should refuse a value that does not begin with a digit", raw)
	}
	n, err := config.ParseBytes(raw)
	if err != nil {
		t.Fatalf("config.ParseBytes(%q) = %v; it is expected to parse the sign", raw, err)
	}
	if n >= 0 {
		t.Fatalf("config.ParseBytes(%q) = %d, want a negative number", raw, n)
	}
	k := config.DefineBytes("HTTP_API_TEST_SIZE", 1<<20, config.Startup, "doc")
	if got := k.Get(sizeSource{"HTTP_API_TEST_SIZE": raw}); got != 1<<20 {
		t.Errorf("BytesKnob.Get(%q) = %d, want the default; a negative ceiling reads as "+
			"'refuse everything' to every comparison that uses it", raw, got)
	}
}

type sizeSource map[string]string

func (s sizeSource) Get(key string) (string, bool) { v, ok := s[key]; return v, ok }
