// Loop-Guard plugin for OpenCode.
//
// Installed by `loop-guard doctor --fix` into .opencode/plugins/ (or
// ~/.config/opencode/plugins/). Runs inside opencode's embedded Bun runtime —
// no Node.js or npm install required; the node:* imports below are Bun
// built-ins. Blocks tool calls when the loop-guard binary detects a loop,
// surfacing the recovery prompt to the model as the error message. If the
// binary is not installed this plugin is a no-op, so it is always safe to
// keep loaded.
import { spawnSync } from "node:child_process"
import { homedir } from "node:os"
import { join } from "node:path"

// Injected at install time by `loop-guard doctor --fix` (JSON-quoted absolute
// path). When null, only the candidate scan below is used.
const LOOPGUARD_BIN = "__LOOPGUARD_BIN__"

const candidates = [
  LOOPGUARD_BIN.startsWith("__") ? null : LOOPGUARD_BIN,
  process.env.LOOPGUARD_BINARY || null,
  "loop-guard",
  join(homedir(), ".local", "bin", "loop-guard"),
  join(homedir(), "go", "bin", "loop-guard"),
  "/usr/local/bin/loop-guard",
  "/opt/homebrew/bin/loop-guard",
].filter(Boolean)

let resolved

function findBinary() {
  if (resolved !== undefined) return resolved
  for (const candidate of candidates) {
    try {
      const r = spawnSync(candidate, ["version"], { encoding: "utf8" })
      if (r.status === 0) {
        resolved = candidate
        return resolved
      }
    } catch {
      // keep looking
    }
  }
  resolved = null
  return null
}

export const LoopGuard = async ({}) => ({
  "tool.execute.before": async (input, output) => {
    const bin = findBinary()
    if (!bin) return
    const event = {
      type: "tool",
      name: input.tool,
      args: output.args ?? {},
    }
    const session = String(input.sessionID ?? "default")
    let r
    try {
      r = spawnSync(bin, ["record", "--session", session], {
        input: JSON.stringify(event),
        encoding: "utf8",
      })
    } catch (e) {
      // Never break the harness because loop-guard failed to run.
      return
    }
    if (r.status === 2 || r.status === 3) {
      const message = (r.stderr || "").trim() || "Loop-Guard blocked this call."
      throw new Error(message)
    }
  },
})
