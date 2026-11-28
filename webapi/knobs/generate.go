//go:build ignore

// Command generate turns knobs.yaml into knobs_gen.go. Run it with
// `make knobs`; `go test ./knobs/` fails when the generated file no
// longer matches the YAML.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/htcondor/golang-htcondor/config"
	"gopkg.in/yaml.v3"
)

type knob struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	Default string `yaml:"default"`
	Scope   string `yaml:"scope"`
	Doc     string `yaml:"doc"`
	// Secret marks a setting that names or holds a credential. It is
	// appended to the documentation, so the reference says so without
	// each paragraph having to remember to.
	Secret bool `yaml:"secret"`
	// DefaultDoc is what the reference prints in place of the literal
	// default, for a setting whose "unset" behaviour is not a value
	// this table holds: several token lifespans are read as zero and
	// the duration is substituted downstream, so the literal is 0s and
	// the answer an operator wants is 1h. Written as a whole sentence.
	DefaultDoc string `yaml:"default_doc"`
	// See is where the setting is explained at length. The reference
	// prints it as a link, which the documentation tests then check
	// resolves; a Go doc comment gets the title alone, since a
	// relative path means nothing from there and a path in two places
	// is a path that goes stale in one of them.
	See []seeAlso `yaml:"see"`
	// Obsolete marks a setting that still works but should not be
	// used. The reference leaves it out entirely -- documenting it is
	// how an operator finds it -- and the generated declaration is
	// marked Deprecated, which staticcheck reports at every read, so
	// the one place allowed to read it has to say so out loud.
	Obsolete *obsolete `yaml:"obsolete"`
}

// obsolete says a setting is on its way out, and what to use instead.
type obsolete struct {
	// SupersededBy names the setting that replaces this one. Optional,
	// since not everything obsolete has a single successor, but where
	// it is set a test checks the name is one the reference documents:
	// a pointer into nothing is worse than no pointer.
	SupersededBy string `yaml:"superseded_by"`
}

// seeAlso is one cross-reference: the section's title, and its URL
// relative to the page the reference is generated into.
type seeAlso struct {
	Text string `yaml:"text"`
	URL  string `yaml:"url"`
}

// family is a group of settings repeated per named instance, with the
// name folded into the middle of the parameter.
type family struct {
	Family       string `yaml:"family"`
	Prefix       string `yaml:"prefix"`
	EnumeratedBy string `yaml:"enumerated_by"`
	Doc          string `yaml:"doc"`
	Settings     []knob `yaml:"settings"`
	// EnumeratedByDir says the instances are discovered somewhere
	// other than a parameter -- site apps are the bundles found in a
	// directory -- so there is no list knob to declare. Declaring one
	// anyway would put a setting in the reference that nothing reads,
	// which is the defect this table exists to prevent.
	EnumeratedByDir bool `yaml:"enumerated_by_dir"`
	// EnumeratedScope is the reload scope of the enumerating parameter,
	// which is not always the scope of the settings it enumerates:
	// connector and picker definitions are re-read wholesale on every
	// reconfigure, so adding one takes effect without a restart even
	// though each setting is read when the definition is built.
	EnumeratedScope string `yaml:"enumerated_scope"`
	// EnumeratedDoc is the enumerating parameter's own paragraph.
	// Required wherever there is one: falling back to the family's
	// would print the same prose twice, side by side, since the two
	// entries sort next to each other.
	EnumeratedDoc string `yaml:"enumerated_doc"`
}

type file struct {
	Knobs    []knob   `yaml:"knobs"`
	Families []family `yaml:"families"`
}

func main() {
	in := flag.String("in", "knobs.yaml", "knob table to read")
	out := flag.String("out", "knobs_gen.go", "Go file to write")
	docs := flag.String("docs", "", "Markdown page whose generated region to rewrite; empty writes no reference")
	flag.Parse()
	if err := run(*in, *out, *docs); err != nil {
		fmt.Fprintln(os.Stderr, "knobs:", err)
		os.Exit(1)
	}
}

