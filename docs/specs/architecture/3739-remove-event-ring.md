# Remove unused event-ring storage

## Files read

- `cmd/pyry/interactive_turn_v2.go`, `interactive_live_publication.go` → `interactiveTurnEmitterV2`, `publishLiveState`: ring ownership, live fanout and envelope ordering.
- `cmd/pyry/runtime_boundary.go`, `stream_turn_drain.go` → `installRuntimeHistory`, `streamTurnSink`: unused ring alias beside retained source recording.
- `cmd/pyry/relay.go` → `startRelay`, `startRelayV2`, `dropRingOnConversationDelete`: both relay postures and composed removal ownership.
- `internal/relay/v2session.go`, `v2session_snapshot.go` → `V2SessionManager`, `SetReplaySource`: inert storage inputs; snapshot refusals remain supported.
- `internal/eventring/ring.go`, `ring_test.go`: exclusively retired replay storage and tests.
- Emitter history/provenance/content-retirement tests, `live_history_id_test.go`, `ordinary_queue_placement_test.go`: retain durable facts, metadata and FIFO assertions independently of the ring.
- Relay recovery/thread/catch-up tests and daemon thread/live/suggestion removal tests: retain authenticated recovery, isolation and cancellation coverage.
- `docs/knowledge/features/eventring-package.md` → **Where the durable id is assigned**, **Ownership & wiring**: obsolete production ownership claims.
- `docs/knowledge/features/conversations-registry-crud.md` → **SetOnDelete**: one observer must compose every surviving cleanup.
- `docs/knowledge/features/thread-package.md`, `thread-package-shadow-lifecycle-and-evidence-coordination.md`: removal joins thread demand; relay-disabled cleanup is equally required.
- `docs/knowledge/features/history-package.md`, `relay-package.md`, `development-verification.md`, `CODING-STYLE.md`: durable IDs are separate from delivery IDs; preserve surviving assertions when removing fixtures.
- `docs/specs/architecture/3738-retire-event-replay.md`, parent #3719 and ADR 042 **Update messages**: replay admission is merged; explicit per-conversation recovery remains authoritative.
- `docs/protocol-mobile.md` → **Security model**: authenticated routing, confidentiality and nonce ownership remain unchanged.

## Context

One deliverable removes the unused event-ring storage behind already-retired
replay admission. Source history, live-state delivery and composed conversation
removal remain supported. Parent #3719 owns final integration. No deployment or
data migration is authorized, and existing holds and failed evidence remain.

