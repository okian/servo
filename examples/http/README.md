# examples/http — the `//servo:` HTTP directives

A small order service whose router, request decoding, response encoding and
server lifecycle are all generated. The handlers in [`api/`](api/api.go) are
plain exported functions with a directive comment:

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

The spec ([`cmd/app/spec.go`](cmd/app/spec.go)) opts in with `servo.HTTP()`;
the listen address, optional TLS files and body limit come from the
`*servo.HTTPConfig` provider in `api` — a node the user constructs, resolved
like any other.

What the routes demonstrate:

| Route | Shows |
|---|---|
| `POST /order/{category}/` | path + typed query + JSON body binding, a graph-resolved dependency, and the whole status contract (`CREATED` as success-in-error-position, `NOT_FOUND.Wrapf`, `FORBIDDEN.New`, `INTERNAL_SERVER_ERROR.Wrap` hiding its detail) |
| `GET /search` | generated `strconv` parsing for `uint16`/`float64`/`bool` query fields, and the 400 a malformed value earns |
| `GET /whoami` | `header:"X-User"` binding |
| `POST /feedback` | `form:"..."` binding, urlencoded and multipart |

[`cmd/app/app_test.go`](cmd/app/app_test.go) drives all of it end to end
against a real listener: statuses, bodies, the body-size limit, and a clean
`Shutdown` report.

Regenerate with:

```sh
go run ./cmd/servo generate --dir examples/http   # from the repo root
```
