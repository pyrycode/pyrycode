package main

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// TestPermissionArgs_YOLO pins the skip-permissions branch: exactly the
// dangerous flag, and none of the enforcement flags. (AC-2.)
func TestPermissionArgs_YOLO(t *testing.T) {
	got := permissionArgs(true, "")

	want := []string{"--dangerously-skip-permissions"}
	if !slices.Equal(got, want) {
		t.Fatalf("permissionArgs(true, \"\") = %v, want %v", got, want)
	}
	for _, banned := range []string{
		"--permission-prompt-tool", "--mcp-config",
		"--strict-mcp-config", "--permission-mode",
	} {
		if slices.Contains(got, banned) {
			t.Errorf("YOLO branch must not carry %q; got %v", banned, got)
		}
	}
}

// TestPermissionArgs_NonYOLO pins the enforcement branch: the exact 7-element
// slice routing every tool use through the daemon approval registry, and the
// absence of --dangerously-skip-permissions. (AC-1.)
func TestPermissionArgs_NonYOLO(t *testing.T) {
	got := permissionArgs(false, "/tmp/cfg.json")

	want := []string{
		"--permission-prompt-tool", "mcp__pyry_approve__approve",
		"--mcp-config", "/tmp/cfg.json",
		"--strict-mcp-config",
		"--permission-mode", "default",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("permissionArgs(false, ...) = %v, want %v", got, want)
	}

	// Named structural pins so a re-order that still equals a stale `want`
	// trips a clearly-named guard.
	if !nextValueEquals(got, "--permission-prompt-tool", approveToolRef) {
		t.Errorf("missing `--permission-prompt-tool %s` in %v", approveToolRef, got)
	}
	if !nextValueEquals(got, "--mcp-config", "/tmp/cfg.json") {
		t.Errorf("missing `--mcp-config /tmp/cfg.json` in %v", got)
	}
	if !nextValueEquals(got, "--permission-mode", "default") {
		t.Errorf("missing `--permission-mode default` in %v", got)
	}
	if !slices.Contains(got, "--strict-mcp-config") {
		t.Errorf("missing --strict-mcp-config in %v", got)
	}
	if slices.Contains(got, "--dangerously-skip-permissions") {
		t.Errorf("enforcement branch must not carry --dangerously-skip-permissions; got %v", got)
	}
}

// TestApproveToolRef_DriftGuard asserts the tool reference is composed from the
// merged mcp-approve constants, so renaming mcpServerName / approveToolName in
// mcp_approve.go cannot silently desync the reference claude is pointed at from
// the server/tool the subcommand advertises. (AC-3.)
func TestApproveToolRef_DriftGuard(t *testing.T) {
	want := "mcp__" + mcpServerName + "__" + approveToolName
	if approveToolRef != want {
		t.Errorf("approveToolRef = %q, want %q", approveToolRef, want)
	}
	if want != "mcp__pyry_approve__approve" {
		t.Errorf("advertised tool reference = %q, want mcp__pyry_approve__approve", want)
	}
}

// TestRenderMCPApproveConfig_Content asserts the generated config registers
// exactly one pyry_approve server whose command forks the running pyry binary
// as `pyry mcp-approve -pyry-socket <socket>` — the socket-targeting identity
// that reaches the spawning daemon rather than a default-named instance.
// (AC-3, AC-4.)
func TestRenderMCPApproveConfig_Content(t *testing.T) {
	const (
		pyryBin = "/opt/pyry"
		socket  = "/home/u/.pyry/elli.sock"
	)
	b, err := renderMCPApproveConfig(pyryBin, socket)
	if err != nil {
		t.Fatalf("renderMCPApproveConfig: %v", err)
	}

	var cfg mcpApproveConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("generated config is not well-formed JSON: %v\n%s", err, b)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("want exactly one server, got %d: %v", len(cfg.MCPServers), cfg.MCPServers)
	}
	spec, ok := cfg.MCPServers[mcpServerName]
	if !ok {
		t.Fatalf("config has no server keyed %q; keys: %v", mcpServerName, cfg.MCPServers)
	}
	if spec.Command != pyryBin {
		t.Errorf("server command = %q, want %q", spec.Command, pyryBin)
	}
	wantArgs := []string{"mcp-approve", "-pyry-socket", socket}
	if !slices.Equal(spec.Args, wantArgs) {
		t.Fatalf("server args = %v, want %v", spec.Args, wantArgs)
	}

	// Explicit AC-4 pin: the socket-targeting flag is present and carries the
	// exact spawning-daemon socket value.
	if !nextValueEquals(spec.Args, "-pyry-socket", socket) {
		t.Errorf("server args missing `-pyry-socket %s`: %v", socket, spec.Args)
	}
	if slices.Equal(spec.Args, []string{"mcp-approve"}) {
		t.Errorf("bare `mcp-approve` (no socket) resolves to the default instance; must target %s", socket)
	}
}

// TestRenderMCPApproveConfig_FailClosed asserts the deterministic net behind
// AC-4: an empty socket or binary path yields an error and nil bytes, never a
// config that omits -pyry-socket or names an empty command.
func TestRenderMCPApproveConfig_FailClosed(t *testing.T) {
	tests := []struct {
		name    string
		pyryBin string
		socket  string
	}{
		{"empty socket", "/opt/pyry", ""},
		{"empty binary", "", "/home/u/.pyry/elli.sock"},
		{"both empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := renderMCPApproveConfig(tc.pyryBin, tc.socket)
			if err == nil {
				t.Fatalf("renderMCPApproveConfig(%q, %q) = nil error; want fail-closed error", tc.pyryBin, tc.socket)
			}
			if b != nil {
				t.Errorf("fail-closed path returned non-nil bytes: %s", b)
			}
		})
	}
}

// TestWriteMCPApproveConfig writes the config to a tmp file and asserts the
// on-disk bytes equal renderMCPApproveConfig's output verbatim (no trailing
// encoder newline divergence).
func TestWriteMCPApproveConfig(t *testing.T) {
	const (
		pyryBin = "/opt/pyry"
		socket  = "/home/u/.pyry/elli.sock"
	)
	path, err := writeMCPApproveConfig(pyryBin, socket)
	if err != nil {
		t.Fatalf("writeMCPApproveConfig: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back config: %v", err)
	}
	want, err := renderMCPApproveConfig(pyryBin, socket)
	if err != nil {
		t.Fatalf("renderMCPApproveConfig: %v", err)
	}
	if !slices.Equal(onDisk, want) {
		t.Errorf("on-disk config != rendered config\n disk = %s\n want = %s", onDisk, want)
	}
}

// TestWriteMCPApproveConfig_FailClosed asserts the writer propagates
// renderMCPApproveConfig's fail-closed error and creates no file.
func TestWriteMCPApproveConfig_FailClosed(t *testing.T) {
	path, err := writeMCPApproveConfig("/opt/pyry", "")
	if err == nil {
		t.Fatalf("writeMCPApproveConfig with empty socket = nil error; want fail-closed error")
	}
	if path != "" {
		t.Errorf("fail-closed path returned a leaked file path: %q", path)
	}
}
