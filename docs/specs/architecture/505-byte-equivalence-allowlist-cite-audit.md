# Spec — #505: Align ptyrunner_byte_equivalence allowlist with #503 audit decisions

## Files to read first

- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:73-97` — current allowlist (`expectedStreamRunnerOnly`, `expectedPtyRunnerOnly`); the two tables you'll edit. `expectedStreamRunnerOnly` is populated with 8 bare `// #503` comments to upgrade; `expectedPtyRunnerOnly` is initialised-but-empty and gets 4 new entries.
- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:128-141` — current `envelopeShape` doc-comment; the block you'll replace with audit-pointing wording. Block landmark: starts with `// envelopeShape captures the per-line dispatcher-visible structural shape`.
- `docs/audits/2026-05-23-ptyrunner-streamrunner-byte-equivalence.md` — full read. Sections that drive every comment you'll write: "Per-field / per-event decisions" (per-field rationale) and "Ptyrunner-only events" (the four new entries' rationale).
- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:238-303` (`additiveDriftViolations`) — for context only. Read once so you understand WHY the allowlist's failure message points contributors at #503/audit doc; do not modify.

## Context

`internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go` carries 8 streamrunner-only allowlist entries (1 Event + 7 ResultTrailerFields) marked `// #503` with no per-field rationale, an empty `expectedPtyRunnerOnly` table that should hold 4 known ptyrunner-only emissions, and an `envelopeShape` doc-comment that frames the divergence as ptyrunner being "a strict subset by design" — generic, no per-field justification.

The #503 audit landed 2026-05-23 (commit `c6f5953`) and concluded: **zero "must close" items; every divergence is `document-as-omission`**. The audit enumerates the dispatcher's complete read surface and shows none of the divergent fields/events are consumed semantically — they all fall into `dispatch.ts:240`'s default-case preview log.

This ticket operationalises that outcome in the test source: every tolerated divergence cites the audit by date + one-line rationale, the doc-comment points at the audit instead of the generic "strict subset" framing, and the empty side gets populated. Purely test-source edits — no production code, no behaviour change.

## Design

### Edits, in order

**Edit 1 — upgrade `expectedStreamRunnerOnly` comments (test file, lines 73-86).**

For each of the 9 existing trailing comments, replace the bare `// #503` (or `// #503: <existing one-line>`) with `// #503 audit 2026-05-23: <one-line reason>`. The per-field reasons are drawn verbatim from the audit's tables (audit doc § "Streamrunner-only events" and § "Streamrunner-only result-trailer fields"):

| Map key | Replacement trailing comment |
|---|---|
| `"rate_limit_event"` (Events) | `// #503 audit 2026-05-23: API-stream event, structurally unreachable from claude's local JSONL (which ptyrunner tails). Dispatcher default-case preview log only — no semantic consumption.` |
| `"api_error_status"` (ResultTrailerFields) | `// #503 audit 2026-05-23: API-only (claude's HTTP layer); dispatcher never reads it.` |
| `"duration_api_ms"` | `// #503 audit 2026-05-23: API-only; dispatcher never reads it.` |
| `"fast_mode_state"` | `// #503 audit 2026-05-23: API-only; dispatcher never reads it.` |
| `"modelUsage"` | `// #503 audit 2026-05-23: API-only (per-model breakdown); dispatcher never reads it.` |
| `"permission_denials"` | `// #503 audit 2026-05-23: API-only (claude's gate result); dispatcher never reads it.` |
| `"ttft_ms"` | `// #503 audit 2026-05-23: API-only (time to first token); dispatcher never reads it.` |
| `"uuid"` | `// #503 audit 2026-05-23: API-side session UUID, distinct from session_id (which dispatcher does read); duplicate signal.` |

The map keys themselves do not change. Only the trailing comments change.

**Edit 2 — populate `expectedPtyRunnerOnly.Events` (test file, lines 94-97).**

Replace the empty `Events: map[string]struct{}{}` with four entries, each citing the audit. Verbatim block to insert:

```go
Events: map[string]struct{}{
    "permission-mode":       {}, // #503 audit 2026-05-23: claude local-JSONL housekeeping envelope. Dispatcher default-case preview log only.
    "file-history-snapshot": {}, // #503 audit 2026-05-23: ditto.
    "skill_listing":         {}, // #503 audit 2026-05-23: ditto.
    "ai-title":              {}, // #503 audit 2026-05-23: ditto.
},
```

`ResultTrailerFields` stays initialised-but-empty (`map[string]struct{}{}`) — the audit found no ptyrunner-only result-trailer fields.

**Edit 3 — replace `envelopeShape` doc-comment (test file, lines 128-141).**

Replace the existing 14-line block with the AC-prescribed wording (audit-pointing rather than "strict subset by design"). Verbatim block:

```go
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
```

The `type envelopeShape struct { Type, Subtype string }` declaration immediately following the doc-comment is unchanged.

### What stays unchanged

- The `additiveDriftAllowlist` struct definition and its sibling doc-comment (lines 55-64).
- `additiveDriftViolations` and its failure messages (lines 238-303) — they reference `#503 or audit doc`, which remains accurate.
- The `expectedStreamRunnerOnly`/`expectedPtyRunnerOnly` doc-comments at lines 66-72 and 88-93 — they already point at the right tickets; do NOT rewrite them. (Spot-check: the line 70-72 sentence says "Sibling ticket #505 tunes membership per the #503 audit's per-field 'must close' vs 'document as omitted' decisions" — that sentence describes this ticket. Leave it; the developer is fulfilling it, not editing it.)
- All test functions, helpers (`extractShapes`, `extractEventTypeSet`, `extractResultTrailerFields`, `additiveDriftViolations`, `compareShapes`, `formatShapes`, `checkInit`, `checkUserContains`, `decodeResultTrailer`).
- `usage.server_tool_use` (audit § Streamrunner-only result-trailer fields) — out of scope per ticket body: it's a nested key on `usage`, not a top-level result-trailer field, so it isn't and shouldn't be in the top-level allowlist.

## Concurrency model

N/A — comment + map-literal edits only, no goroutines added or modified.

## Error handling

N/A — no executable code added; behaviour unchanged.

## Testing strategy

Two verification steps, both already in the AC:

- **Behavioural:** `go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...` still passes. No behavioural change is expected — comment + map-key edits only. The new map keys in `expectedPtyRunnerOnly.Events` are the four event types the audit confirms ptyrunner emits today; before this change, if those events appeared in a real run, `additiveDriftViolations` would have emitted four violations. The test only runs against the real claude CLI (gated by `WithWorktreeAuthenticated`, which skips without `ANTHROPIC_API_KEY`), so the contributor making this change locally may not be able to exercise the end-to-end test. The self-check sub-test `TestAdditiveDriftAssertion_SelfCheck` runs without an API key and exercises the allowlist mechanism with hand-crafted fixtures — running just that sub-test (`go test -race -tags e2e_realclaude -run TestAdditiveDriftAssertion_SelfCheck ./internal/e2e/realclaude/...`) confirms the four added map entries do not crash the comparison logic and the existing test infrastructure still compiles.
- **Format:** `gofmt -l internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go` reports clean. Trailing comments may push column alignment — gofmt will renormalise; verify after editing.

## Open questions

None. The AC is fully prescriptive; the audit doc provides the per-field rationale verbatim; no production code is touched.
