# Writing a plugdash plugin

A plugin is a self-contained data source. Given a user-supplied configuration it
runs (typically fetching from some external API) and returns a `Result` that
describes both the data and how the dashboard should render it. This guide walks
through the contract, the visualization data shapes, the config schema, the
just-in-time design philosophy, and how to register a new plugin.

All of the interfaces and helpers below live in `internal/plugin/plugin.go`.

## Design philosophy: just-in-time, nothing stored

plugdash does **not** store time-series or historical data. The store persists
only *trackers* — a plugin id plus a saved config. Every value a widget shows is
**computed at request time** by the plugin's `Run` method, freshly each time it
runs.

This is most visible in the `github-activity` plugin: rather than keeping a
running log of star counts, it reconstructs the entire stars-over-time series on
every run by fetching each star's `starred_at` timestamp from GitHub (via the
`application/vnd.github.star+json` media type) and bucketing them into a
cumulative daily series. There is no database of historical points — the history
is derived live from the source's own timestamps.

Consequences for plugin authors:

- Your `Run` is the single source of truth. Compute the whole result each call;
  do not assume any prior state survives between runs.
- If you need "over time" data, derive it from timestamps the source already
  carries (created/published/starred dates), not from accumulated local state.
- Because every run does real work, declare a sensible `RefreshInterval` (see
  below) so expensive reconstructions are not repeated more often than needed.

## 1. Implement the `plugin.Plugin` interface

Every plugin implements this interface, copied verbatim from
`internal/plugin/plugin.go`:

```go
// Plugin is implemented by every data source. Implementations must be safe for
// concurrent use: Run may be invoked for multiple trackers at once.
type Plugin interface {
	// ID is the stable machine identifier (e.g. "github-releases").
	ID() string
	// Name is the human-friendly label shown in the UI.
	Name() string
	// Description is a one-line explanation of what the plugin tracks.
	Description() string
	// ConfigSchema lists the configuration fields the plugin accepts.
	ConfigSchema() []ConfigField
	// RefreshInterval is the minimum time the dashboard should wait between
	// automatic re-runs of this plugin. It lets a plugin tell the dashboard how
	// fresh its data needs to be, so cheap/volatile sources (health checks) can
	// poll often while expensive/slow-moving ones (releases, star history) avoid
	// hammering external APIs. Users can always force an immediate refresh from
	// the widget. The value is advisory but the dashboard honors it as a floor.
	RefreshInterval() time.Duration
	// Run executes the plugin against cfg and returns a Result.
	Run(ctx context.Context, cfg Config) (Result, error)
}
```

Each method:

- **`ID() string`** — stable, unique machine identifier (e.g. `"github-releases"`).
  The registry **panics** at startup on a duplicate id. Trackers reference a
  plugin by this id, so do not change it once shipped.
- **`Name() string`** — human-friendly label shown in the UI's plugin picker and
  on widgets.
- **`Description() string`** — one-line explanation shown next to the name.
- **`ConfigSchema() []ConfigField`** — the configuration fields the plugin
  accepts. The server serializes these so the config UI can render a form
  without knowing anything about the plugin internals (see section 3).
- **`RefreshInterval() time.Duration`** — how long the dashboard should wait
  between automatic re-runs of this plugin (see below).
- **`Run(ctx, cfg) (Result, error)`** — does the work and returns a `Result`.
  Must be safe for concurrent use; it may run for several trackers at once. The
  server applies a 30-second timeout to each `Run` via the passed `ctx` — honor
  it (pass it to your HTTP requests). Returning an `error` surfaces as the
  tracker's `error` field. If a "bad" outcome is still a meaningful result (e.g.
  an endpoint being DOWN, or one repo in a checklist failing), prefer returning
  a `Result` describing it rather than an error, so one bad item does not sink
  the whole widget.

### Why `RefreshInterval` exists

The dashboard auto-refreshes on a global tick, but it honors **each plugin's
declared cadence as a per-widget floor**. A widget is only re-run automatically
once at least its `RefreshInterval` has elapsed since its last run. This means:

- Cheap, volatile sources (an HTTP health check, ~30s) can be polled frequently.
- Expensive or slow-moving sources (releases or star history, ~daily) are *not*
  hammered on every dashboard tick, which keeps you within external API rate
  limits and avoids needless work.
- A user can always **force an immediate refresh** of a single widget from its
  refresh button, regardless of the interval.

