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
	"sort"
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
	Secret  bool   `yaml:"secret"`
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
}

type file struct {
	Knobs    []knob   `yaml:"knobs"`
	Families []family `yaml:"families"`
}

func main() {
	in := flag.String("in", "knobs.yaml", "knob table to read")
	out := flag.String("out", "knobs_gen.go", "Go file to write")
	flag.Parse()
	if err := run(*in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "knobs:", err)
		os.Exit(1)
	}
}

func run(in, out string) error {
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
	return os.WriteFile(out, src, 0o644)
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
		for _, line := range wrap(strings.Join(strings.Fields(k.Doc), " "), 68) {
			fmt.Fprintf(&b, "// %s\n", line)
		}
		fmt.Fprintf(&b, "var %s = config.%s(%q, %s, %s,\n\t%q)\n\n",
			ident, ctor, k.Name, def, scope, strings.Join(strings.Fields(k.Doc), " "))
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
	case len(f.Settings) == 0:
		return fmt.Errorf("%s: no settings", f.Family)
	case strings.TrimSpace(f.Doc) == "":
		return fmt.Errorf("%s: no doc", f.Family)
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
			f.Family, f.Family, f.Family, f.EnumeratedBy, enumScope,
			fmt.Sprintf("Names the %s instances that exist. %s", f.Family, doc))
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
		sdoc := strings.Join(strings.Fields(k.Doc), " ")
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
	return nil
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
	"MCP": "MCP", "OAUTH2": "OAuth2", "SSH": "SSH", "TLS": "TLS",
	"UI": "UI", "URL": "URL", "AP": "AP", "KEK": "KEK", "CPUS": "CPUs",
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
