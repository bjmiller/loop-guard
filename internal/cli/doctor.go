package cli

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

var openCodePluginRelPath = filepath.Join("plugin", "loop-guard.js")

//go:embed assets/opencode-plugin.js
var pluginFS embed.FS

func openCodePluginSource() []byte {
	data, err := pluginFS.ReadFile("assets/opencode-plugin.js")
	if err != nil {
		return nil
	}
	return data
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func containsLoopGuard(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return len(locate(data, []byte("loop-guard"))) > 0 || indexOf(data, []byte("loop-guard")) >= 0
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

func locate(haystack, needle []byte) []int {
	var out []int
	for i := indexOf(haystack, needle); i >= 0; {
		out = append(out, i)
		break
	}
	return out
}

func detectHarnesses(p Paths) []harnessStatus {
	claudeSettings := claudeSettingsPath(p)
	opGlobal := openCodeGlobalRoot(p)
	opProject := openCodeProjectRoot(p)

	return []harnessStatus{
		{
			Name:     "claude-code",
			Detected: fileExists(claudeSettings) || fileExists(filepath.Join(p.Home, ".claude")),
			Wired:    containsLoopGuard(claudeSettings),
			Detail:   claudeSettings,
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
		switch h.Name {
		case "opencode (project)":
			err = installOpenCodePlugin(openCodeProjectRoot(p))
		case "opencode (global)":
			err = installOpenCodePlugin(openCodeGlobalRoot(p))
		case "claude-code":
			err = wireClaudeHook(claudeSettingsPath(p))
		}
		if err != nil {
			fmt.Fprintf(stderr, "loop-guard doctor: wiring %s: %v\n", h.Name, err)
			continue
		}
		fmt.Fprintf(stdout, "wired %s -> %s\n", h.Name, h.Detail)
		fixed++
	}
	return fixed
}

func installOpenCodePlugin(root string) error {
	target := filepath.Join(root, openCodePluginRelPath)
	if fileExists(target) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, openCodePluginSource(), 0o644)
}

func wireClaudeHook(settingsPath string) error {
	if containsLoopGuard(settingsPath) {
		return nil
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existed := err == nil

	settings := map[string]any{}
	if existed {
		if json.Unmarshal(data, &settings) != nil {
			return fmt.Errorf("%s is not valid JSON; refusing to modify", settingsPath)
		}
		backup := settingsPath + ".bak-loopguard"
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
		"hooks": []any{
			map[string]any{"type": "command", "command": "loop-guard claude-hook"},
		},
	})

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(settingsPath), 0o755); mkErr != nil {
		return mkErr
	}
	return os.WriteFile(settingsPath, out, 0o600)
}
