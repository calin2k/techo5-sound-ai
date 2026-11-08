// Package addon implements the standalone techo5-sound-ai daemon, audio stream consumer,
// YAMNet classifier loop, config store, and HTTP setup/status server.
package addon

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/calin2k/techo5-sound-ai/internal/sound"
)

const (
	WindowSamples = 15600 // 975 ms at 16 kHz
	HopSamples    = 7680  // 480 ms hop (~50% overlap)

	DefaultModelPath  = "/data/misc/techo5/yamnet/yamnet.tflite"
	DefaultConfigPath = "/data/techo5-sound-ai/config.json"
	DefaultModelURL   = "https://www.kaggle.com/api/v1/models/google/yamnet/tfLite/classification-tflite/1/download"
	MaxModelBytes     = 16 << 20
)

type Event struct {
	Time       string  `json:"time"`
	Category   string  `json:"category"`
	Label      string  `json:"label"`
	RawClass   string  `json:"raw_class"`
	Confidence float64 `json:"confidence"`
	RMSDBFS    float64 `json:"rms_dbfs"`
	Alert      bool    `json:"alert"`
}

type Status struct {
	ModelReady     bool     `json:"model_ready"`
	ModelSize      int64    `json:"model_size"`
	Running        bool     `json:"running"`
	AudioConnected bool     `json:"audio_connected"`
	FramesReceived uint64   `json:"frames_received"`
	Current        string   `json:"current"`
	Category       string   `json:"category"`
	RawClass       string   `json:"raw_class"`
	Confidence     float64  `json:"confidence"`
	RMSDBFS        float64  `json:"rms_dbfs"`
	InferenceMS    float64  `json:"inference_ms"`
	LastAlert      string   `json:"last_alert"`
	LastAlertAt    string   `json:"last_alert_at"`
	LastNotifyErr  string   `json:"last_notify_err"`
	Recent         []Event  `json:"recent"`
	AlertChoices   []string `json:"-"`
}

type Daemon struct {
	cfgPath   string
	modelPath string
	sockPath  string
	token     string

	mu            sync.RWMutex
	cfg           sound.Config
	eng           *sound.Engine
	current       string
	category      string
	rawClass      string
	confidence    float64
	rmsDBFS       float64
	inferenceMS   float64
	lastAlert     string
	lastAlertAt   time.Time
	lastAlertTime map[string]time.Time
	lastNotifyErr string
	recent        []Event
	mqttDiscSent  bool
	prevCat       string
	streak        int
	quietHops     int

	framesRx    atomic.Uint64
	lastFrameNs atomic.Int64
	pcmCh       chan []int16
	httpClient  *http.Client
	mqttDial    func(network, addr string, timeout time.Duration) (net.Conn, error)
}

func NewDaemon(cfgPath, modelPath, sockPath string) (*Daemon, error) {
	if cfgPath == "" {
		cfgPath = DefaultConfigPath
	}
	if modelPath == "" {
		modelPath = DefaultModelPath
	}
	var tok [16]byte
	_, _ = rand.Read(tok[:])

	d := &Daemon{
		cfgPath:       cfgPath,
		modelPath:     modelPath,
		sockPath:      sockPath,
		token:         hex.EncodeToString(tok[:]),
		cfg:           sound.DefaultConfig(detectDeviceName()),
		current:       "Quiet",
		category:      "quiet",
		lastAlertTime: make(map[string]time.Time),
		pcmCh:         make(chan []int16, 32),
		httpClient:    &http.Client{Timeout: 6 * time.Second},
	}
	_ = d.loadConfig()
	_ = d.ReloadModel()
	return d, nil
}

func detectDeviceName() string {
	// Try reading TECHO5's friendly name from /data/misc/techo5/config.json if available.
	data, err := os.ReadFile("/data/misc/techo5/config.json")
	if err == nil {
		var m struct {
			Device struct {
				FriendlyName string `json:"friendly_name"`
				Name         string `json:"name"`
			} `json:"device"`
		}
		if json.Unmarshal(data, &m) == nil {
			if s := strings.TrimSpace(m.Device.FriendlyName); s != "" {
				return s
			}
			if s := strings.TrimSpace(m.Device.Name); s != "" {
				return s
			}
		}
	}
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return strings.TrimSpace(h)
	}
	return "TECHO5"
}