func run(in, out, docs string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var f file
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	src, err := Render(f.Knobs, f.Families)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, src, 0o644); err != nil {
		return err
	}
	if docs == "" {
		return nil
	}
	return rewriteDocs(docs, f.Knobs, f.Families)
}

// Render is the whole generator, kept separate from the file handling
// so a test can exercise it on a knob table of its own.
func Render(knobs []knob, families []family) ([]byte, error) {
	sort.Slice(knobs, func(i, j int) bool { return knobs[i].Name < knobs[j].Name })
	sort.Slice(families, func(i, j int) bool { return families[i].Family < families[j].Family })

	needsTime := false
	for _, k := range knobs {
		if k.Type == "duration" {
			needsTime = true
		}
	}
	for _, f := range families {
		for _, k := range f.Settings {
			if k.Type == "duration" {
				needsTime = true
			}
		}
	}

	var b bytes.Buffer
	b.WriteString("// Code generated from knobs.yaml by go generate; DO NOT EDIT.\n\npackage knobs\n\nimport (\n")
	if needsTime {
		b.WriteString("\t\"time\"\n\n")
	}
	b.WriteString("\t\"github.com/htcondor/golang-htcondor/config\"\n)\n\n")
	seen := map[string]string{}
	for _, k := range knobs {
		if err := validate(k); err != nil {
			return nil, err
		}
		ident := Identifier(k.Name)
		if other, dup := seen[ident]; dup {
			return nil, fmt.Errorf("%s and %s both render as %s", other, k.Name, ident)
		}
		seen[ident] = k.Name

		def, ctor, err := literal(k)
		if err != nil {
			return nil, err
		}
		scope := "config.Startup"
		if k.Scope == "reconfig" {
			scope = "config.Reconfig"
		}
		fmt.Fprintf(&b, "// %s is %s.\n//\n", ident, k.Name)
		doc := docText(k)
		for _, line := range wrap(doc, 68) {
			fmt.Fprintf(&b, "// %s\n", line)
		}
		// Go's own deprecation marker, in its own paragraph where the
		// tooling looks for it: staticcheck then reports every read of
		// this variable, so a read has to be deliberate and annotated.
		if k.Obsolete != nil {
			b.WriteString("//\n")
			for _, line := range wrap("Deprecated: "+
				strings.TrimPrefix(obsoleteSentence(k), "Obsolete: "), 68) {
				fmt.Fprintf(&b, "// %s\n", line)
			}
		}
		fmt.Fprintf(&b, "var %s = config.%s(%q, %s, %s,\n\t%q)\n\n",
			ident, ctor, k.Name, def, scope, doc)
	}

	for _, f := range families {
		if err := renderFamily(&b, f); err != nil {
			return nil, err
		}
	}

	// All carries the family enumerating knobs too: HTTP_API_CONNECTORS
	// and HTTP_API_PICKERS are ordinary settings an operator sets, and
	// leaving them out would leave them out of the reference.
	fmt.Fprintf(&b, "// All is every declared knob -- the settings above and each family's\n"+
		"// enumerating parameter -- for the configuration reference and for tests\n"+
		"// that check the table against the code. A family's per-instance settings\n"+
		"// are not here: they have no name until an instance does.\nvar All = []config.Knob{\n")
	for _, k := range knobs {
		fmt.Fprintf(&b, "\t%s.Knob,\n", Identifier(k.Name))
	}
	for _, f := range families {
		if f.EnumeratedBy != "" {
			fmt.Fprintf(&b, "\t%sInstances.Knob,\n", f.Family)
		}
	}
	b.WriteString("}\n\n")

	// Obsolete is emitted even when empty, so a consumer that filters
	// on it compiles whether or not anything is currently marked.
	b.WriteString("// Obsolete maps each obsolete setting to the setting that replaces\n" +
		"// it, or to the empty string where nothing single replaces it. It is\n" +
		"// what the reference leaves out and what a test checks stays out of\n" +
		"// the documentation; a daemon can also use it to report a setting an\n" +
		"// operator still has in their configuration.\nvar Obsolete = map[string]string{\n")
	for _, k := range knobs {
		if k.Obsolete != nil {
			fmt.Fprintf(&b, "\t%q: %q,\n", k.Name, k.Obsolete.SupersededBy)
		}
	}
	b.WriteString("}\n")

	return format.Source(b.Bytes())
}

