# Require thread before app session binding

## Files read

- `internal/relay/v2session_handshake.go` → `handleNoiseInit`, `checkClientVersion`: authentication, binding, ack construction and persistence ordering.
- `internal/relay/v2session_test.go` → `startManager`, `driveToOpen`, `driveToOpenCaps`, hello builders: shared supported fixtures and real Noise transport.
- `internal/relay/v2session_min_version_test.go` → `runHelloFrom`, `assertVersionRejected`: correlated encrypted refusal assertions.
- `internal/relay/v2session_static_key_test.go` → static-key tests: atomic binding and persistence regressions.
- `internal/relay/v2session_thread_test.go` → negotiation, summaries and delivery: obsolete positive non-thread cases must become supported cases or admission refusals.
- `internal/e2e/relay_v2_handshake_test.go` → shared handshake helpers: fake-daemon supported admission.
- `internal/e2e/realclaude/harness_daemon_test.go` → `driveHandshake`: shared live connection setup.
- `internal/e2e/liverelay/liverelay_test.go` → `driveHandshake`, `buildHello`: real-relay connection setup.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`: grants remain value-specific, rekey retains them, rejected ack grants no session authority.
- `docs/knowledge/features/e2e-harness.md`, `e2e-realclaude.md`, `development-verification.md`: consume encrypted frames in order, catch up named conversations, compilation is not live proof.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: thread is the supported conversation-content route; final compatibility wording belongs to the parent.
- `cmd/pyry/agent_switch_frames_test.go` → `switchWire`: exact advertisements, correlated Noise response and admitted-session snapshots.
- `cmd/pyry/daemon_live_relay_test.go` → `testDaemonRelayWire`, `TestDaemonLiveRelayRetiredSnapshot`: live-provider installation, publication and deliberately held admission.
- `cmd/pyry/agent_switch_publication_test.go` → `TestRelayAgentSwitchDelayedPublication`: publication exclusion and attribution across consecutive switches.
- `cmd/pyry/session_model_selection_test.go` → `TestPublishedModelSelection_SettingsRoundTrip`: preserved model selection/projection on supported sessions.
- `cmd/pyry/daemon_model_catalog_test.go`, `daemon_model_catalog_reconnect_test.go` → catalog request/reconnect tests: value-specific projection, generated clears and refusal behavior.
- `cmd/pyry/daemon_thread_compact_test.go`, `daemon_thread_compact_read_test.go`, `daemon_relay_wire_test.go` → compact negotiation and held-handshake tests: stopped readiness and response-before-admission boundaries.
- `cmd/pyry/daemon_thread_queries_test.go`, `daemon_thread_relay_test.go` → query/join and publication fixtures: named catch-up, checkpoint integrity and content isolation.
- `cmd/pyry/daemon_thread_list_test.go`, `unread_visibility_test.go`, `daemon_thread_offscreen_test.go` → shown-authority tests: supported-peer watermarks and joined retirement/recovery.
- `cmd/pyry/prompt_answer_relay_test.go`, `thread_shadow_legacy_test.go` → prompt responsiveness and unavailable-route tests: live dismissals and durable producer behavior under refusal.
- `docs/knowledge/features/development-verification-test-execution-and-artifact-survival.md`: Noise completion is distinct from admission; retain failed evidence and completed query boundaries.

## Context

Authenticated/version-accepted apps currently bind and open without a usable thread route. Require negotiated thread before any admission side effects. One independently useful behavior includes the fixture migrations needed to retain runnable gates. Estimated 1000–1300 written lines, zero exported APIs, four acceptance criteria and two new refusal branches. The verification floor retains fixtures with their consumer despite the 800-line ceiling; this is already a grandchild (#3714 → #3720 → #3740), recorded with needs-human:sizing. No further split is permitted. No new decision record is needed.

Overlap: #3240 adds a push-queue observer later in `handleNoiseInit`; #3296 adds catch-up coverage. Neither supplies an admission prerequisite or restructures the admission block. Keep edits local.

## Design

`negotiateCapabilities` remains the sole grant authority. After token/static-key validation and existing version checks, derive a local thread refusal: no advertisement means `client.update_required`/4412; advertised but ungranted means `protocol.unsupported`/4421. Gate both binding and workspace construction on the complete verdict. Preserve the binding race's atomic decision and generic authentication rejection.

Complete Noise response construction on refusals, then send `noise_resp` with the correlated ack before exactly one routing envelope containing the correlated sealed nonretryable error plus close. No workspace metadata, session worker, redemption or client-version persistence may precede admission. Keep LastSeenAt's ordinary validation bookkeeping. Log closed-set reasons only.

Shared positive fixtures advertise and genuinely negotiate thread with a ready backend seam; explicit negative setup preserves missing/false readiness and absent advertisements. Optional interactive, expansion, compact and multi-agent flags retain value-specific membership checks. Replace obsolete legacy-positive content expectations with thread-supported assertions. Shared fake/live/real-relay helpers perform named-conversation catch-up before expecting live content, reusing existing scenario receivers rather than translating legacy content.

## Concurrency model

No new goroutines or shared state. Admission stays on the manager Run goroutine; registry binding remains atomic under its existing lock. Existing cancellation and worker teardown paths remain intact.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Authenticated hello rejected before open/persistence | `TestV2Session_ThreadAdmission` |
| Unknown/expired token or different static key with any thread advertisement/readiness | `TestV2Session_ThreadAdmissionAuthPrecedence` |
| Existing version rejection precedes thread rejection | `TestV2Session_MinVersion_Enforced`, `TestV2Session_ThreadAdmissionVersionPrecedence` |
| Concurrent installs race to bind one token | `TestV2Session_StaticKey_BindRace` (existing static-key family) |
| Repeated handshake/rekey preserves optional grants | `TestV2Session_ThreadNegotiation`, existing rekey and static-key families |
| Reconnect/catch-up reestablishes a named content route | migrated affected fake-daemon scenarios; live execution pending dispatcher |

## Error handling

Authentication errors retain indistinguishable `auth.invalid_token`/4401. Existing version payloads remain unchanged. Thread errors contain no remote-authored hello values. Reuse existing error sealing and combined error/close handling, including close-only fallback if sealing fails. No compatibility switch or fallback admission.

## Testing strategy

Write real Noise_IK refusal tests first and observe them fail before implementation. Assert ordering, exact codes/correlation, omitted workspace root, no open session/dispatch/push route, untouched pairing admission fields in memory and disk, LastSeenAt bookkeeping and private logs. Preserve supported negotiation/rekey and minimum-version/static-key assertions while migrating required positive fixtures. Run affected package/scenario checks under race on Linux and compile changed e2e, e2e_realclaude and e2e_liverelay suites. Run go vet and build. Retain commands and counted outcomes in the PR. Full configured acceptance and live-Claude execution belong to dispatcher; macOS is outside this ticket's scope.

## Open questions

None. Fixture breadth will be determined by affected-package failures; do not remove supported scenario assertions.

## Documentation handoff

Pending documentation stage:
- `docs/protocol-mobile.md` sections **Handshake**, **hello (v2-specific note)**, **Capability negotiation (v2)**, **Compatibility**, **Appendix: example flow — first pairing + first message (v2)**: require negotiated thread after authentication/version checks and before binding, redemption or workspace metadata; state missing-thread 4412 versus advertised-but-ungrantable 4421, authentication privacy precedence and optional capabilities.
- Reconcile `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` and its **Thread delivery and summary projection** topic with this admission rule.
- Final compatibility-retirement/ADR wording remains with #3720's integrated documentation handoff.

## Security review

**Verdict:** PASS

- [Trust boundaries] `handleNoiseInit` admits only a token/static-key-authenticated, version-accepted peer with a granted thread capability; unknown strings cannot satisfy membership.
- [Tokens] Preserve generic authentication refusal precedence and Validate LastSeenAt updates. No token, token hash, static key or hello text enters new logs/errors.
- [File operations] No new paths or writes. Rejected admissions reach none of `recordStaticKey`, `recordRedemption`, `recordClientVersion`; tests compare disk and memory admission fields.
- [Subprocesses] No production subprocess changes; existing harness teardown owns test children.
- [Cryptography] Reuse Noise_IK response and existing seal functions; response precedes encrypted error so the initiator can decrypt. No nonce/key changes.
- [Network and I/O] No new socket or framing boundary; existing bounded Noise frame decoding and send path remain.
- [Errors/logs] SHOULD FIX during build: pin privacy with marker-bearing hello values and verify fixed rejection reason logs; preserve version payloads and generic authentication bytes.
- [Concurrency] Atomic `BindStaticKey` remains after all earlier verdicts and before workspace construction. No new lock/goroutine; race tests cover binding and rekey.
- [Threat model] Relay remains untrusted and sees encrypted application data. OUT OF SCOPE: removing remaining legacy production branches belongs to #3741 and final integrated retirement to #3720.

**Reviewer:** builder (self-review)
**Date:** 2026-10-23

## Revisions

2026-10-23: The affected package run showed retained modal, question, model, slash,
turn-phase and suggestion fixtures still supplied only legacy live-state seams.
Their fixture adapters now supply the same records through `ThreadLiveState`,
assert generated clears and retain payload, unicast, revision and optional-gate
checks. Generic transport tests use supported workspace updates; obsolete
non-thread positives become admission refusals. Droppable legacy deltas remain
covered by pure queue tests; the supported nonblocking transport scenario now
proves FIFO delivery of control frames without drops. Shared fake/live helpers
complete named catch-up through the existing scenario query reader. Total written
work remains approximately 1200 lines under the recorded verification-floor and
grandchild exception. The existing binding-race test's exact name is
`TestV2Session_StaticKey_RaceAcceptsAtMostOne`.

2026-10-23 rework: Linux candidate b862b6f8 reproduces all 13 reported command
families (33 test events: 7 passed, 26 failed, zero skipped). The common invariant
is incomplete supported-fixture migration, not another product deliverable.
Keep the recorded verification-floor/grandchild sizing exception. Repair
`switchWire` so its low-level hello preserves exact advertisements, validates the
correlated `hello_ack`, and returns its actual grants; supported opening requires
thread plus a bounded manager admission snapshot. Explicit refusal helpers assert
the sealed correlated error and close before proving no open session. Supported
daemon fixtures install query/live providers and a running publisher, consume
snapshot clears, and finish named catch-up before content assertions. Replace
obsolete non-thread/unready positives with 4412/4421 refusals or supported-thread
scenarios, preserving projection, prompt responsiveness, attribution, unread,
isolation and lifecycle checks. Coverage includes the reported 13 families and
the shared command package, plus existing relay admission/auth/version/key races
and tagged compilation. Earlier failed gates remain failed; complete acceptance
and live execution remain dispatcher-owned pending work.

Rework security review: PASS. No production boundary changes. Exact advertisements
and correlated grants prevent fixtures from mistaking Noise completion for
authorization; refusal checks retain workspace privacy and authentication
precedence. Existing cryptography, bounded transport, token storage, subprocess
and publisher cancellation contracts remain unchanged. Manager snapshot waits
use bounded contexts; new test publishers retain joined shutdown paths. No new
exports, dependencies, paths, logs or credentials are introduced.

The shared-package check also exposed three intentional response-before-admission
fixtures. `TestCompactReadWaitsForOpenSession`,
`TestDaemonRelayWireAdmissionBeforePush` and `TestDaemonLiveRelayRetiredSnapshot`
now use the exact low-level hello while their response callback is held, then
prove actual thread admission after releasing it. Supported opening keeps its
strong admission check. Reset state is delivered through the production live
publisher, whose current-state coalescing can omit superseded intermediate
edges; the switch fixture still checks the inactive state before delayed
publication, exclusion, successive agent attribution and durable delimiters.

2026-10-23 verifier rework (`f1276c1f`, findings 1–4): finish the same supported
fixture migration. Hosted resource peers offer and verify thread independently
of hosted-app capability. Handshake catch-up uses correlated query completion
and preserves every interleaved decrypted envelope on its phone for the next
scenario receiver; it creates no disposable derived content reader. Receivers
consume buffered frames before further decryption. Explicit generated clears
remain distinguishable from readings, and scenario consumers handle them before
validating prompt ownership or rate-limit vocabulary. Investigate terminal idle
against supported source/session attribution rather than removing completion
checks. Reconnect capacity coverage enumerates retained `ThreadLiveState`
records, including generated clear, without post-admission manual pushes.

The repair remains one admission behavior with its required consumer fixtures;
the recorded grandchild/verification-floor exception still applies. Expected
repair: 400–650 written lines, no new exported types, no production admission
changes. Focused race proof covers hosted readiness, every named recorded/channel/
eviction regression, reconnect model/slash/suggestion snapshots, shared scenario
receiver and relay admission/auth/version/static-key/capacity tests. Tagged live
compilation is required; full acceptance and live execution remain pending with
the dispatcher. Tests cover repeated catch-up, interleaved snapshots, cleared
prompt/readings, and terminal idle ownership under `go test -race`.

Security review of this repair: PASS. The test-only receive buffer belongs to one
phone and is synchronized; teardown discards it with that phone. Preserve Noise
nonce order by storing decrypted envelopes without resealing or manufacturing
content. Correlated acknowledgments and successful named queries prove supported
admission. No new credential/log/path/subprocess/cryptographic boundary. Ownership,
generic authentication refusals and source provenance checks remain required.

Measured revision: the exact recorded reply's live idle preceded even its first
history delta by 525 microseconds, and its terminal history append by 1.14 ms.
Use the retained accepted-send (or channel message) timestamp plus producing
session and owned source completion to exclude predecessor idles; append clocks
cannot order this independently captured live reading. Controlled tests retain
foreign-session, previous-command and generated-clear rejection. The snapshot
iterator's exhaustion, rather than `FlushPushes` alone, fences enumeration;
transport capacity must prevent exhausting it before the held writer releases.

Out-of-scope production finding #3843: after the first supported recorded turn,
`daemonThreadPublisher.publish` reports `baseline_required`, retires the peer,
and loses its terminal close as the binary transport disconnects. Preserve every
recorded assertion and skip only when a receive failure is accompanied by both
that daemon gap reason and producer-gap retirement. Other receive failures still
fail. No reconnect, accepted-send replay or production continuity workaround is
added. The unresolved repeated-turn assertion remains pending on #3843.
Late retained model/slash/suggestion consumers distinguish generated clears from
fresh readings. Named catch-up assembles correlated fragments and verifies their
index, offset, length and SHA-256 before awaiting the separate final coverage
marker; interleaved live observations remain buffered for scenario assertions.

2026-10-23 verifier rework (`9bed20c6`, findings 1–3): keep this repair within the
supported-fixture migration and its existing grandchild/verification-floor
exception. Completion must use the producing session and `main_turn_opened`
identity/`occurred_at`, falling back conservatively to the first source content
timestamp when no execution fact exists. Queue acceptance is not execution.
Controlled tests place a same-session predecessor idle after successor acceptance
but before successor execution, with no successor terminal delivery. Preserve
the before-history-append idle case. Positive client-feature peers advertise and
verify thread, correlate their ack and finish named catch-up. Remaining connected
and recorded consumers explicitly consume generated clears without counting or
decoding them as fresh readings; retain payload, ownership, cardinality and
lifecycle assertions. Diagnose hook ordering, subagent extra launch, fallback
retirement and thinking-progress separately. Expected repair remains 400–700
written lines, zero exports and no production admission changes.

Security review: PASS. These changes retain authentication/privacy precedence,
one ordered Noise reader, explicit clear visibility and real source ownership.
Execution facts must match both turn ID and producing session; no timing allowance
may make a predecessor idle prove successor completion. New waits remain bounded
and publishers/processes retain their existing joined teardown. Linux focused
race checks cover the reported regressions and helper controls; tagged compilation,
vet and build remain builder-owned. Full acceptance and live execution remain
pending with the dispatcher; all historical failures and continuity holds remain.

The conservative execution-boundary check exposed a necessary timestamp repair:
`main_turn_opened.occurred_at` currently records drain time, after a fast producer
may already have captured idle. Carry the existing source turn's opening instant
alongside its already captured identity in `liveStreamCapture`, and use it for
the existing opening fact. This adds no wire fields or admission behavior. The
controlled `TestDaemonLiveExecutionOpeningClock` queues two complete same-session
turns before draining and checks each opening precedes its own captured idle.
This is the source-proof prerequisite of finding 1, retained with its consumer.
Security review: PASS; the timestamp is daemon-owned, captured under the existing
live-state lock and immutable across queue/drain; no new goroutines or logs.

Additional files read: `cmd/pyry/daemon_live_source.go` (`acceptEventLocked`),
`cmd/pyry/interactive_turn_v2.go` (`startTurnIfNeeded`),
`cmd/pyry/runtime_history.go` (`recordRuntimeFact`), and
`internal/e2e/internal/scenario/source_turn.go` (`feed`, `idle`): execution
identity and clock cross independently scheduled producer/drain delivery.
Retain an actually received idle until its matching source append is visible;
`TestSourceIdleBeforeHistoryAppend` covers this ordering. Heavy subagent history
polling is bounded to the existing 20ms source-poll cadence, while every app frame
still passes through the single ordered receiver. This avoids repeatedly scanning
the growing transcript for each fragment and preserves all lane/cardinality checks.

| Rework event | Race-detector coverage |
| --- | --- |
| Same-session predecessor finishes after successor acceptance | `TestSourceObserverQueuedPredecessorIdle`, `TestSourceQueuedExecutionIdentity` |
| Received idle precedes history append, then repeats, clears or resumes | `TestSourceIdleBeforeHistoryAppend`, `TestSourceTurnSupportedIdle` |
| Two complete same-session turns wait behind the history drain | `TestDaemonLiveExecutionOpeningClock` |
| Bootstrap snapshot followed by fresh MCP request/turn observations | `TestRelayV2_MCPActuationGatedAuditedAndAnsweredFresh`, `TestRelayV2_MCPStatusRequestQueriesLiveChildRequesterOnly`, `TestRelayV2_StreamMCPStatusReachesConnectedPhone` |

Measured follow-up: the selected connected run passed ten of fourteen tests.
Send-file's post-modal `SourceSince` lost the parked execution anchor, so use
the existing continuation boundary with the current source snapshot. A cold
new conversation has no usable history until its first owned post; finish named
catch-up after that source completion and before any live-content expectation.
MCP queries/actuations update public retained readings as well as returning one
correlated reply; validate uncorrelated readings separately while retaining exact
requester-only reply counts and payload/provenance checks. Reset retirement is
two active readings plus an explicit clear, not a third resetting payload:
retain the clear envelope separately, assert its conversation/new session and
ordering, and preserve the combined cardinality and retained rotation assertions.
Diagnose subagent's extra invocation by recording whether it exists at the
scenario-sealed boundary before joined daemon shutdown; do not suppress it.
The same fixture contract and grandchild/verification floor apply; the expanded
repair is approximately 700–900 written lines with zero exported APIs.
Security review: PASS. No admission change, invented content or relaxed reply
correlation; clear metadata and public reading provenance remain mandatory.

The subagent diagnostic establishes that its extra child is the daemon's normal
reply-suggestion fallback before scenario sealing, not a second foreground
provider or a shutdown-only launch. Account for one ancillary worker explicitly:
recognize only the complete fixed fallback argv, require the foreground claim,
read bounded safe exchange JSON, record its own PID/parent and input size/hash,
then decline it with empty output and exit 1 without executing a provider. After
joined shutdown, require the matching daemon-owned completed Wait/exit diagnostic.
Unknown argv, duplicate ancillary workers and extra foreground launches retain
the existing duplicate failure. This extends offline fixture accounting, not live
provider evidence; no native response or application frame is synthesized.
`TestRecordedSubagentWorkerPolicy` covers exact/altered argv, invalid input,
duplicate receipts, ownership and completed-Wait checks. The same verification
floor/grandchild sizing exception retains this necessary process-accounting seam.
Security review: PASS; closed argv and bounded safe input, private receipt under
the existing fixture home, actual process parent plus production Wait, zero
provider execution, and no new goroutines or admission/credential changes.

The final connected replay exposed an ordering bug in the held thinking fixture:
the phone received its one progress reading and retained reply, but the source
callback could observe the reply while the reader was already waiting for another
frame, leaving the terminal unreleased. Release from either existing observation
callback as soon as both predicates hold. Both callbacks run on the one receiver;
no goroutine, artificial frame or weaker completion predicate is added. Preserve
the failed 13-pass/1-fail comparison and rerun crossing/below-bound controls.
Ancillary process ownership/exit controls are covered separately by
`TestRecordedSubagentWorkerWait`; the process policy controls remain in
`TestRecordedSubagentWorkerPolicy`. Subagent source polling after its first-main
checkpoint now uses 100ms between rescans, with immediate scans on real turn-state
readings; the 281-event helper race run preserves the exact first-main boundary.

The final recorded run passed the clear-reset payload/rotation assertions, then
its successor recall lost the phone with 4414. Before classifying this as the
existing #3844/#3845 continuity failure, retain closed-set daemon gap/retirement
diagnostics on that root's failure; the original assertions and failed replay
remain intact. A receive code alone is insufficient evidence of the known hold.

Repeated thinking checks exposed a second ordering defect: a generated clear for
an unrelated reading discarded an already received terminal idle before the
history append became visible. The controlled `other-reading-clear` case in
`TestSourceIdleBeforeHistoryAppend` fails before repair. Only a `turn_state`
clear for the watched conversation invalidates its retained turn-state reading;
other reading clears remain distinct and cannot certify completion themselves.
This preserves both exact execution ownership and the independently captured
terminal delivery requirement. The isolated clear-reset diagnostic rerun passed
all three events, including its successor recall, without any new skip.

## Builder validation — 2026-10-23 verifier rework

Linux x86_64 source candidate `4b8c0a84bf28dea60f2ecc7526a0a8b3d0f36a65`.
Counts are named Go test events, including parent/subtest events; skipped events
are separate from executed events. Logs are retained under
`/tmp/builder-3740-rework/`. All test commands use ordinary shared admission:
`python3 scripts/test-admission.py -- go test -timeout=30m -race -json`.

| Selection / command suffix | Executed | Passed | Failed | Skipped | Retained log |
| --- | ---: | ---: | ---: | ---: | --- |
| `./internal/e2e/internal/scenario` | 282 | 282 | 0 | 0 | `scenario-terminal-final.json` |
| `-tags e2e -run <connected selection below> ./internal/e2e` | 14 | 14 | 0 | 0 | `connected-terminal-final.json` |
| `-tags e2e -run <recorded selection below> ./internal/e2e` | 13 | 13 | 0 | 0 | `recorded-terminal-final.json` |
| `-tags e2e -count=5 -run '^(TestRelayV2_StreamThinkingProgressCrossingDeliversOneFrame\|TestRelayV2_StreamThinkingProgressBelowBoundIsSilent)$' ./internal/e2e` | 10 | 10 | 0 | 0 | `thinking-terminal-final.json` |
| `-tags e2e -run '^(TestRecordedSubagentWorkerPolicy\|TestRecordedSubagentWorkerWait)$' ./internal/e2e` (unchanged worker source from `09cda2e3`) | 17 | 17 | 0 | 0 | `worker-controls.json` |
| `./cmd/pyry` (retained `c7460bb8` package proof) | 16834 | 16834 | 0 | 4 | `cmd-package.json` |
| Execution/history clock named selection | 9 | 9 | 0 | 0 | `clock.json` |
| Relay admission/authentication/version/static-key/capacity named selection | 85 | 85 | 0 | 0 | `relay.json` |

The four command-package skips retain their original reasons: opt-in real-Claude
agent-run, two slow checks, and a case-insensitive-filesystem-only check.
`git diff c7460bb8..4b8c0a84 -- cmd/pyry internal/relay` is empty; subsequent
changes affect e2e consumers and the plan. The clock selection was
`TestDaemonLiveExecutionOpeningClock`, `TestDaemonLiveLegacyDelayAndIdentity`,
`TestDaemonLiveNeutralMainTurnIdentity`, `TestRuntimeHistoryExitSourceAndReplacement`
and `TestRuntimeHistorySourceClosure`. The relay selection retained
`TestReadinessThreadReconnectSnapshotCapacity`, `TestV2Session_ThreadAdmission`,
`TestV2Session_ThreadAdmissionAuthPrecedence`,
`TestV2Session_ThreadAdmissionVersionPrecedence`, `TestV2Session_ThreadNegotiation`,
the minimum-version NoMinimumAcceptsAll/Enforced/RejectRecordsNothing/
BadTokenStill4401/RejectLog cases, and the static-key DispatchedSnapshot/
UnknownTokenBindsNothing/FirstConnectionBindsAndPersists/
OtherInstallRefusedLikeUnknownToken/VersionRefusedBindsNothing/
RaceAcceptsAtMostOne/RevokeAndRepairFreesTheDevice cases.

Exact connected selector:
`^(TestRecordedMCPSendFileWaitsForStartup|TestRelayV2_ClientFeaturesSpawnSnapshot|TestRelayV2_MCPActuationGatedAuditedAndAnsweredFresh|TestRelayV2_MCPStatusRequestQueriesLiveChildRequesterOnly|TestRelayV2_StreamBackgroundTaskFramesReachConnectedPhone|TestRelayV2_StreamContextUsageReachesConnectedPhone|TestRelayV2_StreamMCPStatusReachesConnectedPhone|TestRelayV2_StreamModelListReachesConnectedPhone|TestRelayV2_StreamRateLimitNonBenignReachesPhone|TestRelayV2_StreamRateLimitWarningBandReachesPhone|TestRelayV2_StreamSessionFactsReachesConnectedPhone|TestRelayV2_StreamSlashCommandListReachesConnectedPhone|TestRelayV2_StreamThinkingProgressCrossingDeliversOneFrame|TestRelayV2_StreamThinkingProgressBelowBoundIsSilent)$`.

Exact recorded selector:
`^TestRecordedClaude/hook-banner|TestRecordedClaude/question-answer-witness|TestRecordedClaude/bypass-witness-mcp-stored|TestRecordedClaude/bypass-witness-mcp-operator|TestRecordedClaude/bypass-witness-stdio-stored|TestRecordedClaude/bypass-witness-stdio-operator|TestRecordedClaude/fallback-suggestion|TestRecordedClaude/subagent-text|TestRecordedClaude/clear-wrapup-skipped|TestRecordedClaude/clear-reset|TestRecordedClaude/mcp-send-file$`.
This executes eleven named leaves, their parent, and the matching first-turn
startup test. The fallback replay completes without the continuity skip.

Passed `go vet ./...`, `go vet -tags e2e ./internal/e2e`,
`go vet -tags e2e_realclaude ./internal/e2e/realclaude/...`,
`go vet -tags e2e_liverelay ./internal/e2e/liverelay/...`, and
`go build -o /tmp/builder-3740-rework/pyry-terminal-final ./cmd/pyry`.
Tagged compilation executes zero live scenarios and supplies no live proof.

Historical failures remain failed: the original `9bed20c6` fake gate
(2157 passed/26 failed/two skipped), its named comparison (59 passed/26 failed),
the earlier `b862b6f8` comparison (seven passed/26 failed), and the timed-out
`f1276c1f` acceptance. This rework retains the red queued-predecessor control,
consumer reproductions, compilation/setup failures, first-main checkpoint
failure, the 11-pass/two-fail recorded comparison, the 13-pass/one-fail connected
comparison, the nine-pass/one-fail repeated thinking check, and the four-pass/
two-fail other-reading-clear control. Final focused passes do not turn those
captures into passes. Production continuity holds #3844/#3845 remain unresolved.
The measured cold thread-only catch-up anomaly is recorded on existing #3741;
the interactive fixture pass is not optional-capability proof.

Pending dispatcher/verifier: normal approval and independent security review,
full-module and complete configured acceptance, affected live-Claude scenarios
and final gate build. Pending documentation remains as specified above. macOS
is outside this Linux ticket's verification scope. No live artifacts are required.

## Revisions — verifier rework of `28322de7`

2026-10-23 (prospective): findings 1–3 concern the same supported-fixture
completion invariant. Retain the recorded verification-floor/grandchild sizing
exception. Expected repair is 450–700 written lines, zero exported types,
seven consumer updates and no production admission changes.

Additional files read: `source_turn.go` (`feed`, `idle`), `source_observer.go`
(`Watch`, `Next`) and the seven consumers `CompletedTurn`, `AttachmentTurn`,
`OutsideRead`, `permissionTurn`, `RefusedTurn`, `AnsweredTurn`, `NativeSuggestions`.
Existing source, question and modal controls cover ownership and dismissal but
omit delayed predecessor openings and consumer-level idle-before-history ordering.
The e2e lifecycle and development-verification topics require independent actual
delivery and owned completion; a private helper pass cannot prove its consumers.
No concurrent feature branch overlaps these scenario files.

Design: retain a command-time boundary from the watched command and its accepted
source fact. An opening's producer `occurred_at` must meet that boundary before
its content can select a turn. Preserve the captured opening clock and conservative
no-opening behavior. Every consumer evaluates the lifecycle helper on every
observation, including polls, running states and generated clears, before payload
filtering. Reevaluate retained real idle when delayed source completion arrives;
invalidate it on the watched turn-state clear/running state. Keep nonempty text,
source ownership, dismissal attribution, retry caps and suggestion revisions.
Declare `all` under the PR's `Live tests` heading, with pending-execution prose
under a separate heading; existing live scenarios remain dispatcher-owned.

| Rework event | Race-detector coverage |
| --- | --- |
| Predecessor opening/content/end append after successor watch/acceptance | `TestSourceDelayedPredecessorOpening`, `TestSourceObserverDelayedPredecessorOpening` |
| Owned idle precedes text/end append, followed only by polling | `TestConsumerIdleBeforeHistory` (all seven consumers) |
| Clear/running invalidates idle; unrelated clear preserves it | `TestConsumerIdleBeforeHistory` (all seven consumers) |

Testing: first reproduce failures with the new controls, then run the affected
scenario package and focused recorded consumer cases through ordinary Linux race
admission. Retain prior failed evidence and continuity holds. Run vet, affected
tagged compilation and build. Full acceptance/live execution and documentation
remain pending with their existing owners; macOS remains outside scope.

Security review: PASS (builder self-review, 2026-10-23). The command clock and
opening identity jointly reject a predecessor even when history append is late.
Only an actual ordered app reading can certify delivery; polls never fabricate
one. Explicit clear/running invalidation cannot be bypassed by a consumer's
payload filter. All state remains test-goroutine-owned, with bounded waits and
no new goroutines, secrets, logs, file paths, subprocess contracts, crypto or
network boundaries. Authentication/privacy precedence and production admission
are unchanged. Existing dismissal, foreign-session and retry controls remain
mandatory. Independent security review and live acceptance are still pending.

## Builder validation — command-bound consumer rework

Linux x86_64 source candidate `46ff360420e7cb6b1f9ec2fbf2657a97dee11504`.
Prospective plan/security commit `bae94a67` precedes implementation `46ff3604`.
Logs are retained in `/tmp/builder-3740-ordering/`. Counts include named Go
parents/subtests; executed excludes skipped events.

| Selection | Executed | Passed | Failed | Skipped | Log |
| --- | ---: | ---: | ---: | ---: | --- |
| New ordering controls before repair (unchanged source plus regression tests) | 62 | 26 | 36 | 0 | `red.json` |
| Affected shared scenario package after repair | 344 | 344 | 0 | 0 | `scenario.json` |
| Recorded consumer selection after repair | 11 | 11 | 0 | 0 | `recorded.json` |

All runs used `python3 scripts/test-admission.py -- go test -timeout=30m -race
-count=1 -json`. The red selection was
`-run '^(TestSourceDelayedPredecessorOpening|TestSourceObserverDelayedPredecessorOpening|TestConsumerIdleBeforeHistory)$' ./internal/e2e/internal/scenario`.
The green package selection was `./internal/e2e/internal/scenario`, including
all 62 new ordering controls and the existing ownership/dismissal controls.
The recorded selection was
`-tags e2e -run '^TestRecordedClaude/reply|TestRecordedClaude/attachment-read|TestRecordedClaude/default-outside-read|TestRecordedClaude/permission-allow|TestRecordedClaude/permission-deny|TestRecordedClaude/question-refuse|TestRecordedClaude/question-answer|TestRecordedClaude/native-suggestions$' ./internal/e2e`.
It executed those eight leaves, matching `question-answer-witness`, their parent,
and `TestRecordedClaudeFirstTurnWaitsForStartup`. Two earlier selector forms
were refused before admission and executed zero tests; the refusal is retained
in `recorded-selection-refused.stderr.log`. Admission waits were 705 seconds for
the red run, 493 seconds for the package, and 409 seconds for the recorded run.

Passed `go vet ./...`, `go vet -tags e2e ./internal/e2e`,
`go vet -tags e2e_realclaude ./internal/e2e/realclaude/...`,
`go vet -tags e2e_liverelay ./internal/e2e/liverelay/...`, and
`go build -o /tmp/builder-3740-ordering/pyry ./cmd/pyry`.
Tagged compilation executes zero live scenarios. `cmd/pyry` and `internal/relay`
are unchanged from the reviewed candidate; retain their prior admission,
authentication/version, static-key/race, clock and package evidence.

PR #3842 now declares exactly `all` under `## Live tests`, with pending prose
under `## Live execution status`; the required three numbered answers are at
`/Users/juhanailmoniemi/.codex/publish/pyrycode/verdict-handoff/rework-answers-3740-28322de7f5b2.md`.
Findings 1–3 are fixed in the pushed `46ff3604` candidate and its PR metadata.

