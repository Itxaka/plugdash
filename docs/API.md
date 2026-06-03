# plugdash REST API Reference

plugdash exposes a small HTTP API used by its single-page frontend. A **plugin** is a
data source (e.g. GitHub releases, star history). A **tracker** is a saved instance of a
plugin together with the configuration a user supplied for it; running a tracker produces
a widget on the dashboard.

**Server-side run engine.** When the run engine is enabled, plugdash does not run trackers
once per HTTP request. Instead the server schedules runs centrally, caches the latest
**snapshot** for each tracker, and shares it with every client. In this mode:

- `GET /api/stream` opens a Server-Sent Events (SSE) channel that replays the current
  snapshots on connect and then pushes a fresh one each time a tracker re-runs.
- `GET /api/run` and `GET /api/trackers/{id}/run` serve the cached snapshots rather than
  triggering fresh upstream calls.
- An open SSE connection (and, as a fallback, a poll of `GET /api/run`) counts as client
  **presence**. The engine only schedules work while at least one client is present and
  idles otherwise, so a dashboard nobody is watching stops hitting upstream APIs.

If the engine is not enabled, the server falls back to the per-request behavior described
inline for each endpoint, and `GET /api/stream` returns `501 Not Implemented`.

**Config-as-code.** Trackers can either be user-created in the UI (editable) or declared in
a config file and reconciled into the database on load (read-only). Each tracker object
reports this via its `source` field; see the tracker schema below.

## Conventions

- **Base URL:** `http://localhost:8080` (the host/port plugdash is bound to). All paths
  below are relative to this base.
- **Content type:** Request and response bodies are JSON. Send `Content-Type: application/json`
  on requests that carry a body. All responses are sent with `Content-Type: application/json`,
  except the static SPA route and `204 No Content` responses, which have no body.
- **Authentication:** None. plugdash is designed to run locally or on a trusted network.
  Do not expose it directly to the public internet.
- **Errors:** Error responses use the shape `{"error": "<message>"}` together with a non-2xx
  HTTP status. Per-tracker run failures are an exception: they return `200` with the failure
  captured in the `error` field of the run object (see `GET /api/run` and
  `GET /api/trackers/{id}/run`).

## Route summary

| Method | Path | Purpose |
| ------ | ---- | ------- |
| GET | `/api/plugins` | List available plugins and their config schemas |
| POST | `/api/plugins/rescan` | Re-discover external plugins from the plugin directory |
| GET | `/api/trackers` | List all configured trackers |
| POST | `/api/trackers` | Create a tracker |
| PUT | `/api/trackers/{id}` | Update a tracker's name, config, and refresh interval |
| DELETE | `/api/trackers/{id}` | Delete a tracker |
| GET | `/api/trackers/{id}/run` | Get a single tracker's cached snapshot (`?force=true` to re-run) |
| GET | `/api/run` | Get all trackers' cached snapshots as an array |
| GET | `/api/stream` | Server-Sent Events stream of tracker snapshots |
| GET | `/api/settings` | Get dashboard-wide settings |
| PUT | `/api/settings` | Save dashboard-wide settings |
| GET | `/api/logs` | Get captured log entries and debug state |
| DELETE | `/api/logs` | Clear captured log entries |
| GET | `/` | Serve the embedded single-page frontend |

---

## Plugins

### GET /api/plugins

Returns the list of registered plugins (built-in and external). Each entry includes the
plugin's config schema so the frontend can render a configuration form without knowing
anything about the plugin internals.

**Request:** No body.

**Response `200 OK`:** A JSON array of plugin objects.

```json
[
  {
    "id": "github-releases",
    "name": "GitHub Releases",
    "description": "Track the latest releases of a GitHub repository.",
    "refresh_interval_seconds": 86400,
    "external": false,
    "schema": [
      {
        "key": "repo",
        "label": "Repository",
        "type": "string",
        "required": true,
        "placeholder": "owner/repo",
        "help": "GitHub repository as owner/repo or full URL."
      },
      {
        "key": "count",
        "label": "Number of releases",
        "type": "number",
        "required": false,
        "default": 5
      },
      {
        "key": "show_prereleases",
        "label": "Show prereleases",
        "type": "bool",
        "required": false,
        "default": false
      }
    ]
  }
]
```

