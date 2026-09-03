# #2065 — launch every child with bypass; the stored setting decides the posture

Make `--dangerously-skip-permissions` unconditional on every claude child the
daemon spawns, and move the two fail-safes that are currently expressed as *the
flag's absence from the argv* onto a **provenance** signal the daemon composes
itself.

## Files read

- `internal/sessions/session.go` → `claudeSettingsArgs`, `Session.spawnArgs`,
  `Session.spawnBase`, `SessionSettings`, `permissionModeInBand`,
  `canonicalPermissionMode`, `permissionModeDefault` / `permissionModeBypass` —
  the composition site the ticket makes unconditional, and the doc block whose
  "the bypass flag has exactly one origin" sentence stops being a complete
  account.
- `internal/sessions/pool.go` → `Pool.New`, `Pool.buildSession` — the two
  `RunnerConfig` construction sites, both of which build a settings-free `base`
  and then append `claudeSettingsArgs`. `base` is where the provenance bit is
  read. Also `Pool.UpdateSettings` (its `SetSpawnPermissionMode` /
  `Restart` / `SetSpawnArgs` split) and its doc's claim that
  `claudeSettingsArgs` "expresses default by OMITTING the flag".
- `internal/sessions/runnerstate.go` → `RunnerConfig`, and `PermissionMode`'s
  doc — the seam the new bit joins.
- `internal/sessions/revive.go` → `Pool.Revive` — passes the zero
  `SessionSettings`, which `canonicalSettings` turns into `default`; the #1487
  invariant AC 1 restates.
- `internal/streamsup/runner.go` → `spawnAndWait` (the posture-gate arm site and
  its `!slices.Contains(args, bypassPermissionsFlag)` interlock),
  `bypassPermissionsFlag`, `Config.SpawnPermissionMode`, `Config.PostureGate`,
  `Runner.spawnMode`, `beginSpawn`, `SetSpawnArgs` — fail-safe 2 and the reason
  its predicate is evaluated per spawn today.
- `internal/streamsup/envelope.go` → `permissionModeAllowed`,
  `WritePermissionMode` — the writer-side allow-list that already refuses the
  escalation by non-membership.
- `cmd/pyry/streamsup_runner.go` → `withApprovalArgs`, `newStreamRunnerFactory`,
  `mapStreamsupConfig`, `namesPermissionMode`, `dropPermissionMode` — fail-safe 1
  and the single construction-time call site that has `cfg` in hand.
- `cmd/pyry/mcp_config.go` → `permissionArgs` — shared with `pyry agent-run`, so
  out of bounds for this change (`#1387` pins the flag's absence there).
- `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go` →
  `TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows`,
  `seedBypassRegistry`, `revokeTap`, `newRevokeLogRecorder`, `revokeBaseArgs`,
  `inbandSendTurn` — the live arm AC 5 extends.
- `internal/e2e/internal/fakeclaude/main.go` → the two stream-json read loops.
  Neither reads `--dangerously-skip-permissions`, so the always-on flag is inert
  to the fake-daemon suite; `runStreamJSONApprove` already answers
  `set_permission_mode` since #2064.
- `docs/knowledge/features/streamsup-package-posture-gate-spawn-permission-mode-ack.md`
  — records that this predicate is *the one place* the provenance distinction is
  currently held, and names #2065 as the ticket that must give it one. It also
  records the fakeclaude duplicate-read-loop regression, which is why the fake
  e2e suite is checked rather than assumed.
- `docs/knowledge/features/sessions-package-key-types-sessionsettings-claudesettingsargs.md`
  — the mutual-exclusion account AC 2 says must be restated, not dropped.

## Context

`claudeSettingsArgs` encodes the permission posture in the launch argv:
`--dangerously-skip-permissions` when the YOLO bit is set, `--permission-mode
<mode>` otherwise, in one mutually-exclusive `switch`. That is why enabling
bypass on a running session needs a respawn — claude refuses an in-band
escalation at a child that did not launch with the flag.

#2060 and #2061 re-measured, at claude 2.1.239, that a child launched *with* the
flag accepts a re-escalation after being dropped to `default`, that the approval
flags survive sitting beside it, and that the launch→downgrade race window is
closed. #2064 shipped the fail-closed turn gate that makes "the write was
confirmed" a real property. This ticket removes the argv's authority over the
posture. It does **not** route the escalation in-band; enabling bypass on a
running session still respawns.

Putting the flag on every argv breaks three places where the flag's *absence*
was doing the safety work, and none of them fails loudly — each degrades to a
session more permissive than the operator asked for:

1. `withApprovalArgs` returns args unchanged when the flag is present, so a
   universally-present flag deletes the daemon's approval gate for every session.
2. `spawnAndWait` suppresses the spawn-time posture write when the flag is
   present, so a universally-present flag means no child is ever downgraded —
   every child stays in bypass and every gate stays open, the exact inverse of
   this ticket, shipped green.
3. The registry's fail-closed reads (`canonicalSettings`, `Pool.Revive`'s zero
   settings) stop deciding what a child *launches* as and start deciding only
   what it is walked back *to*. The safety moves from "the flag is absent" to
   "the write was confirmed", which is #2064's gate.

