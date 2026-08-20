# #1631 — Supply the claude sessions directory to the daemon's stream runner

Arms #1630's inert by-id transcript probe on the production path, so a stream-json
session that launched but never established a transcript respawns with
`--session-id <id>` instead of crash-looping forever on `--resume <id>`.

## Files to read first

Symbols, not lines — resolve each with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/streamsup_runner.go` | `mapStreamsupConfig` | The mapper this ticket adds one field to. Read its doc comment in full — two of its claims change (see § Design). |
| `cmd/pyry/streamsup_runner.go` | `newStreamRunnerFactory` | Why `Stdout` / `OnChildExit` are installed *here* and not in the mapper: they are runtime objects. The new field is not one — that distinction is the whole placement argument. |
| `cmd/pyry/main.go` | `resolveClaudeSessionsDir` | The daemon's existing startup derivation. **Do not reuse it here** — § Design says why. |
| `cmd/pyry/main.go` | `confineWorkdirToHome`, `resolveSpawnDir` | The two producers of the workdirs that reach the pool. Both return `EvalSymlinks(Abs(w))` and neither canonicalises case — the gap § Design closes. |
| `internal/sessions/reconcile.go` | `DefaultClaudeSessionsDir`, `encodeWorkdir` | The encoder. Note its contract: `""` for an empty workdir or unresolvable `$HOME`; `EvalSymlinks` alone, no case canonicalisation. |
| `internal/agentrun/workdir.go` | `ResolveWorkdir`, `canonicalCase` | The resolution `streamsup.New` applies to produce `cmd.Dir`. `canonicalCase` is the delta that matters. |
| `internal/streamsup/runner.go` | `useCreateForm`, `beginSpawn` | The consumer. Read that with a dir set, the probe decides **outright** — the latch is ignored on every spawn, including a `RestartFresh` rotation. |
| `internal/streamsup/runner.go` | `Config` (`ClaudeSessionsDir` field doc) | The contract the supplied value must satisfy. |
| `internal/sessions/runnerstate.go` | `RunnerConfig` | Confirm it already carries `WorkDir` and that no new field is needed. |
| `internal/sessions/pool.go` | `buildSession` (the `workDir := tpl.WorkDir` / `spawnDir` override) | Where a per-conversation runner's workdir diverges from the bootstrap's — the divergence AC 1's test must force. |
| `internal/e2e/internal/fakeclaude/main.go` | `argvSessionID`, `streamStdinLogPath`, `main` (the `envStreamJSON` branch) | The knob convention, the stem guard, and the exact place the new reject lands. |
| `internal/e2e/harness.go` | `spawnWith`, `spawnOpts`, `writeStreamInteractiveConfig`, `seedBootstrapRegistry`, `ensureFakeClaudeBuilt`, `childEnv` | Everything the new e2e starter composes. `harness.go` itself is **not** modified. |
| `internal/e2e/per_conversation_eviction_test.go` | `startPerConvHarness` | The precedent for a test-local starter that builds a `Harness` by hand. Mirror its shape. |
| `internal/control/client.go` | `Status` | `*StatusPayload{Phase, ChildPID, RestartCount}` — the e2e's observable. |
| `docs/knowledge/features/session-transcript-and-resume-probe.md` | § "The directory comparison" | #1655's measurement, and its closing paragraph naming the `canonicalCase` asymmetry as *"the finding the follow-up that supplies this directory on the daemon's production path needs"*. That follow-up is this ticket. |
| `docs/knowledge/features/streamsup-package.md` | § `useCreateForm` | Why `StatByID` (not a hand-rolled join) and why no `Newest` fallback. AC 4's by-id-only clause restates this; do not weaken it. |
| `docs/knowledge/decisions/032-bootstrap-resume-per-spawn-existence-probe.md` | § Related | Records that claude refuses `--session-id` against an existing transcript — the mirror failure a *wrong* directory would cause permanently. |

## Context

`internal/streamsup`'s spawn loop decides its id flag from a `firstRun` latch that
flips on `cmd.Start` succeeding, not on a transcript appearing. A session that
launched, ran no turn, and was torn down therefore looks established while
`<id>.jsonl` does not exist; every later respawn emits `--resume <id>`, claude
answers `No conversation found with session ID`, exits non-zero, and the daemon
retries forever on a widening backoff. Observed 2026-08-18.

#1630 landed the decision rule (`useCreateForm`) and left it **inert**: with
`Config.ClaudeSessionsDir` empty the probe is never consulted. That inertness is
structural rather than a default — nothing on the production path could set the
field. This ticket supplies the directory and proves the recovery end to end.

**Which directory is the whole of the risk.** A wrong directory reads "absent" for
a session whose transcript exists elsewhere, so the spawn emits `--session-id`
against a live transcript, which claude refuses (ADR 032). And because
`useCreateForm` with a dir set decides *outright* — ignoring the latch on every
spawn — a persistently wrong directory does not self-heal after one wasted spawn
the way the latch does. It is a permanent loop in the mirror direction: the same
defect this ticket exists to close, caused by the fix. The choice of derivation
below is therefore the load-bearing decision, not a detail.

**ADR-worthy?** No. ADR 032 already holds the by-id-existence decision and
`docs/knowledge/features/streamsup-package.md` § `useCreateForm` already holds the
port. This ticket adds one derivation, whose reasoning belongs in that ADR's
Related section and in the streamsup / fakeclaude package overviews.

**A stale forward reference the documentation phase should correct.** ADR 032's
Related section and `streamsup-package.md` both predict that #1631 "threads the
directory through `sessions.RunnerConfig`". This design deliberately does not (see
§ Rejected alternatives). Both sentences will be wrong once this lands. The
documentation phase owns those files; the developer must not edit them.

## Design

### The derivation

One new unexported helper in `cmd/pyry/streamsup_runner.go`:

```go
// streamClaudeSessionsDir derives the directory claude writes <uuid>.jsonl into
// for a runner whose WorkDir is workdir. "" means "no probe" — the contract
// streamsup.Config.ClaudeSessionsDir already documents.
func streamClaudeSessionsDir(workdir string) string
```

Three arms, in order:

1. `workdir == ""` → `""`. Unreachable at both pool sites (both carry a confined
   realpath), but it is the arm AC 4 names, and a unit test reaches it directly.
2. `agentrun.ResolveWorkdir(workdir)` errors → `""`. This is the "unresolvable
   path" arm. Note it introduces **no new failure**: `streamsup.New` calls the
   same function on the same value and returns an error, so a workdir that fails
   here never produced a runner on the pre-change tree either.
3. otherwise → `sessions.DefaultClaudeSessionsDir(resolved)`, returned verbatim.
   That function is where the `$HOME`-unavailable arm degrades to `""`.

**Why `ResolveWorkdir` first, and not `resolveClaudeSessionsDir` or a bare
`DefaultClaudeSessionsDir`.** `streamsup.New` sets the child's `cmd.Dir` to
`agentrun.ResolveWorkdir(WorkDir)`, and claude encodes its own resolved cwd into
the projects folder name. #1655 measured the empirical directory against
`sessions.DefaultClaudeSessionsDir(child_cwd)` where `child_cwd` was *"what
`agentrun.ResolveWorkdir` resolved the test workdir to, mirroring what `streamsup`
sets `cmd.Dir` to"` — so this composition is the measured one, not a preference.
The delta between the candidates is `canonicalCase`, which `ResolveWorkdir` applies
and `DefaultClaudeSessionsDir` does not; that same #1655 record names the asymmetry
and says a divergence "is the finding the follow-up that supplies this directory on
the daemon's production path needs". Neither `confineWorkdirToHome` (bootstrap) nor
`resolveSpawnDir` (phone) canonicalises case, so on a case-insensitive filesystem a
wrong-cased workdir survives to the pool intact, the child still lands in the
canonical-case cwd (the kernel's `getcwd` reports the on-disk spelling), and a
probe built without `canonicalCase` would read a directory claude never writes —
the permanent mirror loop described above. Closing it costs one function call.

### Where the call goes: `mapStreamsupConfig`

```go
ClaudeSessionsDir: streamClaudeSessionsDir(cfg.WorkDir),
```

One field, in the mapper, alongside the other `RunnerConfig` → `streamsup.Config`
field mappings. Not in `newStreamRunnerFactory`'s closure, for two reasons:

- **It is not a runtime object.** `Stdout` and `OnChildExit` live in the factory
  because they are live objects bound to the fan-in sink. `ClaudeSessionsDir` is a
  plain string derived from one `RunnerConfig` field — exactly what the mapper does.
- **AC 1 requires it to be observable.** `streamsup.Config` is an inspectable
  struct the mapper returns; `*streamsup.Runner` exposes no accessor for its
  config. Putting the derivation in the closure would make AC 1's assertion —
  *the directory reaching the runner's config* — unwriteable without adding a new
  exported accessor to `internal/streamsup`.

**Two doc-comment claims on `mapStreamsupConfig` must be rewritten, not left to rot.**

- *"Pure and side-effect-free"* is no longer true as written: the mapper now
  performs read-only filesystem syscalls (`EvalSymlinks`, and `canonicalCase`'s
  per-component `ReadDir`) and reads `$HOME`. What survives — and what that
  sentence was actually load-bearing for — is that it mutates nothing, constructs
  no runtime object, and returns a struct every field of which is inspectable, so
  the no-double-inject invariant is still asserted directly. Say that; do not just
  delete the word "pure".
- The `ResolveSessionID` line currently justifies dropping that PTY field with
  *"streamsup owns its own id-flag inversion"*. Still true, but incomplete: what
  streamsup now receives is a **directory**, from which its own per-spawn by-id
  probe decides the flag. No resolver callback crosses this seam, and no directory
  scan happens on either side. State that.

Nothing else in `cmd/pyry` changes. `mapStreamsupConfig` keeps its signature, so
its one caller is untouched.

### Rejected alternative: a field on `sessions.RunnerConfig`

Adding `ClaudeSessionsDir` to `sessions.RunnerConfig` and populating it at both
pool construction sites costs three production files instead of one, and adds a
field that is a pure function of `WorkDir` — already on the same struct. Two
sources of truth for one fact, populated at two sites, is exactly the shape that
drifts: a future third construction site would forget the second field and get a
silently inert probe. The mapper derives it from `WorkDir` at the one place that
already builds the streamsup config.

### Concurrency

None introduced. The derivation runs at runner construction, never per spawn.
`buildSession` — the per-session construction site — deliberately runs **before**
`p.mu.Lock()` at both of its callers (`Pool.CreateIn`, `Pool.materialise`), so no
pool lock is held across the new syscalls. The comment at `materialise`'s call
that calls `buildSession` "non-blocking" is about not spawning a process and is
unaffected: `streamsup.New` already performs `exec.LookPath` and the same
`agentrun.ResolveWorkdir` walk on this path today.

### Diagnostics — the deferred call, made

**No new per-spawn Debug log.** #1630's spec deferred a "dir/id/found" diagnostic
to this ticket on the grounds that a wrong directory was not yet reachable. It now
is, and the answer is still no:

- `Run` already logs `"spawning claude"` at **Info** with both `args` and
  `workdir`. The decided flag is in `args`; the directory is a deterministic,
  documented function of `workdir`. Both are already on the operator's log at the
  default level.
- The only bit a Debug line would add is `found`, and it is implied with no
  ambiguity: `--resume` ⟺ found, `--session-id` ⟺ not found.
- No diagnosis failure has been observed. Per Evidence-Based Fix Selection, if a
  wrong-directory incident is ever recorded, that record is what should shape the
  diagnostic — not a guess made in advance.

### Fake claude: an opt-in reject knob

`internal/e2e/internal/fakeclaude/main.go` gains one env knob, following the
existing `PYRY_FAKE_CLAUDE_*` convention and **defaulting off**:

```
PYRY_FAKE_CLAUDE_REJECT_ABSENT_RESUME  optional directory. When set, a spawn whose
                                       argv's LAST id flag is --resume <id> and for
                                       which <dir>/<id>.jsonl does not exist writes
                                       claude's own refusal to stderr and exits 1.
                                       Stream mode only. Default off — unset is
                                       byte-identical to prior behaviour.
```

- The check lives **inside the `envStreamJSON` branch**, before the stdin tee and
  the startup hold. Stream-only by construction, so no PTY-tier test can observe
  it even if the env leaked.
- Empty ⟹ off ⟹ every existing test is byte-identical. This is what AC 3's first
  hazard requires.
- The predicate needs "was the last id flag `--resume`?", which `argvSessionID`
  does not answer. Extract a shared unexported core — signature
  `argvIDFlag(args []string) (id string, resume bool, ok bool)` — holding the
  last-wins loop and the stem guard, with `argvSessionID` becoming a two-line
  wrapper over it and keeping its doc comment. **One copy of the guard**: that
  guard is the security-relevant part of `argvSessionID` (the value reaches
  `filepath.Join`), and a duplicated security guard is the thing that drifts. The
  existing `argv_session_id_test.go` table passing unmodified is the refactor's
  regression check.
- The stderr text should mirror real claude's (`No conversation found with session
  ID: <id>`) so a developer reading a failure log recognises it. Exit code 1, via
  the existing `fatalf`.

## Error handling

| Condition | Behaviour |
|---|---|
| Empty `WorkDir` | `ClaudeSessionsDir == ""` → probe inert → argv byte-identical to today. |
| `ResolveWorkdir` fails (path gone, permission) | `""` → inert. `streamsup.New` fails on the same input anyway, so no runner is constructed either way — no new failure mode. |
| `$HOME` unresolvable | `DefaultClaudeSessionsDir` returns `""` → inert. |
| Directory resolves but does not exist | Live probe; `StatByID` reports a miss → `--session-id`. This is the fixed case, not an error. |
| Directory unreadable (permissions) | `useCreateForm` already treats every non-hit as a legitimate answer, never an error — `--session-id`. No spawn can fail on the probe. |

No new error type, no new error return. The helper's only output is a string, and
`""` is a documented value rather than a failure.

## Testing strategy

### AC 1 — per-runner derivation (`cmd/pyry/streamsup_runner_test.go`)

One table test over `mapStreamsupConfig`, parallel. Scenarios:

- **Bootstrap workdir and a divergent per-conversation spawn dir.** Two
  `sessions.RunnerConfig` values differing only in `WorkDir` (two real
  `t.TempDir()`s — the derivation stats the path). Assert the two mapped
  `ClaudeSessionsDir` values **differ**, and that each equals
  `sessions.DefaultClaudeSessionsDir(agentrun.ResolveWorkdir(thatWorkDir))`
  computed in the test. A pool-global implementation returns one value for both
  and fails the first assertion; an implementation that derives per-runner but
  from the wrong path fails the second.
- **Empty workdir** → `""` (AC 4).
- **Non-existent path** → `""` (AC 4).
- **`$HOME` unavailable** → `""` (AC 4). Its own non-parallel test function using
  `t.Setenv("HOME", "")`; `cmd/pyry`'s test package already establishes
  `t.Setenv("HOME", …)` as its idiom, so this adds no new hazard class.

Existing `TestMapStreamsupConfig_Bootstrap` / `_PerSession` assert per-field, not
by `reflect.DeepEqual`, so a new field does not disturb them. They must pass
unmodified.

### AC 2 — end-to-end recovery (new file under `internal/e2e/`, `//go:build e2e`)

One test. Test-local starter modelled on `startPerConvHarness` — **do not modify
`harness.go`**; the test is in `package e2e` and can call `spawnWith`,
`writeStreamInteractiveConfig`, `seedBootstrapRegistry` and `ensureFakeClaudeBuilt`
directly.

Setup:

- `home := t.TempDir()`; write the stream-json config; seed the bootstrap registry
  at a fixed UUID.
- Compute the probe directory the daemon will derive, via the same two exported
  functions the production helper composes. **Do not create it** — its absence is
  the fixture. Note the coupling in a comment: this re-derivation exists because
  the production helper is unexported and cross-package.
- Spawn with `spawnOpts{claudeBin: <fakeclaude>, claudeArgs: []string{}, extraEnv:
  PYRY_FAKE_CLAUDE_STREAM_JSON=1 and PYRY_FAKE_CLAUDE_REJECT_ABSENT_RESUME=<probe
  dir>}`. The workdir defaults to `home`, which is what the derivation keys on.

Scenario:

1. Poll `control.Status` until `Phase == "running"` with a non-zero `ChildPID`.
   Record it. (Spawn 1 is `--session-id`; the fake accepts it on both trees.)
2. `SIGKILL` that pid — a crash respawn, the no-operator-involved reachability the
   ticket names first.
3. Poll `control.Status` until `RestartCount >= 1`. Generous deadline (≥20s):
   under `-race` a kill→respawn has been measured near a second, so a tight
   deadline is a flake, not a signal.
4. **Settle, then re-read.** Wait ≥4s and assert: `RestartCount == 1` still,
   `Phase == "running"`, `ChildPID` non-zero and different from step 1's. The
   settle is what makes the pre-change tree red rather than transiently green —
   `streamsup` reports `running` the moment `cmd.Start` succeeds, so a looping
   child is briefly indistinguishable from a healthy one at a single sample. With
   a 500ms→1s→2s ladder, four seconds is several more restarts.
5. **Argv evidence, control first.** From the captured daemon stderr
   (`h.Stderr.String()`; `Run` logs `"spawning claude"` with `args` at Info, and
   the harness daemon runs at Info by default): first assert the log **contains**
   `--session-id <bootstrapUUID>` — without that control the next assertion is
   vacuous, green on a log that never carried argv at all. Then assert the log
   contains **no** `--resume` anywhere. Spawn 1 is `--session-id` on both trees,
   so any `--resume` in the log is a post-kill respawn, which is precisely the
   loop.

Demonstrating the red: the fake's knob ships on this branch, so "the tree as it
stands before this ticket's change" means reverting only the one mapper field.
Do that temporarily, run this test, and confirm it fails on step 4/5 — a climbing
`RestartCount` and `--resume` in the log — **not** on a compile error and not on a
harness error. Restore before committing.

### AC 3 — existing fake-daemon e2e unmodified

Two hazards, and the argument that each is closed:

- *The reject behaviour* is knob-gated and stream-only. No existing test sets it.
- *The flag flip.* The fake writes no transcript in stream mode, so the probe now
  answers "absent" and every stream respawn becomes `--session-id <id>`. This is
  invisible to the tier because the fake keys everything on the **id**, not the
  flag: `argvSessionID` accepts both, so `streamStdinLogPath` and the per-child
  JSONL trigger path are unchanged. Sweeping the tier turns up no assertion on the
  literal `--resume` — only narration.

The developer must still **run** the suite rather than reason about it, with the
Makefile target's flags (`internal/e2e` is behind the `e2e` build tag — a bare
`go test ./internal/e2e/` runs zero tests and exits 0), and report the count of
tests executed, not the exit code.

