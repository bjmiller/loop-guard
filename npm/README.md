# npm distribution (built locally; never published while experimental)

This directory contains the launcher-package structure for distributing the
Go binary through npm with registry-verified integrity — no curl-pipe-shell.

## Layout

- `package.json` — thin launcher; platform binaries are `optionalDependencies`
- `bin/loop-guard.js` — resolves the binary and execs it

Binary resolution order:
1. `LOOPGUARD_BINARY` env var
2. Platform package `@loop-guard/<os>-<arch>` (each would ship one static
   binary; created by CI from goreleaser output when publishing begins)
3. `dist/` directory next to the package (snapshot builds)

## Local testing without publishing

```sh
go build -o npm/dist/loop-guard .          # snapshot build
npm pack ./npm                              # produces loop-guard-0.1.0.tgz
npm install -g ./loop-guard-0.1.0.tgz       # installs launcher + local dist
loop-guard version
```

When ready to publish for real: split goreleaser output into per-platform
packages (`@loop-guard/darwin-arm64` etc.), `npm publish` each, then the main
launcher. Registry checksums provide the integrity guarantee.