**Size — over the ceiling, deliberately.** The refiner's estimate is ~1,300
written lines, above the 800-line boundary. Re-counted against this written plan
(§ Size re-check) it still is. The floor rule decides it: the provenance seam has
exactly one consumer, the always-on flag, and neither half ships alone — the seam
alone is a no-op with no observable behaviour change, and the flag alone leaves
every child permanently in bypass with the approval gate absent. The floor wins
over the ceiling, so this stays one ticket and the overage is stated rather than
split away.

**ADR.** No new decision record is warranted. The design consumes ADR-030's
plain-bool fail-safe reasoning and #2064's posture-gate record rather than
establishing a new position; the durable account belongs in the two package
overviews the documentation phase owns.

## Design

### The one thing that crosses the seam: provenance, one bit

Bypass reaches a spawn's argv from exactly two entry points, and only one is
modelled by the stored posture:

| how the flag got on the argv | stored posture | what must happen |
|---|---|---|
| `claudeSettingsArgs`, from the YOLO bit | `bypassPermissions` | nothing written — `permissionModeAllowed` refuses it by non-membership |
| `claudeSettingsArgs`, unconditionally (this ticket) | a real in-band mode | **the mode IS written** — the downgrade this ticket exists for |
| the operator's bootstrap pass-through claude args | a real in-band mode | **nothing written** — asserting a mode here silently revokes a bypass the operator explicitly asked for |

Rows 2 and 3 are indistinguishable to anything reading the assembled argv. The
signal that separates them already exists inside `internal/sessions`:
`Session.spawnBase` is the **settings-free** argv (template args — which carry
the operator's pass-through — plus the construction-time `--session-id` and
`--settings` suffixes), and `claudeSettingsArgs` is the suffix appended to it. So:

> the bypass on this session's argv is **operator-granted** iff `spawnBase`
> already carries the flag.

That is construction-fixed and cannot go stale: `spawnBase` is immutable
post-construction, and every post-construction argv install (`Pool.UpdateSettings`
→ `Session.spawnArgs` → `Runner.Restart` / `Runner.SetSpawnArgs`) recomposes from
that same base.

`internal/sessions/session.go` gains, beside `claudeSettingsArgs`:

```go
const bypassPermissionsArg = "--dangerously-skip-permissions"  // one spelling, hoisted
const PermissionModeBypass = permissionModeBypass              // exported for cmd/pyry
func operatorBypass(base []string) bool                        // slices.Contains(base, flag)
```

`PermissionModeBypass` is an alias of the existing unexported constant, not a
second literal — the escalation keeps exactly one spelling in this package.

The bit crosses two package boundaries as one new field on each of two existing
config structs:

- `sessions.RunnerConfig.OperatorBypass bool` — set at both construction sites
  from `operatorBypass(base)`.
- `streamsup.Config.OperatorBypass bool` — mapped straight through by
  `mapStreamsupConfig` (a plain bool derived from one `RunnerConfig` field, so it
  sits on `RequestInitializeOnSpawn`'s side of that mapper's dividing line).

No new type, no new interface method, no runtime object.

### `claudeSettingsArgs` — the flag goes unconditional

New shape (contract, not body):

```go
func claudeSettingsArgs(s SessionSettings) []string
```

