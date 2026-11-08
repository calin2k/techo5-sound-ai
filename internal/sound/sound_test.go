package sound

import (
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassifyPrioritizesActionableSounds(t *testing.T) {
	tests := []struct {
		name     string
		top      []TopClass
		wantCat  string
		wantText string
		wantAct  bool
	}{
		{
			name: "smoke alarm beats background speech",
			top: []TopClass{
				{ID: 0, Name: "Speech", Score: 0.45},
				{ID: 393, Name: "Smoke detector, smoke alarm", Score: 0.25},
				{ID: 394, Name: "Fire alarm", Score: 0.15},
			},
			wantCat:  "smoke_alarm",
			wantText: "Smoke / fire alarm",
			wantAct:  true,
		},
		{
			name: "baby cry sibling sum",
			top: []TopClass{
				{ID: 19, Name: "Crying, sobbing", Score: 0.19},
				{ID: 20, Name: "Baby cry, infant cry", Score: 0.16},
			},
			wantCat:  "baby_cry",
			wantText: "Baby cry",
			wantAct:  true,
		},
		{
			name: "glass breaking",
			top: []TopClass{
				{ID: 435, Name: "Glass", Score: 0.24},
				{ID: 437, Name: "Shatter", Score: 0.18},
			},
			wantCat:  "glass",
			wantText: "Glass breaking",
			wantAct:  true,
		},
		{
			name: "finger snap",
			top: []TopClass{
				{ID: 57, Name: "Finger snapping", Score: 0.31},
			},
			wantCat:  "finger_snap",
			wantText: "Finger snapping",
			wantAct:  true,
		},
		{
			name: "clapping",
			top: []TopClass{
				{ID: 58, Name: "Clapping", Score: 0.40},
				{ID: 62, Name: "Applause", Score: 0.20},
			},
			wantCat:  "clapping",
			wantText: "Clapping",
			wantAct:  true,
		},
		{
			name: "ambient speech when no alert sound",
			top: []TopClass{
				{ID: 0, Name: "Speech", Score: 0.72},
			},
			wantCat:  "speech",
			wantText: "Speech",
			wantAct:  false,
		},
		{
			name: "noise floor maps to quiet",
			top: []TopClass{
				{ID: 494, Name: "Silence", Score: 0.91},
				{ID: 500, Name: "Inside, small room", Score: 0.05},
			},
			wantCat:  "quiet",
			wantText: "Quiet",
			wantAct:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.top)
			if got.Category != tc.wantCat || got.Label != tc.wantText || got.Actionable != tc.wantAct {
				t.Fatalf("Classify() = %+v, want category=%q label=%q actionable=%v",
					got, tc.wantCat, tc.wantText, tc.wantAct)
			}
		})
	}
}

func TestClassifyWithMinScalesThreshold(t *testing.T) {
	top := []TopClass{
		{ID: 58, Name: "Clapping", Score: 0.30},
	}
	if got := ClassifyWithMin(top, 0.25); got.Category != "clapping" {
		t.Fatalf("at 0.25 threshold: got %q, want clapping", got.Category)
	}
	if got := ClassifyWithMin(top, 0.45); got.Category != "quiet" {
		t.Fatalf("at 0.45 threshold: got %q, want quiet", got.Category)
	}
}

