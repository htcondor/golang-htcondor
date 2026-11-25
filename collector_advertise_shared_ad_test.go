package htcondor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/PelicanPlatform/classad/classad"
	"github.com/htcondor/cedar/commands"
)

// adWithoutMyAddress is the case that matters: normalising an ad that
// already carries MyAddress is a pure read, so only an ad missing it can
// be written by a fan-out worker.
func adWithoutMyAddress(t *testing.T, name string) *classad.ClassAd {
	t.Helper()
	ad := classad.New()
	if err := ad.Set("MyType", "Generic"); err != nil {
		t.Fatal(err)
	}
	if err := ad.Set("Name", name); err != nil {
		t.Fatal(err)
	}
	return ad
}

// TestAdvertiseNormalisesThePrivateAdBeforeFanningOut: every
// per-collector goroutine must be handed a private ad that already
// carries MyAddress.
//
// The copy used to happen inside putAdvertiseAds, i.e. once per
// connection, on the single opts.PrivateAd every goroutine shares. That
// is a write to an unsynchronised classad.ClassAd from N goroutines --
// against each other, and against the siblings reading and serializing
// the same ad, which is the pair that can bring the process down with
// "concurrent map read and map write". Observed outcomes also included a
// private ad that lost MyAddress and one that carried it twice.
//
// Checked by what the workers see rather than by provoking the race, so
// it holds whether or not the run has the detector on.
func TestAdvertiseNormalisesThePrivateAdBeforeFanningOut(t *testing.T) {
	var mu sync.Mutex
	var sawMissing int
	var calls int

	saved := sendOneAd
	sendOneAd = func(_ *Collector, _ context.Context, _ string, _ commands.CommandType,
		_ *classad.ClassAd, opts *AdvertiseOptions) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if _, ok := opts.PrivateAd.Lookup("MyAddress"); !ok {
			sawMissing++
		}
		return nil
	}
	t.Cleanup(func() { sendOneAd = saved })

	private := adWithoutMyAddress(t, "slot1@host")
	c := NewCollector("cm-1.example.org:9618,cm-2.example.org:9618,cm-3.example.org:9618")
	if err := c.Advertise(context.Background(), testAd(t),
		&AdvertiseOptions{PrivateAd: private}); err != nil {
		t.Fatalf("Advertise: %v", err)
	}

	if calls != 3 {
		t.Fatalf("fan-out called %d times, want 3", calls)
	}
	if sawMissing != 0 {
		t.Errorf("%d of %d goroutines were handed a private ad with no MyAddress, "+
			"so each would have written the caller's ad itself", sawMissing, calls)
	}
	if addr, ok := private.EvaluateAttrString("MyAddress"); !ok || addr != "<127.0.0.1:9618>" {
		t.Errorf("private ad MyAddress = %q (set=%v), want the public ad's", addr, ok)
	}
}

// TestAdvertiseMultipleDoesNotRaceOnSharedAds: the batch fan-out hands
// every goroutine the same []*classad.ClassAd, and normalising an ad
// that arrived without MyAddress writes it. Done per worker, that is N
// goroutines mutating one ad's index map and Attributes slice.
//
// The collectors are unreachable on purpose: the normalisation happens
// before a connection is attempted, so a refused dial is enough to reach
// the code under test and keeps the test fast. Under -race (which CI
// runs for this module) the old code fails here; without it the test
// still pins that every ad comes back normalised exactly once.
func TestAdvertiseMultipleDoesNotRaceOnSharedAds(t *testing.T) {
	ads := make([]*classad.ClassAd, 0, 8)
	for i := range 8 {
		ads = append(ads, adWithoutMyAddress(t, fmt.Sprintf("slot%d@host", i)))
	}

	// Several addresses, all refusing immediately.
	c := NewCollector("127.0.0.1:1,127.0.0.1:2,127.0.0.1:3,127.0.0.1:4")
	_ = c.AdvertiseMultiple(context.Background(), ads, &AdvertiseOptions{})

	for i, ad := range ads {
		addr, ok := ad.EvaluateAttrString("MyAddress")
		if !ok {
			t.Errorf("ad %d came back with no MyAddress", i)
			continue
		}
		if addr != DefaultMyAddress {
			t.Errorf("ad %d MyAddress = %q, want %q", i, addr, DefaultMyAddress)
		}
	}
}

// TestAdvertiseMultipleReportsANilAdWithoutContactingCollectors: an ad
// that cannot be normalised fails on its own, before the fan-out, and
// says why -- rather than being reported as whatever each separate
// connection made of it.
func TestAdvertiseMultipleReportsANilAdWithoutContactingCollectors(t *testing.T) {
	ads := []*classad.ClassAd{adWithoutMyAddress(t, "good@host"), nil}

	c := NewCollector("127.0.0.1:1,127.0.0.1:2")
	errs := c.AdvertiseMultiple(context.Background(), ads, &AdvertiseOptions{})
	if len(errs) != 2 {
		t.Fatalf("got %d results, want 2", len(errs))
	}
	if errs[1] == nil || !strings.Contains(errs[1].Error(), "ad cannot be nil") {
		t.Errorf("nil ad: err = %v, want one naming the nil ad", errs[1])
	}
	// The good ad still went out (and failed, these collectors being
	// unreachable) -- one bad entry does not take the batch down.
	if errs[0] == nil {
		t.Error("the reachable-path ad reported success against refused collectors")
	}
}
