package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/brian/loop-guard/internal/cli"
)

// runCLI executes the CLI with LOOPGUARD_CACHE_DIR pointed at a temp dir and
// returns exit code, stdout, stderr.
func runCLI(args ...string) (int, string, string) {
	return runCLIWithStdin("", args...)
}

func runCLIWithStdin(stdin string, args ...string) (int, string, string) {
	var out, errB bytes.Buffer
	code := cli.Run(args, bytes.NewBufferString(stdin), &out, &errB)
	return code, out.String(), errB.String()
}

// recordJSON posts one event to `record` and returns parsed verdict + code.
func recordJSON(session, payload string) (int, map[string]any) {
	code, out, _ := runCLIWithStdin(payload, "record", "--session", session)
	var v map[string]any
	Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
	return code, v
}

// recordRaw posts a payload and returns code, stdout, stderr.
func recordRaw(session, payload string, extra ...string) (int, string, string) {
	args := append([]string{"record", "--session", session}, extra...)
	return runCLIWithStdin(payload, args...)
}

const loopToolEvent = `{"type":"tool","name":"bash","args":{"command":"flaky --retry"}}`

var _ = Describe("record", func() {
	var cacheDir string

	BeforeEach(func() {
		cacheDir = GinkgoT().TempDir()
		DeferCleanup(os.Unsetenv, "LOOPGUARD_CACHE_DIR")
		Expect(os.Setenv("LOOPGUARD_CACHE_DIR", cacheDir)).To(Succeed())
	})

	It("exits 0 and prints an allow verdict", func() {
		code, v := recordJSON("a1", loopToolEvent)
		Expect(code).To(Equal(0))
		Expect(v["action"]).To(Equal("allow"))
		Expect(v["ok"]).To(BeTrue())
	})

	It("exits 2 on loop detection with message and stderr feedback", func() {
		for i := 0; i < 2; i++ {
			code, v := recordJSON("a2", loopToolEvent)
			Expect(code).To(Equal(0))
			Expect(v["action"]).To(Equal("allow"))
		}
		code, v := recordJSON("a2", loopToolEvent)
		Expect(code).To(Equal(2))
		Expect(v["action"]).To(Equal("inject"))
		Expect(v["message"]).NotTo(BeEmpty())

		_, _, stderr := runCLIWithStdin(loopToolEvent, "record", "--session", "a2")
		Expect(stderr).NotTo(BeEmpty()) // harnesses surface this to the model
	})

	It("exits 3 once the breaker trips", func() {
		for i := 0; i < 5; i++ {
			recordJSON("a3", loopToolEvent)
		}
		code, v := recordJSON("a3", loopToolEvent)
		Expect(code).To(Equal(3))
		Expect(v["action"]).To(Equal("breaker"))

		handoff, _ := v["handoff_path"].(string)
		Expect(handoff).NotTo(BeEmpty())
		_, err := os.Stat(handoff)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects malformed events with exit 1", func() {
		code, _, stderr := runCLIWithStdin("{bad json", "record", "--session", "a4")
		Expect(code).To(Equal(1))
		Expect(stderr).To(ContainSubstring("parse"))
	})

	It("rejects missing session with exit 1", func() {
		code, _, stderr := runCLIWithStdin(loopToolEvent, "record")
		Expect(code).To(Equal(1))
		Expect(stderr).NotTo(BeEmpty())
	})

	It("honors threshold flags", func() {
		code, _, _ := recordRaw("a5", loopToolEvent, "--tool-threshold", "2")
		Expect(code).To(Equal(0)) // first occurrence: below threshold
		code, out, _ := recordRaw("a5", loopToolEvent, "--tool-threshold", "2")
		Expect(code).To(Equal(2)) // second occurrence trips threshold-2
		var v map[string]any
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
		Expect(v["loop"]).To(BeTrue())
	})
})

var _ = Describe("check", func() {
	var cacheDir string

	BeforeEach(func() {
		cacheDir = GinkgoT().TempDir()
		DeferCleanup(os.Unsetenv, "LOOPGUARD_CACHE_DIR")
		Expect(os.Setenv("LOOPGUARD_CACHE_DIR", cacheDir)).To(Succeed())
	})

	It("reports allow for unknown sessions without creating state", func() {
		code, out, _ := runCLI("check", "--session", "ghost")
		Expect(code).To(Equal(0))
		var v map[string]any
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
		Expect(v["action"]).To(Equal("allow"))
		_, err := os.Stat(filepath.Join(cacheDir, "ghost.json"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("projects breaker on next occurrence after exhausted interventions", func() {
		for i := 0; i < 4; i++ {
			recordJSON("c1", loopToolEvent)
		}
		code, out, _ := runCLI("check", "--session", "c1")
		Expect(code).To(Equal(2)) // live loop blocks, but check never claims a tripped breaker
		var v map[string]any
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
		Expect(v["action"]).To(Equal("inject_final"))
	})
})

var _ = Describe("claude-hook", func() {
	var cacheDir string

	BeforeEach(func() {
		cacheDir = GinkgoT().TempDir()
		DeferCleanup(os.Unsetenv, "LOOPGUARD_CACHE_DIR")
		Expect(os.Setenv("LOOPGUARD_CACHE_DIR", cacheDir)).To(Succeed())
	})

	claudeHookJSON := `{
		"session_id": "cl-123",
		"tool_name": "Bash",
		"tool_input": {"command": "npm test"}
	}`

	It("maps Claude hook payloads onto record semantics", func() {
		code, out, _ := runCLIWithStdin(claudeHookJSON, "claude-hook")
		Expect(code).To(Equal(0))
		var v map[string]any
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
		Expect(v["action"]).To(Equal("allow"))

		data, err := os.ReadFile(filepath.Join(cacheDir, "cl-123.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring("npm test"))
	})

	It("blocks with exit 2 when a loop is detected", func() {
		runCLIWithStdin(claudeHookJSON, "claude-hook")
		runCLIWithStdin(claudeHookJSON, "claude-hook")
		runCLIWithStdin(claudeHookJSON, "claude-hook") // 3rd: first detection
		code, _, stderr := runCLIWithStdin(claudeHookJSON, "claude-hook")
		Expect(code).To(Equal(2))
		Expect(stderr).NotTo(BeEmpty())
	})

	It("trips the breaker with exit 3 on the third detection", func() {
		for i := 0; i < 5; i++ {
			runCLIWithStdin(claudeHookJSON, "claude-hook")
		}
		code, out, _ := runCLIWithStdin(claudeHookJSON, "claude-hook")
		Expect(code).To(Equal(3))
		var v map[string]any
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
		Expect(v["handoff_path"]).NotTo(BeEmpty())
	})

	It("fails cleanly on malformed hook input", func() {
		code, _, _ := runCLIWithStdin("garbage", "claude-hook")
		Expect(code).To(Equal(1))
	})
})

var _ = Describe("version", func() {
	It("prints a version and exits 0", func() {
		code, out, _ := runCLI("version")
		Expect(code).To(Equal(0))
		Expect(out).NotTo(BeEmpty())
	})
})

var _ = Describe("usage errors", func() {
	It("returns exit 1 for unknown commands", func() {
		code, _, stderr := runCLI("bogus-command")
		Expect(code).To(Equal(1))
		Expect(stderr).NotTo(BeEmpty())
	})
})
