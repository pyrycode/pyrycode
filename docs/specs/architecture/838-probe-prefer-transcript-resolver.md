# Spec #838 — Probe-prefer `newTranscriptResolver` (delivery-confirm tail baseline) in a shared sessions dir

**Ticket:** [#838](https://github.com/pyrycode/pyrycode/issues/838) · split from #828 · size **S** · `security-sensitive`

## Files to read first

- `internal/sessions/reconcile.go:57-122` — `mostRecentJSONL` + `newTranscriptResolver` (the mtime baseline this ticket replaces on the probe path and **keeps** as the AC5 no-lsof fallback) + `uuidStemPattern`/`jsonlExt` (reused by the new guard). New code lands in this file.
- `internal/sessions/pool.go:422-434` — the wiring site: `supCfg.ResolveTranscript = newTranscriptResolver(cfg.ClaudeSessionsDir)` at `429`, `supervisor.New(supCfg)` at `431`. `supCfg` is copied **by value** into the supervisor before the bootstrap `Session`/`Pool` exist — the root of the late-bound-PID subtlety.
- `internal/sessions/pool.go:30` — `newProbe = rotation.DefaultProbe` (the probe factory, already present; reused to construct the resolver's probe).
- `cmd/pyry/interactive_turn_stream_v2.go:250-367` — the shipped sibling `resolveOwnBootstrapJSONL` (#827). **Copy the guard *shape*, invert the not-found *convention*** (see § Error handling). Note it also carries `resolvedOnce`/`sawEmpty` cold/warm offset state we deliberately drop.
- `cmd/pyry/interactive_turn_stream_v2.go:33-46` — `availabilityReporter` interface + `bootstrapProbeUsable`. Mirror these unexported into `internal/sessions` (same duplication direction the sibling already took for `jsonlStemPattern`).
- `cmd/pyry/relay.go:413-422` — the sibling's live-PID wiring `pidFn := func() int { return sup.State().ChildPID }`. Our `New` must reproduce this **without** a constructed `sup` in scope yet.
- `internal/supervisor/supervisor.go:352-455` — `confirmViaTranscriptGrowth` (the consumer). Line `409` baseline-resolve, line `410-423` the **stochastic Committed-chip fallback a non-nil error diverts to**, `grew()` at `453`. This is why AC3's `("", 0, nil)` convention is load-bearing.
- `internal/sessions/rotation/probe.go:18-20` + `probe_darwin.go:17-38` — `Probe` interface (`OpenJSONL(pid) (string, error)`) and `noopProbe.Available() == false` (no-lsof fallback signal). `DefaultProbe` on Linux always returns a usable `linuxProbe{}`.
- `internal/sessions/reconcile_test.go:177-246` — the `newTranscriptResolver` test idiom (`touchJSONL`, `t.TempDir()`, direct constructor invocation) the new tests mirror.
- `internal/sessions/pool_test.go:799-801` — the `newProbe` package-var override pattern (available if a live-pool wiring test is added; the resolver's own tests inject a fake probe directly instead).

## Context

PR #827 (`88b2e1e`) made the **interactive turn/modal stream** resolver probe-preferred: it tails the `<uuid>.jsonl` the daemon's own claude child actually holds open (via `rotation.Probe` + the child PID) instead of the newest file by mtime, so a second interactive claude in the same shared `~/.claude/projects/<encoded-cwd>/` folder can't redirect the tail. It deliberately deferred the **other** live-child bootstrap-transcript consumer:

`newTranscriptResolver` (`reconcile.go`), wired as `supervisor.Config.ResolveTranscript` (`pool.go:429`), is the **delivery-confirm growth baseline**. `confirmViaTranscriptGrowth` (#668) resolves it *before* delivering a turn and again while polling, confirming the turn committed by watching that file grow. It calls `mostRecentJSONL` — so when a second claude writes a newer transcript into the same dir, the baseline latches onto the wrong file and the growth-confirm can false-confirm or hang on a file the daemon's own child never touches.

This consumer has a **live** bootstrap child at resolve time (growth-confirm runs during `WriteUserTurn`, well after startup), so the exact probe + child-PID mechanism applies. Self-contained `internal/sessions` change — no cross-package plumbing.

## Design

### New unexported symbols — `internal/sessions/reconcile.go`

Add (do **not** touch `mostRecentJSONL` / `newTranscriptResolver` / `reconcileBootstrapOnNew` — all stay for reconcile + the AC5 fallback):

```
// availabilityReporter is the optional interface a rotation.Probe implements to
// declare it cannot answer OpenJSONL (the no-lsof noopProbe → false). Mirrors
// cmd/pyry's local copy; the shared rotation.Probe stays single-method.
type availabilityReporter interface{ Available() bool }

func probeUsable(probe rotation.Probe) bool
    // type-assert to availabilityReporter; a probe without the method is usable
    // (linuxProbe / real darwinProbe), noopProbe reports false.

func newProbePreferredTranscriptResolver(
    dir string, probe rotation.Probe, pidFn func() int,
) func(ctx context.Context) (string, int64, error)
```

`newProbePreferredTranscriptResolver` behaviour (contract — the developer writes the body; the invariants are pinned by the tests in § Testing):

1. **No usable probe** (`!probeUsable(probe)`) → `return newTranscriptResolver(dir)`. AC5: no-lsof path stays byte-identical to today's newest-by-mtime baseline, mirroring #827's `noopProbe.Available()==false` delegation. This is the **only** returned closure that may emit a non-nil error, and only because it *is* today's behaviour.
2. Otherwise precompute `resolvedDir` once: `filepath.EvalSymlinks(dir)`, falling back to `filepath.Clean(dir)` on error — same dir canonicalisation the sibling and the rotation watcher use. The returned closure, per resolve:
   - `pid := pidFn()`. `pid <= 0` (restart backoff / not yet spawned) → **`("", 0, nil)`**. Probe is not called.
   - `open, err := probe.OpenJSONL(pid)`. `err != nil` → **`("", 0, nil)`** (see § Error handling — a transient/unrecoverable probe error must not divert the caller to the stochastic fallback, and must never fall back to mtime).
   - `open == ""` (cold start: claude under `--continue` has not created its JSONL fd yet) → **`("", 0, nil)`**.
   - **Confidentiality guard (AC4):** canonicalise `open` (`EvalSymlinks`, `Clean` on error). Reject → **`("", 0, nil)`** when either: `filepath.Dir(openResolved) != resolvedDir`, or the base name lacks the `.jsonl` suffix / its stem fails `uuidStemPattern`.
   - `candidate := filepath.Join(dir, base)` (rebuilt under the **original** `dir`, the form the rest of the daemon uses; the symlink-resolved form is guard-only). `os.Stat(candidate)`; error (raced away between probe and stat) → **`("", 0, nil)`**.
   - Success → `(candidate, info.Size(), nil)`.

**Deliberately dropped vs the sibling:** no `resolvedOnce` / `sawEmpty` cold/warm-offset state. That machinery exists only to pick tail-offset 0 vs EOF for the turn-*stream* subscriber. This consumer uses the return purely as a `(path, size)` **baseline** for `grew()` — it needs the true current byte size every call, never a rewound offset. Dropping it is a real simplification, not an omission.

### Wiring — `internal/sessions/pool.go`, `New`

The resolver needs the bootstrap child's **live** PID (`sup.State().ChildPID`), but `sup` is constructed at `pool.go:431` *after* `supCfg.ResolveTranscript` is assigned (`429`) and *copied by value* into the supervisor — the config is frozen by the time `sup` exists. Moving the assignment later can't work; the fix is a **late-bound holder** the `pidFn` closes over.

Change the `if cfg.ClaudeSessionsDir != ""` block (currently `pool.go:428-430`) plus a holder declaration above it and a one-line late-bind below `supervisor.New`:

```
var bootstrapSup *supervisor.Supervisor   // declared before the block
...
if cfg.ClaudeSessionsDir != "" {
    probe := newProbe(cfg.Logger)
    pidFn := func() int {
        if bootstrapSup == nil {
            return 0            // pre-construction guard; never observed at resolve time
        }
        return bootstrapSup.State().ChildPID
    }
    supCfg.ResolveTranscript = newProbePreferredTranscriptResolver(cfg.ClaudeSessionsDir, probe, pidFn)
}
sup, err := supervisor.New(supCfg)
if err != nil { ... }          // unchanged
bootstrapSup = sup             // late-bind the live-PID source now that sup exists
```

`newProbe(cfg.Logger)` reuses the existing `pool.go:30` factory. A second probe instance (the rotation watcher builds its own at `pool.go:983`) is fine — the probe is a stateless lsof/`/proc` wrapper, and cmd/pyry already runs a separate `newBootstrapProbe` instance. The probe is built only inside the `ClaudeSessionsDir != ""` branch, so the growth-confirm-disabled path pays nothing.

### Data flow

```
WriteUserTurn                                    (supervisor, per turn)
  └─ confirmViaTranscriptGrowth
       ├─ resolve(ctx)  ── baseline ──►  newProbePreferredTranscriptResolver closure
       │                                    pidFn() ─► bootstrapSup.State().ChildPID (live)
       │                                    probe.OpenJSONL(pid) ─► own child's <uuid>.jsonl
       │                                    guard: dir == resolvedDir && UUID stem
       │                                    (path, size)  |  ("",0,nil) no-baseline
       ├─ deliver(ctx)                     (PTY write)
       └─ poll resolve(ctx) until grew(base, new)  |  timeout → ErrTurnNotCommitted
```

## Concurrency model

No new goroutines. Two shared reads to reason about:

- **`bootstrapSup` holder.** Written twice on the `New` goroutine (`nil` init, then `= sup`), both completing before `New` returns. `pidFn` reads it only from the supervisor's `WriteUserTurn` goroutine, which is created long after `New` returns (via `Pool.Run` → relay handlers). Goroutine creation is a happens-before edge, so the read strictly follows both writes — race-free, no mutex needed. No goroutine reads the holder *during* `New` (`supervisor.New` and `reconcileBootstrapOnNew` are synchronous and never invoke `ResolveTranscript`). The `bootstrapSup == nil` guard is belt-and-suspenders for the impossible "resolve before late-bind" ordering; it is never observed in practice.
- **`sup.State().ChildPID`.** `Supervisor.State()` is mutex-guarded (same accessor the sibling reads per-resolve at `relay.go:420`); reading it while the child respawns across backoff returns a consistent snapshot (`0` between restarts).

## Error handling — the load-bearing no-baseline convention

The consumer's `(path, size, err)` branches are asymmetric — this is the whole point of the ticket (see memory: *probe-prefer resolver no-baseline convention is consumer-specific*):

| Resolve result | Baseline call (`supervisor.go:409`) | Poll call (`supervisor.go:442`) |
|---|---|---|
| `(path, size, nil)` | baseline set → growth path | `grew()` check |
| `("", 0, nil)` | **empty baseline → growth path** (deliver, then poll for own child's file to appear/grow; else loud `ErrTurnNotCommitted`) | `grew("",0,…)` false → keep polling |
| non-nil error | **diverts to stochastic Committed-chip fallback** (`supervisor.go:410`) — the #668 heuristic this path exists to replace | treated as "no growth this tick" |

The resolver cannot tell which call site it serves, so **every no-result condition on the probe path returns `("", 0, nil)`, never a non-nil error** (AC3). Consequences that are *intended*, not accidental:

- A persistent probe error or perpetually-down child yields `("", 0, nil)` at baseline and every poll → deliver happens, growth never observed → `ErrTurnNotCommitted` (loud, retryable). This is the **designed** safe failure — a false-negative that the caller surfaces, never a false-ack.
- We **never** fall back to `mostRecentJSONL` on the probe path (AC3): that is the colliding heuristic #838 deletes. mtime is reachable only via the `!probeUsable` AC5 branch, which *is* today's behaviour.

Contrast the sibling `resolveOwnBootstrapJSONL`: it returns non-nil errors for the same conditions because its subscriber *retries on error*. Copying that convention here would silently reintroduce false-ack risk. **Copy the guard logic; invert the not-found signal.**

## Testing strategy

Table-driven, in `internal/sessions/reconcile_test.go`, mirroring the existing `TestNewTranscriptResolver_*` idiom (`t.TempDir()`, `touchJSONL`, direct constructor call, `t.Parallel()`). Inject a **fake `rotation.Probe`** (a struct with a scriptable `OpenJSONL(pid) (string, error)`; optionally implementing `Available()` to exercise both branches) and a **fake `pidFn`** (closure returning a chosen int) — no live claude, no `newProbe` override needed.

Scenarios (each an assertion on `(path, size, err)`):

- **Probe-preferred selection (AC1):** fake probe returns the daemon child's `<uuid>.jsonl`; a valid file at that path with content N. Expect `(that path, N, nil)` — even when a lexically/mtime-newer foreign `<uuid>.jsonl` also exists in the dir.
- **Newer foreign sibling regression (AC2):** two valid `<uuid>.jsonl` in the dir; the foreign one has the later mtime. Probe points at the daemon's own (older-mtime) file. Expect the resolver returns the daemon's own path, proving mtime is not consulted.
- **`pid <= 0` (AC3):** `pidFn` returns `0` (and separately `-1`). Expect `("", 0, nil)`; assert the probe's `OpenJSONL` was **not** called (a call-count field on the fake).
- **Empty probe (AC3):** probe returns `("", nil)`. Expect `("", 0, nil)`.
- **Probe error (AC3):** probe returns `("", someErr)`. Expect `("", 0, nil)` — assert `err == nil` explicitly (this is the convention-inversion vs the sibling; the most important negative test).
- **Guard: outside dir (AC4):** probe returns a valid-UUID `.jsonl` path in a *different* temp dir. Expect `("", 0, nil)`.
- **Guard: non-UUID stem (AC4):** probe returns `<dir>/not-a-uuid.jsonl` (and a valid-UUID name without the `.jsonl` suffix). Expect `("", 0, nil)`.
- **Vanished between probe and stat:** probe returns a path with no file on disk. Expect `("", 0, nil)`.
- **No-lsof fallback byte-identity (AC5):** fake probe with `Available() == false`; assert the constructor returns a closure behaviourally identical to `newTranscriptResolver(dir)` over the same tempdir fixtures (same newest-by-mtime pick, same `("", 0, nil)` on empty, same `fs.ErrNotExist` propagation on a missing dir). Simplest assertion: build both, compare their outputs on a shared fixture including the newest-mtime and empty-dir cases.

Full suite must stay green: `go test -race ./...`, `go vet ./...`, `staticcheck ./...`. Confirm no existing pool/session test regresses — none drive `ResolveTranscript` through a live pool today (only `session_test.go:331` *mentions* `WriteUserTurn`, in a comment), so switching the wired resolver is invisible to them; the change is exercised only by the new unit tests and the real growth-confirm path.

## Security review

**Verdict:** PASS

Adversarial self-review per `architect/security-review.md`, gated on the `security-sensitive` label. The one new trust decision is the AC4 confidentiality guard; every no-result path fails safe to `("", 0, nil)`.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The single untrusted→trusted crossing is the probe-reported path `open := probe.OpenJSONL(pid)` — under PID reuse (child exits mid-resolve, OS reassigns the number) the fd table could name any file on disk. The boundary is **one explicit gate** in `newProbePreferredTranscriptResolver`'s closure (§ Design step 2): accept only if `filepath.Dir(EvalSymlinks(open)) == resolvedDir` **and** the base stem matches `uuidStemPattern`; reject → `("", 0, nil)`. Downstream, the supervisor only `os.Stat`s the result for its **size** (`grew()` compares path+size) and never reads the bytes — so even a guard bypass could at worst mis-size a baseline, never disclose another claude's transcript *content*. Narrower blast radius than the byte-tailing sibling (#827). PID source is trusted (`bootstrapSup.State().ChildPID`, server-owned).
- **[File operations]** No MUST FIX. Path traversal is closed: `candidate := filepath.Join(dir, base)` with `base` already through the `uuidStemPattern` gate (36 hex/hyphen chars, no `/`, no `..`, only the fixed `.jsonl`) cannot escape `dir`. Symlinks are canonicalised on **both** sides before the dir-equality compare (`resolvedDir` once at construction, `EvalSymlinks(open)` per resolve), so an in-dir symlink pointing out is rejected — the comparison is over real paths, not lexical. TOCTOU note (accepted, not gating): the `guard → os.Stat(candidate)` sequence is inherently racy like any stat, and `candidate` is rebuilt under the original `dir` (matching the daemon-wide path form) rather than the resolved form — but the sole use is a size read, so the worst outcome of a swap is a mis-confirmed/mis-missed turn (correctness), never a content leak; the pre-existing mtime resolver carried the identical stat-race. No new files created (read-only resolver) → no mode/atomic-write surface.
- **[Tokens/secrets]** No findings — the change generates, stores, and logs no token, key, or credential.
- **[Subprocess]** No findings — this ticket adds no `exec`. The probe's own `lsof` invocation lives in `internal/sessions/rotation` (unchanged); the only value crossing into it is the daemon's own child PID, not caller input.
- **[Cryptographic primitives]** No findings — no crypto, no RNG on this path.
- **[Network & I/O]** No findings — no socket, no unbounded read; `os.Stat` metadata only, no size cap needed.
- **[Error messages, logs, telemetry]** No findings, net-positive: the resolver returns `("", 0, nil)` (not wrapped errors) for every no-result branch, so it emits **no** per-branch log and leaks no probed path into logs. It also stops the caller's `supervisor.go:414` "baseline resolve failed" Warn from firing on routine cold-start/backoff polls (that Warn only triggers on a non-nil baseline error, which the probe path never returns). SHOULD-CONSIDER (deferred, not gating): swallowing probe errors silently loses "probe persistently failing" observability — but the symptom still surfaces loudly as `ErrTurnNotCommitted`, and threading a logger to emit a Debug line would widen the signature for a diagnostic nicety; leave to a future observability pass.
- **[Concurrency]** No MUST FIX — see § Concurrency model. The `bootstrapSup` holder is written-once on the `New` goroutine before return and read only from the later `WriteUserTurn` goroutine (happens-before via goroutine creation); the resolver takes no locks and adds no lock-order edge; `sup.State()` is mutex-guarded. `go test -race` validates the holder ordering; the `nil` guard covers the impossible pre-late-bind call.
- **[Threat model alignment]** Addresses the target threat directly: a second interactive claude in the shared `~/.claude/projects/<encoded-cwd>/` dir can no longer redirect the delivery-confirm baseline onto its own newer transcript (false-ack / mis-confirm). The reconcile-time variant (`ChildPID == 0`, `reconcileBootstrapOnNew`) is explicitly OUT OF SCOPE → owned by the #828 deterministic-`--session-id` sibling. The `security-sensitive` label is correct and retained (shared-dir file-selection confidentiality guard).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-08

## Open questions

1. **Live-binding wiring test.** The resolver's correctness is fully unit-tested; the late-bound-holder wiring (that `pidFn` reads the *live* PID after `New` returns) is covered structurally by build + the existing pool suite. An end-to-end assertion would require driving `WriteUserTurn` through a fake claude with a known open fd — disproportionate for this ticket. Recommend deferring unless the developer finds a cheap seam (e.g. asserting via the `newProbe` override that `New` with `ClaudeSessionsDir` set wires *a* probe-preferred resolver). Not an AC.
2. **Foreground / `--session-id` sessions are out of scope** — only the bootstrap growth-confirm path (`pool.go:429`) is rewired; `buildSession` keeps its nil-resolver Committed behaviour, unchanged.

## Not in scope

- `reconcileBootstrapOnNew` startup adoption + the deterministic `--session-id` / `--continue` restart-resume identity hazard — sibling ticket (split from #828). That call site runs at pool construction *before* the child spawns (`ChildPID == 0`), so the probe can't disambiguate there; the deterministic-id fix owns it. `mostRecentJSONL` stays in use by `reconcileBootstrapOnNew` — do not delete it.
