# Queued Claude readiness diagnosis (#3071)

## Files read

- `internal/streamsup/runner.go` → `spawnAndWait`: child Start, posture arm, stdin publication and permission write ordering.
- `internal/streamsup/parser.go`, `parser_control.go`, `posture_gate.go` → `Write`, `noteControlAck`, `release`: matching current-child confirmation remains authoritative.
- `cmd/pyry/inbound_deliver.go` → `newInboundDeliver`: activation, idle hold, write and durable queue boundaries.
- `internal/e2e/realclaude/harness_modal_test.go` → `spawnPermissionDaemon`: isolated authenticated daemon with operator bypass false.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `startObservedPermissionHarnessWithOperatorBypass`: real encrypted relay and isolated HOME.
- Checkpoint `9b8b5569` → corrected capture, replay, readers and observations; historical records remain linked there.
- Pinned mobile `90d0407f06` → `InteractiveStreamE2ETest`, `SecondClientPeer`, `DurableGapProof`, `DaemonFaultControl`: peer ownership, attachment cuts and durable daemon restart.
- Pinned mobile launcher, `ThreadViewModel`, `RemoteConversationRepository`: candidate schedule and opening/reconnect asks; source is not a runtime ordering witness.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`: bounded phone reads, independent observation sites and counted durable evidence.
- `docs/knowledge/features/streamsup-package-posture-gate-spawn-permission-mode-ack.md`: bypass provenance and fail-closed replacement identity.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: source symbols, race checks, capture survival and attribution limits.

## Context

Original accepted target admission took 185.6 seconds. Prior authenticated comparisons did not reproduce it and omitted peer, attachment, restart and dependent-startup effects. No host cause is established. Diagnose before selecting a repair. A mechanism decision record is premature.

## Design

Recover ticket-owned corrected code onto current main, preserving unrelated changes. Ordinary builds retain no-op observations. The live build witnesses successful Start, permission write, forwarder complete line before destination Write, parser complete line, matching acknowledgment processing and release. Neither stdout site establishes kernel arrival.

Retain the 37-row candidate prefix and add actual independently paired peer operations, generated attachment transfer/retrieval and channel-post traffic through production paths. Restart only the isolated first daemon while retaining its durable HOME. Record accepted/rejected operations, source terminal edges, disconnect ownership, restart epochs, teardown/revival and incomplete rows. Avoid per-method idle barriers. Dedicated-daemon methods remain excluded from main-daemon load.

Compare prefix/fresh with identical opening asks and fixture configurations. Create and enqueue promptly without prewarming or awaiting posture confirmation. Immediate startup is the control. A local dependency variant releases MCP initialize and SessionStart when the fixture service observes dependent entry; bounded cleanup releases all waiters. This is a synthetic hypothesis, not historical configuration or evidence that expected waiting is a defect.

Retain safe setup/source hashes, named counts including failures, actual boundary and resource samples, content/token/identity match booleans and unique conversation/child/restart ordinals. Record lifetime CPU separately from interval load. Judge the unchanged 90-second limit from persisted enqueue independently of later observation.

Only a measured host mechanism authorizes a repair. Before implementing one, append its contract and a failing-before/passing-after regression to Revisions and reassess size. If no defect reproduces, retain observations and return uncertainty through refinement without claiming repair.

Sizing: approximately 2,200 written lines, one exported observation function, no simultaneous consumer migration, three acceptance criteria, no new production rejection state machine before diagnosis. The 800-line ceiling is exceeded by approximately 1,400 lines. The handbacks floor wins: capture/replay/observers have exactly one consumer, this cause-backed repair; no standalone reusable deliverable exists. Actual parent is none. Reassess discovered repair scope. Fetched feature/2873, /2882, /3139, /3163 and /3178: none modifies the selected observation/replay files or supplies a prerequisite.

## Concurrency model

Existing queue and child stdout goroutines remain authoritative. Tagged observations serialize private appends outside production locks. Fixture HTTP requests have bounded cancellation and test teardown releases waiters before closing servers. Resource sampler is canceled and joined before capture export. Each peer connection owns independent ordered cipher states; reconnect replaces only that connection's states.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Fresh target pending matching confirmation | `TestInteractiveStreamQueuedTurnReadiness`, all four arms, release precedes exactly-once delivery |
| Conversation selection and replacement | `TestReadinessConversationOrdinals`, child/restart observations |
| Peer queue/drop/allow and send-now | live named prefix rows; independent peer owner and queue observations |
| Attachment cut/retry and reconnect retrieval | live named prefix rows; stored acknowledgment and byte-match witnesses |
| Durable daemon restart and retained conversation revival | offline-read prefix row, restart epoch and old-child teardown witnesses |
| Dependency entry/release/timeout | startup fixture tests and live prefix/fresh dependent arms |
| Stale/mismatched acknowledgment | existing posture-gate race family and `TestReadinessObservationMatchesOnlyCurrentAck` |

## Error handling

Launcher authentication/access failure is an environment blocker. Capture defects, expected fixture waiting and host defects remain separate conclusions. Replay errors retain safe error-family counts and continue through both target arms where possible. Missing acknowledgment/identity/delivery observations fail; late admission cannot become success. Cleanup always releases fixture waiters and stops isolated processes.

## Testing strategy

Write focused observer/fixture/replay contract tests first. Run tagged offline tests under race detection, scoped touched-package race checks, tagged compilation/vet, `go vet ./...` and `go build ./cmd/pyry`. Run exactly the ticket-owned authenticated launcher test and count named executed/passed/failed/skipped results; retain usable records and hashes. Full host live gate belongs to dispatcher; unchanged full mobile gate belongs to #2010.

## Open questions

- Does any added effect reproduce delayed host confirmation? Original runtime pacing/configuration remain unknown.
- Which measured boundary accounts for delay? A deliberately held fixture alone does not identify the historical cause.
- If a defect is demonstrated, what lowest-layer regression preserves its causal ordering/load?

## Documentation handoff

Pending documentation stage: `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`, under `Test infrastructure`, records the established trigger, permission-aware daemon/queue prefix (including actual peer/attachment/restart effects), fresh-daemon comparison, setup provenance and counted before/after evidence links. Distinguish process start, stdout observation site, acknowledgment processing and gate release; state that #2010 owns the unchanged full mobile gate. Describe the mechanism only after diagnosis establishes it.

## Security review

**Verdict:** PASS

- Trust boundaries: preserve `noteControlAck` matching-success and current-child identity; observations never admit turns. Peer uses independently paired encrypted relay path.
- Tokens: launcher owns authentication; portable export contains booleans/ordinals, no credentials, authored content, raw argv or correlation tokens.
- Files: generated test-owned bytes and private joins only under isolated temporary HOME, files 0600/directories 0700; export uses temporary write/rename. No operator configuration read.
- Subprocesses: bounded test-owned Python fixtures and isolated daemon only; existing harness stops descendants. No caller-controlled shell interpolation.
- Cryptography: existing pairing/Noise only; hashes establish source/artifact provenance, not authentication.
- Network/I/O: local fixture server only, bounded waits and cleanup; fakephone uses existing frame bounds and full operation deadlines, avoiding timeout-driven socket destruction.
- Errors/logs: sanitized error families and structured boundary data; no raw protocol payloads exported.
- Concurrency: joined sampler and released fixture waiters; observation mutex is leaf and no production admission semantics change.
- Threat model: existing protocol Security model remains authoritative; this diagnostic introduces no remote capability or bypass.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-11

## Revisions

- 2026-10-11: the restored reduced replay executed 39/39 passed with no skips; this is exploratory dirty-source non-reproduction, retained separately. The first expanded four-arm comparison at `ba1ea10f` executed 78/74 passed/4 failed/0 skipped. Every target met the 90-second bound after confirmation. Four prefix failures were capture defects: upload waits treated unrelated `context_usage.unavailable` or `mcp_status.unavailable` replies as upload refusal. Correlate error replies to actual chunk request IDs; retain that failed batch. Fence the older 120-post batch before the ordinary peer turn, record durable delivery counts around restart, and send image/document references together in the direct-share turn. No host repair is selected. The dependency service became ready after observed role entries and no fixture timeout occurred; its readiness lifecycle is test-owned and does not recover historical configuration.

- 2026-10-11: corrected source `d56ca49f` executed 78 named driver checks / 78 passed / 0 failed / 0 skipped. Immediate prefix/fresh openings were 3.058/3.895 seconds; dependent prefix/fresh were 3.434/4.214 seconds. Each target delivered exactly once after matching confirmation. Both prefix arms exercised six retrieval byte matches, a real `pyry_files` offer, independently paired queue traffic, and a durable first-daemon restart. In the immediate arm, 120/119 posts had delivered before restart and 120/120 afterward, distinguishing acceptance from delivery. Prefix peak live-child count was 21, not a historical registry count. No startup fixture timeout or host defect was demonstrated. Source merge inputs were freshly retrieved and merged without conflicts in scratch; all 65 selected methods and the 37-row hash-ordered prefix were verified against that reconstructed source. This remains a candidate schedule, not an execution witness.
- The current-ack race scenario uses the restored name `TestReadinessObservationMatching`; the earlier table's tentative name `TestReadinessObservationMatchesOnlyCurrentAck` refers to that scenario. Diagnosis resolves the repair-selection question as no established host cause. Original effective runtime configuration, pacing/outcomes, dependency readiness lifecycle and kernel arrival remain unknown. The local service becomes ready once both dependency roles enter and stays ready afterward; this cannot recover the historical configuration. Return this uncertainty for refinement and retain checkpoint evidence; full host/mobile gates and mechanism documentation remain pending until a cause-backed repair exists.
