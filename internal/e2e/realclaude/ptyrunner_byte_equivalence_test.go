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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun/ptyrunner"
	"github.com/pyrycode/pyrycode/internal/agentrun/settings"
	"github.com/pyrycode/pyrycode/internal/agentrun/streamrunner"
	"github.com/pyrycode/pyrycode/internal/agentrun/trust"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
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
//
// Version-bump note (#1218, 2026-07-28, claude 2.1.220). The three fields
// citing #1218 below were SURFACED by that ticket, not caused by it: the shape
// comparison used to t.Fatalf on the system-family mismatch before the
// additive-drift loop ever ran, so a claude release adding result-trailer
// fields stayed masked. They are one-sided BY CONSTRUCTION, not by judgement —
// ptyrunner's trailer is a fixed Go struct (internal/agentrun/streamjson
// emitter.go, type trailer), so it cannot emit a field claude adds on the API
// side. Each is a sibling of a field the #503 audit already classified
// API-only, and none is read by agent-dispatcher.
var expectedStreamRunnerOnly = additiveDriftAllowlist{
	Events: map[string]struct{}{
		"rate_limit_event": {}, // #503 audit 2026-05-23: API-stream event, structurally unreachable from claude's local JSONL (which ptyrunner tails). Dispatcher default-case preview log only — no semantic consumption.
	},
	ResultTrailerFields: map[string]struct{}{
		"api_error_status":          {}, // #503 audit 2026-05-23: API-only (claude's HTTP layer); dispatcher never reads it.
		"duration_api_ms":           {}, // #503 audit 2026-05-23: API-only; dispatcher never reads it.
		"fast_mode_disabled_reason": {}, // #1218 2026-07-28: claude 2.1.220 API-only telemetry, sibling of fast_mode_state. See the version-bump note above.
		"fast_mode_state":           {}, // #503 audit 2026-05-23: API-only; dispatcher never reads it.
		"modelUsage":                {}, // #503 audit 2026-05-23: API-only (per-model breakdown); dispatcher never reads it.
		"permission_denials":        {}, // #503 audit 2026-05-23: API-only (claude's gate result); dispatcher never reads it.
		"time_to_request_ms":        {}, // #1218 2026-07-28: claude 2.1.220 API-only timing, sibling of duration_api_ms. See the version-bump note above.
		"ttft_ms":                   {}, // #503 audit 2026-05-23: API-only (time to first token); dispatcher never reads it.
		"ttft_stream_ms":            {}, // #1218 2026-07-28: claude 2.1.220 API-only timing, sibling of ttft_ms. See the version-bump note above.
		"uuid":                      {}, // #503 audit 2026-05-23: API-side session UUID, distinct from session_id (which dispatcher does read); duplicate signal.
	},
}

// expectedPtyRunnerOnly is the inverse table — event types and top-level
// result-trailer fields ptyrunner emits but streamrunner does not. ptyrunner
// synthesises its stream from claude's local session JSONL, which carries
// interactive housekeeping envelopes (and the user-turn echo) that
// streamrunner's raw claude stdout never emits. This table is the single
// source of truth for those one-sided emissions: additiveDriftViolations
// tolerates them in the SET check, and extractShapes drops them so the
// SEQUENCE comparison sees only the cross-runner intersection.
var expectedPtyRunnerOnly = additiveDriftAllowlist{
	Events: map[string]struct{}{
		"permission-mode":       {}, // #503 audit 2026-05-23: claude local-JSONL housekeeping envelope. Dispatcher default-case preview log only.
		"file-history-snapshot": {}, // #503 audit 2026-05-23: ditto.
		"skill_listing":         {}, // #503 audit 2026-05-23: ditto.
		"ai-title":              {}, // #503 audit 2026-05-23: ditto.
		"mode":                  {}, // #562 2026-06-06: claude 2.1.158 interactive housekeeping envelope (session mode). Dispatcher default-case preview log only.
		"attachment":            {}, // #562 2026-06-06: claude 2.1.158 interactive envelope (prompt attachments). Dispatcher default-case preview log only.
		"user":                  {}, // #562 2026-06-06: ptyrunner echoes the user turn from the session JSONL; streamrunner's raw stdout never echoes stdin input. User-prompt content is still asserted on the ptyrunner side via checkUserContains.
	},
	ResultTrailerFields: map[string]struct{}{},
}

// parserMappedSubtypes is the set of subtypes the shipped parser MAPS out of an
// otherwise-dropped top-level type — the EXCEPTIONS that escape that type's
// drop, never the drops themselves. nil means no exceptions: the whole type is
// dropped.
type parserMappedSubtypes map[string]struct{}

