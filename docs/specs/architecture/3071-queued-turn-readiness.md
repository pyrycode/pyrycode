# Queued Claude turn readiness (#3071)

## Files read

- `internal/streamsup/runner.go` → `spawnAndWait`: actual Start, arm/publication and permission-write ordering.
- `internal/streamsup/parser.go` → `Write`, `consumeLine`: complete stdout lines and synchronous control processing.
- `internal/streamsup/parser_control.go` → `noteControlAck`: matching-success gate release.
- `internal/streamsup/posture_gate.go` → `arm`, `release`: child replacement cannot reuse an old acknowledgment.
- `cmd/pyry/inbound_deliver.go` → `newInboundDeliver`: activation, idle hold and write attempts.
- `cmd/pyry/queued_send_history.go` → `accepted`, `delivered`: persisted enqueue and delivery timestamps/references.
- `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: exactly-once durable user message.
- `internal/e2e/realclaude/harness_modal_test.go` → `spawnPermissionDaemon`: default posture, idle timeout 0, no operator bypass.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `startObservedPermissionHarnessWithOperatorBypass`: authenticated isolated relay composition.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `createConversationViaPhone`: fresh conversations over the actual wire.
- `internal/e2e/realclaude/session_error_recovery_test.go` → `TestInteractiveSessionErrorRecovery`: tagged daemon build with normal live admission.
- `docs/knowledge/features/streamsup-package-posture-gate-spawn-permission-mode-ack.md`: operator bypass provenance and residual-arm tests prevent false readiness proof.
- `docs/knowledge/features/msgqueue-package.md`: per-conversation serial drains and accepted callback ordering.
- `docs/knowledge/features/sessions-package.md`, `sessions-package-key-types-writemcpsettings-session-settingspath.md`: retention and generated session settings.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`: serial HOME isolation, separate stdout/processing witnesses, counted evidence and artifact survival.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: source/evidence and privacy contracts.

## Context

The retained occurrence proves 185.6 seconds from enqueue to delivery but does not identify the slow mechanism. Earlier unauthenticated control probes missed the daemon queue. Diagnose the actual permission-aware path before selecting a repair. The launcher authenticated on this machine after sandbox escalation; the initial access-only invocation executed zero tests and proves no behavior. No production daemon or credentials are changed. No decision record is warranted until a cause is established.

## Design

Add serial `TestInteractiveStreamQueuedTurnReadiness`, reachable under the normal live suite. Build an instrumented daemon with `e2e_realclaude`; reuse `spawnPermissionDaemon` with sonnet and operator bypass false. Seed the ordinary bootstrap separately and preserve both recorded MCP settings. Run two isolated arms: 32 preceding fresh conversations each with a completed short queued turn, retaining sessions, then a fresh unprewarmed target; and a fresh daemon with only its target. Record bootstrap separately. Idle timeout 0, active cap 0 (uncapped), default sweep/retention and serial pacing are explicit reproduction assumptions, not original settings.

Content-free observations are enabled only in the tagged test daemon. Ordinary builds use no-op functions. Observe successful Start, pending arm, permission-write completion, complete matching acknowledgment line reaching the stdout forwarder, parser processing and gate release. Observe activation/idle/write attempt boundaries. Private observations may join session/conversation identifiers in memory; exported evidence uses only test-local ordinals, static boundary names, times and booleans. The stdout witness is complete-line availability to the forwarder, not kernel arrival. Persisted history independently proves enqueue, exact intended message once, matching delivery references, main-turn opening and exact reply comparison.

Sample contemporaneous daemon descendants with `ps` numeric PID/PPID/CPU/RSS fields only, joining process identities privately. Retain counts and aggregate resources through setup and target. Mark unavailable measurements unknown. Record daemon commit, dirty-tree status, Claude version and allowlisted launch/setup values. Write portable sanitized JSON records under `internal/e2e/realclaude/testdata/queued_turn_readiness/`, including failed and non-reproducing arms and named counts.

An unchanged 90-second enqueue deadline decides admission. Bounded observation after a miss retains diagnosis without changing that failure. A successful comparison is non-reproduction. After capture, select only a cause established by measured ordering/load; append a revision defining that repair and its lowest-layer failing-before/passing-after race test before implementing it. If neither arm exposes the cause, retain evidence and return uncertainty through refinement without inventing a repair. A mobile-harness cause routes to mobile #2010.

Overlap audit: fetched feature/2873, /2882, /3158, /3163 and /3178; no overlaps in the initially planned readiness files. Sharing later additive wiring does not itself establish a dependency.

