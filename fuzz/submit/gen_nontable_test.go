//go:build libcondor_utils

package fuzzsubmit

import (
	"strings"
	"testing"

	"github.com/htcondor/golang-htcondor/fuzz/submit/oracle"
)

// TestNonTableKeywordsAreStillAbsent checks that every hand-seeded keyword is
// still missing from HTCondor's own table.
//
// The seeds exist only because get_submit_keywords() does not report these.
// If upstream gives one a row, the vocabulary would learn it twice -- once
// from the table and once from here -- and the hand-written flags, which are a
// transcription rather than the authority, would start disagreeing with the
// real ones in whichever copy won. Failing here says to drop the seed.
func TestNonTableKeywordsAreStillAbsent(t *testing.T) {
	requireOracle(t)

	inTable := map[string]oracle.Keyword{}
	for _, kw := range oracle.Keywords() {
		if kw.Key != "" {
			inTable[strings.ToLower(kw.Key)] = kw
		}
	}
	for _, kw := range nonTableKeywords {
		if got, dup := inTable[strings.ToLower(kw.Key)]; dup {
			t.Errorf("%s now has a table row (%s, opts %#x): drop it from nonTableKeywords, the table is the authority",
				kw.Key, got.Attr, got.Opts)
		}
	}
	for key := range nonTableSkipped {
		if got, dup := inTable[strings.ToLower(key)]; dup {
			t.Errorf("%s now has a table row (%s, opts %#x): drop it from nonTableSkipped, the vocabulary gets it from the table",
				key, got.Attr, got.Opts)
		}
	}
}

// TestNonTableKeywordsReachTheVocabulary checks the seeds are actually in the
// vocabulary the generator draws from.
//
// Without this the whole file could be inert -- a list nothing reads is
// exactly the state it was written to fix -- and the failure would look like
// the keyword simply never coming up at random.
func TestNonTableKeywordsReachTheVocabulary(t *testing.T) {
	requireOracle(t)

	v := genVocab(t)
	for _, kw := range nonTableKeywords {
		if _, skipped := v.Skipped[kw.Key]; skipped {
			t.Errorf("%s was seeded and then skipped: %s", kw.Key, v.Skipped[kw.Key])
			continue
		}
		if !vocabHasKeyword(v, kw.Key) {
			t.Errorf("%s was seeded but is not in the vocabulary", kw.Key)
		}
	}
}

// vocabHasKeyword reports whether any universe's pool carries the keyword.
func vocabHasKeyword(v *Vocab, key string) bool {
	for _, entries := range v.byUniverse {
		for _, e := range entries {
			if strings.EqualFold(e.key, key) {
				return true
			}
		}
	}
	return false
}
