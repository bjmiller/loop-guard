package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/brian/loop-guard/internal/mcp"
)

func newServeFlagSet() *flag.FlagSet {
	return flag.NewFlagSet("serve", flag.ContinueOnError)
}

func cmdServe(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newServeFlagSet()
	cacheDir := fs.String("cache-dir", "", "state directory override")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "loop-guard serve: %v\n", err)
		return exitErr
	}
	fmt.Fprintln(stderr, "loop-guard: MCP server listening on stdio")
	mcp.Serve(stdin, stdout, *cacheDir)
	return exitOK
}
