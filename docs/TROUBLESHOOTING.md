# plugdash Troubleshooting & FAQ

This guide covers the problems you are most likely to hit while running
plugdash, what causes each one, and how to fix it. It is written in a
problem → cause → fix style. A short [FAQ](#faq) is at the end.

If a fix mentions debug logging, see
[Reading widget errors & enabling debug logging](#a-widget-shows-an-error)
for how to turn it on and where to read the output.

---

## "GitHub API rate limit exceeded"

**Symptom.** A GitHub-backed widget (releases, issues, stars, repo stats,
Actions status, rate, artifacts) shows an error containing:

```
GitHub API rate limit exceeded — add a GitHub token in Settings to raise the limit
```

**Cause.** plugdash talks to the GitHub REST API. When no token is
configured, GitHub treats the requests as anonymous and limits you to about
**60 requests per hour** (per source IP). With several GitHub widgets
refreshing, that budget is exhausted quickly. plugdash detects the
rate-limit response (HTTP 403/429 with `X-RateLimit-Remaining: 0`) and shows
the friendly hint above instead of the raw HTTP error.

**Fix.** Give plugdash a GitHub token. An authenticated client gets a far
higher limit (5,000 requests/hour). You have three options, in order of
precedence:

1. **Settings (recommended).** Open the **Settings** panel and paste a token
   into the **GitHub token** field, then save. plugdash exports it as
   `GITHUB_TOKEN` for every GitHub plugin immediately, and persists it so it
   is applied on the next start too. (Note: an environment `GITHUB_TOKEN`
   set before startup is not overwritten by the saved setting; the saved
   token *is* applied when you save it in the UI.)
2. **Environment variable.** Start plugdash with `GITHUB_TOKEN=<token>` in
   the environment. All GitHub plugins fall back to this when no explicit
   token is configured.
3. **Per-widget token.** Some widgets (e.g. the Docker image widget) expose
   their own **GitHub token** config field, which authenticates just that
   widget. Useful when one widget needs access to a private repo that the
   others do not.

A fine-grained or classic token with read-only public-repo scope is enough
for public data; add the relevant repo scopes for private repositories.

---

## Docker image widget errors

The Docker image widget checks whether an image:tag exists in a registry and,
where possible, which architectures it advertises.

### "image must not contain spaces" / "must be a registry reference, not a URL" / "do not include a tag"

**Cause.** The **Image** field expects *just* the image reference with **no
tag, no digest, no command, and no scheme** — for example
`ghcr.io/org/repo` or `nginx`. The field is validated, and common
copy-paste mistakes are rejected with a specific message:

| What you typed | Message you get |
| --- | --- |
| `docker pull ghcr.io/org/repo` (a whole command, contains spaces) | `image "<value>" must not contain spaces (enter just the image, e.g. ghcr.io/org/repo)` |
| `https://ghcr.io/org/repo` (a URL) | `image must be a registry reference like ghcr.io/org/repo, not a URL` |
| `ghcr.io/org/repo:v1.2.3` (tag baked in) | `do not include a tag in the image; put the tag in the Tags field` |
| `ghcr.io/org/repo@sha256:...` (digest baked in) | `do not include a digest in the image` |
| empty | `image is required` |
| weird characters | `image "<value>" contains invalid characters` |

**Fix.** Put only the image in the **Image** field. Put tags (e.g. `latest`,
`v1.2.3`) in the **Tags** field, and architectures (e.g. `amd64`, `arm64`)
in the **Arches** field. The host portion (`registry:port`) is allowed in
the Image field — only the repository part may not carry a tag or digest.

### Private or nonexistent `ghcr.io` images / 401 Unauthorized

**Symptom.** A registry check fails with something like:

```
registry https://ghcr.io/v2/org/repo/manifests/latest returned 401: ...
```

**Cause.** The image is private (or the path is wrong, so the registry
refuses to confirm it exists). plugdash automatically fetches an anonymous
pull token for Docker Hub and ghcr.io and honors `WWW-Authenticate`
challenges, but anonymous tokens only grant access to *public* images. A
private image still returns **401 Unauthorized**.

**Fix.** Put a credential in the widget's **Registry token** field. This is
sent as a `Bearer` token to the registry. For ghcr.io, a GitHub personal
access token with `read:packages` works. Double-check the image path
(`ghcr.io/<owner>/<name>`) if you are sure it should be public — a typo
yields the same 401 because the registry will not reveal whether a private
name exists.

### Tag shows as present but "single-arch (unverified)"

**Symptom.** A row reads, e.g.:

```
tag latest present, single-arch (unverified)
```

**Cause.** This is **not an error** — the tag exists. The image is a *single*
image manifest rather than a multi-arch image index (manifest list), so it
advertises no per-platform entries. plugdash cannot confirm the specific
architecture you asked for, so it reports the tag as present but the arch as
unverified.

**Fix.** Nothing to fix if you only care that the tag exists. If you need
arch verification, point at a multi-arch tag (one published as an OCI image
index / Docker manifest list); for those, plugdash reports `found: <arch>`
when present, or `tag <tag> present, arch not in manifest list` when the
requested arch is genuinely absent.

---

## A widget shows an error

**How to read it.** Each widget runs its plugin just-in-time. If the plugin
returns an error, that error string is shown on the card verbatim (it is the
same string the API returns in the `error` field for that tracker). The
message usually names the cause directly — a rate limit, a 401, a bad URL,
an external-plugin failure, or a timeout (every run is capped at **30
seconds**).

**Enable debug logging** for the full picture (outbound HTTP requests,
response statuses, plugin stderr, timing). There are three ways, any one of
which turns it on:

- **Flag:** start plugdash with `-debug`.
- **Environment:** start with `PLUGDASH_DEBUG=1` (any non-empty value).
- **Settings toggle:** flip the **Debug** switch in the Settings panel. This
  is persisted and takes effect immediately, no restart needed.

**View the logs** in the **Logs** tab in the UI (backed by the `/api/logs`
endpoint, an in-memory ring buffer of the last 1,000 entries). You can clear
it from the same tab.

**Important:** debug log lines are only emitted on *new* plugin runs. Turning
debug on does not retroactively add detail to errors that already happened —
**force-refresh the widget** (its refresh button, or the global **Refresh**)
to produce a fresh, fully-logged run. Note also that the server caches one
snapshot per tracker and only re-runs on the tracker's cadence (see
[Data looks stale](#data-looks-stale--f5-doesnt-refresh)), so a plain page reload
re-paints from the cached snapshot and does not re-run the widget.

---

## External plugin not showing up

An external plugin is a standalone executable plugdash shells out to. If
yours does not appear in the plugin list, check each requirement below —
plugdash skips anything that fails one of them, and never aborts the scan
because of a bad plugin.

1. **Name.** The file must be named with the prefix `plugdash-plugin-`
   (for example `plugdash-plugin-weather`). Files without this prefix are
   ignored entirely.
2. **Executable.** It must be a regular file with the executable bit set
   (`chmod +x plugdash-plugin-weather`). Directories and non-executable
   files are skipped with a warning.
3. **Location.** It must live in the plugins directory. The directory is
   resolved in this order:
   1. the `-plugins-dir` flag,
   2. the `PLUGDASH_PLUGINS_DIR` environment variable,
   3. the default `~/.config/plugdash/plugins` (under your user config dir).

   A missing directory is not an error — it just yields no external plugins.
4. **`describe` must work.** plugdash runs `plugdash-plugin-foo describe` and
   expects it to print **valid JSON** to stdout that includes a non-empty
   **`id`** field, and to finish within **5 seconds**. If `describe` exits
   non-zero, prints invalid JSON, omits `id`, or times out, the plugin is
   skipped. (Other fields: `name` defaults to the `id` if omitted;
   `refresh_interval_seconds` ≤ 0 defaults to 1 hour; `schema` is optional.)
5. **No id collision.** If the plugin's `id` matches a **built-in** plugin's
   id, it is skipped — built-ins win and are never replaced. Choose a unique
   id.

**Pick up changes without restarting.** Use the **Rescan** action in the UI
(`POST /api/plugins/rescan`). It re-reads the directory, registers new
plugins, removes vanished ones, and re-`describe`s existing ones. If you see
`external plugins are not enabled`, no plugins directory was resolved at
startup (see step 3).

**Check the server logs (stderr).** Skips are logged there, e.g.:

```
extplugin: skipping /path/plugdash-plugin-foo: not an executable file
extplugin: skipping /path/plugdash-plugin-foo: describe failed: ...
extplugin: skipping /path/plugdash-plugin-foo: invalid describe JSON: ...
extplugin: skipping /path/plugdash-plugin-foo: id "github-releases" collides with a built-in plugin
```

These warnings tell you exactly which requirement the plugin failed.

> Runtime tip: when debug is on, plugdash sets `PLUGDASH_DEBUG=1` in the
> plugin's environment and captures everything the plugin writes to **stderr**
> as debug log lines — so an external plugin can log just by printing to
> stderr. On a failed `run`, the plugin's stderr is folded into the error
> message shown on the card; a `run` that exceeds the timeout shows
> `plugin "<id>" timed out`, and non-JSON output shows
> `plugin "<id>" produced invalid result JSON`.

---

## Data looks stale / F5 doesn't refresh

**Cause.** This is intentional. Runs happen on the **server**: the engine runs
each tracker on **its own cadence** and caches **one snapshot per tracker** that
every client shares. A page reload does not re-run anything — the dashboard just
re-paints from the current server-side snapshot (replayed over SSE on connect, or
read from `/api/run`). This is what spares the external APIs (GitHub especially)
from a burst of calls every time someone reloads: a widget with, say, a 24-hour
interval keeps showing its last snapshot until the engine re-runs it 24 hours
later.

Snapshots are also **persisted to the database** (a `snapshots` table, one row
per tracker, overwritten each run — last-known state, *not* history). So after a
**restart** the dashboard paints instantly from the last-known snapshots, and —
importantly — a restart does **not** re-trigger checks whose interval hasn't
elapsed: the engine restores each tracker's last-run time from the persisted
`fetched_at`, so a redeploy or crash-loop doesn't cause a burst of fresh upstream
calls.

**How to force a refresh:**

- **One widget:** click that widget's **refresh button** — a forced run
  (`/api/trackers/{id}/run?force=true`) that ignores the cadence and re-runs the
  tracker immediately.
- **Everything:** click the global **Refresh** button ("Refresh all now"), which
  forces every widget.

**Long-cadence widgets (daily, etc.) won't refetch on reload — by design.** A
widget that declares a 24-hour interval is only re-run by the engine once a day,
so reloading the page keeps showing the cached snapshot rather than hammering the
source on every visit. If you need fresher data, use the force-refresh button or
lower the tracker's refresh interval. Because runs are presence-gated, the engine
only schedules while someone is watching (see
[Widgets never load](#widgets-never-load--stay-on-the-loading-state)).

---

## "plugin not found: &lt;id&gt;"

**Symptom.** A widget shows:

```
plugin not found: <some-id>
```

**Cause.** The tracker references a plugin id that the registry no longer
knows about. This happens when:

- an **external plugin was removed or renamed** (its id is gone after a
  rescan/restart), or
- a plugin's **id changed** (e.g. you edited an external plugin's
  `describe` output to emit a different `id`), leaving old trackers pointing
  at the previous id.

**Fix.** Restore the plugin under its original id, or recreate/repoint the
affected tracker to a plugin that currently exists. (Tracker config is keyed
by `plugin_id`; plugdash validates the id when you *create* a tracker, but a
plugin can disappear afterwards, which is when this message appears at run
time.) After restoring an external plugin, use **Rescan** so the registry
picks it up again.

---

## CI status shows "no checks"

**Symptom.** A GitHub Actions / CI status widget lists a repo with detail
`no checks`.

**Cause.** plugdash queries the check runs for the repository's **default
branch** (its latest commit). `no checks` means GitHub reported **zero check
runs** there — typically because the repo has no CI configured, the workflow
has never run on the default branch, or checks run only on other branches /
pull requests.

**Fix.** This is informational, not an error. If you expect checks, confirm
the repo actually runs CI on its default branch (push a commit that triggers
the workflow, or point the widget at a repo that does). A private repo with
checks also needs a token with access (see
["GitHub API rate limit exceeded"](#github-api-rate-limit-exceeded) for how
to set one).

---

## Widgets never load / stay on the loading state

**Symptom.** Cards sit on their loading/skeleton state and never fill in
with data.

**Cause.** Execution is now **server-side and presence-gated**. The server
only runs trackers while a client is actually watching — that is, while
there is an open SSE subscriber on `/api/stream`, or a client has polled
`/api/run` within the last ~20 seconds. With nobody connected, the engine
goes fully idle and makes no upstream calls (no point polling external APIs
when no one is looking, and no history is kept). If your client never
establishes presence, no runs happen and the widgets stay blank.

**Fix.** Make sure the **Live** toggle in the dashboard toolbar is **ON** —
that is what opens the SSE connection / polling that registers presence. If
you are behind a reverse proxy, SSE on `/api/stream` may be buffered or
blocked, which prevents the stream from ever opening; disable proxy
buffering (for nginx, `proxy_buffering off;`). Even without working SSE, the
client **auto-falls back to polling `/api/run` every 8 seconds**, so widgets
should still fill within ~8 seconds. Give it a moment on first load: the
first run of each tracker has to complete before its card has anything to
show.

---

## No live updates / cards don't refresh

**Symptom.** Widgets loaded once but never update on their own.

**Cause.** Live updates depend on the same presence mechanism. If the
**Live** toggle is off, or the SSE stream isn't actually open, the engine
won't keep scheduling runs for you. Proxy buffering or aggressive idle
timeouts can silently kill a long-lived SSE connection.

**Fix.** Confirm the **Live** toggle is on. In the browser **network tab**,
check that `/api/stream` is an open `text/event-stream` request that stays
connected. If a proxy is buffering or timing out the stream, fix the proxy
(see [Widgets never load](#widgets-never-load--stay-on-the-loading-state)) —
otherwise the client falls back to polling `/api/run`, which still works but
is less immediate. Note that each tracker refreshes on **its own interval**
(its `refresh_interval_seconds` override, or the plugin default), not
instantly — so cards update at their own cadence, not all at once.

---

## Can't edit a tracker (Edit missing or 403)

**Symptom.** A tracker has no Edit control, shows a `config` badge, or the API
returns **403** when you try to edit it (`PUT /api/trackers/{id}`).

**Cause.** That tracker is **managed by the config file** (`source=file`). The
config file is its source of truth, so editing it from the UI is blocked — a
reload would just overwrite the change. (Deleting it *is* allowed; see below.)

**Fix.** Edit the tracker in the **YAML config file**, then **Reload from file**
in the Trackers view (or restart) to apply the change. If instead you want to
manage that tracker from the UI, **remove its entry from the config** (and
reload/restart); once it is no longer file-managed you can edit it normally.

**Note on deleting.** File-managed trackers *can* be deleted from the UI (and via
`DELETE /api/trackers/{id}` / **Clear all**) — the on-disk config is never
touched, so **Reload from file** or a restart brings them back. Deleting one then
reloading restores the full configured set with no duplicates.

---

## Trackers from my config file disappeared

**Symptom.** Trackers you had defined in the config file are gone after a
restart.

**Cause.** File-managed trackers are **reconciled on startup**. If you start
plugdash **without `--config`**, or you removed an entry from the file, the
corresponding file trackers no longer have a backing spec and are **deleted**
during reconciliation. (User-created DB trackers, `source=db`, are never
touched by this — only file trackers are reconciled away.)

**Fix.** Always start plugdash with **`--config` pointing at the same file**.
To remove a single file tracker, delete just its entry from the config; to
keep them all, keep them all in the file and keep passing `--config`.

---

## Config file fails / server won't start with --config

**Symptom.** plugdash aborts at startup with a fatal error when given
`--config`.

**Cause.** Config parsing is **strict**. Unknown or misspelled keys are
rejected, `plugin` is **required** on every tracker entry, and duplicate keys
are an error. Any one of these aborts startup rather than starting with a
partially-applied config.

**Fix.** Read the error message — it names the offending entry by its
`trackers[N]` index (for example `trackers[2]: plugin is required` or
`trackers[3]: duplicate key "..."`). Fix that entry: correct the misspelled
key, add the required `plugin` field, or give each tracker a unique key.
Then start again.

---

## FAQ

**Does plugdash store historical data / metrics over time?**
No. plugdash is **just-in-time** and keeps **no history**: every run reconstructs
the current result (any "over time" chart is derived from the source's own
timestamps on each run). It does persist the **latest snapshot per tracker** in a
`snapshots` table — one row per tracker, overwritten each run — purely so the
dashboard paints instantly after a restart and a restart doesn't re-trigger
checks whose interval hasn't elapsed. That is last-known state, not a time series.
(The browser keeps no widget result cache; the only client-side `localStorage` use
is the theme preference.)

**Is there authentication / login?**
No. plugdash has no user accounts or auth layer. Run it on a **trusted
network** (e.g. localhost or behind your own reverse proxy / VPN). Anyone who
can reach the listen address can view and edit trackers and settings.

**Where is the database?**
A single SQLite file. By default it is `plugdash.db` in the working directory
(resolved to an absolute path at startup); override with the `-db` flag. It
stores your trackers, settings, and the latest snapshot per tracker (a
`snapshots` table — last-known state for an instant restart, not history); no
time series is kept. The driver is pure-Go (`modernc.org/sqlite`, no CGO) and uses
WAL mode.

**How do I back it up?**
Copy the `.db` file. Because WAL mode is used, also copy the sidecar files if
they are present (`plugdash.db-wal` and `plugdash.db-shm`) for a fully
consistent snapshot — or stop plugdash first, which checkpoints the WAL, and
then copy just `plugdash.db`. To restore, put the file back and point `-db`
at it.

**Can plugins be written in any language?**
Yes, via **external plugins**. Any executable named `plugdash-plugin-*` that
speaks the tiny stdio protocol works — `describe` prints metadata JSON to
stdout, and `run` reads a config JSON on stdin and writes a result JSON to
stdout. The language is irrelevant (shell, Python, Go, Rust, …) as long as it
honors that contract. See
[External plugin not showing up](#external-plugin-not-showing-up) for the
discovery requirements.

**Where are the configuration flags / environment variables?**

| Flag | Env | Default | Purpose |
| --- | --- | --- | --- |
| `-addr` | — | `:8080` | HTTP listen address |
| `-db` | — | `plugdash.db` | SQLite database file path |
| `-plugins-dir` | `PLUGDASH_PLUGINS_DIR` | `~/.config/plugdash/plugins` | external plugins directory |
| `-config` | — | unset | declarative config file (YAML); trackers reconciled read-only into the DB |
| `-debug` | `PLUGDASH_DEBUG=1` | off | verbose debug logging (also toggleable in Settings) |
| `-version` | — | off | print the version and exit |
| — | `GITHUB_TOKEN` | unset | token for GitHub plugins (also settable in Settings) |
