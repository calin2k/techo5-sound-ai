package addon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calin2k/techo5-sound-ai/internal/asp"
	"github.com/calin2k/techo5-sound-ai/internal/sound"
)

func TestDaemonIndexAndSaveConfigPreservesSecrets(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDaemon(
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "yamnet.tflite"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	h := d.Handler()

	// 1. GET / renders HTML
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Room Sound AI Add-On") {
		t.Fatalf("GET / status=%d", rec.Code)
	}

	// 2. POST /setup/sound without token is rejected
	form := url.Values{"enabled": {"1"}}
	req = httptest.NewRequest(http.MethodPost, "/setup/sound", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without token, got %d", rec.Code)
	}

	// 3. POST /setup/sound with token saves settings and secrets
	form = url.Values{
		"token":          {d.token},
		"enabled":        {"1"},
		"device_name":    {"Kitchen Show"},
		"min_confidence": {"0.30"},
		"cooldown_sec":   {"20"},
		"alerts":         {"smoke_alarm", "baby_cry"},
		"ha_url":         {"http://ha.local:8123"},
		"ha_token":       {"secret-ha-token"},
		"ha_service":     {"notify.mobile_app"},
		"mqtt_broker":    {"192.168.1.5:1883"},
		"mqtt_user":      {"mqttuser"},
		"mqtt_pass":      {"secret-mqtt-pass"},
		"mqtt_discovery": {"1"},
	}
	req = httptest.NewRequest(http.MethodPost, "/setup/sound", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}

	cfg := d.Config()
	if cfg.DeviceName != "Kitchen Show" || cfg.HAToken != "secret-ha-token" || cfg.MQTTPass != "secret-mqtt-pass" {
		t.Fatalf("unexpected saved config: %+v", cfg)
	}

	// 4. Subsequent save with blank ha_token / mqtt_pass keeps existing secrets
	form.Set("ha_token", "")
	form.Set("mqtt_pass", "")
	req = httptest.NewRequest(http.MethodPost, "/setup/sound", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	cfg = d.Config()
	if cfg.HAToken != "secret-ha-token" || cfg.MQTTPass != "secret-mqtt-pass" {
		t.Fatalf("secrets were not preserved on blank save: %+v", cfg)
	}

	// 5. GET /api/status returns valid JSON
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal /api/status: %v", err)
	}
	if st.Current != "Quiet" {
		t.Fatalf("expected initial status Quiet, got %q", st.Current)
	}
}

