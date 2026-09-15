# `Pool.UpdateSettings` (#840, `PermissionMode` #2043, keeps the spawn posture current #2064, escalation routed in-band #2066)

The persistence seam the v2 settings verb (#841, split into wire vocabulary #844 + handler #845) calls to change an existing session's `Model` / `Effort`
/ permission posture after creation — `SessionSettings` above was immutable
post-construction until this ticket.

```go
type SettingsUpdate struct {
    Model          *string
    Effort         *string
    YOLO           *bool
    PermissionMode *string
}

func (p *Pool) UpdateSettings(id SessionID, update SettingsUpdate) error
```

`SettingsUpdate` is the presence contract: a `nil` field leaves the stored
value untouched; a non-nil field overwrites it, including `""` for
`Model`/`Effort` and `false` for `YOLO` — both distinguishable from omitted.
`YOLO`'s `*bool` is the security-relevant choice: an absent (`nil`) `YOLO` can
never enable bypass, only an explicit non-nil `*true` can (fail-safe-OFF by
construction, not convention). `PermissionMode` has no `""`-means-omit reading
at all — unlike `Model`/`Effort`, the default posture is a *nameable* mode, so
an explicit empty string is rejected the same as any other unrecognised value.

**`YOLO` and `PermissionMode` update one posture, and `validatePermissionUpdate`
runs before any mutation.** It returns `ErrUnsupportedPermissionMode` for a
mode outside `permissionModeKnown` and `ErrPermissionModeConflict` for a mode
and a `YOLO` in the same frame that disagree (`(mode == bypassPermissions) !=
*yolo`) — rejection, not a precedence rule, because letting the mode win would
downgrade an escalation silently and letting `YOLO` win would grant one from a
frame that said `false`. Both are pre-mutation: a rejected frame leaves the
stored settings, the registry file and the running child byte-identical to
their prior state. Then the merge:

| update carries | stored mode becomes | stored `YOLO` becomes |
|---|---|---|
| `yolo:true` | `bypassPermissions` | `true` |
| `yolo:false`, stored mode is `bypassPermissions` | `default` | `false` |
| `yolo:false`, stored mode is anything else | unchanged | `false` |
| mode `bypassPermissions` | `bypassPermissions` | `true` |
| any other known mode | that mode | `false` |

Row three is the one to read twice: a `yolo:false` must not drag an operator
out of `plan` and into `default` as a side effect of naming a field it was not
asked about. When both fields are present and consistent (already checked
above), the mode arm runs and the `YOLO` arm is skipped — not a precedence
call, since they derive the same pair.

Same shape as `Pool.Rename`: takes `Pool.mu` (write), looks up the session
(miss → `ErrSessionNotFound`, no entry created), overlays present fields onto
a copy of `sess.settings`, no-op short-circuits if nothing changed
(`SessionSettings` is comparable), else swaps in the merged value and calls
`saveLocked`, rolling the field back to its previous value if the save fails.
Never takes `Session.lcMu` — `settings` is a `Pool.mu`-guarded field, same as
`label`, so no lock-order hazard with `saveLocked`'s internal `lcMu`
re-acquire (`docs/lessons.md` § "Lock order with callback into the host").

Validating untrusted model/effort values is explicitly **not** this method's
job — it operates on operator-trusted input. The relay handler owns the
charset/length shape check for `Model` and closed enum for `Effort`; for a
non-empty model, `cmd/pyry`'s `settingsUpdaterAdapter.UpdateSettings` then owns
the exact membership check against the retained published vocabulary before
this method can mutate or deliver anything. See [Inbound
`set_session_settings`](v2-session-manager-state-machine-inbound-set-session-settings-settingsupd.md).

**Live-apply on a real change (#842, #1581).** After a successful persist of a
real change (not a no-op, not a failed save), `UpdateSettings` recomposes the
session's full spawn argv and live-applies the change — so a single client
message both persists **and** takes effect on the currently-running child,
without waiting for the session's next spawn.
`Session.spawnBase []string` holds the settings-free argv (template/bootstrap
args + any construction-time resume suffix), set alongside the full
`ClaudeArgs` at both construction sites (`Pool.New`, `Pool.buildSession`).
`Session.spawnArgs(settings SessionSettings) []string` —
`append(slices.Clone(s.spawnBase), claudeSettingsArgs(settings)...)` — is the
**single** argv-recompose path outside construction, reusing `claudeSettingsArgs`
verbatim so the YOLO fail-safe has exactly one origin. `UpdateSettings` captures
`newArgs := sess.spawnArgs(merged)` and `sup := sess.sup` under `Pool.mu`, then
releases the lock before either live-apply branch runs — **outside** `Pool.mu`,
never touching `Session.lcMu`.

**`sup.SetSpawnPermissionMode(merged.PermissionMode)` runs unconditionally on the line
above the branch split (#2064)**, the one place both branches pass through. Without it
a runner's spawn-time posture write is construction-time-only — nothing rebuilds the
runner here — so it would go stale the moment an operator changed the stored posture,
and a later crash-respawn would silently re-assert the old one (able to *loosen* a
posture just tightened). Two cheaper shapes were rejected first: piggybacking the
install on `SetPermissionMode` covers every in-band `X → Y` change but not `X →
bypassPermissions`, which takes the restart branch below and never reaches
`SetPermissionMode` at all; deriving the posture from the installed argv dies the
moment #2065 removes the posture from the launch argv. See [streamsup-package's
Posture gate § Keeping the spawn posture in
step](streamsup-package-posture-gate-spawn-permission-mode-ack.md#keeping-the-spawn-posture-in-step).

**Which branch, and why (#1581, redrawn by #1604, generalised by #2043, the
escalation opened by #2066).**
`inBandDeliverable(update)` partitions on what the update carried — which
fields, and for the posture its *value* too — never on merged-vs-previous per
field. `SetSessionSettingsPayload`'s `omitempty` pointers are a presence
contract, so a client changing one setting sends one field, and a present
`YOLO`/`PermissionMode` is read for its direction rather than diffed against
stored state. Since #2043 one posture is expressed by two fields, so an update
naming *either* is an update to the posture. Since #2066 the routing question
is membership in `permissionModeKnown` — every posture this daemon can
store — not `permissionModeInBand`, which stays the five NON-escalating modes
on purpose (see [`SessionSettings` /
`claudeSettingsArgs`](sessions-package-key-types-sessionsettings-claudesettingsargs.md)
for why `permissionModeInBand` itself must not widen: three *other* readers of
that predicate — `canonicalPermissionMode`, `claudeSettingsArgs`,
`permissionModeForDisk` — depend on it staying exactly five, and reusing
`permissionModeKnown` here rather than widening `permissionModeInBand` in
place is what keeps them safe.):

- **A change claude accepts on the already-open stream** — a non-empty
  `Model`/`Effort`, and/or an update whose resulting posture is one of the
  **six** storable modes (refused by non-membership: `update.PermissionMode !=
  nil && !permissionModeKnown(*update.PermissionMode)` returns `false`, so
  every unanticipated spelling falls out here, but `bypassPermissions` no
  longer does; the `update.YOLO != nil && *update.YOLO` clause that used to
  refuse the bit spelling is gone outright, since `internal/relay`'s
  `validPermissionMode` means a mobile client can only ever spell the
  escalation as `yolo:true` and that clause was the one place it was still
  refused) — →
  `sup.SetSpawnArgs(newArgs)` then `deliverSettingsInBand`, which writes
  model as one newline-terminated `set_model` control request via
  `sup.SetModel`, `/effort <v>` as the remaining ordinary user turn via
  `sup.WriteUserTurn(context.Background(), "", …)`, and the **resulting**
  posture — including the escalation — as one `set_permission_mode` control
  request via `sup.SetPermissionMode(merged.PermissionMode)` — model, then
  effort, then posture, **exactly one send** for each present setting and no
  user turn for a model-only change. A model acknowledgement is consumed as a
  normal control response but does not retarget `PostureGate`; unlike spawn and
  permission posture, model acknowledgement is not a turn-admission condition.
  There is exactly one posture send for an update naming either
  posture field.
  That arithmetic is deliberate and load-bearing: the pre-#2043 shape (a mode
  clause kept beside the old `!*update.YOLO → RevokeBypass()` clause) would
  emit *two* identical control requests for one revocation, because the
  derivation table above makes a `yolo:false` update also carry a non-bypass
  mode; dropping the old clause with no replacement would emit *zero* and
  silently end the revocation this path performs today. `RevokeBypass` was
  `SetPermissionMode("default")`, so the wire bytes of a revocation are
  unchanged by the #2043 collapse — what changed is which method sends them,
  and that `RevokeBypass` is off the `sessions.Runner` seam entirely (see
  [Runner interface](sessions-package-key-types-runner-interface-runnerfactory.md)).
  A `yolo:false` against a stored non-default mode re-sends that mode to a
  child already in it — the same redundancy this path already tolerates for
  an unchanged model re-sent alongside a new effort. Claude accepts all sends
  on the stream the daemon already holds open and applies them to the running
  session, so **nothing is killed and the transcript survives**, and since
  #2066 that now includes the escalation: every child has launched with
  `--dangerously-skip-permissions` since #2065, so `bypassPermissions` is a
  request claude has been measured (#2060) to accept back on such a child
  without a respawn. The
  `SetSpawnArgs` call is not optional: it is `Restart`'s swap half (#1580),
  and skipping it would let the operator's change silently revert on the next
  crash-respawn or evict → `Activate` — this is what makes a revocation
  survive those too, with no new mechanism. Swap **before** write — the
  install is the durable half. Delivery is fire-and-forget: every write error
  is logged at `Info`
  (`"sessions: in-band settings command not delivered"`, fixed event
  `sessions.settings.delivery_err`, fields `session` / a
  fixed `setting` literal (`"permission_mode"` for the posture) / `err` —
  **never** the value, the payload bytes, or the conversation id) and
  swallowed, so the client sees success. They cannot be classified anyway:
  `internal/sessions` must not import `internal/streamsup`, and the reachable
  set (`ErrNoLiveChild`, `turncommit.ErrDropped`, a wrapped pipe failure, or
  the seam's own bare unsupported-mode sentinel — which does not echo the
  rejected string, so it may be logged verbatim) all warrants the same
  response, with the dominant case — an evicted session — not a degradation.
- **Everything else** → `sup.BeginTeardown()` then `sup.Restart(newArgs)`
  (the arm added by #1513, **inside** this branch only — the in-band branch
  above tears no child down, so arming there would refuse turns for a
  delivery that kills nothing). Since #2066 the escalation no longer reaches
  this branch, so what is left is clearing model or effort to `""` ("run at
  claude's own default", which `claudeSettingsArgs` expresses by *omitting*
  the flag; the control layer's reset spellings do not change Pyrycode's
  explicit-clear contract) — including a `yolo:true` or `bypassPermissions`
  mode mixed into the same frame as a cleared `Model`/`Effort`. The
  empty-value reject wins, but costs nothing: the restart recomposes argv
  from the **merged** settings, so the respawn still carries the escalation.
  No frame can lose a posture change by mixing. Clean partition — never both
  mechanisms for one change, no case left unserved. Without the arm, a write
  already past the delivery seam when `Restart` tears the child down lands in
  its doomed stdin pipe and is silently dropped as a false commit — see
  [Rotation-delivery gate (#1330) §
  teardown](streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md).

**A no-log test must capture the pool's logger, not a spawning runner's.**
`(*streamsup.Runner)` already logs the full spawn argv at `Info`
(`"spawning claude", "args", args`), which has always exposed `--model` and
`--effort` and now exposes `--permission-mode` identically — that is
pre-existing and out of this path's no-log rule, which is scoped to the
delivery record and the two rejection errors above. A no-log test built
against a pool that actually spawns a child would fail for that pre-existing
reason and invite an out-of-scope fix to the spawn record; #2043's test drives
`UpdateSettings` and its rejections against a buffer-backed logger with no
live spawn in the loop.

The mechanism swap was a bug fix, not an optimisation. The respawn re-execs
with `--resume` on a session that has never run a turn; claude answers
`No conversation found with session ID` and exits 1, and the daemon retries
forever on a widening backoff (observed 2026-08-18). The in-band path
**avoids** that rather than fixing it — no resume, no lost transcript, no
crash-loop. Live-applying a `YOLO` revoke is #1604; the enable direction gained
its own in-band form at #2066, once #2065 put the launch argv permanently
ahead of the stored posture. #1574 may **not** delete `Restart`: clearing
`Model`/`Effort` to `""` is a live production caller that remains. **#1605 was split, not
landed as such**: the live-claude proof that this composed path (`Pool` →
`inBandDeliverable` → `deliverSettingsInBand` → `Runner.SetPermissionMode`,
formerly `Runner.RevokeBypass` until #2043 collapsed the pair) reaches a real
child without tearing it down is #1622, measured against claude 2.1.220 — see
[`e2e-realclaude.md`](e2e-realclaude.md#interactive_stream_inband_bypass_revoke_test-go-1622).
The question #1605 also implied but #1622 deliberately leaves open — whether
the revoked posture is *behaviourally enforced*, not just echoed back — is a
sibling ticket that consumes #1622's harness, not yet landed. See
[codebase/1581.md](../codebase/1581.md) and
[codebase/1604.md](../codebase/1604.md).

`Supervisor.Restart(args []string)` (`internal/supervisor`) swaps the live
spawn args under a leaf `restartMu` and, if a child is currently running,
forces it to exit (SIGKILL via a per-iteration derived ctx) so the
supervisor's existing forever-retry loop relaunches it with the new argv,
resuming the conversation. When no child is running, the swap alone applies
on the session's next spawn (e.g. its next `Activate`) — which incidentally
closes a latent gap: a *reused* supervisor (evict → activate within one
process) previously kept stale baked args across that boundary. `Restart` is
non-blocking and fire-and-forget by design — see [ADR
031](../decisions/031-settings-restart-fire-and-forget.md) for why the AC
"a failed live-apply must not surface as a false success" is satisfied by a
deterministic kill + the supervisor's retry guarantee rather than a
synchronous wait. Not-found, no-op, and persist-failure paths in
`UpdateSettings` all return before `Restart` is ever called — a still-correct
running child is never disturbed. See [codebase/842.md](../codebase/842.md)
and `docs/specs/architecture/842-live-restart-on-settings-change.md` (§
Security review, verdict PASS) for the full design and the argument that the
YOLO fail-safe survives the restart.

**Security (`security-sensitive` ticket).** `YOLO`'s fail-safe posture — a
missing or corrupt on-disk value can never enable bypass — rests entirely on
`YOLO` being a plain `bool` (Go zero value = `false`) plus the pre-existing
`loadRegistry` whole-parse strictness (a malformed value fails the entire load,
not just that field). No new decoder was needed. See [ADR
030](../decisions/030-plain-bool-failsafe-persisted-flag.md) for the full
argument. Model/effort values on this path are operator-trusted (local 0600
registry, delivered to `exec.CommandContext` as discrete argv tokens — no
shell, no injection surface); validating *wire-supplied* model/effort is
explicitly out of scope here and deferred to #826b.

**Reference, not a shared code path:** `internal/agentrun/ptyrunner`
([ptyrunner-package.md](ptyrunner-package.md)) already models `--model` /
`--effort` / bypass as first-class knobs for `pyry agent-run`, and its
`buildArgs` supplied the exact flag spellings here — but that path forbids
`--dangerously-skip-permissions` outright (#538) and uses
`--permission-mode dontAsk` instead. The two packages do not share code.

See [codebase/833.md](../codebase/833.md) for the full implementation writeup.

### `Pool.DefaultSettings` (#847)

The read counterpart to `Pool.UpdateSettings` above — a locked accessor that
surfaces the bootstrap session's currently-persisted `SessionSettings` across
the package boundary. `SessionInfo` (from `Pool.List()`) does not carry
`settings`, and `settings` is a private field readable only under `Pool.mu`,
so a consumer outside `internal/sessions` cannot reach it without this.

```go
func (p *Pool) DefaultSettings() (SessionSettings, bool)
```

Mirrors `Default()`'s lock discipline exactly: `p.mu.RLock()`, resolve
`p.sessions[p.bootstrap]` **fresh** on every call (not cached) so the result
stays correct across a `RotateID` (which flips `p.bootstrap` under the write
lock). Returns `(SessionSettings{}, false)` when there is no bootstrap to read
from (the embedded evicted-bootstrap host, or a zero-value `&Pool{}` map-miss)
so a consumer falls back to daemon defaults — no error path.
`SessionSettings` is a value type, so the return is a snapshot copy with no
aliasing of the pool's live field.

Shipped unwired in #847; wired by #848, which populates the `screen_snapshot`
reply's `model`/`effort`/`yolo` fields via a closure over this accessor built
in `cmd/pyry/main.go` and threaded through `V2SessionConfig.SnapshotSettings`
(see [v2-session-manager.md § Inbound screen-snapshot handler](v2-session-manager.md)
and [protocol-package.md § Screen-snapshot payloads](protocol-package.md)).
Deliberately has no conversation-keyed variant: the snapshot source is always
the bootstrap session, so settings-source == snapshot-source by construction.
See [codebase/847.md](../codebase/847.md) and [codebase/848.md](../codebase/848.md).
