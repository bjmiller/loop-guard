// Package cli implements the loop-guard command-line contract:
//
//	exit 0 — no loop (allow)
//	exit 2 — loop detected (harness should block and feed stderr to the model)
//	exit 3 — circuit breaker tripped (stop the session; see handoff artifact)
//	exit 1 — usage or internal error
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/bjmiller/loop-guard/internal/detector"
	"github.com/bjmiller/loop-guard/internal/guard"
	"github.com/bjmiller/loop-guard/internal/state"
	"github.com/bjmiller/loop-guard/internal/version"
)

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

// copilotHookInput mirrors GitHub Copilot CLI's preToolUse hook payload.
// Field names have varied across Copilot versions, so both camelCase and
// snake_case spellings are accepted for every field.
type copilotHookInput struct {
	Session      string
	Conversation string
	ToolName     string
	ToolArgs     json.RawMessage
}

func (h *copilotHookInput) UnmarshalJSON(data []byte) error {
	var camel struct {
		SessionID      string          `json:"sessionId"`
		ConversationID string          `json:"conversationId"`
		ToolName       string          `json:"toolName"`
		ToolInput      json.RawMessage `json:"toolInput"`
	}
	var snake struct {
		SessionID string          `json:"session_id"`
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
		Input     json.RawMessage `json:"input"`
		Args      json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(data, &camel); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &snake); err != nil {
		return err
	}
	h.Conversation = camel.ConversationID
	h.Session = firstNonEmpty(camel.SessionID, snake.SessionID)
	h.ToolName = firstNonEmpty(camel.ToolName, snake.ToolName)
	for _, raw := range []json.RawMessage{camel.ToolInput, snake.ToolInput, snake.Input, snake.Args} {
		if len(raw) > 0 {
			h.ToolArgs = raw
			break
		}
	}
	return nil
}

// ID returns the best available session identifier. conversationId is the
// most stable per-conversation identifier Copilot has exposed; it wins over
// session ids when present.
func (h copilotHookInput) ID() string {
	if h.Conversation != "" {
		return h.Conversation
	}
	return h.Session
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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
		return cmdClaudeHook(args[1:], stdin, stdout, stderr)
	case "copilot-hook":
		return cmdCopilotHook(args[1:], stdin, stdout, stderr)
	case "vscode-hook":
		return cmdVSCodeHook(args[1:], stdin, stdout, stderr)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "serve":
		return cmdServe(args[1:], stdin, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "loop-guard %s\n", version.Version)
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
  claude-hook  Adapter for Claude Code / Codex PreToolUse hooks (reads hook JSON).
  copilot-hook Adapter for GitHub Copilot CLI preToolUse hooks (reads hook JSON).
  vscode-hook  Adapter for VS Code chat agent hooks (reads hook JSON).
  doctor       Detect installed harnesses, validate setup. --fix wires config.
	init         Print integration instructions for a harness
               (--harness custom|claude|opencode|codex|copilot|pi|vscode).
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

// parseFlags parses fs with errors and help text routed to stderr. It returns
// (code, false) when the caller must return code immediately; -h is a clean
// exit 0 rather than an error.
func parseFlags(fs *flag.FlagSet, args []string, cmd string, stderr io.Writer) (int, bool) {
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		fmt.Fprintf(stderr, "loop-guard %s: %v\n", cmd, err)
		return exitErr, false
	}
	return exitOK, true
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

func cmdRecord(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, session, cacheDir, cfg, maxInj := fsFor("record")
	if code, ok := parseFlags(fs, args, "record", stderr); !ok {
		return code
	}
	if *session == "" {
		fmt.Fprintln(stderr, "loop-guard record: --session is required")
		return exitErr
	}
	raw, err := io.ReadAll(stdin)
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
		args, err := state.DecodeArgs(ev.Args)
		if err != nil {
			return state.Event{}, fmt.Errorf("parse args: %w", err)
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
	if code, ok := parseFlags(fs, args, "check", stderr); !ok {
		return code
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
	v, err := g.Check(*session)
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard check: %v\n", err)
		return exitErr
	}
	return emitVerdict(v, stdout, stderr)
}

// cmdClaudeHook adapts Claude Code's and Codex's PreToolUse hooks. Both block
// the tool call only on exit 2 and feed stderr to the model, so the breaker
// takes the same path as a loop intervention: blocking the call is the only
// way to make it stop. stdout is left empty because these harnesses parse it
// as hook-decision JSON.
func cmdClaudeHook(args []string, stdin io.Reader, _ io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("claude-hook", flag.ContinueOnError)
	cacheDir := fs.String("cache-dir", "", "state directory override")
	if code, ok := parseFlags(fs, args, "claude-hook", stderr); !ok {
		return code
	}
	raw, err := io.ReadAll(stdin)
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

	g, err := newGuard(*cacheDir, detector.Config{}, guard.DefaultMaxInterventions)
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

	switch v.Action {
	case guard.ActionInject, guard.ActionInjectFinal, guard.ActionBreaker:
		fmt.Fprintln(stderr, v.Message)
		return exitLoop
	default:
		return exitOK
	}
}

func rawArgs(raw json.RawMessage) any {
	v, err := state.DecodeArgs(raw)
	if err != nil {
		return string(raw)
	}
	return v
}

// cmdCopilotHook adapts GitHub Copilot CLI's preToolUse hook onto record
// semantics. preToolUse is fail-closed there (any non-zero exit denies), so it
// shares the claude-hook adapter's contract: 0 allow, 2 block with the
// recovery prompt on stderr, 1 error.
func cmdCopilotHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("copilot-hook", flag.ContinueOnError)
	cacheDir := fs.String("cache-dir", "", "state directory override")
	if code, ok := parseFlags(fs, args, "copilot-hook", stderr); !ok {
		return code
	}
	raw, err := io.ReadAll(stdin)
	if err != nil || len(raw) == 0 {
		fmt.Fprintf(stderr, "loop-guard copilot-hook: expected hook JSON on stdin")
		return exitErr
	}
	var h copilotHookInput
	if err := json.Unmarshal(raw, &h); err != nil {
		fmt.Fprintf(stderr, "loop-guard copilot-hook: parse hook input: %v\n", err)
		return exitErr
	}
	session := h.ID()
	if session == "" || h.ToolName == "" {
		fmt.Fprintf(stderr, "loop-guard copilot-hook: hook input needs a session/conversation id and a tool name")
		return exitErr
	}

	g, err := newGuard(*cacheDir, detector.Config{}, guard.DefaultMaxInterventions)
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard copilot-hook: %v\n", err)
		return exitErr
	}
	v, err := g.Record(session, state.Event{
		Type: "tool",
		Name: h.ToolName,
		Args: rawArgs(h.ToolArgs),
	})
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard copilot-hook: %v\n", err)
		return exitErr
	}
	return emitVerdict(v, stdout, stderr)
}

