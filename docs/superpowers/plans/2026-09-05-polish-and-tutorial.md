# Polish & Tutorial Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the Tier-1 gaps (HTTP test seam, `servo list` routes, graph JSON attribution, servo-vet directive rule), sweep every stale doc the HTTP rounds left behind, and teach the directive transport as tutorial chapter 13 (renumbering 13–21 → 14–22) with a real fourth injector.

**Architecture:** Features ride the existing seams: the test seam is one generated `HTTPHandler(group)` method; list/graph read `pipeline.routes` / `Resolved.HTTP`; the vet rule reuses an exported grammar check from `internal/route`. The tutorial injector `cmd/ordersservo` + `internal/transport/servoapi` reuses the service layer, sessions via the accessor, auth as `servo.Use`, claims as `servo.Extract` — and, if `observability.Tracer/Metrics` and `resilience.RateLimiter` already carry `Middleware(next http.Handler) http.Handler`, attaches the existing chain as `Use` lines verbatim.

**Tech Stack:** Go 1.27 stdlib; tutorial module's existing deps.

**Spec:** the decisions in this file's header + the explorer inventory (chat, 2026-09-05); roadmap queue recorded in project memory.

## Global Constraints

- Generated public method set grows by exactly `HTTPHandler(group string) http.Handler` (nil for unknown groups; `""` or `"default"` = default group) — still unreleased, called out in CHANGELOG.
- `fullapp`/`scopedapp` goldens byte-identical; `httpapp.go.golden` refreshed deliberately.
- Tutorial renumber: `git mv` in descending order; every cross-reference updated (grep `1[3-9]-|2[01]-` link targets, `chapter 1[3-9]|chapter 2[01]` prose, `docs/tutorial/` path mentions in code comments — `session.go`, `server.go`, workflows).
- Tutorial CI constraints hold: errcheck-clean handlers, tests self-skip without `TEST_*` env, committed fresh `servo_gen.go` for the new injector, Makefile gains `run-servo`.

### Task 1: Generated `HTTPHandler` test seam
`internal/emit` emits on App (and TestApp): switch over groups returning `a.<field>.srv.Handler`; emit tests + golden refresh; a port-free `httptest` test in `examples/http` (regenerate) and a cmd/servo fixture assertion; `docs/reference/generated-api.md` documents it in Task 5.

### Task 2: `servo list` routes section
`cmd/servo/list.go` prints the module's routes (`METHOD PATTERN [group] -> handler (args...)`, plus use/extract attachments when the spec declares HTTP) after the existing sections; integration test asserts the lines.

### Task 3: Graph JSON attribution
`servo.Graph` gains `HTTP *GraphHTTP{Groups []string; Routes []GraphRoute{Method, Pattern, Group, Handler, Args []string}; Uses []GraphUse{Type, Scope}; Extractors []GraphExtractor{Type, Produces}}`; emitted `Graph()` populates it; `internal/render` JSON format includes it (text/dot/mermaid unchanged, noted in docs); CHANGELOG calls out the schema addition.

### Task 4: servo-vet directive rule
Export `route.CheckDirectiveComment(text string) (ok bool, msg string)` wrapping the grammar (+ per-line mux syntax probe); `cmd/servo-vet` flags every `//servo:`-prefixed comment failing it, in any file; vet tests.

### Task 5: Stale-docs sweep (inventory from the explorer, all of it)
`generated-api.md` (App fields/table, header block, Run/Shutdown/Ready/Graph/stop methods, stable-surface list, HTTPHandler), `lifecycle.md` (third lifecycle case, Run/Shutdown/Ready sections, HTTP sibling of the scopes section), `cli.md` (generate scans routes; doctor `[INFO]` + two new table rows; module-wide scan note; graph/explain/why/list visibility notes updated per Tasks 2–3), `resolution.md` (entry points beyond roots; by-name method validation note; server construction outside levels), `docs/index.md` + `docs/_layouts/home.html` (route-scan stage, HTTP pitch line, "Twenty-two chapters"), `scopes.md` (transport claim requalified, handler-widening rule, examples/http pointer), `spec.md` (intro, "every marker" claim, marker lists), `http.md` self-contradictions (`:8-9`, `:164`, `:285-286`), `reference/index.md` (HTTP rows + examples/http), `limitations.md` (`:138-144` middleware-seam claim, wrong-tool list), `comparison.md` (transport differentiator, Use-vs-value-groups qualification), `README.md` (pitch, docs list, quick-start note, CLI table rows, reflect carve-out), `servo/markers.go` (Marker/Build doc comments), `examples/tutorial/internal/session/session.go:28-30` comment.

### Task 6: `cmd/ordersservo` + `internal/transport/servoapi`
Exported DTOs; handlers: Login (`*service.AuthService`), CreateOrder/GetOrder/ListOrders (`*service.OrderService`, claims extracted, GetOrder+Recent via `session.Sessions` accessor); `mw` types in servoapi: `Auth` (verify Bearer via `*auth.Issuer`, plant claims + `session.WithUser` in ctx) attached with Route selectors on the four protected routes, `ClaimsExtractor` reading ctx; `NewHTTPConfig(src config.Source)` parsing `HTTP_ADDR`; spec mirrors cmd/orders' binds/scope/overrides minus `Root[*api.Server]` plus `servo.HTTP(...)`+`servo.Extract`; check whether `observability.Tracer/Metrics`, `resilience.RateLimiter` Middleware signatures already fit `servo.Use` and attach whichever do; `app_test.go` with gomock overrides + `HTTPHandler("")` httptest (no ports, no containers); main.go; Makefile `run-servo`; `tutorial.yml:78-81` comment; regenerate + `servo check`.

### Task 7: Chapter 13 + renumber
`git mv` 21→22 … 13→14 (descending); write `13-directive-transport.md` in the 11/12 register ("same API, what the swap costs": DTOs→handlers→middleware/extractor→config→spec preview deferring mechanics to ch. 14→testing via HTTPHandler→the trade table); update `README.md` chapter table, `nav.yml` (Part III gains 13; later parts shift), `home.html` "Twenty-two chapters", ch. 10 fork list (three alternatives), Next/Prev links in 10/12/14, all renumbered cross-references (grep-driven), code comments naming moved chapter files.

### Task 8: CHANGELOG + final verification
CHANGELOG: HTTPHandler, list/graph visibility, vet rule, tutorial chapter. Verify: root `build/vet/gofmt/test -race`; every example `servo check`; `golangci-lint` root + examples/http + examples/tutorial (its own config); tutorial module `go build/vet/gofmt -l/test` (unit tier only); read the refreshed golden.