`RefreshInterval` is the **default** cadence: when a user adds a tracker the
interval field is prefilled with this value, and they can override it per
tracker. The dashboard arms one timer per widget at its effective interval.

### Logging from a plugin

Built-in plugins get a logger from the run context — use it for debug output
that lands in the dashboard Logs tab:

```go
plugin.LoggerFrom(ctx).Debug("fetching releases", "repo", repo, "count", n)
```

The shared GitHub client and the Docker registry helper already log every
request/response at debug level, so plugins built on them get query logging for
free. (External plugins log by writing to stderr — see §7.)

**Choosing a value.** Match it to how fast the underlying data actually changes
and how expensive a run is. Use the built-in plugins as a reference:

| Plugin                     | `RefreshInterval` | Why |
| -------------------------- | ----------------- | --- |
| `http-health`              | `30 * time.Second`| Volatile, and the check is cheap. |
| `github-actions-status`    | `2 * time.Minute` | CI state changes within minutes. |
| `rss-feed`                 | `15 * time.Minute`| Feeds update occasionally. |
| `github-repo-stats`        | `time.Hour`       | Counts drift slowly. |
| `github-releases`          | `24 * time.Hour`  | Releases rarely change minute-to-minute. |
| `github-release-artifacts` | `24 * time.Hour`  | A published release's assets are stable. |
| `docker-image`             | `24 * time.Hour`  | Published image tags rarely change. |
| `github-activity`          | `24 * time.Hour`  | Activity history moves slowly and the run is expensive. |