The pre-repair 36 failing events remain failed. Preserve all earlier failed
captures and the last verifier's accepted-flaky-rerun disposition: unit gate
30626 passed/27 skipped; original fake gate 2205 passed/four failed/two skipped;
named rerun 71 passed/one skipped. Those accepted gates do not supersede the
verifier's ten failing ordering probes. Production continuity holds #3844/#3845
and the #3741 cold thread-only anomaly remain unresolved. Full configured
acceptance, normal approval/independent security review, live-Claude execution
(`all`, `needs-real-claude`) and the existing documentation handoff remain
pending with their later-stage owners. No live artifacts are required. macOS
remains outside this Linux ticket's verification scope.

## Manual completion plan, 2026-10-23

The exact-ticket manual hold is effective at central revision 110. No ticket
worker or child remains. Continue the existing feature/3740 branch from
de881516094259b71cc3c113d8329ce33966b0c5. The independent source/security PASS
for that candidate remains historical evidence, not review of these changes.

The complete live run failed five leaves. Its repeat still failed the receipt
positive control, stdio always-allow continuation, question refusal and dormant
restart reset. All four pass the retained unchanged-base comparison. The
original clear-reset failure passed the candidate repeat; its producer-gap/4414
evidence remains failed and is not reassigned from the close code alone.

