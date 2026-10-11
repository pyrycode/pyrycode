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
- Full reconstructed mobile merged tree: `8564ff1d87ee16944c1b99cd577449bd96b1534b`; all three input root tree hashes were independently reproduced. Current wrapper and delegated admission implementation are separately pinned in `emulator-capture.json`. The sixty-five-method list filters ordinary scheduling. Selected instrumented daemon revision is `8a076f1b6b65be51b91bce5435f42dd5719fd312`; Android diagnostic remains pending.

### Pending dispatcher capture and builder recovery

Concrete run ID: `3071-emulator-20261011`. Coupled exporter/readers: `readinessExportEmulator`, `readinessValidateRuntime`, `TestReadinessRuntimeRetained`, `TestReadinessEmulatorCaptureContract`. Run the contract's tagged race/compilation checks after recovering and before committing. `needs-live-artifacts` remains until the exact records below are validated, committed and pushed. No Android run or subsequent full host/mobile gate is claimed.

| Recovery | Destination |
| --- | --- |
| `/tmp/pyry-3071-live-records/3071-emulator-20261011/setup.json` | `/work/Projects/.pyrycode-worktrees/pyrycode/builder-3071/internal/e2e/realclaude/testdata/queued_turn_readiness/runtime/3071-emulator-20261011/setup.json` |
| `/tmp/pyry-3071-live-records/3071-emulator-20261011/request-events.jsonl` | `/work/Projects/.pyrycode-worktrees/pyrycode/builder-3071/internal/e2e/realclaude/testdata/queued_turn_readiness/runtime/3071-emulator-20261011/request-events.jsonl` |
| `/tmp/pyry-3071-live-records/3071-emulator-20261011/startup-events.jsonl` | `/work/Projects/.pyrycode-worktrees/pyrycode/builder-3071/internal/e2e/realclaude/testdata/queued_turn_readiness/runtime/3071-emulator-20261011/startup-events.jsonl` |
| `/tmp/pyry-3071-live-records/3071-emulator-20261011/boundary-events.jsonl` | `/work/Projects/.pyrycode-worktrees/pyrycode/builder-3071/internal/e2e/realclaude/testdata/queued_turn_readiness/runtime/3071-emulator-20261011/boundary-events.jsonl` |
| `/tmp/pyry-3071-live-records/3071-emulator-20261011/resource-samples.jsonl` | `/work/Projects/.pyrycode-worktrees/pyrycode/builder-3071/internal/e2e/realclaude/testdata/queued_turn_readiness/runtime/3071-emulator-20261011/resource-samples.jsonl` |
| `/tmp/pyry-3071-live-records/3071-emulator-20261011/named-results.json` | `/work/Projects/.pyrycode-worktrees/pyrycode/builder-3071/internal/e2e/realclaude/testdata/queued_turn_readiness/runtime/3071-emulator-20261011/named-results.json` |
| `/tmp/pyry-3071-live-records/3071-emulator-20261011/emulator-results.xml` | `/work/Projects/.pyrycode-worktrees/pyrycode/builder-3071/internal/e2e/realclaude/testdata/queued_turn_readiness/runtime/3071-emulator-20261011/emulator-results.xml` |
| `/tmp/pyry-3071-live-records/3071-emulator-20261011/manifest.json` | `/work/Projects/.pyrycode-worktrees/pyrycode/builder-3071/internal/e2e/realclaude/testdata/queued_turn_readiness/runtime/3071-emulator-20261011/manifest.json` |

### 2026-10-11: complete actual-client capture preparation before admission

Files read for this revision: `readinessExportEmulator`, `readinessExportRuntime`,
`readinessValidateRuntime`, `readinessResources`, `readinessFoldTurn`,
`ObserveReadiness`, `ObserveReadinessRequest`, `newInboundDeliver`,
`queuedSendHistory.accepted`/`delivered`, and `TestReadinessEmulatorCaptureContract`.
The historical four-arm batch remains immutable and is not rerun. No other fetched
feature branch overlaps these capture files. No production repair is selected.

