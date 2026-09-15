package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bjmiller/loop-guard/internal/cli"
)

// isolateHome points HOME/USERPROFILE/XDG_CONFIG_HOME (and the Windows app
// data vars) at a temp dir and restores them when the test finishes.
func isolateHome() string {
	dir := GinkgoT().TempDir()
	keys := []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA", "COPILOT_HOME", "LOOPGUARD_BINARY"}
	old := map[string]string{}
	for _, key := range keys {
		old[key] = os.Getenv(key)
	}
	expectNoErr(os.Setenv("HOME", dir))
	if isWindows() {
		expectNoErr(os.Setenv("USERPROFILE", dir))
		expectNoErr(os.Setenv("APPDATA", dir))
		expectNoErr(os.Setenv("LOCALAPPDATA", dir))
	} else {
		os.Unsetenv("USERPROFILE")
		os.Unsetenv("APPDATA")
		os.Unsetenv("LOCALAPPDATA")
	}
	os.Unsetenv("COPILOT_HOME")
	os.Unsetenv("LOOPGUARD_BINARY")
	expectNoErr(os.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config")))
	DeferCleanup(func() {
		for k, v := range old {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	})
	return dir
}

// chdirTo moves into dir and restores the original working directory after
// the spec, so specs never inherit a deleted temp dir as their cwd.
func chdirTo(dir string) {
	orig, err := os.Getwd()
	expectNoErr(err)
	expectNoErr(os.Chdir(dir))
	DeferCleanup(func() { expectNoErr(os.Chdir(orig)) })
}

func isWindows() bool { return runtime.GOOS == "windows" }

// claudeDesktopDir mirrors doctor's per-platform Claude Desktop detection path.
func claudeDesktopDir(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude")
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Claude")
	default:
		return filepath.Join(home, ".config", "Claude")
	}
}

// chatGPTDesktopDir mirrors doctor's per-platform ChatGPT Desktop path.
func chatGPTDesktopDir(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "com.openai.chat")
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "ChatGPT")
	default:
		return filepath.Join(home, ".config", "ChatGPT")
	}
}

