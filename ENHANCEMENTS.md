# Nightsky Enhancement Plan

Sequenced work to make capture more stable/efficient and to deliver the three
priorities: **raw**, **darks**, and **best-compression video**. Each step is a
single self-contained git commit. Steps are ordered so that correctness fixes
land before the larger raw-space rework that depends on them.

Legend: `[ ]` todo · `[~]` in progress · `[x]` done

---

## Phase 1 — Dark frame correctness

The dark pipeline is the biggest correctness gap under `auto_exposure: true`:
darks are keyed by the *configured base* exposure/gain and loaded once per mode
transition, so they only match the first frame of the night. Dark current
scales with exposure and gain, so most of the night subtracts the wrong dark.

- [x] **Step 1 — Median-stack dark frames instead of mean**
  `averageFrames` in `internal/capture/darkframe.go` uses a pixel-wise mean, so
  a single satellite/plane/cosmic-ray hit in any dark contaminates the master.
  Replace with a per-pixel median (keep mean as a fallback for count < 3).
  - Files: `internal/capture/darkframe.go`
  - Tests: synthetic frames with one outlier pixel → median rejects it.
  - **Commit:** `Median-stack dark frames to reject outlier pixels`

- [x] **Step 2 — Match darks to actual capture settings, not config base**
  Load/select the dark using `result.Meta.Exposure` / `result.Meta.Gain`
  (the values libcamera actually used) rather than the configured base. When no
  close match exists, log a warning and skip subtraction instead of silently
  subtracting a mismatched frame.
  - Files: `internal/capture/loop.go`, `internal/capture/darkframe.go`
  - Tests: nearest-match selection; no-match path skips subtraction.
  - **Commit:** `Match dark frames to actual per-frame exposure and gain`

- [x] **Step 3 — Capture a dark library across the auto-exposure grid**
  Extend `nightsky dark` to sweep the exposure/gain combinations the controller
  can visit at night (e.g. 1/2/4/8/15.5s × a few gains) and store one master
  dark per grid point. `LoadDark` picks the nearest grid point (combined with
  Step 2). Document the grid in the config/README.
  - Files: `cmd/nightsky/main.go` (dark cmd), `internal/capture/darkframe.go`,
    `README.md`, `configs/nightsky.example.yaml`
  - Tests: nearest-grid-point selection across a populated library dir.
  - **Commit:** `Capture and select darks across an exposure/gain library`

---

## Phase 2 — Video compression

The encoder already supports AV1 (the CRF / `-b:v 0` handling is AV1-aware) but
nothing exposes `-preset`, GOP, or tune — so AV1/x265 run at encoder defaults.
For an all-sky timelapse (large flat dark regions, slow motion) AV1 at a tuned
preset is the highest-leverage size win.

- [x] **Step 4 — Expose encoder preset, keyframe interval, and tune**
  Add `preset`, `gop` (keyframe interval), and optional `tune` to
  `TimelapseConfig` and the `timelapse`/`process` command flags; thread them into
  `encodeImages`. Sensible defaults that don't change current output when unset.
  - Files: `internal/config/config.go`, `internal/timelapse/ffmpeg.go`,
    `cmd/nightsky/main.go`
  - Tests: arg-builder includes the flags only when set; defaults unchanged.
  - **Commit:** `Expose ffmpeg preset, GOP, and tune for timelapse encoding`

- [x] **Step 5 — Document AV1/SVT-AV1 as the archival codec**
  Add `libsvtav1` guidance to the README and example config: recommended CRF
  range (~30–35), preset trade-off, and the existing
  `deflicker` ⇒ `segment_frames: 0` caveat for best-quality runs.
  - Files: `README.md`, `configs/nightsky.example.yaml`
  - **Commit:** `Document SVT-AV1 as the recommended timelapse codec`

---

## Phase 3 — Capture-path efficiency

- [x] **Step 6 — Direct `.Pix` access in hot per-pixel loops**
  `SubtractDark`, `averageFrames`, and `computeMeanBrightness` call
  `image.At()` per pixel — millions of interface dispatches per 4056×3040 frame.
  Type-assert to `*image.RGBA` / `*image.NRGBA` and walk `.Pix` directly, with
  the generic `At()` path kept as a fallback for other image types.
  - Files: `internal/capture/darkframe.go`,
    `internal/camera/libcamera/camera.go`
  - Tests: results identical to the `At()` implementation on a known image.
  - **Commit:** `Use direct pixel access in capture hot loops`

- [x] **Step 7 — Sweep stale `*.tmp` frame files on startup**
  The atomic frame/DNG write (`writeFileAtomic`) can leave `*.tmp` behind if the
  process is killed mid-write. Add a startup sweep of the output tree, mirroring
  `removeStaleTmp` from the timelapse package.
  - Files: `internal/capture/loop.go`
  - Tests: stale `.tmp` removed, real frames untouched.
  - **Commit:** `Remove stale partial-frame .tmp files on startup`

- [x] **Step 8 — Cache last-seen sensor temperature (libcamera)**
  `Temperature()` always returns 0 for libcamera; temp is only present in
  per-capture metadata. Cache the last parsed value so metrics and any
  temperature-aware logic see a real number between captures.
  - Files: `internal/camera/libcamera/camera.go`
  - Tests: cached value returned after a capture populates metadata.
  - **Commit:** `Cache last-seen sensor temperature for libcamera`

---

## Phase 4 — Raw-space calibration (larger; scope before starting)

Today `save_raw` only writes the DNG to disk; the whole pipeline (dark, flat,
stretch, overlay, timelapse) runs on the 8-bit decoded JPEG/PNG. Real
calibration happens on the linear Bayer raw. This phase pulls in a DNG/Bayer
decoder and is a meaningful dependency decision — confirm scope first.

- [ ] **Step 9 — Decode DNG to linear Bayer/16-bit**
  Add a raw decode path (library or minimal DNG reader) producing linear sensor
  data. Behind a config flag; no behavior change when disabled.
  - Files: new `internal/raw/` package, `internal/config/config.go`
  - Tests: decode a sample DNG; dimensions/bit-depth correct.
  - **Commit:** `Add DNG raw decode to linear sensor data`

- [ ] **Step 10 — Dark-subtract and flat-divide in raw space**
  Apply calibration on the linear raw before debayer/stretch, where the linear
  subtraction model holds and 12-bit headroom is preserved.
  - Files: `internal/raw/`, `internal/capture/loop.go`
  - Tests: synthetic raw + dark → expected linear result.
  - **Commit:** `Calibrate dark and flat in linear raw space`

- [ ] **Step 11 — Raw-derived auto-exposure metering**
  Compute metered brightness from the linear raw instead of the gamma-encoded
  display frame so auto-exposure targets are physically meaningful.
  - Files: `internal/capture/metering.go`, `internal/capture/loop.go`
  - Tests: metering on linear vs gamma frame differs as expected.
  - **Commit:** `Meter auto-exposure from linear raw data`

---

## Suggested execution order

Phases 1 → 2 → 3 are independent and low-risk; do them in order (Steps 1–8),
one commit each. Phase 4 (Steps 9–11) is the large raw-space rework — confirm
the DNG-decoder dependency before starting, then proceed Step 9 → 11.