// vscodeSessionFallback is the session id used when VS Code omits session_id
// (an optional field in its hook payload). Sharing one state file across such
// sessions only makes detection marginally more eager, never less.
const vscodeSessionFallback = "vscode"

// vscodeOutput is the stdout contract VS Code hooks parse on exit 0.
type vscodeOutput struct {
	Continue   bool   `json:"continue"`
	StopReason string `json:"stopReason,omitempty"`
}

// cmdVSCodeHook adapts VS Code chat's agent hooks (Preview) onto record
// semantics. The stdin payload matches Claude Code's (session_id, tool_name,
// tool_input), but the exit contract differs: exit 2 blocks the call and
// feeds stderr to the model, while a tripped breaker must exit 0 with
// {"continue":false} — a plain exit 3 is only a non-blocking warning in VS
// Code and would let the looping tool call proceed. Our own errors exit 1,
// which VS Code treats as fail-open.
func cmdVSCodeHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vscode-hook", flag.ContinueOnError)
	cacheDir := fs.String("cache-dir", "", "state directory override")
	if code, ok := parseFlags(fs, args, "vscode-hook", stderr); !ok {
		return code
	}
	raw, err := io.ReadAll(stdin)
	if err != nil || len(raw) == 0 {
		fmt.Fprintln(stderr, "loop-guard vscode-hook: expected hook JSON on stdin")
		return exitErr
	}
	var h claudeHookInput
	if err := json.Unmarshal(raw, &h); err != nil {
		fmt.Fprintf(stderr, "loop-guard vscode-hook: parse hook input: %v\n", err)
		return exitErr
	}
	session := h.SessionID
	if session == "" {
		session = vscodeSessionFallback
	}
	if h.ToolName == "" {
		fmt.Fprintln(stderr, "loop-guard vscode-hook: hook input needs a tool_name")
		return exitErr
	}

	g, err := newGuard(*cacheDir, detector.Config{}, guard.DefaultMaxInterventions)
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard vscode-hook: %v\n", err)
		return exitErr
	}
	v, err := g.Record(session, state.Event{
		Type: "tool",
		Name: h.ToolName,
		Args: rawArgs(h.ToolInput),
	})
	if err != nil {
		fmt.Fprintf(stderr, "loop-guard vscode-hook: %v\n", err)
		return exitErr
	}

	switch v.Action {
	case guard.ActionInject, guard.ActionInjectFinal:
		// Exit 2: VS Code blocks the call and shows stderr to the model.
		fmt.Fprintln(stderr, v.Message)
		return exitLoop
	case guard.ActionBreaker:
		// continue:false ends the whole agent session; stopReason is shown
		// to the user and carries the handoff artifact path.
		out, _ := json.Marshal(vscodeOutput{Continue: false, StopReason: v.Message})
		fmt.Fprintln(stdout, string(out))
		return exitOK
	default:
		fmt.Fprintln(stdout, `{"continue":true}`)
		return exitOK
	}
}
