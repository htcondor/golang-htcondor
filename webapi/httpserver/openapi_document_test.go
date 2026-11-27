package httpserver

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// The document now lives in openapi.json rather than a Go string
// literal, so these are the checks the compiler used to make for free
// plus the ones it never made.

func TestOpenAPIDocumentIsWellFormed(t *testing.T) {
	doc := specDoc(t)

	if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.") {
		t.Errorf("openapi = %q, want a 3.x version", v)
	}
	info, ok := doc["info"].(map[string]any)
	if !ok {
		t.Fatal("no info object")
	}
	for _, k := range []string{"title", "version"} {
		if v, _ := info[k].(string); v == "" {
			t.Errorf("info.%s is empty", k)
		}
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatal("no paths")
	}

	methods := map[string]bool{
		"get": true, "put": true, "post": true, "delete": true,
		"options": true, "head": true, "patch": true, "trace": true,
	}
	for p, raw := range paths {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("path %q does not start with /", p)
		}
		item, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("path %q is not an object", p)
			continue
		}
		operations := 0
		for k, op := range item {
			if k == "parameters" || k == "summary" || k == "description" || k == "servers" {
				continue
			}
			if !methods[strings.ToLower(k)] {
				t.Errorf("path %q has %q, which is not an HTTP method", p, k)
				continue
			}
			operations++
			if o, ok := op.(map[string]any); ok {
				if _, has := o["responses"]; !has {
					t.Errorf("%s %s declares no responses", strings.ToUpper(k), p)
				}
			}
		}
		if operations == 0 {
			t.Errorf("path %q declares no operations", p)
		}
	}
}

// The handler points the OAuth2 flow at the endpoints this server
// routes, by walking a fixed path into the document and returning
// quietly if it is not there. Move or rename any of those keys and the
// served document keeps advertising the /mcp/ spelling to a server that
// does not route it, with nothing saying so.
func TestOpenAPIOAuth2FlowIsWhereTheHandlerLooks(t *testing.T) {
	doc := specDoc(t)
	flow, ok := nestedMap(doc, "components", "securitySchemes", "oauth2", "flows", "authorizationCode")
	if !ok {
		t.Fatal("components.securitySchemes.oauth2.flows.authorizationCode is not an object; " +
			"setOpenAPIOAuth2URLs would silently do nothing")
	}
	for _, k := range []string{"authorizationUrl", "tokenUrl"} {
		if _, has := flow[k]; !has {
			t.Errorf("the flow has no %s for the handler to replace", k)
		}
	}
}

// A $ref naming a schema that is not in the document renders as a
// broken reference in every client generated from it.
func TestOpenAPIRefsResolve(t *testing.T) {
	doc := specDoc(t)
	var refs []string
	var walk func(any, string)
	walk = func(v any, where string) {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				if k == "$ref" {
					if s, ok := child.(string); ok {
						refs = append(refs, s)
					}
					continue
				}
				walk(child, where+"/"+k)
			}
		case []any:
			for i, child := range t {
				walk(child, fmt.Sprintf("%s/%d", where, i))
			}
		}
	}
	walk(doc, "")
	if len(refs) == 0 {
		t.Skip("the document uses no $ref")
	}

	missing := map[string]bool{}
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "#/") {
			// An external reference is somebody else's document.
			continue
		}
		cur := any(doc)
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			m, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur, ok = m[part]
			if !ok {
				cur = nil
				break
			}
		}
		if cur == nil {
			missing[ref] = true
		}
	}
	if len(missing) > 0 {
		var names []string
		for r := range missing {
			names = append(names, r)
		}
		sort.Strings(names)
		t.Errorf("unresolved $ref: %s", strings.Join(names, ", "))
	}
}

// unimplementedOperations names every operation the document says this
// server does not implement, by operationId.
//
// Empty on purpose. An entry here is a promise that the handler really
// does refuse the request, and the point of the list is that adding one
// takes a deliberate edit to a file whose comment says what the promise
// costs.
var unimplementedOperations = map[string]string{}

// An operation that documents itself as unimplemented has to still be
// unimplemented.
//
// deleteJob and editJob were both described as "NOT YET IMPLEMENTED"
// with a single 501 response, for long enough that the handlers behind
// them had grown owner scoping, superuser impersonation and an audit
// trail. A client generated from the document could not remove or edit
// a job at all, and nothing failed: the route existed, so the
// route-coverage check in openapi_routes_test.go passed, and the
// document was self-consistent, so every check inside it passed too.
//
// What this does NOT do is read the handler. It cannot: deciding
// whether a route is implemented means calling it, and every one of
// these needs a credential and a schedd first. What it does is make the
// claim expensive to make and impossible to make silently -- which is
// the half that was missing, since nobody chose to leave those two
// stale, they were simply never revisited.
func TestNoOperationSilentlyClaimsToBeUnimplemented(t *testing.T) {
	doc := specDoc(t)
	paths, _ := doc["paths"].(map[string]any)

	seen := map[string]bool{}
	for path, item := range paths {
		methods, _ := item.(map[string]any)
		for method, raw := range methods {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := op["operationId"].(string)
			where := fmt.Sprintf("%s %s", strings.ToUpper(method), path)
			if id != "" {
				where += " (" + id + ")"
			}

			desc, _ := op["description"].(string)
			saysSo := strings.Contains(strings.ToUpper(desc), "NOT YET IMPLEMENTED")

			responses, _ := op["responses"].(map[string]any)
			onlyRefusal := false
			if len(responses) == 1 {
				_, onlyRefusal = responses["501"]
			}

			if !saysSo && !onlyRefusal {
				continue
			}
			seen[id] = true
			if _, allowed := unimplementedOperations[id]; allowed {
				continue
			}
			switch {
			case saysSo && onlyRefusal:
				t.Errorf("%s is documented as unimplemented. If that is still true, add its "+
					"operationId to unimplementedOperations with the reason; if the handler "+
					"does the work now, describe what it does and declare the responses it "+
					"really returns.", where)
			case saysSo:
				t.Errorf("%s says it is not implemented but declares responses other than 501; "+
					"one of the two is wrong.", where)
			default:
				t.Errorf("%s declares 501 as its only response, so no client can call it "+
					"successfully. Declare what it returns, or add it to "+
					"unimplementedOperations.", where)
			}
		}
	}

	// The other direction, so the list cannot outlive what it excuses.
	for id, why := range unimplementedOperations {
		if !seen[id] {
			t.Errorf("unimplementedOperations lists %s (%q), but no operation by that id is "+
				"documented as unimplemented; drop the entry.", id, why)
		}
	}
}
