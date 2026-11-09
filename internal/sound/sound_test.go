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
	"sync"
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

	// 1. Non-existent file or directory (non-regular file)
	if _, err := LoadEngine(filepath.Join(dir, "missing.tflite")); err == nil {
		t.Fatal("expected error for missing file")
	}
	if _, err := LoadEngine(dir); err == nil {
		t.Fatal("expected error when passing a directory to LoadEngine")
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

func TestDot4I8BoundaryAndTailLengths(t *testing.T) {
	// Verify dot4I8 across all remainder lengths (n % 4 in {0, 1, 2, 3}) and extreme int8 values (-128, 127).
	for n := 0; n <= 19; n++ {
		in := make([]int8, n)
		w0 := make([]int8, n)
		w1 := make([]int8, n)
		w2 := make([]int8, n)
		w3 := make([]int8, n)
		for i := 0; i < n; i++ {
			switch i % 4 {
			case 0:
				in[i] = -128
				w0[i], w1[i], w2[i], w3[i] = -128, 127, -1, 0
			case 1:
				in[i] = 127
				w0[i], w1[i], w2[i], w3[i] = 127, -128, 2, -3
			case 2:
				in[i] = -64
				w0[i], w1[i], w2[i], w3[i] = 33, -44, 55, -66
			case 3:
				in[i] = 99
				w0[i], w1[i], w2[i], w3[i] = -11, 22, -33, 44
			}
		}
		got := [4]int32{10, -20, 30, -40}
		want := got
		for i := 0; i < n; i++ {
			x := int32(in[i])
			want[0] += x * int32(w0[i])
			want[1] += x * int32(w1[i])
			want[2] += x * int32(w2[i])
			want[3] += x * int32(w3[i])
		}
		dot4I8(in, w0, w1, w2, w3, &got)
		if got != want {
			t.Fatalf("dot4I8(n=%d) = %v, want %v", n, got, want)
		}
	}
}

func FuzzClassifyWithMin(f *testing.F) {
	f.Add(393, "Smoke alarm", 0.65, 0.25)
	f.Add(494, "Silence", 0.99, 0.25)
	f.Add(420, "Custom sound (test)/1", 0.50, 0.10)
	f.Add(420, "(", 1.0, 0.25)
	f.Fuzz(func(t *testing.T, id int, name string, score float64, minConf float64) {
		det := ClassifyWithMin([]TopClass{{ID: id, Name: name, Score: score}}, minConf)
		if det.Category == "" || det.Label == "" {
			t.Fatalf("empty Category or Label returned: %+v", det)
		}
		if math.IsNaN(det.Confidence) || math.IsInf(det.Confidence, 0) || det.Confidence < 0 || det.Confidence > 1.0 {
			t.Fatalf("invalid Confidence %v in %+v", det.Confidence, det)
		}
	})
}

func FuzzLoadEngine(f *testing.F) {
	f.Add([]byte("short"))
	hdr := make([]byte, 128)
	copy(hdr[4:8], "TFL3")
	hdr[0] = 16
	f.Add(hdr)
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(t.TempDir(), "fuzz.tflite")
		if err := os.WriteFile(p, data, 0600); err != nil {
			return
		}
		eng, err := LoadEngine(p)
		if err == nil {
			eng.Close()
		}
	})
}

func BenchmarkScore(b *testing.B) {
	var modelPath string
	for _, p := range []string{os.Getenv("YAMNET_TFLITE"), filepath.Join("..", "..", "..", "sound-ai", "yamnet.tflite")} {
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				modelPath = p
				break
			}
		}
	}
	if modelPath == "" {
		b.Skip("local yamnet.tflite not present; skipping BenchmarkScore")
	}
	eng, err := LoadEngine(modelPath)
	if err != nil {
		b.Fatal(err)
	}
	defer eng.Close()
	pcm := make([]int16, windowSamples)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.Score(pcm); err != nil {
			b.Fatal(err)
		}
	}
}

func TestClassifyWithConfigPerCategoryOverrides(t *testing.T) {
	top := []TopClass{{ID: 353, Name: "Knock", Score: 0.16}}
	// Default minConf=0.25 requires >=0.22 for knock -> Quiet
	if det := ClassifyWithConfig(top, Config{MinConfidence: 0.25}); det.Category != "quiet" {
		t.Fatalf("expected Quiet at default 0.25, got %+v", det)
	}
	// With CategoryMin["knock"] = 0.15, knock at 0.16 is detected
	sc := Config{
		MinConfidence: 0.25,
		CategoryMin:   map[string]float64{"knock": 0.15},
	}
	if det := ClassifyWithConfig(top, sc); det.Category != "knock" {
		t.Fatalf("expected knock with CategoryMin override 0.15, got %+v", det)
	}
}

