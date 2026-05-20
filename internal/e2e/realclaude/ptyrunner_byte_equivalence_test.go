//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun/ptyrunner"
	"github.com/pyrycode/pyrycode/internal/agentrun/settings"
	"github.com/pyrycode/pyrycode/internal/agentrun/streamrunner"
	"github.com/pyrycode/pyrycode/internal/agentrun/trust"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// ptyRunnerArgvFlags is the literal flag set internal/agentrun/ptyrunner's
// buildArgs emits for the interactive-TUI claude spawn. Source of truth:
// ptyrunner.buildArgs (internal/agentrun/ptyrunner/runner.go ~lines 395-404).
// Drift in either direction — a flag renamed in buildArgs without updating
// this list, OR a future claude release dropping one of the flags — is what
// TestPtyRunnerArgvFlagsExistInClaudeHelp catches. Pinning the flag SET
// rather than a claude version makes the assertion durable across upgrades.
var ptyRunnerArgvFlags = []string{
	"--session-id",
	"--settings",
	"--permission-mode",
	"--append-system-prompt-file",
	"--model",
	"--effort",
}

// ptyRunnerArgvFlagAlternates maps a buildArgs flag to a claude-help
// shorthand that documents the same flag. As of claude 2.1.144, the
// --append-system-prompt-file flag is documented only as the bracketed
// shorthand --append-system-prompt[-file] (meaning both --append-system-prompt
// and --append-system-prompt-file are valid). The flag works at runtime —
// every existing realclaude test that uses streamrunner argv passes it — but
// the literal "--append-system-prompt-file" token is absent from `claude
// --help`. Accept either form. If a future release drops BOTH forms, the
// drift-detection still fires.
var ptyRunnerArgvFlagAlternates = map[string]string{
	"--append-system-prompt-file": "--append-system-prompt[-file]",
}

// TestPtyRunnerArgvFlagsExistInClaudeHelp asserts every flag ptyrunner.buildArgs
// emits is still recognized by `claude --help`. Cheap (no API key, <1s
// wall-clock); contributors without ANTHROPIC_API_KEY can still verify this
// half of the file locally.
func TestPtyRunnerArgvFlagsExistInClaudeHelp(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatalf("claude binary not on PATH: %v\nthis suite requires the real claude CLI; install it or adjust PATH before running `make e2e-realclaude`", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "claude", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("claude --help failed: %v\noutput:\n%s", err, out)
	}

	for _, flag := range ptyRunnerArgvFlags {
		if bytes.Contains(out, []byte(flag)) {
			continue
		}
		if alt, ok := ptyRunnerArgvFlagAlternates[flag]; ok && bytes.Contains(out, []byte(alt)) {
			continue
		}
		t.Fatalf("claude --help does not mention %s (alt %q); ptyrunner.buildArgs may need adjustment (claude --help output:\n%s)",
			flag, ptyRunnerArgvFlagAlternates[flag], out)
	}
}

// envelopeShape captures the per-line dispatcher-visible structural shape
// extracted from a stream-json byte stream. The two pipelines emit DIFFERENT
// field sets on the `result` trailer — streamrunner forwards claude's native
// fields verbatim (api_error_status, duration_api_ms, result, modelUsage,
// permission_denials, fast_mode_state, uuid, usage.server_tool_use);
// ptyrunner's emitter (internal/agentrun/streamjson/emitter.go:251-273)
// declares a strict subset by design. Byte-equivalence would fail. The
// structural comparison asks the right question — *does the dispatcher see
// the same signal?*
//
// Normalization rules — fields we deliberately DO NOT compare and why:
//
//   - session_id            : random per run (NewID for pty, claude-minted for stream)
//   - ts / timestamp        : wall-clock-dependent
//   - duration_ms /
//     duration_api_ms       : run-time-dependent
//   - total_cost_usd /
//     usage.*               : token counts depend on LLM response
//   - result (text body)    : LLM output is non-deterministic
//   - api_error_status,
//     modelUsage,
//     permission_denials,
//     fast_mode_state, uuid : in streamrunner's pass-through; omitted by
//     usage.server_tool_use   ptyrunner's emitter trailer by design
//   - message.id / .text /
//     message.usage         : claude-internal id; non-deterministic text;
//                             token counts
//   - tool_use_id /
//     content[*].id         : claude-internal correlation ids
//   - parentUuid, cwd,
//     sessionId (JSONL-
//     native per-line meta) : present in ptyrunner's JSONL pass-through;
//                             streamrunner emits a minimal user envelope
//                             from its userTurn struct. The shape-only
//                             comparison naturally ignores them.
//
// extractShapes reads ONLY `type` + `subtype`. Field-level invariants
// (init.model / user prompt text / result.is_error / result.num_turns) are
// asserted via targeted decodes below; this struct never materialises a
// normalised form.
type envelopeShape struct {
	Type    string
	Subtype string
}

