package main

import (
	"testing"

	"github.com/htcondor/golang-htcondor/config"
)

// HTTP_API_SPOOL_BUFFER_DIR is documented for operators, and until this
// was fixed the only config.Get for it in the tree was inside
// runDemoMode. An operator who set it on a real deployment got the
// in-memory path, a much smaller ceiling than they asked for, and no
// warning -- while a grep for the parameter name found the demo call
// site and made it look wired.
//
// Both server paths now read it through loadSpoolBufferDir, so they
// cannot answer differently.
func TestLoadSpoolBufferDir(t *testing.T) {
	cfg := config.NewEmpty()
	if got := loadSpoolBufferDir(cfg); got != "" {
		t.Errorf("unset = %q, want empty (buffer in memory)", got)
	}

	cfg.Set("HTTP_API_SPOOL_BUFFER_DIR", "/var/lib/condor/spoolbuf")
	if got := loadSpoolBufferDir(cfg); got != "/var/lib/condor/spoolbuf" {
		t.Errorf("set = %q", got)
	}

	// A value that expands to nothing is unset, as everywhere else.
	cfg.Set("HTTP_API_SPOOL_BUFFER_DIR", "   ")
	if got := loadSpoolBufferDir(cfg); got != "" {
		t.Errorf("whitespace = %q, want empty", got)
	}
}
