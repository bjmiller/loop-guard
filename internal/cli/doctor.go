package cli

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/brian/loop-guard/internal/state"
)

// Paths abstracts filesystem locations so doctor/init are testable without
// touching a real HOME directory.
type Paths struct {
	Home    string // e.g. /Users/brian or C:\Users\brian
	Config  string // XDG-style config root, e.g. ~/.config
	WorkDir string // current working directory
}

// DefaultPaths resolves platform locations using os.UserHomeDir and
// XDG_CONFIG_HOME (honored on all platforms for consistency).
func DefaultPaths() Paths {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	wd, err := os.Getwd()
	if err != nil || wd == "" {
		wd = "."
	}
	return Paths{Home: home, Config: config, WorkDir: wd}
}

type harnessStatus struct {
	Name     string
	Detected bool
	Wired    bool
	Detail   string
}

func claudeSettingsPath(p Paths) string {
	return filepath.Join(p.Home, ".claude", "settings.json")
}

func openCodeGlobalRoot(p Paths) string {
	return filepath.Join(p.Config, "opencode")
}

func openCodeProjectRoot(p Paths) string {
	return filepath.Join(p.WorkDir, ".opencode")
}

var openCodePluginRelPath = filepath.Join("plugins", "loop-guard.js")

// Codex: hooks.json at user (~/.codex) or project (.codex) level.
func codexUserRoot(p Paths) string { return filepath.Join(p.Home, ".codex") }

func codexProjectRoot(p Paths) string { return filepath.Join(p.WorkDir, ".codex") }

// Copilot CLI: hook configs in .github/hooks (repo) or ~/.copilot/hooks
// ($COPILOT_HOME/hooks) at user level.
func copilotProjectHooksDir(p Paths) string {
	return filepath.Join(p.WorkDir, ".github", "hooks")
}

func copilotUserHooksDir(p Paths) string {
	if ch := os.Getenv("COPILOT_HOME"); ch != "" {
		return filepath.Join(ch, "hooks")
	}
	return filepath.Join(p.Home, ".copilot", "hooks")
}

// Pi: extensions in .pi/extensions (project) or ~/.pi/agent/extensions
// (global).
func piProjectExtDir(p Paths) string { return filepath.Join(p.WorkDir, ".pi", "extensions") }

func piGlobalExtDir(p Paths) string { return filepath.Join(p.Home, ".pi", "agent", "extensions") }

// claudeDesktopDir is the config directory of the Claude Desktop /
// Claude Code Desktop app. The Desktop app fires the same hook events as the
// CLI and reads ~/.claude/settings.json, so the standard claude-code wiring
// covers it; this directory is only used as an extra detection signal for
// machines where ~/.claude does not exist yet.
func claudeDesktopDir(p Paths) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(p.Home, "Library", "Application Support", "Claude")
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Claude")
		}
	default:
		if config := os.Getenv("XDG_CONFIG_HOME"); config != "" {
			return filepath.Join(config, "Claude")
		}
	}
	return filepath.Join(p.Home, ".config", "Claude")
}

// chatGPTDesktopDir is the config directory of the ChatGPT desktop app,
// which runs Codex sessions against the same ~/.codex config layers as the
// CLI. It is used only as an extra detection signal for machines where
// ~/.codex does not exist yet.
func chatGPTDesktopDir(p Paths) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(p.Home, "Library", "Application Support", "com.openai.chat")
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "ChatGPT")
		}
	default:
		if config := os.Getenv("XDG_CONFIG_HOME"); config != "" {
			return filepath.Join(config, "ChatGPT")
		}
	}
	return filepath.Join(p.Home, ".config", "ChatGPT")
}

var (
	piExtensionFileName = "loop-guard.ts"
	copilotHookJSONName = "loop-guard-hooks.json"
	codexHooksJSONName  = "hooks.json"
)

//go:embed assets/opencode-plugin.js
var pluginFS embed.FS

//go:embed assets/pi-extension.ts
var piExtensionFS embed.FS

func openCodePluginSource() []byte {
	data, err := pluginFS.ReadFile("assets/opencode-plugin.js")
	if err != nil {
		return nil
	}
	return data
}

