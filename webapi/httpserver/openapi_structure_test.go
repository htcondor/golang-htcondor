package httpserver

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Structural checks on openapi.json.
//
// Each guards a defect that a client generated from this document would
// hit and that nothing else here would notice. None of them fails today
// -- they were written against a clean document on purpose, the way the
// undeclared-settings inventory and unimplementedOperations were: the
// value is in what they refuse next, not in what they found.
//
// These are deliberately NOT a validation of the document against the
// OpenAPI 3.0 meta-schema. That needs the meta-schema vendored, it
// reports failures in schema language rather than in terms of this
// document, and the classes below are the ones that have actually
// broken generated clients. If a meta-schema validator is added later
// these stay: a spec-valid document can still have two operations
// sharing an operationId.

// openAPIMethods are the operation keys inside a path item. Anything
// else there (parameters, servers, summary, $ref) is not an operation.
var openAPIMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

// eachOperation calls fn for every operation in the document, with the
// path item's own shared parameters alongside.
func eachOperation(t *testing.T, fn func(path, method string, op, item map[string]any)) {
	t.Helper()
	doc := specDoc(t)
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatal("the document declares no paths; every check here would pass vacuously")
	}
	n := 0
	for p, raw := range paths {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("path %s is not an object", p)
			continue
		}
		for method, rawOp := range item {
			if !openAPIMethods[strings.ToLower(method)] {
				continue
			}
			op, ok := rawOp.(map[string]any)
			if !ok {
				t.Errorf("%s %s is not an object", strings.ToUpper(method), p)
				continue
			}
			n++
			fn(p, method, op, item)
		}
	}
	if n == 0 {
		t.Fatal("no operations found; every check here would pass vacuously")
	}
}

// An operationId is the method name in most generated clients. A
// missing one makes the generator invent a name from the path, and two
// operations sharing one silently collide -- the second overwrites the
// first, so a client simply cannot call one of the two endpoints and
// nothing says which.
func TestOperationIDsArePresentAndUnique(t *testing.T) {
	seen := map[string][]string{}
	eachOperation(t, func(p, method string, op, _ map[string]any) {
		where := fmt.Sprintf("%s %s", strings.ToUpper(method), p)
		id, _ := op["operationId"].(string)
		if strings.TrimSpace(id) == "" {
			t.Errorf("%s has no operationId, so a generated client names it after its path", where)
			return
		}
		seen[id] = append(seen[id], where)
	})
	for id, where := range seen {
		if len(where) > 1 {
			sort.Strings(where)
			t.Errorf("operationId %q is used by %d operations (%s); in most generated clients "+
				"the later one silently replaces the earlier, so one endpoint becomes uncallable",
				id, len(where), strings.Join(where, ", "))
		}
	}
}

// A {placeholder} in a path template that no parameter declares is a
// value the client has no way to supply. Generators differ on what they
// do with it -- some emit a method that cannot build its own URL, some
// drop the segment -- and none of them is right.
//
// Declarations count whether they sit on the operation or on the path
// item, because the spec allows either and this document uses both.
func TestPathTemplateParametersAreDeclared(t *testing.T) {
	eachOperation(t, func(p, method string, op, item map[string]any) {
		declared := map[string]bool{}
		for _, src := range []any{item["parameters"], op["parameters"]} {
			list, ok := src.([]any)
			if !ok {
				continue
			}
			for _, raw := range list {
				q, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if in, _ := q["in"].(string); in == "path" {
					if name, _ := q["name"].(string); name != "" {
						declared[name] = true
					}
				}
			}
		}
		for _, name := range templateParams(p) {
			if !declared[name] {
				t.Errorf("%s %s has {%s} in its path but declares no path parameter for it",
					strings.ToUpper(method), p, name)
			}
		}
	})
}

// templateParams returns the {names} in a path template.
func templateParams(p string) []string {
	var out []string
	for {
		i := strings.IndexByte(p, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(p[i:], '}')
		if j < 0 {
			return out
		}
		if name := p[i+1 : i+j]; name != "" {
			out = append(out, name)
		}
		p = p[i+j+1:]
	}
}

// A response with no description is invalid per the spec and renders in
// generated documentation as a blank row. Cheap to get wrong when
// adding a status code by copying a neighbour and deleting its text.
func TestEveryResponseHasADescription(t *testing.T) {
	eachOperation(t, func(p, method string, op, _ map[string]any) {
		responses, ok := op["responses"].(map[string]any)
		if !ok || len(responses) == 0 {
			t.Errorf("%s %s declares no responses", strings.ToUpper(method), p)
			return
		}
		for code, raw := range responses {
			r, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if _, isRef := r["$ref"]; isRef {
				continue
			}
			if d, _ := r["description"].(string); strings.TrimSpace(d) == "" {
				t.Errorf("%s %s response %s has no description", strings.ToUpper(method), p, code)
			}
		}
	})
}

// A schema that lists a property as required and does not define it
// describes an object no value can satisfy. Validators reject every
// instance; generators emit a field that is not there.
//
// Only schemas that define `properties` are checked: `required`
// alongside `additionalProperties` or a composed schema is a different
// statement, and reading it as this one would produce false failures.
func TestRequiredPropertiesAreDefined(t *testing.T) {
	doc := specDoc(t)
	var walk func(v any, where string)
	walk = func(v any, where string) {
		switch node := v.(type) {
		case map[string]any:
			req, hasReq := node["required"].([]any)
			props, hasProps := node["properties"].(map[string]any)
			if hasReq && hasProps {
				for _, raw := range req {
					name, ok := raw.(string)
					if !ok {
						continue
					}
					if _, defined := props[name]; !defined {
						t.Errorf("%s lists %q as required but does not define it, so no value "+
							"can satisfy the schema", where, name)
					}
				}
			}
			for k, child := range node {
				walk(child, where+"/"+k)
			}
		case []any:
			for i, child := range node {
				walk(child, fmt.Sprintf("%s/%d", where, i))
			}
		}
	}
	walk(doc["components"], "components")
	walk(doc["paths"], "paths")
}

// A component schema nothing references is dead weight that still has
// to be read by anyone maintaining the document, and is usually the
// remains of an endpoint that was removed or renamed.
//
// Matched on the exact $ref string rather than the bare name, so a
// schema called Job is not kept alive by a reference to JobSummary.
func TestNoOrphanComponentSchemas(t *testing.T) {
	doc := specDoc(t)
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	if len(schemas) == 0 {
		t.Skip("the document defines no component schemas")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	var orphans []string
	for name := range schemas {
		if !strings.Contains(body, `"#/components/schemas/`+name+`"`) {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("defined under components.schemas and never referenced: %v\n"+
			"Reference them, or delete them.", orphans)
	}
}