func extractShapes(stream []byte) ([]envelopeShape, error) {
	var out []envelopeShape
	for i, line := range bytes.Split(stream, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return nil, fmt.Errorf("extractShapes: line %d: %w (raw: %s)", i, err, truncatePrefix(line, 256))
		}
		out = append(out, envelopeShape{Type: env.Type, Subtype: env.Subtype})
	}
	return out, nil
}

func truncatePrefix(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}

// streamRunnerArgs mirrors cmd/pyry/agent_run.go:buildStreamRunnerClaudeArgs
// verbatim. cmd/pyry is `package main` so the function is not importable;
// duplicating the argv shape here is the architect-prescribed pinning
// approach (spec #482, "Don't import cmd/pyry"). If either side drifts, this
// test fails on the next run.
func streamRunnerArgs(systemPath, model, effort string, maxTurns int, allowedTools []string) []string {
	return []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--append-system-prompt-file", systemPath,
		"--model", model,
		"--effort", effort,
		"--max-turns", strconv.Itoa(maxTurns),
		"--allowed-tools", strings.Join(allowedTools, ","),
	}
}

// TestPtyRunnerVsStreamRunner_StructuralEquivalence is the empirical-validation
// gate: both runners drive the real `claude` CLI with the same prompt + same
// budget; the two stdout byte streams are parsed into per-envelope
// (type,subtype) sequences and asserted equal element-by-element. Four
// field-level invariants pin dispatcher-visible content that the shape
// comparison alone does not cover (init model, user prompt text, result
// is_error, result num_turns). See envelopeShape for the normalization
// rationale.
//
// Test gating: WithWorktreeAuthenticated skips when ANTHROPIC_API_KEY is
// absent. CI without the key skips cleanly; the argv-shape test in the same
// file still runs.
//
// Wall-clock: ~30-60s (two 5-15s real-claude turns). Both runs use Haiku 4.5
// at low effort with MaxTurns=1 to minimise LLM stochasticity surface.
func TestPtyRunnerVsStreamRunner_StructuralEquivalence(t *testing.T) {
	home := WithWorktreeAuthenticated(t)

	workdirStream := filepath.Join(home, "stream-work")
	workdirPty := filepath.Join(home, "pty-work")
	for _, d := range []string{workdirStream, workdirPty} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	systemPath := filepath.Join(home, "system.txt")
	const systemText = "You are a real-claude smoke test. Reply with the single word OK and nothing else."
	if err := os.WriteFile(systemPath, []byte(systemText), 0o600); err != nil {
		t.Fatalf("write system.txt: %v", err)
	}

	const promptText = "Reply with the single word OK and nothing else."
	promptBytes := []byte(promptText)
	allowedTools := []string{"Read"}
	const (
		model    = "claude-haiku-4-5"
		effort   = "low"
		maxTurns = 1
	)

	// Step A — streamrunner side. Sequential with Step B; each run owns its
	// own ctx-with-timeout so a slow first run does not eat the second's
	// budget.
	var streamOut, streamErr bytes.Buffer
	{
		ctxA, cancelA := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancelA()
		if err := streamrunner.Run(ctxA, streamrunner.Config{
			ClaudeBin:   "claude",
			WorkDir:     workdirStream,
			Args:        streamRunnerArgs(systemPath, model, effort, maxTurns, allowedTools),
			PromptBytes: promptBytes,
			Stdout:      &streamOut,
			Stderr:      &streamErr,
		}); err != nil {
			t.Fatalf("streamrunner.Run: %v\nstderr:\n%s\nstdout (1024):\n%s",
				err, streamErr.String(), truncatePrefix(streamOut.Bytes(), 1024))
		}
	}

	// Step B — ptyrunner side. Mirrors cmd/pyry/agent_run.go's runAgentRunPty
	// wiring: trust pre-write keyed on realpath, deny-default settings tempfile,
	// fresh UUIDv4 session id. WorkDir is realpath (not workdirPty) — handing
	// claude a symlinked path when the trust pre-write keyed the realpath would
	// miss the modal-check and re-render the trust modal (#470 / codebase/470.md).
	realpath, err := trust.MarkWorkdirTrusted(workdirPty)
	if err != nil {
		t.Fatalf("trust.MarkWorkdirTrusted: %v", err)
	}
	settingsPath, err := settings.WriteSettings(allowedTools)
	if err != nil {
		t.Fatalf("settings.WriteSettings: %v", err)
	}
	// t.Cleanup (not defer) survives any earlier t.Fatalf in the test and is
	// the idiomatic stdlib pattern for test-owned tempfiles.
	t.Cleanup(func() { _ = os.Remove(settingsPath) })

	sid, err := sessions.NewID()
	if err != nil {
		t.Fatalf("sessions.NewID: %v", err)
	}

	var ptyOut, ptyErr bytes.Buffer
	{
		ctxB, cancelB := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancelB()
		// HomeDir intentionally left empty — WithWorktreeAuthenticated's
		// t.Setenv("HOME", home) already pins os.UserHomeDir() so the JSONL
		// watcher reads from `home` exactly as in production. Setting HomeDir
		// would test a different code path.
		if err := ptyrunner.Run(ctxB, ptyrunner.Config{
			ClaudeBin:    "claude",
			WorkDir:      realpath,
			SessionID:    string(sid),
			SettingsPath: settingsPath,
			SystemPrompt: systemPath,
			Model:        model,
			Effort:       effort,
			MaxTurns:     maxTurns,
			PromptBytes:  promptBytes,
			Stdout:       &ptyOut,
			Stderr:       &ptyErr,
		}); err != nil {
			t.Fatalf("ptyrunner.Run: %v\nstderr:\n%s\nstdout (1024):\n%s",
				err, ptyErr.String(), truncatePrefix(ptyOut.Bytes(), 1024))
		}
	}

	// Step C — normalise and compare.
	streamShapes, err := extractShapes(streamOut.Bytes())
	if err != nil {
		t.Fatalf("extractShapes(streamrunner stdout): %v", err)
	}
	ptyShapes, err := extractShapes(ptyOut.Bytes())
	if err != nil {
		t.Fatalf("extractShapes(ptyrunner stdout): %v", err)
	}

	compareShapes(t, streamShapes, ptyShapes,
		streamOut.Bytes(), ptyOut.Bytes(),
		streamErr.Bytes(), ptyErr.Bytes())

	// Field-level invariants 7-10. Errorf (not Fatalf) so all four run.
	checkInitModel(t, "streamrunner", streamOut.Bytes(), model)
	checkInitModel(t, "ptyrunner", ptyOut.Bytes(), model)
	checkUserContains(t, "streamrunner", streamOut.Bytes(), promptText)
	checkUserContains(t, "ptyrunner", ptyOut.Bytes(), promptText)
	streamResult := decodeResultTrailer(t, "streamrunner", streamOut.Bytes())
	ptyResult := decodeResultTrailer(t, "ptyrunner", ptyOut.Bytes())
	if streamResult.IsError != ptyResult.IsError {
		t.Errorf("result.is_error mismatch: streamrunner=%v ptyrunner=%v",
			streamResult.IsError, ptyResult.IsError)
	}
	if streamResult.NumTurns != ptyResult.NumTurns {
		t.Errorf("result.num_turns mismatch: streamrunner=%d ptyrunner=%d",
			streamResult.NumTurns, ptyResult.NumTurns)
	}
}

