# plugdash documentation

**plugdash** is an org-wide, birds-eye dashboard built from small plugin
**widgets**. Each widget tracks one thing across your repositories and
services — CI status, release/artifact checks, latest releases,
activity-over-time, issues that need attention, Docker image availability,
HTTP health, RSS feeds, and more. Data is fetched **just-in-time** on each
refresh and rendered as a chosen visualization; nothing is stored except the
small set of saved widget configurations (trackers) in a single SQLite file.
The API server and web UI ship together as one self-contained Go binary with
the frontend embedded.

## 60-second Quick Start

```sh
# 1. Build
go build ./cmd/plugdash

# 2. Run (listens on :8080, creates ./plugdash.db if missing)
./plugdash
```

3. Open <http://localhost:8080> in your browser.
4. Go to the **Configure** section, **add a tracker** — pick a plugin (e.g.
   *GitHub Releases*), fill in its fields (e.g. `repo` = `kubernetes/kubernetes`),
   and save. It appears on the dashboard immediately.
5. Open **Settings** and paste a **GitHub token**. The GitHub plugins work
   unauthenticated but are limited to 60 requests/hour; a token raises that to
   5000/hour and is strongly recommended once you have more than a widget or two.

That's it. For everything else, see the docs below. Common next steps:
deployment behind a reverse proxy (`DEPLOYMENT.md`), tuning refresh and tokens
(`CONFIGURATION.md`), or writing your own plugin (`PLUGINS.md`).

## Documentation index

### For users

- [PLUGIN_CATALOG.md](PLUGIN_CATALOG.md) — every built-in plugin, its config fields, and visualization.
- [VISUALIZATIONS.md](VISUALIZATIONS.md) — the visualization types (list, table, stat, checklist, timeseries) and the data shapes they expect.
- [CONFIGURATION.md](CONFIGURATION.md) — command-line flags, environment variables, Settings, and GitHub tokens.
- [DEPLOYMENT.md](DEPLOYMENT.md) — running plugdash, Docker, and reverse-proxy setup.
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — common problems and FAQ.

### For plugin authors

- [PLUGINS.md](PLUGINS.md) — how to write a plugin, both in Go and as an external executable in any language.
- [../examples/plugins/plugdash-plugin-fileversion](../examples/plugins/plugdash-plugin-fileversion) — a complete external (Python) plugin example.

### For developers

- [ARCHITECTURE.md](ARCHITECTURE.md) — system design, components, and data flow.
- [DEVELOPMENT.md](DEVELOPMENT.md) — dev environment setup and an add-a-plugin walkthrough.
- [FRONTEND.md](FRONTEND.md) — internals of the embedded single-page web UI.
- [API.md](API.md) — the REST API reference (endpoints, request/response shapes).
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — how to contribute changes.

## Key concepts

- **Plugin** — a self-describing data source. It declares an id, name, and a
  config schema, and exposes a `Run` method that fetches data and returns a
  result plus a visualization type. Plugins are registered at startup.
- **Tracker** — a saved plugin configuration: a plugin id plus the
  user-supplied config values. Trackers are persisted in SQLite and are the
  only state plugdash keeps.
- **Widget** — a tracker as it appears on the dashboard: one card showing that
  tracker's latest rendered result. Cards are drag-and-drop reorderable and the
  order persists.
- **Visualization** — how a result is rendered. The plugin chooses a type
  (`list`, `table`, `stat`, `checklist`, `timeseries`) and returns data shaped
  to match; the UI renders it accordingly.
- **Refresh cadence** — how often a widget re-runs. Each plugin declares a
  default interval (e.g. an HTTP health check ~30s, a release tracker daily)
  that you can override per tracker. A global Auto-refresh toggle is the master
  on/off, and each widget has a force-refresh button that re-runs it now.
- **External plugin** — a plugin written in any language as an executable named
  `plugdash-plugin-*`, dropped into the plugins directory. plugdash discovers it
  at startup (or via Rescan plugins) and it behaves exactly like a built-in.
