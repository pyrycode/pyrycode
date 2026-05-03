# Spec — Ticket #118: rotation watcher exact-match path comparison must tolerate symlink resolution

## Files to read first

- `internal/sessions/rotation/watcher.go:73-104` — `New` constructor; the natural place to capture the resolved directory once.
- `internal/sessions/rotation/watcher.go:131-176` — `handleCreate`; the comparison gate at line 167 is the single line that needs to change shape.
- `internal/sessions/rotation/watcher_test.go:96-128` — `TestWatcher_DetectsRotation`; the regression test mirrors its skeleton (fake probe + WriteFile + poll for OnRotate).
- `internal/sessions/rotation/probe_darwin.go` (whole file, ~50 lines) — confirm `lsof -F fn` returns canonicalized paths, so resolving the watch dir once at startup matches what the probe emits per call.
- `internal/sessions/pool.go:720-735` — sole caller of `rotation.New`; verifies `Config.Dir` is the only field passed for the watch path (no new field added or consumer call site touched).
- `docs/lessons.md` § "Probing open files cross-platform" and § "Closing a fd to interrupt a goroutine's Read requires O_NONBLOCK" — context for how the probe is wired and the fsnotify race the existing retry loop already handles.
- `docs/lessons.md` § "Claude session storage on disk" — the broader symptom this bug produces (`session UUID stops updating after /clear`) so the developer recognises the failure mode.

## Context

The rotation watcher matches fsnotify CREATE events against the platform probe's report of which JSONL each tracked PID has open. The match gate at `internal/sessions/rotation/watcher.go:167` does a lexical comparison via `filepath.Clean`:

```go
if filepath.Clean(open) != filepath.Clean(fullPath) {
    continue
}
```

`filepath.Clean` normalizes lexically only — it does not resolve symlinks. On macOS, `/var` is a symlink to `/private/var` by default, and `t.TempDir()` lands under `/var/folders/...`. fsnotify reports the path as-watched (`/var/...`); `lsof` returns the canonicalized path (`/private/var/...`). The two strings differ, the comparison fails, the rotation event is silently dropped, and the session UUID stops updating after `/clear`.

This affects any macOS user whose `~/.claude/projects` lives behind a symlink (custom HOME, external-drive home, alternate accounts) and every `t.TempDir`-based test on macOS. Linux is unaffected on default installs, but the same bug shape exists wherever the watched directory crosses a symlink.

Surfaced by ticket #55 (e2e /clear rotation test, run 2 on 2026-05-03 early morning) — the developer agent observed CREATE firing, probe finding the right fd, and the comparison gate at line 167 rejecting a match that was, semantically, correct.

## Design

### Where to fix it

Resolve the watched directory **once at construction time**, store the resolved path on the `Watcher`, and use it to build the comparison path per event. The probe already returns canonical paths (it shells out to `lsof` on macOS and reads `/proc/<pid>/fd` on Linux — both return symlink-resolved targets). Bringing the event side into the same canonical form aligns the two sides at the only point where the mismatch exists.

This is the option the ticket's technical notes flag as cleanest, and it has three properties worth naming:

1. **One syscall per `New`, zero per event.** Per-event `EvalSymlinks` of the probe output would add a syscall to every CREATE; we don't need it.
2. **No race window.** Resolving per-event invites the "file unlinked between event and resolution" failure mode the AC calls out. Resolving the directory once at startup avoids the window entirely — the directory's lifetime spans the watcher's.
3. **No API change.** `Config.Dir` stays as-is; the resolution is an internal detail of the watcher. `pool.go:726` is untouched.

### Concrete shape

Add an unexported field to `Watcher`:

```go
type Watcher struct {
    cfg        Config
    fsw        *fsnotify.Watcher
    resolvedDir string // cfg.Dir with symlinks resolved; falls back to cfg.Dir on resolution failure
}
```

In `New`, after the existing `MkdirAll` + `fsnotify.NewWatcher` + `fsw.Add` succeed, resolve the directory:

```go
resolved, err := filepath.EvalSymlinks(cfg.Dir)
if err != nil {
    cfg.Logger.Warn("rotation: EvalSymlinks failed; using unresolved dir for path comparison",
        "dir", cfg.Dir, "err", err)
    resolved = cfg.Dir
}
return &Watcher{cfg: cfg, fsw: fsw, resolvedDir: resolved}, nil
```

In `handleCreate`, replace the line-167 gate. The current shape compares `filepath.Clean(open)` to `filepath.Clean(fullPath)`. The new shape compares `filepath.Clean(open)` to `filepath.Join(w.resolvedDir, base)` — `base` is already in scope from line 134 (`base := filepath.Base(fullPath)`):

```go
expected := filepath.Join(w.resolvedDir, base)
if filepath.Clean(open) != expected {
    continue
}
```

`filepath.Join` already calls `filepath.Clean` on its result, so no double-Clean is needed on the right side. The left side keeps `filepath.Clean(open)` to defend against a probe returning a path with redundant separators (cheap; same call as today).

### Why fall back to `cfg.Dir` on resolution failure

`EvalSymlinks` can in principle fail even after `MkdirAll` (broken symlink components, permission flake, racing unlink). The watcher should not abort startup over it — it should continue with the unresolved path and accept that the macOS-symlink case will silently drop matches in that specific run, which is no worse than today's behaviour. A `Warn`-level log surfaces it for diagnosis without polluting normal-path logs at higher severity. This satisfies the AC's "falls back gracefully" clause for the per-event resolution case (which this design avoids), and applies the same discipline to the per-startup case.

