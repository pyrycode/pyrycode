# #2981 — producing-session attribution for interactive history

## Files read

- `cmd/pyry/stream_turn_drain.go` → `sinkForTag`, `startStreamTurnDrainV2`: capture and resolve producer routing tags before emitter delivery.
- `cmd/pyry/interactive_turn_v2.go` → `convTurnState`, `HandleFor`, `flushDelta`, `closeForConversation`, `emit`: retained lifecycle and main/child buffer ownership.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`: combine captured provenance with visibility and retain nil/failure behavior.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`: Claude parser's producing kind and rotating daemon tag.
- `cmd/pyry/codex_runner.go` → `newCodexRunnerFactory`: Codex translator's producing kind and daemon tag, distinct from its thread ID.
- `cmd/pyry/interactive_turn_v2_history_test.go`, `stream_turn_drain_test.go`, `streamsup_runner_test.go`, `codex_runner_test.go`: existing history, drain and hermetic factory fixtures.
- `docs/knowledge/features/history-package.md` § Producers and Shape: metadata is outside payloads; legacy absence and visibility must survive.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: unresolved producers drop before emission; historical bindings remain resolvable.
- `docs/knowledge/features/streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md`, `codexsup-package-production-wiring.md`: factory wiring and producer-side capture.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, `docs/knowledge/decisions/042-daemon-built-thread.md`: behavioral tests, retention and session provenance contract.
- `docs/protocol-mobile.md` § Security model: subprocess content remains untrusted; pairing and recipient gates remain transport-owned.

## Context

The fan-in retains the producing routing ID but discards it before history append. Looking up the current session later misattributes delayed events. This is one deliverable: attribute the existing interactive history writer. ADR 042 already records the decision; no new ADR is required. No fetched feature branch overlaps the five production files.

Sizing: approximately 600 written lines including tests and this plan, no new exported types/interfaces, three production consumers updated together, three acceptance criteria, and no new rejection branches. The ~500-line estimate and #2135 analogue are consistent with this scoped change; all five limits remain satisfied after planning.

## Design

- `sinkForTag(tag, optionalKind)` captures the kind and the single tag read into each event envelope. Both production factories supply their fixed kind; old sink calls keep absent provenance.
- The drain passes the captured value into `HandleFor(ctx, convID, event, optionalSource)` after the existing session-to-conversation resolution gate. Neither a cursor nor event payload supplies provenance.
- Retain `history.SessionProvenance` by value in `convTurnState`. Key retained state by conversation plus kind and routing ID, with the existing conversation-only key for absent provenance. A fixed delimiter separates canonical conversation IDs and the fixed kind from the opaque session ID. This isolates main/child lanes, reused message IDs, launcher IDs, buffers and lifecycle state across producing sessions without changing legacy entry surfaces.
- Timer flush selects each retained state; conversation close flushes and closes every retained source for that conversation using its own provenance. Empty states are released by their full key. No emitter-global current-source value is introduced.
- `appendConversationHistory(..., optionalSource)` adds nonempty captured provenance to the existing visibility metadata. Existing callers omit it and remain untagged; legacy payloads, ring IDs, timestamps, durable IDs and recipient selection are unchanged.

## Concurrency model

No new goroutines or locks. Producer closures read the existing atomic routing tag once per event and enqueue values. The existing context-bound drain is the sole writer of retained emitter state and handles events, timer flushes and lifecycle closes serially. Store and ring retain their existing synchronization.

## Error handling

Keep unresolved-session drops, bounded fan-in behavior, marshaling failures and nil/failed history behavior. Storage validates provenance at append. Do not infer `none` or rewrite old entries. No new error text or logging of source content is needed.

## Testing strategy

Write focused tests first and observe failures before implementation. Exercise both real production factories with hermetic children, capture before rotation and drain afterward, conversation interleaving and cursor changes, reused main/child message and parent IDs across sessions, timer flush and conversation lifecycle close. Check every produced raw history entry, warm and reopened stores, explicit visibility, legacy payload/ring/page equivalence and absent-source compatibility. Existing nil/failure and recipient tests remain part of the touched-package race suite. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and build `./cmd/pyry` to scratch; the dispatcher owns `make check` and the full-module gate. No live Claude test is required.

## Open questions

None. Source facts are producer kind and daemon routing tag, independent of conversation and Codex thread identity.

## Documentation handoff

Pending for documentation stage: in `docs/knowledge/features/history-package.md`, “Producers (#2114, #2115)”, state that interactive provenance is captured from the producing runner before fan-in, retained with main/child buffered text, and preserved through delayed drain and lifecycle flush. Update the statement that all producers leave session metadata absent to distinguish the attributed interactive writer from the remaining producers. State that legacy/unknown provenance stays absent.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Producer kinds are fixed in `newStreamRunnerFactory` and `newCodexRunnerFactory`; routing tags come from daemon runner configuration. `startStreamTurnDrainV2` resolves those tags before emission. Subprocess/client fields cannot override provenance.
- [Tokens, secrets, credentials] No new credential handling. Provenance contains existing routing IDs, never pairing identity, tokens or Codex thread IDs.
- [File operations] `appendConversationHistory` reuses `Store.AppendWithMetadata` containment and metadata validation; routing IDs are scalar metadata, never path components. No new filesystem primitive.
- [Subprocesses] Factory sink wiring changes only; launch argv, environment and shutdown paths stay unchanged.
- [Cryptography] No new cryptographic operation or security identifier.
- [Network and I/O] No new wire field or socket read. Existing frame caps, delta splitting and recipient capability gate remain in force.
- [Errors, logs, telemetry] Keep content-free failure discriminants and existing logs; do not log source payloads or paths.
- [Concurrency] Value capture before fan-in and session-keyed retained states prevent rotation or conversation selection from substituting another producer. The existing single drain and cancellation cleanup remain the only state writer and shutdown path.
- [Threat model] Protocol threat 1 remains client-owned untrusted rendering; no payload trust upgrade. Pairing and replay/live/page recipient authorization stay unchanged. Other producers' provenance migrations are out of scope and owned by #2982 and #2983.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08

## Revisions

- 2026-10-08: retained-state tests exposed that independent source buffers could join same-source text across intervening successor events and reorder a conversation's flushes. `HandleFor` now flushes another source's pending text before selecting the incoming source, preserving arrival order while retaining each source's lifecycle and child identities. At most one source per conversation holds buffered text.
- 2026-10-08 (verifier finding 1): per-source lifecycle deduplication incorrectly let a delayed old-session end idle the conversation and erase its successor's reconnect phase. `transitionTo` now projects and deduplicates the conversation phase from the retained source with the most recent main-phase event, including repeated events in the same phase. Ending a different source preserves that projection; ending its owner restores the latest remaining running phase with that source's provenance. `endTurn` resets only the selected source; the projection alone publishes/clears the snapshot. Conversation-wide close processes older phases first and emits one final idle. Turn-end entries keep their producing source, while `turn_state` history/live/replay/page entries describe the conversation projection with the responsible source's captured provenance. Regression scenarios cover Claude/Codex rotations, switches in both directions, either source ending, successor continuation, reconnect snapshots, and lifecycle close. This adds no trust boundary, wire field, goroutine, or failure branch; the security review remains PASS. Revised total written work remains below 800 lines, with no new exported type/interface or simultaneous consumer change.
