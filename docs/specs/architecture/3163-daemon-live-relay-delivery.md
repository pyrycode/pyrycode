# #3163 — daemon retained live-reading delivery

## Files read
- `cmd/pyry/daemon_live_state.go` → `admitLocked`, `advanceLocked`, `snapshot`, `remove`: immutable source records and per-generation/per-identity ordering.
- `cmd/pyry/daemon_control_live.go` → `complete`, `offer`, `liveInventoryResult`, `runSettingsReading`, `UpdateLive`: retention acceptance differs from valid provider availability.
- `cmd/pyry/daemon_live_source.go` → `acceptEventLocked`: admission precedes fan-in and preserves mapped identity.
- `cmd/pyry/modal_resolve_v2.go` → `Surface`, `surfaceQuestion`, answer/retirement paths: captured eligibility and one-shot dismissal ownership.
- `cmd/pyry/session_model_list.go`, `session_router.go`, `relay_context_usage.go` → `modelListFor`, `resolveBoundMCPStatusReading`, `GetLive`: existing single-work source results.
- `cmd/pyry/main.go`, `relay.go` → `runSupervisor`, `startRelayV2`, `runConfigFor`: production composition and late bootstrap installation.
- `internal/relay/v2session_livestate.go`, `v2session_push.go`, `v2session_conns.go` → `PushLiveState`, `forwardLiveState`, `handlePushOverflow`, `ActiveConns`: bounded delivery, access gates and relay-owned ordering.
- `cmd/pyry/agent_switch_frames_test.go`, `session_model_selection_test.go`, `daemon_control_live_test.go`: authenticated encrypted-wire and real producer fixtures.
- `docs/knowledge/features/streamsup-package.md` and its daemon-retained-live-state topic: cache/source pairing and source-lock ordering lessons.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § Supplied live-state ordering: provider selection and correlation do not confer authorization.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries and producer completion barriers: test complete encoded envelopes and await asynchronous handoff.
- `docs/protocol-mobile.md` § Security model; ADR 042: paired remote control with source-bound live state separate from history.

## Context
Install delivery of existing daemon-owned live state without enabling production thread negotiation. No decision record is needed. All prerequisite issues are closed, including #3157/PR #3182. Refreshed feature branches have no overlap with planned files.
Sizing: approximately 1000 written lines, zero new exported types/interfaces, fewer than ten simultaneous constructor/provider consumers, four acceptance criteria and fewer than ten new reject branches. This exceeds the line ceiling; actual lineage #2962 → #3077 → #3163 invokes the grandchild exception. Recorded `needs-human:sizing`; independently verifiable unsolicited delivery and requested-reading/settings delivery would be the split if another level were permitted.

## Design
Adapt detached `daemonLiveReading` to `relay.LiveState`, preserving all source/order/envelope fields. Prompt show delivery uses captured `Answerable`, excluding unavailable eligibility and interaction-required prompts; clear/dismissal delivery remains possible. Add captured eligibility to relay metadata so delayed snapshots do not re-read eligibility from current prompt state.
Install a snapshot factory spanning owner conversations, yielding one detached eligible reading per call. Install publication on the owner before starting producer consumers. Admission and successful operation retention hand immutable records to one bounded daemon publisher, independent of legacy delivery/fan-in. Prompt dismissals carry their original wire payload at the producing boundary; only the successful one-shot resolver publishes them.
The publisher has a fixed 256-reading handoff and one cancellation-owned goroutine. It enumerates recipients and calls `PushLiveState` for thread recipients outside source/owner locks. A handoff overflow retires thread recipients through relay's existing queue-overflow teardown; reconnect restores a fresh snapshot rather than allowing a silent gap. Removal discards pending readings by checking captured generation against current ownership; shutdown detaches publication, cancels and joins the worker. Legacy recipients retain their existing producer paths and bytes.
Expose source-bearing model/MCP providers beside existing signatures, with shared implementations rather than repeated queries. Settings composition shares one resolve/usage read. `GetLive`, inventory results, settings reads and `UpdateLive` supply the five relay providers. Provider availability is based on a valid returned reading, never `complete`'s retention boolean. Keep all existing query collapse, deferral, cancellation, enrichment, refusal/fallback and mutation validation behavior. Production `ThreadReady` remains nil.

