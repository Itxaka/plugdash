# Visualizations

plugdash widgets are driven entirely by what a plugin returns from `Run`. A
plugin returns a `plugin.Result`:

```go
type Result struct {
    Visualization Visualization `json:"visualization"` // which renderer to use
    Title         string        `json:"title,omitempty"`
    Data          any           `json:"data"`          // shape depends on Visualization
}
```

The `Visualization` field is a string naming one of five renderers. The
frontend (`web/assets/app.js`) maps each name to a render function in
`renderViz(visualization, data)`:

| `Visualization` constant | string value | renderer        |
| ------------------------ | ------------ | --------------- |
| `VizList`                | `list`       | `renderList`    |
| `VizTable`               | `table`      | `renderTable`   |
| `VizChecklist`           | `checklist`  | `renderChecklist` |
| `VizStat`                | `stat`       | `renderStat`    |
| `VizTimeseries`          | `timeseries` | `renderTimeseries` |

The constants are defined in `internal/plugin/plugin.go`. `Data` must be
JSON-serializable and match the shape the chosen visualization expects. Any
unrecognized visualization value falls through to `renderRaw`, which
pretty-prints the data as JSON (see [Fallback](#fallback-unknown-visualizations)).

The card title shown on the dashboard is always the user's tracker name; the
plugin's own `Result.Title` (when different) is displayed as a muted subtitle.

---

## `list`

- **Constant:** `VizList`
- **String value:** `"list"`
- **Renderer:** `renderList`

A vertical list of items, each with an optional link, subtitle, relative
timestamp, and badge pill. Used by `github-releases`, `github-issues`,
`rss-feed`, etc.

### Data shape

```jsonc
{
  "items": [
    {
      "title":     "string",   // required-ish; falls back to "(untitled)" if null
      "subtitle":  "string",   // optional; rendered as muted secondary line
      "url":       "string",   // optional; if present, the title becomes a link
      "timestamp": "string",   // optional; RFC3339 or any Date-parseable string, shown relative
      "badge":     "string",   // optional; rendered as a small pill on the right
      "icon":      "string",   // optional; image URL shown as a small avatar (e.g. repo owner)
      "badges": [              // optional; multiple tone-colored pills (e.g. answered + CI)
        { "label": "string", "tone": "ok|warn|bad|neutral" }
      ],
      "collapsed": false       // optional; if true, hidden behind a "N more" expander
    }
  ]
}
```

Only `items` is read off the top-level object. If it is missing or not an
array, the widget shows "No items."

Field-by-field:

- `title` — item heading. If `null`/missing it renders as `(untitled)`.
- `subtitle` — optional muted line under the title; omitted if falsy.
- `url` — optional. When present, `title` is wrapped in an
  `<a target="_blank" rel="noopener noreferrer">`; otherwise it is plain text.
- `timestamp` — optional. Passed through `fmtTimestamp`, which renders it
  **relative**: `just now`, `Nm ago`, `Nh ago`, `Nd ago` (within a week), or an
  absolute localized date (`Mon DD, YYYY`) beyond that. Unparseable values are
  shown verbatim.
- `badge` — optional short label rendered as a `.pill`.
- `icon` — optional image URL rendered as a small left-hand avatar (used by
  `github-issues` and `github-issue-watch` for the repo owner).
- `badges` — optional array of `{label, tone}` rendered as additional pills, each
  colored by `tone` (`ok` green, `warn` amber, `bad` red, `neutral` default).
  Used by `github-issue-watch` for the answered and CI badges. Coexists with the
  single `badge` field.
- `collapsed` — optional. Rows flagged `true` are hidden behind a `▸ N more`
  toggle at the bottom of the list, keeping a long tail of low-interest rows out
  of the way (e.g. `dependency-freshness` collapses up-to-date deps). Put the
  rows that matter first and flag the rest.

### Example

```json
{
  "items": [
    {
      "title": "v2.4.0",
      "subtitle": "kairos-io/kairos",
      "url": "https://github.com/kairos-io/kairos/releases/tag/v2.4.0",
      "timestamp": "2026-05-30T10:00:00Z",
      "badge": "prerelease"
    }
  ]
}
```

### How it renders

A `.viz-list` of rows. Each row has a main column (linked or plain title, plus
optional subtitle) and, when there is a timestamp and/or badge, a meta column
showing the relative time and a pill. In the example above the title links to
the release page, the timestamp shows e.g. "3d ago", and a "prerelease" pill
appears on the right.

---

## `table`

- **Constant:** `VizTable`
- **String value:** `"table"`
- **Renderer:** `renderTable`

A simple columns-and-rows table. Used by `github-repo-stats`.

### Data shape

```jsonc
{
  "columns": ["string", ...],   // header cells
  "rows":    [ ["cell", ...], ... ]  // each row is an array of cells
}
```

- `columns` — array of header labels, rendered as `<th>`. Cells are stringified
  via `cellText` (objects are `JSON.stringify`-ed, `null` becomes `""`).
- `rows` — array of rows; each row is an array of cell values rendered as
  `<td>`. Cell values can be strings, numbers, booleans, or objects. A row that
  is not itself an array is treated as a single-cell row.

If both `columns` and `rows` are empty/missing, the widget shows "No data."
There is no requirement that row length match column count; cells are rendered
positionally as-is.

### Example

```json
{
  "columns": ["Metric", "Value"],
  "rows": [
    ["Stars", 1234],
    ["Forks", 56],
    ["Open issues", 7],
    ["Watchers", 42],
    ["Language", "Go"]
  ]
}
```

### How it renders

A `.table-wrap` containing a `.viz-table` with a header row from `columns` and
one body row per entry in `rows`. The example renders a two-column metric/value
table.

---

## `checklist`

- **Constant:** `VizChecklist`
- **String value:** `"checklist"`
- **Renderer:** `renderChecklist`

A pass/fail check list with an overall header. Used by `github-actions-status`,
which also populates the optional per-job `links`.

### Data shape

```jsonc
{
  "items": [
    {
      "label":  "string",   // the check name; links if `url` is set
      "ok":     true,        // boolean pass/fail; drives the ✓/✗ mark
      "detail": "string",   // optional; muted line under the label (e.g. "passing")
      "url":    "string",   // optional; makes the label a link
      "links": [             // optional; per-job pills behind a collapsible toggle
        { "label": "string", "url": "string", "ok": true }
      ]
    }
  ],
  "all_ok": true            // overall status; drives the header and card strip
}
```

- `items` — array of checks. Missing/non-array yields an empty list ("No
  checks.").
- `item.label` — check name. If `item.url` is set, the label becomes a link;
  otherwise plain text.
- `item.ok` — boolean. Renders a green ✓ (`.check-mark.ok`) or red ✗
  (`.check-mark.no`).
- `item.detail` — optional muted line below the label.
- `item.links` — optional array of per-job links. Only entries with a `url` are
  kept. When present they are summarized behind a collapsible toggle button
  reading e.g. `▸ 5 jobs · 2 failing` (the failing count is the number of links
  with `ok === false`). Clicking expands `.check-links` into a set of
  `.job-pill` links, each prefixed with `✓ ` (ok) or `✗ ` (not ok), colored by
  `ok`, linking to `link.url`, titled "passed:"/"failed:" + label.
- `all_ok` — overall boolean. If it is not a boolean, the renderer falls back to
  `items.every(i => i && i.ok)`.

### Example

```json
{
  "items": [
    {
      "label": "kairos-io/kairos@main",
      "ok": false,
      "detail": "2 failing",
      "url": "https://github.com/kairos-io/kairos",
      "links": [
        { "label": "build", "url": "https://github.com/.../runs/1", "ok": true },
        { "label": "e2e",   "url": "https://github.com/.../runs/2", "ok": false }
      ]
    }
  ],
  "all_ok": false
}
```

### How it renders

A header row (`.checklist-head`) showing "✓ All checks passing" (green) or
"✗ Some checks failing" (red) based on `all_ok`, followed by one row per check.
Each row shows the ✓/✗ mark, the (optionally linked) label, an optional detail
line, and — if `links` are present — a collapsible "N jobs" toggle that expands
to per-job pills. In the example the row links to the repo, shows "2 failing",
and a `▸ 2 jobs · 1 failing` toggle that expands to a green "✓ build" pill and a
red "✗ e2e" pill.

---

## `stat`

- **Constant:** `VizStat`
- **String value:** `"stat"`
- **Renderer:** `renderStat`

A single big value with an optional label and a status color. Used by
`http-health`.

### Data shape

```jsonc
{
  "value":  "string|number", // the big number/text; "—" if null/missing
  "label":  "string",        // optional caption under the value
  "status": "ok|warn|error"  // optional; one of these three, else ignored
}
```

- `value` — the headline. Rendered stringified; `null`/missing shows an em
  dash `—`.
- `label` — optional caption beneath the value.
- `status` — must be exactly `"ok"`, `"warn"`, or `"error"`. Any other value is
  ignored (no color class applied). It colors the value via the
  `.stat-value.<status>` class and also drives the card status strip (see
  below).

### Example

```json
{
  "value": "UP",
  "label": "200 in 87ms",
  "status": "ok"
}
```

### How it renders

A `.viz-stat` with a large `.stat-value` (colored by `status`) and an optional
`.stat-label`. The example renders a green "UP" with the caption "200 in 87ms".

---

## `timeseries`

- **Constant:** `VizTimeseries`
- **String value:** `"timeseries"`
- **Renderer:** `renderTimeseries`

A small SVG line/area chart of a value over time, with a headline total and a
date axis. Used by `github-activity` (cumulative stars/commits/issues/PRs
history) and `github-activity-rate`. Points must be supplied in **ascending**
time order.

### Data shape

```jsonc
{
  "label":  "string",   // optional; chart caption next to the total
  "unit":   "string",   // optional; appended in parentheses to the label
  "total":  123,         // optional; headline number; defaults to last point's v
  "points": [            // ascending time order
    { "t": "RFC3339-or-date", "v": 0 }
  ]
}
```

- `points` — array of `{t, v}`. Only points with a non-null `v` are kept.
  - `t` — a timestamp string. The renderer tries `Date.parse` on every `t`; if
    **all** parse, the x-axis uses real dates, otherwise it falls back to point
    index for x positions.
  - `v` — the numeric value (coerced with `Number(v) || 0`).
- `total` — optional headline number shown above the chart. If null/missing it
  defaults to the last point's `v` (or `0` when there are no points).
- `label` — optional caption shown next to the total.
- `unit` — optional; when present it is appended to the label as `label (unit)`.

### Example

```json
{
  "label": "Stars",
  "unit": "total",
  "total": 1850,
  "points": [
    { "t": "2026-01-01T00:00:00Z", "v": 1200 },
    { "t": "2026-03-01T00:00:00Z", "v": 1500 },
    { "t": "2026-05-01T00:00:00Z", "v": 1850 }
  ]
}
```

### How it renders

A `.viz-ts` block: a head showing the total and `label (unit)` caption, then a
300x90 `.ts-svg` SVG with a filled area (`.ts-area`) under a line (`.ts-line`),
auto-scaled to the min/max of the data, and finally a `.ts-axis` row with the
first and last x labels. When dates parse, axis labels are formatted as
`Mon 'YY`. If there are fewer than two usable points the chart is replaced by
"Not enough data to chart." (one point) or "No data yet." (none).

---

## `gauge`

A progress bar with a big percentage, for completion / utilization widgets
(e.g. `github-milestone`).

### Data shape

```jsonc
{
  "label":  "string",   // optional; shown next to the percentage
  "value":  0,          // current amount
  "max":    0,          // total; percentage = value / max (clamped 0–100)
  "unit":   "string",   // optional; appended to the "value / max" footer
  "status": "ok",       // optional: ok | warn | error → bar/percent color
  "detail": "string"    // optional; extra footer text (e.g. due date)
}
```

### How it renders

The percentage `value / max` drives both the headline number and the fill width
of a rounded bar. `status` colors the bar and percentage (green / amber / red);
without it the accent color is used. The footer shows `value / max [unit]` and
any `detail`. When `max` is 0 the bar reads 0%.

---

## Card status strip

Each dashboard card has an accent strip whose color reflects widget health,
derived by `cardStatus(result)` and applied via `applyCardStatus`:

- **green** (`status-ok`) — `checklist` with `all_ok === true`, or `stat` with
  `status === "ok"`.
- **red** (`status-fail`) — `checklist` with `all_ok` falsy, `stat` with
  `status === "error"`, or any widget that errored while running.
- **amber** (`status-warn`) — `stat` with `status === "warn"`.

Only the `checklist` and `stat` visualizations contribute a status; `list`,
`table`, and `timeseries` leave the strip uncolored (no class). A run that
fails is always marked red regardless of visualization.

---

## Fallback: unknown visualizations

If `Result.Visualization` is not one of the five known values, `renderViz`
falls through to `renderRaw`, which renders the data as pretty-printed JSON
(`JSON.stringify(data, null, 2)`) inside a `<pre class="viz-raw">`. This makes
new or experimental plugins visible even before a dedicated renderer exists.