- `Model != ""` → `--model <x>`; `Effort != ""` → `--effort <x>` (unchanged).
- **always** → `--dangerously-skip-permissions`.
- then, iff the stored posture is **not** the escalation and is an in-band
  member → `--permission-mode <mode>`, **including `default`**.

Two consequences worth stating because they are the AC-2 restatement:

- The posture slot is no longer mutually exclusive — the two flags now sit side
  by side. What *is* preserved is the property the exclusion existed for: the
  escalation keeps exactly one spelling. `permissionModeInBand` gates the value,
  and `bypassPermissions` is not a member, so no mode string can compose a bypass
  child, and the daemon never emits `--permission-mode bypassPermissions`.
- `default` now emits its flag, where it emitted nothing before. The old reason
  for the silence — "`default` **is** claude's own default, so omitting the flag
  keeps every argv byte-identical" — dies with this change: silence no longer
  reads as `default`, it reads as *whatever the unconditional bypass flag says*.
  The assembled argv is not new either: every non-bypass stream spawn already
  ends in `--permission-mode default` today, injected by `permissionArgs`;
  `withApprovalArgs`' existing `dropPermissionMode` arm (#2043) simply becomes
  the common case, which is what the ticket predicted.

A zero-value `SessionSettings` therefore no longer returns `nil`. That is the
whole point, and `Pool.mintSettings`' existing "returns nil for an unconfigured
pool" assertion changes with it.

### Fail-safe 1 — `withApprovalArgs` keys on posture + provenance

```go
func withApprovalArgs(args []string, mcpApprovePath, storedMode string, operatorBypass bool) []string
```

Early return (inject nothing) iff **the child will actually stay in bypass**:

```
storedMode == sessions.PermissionModeBypass  ||  operatorBypass
```

Everything below that is unchanged: append `permissionArgs(false, path)`, minus
its own `--permission-mode` pair when `namesPermissionMode(args)` fires.

This preserves today's behaviour on all three rows exactly — AC 3's "gated
exactly as it is today". The row that changes shape but not outcome is row 2:
today it has no flag and gets the injection; after this change it has the flag
and *still* gets the injection, because the predicate no longer reads the argv.
**That row is the one that reddens on any tree that keys on the bare presence of
the flag**, and it is the sole reason the doc's "a bypass child has no approval
gate to lose" sentence stops holding: after this change every child launches in
bypass, and the non-bypass ones have an approval gate they very much can lose.

Deliberate polarity asymmetry against fail-safe 2, stated so it is not read as
drift: an unrecognised or empty `storedMode` here falls **through** to injection
(approval gate present — the safe direction for this consumer), where the same
value in `spawnAndWait` suppresses the write (no posture asserted — the safe
direction for *that* consumer, and what keeps every non-daemon construction site
byte-identical).

Call site: `newStreamRunnerFactory` already holds `cfg`, so it passes
`cfg.PermissionMode` and `cfg.OperatorBypass`.

### Fail-safe 2 — `spawnAndWait` keys on provenance, not the argv

```go
postureID := ""
if permissionModeAllowed(spawnMode) && !r.cfg.OperatorBypass {
    postureID = r.nextControlID()
}
r.postureGate.arm(postureID)
```

The arm stays unconditional (only the id is conditional) — #2064's residual-arm
defect is not reintroduced. What changes is only the second clause, from *reading
this spawn's argv* to *reading the construction-time provenance bit*.

Why a `Config` field is correct here even though the doc it replaces argued for
per-spawn evaluation: the value it replaces was a property of the assembled argv,
which `Restart(newArgs)` can change; provenance is a property of `spawnBase`,
which nothing can. The field's doc will state the one constraint that keeps it
true — `SetSpawnArgs` / `Restart` install verbatim, so a future caller that
composes an argv from something other than `Session.spawnArgs` owns keeping this
bit in step.

### Where the bit is computed

Both `RunnerConfig` sites already have `base` in hand one statement above:

- `Pool.New`: `base := append(clone(cfg.Bootstrap.ClaudeArgs), "--settings", path)`
- `Pool.buildSession`: `base := clone(tpl.ClaudeArgs) + "--session-id" + "--settings"`

