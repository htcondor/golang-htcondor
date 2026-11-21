package connectors

import (
	"strings"
	"testing"
)

// A chooser is configured in its own right, not as a flag on a
// connector. The test that matters most is the one about the developer
// key: it is the setting that proved a chooser is not a connector
// property, because a boolean on a credential had nowhere to put it.

func TestPickerConfigReadsAChooser(t *testing.T) {
	src := NewConfigSource(fakeParams{
		"HTTP_API_PICKERS":                "drive",
		"HTTP_API_PICKER_DRIVE_PROVIDER":  "gdrive",
		"HTTP_API_PICKER_DRIVE_CONNECTOR": "builtin:gdrive",
		"HTTP_API_PICKER_DRIVE_API_KEY":   "dev-key",
		"HTTP_API_PICKER_DRIVE_APP_ID":    "123456",
	})

	pickers := src.Pickers()
	if len(pickers) != 1 {
		t.Fatalf("pickers = %+v, want one", pickers)
	}
	p := pickers[0]
	if p.Provider != "gdrive" {
		t.Errorf("Provider = %q", p.Provider)
	}
	if p.Connector.String() != "builtin:gdrive" {
		t.Errorf("Connector = %q", p.Connector)
	}
	// The pair that does not fit on a connector.
	if p.APIKey != "dev-key" || p.AppID != "123456" {
		t.Errorf("APIKey = %q AppID = %q", p.APIKey, p.AppID)
	}
}

// The name is the provider unless told otherwise, so the common case is
// one line of configuration.
func TestPickerNameDefaultsToTheProvider(t *testing.T) {
	src := NewConfigSource(fakeParams{
		"HTTP_API_PICKERS":                 "gdrive",
		"HTTP_API_PICKER_GDRIVE_CONNECTOR": "builtin:gdrive",
		"HTTP_API_PICKER_GDRIVE_API_KEY":   "k",
	})
	pickers := src.Pickers()
	if len(pickers) != 1 || pickers[0].Provider != "gdrive" {
		t.Fatalf("pickers = %+v", pickers)
	}
	if len(pickers[0].Problems()) != 0 {
		t.Errorf("a one-line chooser reported problems: %v", pickers[0].Problems())
	}
}

// Two choosers for one provider is a real configuration: two labs,
// two client registrations, one Google Drive.
func TestTwoChoosersForOneProvider(t *testing.T) {
	src := NewConfigSource(fakeParams{
		"HTTP_API_PICKERS":                "lab-a, lab-b",
		"HTTP_API_PICKER_LAB_A_PROVIDER":  "gdrive",
		"HTTP_API_PICKER_LAB_A_CONNECTOR": "site:lab-a",
		"HTTP_API_PICKER_LAB_A_API_KEY":   "ka",
		"HTTP_API_PICKER_LAB_B_PROVIDER":  "gdrive",
		"HTTP_API_PICKER_LAB_B_CONNECTOR": "site:lab-b",
		"HTTP_API_PICKER_LAB_B_API_KEY":   "kb",
	})
	pickers := src.Pickers()
	if len(pickers) != 2 {
		t.Fatalf("pickers = %+v, want two", pickers)
	}
	if pickers[0].Connector == pickers[1].Connector {
		t.Error("two choosers for one provider share a connector; they should not have to")
	}
}

// A chooser with no credential cannot show anybody's files.
func TestAChooserWithNoConnectorIsAProblem(t *testing.T) {
	src := NewConfigSource(fakeParams{
		"HTTP_API_PICKERS":               "drive",
		"HTTP_API_PICKER_DRIVE_API_KEY":  "k",
		"HTTP_API_PICKER_DRIVE_PROVIDER": "gdrive",
	})
	problems := src.Pickers()[0].Problems()
	if len(problems) == 0 {
		t.Fatal("a chooser with no connector reported no problem")
	}
	if !strings.Contains(strings.Join(problems, "; "), "CONNECTOR") {
		t.Errorf("problems = %v, want the parameter to set named", problems)
	}
}

// The developer key is the setting that showed a chooser is not a
// connector property: it belongs to the provider's project, and a
// boolean on a credential had nowhere to hold it.
func TestAChooserThatNeedsADeveloperKeySaysSo(t *testing.T) {
	src := NewConfigSource(fakeParams{
		"HTTP_API_PICKERS":                "drive",
		"HTTP_API_PICKER_DRIVE_PROVIDER":  "gdrive",
		"HTTP_API_PICKER_DRIVE_CONNECTOR": "builtin:gdrive",
	})
	problems := src.Pickers()[0].Problems()
	joined := strings.Join(problems, "; ")
	if !strings.Contains(joined, "developer key") {
		t.Fatalf("problems = %v, want the missing developer key named", problems)
	}
	if !strings.Contains(joined, "API_KEY") {
		t.Errorf("problems = %v, want the parameter to set named", problems)
	}
}

func TestAnUnknownProviderIsAProblem(t *testing.T) {
	src := NewConfigSource(fakeParams{
		"HTTP_API_PICKERS":               "thing",
		"HTTP_API_PICKER_THING_PROVIDER": "nosuch",
	})
	problems := src.Pickers()[0].Problems()
	if len(problems) == 0 {
		t.Fatal("an unknown provider reported no problem")
	}
	if !strings.Contains(problems[0], "gdrive") {
		t.Errorf("problems = %v, want the known providers listed", problems)
	}
}

// Only what HTTP_API_PICKERS lists. A half-written chooser is not a
// chooser -- the listing is the operator saying this one is ready, the
// same rule connectors follow.
func TestOnlyListedChoosersAreRead(t *testing.T) {
	src := NewConfigSource(fakeParams{
		"HTTP_API_PICKERS":                 "drive",
		"HTTP_API_PICKER_DRIVE_PROVIDER":   "gdrive",
		"HTTP_API_PICKER_HALFDONE_API_KEY": "typed-but-not-listed",
	})
	if n := len(src.Pickers()); n != 1 {
		t.Errorf("read %d choosers, want only the listed one", n)
	}
}

func TestNoChoosersConfiguredIsNone(t *testing.T) {
	if n := len(NewConfigSource(fakeParams{}).Pickers()); n != 0 {
		t.Errorf("read %d choosers from an empty configuration", n)
	}
	if n := len(NewConfigSource(nil).Pickers()); n != 0 {
		t.Errorf("read %d choosers with no configuration at all", n)
	}
}