// compareShapes asserts the two shape sequences agree element-by-element and
// prints a diagnostic-friendly failure message on mismatch. Failure prints:
// both shape sequences, the first divergent index (or "lengths differ"), and
// 1024-byte truncated prefixes of both raw streams + both stderr buffers so
// a regression is diagnosable from a single test-output read.
func compareShapes(t *testing.T, stream, pty []envelopeShape, streamRaw, ptyRaw, streamErrBuf, ptyErrBuf []byte) {
	t.Helper()
	fail := func(reason string) {
		t.Fatalf(
			"structural mismatch: %s\n\nstreamrunner shapes (%d):\n%sptyrunner shapes (%d):\n%sstreamrunner stdout (1024):\n%s\nptyrunner stdout (1024):\n%s\nstreamrunner stderr:\n%s\nptyrunner stderr:\n%s",
			reason, len(stream), formatShapes(stream), len(pty), formatShapes(pty),
			truncatePrefix(streamRaw, 1024), truncatePrefix(ptyRaw, 1024),
			truncatePrefix(streamErrBuf, 1024), truncatePrefix(ptyErrBuf, 1024),
		)
	}

	if len(stream) < 4 {
		fail(fmt.Sprintf("streamrunner produced %d shapes, want >= 4 (init+user+assistant+result)", len(stream)))
	}
	if len(pty) < 4 {
		fail(fmt.Sprintf("ptyrunner produced %d shapes, want >= 4 (init+user+assistant+result)", len(pty)))
	}
	if stream[0].Type != "system" || stream[0].Subtype != "init" {
		fail(fmt.Sprintf("streamrunner[0] = (%q,%q), want (system,init)", stream[0].Type, stream[0].Subtype))
	}
	if pty[0].Type != "system" || pty[0].Subtype != "init" {
		fail(fmt.Sprintf("ptyrunner[0] = (%q,%q), want (system,init)", pty[0].Type, pty[0].Subtype))
	}
	streamLast := stream[len(stream)-1]
	ptyLast := pty[len(pty)-1]
	if streamLast.Type != "result" {
		fail(fmt.Sprintf("streamrunner last = (%q,%q), want type=result", streamLast.Type, streamLast.Subtype))
	}
	if ptyLast.Type != "result" {
		fail(fmt.Sprintf("ptyrunner last = (%q,%q), want type=result", ptyLast.Type, ptyLast.Subtype))
	}
	if streamLast.Subtype != ptyLast.Subtype {
		fail(fmt.Sprintf("result subtype mismatch: streamrunner=%q ptyrunner=%q (both pipelines should agree on success vs error_max_turns)", streamLast.Subtype, ptyLast.Subtype))
	}

	if !reflect.DeepEqual(stream, pty) {
		if len(stream) != len(pty) {
			fail(fmt.Sprintf("shape lengths differ: streamrunner=%d ptyrunner=%d", len(stream), len(pty)))
		}
		for i := range stream {
			if stream[i] != pty[i] {
				fail(fmt.Sprintf("first divergent index = %d: streamrunner=(%q,%q) ptyrunner=(%q,%q)",
					i, stream[i].Type, stream[i].Subtype, pty[i].Type, pty[i].Subtype))
			}
		}
	}
}

