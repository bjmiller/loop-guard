// Package version holds the build-stamped loop-guard version. It lives in
// its own leaf package so both the CLI and the MCP server can report the
// same value set via -ldflags at build time.
package version

// Version is stamped at build time via -ldflags.
var Version = "0.1.0"
