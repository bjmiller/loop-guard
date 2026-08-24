package cli

import (
	"flag"
	"fmt"
	"io"
)

func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	harness := fs.String("harness", "custom", "which harness: custom, claude, or opencode")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "loop-guard init: %v\n", err)
		return exitErr
	}

	switch *harness {
	case "claude":
		fmt.Fprint(stdout, claudeInstructions)
	case "opencode":
		fmt.Fprint(stdout, opencodeInstructions)
	case "custom":
		fmt.Fprint(stdout, customInstructions)
	default:
		fmt.Fprintf(stderr, "loop-guard init: unknown harness %q (want custom, claude, or opencode)\n", *harness)
		return exitErr
	}
	return exitOK
}

const cliContract = `Loop-Guard integration contract
===============================
Every harness integration reduces to two shell-outs:

  1. Before each tool call, record it and evaluate:

       echo '{"type":"tool","name":"<tool>","args":<args>}' \
         | loop-guard record --session <SESSION_ID>

     Exit codes:
       0  allow the call
       2  block the call; stderr holds a recovery prompt — surface it to the model
       3  circuit breaker tripped; stop the session and show the handoff artifact
          path from stdout ("handoff_path")

  2. Optionally, after each model response:

       echo '{"type":"response","text":"<text>"}' | loop-guard record --session <SESSION_ID>

State lives under the platform cache dir (override: LOOPGUARD_CACHE_DIR or
--cache-dir). One JSON file per session; safe for concurrent processes.

MCP alternative (no hooks needed): register the server
    loop-guard serve
Tools exposed: loop_guard_record, loop_guard_check.
`

const claudeInstructions = cliContract + `
Claude Code setup (manual)
==========================
1. Install the binary anywhere (PATH not required).
2. Add to ~/.claude/settings.json (loop-guard doctor --fix does this for you,
   embedding the binary's ABSOLUTE path so PATH is irrelevant):

   {
     "hooks": {
       "PreToolUse": [
         { "matcher": "*", "hooks": [ { "type": "command", "command": "\"/abs/path/to/loop-guard\" claude-hook" } ] }
       ]
     }
   }

Claude passes {"session_id","tool_name","tool_input"} on stdin; the adapter maps
it onto ` + "`record`" + `. Exit code 2 blocks the tool call and feeds stderr back
to Claude as feedback — exactly the recovery prompt.
`

const opencodeInstructions = cliContract + `
OpenCode setup (manual)
=======================
1. Install the binary anywhere (PATH not required).
2. Copy the plugin into your config:

       mkdir -p .opencode/plugins
       loop-guard doctor --fix      # writes .opencode/plugins/loop-guard.js
                                    # with the binary's absolute path embedded

   Or fetch it from the install: internal/cli/assets/opencode-plugin.js in the repo.

The plugin hooks "tool.execute.before", calls ` + "`loop-guard record`" + `, and
throws the recovery prompt as an error on exit codes 2/3, which blocks the
tool call and shows the model what to do instead.
`

const customInstructions = cliContract + `
Wiring an unknown / future harness
==================================
Find the closest thing your harness has to "run a command before each tool
call" and point it at the contract above. Checklist:

  [ ] Session identity: pick a stable per-conversation SESSION_ID.
  [ ] Pre-tool-call hook: pipe a tool event JSON to "loop-guard record".  [ ] Exit 0: proceed. Exit 2: cancel the call, show stderr to the model.
  [ ] Exit 3: end the session; direct the human to handoff_path from stdout.
  [ ] Optional response recording after each model turn.
  [ ] If the harness speaks MCP instead: register "loop-guard serve".

See README.md section "Integrating a new harness" for worked examples.
`
