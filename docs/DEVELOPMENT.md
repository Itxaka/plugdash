# Development Guide

This guide covers building, running, and extending **plugdash** as a contributor.
For the full plugin reference — including external (any-language) plugins — see
[`docs/PLUGINS.md`](./PLUGINS.md).

## Prerequisites

- **Go 1.25** or newer (the module targets `go 1.25`).
- A C compiler is **not** required: the SQLite driver is the pure-Go
  `modernc.org/sqlite`.
- `git`, and optionally `docker` for the container build.

## Getting Started

```sh
git clone <your-fork-url> plugdash
cd plugdash
make build      # produces ./bin/plugdash
./bin/plugdash  # serves the dashboard on http://localhost:8080
```

## Makefile Targets

All targets are defined in the [`Makefile`](../Makefile):

| Target        | Command                              | Purpose                                                   |
| ------------- | ------------------------------------ | --------------------------------------------------------- |
| `make build`  | `go build -o bin/plugdash ./cmd/plugdash` | Build the binary into `bin/plugdash`.                |
| `make run`    | `go run ./cmd/plugdash`              | Build and run from source (no binary kept).               |
| `make test`   | `go test ./...`                      | Run the full test suite.                                  |
| `make vet`    | `go vet ./...`                       | Run the Go static analyzer.                               |
| `make fmt`    | `gofmt -w .`                         | Format all Go source in place.                            |
| `make tidy`   | `go mod tidy`                        | Sync `go.mod`/`go.sum` with imports.                      |
| `make clean`  | `rm -rf bin/ *.db`                   | Remove the build output and local SQLite databases.       |
| `make docker` | `docker build -t plugdash .`         | Build the container image.                                |
| `make all`    | `fmt vet test build`                 | Format, vet, test, then build — the pre-commit sweep.     |

Run `make all` before sending a change.

## Project Layout

| Path                     | Purpose                                                                                  |
| ------------------------ | ---------------------------------------------------------------------------------------- |
| `cmd/plugdash/`          | The `main` package: flags, store/logging setup, plugin registration, server bootstrap.   |
| `internal/plugin/`       | The core `Plugin` interface, `Config`, `Result`, `Visualization` types, and the `Registry`. |
| `internal/plugins/`      | Built-in plugin implementations, one package each, plus shared GitHub helpers (`github.go`, `ghactivity.go`). |
| `internal/extplugin/`    | External-plugin manager: discovers and runs executables as plugins.                      |
| `internal/server/`       | HTTP server, JSON API, logging ring buffer, static-asset serving.                        |
| `internal/store/`        | SQLite persistence for trackers and settings.                                            |
| `web/`                   | The `web` package and the embedded frontend (`web/assets/`).                             |
| `examples/plugins/`      | Example external plugins.                                                                 |
| `docs/`                  | This guide and `PLUGINS.md`.                                                              |

## Running Locally & the Dev Loop

```sh
make run                         # default: :8080, ./plugdash.db
go run ./cmd/plugdash -addr :9000 -db /tmp/dev.db -debug
```

Useful flags (see `cmd/plugdash/main.go`):

- `-addr` — HTTP listen address (default `:8080`).
- `-db` — path to the SQLite database file (default `plugdash.db`).
- `-plugins-dir` — directory of external plugin executables.
- `-config` — path to a declarative config file (YAML); trackers in it are
  reconciled into the DB and shown read-only in the UI.
- `-debug` — verbose logging (also via `PLUGDASH_DEBUG=1` or the Settings UI).
- `-version` — print the version and exit.

A GitHub token can be supplied via `GITHUB_TOKEN` or saved in the Settings UI;
it authenticates all GitHub plugins and raises rate limits.

### IMPORTANT: rebuild after editing frontend assets

The frontend in `web/assets/` is **baked into the binary at build time** via
`go:embed` (see `web/web.go`). The running server serves the embedded copy, **not**
the files on disk. This means:

> **After editing anything in `web/assets/` you MUST rebuild the binary**
> (`make build`, or restart `make run`) before the changes take effect.

Editing `app.js` / `index.html` / CSS and refreshing the browser alone will show
the old, embedded version.

## Running Tests

```sh
go test ./...        # or: make test
go test ./internal/plugins/githubreleases/   # a single package
```

### Test conventions

Plugin tests do **not** hit the live GitHub API. The shared client reads its base
URL from the exported `plugins.GHBaseURL` var, so tests:

1. Spin up an `httptest.Server` that returns canned JSON for the expected paths.
2. Override the base URL and restore it afterward:

   ```go
   orig := plugins.GHBaseURL
   plugins.GHBaseURL = srv.URL
   defer func() { plugins.GHBaseURL = orig }()
   ```

3. Run the plugin against a `plugin.Config` and assert on the `Result` —
   marshaling `Result.Data` through JSON keeps assertions resilient to the
   concrete item type. See
   `internal/plugins/githubreleases/githubreleases_test.go` for the pattern.

**External-plugin tests** (`internal/extplugin/extplugin_test.go`) use small
executable **shell-script fixtures** written to a temp dir with mode `0755`
(scripts named `plugdash-plugin-*`), exercising discovery, the `run`/schema
protocol, failures, timeouts, and ID clashes — no real subprocess binaries are
checked in.

## Add a Built-in Go Plugin — Walkthrough

