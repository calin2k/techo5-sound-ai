package addon

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/calin2k/techo5-sound-ai/internal/sound"
)

func escHTML(s string) string {
	if !strings.ContainsAny(s, "&<>\"'") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&#34;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func (d *Daemon) handleIndex(w http.ResponseWriter, r *http.Request) {
	st := d.Status()
	cfg := d.Config()
	msg := r.URL.Query().Get("msg")
	errMsg := r.URL.Query().Get("err")
	modelSizeKB := st.ModelSize / 1024
	defaultTopic := sound.DefaultTopic(cfg, cfg.DeviceName)
	hasHAToken := cfg.HAToken != ""
	hasMQTTPass := cfg.MQTTPass != ""

	runBadgeClass := "warn"
	runBadgeText := "Paused"
	if st.Running {
		runBadgeClass = "ok"
		runBadgeText = "Active"
	} else if !st.ModelReady {
		runBadgeText = "Model needed"
	}

	audioBadgeClass := "warn"
	audioBadgeText := "Waiting for External ASP stream"
	if st.AudioConnected {
		audioBadgeClass = "ok"
		audioBadgeText = fmt.Sprintf("Mic stream connected (%d frames)", st.FramesReceived)
	}

	confText := fmt.Sprintf("Floor %.1f dBFS", st.FloorDBFS)
	if st.Confidence > 0 {
		confText = fmt.Sprintf("%.0f%% (%.1f dBFS)", st.Confidence*100, st.RMSDBFS)
	}

	lastAlertText := "None"
	if st.LastAlert != "" {
		lastAlertText = escHTML(st.LastAlert) + " (" + escHTML(st.LastAlertAt) + ")"
	}

	infText := "Idle (energy gate)"
	if st.InferenceMS > 0 {
		infText = fmt.Sprintf("%.1f ms / hop", st.InferenceMS)
	}

	modelBadgeClass := "warn"
	modelBadgeText := "Not installed"
	dlBtnText := "Download Google YAMNet (4.0 MB)"
	if st.ModelReady {
		modelBadgeClass = "ok"
		modelBadgeText = fmt.Sprintf("Installed (%d KB)", modelSizeKB)
		dlBtnText = "Re-download YAMNet (4.0 MB)"
	}

	var b strings.Builder
	b.Grow(12288)
	b.WriteString(indexHeadPrefix)
	b.WriteString(escHTML(cfg.DeviceName))
	b.WriteString(indexHeadStyle)
	b.WriteString(escHTML(st.Version))
	b.WriteString(`</span></h1><div class="sub">On-device Google YAMNet (521 AudioSet classes) for `)
	b.WriteString(escHTML(cfg.DeviceName))
	b.WriteString(`</div></div><span id="run-badge" class="badge `)
	b.WriteString(runBadgeClass)
	b.WriteString(`">`)
	b.WriteString(runBadgeText)
	b.WriteString(`</span></header>`)

	if msg != "" {
		b.WriteString(`<div class="banner ok">`)
		b.WriteString(escHTML(msg))
		b.WriteString(`</div>`)
	}
	if errMsg != "" {
		b.WriteString(`<div class="banner err">`)
		b.WriteString(escHTML(errMsg))
		b.WriteString(`</div>`)
	}
	b.WriteString(`<div id="notify-err" class="banner err" style="`)
	if st.LastNotifyErr == "" {
		b.WriteString(`display:none`)
	}
	b.WriteString(`">`)
	b.WriteString(escHTML(st.LastNotifyErr))
	b.WriteString(`</div>`)

	b.WriteString(`<div class="card"><h2><span>Live Watch</span><span id="audio-pill" class="badge `)
	b.WriteString(audioBadgeClass)
	b.WriteString(`">`)
	b.WriteString(audioBadgeText)
	b.WriteString(`</span></h2><div class="grid2">`)
	b.WriteString(`<div class="stat"><div class="k">Current sound</div><div class="v" id="st-current">`)
	b.WriteString(escHTML(st.Current))
	b.WriteString(`</div></div>`)
	b.WriteString(`<div class="stat"><div class="k">Confidence / Level</div><div class="v" id="st-conf">`)
	b.WriteString(confText)
	b.WriteString(`</div></div>`)
	b.WriteString(`<div class="stat"><div class="k">Last alert</div><div class="v" id="st-alert">`)
	b.WriteString(lastAlertText)
	b.WriteString(`</div></div>`)
	b.WriteString(`<div class="stat"><div class="k">Inference latency</div><div class="v" id="st-ms">`)
	b.WriteString(infText)
	b.WriteString(`</div></div></div><div id="recent-wrap" style="margin-top:14px">`)

	if len(st.Recent) > 0 {
		b.WriteString(`<table><thead><tr><th>Time</th><th>Sound</th><th>AudioSet class</th><th>Confidence</th><th>Level</th></tr></thead><tbody id="recent-body">`)
		for _, ev := range st.Recent {
			b.WriteString(`<tr><td>`)
			b.WriteString(escHTML(ev.Time))
			b.WriteString(`</td><td>`)
			b.WriteString(escHTML(ev.Label))
			if ev.Alert {
				b.WriteString(` <span class="badge ok">Alert</span>`)
			}
			b.WriteString(`</td><td>`)
			b.WriteString(escHTML(ev.RawClass))
			b.WriteString(fmt.Sprintf(`</td><td>%.0f%%</td><td>%.1f dBFS</td></tr>`, ev.Confidence*100, ev.RMSDBFS))
		}
		b.WriteString(`</tbody></table>`)
	}
	b.WriteString(`</div></div>`)

	escTok := escHTML(d.token)
	b.WriteString(`<div class="card"><h2><span>YAMNet TFLite Model</span><span class="badge `)
	b.WriteString(modelBadgeClass)
	b.WriteString(`">`)
	b.WriteString(modelBadgeText)
	b.WriteString(`</span></h2><p style="margin:0 0 10px;color:var(--muted);font-size:13px">Stored on userdata at <code>/data/misc/techo5/yamnet/yamnet.tflite</code> (read-only mmap, &lt; 1 MB RAM, survives TECHO5 OTA updates).</p><div class="btns">`)
	b.WriteString(`<form method="post" action="/setup/sound/model/download" style="margin:0"><input type="hidden" name="token" value="`)
	b.WriteString(escTok)
	b.WriteString(`"><button type="submit" class="primary">`)
	b.WriteString(dlBtnText)
	b.WriteString(`</button></form>`)
	b.WriteString(`<form method="post" action="/setup/sound/model/upload" enctype="multipart/form-data" style="display:flex;gap:8px;align-items:center;margin:0"><input type="hidden" name="token" value="`)
	b.WriteString(escTok)
	b.WriteString(`"><input type="file" name="model" accept=".tflite" style="font-size:12px;color:var(--muted)"><button type="submit">Upload .tflite</button></form>`)
	if st.ModelReady {
		b.WriteString(`<form method="post" action="/setup/sound/model/remove" style="margin:0"><input type="hidden" name="token" value="`)
		b.WriteString(escTok)
		b.WriteString(`"><button type="submit" class="danger">Remove</button></form>`)
	}
	b.WriteString(`</div></div>`)

	b.WriteString(`<form method="post" action="/setup/sound"><input type="hidden" name="token" value="`)
	b.WriteString(escTok)
	b.WriteString(`"><div class="card"><h2>Classifier &amp; Alert Categories</h2><label style="display:flex;align-items:center;gap:8px;margin-bottom:14px;cursor:pointer"><input type="checkbox" name="enabled" value="1"`)
	if cfg.Enabled {
		b.WriteString(` checked`)
	}
	b.WriteString(`><strong>Enable on-device room sound classification</strong></label><div class="grid2">`)
	b.WriteString(`<label class="row"><span class="lbl">Device name</span><input type="text" name="device_name" value="`)
	b.WriteString(escHTML(cfg.DeviceName))
	b.WriteString(`"></label>`)
	b.WriteString(fmt.Sprintf(`<label class="row"><span class="lbl">Minimum confidence (0.05 – 0.95)</span><input type="number" step="0.01" min="0.05" max="0.95" name="min_confidence" value="%.2f"></label>`, cfg.MinConfidence))
	b.WriteString(fmt.Sprintf(`<label class="row"><span class="lbl">Alert cooldown (seconds)</span><input type="number" min="1" max="3600" name="cooldown_sec" value="%d"></label>`, cfg.CooldownSec))
	b.WriteString(`</div><div style="margin-top:8px;font-weight:500">Alert on these sounds:</div><div class="checks">`)

	for _, ch := range sound.AlertChoices {
		b.WriteString(`<label><input type="checkbox" name="alerts" value="`)
		b.WriteString(escHTML(ch.Category))
		b.WriteString(`"`)
		if sound.AlertEnabled(cfg, ch.Category) {
			b.WriteString(` checked`)
		}
		b.WriteString(`><span>`)
		b.WriteString(escHTML(ch.Label))
		b.WriteString(`</span></label>`)
	}
	b.WriteString(`</div><div class="note"><strong>Safety disclaimer:</strong> Sound classification (including smoke/fire alarm and glass-break recognition) is an experimental, informational convenience feature and is <strong>not</strong> a certified life-safety or UL/EN smoke detection system. Always rely on dedicated, certified smoke and fire alarms.</div></div>`)

	b.WriteString(`<div class="card"><h2>Home Assistant Notifications</h2><label class="row"><span class="lbl">Home Assistant Webhook URL (easiest — no token required)</span><input type="text" name="ha_webhook_url" value="`)
	b.WriteString(escHTML(cfg.HAWebhookURL))
	b.WriteString(`" placeholder="http://homeassistant.local:8123/api/webhook/techo5_sound"><span class="hint">POSTs JSON with title, message, category, sound, confidence, rms_dbfs, and device whenever an alert fires.</span></label><div class="grid2"><label class="row"><span class="lbl">Home Assistant URL (optional REST service call)</span><input type="text" name="ha_url" value="`)
	b.WriteString(escHTML(cfg.HAURL))
	b.WriteString(`" placeholder="http://homeassistant.local:8123"></label><label class="row"><span class="lbl">Notification service</span><input type="text" name="ha_service" value="`)
	b.WriteString(escHTML(cfg.HAService))
	b.WriteString(`" placeholder="persistent_notification.create or notify.mobile_app_phone"></label></div><label class="row"><span class="lbl">Long-Lived Access Token `)
	if hasHAToken {
		b.WriteString(`(saved — leave blank to keep)`)
	}
	b.WriteString(`</span><input type="password" name="ha_token" value="" placeholder="`)
	if hasHAToken {
		b.WriteString(`••••••••`)
	} else {
		b.WriteString(`eyJhbGciOi...`)
	}
	b.WriteString(`"></label>`)
	if hasHAToken {
		b.WriteString(`<label style="display:flex;align-items:center;gap:8px;font-size:12px;color:var(--muted)"><input type="checkbox" name="ha_clear_token" value="1"> Clear saved token</label>`)
	}
	b.WriteString(`</div>`)

	b.WriteString(`<div class="card"><h2>MQTT &amp; Home Assistant Auto-Discovery</h2><div class="grid2"><label class="row"><span class="lbl">MQTT broker (host or host:port)</span><input type="text" name="mqtt_broker" value="`)
	b.WriteString(escHTML(cfg.MQTTBroker))
	b.WriteString(`" placeholder="192.168.1.10:1883"></label><label class="row"><span class="lbl">Topic prefix</span><input type="text" name="mqtt_topic" value="`)
	b.WriteString(escHTML(cfg.MQTTTopic))
	b.WriteString(`" placeholder="`)
	b.WriteString(escHTML(defaultTopic))
	b.WriteString(`"></label><label class="row"><span class="lbl">MQTT username</span><input type="text" name="mqtt_user" value="`)
	b.WriteString(escHTML(cfg.MQTTUser))
	b.WriteString(`"></label><label class="row"><span class="lbl">MQTT password `)
	if hasMQTTPass {
		b.WriteString(`(saved — leave blank to keep)`)
	}
	b.WriteString(`</span><input type="password" name="mqtt_pass" value="" placeholder="`)
	if hasMQTTPass {
		b.WriteString(`••••••••`)
	}
	b.WriteString(`"></label></div>`)
	if hasMQTTPass {
		b.WriteString(`<label style="display:flex;align-items:center;gap:8px;font-size:12px;color:var(--muted);margin-bottom:8px"><input type="checkbox" name="mqtt_clear_pass" value="1"> Clear saved MQTT password</label>`)
	}
	b.WriteString(`<label style="display:flex;align-items:center;gap:8px;margin-top:6px;cursor:pointer"><input type="checkbox" name="mqtt_discovery" value="1"`)
	if cfg.MQTTDiscovery {
		b.WriteString(` checked`)
	}
	b.WriteString(`><span>Publish Home Assistant MQTT Auto-Discovery sensors (<code>Room sound</code> &amp; <code>Last sound alert</code>)</span></label>`)
	b.WriteString(`<label style="display:flex;align-items:center;gap:8px;margin-top:6px;cursor:pointer"><input type="checkbox" name="mqtt_all_sounds" value="1"`)
	if cfg.MQTTAllSounds {
		b.WriteString(` checked`)
	}
	b.WriteString(`><span>Publish ambient sounds (speech, music, etc.) as well as alerts</span></label>`)
	b.WriteString(`<div class="btns" style="margin-top:16px"><button type="submit" class="primary">Save settings</button><button type="submit" formaction="/setup/sound/test">Send test notification</button></div></div></form></div>`)
	b.WriteString(indexFooterScript)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

const indexHeadPrefix = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>`

const indexHeadStyle = ` — Room Sound AI Add-On</title>
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
      <h1>Room Sound AI Add-On <span style="font-size:12px;font-weight:500;color:var(--muted)">`

const indexFooterScript = `<script>
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
    } else if (wrap) {
      wrap.innerHTML = '';
    }
  } catch (e) {}
}, 2000);
</script>
</body>
</html>
`