Repair the confirmed fixture gaps together: timestamp the valid receipt frames,
consume preserved decrypted setup observations and distinguish generated question
clears from fresh batches, and retain the permission continuation's exact existing
turn opening rather than using a new-send boundary. Preserve negative controls,
remote refusal attribution, fresh-session permission isolation and actual terminal
delivery. Diagnose dormant reset from received frames and source observations; do
not enlarge its timeout, synthesize frames or replace the live proof with history.
Run focused controls and the affected live selections through normal admission.
Retain candidate/base failures and require independent review of every changed
candidate before the remaining acceptance and Documentation handoff.

Security assessment: author review only. These repairs change test observations,
not product authentication, admission, permissions, cryptography or persistence.
Only actually received lifecycle frames may prove completion. Exact conversation,
session, turn, answer attribution and fresh-session isolation remain mandatory.
Independent review and execution are pending. No release or deployment is included.

### Confirmed dormant reset prerequisite

The admitted controlled regression fails at the intended assertion: revival
discards the reset operation before rotation. Existing late-transition rejection
still passes. Dormant revival calls the real runtime-producer registration seam.
That advances the live generation after start's first failed bound lookup already
reserved a reset operation. The operation then loses every progress result.
This is a production prerequisite exposed by mandatory thread admission.

After successful revival, reserve again under the existing synchronous live
binding boundary. Require its captured session ID still equal the revived ID.
A changed binding must remain inert. Capture stays before workspace resolution
and asynchronous reset work, so a later transition still invalidates it. Do not
weaken generation checks, retag old payloads or hold the live boundary across
revival, filesystem work or producer activation. Nil legacy wiring keeps its
existing path. Verify positive revival and rebound refusal, existing late-change
rejection and dormant reset guards before the original authenticated live case.

