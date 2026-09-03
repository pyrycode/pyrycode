# #1673 — the per-arm Pool revoke harness, and the three arms' pre-spawn identity, posture and argv

One new offline file in `internal/e2e/realclaude`, one appended `finOfflineExecBans`
entry, two stale doc-comment corrections. Zero production files.

## Files read

- `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go` →
  `seedBypassRegistry`, `revokeBaseArgs`, `revokeTap`/`newRevokeTap`,
  `revokeLogHandler`/`newRevokeLogRecorder`,
  `TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode` — the whole
  rig this ticket generalises; its factory and its instrument checks A and B are the
  shape lifted. Also the two stale doc comments this PR corrects.
- `internal/e2e/realclaude/inband_bypass_revoke_arms_test.go` → `poolRevokeArms`,
  `poolRevokeArm` — the three rows, read-only by contract (range is the only
  supported access).
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` and
  #1651's entry — the six-name shape AC 5's entry copies.
- `internal/e2e/realclaude/interactive_stream_inband_model_test.go` → `inbandRunner`
  and its `var _ sessions.Runner` assertion — the runner wrapper the factory returns.
- `internal/sessions/pool.go` → `New` — composes `base`, `bootstrapArgs` and the whole
  `RunnerConfig`, then runs the factory, all before any spawn; `Default`,
  `DefaultSettings`, `BootstrapID` — the three pre-spawn readings.
- `internal/sessions/session.go` → `claudeSettingsArgs` (the unconditional escalation
  flag since #2065, and the `--permission-mode` pair gated on `!YOLO`),
  `canonicalSettings` / `canonicalPermissionMode` (a stored `{yolo:false}` becomes
  `default`), `operatorBypass` (reads the settings-free base), `Session.Runner`.
- `internal/sessions/registry.go` → `settingsFromEntry`, `loadRegistry` — the warm
  start; `(nil, nil)` for an absent file is why a cold start is indistinguishable from
  a seeded `control_default` at the posture read.
- `internal/sessions/runnerstate.go` → `RunnerConfig.PermissionMode` and
  `RunnerConfig.OperatorBypass` — the two fields AC 3 pins, and `OperatorBypass`'s own
  doc block ("TRUE means the daemon may NOT walk this child back") is why a false is
  the load-bearing reading.
- `internal/sessions/settings.go` → `writeMCPSettings` — writes
  `<abs(dir(registryPath))>/session-settings/<id>.json`, deriving the directory with
  `filepath.Abs` and no symlink resolution.
- `internal/streamsup/runner.go` → `New` — the three preconditions (`ClaudeBin`
  resolvable, non-empty `SessionID`, existing `WorkDir`) plus the
  `SpawnPermissionMode`-requires-`PostureGate` refusal; `Run` is where the spawn is,
  and it is never called here.
- `internal/agentrun/workdir.go` → `ResolveWorkdir` — `EvalSymlinks`, which is the
  other half of the macOS `/var` vs `/private/var` trap.
- `cmd/pyry/streamsup_runner.go` → the production factory, which does wire
  `SpawnPermissionMode` and a parser-minted `PostureGate`. See Open questions.
- `docs/knowledge/features/e2e-realclaude-inband-bypass-revoke-arms-test-go.md` —
  #1651's, #1661's and #1662's lessons. The one that changes this design: **a nested
  assertion is never the sole red for a mutant that trips its guard.** Assertions
  below are flat and independent wherever the readings are independent.

## Context

#1675 compares three arms' tool-gating behaviour against a live claude, at 8–14
minutes and real tokens per run. Everything that comparison needs which can be
settled with no child process is this ticket. The seam moved under the original body:
since #2065 `claudeSettingsArgs` appends `--dangerously-skip-permissions`
unconditionally, so the launch argv no longer says which posture a child runs in —
`RunnerConfig.PermissionMode` and `RunnerConfig.OperatorBypass` carry that now, and
they are readable before the factory returns.

No ADR is warranted: this adds no production behaviour and establishes no new
convention beyond what #1651/#1661/#1662 already recorded for this family.

## Design

### `poolRevokeHarness` — the returned observation surface

A package-level struct, not exported (this package has no consumers outside itself).
Fields, all set by the constructor:

| Field | Type | Why a later driver needs it |
|---|---|---|
| `arm` | `poolRevokeArm` | the row this harness was built from, so a caller need not carry it separately |
| `pool` | `*sessions.Pool` | the live pool; `Pool.Run` is never called |
| `runner` | `inbandRunner` | the captured runner, concrete rather than `sessions.Runner` so #1675 reaches `Stdin()` |
| `runnerCfg` | `sessions.RunnerConfig` | the whole config the factory received — AC 3's subject |
| `tap` | `*revokeTap` | the stdout sink |
| `spawns` | `func() int` | log recorder's spawn counter |
| `notDelivered` | `func() []string` | log recorder's swallowed-delivery reader |
| `registryPath` | `string` | AC 2's and AC 4's subject |
| `seededID` | `sessions.SessionID` | what `seedBypassRegistry` returned — AC 2's identity reading |

### `newPoolRevokeHarness(t *testing.T, arm poolRevokeArm, claudeBin, workdir string) poolRevokeHarness`

Signature contract: the row, plus exactly the two values a live consumer must supply.
Both are parameters rather than package consts because #1675 passes
`resolveClaudeBin(t)` and a workdir under `WithWorktreeAuthenticated`'s pinned `$HOME`
into them — names this file may not itself reach (AC 5).

Sequence, in order:

1. `registryPath := filepath.Join(t.TempDir(), "sessions.json")` — a fresh directory
   per call, which is what makes AC 4's distinctness hold by construction and what
   `seedBypassRegistry`'s "hand it a fresh path" doc requires.
2. `seededID := seedBypassRegistry(t, registryPath, arm.launchYOLO)` — before
   `sessions.New`, because the warm-start seam reads the file at construction.
   The launch posture reaches the pool through this entry and nowhere else: the base
   argv stays `revokeBaseArgs`, which is empty by design.
3. `tap := newRevokeTap()`; `logHandler, spawns, notDelivered := newRevokeLogRecorder()`.
4. A factory closure that calls `streamsup.New` with `ClaudeBin`, `WorkDir`,
   `SessionID` and `Args` off the `RunnerConfig`, `Stdout: tap`, `Logger: cfg.Logger`
   — byte-for-byte #1622's factory — and, before returning, records **both** the
   `inbandRunner` it built and the whole `sessions.RunnerConfig` it received into
   closure variables. The factory runs synchronously inside `sessions.New`, so the
   captures need no lock.
5. `sessions.New` with `Bootstrap: sessions.SessionConfig{ClaudeBin, WorkDir,
   ClaudeArgs: revokeBaseArgs}`, `RegistryPath: registryPath`, `RunnerFactory`,
   `Logger: slog.New(logHandler)`. A non-nil error is `t.Fatalf`.
6. **Instrument check A**, for every arm, with its own message: `pool.Default().Runner()`
   must equal the captured runner. Unchecked, the harness can hand a later driver a
   bystander while every reading in AC 2 and AC 3 still passes, because none of them
   consult the runner.

`Pool.Run` is not called, so no goroutine is started and no settings-file reaper runs;
the per-arm settings file is left under that arm's own `t.TempDir()`.

### `newInertClaudeStub(t *testing.T) (claudeBin, workdir string)`

This file's own offline supplier for the constructor's two parameters. One
`t.TempDir()` serves both: an empty file written at `0755` (`exec.LookPath` accepts
any regular file with `mode&0111 != 0` on a path containing a separator, so it
resolves under any umask), and the same directory as the workdir, which exists by
construction and so survives `ResolveWorkdir`'s `EvalSymlinks`. Never executed —
`Runner.Run` is the only exec site and nothing calls it. Deliberately not the test
binary: this package carries a fork-bomb defence against exactly that shape in
`resolveClaudeBin`.

### `revokeArgvFlagIndices(args []string, flag string) []int`

One pure helper backing every AC 3 argv clause: exactly-one, present, absent, and
value-of-the-following-element are all expressed from the index set. Assertions read
off it flatly rather than nesting, per #1651's mutation lesson.

### The test — `TestPoolRevokeHarness_PinsSeededPostureAndArgvBeforeAnySpawn`

`t.Parallel()` at the top level, sequential subtests below it. All three harnesses are
built in the outer scope into a slice, which is what lets the isolation subtest hold
three readings at once (AC 4). Then:

- `t.Run(arm.name, …)` per row — AC 2's two readings and AC 3's clauses.
- `t.Run("the three arms are mutually isolated", …)` — AC 4's pairwise comparison.

#### AC 2 — two independent assertions, each with its own message

- `pool.BootstrapID() == seededID`. A red here is a seed the pool never read. On
  `control_default` this is the **only** one of the two that separates a working seed
  from a cold start, because `loadRegistry` returns `(nil, nil)` for an absent file and
  the cold-start path reaches `canonicalSettings` with a zero `SessionSettings`,
  yielding the same `{YOLO:false, PermissionMode:"default"}` a correct seed does. A
  cold start mints a fresh id, so the identity reading cannot collide.
- `DefaultSettings()` reports `ok`, `YOLO == arm.launchYOLO`, and `PermissionMode ==
  bypassPermissions` on a YOLO row / `default` otherwise. A red here is a seed the pool
  read carrying the wrong value.

Each message names the arm, the registry path and both readings.

#### AC 3 — the pre-spawn posture the factory received

Over `runnerCfg.ClaudeArgs`, by shape only; the settings path is per-arm and per-run,
so no whole-slice literal is ever compared.

- exactly one `--settings`, and `filepath.Dir` of the element after it equals
  `filepath.Join(abs(filepath.Dir(registryPath)), "session-settings")`. Built from the
  registry path and from nothing the runner reports back — `writeMCPSettings` uses
  `filepath.Abs` with no `EvalSymlinks` while the workdir goes through `EvalSymlinks`,
  so on macOS the same `t.TempDir()` spells itself two ways. And it lands one level
  *deeper* than the registry directory, so a "parent equals the registry dir" check is
  red on all three arms for the wrong reason.
- `--dangerously-skip-permissions` present, asserted **unconditionally on every arm**,
  `control_default` included, with a message naming #2065 so a later reader does not
  "correct" it back to an iff-`launchYOLO` form.
- `--permission-mode` followed by `default` iff `!arm.launchYOLO`; no `--permission-mode`
  at all on the two YOLO rows.
- none of `--permission-prompt-tool`, `--mcp-config`, `--strict-mcp-config`, `--model`,
  `--effort`.
- `runnerCfg.PermissionMode` equals that row's canonical posture.
- `runnerCfg.OperatorBypass` is **false** on all three, each with its own message
  quoting the field's stakes: a true would leave #1675's `revoke` arm unrevocable while
  every other reading stayed green.

#### AC 4 — mutual isolation

Pairwise-distinct across the three harnesses: bootstrap ids, registry paths, and the
composed `--settings` path (read back through the same index helper). Reported per
colliding pair rather than as one aggregate, so a red names which two arms share what.

### AC 5 — the offline entry

`finOfflineExecBans` gains one entry for the new file, copying #1651's six names —
`resolveClaudeBin`, `WithWorktreeAuthenticated`, `WithWorktree`, `probeClaudeVersion`,
`os.Getenv`, `os.Environ` — for the reason that entry states: the first four skip
*inside* the test body, after `=== RUN` prints, and a skip exits 0, which reads as a
pass under `make e2e-realclaude`. `t.TempDir` stays available on purpose: the seed
writes a `sessions.json`, and the stub and workdir need somewhere to live. #1661's and
#1662's wider entries are **not** copied — their `packageDir`/fixture/`os` read-write
bans fence off the committed `testdata/`, and this file neither reads nor writes a
fixture, so those bans would defend against a failure mode it cannot have.

### The two stale doc-comment corrections

Not acceptance criteria, and deliberately not grown into any. Both assert the inverse
of the truth since #2065, in a file this ticket already reads:

- `seedBypassRegistry`'s doc says `claudeSettingsArgs` "turns true into
  `--dangerously-skip-permissions` and false into nothing at all."
- the same file's header and its `revokeBaseArgs` comment reason from the flag's
  *absence* being observable.

Corrected in place; the surrounding claims (the base argv must stay empty; the stored
posture is the only source of the launch posture) are unaffected and stay.

## Concurrency model

No goroutines. `Pool.Run` is not called, so no session lifecycle goroutine, rotation
watcher or conversations sweep exists; `Runner.Run` is not called, so no child and no
supervise loop. The factory closure's two captures are written on the goroutine that
calls `sessions.New` and read after it returns, on the same goroutine — no
synchronisation needed, and nothing for `-race` to find. The top-level test declares
`t.Parallel()`; its subtests do not, so all three harnesses stay confined to one
goroutine.

## Error handling

Every constructor step that can fail is `t.Fatalf` with a message naming the arm and
the registry path: `sessions.New` (which surfaces a bad seed, an unresolvable
`ClaudeBin` and an absent `WorkDir` alike), the stub write, and `filepath.Abs`. Per-arm
assertions use `t.Errorf` so one arm's failure does not hide another's, except where a
later read would panic or be meaningless — an absent `--settings` value, or a nil pool
— which `t.Fatalf` inside that arm's subtest.

## Testing strategy

`go vet -tags e2e_realclaude ./internal/e2e/realclaude/`, then
`go test -tags e2e_realclaude -race -count=1 -v -run 'TestPoolRevokeHarness_|TestFinOfflineFilesReachNoExecHelper' ./internal/e2e/realclaude/`.
The count of `=== RUN` lines is the verdict, never the exit code — `make check` never
compiles this package, and a build failure and a full credentials skip both exit 0.
The new tests must appear in that count on a machine with no claude installed and no
credentials, and must report PASS rather than SKIP.

`make check` is deliberately not run: it cannot see this package at all.

## Open questions

1. **Does the factory need `streamsup.Config.SpawnPermissionMode` and `PostureGate`?**
   Production (`cmd/pyry`'s stream factory) sets both — the mode from
   `RunnerConfig.PermissionMode`, the gate minted by the turnevent parser — and
   `streamsup.New` refuses a mode with a nil gate. #1622's factory sets neither,
   because the tap occupies the `Stdout` slot the parser would hold. **Resolved: mirror
   #1622.** Nothing this ticket asserts is affected — every reading is taken from the
   `RunnerConfig` the factory *received*, upstream of what it does with it — and wiring
   a gate would mean minting a parser this harness has no other use for. It is recorded
   in the harness doc comment as a decision #1675 must make deliberately, since that
   ticket's `control_default` is a control by virtue of the spawn-time in-band write
   and not by virtue of its argv.
2. **One test or two?** Resolved: one, with subtests. AC 4 requires the three harnesses
   in one scope; building them twice to split AC 2/3 from AC 4 would double the
   construction for no assertion strength.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The design has exactly one boundary — a
  `sessions.json` on disk crossing into in-memory `SessionSettings` and then into a
  composed argv — and the test *is* the audit of that crossing. Both sides are
  test-controlled: the file is written by `seedBypassRegistry` into a per-arm
  `t.TempDir()`, and the two caller-supplied values (`claudeBin`, `workdir`) enter at
  the same trust level as production's operator CLI (`SessionConfig.ClaudeBin` /
  `WorkDir`). No socket, no relay frame, no subprocess stdout enters: `revokeTap` is
  installed as `streamsup.Config.Stdout` but nothing ever writes to it, because
  `Runner.Run` is never called.

- **[Subprocess execution]** SHOULD FIX — *"no child spawns" is a design property, not
  a mechanically enforced one.* `finOfflineExecBans` bans helper **names**; it cannot
  ban a future `Run` call. What makes the design fail closed is the choice of stub: an
  **empty** file at `0755`. `exec.LookPath` resolves it, so `streamsup.New` is
  satisfied, but an actual exec of a zero-byte file fails immediately with an
  exec-format error — it cannot run anything, and it cannot re-enter the test binary
  the way `os.Args[0]` would (the fork-bomb shape `resolveClaudeBin` already defends
  against). That property is invisible from the call site, so **`newInertClaudeStub`'s
  doc comment must state it in Phase B**: "empty, so an accidental exec fails rather
  than runs" is the sentence that stops a later developer swapping in a real script or
  a `resolveClaudeBin(t)` result. No `sh -c` anywhere; no environment is scrubbed or
  inherited, because no process is created.

- **[Tokens, secrets, credentials]** No findings, and the `os.Getenv` / `os.Environ`
  ban is load-bearing rather than decorative: this process environment carries
  `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`, and a test whose subject is a
  privilege posture is exactly the kind most tempted to dump the ambient environment
  into a failure message. Seeded ids come from `sessions.NewID`, i.e. `crypto/rand`.
  Bounded observation, not a fix: the argv printed in AC 3's failure messages is
  `--settings <tempdir path> --dangerously-skip-permissions [--permission-mode
  default]` and carries no secret today — a future arm carrying a `Model` or `Effort`
  would put a settings value in a *test* log, which is not the daemon log #833 governs,
  but is worth knowing before anyone widens the table.

- **[File operations]** No findings. No user input is concatenated into a path: the
  registry path comes from `t.TempDir()`, and the settings path is derived by
  production code (`writeMCPSettings`) from that path plus an id it gates on `ValidID`.
  No check-then-use, so no TOCTOU. Modes: the seed writes `0600` (already pinned by
  #1651), and the stub is `0755` — mode bits that would matter on a shared path, but
  the file's actual boundary is `t.TempDir()`, which Go creates at `0700` and removes
  at cleanup. Symlinks are followed on the workdir by `ResolveWorkdir`'s `EvalSymlinks`
  and *not* on the settings dir by `writeMCPSettings`' `filepath.Abs`; that asymmetry
  is a correctness trap, not a security one, and AC 3's containment check is built from
  `filepath.Dir(registryPath)` precisely so it never straddles the two spellings.

- **[Cryptographic primitives]** Not applicable by design: this ticket performs no
  comparison against a secret and derives no key. The equality reads it does perform
  (`BootstrapID` against the seeded id, and the pairwise distinctness pass) are over
  non-secret identifiers in a test process, so constant-time comparison is not the
  relevant property — a red is a wiring bug, not an oracle.

- **[Network & I/O]** Not applicable by design: no socket, no HTTP server, no relay
  frame, no read from any descriptor. The one input cap in the design's reach —
  `revokeTap`'s `maxPartial` — is inherited unchanged from #1622 and is never exercised
  here, because nothing writes to the tap.

- **[Error messages, logs, telemetry]** No findings. The log recorder installed as
  `sessions.Config.Logger` drops every record but two message strings, which also keeps
  the runner's lifecycle lines out of test output (a nil logger would fall back to
  `slog.Default()`). Its `notDelivered` reader stays empty here, since nothing calls
  `UpdateSettings`. Every failure message this ticket writes names only the arm, the
  registry path, the seeded id and the composed argv — all four minted inside the test
  — and the `os.Getenv` / `os.Environ` ban makes an operator-derived path or a
  credential unreachable as a message source.

- **[Concurrency]** No findings. No goroutine is created: `Pool.Run` and `Runner.Run`
  are both uncalled, so there is no lifecycle goroutine, rotation watcher, conversations
  sweep or supervise loop, and nothing to leak. The factory's two captures are written
  and read on the one goroutine that calls `sessions.New`. `t.Parallel()` is declared at
  the top level only; the subtests are sequential, so the three harnesses never share a
  goroutine. `poolRevokeArms` is ranged over and never appended to or reassigned, per
  its own read-only contract. AC 4's distinctness rests on `t.TempDir()` returning a
  unique directory per call on the same `*testing.T` — a documented `testing` guarantee,
  and the reason the constructor calls it rather than taking a directory.

- **[Threat model alignment]** No findings, and this is the category the ticket exists
  for. The threat is a child running escalated while the daemon believes it can walk it
  back. `RunnerConfig.OperatorBypass`'s own doc block names the inversion: after #2065
  made `--dangerously-skip-permissions` unconditional, both daemon fail-safes keyed on
  that flag flip permissive, and provenance — not presence — became the discriminator.
  AC 3 pins `OperatorBypass == false` on all three arms, which is the single reading
  that would catch an escalation arriving from the operator's pass-through rather than
  from this package's own composition; a true there would leave #1675's `revoke` arm
  unrevocable while every other pre-spawn reading stayed green. **Explicitly out of
  scope:** behavioural enforcement — whether a revoked child actually gates a tool call
  — is #1675's measurement, and no assertion here claims it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
