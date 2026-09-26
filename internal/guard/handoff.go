package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bjmiller/loop-guard/internal/state"
)

// RecoveryMessage is the prompt injected into the conversation when a loop is
// detected. It is addressed to the model and must be actionable.
func RecoveryMessage(kind string, count int, final bool) string {
	var b strings.Builder
	if final {
		fmt.Fprintf(&b, "FINAL WARNING — Loop-Guard: this session has repeated the same %s %d times. ", kind, count)
		b.WriteString("Your recovery attempts were ignored or repeated the pattern.\n\n")
	} else {
		fmt.Fprintf(&b, "Loop-Guard: you are stuck in a loop — the same %s has now occurred %d times. ", kind, count)
		b.WriteString("Stop repeating it.\n\n")
	}
	b.WriteString(`Required actions, in order:
1. STOP. Do not re-run the same call. Do not rephrase the same response.
2. State in one sentence what you expected versus what happened.
3. Change approach: try a materially different method, tool, inputs, or plan.
4. If two different approaches have already failed, stop working and ask the human how to proceed.

Repeating the pattern again will trip the circuit breaker and end this session.`)
	return b.String()
}

// WriteHandoff emits the structured restart artifact when the breaker trips.
// It is written for a *fresh* session to consume; the corrupted transcript is
// never replayed. now stamps the "tripped at" line; callers pass their
// store's clock time so tests stay deterministic.
func WriteHandoff(dir string, sess *state.Session, kind string, count int, now time.Time) (string, error) {
	path := HandoffPathFor(dir, sess.ID)

	var b strings.Builder
	b.WriteString("# Loop-Guard Handoff\n\n")
	fmt.Fprintf(&b,
		"This session was stopped by the Loop-Guard circuit breaker after repeated "+
			"identical behavior that ignored recovery prompts. Restart work in a FRESH "+
			"session seeded with this file only — do not replay the old transcript.\n\n")

	fmt.Fprintf(&b, "- Session id: `%s`\n", sess.ID)
	fmt.Fprintf(&b, "- Tripped at: %s\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Loop kind: **%s** (%d consecutive occurrences)\n", kind, count)
	fmt.Fprintf(&b, "- Interventions attempted before breaker: %d\n\n", sess.Interventions)

	b.WriteString("## Observed loop\n\n")
	b.WriteString("The agent repeated the following pattern instead of changing strategy:\n\n")
	for _, e := range loopPattern(sess, kind) {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}

	b.WriteString("\n## Recent activity (oldest first)\n\n")
	b.WriteString("| # | Type | Detail |\n|---|------|--------|\n")
	start := len(sess.Events) - 10
	if start < 0 {
		start = 0
	}
	for i, e := range sess.Events[start:] {
		b.WriteString(fmt.Sprintf("| %d | %s | %s |\n", i+1, e.Type, describe(e)))
	}

	fmt.Fprintf(&b, "\n## Interventions attempted\n\n"+
		"%d recovery prompt(s) were injected into the conversation and had no effect. "+
		"The thread's context should be considered corrupted; further prompting is futile.\n\n",
		sess.Interventions)

	b.WriteString("## Suggested restart approach\n\n" +
		"1. Start a new session with clean context.\n" +
		"2. Paste or reference this handoff file as the task briefing.\n" +
		"3. Re-state the original goal explicitly.\n" +
		"4. Instruct the new session to avoid the loop pattern listed above and to " +
		"change strategy after two failed attempts.\n")

	data := b.String()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// loopPattern summarizes the most recent matching entries at the tail of
// history, newest first.
func loopPattern(sess *state.Session, kind string) []string {
	var out []string
	for i := len(sess.Events) - 1; i >= 0 && len(out) < 3; i-- {
		e := sess.Events[i]
		if kindFor(e) != kind {
			continue
		}
		out = append(out, describe(e))
	}
	return out
}

func kindFor(e state.Event) string {
	if e.Type == "tool" {
		return "tool"
	}
	return "response"
}

// describe renders one event on a single line, truncated for readability.
func describe(e state.Event) string {
	const maxRunes = 120
	trunc := func(s string) string {
		s = strings.ReplaceAll(s, "|", "\\|")
		if utf8.RuneCountInString(s) > maxRunes {
			s = string([]rune(s)[:maxRunes]) + "…"
		}
		return s
	}
	switch e.Type {
	case "tool":
		args := formatArgs(e.Args)
		return trunc(fmt.Sprintf("`%s` args: %s", e.Name, args))
	default:
		return trunc(fmt.Sprintf("%q", e.Text))
	}
}

func formatArgs(args any) string {
	if args == nil {
		return "{}"
	}
	if b, err := json.Marshal(args); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", args)
}
