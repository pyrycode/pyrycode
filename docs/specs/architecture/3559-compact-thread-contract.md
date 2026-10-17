# Compact thread projection and freshness contract

## Files read

- `internal/protocol/thread.go` → `ThreadItem`: full content and source revision remain unchanged.
- `internal/protocol/thread_query.go` → query DTOs and `thread_query_encoding.go` → `EncodeThreadReply`: separate success markers and atomic encoding failure.
- `internal/protocol/thread_encoding.go` → `EncodeThreadUpdate`, `ThreadUpdatePart`, `threadStampBudget`: escaped whole-envelope sizing and assembly.
- `internal/protocol/thread_expansion.go` → `ThreadSummary`, `NewThreadSummary`, `ThreadExpansionCursor.Matches`: bounded metadata and consumed-version-bound enumeration.
- `internal/protocol/thread_expansion_encoding.go` → expansion encoders: body/page completion remains separate from history coverage.
- `internal/protocol/handshake.go`, `codes.go`, `compat_test.go`, `cmd/pyry/relay_guard_test.go`: vocabulary does not register handlers or negotiate support.
- `internal/protocol/thread_query_test.go`, `thread_encoding_test.go`, `thread_expansion_test.go`: existing complete-view assertions and reusable budget/assembly helpers.
- `docs/knowledge/features/protocol-package.md`, `protocol-package-thread-messages.md`: no Store dependency; fragment final never certifies query coverage.
- `docs/knowledge/features/development-verification-protocol-boundaries.md`: typed decode/re-encode and failure after a flushed batch are required proofs.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`: expansion is installed; compact must stay unnegotiated.
- `docs/protocol-mobile.md` → thread queries, expansion, negotiation and security model: display data remains inert under authenticated transport.

## Context

Clients need a distinct compact coverage contract before Store projection (#3560/#3561), authenticated admission (#3562) and daemon activation (#3563). This slice declares and encodes it only. No decision record is needed. No concurrent feature branch touches the proposed existing files at planning time.

Sizing: one deliverable, executable additive protocol contract; estimated 740 written lines including this plan and fixtures, one new exported type, zero production consumer migrations, four acceptance criteria, at most six added rejection conditions. The #3233 analogue supplies batching/assembly already, so they are extended locally rather than replaced. Recount before plan commit remains below all five limits.

## Design

`CapabilityThreadCompact = "thread_compact"` is distinct from expansion. Optional `Projection` on catch-up/page selects `"compact-v1"` for that connection and conversation, including subsequent live delivery; omission selects complete. Compact page also supplies epoch. First selection or incompatible view change requires version-zero newest-window initialization. Reconnect may resume only successfully completed coverage matching conversation, projection and epoch. Unknown projections require refusal by future handlers; declarations grant neither support nor authorization.

`ThreadItemsPayload` adds optional projection and `Headers []ThreadSummary`. In compact replies, existing `items` contains public top-level non-tool/agent rows with full unchanged content; `headers` contains public top-level tool/agent metadata only. Placement comes from order/ID, not array concatenation. Complete replies omit both additions. Data batches, separate empty finals, resets and assembly parts repeat scope. Compact finals certify only the compact root window or consumed source interval, never full inventory or fetched detail completeness.

`ThreadSummary` adds optional error summary, public direct-child count/existence and subtree version. Nil means unknown; known zero/false/empty is explicit. Count and freshness are integers below 2^53; known count requires matching existence (`count > 0`); existence alone is allowed. `NoChild` remains attribution provenance, not an inventory count. Headline/error are inert single-line text bounded to the existing 200-code-point limit including ellipsis. A blank headline uses a kind-based usable fallback. Summary serialization enforces these display bounds without inspecting Content. Public counts exclude private children. Epoch-scoped subtree version changes on nested public changes independently of source `Rev`, which remains the expansion-read revision.

One new `ThreadCompactUpdatePayload` uses a declared outbound frame with projection/conversation/epoch, exclusive from-version and consumed version, and either a full top-level message or tool/agent header. Explicit progress has neither row and reports hidden-only consumed progress; it is not a missing event. Full row replacement avoids inventing header patches. Existing query batching handles message and header arrays; existing fragmentation and stamp reservation handle oversized messages and live payloads.

Body cache keys include conversation/epoch/item/source revision. Changed ancestor subtree version invalidates cached subtree knowledge even while open and prompts bounded explicit refresh of demanded bodies/pages; conservative invalidation is allowed. Unknown freshness proves nothing. Expansion reads neither subscribe descendants nor change view. Child cursors retain capture version/revision/epoch binding and stale refusal. Expansion success certifies only its body/page, query success only projection coverage, fragment final only assembly. Error, disconnect or partial data certifies none and cannot advance completed checkpoints.

## Concurrency model

Pure detached DTO encoding; no goroutines, locks, shared mutable state or I/O. Callers must not mutate inputs during encoding. Production connection ownership and view admission belong to #3562/#3563.

## State transitions and identity reuse

| Event | Contract and race test |
| --- | --- |
| First selection, view change, reconnect, other conversation or epoch | Compatible coverage only; otherwise initialize at version zero; `TestThreadCompactSelection` uses literal request fixtures. |
| Nested public change with stable ancestor source revision | Subtree version changes independently and invalidates open cached subtree knowledge; `TestThreadCompactFreshness` models cache invalidation and consumed-version cursor refusal. |
| Hidden-only progress and repeated live headers | Explicit scoped source interval, idempotent row identity; `TestThreadCompactLive` round trips replacement/progress fixtures. |
| Query/expansion/assembly completion, interruption or reset | Distinct evidence; `TestThreadCompactReplies`, `TestThreadCompactFreshness` and existing expansion/assembly tests. |

## Error handling

Encoders retain content-free errors and nil output on every failure, including after an earlier encodable batch. New live shape rejects missing scope, wrong projection, incompatible row/progress combinations and invalid interval. Query callers retain semantic authorization/visibility/numeric validation responsibilities. No new read/admission path is installed.

## Testing strategy

Write focused fixtures first and observe failure, then implement. Typed decode/re-encode checks literal selection keys, mixed full messages/headers, empty and catch-up finals, resets, live headers/progress, optional unknown versus known zero, escaped bounded text, absent versus available empty bodies and oversized message assembly. Measure every envelope with maximum stamps and variable routing metadata. Force batch flush before invalid JSON to prove nil-prefix failure. Retain existing complete query/live/expansion tests and run Linux protocol race tests, focused command vocabulary guard, `go vet ./...` and `go build ./cmd/pyry`. Full-module race gate belongs to verifier.

## Open questions

None. Header facts are optional and producer-owned; no Store derivation or cache runtime is added here.

## Documentation handoff

- Pending documentation stage: `docs/protocol-mobile.md`, “Thread query messages”: add compact contract/examples for selection, header bounds, projection identity, invalidation, coverage and recovery; mark declared pending production installation.
- Pending documentation stage: `docs/protocol-mobile.md`, “Bounded expansion reads” and “Capability negotiation (v2)”: distinguish installed expansion from declared compact support; mark the new compact live message-table row declared.
- Pending documentation stage: `docs/knowledge/features/protocol-package-thread-messages.md`, “Query replies and coverage”: link/distinguish coverage contracts and correct pending-expansion wording for merged #3523/#3550.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX: source tool Content must never enter compact headers. Use `ThreadSummary` only, retain unknown optional facts, and fixture-test secret-bearing Content exclusion. Producers own public visibility and numeric validation; #3560/#3561 derive facts, #3562 admits selections.
- [Tokens, secrets, credentials] No credential operations. Fixtures use synthetic content; bounded errors still remain untrusted display data.
- [File operations] No production file operations or caller-controlled paths.
- [Subprocesses] No production subprocess or shell execution.
- [Cryptography] Reuse existing SHA-256 assembly identity without key/nonce changes. A digest is assembly integrity, never authorization.
- [Network and I/O] SHOULD FIX: projection metadata must be included on fragments before escaped-envelope sizing. Reuse `threadStampBudget` and test fully stamped output against `MaxThreadEnvelopeBytes`.
- [Errors, logs, telemetry] Reuse generic encoder errors, no payload logging, and nil output on failure after batch flush.
- [Concurrency] Detached encoding only; inputs cannot be mutated concurrently. Interrupted streams cannot establish coverage.
- [Threat model] Inert source text and explicit projection prevent display/coverage confusion. Authenticated conversation eligibility, capability readiness and connection lifecycle remain OUT OF SCOPE for #3562/#3563; this declaration must not activate them.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-17