One narration line does go stale in this tier: `startPerConvHarness`'s test,
`TestE2E_PerConversation_IdleEvictsAndReactivates`, describes AC#2's reactivation
as `respawn claude --resume <its own uuid>`. Correct it to say the reactivation
respawns that session's own id, and that the flag is now decided per spawn by the
by-id probe. This is a comment correction; it changes no assertion, so the test
still "passes unmodified" in the sense AC 3 means.

### The live tier's stale prose

`internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` narrates
re-activation as `--session-id`(refused)→backoff→`--resume` in two places (its
header's numbered note (2), and the comment above the resume turn) and calls that
"the exact 'restart after eviction' path this test verifies live". After this
change the probe finds the real transcript and emits `--resume` directly, skipping
the refusal and the backoff. The test's assertion is token continuity, so it should
still pass — faster — but both comments must be corrected to describe the new
shape, including the note that the recovery budget now has slack it did not have.
Do not weaken the assertion or the budget; only the prose is wrong.

This is a `needs-real-claude` ticket, so the live run is the dispatcher's, not the
developer's. Correct the prose from the design; do not attempt to run the suite.

## Scope and budget

Six files. Keep the whole change inside roughly 380 written lines — the logic is
about 25 lines and everything else is this repo's comment density and one e2e test.

| File | Kind | Budget |
|---|---|---|
| `cmd/pyry/streamsup_runner.go` | production | ~45 (helper + one field + two doc rewrites) |
| `internal/e2e/internal/fakeclaude/main.go` | production (test binary) | ~63 (knob doc + `argvIDFlag` + reject branch) |
| `cmd/pyry/streamsup_runner_test.go` | test | ~55 |
| `internal/e2e/<new>_test.go` | test | ~175 |
| `internal/e2e/internal/fakeclaude/argv_session_id_test.go` | test | ~22 |
| `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` | test (comments) | ~14 |
| `internal/e2e/per_conversation_eviction_test.go` | test (comment) | ~3 |

Out of scope, deliberately: any `sessions.RunnerConfig` change; any change to
`internal/streamsup`; a new harness starter in `harness.go`; a per-spawn probe
diagnostic; and every knowledge-base doc (the documentation phase owns those,
including the two stale forward references named in § Context).

Citations in new comments name symbols, never lines — `make cite-guard` is
diff-scoped and has no depth or range exemption.

## Open questions

- **Does any real-world deployment actually hit the case asymmetry?** The
  derivation closes it either way at no cost, so this does not gate the build. If
  the live tier ever records a divergence between the empirical and recomputed
  directories, that measurement belongs in
  `session-transcript-and-resume-probe.md` § "The directory comparison", which
  already reserves the slot.
- **Should `resolveClaudeSessionsDir` (the startup reconciliation derivation)
  adopt `ResolveWorkdir` too?** It has the same case gap. Out of scope here — it
  feeds reconciliation, not spawn argv, so its failure mode is a missed
  reconciliation rather than a crash loop. File separately if it matters.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. This change gives phone-originated data a
  new destination: a per-conversation runner's `WorkDir` traces back to the
  phone's requested cwd in `create_conversation.go`, and that value now also
  determines a directory the daemon `os.Stat`s. The boundary stays explicit and
  single: `resolveSpawnDir` confines the requested path to a realpath under `$HOME`
  (`expandTilde` → `confineWorkdirToHomeCreating` → `trustMark`) before it ever
  reaches `Pool.CreateIn`, and everything downstream — including this derivation —
  consumes only that confined value. No new parse site, no second boundary.
- **[File operations — traversal]** No MUST FIX. The derived path is
  `filepath.Join($HOME, ".claude", "projects", encodeWorkdir(resolved))`.
  `encodeWorkdir` replaces every character matching `[^A-Za-z0-9]` with `-`, so the
  encoded component provably contains no separator, no `.`, and no `..` — it is a
  single path component by construction, whatever the workdir was. Traversal via
  the workdir is not expressible. The id half is guarded independently:
  `transcript.StatByID` runs `ValidStem` **before** its join, which is exactly why
  `useCreateForm` must keep calling `StatByID` rather than hand-rolling the join —
  a hand-rolled one would turn a non-canonical `SessionID` into an arbitrary-path
  existence oracle. The spec forbids the hand-rolled form.
- **[File operations — TOCTOU]** No MUST FIX, stated deliberately. The design is
  check-then-use by nature: `StatByID` at `beginSpawn`, then `exec`. A transcript
  that appears or vanishes in the gap yields the wrong flag for exactly one spawn;
  the probe re-runs on the next one, so it converges rather than latching — the
  property that distinguishes it from the `firstRun` latch this ticket is
  replacing. No lock or lease is warranted for a self-correcting one-spawn window.
- **[File operations — attacker-planted transcript]** No MUST FIX; no new
  exposure. An actor able to write `<probe dir>/<id>.jsonl` could force `--resume`
  and have claude load an attacker-authored history. That requires write access to
  `$HOME/.claude/projects/…`, which already grants `~/.claude.json` and
  `~/.claude/settings.json` — strictly stronger positions. The exposure is
  identical to the PTY bootstrap path ADR 032 already ships. What keeps it from
  widening is the **by-id-only** shape: no `--continue` and no adopt-by-mtime scan,
  so an attacker must also know the minted session UUID and cannot win by planting
  a merely-newer file. #839 removed the scan to close exactly that confused-deputy
  gap; AC 4 pins it shut, and the spec restates the ban.
- **[File operations — permissions / atomicity / symlinks]** Not applicable by
  design: this change creates no file, writes no file, and opens nothing. It
  `Stat`s. `EvalSymlinks` is applied deliberately (resolving *toward* the path
  claude encodes is the correctness requirement, per #989), and the result is
  confined under `$HOME` upstream at `confineWorkdirToHome` / `resolveSpawnDir`,
  both of which compare symlink-resolved paths on both sides.
- **[Subprocess execution]** No findings. The spawn argv is untouched by this
  change — `useCreateForm` selects between two flags whose value is the pool-minted
  `SessionID`, and `stripSessionIDFlags` still guarantees exactly one id flag. No
  `sh -c`, no new argument reaches `exec.Command`, no environment change on the
  production path.
- **[Tokens, secrets, credentials]** Not applicable. No token, key, or credential
  is generated, stored, compared, or transported. No `crypto/*` surface is touched.
- **[Cryptographic primitives]** Not applicable — same reason.
- **[Network & I/O]** Not applicable. No socket, listener, deadline, or size cap is
  introduced; the only I/O added is a bounded local `Stat` plus the per-component
  `ReadDir` walk inside `canonicalCase`, both over a path already bounded by
  `$HOME` confinement.
- **[Error messages, logs, telemetry]** No MUST FIX. The new field's value is a
  directory path under `$HOME` that the existing `"spawning claude"` Info line
  already implies via its `workdir` field, so nothing newly sensitive can reach the
  log — which is also part of why § Diagnostics declines to add a second line. The
  new fake-claude stderr message echoes the session id, which is already on that
  process's argv and visible in `ps`; the binary is test-only and visibility-fenced
  under `internal/e2e/internal/`.
- **[Concurrency]** No MUST FIX. No goroutine, channel, or lock is added. The new
  syscalls run at runner construction inside `buildSession`, which both of its
  callers (`Pool.CreateIn`, `Pool.materialise`) invoke **before** taking `p.mu` —
  `materialise` documents that ordering as deliberate — so no pool lock is held
  across filesystem I/O and the control plane cannot be stalled by a slow path
  walk. `streamsup.New` already performs `exec.LookPath` and the identical
  `agentrun.ResolveWorkdir` walk at this same point today, so the blocking profile
  of that critical-section-adjacent region is unchanged in kind.
- **[Threat model alignment]** The relevant threat is the one ADR 032 names: a
  spawn whose id flag contradicts on-disk state. Before this change streamsup was
  exposed in both directions — one wasted spawn for assumed-fresh/actually-present,
  and a permanent loop for assumed-established/actually-absent. This closes both by
  deciding from disk per spawn. The residual, and it is named rather than fixed: a
  *wrong* directory makes the outright decision wrong on every spawn with no
  self-heal, which is why the derivation is pinned to the same transform
  `streamsup.New` applies to produce `cmd.Dir` (§ Design) rather than to a
  same-looking sibling. Out of scope and unowned: `resolveClaudeSessionsDir`'s
  identical case gap on the startup reconciliation path (§ Open questions) — a
  missed reconciliation, not a crash loop, and no ticket claims it yet.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
