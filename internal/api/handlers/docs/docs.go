// Package docs serves the role-filtered OpenAPI spec (/openapi.yaml,
// /openapi.json) and the Swagger UI shell at /docs. The filtering reads the
// caller's bearer token and falls back to the anonymous variant when no /
// invalid / forged token is presented.
package docs

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/vpramatarov/micro-blog/api"
	"github.com/vpramatarov/micro-blog/internal/auth"
)

// Service exposes the docs endpoints. Issuer is used to decode the audience
// from the bearer token; nil falls back to anonymous on every request.
type Service struct {
	Issuer *auth.Issuer
	Log    *slog.Logger
}

func New(issuer *auth.Issuer, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{Issuer: issuer, Log: log}
}

// docsHTML drives Swagger UI at /docs. Two behaviors beyond a stock page:
//
//  1. requestInterceptor attaches the persisted bearer token (if any) to
//     /openapi.{yaml,json} fetches so the server can return the role-filtered
//     spec for the authorized user.
//  2. A Redux-store subscriber re-downloads the spec whenever the auth slice
//     changes (Authorize or Logout), so the displayed operations update
//     immediately with no page reload.
//
// Swagger UI assets are pinned to an exact version. unpkg's `@5` floating tag
// would let any 5.x release land in the page without review — pinning makes a
// CDN compromise visible (it would have to actively replace the pinned file).
// For full Subresource Integrity, compute SHA-384 of the two referenced
// files and add `integrity="sha384-..." crossorigin="anonymous"` to the <link>
// and <script> tags. The simplest way to embed the assets and drop the CDN
// dependency entirely is github.com/flowchartsman/swaggerui — left as a
// follow-up.
const swaggerUIVersion = "5.17.14"
const docsHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Micro-Blog API — Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui.css" crossorigin="anonymous">
</head>
<body>
  <div id="ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui-bundle.js" crossorigin="anonymous"></script>
  <script>
    // Read the persisted bearer token. Two sources, in order of freshness:
    //   1. Live Redux store — updated synchronously by the AUTHORIZE reducer,
    //      so the subscriber below sees the new token in the same tick.
    //   2. localStorage["authorized"] — persistAuthorization writes this AFTER
    //      the dispatch wrapper completes; reliable on page reload but lags
    //      the same-tick fetch the subscriber kicks off after Authorize.
    function bearerFromStore() {
      try {
        const authorized = ui.getStore().getState().auth.get('authorized');
        const tok = authorized && authorized.getIn(['BearerAuth', 'value']);
        if (typeof tok === 'string' && tok) return tok;
      } catch (_) {}
      return '';
    }
    function bearerFromStorage() {
      try {
        const raw = localStorage.getItem('authorized');
        if (!raw) return '';
        const parsed = JSON.parse(raw);
        if (parsed && parsed.BearerAuth && parsed.BearerAuth.value) {
          return parsed.BearerAuth.value;
        }
      } catch (_) {}
      return '';
    }
    function bearerToken() {
      const raw = bearerFromStore() || bearerFromStorage();
      // Strip an accidentally-pasted "Bearer " prefix so the header never
      // ends up as "Authorization: Bearer Bearer eyJ...".
      return raw.replace(/^\s*Bearer\s+/i, '');
    }

    const ui = SwaggerUIBundle({
      url: "/openapi.json",
      dom_id: "#ui",
      deepLinking: true,
      persistAuthorization: true,
      requestInterceptor: (req) => {
        try {
          if (/\/openapi\.(json|yaml)(\?|$)/.test(req.url)) {
            const tok = bearerToken();
            if (tok) {
              req.headers['Authorization'] = 'Bearer ' + tok;
            }
          }
        } catch (_) {}
        return req;
      }
    });

    // Re-fetch the spec whenever auth state changes so the visible operation
    // set tracks the current role. The auth slice is undefined on the very
    // first dispatches the bundle fires during init — bail until it's there.
    // The cache-buster timestamp on the URL stops the browser HTTP cache from
    // returning a stale anonymous spec for the authenticated re-fetch.
    let prev = null;
    ui.getStore().subscribe(() => {
      try {
        const authSlice = ui.getStore().getState().auth;
        if (!authSlice) return;
        const a = authSlice.get('authorized');
        if (a !== prev) {
          prev = a;
          ui.specActions.download('/openapi.json?ts=' + Date.now());
        }
      } catch (_) {}
    });
  </script>
</body>
</html>`

// audienceFor picks the pre-filtered spec variant for the caller. The spec is
// documentation, not a security boundary, so a missing/expired/forged token
// falls back to the anonymous variant rather than 401-ing the docs fetch.
func (s *Service) audienceFor(r *http.Request) string {
	const prefix = "Bearer "
	raw := r.Header.Get("Authorization")
	if !strings.HasPrefix(raw, prefix) || s.Issuer == nil {
		return "anonymous"
	}
	claims, err := s.Issuer.Parse(strings.TrimSpace(raw[len(prefix):]))
	if err != nil || claims == nil || claims.Role == "" {
		return "anonymous"
	}
	if _, ok := api.SpecJSONByRole[claims.Role]; ok {
		return claims.Role
	}
	return "anonymous"
}

// ServeOpenAPIYAML — GET /openapi.yaml. Public; response varies by bearer token.
func (s *Service) ServeOpenAPIYAML(w http.ResponseWriter, r *http.Request) {
	writeSpecHeaders(w, "application/yaml")
	_, _ = w.Write(api.SpecYAMLByRole[s.audienceFor(r)])
}

// ServeOpenAPIJSON — GET /openapi.json. Public; response varies by bearer token.
func (s *Service) ServeOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	writeSpecHeaders(w, "application/json")
	_, _ = w.Write(api.SpecJSONByRole[s.audienceFor(r)])
}

// writeSpecHeaders prevents the browser HTTP cache from serving the anonymous
// variant of the spec after the user authorizes (the response body varies by
// Authorization header but the URL is the same). Vary signals the same to any
// well-behaved intermediate cache.
func writeSpecHeaders(w http.ResponseWriter, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Vary", "Authorization")
}

// ServeDocs — GET /docs. Public.
func (s *Service) ServeDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Inline JS is embedded in the binary; without this, a browser will happily
	// keep serving the previous build's HTML after a redeploy.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	// Override the strict default CSP from the SecurityHeaders middleware: the
	// docs page legitimately loads CSS+JS from unpkg.com and runs an inline
	// <script> to bootstrap Swagger UI. All other routes keep the default.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; "+
			"style-src https://unpkg.com 'unsafe-inline'; "+
			"script-src https://unpkg.com 'unsafe-inline'; "+
			"img-src 'self' data:; "+
			"connect-src 'self'; "+
			"frame-ancestors 'none'")
	_, _ = w.Write([]byte(docsHTML))
}
