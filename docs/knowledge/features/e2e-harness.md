# E2E Harness

`internal/e2e` is a build-tag-isolated test harness that spawns `pyry` as a real
daemon in an isolated temp `$HOME`, blocks until the control socket is dialable,
drives CLI verbs against it, and tears down reliably on test cleanup.

Phase: tickets #68 (spawn + cleanup), #69 (CLI driver + first feature e2e), #52 (CLI verbs e2e coverage — `stop`, `logs`, `version`, `status` stopped path
+ `RunBare` helper), #106 (restart primitive — `StartIn` / `Stop` + first
restart-survival test), #107 (two more restart-survival tests — evicted
state + `lastActiveAt` timestamps — plus file-local `newRegistryHome`
helper), #111 (failed-start primitive — `StartExpectingFailureIn` + the
corrupt-registry fail-loud test), #112 (positive-outcome startup test —
`TestE2E_Startup_MissingClaudeProjectsDir`, no harness changes), #115
(idle-eviction + lazy-respawn e2e — variadic flags on `StartIn` / `spawn`
+ two new tests asserting eviction and respawn at the binary boundary), #125 (attach PTY harness — `AttachHarness` + `StartAttach(t, sessionID)`
in `attach_pty.go` + `TestE2E_Attach_RoundTripsBytes` proving terminal →
attach client → control socket → bridge → supervisor PTY → claude →ack
flow at the binary boundary), #123 (rotation primitive — `StartRotation(t,
home, sessionsDir, initialUUID, trigger)` constructor wires #122's
fake-claude binary as the supervised child via `-pyry-claude=<fakeBin>` +
three `PYRY_FAKE_CLAUDE_*` env vars; refactors `spawn` over a shared
`spawnWith(t, home, spawnOpts)` core), #127 (attach clean-detach proof —
`AttachHarness.WaitDetach(t, timeout)` + `AttachHarness.Run(t, verb,
args...)` methods on the existing struct, `runVerb` extracted from
`Harness.Run` as the shared body, plus
`TestE2E_Attach_DetachesCleanly` driving the documented `Ctrl-B d`
sequence and asserting the triple invariant attach-exits-0 +
daemon-survives + supervised-child-still-`Phase: running`), #120
(rotation-watcher e2e — `TestE2E_RotationWatcher_DetectsClear` consumes
`StartRotation` + the fake-claude binary to drive a real pyry through
one `/clear`-shaped JSONL rotation; asserts the registry's bootstrap
id follows from the pre-created `<initialUUID>.jsonl` to the post-trigger
fresh UUID against the real `/proc`-or-`lsof` probe; no harness changes), #128 (attach survives claude restart e2e —
`TestE2E_Attach_SurvivesClaudeRestart` extends `TestHelperProcess`'s
`echo` mode with a startup PID marker + `__EXIT__\n`/`__PID__\n`
control lines and drives an attach across a forced child restart; the
spec was billed as test-only but the test surfaced a real production
bug in the supervisor's bridge input path — the input pump leaked
across `runOnce` iterations and silently corrupted bytes typed during
a restart. Fix: replaced `io.Pipe` in `Bridge` with a `chan []byte` +
per-iteration cancel signal (`BeginIteration` / `EndIteration`),
supervisor now drains both copy goroutines on each iteration. See
[ADR 007](../decisions/007-bridge-iteration-boundaries.md)),
split from #51.

## What It Does

- Builds `pyry` once per test process (or reuses `$PYRY_E2E_BIN`).
- Spawns it pointed at a `t.TempDir()` `$HOME`, with the sleep-claude wrapper
  (see § Isolation Strategy) as the default supervised "claude" and idle
  eviction disabled.
- Polls the Unix socket until `net.Dial` succeeds (5s deadline), short-circuiting
  if pyry exits early.
- Each test daemon starts in its own process group. After it exits, the harness
  stops any remaining children in that group and waits for the group to disappear.
  This also covers forced daemon termination and failure before readiness.
- On test cleanup: SIGTERM, escalate to SIGKILL after 3s, then `os.Remove` the
  socket. The temp `$HOME` is auto-cleaned by `t.TempDir`. The harness's `Done`
  channel closes after process-group cleanup. An inherited output pipe cannot
  keep daemon collection waiting indefinitely.

## Invocation

```
go test -tags=e2e ./internal/e2e/...
go test -tags=e2e_install ./internal/e2e/...   # install-service round-trip (Linux)
```

Default `go test ./...` does not compile the package. The harness file's
build tag is `//go:build e2e || e2e_install` so the binary cache and
`childEnv` helper are reusable from the install-e2e tests (see
[install-e2e.md](install-e2e.md)) without duplicating boilerplate. Setting
`PYRY_E2E_BIN=/path/to/pyry` skips the per-process `go build` (CI
optimization).

## Isolation Strategy

Pyry resolves `~/.pyry/<name>.sock`, `~/.pyry/<name>/sessions.json`, and
`~/.claude/projects/<encoded-cwd>/<uuid>.jsonl` via `os.UserHomeDir()`, which
honors `$HOME` on Unix. The harness redirects `HOME` to `t.TempDir()` so every
path the daemon would touch under a real home is contained, with one env var.

Belt-and-suspenders: `-pyry-socket=<HomeDir>/pyry.sock` is also passed
explicitly. The registry still lands at `<HomeDir>/.pyry/test/` via HOME
redirection — no new `-pyry-registry` flag was needed.

`PYRY_NAME` is stripped from the child's env so the operator's shell alias can't
leak into a test daemon. Helpers that accept `extraEnv` append it after this
scrub, allowing a test whose subject is `PYRY_NAME` to opt in deliberately.

Spawn args:

```
-pyry-socket=<HomeDir>/pyry.sock
-pyry-name=test
-pyry-claude=<home>/sleep-claude.sh
-pyry-idle-timeout=0
<extraFlags...>          # variadic, last-wins via Go's flag package
-- 99999
```