**Plugin object fields:**

| Field | Type | Description |
| ----- | ---- | ----------- |
| `id` | string | Stable machine identifier (e.g. `github-releases`). Used as `plugin_id` when creating a tracker. |
| `name` | string | Human-friendly label. |
| `description` | string | One-line explanation of what the plugin tracks. |
| `refresh_interval_seconds` | number | Plugin's default refresh cadence in seconds. Advisory floor for automatic re-runs. |
| `external` | boolean | `true` if the plugin was loaded from the external plugin directory rather than built in. |
| `schema` | array | List of `ConfigField` objects describing the configuration inputs the plugin accepts (see below). |

**ConfigField object:**

| Field | Type | Description |
| ----- | ---- | ----------- |
| `key` | string | Config key the value is stored under (used as a key in a tracker's `config` object). |
| `label` | string | Human-friendly field label for the form. |
| `type` | string | Field type — one of `string`, `number`, `bool`, `list`, `select` (see below). |
| `required` | boolean | Whether the field must be provided. |
| `placeholder` | string | Optional input placeholder. Omitted when empty. |
| `help` | string | Optional help text. Omitted when empty. |
| `default` | any | Optional default value. Omitted when empty. |
| `options` | array | Choices for a `select` field; a list of `{ "value": string, "label": string }` objects. Omitted for other types. |

**Field `type` values:**

| `type` | Meaning | Stored config value |
| ------ | ------- | ------------------- |
| `string` | Single-line text input. | string |
| `number` | Numeric input. | number |
| `bool` | Checkbox / toggle. | boolean |
| `list` | Comma- or newline-separated list of strings (textarea in the UI). | array of strings, or a single string the server splits on commas/newlines |
| `select` | Single choice from `options`. | string (one of the option `value`s) |

---

### POST /api/plugins/rescan

Re-discovers external plugins from the plugin directory. Useful after adding or removing a
plugin binary without restarting plugdash.

**Request:** No body.

**Response `200 OK`:**

```json
{
  "added": 1,
  "removed": 0,
  "dir": "/home/user/.config/plugdash/plugins"
}
```

| Field | Type | Description |
| ----- | ---- | ----------- |
| `added` | number | Number of external plugins newly discovered. |
| `removed` | number | Number of external plugins no longer present and unregistered. |
| `dir` | string | Directory that was scanned. |

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `501 Not Implemented` | External plugins are not enabled (no rescanner configured). | `{"error": "external plugins are not enabled"}` |
| `500 Internal Server Error` | The rescan failed. | `{"error": "<message>"}` |

---

## Trackers

A tracker object has the following shape:

```json
{
  "id": 1,
  "plugin_id": "github-releases",
  "name": "Kubernetes releases",
  "config": { "repo": "kubernetes/kubernetes" },
  "refresh_interval_seconds": 0,
  "created_at": "2026-06-02T10:15:00Z",
  "source": "db"
}
```

| Field | Type | Description |
| ----- | ---- | ----------- |
| `id` | number | Auto-assigned tracker ID. |
| `plugin_id` | string | The plugin this tracker is an instance of. Immutable after creation. |
| `name` | string | Display name. |
| `config` | object | Arbitrary JSON object of config keys matching the plugin's schema. |
| `refresh_interval_seconds` | number | Per-tracker override of the plugin's default cadence. `0` means "use the plugin default". |
| `created_at` | string | RFC3339 creation timestamp. |
| `source` | string | Where the tracker came from: `"db"` for a user-created tracker (editable and deletable via the API/UI) or `"file"` for a config-managed tracker (declared in the config file, reconciled on load, and read-only — see `PUT`/`DELETE` below). |
| `config_key` | string | Stable identity of a file-managed tracker within the config file, used to reconcile it in place across reloads. Present only when `source` is `"file"`; omitted for `"db"` trackers. |

### GET /api/trackers

Lists all configured trackers, ordered by creation time (oldest first).

**Request:** No body.

**Response `200 OK`:** A JSON array of tracker objects (empty array `[]` when none exist).

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `500 Internal Server Error` | Database read failed. | `{"error": "<message>"}` |

---

### POST /api/trackers

Creates a tracker.

**Request body:**

```json
{
  "plugin_id": "github-releases",
  "name": "Kubernetes releases",
  "config": { "repo": "kubernetes/kubernetes" },
  "refresh_interval_seconds": 0
}
```

| Field | Type | Required | Description |
| ----- | ---- | -------- | ----------- |
| `plugin_id` | string | Yes | Must match a registered plugin's `id`. |
| `name` | string | No | Display name. If empty/omitted, defaults to `plugin_id`. |
| `config` | object | No | Config keys matching the plugin schema. Defaults to `{}`. |
| `refresh_interval_seconds` | number | No | Per-tracker cadence override. `0` (or omitted) means use the plugin default. Negative values are coerced to `0`. |

**Response `201 Created`:** The created tracker object (see the tracker shape above), with its
assigned `id` and `created_at`.

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `400 Bad Request` | Body is not valid JSON. | `{"error": "invalid JSON body"}` |
| `400 Bad Request` | `plugin_id` is missing/blank. | `{"error": "plugin_id is required"}` |
| `400 Bad Request` | `plugin_id` does not match a registered plugin. | `{"error": "unknown plugin: <id>"}` |
| `500 Internal Server Error` | Database write failed. | `{"error": "<message>"}` |

---

### PUT /api/trackers/{id}

Updates an existing tracker's `name`, `config`, and `refresh_interval_seconds`. The
`plugin_id` is immutable and cannot be changed (changing it would invalidate the stored
config against a different schema).

Config-managed trackers (`source: "file"`) are read-only: this endpoint returns
`403 Forbidden` for them. Only `source: "db"` trackers can be updated.

**Path parameters:** `id` — integer tracker ID.

**Request body:** Same shape as create. `plugin_id` in the body is ignored. If `name` is
empty/omitted, the existing name is preserved.

```json
{
  "name": "Kubernetes releases (stable only)",
  "config": { "repo": "kubernetes/kubernetes", "show_prereleases": false },
  "refresh_interval_seconds": 1800
}
```

**Response `200 OK`:** The updated tracker object.

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `400 Bad Request` | `id` is not an integer. | `{"error": "invalid id"}` |
| `400 Bad Request` | Body is not valid JSON. | `{"error": "invalid JSON body"}` |
| `403 Forbidden` | The tracker's `source` is `"file"` (config-managed, cannot be edited from the UI). | `{"error": "tracker is managed by config and cannot be edited from the UI"}` |
| `404 Not Found` | No tracker with that `id`. | `{"error": "tracker not found"}` |
| `500 Internal Server Error` | Database write failed. | `{"error": "<message>"}` |

---

### DELETE /api/trackers/{id}

Deletes a tracker. Config-managed trackers (`source: "file"`) cannot be deleted via the API
and return `403 Forbidden`; only `source: "db"` trackers can be deleted.

**Path parameters:** `id` — integer tracker ID.

**Request:** No body.

**Response `204 No Content`:** Empty body on success.

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `400 Bad Request` | `id` is not an integer. | `{"error": "invalid id"}` |
| `403 Forbidden` | The tracker's `source` is `"file"` (config-managed, cannot be deleted from the UI). | `{"error": "tracker is managed by config and cannot be deleted from the UI"}` |
| `404 Not Found` | No tracker with that `id`. | `{"error": "tracker not found"}` |
| `500 Internal Server Error` | Database delete failed. | `{"error": "<message>"}` |

---

## Running trackers

A **run response** (also called a **snapshot** when served from the engine's cache) carries
either a plugin `result` or an `error` string for a single tracker run:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `tracker_id` | number | The tracker that was run. |
| `name` | string | Tracker display name. |
| `plugin_id` | string | The tracker's plugin. |
| `refresh_interval_seconds` | number | Effective cadence: the tracker's override if set (> 0), otherwise the plugin default. |
| `result` | object | The plugin `Result` (see below). Omitted when the run failed. |
| `error` | string | Failure message. Omitted when the run succeeded. |
| `fetched_at` | string | RFC3339 timestamp of when this snapshot was produced. Present only on cached snapshots served by the engine (`/api/stream`, and the engine-backed `/api/run` and `/api/trackers/{id}/run`); absent from per-request run responses when the engine is disabled. |

**Plugin `Result` object:**

```json
{
  "visualization": "list",
  "title": "Latest releases",
  "data": { "items": [] }
}
```

| Field | Type | Description |
| ----- | ---- | ----------- |
| `visualization` | string | Renderer the frontend should use: `list`, `table`, `checklist`, `stat`, or `timeseries`. |
| `title` | string | Optional widget title. Omitted when empty. |
| `data` | any | JSON data whose shape matches the chosen visualization (see below). |

**Visualization data shapes:**

| `visualization` | `data` shape |
| --------------- | ------------ |
| `list` | `{ "items": [{ "title", "subtitle", "url", "timestamp" }] }` |
| `table` | `{ "columns": [], "rows": [[]] }` |
| `checklist` | `{ "items": [{ "label", "ok", "detail" }] }` |
| `stat` | `{ "value", "label", "status" }` |
| `timeseries` | `{ "label", "unit", "total", "points": [{ "t": "RFC3339-or-date", "v": number }] }` (points in ascending time order) |

Each run is executed with a 30-second timeout.

### GET /api/trackers/{id}/run

Returns the cached snapshot for a single tracker.

When the engine is enabled, this serves the engine's cached snapshot rather than running the
tracker on the spot. Pass `?force=true` to force an immediate run of the tracker regardless
of presence or its refresh interval (this is what the per-widget refresh button uses); the
forced run's snapshot is then returned. When the engine is disabled, the tracker is run
per-request and the fresh result is returned (no `fetched_at` field).

**Path parameters:** `id` — integer tracker ID.

**Query parameters:** `force` — set to `true` to force an immediate run before returning the
snapshot. Only effective when the engine is enabled.

**Request:** No body.

**Response `200 OK`:** A single snapshot object.

Success example (engine-backed, note the `fetched_at`):

```json
{
  "tracker_id": 1,
  "name": "Kubernetes releases",
  "plugin_id": "github-releases",
  "refresh_interval_seconds": 900,
  "result": {
    "visualization": "list",
    "title": "Latest releases",
    "data": { "items": [{ "title": "v1.33.0", "subtitle": "kubernetes/kubernetes", "url": "https://github.com/kubernetes/kubernetes/releases/tag/v1.33.0", "timestamp": "2026-05-20T00:00:00Z" }] }
  },
  "fetched_at": "2026-06-02T10:15:00Z"
}
```

Failure example (note: still `200 OK`):

```json
{
  "tracker_id": 1,
  "name": "Kubernetes releases",
  "plugin_id": "github-releases",
  "refresh_interval_seconds": 900,
  "error": "github api: 403 rate limit exceeded",
  "fetched_at": "2026-06-02T10:15:00Z"
}
```

**Response `202 Accepted`:** The tracker exists but the engine has not produced a snapshot for
it yet (it has not run since startup). The body reports the pending state; poll again, or
watch `/api/stream`, to receive the snapshot once it is ready.

```json
{ "tracker_id": 1, "pending": true }
```

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `400 Bad Request` | `id` is not an integer. | `{"error": "invalid id"}` |
| `404 Not Found` | No tracker with that `id`. | `{"error": "tracker not found"}` |
| `500 Internal Server Error` | Database read failed (engine-disabled path). | `{"error": "<message>"}` |

---

### GET /api/run

Returns the engine's cached snapshots as a single JSON array, in tracker order.

When the engine is enabled this **serves the cache** — it does not itself trigger fresh
upstream calls. Trackers that have not produced a snapshot yet are omitted from the array, so
the result is `[]` until the first runs complete. Calling this endpoint also records a
cached-poll **presence** tick, which keeps the engine scheduling for a short window (~20s) on
behalf of clients that cannot use SSE; poll it on an interval to keep the dashboard fresh
without an open stream.

When the engine is disabled, this falls back to running every configured tracker concurrently
(capped at 8 at a time) and returning their run responses (without `fetched_at`). In both
modes results preserve tracker order and per-tracker failures are captured in each object's
`error` field rather than failing the whole request.

**Request:** No body.

**Response `200 OK`:** A JSON array of snapshot objects (empty array `[]` when nothing has run
yet, or when no trackers exist).

```json
[
  {
    "tracker_id": 1,
    "name": "Kubernetes releases",
    "plugin_id": "github-releases",
    "refresh_interval_seconds": 900,
    "result": { "visualization": "list", "title": "Latest releases", "data": { "items": [] } },
    "fetched_at": "2026-06-02T10:15:00Z"
  },
  {
    "tracker_id": 2,
    "name": "Repo stars",
    "plugin_id": "github-activity",
    "refresh_interval_seconds": 3600,
    "error": "config: repos is required",
    "fetched_at": "2026-06-02T10:15:02Z"
  }
]
```

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `500 Internal Server Error` | Listing trackers from the database failed (engine-disabled path). | `{"error": "<message>"}` |

---

### GET /api/stream

Opens a [Server-Sent Events](https://developer.mozilla.org/docs/Web/API/Server-sent_events)
stream of tracker snapshots. The frontend uses this for live updates: on connect the server
**replays the current cached snapshot for every tracker**, and thereafter it pushes a fresh
`snapshot` event each time a tracker re-runs. An open connection counts as client
**presence**, so subscribing is what drives the engine to schedule runs; the engine idles
when no stream is connected.

**Request:** No body. The response is a long-lived `text/event-stream` connection (not JSON),
so use an `EventSource` or a streaming HTTP client rather than a one-shot request.

**Response `200 OK`:** `Content-Type: text/event-stream`. The server first sends a `retry`
directive, then a `snapshot` event per already-cached tracker, then additional `snapshot`
events as runs complete. A `: keepalive` comment line is emitted roughly every 25 seconds to
hold the connection open through idle proxies.

Each snapshot is delivered as one SSE frame:

```
event: snapshot
data: {"tracker_id":1,"name":"Kubernetes releases","plugin_id":"github-releases","refresh_interval_seconds":900,"result":{"visualization":"list","title":"Latest releases","data":{"items":[]}},"fetched_at":"2026-06-02T10:15:00Z"}

```

(The blank line terminating the frame is significant per the SSE spec.) The `data` payload is
a snapshot object with the same shape as `GET /api/run`'s array elements — `result` is present
on success and omitted on error; `error` is present on failure and omitted on success.

A keepalive comment looks like:

```
: keepalive

```

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `501 Not Implemented` | The run engine is not enabled. | `{"error": "live updates not enabled"}` |
| `500 Internal Server Error` | The underlying connection does not support streaming/flushing. | `{"error": "streaming unsupported"}` |

---

## Settings

Dashboard-wide preferences, persisted as a single JSON record.

| Field | Type | Description |
| ----- | ---- | ----------- |
| `autorefresh_enabled` | boolean | Whether the dashboard re-runs all trackers on a timer. |
| `autorefresh_interval` | number | Timer period in seconds. Clamped to the range 5–3600; `0` is treated as the default 60. |
| `dashboard_order` | array of numbers | Preferred display order of trackers by tracker ID. Trackers absent from this list are shown after the ordered ones in creation order. |
| `debug` | boolean | Enables verbose logging (each run, outbound queries, plugin stderr). |
| `github_token` | string | When set, is exported as the `GITHUB_TOKEN` environment variable so every GitHub plugin authenticates (higher API rate limits) without per-tracker config. |

### GET /api/settings

Returns the saved settings, or defaults if none have been saved yet
(`autorefresh_enabled: false`, `autorefresh_interval: 60`).

**Request:** No body.

**Response `200 OK`:**

```json
{
  "autorefresh_enabled": true,
  "autorefresh_interval": 120,
  "dashboard_order": [2, 1],
  "debug": false,
  "github_token": "ghp_xxx"
}
```

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `500 Internal Server Error` | Database read failed. | `{"error": "<message>"}` |

---

### PUT /api/settings

Saves the settings and returns the normalized stored values. `autorefresh_interval` is
clamped to 5–3600 (0 becomes 60). Saving also applies side effects immediately: the log
level is toggled to match `debug`, and a non-empty `github_token` is exported as
`GITHUB_TOKEN`.

**Request body:** A full settings object (see fields above).

```json
{
  "autorefresh_enabled": true,
  "autorefresh_interval": 120,
  "dashboard_order": [2, 1],
  "debug": true,
  "github_token": "ghp_xxx"
}
```

**Response `200 OK`:** The normalized settings object that was stored.

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `400 Bad Request` | Body is not valid JSON. | `{"error": "invalid JSON body"}` |
| `500 Internal Server Error` | Database write failed. | `{"error": "<message>"}` |

---

## Logs

plugdash keeps recent log records in an in-memory ring buffer for display in the Logs screen.

### GET /api/logs

Returns the captured log entries plus whether debug-level logging is currently active.

**Request:** No body.

**Response `200 OK`:**

```json
{
  "debug": true,
  "entries": [
    {
      "time": "2026-06-02T10:15:00Z",
      "level": "INFO",
      "msg": "settings updated",
      "attrs": { "debug": true, "autorefresh": true, "github_token_set": true }
    }
  ]
}
```

| Field | Type | Description |
| ----- | ---- | ----------- |
| `debug` | boolean | `true` when the current log level is debug or lower. |
| `entries` | array | Captured log entries. |

**Log entry object:**

| Field | Type | Description |
| ----- | ---- | ----------- |
| `time` | string | RFC3339 timestamp of the record. |
| `level` | string | Log level (e.g. `INFO`, `DEBUG`, `ERROR`). |
| `msg` | string | Log message. |
| `attrs` | object | Optional structured attributes. Omitted when empty. |

> Note: If the log ring is not configured, this endpoint returns an empty JSON array `[]`
> instead of the object above.

---

### DELETE /api/logs

Clears the captured log entries.

**Request:** No body.

**Response `204 No Content`:** Empty body. Always succeeds (no-op if logging is not configured).

---

## Static frontend

### GET /

Serves the embedded single-page application (the `web/` assets). Any path not matched by an
`/api/...` route is served by the static file server, so client-side routes resolve to the
SPA assets. Responses are static files (HTML, JS, CSS, etc.), not JSON.

---

## Examples

List available plugins:

```bash
curl http://localhost:8080/api/plugins
```

Add a tracker:

```bash
curl -X POST http://localhost:8080/api/trackers \
  -H 'Content-Type: application/json' \
  -d '{
    "plugin_id": "github-releases",
    "name": "Kubernetes releases",
    "config": { "repo": "kubernetes/kubernetes" },
    "refresh_interval_seconds": 0
  }'
```

Get a single tracker's cached snapshot (assuming it got id 1):

```bash
curl http://localhost:8080/api/trackers/1/run
```

Force an immediate re-run of a single tracker (the per-widget refresh button):

```bash
curl 'http://localhost:8080/api/trackers/1/run?force=true'
```

Get all cached snapshots:

```bash
curl http://localhost:8080/api/run
```

Watch the live snapshot stream:

```bash
curl -N http://localhost:8080/api/stream
```

Set the GitHub token (and enable auto-refresh):

```bash
curl -X PUT http://localhost:8080/api/settings \
  -H 'Content-Type: application/json' \
  -d '{
    "autorefresh_enabled": true,
    "autorefresh_interval": 120,
    "debug": false,
    "github_token": "ghp_your_token_here"
  }'
```

Update a tracker:

```bash
curl -X PUT http://localhost:8080/api/trackers/1 \
  -H 'Content-Type: application/json' \
  -d '{ "name": "K8s releases", "config": { "repo": "kubernetes/kubernetes" }, "refresh_interval_seconds": 1800 }'
```

Delete a tracker:

```bash
curl -X DELETE http://localhost:8080/api/trackers/1
```
