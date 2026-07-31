# Nightsky

A lightweight, CLI-configured all-sky camera service for long-exposure night photography and timelapse generation. Captures images continuously, automatically switching between day and night modes based on sun position, and uploads results to S3 or HTTP endpoints.

Inspired by [AllskyTeam/allsky](https://github.com/AllskyTeam/allsky) but stripped down to the essentials — no PHP, no Node. Configure everything from the command line or a single YAML file.

## Features

- **ZWO ASI camera support** — Full control via CGo bindings to the ASI SDK: exposure up to 300s, gain, white balance, binning (1x/2x/4x), flip, TEC cooler, temperature readout, USB bandwidth control
- **Raspberry Pi camera support** — Wraps `rpicam-still` / `libcamera-still` for CSI cameras (HQ IMX477, Module 3 IMX708, and others)
- **Automatic day/night switching** — Built-in NOAA solar position algorithm determines day/night based on your coordinates and a configurable sun altitude angle (civil, nautical, or astronomical twilight)
- **Auto-exposure** — Logarithmic exposure level algorithm that adjusts exposure and gain to maintain target brightness, with anti-oscillation detection
- **Dark frame subtraction** — Capture, average, and subtract calibration frames to remove hot pixels and fixed-pattern noise
- **DNG raw capture** — Optionally save a DNG file alongside each captured image (libcamera only), preserving full sensor bit depth and Bayer pattern for post-processing in Lightroom, darktable, or RawTherapee
- **Timelapse generation** — Assembles each night's images into an MP4 via ffmpeg with CRF quality mode, optional deflicker filter, and smooth Holy Grail day/night exposure transitions
- **Keogram** — Single-image summary of the night: center column from each frame stitched left-to-right so clouds, aurora, and milky way transits are visible at a glance
- **Star trails** — Max-blend stack of all night frames, keeping the brightest pixel seen at each position across the full night
- **White balance analysis** — Samples images at end of night, measures mean R/G/B channel values, and writes a plain-text report (`wb-analysis-<date>.txt`) with suggested WB red/blue adjustments
- **WebP conversion** — Converts captured PNGs to WebP at end of night for long-term storage; configurable quality and optional deletion of originals (requires `cwebp`)
- **S3 upload** — AWS S3 with support for custom endpoints (Backblaze B2, MinIO, etc.)
- **HTTP upload** — POST images/videos to any HTTP endpoint with optional auth
- **Metadata overlay** — Timestamp, exposure, gain, and sensor temperature rendered directly on images
- **Automatic cleanup** — Space-based (`min_free_gb`) or time-based (`days_to_keep`) full-directory deletion; or `prune_raw_after_days` to delete only raw frames while keeping timelapse, keogram, star trails, and WB analysis permanently
- **Flat field correction** — Correct lens vignetting by dividing each frame by a master flat frame (point camera at a uniformly lit surface and run `nightsky flat`). Eliminates gradient falloff at frame edges.
- **Configurable metering zone** — Choose which sky region drives auto-exposure: `full` (whole frame), `center` (inner 50%-radius circle, ideal for zenith-pointing fisheye cameras), or `top` (top third, for horizon-facing cameras)
- **Histogram stretch** — Linear remap of `[black, white] → [0, 255]` applied to saved images for contrast enhancement. Auto mode derives black/white points from configurable percentiles of frame luminance (default 10th/99.9th — tuned for dark skies); manual mode accepts explicit values. Does not affect dark/flat calibration.
- **GPS auto-location** — Automatically fetches latitude/longitude from a local `gpsd` daemon at startup (requires `gpsd`), overriding config coordinates. Falls back gracefully to configured values if no fix is available.
- **Cloud coverage metric** — Estimates cloud cover per frame from mean luminance and standard deviation of the sky region. Writes `cloud-YYYY-MM-DD.csv` at end of night alongside other synthesized outputs.
- **Aurora alerting** — Watches the sky's green ratio against a rolling baseline and pushes a webhook notification (ntfy-compatible) when a sustained green excess appears under clear skies. One alert per surge, re-armed after quiet.
- **Moon awareness** — Built-in lunar position/phase math: optionally raises the auto-exposure target while the moon is up (`moon_target_boost`), skips star trails on bright moonlit nights (`skip_moonlit`), and reports moon state in the live metrics.
- **Star metrics + focus aid** — Counts point sources and measures their sharpness (FWHM) on night frames; logged to the nightly CSV, shown live in the web UI, and served on demand at `/api/focus` for focusing the lens (maximize stars, minimize FWHM).
- **Night summary & highlights** — Optional dawn notification with the night's statistics, plus an automatic top-5 highlight selection (stars × clear sky): the best frames are copied to protected `highlight-N-*` names that survive raw pruning and shown on a dedicated Highlights tab in the web gallery.
- **Live `/latest` endpoint** — `GET /latest` on the web server always returns the most recently captured image. Supports `?w=N` for on-the-fly resizing. Useful for embedding a live view in external dashboards.
- **Live metrics endpoint** — `GET /api/metrics` returns the current capture state as JSON (mode, exposure, gain, mean brightness, cloud coverage, sensor temperature, frame count). Written to `.metrics.json` after every frame so it works across process boundaries in Docker Compose and systemd split-service deployments.
- **Web UI** — Built-in HTTP server (`nightsky serve`) with Configuration, Captures, and Live tabs. Captures tab: browse by date with Video, Keogram, Star Trails, and Images sub-tabs, paginated lazy-loaded thumbnails. Live tab: auto-refreshing current frame with real-time metrics sidebar (exposure, gain, brightness, cloud coverage bar, sensor temperature). No external dependencies, embedded in the binary.
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
- **gpsd** — for GPS auto-location (only if `location.gps: true`; `apt-get install gpsd`)

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
Type=notify
User=pi
ExecStart=/usr/local/bin/nightsky capture --config /etc/nightsky/nightsky.yaml
Restart=always
RestartSec=10
# The capture loop sends a watchdog heartbeat every frame; if the process
# wedges (camera driver stall, kernel hiccup) systemd kills and restarts it.
# Must comfortably exceed the longest frame cycle (max exposure + capture
# grace + retry backoff) — 10 minutes is safe for exposures up to 60s.
WatchdogSec=600
# Creates /run/nightsky (tmpfs) for the live metrics file; pair with
# metrics_dir: /run/nightsky in nightsky.yaml to keep the per-frame
# .metrics.json rewrite off the SD card.
RuntimeDirectory=nightsky
# Contain worst-case memory inside this service (ffmpeg children included)
# so an OOM kill lands here — where Restart=always recovers it — rather
# than on a random system process. Size for your board; 350M suits a
# 512 MB Pi Zero 2 alongside memory_limit_mb: 250 in nightsky.yaml.
MemoryMax=350M

[Install]
WantedBy=multi-user.target
```

Night sessions are named by the night's start date, so a mid-night watchdog restart resumes writing into the same directory.

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

On low-memory boards, set a top-level `memory_limit_mb` (e.g. `250` on a 512 MB Pi Zero 2) — it installs a soft Go heap limit so the garbage collector frees memory aggressively before the kernel OOM killer gets involved, leaving headroom for ffmpeg during end-of-night encoding. The `GOMEMLIMIT` environment variable takes precedence when set.

On thermally-constrained boards, also set a top-level `thermal_limit_c` (e.g. `70`) — every heavy processing step (timelapse, keogram, star trails, WebP conversion) defers until the SoC cools below the limit, so end-of-night work never stacks heat onto an already-hot chip. See the codec notes under Output for details.

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
  # GPS auto-location: override lat/lon from gpsd at startup
  gps: false
  gps_addr: "localhost:2947"
```

When `gps: true`, a fix is fetched from `gpsd` at startup and overrides `latitude`/`longitude`. Requires `gpsd` to be installed (`apt-get install gpsd`).

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
  metering_zone: full

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
  metering_zone: center   # "full", "center" (zenith), or "top"
  save_raw: false         # Save a DNG alongside each image (libcamera only)
  cooler_enabled: false   # ZWO TEC cooler
  cooler_target: -10      # Target temperature (°C)
```

### Output

```yaml
output:
  directory: ./output       # Base directory; images saved to <dir>/YYYY-MM-DD/
  metrics_dir: ""           # Live metrics file location; use tmpfs (e.g. /run/nightsky)
                            # to avoid per-frame SD writes. "" = output directory.
  filename_prefix: allsky   # Filename prefix for captured images
  # Disk space cleanup — choose one strategy (or leave both at 0 to keep everything):
  min_free_gb: 5            # Space-based (recommended): delete oldest nights when disk is low
  days_to_keep: 0           # Time-based: delete directories older than N days (0 = disabled)
  overlay: true             # Render timestamp/metadata on images
  overlay_font_size: 24

  timelapse:
    enabled: true
    fps: 25
    codec: libx264          # libx264, libx265, libsvtav1, or libaom-av1
    crf: 20                 # constant-rate-factor quality (0 = use bitrate instead)
    bitrate: 2000k          # used only when crf: 0
    deflicker: true         # smooth per-frame brightness variation
    preset: ""              # encoder preset (see codec notes below); "" = default
    gop: 0                  # max keyframe interval in frames; 0 = encoder default
    tune: ""                # encoder -tune value (codec-specific); "" = none
    threads: 0              # cap encoder threads (2 recommended on Pi Zero 2); 0 = default
    pix_fmt: ""             # "" = yuv420p (8-bit); yuv420p10le = 10-bit for AV1/x265
    # Iterative mode: encode a video segment every N captured frames during
    # the night, then concatenate segments at dawn without re-encoding.
    # Spreads encoding cost across the night instead of one big job at dawn.
    # 0 = encode the whole night at once. Note: deflicker only smooths within
    # a segment, so use segment_frames: 0 for full-night deflicker.
    segment_frames: 0

  # Histogram stretch applied to saved images (does not affect calibration)
  stretch:
    enabled: false
    mode: auto        # "auto" (percentile-based) or "manual"

    # Auto mode — tune percentiles for your sky conditions:
    #   auto_black_percentile: higher = sky goes darker = more stars visible.
    #     Dark sites (Bortle 1-3): 10–15. Light-polluted sites: 2–5.
    #   auto_white_percentile: higher = more headroom before bright stars clip.
    auto_black_percentile: 10    # 10th percentile — good default for dark skies
    auto_white_percentile: 99.9  # 99.9th percentile — preserves star brightness

    # Manual mode — explicit input levels mapped to black and white:
    black_point: 15   # input level mapped to black
    white_point: 220  # input level mapped to white

  # Prune raw frames after N days, keeping synthesized outputs (0 = disabled).
  # Combine with days_to_keep to remove entire directories after even longer.
  prune_raw_after_days: 0

  # EXPERIMENTAL: single switch for raw mode. true = capture DNGs (implied,
  # both modes) and process each frame from the linear raw (see "Raw
  # calibration pipeline" below). false = run exactly as before; none of the
  # raw pipeline code executes.
  raw_calibration: false

  # Requires: apt-get install webp
  webp:
    enabled: false
    quality: 85             # 0–100 lossy quality
    delete_originals: false # remove source PNG after conversion
```

#### Raw calibration pipeline (experimental)

`output.raw_calibration` is a **single switch** that selects how the camera runs:

- `false` (default) — capture runs exactly as it always has; none of the raw pipeline code executes.
- `true` — DNG capture is turned on automatically in both modes (no separate `save_raw` needed) and frames are processed from the **linear 12-bit DNG** rather than the gamma-encoded 8-bit ISP output: the DNG is decoded, the nearest linear master dark (`darkraw_*.png`, produced by `nightsky dark` from DNG sidecars) is subtracted, auto-exposure is metered from the linear data, and the frame is debayered with the mode's `wb_red`/`wb_blue` gains and gamma 2.2. Flat-field correction, stretch, and overlay then apply as usual.

Calibrating in linear space is where dark subtraction is physically valid — hot pixels and dark current subtract exactly instead of approximately on gamma-encoded pixels. Caveats:

- Requires the `libcamera` backend (DNG capture); the capture command warns at startup otherwise. `save_raw` remains available independently for keeping DNG sidecars without raw processing.
- **Fail-safe**: any per-frame error (missing DNG, decode failure) falls back to the standard 8-bit image with a warning.
- The linear metered mean reads darker than the display-image mean for the same scene — retune `target_brightness` when enabling.
- With `awb: true`, libcamera's dynamic gains aren't recorded in the DNG, so the configured `wb_red`/`wb_blue` are used; no colour-correction matrix is applied, so colours approximate the ISP output.
- Raw master *flats* are not yet supported; the RGB flat applies after debayer.

#### Choosing a video codec

All-sky timelapses are unusually compressible — large flat dark regions and slow, smooth motion — so the codec and preset matter more than for ordinary video.

- **`libsvtav1` (recommended for archival).** SVT-AV1 gives the smallest files at equal quality and is well-threaded for multi-core encoding. Start with `crf: 32` (range ~30–35; lower = higher quality/larger) and a `preset` of `6`–`8` (lower number = slower and smaller; `4` if you have time to spare on a Pi). AV1's constant-quality mode is wired up automatically (`-b:v 0` alongside `-crf`).
- **`libx265`.** Smaller than x264 with broad-enough playback support. `crf: 24`–`28`, `preset: slow`.
- **`libx264` (default, most compatible).** Plays everywhere. `crf: 18`–`23`, `preset: slow`.

A long keyframe interval helps a static-camera timelapse: try `gop: 250` (≈10 s at 25 fps). Encodes run at idle CPU priority (`nice -n 19`) so they don't starve capture.

On low-memory boards (Pi Zero 2, 512 MB) also set `threads: 2` — it caps the encoder's thread pool (`-threads`, plus `lp=2` for SVT-AV1), roughly halving peak encode memory and softening the all-core CPU/power spike that can brown out a marginal supply. Combine with `segment_frames` so the night is encoded in small chunks rather than one large dawn job.

For enclosures that run hot (summer sun on an all-sky dome), set a top-level `thermal_limit_c: 70` — every heavy processing step (timelapse encode, keogram, star trails, WebP conversion) then waits for the SoC to cool below the limit before starting, rechecking every 2 minutes for up to 30 minutes per step, instead of piling processing heat onto an already-hot chip. Sustained heat triggers CPU frequency capping and destabilizes the Pi Zero 2's SDIO WiFi. A `timelapse.thermal_limit_c` can override the limit for the encoder specifically; it inherits the global value when unset.

Note: `deflicker` only smooths brightness *within* a single encode. With `segment_frames > 0` the night is encoded in independent segments and stream-copy concatenated, so brightness steps at segment boundaries are not smoothed — use `segment_frames: 0` when you want full-night deflicker.

**Color and bit depth.** Every encode explicitly converts to and tags **BT.709 limited range** (via `scale` + `setparams`), so the encode math and the container metadata agree — untagged output would be converted with BT.601 coefficients but decoded as BT.709 by most players, subtly shifting saturated hues like aurora green. For AV1/x265 archival encodes, `pix_fmt: yuv420p10le` enables **10-bit** output: dark-sky gradient banding largely disappears at similar file size, at the cost of playback on older hardware decoders.

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

### Alerts

```yaml
alerts:
  webhook_url: https://ntfy.sh/my-secret-topic   # empty = alerts disabled
  base_url: http://astrocam:8080                 # optional, for links in messages
  night_summary: true    # dawn message: frames, clear-sky stats, peak stars, aurora events
  aurora:
    enabled: true
    ratio_threshold: 1.3 # green ratio must exceed its rolling baseline × this
    min_frames: 3        # consecutive anomalous frames before alerting
    max_cloud: 0.5       # suppress detection above this cloud coverage
```

The wire format is ntfy-compatible (message body + `Title` header): create a topic at [ntfy.sh](https://ntfy.sh), put its URL in `webhook_url`, and install the ntfy phone app — "possible aurora" pushes arrive within seconds of detection. Any endpoint that accepts a plain HTTP POST works. The webhook URL is redacted in `/api/config` since ntfy topics are effectively secrets.

### Dark frames

```yaml
dark:
  enabled: false        # Enable dark frame subtraction during capture
  directory: ./darks    # Where dark frames are stored
  count: 5              # Frames to average when capturing darks
```

### Flat field correction

```yaml
flat:
  enabled: false          # Apply flat-field correction during capture
  directory: ./flats      # Where the master flat frame is stored
  count: 10               # Frames to average when capturing
```

Capture a master flat by pointing the camera at a uniformly lit surface (overcast sky, white screen, or T-shirt over lens) and running:

```bash
nightsky flat
```

The master flat is saved to `flat.directory/master_flat.png` and loaded automatically when `flat.enabled: true`.

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
  flat        Capture flat frames for vignetting correction
  analyze     Analyse images and suggest white balance settings
  process     Run end-of-night processing on a capture directory
  webp        Convert captured PNGs to WebP
  info        Display camera information
  clean       Remove old capture directories
  version     Print version

Global Flags:
  -c, --config string      Path to config file (default: ./nightsky.yaml)
  -l, --log-level string   Log level: debug, info, warn, error (default "info")
```

### `nightsky capture`

Runs the main capture loop. Automatically detects day/night based on sun position and applies the corresponding settings. All images from a single night session are kept in one directory named after the night's start date, even when captures cross midnight.

At the end of each night the synthesis suite runs sequentially (one memory-heavy job at a time), quick outputs first: keogram, star trails, white balance analysis, then the timelapse encode (uploaded in the background when configured), then WebP conversion, and finally disk cleanup. Day capture continues concurrently — end-of-night processing runs in the background.

```bash
nightsky capture
nightsky capture --config /etc/nightsky/nightsky.yaml
nightsky capture --log-level debug
```

### `nightsky serve`

Starts the web UI server. Reads from the configured output directory — does not require a camera to be connected.

```bash
nightsky serve
nightsky serve --addr :8080
nightsky serve --config /etc/nightsky/nightsky.yaml --addr 0.0.0.0:8080
```

The UI is served at `http://localhost:8080` by default. It is embedded in the binary with no external dependencies. Three tabs are available:

- **Configuration** — current config values rendered as a read-only dashboard
- **Captures** — browse nights via a calendar date picker; sub-tabs for Video, Keogram, Star Trails, Highlights, and Images with paginated lazy-loaded thumbnails
- **Live** — auto-refreshing current frame (polls `/latest` every 5 s) alongside a real-time metrics sidebar showing mode, exposure, gain, brightness, cloud coverage, and sensor temperature; shows "capture not running" when the capture loop is stopped

HTTP API endpoints:

| Endpoint | Description |
|---|---|
| `GET /latest` | Most recent captured image; `?w=N` resizes |
| `GET /api/metrics` | Live capture state as JSON (mode, exposure, gain, brightness, cloud coverage, stars, moon, temp, frame count) |
| `GET /api/focus` | Star count + mean FWHM of the latest frame (live focus aid; cached per frame) |
| `GET /api/captures` | List of capture date directories |
| `GET /api/captures/{date}/images` | Images, video, keogram, star trails for a date |
| `GET /api/config` | Current configuration as JSON |
| `GET /api/status` | Version and camera type |

### `nightsky timelapse`

Generates a timelapse video from a directory of captured images. Settings come from the `output.timelapse` section of the config; flags override individual settings only when explicitly passed.

```bash
# Most recent date directory, using the config's timelapse settings
nightsky timelapse

# Specific directory, overriding just the codec and fps
nightsky timelapse --dir ./output/2026-03-16 --fps 30 --codec libx265
```

### `nightsky dark`

Captures a **dark library** for calibration. **Cover the camera lens before running this command.**

Because auto-exposure drifts the exposure and gain through the night, a single dark would only match the first frame. Instead, `nightsky dark` sweeps the exposure/gain grid the auto-exposure controller actually follows in each mode — an exposure ramp (doubling from the base up to `max_exposure`) at the base gain, then a gain ramp (doubling from the base up to `max_gain`) at the max exposure — and captures one master dark per grid point. During capture, each frame is matched to the nearest dark in the library (within one stop of `exposure × gain`); if none is close enough, dark subtraction is skipped for that frame rather than using a mismatched dark.

```bash
nightsky dark
```

Dark frames are saved to `dark.directory` (default `./darks/`), one PNG per grid point named `dark_<ms>ms_gain<g>_bin<n>.png`, and are selected automatically during capture when `dark.enabled` is true. For fixed-exposure modes (`auto_exposure: false`) only the single base point is captured.

### `nightsky flat`

Captures flat frames for vignetting correction. Point the camera at a uniformly lit surface (overcast sky, white T-shirt over the lens, or lightbox) before running. The frames are averaged into a master flat saved to `flat.directory`.

```bash
nightsky flat
```

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

### `nightsky process`

Runs the full end-of-night processing pipeline on an existing capture directory: timelapse, keogram, star trails, white balance analysis, and WebP conversion. Each output respects its enabled/disabled flag in the config. Useful when the capture loop was stopped before dawn and end-of-night processing never ran, or to regenerate outputs for historical directories.

```bash
# Most recent capture directory
nightsky process

# Specific directory
nightsky process --dir ./output/2026-05-14
```

### `nightsky webp`

Converts captured PNG images in a directory to WebP. Requires `cwebp` (`apt-get install webp`). Skips keogram and star-trails files. Defaults to quality and delete-originals settings from your config.

```bash
# Most recent night
nightsky webp

# Specific directory
nightsky webp --dir ./output/2026-05-14

# Override quality and delete originals
nightsky webp --dir ./output/2026-05-14 --quality 90 --delete-originals
```

This is the manual equivalent of the automatic conversion that runs at end of night when `output.webp.enabled: true`. To re-run the entire end-of-night pipeline rather than just the WebP step, use `nightsky process`.

---

## Output Structure

```
output/
├── 2026-03-15/             ← named after the night's start date (survives midnight crossings)
│   ├── allsky-20260315191503.png       ← or .webp if conversion enabled
│   ├── allsky-20260315191503.dng       ← only when save_raw: true (kept by raw pruning)
│   ├── allsky-20260315191513.png
│   ├── ...
│   ├── timelapse-2026-03-15.mp4
│   ├── keogram-2026-03-15.jpg
│   ├── startrails-2026-03-15.jpg
│   ├── wb-analysis-2026-03-15.txt
│   ├── cloud-2026-03-15.csv              ← per-frame cloud coverage metrics
│   └── .thumbs/            ← auto-generated thumbnail cache (160px JPEG)
│       ├── allsky-20260315191503_160.jpg
│       └── ...
├── 2026-03-16/
│   └── ...
.metrics.json               ← live capture state (updated every frame, read by /api/metrics)
darks/
├── dark_10000ms_gain200_bin1.png
└── dark_1ms_gain1_bin1.png
flats/
└── master_flat.png
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
├── flat/              Flat field correction — capture, normalize, apply per-frame
├── cloud/             Per-frame cloud coverage metric (mean + stddev of sky region)
├── gps/               GPS fix from gpsd (JSON streaming protocol, no external deps)
├── metrics/           Live capture state written per-frame, read by /api/metrics
└── config/            YAML configuration with viper
```

## License

MIT
