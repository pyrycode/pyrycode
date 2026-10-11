# #3163 — daemon retained live-reading delivery

## Files read
- `cmd/pyry/daemon_live_state.go` → `admitLocked`, `advanceLocked`, `snapshot`, `remove`: immutable source records and per-generation/per-identity ordering.
- `cmd/pyry/daemon_control_live.go` → `complete`, `offer`, `liveInventoryResult`, `runSettingsReading`, `UpdateLive`: retention acceptance differs from valid provider availability.
- `cmd/pyry/daemon_live_inventory.go` → `cachedModelVocabularySource`, `inventoryResultSource`: bootstrap vocabulary fallback preserves foreign producer evidence; the reply adapter supplies target ordering without retention.
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
| Borrowed bootstrap vocabulary across matching generations and reused-ID clears | `TestDaemonLiveRelayBorrowedBootstrapVocabulary` |
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
- Trust boundaries: MUST FIX addressed in rework: retain unprojected model inventories with their captured source, and mark the daemon relay adapter for per-recipient projection at delivery. Legacy provider replies retain their existing projection and bytes. The relay leaves model clears empty and validates the projected envelope size. MUST FIX addressed in design: only captured eligible prompt shows enter delivery; original answer IDs and existing authorized resolver gates remain authoritative. `liveWithheld` checks source conversation and agent/access gates.
- Trust boundaries (2026-10-11 bootstrap fallback rework): MUST FIX addressed: `inventoryResultSource` preserves foreign cache evidence unchanged. `liveInventoryResult` uses the target request's captured positive generation/revision for borrowed vocabulary replies, carrying the original bootstrap session tag. `daemonLiveOperation.replyOnly` prevents retention/publication even if source and target generation numbers coincide. This ordering describes the request, not a newly observed target inventory; same-conversation predecessor evidence keeps its producing generation and stale suppression. Capability projection and target-conversation access gates remain relay-owned.
- Tokens/secrets and cryptography: existing device authentication and Run-owned Noise encryption remain unchanged; adapters introduce no credentials or nonce ownership.
- Files/subprocesses: no new filesystem or subprocess operation; existing settings persistence and bounded query validation remain in their adapters.
- Network/I/O: SHOULD FIX: exercise full encoded-envelope bounds and bounded handoff overload recovery; no new listener or network read.
- Errors/logs: delivery diagnostics carry only fixed event names and event types; no payload or evidence bytes.
- Concurrency: SHOULD FIX: verify progress with held lookup/publication, removal/reuse, and cancellation/join; queued values must remain immutable.
- Threat model: paired hostile requests retain existing authorization and shape checks. Production thread activation/history delivery is OUT OF SCOPE, owned by #3164; catch-up/pages by #2963.
**Reviewer:** builder (self-review)
**Date:** 2026-10-11

## Revisions
- 2026-10-11: the permission wire proof exposed a legacy dismissal reaching thread recipients before source-bearing delivery. With `ThreadLiveState` installed, unsolicited legacy live frames are withheld from thread recipients; legacy connections remain unchanged.
- 2026-10-11: the removed-snapshot wire proof exposed a detached cursor surviving owner removal. Add optional bounded `LiveStateCurrent` admission at Run delivery for unsolicited readings, including cursors and previously queued publication. Correlated replies preserve captured source and existing relay ordering. Publisher cleanup also releases buffered records after joining.
- 2026-10-11: final lifecycle proofs additionally use `TestDaemonLiveRelayRetiredSnapshot`, `TestDaemonLiveRelayConnectOvertaken`, `TestDaemonLiveRelayInventory`, `TestLiveStateCapturedEligibility` and `TestRetireLiveState`. They cover removal between snapshot capture and delivery, connect/live overtaking, bootstrap parser ingress, detached eligibility and consumer queue retirement.
- 2026-10-11: written-work recount is approximately 1000 lines including the plan and tests, matching the recorded grandchild sizing exception. New exported types/interfaces remain zero; optional metadata/config fields and the retirement method require no fixture or legacy-constructor migration.
- 2026-10-11: cleanup cancels and joins before detaching the publisher pointer, preventing a concurrent installation from overlapping an exiting worker. No source/owner lock is held during the join.
- 2026-10-11: `TestDaemonLiveRelayReservedQuerySnapshot` failed when delivery compared a retained reading to an uncompleted query reservation. For ordinary unsolicited readings, `LiveStateCurrent` now checks the retained record's revision; dismissal ordering uses its terminal reservation and clears still use current generation. A failed/refused query leaves prior accepted state available. `TestDaemonLivePublicationShutdownInstallation` covers concurrent installation during shutdown.

- 2026-10-11: verifier finding 1: drain one bounded publication batch (one selected reading) before refreshing recipients. Selection precedes enumeration, so a later connection captures this reading or receives a subsequent queued replacement. `TestDaemonLiveRelayPublisherConnectGap` holds delivery while a second authenticated connection captures an overtaken snapshot.
- 2026-10-11: verifier finding 2: remote answer/cancel captures prompt owner and correlation with the registry consume under `streamApprovalBridge.mu`; retirement takes the same bridge-to-modal registry lock order. Actuation, history and source-bearing dismissal use the detached winning capture after unlocking, even if retirement or permbridge timeout removes the parked approval. `TestDaemonLiveRelayPermissionRetirementGap` covers allow, reject and cancel with consume held across concurrent retirement.
- 2026-10-11: verifier finding 3: `modelListResultFor` retains the unprojected Claude inventory and projects only its legacy return payload. `daemonRelayReading` marks this canonical inventory with optional metadata; relay projection uses each connection’s negotiated capability for snapshots, pushes and correlated replies. Existing supplied providers without that metadata keep their projection contract. `TestDaemonLiveRelayModelProjection` covers both capability choices, connect/live and requested-result fanout, canonical retention, captured source and envelope fit.
- 2026-10-11: the finding 3 fallback audit added `TestDaemonLiveRelayCodexOnlyProjection`: canonical empty Claude inventories from a successful Codex-only request are withheld from basic recipients before generating clears. The legacy basic provider retains its unavailable result. Requested fanout now covers both requester capability choices; projected clears stay `{}`.
- 2026-10-11: `TestStreamApprovalBridge_InteractionRequiredRefusalSurvivesConcurrentRetire` rejects holding the bridge lock across a held registry consume. This supersedes the finding 2 lock-order revision: remote resolution and retirement both capture their owner before consumption, release the bridge lock, and only use that detached capture if they win the registry one-shot. Eligibility reads, source writers and publication remain independent of registry latency.
- 2026-10-11: verifier finding 1 on `d003f5c8`: borrowed bootstrap vocabulary was available with generation zero and silently rejected by `PushLiveState`. Preserve foreign cache evidence in `inventoryResultSource`; combine its original session tag with the target operation's captured generation/revision in `liveInventoryResult`, marking that operation reply-only. Completion cannot replace target retention or publish borrowed state; delayed replies stay ordered against later clears without recapturing a successor. `TestDaemonLiveRelayBorrowedBootstrapVocabulary` exercises actual parser/hold ingress and authenticated decrypted replies for basic/multi-agent clients, unchanged legacy payload bytes, no target retention, reused-ID clear and predecessor suppression. No new exported types, interfaces or simultaneous provider consumers are required; this rework adds approximately 120 written lines within the recorded grandchild sizing exception. Refreshed feature branches have no overlap with these rework files.
