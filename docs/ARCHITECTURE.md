# plugdash Architecture

plugdash is a small Go dashboard server. Each widget on the dashboard is produced by a
**plugin**: a self-contained unit that, given a user-supplied configuration, runs (typically
fetching data from some external source such as the GitHub API) and returns a `Result` describing
*both* the data and *how* the frontend should visualize it. A saved plugin configuration is called
a **tracker** and is the only thing persisted, in a SQLite database. The frontend is a vanilla-JS
single-page app embedded into the binary with `go:embed`. The core idea is **just-in-time**: nothing
time-series or historical is ever stored. When a tracker runs, the plugin reconstructs everything it
needs (including charts "over time") from the timestamps on the source data it fetches right then.

## Table of Contents

- [Core idea](#core-idea)
- [Component diagram](#component-diagram)
- [Request / data-flow walkthrough](#request--data-flow-walkthrough)
- [Components](#components)
- [Plugin lifecycle](#plugin-lifecycle)
- [Concurrency & safety](#concurrency--safety)
- [Project layout](#project-layout)
- [Design principles](#design-principles)

## Core idea

```
Plugin.Run(ctx, Config) -> Result{ Visualization, Title, Data }
```

- A **plugin** implements one interface (`plugin.Plugin`). It declares its config schema, an advisory
  refresh cadence, and a `Run` method.
- A **tracker** is a saved instance of a plugin plus the config a user supplied for it (e.g. "the
  `github-releases` plugin pointed at `kubernetes/kubernetes`"). Trackers live in SQLite.
- A **`Result`** pairs the data with a `Visualization` tag (`list`, `table`, `checklist`, `stat`,
  `timeseries`). The frontend maps each tag to a renderer; the server and plugin never touch the DOM.
- **Nothing time-series is persisted.** A "stars over time" chart is computed just-in-time from the
  timestamped events the plugin fetches on each run. The database holds *configuration only*, never
  results or history.

## Component diagram

```
                         Browser (embedded SPA: web/assets/app.js)
                                   |  HTTP + JSON  (/api/...)
                                   v
+--------------------------------------------------------------------------+
|                         internal/server (HTTP handlers)                  |
|   routes:  /api/plugins[/rescan]  /api/trackers[/{id}[/run]]             |
|            /api/run  /api/settings  /api/logs                            |
|                                                                          |
|   runTracker(): per-run 30s timeout + tracker-scoped slog logger         |
|   handleRunAll(): fan-out with concurrency cap (8)                       |
|   LogRing (ring buffer) + dynamic slog LevelVar (debug toggle)           |
+----------------+----------------------------+----------------------------+
                 |                            |                            |
                 v                            v                            v
   +-----------------------+     +------------------------+     +---------------------+
   |  internal/plugin      |     |   internal/store       |     |  internal/extplugin |
   |  Plugin interface     |     |   SQLite: trackers     |     |  Manager (discover/ |
   |  Registry (RWMutex)   |     |   + settings, migrate  |     |  rescan)            |
   |  context logger       |     |   (modernc.org/sqlite) |     |  ExternalPlugin     |
   +-----------+-----------+     +------------------------+     |  adapter (exec)     |
               |                                                +----------+----------+
               | Registry holds, keyed by ID:                             |
               |   - built-in plugins (internal/plugins/*)                | registers
               |   - external plugins (adapters) <------------------------+ into registry
               v
   +-----------------------------------------------------------+
   |  internal/plugins/*  (built-in plugins)                   |
   |  github-releases, github-activity, http-health, rss-feed, |
   |  docker-image, ...  + shared GitHub / registry / activity |
   |  helpers (github.go, registry.go, ghactivity.go)          |
   +-----------------------------------------------------------+

   web (go:embed) supplies the static SPA filesystem to the server.
   cmd/plugdash/main.go wires store + registry + server + extplugin together.
```

## Request / data-flow walkthrough

A single tracker run (`GET /api/trackers/{id}/run`):

1. **Browser** calls `api("/api/trackers/{id}/run")` (see `web/assets/app.js`).
2. **HTTP API** (`server.handleRunTracker`) parses the id and loads the tracker from the **store**
   (`store.GetTracker`). Missing rows become `404`.
3. The handler builds a request context with `runCtx`, attaching a **tracker-scoped logger**
   (`plugin.WithLogger`) tagged with `tracker_id`, `plugin`, and `tracker` so everything the plugin
   logs is attributable.
4. `runTracker` looks the plugin up in the **registry** (`reg.Get(t.PluginID)`). Unknown plugin ids
   are returned as a per-tracker error, not a crashed request.
5. It computes the effective refresh cadence (tracker override if set, else the plugin default),
   applies a **30s per-run timeout** (`context.WithTimeout`), and calls
   `plugin.Run(ctx, plugin.Config(t.Config))`.
6. The plugin fetches from its source and returns a **`Result{Visualization, Title, Data}`** (or an
   error, which is captured into the response, not propagated as an HTTP failure).
7. The handler serializes the `runResponse` (tracker metadata + `Result` *or* `Error`) to **JSON**.
8. The **frontend renderer** (`renderViz` in `app.js`) switches on `result.visualization` and calls
   the matching renderer (`renderList`, `renderTable`, `renderChecklist`, `renderStat`,
   `renderTimeseries`; unknown types fall back to `renderRaw`).

The **"run all"** path (`GET /api/run`, `handleRunAll`):

- Loads every tracker, then fans out one goroutine per tracker, each gated by a **semaphore capped at
  8** concurrent runs.
- Each goroutine writes into a pre-sized `results[i]` slice, so output **preserves tracker order**.
- A per-tracker failure is recorded in that tracker's `runResponse.Error`; the request as a whole
  still returns `200` with the full array. The frontend renders one widget per entry.

Other endpoints follow the same shape: `GET/POST/PUT/DELETE /api/trackers` for CRUD,
`GET /api/plugins` to list available plugins + their config schema (so the UI can build forms),
`POST /api/plugins/rescan` to re-discover external plugins, `GET/PUT /api/settings`, and
`GET/DELETE /api/logs`.

## Components

### `internal/plugin` — the contract

- **`Plugin` interface**: `ID()`, `Name()`, `Description()`, `ConfigSchema() []ConfigField`,
  `RefreshInterval() time.Duration`, `Run(ctx, Config) (Result, error)`. Implementations must be
  safe for concurrent use.
- **`Result`**: `{ Visualization, Title, Data any }`. `Data` must be JSON-serializable and match the
  shape the chosen `Visualization` expects.
- **`Visualization`** constants: `list`, `table`, `checklist`, `stat`, `timeseries`. `timeseries`
  data carries `points:[{t, v}]` in ascending time order — the just-in-time chart shape.
- **`Config`** is a `map[string]any` with typed accessors (`String`, `Int`, `Bool`, `List`). `List`
  accepts a JSON array or a comma/newline-separated string.
- **`ConfigField`** + `FieldType` (`string`, `number`, `bool`, `list`, `select`) describe form
  inputs; the server exposes these verbatim so the UI renders forms without knowing plugin internals.
- **`Registry`** (`registry.go`): a map of plugins keyed by ID, guarded by an `sync.RWMutex`.
  `Register` (panics on duplicate id), `Unregister` (used to swap external plugins; built-ins are
  never removed), `Get`, and `List` (sorted by id).
- **Context logger** (`log.go`): `WithLogger` / `LoggerFrom`. The server attaches a tracker-scoped
  logger to the context before each run; plugins and shared helpers call
  `plugin.LoggerFrom(ctx).Debug(...)`. With no logger present, a discard logger is returned so
  callers never nil-check.

### `internal/store` — persistence (SQLite)

- Pure-Go driver `modernc.org/sqlite` (no CGO). Opened in WAL mode with foreign keys on.
- **`Tracker`**: `{ ID, PluginID, Name, Config map[string]any, RefreshIntervalSeconds, CreatedAt }`.
  `RefreshIntervalSeconds == 0` means "use the plugin default". `Config` is stored as JSON text.
- **CRUD**: `CreateTracker`, `UpdateTracker` (plugin_id is intentionally immutable so the stored
  config never mismatches a different schema), `GetTracker`, `ListTrackers` (ordered by creation),
  `DeleteTracker`.
- **`migrate()`**: creates the `trackers` and `settings` tables, idempotently adds columns introduced
  later (`ensureColumn` checks `pragma_table_info` first), and rewrites a renamed plugin id
  (`github-stars-history` → `github-activity`) for old databases.
- **`Settings`** (`settings.go`): a single JSON row (`id = 1`, upserted). Holds
  `AutoRefreshEnabled`, `AutoRefreshInterval` (clamped 5–3600s, default 60), `DashboardOrder`,
  `Debug`, and `GitHubToken`. `GetSettings` falls back to `DefaultSettings()` when unset and
  `normalize()`s bounds.

### `internal/server` — HTTP API & run orchestration

- **`Server`** wires the registry, store, embedded static FS, logger, log ring, and dynamic level
  into an `http.Handler` via a `http.ServeMux`. `routes()` registers the `/api/...` endpoints and
  serves the SPA at `/` from the embedded FS.
- **Handlers** cover plugin listing/rescan, tracker CRUD + run, run-all, settings, and logs.
  `handleListPlugins` emits a `pluginDTO` per plugin including its schema and an `external` flag
  (detected via an `IsExternal()` interface assertion, avoiding an import of `extplugin`).
- **Run orchestration**: `runTracker` applies the 30s timeout and tracker-scoped logger and captures
  per-tracker errors; `handleRunAll` fans out under a semaphore cap of 8.
- **Settings side effects**: saving settings flips the dynamic debug level (`applyDebugLevel`) and
  exports a configured `GitHubToken` as the `GITHUB_TOKEN` env var for all GitHub plugins.
- **Log ring + dynamic level** (`logbuffer.go`): `LogRing` is a fixed-size, mutex-guarded ring buffer
  of recent `LogEntry`s (1000 in `main`). `NewRingHandler` wraps a base `slog.Handler`, writing every
  enabled record to both stderr and the ring. A shared `slog.LevelVar` lets `-debug` / `PLUGDASH_DEBUG`
  / the persisted setting / the Settings UI raise the level to `Debug` at runtime. The buffer is
  served at `GET /api/logs` and cleared via `DELETE /api/logs`.

### `internal/extplugin` — external (out-of-process) plugins

- Lets plugins ship as standalone executables in *any* language. A binary named
  `plugdash-plugin-<name>` speaks a tiny stdio protocol: `describe` prints metadata JSON; `run` reads
  config JSON on stdin and writes a `Result` JSON to stdout.
- **`ExternalPlugin`** (`external.go`) adapts such a binary to the `plugin.Plugin` interface by
  shelling out, so the registry, server, and frontend treat it identically to a built-in. Metadata is
  captured once at discovery (`describe`); each `Run` re-execs (`exec.CommandContext`), matching the
  stateless/just-in-time model. stdout/stderr are size-capped (8 MiB via `limitedBuffer`); stderr
  lines are surfaced as debug logs; the per-run timeout comes from the context; `PLUGDASH_DEBUG=1` is
  passed through when debug is on. It reports `IsExternal() == true`; declared refresh of `<= 0`
  defaults to a conservative 1h.
- **`Manager`** (`manager.go`) owns discovery + lifecycle. `discoverDir` lists executables prefixed
  `plugdash-plugin-` in deterministic order, runs `describe` (5s timeout) on each, and skips bad
  entries with logged warnings rather than aborting. `Rescan` reconciles the registry: register new
  binaries, unregister vanished ones, re-describe/replace changed ones, and skip ids that collide
  with a built-in. `Load` is the initial scan. The server reaches it through the narrow
  `PluginRescanner` interface (`Rescan`, `Dir`).

### `internal/plugins/*` — built-in plugins + shared helpers

- One package per plugin, each `New()`-constructed and registered in `main`. Built-in ids include:
  `github-releases`, `github-release-artifacts`, `github-repo-stats`, `github-actions-status`,
  `github-activity`, `github-activity-rate`, `github-issues`, `http-health`, `rss-feed`,
  `docker-image`.
- **Shared helpers** in the `plugins` package root: `github.go` (`GHClient` — authenticated GitHub
  REST calls, falling back to `GITHUB_TOKEN`), `registry.go` (container image reference validation /
  registry queries), and `ghactivity.go` (paginated activity fetching and the metric options the
  activity/stars plugins plot). These centralize the "fetch + reconstruct timeseries from timestamps"
  logic the just-in-time charts rely on.

### `web` — embedded SPA

- `web.go` uses `//go:embed assets` and exposes `web.FS()`, re-rooted so `index.html` is at `/`. The
  server mounts it with `http.FileServer`.
- `web/assets/app.js` is a vanilla-JS SPA: it calls the `/api/...` endpoints, renders the dashboard /
  configure / settings / logs screens, and maps each `Result.visualization` to a renderer in
  `renderViz`.

## Plugin lifecycle

1. **Discovery / registration (startup).** `main` builds a `Registry` and registers every built-in
   plugin's `New()`. If a plugins directory is resolved (`-plugins-dir` flag, then
   `PLUGDASH_PLUGINS_DIR`, then `~/.config/plugdash/plugins`), an `extplugin.Manager` discovers
   external executables and registers their adapters into the *same* registry. From here on, internal
   and external plugins are indistinguishable to the rest of the system.
2. **Configuration → tracker.** A user picks a plugin in the UI (its `ConfigSchema` drives the form),
   fills it in, and the config is persisted as a `Tracker` row via `POST /api/trackers`. The plugin
   id is fixed for the life of the tracker.
3. **Run on demand.** A tracker runs only when asked — a single run, a run-all, or the optional
   auto-refresh timer. Each run gets a fresh **30s timeout** and a **tracker-scoped logger** attached
   to the context. The plugin fetches live data and returns a `Result`. Results are never stored;
   anything historical is reconstructed from source timestamps on each run.
4. **Rescan.** External plugins can be re-discovered at runtime via `POST /api/plugins/rescan`
   without restarting; built-in plugins are untouched.

## Concurrency & safety

- **Registry** is guarded by a `sync.RWMutex`: many concurrent `Get`/`List` reads, exclusive
  `Register`/`Unregister`. `Register` panics on a duplicate id (a startup programming error).
- **Run-all** caps concurrency with a buffered-channel semaphore of **8**, so a large number of
  trackers can't spawn unbounded goroutines or connections. Results are written into a pre-sized,
  index-addressed slice (no shared mutation), preserving order without a lock.
- **Per-run timeout** of 30s bounds every plugin invocation; per-tracker errors are isolated into the
  response and never fail the whole batch.
- **External exec safety**: `exec.CommandContext` kills the process on context cancel; `WaitDelay`
  (2s) bounds how long exec waits afterward for lingering children holding the output pipes;
  stdout/stderr are capped at 8 MiB to guard against runaway processes.
- **LogRing** and the **store** (`database/sql` handle, WAL mode) are safe for concurrent access; the
  log level is a single shared `slog.LevelVar` flipped atomically.
- **Manager.Rescan** holds its own mutex and reconciles the registry deterministically (sorted
  discovery order) so id-collision resolution is stable.

## Project layout

```
plugdash/
├── cmd/plugdash/
│   └── main.go                 Entry point: open store, build logger/ring, register
│                               built-ins, load external plugins, start HTTP server.
├── internal/
│   ├── plugin/
│   │   ├── plugin.go           Plugin interface, Result, Config, Visualization, ConfigField.
│   │   ├── registry.go         Concurrent (RWMutex) plugin registry keyed by id.
│   │   └── log.go              Context-carried tracker-scoped logger (WithLogger/LoggerFrom).
│   ├── store/
│   │   ├── store.go            SQLite Tracker CRUD + schema migrations (modernc.org/sqlite).
│   │   └── settings.go         Dashboard-wide settings (single JSON row) + bounds.
│   ├── server/
│   │   ├── server.go           HTTP handlers, run orchestration, run-all semaphore (8).
│   │   └── logbuffer.go        Log ring buffer + slog ring handler.
│   ├── extplugin/
│   │   ├── external.go         ExternalPlugin adapter: stdio describe/run protocol.
│   │   └── manager.go          Discovery + rescan of plugdash-plugin-* executables.
│   └── plugins/
│       ├── github.go           Shared authenticated GitHub REST client (GHClient).
│       ├── registry.go         Shared container-image / registry helpers + validation.
│       ├── ghactivity.go       Shared paginated activity fetch + plottable metrics.
│       ├── githubreleases/  github-releases plugin.
│       ├── githubartifacts/ github-release-artifacts plugin.
│       ├── githubrepostats/ github-repo-stats plugin.
│       ├── githubactions/   github-actions-status plugin.
│       ├── githubstars/     github-activity plugin (stars/activity over time).
│       ├── githubrate/      github-activity-rate plugin.
│       ├── githubissues/    github-issues plugin.
│       ├── httphealth/      http-health plugin.
│       ├── rssfeed/         rss-feed plugin.
│       └── dockerimage/     docker-image plugin.
├── web/
│   ├── web.go                  go:embed of assets/, exposes web.FS().
│   └── assets/
│       └── app.js              Vanilla-JS SPA: API client + per-visualization renderers.
├── examples/
│   └── plugins/                Example external plugins (e.g. plugdash-plugin-example).
└── docs/
    └── ARCHITECTURE.md         This document.
```

## Design principles

- **Just-in-time, no stored history.** The database stores configuration (trackers + settings) only.
  Every run fetches fresh data, and any "over time" view (e.g. a `timeseries` stars chart) is
  reconstructed from the timestamps on the source data at run time. There is no time-series store, no
  cache of results, and therefore no stale-data reconciliation to maintain.
- **Plugins isolated behind one interface.** Everything a data source needs to do is expressed
  through `plugin.Plugin` (+ `Config` in, `Result` out). The server, registry, and frontend never
  know what a plugin does internally; the frontend only knows the five visualization tags.
- **Internal and external plugins are indistinguishable.** External executables are wrapped by an
  adapter that implements the same interface and live in the same registry. The only place the
  distinction surfaces is a cosmetic `external` flag in the plugins API; run orchestration, tracker
  storage, and rendering treat both identically.
