# `newProbePreferredTranscriptResolver` (#838, migrated onto `internal/transcript` in #1149)

```go
func newProbePreferredTranscriptResolver(
    dir string, probe rotation.Probe, pidFn func() int, pinnedID func() string,
) func(ctx context.Context) (string, int64, error)
```

The probe-preferred replacement for the `supervisor.Config.ResolveTranscript`
closure wired at `pool.go`'s bootstrap block (`newTranscriptResolver` stays as
the AC5 no-lsof fallback — its other caller, `reconcileBootstrapOnNew`, was
removed in #839; see [jsonl-reconciliation.md](jsonl-reconciliation.md), now
marked retired). Follow-up to #827's turn-stream fix, applied to the *other*
live-child bootstrap consumer of the shared sessions dir: the delivery-confirm
growth baseline. As of #1149 both this resolver and `newTranscriptResolver`
are thin adapters over the `internal/transcript` leaf (#1148) — the dir
canonicalisation, confidentiality guard, by-id/probe selection, and UUID-stem
matcher all live there now; this function composes those primitives in
Family A's own dispatch order and maps every no-result to Family A's
`("", 0, nil)` convention. See [transcript-package.md](transcript-package.md).

- **`!probeUsable(probe)`** (the no-lsof `noopProbe`, detected via the local
  `availabilityReporter{ Available() bool }` interface — a probe that omits
  the method is treated as usable) → delegates to `newTranscriptResolver(dir)`
  wholesale (which itself now calls `transcript.Newest(dir)`). This is the
  *only* returned closure that may emit a non-nil error, because it *is*
  today's newest-by-mtime behaviour (AC5).
- Otherwise, `canonicalDir := transcript.CanonicalDir(dir)` is computed once
  at construction. Per resolve:
  - **Pinned-id path** (#989): `pinnedID()` non-empty and
    `transcript.ValidStem`-valid → `transcript.StatByID(dir, id)`; its error
    maps to `("", 0, nil)` **without falling through to the probe** — the
    `ValidStem` pre-check is the branch selector (a miss on a *valid* stem is
    no-baseline-no-probe; an *empty/invalid* stem is what falls through).
  - **Probe path** (pinned id nil/empty/invalid): `pid := pidFn()`;
    `res, _ := transcript.Probed(dir, canonicalDir, probe, pid)` — the
    confidentiality guard (AC4: probed path must canonicalise to a
    `<uuid>.jsonl` directly inside `canonicalDir`) and every benign no-result
    (`pid <= 0`, empty open, guard reject, vanished-before-stat) all collapse
    inside `transcript.Probed` to `(Result{}, nil)`. The adapter additionally
    **swallows** the one error `Probed` *can* surface (a failing
    `probe.OpenJSONL`) via `res, _ :=` — never propagated, never an mtime
    fallback.
- **The nil-error no-baseline convention is load-bearing** and is the inverse
  of the sibling resolvers in `cmd/pyry` (Family B, #1150 — which *wrap* that
  same `Probed` error because their turn-stream subscriber retries on error):
  a non-nil error here would divert `confirmViaTranscriptGrowth` to the #668
  stochastic Committed-chip fallback — the very heuristic the growth-confirm
  exists to replace. `("", 0, nil)` instead keeps the caller on the growth
  path (deliver, then poll for the daemon's own child's file to appear/grow,
  else loud `ErrTurnNotCommitted`). The neutral `transcript` core surfaces one
  error signal; each adapter maps it to its own convention.
- Drops the sibling's `resolvedOnce`/`sawEmpty` cold/warm tail-offset state —
  this consumer needs the true current byte size every call as a `grew()`
  baseline, never a rewound stream offset.

**Wiring (`pool.go`, `New`):** the resolver needs the bootstrap child's *live*
PID, but `supCfg` is copied by value into `supervisor.New` before the
`Supervisor` (the PID source) exists. `New` declares `var bootstrapSup
*supervisor.Supervisor` above the `ClaudeSessionsDir != ""` block; `pidFn`
closes over it (`bootstrapSup == nil` → `0`, a guard never observed in
practice); `bootstrapSup = sup` is assigned immediately after
`supervisor.New` returns. The only caller of the resolver (`WriteUserTurn`) is
reached from a goroutine created long after `New` returns, so the read
strictly follows the assignment — race-free by goroutine-creation
happens-before, no mutex needed. `probe := newProbe(cfg.Logger)` reuses the
existing factory (the rotation watcher builds its own separate instance;
the probe is a stateless lsof/`/proc` wrapper). Unchanged by #1149 — the
4-arg signature and this wiring are untouched by the adapter migration.

See [codebase/838.md](../codebase/838.md) for the original implementation
writeup, [codebase/1149.md](../codebase/1149.md) for the `internal/transcript`
migration, and [rotation-watcher.md](rotation-watcher.md) for the
`rotation.Probe` interface this resolver shares with the watcher and #827.

---
