# `Pool.UpdateSettings` (#840)

The persistence seam the v2 settings verb (#841, split into wire vocabulary #844 + handler #845) calls to change an existing session's `Model` / `Effort`
/ `YOLO` after creation — `SessionSettings` above was immutable
post-construction until this ticket.

```go
type SettingsUpdate struct {
    Model  *string
    Effort *string
    YOLO   *bool
}

func (p *Pool) UpdateSettings(id SessionID, update SettingsUpdate) error
```

`SettingsUpdate` is the presence contract: a `nil` field leaves the stored
value untouched; a non-nil field overwrites it, including `""` for
`Model`/`Effort` and `false` for `YOLO` — both distinguishable from omitted.
`YOLO`'s `*bool` is the security-relevant choice: an absent (`nil`) `YOLO` can
never enable bypass, only an explicit non-nil `*true` can (fail-safe-OFF by
construction, not convention).

Same shape as `Pool.Rename`: takes `Pool.mu` (write), looks up the session
(miss → `ErrSessionNotFound`, no entry created), overlays present fields onto
a copy of `sess.settings`, no-op short-circuits if nothing changed
(`SessionSettings` is comparable), else swaps in the merged value and calls
`saveLocked`, rolling the field back to its previous value if the save fails.
Never takes `Session.lcMu` — `settings` is a `Pool.mu`-guarded field, same as
`label`, so no lock-order hazard with `saveLocked`'s internal `lcMu`
re-acquire (`docs/lessons.md` § "Lock order with callback into the host").

Validating untrusted model/effort values is explicitly **not** this method's
job — it operates on operator-trusted input; the wire handler (#845, a
charset/length shape check for `Model`, a closed enum for `Effort`) owns the
untrusted → trusted crossing. See [codebase/840.md](../codebase/840.md).

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

**Which branch, and why (#1581, redrawn by #1604).** `inBandDeliverable(update)`
partitions on what the update carried — which fields, and for `YOLO` its
*value* too — never on merged-vs-previous per field. `SetSessionSettingsPayload`'s
three `omitempty` pointers are a presence contract, so a client changing one
setting sends one field, and a present `YOLO` is read for its direction rather
than diffed against stored state:

- **A change claude accepts on the already-open stream** — a non-empty
  `Model`/`Effort`, and/or a `YOLO` **revoke** (`true → false`) — →
  `sup.SetSpawnArgs(newArgs)` then `deliverSettingsInBand`, which writes
  `/model <v>` and `/effort <v>` as ordinary user turns via
  `sup.WriteUserTurn(context.Background(), "", …)`, and a bypass revoke as a
  `set_permission_mode` control request via `sup.RevokeBypass()` (#1604) —
  model, then effort, then bypass, one send per **present** field. Claude
  accepts all three on the stream the daemon already holds open and applies
  them to the running session, so **nothing is killed and the transcript
  survives**. The `SetSpawnArgs` call is not optional: it is `Restart`'s swap
  half (#1580), and skipping it would let the operator's change silently
  revert on the next crash-respawn or evict → `Activate` — this is what makes
  a revocation survive those too, with no new mechanism. Swap **before**
  write — the install is the durable half. Delivery is fire-and-forget: every
  write error is logged at `Info`
  (`"sessions: in-band settings command not delivered"`, fields `session` / a
  fixed `setting` literal / `err` — **never** the value, the payload bytes, or
  the conversation id) and swallowed, so the client sees success. They cannot
  be classified anyway: `internal/sessions` must not import
  `internal/streamsup`, and the reachable set (`ErrNoLiveChild`,
  `turncommit.ErrDropped`, a wrapped pipe failure) all warrants the same
  response, with the dominant case — an evicted session — not a degradation.
- **Everything else** → `sup.Restart(newArgs)`, unchanged. That is a `YOLO`
  **enable** (`false → true`) — claude gates the escalation on the launch argv
  and refuses the control request in words (#1595 measured this live against
  claude 2.1.220), so only a respawn under the recomposed argv can grant it —
  and clearing model or effort to `""` ("run at claude's own default", which
  `claudeSettingsArgs` expresses by *omitting* the flag, and for which no
  `/model` invocation means "revert"). A `YOLO` revoke takes this branch too
  when a present-but-empty `Model`/`Effort` is mixed into the same frame — the
  empty-value reject wins, but costs nothing: the restart recomposes argv from
  the **merged** settings, so the respawn still carries the revocation. No
  frame can lose a revocation by mixing. Clean partition — never both
  mechanisms for one change, no case left unserved.

The mechanism swap was a bug fix, not an optimisation. The respawn re-execs
with `--resume` on a session that has never run a turn; claude answers
`No conversation found with session ID` and exits 1, and the daemon retries
forever on a widening backoff (observed 2026-08-18). The in-band path
**avoids** that rather than fixing it — no resume, no lost transcript, no
crash-loop. Live-applying a `YOLO` revoke is #1604 — the enable direction has
no in-band form; claude refuses it. #1574 may **not** delete `Restart`: the
enable direction keeps a live production caller. **#1605 was split, not
landed as such**: the live-claude proof that this composed path (`Pool` →
`inBandDeliverable` → `deliverSettingsInBand` → `Runner.RevokeBypass`)
reaches a real child without tearing it down is #1622, measured against
claude 2.1.220 — see
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
