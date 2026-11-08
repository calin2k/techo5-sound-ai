package sound

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config holds the persistent configuration for techo5-sound-ai.
type Config struct {
	Enabled       bool               `json:"enabled"`
	DeviceName    string             `json:"device_name,omitempty"`
	MinConfidence float64            `json:"min_confidence"`
	CooldownSec   int                `json:"cooldown_sec"`
	Alerts        []string           `json:"alerts,omitempty"`
	CategoryMin   map[string]float64 `json:"category_min,omitempty"`

	// Home Assistant HTTP notification (Webhook URL and/or HA Base URL + Token + Service)
	HAWebhookURL string `json:"ha_webhook_url,omitempty"`
	HAURL        string `json:"ha_url,omitempty"`
	HAToken      string `json:"ha_token,omitempty"`
	HAService    string `json:"ha_service,omitempty"`

	// MQTT v3.1.1 broker & Home Assistant MQTT Discovery
	MQTTBroker    string `json:"mqtt_broker,omitempty"`
	MQTTUser      string `json:"mqtt_user,omitempty"`
	MQTTPass      string `json:"mqtt_pass,omitempty"`
	MQTTTopic     string `json:"mqtt_topic,omitempty"`
	MQTTDiscovery bool   `json:"mqtt_discovery"`
	MQTTAllSounds bool   `json:"mqtt_all_sounds"`
}

// DefaultConfig returns sensible defaults for a new installation.
func DefaultConfig(devName string) Config {
	if devName == "" {
		devName = "TECHO5"
	}
	return Config{
		Enabled:       true,
		DeviceName:    devName,
		MinConfidence: 0.25,
		CooldownSec:   15,
		Alerts:        append([]string(nil), DefaultAlertCategories...),
		MQTTDiscovery: true,
	}
}

// DefaultAlertCategories are the household alert sounds enabled when Alerts is unset.
var DefaultAlertCategories = []string{
	"smoke_alarm", "siren", "glass", "baby_cry", "doorbell", "alarm", "dog", "knock", "clapping",
}

// AlertChoice describes one selectable sound alert category.
type AlertChoice struct {
	Category string `json:"category"`
	Label    string `json:"label"`
}

var AlertChoices = []AlertChoice{
	{Category: "smoke_alarm", Label: "Smoke / fire alarm (informational)"},
	{Category: "siren", Label: "Siren"},
	{Category: "glass", Label: "Glass breaking"},
	{Category: "baby_cry", Label: "Baby cry"},
	{Category: "doorbell", Label: "Doorbell"},
	{Category: "alarm", Label: "Alarm / ring"},
	{Category: "dog", Label: "Dog"},
	{Category: "cat", Label: "Cat"},
	{Category: "knock", Label: "Knock"},
	{Category: "clapping", Label: "Clapping"},
	{Category: "finger_snap", Label: "Finger snapping"},
	{Category: "whistle", Label: "Whistling"},
	{Category: "cough", Label: "Cough"},
	{Category: "sneeze", Label: "Sneeze"},
	{Category: "laughter", Label: "Laughter"},
	{Category: "door", Label: "Door"},
}

// AlertEnabled reports whether category is selected in sc.Alerts (or DefaultAlertCategories when nil).
func AlertEnabled(sc Config, category string) bool {
	list := sc.Alerts
	if list == nil {
		list = DefaultAlertCategories
	}
	for _, c := range list {
		if c == category {
			return true
		}
	}
	return false
}

// DefaultTopic returns the MQTT topic prefix for devName.
func DefaultTopic(sc Config, devName string) string {
	if t := strings.Trim(strings.TrimSpace(sc.MQTTTopic), "/"); t != "" {
		return t
	}
	slug := SanitizeNode(devName)
	return "techo5/" + slug + "/sound"
}

// SanitizeNode converts a friendly device name into an MQTT/HA identifier slug.
func SanitizeNode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "techo5"
	}
	return out
}

