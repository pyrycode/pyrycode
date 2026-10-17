# Authenticated thread tool-body reads

## Files read
- `internal/relay/v2session_catchup.go` → `admitThreadQuery`, `runThreadQuery`, `threadQueryGate`: shared unfinished slot and full-query join cut.
- `internal/relay/v2session_thread_delivery.go` → `deliverThread`: serial cancellable transport outcomes and generation fencing.
- `internal/relay/v2session_hosted.go` → `hostedDeliver`, `hostedAuthorized`: late gates and single-owner Noise sealing.
- `internal/relay/v2session.go`, `v2session_seams.go`, `v2session_appframe.go`: discriminator, configuration and off-Run worker.
- `internal/relay/v2session_thread.go`, `v2session_livestate.go`: recipient visibility and agent gates.
- `internal/relay/v2session_catchup_test.go`, `v2session_hosted_test.go`: authenticated fixtures and interruption scenarios.
- `internal/thread/tool_body.go`, `tool_body_test.go` → `ToolBody`: public ownership and immutable capture rendering.
- `internal/protocol/thread_expansion_encoding.go` → `EncodeThreadToolBody`: complete rendered content, assembly and separate final marker.
- `cmd/pyry/relay_guard_test.go`: switch interception classification.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § Thread delivery and summary projection: legacy refusal must precede lane initialization; completion must fence live admission.
- `docs/knowledge/features/thread-package-background-store-queries.md` § Revision-scoped tool bodies: capture recipes and public membership remain provider responsibilities.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, `docs/protocol-mobile.md` § Bounded expansion reads and Security model: validation and security contracts.

## Context
One authenticated bounded body read consumes the merged Store/encoder contracts. Child reads and readiness negotiation belong to #3550; production installation belongs to #3523. No decision record is needed. Branch overlaps #3240/#3545 in session wiring and #3538 in the guard are local additive changes, not dependencies.
Sizing: approximately 760 written lines (180 production, 510 tests, 70 plan); no new exported types, no migrated callback consumers, five criteria and fewer than ten new refusal categories. This is one deliverable and a grandchild of #3516 via #3522.

## Design
Extend `ToolBodyResult` with captured `Kind`. Add optional `V2SessionConfig.ThreadToolBody(ctx, ThreadExpansionRequest) (ToolBodyResult, error)`; providers must enforce public ownership in the named conversation and cancellation. Leave production callbacks unset and expansion negotiation withheld. A session expansion flag is zero by default; authenticated tests enable it on Run.
Route body requests, including invalid envelope IDs, to shared admission. Require typed nonempty epoch, canonical registered conversation and nonzero uint64 item/revision; reject any cursor. Apply open/paired/thread/interactive/expansion and existing agent/visibility gates before provider work. Share the unfinished query slot, but body admission does not consume full-thread join capacity.
The worker calls the provider and validates usable/error-free captured identity/kind, then calls `EncodeThreadToolBody` without truncation. Send frames serially through `deliverThread`, pinning the query to its original inbox generation. Body finals release only their query and bypass join installation. Refusal/cancellation cleans up only that query even if a late gate withholds its error.

## Concurrency model
Run owns session capabilities, authorization checks and Noise ciphers. Existing per-connection workers own cancellable provider/encoding work. Inbox query identity and completion/live cancellation share `hostedMu`; no new goroutine or lock order. Connection/manager cancellation ends work and transport waits.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Repeated successful reads, existing full join and unrelated live delivery | `TestThreadToolBodyResults` |
| Body/full query contention and retry after live cancellation | `TestThreadToolBodySharedSlot` |
| Held provider, other client/conversation progress | `TestThreadToolBodySharedSlot` |
| Disconnect/reused connection ID, manager cancellation | `TestThreadToolBodyInterrupted` |
| Deletion, capability withdrawal, revocation, failed/withheld final delivery | `TestThreadToolBodyInterrupted` |
| Store publication changes during body rendering | `TestStoreToolBodyCapture` |

## Error handling
Invalid envelope identity closes using existing query policy. All other failures return correlated content-free `thread.query_failed`; missing/private/wrong-kind/stale/nonusable/error bodies never become empty successes. Delivery failures stop before success; a sealed lost frame retires the generation. Captured content stays inert and is never logged.

## Testing strategy
Write failing tests first. Extend Store parity/capture assertions for kind. Authenticated body tests cover strict field/gate refusals, available empty objects, tool/agent parity, oversized escaped reconstruction, selected-row and fully stamped byte counts independent of unrelated inventory, shared admission, join preservation, live cancellation and delivery interruption. Run focused race packages, command dispatch guard, `go vet ./...` and `go build ./cmd/pyry` on Linux. Full-module `make check` is pending the verifier's gate under builder policy.

## Open questions
None.

## Documentation handoff
Pending documentation stage:
- `docs/protocol-mobile.md`, body-request/reply message-table rows, “Bounded expansion reads” and “Query/live ordering”: mark the body handler implemented, with child handling/negotiation pending #3550 and production installation pending #3523. Document strict admission, refusal/cancellation, shared query admission and ordered expansion completion distinct from recovery coverage. Keep production expansion advertisement pending real Store installation.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, “Thread delivery and summary projection”: document body recipient gates and staged capability withholding.
- `docs/knowledge/features/thread-package-background-store-queries.md`, “Revision-scoped tool bodies”: include same-capture body kind.

## Security review
**Verdict:** PASS
- Trust boundaries: admission validates authenticated typed scope; `ToolBody` enforces public ownership and the encoder checks captured identity/kind. No fallback conversation/provider.
- Tokens: `hostedAuthorized` checks current pairing before admission and each sealed frame; no credential changes or logging.
- File operations/subprocesses: no new filesystem operation or subprocess; Store renders existing confined references.
- Cryptography: existing Run-owned Noise cipher and generation-bound inbox; no primitive or nonce changes.
- Network/I/O: existing inbound limits, one unfinished query and bounded delivery queue; each stamped body frame fits 65519 bytes. Deliberately no aggregate body cap.
- Errors/logs: generic content-free refusal only; body and provider errors remain private.
- Concurrency: SHOULD FIX addressed by design: bind query delivery to the original inbox so a reused connection ID cannot accept stale work; cleanup compares query identity.
- Threat model: encrypted output remains inert untrusted content (client rendering sanitization unchanged); malicious relay cannot decrypt/misroute authenticated bodies. OUT OF SCOPE: production installation #3523 and both-read negotiation #3550.
**Reviewer:** builder self-review. **Date:** 2026-10-17.
