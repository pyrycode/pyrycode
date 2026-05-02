# Phase 1.2b-B: Live `/clear` Rotation Detection (fsnotify + per-PID FD probe)

**Ticket:** [#39](https://github.com/pyrycode/pyrycode/issues/39)
**Size:** M (~150–170 production lines)
**Depends on:** #38 (`Pool.RotateID`, `Config.ClaudeSessionsDir`, `DefaultClaudeSessionsDir`, `encodeWorkdir`).

## Why M, not split

The natural seams produce incoherent slices:

- **Probe-only first / watcher-second.** The probe package has no consumer until the watcher lands; slice 1 ships dead code with tests-for-tests-sake. The probe's contract is only validated when the watcher exercises it, so splitting just delays the integration test that catches design errors.
- **Per-platform split (Linux first, macOS second).** AC requires cross-platform smoke. Landing one platform yields a binary that fails on the other.
- **Skip-set + errgroup first / fsnotify second.** The skip set has no live caller in 1.2b (Phase 1.1's `pyry sessions new` is not landed). A skip-set-only ticket would be pure scaffolding. The errgroup wrap is one method body.

The four files in `internal/sessions/rotation/` (watcher + probe interface + two build-tagged probes) cohere as one logical unit: a probe interface, two implementations, one consumer. Edit fan-out is under 5 call sites (Pool gains methods; `Pool.Run`'s body changes; cmd/pyry already wires `ClaudeSessionsDir`). Line count, not fan-out, is the binding constraint, and it sits inside the M ceiling.

## Context

When a user runs `/clear` inside a supervised claude, claude rotates the session UUID: it stops writing to `<old-uuid>.jsonl` in `~/.claude/projects/<encoded-cwd>/` and starts writing to `<new-uuid>.jsonl`. #38 made the **startup** path self-heal: on `Pool.New`, scan the dir, pick the most-recently-modified JSONL, call `Pool.RotateID`. This ticket is the **live** half: while pyry is running, detect the rotation within ~1 second so lazy respawn (1.2c) and any in-flight reads see the post-clear UUID.

The mechanism is fsnotify on `cfg.ClaudeSessionsDir` plus a per-PID FD probe to confirm which session the new JSONL belongs to. The OS knows definitively which file each claude has open: Linux via `/proc/<pid>/fd/*` symlinks, macOS via `lsof -p <pid>`.

`Pool.RotateID` is reused as-is. The watcher does not introduce a parallel mutation path.

### Verified facts (carried from #38)

- JSONLs live **directly** under `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`. There is **no** `sessions/` subdirectory.
- `encodeWorkdir` replaces both `/` and `.` with `-` (so `/foo/.bar` → `-foo--bar`).
- The pre-rotation JSONL is preserved on disk; only the registry pointer moves.

## Design

### Package layout

```
internal/sessions/rotation/             [new package]
  watcher.go            fsnotify lifecycle, event loop, probe orchestration
  probe.go              Probe interface, parseProcFD, parseLsofOutput, OpenFile, DefaultProbe (fallback)
  probe_linux.go        //go:build linux — linuxProbe walks /proc/<pid>/fd
  probe_darwin.go       //go:build darwin — darwinProbe shells out to lsof
  probe_test.go         table-driven parser tests, fixture-based
  watcher_test.go       fake-Probe + temp-dir fsnotify test
internal/sessions/
  pool.go               [edit] add claudeSessionsDir field, allocated map,
                               RegisterAllocatedUUID, IsAllocated,
                               Snapshot; rewrite Pool.Run body to errgroup
  pool_test.go          [edit] skip-set tests, snapshot test
docs/knowledge/decisions/
  004-fsnotify-for-rotation-detection.md   [new ADR]
```

### Dependency direction

```
cmd/pyry  ──> internal/sessions  ──> internal/sessions/rotation  ──> fsnotify
                                                                 \─> stdlib (exec, os)
```

`internal/sessions/rotation` does NOT import `internal/sessions`. The `Rotator`-like interface is expressed as **closures** on a `rotation.Config`, with primitive types only (`string`, `int`). This avoids the import cycle that would arise if `rotation` imported `sessions.SessionID` while `sessions.Pool.Run` constructed a `rotation.Watcher`. The closures are wired inside `Pool.Run`, so the type-safety conversion (`SessionID` ↔ `string`) happens once, in one place.

### Key types and signatures

```go
// internal/sessions/rotation/watcher.go

package rotation

// Watcher detects /clear-style UUID rotations by combining fsnotify CREATE
// events on the claude session dir with a per-PID probe of each tracked
// process's currently-open JSONL.
type Watcher struct { /* unexported */ }

// SessionRef is one (id, pid) pair the watcher should consider when matching
// a CREATE event. Pid == 0 means no live child — skip and let the next event
// retry.
type SessionRef struct {
    ID  string
    PID int
}

// Config is the watcher's contract with its host. All callbacks are invoked
// from the watcher's single event-loop goroutine; implementations are NOT
// called concurrently from the watcher itself, but MUST be safe to call from
// a goroutine other than the one that constructed them.
type Config struct {
    // Dir is the claude sessions dir to watch (cfg.ClaudeSessionsDir).
    Dir string

    // Probe is the platform-specific FD probe. Required.
    Probe Probe

    // Snapshot returns the (id, pid) pairs to consider on each CREATE event.
    // The watcher snapshots once per event; ordering is not significant.
    Snapshot func() []SessionRef

    // IsAllocated returns true if id was registered via Pool.RegisterAllocatedUUID
    // and not yet expired. Implementations consume the entry on first true return.
    IsAllocated func(id string) bool

    // OnRotate is invoked when a CREATE for newID matches a session whose
    // probe reports it has newID's JSONL open. The watcher logs and continues
    // on error — rotation detection failures are not fatal.
    OnRotate func(oldID, newID string) error

    Logger *slog.Logger
}

// New constructs a Watcher. Returns an error only if fsnotify itself fails
// to initialize. A missing Dir is created (MkdirAll, 0700) before the watch
// is added, so first-run pyry against a fresh workdir works without manual
// setup.
func New(cfg Config) (*Watcher, error)

// Run blocks until ctx is cancelled or the underlying fsnotify watcher
// returns a fatal error. Returns ctx.Err() on clean shutdown.
func (w *Watcher) Run(ctx context.Context) error
```

```go
// internal/sessions/rotation/probe.go

package rotation

// Probe answers "what JSONL does PID currently have open?".
//
// Returns absolute path on success, "" if PID has no JSONL fd open. Returns
// error only for unrecoverable probe failures (e.g. lsof missing on darwin);
// transient conditions like "process exited", "permission denied on one fd"
// are squashed to ("", nil) so the watcher skips and retries on the next
// event. The watcher logs at debug level when a probe returns "".
type Probe interface {
    OpenJSONL(pid int) (string, error)
}

// DefaultProbe returns the platform-appropriate Probe. Implemented in
// probe_linux.go and probe_darwin.go via build tags. On unsupported
// platforms (none in scope), returns a no-op probe that always returns "".
func DefaultProbe() Probe

// OpenFile is one row of lsof's machine-readable output (Darwin).
type OpenFile struct {
    FD   string // e.g. "12u" — see `lsof -F` docs
    Name string // path
}

// parseProcFD interprets a single readlink target from /proc/<pid>/fd/<n>.
// Returns the path if it looks like a regular file path (rooted at "/"),
// otherwise "" (sockets like "socket:[123]", pipes like "pipe:[456]",
// anon_inode entries, etc.). Caller filters by .jsonl suffix.
func parseProcFD(linkTarget string) string

// parseLsofOutput parses `lsof -nP -p <pid> -F fn` output (file/name fields
// only). Records start with 'p<pid>' and contain alternating 'f<fd>' and
// 'n<name>' lines. Returns one OpenFile per (f, n) pair. Sockets and pipes
// are filtered out (their 'n' value does not start with '/'). Order matches
// lsof's output order.
func parseLsofOutput(raw string) []OpenFile
```

### Pool changes

```go
// internal/sessions/pool.go

// allocatedTTL bounds how long a UUID stays in the skip set before being
// pruned. Defined as a package var (not const) so tests can shrink it.
var allocatedTTL = 30 * time.Second

type Pool struct {
    mu                sync.RWMutex
    sessions          map[SessionID]*Session
    bootstrap         SessionID
    log               *slog.Logger
    registryPath      string
    claudeSessionsDir string                  // NEW: retained for Run()
    allocated         map[SessionID]time.Time // NEW: id → deadline
}

// RegisterAllocatedUUID records that id is a UUID pyry just minted (and is
// about to write to disk via claude --session-id). The watcher consults this
// set on every CREATE; matching entries skip the rotation path. Entries are
// consumed on first IsAllocated hit, or pruned after allocatedTTL.
//
// Phase 1.2b-B has no live caller — pyry currently launches claude with
// --continue, so claude picks the UUID. The scaffolding lands now so Phase
// 1.1's `pyry sessions new` and `claude --session-id` wiring is a one-liner.
func (p *Pool) RegisterAllocatedUUID(id SessionID)

// IsAllocated reports whether id is in the freshly-allocated set, consuming
// the entry on a true return. Opportunistically prunes expired entries.
// Safe for concurrent use.
func (p *Pool) IsAllocated(id SessionID) bool

// Snapshot returns one SessionRef per session, capturing the current
// supervisor.State().ChildPID. PID == 0 means no live child. Safe for
// concurrent use; takes RLock only.
func (p *Pool) Snapshot() []rotationSessionRef
```

`rotationSessionRef` cannot reference `rotation.SessionRef` directly (it would create the import cycle). The watcher's `Config.Snapshot` closure does the conversion inline:

```go
Snapshot: func() []rotation.SessionRef {
    snap := p.Snapshot() // returns []sessions.snapshotEntry, primitive fields
    out := make([]rotation.SessionRef, len(snap))
    for i, s := range snap {
        out[i] = rotation.SessionRef{ID: string(s.ID), PID: s.PID}
    }
    return out
},
```

To keep this airtight, expose `Pool.Snapshot` returning a small package-local struct (`type SnapshotEntry struct { ID SessionID; PID int }`). The closure in `Pool.Run` translates to `rotation.SessionRef`.

### `Pool.Run` rewrite

```go
func (p *Pool) Run(ctx context.Context) error {
    p.mu.RLock()
    bootstrap := p.sessions[p.bootstrap]
    dir := p.claudeSessionsDir
    p.mu.RUnlock()

    g, gctx := errgroup.WithContext(ctx)
    g.Go(func() error { return bootstrap.Run(gctx) })

    if dir != "" {
        w, err := rotation.New(rotation.Config{
            Dir:         dir,
            Probe:       rotation.DefaultProbe(),
            Logger:      p.log,
            Snapshot:    func() []rotation.SessionRef { return p.snapshotForRotation() },
            IsAllocated: func(id string) bool { return p.IsAllocated(SessionID(id)) },
            OnRotate:    func(oldID, newID string) error { return p.RotateID(SessionID(oldID), SessionID(newID)) },
        })
        if err != nil {
            // AC: pyry startup proceeds without a watcher rather than failing.
            p.log.Warn("rotation watcher disabled", "err", err)
        } else {
            g.Go(func() error { return w.Run(gctx) })
        }
    }
    return g.Wait()
}
```

`snapshotForRotation` is an unexported helper that calls `p.Snapshot()` and returns `[]rotation.SessionRef`. Its body is the conversion shown above.

`golang.org/x/sync/errgroup` is the recommended fan-out primitive. Phase 1.1's session fan-out reuses this same wrapper. (See "Open question 2" if the developer prefers a stdlib-only ad-hoc coordinator — both work; errgroup is the documented intent.)

### Data flow — live rotation

```
user runs /clear in claude
        │
        ▼
claude opens new <new>.jsonl, stops writing to <old>.jsonl
        │
        ▼
inotify (Linux) / kqueue (Darwin) fires CREATE
        │
        ▼
fsnotify.Watcher delivers event on Events channel
        │
        ▼
watcher event loop:
  - filter Op.Has(Create)
  - extract <new> from filename, validate UUID shape
  - if cfg.IsAllocated(<new>): consume + skip (fresh session, not a rotation)
  - snapshot sessions:  [{id: <old>, pid: 4711}, ...]
  - for each ref with pid > 0:
      open := probe.OpenJSONL(pid)
      if Clean(open) == Clean(ev.Name) and ref.ID != <new>:
          cfg.OnRotate(ref.ID, <new>)  →  Pool.RotateID(...)
          break
        │
        ▼
sessions.json on disk now points at <new>; <old>.jsonl preserved.
```

### Probe debounce

fsnotify's CREATE may fire before claude has fully `open(2)`'d the file (the FD is what `O_CREAT` produces, but sub-millisecond races between event delivery and probe execution are possible in practice). The watcher does a small bounded retry inside the event handler:

```go
delays := []time.Duration{0, 50 * time.Millisecond, 200 * time.Millisecond}
var open string
for _, d := range delays {
    if d > 0 {
        select {
        case <-ctx.Done(): return ctx.Err()
        case <-time.After(d): }
    }
    open, _ = probe.OpenJSONL(pid)
    if open != "" { break }
}
```

Total worst-case latency: 250ms — well inside the AC's "within ~1 second". Three attempts is a compile-time constant; if a probe misses three times, the next CREATE on the same dir (e.g. claude's first message-write `O_APPEND`) won't re-deliver, so we accept that as a rare miss. Phase 1.2c can revisit.

### Concurrency model

- **One goroutine per Watcher** (the event loop). Reads from `fsw.Events`, `fsw.Errors`, `ctx.Done()`. No shared mutable state inside the watcher.
- **Pool.Run owns 2 goroutines** via `errgroup`: bootstrap supervisor + watcher. `errgroup.WithContext` propagates cancellation: if the watcher returns a fatal error (none in normal operation), bootstrap is cancelled too. If bootstrap returns (e.g. SIGINT), the gctx cancels the watcher's `<-ctx.Done()` arm.
- **Lock contract.** All mutations on `Pool` go through existing `Pool.mu` (write):
  - `RegisterAllocatedUUID` — write lock, writes the map.
  - `IsAllocated` — write lock (consumes + prunes).
  - `RotateID` — write lock (existing).
  - `Snapshot` — read lock.
- The watcher never holds a lock across the probe call. The `Snapshot` lock window is short (one map iteration); the probe's `lsof` shell-out (~10–50ms) happens lock-free.

### Shutdown sequence

```
ctx cancel
   │
   ├─> bootstrap.Run returns: supervisor cleanup runs (existing behaviour)
   │
   └─> watcher loop's <-ctx.Done() arm fires
         ├─> defer fsw.Close()  (releases inotify/kqueue resources)
         └─> return ctx.Err()
   │
   ▼
errgroup.Wait returns first non-nil error (typically ctx.Canceled)
Pool.Run returns to cmd/pyry
```

Net result: identical to today's shutdown shape, with one extra goroutine that respects ctx.

### Error handling

| Failure | Behaviour | Source AC |
|---|---|---|
| `cfg.Dir == ""` (test default; production fallback when $HOME unresolvable) | Watcher not constructed; `Pool.Run` runs bootstrap only. | AC: "watcher is disabled — same posture as 1.2b-A" |
| Dir does not exist at startup | `MkdirAll(dir, 0700)`. If MkdirAll fails, log warn, do not construct watcher; pyry startup proceeds. | AC: "creates it ... rather than failing pyry startup" |
| `fsnotify.NewWatcher()` returns error | Log warn, do not construct watcher; bootstrap continues. | AC: "Pyry startup proceeds without a watcher rather than failing" |
| `fsw.Add(dir)` returns error | Log warn, close fsw, return nil from `New`; bootstrap continues. | AC |
| CREATE event for a non-`.jsonl` filename | Skip. | implicit |
| CREATE event with malformed UUID stem | Skip. | implicit |
| `IsAllocated(newID)` true | Skip rotation path entirely (consumed by IsAllocated). | AC: "freshly-allocated UUIDs … skipped" |
| Probe returns error (`lsof` missing, /proc unreadable) | Log debug; skip this PID; loop continues. | AC: "/.../" |
| Probe returns "" for all PIDs after retry | Skip event; loop continues. May happen if /clear was run while no session has the file open (e.g. claude paused). | AC implicit |
| `OnRotate` returns error (save failure) | Log warn; loop continues. The in-memory rotation already applied; persistence will be retried on the next mutation. | spec choice |
| `fsw.Errors` delivers an error | Log warn; loop continues unless context cancels. | implicit |
| ctx cancelled | `defer fsw.Close()`; return ctx.Err(). | AC: "shuts down cleanly" |

### Skip-set semantics

```go
func (p *Pool) RegisterAllocatedUUID(id SessionID) {
    p.mu.Lock()
    defer p.mu.Unlock()
    if p.allocated == nil {
        p.allocated = make(map[SessionID]time.Time)
    }
    p.pruneAllocatedLocked()
    p.allocated[id] = time.Now().Add(allocatedTTL)
}

func (p *Pool) IsAllocated(id SessionID) bool {
    p.mu.Lock()
    defer p.mu.Unlock()
    p.pruneAllocatedLocked()
    deadline, ok := p.allocated[id]
    if !ok || time.Now().After(deadline) {
        delete(p.allocated, id)
        return false
    }
    delete(p.allocated, id) // consume on first hit
    return true
}

func (p *Pool) pruneAllocatedLocked() {
    now := time.Now()
    for id, d := range p.allocated {
        if now.After(d) {
            delete(p.allocated, id)
        }
    }
}
```

- Consume-on-first-hit means a fresh-session CREATE is skipped exactly once. If claude touches the file again as a CREATE (it shouldn't), the second time falls through to the rotation path — which is harmless because the sessions's UUID already equals newID and the watcher's `if ref.ID == newID { continue }` short-circuits before `OnRotate`.
- Pruning on every read keeps the map bounded to the active in-flight allocations, even if some entries are never observed (claude crashed before writing).
- `allocatedTTL` is a package `var` so tests can override (e.g. set to `100*time.Millisecond` in a TTL-expiry test).

### Linux probe implementation

```go
//go:build linux

func (l *linuxProbe) OpenJSONL(pid int) (string, error) {
    fdDir := fmt.Sprintf("/proc/%d/fd", pid)
    entries, err := os.ReadDir(fdDir)
    if err != nil {
        // ESRCH (pid gone) / EACCES → caller skips this pid
        if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
            return "", nil
        }
        return "", err
    }
    for _, e := range entries {
        target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
        if err != nil { continue }
        path := parseProcFD(target)
        if path == "" { continue }
        if strings.HasSuffix(path, ".jsonl") {
            return path, nil
        }
    }
    return "", nil
}
```

`parseProcFD` is the pure function — returns `target` if it starts with `/`, else `""`. Filtering is in the caller for clarity and to keep the parser unit-testable.

### Darwin probe implementation

```go
//go:build darwin

func (d *darwinProbe) OpenJSONL(pid int) (string, error) {
    cmd := exec.Command("lsof", "-nP", "-p", strconv.Itoa(pid), "-F", "fn")
    out, err := cmd.Output()
    if err != nil {
        // exit code 1 from lsof means "no matching files" — not an error
        if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
            return "", nil
        }
        return "", fmt.Errorf("lsof: %w", err)
    }
    for _, f := range parseLsofOutput(string(out)) {
        if strings.HasSuffix(f.Name, ".jsonl") {
            return f.Name, nil
        }
    }
    return "", nil
}
```

`-F fn` produces machine-readable output: lines prefixed with `p` (process), `f` (fd), `n` (name). `parseLsofOutput` walks lines, pairs `f`+`n`, drops non-`/`-rooted names (sockets, pipes, anon).

`exec.LookPath("lsof")` should be checked at probe construction so a missing-lsof is detected at startup, not on the first event:

```go
func darwinNewProbe(log *slog.Logger) Probe {
    if _, err := exec.LookPath("lsof"); err != nil {
        log.Warn("lsof not found; rotation probe disabled", "err", err)
        return noopProbe{}
    }
    return &darwinProbe{}
}
```

`noopProbe.OpenJSONL` returns `("", nil)` always — the watcher then falls through to the "no PID match" path, no rotations are detected on this host. This matches the AC's "watcher is disabled rather than failing pyry startup".

### Filesystem layout assumed

Same as #38: `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`. No `sessions/` subdir. `encodeWorkdir` does the encoding (already correct, replaces `/` and `.` with `-`).

## Testing strategy

### Pure-function tests (`probe_test.go`)

Table-driven, no filesystem access.

- `TestParseProcFD` — `/path/to/file.jsonl` → returns path; `socket:[12]` → ""; `pipe:[34]` → ""; `anon_inode:[bpf-prog]` → ""; `[eventfd]` → ""; "" → "".
- `TestParseLsofOutput_FilesAndSockets` — fixture: real `lsof -nP -p <some-pid> -F fn` output captured from a macOS host (committed in `testdata/lsof_basic.txt`). Asserts only regular-file `Name` values are returned, in the order lsof emitted them.
- `TestParseLsofOutput_EmptyAfterPID` — fixture with only the `p<pid>` line and no `f`/`n` records. Returns empty slice.
- `TestParseLsofOutput_OrphanFRecord` — fixture with `f12u` not followed by `n` (defensive); the orphan is dropped, no panic.
- `TestParseLsofOutput_PathWithSpaces` — `n/Users/x/with space/foo.jsonl` returned verbatim; lsof escapes nothing in `-F` mode beyond null termination, which we don't enable.

### Watcher integration test (`watcher_test.go`)

Uses `t.TempDir()` and a fake `Probe`.

- `TestWatcher_DetectsRotation` — start a Watcher pointing at `t.TempDir()` with fake probe returning the path of whatever file was just created and a Snapshot returning `[{ID: "old-uuid", PID: 1234}]`. `os.WriteFile(<new>.jsonl)` and assert `OnRotate("old-uuid", "<new>")` fires within 1s.
- `TestWatcher_SkipsAllocated` — IsAllocated returns true once for `<new>`; create the file; assert OnRotate never fires.
- `TestWatcher_SkipsNonJSONL` — create `foo.txt`; OnRotate never fires.
- `TestWatcher_SkipsMalformedUUID` — create `not-a-uuid.jsonl`; OnRotate never fires.
- `TestWatcher_NoSessionsZeroPID` — Snapshot returns `[{ID: "x", PID: 0}]`; create file; probe never called; OnRotate never fires.
- `TestWatcher_ProbePathMismatch` — fake probe returns a path that does NOT match the created file (claude has the OLD jsonl open); OnRotate never fires.
- `TestWatcher_CreatesMissingDir` — pass a non-existent path; assert dir is created with 0700.
- `TestWatcher_ContextCancelExits` — cancel ctx; Watcher.Run returns `context.Canceled` within 100ms.

The watcher_test does NOT depend on real `/proc` or real `lsof` — it injects `Probe` directly. The probe_*_test verifies the parsers; the watcher_test verifies the orchestration. Combined, these cover both halves without a cross-platform CI matrix dependency.

### Pool tests (extension to `pool_test.go`)

- `TestPool_Snapshot` — construct a pool with one session, assert `Snapshot()` returns `{ID: bootstrap, PID: 0}` (no Run yet).
- `TestPool_RegisterAllocatedUUID_Consumed` — register `x`, assert `IsAllocated("x") == true`, then `IsAllocated("x") == false` (consumed).
- `TestPool_RegisterAllocatedUUID_Expires` — set `allocatedTTL = 50ms`, register, sleep 100ms, assert `IsAllocated == false`.
- `TestPool_RegisterAllocatedUUID_PrunesOnWrite` — set TTL short, register A, sleep, register B, assert A removed.
- `TestPool_Run_NoWatcherWhenDirEmpty` — set `ClaudeSessionsDir: ""`, Run → returns when ctx cancels with no spurious goroutines (race detector confirms).
- `TestPool_Run_StartsWatcher` — set `ClaudeSessionsDir: t.TempDir()`, run with brief ctx, write a UUID-shaped JSONL during the window, assert `RotateID` was called (verify via reading the registry post-run).

The `Pool.Run` test that exercises the real watcher requires fsnotify, real fs, real probe — but uses a fake probe injected by replacing `rotation.DefaultProbe` via a package-level test hook (`var defaultProbe = ...` in rotation, settable). Or simpler: structure `Pool.Run` so the probe factory is overridable via a test seam, e.g. a package-private `var newProbe = rotation.DefaultProbe` in `internal/sessions/pool.go`.

### Race detector

`go test -race ./internal/sessions/...` covers:
- `Pool.RotateID` lock contract (existing).
- `Pool.RegisterAllocatedUUID` / `IsAllocated` / `Snapshot` concurrency (new).
- Watcher event loop with concurrent Snapshot calls from a separate goroutine in `TestWatcher_DetectsRotation`.

### Manual smoke (AC final bullets)

1. Start pyry in `~/Workspace/scratch/`.
2. Type a message; observe `<A>.jsonl` appear under `~/.claude/projects/-Users-...-scratch/`.
3. Run `/clear` inside claude.
4. Within ~1s, pyry's stderr logs `rotated bootstrap session id A → B from fsnotify event`.
5. `cat ~/.pyry/<name>/sessions.json` shows the bootstrap entry's `id` is now `B`.
6. `<A>.jsonl` is still on disk, untouched.
7. Repeat on Linux; same result.

## ADR

`docs/knowledge/decisions/004-fsnotify-for-rotation-detection.md` — short ADR justifying the fsnotify dependency:

- **Considered:** poll the dir every N seconds (worse latency, worse CPU); raw inotify + kqueue (~150 lines of duplicated platform code); fsnotify (mature, BSD-3, ~5k lines, used by helm/k8s/etcd).
- **Choice:** fsnotify. Latency requirement (~1s) and the cost of maintaining two platform-specific notification stacks tip it.
- **Risk:** First new external dep since `creack/pty`. fsnotify pulls in `golang.org/x/sys` (already transitively present via creack/pty and x/term — verified in `go.mod`).

## Open questions

1. **Probe debounce policy.** Spec includes a 0/50/200ms three-attempt retry inside the event handler. Worst-case latency is 250ms, well inside the AC. If real-world CREATE/open races prove tighter, drop to one attempt. If they prove looser, the next CREATE (e.g. claude's first append) won't re-fire — accept the rare miss and let Phase 1.2c cover it.
2. **errgroup vs ad-hoc coordinator.** AC names errgroup explicitly. Cost: `golang.org/x/sync` (a semi-official extension). Alternative: ~10-line ad-hoc 2-goroutine coordinator using stdlib `sync` + `chan error` — works for this ticket but Phase 1.1's N-session fan-out is cleaner with errgroup. Going with `golang.org/x/sync/errgroup`. If the developer prefers stdlib-only, the spec's contract (errors propagate, ctx cancels both, Wait returns first error) is identical with either implementation.
3. **`allocatedTTL = 30s` rationale.** Long enough that pyry's mint→write→fsnotify-CREATE round trip never expires under normal load (single-digit ms). Short enough that a never-materialized UUID (claude crashed before writing) doesn't accumulate. 30s is the ticket body's suggestion; spec keeps it. Tunable as a package var.
4. **Probe interface granularity.** The spec uses `OpenJSONL(pid) → (path, error)` with a single-result return. Alternative: `OpenFiles(pid) → []OpenFile, error` and let the watcher filter. Single-result is simpler and matches the watcher's actual need ("does this PID have a JSONL open?"). Phase 1.2c (lazy respawn) might want all open files for richer diagnostics — at that point, widen the interface.
5. **Multi-PID disambiguation.** When 1.1 lands and there are N sessions, a CREATE event must match the right one. The spec's "compare probe.OpenJSONL == ev.Name" handles this correctly — only the session whose probe reports the new file gets rotated. The interface is forward-compatible by construction.
6. **Restarting the watcher.** If `fsw.Errors` delivers a fatal error (it never does in practice for a single-dir watch), the spec logs and continues. If the dir is renamed/deleted out from under us, fsnotify stops delivering events; we accept this as out-of-scope for Phase 1.2 and will revisit in 1.2c if it surfaces.

## Out of scope

- Idle eviction + lazy respawn (1.2c).
- Reconciling non-bootstrap entries (1.1).
- Disabling or mediating `/clear` itself.
- Garbage-collecting old JSONLs.
- Windows / WSL specifics (Pyrycode is Linux + macOS).
