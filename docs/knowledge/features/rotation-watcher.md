# Live `/clear` Rotation Watcher

While pyry is running, an fsnotify watcher on the claude sessions dir detects new `<uuid>.jsonl` files in real time. For each CREATE event it probes the tracked claude PID for its currently-open JSONL; on a match it calls `Pool.RotateID(oldID, newID)` so the registry follows claude's `/clear` rotation within ~1 second. Companion to the startup-side reconciler (#38); both drivers use the same single mutation seam.

## Status

- **Phase 1.2b-A (#38):** startup-side reconciliation (cold-start scan).
- **Phase 1.2b-B (#39, this doc):** live detection while claude runs (fsnotify + per-PID FD probe).
- **Phase 1.2c:** idle eviction + lazy respawn — reuses the now-current registry.

## Why

`/clear` rotates claude's session UUID even with `--resume <uuid>`: the original `<uuid>.jsonl` freezes mid-conversation and a fresh `<new-uuid>.jsonl` is opened. Phase 1.2b-A self-heals on the next `pyry` start, but a long-running pyry that doesn't restart would keep serving the stale UUID until then — bad for lazy respawn (1.2c) and any in-flight reads. Live detection closes the gap.

## Package layout

```
internal/sessions/rotation/
  watcher.go            fsnotify lifecycle, event loop, probe orchestration
  probe.go              Probe interface, OpenFile, parseProcFD, parseLsofOutput, noopProbe
  probe_linux.go        //go:build linux  — linuxProbe walks /proc/<pid>/fd
  probe_darwin.go       //go:build darwin — darwinProbe shells out to lsof
  probe_test.go         table-driven parser tests, fixture-based
  watcher_test.go       fake-Probe + temp-dir fsnotify tests
  testdata/lsof_basic.txt   captured `lsof -nP -p <pid> -F fn` fixture
```

## Dependency direction

```
cmd/pyry → internal/sessions → internal/sessions/rotation → fsnotify, stdlib (exec, os)
```

`internal/sessions/rotation` does **not** import `internal/sessions`. The contract is expressed as closures over primitive types on `rotation.Config` (`Snapshot`, `IsAllocated`, `OnRotate`). The closures are wired in `Pool.Run`, so the `SessionID ↔ string` conversion happens once, in one place. This avoids the import cycle that would arise if `rotation` referenced `sessions.SessionID`.

## Symlink-resolved match path

The watcher canonicalises **both sides** of the rotation match. There are two sources of paths and either may carry a symlink:

- **fsnotify event paths and the watched dir** — fsnotify reports `<watchedDir>/<base>` as-watched. If `cfg.Dir` is a symlink (or has a symlinked ancestor), the event side is the symlink form.
- **Platform probe paths** — `lsof` on macOS and `/proc/<pid>/fd` readlinks on Linux usually canonicalise, but not always: `lsof`'s reported path can include unresolved ancestors (e.g. `/var/folders/...` rather than `/private/var/folders/...`) depending on how the file was opened.

The asymmetry is therefore two-sided. The fix canonicalises each side at the point it enters the comparison:

1. **Watched directory — once at construction.** `New` calls `filepath.EvalSymlinks(cfg.Dir)` and stores the result as the unexported `resolvedDir`. `handleCreate` builds `expected := filepath.Join(resolvedDir, base)`. Resolving once avoids per-event syscalls and dodges any race between the event and the resolution (the directory's lifetime spans the watcher's). Startup `EvalSymlinks` failure logs `Warn` and falls back to `cfg.Dir` — watcher remains functional.
2. **Probe-returned path — per event in `handleCreate`.** `filepath.EvalSymlinks(open)` is called once per CREATE just before the equality check. On success: compare the resolved form. On failure (non-existent / dangling target / permission / loop): log at Debug and fall back to `filepath.Clean(open)`. The fallback is correct because a failure most often means the file the probe reported has already been unlinked between the probe and the resolve — a benign mismatch, not a rotation; `continue` falls through to the next ref.

Per-event resolve on the probe side cannot be hoisted to construction time: the file the probe will report doesn't exist when the watcher starts. The cost is one `lstat`-walk per CREATE (microseconds) and the `EvalSymlinks` race window (file unlinked between probe and resolve) is absorbed by the fallback path — the worst case is a missed match, never a panic, never a wrong rotation.

The pre-fix gates rejected silently in two distinct shapes, with the same operator-visible symptom ("session UUID stops updating after `/clear`"):

- **#118 — watched dir is symlink, probe canonical.** macOS `/var → /private/var` is a default symlink, so any watcher built off `t.TempDir()` or a custom HOME under `/var` saw fsnotify's `/var/...` ev.Name not match `lsof`'s `/private/var/...` probe output. Closed by resolving `cfg.Dir` once.
- **#221 — watched dir canonical, probe symlinked.** The inverse case: e2e harness #55 surfaced a probe path under `/var/folders/...` against an `expected` already canonicalised to `/private/var/folders/...`. Closed by resolving the probe path per event with debug-logged fallback.

Locked in by three tests: `TestWatcher_DetectsRotationThroughSymlink` (#118 — symlinked watch dir, canonical probe), `TestWatcher_DetectsRotationProbeReportsSymlinkPath` (#221 — canonical watch dir, symlinked probe), and `TestWatcher_ProbePathUnresolvableNoCrashNoRotate` (#221 — dangling-symlink probe path; no panic, no rotate). All three use explicit `os.Symlink` so they are portable across Linux and macOS regardless of platform tempdir conventions.

## Key types

### `rotation.Config`

```go
type Config struct {
    Dir         string                          // claude sessions dir
    Probe       Probe                           // platform FD probe (required)
    Snapshot    func() []SessionRef             // returns (id, pid) pairs to consider
    IsAllocated func(id string) bool            // pyry-minted UUID skip set
    OnRotate    func(oldID, newID string) error // calls Pool.RotateID under the hood
    Logger      *slog.Logger
}

type SessionRef struct {
    ID  string
    PID int  // 0 = no live child; skip probe
}
```

`IsAllocated` is optional (defaults to `func(string) bool { return false }`). Everything else is required and validated by `New`. A nil `Logger` falls back to `slog.Default()`.

### `rotation.Probe`

```go
type Probe interface {
    OpenJSONL(pid int) (string, error)
}

func DefaultProbe(log *slog.Logger) Probe // platform-dispatched via build tags
```

Returns the absolute path of the first `.jsonl` the PID has open, or `""` if none. `error` is reserved for unrecoverable failures; transient conditions (process gone, permission denied) collapse to `("", nil)` so the watcher silently skips and waits for the next event.

The `noopProbe` (always returns `("", nil)`) is the fallback when a real probe can't be constructed (e.g. `lsof` missing on darwin). The watcher then runs but never confirms a rotation — startup proceeds, no detection on this host.

## Flow

```
user runs /clear
  │
  ▼
claude opens new <new>.jsonl, stops writing to <old>.jsonl
  │
  ▼
inotify (Linux) / kqueue (Darwin) → fsnotify CREATE
  │
  ▼
watcher event loop:
  - filter Op.Has(Create) and ".jsonl" suffix
  - validate uuidStemPattern (36-char canonical UUIDv4)
  - if cfg.IsAllocated(<new>): consume + skip (fresh session, not a rotation)
  - cfg.Snapshot() → [{id, pid}, ...]
  - for each ref with pid > 0:
      if ref.ID == <new>: return  (already rotated by another path)
      open := probeWithRetry(pid)  // 0 / 50ms / 200ms attempts
      // symlink-resolved match — both sides canonicalised (#118, #221)
      if EvalSymlinks(open)(fallback Clean(open)) == Join(resolvedDir, base):
          cfg.OnRotate(ref.ID, <new>)  →  Pool.RotateID(...)
          return
  │
  ▼
sessions.json on disk now points at <new>; <old>.jsonl preserved untouched.
```

## Probe debounce

fsnotify CREATE can fire before claude has fully `open(2)`'d the file. `probeWithRetry` walks `[]time.Duration{0, 50ms, 200ms}` — total worst case 250ms, well inside the AC's "within ~1 second". A probe error or empty result on attempt N continues to N+1; ctx cancel during the sleep aborts the retry. If all three miss, the next CREATE on the same dir won't re-fire — accept it as a rare miss and let Phase 1.2c cover it.

The retry schedule is a package var (`probeRetryDelays`) so tests can override.

## Skip set (pyry-allocated UUIDs)

The skip set lives on `Pool`, not on the watcher:

```go
func (p *Pool) RegisterAllocatedUUID(id SessionID)   // before claude --session-id is launched
func (p *Pool) IsAllocated(id SessionID) bool         // consume-on-first-hit
```

- Entries are consumed on first `IsAllocated` true return so a fresh-session CREATE skips rotation exactly once.
- `allocatedTTL = 30 * time.Second` (package `var`, tests shrink it). Pruned opportunistically on every read/write, so never-materialized entries don't accumulate.
- `Pool.mu` is held (write) for both Register and IsAllocated — same lock contract as `RotateID` and `saveLocked`.

**Phase 1.2b-B has no live caller.** Pyry currently launches claude with `--continue`, so claude picks the UUID and the on-disk JSONL is what the registry follows. The scaffolding lands now so Phase 1.1's `pyry sessions new` + `claude --session-id` is a one-liner: register the UUID before spawn, and the inevitable subsequent CREATE no-ops through the rotation path.

## `Pool.Run` errgroup wrap

```go
func (p *Pool) Run(ctx context.Context) error {
    g, gctx := errgroup.WithContext(ctx)
    g.Go(func() error { return bootstrap.Run(gctx) })

    if dir != "" {
        if w, err := rotation.New(rotation.Config{...}); err == nil {
            g.Go(func() error { return w.Run(gctx) })
        } else {
            p.log.Warn("rotation watcher disabled", "err", err)
        }
    }
    return g.Wait()
}
```

`golang.org/x/sync/errgroup` is the new fan-out primitive. Phase 1.1's N-session fan-out reuses this same wrapper — the extension point is one `g.Go(func() error { return sess.Run(gctx) })` per pool entry. Cancellation propagates both ways: a watcher fatal cancels bootstrap, SIGINT cancels the watcher.

`newProbe = rotation.DefaultProbe` is a package var in `internal/sessions/pool.go`, indirected so tests can inject a fake without touching the build-tagged platform files.

## Probe implementations

### Linux (`probe_linux.go`)

```go
func (linuxProbe) OpenJSONL(pid int) (string, error) {
    entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
    if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
        return "", nil  // pid gone / unreadable — not a probe failure
    }
    // Readlink each entry; first .jsonl-suffixed regular path wins.
}
```

`parseProcFD` is the pure function: returns `target` if it starts with `/`, else `""` (filters out `socket:[...]`, `pipe:[...]`, `anon_inode:[...]`, etc.).

### Darwin (`probe_darwin.go`)

```go
func (darwinProbe) OpenJSONL(pid int) (string, error) {
    out, err := exec.Command("lsof", "-nP", "-p", strconv.Itoa(pid), "-F", "fn").Output()
    // Exit code 1 from lsof = "no matching files" / "process gone" → ("", nil).
    // First .jsonl-suffixed name from parseLsofOutput wins.
}
```

`exec.LookPath("lsof")` is checked at probe construction (in `DefaultProbe`). Missing-lsof returns `noopProbe` and logs a startup warning rather than failing on every event. `parseLsofOutput` walks `lsof -F fn` records (lines prefixed with `f<fd>` and `n<name>`), pairs them, and drops entries whose name doesn't start with `/` (sockets, pipes). Orphan `f` records without a following `n` are dropped silently.

## Concurrency

- **One goroutine per Watcher** (the event loop). Reads `fsw.Events`, `fsw.Errors`, `ctx.Done()`. No shared mutable state inside the watcher itself.
- **Pool.Run owns 2 goroutines** via errgroup — bootstrap supervisor + watcher.
- All `Pool` mutations go through `p.mu` (write): `RotateID`, `RegisterAllocatedUUID`, `IsAllocated`, `saveLocked`. `Snapshot` takes `RLock`.
- The watcher never holds a `Pool` lock across the probe call. The `Snapshot` window is one map iteration; the probe's `lsof` shell-out (~10–50ms) runs lock-free.

## Shutdown

```
ctx cancel
  ├── bootstrap.Run returns: existing supervisor cleanup
  └── watcher loop's <-ctx.Done() arm fires
        ├── defer fsw.Close() (releases inotify/kqueue resources)
        └── return ctx.Err()
errgroup.Wait returns first non-nil error (typically context.Canceled)
```

Net result: same shutdown shape as Phase 1.2b-A, plus one extra goroutine that respects ctx.

## Error handling

| Failure | Behaviour |
|---|---|
| `cfg.Dir == ""` (test default; production fallback) | Watcher not constructed; bootstrap-only Pool.Run. |
| Dir doesn't exist at startup | `MkdirAll(dir, 0700)`. If that fails, `New` returns error → log warn, bootstrap continues. |
| `fsnotify.NewWatcher()` or `fsw.Add(dir)` fails | `New` returns error → log warn, bootstrap continues. |
| CREATE for non-`.jsonl` filename or malformed UUID stem | Skip silently. |
| `IsAllocated(newID)` true | Consume + skip (fresh session, not a rotation). |
| Probe error (`lsof` missing, /proc unreadable) | Log debug, skip this PID, loop continues. |
| `EvalSymlinks` on probe path fails (dangling, gone, permission) | Log debug; fall back to `filepath.Clean(open)`; mismatch path falls through to `continue`. (#221) |
| All probes empty after retry | Skip event, loop continues. |
| `OnRotate` returns error (save failure) | Log warn; loop continues. The in-memory rotation already applied; the next mutation's `saveLocked` will retry persistence. |
| `fsw.Errors` non-fatal error | Log warn, loop continues. |
| ctx cancelled | `defer fsw.Close()`; return `ctx.Err()`. |

The contract: rotation detection failures are **never fatal**. Pyry startup proceeds without a watcher rather than failing if the dependency surface (fsnotify init, lsof PATH, /proc readability) is broken.

## Testing

`probe_test.go` — pure-function tests, no filesystem:

- `TestParseProcFD` — `/path/to/file.jsonl` returned; `socket:[12]`, `pipe:[34]`, `anon_inode:[bpf-prog]`, `[eventfd]`, empty all → `""`.
- `TestParseLsofOutput_FilesAndSockets` — fixture `testdata/lsof_basic.txt`. Asserts only regular-file `Name` values, in lsof's emit order.
- `TestParseLsofOutput_EmptyAfterPID`, `_OrphanFRecord`, `_PathWithSpaces`.

`watcher_test.go` — `t.TempDir()` + fake `Probe`:

- `TestWatcher_DetectsRotation` — write `<new>.jsonl`; assert `OnRotate("old", "<new>")` fires within 1s.
- `TestWatcher_DetectsRotationThroughSymlink` (#118) — watch a symlink whose target is the real sessions dir; probe reports the resolved path; `OnRotate` fires only because `EvalSymlinks` ran in `New`. Portable across platforms via explicit `os.Symlink`.
- `TestWatcher_DetectsRotationProbeReportsSymlinkPath` (#221) — canonical watched dir, probe returns the symlink-form path; `OnRotate` fires only because `EvalSymlinks` runs on the probe output per event. Inverse of the #118 case.
- `TestWatcher_ProbePathUnresolvableNoCrashNoRotate` (#221) — probe returns a path through a dangling symlink (`EvalSymlinks` errors); watcher logs debug, falls back to `filepath.Clean(open)`, the mismatch path takes `continue`, no panic and no `OnRotate`. The 300ms wait covers `probeRetryDelays` (50ms + 200ms) plus slack.
- `TestWatcher_SkipsAllocated` / `_SkipsNonJSONL` / `_SkipsMalformedUUID`.
- `TestWatcher_NoSessionsZeroPID` — pid 0 → probe never called.
- `TestWatcher_ProbePathMismatch` — probe returns wrong path → no rotation.
- `TestWatcher_CreatesMissingDir` — non-existent path → `MkdirAll(0700)`.
- `TestWatcher_ContextCancelExits` — `Watcher.Run` returns `context.Canceled` within 100ms.

`pool_test.go` — extension:

- `TestPool_Snapshot` — `{ID: bootstrap, PID: 0}` pre-Run.
- `TestPool_RegisterAllocatedUUID_Consumed` / `_Expires` (TTL shrunk to 50ms) / `_PrunesOnWrite`.
- `TestPool_Run_NoWatcherWhenDirEmpty` — `ClaudeSessionsDir: ""` → bootstrap-only.
- `TestPool_Run_StartsWatcher` — fake probe injected via the `newProbe` package var; assert `RotateID` fires.

The `watcher_test` does not depend on real `/proc` or real `lsof` — it injects `Probe` directly. The probe parsers are validated separately. Combined coverage is cross-platform without a CI matrix dependency.

`internal/e2e/rotation_test.go` (#120, build tag `e2e`) — `TestE2E_RotationWatcher_DetectsClear` drives a real `pyry` daemon through one `/clear`-shaped JSONL rotation against the **real** platform probe (`/proc/<pid>/fd` on Linux, `lsof` on macOS) using #122's `fakeclaude` test binary as the supervised child. Pre-creates `<initialUUID>.jsonl` so `reconcileBootstrapOnNew` settles before the readiness gate releases, drops a trigger file to make fakeclaude rotate, polls the registry until the bootstrap id changes, asserts UUIDv4 stem + `lastActiveAt` advance + post-sleep stability. Closes the gap the unit-only `TestPool_Run_StartsWatcher` (which uses a `dirProbe` substitute) leaves at the binary boundary. See [e2e-harness § Rotation Watcher Pattern](e2e-harness.md).

## Manual smoke (cross-platform)

1. `pyry` in a workdir with no prior claude session.
2. Type a message; observe `~/.claude/projects/<encoded-cwd>/<A>.jsonl`.
3. Run `/clear` inside claude.
4. Within ~1s, pyry's stderr logs `rotation: detected /clear from=A to=B pid=<pid>`.
5. `cat ~/.pyry/<name>/sessions.json` — bootstrap entry's `id` is now `B`.
6. `<A>.jsonl` is still on disk, untouched.
7. Repeat on the other platform; same result.

## References

- Ticket: [#39](https://github.com/pyrycode/pyrycode/issues/39)
- Spec: [`docs/specs/architecture/39-live-rotation-watcher.md`](../../specs/architecture/39-live-rotation-watcher.md)
- ADR: [`004-fsnotify-for-rotation-detection.md`](../decisions/004-fsnotify-for-rotation-detection.md)
- Sibling startup half: [`jsonl-reconciliation.md`](jsonl-reconciliation.md)
- Sessions surface: [`sessions-package.md`](sessions-package.md), [`sessions-registry.md`](sessions-registry.md)
- Phase plan: [`docs/multi-session.md`](../../multi-session.md), [`docs/plan.md`](../../plan.md)