func (d *Daemon) loadConfig() error {
	data, err := os.ReadFile(d.cfgPath)
	if err != nil {
		return err
	}
	var c sound.Config
	if err := json.Unmarshal(data, &c); err != nil {
		return err
	}
	if c.DeviceName == "" {
		c.DeviceName = detectDeviceName()
	}
	if c.MinConfidence <= 0 {
		c.MinConfidence = 0.25
	}
	if c.CooldownSec <= 0 {
		c.CooldownSec = 15
	}
	d.mu.Lock()
	d.cfg = c
	d.mu.Unlock()
	return nil
}

func (d *Daemon) saveConfig(c sound.Config) error {
	if err := os.MkdirAll(filepath.Dir(d.cfgPath), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := d.cfgPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, d.cfgPath); err != nil {
		return err
	}
	d.mu.Lock()
	d.cfg = c
	d.mqttDiscSent = false
	d.mu.Unlock()
	return nil
}

func (d *Daemon) Config() sound.Config {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cfg
}

// ReloadModel loads or reloads the YAMNet .tflite model from d.modelPath.
func (d *Daemon) ReloadModel() error {
	eng, err := sound.LoadEngine(d.modelPath)
	d.mu.Lock()
	old := d.eng
	if err == nil {
		d.eng = eng
	} else {
		d.eng = nil
	}
	d.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return err
}

func (d *Daemon) Close() {
	d.mu.Lock()
	eng := d.eng
	d.eng = nil
	d.mu.Unlock()
	if eng != nil {
		eng.Close()
	}
}

// FeedFrame pushes a 16 kHz mono int16 PCM frame into the classifier ring buffer.
func (d *Daemon) FeedFrame(samples []int16) {
	d.framesRx.Add(1)
	d.lastFrameNs.Store(time.Now().UnixNano())
	cp := append([]int16(nil), samples...)
	select {
	case d.pcmCh <- cp:
	default:
	}
}

// Run starts the Unix datagram socket listener (if sockPath != "") and the YAMNet window loop.
func (d *Daemon) Run(ctx context.Context) error {
	if d.sockPath != "" {
		_ = os.Remove(d.sockPath)
		if err := os.MkdirAll(filepath.Dir(d.sockPath), 0755); err != nil {
			return err
		}
		uconn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: d.sockPath, Net: "unixgram"})
		if err != nil {
			return fmt.Errorf("listen unixgram %s: %w", d.sockPath, err)
		}
		_ = os.Chmod(d.sockPath, 0600)
		defer func() {
			_ = uconn.Close()
			_ = os.Remove(d.sockPath)
		}()

		go func() {
			buf := make([]byte, 4096)
			for {
				n, _, err := uconn.ReadFromUnix(buf)
				if err != nil {
					if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
						return
					}
					continue
				}
				if n < 2 {
					continue
				}
				samples := make([]int16, n/2)
				for i := range samples {
					samples[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
				}
				d.FeedFrame(samples)
			}
		}()
	}

	ring := make([]int16, 0, WindowSamples*2)
	sinceHop := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case frame := <-d.pcmCh:
			ring = append(ring, frame...)
			sinceHop += len(frame)
			if len(ring) > WindowSamples {
				ring = ring[len(ring)-WindowSamples:]
			}
			if len(ring) < WindowSamples || sinceHop < HopSamples {
				continue
			}
			sinceHop = 0

			d.mu.RLock()
			eng := d.eng
			enabled := d.cfg.Enabled
			d.mu.RUnlock()
			if eng == nil || !enabled {
				continue
			}

			win := make([]int16, WindowSamples)
			copy(win, ring)
			rep, err := eng.Score(win)
			if err != nil {
				continue
			}
			d.handleReply(rep)
		}
	}
}