Author security review: this repair preserves source ownership by checking the
revived identity before reservation. It adds no permission, authentication or
privacy exception. Independent review of the changed candidate remains pending.

### Activation-order correction

Further source inspection corrects the earlier hypothesis: Pool.Revive only
materialises. The producer starts later in wrap-up. The first control therefore
placed activation too early, and recapture immediately after revival is not a
production repair. Its limited red/green evidence and source are retained. The
package run on that superseded patch was stopped through its own admission
wrapper with exit 143; its partial log is not a complete package pass.

The phase-accurate control reserves reset progress before the actual first
producer registration, then advances to restarting. It separately requires
replacement-producer and changed-session rejection. Repair the distinction
between a cold placeholder and an existing producer, preserving pending control
operations only for the first producer of the same session. Later producer or
ownership changes must still advance generation and reject stale results.
No production change is yet validated.

### Manual socket reader correction

The two original live reset checks failed on a032af1a. The clear case reported
WebSocket framing corruption. The new actual encrypted socket regression repeats
that same failure for both polling and synchronous successor consumers. A source
poll may finish a consumer with one raw read still pending in its local closure.
The next consumer creates another read on the same connection.

Move ownership of the pending read to the fakephone connection. Short observation
polls retain it; all subsequent consumers inherit it before starting another read.
Keep decryption on the test goroutine in actual wire order. Close must release and
join the pending read. Preserve ordinary blocking-read deadline behaviour when
there is no outstanding poll. Generic non-phone receivers retain the existing
polling path. Add no production transport or cryptographic exceptions.

