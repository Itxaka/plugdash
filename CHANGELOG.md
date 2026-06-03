# Changelog

All notable changes to plugdash are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.0] - 2026-06-03

A big batch of new widgets, a new visualization, and per-widget sizing.

### Added

- **Nine new plugins:**
  - `github-issue-watch` — watch specific issues/PRs: answered state, time since
    last reply, and (for PRs) CI status.
  - `github-prs` — open PR review queue across repos (review state + CI + draft).
  - `github-review-requested` — open PRs awaiting your review (search API).
  - `github-stale` — open issues/PRs with no activity for > N days (search API).
  - `github-milestone` — milestone completion as a gauge.
  - `github-workflow-health` — CI success rate + run-duration trend (timeseries).
  - `dependency-freshness` — go.mod / package.json deps vs their latest releases.
  - `endoflife` — end-of-life / support countdown via endoflife.date (no auth).
  - `osv-vulns` — known vulnerabilities for a package version via OSV.dev (no auth).
- **New `gauge` visualization** — a progress bar + percentage (used by
  `github-milestone`).
- **Widget sizes.** Plugins may implement the optional `plugin.Sizer` interface
  to request a wider/taller tile (1–2 cells per axis; external plugins set
  `width`/`height` in `describe`). The dashboard honors it via CSS grid spans;
  a new **Settings → "Uniform widget sizes"** toggle forces a regular 1×1 grid.
  `github-prs`, `github-review-requested` and `dependency-freshness` are 2×1;
  `github-actions-status` is 1×2.
- **List visualization extras:** per-item `badges` (tone-colored pills), `icon`
  (owner avatars), and `collapsed` (rows tucked behind a "N more" expander).

### Changed

- `dependency-freshness` orders deps worst-first and collapses up-to-date ones;
  when everything is current it shows an "All dependencies up to date" summary
  instead of an empty list. Versions are compared numerically, so a dep that is
  *ahead* of the proxy's latest (e.g. a `vX+incompatible` pin) is shown as up to
  date rather than "major behind".
- Shared the GitHub CI-badge aggregation (`plugins.AggregateCIBadge` /
  `GHClient.CIBadge`) across the issue/PR widgets.

## [0.3.0] - 2026-06-03

This release adds bulk tracker management to the Trackers view, building on the
config-as-code reconcile from 0.2.0.

### Added

- **Clear all trackers.** `POST /api/trackers/clear` removes every tracker
  (user- and file-sourced). The on-disk config file is left untouched, so a
  reload or restart restores the file-managed ones.
- **Reload from file.** `POST /api/trackers/reload` re-reads the server's
  `--config` file and reconciles it. Idempotent and dedup-by-key: remove a file
  tracker, reload, and the full set is back with no duplicates. Returns `409` when
  the server was started without `--config`.
- **Load from file / paste.** `POST /api/trackers/import` loads trackers from an
  uploaded or pasted config document. They reconcile in as `source="file"`, so
  they are **session-only** — a restart reverts to the bundled/`--config` set.
- **Dump to config.** `GET /api/trackers/export` downloads the current trackers as
  a `--config`-style YAML. The dump contains only a `trackers:` list — never a
  `settings:` block — so the GitHub token never leaves in a dump.
- **Config status.** `GET /api/config` reports whether a `--config` file is set,
  so the UI can enable/disable the "Reload from file" action.
- A **bulk-action bar** in the Trackers view wiring up the above (Reload from
  file, Load from file…, Paste config…, Dump to config, Clear all), with a
  session-only warning on import.

### Changed

- **File-managed trackers are now deletable** from the UI and API (per-widget and
  via Clear); a reload restores them. Editing them stays blocked (`403` on `PUT`),
  since a reload would overwrite the change. Previously both edit and delete were
  blocked with `403`.
- Modernized a min-clamp in `ReconcileFileTrackers` to use the builtin `max`.

## [0.2.0] - 2026-06-03

This release moves tracker execution off the browser and onto the server, adds
declarative configuration, and makes the result cache survive restarts.

### Added

- **Server-side execution engine.** Trackers now run on the server on their own
  cadence. The latest result per tracker is cached as a `Snapshot` and shared by
  every connected client, so N viewers cost one upstream API call, not N. Still
  real-time only — the latest snapshot, never history.
- **Presence-gating.** The engine only refreshes while at least one client is
  watching (an SSE subscriber, or a `GET /api/run` poll within the last 20s) and
  goes fully idle — zero upstream calls — when nobody is looking.
- **Live updates over Server-Sent Events.** `GET /api/stream` replays the current
  snapshots on connect and pushes each re-run; the browser uses `EventSource`
  with an 8s cached-poll fallback. A **Live** toggle in the dashboard toolbar
  controls it (default on).
- **Config-as-code.** A new `--config <file.yaml>` flag declaratively defines
  trackers. File-managed trackers are reconciled into the store and shown
  read-only in the UI (a `config` badge; edit/delete disabled), while users can
  still add their own ad-hoc trackers — a hybrid model. See
  `docs/CONFIGURATION.md` and `examples/plugdash.yaml`.
- **Snapshot persistence.** The latest result per tracker is persisted to a
  `snapshots` table and restored on startup, so a restart paints the dashboard
  instantly from last-known data and — by restoring each tracker's last-run time —
  does **not** re-run trackers whose interval hasn't elapsed. No more burst of
  fresh upstream calls on every redeploy/crashloop.
- **GitHub ETag conditional requests.** A shared cache sends `If-None-Match`, so
  unchanged resources return `304 Not Modified` (which GitHub does not charge
  against the rate limit) and reuse the cached body.
- **GitHub rate-limit back-off.** On a `403`/`429` reporting an exhausted budget
  (or a `Retry-After`), further calls for that token fail fast until the reset
  instead of hammering the API.
- **First-run stagger.** Trackers that have never run are spread across a short
  window so a fleet of same-interval trackers doesn't fire in one synchronized
  burst; every widget still paints within ~10s.
- **Built-in `file-version` plugin.** Tracks the value of a variable in a file on
  a repository branch (e.g. the Go version in `go.mod`).
- **GitHub avatars and SVG type icons.** Cards show owner/repo avatars and a
  per-plugin-type SVG icon.
- A per-widget **force refresh** (`GET /api/trackers/{id}/run?force=true`) that
  runs one tracker immediately regardless of presence or interval.

### Changed

- Execution is server-driven: the client-side per-widget refresh timers and the
  `localStorage` widget cache were removed in favor of the engine + SSE.
- `GET /api/run` now serves the cached snapshots (and records presence) instead
  of triggering synchronous runs; `GET /api/trackers/{id}/run` returns the cached
  snapshot (`202` while a first run is pending).
- Auto-refresh / **Live** now defaults on, since viewing the dashboard is what
  drives (and gates) execution.
- Toolchain and dependencies: Docker image builds on Go 1.26, CI runs on Node 24,
  and `goreleaser-action` was updated to v7.

### Fixed

- The "updated X ago" card label now ages live (re-computed every 30s on the
  client, no server calls) instead of freezing on "just now" until a full page
  reload; the exact fetch time is available on hover.

[0.4.0]: https://github.com/Itxaka/plugdash/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/Itxaka/plugdash/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Itxaka/plugdash/compare/v0.1.6...v0.2.0
