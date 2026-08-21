# #1634 — Prove a real claude's announced model reaches the daemon's own emitted frame

**Size:** S (PO's label, not overridden). One new test file under `internal/e2e/realclaude`.
**Production source files created or modified: 0.** **Shared test files edited: 0.**

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`, then Read the
enclosing declaration.

| File | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/harness_daemon_test.go` | `spawnBootstrapDaemon` | The exact argv, env, stderr tee and `waitForReady` sequence your verbose variant transcribes. Note `--model haiku` and `-pyry-idle-timeout=0` in the args block. |
| `internal/e2e/realclaude/harness_daemon_test.go` | `bootstrapDaemon`, `lockedBuffer` | The daemon handle and the mutex-guarded stderr buffer AC-4 reads. Both are reused as-is. |
| `internal/e2e/realclaude/harness_daemon_test.go` | `seedBootstrapRegistry`, `seedBoundConversation`, `readPersistedServerID`, `waitBinaryHello`, `runPyry`, `decodePairPayload`, `driveHandshakeInteractive`, `sealSendMessage`, `relayTestLogger` | Every rig helper this test needs already exists here. Write none of them again. |
| `internal/e2e/realclaude/interactive_stream_liveness_test.go` | `TestInteractiveStreamLiveness` | The end-to-end drive sequence to copy: pair → seed → fakerelay → spawn → serverID → `waitBinaryHello` → `fakephone.Dial` → handshake → `sealSendMessage` → drain. |
| `internal/e2e/realclaude/interactive_stream_liveness_test.go` | `drainForCompletedTurn`, `writeStreamInteractiveConfig` | The M1/M2 milestone drain shape and its in-order noise-decrypt discipline; the runner pin. |
| `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` | `spawnBootstrapDaemonWithIdle` | **The precedent this spec follows for the spawner.** Read its doc comment — it states why a self-contained near-copy beats a signature change to the shared spawner. |
| `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` | `drainForResumedTurnText` | The house idiom for forking `drainForCompletedTurn`: it drops the two sentinel arms. Yours does too. |
| `internal/e2e/realclaude/interactive_stream_inband_model_test.go` | `inbandModelTargets`, `inbandModelTarget` | AC-2's equality comparand. Its doc comment already states what a red means and that the row must be updated rather than weakened — your failure message restates that, it does not invent it. |
| `internal/e2e/realclaude/interactive_stream_liveness_test.go` | `drainForCompletedTurn`'s `TypeUnrecognizedMessage` and `TypeRateLimited` arms | The prose style AC-2 asks both failure messages to match: name the legitimate non-daemon cause before blaming the daemon. |
| `internal/e2e/realclaude/fixtures.go` | `ensurePyryBuilt` | **Load-bearing for AC-3.** It runs a plain `go build`; it honours `PYRY_E2E_BIN`. See § Mutation route. |
| `internal/e2e/realclaude/resilience_test.go` | `resolveClaudeBin` | The `t.Skip`-on-absent-binary guard (`interactive_stream_inband_model_test.go` uses it; the liveness test open-codes `exec.LookPath`). Either is fine; prefer this one. |
| `internal/protocol/interactive.go` | `ModelAnnouncedPayload` | Three fields: `ConversationID`, `Model`, `Truncated`. |
| `internal/protocol/codes.go` | `TypeModelAnnounced` | The envelope type to switch on. |
| `internal/turnbridge/outbound.go` | `MapEvent`'s `turnevent.ModelAnnounced` case | Mutant 1's target. Read the arm so you delete exactly it. |
| `cmd/pyry/interactive_turn_v2.go` | `interactiveTurnEmitterV2.Handle`'s `turnevent.ModelAnnounced` case | Mutant 2's target. **Not** `eventKind`'s same-named case in the same file — that one stays. |
| `cmd/pyry/main.go` | `selectInteractiveRunner` | Confirms the empty config default selects the stream runner since #1348. |
| `docs/knowledge/features/e2e-realclaude.md` | § on `interactive_stream_resume_after_eviction_test.go` (#1177) | Documents the zero-shared-file-merge-surface discipline and the ticket-encoded-UUID convention this file follows. |

## Context

`turnevent.ModelAnnounced` now has a complete path to a client: the parser mints it
(`streamsup.Parser.emitModelAnnounced`, #1600), `turnbridge.MapEvent` maps it to
`protocol.TypeModelAnnounced` (#1638), and `interactiveTurnEmitterV2.Handle` pushes it (#1638).
Every test on that path is hermetic — each feeds a constructed `turnevent.ModelAnnounced` and reads
what one link produced.

The existing live test, `TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel`, taps
`streamsup.Config.Stdout`, which is **upstream of the parser**. It was green for the whole period
when `MapEvent` dropped the variant and no frame existed, and it would stay green if #1638's two
arms were reverted. Nothing live guards them.

This ticket adds the one live test that does: read the assertion off the daemon's own emitted frame,
decoded at a connected fakephone.

No ADR is warranted — this adds a test to an established live-proof family and introduces no new
decision.

## Design

### One file, no shared-file edits

Everything lands in a new `internal/e2e/realclaude/interactive_stream_model_announced_test.go`
behind `//go:build e2e_realclaude`. Nothing outside it is created or modified.

Ticket-encoded fixed UUIDs (the single-char-repeat stems are exhausted; see § "Ticket-encoded fixed
UUIDs" in the package overview). Both are free as of 2026-08-20:

- bootstrap pool id: `16340000-0000-4000-8000-000000000001`
- driving conversation: `16340000-0000-4000-8000-000000000002`

### The spawn alias is one constant, used four ways

Declare `announcedSpawnAlias = "haiku"` once. It is:

1. the value after `--model` in the verbose spawner's argv,
2. the key looked up in `inbandModelTargets`,
3. the comparand in AC-2's inequality,
4. the alias named in both failure messages.

Four literals could drift apart silently and leave the inequality comparing the frame against a
string the daemon was never spawned with — a vacuous pass. One constant makes that impossible.

### `spawnBootstrapDaemonVerbose` — a local near-copy, not a shared-spawner change

Signature mirrors `spawnBootstrapDaemon` exactly; the sole delta is `-pyry-verbose` added to the
pyry-flag block, **before** the `--` separator. `cmd/pyry`'s `main` maps that flag to
`slog.LevelDebug`, which is the level AC-4's haystack requires: both plausible leak sites for this
value — `streamsup.Parser.emitModelAnnounced`'s undecodable-line drop and
`interactiveTurnEmitterV2.Handle`'s `interactive_turn.unknown` default — log at Debug, so at
`LevelInfo` their absence from stderr proves nothing.

Placement before `--` is required and is pinned independently by `cmd/pyry`'s `args_test.go` table
(the row for `{"-pyry-verbose", "--", "--model", "sonnet"}` routes the flag to pyry; the row without
`--` routes it to claude).

It reuses `bootstrapDaemon`, `shortSocketPath`, `ensurePyryBuilt`, `lockedBuffer` and
`waitForReady` — same-package, no copies of those.

**Why a near-copy and not a variadic option on the shared `spawnBootstrapDaemon`.** The variadic is
~25 lines smaller and cannot change existing call sites. It was rejected anyway: it restructures the
argv assembly inside a function thirteen live tests depend on, and **no hermetic gate covers this
package** — `make check` never compiles it, so a restructure error surfaces only as thirteen failing
live runs. `spawnBootstrapDaemonWithIdle` established the near-copy precedent for exactly this
reason and its doc comment says so; the package overview records the same rationale. Twenty-five
lines does not buy that risk. Do not "improve" this into a shared-spawner refactor.

### The runner pin

Call `writeStreamInteractiveConfig(t, home)` before the spawn, exactly as
`TestInteractiveStreamLiveness` does.

It is currently redundant — `selectInteractiveRunner` maps the empty default to the stream runner
since #1348, and the isolated HOME has no config file — but keep it. The default has already moved
once, this is one line, and the failure mode it guards against is a silent multi-minute drain
timeout on a live run. Say in the comment that it is a pin rather than a requirement, so a future
reader does not infer the default is still PTY.

### `drainForAnnouncedModel` — capture on the way to the turn's close

A lean fork of `drainForCompletedTurn`, following `drainForResumedTurnText`'s precedent (which drops
the `unrecognized_message` / `rate_limited` sentinel arms; so does this one — those sentinels live in
the shared drain and are not re-implemented per fork).

Contract:

```
func drainForAnnouncedModel(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
    convID string, timeout time.Duration) (protocol.ModelAnnouncedPayload, bool)
```

Behaviour, one line: decrypts binary→phone frames in receive order, records the **first**
`protocol.TypeModelAnnounced` payload it sees, and returns it (plus whether one was seen) when the
turn closes with `turn_state{idle}` for `convID`; `t.Fatalf`s on the deadline naming which milestone
was missing.

Three properties that are not negotiable:

- **In-order decrypt.** The receive nonce is sequential. Every `TypeNoiseMsg` frame must be
  decrypted in arrival order or the `CipherState` desyncs; non-`noise_msg` control frames (rekey)
  are skipped **without** decrypting. Copy this discipline verbatim from `drainForResumedTurnText` —
  it is the one thing in the loop that is easy to get subtly wrong and hard to debug live.
- **Return at turn close, not at the deadline.** Under either mutant no frame is emitted, so a
  drain that waited for the frame would burn its full timeout on every red run. Returning at
  `turn_state{idle}` makes each mutant run RED in seconds instead of minutes — the difference
  between AC-3 costing one live-suite budget and costing three.
- **No conversation filter on the `model_announced` arm.** Take the first frame of that type
  whatever its `ConversationID`, and `t.Logf` the id you got. `Handle` derives it from
  `sup.CurrentConversation()`, so it will be the driving conv in practice, but asserting it is
  outside every AC and a filter that guessed wrong would hang the drain instead of failing loudly.
  The `turn_state` arm keeps its `convID` filter — that one is the turn boundary.

Milestone messages follow `drainForCompletedTurn`'s: name the likely cause, and name a UUID mismatch
between `seedBootstrapRegistry` and `seedBoundConversation` as the first thing to check.

### Data flow being proven

```
real claude  --system/init line-->  streamsup.Parser.emitModelAnnounced
             --turnevent.ModelAnnounced-->  turnbridge.MapEvent          <-- mutant 1
             --protocol.TypeModelAnnounced--> interactiveTurnEmitterV2.Handle  <-- mutant 2
             --sealed InnerFrameV2--> fakerelay --> fakephone --> the assertion
```

Every arrow except the first two is already exercised by the sibling live tests; the two mutants sit
on exactly the arrows nothing live guards today.

### Concurrency model

No goroutines are introduced. The three that exist are inherited and already owned:

- `os/exec`'s stderr copier, writing into `lockedBuffer` while the test goroutine reads it — the
  mutex is why `lockedBuffer` exists.
- `spawnBootstrapDaemon`'s `cmd.Wait` goroutine closing `doneCh`, consumed by `waitForReady` and
  `stop`.
- `fakerelay` / `fakephone` internals.

Shutdown: `t.Cleanup` in reverse order — phone close, daemon `stop` (SIGTERM → 3s → SIGKILL),
fakerelay close. Register each immediately after the resource is created, so a `t.Fatalf` anywhere
downstream still tears the daemon down.

### Error handling

Every failure is a `t.Fatalf`/`t.Errorf` with the cause named. The two AC-2 assertions are
`t.Errorf`, not `t.Fatalf`, so a single run reports both colours — that is the whole point of the
pair (see § Assertions).

## Testing strategy

One test: `TestInteractiveStreamModelAnnouncedFrame`.

### Drive sequence

- `resolveClaudeBin(t)` then `WithWorktreeAuthenticated(t)` — both skip cleanly without a binary or
  credentials. No `t.Parallel` (the latter calls `t.Setenv`).
- Create an isolated workdir under the authenticated HOME.
- `writeStreamInteractiveConfig(t, home)`.
- `runPyry(t, "pair", "-pyry-name=test", "--name=phone-a")` → `decodePairPayload` → base64-decode
  `ServerStaticPubkey`.
- `seedBootstrapRegistry` + `seedBoundConversation` with the two ticket-encoded UUIDs, **before**
  the spawn (the registry loads once at startup).
- `fakerelay.New(relayTestLogger())`, cleanup registered.
- `spawnBootstrapDaemonVerbose(...)`, cleanup registered.
- `readPersistedServerID` → `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeInteractive`.
- `sealSendMessage` one turn: a short deterministic instruction with a per-run nonce
  (`time.Now().UnixNano()`) to defeat caching. Nothing is asserted about the reply's content.
- `drainForAnnouncedModel(..., 120*time.Second)`.

The announcement is **once per turn**, not once per session — drive one turn and read the frame from
inside it. Do not latch a value and compare it across turns. (If claude also emits an init line at
child spawn, that one lands before the phone has handshaken and before `Handle` has a conversation
cursor, so it is dropped at the `interactive_turn.no_cursor` guard and cannot be what you drain.)

### Assertions

Scenarios, not code:

- **AC-1 — a frame arrived.** `drainForAnnouncedModel` returned `seen == true`. On `false`, fail
  naming the two mutants' symptom: `MapEvent` mapped nothing, or `Handle` dropped the event. This is
  the assertion both mutants must RED.
- **AC-2 equality.** `payload.Model == announcedTargetFor(t, announcedSpawnAlias)`, where the helper
  looks the alias up in `inbandModelTargets` and `t.Fatalf`s if the row is missing (a broken
  instrument, not a skip). Failure message must name **"the measured `inbandModelTargets` row is
  stale for this claude version"** as a cause distinct from a daemon bug, and must say to update the
  row rather than weaken the assertion to a substring match — the same instruction that row's own
  doc comment already carries.
- **AC-2 inequality.** `payload.Model != announcedSpawnAlias`. Failure message must name **"claude
  now announces the bare alias"** as its legitimate cause, and say what that would mean: the daemon
  can no longer report anything more specific than what it asked for, which is worth a human look
  rather than a daemon fix. Document in-file that this assertion is never the sole red under the
  equality and is kept deliberately as the **discriminator** between a stale row (inequality green)
  and a claude regression (inequality red) — the same A1/A2 relationship
  `TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel` documents and maintains.
  Both are `t.Errorf` so one run reports both colours.
- **AC-4 — no leak.** After the drain returns, snapshot `d.stderr.String()` and assert
  `!strings.Contains(stderr, payload.Model)`. Search for the **frame's own value**, never the alias:
  `spawning claude` logs the argv, which contains `--model haiku`, so searching the alias would fail
  on a healthy daemon. On failure, print the matching context and name the `#833` posture
  (`handleRequestSessionSettings`, `internal/relay`'s `v2session_settings.go`, `internal/sessions`'
  `pool.go`) as what regressed.

  Two things make this non-vacuous, and both belong in the comment: the drained frame on the **same
  run** proves the value existed and the daemon held it (AC-1 is the control), and `-pyry-verbose`
  makes the haystack one that would actually carry a Debug-level leak. The child's own stderr is not
  a confound — `mapStreamsupConfig` leaves `streamsup.Config.Stderr` nil and the runner assigns that
  straight to `cmd.Stderr`, so claude's stderr is discarded and the buffer holds the daemon's slog
  output only. Verified 2026-08-20; no production site logs a model value at any level.

### AC-3 — the mutation route

**`go test -overlay=…` does not work here, and using it produces a false green.**

This test asserts against a **separately built daemon binary**. `ensurePyryBuilt` shells out to a
plain `go build` with no overlay forwarding, and mutant 2 lives in `cmd/pyry`, which the test binary
never compiles at all. An overlay passed to `go test` reaches neither mutant.

The route that works — `ensurePyryBuilt` returns `$PYRY_E2E_BIN` unbuilt when it is set:

```bash
# 1. overlay.json maps the mutated file to a temp copy (absolute paths, no worktree write)
go build -overlay=/abs/path/overlay.json -o /tmp/pyry-mutant ./cmd/pyry
# 2. point the harness at it
PYRY_E2E_BIN=/tmp/pyry-mutant go test -tags e2e_realclaude -race -v \
  -run TestInteractiveStreamModelAnnouncedFrame ./internal/e2e/realclaude/
```

`ensurePyryBuilt` refuses a `PYRY_E2E_BIN` that resolves to the test binary itself (the 2026-05-16
fork-bomb guard); a `/tmp` path is fine.

The two mutants, each of which compiles because the deleted case falls through to an existing
`default`:

| # | Delete | Falls through to | Expected |
|---|---|---|---|
| 1 | `MapEvent`'s `case turnevent.ModelAnnounced:` in `internal/turnbridge/outbound.go` | `default: return "", nil, false` → `emitMapped` logs `interactive_turn.unmapped` at Debug | no frame → AC-1 RED |
| 2 | `Handle`'s `case turnevent.ModelAnnounced:` in `cmd/pyry/interactive_turn_v2.go` | the `interactive_turn.unknown` Debug default | no frame → AC-1 RED |

Mutant 2's target is the case inside `interactiveTurnEmitterV2.Handle`. The **same file's `eventKind`
has a case with the same name** — leave it alone; deleting that one only changes a log field and the
test would stay green, which would look like a failed discriminator and is not.

Record all three runs (green + two mutants) as a compressed § Evidence block in the test file's
header, following `interactive_stream_inband_model_test.go`'s § Evidence idiom: per run, the tree
identity, the drained model value or its absence, which assertions were red, and wall clock. Three
short paragraphs, not a narrative — the #1600/#1616/#1638 history is already written up in that
file's corrected header and must not be restated here.

### Reading the suite

This package is behind `e2e_realclaude`; `make check` never compiles it. Read the **count of
executed tests**, never the exit code — a package that fails to build and a package that skips
everything both exit 0. `make preship` is the gate that proves it builds.

## Open questions

- **Does claude emit an init line at child spawn as well as per turn?** #1582's measurement (3 init
  lines over 3 turns) is consistent with either reading. The design is robust either way: a
  spawn-time announcement is dropped at `Handle`'s no-cursor guard, and the driven turn produces the
  frame the test drains. If the drain reports zero `model_announced` frames on an otherwise healthy
  turn (deltas and `turn_state{idle}` observed), that assumption is where to look first — record what
  you find in the header rather than changing the assertion.
- **The frame's `ConversationID` is logged, not asserted.** If it turns out to be empty or to differ
  from the driving conv on a real daemon, that is a finding worth a follow-up ticket, not something
  this test should start asserting.

## Scope

Do **not** touch the mapping, `ModelAnnouncedPayload`, `docs/protocol-mobile.md`, any stale-claim
site, `harness_daemon_test.go`, or `interactive_stream_inband_model_test.go`. #1638 (landed) and
#1639 (landed) own those. This ticket adds one test file and nothing else.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The design adds no trust boundary. It *observes* the existing
  one: claude's stdout crosses untrusted→trusted at `streamsup.Parser`, and the value's treatment
  after that is decided by `emitModelAnnounced` (bounds it at `maxModelField`, sets `Truncated`) and
  by `MapEvent`'s `turnevent.ModelAnnounced` arm (crosses it byte-for-byte, no re-cap, no charset
  check). Both are pre-existing and out of scope. The one thing worth naming: the frame's `Model` is
  **claude-controlled data reaching a test assertion**, so a test that pattern-matched it into
  something executable would be a new boundary. The design keeps it in two comparisons
  (`==` against a table row, `!=` against a constant) and one `strings.Contains` haystack search. No
  regexp is compiled from it, no path is built from it, nothing is executed with it.
- **[Tokens, secrets, credentials]** No findings. The pairing token comes from `runPyry("pair", …)`
  and reaches only `driveHandshakeInteractive` and `buildHelloEarlyInteractive`, both existing. The
  test mints no token, stores none, and prints none. `WithWorktreeAuthenticated` reads the
  operator's credential from the outer environment and re-pins it under an isolated HOME; the test
  neither reads nor logs it.
- **[File operations]** No findings. Every path is framework-owned: `t.TempDir()`-rooted HOME,
  `shortSocketPath`'s `os.MkdirTemp("/tmp", …)`, and the two seed writers at `0600` under a `0700`
  directory. No caller-controlled path is concatenated, no check-then-use on a path an attacker
  could swap, no symlink following introduced. The mutant binary at `/tmp/pyry-mutant` is written by
  the operator's own `go build` during evidence gathering, never by the test.
- **[Subprocess execution]** SHOULD FIX (documentation, not code). The test spawns the daemon via
  the existing `exec.Command(bin, args...)` shape — argv slice, never `sh -c`, so nothing is
  shell-interpreted; `--model haiku` is a compile-time constant, not caller input. The one thing to
  say out loud in the spec is already said in § Mutation route: `PYRY_E2E_BIN` makes `ensurePyryBuilt`
  return an **operator-supplied binary path unbuilt**, which is an intentional escape hatch guarded
  only against self-reference. That is pre-existing and is exactly what AC-3 relies on; the developer
  must not widen it, and must not leave `PYRY_E2E_BIN` set for the green run — a green recorded
  against a stale mutant binary would be the worst possible outcome of this ticket. Stated in the
  spec; code review should check the recorded green run was taken with `PYRY_E2E_BIN` unset.
- **[Cryptographic primitives]** No findings. The Noise_IK handshake, the X25519 keygen
  (`ecdh.X25519().GenerateKey(rand.Reader)` — `crypto/rand`), and the sequential receive-nonce
  discipline are all inherited verbatim from `driveHandshakeInteractive` and the existing drains. The
  design adds no primitive and no key. The in-order-decrypt requirement is called out in § Design
  precisely because a fork that reorders frames breaks the CipherState — a correctness hazard in a
  crypto path, which is why it is a constraint and not a suggestion.
- **[Network & I/O]** No findings. The daemon is spawned with `PYRY_ALLOW_INSECURE_RELAY=1` against a
  loopback `fakerelay`, matching all thirteen sibling live tests; no real relay, no external network.
  The drain is bounded by an explicit 120s deadline and `fakephone.ErrReceiveTimeout`, so no read is
  unbounded in time. Input size: the only accumulator this test owns is `lockedBuffer`, which grows
  with the daemon's own Debug-level stderr for the life of one turn — bounded in practice by the
  turn, and the same buffer `spawnBootstrapDaemonWithIdle` already fills at Info. Worth one sentence
  in the file, not a cap.
- **[Error messages, logs, telemetry]** **This is the category the ticket exists for, and it has one
  real finding — MUST-FIX-shaped, and the design already addresses it.** AC-4's failure path prints
  the leak context, and the naive form of that message would print the daemon stderr region
  containing the model — which is fine (the value is already in the test's hands and in the test
  log). But the *assertion input* matters: searching for `announcedSpawnAlias` instead of
  `payload.Model` would fail on every healthy daemon, because `spawning claude` logs the argv
  containing `--model haiku`. That is named explicitly in § Assertions AC-4 and in the constant's
  four uses. Second: raising the daemon to `-pyry-verbose` **widens what the test's own output
  carries** — Debug-level daemon stderr is teed to `os.Stderr` by the existing spawner, so a verbose
  live run prints more daemon internals into CI output than an Info run. Verified 2026-08-20 that no
  production site logs a model value at any level, and that the Debug records on this path
  (`streamsup: dropping undecodable system line`, `interactive_turn.unknown`,
  `interactive_turn.unmapped`) carry no claude-derived payload — `emitModelAnnounced` deliberately
  omits the `json.Unmarshal` error for exactly this reason. So the widening is bounded and known.
  Not a MUST FIX, but the developer must not add a stderr dump to any *passing* path.
- **[Concurrency]** No findings. No new goroutine, no new lock. The one shared-state read —
  the test goroutine reading `lockedBuffer` while `os/exec`'s copier writes it — is exactly what that
  type's mutex exists for and what `waitForIdleEvictionWARN` already does. `t.Cleanup` ordering is
  specified in § Concurrency model. AC-4's read is a deliberate point-in-time snapshot taken after
  the drain returns; it is not a check-then-mutate.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model's relevant
  threat here is the daemon leaking session configuration to a persistent log a client never sees —
  #833's posture. This ticket *tests* that posture against a whole real daemon rather than changing
  it. Out of scope and named as such by the ticket: interposing a tee on the claude binary via
  `-pyry-claude=` for a same-run cross-tier comparand (changes the child process tree and the pid the
  daemon reaps).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
