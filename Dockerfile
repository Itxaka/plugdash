# syntax=docker/dockerfile:1

# --- Builder stage ---
# Pinned to the build platform so multi-arch builds cross-compile (fast) instead
# of emulating the target under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build

WORKDIR /src

# Download dependencies first for better layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Build a fully static binary (pure-Go SQLite, so CGO can stay off). TARGETOS /
# TARGETARCH / TARGETVARIANT are provided automatically by buildx for the
# requested platform; TARGETVARIANT (e.g. v7) maps to GOARM for 32-bit arm.
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /plugdash ./cmd/plugdash

# --- Final stage ---
FROM gcr.io/distroless/static-debian12

LABEL org.opencontainers.image.title="plugdash" \
      org.opencontainers.image.description="A small, self-contained plugin dashboard (CI status, releases, activity, issues, image checks)."

# The SQLite database lives here; mount a volume to persist it.
WORKDIR /data
VOLUME ["/data"]

COPY --from=build /plugdash /plugdash

EXPOSE 8080

# External plugins and user themes live under /data so a single mounted volume
# persists them alongside the DB. Drop executables in plugins/, theme *.css in themes/.
ENV PLUGDASH_PLUGINS_DIR=/data/plugins \
    PLUGDASH_THEMES_DIR=/data/themes

ENTRYPOINT ["/plugdash"]
CMD ["-addr", ":8080", "-db", "/data/plugdash.db"]
