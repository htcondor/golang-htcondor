// Copyright 2026 Morgridge Institute for Research
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package httpserver

import (
	"reflect"
	"strings"
	"testing"
)

// The /ssh family in the OpenAPI document.
//
// None of it was documented, including the two endpoints added with
// certificate revocation. A caller is told to read /openapi.json rather
// than guess at endpoints, and that rule is only honest if the endpoints
// are in there -- which is the same gap TestAuthMeIsDocumented closed for
// /auth/me.
//
// These tests are mostly about DRIFT. A spec that says one thing while
// the server sends another is worse than no spec: it is wrong with
// authority, and a caller acts on it.

// schemaProps returns one component schema's properties.
func schemaProps(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	schema, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("no %s schema in the OpenAPI document", name)
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		t.Fatalf("%s schema documents no properties", name)
	}
	return props
}

// assertDocumentsOnlyRealFields checks every documented property against
// the struct's JSON names.
//
// One direction only, as the /auth/me tests do it: a field may be left
// undocumented on purpose, but a documented field the server never sends
// is a promise nothing keeps.
func assertDocumentsOnlyRealFields(t *testing.T, doc map[string]any, schema string, v any) {
	t.Helper()
	jsonNames := map[string]bool{}
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		jsonNames[strings.Split(tag, ",")[0]] = true
	}
	for name := range schemaProps(t, doc, schema) {
		if !jsonNames[name] {
			t.Errorf("the spec documents %s.%s, which %s does not send",
				schema, name, rt.Name())
		}
	}
}

// Every /ssh route the mux serves is in the document.
//
// Hardcoded rather than derived from the mux: a list built from the
// router would pass by construction and prove nothing.
func TestSSHEndpointsAreDocumented(t *testing.T) {
	doc := specDoc(t)
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("no paths object")
	}
	for _, p := range []string{
		"/ssh/ca",
		"/ssh/certificate",
		"/ssh/certificates",
		"/ssh/certificates/revoke",
		"/ssh/device",
		"/ssh/device/approve",
	} {
		if _, ok := paths[p]; !ok {
			t.Errorf("%s is served but not in the OpenAPI document", p)
		}
	}
}

func TestSSHSchemasMatchTheirStructs(t *testing.T) {
	doc := specDoc(t)
	for _, c := range []struct {
		schema string
		value  any
	}{
		{"SSHCAInfo", sshCAResponse{}},
		{"SSHCertificate", sshCertResponse{}},
		{"SSHCertificateRecord", sshCertRecord{}},
		{"SSHConsentView", sshConsentView{}},
		{"SSHConsentResult", sshConsentResult{}},
		{"InteractiveTerminalRequest", InteractiveCreateTerminalRequest{}},
	} {
		t.Run(c.schema, func(t *testing.T) {
			assertDocumentsOnlyRealFields(t, doc, c.schema, c.value)
		})
	}
}

// The serial is documented as a STRING, in both places it appears.
//
// It is 63 bits and a JSON number is read into a 53-bit float by most
// JavaScript, which rounds it into a value that revokes nothing. A spec
// saying "integer" would be an instruction to write that bug, and the
// client would be following the document correctly.
func TestSerialIsDocumentedAsAString(t *testing.T) {
	doc := specDoc(t)
	for _, schema := range []string{"SSHCertificate", "SSHCertificateRecord"} {
		props := schemaProps(t, doc, schema)
		serial, ok := props["serial"].(map[string]any)
		if !ok {
			t.Errorf("%s does not document a serial", schema)
			continue
		}
		if serial["type"] != "string" {
			t.Errorf("%s.serial is documented as %v; a 63-bit serial sent as a JSON number "+
				"arrives rounded and matches nothing", schema, serial["type"])
		}
	}

	// And on the request body, which is the half that actually has to
	// match for a revoke to land.
	paths, _ := doc["paths"].(map[string]any)
	revoke, ok := paths["/ssh/certificates/revoke"].(map[string]any)
	if !ok {
		t.Fatal("/ssh/certificates/revoke is not documented")
	}
	post, _ := revoke["post"].(map[string]any)
	body, _ := post["requestBody"].(map[string]any)
	content, _ := body["content"].(map[string]any)
	js, _ := content["application/json"].(map[string]any)
	schema, _ := js["schema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	serial, ok := props["serial"].(map[string]any)
	if !ok {
		t.Fatal("the revoke request body does not document a serial")
	}
	if serial["type"] != "string" {
		t.Errorf("the revoke body documents serial as %v, want string", serial["type"])
	}
}

// The two listing endpoints are session-only on the server. A spec
// offering bearerAuth for them would send an integrator to build against
// a 401.
func TestSSHCertificateListingIsDocumentedSessionOnly(t *testing.T) {
	doc := specDoc(t)
	paths, _ := doc["paths"].(map[string]any)
	for path, method := range map[string]string{
		"/ssh/certificates":        "get",
		"/ssh/certificates/revoke": "post",
	} {
		op, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("%s is not documented", path)
			continue
		}
		verb, _ := op[method].(map[string]any)
		security, _ := verb["security"].([]any)
		if len(security) != 1 {
			t.Errorf("%s %s documents %d security schemes; it accepts a session only",
				strings.ToUpper(method), path, len(security))
			continue
		}
		scheme, _ := security[0].(map[string]any)
		if _, ok := scheme["sessionCookie"]; !ok {
			t.Errorf("%s %s does not document sessionCookie as its only scheme: %v",
				strings.ToUpper(method), path, scheme)
		}
	}
}

// /ssh/ca and /ssh/certificate, by contrast, are reachable with a token
// -- that is the whole point of a certificate for BatchMode scripts, and
// the gateway documentation shows curl using one.
func TestSSHIssuanceIsDocumentedForTokens(t *testing.T) {
	doc := specDoc(t)
	paths, _ := doc["paths"].(map[string]any)
	for path, method := range map[string]string{
		"/ssh/ca":          "get",
		"/ssh/certificate": "post",
	} {
		op, _ := paths[path].(map[string]any)
		verb, _ := op[method].(map[string]any)
		security, _ := verb["security"].([]any)
		found := false
		for _, s := range security {
			if scheme, ok := s.(map[string]any); ok {
				if _, ok := scheme["bearerAuth"]; ok {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s %s does not offer bearerAuth, but a script holding a token is "+
				"exactly who needs it", strings.ToUpper(method), path)
		}
	}
}