// parserIgnoredTypes mirrors the shipped internal/streamsup parser's EFFECTIVE
// drop behaviour: which stream-json lines it consumes in silence, mapping
// nothing. It does NOT mirror the SHAPE of streamsup.ignoredLineTypes — read as
// a table-shape claim it would point at the wrong file, since that table is
// top-level-keyed and deliberately stays that way (see below).
//
// GRANULARITY. Keyed by top-level type; each value is the set of subtypes the
// parser MAPS out of that type. That reproduces the parser's own shape — the
// top-level gate first, then the mapped subtypes carved out from INSIDE it. The
// ignored side stays an OPEN set: a subtype in no table at all is DROPPED, and a
// subtype escapes only by being enumerated. Enumerating the ignored side instead
// would invert that default and let every unwritten `system` subtype through.
// streamsup.emitSystemSubtype's case arms are the one enumeration site.
//
// WHY IT IS NO LONGER TOP-LEVEL-ONLY. Since #1380/#1381 the parser maps three
// `system` subtypes into background-task events. A top-level `system` key drops
// those mapped envelopes from the sequence comparison along with the ignored
// ones, so the two runners could diverge on any of the three with this gate
// green (#1379).
//
// WHY streamsup.ignoredLineTypes NONETHELESS STAYS TOP-LEVEL-KEYED. Different
// table, different job. Keeping `system` whole on that list is what makes the
// parser's emitUnrecognized structurally unreachable from any system line
// whatever its subtype; subtype granularity there would put unknown `system`
// subtypes back on the unrecognized lane and break the live zero-unrecognized
// gate. This mirror carries no such job — it only decides what is worth
// comparing — so the two tables can differ in shape while agreeing on behaviour.
//
// WHY THIS IS A THIRD TABLE, AND WHY THE TWO ONE-SIDED TABLES ABOVE STAY
// TOP-LEVEL-KEYED. Those answer "is this one-sided emission tolerated?" and
// govern the SET check as well as the sequence filter; this one answers "does
// the shipped parser map anything out of this line?" and governs the SEQUENCE
// filter ONLY. One-sidedness is a property of the TYPE, so subtype granularity
// would buy those tables nothing. A member here is dropped from the sequence
// comparison and stays fully policed by additiveDriftViolations — so a genuinely
// one-sided divergence (one runner stops emitting system/init while the other
// still does) still fires (#1218). Putting `system` in either one-sided table
// instead would be factually false — both runners emit system/init — and,
// because additiveDriftViolations reads those same tables, would pre-authorise
// exactly that future divergence.
//
// TestParserIgnoredTypesMatchesStreamsupParser pins the mirror against the real
// parser in both directions, and that pin is what makes restating the mapped set
// here legitimate where the prose comments in this package must merely point at
// streamsup.emitSystemSubtype: the enumeration site is unexported and in another
// package, so this mirror necessarily duplicates it — but unlike a comment, this
// duplicate fails a build when it drifts. map[string]struct{} for the inner set
// rather than parser.go's map[string]bool, to match the surrounding file; the
// mirrored content is the KEY SET at both levels.
var parserIgnoredTypes = map[string]parserMappedSubtypes{
	// #1218: claude's catch-all namespace and its highest-rate emitter (~10
	// thinking_tokens/turn vs exactly 1 init/turn, measured 2026-07-27) — the
	// measurement that makes a silent drop the right default for every subtype not
	// carved out below. #1379: the parser no longer drops the family wholesale, so
	// the carve-out is enumerated here; every other subtype, named or never seen,
	// is still dropped. Enumeration site: streamsup.emitSystemSubtype.
	//
	// CORRECTED 2026-08-09 (#1385): thinking_tokens is now carved out too, so the
	// ~10/turn rate above is no longer the reason it is DROPPED — it is the reason
	// its mapping is RATE-BOUNDED (streamsup.minThinkingTokensPerEvent), which is a
	// statement about how many events the parser emits, not about whether it maps
	// the line. The rate still argues for the silent-drop default on every subtype
	// NOT carved out, which is what the paragraph above says and what stands.
	// Carving it out has a consequence the other three do not:
	// expectedStreamRunnerOnlySubtypes below, without which the live sequence
	// comparison goes RED.
	"system": {"task_started": {}, "task_updated": {}, "background_tasks_changed": {}, "thinking_tokens": {}},
	// #1218: parser-ignored whole — nil, i.e. no mapped subtypes, no exceptions.
	// Also in expectedStreamRunnerOnly.Events — independently true (streamrunner
	// emits it, ptyrunner cannot) and that entry still governs the SET check.
	// shapeFilterDrops consults the one-sided tables first, so carrying it twice
	// does not change the filter.
	"rate_limit_event": nil,
}

