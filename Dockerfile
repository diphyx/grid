# syntax=docker/dockerfile:1

# One file, two images: `--target node` runs the node daemon, `--target hub` the hub daemon.
# Build from the repo root:
#
#   docker build --target node -t ghcr.io/diphyx/grid-node .
#   docker build --target hub -t ghcr.io/diphyx/grid-hub .

# Go cross-compiles, so this stage stays on the runner's own architecture and every target
# builds at native speed; TARGETOS and TARGETARCH pick the binary's platform.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# The module files land first, so the dependency layer only rebuilds when they change.
COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY main.go ./
COPY pkg/ pkg/
COPY cmd/ cmd/

# Static, so the runtime needs no libc of a matching build; `grid` carries the node daemon,
# its view, and the test kit.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/grid .

# Alpine over Debian: the binary is static, and cmds still get `sh` for their scripts.
FROM alpine:3 AS base

# tini is PID 1: it forwards SIGTERM and reaps the orphans a cmd's children leave behind.
RUN apk add --no-cache ca-certificates tini

# Keys and config live in /etc/grid, state in /var/lib/grid, the daemon's socket in /run/grid;
# the compose file mounts over the first two.
RUN addgroup -S grid \
    && adduser -S -G grid -h /var/lib/grid grid \
    && mkdir -p /etc/grid /var/lib/grid /run/grid \
    && chown -R grid:grid /var/lib/grid /run/grid

COPY --from=build /out/grid /usr/local/bin/grid

USER grid

# The default grid port: the node always listens on it, a hub only with `signal: true`.
EXPOSE 9420

ENTRYPOINT ["/sbin/tini", "--"]

# A node is FROM this plus its cmds and node.toml; the cmds run as the grid user, in the space.
FROM base AS node

# The view answers only while the daemon is up, so it doubles as the health check.
HEALTHCHECK --start-period=10s --interval=30s --timeout=5s --retries=3 \
    CMD ["grid", "node", "status"]

CMD ["grid", "node", "run", "--config", "/etc/grid/node.toml"]

# A hub is FROM this plus its hub.toml; jobs come in through `grid hub submit` or the socket API.
FROM base AS hub

# The same as the node: the view answers only while the daemon is up.
HEALTHCHECK --start-period=10s --interval=30s --timeout=5s --retries=3 \
    CMD ["grid", "hub", "status"]

CMD ["grid", "hub", "run", "--config", "/etc/grid/hub.toml"]
