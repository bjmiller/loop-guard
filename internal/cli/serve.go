package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/bjmiller/loop-guard/internal/mcp"
)

func cmdServe(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cacheDir := fs.String("cache-dir", "", "state directory override")
	if code, ok := parseFlags(fs, args, "serve", stderr); !ok {
		return code
	}
	fmt.Fprintln(stderr, "loop-guard: MCP server listening on stdio")
	mcp.Serve(stdin, stdout, *cacheDir)
	return exitOK
}
