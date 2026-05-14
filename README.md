# Nightsky

A lightweight, CLI-configured all-sky camera service for long-exposure night photography and timelapse generation. Captures images continuously, automatically switching between day and night modes based on sun position, and uploads results to S3 or HTTP endpoints.

Inspired by [AllskyTeam/allsky](https://github.com/AllskyTeam/allsky) but stripped down to the essentials — no PHP, no Node. Configure everything from the command line or a single YAML file.

## Features

- **ZWO ASI camera support** — Full control via CGo bindings to the ASI SDK: exposure up to 300s, gain, white balance, binning (1x/2x/4x), flip, TEC cooler, temperature readout, USB bandwidth control
- **Raspberry Pi camera support** — Wraps `rpicam-still` / `libcamera-still` for CSI cameras (HQ IMX477, Module 3 IMX708, and others)
- **Automatic day/night switching** — Built-in NOAA solar position algorithm determines day/night based on your coordinates and a configurable sun altitude angle (civil, nautical, or astronomical twilight)
- **Auto-exposure** — Logarithmic exposure level algorithm that adjusts exposure and gain to maintain target brightness, with anti-oscillation detection
- **Dark frame subtraction** — Capture, average, and subtract calibration frames to remove hot pixels and fixed-pattern noise
- **Timelapse generation** — Assembles each night's images into an MP4 via ffmpeg with CRF quality mode, optional deflicker filter, and smooth Holy Grail day/night exposure transitions
- **Keogram** — Single-image summary of the night: center column from each frame stitched left-to-right so clouds, aurora, and milky way transits are visible at a glance
- **Star trails** — Max-blend stack of all night frames, keeping the brightest pixel seen at each position across the full night
- **White balance analysis** — Samples images at end of night, measures mean R/G/B channel values, and writes a plain-text report (`wb-analysis-<date>.txt`) with suggested WB red/blue adjustments
- **WebP conversion** — Converts captured PNGs to WebP at end of night for long-term storage; configurable quality and optional deletion of originals (requires `cwebp`)
- **S3 upload** — AWS S3 with support for custom endpoints (Backblaze B2, MinIO, etc.)
- **HTTP upload** — POST images/videos to any HTTP endpoint with optional auth
- **Metadata overlay** — Timestamp, exposure, gain, and sensor temperature rendered directly on images
- **Automatic cleanup** — Space-based (`min_free_gb`) or time-based (`days_to_keep`) full-directory deletion; or `prune_raw_after_days` to delete only raw frames while keeping timelapse, keogram, star trails, and WB analysis permanently
- **Web UI** — Built-in HTTP server (`nightsky serve`) for browsing captures by date with Video, Keogram, Star Trails, and Images tabs; paginated lazy-loaded thumbnails; no external dependencies, embedded in the binary
- **Single binary** — Cross-compiles to ARM64/ARMv7 for Raspberry Pi deployment

---

## Building

### Requirements (build)

- **Go 1.26+**
- **Docker** (optional, for cross-compilation without a local toolchain)

### Build from source

```bash
git clone https://github.com/dburman/nightsky.git
cd nightsky

# Build for the current platform (RPi cameras only)
make build

# Build with ZWO ASI SDK support (requires libASICamera2 + libusb-1.0 headers)
make build-zwo

# Install to /usr/local/bin
sudo make install

# Run tests
make test

# Lint (requires golangci-lint)
make lint
```

### Cross-compile for Raspberry Pi

#### Native cross-compilation

Requires `aarch64-linux-gnu-gcc` (arm64) or `arm-linux-gnueabihf-gcc` (armv7) installed locally.

```bash
# RPi 3/4/5 (64-bit ARM)
make build-arm64

# RPi 3/4/5 with ZWO ASI SDK
make build-arm64-zwo
```

Copy the binary to your Pi:

```bash
scp bin/nightsky-arm64 pi@raspberrypi:~/nightsky
```

#### Cross-compile via Docker (no toolchain required)

Docker handles the entire cross-compilation environment — no need to install Go, cross-compilers, or ARM libraries on your host machine.

```bash
# RPi 3/4/5 (64-bit ARM)
make docker-arm64

# RPi 3/4/5 (64-bit ARM) with ZWO ASI SDK
make docker-arm64-zwo

# RPi 2 / Zero 2 (32-bit ARMv7)
make docker-armv7

# RPi 2 / Zero 2 (32-bit ARMv7) with ZWO ASI SDK
make docker-armv7-zwo
```

The binary is output to `bin/nightsky-arm64` or `bin/nightsky-armv7`.

### CI / GHCR builds

Pushing to `main` (or tagging a release) triggers `.github/workflows/docker.yml`, which builds a `linux/arm64` image via QEMU and pushes it to `ghcr.io/dburman/nightsky`. The `docker-compose.yml` on the Pi pulls this image directly — no manual build or transfer step required.

### Build a Docker runtime image locally

Use the plain `Dockerfile` with `docker buildx` to build and load a local image (libcamera / no ZWO):

```bash
docker buildx build --platform linux/arm64 --load -t nightsky:latest .
```

For the ZWO variant or ARMv7 targets, use `Dockerfile.build` via the Makefile targets (`make docker-arm64-zwo`, `make docker-armv7`, etc.).

---

## Deploying

### Requirements (runtime)

- **ffmpeg** — for timelapse generation
- **rpicam-still** or **libcamera-still** — for Raspberry Pi CSI cameras (Bookworm uses `rpicam-still`; Bullseye uses `libcamera-still` — nightsky auto-detects both)
- **libASICamera2 + libusb-1.0** — for ZWO cameras (only if built with `-tags zwo`)
- **cwebp** — for WebP conversion (only if `output.webp.enabled: true`; install via `apt-get install webp`)

### Option 1: Docker Compose (recommended)

The `docker-compose.yml` in this repo is ready to use. It pulls the pre-built `linux/arm64` image from GHCR and runs both services — no local build required.

```bash
# Copy and edit your config
cp configs/nightsky.example.yaml nightsky.yaml

# Pull and start both services
docker compose pull
docker compose up -d

# View logs
docker compose logs -f nightsky      # capture loop
docker compose logs -f nightsky-ui   # web UI
```

Then open `http://<pi-hostname>:8080` in a browser.

The `nightsky-ui` service shares the same `./output` volume as `nightsky` so it sees captures in real time without needing camera access.

To pass AWS credentials for S3 upload, either set them in the `environment` section of `docker-compose.yml` or mount your credentials file:

```yaml
volumes:
  - ~/.aws:/root/.aws:ro
```

### Option 2: systemd services

Create two unit files — one for capture, one for the web UI.

`/etc/systemd/system/nightsky.service`:

```ini
[Unit]
Description=Nightsky All-Sky Camera
After=network.target

[Service]
Type=simple
User=pi
ExecStart=/usr/local/bin/nightsky capture --config /etc/nightsky/nightsky.yaml
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
```

`/etc/systemd/system/nightsky-ui.service`:

```ini
[Unit]
Description=Nightsky Web UI
After=network.target

[Service]
Type=simple
User=pi
ExecStart=/usr/local/bin/nightsky serve --config /etc/nightsky/nightsky.yaml --addr :8080
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable nightsky nightsky-ui
sudo systemctl start nightsky nightsky-ui

# View logs
journalctl -u nightsky -f
journalctl -u nightsky-ui -f
```

Then open `http://<pi-hostname>:8080` in a browser.

### Option 3: Docker run

```bash
# Capture
docker run -d --name nightsky \
  --device /dev/video0 \
  --device /dev/vchiq \
  -v ./nightsky.yaml:/etc/nightsky/nightsky.yaml:ro \
  -v ./output:/output \
  nightsky:latest

# Web UI (shares the same output volume)
docker run -d --name nightsky-ui \
  -p 8080:8080 \
  -v ./nightsky.yaml:/etc/nightsky/nightsky.yaml:ro \
  -v ./output:/output \
  nightsky:latest \
  serve --config /etc/nightsky/nightsky.yaml --addr :8080
```

---

## Configuration

Nightsky uses a single YAML configuration file. The search order is:

1. Path specified by `--config` flag
2. `./nightsky.yaml`
3. `$HOME/.config/nightsky/nightsky.yaml`
4. `/etc/nightsky/nightsky.yaml`

Environment variables override config file values with the prefix `NIGHTSKY_` (e.g., `NIGHTSKY_CAMERA_TYPE=zwo`).

See [`configs/nightsky.example.yaml`](configs/nightsky.example.yaml) for a fully commented example.

### Camera

```yaml
camera:
  type: libcamera    # "libcamera" or "zwo"
  index: 0           # ZWO camera index (when multiple connected)
  device: 0          # libcamera device index
  usb_bandwidth: 80  # ZWO USB bandwidth limit (40-100)
  flip: 0            # 0=none, 1=horizontal, 2=vertical, 3=both
```

### Location

```yaml
location:
  latitude: 40.7128
  longitude: -74.0060
  # Sun altitude angle for day/night transition (degrees)
  # -6 = civil twilight, -12 = nautical, -18 = astronomical
  angle: -6
```

### Day/Night mode settings

Each mode has independent settings for exposure, gain, and image format:

```yaml
day:
  exposure: 1ms
  max_exposure: 100ms
  gain: 1
  max_gain: 10
  auto_exposure: true
  target_brightness: 160
  delay: 5s
  binning: 1
  wb_red: 52              # White balance red (ZWO: 0-99)
  wb_blue: 90             # White balance blue (ZWO: 0-99)
  awb: true               # Auto white balance (libcamera only)
  image_type: jpg         # "jpg" or "png"
  quality: 95             # JPEG quality (1-100)
  skip_frames: 5          # Frames to discard after mode transition
  denoise: cdn_fast       # libcamera denoising: off, cdn_off, cdn_fast, cdn_hq

night:
  exposure: 10s
  max_exposure: 60s
  gain: 200
  max_gain: 400
  auto_exposure: true
  target_brightness: 90
  delay: 0s
  binning: 1
  wb_red: 52
  wb_blue: 90
  awb: false
  image_type: png
  quality: 95
  skip_frames: 1
  denoise: off
  cooler_enabled: false   # ZWO TEC cooler
  cooler_target: -10      # Target temperature (°C)
```

### Output

```yaml
output:
  directory: ./output       # Base directory; images saved to <dir>/YYYY-MM-DD/
  filename_prefix: allsky   # Filename prefix for captured images
  # Disk space cleanup — choose one strategy (or leave both at 0 to keep everything):
  min_free_gb: 5            # Space-based (recommended): delete oldest nights when disk is low
  days_to_keep: 0           # Time-based: delete directories older than N days (0 = disabled)
  overlay: true             # Render timestamp/metadata on images
  overlay_font_size: 24

  timelapse:
    enabled: true
    fps: 25
    codec: libx264          # or libx265
    crf: 20                 # constant-rate-factor quality (0 = use bitrate instead)
    bitrate: 2000k          # used only when crf: 0
    deflicker: true         # smooth per-frame brightness variation

  # Prune raw frames after N days, keeping synthesized outputs (0 = disabled).
  # Combine with days_to_keep to remove entire directories after even longer.
  prune_raw_after_days: 0

  # Requires: apt-get install webp
  webp:
    enabled: false
    quality: 85             # 0–100 lossy quality
    delete_originals: false # remove source PNG after conversion
```

### Upload

```yaml
upload:
  upload_images: false     # Upload every captured image
  upload_timelapse: true   # Upload timelapse at end of night

  s3:
    enabled: true
    bucket: my-allsky-bucket
    region: us-east-1
    prefix: nightsky
    # For S3-compatible services:
    # endpoint: https://s3.us-east-005.backblazeb2.com

  http:
    enabled: false
    url: https://example.com/upload
    authorization: Bearer mytoken
```

S3 credentials are resolved via the standard AWS credential chain (environment variables, `~/.aws/credentials`, IAM role, etc.).

### Dark frames

```yaml
dark:
  enabled: false        # Enable dark frame subtraction during capture
  directory: ./darks    # Where dark frames are stored
  count: 5              # Frames to average when capturing darks
```

---

## Usage

### Quick start

```bash
# 1. Create config
cp configs/nightsky.example.yaml nightsky.yaml

# 2. Verify your camera
nightsky info

# 3. Start capturing
nightsky capture
```

Images are saved to `./output/YYYY-MM-DD/`. The service runs continuously, switching between day and night modes automatically. Press Ctrl+C to stop.

### Commands

```
nightsky [command] [flags]

Commands:
  capture     Start the continuous capture loop
  serve       Start the web UI server
  timelapse   Generate a timelapse video from captured images
  dark        Capture dark frames for calibration
  analyze     Analyse images and suggest white balance settings
  info        Display camera information
  clean       Remove old capture directories
  version     Print version

Global Flags:
  -c, --config string      Path to config file (default: ./nightsky.yaml)
  -l, --log-level string   Log level: debug, info, warn, error (default "info")
```

### `nightsky capture`

Runs the main capture loop. Automatically detects day/night based on sun position and applies the corresponding settings. All images from a single night session are kept in one directory named after the night's start date, even when captures cross midnight.

At the end of each night, generates (in parallel where possible): a timelapse video, a keogram, a star-trails image, and a white balance analysis report. If WebP conversion is enabled it runs next, then uploads and disk cleanup.

```bash
nightsky capture
nightsky capture --config /etc/nightsky/nightsky.yaml
nightsky capture --log-level debug
```

### `nightsky serve`

Starts the web UI server. Reads from the configured output directory — does not require a camera to be connected. Open the URL in a browser to browse captures by date across four sub-tabs (Video, Keogram, Star Trails, Images) and inspect the current configuration.

```bash
nightsky serve
nightsky serve --addr :8080
nightsky serve --config /etc/nightsky/nightsky.yaml --addr 0.0.0.0:8080
```

The UI is served at `http://localhost:8080` by default. It is embedded in the binary with no external dependencies.

### `nightsky timelapse`

Generates a timelapse video from a directory of captured images.

```bash
# Most recent date directory
nightsky timelapse

# Specific directory with custom settings
nightsky timelapse --dir ./output/2026-03-16 --fps 30 --bitrate 4000k --codec libx265
```

### `nightsky dark`

Captures dark frames for calibration. **Cover the camera lens before running this command.** Captures dark frames for both day and night mode exposure settings.

```bash
nightsky dark
```

Dark frames are saved to `dark.directory` (default `./darks/`) and are automatically loaded during capture when `dark.enabled` is true.

### `nightsky info`

Displays information about the connected camera, including resolution, controls, and supported features.

```bash
nightsky info
```

Example output:

```
Camera: RPi imx477
Model:  imx477
Resolution: 4056 x 3040
Color:  true
Cooler: false
Bayer:  RGGB

Controls:
  Exposure                  min=1        max=200000   default=10000
  Gain                      min=0        max=600      default=0    [auto]
  ...

Sensor Temperature: 22.5°C
```

### `nightsky clean`

Removes capture directories older than the configured retention period.

```bash
nightsky clean
nightsky clean --days 7
```

### `nightsky analyze`

Samples images from a capture directory, measures the mean R/G/B channel values, and writes a white balance suggestion report. Runs automatically at end of night; use this command to re-run analysis manually or on a specific directory.

```bash
# Most recent session
nightsky analyze

# Specific directory
nightsky analyze --dir ./output/2026-03-15
```

The report is written to `wb-analysis-<date>.txt` in the image directory:

```
Nightsky White Balance Analysis
================================
Generated: 2026-05-13 06:14:22
Session:   2026-05-12

Night Mode
----------
Images analysed: 87 (sampled from 435)
Mean channels:   R=45.2  G=52.8  B=71.3
Color cast:      cool/blue (B high, R low)

Current:   WB Red=52  WB Blue=90
Suggested: WB Red=61  WB Blue=67

  Suggested change: increase WB Red by 9, decrease WB Blue by 23.
  Apply gradually — one adjustment per night is recommended.
```

---

## Output Structure

```
output/
├── 2026-03-15/             ← named after the night's start date (survives midnight crossings)
│   ├── allsky-20260315191503.png       ← or .webp if conversion enabled
│   ├── allsky-20260315191513.png
│   ├── ...
│   ├── timelapse-2026-03-15.mp4
│   ├── keogram-2026-03-15.jpg
│   ├── startrails-2026-03-15.jpg
│   ├── wb-analysis-2026-03-15.txt
│   └── .thumbs/            ← auto-generated thumbnail cache (160px JPEG)
│       ├── allsky-20260315191503_160.jpg
│       └── ...
├── 2026-03-16/
│   └── ...
darks/
├── dark_10000ms_gain200_bin1.png
└── dark_1ms_gain1_bin1.png
```

## Architecture

```
cmd/nightsky/          CLI entrypoint (cobra)
internal/
├── camera/            Camera interface
│   ├── zwo/           ZWO ASI SDK via CGo (build tag: zwo)
│   └── libcamera/     rpicam-still / libcamera-still CLI wrapper
├── capture/           Capture loop, auto-exposure, dark frames, disk cleanup
├── astro/             Sun position calculation (NOAA algorithm)
├── image/             Overlay rendering, image encoding, thumbnail scaling
├── keogram/           Keogram generation (parallel image decode, direct pixel access)
├── startrails/        Star-trails generation (max-blend stack, parallel rows)
├── timelapse/         ffmpeg video assembly
├── convert/           End-of-night PNG→WebP conversion (cwebp)
├── upload/            S3 and HTTP upload backends
├── web/               Embedded HTTP server and UI (no external dependencies)
├── whitebalance/      WB channel analysis and per-night adjustment suggestions
└── config/            YAML configuration with viper
```

## License

MIT
