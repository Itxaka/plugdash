# Plugin Catalog

Reference for every built-in plugin shipped with plugdash. All eleven plugins listed
here are registered in `cmd/plugdash/main.go`.

## How to use this reference

Each plugin is identified by a stable `id`. You create a tracker by POSTing to
`/api/trackers` with this JSON body:

```json
{
  "plugin_id": "<plugin id>",
  "name": "A human label for this tracker",
  "config": { "...": "plugin-specific config fields" },
  "refresh_interval_seconds": 3600
}
```

- `plugin_id` — the plugin `id` from the tables below.
- `name` — free-form label shown in the UI.
- `config` — an object keyed by the plugin's config `Key`s.
- `refresh_interval_seconds` — optional; omit to use the plugin's default
  refresh interval.

### Config field types

Types come from `internal/plugin/plugin.go`: `string`, `number`, `bool`,
`list` (newline-separated text — pass as a JSON array or a `\n`-joined string),
and `select` (one of a fixed set of option values).

### GitHub token

Every GitHub-backed plugin accepts an optional `token` field. When empty it
falls back to the `GITHUB_TOKEN` environment variable. A token saved in the
plugdash Settings UI is exported into `GITHUB_TOKEN` at startup (see
`cmd/plugdash/main.go`), so configuring it once there covers all GitHub plugins.

## Summary

| id | Visualization | Default refresh | Tracks |
|----|---------------|-----------------|--------|
| `github-releases` | list | 24h | Latest N releases of a GitHub repo |
| `github-release-artifacts` | checklist | 24h | Whether a release contains expected asset files |
| `github-repo-stats` | table | 1h | Stars, forks, open issues, watchers, language |
| `http-health` | stat | 30s | Whether an HTTP endpoint is up and returns expected status |
| `rss-feed` | list | 15m | Latest entries of an RSS 2.0 / Atom feed |
| `docker-image` | checklist | 24h | Whether image tags (and arches) exist in a registry |
| `github-actions-status` | checklist | 2m | CI status of the latest commit across many repos |
| `github-activity` | timeseries | 24h | Cumulative stars/commits/issues/PRs over time |
| `github-activity-rate` | timeseries | 6h | Per-period counts of stars/commits/issues/PRs |
| `github-issues` | list | 15m | Latest open issues needing a first reply across repos |
| `github-issue-watch` | list | 15m | Specific issues/PRs: answered state, time since last reply, CI status |
| `file-version` | stat | 1h | Value of a variable in a file on a repo branch (e.g. a pinned dependency) |

---

## GitHub Releases — `github-releases` (visualization: `list`)

Track the latest releases of a GitHub repository. Each item links to the
release and shows its tag, asset count, publish date, and a `draft` /
`prerelease` badge.

**Default refresh interval:** 24h

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `repo` | Repository | string | yes | — | `owner/repo` or full URL |
| `count` | Number of releases | number | no | 5 | How many recent releases to show |
| `show_prereleases` | Show prereleases | bool | no | false | Include prereleases/drafts. Off by default — only stable releases |
| `token` | GitHub token (optional) | string | no | — | Raises rate limits; falls back to `GITHUB_TOKEN` |

The newest stable (non-draft, non-prerelease) release is tagged `latest` (a green
badge in the UI). Prereleases get a `prerelease` badge when shown.

```json
{
  "plugin_id": "github-releases",
  "name": "Kairos releases",
  "config": {
    "repo": "kairos-io/kairos",
    "count": 10
  }
}
```

---

## GitHub Release Artifacts — `github-release-artifacts` (visualization: `checklist`)

Check that a GitHub release contains the expected artifacts. Each expected name
becomes a pass/fail row; an exact case-insensitive match is preferred, and
`*` / `?` glob wildcards are supported.

**Default refresh interval:** 24h

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `repo` | Repository | string | yes | — | `owner/repo` or full URL |
| `tag` | Release tag | string | no | — | `v1.2.3`, or empty / `latest` for the most recent release |
| `expected` | Expected artifacts | list | yes | — | One artifact name per line; supports `*` and `?` glob wildcards |
| `token` | GitHub token (optional) | string | no | — | Falls back to `GITHUB_TOKEN` |