`tpl == cfg.Bootstrap`, so both bases carry the same operator pass-through. Each
site sets `OperatorBypass: operatorBypass(base)`.

### Data flow

```
operator argv ──┐
                ├─► Session.spawnBase ──► operatorBypass(base) ─┐
--session-id  ──┤                                               │
--settings    ──┘                                               │
                                                                ▼
settings ──► claudeSettingsArgs ──► spawnArgs ──► RunnerConfig{ClaudeArgs, PermissionMode, OperatorBypass}
                                                                │
                                          cmd/pyry              ▼
                            withApprovalArgs(args, path, mode, operatorBypass)   ← fail-safe 1
                            mapStreamsupConfig → streamsup.Config{SpawnPermissionMode, OperatorBypass}
                                                                │
                                          streamsup             ▼
                            spawnAndWait: permissionModeAllowed(mode) && !OperatorBypass  ← fail-safe 2
                                          → WritePermissionMode → PostureGate
```

### Out of scope, explicitly

- `permissionArgs` (`cmd/pyry/mcp_config.go`) is untouched: it is shared with
  `pyry agent-run`, where `#1387` pins the flag's absence because it defeats
  `--allowed-tools`.
- Routing the escalation in-band. Enabling bypass on a running session still
  takes `Pool.UpdateSettings`' restart branch.
- `withApprovalArgs` runs once at **runner construction**, not per spawn.
  `Pool.UpdateSettings` installs recomposed args through `SetSpawnArgs` (in-band
  branch) and `Restart` (escalation branch), neither of which re-applies the
  injection. **What this ticket changes about that pre-existing gap:** today a
  post-downgrade crash-respawn re-execs an argv with no bypass flag and no
  approval flags — an ungated non-bypass child. After this change the same
  respawn re-execs an argv that *does* carry the always-on flag and still no
  approval flags, and the spawn-time write walks it back to the stored mode
  (fail-safe 2 is per spawn and reads live `r.spawnMode`). So the posture is
  right in both trees, and the missing approval flags are the same defect before
  and after — a child in `default` with no `--permission-prompt-tool` cannot
  route a tool use to the daemon and claude prompts locally instead. Not fixed
  here; it belongs to whoever moves the injection onto the spawn path.

## Concurrency model

No new goroutines, no new locks, no lock-order change.

- `OperatorBypass` is written once at `RunnerConfig` construction and read-only
  thereafter, on both sides of the seam. `streamsup.Runner` reads it via
  `r.cfg`, which is already immutable post-`New` (the mutable analogues
  `r.args` / `r.sessionID` / `r.spawnMode` live under `restartMu`; this value has
  no mutable analogue because nothing can change it).
- `spawnAndWait` reads `r.cfg.OperatorBypass` outside any acquisition, exactly as
  it reads `r.cfg.RequestInitializeOnSpawn` and `r.cfg.ClaudeBin` today. The
  clause it replaces read `args`, a `beginSpawn` local — also unlocked. No
  acquisition is added or removed, so the one-acquisition-per-spawn-setup charter
  holds unchanged.
- `claudeSettingsArgs` stays pure and allocation-fresh; `Session.spawnArgs` still
  returns a slice aliasing neither `spawnBase` nor caller state.

## Error handling

No new error values and no new failure modes. The change is entirely in two
boolean predicates and one argv composer, none of which can fail.

The failure modes that matter are the silent ones the design has to *not*
introduce, each with the test that reddens on it:

