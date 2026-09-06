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

// TestRenderMCPServersConfig_Content asserts the generated config registers the
// two servers claude is meant to see and no third: pyry_approve (#1106) and, since
// #2169, pyry_files. Each forks the running pyry binary as
// `pyry <subcommand> -pyry-socket <socket>` — the socket-targeting identity that
// reaches the SPAWNING daemon rather than a default-named instance.
//
// Table-driven over the two entries rather than two straight-line blocks, so a
// third registration added later cannot be given a weaker check than these two:
// the row is the check. (#1106 AC-3/AC-4; #2169 AC-1.)
func TestRenderMCPServersConfig_Content(t *testing.T) {
	const (
		pyryBin = "/opt/pyry"
		socket  = "/home/u/.pyry/elli.sock"
	)
	b, err := renderMCPServersConfig(pyryBin, socket)
	if err != nil {
		t.Fatalf("renderMCPServersConfig: %v", err)
	}

	var cfg mcpServersConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("generated config is not well-formed JSON: %v\n%s", err, b)
	}

	// The server names come from the subcommands' own constants, never re-spelled
	// here, for approveToolRef's reason: a rename there must move this expectation
	// with it rather than leave a test asserting a name nothing advertises.
	entries := []struct {
		name       string
		subcommand string
	}{
		{mcpServerName, "mcp-approve"},
		{mcpFilesServerName, "mcp-files"},
	}
	if len(cfg.MCPServers) != len(entries) {
		t.Fatalf("want exactly %d servers, got %d: %v", len(entries), len(cfg.MCPServers), cfg.MCPServers)
	}
	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			spec, ok := cfg.MCPServers[e.name]
			if !ok {
				t.Fatalf("config has no server keyed %q; keys: %v", e.name, cfg.MCPServers)
			}
			if spec.Command != pyryBin {
				t.Errorf("server command = %q, want %q", spec.Command, pyryBin)
			}
			wantArgs := []string{e.subcommand, "-pyry-socket", socket}
			if !slices.Equal(spec.Args, wantArgs) {
				t.Fatalf("server args = %v, want %v", spec.Args, wantArgs)
			}

			// Explicit pin: the socket-targeting flag is present and carries the
			// exact spawning-daemon socket value.
			if !nextValueEquals(spec.Args, "-pyry-socket", socket) {
				t.Errorf("server args missing `-pyry-socket %s`: %v", socket, spec.Args)
			}
			if slices.Equal(spec.Args, []string{e.subcommand}) {
				t.Errorf("bare `%s` (no socket) resolves to the default instance; must target %s", e.subcommand, socket)
			}
		})
	}
}

// TestFilesToolRef_DriftGuard is TestApproveToolRef_DriftGuard's sibling for the
// send_file server, and it exists because the reference has no production
// spelling to guard. approveToolRef is a package var because
// --permission-prompt-tool needs the string; nothing in production needs
// mcp__pyry_files__send_file, so the only place it is written down is the live
// gate in internal/e2e/realclaude, which is a different package and must
// transcribe the literal.
//
// This pins that literal against the two constants the server actually advertises
// (mcpFilesServerName from the mcp-config entry, sendFileToolName from
// filesServer.toolsList), so renaming either reddens HERE — in the cheap suite —
// instead of silently in a live-claude run that costs real tokens to discover.
// (#2169 AC-1.)
func TestFilesToolRef_DriftGuard(t *testing.T) {
	got := "mcp__" + mcpFilesServerName + "__" + sendFileToolName
	if want := "mcp__pyry_files__send_file"; got != want {
		t.Errorf("advertised send_file tool reference = %q, want %q — the live gate transcribes %q as a literal", got, want, want)
	}
}

// TestRenderMCPServersConfig_FailClosed asserts the deterministic net behind
// #1106's AC-4: an empty socket or binary path yields an error and nil bytes,
// never a config that omits -pyry-socket or names an empty command.
//
// One render gates BOTH registrations, which is the whole reason #2169 added its
// entry to this function instead of composing a second document: there is no
// arrangement in which pyry_files is emitted with a bare argv (resolving to the
// default-named instance = wrong daemon) while pyry_approve is not. Asserting nil
// bytes is what covers both — a partial document is not one of the shapes this
// can return. (#2169 AC-1.)
func TestRenderMCPServersConfig_FailClosed(t *testing.T) {
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
			b, err := renderMCPServersConfig(tc.pyryBin, tc.socket)
			if err == nil {
				t.Fatalf("renderMCPServersConfig(%q, %q) = nil error; want fail-closed error", tc.pyryBin, tc.socket)
			}
			if b != nil {
				t.Errorf("fail-closed path returned non-nil bytes: %s", b)
			}
		})
	}
}

// TestWriteMCPServersConfig writes the config to a tmp file and asserts the
// on-disk bytes equal renderMCPServersConfig's output verbatim (no trailing
// encoder newline divergence).
func TestWriteMCPServersConfig(t *testing.T) {
	const (
		pyryBin = "/opt/pyry"
		socket  = "/home/u/.pyry/elli.sock"
	)
	path, err := writeMCPServersConfig(pyryBin, socket)
	if err != nil {
		t.Fatalf("writeMCPServersConfig: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back config: %v", err)
	}
	want, err := renderMCPServersConfig(pyryBin, socket)
	if err != nil {
		t.Fatalf("renderMCPServersConfig: %v", err)
	}
	if !slices.Equal(onDisk, want) {
		t.Errorf("on-disk config != rendered config\n disk = %s\n want = %s", onDisk, want)
	}
}

// TestWriteMCPServersConfig_FailClosed asserts the writer propagates
// renderMCPServersConfig's fail-closed error and creates no file.
func TestWriteMCPServersConfig_FailClosed(t *testing.T) {
	path, err := writeMCPServersConfig("/opt/pyry", "")
	if err == nil {
		t.Fatalf("writeMCPServersConfig with empty socket = nil error; want fail-closed error")
	}
	if path != "" {
		t.Errorf("fail-closed path returned a leaked file path: %q", path)
	}
}