```json
{
  "plugin_id": "github-release-artifacts",
  "name": "Kairos latest assets",
  "config": {
    "repo": "kairos-io/kairos",
    "tag": "latest",
    "expected": ["kairos-*-amd64.iso", "kairos-*-arm64.iso", "*.sha256"]
  }
}
```

---

## GitHub Repo Stats — `github-repo-stats` (visualization: `table`)

Show stars, forks, open issues, watchers and language for a GitHub repository as
a two-column metric/value table.

**Default refresh interval:** 1h

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `repo` | Repository | string | yes | — | `owner/repo` or full URL |
| `token` | GitHub token | string | no | — | Falls back to `GITHUB_TOKEN` |

```json
{
  "plugin_id": "github-repo-stats",
  "name": "Kairos stats",
  "config": {
    "repo": "kairos-io/kairos"
  },
  "refresh_interval_seconds": 3600
}
```

---

## HTTP Health Check — `http-health` (visualization: `stat`)

Check that an HTTP endpoint is reachable and returns the expected status. Shows a
single big stat: `UP` (status ok), the actual status code (unexpected status), or
`DOWN` (request error). A URL without a scheme is prefixed with `https://`.
A failed request or unexpected status is a valid result, not a plugin error.

**Default refresh interval:** 30s

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `url` | URL | string | yes | — | Scheme optional; defaults to `https://` |
| `expected_status` | Expected status | number | no | 200 | Expected HTTP status code |
| `timeout_seconds` | Timeout (seconds) | number | no | 10 | Request timeout |

```json
{
  "plugin_id": "http-health",
  "name": "API health",
  "config": {
    "url": "https://example.com/health",
    "expected_status": 200,
    "timeout_seconds": 5
  }
}
```

---

## RSS / Atom Feed — `rss-feed` (visualization: `list`)

Show the latest entries from an RSS 2.0 or Atom feed. Parses both formats; for
Atom links it prefers `rel="alternate"`. Feed bodies are capped at ~5MB.

**Default refresh interval:** 15m

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `url` | Feed URL | string | yes | — | RSS 2.0 or Atom feed URL |
| `count` | Number of entries | number | no | 5 | How many recent entries to show |

```json
{
  "plugin_id": "rss-feed",
  "name": "Go blog",
  "config": {
    "url": "https://blog.golang.org/feed.atom",
    "count": 8
  }
}
```

---

## Docker Image Check — `docker-image` (visualization: `checklist`)

Check whether Docker images exist for given tags (manual or a repo's latest
release) and architectures, against a Docker Registry v2 endpoint. Docker Hub and
ghcr.io are supported (bare names like `nginx` resolve to `library/nginx`);
generic registries are attempted with their conventional `/token` endpoint and any
bearer challenge.

**Default refresh interval:** 24h

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `image` | Image | string | yes | — | Image ref *without* a tag, e.g. `ghcr.io/org/repo` or `nginx` |
| `tags` | Tags | list | no | — | Tags to check, one per line. Optional if `tag_source` is set |
| `tag_source` | Tag from GitHub repo | string | no | — | `owner/repo`; also checks that repo's latest stable release tag |
| `arches` | Architectures | list | no | — | e.g. `amd64`, `arm64`. Leave empty to only check tag existence |
| `token` | Registry token | string | no | — | Bearer token for private registries |
| `github_token` | GitHub token | string | no | — | Used only to resolve `tag_source`; falls back to `GITHUB_TOKEN` |

At least one of `tags` or `tag_source` must be provided.

**`tag_source` behavior:** when set, the latest stable release of the GitHub repo
is resolved and its tag added as an extra checklist entry. The registry is probed
with both the raw tag and a `v`-stripped variant (so a release tagged `v1.2.3` is
checked as both `v1.2.3` and `1.2.3`); the first that exists wins.

**Architecture checks:** when `arches` is set, each tag is expanded to one row per
arch. A multi-arch image index is matched against its platform list; a single-arch
manifest is reported as `present, single-arch (unverified)`.

**Image validation (`internal/plugins/registry.go`, `ValidateImage`):** the
`image` field must be a bare registry reference. It is rejected if it is empty,
contains whitespace (e.g. a pasted `docker pull ...` command), contains `://` (a
URL), contains invalid characters, includes a tag (`:` in the repo portion —
host:port is allowed), or includes a digest (`@`).

