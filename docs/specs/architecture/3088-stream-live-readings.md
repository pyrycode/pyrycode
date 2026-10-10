# Source-ordered daemon stream live readings

## Files read
- `cmd/pyry/stream_turn_drain.go` → `sinkForProducer`, `offer`, `startStreamTurnDrainV2`: bounded fan-in and delayed conversation resolution.
- `cmd/pyry/runtime_boundary.go` → `beginRuntimeProducer`, `queueBoundary`, `sinkForSessionTag`: activation identity and capture before asynchronous boundary publication.
- `cmd/pyry/interactive_turn_v2.go` → `handleForSource`, `startTurnIfNeeded`, `ensureDeltaLane`, `transitionTo`, `emit`: source-specific turns, phase projection and legacy/history side effects.
- `cmd/pyry/session_transition_v2.go` → `Enqueue`, `publishSwitch`, `capture`: captured successor provenance, including unresolved harnesses.
- `cmd/pyry/relay.go` → `startRelay`, `startRelayV2`: both daemon compositions, including history without relay.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`, `Run`: producer activation precedes output.
- `cmd/pyry/codex_runner.go` → `Run`, `newCodexRunnerFactory`: same optional source boundary for Codex.
- `internal/protocol/envelope.go` → `Envelope`: detached bytes, pointer correlation fields and tri-state session metadata.
- `internal/turnbridge/outbound.go` → `MapEvent`, `BuildTurnState`: unchanged family payload mappings and size budgets.
- `internal/turnevent/event_task.go` → `BackgroundTaskUpdated`: nonempty status is terminal; a patch is not.
- `internal/relay/v2session_livestate.go` → `LiveState`, `validateLiveState`: downstream semantic record and finite-size contract; adapter belongs to #3077.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: source phase projection survives predecessor closure; background children outlive the main turn.
- `docs/knowledge/features/development-verification.md`: prove source admission, not merely an owner fixture, and measure escaping through production mappings.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: live readings are separate from items and legacy history.

## Context
Reconnect needs current producer readings without interpreting legacy history. Retention must precede fan-in and cannot depend on legacy connection enumeration or delivery. This implements the stream half of ADR 042; no new decision record is needed. No concurrent feature branch overlaps the proposed existing files.

Sizing: approximately 1050–1200 total written lines, zero new exported types/interfaces, fewer than ten consumer updates, four acceptance criteria, and fewer than ten reject branches. The line limit is exceeded because admission must also capture shared main/child turn identities and lifecycle retirement. This ticket is a grandchild of #2962 through #3076; `needs-human:sizing` records the judgement and the builder continues as required.

## Design
A daemon-local `daemonLiveState` owns conversation generations, immutable retained reading records, source activation bindings and per-identity revisions under one mutex. Its semantic `daemonLiveReading` has `Envelope`, `ConversationID`, `SessionGeneration`, `ReadingID`, and `Revision`. `admit` assigns positive revisions and returns detached updates; `retain` rejects retired generations and overtaken revisions. These optional seams remain separate from relay code.

Install one owner on the stream sink in both compositions before workers start, with the existing session-to-conversation resolver. Capture conversation, producing provenance, incarnation and generation on the producer side. Each source event maps live families with `turnbridge.MapEvent`, before bounded fan-in acceptance. Returned source capture travels in `streamTurnEnvelope`, and is retained in `convTurnState` for buffered work. Source admission mints main/child lane IDs which the legacy emitter adopts through optional fields, preserving one identity across both projections. Nil seams retain all existing behavior and signatures.

Singletons use an empty identity. Tool/task progress uses its producing ID. Source-side turn activity admits phase even when legacy phase deduplication suppresses a send. Main-turn termination retires thinking/main tool progress; child and background progress retain their own scopes. Terminal tool results/denials and task status notifications retire their progress. Activity removes stalls. Retry/compaction inactive values remain readings.

Captured transitions advance the generation before queueing publication, erase predecessor readings and admit one `{}` clear with successor metadata for every stream family. Activation detects same-ID reuse independently of filtered same-ID legacy notifications. Existing producer bindings keep their retired generation. Unknown provenance is omitted; positive no-session provenance is `null`; producer strings are never ordering counters.

A finite cursor captures references to immutable records under the owner lock, then returns one deep-detached record per `Next`. It copies no payloads eagerly, invokes no broadcaster or history APIs, and never incorporates later updates. Returned updates and snapshots clone all envelope bytes and pointer fields. Payloads and clears are measured against `protocol.MaxThreadEnvelopeBytes` at admission; invalid or oversized envelopes never replace retained state.

## Concurrency model
Producer callbacks perform bounded local mapping and mutex-protected admission. No owner lock spans resolver calls, I/O, broadcaster calls or fan-in operations. Legacy drain ownership and goroutine shutdown remain unchanged. Retention adds no goroutines. Cursors are detached consumers, with no owner access during consumption.

## State transitions and identity reuse
| Event | Coverage under `go test -race` |
| --- | --- |
| Reset/clear, agent switch, recovery | `TestDaemonLiveTransitions`: advances generation, supplies all clears and rejects delayed old capture |
| Same routing ID activation, sleep/eviction followed by reactivation | `TestDaemonLiveProducerCapture`: incarnation changes generation before fresh queued output |
| Repeated same-phase activity/new producer | `TestDaemonLiveProducerCapture`: retention attribution advances without extra legacy phase sends |
| Main turn ends; background/child work continues | `TestDaemonLiveProgressRetirement`: independent scopes and shared turn identities |
| Tool result/denial, task terminal notification, nonterminal task patch | `TestDaemonLiveProgressRetirement`: retire only the matching identity |
| Same-generation delayed revision and two conversations | `TestDaemonLiveDetachedCursor`: reject overtaken update; preserve independent readings |
| Concurrent cursor consumption and updates | `TestDaemonLiveDetachedCursor`: finite captured records and mutation isolation |

## Error handling
Unknown conversations do not gain a current snapshot. Unresolved provenance stays omitted. JSON mapping/size rejection changes no legacy behavior and exposes no payload in logs. Late retired source updates remain attributed to their captured generation and cannot replace the current snapshot. Turn-ID mint failures leave the optional capture empty so existing emitter handling remains available.

## Testing strategy
Write source-to-owner tests first and observe missing-owner compilation failure. Exercise every stream family, inactive values, source admission while fan-in is full and legacy delivery is held, transitions and activation reuse, progress retirement, phase deduplication, and immutable finite cursors. Compare payloads through the existing mapping and legacy/history fixture assertions. Measure bounded worst-escaping inventories and generated clears including maximum ordering/correlation metadata. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry`. The verifier owns the full-module gate. No live Claude turn is required.

