# Runtime session boundaries

## Files read

- `cmd/pyry/session_transition_v2.go` → `capture`, `broadcast`, `publishSwitch`: captured ownership and legacy switch sealing.
- `internal/sessions/transition.go` → `SessionTransition`, `RotateForNewSessionWithHandoff`: distinct causes and optional outcome already exist.
- `cmd/pyry/interactive_turn_v2.go` → `HandleFor`, `startTurnIfNeeded`, `closeForConversation`: retained source state, buffered text and child isolation.
- `cmd/pyry/stream_turn_drain.go` → `offer`, `takeStopped`, `startStreamTurnDrainV2`: single writer and retained stop ordering.
- `cmd/pyry/channel_delivery.go` → `drain`, `held`, `lockPostBoundary`: existing publication gate shared with interactive writes.
- `cmd/pyry/queued_message_placement.go`, `operator_message_history.go`, `send_now.go`: echo/idle placement and confirmation-only provenance.
- `cmd/pyry/new_session_starter.go`, `main.go`, `relay.go`: actual wrap-up outcome and both transition installations.
- `docs/knowledge/features/history-package-producers.md`: delayed facts must preserve captured attribution; nil/failed stores cannot suppress legacy publication.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: retained stops order after parsed tails.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, ADR 042: behavioral tests, content-free errors and hidden durable facts.

## Context

Legacy delimiters cannot describe runtime divider causes or interrupted work. Exit cleanup currently retires every source in the conversation, including replacement work. This implements ADR 042's existing decision; no new decision record is needed. Workspace change remains vocabulary and recovery introduces no production caller.

Sizing: approximately 1100–1300 written lines, zero exported types, four acceptance criteria, fewer than ten consumer changes. This exceeds the line ceiling. Actual lineage is #2959 → #2968 → #3013; the grandchild rule requires building, with `needs-human:sizing` recorded. No overlapping remote feature branch touches the planned files.

## Design

Add local history-only facts for a main-turn opening, interrupted tool/turn endings and a session divider. Payloads carry conversation, existing turn/tool identities, cause, occurrence time and captured source; dividers retain both routing/agent identities and optional actual reset outcome. Openings are hidden, interrupted endings shown, and only sleep dividers hidden. Capacity eviction keeps its own cause. Legacy fact eligibility and payload meanings stay unchanged.

Retain unfinished ordinary main tools per source, excluding Agent/Task launchers and child tools. Results and denials retire tools; a normal turn end retires the turn. Source closure flushes buffered text, writes interruptions once, then retires state. A sealed predecessor may still contribute delayed legacy facts with its captured provenance but cannot open another main turn. Same-session exits close only work preceding their exit epoch, without sealing future replacement work or writing a divider.

Route runtime boundaries through the stream drain before publication. Retain ordinary callbacks behind their accepted output watermark without blocking the callback. Preserve captured transition pairs rather than coalescing by conversation. The drain owns the pre-divider closure point, appends the new divider and retains legacy transition publication. Committed switches wait through this publication and the existing row/transport sealing barrier. Coordinate channel writes using their existing gate and pending-boundary holds; confirmation-only operator writes use drain placement. Both relay configurations install the same runtime path.

Preserve existing constructor/test surfaces. Wire the optional handoff-aware rotation callback only where `resetThenRotate` has the actual wrap-up boolean; legacy callers keep unknown.

## Concurrency model

Emitter lifecycle remains drain-owned. Boundary enqueue takes only a short in-memory lock and signals a wake; it performs no storage or network I/O. Boundary consumption and interactive/channel publication use the existing post gate. Never wait for the drain while holding that gate. Exit epochs distinguish older stops from replacement activity. All drain/transition workers and placement waits stop on daemon context cancellation.

## Error handling

Unknown causes and unresolved ownership do not invent facts. Invalid/absent reset outcomes remain absent. New facts use the existing best-effort append helper and content-free failure discriminants. Storage failure does not change sealing or legacy delivery. New facts never enter legacy pages, replay or live fanout.

## Testing strategy

Write failing tests for cause/visibility and outcome mapping, thinking-only openings, unfinished versus completed/denied tools, exactly-once source closure, ordered old text/closure/divider/successor writes, captured A→B→C ownership, other-conversation isolation, same-session replacement exits, and legacy exclusion with nil/failed storage. Exercise operator and channel writes at the shared boundary. Run `go test -race ./cmd/pyry/...`, `go vet ./...` and `go build ./cmd/pyry`; the verifier owns `make check`.

## Open questions

None. Agent/background-task closure remains #2969; daemon-start reconciliation remains #3014.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/history-package-producers.md`, “Producers (#2114, #2115)”, linked from `history-package.md`. Describe runtime divider causes/visibility, actual versus unknown reset handoff outcome, old-source closure before dividers, same-session crash closure, hidden durable main-turn openings and legacy exclusion. State that workspace change is vocabulary without a current producer and recovery adds no production self-heal policy.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: captured daemon routing/ownership remains authoritative; unresolved ownership drops. Tool identities originate at the existing parser and stay scoped to their source and conversation.
- Tokens/cryptography: no credentials, keys or crypto contract changes; turn identities retain `conversations.NewID`.
- File operations: reuse `history.Store` containment, modes and append validation; no new file-path input or durable store.
- Subprocesses: no command, argument or environment changes.
- Network/I/O: new facts stay outside legacy fanout/replay; existing authenticated transport and recipient gates are retained.
- Errors/logs: SHOULD FIX: use only the append helper's failure discriminants, never tool input, thought text, payloads or filesystem errors. Opening facts contain identity only.
- Concurrency: SHOULD FIX: boundary callback must not wait on the publication gate; drain dispatch must not wait while holding that gate. Preserve switch cancellation and sealing waits.
- Threat model: no new remotely callable verb or authorization oracle; startup reconciliation (#3014) and child lifecycle endings (#2969) are out of scope.

**Reviewer:** builder (self-review)
**Date:** 2026-10-08

## Revisions

- 2026-10-08: ordering tests required per-source accepted-output watermarks rather than the whole queue's tail for transition facts; confirmation placements retain the whole-queue fence. Channel holds survive removal from the pending queue until publication finishes. Evictions additionally wait for a consumed producer stop, retaining late parsed tails and the actual eviction cause. Confirmation-only dispatch is activated only with runtime history, preserving older test entry surfaces. Untagged legacy callers retain their single source fallback; production callbacks capture the last output source independently of a rotated routing tag.
- 2026-10-08: a successor denial can open a turn without a running phase, so runtime cleanup checks retained open turns rather than phase projection before releasing the publication gate. Sealed predecessor events bypass busy and idle-placement mutation while retaining their legacy provenance. Tests cover late predecessor endings and stale same-session exits.
- 2026-10-08: sealed sources retain their closed turn address for delayed legacy facts. Late text keeps the existing chunk-size bound and sequence progression without opening work again; the regression test crosses the chunk-size boundary.
- 2026-10-08: `streamSessionTag.Rotate` captures the retiring routing ID until its exit callback consumes it. A final old-child event stamped after rotation can update the last-output source, so that reading alone cannot identify the dying predecessor. Announced in-band clear does not mark a retiring child.