func piExtensionSource(binPath string) ([]byte, error) {
	data, err := piExtensionFS.ReadFile("assets/pi-extension.ts")
	if err != nil {
		return nil, err
	}
	bin, _ := json.Marshal(binPath)
	// The quoted placeholder in the TS source is replaced wholesale with a
	// JSON-quoted string literal.
	return []byte(strings.ReplaceAll(string(data), `"__LOOPGUARD_BIN__"`, string(bin))), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// claudeWired reports whether settingsPath already carries a loop-guard hook.
// It checks the hook structure for our adapter signature ("claude-hook")
// rather than the binary name, which may be renamed or installed anywhere.
// Hand-written configs invoking the binary by name are still recognized.
func claudeWired(settingsPath string) bool {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}
	var settings struct {
		Hooks struct {
			PreToolUse []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &settings) == nil {
		for _, entry := range settings.Hooks.PreToolUse {
			for _, h := range entry.Hooks {
				if strings.Contains(h.Command, "claude-hook") ||
					strings.Contains(h.Command, "loop-guard") {
					return true
				}
			}
		}
		return false
	}
	// Not valid JSON: fall back to substring scan.
	return indexOf(data, []byte("loop-guard")) >= 0
}

// codexWired reports whether a Codex hooks.json already carries a loop-guard
// entry. Codex reuses Claude Code's hook schema, so the same signature check
// applies.
func codexWired(hooksPath string) bool {
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		return false
	}
	return indexOf(data, []byte("loop-guard")) >= 0 ||
		indexOf(data, []byte("claude-hook")) >= 0
}

// copilotWired reports whether a Copilot CLI hooks config already references
// loop-guard.
func copilotWired(hooksPath string) bool {
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		return false
	}
	return indexOf(data, []byte("loop-guard")) >= 0 ||
		indexOf(data, []byte("copilot-hook")) >= 0
}

func detectHarnesses(p Paths) []harnessStatus {
	claudeSettings := claudeSettingsPath(p)
	opGlobal := openCodeGlobalRoot(p)
	opProject := openCodeProjectRoot(p)
	codexUser := codexUserRoot(p)
	codexProject := codexProjectRoot(p)
	piGlobal := piGlobalExtDir(p)

	copilotProjectJSON := filepath.Join(copilotProjectHooksDir(p), copilotHookJSONName)
	copilotUserJSON := filepath.Join(copilotUserHooksDir(p), copilotHookJSONName)

	return []harnessStatus{
		{
			Name: "claude-code",
			// The Claude Code Desktop app fires the same hooks from the same
			// ~/.claude/settings.json, so its config dir counts as detection.
			Detected: fileExists(claudeSettings) ||
				fileExists(filepath.Join(p.Home, ".claude")) ||
				fileExists(claudeDesktopDir(p)),
			Wired:  claudeWired(claudeSettings),
			Detail: claudeSettings,
		},
		{
			Name: "codex (user)",
			// The ChatGPT desktop app runs Codex sessions from the same
			// ~/.codex config, so its config dir counts as detection.
			Detected: fileExists(codexUser) || fileExists(chatGPTDesktopDir(p)),
			Wired:    codexWired(filepath.Join(codexUser, codexHooksJSONName)),
			Detail:   filepath.Join(codexUser, codexHooksJSONName),
		},
		{
			Name:     "codex (project)",
			Detected: fileExists(codexProject),
			Wired:    codexWired(filepath.Join(codexProject, codexHooksJSONName)),
			Detail:   filepath.Join(codexProject, codexHooksJSONName),
		},
		{
			Name:     "copilot-cli (project)",
			Detected: fileExists(filepath.Join(p.WorkDir, ".github")),
			Wired:    copilotWired(copilotProjectJSON),
			Detail:   copilotProjectJSON,
		},
		{
			Name:     "copilot-cli (user)",
			Detected: fileExists(filepath.Dir(filepath.Dir(copilotUserJSON))),
			Wired:    copilotWired(copilotUserJSON),
			Detail:   copilotUserJSON,
		},
		{
			Name:     "pi (project)",
			Detected: fileExists(filepath.Join(p.WorkDir, ".pi")),
			Wired:    fileExists(filepath.Join(piProjectExtDir(p), piExtensionFileName)),
			Detail:   filepath.Join(piProjectExtDir(p), piExtensionFileName),
		},
		{
			Name:     "pi (global)",
			Detected: fileExists(filepath.Join(p.Home, ".pi")),
			Wired:    fileExists(filepath.Join(piGlobal, piExtensionFileName)),
			Detail:   filepath.Join(piGlobal, piExtensionFileName),
		},
		{
			Name:     "opencode (global)",
			Detected: fileExists(opGlobal),
			Wired:    fileExists(filepath.Join(opGlobal, openCodePluginRelPath)),
			Detail:   filepath.Join(opGlobal, openCodePluginRelPath),
		},
		{
			Name:     "opencode (project)",
			Detected: fileExists(opProject),
			Wired:    fileExists(filepath.Join(opProject, openCodePluginRelPath)),
			Detail:   filepath.Join(opProject, openCodePluginRelPath),
		},
		{
			Name:     "any-MCP-harness",
			Detected: false,
			Wired:    false,
			Detail:   `register MCP server: loop-guard serve`,
		},
	}
}

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fix := fs.Bool("fix", false, "install adapters into detected harnesses")
	cacheDir := fs.String("cache-dir", "", "state directory override")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "loop-guard doctor: %v\n", err)
		return exitErr
	}
	p := DefaultPaths()

	dir := *cacheDir
	if dir == "" {
		d, err := stateDefaultDir()
		if err != nil {
			fmt.Fprintf(stderr, "loop-guard doctor: %v\n", err)
			return exitErr
		}
		dir = d
	}
	stateOK := checkWritable(dir)

	statuses := detectHarnesses(p)
	fmt.Fprintln(stdout, "loop-guard doctor")
	fmt.Fprintln(stdout, "=================")
	fmt.Fprintf(stdout, "state dir: %s [%s]\n", dir, writableLabel(stateOK))
	for _, h := range statuses {
		stateStr := "not detected"
		switch {
		case h.Detected && h.Wired:
			stateStr = "wired"
		case h.Detected:
			stateStr = "detected, NOT wired"
		case h.Name == "any-MCP-harness":
			stateStr = "universal fallback available"
		}
		fmt.Fprintf(stdout, "%-22s %-30s (%s)\n", h.Name, stateStr, h.Detail)
	}

	if *fix {
		fixed := applyFixes(p, statuses, stdout, stderr)
		if fixed > 0 {
			fmt.Fprintf(stdout, "\nwired %d harness(es). Run 'loop-guard doctor' to verify.\n", fixed)
		} else {
			fmt.Fprintln(stdout, "\nnothing to wire (see 'loop-guard init --harness custom' for other harnesses).")
		}
	} else {
		fmt.Fprintln(stdout, "\nrun 'loop-guard doctor --fix' to auto-wire detected harnesses.")
	}
	return exitOK
}