## Open questions
None. Relay provider installation and update delivery belong to #3077, and control/prompt producers belong to #3089.

## Documentation handoff
Pending documentation stage:
- Add “Daemon-retained live state” to `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`, documenting captured provenance, detached stream readings, generation/revision identities and transition retirement.
- In `docs/knowledge/architecture/system-overview.md`, “Interactive Session”, state that this in-memory owner is separate from folded items and legacy delivery.
- Both sections must say relay provider installation and production activation await #3077 and the control/provider sibling #3089; final producer-to-relay protocol proof remains #3077.

## Security review
**Verdict:** PASS
**Findings:**
- [Trust boundaries] Source metadata is captured from daemon routing/provenance, never inferred from payload session fields. `admit` validates JSON and the full envelope bound. Strings remain inert payloads.
- [Tokens, secrets, credentials] No credential generation, lookup or storage is added. Retained source output stays memory-only and is never logged.
- [File operations] No new runtime file operations; the owner cannot write history or read a transcript.
- [Subprocesses] Existing runners are unchanged; no new command or environment flow.
- [Cryptography] No crypto changes. Ordering counters provide suppression, not authorization.
- [Network and I/O] Full detached envelopes and family clears must fit 65519 bytes. No new socket path; relay installation/sealing remains OUT OF SCOPE in #3077.
- [Errors, logs, telemetry] Invalid content is dropped from retention without payload/error-text logs; legacy handling remains unchanged.
- [Concurrency] Immutable records and deep detachment prevent caller mutation. One owner mutex serializes admission/transition and is released before delivery; no goroutine or lock-order cycle is added.
- [Threat model] Conversation metadata does not authorize access. Existing relay interactive/harness gates stay unchanged; encrypted wire proof and production activation are OUT OF SCOPE in #3077.
**Reviewer:** builder (self-review)
**Date:** 2026-10-10