func (d *Daemon) handleReply(rep sound.EngineReply) {
	d.mu.RLock()
	sc := d.cfg
	d.mu.RUnlock()

	minConf := sc.MinConfidence
	if minConf <= 0 {
		minConf = 0.25
	}
	cooldown := time.Duration(sc.CooldownSec) * time.Second
	if cooldown <= 0 {
		cooldown = 15 * time.Second
	}

	det := sound.ClassifyWithMin(rep.Top, minConf)

	d.mu.Lock()
	if det.Category == "quiet" {
		d.prevCat = ""
		d.streak = 0
		d.quietHops++
		if d.quietHops >= 3 {
			d.current = "Quiet"
			d.category = "quiet"
			d.rawClass = ""
			d.confidence = 0
		}
		d.rmsDBFS = rep.RMSDBFS
		d.inferenceMS = rep.MS
		d.mu.Unlock()
		return
	}

	d.quietHops = 0
	if det.Category == d.prevCat {
		d.streak++
	} else {
		d.prevCat = det.Category
		d.streak = 1
	}

	urgent := det.Category == "smoke_alarm" || det.Category == "glass" || det.Category == "siren"
	if !urgent && det.Confidence < 0.45 && d.streak < 2 {
		d.rmsDBFS = rep.RMSDBFS
		d.inferenceMS = rep.MS
		d.mu.Unlock()
		return
	}

	now := time.Now()
	d.current = det.Label
	d.category = det.Category
	d.rawClass = det.RawClass
	d.confidence = math.Round(det.Confidence*1000) / 1000
	d.rmsDBFS = math.Round(rep.RMSDBFS*10) / 10
	d.inferenceMS = math.Round(rep.MS*10) / 10

	isAlert := false
	if sound.AlertEnabled(sc, det.Category) {
		last := d.lastAlertTime[det.Category]
		if last.IsZero() || now.Sub(last) >= cooldown {
			isAlert = true
			d.lastAlertTime[det.Category] = now
			d.lastAlert = det.Label
			d.lastAlertAt = now
		}
	}
	d.recordEventLocked(now, det, rep.RMSDBFS, isAlert)
	disc := !d.mqttDiscSent
	if disc && strings.TrimSpace(sc.MQTTBroker) != "" {
		d.mqttDiscSent = true
	}
	d.mu.Unlock()

	if isAlert || sc.MQTTAllSounds {
		go d.dispatchNotifications(sc, det, rep, isAlert, disc)
	}
}

func (d *Daemon) recordEventLocked(now time.Time, det sound.Detection, rms float64, isAlert bool) {
	ev := Event{
		Time:       now.Format("15:04:05"),
		Category:   det.Category,
		Label:      det.Label,
		RawClass:   det.RawClass,
		Confidence: math.Round(det.Confidence*100) / 100,
		RMSDBFS:    math.Round(rms*10) / 10,
		Alert:      isAlert,
	}
	if len(d.recent) > 0 && !isAlert {
		last := &d.recent[0]
		if last.Category == det.Category {
			if det.Confidence > last.Confidence {
				last.Confidence = ev.Confidence
			}
			last.Time = ev.Time
			return
		}
	}
	d.recent = append([]Event{ev}, d.recent...)
	if len(d.recent) > 25 {
		d.recent = d.recent[:25]
	}
}

func (d *Daemon) dispatchNotifications(sc sound.Config, det sound.Detection, rep sound.EngineReply, isAlert, includeDiscovery bool) {
	devName := sc.DeviceName
	if devName == "" {
		devName = "TECHO5"
	}
	var notifyErr string
	if isAlert && (strings.TrimSpace(sc.HAWebhookURL) != "" || (strings.TrimSpace(sc.HAURL) != "" && strings.TrimSpace(sc.HAService) != "")) {
		if err := sound.SendHANotification(d.httpClient, sc, devName, det, rep); err != nil {
			notifyErr = err.Error()
			slog.Warn("HA notification failed", "err", err)
		}
	}
	if strings.TrimSpace(sc.MQTTBroker) != "" && (isAlert || sc.MQTTAllSounds) {
		if err := sound.PublishMQTT(d.mqttDial, sc, devName, det, rep, isAlert, includeDiscovery); err != nil {
			notifyErr = "MQTT: " + err.Error()
			slog.Warn("MQTT publish failed", "err", err)
		}
	}
	d.mu.Lock()
	d.lastNotifyErr = notifyErr
	d.mu.Unlock()
}

