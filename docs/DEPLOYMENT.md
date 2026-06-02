# Deployment

plugdash ships as a single self-contained binary and is easy to run locally,
under a process supervisor, or in a container. This document covers building,
running, containerizing, persistence, reverse proxying, and external plugins.

For configuration details (flags, environment variables, settings, the
database), see [CONFIGURATION.md](CONFIGURATION.md).

## Building

Build with either the Makefile or `go build` directly:

```sh
make build                  # produces ./bin/plugdash
# or
go build -o plugdash ./cmd/plugdash
```

Other useful Makefile targets:

```sh
make run      # go run ./cmd/plugdash
make test     # go test ./...
make vet      # go vet ./...
make fmt      # gofmt -w .
make docker   # docker build -t plugdash .
make clean    # remove bin/ and *.db
make          # fmt, vet, test, build
```

### Single self-contained binary

The resulting binary is fully self-contained:

- The frontend assets are compiled into the binary via `go:embed`, so there is
  no separate static-file directory to ship or serve.
- plugdash uses a **pure-Go SQLite driver** (`modernc.org/sqlite`), so **no CGO**
  is required. You can produce a fully static binary with `CGO_ENABLED=0`:

  ```sh
  CGO_ENABLED=0 go build -ldflags="-s -w" -o plugdash ./cmd/plugdash
  ```

This combination means the binary has no runtime dependencies beyond a writable
location for its database file.

## Running locally

Run with defaults (listens on `:8080`, database at `plugdash.db` in the working
directory):

```sh
./plugdash
```

Common overrides:

```sh
./plugdash -addr 127.0.0.1:8080 -db /var/lib/plugdash/plugdash.db -debug
```

