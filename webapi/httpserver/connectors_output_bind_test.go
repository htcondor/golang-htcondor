package httpserver

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Nominating a destination, and the checks that happen while someone is
// still watching. Everything here runs before any job is queued, which
// is the whole reason these are refusals rather than warnings.

// Whether a job's result file will come back has three answers, and the
// middle one is the one a two-valued check would get wrong.
func TestOutputFileComesBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		submit  string
		want    string // "ok", "warn" or "refuse"
		mention string
	}{
		{
			name:   "no transfer_output_files brings everything back",
			submit: "executable = /bin/sh\nqueue\n",
			want:   "ok",
		},
		{
			name:   "named outright",
			submit: "transfer_output_files = result.json\nqueue\n",
			want:   "ok",
		},
		{
			name:   "named among others",
			submit: "transfer_output_files = log.txt, result.json, plot.png\nqueue\n",
			want:   "ok",
		},
		{
			name:   "quoted",
			submit: `transfer_output_files = "result.json"` + "\nqueue\n",
			want:   "ok",
		},
		{
			name:    "a list that does not name it",
			submit:  "transfer_output_files = log.txt, plot.png\nqueue\n",
			want:    "refuse",
			mention: "log.txt, plot.png",
		},
		{
			name:    "set to nothing transfers nothing",
			submit:  "transfer_output_files =\nqueue\n",
			want:    "refuse",
			mention: "result.json",
		},
		{
			name:    "built from a macro cannot be checked",
			submit:  "transfer_output_files = out_$(sample).json\nqueue\n",
			want:    "warn",
			mention: "macro",
		},
		{
			name:   "a macro alongside the file itself is still fine",
			submit: "transfer_output_files = out_$(sample).json, result.json\nqueue\n",
			want:   "ok",
		},
		{
			name:   "the last assignment is the one in force",
			submit: "transfer_output_files = log.txt\ntransfer_output_files = result.json\nqueue\n",
			want:   "ok",
		},
		{
			name:    "and the last one can take it away",
			submit:  "transfer_output_files = result.json\ntransfer_output_files = log.txt\nqueue\n",
			want:    "refuse",
			mention: "log.txt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warning, err := outputFileComesBack(tc.submit, "result.json")
			got := "ok"
			switch {
			case err != nil:
				got = "refuse"
			case warning != "":
				got = "warn"
			}
			if got != tc.want {
				t.Fatalf("got %s (warning=%q err=%v), want %s", got, warning, err, tc.want)
			}
			if tc.mention == "" {
				return
			}
			said := warning
			if err != nil {
				said = err.Error()
			}
			if !strings.Contains(said, tc.mention) {
				t.Errorf("%q does not mention %q", said, tc.mention)
			}
		})
	}
}

// Reading a submit command: last wins, comments are not commands, and
// present-with-no-value is not the same as absent.
func TestSubmitCommandValue(t *testing.T) {
	for _, tc := range []struct {
		name      string
		submit    string
		want      string
		wantFound bool
	}{
		{"absent", "executable = /bin/sh\n", "", false},
		{"simple", "leave_in_queue = true\n", "true", true},
		{"colon form", "leave_in_queue : true\n", "true", true},
		{"case insensitive key", "LEAVE_IN_QUEUE = true\n", "true", true},
		{"indented", "  leave_in_queue = true\n", "true", true},
		{"commented out is absent", "# leave_in_queue = true\n", "", false},
		{"last wins", "leave_in_queue = true\nleave_in_queue = false\n", "false", true},
		{"present but empty", "leave_in_queue =\n", "", true},
		// A command whose name merely contains the key is a different
		// command; matching loosely here would read the wrong value.
		{"a longer name is not this one", "my_leave_in_queue = true\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, found := submitCommandValue(tc.submit, "leave_in_queue")
			if got != tc.want || found != tc.wantFound {
				t.Errorf("= (%q, %v), want (%q, %v)", got, found, tc.want, tc.wantFound)
			}
		})
	}
}

