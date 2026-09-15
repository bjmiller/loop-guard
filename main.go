// Command loop-guard prevents coding agents from looping forever.
//
// It records tool calls and model responses per session, detects repetition
// (exact fingerprinting for tools, fuzzy similarity for responses), injects
// recovery prompts through harness hooks, and trips a circuit breaker with a
// handoff artifact when a thread cannot recover.
package main

import (
	"os"

	"github.com/bjmiller/loop-guard/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
