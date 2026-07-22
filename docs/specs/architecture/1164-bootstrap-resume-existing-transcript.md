# Spec — #1164: Bootstrap resumes its pinned `--session-id` safely when the transcript already exists

**Size:** S (PO sized S; architect confirms S). 2 production files (`internal/supervisor/supervisor.go`, `internal/sessions/pool.go`), **0 new exported symbols** (one existing field's signature widens), edit fan-out ~7 sites (< 10). See § Size note.

**Label:** not `security-sensitive` (labels: `bug`, `size:s`). No architect security-review pass required. The change *narrows* the trust surface anyway (a by-id stat replaces nothing; no dir scan is added) — noted in § Isolation.

**Strategy chosen: RESUME (not rotate).** The architect owns this decision (ticket Technical Notes). Rationale in § Design decision.

## Files to read first

- `internal/supervisor/supervisor.go:104-131` — `Config.ResumeLast` + `ResolveSessionID` + `SessionID` field docs. `ResolveSessionID`'s type widens here; the `ResumeLast`/`--continue` history at 104-110 is the exact "bare `--resume` opens a picker" concern this spec must not re-trip (we always pass `--resume <id>`, never bare).
- `internal/supervisor/supervisor.go:742-811` — `Run` loop; **747-751** is the provider-read + `buildClaudeArgs` call site to update. `liveArgs()` at 703-707.
- `internal/supervisor/supervisor.go:825-840` — `buildClaudeArgs`, the pure function to extend with a `resume bool` and a `--resume` branch. Its test is `internal/supervisor/args_test.go`.
- `internal/sessions/pool.go:440-512` — bootstrap `supCfg` wiring. **455** (`ResolveSessionID` closure to widen), **496-512** (`ClaudeSessionsDir != ""` gate + the `newProbePreferredTranscriptResolver(... , supCfg.ResolveSessionID)` at **511**, whose `pinnedID func() string` arg must be re-sourced when the field's type changes).
- `internal/transcript/transcript.go:126-143` — `StatByID(dir, id) (Result, error)`: validates the stem via `ValidStem` **before** any `filepath.Join`, stats exactly `<dir>/<id>.jsonl`, returns the raw `os.Stat` error on absent/unreadable. This is the by-id existence probe the resume decision rides — **not** a dir scan (AC-3).
- `internal/streamsup/runner.go:583-605` — `buildArgs`: the sibling stream-json runner's **first-spawn `--session-id` / respawn `--resume`** pattern. Precedent that `--resume <id>` (with id) is the established reattach form. Note it keys off `firstRun`, which this spec improves on (§ Design decision).
- `internal/sessions/pool_settings_test.go:17-90` — `argvRecorderScript` + `helperPoolArgvRecorder` + `waitArgvRaw`: a real `/bin/sh` child that records the **exact spawned argv**. The canonical AC-5 seam (observe `--resume` vs `--session-id` end-to-end, no live claude).
- `internal/sessions/pool_bootstrap_sessionid_test.go` — the #839 `Pool.New`/`BootstrapID` direct-drive tests + `helperPoolReconciling(t, regPath, claudeDir)`. The new resume tests live beside these.
- `internal/supervisor/restart_test.go:131-163` — `TestSupervisor_Run_IgnoresSessionIDField`: sets `cfg.ResolveSessionID` and asserts recorded argv via `waitForSpawns`/`recorderConfig`. **Must update** the closure to the new signature (`return resolvedID, false`); its `--session-id` assertion stays valid under `resume=false`. This file is also the supervisor-side argv harness the new `--resume` supervisor test reuses.
- `docs/specs/architecture/839-deterministic-bootstrap-session-id.md` — the pull-provider seam this extends; § "Seam choice" and § Concurrency are the invariants to preserve.
- `docs/lessons.md:54` — "`/clear` rotates claude's session UUID **even with `--resume <uuid>`**" — confirms `--resume <uuid>` is a real, used spawn form (not the bare-`--resume` picker case).

## Context

#839 made the bootstrap claude spawn with `--session-id <bootstrapID>` on **every** spawn, resolved fresh from the daemon's own persisted id via the `ResolveSessionID func() string` provider (pull seam). This is deterministic and closes the confused-deputy isolation gap.

But claude 2.1.199 **refuses `--session-id <uuid>` when that transcript already exists on disk** (`Session ID <uuid> is already in use`, exits ~220ms). On a hard restart (`launchctl kickstart -k`, binary cutover, crash) the transcript `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl` survives. The warm-start branch of `internal/sessions/pool.go` `New()` (`pickBootstrap(reg) != nil`) reuses the persisted id **unconditionally**, so `ResolveSessionID` hands back that id, `buildClaudeArgs` emits `--session-id <id>`, claude refuses, and — because the PTY supervisor emits `--session-id` on **every** spawn (never switching form) — every backoff respawn fails identically. Infinite crash-loop; daemon up, no bootstrap session.

**Why the PTY path loops but streamsup does not.** `internal/streamsup` already spawns `--session-id` on `firstRun`, `--resume` on every respawn, and advances `firstRun→false` once `cmd.Start` succeeds. So on a daemon restart it wastes exactly **one** ~220ms crash (first spawn `--session-id` refused) then self-heals to `--resume`. The PTY supervisor's `buildClaudeArgs` has **no `--resume` branch at all** — when `sessionID != ""` it always emits `--session-id`, regardless of `firstRun`. It can never escape. That missing branch is the root cause.

The fix must **preserve #839's determinism** — no `--continue` (resumes "most recent", non-deterministic), no adopt-by-mtime dir scan. Both were deleted by #839 to close the isolation gap; `docs/lessons.md`/#839 forbid reintroducing them.

This ticket is the **root fix**. Its sibling #1165 (independent, no blocked-by) is the generic **safety net**: a supervisor N-fast-crash self-heal that rotates to a fresh id via `RotateID` when *any* crash-loop persists. The two compose as belt-and-suspenders of **different fabric** (§ Interaction with #1165) — this ticket does not implement, depend on, or duplicate #1165.

## Design decision: RESUME, per-spawn, by-id existence probe

### Resume vs rotate

| | Resume (chosen) | Rotate (rejected) |
|---|---|---|
| Preserves the idle bootstrap conversation | **Yes** — the whole point of #839 pinning | No — drops it on every restart, permanently; pinning becomes pointless |
| Within-process respawn after first spawn | Handled uniformly (transcript exists → `--resume`) | Still collides (`--session-id <freshid>` refused on the 2nd spawn) — relies on #1165 |
| Determinism / isolation (AC-3) | `StatByID` stats exactly `<id>.jsonl` — no dir scan | Same (by-id), but the id churns each restart |
| Surface | +1 `buildClaudeArgs` branch, widen one provider field | Stays in `internal/sessions` |
| Relationship to #1165 | Different fabric (resume vs rotate) → clean belt-and-suspenders | Same fabric as #1165 → redundant |

**Resume is chosen** because the user story's value is "a working bootstrap session after restart" **and** #839 pinned the id specifically to keep *that idle conversation* across restart. Rotate yields a working-but-empty session and defeats #839's purpose; resume yields a working-and-preserved session. Resume also subsumes the within-process respawn cleanly, so #1165 stays a genuine independent net for the *residual* case (a transcript that is genuinely unresumable → `--resume` also fails → after N fast crashes, #1165 rotates). See § Interaction with #1165.

### Why per-spawn (not a one-shot decision in `New()`)

A decision fixed at `New()` is **insufficient**: cold start picks `--session-id` (no transcript yet), but the moment the first child creates `<id>.jsonl`, the next in-process respawn with the same fixed `--session-id` collides. Only a **per-spawn** rule is uniformly correct. The single rule:

> Spawn `--resume <id>` iff `<id>.jsonl` already exists; else `--session-id <id>`.

subsumes every case with no `firstRun` bookkeeping:

| Case | `<id>.jsonl` at spawn | Flag | Result |
|---|---|---|---|
| Cold start, first spawn | absent | `--session-id` | creates (AC-2, byte-identical to #839) |
| Within-process respawn | present (child wrote it) | `--resume` | reattaches, appends |
| Daemon restart (hard) | present (survived) | `--resume` | reattaches — **AC-1** |

This is strictly better than streamsup's `firstRun` heuristic (which wastes one crash on daemon restart). The `ResolveSessionID` provider already runs **every spawn** under #839; the probe rides that existing cadence — no new seam in the loop.

### Why `--resume <id>` is safe here

The commit that first chose `--continue` over `--resume` (#3) rejected **bare `--resume`** because it opens claude's interactive session picker. We always pass `--resume <id>` **with the id**, which resumes that exact session non-interactively — the form `internal/streamsup` already uses and `lessons.md:54` documents. The id is a canonical UUID (`NewID()` / a `ValidStem`-gated rotated stem), so no picker, no flag injection. The transcript path stays `<id>.jsonl` under resume, so the #838 growth-confirm (`ResolveTranscript` keyed on the same id), the rotation watcher's `ref.ID == stem` guard, and per-session pinning all keep working unchanged.

## Design

### 1. `internal/supervisor/supervisor.go` — widen the provider, add the `--resume` branch

**Widen `Config.ResolveSessionID`** so one call resolves both the id and whether it already exists (atomic — one read, no id/decision skew across a mid-loop rotation):

```go
// ResolveSessionID, when non-nil, is called at the start of every spawn and
// returns (id, resume). A non-empty id emits "--resume <id>" when resume is
// true (the transcript already exists — reattach), else "--session-id <id>"
// (create). Either suppresses --continue. Resolved fresh each spawn so a
// /clear rotation and a just-created transcript are both picked up next spawn.
// Nil preserves the ResumeLast/--continue behaviour (per-caller, foreground).
ResolveSessionID func() (id string, resume bool)
```

**Extend `buildClaudeArgs`** (keep it pure, table-tested) — signature `(claudeArgs []string, firstRun, continueLast bool, sessionID string, resume bool) []string`. Behavior contract (the developer writes the body; the `args_test.go` table below asserts it):

- `sessionID != ""` → `resume` true ⇒ append `"--resume", sessionID`; `resume` false ⇒ append `"--session-id", sessionID`. Never `--continue` in either case.
- `sessionID == ""` → today's logic verbatim (`--continue` prepended iff `!firstRun && continueLast`). `resume` is ignored here.
- Never mutate `claudeArgs` (existing aliasing guarantee).

**`Run` call site** (was 747-751): read `(sessionID, resume)` from the provider when non-nil, thread both into `buildClaudeArgs`. Contract sketch:

```go
sessionID, resume := "", false
if s.cfg.ResolveSessionID != nil {
    sessionID, resume = s.cfg.ResolveSessionID()
}
args := buildClaudeArgs(s.liveArgs(), firstRun, s.cfg.ResumeLast, sessionID, resume)
```

`ResumeLast`/`continueLast` stay for the per-caller/foreground/test `--continue` paths — untouched.

### 2. `internal/sessions/pool.go` — compute the resume bit in the bootstrap closure

Name the id resolver once so both consumers share it, then compose the existence probe (contract, not final body):

```go
resolveID := func() string { return string(p.BootstrapID()) }
supCfg.ResolveSessionID = func() (string, bool) {
    id := resolveID()
    if id == "" || cfg.ClaudeSessionsDir == "" {
        return id, false // create semantics; empty-dir stays non-fatal
    }
    _, err := transcript.StatByID(cfg.ClaudeSessionsDir, id)
    return id, err == nil // exists ⇒ resume
}
```

- **`pinnedID` re-source** at line 511: pass `resolveID` (the `func() string`) to `newProbePreferredTranscriptResolver(...)` instead of `supCfg.ResolveSessionID` — the growth-confirm needs the id only, and the field is no longer `func() string`. Behaviour for that resolver is unchanged.
- **Import** `internal/transcript` in `pool.go` (already a same-package dependency via `reconcile.go`; `StatByID`/`ValidStem` live there).
- **`ClaudeSessionsDir == ""`** (unresolvable `$HOME`) ⇒ `resume=false` ⇒ `--session-id` (today's create semantics). The probe is at **spawn time**, never construction — it introduces **no** new startup-fatal condition (ticket's explicit constraint). `StatByID`'s absent-file error ⇒ `resume=false` ⇒ create; no error escapes the closure.

`New()`'s warm-start branch is otherwise untouched — it still reuses the persisted id (AC-2's byte-identical create path when no transcript exists). The resolution is deferred to spawn time, which is where the transcript-existence fact actually lives.

### Data flow after the change

```
supervisor.Run (each spawn)
  └─ ResolveSessionID() ─(RLock BootstrapID)→ id
        └─ StatByID(dir, id):  exists ─→ (id, true)  → "--resume <id>"      (reattach)
                               absent ─→ (id, false) → "--session-id <id>"  (create)

daemon restart, <id>.jsonl survived  → exists → "--resume <id>"            (AC-1)
cold start, no transcript            → absent → "--session-id <id>"        (AC-2)
in-process respawn after first spawn → exists → "--resume <id>"            (no loop)
/clear rotates p.bootstrap (RotateID persists) → next spawn resolves new id (AC-4 vacuous: no rotate here)
```

## Concurrency model

- The provider runs on the supervisor's `Run` goroutine each spawn. `BootstrapID()` takes `p.mu.RLock()`; `RotateID` writes `p.bootstrap` under `p.mu.Lock()`. The id read is fully synchronized (`-race`-clean) — unchanged from #839.
- **Atomicity of (id, resume).** Both come from **one** `resolveID()` call inside a single closure invocation, so the `StatByID` probe always targets the id that will be spawned — no skew if a `/clear` rotation races between two reads (the widened single-return provider is chosen precisely to avoid a two-call TOCTOU).
- The provider still reads `p.bootstrap` (not `sess.id`), preserving #839's "route new spawn-time readers around `sess.id`" invariant. `StatByID` adds no lock (a stateless `os.Stat`).
- Eventual consistency by design (unchanged): a `/clear` between resolve and the next spawn is picked up on the following spawn; no attempt to hot-swap a running child.

## Error handling

- **No new error path, no new sentinel.** `StatByID`'s error (absent/unreadable transcript) is interpreted as "not resumable ⇒ create", never surfaced. `ClaudeSessionsDir == ""` and `id == ""` both fall to `resume=false`.
- **Not startup-fatal.** The probe is spawn-time; a shared-dir read failure cannot abort `New()`. (`New()` already lost its last construction-time shared-dir read when #839 deleted `reconcileBootstrapOnNew`; this spec adds none back.)
- **Residual unresumable transcript** (corrupt/partial `<id>.jsonl` that claude refuses to `--resume`): `--resume` fails, the child crashes, backoff respawns, `--resume` fails again → a genuine crash-loop that #1164 alone cannot break. This is exactly #1165's job (N-fast-crash self-heal → `RotateID`). Called out as the seam between the two tickets, not handled here.

## Interaction with #1165 (belt-and-suspenders, different fabric)

- **#1164 (this) = deterministic root fix.** Resume-or-create per spawn by by-id transcript existence. Eliminates the `--session-id`-already-in-use crash-loop for the common cases (daemon restart, in-process respawn) **without** dropping the conversation.
- **#1165 = generic safety net.** If claude *still* fast-crashes N times (unresumable transcript, broken binary, bad config), rotate to a fresh id via `RotateID`. Different trigger (persistent failure vs transcript-exists) and different action (rotate vs resume) — so the pipeline's "belt-and-suspenders means different fabric" principle holds. Independent: either can ship first; together they're complete.

## Testing strategy

### `internal/supervisor/args_test.go` — extend the `buildClaudeArgs` table

Scenarios (bullets, not code):

- `sessionID != ""`, `resume=true` → output contains `--resume <id>` and **no** `--session-id`, **no** `--continue` — even with `firstRun=false, continueLast=true`.
- `sessionID != ""`, `resume=false` → output contains `--session-id <id>` and **no** `--resume`, **no** `--continue` (AC-2 arg shape; #839 rows preserved).
- `sessionID == ""` → every existing row unchanged for both `resume` values (regression: `--continue` iff `!firstRun && continueLast`; `resume` inert).
- Input-slice-not-mutated assertion extended to the new param.

### `internal/supervisor/restart_test.go` — update + one new supervisor test

- **Update** `TestSupervisor_Run_IgnoresSessionIDField`: closure becomes `func() (string, bool) { return resolvedID, false }`; the `--session-id <resolved>` assertion stays valid (`resume=false`).
- **New** (reuse `recorderConfig`/`waitForSpawns`): a `ResolveSessionID` returning `(id, true)` records a first spawn carrying `--resume <id>` and never `--session-id`. Proves the supervisor honours the resume bit end-to-end.

### `internal/sessions` — AC-5, drive `Pool`/`New` directly (no live claude)

Use the `helperPoolArgvRecorder`/`waitArgvRaw` pattern (real `/bin/sh` child records the spawned argv), with `ClaudeSessionsDir` set to a temp dir and a warm-start registry pinning `bootstrapID`:

- **AC-1 branch (transcript exists → resume):** pre-create `<bootstrapID>.jsonl` in `ClaudeSessionsDir`; assert the recorded argv contains `--resume <bootstrapID>` and **no** `--session-id`.
- **AC-2 branch (no transcript → create):** no `<bootstrapID>.jsonl`; assert `--session-id <bootstrapID>` and **no** `--resume` — byte-identical to #839.
- **AC-3 isolation:** with `<bootstrapID>.jsonl` **absent** but a **foreign** `<other-uuid>.jsonl` present (even newer), assert the argv still carries `--session-id <bootstrapID>` — the by-id probe ignores foreign files; no dir scan, no adoption.
- **(Recommended, strengthens "no loop")** in-process respawn: a recorder script that writes `<bootstrapID>.jsonl` then exits non-zero so the supervisor respawns; assert the **second** spawn's argv carries `--resume <bootstrapID>`. Proves the fix is complete without leaning on #1165. Optional — AC-5 requires only the two startup branches above.

The existing #839 `pool_bootstrap_sessionid_test.go` cases (no-foreign-adoption, `BootstrapID` follows `RotateID`, stability/warm-start reuse) stay green — the resume bit rides on top of them.

### Full suite

`go test -race ./...`, `go vet ./...`, `staticcheck ./...`, and `go test -tags e2e ./internal/e2e/...`. The e2e run is the ground truth that interactive `--resume <id>` brings up a working session under tui-driver's readiness gates (see § Open questions).

## Out of scope

- **`internal/streamsup` daemon-restart-first-spawn crash.** streamsup self-heals in one respawn (`firstRun→false` after `cmd.Start` succeeds), so it does not infinite-loop; its equivalent by-id existence probe is a separate, unobserved-on-that-path concern. The observed crash-loop (ticket) is the PTY path only. Do not touch `streamsup.buildArgs`.
- **#1165's supervisor N-fast-crash self-heal** — separate independent ticket.
- **Renaming `ResolveSessionID`** to reflect the widened return (e.g. `ResolveSpawn`) — kept as-is to minimize churn; a rename is a cosmetic follow-up if desired.
- **Removing `-pyry-resume` / `SessionConfig.ResumeLast` / `Config.ResumeLast`** — still back the per-caller/foreground `--continue` paths (#839 out-of-scope, unchanged).

## Open questions

- **Interactive `--resume <id>` under tui-driver.** `--resume <id>` is proven in `internal/streamsup` (headless stream-json) and documented in `lessons.md:54`, but the interactive PTY bootstrap has never spawned `--resume <id>` in production (it used `--continue`, then #839's `--session-id`). The readiness gates (`WaitReady`, trust modal, growth-confirm) should behave identically or better on a resumed session (same id, same `<id>.jsonl`), but the `-tags e2e` run against the fakeclaude harness is the ground truth. If a specific gate misbehaves on resume, that is a tui-driver/readiness follow-up, not a reason to fall back to rotate — #1165 backstops any residual failure.
- **Empty-id defense placement.** The closure returns `("", false)` on an empty id and the supervisor emits neither flag (fresh session). Equivalent to defending in `buildClaudeArgs`; kept in the sessions closure so `buildClaudeArgs` stays the single arg-shape authority. Behaviour, not placement, is the contract.
