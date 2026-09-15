# #2446 — reapply the construction-time argv shapings on the settings-update install path

## Files read

- `cmd/pyry/streamsup_runner.go` → `streamRunner`, `streamRunner.Restart`,
  `streamRunner.SetSpawnArgs`, `streamRunner.SetSpawnPermissionMode`,
  `newStreamRunnerFactory`, `withApprovalArgs`, `mapStreamsupConfig`,
  `stripSessionIDFlags` — the adapter that forwards the install verbatim, and the
  two shapings the construction path applies that the install path does not.
- `internal/sessions/pool.go` → `Pool.UpdateSettings` — the one caller of both
  install methods; `SetSpawnPermissionMode(merged.PermissionMode)` sits
  unconditionally above the branch split, which is what lets the adapter know the
  live posture before it has to shape anything.
- `internal/sessions/pool.go` → `Pool.New`, `Pool.buildSession` — the two
  `RunnerConfig` sites: `OperatorBypass` is derived from the settings-free `base`
  at both, and `buildSession` is the site that bakes `--session-id <id>` into
  `Session.spawnBase`.
- `internal/sessions/session.go` → `Session.spawnArgs`, `claudeSettingsArgs`,
  `operatorBypass` — the recompose is `spawnBase + claudeSettingsArgs`, so the
  recomposed argv carries the baked id and never carries approval flags.
- `internal/streamsup/runner.go` → `Runner.Restart`, `Runner.SetSpawnArgs`,
  `Runner.setArgsLocked` — the install is verbatim and the runner exposes no
  reader for the installed argv, which decides how the unit test observes it.
- `internal/e2e/internal/fakeclaude/main.go` → `main`, `argvIDFlag`, `fatalf` —
  the fake takes the LAST id flag, so it treats `--session-id X … --resume X` as
  a plain resume and hides the defect.
- `internal/e2e/stream_absent_transcript_respawn_test.go` →
  `TestE2E_StreamNeverEstablishedSession_RespawnsWithCreateForm` — the template
  for deriving the daemon's real probe directory in a test.
- `internal/e2e/per_conversation_eviction_test.go` → `createConversationViaPhone`,
  `activateViaTurn` — how a phone-created session (the only kind with a baked id)
  comes up in the fake tier, and the evict → reactivate respawn.
- `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` →
  `driveInteractiveStreamPermissionDeny`, `startStreamModalResolutionHarness` —
  the live permission round-trip the new live arm reuses verbatim.

## Context

`Pool.UpdateSettings` recomposes a session's spawn argv from `Session.spawnBase`
plus `claudeSettingsArgs` and installs it through the adapter — `SetSpawnArgs` on
the in-band branch, `Restart` on the other. Both adapter methods forward the argv
to `streamsup.Runner` unchanged, but the CONSTRUCTION path does two things to an
argv before it reaches the same runner field:

1. `mapStreamsupConfig` applies `stripSessionIDFlags`, because `streamsup.buildArgs`
   injects the create (`--session-id`) or resume (`--resume`) form itself per spawn.
2. `newStreamRunnerFactory` applies `withApprovalArgs`, which puts the daemon's
   approval flags on the argv of every child the daemon downgrades in band.

Neither runs on the install path, so after any live settings change:

- the baked `--session-id <id>` survives, and the next respawn that takes the
  resume form spawns `--session-id X … --resume X`, which claude refuses outright
  (`exit 1`, one stderr line). Observed on pyrybox 2026-09-15: every respawn died
  in a quarter second and the queue stalled until the daemon was restarted.
- the approval flags are absent, so a downgraded session respawns with no
  `--permission-prompt-tool`, no `--mcp-config` and no `--strict-mcp-config`
  beside the `--dangerously-skip-permissions` every argv now carries. Masked today
  by the crash-loop; fixing the id strip alone is what makes it reachable.

This is not an ADR-sized decision — it restores an existing invariant on a second
call site rather than moving a boundary — so the documentation stage has no
decision record to write, only the three package overviews the ticket names.

## Design

### The seam: one installer object, three one-line forwards

Fixing it in `Pool.buildSession` (dropping the baked id) is rejected: eight test
files assert the baked `--session-id`, and `RunnerConfig.SessionID` is fed from
that same site. Fixing it in `Pool.UpdateSettings` is rejected too — `internal/sessions`
knows nothing about `mcpServersPath`, the stdio bit or claude's approval flags,
and must not.

So the shaping stays in `cmd/pyry`, in one object the factory owns:

```go
// The half of *streamsup.Runner that Pool.UpdateSettings drives.
type spawnArgvInstaller interface {
	Restart(args []string)
	SetSpawnArgs(args []string)
	SetSpawnPermissionMode(mode string)
}

type settingsInstaller struct {
	target         spawnArgvInstaller
	mcpServersPath string
	stdioPrompt    bool
	operatorBypass bool

	mu   sync.Mutex
	mode string // the session's stored posture, as last installed
}
```