func TestPublishMQTTAndDiscovery(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type rxPub struct {
		topic   string
		payload string
		retain  bool
	}
	var received []rxPub
	done := make(chan error, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

		hdr, body, err := readMQTTTestPacket(conn)
		if err != nil || hdr != 0x10 {
			done <- err
			return
		}
		if !strings.Contains(string(body), "MQTT") {
			done <- io.ErrUnexpectedEOF
			return
		}
		_, _ = conn.Write([]byte{0x20, 0x02, 0x00, 0x00})

		for {
			h, b, err := readMQTTTestPacket(conn)
			if err != nil {
				done <- err
				return
			}
			if h == 0xe0 {
				done <- nil
				return
			}
			if h&0xf0 == 0x30 {
				tlen := int(b[0])<<8 | int(b[1])
				received = append(received, rxPub{
					topic:   string(b[2 : 2+tlen]),
					payload: string(b[2+tlen:]),
					retain:  h&0x01 != 0,
				})
			}
		}
	}()

	sc := Config{
		MQTTBroker:    ln.Addr().String(),
		MQTTUser:      "ha",
		MQTTPass:      "secret",
		MQTTDiscovery: true,
	}
	det := Detection{Category: "dog", Label: "Dog", RawClass: "Bark", Confidence: 0.82, Actionable: true}
	rep := EngineReply{MS: 38.4, RMSDBFS: -21.5}

	if err := PublishMQTT(nil, sc, "Kitchen Show", det, rep, true, true); err != nil {
		t.Fatalf("PublishMQTT failed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("broker error: %v", err)
	}
	if len(received) != 6 {
		t.Fatalf("received %d MQTT publishes, want 6 (2 discovery + state + event + last_alert + alert_event): %+v", len(received), received)
	}
	if received[0].topic != "homeassistant/sensor/kitchen_show_room_sound/config" || !received[0].retain {
		t.Fatalf("unexpected discovery[0]: %+v", received[0])
	}
	if received[2].topic != "techo5/kitchen_show/sound/state" || received[2].payload != "Dog" {
		t.Fatalf("unexpected state publish: %+v", received[2])
	}
	if received[4].topic != "techo5/kitchen_show/sound/last_alert" || received[4].payload != "Dog" || !received[4].retain {
		t.Fatalf("unexpected last_alert publish: %+v", received[4])
	}
}

func TestSendHANotificationWebhookAndService(t *testing.T) {
	var webhookGot map[string]any
	var serviceGot map[string]string
	var authHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/webhook/test-hook":
			_ = json.NewDecoder(r.Body).Decode(&webhookGot)
			w.WriteHeader(http.StatusOK)
		case "/api/services/persistent_notification/create":
			authHeader = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&serviceGot)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	sc := Config{
		HAWebhookURL: srv.URL + "/api/webhook/test-hook",
		HAURL:        srv.URL,
		HAToken:      "test-token-123",
		HAService:    "persistent_notification.create",
	}
	det := Detection{Category: "baby_cry", Label: "Baby cry", RawClass: "Infant cry", Confidence: 0.78, Actionable: true}
	rep := EngineReply{MS: 33.2, RMSDBFS: -24.0}

	if err := SendHANotification(srv.Client(), sc, "Bedroom Show", det, rep); err != nil {
		t.Fatalf("SendHANotification: %v", err)
	}
	if webhookGot["category"] != "baby_cry" || webhookGot["device"] != "Bedroom Show" {
		t.Fatalf("webhook payload = %+v", webhookGot)
	}
	if authHeader != "Bearer test-token-123" {
		t.Fatalf("Authorization = %q, want Bearer test-token-123", authHeader)
	}
	if serviceGot["title"] != "Bedroom Show heard Baby cry" {
		t.Fatalf("service payload = %+v", serviceGot)
	}
}

func readMQTTTestPacket(r io.Reader) (byte, []byte, error) {
	var h [1]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	rem := 0
	shift := 0
	for {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		rem |= int(b[0]&0x7f) << shift
		if b[0]&0x80 == 0 {
			break
		}
		shift += 7
	}
	body := make([]byte, rem)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return h[0], body, nil
}

func TestInProcessYAMNetEngineRealModel(t *testing.T) {
	candidates := []string{
		os.Getenv("YAMNET_TFLITE"),
		filepath.Join("..", "..", "..", "sound-ai", "yamnet.tflite"),
	}
	var modelPath string
	for _, p := range candidates {
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				modelPath = p
				break
			}
		}
	}
	if modelPath == "" {
		t.Skip("local yamnet.tflite not present; skipping real model accuracy check")
	}

	eng, err := LoadEngine(modelPath)
	if err != nil {
		t.Fatalf("LoadEngine(%s): %v", modelPath, err)
	}
	defer eng.Close()

	silence := make([]int16, windowSamples)
	rep0, err := eng.Score(silence)
	if err != nil {
		t.Fatalf("Score(silence): %v", err)
	}
	if len(rep0.Top) == 0 || rep0.Top[0].ID != 494 || rep0.Top[0].Score < 0.80 {
		t.Fatalf("silence top-1 = %+v, want #494 Silence >= 0.80", rep0.Top)
	}

	tone := make([]int16, windowSamples)
	var phase float64
	for i := range tone {
		tm := float64(i) / 16000.0
		freq := 880.0
		if int(tm*4)%2 == 1 {
			freq = 1320.0
		}
		phase += 2 * math.Pi * freq / 16000.0
		w := math.Sin(phase) + 0.4*math.Sin(2*phase) + 0.2*math.Sin(3*phase)
		tone[i] = int16(9000 * w)
	}
	rep1, err := eng.Score(tone)
	if err != nil {
		t.Fatalf("Score(tone): %v", err)
	}
	if len(rep1.Top) == 0 || rep1.Top[0].ID == 494 || rep1.Top[0].Score < 0.20 {
		t.Fatalf("synthesized tone failed to score distinctly: top=%+v", rep1.Top)
	}

	// Full-scale clipping square wave (+32767 / -32768) should not overflow or produce NaN/Inf.
	square := make([]int16, windowSamples)
	for i := range square {
		if (i/16)%2 == 0 {
			square[i] = 32767
		} else {
			square[i] = -32768
		}
	}
	rep2, err := eng.Score(square)
	if err != nil {
		t.Fatalf("Score(square): %v", err)
	}
	if math.IsNaN(rep2.RMSDBFS) || math.IsInf(rep2.RMSDBFS, 0) || len(rep2.Top) != 8 {
		t.Fatalf("unexpected square wave reply: %+v", rep2)
	}
}

