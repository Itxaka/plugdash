# Configuration

plugdash is configured through a small set of command-line flags, a few
environment variables, and a handful of runtime settings persisted in its
database and editable from the Settings UI. This document describes all of them.

## Command-line flags

All flags are optional and have sane defaults. They are parsed by
`cmd/plugdash/main.go`.

| Flag           | Default        | Purpose                                                                                  |
| -------------- | -------------- | ---------------------------------------------------------------------------------------- |
| `-addr`        | `:8080`        | HTTP listen address (host:port). Use e.g. `127.0.0.1:8080` to bind only to localhost.    |
| `-db`          | `plugdash.db`  | Path to the SQLite database file. Relative paths are resolved to an absolute path at startup. The file (and its WAL sidecars) is created if it does not exist. |
| `-plugins-dir` | `""` (unset)   | Directory of external plugin executables. When unset, falls back to the environment variable and then a default location (see [Plugins directory resolution](#plugins-directory-resolution)). |
| `-debug`       | `false`        | Enable verbose debug logging (each tracker run, outbound GitHub queries, external plugin stderr). Also enabled via `PLUGDASH_DEBUG` or the Settings toggle. |

## Environment variables

| Variable               | Purpose                                                                                                   |
| ---------------------- | --------------------------------------------------------------------------------------------------------- |
| `PLUGDASH_DEBUG`        | If set to any non-empty value, enables debug logging (equivalent to `-debug`).                            |
| `PLUGDASH_PLUGINS_DIR`  | External plugins directory, used when `-plugins-dir` is not given.                                        |
| `GITHUB_TOKEN`          | GitHub personal access token used to authenticate all GitHub plugins. See [GitHub authentication](#github-authentication-and-rate-limits). |

## Plugins directory resolution

The external plugins directory is resolved in the following order of precedence
(first match wins):

1. The `-plugins-dir` flag, if non-empty.
2. The `PLUGDASH_PLUGINS_DIR` environment variable, if non-empty.
3. `~/.config/plugdash/plugins` (the per-user config directory as reported by
   the OS; on Linux this is `$XDG_CONFIG_HOME/plugdash/plugins` or
   `~/.config/plugdash/plugins`).

If none of these can be determined, no external plugins directory is used and
only the built-in plugins are available. When a directory is resolved, plugdash
scans it for executables at startup, registers each discovered executable as a
plugin alongside the built-ins, and logs how many were loaded.

See [DEPLOYMENT.md](DEPLOYMENT.md#external-plugins-in-containers) for using
external plugins inside containers.

## The SQLite database

plugdash stores all of its persistent state in a single SQLite database file
(default `plugdash.db`, overridable with `-db`).

- **Driver**: a pure-Go SQLite driver (`modernc.org/sqlite`), so no CGO is
  required and fully static builds work (`CGO_ENABLED=0`).
- **Journal mode**: opened with `PRAGMA journal_mode=WAL`, so you will see
  `-wal` and `-shm` sidecar files next to the database file. Persist the whole
  directory, not just the main `.db` file (see
  [DEPLOYMENT.md](DEPLOYMENT.md#persistence)).
- **Foreign keys**: enabled via `PRAGMA foreign_keys=ON`.

Two things are stored in the database:

1. **Trackers** — each tracker is a saved instance of a plugin plus the
   configuration a user supplied for it. The dashboard runs each tracker to
   produce a widget. A tracker records its plugin ID, name, config (JSON), an
   optional per-tracker refresh interval (`0` means "use the plugin default"),
   and a creation timestamp. The schema is migrated automatically on startup,
   including additive column migrations for databases created by older versions.
2. **Settings** — a single JSON row holding the dashboard-wide preferences
   described below.

## Runtime settings

Settings are persisted as a single JSON row and can be edited from the Settings
UI. They are read at startup and applied to logging and GitHub authentication.

| Setting                 | JSON key                | Default | Notes                                                                |
| ----------------------- | ----------------------- | ------- | -------------------------------------------------------------------- |
| Auto-refresh enabled    | `autorefresh_enabled`   | `false` | Master toggle: when on, the dashboard re-runs trackers on a timer.   |
| Auto-refresh interval   | `autorefresh_interval`  | `60`    | Timer period in seconds. Clamped to the range 5–3600; `0` becomes `60`. |
| Dashboard order         | `dashboard_order`       | `[]`    | Preferred display order of trackers by ID. Trackers not listed are shown after the ordered ones in creation order. |
| Debug                   | `debug`                 | `false` | Enables verbose logging (see [Debug logging](#debug-logging)).       |
| GitHub token            | `github_token`          | `""`    | Token applied to all GitHub plugins (see below).                     |

### Auto-refresh model

Auto-refresh has two layers:

- A **master toggle** (`autorefresh_enabled`) and a single dashboard-wide
  **interval** (`autorefresh_interval`, in seconds). When the toggle is off, the
  dashboard does not refresh on a timer.
- A **per-widget (per-tracker) cadence**: each tracker may define its own
  refresh interval that overrides the plugin's default cadence. A per-tracker
  interval of `0` means "use the plugin's default cadence". This lets individual
  widgets refresh faster or slower than the dashboard-wide default.

The dashboard-wide interval is clamped on save and on read: values below the
minimum (5 seconds) are raised to the minimum, values above the maximum
(3600 seconds) are lowered to the maximum, and a value of `0` is replaced with
the default (60 seconds).

### Debug logging

Debug logging is enabled if **any** of the following are true:

- the `-debug` flag is passed, **or**
- the `PLUGDASH_DEBUG` environment variable is set (to any non-empty value),
  **or**
- the `debug` Settings toggle is on.

When enabled, plugdash logs each tracker run, outbound GitHub queries, and
external plugin stderr. Logs are written to stderr and also kept in an in-memory
ring buffer served at `/api/logs`; the effective log level can be toggled at
runtime from the Settings UI.

### GitHub authentication and rate limits

GitHub plugins authenticate using a token resolved with the following
precedence (first match wins):

1. **Per-tracker token** — a token set in an individual tracker's
   configuration. This applies only to that tracker.
2. **Settings token / `GITHUB_TOKEN` env** — at startup, if a token is saved in
   Settings (`github_token`) and `GITHUB_TOKEN` is not already set in the
   environment, plugdash exports the Settings token as `GITHUB_TOKEN`. GitHub
   plugins without a per-tracker token fall back to `GITHUB_TOKEN`. An explicit
   `GITHUB_TOKEN` in the environment is therefore not overwritten by the
   Settings value.

If no token is available at any level, requests are made anonymously.

#### Rate-limit guidance

GitHub's REST API allows roughly **60 requests/hour for anonymous (unauthenticated)
requests**, versus **5,000 requests/hour with a token**. Without a token, busy
dashboards will hit the anonymous limit quickly.

When a GitHub request fails with a rate-limit response (HTTP `403` or `429` with
`X-RateLimit-Remaining: 0`, or a body mentioning "rate limit"), plugdash
surfaces a friendly hint:

> GitHub API rate limit exceeded — add a GitHub token in Settings to raise the limit

Adding a token in Settings (or via `GITHUB_TOKEN`) raises the limit to
5,000 requests/hour and resolves most rate-limit problems.