// renderFamily emits the instance constructor and one accessor per
// setting. The instance name is folded once, in the constructor, so no
// call site repeats the rule -- and no call site spells a parameter
// name at all.
func renderFamily(b *bytes.Buffer, f family) error {
	switch {
	case f.Family == "":
		return fmt.Errorf("a family has no name")
	case !strings.HasPrefix(f.Prefix, "HTTP_API_") || !strings.HasSuffix(f.Prefix, "_"):
		return fmt.Errorf("%s: prefix must start with HTTP_API_ and end with _, got %q", f.Family, f.Prefix)
	case f.EnumeratedBy == "" && !f.EnumeratedByDir:
		return fmt.Errorf("%s: set enumerated_by, or enumerated_by_dir when the instances "+
			"come from somewhere other than a parameter", f.Family)
	case f.EnumeratedBy != "" && f.EnumeratedByDir:
		return fmt.Errorf("%s: enumerated_by and enumerated_by_dir are alternatives", f.Family)
	case f.EnumeratedBy != "" && f.EnumeratedScope != "startup" && f.EnumeratedScope != "reconfig":
		return fmt.Errorf("%s: enumerated_scope must be startup or reconfig, not %q",
			f.Family, f.EnumeratedScope)
	case f.EnumeratedBy != "" && strings.TrimSpace(f.EnumeratedDoc) == "":
		return fmt.Errorf("%s: enumerated_by needs enumerated_doc, the paragraph for %s itself",
			f.Family, f.EnumeratedBy)
	case len(f.Settings) == 0:
		return fmt.Errorf("%s: no settings", f.Family)
	case strings.TrimSpace(f.Doc) == "":
		return fmt.Errorf("%s: no doc", f.Family)
	}
	if err := noMarkdown(f.Family, f.Doc+f.EnumeratedDoc); err != nil {
		return err
	}

	typ := f.Family + "Settings"
	doc := strings.Join(strings.Fields(f.Doc), " ")

	fmt.Fprintf(b, "// %s names the %s settings of one instance.\n//\n", typ, f.Family)
	for _, line := range wrap(doc, 68) {
		fmt.Fprintf(b, "// %s\n", line)
	}
	fmt.Fprintf(b, "type %s struct{ id string }\n\n", typ)

	fmt.Fprintf(b, "// %s returns the settings of one %s instance. The name is\n"+
		"// folded into the parameter form here, once, so no caller repeats it.\n"+
		"func %s(instance string) %s {\n\treturn %s{config.InstanceID(instance)}\n}\n\n",
		f.Family, f.Family, f.Family, typ, typ)

	if f.EnumeratedBy != "" {
		enumScope := "config.Startup"
		if f.EnumeratedScope == "reconfig" {
			enumScope = "config.Reconfig"
		}
		fmt.Fprintf(b, "// %sInstances is the parameter naming every %s that exists.\n"+
			"var %sInstances = config.DefineList(%q, nil, %s,\n\t%q)\n\n",
			f.Family, f.Family, f.Family, f.EnumeratedBy, enumScope, enumDoc(f))
	}

	for _, k := range f.Settings {
		sk := k
		sk.Name = f.Prefix + "<instance>_" + k.Name
		if err := validateSetting(f, k); err != nil {
			return err
		}
		def, ctor, err := literal(sk)
		if err != nil {
			return err
		}
		scope := "config.Startup"
		if k.Scope == "reconfig" {
			scope = "config.Reconfig"
		}
		method := Identifier("HTTP_API_" + k.Name)
		sdoc := docText(k)
		fmt.Fprintf(b, "// %s is %s%s_%s.\n//\n", method, f.Prefix, "<instance>", k.Name)
		for _, line := range wrap(sdoc, 68) {
			fmt.Fprintf(b, "// %s\n", line)
		}
		fmt.Fprintf(b, "func (s %s) %s() config.%s {\n"+
			"\treturn config.%s(%q+s.id+%q, %s, %s,\n\t\t%q)\n}\n\n",
			typ, method, knobType(k.Type), ctor, f.Prefix, "_"+k.Name, def, scope, sdoc)
	}
	return nil
}

