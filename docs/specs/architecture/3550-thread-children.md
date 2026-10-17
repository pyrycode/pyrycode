# Authenticated bounded child pages

## Files read
- `internal/relay/v2session_catchup.go` → `admitThreadQuery`, `threadQueryGate`: shared slot, live cancellation and join cut.
- `internal/relay/v2session_tool_body.go`, `v2session_tool_body_test.go`: expansion worker cleanup and authenticated lifecycle fixtures.
- `internal/relay/v2session_thread_delivery.go`, `v2session_thread.go`, `v2session_hosted.go` → `deliverThread`, `threadWithheld`, `hostedDeliver`: generation identity and late recipient checks.
- `internal/relay/v2session.go`, `v2session_seams.go`, `v2session_handshake.go`: dispatch, provider contract and negotiated flags.
- `internal/thread/child_pages.go` → `ChildPage`, `ChildPageResult`: detached summaries, current publication and public membership.
- `internal/protocol/thread_expansion.go`, `thread_expansion_encoding.go` → `ThreadExpansionCursor`, `EncodeThreadChildren`: typed cursor and byte-selected prefix.
- `internal/relay/v2session_catchup_test.go`, `v2session_thread_test.go`: authenticated query and capability fixtures.
- `cmd/pyry/relay_guard_test.go`: implemented switch handler classification.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § Thread delivery and summary projection: final raw fields must reject trailing duplicate null; expansion completion cannot advance joins.
- `docs/knowledge/features/relay-package.md` § Test surface: held sends record accepted prefix before signalling; use retirement barriers for reuse.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, `docs/protocol-mobile.md` § Bounded expansion reads and Security model: validation and security contracts.

## Context
Opening a parent needs only bounded direct summaries. Store and encoder are merged; this adds their authenticated relay adapter and conditional negotiation. Production installation remains #3523, compact projection #3517. No decision record needed.
One deliverable, estimated 740 written lines (170 production, 500 tests, 70 plan), zero new exported types, no callback migrations, five criteria and fewer than ten refusal categories. Overlaps #3240 in session/handshake wiring and #3538 in the guard are additive, independent blocks.

## Design
Add optional `V2SessionConfig.ThreadChildren(context.Context, protocol.ThreadExpansionRequest) (thread.ChildPageResult, error)`. Providers honor cancellation and enforce current epoch/revision/version plus public parent/cursor membership in the named conversation; no fallback.
Extend shared admission with a child request arm. Require current open pairing, canonical registered visible conversation and negotiated expansion/thread/interactive plus existing Codex/multi_agent gates. Parse required raw fields strictly, including every cursor field: zero version/order are valid; cursor ID is nonzero; all scope fields match. Normalize omitted/zero limit to 50 and clamp positives to 256; negative/null/nonintegers refuse. Invalid envelope IDs retain connection-close policy.
Use the existing unfinished slot and worker. Child requests bypass join capacity. Adapt only detached direct summaries and authoritative More evidence into `EncodeThreadChildren`; preserve recorded session attribution and capture metadata. Refuse nonusable/error/mismatched results or contradictory completion evidence. The encoder bounds the entire stamped escaped page including its separate completion to 65519 bytes and derives continuation from delivered rows, retaining undelivered selected rows.
Generalize expansion final handling to children without entering the join-writing items path. Extend content and expansion recipient gates to every child frame. Negotiate and record expansion only with both provider callbacks, existing thread readiness, both compiled encoders/handlers and all three advertised capabilities. Production callbacks stay absent.

## Concurrency model
Run owns session state, recipient gates and Noise sealing. Existing per-connection workers run providers and encoding under query context. Existing `hostedMu` serializes query identity, cancellation and final selection. No new goroutines or lock order. Cleanup compares query identity; generation-bound `deliverThread` rejects stale inboxes.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Three pages, nested parent, empty page, shortened continuation and repeated reads | `TestThreadChildrenPages` |
| Stale revision/version, invalid cursor, forged conversation parent | `TestThreadChildrenAdmission`, `TestThreadChildrenRefusals` |
| Shared child/body/full contention, live cancellation, other client/conversation progress | `TestThreadChildrenSharedSlot` |
| Held provider disconnect, reuse and manager stop | `TestThreadChildrenProviderShutdown` |
| Deleted/revoked recipient, capabilities withdrawn, failed or withheld final, reuse | `TestThreadChildrenInterrupted` |
| Existing joins and unrelated live delivery after success | `TestThreadChildrenPages` |
| Provider/client readiness combinations at handshake | `TestThreadExpansionHandshake` |

## Error handling
Use correlated content-free `thread.query_failed` for all refusals except malformed envelope identity. No-row-fits is a refusal; usable empty is success. Serial delivery stops on any failure before success; cleanup releases only its query, while sealed failed sends retire the connection.

## Testing strategy
Write tests first and observe failure. Use authenticated synthetic providers for strict admission, bounded selection, escaped stamped bytes, continuation and lifecycle checks; retain body/full/legacy tests. Run race tests for touched relay package, focused command guard, `go vet ./...` and `go build ./cmd/pyry` on macOS. Verifier owns `make check` and the full-module gate; record executed/passed/failed/skipped counts.

## Open questions
None.

## Documentation handoff
Pending documentation stage:
- `docs/protocol-mobile.md`, expansion request/reply message-table rows, “Bounded expansion reads” and “Query/live ordering”: mark both handlers and conditional negotiation implemented, with production installation pending #3523. Document strict cursor/limit admission, additive readiness, stale-continuation refusal, cancellation and ordered expansion completion distinct from recovery coverage. Keep production advertisement pending real Store installation.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, “Thread delivery and summary projection”: describe readiness requiring both providers and all three client capabilities, with expansion recipient gates on every frame. Replace staged body-only withholding/test-flag wording with the conditional handshake contract; production remains uninstalled.

## Security review
**Verdict:** PASS
- Trust boundaries: strict raw-field admission in `admitThreadQuery`; provider owns current public membership, encoder validates scope and ordered prefix. SHOULD FIX: reject result conversation mismatch rather than replacing it with request scope.
- Tokens: current pairing checked by `hostedAuthorized` on admission and each frame; no credential generation/storage changes.
- Files/subprocesses: no filesystem or process operations introduced.
- Cryptography: existing Run-owned Noise sealing and inbox generation fence; no keys, primitives or nonce changes.
- Network/I/O: existing inbound caps and queue bounds; one unfinished query; page total capped at 65519 stamped bytes and 256 rows.
- Errors/logs: generic refusal, no payload or provider diagnostics exposed.
- Concurrency: identity-checked cleanup and final release share existing mutex; connection/manager cancellation ends provider and delivery work.
- Threat model: hostile relay sees ciphertext only; summaries remain inert untrusted display text. Public membership cannot derive from globally reused IDs. OUT OF SCOPE: provider production installation #3523, client rendering sanitization unchanged.
**Reviewer:** builder self-review. **Date:** 2026-10-17.