Author security assessment: the owner is test-only and retains at most one raw
frame. No secrets or payloads are logged. Shutdown closes the socket before joining.
The actual two-frame regression requires ordered decryption through both consumer
kinds, so a missing or duplicated frame cannot satisfy it. Independent review of
the final candidate is pending.

### Reset observation contract correction

The supplied session-live-state contract suppresses overtaken singleton revisions,
rejects earlier generations and initializes each new generation with a family clear.
The current publisher checks that contract before delivery. The old reset fixture
instead demands exactly two active observations and one clear, then indexes its
second phase after a combined count that duplicate clears can satisfy. The live
dormant case panicked with only one active observation.

Replace the exact transient count with actual current-state proof: observe active
reset progress, retain exactly one correctly owned rotation, and receive a reset
family clear naming its successor at or after rotation. Validate every observed
active phase and its handoff token, including ordering against the retained
rotation. Do not manufacture a missing restarting observation. Initial or old-owner
clears cannot establish completion. Keep fresh-note/successor-prompt proof, actual
successor recall, skipped-wrapup note preservation, source ownership and retired
wire-content alarms. Duplicate rotations remain a failure.

The protocol reset section still describes the former synchronous producer. Its
stronger linked supplied-delivery contract governs the thread fixtures. Reconcile
that stale description in the existing documentation handoff. Record this narrow
requirement correction on 3740. No product permission, crypto, stale-state exception,
timeout increase or live acceptance waiver is introduced.