// SendHANotification sends an alert to Home Assistant via Webhook URL and/or REST service call.
func SendHANotification(client *http.Client, sc Config, devName string, det Detection, rep EngineReply) error {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	if devName == "" {
		devName = "TECHO5"
	}
	webhookURL := strings.TrimSpace(sc.HAWebhookURL)
	haURL := strings.TrimRight(strings.TrimSpace(sc.HAURL), "/")
	haToken := strings.TrimSpace(sc.HAToken)
	service := strings.TrimSpace(sc.HAService)

	if webhookURL == "" && (haURL == "" || service == "") {
		return errors.New("no Home Assistant webhook URL or REST service is configured")
	}

	title := fmt.Sprintf("%s heard %s", devName, det.Label)
	var msg string
	switch {
	case rep.RMSDBFS == 0:
		msg = fmt.Sprintf("%s (%.0f%% confidence)", det.Label, det.Confidence*100)
	case det.RawClass == "" || det.RawClass == det.Label:
		msg = fmt.Sprintf("%s (%.0f%% confidence, %.1f dBFS)", det.Label, det.Confidence*100, rep.RMSDBFS)
	default:
		msg = fmt.Sprintf("%s (%.0f%% confidence, %.1f dBFS)", det.RawClass, det.Confidence*100, rep.RMSDBFS)
	}

	var firstErr error
	if webhookURL != "" {
		if !hasHTTPScheme(webhookURL) {
			firstErr = errors.New("webhook URL must start with http:// or https://")
		} else {
			payload, _ := json.Marshal(map[string]any{
				"title":        title,
				"message":      msg,
				"category":     det.Category,
				"sound":        det.Label,
				"raw_class":    det.RawClass,
				"confidence":   float64(int(det.Confidence*1000+0.5)) / 1000,
				"rms_dbfs":     float64(int(rep.RMSDBFS*10)) / 10,
				"inference_ms": float64(int(rep.MS*10)) / 10,
				"device":       devName,
				"timestamp":    time.Now().UTC().Format(time.RFC3339),
			})
			req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(payload))
			if err != nil {
				firstErr = err
			} else {
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					firstErr = err
				} else {
					_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
					resp.Body.Close()
					if resp.StatusCode >= 300 {
						firstErr = fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
					}
				}
			}
		}
	}

	if haURL != "" && service != "" {
		if !hasHTTPScheme(haURL) {
			return errors.New("Home Assistant URL must start with http:// or https://")
		}
		parts := strings.SplitN(service, ".", 2)
		if len(parts) != 2 || !isValidHASlug(parts[0]) || !isValidHASlug(parts[1]) {
			return errors.New("service must be domain.action, such as notify.notify or persistent_notification.create")
		}
		data := map[string]string{
			"title":   title,
			"message": msg,
		}
		if service == "persistent_notification.create" {
			data["notification_id"] = "techo5_sound_" + SanitizeNode(devName)
		}
		body, _ := json.Marshal(data)
		endpoint := fmt.Sprintf("%s/api/services/%s/%s", haURL, parts[0], parts[1])
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if haToken != "" {
			req.Header.Set("Authorization", "Bearer "+haToken)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("Home Assistant service returned HTTP %d", resp.StatusCode)
		}
	}

	return firstErr
}

