# Contributing

## Before you start

For anything beyond a small fix — a new marker, a resolution-rule change, a new CLI
command — open an issue first. It's cheaper to align on approach before writing code than after.
Typos, doc fixes, and small bug fixes can go straight to a PR.

## Dev workflow

Go 1.25+. From the repo root:

```
go build ./...
go vet ./...
go test -covermode=atomic -coverprofile=cover.out ./...
```

`examples/basic` and `examples/mocking` are separate modules (each `replace`s
`github.com/okian/servo/v3` with the repo root) and need their own `go build ./...` /
`go test ./...` inside those directories. This mirrors [.github/workflows/go.yml](.github/workflows/go.yml)
— match it and CI won't surprise you.

If you change anything that affects generated output, regenerate and check the examples:

```
go run ./cmd/servo check --dir examples/basic
go run ./cmd/servo check --dir examples/mocking
```

Optionally wire the reference pre-commit hook, which runs `servo check` before every commit:

```
git config core.hooksPath githooks
```

## Code expectations

- `gofmt`-formatted; `go vet` clean.
- New behavior needs a test. Coverage is tracked via Codecov on every PR.
- If a change is breaking under the definition in [CHANGELOG.md](CHANGELOG.md) — exported API,
  CLI flag/exit-code contracts, or generated code's public method set — say so in the PR
  description; it affects whether the next release needs a `/vN` bump.

## License

Contributions are accepted under the project's [MIT license](LICENSE.md).
