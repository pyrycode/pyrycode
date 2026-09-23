# `Pool.BootstrapID` + `supervisor.Config.ResolveSessionID` (#839, resume branch #1164)

```go
func (p *Pool) BootstrapID() SessionID
```

RLock, resolve `p.bootstrap` fresh, RUnlock — the same shape as
`Default()`/`DefaultSettings()`. Deliberately reads `p.bootstrap`, never
`Default().ID()`/`sess.id`: `p.bootstrap` is the `Pool.mu`-guarded value, and
reading it keeps the spawn path off `Session.lcMu` entirely (`sess.id` is
guarded by both `Pool.mu` and `lcMu` as of #866 — see *Concurrency* below —
but there is no reason for this call to take `lcMu` when `Pool.mu` already
gives it a race-clean answer).

**Wiring (`pool.go`, `New`):** the bootstrap `supCfg` sets `ResumeLast: false`
and (as of #1164) a named `resolveID := func() string { return string(p.BootstrapID()) }`
closure feeding `ResolveSessionID: func() (string, bool) { ... }` — see below.
Same late-bind problem as the `bootstrapSup` pattern above — the closure
needs to reference `p`, but `supCfg` is built (and copied by value into
`supervisor.New`) before the `&Pool{...}` literal exists. `New` forward-declares
`var p *Pool` before `supCfg`, then reassigns (`p = &Pool{...}`, not
`p := &Pool{...}`) at the existing construction site. `ResolveSessionID` is
only *called* inside `supervisor.Run`'s spawn loop, on a goroutine started
long after `New` returns, so the read is race-free by the same
goroutine-creation happens-before argument as `bootstrapSup`.

`supervisor.Run` calls the resolver at the start of every spawn (first run
and every restart) and passes both return values into `buildClaudeArgs`.
Since #1164, `ResolveSessionID` returns `(id string, resume bool)`, resolved
in one call so the resume decision always targets the id about to be
spawned: non-empty id with `resume=false` → `--session-id <id>` appended,
`--continue` suppressed (#839's original create path, byte-identical when no
transcript exists yet); non-empty id with `resume=true` → `--resume <id>`
instead (claude 2.1.199 refuses `--session-id` for a transcript that already
exists — a hard daemon restart, or the in-process respawn after the first
spawn created `<id>.jsonl` — so this reattaches instead of crash-looping);
empty id (defensive only, never hit on the bootstrap path) → falls through
to the existing `ResumeLast`/`--continue` logic. `pool.go`'s closure decides
`resume` by probing `transcript.StatByID(cfg.ClaudeSessionsDir, id)` fresh on
every call — exists → `resume=true`; absent, empty id, or
`ClaudeSessionsDir == ""` → `resume=false`. Because the resolver re-reads
`p.bootstrap` on every call, a `/clear` rotation the watcher already
persisted via `RotateID` is picked up automatically on the *next* spawn — no
push notification, no supervisor argv-swap method, no `onRotate` wiring.
This also retires the startup `reconcileBootstrapOnNew` call (deleted from
`New` by #839): with the session flag deterministic from the first spawn,
there is nothing left to adopt-by-mtime at startup. See
[jsonl-reconciliation.md](jsonl-reconciliation.md) (marked retired),
[codebase/839.md](../codebase/839.md), [codebase/1164.md](../codebase/1164.md),
and [ADR 032](../decisions/032-bootstrap-resume-per-spawn-existence-probe.md).

**Growth-confirm resolver re-sourced (#1164), now deleted (#1550).**
`newProbePreferredTranscriptResolver`'s 4th argument (a `func() string` — the
pinned id) used to be fed `supCfg.ResolveSessionID` directly; once the field
returned a 2-tuple, the growth-confirm resolver was re-sourced to the named
`resolveID` closure instead (id-only, unchanged behaviour for that consumer).
\#1550 deleted `newProbePreferredTranscriptResolver` along with
`newTranscriptResolver`, `probeUsable` and `availabilityReporter`; nothing in
`cmd/` or `internal/` still calls `resolveID` for this purpose.