Estimate: approximately 500–700 written lines including fixture migrations and
this plan, plus obsolete storage/test deletions; zero new exported types, four
acceptance criteria and zero new reject branches. The raw setter/observer
consumer count exceeds ten, as recorded by the existing `needs-human:sizing`
label and depth-two exception (#3714 → #3719 → #3739). Keep this cohesive slice.
Remote #3240 overlaps `V2SessionManager` additively; it supplies no dependency.

## Design

Remove the ring allocation/field, sink alias, manager fields and setter, ring
arguments and ring-only configuration, then delete `internal/eventring`.
`publishLiveState` keeps one logical timestamp, durable `HistoryEntryID` and
monotonic per-connection fanout IDs; it never assigns ring-derived `EventID`.
Keep the emitter constructor signature and generic `Envelope.EventID` schema.
Retain existing projection, model-list, prompt and correlated-reply gates.

Rename the composed registry observer to `cleanupOnConversationDelete`, removing
only its ring argument/drop. Suggestions are forgotten and pending inference
canceled; stream/live and thread owners are removed and joined in both relay
postures. A nil registry remains a no-op. Adapt mixed tests to inspect durable
history or actual publication, removing only obsolete ring/replay assertions.

## Concurrency model

No new goroutine or lock. The drain owns emitter counters; manager Run owns
delivery/ciphers. Registry callbacks remain outside the registry lock and call
the same surviving owners in the same order. Existing cancellation/join paths
and bounded queues remain intact.

## State transitions and identity reuse

| Event | Required race regression |
| --- | --- |
| Direct deletion and idle sweep; another conversation survives | `TestConversationDeleteCleanup` |
| Nil registry | `TestConversationDeleteCleanup_NilRegistry` |
| Thread deletion and reused conversation identity | `TestThreadShadowRemoval` |
| Pending inference canceled on removal | `TestReplyFallbackRegistryRemovalCancellation` |
| Stream/live owner deletion and recreation; relay disabled | `TestDaemonLiveConversationRemoval`, `TestDaemonLiveNoRelayRemoval` |
| Reconnect and arbitrary retired cursor values | `TestV2Session_LastEventIDIgnored`, `TestThreadRecoveryAfterReconnect` |
| Successful, nil and failed source storage with multiple recipients | `TestLiveProducers_HistoryEntryID` |
| Transport-down/reconnect and rekey | existing focused push/inline nonce/rekey tests |

## Error handling

No new failure modes. History append failure still publishes supported state
without `HistoryEntryID`; source content remains retired. Snapshot/thread
refusals stay generic. A failed peer push cannot stop other recipients.

## Testing strategy

First strengthen the live-history regression to reject ring-derived IDs and
observe its failure. Migrate ring fixtures while retaining history payload,
timestamp, provenance, isolation, ordering and lifecycle assertions. Run focused
affected Linux race checks through `scripts/test-admission.py`, `go vet ./...`
and a daemon build into `/tmp/builder-3739/`. Compile affected integration
packages under `e2e`, `e2e_realclaude` and `e2e_liverelay` without running live
tests. Record candidate, commands and test counts in the PR. Complete configured
acceptance and release/live gates remain pending for the dispatcher/verifier.

## Open questions

None. Remove only ring-exclusive configuration; shared metadata stays.

## Documentation handoff

Pending for the documentation stage:

- Retire current-production storage/API/wiring claims in `docs/knowledge/features/eventring-package.md`, especially **Where the durable id is assigned**, **Ownership & wiring** and **Files**. State: “The daemon no longer retains an event ring or assigns ring-derived event IDs. Durable source history and HistoryEntryID remain separate; content recovery uses explicit per-conversation thread catch-up/pages/reset.”
- Update `docs/knowledge/features/conversations-registry-crud.md`, **SetOnDelete**, to describe surviving cleanup without ring Drop.
- Reconcile ring references in `docs/knowledge/features/thread-package.md`, **Shadow lifecycle and evidence**, `docs/knowledge/features/protocol-package-types-envelope.md`, and the storage paragraph in `docs/knowledge/features/v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md`; preserve supported envelope metadata and source-history distinctions. #3719 integrates this with #3738's reconnect handoff.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `handleNoiseInit` still authenticates paired devices before admission; thread handlers validate conversation/host scope. Removing an inert cursor grants no new authority. Keep ignored-cursor and scoped recovery tests.
- [Tokens/secrets] No changes to credential generation, storage or revocation; no new payload or credential logging.
- [File operations] `appendConversationHistory` retains durable storage and provenance; only in-memory ring storage is removed. No file migration or new filesystem boundary.
- [Subprocesses] No process/environment changes; tagged compilation uses existing fixtures without launching live Claude.
- [Cryptography] SHOULD FIX: preserve transport-down no-nonce-consumption and rekey regressions when removing fixtures; Run remains the sole cipher owner.
- [Network/I/O] Existing frame caps, bounded ordered queues and live-state projection stay. SHOULD FIX: assert supported fanout still works with absent/failed storage and no event ID; retain model-list/correlated reply tests.
- [Errors/logs] Existing static refusal messages and content-free push diagnostics remain; storage failures cannot add content to logs.
- [Concurrency] SHOULD FIX: preserve the composed deletion callback in both relay postures; test direct deletion, sweep, pending cancellation and recreated identity so stale work cannot attach to it.
- [Threat model] Noise confidentiality, authentication, host isolation and input trust remain at their existing boundaries. Deployment/hold release and final integrated acceptance are OUT OF SCOPE here and owned by #3719 and the dispatcher.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-23

## Revisions

- 2026-10-23: `mergedForConn` used `EventID` to distinguish canonical model pushes from projected replies. The retained multi-agent regression fails when its ring ID is removed. `forwardEnvelopeSource` now passes projection eligibility explicitly: unsolicited ordinary pushes and explicitly unprojected supplied readings are canonical; correlated replies and provider-projected readings keep their bytes. Preserve generic envelope metadata, thread live-state validation and all capability/size gates. Mixed source tests compare actual per-recipient envelopes with durable records; zero-recipient tests assert source append counts directly.
- 2026-10-23: Connect-time `RetainedModelLists` values are already projected but have no reply correlation. Preserve that distinction with an internal `queuedEnv.modelListProjected` flag carried under the existing queue lock, without skipping delivery validation or changing wire fields. `reconcileModelLists` sets it; legacy supplied readings derive it from `ModelListUnprojected`. Ordinary pushes default to canonical. Existing `TestV2Session_ModelListReconcile_PassesTheConnsMultiAgentDecision` pins the no-double-merge contract alongside the ID-free live-push regression. No new lifecycle, lock or goroutine is added.
