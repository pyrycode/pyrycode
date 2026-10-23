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
