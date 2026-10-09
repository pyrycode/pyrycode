## Files read
- `cmd/pyry/main.go` → `runSupervisor`: instance ownership, startup reconciliation and producer joins.
- `cmd/pyry/relay.go` → `dropRingOnConversationDelete`, `startRelayV2`: single registry observer and legacy publication.
- `cmd/pyry/channel_delivery.go` → `held`, `pendingFor`, `deliver`: publication gate, pending writes and direct history appends.
- `cmd/pyry/runtime_boundary.go` → `boundaryPending`: closure/divider facts remain pending until publication.
- `internal/history/forward.go` → `ForwardReader.Tail`: raw committed tail, notification registration and captured metadata.
- `internal/thread/store.go` → `Load`, `Snapshot`, `Unload`, `Shutdown`: independent replay, joined retirement and final persistence.
- `internal/thread/fold.go` → `Items`: unresolved children are omitted from public items; unload eligibility must include private active items.
- `docs/knowledge/features/thread-package.md` and `thread-package-background-store.md`: caches cannot seed private continuation; worker contexts must survive producer cancellation.
- `CODING-STYLE.md`, ADR 042 and `development-verification.md`: ownership, shadow migration and deterministic evidence.

## Context
Wire the existing history-owned thread cache in shadow. History remains the sole durable record and the legacy protocol remains the only publication path. No new decision record is needed.

## Design
A daemon-local shadow owner discovers registered conversations on a background tick and starts independent coordination per conversation. It uses the same history store as every producer, including direct channel delivery. Startup starts discovery after reconciliation without waiting for replay. Discovery compares committed history versions with scalar unloaded progress; later appends reload a retired worker.
Coordination uses Store Load/Snapshot/Retry/Unload. Snapshot gains an active-work flag computed from all private fold items so unresolved children cannot permit early unload. Once usable and caught up, a worker unloads only under a quiescence decision using the existing channel publication gate, pending runtime boundaries, queue items and pending channel writes. Unload I/O runs after releasing that gate; an append racing retirement causes rediscovery from the retained consumed version.
The existing removal observer additionally retires and joins shadow coordination. A removed-ID tombstone and registry membership checks prevent stale discovery and late callbacks from reopening removed work. No new observer replaces ring or suggestion cleanup. Relay-disabled composition installs the same observer.
Shutdown seals and joins existing producers first. Detached shadow contexts survive daemon cancellation. Shadow shutdown stops discovery, asks each remaining coordinator to reach its final committed history version, joins coordination, then calls Store.Shutdown. Read/fold failures terminate final waiting and persistence failure never produces a clean-cache diagnostic. Nil shadow dependencies are inert.

## Concurrency model
One joined discovery goroutine and one joined coordinator per registered conversation; Store owns the existing reader/fold goroutines. Short owner locks protect identity and tombstones. Registry and publication gates never surround replay/cache I/O or joins. Producer cancellation precedes shadow cancellation, including error-return defers.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Startup/refold and interleaved commits | `TestThreadShadowDiscovery` |
| New/direct-post activity and unload/reload, retirement append | `TestThreadShadowReload` |
| Active/queued work and pending idle boundary | `TestThreadShadowQuiescence` |
| Removal during rebuild/tail, stale discovery | `TestThreadShadowRemoval` |
| Final commits, cancellation and persistence failure | `TestThreadShadowShutdown` |
| Paused 36,000-entry refold and unrelated legacy publication | `TestThreadShadowLongReplay` |

## Error handling
Use validated conversation IDs and fixed reasons only. Failed history discovery is retried on the next tick; unavailable folds retry in the background. During final draining, unavailable/history failure ends that conversation's wait; Store.Shutdown determines clean persistence. No error or payload/path is logged.

## Testing strategy
Write daemon coordinator tests first using real history and thread stores, with injected lifecycle barriers at the daemon/store admission boundary for deterministic ownership races. Compare settled snapshots with full raw replay; exercise actual producers and legacy broadcaster/replay/history-page output with shadow enabled/disabled. Reuse existing Store replay/handoff and cache-security tests. Run race tests for touched packages, recorded agent/shell replay, go vet and go build. The verifier owns make check/full-module gates.

## Open questions
None. Estimate: at most 800 written lines, no new exported types/interfaces, fewer than 10 consumer updates, five acceptance criteria and fewer than 10 reject branches. No overlapping remote feature branches touch the planned files.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/thread-package.md`, “Shadow lifecycle and evidence”: production writers, background startup replay, discovery after unload, quiescent/idle-sleep unload, reload and shutdown. State that no app receives items yet and retained authenticated real-log evidence is #3068's deliverable.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: discovery accepts only valid registered IDs; raw entries retain recorded provenance and existing Fold validation.
- Credentials: no new credentials or capabilities; shadow has no broadcaster or protocol capability wiring.
- File operations: reuse history containment checks and Store owner-only, no-follow atomic cache persistence.
- Network/I/O: background workers perform cache/replay I/O, never publication callbacks; protocol handlers remain unchanged.
- Concurrency: tombstones prevent removed identity reuse; joined detached contexts retain instance ownership through final persistence.
- Logging/errors: diagnostics use fixed failure reasons and validated IDs, never underlying errors, content, payloads or paths.
- Resource lifetime: per-conversation unload releases private fold state; shutdown joins all coordination and Store workers.
**Reviewer:** builder (self-review)
**Date:** 2026-10-09
