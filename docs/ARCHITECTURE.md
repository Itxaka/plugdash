# plugdash Architecture

plugdash is a small Go dashboard server. Each widget on the dashboard is produced by a
**plugin**: a self-contained unit that, given a user-supplied configuration, runs (typically
fetching data from some external source such as the GitHub API) and returns a `Result` describing
*both* the data and *how* the frontend should visualize it. A saved plugin configuration is called
a **tracker** and is the only thing persisted, in a SQLite database. The frontend is a vanilla-JS
single-page app embedded into the binary with `go:embed`. The core idea is **just-in-time**: nothing
time-series or historical is ever stored. When a tracker runs, the plugin reconstructs everything it
needs (including charts "over time") from the timestamps on the source data it fetches right then.

Trackers do not run in the browser. A **server-side execution engine** (`internal/engine`) runs each
tracker on its own cadence and caches the *latest* result per tracker as a **`Snapshot`**, which all
connected clients share — so N clients cost one upstream API call, not N. The engine is
**presence-gated**: it only runs while at least one client is watching and goes fully idle (zero
upstream calls) otherwise. Clients receive snapshots over **Server-Sent Events** (`/api/stream`),
falling back to cached polling when SSE is unavailable. This caching is still real-time only — just
the latest snapshot, never history. Trackers may also be declared in a YAML **config file**
(`--config`) and reconciled into the store alongside ad-hoc UI-created ones.

## Table of Contents