Design: preserve private owner/process-lifetime joins independently from conversation,
session, child and full-width request identifiers. Export owner and session ordinals
and explicit unknown joins. Add a private capture-input record for witnessed runner
method intervals/status, clock comparison, effective settings and persistence witnesses;
these are distinct from XML, which supplies outcomes and retains available durations.
The sampler retains registry/history snapshots while each owned daemon is alive, so
replacement/deletion/restart cannot erase an earlier binding. Fold only the named target's
accepted entry, delivered receipt, main-turn and exact reply; `turn_write` stays separate.
Incomplete targets retain known timestamps with absent comparisons unknown.

Contract: every selected method appears once, unreached methods are incomplete, duplicate
and unexpected names fail. Resource intervals are scoped to owner/process lifetime and
current descendants, with per-child samples and separately retained lifetime CPU. Portable
emulator readers validate semantics as well as hashes and require the concrete run on the
live-artifact return. Input setup records are typed/allowlisted; payloads, private paths,
raw argv and identifiers never enter the portable output. Source and tooling pins remain
separate. Runner status times identify their observation clock; XML order never creates
method intervals or request associations. Missing original configuration stays unknown.

Concurrency: existing admission goroutines are unchanged. The bounded sampler has one
owner, snapshots read-only persistence, exits on completion/cancellation and joins before
export. Its interval state is independent per daemon lifetime; no observation releases a gate.

State transitions and tests: `TestReadinessEmulatorOwnership` covers reused IDs on separate
daemons and daemon restart; `TestReadinessEmulatorResults` covers partial/duplicate/unexpected
selection and durations; `TestReadinessEmulatorEvidence` covers chronology, current-child
confirmation, durable receipts and unknown comparisons; `TestReadinessEmulatorIntervals`
covers daemon/child replacement and independent interval CPU. Run these with tagged race
checks before handoff. Capture defects, fixture waiting, non-reproduction and demonstrated
host defects retain separate conclusions; zero executed records cannot satisfy recovery.

Sizing: approximately 900 additional source/test/plan lines plus contract edits; the existing
ticket already exceeds the ceiling. All additions have this investigation as their single
consumer; the shared handbacks floor continues to win. No new exported type/interface,
consumer signature migration or admission rejection state is planned. Three acceptance criteria.

Security review of this revision: PASS. Trust boundary is typed private-file decoding with
bounded inputs and fixed public categories. SHOULD FIX: allowlist setup fields, validate
method names and deny-scan all new files before publishing. Authentication remains wrapper
owned; file modes remain 0700/0600, source hashes use SHA-256, no shell or network entry
point is added, and sampler cancellation bounds all new work. Observation failure remains
inert. Admission/token/permission contracts are unchanged. Reviewer: builder; 2026-10-11.

Documentation handoff remains pending at the existing exact path/section above, including
effective setup and clock/host/client observation boundaries. Dispatcher admission of
`3071-emulator-20261011` and the eight recovery/destination files above remains pending.

### Capture-preparation implementation and remaining handoff

The finished preparation adds approximately 1,800 source/test/plan lines before
contract metadata. The forecast increased because semantic validation needs independent
fixtures for ownership, full result inventory, persisted receipts and current-child
ordering, rather than checking file hashes alone. These are all one-consumer support
for the same investigation; the sizing floor still applies, with zero new exported
types/interfaces and no admission consumer migration or rejection branch.

`readinessRunnerIntervals` reads the admitted runner's timestamped instrumentation
status stream. Times are explicitly runner-host receipts; XML preserves available
durations and never supplies method order or client send/receipt times. A missing
terminal XML record can retain a witnessed terminal status; unreached methods remain
incomplete. No retry substitutes for the single acquisition.

`readinessSampleEmulator` joins process kernel start identity and owned HOME/instance,
checks each running executable against the rebuilt binary, retains registry/history
snapshots across restart and samples independent current descendant CPU/RSS intervals.
`readinessNewJoins` preserves separate owner, lifetime, session, conversation, child
and connection/request namespaces. The target fold follows the matching accepted
entry and delivery receipt; it never uses `turn_write` as durable delivery. Absent
reply completion leaves its exact-content comparison unknown. The reader requires
current-child Start, permission-write, forwarder/parser complete-line, processing and
gate-release witnesses when confirmation is claimed. Permission write completion
and stdout observations are independent goroutines, so their relative order is not
invented. Exported target persistence boundaries also have their own witnessed times.