func TestClassifyAllRuleCategoriesAndEdgeCases(t *testing.T) {
	ruleCases := []struct {
		id       int
		name     string
		wantCat  string
		wantText string
		wantAct  bool
	}{
		{393, "Smoke detector, smoke alarm", "smoke_alarm", "Smoke / fire alarm", true},
		{318, "Police car (siren)", "siren", "Siren", true},
		{436, "Chink, clink", "glass", "Glass breaking", true},
		{20, "Baby cry, infant cry", "baby_cry", "Baby cry", true},
		{349, "Doorbell", "doorbell", "Doorbell", true},
		{384, "Alarm clock", "alarm", "Alarm / ring", true},
		{70, "Bark", "dog", "Dog", true},
		{76, "Cat", "cat", "Cat", true},
		{57, "Finger snapping", "finger_snap", "Finger snapping", true},
		{58, "Clapping", "clapping", "Clapping", true},
		{353, "Knock", "knock", "Knock", true},
		{35, "Whistling", "whistle", "Whistling", true},
		{42, "Cough", "cough", "Cough", true},
		{44, "Sneeze", "sneeze", "Sneeze", true},
		{13, "Laughter", "laughter", "Laughter", true},
		{348, "Door", "door", "Door", true},
		{364, "Water tap, faucet", "water", "Running water", false},
		{371, "Vacuum cleaner", "vacuum", "Vacuum cleaner", false},
		{378, "Typing", "typing", "Typing", false},
		{38, "Snoring", "snoring", "Snoring", false},
		{106, "Bird", "bird", "Bird", false},
		{0, "Speech", "speech", "Speech", false},
		{132, "Music", "music", "Music", false},
		{150, "Guitar", "music", "Music", false},
	}

	for _, tc := range ruleCases {
		got := Classify([]TopClass{{ID: tc.id, Name: tc.name, Score: 0.65}})
		if got.Category != tc.wantCat || got.Label != tc.wantText || got.Actionable != tc.wantAct {
			t.Fatalf("id=%d (%s): got %+v, want cat=%q label=%q act=%v",
				tc.id, tc.name, got, tc.wantCat, tc.wantText, tc.wantAct)
		}
	}

	// Edge cases: empty, NaN, Inf, negative scores, extreme minConf, fallback classes, noise floor IDs.
	if got := Classify(nil); got.Category != "quiet" {
		t.Fatalf("Classify(nil) = %+v, want quiet", got)
	}
	nanInfTop := []TopClass{
		{ID: 393, Name: "Smoke alarm", Score: math.NaN()},
		{ID: 318, Name: "Siren", Score: math.Inf(1)},
		{ID: 435, Name: "Glass", Score: math.Inf(-1)},
		{ID: 70, Name: "Bark", Score: -0.8},
		{ID: 349, Name: "Doorbell", Score: 1.5}, // clamped to 1.0
	}
	if got := ClassifyWithMin(nanInfTop, math.NaN()); got.Category != "doorbell" || got.Confidence != 1.0 {
		t.Fatalf("ClassifyWithMin(nanInfTop, NaN) = %+v, want doorbell confidence=1.0", got)
	}
	if got := ClassifyWithMin([]TopClass{{ID: 393, Name: "Smoke", Score: 0.99}}, 5.0); got.Category != "smoke_alarm" {
		t.Fatalf("ClassifyWithMin with minConf=5.0 (clamped to 0.99) = %+v, want smoke_alarm", got)
	}

	// Fallback non-rule class with punctuation and empty name fallback
	fb := Classify([]TopClass{{ID: 420, Name: "Microwave oven (beep/hum), kitchen", Score: 0.55}})
	if fb.Category != "microwave_oven_beep_hum_kitchen" || fb.Actionable {
		t.Fatalf("fallback class = %+v", fb)
	}
	fbEmpty := Classify([]TopClass{{ID: 420, Name: "   ", Score: 0.55}})
	if fbEmpty.Category != "unknown" || fbEmpty.Label != "Unknown" {
		t.Fatalf("empty fallback name = %+v", fbEmpty)
	}

	// All noise-floor IDs map to quiet
	for _, nfID := range []int{494, 500, 501, 502, 506, 507, 513, 514, 515} {
		if got := Classify([]TopClass{{ID: nfID, Name: "NoiseFloor", Score: 0.95}}); got.Category != "quiet" {
			t.Fatalf("noise floor ID %d mapped to %+v, want quiet", nfID, got)
		}
	}
}