A built-in plugin is a Go package under `internal/plugins/` that implements the
`plugin.Plugin` interface and is registered at startup.

### 1. Implement the interface

The contract (from `internal/plugin/plugin.go`):

```go
type Plugin interface {
    ID() string                          // stable machine id, e.g. "github-releases"
    Name() string                        // human label shown in the UI
    Description() string                 // one-line summary
    ConfigSchema() []ConfigField         // config inputs the UI renders
    RefreshInterval() time.Duration      // advisory auto-refresh floor
    Run(ctx context.Context, cfg Config) (Result, error)
}
```

Create `internal/plugins/myplugin/myplugin.go`:

```go
// Package myplugin reports <thing> as a <viz> widget.
package myplugin

import (
    "context"
    "time"

    "plugdash/internal/plugin"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string          { return "my-plugin" }
func (p *Plugin) Name() string        { return "My Plugin" }
func (p *Plugin) Description() string { return "Track something useful." }

func (p *Plugin) RefreshInterval() time.Duration { return time.Hour }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
    return []plugin.ConfigField{
        {Key: "repo", Label: "Repository", Type: plugin.FieldString, Required: true,
            Placeholder: "owner/repo"},
    }
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
    // ... fetch + build the data shape ...
    return plugin.Result{
        Visualization: plugin.VizList,
        Title:         "My widget",
        Data:          map[string]any{"items": items},
    }, nil
}
```

Pull typed config values with the `Config` helpers: `cfg.String`, `cfg.Int`,
`cfg.Bool`, `cfg.List`.

### 2. Pick a visualization and build its data shape

Set `Result.Visualization` to one of the constants in `internal/plugin/plugin.go`
and make `Result.Data` match its expected shape:

| Visualization     | `Data` shape                                                                 |
| ----------------- | ---------------------------------------------------------------------------- |
| `VizList`         | `{items: [{title, subtitle, url, timestamp, badge?}]}`                       |
| `VizTable`        | `{columns: [], rows: [[]]}`                                                  |
| `VizChecklist`    | `{items: [{label, ok, detail}]}`                                             |
| `VizStat`         | `{value, label, status}`                                                     |
| `VizTimeseries`   | `{label, unit, total, points: [{t, v}]}` (points in ascending time order)    |

`Data` must be JSON-serializable.

### 3. Reuse the shared GitHub helpers

For GitHub-backed plugins, don't re-implement HTTP:

- `internal/plugins/github.go` — `plugins.NewGHClient(token)` (falls back to
  `GITHUB_TOKEN`), `client.Get`, `ListReleases`, `ReleaseByTag`, and
  `plugins.NormalizeRepo` for parsing `owner/repo` or a full URL. It also emits
  friendly rate-limit errors.
- `internal/plugins/ghactivity.go` — `plugins.FetchActivityTimestamps(...)` for
  paginated stars/commits/issues/PRs timestamps, plus `ActivityMetricOptions` /
  `ActivityMetricLabel` for `FieldSelect` choices.

### 4. Add a test using the `GHBaseURL` override

Create `internal/plugins/myplugin/myplugin_test.go` following the conventions
above: stub the API with `httptest`, point `plugins.GHBaseURL` at it, run, and
assert on the `Result`.

### 5. Register the plugin

In `cmd/plugdash/main.go`, import the package and register the instance:

```go
import "plugdash/internal/plugins/myplugin"

// ... inside main, with the other reg.Register calls:
reg.Register(myplugin.New())
```

`Registry.Register` panics on a duplicate `ID()`, so keep ids unique.

### 6. Add a frontend icon

In `web/assets/app.js`, add an entry to the `iconFor` map keyed by your plugin
id so the widget gets a glyph and accent color:

```js
"my-plugin": { g: "🧩", c: "#9aa4b1" },
```

Without an entry it falls back to the default puzzle-piece glyph. Remember to
**rebuild the binary** (see above) after editing `app.js`.

## Frontend Dev Notes

- **Vanilla JS, no build step.** `web/assets/app.js` is hand-written ES; there is
  no bundler, transpiler, or `node_modules`. Edit and rebuild the binary.
- **Hash routing.** Views are deep-linkable via `location.hash` (e.g.
  `#dashboard`, `#settings`); the app listens for `hashchange` and renders the
  matching view, honoring the initial hash on load.
- **Server-driven, live over SSE.** Runs happen on the server (the engine
  executes each tracker on its cadence and caches one snapshot shared by all
  clients). The dashboard subscribes to `GET /api/stream` with an `EventSource`
  while it is the active view — the **Live** toggle controls this — and applies
  each pushed snapshot to its widget. The browser does **not** keep a
  localStorage widget cache or per-widget `setInterval` timers; instead, on
  connect the stream replays the current snapshots so the dashboard paints
  immediately.
- **Polling fallback.** If `EventSource` is unavailable or the stream closes, the
  app falls back to polling the cached `GET /api/run` every 8s
  (`pollTimer = setInterval(poll, 8000)`), which serves the same snapshots and
  keeps the engine considered present. A force-refresh button on each widget
  always re-runs that tracker immediately (`?force=true`).
- **Live-aging "updated X ago" label.** Each card shows when its data was fetched
  as a relative label; a single ticker re-renders all the labels every 30s
  (`setInterval(refreshUpdatedLabels, 30000)`), and hovering shows the absolute
  fetch time.
