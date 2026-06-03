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

// additiveDriftAllowlist groups the per-runner tolerated emissions used by
// additiveDriftViolations. The Events set covers top-level "type" values; the
// ResultTrailerFields set covers top-level JSON keys on the `result` line.
// Each populated entry MUST carry a trailing line comment citing #503 (or
// the eventual audit doc) so a future contributor reading a failure has the
// single edit point. See [[503]] for the catalogue + per-field rationale.
type additiveDriftAllowlist struct {
	Events              map[string]struct{}
	ResultTrailerFields map[string]struct{}
}

// expectedStreamRunnerOnly enumerates event types and top-level
// result-trailer fields that streamrunner emits but ptyrunner does not, and
// which are accepted as documented one-sided emissions rather than drift.
// Names match the AC literally so a failure message naming the table is
// grep-able. Sibling ticket #505 tunes membership per the #503 audit's
// per-field "must close" vs "document as omitted" decisions; this ticket
// only ships the mechanism.
var expectedStreamRunnerOnly = additiveDriftAllowlist{
	Events: map[string]struct{}{
		"rate_limit_event": {}, // #503 audit 2026-05-23: API-stream event, structurally unreachable from claude's local JSONL (which ptyrunner tails). Dispatcher default-case preview log only — no semantic consumption.
	},
	ResultTrailerFields: map[string]struct{}{
		"api_error_status":   {}, // #503 audit 2026-05-23: API-only (claude's HTTP layer); dispatcher never reads it.
		"duration_api_ms":    {}, // #503 audit 2026-05-23: API-only; dispatcher never reads it.
		"fast_mode_state":    {}, // #503 audit 2026-05-23: API-only; dispatcher never reads it.
		"modelUsage":         {}, // #503 audit 2026-05-23: API-only (per-model breakdown); dispatcher never reads it.
		"permission_denials": {}, // #503 audit 2026-05-23: API-only (claude's gate result); dispatcher never reads it.
		"ttft_ms":            {}, // #503 audit 2026-05-23: API-only (time to first token); dispatcher never reads it.
		"uuid":               {}, // #503 audit 2026-05-23: API-side session UUID, distinct from session_id (which dispatcher does read); duplicate signal.
	},
}

// expectedPtyRunnerOnly is the inverse table — event types and top-level
// result-trailer fields ptyrunner emits but streamrunner does not. Starts
// empty (AC mandate); sibling ticket #505 will populate per the #503 audit
// if any ptyrunner-only emissions are decided to be tolerated rather than
// closed. Empty-but-initialised, not nil, to keep the intentional-emptiness
// signal explicit.
var expectedPtyRunnerOnly = additiveDriftAllowlist{
	Events: map[string]struct{}{
		"permission-mode":       {}, // #503 audit 2026-05-23: claude local-JSONL housekeeping envelope. Dispatcher default-case preview log only.
		"file-history-snapshot": {}, // #503 audit 2026-05-23: ditto.
		"skill_listing":         {}, // #503 audit 2026-05-23: ditto.
		"ai-title":              {}, // #503 audit 2026-05-23: ditto.
	},
	ResultTrailerFields: map[string]struct{}{},
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
// extracted from a stream-json byte stream. The two pipelines emit
// DIFFERENT field sets on the `result` trailer; this comparison asks the
// right question — does the dispatcher see the same SIGNAL?
//
// Per-field rationale for every tolerated divergence (both directions):
// docs/audits/2026-05-23-ptyrunner-streamrunner-byte-equivalence.md (#503).
// TL;DR — none of the divergent fields/events are consumed semantically
// by agent-dispatcher; they are all log-preview-only.
//
// extractShapes reads ONLY `type` + `subtype`. Field-level invariants
// (init.cwd / .tools / .model / .session_id, user prompt text,
// result.is_error / result.num_turns) are asserted via targeted decodes
// below; this struct never materialises a normalised form.
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

// extractEventTypeSet returns the set of distinct top-level "type" values
// observed across all non-empty lines in stream. The empty type string is
// included if anything emitted an envelope with no "type" field — that
// itself is a drift signal worth surfacing through the same channel.
func extractEventTypeSet(stream []byte) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for i, line := range bytes.Split(stream, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return nil, fmt.Errorf("extractEventTypeSet: line %d: %w (raw: %s)", i, err, truncatePrefix(line, 256))
		}
		out[env.Type] = struct{}{}
	}
	return out, nil
}

