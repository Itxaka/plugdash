# plugdash Frontend

## Overview

The plugdash frontend is a **dependency-free, vanilla-JavaScript single-page
application**. There is no framework (no React/Vue/etc.), no bundler, and **no
build step** — the code that ships is exactly the code that runs in the browser.

It consists of three files under `web/assets/`:

- `index.html` — the page shell (topbar, nav, an empty `<main>`).
- `app.js` — the entire application (`"use strict"`, ~1700 lines).
- `style.css` — all styling.

The assets are embedded into the Go binary via `go:embed` and served from the
root of the embedded filesystem. From `web/web.go`:

```go
//go:embed assets
var embedded embed.FS

// FS returns the frontend asset filesystem rooted so that index.html is at "/".
func FS() fs.FS {
    sub, _ := fs.Sub(embedded, "assets")
    return sub
}
```

So `index.html` is served at `/`, `/app.js` and `/style.css` resolve from the
same embedded tree. `index.html` references them with absolute paths
(`<link rel="stylesheet" href="/style.css">`, `<script src="/app.js">`).

Because everything is embedded and self-contained, the app **works fully
offline**: there are no CDN script/style references and no remote web fonts —
all styling is local CSS, and all glyphs/icons are Unicode characters (emoji and
symbols like `↻`, `✎`, `⠿`). The only network traffic the SPA generates is to
the dashboard's own `/api/*` endpoints (plus any outbound calls those endpoints
make server-side, and links the user explicitly opens).

## DOM helpers and the API wrapper

### `el(tag, attrs, children)`

A tiny hyperscript-style element factory used everywhere instead of HTML
templating. It creates a `tag`, then applies `attrs`:

- `class` → `node.className`
- `text` → `node.textContent`
- `html` → `node.innerHTML`
- a key starting with `on` whose value is a function → an event listener
  (`onClick`/`onclick` both work; the handler is registered for
  `k.slice(2).toLowerCase()`)
- a value of `true` → a boolean attribute (`setAttribute(k, "")`)
- `null`/`false` values are skipped
- anything else → `setAttribute(k, v)`

`children` may be a single node/string or an array; strings become text nodes,
`null`/`false` entries are skipped. This null-skipping is what allows the
conditional `cond ? el(...) : null` pattern used throughout.

### `clear(node)`

Removes all children of a node (`while (node.firstChild) ...`). Used to reset
`<main>` on view changes and to re-fill card bodies and panels.

### `api(path, opts)` and the `API` object

`api()` is a thin `fetch` wrapper that:

- throws an `Error` on non-OK responses, with a message of
  `"<status> <statusText> — <body text>"`;
- returns `null` for `204 No Content`;
- parses JSON when the `content-type` is `application/json`, otherwise returns
  text.

`API` is the typed surface of named methods over the backend REST endpoints:

| Method | HTTP / endpoint |
| --- | --- |
| `API.plugins()` | `GET /api/plugins` |
| `API.trackers()` | `GET /api/trackers` |
| `API.createTracker(body)` | `POST /api/trackers` (JSON) |
| `API.updateTracker(id, body)` | `PUT /api/trackers/:id` (JSON) |
| `API.deleteTracker(id)` | `DELETE /api/trackers/:id` |
| `API.runTracker(id)` | `GET /api/trackers/:id/run` |
| `API.rescanPlugins()` | `POST /api/plugins/rescan` |
| `API.getLogs()` | `GET /api/logs` |
| `API.clearLogs()` | `DELETE /api/logs` |
| `API.getSettings()` | `GET /api/settings` |
| `API.saveSettings(body)` | `PUT /api/settings` (JSON) |

IDs are passed through `encodeURIComponent`.

## Views and routing

There are **four views**, declared in `VIEWS`:

```js
const VIEWS = ["dashboard", "configure", "settings", "logs"];
```

- **dashboard** — the widget grid (`renderDashboard`).
- **configure** — labelled **"Trackers"** in the nav; add/edit/remove widgets
  (`renderConfigure`).
- **settings** — dashboard preferences (`renderSettings`).
- **logs** — the run log (`renderLogs`).

### `setView(view)`

The single entry point for switching views. It:

1. Falls back to `"dashboard"` for any unknown view.
2. Updates `currentView`.
3. Syncs the URL hash (`location.hash = view`) so views are **deep-linkable**.
4. Sets `main.className = "main view-" + view` (see below).
5. Tears down timers via `clearAutoRefresh()` and `clearLogsTimer()`.
6. Toggles the `.active` class on the matching nav button.
7. Dispatches to the view's render function.

### Hash routing