`-pyry-claude` defaults (zero-value `spawnOpts.claudeBin`) to the
sleep-claude wrapper — `writeSleepClaude(t, home)` writes `#!/bin/sh` +
`exec sleep 99999` to `<home>/sleep-claude.sh` (0o755) and returns its path
(#918; was a bare `/bin/sleep` before). The wrapper ignores all argv, so it
tolerates both the bootstrap invocation's trailing `99999` and any
daemon-appended spawn-time flag — see § Default Stand-In Argv Tolerance
below for why a bare `/bin/sleep` stopped working. `exec sleep 99999`
survives ~27 hours (longer than any test runs), and the readiness gate
doesn't depend on the child being a real claude. `99999` (a plain integer in
seconds) is the only argv form portable across BSD and GNU `sleep`:
`infinity` is GNU coreutils only and macOS BSD sleep rejects it (see
`lessons.md § Test helpers across packages`). #115 changed the harness from
`infinity` to `99999` because the lazy-respawn test waits for `Phase:
running` after a respawn — under `infinity`, macOS BSD sleep exits
immediately, the supervisor enters perpetual backoff, and `Phase: running`
is never observed. `IdleTimeout=0` defeats the eviction timer by default;
tests that need eviction pass `-pyry-idle-timeout=<dur>` via the variadic on
`StartIn`.

