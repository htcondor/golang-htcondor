package htcondor

import (
	"strings"
	"testing"
)

// TestQueueCounters pins $(Process), $(Step), $(ItemIndex) and $(Row)
// against condor_submit's own derivation of them.
//
// SubmitStepFromQArgs::next_impl computes all of them from one counter:
//
//	jid.proc   = initial_proc + iter_index
//	item_index = iter_index / step_size
//	step       = iter_index % step_size
//
// where step_size is the N of "queue N ...". $(Row) and $(ItemIndex) are
// the same macro -- SubmitMacroDefaults maps both to UnliveRowMacroDef --
// so they are never allowed to differ.
//
// Two things used to disagree with that. A bare "queue N" reported the
// proc counter as ItemIndex, where there is one item and item_index stays
// 0; and $(Row) fell back to the proc id for every iterator that did not
// spell it out, so it counted procs rather than items.
func TestQueueCounters(t *testing.T) {
	for _, c := range []struct {
		name  string
		queue string
		// One "Process Step ItemIndex Row" per proc, in order.
		want []string
	}{
		{
			// One item, N steps: ItemIndex and Row stay 0 while Step
			// and Process count together.
			name:  "bare queue N",
			queue: "queue 3",
			want:  []string{"0 0 0 0", "1 1 0 0", "2 2 0 0"},
		},
		{
			name:  "queue with no count",
			queue: "queue",
			want:  []string{"0 0 0 0"},
		},
		{
			// One step per item: ItemIndex and Row count items, Step
			// stays 0.
			name:  "one step per item",
			queue: "queue x in (a, b, c)",
			want:  []string{"0 0 0 0", "1 0 1 1", "2 0 2 2"},
		},
		{
			// Item-major with two steps each: Process counts procs,
			// Step counts within the item, ItemIndex and Row count
			// items.
			name:  "N steps per item",
			queue: "queue 2 x in (a, b)",
			want:  []string{"0 0 0 0", "1 1 0 0", "2 0 1 1", "3 1 1 1"},
		},
		{
			// Same, written without a loop variable.
			name:  "N steps per item, no loop variable",
			queue: "queue 2 in (a, b)",
			want:  []string{"0 0 0 0", "1 1 0 0", "2 0 1 1", "3 1 1 1"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			sf, err := ParseSubmitFile(strings.NewReader(
				"executable = /bin/sleep\n" +
					"arguments = $(Process) $(Step) $(ItemIndex) $(Row)\n" +
					c.queue + "\n"))
			if err != nil {
				t.Fatalf("parse %q: %v", c.queue, err)
			}
			res, err := sf.Submit(1)
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if len(res.ProcAds) != len(c.want) {
				t.Fatalf("%q produced %d procs, want %d", c.queue, len(res.ProcAds), len(c.want))
			}
			for i, want := range c.want {
				expr, ok := res.ProcAds[i].Lookup("Args")
				if !ok {
					t.Fatalf("proc %d has no Args", i)
				}
				got, err := expr.Eval(nil).StringValue()
				if err != nil {
					t.Fatalf("proc %d: Args is not a string: %v", i, err)
				}
				if got != want {
					t.Errorf("%q proc %d: Process/Step/ItemIndex/Row = %q, want %q",
						c.queue, i, got, want)
				}
			}
		})
	}
}
