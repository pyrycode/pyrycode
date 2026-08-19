# #1580 — streamsup: install a new spawn argv without killing the running child

**Size:** XS (confirmed; PO's estimate stands)
**Files:** 3 production, 6 test
**Behaviour change:** none. Additive method + comment corrections.

---

## Files to read first

Read by **symbol name**, not line number. `codegraph_search <name>` resolves each
one; this repo's `make cite-guard` fails a comment citation that points at (or
within 20 lines of) a declaration, so do not carry line numbers from the ticket
body into code comments.

### The mechanism you are changing

| File | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/runner.go` | `Restart` | The two halves in one call: `slices.Clone` into `r.args`, then hint + `iterCancel`. The clone is the assignment you are extracting. |
| `internal/streamsup/runner.go` | `beginSpawn` | The #1481 single-acquisition invariant and the non-reentrancy trap. This doc enumerates the racers; you are adding one. |
| `internal/streamsup/runner.go` | `RestartFresh` | The precedent for a doc that states *what it deliberately leaves alone* ("Unlike Restart it leaves `r.args` untouched"). Your new method's doc mirrors this shape inverted. |
| `internal/streamsup/runner.go` | `Runner` (struct) | The `restartMu` field doc: what it guards (`args`, `iterCancel`, `sessionID` + `rotatePending`) and why it is a leaf. |
| `internal/streamsup/runner.go` | `Run` | The post-child-exit branch. A child ended *externally* takes `drainRestart() == false` → backoff → respawn, so `RestartCount` **increments**. Test-relevant; see Testing strategy. |

### The interface + delegate

| File | Symbol | What to extract |
|---|---|---|
| `internal/sessions/runner.go` | `Runner`, `RunnerFactory` | The interface to widen, and the two doc comments AC 5 corrects. Both carry the stale nil-default claim. |
| `internal/sessions/pool.go` | `New` | The `"sessions: Config.RunnerFactory is required"` error that falsifies the nil-default claim in both docs above. |
| `internal/sessions/session.go` | `Runner` (method) | The accessor that exists, replacing the phantom `Session.Supervisor()` the interface doc names. |
| `cmd/pyry/streamsup_runner.go` | `streamRunner` | The delegate set (`Restart`, `Interrupt`, `RestartFresh`, `BeginRotation`) and the `var _ sessions.Runner = streamRunner{}` assertion whose comment carries the stale back-reference. |

### The five doubles (AC 3 — all must gain the method)

| File | Symbol |
|---|---|
| `internal/sessions/runner_test.go` | `fakeRunner`, `lifecycleRunner` |
| `internal/sessions/session_evict_race_test.go` | `raceRunner` |
| `cmd/pyry/session_router_test.go` | `stubRunner` |
| `cmd/pyry/inbound_deliver_rotation_test.go` | `baseRunner` |

`git grep -nE '\) Restart\('` returns exactly seven declarations: these five, plus
`streamRunner.Restart` and `(*streamsup.Runner).Restart`. That enumeration is the
complete fan-out — verified at spec time.

### Test instruments

| File | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/interface_test.go` | `TestRunner_LiveRestart` | Nearest existing shape. Copy its scaffolding; do **not** copy its `RestartCount == 0` assertion (see Testing strategy). |
| `internal/streamsup/runner_test.go` | `spawnArgsRecorder`, `idFlagValue` | Log-side argv capture. `count()` is the "no additional spawn" instrument; `all()` gives the argv sequence. |
| `internal/streamsup/runner_test.go` | `TestRunner_RestartFresh_RotatesThenResumesNewID` | The recorder-driven spawn-sequence pattern: poll `rec.count()` to a target, then `cancel()`/`join()`, then assert over `rec.all()`. |
| `internal/streamsup/runner_test.go` | `helperRunCfg`, `runInBackground` | Config + Run scaffolding. |
| `internal/streamsup/helper_test.go` | `helperChild` | The `record_block` mode: records argv, installs a SIGTERM handler, blocks, **never self-exits**. |
| `internal/sessions/pool.go` | `rekeyLocked` (or `internal/msgqueue/queue.go` → `advanceLocked`) | The repo's `xxxLocked` + "Caller MUST hold …" convention your unexported helper follows. |

---

## Context

`(*streamsup.Runner).Restart` is two operations fused: it installs the next
spawn's base argv, and it cancels the live child so `Run` relaunches. It is also
the *only* argv installer after construction — the sole write to `r.args`.