Sizing: one cause-backed repair; forecast 780 written lines including plan, observations, capture, focused causal test and records; one new exported observation function, no consumer migration, three acceptance criteria, no new production state machine. Recount after diagnosis before expanding. Observations have only this repair as consumer and cannot be split into an independent ticket.

## Concurrency model

Reuse the daemon's runner, stdout-copy and per-conversation drain goroutines. Observation writes occur outside existing locks and serialize only their private file appends. Test resource sampling is canceled and joined before evidence export; daemon cleanup stops all children. Serial subtests isolate HOME and observation files.

## State transitions and identity reuse

| Event | Coverage |
| --- | --- |
| Fresh queued target before confirmation | `TestInteractiveStreamQueuedTurnReadiness` retained/fresh arms; permission arm/write/matching ack/release before delivery |
| Repeated conversation/session creation | same named serial test's 32 completed predecessors and independent target |
| Child replacement or acknowledgment mismatch | existing `TestPostureGate` and spawn-posture regression family under race; new cause-specific test selected after diagnosis |
| Queue retry during pending confirmation | attempt boundaries in both arms; delivery reference and message count remain exactly one |

## Error handling

Missing authenticated launcher access is blocked, not a skip/pass. Missing observations or unusable evidence fail capture. Setup failures retain safe partial evidence. Observation I/O is best effort so diagnostics cannot alter permission decisions; the test rejects missing required witnesses. No timeout, retry, bypass or gate changes are a repair by themselves.

## Testing strategy

First validate observation privacy and exact matching with focused race tests. Run the ticket's exact targeted authenticated launcher invocation, retain both arms/counts, then diagnose. Write the causal regression first and observe the pre-repair failure before implementing. Run race tests on touched packages, tagged compile/vet, `go vet ./...` and `go build ./cmd/pyry`. Full host/mobile live gates belong to later owners.

## Open questions

- Which measured boundary accounts for the delay, and does the initial 32-turn comparison reproduce it?
- Does the established cause fit the remaining size/budget? Reassess before any repair.
- Original cap, idle, pacing and exact argv remain unknown; do not replace them with assumptions.

## Revisions

- 2026-10-11: the first authenticated diagnostic used a nonexistent `busy` wire state to recognize turn activity. The emitter reports `thinking`/`responding`, then `idle`; use a nonempty non-idle state. Keep the first run's failed captures and counts, classified as a capture pacing defect rather than a host readiness failure. This corrects the experiment without changing production behavior or the admission deadline.

## Documentation handoff

Pending documentation stage: `docs/knowledge/features/e2e-realclaude-test-infrastructure.md` § Test infrastructure must document the established trigger, permission-aware daemon/queue reproduction and fresh-daemon comparison, and link retained counted before/after evidence. State that process start, acknowledgment arrival, parser processing and gate release are separate observations and that mobile #2010 owns the unchanged full mobile gate. Describe a mechanism only once established.

## Security review

**Verdict:** PASS. **Reviewer:** builder self-review. **Date:** 2026-10-11.

- [Trust boundaries] Preserve `noteControlAck` matching-success and `PostureGate` current-child binding. Observation booleans never authorize delivery. Test-owned turns travel through paired Noise and the ordinary queue.
- [Tokens/secrets] SHOULD FIX: retain identifiers only privately for joins; evidence uses ordinals/booleans. Never serialize payloads, raw argv, credentials, request ids or pairing tokens.
- [File operations] Test-owned observation paths are under private temporary directories; mode 0600. Export structured allowlisted records atomically; no operator paths are retained.
- [Subprocesses] Reuse authenticated HOME and existing daemon cleanup; `ps` requests numeric columns without command/argv. No shell-interpreted runtime values or credential lookup in tests.
- [Cryptography] Existing paireddevice/Noise handshake owns keys and nonces; no new crypto or authentication bypass.
- [Network/I/O] Existing fakephone/fakerelay bounds apply; test waits remain bounded. Diagnostic stdout availability and parser processing remain distinct.
- [Errors/logs] SHOULD FIX: partial failures export only static reasons and safe measurements, never primary daemon buffers or subprocess-auth errors.
- [Concurrency] Observe off-lock, join sampler, and close observation files after every append. Existing gate mutex remains a leaf.
- [Threat model] Prompt injection, relay MITM, token binding, replay and per-conversation routing protections remain intact. Resource pressure is measured rather than inferred from historical counts.
