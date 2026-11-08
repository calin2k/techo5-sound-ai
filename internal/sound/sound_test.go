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
}

