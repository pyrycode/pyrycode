# #1604 — Deliver a bypass revocation in-band; keep the enable on the restart

**Size:** S · **Split from:** #1596 · **Blocked-by:** #1603 (landed) · **Blocks:** #1574, #1605
**Measurement authority:** #1595 (`docs/knowledge/features/set-permission-mode-inband-probe.md`)
**Label:** `security-sensitive` → the § Security review pass at the end of this spec is mandatory reading before implementation.

---

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`. This is the turn-1 data load; reading these five first should make the rest of the spec unambiguous.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/sessions/pool.go` | `Pool.UpdateSettings` | The two-branch live-apply, the `p.mu` release point, and the install-before-deliver ordering rule. **The branch you are changing.** |
| `internal/sessions/pool.go` | `inBandDeliverable` | The predicate and — more important — its doc's *keys-on-presence, never on merged-vs-previous* rule. You extend it with a value test on one field; you do not turn it into a differ. |
| `internal/sessions/pool.go` | `Pool.deliverSettingsInBand` | Fire-and-forget contract, the model→effort ordering rationale, and the never-log list. The revocation becomes its third clause. |
| `internal/sessions/runner.go` | `Runner`, and `SetSpawnArgs`'s doc on it | The interface you widen, and the precedent doc for a method that is ON it because its consumer is inside `internal/sessions`. |
| `internal/streamsup/runner.go` | `RevokeBypass`, `Interrupt` | The method you wire (already built by #1603) and the sibling it mirrors. `RevokeBypass`'s doc is one of AC5's two named targets — it currently predicts a type assertion. |
| `cmd/pyry/streamsup_runner.go` | `streamRunner.SetSpawnArgs`, `streamRunner.Interrupt` | The two adapter-doc shapes: on-the-interface (what you write) vs off-it-by-assertion (what you must not copy). `SetSpawnArgs`'s doc already carries the fail-open argument this ticket reuses. |
| `internal/sessions/runner_test.go` | `lifecycleRunner`, `runnerDouble`, `restartArgs`/`spawnArgSets`/`userTurns` | The recorder double and its three read accessors. You add a fourth record. |
| `internal/sessions/pool_update_settings_inband_test.go` | `TestInBandDeliverable`, `TestPool_UpdateSettings_InBand_ModelAndEffort` | The predicate table (two rows flip) and the working template for a no-respawn assertion. |
| `internal/sessions/pool_update_settings_restart_test.go` | `TestPool_UpdateSettings_YOLORevoke_DropsBypassOnRestart`, `TestPool_UpdateSettings_YOLOAbsent_NoBypassInBand`, `helperRestartPool`, `doneAppears` | The test to retarget, the test that must NOT move, the pool helper, and `doneAppears`'s doc explaining why it is blind under the double. |
| `internal/sessions/session.go` | `claudeSettingsArgs` | The one place the YOLO fail-safe is enforced in argv, and the model→effort→bypass order this ticket extends into the delivery path. |
| `docs/knowledge/features/set-permission-mode-inband-probe.md` | § "Behavioural verdict per direction", § "The escalation finding" | The measurement. Read it; do not re-derive it. |

---

## Context

A bypass-posture change live-applies today by restarting the child. `Pool.UpdateSettings` recomposes the spawn argv and calls `sup.Restart(newArgs)`; that restart is what makes a revocation take effect *now* rather than at the next spawn, and #842's security review rests on exactly that.

#1581 partitioned the live-apply: a model/effort-only change goes in-band as `/model` and `/effort` user turns on the held-open stream, with no respawn. `inBandDeliverable` requires `YOLO == nil`, so a present YOLO can never reach the non-restarting branch. The bypass kept the restart because no in-band form had been measured.

#1595 measured it live against claude 2.1.220 and **the finding is directional**:

- **true → false lands in-band.** A `set_permission_mode` control request carrying `mode: "default"`, written on the same held-open stdin the daemon already writes interrupts to, confirmed three independent ways (`success` control response, next `init.permissionMode` echoing `default`, turn-2 behaviour matching a `default`-launched control child exactly).
- **false → true does not.** claude refuses it in words — *"Cannot set permission mode to bypassPermissions because the session was not launched with --dangerously-skip-permissions"* — gating on the launch argv rather than on the request.

So the partition splits on the **direction** of a bypass change, not on its presence. #1603 already built the writer (`marshalBypassRevocationEnvelope` / `WriteBypassRevocation` / `(*streamsup.Runner).RevokeBypass`), emitting `default` and only `default` by construction. It has no production caller. This ticket is that caller.

**What this settles for #1574.** #1574 rests on the premise that once model, effort and bypass posture have all moved off `Restart(args)`, it has no production caller left. The bypass posture only half-moves. After this ticket `Pool.UpdateSettings` still calls `sup.Restart(newArgs)` for the enable direction, so `Restart` keeps a live production caller and deleting it would regress an enable into a next-spawn-only change. AC5 requires the call site to say so.

---

## Design

### 1. Widen `sessions.Runner` with `RevokeBypass() error`

```go
// on internal/sessions.Runner
RevokeBypass() error
```

**Interface, not type assertion — and the reason is not the one an earlier draft gave.** The structural-assertion escape `cmd/pyry` uses for `Interrupt` (`case interface{ Interrupt() error }:`) needs no import and is genuinely reachable from `internal/sessions`; there is no import cycle to appeal to (`internal/streamsup` does not import `internal/sessions`). What rules it out is that **a structural assertion fails open**: its unmatched arm is a silent no-op, so a runner missing the method would leave the posture un-revoked while `UpdateSettings` reports success. That is precisely the regression this ticket exists to prevent, and it is why AC1 demands a *build* failure. `SetSpawnArgs`'s adapter doc already records this same reasoning for this same situation.

The placement rule is consistent, not ad hoc: `Interrupt`, `RestartFresh` and `BeginRotation` stay off the interface because their dispatch lives in `cmd/pyry`, which can assert; `SetSpawnArgs` is ON it (#1580) because its consumer is `Pool.UpdateSettings`, inside `internal/sessions`. A bypass revocation has that same consumer. #1077's "un-widened" convention is a statement about the first group, not a blanket ban.

**Cascade — exactly six sites**, verified by sweeping declarations of the interface's newest method (`SetSpawnArgs`) across all tracked Go files:

| Site | Kind | Work |
|---|---|---|
| `cmd/pyry/streamsup_runner.go` — `streamRunner` | production adapter | one-line delegate + doc (below) |
| `cmd/pyry/inbound_deliver_rotation_test.go` — `baseRunner` | double | `{ return nil }` |
| `cmd/pyry/session_router_test.go` — `stubRunner` | double | `{ return nil }` |
| `internal/sessions/runner_test.go` — `fakeRunner` | double | `{ return nil }` |
| `internal/sessions/runner_test.go` — `lifecycleRunner` | recorder double | records a count (below) |
| `internal/sessions/session_evict_race_test.go` — `raceRunner` | double | `{ return nil }` |

`*streamsup.Runner` already has `RevokeBypass() error` from #1603, so it needs no edit and the adapter's one-line forward is the whole production cost.

**Two stale counts sit inside doc blocks you will be editing.** Correct them in place; do not go hunting further.

- `baseRunner`'s doc calls itself "the five trivial `sessions.Runner` methods" — it becomes six.
- `streamRunner`'s type doc says the adapter "maps that one method and forwards the other four unchanged" — already stale since #1580 (five), and six with this ticket. State the right number or drop the count.

The "one production implementation and five test doubles" phrasing in `streamRunner.SetSpawnArgs`'s doc counts *implementations*, not methods, and stays correct.

### 2. Split `inBandDeliverable` on the direction of a present YOLO

The function stays a single `bool` — the branch is still in-band vs restart. Its reject arms become, in this order:

| # | Condition | Verdict | Why |
|---|---|---|---|
| 1 | `YOLO` present and `true` | restart | claude gates the escalation on the launch argv and refuses the control request (#1595). Only a respawn under the recomposed argv can grant it. |
| 2 | `Model` present and `""` | restart | "run at claude's own default" is expressed by *omitting* the flag; no `/model` invocation means that. Unchanged. |
| 3 | `Effort` present and `""` | restart | as above. Unchanged. |
| 4 | `Model`, `Effort` **and `YOLO`** all nil | restart | nothing to deliver. **This arm must gain the `YOLO == nil` conjunct** — without it a bare `SettingsUpdate{YOLO: ptr(false)}` falls through to `false` and the revoke never reaches the in-band branch. |
| — | otherwise | in-band | |

**Do not convert the predicate into a per-field differ.** Its doc's standing rule is that it keys on which fields the wire carried, never on merged-vs-previous per field. A direction split keys on the *value* of a present YOLO, which is still a property of the frame, not a diff against stored state. Extend the doc to say exactly that; the ticket's own notes call this out because it is the tempting wrong refactor.

Update the doc's first bullet, which today reads "A present YOLO takes the restart, INCLUDING a YOLO equal to the stored value. No in-band form for a bypass-posture change has been measured." Both clauses are now wrong.

**Accepted redundancy, deliberately not fixed.** `SettingsUpdate{Model: "opus", YOLO: false}` against a session whose stored YOLO is already `false` sends a revocation to a child that was never in bypass. #1595 did not measure that combination. It is harmless: the delivery is fire-and-forget, the installed argv is the durable half and carries no bypass flag either way, and the child is not in bypass to begin with. This is the same redundancy the path already tolerates for an unchanged model re-sent alongside a new effort. **Do not add a diff to avoid it and do not file a measurement ticket for it.**

**Mixed frames still resolve conservatively.** `{Model: "", YOLO: false}` hits arm 2 and takes the restart — and the restart recomposes argv from the *merged* settings, so the revocation still applies, just via the respawn. No frame can lose a revocation by mixing.

### 3. Deliver the revocation as `deliverSettingsInBand`'s third clause

Extend `Pool.deliverSettingsInBand` with a bypass clause after model and effort:

```go
if update.YOLO != nil && !*update.YOLO {
    // sup.RevokeBypass(); log-and-swallow on error, same as the two sends above
}
```

Contract notes for the doc:

- **Ordering is model → effort → bypass**, extending the order `claudeSettingsArgs` fixes and `deliverSettingsInBand` already half-fixes, for the reason already stated there: determinism buys testability at no cost.
- **The revocation is a control request, not a queued turn**, so it does not pass the turncommit gate and may reach the child ahead of a `/model` turn queued in the same update. That affects ordering, not the resulting posture — the two settings are independent. Say so in the doc; a reader who assumes turn ordering will otherwise mis-read the send order as a guarantee.
- **Same fire-and-forget contract**, same `Info` record, same never-log list. The record's `setting` field is `"bypass"`. There is no value to leak — `RevokeBypass` takes no mode — so #833's keep-settings-values-out-of-the-log rule is satisfied structurally rather than by discipline.
- **The `!*update.YOLO` half of the guard is unreachable under the current predicate**, since arm 1 already rejects an enable. Keep it anyway and label it as the enable-direction fail-safe: it makes the delivery site independently correct rather than dependent on a caller-side invariant — the same argument `inBandDeliverable`'s own doc makes for keeping its redundant Model-or-Effort clause. It is directly asserted by a unit scenario (§ Testing) rather than left as untested defence.

No changes to `UpdateSettings`' control flow. The existing sequence is already correct for the revoke:

```
recompose argv under p.mu → release p.mu
  ├─ inBandDeliverable(update)  → SetSpawnArgs(newArgs) ; deliverSettingsInBand(...)
  └─ otherwise                  → Restart(newArgs)
```

`SetSpawnArgs` before the delivery is what makes AC1's second half hold with no new mechanism: the install is the durable half, so a revocation survives a crash-relaunch, a rotation or an eviction even if the write is lost. An evicted session reaches the same branch — `SetSpawnArgs` installs, `RevokeBypass` returns the runner's retryable no-live-child error, which is logged and swallowed, and the caller still sees success.

### 4. Document the mechanism at the call site (AC5)

Two doc obligations in `Pool.UpdateSettings`:

1. Its own doc's two-mechanism list currently says the restart "is the path a bypass-posture change takes". Rewrite to name the direction split, citing #1595 for the asymmetry and #1603 for the writer.
2. A comment **at the `sup.Restart(newArgs)` call itself** stating plainly that this is a live production caller of `Restart`, that the enable direction is what keeps it alive, and that **#1574 may not delete it** — deleting it would regress an enable from "applies now" to "applies at the next spawn". #1574's stated premise ("once model, effort and bypass move off `Restart`, it has no production caller") is falsified by this ticket, and the call site is where a #1574 implementer will look.

### 5. Correct `(*streamsup.Runner).RevokeBypass`'s doc (AC5)

Its second paragraph currently predicts the opposite wiring — that `sessions.Runner` "stays un-widened" and that #1604 reaches the method "via a type assertion". Replace with what shipped: the method is on the interface, because its consumer is `Pool.UpdateSettings` inside `internal/sessions` and a structural assertion there would fail open. Keep the `Interrupt` cross-reference but recast it as the *contrast* (dispatch in `cmd/pyry` → assertion) rather than the parallel.

---

## Scope boundaries

**IN — production (4 files):** `internal/sessions/runner.go`, `internal/sessions/pool.go`, `cmd/pyry/streamsup_runner.go`, `internal/streamsup/runner.go`.

**IN — tests (6 files):** the four double-bearing files above plus `internal/sessions/pool_update_settings_inband_test.go` and `internal/sessions/pool_update_settings_restart_test.go`.

**OUT — the downstream stale-claim sweep, and this is a stated deviation from AC5's literal wording.** AC5 ends "no doc in the tree may still say that once this ticket lands." Three further sites carry a claim this ticket falsifies, and none is in the developer's scope:

| Site | Claim that goes stale | Owner |
|---|---|---|
| `internal/relay/v2session_settings.go` (2 sites: `handleSetSessionSettings`'s doc, and the success-path comment) | "every other change live-restarts the session's supervisor (#842)" | documentation |
| `cmd/pyry/relay.go` (the settings-reply comment) | same sentence | documentation |
| `docs/knowledge/features/streamsup-package.md` § bypass revocation send primitive | "`RevokeBypass` … deliberately not on `sessions.Runner` … the session-layer wiring (#1604) reaches it via a type assertion" | documentation |
| `docs/knowledge/features/sessions-package.md` § "Which branch, and why" | "a bypass-posture (`YOLO`) change, for which no in-band form has been measured" | documentation |

This is not an invention: **the repo already ran exactly this pattern for #1581.** Commit `eba5e2b` ("docs: correct the *settings reach claude on the next spawn* claim", `Refs #1581`) landed on `feature/1581` as its own `docs:` commit, touching these same two Go files plus the same evergreen markdown, immediately before the patterns/lessons commit. Following it keeps the developer's budget on code and puts the sweep where it went last time.

**Left alone deliberately — frozen point-in-time records.** `docs/knowledge/codebase/1603.md` and `docs/specs/architecture/1603-*.md` both say #1604 will reach `RevokeBypass` by type assertion. `eba5e2b`'s own message states the rule: *"The frozen point-in-time records that carry the same sentence are left alone: docs/specs/architecture/\*\*, docs/knowledge/codebase/\*\*. Editing those would falsify the build record."* Reviewers: a surviving hit in those two paths is correct, not a miss.

**Also out:** `docs/knowledge/codebase/1604.md` — the documentation phase writes it from the merged diff. The developer must not.

---

## Concurrency model

No new goroutines, no new locks, no channels.

- `RevokeBypass` is invoked from `deliverSettingsInBand`, which runs **outside `p.mu`** — same as the two existing `WriteUserTurn` sends. The `Pool.mu → Session.lcMu` order is untouched.
- On the concrete runner, `Stdin()` releases the runner's own mutex before returning, so the potentially-blocking write never holds it (#1603's contract). Safe from any goroutine.
- **The write races a respawn, benignly.** Between `SetSpawnArgs` and `RevokeBypass` a crash-relaunch or a rotation can swap the child. Either the write lands on the old bypass child (revoked) or on a new child spawned from the just-installed bypass-free argv (already `default`, so the request is a no-op). Both outcomes are non-bypass. There is no ordering in which the argv install and the control write combine to leave a child in bypass.
- `UpdateSettings` gains no new failure path: the revoke is fire-and-forget and cannot make the call return an error it does not return today.

---

## Error handling

| Failure | Behaviour | Why |
|---|---|---|
| No live child (evicted, between spawns, backing off) | `RevokeBypass` returns the runner's retryable no-live-child error; logged at `Info`, swallowed | Dominant case, nothing degraded — the installed argv already carries the revocation. Identical to the existing model/effort treatment. |
| Write error on a live stdin | logged at `Info`, swallowed | Settings are already persisted and the argv already installed, so a failed write loses nothing durable. Same contract `Restart` has had on this path since #842. |
| A runner that cannot revoke | **compile error** | This is AC1's dispatch requirement. The interface method makes it a build failure; a structural assertion would make it a silent success. |

Errors are not classified here and must not be: `internal/sessions` must not import `internal/streamsup`, which would invert the `Runner` seam, and the reachable set all warrants the same response. This is the existing rule in `deliverSettingsInBand`'s doc, unchanged.

---

## Testing strategy

Scenarios, not code — write them in the package's idiom. `internal/sessions` tests are the proof; the live gate is #1605's.

### Recorder double

`lifecycleRunner` gains a `RevokeBypass() error` that increments a **count** under its existing mutex, plus a `revokeCount()` reader that deep-reads under the same lock (matching `restartArgs`/`spawnArgSets`/`userTurns`). A count, not a bool: a test must be able to distinguish "exactly one" from "two" and catch a double-send.

Record on **every** call including no-live-child, for the reason the `writes` record already documents: it is the production runner, not the pool, that refuses when no child is bound, and the pool's contract is to attempt unconditionally.

### `TestInBandDeliverable` — extend the table

Two existing rows flip, three are added. The table is documented as total over the shapes the wire can produce; keep it that way.

| Row | Update | Want |
|---|---|---|
| model with yolo revoke *(flips)* | `{Model: "opus", YOLO: false}` | **true** |
| yolo only *(rename to name the direction; flips)* | `{YOLO: true}` | false — enable keeps the restart |
| yolo revoke only *(new)* | `{YOLO: false}` | **true** — the arm-4 conjunct is what makes this reachable |
| yolo revoke with cleared model *(new)* | `{Model: "", YOLO: false}` | false — empty-value reject wins; the respawn still carries the revocation |
| yolo revoke with effort *(new)* | `{Effort: "high", YOLO: false}` | true |

Existing rows for model-only / effort-only / model+effort / cleared values / `{Model, YOLO: true}` / nothing-present are unchanged and must stay green.

### Retarget `TestPool_UpdateSettings_YOLORevoke_DropsBypassOnRestart` (AC4)

**Rename** — the name claims a restart that no longer happens. `TestPool_UpdateSettings_YOLORevoke_DropsBypassInBand` mirrors the sibling `_YOLOAbsent_NoBypassInBand`.

**Retarget, do not weaken.** The template is `_YOLOAbsent_NoBypassInBand`, which already reads `restartArgs()` / `spawnArgSets()` / `userTurns()` off the double instead of waiting on a real second spawn. The current revoke test waits on `waitArgv(t, tplWorkDir)`, which cannot work once no respawn happens: `lifecycleRunner.Restart` is what feeds `recordArgv`, so with no restart the wait would return the stale construction argv or time out.

Setup: `helperRestartPool` with `SessionSettings{YOLO: true}`, `runnerDouble`, `waitRunning`. First-spawn argv assertion (bypass flag present) is a `waitArgv` read and stays. Then `UpdateSettings(id, {YOLO: ptr(false)})` and assert **all four**:

- `revokeCount() == 1` — the live half of AC1. Exactly one; not "at least one".
- `restartArgs()` is empty — no respawn.
- exactly one `SetSpawnArgs` install, and `installedArgv` of it carries **no** `--dangerously-skip-permissions` and in fact no settings flags at all (revoked YOLO with no model or effort leaves none) — the next-spawn half of AC1, which is what makes the revocation survive a crash-relaunch, a rotation or an eviction.
- `userTurns()` is empty — a YOLO-only update invents no `/model`.

Do not reach for `doneAppears`: its own doc records that it cannot observe a respawn under this double (measured 2026-08-19 against a live mutant).

### `TestPool_UpdateSettings_YOLOAbsent_NoBypassInBand` — one line added, nothing moved (AC3, AC4)

This test must **not** be dragged back to a respawn assertion. It reads the installed next-spawn argv, which is correct for the model-only path #1581 gave it. Add exactly one assertion: `revokeCount() == 0` — an omitted YOLO writes no permission-mode change at all. That is AC3's first half, and it is the direct red for a mutant that relaxes the delivery guard from `update.YOLO != nil && !*update.YOLO` to an unconditional send.

### Enable direction still restarts (AC2, AC3 second half)

`TestPool_UpdateSettings_LiveRestart_Bootstrap` already flips YOLO false→true on a live child and asserts the relaunch argv carries `--dangerously-skip-permissions`. Extend it — do not write a second test for behaviour already pinned — with a `runnerDouble` handle and `revokeCount() == 0`: no production path writes over the control channel on an enable.

`TestPool_UpdateSettings_LiveRestart_Minted` and `TestPool_UpdateSettings_Evicted_SwapOnly` both carry `YOLO: true` and must stay green **unmodified**. They are the regression pin that arm 1 still routes an enable to the restart.

### Direct unit scenario for the delivery guard (AC3)

`deliverSettingsInBand` is unexported and callable from an in-package test with a pool and a double. Call it directly with `SettingsUpdate{YOLO: ptr(true)}` — a shape `inBandDeliverable` never lets through — and assert `revokeCount() == 0`. This is what turns the enable-direction fail-safe from untested defence into an asserted contract, and it is the only red available for a mutant that drops the `!*update.YOLO` conjunct.

### Mutation coverage — the map a reviewer should be able to reproduce

| Mutant | Sole red |
|---|---|
| Drop the `RevokeBypass` call from `deliverSettingsInBand` | `_YOLORevoke_DropsBypassInBand` (`revokeCount() == 1`) |
| Drop the `YOLO == nil` conjunct from arm 4 | `TestInBandDeliverable` row "yolo revoke only" |
| Flip arm 1 to route an enable in-band | `TestInBandDeliverable` "yolo only (enable)"; `_LiveRestart_Bootstrap` (`revokeCount() == 0` and its `waitArgv` restart-argv read) |
| Relax the guard to `update.YOLO != nil` | `_YOLOAbsent_NoBypassInBand` (`revokeCount() == 0`) |
| Drop the `!*update.YOLO` conjunct | the direct unit scenario above |
| Drop `SetSpawnArgs` from the in-band branch | `_YOLORevoke_DropsBypassInBand`'s install assertion (and `_InBand_ModelAndEffort`'s, already green) |

### Gate

`make check` — every test in scope is hermetic. `internal/e2e/realclaude` is untouched; no `e2e_realclaude` build-tag surface changes. The live composed-path proof is #1605, which already carries `needs-real-claude` and is blocked on this ticket.

---

## Open questions

1. **The in-flight-turn window.** #1595 measured the revocation against *turn 2*. It did not measure what happens to a tool call already dispatched within the turn in flight when the control request arrives. The old restart path killed the child, which ended any in-flight bypass tool call outright; the in-band path does not. See § Security review finding [Threat model alignment] — the resolution is to document the window, not to change the design, and the measurement belongs to #1605.
2. **`streamRunner` adapter-doc phrasing.** The adapter's per-method docs each explain *why* the method is on or off the interface. `RevokeBypass`'s should be the fail-open argument, and it is nearly word-for-word `SetSpawnArgs`'s. Whether to state it fresh or explicitly cross-reference `SetSpawnArgs` is the developer's call; a bare "see SetSpawnArgs" is fine and is cheaper to keep true.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the boundary tightens rather than moves. The relay-reachable `set_session_settings` verb already validates model/effort at the wire boundary and already decodes YOLO as a typed `*bool`, so a malformed yolo fails type-decode before reaching the seam and bypass can never be inferred from a bad value. This ticket adds no new inbound field and no new parse site. The only value crossing into the new path is the `bool` itself, and it selects between two code paths rather than being interpolated anywhere. The control envelope's one variable field is `request_id`, minted locally from an atomic counter — digits only, never caller-supplied (#1603).

- **[Tokens, secrets, credentials]** No findings — no token, key or credential is read, written, derived or logged on this path. Revocation *propagation* is the ticket's subject and both halves are addressed: immediate (the control request to the running child) and durable (the argv install, which survives crash-relaunch, rotation and eviction). There is no partial state in which one lands and the other silently does not: the install runs first and is non-blocking, so a lost write degrades to today's next-spawn behaviour rather than to no revocation.

- **[File operations]** Not applicable — no path is constructed, opened, stat'd or written. The settings persist (`saveLocked`, atomic temp-plus-rename) happens before the live-apply and is untouched.

- **[Subprocess / external command execution]** No findings, and this is the category with the sharpest edge. The escalation direction is where an argv would matter, and it is *unchanged*: an enable still goes through `Restart(newArgs)` with argv recomposed by `claudeSettingsArgs`, the single place `--dangerously-skip-permissions` is emitted. The revoke direction adds no exec at all — it writes one line to an already-open stdin. Nothing new is interpolated into an argv, and no shell is involved.

- **[Cryptographic primitives]** Not applicable — no randomness, hashing, comparison against a secret, or key material on this path. The control-request correlation id is a monotonic counter and is not security-relevant (nothing reads the ack).

- **[Network & I/O]** No findings — no socket, no size-unbounded read, no new inbound surface. The write is a single fixed-shape JSON line onto an existing pipe, produced by a marshaller with no caller-controlled field.

- **[Error messages, logs, telemetry]** No findings. The new record is `Info` with `session` and `setting: "bypass"` — a field name, no value. Unlike the model/effort sends there is no value that *could* leak: `RevokeBypass` takes no mode parameter, so #833's keep-settings-values-out-of-the-daemon-log rule is satisfied structurally rather than by developer discipline. `UpdateSettings` returns the same errors it returns today, so nothing new reaches a client reply.

- **[Concurrency]** No findings. No new lock, no new goroutine, no check-then-mutate. The call runs outside `p.mu` like the existing in-band sends, and the concrete method releases the runner's mutex before the potentially-blocking write. The respawn race is enumerated in § Concurrency model and is closed by construction: every interleaving of the argv install and the control write leaves the child non-bypass, because the installed argv carries no bypass flag and the control request can only emit `default`.

- **[Threat model alignment]** Two findings, both SHOULD FIX, neither gating.

  1. **The revocation no longer kills the child, so an in-flight turn is not torn down.** Today a revoke restarts the child, which ends any tool call already dispatched under bypass. After this ticket the posture flips on the running child and the current turn continues. #1595 measured turn-2 behaviour matching a `default`-launched control exactly; it did **not** measure a tool call already in flight when the request arrives. For an operator revoking bypass as incident response, the containment property silently narrows from "kill + revoke" to "revoke". *Resolution:* state the window plainly in `deliverSettingsInBand`'s doc — the revocation applies to the child's permission mode, not to work already dispatched in the turn in flight — and note that `interrupt` remains the verb for ending a running turn. Hand the measurement to #1605. This is a consequence of the "no respawn" requirement AC1 states, not a defect in the design, so it is documented rather than designed around.

  2. **#1595's own caveat, now with a production caller.** The probe recorded that anything able to write a child's stdin can drop that child's bypass posture mid-session, and this ticket gives that capability a relay-reachable trigger for the first time. It is a privilege *reduction* in every reachable direction — the writer emits `default` and only `default`, with no mode parameter on either exported entry point (#1603), so the surface cannot be turned into an escalation by a confused caller. The escalation direction stays gated by claude on the launch argv and is refused in words. No new authorisation gate is warranted: the same relay verb already grants the far stronger power of *enabling* bypass via the restart path, behind the same capability check. *Resolution:* none required; recorded so a reader of the call site does not conclude the caveat went unexamined.

  3. Out of scope, named: **#1487's fail-closed rule for a phone-granted `--dangerously-skip-permissions` across a daemon restart** (recorded in `internal/sessions/revive.go`). Nothing here changes it and nothing here should. The revive path recomposes from persisted settings and is not touched.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
