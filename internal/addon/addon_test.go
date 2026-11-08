package addon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

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
