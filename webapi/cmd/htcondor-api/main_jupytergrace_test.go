package main

import (
	"testing"

	"github.com/htcondor/golang-htcondor/config"
	"github.com/htcondor/golang-htcondor/webapi/httpserver"
	"github.com/htcondor/golang-htcondor/webapi/knobs"
)

// A grace period cannot be turned off or made unbounded: zero, negative and
// unreadable fall back to the default, and anything past the ceiling is
// clamped to it.
func TestLoadJupyterGraceSecIsAlwaysBounded(t *testing.T) {
	// The defaults come from the knobs, so this also says that what the
	// configuration reference prints is what applies.
	params := []struct {
		knob          config.IntKnob
		def, ceiling  int
		explicitValue string
		explicit      int
	}{
		{knobs.JupyterReconnectGraceSec, httpserver.DefaultJupyterReconnectGraceSec, httpserver.MaxJupyterReconnectGraceSec, "120", 120},
		{knobs.JupyterStartGraceSec, httpserver.DefaultJupyterStartGraceSec, httpserver.MaxJupyterStartGraceSec, "3600", 3600},
	}
	for _, p := range params {
		cases := map[string]struct {
			value string
			set   bool
			want  int
		}{
			"unset":      {want: p.def},
			"explicit":   {value: p.explicitValue, set: true, want: p.explicit},
			"zero":       {value: "0", set: true, want: p.def},
			"negative":   {value: "-5", set: true, want: p.def},
			"unreadable": {value: "4h", set: true, want: p.def},
			"too long":   {value: "999999999", set: true, want: p.ceiling},
		}
		// The knob table and the package constant are two statements
		// of one number; the reference prints the first and the
		// fallback uses neither unless they agree.
		if got := int(p.knob.Default()); got != p.def {
			t.Errorf("%s declares a default of %d and the package constant is %d; "+
				"the configuration reference would print the wrong number",
				p.knob.Name(), got, p.def)
		}
		for label, c := range cases {
			t.Run(p.knob.Name()+"/"+label, func(t *testing.T) {
				cfg := config.NewEmpty()
				if c.set {
					cfg.Set(p.knob.Name(), c.value)
				}
				if got := loadJupyterGraceSec(cfg, ccbTestLogger(t), p.knob, p.ceiling); got != c.want {
					t.Errorf("loadJupyterGraceSec(%q) = %d, want %d", c.value, got, c.want)
				}
			})
		}
	}
}
