# Permission queue observations

## Files read
- `internal/relay/v2session_push.go` → `push`, `drainOnce`: admission is independent of return value, dequeue precedes forwarding.
- `internal/relay/v2session_modal.go` → `enqueue`, `popHead`, `queuedEnv`: delta eviction admits controls; the overflow latch does not itself reject later controls.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: each authenticated queue is a connection lifetime.
- `internal/relay/v2session.go` → `teardown`: discard precedes queue deletion; preserve barrier release.
- `internal/relay/v2session_livestate.go` → `PushLiveState`, `LiveState`: supplied conversation/generation/reading/revision establish source scope independently of envelope ID.
- `internal/relay/v2session_livestate_test.go` → `liveFixture`: use existing authenticated Noise fixtures.
- `internal/permbridge/observation.go`, `observation_test.go`: TryLock, bounded aliases and issued numerical metadata; registration/terminal meanings remain unchanged.
- `docs/knowledge/features/permbridge-package.md` § Isolated permission observation: independent session/conversation; originating epochs and explicit loss.
- `docs/knowledge/features/v2-session-manager-state-machine-concurrency-safe-unsolicited-push-push.md`: pre-seal drop/hold contract.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, `docs/protocol-mobile.md` § Security model: race checks, meaningful evidence, content confidentiality.

## Context
Mobile captures need evidence of real queue disposition. Neither a nil Push return nor queue admission establishes transport invocation or peer receipt. One deliverable, estimated 700 written lines including tests and plan; no new exported types, no consumer migration, four acceptance criteria and no new policy rejection branches. No decision record needed.

Unmerged #3214 overlaps `push`, `drainOnce` and `enqueue`; these hooks remain additive and local. Its capacity hold/requeue paths are absent from this base, so they are not consumed. Do not consume #3071. Forwarding/preparation (#3241), transport attempts (#3242), activation/output (#3226) remain handoffs.

## Design
Diagnostic-only `V2SessionManager.ObservePermissions(*permbridge.ObservationRecorder)` attaches under pushMu. nil disables future observations; retained entries keep their originating recorder and identity. Attachment during existing activity/replacement marks incomplete coverage. Each created queue receives a fresh recorder-issued connection alias; connection IDs are never lifetime keys. Private ordinary-build companions have empty state and inert hooks, including the cancelled-entry hook.

Add `ObservationIdentity.Envelope` (alias kind `e`) and optional source input to `Identity`, plus recorder-issued fresh connection identity and explicit incomplete marking. All six alias kinds share the existing 512 entries/20992 payload-byte budget and allocator ceiling; issued numerical metadata validation stays intact. Add only stages `queue_modal_shown`, `queue_modal_dismissed` and reasons `admitted`, `cancelled`, `byte_ceiling`, `hold_transport`, `hold_replay`, `quarantined`, `dequeued`, `discarded` (reuse `absent`). Stage counters remain bounded; two additional possible keys. Registry-only register/terminal meanings do not change.

Shown payloads must be class permission. Dismissals contain no class: record their modal identity, with permission membership established only by a known show join, never guessed from outcomes. Clear envelopes are excluded before payload decoding. Decode only modal/class/conversation identifiers; ignore display/content fields. Canonical session is drawn only from envelope SessionID; conversation from supplied provenance or available envelope/payload tags, never current binding. Malformed/missing identifiers are unknown. Ordinary Push has unknown source Envelope; reusable envelope ID is insufficient. Supplied LiveState source keys include conversation, source generation, reading, revision and envelope type, bounded before assembly; this joins the same source across clients without conflating reused IDs.

Store only recorder pointer, opaque identity and finite stage in retained queuedEnv. At push, determine admission from actual queue length change or delta eviction, then attach to the admitted tail. Rejection despite nil return records byte_ceiling. An already-latched queue can admit or reject; retained entries record quarantined and later discarded. drainOnce records transport/replay holds and quarantine without popping; popHead records dequeue. teardown records each retained permission discard before barrier cleanup. No transport-success claims.

## Concurrency model
Run retains Noise and queue key ownership. pushMu guards attachment, queue lifetime identity and queued observation values. Recorder hooks use only nonblocking TryLock under the leaf lock; recorder operations never acquire pushMu. No observer goroutine, I/O, callback or wait. Disabled diagnostic hooks return before parsing/alias allocation.

## State transitions and identity reuse
| Event | Race test |
|---|---|
| Admission, delta eviction, ceiling rejection, latched retention/quarantine/discard | `TestPermissionQueueDisposition` |
| Transport/replay hold then dequeue, reset during retention | `TestPermissionQueueHolds` |
| Two clients, teardown/reconnect with same connection ID, repeated envelope IDs | `TestPermissionQueueIdentity` |
| Unknown provenance, missing session, clear exclusion, hostile payload | `TestPermissionQueueUnknown` |
| Recorder replacement, saturation/contention, forwarding/Stop progress | `TestPermissionQueueLoss`, `TestObservationQueueContract` |

## Error handling
Observation failure only loses evidence explicitly through existing recorder loss flags. Context cancellation and absent queue errors retain existing results; byte-ceiling rejection retains nil. Exhausted aliases become unknown. No observation failure changes queue/drop/hold policy or approval.

## Testing strategy
Write diagnostic tests first and confirm they fail before hooks exist. Use liveFixture and existing queue fillers to exercise real Push/PushLiveState/drainOnce/teardown; assert queue state, decrypted wire output and stable opaque joins. Test additions to vocabulary/alias validation, ceilings and stalled recorder deterministically in permbridge. Run diagnostic and ordinary relay/permbridge race JSON suites; report named results and executed/pass/fail/skip counts. Run go vet ./... and go build ./cmd/pyry; the verifier owns the full-module gate. No live Claude run.

## Open questions
None. Upstream capacity observations are conditional on #3214 landing, which has not occurred in this base.

## Documentation handoff
Pending documentation stage: add **Permission delivery observations** to `docs/knowledge/features/v2-session-manager-state-machine-concurrency-safe-unsolicited-push-push.md`, covering diagnostic build/attachment, queue stage meanings, opaque join scopes, unknown identity and bounded loss. State explicitly that queue admission is not transport invocation or peer receipt; link the recorder contract.
Pending documentation stage: update **Isolated permission observation** in `docs/knowledge/features/permbridge-package.md` with consumed vocabulary/schema additions and resulting counters/limits, preserving registry-only registration/terminal meanings. Later siblings extend forwarding/transport coverage; daemon activation/output remains #3226.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: only finite queue stages and bounded identifier aliases enter Submit; modal content is ignored. Missing source identity stays unknown.
- Tokens/secrets/telemetry: no prompt, tool, command, instruction, credentials, pairing or exception fields enter identities/records; hostile identifiers are hashed and aliases validated.
- Files/subprocesses/network I/O: none introduced; attachment is explicit in-process diagnostic API, output acquisition belongs to #3226.
- Cryptography: Noise allocation/order unchanged; alias keys use existing SHA-256, not an authentication credential.
- Concurrency: only recorder TryLock under pushMu, no reverse acquisition; no new goroutines or waits. Record saturation cannot block teardown or forwarding.
- Threat model: existing authentication and confidentiality boundaries unchanged. Prompt injection and remote authorization policy are outside this observation ticket; activation/output is #3226.
**Reviewer:** builder (self-review)
**Date:** 2026-10-12
