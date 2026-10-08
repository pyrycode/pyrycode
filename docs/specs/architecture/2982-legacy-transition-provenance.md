# Legacy transition provenance (#2982)

## Files read

- `cmd/pyry/session_transition_v2.go` → `Enqueue`, `publishSwitch`, `broadcast`, `toWirePayload`, `startSessionTransitionStreamV2`: queue, publication order, routing and legacy payload mapping.
- `cmd/pyry/relay.go` → `startRelay`, `startRelayV2`, `relayWiring.sessionHarness`: relay and history-only observer installations.
- `cmd/pyry/pool_adapters.go` → `sessionHarness`: exact-session lookup through `Pool.HarnessFor`, without conversation fallback.
- `internal/sessions/transition.go` → `SessionTransition`, `PublishSwitchTransition`, `notifyEviction`: captured ownership and switch-agent facts; callbacks run off pool locks.
- `internal/sessions/pool.go` → `HarnessFor`: live/dormant exact-session lookup without starting a child.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`: optional provenance alongside visibility, nil/failed-store delivery.
- `internal/history/log.go` → `Entry`, `SessionProvenance`, `Page`, `LatestEntryID`: metadata validation and raw durable reads.
- `cmd/pyry/session_transition_v2_history_test.go`, `session_transition_v2_test.go` → history cardinality, routing drops, capability gate and nonblocking enqueue tests.
- `cmd/pyry/agent_switch_publication_test.go` → `TestRelayAgentSwitchDelayedPublication`, `TestRelayAgentSwitchPublicationQueuePressure`: reset exclusion and sealing order.
- `docs/knowledge/features/history-package.md`, `history-package-producers.md`: source capture, legacy metadata compatibility and absent-source convention.
- `docs/knowledge/features/sessions-package.md`, `sessions-package-key-types-transition-observer.md`: captured facts must survive delayed delivery; committed switches use their dedicated lane.
- `docs/knowledge/features/development-verification.md`: deterministic delay witnesses and raw protocol assertions.
- `CODING-STYLE.md`, `docs/knowledge/decisions/042-daemon-built-thread.md` § Sessions, agents, messages, read marks, `docs/protocol-mobile.md` § Security model: persistence, provenance and remote threat boundaries.

## Context

The emitter resolves ownership at drain time and writes no session metadata. A delayed rotation/eviction must retain its captured conversation and exact producing session. ADR 042 already records the decision; no new decision record is needed. No overlapping remote feature branch touches the two production files at planning time.

## Design

Add an optional exact-session agent resolver on the emitter. At `Enqueue` and `publishSwitch`, capture missing agent facts into the transition value before queueing. Only `ReasonClear` uses `NewID`/`NextAgent`; only `ReasonEviction` uses `PreviousID`/`PreviousAgent`. Existing nonempty facts take precedence; absent IDs, missing lookups or unsupported agents yield absent history provenance, never `none` or a Claude default.

Capture missing conversation ownership at handoff through the existing exact-session resolver. Nonempty `ConversationID` is authoritative. `broadcast` uses that captured ID first, retaining the legacy exact-session fallback for uncaptured direct callers and unresolved-event drop if ownership remains unavailable. It never looks up an agent at drain time. `toWirePayload` carries captured conversation ownership while retaining all existing wire reason/ID meanings. Append valid captured `history.SessionProvenance` through the common history seam.

Keep `newSessionTransitionEmitterV2` and `startSessionTransitionStreamV2` compatible. The latter delegates to a helper accepting the optional exact-session lookup; both relay and no-relay production installations call that helper with `w.sessionHarness`. No exported sessions contract changes or new fact producers.

Sizing: one deliverable, approximately 400 written lines including plan/tests, zero new exported types/interfaces, two production installation updates, three acceptance criteria, fewer than ten rejection branches. All five builder limits hold.

## Concurrency model

Capture runs synchronously off pool/session locks, using the existing bounded registry/pool lookups. Ordinary enqueue retains its nonblocking buffered send and drop-on-full policy. The dedicated switch lane retains daemon cancellation, broadcast/row ordering and sealing acknowledgement. No new goroutine or shutdown path; emitter Run remains context-owned.

## Error handling

Unknown reasons and unresolved ownership still drop. Missing/unsupported agent facts omit metadata while eligible legacy delivery continues. Nil/failed history storage still yields no history identity and continues fanout; no payload/session/agent facts are added to logs.

## Testing strategy

Write focused regressions first and observe failures. Table-drive both reasons and both agent kinds, preserved captured agents, unavailable/invalid provenance, and missing IDs. Delay drain after handoff while replacing/removing lookup results; check authoritative routing, raw warm/reopened history metadata, unchanged old entries, visibility, watermarks and live payloads. Exercise both observer installations and dedicated switch publication. Move the existing delayed-switch witness to broadcaster snapshotting because authoritative captured ownership bypasses its old resolver gate. Existing enqueue/full, recipient, nil/failed-store and switch-order tests remain relevant.

Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-2982/pyry ./cmd/pyry`. The dispatcher/verifier owns `make check`; live Claude tests are unnecessary.

## Open questions

None.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/history-package-producers.md` § Producers (#2114, #2115), linked from `history-package.md`, document capture at observer/publication handoff, authoritative captured conversation ownership, clear-successor/evicted-session convention, and absent provenance when source facts are unavailable. State that delayed broadcast does not resolve a replacement session for provenance.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `SessionTransition` is an in-process pool fact; `sessionHarness` reads exact pool IDs. Only known `claude`/`codex` pairs enter metadata; no client or native-agent identifier supplies provenance.
- [Tokens, secrets, credentials] No token generation/storage/lifecycle changes. Routing and provenance identifiers stay out of new logs.
- [File operations] `appendConversationHistory` retains store ID validation, bounded paging and existing write behavior. No new path construction or permissions.
- [Subprocesses] Capture uses `HarnessFor` without launching or reviving a child; no command or environment changes.
- [Cryptography] Existing encrypted transport remains untouched; metadata is not an authorization or cryptographic input.
- [Network and I/O] `broadcast` retains existing interactive recipient gates and transport limits; no new network read or payload fields.
- [Errors, logs, telemetry] Capture failure omits provenance; content-free existing drop/append logs remain. Invalid agent facts must not poison an otherwise valid history append.
- [Concurrency] Lookups run off lifecycle locks, before queue/publication delay. Value capture prevents later binding changes from reattributing an event; switch sealing and context cancellation remain unchanged.
- [Threat model] This daemon-originated boundary adds no remote control input or agent-authored text. Existing pairing, encryption, client rendering and transport policy remain owned by their current components.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-08
