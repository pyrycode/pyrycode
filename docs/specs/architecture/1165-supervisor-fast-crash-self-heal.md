# Spec: Supervisor self-heals on a deterministic fast-crashing bootstrap child (#1165)

**Size:** S · **Security-sensitive:** no (local daemon lifecycle; the rotated
identifier is a UUIDv4, not key material; no untrusted input reaches the new
code) · **Split from:** #1163 · **Sibling root fix:** #1164 (merged, PR #1166)

## Files to read first

- `internal/supervisor/supervisor.go:731-815` — `Run` loop. The backoff path
  (`uptime := time.Since(start)`, the two parent-ctx / `drainRestart` guards,
  `bo.next(uptime)`). This is the **only** insertion point for the fast-crash
  accounting + self-heal call.
- `internal/supervisor/supervisor.go:100-189` — `Config` struct. `ResolveSessionID`
  (the #839 pull-callback the fresh id flows back through) and the backoff-param
  block are the models for the three new fields.
- `internal/supervisor/supervisor.go:637-656` — `New` default-application block
  (`if cfg.BackoffInitial == 0 { … }`). Mirror it for the two new duration/int
  fields.
- `internal/supervisor/supervisor.go:781-785` — the `err != nil` ("claude exited")
  vs `else` ("claude exited cleanly") log branch. Confirms `runOnce`'s error is
  non-nil **iff** the child exited abnormally/non-zero — the AC1 "exits non-zero"
  signal.
- `internal/supervisor/backoff.go:1-45` — `backoffTimer`. Self-heal is **orthogonal**
  to backoff: extract `uptime` the same way, but do NOT change this file.
- `internal/sessions/pool.go:625-656` — `RotateID` + `rekeyLocked`. The shared
  re-key-under-`p.mu` seam the new pool method is modelled on.
- `internal/sessions/pool.go:449-535` — bootstrap `supCfg` wiring (where
  `ResolveSessionID` / `ResolveTranscript` are set). The `SelfHeal` closure is
  added here; `p` is late-bound exactly like `resolveID`.
- `internal/sessions/pool.go:1076-1091` — `BootstrapID()` (current pinned id read
  under RLock).
- `internal/sessions/transition.go:90-140` — `RotateForNewSession`. The mint+rekey
  precedent. **Read the differences carefully** (§ Design decision 3): self-heal
  does NOT register the skip-set and does NOT fire a client transition.
- `internal/sessions/rotation/watcher.go:138-190` — `handleCreate`. The
  `ref.ID == stem` guard at **lines 161-165** (comment: "the bootstrap entry was
  rotated by another path") is *why* self-heal needs no skip-set registration.
- `internal/sessions/id.go:20-32` — `NewID()` (fresh UUIDv4 via crypto/rand).
- `internal/supervisor/supervisor_test.go:170-318` — `TestHelperProcess` fake-child
  + `helperConfig` harness. The self-heal supervisor test reuses this; add one new
  helper mode (§ Testing strategy).

## Context

When the bootstrap child exits non-zero deterministically, the supervisor's
`Run` loop retries **forever** on the exponential backoff ladder
(0.5/1/2/4/8/16/…/30s). The daemon process stays up but has no working
interactive session; only a manual runbook recovery clears it. Forever-retry is
the correct default for a *service* supervisor recovering from a *transient*
outage — but a *deterministic* fast-crash never recovers on its own.

#1163 observed exactly this: claude 2.1.199 refuses `--session-id <uuid>` when
`<uuid>.jsonl` already exists ("Session ID … is already in use"), exiting in
~220ms, in an infinite loop. The sibling root fix #1164 (merged) resolves *that
trigger* — the bootstrap now spawns `--resume <id>` when its transcript exists.

**Evidence-Based Fix Selection — is this backstop warranted now, or deferred?**
Warranted now. The distinction is trigger vs failure mode:

- The **failure mode** — a deterministic fast-crash silently wedging the daemon
  with no bootstrap session — *was observed* (#1163). We are not defending a
  hypothetical.
- #1164 fixes one **trigger** ("id already in use"). Any *other* deterministic
  fast-crash cause re-wedges the daemon the same way, and the loop gives no
  signal and never self-clears.
- "Belt-and-Suspenders Means Different Fabric" explicitly endorses pairing a
  specific root fix (deterministic *startup* resolution, #1164) with a general
  backstop (deterministic *runtime* detection, here) when both are code. They
  are different fabric.
- This is the concrete realization of the long-tracked **"Backoff cooldown/
  bail-out"** follow-up (`docs/PROJECT-MEMORY.md` → Open follow-ups) — a gap the
  team already flagged, not novel speculation.
- Cost is low: S, additive, no consumer fan-out, self-contained.

## Design

Two seams, mirroring the #839 decoupling: the supervisor **detects** the
crash-loop (it owns uptime + exit status) and **signals** an owner-supplied
callback to rotate; the pool **implements** the rotation (it owns `SessionID`
and the registry). The supervisor stays zero-knowledge of `SessionID`.

```
Run loop (each crash iteration, after the shutdown + drainRestart guards):

  err != nil && uptime < FastCrashWindow ?
     ├─ yes → fastCrashes++
     └─ no  → fastCrashes = 0            (healthy run or clean exit resets streak)

  SelfHeal != nil && fastCrashes >= FastCrashThreshold ?
     └─ yes → SelfHeal()                 (owner mints a fresh pinned id + persists)
              fastCrashes = 0            (give the fresh id a full N-window)

  → fall through to existing bo.next(uptime) backoff + respawn
       next spawn: ResolveSessionID() reads the rotated id → --session-id <newID>
       (newID has no transcript → resume=false) → clean spawn
```

The self-heal **reuses the existing `ResolveSessionID` pull** to apply the fresh
id — no new push into the spawn path. `SelfHeal()` only mutates `p.bootstrap`;
the very next iteration's `ResolveSessionID()` (`func() string { return
string(p.BootstrapID()) }`) resolves the rotated id automatically. This is the
elegant part: the rotation and the respawn are already wired.

### Design decision 1 — new `supervisor.Config` fields (three, all additive/optional)

```go
FastCrashWindow    time.Duration // "fast" uptime threshold; 0 → default 2s
FastCrashThreshold int           // consecutive fast crashes to trip; 0 → default 4
SelfHeal           func() error  // nil → self-heal disabled (retry forever, unchanged)
```

- Modelled on the existing `BackoffInitial/Max/Reset` fields (zero → default,
  applied in `New`) and on `ResolveSessionID` (optional callback, nil-disabled).
- **Additive-only → zero edit fan-out.** Every existing `supervisor.Config{…}`
  literal (`buildSession`, `helperConfig`, foreground `cmd/pyry`) leaves the new
  fields zero and is byte-identical in behaviour: `SelfHeal == nil` disables the
  whole path. Only the bootstrap `supCfg` in `pool.go` `New()` sets `SelfHeal`.
- **`SelfHeal func() error`, not a `SessionID`-typed push.** The supervisor never
  learns the id — same decoupling as `ResolveSessionID`. A non-nil return is
  logged (`slog.Warn`) and the streak resets; the loop falls back to
  retry-forever on the same id (no regression vs today).

### Design decision 2 — defaults, documented at the point of enforcement (AC4)

Package-level consts in `supervisor.go`, applied in `New`:

```go
const (
    // A non-zero child exit within this uptime is a "fast crash". The #1163
    // collision failed in ~220ms; 2s covers a deterministic startup crash
    // (incl. ~200ms re-exec/startup overhead) yet stays far below any real
    // interactive session (minutes).
    defaultFastCrashWindow = 2 * time.Second
    // Consecutive fast crashes that trip self-heal. Chosen in the ticket's 3–5
    // band. With the 0.5/1/2/4s ladder, 4 fast crashes elapse in ~7.5s —
    // long enough to rule out a one-off, short enough to self-heal in seconds.
    defaultFastCrashThreshold = 4
)
```

The doc comments at these consts + the `Config` field comments + the inline
comment on the `Run` accounting block together satisfy AC4 ("documented at the
point of enforcement").

### Design decision 3 — new pool method: mint + rekey + persist (atomic under `p.mu`)

Add to `internal/sessions/pool.go`:

```go
// RotateBootstrapForSelfHeal mints a fresh daemon id, re-keys the CURRENT
// bootstrap entry to it, and persists. Reads p.bootstrap under the SAME p.mu
// hold as the re-key, so a (practically impossible during a crash-loop)
// concurrent /clear can't skew old vs new. Returns the minted id.
func (p *Pool) RotateBootstrapForSelfHeal() (SessionID, error)
```

- Body: `NewID()` → `p.mu.Lock()` → read `old := p.bootstrap` → guard
  `p.sessions[old]` present (else `ErrSessionNotFound`) → `p.rekeyLocked(old,
  newID)` → `p.saveLocked()` → unlock. Reuses `rekeyLocked` (the same shared
  re-key seam `RotateID` and `RotateForNewSession` use).
- **`saveLocked` failure posture:** match `RotateForNewSession` — log at `Warn`
  and return `(newID, nil)`. The in-memory rotation is authoritative for the
  running daemon (which is what self-heal needs: get *this* process off the
  wedged id now); persistence is best-effort.
- The bootstrap `SelfHeal` closure is a thin adapter:
  `SelfHeal: func() error { _, err := p.RotateBootstrapForSelfHeal(); return err }`
  (added in the `supCfg` block, pool.go ~line 486, next to `ResolveSessionID`).

**Why NOT reuse `RotateForNewSession`, and why NO skip-set registration:**

- `RotateForNewSession` also registers the new id in the allocated skip-set AND
  fires a `ReasonClear` client transition + rebinds the conversation. Self-heal
  wants **neither**: it is a supervisor-internal crash-recovery rotation, and
  `ReasonClear` would mislead clients into thinking the user ran `/clear`.
  Client notification of a self-heal is **out of scope** (§ Open questions).
- **No skip-set entry is needed** — and this is the load-bearing correctness
  argument, so verify it in review. Self-heal re-keys `p.bootstrap` → `newID`
  *before* the next spawn creates `<newID>.jsonl`. When the fsnotify watcher
  fires `handleCreate` for `<newID>.jsonl`, `Snapshot()` already returns
  `{ID: newID}` (the rekey committed under `p.mu`, happens-before the spawn →
  jsonl-create → fsnotify chain), so the `ref.ID == stem` guard at
  `watcher.go:161-165` returns early — no rotation. This is **structurally
  identical to cold-start**, where the bootstrap id is likewise NOT in the
  skip-set (`pool.go:570-579`) and relies on the same guard. Adding a skip-set
  entry would defend an unobserved probe-timing race that the guard already
  covers — declined per Evidence-Based Fix Selection.

### What is deliberately NOT changed

- `backoff.go` — untouched. Self-heal does not reset the backoff timer; the
  fresh id spawns after the current backoff delay, comes up clean, stays up
  past `BackoffReset` (60s), and backoff resets naturally on the next crash (if
  any). Coupling self-heal to backoff would add behaviour for no AC.
- `Config.SessionID` (the #1108 construction-fixed field) — the PTY supervisor
  ignores it; only a stream-json `RunnerFactory` reads it, and that path is not
  wired to the bootstrap PTY supervisor. It goes stale after a self-heal
  rotation but is never consulted on this path. Out of scope; note only.
- No new `SessionTransition` reason, no wire change, no `cmd/pyry` change.

## Concurrency model

- The fast-crash counter (`fastCrashes int`) is a `Run`-goroutine-local, declared
  alongside `bo`/`firstRun`. No lock — single-goroutine state.
- `SelfHeal()` runs on the `Run` goroutine, synchronously between iterations.
  `RotateBootstrapForSelfHeal` takes `p.mu` (write) for the read-current +
  rekey + persist, matching the `RotateID`/`RotateForNewSession` invariant
  (lock order `Pool.mu → Session.lcMu`, via `rekeyLocked`). No new lock, no new
  ordering.
- The rotated id reaches the spawn path via the already-existing `ResolveSessionID`
  RLock read on the next iteration — no cross-goroutine handoff added.

## Error handling / failure modes

| Condition | Behaviour |
|---|---|
| Fast non-zero exit, streak < N | increment counter, normal backoff + respawn on **same** id |
| Healthy exit (uptime ≥ window) or clean exit (err == nil) | reset counter to 0 (AC2/AC3), normal backoff/respawn |
| Streak reaches N, `SelfHeal != nil` | rotate to fresh id, reset counter, backoff + respawn on **new** id (AC1) |
| `SelfHeal()` returns error (rng fail / absent bootstrap TOCTOU) | `slog.Warn`, reset counter, fall back to retry-forever on same id — no regression |
| `SelfHeal == nil` (per-caller sessions, foreground, tests) | entire path inert — retry-forever, byte-identical to today |
| `saveLocked` fails inside the pool method | `Warn` + in-memory rotation stands; daemon uses fresh id immediately |
| Spawn failure unrelated to the id (missing binary) | counts as a fast crash; rotating the id is a harmless no-op improvement (never worse — still loops on backoff) |

## Testing strategy

Two independent test surfaces; neither couples packages.

**A. Supervisor detection/trigger (`internal/supervisor`, reuse `TestHelperProcess`
+ `helperConfig`).** Inject a fake `SelfHeal func() error` (records call count;
the test does NOT need a real `Pool`). Set `FastCrashWindow`/`FastCrashThreshold`
on the `Config` to keep the test fast and deterministic. Add one helper-child
mode (e.g. `fast_crash_until_healed`): reads a marker file — absent → `os.Exit(1)`
immediately (fast crash); present → block until killed (healthy). The injected
`SelfHeal` writes the marker + increments its counter.

Scenarios (bullet, not full bodies — developer writes them in the table-driven
idiom):

- **AC1/AC5 (trigger + exactly one rotation → clean spawn):** `FastCrashThreshold`
  small (e.g. 3), `FastCrashWindow` generous (e.g. 5s, so instant exits — whose
  uptime is dominated by the ~200ms re-exec overhead — count as fast). Child
  fast-crashes; at the Nth, `SelfHeal` fires once, writes the marker; the next
  spawn reads the marker and stays up. Assert: `SelfHeal` called **exactly once**,
  and the child then runs (state reaches `PhaseRunning` and persists / no further
  crash). Cancel ctx to end.
- **AC2/AC3 (healthy/slow exit does NOT trigger; streak resets):** child stays up
  past `FastCrashWindow` before a non-zero exit (`sleep_then_crash`-style, sleep
  clearly above the chosen window), OR a clean exit. Assert `SelfHeal` **never**
  called. A stronger variant: interleave `k < N` fast crashes, then one healthy
  run, then fast crashes again — assert `SelfHeal` never fires because the streak
  never reaches N. Watch the re-exec overhead (~200ms is part of `uptime`): the
  window must sit clearly on one side of the child's real uptime.
- **AC3 disabled path:** `SelfHeal == nil` → fast-crash loop never calls anything;
  supervisor keeps retrying (assert it does not panic / `SelfHeal` seam untouched).

**B. Pool rotation primitive (`internal/sessions`, e.g. in `pool_test.go` or a new
`selfheal_test.go`).** Unit-test `RotateBootstrapForSelfHeal` directly on a `Pool`:

- Returns a fresh, `ValidID` UUID distinct from the old bootstrap.
- `p.BootstrapID()` now returns the new id; the old id is gone from `p.sessions`.
- The registry file on disk is re-persisted with the new id (round-trip
  `readRegistry` / the existing pool test helper).
- No `ErrSessionNotFound` on the happy path; the bootstrap `Session` pointer is
  preserved through the rekey (same `*Session`, new key).

## Open questions

1. **Client notification of a self-heal.** After a self-heal rotation, a remote
   client tracking the bootstrap id holds a stale id (a `/clear` rotation *does*
   fire a `ReasonClear` transition; self-heal deliberately does not). The ACs do
   not require notification, and adding a `SessionTransition` reason + wire
   threading is beyond S. **Recommendation:** ship without it; file a follow-up
   if operators observe stale client views after a self-heal. Flagging for
   PO/reviewer visibility.
2. **`FastCrashWindow` / `FastCrashThreshold` as `Config` fields vs consts.** Chosen
   as fields (zero → default) to mirror the backoff params and keep the tests
   fast/deterministic. If a reviewer prefers pure consts, the tests would need
   real sub-second timing against the ~200ms re-exec overhead — messier. Fields
   is the recommended call.
