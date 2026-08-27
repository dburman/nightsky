# Standard Dockerfile for CI / GHCR builds targeting linux/arm64.
# Produces a minimal runtime image with the nightsky binary and ffmpeg.
#
# CGO_ENABLED=0 — the libcamera backend wraps rpicam-still/libcamera-still
# via CLI and requires no C libraries. ZWO ASI USB cameras require CGo;
# use Dockerfile.build for that variant.
#
# Build locally:
#   docker buildx build --platform linux/arm64 -t nightsky:latest .
#
# The VERSION build arg is set by CI; omit it for a "dev" binary.

# ── stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.26-bookworm AS builder

ARG VERSION=dev
ARG BUILD_TAGS=""

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN set -e; \
    LDFLAGS="-X github.com/dburman/nightsky/internal/config.Version=${VERSION} -s -w"; \
    TAGS=""; \
    [ -n "${BUILD_TAGS}" ] && TAGS="-tags ${BUILD_TAGS}"; \
    CGO_ENABLED=0 go build ${TAGS} \
        -ldflags "${LDFLAGS}" \
        -o /out/nightsky \
        ./cmd/nightsky

# ── stage 2: runtime ──────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ffmpeg \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/nightsky /usr/local/bin/nightsky
COPY configs/nightsky.example.yaml /etc/nightsky/nightsky.yaml

# Run as a non-root user. The uid/gid are fixed so host bind mounts can be
# matched: the ./output and ./darks directories on the host must be writable
# by uid 10001, e.g.
#
#   sudo chown -R 10001:10001 ./output ./darks
#
# or override with `user:` in docker-compose.yml to match your own uid.
RUN groupadd --gid 10001 nightsky \
    && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin nightsky \
    && mkdir -p /output /darks \
    && chown nightsky:nightsky /output /darks
USER 10001:10001

ENTRYPOINT ["nightsky"]
CMD ["capture", "--config", "/etc/nightsky/nightsky.yaml"]