Contract, no bodies:

- `newSettingsInstaller(target, mcpServersPath string, stdioPrompt, operatorBypass bool, mode string) *settingsInstaller`
  — built inside `newStreamRunnerFactory` from the same four values
  `withApprovalArgs` is already called with there, so the construction path and
  the install path cannot be given different shaping inputs.
- `(*settingsInstaller).shape(args []string) []string` — returns
  `withApprovalArgs(stripSessionIDFlags(args), mcpServersPath, <live mode>, operatorBypass, stdioPrompt)`.
  Same two functions in the same order as construction, so for one session and
  one posture the shaped argv equals what the construction path produces (AC2).
- `(*settingsInstaller).setSpawnPermissionMode(mode string)` — stores mode, then
  forwards. `Pool.UpdateSettings` calls it unconditionally above the branch split,
  so the posture the installer shapes with is the posture the update just persisted.
- `(*settingsInstaller).restart / .setSpawnArgs` — `target.X(s.shape(args))`.

`streamRunner` gains one pointer field, `install *settingsInstaller`, and its
three install methods become forwards to it. The adapter stays a value type and
the compile-time `sessions.Runner` assertion is unaffected; the four zero-value
`streamRunner{}` interface assertions in the test file never call these methods,
so a nil installer there is inert.

### Why an interface for the target rather than the concrete runner

`*streamsup.Runner` exposes no reader for its installed argv (`setArgsLocked` is
the sole writer and there is no getter), so a unit test driving the adapter
against a real runner could only observe the argv by spawning a child and reading
its command line — a timing-dependent integration test for a pure transformation.
The three-method interface lets the unit test drive the REAL adapter methods
against a recording target and assert the exact installed argv, with no child and
no clock. It is the narrowest seam that does that: three methods, one production
implementer (`*streamsup.Runner`, structurally), one test double.

### Double-injection

`Session.spawnArgs` output never carries approval flags — they are injected into
`streamsup.Config.Args` at construction, not into `spawnBase` — so reapplying
`withApprovalArgs` on the install path cannot double-inject. `claudeSettingsArgs`
does append `--permission-mode <mode>` for a non-bypass posture, which is exactly
the case `withApprovalArgs` already handles by dropping its own mode pair.

`stripSessionIDFlags` is idempotent and allocates a fresh slice, so the shaped
argv aliases neither the caller's slice nor `spawnBase`.

### The fake's argv refusal

`fakeclaude`'s `argvIDFlag` takes the LAST id flag, so it reads
`--session-id X … --resume X` as a plain resume and serves turns real claude
would refuse. A new unconditional guard at the top of `main` — above the
stream-mode branch, so every mode inherits it — refuses the combination claude
refuses: a `--session-id` together with a `--resume` or `--continue`, with no
`--fork-session`, exits 1 with claude's own line

```
Error: --session-id can only be used with --continue or --resume if --fork-session is also specified.
```

It is ungated by env, like the stem guard in `argvIDFlag`: a fidelity guard that
only fires when the daemon composes an argv claude rejects is not a rider.

## Concurrency model

No new goroutines. `settingsInstaller.mode` is written by
`setSpawnPermissionMode` and read by `shape`; both run on whatever goroutine
calls `Pool.UpdateSettings` (today one at a time under no lock, past `p.mu`), and
`Pool.Activate`'s respawn reads the argv from the runner, not from here. The
mutex is there so a future second writer cannot make it a race; it is a leaf —
`shape` makes no call-out under it, and the forward to `target` happens after the
unlock. The other three fields are immutable after construction.

Lock order is untouched: no Pool lock is taken here, and the installer's mutex is
never held across a call into `streamsup`.

## Error handling

None of the three install methods can fail, upstream or downstream, and this
change adds no failure mode: `shape` is total over any argv. The two degenerate
inputs are already handled by the functions it composes — a dangling id flag
drops the lone token (`stripSessionIDFlags`), and an unrecognised or empty stored
posture falls through to injection (`withApprovalArgs`), which is the fail-safe
direction for this consumer because it leaves the approval gate present.

A nil `install` field cannot occur on a runner the factory built; it can only
occur on a hand-built `streamRunner{}`, which is a compile-time assertion
vehicle in this package's tests and calls nothing.

## Testing strategy

**Unit (`cmd/pyry/streamsup_runner_test.go`), the AC1 + AC2 proof.** A recording
`spawnArgvInstaller` double plus a real `streamRunner` wrapping the installer:

- Both install methods, table-driven over the two id-flag spellings and the
  dangling form: the installed argv carries no baked `--session-id` and no
  `--resume`, so `streamsup.buildArgs` remains the only source of an id flag.
