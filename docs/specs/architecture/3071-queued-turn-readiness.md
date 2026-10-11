# Child-specific queued Claude readiness investigation (#3071)

## Files read

- `internal/streamsup/runner.go` → `spawnAndWait`: Start, stdin publication and permission-request ordering.
- `internal/streamsup/parser.go`, `parser_control.go`, `posture_gate.go` → `Write`, `noteControlAck`, `release`: only matching current-child success admits turns.
- `cmd/pyry/inbound_deliver.go` → `newInboundDeliver`: activation and idle waits precede the turn write.
- `internal/e2e/realclaude/harness_modal_test.go` → `spawnPermissionDaemon`: authenticated isolated daemon, default posture, operator bypass false.
- Checkpoint `255e86ae643c13f7bd36affb99b4ae786ad5ee95` → readiness capture, replay, attachment/peer/restart effects, readers and tagged observers. Its manifests and failed capture attempts remain immutable historical evidence.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`: authentication is launcher-owned; non-reproduction establishes no correction; records must survive checkout cleanup.
- `docs/knowledge/features/streamsup-package-posture-gate-spawn-permission-mode-ack.md`: arm before stdin; replacement cannot inherit predecessor confirmation; daemon-composed bypass differs from operator bypass.
- `docs/knowledge/features/development-verification.md` → Captures and live evidence, Test execution and artifact survival: named results and pinned committed readers are necessary.
- `CODING-STYLE.md`: bounded cleanup, symbol citations and contract comments.

## Context

Historical accepted admission took 185.6 seconds; the last 76.2 seconds cannot all be attributed to posture acknowledgment. The corrected checkpoint's four-arm batch counted 78/78/0/0 and established no host cause. Its startup service latched globally after bootstrap, leaving later children untested. This run acquires one bounded four-arm comparison with independent child/restart readiness. A mechanism decision record remains premature.

## Design

Recover only the ticket's corrected source and the records its readers require from `255e86ae`, preserving current main and historical manifests. Restore no-op observation calls for ordinary builds and private tagged observations for live builds. Current main is `847367f4`; the branch contains ordinary reverts and has no remaining implementation diff. The fetched feature branches do not overlap the selected files.

Assign each actual spawn a unique test-only child key inherited by its MCP and SessionStart processes. Portable records replace daemon, conversation, request and child identifiers with ordinals; tokens and content become match booleans. Record successful Start, permission write, complete line at forwarder before destination Write, complete line inside Parser.Write, matching acknowledgment processing, gate release, activation/idle attempts, durable history delivery and main-turn opening. Neither stdout observation establishes kernel arrival.

Replace the globally latched fixture with one readiness lifecycle per child key (therefore per restart). Each applicable role enters independently, checks a test-owned ready service, and records release and completion/error/timeout. Never require both roles before releasing either. Bound readiness and cleanup to ten seconds; missing entry remains unknown. Immediate fixtures are the matched control. Fixture waiting alone does not establish a host defect.

Keep independent encrypted peer traffic, actual generated attachments/file offers, attachment-bearing compact, both 120-post batches, durable first-daemon HOME restart and request-correlated refusals. Record ordered request sends and separate reply arrivals, method intervals/outcomes, ownership, reconnect/restart and teardown/revival. Target opening sends MCP/context/settings/newest-history asks without serial reply waits; create and enqueue promptly without prewarming or waiting for confirmation. Prefix/fresh differ only by the prefix.

Export each arm to `testdata/queued_turn_readiness/runtime/<run-id>/` with setup, request/startup/boundary/resource streams, named results and SHA-256 manifest. Keep a deny-scanned recovery copy in `/tmp/pyry-3071-live-records/<run-id>/`. Pin source revision, source bytes and transformations separately. Readers reject missing, changed or unpinned files. Preserve incomplete/failed arms and historical manifests. Sample contemporaneous descendants, interval CPU/RSS and memory/pressure, keeping lifetime CPU separate. Record configured substitutes separately from witnessed effective settings and original unknown configuration.

Run exactly one authenticated launcher batch of immediate-prefix, immediate-fresh, child-dependent-prefix and child-dependent-fresh within its existing 20-minute limit. Admission stays failed after 90 seconds from persisted enqueue; continued observation is bounded to 210 seconds. Select a localized production repair only after evidence identifies a host defect; append its regression/design in Revisions and reassess size before building. Otherwise commit observations and return a refinement handoff identifying the remaining boundary and admitted actual-client capture. A full Android capture belongs to the dispatcher and unchanged Android acceptance to mobile #2010.

Sizing before commit: approximately 2,800–3,200 written lines including restored tests/observers and runtime capture, at most two new exports, no consumer migration, three acceptance criteria, no new production rejection state machine. This exceeds 800 lines by approximately 2,000–2,400. Actual parent/grandparent are absent. The shared handbacks floor wins because all capture/replay support has exactly one consumer, this investigation/repair. Reassess any discovered repair or actual-client expansion before implementation.

## Concurrency model

Existing queue and stdout goroutines remain authoritative. Observations are append-only behind a leaf mutex and never release gates. Each fixture handler waits on its own child's readiness or request cancellation; the service owner cancels all lifecycles before server close. Resource sampling is canceled and joined before export. Device cipher states stay owned by their respective connection; reconnect replaces only that connection. Child teardown is witnessed separately from daemon restart and new-child retention.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Sequential MCP/SessionStart entry | `TestReadinessDependencyRelease`: either role can complete first |
| Bootstrap then target/replacement/restart child | `TestReadinessDependencyChildIsolation`: new child cannot inherit readiness |
| Cancellation or absent role | `TestReadinessDependencyCancellation`: all waiters stop without manufacturing entry |
| Split complete stdout acknowledgment | `TestReadinessForwarderBeforeParser` |
| Stale acknowledgment after replacement | `TestReadinessObservationMatching` |
| Conversation selection/rebinding | `TestReadinessConversationOrdinals` |
| Peer queue, attachment reconnect, durable restart | named prefix cases in `TestInteractiveStreamQueuedTurnReadiness` |
| Current-child confirmation then exactly-once target delivery | all four live arms and `TestQueuedReadinessFold` |
| Missing/changed/unpinned runtime files | `TestReadinessRuntimeManifest` |

## Error handling

Missing launcher access is an environment blocker, never inferred from a separate login probe. Capture defects, deliberate fixture waiting and host defects remain separate. Failure records contain safe fixed categories and counts, never raw errors or payloads. Retain all named failures/skips and incomplete arms. Missing witnesses stay unknown; a failed or late admission cannot become a pass. No retries replace the bounded batch.

## Testing strategy

Write child-lifecycle and runtime-reader tests first and observe the checkpoint fail. Restore existing tests before observers; use focused tagged race runs and scoped race checks for touched packages, tagged vet, `go vet ./...`, and `go build ./cmd/pyry`. Execute only the ticket-owned authenticated diagnostic through `live-claude-gate.py`; retain its own authentication/count evidence. The dispatcher owns the subsequent full host suite; #2010 owns the fresh full mobile gate. Any dispatcher capture needs the live-artifacts return trip before its records are claimed committed.

## Open questions

- Do independently reset child fixtures reproduce a separate host delay after readiness?
- Which measured boundary accounts for any delay, and what lowest-layer regression preserves it?
- Does localization require ordinary actual-client pacing/configuration? If so, prepare the pinned isolated emulator export contract before handoff.

## Documentation handoff

Pending documentation stage: `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`, under `Test infrastructure`, records the established trigger, permission-aware daemon/queue prefix (including actual peer/attachment/restart effects), fresh-daemon and child-specific startup comparisons, setup provenance and counted before/after evidence links. Distinguish process start, stdout observation site, acknowledgment processing and gate release; state that #2010 owns the unchanged full mobile gate. Describe the mechanism only after diagnosis establishes it.

## Security review

**Verdict:** PASS

- Trust boundaries: observations remain outside admission; `noteControlAck` matching success and current gate binding remain authoritative. Independent peers use existing pairing/Noise.
- Tokens: authentication stays launcher-owned. SHOULD FIX: portable export must deny-scan private child/session/request joins, paths and authored content before both publication and recovery copy; export only ordinals/booleans.
- Files: temporary authenticated HOME/workspace and private append files are test-owned, mode 0700/0600; publish manifests after atomic files. Validate basenames and reject unpinned files.
- Subprocesses: fixtures inherit only child identity for private joining; never export raw argv/environment. Existing harness owns isolated daemon teardown. No operator HOME/configuration mutation.
- Cryptography: existing Noise and random attachment IDs; SHA-256 pins provenance only.
- Network/I/O: loopback fixture with explicit HTTP timeouts and bounded reads; full-operation fakephone deadlines avoid timeout-driven socket closure.
- Errors/logs: fixed error families, booleans and named test methods; no raw protocol/error text in portable records.
- Concurrency: cancel all readiness workers and join sampler before export; no observation lock surrounds a production lock or gate transition.
- Threat model: no new remote capability, permission bypass or token contract. Existing protocol Security model remains unchanged.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-11

## Revisions

- 2026-10-11: source `3125097f` completed one authenticated four-arm batch: 78 executed, 78 passed, 0 failed, 0 skipped. Immediate prefix/fresh main-turn openings were 2.969/3.993 seconds; child-dependent prefix/fresh were 3.315/4.107 seconds. Every target delivered exactly once after matching confirmation. Each dependent prefix exercised thirty independent child readiness lifecycles, including restarted children; no fixture timeout or host defect was demonstrated. No host repair is selected. Runtime records retain all methods, effects, boundary/resource streams and their hashes; the earlier checkpoint manifest remains byte-identical.
- The sequential-role regression failed against the checkpoint because its global fixture required both entries. It passes under the independently served child health checks. This corrects a capture defect, not a historical host cause.
- Actual-client pacing remains the unresolved boundary. Add test-tag-only incoming/outgoing header observations at `internal/dispatch/dispatch.go` → `Route` and `Conn.Send`, with no-op ordinary builds; dispatch remains free of I/O dependencies in ordinary builds. Extend the runtime reader/exporter for sanitized Android XML and independently attributed host receipts. Client send, phone receipt and UI projection remain unknown where not witnessed. Tests `TestReadinessRequestObservation` and `TestReadinessEmulatorExportPrivacy` cover correlation, unchanged reply behavior, failed named methods and payload exclusion. Source/selection provenance and the exact isolated dispatcher admission/recovery contract belong to `emulator-capture.json`; no mobile source or assertion edits occur. This additional one-consumer capture support takes the total forecast to approximately 3,700 written lines; the sizing floor still applies. No fetched branch overlaps the new dispatch observation file.
- The private request observer initially round-tripped integer joins through float64. `TestReadinessRequestObservation` now uses a request above 2^53 and failed until `readinessAppend` used `json.Decoder.UseNumber`. The emulator reader explicitly models `reply_to`; rounding or an underscore mismatch would make unrelated replies appear uncorrelated. These are observation repairs only. Emulator recovery validates clean source/build-tag metadata on the actual rebuilt binary, preserves all named failed/skipped XML outcomes without messages or stdout, and keeps client send/receipt and method attribution unknown when only host receipt is witnessed. Its sampler runs beside the admitted wrapper until a completion sentinel or bounded context, with test-owned output and no Android execution of its own.
