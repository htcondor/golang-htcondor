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
}

type file struct {
	Knobs []knob `yaml:"knobs"`
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
	src, err := Render(f.Knobs)
	if err != nil {
		return err
	}
	return os.WriteFile(out, src, 0o644)
}

// Render is the whole generator, kept separate from the file handling
// so a test can exercise it on a knob table of its own.
func Render(knobs []knob) ([]byte, error) {
	sort.Slice(knobs, func(i, j int) bool { return knobs[i].Name < knobs[j].Name })

	needsTime := false
	for _, k := range knobs {
		if k.Type == "duration" {
			needsTime = true
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

	fmt.Fprintf(&b, "// All is every declared knob, for the configuration reference and\n"+
		"// for tests that check the table against the code.\nvar All = []config.Knob{\n")
	for _, k := range knobs {
		fmt.Fprintf(&b, "\t%s.Knob,\n", Identifier(k.Name))
	}
	b.WriteString("}\n")

	return format.Source(b.Bytes())
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