Routing is hash-based (`#dashboard`, `#configure`, `#settings`, `#logs`):

- The nav bar (`#nav`) uses event delegation: a click on a `.nav-btn` reads its
  `data-view` and calls `setView`.
- A `hashchange` listener handles back/forward navigation and pasted deep links,
  calling `setView` when the hash names a known, different view.
- On load, the app honors a deep-linked hash: `setView(location.hash.slice(1)
  || "dashboard")`.

### Per-view `<main>` class

`setView` sets `main.className` to `view-<name>`, and CSS uses that to size the
layout. The dashboard is allowed to be wide so it can fit many widget columns;
the form-centric views are constrained and centered:

```css
.main { max-width: 2200px; margin: 0 auto; padding: 28px 28px 64px; }
.main.view-configure,
.main.view-settings { max-width: 1040px; }
```

## Dashboard rendering

`renderDashboard` builds a slim right-aligned toolbar (`.dash-toolbar`)
containing the Auto-refresh toggle, the edit-mode toggle (`✎`), and a
"refresh all now" button (`↻`), then a `body` container that holds the card
grid (`.grid`, an auto-fill CSS grid of `minmax(300px, 1fr)` columns).

### Card shell — `buildCardShell(tracker)`

Each widget is a `.card` with `draggable="true"` and `data-trackerId`,
structured as head / body / footer:

- **Icon badge** — `iconFor(tracker.plugin_id)` maps a plugin id to a glyph and
  accent color (e.g. `github-releases → {🏷️, #a371f7}`, `http-health → {🌐,
  #39c5cf}`, with a `🧩 / #9aa4b1` fallback for unknown plugins). The badge's
  color, background tint, and inset ring are derived from that color.
- **Title** — the user's tracker name (`tracker.name`, falling back to
  `"Tracker"`).
- **Subtitle** — set later from the plugin result's own title (see `fillCard`).
- **Drag handle** — the `⠿` glyph (`.card-handle`, "Drag to reorder").
- **Edit-mode actions** — an edit (`✎`) and delete (`✕`) button
  (`.card-actions`), hidden unless the dashboard is in edit mode.
- **Footer** (`.card-foot`) — refresh **cadence** ("updates every 2m"),
  **updated** timestamp ("updated 3m ago"), and a per-card force-refresh button
  (`↻`).

The card also exposes its per-type accent color to CSS:
`root.style.setProperty("--type", ic.c)` — used for the head tint, hover ring,
and border (see "status strip" below).

### Visualization dispatch — `renderViz(visualization, data)`

The body content depends on the result's `visualization` string. `renderViz`
switches over it:

| `visualization` | renderer |
| --- | --- |
| `"list"` | `renderList` — titled items, optional URL link, subtitle, timestamp pill/badge |
| `"checklist"` | `renderChecklist` — pass/fail header + per-item `✓/✗`, optional collapsible job links |
| `"table"` | `renderTable` — `columns` header + `rows` |
| `"stat"` | `renderStat` — big value + label, colored by `status` (`ok`/`warn`/`error`) |
| `"timeseries"` | `renderTimeseries` — inline SVG sparkline (area + line), total + axis labels |
| _anything else_ | `renderRaw` — pretty-printed JSON in a `<pre>` |

### Uniform-height tiles

Cards are a fixed `height: 300px` with `overflow: hidden`; tall content scrolls
inside the `.card-body` rather than letting one card tower over its neighbors,
keeping a multi-row grid tidy.

`fillCard(card, tracker, res, intervalSec, at)` fills a card from a successful
run: it clears `is-loading`, updates the footer, applies the status strip, sets
the title (always the tracker name) and the **subtitle** (the plugin result's
`title`, shown only when it differs from the tracker name), then renders the
visualization into the body. `fillCardError(...)` renders a `.card-error` row and
forces a `fail` status strip.

## Per-widget refresh model

Each widget refreshes on **its own cadence**, not a single global tick.

- **`cardStates`** — one `state` per card: `{ tracker, card, intervalSec,
  lastRun, running }`.
