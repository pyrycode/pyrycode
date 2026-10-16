# Bounded thread expansion reads

## Files read
- `internal/protocol/thread.go` → `ThreadItem`: full metadata and inert content.
- `internal/protocol/thread_encoding.go` → `threadEncodePayload`, `threadStampBudget`: escaped fragments and later-stamp reservation.
- `internal/protocol/thread_query.go`, `thread_query_encoding.go` → `NormalizeThreadQueryLimit`, `EncodeThreadReply`: existing complete-query contract stays unchanged.
- `internal/protocol/thread_query_test.go`, `thread_encoding_test.go` → query fixtures and budget/assembly helpers.
- `internal/protocol/codes.go`, `handshake.go`, `compat_test.go` → vocabulary and v1 exclusion.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`: declarations must not register handlers.
- `docs/knowledge/features/protocol-package-thread-messages.md` → Query replies and coverage: assembly final is not request completion; variable envelope metadata must precede encoding.
- `docs/knowledge/features/development-verification-protocol-boundaries.md`: raw-key fixtures and decoded DTO re-encoding.
- `docs/protocol-mobile.md` → thread schemas and Security model: inert display data and authenticated delivery boundaries.

## Context
Collapsed tool/agent rows need bounded reads independent of complete-thread coverage.
This is one protocol deliverable with two read shapes, not Store selection or handler installation.
The analogue #3233 added 657 lines in protocol; this design reuses its fragmentation machinery.
Budget: approximately 780 total written lines, five exported DTOs, zero migrated consumers,
four acceptance criteria, and no production state machine. No overlapping remote feature
branches touch the planned files at planning time. No new decision record is needed.

## Design
- Declare `thread_read_tool_body` / `thread_tool_body` and `thread_read_children` / `thread_children`, plus capability `thread_expansion`. Do not advertise or admit requests.
- Five DTOs: `ThreadExpansionRequest`, `ThreadExpansionCursor`, `ThreadSummary`, `ThreadToolBodyPayload`, `ThreadChildrenPayload`.
- Both requests name conversation, nonempty epoch, `item_id` (the parent for children), and `expected_rev`. Children also have limit and optional cursor. Required-field/auth/public ownership checks belong to #3521/#3522.
- Cursor binds conversation, epoch, item ID, revision, consumed version and last `(order,id)`. `Matches` compares request/capture scope; resumption is strictly after the tuple. Mismatch requires a fresh first page, without retaining old captures.
- `NewThreadSummary` copies identity/kind/parent/order/revision, attribution and lifecycle into a content-free DTO with `detail_omitted:true`. Normalize whitespace/control characters to a single line; cap headline at 200 Unicode code points including an ellipsis. Text is plain display data, never interpreted markup.
- `EncodeThreadToolBody(env, request, body)` accepts only tool_call/agent with available object content and matching capture. Preserve rendered content, including unknown fields and empty input/output, with no additional content limit or truncation. Reuse `ThreadUpdatePart` for oversized whole-body payloads.
- `EncodeThreadChildren(env, request, page, remaining)` packs an ascending detached candidate prefix, including order-zero rows, with a hard normalized limit (50 default, 256 max, negative rejects). Caller supplies direct-public-child candidates and authoritative evidence of rows beyond them; the encoder does no Store I/O. No parents, active extras, descendants or tie expansion.
- Child data is one envelope and a separate empty final marker under `thread_children`. Sum their encoded sizes with maximum reserved stamps; total must be <=65519. Try prefixes in order and stop before the first row that cannot fit. Remaining is caller evidence OR any supplied suffix excluded by rows/bytes. Cursor names the last emitted row only when more remain. No-row-fits with remaining rows, or marker-too-large, returns nil/error.
- Body data has `final:false`, then a separate content-free `final:true` marker under `thread_tool_body`. Child data omits cursor/more; its final marker carries explicit `more` and optional cursor. Only `more:false` authoritatively finishes parent enumeration at that capture.
- Every reply preserves InReplyTo and supplied variable envelope metadata. Errors return no usable prefix. Stale/unavailable/private/missing/wrong-kind reads use existing correlated content-free `thread.query_failed`; no successful empty substitute.
- A validated final fragment completes assembly only. Request completion requires all data and the separate success marker; interruption, errors and cancellation complete nothing. Expansion never advances full-thread recovery checkpoints or live join watermarks.

## Concurrency model
Pure detached encoding, no goroutines, I/O, locks or retained snapshots. Callers must not mutate inputs concurrently.

## State transitions and identity reuse
| Event | Race-enabled fixture |
| --- | --- |
| Three consecutive pages, tied and zero orders, exclusive resume | `TestThreadExpansionPages` |
| Epoch/revision/version/scope change between pages or first reads | `TestThreadExpansionMismatch` |
| Repeat encode without mutating input | `TestThreadExpansionPages` |
| Fragment completion, partial delivery, error or cancellation before marker | `TestThreadExpansionCompletion` |
No Store/relay lifecycle state is implemented in this slice.

## Error handling
Use existing content-free encoding sentinels. Encoding failures return nil, including invalid later candidates after earlier rows could fit. Unavailable content is absent, never `{}` success. Handler refusal maps to existing `thread.query_failed` via `EncodeThreadReply`.

## Testing strategy
Write executable raw-key/round-trip fixtures first and observe missing-symbol failure.
Cover all summary fields, omitted/empty/unavailable distinctions, unknown escaped body assembly,
limits/ties/order-zero/exclusive cursors, exact aggregate and envelope budgets with variable
metadata/reserved stamps, byte suffixes, empty success, no-fit refusal, atomic failures and
independent completion/checkpoint semantics. Run macOS `go test -race ./internal/protocol/...`,
focused command classification guards, `go vet ./...`, and `go build ./cmd/pyry` (output outside tree).
The verifier owns full-module race tests; no live run applies.

## Open questions
None. Store freshness/authorization and capability activation remain #3521/#3522/#3523.

## Documentation handoff
Pending for documentation stage:
- `docs/protocol-mobile.md`, “Thread query messages”: add “Bounded expansion reads” with exact request/reply examples, summary fields/headline bound, limit/byte rules, ordering, cursor binding, stale refusal, omitted/empty/unavailable meanings, assembly/completion and independent checkpoint semantics. Mark message-table entries and section declared ahead of handlers. State negotiation requires both reads wired to real Store and normal thread/interactive authorization; declaration alone does not enable support.
- `docs/knowledge/features/protocol-package-thread-messages.md`, “Query replies and coverage”: distinguish expansion completion from full-thread coverage and link the new contract.

## Security review
**Verdict:** PASS
- Trust boundaries: OUT OF SCOPE authentication, required-field checks and public ownership at #3521/#3522; encoder checks capture matching and copies only summary metadata. No handler is admitted here.
- Tokens/secrets: no credentials; content may be sensitive, remains inert raw JSON and is never logged. Encoding errors contain no supplied data.
- File operations/subprocesses: none in production; encoder returns bytes only.
- Cryptography: reuse existing SHA-256 assembly identity, not an authentication credential. Cursor is a public binding tuple, not authority.
- Network/I/O: SHOULD FIX ensure aggregate child budget reserves two envelopes' stamps, plus variable conversation/session metadata; tests cover exact ceiling. Socket admission/authorization belongs to #3522.
- Errors/logs/telemetry: existing generic sentinels and correlated refusal; no new logs or metrics.
- Concurrency: no shared state; all-or-nothing output prevents a failed later encode exposing a usable prefix.
- Threat model: untrusted text stays display data. No support negotiation before real providers and thread/interactive authorization (#3523).
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-17

## Revisions
2026-10-17 — Validation: protocol race checks, vet and binary build passed on macOS.
The command package race suite failed `TestDaemonThreadDemandWireAfterIdle` on an
unsolicited `thread_item_added` frame, then exhausted its ten-minute package budget
during `TestThreadRecordedBaselines`. The idle fixture repair is already on
unmerged #3504; cumulative macOS command-suite timeouts are tracked by #3525.
Neither runtime path changes here. Preserve these as validation blockers, without
copying their repairs into this protocol contract ticket.
