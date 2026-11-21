package connectors

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Naming a connector's file as a job input, and taking it back out
// again for the spool path.
//
//	transfer_input_files = connector://webapi_builtin_gdrive/FILEID/data.csv
//
// The authority is the credd SERVICE name, which is already built to be
// a safe identifier -- no colons, so url.Parse does not read half of it
// as a port -- and is the same string the credential is stored under.
// The last path component is the file's name in the sandbox, because
// that is where HTCondor takes it from: "the destination file is given
// by the file name within the URL" (file-transfer.rst).
//
// In SPOOL mode the server fetches these itself and the reference is
// replaced by the bare name, because nothing advertises a `connector`
// transfer method -- left in place, the URL would add a
// TARGET.HasFileTransferPluginMethods clause nothing can satisfy and
// the job would never match anything. Rewriting is not a nicety.

// InputScheme is the URL scheme that names a connector's file.
const InputScheme = "connector"

// InputRef is one connector-sourced job input.
type InputRef struct {
	// Service is the credd service name in the URL's authority, which
	// is what identifies the connector.
	Service string
	// FileID is the provider's id for the file.
	FileID string
	// Name is what the file is called in the job's sandbox.
	Name string
	// Raw is the reference exactly as the submit file wrote it.
	Raw string
}

// inputURL matches a connector reference wherever it appears in a
// submit file.
//
// Deliberately a token match on the raw text rather than a parse of the
// submit language: replacing one token in place keeps every comment,
// line continuation and macro in the file exactly as the submitter
// wrote them, where regenerating the file from a parse would not. The
// scheme is distinctive enough that a false positive would have to be
// somebody writing this URL where they did not mean one.
var inputURL = regexp.MustCompile(`\bconnector://[^\s,;"']+`)

// ParseInputURL reads one reference.
func ParseInputURL(raw string) (InputRef, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return InputRef{}, fmt.Errorf("%q is not a usable connector reference: %w", raw, err)
	}
	if u.Scheme != InputScheme {
		return InputRef{}, fmt.Errorf("%q is not a %s:// reference", raw, InputScheme)
	}
	if u.Host == "" {
		return InputRef{}, fmt.Errorf("%q names no connector; expected %s://<service>/<id>/<name>",
			raw, InputScheme)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return InputRef{}, fmt.Errorf(
			"%q does not name a file; expected %s://<service>/<id>/<name>", raw, InputScheme)
	}
	return InputRef{
		Service: u.Host,
		FileID:  parts[0],
		Name:    parts[1],
		Raw:     raw,
	}, nil
}

// SplitSpoolInputs finds every connector reference in a submit file,
// replaces it with the name its spooled copy will have, and returns
// what has to be fetched.
//
// The rewritten file is what goes to the schedd; the refs are what this
// server fetches into the spool before releasing the job.
func SplitSpoolInputs(submitFile string) (string, []InputRef, error) {
	var refs []InputRef
	var parseErr error

	rewritten := inputURL.ReplaceAllStringFunc(submitFile, func(raw string) string {
		ref, err := ParseInputURL(raw)
		if err != nil {
			if parseErr == nil {
				parseErr = err
			}
			return raw
		}
		refs = append(refs, ref)
		return ref.Name
	})
	if parseErr != nil {
		return "", nil, parseErr
	}
	if len(refs) == 0 {
		return submitFile, nil, nil
	}
	if err := checkSpoolNames(refs, submitFile); err != nil {
		return "", nil, err
	}
	return rewritten, dedupeRefs(refs), nil
}

