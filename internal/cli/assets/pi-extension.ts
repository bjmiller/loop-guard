// Loop-Guard extension for the Pi coding agent (badlogic/pi-mono).
//
// Installed by `loop-guard doctor --fix` into .pi/extensions/ (project) or
// ~/.pi/agent/extensions/ (global). Hooks "tool_call", calls the loop-guard
// binary, and blocks the call when it detects a loop, surfacing the recovery
// prompt as the block reason. If the binary is not installed this extension
// is a no-op, so it is always safe to keep loaded.
import { spawnSync } from "node:child_process";

// Injected at install time by `loop-guard doctor --fix`, which replaces this
// string-literal placeholder wholesale with a JSON-quoted absolute path.
// When null, only PATH lookup via the bare name is used.
const LOOPGUARD_BIN = "__LOOPGUARD_BIN__"

export default function (pi) {
  pi.on("tool_call", async (event, ctx) => {
    const bin = LOOPGUARD_BIN.startsWith("__")
      ? process.env.LOOPGUARD_BINARY || "loop-guard"
      : LOOPGUARD_BIN;
    const session = String(
      ctx.sessionManager.getSessionId?.() ?? "pi",
    );
    let r;
    try {
      r = spawnSync(bin, ["record", "--session", session], {
        input: JSON.stringify({
          type: "tool",
          name: event.toolName,
          args: event.input ?? {},
        }),
        encoding: "utf8",
      });
    } catch {
      return; // never break pi because loop-guard failed to run
    }
    if (r.status === 2 || r.status === 3) {
      const reason =
        (r.stderr || "").trim() || "Loop-Guard blocked this call.";
      if (r.status === 3) ctx.abort(); // breaker tripped: stop this session
      return { block: true, reason };
    }
  });
}
