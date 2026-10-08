# Operator delivery provenance (#2983)

## Files read

- `cmd/pyry/session_router.go` → `resolve`, `boundSession.WriteUserTurn`: exact receiving session; resolution precedes waits.
- `internal/sessions/session.go` → `ID`, `WriteUserTurn`; `internal/sessions/pool.go` → `HarnessFor`; `pool_identity.go` → `rekeyLocked`: routing IDs rotate, agent kind is construction-fixed.
- `cmd/pyry/inbound_deliver.go` → `newInboundDeliver`: activation, idle hold and final write.
- `cmd/pyry/send_now.go` → `newSendNowDeliver`, `attach`, `take`: send-now registration and exactly-once placement.
- `cmd/pyry/queued_message_placement.go` → `write`, `bindQueued`: outcome signal and safe projection retained until echo/idle.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory`: safe payload, placement timestamp and publication.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`: combine captured provenance with visibility.
- `cmd/pyry/main.go` → `runSupervisor`: shared placement instance across delivery and confirmation.
- `internal/msgqueue/delivery_context.go` → `DeliveryMessage`: safe attempt projection without composed delivery bytes.
- `cmd/pyry/ordinary_queue_placement_test.go`, `send_now_placement_test.go`, `operator_message_history_test.go`: ordering and publication fixtures.
- `docs/knowledge/features/history-package.md`, `history-package-producers.md`: unknown provenance remains absent; safe projection is structural.
- `docs/knowledge/features/msgqueue-package.md`, `sessions-package.md`, `development-verification.md`: preserve queue seams and synchronize callback completion independently from drain barriers.
- `docs/knowledge/decisions/042-daemon-built-thread.md`, `docs/protocol-mobile.md` § Security model, `CODING-STYLE.md`: provenance contract and content isolation.

## Context

Delivered operator messages currently lack session provenance. Delayed placement or confirmation must name the writer that accepted the turn rather than a subsequent conversation binding. This implements ADR 042 using the existing metadata seam; no additional decision record is needed.

Sizing: one deliverable, approximately 550 written lines including plan and tests; zero exported types/interfaces, at most six production consumers, four acceptance criteria and fewer than ten failure branches. Existing public queue signatures remain unchanged. Recount before commit stays below all limits. Fetched feature branches #2873, #2882 and #2982 do not touch the planned production files.

## Design

`boundSession` exposes an optional daemon-only provenance capability. Retain its construction-fixed kind from the resolved session's pool harness lookup; read its live `Session.ID()` immediately at the final writer call, after activation and idle waits. Never use the cached routing ID on `boundSession`, current conversation binding, subprocess IDs or client fields for attribution.

A shared operator-write helper captures provenance by value immediately before invoking the resolved writer. Unknown writers keep absent provenance and existing placement selection. Known writers select Claude placement from their own kind, rather than a later conversation lookup. Failed writes discard the snapshot.

Claude queue-backed placement retains the successful snapshot on its registered entry, publishing it only after the outcome signal. The safe producer accepts optional captured provenance and forwards it to `appendConversationHistory`. Existing echo/idle ordering, matching digests, callback bookkeeping and publication remain intact.

Codex/no-stream writes retain successful snapshots keyed by canonical conversation and queue ID on the same placement coordinator. Confirmation consumes the snapshot once and creates the existing safe commit. Failed attempts save nothing; retries capture their own writer. Source-less callers still work through the original producer and delivery surfaces. No new lifecycle fact or wire field is introduced.

## Concurrency model

Existing queue, stream drain and idle waiter goroutines remain unchanged, with context cancellation as their shutdown path. Snapshot publication for Claude occurs before closing the write outcome channel, which synchronizes echo/idle readers. Confirmation snapshots use the placement mutex; copy and remove under that mutex, then append/publish outside it. Do not hold placement locks across session, storage or publication calls. The resolved agent kind is immutable; session IDs use the existing locked accessor.

## Error handling

Resolve, activation and hold errors capture nothing. Writer failure removes registration and retains no source. Successful retries replace failed attempts without inheritance. Nil/failed history storage preserves safe publication with absent durable identity. Unknown provenance is omitted, never inferred as `none` or defaulted to Claude.

## Testing strategy

Write focused hermetic tests first and observe missing-provenance failures. Exercise queued Claude echo and ordinary idle, both send-now placements, Codex confirmation after rebinding, routing-ID rotation during activation/idle, enqueue under A then delivery to B, and failed-write retry. Assert exactly one entry after late echoes and completed callbacks, safe text/attachments/client metadata, visibility and durable identity. Compare warm and reopened raw pages and preserve an old untagged entry. Existing wired drain/live/replay ordering tests and nil/failed-store tests remain regression coverage. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry` with output outside the worktree. The verifier owns `make check`; live Claude is unnecessary.

## Open questions

None. The receiving writer defines attribution even if the conversation rebinds after resolution; its live routing ID is read after delivery waits.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/history-package-producers.md`, “Producers (#2114, #2115)” (linked from `history-package.md`), state that operator provenance names the successful receiving session captured at delivery and retained through Claude echo/idle placement or Codex confirmation. Distinguish this from enqueue-time binding, preserve the safe-projection explanation, and update the statement that operator provenance is absent. Legacy/unknown provenance stays absent.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The optional capability on `boundSession` supplies daemon session identity and pool-owned kind. `DeliveryMessage` supplies safe queue data; neither client fields nor subprocess output supplies source identity.
- [Tokens] No credential generation, storage or lifecycle changes; provenance carries only routing identity and agent kind.
- [File operations] Reuse `appendConversationHistory` and the existing contained history store; no new filesystem path or file mode is introduced.
- [Subprocesses] The same resolved writer receives the existing composed bytes; no process invocation or environment changes.
- [Cryptography] Keep SHA-256 only as the existing private matching digest. No new cryptographic operation or key usage.
- [Network and I/O] No new input, payload field, socket or read boundary; existing relay limits and recipient gates apply unchanged.
- [Errors/logs] SHOULD FIX: tests must assert that composed prompts, host paths and matching digests stay absent from history, publication and logs. No new logging is necessary.
- [Concurrency] Capture into a local value, then publish before the outcome signal; retain confirmation snapshots under the placement leaf mutex. Failed writes save nothing, preventing stale retry attribution.
- [Threat model] Preserve safe projection and legacy eligibility through the existing producer. Acceptance/drop/loss facts and shutdown gap recovery are OUT OF SCOPE, owned by #2972.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08