// extractResultTrailerFields returns the set of top-level JSON keys on the
// FIRST type:"result" line in stream. Errors if no result line is found —
// mirrors decodeResultTrailer's contract but returns the error so the
// self-check sub-tests can assert on the result without a fake *testing.T.
func extractResultTrailerFields(stream []byte) (map[string]struct{}, error) {
	for i, line := range bytes.Split(stream, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return nil, fmt.Errorf("extractResultTrailerFields: line %d: %w (raw: %s)", i, err, truncatePrefix(line, 256))
		}
		if env.Type != "result" {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			return nil, fmt.Errorf("extractResultTrailerFields: line %d: decode keys: %w (raw: %s)", i, err, truncatePrefix(line, 256))
		}
		out := make(map[string]struct{}, len(fields))
		for k := range fields {
			out[k] = struct{}{}
		}
		return out, nil
	}
	return nil, fmt.Errorf("extractResultTrailerFields: no result line found")
}

// additiveDriftViolations returns one violation message per drift between
// the two raw streams (empty slice = no drift). Each message names the
// unknown identifier AND the allowlist table that needs updating, so a
// contributor reading a failure has a single edit point.
//
// Returns []string rather than calling t.Errorf so the self-check
// sub-tests can feed hand-crafted byte fixtures and assert on the slice
// directly without a fake *testing.T. The real test
// (TestPtyRunnerVsStreamRunner_StructuralEquivalence) iterates the slice
// with t.Error so all signals surface in one run.
//
// Helper-extraction failures (malformed JSON, missing result line) are
// surfaced as violation messages too — a malformed stream is itself drift
// worth surfacing through the same channel.
func additiveDriftViolations(streamRaw, ptyRaw []byte) []string {
	var out []string

	streamEvents, err := extractEventTypeSet(streamRaw)
	if err != nil {
		out = append(out, fmt.Sprintf("extractEventTypeSet(streamrunner): %v", err))
	}
	ptyEvents, err := extractEventTypeSet(ptyRaw)
	if err != nil {
		out = append(out, fmt.Sprintf("extractEventTypeSet(ptyrunner): %v", err))
	}
	streamFields, err := extractResultTrailerFields(streamRaw)
	if err != nil {
		out = append(out, fmt.Sprintf("extractResultTrailerFields(streamrunner): %v", err))
	}
	ptyFields, err := extractResultTrailerFields(ptyRaw)
	if err != nil {
		out = append(out, fmt.Sprintf("extractResultTrailerFields(ptyrunner): %v", err))
	}

	for ev := range streamEvents {
		if _, ok := ptyEvents[ev]; ok {
			continue
		}
		if _, ok := expectedStreamRunnerOnly.Events[ev]; ok {
			continue
		}
		out = append(out, fmt.Sprintf(
			"streamrunner emitted event type %q which is not in the cross-runner intersection and not in expectedStreamRunnerOnly.Events; either add it to that table with a citation comment (#503 or audit doc), or treat the divergence as a real bug",
			ev))
	}
	for ev := range ptyEvents {
		if _, ok := streamEvents[ev]; ok {
			continue
		}
		if _, ok := expectedPtyRunnerOnly.Events[ev]; ok {
			continue
		}
		out = append(out, fmt.Sprintf(
			"ptyrunner emitted event type %q which is not in the cross-runner intersection and not in expectedPtyRunnerOnly.Events; either add it to that table with a citation comment, or file a follow-up ticket to align ptyrunner's emitter",
			ev))
	}
	for f := range streamFields {
		if _, ok := ptyFields[f]; ok {
			continue
		}
		if _, ok := expectedStreamRunnerOnly.ResultTrailerFields[f]; ok {
			continue
		}
		out = append(out, fmt.Sprintf(
			"streamrunner result-trailer field %q is not in the cross-runner intersection and not in expectedStreamRunnerOnly.ResultTrailerFields; either add it to that table with a citation comment (#503 or audit doc), or treat the divergence as a real bug",
			f))
	}
	for f := range ptyFields {
		if _, ok := streamFields[f]; ok {
			continue
		}
		if _, ok := expectedPtyRunnerOnly.ResultTrailerFields[f]; ok {
			continue
		}
		out = append(out, fmt.Sprintf(
			"ptyrunner result-trailer field %q is not in the cross-runner intersection and not in expectedPtyRunnerOnly.ResultTrailerFields; either add it to that table with a citation comment, or file a follow-up ticket to align ptyrunner's emitter",
			f))
	}
	return out
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
		model  = "claude-haiku-4-5"
		effort = "low"
		// Loose backstop, not a target. On claude 2.1.158 even the one-word
		// "OK" reply spends a thinking message plus the text message, and the
		// interrupt-runner budget counts every assistant message, so a cap of
		// 1 tripped it before completion while the other runner finished. Both
		// runners complete the reply far under this. See Lessons 2026-06-03.
		maxTurns = 6
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
			AllowedTools: allowedTools,
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

	// Additive-drift check: shape comparison catches ptyrunner SHRINKING
	// relative to streamrunner but not either side GROWING new event types
	// or result-trailer fields. t.Error (not t.Fatal) so the field-level
	// invariants below still run and all signals surface in one test output.
	for _, v := range additiveDriftViolations(streamOut.Bytes(), ptyOut.Bytes()) {
		t.Error(v)
	}

	// Field-level invariants 7-10. Errorf (not Fatalf) so all four run.
	checkInit(t, "streamrunner", streamOut.Bytes(), workdirStream, model, allowedTools)
	checkInit(t, "ptyrunner", ptyOut.Bytes(), realpath, model, allowedTools)
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

// checkInit decodes the first system/init line and asserts the load-bearing
// required-field set (type, subtype, cwd, tools, model, session_id) matches
// the values stamped into it by the runner. Asserts session_id is non-empty
// — the parseInitSessionID contract.
func checkInit(t *testing.T, side string, stream []byte, wantCwd, wantModel string, wantTools []string) {
	t.Helper()
	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env struct {
			Type      string   `json:"type"`
			Subtype   string   `json:"subtype"`
			Cwd       string   `json:"cwd"`
			Tools     []string `json:"tools"`
			Model     string   `json:"model"`
			SessionID string   `json:"session_id"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Type == "system" && env.Subtype == "init" {
			if env.Cwd != wantCwd {
				t.Errorf("%s init.cwd = %q, want %q", side, env.Cwd, wantCwd)
			}
			if env.Model != wantModel {
				t.Errorf("%s init.model = %q, want %q", side, env.Model, wantModel)
			}
			if env.SessionID == "" {
				t.Errorf("%s init.session_id is empty", side)
			}
			if !reflect.DeepEqual(env.Tools, wantTools) {
				t.Errorf("%s init.tools = %v, want %v", side, env.Tools, wantTools)
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

// TestAdditiveDriftAssertion_SelfCheck regression-tests the additive-drift
// assertion logic against hand-crafted byte fixtures. Runs under the
// e2e_realclaude build tag (the whole file is gated) but does NOT require
// the real claude CLI or any network — the fixtures are inline string
// constants. Together the sub-tests cover the cross-product of
// (event-type, result-field) × (streamrunner-side, ptyrunner-side) ×
// (drift, no-drift).
func TestAdditiveDriftAssertion_SelfCheck(t *testing.T) {
	const intersectionBaseline = `{"type":"system","subtype":"init","cwd":"/tmp","tools":[],"model":"claude-haiku-4-5","session_id":"abc"}
{"type":"user","content":"hi"}
{"type":"assistant","content":"hi"}
{"type":"result","subtype":"success","is_error":false,"duration_ms":100,"num_turns":1,"session_id":"abc","total_cost_usd":0.0,"usage":{}}
`

	inject := func(t *testing.T, stream, target, replacement string) []byte {
		t.Helper()
		out := strings.Replace(stream, target, replacement, 1)
		if out == stream {
			t.Fatalf("inject: target %q not found in fixture", target)
		}
		return []byte(out)
	}

	t.Run("intersection_match_no_drift", func(t *testing.T) {
		vs := additiveDriftViolations([]byte(intersectionBaseline), []byte(intersectionBaseline))
		if len(vs) != 0 {
			t.Errorf("expected no violations, got %d:\n%s", len(vs), strings.Join(vs, "\n"))
		}
	})

	t.Run("streamrunner_extra_event_type", func(t *testing.T) {
		const extra = `{"type":"synthetic_extra_event","subtype":"x"}` + "\n"
		streamRaw := []byte(extra + intersectionBaseline)
		ptyRaw := []byte(intersectionBaseline)
		vs := additiveDriftViolations(streamRaw, ptyRaw)
		if len(vs) != 1 {
			t.Fatalf("expected 1 violation, got %d:\n%s", len(vs), strings.Join(vs, "\n"))
		}
		if !strings.Contains(vs[0], "synthetic_extra_event") {
			t.Errorf("violation does not name synthetic identifier: %q", vs[0])
		}
		if !strings.Contains(vs[0], "expectedStreamRunnerOnly.Events") {
			t.Errorf("violation does not name expectedStreamRunnerOnly.Events: %q", vs[0])
		}
	})

	t.Run("ptyrunner_extra_event_type", func(t *testing.T) {
		const extra = `{"type":"synthetic_pty_event","subtype":"x"}` + "\n"
		streamRaw := []byte(intersectionBaseline)
		ptyRaw := []byte(extra + intersectionBaseline)
		vs := additiveDriftViolations(streamRaw, ptyRaw)
		if len(vs) != 1 {
			t.Fatalf("expected 1 violation, got %d:\n%s", len(vs), strings.Join(vs, "\n"))
		}
		if !strings.Contains(vs[0], "synthetic_pty_event") {
			t.Errorf("violation does not name synthetic identifier: %q", vs[0])
		}
		if !strings.Contains(vs[0], "expectedPtyRunnerOnly.Events") {
			t.Errorf("violation does not name expectedPtyRunnerOnly.Events: %q", vs[0])
		}
	})

	t.Run("streamrunner_extra_result_field", func(t *testing.T) {
		streamRaw := inject(t, intersectionBaseline, `"usage":{}`, `"usage":{},"synthetic_stream_field":"x"`)
		ptyRaw := []byte(intersectionBaseline)
		vs := additiveDriftViolations(streamRaw, ptyRaw)
		if len(vs) != 1 {
			t.Fatalf("expected 1 violation, got %d:\n%s", len(vs), strings.Join(vs, "\n"))
		}
		if !strings.Contains(vs[0], "synthetic_stream_field") {
			t.Errorf("violation does not name synthetic identifier: %q", vs[0])
		}
		if !strings.Contains(vs[0], "expectedStreamRunnerOnly.ResultTrailerFields") {
			t.Errorf("violation does not name expectedStreamRunnerOnly.ResultTrailerFields: %q", vs[0])
		}
	})

	t.Run("ptyrunner_extra_result_field", func(t *testing.T) {
		streamRaw := []byte(intersectionBaseline)
		ptyRaw := inject(t, intersectionBaseline, `"usage":{}`, `"usage":{},"synthetic_pty_field":"x"`)
		vs := additiveDriftViolations(streamRaw, ptyRaw)
		if len(vs) != 1 {
			t.Fatalf("expected 1 violation, got %d:\n%s", len(vs), strings.Join(vs, "\n"))
		}
		if !strings.Contains(vs[0], "synthetic_pty_field") {
			t.Errorf("violation does not name synthetic identifier: %q", vs[0])
		}
		if !strings.Contains(vs[0], "expectedPtyRunnerOnly.ResultTrailerFields") {
			t.Errorf("violation does not name expectedPtyRunnerOnly.ResultTrailerFields: %q", vs[0])
		}
	})

	t.Run("allowlisted_streamrunner_field_does_not_fire", func(t *testing.T) {
		// streamrunner carries an allowlisted entry (api_error_status);
		// ptyrunner does not. Allowlist absorbs the asymmetry → zero
		// violations. Sanity check the allowlist actually allowlists.
		streamRaw := inject(t, intersectionBaseline, `"usage":{}`, `"usage":{},"api_error_status":"ok"`)
		ptyRaw := []byte(intersectionBaseline)
		vs := additiveDriftViolations(streamRaw, ptyRaw)
		if len(vs) != 0 {
			t.Errorf("expected no violations (api_error_status is allowlisted), got %d:\n%s",
				len(vs), strings.Join(vs, "\n"))
		}
	})
}
