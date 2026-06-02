# plugdash REST API Reference

plugdash exposes a small HTTP API used by its single-page frontend. A **plugin** is a
data source (e.g. GitHub releases, star history). A **tracker** is a saved instance of a
plugin together with the configuration a user supplied for it; running a tracker produces
a widget on the dashboard.

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
| GET | `/api/trackers/{id}/run` | Run a single tracker and return its result |
| GET | `/api/run` | Run all trackers concurrently and return their results |
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
    "description": "Latest releases for one or more repositories",
    "refresh_interval_seconds": 900,
    "external": false,
    "schema": [
      {
        "key": "repos",
        "label": "Repositories",
        "type": "list",
        "required": true,
        "placeholder": "owner/repo, owner/other",
        "help": "One or more owner/repo entries, comma or newline separated."
      },
      {
        "key": "include_prereleases",
        "label": "Include pre-releases",
        "type": "bool",
        "required": false,
        "default": false
      },
      {
        "key": "sort",
        "label": "Sort order",
        "type": "select",
        "required": false,
        "default": "newest",
        "options": [
          { "value": "newest", "label": "Newest first" },
          { "value": "oldest", "label": "Oldest first" }
        ]
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
  "config": { "repos": ["kubernetes/kubernetes"] },
  "refresh_interval_seconds": 0,
  "created_at": "2026-06-02T10:15:00Z"
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
  "config": { "repos": ["kubernetes/kubernetes"] },
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

**Path parameters:** `id` — integer tracker ID.

**Request body:** Same shape as create. `plugin_id` in the body is ignored. If `name` is
empty/omitted, the existing name is preserved.

```json
{
  "name": "Kubernetes releases (stable only)",
  "config": { "repos": ["kubernetes/kubernetes"], "include_prereleases": false },
  "refresh_interval_seconds": 1800
}
```

**Response `200 OK`:** The updated tracker object.

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `400 Bad Request` | `id` is not an integer. | `{"error": "invalid id"}` |
| `400 Bad Request` | Body is not valid JSON. | `{"error": "invalid JSON body"}` |
| `404 Not Found` | No tracker with that `id`. | `{"error": "tracker not found"}` |
| `500 Internal Server Error` | Database write failed. | `{"error": "<message>"}` |

---

### DELETE /api/trackers/{id}

Deletes a tracker.

**Path parameters:** `id` — integer tracker ID.

**Request:** No body.

**Response `204 No Content`:** Empty body on success.

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `400 Bad Request` | `id` is not an integer. | `{"error": "invalid id"}` |
| `404 Not Found` | No tracker with that `id`. | `{"error": "tracker not found"}` |
| `500 Internal Server Error` | Database delete failed. | `{"error": "<message>"}` |

---

## Running trackers

A **run response** object carries either a plugin `result` or an `error` string for a single
tracker run:

| Field | Type | Description |
| ----- | ---- | ----------- |
| `tracker_id` | number | The tracker that was run. |
| `name` | string | Tracker display name. |
| `plugin_id` | string | The tracker's plugin. |
| `refresh_interval_seconds` | number | Effective cadence: the tracker's override if set (> 0), otherwise the plugin default. |
| `result` | object | The plugin `Result` (see below). Omitted when the run failed. |
| `error` | string | Failure message. Omitted when the run succeeded. |

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

Runs a single tracker and returns its result.

**Path parameters:** `id` — integer tracker ID.

**Request:** No body.

**Response `200 OK`:** A single run response object.

Success example:

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
  }
}
```

Failure example (note: still `200 OK`):

```json
{
  "tracker_id": 1,
  "name": "Kubernetes releases",
  "plugin_id": "github-releases",
  "refresh_interval_seconds": 900,
  "error": "github api: 403 rate limit exceeded"
}
```

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `400 Bad Request` | `id` is not an integer. | `{"error": "invalid id"}` |
| `404 Not Found` | No tracker with that `id`. | `{"error": "tracker not found"}` |
| `500 Internal Server Error` | Database read failed. | `{"error": "<message>"}` |

---

### GET /api/run

Runs every configured tracker concurrently (capped at 8 at a time) and returns their run
responses in a single JSON array. Results preserve tracker order. Per-tracker failures are
captured in each object's `error` field rather than failing the whole request.

**Request:** No body.

**Response `200 OK`:** A JSON array of run response objects (one per tracker, empty array
`[]` when no trackers exist).

```json
[
  {
    "tracker_id": 1,
    "name": "Kubernetes releases",
    "plugin_id": "github-releases",
    "refresh_interval_seconds": 900,
    "result": { "visualization": "list", "title": "Latest releases", "data": { "items": [] } }
  },
  {
    "tracker_id": 2,
    "name": "Repo stars",
    "plugin_id": "github-activity",
    "refresh_interval_seconds": 3600,
    "error": "config: repos is required"
  }
]
```

**Error responses:**

| Status | When | Body |
| ------ | ---- | ---- |
| `500 Internal Server Error` | Listing trackers from the database failed. | `{"error": "<message>"}` |

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
    "config": { "repos": ["kubernetes/kubernetes"] },
    "refresh_interval_seconds": 0
  }'
```

Run a single tracker (assuming it got id 1):

```bash
curl http://localhost:8080/api/trackers/1/run
```

Run all trackers:

```bash
curl http://localhost:8080/api/run
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
  -d '{ "name": "K8s releases", "config": { "repos": ["kubernetes/kubernetes"] }, "refresh_interval_seconds": 1800 }'
```

Delete a tracker:

```bash
curl -X DELETE http://localhost:8080/api/trackers/1
```
