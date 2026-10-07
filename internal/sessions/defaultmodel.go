package sessions

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// maxClaudeSettingsFile bounds one settings file read by ClaudeSettingsModel. A
// settings file is a few kilobytes; one past this is skipped rather than read.
const maxClaudeSettingsFile = 1 << 20

// ClaudeSettingsModel answers the model claude would start on in workDir when its
// argv names none, which is Config.DefaultModel's contract. The first source that
// names a model wins, in Claude Code's documented precedence:
//
//  1. ANTHROPIC_MODEL in the environment, which every child inherits.
//  2. "model" in <workDir>/.claude/settings.local.json.
//  3. "model" in <workDir>/.claude/settings.json.
//  4. "model" in the user's settings.json, under $CLAUDE_CONFIG_DIR when set and
//     ~/.claude otherwise.
//
// "" when none names one. An empty workDir reads the project files relative to the
// daemon's own directory, which is where such a child starts.
//
// Two sources are left out on purpose: managed settings, which no pyry host uses,
// and an ANTHROPIC_MODEL set through a settings file's env block. Missing either
// can only leave a pinned default unresolved, which is the behaviour before this
// existed, never a wrong model. A file that is absent, unreadable, oversized or not
// JSON is skipped.
//
// The answer is never placed on an argv as it is. composeSpawnArgs names only its
// modelfamily.Alias rewrite, whose output is a bare family of letters plus at most
// one bounded variant group, so nothing in these files can reach claude as a flag.
func ClaudeSettingsModel(workDir string) string {
	userDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if userDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			userDir = filepath.Join(home, ".claude")
		}
	}
	return claudeSettingsModel(os.Getenv, userDir, workDir)
}

// claudeSettingsModel is ClaudeSettingsModel with its environment and user
// settings directory passed in, so a test never reads the host's own. An empty
// userDir skips the user file.
func claudeSettingsModel(getenv func(string) string, userDir, workDir string) string {
	if model := getenv("ANTHROPIC_MODEL"); model != "" {
		return model
	}
	files := []string{
		filepath.Join(workDir, ".claude", "settings.local.json"),
		filepath.Join(workDir, ".claude", "settings.json"),
	}
	if userDir != "" {
		files = append(files, filepath.Join(userDir, "settings.json"))
	}
	for _, path := range files {
		if model := settingsFileModel(path); model != "" {
			return model
		}
	}
	return ""
}

// settingsFileModel returns the "model" key of one Claude Code settings file, or
// "" when the file names none or cannot be read.
func settingsFileModel(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, maxClaudeSettingsFile+1))
	if err != nil || len(body) > maxClaudeSettingsFile {
		return ""
	}
	var settings struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		return ""
	}
	return settings.Model
}