func TestPersistentMQTTClientLWTAndConnectionReuse(t *testing.T) {
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
	var mu sync.Mutex
	var pubs []rxPub
	var connects int

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connects++
			mu.Unlock()
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				hdr, _, err := readMQTTTestPacket(c)
				if err != nil || hdr != 0x10 {
					return
				}
				_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x00})
				for {
					h, b, err := readMQTTTestPacket(c)
					if err != nil || h == 0xe0 {
						return
					}
					if h&0xf0 == 0x30 {
						tlen := int(b[0])<<8 | int(b[1])
						mu.Lock()
						pubs = append(pubs, rxPub{
							topic:   string(b[2 : 2+tlen]),
							payload: string(b[2+tlen:]),
							retain:  h&0x01 != 0,
						})
						mu.Unlock()
					}
				}
			}(conn)
		}
	}()

	cli := NewMQTTClient(nil)
	sc := Config{
		MQTTBroker:    ln.Addr().String(),
		MQTTUser:      "user",
		MQTTPass:      "pass",
		MQTTDiscovery: true,
	}
	det := Detection{Category: "knock", Label: "Knock", RawClass: "Knock", Confidence: 0.55, Actionable: true}
	rep := EngineReply{MS: 22.0, RMSDBFS: -25.0}

	// 1st publish connects, sends birth "online", discovery, state, event, last_alert, alert_event
	if err := cli.Publish(sc, "Hall Show", det, rep, true, true); err != nil {
		t.Fatalf("1st Publish: %v", err)
	}
	// 2nd publish reuses the exact same TCP connection without reconnecting
	if err := cli.Publish(sc, "Hall Show", Detection{Category: "quiet", Label: "Quiet"}, rep, false, false); err != nil {
		t.Fatalf("2nd Publish: %v", err)
	}
	// Close publishes retained "offline" availability and disconnects
	_ = cli.Close()
	time.Sleep(30 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if connects != 1 {
		t.Fatalf("expected persistent client to reuse 1 TCP connection across 2 publishes, got %d connects", connects)
	}
	if len(pubs) == 0 || pubs[0].topic != "techo5/hall_show/sound/availability" || pubs[0].payload != "online" || !pubs[0].retain {
		t.Fatalf("expected 1st publish to be retained online availability, got %+v", pubs)
	}
	last := pubs[len(pubs)-1]
	if last.topic != "techo5/hall_show/sound/availability" || last.payload != "offline" || !last.retain {
		t.Fatalf("expected final publish on Close() to be retained offline availability, got %+v", last)
	}
	var sawBinSensorDisc, sawAlertON, sawAlertOFF bool
	for _, p := range pubs {
		if p.topic == "homeassistant/binary_sensor/hall_show_sound_alert/config" && p.retain {
			sawBinSensorDisc = true
		}
		if p.topic == "techo5/hall_show/sound/alert_active" && p.payload == "ON" {
			sawAlertON = true
		}
		if p.topic == "techo5/hall_show/sound/alert_active" && p.payload == "OFF" {
			sawAlertOFF = true
		}
	}
	if !sawBinSensorDisc || !sawAlertON || !sawAlertOFF {
		t.Fatalf("expected binary_sensor discovery and ON/OFF alert_active states: disc=%v on=%v off=%v", sawBinSensorDisc, sawAlertON, sawAlertOFF)
	}
}

func TestEmbeddingSimilarityAndLearnMatchCustomSound(t *testing.T) {
	// 1. EmbeddingSimilarity edge cases
	if sim := EmbeddingSimilarity(nil, nil); sim != 0 {
		t.Fatalf("nil similarity = %v, want 0", sim)
	}
	zero := make([]byte, 1024)
	if sim := EmbeddingSimilarity(zero, zero); sim != 0 {
		t.Fatalf("zero vector similarity = %v, want 0", sim)
	}

	vecA := make([]byte, 1024)
	vecB := make([]byte, 1024)
	vecOpp := make([]byte, 1024)
	s8 := func(v int8) byte { return byte(v) }
	for i := 0; i < 1024; i++ {
		if i%2 == 0 {
			vecA[i] = s8(60)
			vecB[i] = s8(58)
			vecOpp[i] = s8(-60)
		} else {
			vecA[i] = s8(-40)
			vecB[i] = s8(-42)
			vecOpp[i] = s8(40)
		}
	}
	if sim := EmbeddingSimilarity(vecA, vecA); sim < 0.999 {
		t.Fatalf("self similarity = %v, want ~1.0", sim)
	}
	if sim := EmbeddingSimilarity(vecA, vecOpp); sim != 0 {
		t.Fatalf("opposite vector similarity = %v, want 0", sim)
	}

	// 2. LearnCustomSound validation + 1-shot & 2-shot prototype averaging
	if _, _, err := LearnCustomSound(nil, "", 0.82, vecA); err == nil {
		t.Fatal("expected error for empty custom sound name")
	}
	if _, _, err := LearnCustomSound(nil, "Kettle", 0.82, nil); err == nil {
		t.Fatal("expected error for short embedding")
	}

	var list []CustomSound
	var cs CustomSound
	var err error
	list, cs, err = LearnCustomSound(list, "Kettle Whistle!", 0.84, vecA)
	if err != nil || cs.Category != "custom_kettle_whistle" || cs.Samples != 1 || len(list) != 1 {
		t.Fatalf("1-shot LearnCustomSound: cs=%+v len=%d err=%v", cs, len(list), err)
	}
	list, cs, err = LearnCustomSound(list, "Kettle Whistle!", 0.84, vecB)
	if err != nil || cs.Samples != 2 || len(list) != 1 {
		t.Fatalf("2-shot LearnCustomSound: cs=%+v len=%d err=%v", cs, len(list), err)
	}

	// 3. MatchCustomSound matches vecA against learned prototype
	det, ok := MatchCustomSound(vecA, list)
	if !ok || det.Category != "custom_kettle_whistle" || det.Label != "Kettle Whistle!" || !det.Actionable {
		t.Fatalf("expected MatchCustomSound to match Kettle Whistle, got ok=%v det=%+v", ok, det)
	}
	if _, ok := MatchCustomSound(vecOpp, list); ok {
		t.Fatal("expected opposite embedding not to match custom sound")
	}

	// 4. AlertEnabled automatically enables custom_* categories unless explicitly listed
	cfg := Config{}
	if !AlertEnabled(cfg, "custom_kettle_whistle") {
		t.Fatal("expected custom_* sound to be alert-enabled by default")
	}
}


