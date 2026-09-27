package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// codexHomeDirName is the daemon-owned CODEX_HOME under the instance
// directory (#2620). The operator signs in once with CODEX_HOME set to it;
// nothing reads the operator's personal ~/.codex, whose config loads their own
// default model, MCP servers, plugins and notify hook.
const codexHomeDirName = "codex-home"

// codexHomeConfig is the config.toml the daemon writes into its Codex home:
// threads start read-only and every approval is routed to the user, never to
// Codex's reviewer agent. The keys and kebab-case values are v2.Config's in
// the pinned 0.156.1 schema. Until the approvals ticket lands, codexsup's
// default decline answers every request this posture raises.
const codexHomeConfig = `# Written by pyry on every Codex session start; edits are overwritten.
approval_policy = "on-request"
sandbox_mode = "read-only"
approvals_reviewer = "user"

[features]
default_mode_request_user_input = true
`

// codexHomePath is the Codex home for the instance whose directory is
// instanceDir (resolveInstanceDirPath).
func codexHomePath(instanceDir string) string {
	return filepath.Join(instanceDir, codexHomeDirName)
}

// prepareCodexHome creates dir at 0700 (tightening an existing one) and writes
// codexHomeConfig to its config.toml at 0600 through a temp file and a rename,
// so a planted symlink is replaced rather than followed. It touches no other
// file: the operator's sign-in in the same directory is never read or moved.
func prepareCodexHome(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("codex home: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("codex home: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config.toml.*")
	if err != nil {
		return fmt.Errorf("codex home config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(codexHomeConfig); err != nil {
		tmp.Close()
		return fmt.Errorf("codex home config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("codex home config: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, "config.toml")); err != nil {
		return fmt.Errorf("codex home config: %w", err)
	}
	return nil
}