func formatShapes(s []envelopeShape) string {
	var b strings.Builder
	for i, sh := range s {
		fmt.Fprintf(&b, "  [%2d] %-15s %s\n", i, sh.Type, sh.Subtype)
	}
	return b.String()
}

// checkInitModel decodes the first system/init line and asserts model matches.
func checkInitModel(t *testing.T, side string, stream []byte, wantModel string) {
	t.Helper()
	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Model   string `json:"model"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Type == "system" && env.Subtype == "init" {
			if env.Model != wantModel {
				t.Errorf("%s init.model = %q, want %q", side, env.Model, wantModel)
			}
			return
		}
	}
	t.Errorf("%s: no system/init line found in stdout", side)
}

// checkUserContains asserts the first user line contains the prompt text
// somewhere. Robust to content-string vs content-array shape variation (the
// two pipelines may serialise the user-turn payload differently); the prompt
// text has no JSON-escape-sensitive characters so bytes.Contains is sound.
func checkUserContains(t *testing.T, side string, stream []byte, prompt string) {
	t.Helper()
	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Type == "user" {
			if !bytes.Contains(line, []byte(prompt)) {
				t.Errorf("%s: first user line does not contain prompt text\nline: %s",
					side, truncatePrefix(line, 512))
			}
			return
		}
	}
	t.Errorf("%s: no user line found in stdout", side)
}

// byteEquivResultTrailer is the minimal subset of the result-trailer fields
// this test compares (is_error, num_turns). Named with a test-local prefix
// to avoid the `resultTrailer` collision with tool_loop_test.go's identically
// named type in the same package.
type byteEquivResultTrailer struct {
	IsError  bool `json:"is_error"`
	NumTurns int  `json:"num_turns"`
}

func decodeResultTrailer(t *testing.T, side string, stream []byte) byteEquivResultTrailer {
	t.Helper()
	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Type == "result" {
			var tr byteEquivResultTrailer
			if err := json.Unmarshal(line, &tr); err != nil {
				t.Fatalf("%s: decode result trailer: %v\nline: %s", side, err, truncatePrefix(line, 512))
			}
			return tr
		}
	}
	t.Fatalf("%s: no result trailer found in stdout", side)
	return byteEquivResultTrailer{}
}
