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
Channel-post publication remains for #3737.

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
handoff, not parent #3718's final family acceptance. Other operating systems
are outside this ticket's verification scope. See
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
on `81831fd4` was resolved by removing the unused test helper, while shared
production receipt adapters remain. This evidence covers the Linux ticket;
macOS is outside its verification scope. Other runtime/startup and prompt-answer
receipts, standalone banners and channel-post publication remain for #3737,
and #3718 still owns final family acceptance. See the
[plan and revisions](../../specs/architecture/3736-session-content-retirement.md).
