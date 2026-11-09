package sound

import (
	"errors"
	"math"
	"strings"
)

// TopClass is one scored AudioSet class from yamnet-tflite.
type TopClass struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// EngineReply is one scored window result from the in-process YAMNet engine.
type EngineReply struct {
	MS        float64    `json:"ms"`
	RMSDBFS   float64    `json:"rms_dbfs"`
	Top       []TopClass `json:"top"`
	Embedding []byte     `json:"embedding,omitempty"`
}

// CustomSound stores a user-taught 1024-D YAMNet acoustic embedding prototype.
type CustomSound struct {
	Name      string  `json:"name"`
	Category  string  `json:"category"`
	Threshold float64 `json:"threshold"`
	Samples   int     `json:"samples"`
	Embedding []byte  `json:"embedding"`
}

// Detection is the room sound chosen for Home Assistant from one YAMNet window.
type Detection struct {
	Category   string
	Label      string
	RawClass   string
	Confidence float64
	Actionable bool
}

type rule struct {
	category   string
	label      string
	minScore   float64
	actionable bool
	ids        map[int]bool
}

func idSet(ids ...int) map[int]bool {
	m := make(map[int]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// Ordered from highest-priority specific/alert room events to general ambient states.
var rules = []rule{
	{
		category: "smoke_alarm", label: "Smoke / fire alarm", minScore: 0.20, actionable: true,
		ids: idSet(393, 394),
	},
	{
		category: "siren", label: "Siren", minScore: 0.22, actionable: true,
		ids: idSet(317, 318, 319, 390, 391),
	},
	{
		category: "glass", label: "Glass breaking", minScore: 0.22, actionable: true,
		ids: idSet(435, 436, 437),
	},
	{
		category: "baby_cry", label: "Baby cry", minScore: 0.22, actionable: true,
		ids: idSet(19, 20),
	},
	{
		category: "doorbell", label: "Doorbell", minScore: 0.22, actionable: true,
		ids: idSet(349, 195, 197),
	},
	{
		category: "alarm", label: "Alarm / ring", minScore: 0.24, actionable: true,
		ids: idSet(304, 382, 383, 384, 385, 389, 392, 475),
	},
	{
		category: "dog", label: "Dog", minScore: 0.22, actionable: true,
		ids: idSet(69, 70, 71, 72, 75, 117),
	},
	{
		category: "cat", label: "Cat", minScore: 0.22, actionable: true,
		ids: idSet(76, 77, 78, 80),
	},
	{
		category: "finger_snap", label: "Finger snapping", minScore: 0.20, actionable: true,
		ids: idSet(57),
	},
	{
		category: "clapping", label: "Clapping", minScore: 0.22, actionable: true,
		ids: idSet(56, 58, 62),
	},
	{
		category: "knock", label: "Knock", minScore: 0.22, actionable: true,
		ids: idSet(353, 354),
	},
	{
		category: "whistle", label: "Whistling", minScore: 0.22, actionable: true,
		ids: idSet(35, 396, 497),
	},
	{
		category: "cough", label: "Cough", minScore: 0.22, actionable: true,
		ids: idSet(42, 43),
	},
	{
		category: "sneeze", label: "Sneeze", minScore: 0.22, actionable: true,
		ids: idSet(44),
	},
	{
		category: "laughter", label: "Laughter", minScore: 0.24, actionable: true,
		ids: idSet(13, 14, 15, 17, 18),
	},
	{
		category: "door", label: "Door", minScore: 0.25, actionable: true,
		ids: idSet(348, 351, 356, 357),
	},
	{
		category: "water", label: "Running water", minScore: 0.28, actionable: false,
		ids: idSet(282, 364, 365, 368),
	},
	{
		category: "vacuum", label: "Vacuum cleaner", minScore: 0.28, actionable: false,
		ids: idSet(371),
	},
	{
		category: "typing", label: "Typing", minScore: 0.26, actionable: false,
		ids: idSet(378, 380),
	},
	{
		category: "snoring", label: "Snoring", minScore: 0.26, actionable: false,
		ids: idSet(38),
	},
	{
		category: "bird", label: "Bird", minScore: 0.28, actionable: false,
		ids: idSet(106, 107, 108),
	},
	{
		category: "speech", label: "Speech", minScore: 0.28, actionable: false,
		ids: idSet(0, 1, 2, 3, 4, 5, 65),
	},
	{
		category: "music", label: "Music", minScore: 0.30, actionable: false,
		ids: idSet(24, 29, 132, 133),
	},
}

func isMusicID(id int) bool {
	return (id >= 132 && id <= 276) || id == 24 || id == 29 || id == 30
}

func isNoiseFloorID(id int) bool {
	// Silence, Inside small/large room, Pink/White noise, Static, Hum
	switch id {
	case 494, 500, 501, 502, 506, 507, 513, 514, 515:
		return true
	}
	return false
}

// Classify maps YAMNet's top-k AudioSet scores to a clean room category and label using default thresholds.
func Classify(top []TopClass) Detection {
	return ClassifyWithMin(top, 0.25)
}

// ClassifyWithConfig maps YAMNet's top-k AudioSet scores using sc.MinConfidence and optional
// per-category threshold overrides in sc.CategoryMin.
func ClassifyWithConfig(top []TopClass, sc Config) Detection {
	return classifyInternal(top, sc.MinConfidence, sc.CategoryMin)
}

// ClassifyWithMin maps YAMNet's top-k AudioSet scores to a room category and label,
// scaling thresholds relative to minConf (where 0.25 is the calibrated default).
func ClassifyWithMin(top []TopClass, minConf float64) Detection {
	return classifyInternal(top, minConf, nil)
}

func classifyInternal(top []TopClass, minConf float64, catMin map[string]float64) Detection {
	if len(top) == 0 {
		return Detection{Category: "quiet", Label: "Quiet"}
	}
	if math.IsNaN(minConf) || math.IsInf(minConf, 0) || minConf <= 0 {
		minConf = 0.25
	} else if minConf > 0.99 {
		minConf = 0.99
	}
	baseScale := minConf / 0.25

	// Evaluate actionable and ambient category groups, combining hierarchical siblings.
	var best Detection
	bestIdx := len(rules)
	for i, r := range rules {
		var sum, topScore float64
		var raw string
		for _, tc := range top {
			if math.IsNaN(tc.Score) || math.IsInf(tc.Score, 0) || tc.Score <= 0 {
				continue
			}
			sc := tc.Score
			if sc > 1.0 {
				sc = 1.0
			}
			match := r.ids[tc.ID] || (r.category == "music" && isMusicID(tc.ID))
			if !match {
				continue
			}
			sum += sc
			if sc > topScore {
				topScore = sc
				raw = tc.Name
			}
		}
		if sum > 1.0 {
			sum = 1.0
		}
		effMin := minConf
		scale := baseScale
		if custom, ok := catMin[r.category]; ok && !math.IsNaN(custom) && !math.IsInf(custom, 0) && custom >= 0.05 && custom <= 0.95 {
			effMin = custom
			scale = custom / 0.25
		}
		req := r.minScore * scale
		if effMin > 0.25 && req < effMin {
			req = effMin
		}
		rawFloor := 0.14 * scale
		if rawFloor > req*0.8 {
			rawFloor = req * 0.8
		}
		// Require at least one individual class to be meaningful while rewarding sibling agreement.
		eff := topScore + 0.35*(sum-topScore)
		if eff < req || topScore < rawFloor {
			continue
		}
		// Actionable specific events beat ambient speech/music unless ambient is vastly stronger;
		// within the same tier, higher effective confidence wins.
		if best.Category == "" ||
			(r.actionable && !best.Actionable && eff >= req) ||
			(r.actionable == best.Actionable && eff > best.Confidence+0.05) ||
			(i < bestIdx && eff >= best.Confidence-0.05 && r.actionable == best.Actionable) {
			best = Detection{
				Category:   r.category,
				Label:      r.label,
				RawClass:   raw,
				Confidence: eff,
				Actionable: r.actionable,
			}
			bestIdx = i
		}
	}
	if best.Category != "" {
		return best
	}

	// Fallback: surface any other distinct AudioSet class (e.g., Microwave oven, Thunder, Guitar).
	fallbackMin := 0.35 * baseScale
	if minConf > 0.35 && fallbackMin < minConf {
		fallbackMin = minConf
	}
	for _, t0 := range top {
		if math.IsNaN(t0.Score) || math.IsInf(t0.Score, 0) || t0.Score <= 0 {
			continue
		}
		if isNoiseFloorID(t0.ID) {
			break
		}
		sc := t0.Score
		if sc > 1.0 {
			sc = 1.0
		}
		if sc >= fallbackMin {
			name := strings.TrimSpace(t0.Name)
			if name == "" {
				name = "Unknown"
			}
			cat := strings.ToLower(name)
			cat = strings.Trim(strings.NewReplacer(", ", "_", " ", "_", "(", "", ")", "", "/", "_").Replace(cat), "_")
			if cat == "" {
				cat = "unknown"
				name = "Unknown"
			}
			return Detection{
				Category:   cat,
				Label:      name,
				RawClass:   name,
				Confidence: sc,
				Actionable: false,
			}
		}
		break
	}

	return Detection{Category: "quiet", Label: "Quiet"}
}

// EmbeddingSimilarity computes the cosine similarity in [0.0, 1.0] between two
// 1024-D signed int8 YAMNet embeddings.
func EmbeddingSimilarity(a, b []byte) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB int64
	for i := 0; i < len(a); i++ {
		va := int64(int8(a[i]))
		vb := int64(int8(b[i]))
		dot += va * vb
		normA += va * va
		normB += vb * vb
	}
	if dot <= 0 || normA == 0 || normB == 0 {
		return 0
	}
	sim := float64(dot) / math.Sqrt(float64(normA)*float64(normB))
	if sim > 1.0 {
		sim = 1.0
	}
	return sim
}

// MatchCustomSound compares embed against user-taught custom sound prototypes and returns
// the strongest match exceeding its threshold.
func MatchCustomSound(embed []byte, custom []CustomSound) (Detection, bool) {
	if len(embed) == 0 || len(custom) == 0 {
		return Detection{}, false
	}
	var best Detection
	for _, cs := range custom {
		if len(cs.Embedding) != len(embed) || cs.Name == "" {
			continue
		}
		th := cs.Threshold
		if th < 0.50 || th > 0.98 || math.IsNaN(th) {
			th = 0.80
		}
		sim := EmbeddingSimilarity(embed, cs.Embedding)
		if sim >= th && sim > best.Confidence {
			cat := cs.Category
			if cat == "" {
				cat = "custom_" + SanitizeNode(cs.Name)
			}
			best = Detection{
				Category:   cat,
				Label:      cs.Name,
				RawClass:   "Custom (" + cs.Name + ")",
				Confidence: sim,
				Actionable: true,
			}
		}
	}
	return best, best.Category != ""
}

// LearnCustomSound adds a new custom sound prototype or refines an existing one with the
// same name via running prototype averaging.
func LearnCustomSound(existing []CustomSound, name string, threshold float64, embed []byte) ([]CustomSound, CustomSound, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return existing, CustomSound{}, errors.New("custom sound name is required")
	}
	if len(name) > 48 {
		name = name[:48]
	}
	if len(embed) == 0 {
		return existing, CustomSound{}, errors.New("no recent active sound embedding captured yet — make the sound near the microphone first")
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold <= 0 {
		threshold = 0.80
	} else if threshold < 0.50 {
		threshold = 0.50
	} else if threshold > 0.98 {
		threshold = 0.98
	}

	cat := "custom_" + SanitizeNode(name)
	out := make([]CustomSound, len(existing))
	copy(out, existing)

	for i := range out {
		if strings.EqualFold(out[i].Name, name) || out[i].Category == cat {
			prev := out[i]
			samples := prev.Samples
			if samples < 1 {
				samples = 1
			}
			merged := make([]byte, len(embed))
			if len(prev.Embedding) == len(embed) {
				var maxAbs float64
				tmp := make([]float64, len(embed))
				wPrev := float64(samples)
				if wPrev > 5 {
					wPrev = 5
				}
				for j := 0; j < len(embed); j++ {
					v := (wPrev*float64(int8(prev.Embedding[j])) + float64(int8(embed[j]))) / (wPrev + 1.0)
					tmp[j] = v
					if av := math.Abs(v); av > maxAbs {
						maxAbs = av
					}
				}
				if maxAbs > 0 {
					for j := 0; j < len(embed); j++ {
						q := int32(math.Round(tmp[j] * 127.0 / maxAbs))
						merged[j] = byte(int8(clampI32(q, -127, 127)))
					}
				}
			} else {
				copy(merged, embed)
			}
			out[i] = CustomSound{
				Name:      name,
				Category:  cat,
				Threshold: threshold,
				Samples:   samples + 1,
				Embedding: merged,
			}
			return out, out[i], nil
		}
	}

	if len(out) >= 32 {
		return existing, CustomSound{}, errors.New("maximum of 32 custom sounds reached")
	}
	embCopy := make([]byte, len(embed))
	copy(embCopy, embed)
	cs := CustomSound{
		Name:      name,
		Category:  cat,
		Threshold: threshold,
		Samples:   1,
		Embedding: embCopy,
	}
	out = append(out, cs)
	return out, cs, nil
}