func (d *Daemon) Status() Status {
	var modelSize int64
	if st, err := os.Stat(d.modelPath); err == nil && !st.IsDir() {
		modelSize = st.Size()
	}
	lastNs := d.lastFrameNs.Load()
	audioConn := lastNs > 0 && time.Since(time.Unix(0, lastNs)) < 5*time.Second

	d.mu.RLock()
	defer d.mu.RUnlock()
	var alertAt string
	if !d.lastAlertAt.IsZero() {
		alertAt = d.lastAlertAt.Format("15:04:05")
	}
	recent := make([]Event, len(d.recent))
	copy(recent, d.recent)
	return Status{
		ModelReady:     d.eng != nil && modelSize > 1024,
		ModelSize:      modelSize,
		Running:        d.eng != nil && d.cfg.Enabled,
		AudioConnected: audioConn,
		FramesReceived: d.framesRx.Load(),
		Current:        d.current,
		Category:       d.category,
		RawClass:       d.rawClass,
		Confidence:     d.confidence,
		RMSDBFS:        d.rmsDBFS,
		InferenceMS:    d.inferenceMS,
		LastAlert:      d.lastAlert,
		LastAlertAt:    alertAt,
		LastNotifyErr:  d.lastNotifyErr,
		Recent:         recent,
	}
}

// Handler returns the HTTP mux for the standalone Web UI and API endpoints.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", d.handleIndex)
	mux.HandleFunc("GET /api/status", d.handleAPIStatus)
	mux.HandleFunc("POST /setup/sound", d.handleSaveConfig)
	mux.HandleFunc("POST /setup/sound/test", d.handleTestNotify)
	mux.HandleFunc("POST /setup/sound/model/download", d.handleModelDownload)
	mux.HandleFunc("POST /setup/sound/model/upload", d.handleModelUpload)
	mux.HandleFunc("POST /setup/sound/model/remove", d.handleModelRemove)
	return mux
}

func (d *Daemon) checkToken(r *http.Request) bool {
	_ = r.ParseForm()
	return r.FormValue("token") == d.token
}

func (d *Daemon) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d.Status())
}

func (d *Daemon) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if !d.checkToken(r) {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	cur := d.Config()
	next := sound.Config{
		Enabled:       r.FormValue("enabled") == "1",
		DeviceName:    strings.TrimSpace(r.FormValue("device_name")),
		MinConfidence: cur.MinConfidence,
		CooldownSec:   cur.CooldownSec,
		Alerts:        r.Form["alerts"],
		HAWebhookURL:  strings.TrimSpace(r.FormValue("ha_webhook_url")),
		HAURL:         strings.TrimSpace(r.FormValue("ha_url")),
		HAToken:       strings.TrimSpace(r.FormValue("ha_token")),
		HAService:     strings.TrimSpace(r.FormValue("ha_service")),
		MQTTBroker:    strings.TrimSpace(r.FormValue("mqtt_broker")),
		MQTTUser:      strings.TrimSpace(r.FormValue("mqtt_user")),
		MQTTPass:      r.FormValue("mqtt_pass"),
		MQTTTopic:     strings.TrimSpace(r.FormValue("mqtt_topic")),
		MQTTDiscovery: r.FormValue("mqtt_discovery") == "1",
		MQTTAllSounds: r.FormValue("mqtt_all_sounds") == "1",
	}
	if next.DeviceName == "" {
		next.DeviceName = cur.DeviceName
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("min_confidence")), 64); err == nil && v >= 0.05 && v <= 0.95 {
		next.MinConfidence = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("cooldown_sec"))); err == nil && v >= 1 && v <= 3600 {
		next.CooldownSec = v
	}
	if next.Alerts == nil {
		next.Alerts = []string{}
	}
	// Preserve existing secrets when submitted blank unless clear is checked.
	if next.HAToken == "" && r.FormValue("ha_clear_token") != "1" {
		next.HAToken = cur.HAToken
	}
	if next.MQTTPass == "" && r.FormValue("mqtt_clear_pass") != "1" {
		next.MQTTPass = cur.MQTTPass
	}

	if err := d.saveConfig(next); err != nil {
		d.redirectMsg(w, r, "err", err.Error())
		return
	}
	d.redirectMsg(w, r, "msg", "Saved")
}

