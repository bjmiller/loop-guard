// Package cli implements the loop-guard command-line contract:
//
//	exit 0 — no loop (allow)
//	exit 2 — loop detected (harness should block and feed stderr to the model)
//	exit 3 — circuit breaker tripped (stop the session; see handoff artifact)
//	exit 1 — usage or internal error
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/brian/loop-guard/internal/detector"
	"github.com/brian/loop-guard/internal/guard"
	"github.com/brian/loop-guard/internal/state"
)

// Version is stamped at build time via -ldflags.
var Version = "0.1.0"

// Event is the stdin payload accepted by `record`.
type Event struct {
	Type string          `json:"type"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	Text string          `json:"text,omitempty"`
}

// claudeHookInput mirrors Claude Code's PreToolUse hook payload.
type claudeHookInput struct {
	SessionID string          `json:"session_id"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// Run executes one command and returns the process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 1
	}
	switch args[0] {
	case "record":
		return cmdRecord(args[1:], stdin, stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "claude-hook":
		return cmdClaudeHook(stdin, stdout, stderr)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "serve":
		return cmdServe(args[1:], stdin, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "loop-guard %s\n", Version)
		return 0
	default:
		fmt.Fprintf(stderr, "loop-guard: unknown command %q\n\n", args[0])
		usage(stderr)
		return 1
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `loop-guard — infinite-loop prevention for coding agents

Usage: loop-guard <command> [flags]

Commands:
  record       Record a tool call or response from JSON on stdin, then evaluate.
               Exit codes: 0 allow, 2 loop (block+instruct), 3 breaker tripped.
  check        Read-only evaluation of a session's current state.
  claude-hook  Adapter for Claude Code PreToolUse hooks (reads hook JSON).
  doctor       Detect installed harnesses, validate setup. --fix wires config.
  init         Print integration instructions for a harness (--harness custom|claude|opencode).
  serve        Run as an MCP server over stdio.
  version      Print version.

Common flags:
  --session ID           Session identifier (required for record/check)
  --cache-dir DIR        Override state directory (or LOOPGUARD_CACHE_DIR env)
  --tool-threshold N     Identical tool calls before flagging (default 3)
  --tool-window N        Recent calls considered (default 6)
  --response-threshold N Near-duplicate responses before flagging (default 3)
  --similarity F         Jaccard threshold for near-duplicates (default 0.8)
  --max-injections N     Recovery prompts before breaker trips (default 2)
`)
}

func fsFor(name string) (*flag.FlagSet, *string, *string, *detector.Config, *int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	session := fs.String("session", "", "session identifier")
	cacheDir := fs.String("cache-dir", "", "state directory override")
	cfg := &detector.Config{}
	maxInj := fs.Int("max-injections", guard.DefaultMaxInterventions, "recovery prompts before breaker")
	fs.IntVar(&cfg.ToolThreshold, "tool-threshold", detector.DefaultToolThreshold, "")
	fs.IntVar(&cfg.ToolWindow, "tool-window", detector.DefaultToolWindow, "")
	fs.IntVar(&cfg.ResponseThreshold, "response-threshold", detector.DefaultResponseThreshold, "")
	fs.Float64Var(&cfg.Similarity, "similarity", detector.DefaultSimilarity, "")
	return fs, session, cacheDir, cfg, maxInj
}

func newGuard(cacheDir string, cfg detector.Config, maxInj int) (*guard.Guard, error) {
	dir := cacheDir
	if dir == "" {
		var err error
		dir, err = state.DefaultDir()
		if err != nil {
			return nil, err
		}
	}
	return &guard.Guard{
		Store:            &state.Store{Dir: dir},
		Config:           cfg,
		MaxInterventions: maxInj,
	}, nil
}

func emitVerdict(v guard.Verdict, stdout, stderr io.Writer) int {
	data, _ := json.Marshal(v)
	fmt.Fprintln(stdout, string(data))
	switch v.Action {
	case guard.ActionInject, guard.ActionInjectFinal:
		fmt.Fprintln(stderr, v.Message)
		return exitLoop
	case guard.ActionBreaker:
		fmt.Fprintln(stderr, v.Message)
		return exitBreaker
	default:
		return exitOK
	}
}

const (
	exitOK      = 0
	exitErr     = 1
	exitLoop    = 2
	exitBreaker = 3
)

func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

func cmdRecord(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, session, cacheDir, cfg, maxInj := fsFor("record")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "loop-guard record: %v\n", err)
		return exitErr
	}
	if *session == "" {
		fmt.Fprintln(stderr, "loop-guard record: --session is required")
		return exitErr
	}
	raw, err := readAll(stdin)
	if err != nil || len(raw) == 0 {
		fmt.Fprintln(stderr, "loop-guard record: expected an event JSON object on stdin")
		return exitErr
	}
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		fmt.Fprintf(stderr, "loop-guard record: parse event: %v\n", err)
		return exitErr
	}

	g, err := newGuard(*cacheDir, *cfg, *maxInj)
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard record: %v\n", err)
		return exitErr
	}

	sev, err := toStateEvent(ev)
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard record: %v\n", err)
		return exitErr
	}
	v, err := g.Record(*session, sev)
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard record: %v\n", err)
		return exitErr
	}
	return emitVerdict(v, stdout, stderr)
}

func toStateEvent(ev Event) (state.Event, error) {
	switch ev.Type {
	case "tool":
		var args any
		if len(ev.Args) > 0 {
			if err := json.Unmarshal(ev.Args, &args); err != nil {
				return state.Event{}, fmt.Errorf("parse args: %w", err)
			}
		}
		return state.Event{Type: "tool", Name: ev.Name, Args: args}, nil
	case "response":
		return state.Event{Type: "response", Text: ev.Text}, nil
	default:
		return state.Event{}, fmt.Errorf("event type must be %q or %q, got %q", "tool", "response", ev.Type)
	}
}

func cmdCheck(args []string, stdout, stderr io.Writer) int {
	fs, session, cacheDir, cfg, maxInj := fsFor("check")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "loop-guard check: %v\n", err)
		return exitErr
	}
	if *session == "" {
		fmt.Fprintln(stderr, "loop-guard check: --session is required")
		return exitErr
	}
	g, err := newGuard(*cacheDir, *cfg, *maxInj)
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard check: %v\n", err)
		return exitErr
	}
	v := g.Check(*session)
	if err != nil {
		return exitErr
	}
	return emitVerdict(v, stdout, stderr)
}

func cmdClaudeHook(stdin io.Reader, stdout, stderr io.Writer) int {
	raw, err := readAll(stdin)
	if err != nil || len(raw) == 0 {
		fmt.Fprintln(stderr, "loop-guard claude-hook: expected hook JSON on stdin")
		return exitErr
	}
	var h claudeHookInput
	if err := json.Unmarshal(raw, &h); err != nil {
		fmt.Fprintf(stderr, "loop-guard claude-hook: parse hook input: %v\n", err)
		return exitErr
	}
	if h.SessionID == "" || h.ToolName == "" {
		fmt.Fprintln(stderr, "loop-guard claude-hook: hook input needs session_id and tool_name")
		return exitErr
	}

	g, err := newGuard("", detector.Config{}, guard.DefaultMaxInterventions)
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard claude-hook: %v\n", err)
		return exitErr
	}
	v, err := g.Record(h.SessionID, state.Event{
		Type: "tool",
		Name: h.ToolName,
		Args: rawArgs(h.ToolInput),
	})
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard claude-hook: %v\n", err)
		return exitErr
	}
	return emitVerdict(v, stdout, stderr)
}

func rawArgs(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		return v
	}
	return string(raw)
}
