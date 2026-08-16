package streamrunner

import (
	"strconv"
	"strings"
)

// ArgsParams is the input to BuildClaudeArgs: everything that varies between
// one headless claude spawn and another.
type ArgsParams struct {
	// SystemPromptFile is passed to --append-system-prompt-file. Required by
	// claude even when empty is meant; callers with nothing to append pass
	// /dev/null, a portable 0-byte readable device on both targets.
	SystemPromptFile string

	Model    string
	Effort   string
	MaxTurns int

	// AllowedTools is comma-joined for --allowed-tools. Note this flag is NOT
	// the enforcement — see SettingsPath.
	AllowedTools []string

	// SettingsPath is the per-spawn deny-default settings file, and it is what
	// actually enforces the tool boundary. Omitted from the argv when empty.
	SettingsPath string

	// PermissionArgs replaces the default `--permission-mode dontAsk` pair when
	// non-empty. This is the daemon-approval branch, which routes every tool use
	// through the approval registry instead of auto-denying.
	PermissionArgs []string
}

// BuildClaudeArgs assembles the claude argv (excluding argv[0]) for a headless
// stream-json spawn. It is the single source of that shape, shared by
// `pyry agent-run` and by the permission self-check that exists to verify the
// shape enforces what it claims.
//
// That sharing is the point rather than a convenience. The self-check spawns a
// real claude and proves the settings file blocks a tool that is not on the
// allowlist. A self-check assembling its own argv would verify a spawn nobody
// performs, which is how the check ended up attached to a runner production had
// already stopped using.
//
// Two flag choices are load-bearing and neither is obvious:
//
//   - `--permission-mode dontAsk`, NOT `--dangerously-skip-permissions`. dontAsk
//     is the documented mode for CI and restricted environments: it auto-denies
//     anything not pre-approved and never waits for input. The skip flag looked
//     equivalent and is not, because it outranks every mode including the one
//     the settings file itself asks for. With it present the allowlist enforced
//     nothing at all, which is how every dispatched agent ran unrestricted for
//     two weeks after the 2026-07-25 fleet switch (#1387).
//   - `--settings` carries the per-spawn deny-default file and IS the boundary.
//     `--allowed-tools` is still passed because it is harmless and becomes
//     meaningful again if the permission mode changes, but it must never be
//     relied on as the enforcement.
func BuildClaudeArgs(p ArgsParams) []string {
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
	}
	if len(p.PermissionArgs) > 0 {
		args = append(args, p.PermissionArgs...)
	} else {
		args = append(args, "--permission-mode", "dontAsk")
	}
	if p.SettingsPath != "" {
		args = append(args, "--settings", p.SettingsPath)
	}
	return append(args,
		"--append-system-prompt-file", p.SystemPromptFile,
		"--model", p.Model,
		"--effort", p.Effort,
		"--max-turns", strconv.Itoa(p.MaxTurns),
		"--allowed-tools", strings.Join(p.AllowedTools, ","),
	)
}
