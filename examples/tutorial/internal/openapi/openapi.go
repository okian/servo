// Package openapi serves the API contract and a browser UI for it.
//
// It is its own package because all three transport variants serve the
// same contract: api (net/http), ginapi (Gin) and grpcapi. Keeping the
// spec next to the code that serves it is also what lets it be embedded:
// an embed directive cannot reach outside its own directory, so a spec at
// the module root could only be read from disk at runtime, which would
// depend on the working directory the binary happened to start in.
package openapi

import (
	_ "embed"
	"net/http"
)

// Spec is the contract itself. Embedding it means the binary and the
// documentation of that binary cannot drift apart in a deployment: there
// is no file to forget to copy into the image.
//
//go:embed openapi.yaml
var Spec []byte

// swaggerUI loads Swagger UI from a CDN rather than vendoring several
// megabytes of JavaScript into the repository. That is a deliberate trade
// and worth knowing about: a browser with no route to the internet gets
// an empty page, and the spec itself is still readable at /openapi.yaml.
// A service that must document itself in an air-gapped network should
// vendor the assets and serve them from this package instead.
const swaggerUI = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Orders API</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  </head>
  <body>
    <div id="ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script>
      window.onload = () => SwaggerUIBundle({url: "../openapi.yaml", dom_id: "#ui"});
    </script>
  </body>
</html>
`

// Handler serves the spec at /openapi.yaml and the UI at /swagger/.
//
// Both are read-only and neither touches the service, so they are safe on
// whichever listener the caller mounts them on. This service mounts them
// on the public one, beside the API they describe; a service whose
// endpoint list is itself sensitive should mount them on the admin
// listener instead, next to /healthz — see cmd/orders/main.go.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(Spec)
	})
	mux.HandleFunc("GET /swagger/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerUI))
	})
	return mux
}
