package httpserver

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/htcondor/golang-htcondor/logging"
)

// OpenAPI schema for the HTCondor RESTful API
// openAPISchema is the served OpenAPI document. It lives in a file
// rather than a Go string literal so that a change to it shows up as a
// diff of the document, can be validated and linted as a document, and
// can be read by anything that reads OpenAPI.
//
// The handler parses and re-encodes it, so the key order here does not
// reach the wire; what is served is this content with the OAuth2 URLs
// pointed at the endpoints this server actually routes.
//
//go:embed openapi.json
var openAPISchema []byte

// handleOpenAPISchema serves the OpenAPI schema
func (s *Handler) handleOpenAPISchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Parse and re-encode to ensure valid JSON and pretty printing
	var schema map[string]any
	if err := json.Unmarshal(openAPISchema, &schema); err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to parse OpenAPI schema")
		return
	}
	s.setOpenAPIOAuth2URLs(schema)

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(schema); err != nil {
		s.logger.Error(logging.DestinationHTTP, "Failed to encode OpenAPI schema", "error", err)
	}
}

// setOpenAPIOAuth2URLs points the schema's OAuth2 security scheme at the
// endpoints this server advertises. The literal in openAPISchema names
// the /mcp/ spelling, which a server without MCP does not route.
func (s *Handler) setOpenAPIOAuth2URLs(schema map[string]any) {
	flow, ok := nestedMap(schema, "components", "securitySchemes", "oauth2", "flows", "authorizationCode")
	if !ok {
		return
	}
	flow["authorizationUrl"] = s.oauth2EndpointPath("authorize")
	flow["tokenUrl"] = s.oauth2EndpointPath("token")
}

// nestedMap walks m through the named keys, each of which must hold an
// object.
func nestedMap(m map[string]any, keys ...string) (map[string]any, bool) {
	for _, k := range keys {
		next, ok := m[k].(map[string]any)
		if !ok {
			return nil, false
		}
		m = next
	}
	return m, true
}

// handleSwaggerUI serves the Swagger UI
func (s *Handler) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Override the global CSP for this route. The default policy set
	// in applySecurityHeaders is `default-src 'self'`, which blocks
	// the Swagger UI assets we pull from unpkg.com. Until we bundle
	// swagger-ui-dist into the binary (TODO: ship the assets via
	// go:embed so this route works air-gapped), allow unpkg + the
	// cross-origin font/style/script the bundle needs.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; "+
			"style-src 'self' 'unsafe-inline' https://unpkg.com; "+
			"script-src 'self' 'unsafe-inline' https://unpkg.com; "+
			"img-src 'self' data: https://unpkg.com; "+
			"font-src 'self' data: https://unpkg.com; "+
			// connect-src must include unpkg so browser devtools can
			// fetch the .js.map sourcemaps the swagger-ui bundle
			// references. Not fetching them is harmless (Swagger UI
			// still runs) but produces noisy console errors.
			"connect-src 'self' https://unpkg.com; "+
			"frame-ancestors 'self'; "+
			"base-uri 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	html := `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <meta
      name="description"
      content="SwaggerUI"
    />
    <title>HTCondor API Documentation</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui.css" />
  </head>
  <body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-bundle.js" crossorigin></script>
  <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-standalone-preset.js" crossorigin></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: '/openapi.json',
        dom_id: '#swagger-ui',
        oauth2RedirectUrl: window.location.origin + '/docs/oauth2-redirect',
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIStandalonePreset
        ],
        layout: "StandaloneLayout",
      });

      window.ui.initOAuth({
        clientId: "swagger-client",
        appName: "HTCondor API",
        usePkceWithAuthorizationCodeGrant: true
      });
    };
  </script>
  </body>
</html>`

	_, _ = w.Write([]byte(html))
}

// handleSwaggerOAuth2Redirect serves the OAuth2 redirect page for Swagger UI
func (s *Handler) handleSwaggerOAuth2Redirect(w http.ResponseWriter, _ *http.Request) {
	html := `<!doctype html>
<html lang="en-US">
<head>
    <title>Swagger UI: OAuth2 Redirect</title>
</head>
<body>
<script>
    'use strict';
    function run () {
        var oauth2 = window.opener.swaggerUIRedirectOauth2;
        var sentState = oauth2.state;
        var redirectUrl = oauth2.redirectUrl;
        var isValid, qp, arr;

        if (/code|token|error/.test(window.location.hash)) {
            qp = window.location.hash.substring(1).replace('?', '&');
        } else {
            qp = location.search.substring(1);
        }

        arr = qp.split("&");
        arr.forEach(function (v,i,_arr) { _arr[i] = '"' + v.replace('=', '":"') + '"';});
        qp = qp ? JSON.parse('{' + arr.join() + '}',
                function (key, value) {
                    return key === "" ? value : decodeURIComponent(value)
                }
        ) : {};

        isValid = qp.state === sentState;

        if ((
          oauth2.auth.schema.get("flow") === "accessCode" ||
          oauth2.auth.schema.get("flow") === "authorizationCode" ||
          oauth2.auth.schema.get("flow") === "authorization_code"
        ) && !oauth2.auth.code) {
            if (!isValid) {
                oauth2.errCb({
                    authId: oauth2.auth.name,
                    source: "auth",
                    level: "warning",
                    message: "Authorization may be unsafe, passed state was changed in server. The passed state wasn't returned from auth server."
                });
            }

            if (qp.code) {
                delete oauth2.state;
                oauth2.auth.code = qp.code;
                oauth2.callback({auth: oauth2.auth, redirectUrl: redirectUrl});
            } else {
                let oauthErrorMsg;
                if (qp.error) {
                    oauthErrorMsg = "["+qp.error+"]: " +
                        (qp.error_description ? qp.error_description+ ". " : "no accessCode received from the server. ") +
                        (qp.error_uri ? "More info: "+qp.error_uri : "");
                }

                oauth2.errCb({
                    authId: oauth2.auth.name,
                    source: "auth",
                    level: "error",
                    message: oauthErrorMsg || "[Authorization failed]: no accessCode received from the server."
                });
            }
        } else {
            oauth2.callback({auth: oauth2.auth, token: qp, isValid: isValid, redirectUrl: redirectUrl});
        }
        window.close();
    }

    if (document.readyState !== 'loading') {
        run();
    } else {
        document.addEventListener('DOMContentLoaded', function () {
            run();
        });
    }
</script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}