func TestLoadEngineCorruptAndMalformedModels(t *testing.T) {
	dir := t.TempDir()

	// 1. Non-existent file
	if _, err := LoadEngine(filepath.Join(dir, "missing.tflite")); err == nil {
		t.Fatal("expected error for missing file")
	}

	// 2. Various corrupt/malformed payloads
	corruptCases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"short_10b", []byte("0123456789")},
		{"bad_magic_64b", make([]byte, 64)},
		{"bad_root_zero", func() []byte {
			b := make([]byte, 128)
			copy(b[4:8], "TFL3")
			return b
		}()},
		{"bad_root_huge", func() []byte {
			b := make([]byte, 128)
			copy(b[4:8], "TFL3")
			b[0], b[1], b[2], b[3] = 0xff, 0xff, 0xff, 0x7f
			return b
		}()},
		{"corrupt_vtable_offsets", func() []byte {
			b := make([]byte, 256)
			copy(b[4:8], "TFL3")
			b[0] = 16 // root at 16
			// vtable offset points out of bounds
			b[16], b[17], b[18], b[19] = 0x00, 0x10, 0x00, 0x00
			return b
		}()},
		{"all_0xff_after_magic", func() []byte {
			b := make([]byte, 512)
			for i := range b {
				b[i] = 0xff
			}
			copy(b[4:8], "TFL3")
			b[0], b[1], b[2], b[3] = 32, 0, 0, 0
			return b
		}()},
	}

	for _, tc := range corruptCases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name+".tflite")
			if err := os.WriteFile(p, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			eng, err := LoadEngine(p)
			if err == nil {
				eng.Close()
				t.Fatalf("LoadEngine(%s) succeeded unexpectedly", tc.name)
			}
		})
	}

	// 3. Score on nil or closed Engine, and double Close()
	var nilEng *Engine
	nilEng.Close()
	if _, err := nilEng.Score(make([]int16, windowSamples)); err == nil {
		t.Fatal("expected error scoring on nil Engine")
	}
	closedEng := &Engine{}
	closedEng.Close()
	closedEng.Close()
	if _, err := closedEng.Score(make([]int16, windowSamples)); err == nil {
		t.Fatal("expected error scoring on closed Engine")
	}
}

