package mcp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/brian/loop-guard/internal/mcp"
)

// callServer runs one Serve goroutine over scripted input lines and returns
// the raw output.
func callServer(cacheDir string, input string) string {
	var out bytes.Buffer
	in := strings.NewReader(input)
	done := make(chan struct{})
	go func() {
		defer close(done)
		mcp.Serve(in, &out, cacheDir)
	}()
	Eventually(done, 2*time.Second).Should(BeClosed())
	return out.String()
}

// respFor finds the response line whose id matches (or the id-less error
// response when methodID is empty).
func respFor(raw, methodID string) map[string]any {
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if methodID == "" && m["id"] == nil {
			return m
		}
		if s, ok := m["id"].(string); ok && s == methodID {
			return m
		}
	}
	return nil
}

var _ = Describe("Serve", func() {
	var cacheDir string

	BeforeEach(func() {
		cacheDir = GinkgoT().TempDir()
	})

	initialize := `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{}}` + "\n"

	It("responds to initialize with protocol version and capabilities", func() {
		raw := callServer(cacheDir, initialize)
		r := respFor(raw, "init")
		Expect(r).NotTo(BeNil())
		result := r["result"].(map[string]any)
		Expect(result["protocolVersion"]).NotTo(BeEmpty())
		Expect(result["serverInfo"].(map[string]any)["name"]).To(Equal("loop-guard"))
	})

	It("does not respond to notifications", func() {
		raw := callServer(cacheDir, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
		Expect(strings.TrimSpace(raw)).To(BeEmpty())
	})

	It("lists loop-guard tools", func() {
		raw := callServer(cacheDir, initialize+`{"jsonrpc":"2.0","id":"tl","method":"tools/list"}`+"\n")
		r := respFor(raw, "tl")
		tools := r["result"].(map[string]any)["tools"].([]any)
		names := []string{}
		for _, t := range tools {
			names = append(names, t.(map[string]any)["name"].(string))
		}
		Expect(names).To(ConsistOf("loop_guard_record", "loop_guard_check"))
	})

	It("answers ping", func() {
		raw := callServer(cacheDir, initialize+`{"jsonrpc":"2.0","id":"p","method":"ping"}`+"\n")
		Expect(respFor(raw, "p")["result"]).NotTo(BeNil())
	})

	It("returns a JSON-RPC error for unknown methods", func() {
		raw := callServer(cacheDir, initialize+`{"jsonrpc":"2.0","id":"x","method":"no/such"}`+"\n")
		r := respFor(raw, "x")
		Expect(int(r["error"].(map[string]any)["code"].(float64))).To(Equal(-32601))
	})

	It("returns a parse error for malformed lines without killing the server", func() {
		raw := callServer(cacheDir, "{oops\n"+initialize)
		Expect(respFor(raw, "")).NotTo(BeNil()) // -32700 response
		Expect(respFor(raw, "init")).NotTo(BeNil())
	})

	It("records tool calls and escalates through the ladder via tools/call", func() {
		recPayload := func(id int) string {
			return fmt.Sprintf(`{"jsonrpc":"2.0","id":"c%d","method":"tools/call","params":{"name":"loop_guard_record","arguments":{"session":"mcp-1","type":"tool","name":"bash","args":{"command":"flaky"}}}}`+"\n", id)
		}
		lines := initialize
		for i := 1; i <= 3; i++ {
			lines += recPayload(i)
		}
		raw := callServer(cacheDir, lines)

		var verdict map[string]any
		r := respFor(raw, "c3")
		Expect(r).NotTo(BeNil())
		content := r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
		Expect(json.Unmarshal([]byte(content["text"].(string)), &verdict)).To(Succeed())
		Expect(verdict["action"]).To(Equal("inject"))

		checkRaw := callServer(cacheDir, initialize+
			`{"jsonrpc":"2.0","id":"ck","method":"tools/call","params":{"name":"loop_guard_check","arguments":{"session":"mcp-1"}}}`+"\n")
		r = respFor(checkRaw, "ck")
		content = r["result"].(map[string]any)["content"].([]any)[0].(map[string]any)
		var cv map[string]any
		Expect(json.Unmarshal([]byte(content["text"].(string)), &cv)).To(Succeed())
		Expect(cv["loop"]).To(BeTrue())
	})
})
