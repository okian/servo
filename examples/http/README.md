# examples/http — the `//servo:` HTTP directives

A small order service whose routers, request decoding, response encoding,
middleware chains and server lifecycles are all generated — across **three
listeners**: the default group (public API), `telemetry` and `internal`. The
handlers in [`api/`](api/api.go) are plain exported functions with a
directive comment:

```go
//servo:post /order/{category}/
func Order(ctx context.Context, req *OrderReq, st *store.Store) (servo.Json[*OrderResp], error) {
	cat, err := st.Category(req.Category)
	if errors.Is(err, store.ErrNotFound) {
		return nil, servo.Status.NOT_FOUND.Wrapf("category %q: %w", req.Category, err)
	}
	...
	return servo.JSON(resp), servo.Status.CREATED
}
```

The spec ([`cmd/app/spec.go`](cmd/app/spec.go)) opts in with
`servo.HTTP(...)`, declares the two extra groups, and attaches middleware —
`RequestID` around every group, `Auth` around the internal one only:

```go
servo.Build(
	servo.HTTP(
		servo.Group("telemetry"),
		servo.Group("internal"),
		servo.Use[*mw.RequestID](),
		servo.Use[*mw.Auth](servo.Group("internal")),
	),
	servo.Extract[*mw.UserExtractor](),
)
```

Listen addresses come from the `*servo.HTTPConfig` provider in `api` — the
flat fields for the default group, `Groups["telemetry"]` / `Groups["internal"]`
for the rest.

What the routes demonstrate:

| Route | Shows |
|---|---|
| `POST /order/{category}/` | path + typed query + JSON body binding, a graph-resolved dependency, and the whole status contract (`CREATED` as success-in-error-position, `NOT_FOUND.Wrapf`, `FORBIDDEN.New`, `INTERNAL_SERVER_ERROR.Wrap` hiding its detail) |
| `GET /search` | generated `strconv` parsing for `uint16`/`float64`/`bool` query fields, and the 400 a malformed value earns |
| `GET /whoami` | an **extracted parameter**: `mw.UserExtractor` produces the `*mw.User` per request (401 before the handler when the header is missing), plus the context value the middleware planted |
| `GET /healthz` (`telemetry`) | a route on its own listener — absent from the public port — still wrapped by the server-level middleware |
| `POST /replicate/{shard}` (`internal`) | a group behind its own auth middleware, combining a path binding with an extracted parameter |
| `POST /feedback` | `form:"..."` binding, urlencoded and multipart |

[`mw/mw.go`](mw/mw.go) holds the middleware (context mutation, pre/post
behavior, group guarding) and the extractor.
[`cmd/app/app_test.go`](cmd/app/app_test.go) drives all of it end to end
against three real listeners: statuses, bodies, the body-size limit,
per-port route isolation, middleware effects, extraction failures, and a
clean `Shutdown` report.

Regenerate with:

```sh
go run ./cmd/servo generate --dir examples/http   # from the repo root
```
