---
ticket: 221
size: XS
---

# 221 — watcher: resolve symlinks on macOS for fsnotify path compare

## Files to read first

- `internal/sessions/rotation/watcher.go:78-115` — `New`: where `resolvedDir` is captured (one side of the comparison is already canonicalised).
- `internal/sessions/rotation/watcher.go:142-188` — `handleCreate`: the per-event match; the only line changing is the comparison gate at line 179.
- `internal/sessions/rotation/watcher_test.go:1-95` — test scaffolding (`fakeProbe`, `rotateRecord`, `startWatcher`, `newUUID`/`oldUUID`).
- `internal/sessions/rotation/watcher_test.go:347-392` — `TestWatcher_DetectsRotationThroughSymlink`: exact mirror image of the new "watched dir is symlinked → probe canonical" case; the new test inverts the symlink direction.
- `internal/sessions/rotation/watcher_test.go:259-289` — `TestWatcher_ProbePathMismatch`: shape to follow for the "non-resolvable / mismatch" negative test.
- `internal/sessions/rotation/probe.go:18-30` — `Probe` interface (so the fix doesn't accidentally re-shape it).

## Context

The rotation watcher uses fsnotify CREATE + a per-PID `lsof`-style probe to detect `/clear`-style UUID rotations. The match gate at `internal/sessions/rotation/watcher.go:179` compares the probe-returned path against `expected = filepath.Join(w.resolvedDir, base)`.

`resolvedDir` is canonicalised once at construction time (`watcher.go:108-113`, `filepath.EvalSymlinks` with fallback to the unresolved dir). The probe's return value, however, is whatever the kernel/`lsof` reports, which on macOS can be the symlink form (`/var/folders/...`) when the canonical form is `/private/var/folders/...`. The comparison then mismatches and the rotation event is silently dropped.

This is one-sided asymmetry: `expected` is already canonical, so canonicalising the probe-returned path before equality is sufficient. No need to resolve `expected` per-event.

The mirror case (watched dir is a symlink, probe reports canonical) is already covered by `TestWatcher_DetectsRotationThroughSymlink` and works because `New` resolves `cfg.Dir`. This ticket closes the inverted case.

## Design

### Production change (`internal/sessions/rotation/watcher.go`, ~5 lines)

In `handleCreate`, replace the single comparison line at `watcher.go:179` with:

```go
expected := filepath.Join(w.resolvedDir, base)
openResolved, resolveErr := filepath.EvalSymlinks(open)
if resolveErr != nil {
    w.cfg.Logger.Debug("rotation: EvalSymlinks on probe path failed; falling back to literal",
        "path", open, "err", resolveErr)
    openResolved = filepath.Clean(open)
}
if openResolved != expected {
    continue
}
```

Notes:

- The fallback path (`filepath.Clean(open)`) preserves existing behaviour when `EvalSymlinks` fails, including the dangling-symlink / non-resolvable case the AC calls out. The `continue` then falls through naturally — no panic, no crash, no silent drop into `OnRotate`.
- `EvalSymlinks` returns `*PathError` for non-existent / dangling targets. We treat that as a benign mismatch: the file the probe reports doesn't resolve to anything under our watched dir, so it can't be the rotation target.
- Debug-level logging is intentional: probe-path resolution failures are expected on race-y CREATE events (the file may have been unlinked by claude between the probe and the resolve) and should not warn-spam the logs.
- `resolvedDir` is left untouched. The asymmetry fix is one-sided by design — the per-event cost of resolving `open` (a single `lstat` walk) is acceptable; re-resolving the watched dir each event would be redundant.

### No interface, type, or signature changes

- `Probe`, `Config`, `Watcher`, `SessionRef` are unchanged.
- `New` is unchanged — `resolvedDir` capture remains correct.
- `Run` is unchanged.
- No new exports, no new files, no consumer call-site updates.

## Concurrency model

Unchanged. The fix lives entirely inside `handleCreate`, which runs on the watcher's single event-loop goroutine (`Run`'s `for { select … }` body). `filepath.EvalSymlinks` is a stdlib filesystem call with no shared state, safe to invoke from this goroutine.

## Error handling

Two failure modes for the probe-path resolve:

| Case | Behaviour |
|---|---|
| `EvalSymlinks` succeeds | Compare resolved form to `expected`. Match → `OnRotate`. Mismatch → `continue` to next ref. |
| `EvalSymlinks` fails (non-existent, dangling, permission, loop) | Log at Debug. Compare `filepath.Clean(open)` to `expected`. Mismatch → `continue` (the realistic outcome — if the probe reported a path under a different volume, it can't be the rotation target). |

Neither path crashes, neither path drops the *fsnotify* event itself — the for-loop continues iterating refs, and subsequent CREATE events still feed `handleCreate` normally.

## Testing strategy

Add two new tests to `internal/sessions/rotation/watcher_test.go`. Both follow existing scaffolding (`startWatcher`, `fakeProbe`, `rotateRecord`, `newUUID`/`oldUUID` constants).

### Test 1 — `TestWatcher_DetectsRotationProbeReportsSymlinkPath`

The case the AC primarily targets: watched dir is canonical, probe returns the symlink form.

Shape:

1. `realDir := filepath.EvalSymlinks(t.TempDir())` — canonical dir.
2. `link := filepath.Join(t.TempDir(), "linked-sessions"); os.Symlink(realDir, link)` — a symlink that resolves to `realDir`.
3. Construct watcher with `Dir: realDir` (canonical, so `resolvedDir == realDir`).
4. `probe.pathFn` returns `filepath.Join(link, newUUID+".jsonl")` — the symlink-form path.
5. Trigger CREATE by writing the file under `realDir`.
6. Assert `OnRotate` is invoked with `(oldUUID, newUUID)` within 1 second (mirror the existing 1s/20ms poll loop in `TestWatcher_DetectsRotationThroughSymlink`).

This is the inverse of the existing `TestWatcher_DetectsRotationThroughSymlink`: that test exercises symlinked-watch-dir + canonical-probe; this one exercises canonical-watch-dir + symlinked-probe.

### Test 2 — `TestWatcher_ProbePathUnresolvableNoCrashNoRotate`

The negative-path AC: probe returns a path whose symlinks can't be resolved (e.g. a dangling symlink under a sibling temp dir). Verify:

1. The watcher does not panic.
2. `OnRotate` is **not** invoked.
3. (Optional) Verify the event-loop is still alive by triggering a second CREATE that *does* match and observing `OnRotate` for it. Skip this if it adds noise — the panic-free + no-rotate assertion is the load-bearing one.

Shape sketch:

```go
func TestWatcher_ProbePathUnresolvableNoCrashNoRotate(t *testing.T) {
    t.Parallel()
    dir, err := filepath.EvalSymlinks(t.TempDir())
    if err != nil { t.Fatal(err) }

    danglingTarget := filepath.Join(t.TempDir(), "does-not-exist")
    dangling := filepath.Join(t.TempDir(), "dangling-link")
    if err := os.Symlink(danglingTarget, dangling); err != nil { t.Fatal(err) }

    probe := &fakeProbe{pathFn: func() string {
        return filepath.Join(dangling, newUUID+".jsonl")
    }}
    rec := &rotateRecord{}

    startWatcher(t, Config{
        Dir: dir, Probe: probe, Logger: discardLogger(),
        Snapshot: func() []SessionRef { return []SessionRef{{ID: oldUUID, PID: 1234}} },
        OnRotate: rec.record,
    })

    if err := os.WriteFile(filepath.Join(dir, newUUID+".jsonl"), []byte("x"), 0o600); err != nil {
        t.Fatal(err)
    }

    // Give the event loop time to process & probe-retry, then assert no
    // rotate fired. The 300ms wait covers the bounded probeRetryDelays
    // schedule (50ms + 200ms = 250ms) plus slack.
    time.Sleep(300 * time.Millisecond)
    if calls := rec.snapshot(); len(calls) != 0 {
        t.Fatalf("OnRotate fired %d times for unresolvable probe path; want 0", len(calls))
    }
}
```

The polling/sleep shape is consistent with `TestWatcher_ProbePathMismatch` at `watcher_test.go:259-289`. Use the existing `discardLogger()` helper — debug-level resolve-error logs are expected and should be silenced from test output.

### Existing tests to verify still pass

- `TestWatcher_DetectsRotation` — canonical/canonical path; unaffected.
- `TestWatcher_DetectsRotationThroughSymlink` — symlinked-dir/canonical-probe; the watched-dir resolution still happens in `New`, so this keeps working without change.
- `TestWatcher_ProbePathMismatch` — genuine mismatch (different filename); `EvalSymlinks` will succeed and the `!=` check will still reject it.

## Open questions

None. The asymmetry analysis is in the ticket body; the fix shape is a textbook canonicalise-both-sides; the fallback contract is explicit in the AC.

## Out of scope

- Re-evaluating `resolvedDir` on a directory rename / re-symlink during runtime. Watcher lifetime is process-scoped; the canonical dir is captured at `New` and that's sufficient for the supervisor's use.
- Switching the watch target itself to the canonical path (currently `cfg.Dir`, which may be a symlink). fsnotify works correctly through symlinks on macOS for our use case (CREATE events on the underlying inode), and the existing `TestWatcher_DetectsRotationThroughSymlink` proves it.