## Revisions
- 2026-10-10: Source admission owns shared turn/lane identities so retention can progress while legacy delivery is held. Child assistant lanes remain distinct from the launcher's tool origin. Terminal identities retain a suppression tombstone until a new start; process stop retires all progress. The first output after a confirmed stop advances generation even when the supervisor recovers within the same Run activation and routing ID (`TestDaemonLiveProducerRecovery`). Captured boundaries retain detached clear cursors, and source updates include retained family clears before fresh readings.
- 2026-10-10: `TestDaemonLiveMappedInventoryBounds` exposed the existing MCP mapping's absent envelope budget: sixteen fully escaped rows can exceed 125 KB. Filed #3091 in Inbox; the hostile MCP fit assertion is skipped pending its independent mapping fix. The owner rejects oversized envelopes, while ordinary MCP retention and clear bounds remain covered. Changing legacy MCP payloads is outside this ticket.
- 2026-10-10: Final written-work count is approximately 1260 added lines including tests and the plan, above the initial 1050–1200 sketch. The grandchild sizing exception remains required. No exported type/interface or constructor signature was added.
- 2026-10-10 (verifier findings 1–4): Routing/provenance/generation capture, activation, exit capture/retirement, every transition path (including same-ID notifications), and removal serialize on `streamTurnSink.offerMu`. The lock order is `offerMu` then the owner mutex; resolution takes place outside the owner mutex. Normal event mapping, scheduling hooks, fan-in, history and delivery run after releasing `offerMu`; stop admission and lifecycle retirement are atomic under the owner mutex. A captured predecessor remains immutable when a transition overtakes admission (`TestDaemonLiveCaptureTransitionOrdering`).
- 2026-10-10 (finding 2): Generation advancement releases cached source bindings. Returning to A after A → Z → A creates a fresh successor binding without changing already captured A values (`TestDaemonLiveReturningRoutingID`). Incarnation high-water marks reject retired activations without retaining their bindings.
- 2026-10-10 (finding 3): Main identity minting also covers unassociated progress and denial, matching the emitter's turn-opening contract while preserving lifecycle-neutral busy classification and absent phase sends (`TestDaemonLiveNeutralMainTurnIdentity`).
- 2026-10-10 (finding 4): Stop releases source/turn lifecycle maps; generation advancement discards predecessor revision maps. Late source admission cannot allocate retired bookkeeping. Both compositions include live-owner cleanup in the existing conversation-removal observer. Detached cursors survive cleanup; a single generation high-water counter seeds recreated conversations to suppress old readings without accumulating deletion tombstones. `TestDaemonLiveCompletedActivationStorage`, `TestDaemonLiveConversationRemoval`, and `TestDaemonLiveNoRelayRemoval` cover bounded storage and removal.

### Rework concurrency and security account
The owner remains a leaf lock with no resolver, delivery, history or filesystem operations under it. Routing capture and generation ordering now share the outer acceptance lock, including previously filtered notifications. Detached predecessor sources and immutable cursor records do not require retired maps to stay alive. Missing/deleted conversations and stale generations cannot recreate turn/revision bookkeeping; late transitions resolve ownership outside the owner lock before creating state. No new goroutine, credential, subprocess, cryptographic or network operation is added. Security self-review verdict: PASS after closing the attribution race and lifecycle memory-retention findings. Relay installation, activation and encrypted-wire proof remain pending #3077.
