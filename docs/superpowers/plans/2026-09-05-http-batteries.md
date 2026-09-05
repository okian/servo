# HTTP Batteries Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the response family (`Response`, `Xml`, `Text`, `HTML`, `Blob`, `Redirect`, `Stream`) and the `middleware` package (Recover, CORS, RateLimit, BodyLimit, AccessLog, Timeout, RequestID, Gzip).

**Architecture:** Responses become a sealed interface family whose encoding lives in `servo.WriteResponse`, so the scanner validates "implements `servo.Response`" and the emitter's respond helper stops being generic. Middleware ships as ordinary graph nodes in a new stdlib-only root-module package, riding the existing `servo.Use` machinery unchanged.

**Tech Stack:** Go 1.27 stdlib only.

**Spec:** `docs/superpowers/specs/2026-09-05-http-batteries-design.md`

## Global Constraints

- Both packages stdlib-only; no CI edits needed (root module covers `middleware/`).
- Existing goldens `fullapp`/`scopedapp` byte-identical; `httpapp.go.golden` refreshed deliberately and reviewed.
- `nil` error stays 200; `nil` response writes the code alone; success-as-error window becomes `code < 400`.
- TDD per task, gofmt/vet/tests before each commit. Branch `http-batteries`.

### Task 1: Response family in `servo`

Files: `servo/json.go` (rework), `servo/response.go` (new), tests in `servo/response_test.go` + `servo/json_test.go`.
Produces: the spec's exact API. `Json[T]`/`Xml[T]` embed `Response`; impls implement `write(w, code) error`; `WriteResponse` bridges. Tests drive every kind through `httptest.NewRecorder` asserting status, `Content-Type`, body, `Location`; nil response writes code alone.

### Task 2: Scanner accepts the family

Files: `internal/route/scan.go` (`checkResults` → implements-`servo.Response` + `Json`/`Xml` targ extraction), `internal/route/scan_test.go`.
New message: ``must return (R, error) where R is a servo response type (servo.Json[T], servo.Xml[T] or servo.Response)`` — update the existing expectation. Positive cases: `Xml[T]`, bare `Response`.

### Task 3: Emitter delegates to WriteResponse

Files: `internal/emit/http.go` (non-generic `httpRespond` calling `servo.WriteResponse`, drop `httpWriteJSON`, adapter branch `< 400`), `internal/emit/http_test.go`, golden refresh.

### Task 4: `middleware` package

Files: `middleware/recover.go`, `cors.go`, `ratelimit.go`, `bodylimit.go`, `accesslog.go`, `timeout.go`, `requestid.go`, `gzip.go`, one `_test.go` each, package doc in `middleware/middleware.go`.
Behavior per the spec's table; every middleware has the standard `Middleware(next http.Handler) http.Handler` method; rate limiter exposes an unexported `now func() time.Time` for tests; gzip decides at `WriteHeader` time.

### Task 5: examples/http + e2e

`servo.Use[*middleware.Recover]()` server-wide and `servo.Use[*middleware.CORS]()` on the default group with a user `*middleware.CORSConfig` provider; new `//servo:get /version` → `servo.Text`, `//servo:get /old-orders` → `servo.Redirect` + `Status.FOUND`; e2e asserts text body + content type, redirect Location + 302 (no-follow client), CORS preflight + actual headers. Regenerate, `-race`, lint, `servo check`.

### Task 6: Docs + CHANGELOG

`docs/reference/http.md` (response table, shipped-middleware section), new `docs/reference/middleware.md` + `reference.yml` entry, `servo-package.md` (Response family + WriteResponse), README (Layout + HTTP section sentence), CHANGELOG bullet extension, `examples/http/README.md`.

### Final verification

Root `go build && go vet && gofmt -l && go test -race ./...`; every example `servo check`; root + examples/http `golangci-lint`; read the refreshed golden once.