See [CONFIGURATION.md](CONFIGURATION.md#command-line-flags) for the full list of
flags and environment variables.

## Docker

plugdash builds with a multi-stage `Dockerfile`:

1. A `golang` builder stage downloads dependencies and compiles a static binary
   with `CGO_ENABLED=0`.
2. The final stage is based on `gcr.io/distroless/static-debian12`, a minimal
   distroless image containing just the static binary — no shell, no package
   manager.

Key image facts:

- Working directory is `/data`, declared as a `VOLUME`, where the database is
  expected to live.
- The container `EXPOSE`s port `8080`.
- The default command is `-addr :8080 -db /data/plugdash.db`.

Build and run:

```sh
make docker
# or
docker build -t plugdash .

docker run -d --name plugdash \
  -p 8080:8080 \
  -v plugdash-data:/data \
  plugdash
```

## Persistence

plugdash keeps all state in its SQLite database (default `/data/plugdash.db` in
the container). Because the database is opened in WAL mode, it produces `-wal`
and `-shm` sidecar files; **mount/persist the whole directory** containing the
database, not just the `.db` file.

Persist two things across restarts:

1. **The database directory** — mount a volume at `/data` (as in the
   `docker run` example above) or point `-db` at a path on persistent storage.
2. **The plugins directory** — if you use external plugins, mount the directory
   that holds their executables and point plugdash at it (see below).

Example mounting both:

```sh
docker run -d --name plugdash \
  -p 8080:8080 \
  -v plugdash-data:/data \
  -v /opt/plugdash/plugins:/plugins:ro \
  -e PLUGDASH_PLUGINS_DIR=/plugins \
  plugdash
```

## Deploying with a config file

You can manage trackers declaratively ("config-as-code") by pointing `--config`
at a YAML file. Trackers defined there are reconciled into the database and shown
read-only in the UI (a `config` badge, with edit/delete disabled); users can
still add their own trackers through the UI. See
[CONFIGURATION.md](CONFIGURATION.md) for the schema and `examples/plugdash.yaml`
for a sample.

Mount the file into the container and point `--config` at it:

```sh
docker run -d --name plugdash \
  -p 8080:8080 \
  -v plugdash-data:/data \
  -v ./plugdash.yaml:/etc/plugdash.yaml:ro \
  plugdash \
  -addr :8080 -db /data/plugdash.db -config /etc/plugdash.yaml
```

Or in `docker-compose.yml`:

```yaml
services:
  plugdash:
    image: ghcr.io/<owner>/plugdash:latest
    command: ["-addr", ":8080", "-db", "/data/plugdash.db", "-config", "/etc/plugdash.yaml"]
    ports:
      - "8080:8080"
    volumes:
      - plugdash-data:/data
      - ./plugdash.yaml:/etc/plugdash.yaml:ro
    environment:
      - GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxx
volumes:
  plugdash-data:
```

A **GitHub token** can live in the config file's `settings` block (`github_token`)
or be supplied via the `GITHUB_TOKEN` environment variable — whichever you prefer
for keeping secrets out of the YAML.

## Behind a reverse proxy

plugdash has **no built-in authentication or authorization**. Anyone who can
reach the listen address has full access to the dashboard and its settings
(including any stored GitHub token). Therefore:

- Keep plugdash on a **trusted network**, and/or
- Put it **behind a reverse proxy that enforces authentication** (e.g. HTTP
  basic auth, OAuth2 proxy, or your ingress' auth integration).

A typical setup binds plugdash to localhost and lets the proxy terminate TLS and
handle auth:

```sh
./plugdash -addr 127.0.0.1:8080
```

Example nginx location block:

```nginx
location / {
    auth_basic           "plugdash";
    auth_basic_user_file /etc/nginx/.htpasswd;
    proxy_pass           http://127.0.0.1:8080;
    proxy_set_header     Host $host;
    proxy_set_header     X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header     X-Forwarded-Proto $scheme;
}
```

### Server-Sent Events (`/api/stream`)

Live updates are delivered over a **long-lived streaming response** at
`/api/stream`. plugdash already sends `X-Accel-Buffering: no` to ask proxies not
to buffer it, but some proxies still need explicit configuration:

- Disable response buffering for the stream so events flush immediately. In
  nginx, set `proxy_buffering off;` (and `proxy_cache off;`) on the
  `/api/stream` location.
- Don't close idle connections too aggressively: keep `proxy_read_timeout`
  generous (the connection is intentionally long-lived even when quiet) and use
  HTTP/1.1.

```nginx
location /api/stream {
    proxy_pass            http://127.0.0.1:8080;
    proxy_http_version    1.1;
    proxy_set_header      Connection "";
    proxy_buffering       off;
    proxy_cache           off;
    proxy_read_timeout    1h;
}
```

If live updates work locally but not behind the proxy (the **Live** toggle stays
connected but widgets only refresh on reload), buffering is the usual culprit.

## External plugins in containers

External plugins are standalone executables discovered in the plugins directory
at startup and registered alongside the built-in plugins.

To use them in a container, mount the executables into a directory and point
plugdash at it via `-plugins-dir` or `PLUGDASH_PLUGINS_DIR`:

```sh
docker run -d --name plugdash \
  -p 8080:8080 \
  -v plugdash-data:/data \
  -v /opt/plugdash/plugins:/plugins:ro \
  -e PLUGDASH_PLUGINS_DIR=/plugins \
  plugdash
```

Notes:

- The mounted files must be **executable** and built for the container's
  platform/architecture (the distroless image is Linux; the binaries must be
  static or otherwise runnable in that minimal environment).
- The directory resolution order is `-plugins-dir` > `PLUGDASH_PLUGINS_DIR` >
  `~/.config/plugdash/plugins`. In a container, prefer an explicit
  `-plugins-dir`/`PLUGDASH_PLUGINS_DIR` rather than relying on the per-user
  default. See
  [CONFIGURATION.md](CONFIGURATION.md#plugins-directory-resolution).
- plugdash logs how many external plugins it loaded at startup.

## Prebuilt images & CI

Two GitHub Actions workflows ship with the project (`.github/workflows/`):

- **`ci.yml`** — on every push/PR: `gofmt` check, `go vet`, `go test -race ./...`,
  `go build`, a `node --check` of the frontend, and a Docker image build (no push).
- **`release.yml`** — on a `vX.Y.Z` tag:
  - builds and pushes **multi-arch** (`linux/amd64`, `linux/arm64`) images to the
    GitHub Container Registry as `ghcr.io/<owner>/plugdash`, tagged with the
    semver (`X.Y.Z`, `X.Y`, `X`) and `latest`;
  - cross-compiles standalone binaries (linux/darwin/windows × amd64/arm64) with
    the version stamped via `-ldflags "-X main.version=<tag>"`, plus a
    `checksums.txt`, and attaches them to a generated GitHub Release.

Pull and run a released image:

```sh
docker run -p 8080:8080 -v plugdash-data:/data ghcr.io/<owner>/plugdash:latest
```

Cut a release by pushing a tag:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

`./plugdash -version` prints the stamped version (`dev` for local builds).
