package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// approveToolRef is the full MCP tool reference claude's --permission-prompt-tool
// is pointed at. It is derived from the merged mcp-approve subcommand's constants
// (mcpServerName / approveToolName, cmd/pyry/mcp_approve.go) so the reference
// cannot drift from the server/tool that `pyry mcp-approve` actually advertises —
// renaming a constant there updates this reference in lockstep.
var approveToolRef = fmt.Sprintf("mcp__%s__%s", mcpServerName, approveToolName)

// permissionArgs returns the claude permission-mode flags for a stream spawn.
// It is the enforce-vs-skip switch for the whole permission bridge; the two
// branches are exhaustive and exactly what the tests pin:
//
//   - yolo == true  → exactly ["--dangerously-skip-permissions"]. claude's
//     permission path is disabled: no prompt tool, no mcp-config.
//   - yolo == false → the --permission-prompt-tool + --mcp-config pair that
//     routes every non-allowlisted tool use through the daemon approval
//     registry, plus:
//     --strict-mcp-config — claude loads ONLY the supplied document's servers,
//     ignoring any project/user .mcp.json that could add or shadow a fake
//     pyry_approve server (non-negotiable; its omission silently defeats
//     enforcement). It is also what makes the document the sole route by which
//     pyry_files can reach a spawn (#2169); and
//     --permission-mode default — the mode where the prompt tool is consulted
//     (not bypassPermissions/acceptEdits/plan). No --dangerously-skip-permissions.
//
// In the non-YOLO branch mcpConfigPath must be the non-empty path of a config
// written by writeMCPServersConfig; the composed live flow (write → check err →
// permissionArgs) guarantees that by construction.
func permissionArgs(yolo bool, mcpConfigPath string) []string {
	if yolo {
		return []string{"--dangerously-skip-permissions"}
	}
	return []string{
		"--permission-prompt-tool", approveToolRef,
		"--mcp-config", mcpConfigPath,
		"--strict-mcp-config",
		"--permission-mode", "default",
	}
}

// mcpServersConfig is the --mcp-config document claude reads: the top-level
// {"mcpServers":{...}} shape claude's --mcp-config expects, carrying the
// pyry_approve and pyry_files stdio server registrations.
//
// It is daemon-global — written once at startup, byte-identical for every
// session, removed at shutdown — which is precisely why no entry's argv can name
// a session. A per-spawn identity travels on the child's ENVIRONMENT instead; see
// mapStreamsupConfig's Env field and envSessionID's doc.
type mcpServersConfig struct {
	MCPServers map[string]mcpServerSpec `json:"mcpServers"`
}

// mcpServerSpec is one stdio MCP server entry: the command to exec and its argv
// (argv[0] excluded — command is argv[0]).
type mcpServerSpec struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// renderMCPServersConfig builds the mcp-config bytes registering the two stdio
// servers claude is meant to see: pyry_approve, the permission bridge (#1106),
// and pyry_files, the send_file tool (#2169). Each server command forks the
// running pyry binary as `pyry <subcommand> -pyry-socket <socketPath>`, so both
// forwarders connect back to the daemon that spawned claude rather than a
// default-named instance.
//
// Fail-closed: an empty socketPath or pyryBin returns an error and nil bytes.
// This function NEVER emits a bare ["mcp-approve"] / ["mcp-files"] (either would
// resolve to the default-named instance's socket = wrong daemon = silent
// enforcement failure, the #1106 AC-4 hazard) nor a config naming an empty
// command. It is the deterministic net behind the caller-supplies-the-right-socket
// contract, not a mere convention the caller is trusted to honour.
//
// pyry_files is a second ENTRY here rather than a second document, and that is
// what extends both guards to it for free: there is no arrangement in which one
// server is emitted with a bare argv while the other is not, because a refusal
// returns nil bytes and no document at all.
//
// The server keys are the subcommands' own constants (mcpServerName from
// mcp_approve.go, mcpFilesServerName from mcp_files.go), never re-spelled here,
// for approveToolRef's reason: a rename there moves the registration with it
// instead of leaving a document advertising a name nothing serves.
func renderMCPServersConfig(pyryBin, socketPath string) ([]byte, error) {
	if pyryBin == "" {
		return nil, errors.New("mcp-approve config: empty pyry binary path")
	}
	if socketPath == "" {
		return nil, errors.New("mcp-approve config: empty control socket path")
	}
	cfg := mcpServersConfig{
		MCPServers: map[string]mcpServerSpec{
			mcpServerName: {
				Command: pyryBin,
				Args:    []string{"mcp-approve", "-pyry-socket", socketPath},
			},
			// The same client-flag pair mcp-approve takes, and no positionals —
			// runMCPFiles' stated contract, written against here.
			mcpFilesServerName: {
				Command: pyryBin,
				Args:    []string{"mcp-files", "-pyry-socket", socketPath},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		// Unreachable: a fixed-shape struct of string fields always marshals.
		// Propagate for hygiene rather than assert.
		return nil, fmt.Errorf("mcp-approve config: marshal: %w", err)
	}
	return b, nil
}

// writeMCPServersConfig renders the mcp-config and writes it to a per-spawn tmp
// file (os.CreateTemp "pyry-mcp-approve-*.json", mode 0600), returning the path.
// Mirrors internal/agentrun/settings.writeSettings: on any post-create failure the
// tmp file is removed before the error is returned, so callers never see a leaked
// path on the error path.
//
// The tmp-file prefix and the "mcp-approve config:" error prefix keep their
// original spelling even though the document now carries two servers. The prefix
// is an observable in a SHARED $TMPDIR that #1240's recorded runner attribution
// reasons about, and the error prefix was chosen for grep-ability; renaming either
// changes a value outside this file's reach for no gain here.
//
// The caller owns removal on the success path (defer os.Remove(path)) at the
// live-spawn site; this helper registers no cleanup itself. The written bytes
// are byte-identical to renderMCPServersConfig's output (no trailing newline).
func writeMCPServersConfig(pyryBin, socketPath string) (string, error) {
	b, err := renderMCPServersConfig(pyryBin, socketPath)
	if err != nil {
		return "", err
	}

	f, err := os.CreateTemp("", "pyry-mcp-approve-*.json")
	if err != nil {
		return "", fmt.Errorf("mcp-approve config: create temp: %w", err)
	}
	tmpName := f.Name()

	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("mcp-approve config: write: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("mcp-approve config: close: %w", err)
	}
	return tmpName, nil
}
