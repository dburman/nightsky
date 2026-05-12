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
FROM golang:1.22-bookworm AS builder

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

ENTRYPOINT ["nightsky"]
CMD ["capture", "--config", "/etc/nightsky/nightsky.yaml"]
