# Spec — #509: `agentrun/jsonl/tail` delegates path + file-appear wait to tuidriver

Confirms PO's size: **S**. One production file, one test file, a mechanical `go.mod` / `go.sum` bump.

## Files to read first

- `internal/agentrun/jsonl/tail/watcher.go` (full file, 295 LOC) — the only production file in scope. Lines to internalise:
  - 27–31 — `probeRetryDelays` schedule (`[]time.Duration{0, 50ms, 200ms}`). Goes away.
  - 36–65 — `Config` (keep unchanged). The `Workdir` doc-comment at 37–40 currently names `agentrun.EncodeProjectDir`; reword to mention the encoding without naming the helper.
  - 89–139 — `New`. Lines 106–113 (home resolution) stay; lines 115–122 are the surface to change (drop `agentrun.EncodeProjectDir`, derive path via `tuidriver.SessionJSONLPath`, keep `MkdirAll` and `fsnotify.Add(dir)`).
  - 146–203 — `Run`. Lines 154–165 (initial stat) and the CREATE-branch handling at 179–186 collapse into a single up-front `tuidriver.WaitForSessionJSONL` call; the WRITE pump at 187–195 stays verbatim.
  - 205–236 — `openAndDrain`. Drops the bounded-retry call; the `errors.Is(err, fs.ErrNotExist)` log-and-return-false branch goes away because `WaitForSessionJSONL` has already guaranteed appearance.
  - 270–294 — `openWithRetry`. Deleted.
- `internal/agentrun/jsonl/tail/watcher_test.go` (full file, 392 LOC) — test seam.
  - 90–99 — `expectedEncodedDir` helper; its body becomes `filepath.Dir(tuidriver.SessionJSONLPath(home, resolved, sessionID))`. (Test-only; `agentrun.EncodeProjectDir` continues to exist for other callers.)
  - 209–250 — `TestWatcher_LateCreate`; stays green unchanged. The wait loop now polls at 50 ms (DefaultPollInterval) instead of stepped 0/50/200; `writeLineByLine` already delays 20 ms between lines, so the first appearance is detected within one tick.
  - 254–299 — `TestWatcher_ExistingFile`; stays green unchanged. `WaitForSessionJSONL` short-circuits on the up-front stat.
  - 370–391 — `TestWatcher_ContextCancellation`; stays green. The wrap now produces `fmt.Errorf("... %w", context.Cause(ctx))`; `errors.Is(err, context.Canceled)` still matches through the wrap.
- `github.com/pyrycode/tui-driver/pkg/tuidriver/jsonl.go` (in module cache after the `go get`, full file) — confirms exact signatures and semantics:
  - `func SessionJSONLPath(home, cwd, sessionID string) string` — pure join; calls `EncodeCwd(cwd)` internally; does NOT resolve symlinks.
  - `func WaitForSessionJSONL(ctx context.Context, path string) error` — initial `os.Stat`; on `IsNotExist`, polls at `DefaultPollInterval` (50 ms); on cancel/deadline returns `fmt.Errorf("... %w", context.Cause(ctx))`; on any non-NotExist stat error, returns wrapped without polling.
  - `DefaultPollInterval = 50 * time.Millisecond`.
- `github.com/pyrycode/tui-driver/pkg/tuidriver/cwd.go` (in module cache) — `EncodeCwd` rule (every non-`[a-zA-Z0-9]` byte → `-`, no run-collapse). Don't duplicate its tests.
- `internal/agentrun/workdir.go` (full file, 44 LOC) — confirms `agentrun.ResolveWorkdir` is the resolver we still need. `EncodeProjectDir` itself is no longer called by the watcher, but `ResolveWorkdir` is, so the package import remains.
- `docs/lessons.md` § "Claude session storage on disk" (lines 50–66) and § "fsnotify reports as-watched, kernel probes report canonicalised" (lines 219–223) — the on-disk layout and the realpath-before-encode invariant. The realpath step is non-negotiable on macOS: claude resolves `/var → /private/var` before encoding; pyry must match or the JSONL tail watches the wrong directory.