func writableLabel(ok bool) string {
	if ok {
		return "writable"
	}
	return "NOT writable"
}

func stateDefaultDir() (string, error) { return state.DefaultDir() }

// ResolveLoopGuardBin determines the absolute path of the loop-guard binary
// to embed in generated harness configs. Order:
//  1. LOOPGUARD_BINARY env override
//  2. this process's own executable (doctor runs AS the installed binary,
//     so its location is authoritative)
//  3. well-known install locations
//  4. bare name (PATH fallback)
func ResolveLoopGuardBin() string {
	bin := "loop-guard"
	if runtime.GOOS == "windows" {
		bin = "loop-guard.exe"
	}

	if v := os.Getenv("LOOPGUARD_BINARY"); v != "" {
		return filepath.Clean(v)
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", bin),
		filepath.Join(home, "go", "bin", bin),
		filepath.Join(home, ".bin", bin),
		"/usr/local/bin/" + bin,
		"/opt/homebrew/bin/" + bin,
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		candidates = append(candidates,
			filepath.Join(localAppData, "Programs", "loop-guard", bin))
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return bin
}

func checkWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return false
	}
	os.Remove(probe)
	return true
}

func applyFixes(p Paths, statuses []harnessStatus, stdout, stderr io.Writer) int {
	fixed := 0
	for _, h := range statuses {
		if !h.Detected || h.Wired {
			continue
		}
		var err error
		var skipped string
		switch h.Name {
		case "opencode (project)":
			err = installOpenCodePlugin(openCodeProjectRoot(p))
		case "opencode (global)":
			err = installOpenCodePlugin(openCodeGlobalRoot(p))
		case "claude-code":
			err = wireClaudeHook(claudeSettingsPath(p))
		case "codex (user)":
			err = wireCodexHook(filepath.Join(codexUserRoot(p), codexHooksJSONName))
		case "codex (project)":
			err = wireCodexHook(filepath.Join(codexProjectRoot(p), codexHooksJSONName))
		case "copilot-cli (project)":
			err = wireCopilotHook(copilotProjectHooksDir(p))
		case "copilot-cli (user)":
			err = wireCopilotHook(copilotUserHooksDir(p))
		case "pi (project)":
			err = installPiExtension(piProjectExtDir(p))
		case "pi (global)":
			err = installPiExtension(piGlobalExtDir(p))
		default:
			// Detected but not automatable (desktop apps, etc.): report and
			// move on rather than falling through with a nil error.
			skipped = h.Detail
		}
		if skipped != "" {
			fmt.Fprintf(stdout, "skipped %s -> %s\n", h.Name, h.Detail)
			continue
		}
		if err != nil {
			fmt.Fprintf(stderr, "loop-guard doctor: wiring %s: %v\n", h.Name, err)
			continue
		}
		fmt.Fprintf(stdout, "wired %s -> %s\n", h.Name, h.Detail)
		if strings.HasPrefix(h.Name, "codex") {
			fmt.Fprintln(stdout, "  note: run '/hooks' inside Codex to trust the new hook before it will fire.")
		}
		fixed++
	}
	return fixed
}

