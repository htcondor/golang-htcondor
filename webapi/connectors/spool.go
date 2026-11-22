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
//	transfer_input_files = webapi-builtin-gdrive+gdrive2://FILEID/data.csv
//
// ONE spelling for both transfer modes, which is the point. The scheme
// is the provider's protocol and the prefix before '+' is the
// credential -- exactly the grammar contrib/gdrive2_plugin reads, and
// the shape HTCondor already uses for a prefixed scheme (getURLType
// returns the part after the last '+', so chtc+https dispatches to the
// https plugin).
//
// So the submitter writes one thing and the ACCESS POINT decides what
// happens to it:
//
//   - spool: this server fetches the file and rewrites the reference to
//     the bare name, so no third-party token reaches an execute node.
//   - direct: the reference is left alone and the plugin on the execute
//     node fetches it.
//
// A submit file therefore moves between modes without being rewritten,
// which a scheme naming the connector rather than the protocol could
// not manage -- and which is why this is not `connector://`: the
// connector is the auth, the scheme is the provider.
//
// The last path component is the file's name in the sandbox, because
// that is where HTCondor takes it from: "the destination file is given
// by the file name within the URL" (file-transfer.rst).

// InputMethods are the transfer-plugin methods a connector reference
// may name, mapped to the provider this server knows how to fetch from
// in spool mode.
//
// gdrive is the stock plugin's method and gdrive2 the ID-addressed
// one; both name Google Drive, and spool mode fetches the same way for
// either. A method this server cannot fetch for is refused rather than
// silently left in place.
var InputMethods = map[string]string{
	"gdrive":  "gdrive",
	"gdrive2": "gdrive",
}

// InputRef is one connector-sourced job input.
type InputRef struct {
	// Service is the credd service name, the part of the scheme before
	// '+'. It is what identifies the connector.
	Service string
	// Method is the transfer-plugin method after '+': which provider's
	// protocol this is.
	Method string
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
var inputURL = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9.+-]*\+(?:` +
	`gdrive2|gdrive` +
	`)://[^\s,;"']+`)

// ParseInputURL reads one reference.
func ParseInputURL(raw string) (InputRef, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return InputRef{}, fmt.Errorf("%q is not a usable file reference: %w", raw, err)
	}
	service, method, found := strings.Cut(u.Scheme, "+")
	if !found || service == "" {
		return InputRef{}, fmt.Errorf(
			"%q names no credential; expected <service>+<method>://<id>/<name>", raw)
	}
	if _, ok := InputMethods[method]; !ok {
		return InputRef{}, fmt.Errorf(
			"%q names the method %q, which this access point cannot fetch for", raw, method)
	}
	if u.Host == "" {
		return InputRef{}, fmt.Errorf(
			"%q names no file id; expected <service>+%s://<id>/<name>", raw, method)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return InputRef{}, fmt.Errorf(
			"%q does not name a file; expected <service>+%s://<id>/<name>", raw, method)
	}
	if err := CheckSpoolName(name); err != nil {
		return InputRef{}, fmt.Errorf("%q cannot be fetched: %w", raw, err)
	}
	return InputRef{
		Service: service,
		Method:  method,
		FileID:  u.Host,
		Name:    name,
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
			if item == "" || inputURL.MatchString(item) {
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

// CheckSpoolName reports whether a chosen file's name can be the name it
// lands under in the sandbox.
//
// The name has two jobs at once, and a name that fails either fails
// quietly. It is the file written into the spool, so it must be a plain
// filename and not a path. It is ALSO the token that replaces the
// reference in the submit file, so it must survive two more readers:
// the regexp that finds references, and HTCondor's own parse of
// transfer_input_files.
//
// Each of these was reachable before this check existed, and none of
// them produced an error:
//
//   - Percent-encoding a space gets past the regexp that finds
//     references and puts the space in the rewritten line instead,
//     where HTCondor splits on it. The job then looks for two inputs,
//     one of which nothing produces.
//
//     A RAW space is a different matter and not this check's to catch:
//     the regexp stops there, so the text parses as a shorter reference
//     plus an ordinary input, which is also what those tokens mean when
//     a submitter writes them on purpose. A name with a space is not
//     expressible as a reference, and the chooser is where that is
//     prevented, because it has the real name in hand. The same goes
//     for a comma.
//
//   - "." and ".." are legal URL paths and parsed as names, so they
//     reached the spool tar as entry names. A tar entry called ".." is
//     not a file to write but a direction to walk.
//
//   - A submit file is macro-expanded, so "$" in a name is not a
//     character but an instruction. $(DOLLAR) would escape it for the
//     rewrite, but the file in the sandbox still has to be found under
//     its real name by a reader that does not expand anything, so the
//     two cannot both be satisfied without more than an escape.
//
// A provider lets people name files things like this; it is only the
// reference that cannot carry them. So the message says what to do --
// rename the file -- rather than reporting a parse failure.
func CheckSpoolName(name string) error {
	switch name {
	case ".", "..":
		return fmt.Errorf(
			"a file named %q cannot be fetched into a job's sandbox, because that "+
				"is a directory reference and not a file name", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf(
				"the file name %q contains a control character, which cannot appear "+
					"in a submit file", name)
		}
		if strings.ContainsRune(spoolNameReserved, r) {
			return fmt.Errorf(
				"the file name %q contains %q, which a submit file reads as something "+
					"other than part of a name; rename the file in the provider and "+
					"choose it again", name, r)
		}
	}
	return nil
}

// spoolNameReserved is what a name may not contain.
//
// The whitespace and the separators are where transfer_input_files ends
// one entry and starts the next; the quotes are how it would be told
// otherwise, which HTCondor does not support for this attribute. "$" is
// macro expansion and "\\" is a path separator on the other platform
// this runs on.
const spoolNameReserved = " \t\r\n,;\"'$\\"
