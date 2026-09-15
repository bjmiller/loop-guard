# loop-guard

Infinite-loop prevention for coding agents. Portable across harnesses.

loop-guard watches an agent session's tool calls and model responses, detects
when the agent is stuck in a repetition loop, injects a recovery prompt, and —
if the thread is too corrupted to recover — trips a circuit breaker and writes
a handoff artifact so work can resume in a fresh session.

## Why

Agents sometimes spin: re-running the same failing command forever, emitting
near-identical responses, retrying a broken build dozens of times. Prompting
alone cannot stop this because a corrupted context ignores prompts. loop-guard
enforces from outside the conversation:

1. **Detect** — deterministic fingerprinting of tool calls plus fuzzy
   similarity matching of response text.
2. **Interrupt + instruct** — block the repeated call and inject a recovery
   prompt the model must follow.
3. **Breaker** — after two ignored interventions, stop talking to the thread,
   write a handoff artifact, and let a fresh session pick up the task.

## Install

Requires Go 1.25 or newer.

```sh
go install github.com/bjmiller/loop-guard@latest
```

From a checkout instead:

```sh
go build -o /usr/local/bin/loop-guard .
```

Then wire your harness(es):

```sh
loop-guard doctor          # report detected harnesses + self-tests
loop-guard doctor --fix    # auto-wire opencode / Claude Code / Codex /
                           # Copilot CLI / Pi / VS Code configs
```

`--fix` is idempotent, merges JSON instead of overwriting it, and backs up any
file it modifies (`*.bak-loopguard`).

## The exit-code contract

Every harness integration is the same two shell-outs:

```sh
echo '{"type":"tool","name":"bash","args":{"command":"npm test"}}' \
  | loop-guard record --session "$SESSION_ID"
```

| Exit | Meaning         | Harness action                                                 |
| ---- | --------------- | -------------------------------------------------------------- |
| 0    | allow           | proceed                                                        |
| 2    | loop detected   | block the call; feed stderr (the recovery prompt) to the model |
| 3    | breaker tripped | end the session; show `handoff_path` from stdout JSON          |

`record` and `check` always print verdict JSON on stdout alongside the exit
code. Harness adapters own stdout for their hook protocol instead (VS Code
reads its own JSON; Claude Code must not receive foreign JSON), so they carry
the message on stderr. `loop-guard check` evaluates read-only (never records,
never trips the breaker).

Exit 3 is the generic contract; several harnesses cannot "end a session" from a
hook. The adapters map it onto whatever their hook protocol supports:

| Harness                | Loop (2)                        | Breaker (3)                                                        |
| ---------------------- | ------------------------------- | ------------------------------------------------------------------ |
| `record` / custom      | block via exit 2                | exit 3; human ends the session                                     |
| Claude Code / Codex    | exit 2, stderr to the model     | also exit 2 (the only blocking code), message names the handoff    |
| Copilot CLI            | exit 2 denies (stderr shown)    | exit 3 also denies, blocking the call                              |
| VS Code chat           | exit 2, stderr to the model     | exit 0 + `{"continue":false}`, `stopReason` names the handoff      |
| Pi                     | `{ block: true, reason }`       | same, plus `ctx.abort()` ends the session                          |
| OpenCode plugin        | thrown error blocks and informs | thrown error; the human restarts from the handoff                  |

State lives in one JSON file per session under the platform cache dir
(`LOOPGUARD_CACHE_DIR` or `--cache-dir` override), safe for concurrent processes.

## Harness support

| Harness                                | Mechanism                                                                                               | Setup                                                                      |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------- |
| OpenCode                               | plugin (`tool.execute.before`)                                                                          | `doctor --fix`, or copy `internal/cli/assets/opencode-plugin.js`           |
| Claude Code (CLI, IDE, or Desktop app) | `PreToolUse` hook (`claude-hook` adapter) — Desktop fires the same hooks from `~/.claude/settings.json` | `doctor --fix`, or `init --harness claude` for manual steps                |
| Codex CLI / ChatGPT desktop app        | `PreToolUse` hook in `.codex/hooks.json` (shared config; exit 2 + stderr blocks)                        | `doctor --fix`, or `init --harness codex`; trust via `/hooks` inside Codex |
| GitHub Copilot CLI                     | `preToolUse` command hook (`.github/hooks/`) via the native `copilot-hook` adapter                      | `doctor --fix`, or `init --harness copilot`                                |
| VS Code chat (agent hooks, Preview)    | `PreToolUse` hook in `.github/hooks/loop-guard.json` (flat format) via the native `vscode-hook` adapter; the breaker emits `continue:false` to end the session | `doctor --fix`, or `init --harness vscode`                          |
| Pi coding agent                        | TypeScript extension (`tool_call` event)                                                                | `doctor --fix`, or `init --harness pi`                                     |
| MCP-speaking harnesses                 | stdio MCP server                                                                                        | register command `loop-guard serve`                                        |
| Anything else                          | generic CLI contract                                                                                    | `loop-guard init --harness custom`                                         |

