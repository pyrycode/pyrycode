# Phase 1.2b-A: Registry Self-Heals to Most-Recent JSONL on Startup

**Ticket:** [#38](https://github.com/pyrycode/pyrycode/issues/38)
**Size:** S (~90 production lines)
**Depends on:** Phase 1.2a (#34) — `internal/sessions` registry, `Pool.saveLocked` seam.

## Context

When a user runs `/clear` in claude, claude rotates the session UUID: it stops writing to `<old-uuid>.jsonl` and starts writing to `<new-uuid>.jsonl`. Pyry's 1.2a registry froze the bootstrap UUID at first cold-start mint; after a `pyry stop`/restart it still points at the pre-clear UUID. Phase 1.2c (lazy respawn) and any operator-visible read would surface the wrong conversation.

This ticket is the **startup-side** reconciliation pass: on `Pool.New`, scan claude's session dir, pick the most-recently-modified `<uuid>.jsonl`, and update the bootstrap entry's ID if it disagrees. Live detection (fsnotify watcher + per-PID FD probe) is a separate ticket; the seam this one establishes — an ID-mutation method on `Pool` — is what live-detection reuses.

Stdlib-only. No new dependencies.

### Verified facts

- **JSONL location:** `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl` (directly in the project dir; **not** in a `sessions/` subdirectory). The ticket body says `<encoded-cwd>/sessions/` — this is wrong on the verified install. The spec follows reality.
- **CWD encoding:** claude maps both `/` and `.` to `-`. Confirmed against existing dirs:
  - `/Users/juhanailmoniemi/Workspace/Projects/.pyrycode-worktrees/architect-38`
  - → `-Users-juhanailmoniemi-Workspace-Projects--pyrycode-worktrees-architect-38`

## Design

### Package layout

All new code lives in `internal/sessions`. No new packages.

```
internal/sessions/
  pool.go             [edit] Config.ClaudeSessionsDir, reconcile call in Pool.New, RotateID
  reconcile.go        [new]  encodeWorkdir, mostRecentJSONL — pure functions
  reconcile_test.go   [new]  pure-function table tests
  pool_test.go        [edit] reconciliation + RotateID tests against t.TempDir()
```

### Key types and signatures

```go
// pool.go — Config gains one field.
type Config struct {
    Bootstrap    SessionConfig
    Logger       *slog.Logger
    RegistryPath string

    // ClaudeSessionsDir is the directory containing claude's <uuid>.jsonl
    // files for this WorkDir. Empty disables reconciliation (test default,
    // and the production fallback when $HOME is unresolvable). Production
    // callers in cmd/pyry resolve this from cfg.Bootstrap.WorkDir.
    ClaudeSessionsDir string
}

// pool.go — new method, the load-bearing seam reused by live-detection (#?).
//
// RotateID atomically replaces the in-memory entry keyed by oldID with one
// keyed by newID, and persists. Updates the bootstrap pointer if oldID is the
// bootstrap. p.mu is held (write) across the whole operation, matching the
// 1.2a saveLocked pattern.
//
// Returns ErrSessionNotFound if oldID is unknown. Returns the save error
// verbatim if persistence fails — caller decides whether to treat that as
// fatal (Pool.New does; live-detection logs and continues with the in-memory
// rotation already applied).
func (p *Pool) RotateID(oldID, newID SessionID) error

// reconcile.go — pure functions, easy to unit-test.

// encodeWorkdir maps a working directory to the path component claude uses
// under ~/.claude/projects/. Replaces '/' and '.' with '-'.
//   "/foo/bar"      -> "-foo-bar"
//   "/foo/.bar"     -> "-foo--bar"
//   ""              -> ""
func encodeWorkdir(workdir string) string

// mostRecentJSONL scans entries for files matching <uuid>.jsonl (36-char
// canonical UUID stem, ".jsonl" suffix) and returns the SessionID of the one
// with the latest ModTime. Returns ("", nil) when no matching entry exists.
// Non-matching filenames are silently skipped. statFn lets tests inject mtimes
// without touching the filesystem; production passes os.Stat.
func mostRecentJSONL(dir string, entries []os.DirEntry, statFn func(string) (os.FileInfo, error)) (SessionID, error)
```

### Data flow

```
sessions.New(cfg)
    │
    ├── loadRegistry(cfg.RegistryPath)        ── existing 1.2a path
    ├── pickBootstrap / mint UUID              ── existing 1.2a path
    ├── supervisor.New                         ── existing 1.2a path
    ├── construct *Pool, install bootstrap     ── existing 1.2a path
    │
    ├── if cfg.RegistryPath != "" && reg == nil:
    │       p.saveLocked()                     ── existing 1.2a cold-start
    │
    └── reconcileBootstrapOnNew(p, cfg)        ── NEW
            │
            ├── if cfg.ClaudeSessionsDir == "": return (no-op)
            ├── os.ReadDir(cfg.ClaudeSessionsDir)
            │       on error (missing/unreadable): log warn, return
            ├── mostRecentJSONL(...)
            │       on empty dir / no match: return
            ├── if mostRecent == bootstrapID:   return (warm reload no-op)
            └── p.RotateID(bootstrapID, mostRecent)
                    on save error: return error (fatal-at-startup)
```

`reconcileBootstrapOnNew` is a small free-standing helper inside `pool.go` (or `reconcile.go`) that owns the orchestration. It is invoked unconditionally at the end of `New`, before the function returns the pool.

### `RotateID` semantics

```go
func (p *Pool) RotateID(oldID, newID SessionID) error {
    p.mu.Lock()
    defer p.mu.Unlock()
    sess, ok := p.sessions[oldID]
    if !ok {
        return ErrSessionNotFound
    }
    if oldID == newID {
        return nil
    }
    sess.id = newID
    sess.lastActiveAt = time.Now().UTC()
    delete(p.sessions, oldID)
    p.sessions[newID] = sess
    if p.bootstrap == oldID {
        p.bootstrap = newID
    }
    return p.saveLocked()
}
```

- `Session.id` is a private field already (`session.go:24`) — mutation under `Pool.mu` matches the existing 1.2a "metadata mutates with `Pool.mu` held (write)" rule.
- `lastActiveAt` is bumped because the JSONL the registry now points at is, by definition, the most-recently-modified one we observed. The created_at of the entry stays — this is the same conversational session conceptually, just with a new UUID after `/clear`. (Open question 1 below.)
- `saveLocked` is the existing 1.2a atomic temp+rename path. No new I/O machinery.
- `oldID == newID` guard makes `RotateID` idempotent for callers that aren't sure whether they need to mutate; reconcile uses an explicit equality check first to avoid an unnecessary lock cycle, but the guard is there for live-detection's benefit.

### `cmd/pyry/main.go` wiring

One field added to the `sessions.Config{...}` literal in `runSupervisor`:

```go
ClaudeSessionsDir: resolveClaudeSessionsDir(*workdir),
```

`resolveClaudeSessionsDir` mirrors `resolveRegistryPath`: returns `~/.claude/projects/<encodeWorkdir(absWorkdir)>/`. If `*workdir` is empty, resolve via `os.Getwd()` (claude's effective cwd). If `os.UserHomeDir()` fails, return "" — reconciliation is disabled, and the existing 1.2a bootstrap behaviour is preserved (the AC's "logs and proceeds" path).

`encodeWorkdir` is unexported; `resolveClaudeSessionsDir` calls it via the `sessions` package. Two options:

1. **Export** `EncodeWorkdir` from `internal/sessions`. Cheap, but exposes claude-CLI-specific encoding from a package that otherwise has no opinion on claude's filesystem layout.
2. **Compute the path inside `sessions`**. Add `func DefaultClaudeSessionsDir(workdir string) string` to the package. Caller passes a workdir; package returns the resolved path. Keeps the encoding internal.

**Choice: option 2.** The encoding is a claude-CLI implementation detail; pinning it inside `internal/sessions` (where the rest of the JSONL-reconciliation logic lives) keeps `cmd/pyry` ignorant of how claude stores files. `DefaultClaudeSessionsDir("")` returns "" so caller doesn't need a guard.

### Concurrency model

- `Pool.New` is single-goroutine — no concurrency concerns at startup.
- `RotateID` acquires `p.mu` (write) for the full critical section: map mutation + bootstrap-pointer update + `saveLocked`. Matches 1.2a's invariant that `saveLocked` is always called with the write lock held.
- Future live-detection goroutine will call `RotateID` from outside `New`. The lock contract is identical, so no design change is needed when that ticket lands.
- No goroutines are spawned by this ticket.

### Error handling

| Failure | Behaviour | Source AC |
|---|---|---|
| `ClaudeSessionsDir` empty | Skip silently (test or production-fallback). | implicit |
| `os.ReadDir` returns `fs.ErrNotExist` or other read error | Log warn, return without rotating. `Pool.New` succeeds. | AC: "session dir missing/unreadable does not fail" |
| Dir is empty or contains no UUID-shaped JSONLs | Return without rotating. | AC |
| Most-recent JSONL UUID == bootstrap UUID | Return without rotating; no write. | AC: "warm reload remains a no-op" |
| Most-recent JSONL UUID != bootstrap | Call `RotateID`. On save failure, return error from `Pool.New` (fatal-at-startup, same posture as 1.2a cold-start save failure). | AC: persisted "via the existing atomic-write path before `Pool.New` returns" |
| Filename present but UUID stem malformed | Skip that file; pick from the rest. | implicit |

The pre-rotation JSONL is **never** deleted or modified. Only the registry pointer moves. Tests assert this explicitly.

### Cold-start non-rotation guarantee

The AC requires that fresh sessions are not mistaken for rotations. Two cases:

1. **Cold start, no JSONLs yet.** `mostRecentJSONL` returns ("", nil). No rotation.
2. **Cold start, claude has already written a JSONL whose UUID differs from pyry's mint.** This is Phase 1.2a's reality: pyry mints `A`, claude writes `<B>.jsonl`. The reconciler detects the mismatch and rotates `A → B`. This is **correct**: the registry should track the actual JSONL.

The AC's wording — "When the registry's bootstrap UUID matches a JSONL pyry just minted ... no rotation is detected" — is a forward-looking guarantee for Phase 1.1+, when pyry will invoke `claude --session-id <uuid>` and the on-disk JSONL UUID will match the registry. The pure-function check (`mostRecent == bootstrap` ⇒ skip) satisfies it by construction. No special-casing required.

This means **on the second pyry restart in 1.2a, a one-time rotation typically occurs** as the registry catches up to the claude-chosen UUID. This is expected and harmless — the operator sees the registry self-heal to the real conversation.

## Testing strategy

### Pure-function tests (`reconcile_test.go`)

Table-driven, no filesystem dependency for the encoder; `t.TempDir()` for the picker.

- `TestEncodeWorkdir` — `/foo/bar`, `/foo/.bar`, root `/`, empty, paths with multiple consecutive dots/slashes. Spot-check against a real entry under `~/.claude/projects/` is documented but not asserted (CI doesn't have the dir).
- `TestMostRecentJSONL_PicksLatestMtime` — three valid JSONLs with stamped mtimes via `os.Chtimes`; assert the correct UUID returns.
- `TestMostRecentJSONL_IgnoresNonJSONL` — `.txt`, `.jsonl.bak`, dirs, malformed UUID stems all skipped.
- `TestMostRecentJSONL_EmptyDir` — returns `("", nil)`.
- `TestMostRecentJSONL_SingleEntry` — deterministic with one file.
- `TestMostRecentJSONL_TieBreak` — equal mtimes: stable result (sort by ID for determinism is fine; document in code).

### `Pool` tests (`pool_test.go`)

- `TestPool_New_Reconciles_RotatesToNewest` — seed registry with bootstrap UUID `A`; populate `ClaudeSessionsDir` with `<B>.jsonl` (newer than `<A>.jsonl`); construct Pool. Assert `Default().ID() == B`, registry on disk now has `B`, the `<A>.jsonl` file still exists untouched.
- `TestPool_New_Reconciles_NoRotationWhenMatch` — seed with `A`, only `<A>.jsonl` on disk. Assert no rewrite (mtime + bytes unchanged).
- `TestPool_New_Reconciles_MissingDir_ProceedsWithBootstrap` — `ClaudeSessionsDir` points at a non-existent path. Assert `New` succeeds, default ID equals seeded UUID, registry unchanged.
- `TestPool_New_Reconciles_EmptyDir_NoOp` — dir exists but empty. Same assertion as above.
- `TestPool_New_Reconciles_ColdStart_PicksNewestImmediately` — cold start (no registry), `<X>.jsonl` already on disk. After `New`, registry's bootstrap entry is `X`, not a freshly-minted UUID. (This exercises the 1.2a→1.2b interaction: cold-start mint followed by reconcile detects the existing JSONL.)
- `TestPool_RotateID_HappyPath` — construct pool, call `RotateID(old, new)`, assert map keys, bootstrap pointer, `lastActiveAt` advanced, registry on disk reflects `new`.
- `TestPool_RotateID_UnknownOldID` — returns `ErrSessionNotFound`, no map mutation, no save call (verified via mtime).
- `TestPool_RotateID_Idempotent` — `RotateID(x, x)` is a no-op (still acquires lock, but no write).

### Race detector

`go test -race ./internal/sessions/...` covers `RotateID`'s lock contract. No new races introduced; the lock surface is identical to 1.2a's `saveLocked` callers.

### Manual smoke (AC's last bullet)

1. Start `pyry` in a fresh workdir.
2. Send a message → claude writes `<A>.jsonl`.
3. Run `/clear` → claude rotates to `<B>.jsonl`.
4. Send another message in the post-clear session.
5. `pyry stop`.
6. `pyry` restart → registry should now report bootstrap UUID `B`. `<A>.jsonl` still on disk, untouched.

## Open questions

1. **`lastActiveAt` on rotation.** Bump to `time.Now()` (this spec's choice) or preserve the pre-rotation value? Bumping is the simpler invariant — "lastActiveAt = when we last observed activity on this session" — and the JSONL's mtime is the activity. Preserving would require carrying the old value, which adds no information. Going with bump.
2. **Logging level for "rotation detected".** Info or Warn? On every restart in 1.2a today this fires once (claude's UUID ≠ pyry's mint). After Phase 1.1+'s `--session-id` wiring, it should be rare. Suggest Info for now with a clear message ("rotated bootstrap session id A → B from on-disk JSONL"); easy to bump later.
3. **`statFn` injection vs raw `os.Stat`.** Spec includes `statFn` as a parameter for testability. If tests can stamp mtime via `os.Chtimes` cleanly, the injection is unnecessary and adds API surface. **Default: keep `mostRecentJSONL` calling `os.Stat` directly; tests use `os.Chtimes` on real tempfiles.** Drop `statFn` from the signature unless a test reveals a need.
4. **Symlinks in the session dir.** `os.ReadDir` returns `DirEntry`; `os.Stat` follows symlinks, `os.Lstat` does not. claude doesn't write symlinks here. Use `os.Stat` (follow); a broken symlink → skip.

## Out of scope (per ticket)

- Live `/clear` detection while claude is running — separate ticket (fsnotify + per-PID FD probe). The `RotateID` seam this ticket establishes is what that work plugs into.
- Idle eviction + lazy respawn (1.2c).
- Reconciling non-bootstrap entries (multi-session, 1.1).
- Garbage-collecting old JSONLs.