func expectNoErr(err error) {
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

// quotedBin mirrors the shell quoting doctor uses in generated hook commands.
func quotedBin(suffix string) string {
	exe := mustExe()
	if isWindows() {
		return `"` + exe + `" ` + suffix
	}
	return "'" + exe + "' " + suffix
}

var _ = Describe("copilot-hook", func() {
	var cacheDir string

	BeforeEach(func() {
		cacheDir = GinkgoT().TempDir()
		DeferCleanup(os.Unsetenv, "LOOPGUARD_CACHE_DIR")
		Expect(os.Setenv("LOOPGUARD_CACHE_DIR", cacheDir)).To(Succeed())
	})

	It("maps a camelCase Copilot payload onto record semantics", func() {
		code, out, _ := runCLIWithStdin(
			`{"conversationId":"conv-1","toolName":"bash","toolInput":{"command":"npm test"}}`,
			"copilot-hook")
		Expect(code).To(Equal(0))
		var v map[string]any
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
		Expect(v["action"]).To(Equal("allow"))

		data, err := os.ReadFile(filepath.Join(cacheDir, "conv-1.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring("npm test"))
	})

	It("accepts snake_case spellings and sessionId fallback", func() {
		code, _, _ := runCLIWithStdin(
			`{"session_id":"sess-2","tool_name":"bash","args":{"command":"ls"}}`,
			"copilot-hook")
		Expect(code).To(Equal(0))
		_, err := os.Stat(filepath.Join(cacheDir, "sess-2.json"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("blocks with exit 2 on loop detection", func() {
		payload := `{"conversationId":"conv-3","toolName":"Bash","toolInput":{"command":"flaky"}}`
		for i := 0; i < 2; i++ {
			runCLIWithStdin(payload, "copilot-hook")
		}
		code, _, stderr := runCLIWithStdin(payload, "copilot-hook")
		Expect(code).To(Equal(2))
		Expect(stderr).NotTo(BeEmpty())
	})
})

var _ = Describe("vscode-hook", func() {
	var cacheDir string

	BeforeEach(func() {
		cacheDir = GinkgoT().TempDir()
		DeferCleanup(os.Unsetenv, "LOOPGUARD_CACHE_DIR")
		Expect(os.Setenv("LOOPGUARD_CACHE_DIR", cacheDir)).To(Succeed())
	})

	It("maps VS Code hook payloads onto record semantics", func() {
		code, out, _ := runCLIWithStdin(
			`{"session_id":"vsc-1","tool_name":"create_file","tool_input":{"filePath":"a.go"}}`,
			"vscode-hook")
		Expect(code).To(Equal(0))
		var v map[string]any
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
		Expect(v).To(HaveKeyWithValue("continue", true))

		data, err := os.ReadFile(filepath.Join(cacheDir, "vsc-1.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring("a.go"))
	})

	It("falls back to a shared session when session_id is omitted", func() {
		code, _, _ := runCLIWithStdin(`{"tool_name":"bash","tool_input":{"command":"make"}}`, "vscode-hook")
		Expect(code).To(Equal(0))
		_, err := os.Stat(filepath.Join(cacheDir, "vscode.json"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("blocks with exit 2 and the recovery prompt on stderr", func() {
		payload := `{"session_id":"vsc-3","tool_name":"Bash","tool_input":{"command":"flaky"}}`
		for i := 0; i < 2; i++ {
			runCLIWithStdin(payload, "vscode-hook")
		}
		code, _, stderr := runCLIWithStdin(payload, "vscode-hook")
		Expect(code).To(Equal(2))
		Expect(stderr).NotTo(BeEmpty())
	})

	It("ends the session with continue:false instead of exit 3 when the breaker trips", func() {
		payload := `{"session_id":"vsc-4","tool_name":"Bash","tool_input":{"command":"flaky"}}`
		for i := 0; i < 5; i++ {
			runCLIWithStdin(payload, "vscode-hook")
		}
		code, out, _ := runCLIWithStdin(payload, "vscode-hook")
		Expect(code).To(Equal(0)) // exit 3 would be a non-blocking warning in VS Code
		var v map[string]any
		Expect(json.Unmarshal([]byte(out), &v)).To(Succeed())
		Expect(v["continue"]).To(BeFalse())
		Expect(v["stopReason"]).To(ContainSubstring("handoff"))
	})

	It("fails cleanly on malformed hook input", func() {
		code, _, _ := runCLIWithStdin("garbage", "vscode-hook")
		Expect(code).To(Equal(1))
	})
})

var _ = Describe("doctor --fix (codex/copilot/pi)", func() {
	It("merges a PreToolUse hook into an existing Codex hooks.json with backup", func() {
		home := isolateHome()
		codexDir := filepath.Join(home, ".codex")
		expectNoErr(os.MkdirAll(codexDir, 0o755))
		hooksPath := filepath.Join(codexDir, "hooks.json")
		expectNoErr(os.WriteFile(hooksPath, []byte(`{"hooks":{"PreToolUse":[]}}`), 0o600))

		code, out, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("wired codex"))
		Expect(out).To(ContainSubstring("/hooks"))

		data, err := os.ReadFile(hooksPath)
		expectNoErr(err)
		var hooks struct {
			Hooks struct {
				PreToolUse []struct {
					Matcher string `json:"matcher"`
					Hooks   []struct {
						Command string `json:"command"`
					} `json:"hooks"`
				} `json:"PreToolUse"`
			} `json:"hooks"`
		}
		expectNoErr(json.Unmarshal(data, &hooks))
		Expect(hooks.Hooks.PreToolUse).To(HaveLen(1))
		Expect(hooks.Hooks.PreToolUse[0].Hooks[0].Command).
			To(Equal(quotedBin("claude-hook")))

		backupData, err := os.ReadFile(hooksPath + ".bak-loopguard")
		expectNoErr(err)
		Expect(string(backupData)).To(Equal(`{"hooks":{"PreToolUse":[]}}`))
	})

	It("writes a Copilot preToolUse config invoking the copilot-hook adapter", func() {
		isolateHome()
		project := GinkgoT().TempDir()
		expectNoErr(os.MkdirAll(filepath.Join(project, ".github"), 0o755))
		chdirTo(project)

		code, out, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("wired copilot-cli"))

		path := filepath.Join(project, ".github", "hooks", "loop-guard-hooks.json")
		data, err := os.ReadFile(path)
		expectNoErr(err)
		var cfg map[string]any
		expectNoErr(json.Unmarshal(data, &cfg))
		Expect(cfg["version"]).To(Equal(float64(1)))
		hooks := cfg["hooks"].(map[string]any)["preToolUse"].([]any)
		Expect(hooks).To(HaveLen(1))
		entry := hooks[0].(map[string]any)
		Expect(entry["bash"]).To(Equal(quotedBin("copilot-hook")))
	})

	It("installs the Pi extension with the binary path embedded", func() {
		home := isolateHome()
		expectNoErr(os.MkdirAll(filepath.Join(home, ".pi"), 0o755))

		code, out, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("wired pi"))

		path := filepath.Join(home, ".pi", "agent", "extensions", "loop-guard.ts")
		data, err := os.ReadFile(path)
		expectNoErr(err)
		s := string(data)
		Expect(s).To(ContainSubstring(`pi.on("tool_call"`))
		bin, err := json.Marshal(mustExe())
		expectNoErr(err)
		Expect(s).To(ContainSubstring(string(bin)))
		Expect(s).NotTo(ContainSubstring("__LOOPGUARD_BIN__"))
	})
})

var _ = Describe("doctor --fix (vscode)", func() {
	It("writes the flat VS Code hook into .github/hooks and is idempotent", func() {
		isolateHome()
		project := GinkgoT().TempDir()
		expectNoErr(os.MkdirAll(filepath.Join(project, ".vscode"), 0o755))
		chdirTo(project)

		code, out, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("wired vscode (project)"))

		path := filepath.Join(project, ".github", "hooks", "loop-guard.json")
		data, err := os.ReadFile(path)
		expectNoErr(err)
		var cfg map[string]any
		expectNoErr(json.Unmarshal(data, &cfg))
		pre := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)
		Expect(pre).To(HaveLen(1))
		entry := pre[0].(map[string]any)
		Expect(entry["type"]).To(Equal("command"))
		Expect(entry["command"]).To(Equal(quotedBin("vscode-hook")))
		// No Copilot-format file: VS Code would run both and double-record.
		_, err = os.Stat(filepath.Join(project, ".github", "hooks", "loop-guard-hooks.json"))
		Expect(os.IsNotExist(err)).To(BeTrue())

		code, out, _ = runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).NotTo(ContainSubstring("wired vscode"))
		// .github/hooks now exists, so the Copilot CLI wiring is detected
		// but must decline the occupied slot.
		Expect(out).To(ContainSubstring("skipped copilot-cli (project)"))
	})

	It("does not add a second file when the Copilot CLI hook already exists", func() {
		isolateHome()
		project := GinkgoT().TempDir()
		expectNoErr(os.MkdirAll(filepath.Join(project, ".vscode"), 0o755))
		expectNoErr(os.MkdirAll(filepath.Join(project, ".github", "hooks"), 0o755))
		copilotPath := filepath.Join(project, ".github", "hooks", "loop-guard-hooks.json")
		expectNoErr(os.WriteFile(copilotPath, []byte(
			`{"version":1,"hooks":{"preToolUse":[{"type":"command","bash":"x copilot-hook"}]}}`), 0o600))
		chdirTo(project)

		code, out, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).NotTo(ContainSubstring("wired vscode"))
		Expect(out).To(ContainSubstring("copilot format"))

		_, err := os.Stat(filepath.Join(project, ".github", "hooks", "loop-guard.json"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
})

var _ = Describe("desktop apps", func() {
	It("treats the Claude Desktop app as claude-code: same settings.json, wired by --fix", func() {
		home := isolateHome()
		// Desktop app installed, but ~/.claude does not exist yet.
		expectNoErr(os.MkdirAll(claudeDesktopDir(home), 0o755))

		code, out, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("wired claude-code"))

		// The standard user-settings hook is what the Desktop app reads.
		data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
		expectNoErr(err)
		var settings map[string]any
		expectNoErr(json.Unmarshal(data, &settings))
		Expect(settings["hooks"]).NotTo(BeNil())
	})

	It("treats the ChatGPT Desktop app as codex: same ~/.codex config, wired by --fix", func() {
		home := isolateHome()
		// Desktop app installed, but ~/.codex does not exist yet.
		expectNoErr(os.MkdirAll(chatGPTDesktopDir(home), 0o755))

		code, out, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("wired codex"))
		Expect(out).To(ContainSubstring("/hooks"))

		data, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
		expectNoErr(err)
		Expect(string(data)).To(ContainSubstring("claude-hook"))
	})
})

func mustExe() string {
	exe, err := os.Executable()
	expectNoErr(err)
	return exe
}

var _ = Describe("ResolveLoopGuardBin", func() {
	It("prefers the LOOPGUARD_BINARY override", func() {
		DeferCleanup(os.Unsetenv, "LOOPGUARD_BINARY")
		override := filepath.FromSlash("/custom/place/loop-guard")
		Expect(os.Setenv("LOOPGUARD_BINARY", override)).To(Succeed())
		p := cli.ResolveLoopGuardBin()
		Expect(p).To(Equal(override))
	})

	It("falls back to the running executable (authoritative install location)", func() {
		os.Unsetenv("LOOPGUARD_BINARY")
		exe, err := os.Executable()
		expectNoErr(err)
		Expect(cli.ResolveLoopGuardBin()).To(Equal(exe))
	})
})

var _ = Describe("doctor", func() {
	It("reports nothing detected in a clean environment", func() {
		isolateHome()
		code, out, _ := runCLI("doctor", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("not detected"))
		Expect(out).To(ContainSubstring("state dir:"))
	})

	It("--fix wires opencode (project) by writing the plugin", func() {
		home := isolateHome()
		project := filepath.Join(home, "proj")
		expectNoErr(os.MkdirAll(filepath.Join(project, ".opencode"), 0o755))
		chdirTo(project)

		code, out, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("wired opencode (project)"))

		data, err := os.ReadFile(filepath.Join(project, ".opencode", "plugins", "loop-guard.js"))
		expectNoErr(err)
		Expect(string(data)).To(ContainSubstring("tool.execute.before"))
		// The plugin embeds the absolute binary path as a JS string literal.
		bin, err := json.Marshal(mustExe())
		expectNoErr(err)
		Expect(string(data)).To(ContainSubstring(string(bin)))
		Expect(string(data)).NotTo(ContainSubstring("__LOOPGUARD_BIN__"))
	})

	It("--fix merges the claude settings.json hook with backup and is idempotent", func() {
		home := isolateHome()
		settingsDir := filepath.Join(home, ".claude")
		expectNoErr(os.MkdirAll(settingsDir, 0o755))
		settingsPath := filepath.Join(settingsDir, "settings.json")
		expectNoErr(os.WriteFile(settingsPath, []byte(`{"model":"opus"}`), 0o600))

		code, _, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))

		data, err := os.ReadFile(settingsPath)
		expectNoErr(err)
		var settings map[string]any
		expectNoErr(json.Unmarshal(data, &settings))
		Expect(settings["model"]).To(Equal("opus")) // preserved
		pre := settings["hooks"].(map[string]any)["PreToolUse"].([]any)
		Expect(pre).To(HaveLen(1))

		// The hook command embeds the absolute binary path, shell-quoted.
		entry := pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		cmd := entry["command"].(string)
		Expect(cmd).To(Equal(quotedBin("claude-hook")))

		backupData, err := os.ReadFile(settingsPath + ".bak-loopguard")
		expectNoErr(err)
		Expect(string(backupData)).To(Equal(`{"model":"opus"}`))

		code, _, _ = runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		data, _ = os.ReadFile(settingsPath)
		expectNoErr(json.Unmarshal(data, &settings))
		pre = settings["hooks"].(map[string]any)["PreToolUse"].([]any)
		Expect(pre).To(HaveLen(1)) // still one entry: idempotent
	})

	It("refuses to merge into an unexpected hooks shape instead of clobbering it", func() {
		home := isolateHome()
		settingsDir := filepath.Join(home, ".claude")
		expectNoErr(os.MkdirAll(settingsDir, 0o755))
		settingsPath := filepath.Join(settingsDir, "settings.json")
		original := `{"hooks":[]}`
		expectNoErr(os.WriteFile(settingsPath, []byte(original), 0o600))

		code, _, stderr := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(stderr).To(ContainSubstring("unexpected JSON type"))

		data, err := os.ReadFile(settingsPath)
		expectNoErr(err)
		Expect(string(data)).To(Equal(original)) // untouched
	})
})

var _ = Describe("init", func() {
	It("prints custom-harness instructions including the contract", func() {
		var out bytes.Buffer
		code := cli.Run([]string{"init", "--harness", "custom"}, strings.NewReader(""), &out, &bytes.Buffer{})
		Expect(code).To(Equal(0))
		s := out.String()
		Expect(s).To(ContainSubstring("integration contract"))
		Expect(s).To(ContainSubstring("loop-guard record --session"))
		Expect(s).To(ContainSubstring("block the call"))
	})

	It("prints harness-specific instructions", func() {
		cases := []struct{ harness, want string }{
			{"claude", "PreToolUse"},
			{"opencode", "tool.execute.before"},
			{"codex", ".codex/hooks.json"},
			{"copilot", "preToolUse"},
			{"pi", "tool_call"},
			{"vscode", ".github/hooks/loop-guard.json"},
		}
		for _, tc := range cases {
			var out bytes.Buffer
			code := cli.Run([]string{"init", "--harness", tc.harness}, strings.NewReader(""), &out, &bytes.Buffer{})
			ExpectWithOffset(1, code).To(Equal(0), tc.harness)
			ExpectWithOffset(1, out.String()).To(ContainSubstring(tc.want), tc.harness)
			ExpectWithOffset(1, out.String()).To(ContainSubstring("loop-guard record --session"), tc.harness)
		}
	})

	It("rejects unknown harness names", func() {
		var errB bytes.Buffer
		code := cli.Run([]string{"init", "--harness", "bogus"}, strings.NewReader(""), &bytes.Buffer{}, &errB)
		Expect(code).To(Equal(1))
		Expect(errB.String()).NotTo(BeEmpty())
	})
})

var _ = Describe("serve", func() {
	It("speaks JSON-RPC over stdio end-to-end", func() {
		cacheDir := GinkgoT().TempDir()
		input := `{"jsonrpc":"2.0","id":"i","method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":"r","method":"tools/call","params":{"name":"loop_guard_record","arguments":{"session":"srv-1","type":"tool","name":"bash","args":{"command":"x"}}}}` + "\n"
		var out bytes.Buffer
		code := cli.Run([]string{"serve", "--cache-dir", cacheDir}, strings.NewReader(input), &out, &bytes.Buffer{})
		Expect(code).To(Equal(0))
		Expect(out.String()).To(ContainSubstring("protocolVersion"))
		// verdict JSON is embedded (escaped) inside content[0].text
		Expect(out.String()).To(ContainSubstring(`\"action\":\"allow\"`))
	})
})