## Concurrency model

Unchanged. The watcher remains a single event-loop goroutine in `Run`. `resolvedDir` is set in `New` (before the watcher is exposed to any caller) and only read in `handleCreate` from that same goroutine — no synchronisation required. No new goroutines, no new channels, no shutdown sequencing changes.

## Error handling

- `EvalSymlinks` failure at startup: log `Warn`, fall back to `cfg.Dir`. Watcher remains functional; macOS symlink-bridge case will continue to fail (same as today) for this run.
- `EvalSymlinks` is **not** called per event. The AC clause about "file unlinked between event and resolution" is satisfied by design — the race window does not exist in this approach.
- Existing retry loop (`probeWithRetry`) and CREATE-vs-open race handling unchanged.
- `OnRotate` error handling unchanged (logged, squashed, event loop continues).

## Testing strategy

### Regression test (required by AC)

Add `TestWatcher_DetectsRotationThroughSymlink` to `watcher_test.go`. Skeleton mirrors `TestWatcher_DetectsRotation` (line 96), with one structural change: the watch is set up against a symlink to the real directory, and the probe reports the **resolved** path while fsnotify will report the **as-watched** (symlink) path.

Shape:

```go
func TestWatcher_DetectsRotationThroughSymlink(t *testing.T) {
    t.Parallel()
    real, err := filepath.EvalSymlinks(t.TempDir()) // canonicalise to remove macOS /var → /private/var
    if err != nil { t.Fatal(err) }
    link := filepath.Join(t.TempDir(), "linked-sessions")
    if err := os.Symlink(real, link); err != nil { t.Fatal(err) }

    // Probe reports the *resolved* path (mimics what lsof / proc-fd actually return).
    probedPath := filepath.Join(real, newUUID+".jsonl")
    probe := &fakeProbe{pathFn: func() string { return probedPath }}
    rec := &rotateRecord{}

    startWatcher(t, Config{
        Dir:      link,             // watch the symlink
        Probe:    probe,
        Logger:   discardLogger(),
        Snapshot: func() []SessionRef { return []SessionRef{{ID: oldUUID, PID: 1234}} },
        OnRotate: rec.record,
    })

    // Write through the symlinked path so fsnotify reports an as-watched ev.Name.
    if err := os.WriteFile(filepath.Join(link, newUUID+".jsonl"), []byte("x"), 0o600); err != nil {
        t.Fatal(err)
    }
    // poll rec for OnRotate within 1s, same shape as TestWatcher_DetectsRotation.
}
```

Three properties this test locks in:

1. **Fails on current code.** With `filepath.Clean` only, `Clean(real/...) != Clean(link/...)` because `link` is a different absolute path; the gate rejects, OnRotate never fires, the test times out at the 1s deadline.
2. **Passes after the fix.** `EvalSymlinks(link)` in `New` returns `real`; `filepath.Join(real, base) == filepath.Clean(probedPath)`; gate passes; OnRotate fires.
3. **Portable.** Linux runs it through the same code path because the explicit `os.Symlink` produces a non-canonical watch path on every platform — Linux CI exercises the symlink branch even though Linux defaults don't put `t.TempDir` behind a symlink.

### Existing tests

All seven existing tests in `watcher_test.go` use `t.TempDir()` directly as `Config.Dir`. On macOS this means `cfg.Dir` is e.g. `/var/folders/...` and `EvalSymlinks` resolves to `/private/var/folders/...`. Today's tests pass anyway because both `probe.pathFn` and `os.WriteFile` use the same `dir` variable — the strings match lexically without needing resolution. After the fix, the probe returns `dir + "/...jsonl"` (still `/var/folders/...`); `w.resolvedDir` is `/private/var/folders/...`; `filepath.Join(resolvedDir, base)` is `/private/var/.../...jsonl`; `filepath.Clean(open)` is `/var/.../...jsonl`. **These would fail** unless we adjust either the test expectation or the probe's reported path.

The clean fix is to make existing tests use `filepath.EvalSymlinks(t.TempDir())` for both the watch dir *and* the probe-returned path — i.e., feed the watcher a pre-canonicalised path. This mirrors what production callers would experience after a single boot resolution. Touch all six tests that build a `dir` variable from `t.TempDir()`:

```go
dir, err := filepath.EvalSymlinks(t.TempDir())
if err != nil { t.Fatal(err) }
```

This is a mechanical edit at the top of each test that uses `dir`. `TestWatcher_CreatesMissingDir` and `TestWatcher_ContextCancelExits` don't compare paths and need no change.

### Verification commands

```bash
go test -race ./internal/sessions/rotation/...
go vet ./...
```

Confirm both pass on darwin (the platform where the bug actually bites) before commit. Linux CI will exercise the new symlink test through the explicit `os.Symlink`.

## Open questions

None. The technical notes in the ticket already pin the primitive (`filepath.EvalSymlinks`), the fix site (event-side via watcher startup), and the fallback discipline. The existing-test adjustment above is a consequence of the chosen design — not a separate decision to make.

## Out of scope (per ticket)

- The full e2e test (#55) is already in flight under its own ticket.
- Migrating away from exact-match comparison to inode-based identity. Only the symlink mismatch is in scope.

## Size

XS. Production diff: ~8 lines in `internal/sessions/rotation/watcher.go` (one new struct field, ~5 lines in `New` for the resolve + fallback, one rewritten line in `handleCreate`). Test diff: ~30 lines for the new regression test + ~6 mechanical 2-line additions across the existing tests. Two files total. Zero consumer call sites touched.
