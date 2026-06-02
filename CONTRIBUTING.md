# Contributing to plugdash

Thanks for contributing! This is the short version — see
[`docs/DEVELOPMENT.md`](./docs/DEVELOPMENT.md) for the full dev guide and
[`docs/PLUGINS.md`](./docs/PLUGINS.md) for the plugin reference (including
external, any-language plugins).

## Proposing Changes

1. Fork and create a topic branch.
2. Make your change, with tests where applicable.
3. Run `make all` (format, vet, test, build) — it must pass.
4. Open a pull request describing **what** changed and **why**.

For anything large or design-affecting, open an issue to discuss first.

## Code Style

- **Format** with `gofmt` — run `make fmt` (CI/`make all` will catch unformatted code).
- **Vet** clean — `make vet` must report nothing.
- **Keep plugins isolated.** Each built-in plugin lives in its own package under
  `internal/plugins/` and depends only on `internal/plugin` and the shared
  helpers (`internal/plugins/github.go`, `ghactivity.go`). Plugins must be safe
  for concurrent `Run`.
- **Tests are required for new plugins.** Use `httptest` stubs with the
  `plugins.GHBaseURL` override (no live network calls). Follow
  `internal/plugins/githubreleases/githubreleases_test.go`.

## Where Things Go

Adding a new built-in plugin? You touch exactly three places:

1. **New package** under `internal/plugins/<name>/` implementing the
   `plugin.Plugin` interface, with a `_test.go`.
2. **Register it** in `cmd/plugdash/main.go` with `reg.Register(<name>.New())`.
3. **Add an icon** entry for the plugin id in the `iconFor` map in
   `web/assets/app.js`.

Remember: `web/assets` is embedded via `go:embed`, so **rebuild the binary after
editing any frontend asset**.

## Commits & PRs

- Write clear, imperative commit subjects (e.g. "Add GitHub stars plugin").
- Keep commits focused; group unrelated changes into separate PRs.
- Make sure `make all` passes and new behavior is covered by tests before
  requesting review.