VS Code also reads `.claude/settings.json` (Claude Code format) and Copilot
CLI's `.github/hooks` configs, so those wirings cover VS Code chat too — but
only the native `vscode-hook` adapter maps the breaker onto `continue:false`
(a bare exit 3 is just a non-blocking warning there, and the looping call
would proceed). Because VS Code loads every file in `.github/hooks/` and
parses both formats, `doctor --fix` wires VS Code and Copilot CLI mutually
exclusively per workspace.

### Binary location (no PATH requirement)

Generated configs never rely on PATH. `loop-guard doctor --fix` resolves the
binary in this order and embeds the absolute path into every config it writes:

1. `LOOPGUARD_BINARY` env override
2. the running binary itself (`doctor --fix` executes as the installed
   loop-guard, so its own location is authoritative)
3. well-known install locations (`~/.local/bin`, `~/go/bin`, `/usr/local/bin`,
   `/opt/homebrew/bin`, `%LOCALAPPDATA%\Programs\loop-guard`)
4. bare name as a last resort

The OpenCode plugin additionally scans those locations at runtime if the
embedded path stops working (e.g. you moved the binary). If you relocate it,
re-run `doctor --fix` after removing the stale hook/plugin, or set
`LOOPGUARD_BINARY`.

### Integrating a new harness

Run `loop-guard init --harness custom`. The short version: find your harness's
"run this before each tool call" hook, pipe a tool event JSON to
`loop-guard record --session <ID>`, and map exit codes 0/2/3 as above. If the
harness speaks MCP, registering `loop-guard serve` needs no hooks at all —
tools `loop_guard_record` and `loop_guard_check`.

## Tuning

| Flag                     | Default | Meaning                                       |
| ------------------------ | ------- | --------------------------------------------- |
| `--tool-threshold N`     | 3       | identical calls within window before flagging |
| `--tool-window N`        | 6       | recent calls considered                       |
| `--response-threshold N` | 3       | near-duplicate long responses before flagging |
| `--similarity F`         | 0.8     | token-Jaccard threshold for "near-duplicate"  |
| `--max-injections N`     | 2       | recovery prompts before the breaker trips     |

Responses under 40 characters are exempt from similarity detection (short
acknowledgements repeat naturally).

## The handoff artifact

When the breaker trips, `~/.cache/loop-guard/<session>-handoff.md` (platform
paths vary) contains: what was looping, recent activity, how many
interventions were ignored, and a suggested restart approach. Seed a **fresh**
session with this file — never replay the corrupted transcript.

## Distribution

- goreleaser builds checksummed static binaries for linux, darwin, and windows
  (amd64 and arm64). For a local build: `goreleaser build --snapshot --clean`.
- `npm/` holds the launcher package. It execs the platform binary installed
  through optionalDependencies, and `npm pack` builds a tarball for local
  testing.
- `Formula/loop-guard.rb` is the Homebrew formula template for the
  `homebrew-loop-guard` tap. It matches goreleaser's output; checksums come
  from the release `checksums.txt`.
- The prompt-level skill lives in `skill/avoid-loops/` and installs into any
  Agent Skills harness, for example
  `npx skills add https://github.com/bjmiller/loop-guard`.

## Development

```sh
go test ./...        # Ginkgo/Gomega BDD suites, all packages
goreleaser check     # validate release config
goreleaser build --snapshot --clean   # local multi-platform builds
```

Layout: `internal/detector` (fingerprinting + similarity), `internal/state`
(session persistence + locking), `internal/guard` (escalation ladder +
handoff), `internal/cli` (CLI contract, doctor, init, embedded plugin),
`internal/mcp` (MCP server).