Effective runtime/configuration readbacks are a separate private typed input joined
by owner/lifetime/session; persistence-only samples cannot supply unknown runtime
values. Missing original configuration and unresolved startup roles remain explicit.
The isolated dispatcher must timestamp its existing runner status stream, record the
clock comparison and readbacks, and join/stop the sidecar before export. This stage
creates no Android execution or new startup fixture. `needs-live-artifacts` remains.

`TestReadinessRuntimeRetained` requires the concrete run when the recovery environment
`PYRY_E2E_READINESS_REQUIRE_EMULATOR=1` is set (and when the contract is executed or
recovered). The contract's return checks set it; existing Go directories cannot satisfy
that check. Missing/unusable or zero-executed returned evidence is a blocker. Before
admission, it remains an explicit live-artifact handoff. The clean instrumented binary
revision and support hashes are pinned by a subsequent metadata-only contract commit.

Focused offline evidence tests failed against the pre-preparation reader for accepting
missing/contradictory emulator evidence, duplicate/unselected results and zero executed
counts. Tagged race tests now cover those cases plus reused daemon/PID/child identities,
runner intervals, exact result inventory, partial outcomes, independent resource intervals,
current-child replacement and persisted delivery chronology. No cause-backed host red/green
or before/after live proof is claimed. Diagnosis, one localized repair and ordinary targeted
host proof follow the dispatcher acquisition; the full mobile gate remains with #2010 and
the subsequent full host gate with the dispatcher. The Documentation handoff above remains
pending, including client/host boundaries and effective setup provenance.

### 2026-10-11: verifier rework on `2ec4d53c`

Files read: `readinessEmulatorJoins.targets`, `readinessValidateEmulator`,
`readinessSampleEmulator`, `readinessResources`, `ObserveReadiness`,
`queuedSendHistory.accepted`/`delivered`, `history.Entry`/`SessionProvenance`,
and `runtimeHistoryFact`: the four findings concern diagnostic joins and lifecycle
proof, not admission behavior. No other fetched feature branch overlaps these files.

Design revisions for findings 1–4:

- Preserve the acceptance's independently witnessed session, enqueue epoch and
  current child before delivery exists. Absent delivery/opening/reply comparisons
  remain unknown. Keep enqueue identity separate when delivery follows a rebind.
- Join the target's opening, deltas and completion by producing session, daemon
  epoch and nonempty persisted turn ID. Stop attribution at another user message;
  independent turns neither satisfy nor invalidate the target comparison.
- Validate the complete session child lifecycle at delivery, including a successor
  Start and predecessor completion. Persistence boundaries must match their own
  epoch/session/child identities, not merely conversation and timestamp.
- Record each spawned child's kernel start identity in private tagged observations.
  Census and sampler require that identity and an uncompleted owned lifecycle before
  attributing current CPU/RSS. Counter identity comes from the same stat read as ticks.

Lifecycle race coverage: `TestReadinessEmulatorPendingTarget` (accepted-only failure
and incomplete); `TestReadinessEmulatorTurnIsolation` (foreign/missing provenance,
turn identity and later turns); `TestReadinessEmulatorSemanticValidation` (replacement,
completion and persistence identity contradictions); `TestReadinessEmulatorChildResources`
(wired sampler/census, PID reuse, missing identity and completed child).

Concurrency remains unchanged: the existing bounded sampler and tagged append lock
own observations; no observer changes a gate or admission outcome. Security review:
PASS. Kernel identities remain private and are excluded from portable resource output;
existing allowlists/deny scans, file modes and bounded cleanup remain authoritative.
No credential, subprocess command or remote contract is added.

Sizing: approximately 650 additional source/test/plan lines, zero new exported types,
zero admission consumer migrations or rejection states. The existing one-consumer
sizing floor remains applicable. Refresh support hashes and the clean host revision
in a metadata-only contract commit. The eight-file live-artifact recovery and exact
Documentation handoff above remain pending with their existing owners.