func TestHandleReplyAlertAndCooldown(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDaemon(
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "yamnet.tflite"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	rep := sound.EngineReply{
		MS:      34.5,
		RMSDBFS: -19.2,
		Top: []sound.TopClass{
			{ID: 393, Name: "Smoke detector, smoke alarm", Score: 0.62},
		},
	}
	d.handleReply(rep)

	st := d.Status()
	if st.Current != "Smoke / fire alarm" || st.LastAlert != "Smoke / fire alarm" {
		t.Fatalf("expected Smoke / fire alarm alert, got status=%+v", st)
	}
	if len(st.Recent) != 1 || !st.Recent[0].Alert {
		t.Fatalf("expected 1 alert event in recent, got %+v", st.Recent)
	}

	// Immediate second hop within cooldown coalesces into the existing event without firing a second alert
	d.handleReply(rep)
	st = d.Status()
	if len(st.Recent) != 1 {
		t.Fatalf("expected second hop within cooldown to coalesce, got %d events", len(st.Recent))
	}
}

func TestEnergyGateSkipsQuietFloorAndWakesOnActiveSound(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDaemon(
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "yamnet.tflite"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// 1. Quiet floor at -60 dBFS should not trigger YAMNet inference
	if d.shouldScoreHop(-60.0) {
		t.Fatal("expected energy gate to skip quiet hop at -60 dBFS")
	}
	if d.shouldScoreHop(-59.0) {
		t.Fatal("expected energy gate to skip hop within +4.5 dB of floor")
	}

	// 2. Sound +6 dB above the floor (-54 dBFS) wakes the classifier
	if !d.shouldScoreHop(-54.0) {
		t.Fatal("expected energy gate to wake on hop +6 dB above floor")
	}
}

func TestHysteresisStreakAndQuietRecovery(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDaemon(
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "yamnet.tflite"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// 1. Single hop of non-urgent low-confidence sound (< 0.45) is held for confirmation
	lowDog := sound.EngineReply{
		MS:      30.0,
		RMSDBFS: -25.0,
		Top:     []sound.TopClass{{ID: 70, Name: "Bark", Score: 0.32}},
	}
	d.handleReply(lowDog)
	if st := d.Status(); st.Current != "Quiet" {
		t.Fatalf("expected 1st low-confidence non-urgent hop to stay Quiet, got %q", st.Current)
	}

	// 2. Second consecutive hop of the same category confirms the detection
	d.handleReply(lowDog)
	if st := d.Status(); st.Current != "Dog" {
		t.Fatalf("expected 2nd consecutive hop to confirm Dog, got %q", st.Current)
	}

	// 3. Two quiet hops keep Dog, third consecutive quiet hop resets to Quiet
	quietRep := sound.EngineReply{
		MS:      28.0,
		RMSDBFS: -58.0,
		Top:     []sound.TopClass{{ID: 494, Name: "Silence", Score: 0.92}},
	}
	d.handleReply(quietRep)
	d.handleReply(quietRep)
	if st := d.Status(); st.Current != "Dog" {
		t.Fatalf("expected 2 quiet hops to hold Dog, got %q", st.Current)
	}
	d.handleReply(quietRep)
	if st := d.Status(); st.Current != "Quiet" {
		t.Fatalf("expected 3rd quiet hop to reset to Quiet, got %q", st.Current)
	}

	// 4. Urgent category (glass breaking) triggers immediately on hop 1 even below 0.45
	lowGlass := sound.EngineReply{
		MS:      31.0,
		RMSDBFS: -22.0,
		Top:     []sound.TopClass{{ID: 435, Name: "Glass", Score: 0.30}},
	}
	d.handleReply(lowGlass)
	if st := d.Status(); st.Current != "Glass breaking" {
		t.Fatalf("expected urgent glass breaking to trigger on 1st hop, got %q", st.Current)
	}
}

func TestCorruptConfigAndOutOfRangeClamping(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	modelPath := filepath.Join(dir, "yamnet.tflite")

	// 1. Corrupt JSON on disk -> NewDaemon falls back to defaults without failing
	if err := os.WriteFile(cfgPath, []byte("{corrupt json!!!"), 0600); err != nil {
		t.Fatal(err)
	}
	d1, err := NewDaemon(cfgPath, modelPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg := d1.Config(); cfg.MinConfidence != 0.25 || cfg.CooldownSec != 15 {
		t.Fatalf("corrupt config fallback = %+v", cfg)
	}
	d1.Close()

	// 2. Out-of-range values on disk -> clamped to [0.05, 0.95] and [1, 3600]
	if err := os.WriteFile(cfgPath, []byte(`{"min_confidence": 99.9, "cooldown_sec": 999999}`), 0600); err != nil {
		t.Fatal(err)
	}
	d2, err := NewDaemon(cfgPath, modelPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	if cfg := d2.Config(); cfg.MinConfidence != 0.95 || cfg.CooldownSec != 3600 {
		t.Fatalf("clamped config = %+v, want MinConfidence=0.95 CooldownSec=3600", cfg)
	}

	// 3. Clearing secrets via ha_clear_token=1 and mqtt_clear_pass=1
	_ = d2.saveConfig(sound.Config{
		Enabled:       true,
		DeviceName:    "Show",
		MinConfidence: 0.25,
		CooldownSec:   15,
		HAToken:       "tok",
		MQTTPass:      "pass",
	})
	form := url.Values{
		"token":           {d2.token},
		"ha_clear_token":  {"1"},
		"mqtt_clear_pass": {"1"},
	}
	req := httptest.NewRequest(http.MethodPost, "/setup/sound", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	d2.Handler().ServeHTTP(rec, req)
	if cfg := d2.Config(); cfg.HAToken != "" || cfg.MQTTPass != "" {
		t.Fatalf("expected secrets to be cleared, got %+v", cfg)
	}
}

func TestModelUploadDownloadRemoveAndTestNotifyEndpoints(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDaemon(
		filepath.Join(dir, "config.json"),
		filepath.Join(dir, "yamnet.tflite"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := d.Handler()

	// 1. POST /setup/sound/test when unconfigured -> redirects with ?err=
	form := url.Values{"token": {d.token}}
	req := httptest.NewRequest(http.MethodPost, "/setup/sound/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatalf("expected redirect with err when unconfigured, got code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// 2. POST /setup/sound/test with mock HA webhook -> redirects with ?msg=Test+sent
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()
	_ = d.saveConfig(sound.Config{
		Enabled:      true,
		DeviceName:   "Office Show",
		HAWebhookURL: webhookSrv.URL + "/api/webhook/ok",
	})
	req = httptest.NewRequest(http.MethodPost, "/setup/sound/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "msg=Test+sent") {
		t.Fatalf("expected Test sent redirect, got code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// 3. POST /setup/sound/model/upload with corrupt .tflite file -> rejected cleanly
	uploadMultipart := func(tok string, fileBytes []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("token", tok)
		if fileBytes != nil {
			fw, _ := mw.CreateFormFile("model", "yamnet.tflite")
			_, _ = fw.Write(fileBytes)
		}
		_ = mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/setup/sound/model/upload", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if r0 := uploadMultipart("wrong-token", []byte("123")); r0.Code != http.StatusForbidden {
		t.Fatalf("upload with wrong token: got %d, want 403", r0.Code)
	}
	if r1 := uploadMultipart(d.token, nil); !strings.Contains(r1.Header().Get("Location"), "err=") {
		t.Fatalf("upload without file: loc=%q", r1.Header().Get("Location"))
	}
	if r2 := uploadMultipart(d.token, []byte("short")); !strings.Contains(r2.Header().Get("Location"), "err=") {
		t.Fatalf("upload too small: loc=%q", r2.Header().Get("Location"))
	}
	if r3 := uploadMultipart(d.token, make([]byte, 4096)); !strings.Contains(r3.Header().Get("Location"), "err=") {
		t.Fatalf("upload corrupt model: loc=%q", r3.Header().Get("Location"))
	}
	if err := d.installModelReader(bytes.NewReader(make([]byte, MaxModelBytes+16))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized model stream to fail with size limit error, got %v", err)
	}

	// 4. POST /setup/sound/model/download against mock HTTP server (404, corrupt, and valid if present)
	var dlMode string
	var realModelBytes []byte
	for _, p := range []string{os.Getenv("YAMNET_TFLITE"), filepath.Join("..", "..", "..", "sound-ai", "yamnet.tflite")} {
		if p != "" {
			if b, err := os.ReadFile(p); err == nil {
				realModelBytes = b
				break
			}
		}
	}

	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch dlMode {
		case "404":
			w.WriteHeader(http.StatusNotFound)
		case "corrupt":
			_, _ = w.Write(make([]byte, 4096))
		case "valid":
			_, _ = w.Write(realModelBytes)
		}
	}))
	defer dlSrv.Close()
	d.modelURL = dlSrv.URL

	dlMode = "404"
	req = httptest.NewRequest(http.MethodPost, "/setup/sound/model/download", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatalf("expected error on 404 download, got %q", rec.Header().Get("Location"))
	}

	dlMode = "corrupt"
	req = httptest.NewRequest(http.MethodPost, "/setup/sound/model/download", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Fatalf("expected error on corrupt download, got %q", rec.Header().Get("Location"))
	}

	if len(realModelBytes) > 0 {
		dlMode = "valid"
		req = httptest.NewRequest(http.MethodPost, "/setup/sound/model/download", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !strings.Contains(rec.Header().Get("Location"), "msg=") || !d.Status().ModelReady {
			t.Fatalf("expected valid model download to succeed: loc=%q status=%+v", rec.Header().Get("Location"), d.Status())
		}

		// Remove model
		req = httptest.NewRequest(http.MethodPost, "/setup/sound/model/remove", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if d.Status().ModelReady {
			t.Fatal("expected ModelReady=false after model remove")
		}
	}
}

func TestEndToEndDaemonRunStreamingAndConcurrency(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "sound.sock")
	modelPath := filepath.Join(dir, "yamnet.tflite")

	for _, p := range []string{os.Getenv("YAMNET_TFLITE"), filepath.Join("..", "..", "..", "sound-ai", "yamnet.tflite")} {
		if p != "" {
			if b, err := os.ReadFile(p); err == nil {
				_ = os.WriteFile(modelPath, b, 0600)
				break
			}
		}
	}

	d, err := NewDaemon(filepath.Join(dir, "config.json"), modelPath, sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- d.Run(ctx)
	}()

	// Wait for unixgram socket to appear
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	sink, closeSink := asp.NewUnixDatagramSink(sockPath)
	defer closeSink()

	// Concurrently stream 60 frames (19,200 samples > 15,600 window), query HTTP status, and reload model.
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		pkt := make([]byte, asp.OutputBytes)
		for f := 0; f < 60; f++ {
			for i := 0; i < asp.OutputSamples; i++ {
				s := int16(8000 * math.Sin(2*math.Pi*880*float64(f*asp.OutputSamples+i)/16000.0))
				binary.LittleEndian.PutUint16(pkt[i*2:], uint16(s))
			}
			sink(pkt)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	go func() {
		defer wg.Done()
		h := d.Handler()
		for i := 0; i < 20; i++ {
			req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			_ = d.ReloadModel()
			time.Sleep(15 * time.Millisecond)
		}
	}()

	wg.Wait()
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Daemon.Run returned error: %v", err)
	}
	if d.Status().FramesReceived == 0 {
		t.Fatal("expected FramesReceived > 0 after unixgram streaming")
	}
}

func TestDispatchNotificationsMQTTThrottleAndSecurityHeaders(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	d, err := NewDaemon(cfgPath, filepath.Join(dir, "yamnet.tflite"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// 1. Verify HTTP security and cache-control headers on GET / and GET /api/status
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" ||
		rec.Header().Get("X-Frame-Options") != "DENY" ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing security headers: %+v", rec.Header())
	}

	// 2. Verify saveConfig enforces 0600 file mode on config.json
	if err := d.saveConfig(sound.Config{
		Enabled:       true,
		DeviceName:    "Bedroom Show",
		MinConfidence: 0.25,
		CooldownSec:   15,
		MQTTAllSounds: true,
	}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(cfgPath); err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("config.json perm = %v, err = %v, want 0600", st.Mode().Perm(), err)
	}

	// 3. Verify MQTTAllSounds throttling: 2 consecutive high-confidence speech hops within 5s
	// queue only 1 notification job; switching to music queues a 2nd job immediately.
	speechRep := sound.EngineReply{
		MS: 25, RMSDBFS: -20,
		Top: []sound.TopClass{{ID: 0, Name: "Speech", Score: 0.80}},
	}
	musicRep := sound.EngineReply{
		MS: 25, RMSDBFS: -20,
		Top: []sound.TopClass{{ID: 132, Name: "Music", Score: 0.80}},
	}
	d.handleReply(speechRep)
	d.handleReply(speechRep)
	if got := len(d.notifyCh); got != 1 {
		t.Fatalf("expected 1 queued job for repeated speech hops, got %d", got)
	}
	d.handleReply(musicRep)
	if got := len(d.notifyCh); got != 2 {
		t.Fatalf("expected 2nd queued job on category change to music, got %d", got)
	}

	// 4. Test dispatchNotifications error recording and clear on success
	badCfg := sound.Config{
		DeviceName:   "Bedroom Show",
		HAWebhookURL: "ftp://invalid-scheme",
		MQTTBroker:   "127.0.0.1:1",
	}
	det := sound.Detection{Category: "siren", Label: "Siren", Confidence: 0.9, Actionable: true}
	d.dispatchNotifications(badCfg, det, speechRep, true, true)
	if d.Status().LastNotifyErr == "" {
		t.Fatal("expected LastNotifyErr to be recorded when notification fails")
	}

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	okCfg := sound.Config{
		DeviceName:   "Bedroom Show",
		HAWebhookURL: okSrv.URL,
	}
	d.dispatchNotifications(okCfg, det, speechRep, true, false)
	if errStr := d.Status().LastNotifyErr; errStr != "" {
		t.Fatalf("expected LastNotifyErr to clear on success, got %q", errStr)
	}
}

