package addon

import (
	"fmt"
	"html/template"
	"net/http"

	"github.com/calin2k/techo5-sound-ai/internal/sound"
)

type alertItemView struct {
	Category string
	Label    string
	Checked  bool
}

type pageView struct {
	Token        string
	Msg          string
	Err          string
	Status       Status
	Config       sound.Config
	ModelSizeKB  int64
	DefaultTopic string
	HasHAToken   bool
	HasMQTTPass  bool
	AlertChoices []alertItemView
}

func (d *Daemon) handleIndex(w http.ResponseWriter, r *http.Request) {
	st := d.Status()
	cfg := d.Config()
	choices := make([]alertItemView, 0, len(sound.AlertChoices))
	for _, ch := range sound.AlertChoices {
		choices = append(choices, alertItemView{
			Category: ch.Category,
			Label:    ch.Label,
			Checked:  sound.AlertEnabled(cfg, ch.Category),
		})
	}
	v := pageView{
		Token:        d.token,
		Msg:          r.URL.Query().Get("msg"),
		Err:          r.URL.Query().Get("err"),
		Status:       st,
		Config:       cfg,
		ModelSizeKB:  st.ModelSize / 1024,
		DefaultTopic: sound.DefaultTopic(cfg, cfg.DeviceName),
		HasHAToken:   cfg.HAToken != "",
		HasMQTTPass:  cfg.MQTTPass != "",
		AlertChoices: choices,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, v); err != nil {
		http.Error(w, fmt.Sprintf("template: %v", err), http.StatusInternalServerError)
	}
}