- [Core idea](#core-idea)
- [Component diagram](#component-diagram)
- [Request / data-flow walkthrough](#request--data-flow-walkthrough)
- [Execution engine, SSE & presence](#execution-engine-sse--presence)
- [Config-as-code](#config-as-code)
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
                EventSource(/api/stream)  ──or fallback──>  GET /api/run (8s)
                                   ^  SSE snapshot events    |  HTTP + JSON  (/api/...)
                                   |                         v
+--------------------------------------------------------------------------+
|                         internal/server (HTTP handlers)                  |
|   routes:  /api/plugins[/rescan]  /api/trackers[/{id}[/run]]             |
|            /api/run  /api/stream (SSE)  /api/settings  /api/logs         |
|                                                                          |
|   /api/stream: Subscribe() -> replay snapshots + push, 25s keepalive    |
|   /api/run: serves cached snapshots, records Poll() presence            |
|   per-widget refresh -> engine.Force(id);  CRUD -> engine.Reconcile()   |
|   LogRing (ring buffer) + dynamic slog LevelVar (debug toggle)           |
+--------+-------------------+----------------------+----------------------+
         |                   |                      |                      |
         v                   v                      v                      v
+--------------------+  +-----------------+  +------------------+  +---------------------+
| internal/engine    |  | internal/plugin |  |  internal/store  |  |  internal/extplugin |
| scheduler (1s tick)|  | Plugin interface|  |  SQLite: trackers|  |  Manager (discover/ |
| Snapshot cache     |  | Registry        |  |  + settings,     |  |  rescan)            |
| presence-gated     |  | (RWMutex)       |  |  source=file/db, |  |  ExternalPlugin     |
| SSE subscribers    |  | context logger  |  |  migrate         |  |  adapter (exec)     |
| sem(8), 30s/run    |  +--------+--------+  |  + config_key    |  +----------+----------+
+---------+----------+           |          +--------+---------+             |
          |                      |                   ^                       |
          | runs trackers        |                   | reconciles trackers  | registers
          | (reg.Get +           |          internal/config (--config YAML) -+ into
          |  plugin.Run, caches  |                                            registry
          |  Snapshot, SSE push) |                                            |
          +----------+-----------+ <----------------------------------------+
                     | Registry holds, keyed by ID:
                     |   - built-in plugins (internal/plugins/*)
                     |   - external plugins (adapters)
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

**How a widget gets its data.** Unlike a classic request-per-widget design, the browser does not
trigger plugin runs. The **engine** runs trackers on the server on their own cadence, caches the
latest **`Snapshot`** per tracker, and pushes it to clients:

1. **Browser** opens an `EventSource` on `GET /api/stream`. The server `Subscribe()`s it to the
   engine, which immediately **replays the current snapshots** so the dashboard paints from cache.
2. Whenever the **engine** re-runs a tracker (because its interval elapsed, a viewer just arrived, or
   a per-widget refresh forced it), it caches the new snapshot and **broadcasts** a `snapshot` SSE
   event to every subscriber.
3. The **frontend renderer** (`renderViz` in `app.js`) switches on `snapshot.result.visualization`
   and calls the matching renderer (`renderList`, `renderTable`, `renderChecklist`, `renderStat`,
   `renderTimeseries`; unknown types fall back to `renderRaw`), repainting just that widget.
4. If SSE fails or closes, the browser **falls back to polling `GET /api/run` every 8s** — a cached
   read that returns all current snapshots *and* keeps the engine considered "present" (`Poll()`).

So one upstream call per tracker per interval serves every connected client, and no upstream call is
made at all while nobody is watching.

**Inside a single engine run** (`engine.runOne`, the mechanics each scheduled or forced run uses):

1. The engine looks the plugin up in the **registry** (`reg.Get(t.PluginID)`). Unknown plugin ids
   become a per-tracker `Snapshot.Error`, not a crashed run.
2. It builds a context with a **tracker-scoped logger** (`plugin.WithLogger`) tagged with
   `tracker_id`, `plugin`, and `tracker` so everything the plugin logs is attributable.
3. It applies a **30s per-run timeout** (`context.WithTimeout`), gates on the **semaphore capped at
   8** concurrent runs, and calls `plugin.Run(ctx, plugin.Config(t.Config))`.
4. The plugin fetches from its source and returns a **`Result{Visualization, Title, Data}`** (or an
   error, captured into `Snapshot.Error`).
5. The engine stamps `FetchedAt`, stores the `Snapshot` in its cache, and broadcasts it (above).

The cached-read paths reuse those snapshots without re-running plugins:

- `GET /api/run` (`handleRunAll`) serves the engine's current snapshots in **tracker order** and
  records presence so the engine keeps scheduling for poll-only clients. It never fans out plugin
  runs itself.
- `GET /api/trackers/{id}/run` / per-widget refresh calls `engine.Force(id)`, which runs that one
  tracker immediately regardless of presence or interval, then returns/pushes its snapshot.
- A per-tracker failure is isolated in that snapshot's `Error`; other widgets are unaffected.

Other endpoints follow the same shape: `GET /api/stream` for the SSE snapshot feed,
`GET/POST/PUT/DELETE /api/trackers` for CRUD (each mutation triggers `engine.Reconcile()`),
`GET /api/plugins` to list available plugins + their config schema (so the UI can build forms),
`POST /api/plugins/rescan` to re-discover external plugins, `GET/PUT /api/settings`, and
`GET/DELETE /api/logs`. File-defined (`source="file"`) trackers reject UI edits/deletes.

## Execution engine, SSE & presence

The **`internal/engine`** package moves tracker execution off the browser and onto the server, so the
cost of a busy dashboard is decoupled from the number of viewers.

- **Server-side scheduling.** A single goroutine loop (`Engine.loop`) ticks every **1s** and runs any
  tracker whose **effective interval** has elapsed — the tracker's `refresh_interval_seconds`
  override if set, else the plugin's declared default, else **60s** (`effectiveInterval`). Runs are
  bounded by a **semaphore of 8** concurrent runs and a **30s per-run timeout**, identical to the old
  in-handler limits. A tracker already running is never double-started.
- **Snapshot cache.** Each run produces a **`Snapshot`** (`{tracker_id, name, plugin_id,
  refresh_interval_seconds, result | error, fetched_at}`) stored in an in-memory map keyed by tracker
  id. This is the latest result per tracker, **no history** — fully consistent with the just-in-time
  principle (still no time-series store; charts are reconstructed per run). All clients read from this
  one cache, so **N clients = 1 upstream call** per tracker per interval.
- **Warm restart (persistence).** Every snapshot is also written to a `snapshots` table (one row per
  tracker, overwritten each run — last-known state, *not* history; rows cascade-delete with their
  tracker). On `Start`, the engine loads them back into the cache **and restores each tracker's
  `lastRun` from the persisted `fetched_at`**. So a restart paints the dashboard instantly from
  last-known data and, crucially, does **not** re-run trackers whose interval hasn't elapsed —
  avoiding a burst of fresh upstream calls on every redeploy/crashloop. (The ETag cache is not yet
  persisted, so a tracker that *is* legitimately due after a long downtime makes a full call on its
  next run rather than a free 304.)
- **Presence-gating.** The scheduler only runs trackers while at least one client is **present**.
  Presence = an open SSE subscriber on `/api/stream`, **or** a cached-poll `GET /api/run` within the
  last **20s** (`pollPresenceTTL`, recorded by `Poll()`). With nobody watching, `clientCount()`
  returns 0 and the loop idles, making **zero upstream calls**. This is the key efficiency property: a
  dashboard left closed costs nothing.
- **SSE push.** `GET /api/stream` is a Server-Sent Events endpoint. `Engine.Subscribe()` returns a
  buffered channel pre-loaded with **all current snapshots** (so a fresh client paints from cache
  immediately) and registers the subscriber; thereafter every re-run is `broadcast` as a
  `snapshot` event. The handler emits **25s keepalive** comments. A slow subscriber's buffer
  (`subBuffer = 64`) **drops frames rather than blocking** the engine. Subscribing counts as presence
  and nudges the scheduler, so the first viewer triggers an immediate refresh of due work.
- **Polling fallback.** If `EventSource` fails or closes, the browser polls `GET /api/run` every
  **8s**. That is a cached read of `SnapshotAll()` (tracker order, runs nothing) which also calls
  `Poll()` to keep presence alive — so poll-only clients still drive the scheduler.
- **First-run stagger (anti-thundering-herd).** A tracker that has never run becomes due at
  `startedAt + phaseOffset(id)`, where the offset is a deterministic hash of the tracker id capped to
  `min(interval, 10s)`. Same-interval trackers therefore don't all fire on the same tick — their
  later runs anchor to these offset first-run times and stay de-aligned — while every widget still
  gets its first data within ~10s. 0/sub-second-interval trackers ("run ASAP") are never staggered.
- **Force.** A per-widget refresh calls `Engine.Force(id)`, enqueued on `forceC` and serviced
  immediately by the loop; it runs that one tracker **regardless of presence or interval**.
- **Reconcile.** After any tracker create/update/delete (or a config reload), the server calls
  `Engine.Reconcile()`, which reloads the tracker set from the store and **drops cached snapshots and
  last-run times for trackers that were removed or whose config/interval/plugin changed**
  (`trackerChanged`) so nothing is served stale against an old config. New trackers run on the next
  tick.

## Config-as-code

An optional `--config <file.yaml>` declaratively defines trackers, reconciled into the SQLite store at
startup by **`internal/config`** alongside ad-hoc trackers created in the UI — a **hybrid model**.

- Trackers from the file are stored with **`source="file"`** and are **read-only in the UI** (the API
  rejects edits/deletes of them). Trackers created through the UI are **`source="db"`**.
- Reconciliation matches file entries by a stable **`config_key`**, so a file-defined tracker keeps
  its row id (and therefore its dashboard order) across restarts even as its config changes.
- Entries removed from the file are **deleted on the next start**; **`source="db"` trackers are never
  touched** by reconciliation. After reconciling, the engine's `Reconcile()` picks up the new set.

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

### `internal/engine` — server-side execution & SSE fan-out

- **`Engine`** owns the scheduler loop, the per-tracker `Snapshot` cache, the SSE subscriber set, and
  presence tracking, all guarded by a single `sync.Mutex`. Constructed with the registry, store, and
  logger; `Reconcile()` loads the initial tracker set and `Start()` launches `loop`.
- **`loop`** ticks every **1s** and, *only while a client is present*, runs every due tracker
  (`runDue`); it services `Force` requests immediately regardless of presence, and a `wake` nudge
  (new subscriber / reconcile / returning poller) runs due work without waiting for the next tick.
- **`runOne`** is the run mechanics: semaphore (`maxConcurrent = 8`), `runTimeout = 30s`,
  tracker-scoped logger, `reg.Get` + `plugin.Run`, then cache the `Snapshot` and `broadcast`.
- **Presence** (`clientCount`): open SSE subscribers plus a recent `Poll()` within `pollPresenceTTL`
  (**20s**). Zero presence ⇒ the loop runs nothing ⇒ zero upstream calls.
- **Fan-out**: `Subscribe()` (replay + register, returns frame channel + unsubscribe), `broadcast`
  (non-blocking send to all subs; `subBuffer = 64`, drop on slow client), `Snapshot`/`SnapshotAll`
  (cached reads), `Force` (immediate one-tracker run), `Reconcile` (reload + drop stale snapshots).

### `internal/config` — declarative trackers (config-as-code)

- Parses an optional `--config <file.yaml>` into tracker definitions and reconciles them into the
  store at startup. File trackers are stored `source="file"` and matched on a stable `config_key` so
  they keep their ids/order across restarts; entries dropped from the file are deleted next start;
  `source="db"` (UI-created) trackers are left untouched. The engine then `Reconcile()`s the result.

### `internal/store` — persistence (SQLite)

- Pure-Go driver `modernc.org/sqlite` (no CGO). Opened in WAL mode with foreign keys on.
- **`Tracker`**: `{ ID, PluginID, Name, Config map[string]any, RefreshIntervalSeconds, Source,
  ConfigKey, CreatedAt }`. `RefreshIntervalSeconds == 0` means "use the plugin default". `Source` is
  `"db"` (UI-created) or `"file"` (config-as-code, read-only in the UI); `ConfigKey` is the stable
  match key used to reconcile file trackers. `Config` is stored as JSON text.
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

### `internal/server` — HTTP API & engine front-end

- **`Server`** wires the registry, store, **engine**, embedded static FS, logger, log ring, and
  dynamic level into an `http.Handler` via a `http.ServeMux`. `routes()` registers the `/api/...`
  endpoints and serves the SPA at `/` from the embedded FS.
- **Handlers** cover plugin listing/rescan, tracker CRUD + run, the cached run-all, the SSE stream,
  settings, and logs. `handleListPlugins` emits a `pluginDTO` per plugin including its schema and an
  `external` flag (detected via an `IsExternal()` interface assertion, avoiding an import of
  `extplugin`).
- **Engine delegation**: the server no longer runs plugins inline. `GET /api/stream` subscribes the
  caller to the engine and streams `snapshot` events with 25s keepalives; `GET /api/run` serves
  `SnapshotAll()` and records `Poll()` presence; per-widget refresh calls `engine.Force(id)`; every
  tracker CRUD mutation calls `engine.Reconcile()`. Edits/deletes of `source="file"` trackers are
  rejected.
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
- **GitHub client resilience.** Plugins build a fresh `GHClient` per run, so two safeguards live in
  **process-global** state (keyed by a token fingerprint so auth contexts never mix): an **ETag
  conditional-request cache** — `GHClient.Get` stores each response's `ETag`/body and replays
  `If-None-Match`, so an unchanged resource returns **`304 Not Modified` (free, no rate-limit cost)**
  and the cached body is reused; and a **rate-limit back-off** — on a `403`/`429` carrying
  `Retry-After` or an exhausted `X-RateLimit-Remaining`, the reset time is recorded and further calls
  for that token **fail fast** until it passes, instead of hammering the API.

### `web` — embedded SPA

- `web.go` uses `//go:embed assets` and exposes `web.FS()`, re-rooted so `index.html` is at `/`. The
  server mounts it with `http.FileServer`.
- `web/assets/app.js` is a vanilla-JS SPA: it subscribes to `/api/stream` with an `EventSource`
  (falling back to polling `/api/run` every 8s if SSE fails/closes), calls the other `/api/...`
  endpoints, renders the dashboard / configure / settings / logs screens, and maps each snapshot's
  `Result.visualization` to a renderer in `renderViz`. File-defined trackers render without
  edit/delete controls.

## Plugin lifecycle

1. **Discovery / registration (startup).** `main` builds a `Registry` and registers every built-in
   plugin's `New()`. If a plugins directory is resolved (`-plugins-dir` flag, then
   `PLUGDASH_PLUGINS_DIR`, then `~/.config/plugdash/plugins`), an `extplugin.Manager` discovers
   external executables and registers their adapters into the *same* registry. From here on, internal
   and external plugins are indistinguishable to the rest of the system.
2. **Configuration → tracker.** A user picks a plugin in the UI (its `ConfigSchema` drives the form),
   fills it in, and the config is persisted as a `source="db"` `Tracker` row via `POST /api/trackers`.
   Alternatively, trackers are declared in the `--config` YAML and reconciled in as `source="file"`
   (read-only). Either way the plugin id is fixed for the life of the tracker. Each create/update/
   delete (and the config reconcile) calls `engine.Reconcile()`.
3. **Run on the server, on a cadence.** The **engine** runs each tracker on its **effective interval**
   while at least one client is present, caches the latest result as a **`Snapshot`**, and pushes it
   over SSE; when nobody is watching it idles and makes no upstream calls. Each run gets a fresh **30s
   timeout** and a **tracker-scoped logger**. A per-widget refresh (`engine.Force`) runs one tracker
   immediately regardless of interval/presence. Results are never stored beyond the latest snapshot;
   anything historical is reconstructed from source timestamps on each run.
4. **Rescan.** External plugins can be re-discovered at runtime via `POST /api/plugins/rescan`
   without restarting; built-in plugins are untouched.

## Concurrency & safety

- **Registry** is guarded by a `sync.RWMutex`: many concurrent `Get`/`List` reads, exclusive
  `Register`/`Unregister`. `Register` panics on a duplicate id (a startup programming error).
- **Engine** keeps its snapshot cache, last-run map, running-set, subscriber set, and presence
  timestamp under one `sync.Mutex`; the scheduler is a single goroutine and each run is its own
  goroutine, so no plugin runs hold the lock. A tracker already in `running` is never double-started.
- **Concurrency cap** is a buffered-channel semaphore of **8** in the engine, so a large number of
  trackers can't spawn unbounded goroutines or connections.
- **SSE broadcast** is non-blocking: a subscriber whose 64-frame buffer is full has the frame
  dropped rather than stalling the engine; unsubscribe closes the channel under the lock.
- **Per-run timeout** of 30s bounds every plugin invocation; per-tracker errors are isolated into the
  snapshot's `Error` and never fail other trackers or the stream.
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
│                               built-ins, load external plugins, reconcile --config,
│                               start engine + HTTP server.
├── internal/
│   ├── plugin/
│   │   ├── plugin.go           Plugin interface, Result, Config, Visualization, ConfigField.
│   │   ├── registry.go         Concurrent (RWMutex) plugin registry keyed by id.
│   │   └── log.go              Context-carried tracker-scoped logger (WithLogger/LoggerFrom).
│   ├── engine/
│   │   └── engine.go           Server-side scheduler, Snapshot cache, presence-gating,
│   │                           SSE fan-out (1s tick, sem 8, 30s/run).
│   ├── config/                 --config YAML parsing + store reconcile (source=file).
│   ├── store/
│   │   ├── store.go            SQLite Tracker CRUD (source/config_key) + migrations.
│   │   └── settings.go         Dashboard-wide settings (single JSON row) + bounds.
│   ├── server/
│   │   ├── server.go           HTTP handlers; /api/stream SSE; delegates runs to engine.
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
  reconstructed from the timestamps on the source data at run time. There is no time-series store and
  no persisted history; the only cache is the engine's in-memory **latest snapshot per tracker**
  (dropped/invalidated on reconcile), so there is no stale-data reconciliation to maintain.
- **Run on the server, share one result, idle when unwatched.** Plugin execution lives in the engine,
  not the browser. All clients read one cached snapshot per tracker, so cost scales with the number
  of *trackers*, not the number of *viewers* — and because scheduling is presence-gated, a dashboard
  nobody is looking at makes zero upstream calls. Real-time push (SSE) with a cached-poll fallback
  keeps clients current without each one driving its own API calls.
- **Hybrid declarative + ad-hoc config.** Trackers can be declared in a YAML file
  (`source="file"`, read-only, reconciled by stable `config_key`) or added through the UI
  (`source="db"`). Reconciliation only ever touches file trackers, so the two sources coexist without
  clobbering each other across restarts.
- **Plugins isolated behind one interface.** Everything a data source needs to do is expressed
  through `plugin.Plugin` (+ `Config` in, `Result` out). The server, registry, and frontend never
  know what a plugin does internally; the frontend only knows the five visualization tags.
- **Internal and external plugins are indistinguishable.** External executables are wrapped by an
  adapter that implements the same interface and live in the same registry. The only place the
  distinction surfaces is a cosmetic `external` flag in the plugins API; run orchestration, tracker
  storage, and rendering treat both identically.
