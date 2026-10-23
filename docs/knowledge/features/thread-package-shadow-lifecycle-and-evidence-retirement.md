# `internal/thread` — source retirement and lifecycle evidence

[Shadow lifecycle overview](thread-package-shadow-lifecycle-and-evidence.md).

## Shadow lifecycle and evidence

`runSupervisor` composes `thread.NewStore` with the same `history.Store` used by
interactive emission, session transitions, operator sends/queue facts and direct
channel posts. After `reconcileStartupHistory` and completion of `startRelay`'s
runtime-history construction, `threadShadow.start` schedules registered old logs
in the background without waiting for replay. Construction must finish before
shadow quiescence readers enter: `installRuntimeHistory` assigns
`busy.runtimeSink`, which those readers subsequently treat as immutable.
Folding consumes raw entries, including hidden facts and captured source
provenance; it never attributes old entries through the active session cursor.

Interactive `assistant_delta`, `tool_use`, `tool_result`, `tool_denied` and
`turn_end` remain source vocabulary (#3734). `interactiveTurnEmitterV2.emit`
attempts their history append once, even without clients, then returns before
legacy projection, replay insertion and fan-out to every app, including peers
without `thread`. Successful appends preserve existing payloads, timestamps,
visibility and producing-session provenance. Nil/failed storage cannot restore
raw content or substitute receipts; supported live state remains separate.
Channel-post publication is also retired (#3737); see the residual-content
section below.

`TestInteractiveContentRetired` drives interleaved main/child text, child tool
use/result, denial and duplicate completion through the emitter and real Store.
It applies actual live additions/patches/appends, rejects duplicate additions
and broken base revisions, and compares final rows with explicit catch-up.
One publication interval may contain several revisions of one item: counting
frames as final rows would reject correct delivery. Ordered settled text and
scoped parents survive reused message/tool IDs across conversations and producing
sessions. Only genuine unsupported source content becomes a notice; normal
completion need not add a visible end row. `TestInteractiveContentRetiredStorage`
checks nil/failed history, absent clients and content-free failure logs alongside
absent raw frames/replay. These deterministic proofs and tagged compilation do
not establish live acceptance. See [source and canonical test observation](e2e-harness-stream-interactive-harness-pattern-startstreamin.md#source-and-canonical-observation).

Background-agent `background_task_started`, `background_task_updated` and
`background_task_roster` values likewise retain their original append attempts,
including without clients (#3735). `recordAgentFact` and `closeAgentHistory`
retain neutral identities, source/lifetime attribution, outcomes and saved
gone/session-ending causes. Mapped and neutral evidence feeds the canonical fold;
none of these producers publishes compatibility receipts. No app receives raw
agent content, newly appended replay copies or connect-time roster snapshots,
including peers without `thread` and nil/failed-history operation. The runner's
roster/join hold still supports addressed stops; task progress and other supported
live state retain their delivery and ordering contracts.

`TestAgentContentRetired` compares applied live revisions with explicit catch-up.
Late task links preserve launch/result meaning after main-turn end; attributed
child text, reported outcomes, complete empty versus incomplete rosters and saved
session causes retain one agent item with a stable ID. Reused identities remain
scoped by conversation and source; warm/reopened reads retain timestamps,
visibility and provenance. Agent-history and session-ending families retain
lifetime isolation. Child/background traffic cannot change the main turn.
`TestRelayV2_AgentContentRetiredReconnect` uses an ordered control reply to prove
absence of roster publication for both capability modes, then requires explicit
authenticated thread catch-up to finish.

**Correlate agent receipt assertions with source identity.** Agent facts and
other runtime producers share `banner`; banning every banner would reject
surviving sibling runtime behavior rather than isolate agent retirement. Match
banner history IDs to agent source types in both live and replay assertions.
Source-observing fixtures must reject retired transport envelopes: `SourceObserver` exposes retained start/update/roster
entries separately, never fabricating compatibility delivery. See
[source and canonical observation](e2e-harness-stream-interactive-harness-pattern-startstreamin.md#source-and-canonical-observation).

**Live stop proof is separate from deterministic evidence.** Offline helper
passes, tagged compilation, replay and control acceptance establish no live
completion. `TestRealClaudeStopBackgroundTaskCompletion` identifies the exact
physically executing FIFO-held task using retained source joins, sends the
addressed stop and requires its canonical terminal item to match a post-stop
saved task outcome or complete-roster gone fact with producing-session
provenance while the FIFO remains held. The dispatcher subsequently counted it
passed, alongside `TestRealClaudeComposerStopPreservesBackgroundTask`
on Linux at candidate `ad1cb1aec21452edd41c01ddc76a1c39e566dbcd`
merged with main `ac0a13f59ebe` (2026-10-22). That configured live run counted
2267 executed, 2267 passed, 0 failed and 27 skipped; skips are not evidence for
those skipped probes. This supersedes the PR/verifier's earlier pending-live
handoff. Its child scope is supplemented by the integrated acceptance below.
Other operating systems are outside this ticket's verification scope. See
[the reviewed implementation and handoff](https://github.com/pyrycode/pyrycode/pull/3830)
and [the plan](../../specs/architecture/3735-retire-agent-publication.md).

## Session and content-notice retirement (#3736)

`sessionTransitionEmitterV2.broadcast` retains runtime `session_divider` and
validated transition appends, then invokes the committed-switch callback after
recording. It publishes no raw transition or substitute divider receipt.
`interactiveTurnEmitterV2.emit` retains both refusal types, compaction boundaries
and unsupported-content notices, then returns before projection, replay or
fan-out. This applies to every app, including peers without `thread`, absent
clients and nil/failed history. Successful appends preserve payloads, timestamps,
visibility and captured conversation/agent/session provenance. Unknown reasons
and unresolved ownership remain rejected; diagnostics contain no source prose.
The canonical fold consumes these records as session dividers, notices and
compaction items. Supported model/settings/catalogs, prompts, progress, retry
and compaction live state retain their tags, ordered readings/clears and controls.
Runtime pre-divider closure, switch completion/cancellation and busy release
remain on their existing lanes; delayed predecessor output cannot close or clear
the successor.

`TestSessionContentRetired` applies real canonical live revisions and compares
them with explicit catch-up through reset, clear, agent switch, recovery, sleep
and eviction causes across conversations and reused source IDs. Repeated
publication adds no duplicates; refusal/compaction/unsupported-content records
inside and outside a turn create no lifecycle edges. Storage controls in
`TestSessionContentRetiredStorage` reject raw frames/replay and divider receipts
while retaining switch completion, including nil/failed storage and no clients.

**Source visibility is not live delivery order.** A transition can be readable
from history before earlier live reset edges have drained. Reset-reader tests
compare retained transition time with actual `resetting` envelope timestamps,
rather than treating a source poll as a frame arrival.
`TestResetWindowSourceTransitionOrdering` exercises early visibility and repeated
polling without inventing a transport frame or duplicating the transition.
Similarly, delaying `ActiveConns` no longer pauses a source-only transition:
`TestRelayAgentSwitchDelayedPublication` pauses the retained committed-row
callback to preserve the exclusion/sealing proof. Keep a barrier on an operation
that still runs after retirement.

Linux evidence at `9ebba3462074f1537741a08c06151eae2343693b` (2026-10-22),
with the live gate tested against main `915a2bcbfe8c`:

| Dispatcher command | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| `python3 scripts/test-admission.py -- go test -timeout=30m -race -json ./...` | 28121 | 28121 | 0 | 27 |
| `python3 scripts/test-admission.py -- go test -timeout=30m -tags e2e -race -count=1 -json ./internal/e2e/...` | 1963 | 1963 | 0 | 2 |
| `go test -tags e2e_realclaude -timeout 30m -json ./internal/e2e/realclaude/...` | 2268 | 2268 | 0 | 27 |

These are dispatcher-reported leaf counts; skips are excluded from executed and
are not passes. The configured live gate passed the named
`TestResetWindowSourceTransitionOrdering`; that test itself is an offline helper,
so its tagged pass alone proves no live behavior. The subsequent counted live
suite supersedes the earlier pending-live handoff. Guard, vet, test-tools,
restoration selection, staticcheck, substrate and build gates also passed.
The builder's exact admitted selection in [PR #3831](https://github.com/pyrycode/pyrycode/pull/3831)
passed 163/163 parent/subtest events, zero failed/skipped, on the same candidate;
these event counts use a different unit from the leaf counts above. Tagged vet
also passed and establishes compilation only.

Historical failures remain failed: the initial builder package run recorded
16887 executed, 16881 passed, 6 failed and 4 skipped; the metadata/switch
corrections subsequently passed 43/43 events with zero failed/skipped. The PR
retains the earlier red retirement/focused results. Staticcheck's U1000 finding
on `81831fd4` was resolved by removing the unused test helper. Shared receipt
adapters remained at that candidate and are removed by #3737 below. This evidence
covers the Linux child ticket; macOS is outside its verification scope.
Final integrated acceptance is recorded below. See the
[plan and revisions](../../specs/architecture/3736-session-content-retirement.md).

## Residual content retirement (#3737)

Standalone `banner` sources, runtime/startup facts, winning remote
`prompt_answered` resolutions and channel post chunks/completion retain their
append attempts, payloads, timestamps, visibility and captured provenance.
No app receives raw banners, post deltas/completion, inert receipts or new
content replay copies on initial connection, live output or reconnect, with or
without `thread`. Nil/failed history and no clients never restore publication.
Successful source appends feed authoritative thread items and explicit catch-up.
Open prompts, single dismissals, controls and live state remain separate,
with conversation/session/generation identity and ordered delivery intact.
`historyVisibilityMetadata`/`historyEntryShown` and constructor seams survive;
exclusive receipt/prompt/post publishers are removed. The interactive envelope
counter still serves supported live state; event replay and ring storage/wiring
are now removed (#3738/#3739). The integrated acceptance below covers #3718;
[integrated recovery acceptance](v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md#integrated-acceptance)
records #3719's final verdict and pending checks.

`TestResidualContentRetired` applies actual canonical live updates and compares
explicit catch-up across conversations with reused turn/answer IDs and original
session provenance; repeat publication adds no duplicates.
`TestResidualContentRetiredStorage` checks absent/failed storage, no clients and
content-free diagnostics. `TestPromptAnswerPublicationRetired` folds all five
actual remote decisions to canonical answer notices under the asking session,
including after rebinding. Unauthorized, stale, invalid, duplicate, timeout and
teardown paths retain their rejection/non-recording rules. The saved context
remains bounded to 16 KiB without changing child input.

**Removing receipt delivery must remove its wait dependency too.** A connection
query on the answer stack can wait on the very relay loop resolving the answer;
releasing bridge/registry locks alone does not avoid that cycle. There is now no
receipt worker, serialization mutex or discovery call on the recording path.
`TestPromptAnswerRelayRemainsResponsive` retains winning verdict/dismissal and
relay progress; `TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer` holds an
unrelated discovery query across consecutive answers, with cancelled context and
failed storage controls. Storage failure never retries actuation or waits for
receipt delivery. See [prompt ownership and responsiveness](history-package-producers.md#resolved-remote-prompt-answers-2973).

**Durable completion owns recovery and wake eligibility.** Channel partial-write
retry preserves ordered chunks and original source without duplicates. Failed
completion stays pending without a wake; only a newly appended successful
completion calls the asynchronous waker. Cleanup-only reload/retry and catch-up
must not notify again. `TestChannelPost_RecordingReplayLiveWakeOrder` retains the
record-before-wake proof despite its historical name, rejecting raw fan-out and
repeated wakes. Recovery checks compare the same canonical completed post after
partial writes and cleanup/restart. Inspect durable text/chunks/completion in
poster helpers: a synthetic fan-out oracle would rebuild the retired contract
and lose the actual carry, chunking and turn-identity witnesses. Active-turn,
write-reservation and teardown boundaries remain.

Linux acceptance on `895f4ff0c9bf84fe5723dacfbedff9f253a9d92d`, merged with
main `287ecbd15727` (2026-10-22), supersedes the PR/verifier's earlier pending-live
handoff. The dispatcher ran
`go test -tags e2e_realclaude -timeout 30m -json ./internal/e2e/realclaude/...`:
2250 executed, 2249 passed, 1 failed, 27 skipped (leaf counts excluding skips).
`TestInteractiveStreamForwardsSubagentText` failed, then passed the same-tree
named rerun, 1 executed/1 passed/0 failed/0 skipped. The stage record accepts this
as a flaky pass; the original failed result remains failed. The first pass
individually passed `TestInteractiveStreamStdioModalAllow`,
`TestInteractiveStreamStdioPermissionDeny`, `TestInteractiveStreamQuestionAnswer`,
`TestInteractiveStreamQuestionRefusal` and
`TestInteractiveStreamHookBlockedBannerReachesTheClient`.
The retained run is `2026-10-22T22-54-41-912Z`; accepted live stage
`6695439568ef939d61191ec80543f212f5f06376c72a45729f78f3010fff1c65`
records candidate, base, named results and rerun separately.

[PR #3832](https://github.com/pyrycode/pyrycode/pull/3832) retains exact admitted
focused commands and earlier failed attempts. Final focused daemon race checks
passed 211/211 events and banner metadata checks 42/42; three channel scenarios
each passed 1/1 and offline tagged hook fixtures 11/11, all with zero failed or
skipped. These counts include parent/subtest events and differ from live leaf
counts. The verifier accepted the Linux race unit gate and fake-daemon gate;
`TestRecordedClaude/session-resume` originally failed and passed on rerun, tracked
in [#3833](https://github.com/pyrycode/pyrycode/issues/3833). Tagged vet establishes
compilation only; offline hook fixtures execute no Claude turns. Mixed tagged
oracles retain the no-read refusal window, correlated ACK, later live phase,
canonical queue delivery, actual wake and held-turn/successor order rather than
merely deleting compatibility tests. macOS is outside this Linux ticket's
verification scope. These child results alone do not establish parent
acceptance; the current combined proof follows below. See the
[plan and revisions](../../specs/architecture/3737-retire-content-adapters.md).

## Integrated retirement acceptance (#3718)

The [archived contract](https://github.com/pyrycode/pyrycode/issues/3718#issuecomment-6323312178)
is pinned by SHA-256
`1500d9c108066e72576389f580c06ebd973d676cf1e67fa337b93429ed2f48ee`.
The [requirement-to-evidence map](../../specs/architecture/3718-integrated-content-retirement.md#requirement-to-evidence-map)
retains the four merged child PRs/revisions, accepted stage IDs, exact selections,
original failures and accepted reruns. Those ancestor implementations remain
applicable because later children remove additional publication paths while
preserving source/fold contracts. Their acceptance alone did not establish the
integrated boundary or the changed reader's live behavior.

Final verifier PASS on [PR #3838](https://github.com/pyrycode/pyrycode/pull/3838)
was recorded at `74387269216c2ce891c4436e8956c33218d6d456` on Linux.
The dispatcher then tested that feature revision merged with main `b779b6acacf3`
in the configured real-Claude gate, run `2026-10-23T01-49-42-876Z` (8m 40s).
The daemon was built from that tested tree. Subsequent documentation edits
change no production, test or build inputs.

| Final configured selection | Executed | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| `python3 scripts/test-admission.py -- go test -timeout=30m -race -json ./...` | 28076 | 28076 | 0 | 27 |
| `python3 scripts/test-admission.py -- go test -timeout=30m -tags e2e -race -count=1 -json ./internal/e2e/...` | 1971 | 1971 | 0 | 2 |
| `go test -tags e2e_realclaude -timeout 30m -json ./internal/e2e/realclaude/...` | 2264 | 2264 | 0 | 27 |

These are dispatcher-reported leaf counts, excluding skips from executed.
The verifier's parent/subtest-event totals use a different unit and are not
summed with them. Citation/docs guards, vet, helper tests, staticcheck,
substrate guard and build passed. The restoration selector passed with exhaustive
restoration not required because relevant code/dependencies/build inputs were
unchanged; this is no executed exhaustive sweep. Affected tagged vet also passed,
establishing compilation separately from live acceptance.

| Retained obligation | Final evidence and disposition |
| --- | --- |
| Combined retirement and storage boundary | `TestInteractiveContentRetired`, `TestAgentContentRetired`, `TestSessionContentRetired`, `TestResidualContentRetired` and applicable storage/answer families retain source/fold, canonical live/catch-up, reused-identity and no-raw-publication controls. The integrated race gate passed. |
| Actual child/roster lifecycle neutrality | The verifier individually confirmed `TestRelayV2_AgentContentLifecycleNeutral` and `TestRelayV2_AgentContentRetiredReconnect` passed in the fake-daemon gate. The former requires actual encrypted main running/idle, then source child/roster/outcome witnesses plus live task progress and a correlated ordered reply without another turn-state envelope. `TestAgentContentRetired` additionally checks emitted phases for both capabilities and reused identities. |
| Main completion/source identity | `TestCompletedSourceText` and all four `TestCompletedSourceTextIdentity` leaves passed in the configured tagged gate: session reuse, agent-kind reuse, later child completion and text after completion. |
| Changed reader's actual live consumers | `TestInteractiveStreamMultiTurnContinuity` passed (7.3s); `TestInteractiveConversationSystemPrompt_ReachesTheReply` passed (6.6s). Both retain actual running/idle before reading the completed source text. Their previously pending live obligation is satisfied. |
| Supported controls, isolation and ordering | Current configured unit/fake/live gates supplement the child requirement map and retained mixed lifecycle/control assertions. `TestInteractiveStreamForwardsSubagentText` passed (24.8s) in this full live run, without a flaky rerun. |
| Skipped probes and downstream work | The 27 live skips are opt-in captures, unconfigured Codex captures and API-key MCP probes, not acceptance of those behaviors. Replay admission/pump (#3738), shared ring/`SetReplaySource` (#3739), deployment and operator/approval hold release remain outside this acceptance. |

The integrated live gate had zero flaky passes. Earlier failures remain failed:
\#3737's subagent-text original and named accepted rerun remain separately counted
above; #3734's failed full modal run and failed named rerun, #3735's original
replay-equality failures and #3736's package/staticcheck failures remain in the
linked map. For this ticket, the controlled old source reader passed only the
legacy fixture: 6 events, 1 passed/5 failed/0 skipped (5 leaves, 1 passed/4 failed).
The corrected focused run passed 6/6 events (5/5 leaves), with no failures/skips.
The earlier verifier FAIL concerned PR live-test selection; the published
routing repair closed it without rewriting any failed evidence.

**Lifecycle neutrality requires a wire witness.** A source callback cannot see a
leaked `turn_state`, and silence is vacuous without proving traffic occurred.
Keep source joins and progress witnesses independent of the encrypted reader;
drain to an ordered control reply after the witnessed main idle. Reconnect's
absence-of-roster proof alone does not establish phase neutrality.
For completion joins, use both producing kind/session and the main completion's
entry boundary; a reused turn ID or a later child completion can otherwise select
foreign text. See [source-reader identity controls](history-package-producers-interactive-provenance.md#interactive-provenance-2981).

Disposition: integrated content-publication retirement and both retained #3734
coverage findings are accepted on Linux. This supersedes ADR 042's transitional
raw-publication compatibility rule while preserving its retained sources and
supported live state. macOS is outside this ticket's verification scope. No
second-OS pass, skipped-probe pass, downstream replay removal or hold release is
implied.
