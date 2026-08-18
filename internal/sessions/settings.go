package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// writeMCPSettings writes the mcpSettingsFile payload above for session id and
// returns its absolute path. Two branches, selected by registryPath:
//
//   - registryPath != "" — the file is <dataDir>/session-settings/<id>.json,
//     where dataDir is the absolutised parent of registryPath (the directory
//     holding sessions.json). session-settings is created on demand at 0700,
//     mirroring the archived-sessions subdirectory disposeJSONLLocked creates;
//     cold start needs that, because the data dir itself is created only by
//     saveRegistryLocked, which has not necessarily run when Pool.New writes the
//     bootstrap file. This is the production path (#1518). The file must outlive
//     every respawn — a backoff restart and the #842 live settings-restart both
//     re-exec with the same --settings path baked into spawnBase, and nothing
//     re-reads or re-creates it in between — so it does not belong in an OS temp
//     dir where an age-based reaper can delete it out from under a live session.
//     Deriving the name from the session id rather than from os.CreateTemp's
//     random suffix is what bounds the on-disk set by session count instead of by
//     daemon-restart count, with no startup sweeper.
//   - registryPath == "" — persistence is disabled (the test-only mode Pool.dataDir
//     reports as ""), so there is no data dir to write into and the file goes to
//     os.TempDir with a random name, observably identical to pre-#1518 behaviour.
//     Most of this package's tests build a pool with no RegistryPath.
//
// The derivation is duplicated from Pool.dataDir rather than shared with it
// because Pool.New writes the bootstrap file before the *Pool literal exists —
// which is also why this is a package function and not a method.
//
// id is gated on ValidID on the data-dir branch only. A warm-start bootstrap id
// is decoded straight out of the registry with no shape check anywhere on that
// path, and after #1518 it names a file, so an id carrying a separator or a ".."
// segment would place the write outside the data dir. A malformed id is a hard
// error, matching loadRegistry's "a malformed file is a hard error (operator must
// fix or remove)" posture; silently falling back to a temp file would hide a
// corrupt registry. The persistence-disabled branch performs no path join and is
// deliberately not gated — tests pass hand-made ids there.
//
// The write is atomic (scratch file in the target directory, fsync, rename), the
// same recipe as saveRegistryLocked. Two reasons beyond convention, both
// introduced by the relocation: deterministic naming means two same-id
// materialise callers now write the same path concurrently, and a rename hands a
// live child either the old complete file or the new one rather than a truncated
// prefix; and os.Rename replaces a symlink at the destination instead of writing
// through it. os.CreateTemp creates at 0600 and the rename preserves it, so the
// file is never group- or world-readable — which matters for integrity, not
// confidentiality: the payload is two public booleans, but anyone who could write
// this file could add hooks/permissions keys to a file pyry hands claude as
// --settings, i.e. code execution as the operator.
//
// The caller — not this helper — removes the file at session teardown, and on
// every error return between the write and its own success: in the data dir an
// orphan is permanent, where in os.TempDir it was eventually reaped.
func writeMCPSettings(registryPath string, id SessionID) (string, error) {
	// dir == "" selects os.CreateTemp's own os.TempDir contract and final == ""
	// means "keep the scratch name" — the persistence-disabled branch leaves both
	// empty and the data-dir branch sets both.
	var dir, final string
	if registryPath != "" {
		if !ValidID(string(id)) {
			return "", fmt.Errorf("sessions: settings file for session %q: not a canonical session id", id)
		}
		dataDir, err := filepath.Abs(filepath.Dir(registryPath))
		if err != nil {
			return "", fmt.Errorf("sessions: resolve settings dir: %w", err)
		}
		dir = filepath.Join(dataDir, "session-settings")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("sessions: mkdir settings dir: %w", err)
		}
		final = filepath.Join(dir, string(id)+".json")
	}

	pattern := "pyry-session-settings-*.json"
	if final != "" {
		// Dotted .tmp suffix so a scratch file left by a SIGKILL inside the write
		// window is never mistaken for a session's settings file, and so a *.json
		// glob of the directory counts sessions exactly. Mirrors
		// saveRegistryLocked's ".sessions-*.json.tmp".
		pattern = ".settings-*.json.tmp"
	}
	f, err := os.CreateTemp(dir, pattern)
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
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: fsync settings: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: close settings: %w", err)
	}
	if final == "" {
		return tmpName, nil
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: rename settings: %w", err)
	}
	return final, nil
}
