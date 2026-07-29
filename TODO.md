# Nightsky — Night-Sky Photography Feature Roadmap

Feature ideas discussed and scoped but not yet built, in recommended order.
Each is sized for one or a few commits. The stability/raw/compression work
that preceded this list lives in `ENHANCEMENTS.md` (all complete).

Site context that shaped the ranking: camera at 47.9°N (aurora latitude),
Pi Zero 2 W (512 MB, thermal-constrained), IMX462 via libcamera at 1080p,
per-frame cloud metrics already collected in `internal/cloud`.

Legend: `[ ]` todo · `[~]` in progress · `[x]` done

---

## 1. Aurora / sky-anomaly alerting

- [x] **Green-channel sky metric per frame**
  Aurora reads as a broad green (557.7 nm) glow. Compute mean G/(R+B) over
  the sky region per night frame — a few lines beside the existing cloud
  metric in `internal/cloud` (or a new `internal/skymetrics`). Store in the
  same per-frame metric stream and the nightly CSV.
- [x] **Baseline + spike detection**
  Rolling median of the green ratio over the last ~30 frames; flag when the
  current frame exceeds baseline by a configurable factor AND cloud coverage
  is low (clouds + light pollution also skew green). Debounce: require N
  consecutive anomalous frames before alerting, one alert per night max
  unless it clears and re-triggers.
- [x] **Push notification**
  Simple webhook POST (ntfy.sh works with zero infrastructure: one URL,
  phone app) with the trigger frame's `/latest` URL. Config block:
  `alerts: { webhook_url, aurora: { enabled, ratio_threshold, min_frames } }`.
  Reuse `upload.WithRetry` for delivery.
  _Why first: at this latitude, "wake up, there's aurora" is the single most
  useful thing this camera can do._

## 2. Moon awareness

- [x] **Moon position + phase in `internal/astro`**
  Add `MoonPosition(t, lat, lon)` (alt/az) and `MoonPhase(t)` (illuminated
  fraction) using standard low-precision algorithms (Meeus truncated series —
  ~0.3° accuracy is ample). Pure functions, unit-testable against known
  ephemeris values.
- [x] **Moon-compensated auto-exposure**
  When the moon is up and >40% illuminated, raise the night target
  brightness proportionally (config: `night.moon_target_boost`) so
  auto-exposure stops fighting moonlight with maximum gain.
- [x] **Moon-aware star trails + keogram annotation**
  Skip star-trails generation when the moon was up for most of the night
  (replaces the crude brightness-threshold failure with an informed
  decision and a clear log line). Annotate the keogram/WB report with
  moonrise/moonset times and phase.
  _Partial: the `skip_moonlit` gate is done; keogram/WB-report moonrise/set
  annotation is still open._
- [x] **Dashboard**: moon phase + rise/set on the Configuration or Live tab.

## 3. Star count / sky quality metric + focus aid

- [x] **Star detection on night frames**
  Local-maxima count above background on the stretched grayscale frame
  (threshold = median + k·MAD, 3×3 maxima, minimum separation). Runs on the
  already-decoded frame in the capture loop every Nth frame; count stored in
  metrics + nightly CSV.
- [ ] **Sky-quality trend**
  Star count vs. time plot per night (aligns with the keogram); a
  transparency/quality score per night in the captures list — answers "was
  Tuesday actually clear?" at a glance.
- [x] **Live focus aid in the web UI**
  Focus mode on the Live tab: zoomed center crop + live star count + mean
  star FWHM (sharpness). Maximize stars / minimize FWHM while turning the
  lens. All-sky lenses are notoriously hard to focus; this makes it a
  2-minute job. Needs a lightweight `/api/focus` endpoint sampling the
  latest frame.
  _Partial: `/api/focus` and live stars/FWHM rows are done; a dedicated
  zoomed-crop focus mode on the Live tab is still open._

## 4. Nightly summary + notification

- [x] **Best-frames selection at dawn**
  Rank the night's frames by star count (high), cloud coverage (low), and
  pick the top N as `highlights-YYYY-MM-DD/` symlinks or a JSON manifest the
  web UI renders as a "Highlights" strip on the Captures tab.
- [x] **Dawn summary webhook**
  One message after end-of-night processing: frames captured, hours of
  clear sky (from cloud metrics), min/max sensor temp, star-count peak,
  aurora events if any, plus keogram thumbnail and timelapse link. Same
  webhook config as aurora alerts. Turns the camera from "thing you check"
  into "thing that reports".

## 5. Meteor / streak detection (ambitious)

- [ ] **Frame differencing pipeline**
  Difference consecutive night frames; threshold; Hough (or RANSAC line
  fit) on the residual for straight streaks. Distinguish meteors (single
  frame, fast) from satellites (multi-frame, consistent velocity) and
  planes (blinking, slow). CPU cost is the concern on the Zero 2 — consider
  running at reduced resolution, or as a post-night batch during the day
  rather than live.
- [ ] **Event gallery**
  Save flagged frames + a cropped animation per event to
  `events-YYYY-MM-DD/`; surface in the web UI as an Events tab; include the
  count in the dawn summary. Expect a false-positive tuning period.

---

## Deferred / prerequisites parked earlier

- **Raw master flats** — flat capture doesn't save DNG sidecars yet;
  raw-pipeline flat correction currently falls back to the RGB flat after
  debayer (see ENHANCEMENTS.md Phase 4 notes).
- **Raw pipeline hardware verification** — `raw_calibration: true` end-to-end
  on the Pi against real night frames: dark library capture with DNGs,
  target_brightness retune, visual comparison vs. the ISP pipeline.
- **Colour-correction matrix for the debayer** — colours currently
  approximate the ISP output; a CCM from the sensor's calibration data would
  close the gap. Only worth it once raw calibration is in nightly use.