- Downgraded session (stored posture `default`, `operatorBypass` false): the
  installed argv carries `--permission-prompt-tool`, `--mcp-config` and
  `--strict-mcp-config`, and only the mode pair `claudeSettingsArgs` composed.
- Bypass-keeping session, both provenances (stored posture `bypassPermissions`;
  and `operatorBypass` true): the installed argv gets nothing injected.
- Construction agreement (AC2): for one `RunnerConfig` and one posture, the
  installer's output equals `withApprovalArgs(mapStreamsupConfig(cfg).Args, …)`.
- Posture movement: `SetSpawnPermissionMode` to a downgraded mode, then install →
  approval flags present; the same sequence into the escalation → nothing injected.
  This is the value that moves, so it gets its own case.
- The stdio bit reaches the shaped argv (`--permission-prompt-tool stdio`).

**Fake-daemon e2e (`internal/e2e/relay_v2_stream_settings_respawn_test.go`), AC3.**
A phone-created conversation (the only session kind with a baked id) with a live
child: create the conversation, drive a turn to bring the child up, seed
`<session id>.jsonl` in the daemon's real probe directory so the NEXT spawn takes
the resume form, send `set_session_settings` with a permission mode, wait for
`session_settings_updated`, then force the child to exit and drive a second turn.
The assertion is that the second turn's reply reaches the phone. On main the
respawn argv is `--session-id X … --resume X`, the new fake guard exits 1, and
the daemon crash-loops with the turn stuck — red for the ticket's exact reason.

The forced exit uses idle eviction → reactivate rather than a targeted SIGKILL:
the control plane reports a `child_pid` for the bootstrap runner only, so a
per-conversation child has no pid on any surface a test can read, and the
incident report names idle eviction as one of the two triggers anyway.

**Live (`internal/e2e/realclaude/interactive_stream_permission_deny_test.go`),
AC4.** A new arm on the existing modal-resolution harness rather than a new file:
settings change on a live session, force the child to exit, then run the existing
`driveInteractiveStreamPermissionDeny` round-trip against the respawned child. It
covers both defects at once on real claude — the id defect kills the respawn, and
the approval defect lets the gated Write execute with no modal at all, which the
helper's non-vacuity gate already fails on.

**Fake unit (`internal/e2e/internal/fakeclaude/argv_session_id_test.go`).** A
table on the new predicate beside the existing `argvIDFlag` table: refused pairs
(`--session-id` + `--resume`, `--session-id` + `--continue`, joined spellings),
and accepted ones (either flag alone, both plus `--fork-session`).

## Open questions

- Whether the live arm can force the respawn by pid or must reuse the eviction
  route the fake arm takes. Resolved at implementation against the realclaude
  harness's own surface; the choice changes no production code.
- Whether any existing fake-tier test composes `--session-id` together with
  `--resume` and would newly be refused by the fidelity guard. Expected none —
  the daemon only composes that pair through the defect this ticket fixes — and
  the e2e run settles it.

## Documentation handoff

Pending for the documentation stage; the builder does not edit these:

- `docs/knowledge/features/streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md`
  — `stripSessionIDFlags` and `withApprovalArgs` are no longer construction-only.
  Record where the second application happens and why.
- `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md` —
  both install branches hand the adapter a base the adapter shapes; the pool still
  composes only from `spawnBase` plus `claudeSettingsArgs`.
- `docs/knowledge/features/e2e-harness.md` — the fake's new argv refusal,
  alongside its other claude-fidelity guards.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No design change — the boundary this touches is
  `Pool.UpdateSettings`' recomposed argv on its way to `exec.Command`, and the
  shaping reads only tokens the daemon itself composed: `stripSessionIDFlags`
  matches two constant flag spellings and `withApprovalArgs` decides on
  `cfg.OperatorBypass` plus the stored posture, never on a device-supplied
  string. No device value is parsed, re-emitted or interpreted here.
- [Subprocess execution] MUST HOLD, and it IS the design rather than a revision:
  `operatorBypass` comes from `sessions.RunnerConfig.OperatorBypass` — derived at
  the pool from the SETTINGS-FREE `spawnBase` — and must never be re-derived from
  the argv being installed. `claudeSettingsArgs` appends
  `--dangerously-skip-permissions` to every composition since #2065, so a
  predicate reading the installed argv answers "bypass" for every session and
  drops the daemon's approval gate from every install, silently. That is defect 2
  of this ticket re-introduced through the fix for it. The same trap applies to
  the posture cell: it is seeded from `cfg.PermissionMode` and moved only by
  `SetSpawnPermissionMode`, never read back off an argv.
- [Subprocess execution] No findings on the fail-safe direction — an unrecognised
  or empty posture falls through to injection in `withApprovalArgs`, so the
  degenerate cell value leaves the approval gate PRESENT. A nil installer is
  unreachable on a factory-built runner.
