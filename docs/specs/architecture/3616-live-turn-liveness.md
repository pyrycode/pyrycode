# Live turn liveness independent of response chunk counts

## Files read
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` → `TestInteractiveStreamLiveness`, `drainForCompletedTurnOptions`: live caller, ordered decrypt, milestones and global alarms.
- `internal/e2e/realclaude/interactive_stream_interrupt_test.go` → `TestInteractiveStreamInterruptStopsRunningTurn`, `drainForCancelledTurnEnd`: cancellation returns before queued idle; the subsequent healthy turn must still drain.
- `cmd/pyry/stream_turn_drain_test.go` → `TestStreamTurnDrainV2_FlushTimerCoalesces`, `feedLines`, `collectEnvs`: parser-to-emitter timer proof and bounded collection.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_TimerFlushMidMessage`, `TestInteractiveTurnEmitterV2_FlushAtTurnBoundary`: existing deterministic ordering proofs to retain.
- `internal/noise/noise.go` → `NewInitiator`, `NewResponder`, `CipherState`: credential-free paired transport states for controls.
- `docs/knowledge/features/e2e-realclaude.md` and `e2e-realclaude-test-infrastructure.md`: live evidence and explicit helper-process isolation.
- `docs/knowledge/features/development-verification.md` and `development-verification-prove-that-tests-distinguish-the-change.md`: assertions must distinguish invalid evidence and ordering.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-fan-in.md`: drain owns emitter state; cancellation must precede joining.

## Context
The live test conflates a working turn with provider-dependent chunk counts. This is a source-audit repair, without a reproduced live failure. One deliverable is a liveness proof with deterministic incremental coverage as its verification floor; separating those would weaken the repair. No decision record is needed.

## Design
Use a brief tool-free prompt and `drainForCompletedTurn` with its existing 120-second deadline. Remove the unused minimum-count variant. Preserve accumulation, matching non-whitespace delta then matching idle, leading-idle rejection, ordered decryption and both global alarms. Broaden only the test drain's phone parameter to a local `ReceiveBytes(time.Duration) ([]byte, error)` interface so controls run exactly the live drain over encrypted, in-memory frames.

Credential-free tagged controls use table cases and a test-helper subprocess for expected fatal outcomes. They cover single/multiple chunks, empty/whitespace, foreign delta/idle, absent closure, queued idle before any matching nonempty delta, subsequent valid closure, and global alarms. A small AST assertion ties the live test to the checked drain and fails on the old caller.

Extend `TestStreamTurnDrainV2_FlushTimerCoalesces` using one persistent parser: observe first timer-flushed text before feeding another fragment of the same message, observe the second flush before feeding result, then assert ordered full text, shared nonempty turn/conversation attribution, advancing sequence and terminal idle. Production parser, coalescing and routing remain unchanged.

Scope check: about 380 written lines including plan/tests/helper edits, zero production files, zero new exported types/interfaces, zero consumers requiring updates, three acceptance criteria, fewer than ten drain reject branches (no new ones). Refiner's ~320-line S analogue is consistent with this sketch. Refreshed feature branches have no overlap on the two existing edited files.

## Concurrency model
The controls have synchronous receive/decrypt state and no goroutines. Their child processes have bounded contexts and are reaped by `CombinedOutput`. The existing stream drain owns emitter state and timer; cleanup cancels its context before joining. Assertions observe envelopes rather than racing reads of emitter fields.

## State transitions and identity reuse
| Event | Race proof |
| --- | --- |
| First/multiple matching deltas then idle | `TestCompletedTurnDrainControls` |
| Empty or foreign evidence, missing closure | `TestCompletedTurnDrainControls` |
| Queued cancelled-turn idle before new response, then later idle | `TestCompletedTurnDrainControls`; retain live interrupt healthy-turn check |
| Same message across two timer windows then result | `TestStreamTurnDrainV2_FlushTimerCoalesces` |
| Cancellation and boundary flush | Existing affected drain and emitter tests |

## Error handling
Invalid evidence cannot complete the drain; controls require its milestone-specific fatal diagnostic. Unknown-message and unrecognized rate-limit statuses remain fatal even for foreign conversations; the measured warning status remains tolerated. Transport/decode failures remain fatal.

## Testing strategy
First add/run the live-caller assertion against the old source and observe failure. Then run credential-free tagged drain controls and focused drain/emitter tests with race detection on this macOS host. Compile the tagged live package; run vet and binary build. Complete offline/live suites belong to the dispatcher. Its live gate must execute and pass `TestInteractiveStreamLiveness`, retaining exact source revision, macOS identity, named outcomes and executed/passed/failed/skipped counts; zero executed or skipped liveness remains pending.

## Open questions
None. Dispatcher live evidence is a later-stage handoff, not builder proof.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: controls must reject foreign-conversation response/closure; the live drain retains payload attribution and decrypts every frame in order.
- Tokens/credentials: controls use fresh test-only Noise keys and no real credentials; the live test retains existing authenticated-home ownership.
- File operations: only source parsing and test-owned temporary state; no new product paths or persistence.
- Subprocesses: controls execute only the test binary with an exact test selector and case-name environment value, no shell, bounded context and reaping.
- Cryptography: existing Noise primitives and sequential cipher states are reused, with fresh handshake state per case and no production changes.
- Network/I/O: controls use bounded fixture frame slices and deadline-bound receives; no listener or new network surface.
- Errors/logging: expected child diagnostics contain synthetic fixtures only; existing live alarms remain fail-loud.
- Concurrency: timer observations use broadcast channels; cleanup cancels then joins, and cipher states have one owner.
- Threat model: this test-only repair preserves conversation isolation alarms; product authorization, relay transport and storage contracts are unchanged.

**Reviewer:** builder (self-review per checklist)
**Date:** 2026-10-19