// enumDoc is the documentation of a family's enumerating parameter.
// Both the Go declaration and the reference print it, so it is written
// once: two spellings of one setting is the drift this table exists to
// prevent.
func enumDoc(f family) string {
	return strings.Join(strings.Fields(f.EnumeratedDoc), " ")
}

func validateSetting(f family, k knob) error {
	switch {
	case k.Name == "":
		return fmt.Errorf("%s: a setting has no name", f.Family)
	case knobType(k.Type) == "":
		return fmt.Errorf("%s/%s: unknown type %q", f.Family, k.Name, k.Type)
	case strings.TrimSpace(k.Doc) == "":
		return fmt.Errorf("%s/%s: no doc", f.Family, k.Name)
	case k.Scope != "startup" && k.Scope != "reconfig":
		return fmt.Errorf("%s/%s: scope must be startup or reconfig, not %q", f.Family, k.Name, k.Scope)
	}
	if err := noMarkdown(f.Family+"/"+k.Name, k.Doc); err != nil {
		return err
	}
	return validateSee(k)
}

// docParagraph is the documentation both outputs share: the paragraph
// from the table, with a sentence added for a setting that holds a
// credential.
func docParagraph(k knob) string {
	doc := strings.Join(strings.Fields(k.Doc), " ")
	if k.Secret {
		doc += " Holds or names a credential: keep it readable only by the account the server runs as."
	}
	return doc
}

// docText is the documentation as a Go doc comment prints it: the
// paragraph, and each cross-reference named rather than linked.
func docText(k knob) string {
	doc := docParagraph(k)
	if k.Obsolete != nil {
		doc = obsoleteSentence(k) + " " + doc
	}
	for _, s := range k.See {
		doc += fmt.Sprintf(" See %q in the operator guide.", s.Text)
	}
	return doc
}

// obsoleteSentence is how an obsolete setting introduces itself,
// wherever its documentation is printed.
func obsoleteSentence(k knob) string {
	if k.Obsolete.SupersededBy != "" {
		return fmt.Sprintf("Obsolete: superseded by %s.", k.Obsolete.SupersededBy)
	}
	return "Obsolete: do not use it in new configuration."
}

func validate(k knob) error {
	switch {
	case k.Name == "":
		return fmt.Errorf("a knob has no name")
	case !strings.HasPrefix(k.Name, "HTTP_API_"):
		return fmt.Errorf("%s: name must start with HTTP_API_", k.Name)
	case strings.TrimSpace(k.Doc) == "":
		return fmt.Errorf("%s: no doc; a knob without one cannot appear in the reference", k.Name)
	case k.Scope != "startup" && k.Scope != "reconfig":
		return fmt.Errorf("%s: scope must be startup or reconfig, not %q", k.Name, k.Scope)
	}
	if err := noMarkdown(k.Name, k.Doc); err != nil {
		return err
	}
	if d := strings.TrimSpace(k.DefaultDoc); d != "" && !strings.HasSuffix(d, ".") {
		return fmt.Errorf("%s: default_doc is a whole sentence and ends with a period, got %q",
			k.Name, d)
	}
	if o := k.Obsolete; o != nil {
		switch {
		case o.SupersededBy == k.Name:
			return fmt.Errorf("%s: superseded_by names the setting itself", k.Name)
		case o.SupersededBy != "" && !strings.HasPrefix(o.SupersededBy, "HTTP_API_"):
			return fmt.Errorf("%s: superseded_by must name an HTTP_API_ setting, got %q",
				k.Name, o.SupersededBy)
		}
	}
	return validateSee(k)
}

