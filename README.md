# techo5-sound-ai — On-Device Room Sound Classifier Add-On for TECHO5

`techo5-sound-ai` is a standalone, zero-CGO on-device **Google YAMNet (521 AudioSet classes)** room sound classifier and alert add-on for [TECHO5](https://github.com/HuskerMinion/techo5) devices (Amazon Echo Show 5 Gen 1 & 2, Echo Show 8 Gen 1 & 2, Echo Spot, and Echo Dot).

It lives entirely on `/data` (`/data/techo5-sound-ai`), requires **zero modifications** to the stock TECHO5 root filesystem, and **survives all TECHO5 OTA firmware updates** via TECHO5's `/data/techo5-linux/rc.local` and `External ASP` (`ASP1`) interfaces.

> **Safety Disclaimer:** Sound classification (including smoke/fire alarm and glass-break recognition) is an experimental, informational convenience feature and is **not** a certified life-safety or UL/EN smoke detection device. Never rely on it in place of dedicated, certified smoke and fire alarms.

---

## Highlights

- **100% On-Device & Private:** Runs Google's quantized `yamnet.tflite` (`3.7 MB`) locally on the device's ARM CPU. No audio ever leaves your LAN.
- **Zero CGO, Zero External Go Dependencies:** Pure Go FlatBuffer parser + MobileNetV1 depthwise-separable `int8` TFLite interpreter with an ARMv7 `MULA` 4x-unrolled dot-product assembly kernel (`internal/sound/dot4_arm.s`).
- **Tiny Footprint:**
  - **Firmware rootfs (`/`) impact:** `0 bytes`
  - **RAM usage:** `< 1 MB` heap (`yamnet.tflite` is mapped read-only from `/data/misc/techo5/yamnet/yamnet.tflite` via `mmap(PROT_READ, MAP_SHARED)`).
  - **Inference speed:** `~35–60 ms` per `480 ms` hop (`975 ms` window) on a 4-core Cortex-A35 (`MT8163`).
- **Seamless TECHO5 Audio Integration (`ASP1`):**
  - Hooks into TECHO5's stock `/data/techo5-linux/external-asp/bin/external-asp` (`ASP1` wire protocol).
  - If you already use an upstream `external-asp` binary, `techo5-sound-ai` preserves it as `external-asp.orig`, chains to it transparently, and tees the 16 kHz mono PCM stream to the classifier over a non-blocking Unix datagram socket (`/run/techo5-sound-ai.sock`).
  - If no upstream `external-asp` binary is installed, `techo5-sound-ai` mixes the raw microphone channels (with lightweight 64-tap NLMS echo cancellation against the playback loopback channels) in `< 100 µs` and feeds both TECHO5 (wake-word + STT) and the YAMNet classifier.
- **Built-in Web UI (`:8182`), Home Assistant & MQTT Auto-Discovery:**
  - Dark-themed web UI at `http://<device-ip>:8182/` for one-click model download/upload, live room sound watch, alert category selection, **Home Assistant Webhook / REST service notifications**, and **MQTT v3.1.1 with Home Assistant MQTT Auto-Discovery** (`sensor.<device>_room_sound` and `sensor.<device>_last_sound_alert`).

---

## Quick Install

### Option A: One-command install directly on a TECHO5 device (via SSH)

```sh
ssh root@<device-ip> '
  mkdir -p /data/techo5-sound-ai &&
  wget -O /tmp/install.sh https://raw.githubusercontent.com/calin2k/techo5-sound-ai/main/scripts/install.sh &&
  sh /tmp/install.sh
'
```

### Option B: Install from a workstation (cloning & building from source)

```sh
git clone https://github.com/calin2k/techo5-sound-ai.git
cd techo5-sound-ai
chmod +x scripts/install.sh
./scripts/install.sh <device-ip>
```

---

## Setup & Usage

1. **Enable External ASP in Home Assistant:**
   On your TECHO5 device page in Home Assistant, change **Microphone ASP selected** (`select.<device>_microphone_asp_selected`) from `Built-in` to **`External`**.
   *(TECHO5 will immediately launch `/data/techo5-linux/external-asp/bin/external-asp` and begin streaming 16 kHz audio frames to `techo5-sound-ai`.)*
2. **Open the Web UI (`http://<device-ip>:8182/`):**
   - Click **Download Google YAMNet (3.7 MB)** (or upload a `.tflite` file from your browser) to install `/data/misc/techo5/yamnet/yamnet.tflite`.
   - Select which household sounds should trigger an alert:
     - `Doorbell`, `Knock`, `Baby cry`, `Dog`, `Cat`, `Glass breaking`, `Siren`, `Smoke / fire alarm (informational)`, `Alarm / ring`, `Clapping`, `Finger snapping`, `Whistling`, `Cough`, `Sneeze`, `Laughter`, `Door`.
   - Configure **Home Assistant Notifications** (via a standard Home Assistant Webhook URL `http://homeassistant.local:8123/api/webhook/...` or REST service call such as `persistent_notification.create` / `notify.mobile_app_*`) and/or **MQTT + Home Assistant MQTT Auto-Discovery**.
   - Click **Send test notification** to verify end-to-end delivery.

---

## Uninstall

```sh
ssh root@<device-ip> 'wget -qO- https://raw.githubusercontent.com/calin2k/techo5-sound-ai/main/scripts/uninstall.sh | sh'
```

---

## Building & Testing

```sh
# Run all unit tests (classifier, TFLite engine, ASP1 wire protocol, MQTT broker, HA webhook/service, web UI)
go test -v ./...

# Cross-compile static ARMv7 binary for TECHO5 devices
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -ldflags="-s -w" -o techo5-sound-ai-linux-arm ./cmd/techo5-sound-ai
```

## License

MIT
