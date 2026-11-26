package htcondor

import (
	"strings"
	"testing"
)

// adAttrs renders a job ad and returns attribute -> value.
func adAttrs(t *testing.T, submitFile string) map[string]string {
	t.Helper()
	sf, err := ParseSubmitFile(strings.NewReader(submitFile))
	if err != nil {
		t.Fatalf("ParseSubmitFile: %v", err)
	}
	ad, err := sf.MakeJobAd(JobID{Cluster: 1, Proc: 0}, nil)
	if err != nil {
		t.Fatalf("MakeJobAd: %v", err)
	}
	out := map[string]string{}
	for _, part := range strings.Split(strings.Trim(ad.String(), "[]"), ";") {
		if i := strings.Index(part, "="); i > 0 {
			out[strings.TrimSpace(part[:i])] = strings.TrimSpace(part[i+1:])
		}
	}
	return out
}

// TestSubmitDoesNotInheritConfigDefaults is the regression. The submit
// file's namespace is seeded with HTCondor's param_info defaults and
// lookups are case-insensitive, so a submit command sharing a name with
// a config knob picked up that knob's default. Job ads gained attributes
// the submit file never mentioned, and a schedd that protects one of
// them rejects the submission outright:
//
//	CommitTransaction failed: You are not allowed to change
//	MAX_TRANSFER_INPUT_MB. (error code 22)
func TestSubmitDoesNotInheritConfigDefaults(t *testing.T) {
	attrs := adAttrs(t, "executable = /bin/sleep\narguments = 60\nqueue 1\n")

	// Each of these has a same-named entry in param_defaults.go and was
	// leaking its value into every job ad.
	for _, attr := range []string{
		"MaxTransferInputMB",  // MAX_TRANSFER_INPUT_MB = -1
		"MaxTransferOutputMB", // MAX_TRANSFER_OUTPUT_MB = -1
		"MountUnderScratch",   // MOUNT_UNDER_SCRATCH = "/tmp,/var/tmp"
		"UserLog",             // LOG = $(LOCAL_DIR)/log
	} {
		if v, ok := attrs[attr]; ok {
			t.Errorf("%s = %s, but the submit file never set it", attr, v)
		}
	}
}

// TestSubmitHonorsExplicitValues is the other half: gating on "the file
// assigned this" must not stop the file from actually setting them.
func TestSubmitHonorsExplicitValues(t *testing.T) {
	attrs := adAttrs(t, `executable = /bin/sleep
max_transfer_input_mb = 512
max_transfer_output_mb = 256
log = /tmp/job.log
queue 1
`)
	for attr, want := range map[string]string{
		"MaxTransferInputMB":  "512",
		"MaxTransferOutputMB": "256",
	} {
		if got := attrs[attr]; got != want {
			t.Errorf("%s = %q, want %q", attr, got, want)
		}
	}
	if got := attrs["UserLog"]; !strings.Contains(got, "/tmp/job.log") {
		t.Errorf("UserLog = %q, want /tmp/job.log", got)
	}
}

// TestSubmitCommandIsCaseInsensitive keeps HTCondor's behavior: submit
// commands are matched without regard to case, so the gate must fold too
// or a MaxTransferInputMB written in mixed case would be dropped.
func TestSubmitCommandIsCaseInsensitive(t *testing.T) {
	attrs := adAttrs(t, "executable = /bin/sleep\nMax_Transfer_Input_MB = 128\nqueue 1\n")
	if got := attrs["MaxTransferInputMB"]; got != "128" {
		t.Errorf("MaxTransferInputMB = %q, want 128", got)
	}
}

// TestSubmitAssignmentInsideConditionalCounts covers a name assigned
// only inside an if/else. It must still read as submit-file-provided,
// or the config default silently returns.
func TestSubmitAssignmentInsideConditionalCounts(t *testing.T) {
	sf, err := ParseSubmitFile(strings.NewReader(`executable = /bin/sleep
if defined FOO
  max_transfer_input_mb = 64
endif
queue 1
`))
	if err != nil {
		t.Fatalf("ParseSubmitFile: %v", err)
	}
	if _, ok := sf.assigned["MAX_TRANSFER_INPUT_MB"]; !ok {
		t.Error("a name assigned inside a conditional was not recorded")
	}
}

// TestUntakenBranchDoesNotResurrectAConfigDefault is the other half of
// TestSubmitDoesNotInheritConfigDefaults, and the case that slipped
// through it.
//
// assignedNames counts a name assigned inside a conditional branch the
// executor did not run. The name then passes the gate while cfg holds no
// value the file wrote, so cfg.Get answers with the param_info default of
// the knob that shares the name -- which is exactly the bleed the gate
// exists to stop, arriving through the gate itself.
//
// condor_submit on the identical file writes none of these attributes.
func TestUntakenBranchDoesNotResurrectAConfigDefault(t *testing.T) {
	// FOO is undefined, so the branch does not run.
	attrs := adAttrs(t, `executable = /bin/sleep
if defined FOO
  max_transfer_input_mb = 64
  max_transfer_output_mb = 64
  mount_under_scratch = /scratch
  encrypt_execute_directory = true
endif
queue 1
`)
	for _, attr := range []string{
		"MaxTransferInputMB",
		"MaxTransferOutputMB",
		"MountUnderScratch",
		"EncryptExecuteDirectory",
	} {
		if got, ok := attrs[attr]; ok {
			t.Errorf("%s = %q, want absent: the branch did not run, so the file set no value "+
				"and this is the knob's default", attr, got)
		}
	}
}

// The taken branch must still come through, or the fix above would have
// turned a bleed into a silent loss -- which is the worse of the two.
func TestTakenBranchStillAssigns(t *testing.T) {
	attrs := adAttrs(t, `executable = /bin/sleep
FOO = 1
if defined FOO
  max_transfer_input_mb = 64
  mount_under_scratch = /scratch
endif
queue 1
`)
	if got := attrs["MaxTransferInputMB"]; got != "64" {
		t.Errorf("MaxTransferInputMB = %q, want 64 from the taken branch", got)
	}
	if got := attrs["MountUnderScratch"]; !strings.Contains(got, "/scratch") {
		t.Errorf("MountUnderScratch = %q, want the taken branch's /scratch", got)
	}
}

// And an else-branch that DOES run is a taken branch.
func TestElseBranchThatRunsStillAssigns(t *testing.T) {
	attrs := adAttrs(t, `executable = /bin/sleep
if defined NOPE
  max_transfer_input_mb = 1
else
  max_transfer_input_mb = 77
endif
queue 1
`)
	if got := attrs["MaxTransferInputMB"]; got != "77" {
		t.Errorf("MaxTransferInputMB = %q, want 77 from the else branch", got)
	}
}
