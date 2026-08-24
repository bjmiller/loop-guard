package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/brian/loop-guard/internal/cli"
)

// doctorIn runs doctor with HOME/Config/WorkDir faked via env overrides that
// DefaultPaths reads. We instead chdir into a temp dir and point XDG_CONFIG_HOME
// and home at it.
type fakeEnv struct {
	dir string
	old map[string]string
}

func isolateHome() string {
	dir := GinkgoT().TempDir()
	old := map[string]string{}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		old[key] = os.Getenv(key)
	}
	expectNoErr(os.Setenv("HOME", dir))
	if isWindows() {
		expectNoErr(os.Setenv("USERPROFILE", dir))
	} else {
		os.Unsetenv("USERPROFILE")
	}
	expectNoErr(os.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config")))
	DeferCleanup(func() {
		for k, v := range old {
			os.Setenv(k, v)
		}
	})
	return dir
}

func isWindows() bool { return strings.EqualFold(os.Getenv("OS"), "windows_nt") }

func expectNoErr(err error) {
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

var _ = Describe("ResolveLoopGuardBin", func() {
	It("prefers the LOOPGUARD_BINARY override", func() {
		DeferCleanup(os.Unsetenv, "LOOPGUARD_BINARY")
		Expect(os.Setenv("LOOPGUARD_BINARY", "/custom/place/loop-guard")).To(Succeed())
		p := cli.ResolveLoopGuardBin()
		Expect(p).To(Equal("/custom/place/loop-guard"))
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
		expectNoErr(os.Chdir(project))
		DeferCleanup(func() {
			expectNoErr(os.Chdir(home))
		})

		code, out, _ := runCLI("doctor", "--fix", "--cache-dir", GinkgoT().TempDir())
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("wired opencode (project)"))

		data, err := os.ReadFile(filepath.Join(project, ".opencode", "plugins", "loop-guard.js"))
		expectNoErr(err)
		Expect(string(data)).To(ContainSubstring("tool.execute.before"))
		// The plugin embeds the absolute binary path, JSON-quoted.
		exe, err := os.Executable()
		expectNoErr(err)
		Expect(string(data)).To(ContainSubstring(strconv.Quote(exe)))
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

		// The hook command embeds the absolute binary path, quoted.
		entry := pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		cmd := entry["command"].(string)
		exe, err := os.Executable()
		expectNoErr(err)
		Expect(cmd).To(Equal(strconv.Quote(exe) + " claude-hook"))

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
		var out bytes.Buffer
		code := cli.Run([]string{"init", "--harness", "claude"}, strings.NewReader(""), &out, &bytes.Buffer{})
		Expect(code).To(Equal(0))
		Expect(out.String()).To(ContainSubstring("PreToolUse"))

		out.Reset()
		code = cli.Run([]string{"init", "--harness", "opencode"}, strings.NewReader(""), &out, &bytes.Buffer{})
		Expect(code).To(Equal(0))
		Expect(out.String()).To(ContainSubstring("tool.execute.before"))
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