```json
{
  "plugin_id": "docker-image",
  "name": "Kairos image published",
  "config": {
    "image": "quay.io/kairos/kairos",
    "tag_source": "kairos-io/kairos",
    "tags": ["latest"],
    "arches": ["amd64", "arm64"]
  }
}
```

---

## GitHub Actions Status — `github-actions-status` (visualization: `checklist`)

Watch CI status of the latest commit across many repositories. For each repo it
resolves the ref (the given branch, or the repo's default branch) and aggregates
the commit's check runs into a single pass/fail row. A repo with no checks shows
`no checks` (failing); an in-progress run shows `running`. Per-repo errors become a
failing row so one bad repo cannot sink the whole run.

**Default refresh interval:** 2m

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `repos` | Repositories | list | yes | — | One `owner/repo` per line |
| `branch` | Branch | string | no | — | Leave empty to use each repo's default branch |
| `token` | GitHub Token | string | no | — | Falls back to `GITHUB_TOKEN` |

**Per-job links:** each row carries an additive `links` array — one pill per check
run that has an `html_url` (label = job name, `ok` = conclusion `success`), capped
at 25 per repo. These let the UI jump straight to an individual job; they do not
change the row's overall pass/fail aggregation.

```json
{
  "plugin_id": "github-actions-status",
  "name": "CI overview",
  "config": {
    "repos": ["kairos-io/kairos", "kubernetes/kubernetes"],
    "branch": ""
  }
}
```

---

## GitHub Activity Over Time — `github-activity` (visualization: `timeseries`)

Plot a repository's **cumulative** stars, commits, issues or PRs over time,
computed live from item timestamps (nothing is persisted between runs). The
series is daily, in ascending time order, downsampled to at most 365 points.

**Default refresh interval:** 24h

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `repo` | Repository | string | yes | — | `owner/repo` or full URL |
| `metric` | Metric | select | no | `stars` | One of `stars`, `commits`, `issues`, `prs` |
| `token` | GitHub token | string | no | — | Falls back to `GITHUB_TOKEN` |
| `max_pages` | Max pages | number | no | 30 | Pages of 100 items to fetch; caps history depth and API usage |

**Newest-first windowing caveat:** timestamps are fetched from GitHub list
endpoints (e.g. commits, issues, pulls) which return newest items first, paginated
up to `max_pages` pages of 100. When a repo has more activity than the page budget
covers, the fetched window is the *most recent* items, so the cumulative curve
reflects only that recent slice rather than the project's full history. Raise
`max_pages` for deeper history (capped internally at 400 pages). The `issues`
metric excludes pull requests (PRs returned by the issues endpoint are skipped).

```json
{
  "plugin_id": "github-activity",
  "name": "Kairos stars growth",
  "config": {
    "repo": "kairos-io/kairos",
    "metric": "stars",
    "max_pages": 50
  }
}
```

---

## GitHub Activity Rate — `github-activity-rate` (visualization: `timeseries`)

Plot how many commits / issues / PRs / stars happen **per period** (day, week or
month). Unlike the cumulative `github-activity` plugin, each point is the count
within its bucket, with quiet periods filled in as zero. Weeks are Monday-based
(UTC). Computed live; downsampled to at most 365 points.

**Default refresh interval:** 6h

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `repo` | Repository | string | yes | — | `owner/repo` or full URL |
| `metric` | Metric | select | no | `commits` | One of `stars`, `commits`, `issues`, `prs` |
| `period` | Period | select | no | `week` | One of `day`, `week`, `month` |
| `token` | GitHub token | string | no | — | Falls back to `GITHUB_TOKEN` |
| `max_pages` | Max pages | number | no | 20 | Pages of 100 items to fetch; caps the window and API usage |

The same newest-first windowing applies: the fetched window is the most recent
`max_pages × 100` items, so older buckets fall outside the window when activity
exceeds the page budget.

```json
{
  "plugin_id": "github-activity-rate",
  "name": "Kairos commits per week",
  "config": {
    "repo": "kairos-io/kairos",
    "metric": "commits",
    "period": "week",
    "max_pages": 20
  }
}
```

---

## Issues Needing Attention — `github-issues` (visualization: `list`)

Latest open issues across one or more repos that have no response yet (zero
comments). Issues are gathered per repo (newest first, up to 30 per repo),
combined, sorted newest-first across all repos, and trimmed to `count`.

**Default refresh interval:** 15m

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `repos` | Repositories | list | yes | — | One `owner/repo` per line |
| `unanswered_only` | Unanswered only | bool | no | true | Only show issues with zero comments |
| `exclude_labels` | Ignore labels | list | no | — | Hide issues carrying any of these labels (case-insensitive), one per line |
| `count` | Number of issues | number | no | 10 | Max issues to show in total |
| `token` | GitHub token (optional) | string | no | — | Falls back to `GITHUB_TOKEN` |

**Behaviors:** the GitHub issues endpoint also returns pull requests; those carry
a non-null `pull_request` field and are **excluded**. With `unanswered_only` true
(the default — a missing key is treated as true), issues with any comments are
filtered out. `exclude_labels` drops issues tagged with e.g. `blocked` or
`need-discussion`. Issues with zero comments get a `no reply` badge, and each
item shows its repo owner's avatar. (The `github-actions-status` checklist items
likewise show the org avatar per repo.)

