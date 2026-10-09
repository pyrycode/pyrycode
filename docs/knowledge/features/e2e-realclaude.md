# `internal/e2e/realclaude` — real-`claude`-binary integration suite

Sibling Go package to [`internal/e2e`](e2e-harness.md), gated by a distinct build tag so the real-`claude` trust-boundary suite is opt-in and never runs under `make test` / `make check`.

## Why a sibling, not part of `internal/e2e`

`internal/e2e` carries `//go:build e2e || e2e_install` and drives `pyry` against a fake-claude (`TestHelperProcess` or shell wrapper). That harness deliberately stops at the trust boundary with the real `claude` binary — useful for control-plane / supervisor coverage, but it can't catch the `/doctor` prompt-poisoning class of bug that broke Phase C on 2026-05-14.

`internal/e2e/realclaude` is the package where tests DO cross that boundary. Keeping it separate means:

- `make test` skips it via tag exclusion alone (no path filter).
- A future `make e2e` that picks up `e2e` / `e2e_install` won't accidentally pull real-claude tests in.
- Each suite's tag set documents its intent at the file header.

## Build tag

All files in the directory carry exactly:

```go
//go:build e2e_realclaude
```

Single tag, no alternation. The `e2e_install` precedent established the `e2e_<purpose>` naming.

## Source-citation checks