// A submission that nominates nothing is untouched: no session is
// needed, nothing is checked, and nothing is recorded.
func TestPrepareOutputBindingIgnoresAnAbsentSheet(t *testing.T) {
	h := &Handler{}
	b, warnings, err := h.prepareOutputBinding(t.Context(),
		httptest.NewRequestWithContext(t.Context(), "POST", "/", nil),
		nil, "executable = /bin/sh\nqueue\n")
	if err != nil || b != nil || len(warnings) > 0 {
		t.Errorf("= (%v, %v, %v), want all empty", b, warnings, err)
	}
}

// The refusals that need nothing but the request itself. Each one is a
// 400 before any job exists.
func TestPrepareOutputBindingRefusals(t *testing.T) {
	good := SubmitOutputSheet{
		Connector: "builtin:gdrive", FileID: "SHEET", Tab: "results", JobFile: "result.json",
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*SubmitOutputSheet)
		mention string
	}{
		{"not a connector id", func(s *SubmitOutputSheet) { s.Connector = "nonsense" }, "connector id"},
		{"no spreadsheet", func(s *SubmitOutputSheet) { s.FileID = "" }, "file_id"},
		{"no tab", func(s *SubmitOutputSheet) { s.Tab = "" }, "tab"},
		{"no results file", func(s *SubmitOutputSheet) { s.JobFile = "" }, "job_file"},
		// The sweep reads the sandbox's top level and nothing else, so a
		// path would name a file it will never find.
		{"a path, not a name", func(s *SubmitOutputSheet) { s.JobFile = "out/result.json" }, "not a path"},
		{"a windows path either", func(s *SubmitOutputSheet) { s.JobFile = `out\result.json` }, "not a path"},
		// Last, because it is reached only once the shape is right: a
		// binding is a standing instruction to spend a connector
		// credential, and only the session holding one can leave it.
		{"no browser session", func(*SubmitOutputSheet) {}, "browser session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sheet := good
			tc.mutate(&sheet)
			h := &Handler{}
			b, _, err := h.prepareOutputBinding(t.Context(),
				httptest.NewRequestWithContext(t.Context(), "POST", "/", nil),
				&sheet, "executable = /bin/sh\nqueue\n")
			if err == nil {
				t.Fatalf("accepted %+v, giving binding %+v", sheet, b)
			}
			if b != nil {
				t.Errorf("a refused nomination still produced a binding: %+v", b)
			}
			if !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("%q does not mention %q", err, tc.mention)
			}
		})
	}
}

// A header row has to have something in it. An all-blank first row is a
// tab somebody has not set up yet, not a header of empty columns.
func TestNonEmptyCells(t *testing.T) {
	if got := nonEmpty([]string{"", "  ", "\t"}); len(got) != 0 {
		t.Errorf("a blank row read as %d headers", len(got))
	}
	if got := nonEmpty([]string{"", "sample", ""}); len(got) != 1 || got[0] != "sample" {
		t.Errorf("= %q, want just the one heading", got)
	}
}

// The submit request's fields have to appear in the published schema.
//
// queue_vars and queue_rows were live and undocumented until this was
// written, which is the failure this guards: a client reads the schema,
// not the struct, so a field missing from it does not exist as far as
// anyone outside this repository is concerned.
func TestSubmitRequestFieldsAreDocumented(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(openAPISchema), &doc); err != nil {
		t.Fatalf("the published schema is not valid JSON: %v", err)
	}

	req, ok := doc.Components.Schemas["JobSubmitRequest"]
	if !ok {
		t.Fatal("JobSubmitRequest is not in the published schema")
	}
	for _, field := range []string{"submit_file", "queue_vars", "queue_rows", "output_sheet"} {
		if _, ok := req.Properties[field]; !ok {
			t.Errorf("JobSubmitRequest.%s is accepted by the handler but not documented", field)
		}
	}

	sheet, ok := doc.Components.Schemas["SubmitOutputSheet"]
	if !ok {
		t.Fatal("SubmitOutputSheet is not in the published schema")
	}
	for _, field := range []string{"connector", "file_id", "tab", "job_file"} {
		if _, ok := sheet.Properties[field]; !ok {
			t.Errorf("SubmitOutputSheet.%s is not documented", field)
		}
	}
	// All four are needed; a schema that leaves one optional invites a
	// request this server will refuse.
	if len(sheet.Required) != 4 {
		t.Errorf("SubmitOutputSheet requires %v, want all four fields", sheet.Required)
	}
}
