package sessions

import (
	"encoding/json"
	"fmt"
	"os"
)

// mcpSettingsFile is the minimal interactive-spawn settings payload. The
// encoded JSON is
//
//	{"enableAllProjectMcpServers":true,"skipDangerousModePermissionPrompt":true}
//
// — no "permissions" object, no allow/deny list, no defaultMode. That is the
// whole point: pre-approve the project's MCP servers so claude 2.1.199's startup
// "N new MCP servers found in this project" enablement modal never renders (the
// PTY readiness check otherwise reports "unexpected dialog at startup" and every
// live turn wedges), WITHOUT importing the agent-run deny-default posture
// (permissions.defaultMode:"dontAsk") that would break normal interactive tool
// prompts (#943). It deliberately does NOT reuse internal/agentrun/settings,
// whose struct always stamps that posture and requires a non-empty allowlist.
//
// skipDangerousModePermissionPrompt suppresses the second startup dialog of the
// same wedge class: claude 2.1.199's Bypass Permissions warning (a "WARNING:
// Claude Code running in Bypass Permissions mode" selection dialog offering
// "1. No, exit" / "2. Yes, I accept"), which renders whenever the spawn carries
// --dangerously-skip-permissions on a HOME that has not persisted the
// acceptance (persisted form: ~/.claude/settings.json
// skipDangerousModePermissionPrompt + .claude.json bypassPermissionsModeAccepted).
// tui-driver's novel-dialog readiness gate (#173/#224) correctly refuses to type
// into it, so every live turn parks with "unexpected dialog at startup". The
// field is inert when the spawn does not request bypass mode, and pyry only adds
// the bypass flag when the operator's own daemon config or persisted session
// settings enable YOLO, so writing it unconditionally does not weaken any
// posture the operator did not already choose.
//
// Mirrors the un-gated agent-run compat fix (commit d10ce87).
type mcpSettingsFile struct {
	EnableAllProjectMcpServers        bool `json:"enableAllProjectMcpServers"`
	SkipDangerousModePermissionPrompt bool `json:"skipDangerousModePermissionPrompt"`
}

// writeMCPSettings creates an os.TempDir settings file containing the
// mcpSettingsFile payload above and returns its absolute path. os.TempDir
// (not the pool data dir) keeps the writer self-contained and testable — the
// data dir is empty in test mode.
//
// The file is per-session and must outlive every respawn (backoff restart and
// the #842 live settings-restart both re-exec with the same --settings path), so
// the caller — not this helper — removes it at session teardown.
//
// Error path: if Encode or Close fails after os.CreateTemp succeeds, the
// tempfile is best-effort removed before returning the error, so callers never
// see a leaked path on the error path.
func writeMCPSettings() (string, error) {
	f, err := os.CreateTemp("", "pyry-session-settings-*.json")
	if err != nil {
		return "", fmt.Errorf("sessions: create settings tempfile: %w", err)
	}
	tmpName := f.Name()

	if err := json.NewEncoder(f).Encode(&mcpSettingsFile{
		EnableAllProjectMcpServers:        true,
		SkipDangerousModePermissionPrompt: true,
	}); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: encode settings: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: close settings: %w", err)
	}
	return tmpName, nil
}