The value is also surfaced over the API as `refresh_interval_seconds` (see the
README's REST API section).

The `Result` you return:

```go
type Result struct {
	Visualization Visualization `json:"visualization"`
	Title         string        `json:"title,omitempty"`
	Data          any           `json:"data"`
}
```

`Data` must be JSON-serializable and match the shape expected by the chosen
`Visualization`.

## 2. Visualization types and their data shapes

There are five visualizations (`internal/plugin/plugin.go`). Each expects a
specific `Data` shape. The shapes below are taken from the `plugin.go` doc
comments and from how the existing plugins build them.

### `VizList` (`"list"`)

`Data` is `{"items": [ ... ]}`, where each item has `title`, `subtitle`, `url`,
`timestamp`, and an optional `badge`. From `githubreleases`:

```go
type listItem struct {
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	URL       string `json:"url"`
	Timestamp string `json:"timestamp"`
	Badge     string `json:"badge,omitempty"`
}

return plugin.Result{
	Visualization: plugin.VizList,
	Title:         "...",
	Data:          map[string]any{"items": items},
}, nil
```

### `VizTable` (`"table"`)

`Data` is `{"columns": [...], "rows": [[...], ...]}`. From `githubrepostats`:

```go
rows := [][]any{
	{"Stars", out.StargazersCount},
	{"Forks", out.ForksCount},
}

return plugin.Result{
	Visualization: plugin.VizTable,
	Title:         "...",
	Data: map[string]any{
		"columns": []string{"Metric", "Value"},
		"rows":    rows,
	},
}, nil
```

### `VizChecklist` (`"checklist"`)

`Data` is `{"items": [{label, ok, detail}, ...]}` rendered with pass/fail marks.
Each item may also carry an **optional `url`** that the UI links to (use the
`json:"url,omitempty"` tag). From `githubactions`:

```go
type checkItem struct {
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	URL    string `json:"url,omitempty"` // optional: links the item
}

return plugin.Result{
	Visualization: plugin.VizChecklist,
	Title:         "...",
	Data: map[string]any{
		"items":  items,
		"all_ok": allOK, // extra fields the UI may use are fine
	},
}, nil
```

### `VizStat` (`"stat"`)

`Data` is `{"value", "label", "status"}` rendered as one big stat. From
`httphealth`:

```go
plugin.Result{
	Visualization: plugin.VizStat,
	Data: map[string]any{
		"value":  value,  // e.g. "UP", "DOWN", "503"
		"label":  label,  // a descriptive line
		"status": status, // "ok" | "warn" | "error"
	},
}
```

### `VizTimeseries` (`"timeseries"`)

A line chart of a value over time. `Data` is
`{label, unit, total, points: [{t, v}]}` where `points` must be in **ascending
time order** and `v` is **cumulative**. `t` is an RFC3339 timestamp or a
`YYYY-MM-DD` date string; `v` is a number; `total` is the final/grand total and
`unit` is an optional display unit (`""` if none). This is the canonical "value
over time" widget, computed just-in-time from timestamped source data. From
`githubstars`:

```go
type point struct {
	T string  `json:"t"` // RFC3339 timestamp or YYYY-MM-DD date
	V float64 `json:"v"` // cumulative value at t
}

type timeseriesData struct {
	Label  string  `json:"label"`  // series label, e.g. "Stars"
	Unit   string  `json:"unit"`   // optional display unit, "" if none
	Total  float64 `json:"total"`  // grand total at the latest point
	Points []point `json:"points"` // ascending, cumulative
}

return plugin.Result{
	Visualization: plugin.VizTimeseries,
	Title:         "...",
	Data: timeseriesData{
		Label:  "Stars",
		Unit:   "",
		Total:  float64(total),
		Points: points, // ascending by t, cumulative v
	},
}, nil
```

`githubstars` builds this series live: it sorts every star's `starred_at`,
buckets them by day, and emits one cumulative point per day that gained stars —
downsampling to roughly evenly-spaced points (keeping the first and last) when
there are more days than the chart needs.

## 3. Config schema and accessors

Each plugin advertises the fields it accepts via `ConfigSchema()`. The server
serializes these so the UI can render a form without knowing plugin internals.

```go
type ConfigField struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Placeholder string    `json:"placeholder,omitempty"`
	Help        string    `json:"help,omitempty"`
	Default     any       `json:"default,omitempty"`
}
```

### Field types

| `FieldType`   | Value      | UI / parsing                                              |
| ------------- | ---------- | --------------------------------------------------------- |
| `FieldString` | `"string"` | A text input.                                             |
| `FieldNumber` | `"number"` | A number input (arrives as JSON `float64`).               |
| `FieldBool`   | `"bool"`   | A checkbox.                                               |
| `FieldList`   | `"list"`   | A textarea; comma- or newline-separated, parsed to `[]string`. |
| `FieldSelect` | `"select"` | A dropdown; choices given in `ConfigField.Options` (`[]SelectOption{Value,Label}`). |

### Reading config values

In `Run` you receive a `plugin.Config` (a `map[string]any`). Use the typed
helpers rather than indexing the map directly — they coerce JSON's loose types:

```go
func (c Config) String(key string) string  // "" if missing/not a string
func (c Config) Int(key string) int         // coerces float64/int, else 0
func (c Config) Bool(key string) bool        // false if missing
func (c Config) List(key string) []string    // []string, []any, or a split string
```

`List` accepts a JSON array (`[]string` / `[]any`) or a single string, which it
splits on commas and newlines (trimming whitespace, dropping empties).

Because numbers arrive as `float64` and defaults are advisory (the UI may or may
not send them), always re-apply your own defaults in `Run`:

```go
count := cfg.Int("count")
if count <= 0 {
	count = 5
}
```

## 4. Worked example: a minimal complete plugin

The snippet below is a complete, registrable plugin implementing **every**
interface method. It returns a single stat. Use it as a skeleton; for a real
data source see `internal/plugins/githubreleases/githubreleases.go` (a `list`),
`internal/plugins/githubactions/githubactions.go` (a `checklist`), or
`internal/plugins/githubstars/githubstars.go` (a `timeseries`).

```go
// Package hello is a minimal example plugdash plugin.
package hello

import (
	"context"
	"time"

	"plugdash/internal/plugin"
)

type Plugin struct{}

// New returns a ready-to-use plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string          { return "hello" }
func (p *Plugin) Name() string        { return "Hello" }
func (p *Plugin) Description() string { return "A minimal example plugin." }

// RefreshInterval: this does no external work, so a minute is plenty.
func (p *Plugin) RefreshInterval() time.Duration { return time.Minute }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "who",
			Label:       "Who",
			Type:        plugin.FieldString,
			Required:    false,
			Placeholder: "world",
			Default:     "world",
		},
	}
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	who := cfg.String("who")
	if who == "" {
		who = "world"
	}
	return plugin.Result{
		Visualization: plugin.VizStat,
		Title:         "Hello",
		Data: map[string]any{
			"value":  "hello, " + who,
			"label":  "a friendly greeting",
			"status": "ok",
		},
	}, nil
}
```

## 5. Register the plugin

Add your plugin to the registry in `cmd/plugdash/main.go`:

```go
import (
	// ...
	"plugdash/internal/plugins/yourpkg"
)

reg := plugin.NewRegistry()
reg.Register(githubreleases.New())
reg.Register(githubartifacts.New())
reg.Register(yourpkg.New()) // <- your plugin
```

`Register` panics on a duplicate `ID()`, so make sure your `ID()` is unique.

If your plugin talks to GitHub, reuse the shared helper (client,
`Release`/`Asset` types, `NormalizeRepo`, the `NewGHClient(token)` constructor
that falls back to `GITHUB_TOKEN`, and the `GHBaseURL` package var) in
`internal/plugins/github.go` rather than rolling a new HTTP client.

## 6. Testing

Place a `*_test.go` alongside your plugin (see the existing
`githubreleases_test.go` and `githubartifacts_test.go`). The shared GitHub helper
exposes `plugins.GHBaseURL` as a package var so tests can point it at a local
stub server. Run the suite with:

```sh
go test ./...
```

## 7. External plugins (any language)

You don't have to write a plugin in Go or recompile plugdash. An **external
plugin** is a standalone executable in any language that plugdash discovers at
runtime and runs on demand. Internally it is wrapped to satisfy the same
`plugin.Plugin` interface, so it behaves identically everywhere (dashboard,
auto-refresh cadence, force-refresh, drag-and-drop, Configure form).

### Discovery

At startup plugdash scans a plugins directory for executable files named
`plugdash-plugin-*` and runs `describe` on each. The directory is resolved from,
in order:

1. the `-plugins-dir` flag,
2. the `PLUGDASH_PLUGINS_DIR` environment variable,
3. the default `~/.config/plugdash/plugins`.

A plugin whose `describe` fails, times out (5s), or returns invalid JSON is
skipped with a logged warning — one bad binary never breaks startup. An `id`
that collides with a built-in plugin is skipped (built-ins win). `POST
/api/plugins/rescan` (or the **Rescan plugins** button in Settings) re-scans the
directory to pick up added/removed/changed plugins without a restart.

### Protocol

The executable must handle two subcommands, exchanging JSON over stdio:

`<plugin> describe` → prints metadata JSON to **stdout**, exit 0:

```json
{
  "id": "file-version",
  "name": "File Value Watcher",
  "description": "…",
  "refresh_interval_seconds": 3600,
  "schema": [
    {"key": "repo", "label": "Repository", "type": "string", "required": true,
     "placeholder": "owner/repo", "help": "…"}
  ]
}
```

`<plugin> run` → reads the tracker **config JSON object on stdin**, prints a
**Result JSON** to stdout, exit 0:

```json
{"visualization": "stat", "title": "…", "data": {"value": "1.26.0", "label": "…", "status": "ok"}}
```

- The `schema`, `refresh_interval_seconds`, and Result/visualization shapes are
  exactly the same as for built-in plugins (see sections above).
- **Errors:** exit non-zero; whatever you write to **stderr** becomes the error
  shown on the widget. The run is bounded by the dashboard's per-run timeout
  (30s); a plugin that overruns is killed.
- **Logging:** stderr is also your log channel. On a successful run, any stderr
  output is captured and shown in the dashboard Logs tab (at debug level). When
  debug is on, the dashboard sets `PLUGDASH_DEBUG=1` in the plugin's environment,
  so you can be more verbose when it is set.
- The process inherits plugdash's environment, so secrets like `GITHUB_TOKEN`
  are available if you read them.
- A plugin with `refresh_interval_seconds <= 0` defaults to a 1-hour floor.

### Example

A minimal, dependency-free example lives at
`examples/plugins/plugdash-plugin-example` (Python, stdlib-only) — it reports the
current UTC time as a `stat`, showing the `describe`/`run` protocol end to end.
Copy it into your plugins directory, mark it executable, and rescan.

The "track a variable's value in a file on a branch" idea (e.g. the `go`
directive in a `go.mod`) ships as the **built-in** `file-version` plugin, so it
works in every deployment without an interpreter — see its source at
`internal/plugins/fileversion/` for a real-world worked example.

### Security

Discovering and running external plugins is **arbitrary code execution** by
design (like `git`/`kubectl` plugins). The plugins directory is a trust
boundary: only place executables there that you trust. plugdash does not sandbox
plugins.