| failure mode | consequence | caught by |
|---|---|---|
| predicate keyed on bare flag presence in `withApprovalArgs` | approval gate absent for every session | AC-3 unit row: stored `default`, argv carries the flag → injection still happens |
| predicate keyed on bare flag presence in `spawnAndWait` | no child ever downgraded; every gate open | AC-4 row 2 |
| interlock deleted outright | operator's pass-through bypass silently revoked | AC-4 row 3 |
| `--permission-mode bypassPermissions` composable | escalation gains a second spelling | AC-2 vocabulary sweep |
| revived session left in bypass | phone-granted bypass survives a restart (#1487) | AC-1 revive row |

`WritePermissionMode`'s error stays absorbed at Debug and still must not release
the gate — untouched by this change, and structurally so (nothing on that path
touches the gate).

## Testing strategy

Scenarios, by AC. No test bodies here.

**AC 1 — every child launches with the flag (`internal/sessions`).**
- `claudeSettingsArgs` table: every row carries the flag, including the zero
  value; zero value no longer returns `nil`.
- Through the pool, via a capturing `RunnerFactory`: bootstrap, minted and
  revived `RunnerConfig.ClaudeArgs` each carry the flag.
- Revive row (the #1487 restatement): a revived session's stored posture is
  `default`, its argv carries the flag **and** `--permission-mode default`, and
  its `RunnerConfig.PermissionMode` is `default` — i.e. the child is downgraded,
  not left in bypass. Reddens on any tree that lets a revived child keep bypass.

**AC 2 — composition (`internal/sessions`).**
- Non-bypass posture → flag *and* `--permission-mode <mode>` in the fixed order.
- Bypass posture → flag and **no** mode pair.
- Vocabulary sweep over `permissionModeKnown`'s members plus junk strings and the
  empty string: `claudeSettingsArgs` never emits `bypassPermissions` as a
  `--permission-mode` value.

**AC 3 — the approval set still reaches every non-bypass spawn (`cmd/pyry`,
hermetic — the live gate cannot see it).**
- `withApprovalArgs`, stored `default`, args carrying the always-on flag →
  `--permission-prompt-tool`, `--mcp-config <path>`, `--strict-mcp-config`
  present. This is the row that fails on a flag-presence tree.
- Stored `bypassPermissions` → args unchanged, no duplicate flag.
- Args already naming a mode → exactly one `--permission-mode`, the operator's;
  approval flags retained (#2043's arm, now the common case).
- No mutation, no aliasing of the input (retained).
- `newStreamRunnerFactory` passes `cfg.PermissionMode` and `cfg.OperatorBypass`
  through, and `mapStreamsupConfig` maps `OperatorBypass` onto `streamsup.Config`.
- `TestRelayV2_StreamModalPermissionRoundTrip` stays green (fake-daemon suite,
  in `make check`).

**AC 4 — the spawn-time write discriminates by provenance
(`internal/streamsup`), one row per fail-safe-2 table row.**
- Row 1: `SpawnPermissionMode = "bypassPermissions"` → nothing written, gate
  armed OPEN.
- Row 2: `SpawnPermissionMode = "default"`, `OperatorBypass = false`, **args
  carrying the flag** → the mode **is** written. Fails on a bare-flag tree.
- Row 3: `SpawnPermissionMode = "default"`, `OperatorBypass = true` → nothing
  written, gate armed OPEN. Fails on a tree that deletes the interlock.
- The residual-arm coverage #2064 added (a no-write spawn must not inherit its
  predecessor's armed id) is preserved across the rewrite.
- **Security-review addendum (`internal/sessions`):** the mode handed to the
  factory always satisfies `permissionModeKnown`, over all three construction
  paths including a bootstrap warm-start whose on-disk mode is junk. That is the
  invariant making the unknown-mode arm — whose consequence this ticket inverts
  from benign to "child stays in bypass" — unreachable.

**AC 5 — live measurement on the daemon's own spawn path
(`internal/e2e/realclaude`, `e2e_realclaude` tag).**
Extend `TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows` rather
than authoring a second rig — it already drives `sessions.New` with a seeded
`yolo:false` registry, a real `streamsup.Parser` teed beside the tap, and
`inbandSendTurn`. Two additions:
- **Instrument check** (deterministic, in the factory, before any child spawns):
  the argv the pool composed carries `--dangerously-skip-permissions`. Without
  it the run is vacuous — on a tree where the flag never went unconditional the
  posture assertion passes for the pre-#2065 reason. This is the guard the rig's
  own header calls for on every seam it measures.
- **The race-window re-proof**: the child reports the stored non-bypass posture
  at its **first** `system/init` line, not merely its last. #2060 measured that
  verdict on hand-driven argvs; this puts it on the daemon's own spawn path.
  `snapshotInitModes` already retains them in order.

Run it and read the count of `=== RUN` lines, never the exit code. Transcribe
the measured output into the test's header.

**Verification gate (§ B2):** `go test -race` on
`./internal/sessions/... ./internal/streamsup/... ./cmd/pyry/... ./internal/e2e/...`,
`go vet ./...`, `go build ./cmd/pyry`, plus the one live test above. The
full-module race suite is the verifier's gate.

## Open questions

1. **Does `--permission-mode default` sitting beside the always-on flag change
   anything at launch?** Expected no: #2061 measured exactly that argv shape
   (the flag beside `permissionArgs`' four, which include `--permission-mode
   default`) and found the child ran turn 1 in bypass with the approval gate
   intact. Resolved by AC 5's live run, which composes that argv from the daemon.
2. **Does a non-`default` in-band mode (`plan`, `acceptEdits`) beside the flag
   behave the same?** Unmeasured; not on this ticket's path, since AC 5 measures
   the `default` posture the seeder produces and the in-band write is what
   decides the posture either way. Recorded so nobody reads AC 5 as covering it.
3. **Does the always-on flag disturb the fake-daemon e2e suite?** Expected no —
   neither `fakeclaude` read loop reads the flag. Resolved by running
   `./internal/e2e/...` in § B2.

Each is resolved in Phase B and, if it changes the design, recorded under
`## Revisions`.

## Size re-check (§ A4)

| boundary | limit | this plan |
|---|---|---|
| production source files created/modified | ≤ 5 | **5** — `internal/sessions/session.go`, `internal/sessions/pool.go`, `internal/sessions/runnerstate.go`, `internal/streamsup/runner.go`, `cmd/pyry/streamsup_runner.go` |
| total written work | ≤ 800 | **over** — ~1,300 est. Stated overage; see § Context |
| new exported types or interfaces | ≤ 5 | **0** types; 1 exported const + 2 struct fields |
| consumer call sites needing simultaneous update | ≤ 10 | **4** — 1 `withApprovalArgs` call, 2 `RunnerConfig` sites, 1 `spawnAndWait` predicate |
| acceptance criteria | ≤ 5 | **5** |
| distinct error/reject branches in a state machine | ≤ 10 | **0** new |

One boundary exceeded, and it is the one the ticket instructs to absorb rather
than split, on the floor rule. Every other boundary holds.

## Revisions

### 2026-09-03 — Open questions resolved, and one that could not be

The design landed as planned; nothing in Design or Concurrency model changed. The
three Open Questions resolved as follows.

**OQ 3 (fake-daemon e2e suite) — RESOLVED GREEN.** `go test -tags e2e -race
-count=1 ./internal/e2e/...` passes, and
`TestRelayV2_StreamModalPermissionRoundTrip` was run by name and reported PASS on
all four subtests. Neither `fakeclaude` read loop reads the escalation flag, so
the always-on flag is inert there as predicted.

**OQ 1 and OQ 2 (does the flag beside a `--permission-mode` pair change anything
at launch) — NOT RESOLVED. AC 5 is written but UNEXECUTED.** The live arm
`TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows` skips in this
environment: neither `ANTHROPIC_API_KEY` nor `CLAUDE_CODE_OAUTH_TOKEN` is set, and
a skip exits 0. That is the established behaviour for a `needs-real-claude`
ticket — the dispatch environment carries no Claude login and such tickets park
for an operator's live run — not a defect in the test. Both #2065 additions (the
argv instrument check and A5's first-`system/init` read) are in the tree and
compile under `go vet -tags e2e_realclaude`; neither has been measured against a
live claude, and the test's header says so in place of a fabricated transcript.

Consequences, stated rather than left to be inferred:

- The claim AC 5 exists to re-prove — #2060's closed launch→downgrade window,
  measured on hand-driven argvs, still holding when the argv is composed by the
  daemon — rests for now on #2060's own measurement plus the posture gate, which
  refuses every user turn until the ack lands. It is not proven on this path.
- The ticket's stop-and-report rule is live and encoded in A5's failure message:
  if an operator's run finds `modes[0]` reporting the escalation, the window is
  reachable, and the response is a comment plus `needs-rework:refiner`, never a
  mitigation.

**Evidence added beyond the plan.** The plan predicted which row of AC 3 and AC 4
kills which wrong tree; those predictions were then measured with four `go test
-overlay` mutants rather than left as reasoning. Each reddened exactly its
predicted row: the old argv-presence predicate in `spawnAndWait` (AC 4 row 2), the
interlock deleted there (row 3), the old argv-presence predicate in
`withApprovalArgs` (AC 3), and its provenance clause deleted (AC 4 row 3 at the
`cmd/pyry` seam). The results are recorded in the two test headers.

**Security-review SHOULD FIX landed** as `TestRunnerConfigPermissionModeIsAlwaysKnown`
in `internal/sessions`, driving a junk on-disk mode through all three construction
paths.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No finding — verified, not assumed.** The design's whole
  safety rests on `OperatorBypass` being unreachable from anything a remote
  client controls, so the claim was checked rather than reasoned. It derives from
  `Session.spawnBase`, whose pass-through half is `SessionConfig.ClaudeArgs`;
  that field is written at exactly one production site, `cmd/pyry`'s daemon
  composition in `main.go` from the operator's `pyry supervise -- <args>` CLI
  split. No wire verb reaches it: `Pool.Create`, `Pool.CreateIn`,
  `Pool.GetOrCreateIn` and `Pool.Revive` take only `label` and `spawnDir`, and
  `Pool.buildSession` copies `p.sessionTpl` wholesale. A phone can choose a
  session's workdir, label and posture; it cannot choose a claude flag. The
  boundary is explicit and single-function on the read side (`operatorBypass`),
  and the bit is carried as a named struct field on both config types rather
  than re-derived per consumer.

- **[Subprocess execution] SHOULD FIX — the unknown-mode failure mode inverts
  polarity, so pin the invariant that makes it unreachable.** `spawnAndWait`
  writes nothing when `permissionModeAllowed(spawnMode)` is false, which covers
  the empty mode and any junk. Today that arm means "child launched without the
  flag, nothing written, child runs in `default`" — benign. After this change it
  means "child launched **in bypass**, nothing written, gate armed OPEN, turns
  flow in bypass". The polarity of that arm's consequence flips, and it flips
  silently. It is unreachable through the sessions layer — `canonicalSettings`
  runs above both `RunnerConfig` construction sites and `Pool.UpdateSettings`
  canonicalises `merged.PermissionMode` before `SetSpawnPermissionMode` — but
  "unreachable" is exactly the kind of claim that decays. Phase B pins it:
  a test over all three construction paths (bootstrap warm-start from a registry
  carrying a junk on-disk mode, minted, revived) asserting the
  `RunnerConfig.PermissionMode` handed to the factory always satisfies
  `permissionModeKnown`. Do **not** "fix" it by making streamsup's empty-mode arm
  fail-closed: that would gate every construction site outside the interactive
  daemon, which is the bricked-session shape #2064 rejected.

- **[Subprocess execution] No finding — the nil-gate pairing is already refused
  at construction.** A runner with `SpawnPermissionMode` set and `PostureGate`
  nil would write a posture nothing can confirm and leave the gate open, so a
  child could take turns in bypass with the downgrade unacknowledged.
  `streamsup.New` returns `"streamsup: SpawnPermissionMode requires PostureGate"`
  for that pairing, and `newStreamRunnerFactory` sets both from one `parser`.
  This ticket adds no construction site and does not weaken that check.

- **[Subprocess execution] The load-bearing finding, measured rather than
  argued: the launch→downgrade window is now open for every session, not only
  YOLO ones.** Between `cmd.Start` and claude processing the `set_permission_mode`
  line, a real child is in bypass. Two things close it, and neither is new code
  this ticket writes: no user turn can reach the child, because `WriteUserTurn`
  refuses on the armed `PostureGate` until the ack for that child's minted id
  lands; and #2060 measured 3/3 consecutive spawns reporting the downgraded
  posture at their own **first** `system/init` line — i.e. before claude
  published an opening posture at all. Nothing else executes in the window:
  claude acts on turns, and only `RequestInitialize` (a control request) is
  written there. AC 5 re-proves the verdict on the daemon's own spawn path, which
  is where it now has to hold. **If that live run finds the window reachable, the
  ticket stops and reports** — comment the finding, apply `needs-rework:refiner`,
  ship no mitigation. A narrowed window is still a window.

- **[Subprocess execution] No finding — the argv/provenance mis-pairings both
  fail safe.** `Runner.SetSpawnArgs` and `Runner.Restart` install an argv
  verbatim, so a future caller composing one outside `Session.spawnArgs` could
  disagree with the construction-time bit. Both directions are checked: flag on
  the argv with `OperatorBypass` false → the write happens and the child is
  downgraded (safe); `OperatorBypass` true with no flag on the argv → no write at
  a child that is not in bypass anyway (neutral). Neither yields a child in
  bypass that the daemon believes it downgraded. The field's doc carries the
  constraint so the next caller inherits it.

- **[Subprocess execution] OUT OF SCOPE — an operator-granted bypass cannot be
  tightened across a respawn, before or after this ticket.** Operator launches
  with the pass-through flag; a phone sets `plan`; `Pool.UpdateSettings` delivers
  it in-band to the live child, then a crash-respawn puts the child back in
  bypass because the daemon writes nothing for an operator-granted bypass.
  Verified identical on `main`: today's `spawnAndWait` reads the same flag off
  the recomposed argv and suppresses the same write, so this ticket neither
  creates nor widens it. It is arguably correct — the machine owner's explicit
  grant outranking a remote tightening — but it is undocumented, and this design
  makes the predicate that decides it more prominent rather than less. Belongs to
  whoever routes the escalation in-band; recorded as a PR lesson.

- **[Subprocess execution] No finding — `pyry agent-run` is untouched.** The
  always-on flag lives in `claudeSettingsArgs`, not `permissionArgs`, which
  `agent_run.go` shares. `#1387` pins the flag's absence on that path because it
  defeats `--allowed-tools`, and that test stays green by construction.

- **[Error messages, logs] SHOULD FIX (documentation, not code) — the daemon's
  own log stops distinguishing a bypass session from a downgraded one.**
  `Runner.Run` logs the composed argv at Info ("spawning claude"), so after this
  change every session's record shows `--dangerously-skip-permissions`. Nothing
  secret leaks — the flag is not a credential and the mode value never reaches a
  record (`WritePermissionMode`'s refusal is a bare sentinel, and #833 keeps
  settings values out of the log) — but an operator reading logs can no longer
  read the posture off the argv. The stored posture, not the argv, is the answer,
  which is precisely this ticket's thesis. No redaction and no new record is
  warranted; carried as a PR lesson for the package overview.

- **[Concurrency] No finding.** `OperatorBypass` is write-once at config
  construction on both sides. `streamsup.Runner` stores `cfg` by value in `New`
  and never reassigns it, so the unlocked read in `spawnAndWait` matches how
  `cfg.RequestInitializeOnSpawn` and `cfg.ClaudeBin` are already read there. The
  clause it replaces read a `beginSpawn` local, also unlocked, so no acquisition
  is added or removed and the one-acquisition-per-spawn-setup charter is
  unchanged. No goroutine, channel, or lock is introduced.

- **[Tokens, secrets, credentials] Not applicable — no token, key or credential
  is created, stored, compared or logged. The `request_id` correlating the
  posture ack is minted by `nextControlID`, an unexported per-runner counter, and
  is unchanged by this ticket.

- **[File operations] Not applicable — no path is constructed, opened, or
  created. `writeMCPSettings` and the registry write are untouched.

- **[Cryptographic primitives] Not applicable — no randomness, hashing, key
  material or comparison of attacker-controlled values against a secret.

- **[Network & I/O] Not applicable — no socket, listener, deadline or size cap
  is added; the change is confined to argv composition and two in-process
  predicates.

- **[Threat model alignment] No finding.** The relevant `docs/protocol-mobile.md`
  § Security model threat is an authenticated-but-hostile phone escalating its
  own session. Each fail-safe it relies on survives: the escalation still needs
  the YOLO bit and keeps exactly one spelling (AC 2's vocabulary sweep);
  `Pool.UpdateSettings` still rejects an unknown mode and a self-contradictory
  frame before mutating anything; `Pool.Revive`'s zero settings still downgrade a
  revived child, so a phone-granted bypass still does not survive a daemon
  restart (#1487, AC 1's revive row); and the phone cannot reach the one input
  that suppresses a downgrade, per the trust-boundary finding above.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