var indexTmpl = template.Must(template.New("index").Funcs(template.FuncMap{
	"mulf": func(a, b float64) float64 { return a * b },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Config.DeviceName}} — Room Sound AI Add-On</title>
<style>
:root {
  --bg: #0f1318;
  --panel: #171d24;
  --border: #26303c;
  --text: #e6edf3;
  --muted: #8b949e;
  --accent: #3fb950;
  --warn: #d29922;
  --danger: #f85149;
  --blue: #58a6ff;
}
* { box-sizing: border-box; }
body {
  margin: 0;
  padding: 24px 16px 48px;
  background: var(--bg);
  color: var(--text);
  font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
}
.wrap { max-width: 780px; margin: 0 auto; }
header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
  padding-bottom: 14px;
  border-bottom: 1px solid var(--border);
}
h1 { margin: 0; font-size: 20px; font-weight: 600; }
.sub { color: var(--muted); font-size: 13px; margin-top: 2px; }
.badge {
  display: inline-block;
  padding: 3px 10px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 600;
  background: #1f2937;
  color: var(--muted);
}
.badge.ok { background: rgba(63,185,80,0.16); color: var(--accent); }
.badge.warn { background: rgba(210,153,34,0.16); color: var(--warn); }
.card {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 18px 20px;
  margin-bottom: 16px;
}
.card h2 {
  margin: 0 0 12px;
  font-size: 15px;
  font-weight: 600;
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.grid2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
.stat {
  background: rgba(255,255,255,0.02);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px 12px;
}
.stat .k { color: var(--muted); font-size: 12px; }
.stat .v { font-size: 16px; font-weight: 600; margin-top: 2px; }
label.row { display: block; margin-bottom: 12px; }
label.row span.lbl { display: block; font-weight: 500; margin-bottom: 4px; }
label.row span.hint { display: block; color: var(--muted); font-size: 12px; margin-top: 3px; }
input[type=text], input[type=password], input[type=number] {
  width: 100%;
  padding: 8px 10px;
  background: #0d1117;
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 14px;
}
.checks {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(210px, 1fr));
  gap: 6px 12px;
  margin: 8px 0 4px;
}
.checks label {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  padding: 4px 0;
}
.btns { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; margin-top: 12px; }
button, .btn {
  padding: 8px 14px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: #21262d;
  color: var(--text);
  font-weight: 500;
  cursor: pointer;
  font-size: 13px;
}
button.primary { background: #238636; border-color: rgba(240,246,252,0.1); color: #fff; }
button.danger { background: rgba(248,81,73,0.12); border-color: rgba(248,81,73,0.4); color: var(--danger); }
.banner {
  padding: 10px 14px;
  border-radius: 8px;
  margin-bottom: 16px;
  font-size: 13px;
}
.banner.ok { background: rgba(63,185,80,0.15); border: 1px solid rgba(63,185,80,0.4); color: #7ee787; }
.banner.err { background: rgba(248,81,73,0.15); border: 1px solid rgba(248,81,73,0.4); color: #ffa198; }
.note {
  padding: 10px 12px;
  border-radius: 6px;
  background: rgba(210,153,34,0.1);
  border: 1px solid rgba(210,153,34,0.3);
  color: #e3b341;
  font-size: 12px;
  margin-top: 10px;
}
table { width: 100%; border-collapse: collapse; font-size: 13px; margin-top: 8px; }
th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--border); }
th { color: var(--muted); font-weight: 500; }
</style>
</head>
<body>
<div class="wrap">
  <header>
    <div>
      <h1>Room Sound AI Add-On <span style="font-size:12px;font-weight:500;color:var(--muted)">{{.Status.Version}}</span></h1>
      <div class="sub">On-device Google YAMNet (521 AudioSet classes) for {{.Config.DeviceName}}</div>
    </div>
    <span id="run-badge" class="badge {{if .Status.Running}}ok{{else}}warn{{end}}">
      {{if .Status.Running}}Active{{else if not .Status.ModelReady}}Model needed{{else}}Paused{{end}}
    </span>
  </header>

  {{if .Msg}}<div class="banner ok">{{.Msg}}</div>{{end}}
  {{if .Err}}<div class="banner err">{{.Err}}</div>{{end}}
  <div id="notify-err" class="banner err" style="{{if not .Status.LastNotifyErr}}display:none{{end}}">{{.Status.LastNotifyErr}}</div>

  <div class="card">
    <h2>
      <span>Live Watch</span>
      <span id="audio-pill" class="badge {{if .Status.AudioConnected}}ok{{else}}warn{{end}}">
        {{if .Status.AudioConnected}}Mic stream connected ({{.Status.FramesReceived}} frames){{else}}Waiting for External ASP stream{{end}}
      </span>
    </h2>
    <div class="grid2">
      <div class="stat">
        <div class="k">Current sound</div>
        <div class="v" id="st-current">{{.Status.Current}}</div>
      </div>
      <div class="stat">
        <div class="k">Confidence / Level</div>
        <div class="v" id="st-conf">{{if gt .Status.Confidence 0.0}}{{printf "%.0f%%" (mulf .Status.Confidence 100)}} ({{printf "%.1f" .Status.RMSDBFS}} dBFS){{else}}Floor {{printf "%.1f" .Status.FloorDBFS}} dBFS{{end}}</div>
      </div>
      <div class="stat">
        <div class="k">Last alert</div>
        <div class="v" id="st-alert">{{if .Status.LastAlert}}{{.Status.LastAlert}} ({{.Status.LastAlertAt}}){{else}}None{{end}}</div>
      </div>
      <div class="stat">
        <div class="k">Inference latency</div>
        <div class="v" id="st-ms">{{if gt .Status.InferenceMS 0.0}}{{printf "%.1f ms / hop" .Status.InferenceMS}}{{else}}Idle (energy gate){{end}}</div>
      </div>
    </div>
    <div id="recent-wrap" style="margin-top:14px">
      {{if .Status.Recent}}
      <table>
        <thead><tr><th>Time</th><th>Sound</th><th>AudioSet class</th><th>Confidence</th><th>Level</th></tr></thead>
        <tbody id="recent-body">
          {{range .Status.Recent}}
          <tr>
            <td>{{.Time}}</td>
            <td>{{.Label}}{{if .Alert}} <span class="badge ok">Alert</span>{{end}}</td>
            <td>{{.RawClass}}</td>
            <td>{{printf "%.0f%%" (mulf .Confidence 100)}}</td>
            <td>{{printf "%.1f dBFS" .RMSDBFS}}</td>
          </tr>
          {{end}}
        </tbody>
      </table>
      {{end}}
    </div>
  </div>

  <div class="card">
    <h2>
      <span>YAMNet TFLite Model</span>
      <span class="badge {{if .Status.ModelReady}}ok{{else}}warn{{end}}">
        {{if .Status.ModelReady}}Installed ({{.ModelSizeKB}} KB){{else}}Not installed{{end}}
      </span>
    </h2>
    <p style="margin:0 0 10px;color:var(--muted);font-size:13px">
      Stored on userdata at <code>/data/misc/techo5/yamnet/yamnet.tflite</code> (read-only mmap, &lt; 1 MB RAM, survives TECHO5 OTA updates).
    </p>
    <div class="btns">
      <form method="post" action="/setup/sound/model/download" style="margin:0">
        <input type="hidden" name="token" value="{{.Token}}">
        <button type="submit" class="primary">{{if .Status.ModelReady}}Re-download YAMNet (4.0 MB){{else}}Download Google YAMNet (4.0 MB){{end}}</button>
      </form>
      <form method="post" action="/setup/sound/model/upload" enctype="multipart/form-data" style="display:flex;gap:8px;align-items:center;margin:0">
        <input type="hidden" name="token" value="{{.Token}}">
        <input type="file" name="model" accept=".tflite" style="font-size:12px;color:var(--muted)">
        <button type="submit">Upload .tflite</button>
      </form>
      {{if .Status.ModelReady}}
      <form method="post" action="/setup/sound/model/remove" style="margin:0">
        <input type="hidden" name="token" value="{{.Token}}">
        <button type="submit" class="danger">Remove</button>
      </form>
      {{end}}
    </div>
  </div>

  <form method="post" action="/setup/sound">
    <input type="hidden" name="token" value="{{.Token}}">

    <div class="card">
      <h2>Classifier &amp; Alert Categories</h2>
      <label style="display:flex;align-items:center;gap:8px;margin-bottom:14px;cursor:pointer">
        <input type="checkbox" name="enabled" value="1" {{if .Config.Enabled}}checked{{end}}>
        <strong>Enable on-device room sound classification</strong>
      </label>
      <div class="grid2">
        <label class="row">
          <span class="lbl">Device name</span>
          <input type="text" name="device_name" value="{{.Config.DeviceName}}">
        </label>
        <label class="row">
          <span class="lbl">Minimum confidence (0.10 – 0.90)</span>
          <input type="number" step="0.05" min="0.10" max="0.90" name="min_confidence" value="{{printf "%.2f" .Config.MinConfidence}}">
        </label>
        <label class="row">
          <span class="lbl">Alert cooldown (seconds)</span>
          <input type="number" min="1" max="3600" name="cooldown_sec" value="{{.Config.CooldownSec}}">
        </label>
      </div>

      <div style="margin-top:8px;font-weight:500">Alert on these sounds:</div>
      <div class="checks">
        {{range .AlertChoices}}
        <label>
          <input type="checkbox" name="alerts" value="{{.Category}}" {{if .Checked}}checked{{end}}>
          <span>{{.Label}}</span>
        </label>
        {{end}}
      </div>
      <div class="note">
        <strong>Safety disclaimer:</strong> Sound classification (including smoke/fire alarm and glass-break recognition) is an experimental, informational convenience feature and is <strong>not</strong> a certified life-safety or UL/EN smoke detection system. Always rely on dedicated, certified smoke and fire alarms.
      </div>
    </div>

    <div class="card">
      <h2>Home Assistant Notifications</h2>
      <label class="row">
        <span class="lbl">Home Assistant Webhook URL (easiest — no token required)</span>
        <input type="text" name="ha_webhook_url" value="{{.Config.HAWebhookURL}}" placeholder="http://homeassistant.local:8123/api/webhook/techo5_sound">
        <span class="hint">POSTs JSON with title, message, category, sound, confidence, rms_dbfs, and device whenever an alert fires.</span>
      </label>
      <div class="grid2">
        <label class="row">
          <span class="lbl">Home Assistant URL (optional REST service call)</span>
          <input type="text" name="ha_url" value="{{.Config.HAURL}}" placeholder="http://homeassistant.local:8123">
        </label>
        <label class="row">
          <span class="lbl">Notification service</span>
          <input type="text" name="ha_service" value="{{.Config.HAService}}" placeholder="persistent_notification.create or notify.mobile_app_phone">
        </label>
      </div>
      <label class="row">
        <span class="lbl">Long-Lived Access Token {{if .HasHAToken}}(saved — leave blank to keep){{end}}</span>
        <input type="password" name="ha_token" value="" placeholder="{{if .HasHAToken}}••••••••{{else}}eyJhbGciOi...{{end}}">
      </label>
      {{if .HasHAToken}}
      <label style="display:flex;align-items:center;gap:8px;font-size:12px;color:var(--muted)">
        <input type="checkbox" name="ha_clear_token" value="1"> Clear saved token
      </label>
      {{end}}
    </div>

    <div class="card">
      <h2>MQTT &amp; Home Assistant Auto-Discovery</h2>
      <div class="grid2">
        <label class="row">
          <span class="lbl">MQTT broker (host or host:port)</span>
          <input type="text" name="mqtt_broker" value="{{.Config.MQTTBroker}}" placeholder="192.168.1.10:1883">
        </label>
        <label class="row">
          <span class="lbl">Topic prefix</span>
          <input type="text" name="mqtt_topic" value="{{.Config.MQTTTopic}}" placeholder="{{.DefaultTopic}}">
        </label>
        <label class="row">
          <span class="lbl">MQTT username</span>
          <input type="text" name="mqtt_user" value="{{.Config.MQTTUser}}">
        </label>
        <label class="row">
          <span class="lbl">MQTT password {{if .HasMQTTPass}}(saved — leave blank to keep){{end}}</span>
          <input type="password" name="mqtt_pass" value="" placeholder="{{if .HasMQTTPass}}••••••••{{end}}">
        </label>
      </div>
      {{if .HasMQTTPass}}
      <label style="display:flex;align-items:center;gap:8px;font-size:12px;color:var(--muted);margin-bottom:8px">
        <input type="checkbox" name="mqtt_clear_pass" value="1"> Clear saved MQTT password
      </label>
      {{end}}
      <label style="display:flex;align-items:center;gap:8px;margin-top:6px;cursor:pointer">
        <input type="checkbox" name="mqtt_discovery" value="1" {{if .Config.MQTTDiscovery}}checked{{end}}>
        <span>Publish Home Assistant MQTT Auto-Discovery sensors (<code>Room sound</code> &amp; <code>Last sound alert</code>)</span>
      </label>
      <label style="display:flex;align-items:center;gap:8px;margin-top:6px;cursor:pointer">
        <input type="checkbox" name="mqtt_all_sounds" value="1" {{if .Config.MQTTAllSounds}}checked{{end}}>
        <span>Publish ambient sounds (speech, music, etc.) as well as alerts</span>
      </label>

      <div class="btns" style="margin-top:16px">
        <button type="submit" class="primary">Save settings</button>
        <button type="submit" formaction="/setup/sound/test">Send test notification</button>
      </div>
    </div>
  </form>
</div>
<script>
function esc(s) {
  const d = document.createElement('div');
  d.textContent = s || '';
  return d.innerHTML;
}
setInterval(async () => {
  try {
    const r = await fetch('/api/status');
    if (!r.ok) return;
    const s = await r.json();
    document.getElementById('st-current').textContent = s.current || 'Quiet';
    document.getElementById('st-conf').textContent = s.confidence > 0
      ? Math.round(s.confidence * 100) + '% (' + s.rms_dbfs.toFixed(1) + ' dBFS)'
      : 'Floor ' + (s.floor_dbfs || -60).toFixed(1) + ' dBFS';
    document.getElementById('st-alert').textContent = s.last_alert
      ? s.last_alert + ' (' + s.last_alert_at + ')'
      : 'None';
    document.getElementById('st-ms').textContent = s.inference_ms > 0
      ? s.inference_ms.toFixed(1) + ' ms / hop'
      : 'Idle (energy gate)';
    const pill = document.getElementById('audio-pill');
    if (pill) {
      pill.textContent = s.audio_connected ? 'Mic stream connected (' + s.frames_received + ' frames)' : 'Waiting for External ASP stream';
      pill.className = 'badge ' + (s.audio_connected ? 'ok' : 'warn');
    }
    const badge = document.getElementById('run-badge');
    if (badge) {
      badge.textContent = s.running ? 'Active' : (!s.model_ready ? 'Model needed' : 'Paused');
      badge.className = 'badge ' + (s.running ? 'ok' : 'warn');
    }
    const nerr = document.getElementById('notify-err');
    if (nerr) {
      if (s.last_notify_err) {
        nerr.textContent = s.last_notify_err;
        nerr.style.display = 'block';
      } else {
        nerr.style.display = 'none';
      }
    }
    const wrap = document.getElementById('recent-wrap');
    if (wrap && Array.isArray(s.recent) && s.recent.length > 0) {
      let rows = '';
      for (const ev of s.recent) {
        const tag = ev.alert ? ' <span class="badge ok">Alert</span>' : '';
        rows += '<tr><td>' + esc(ev.time) + '</td><td>' + esc(ev.label) + tag + '</td><td>' +
          esc(ev.raw_class) + '</td><td>' + Math.round((ev.confidence || 0) * 100) + '%</td><td>' +
          (ev.rms_dbfs || 0).toFixed(1) + ' dBFS</td></tr>';
      }
      wrap.innerHTML = '<table><thead><tr><th>Time</th><th>Sound</th><th>AudioSet class</th><th>Confidence</th><th>Level</th></tr></thead><tbody>' + rows + '</tbody></table>';
    }
  } catch (e) {}
}, 2000);
</script>
</body>
</html>
`))
