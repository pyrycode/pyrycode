# Spec #1108 — Construction-safe session-id seam for a stream-json runner factory

**Ticket:** [#1108](https://github.com/pyrycode/pyrycode/issues/1108) · **Size:** S · **Not security-sensitive** (id is an identifier, not a credential; no new trust boundary — the seam only re-exposes an already-minted id value at construction time).

Split from #1107. This is the **seam** child: it delivers the *means* for a future runner factory to read a construction-safe, non-empty session id at both pool construction sites. It builds no factory (#1109) and adds no `streamsup.New` caller.

---

## Seam decision — (i) additive `supervisor.Config.SessionID`

The ticket offers two seams. **We take seam (i): add an eager `SessionID string` field to `supervisor.Config`, set unconditionally at both pool sites from the already-minted id, and deliberately ignored by the PTY supervisor.**

Why (i) over (ii) (relax `streamsup.New` to a lazy resolver):

1. **Simplicity / minimal surface.** Seam (i) is one additive field plus two assignment lines. Seam (ii) changes the *public signature* of a shipped, reviewed package (`streamsup.New`, #1076/#1087/#1088/#1097) **and** requires `buildSession` to newly set `ResolveSessionID` — strictly more surface and more risk.
2. **Seam (ii) fights `streamsup`'s design.** `streamsup` is structurally **fixed-at-construction**: `streamsup.New` requires an eager non-empty `SessionID`, and `buildArgs` reuses that *same* id verbatim on every respawn (`--session-id` first spawn → `--resume <same-id>` thereafter, no fork). It has no rotation mechanism by design — no fsnotify, no transcript tailing (`docs/knowledge/features/streamsup-package.md`). A lazy resolver could not be honoured by the streamsup runner without also rewriting `streamsup.New` — out of scope and against the package's whole point.
3. **The rollback guarantee is trivial under (i).** The PTY supervisor never reads the new field (it keeps resolving its id via `ResolveSessionID`), so the PTY spawn path is byte-identical by construction — the field is dead weight on that path. Ignoring it *is* the guarantee.

The future factory (#1109) is `func(cfg supervisor.Config) (Runner, error)`; it reads `cfg.SessionID` to build a `streamsup.Config{SessionID: cfg.SessionID, …}`. `supervisor.Config` is already the seam surface RunnerFactory receives — adding the id there is consistent with how the bootstrap already carries factory inputs (`ResolveSessionID`, `ClaudeArgs`) on the same struct.

## `/clear` rotation decision — **accepted-and-documented as fixed-at-construction for the stream-json path**

The PTY bootstrap re-resolves a **rotated** id each spawn via `ResolveSessionID` (a `/clear` inside claude rotates the on-disk id; the fsnotify rotation watcher fires `Pool.RotateID`, which flips `p.bootstrap`; the closure `func() string { return string(p.BootstrapID()) }` reads it fresh — #839/#118). A construction-fixed id cannot mirror that.

**Decision: the stream-json path's id is fixed at construction; rotation is NOT mirrored on that path. This is accepted, not a gap to close now.** Rationale:

- **The PTY path keeps full rotation.** Seam (i) leaves `ResolveSessionID` untouched and the PTY supervisor keeps calling it every spawn. PTY `/clear` rotation is preserved exactly as today — the new field changes nothing there.
- **`streamsup` is fixed-at-construction anyway.** Even seam (ii)'s lazy resolver could not make a streamsup child re-resolve — `buildArgs` bakes the id in at construction and `--resume`s the same id forever. Mirroring rotation on the stream-json path is not a property this seam can grant; it would require a different `streamsup` design.
- **Evidence-based deferral.** The stream-json path has no production consumer yet (the factory is #1109; wiring is T4/T7). Shipping rotation-preservation for a path nothing drives is a defense for an unobserved failure mode. If stream-json `/clear` rotation is ever needed, it belongs to the factory/wiring slice and would change `streamsup.New` — a separate, larger change. Defer.

This section, plus the doc comment the developer writes on the new field (see AC-3 below), *is* the "decided and documented" deliverable.

---

## Files to read first

- `internal/supervisor/supervisor.go:91-174` — `Config` struct; add the new field near `ResolveSessionID` (lines 112-120). Read that field's doc comment — the new one mirrors its "resolved fresh each spawn / nil preserves --continue" framing but states the inverse (eager, construction-fixed, PTY-ignored).
- `internal/supervisor/supervisor.go:736-740` — the *only* place the PTY spawn path resolves its id (`ResolveSessionID()` → `buildClaudeArgs`). Confirm the new field is **not** wired in here — that disconnect is the byte-identical guarantee.
- `internal/sessions/pool.go:443-463` — bootstrap `supCfg` literal in `Pool.New`; add `SessionID: string(bootstrapID)`. `bootstrapID` (declared 375, set at 383 warm / 407 cold) is fully resolved and non-empty before this literal.
- `internal/sessions/pool.go:504-510` — `newRunner(supCfg)` bootstrap call site; the new field must be set on `supCfg` before this line.
- `internal/sessions/pool.go:1284-1319` — `buildSession`: `--session-id <id>` is already baked into `ClaudeArgs` (1284); add `SessionID: string(id)` to the `supCfg` literal (1297-1307). This is the second construction site; `id` is the parameter.
- `internal/sessions/runner.go:38-46` — `RunnerFactory func(cfg supervisor.Config) (Runner, error)`; the future consumer reads `cfg.SessionID`. No change here — just the contract this seam feeds.
- `internal/sessions/runner_test.go:1-79` — existing `fakeRunner` + `TestRunnerFactory_InvokedAtEveryConstructionSite`. The factory closure already receives `cfg supervisor.Config`; AC-4 extends it to capture and assert `cfg.SessionID` at each site.
- `internal/supervisor/args_test.go:1-107` — golden `TestBuildClaudeArgs` table + the input-mutation guard. AC-2's guard test lives alongside these; `buildClaudeArgs`'s signature (no `SessionID` param) is the structural half of the proof.
- `docs/knowledge/features/streamsup-package.md` § "Public API" + § "`buildArgs`" — proves `streamsup.Config.SessionID` is eager/required and the id is `--resume`d verbatim (no rotation). Grounds the `/clear` decision.

---

## Design

### Change 1 — `supervisor.Config`: add `SessionID string` (ignored by the supervisor)

Add one exported field to `supervisor.Config`, placed next to `ResolveSessionID`:

```go
// SessionID is the caller-minted session id, set eagerly at construction.
// The PTY supervisor DELIBERATELY IGNORES it — it resolves its own id lazily
// via ResolveSessionID every spawn (so a /clear rotation is picked up). This
// field exists only so an alternative RunnerFactory (the stream-json path,
// #1109) can read a construction-safe, non-empty id from the same
// supervisor.Config it is handed; streamsup requires its id at construction.
// Because the supervisor never reads it, the PTY spawn path stays
// byte-identical whether or not it is set — that is the rollback guarantee.
// Construction-fixed: it does NOT mirror a /clear rotation (see spec #1108).
SessionID string
```

No other change in `internal/supervisor`. `buildClaudeArgs` keeps its four-parameter signature and never receives `SessionID`; the spawn loop (supervisor.go:736-740) is untouched. The field is inert on the PTY path.

### Change 2 — `Pool.New`: set the bootstrap site's `SessionID`

In the `supCfg := supervisor.Config{…}` literal (pool.go:444-463), add:

```go
SessionID: string(bootstrapID),
```

Set unconditionally (not gated on `cfg.RunnerFactory != nil`): the seam always *exposes* the id; whether a factory *consumes* it is the factory's concern. Gating would couple the seam to its consumer. `ResolveSessionID` stays exactly as-is — the PTY bootstrap keeps rotating. A one-line comment should note the construction-fixed-vs-rotation split (the PTY path uses `ResolveSessionID` and rotates; the field is the fixed value a stream-json factory would read).

### Change 3 — `Pool.buildSession`: set the per-session site's `SessionID`

In the `supCfg := supervisor.Config{…}` literal (pool.go:1297-1307), add:

```go
SessionID: string(id),
```

`buildSession` is the single funnel for `CreateIn` and `GetOrCreateIn`, so this one line covers every per-session mint path. `--session-id <id>` remains baked into `ClaudeArgs` (pool.go:1284) for the PTY path — unchanged. The new field is the same id value delivered through the factory-readable channel instead of only through argv.

### Data flow (both sites, after the change)

```
Pool.New:        bootstrapID (local, resolved) ─┬─> supCfg.SessionID   (new: factory reads this)
                                                 └─> supCfg.ResolveSessionID closure (PTY reads this; rotates)
Pool.buildSession: id (param) ──────────────────┬─> supCfg.SessionID   (new: factory reads this)
                                                 └─> ClaudeArgs "--session-id <id>" (PTY reads this)

nil RunnerFactory  → supervisor.New(supCfg): reads ResolveSessionID / ClaudeArgs, IGNORES SessionID → byte-identical
non-nil (future)   → factory(supCfg): reads supCfg.SessionID → non-empty at construction → builds streamsup.New
```

## Concurrency model

None introduced. `bootstrapID` and `id` are plain locals/params read on the constructing goroutine before the runner exists; the new field is a value copied into `supCfg` by value. No goroutines, channels, or locks are added or touched. `BootstrapID()`'s existing `p.mu`-guarded rotation path is unaffected — the new field snapshots the *construction-time* id and is intentionally not re-read.

## Error handling

No new failure modes. The field is a plain string set from an already-validated, always-non-empty id (`bootstrapID` from mint/registry; `id` from `NewID()` or a caller-supplied validated id). No validation is added at the supervisor boundary — `streamsup.New`'s own non-empty check (`"streamsup: SessionID required"`) is the future consumer's guard, and it is fed a value that is structurally non-empty at both sites. The existing `"sessions: … supervisor: %w"` wraps at both `newRunner` call sites already cover any factory error.

## Testing strategy

**AC-4 — id is non-empty and correct at each site** (`internal/sessions/runner_test.go`, extend or sibling of the existing factory test):
- Reuse the `fakeRunner` + capturing factory. Record the `cfg.SessionID` seen at each `newRunner` invocation.
- Bootstrap site (`New`): assert the captured `SessionID` is non-empty and equals `string(p.BootstrapID())`.
- Per-session site (`buildSession(id, …)`): assert the captured `SessionID` equals `string(id)` (the arg passed in) and is non-empty.
- Keep the existing call-count assertions (factory invoked once per site) so the seam-is-threaded proof is not lost.

**AC-2 — PTY path byte-identical (golden-args / behaviour guard)** (`internal/supervisor`):
- Structural half (compile-enforced, note in the test comment): `buildClaudeArgs` takes no `SessionID` parameter, so the field cannot reach the arg builder.
- Behaviour half — a focused guard test: construct a `Supervisor` with **both** `SessionID: "<constructed-id>"` and `ResolveSessionID: func() string { return "<resolved-id>" }`, drive one spawn through the existing `TestHelperProcess` fake-child harness (see `child_env_test.go` / the supervisor spawn tests), capture the child argv, and assert it carries `--session-id <resolved-id>` and does **not** contain `<constructed-id>`. This proves the PTY spawn path ignores the new field even when the two disagree — the belt-and-suspenders against a future edit accidentally wiring `SessionID` into the arg path (deterministic code guard, not a stochastic rule).
- The existing `TestBuildClaudeArgs` golden table is unchanged and continues to pin the arg shape.

**Full suite:** `go test -race ./...`, `go vet ./...`, `staticcheck ./...` all green. No new package, no new dependency.

## AC-3 developer deliverable — document the decision in code

Beyond this spec, the developer reinforces the `/clear` decision with the doc comment on `supervisor.Config.SessionID` (Change 1 above) stating: the PTY path ignores it and keeps rotating via `ResolveSessionID`; the field is construction-fixed and does not mirror a `/clear` rotation for the stream-json path; see spec #1108. A one-line comment at each pool assignment site pointing at the same split is welcome but not required.

## Open questions

- **None blocking.** The factory that reads `cfg.SessionID` (#1109), the config selector toggle (#1081), and any future decision to mirror `/clear` rotation on the stream-json path are all explicitly out of scope and downstream of this seam.

## Scope / red-line self-check

- Production source files modified: **2** (`internal/supervisor/supervisor.go`, `internal/sessions/pool.go`) — under the 5-file gate.
- New files: **0**. New exported types/interfaces: **0** (one field on an existing struct).
- Consumer call sites needing simultaneous update: **0** — purely additive, no production reader yet, no fan-out.
- Error/reject branches: **0**. Total written LOC (production + tests + spec): well under 600.
- Size confirmed **S** (not overridden to XS: two distinct tests across two packages plus a documented design decision is more than a trivial field-add).