For service-folder tests, set `exec.Cmd.Dir` explicitly as well as the isolated
HOME. `spawnWith` sets `--pyry-workdir` but inherits the test process cwd; that
flag controls Claude's spawn directory, while the daemon's startup workspace
base comes from its own process cwd. With the usual isolated HOME, leaving
`Dir` unset exercises the legacy fallback instead of the intended service folder.
Use a distinct spawn override
and assert both the decrypted raw `hello_ack.workspace_root` and the persisted
General cwd, label and seed marker so wiring either consumer to the wrong base
fails the test. See `TestWorkspaceBase_DaemonAdvertisesAndSeedsProcessCwd` and
the [startup-base contract](../../protocol-mobile.md#hello_ack-v2-specific-note).

### Pre-daemon paired-device setup

Tests that need an authenticated phone before daemon startup use
`paireddevice.Setup` from `internal/e2e/internal/paireddevice`. Invoking the
operator-facing `pyry pair` command is the wrong fixture boundary: that command
is a live-daemon client, while setup must finish before the daemon under test
exists. Keeping the helper under `internal/e2e/internal` makes the offline path
available to the hermetic, live-Claude and live-relay suites without exposing it
to production packages or external callers.

`Setup` takes an absolute isolated home, instance name, relay destination,
optional label and remote-permission choice. It reuses the instance identity and
static key, appends a fresh device, and returns the matching `pair.Payload` only
after `devices.Registry.Save` succeeds. The plaintext token is therefore an
egress value, never persisted state; failures return a zero payload. Sequential
repeats are supported, but concurrent setup against one instance is outside the
contract because the underlying identity and key stores are not concurrent
initializers. See [Pair command](pyry-pair-command.md) for the shared credential
shape and [Device registry](devices-registry.md) for persistence semantics.

A save-failure test must inject a known token and fail at the save seam. That is
what lets it prove both halves of the security contract: the prior registry is
byte-for-byte unchanged, and neither the known plaintext nor its encoded pairing
appears in the error or any persisted file. Filesystem permission failures are a
poor substitute because they are platform-dependent and often fail before a
secret exists.

## Readiness Signal

Poll `os.Stat` + `net.Dial` on the socket with a 5s deadline and 50ms gap.
A successful `Dial` establishes that `ctrl.Listen` bound the listener; it does
not establish that `ctrl.Serve` has accepted a request or the pool can supervise
a session. `serveControlWhenReady` now waits for `Pool.Ready` before serving
requests (#2866). The session-removal fixtures additionally use
`waitForRunnerStatus` to observe bootstrap phase `running` and a nonzero child
PID before `control.SessionsNew`. Retrying a premature mint is unsafe:
`Pool.MintWith` persists before `supervise` can return `ErrPoolNotRunning`, so a
retry can leave an extra registry entry. The observed child is a bounded fixture
barrier, not a substitute for the production serving gate.

It is deliberately **not** a child-initialize readiness signal (#2281). A test
that immediately requests retained child state can observe the hosted session
before its `initialize` response has populated the hold. Such a test may retry
only the protocol's explicit transient outcome — for a model-list request,
retryable `model_list.unavailable` — and must fail on every other reply. Treating
socket readiness as vocabulary readiness makes an otherwise-correct integration
test timing-dependent. The same race affects spawn-time live-frame cardinality:
if connecting or routing a client could expose the bootstrap child's report, wait
for a child-level observation before dialing. #2375 uses the fixed, content-free
no-cursor drop diagnostic for the bootstrap report; without that barrier, a fast
client can receive the bootstrap frame and make a replacement-child proof count
the wrong spawn.

A second `select` watches `doneCh` (closed by the wait goroutine on
`cmd.Wait` return). An early pyry exit short-circuits the deadline and surfaces
captured stderr in the `t.Fatalf` message.

## First Feature E2E (`TestStatus_E2E`)

```go
func TestStatus_E2E(t *testing.T) {
    h := Start(t)

    r := h.Run(t, "status")
    if r.ExitCode != 0 {
        t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
            r.ExitCode, r.Stdout, r.Stderr)
    }
    if !bytes.Contains(r.Stdout, []byte("Phase:")) {
        t.Errorf("status stdout missing %q line:\n%s", "Phase:", r.Stdout)
    }
}
```

`"Phase:"` is the leading literal in `runStatus`'s output (`fmt.Printf("Phase:
        %s\n", resp.Phase)`) and is stable across phase values, restart counts,
and future field additions. Asserting on the *value* (`PhaseRunning` etc.)
would couple the test to claude-child startup timing — exactly what
`/bin/sleep infinity` was chosen to avoid. The contract this test verifies is
"daemon is up, socket answers, status verb round-trips."

`pyry version` was rejected as the *proof-of-life* verb (it short-circuits in
`main.go` before parsing flags, so it doesn't exercise the socket plumbing the
harness sells), but is covered by `TestVersion_E2E` below via `RunBare`.

## Bare CLI Driver (`RunBare`)

`RunBare(t, args...)` is the daemon-free sibling of `Harness.Run`. Same binary
cache (`ensurePyryBuilt`), same `runTimeout` (10s), same exit-code mapping —
but no daemon spawn, no auto-injected `-pyry-socket`, no `childEnv(h.HomeDir)`.
The test process env passes through unchanged.

Two use cases motivated the helper:

1. **Verbs that don't touch the socket.** `pyry version` short-circuits in
   `main.go` before flag parsing. Spinning up a daemon to test it is wasted
   wall-clock and inverts the test's intent.
2. **Negative tests against a known-bad socket path.** "Run `status` against a
   socket with no daemon" is most cleanly expressed as "point at a fresh temp
   path and assert the failure shape" — no spawn-then-stop-then-race-the-
   teardown ordering glue.

The helper is the *only* harness API added in #52. (`Harness.Stop()` mid-test
was deferred at the time and shipped later in #106 — see the Restart Pattern
section above. Typed `Status()` / `Logs()` wrappers remain declined.)

## HOME-Isolated Bare Drivers (`RunBareIn`, `RunBareInWithEnv`)

`RunBareIn(t, home, args...)` pins `HOME` through `childEnv(home)` so a
one-shot binary invocation can read or discover `~`-relative state inside a
`t.TempDir()` without disturbing the test process. It does not inject a socket
flag or spawn a daemon. Since #2393 it delegates to
`RunBareInWithEnv(t, home, nil, args...)`, keeping one execution and capture
implementation.

`RunBareInWithEnv(t, home, extraEnv, args...)` appends explicit environment
entries after `childEnv` has replaced `HOME` and stripped `PYRY_NAME`. Reserve it
for behavior whose contract is specifically about an environment variable. The
pairing incident regression passes `PYRY_NAME=pyry-agent` here to prove that bare
issuance still selects the sole running `pyry` service.

The ordering is load-bearing: `RunBareIn`'s scrub makes ordinary tests immune to
the operator's shell, but it also means a selector test that relies on
`t.Setenv("PYRY_NAME", ...)` alone can pass while exercising no environment
selector at all. Such tests must use the explicit override helper. See
[Pair command](pyry-pair-command.md).

## CLI Verb Coverage Tests (`cli_verbs_test.go`)

`internal/e2e/cli_verbs_test.go` (build tag `//go:build e2e`) covers the
remaining shipped non-interactive verbs. Lives in its own file alongside
`harness_test.go` — the latter is about *harness behaviour* (smoke,
no-leak-on-fatal, the canonical `TestStatus_E2E` proof-of-life), the former
about *CLI surface coverage*. `processAlive` from `harness_test.go` is reused
via package scope.

| Test | What it asserts |
|---|---|
| `TestStop_E2E` | exit 0, stdout contains `"stop requested"` fragment, then bounded poll (3s deadline, 50ms gap) until both `!processAlive(pid)` AND `os.Stat(sock)` returns `fs.ErrNotExist` |
| `TestStatus_E2E_Stopped` | `RunBare("status", "-pyry-socket="+bogusSock)` against a fresh non-existent path: exit != 0, non-empty stderr, no `panic` / `goroutine ` / `runtime/` substrings |
| `TestLogs_E2E` | exit 0, non-empty `bytes.TrimSpace(r.Stdout)` (the supervisor's in-memory ring captures startup lines, so a healthy daemon's log buffer is never empty by the time `Start(t)` returns) |
| `TestVersion_E2E` | `RunBare("version")`: exit 0, output starts with literal `"pyry "` prefix, remaining token is non-empty (`dev` in test builds, real version under `-ldflags`) |

### Why bogus-socket, not spawn-then-stop, for the stopped-status test

The spawn-then-stop-then-status path needs the test to wait for the daemon to
actually shut down (otherwise status hits a still-listening socket and
succeeds, defeating the test). That's the same poll loop as `TestStop_E2E`,
plus ordering glue, plus a second `Run` call. The bogus-socket variant
exercises the same code path (`net.Dial` fails → error surfaces clean to
stderr → non-zero exit) without any timing dependency. Strictly simpler,
strictly more deterministic.

### Why poll *both* `processAlive` and `os.Stat(sock)` in `TestStop_E2E`

`pyry stop` returns once the server has acknowledged the request, but the
daemon's child unwind and the supervisor's deferred socket cleanup happen
asynchronously after `Wait` returns. Asserting on either condition alone
admits a flake. Both in the same iteration costs nothing (each probe is
syscall-cheap) and avoids racing the cleanup defer.

### Negative assertion vocabulary for "clean error"

`TestStatus_E2E_Stopped` deliberately doesn't pin the dial-failure error
wording (today: `pyry: status: ... connect: no such file or directory`) — that
string is allowed to evolve. Instead it asserts the *shape* of the failure:

- `panic` — Go's panic header
- `goroutine ` — Go's stack-trace header (`goroutine N [state]:`)
- `runtime/` — Go runtime frames in tracebacks

Three conservative substrings catch panics and stack traces without coupling
to the exact wording. The same pattern fits any "clean error, not a crash"
assertion.

## Concurrency Model

| Goroutine | Owns | Lifetime |
|---|---|---|
| Test goroutine | `Start` flow, teardown | Test scope |
| Wait goroutine | `cmd.Wait()`, `close(doneCh)` | From `cmd.Start` until child exits |

`Stdout`/`Stderr` are `bytes.Buffer`s wired into `cmd.Stdout`/`cmd.Stderr`
directly — `exec.Cmd` synchronizes its writers with `Wait`, so reads after
`<-doneCh` are race-free without an explicit mutex.

`sync.Once` guards build (`binOnce`) and teardown (`cleanupOnce`). No locks.

## Teardown Sequence

Registered via `t.Cleanup`:

1. `cmd.Process.Signal(SIGTERM)`
2. Wait on `doneCh` with a 3s grace timer.
3. On grace expiry: `SIGKILL`, wait another 1s on `doneCh`.
4. On SIGKILL grace expiry: `t.Logf` warning; let leak verification surface it.
5. `os.Remove(SocketPath)` — defensive, since SIGKILL bypasses pyry's own
   socket cleanup.
6. `HomeDir` is auto-cleaned by `t.TempDir` when allocated by `Start(t)`.
   Under `StartIn(t, home)` the caller owns the directory's lifecycle —
   teardown leaves `HomeDir` intact so a subsequent `StartIn` can reuse it.

The `sync.Once` makes this safe to call from a manual `Stop()` (shipped in #106) plus `t.Cleanup` without double-firing.

## Failure Posture

Fail-fast — `Start` calls `t.Fatalf` rather than returning an error, since the
only reasonable response in test code is to abort.

| Failure | Response |
|---|---|
| `go build` fails | `t.Fatalf` with build output |
| `cmd.Start` fails | `t.Fatalf` |
| Readiness deadline | `t.Fatalf` with stderr tail |
| Pyry exits during readiness | `t.Fatalf` with stderr tail |
| SIGTERM grace expires | escalate to SIGKILL |
| SIGKILL grace expires | `t.Logf` warning |
| `os.Remove(socket)` post-kill | best-effort, ignore err |

## Recorded lifecycle timing and settlement

Private artifact validation must precede reading. An `Lstat` followed by an
ordinary open leaves a replacement window: a FIFO can wait for a writer, and a
symlink can expose target bytes before a later identity rejection. Inode equality
also accepts a symlink back to the inspected original. `runnerPrivateRead` opens
with `O_NOFOLLOW|O_NONBLOCK`, then checks the descriptor is regular, has mode
exactly 0600 (including absence of special mode bits), fits the caller's size
limit and matches the inspected inode before consuming bytes. Rejected
descriptors are closed; accepted reads retain the limit-plus-one bound and
content-free error contract. Parent-directory ownership and final artifact
audits remain separate requirements. See the
[read-ordering controls](development-verification-prove-that-tests-distinguish-the-change.md).

`TestRecordedClaude` runs the original scenario assertions through recorded
transport. `RunLifecycleBroker` keeps real input pipes and owned processes in
replay. Strict `Player.Input` matching releases responses independently of the
stand-in's stdin lifetime. Writing a matched final query into that pipe before
delivering its responses can lose every released response to EPIPE after owned
predecessor exit, even with an exhausted player. Capture retains bounded input
forwarding and pipe-error rejection; replay still invokes its input observer
through `forwardLifecycleFrame`'s exactly-once callback contract.

Startup request order is a capture timing outcome. In replay, initialize
completion alone does not mean the player expects its first user input: an
asynchronous `mcp_status` request may still precede it. `ReplayTurnReady`
signals the first `Player.AwaitsTurn` boundary; the send-file driver waits for
its private one-shot marker before triggering input. Keep strict frame matching
and the original modal, tool-reference and stored-attachment assertions.
`TestRecordedMCPSendFileWaitsForStartup` delays initialize to expose this ordering
without changing capture or live Claude behavior.

Response delivery, action acknowledgement, player exhaustion, terminal receipts
and predecessor disappearance are separate observations. For idle resume,
receipt publication may precede daemon reaping; see the
[resume boundary](e2e-realclaude-interactive-stream-resume-after-eviction-tes.md#resume-snapshot-boundary).
Kill/rotation/eviction completion requires authorized
owned termination, both provider output EOFs, complete strict input with no
partial frames, terminal checks and receipts, and the outer artifact audit.
Both EOFs can precede a final query, so an incomplete replay waits within the
existing deadline while original input remains open. Input EOF with missing
frames still fails. `TestLifecycleReplayAfterProviderExit` kills and reaps its
owned child before the final query, checks every released response and holds
stdout and stderr open separately to reject early completion. Its live-exhausted
arm also proves that action acknowledgement and player exhaustion cannot replace
real teardown; changed/partial/missing input, foreign/missing actions and failed
terminal checks remain rejection controls. This is offline replay proof; see the
[rotation contract](../../testing/daemon-recordings.md#session-rotation-and-native-transcript-snapshots).

The readiness origin is established after startup validation; a causal
user/input epoch is recorded after capture forwarding or strict replay matching
and its input observer, before completion observation and `OnInput` witness work.
Subsequent outputs reuse their recorded
anchor and absolute offset. Re-anchoring an existing phase, compressing gaps or
retiming a fixture would erase the timing assertion.

`waitLifecycleOutput` checks the absolute due time before and after waiting;
`forwardLifecycleOutput` checks again after forwarding. All three retain the
100 ms allowance, and the actual pipe write deadline is capped at due + 100 ms.
`forwardPreparedLifecycleOutput` restores immutable bytes with `DaemonBinding.Frame`
before waiting. Waiting first wastes the captured gap and puts preparation
inside the forwarding allowance. Physical `BeforeOutput` observation, replay
projection and actual forwarding stay at delivery; moving physical observation
early would change the schedule. `safeCLIJSON` retains duplicate-key and privacy
validation before physical parsing without a redundant outer duplicate-key scan.
`TestComposerOutputPreparationSchedule` gates real restoration of the reviewed
Composer response and charges 120 ms against its 34.33 ms offset: wait-first
misses due + 100 ms, while prepare-first fits. No observer or write runs early
or after preparation failure; preparation overrun still rejects. See the
[controlled proof and historical attribution limit](../../testing/daemon-recordings.md#physical-background-task-boundary).
Late input, scheduler overshoot and final-write backpressure must still fail.
`TestLifecycleTimelineUsesAbsoluteDueTimes`,
`TestLifecycleTimelineRejectsLateFinalForward`,
`TestLifecycleInputTimelineBindsActualPrecedingUser` and
`TestLifecycleInputTimelineDoesNotResetAnExistingUserPhase` cover these boundaries
and stale/future anchors.

For the two conversation scenarios, `NewConversationCompletionObserver` collects
every successfully forwarded frame in capture and replay, but calls
`completedConversationExchange` only at a stdout `result` before publication.
The cheap result-header check only admits full validation: failed, malformed,
incomplete or foreign sources cannot authorize a receipt. Revalidating the whole
growing prefix between streaming frames can consume the captured gaps even when
the absolute waits are correct. `TestConversationCompletionPreservesOutputSchedule`
charges 35 ms per real derivation against a 20 ms cadence in both modes and
independently validates the resulting receipt.

Reply arrival alone does not establish terminal settlement.
`CheckConversationSettlement` still requires the owned completion and worker
evidence before sealing; `ValidateConversationCompletion` independently derives
the receipt prefix and final tape, and final ownership, source and I/O audits
remain required. See [timing receipt propagation](e2e-realclaude-test-infrastructure.md#recorded-lifecycle-failure-receipts)
and the [measured Linux diagnosis](development-verification-test-execution-and-artifact-survival.md#diagnosing-recorded-lifecycle-output-timing).

On Linux and macOS, session-resume and Composer Stop launch ownership must reach
the actual outer-owned invocation.
Checking a helper against a daemon PID supplied by a control file accepted an
unrelated daemon's otherwise qualifying helper even with a real SIGKILL/Wait.
`StartFallbackLaunchAudit` binds the command immediately after Start;
`FallbackLaunchAudit.ownDaemon` retains and rechecks the complete current kernel
ancestry to the outer process before observing children. PID, start identity,
parent and group matter together: an unknown chain or changed identity cannot
authorize a cancelled launch. `TestFallbackCancelledLaunchOwnership` synchronizes
owned and unrelated process trees before real cancellation; macOS uses
`TestDarwinLaunchOwnership` for the same boundary.

The macOS sampler uses bounded libproc queries through cgo plus kernel sysctls.
It checks the actual executable path and mapped vnode, closed helper/worker argv
and private cwd vnode, then rechecks worker and ancestry identities. Unreadable,
dying or missed observations remain unknown; changed retained identities invalidate
the audit. Without cgo, native queries remain unknown and cannot authorize a
cancellation. `proc_listchildpids` takes buffer bytes but returns a PID count;
treating that result as bytes silently discards launches despite passing static
checks. `TestDarwinCancelledLaunch` holds a real helper before registration until
the normal sampler observes it, so a broken adapter cannot pass through direct
post-sampler observations alone.

Production can complete a cancellation Wait before the helper registers a job.
`ReadLifecycleWorkers` accepts that boundary on Linux and macOS for session-resume
and Composer Stop only with the independent current-launch audit, observed absent
registration, a completed production SIGKILL Wait with group cancellation, and
process/group death. Every Wait joins once;
cancelled launches consume the two-attempt allowance but add no native exchange.
`TestFallbackCancelledLaunchBoundary` synchronizes a current owned helper before
registration, then cancels and reaps it; one launch and one Wait with zero
registered workers must pass only with the independent audit. Wait-only, unknown,
duplicate, foreign and unmatched evidence still fails. Exact reviewed exchange
cardinality, registered-worker source/peer checks and complete literal native
I/O/EOF remain required. The `live-group` control in `TestDarwinLaunchOwnership`
reaps the worker while retaining another owned group member: rejecting only a
live worker would leave the group-death check unproved. See the
[recording contract](../../testing/daemon-recordings.md#idle-eviction-and-literal-native-resume-inputs),
[Composer Stop boundary](../../testing/daemon-recordings.md#physical-background-task-boundary)
and [post-stop evidence retention](e2e-realclaude-test-infrastructure.md#recorded-lifecycle-failure-receipts).

Failure cleanup must reach the actual owning daemon before its sole worker Wait.
A reported worker PID can be reused after reaping even while the outer command
remains alive. `testStartLaunchOwner` closes its control pipe so
`testRunLaunchDaemon` cancels its owned worker group before reaping; it never
signals a reported PID.
`TestDarwinLaunchCleanup` expires evidence reads before cancellation and after
reaping, requiring bounded cleanup of an owned surviving group member while an
unrelated live group remains untouched.

A `total-wait-cardinality` rejection identifies a failed inventory join, but
cannot establish cancellation before registration once the original home and
counts are gone. Preserve `FallbackLaunchAudit.Save` diagnostics in an external
private `PYRY_CLAUDE_TAPE_EVIDENCE` directory: current launch identities,
registration states, registered owners and actual Wait flags/statuses distinguish
unknown from absent without copying worker content. A passing replay with zero
launches cannot explain an earlier mismatch. Synchronized controls prove the
accounting boundary; offline replay establishes no new live-provider behavior.

Recover the original failure stage before selecting a repair. A forwarding miss
before inventory cannot be diagnosed as a failed worker join. `LifecycleDiagnostic`
retains four output-stage maxima with ordinals and at most 64 numeric timing
samples, including process CPU elapsed (`-1` means unknown). CPU includes other
broker goroutines; neither it nor passing replays can attribute an older miss
without substage evidence. Diagnostics confer no completion authority.

## Failure-Injection Verification

`TestHarness_NoLeakOnFatal` verifies the load-bearing safety property: a
`t.Fatal` mid-test must not leak a `pyry` process or socket file.

The naive in-process subtest (`t.Run("crash", ...)`) doesn't work — Go's testing
framework propagates an inner `t.Fatal` to the parent, ending the outer test
before it can inspect leak state. The harness re-execs the test binary instead:

```
parent test
  └── exec.Command(os.Args[0], -test.run=^TestInnerFatalChild$, ...)
        with PYRY_E2E_INNER_FATAL_OUT=<state-file>
        │
        └── child test process
              ├── Start(t) → Harness
              ├── write (pid, socket) to state-file
              └── t.Fatal — exercises harness cleanup
        ↓ child exits ↓
  ├── read state-file
  ├── processAlive(pid)?  via `kill -0` (POSIX zero-signal probe)
  └── os.Stat(sock) is fs.ErrNotExist?
```

`TestInnerFatalChild` is gated on `PYRY_E2E_INNER_FATAL_OUT` — unset in normal
runs (`t.Skip`), set under the parent's re-exec. The state file passes the
observed pid + socket path across the process boundary.

`processAlive` uses `os.FindProcess` + `Signal(syscall.Signal(0))` — POSIX
"is this PID alive" probe, zero-cost, returns ESRCH if gone.

## Build Helper

`ensurePyryBuilt(t)` builds pyry once per test process via `sync.Once` into a
persistent `os.MkdirTemp` (intentionally not cleaned — `go test`'s own cleanup
takes /tmp eventually).
`PYRY_E2E_BIN` short-circuits to a known-good binary on disk for CI.

The recorded pool and MCP daemon caches also use process-lifetime
`os.MkdirTemp` directories; `TestMain` removes those two directories after
`m.Run`. A process-global `sync.Once` path cannot live in one caller's
`t.TempDir`: parent cleanup deletes the executable before a later top-level
consumer uses the cached path. Run preceding `TestRecordedClaude` scenarios and
`TestRecordedMCPSendFileWaitsForStartup` together in one uncached process to
exercise that lifetime; an isolated startup pass misses it.

**An `-overlay` mutant does not reach a spawned daemon by default.** `ensurePyryBuilt`
shells a plain `go build ./cmd/pyry` with none of the parent `go test` invocation's
flags, so mutating `cmd/pyry` source and re-running the suite under `-overlay=<path>`
only mutates the *test binary's own compilation unit* — the child `pyry` these specs
spawn is unaffected and the mutant silently fails to redden anything. To exercise a
mutant against the spawned daemon, build it separately
(`go build -overlay=<path> -o <bin> ./cmd/pyry`) and inject it via `PYRY_E2E_BIN`
(#1512). A mutation run that skips this reads as "the assertion under test is
vacuous" when the real cause is "the mutant never shipped".

**A green run under `PYRY_E2E_BIN` still needs a control (#1845).** A mutant
built and injected this way can be green for two indistinguishable reasons:
the assertion is genuinely dead weight, or the injection silently didn't take
(stale cached binary, a build that failed in a way that still left a binary
on disk). Pair every `PYRY_E2E_BIN` mutant run with a control run of the
*clean* binary through the identical `PYRY_E2E_BIN` invocation — only a
clean-PASS / mutant-FAIL pair proves the mutant actually reached the spawned
daemon.

**`ensureFakeClaudeBuilt` has the identical shape, and the identical fix
(#1918).** It shells its own plain `go build ./internal/e2e/internal/fakeclaude`,
so `go test -overlay=<path>` mutating fakeclaude's `main.go` mutates only the
test binary's compilation unit — the spawned fake runs unmutated, and a spec
that dials it (rather than reading its source) reads green for the same
"vacuous assertion" reason as the pyry case above. `PYRY_E2E_FAKE_CLAUDE_BIN`
is the injection point, same two-command route: build the mutant to its own
`-o` path, then run the suite with the env var set to it and a clean-binary
control run for comparison. An e2e that spawns *both* binaries needs mutants
targeted at whichever one the claim under test actually lives in — `PYRY_E2E_BIN`
does not reach fakeclaude and `PYRY_E2E_FAKE_CLAUDE_BIN` does not reach pyry.

**A fixture whose two channel-halves are written from the same constant
cannot fail (#2118).** `TestRelayV2_StreamSessionSettingsReportsTheObservedWindow`
plants a transcript naming a model and a fake-claude rider reporting that
model's context window, and passed full-stack while
[`contextwindow.Read`](contextwindow-package.md#context-window-size--a-believed-default-not-an-asserted-fact)'s
join between those two channels was broken — because the rider's model id and
the planted transcript's model id were the same string literal, so the one
divergence the test existed to catch could never appear in its own fixture.
Any spec that joins two independently-sourced channels (a spawned fake's
output against a planted input file, in general) only exercises the join if
the two sides are seeded from two independently-chosen values, not two copies
of one constant — pin the values the real channels are observed to disagree on,
the way #2118's rider key and transcript id now do.

**A silent fake control request is a timeout, not a no-op (#2517).** When the
production session-settings path began sending `get_settings`, fakeclaude's
unknown-control posture left the connection worker waiting until its 30-second
deadline and three existing stream e2es timed out. A control request that becomes
ordinary production traffic therefore needs an unconditional default fake answer,
unless the test deliberately exercises refusal. The full-stack spec must also
assert the value returned through that control round trip: unrelated saved-settings
or context-window assertions can stay green when the provider is accidentally
unwired and the optional field is merely omitted.

**Checking that a small advertised subset belongs to a larger capture does not
prove the capture traversed the system (#2281).** The first model-rejection e2e
loaded #2279's six-row capture only as a comparison set while fake claude still
published its default two-row fixture; it stayed green although four source rows
never crossed initialize retention or client publication. When a capture is the
claimed provenance, feed an exact projection of every complete row into the
producer and compare the full ordered output before testing downstream behavior.

**A fake that normalises an argv real claude refuses hides a whole defect class
indefinitely, not just one test (#2446).** `fakeclaude.argvIDFlag` keeps the
LAST id flag on the argv it's handed, so `--session-id X … --resume X` — which
real claude refuses outright, exit 1, one stderr line — read here as an ordinary
resume, and the fake tier stayed green through a production crash-loop
(`Pool.UpdateSettings` installing a recomposed argv that still carried the baked
session id; see [`Pool.UpdateSettings`](sessions-package-key-types-pool-updatesettings.md)).
The fix is a second, unconditional guard at the top of `fakeclaude`'s `main`,
above every mode branch: it refuses the same `--session-id` + `--resume`/
`--continue` (without `--fork-session`) pair claude refuses, with claude's own
stderr line. Ungated by env, for `argvIDFlag`'s stem guard's reason — it fires
only on an argv the daemon should never compose, so there is nothing for a test
to opt out of. Reproducing the defect in the fake tier also needed a seeded
`<id>.jsonl`: stream-mode fakeclaude establishes no transcript on its own, so
without one every spawn takes the create form and the composed pair is the
harmless `--session-id` twice — the arm has to plant the fixture that makes the
per-spawn probe pick the resume form before the guard has anything to catch.

**Editing the source in place still needs `-count=1` against a daemon-spawning
suite (#2099).** `-overlay` isn't the only way a mutant fails to reach the
spawned binary: even with `cmd/pyry`'s source edited directly on disk, a
plain re-run of `go test -tags=e2e ./internal/e2e/...` can serve a **cached**
pass, because nothing the Go test cache tracks — the test package's own
sources and build inputs — changed. The daemon runs as a subprocess spawned by
`ensurePyryBuilt`'s `go build`, which is invisible to that cache key. Verifying
a fix this way (rather than via `PYRY_E2E_BIN`, above) needs the in-place edit
**and** `-count=1` together; either alone can read as a mutant that never
landed when it actually never ran.

**A test-local `t.Setenv("HOME", …)` must come after `ensurePyryBuilt`/
`ensureFakeClaudeBuilt`, not before (#1631).** Both are `sync.Once`-guarded, so
whichever caller runs first performs the actual `go build`; every later call
(including `spawnWith`'s own) is a no-op that just returns the cached path.
Overriding `HOME` in the test process *before* either call sends that `go build`
subprocess an empty module cache under the temp home, which tries to re-download
the whole module graph and fails on a private dependency. This only bites a test
that needs `HOME` overridden in the test process itself (e.g. to re-derive a
daemon-side value that reads `$HOME`, as #1631's own AC 2 test does) — the
ordinary `Harness.Run`/`StartIn` path never hits it, since it redirects `HOME` for
the **spawned daemon's** environment via `childEnv`, not the test process's own.

**A production change to what a fresh host contains at startup reaches every
e2e test, not just the ones about that feature (#2569).** When `cmd/pyry`
started seeding a promoted `General` channel and its bound session on a
registry that is both empty and unmarked, six unrelated tests that counted
sessions or conversations on a fresh `$HOME` broke, because every fake-daemon
test starts from exactly that state. The fix lives in the harness, not in the
individual tests: `spawnWith` calls `premarkWorkspaceSeeded`, which writes
`{"conversations":[],"seeded":true}` for the `test` instance unless a
`conversations.json` already exists, so a daemon spawned through the harness
opts out of the seed by default. A test that specifically wants to observe the
seed (`TestWorkspaceSeed_E2E_FreshHostGetsGeneral`) writes a zero-byte
`conversations.json` first — the harness leaves an already-present file alone,
and the daemon loads zero bytes as empty and unseeded. Any future startup
behavior gated on "does a fresh registry/session file exist" needs the same
treatment: either give the harness a matching pre-mark default, or accept that
every existing count-based assertion across the suite is now testing the new
behavior too, whether or not that was the ticket's intent.

## Known Limitations

- **Race detector.** When `go test -tags=e2e -race` is invoked, the parent
  binary is race-instrumented but the harness's `go build` runs without
  `-race`. The follow-up may want `go build -race` when the parent suite uses
  it. Not load-bearing for the primitive; filed for the follow-up.
- **Windows.** Out of scope per CLAUDE.md. The harness uses POSIX signals
  (SIGTERM, SIGKILL) and Unix sockets; no build constraint beyond the e2e tag
  is needed because pyry itself is Linux + Darwin only.

## Deliberately Out of Scope

- Per-verb typed wrappers (`Harness.Status()`, `Harness.Attach()`) — `Run`
  + `RunBare` cover every shipped verb; add wrappers if a consumer
  materially benefits.
- `Options` struct for `StartIn` — today there's exactly one knob (`home`).
  Migration to `Options{Home: ..., ...}` is mechanical and non-breaking
  (`StartIn` becomes a thin alias) when a second knob lands.
- `Option` type and any `WithFoo(...)` constructors.
- Stdin plumbing on `Run` — no current verb reads stdin; add when one does.
- `pyry attach` e2e — interactive PTY, separate work; the harness's
  non-interactive `Run` is not the right driver for it.
- Asserting on specific log line content (couples tests to supervisor
  wording) or specific dial-error wording (couples to platform/syscall
  library).
- GitHub Actions matrix running the suite on push/PR — no such workflow
  exists (`release.yml` is tag-triggered, `self-check-daily.yml` is a daily
  cron). `make e2e` (standalone target) and its wiring into the local
  `make check` gate landed separately — see below.
- Race-mode harness build (`go build -race` inside `ensurePyryBuilt` when the
  parent suite uses `-race`).
- `t.Parallel` migration on the e2e tests — defer until wall-clock pressure
  surfaces. Each test owns its own `t.TempDir` HOME, so parallelism is safe
  in principle.

## Build Gate

`make e2e` (`go test -tags e2e -race -count=1 ./internal/e2e/...`) is part of
`make check` as of #919 — a core-daemon regression now fails the standard
local gate instead of sitting red on `main` unnoticed (the #918 incident: eight #839-era regressions accumulated because the suite ran nowhere by default).
`make preship`'s prerequisite list dropped its own explicit `e2e` token in the
same change since `check` now covers it; `preship` still runs `e2e-realclaude`
(live claude, separate suite) on top. No GitHub Actions workflow runs this on
push/PR yet — see the out-of-scope note above. Details: [codebase/919.md](../codebase/919.md).

## Related

- Specs: `docs/specs/architecture/68-e2e-harness-primitive.md`,
  `docs/specs/architecture/69-e2e-cli-driver.md`,
  `docs/specs/architecture/52-cli-verbs-e2e-coverage.md`,
  `docs/specs/architecture/80-e2e-install-systemd-roundtrip.md`,
  `docs/specs/architecture/106-e2e-restart-primitive.md`,
  `docs/specs/architecture/107-e2e-restart-evicted-and-lastactiveat.md`,
  `docs/specs/architecture/111-e2e-corrupt-registry.md`,
  `docs/specs/architecture/112-e2e-missing-claude-projects-dir.md`,
  `docs/specs/architecture/115-e2e-idle-eviction-lazy-respawn.md`,
  `docs/specs/architecture/125-e2e-attach-pty-harness.md`,
  `docs/specs/architecture/123-e2e-startrotation-primitive.md`,
  `docs/specs/architecture/127-e2e-attach-detach-clean.md`,
  `docs/specs/architecture/128-e2e-attach-survives-claude-restart.md`,
  `docs/specs/architecture/956-fakeclaude-rotation-nonempty-gate.md`,
  `docs/specs/architecture/1141-stream-e2e-harness-send-message.md`
- Pattern: lessons.md § Test helpers across packages (`/bin/sleep` as the
  benign fake claude); lessons.md § Unix-socket sun_path limits and
  t.TempDir(); lessons.md § PTY master backpressure stalls slave-side
  process exit
- Consumers: shipped CLI verbs (#52: `stop`, `logs`, `version`,
  `status` stopped path; #69: `status` running path), restart-survival
  proofs (#106: `TestE2E_Restart_PreservesActiveSessions`; #107:
  `TestE2E_Restart_PreservesEvictedSessions`,
  `TestE2E_Restart_LastActiveAtSurvives`), startup-failure proofs
  (#111: `TestE2E_Startup_CorruptRegistryFailsClean`),
  startup positive-outcome proofs (#112:
  `TestE2E_Startup_MissingClaudeProjectsDir`), idle-eviction +
  lazy-respawn proofs (#115: `TestE2E_IdleEviction_EvictsBootstrap`,
  `TestE2E_IdleEviction_LazyRespawn`), attach PTY round-trip proof
  (#125: `TestE2E_Attach_RoundTripsBytes` via `AttachHarness`),
  rotation primitive (#123: `TestE2E_StartRotation_PrimitiveWiresFakeClaude`
  via `StartRotation` + [fakeclaude-binary.md](fakeclaude-binary.md)),
  attach clean-detach proof (#127:
  `TestE2E_Attach_DetachesCleanly` via `AttachHarness.WaitDetach` +
  `AttachHarness.Run`),
  attach restart-survival proof (#128:
  `TestE2E_Attach_SurvivesClaudeRestart` — also surfaced and fixed
  the bridge input-pump leak; see
  [ADR 007](../decisions/007-bridge-iteration-boundaries.md)),
  Phase 1.1 session-verb tickets (#54, #55, #56),
  install-service round-trip ([install-e2e.md](install-e2e.md)),
  stream-interactive turn-drain proof (#1141:
  `TestRelayV2_StreamSendMessageDrainsTurn` via
  `StartStreamInteractiveWithRelay` + [fakeclaude-binary.md § Stream-json mode](fakeclaude-binary.md#stream-json-mode-1140);
  the harness the rider specs #1136–#1139 and the real-claude capstone #1083 ride)


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Public API](e2e-harness-public-api.md) — Fourteen exported names, including
  `RunBareInWithEnv` for explicit environment-under-test invocations.
- [CLI Driver (`Harness.Run`)](e2e-harness-cli-driver-harness-run.md) — `Run(t, verb, args...)` invokes the cached pyry binary with `<verb> -pyry-socket=<h.SocketPath> <args...>`, waits for it to exit (10s…
- [Restart Pattern (`StartIn` + `Stop`)](e2e-harness-restart-pattern-startin-stop.md) — `StartIn` + `Stop` together let a test prove on-disk invariants survive daemon restart: pre-populate `HOME` → `Start` → `Stop` → second…
- [Failed-Start Pattern (`StartExpectingFailureIn`)](e2e-harness-failed-start-pattern-startexpectingfailurein.md) — `StartExpectingFailureIn(t, home) RunResult` is the failure-side sibling of `StartIn`. 
- [Idle-Eviction + Lazy-Respawn Pattern (`idle_test.go`, #115)](e2e-harness-idle-eviction-lazy-respawn-pattern-idle-test-go.md) — Two tests in `internal/e2e/idle_test.go` (build tag `e2e`) exercise the idle-eviction state machine and lazy respawn at the binary boundary…
- [Active-Cap Eviction Pattern (`cap_test.go`, #116)](e2e-harness-active-cap-eviction-pattern-cap-test-go.md) — Two tests in `internal/e2e/cap_test.go` (build tag `e2e`) close the binary-boundary gap on the concurrent active cap (#41). 
- [Default Stand-In Argv Tolerance (#918)](e2e-harness-default-stand-in-argv-tolerance.md) — The zero-value `spawnWith` path (used by `Start` / `StartIn` / `StartInWithEnv`, and by `StartExpectingFailureIn` via `spawn`) invoked…
- [Attach PTY Harness Pattern (`attach_pty.go`, `attach_pty_test.go`, #125)](e2e-harness-attach-pty-harness-pattern-attach-pty-go-attach.md) — The non-interactive `Harness` drives daemon-only verbs over the control socket with stdio pipes. 
- [Rotation Primitive (`StartRotation`, `fakeclaude_test.go`, #123)](e2e-harness-rotation-primitive-startrotation-fakeclaude-test.md) — `StartRotation(t, home, sessionsDir, initialUUID, trigger) *Harness` is the constructor that swaps `/bin/sleep 99999` for the [fake-claude…
- [Attach Detach Pattern (`attach_detach_test.go`, #127)](e2e-harness-attach-detach-pattern-attach-detach-test-go.md) — `TestE2E_Attach_DetachesCleanly` drives the documented `Ctrl-B d` sequence (bytes `0x02 0x64`) into a live attach session and asserts the…
- [Attach Restart Pattern (`attach_restart_test.go`, #128)](e2e-harness-attach-restart-pattern-attach-restart-test-go.md) — `TestE2E_Attach_SurvivesClaudeRestart` is the load-bearing proof of the supervisor's restart loop: an attached `pyry attach` client remains…
- [Attach SIGWINCH Pattern (`attach_pty_test.go`, #126)](e2e-harness-attach-sigwinch-pattern-attach-pty-test-go.md) — `TestE2E_Attach_HandlesSIGWINCH` proves the full live-resize chain end-to-end at the binary boundary: a real `pty.Setsize` on the harness's…
- [Rotation Watcher Pattern (`rotation_test.go`, #120)](e2e-harness-rotation-watcher-pattern-rotation-test-go.md) — `TestE2E_RotationWatcher_DetectsClear` is the consumer for #122's fake-claude binary and #123's `StartRotation` primitive. 
- [Stdio-Attach Harness Pattern (`attach_stdio.go`, `attach_stdio_test.go`, #161)](e2e-harness-stdio-attach-harness-pattern-attach-stdio-go-att.md) — The PTY harness (`AttachHarness`, #125) drives `pyry attach` against a controlling terminal. 
- [Foreground Auto-Attach Harness Pattern (`auto_attach.go`, `auto_attach_happy_test.go`, #163)](e2e-harness-foreground-auto-attach-harness-pattern-auto-atta.md) — The stdio-attach harness drives `pyry attach --stdio <uuid>`. 
- [Foreground Auto-Attach Fallback Pattern (`auto_attach_fallback_test.go`, #164)](e2e-harness-foreground-auto-attach-fallback-pattern-auto-att.md) — The happy-path test (#163) pins that auto-attach fires when the daemon hosts the requested UUID. 
- [Stream Interactive Harness Pattern (`StartStreamInteractiveWithRelay`, #1141)](e2e-harness-stream-interactive-harness-pattern-startstreamin.md) — First e2e opt-in to the stream-json `interactive_runner` (#1081 shipped the production toggle; #1140 shipped the stream-json fakeclaude…