// parserDropsShape reports whether the shipped streamsup parser drops a line of
// this top-level type and subtype in silence. Mirrors consumeLine's drop branch:
// the top-level gate first, then the mapped-subtype carve-out from inside it. A
// subtype in no table at all is DROPPED — the ignored side is an open set.
func parserDropsShape(typ, subtype string) bool {
	mapped, ignored := parserIgnoredTypes[typ]
	if !ignored {
		return false
	}
	_, isMapped := mapped[subtype]
	return !isMapped
}

// expectedStreamRunnerOnlySubtypes enumerates (top-level type, subtype) pairs
// that streamrunner emits but ptyrunner does not, and which are accepted as
// documented one-sided emissions rather than drift. Consulted by
// shapeFilterDrops ONLY — it governs the SEQUENCE filter and nothing else.
//
// WHY IT IS NOT A FIELD ON expectedStreamRunnerOnly. That struct is read by
// additiveDriftViolations, which governs the SET check — the check whose whole
// job is to catch a one-sided divergence. Widening it would pre-authorise, in
// that check, exactly the class of divergence it exists to fire on. It would
// also be factually false at the granularity that struct works at: it is keyed
// by TYPE, both runners emit system/init, so putting `system` in it would
// mis-state the runners' behaviour and silently tolerate a future one-sided
// system divergence. additiveDriftViolations compares top-level types only, and
// `system` is emitted by both runners, so keeping this table out of it loses
// nothing.
//
// WHY IT IS NOT AN ENTRY IN parserIgnoredTypes. That table states a fact about
// the SHIPPED PARSER — "does it map anything out of this line?" — and since
// #1385 the answer for system/thinking_tokens is YES. This table states a fact
// about the TWO RUNNERS — "do both of them emit this?" — and the answer is NO.
// Two different facts; conflating them would force a false statement into one
// table or the other. Before #1385 they agreed by accident (the parser dropped
// the subtype AND ptyrunner never emitted it), so one table could carry both
// jobs; mapping the subtype is what pulled them apart.
//
// THE MEASUREMENT, so a future reader re-checks it rather than inheriting it:
// #1218, 2026-07-28, claude 2.1.220 — streamrunner emits system/thinking_tokens
// ~10 times per turn, ptyrunner exactly 0 (ptyrunner synthesises its stream from
// claude's local session JSONL, which carries no thinking_tokens record).
// Without this table TestPtyRunnerVsStreamRunner_StructuralEquivalence compares
// ~10 streamrunner shapes against zero on the ptyrunner side and fails on the
// live pre-ship gate — which is a real-claude run's worth of wall clock to
// discover. TestShapeFilterKeepsThinkingTokensOutOfTheSequence is the offline
// pin that fires first.
//
// The failure direction is QUIET: if a future claude release makes ptyrunner
// emit the subtype too, this table becomes a false statement that hides a real
// divergence instead of tolerating a known one. Re-measure whenever the
// byte-equivalence suite is re-baselined against a new claude version.
var expectedStreamRunnerOnlySubtypes = map[string]map[string]struct{}{
	"system": {"thinking_tokens": {}}, // #1218 2026-07-28: streamrunner ~10/turn, ptyrunner exactly 0.
}