---

## Issue Watcher — `github-issue-watch` (visualization: `list`)

Watch a specific set of issues and/or pull requests. Each tracked item becomes a
list row showing whether it has been **answered**, how long since the **last
interaction**, and — for pull requests — its **CI status**.

**Default refresh interval:** 15m

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `issues` | Issues / PRs | list | yes | — | One per line: `owner/repo#number`, or a full issue/PR URL |
| `token` | GitHub token (optional) | string | no | — | Raises rate limits; falls back to `GITHUB_TOKEN` |

**Behaviors:**

- **Answered** = the most recent comment is from someone other than the issue
  author. An item with no comments is "no reply". Determined by fetching only the
  last page of comments (computed from the issue's comment count).
- **Last interaction** = the time of the most recent comment (or creation if
  none); the row's right-hand timestamp ages it live ("3h ago"). The subtitle
  reads `owner/repo#N · issue|PR · state · last reply|opened`.
- **CI** applies only to pull requests: the head commit's check runs are
  aggregated into a `CI: passing` / `CI: failing` / `CI: running` / `CI: no
  checks` badge. Plain issues show no CI badge.
- Badges are tone-colored pills (answered/CI). A bad ref or a per-item fetch
  error renders as an `invalid` / `error` row rather than failing the whole run.

```json
{
  "plugin_id": "github-issue-watch",
  "name": "My watched issues",
  "config": {
    "issues": "kairos-io/kairos#1234\nhttps://github.com/kairos-io/kairos/pull/56"
  }
}
```

---

## File Value Watcher — `file-version` (visualization: `stat`)

Reads a file on a GitHub repo branch (over `raw.githubusercontent.com`) and
reports the value of a named variable as a single stat — handy for watching a
pinned dependency or the `go` directive in a `go.mod` across repos.

**Default refresh interval:** 1h

| Key | Label | Type | Required | Default | Notes |
|-----|-------|------|----------|---------|-------|
| `repo` | Repository | string | yes | — | `owner/repo` or full URL |
| `ref` | Branch or tag | string | no | `main` | Branch/tag to read from |
| `path` | File path | string | yes | — | Path to the file within the repo |
| `key` | Variable name | string | yes | — | Name left of a `=` / `:` (or whitespace, e.g. the go.mod `go` directive) |

**Behaviors:** matches `key = value`, `key: value`, or whitespace-delimited
`key value`; strips surrounding quotes and a trailing comma. Missing key →
`status: error`, value `not found`. Public repos only (raw content; no token).

```json
{
  "plugin_id": "file-version",
  "name": "Kairos go version",
  "config": { "repo": "kairos-io/kairos", "ref": "main", "path": "go.mod", "key": "go" }
}
```

```json
{
  "plugin_id": "github-issues",
  "name": "Kairos triage",
  "config": {
    "repos": ["kairos-io/kairos", "kairos-io/immucore"],
    "unanswered_only": true,
    "count": 15
  }
}
```