func TestMQTTExceptionAndBrokerFailureCases(t *testing.T) {
	det := Detection{Category: "dog", Label: "Dog", Confidence: 0.8, Actionable: true}
	rep := EngineReply{MS: 30, RMSDBFS: -20}

	// 1. Empty broker
	if err := PublishMQTT(nil, Config{}, "Dev", det, rep, true, false); err == nil {
		t.Fatal("expected error when MQTTBroker is empty")
	}

	// 2. Oversized credentials (> 65535 bytes)
	huge := strings.Repeat("a", 70000)
	if err := PublishMQTT(nil, Config{MQTTBroker: "127.0.0.1:1883", MQTTUser: huge}, "Dev", det, rep, true, false); err == nil {
		t.Fatal("expected error for oversized MQTTUser")
	}

	// 3. Invalid topic containing NUL byte
	if err := PublishMQTT(nil, Config{MQTTBroker: "127.0.0.1:1883", MQTTTopic: "bad\x00topic"}, "Dev", det, rep, true, false); err == nil {
		t.Fatal("expected error for topic with NUL byte")
	}

	// 4. Dial failure
	failDial := func(network, addr string, timeout time.Duration) (net.Conn, error) {
		return nil, io.ErrClosedPipe
	}
	if err := PublishMQTT(failDial, Config{MQTTBroker: "127.0.0.1"}, "Dev", det, rep, true, false); err == nil {
		t.Fatal("expected error when dial fails")
	}

	// 5. Simulated broker responses: EOF before CONNACK, bad CONNACK code, malformed CONNACK, disconnect mid-publish
	brokerTests := []struct {
		name    string
		handler func(net.Conn)
		wantErr bool
	}{
		{
			name: "eof_before_connack",
			handler: func(c net.Conn) {
				_ = c.Close()
			},
			wantErr: true,
		},
		{
			name: "connack_refused_bad_auth",
			handler: func(c net.Conn) {
				_, _, _ = readMQTTTestPacket(c)
				_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x04})
				_ = c.Close()
			},
			wantErr: true,
		},
		{
			name: "connack_malformed_header",
			handler: func(c net.Conn) {
				_, _, _ = readMQTTTestPacket(c)
				_, _ = c.Write([]byte{0x00, 0x00, 0x00, 0x00})
				_ = c.Close()
			},
			wantErr: true,
		},
		{
			name: "non_alert_without_discovery_succeeds",
			handler: func(c net.Conn) {
				defer c.Close()
				_, _, _ = readMQTTTestPacket(c)
				_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x00})
				for {
					h, _, err := readMQTTTestPacket(c)
					if err != nil || h == 0xe0 {
						return
					}
				}
			},
			wantErr: false,
		},
	}

	for _, bt := range brokerTests {
		t.Run(bt.name, func(t *testing.T) {
			c1, c2 := net.Pipe()
			go bt.handler(c2)
			dial := func(network, addr string, timeout time.Duration) (net.Conn, error) {
				return c1, nil
			}
			err := PublishMQTT(dial, Config{MQTTBroker: "127.0.0.1:1883"}, "", det, rep, false, false)
			if (err != nil) != bt.wantErr {
				t.Fatalf("%s: err=%v, wantErr=%v", bt.name, err, bt.wantErr)
			}
		})
	}
}

func TestSendHANotificationAndConfigHelpersExceptionCases(t *testing.T) {
	det := Detection{Category: "siren", Label: "Siren", RawClass: "Siren", Confidence: 0.85, Actionable: true}
	rep := EngineReply{MS: 25.0, RMSDBFS: 0}

	// 1. Nothing configured
	if err := SendHANotification(nil, Config{}, "", det, rep); err == nil {
		t.Fatal("expected error when neither webhook nor HA service is configured")
	}

	// 2. Invalid webhook URL scheme
	if err := SendHANotification(nil, Config{HAWebhookURL: "ftp://bad/hook"}, "Dev", det, rep); err == nil {
		t.Fatal("expected error for non-http webhook URL")
	}

	// 3. Invalid HA URL scheme
	if err := SendHANotification(nil, Config{HAURL: "ha.local:8123", HAService: "notify.notify"}, "Dev", det, rep); err == nil {
		t.Fatal("expected error for HAURL without http/https scheme")
	}

	// 4. Invalid HAService formats
	for _, badSvc := range []string{"notify", ".notify", "notify.", "notify.bad/path", "notify.bad space"} {
		sc := Config{HAURL: "http://127.0.0.1:8123", HAService: badSvc}
		if err := SendHANotification(nil, sc, "Dev", det, rep); err == nil {
			t.Fatalf("expected error for invalid HAService %q", badSvc)
		}
	}

	// 5. Server returns HTTP 500 on webhook and HTTP 401 on service
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/webhook/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if err := SendHANotification(srv.Client(), Config{HAWebhookURL: srv.URL + "/api/webhook/fail"}, "Dev", det, rep); err == nil {
		t.Fatal("expected error on HTTP 500 webhook")
	}
	if err := SendHANotification(srv.Client(), Config{HAURL: srv.URL, HAService: "notify.notify"}, "Dev", det, rep); err == nil {
		t.Fatal("expected error on HTTP 401 HA service")
	}

	// 6. Helper edge cases
	if got := SanitizeNode("   ---   "); got != "techo5" {
		t.Fatalf("SanitizeNode(punctuation) = %q, want techo5", got)
	}
	if got := DefaultTopic(Config{MQTTTopic: " /custom/topic/ "}, "Dev"); got != "custom/topic" {
		t.Fatalf("DefaultTopic = %q, want custom/topic", got)
	}
	cfg := DefaultConfig("")
	if cfg.DeviceName != "TECHO5" || !AlertEnabled(cfg, "smoke_alarm") || AlertEnabled(cfg, "nonexistent") {
		t.Fatalf("unexpected DefaultConfig/AlertEnabled behavior: %+v", cfg)
	}
}