func hasHTTPScheme(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func isValidHASlug(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// mqttMsg is one publish item in a single short-lived MQTT 3.1.1 session.
type mqttMsg struct {
	topic   string
	payload []byte
	retain  bool
}

// PublishMQTT publishes state/event messages (and optional Home Assistant MQTT Discovery config)
// to an MQTT 3.1.1 broker using only Go's standard library net package.
func PublishMQTT(dial func(network, addr string, timeout time.Duration) (net.Conn, error),
	sc Config, devName string, det Detection, rep EngineReply, isAlert, includeDiscovery bool) error {

	broker := strings.TrimSpace(sc.MQTTBroker)
	if broker == "" {
		return errors.New("no MQTT broker is set")
	}
	if _, _, err := net.SplitHostPort(broker); err != nil {
		broker = net.JoinHostPort(broker, "1883")
	}
	if devName == "" {
		devName = "TECHO5"
	}
	if dial == nil {
		dial = net.DialTimeout
	}

	prefix := DefaultTopic(sc, devName)
	node := SanitizeNode(devName)

	eventPayload, _ := json.Marshal(map[string]any{
		"category":     det.Category,
		"sound":        det.Label,
		"raw_class":    det.RawClass,
		"confidence":   float64(int(det.Confidence*1000+0.5)) / 1000,
		"rms_dbfs":     float64(int(rep.RMSDBFS*10)) / 10,
		"inference_ms": float64(int(rep.MS*10)) / 10,
		"alert":        isAlert,
		"device":       devName,
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
	})

	devBlock := map[string]any{
		"identifiers":  []string{"techo5_sound_ai_" + node},
		"name":         devName + " Sound AI",
		"manufacturer": "TECHO5 Add-On",
		"model":        "YAMNet Room Sound Classifier",
	}

	var msgs []mqttMsg
	if sc.MQTTDiscovery && includeDiscovery {
		discSensor, _ := json.Marshal(map[string]any{
			"name":                  "Room sound",
			"unique_id":             node + "_room_sound_mqtt",
			"state_topic":           prefix + "/state",
			"json_attributes_topic": prefix + "/event",
			"icon":                  "mdi:waveform",
			"device":                devBlock,
		})
		discAlertSensor, _ := json.Marshal(map[string]any{
			"name":                  "Last sound alert",
			"unique_id":             node + "_last_sound_alert_mqtt",
			"state_topic":           prefix + "/last_alert",
			"json_attributes_topic": prefix + "/alert_event",
			"icon":                  "mdi:bell-ring",
			"device":                devBlock,
		})
		msgs = append(msgs,
			mqttMsg{
				topic:   "homeassistant/sensor/" + node + "_room_sound/config",
				payload: discSensor,
				retain:  true,
			},
			mqttMsg{
				topic:   "homeassistant/sensor/" + node + "_last_sound_alert/config",
				payload: discAlertSensor,
				retain:  true,
			},
		)
	}
	msgs = append(msgs,
		mqttMsg{topic: prefix + "/state", payload: []byte(det.Label), retain: true},
		mqttMsg{topic: prefix + "/event", payload: eventPayload, retain: false},
	)
	if isAlert {
		msgs = append(msgs,
			mqttMsg{topic: prefix + "/last_alert", payload: []byte(det.Label), retain: true},
			mqttMsg{topic: prefix + "/alert_event", payload: eventPayload, retain: true},
		)
	}

	return mqttSend(dial, broker, "techo5-sound-"+node, sc.MQTTUser, sc.MQTTPass, msgs)
}

func mqttSend(dial func(string, string, time.Duration) (net.Conn, error),
	addr, clientID, user, pass string, msgs []mqttMsg) error {

	for _, s := range []string{clientID, user, pass} {
		if len(s) > 0xffff {
			return errors.New("mqtt: credential or client ID exceeds 65535 bytes")
		}
	}
	for _, m := range msgs {
		if m.topic == "" || len(m.topic) > 0xffff || strings.IndexByte(m.topic, 0) >= 0 {
			return errors.New("mqtt: invalid topic length or NUL byte")
		}
	}

	conn, err := dial("tcp", addr, 4*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var body bytes.Buffer
	writeMQTTString(&body, "MQTT")
	body.WriteByte(4)     // Protocol Level 4 = MQTT 3.1.1
	var flags byte = 0x02 // Clean Session
	if user != "" {
		flags |= 0x80
		if pass != "" {
			flags |= 0x40
		}
	}
	body.WriteByte(flags)
	body.Write([]byte{0x00, 0x1e}) // KeepAlive = 30s
	writeMQTTString(&body, clientID)
	if user != "" {
		writeMQTTString(&body, user)
		if pass != "" {
			writeMQTTString(&body, pass)
		}
	}

	if err := writeMQTTPacket(conn, 0x10, body.Bytes()); err != nil {
		return err
	}

	var ack [4]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return fmt.Errorf("mqtt connack: %w", err)
	}
	if ack[0] != 0x20 || ack[1] != 0x02 || ack[3] != 0x00 {
		return fmt.Errorf("mqtt broker refused connection (hdr=%#x code=%d)", ack[0], ack[3])
	}

	for _, m := range msgs {
		var pub bytes.Buffer
		writeMQTTString(&pub, m.topic)
		pub.Write(m.payload)
		hdr := byte(0x30)
		if m.retain {
			hdr |= 0x01
		}
		if err := writeMQTTPacket(conn, hdr, pub.Bytes()); err != nil {
			return err
		}
	}

	_, _ = conn.Write([]byte{0xe0, 0x00})
	return nil
}

// MQTTClient maintains a persistent MQTT 3.1.1 connection with Last Will and Testament
// (LWT) availability ("online" / "offline") and transparent reconnect on broken pipes.
type MQTTClient struct {
	mu      sync.Mutex
	dial    func(network, addr string, timeout time.Duration) (net.Conn, error)
	conn    net.Conn
	connKey string
	avail   string
}

// NewMQTTClient creates a persistent MQTT 3.1.1 publisher.
func NewMQTTClient(dial func(network, addr string, timeout time.Duration) (net.Conn, error)) *MQTTClient {
	if dial == nil {
		dial = net.DialTimeout
	}
	return &MQTTClient{dial: dial}
}

// Publish sends the detection state/alert messages over the persistent MQTT connection,
// reconnecting automatically if the broker closed the socket or configuration changed.
func (c *MQTTClient) Publish(sc Config, devName string, det Detection, rep EngineReply, isAlert, includeDiscovery bool) error {
	broker := strings.TrimSpace(sc.MQTTBroker)
	if broker == "" {
		return errors.New("no MQTT broker is set")
	}
	if _, _, err := net.SplitHostPort(broker); err != nil {
		broker = net.JoinHostPort(broker, "1883")
	}
	if devName == "" {
		devName = "TECHO5"
	}
	prefix := DefaultTopic(sc, devName)
	node := SanitizeNode(devName)
	availTopic := prefix + "/availability"
	clientID := "techo5-sound-" + node

	for _, s := range []string{clientID, sc.MQTTUser, sc.MQTTPass, availTopic} {
		if len(s) > 0xffff {
			return errors.New("mqtt: credential, client ID, or topic exceeds 65535 bytes")
		}
	}

	eventPayload, _ := json.Marshal(map[string]any{
		"category":     det.Category,
		"sound":        det.Label,
		"raw_class":    det.RawClass,
		"confidence":   float64(int(det.Confidence*1000+0.5)) / 1000,
		"rms_dbfs":     float64(int(rep.RMSDBFS*10)) / 10,
		"inference_ms": float64(int(rep.MS*10)) / 10,
		"alert":        isAlert,
		"device":       devName,
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
	})

	devBlock := map[string]any{
		"identifiers":  []string{"techo5_sound_ai_" + node},
		"name":         devName + " Sound AI",
		"manufacturer": "TECHO5 Add-On",
		"model":        "YAMNet Room Sound Classifier",
	}

	var msgs []mqttMsg
	if sc.MQTTDiscovery && includeDiscovery {
		discSensor, _ := json.Marshal(map[string]any{
			"name":                  "Room sound",
			"unique_id":             node + "_room_sound_mqtt",
			"state_topic":           prefix + "/state",
			"json_attributes_topic": prefix + "/event",
			"availability_topic":    availTopic,
			"icon":                  "mdi:waveform",
			"device":                devBlock,
		})
		discAlertSensor, _ := json.Marshal(map[string]any{
			"name":                  "Last sound alert",
			"unique_id":             node + "_last_sound_alert_mqtt",
			"state_topic":           prefix + "/last_alert",
			"json_attributes_topic": prefix + "/alert_event",
			"availability_topic":    availTopic,
			"icon":                  "mdi:bell-ring",
			"device":                devBlock,
		})
		msgs = append(msgs,
			mqttMsg{topic: "homeassistant/sensor/" + node + "_room_sound/config", payload: discSensor, retain: true},
			mqttMsg{topic: "homeassistant/sensor/" + node + "_last_sound_alert/config", payload: discAlertSensor, retain: true},
		)
	}
	msgs = append(msgs,
		mqttMsg{topic: prefix + "/state", payload: []byte(det.Label), retain: true},
		mqttMsg{topic: prefix + "/event", payload: eventPayload, retain: false},
	)
	if isAlert {
		msgs = append(msgs,
			mqttMsg{topic: prefix + "/last_alert", payload: []byte(det.Label), retain: true},
			mqttMsg{topic: prefix + "/alert_event", payload: eventPayload, retain: true},
		)
	}
	for _, m := range msgs {
		if m.topic == "" || len(m.topic) > 0xffff || strings.IndexByte(m.topic, 0) >= 0 {
			return errors.New("mqtt: invalid topic length or NUL byte")
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	key := broker + "\x00" + clientID + "\x00" + sc.MQTTUser + "\x00" + sc.MQTTPass + "\x00" + availTopic
	if c.conn != nil && c.connKey != key {
		c.closeLocked()
	}

	for attempt := 0; attempt < 2; attempt++ {
		if c.conn == nil {
			if err := c.connectLocked(broker, clientID, sc.MQTTUser, sc.MQTTPass, availTopic, key); err != nil {
				if attempt == 1 {
					return err
				}
				continue
			}
		}
		if err := c.writePublishesLocked(msgs); err != nil {
			c.dropLocked()
			if attempt == 1 {
				return err
			}
			continue
		}
		return nil
	}
	return errors.New("mqtt: publish failed")
}

func (c *MQTTClient) connectLocked(addr, clientID, user, pass, availTopic, key string) error {
	dial := c.dial
	if dial == nil {
		dial = net.DialTimeout
	}
	conn, err := dial("tcp", addr, 4*time.Second)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var body bytes.Buffer
	writeMQTTString(&body, "MQTT")
	body.WriteByte(4) // MQTT 3.1.1
	// Clean Session (0x02) + Will Flag (0x04) + Will Retain (0x20)
	var flags byte = 0x02 | 0x04 | 0x20
	if user != "" {
		flags |= 0x80
		if pass != "" {
			flags |= 0x40
		}
	}
	body.WriteByte(flags)
	body.Write([]byte{0x00, 0x3c}) // KeepAlive = 60s
	writeMQTTString(&body, clientID)
	writeMQTTString(&body, availTopic)
	writeMQTTString(&body, "offline")
	if user != "" {
		writeMQTTString(&body, user)
		if pass != "" {
			writeMQTTString(&body, pass)
		}
	}

	if err := writeMQTTPacket(conn, 0x10, body.Bytes()); err != nil {
		_ = conn.Close()
		return err
	}
	var ack [4]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		_ = conn.Close()
		return fmt.Errorf("mqtt connack: %w", err)
	}
	if ack[0] != 0x20 || ack[1] != 0x02 || ack[3] != 0x00 {
		_ = conn.Close()
		return fmt.Errorf("mqtt broker refused connection (hdr=%#x code=%d)", ack[0], ack[3])
	}

	// Publish retained "online" availability birth message.
	var birth bytes.Buffer
	writeMQTTString(&birth, availTopic)
	birth.WriteString("online")
	if err := writeMQTTPacket(conn, 0x31, birth.Bytes()); err != nil {
		_ = conn.Close()
		return err
	}

	c.conn = conn
	c.connKey = key
	c.avail = availTopic
	return nil
}

func (c *MQTTClient) writePublishesLocked(msgs []mqttMsg) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	for _, m := range msgs {
		var pub bytes.Buffer
		writeMQTTString(&pub, m.topic)
		pub.Write(m.payload)
		hdr := byte(0x30)
		if m.retain {
			hdr |= 0x01
		}
		if err := writeMQTTPacket(c.conn, hdr, pub.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

func (c *MQTTClient) dropLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *MQTTClient) closeLocked() {
	if c.conn == nil {
		return
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	if c.avail != "" {
		var off bytes.Buffer
		writeMQTTString(&off, c.avail)
		off.WriteString("offline")
		_ = writeMQTTPacket(c.conn, 0x31, off.Bytes())
	}
	_, _ = c.conn.Write([]byte{0xe0, 0x00})
	_ = c.conn.Close()
	c.conn = nil
	c.connKey = ""
	c.avail = ""
}

// Close publishes retained "offline" availability and cleanly disconnects from the broker.
func (c *MQTTClient) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
	return nil
}

func writeMQTTString(b *bytes.Buffer, s string) {
	n := len(s)
	b.WriteByte(byte(n >> 8))
	b.WriteByte(byte(n))
	b.WriteString(s)
}

func writeMQTTPacket(w io.Writer, hdr byte, body []byte) error {
	if len(body) > 268435455 {
		return errors.New("mqtt: packet payload exceeds maximum remaining length")
	}
	var pkt bytes.Buffer
	pkt.WriteByte(hdr)
	rem := len(body)
	for {
		enc := byte(rem % 128)
		rem /= 128
		if rem > 0 {
			enc |= 0x80
		}
		pkt.WriteByte(enc)
		if rem == 0 {
			break
		}
	}
	pkt.Write(body)
	_, err := w.Write(pkt.Bytes())
	return err
}