A caller that wants the argv swap without the kill cannot express it. Declining
to call `Restart` does not give a swap-only path; it gives no swap at all, and the
session's next spawn (crash-respawn, or evict → `Activate`) silently re-execs the
stale argv.

This ticket introduces the swap-only installer. Nothing consumes it yet.

**Why widen the interface rather than type-assert.** There is one production
`sessions.Runner` implementation (`cmd/pyry.streamRunner`, wrapping
`*streamsup.Runner`); the other five are test doubles, all in this repo and all
enumerated above. `internal/supervisor` no longer exists (#1348 deleted it;
CLAUDE.md's architecture table is stale on that line). Widening is compile-checked
across the whole set. A type assertion would fail *silently* at runtime and fall
back to `Restart` — the exact failure a swap-only caller exists to avoid.

---

## Design

### The constraint that shapes everything

AC 2 requires both:

1. exactly one place assigns `r.args`, and
2. `Restart` still acquires `restartMu` **exactly once**.

The obvious delegation — export the installer, have it take `restartMu`, call it
from `Restart` — satisfies (1) and breaks (2). `beginSpawn`'s doc records why (2)
is load-bearing: a racing `Restart`/`RestartFresh` takes `restartMu` exactly once,
so it serialises *wholly* before `beginSpawn`'s section or *wholly* after it.
There is no third position, which is what makes the forbidden state — a live child
under a pre-rotation id with no live iteration cancel — unreachable rather than
merely narrow. Splitting `Restart` into two acquisitions reopens that position.

The same doc records the other half of the trap: `restartMu` is not reentrant, so
any helper that *takes* it deadlocks when called from inside `beginSpawn`'s
section (this is why the `liveArgs` and `nextSpawnID` accessors were deleted).

### Resolution: a lock-held core with an exported wrapper

Three symbols in `internal/streamsup/runner.go`:

```go
// Caller MUST hold restartMu. THE sole assignment to r.args after construction.
func (r *Runner) setArgsLocked(args []string)

// SetSpawnArgs installs the argv the NEXT spawn will use, leaving any live child
// running. Takes restartMu exactly once. Safe from any goroutine.
func (r *Runner) SetSpawnArgs(args []string)

// Restart — signature unchanged, behaviour unchanged.
func (r *Runner) Restart(args []string)
```

- `setArgsLocked` performs the `slices.Clone`. Putting the clone here rather than
  at each caller makes it unmissable: there is no way to install an aliased slice.
  **The clone is load-bearing, not hygiene.** Without it the caller keeps a live
  handle to the runner's spawn argv and can mutate it *after* installation —
  a data race against `beginSpawn`'s read, and a mutation that lands in the
  `exec` argv after whatever validation the caller performed. `beginSpawn`'s doc
  says "`buildArgs` is pure and copies base into a fresh slice, so passing
  `r.args` needs no clone"; that sentence is about handing `r.args` *out*, and
  does not license dropping the clone on the way *in*. Do not "simplify" it away.
- `SetSpawnArgs` = lock, `setArgsLocked`, unlock. Nothing else.
- `Restart` calls `setArgsLocked` **inside its existing single section**, replacing
  the inline `r.args = slices.Clone(args)`. Its acquisition count, its `restartCh`
  send, its `iterCancel` call and their ordering are untouched.

This is the reading AC 2 asks for: the exported method and the lock-held core are
one mechanism, and `Restart` installs through it. The literal alternative — a
by-name call to the exported wrapper — is the acquisition split the ticket's first
Technical Note forbids by name, so it is not available.

`setArgsLocked` follows the repo's existing `xxxLocked` + "Caller MUST hold …"
convention (`rekeyLocked`, `saveLocked`, `advanceLocked`). The suffix and the doc
line are the guard against a future refactor re-fusing the two and reintroducing
the split.

### What `SetSpawnArgs` must NOT do

- **No `restartCh` send.** The hint is not decorative. A pending token makes the
  next child exit skip its backoff, and it *breaks a runner out of an in-progress
  backoff wait* — an observable relaunch. Both are kill-half effects.
- **No `iterCancel` read or call.** That is the kill.
- **No `sessionID` / `rotatePending` write.** That is `RestartFresh`.

Send either of the first two and the method is `Restart` under a new name.

### Security-relevant contract the doc comment MUST state

`r.args` is the base argv of an `exec`'d `claude` child, so the installer sits on
the path to process execution. Two properties are inherited from `Restart` and
must be written into the new method's doc, because this method exists to be
consumed by a *future* caller who will not re-derive them:

1. **The argv is installed verbatim. No validation, no shaping.** Whatever the
   caller passes is what the next spawn execs (modulo `buildArgs`, which only
   prepends the fixed stream-json flags and the id flag). Validation lives
   upstream — `Session.spawnArgs` is where the pool composes and where
   `claudeSettingsArgs` enforces the YOLO fail-safe in one place.
2. **Construction-time argv shaping is NOT reapplied.** `cmd/pyry`'s factory path
   shapes the argv twice on the way in — `mapStreamsupConfig` runs
   `stripSessionIDFlags`, and `newStreamRunnerFactory` runs `withApprovalArgs`.
   Neither runs on any post-construction install path. A caller that composes
   argv from the pool's `spawnBase` and installs it here gets neither.

Property 2 is not hypothetical bookkeeping — see the Security review's
out-of-scope finding, where the existing `Restart` path appears to hit both gaps
already. Stating it on the new method is how the next consumer avoids inheriting
them silently.

Also state, as `Restart`'s doc does, that the method touches neither `Pool.mu`
nor `Session.lcMu`.

Note for whoever writes the doc: `Run` logs the composed argv at Info
("spawning claude", `args`), so anything installed here reaches the daemon log.
Unchanged by this ticket — same log site, same field — but worth one clause so a
future caller does not install a secret-bearing flag unaware.

### Naming

`SetSpawnArgs` is the recommendation (it matches `Session.spawnArgs` in the
sessions layer, which composes the value that flows in). The name is the
developer's call, but the interface method, the `streamRunner` delegate and all
five doubles must agree.

### Interface + delegate (AC 3)

- `sessions.Runner` gains one method line, matching the concrete signature.
- `cmd/pyry.streamRunner` gains a one-line forwarding delegate, in the same shape
  as its `Restart` delegate.
- Each of the five doubles gains a no-op (or, for `lifecycleRunner`, whatever its
  existing `Restart` recording idiom is — read it before deciding; a recording
  double that silently drops the new call is worse than a no-op).

`var _ sessions.Runner = streamRunner{}` in `cmd/pyry/streamsup_runner.go` is the
compile-time proof for the delegate. `make check` is the gate for the doubles: a
double that misses the method fails to compile.

### Doc corrections (AC 5) — comment-only

**`internal/sessions/runner.go`.** Three claims are false, all #1348 fallout.
Correct them where they sit; do not relocate or restructure the file.

1. *"`*supervisor.Supervisor` satisfies Runner structurally … the compile-time
   assertion below is the proof."* — the file contains no `var _` at all, and the
   named type does not exist. The conformance assertion that exists is
   `var _ sessions.Runner = streamRunner{}`, in `cmd/pyry`.
2. *"when `Config.RunnerFactory` is nil, session construction defaults to
   `supervisor.New` … the rollback guarantee."* — a nil factory is now a
   construction **error** (`sessions.New`). **This claim appears twice**: in the
   `Runner` interface doc and again in the `RunnerFactory` doc below it. Correct
   both — the ticket cites both, and a developer reading "the interface doc" alone
   will leave the second one standing.
3. *"Consumers … reach it via `Session.Supervisor()`."* — no such method;
   `git grep -n ') Supervisor()'` is empty. The accessor that exists is
   `Session.Runner()`, and the concrete-method consumers (`Interrupt`,
   `RestartFresh`, `BeginRotation`) reach `*streamsup.Runner` by type assertion —
   as `cmd/pyry/streamsup_runner.go`'s own delegate docs already describe.

The replacement text should describe the implementation set that exists: one
production implementation via the `streamRunner` adapter (which exists because of
the covariant-return mismatch on `State`), five test doubles, a mandatory factory.

**`cmd/pyry/streamsup_runner.go`.** The comment above
`var _ sessions.Runner = streamRunner{}` says it "Mirrors
`var _ Runner = (*supervisor.Supervisor)(nil)` in internal/sessions/runner.go" —
pointing at the assertion claim (1) removes. Drop the back-reference; this
assertion is now the only one, not a mirror of another.

### Explicitly out of scope

`internal/sessions/pool.go` carries the same stale nil-default claim on the
`Config.RunnerFactory` field doc, contradicted by the corrected comment inside
`New` a few lines below. Observed, deliberately **not** fixed here — this ticket
does not otherwise touch `pool.go`, and AC 5 is scoped to the two files it does
touch. Do not widen.

---

## Concurrency model

No new goroutines, no new channels, no new locks.

`SetSpawnArgs` is a strictly weaker racer than `Restart`. Against a concurrent
`beginSpawn` section:

- It takes `restartMu` exactly once, so it serialises wholly before or wholly
  after — the #1481 property is preserved by construction, not by argument.
- Wholly before → spawn N uses the new argv. Wholly after → spawn N uses the old
  argv and spawn N+1 uses the new one. Both are correct: the contract is "the
  **next** spawn", which makes no promise about a spawn already in flight.
- It cannot produce the forbidden state at all. That state is defined over
  `sessionID` / `rotatePending` / `iterCancel`; `SetSpawnArgs` writes none of them.

`setArgsLocked` takes no lock, so it is callable from inside `beginSpawn`'s section
without deadlock — though `beginSpawn` has no reason to call it, since it only
*reads* `r.args`. `SetSpawnArgs` must never be called from inside a `restartMu`
section; the `Locked`-suffixed sibling is what such a caller uses.

`SetSpawnArgs` touches neither `Pool.mu` nor `Session.lcMu` — the same property
that lets the sessions layer call `Restart` after releasing `Pool.mu`. State it in
the doc comment so a future caller can rely on it without re-deriving it.

**No call-out under `restartMu`.** `restartMu` is a leaf: nothing may run under it
that takes another lock or does synchronous I/O. `beginSpawn` moves its
`"spawning claude"` log *below* its section for exactly this reason. So
`setArgsLocked` stays a clone plus an assignment — no logging, no validation
call-out, no metric. If a diagnostic is wanted, put it in `SetSpawnArgs` after the
unlock.

**Update `beginSpawn`'s doc.** It currently enumerates "a racing
`Restart`/`RestartFresh`" as the complete set of racers on `restartMu`. After this
ticket that enumeration is short by one. Add `SetSpawnArgs` to it with the
one-clause reason it is safe (single acquisition, argv only). This is a comment
edit in a file already being modified, and leaving it stale reproduces exactly the
`internal/sessions/runner.go` failure AC 5 exists to repair.

---

## Error handling

There are no new failure modes. `SetSpawnArgs` returns nothing, cannot fail, and
takes no `context.Context` — matching `Restart` and `RestartFresh`.

An empty or nil `args` is a legitimate install (the empty base argv is what
`helperRunCfg` uses), so there is no validity check to add. Contrast
`RestartFresh`, which rejects an empty id because the runner must never emit
`--session-id ""`; no analogous illegal value exists here.

---

## Testing strategy

### The new test (AC 1) — `internal/streamsup`

Both halves against one live child, in one test. Scenarios, not code:

**Setup.** `helperRunCfg` in `record_block` mode (records its argv, then blocks
until SIGTERM — it never self-exits, which is what makes "the same child is still
running" observable). Attach a `spawnArgsRecorder` as `cfg.Logger` and a
`cfg.onSpawn` that publishes the child pid on a buffered channel. Set
`BackoffInitial`/`BackoffMax` to ~1ms. Start via `runInBackground`.

**Half (a) — the swap does not disturb the live child.**

- Wait for spawn 1; capture its pid from `onSpawn`.
- Call `SetSpawnArgs` with a distinctive marker (e.g. `--model swapmarker`).
- After a settle window, assert `rec.count() == 1` — no further spawn was even
  *attempted*. `spawnArgsRecorder` records at the "spawning claude" log site,
  which fires before `cmd.Start`, so it is strictly stronger than counting
  `onSpawn` fires.
- Assert `r.State().ChildPID` still equals the captured pid — same child, not a
  replacement that happens to have restarted within the window.

Both assertions together are the AC's two clauses. Keep both: the count alone
allows "no new spawn *and* the old child died", and the pid alone allows a
spawn-attempt that failed setup.

**Half (b) — the installed argv reaches the next spawn.**

- End the child with a mechanism that provably installs no argv.
- Wait for `rec.count()` to reach 2, then `cancel()` + `join()`, then assert over
  `rec.all()`:
  - spawn 1's argv does **not** contain the marker;
  - spawn 2's argv **does**;
  - spawn 2 carries `--resume <testSessionID>` and no `--session-id` (use
    `idFlagValue`), proving this is an ordinary continuation and the swap did not
    perturb the id form.

Assert over `rec.all()` rather than `GO_STREAMSUP_HELPER_ARGV_FILE`: the log-side
capture is ordered by the Run goroutine, so there is no wait on the child
flushing its argv line to disk.

**Choosing the ending mechanism.** The developer's call; proving attribution is
not. Whichever is chosen, say in a comment *why* it installs no argv.

- **Recommended — signal the captured pid directly** (`syscall.Kill(pid,
  syscall.SIGTERM)`; `record_block` handles SIGTERM and exits 0). Attribution is
  structural: nothing on the `Runner` is called between `SetSpawnArgs` and the next
  spawn, so the marker can have come from nowhere else. No new helper mode, and
  `syscall` is already imported in this package's test files.
  Signalling a pid is only sound because `record_block` **never self-exits**, so
  the captured pid cannot have been reaped and recycled onto an unrelated process
  by the time the test signals it. Tie that reason to the mode in a comment: a
  later switch to a self-exiting mode (`crash`) silently turns this line into a
  signal aimed at whatever now owns that pid.
- **Sanctioned alternative — `RestartFresh`.** Its own doc states it leaves
  `r.args` untouched. Costs a moving part: it also rotates the id, so spawn 2
  becomes `--session-id <newID>` (first-run form) and the id-flag assertions above
  invert.
- A new `helperChild` mode that exits on demand is equally acceptable.

**Trap — do not copy `TestRunner_LiveRestart`'s `RestartCount == 0` assertion.**
That holds because a `Restart` sends the `restartCh` hint, so `drainRestart()`
returns true and `Run` skips the backoff block that increments the counter. An
externally-ended child sends no hint, so `Run` takes the backoff path and
`RestartCount` **increments**. Assert `>= 1`, or omit the assertion; asserting 0
fails for a reason that has nothing to do with this ticket.

### Pinned tests (AC 4)

Both sets of seven `TestPool_UpdateSettings_*` — in
`internal/sessions/pool_update_settings_restart_test.go` and
`internal/sessions/pool_update_settings_test.go` — stay green **unmodified**. All
fourteen are pinned; this ticket changes no behaviour.

Neither file declares a `sessions.Runner` double (the doubles live in
`runner_test.go` and `session_evict_race_test.go`, same package), so neither needs
an edit for the interface widening. That makes the check deterministic rather than
a judgement call:

```
git diff --name-only origin/main...HEAD   # must list NEITHER pool_update_settings*.go
```

If either appears in that output, something went wrong — investigate rather than
adjust the test.

### Gate

`make check`. It compiles all six implementations, so a missed double is a build
failure rather than a silent gap.

---

## Open questions

1. **Method name.** `SetSpawnArgs` recommended; developer's call. It must be
   identical across the concrete method, the interface, the delegate and the five
   doubles.
2. **Ending mechanism for half (b).** Direct SIGTERM recommended; `RestartFresh`
   and a new helper mode are both sanctioned. The AC constrains the *proof*, not
   the choice.
3. **Settle window for half (a).** A short fixed wait is the pragmatic instrument
   for a negative assertion and matches the suite's existing idiom. If the
   developer finds a deterministic edge to hang it on instead, prefer that.

---

## Security review

**Verdict:** PASS (revised once — the findings below are folded into the Design
and Testing sections above; re-walked after revision, no new surface).

**Findings:**

- **[Trust boundaries] SHOULD FIX — addressed in spec.** `r.args` feeds the `exec`
  argv of the `claude` child, so this method is a second door into the
  argv→execution path; today `Restart` is the only one. The installer applies no
  validation, by design — validation lives upstream in `Session.spawnArgs`, where
  `claudeSettingsArgs` enforces the YOLO fail-safe in one place. That is the right
  boundary (duplicating it in the installer would give two places to keep in
  sync), but it is only safe if stated. Spec now requires the doc comment to say
  the argv is installed verbatim. See § Security-relevant contract.

- **[Subprocess execution] SHOULD FIX — addressed in spec.** Construction-time
  argv shaping is not reapplied on any install path: `stripSessionIDFlags` runs in
  `mapStreamsupConfig` and `withApprovalArgs` runs in `newStreamRunnerFactory`,
  both construction-only. The new method inherits that gap from `Restart`, and its
  whole purpose is to serve a future caller. Spec now requires the doc to state
  it. No shell is involved anywhere on this path — `exec.Command(claudeBin,
  args...)` takes an argv vector, so there is no metacharacter-injection surface;
  `Config.Env` is untouched by this ticket.

- **[Subprocess execution] SHOULD FIX — addressed in spec.** The recommended test
  signals a captured pid. Sound only because `record_block` never self-exits, so
  the pid cannot have been recycled. Spec now requires that reason in a comment,
  so a later mode change does not silently turn it into a signal at an unrelated
  process.

- **[Concurrency] No findings.** `SetSpawnArgs` writes only `r.args` and takes
  `restartMu` exactly once, so #1481's single-acquisition property holds by
  construction. It cannot reach the forbidden state at all: that state is defined
  over `sessionID` / `rotatePending` / `iterCancel`, none of which it touches.
  `Restart` keeps exactly one acquisition — the whole point of the lock-held-core
  design. Two hardening requirements added: the `slices.Clone` is mandatory (its
  absence is both a data race against `beginSpawn` and a post-validation mutation
  primitive), and `setArgsLocked` must make no call-out under the leaf mutex.
  No new goroutines, channels, or locks; nothing to leak.

- **[Error messages, logs] SHOULD FIX — addressed in spec.** `Run` logs the
  composed argv at Info, so anything installed reaches the daemon log. Exposure is
  unchanged by this ticket (same site, same field, already true for `Restart`), but
  the doc should note it for the future caller.

- **[Tokens, secrets] Not applicable.** The design generates, stores, compares and
  transmits no credential. The residual exposure is argv-shaped (argv is visible to
  local `ps` and reaches the log) and is covered by the logging finding above.

- **[File operations] Not applicable.** No path is constructed, opened, stat'd or
  written. The test's only filesystem use is the existing `helperRunCfg` /
  `t.TempDir()` scaffolding; the recommended design drops
  `GO_STREAMSUP_HELPER_ARGV_FILE` in favour of log-side capture, so it touches
  fewer files than the test it is modelled on.

- **[Cryptographic primitives] Not applicable.** No RNG, no keys, no nonces, no
  comparison of attacker-controlled values against secrets.

- **[Network & I/O] Not applicable.** The method performs no I/O and opens no
  listener or connection. Recorded here because it is *reachable* from the network
  in principle — see the threat-model note below — not because it does I/O.

- **[Threat model] OUT OF SCOPE — needs its own ticket.** The relevant threat is a
  hostile or compromised paired client influencing what the daemon execs. The
  settings path is remote-reachable: `internal/relay/v2session_settings.go` →
  `SettingsUpdater.UpdateSettings` → `settingsUpdaterAdapter` (`cmd/pyry/main.go`)
  → `Pool.UpdateSettings` → `Session.spawnArgs` → `Restart`. Following that chain
  against the two construction-only shapers, the **existing** `Restart` path reads
  as hitting both gaps on the stream-json runner:

  1. `spawnArgs` recomposes from `spawnBase`, whose own field doc says it carries
     the construction-time `--session-id <id>` suffix for a minted session.
     `stripSessionIDFlags` is not reapplied, so streamsup's `buildArgs` would add
     its own id flag on top — the double-injection that function exists to
     prevent.
  2. `withApprovalArgs` is not reapplied either, so the non-yolo approval flag set
     (`--permission-prompt-tool` / `--mcp-config` / `--strict-mcp-config` /
     `--permission-mode`) present at construction would be absent from every
     post-`UpdateSettings` spawn — i.e. the daemon approval gate stops being
     requested for the rest of that session.

  Evidence is structural, not a live repro: `withApprovalArgs` and
  `stripSessionIDFlags` each have exactly one production caller, both inside the
  construction path; the sessions package contains no reference to the approval
  flags in code or tests. **#1580 must not widen to fix this** — it introduces no
  consumer and changes no behaviour, so nothing here is exploitable as designed.
  Recommend PO open a ticket to confirm against a live stream session and, if
  confirmed, move both shapers to the single install site this ticket creates —
  which is precisely the seam that makes that fix cheap.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