## Manual author validation

Source candidate c88688e2. All commands used ordinary Linux test admission.
Evidence directory: /work/Projects/pyrycode-agents/logs/evidence/manual-3740-20261023.
Counts below are named test events, excluding package outcomes.

| Selection | Passed | Failed | Skipped | Evidence |
| --- | ---: | ---: | ---: | --- |
| Reader handoff before repair | 0 | 3 | 0 | reader-handoff-red.json |
| Complete scenario and fakephone packages | 355 | 0 | 0 | reader-handoff-green.json |
| Reset source and completion controls | 9 | 0 | 0 | reset-window-controls.json |
| Production live ownership/publication/control selection, 61 top-level tests | 147 | 0 | 0 | live-owner-controls.json |
| Actual authenticated dormant reset, normal clear and skipped wrap-up | 3 | 0 | 0 | live-reset-current-state.json |

Earlier repaired receipt, stdio permission isolation and question-refusal live
selection passed 13 named events on 673750b3. Those fixture changes are unchanged
in this source candidate. Its first-producer controls passed seven named events
on a032af1a, now included in the 147-event current selection. Both original reset
live cases failed on a032af1a; preserve live-reset-repair.json as failed evidence.
The superseded command-package run exited 143 and supplies no complete-suite pass.
No original failed capture is replaced by these focused results.

Passed go vet ./..., tagged realclaude vet and go build ./cmd/pyry on c88688e2.
Build output stays in the evidence directory. These are author results. Fresh
independent review, full configured acceptance and the Documentation handoff remain
pending. No merge, release or deployment is authorised. Documentation-only changes
to this validation record do not alter the tested source tree.

## Manual rework of independent review 839ecccc

The final independent verdict is FAIL despite all ten configured offline commands
passing. The exact-ticket manual hold is effective at revision 147, with no ticket
worker or child. Resume the existing feature/3740 branch at the reviewed combined
candidate. Preserve its successful gates, the two independently failing probes
and all older failures. No new requirement or weaker acceptance is proposed.

The shared callable SourceFacts API drops watch metadata. Avoid a second source
protocol or fabricated history entries: enforce the captured command window in
one adapter shared by SourceSince and ObserveSource. Capture its time before the
history watermark and submission. Scan real opening records and the first actual
acceptance within that window before exposing main-turn content. Only main facts
whose producing identity has an opening within that command window can be selected.
Keep subagent facts, unsupported-content alarms and other source families intact.
QuestionBoundary keeps the original valid opening for an already parked turn;
answering it must not start a new command window. Existing SourceObserver.Watch
already owns its pre-command boundary. Also invalidate a selected source turn if
later acceptance proves it began before the command. No received terminal frame is
synthesized or replaced by source evidence.

Audit all feed consumers: completed reply, attachment, outside-read, permission,
refusal, answer, suggestion, subagent, hook and cancellation. Exercise both real
adapters, including predecessor facts before acceptance in one and split source
observations. Keep idle-before-history and parked-continuation controls. Reuse the
independent exact-candidate red logs, then run affected shared race tests, recorded
consumers and relevant authenticated cases through ordinary admission.

Retain the actual active reset envelope beside its payload. Require top-level
conversation and session metadata to match the rotation's conversation and
predecessor, rejecting missing, null, malformed or foreign values. Keep successor
clear validation and all fresh-note/recall/skipped-note proof. Commit the independent
owned and four missing/foreign negative controls, then rerun the three actual reset
cases. This is author validation. Changed-candidate independent review and complete
configured/live acceptance remain mandatory before Documentation.

Author security assessment: only test observation changes. No authentication,
permissions, data storage, transport, encryption or product admission is weakened.
Source filtering uses actual recorded execution identity and producer time, never
history append order alone. Non-main alarms remain observable. Unknown ownership
cannot certify completion. No new goroutine, global state or credential handling.

