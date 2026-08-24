// Package mcp exposes loop-guard as a Model Context Protocol server over
// stdio (newline-delimited JSON-RPC 2.0), so any MCP-speaking harness can use
// it with zero per-harness integration code.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/brian/loop-guard/internal/detector"
	"github.com/brian/loop-guard/internal/guard"
	"github.com/brian/loop-guard/internal/state"
)

const protocolVersion = "2024-11-05"

// Serve reads newline-delimited JSON-RPC requests until EOF and writes
// responses to out. cacheDir overrides the state directory; empty means default.
func Serve(in io.Reader, out io.Writer, cacheDir string) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeError(out, nil, -32700, "parse error")
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue // notification: no response
		}

		switch req.Method {
		case "initialize":
			writeResult(out, req.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "loop-guard", "version": cliVersion()},
			})
		case "ping":
			writeResult(out, req.ID, map[string]any{})
		case "tools/list":
			writeResult(out, req.ID, map[string]any{"tools": toolDefs()})
		case "tools/call":
			handleToolCall(out, req.ID, req.Params, cacheDir)
		default:
			writeError(out, req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
		}
	}
}

func cliVersion() string { return "0.1.0" }

func handleToolCall(out io.Writer, id json.RawMessage, params json.RawMessage, cacheDir string) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		writeError(out, id, -32602, "invalid params")
		return
	}

	var args struct {
		Session  string          `json:"session"`
		Type     string          `json:"type"`
		Name     string          `json:"name,omitempty"`
		Args     json.RawMessage `json:"args,omitempty"`
		Text     string          `json:"text,omitempty"`
		CacheDir string          `json:"cache_dir,omitempty"`
	}
	dir := cacheDir
	if err := json.Unmarshal(p.Arguments, &args); err != nil {
		writeError(out, id, -32602, "invalid arguments")
		return
	}
	if dir == "" {
		dir = args.CacheDir
	}

	g := &guard.Guard{Store: &state.Store{Dir: resolveDir(dir)}, Config: detector.Config{}, MaxInterventions: guard.DefaultMaxInterventions}

	var v guard.Verdict
	switch p.Name {
	case "loop_guard_record":
		if args.Session == "" {
			writeToolError(out, id, "session is required")
			return
		}
		ev := state.Event{Type: args.Type, Name: args.Name, Text: args.Text}
		if len(args.Args) > 0 {
			json.Unmarshal(args.Args, &ev.Args)
		}
		var err error
		v, err = g.Record(args.Session, ev)
		if err != nil {
			writeToolError(out, id, err.Error())
			return
		}
	case "loop_guard_check":
		if args.Session == "" {
			writeToolError(out, id, "session is required")
			return
		}
		v = g.Check(args.Session)
	default:
		writeError(out, id, -32602, fmt.Sprintf("unknown tool: %s", p.Name))
		return
	}

	data, _ := json.Marshal(v)
	writeResult(out, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(data)}},
	})
}

func resolveDir(dir string) string {
	if dir != "" {
		return dir
	}
	d, err := state.DefaultDir()
	if err != nil {
		return ".loop-guard"
	}
	return d
}

func toolDefs() []map[string]any {
	recordSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session":   map[string]any{"type": "string", "description": "Session identifier"},
			"type":      map[string]any{"type": "string", "enum": []string{"tool", "response"}},
			"name":      map[string]any{"type": "string", "description": "Tool name for type=tool"},
			"args":      map[string]any{"type": "object", "description": "Tool arguments for type=tool"},
			"text":      map[string]any{"type": "string", "description": "Response text for type=response"},
			"cache_dir": map[string]any{"type": "string", "description": "Optional state directory override"},
		},
		"required": []string{"session", "type"},
	}
	checkSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session":   map[string]any{"type": "string", "description": "Session identifier"},
			"cache_dir": map[string]any{"type": "string"},
		},
		"required": []string{"session"},
	}
	return []map[string]any{
		{
			"name":        "loop_guard_record",
			"description": "Record one agent event (tool call or model response) and return the loop-guard verdict. Actions: allow, inject (block+recovery prompt), inject_final, breaker (stop session).",
			"inputSchema": recordSchema,
		},
		{
			"name":        "loop_guard_check",
			"description": "Read-only check of a session for live loops. Never mutates state or trips the breaker.",
			"inputSchema": checkSchema,
		},
	}
}

func writeResult(out io.Writer, id json.RawMessage, result any) {
	b, _ := json.Marshal(result)
	fmt.Fprintf(out, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":%s}\n", id, b)
}

func writeError(out io.Writer, id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	fmt.Fprintf(out, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":%d,\"message\":%q}}\n", id, code, msg)
}

func writeToolError(out io.Writer, id json.RawMessage, msg string) {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	})
	fmt.Fprintf(out, "{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":%s}\n", id, b)
}