// streamRunnerOnlySubtype reports whether (typ, subtype) is a documented
// one-sided streamrunner emission at subtype granularity.
func streamRunnerOnlySubtype(typ, subtype string) bool {
	subtypes, ok := expectedStreamRunnerOnlySubtypes[typ]
	if !ok {
		return false
	}
	_, oneSided := subtypes[subtype]
	return oneSided
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
// extractShapes reads ONLY `type` + `subtype`, and drops both the one-sided
// allowlisted types and the lines the shipped parser drops in silence (see
// shapeFilterDrops) so the compared sequence is the cross-runner intersection
// over the alphabet the shipped parser actually maps. Field-level invariants (init.cwd / .tools /
// .model / .session_id, user prompt text, result.is_error / result.num_turns)
// are asserted via targeted decodes below; this struct never materialises a
// normalised form.
type envelopeShape struct {
	Type    string
	Subtype string
}

// shapeFilterDrops reports whether extractShapes drops this (type, subtype) from
// the compared sequence, so that compareShapes sees only the cross-runner
// intersection sequence over the alphabet production actually consumes. Four
// tables feed it, answering three different questions.
//
// The two additive-drift event allowlists (the envelope types one runner emits
// but the other does not) are looked up by TYPE ALONE, and that is correct:
// they answer "is this one-sided emission tolerated?", one-sidedness is a
// property of the type at that granularity, and they govern the SET check as
// well as this filter.
//
// parserIgnoredTypes answers a different question — "does the shipped parser map
// anything out of this line?" — and that one has a subtype-dependent answer, so
// it is the table consulted with the subtype.
//
// expectedStreamRunnerOnlySubtypes (#1385) answers the FIRST question again but
// at SUBTYPE granularity: "is this one-sided emission tolerated, for a type whose
// one-sidedness is not a property of the whole type?" It exists because
// system/thinking_tokens is one-sided while system/init is not, so no
// type-keyed table can state the fact without lying. Unlike the two allowlists
// it governs this filter ONLY, never the SET check — see its doc for why
// widening additiveDriftViolations' input would defeat that check's purpose.
//
// Keeping the allowlists as the single source of truth for TYPE-level
// one-sidedness means a new un-allowlisted one-sided type is still caught
// independently by additiveDriftViolations (the SET check), not silently
// swallowed here — and that holds for parser-ignored types too, since
// additiveDriftViolations does not consult parserIgnoredTypes.
func shapeFilterDrops(typ, subtype string) bool {
	if _, ok := expectedPtyRunnerOnly.Events[typ]; ok {
		return true
	}
	if _, ok := expectedStreamRunnerOnly.Events[typ]; ok {
		return true
	}
	if streamRunnerOnlySubtype(typ, subtype) {
		return true
	}
	return parserDropsShape(typ, subtype)
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
		if shapeFilterDrops(env.Type, env.Subtype) {
			continue
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
// (type,subtype) sequences and asserted equal element-by-element AFTER the
// one-sided allowlisted types and the parser-ignored types are filtered out
// (extractShapes), so the comparison is the cross-runner intersection over the
// alphabet the shipped parser maps. Field-level invariants pin
// dispatcher-visible content the shape comparison does not cover: init
// cwd/model/session_id and tools-containment (both runners), the user prompt
// text (ptyrunner only — streamrunner does not echo stdin), and result
// is_error / num_turns / subtype. See envelopeShape and expectedPtyRunnerOnly
// for the divergence rationale (#503, #562).
//
// Test gating: WithWorktreeAuthenticated skips when ANTHROPIC_API_KEY is
// absent. CI without the key skips cleanly; the argv-shape test in the same
// file still runs.
//
// Wall-clock: ~30-60s (two 5-15s real-claude turns). Both runs use Haiku 4.5
// at low effort with a loose MaxTurns backstop to minimise LLM stochasticity.
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
	// Only ptyrunner echoes the user prompt to stdout — it synthesises the
	// stream from claude's session JSONL, which records the user turn.
	// streamrunner forwards claude's raw stdout, where the prompt is stdin
	// input and never echoed, so there is no user envelope to check (#562).
	checkUserContains(t, "ptyrunner", ptyOut.Bytes(), promptText)
	streamResult := decodeResultTrailer(t, "streamrunner", streamOut.Bytes())
	ptyResult := decodeResultTrailer(t, "ptyrunner", ptyOut.Bytes())
	if streamResult.IsError != ptyResult.IsError {
		t.Errorf("result.is_error mismatch: streamrunner=%v ptyrunner=%v",
			streamResult.IsError, ptyResult.IsError)
	}
	// Both runners must report claude's native logical-turn count: streamrunner
	// forwards it from claude's own result envelope, and ptyrunner's emitter
	// groups consecutive assistant JSONL entries by message.id so a reply claude
	// splits across a thinking line + a text line counts as one turn (#573). The
	// floor guard keeps a 0 == 0 "both wedged to the idle-stall result" outcome
	// failing loudly.
	if ptyResult.NumTurns < 1 {
		t.Errorf("ptyrunner result.num_turns = %d, want >= 1 (0 = wedge/empty result)", ptyResult.NumTurns)
	}
	if streamResult.NumTurns != ptyResult.NumTurns {
		t.Errorf("result.num_turns mismatch: streamrunner=%d ptyrunner=%d (both must report claude's native logical-turn count)",
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

	// Floor guard. Two empty slices are reflect.DeepEqual, so without a floor a
	// turn where BOTH runners produced nothing passes vacuously; it also guards
	// the index accesses below. >= 2 rather than >= 3 deliberately: the
	// two-assistant split (a thinking message plus a text message) is
	// claude-version-dependent — see the maxTurns comment above — so a higher
	// floor would pin a version artifact. assistant+result is the durable floor
	// of the post-filter intersection (#1218).
	if len(stream) < 2 {
		fail(fmt.Sprintf("streamrunner produced %d shapes, want >= 2 (assistant+result)", len(stream)))
	}
	if len(pty) < 2 {
		fail(fmt.Sprintf("ptyrunner produced %d shapes, want >= 2 (assistant+result)", len(pty)))
	}
	// The post-filter intersection opens with model output: system/init and the
	// ptyrunner-only user echo are both filtered out. Catches a turn whose first
	// meaningful envelope is a bare result — no model output at all — with a
	// sharper message than the floor guard gives. init's own field-level
	// invariants are asserted from the RAW bytes by checkInit, independent of
	// this sequence (#1218).
	if stream[0].Type != "assistant" {
		fail(fmt.Sprintf("streamrunner[0] = (%q,%q), want type=assistant (first post-filter envelope should be model output)", stream[0].Type, stream[0].Subtype))
	}
	if pty[0].Type != "assistant" {
		fail(fmt.Sprintf("ptyrunner[0] = (%q,%q), want type=assistant (first post-filter envelope should be model output)", pty[0].Type, pty[0].Subtype))
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
// required fields (type, subtype, cwd, model, session_id) match the values
// stamped in by the runner, plus that init.tools CONTAINS each allowed tool.
// tools is a containment check, not equality, because the two runners gate
// tools differently: streamrunner uses --dangerously-skip-permissions and
// claude reports the full tool registry, while ptyrunner uses a dontAsk
// settings file and claude reports only the allowed subset (#562). The
// dispatcher does not consume init.tools; the meaningful guarantee is that
// each allowed tool is present. Asserts session_id is non-empty — the
// parseInitSessionID contract.
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
			// claude reports cwd as the resolved realpath. On macOS the test
			// tempdir lives under /tmp, a symlink to /private/tmp, so resolve
			// wantCwd before comparing or the streamrunner side (which passes
			// the un-resolved workdir) mismatches (#562).
			wantCwdResolved := wantCwd
			if r, err := filepath.EvalSymlinks(wantCwd); err == nil {
				wantCwdResolved = r
			}
			if env.Cwd != wantCwdResolved {
				t.Errorf("%s init.cwd = %q, want %q", side, env.Cwd, wantCwdResolved)
			}
			if env.Model != wantModel {
				t.Errorf("%s init.model = %q, want %q", side, env.Model, wantModel)
			}
			if env.SessionID == "" {
				t.Errorf("%s init.session_id is empty", side)
			}
			for _, want := range wantTools {
				if !slices.Contains(env.Tools, want) {
					t.Errorf("%s init.tools = %v, does not contain allowed tool %q", side, env.Tools, want)
				}
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

	// The two arms below are the #1218 pin: filtering `system` out of the
	// SEQUENCE comparison must buy no silence in the SET check. They fail if a
	// future contributor "fixes" a system-family mismatch by adding an entry to
	// either one-sided table instead of to parserIgnoredTypes — the entry would
	// make additiveDriftViolations tolerate a genuinely one-sided system
	// divergence, which these arms then no longer see.
	//
	// systemInitLine must stay byte-identical to the init line in
	// intersectionBaseline; inject t.Fatalf's on a miss, so drift is loud.
	const systemInitLine = `{"type":"system","subtype":"init","cwd":"/tmp","tools":[],"model":"claude-haiku-4-5","session_id":"abc"}` + "\n"

	t.Run("system_one_sided_streamrunner", func(t *testing.T) {
		// ptyrunner stops emitting system/init entirely; streamrunner still does.
		streamRaw := []byte(intersectionBaseline)
		ptyRaw := inject(t, intersectionBaseline, systemInitLine, "")
		vs := additiveDriftViolations(streamRaw, ptyRaw)
		if len(vs) != 1 {
			t.Fatalf("expected 1 violation, got %d:\n%s", len(vs), strings.Join(vs, "\n"))
		}
		if !strings.Contains(vs[0], `"system"`) {
			t.Errorf("violation does not name the system type: %q", vs[0])
		}
		if !strings.Contains(vs[0], "expectedStreamRunnerOnly.Events") {
			t.Errorf("violation does not name expectedStreamRunnerOnly.Events: %q", vs[0])
		}
	})

	t.Run("system_one_sided_ptyrunner", func(t *testing.T) {
		// The inverse: streamrunner stops emitting system/init, ptyrunner still does.
		streamRaw := inject(t, intersectionBaseline, systemInitLine, "")
		ptyRaw := []byte(intersectionBaseline)
		vs := additiveDriftViolations(streamRaw, ptyRaw)
		if len(vs) != 1 {
			t.Fatalf("expected 1 violation, got %d:\n%s", len(vs), strings.Join(vs, "\n"))
		}
		if !strings.Contains(vs[0], `"system"`) {
			t.Errorf("violation does not name the system type: %q", vs[0])
		}
		if !strings.Contains(vs[0], "expectedPtyRunnerOnly.Events") {
			t.Errorf("violation does not name expectedPtyRunnerOnly.Events: %q", vs[0])
		}
	})
}

// TestExtractShapes_FiltersParserIgnoredTypes pins the filter behaviour #1218
// added, by fixture rather than by a live turn: the parser-ignored family is
// dropped from the compared sequence, stream order among the survivors is
// preserved, and nothing else is dropped. Needs no claude and no network.
func TestExtractShapes_FiltersParserIgnoredTypes(t *testing.T) {
	// Mirrors the observed 2026-07-27 streamrunner side of the RED run: one
	// init, a burst of thinking_tokens, the model output, the once-per-run
	// rate_limit_event, then the result trailer. Two #1379 additions carry the
	// subtype granularity: a MAPPED system subtype, which must survive, and a
	// subtype in no table at all, which must not.
	const stream = `{"type":"system","subtype":"init","session_id":"abc"}
{"type":"system","subtype":"thinking_tokens"}
{"type":"system","subtype":"thinking_tokens"}
{"type":"system","subtype":"task_started"}
{"type":"system","subtype":"some_unmapped_future_thing"}
{"type":"assistant","message":{"id":"m1"}}
{"type":"rate_limit_event"}
{"type":"result","subtype":"success","is_error":false}
`

	shapes, err := extractShapes([]byte(stream))
	if err != nil {
		t.Fatalf("extractShapes: %v", err)
	}
	want := []envelopeShape{
		{Type: "system", Subtype: "task_started"},
		{Type: "assistant"},
		{Type: "result", Subtype: "success"},
	}
	if !reflect.DeepEqual(shapes, want) {
		t.Fatalf("extractShapes =\n%swant:\n%s", formatShapes(shapes), formatShapes(want))
	}
	// Belt-and-braces on the drop rule, and specifically on its OPEN-SET default:
	// keying the filter on a composite type/subtype would invert that default and
	// leave every `system` subtype nobody wrote down standing — system/thinking_tokens
	// alone fires ~10x/turn, so that failure is noisy as well as wrong. The
	// never-seen subtype in the fixture above is what trips this loop if it ever
	// happens. (Until #1379 this comment claimed the opposite, that a
	// subtype-grained filter was itself the mistake; subtype granularity is now the
	// requirement — what must stay type-grained is the DEFAULT, not the lookup. The
	// mapped set is enumerated at streamsup.emitSystemSubtype.)
	for i, sh := range shapes {
		if parserDropsShape(sh.Type, sh.Subtype) {
			t.Errorf("shape[%d] = (%q,%q) survived the filter but the shipped parser drops that type/subtype in silence",
				i, sh.Type, sh.Subtype)
		}
	}

	// Negative arm: the filter is a denylist, not an allowlist of survivors. A
	// type in neither the one-sided tables nor parserIgnoredTypes must come
	// through — otherwise additiveDriftViolations would be the only thing left
	// watching new envelope types, and the sequence check would go quietly
	// blind to them.
	t.Run("unknown_type_survives", func(t *testing.T) {
		const withUnknown = `{"type":"system","subtype":"init","session_id":"abc"}
{"type":"synthetic_unknown_event","subtype":"x"}
{"type":"result","subtype":"success"}
`
		shapes, err := extractShapes([]byte(withUnknown))
		if err != nil {
			t.Fatalf("extractShapes: %v", err)
		}
		want := []envelopeShape{
			{Type: "synthetic_unknown_event", Subtype: "x"},
			{Type: "result", Subtype: "success"},
		}
		if !reflect.DeepEqual(shapes, want) {
			t.Fatalf("extractShapes =\n%swant:\n%s", formatShapes(shapes), formatShapes(want))
		}
	})
}

// TestShapeFilterKeepsThinkingTokensOutOfTheSequence pins the one place where
// two of shapeFilterDrops' tables deliberately DISAGREE, and it is the only
// assertion in this package that fires if someone "simplifies"
// expectedStreamRunnerOnlySubtypes away.
//
// The two answers are opposite ON PURPOSE:
//
//   - parserDropsShape says FALSE — the shipped parser MAPS system/thinking_tokens
//     since #1385 (→ turnevent.ThinkingProgress). Saying otherwise would be the
//     "dangerous direction" TestParserIgnoredTypesMatchesStreamsupParser's doc
//     names: a mirror claiming a drop the parser does not perform.
//   - shapeFilterDrops says TRUE — the SEQUENCE comparison must still exclude it,
//     because #1218 measured the subtype one-sided (streamrunner ~10/turn,
//     ptyrunner exactly 0, claude 2.1.220, 2026-07-28).
//
// Removing either half re-opens a RED on the LIVE gate
// (TestPtyRunnerVsStreamRunner_StructuralEquivalence, in make e2e-realclaude and
// therefore in make preship): ~10 streamrunner shapes compared against zero.
// That costs a real-claude run to discover, which is why this offline pin exists.
// Verified by construction while building #1385: with the mapped-set entry added
// and this table absent, TestExtractShapes_FiltersParserIgnoredTypes goes RED
// with both thinking_tokens lines surviving the filter.
//
// Needs no claude and no network.
func TestShapeFilterKeepsThinkingTokensOutOfTheSequence(t *testing.T) {
	if parserDropsShape("system", "thinking_tokens") {
		t.Error("parserDropsShape(system/thinking_tokens) = true, want false — the shipped parser MAPS this subtype (streamsup.emitSystemSubtype); a mirror claiming it is dropped hides a real cross-runner divergence from the sequence comparison")
	}
	if !shapeFilterDrops("system", "thinking_tokens") {
		t.Error("shapeFilterDrops(system/thinking_tokens) = false, want true — the subtype is one-sided (#1218: streamrunner ~10/turn, ptyrunner 0), so letting it into the compared sequence turns the live structural-equivalence gate RED")
	}

	// The disagreement must be NARROW: a sibling system subtype that both runners
	// emit stays outside expectedStreamRunnerOnlySubtypes, so a broadened table
	// (say, one keyed by type alone) fails here rather than silently blinding the
	// sequence check to every system envelope.
	if streamRunnerOnlySubtype("system", "init") {
		t.Error("streamRunnerOnlySubtype(system/init) = true, want false — both runners emit system/init; tolerating it as one-sided would blind the sequence comparison to a real divergence")
	}
}

// parserIgnoredTypeFixtures maps each parserIgnoredTypes member to
// representative wire lines, fed to a real streamsup.Parser by
// TestParserIgnoredTypesMatchesStreamsupParser. A table member with no fixture
// here fails that test, and so does a MAPPED subtype no fixture line declares —
// so growing either level of the mirror forces adding a line to exercise it.
//
// Each line's expectation is DERIVED, not declared: the test decodes the
// subtype and asks parserDropsShape. Dropped lines must emit zero events, mapped
// lines at least one. The mapped fixtures are deliberately bare `type` +
// `subtype` — each emits exactly one event on the shipped parser (measured
// 2026-08-08), and every field beyond those two is a chance to trip the
// emitters' undecodable arm, which returns consumed-with-zero-events and would
// make the fixture prove nothing.
//
// thinking_tokens is a DELIBERATE EXCEPTION to that convention (#1385), and it
// carries a payload on purpose. The convention rests on the three background-task
// subtypes emitting UNCONDITIONALLY once decoded, so for them the bare line is
// both minimal and sufficient. thinking_tokens is mapped by a RATE-BOUNDED rule:
// it emits only once claude's accumulated estimated_tokens_delta crosses
// streamsup.minThinkingTokensPerEvent, so a payload-free line emits ZERO — which
// is not a defect to work around but a required property of the mapping
// (streamsup's TestParser_ThinkingProgressSilentWithoutPayload pins it, and the
// daemon must make no liveness claim for a line carrying no evidence of
// thinking). Leaving the bare line here while listing the subtype as mapped
// therefore drives TestParserIgnoredTypesMatchesStreamsupParser RED on its "want
// at least 1" arm — verified, not predicted. The delta is 16x the bound rather
// than just over it, so a later change to the bound cannot silently un-arm this
// fixture. The keys are the committed capture's; no field structure is invented.
var parserIgnoredTypeFixtures = map[string][]string{
	"system": {
		`{"type":"system","subtype":"init","session_id":"abc","cwd":"/tmp","model":"claude-haiku-4-5","tools":[]}`,
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":1024,"estimated_tokens_delta":1024}`,
		`{"type":"system","subtype":"status"}`,
		`{"type":"system","subtype":"task_started"}`,
		`{"type":"system","subtype":"task_updated"}`,
		`{"type":"system","subtype":"background_tasks_changed"}`,
	},
	"rate_limit_event": {
		`{"type":"rate_limit_event"}`,
	},
}

// fixtureShape decodes one fixture line the same two-field way extractShapes
// reads the wire, and returns its subtype. Also pins the line under the map key
// it is filed under: a `system` fixture that declares some other type would
// exercise the wrong half of the mirror.
func fixtureShape(t *testing.T, typ, line string) string {
	t.Helper()
	var env struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		t.Fatalf("parserIgnoredTypeFixtures[%q]: fixture line does not decode: %s: %v", typ, line, err)
	}
	if env.Type != typ {
		t.Fatalf("parserIgnoredTypeFixtures[%q]: fixture line declares type %q: %s", typ, env.Type, line)
	}
	return env.Subtype
}

// TestParserIgnoredTypesMatchesStreamsupParser guards BOTH directions of the
// mirror against the shipped parser, since #1379 made it subtype-granular.
//
// The dangerous direction is the mirror claiming a line is DROPPED that the
// parser actually MAPS: extractShapes then silently drops a meaningful envelope
// from the sequence comparison, and the comparison keeps passing while the two
// runners diverge on it. The other direction — the mirror claiming a MAPPING the
// parser does not have — lets a genuinely ignored, high-rate line into the
// compared sequence, which is noise rather than blindness but is still a false
// statement about the parser.
//
// It asserts by construction rather than by reading the parser's unexported
// table: each fixture line is written to a real streamsup.Parser, and what it
// must emit is DERIVED from the mirror via parserDropsShape — zero events for a
// line the mirror says is dropped, at least one for a subtype it says is mapped.
// A type the parser no longer ignores emits a turnevent.Unrecognized, so it
// fails the dropped arm too. The remaining direction —
// streamsup.ignoredLineTypes growing past this mirror — already fails loudly in
// the same pre-ship gate (interactive_stream_liveness_test.go fatals on any
// unrecognized_message during a normal turn), so it needs no coverage here.
//
// Needs no claude and no network.
func TestParserIgnoredTypesMatchesStreamsupParser(t *testing.T) {
	for typ, mapped := range parserIgnoredTypes {
		lines, ok := parserIgnoredTypeFixtures[typ]
		if !ok || len(lines) == 0 {
			t.Errorf("parserIgnoredTypes member %q has no fixture in parserIgnoredTypeFixtures; add one so the mirror is exercised against the real parser", typ)
			continue
		}
		declared := map[string]struct{}{}
		for i, line := range lines {
			subtype := fixtureShape(t, typ, line)
			declared[subtype] = struct{}{}
			name := typ + "/" + subtype
			if subtype == "" {
				name = fmt.Sprintf("%s/%d", typ, i)
			}
			t.Run(name, func(t *testing.T) {
				got := parseOne(t, line)
				if parserDropsShape(typ, subtype) {
					if len(got) != 0 {
						t.Errorf("streamsup parser emitted %d event(s) for %s, want 0 — %q is in parserIgnoredTypes but the parser maps it; the shape comparison is dropping a meaningful envelope",
							len(got), line, typ)
					}
					return
				}
				if len(got) == 0 {
					t.Errorf("streamsup parser emitted 0 events for %s, want at least 1 — either parserIgnoredTypes[%q] claims subtype %q is mapped when the shipped parser drops it (the mirror is wrong), or this fixture line hit an emitter's undecodable arm, which returns consumed-with-zero-events and makes the fixture prove nothing; see streamsup.emitSystemSubtype",
						line, typ, subtype)
				}
			})
		}
		// Mirrors the member-has-fixture check one level down: a mapped subtype with
		// no fixture line is a mapping claim nothing exercises against the parser.
		for subtype := range mapped {
			if _, ok := declared[subtype]; !ok {
				t.Errorf("parserIgnoredTypes[%q] maps subtype %q but no line in parserIgnoredTypeFixtures[%q] declares it; add one so the mapping is exercised against the real parser", typ, subtype, typ)
			}
		}
	}
	for typ := range parserIgnoredTypeFixtures {
		if _, ok := parserIgnoredTypes[typ]; !ok {
			t.Errorf("parserIgnoredTypeFixtures has an orphan entry %q with no parserIgnoredTypes member; drop it or restore the member", typ)
		}
	}

	// Control arm: keeps the zero-event assertions above from passing vacuously
	// on a mis-wired sink or an unconsumed line. `result` is mapped (→ TurnEnd),
	// so this must be non-zero.
	t.Run("control_mapped_type_emits", func(t *testing.T) {
		if got := parseOne(t, `{"type":"result","subtype":"success"}`); len(got) == 0 {
			t.Error("streamsup parser emitted 0 events for a mapped `result` line; the zero-event assertions above prove nothing")
		}
	})
}

// parseOne writes one stream-json line (plus the trailing newline Parser.Write
// needs to consider it complete) to a fresh streamsup.Parser and returns the
// events it emitted. nil logger → slog.Default; the parser Debug-logs only.
func parseOne(t *testing.T, line string) []turnevent.Event {
	t.Helper()
	var got []turnevent.Event
	p := streamsup.NewParser(func(e turnevent.Event) { got = append(got, e) }, nil)
	if _, err := p.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("parser.Write(%s): %v", line, err)
	}
	return got
}
