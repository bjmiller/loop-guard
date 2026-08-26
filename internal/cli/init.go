package cli

import (
	"flag"
	"fmt"
	"io"
)
func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	harness := fs.String("harness", "custom", "which harness: custom, claude, opencode, codex, copilot, or pi")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "loop-guard init: %v\n", err)
		return exitErr
	}

	switch *harness {
	case "claude":
		fmt.Fprint(stdout, claudeInstructions)
	case "opencode":
		fmt.Fprint(stdout, opencodeInstructions)
	case "codex":
		io.WriteString(stdout, codexInstructions)
	case "copilot":
		io.WriteString(stdout, copilotInstructions)
	case "pi":
		io.WriteString(stdout, piInstructions)
	case "custom":
		fmt.Fprint(stdout, customInstructions)
	default:
		fmt.Fprintf(stderr, "loop-guard init: unknown harness %q (want custom, claude, opencode, codex, copilot, or pi)\n", *harness)
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

const codexInstructions = cliContract + `
OpenAI Codex CLI setup
======================
Codex reuses Claude Code's hook event names and stdin payload shape, so the
claude-hook adapter works as-is — only the config format differs.

Automatic: 'loop-guard doctor --fix' merges the hook into ~/.codex/hooks.json
(user) or .codex/hooks.json (project), backing up any existing file. It cannot
complete activation for you: run /hooks inside Codex and trust the new hook
(Codex >= 0.129 refuses to run untrusted hooks).

Manual steps if you prefer:

1. Enable hooks in .codex/config.toml (project) or ~/.codex/config.toml
   (user). Recent Codex versions default this to true:

        [features]
        hooks = true

2. Create .codex/hooks.json (project) or ~/.codex/hooks.json (user):

    {
      "hooks": {
        "PreToolUse": [
          {
            "matcher": "*",
            "hooks": [
              { "type": "command", "command": "\"/abs/path/to/loop-guard\" claude-hook" }
            ]
          }
        ]
      }
    }

   If your Codex version does not surface stderr on exit 2 as feedback to the
   model, wrap the adapter so a loop becomes an explicit block decision on
   stdout instead:

        #!/bin/sh
        # .codex/hooks/loop-guard.sh
        msg=$(mktemp)
        loop-guard claude-hook >/dev/null 2>"$msg"
        code=$?
        case $code in
          0) rm -f "$msg"; exit 0 ;;
          2|3)
            jq -Rs '{decision: "block", reason: .}' <"$msg"
            rm -f "$msg"; exit 0 ;;
          *) rm -f "$msg"; cat >&2; exit 0 ;;
        esac

3. Trust the hook: run /hooks inside Codex and approve it (Codex >= 0.129
   refuses to run untrusted hooks).

Exit code 3 (breaker) blocks the call and the recovery message names the
handoff artifact; end the session manually and restart fresh from that file.
`

const copilotInstructions = cliContract + `
GitHub Copilot CLI setup
========================
Automatic: 'loop-guard doctor --fix' writes loop-guard-hooks.json into
.github/hooks/ (project) or ~/.copilot/hooks/ ($COPILOT_HOME/hooks, user),
invoking the native 'copilot-hook' adapter — no jq or shell wrapper needed.

Manual steps if you prefer:
Copilot CLI runs command hooks at lifecycle points. preToolUse is FAIL-CLOSED:
exit 2 denies, and any other non-zero exit also denies. The wrapper below
therefore maps loop-guard's own errors to exit 0 (fail-open) so a broken
install never blocks every tool call.

1. Create .github/hooks/loop-guard.sh in your repository (or a user-level
   script under ~/.copilot/):

        #!/bin/sh
        # Maps Copilot's preToolUse payload onto loop-guard's generic contract.
        # Requires jq.
        payload=$(cat)
        session=$(printf '%s' "$payload" | jq -r '.sessionId // .session_id // "copilot"')
        tool=$(printf '%s' "$payload" | jq -r '.toolName // .tool_name // empty')
        [ -z "$tool" ] && exit 0
        args=$(printf '%s' "$payload" | jq -c '.toolInput // .input // .args // {}')

        msg=$(mktemp)
        printf '{"type":"tool","name":"%s","args":%s}' "$tool" "$args" \
          | loop-guard record --session "$session" >/dev/null 2>"$msg"
        code=$?
        case $code in
          0) ;;
          2|3) cat "$msg" >&2 ;;   # deny; Copilot feeds stderr back on exit 2
          *) ;;                    # our own error: allow rather than fail closed
        esac
        rm -f "$msg"
        [ "$code" -eq 0 ] && exit 0
        exit 2

2. Register it in .github/hooks/loop-guard-hooks.json (repository-level) or
   $COPILOT_HOME/hooks/loop-guard-hooks.json / ~/.copilot/hooks/
   (user-level):

    {
      "version": 1,
      "hooks": {
        "preToolUse": [
          { "type": "command", "bash": "/abs/path/to/.github/hooks/loop-guard.sh" }
        ]
      }
    }

Field names of the hook payload have varied across Copilot versions; if your
build sends different keys, adjust the jq selectors in step 1 and consult the
GitHub Copilot hooks reference for the exact schema.
`

const piInstructions = cliContract + `
Pi coding agent setup (badlogic/pi-mono)
========================================
Automatic: 'loop-guard doctor --fix' installs the extension into
.pi/extensions/ (project) or ~/.pi/agent/extensions/ (global) with the
binary's absolute path embedded.

Manual steps if you prefer:
Pi extensions are TypeScript modules auto-discovered from .pi/extensions/
(project) or ~/.pi/agent/extensions/ (global). The tool_call handler can
block with { block: true, reason }.

Create .pi/extensions/loop-guard.ts (or ~/.pi/agent/extensions/loop-guard.ts):

    import { spawnSync } from "node:child_process";

    export default function (pi) {
      pi.on("tool_call", async (event, ctx) => {
        const bin = process.env.LOOPGUARD_BINARY ?? "loop-guard";
        const session =
          String(ctx.sessionManager.getSessionId?.() ?? "pi");
        let r;
        try {
          r = spawnSync(
            bin,
            ["record", "--session", session],
            {
              input: JSON.stringify({
                type: "tool",
                name: event.toolName,
                args: event.input ?? {},
              }),
              encoding: "utf8",
            },
          );
        } catch {
          return; // never break pi because loop-guard failed to run
        }
        if (r.status === 2 || r.status === 3) {
          const reason = (r.stderr || "").trim() || "Loop-Guard blocked this call.";
          if (r.status === 3) ctx.abort(); // breaker tripped: stop this session
          return { block: true, reason };
        }
      });
    }

Reload with /reload (or restart pi). Exit code 3 additionally aborts the
session; seed a FRESH session with the handoff artifact named in the reason.
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