`TestSourceCitationPackage` discovers every direct Go source file and checks
parsed line/block comments and decoded string literals for numeric Go-source
citations. Use symbol references and enclosing-symbol descriptions instead,
following [the citation convention](../../../CODING-STYLE.md#comments--citing-other-code).
The check rejects filename, bare and camel-case symbol citations without
resolving targets; missing files and reversed ranges still fail. Findings name
the citing file, source position and offending text.

Run the check and its regressions from the repository root:

```sh
go test -tags e2e_realclaude -race -count=1 -v -run '^TestSourceCitation' ./internal/e2e/realclaude
```

These tests run without Claude, credentials, a daemon or network. `make check`
does not run this tagged check; the full tagged suite includes live tests.
Colon matching needs positive controls for Go slices, JSON fragments and listen
addresses, which occur in probe text but are ordinary data.

## What's there today

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

The print-mode JSON wrapper forwards exact stdout, inheriting stdin/stderr and
the daemon group. Its 0600 metadata retains correlated wrapper/child PIDs:
successful spawn, first nonempty read, first acknowledged write/successful flush.
Witnesses survive cancellation without completion.
Spawn proves creation, never readiness or authentication. Read/forward counts are
prefixes from 1–4097, saturated exactly at 4097; they prove neither final totals,
pre-deadline arrival, daemon receipt, EOF, child exit nor publication eligibility.

`readSuggestSource` reads ≤64 KiB. Partial final appends preserve prior witnesses;
missing files/fields mean unknown, never zero. Struct decoding accepts duplicate
keys and defaults missing/null scalars to zero: require unique, present, typed
metadata, stage order and matching positive wrapper/child PIDs. Duplicate,
malformed, out-of-order, PID-mismatched or cap-inconsistent records invalidate
progress/completion as ambiguous. Completion needs elapsed time, exit, count and
predicates consistent with prefixes; zero may complete after spawn without
read/forward. Only valid completion sets `output_observed`; otherwise exit/output
stay unknown. Incomplete elapsed time is since invocation, not child duration.

`replyFallback.run` logs `reply_fallback.lifecycle`: wrapper PID, attempt/child
times, parent/fallback cancellation/deadline, `own_deadline_elapsed`, atomic
`group_cancel_requested`, `wait_completed`, wrapper exit code/signal. Snapshot
contexts before cleanup cancellation, which would misclassify success.

**Daemon receipt is a post-Wait snapshot.** `replyFallbackOutput` reads stdout
after buffered Wait receipt, retaining cancellation's received Wait error.
Without receipt, buffer/process state are unread; output/exit/Wait stay unknown.
With receipt, `output_observed=true`;
`stdout_bytes` is 0–4097 (4096-byte allowance plus sentinel), with
`stdout_cap_exceeded` true exactly at 4097. Saturation is a lower bound, never
total output or EOF. `wait_ok` means nil Wait error; `wait_delay` means
`exec.ErrWaitDelay`. Explicit zero means no daemon-retained bytes, not no child
output; empty bytes are valid UTF-8 and fail JSON decoding.

`decodeReplyFallback` uses production Go struct decoding, including null/duplicate
fields. `stdout_json_ok` requires valid UTF-8; otherwise decoding stays unknown.
Decoded output independently permits `stdout_result_ok` (`is_error`/`subtype`)
and `stdout_text_ok` (trimmed `result` through `validReplyFallback`), regardless
of Wait success or overflow. `suggestSource.diagnostic` uses the same gates.

`suggestLifecycle` requires complete daemon lines, matching wrapper PID and
present typed consistent scalars. Missing/partial/malformed evidence or
contradictory count/cap or Wait/exit leaves output unknown. Zero claiming
invalid UTF-8 or successful decoding invalidates output, preserving independent
Wait observations. Missing/inapplicable result/text predicates stay unknown.
Wrapper exit cannot prove child exit; the snapshot proves neither pre-deadline
arrival, child completion nor publication eligibility. Simultaneous contexts stay
ambiguous; timing/cancellation identifies no cause. Records/diagnostics contain
only fixed labels, booleans, validated counts/PIDs and times, never prompt/reply
text, stdout/stderr/errors, argv, paths, credentials, account or environment values.

Offline tests cover exits/deadlines, I/O, privacy, invalid captures and saturation.
`TestReplyFallbackOutputUnknownWait` uses an inaccessible buffer:
cancellation need not reap within its grace. `TestSuggestLifecycleOutput` pins
genuine zero; missing-result tests need nonzero counts so zero rejection cannot
hide a broken gate. `TestSuggestSourceDiagnosticApplicability` pins source gates.
`TestSuggestCLIFallbackIncomplete` cancels at spawn, blocked-flush read and
acknowledged forwarding; completion stays unknown. Readiness missed a forwarding
race: wait for witnesses/`lockedBuffer` bytes, inspect I/O after Wait.
`TestSuggestSourceCapturePresence` rejects invented observations. Python must
mirror Go whitespace, null/duplicate and case-insensitive fields: dictionary
conversion loses type errors; null preserves scalars.

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

**Internal-deadline/live-parent absence remains unresolved.** #2873 run 4
remains FAIL: fallback/own deadline, neither parent flag, wrapper Wait/SIGKILL;
completion, child exit/output and cause unknown. Earlier #2882 runs
2/5 and PR #2896 runs **1, 3 and 6 remain FAIL**. The latter witnessed spawn,
unknown read/forward, independently zero daemon bytes after wrapper Wait/SIGKILL,
UTF-8 true, JSON false, unknown result/text, no set/clear; cause unknown.
The [historical spec][fallback-history] retains all outcomes.
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

## Make target

```make
.PHONY: e2e-realclaude
e2e-realclaude:
	$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...
```

No `-race`. These are I/O-bound trust-boundary checks, not goroutine-stress tests; flip on `-race` per-test when a future test in the directory does spin goroutines.

`make check` is unchanged. CI's per-PR `make check` does not run this suite — it stays opt-in for that path.

## CI cadence: code-review phase, no nightly workflow

The real-`claude` suite is NOT wired into GitHub Actions. It runs **locally
during the code-review phase** of every dispatched ticket via the pipeline
— see the code-review agent's `CLAUDE.md` for the invocation contract.

The earlier nightly workflow (`.github/workflows/e2e-realclaude-nightly.yml`, #362) was removed in #379 the same day it landed. CI-side rationale for the
removal:

- GitHub Actions would need an `ANTHROPIC_API_KEY` repo secret; Max-plan
  tokens used locally are free.
- Per-run cost ($0.10–$0.50, scaling with test count) buys nothing local
  runs don't already cover once code-review runs the suite on every PR.
- Failure surface synchronised to dispatch cadence beats unpredictable
  04:00 UTC failures.
- One fewer CI file to keep in lockstep with `self-check-daily.yml`.

The make target is unchanged — `make e2e-realclaude` is still the entry
point, just no longer invoked by CI.

## Verifying tag exclusion

After landing, `make test 2>&1 | grep realclaude` should be empty (or only an `ok ... [no test files]` line) — files with an unsatisfied build tag are dropped at the build stage, so the package compiles to an empty test binary.

## Related


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Operator authentication](e2e-realclaude-operator-authentication.md) — `make e2e-realclaude` only exercises the trust-boundary tests if the spawned `claude` subprocess can reach Anthropic's API. 
- [smoke_test.go](e2e-realclaude-smoke-test-go.md) — The composition pattern downstream tests use: `WithWorktree` → `RunPyryAgentRun` → `ReadJSONL`. 
- [allowed_tools_enforcement_test.go](e2e-realclaude-allowed-tools-enforcement-test-go.md) — see the document
- [resilience_test.go](e2e-realclaude-resilience-test-go.md) — see the document
- [sigterm_mid_tool_use_test.go](e2e-realclaude-sigterm-mid-tool-use-test-go.md) — see the document
- [budget_test.go](e2e-realclaude-budget-test-go.md) — consumer** (the only file in the suite that isn't): it drives the **daemon's interactive relay path**, not `pyry agent-run`. 
- [interactive_session_control_liveness_test.go](e2e-realclaude-interactive-session-control-liveness-test-go.md) — coverage of the **session-control respawn verbs**, and the last of the #963 families: `new_session` (rotate via `/clear`) and…
- [interactive_stream_resume_after_eviction_test.go](e2e-realclaude-interactive-stream-resume-after-eviction-tes.md) — real-`claude` proof that an idle-evicted **stream** session resumes via `--resume` with prior context intact, closing the last uncovered…
- [background_reach_probe_test.go](e2e-realclaude-background-reach-probe-test-go.md) — gate**; opt-in behind `PYRY_PROBE_BACKGROUND_REACH=1`, reusing #1223's staging rig verbatim (`background_trigger_probe_test.go` not edited;…
- [teardown_liveness_probe_test.go](e2e-realclaude-teardown-liveness-probe-test-go.md) — opt-in behind `PYRY_PROBE_TEARDOWN_LIVENESS=1` on top of the package's normal auth skip. 
- [trailer_admissibility_test.go](e2e-realclaude-trailer-admissibility-test-go.md) — probe**; two pure predicates that decide whether #1266's trailer scan and
- [trail_run_outcome_test.go](e2e-realclaude-trail-run-outcome-test-go.md) — `trailClassifyRun(trailRunReadings) trailRunOutcome` maps one probe run's raw observations onto exactly one of sixteen outcomes (four…
- [finding_staging_fill_test.go](e2e-realclaude-finding-staging-fill-test-go.md) — run's own transcript.** `finOutcomeStagingGate` (#1284, above) decides all seven staging outcomes from synthetic inputs; this file fills…
- [trailer_terminal_reason_test.go](e2e-realclaude-trailer-terminal-reason-test-go.md) — probe**; consumes #1357's `KeyNames` reading and answers a question neither it nor the decoded scalar can answer alone: what a trailer's…
- [finding_run_gather_test.go](e2e-realclaude-finding-run-gather-test-go.md) — decode #1313, published record's bound proven to be the classified sighting's #1316) — **parameterises `trailRigGather` (#1268) on the two…
- [finding_stage_held_group_test.go](e2e-realclaude-finding-stage-held-group-test-go.md) — (#1281) two parameters from a real held command, not hand-passed integers.** `finStageHeldGroup` stages `sh -c '"$1" "$2"; exit 0'` over a…
- [finding_live_run_test.go](e2e-realclaude-finding-live-run-test-go.md) — staging driver, not a probe of pyry itself**; `finLiveRunStage(t, envDelta) *finLiveRunHandle` spawns pyry on the runner path its caller's…
- [finding_stream_exit_path_probe_test.go](e2e-realclaude-finding-stream-exit-path-probe-test-go.md) — `PYRY_USE_STREAMJSON=1` structural sibling of #1337, and only that path**: the two runners do not write the same trailer and do not present…
- [interactive_stream_inband_model_test.go](e2e-realclaude-interactive-stream-inband-model-test-go.md) — live proof that `Pool.UpdateSettings` sends `set_model`, observes its matching success response, and reports the resolved model on the next application turn without a respawn.
- [inband_bypass_revoke_arms_test.go](e2e-realclaude-inband-bypass-revoke-arms-test-go.md) — half of #1643's three-arm substrate: which stored posture each arm launches with.** #1622's `seedBypassRegistry` wrote `yolo:true`…
- [interactive_stream_model_announced_test.go](e2e-realclaude-interactive-stream-model-announced-test-go.md) — real claude's announced model reaches the daemon's **own emitted frame**, not just claude's stdout. 
- [interactive_stream_family_alias_test.go](e2e-realclaude-interactive-stream-family-alias-test-go.md): `TestInteractiveStreamPinnedModelFollowsItsFamily`, the family rule's live arm. Asserts the live menu is one row per family, sends an older pinned id derived from a family row, and asserts the `model_announced` frame after the pick names that family row's own `resolved_model`, sourced from the same run's menu, never a literal.
- [interactive_stream_announced_reset_test.go](e2e-realclaude-interactive-stream-announced-reset-test-go.md) — `TestInteractiveStreamClearRunsDaemonReset` (#2485), the real-claude proof for the daemon-run `/clear` reset: since #2456 a client's `/clear` never reaches claude, and this asserts the three `resetting` edges, the `clear` transition, and that the handoff note composed during rotation actually reaches the successor's first reply.
- [interactive_stream_clear_wrapup_skipped_test.go](e2e-realclaude-interactive-stream-clear-wrapup-skipped-test-go.md) — `TestInteractiveStreamClearWrapUpSkipped` (#2486), the inverse of the case above: spawns the daemon with `-pyry-wrapup-deadline=1ms` and proves a wrap-up that cannot finish still completes the reset as `handoff: skipped` rather than failing it, with the conversation's seeded note left byte-identical.
- [interactive_stream_hook_blocked_banner_test.go](e2e-realclaude-interactive-stream-hook-blocked-banner-test-go.md) — the real-claude proof that a `UserPromptSubmit` hook's block reason reaches a connected client as a `banner`, with the daemon in the path, opening and closing no turn on the wire, and that the session still answers the next unblocked prompt.
- [initialize_control_names_test.go](e2e-realclaude-initialize-control-names-test-go.md) — fourth fixture-name lock in this package. 
- [initialize_control_record_test.go](e2e-realclaude-initialize-control-record-test-go.md) — **the record half of the `initialize` fixture family: fixes the JSON contract #1688's live capture, #1690's decoder and #1692's fake all…
- [initialize_control_window_test.go](e2e-realclaude-initialize-control-window-test-go.md) — send-point window #1723 defined and left unpopulated: a pure, total function over `[]json.RawMessage` and one anchor, returning the…
- [initialize_control_probe_test.go](e2e-realclaude-initialize-control-probe-test-go.md) — **the live run family for the `initialize` fixtures: one real child, one tool-free probe turn, one `control_request` with subtype…
- [initialize_control_redaction_test.go](e2e-realclaude-initialize-control-redaction-test-go.md) — the `initialize` fixture family, proved over raw bytes with no record involved.** `newInitControlRedactor(operatorHome, tempHome, workdir,…
- [initialize_control_probe_test.go](e2e-realclaude-initialize-control-probe-test-go-2.md) — beside the redactor at the capture site and wires it to the fifth field described above.** `newDropcapScanner(home, "", workdir)` — the…
- [initialize_control_compare_test.go](e2e-realclaude-initialize-control-compare-test-go.md) — two arms that send the `initialize` control request compared against the arm that sends none, at the same turn index, over #1763's three…
- [ask_user_question_record_test.go](e2e-realclaude-ask-user-question-record-test-go.md) — the record half of the `AskUserQuestion` capture family: a four-field fixture record, pinned by a hand-written ordered-name literal, proved offline with no claude binary and no credentials.
- [ask_user_question_names_test.go](e2e-realclaude-ask-user-question-names-test-go.md) — the name half of the `AskUserQuestion` capture family: a one-input namer locked against all four committed fixture families, proved offline with no claude binary and no credentials.
- [ask_user_question_writer_test.go](e2e-realclaude-ask-user-question-writer-test-go.md) — the writer half of the `AskUserQuestion` capture family: a directory-injectable writer that refuses a record before any filesystem call when its marshalled bytes carry a denied value, proved offline with no claude binary and no credentials.
- [ask_user_question_shape_test.go](e2e-realclaude-ask-user-question-shape-test-go.md) — the shape half of the `AskUserQuestion` capture family: a findings-returning check over the decoded `tool_input`, its first two checks (tool name, at least one question) each with a negative row, proved offline with no claude binary and no credentials.
- [ask_user_question_capture_test.go](e2e-realclaude-ask-user-question-capture-test-go.md) — the live half of the `AskUserQuestion` capture family: spawns a real claude with the daemon's permission flags, taps the call on the approve path via a stub control socket, denies every call, and commits the record.
- [ask_user_question_reader_test.go](e2e-realclaude-ask-user-question-reader-test-go.md) — the read half of the `AskUserQuestion` capture family: a deterministic, credential-free glob-and-scan-and-decode pass over the committed capture, closing the "gate ran green, artifact never landed" gap the live half alone can't.
- [interactive_stream_question_answer_test.go](e2e-realclaude-interactive-stream-question-answer-test-go.md) — proves a live model change does not resolve or replace a parked `AskUserQuestion` batch: the original answer completes, then the next turn announces the target model.
- [interactive_stream_question_refusal_test.go](e2e-realclaude-interactive-stream-question-refusal-test-go.md) — the refusal half: refuses the surfaced batch through the daemon's own inbound path and proves claude neither answers its own question nor presses on into the work it was blocking.
- [interactive_stream_attachment_read_test.go](e2e-realclaude-interactive-stream-attachment-read-test-go.md) — the live proof that claude opens an attachment's on-host path and echoes its contents, closing the second half of the 2026-05-16 attachment-prompt decision.
- [tool_result_sidecar_probe_test.go](e2e-realclaude-tool-result-sidecar-probe-test-go.md) — the `toolUseResult` sidecar the transcript names arrives on claude's stdout too, but spelled `tool_use_result`, snake_case not camelCase; a one-spelling probe reported the inverse finding first.
- [tool_progress_capture_test.go](e2e-realclaude-tool-progress-capture-test-go.md) — the live `tool_progress` capture: a from-scratch FIFO-hold helper found a `sync.Once`-cleanup hang that killed a 20-minute gate run, and the probe arms on the committed fixture's absence rather than an env var so `make e2e-realclaude` doesn't skip it vacuously.
- [compaction_capture_test.go](e2e-realclaude-compaction-capture-test-go.md) — the live `/compact` capture: confirmed the seam (`system/status` + `system/compact_boundary`) but the dispatcher's gate-only run never commits, so the 2026-09-08 fixture fired and was lost — read before assuming #2227/#2228 have bytes to read.
- [parent_tool_use_capture_test.go](e2e-realclaude-parent-tool-use-capture-test-go.md) — the live `parent_tool_use_id` capture: runs sonnet, not this family's usual haiku, because the probe's whole premise is that claude delegates rather than inlining two reads — a cheaper model produces a green run with no subagent in it. Fixture not yet committed as of #2191's landing.
- [subagent_prompt_capture_test.go](e2e-realclaude-subagent-prompt-capture-test-go.md) — the live capture pinning a subagent's delegated-prompt line under `--forward-subagent-text`: reuses `parent_tool_use_capture_test.go`'s `ptuc*`/`dropcap*` helpers rather than a second rig, and the capture settled that the line carries **no** harness flag, unlike every other member of streamsup's harness-text census except the interrupt notice. Fixture committed as `testdata/subagent_prompt_v2.1.280.json`.
- [task_notification_capture_test.go](e2e-realclaude-task-notification-capture-test-go.md) — the live `task_notification` capture: a FIFO must be released mid-test, not just held, because the subtype only fires on a background task's terminal state; a wait keyed on the companion subtype's `resultSeen` latch is the wrong wait for an event that outlives the turn; and a record field documenting the deny-scan's own literal needles can fail the scan it describes. Fixture committed as `testdata/task_notification_v2.1.259.json`; see the child doc for the stall it records.
- [roster_after_finish_capture_test.go](e2e-realclaude-roster-after-finish-capture-test-go.md) — the live capture settling whether claude emits a trailing `background_tasks_changed` after a backgrounded task completes: measured on 2.1.280 as **yes, unprompted, and it omits the finished task** — but only once the staging asked for `run_in_background` explicitly (`task_started` fires on a still-foreground call) and stopped ordering rosters against the terminal-status line (the finish roster arrived one line *before* it). Fixture committed as `testdata/roster_after_finish_v2.1.280.json`.
- [effort_init_capture_test.go](e2e-realclaude-effort-init-capture-test-go.md) — the live `system/init` capture with an effort actually set on both of the daemon's paths: **`effort` is absent at 2.1.259 even with `--effort low` on the launch argv and `/effort high` acknowledged in band**, settling #2195/#2252's open question in favour of dropping the field. Also the `dropcapRedactor` late-adder pattern (`addValueClass`/`addPathClass`) for values only claude can supply, and the zero-nonce footgun in `strconv.FormatInt`. Fixture not yet committed as of #2251's landing; the finding above comes from the live gate's log, not committed bytes.
- [operator_system_lines_capture_test.go](e2e-realclaude-operator-system-lines-capture-test-go.md) — the live capture of the four unmapped operator-facing `system` subtypes: **`informational` is observed and decodes cleanly (and proves a `UserPromptSubmit` hook runs under `--dangerously-skip-permissions`), `local_command_output` fires as assistant prose rather than a `system` line at all, `commands_changed` stays inconclusive, `notification` has no known trigger.** Also: claude re-broadcasts the full slash-command inventory on every `system/init` line, not just the first, and a slash-command trigger must be picked from a committed init line's inventory rather than hardcoded. Fixture not yet committed as of #2255's landing — lost in the dispatcher's own gate-only worktree, the fourth capture in this family to land that way.
- [api_retry_capture_test.go](e2e-realclaude-api-retry-capture-test-go.md) — the live `system/api_retry` capture, staged by redirecting `ANTHROPIC_BASE_URL` at a rig-owned listener that answers every request 529: fired, ten lines, `message` absent so the mapping needs no `permission_denied`-style gate, and **a retried-out turn closes as `result`/`success` with `terminal_reason: "api_error"`** — subtype alone misreads it as success. Also: a request count alone is not a retry count (check the spawn floor, and repeats within one endpoint, not the raw total), and this is the first probe in the family whose double artifact-directory write actually got exercised and recovered a fixture the gate's own worktree lost.
- [stream_event_capture_test.go](e2e-realclaude-stream-event-capture-test-go.md) — the live `stream_event` capture under `--include-partial-messages`, keeping every line of a turn rather than a filtered quarry: content-block indices are per-message and a stale map silently mislabels the second message onward, a whole-turn capture needed its own drop-cap promotion refusal no filtered sibling had needed, and it ships **without** the messaging-socket redaction class `effort_init_capture_test.go` added — a known gap to close before promoting the fixture, which is not yet committed.
- [mcp_status_capture_test.go](e2e-realclaude-mcp-status-capture-test-go.md) — the live `mcp_status`/`mcp_reconnect`/`mcp_toggle` capture on production's downgraded spawn arm only, against a three-server document with one command that doesn't exist: a shipped claude binary's own bundled schema outranks `sdk.d.ts` (two extra per-server fields, camelCase `serverName`) but is still not the wire, `t.Fatalf` inside a `t.Cleanup` skips every earlier-registered cleanup, and the record's `credential_scan_skipped` field is re-marshalled after the deny-scan and ships unscanned. Fixture not yet committed as of this ticket's landing.
- [mcp_status_reconnect_test.go](e2e-realclaude-mcp-status-reconnect-test-go.md) — the live proof that an `mcp_reconnect` the daemon relays actually reaches claude and changes what claude reports: the broken server arrives via a claude-binary swap that rewrites the daemon's own `--mcp-config` document in place (never a second document — `mcpStatusEligible` fails every read and actuation closed on two), the rewrite copies unmodelled keys through as `json.RawMessage` rather than a lossy typed twin, and the shim writes nothing to stdout. Captures nothing, so nothing needed committing beyond the test.
- [context_usage_capture_test.go](e2e-realclaude-context-usage-capture-test-go.md) — the live `get_context_usage` capture: `detail:"summary"` and `detail:"full"` return the identical payload against claude 2.1.259, differing only by a sevenfold round-trip cost, so a production writer should default to `"summary"`. Also: a dotted-path leaf selector must break ties by depth before lexical order, or a nested duplicate of a candidate field outranks the shallow one meant.
- [mid_turn_user_capture_test.go](e2e-realclaude-mid-turn-user-capture-test-go.md) — the live capture of what claude 2.1.280/haiku does with a `user` line written mid-turn: only a write during a running tool call folds into the current turn, both a text-only answer and a write made just after the turn's last tool result open a second turn instead, and `--replay-user-messages` echoes every turn's opening message too, not only the mid-turn ones. Fixture committed as `testdata/mid_turn_user_v2.1.280.json`.
- [codex_conversation_live_test.go](e2e-realclaude-codex-conversation-live-test-go.md) — `TestCodexConversationLive` (#2660), the first live Codex proof through the whole daemon rather than the runner level: an unset `permission_mode` never reaches `codexTurnOverrides` as `""` because `canonicalSettings` normalises it to `default` first, and the modal wait must not filter on `conversation_id` or an empty-scope modal times out instead of failing visibly.
- [e2e-harness.md](e2e-realclaude-e2e-harness-md.md) — see the document
- [1415](e2e-realclaude-related-tickets-1415-1439.md) — see the document
- [1440](e2e-realclaude-related-tickets-1440-1447.md) — see the document
- [1448](e2e-realclaude-related-tickets-1448-1458.md) — see the document
- [1459](e2e-realclaude-related-tickets-1459-1463.md) — see the document
- [1353](e2e-realclaude-related-tickets-1353-1428.md) — see the document

See [development verification](development-verification.md) for cross-package testing and evidence checks.
