# Startup JSONL Reconciliation (retired 2026-07-09, #839)

**The startup-side mechanism this doc originally described —
`reconcileBootstrapOnNew`, which scanned claude's per-workdir session
directory at `Pool.New` and adopted the most-recently-modified `<uuid>.jsonl`
as the bootstrap id — was deleted in [#839](../codebase/839.md).** It let a
second, unrelated claude process writing into the same shared
`~/.claude/projects/<encoded-cwd>/` dir redirect which conversation the
daemon resumed after a restart (a confused-deputy isolation gap). The
bootstrap now spawns with an explicit, daemon-owned `--session-id
<bootstrapID>` on every start and restart instead of `--continue` — see
[sessions-package.md § `Pool.BootstrapID`](sessions-package.md) and
[system-overview.md § `supervisor.Config`](../architecture/system-overview.md).

**`/clear` reconciliation still works, unchanged in mechanism** — it just no
longer runs at startup. `Pool.RotateID` (below) is still the seam the live
fsnotify watcher (`rotation-watcher.md`) calls when claude rotates its UUID
on `/clear`; the difference is that `ResolveSessionID` reads the *current*
`p.bootstrap` fresh at every spawn, so a rotation `RotateID` already
persisted is picked up on the next restart automatically — no separate
startup scan needed. `mostRecentJSONL` (below) was deleted in #1149, when
`newTranscriptResolver`'s AC5 no-lsof fallback was rewritten onto
`internal/transcript.Newest` instead.

The rest of this document (path layout, `RotateID` seam, `mostRecentJSONL`
semantics) is preserved as historical/reference material for the parts that
are still true — the **Flow** section's `reconcileBootstrapOnNew` step no
longer exists.

## Status

- **Phase 1.2b-A (#38):** startup-side reconciliation introduced. `Pool.RotateID` established as the load-bearing seam.
- **Phase 1.2b-B (#39, shipped):** live-detection while claude is running (fsnotify watcher + per-PID FD probe). Reuses `RotateID` unchanged. See [`rotation-watcher.md`](rotation-watcher.md).
- **Phase 1.2c:** idle eviction + lazy respawn. Reads the reconciled UUID via the existing registry.
- **#839 (2026-07-09):** startup-side `reconcileBootstrapOnNew` **removed**. Replaced by deterministic `--session-id` spawn resolved from `Pool.BootstrapID()` at every spawn. See [codebase/839.md](../codebase/839.md).

## Why

Verified empirically on 2026-05-02: when a user runs `/clear` in a supervised claude session, claude stops writing to `<old-uuid>.jsonl` and starts writing `<new-uuid>.jsonl` — even with `claude --resume <uuid>`. The original JSONL freezes mid-conversation. Without reconciliation, after `pyry stop` + restart the registry's bootstrap entry still points at the pre-clear UUID, and any operator-visible read (or Phase 1.2c respawn) would surface the wrong conversation.

## Path layout (claude convention)

```
~/.claude/projects/<encoded-cwd>/<uuid>.jsonl
```

Files live **directly** in the encoded-cwd dir — there is **no** `sessions/` subdirectory. (The ticket body claimed otherwise; the spec follows reality.)

`encodeWorkdir` maps the workdir to claude's path component: it replaces both `/` **and** `.` with `-`. A leading `/` becomes a leading `-`, and a hidden `.dir` collapses to `--`:

```
/Users/juhana/Workspace/Projects/.pyrycode-worktrees/architect-38
  → -Users-juhana-Workspace-Projects--pyrycode-worktrees-architect-38
```

Encoding is verified against an existing entry under `~/.claude/projects/`, not inferred.

## Flow (historical — pre-#839)

This diagram describes the retired startup scan; it no longer runs. Current
`Pool.New` does `load registry / mint bootstrap → construct *Pool, install
bootstrap → cold-start save (if reg == nil)` and returns — no reconcile step.

```
Pool.New(cfg)  [pre-#839]
  │
  ├── load registry / mint bootstrap            ── existing 1.2a path
  ├── construct *Pool, install bootstrap        ── existing 1.2a path
  ├── cold-start save (if reg == nil)           ── existing 1.2a path
  │
  └── reconcileBootstrapOnNew(p, cfg.ClaudeSessionsDir, log)   [removed #839]
        │
        ├── ClaudeSessionsDir == ""?            return (test-mode no-op)
        ├── os.ReadDir(dir)                     fs.ErrNotExist → silent return
        │                                       other read err → log warn, return
        ├── mostRecentJSONL(dir)                no UUID-shaped JSONL → return
        ├── mostRecent == bootstrap?            return (warm reload, no write)
        └── p.RotateID(bootstrap, mostRecent)   atomic in-memory swap + saveLocked
```

`reconcileBootstrapOnNew` lived in `internal/sessions/reconcile.go` and was invoked unconditionally at the end of `Pool.New`, before the function returned. Failure of the directory scan was never fatal — startup proceeded with the existing bootstrap entry. Deleted in #839; `mostRecentJSONL` (below) was deleted in #1149.

## `Pool.RotateID` — the seam

```go
func (p *Pool) RotateID(oldID, newID SessionID) error
```

- Acquires `p.mu` (write) for the entire critical section: map mutation, bootstrap-pointer update, and `saveLocked`. Same invariant as 1.2a's mutating callers.
- `RotateID(x, x)` is a no-op (returns nil without writing).
- Returns `ErrSessionNotFound` if `oldID` is unknown — no map mutation, no save.
- Bumps the rotated session's `last_active_at` to `time.Now().UTC()` (the JSONL the registry now points at is, by definition, the most-recently-active one we observed). `created_at` is preserved — conceptually this is the same conversational session continuing under a new claude UUID.
- Updates `p.bootstrap` if `oldID` was the bootstrap.

This is the single mutation point reused by Phase 1.2b-B's live-detection goroutine. The lock contract is identical, so no design change is needed when that ticket lands.

## `mostRecentJSONL` semantics

```go
func mostRecentJSONL(dir string) (SessionID, error)
```

- Filters for files with a `.jsonl` suffix and a 36-char canonical UUIDv4 stem (matched by `uuidStemPattern`). Subdirectories, non-matching names (`.txt`, `.jsonl.bak`, malformed UUIDs), and entries that fail to stat are silently skipped.
- Returns the `SessionID` (the stem) of the entry with the latest `ModTime`.
- Tie-break on equal mtime: lexicographically-larger UUID wins. Deterministic for tests; in practice claude doesn't produce ties at second resolution.
- Empty dir or no matching entries → `("", nil)`. Read error → `("", err)` (caller treats `fs.ErrNotExist` as silent skip).

`os.Stat` (not `os.Lstat`) is used — broken symlinks skip naturally, regular files behave as expected. claude doesn't write symlinks here.

## Pre-rotation JSONL is preserved

The reconciler **only moves the registry pointer**. The pre-clear JSONL file is never deleted, truncated, or modified. Tests assert this explicitly (file mtime + bytes unchanged after rotation).

## Cold-start non-rotation guarantee (historical — pre-#839)

Two cases the AC distinguished, back when this scan ran:

1. **Cold start, no JSONLs yet.** `mostRecentJSONL` returns `("", nil)`. No rotation. Registry holds the freshly-minted UUID.
2. **Cold start, claude already wrote a JSONL whose UUID differs from pyry's mint.** This is Phase 1.2a's reality (pyry mints `A`, claude writes `<B>.jsonl`). The reconciler detects the mismatch and rotates `A → B`. **This is correct** — the registry should track the actual JSONL. Expect one such rotation on the second restart in 1.2a; harmless and logged at Info.

The forward-looking guarantee this section originally flagged — "fresh sessions are not mistaken for rotations, once claude is invoked with an explicit id" — is exactly what #839 delivers: the bootstrap now spawns with `--session-id <bootstrapID>`, so the on-disk UUID always matches the registry's id from the first write, and there's no scan left to (not) short-circuit.

## `ClaudeSessionsDir` wiring in `cmd/pyry` (current)

`runSupervisor` resolves the directory and passes it through `Config.ClaudeSessionsDir`:

```go
ClaudeSessionsDir: resolveClaudeSessionsDir(*workdir),
```

Post-#839, this field no longer gates a startup reconcile — it gates the live
rotation watcher (`rotation-watcher.md`) and the #838 growth-confirm probe
resolver. `resolveClaudeSessionsDir` resolves an empty workdir to the process
cwd (matching claude), `filepath.Abs`'s the result, then calls
`sessions.DefaultClaudeSessionsDir`. If `os.UserHomeDir()` fails, it returns
`""` and both the watcher and the probe resolver are disabled — startup
proceeds with the deterministic `--session-id` bootstrap behaviour
regardless.

`encodeWorkdir` is unexported. The encoding is a claude-CLI implementation detail and stays inside `internal/sessions`; `cmd/pyry` only sees the resolved path.

## Error handling (historical — pre-#839 startup scan)

| Failure | Behaviour |
|---|---|
| `ClaudeSessionsDir` empty (test or `$HOME` unresolvable) | Skip silently. |
| Dir doesn't exist (`fs.ErrNotExist`) | Silent skip — pyry hasn't run claude here yet. |
| Other read error | Log warn, return without rotating. `Pool.New` succeeds. |
| Dir empty or no UUID-shaped JSONLs | Return without rotating. |
| Most-recent UUID == bootstrap UUID | Return without rotating; **no write**. |
| Most-recent UUID != bootstrap | Call `RotateID`. Save failure → fatal-at-startup (same posture as 1.2a). |
| Filename present but UUID stem malformed | Skip that file; pick from the rest. |

## Logging (historical)

The removed startup scan logged rotation events at **Info**:
`reconcile: rotating bootstrap session id from on-disk JSONL` with `from`,
`to`, `dir` fields. No longer emitted — startup no longer rotates. Live
`/clear` rotation via the watcher has its own logging (`rotation-watcher.md`).

## Testing

`reconcile_test.go` (still present) — pure-function tests for the surviving
helpers using `t.TempDir()` and `os.Chtimes`:

- `TestEncodeWorkdir` — `/foo/bar`, `/foo/.bar`, root, empty.
- `TestMostRecentJSONL_PicksLatestMtime` — three valid JSONLs with stamped mtimes.
- `TestMostRecentJSONL_IgnoresNonJSONL` — `.txt`, `.jsonl.bak`, dirs, malformed UUID stems all skipped.
- `TestMostRecentJSONL_EmptyDir`, `_SingleEntry`, `_TieBreakDeterministic`, `_MissingDir`.

The `pool_test.go` `TestPool_New_Reconciles_*` table below described the
now-deleted startup-scan behavior and was removed in #839, replaced by
`pool_bootstrap_sessionid_test.go`'s no-foreign-adoption /
`/clear`-via-`RotateID` / id-stability coverage — see
[codebase/839.md](../codebase/839.md#testing). Historical reference only:

- ~~`TestPool_New_Reconciles_RotatesToNewest`~~ — seed registry with `A`; populate dir with newer `<B>.jsonl`; assert `Default().ID() == B`, registry on disk now has `B`, `<A>.jsonl` untouched.
- ~~`TestPool_New_Reconciles_NoRotationWhenMatch`~~ — only `<A>.jsonl` on disk, registry has `A`; assert no rewrite (mtime + bytes unchanged).
- ~~`TestPool_New_Reconciles_MissingDir_ProceedsWithBootstrap`, `_EmptyDir_NoOp`~~.
- ~~`TestPool_New_Reconciles_ColdStart_PicksNewestImmediately`~~ — cold start with `<X>.jsonl` already present; registry's bootstrap entry is `X`, not a freshly-minted UUID.
- `TestPool_RotateID_HappyPath` (still present) — map keys, bootstrap pointer, `lastActiveAt` advanced, registry on disk reflects new id.
- `TestPool_RotateID_UnknownOldID` — `ErrSessionNotFound`, no map mutation, no save call.
- `TestPool_RotateID_Idempotent` — `RotateID(x, x)` is a no-op.

Race detector covers `RotateID`'s lock contract via `go test -race ./internal/sessions/...`.

## References

- Ticket: [#38](https://github.com/pyrycode/pyrycode/issues/38)
- Spec: [`docs/specs/architecture/38-startup-jsonl-reconciliation.md`](../../specs/architecture/38-startup-jsonl-reconciliation.md)
- Sibling features: [`sessions-package.md`](sessions-package.md), [`sessions-registry.md`](sessions-registry.md)
- **Retirement:** [codebase/839.md](../codebase/839.md) — removed `reconcileBootstrapOnNew`, replaced startup adoption with deterministic `--session-id` spawn resolved via `Pool.BootstrapID()`. Spec: [`docs/specs/architecture/839-deterministic-bootstrap-session-id.md`](../../specs/architecture/839-deterministic-bootstrap-session-id.md).
- Locked phase design: [`docs/multi-session.md`](../../multi-session.md), [`docs/plan.md`](../../plan.md)
