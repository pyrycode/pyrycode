# #2492 — evaluate a revive's settings under the lock that retires the entry

## Files read

- `internal/sessions/revive.go` → `Pool.Revive` — the defect's call shape: it
  evaluates `p.revivedSettings(id)` as an *argument* to `materialise`, so the
  read's RLock is released before the write lock that retires the entry.
- `internal/sessions/get_or_create.go` → `Pool.materialise`, `Pool.GetOrCreateIn`
  — the shared core whose `settings` parameter is the value both callers decide,
  and the critical section that registers, retires and persists.
- `internal/sessions/pool.go` → `Pool.revivedSettings`, `Pool.mintSettings`,
  `Pool.DefaultSettings` — the two settings sources, both RLock-takers today,
  and the accessor `mintSettings` delegates to.
- `internal/sessions/pool.go` → `Pool.UpdateDormantSettings` — the write half
  (#2463) that makes this window reachable; its second "two windows outside it"
  bullet names this ticket as open.
- `internal/sessions/pool.go` → `Pool.UpdateSettings` — the in-repo precedent for
  applying settings to an already-built session: recompose through
  `Session.spawnArgs`, install `SetSpawnArgs` + `SetSpawnPermissionMode`.
- `internal/sessions/pool.go` → `Pool.buildSession`, `Pool.CreateIn` — the
  off-lock build (two file writes plus the `RunnerFactory` call-out) and the
  third `mintSettings` caller.
- `internal/sessions/pool.go` → `Pool.dormant` field doc — the key set only ever
  shrinks; that is what both dormant readers rest on.
- `internal/sessions/session.go` → `Session.spawnArgs`, `canonicalSettings`,
  `claudeSettingsArgs`, `Session.settings` field doc — settings is a `p.mu`-guarded
  field, so it may be written inside the critical section.
- `internal/sessions/runner.go` → `Runner.SetSpawnArgs`,
  `Runner.SetSpawnPermissionMode`, `RunnerFactory` — both installers are
  documented non-blocking and take no lock this package holds; the factory is the
  mandatory injection seam the deterministic test drives.
- `internal/sessions/pool_settings_test.go` → `recordingRunnerFactory`,
  `recordArgv`, `waitArgv`, `helperPoolArgvRecorder` — the argv assertions in this
  package read the argv recorded at runner **construction**, which constrains the
  design (see Design).
- `internal/sessions/pool_dormant_entries_test.go` → `helperPoolWarmStart`,
  `helperDormantID` — the warm-start fixture recipe the new test reuses.
- `internal/sessions/pool_mint_settings_test.go` → `TestPool_GetOrCreateIn_InheritsOperatorSettings`,
  `TestPool_Revive_DoesNotInheritOperatorSettings`,
  `TestPool_MintSettings_NoBootstrap_NoFlags` — the existing proofs of AC 3's two
  unchanged contracts, and the one test that calls `mintSettings` directly.
- `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md`
  § "Two concurrency windows sit outside the method" — the passage the
  documentation stage must restate.
- `docs/knowledge/features/sessions-package-concurrency.md` — the lock map; it
  does not mention the revive at all today.

## Context

`Pool.Revive` hands `materialise` a settings **value** computed before the call:
`p.revivedSettings(id)` takes `p.mu.RLock`, reads `p.dormant[id]` and releases.
`materialise` then builds the session off-lock and only afterwards takes
`p.mu.Lock` to register it and delete the dormant entry. A
`Pool.UpdateDormantSettings` landing in that gap is persisted, acknowledged, and
then deleted — the session is registered under the value read *before* the write,
and the next `saveLocked` writes that older value back from the live session. The
client was told the change landed and it did not.

The window has been latent since `materialise` existed because nothing wrote into
`p.dormant`; #2463 added the writer, which is what made it reachable. Impact is
low and self-correcting (model and effort carry no privilege; the posture is on
neither path; the next `request_session_settings` reports the live truth) — what
is wrong is the acknowledgement.

No ADR is warranted: this is a lock-scope correction inside one function, not a
new boundary. The documentation stage owns the three passages listed under
**Documentation handoff**.

## Design

The fix makes `materialise` evaluate the caller's settings **inside the same
critical section that retires the dormant entry**, so the read and the retirement
cannot be split. The settings parameter becomes a *provider* rather than a value,
which closes the window for both callers at once:

```go
// settingsSource is a materialise caller's settings, evaluated by materialise
// itself. MUST be callable with p.mu held.
type settingsSource func() SessionSettings

func (p *Pool) materialise(id SessionID, label, spawnDir string, settings settingsSource) (*Session, bool, error)
```

`Pool.GetOrCreateIn` passes `p.mintSettings`; `Pool.Revive` passes a closure over
`p.revivedSettings(id)`. Both sources become **already-locked readers** — their
contract flips from "MUST be called with `p.mu` unheld" to "MUST be called with
`p.mu` held" — because Go's `RWMutex` is not reentrant.

### The tension the ticket asks to resolve, and the shape chosen

`materialise` builds off-lock on purpose: `buildSession` writes the per-session
MCP settings file and the appended system-prompt file, then calls the injected
`RunnerFactory`. Moving that under `p.mu` would put two file writes and a call-out
to injected code inside the pool's write lock, and would also destroy the
deterministic test seam this ticket depends on (see **Testing strategy**). So the
build stays off-lock, and the ticket's other nominated shape — `Pool.UpdateSettings`'
recompose-and-install — is what carries the authoritative value onto the
already-built session.

**Build optimistically, confirm under the lock.** `materialise` reads the source
once under `p.mu.RLock` to get a *provisional* value, builds with it exactly as
today, and then re-evaluates the source under the write lock. The under-lock read
is the authoritative one; the provisional read exists only so the common,
non-racing path composes the right argv at runner **construction**:

- `Session.settings` and the runner's `ClaudeArgs` stay byte-identical to today on
  every path where no concurrent dormant write lands, so `RunnerConfig.ClaudeArgs`
  remains the argv the child spawns with. That is not merely test convenience —
  the package's argv assertions read the construction argv (`recordingRunnerFactory`),
  and a design that always arrived at the real settings through a post-construction
  install would make construction argv routinely wrong.
- When the two reads disagree — exactly the race this ticket fixes — the
  authoritative value is stored on the session and installed on its runner:
  `Session.spawnArgs(applied)` through `Runner.SetSpawnArgs`, and
  `applied.PermissionMode` through `Runner.SetSpawnPermissionMode` (#2064's reason:
  the runner asserts its stored posture to every child it spawns, so a
  construction-time value must not be left stale).

The comparison is `canonicalSettings(authoritative) != sess.settings`, which is
exact: `buildSession` stores `canonicalSettings(provisional)`, and
`SessionSettings` is a comparable value type — the same equality
`Pool.UpdateSettings` already uses for its no-op arm.

### Where the install sits, and why not past the unlock

`Pool.UpdateSettings` installs **past** its unlock, because there the runner is
live and shared, and taking a supervisor-internal lock under `p.mu` could close a
cycle. Here the install sits **inside** the critical section, above
`p.sessions[id] = sess`, and the difference is load-bearing:

- The session and its runner are locals built by this call and are not yet in
  `p.sessions`, so no other goroutine holds a reference to the runner and none can
  be holding its internal lock. The `Pool.mu → runner-internal` order cannot close
  a cycle against a runner nobody else can reach. Both installers are documented
  non-blocking and take no lock this package holds.
- Installing past the unlock would open a *new* window in place of the one being
  closed: the loser of a same-id race takes `materialise`'s take path, returns
  `took == true`, and its caller (`handleAttach`) activates — which can happen
  between the winner's unlock and the winner's install, spawning the child from
  the provisional argv. Publishing the session only after it is fully composed
  removes that ordering question entirely.

### `mintSettings` becomes an under-lock reader, uniformly

Both sources end up with one contract rather than two spellings of one:

- `Pool.revivedSettings` keeps its name and its body; only its concurrency
  paragraph inverts. It has exactly one caller and no unlocked sibling, so no
  `Locked` suffix: the contract lives in the docstring. (`Pool.DormantSettingsFor`
  remains the public, lock-taking dormant read, unchanged.)
- `Pool.mintSettings` stops delegating to `Pool.DefaultSettings` — which takes its
  own RLock — and resolves `p.bootstrap` plus its settings directly, under the
  caller's lock. The field-by-field literal is preserved verbatim: YOLO stays
  excluded structurally, which is the #1575/#1487 property, and the nil-bootstrap
  arm that `DefaultSettings`' discarded bool used to supply becomes an explicit
  early return of the zero value. `DefaultSettings` itself is untouched and keeps
  its single-acquisition atomicity for its own callers.
- `Pool.CreateIn`, the third `mintSettings` caller, does not go through
  `materialise`; it takes `p.mu.RLock` around its own call. Its id comes fresh
  from `NewID`, so it has no dormant entry to race and needs no further change.

### What deliberately does not change

- The retirement stays exactly where it is, inside the same critical section as
  the registration and the persist, and both rollbacks still restore the entry.
  The live/dormant partition therefore stays **total** at every instant: no
  shape here leaves an id in neither map, and nothing keeps a second copy of a
  retired entry.
- `materialise`'s take path returns before evaluating the source at all, so a
  revive racing a live session still drops the caller's settings by contract —
  and the source's lock acquisition is now skipped on that path.
- `cmd/pyry` needs no change; its `settingsUpdaterAdapter` window is a different
  one and is already correct.

## Concurrency model

No new goroutines. The change is one of lock scope:

| Step | Lock held | Note |
|---|---|---|
| provisional source read | `p.mu` (R) | taken by `materialise`, released immediately |
| `buildSession` | none | two file writes + the `RunnerFactory` call-out stay off-lock |
| take-path lookup | `p.mu` (W) | returns before the source is evaluated |
| **authoritative source read** | `p.mu` (W) | **the fix** — same section as the retirement |
| store + install on mismatch | `p.mu` (W) | runner is unpublished and exclusively owned |
| register, retire, `saveLocked`, `g.Go` | `p.mu` (W) | unchanged |

Lock order `Pool.mu → Session.lcMu` is untouched — nothing here takes `lcMu`.
`Session.settings` is a `p.mu`-guarded field, so writing it inside the section is
the existing discipline, not an exception to it.

## Error handling

- The provider cannot fail: `settingsSource` returns a value, and both sources
  answer a map miss with the zero `SessionSettings` (an unknown id revives to
  claude's defaults — today's behaviour).
- Both existing rollbacks (`saveLocked` failure, `ErrPoolNotRunning`) are
  unchanged and still restore the dormant entry. A session discarded by a
  rollback carries an installed argv nobody reads — harmless.
- A concurrent `UpdateDormantSettings` that loses the race — i.e. arrives after
  the entry is retired — still gets `ErrSessionNotFound`, which is the correct
  answer and the seam's documented one: the settings reached nothing.

## Testing strategy

New file `internal/sessions/pool_revive_settings_race_test.go`, driven
deterministically with no sleeps and no polling for a race.

**The seam.** `Config.RunnerFactory` is mandatory and injectable, and
`materialise` reaches it through `buildSession` — *inside* the window, between the
provisional read and `p.mu.Lock`. A factory that performs the dormant write on its
first construction for the target id lands that write in the gap synchronously.
This seam works only while `buildSession` stays off-lock, which the chosen shape
guarantees.

- **AC 1 — the in-window write is carried.** Warm-start a pool from a registry
  holding a dormant entry at `model: sonnet`. The injected factory calls
  `Pool.UpdateDormantSettings(target, {Model: "opus", Effort: "high"})` on its
  first construction for the target, then returns a runner double that records the
  argv it would actually spawn with (construction argv, replaced by any
  `SetSpawnArgs`, recorded at `Run` — the real runner's `beginSpawn` semantics).
  `Pool.Revive` then `Pool.Activate`. Assert `Pool.SettingsFor(target)` reports
  `opus`/`high`, and that the spawned child's argv carries `--model opus --effort
  high`. Red on the pre-fix tree, where the revived session materialises at
  `sonnet`.
- **AC 2 — an already-retired id still refuses.** Revive to completion, then call
  `Pool.UpdateDormantSettings`; assert `ErrSessionNotFound` and that
  `Pool.SettingsFor` reports the same settings before and after. Red against a fix
  that restores the entry or keeps a reachable copy.
- **AC 3 — the seam's other two contracts.** Proven by the existing suite, which
  must stay green rather than be duplicated:
  `TestPool_Revive_DoesNotInheritOperatorSettings` (#1575: a revive inherits
  nothing from the bootstrap) and `TestPool_GetOrCreateIn_InheritsOperatorSettings`
  + `TestPool_CreateIn_InheritsOperatorSettings` (a minted session starts at the
  operator's model and effort), plus #1487/#2448's posture proofs
  (`TestPool_DormantSettingsFor_RevokesPersistedPosture`, the revive-argv assertion
  in `TestPool_Revive_DoesNotInheritOperatorSettings`). The new fixture's dormant
  entry persists `yolo: true` beside its model so the revived session's default
  posture is asserted in the racing case too.
- **AC 4 — docstrings.** Not test-covered; the three docstrings are part of the
  diff (`Pool.revivedSettings`' concurrency paragraph,
  `Pool.UpdateDormantSettings`' second window bullet, `Pool.materialise`'s settings
  paragraph).

Gate: `go test -race ./internal/sessions/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Open questions

1. Does `Pool.Activate` need the installed argv to be visible before it can spawn?
   Resolved by design: the install happens before `p.sessions[id] = sess`, so no
   caller can reach the session — let alone activate it — before the argv is
   composed. Confirm in Phase B that no path publishes the session earlier.
2. Do any existing tests assert a `SetSpawnArgs` call count on a session that goes
   through `materialise`? The count assertions found all sit on `UpdateSettings`
   tests driven against the bootstrap, which `Pool.New` builds directly; confirm by
   running the package.
3. Does `TestPool_MintSettings_NoBootstrap_NoFlags` need the RLock once
   `mintSettings` becomes an under-lock reader? It drives a single-goroutine
   `&Pool{}`, so it stays green either way; add the acquisition so the call site
   does not model a contract violation.

## Documentation handoff

Owned by the documentation stage, not this ticket. Pending:

- `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md` —
  the passage ending "so it is filed as #2492 rather than fixed here" must
  describe what shipped instead, and say which windows remain around this seam.
- `docs/knowledge/features/sessions-package-key-types-reviving-a-dropped-session-pool-revive.md`
  — its "Model and effort restored, posture still zeroed (#2448)" section opens by
  saying `materialise` takes the settings as a parameter and forwards them
  verbatim. Restate that against the shipped call shape (a provider evaluated
  under the retiring lock), keeping the #2448 and #1575 reasoning intact.
- `docs/knowledge/features/sessions-package-concurrency.md` — record which
  critical section now covers a revive's settings evaluation, and the live/dormant
  partition `materialise` maintains, so the next reader of the lock map sees it
  without reading the code.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, but the boundary is worth naming because
  this fix deliberately makes a previously-dropped, phone-influenced value land.
  `p.dormant[id].Model` originates in a `set_session_settings` frame; it is gated
  upstream by `internal/relay`'s `validModel` (charset + length) and then by
  `cmd/pyry`'s `settingsUpdaterAdapter` membership check against the published
  vocabulary, both *before* `Pool.UpdateDormantSettings` writes anything. The fix
  changes only *when* that stored value is read — it adds no new writer, no new
  reader and no new sink. A gap write and a non-gap write pass the identical gate,
  so carrying the gap write widens timing, not trust.
- **[Tokens, secrets, credentials]** Not applicable by design: `materialise` mints
  nothing. The session id is caller-supplied and refused by `ValidID` before any
  other statement runs, and no token, key or credential is read, written or
  compared on either path.
- **[File operations]** SHOULD FIX — the recompose must not silently shorten the
  argv. `Session.spawnArgs` rebuilds from `spawnBase`, which carries both the
  `--settings <mcp file>` pair (#943's modal wedge) and the
  `--append-system-prompt-file <path>` pair (#2093/#2150's operator text), so the
  pairs survive structurally; but that is exactly the kind of structural claim that
  should be pinned. Phase B asserts the racing revive's argv with `waitArgvRaw`
  (which keeps the pairs) rather than `waitArgv` (which strips them). No path
  concatenates caller input into a filesystem path here: `spawnDir` is used
  verbatim and unvalidated exactly as today (#685/#696 put that duty on the
  caller), and `buildSession`'s two writes plus `saveLocked`'s atomic
  temp-plus-rename are untouched.
- **[Subprocess / external command execution]** No findings. The recomposed argv is
  an `exec` sink, and it is composed only through `claudeSettingsArgs` — the single
  place the YOLO fail-safe is enforced — so an installed argv cannot express an
  escalation the stored settings do not hold. No `sh -c`. `setArgsLocked` clones on
  the way in and `Session.spawnArgs` returns a fresh slice, so the pool keeps no
  live handle on the runner's argv and cannot mutate it after installation.
- **[Cryptographic primitives]** Not applicable by design: the change is a map
  read, a struct comparison and a slice recompose. No RNG, no hashing, no
  comparison against a secret.
- **[Network & I/O]** Not applicable at this layer: `internal/sessions` opens no
  socket and reads no frame. The frames that reach this seam are size- and
  shape-bounded by `internal/relay`, unchanged here.
- **[Error messages, logs, telemetry]** No findings, held as a constraint on the
  diff rather than an observation: nothing added may log. `spawnDir` is a
  phone-influenced workspace path (the #741 precedent), the recomposed argv embeds
  the operator's prompt-file path (#2150), and both `Pool.revivedSettings` and
  `Pool.UpdateDormantSettings` return bare errors that never echo the id (#833).
  The design adds no error path and no log line on any branch.
- **[Concurrency]** SHOULD FIX — the install introduces a new edge,
  `Pool.mu → runner-internal`, that the package has so far avoided
  (`Pool.UpdateSettings` installs past its unlock precisely to avoid it). It is
  safe here only because the runner is *unpublished*: it is a local built by this
  call, absent from `p.sessions`, so no other goroutine can hold its internal lock
  and the edge cannot close a cycle. `(*streamsup.Runner).SetSpawnArgs` and
  `SetSpawnPermissionMode` take `restartMu` and assign — no call-out, so neither
  can re-enter `AdoptAnnouncedReset`, the one `RunnerConfig` closure that would take
  `p.mu` back. Phase B states that precondition in `materialise`'s comment so a
  future `Runner` implementation knows the constraint it is bound by.
- **[Concurrency, TOCTOU]** No findings. The defect is a check-then-mutate split
  across two acquisitions, and the fix puts the authoritative read in the same
  critical section as the retirement, the registration and the persist. The
  provisional read makes no decision: it seeds the construction argv only, and the
  under-lock read overwrites it whenever the two disagree. The ordering is
  specified and load-bearing — the patch sits above `p.sessions[id] = sess` and
  therefore above `saveLocked`, so the persisted entry is written from the
  authoritative value; a patch placed after the save would fix memory and leave
  disk holding the stale model.
- **[Concurrency, shutdown]** No findings. The install is in-memory only. A crash
  between the retirement and the next spawn leaves `sessions.json` holding whatever
  the single `saveLocked` in this section wrote — the authoritative settings — and
  no goroutine is added, so no leak is introduced.
- **[Threat model alignment]** No findings, and this is the finding that would have
  been a MUST FIX had it gone the other way. `docs/protocol-mobile.md` § Security
  model and ADR 035 as amended by #2448 make a daemon restart a revocation point
  for a phone-granted permission bypass. Making the in-window write *land* must not
  become a path for an escalation to survive that restart. It cannot, and the
  exclusion is doubled exactly as the existing docstrings claim:
  `Pool.UpdateDormantSettings` refuses `YOLO` and `PermissionMode` with
  `ErrDormantPostureUnsupported` before any mutation, and `Pool.revivedSettings`
  reads a two-field literal that names `Model` and `Effort` only — an unchanged
  body — so a posture is unreadable even if one were somehow persisted. To keep
  that non-vacuous *on the new path*, the AC 1 fixture persists `yolo: true` beside
  the model, and the racing revive's argv is asserted to carry
  `--permission-mode default` rather than the bare escalation flag.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16

## Revisions

### 2026-09-16 — implementation

- **`waitArgv` rather than `waitArgvRaw` in the AC 1 argv assertion.** The
  security review's first SHOULD FIX asked for `waitArgvRaw` so a recompose that
  dropped the `--settings` or `--append-system-prompt-file` pair would be red.
  `waitArgv` already carries that property and carries it more sharply:
  `stripMCPSettings` and `stripSystemPrompt` each `t.Fatalf` when their pair is
  absent, so the assertion fails by name rather than by an exact-slice mismatch,
  and the expected argv stays comparable to its siblings in
  `pool_mint_settings_test.go`. The finding is discharged, by a different mechanism
  than the one named.
- **Open questions, resolved, none of which moved the design.** (1) Nothing
  publishes the session before the install — it sits above `p.sessions[id] = sess`,
  which is the only publication point in `materialise`. (2) No existing
  `SetSpawnArgs` count assertion sits on a session built through `materialise`;
  they are all `Pool.UpdateSettings` tests driven against the bootstrap, which
  `Pool.New` constructs directly, and the package is green. (3)
  `TestPool_MintSettings_NoBootstrap_NoFlags` now takes `p.mu.RLock` around its
  direct `mintSettings` call — behaviour-neutral on a single-goroutine pool, but it
  keeps the call site off a contract it would otherwise violate.