### Manual rework validation

Source candidate ec00848e84f3dff6b33009f35bdfbd04ea2f788c. Linux commands used
ordinary test admission. All named counts exclude package outcomes. This record
changes documentation only after the tested source candidate.

| Selection | Passed | Failed | Skipped | Evidence |
| --- | ---: | ---: | ---: | --- |
| Both command adapters and completion consumers | 38 | 0 | 0 | completion-window-controls.json |
| Reset envelope ownership and existing source controls | 19 | 0 | 0 | reset-ownership-controls.json |
| Complete shared scenario package | 385 | 0 | 0 | completion-rework-scenario-final.json |
| Recorded reply, permission, question, attachment, suggestion, child and hook consumers | 14 | 0 | 0 | completion-rework-recorded.json |
| Actual dormant reset, normal clear, skipped wrap-up, permission isolation and three question flows | 7 | 0 | 0 | completion-rework-live.json |

All three admitted vet commands passed: the shared scenario package, tagged
realclaude packages and tagged liverelay packages. Exact commands and source
candidate records are retained beside their logs. The first complete scenario
attempt failed one old fixture lacking an actual execution opening. Its failed
completion-rework-scenario.json remains; the corrected fixture still asserts
predecessor-idle rejection. Independent failed probes and the final FAIL on
839ecccc remain authoritative historical results. Its ten passing offline gates
are ancestor evidence, not complete acceptance of this new candidate.

The captured command window now applies to both real source adapters, including
predecessor observations in a separate poll before successor acceptance. Main
content requires its real execution opening within the watched command window.
Child source facts and unsupported-content alarms remain visible. Active reset
proof retains and validates actual conversation and predecessor-session metadata.
Missing, null, malformed and foreign values fail. The seven authenticated cases
retain actual encrypted delivery, successor prompts and recall, parked question
and permission continuation, skipped-note preservation and session isolation.

These are manual author results, not an independent PASS. Fresh independent
source/security review and complete configured/live acceptance remain pending.
Keep the PR draft and stop at Documentation. No merge, release or deployment.

## Manual rework of independent review 420bbc46

The actual final independent FAIL identifies overlapping queue observation and a
late-answer permission fixture that never reaches its surface. The manual hold
is effective at revision 164. No ticket reservation or actual child remains.
Reuse the completed candidate failures and passing unchanged-base comparison.
The prior strict command boundary and active reset ownership repairs remain.

Give the multi-turn queue reader one private, fixed source window established
before its first command. Subsequent queued sends must still refresh the ordinary
single-command observer's boundary, but must not hide the queue reader's actual
predecessor terminal. Share the existing commandSource filter and keep initial
history exclusion, actual session/turn identity, delivered running-to-idle state,
final correlated catch-up and complete placement assertions. Add a real-history
regression that queues during an open turn, proving both the queue's two terminal
observations and the ordinary observer's rejection of that predecessor.

The child-exit fixture currently exits immediately after writing its permission
request. Supplied state may overtake the surface before the phone receives it,
so this does not establish the parked permission required by a late-answer proof.
Use the existing actual child status and kill helpers to end the origin only
after actual permission-surface delivery and validation. Retain the origin's
correlated dismissal, verify a different replacement child and its synchronized
stdin, and prove the old answer is not delivered to it. No timeout increase,
synthetic surface, production permission change or removed assertion is planned.
Confirm this causal correction by running the affected fixture and permission
siblings under ordinary admission.

Run the overlapping control red before its repair. Then run the complete shared
scenario package, both recorded queue scenarios, the affected fake permission
family and relevant live queue consumers. Preserve all failed logs and meaningful
negative controls. Publish separate author validation, keep the PR draft and
obtain fresh independent review plus complete configured/live acceptance before
Documentation. No merge, release or deployment is authorised.

Author security assessment: only test observation and fixture sequencing change.
The queue reader still needs genuine history, delivered lifecycle and final
checkpoint proof. A single-command consumer must still reject predecessor
completion. The controlled crash targets the actual isolated fake child after
permission delivery, never the daemon or another task. Replacement identity and
late-answer refusal remain mandatory. No new exported API or global state.

### Queue and permission author validation

Source candidate 901e15f24f788120390a3131d75f08b85adab30e. All commands used
normal Linux admission. Counts exclude package outcomes.

| Selection | Passed | Failed | Skipped | Evidence |
| --- | ---: | ---: | ---: | --- |
| Actual overlapping queue control before repair | 0 | 1 | 0 | queue-overlap-red-executed.json |
| Complete shared scenario package after repair | 386 | 0 | 0 | queue-rework-scenario.json |
| Both recorded queue cases and all fake-provider permission cases | 15 | 0 | 0 | queue-permission-rework-e2e.json |
| Actual authenticated send-now and ordinary queue placement | 2 | 0 | 0 | queue-permission-live-queues.json |

Passed shared scenario vet, tagged fake e2e vet and tagged liverelay vet. The
15 named events include the two parent outcomes. The complete existing queue
arrival/order and permission late-answer assertions execute. The new source
control proves both overlapping queue terminals remain observable while the
ordinary command observer rejects its predecessor. Real delivered idle and final
correlated catch-up remain necessary. The initial new control had a compilation
error using the append return type incorrectly; queue-overlap-red.json retains
that failure and supplies no executed behavioural proof.

The permission setup now ends the actual isolated owner only after receiving and
validating its surface. It requires a distinct replacement and keeps synchronized
stdin and old-answer rejection. No arbitrary sleep, enlarged deadline, fake
surface or product permission exception is introduced. The shared source window
is private to queue observation. No exported helper API or global state is added.

The independent FAIL on 420bbc46 and all original, repeated and base comparison
logs remain. Its prior two ownership findings are confirmed fixed, but this is
only new author validation. The eight green ancestor configured commands do not
supply current complete acceptance. Fresh independent review and complete normal
configured/live gates remain mandatory. Keep the PR draft and stop in
Documentation. No merge, release or deployment.

## Manual reconnect-fixture correction after live acceptance

The complete live gate on 7da07191 executed 2563 named events: 2562 passed and
one reconnect fixture failed, with 27 skips. Its exact failing repeat remains red
and its unchanged-base comparison passes. The effective manual hold is revision
180 after all actual children and reservations settled. Preserve the complete
failed run and its comparisons. The independent offline PASS remains historical
approval of 7da07191, not this next fixture correction.

Change only the tagged reconnect fixture's retained records. Supply valid
producing-session metadata and an actual execution opening after its watched
command. Keep valid pre-command source facts to prove their exclusion. Across
all three connections require the same exact current opening/content IDs,
current send and receive ciphers, monotonic request correlation and inherited
pending-reader behavior. Do not alter the shared adapter, loosen ownership,
remove an assertion or enlarge a deadline.

Inspect the other direct retained-history fixtures together and run their cheap
hermetic tagged ownership/completion controls with reconnect before another full
provider run. Reuse the failed exact-candidate red evidence rather than repeating
it unchanged. Record every executed result. Preserve all prior complete passes
and failures honestly. Publish the small source-equivalent scope and request
normal independent re-review and required admission. Full live acceptance remains
required; no complete pass is inferred from the 2562 other passing events.