func installOpenCodePlugin(root string) error {
	target := filepath.Join(root, openCodePluginRelPath)
	if fileExists(target) {
		return nil
	}
	// Embed the resolved absolute path so the plugin never depends on PATH.
	// The quoted placeholder in the JS source is replaced wholesale with a
	// JSON-quoted string literal.
	bin, _ := json.Marshal(ResolveLoopGuardBin())
	source := strings.ReplaceAll(string(openCodePluginSource()),
		`"__LOOPGUARD_BIN__"`, string(bin))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(source), 0o644)
}

// claudeCommand is the shell command embedded in Claude Code hook configs.
// It uses the resolved absolute path so PATH is irrelevant; the path is
// quoted for safety on all platforms.
func claudeCommand() string {
	return strconv.Quote(ResolveLoopGuardBin()) + " claude-hook"
}

func codexCommand() string {
	return strconv.Quote(ResolveLoopGuardBin()) + " claude-hook"
}

func copilotCommand() string {
	return strconv.Quote(ResolveLoopGuardBin()) + " copilot-hook"
}

func wireClaudeHook(settingsPath string) error {
	return wireClaudeStyleHooks(settingsPath, claudeWired, map[string]any{
		"type": "command", "command": claudeCommand(),
	})
}

// wireCodexHook merges a PreToolUse entry into a Codex hooks.json. Codex
// reuses Claude Code's hook schema, so the merge logic is shared.
func wireCodexHook(hooksPath string) error {
	return wireClaudeStyleHooks(hooksPath, codexWired, map[string]any{
		"type": "command", "command": codexCommand(), "statusMessage": "Loop-Guard check",
	})
}

// wireClaudeStyleHooks appends an entry under hooks.PreToolUse in a JSON file
// with Claude-style hook structure (Claude Code settings.json, Codex
// hooks.json), backing up any existing file first.
func wireClaudeStyleHooks(path string, alreadyWired func(string) bool, hook map[string]any) error {
	if alreadyWired(path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existed := err == nil

	settings := map[string]any{}
	if existed {
		if json.Unmarshal(data, &settings) != nil {
			return fmt.Errorf("%s is not valid JSON; refusing to modify", path)
		}
		backup := path + ".bak-loopguard"
		if backupErr := os.WriteFile(backup, data, 0o600); backupErr != nil {
			return fmt.Errorf("backup %s: %w", backup, backupErr)
		}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	pre, _ := hooks["PreToolUse"].([]any)
	hooks["PreToolUse"] = append(pre, map[string]any{
		"matcher": "*",
		"hooks":   []any{hook},
	})

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return mkErr
	}
	return os.WriteFile(path, out, 0o600)
}

// wireCopilotHook registers loop-guard's preToolUse hook in a Copilot CLI
// hooks config file (flat event arrays, "version": 1).
func wireCopilotHook(hooksDir string) error {
	path := filepath.Join(hooksDir, copilotHookJSONName)
	if copilotWired(path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existed := err == nil

	config := map[string]any{}
	if existed {
		if json.Unmarshal(data, &config) != nil {
			return fmt.Errorf("%s is not valid JSON; refusing to modify", path)
		}
		backup := path + ".bak-loopguard"
		if backupErr := os.WriteFile(backup, data, 0o600); backupErr != nil {
			return fmt.Errorf("backup %s: %w", backup, backupErr)
		}
	}
	if _, ok := config["version"]; !ok {
		config["version"] = 1
	}
	hooks, _ := config["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		config["hooks"] = hooks
	}
	pre, _ := hooks["preToolUse"].([]any)
	hooks["preToolUse"] = append(pre, map[string]any{
		"type": "command",
		"bash": copilotCommand(),
	})

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(hooksDir, 0o755); mkErr != nil {
		return mkErr
	}
	return os.WriteFile(path, out, 0o600)
}

// installPiExtension writes the embedded Pi extension with the resolved
// binary path embedded, so PATH is irrelevant.
func installPiExtension(extDir string) error {
	target := filepath.Join(extDir, piExtensionFileName)
	if fileExists(target) {
		return nil
	}
	source, err := piExtensionSource(ResolveLoopGuardBin())
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(extDir, 0o755); mkErr != nil {
		return mkErr
	}
	return os.WriteFile(target, source, 0o644)
}
