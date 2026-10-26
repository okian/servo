## Summary

<!-- What does this change do, and why? -->

## Checklist

[CONTRIBUTING.md](https://github.com/okian/servo/blob/master/CONTRIBUTING.md) explains each of these, with the exact commands.

- [ ] `gofmt`-formatted; `go vet ./...` and `golangci-lint run ./...` clean in every module touched
- [ ] `go test -race ./...` passes at the root, and `go build ./... && go test ./...` in every
      example module touched
- [ ] Tests added or updated for new behaviour
- [ ] If this affects generated output: goldens regenerated (`UPDATE_GOLDEN=1 go test ./internal/emit/...`),
      every example regenerated, and `go run ./cmd/servo check --dir examples/<name>` passes for
      each — `examples/variants` also with `--tags=prod`
- [ ] Docs updated: the pages under `docs/`, the README, and any code comment the change makes
      stale
- [ ] A new marker option type is in `servovet`'s `markerResultTypes`
- [ ] If this is a breaking change under [CHANGELOG.md](https://github.com/okian/servo/blob/master/CHANGELOG.md)'s definition, it's
      called out below

## Breaking change?

<!-- If yes, describe what breaks and why a /vN bump is or isn't needed. Otherwise, delete this section. -->
