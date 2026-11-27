package main

import (
	"testing"

	"github.com/htcondor/golang-htcondor/config"
	"github.com/htcondor/golang-htcondor/webapi/knobs"
)

// reconfigExempt are declared settings that reconfigParams deliberately
// does not list, each for a reason that is not "nobody got round to it".
var reconfigExempt = map[string]string{
	// Connector and picker definitions are re-read wholesale on every
	// reconfigure, outside the name table, because declaring a new one
	// adds parameter names nothing was watching. The enumerating knobs
	// are therefore dynamic without being listed.
	knobs.ConnectorInstances.Name(): "connector definitions are re-read wholesale",
	knobs.PickerInstances.Name():    "picker definitions are re-read wholesale",
	// Deprecated and ignored: a change to it really does nothing, and
	// reporting it as needing a restart would promise that restarting
	// makes it work.
	knobs.OAuth2DBPath.Name(): "deprecated and ignored",
}

// A knob says when a change to it takes effect; reconfigParams decides
// what a reconfigure actually does about it. Those two have to agree, or
// the reference tells an operator one thing and the daemon does another.
func TestReconfigScopesMatchTheKnobs(t *testing.T) {
	byName := map[string]reconfigParam{}
	for _, p := range reconfigParams {
		byName[p.name] = p
		// An obsolete spelling is covered by the entry that supersedes
		// it: diff resolves it when the new setting is unset, so a
		// change to it IS applied and IS reported -- under the new
		// name, which is the point. Recording it as the superseding
		// entry keeps both checks below meaningful for it: still
		// listed, and still carrying an apply.
		if p.supersedes != "" {
			byName[p.supersedes] = p
		}
	}

	for _, k := range knobs.All {
		entry, listed := byName[k.Name()]
		if !listed {
			if _, ok := reconfigExempt[k.Name()]; !ok {
				t.Errorf("%s is declared but reconfigParams does not list it: a change to it "+
					"would neither be applied nor reported. Add an entry for it, or "+
					"an entry in reconfigExempt saying why not.", k.Name())
			}
			continue
		}
		if why, exempt := reconfigExempt[k.Name()]; exempt {
			t.Errorf("%s is both listed and exempt (%q); drop one", k.Name(), why)
		}
		switch k.Scope() {
		case config.Reconfig:
			if entry.apply == nil {
				t.Errorf("%s is declared scope: reconfig but its entry has no apply, so a "+
					"reconfigure reports it as needing a restart", k.Name())
			}
		case config.Startup:
			if entry.apply != nil {
				t.Errorf("%s is declared scope: startup but its entry applies a new value to "+
					"the running server; declare it scope: reconfig", k.Name())
			}
		}
	}
}

// Every exemption must name a setting that exists, or the list rots
// into a set of names that excuse nothing.
func TestReconfigExemptionsAreDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, k := range knobs.All {
		declared[k.Name()] = true
	}
	for name := range reconfigExempt {
		if !declared[name] {
			t.Errorf("reconfigExempt names %s, which no longer exists", name)
		}
	}
}
