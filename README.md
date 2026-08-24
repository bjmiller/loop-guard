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

## Install (local experiment)

Nothing is published yet. Build from source:

```sh
go install github.com/brian/loop-guard@latest     # once pushed to a remote
# or from a checkout:
go build -o /usr/local/bin/loop-guard .
```

Then wire your harness(es):

```sh
loop-guard doctor          # report detected harnesses + self-tests
loop-guard doctor --fix    # auto-wire opencode / Claude Code configs
```

`--fix` is idempotent, merges JSON instead of overwriting it, and backs up any
file it modifies (`*.bak-loopguard`).

## The exit-code contract

Every harness integration is the same two shell-outs:

```sh
echo '{"type":"tool","name":"bash","args":{"command":"npm test"}}' \
  | loop-guard record --session "$SESSION_ID"
```

| Exit | Meaning | Harness action |
|------|---------|----------------|
| 0 | allow | proceed |
| 2 | loop detected | block the call; feed stderr (the recovery prompt) to the model |
| 3 | breaker tripped | end the session; show `handoff_path` from stdout JSON |

Verdict JSON on stdout always accompanies the exit code. `loop-guard check`
evaluates read-only (never records, never trips the breaker).

State lives in one JSON file per session under the platform cache dir
(`LOOPGUARD_CACHE_DIR` or `--cache-dir` override), safe for concurrent processes.

## Harness support

| Harness | Mechanism | Setup |
|---------|-----------|-------|
| OpenCode | plugin (`tool.execute.before`) | `doctor --fix`, or copy `internal/cli/assets/opencode-plugin.js` |
| Claude Code | `PreToolUse` hook (`claude-hook` adapter) | `doctor --fix`, or `init --harness claude` for manual steps |
| MCP-speaking harnesses | stdio MCP server | register command `loop-guard serve` |
| Anything else | generic CLI contract | `loop-guard init --harness custom` |

### Integrating a new harness

Run `loop-guard init --harness custom`. The short version: find your harness's
"run this before each tool call" hook, pipe a tool event JSON to
`loop-guard record --session <ID>`, and map exit codes 0/2/3 as above. If the
harness speaks MCP, registering `loop-guard serve` needs no hooks at all —
tools `loop_guard_record` and `loop_guard_check`.

## Tuning

| Flag | Default | Meaning |
|------|---------|---------|
| `--tool-threshold N` | 3 | identical calls within window before flagging |
| `--tool-window N` | 6 | recent calls considered |
| `--response-threshold N` | 3 | near-duplicate long responses before flagging |
| `--similarity F` | 0.8 | token-Jaccard threshold for "near-duplicate" |
| `--max-injections N` | 2 | recovery prompts before the breaker trips |

Responses under 40 characters are exempt from similarity detection (short
acknowledgements repeat naturally).

## The handoff artifact

When the breaker trips, `~/.cache/loop-guard/<session>-handoff.md` (platform
paths vary) contains: what was looping, recent activity, how many
interventions were ignored, and a suggested restart approach. Seed a **fresh**
session with this file — never replay the corrupted transcript.

## Distribution strategy (built, unpublished)

- goreleaser config produces checksummed static binaries for
  linux/darwin/windows (amd64+arm64) via `goreleaser build --snapshot`.
- `npm/` contains the esbuild-style launcher package (platform binaries as
  optionalDependencies) — testable locally with `npm pack`; never published.
- `Formula/loop-guard.rb` is ready for a Homebrew tap when the time comes.
- The prompt-level skill lives in `skill/avoid-loops/` and installs into any
  Agent Skills harness (e.g. `npx skills add <path-to-this-repo>`, pointing at
  `skill/avoid-loops`).

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
