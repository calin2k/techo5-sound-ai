# techo5-sound-ai — On-Device Room Sound Classifier Add-On for TECHO5

[![CI](https://github.com/calin2k/techo5-sound-ai/actions/workflows/ci.yml/badge.svg)](https://github.com/calin2k/techo5-sound-ai/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/calin2k/techo5-sound-ai)](https://github.com/calin2k/techo5-sound-ai/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.22%2B%20(zero%20CGO)-00ADD8)](https://go.dev/)
[![Footprint](https://img.shields.io/badge/Device%20Footprint-10.1%20MiB%20total%20(0%20B%20rootfs)-success)](#on-device-storage--memory-size-audit)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](https://opensource.org/licenses/MIT)

`techo5-sound-ai` is a standalone, zero-CGO on-device **Google YAMNet (521 AudioSet classes)** room sound classifier and Home Assistant alert add-on for [TECHO5](https://github.com/HuskerMinion/techo5) devices (Amazon Echo Show 5 Gen 1 & 2, Echo Show 8 Gen 1 & 2, Echo Spot, and Echo Dot).

The entire installation — **binary + 521-class neural network model + config + logs — takes just `10.06 MiB` (`0.27%` of the `3.7 GB` `/data` partition)**, makes **zero changes (`0 bytes`)** to the read-only TECHO5 root filesystem, and **survives all TECHO5 OTA firmware updates**.

> **Safety Notice:** Acoustic event recognition (including smoke/fire alarm and glass-break classes) is an informational smart-home convenience feature and is **not** a UL/EN-certified life-safety detector. Never use it as a substitute for certified smoke, CO, or intrusion alarms.

---

## Why This Project Exists (An Honest Open-Source Proposition)

### The Value
When you flash [TECHO5](https://github.com/HuskerMinion/techo5) onto an Amazon Echo device, you gain a local, cloud-free Home Assistant voice satellite and media player whose microphone array is already capturing room audio 24/7. However, everything that isn't a wake-word is thrown away.

`techo5-sound-ai` turns that idle microphone stream into a **100% local ambient sound sensor** without touching TECHO5's firmware or degrading wake-word responsiveness:

- **Zero Cloud, Zero External Dependencies:** Runs Google's `yamnet.tflite` (`3.94 MiB`, `521` classes mapped into `21` household categories such as `Doorbell`, `Baby cry`, `Dog`, `Cat`, `Glass breaking`, `Siren`, `Smoke / fire alarm`, `Appliance beep`, `Knock`, `Cough`) directly on the device CPU. No audio or telemetry ever leaves your LAN.
- **Few-Shot "Teach Custom Sound" (`1024-D` Embeddings):** Tap YAMNet's final `1024-D` global average pooling bottleneck vector (`opMean`) to teach your own household sounds (e.g., your specific intercom buzzer, kettle whistle, washer chime, or baby monitor beep) in 1–3 clicks from the `:8182` Web UI using cosine-similarity prototype matching — with zero cloud retraining.
- **Non-Intrusive `ASP1` Audio Tee + Self-Playback Guard:** Hooks into `/data/techo5-linux/external-asp/bin/external-asp`. If you already use an upstream `external-asp` binary, `techo5-sound-ai` preserves it as `external-asp.orig`, chains to it transparently, and tees `16 kHz` mono PCM frames over a non-blocking Unix datagram socket (`/run/techo5-sound-ai.sock`). If no upstream `external-asp` exists, it performs boxcar anti-aliased `48 kHz -> 16 kHz` decimation, 64-tap NLMS acoustic echo cancellation, and `RoleREF` speaker-playback tagging (`PlaybackGuard`) in **`8.2 µs` per `20 ms` frame** with **`0` heap allocations**, suppressing false alerts while the Echo's own speaker is playing media (while still allowing `Smoke / fire alarm`, `Siren`, and `Glass breaking` alerts through).
- **Turnkey Home Assistant Integration:** Built-in **persistent MQTT v3.1.1 client with LWT availability (`online`/`offline`) and Home Assistant Auto-Discovery** (automatically registers `binary_sensor.<device>_sound_alert` with `device_class: sound` + `off_delay`, `sensor.<device>_room_sound`, and `sensor.<device>_last_sound_alert`), plus direct **Home Assistant Webhook** and **REST Service** (`notify.*`, `persistent_notification.create`) push alerts, per-category sensitivity presets, in-RAM last-alert audio playback (`/api/last-alert.wav`), and a self-contained Web UI on port `:8182`.

### Honest Limitations (What It Is Not)
- **Not a life-safety device:** Microphone placement, room acoustics, and background media playback affect classification confidence.
- **Single-window acoustic context (`975 ms`):** YAMNet scores `0.975 s` windows (`0.48 s` hop). Loud overlapping sounds (e.g., loud music or TV dialogue masking a distant knock) can lower confidence below your alert threshold, though multi-hop hysteresis and emergency-class boost help mitigate transient noise.
- **Embedded CPU constraints:** On a quad-core Cortex-A35 (`MT8163`), a full 28-layer MobileNetV1 pass takes `~360 ms` with the stock `float32` YAMNet model or `~45 ms` with an `int8` quantized TFLite model. In a quiet room, an **adaptive noise-floor energy gate** skips inference automatically, keeping idle CPU usage at **`~0.3%`** of the SoC.

---

## On-Device Storage & Memory Size Audit

A complete audit of every file added to a TECHO5 device (measured on an Echo Show 5 Gen 1 `checkers` with a `3.7 GB` `/data` userdata partition):

| On-Device Path | Size | Storage & Memory Mechanism |
| :--- | :--- | :--- |
| `/data/techo5-sound-ai/techo5-sound-ai` | **`6.13 MiB`** (`6,422,688 B`) | Single static `ARMv7` ELF (includes TFLite engine, ARMv7 `MULA` assembly, `ASP1` DSP, MQTT v3.1.1, Web UI, and TLS 1.3 client for HTTPS model download) |
| `/data/techo5-linux/external-asp/bin/external-asp` | **`0 B` extra** | Hard-linked (`ln -f`, same inode on `/data`) to `techo5-sound-ai` — passes TECHO5's `0700` root regular-file check with zero disk duplication |
| `/data/misc/techo5/yamnet/yamnet.tflite` | **`3.94 MiB`** (`4,126,810 B`) | Google YAMNet (`521` classes, `~3.2M` MobileNetV1 params; `~3.4 MiB` if using `int8` TFLite). Mapped read-only via `mmap(PROT_READ, MAP_SHARED)` (`0 B` Go heap) |
| `/data/techo5-sound-ai/config.json` | **`< 1 KiB`** | JSON alert & notification settings |
| `/data/techo5-sound-ai/sound-ai.log` | **`< 4 KiB`** (max `256 KiB`) | Automatically rotated to `.log.1` at `256 KiB` |
| `/data/techo5-linux/rc.local` | **`100 B`** | Boot hook launching `--daemon` across OTA updates |
| **Total Added to `/data` (`3.7 GB` partition)** | **`10.06 MiB`** | **Only `0.27%` of `/data`** (`3.4 GB` remains free) |
| **Total Added to TECHO5 Rootfs (`/`)** | **`0 B`** | Root filesystem remains 100% untouched |

### Is the Size Optimal? (Engineering Evaluation)
1. **55.7% Disk Reduction via Hard-Linking & Dependency Pruning (`22.70 MiB -> 10.06 MiB`):**
   - Replacing `cp -f` with a same-filesystem hard link (`ln -f`) between `/data/techo5-sound-ai/techo5-sound-ai` and `/data/techo5-linux/external-asp/bin/external-asp` eliminated `9.38 MiB` of duplicate flash storage.
   - Replacing `html/template` (which transitively linked `text/template`, `regexp`, and reflection tables) and `log/slog` with a zero-reflection `strings.Builder` HTML renderer, plus building with `-tags nethttpomithttp2 -trimpath -ldflags="-s -w"`, shrunk the static binary by **34.7%** (`9.38 MiB -> 6.13 MiB`).
   - Of the remaining `6.13 MiB` binary, `~75%` is Go's standard `net/http` + `crypto/tls` + `crypto/x509` stack required for one-click HTTPS model downloads and HTTPS Home Assistant webhooks; all `techo5-sound-ai` packages combined (`sound`, `asp`, `addon`, `main`) are **`< 140 KiB` of machine code**.
2. **Why Uncompressed ELF + Hard Link Beats UPX on 1 GB RAM Devices:**
   - Compressing the binary with UPX would shrink `/data` usage by `~3 MiB` (on a partition with `3,400 MiB` free), **but** Linux cannot share demand-paged `.text` mappings from a UPX-packed binary — both `--daemon` and `external-asp` would unpack private anonymous copies into RAM (`+12 MiB` non-evictable RSS).
   - Keeping the ELF uncompressed and hard-linked (`same inode`) allows the Linux kernel to map the `.text` and `.rodata` segments once in the page cache and share the exact same physical RAM pages across both processes.
3. **Why `3.94 MiB` for a 521-Class Audio Neural Network is Near-Minimal:**
   - YAMNet uses Howard et al.'s MobileNetV1 depthwise-separable convolutions (`3.2M` weights instead of `>25M` in standard CNNs) and `mmap`s the FlatBuffer directly from flash, consuming **`< 1.5 MB` RSS** at runtime.

---

## Project Metadata & Runtime Profile

| Property | Specification / Measured Value |
| :--- | :--- |
| **Target Hardware** | TECHO5 devices (`ARM64` / `aarch64` & `ARMv7` / `GOARM=7`, tested on Echo Show 5 Gen 1 `checkers` `MT8163`) |
| **Total On-Device Footprint** | **`9.78–10.06 MiB` on `/data`** (`5.84 MiB` `arm64` / `6.13 MiB` `arm` binary + `3.94 MiB` model), **`0 B` on `/`** |
| **RAM Footprint** | **`< 1.5 MB` RSS** — TFLite FlatBuffer is mapped read-only via `mmap(PROT_READ, MAP_SHARED)` |
| **Audio Input (`ASP1`)** | `16–48 kHz`, `1–16` channels, `S16_LE` / `S32_LE` / `F32_LE` -> `16 kHz` mono (`50 Hz` frame rate) |
| **Idle CPU (Quiet Room)** | **`~1.2%` of 1 core (`~0.3%` total SoC)** via adaptive EMA noise-floor gate (`-68 dBFS` / `+4.5 dB`) |
| **Active Inference Time** | **`~45 ms`** (`int8` TFLite w/ `dot4_arm64.s` / `dot4_arm.s` assembly) / **`278.3 ms`** (`aarch64` on `MT8163`, down from `388.4 ms`) |
| **Web UI & REST API** | `http://<device-ip>:8182/` (`GET /api/status`, `GET /api/last-alert.wav`, `POST /setup/sound`, `POST /setup/sound/custom/learn`) |
| **Daemon Reliability** | Kernel `flock` liveness probe (`/run/techo5-sound-ai.lock`) + `256 KiB` bounded log rotation + SHA-256 verified release upgrades |

---

## Installation & Setup

### Option A: One-Command Install on a TECHO5 Device (Recommended)
Run from your workstation (or directly via SSH on the device) to download the latest prebuilt ARMv7 release, configure `/data/techo5-linux/rc.local` and `external-asp`, and start the daemon:

```sh
ssh root@<device-ip> '
  mkdir -p /data/techo5-sound-ai &&
  wget -O /tmp/install.sh https://raw.githubusercontent.com/calin2k/techo5-sound-ai/main/scripts/install.sh &&
  sh /tmp/install.sh
'
```

### Option B: Build from Source & Deploy from Workstation
Requires Go `1.22+` and SSH access to `root@<device-ip>`:

```sh
git clone https://github.com/calin2k/techo5-sound-ai.git
cd techo5-sound-ai
./scripts/install.sh <device-ip>
```

### Post-Install Configuration (2 Minutes)
1. **Verify External ASP is active:** The installer automatically switches `"asp": "external"` in `/data/misc/techo5/state.json`. You can also toggle **Microphone ASP selected** (`select.<device>_microphone_asp_selected`) to **`External`** on your device page in Home Assistant.
2. **Download the YAMNet Model:** Open **`http://<device-ip>:8182/`** and click **Download Google YAMNet (4.0 MB)** (or upload a local `.tflite` model). If `/data/sound-ai/yamnet.tflite` already exists on the device, it is adopted automatically on first start.
3. **Configure Alerts:** Check the sound categories you want to be notified about, configure **MQTT (with HA Auto-Discovery)** or a **Home Assistant Webhook / REST Service**, and click **Send test notification**.

### Uninstall (Clean Rollback)
Removes `/data/techo5-sound-ai`, restores any prior `external-asp.orig`, cleans `/data/techo5-linux/rc.local`, and stops the daemon:

```sh
ssh root@<device-ip> 'wget -qO- https://raw.githubusercontent.com/calin2k/techo5-sound-ai/main/scripts/uninstall.sh | sh'
```

---

## Test Evaluation, Benchmarks & QA Results

Every push and pull request is validated in GitHub Actions across `go vet`, the Go race detector (`-race`), native fuzzing, hot-path memory benchmarks, and cross-compiled `ARMv7` execution under `qemu-arm`.

### 1. Statement Coverage (`89.5%` Core Packages)

| Package | Coverage | Scope Evaluated |
| :--- | :--- | :--- |
| `internal/sound` | **`93.4%`** | TFLite v3 FlatBuffer parser, STFT/Hann/64-Mel frontend, `int8` & `float32` Conv2D/DepthwiseConv2D/FullyConnected ops, ARMv7 `dot4` kernel, multi-hop hysteresis classifier, MQTT v3.1.1 & HA Webhook/Service notifier |
| `internal/asp` | **`88.2%`** | `ASP1` wire protocol descriptor/frame codec, boxcar anti-aliasing decimator (`48 kHz -> 16 kHz`), 64-tap NLMS echo canceller, non-blocking `unixgram` audio sink |
| `internal/addon` | **`83.1%`** | `16 kHz` ring buffer, adaptive EMA noise-floor energy gate, concurrent-safe `mmap` hot-reload, atomic config/model persistence, HTTP API & security headers |
| `cmd/techo5-sound-ai` | **`21.9%`** | Bounded `256 KiB` log rotator and kernel `flock` daemon liveness probe (`main` entrypoint excluded from unit coverage) |

### 2. Hot-Path Latency & Heap Allocation Benchmarks

Measured via `go test -bench=. -benchmem ./internal/sound ./internal/asp`:

| Benchmark | Latency (x86_64) | Latency (MT8163 ARMv7) | Heap Bytes / Op | Allocations / Op |
| :--- | :--- | :--- | :--- | :--- |
| `BenchmarkProcessorProcess` (`20 ms` 48 kHz 8-ch `ASP1` frame -> 16 kHz mono + NLMS AEC) | **`8.21 µs/op`** | **`< 65 µs/op`** | **`0 B/op`** | **`0 allocs/op`** |
| `BenchmarkScore` (Full 28-layer MobileNetV1 `int8` inference pass) | **`27.66 ms/op`** | **`~45 ms/op`** | **`256 B/op`** | **`1 alloc/op`** |

### 3. Fuzzing, Concurrency & Hardware-in-the-Loop Verification

- **4 Native Go Fuzz Targets (`internal/sound/fuzz_test.go`, `internal/asp/asp_test.go`):**
  - `FuzzLoadEngine`: Feeds mutated/corrupt FlatBuffer binaries (negative vtable offsets, out-of-bounds buffer indices, cyclic operator graphs, zero-scale quantization parameters) to verify `LoadEngineFromBytes` never panics or reads out-of-bounds.
  - `FuzzReadDescriptor` & `FuzzExtractSample`: Fuzzes `ASP1` wire headers (`> 65,536` frame caps, invalid format codes, truncated payloads).
  - `FuzzClassifyWithMin`: Fuzzes 521-class logit vectors (`NaN`, `+Inf`, `-Inf`, negative thresholds, malformed class labels).
- **QEMU ARMv7 & ARM64 Assembly Verification (`internal/sound/dot4_arm.s`, `internal/sound/dot4_arm64.s`):**
  - CI cross-compiles `internal/sound` for both `GOARCH=arm GOARM=7` and `GOARCH=arm64` and executes `TestDot4I8BoundaryAndTailLengths` under `qemu-arm` and `qemu-aarch64`, verifying exact bit-for-bit parity between the unrolled ARM `MULA` / ARM64 `MADD` assembly loops and the pure-Go reference across all vector lengths `0..64` (including `n % 8 != 0` tail paths and `-128 × -128` saturation).
- **Race Detector & Fault-Injection Suite:**
  - Verified with `go test -race ./...`, including concurrent `scoreWindow` execution during live `ReloadModel` `mmap` swaps, simulated daemon `SIGKILL` stale-socket recovery via `flock`, slow-loris HTTP timeouts, and stalled Unix datagram peers (`< 1 ms` write deadline).
- **Live Device Validation (Echo Show 5 Gen 1 `checkers`):**
  - Verified continuous **`50.0 Hz`** (`100 frames / 2.00 s`) `ASP1` ingestion with `0` dropped frames, automatic quiet-room gating at `-65.4 dBFS`, and `0` runtime errors.

### Running the Test Suite Locally

```sh
# Unit + concurrency race tests
go test -race -v ./...

# Benchmarks with heap allocation profiling
go test -bench=. -benchmem ./internal/sound ./internal/asp

# Fuzzing (e.g. FlatBuffer parser & engine loader)
go test -fuzz=FuzzLoadEngine -fuzztime=10s ./internal/sound

# Build size-optimized static ARMv7 binary for TECHO5
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -tags nethttpomithttp2 -trimpath -ldflags="-s -w" -o techo5-sound-ai-linux-arm ./cmd/techo5-sound-ai
```

---

## License

[MIT](LICENSE)