// validateSee rejects a half-written cross-reference. One with no text
// renders as an empty link and one with no URL renders as a link to
// the page itself, neither of which any test would catch.
// noMarkdown rejects a backtick in a documentation paragraph. The
// table is prose that both a Go doc comment and the reference print,
// and the reference adds its own code spans; a backtick here would
// reach the comment as a literal and be doubled in the reference.
func noMarkdown(what, doc string) error {
	if strings.Contains(doc, "`") {
		return fmt.Errorf("%s: the documentation holds a backtick; write the parameter "+
			"name plainly and the reference sets it as code", what)
	}
	return nil
}

func validateSee(k knob) error {
	for _, s := range k.See {
		switch {
		case strings.TrimSpace(s.Text) == "":
			return fmt.Errorf("%s: a see entry has no text", k.Name)
		case strings.TrimSpace(s.URL) == "":
			return fmt.Errorf("%s: see %q has no url", k.Name, s.Text)
		}
	}
	return nil
}

// literal renders the default and names the constructor for a type.
func literal(k knob) (def, ctor string, err error) {
	switch k.Type {
	case "string":
		return fmt.Sprintf("%q", k.Default), "DefineString", nil
	case "bool":
		switch strings.ToLower(k.Default) {
		case "true":
			return "true", "DefineBool", nil
		case "false", "":
			return "false", "DefineBool", nil
		}
		return "", "", fmt.Errorf("%s: %q is not a bool default", k.Name, k.Default)
	case "duration":
		d, perr := time.ParseDuration(k.Default)
		if perr != nil {
			return "", "", fmt.Errorf("%s: %q is not a duration: %w", k.Name, k.Default, perr)
		}
		return fmt.Sprintf("time.Duration(%d) /* %s */", int64(d), k.Default), "DefineDuration", nil
	case "int":
		n, perr := strconv.ParseInt(strings.TrimSpace(k.Default), 10, 64)
		if perr != nil {
			return "", "", fmt.Errorf("%s: %q is not a whole number", k.Name, k.Default)
		}
		return fmt.Sprintf("%d", n), "DefineInt", nil
	case "bytes":
		n, perr := config.ParseBytes(k.Default)
		if perr != nil {
			return "", "", fmt.Errorf("%s: %q is not a size: %w", k.Name, k.Default, perr)
		}
		return fmt.Sprintf("%d /* %s */", n, k.Default), "DefineBytes", nil
	case "words":
		items := strings.Fields(k.Default)
		if len(items) == 0 {
			return "nil", "DefineWords", nil
		}
		var q []string
		for _, w := range items {
			q = append(q, fmt.Sprintf("%q", w))
		}
		return "[]string{" + strings.Join(q, ", ") + "}", "DefineWords", nil
	case "list":
		items, perr := config.ParseList(k.Default)
		if perr != nil {
			return "", "", fmt.Errorf("%s: %q is not a list: %w", k.Name, k.Default, perr)
		}
		if len(items) == 0 {
			return "nil", "DefineList", nil
		}
		var q []string
		for _, s := range items {
			q = append(q, fmt.Sprintf("%q", s))
		}
		return "[]string{" + strings.Join(q, ", ") + "}", "DefineList", nil
	}
	return "", "", fmt.Errorf("%s: unknown type %q", k.Name, k.Type)
}

// knobType names the Go type a setting of this kind reads as. An
// unknown kind returns the empty string.
func knobType(kind string) string {
	switch kind {
	case "string":
		return "StringKnob"
	case "bool":
		return "BoolKnob"
	case "duration":
		return "DurationKnob"
	case "int":
		return "IntKnob"
	case "bytes":
		return "BytesKnob"
	case "list":
		return "ListKnob"
	case "words":
		return "WordsKnob"
	}
	return ""
}

