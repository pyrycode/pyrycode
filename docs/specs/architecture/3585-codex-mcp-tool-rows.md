# #3585 — Codex MCP tool rows

## Files read

- `internal/codexsup/translate.go` → `Translator.item`, `commandItem`, `fileChangeItem`, `toolStatus`, `truncate`, `undecodable`: dispatch, terminal-event and bounded fallback conventions.
- `internal/codexsup/translate_test.go` → `replay`, `TestHandBuiltFramesMatchSchema`, `TestItemTypesClassified`, `TestMethodListsPartitionServerNotifications`: fixture and partition proof.
- `internal/codexsup/serverrequest_test.go` → `jsonSchema.validate`: unrestricted JSON properties and content elements are supported by the fixture validator.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `v2/ThreadItem` MCP arm, `McpToolCallResult`, `McpToolCallError`: required fields and unrestricted JSON contracts.
- `docs/knowledge/features/codexsup-package.md` § The translator's three-list partition: every update becomes a result; only terminal updates may be emitted.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification-protocol-boundaries.md`: table-driven tests and independent wire assertions.

## Context

MCP calls currently surface as unknown items. Map them to existing tool events using the pinned Codex 0.156.1 schema. Evidence is hand-built, not a live MCP observation. No new decision record is needed.

Overlaps: #3582, #3583 and #3584 add separate translator mappings; none supplies a dependency or rewrites MCP logic. Keep shared list and dispatch edits local.

Sizing: one deliverable, five acceptance criteria, approximately 400 written lines including tests, fixtures and this plan; zero exported types, zero consumer updates and fewer than ten reject branches. Rechecked against the finished plan before commitment.

## Design

Move `mcpToolCall` exclusively to `mappedItemTypes`, progress exclusively to `ignoredMethods`, and dispatch to `mcpToolCallItem(method, params) []turnevent.Event`.

Decode required identity/title/status strings with pointers and arguments as `json.RawMessage` so missing differs from valid JSON null. Validate mapping fields using existing `undecodable`; allow unknown string statuses to reach `toolStatus`'s failed fallback. Decode nullable result/error containers with required non-null content/message fields. Ignore presentation metadata.

Start emits one `ToolStart` with item ID, kind other, UTF-8-safe 256-byte `server/tool` title and verbatim arguments. Completion emits one `ToolUpdate`, status from `toolStatus`, empty `ResultDetail`. Non-null error selects its message even when empty; otherwise concatenate only string text from object elements tagged text. Skip all other unrestricted elements. Cap selected display text at 16 KiB using `truncate`; empty text yields nil content. Arguments stay opaque and existing turnbridge projections retain their bounds.

## Concurrency model

Runs synchronously on the existing notification caller. No goroutines, locks or new state.

## State transitions and identity reuse

| Event | Contract and race test |
| --- | --- |
| Start, progress, completion | One start and one terminal update with the same ID; progress silent. `TestTranslateMCPHandBuilt` |
| Repeated sequence reusing item ID | Each sequence maps independently without retained lifecycle state. `TestTranslateMCPHandBuilt` replays twice on one translator. |

## Error handling

Invalid JSON, missing/null required mapping fields or wrong typed containers use bounded `UnrecognizedUndecodable`. Unrestricted malformed text blocks are skipped, not rejected. Unknown completion statuses fail through the existing status helper. No payload is executed or logged.

## Testing strategy

Write focused tests first and observe mapping failures before implementation. Schema-validated hand-built completed, failed/error and empty-result sequences assert entire event lists. Table-driven tests cover opaque JSON values and large arguments, text extraction, precedence, empty variants, invalid required fields, lane membership and multibyte bounds. Run affected `internal/codexsup` race tests through admission, `go vet ./...` and `go build ./cmd/pyry` on macOS. Full acceptance stays with the dispatcher.

## Open questions

None. Schema-derived behavior intentionally makes no claim of live MCP capture.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/codexsup-package.md`, under **The translator's three-list partition**, describe `mcpToolCall` as mapped: bounded `server/tool` title, opaque arguments, one terminal result with error precedence and text-only extraction, 16 KiB UTF-8-safe result cap, and ignored MCP progress. Identify the evidence as schema-derived hand-built frames with no real MCP capture.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `mcpToolCallItem` accepts untrusted subprocess JSON solely as display data; pointers distinguish absent/null required fields, and unrestricted content blocks are independently skipped.
- [Tokens, secrets, credentials] No credential handling or payload logging. Arguments and result text retain peer-authored display provenance.
- [File operations] No production filesystem operations; resource URIs and metadata are ignored.
- [Subprocesses] No execution or shell interpretation; tool names and arguments only populate events.
- [Cryptography] No keys, nonces, randomness or comparisons with secrets.
- [Network and I/O] No URL/resource fetch or image decoding. Existing transport line cap bounds input; title and selected output receive explicit byte caps, while raw arguments remain intact for bounded turnbridge projections.
- [Errors, logs, telemetry] `undecodable` emits the existing bounded fallback; no new logging or telemetry.
- [Concurrency] Synchronous pure mapping adds no mutable lifecycle state or goroutines.
- [Threat model] Peer content remains untrusted under `docs/protocol-mobile.md` § Security model; rendering remains the consumer's responsibility. No new authority or I/O is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
