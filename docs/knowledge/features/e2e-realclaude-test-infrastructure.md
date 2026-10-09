# Live Claude test infrastructure

Infrastructure, staging and evidence contracts for the
[real-Claude suite](e2e-realclaude.md).

## Test infrastructure

Source citation edits can break exact comparisons with historical capture prose.
`TestDropcapFixtureIsACapture` normalizes only the two filename/position labels
recognized by `dropcapHistoricalCitationLabels` before comparing recorded prose
with current constants. Keep committed capture bytes intact and every other
prose byte exact: broad normalization would hide changes to the capture's claims.

`fixtures_test.go` re-execs the test binary as a fake `pyry` through `TestMain`
when a helper test opts into `RunOpts.UseTestBinaryAsFakePyry` and supplies
`GO_TEST_HELPER_PROCESS=1` in `ExtraEnv`. The fake selects behaviour from
`PYRY_E2E_FAKE_MODE` (`happy`, `fail`, `sleep`, `argv`), letting helper-contract
tests run without real Claude or a daemon build. `ensurePyryBuilt` instead uses
a configured `PYRY_E2E_BIN` or builds an untagged daemon; it rejects the test
binary itself as that override. Globally setting `PYRY_E2E_BIN=os.Args[0]` made
real-daemon callers recursively re-execute the suite, so fake selection is an
explicit per-call choice. `TestClaudeBinaryAvailable` checks real Claude on PATH.

Same-runner session-error recovery. An external driver can hold an isolated
daemon's Claude in a crash loop and release it through a local selection file.
Build the daemon from the repository root with:

```sh
go build -tags e2e_realclaude -o <temporary-path>/pyry ./cmd/pyry
```

Before starting that binary, set `PYRY_E2E_CLAUDE_BIN_FILE` in its environment
to a driver-owned regular file in a private directory. Write the file with mode
`0600` and one absolute executable path, optionally surrounded by whitespace;
the whole file is limited to 4096 bytes. Select a failing executable initially,
then release by selecting the real Claude executable. For each update, write a
temporary file in the same directory and atomically rename it over the selection
file; writing in place can expose partial input to a concurrent spawn. Missing,
unreadable, non-regular, oversized or malformed activated input fails that spawn
through the existing backoff path rather than falling back to real Claude.

Selection applies to every streamsup Runner in that daemon. `spawnClaudeBin`
reads one bounded snapshot outside Runner locks for each spawn; a replacement
affects a future spawn and sends no signal or restart request. It leaves a live
child, backoff, Runner and bound session alone. Unset activation preserves
`Config.ClaudeBin`, and selection preserves existing argv, environment and working
directory composition. Ordinary builds exclude the parser and ignore activation
entirely; there is no relay, control-wire or conversation-triggered entry point.
`TestInteractiveSessionErrorRecovery` builds its own tagged daemon and passes it
to `spawnBootstrapDaemonBinary`, ignoring `PYRY_E2E_BIN` locally. Other
`spawnBootstrapDaemon` callers retain `ensurePyryBuilt`'s untagged/prebuilt
selection. See [the spawn snapshot contract](streamsup-package-supervise-loop-run.md)
and [the design](../../specs/architecture/2859-session-error-failure-control.md).