No QMD search required — the encoding rule and the layout are pinned in code (tuidriver) and in lessons.md.

## Context

`internal/agentrun/jsonl/tail/watcher.go` today reconstructs `~/.claude/projects/<encoded>/<sid>.jsonl` locally (lines 106–122) and absorbs the "fsnotify CREATE fires before `open(2)` succeeds" race via a hand-rolled `openWithRetry` walking `probeRetryDelays = {0, 50ms, 200ms}` (lines 270–294). Both responsibilities are now owned by the tui-driver library:

- `tuidriver.SessionJSONLPath(home, cwd, sessionID)` returns the joined path using the canonical encoder.
- `tuidriver.WaitForSessionJSONL(ctx, path)` polls at `DefaultPollInterval` (50 ms) until the file appears, cancellation fires, or a non-NotExist stat error short-circuits.

Both shipped in tui-driver #58 (commit `a2edf4f`, 2026-05-22). `go.mod` currently pins `tui-driver v0.0.0-20260519122208-b09fe70e60a7` (2026-05-19), which predates #58 — step zero is a `go get` bump.

This ticket is the first migration step onto the new library surface. The fsnotify-based WRITE pump stays in place; a later ticket (#512) replaces the per-line read loop with `tuidriver.TailJSONL` and deletes this package.

## Design

### `go.mod` / `go.sum` bump (step zero)

Run, from the repo root:

```bash
go get github.com/pyrycode/tui-driver@latest
go mod tidy
```

The latest commit (`e0d558ebdbe0`, 2026-05-23) includes #58 — confirmed by inspection of `pkg/tuidriver/jsonl.go` in the module cache. AC #1 requires the pinned version to be ≥ `a2edf4f` (2026-05-22); `e0d558ebdbe0` is later. If a newer version exists at developer-run time, that's still fine — `@latest` is the right call.

### `internal/agentrun/jsonl/tail/watcher.go`

**Imports.** Add `github.com/pyrycode/tui-driver/pkg/tuidriver`. Keep `github.com/pyrycode/pyrycode/internal/agentrun` (we still call `ResolveWorkdir`). Drop `io/fs` and `time` only if they end up unused after the deletions — `time` is still used by nothing in the new file (no `time.After`, no `time.Sleep`), so it goes; `io/fs` was used only by the deleted `fs.ErrNotExist` branch, so it goes too. Keep `errors`, `fmt`, `io`, `log/slog`, `os`, `path/filepath`, `github.com/fsnotify/fsnotify`, `github.com/pyrycode/pyrycode/internal/agentrun/jsonl`.

**`probeRetryDelays`** (lines 27–31): delete.

**`Config.Workdir` doc comment** (lines 37–40): reword to drop the `agentrun.EncodeProjectDir` reference. One sentence, e.g. *"The watcher resolves Workdir and encodes it the same way claude does to locate the per-session JSONL under `~/.claude/projects/`."*

**`New` (lines 89–139).** Behavior change is local:

```
1. Validate cfg (unchanged).
2. Resolve home from cfg.HomeDir or os.UserHomeDir (unchanged).
3. resolved, err := agentrun.ResolveWorkdir(cfg.Workdir)
   - wrap as "tail: resolve workdir: %w"
4. expectedPath := tuidriver.SessionJSONLPath(home, resolved, cfg.SessionID)
5. dir := filepath.Dir(expectedPath)
6. os.MkdirAll(dir, 0o700) — unchanged rationale (belt-and-suspenders).
7. fsnotify.NewWatcher + fsw.Add(dir) — unchanged.
8. return &Watcher{ ..., dir: dir, expectedPath: expectedPath }
```

Why we still call `ResolveWorkdir`: `tuidriver.SessionJSONLPath` does NOT resolve symlinks (it's a pure join). Claude *does* resolve before encoding (empirically pinned by `TestEncodeProjectDir_DarwinRealpath` and lessons.md § "fsnotify reports as-watched, kernel probes report canonicalised"). On macOS, a `t.TempDir()` workdir under `/var/folders/...` must encode as `-private-var-folders-...`, not `-var-folders-...`. The resolution step is non-negotiable; it just moves up one call.

(Defensive footnote — if sibling #508 lands first and deletes `agentrun.ResolveWorkdir`, the developer inlines `filepath.Abs` + `filepath.EvalSymlinks` here with the same `fmt.Errorf("tail: resolve workdir: %w", err)` wrapping. Both directions are textual; no design change.)

**`Run` (lines 146–203).** Replace lines 154–165 (initial-stat + bounded-retry block) AND the in-loop CREATE branch (lines 179–186) with one up-front wait:

```
defer fsw.Close + file close (unchanged)

err := tuidriver.WaitForSessionJSONL(ctx, w.expectedPath)
if err != nil { return err }

if done, err := w.openAndDrain(ctx); err != nil { return err } else if done { return nil }

for {
  select on ctx.Done | fsw.Events | fsw.Errors
  events: only the WRITE path remains; drop the CREATE-when-file==nil branch
}
```

The fsnotify watcher is armed in `New` (unchanged), so any WRITE events that arrive *during* `WaitForSessionJSONL` are queued in `fsw.Events`. After `openAndDrain` finishes the initial drain, the event loop drains the queue. Draining the reader when it's already at EOF is a no-op — safe.

**`openAndDrain` (lines 205–236).** Simplifies: `WaitForSessionJSONL` returning nil guarantees the file exists *right now*, so the bounded retry is gone. Body becomes:

```
f, err := os.Open(w.expectedPath)
if err != nil { return false, fmt.Errorf("tail: open %s: %w", w.expectedPath, err) }
optional Seek(StartOffset) (unchanged)
w.file = f; w.reader = jsonl.NewReader(...)  (unchanged)
return w.drain()
```

Remove the `fs.ErrNotExist` log-and-continue branch — that case is no longer reachable. A TOCTOU window between `WaitForSessionJSONL` returning nil and `os.Open` succeeding is theoretical and one-shot; if `os.Open` errors, surface it as a wrapped error and let the caller decide. (Claude is append-only during a turn; it does not delete-then-recreate.)

**Event-loop body (lines 167–203).** Strip the CREATE branch. The remaining shape:

```
for {
  select {
  case <-ctx.Done(): return ctx.Err()
  case ev, ok := <-w.fsw.Events:
    if !ok { return nil }
    if ev.Name != w.expectedPath { continue }
    if ev.Op.Has(fsnotify.Write) || ev.Op.Has(fsnotify.Create) {
      done, err := w.drain()
      if err != nil { return err }
      if done { return nil }
    }
  case err, ok := <-w.fsw.Errors: (unchanged)
  }
}
```

Why keep the `Create` half of the `Op.Has` check: a defensive belt against the rare "CREATE arrives after WaitForSessionJSONL returned (e.g. test wrote the file synchronously and the queued CREATE event lands after the loop starts)" case — calling `w.drain()` on a reader already at EOF is a no-op and costs nothing.

**`openWithRetry` (lines 270–294).** Delete entire function.

**`Offset()` (lines 263–268).** Unchanged.

### `internal/agentrun/jsonl/tail/watcher_test.go`

**Imports.** Add `github.com/pyrycode/tui-driver/pkg/tuidriver`. Keep `github.com/pyrycode/pyrycode/internal/agentrun` (still calling `ResolveWorkdir` in the helper).

**`expectedEncodedDir` helper (lines 90–99).** Update to match the production code path so the test and the watcher agree on the directory:

```
resolved, err := agentrun.ResolveWorkdir(workdir)
if err != nil { t.Fatalf(...) }
return filepath.Dir(tuidriver.SessionJSONLPath(home, resolved, sessionID))
```

Update its signature to take `sessionID` (current signature drops it). Two call sites (lines 197 and 319) — adjust both to pass `testSessionID`. The other call sites (`TestWatcher_ExistingFile` at line 260, the `agentrun.EncodeProjectDir` call inside that test) get folded into the helper too — replace lines 260–268 in `TestWatcher_ExistingFile` with a single call to `expectedEncodedDir(t, home, workdir, testSessionID)` + `os.MkdirAll(dir, 0o700)`. After this change there is exactly one place in the test file that knows how to compose the encoded dir, mirroring the production code's single source of truth.

**`TestWatcher_LateCreate`, `TestWatcher_ExistingFile`, `TestWatcher_FixtureIntegration`** — stay green as-is (modulo the helper-signature change). The polling cadence is now 50 ms throughout instead of stepped 0/50/200, but the inter-write delays (`20ms` in `writeLineByLine`, `3ms` in fixture replay) leave ample headroom against the 2s/3s/5s `waitForEndOfTurn` deadlines.

**`TestWatcher_ContextCancellation`** — stays green. The error returned by `Run` is now `WaitForSessionJSONL`'s wrap: `fmt.Errorf("session jsonl ... %w", context.Cause(ctx))`. The assertion `errors.Is(err, context.Canceled)` matches through the `%w` wrap. No code change required.

**New test — `TestWatcher_WaitTimeout`.** Verifies AC #4's "tuidriver wait-error path (timeout)". Scenario (bullets, not full code — write in the project's table-driven idiom):

- `workdir := t.TempDir()`, `home := t.TempDir()`. The JSONL file is never created.
- Build a `Config` matching the other tests, but instead of `startWatcher` (which uses `context.Background`), construct the watcher manually and call `Run(ctxTimeout)` with `context.WithTimeout(context.Background(), 150 * time.Millisecond)`.
- Assert `Run` returns within ~500 ms of the deadline.
- Assert `errors.Is(err, context.DeadlineExceeded)` is true.

Use a tight deadline (≈150 ms) so the test stays fast but is robust against the 50 ms poll interval (3× headroom).

**New test — `TestWatcher_SpecialCharWorkdir`.** Verifies AC #4's "path correctness across special-char cwds (spot-check at the seam)". Scenario:

- `parent := t.TempDir()`; create a sub-dir literal `"a_b c.d"` under it (`os.MkdirAll(filepath.Join(parent, "a_b c.d"), 0o755)`).
- `home := t.TempDir()`.
- Construct the watcher (no `Run`) with `Workdir = filepath.Join(parent, "a_b c.d")`.
- Assert `filepath.Base(w.dir)` ends with `"-a-b-c-d"` (per-byte encoding of `a_b c.d` is `a-b-c-d`; the leading `-` is the encoded `/` joining `parent` to the literal). Use `strings.HasSuffix` so the test is independent of the `t.TempDir()` prefix.
- Assert the encoded dir exists on disk (`os.Stat` returns nil, `IsDir()` true) — confirms `MkdirAll` was called with the right path.

We deliberately do not exhaustively re-test the byte rule — tuidriver's own `cwd_test.go` table has the canonical cases. This is the spot-check the AC asks for: one differential input (`_` and space, both of which the *old* `EncodeProjectDir` mapped wrong before #501 and which are still the canonical "did the seam wire up?" probes).

## Concurrency model

Unchanged. The shape is still: one goroutine in `Run`, an `fsnotify` background goroutine pushing on `fsw.Events`/`fsw.Errors`, and `tuidriver.WaitForSessionJSONL` polling synchronously on the calling goroutine via a `time.Ticker`. Shutdown sequence: `ctx.Done` → `WaitForSessionJSONL` returns wrapped cancel error OR (post-wait) the event-loop `select` exits → `defer` closes `fsw` then `file`.

The fsnotify subscription is armed in `New` before `Run` is called, so WRITE events that arrive during the wait are buffered in `fsw.Events` (default buffer is 4096 events deep — far above any realistic JSONL append rate) and consumed by the event loop after the initial drain.

## Error handling

- `tuidriver.WaitForSessionJSONL` returns `fmt.Errorf("session jsonl %s did not appear: %w", path, context.Cause(ctx))` on cancellation/timeout. We return it as-is. Callers using `errors.Is(err, context.Canceled)` / `errors.Is(err, context.DeadlineExceeded)` continue to work through the `%w` wrap.
- Non-NotExist stat errors from `WaitForSessionJSONL` (e.g. ENOTDIR if the parent vanished) short-circuit the poll loop with `fmt.Errorf("stat session jsonl %s: %w", ...)`. We propagate.
- `os.Open` post-wait failures (TOCTOU on file vanishing) are wrapped as `"tail: open %s: %w"` and returned. No retry.
- `agentrun.ResolveWorkdir` errors (typically `fs.ErrNotExist`) are wrapped as `"tail: resolve workdir: %w"` in `New`. Behavior matches today's `"tail: encode workdir: %w"` wrap.

## Testing strategy

- All existing tests in `internal/agentrun/jsonl/tail/watcher_test.go` stay green (with the helper-signature touch-up). Per AC #4 they explicitly remain green; the spec adds tests, doesn't replace them.
- Two new tests cover the AC #4 explicit asks: `TestWatcher_WaitTimeout` (deadline path) and `TestWatcher_SpecialCharWorkdir` (path-correctness spot-check at the seam). The cancel-during-wait case is already covered by `TestWatcher_ContextCancellation`.
- `make check` runs `go vet`, `staticcheck`, `go test -race ./...` (AC #5).
- `make e2e-realclaude` is the byte-equivalence regression gate (#506) — the integration this ticket touches is on the live JSONL tail path that ptyrunner depends on, so a regression here would surface there immediately.

The 50 ms poll interval changes are inside the test budgets:
- `TestWatcher_LateCreate` writes lines with 20 ms inter-line delay, `waitForEndOfTurn` deadline 3 s. Wait can take up to 50 ms to detect appearance (vs. today's worst-case 250 ms via `probeRetryDelays`). Net: faster on average, never slower than 50 ms.
- `TestWatcher_FixtureIntegration` waits up to 5 s; replays 64 lines at 3 ms each = ~200 ms of writes. Trivially fits.
- `TestWatcher_ContextCancellation` cancels after 30 ms sleep. `WaitForSessionJSONL`'s up-front stat fires immediately on entry; if it returns NotExist, the ticker arms with a 50 ms tick — the ctx.Done branch wins inside the first tick window. No timing change.

## Open questions

None. The decision to keep `agentrun.ResolveWorkdir` (vs. removing it as part of this ticket) is intentional per ticket body — sibling #508 owns its deletion.

## Acceptance criteria mapping

| AC | Where addressed |
|---|---|
| `go.mod` pins tui-driver ≥ #58 (commit `a2edf4f` / 2026-05-22) | `go get github.com/pyrycode/tui-driver@latest && go mod tidy` step zero; pins `e0d558ebdbe0` (2026-05-23) which postdates `a2edf4f` |
| `tail.New` uses `tuidriver.SessionJSONLPath`; no `agentrun.EncodeProjectDir` call | `New` body update; production-side import of `agentrun` retained only for `ResolveWorkdir` |
| Initial-stat + bounded-retry block replaced by `tuidriver.WaitForSessionJSONL`; WRITE pump preserved | `Run` body update; `probeRetryDelays` and `openWithRetry` deleted; CREATE branch in event loop collapsed |
| New tests: wait-error path (timeout / cancel); special-char path spot-check; existing tests stay green | `TestWatcher_WaitTimeout` (new), `TestWatcher_SpecialCharWorkdir` (new); existing tests covered by helper update only |
| `make check` + `make e2e-realclaude` green | Standard gates — no spec-specific work beyond passing them |
