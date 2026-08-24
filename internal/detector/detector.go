// Package detector implements loop detection for agent tool calls and
// responses: fingerprint hashing with windowed counting, and fuzzy response
// similarity via token Jaccard.
package detector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Kind identifies which detector fired.
const (
	KindTool     = "tool"
	KindResponse = "response"
)

// Defaults applied when Config fields are zero.
const (
	DefaultToolThreshold     = 3
	DefaultToolWindow        = 6
	DefaultResponseThreshold = 3
	DefaultResponseWindow    = 6
	DefaultSimilarity        = 0.8
	// MinResponseLength ignores short acknowledgements ("Done.", "OK.")
	// where repetition is normal conversation, not a loop.
	MinResponseLength = 40
)

// Verdict reports the outcome of detection.
type Verdict struct {
	Loop  bool   `json:"loop"`
	Kind  string `json:"kind,omitempty"`
	Count int    `json:"count,omitempty"`
}

// Config holds tunable thresholds. Zero fields fall back to defaults.
type Config struct {
	ToolThreshold     int
	ToolWindow        int
	ResponseThreshold int
	ResponseWindow    int
	Similarity        float64
}

// WithDefaults returns cfg with zero fields replaced by package defaults.
func WithDefaults(c Config) Config { return c.withDefaults() }

func (c Config) withDefaults() Config {
	if c.ToolThreshold <= 0 {
		c.ToolThreshold = DefaultToolThreshold
	}
	if c.ToolWindow <= 0 {
		c.ToolWindow = DefaultToolWindow
	}
	if c.ResponseThreshold <= 0 {
		c.ResponseThreshold = DefaultResponseThreshold
	}
	if c.ResponseWindow <= 0 {
		c.ResponseWindow = DefaultResponseWindow
	}
	if c.Similarity <= 0 || c.Similarity > 1 {
		c.Similarity = DefaultSimilarity
	}
	return c
}

// ToolFingerprint returns a stable hash of a tool name and its arguments.
// Map arguments are canonicalized (sorted keys, deterministic formatting) so
// semantically identical calls hash identically regardless of key order.
func ToolFingerprint(name string, args any) string {
	canonical, err := json.Marshal(canonicalize(args))
	if err != nil {
		canonical = []byte(fmtSprint(args))
	}
	sum := sha256.Sum256([]byte(name + "\x00" + string(canonical)))
	return hex.EncodeToString(sum[:])
}

func fmtSprint(v any) string {
	type stringer interface{ String() string }
	if s, ok := v.(stringer); ok {
		return s.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func canonicalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = canonicalize(t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = canonicalize(t[i])
		}
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return int64(t)
		}
		return t
	case json.Number:
		return t.String()
	default:
		return v
	}
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Similarity returns token-set Jaccard similarity between two texts in [0,1],
// after lowercasing and stripping punctuation.
func Similarity(a, b string) float64 {
	at, bt := tokens(a), tokens(b)
	if len(at) == 0 && len(bt) == 0 {
		return 1.0
	}
	inter := 0
	for t := range at {
		if bt[t] {
			inter++
		}
	}
	union := len(at) + len(bt) - inter
	if union == 0 {
		return 1.0
	}
	return float64(inter) / float64(union)
}

func tokens(s string) map[string]bool {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, " ")
	fields := strings.Fields(s)
	set := make(map[string]bool, len(fields))
	for _, f := range fields {
		set[f] = true
	}
	return set
}

// DetectToolLoop checks whether seq's last entry repeats ToolThreshold times
// within the trailing ToolWindow entries. seq is chronological; the last
// element is the event under evaluation.
func DetectToolLoop(seq []string, cfg Config) Verdict {
	cfg = cfg.withDefaults()
	n := len(seq)
	if n == 0 {
		return Verdict{}
	}
	current := seq[n-1]
	windowStart := n - cfg.ToolWindow
	if windowStart < 0 {
		windowStart = 0
	}
	count := 0
	for _, fp := range seq[windowStart:] {
		if fp == current {
			count++
		}
	}
	if count >= cfg.ToolThreshold {
		return Verdict{Loop: true, Kind: KindTool, Count: count}
	}
	return Verdict{}
}

// DetectResponseLoop checks whether seq's last entry is similar to
// ResponseThreshold-1 earlier entries within the trailing ResponseWindow.
// Short responses are exempt from similarity matching.
func DetectResponseLoop(seq []string, cfg Config) Verdict {
	cfg = cfg.withDefaults()
	n := len(seq)
	if n == 0 || len(strings.TrimSpace(seq[n-1])) < MinResponseLength {
		return Verdict{}
	}
	current := seq[n-1]
	windowStart := n - cfg.ResponseWindow
	if windowStart < 0 {
		windowStart = 0
	}
	similar := 0
	for _, prev := range seq[windowStart : n-1] {
		if len(strings.TrimSpace(prev)) >= MinResponseLength &&
			Similarity(prev, current) >= cfg.Similarity {
			similar++
		}
	}
	total := similar + 1
	if total >= cfg.ResponseThreshold {
		return Verdict{Loop: true, Kind: KindResponse, Count: total}
	}
	return Verdict{}
}
