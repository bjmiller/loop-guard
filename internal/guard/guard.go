// Package guard orchestrates loop detection with an escalation ladder:
// inject a recovery prompt, then a final warning, then trip the circuit
// breaker and write a handoff artifact for a clean restart.
package guard

import (
	"fmt"
	"path/filepath"

	"github.com/bjmiller/loop-guard/internal/detector"
	"github.com/bjmiller/loop-guard/internal/state"
)

// Action tells the harness what to do with the verdict.
const (
	ActionAllow       = "allow"
	ActionInject      = "inject"       // block + recovery prompt
	ActionInjectFinal = "inject_final" // block + final warning
	ActionBreaker     = "breaker"      // stop talking to this thread
)

// DefaultMaxInterventions is the intervention budget before the breaker trips.
// Two failed injections are enough evidence the thread cannot recover.
const DefaultMaxInterventions = 2

// Verdict is the JSON contract consumed by every harness adapter.
type Verdict struct {
	OK               bool   `json:"ok"`
	Loop             bool   `json:"loop"`
	Kind             string `json:"kind,omitempty"`
	Count            int    `json:"count,omitempty"`
	Interventions    int    `json:"interventions"`
	MaxInterventions int    `json:"max_interventions"`
	Action           string `json:"action"`
	Message          string `json:"message,omitempty"`
	HandoffPath      string `json:"handoff_path,omitempty"`
}

// Guard evaluates recorded traffic for one or more sessions.
type Guard struct {
	Store            *state.Store
	Config           detector.Config
	MaxInterventions int
}

func (g *Guard) maxInterventions() int {
	if g.MaxInterventions > 0 {
		return g.MaxInterventions
	}
	return DefaultMaxInterventions
}

// HandoffPathFor returns where the handoff artifact for id lives.
func HandoffPathFor(dir, id string) string {
	return filepath.Join(dir, id+"-handoff.md")
}

// Record appends ev to the session history and returns the verdict.
func (g *Guard) Record(sessionID string, ev state.Event) (Verdict, error) {
	cfg := detector.WithDefaults(g.Config) // local copy: Guard may be shared
	if ev.Fingerprint == "" && ev.Type == "tool" {
		ev.Fingerprint = detector.ToolFingerprint(ev.Name, ev.Args)
	}

	var verdict Verdict
	err := g.Store.Update(sessionID, func(sess *state.Session) {
		if sess.Breaker {
			verdict = g.breakerVerdict(sess)
			return
		}
		sess.Events = append(sess.Events, ev)
		if d := g.detect(sess, cfg); d.Loop {
			sess.Interventions++
			verdict = g.escalate(sess, d)
			return
		}
		verdict = g.allowVerdict(sess)
	})
	return verdict, err
}

// Check evaluates current state without recording anything or mutating the
// session. When a loop is live it reports an inject-class action so harnesses
// block and instruct; only an actually-tripped breaker yields exit-code-3
// severity, and projections can never trip it.
func (g *Guard) Check(sessionID string) (Verdict, error) {
	cfg := detector.WithDefaults(g.Config) // local copy: Guard may be shared
	sess, err := g.Store.Load(sessionID)
	if err != nil {
		return Verdict{}, err
	}
	if sess.Breaker {
		return g.breakerVerdict(sess), nil
	}
	d := g.detect(sess, cfg)
	if !d.Loop {
		return g.allowVerdict(sess), nil
	}
	action := ActionInjectFinal
	if sess.Interventions+1 < g.maxInterventions() {
		action = ActionInject
	}
	return Verdict{
		OK: false, Loop: true, Kind: d.Kind, Count: d.Count,
		Interventions:    sess.Interventions,
		MaxInterventions: g.maxInterventions(),
		Action:           action,
		Message:          RecoveryMessage(d.Kind, d.Count, sess.Interventions+1 >= g.maxInterventions()),
	}, nil
}

func (g *Guard) allowVerdict(sess *state.Session) Verdict {
	return Verdict{
		OK:               true,
		Action:           ActionAllow,
		Interventions:    sess.Interventions,
		MaxInterventions: g.maxInterventions(),
	}
}

func projectAction(next, max int) string {
	switch {
	case next > max:
		return ActionBreaker
	case next >= max:
		return ActionInjectFinal
	default:
		return ActionInject
	}
}

// detect runs the detector matching the most recently recorded event. Only the
// event under evaluation is examined, so a stale loop from the other history
// (e.g. an old tool fingerprint when a response arrives) can never re-fire and
// escalate a session that already changed behavior.
func (g *Guard) detect(sess *state.Session, cfg detector.Config) detector.Verdict {
	if len(sess.Events) == 0 {
		return detector.Verdict{}
	}
	if sess.Events[len(sess.Events)-1].Type == "tool" {
		fps := make([]string, 0, len(sess.Events))
		for _, e := range sess.Events {
			if e.Type == "tool" {
				fps = append(fps, e.Fingerprint)
			}
		}
		return detector.DetectToolLoop(fps, cfg)
	}
	texts := make([]string, 0, len(sess.Events))
	for _, e := range sess.Events {
		if e.Type != "tool" {
			texts = append(texts, e.Text)
		}
	}
	return detector.DetectResponseLoop(texts, cfg)
}

// escalate applies the ladder step implied by the just-incremented
// intervention count. Callers must have incremented sess.Interventions first.
func (g *Guard) escalate(sess *state.Session, d detector.Verdict) Verdict {
	max := g.maxInterventions()
	switch action := projectAction(sess.Interventions, max); action {
	case ActionBreaker:
		sess.Breaker = true
		path, err := WriteHandoff(g.Store.Dir, sess, d.Kind, d.Count, g.Store.Now())
		out := g.breakerVerdict(sess)
		out.Loop, out.Kind, out.Count = true, d.Kind, d.Count
		if err != nil {
			out.HandoffPath = ""
		} else {
			out.HandoffPath = path
		}
		return out
	case ActionInjectFinal:
		return Verdict{
			OK: false, Loop: true, Kind: d.Kind, Count: d.Count,
			Interventions: sess.Interventions, MaxInterventions: max,
			Action:  ActionInjectFinal,
			Message: RecoveryMessage(d.Kind, d.Count, true),
		}
	default:
		return Verdict{
			OK: false, Loop: true, Kind: d.Kind, Count: d.Count,
			Interventions: sess.Interventions, MaxInterventions: max,
			Action:  ActionInject,
			Message: RecoveryMessage(d.Kind, d.Count, false),
		}
	}
}

func (g *Guard) breakerVerdict(sess *state.Session) Verdict {
	path := HandoffPathFor(g.Store.Dir, sess.ID)
	return Verdict{
		OK:               false,
		Loop:             true,
		Kind:             "breaker",
		Interventions:    sess.Interventions,
		MaxInterventions: g.maxInterventions(),
		Action:           ActionBreaker,
		Message: fmt.Sprintf(
			"Loop-Guard circuit breaker tripped after %d ignored interventions. "+
				"Do not continue in this session. A handoff artifact for restarting "+
				"in a fresh session is at: %s", sess.Interventions, path),
		HandoffPath: path,
	}
}