- [Subprocess execution] SHOULD FIX — the shaping is not idempotent for the
  approval set: handed an already-shaped argv it would append a second
  `--permission-prompt-tool` / `--mcp-config` pair. The only caller recomposes
  from `spawnBase` every time, so it is unreachable today. Phase B states the
  precondition on the installer's doc — the argv it takes is `Session.spawnArgs`
  output, never the runner's current argv — and the verifier checks it landed.
- [Subprocess execution] OUT OF SCOPE — `stripSessionIDFlags` matches its flag
  spellings positionally, so a Model or Effort whose VALUE is the literal
  `--session-id` / `--resume` mangles the argv around it. Reachability and blast
  radius are identical on the construction path, which strips the same composed
  argv at every daemon start, so this change neither widens nor narrows it; the
  mangle drops the token that follows, which for every composition
  `claudeSettingsArgs` produces is the bypass flag or another settings token, so
  it is fail-safe (more restrictive, never an escalation). A future ticket that
  wants it closed closes it at the relay's settings validator, where the value is
  still a value.
- [Concurrency] No exploitable window, and the derivation matters because the
  cell is the one value that moves. Two concurrent `UpdateSettings` calls on one
  session can interleave between `p.mu`'s release and the
  `SetSpawnPermissionMode` + install pair — a pre-existing window, since those
  two calls were already unsynchronised — so an argv composed by update A can be
  shaped with update B's posture. Both interleavings still track the LAST
  persisted posture: the losing argv's `--permission-mode` is stale, but claude's
  bypass flag wins at launch and the runner's own spawn-time in-band write
  carries the winning update's posture, so the child cannot end up looser than
  what was persisted. The installer's mutex is a leaf — no call-out under it, the
  forward to the runner happens after the unlock — so it adds no lock-order edge.
- [Errors, logs, telemetry] No findings, and one thing deliberately NOT added: no
  diagnostic logs the shaped argv. It carries Model and Effort, which are settings
  values #833 keeps out of the daemon log. The fake's refusal line is claude's own
  constant and names no id.
- [File operations] No findings — the change opens no file and builds no path.
  The fake-tier arm writes one transcript fixture under the test's own temp home,
  named from the id the daemon minted and published in its registry, not from any
  value a test or a device composed.
- [Tokens, secrets, credentials] N/A — no credential, token or key is read,
  written, compared or transported on this path.
- [Cryptographic primitives] N/A — no randomness and no primitive is involved;
  the change is a pure argv transformation.
- [Network & I/O] N/A — no socket, listener, deadline or size cap is introduced;
  both e2e arms drive the existing harnesses.
- [Threat model alignment] The relevant threat is a paired device causing the
  daemon to spawn a claude child with the approval gate absent — which is defect 2
  of this ticket, realized today through a device-driven settings change. The
  design closes it by making the install path apply the same `withApprovalArgs`
  the construction path applies. Device authorization and the posture vocabulary
  stay where they are, at the relay's `set_session_settings` handler; this change
  adds no authorization decision and removes none.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-15

## Revisions

### 2026-09-15 — the live arm covers one defect, not both

Both Open Questions are resolved, and the first one moved the live arm's claim.

The live arm forces the respawn by pid after all: `bootstrapDaemon` carries the
control socket, so the child is read off `control.Status` and SIGKILLed, and the
successor is waited for by pid change. That cost one additive line —
`startObservedPermissionHarness` now puts the daemon on the `perConvHarness` it
returns, a field that already existed for its stderr and that every other case
ignores.

What did change: that harness seeds a conversation bound to the BOOTSTRAP
session, whose `spawnBase` carries no baked id, so the id half of the defect is
not reachable on it and the live arm proves the APPROVAL half —
`withApprovalArgs` reapplied, without which the respawned child runs the gated
Write with no modal at all. The id half is proven by the fake-tier arm, which
drives a phone-created session, and by the adapter's unit table. The plan's
"covers both defects at once" reading was wrong about which session kind the live
modal harness uses; the arm's doc states the split.

The second question — whether any existing fake-tier test composes the refused
pair and would newly be refused by the fidelity guard — is settled: a full
`internal/e2e` tier run is green (113s), so no arm in that tier composes the pair
and the unconditional guard costs the suite nothing.

The settings change the live arm sends is an EFFORT change rather than a
permission mode. A posture change would alter whether the Write is gated at all,
which is the very thing the round-trip measures, and a model change would move
the round-trip onto a different model. Effort is in-band deliverable, so it still
drives the `SetSpawnArgs` branch the incident took.

The RED was measured rather than assumed: with `shape` reduced to a pass-through,
the fake-tier arm reproduces the incident exactly — the daemon logs
`--session-id X … --resume X`, `claude exited err="exit status 1"
uptime=9.5ms`, and a widening backoff — and the unit table fails on every row.