## Concurrency model
Lock order remains `offerMu` → owner mutex; producer leaf locks may take the owner mutex. Handoff only performs a nonblocking send under owner locking. The publisher releases all source/owner locks before enumeration or relay calls. One child context owns the worker; cleanup detaches the owner, cancels, and joins it. Immutable queued records cannot be mutated after publication. Relay Run remains the only crypto writer and ordering authority.

## State transitions and identity reuse
| Event | Race-enabled proof |
|---|---|
| Stream/control update with legacy delivery held or fan-in full | `TestDaemonLiveRelayPublication` |
| Prompt show, answer, timeout, repeated retire; independent prompt IDs | `TestDaemonLiveRelayPrompts` |
| Connect snapshot overtaken by fresh push | `TestDaemonLiveRelayOrdering`; existing relay live-state interleaving tests |
| Clear then fresh, reused routing ID, successive generations | `TestDaemonLiveRelayOrdering`; existing returning-routing-ID proof |
| Cached predecessor reply / equal or overtaken result | `TestDaemonLiveRelayReplies`; existing inventory/control rework proofs |
| Two conversations and access/agent gating | `TestDaemonLiveRelayPublication`; existing relay access-gate proofs |
| Remove/recreate with same identity; publisher shutdown/reinstallation | `TestDaemonLivePublicationRetirement` |
| Bounded handoff saturation | `TestDaemonLivePublicationRetirement` |

## Error handling
Malformed/oversized source readings remain rejected before retention. Provider refusal and settings errors keep their current wire behavior; no legacy retry of selected live providers. Delivery errors are content-free best-effort diagnostics. Overload closes thread consumers to force reconciliation. Removal and shutdown abandon pending publication without resurrecting state.

## Testing strategy
Write focused tests first and observe failure before implementation. Reuse the Noise wire fixture and local/session producer fixtures with explicit test-only readiness. Check source tags, ordering, captured eligibility, dismissal payloads, correlation, one provider invocation/mutation and unchanged legacy bytes. Existing mapping bounds cover complete supported envelopes/clears; run them with the producer integration proofs. Run `go test -race ./cmd/pyry/... ./internal/relay/...`, `go vet ./...`, and `go build -o /tmp/builder-3163/pyry ./cmd/pyry`. The verifier owns the full-module gate; no live Claude turn is required.

## Open questions
None. Bounded handoff overload uses existing connection teardown/reconnect reconciliation rather than leaving active consumers with a gap.

## Documentation handoff
Pending documentation stage:
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md`, “Control and on-demand readings” and the closing relay handoff: describe installed snapshot/push/reply delivery, prompt dismissal delivery and preserved source evidence; replace the obsolete claim that #3109's regression is skipped.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, “Supplied live-state ordering”: link and describe installed daemon producer-to-relay delivery. State production `thread` negotiation still awaits #3164 and catch-up/pages remain #2963.

## Security review
**Verdict:** PASS
- Trust boundaries: MUST FIX addressed in design: only captured eligible prompt shows enter delivery; original answer IDs and existing authorized resolver gates remain authoritative. `liveWithheld` checks source conversation and agent/access gates.
- Tokens/secrets and cryptography: existing device authentication and Run-owned Noise encryption remain unchanged; adapters introduce no credentials or nonce ownership.
- Files/subprocesses: no new filesystem or subprocess operation; existing settings persistence and bounded query validation remain in their adapters.
- Network/I/O: SHOULD FIX: exercise full encoded-envelope bounds and bounded handoff overload recovery; no new listener or network read.
- Errors/logs: delivery diagnostics carry only fixed event names and event types; no payload or evidence bytes.
- Concurrency: SHOULD FIX: verify progress with held lookup/publication, removal/reuse, and cancellation/join; queued values must remain immutable.
- Threat model: paired hostile requests retain existing authorization and shape checks. Production thread activation/history delivery is OUT OF SCOPE, owned by #3164; catch-up/pages by #2963.
**Reviewer:** builder (self-review)
**Date:** 2026-10-11
