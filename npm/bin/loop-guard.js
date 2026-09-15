#!/usr/bin/env node
// loop-guard launcher: locates the platform binary and execs it.
//
// Resolution order:
//   1. LOOPGUARD_BINARY env var
//   2. optionalDependencies platform packages (@loop-guard/<os>-<arch>)
//   3. a dist/ directory next to this package (snapshot builds)
"use strict";

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const PLATFORMS = {
  darwin: { arm64: "@loop-guard/darwin-arm64", x64: "@loop-guard/darwin-x64" },
  linux: { arm64: "@loop-guard/linux-arm64", x64: "@loop-guard/linux-x64" },
  win32: { x64: "@loop-guard/win32-x64" },
};

function fail(msg) {
  process.stderr.write(`loop-guard: ${msg}\n`);
  process.exit(1);
}

function resolveBinary() {
  if (process.env.LOOPGUARD_BINARY) return process.env.LOOPGUARD_BINARY;

  const platform = PLATFORMS[process.platform];
  const pkg = platform && platform[process.arch];
  if (pkg) {
    try {
      const exe = process.platform === "win32" ? "loop-guard.exe" : "loop-guard";
      const candidate = path.join(path.dirname(require.resolve(`${pkg}/package.json`)), exe);
      if (fs.existsSync(candidate)) return candidate;
    } catch {
      // fall through to dist/
    }
  }

  const exe = process.platform === "win32" ? "loop-guard.exe" : "loop-guard";
  const distCandidate = path.join(__dirname, "..", "dist", exe);
  if (fs.existsSync(distCandidate)) return distCandidate;

  fail(
    `no binary found for ${process.platform}-${process.arch}. ` +
      `Install from source: go install github.com/bjmiller/loop-guard@latest, ` +
      `or set LOOPGUARD_BINARY to the binary path.`
  );
}

const result = spawnSync(resolveBinary(), process.argv.slice(2), {
  stdio: "inherit",
});
if (result.error) fail(result.error.message);
if (result.signal) {
  // Propagate the child's fatal signal to this process instead of masking it
  // as exit 1 (Ctrl-C, SIGTERM, ...).
  process.kill(process.pid, result.signal);
}
process.exit(result.status ?? 1);