// initialisms keep their capitalisation, so HTTP_API_TLS_CA_CERT reads
// as TLSCACert rather than TlsCaCert.
var initialisms = map[string]string{
	"API": "API", "CA": "CA", "DB": "DB", "ID": "ID", "IDP": "IDP",
	"GB": "GB", "LLM": "LLM", "MB": "MB", "MCP": "MCP", "OAUTH2": "OAuth2", "SSH": "SSH", "TLS": "TLS",
	"UI": "UI", "URL": "URL", "VSCODE": "VSCode", "AP": "AP", "KEK": "KEK", "CPUS": "CPUs",
	// EP is HTCondor's word for an execute point; CIMD and DCR are the
	// two OAuth2 client-registration mechanisms /mcp offers.
	"EP": "EP", "CIMD": "CIMD", "DCR": "DCR", "TTL": "TTL",
	// Not initialisms: two words the configuration spells as one.
	// Without these the identifiers read as Webui and Anyuser.
	"WEBUI": "WebUI", "ANYUSER": "AnyUser",
}

// Identifier turns HTTP_API_TLS_CA_CERT into TLSCACert.
func Identifier(name string) string {
	var b strings.Builder
	for _, part := range strings.Split(strings.TrimPrefix(name, "HTTP_API_"), "_") {
		if part == "" {
			continue
		}
		if s, ok := initialisms[strings.ToUpper(part)]; ok {
			b.WriteString(s)
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + strings.ToLower(part[1:]))
	}
	return b.String()
}

// wrap breaks a doc paragraph into comment-width lines.
func wrap(s string, width int) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		if line == "" {
			line = w
		} else if len(line)+1+len(w) <= width {
			line += " " + w
		} else {
			out = append(out, line)
			line = w
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// --- the configuration reference -------------------------------------
//
// The same table that declares the knobs prints them, so a setting
// cannot be read by the server and missing from what an operator
// reads. The output replaces a marked region of a Markdown page rather
// than being a page of its own, so the prose around it -- which no
// table can generate -- stays where it is.

const (
	docsBegin = "<!-- BEGIN GENERATED SETTINGS: edit webapi/knobs/knobs.yaml, then run `make knobs` -->"
	docsEnd   = "<!-- END GENERATED SETTINGS -->"
)

// rewriteDocs replaces the marked region of path with the reference.
// Absent markers are an error: appending to the end of a page nobody
// prepared would put the reference somewhere arbitrary.
func rewriteDocs(path string, knobs []knob, families []family) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body := string(raw)
	i := strings.Index(body, docsBegin)
	j := strings.Index(body, docsEnd)
	switch {
	case i < 0:
		return fmt.Errorf("%s: no %q marker", path, docsBegin)
	case j < 0:
		return fmt.Errorf("%s: no %q marker", path, docsEnd)
	case j < i:
		return fmt.Errorf("%s: the end marker comes before the begin marker", path)
	}
	ref, err := RenderDocs(knobs, families)
	if err != nil {
		return err
	}
	out := body[:i] + docsBegin + "\n\n" + ref + body[j:]
	return os.WriteFile(path, []byte(out), 0o644)
}

// docEntry is one thing the reference prints, kept with the name it
// sorts under so plain settings and families interleave alphabetically
// -- a reader looking a parameter up does not know which it is.
type docEntry struct {
	key  string
	text string
}

// RenderDocs is the reference body: everything between the markers.
func RenderDocs(knobs []knob, families []family) (string, error) {
	var entries []docEntry
	for _, k := range knobs {
		if err := validate(k); err != nil {
			return "", err
		}
		// An obsolete setting is left out of the reference entirely.
		// Documenting it, even as obsolete, is how somebody finds it
		// and decides to use it; the people who need to know are the
		// ones who already have it set, and the daemon can tell them.
		if k.Obsolete != nil {
			continue
		}
		entries = append(entries, docEntry{k.Name, docKnob("###", k.Name, k)})
	}
	for _, f := range families {
		text, err := docFamily(f)
		if err != nil {
			return "", err
		}
		if f.EnumeratedBy != "" {
			entries = append(entries, docEntry{f.EnumeratedBy, docKnob("###", f.EnumeratedBy, knob{
				Name:  f.EnumeratedBy,
				Type:  "list",
				Scope: f.EnumeratedScope,
				Doc:   enumDoc(f),
			})})
		}
		entries = append(entries, docEntry{f.Prefix, text})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })

	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.text)
	}
	return b.String(), nil
}