func (d *Daemon) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	if !d.checkToken(r) {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	sc := d.Config()
	det := sound.Detection{
		Category:   "test",
		Label:      "Test alert",
		RawClass:   "Test notification",
		Confidence: 0.99,
		Actionable: true,
	}
	rep := sound.EngineReply{MS: 32.0, RMSDBFS: -20.0}

	var sent []string
	var errs []string
	if strings.TrimSpace(sc.HAWebhookURL) != "" || (strings.TrimSpace(sc.HAURL) != "" && strings.TrimSpace(sc.HAService) != "") {
		if err := sound.SendHANotification(d.httpClient, sc, sc.DeviceName, det, rep); err != nil {
			errs = append(errs, "HA: "+err.Error())
		} else {
			sent = append(sent, "Home Assistant")
		}
	}
	if strings.TrimSpace(sc.MQTTBroker) != "" {
		if err := sound.PublishMQTT(d.mqttDial, sc, sc.DeviceName, det, rep, true, sc.MQTTDiscovery); err != nil {
			errs = append(errs, "MQTT: "+err.Error())
		} else {
			sent = append(sent, "MQTT")
		}
	}
	if len(errs) > 0 {
		d.redirectMsg(w, r, "err", strings.Join(errs, "; "))
		return
	}
	if len(sent) == 0 {
		d.redirectMsg(w, r, "err", "Configure a Home Assistant webhook/service or MQTT broker first")
		return
	}
	d.redirectMsg(w, r, "msg", "Test sent to "+strings.Join(sent, " + "))
}

func (d *Daemon) handleModelDownload(w http.ResponseWriter, r *http.Request) {
	if !d.checkToken(r) {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	dlClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := dlClient.Get(DefaultModelURL)
	if err != nil {
		d.redirectMsg(w, r, "err", "Download failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		d.redirectMsg(w, r, "err", fmt.Sprintf("Download returned HTTP %d", resp.StatusCode))
		return
	}
	if err := d.installModelReader(io.LimitReader(resp.Body, MaxModelBytes)); err != nil {
		d.redirectMsg(w, r, "err", err.Error())
		return
	}
	d.redirectMsg(w, r, "msg", "YAMNet model installed and active")
}

func (d *Daemon) handleModelUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(MaxModelBytes); err != nil {
		d.redirectMsg(w, r, "err", "Upload too large")
		return
	}
	if r.FormValue("token") != d.token {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	f, _, err := r.FormFile("model")
	if err != nil {
		d.redirectMsg(w, r, "err", "No model file chosen")
		return
	}
	defer f.Close()
	if err := d.installModelReader(io.LimitReader(f, MaxModelBytes)); err != nil {
		d.redirectMsg(w, r, "err", err.Error())
		return
	}
	d.redirectMsg(w, r, "msg", "YAMNet model uploaded and active")
}

func (d *Daemon) handleModelRemove(w http.ResponseWriter, r *http.Request) {
	if !d.checkToken(r) {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	d.Close()
	_ = os.Remove(d.modelPath)
	d.redirectMsg(w, r, "msg", "Model removed")
}

func (d *Daemon) installModelReader(r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(d.modelPath), 0755); err != nil {
		return err
	}
	tmp := d.modelPath + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, r)
	_ = out.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if n < 1024 {
		_ = os.Remove(tmp)
		return errors.New("file is too small to be a valid YAMNet .tflite model")
	}
	eng, err := sound.LoadEngine(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("invalid YAMNet .tflite model: %w", err)
	}
	eng.Close()
	if err := os.Rename(tmp, d.modelPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return d.ReloadModel()
}

func (d *Daemon) redirectMsg(w http.ResponseWriter, r *http.Request, key, val string) {
	http.Redirect(w, r, "/?"+key+"="+ strings.ReplaceAll(val, " ", "+"), http.StatusSeeOther)
}