Author security assessment: fixture records gain valid test-only provenance and
an execution opening. Product authentication, permissions, transport, persistence
and shared observation code remain untouched. The expected retained source facts
stay explicit, and actual encrypted reconnect delivery remains required. No
synthetic app envelope, new exported API, timeout exception or global state.
Stop in Documentation after required review and acceptance. No merge or deploy.

### Reconnect fixture author validation

Source candidate 1da350cbb953a1fa81f2586f097abff0d48767cd changes only the
hermetic test in shared_scenario_wire_reconnect_test.go. Its retained records now
have valid producing-session metadata and a current execution opening. Every
connection requires the same exact current opening and content IDs, excluding
the valid pre-command predecessor records. All encrypted correlation, replaced
cipher and pending-reader assertions remain. The shared helper is unchanged.

Normal admitted race checking executes 45 named hermetic tagged controls, all
passing with zero failures or skips. These group reconnect, prompt history and
reset ownership/completion, including missing/foreign/malformed negative cases.
Other direct history fixtures were inspected together; their provenance is
already explicit. Tagged realclaude vet also passes. No provider credentials or
provider execution are claimed for these controls. Evidence and exact commands:
reconnect-fixture-controls.json, reconnect-fixture-controls-command.json and
reconnect-fixture-vet.log under the existing manual evidence directory.

The original complete full live attempt remains failed: 2562 named passes, one
failure and 27 skips. The same-tree repeat fails and unchanged-base single-case
comparison passes. All those captures and metadata remain under live-failed-7da07191.
The offline independent PASS is still for 7da07191. This tagged-only fixture
correction needs independent re-review and required normal acceptance. No original
outcome is changed, no gate is waived and no full current live pass is claimed.

## Manual terminal-ownership repair after independent FAIL 3743f4ba

The settled normal verifier confirmed the source finding with its admitted race
probe. An omitted terminal session passes the timestamp-only fallback even when
source content has an explicit owner. Its owned, foreign, null and empty controls
remain retained. The exact-head independent FAIL and original complete integration
failure plus the passing named repeat remain separate historical outcomes.
Manual hold revision 196 and no reservation or ticket child precede this repair.

Require the actual retained idle envelope to carry a valid, nonempty session ID
matching the selected source producer. Remove the unowned timestamp fallback.
Keep the existing execution-opening bound, idle-before-history retention, clear
and running invalidation, command boundary and encrypted delivery. No production
protocol or admission change is planned. No timeout increase or synthetic facts
will be added to runtime observation.

Add omitted, null, empty and malformed terminal controls to the existing owned
and foreign tests, and exercise omitted ownership across the existing completion
consumers. Positive synthetic fixtures must supply their declared source owner;
retain every predecessor, generation, queue and read-count assertion. Inspect
related fixture helpers together, then run the complete shared scenario package,
the independent exact probe against the repair, grouped tagged history controls
and meaningful recorded/live consumers through ordinary Linux admission. Tagged
compilation alone is not execution proof. Complete acceptance stays with normal
review and dispatcher admission. Preserve valid prior checks and all failures.

Author security assessment: this strengthens test proof by requiring independently
observed matching terminal identity. It does not project source facts into actual
app envelopes or change product authentication, persistence, permissions or
transport. The terminal must follow actual execution, and its retained source end
must exist. No exported API or global state is added. Publish author evidence separately
from the real independent FAIL, obtain fresh independent review, keep the PR draft
and stop in Documentation. No merge, release or deployment is authorised.

### Terminal ownership author validation

Source candidate 47f6835d06815e1ea34c94dd32f4fadcd0631de9. Normal Linux admission supplies these named
outcomes, excluding package results. All selections have zero failures or skips.

| Selection | Passed | Evidence |
| --- | ---: | --- |
| Complete shared scenario package | 398 | terminal-owner-scenario.json |
| Unchanged independent ownership probe against the repair | 6 | terminal-owner-independent-probe.json |
| Grouped tagged reconnect/history/reset controls | 45 | terminal-owner-tagged-controls.json |
| Recorded completion consumers, queue cases and permission family | 28 | terminal-owner-recorded.json |
| Actual authenticated reset, permission isolation and question consumers | 7 | terminal-owner-live.json |

Shared scenario, tagged realclaude and tagged liverelay vet all pass. Exact
commands, candidate identities, OS, timestamps and exit codes are the corresponding
terminal-owner-*-command.json files under the retained manual evidence directory.
The probe source is unchanged from the real independent failing execution; only
the scratch overlay maps it into this retained source checkout. These successful
reruns are author validation, not a new independent review. No provider execution
is attributed to the 45 hermetic tagged controls or tagged compilation.

Omitted, null, empty, malformed and non-string terminal sessions cannot complete.
Seven existing completion consumers must wait past an unowned terminal for actual
owned delivery. The complete suite retains early idle, cleared/running state,
foreign ownership, predecessor rejection and overlapping queue controls. Positive
unit fixtures now declare the same owner as their synthetic source facts. Actual
runtime observations are never tagged from source data. Product code, deadlines,
permissions, persistence and public APIs are unchanged.

The real independent FAIL on 3743f4ba remains published and its original red probe
and all configured stdout/stderr captures are retained under independent-fail-3743f4ba.
Its initial integration capture failed one suggestion reconnect case which passed
the normal repeat. That original failed capture stays failed. No new full unit,
integration or live acceptance is inferred from this targeted author validation.
Fresh independent review and required normal configured/live acceptance remain
pending. Keep the PR draft and stop in Documentation. No merge or deployment.

## Manual completion-control fixture correction after live fb216ba8

The completed full tagged run on fb216ba8 has 2548 named passes, 15 failed named
events and 27 skips. The failures are 13 leaves and two parent outcomes across
QuestionAnswerModelTurnControls and CompletedTurnDrainControls. The same-tree
repeat has 23 passes and the same 15 failures; unchanged base passes all 38.
The strict shared completion helper is already independently approved. These
synthetic encrypted terminals omit the fixture source owner, so the positive
paths cannot settle and model-negatives fail at an earlier completion check.
No reservation or actual ticket child remains. The effective manual hold is
Development revision 219. Retain all original, repeat and base captures.

Only the two fixture families should change. Their synthetic idle constructors
must explicitly declare the same fixture owner as their synthetic source facts.
Do not add metadata inside runtime observation or weaken the shared helper.
Retain encrypted delivery, frame draining, foreign-conversation rejection, model
validation, diagnostics, read order and every existing failure expectation. Add
omitted, foreign and null terminal-session negatives so these same fixtures prove
that the strict rule remains required. No timeout increase is planned.

Reuse the completed red repeat and passing base instead of duplicating them.
Run both affected families through ordinary admission. Then execute the entire
tagged package with provider credentials deliberately absent, relying on its
existing authentication guard to skip provider-dependent cases. This broad cheap
sweep must retain exact executed/passed/failed/skipped names and commands. It is
provider-free validation, not complete live acceptance. Do not acquire credentials
or turn skips into passes. Normal authenticated full acceptance remains required
later, after independent re-review. Preserve the valid unchanged helper review
and offline results through normal admission, with all failed history retained.

Author security assessment: only synthetic encrypted fixture envelopes gain their
explicit declared owner. New negatives reject lost or foreign attribution. Actual
wire/source adapters and all product authentication, persistence, permissions and
transport remain unchanged. No new API or global state. Publish author evidence
separately from independent review. Keep the PR draft and stop in Documentation.
No merge, release or deployment is authorised.