`session.child_crashing` is conversation-scoped and signals repeated fast exits
without discarding backlog. Release before queue give-up lets the same Runner
respawn real Claude and deliver that backlog without a client resend.
`session.blocked` marks queue
give-up and dropped backlog: release afterward requires a fresh message and
never replays the dropped message. Both recovery arms retain the daemon and
conversation's bound session. This is the external-driver contract consumed by
[mobile #1731](https://github.com/pyrycode/pyrycode-mobile/issues/1731).

A failing child's open, unread stdin pipe can accept a queued turn and make the
queue report delivery before the child exits. Close stdin before writing a
child-owned readiness file, and observe that file before enqueueing. Gate the
initial exit through phone handshake and enqueue so the one-shot crash notice
remains observable; later launches exit immediately. Child stderr is captured
until exit and cannot establish this pre-enqueue boundary. Recovery proof also
needs delivered message IDs, reply text followed by idle, and prompt markers in
the completed Claude transcript: model reply wording alone cannot prove which
prompt reached Claude or that dropped backlog stayed absent.

Harness constants are not interchangeable merely because they have the same
UUID shape. `startStreamModalResolutionHarness` seeds
`streamModalBootstrapUUID`; addressing `liveModalBootstrapUUID` instead made
\#2281's first live model-rejection test stop at `session.not_found`, before the
vocabulary gate it claimed to exercise. A test for a downstream refusal must use
the identifier returned or documented by the fixture that seeded the pool, and
assert the expected error code rather than accepting any error envelope.

Permission protocol behavior is Claude-version dependent. The captured 2.1.143
spike saw `--permission-prompt-tool stdio` silently bypass the gate, while
`TestRealClaude_StdioPermissionPromptDeny` proves that 2.1.259 emits
`control_request/can_use_tool`, accepts a correlated deny response, completes the
turn, and does not execute the denied command. A useful compatibility test must
therefore keep stdin open, assert the request and response correlation, and use a
side-effect witness; a successful process exit alone cannot distinguish a denial
from a silent bypass. See [the permission protocol findings](permission-protocol-spike.md).

A live gate's timeout-sized budget constant should carry the reason it must stay
well under the production timeout it is proving early, not just reuse an existing
budget. `TestInteractiveStreamStdioCancelDeniesParkedPermission`'s `turn_end` drain
needed to complete in seconds, not the ten-minute `mcpApprovalTimeout` a
parked-completer bug would still (eventually) satisfy; reusing the suite's general
per-turn reply budget would have stayed non-vacuous only by a factor of five, with
nothing in the file explaining why that margin was enough. A dedicated named
constant states the margin against the specific timeout under test, so a later
widening reads as the regression it would be (#2416).

A live permission-denial prompt must tell Claude not to retry, not to use another
tool, and to give a short reply after denial. A prompt that describes only
successful completion leaves the model free to retry: an unanswered retry modal
can mimic a parked-completer hang (#2416), and even successfully rejected retries
can exhaust a bounded drain. In the [#2851 evidence](https://github.com/pyrycode/pyrycode/issues/2851#issuecomment-6000600746),
the first remote denial resolved at 18:13:11.585Z on 2026-10-05, four retries were
rejected, and the fifth exceeded the cap at 18:13:25.599Z; a same-tree rerun reached
idle with zero retries at 18:22:51.071Z. This supports missing denial guidance and
variable model retries; no external defect is established by these logs.

Keep the instruction local to `driveInteractiveStreamPermissionDeny`, preserving
`writeFileTrigger` for allow tests. Prompt guidance supplements the independent
proof: `raiseRealPermissionModal` requires a genuine permission modal; its
matching dismissal must have `source=remote` and `outcome=reject_once`;
`denyModalsUntilIdle` rejects retries within the four-retry cap and
`perTurnReplyBudget`; and `requireTriggerFileAbsent` walks the workspace after
terminal idle. File absence and idle alone could also pass after a timeout denial
or a dropped remote answer, so attribution remains essential.

`TestInteractiveStreamPermissionDenyAfterSettingsRespawn` additionally waits for
`session_settings_updated` before killing the child, then `restartLiveChild`
requires a running successor with a different PID before the denial proof. These
checks prevent racing the settings installation or testing the original child.

`TestInteractiveStreamStdioAlwaysAllowIsSessionScoped` proves that two identical
Bash commands execute after one approval in a session, then requires a fresh
session to ask before execution. A live failure came from a mixed offer:
Claude 2.1.259 supplied a command rule alongside directory and mode alternatives.
The daemon now omits those alternatives while validating every retained command
rule. The checked source is kept in the permbridge testdata, with exact session-only
response coverage. The live test remains mandatory within this tagged suite.

Optional permission fields need a raw-presence observation before ordinary JSON
decoding. A plain string target collapses an absent key, a present empty string,
and JSON `null` to the same Go zero value, so a green assertion on `""` cannot
prove that Claude omitted the key. The live permission-context proof records key
presence and raw value upstream of daemon decoding, then compares that source
with both the initial and reconnect-reconciled `modal_shown`. This matters in
practice: Claude 2.1.259 omitted both reason fields for an ordinary Bash write,
while an outside-working-directory Write supplied `workingDir` plus non-empty
reason text.

A live proof of an effective default should derive its explicit comparison arm
from the installed binary, not from the daemon's current vocabulary. The applied
settings proof selects an untruncated effort level from that run's initialize
menu, asks two clean children for `get_settings` before any user turn, and checks
the production result against an independent raw-stdout decoder. Its inherited
arm never asserts `high` or another literal: a hard-coded default would turn an
upstream policy change into a false regression. When the raw observer wraps the
production parser in `io.MultiWriter`, the runner must also receive that exact
parser through `streamsup.Config.ControlParser`; relying on direct-writer type
inference makes the query return unavailable without sending the request.

The outside-directory gate applies to Read, not just Write, and only under the
posture a daemon session actually runs in. `TestInteractiveStreamDefaultPostureOutsideWorkspaceRead`
measured whether a live claude, walked back to the `default` permission mode
in band (the daemon always launches with `--dangerously-skip-permissions` and
then writes the running posture via `set_permission_mode`, per
`claudeSettingsArgs` and `SpawnPermissionMode`), can read an absolute path
outside its workspace without a modal. Under claude 2.1.259, it prompts: the
Read raised a permission modal with `reason_type: workingDir`, the same
reason and the same gate as the sibling Write, measured 2026-09-15. The
posture matters as much as the permission class — see the corrected #2039
finding in
[interactive_stream_attachment_read_test.go](e2e-realclaude-interactive-stream-attachment-read-test-go.md#lessons-that-outlive-this-ticket),
which read the opposite way only because it measured a bypassed child.

An enforcement test must correlate a permission modal to the tool call it is
meant to prove. The first `TestInteractiveStreamSessionSettingsReportsConfirmedPermissionMode`
accepted a `Read` sighting and any modal from the same turn; it could therefore
pass if `Read` ran ungated while another tool prompted. When a harness exposes
only the turn's distinct tool names, require that set to be exactly `Read`
before treating the modal as the outside-workspace Read gate. A prompt telling
the model not to use another tool is guidance, not evidence of what ran.

Live delegation turns have an extra, valid envelope before the forwarded child
response: claude emits the delegated prompt as a `user` text block, which the
stream parser intentionally surfaces as `unrecognized_message` with
`site=user_block`. A proof concerned with child prose lanes must still decrypt
every Noise frame in order, classify and ignore that known unrelated envelope,
then keep strict assertions for malformed payloads and unexpected parent lanes.
Treating every non-target envelope as a lane failure rejects valid turns; skipping
its decryption desynchronizes the receive nonce.

Optional event waits must not expire the fakephone read context: coder/websocket
closes the connection when that read times out, so a missing event can make the
next turn's send fail with a closed connection. `startSuggestReader` gives one
goroutine ownership of reads and Noise receive nonces; `suggestWatch.pumpUntil`
times out on its channel instead, preserving the connection between turns.
Reader errors are returned to the test goroutine for failure reporting.

Native suggestions need useful staging rather than an assumption that every
successful turn emits one. On Claude 2.1.280, six prompts ending in open
questions produced none; small coding steps with an obvious follow-up did.
Those steps still need explicit enable at `allowed_warning`: Claude's independent
usage-warning guard suppresses generation even with `--prompt-suggestions`.
`installSuggestCLI` sets `CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=1` for the native
persistent stream child, overriding an inherited disable. In the
[#2856 source evidence](https://github.com/pyrycode/pyrycode/pull/2857#issuecomment-6009642509),
the pre-fix child completed six results with `allowed_warning` and no nonempty
source suggestion; explicit enable restored a native event and wire set/clear
within the existing limits. This was absent Claude generation, before parser
acceptance or daemon publication; longer waits or more turns would not fix it.
`TestInteractiveStream_NativeReplySuggestionSetThenClear` drives up to six
completed turns with a 30-second suggestion window after each. It requires a
nonempty native source suggestion from one persistent child and a nonempty
`reply_suggestion` for `suggestConvID`, then an explicit `suggested_reply: null`
at a higher revision after an accepted follow-up send. Missing native output
still fails with credentials present. Short conversations and cold caches can
stay silent. A green turn or a skipped probe cannot prove
[native suggestion publication](streamsup-package-draining-turnevents-into-the-interactive-emitter.md#native-reply-suggestions-after-the-result-2831).

Observe native stdout before parsing to locate a missing set.
`installSuggestCLI` forwards bytes unchanged while recording metadata in a
private temporary file: stream-child and result counts, all native suggestion
event counts, nonempty suggestion counts and total suggestion bytes, and `allowed_warning`
counts. `readSuggestSource` reports these after each bounded wait; generated text,
stderr, environment values and credentials are never retained. Separate event
and nonempty counts distinguish absent events from empty source output. A
nonempty source suggestion without a wire set points investigation at
`Parser.emitPromptSuggestion` and `replySuggestions` eligibility/publication.
`TestSuggestCLIProducerIsolation` and `TestSuggestCLISourceEvidence` check enable,
producer isolation, byte preservation and metadata redaction with a stand-in
that never participates in the live proof.

**A suggestion frame alone cannot prove its source.** `installSuggestCLI`
refuses non-stream native-probe launches. Fallback disables native generation and
removes `--prompt-suggestions` only on the persistent child. One exchange gets
15 seconds: two for native output, then ten for production fallback (9.8-second
internal deadline plus termination). Require a UTF-8 single-line nonempty reply
for `suggestConvID`, ≤240 characters/1024 bytes, then `suggested_reply: null` at a
higher revision after accepted input. Authenticated absence is FAIL. Both tests
run under `make e2e-realclaude` and
[passed a counted gate](https://github.com/pyrycode/pyrycode/issues/2856#issuecomment-6009817217).

The fresh print-mode helper uses `--output-format stream-json --verbose`.
Wrapper and daemon parse independently while the wrapper forwards exact stdout,
inherits stdin/stderr and stays in the daemon group. Each retains at most 4096
bytes per envelope, discarding oversized input through newline. Only complete
UTF-8 JSON `type=result` envelopes supply replies; progress, unknown, malformed
or oversized frames cannot spend a later result's allowance. A complete final
envelope without newline is examined at EOF by the wrapper and after Wait
receipt by the daemon. Partial input cannot invent progress or results.

0600 metadata correlates wrapper/child PIDs and retains successful spawn, first
nonempty read and first acknowledged write/flush, even without completion.
Spawn proves creation, never readiness or authentication. Read/forward counts
are 1–4097 prefixes, saturated at 4097; completed receipt may be zero.
`stdout_bytes` counts whole-stream receipt, independently of `result_bytes`
(1–4096). Saturation cannot invalidate a later bounded result or prove total
output, EOF, daemon receipt, timely arrival, child exit or publication.

`readSuggestSource` reads ≤64 KiB. Interrupted final appends preserve prior
witnesses. Metadata needs unique, present, typed fields, stage order, positive
matching PIDs and consistent prefix/completion counts. Duplicate, malformed,
out-of-order or mismatched records invalidate ambiguous evidence. Completion
needs elapsed time, exit, count and predicates; zero may complete after spawn
without read/forward. Only valid completion sets wrapper `output_observed`;
otherwise exit/output stay unknown. Incomplete elapsed time is since invocation.
Result fields use Go struct null/duplicate semantics, unlike metadata: null
preserves scalars; a later valid duplicate cannot erase an earlier type error.
The Python decoder mirrors Go field folding, whitespace and surrogate handling.

`replyFallback.run` logs `reply_fallback.lifecycle`: wrapper PID, attempt/child
times, parent/fallback cancellation/deadline, `own_deadline_elapsed`, atomic
`group_cancel_requested`, Wait and exit observations. Sample contexts before
cleanup cancellation. **Daemon progress and completion are separate.** Under
one writer mutex, the first `cmd.Cancel` freezes last decoded event, event age,
init/source and retry count before signaling, for both the context watcher and
explicit cancellation. Repeated cancellation and later input cannot populate or
update `cancel_`; `progress_` may advance, including after Wait. Frozen fields
apply only with `cancel_snapshot_observed=true`.

Only decoded `system/init`, `system/api_retry` and `result` advance progress.
`event=none` means no recognized event at this reader; its age is unknown.
Retries count decoded retry events only: observed zero differs from absent or
invalid metadata, which stays unknown; never infer from `attempt`/`max_retries`.
Reported `apiKeySource` is `none`, `ANTHROPIC_API_KEY` or `unknown`; absent,
invalid or other values become unknown. Init/source prove neither authentication
nor readiness, and retry count proves only observed retries.

`replyFallbackOutput` requires buffered Wait receipt before process-state and
completion-dependent output/exit/Wait predicates; independently synchronized
progress can remain known without Wait. Keep cancellation's actual Wait error.
After receipt, `wait_ok` means nil error and `wait_delay` means `exec.ErrWaitDelay`.
Zero retained bytes is valid UTF-8 with no decoded result. `stdout_json_ok`
means a bounded decoded result, not a valid whole stream; decoded result-success
and trimmed-text predicates stay separate from Wait success and receipt
saturation. `decodeReplyFallback` and `validReplyFallback` match production.

`suggestLifecycle` selects complete daemon lines by wrapper PID, validates
presence/applicability and consistent count/cap, result-size and Wait/exit
predicates. `suggestSource.diagnostic` applies the wrapper gates. Missing or
invalid evidence stays unknown. `suggestProgressFields` also rejects impossible
combinations (`none` with init/positive retries, `init` with init false, retry
with zero): conflicting event/age and participating fields become unknown,
while independent init/source or retries stay known. Allowlists alone miss these
contradictions. Wrapper exit is not child exit; a valid result without a wire set
establishes neither forwarding loss nor publication eligibility. Simultaneous
contexts identify no cause. Logs/evidence contain only fixed labels, validated
source categories, booleans, counts/PIDs and times, never prompt/reply text, raw
stdout/stderr/errors, credentials, account identifiers or environment values.

Offline `TestReplyFallbackStream`/`TestReplyFallbackStreamFreeze` and
`TestSuggestCLIFallbackEvidence` cover chunking, skipped frames, saturation then
result, privacy and immutable cancellation. `TestReplyFallbackOutputUnknownWait`
pins independent progress and unknown completion; cancellation need not reap
within its grace. `TestSuggestLifecycleOutput` and
`TestSuggestSourceDiagnosticApplicability` pin predicate applicability; use
nonzero counts for missing-result checks so zero rejection cannot mask a broken
gate. `TestSuggestStreamProgressGates` preserves observed zero and independent
fields under both prefixes. `TestSuggestCLIFallbackIncomplete` cancels at spawn,
blocked-flush read and acknowledged forwarding; readiness alone missed a race,
so wait for witnesses/`lockedBuffer` bytes and inspect I/O after Wait.
`TestSuggestSourceCapturePresence` rejects invented observations.

Historical evidence: [#2873](../../specs/architecture/2873-fallback-source-evidence.md),
[PR #2892](https://github.com/pyrycode/pyrycode/pull/2892).
Counts: executed/passed/failed/skipped.

| Targeted batch commit | E/P/F/S |
| --- | --- |
| `a35e9b89` | 3/2/1/0 |
| `16960306` | 3/2/1/0 |
| `aee249fc` | 3/2/1/0 |
| `ec231249` (daemon lifecycle instrumentation) | 3/3/0/0 |
| `d416eff6` (#2873 declared six-run batch) | 6/5/1/0 |
| All recorded targeted #2873 PR runs | 18/14/4/0 |
| Separate exact-base report, `1b5159d8a0` | 2/1/1/0 |
| [Earlier #2882 batch][fallback-history] | 6/4/2/0 |
| [PR #2896 batch](https://github.com/pyrycode/pyrycode/pull/2896) | 6/3/3/0 |
| [#3024 declared batch](../../specs/architecture/3024-fallback-stream-observation.md#six-run-results), `28974c1c` | 6/2/4/0 |

**Internal-deadline/live-parent absence remains unresolved.** #2873 run 4
remains FAIL: fallback/own deadline, neither parent flag, wrapper Wait/SIGKILL;
completion, child exit/output and cause unknown. Earlier #2882 runs
2/5 and PR #2896 runs **1, 3 and 6 remain FAIL**. The latter witnessed spawn,
unknown read/forward, independently zero daemon bytes after wrapper Wait/SIGKILL,
UTF-8 true, JSON false, unknown result/text, no set/clear; cause unknown.
The [historical spec][fallback-history] retains all outcomes.
\#3024 runs **1, 2, 4 and 6 remain FAIL**: run 1 froze a bounded valid result
before deadline cancellation/unsuccessful Wait with no publication; runs 2/4/6
froze init-only progress, zero observed retries and no decoded result. The
[operator disposition](https://github.com/pyrycode/pyrycode/issues/3024#issuecomment-6077408952)
accepts the batch as observation evidence feeding [#2923](https://github.com/pyrycode/pyrycode/issues/2923),
without correction or a cause claim. Passes with saturated receipt and bounded
results do not resolve these or historical failures. An all-pass batch means
non-reproduction only. The separate dispatcher gate counted 1925/1925/0/27 on
`b27e23ee` with main `c7bc2ed4`; it does not replace the retained six-run batch.
The [#3022 snapshot contract](../../specs/architecture/3022-post-wait-stdout-validation.md)
and [wrapper witness contract](../../specs/architecture/3023-fallback-wrapper-witnesses.md)
preserve execution, deadlines, cancellation, account/isolation, policy, staging and
[release control](https://github.com/pyrycode/pyrycode/issues/2859).

The [#2881 full gate](https://github.com/pyrycode/pyrycode/issues/2881#issuecomment-6017651019)
at `81c7ac78f9` counted 1635/1635/0/27. Passing cannot recover witnesses or resolve
historical failures. Compare repeated counted runs with matching inputs; see
[development verification](development-verification.md#test-execution-and-artifact-survival).
Non-reproduction proves no fix; timeout proves no defect; evidence waives no failed gate.

[fallback-history]: https://github.com/pyrycode/pyrycode/blob/25d778fbd0e54753bd82a6d4eca9a097c996f6f4/docs/specs/architecture/2882-fallback-stdout-evidence.md

Since #2569, a fresh-home daemon now seeds a promoted `General` channel and a
bound-but-never-spawned session on first boot (see
[`conversations-registry.md`](conversations-registry.md)). `spawnBootstrapDaemon`
in `harness_daemon_test.go` has no counterpart to the fake-daemon harness's
`premarkWorkspaceSeeded` (see [`e2e-harness.md`](e2e-harness.md)), so every live
daemon this suite spawns on a fresh home will seed too. No test here asserts a
session or conversation count today, so nothing is known to be broken — but a
live-suite failure that looks like a stray extra session or conversation should
start here, not be chased as a daemon regression.

Composer Stop survival needs a witness tied to the actual rig task. An interrupt
ack or retained roster can stay green after the task has died.
`TestRealClaudeComposerStopPreservesBackgroundTask` uses `stopHeldTaskSetup` to
join the exact background Bash call to its task id, waits for an active
foreground call's FIFO rendezvous, then requires a cancelled turn. Only that
same background task's completion after rig release proves survival; reader
presence immediately after Stop is an additional liveness check. See
[task identity and FIFO staging](e2e-realclaude-roster-after-finish-capture-test-go.md#stop-completion-needs-a-held-task-and-all-terminal-signals)
and [Composer Stop](streamsup-package.md#composer-stop).

Closed-input cleanup needs independent attribution. Parent cancellation invokes
the descendant reaper and could make a task-death assertion pass even if Claude
failed to clean up. `TestRealClaudeComposerStopClosedInputKillsHeldTask` closes
stdin while the foreground result is held, checks the background FIFO reader
still exists, then releases only the foreground file gate. It requires normal
Claude exit with an unexpired context and background-reader disappearance while
the background writer remains held, all before parent cancellation. This
excludes both parent teardown and rig EOF as causes. Both standing tests run
under normal `make e2e-realclaude`, log the Claude version, and passed on Claude
2.1.280 in [the dispatcher live gate](https://github.com/pyrycode/pyrycode/issues/2775#issuecomment-6008885859).

Shortened waits. A live test that has to outlast a daemon timer should shorten
the timer, not wait it out. A daemon built with `go build -tags e2e_realclaude`
reads `PYRY_E2E_QUEUE_GIVE_UP_AFTER`, a positive Go duration such as `3s`, as the
message queue's give-up bound in place of its 2 minute default. An ordinary
build, including the untagged binary `ensurePyryBuilt` produces, ignores it, so
a test using it must build its own tagged daemon. An invalid value fails the
daemon's start. `TestInteractiveSessionErrorRecovery/dropped` sets
`PYRY_E2E_QUEUE_GIVE_UP_AFTER=3s` before starting its isolated daemon, observes
`session.child_crashing`, `session.blocked` and empty backlog with bounded
30-second waits, then releases failure and observes an automatically running
child with `liveChildPID` before enqueueing a fresh message. Enqueueing during
backoff could spend the fresh message's shortened give-up window before Claude
returns. Blocked/empty can precede the crash notice at this duration, so the
dropped arm requires all observations before release without imposing their
order. The retained arm clears inherited override input and keeps the default
two-minute window, requiring undelivered backlog at the crash notice and release
before give-up. Subtest `t.Setenv` cleanup restores the environment after daemon
teardown, preventing leakage into the retained arm or other tests.
`TestInteractiveStreamResumeAfterEviction` runs a 5 s idle
window because, since #1486, a fire during an open turn re-arms rather than
evicting; it counts only evictions logged after the plant send, so a fire before
the plant turn cannot make the resume vacuous.

Parallel tests. `WithWorktree` and `WithWorktreeAuthenticated` pin HOME with
`t.Setenv`, which Go refuses to combine with `t.Parallel`. A live test may run in
parallel only through `runParallel(t)`, and only when it changes no process-wide
state: no `t.Setenv` or `os.Setenv` of its own or in a harness callback, and every
child that needs the isolated HOME receives it explicitly through `homeEnv`, or
`Env` on an in-process runner. `liveHome` returns `authenticatedHome` for such a
test and falls back to `WithWorktreeAuthenticated` for every other caller, so
serial tests keep their pinned HOME. Tests that read transcripts in process
through `ReadJSONL`, install a PATH or environment shim, or count processes stay
serial. Go runs every serial test first and the parallel ones together after,
so the serial tail sets the floor on wall time.
