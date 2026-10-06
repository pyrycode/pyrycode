# Consume known command lifecycle bookkeeping (#2877)

## Files read

- `internal/streamsup/parser.go` → `consumeLine`, `streamLine`, `consumeToolProgress`, `emitUnrecognized`, `truncateRaw`: separate state decode, explicit fallback and existing raw cap.
- `internal/streamsup/envelope.go` → `userTurn`, `marshalTurnEnvelope`: daemon-written user envelopes carry no command uuid.
- `internal/streamsup/parser_test.go` → `collectEvents`, `logRecorder`, `TestParser_UnrecognizedTruncation`, `TestParser_IgnoredLineTypesIsTheMeasuredSet`: event/log fixtures, raw limits and ignored-type contract.
- `internal/streamsup/tool_progress_test.go` → `TestParser_ToolProgressDropIsLoggedContentFree`: exact structured log assertions.
- `internal/streamsup/prompt_suggestion_test.go` → `TestParser_PromptSuggestionAfterResult`: post-result delivery precedent.
- `docs/knowledge/features/streamsup-package.md` and `streamsup-package-turn-io-envelope-write-stdout-parser.md`: persistent child/parser ownership and turn I/O.
- `docs/knowledge/features/streamsup-package-tool-progress-consumed-by-matching.md`: match known varieties rather than hide an entire type; current heartbeats emit progress.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: lifecycle neutrality requires both open and idle assertions; content-free logs need distinctive sentinels or exact attributes.
- `CODING-STYLE.md`: table-driven tests, injected slog and symbol-based citations.

## Context

Claude 2.1.280 emitted started/completed command_lifecycle bookkeeping around peer-message turns in two conversations on 2026-10-06. These became unwanted Unrecognized cards. The refinement comment preserves one supplied raw frame and permits same-shape placeholders for the remaining frames. Only started/completed were observed; queued/cancelled/discarded/refused come from the reported declared vocabulary. Existing turn events already represent the delivered turn, and daemon user envelopes have no command uuid for correlation.

This is one daemon parser behavior, estimated at 300 written lines including tests and plan, zero exported types/interfaces, zero consumer updates, four acceptance criteria and two reject branches (decode failure and unmatched state). No overlapping feature branches touch the planned files. No decision record or separate documentation deliverable is needed.

## Design

Add a dedicated command_lifecycle case to `consumeLine`, outside `ignoredLineTypes`. Decode only its top-level state into a local string field. If decoding succeeds and the state is queued, started, completed, cancelled, discarded or refused, emit no event and log one Debug record with exactly site=line_type, type=command_lifecycle and the matched state. Ignore identifiers and other fields.

Missing/null/empty/unknown state or a non-string decode failure calls `emitUnrecognized(UnrecognizedLineType, "command_lifecycle", line)` exactly once. Preserve existing raw/truncation semantics. The arm docblock records the report provenance, distinguishes observed/declared states and explains why no command correlation is attempted. The arm consults no parser lifecycle state and changes none.

## Concurrency model

All work runs synchronously inside the existing single-writer parser path. No goroutines, locks, retained fields or shutdown changes.

## Error handling

An otherwise decodable line with an invalid state stays visible through the existing Unrecognized event; never log the decode error or rejected state. Outer streamLine failures retain their existing Undecodable handling.

## Testing strategy

Write parser tests first and observe the known-state cases fail against the original default arm. Cover all six states, exact Debug attributes, both reported started/completed pairs with placeholder identifiers, and missing/null/number/bool/object/array/empty/unknown states. Assert exact Unrecognized values including uncapped raw bytes and an oversized input capped by maxUnrecognizedRaw; assert exact metadata-only rejection logs.

For each known state, exercise fresh idle, open-turn and between-turn delivery. Snapshot lifecycle fields before/after the frame, assert zero additional events, and compare subsequent normal parser events against a control parser fed the same turn lines. Include completed after result and two peer-message turns bracketed by lifecycle pairs.

Run the new named tests, then `go test -race ./internal/streamsup/...`, `go vet ./...` and `go build -o /tmp/builder-2877/pyry ./cmd/pyry`. The verifier owns the full-module gate. No live capture or live test is required.

## Open questions

None. Refinement explicitly permits synthesized same-shape pairs and specifies all six matched states.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `consumeLine` accepts untrusted child stdout; the dedicated arm matches only a decoded JSON string from the six-state allowlist. Nested state and other payload fields cannot authorize a drop.
- [Tokens, secrets, credentials] No credentials are created or stored. Exact attribute assertions ensure identifiers and arbitrary payload values never reach the new log record.
- [File operations] The arm performs no file operations and constructs no paths.
- [Subprocesses] No spawn, argv, environment or termination change; this only parses existing child stdout.
- [Cryptography] No primitives, keys, comparisons or randomness are introduced.
- [Network and I/O] Existing `Parser.Write` buffering and `truncateRaw` bounds remain intact; the arm retains no payload between lines or new connection state.
- [Errors, logs, telemetry] Only allowlisted state is logged on a drop. Rejects reuse `emitUnrecognized` metadata-only logging and capped raw events; decode errors and unknown state values are never logged.
- [Concurrency] Local decode only; existing single-writer ordering applies and no additional goroutine or lock exists.
- [Threat model] Scope is untrusted subprocess input classification. Relay transport/authentication and client history migration are untouched; no new threat mitigation is deferred.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
