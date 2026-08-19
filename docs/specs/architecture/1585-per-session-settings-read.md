# #1585 — Report a named session's persisted settings, not only the bootstrap's

**Size:** XS · **Label:** `security-sensitive` · Split from #1577

## Files to read first

Symbols, not lines — resolve each with `codegraph_search` / `codegraph_node`.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/sessions/pool.go` | `DefaultSettings` | The lock discipline and docstring shape to copy; the return convention the new method deliberately **diverges** from. Must end this ticket byte-identical. |
| `internal/sessions/pool.go` | `UpdateSettings` | The `p.sessions[id]` lookup + bare `ErrSessionNotFound` return that is the shape to mirror, and the absence of any empty-id fallback. Read only its head — the live-apply half is irrelevant here. |
| `internal/sessions/pool.go` | `Lookup` | The **opposite** empty-id convention (`""` → bootstrap). Read its docstring so you know precisely what you are choosing not to do. |
| `internal/sessions/pool.go` | `mintSettings` | Its docstring already records the `RWMutex`-non-reentrancy hazard AC-4 restates. This is the in-package precedent for "call me off `p.mu`". |
| `internal/sessions/session.go` | `SessionSettings`, `SettingsUpdate` | The value type being returned, and the pointer-per-field presence contract on the write side (load-bearing for the security finding below). |
| `internal/sessions/session.go` | the `settings` field on `Session` | Its comment states the guard discipline verbatim: `Pool.mu`, **not** `Session.lcMu`. |
| `internal/sessions/pool_update_settings_test.go` | `helperPoolWithSettings`, `TestPool_UpdateSettings_UnknownID` | The warm-start fixture to reuse, and the registry-bytes-unchanged idiom the no-mutation row copies. |
| `internal/sessions/pool_settings_test.go` | `spawnMintedWithSettings` | The build-then-register-under-`p.mu` idiom the second-session fixture copies, **minus** its `supervise`/`Activate` tail. |
| `internal/sessions/pool_default_settings_test.go` | `TestPool_DefaultSettings_KnownSettings`, `TestPool_DefaultSettings_NoBootstrap` | Stay green **unmodified** (AC-5). Also the docstring style for the new rows. |
| `internal/sessions/runner_test.go` | `testRunnerFactory`, `lifecycleRunner` | The runner double every pool test uses; it already implements `SetSpawnArgs` / `Restart` / `WriteUserTurn`, so a concurrent `UpdateSettings` is safe on a non-`Run` pool. |
| `docs/knowledge/features/sessions-package.md` | § `Pool.DefaultSettings` (#847) | The evergreen writeup this ticket sits beside. Read-only for you; documentation phase extends it. |
| `docs/PROJECT-MEMORY.md` | § project-level conventions | "Refusal-to-wire-code mapping is the consumer's job" — the convention that decides error-vs-bool below. |

## Context

`internal/sessions.Pool` is asymmetric about settings. The write side is per-session
(`UpdateSettings` takes an id, finds `p.sessions[id]`, refuses an unknown id with
`ErrSessionNotFound`). The read side is not: `DefaultSettings` reads only
`p.sessions[p.bootstrap]`, and `Session.settings` is unexported with no accessor, so
`Lookup(id)` hands back a `*Session` an outside caller still cannot read settings from.
`SessionInfo` carries no settings either, so `List()` is not a way around it.

Any consumer that wants to describe *a chosen session's* run configuration — rather than
the daemon-wide default — needs the missing read half. This ticket adds exactly that half.
It wires no consumer, changes no existing behaviour, and touches one production file.

Verified while sizing, since AC-5 depends on the count: `DefaultSettings` has exactly two
non-test callers — the `snapshotSettings` closure in `cmd/pyry`'s daemon composition root,
and `Pool.mintSettings`. The other repo hits are doc comments in `cmd/pyry/relay.go` and
`internal/relay/v2session_seams.go` that name it in prose without calling it.

## Design

### The new method

Add one exported method to `internal/sessions/pool.go`, placed immediately after
`DefaultSettings` and before `mintSettings`:

```go
func (p *Pool) SettingsFor(id SessionID) (SessionSettings, error)
```

Behaviour, in full:

- Takes `p.mu.RLock()` with a function-level `defer p.mu.RUnlock()`. **One acquisition per
  call, no other lock.**
- Looks `id` up in `p.sessions`. A miss returns `(SessionSettings{}, ErrSessionNotFound)` —
  the bare sentinel, unwrapped, exactly as `UpdateSettings` returns it.
- A hit returns `(sess.settings, nil)`. `SessionSettings` is a value type, so the return is
  a snapshot copy that aliases no pool-guarded state.
- Mutates nothing: no map write, no `saveLocked`, no argv recompose, no supervisor call.

Four things it deliberately does **not** do, each of which is a design decision rather than
an omission:

1. **No `id == ""` branch.** The empty id misses the map like any other unknown id and gets
   `ErrSessionNotFound`. This is AC-3, and it falls out of the absence of code rather than
   out of a guard — the same way `UpdateSettings` gets it. The bootstrap is registered under
   a real UUID (`NewID()` on cold start, `entry.ID` on warm start), so `""` is never a key.
   `Lookup`'s `""` → bootstrap convention is the one being declined; the new method sits on
   the `UpdateSettings` side of that disagreement so read and write agree.
2. **No `ValidID` gate.** Unlike `writeMCPSettings` (#1518), the id here never names a file
   and never leaves the map lookup. A malformed id is a map miss, which is already the
   correct answer. Adding a validator would introduce a second refusal shape for the same
   outcome.
3. **No delegation, in either direction.** `SettingsFor` does not call `DefaultSettings`,
   and `DefaultSettings` is not rewritten to call `SettingsFor`. Two exported methods each
   taking `p.mu.RLock()` in the same call chain self-deadlock the moment a writer queues
   between the acquisitions — Go's `RWMutex` is not reentrant, which is the hazard
   `mintSettings`'s docstring already records. AC-4 permits a shared `...Locked` helper as
   the safe form of sharing; this design does not need one, because the shared part would be
   three lines wrapping two different return conventions.
4. **No settings on `SessionInfo`.** Widening the `List()` value would put the YOLO posture
   on every enumeration whether or not the caller asked for it. A by-id read is the narrower
   surface.

### Why `error`, not `bool`

Both satisfy AC-2, and the ticket leaves the call to the architect because the two
neighbours disagree. `error` wins on three counts:

- **Project convention.** `docs/PROJECT-MEMORY.md` pins it: `internal/*` primitives return
  Go sentinels and the *consumer* maps them to dotted wire codes via `errors.Is`. A `bool`
  would force the eventual consumer to synthesise its own refusal, which is the mapping this
  codebase deliberately keeps at the dispatcher.
- **Every other id-keyed method on `Pool` already does this** — `UpdateSettings`, `Remove`,
  `Lookup`, `ResolveID`, `Rename` all return `ErrSessionNotFound`. `DefaultSettings`'s bool
  is not a counterexample: it takes **no argument**, and its false means "this daemon has no
  bootstrap to read", a fallback state rather than a caller error. There is nothing for a
  caller to have got wrong, so there is no error to return.
- **AC-3's actual requirement is agreement.** "Read and write must agree on what `""` means"
  is checkable by a caller only if both sides signal refusal the same way. `errors.Is(err,
  ErrSessionNotFound)` on both is that agreement, expressed in the type.

### Why `DefaultSettings` survives

It is not made redundant. `SettingsFor(p.BootstrapID())` is **two** `RLock` acquisitions
with a rotation window between them: `RotateID` can flip `p.bootstrap` under the write lock
after the first returns, so the composed form can read a session that is no longer the
bootstrap. `DefaultSettings` resolves `p.bootstrap` and reads `settings` under a single
acquisition and stays the atomic way to ask for the bootstrap's settings. The docstring must
say this, so a later reader does not "simplify" the two into one.

The near-duplication between the two four-line bodies is intentional and should not be
flagged in review. They are different contracts — a no-argument fallback accessor and an
id-keyed lookup — and the divergent return types are the honest expression of that.
Reconciling them behind one helper would cost more than the three lines it saves.

### Docstring — required content

The docstring is part of the deliverable, not decoration. It must record:

- What the method returns and that the value is a snapshot copy, not a lease.
- The empty-id decision, naming `UpdateSettings` as the side it agrees with and `Lookup` as
  the side it declines, so the asymmetry reads as chosen rather than overlooked.
- The lock discipline: `p.mu` (RLock) only, deliberately **not** `Session.lcMu`, because
  `settings` is a `p.mu`-guarded field (writer `UpdateSettings` holds the write lock; the
  other reader `saveLocked` holds `p.mu`), so there is no torn read.
- That it must not be reached from inside a `p.mu` critical section, and that it does not
  delegate to `DefaultSettings` — with the reentrancy reason, matching `mintSettings`.
- **The read-modify-write warning** (see Security review, category 2): a caller that reads
  the triple and then calls `UpdateSettings` must send only the fields it intends to change.
  Echoing the whole snapshot back re-asserts a `YOLO` posture an operator may have cleared
  in the interval. `SettingsUpdate`'s pointer-per-field presence contract makes sending one
  field the easy path; the warning exists so nobody takes the other one.

Cite symbols, never line numbers, in every comment added (`make cite-guard` is a build gate).

## Concurrency model

No new goroutines, no new channels, no new locks, no shutdown participation.

- **Lock order:** `p.mu` (RLock) only. The method is a leaf — it acquires nothing else and
  calls nothing that acquires anything, so it cannot participate in an ordering cycle.
- **Guard correctness:** `sess.settings` is guarded by `p.mu`, not `Session.lcMu` (stated on
  the field's own comment). Holding the read lock is sufficient and is what the existing
  reader `DefaultSettings` does.
- **Aliasing:** `SessionSettings` is a three-field value struct with no reference members, so
  the return copies. No caller can mutate pool state through it.
- **Nil-map safety:** a zero-value `&Pool{}` has a nil `sessions` map; a read of a nil map
  returns the zero value and `ok == false`, so the method returns `ErrSessionNotFound`
  without panicking. No nil check is needed and none should be added.
- **Staleness:** the snapshot may be one concurrent `UpdateSettings` stale by the time the
  caller looks at it. That is inherent to any lock-releasing accessor and is the same
  contract `DefaultSettings` and `mintSettings` already carry.

## Error handling

One failure mode, one signal.

| Input | Return |
|---|---|
| id present in `p.sessions` | `(sess.settings, nil)` — including `(SessionSettings{}, nil)` when the session is genuinely at defaults |
| id absent (unknown, malformed, or `""`) | `(SessionSettings{}, ErrSessionNotFound)` |

The zero-settings row and the not-found row are the two halves of AC-2: a real session at
defaults is `nil` error, an unknown id is the sentinel. The value alone never has to
distinguish them.

**The error must not wrap the caller's id.** Return `ErrSessionNotFound` bare, as
`UpdateSettings` does — not `fmt.Errorf("sessions: settings for %q: %w", id, ...)`. The
sentinel then carries no caller-supplied bytes, so a hostile or malformed id cannot be
reflected into whatever log line or wire frame a future consumer builds from the error. This
is a deliberate divergence from `ambiguousError`, which does echo ids — but only ids already
resident in the pool.

## Testing strategy

One new file, `internal/sessions/pool_settings_for_test.go`. All rows `t.Parallel()`,
table-driven where the shape repeats, stdlib `testing` only.

**Fixtures.** Reuse `helperPoolWithSettings(t, regPath, settings)` — it warm-starts a
persistent pool whose bootstrap carries the given settings, with `testRunnerFactory` so
nothing spawns. Get the bootstrap id from `pool.BootstrapID()` rather than re-typing the
helper's hard-coded UUID.

For the second-session rows, add one local helper that registers a non-bootstrap session
carrying explicit settings: mint an id with `NewID()`, call `pool.buildSession(id, "", <a
t.TempDir()>, settings)`, then insert into `pool.sessions` under `pool.mu.Lock()`. This is
`spawnMintedWithSettings`'s register step with its `supervise`/`Activate` tail dropped — a
read test needs no lifecycle goroutine. Skip `saveLocked` too; the read never touches disk.
Note that `Pool.New` restores **only** the bootstrap entry from the registry, so a
two-session fixture cannot be built by pre-writing two registry entries.

**Rows.** Each names the mutant it is the sole RED for.

1. **Known id, non-zero settings.** Bootstrap carrying `{Model: "opus", Effort: "high", YOLO:
   true}`; `SettingsFor(pool.BootstrapID())` returns exactly that triple and a nil error.
   *Baseline row — green under every mutant below, which is why it cannot stand alone.*

2. **A non-bootstrap session reports its own settings, not the bootstrap's.** Bootstrap at
   `{Model: "opus", Effort: "high", YOLO: true}`, a registered second session at `{Model:
   "sonnet", Effort: "low", YOLO: false}`. Assert `SettingsFor(secondID)` equals the second
   triple, and assert in the same test that it differs from what `SettingsFor(bootstrapID)`
   returns. *Sole RED for an implementation that ignores its `id` argument and reads
   `p.sessions[p.bootstrap]`. This is the ticket's whole point, so it is the row that must
   not be dropped.*

3. **Known id at zero settings is not-found's opposite.** Bootstrap built with
   `SessionSettings{}`; `SettingsFor(bootstrapID)` returns the zero value **and a nil
   error**. Assert the nil error explicitly — the value assertion alone proves nothing here.
   *Half of AC-2; pairs with row 4.*

4. **Unknown id.** A well-formed UUID not in the pool, against a pool whose bootstrap
   carries non-zero settings. `errors.Is(err, ErrSessionNotFound)` and the returned settings
   is the zero value. *Other half of AC-2; also RED for any implementation that falls back to
   the bootstrap on a miss — which is why the fixture's settings must be non-zero.*

5. **Empty id is not-found, on a pool that has a bootstrap.** Bootstrap carrying non-zero
   settings; `SettingsFor("")` returns `ErrSessionNotFound` and the zero value —
   specifically **not** the bootstrap's triple. *Sole RED for a `Lookup`-shaped `if id == ""
   { return bootstrap }` branch. The fixture choice is load-bearing: against a zero-value
   `&Pool{}` this row is vacuously green under both implementations, so do not use one.*

6. **Read and write agree on `""`.** On one pool with a bootstrap, assert both
   `SettingsFor("")` and `UpdateSettings("", SettingsUpdate{Model: ptr("haiku")})` return
   `ErrSessionNotFound`. *This is AC-3's stated rationale ("a caller reads the bootstrap and
   writes nowhere") encoded directly; it goes RED if a later ticket special-cases `""` on
   either side.* `ptr` already exists in `pool_update_settings_test.go`.

7. **The read mutates nothing.** Capture the registry file's bytes and mtime, run a known-id
   read, an unknown-id read and an empty-id read, then re-read: bytes and mtime unchanged,
   and `pool.List()` unchanged. Mirror the idiom in `TestPool_UpdateSettings_UnknownID`.
   *Second half of AC-4; RED for any implementation that calls `saveLocked` or touches the
   map.*

8. **Concurrent read against a concurrent writer, under `-race`.** A handful of goroutines
   looping `SettingsFor(bootstrapID)` while a handful loop `UpdateSettings(bootstrapID, …)`
   with alternating models; a `sync.WaitGroup` join must complete. Keep it small (a `WaitGroup`
   and two loops), and reuse `helperPoolWithSettings` — the existing `UpdateSettings` tests
   prove that path is safe on a non-`Run` pool with `lifecycleRunner`. *This is the only row
   that can catch AC-4's named hazard: a delegation to `DefaultSettings` double-acquires
   `RLock` and hangs once a writer queues between the two, which surfaces as a test timeout.
   State honestly in the row's comment that the deadlock detection is probabilistic (it needs
   the writer to interleave) while the race-detector coverage is deterministic.*

**Must stay green unmodified (AC-5).** `pool_default_settings_test.go`,
`pool_mint_settings_test.go`, `pool_update_settings_restart_test.go`. If any of them needs an
edit, the design has drifted — `DefaultSettings` and both its callers are out of bounds for
this ticket.

**Gate:** `make check`. This ticket adds no live-claude surface, so `make preship` is not
required. No test file is deleted or moved, so the shared-helper hazard does not apply.

## Open questions

- **Naming.** `SettingsFor` was chosen over `Settings` because a bare `p.Settings(...)` reads
  at the call site as the *pool's* settings, and over `SessionSettings` because that collides
  with the type name in the same package. If the eventual consumer ticket finds it reads
  badly in context, renaming an unwired method with one definition and no callers is
  effectively free — this is not a decision worth reopening now.
- **The `SessionSettings` value may grow.** Any field added later is disclosed by this method
  automatically, with no opt-in. That is the opposite of the structural fail-closed posture
  `mintSettings` takes for inheritance. For a read of one already-persisted session by a
  caller inside the daemon, disclosure-by-default is the right side; the decision is recorded
  here so the consumer ticket revisits it before putting the triple on the wire.
- **No consumer, by design.** The method is exported on an exported type, so staticcheck's
  `unused` will not flag it while it waits. If it is still unwired several tickets from now,
  that is a signal the split's second half stalled, not that this half was wrong.

## Security review

**Verdict:** PASS

**Findings:**

- **[1 — Trust boundaries]** No findings. The one boundary is the caller-supplied `id`
  crossing into a map lookup on `p.sessions`. It is a total operation: nothing is parsed,
  no path is built, no allocation is sized from it, and a miss is already the refusal. This
  is the deliberate contrast with `writeMCPSettings` (#1518), which needed a `ValidID` gate
  precisely because its id *names a file*; here it does not, so no gate is specified and the
  design section says why. Downstream holds a `SessionSettings` value, not a `*Session`, so
  no pool-guarded state escapes the boundary.

- **[2 — Tokens, secrets, credentials]** SHOULD FIX — mitigated in the spec. No credentials
  are involved (`Model` and `Effort` are free-form config strings), but `YOLO` is a
  security-relevant posture bit: it reports whether a session runs with
  `--dangerously-skip-permissions`. Reading it cannot set it, so #833/ADR-030's fail-safe
  (only an explicit `*true` enables bypass) is structurally untouched. The real hazard is
  second-order and is newly enabled by this method: a consumer that reads the triple and
  echoes all three fields back through `UpdateSettings` performs a read-modify-write that
  **re-asserts a `YOLO` posture an operator may have cleared in the interval**. This is
  exactly the trap `mintSettings` avoids by rebuilding its literal field-by-field. Mitigation
  is written into the Design section as a required docstring clause rather than a code-level
  check, per Evidence-Based Fix Selection: no such consumer exists yet, `SettingsUpdate`'s
  pointer-per-field presence contract already makes "send one field" the easy path, and the
  advisory tier is the proportionate response to an unobserved failure mode. The consumer
  ticket inherits the obligation.

- **[3 — File operations]** Not applicable by design. The method performs no file I/O: no
  path is constructed, `saveLocked` is not called, and the id never reaches the filesystem.
  Test row 7 pins this by asserting the registry's bytes and mtime are unchanged across three
  reads, so "no file I/O" is enforced rather than merely intended.

- **[4 — Subprocess execution]** Not applicable by design. The method does not touch
  `spawnBase`, does not call `spawnArgs`, and reaches no `Runner` method — no
  `SetSpawnArgs`, no `Restart`, no `WriteUserTurn`. It cannot influence any claude argv.
  This is the sharpest boundary against `UpdateSettings`, which shares the same field but does
  all four.

- **[5 — Cryptographic primitives]** Not applicable. No randomness, no hashing, no
  comparison against a secret. `NewID()` appears only in test fixtures, where it is generating
  a deliberately-absent id, not a security token.

- **[6 — Network & I/O]** Not applicable directly — this is an in-process accessor with no
  socket, no reader and no size-capped input. On resource exhaustion: an unbounded caller loop
  acquires `p.mu.RLock()` repeatedly, which cannot starve a writer, because Go's `RWMutex` is
  writer-preferring (a queued `Lock` blocks subsequent `RLock` acquisitions). No cap is
  therefore specified. Any rate limiting belongs at the wire verb the consumer ticket adds,
  not on the primitive.

- **[7 — Error messages, logs, telemetry]** No findings, by an explicit decision recorded in
  Error handling: the method returns the bare `ErrSessionNotFound` sentinel and must **not**
  wrap the caller's id into it. The error string therefore contains no caller-supplied bytes,
  so a hostile or malformed id cannot be reflected into a downstream log line or wire frame.
  The method logs nothing at all — it takes no logger and emits no `slog` call — so there is
  no per-read record of which session's posture was inspected.

- **[8 — Concurrency]** No findings. One `p.mu.RLock()` per exported call with a
  function-level `defer`, no second lock, no goroutine, no channel, so the method is a leaf
  in the lock graph and cannot form a cycle. The named hazard — an exported method re-entering
  another exported method's `RLock`, which deadlocks once a writer queues between the
  acquisitions — is excluded structurally by the no-delegation rule, and test row 8 exercises
  it under `-race`. There is no check-then-mutate: the method never mutates. Shutdown safety
  is trivial, since no partial state can exist.

- **[9 — Threat model alignment]** In scope: `internal/sessions` settings storage, whose
  relevant threat is the bypass-permissions posture, addressed under category 2. Out of scope
  and named as such: putting `YOLO` on the mobile wire. `docs/protocol-mobile.md`'s security
  model governs that exposure, and the decision belongs to the consumer ticket that wires this
  read to a verb — which must decide whether a remote client is entitled to learn a session's
  bypass posture, and gate on the same capability the existing settings verb uses. This ticket
  deliberately wires nothing, so it adds no wire surface to review.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