// checkSpoolNames refuses a submission whose spooled files would
// collide.
//
// Two connector files landing on one name in the sandbox means one
// overwrites the other, and which one wins is whichever is fetched
// last. A job that silently runs on the wrong input is worse than one
// that does not start, so this refuses rather than renaming -- a
// generated name would be one the submit file does not mention.
func checkSpoolNames(refs []InputRef, submitFile string) error {
	byName := map[string][]InputRef{}
	for _, ref := range refs {
		byName[ref.Name] = append(byName[ref.Name], ref)
	}

	var problems []string
	for name, group := range byName {
		ids := map[string]bool{}
		for _, ref := range group {
			ids[ref.Service+"/"+ref.FileID] = true
		}
		if len(ids) > 1 {
			problems = append(problems, fmt.Sprintf(
				"%d different connector files are both called %q", len(ids), name))
		}
	}

	// And a collision with an ordinary input file of the same name,
	// which is the same failure arriving from the other direction.
	for name := range byName {
		if plainInputCollides(submitFile, name) {
			problems = append(problems, fmt.Sprintf(
				"%q is both a connector file and an ordinary input file", name))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("this submission would overwrite its own inputs: %s",
			strings.Join(problems, "; "))
	}
	return nil
}

// plainInputCollides reports whether a non-connector input file in the
// submit text has this basename.
func plainInputCollides(submitFile, name string) bool {
	for _, line := range strings.Split(submitFile, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "transfer_input_files") {
			continue
		}
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			if item == "" || strings.HasPrefix(item, InputScheme+"://") {
				continue
			}
			if path.Base(item) == name {
				return true
			}
		}
	}
	return false
}

// dedupeRefs collapses references to the same file. The same input
// named twice is one fetch, not two.
func dedupeRefs(refs []InputRef) []InputRef {
	seen := map[string]bool{}
	out := make([]InputRef, 0, len(refs))
	for _, ref := range refs {
		key := ref.Service + "\x00" + ref.FileID + "\x00" + ref.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	return out
}

// OutputRefs reports connector references used as output destinations,
// which spool mode cannot serve.
//
// Output in spool mode comes back to the access point normally and is
// pushed to the provider afterwards, on request. A connector reference
// in transfer_output_remaps or output_destination would need a transfer
// plugin on the execute node, which is direct mode. Saying so beats
// leaving the URL in place, where it becomes a Requirements clause
// nothing satisfies and the job simply never matches.
func OutputRefs(submitFile string) []string {
	var out []string
	for _, line := range strings.Split(submitFile, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "transfer_output_remaps", "output_destination":
		default:
			continue
		}
		out = append(out, inputURL.FindAllString(value, -1)...)
	}
	return out
}

// --- carrying the refs from submit to spool ---------------------------

// InputAttribute is the job attribute that records which connector
// files a job's spool must contain.
//
// The refs have to survive from submit to upload, because
// SpoolJobFilesFromFS is all-or-nothing: it sends every file the job ad
// lists and then releases, so a connector file and a user's upload go
// in ONE spool or the job is released without one of them.
//
// The job ad is where they live, rather than a table here. It is
// durable, it survives a restart of this server, it needs no new
// schema, and it is visible -- condor_q -af ConnectorInputs says what a
// job pulled, which no private map would.
const InputAttribute = "ConnectorInputs"

// EncodeInputRefs renders refs for the job attribute.
//
// The original URLs, space separated. Not JSON: this lands in a ClassAd
// string a person reads in condor_q, and the URL is the form they
// wrote and will recognise.
func EncodeInputRefs(refs []InputRef) string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.Raw)
	}
	return strings.Join(out, " ")
}

// DecodeInputRefs reads the attribute back.
func DecodeInputRefs(value string) ([]InputRef, error) {
	var refs []InputRef
	for _, raw := range strings.Fields(value) {
		ref, err := ParseInputURL(raw)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// RecordInputRefs adds the attribute to a submit file.
//
// Prepended rather than inserted before a queue statement: a submit
// file may hold several, and a line at the top applies to all of them
// without this having to understand where they are.
func RecordInputRefs(submitFile string, refs []InputRef) string {
	if len(refs) == 0 {
		return submitFile
	}
	return fmt.Sprintf("# Recorded by the connectors machinery; see %s.\n+%s = %q\n%s",
		InputAttribute, InputAttribute, EncodeInputRefs(refs), submitFile)
}
