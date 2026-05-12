.PHONY: build build-zwo build-rpi build-all clean install \
       docker-arm64 docker-arm64-zwo docker-armv7 docker-armv7-zwo docker-runtime

BINARY := nightsky
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X github.com/dburman/nightsky/internal/config.Version=$(VERSION)"

# Default: build without camera-specific CGo bindings (useful for development)
build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/nightsky

# Build with ZWO ASI SDK support (requires libASICamera2 + libusb)
build-zwo:
	go build $(LDFLAGS) -tags zwo -o bin/$(BINARY) ./cmd/nightsky

# Build with RPi libcamera support (always available, uses CLI wrapper)
build-rpi:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/nightsky

# Build with all camera backends
build-all:
	go build $(LDFLAGS) -tags zwo -o bin/$(BINARY) ./cmd/nightsky

# Cross-compile for Raspberry Pi (64-bit)
build-arm64:
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY)-arm64 ./cmd/nightsky

build-arm64-zwo:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc \
		go build $(LDFLAGS) -tags zwo -o bin/$(BINARY)-arm64 ./cmd/nightsky

# ---------- Docker cross-compilation ----------
# These targets use Docker to cross-compile for Raspberry Pi without needing
# a local cross-compilation toolchain. The binary is output to bin/.

# RPi 3/4/5 (64-bit) — libcamera only
docker-arm64:
	docker build -f Dockerfile.build \
		--target export \
		--build-arg TARGETARCH=arm64 \
		--output type=local,dest=bin/ .
	@mv bin/nightsky bin/$(BINARY)-arm64
	@echo "Built bin/$(BINARY)-arm64"

# RPi 3/4/5 (64-bit) — with ZWO ASI SDK
docker-arm64-zwo:
	docker build -f Dockerfile.build \
		--target export \
		--build-arg TARGETARCH=arm64 \
		--build-arg BUILD_TAGS=zwo \
		--output type=local,dest=bin/ .
	@mv bin/nightsky bin/$(BINARY)-arm64
	@echo "Built bin/$(BINARY)-arm64 (with ZWO support)"

# RPi 2/Zero 2 (32-bit ARMv7)
docker-armv7:
	docker build -f Dockerfile.build \
		--target export \
		--build-arg TARGETARCH=arm \
		--build-arg TARGETVARIANT=v7 \
		--output type=local,dest=bin/ .
	@mv bin/nightsky bin/$(BINARY)-armv7
	@echo "Built bin/$(BINARY)-armv7"

# RPi 2/Zero 2 (32-bit ARMv7) — with ZWO ASI SDK
docker-armv7-zwo:
	docker build -f Dockerfile.build \
		--target export \
		--build-arg TARGETARCH=arm \
		--build-arg TARGETVARIANT=v7 \
		--build-arg BUILD_TAGS=zwo \
		--output type=local,dest=bin/ .
	@mv bin/nightsky bin/$(BINARY)-armv7
	@echo "Built bin/$(BINARY)-armv7 (with ZWO support)"

# Build a minimal runtime Docker image (for running on the Pi via Docker)
docker-runtime:
	docker build -f Dockerfile.build \
		--target runtime \
		--platform linux/arm64 \
		-t nightsky:latest .
	@echo "Built nightsky:latest (arm64 runtime image)"

clean:
	rm -rf bin/

install: build
	install -m 755 bin/$(BINARY) /usr/local/bin/

test:
	go test ./...

test-verbose:
	go test -v ./...

lint:
	golangci-lint run ./...