// docKnob prints one setting: the parameter as a heading, a line of
// what the code does with it, and the paragraph from the table.
func docKnob(level, name string, k knob) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s `%s`\n\n", level, name)
	fmt.Fprintf(&b, "**%s.** %s %s\n\n", docType(k.Type), docDefault(k), docScope(k.Scope))
	for _, line := range wrap(codeSpans(docParagraph(k)), 70) {
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	if refs := docSeeAlso(k); refs != "" {
		for _, line := range wrap(refs, 70) {
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// docFamily prints a family: what an instance is, where the instance
// names come from, and one entry per per-instance setting.
func docFamily(f family) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "### `%s<instance>_*`\n\n", f.Prefix)
	for _, line := range wrap(codeSpans(strings.Join(strings.Fields(f.Doc), " ")), 70) {
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	// Nothing is said here about where the instance names come from
	// when a parameter lists them: the family's own paragraph names
	// that parameter, and its entry sits directly above this one.
	// That there is no such parameter is not said anywhere else.
	if f.EnumeratedByDir {
		b.WriteString("The instances are discovered rather than listed in a parameter, " +
			"so there is no setting that names them.\n\n")
	}
	settings := append([]knob(nil), f.Settings...)
	sort.Slice(settings, func(i, j int) bool { return settings[i].Name < settings[j].Name })
	for _, k := range settings {
		if err := validateSetting(f, k); err != nil {
			return "", err
		}
		b.WriteString(docKnob("####", f.Prefix+"<instance>_"+k.Name, k))
	}
	return b.String(), nil
}

// paramRe finds a parameter name in a documentation paragraph, so the
// reference can set it as code without the table carrying Markdown
// that a Go doc comment would then have to print. It matches the
// template spelling too, HTTP_API_PICKER_<instance>_*.
var paramRe = regexp.MustCompile(`HTTP_API_[A-Za-z0-9_<>]*\*?`)

// codeSpans sets each parameter name in a paragraph as code. Safe to
// apply once and only once, which validate guarantees by rejecting a
// backtick in the table.
func codeSpans(s string) string {
	return paramRe.ReplaceAllString(s, "`$0`")
}

// docSeeAlso is the reference's cross-reference line, empty for a
// setting with none.
func docSeeAlso(k knob) string {
	var parts []string
	for _, s := range k.See {
		parts = append(parts, fmt.Sprintf("[%s](%s)", s.Text, s.URL))
	}
	if len(parts) == 0 {
		return ""
	}
	return "See " + strings.Join(parts, " and ") + "."
}

// docType names the kind of value an operator writes.
func docType(kind string) string {
	switch kind {
	case "string":
		return "String"
	case "bool":
		return "Boolean"
	case "duration":
		return "Duration"
	case "int":
		return "Whole number"
	case "bytes":
		return "Size"
	case "list":
		return "Comma-separated list"
	case "words":
		return "Space-separated list"
	}
	return kind
}

// docDefault says what applies when the setting is absent.
func docDefault(k knob) string {
	if d := strings.Join(strings.Fields(k.DefaultDoc), " "); d != "" {
		return d
	}
	if strings.TrimSpace(k.Default) == "" {
		return "Unset by default."
	}
	return fmt.Sprintf("Default `%s`.", k.Default)
}

// docScope says what installing a change takes.
func docScope(scope string) string {
	if scope == "reconfig" {
		return "A change is installed by `condor_reconfig`."
	}
	return "Changing it needs a restart."
}
