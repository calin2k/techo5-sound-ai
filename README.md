# techo5-sound-ai — On-Device Room Sound Classifier Add-On for TECHO5

[![CI](https://github.com/calin2k/techo5-sound-ai/actions/workflows/ci.yml/badge.svg)](https://github.com/calin2k/techo5-sound-ai/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/calin2k/techo5-sound-ai)](https://github.com/calin2k/techo5-sound-ai/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.22%2B%20(zero%20CGO)-00ADD8)](https://go.dev/)
[![Arch](https://img.shields.io/badge/Target-linux%2Farmv7%20(MT8163)-blue)](#project-metadata--resource-profile)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](https://opensource.org/licenses/MIT)

`techo5-sound-ai` is a standalone, zero-CGO on-device **Google YAMNet (521 AudioSet classes)** room sound classifier and Home Assistant alert add-on for [TECHO5](https://github.com/HuskerMinion/techo5) devices (Amazon Echo Show 5 Gen 1 & 2, Echo Show 8 Gen 1 & 2, Echo Spot, and Echo Dot).

It installs entirely onto `/data/techo5-sound-ai`, makes **zero changes** to the read-only TECHO5 root filesystem, and **survives all TECHO5 OTA firmware updates** via TECHO5's native `/data/techo5-linux/rc.local` and `External ASP` (`ASP1`) interfaces.

> **Safety Notice:** Acoustic event recognition (including smoke/fire alarm and glass-break classes) is an informational smart-home convenience feature and is **not** a UL/EN-certified life-safety detector. Never use it as a substitute for certified smoke, CO, or intrusion alarms.

---

## Why This Project Exists (An Honest Open-Source Proposition)

### The Value
When you flash [TECHO5](https://github.com/HuskerMinion/techo5) onto an Amazon Echo device, you gain a local, cloud-free Home Assistant voice satellite and media player whose microphone array is already capturing room audio 24/7. However, everything that isn't a wake-word is thrown away.

`techo5-sound-ai` turns that idle microphone stream into a **100% local ambient sound sensor** without touching TECHO5's firmware or degrading wake-word responsiveness:

- **Zero Cloud, Zero External Dependencies:** Runs Google's `yamnet.tflite` (`521` classes mapped into `21` household categories such as `Doorbell`, `Baby cry`, `Dog`, `Cat`, `Glass breaking`, `Siren`, `Smoke / fire alarm`, `Appliance beep`, `Knock`, `Cough`) directly on the device CPU. No audio or telemetry ever leaves your LAN.
- **Non-Intrusive `ASP1` Audio Tee:** Hooks into `/data/techo5-linux/external-asp/bin/external-asp`. If you already use an upstream `external-asp` binary, `techo5-sound-ai` preserves it as `external-asp.orig`, chains to it transparently, and tees `16 kHz` mono PCM frames over a non-blocking Unix datagram socket (`/run/techo5-sound-ai.sock`). If no upstream `external-asp` exists, it performs boxcar anti-aliased `48 kHz -> 16 kHz` decimation and 64-tap NLMS acoustic echo cancellation in **`8.2 µs` per `20 ms` frame** with **`0` heap allocations**.
- **Turnkey Home Assistant Integration:** Built-in **MQTT v3.1.1 client with Home Assistant Auto-Discovery** (automatically registers `sensor.<device>_room_sound` and `sensor.<device>_last_sound_alert`), plus direct **Home Assistant Webhook** and **REST Service** (`notify.*`, `persistent_notification.create`) push alerts and a self-contained Web UI on port `:8182`.

### Honest Limitations (What It Is Not)
- **Not a life-safety device:** Microphone placement, room acoustics, and background media playback affect classification confidence.
- **Single-window acoustic context (`975 ms`):** YAMNet scores `0.975 s` windows (`0.48 s` hop). Loud overlapping sounds (e.g., loud music or TV dialogue masking a distant knock) can lower confidence below your alert threshold, though multi-hop hysteresis and emergency-class boost help mitigate transient noise.
- **Embedded CPU constraints:** On a quad-core Cortex-A35 (`MT8163`), a full 28-layer MobileNetV1 pass takes `~360 ms` with the stock `float32` YAMNet model or `~45 ms` with an `int8` quantized TFLite model. In a quiet room, an **adaptive noise-floor energy gate** skips inference automatically, keeping idle CPU usage at **`~0.3%`** of the SoC.

---

## Project Metadata & Resource Profile

| Property | Specification / Measured Value |
| :--- | :--- |
| **Target Hardware** | TECHO5 devices (`ARMv7` / `GOARM=7`, tested on Echo Show 5 Gen 1 `checkers` `MT8163`) |
| **Binary & Dependencies** | Single static ELF (`~9.5 MB` incl. TLS stack, `CGO_ENABLED=0`, `0` third-party Go modules) |
| **Rootfs (`/`) Footprint** | **`0 bytes`** — resides in `/data/techo5-sound-ai` and `/data/misc/techo5/yamnet` |
| **RAM Footprint** | **`< 1.5 MB` RSS** — TFLite FlatBuffer is mapped read-only via `mmap(PROT_READ, MAP_SHARED)` |
| **Audio Input (`ASP1`)** | `16–48 kHz`, `1–16` channels, `S16_LE` / `S32_LE` / `F32_LE` -> `16 kHz` mono (`50 Hz` frame rate) |
| **Idle CPU (Quiet Room)** | **`~1.2%` of 1 core (`~0.3%` total SoC)** via adaptive EMA noise-floor gate (`-68 dBFS` / `+4.5 dB`) |
| **Active Inference Time** | **`~45 ms`** (`int8` TFLite w/ ARMv7 `MULA` assembly `dot4_arm.s`) / **`~360 ms`** (stock `float32` YAMNet) |
| **Web UI & REST API** | `http://<device-ip>:8182/` (`GET /api/status`, `POST /api/config`, `POST /api/model/download`, `POST /api/test-notify`) |
| **Daemon Reliability** | Kernel `flock` liveness probe (`/run/techo5-sound-ai.lock`) + `256 KiB` bounded log rotation |

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
2. **Download the YAMNet Model:** Open **`http://<device-ip>:8182/`** and click **Download Google YAMNet (3.7 MB)** (or upload a local `.tflite` model). If `/data/sound-ai/yamnet.tflite` already exists on the device, it is adopted automatically on first start.
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
| `internal/addon` | **`82.6%`** | `16 kHz` ring buffer, adaptive EMA noise-floor energy gate, concurrent-safe `mmap` hot-reload, atomic config/model persistence, HTTP API & security headers |
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
- **QEMU ARMv7 Assembly Verification (`internal/sound/dot4_arm.s`):**
  - CI cross-compiles `internal/sound` with `GOOS=linux GOARCH=arm GOARM=7` and executes `TestDot4I8BoundaryAndTailLengths` under `qemu-arm`, verifying exact bit-for-bit parity between the 4x-unrolled ARM `MULA` assembly loop and the pure-Go reference across all vector lengths `0..64` (including `n % 4 != 0` tail paths and `-128 × -128` saturation).
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

# Build static ARMv7 binary for TECHO5
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -ldflags="-s -w" -o techo5-sound-ai-linux-arm ./cmd/techo5-sound-ai
```

---

## License

[MIT](LICENSE)