- **Effective interval** — `intervalSec` comes from the run result's
  `refresh_interval_seconds` (which reflects the tracker's per-tracker override
  or the plugin's declared default). It is updated whenever a run returns a
  positive value.
- **`refreshCard(state, force)`** — re-runs a single widget via
  `API.runTracker`. When `force` is false it is **skipped** if the widget's
  interval has not elapsed since `lastRun`. On success it calls `fillCard` and
  `saveWidgetCache`; on error, `fillCardError`. It always records `lastRun` and
  resets `running`.
- **In-flight guard** — `if (state.running) return;` at the top of `refreshCard`
  prevents a timer tick from overlapping a run already in progress.
- **`armAutoRefresh()`** — when the master toggle is on, arms one
  `setInterval` per card at `intervalSec * 1000`, each calling
  `refreshCard(state, true)`. Cheap/volatile widgets tick often; slow/expensive
  ones rarely. `autoRefreshTimers` holds the handles; `clearAutoRefresh()`
  clears them all (also called on every view switch).
- **Master Auto-refresh toggle** — a switch in the toolbar bound to
  `settingsCache.autorefresh_enabled`. Toggling it re-arms timers and persists
  the setting via `API.saveSettings`, reverting (and re-arming) on failure.
- **Force-refresh** — the toolbar `↻` calls `load(true)`, forcing a fresh run of
  every card; each card's footer `↻` calls `refreshCard(state, true)` for just
  that widget.

## localStorage widget cache

To avoid re-querying external APIs (e.g. GitHub) on every page reload — which
would hammer rate limits, especially for slow daily-cadence widgets — each
widget's last result is cached in `localStorage`.

- **`widgetSig(tracker)`** — a JSON signature of
  `[plugin_id, config, refresh_interval_seconds]`. Editing a tracker's config or
  interval changes the signature, which **invalidates the cache automatically**.
- **Key** — `widgetCacheKey(id)` = `"plugdash:w:" + id`.
- **`loadWidgetCache(tracker)`** — returns the stored `{ sig, res, at }` entry
  only if its `sig` matches the current `widgetSig`; otherwise `null`. Wrapped in
  try/catch so corrupt/unavailable storage is treated as a miss.
- **`saveWidgetCache(tracker, res)`** — stores `{ sig, res, at: Date.now() }`;
  best-effort (silently ignores quota/availability errors).
- **`clearWidgetCache(id)`** — removes the entry; called when a widget is
  deleted from the dashboard.

### `hydrateOrRefresh(state)`

The load policy that spares external APIs on reloads:

```js
const cached = loadWidgetCache(state.tracker);
if (cached && cached.res) {
  // render immediately from cache...
  const age = Date.now() - (cached.at || 0);
  if (state.intervalSec > 0 && age < state.intervalSec * 1000) {
    return; // still fresh — no network call
  }
}
return refreshCard(state, true); // stale or absent — fetch
```

So a normal load (page open / F5 / view switch) renders from cache while it is
**younger than the widget's interval**, and only fetches stale/absent widgets. A
manual "refresh all" instead forces `refreshCard(s, true)` for every card,
bypassing the cache.

## Edit mode

The toolbar `✎` button toggles the `editing` class on `<main>`:

```js
const on = main.classList.toggle("editing");
editToggle.classList.toggle("active", on);
```

CSS reveals the per-card `.card-actions` (edit/delete) only while `.main.editing`
is set. The per-card **edit** button stashes the tracker id in `pendingEditId`
and switches to the Trackers view (`setView("configure")`); `renderConfigure`
reads `pendingEditId`, opens that tracker in the edit form, scrolls it into
view, and clears the flag. The per-card **delete** button confirms, calls
`API.deleteTracker`, clears the widget cache, and reloads the grid.

## Drag-and-drop reordering

Cards are reorderable by dragging the handle:

- **`wireCardDrag(root, grid)`** — on `dragstart` adds `.dragging` and sets a
  `text/plain` payload (in a try/catch, since some browsers require one); on
  `dragend` removes `.dragging` and calls `persistOrder(grid)`.
- The grid's `dragover` handler computes the drop position with
  **`getDragAfterElement(grid, x, y)`**, which scans the non-dragging cards and
  finds the nearest one the cursor is above (or to the left of, within the same
  row) — so it works across a wrapping, multi-column grid — then re-inserts the
  dragging card there live.
- **`persistOrder(grid)`** snapshots the DOM order of card `data-trackerId`s into
  `settingsCache.dashboard_order` and saves it via `API.saveSettings`, keeping
  the local order even if the save fails.
- On load, **`orderTrackers(trackers, order)`** sorts trackers by the saved
  order; trackers not present in the saved order keep their natural creation
  order and follow the ordered ones (relying on stable sort).

## Configure / Trackers view

`renderConfigure` shows a two-panel layout (`.config-layout`): a **list** of
existing trackers and a **form** panel. Plugins and trackers are loaded together
(`Promise.all([API.plugins(), API.trackers()])`); plugins are cached in
`pluginsCache`.

The list (`refreshList`) renders one `.tracker-row` per tracker (icon + name +
plugin name) with **Edit** and **Delete** buttons. Edit opens the tracker in the
form; Delete calls `API.deleteTracker` then refreshes the list (`afterChange`).

### Schema-driven form — `buildForm` / `renderSchemaForm`

`buildForm` renders the **plugin picker** — a `<select>` whose options show each
plugin's `iconFor` glyph, name, and an "· external" suffix for external plugins.
Selecting a plugin shows its description and renders the dynamic form.

`renderSchemaForm` builds fields from the plugin's `schema` array. Each field has
a `key`, `label`, `type`, `required`, optional `help`/`placeholder`/`default`,
and the form always starts with a required **Tracker name** field. Supported
field types:

- **`string`** (and any unknown type) → text `<input>`
- **`number`** → numeric `<input>`; emitted as a `Number`, omitted when blank
- **`bool`** → checkbox; emitted as a boolean
- **`list`** → `<textarea>` ("one item per line"); submitted as the raw string
  (the backend splits it). Prefilled by joining an array with newlines.
- **`select`** → `<select>` built from `field.options` (`{value, label}`)

After the schema fields, a divider and a **per-tracker Refresh interval
(seconds)** number field is added, prefilled with the plugin's default cadence
(or the existing tracker's override when editing) and explained in help text.

**Edit vs add** — `buildForm(panel, plugins, onDone, editing)` switches on
whether `editing` is a tracker:

- _Add_: title "Add a tracker", submit "Add tracker" → `API.createTracker`,
  shows a success message and resets the form.
- _Edit_: title "Edit tracker", the plugin select is **preselected and disabled**
  (plugin type is immutable), all fields are prefilled from `editing.config`, a
  **Cancel** button appears, and submit "Save changes" → `API.updateTracker`.

On submit the handler validates required fields, builds the `config` object per
type, normalizes the refresh interval (`< 1` becomes `0`), and POSTs/PUTs
`{ plugin_id, name, config, refresh_interval_seconds }`.

## Settings and Logs views

### Settings (`renderSettings`)

An **Auto-refresh** panel with:

- **Enable auto-refresh** checkbox — the master switch (mirrors the dashboard
  toggle).
- **Debug logging** checkbox — logs every run, query and plugin output (viewable
  in Logs).
- **GitHub token** password field — used by GitHub widgets to raise the API
  rate limit; help text notes a read-only fine-grained token is enough.

A single **Save settings** button PUTs `{ autorefresh_enabled, debug,
github_token, dashboard_order }` and updates `settingsCache`.

Below it, `buildPluginsPanel()` renders an **External plugins** panel with a
**Rescan plugins** button (`API.rescanPlugins`), which reports
`+added / -removed` and nulls `pluginsCache` so Configure reloads the plugin
list. (External plugins are executables named `plugdash-plugin-*` in the plugins
directory.)

### Logs (`renderLogs`)

A header (with **Clear** and **Refresh** buttons and a status line) over a panel
listing log entries. `load()` fetches `API.getLogs()`, normalizes the response
(an array, or `{ entries, debug }`), shows whether debug logging is on plus the
entry count, and renders entries **newest first** (`entries.slice().reverse()`).
Each entry (`renderLogEntry`) shows time, level, message, and flattened `attrs`.
The view **polls every 3 seconds** so entries stream in as trackers run:

```js
logsTimer = setInterval(load, 3000);
```

The timer is cleared by `clearLogsTimer()` on any view switch.

## Card status strip and per-type color

Two distinct accent mechanisms color each card:

- **Per-type color (`--type`)** — set in `buildCardShell` from `iconFor`'s color.
  CSS uses `var(--type, ...)` for the card head tint, the hover ring/border, and
  related accents, giving each widget type a consistent identity.
- **Status strip** — a 3px strip down the card's left edge
  (`.card::before`), recolored by health class. `cardStatus(result)` derives the
  status (`checklist` → `ok`/`fail` from `all_ok`; `stat` → `ok`/`fail`/`warn`
  from `status`; otherwise none), and **`applyCardStatus(root, status)`** toggles
  the class:

  ```js
  root.classList.remove("status-ok", "status-fail", "status-warn");
  if (status) root.classList.add("status-" + status);
  ```

  CSS then paints the strip green / red / amber:

  ```css
  .card.status-ok::before   { background: var(--green);  opacity: 1; }
  .card.status-fail::before { background: var(--red);    opacity: 1; }
  .card.status-warn::before { background: var(--yellow); opacity: 1; }
  ```

  Errors always force a `fail` strip via `fillCardError`. This lets the dashboard
  read at a glance: green = all good, red = failing, amber = warn.
