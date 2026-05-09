# 243 — `conv`: wire registry + sweep loop into daemon

## Files to read first

- `cmd/pyry/main.go:88-97` — `resolveRegistryPath(name)`. Returns `~/.pyry/<sanitized-name>/sessions.json` with the home-fallback behaviour. The new conversations resolver mirrors this exactly; do not invent a second path-resolution scheme.
- `cmd/pyry/main.go:332-432` — `runSupervisor`. Lines 347-349 are where path resolution lives today (socket / sessions registry / claude sessions dir). Lines 383-396 are the `sessions.Config` literal that gains two new fields. Lines 411-432 are the `Pool.Run` invocation + shutdown — no changes here.
- `internal/sessions/pool.go:51-93` — `Config`. `RegistryPath` (line 56-59) and `ClaudeSessionsDir` (line 61-66) are the precedent for "optional plumbing field with a doc-comment naming the cmd/pyry resolver." The two new fields slot in alongside, same shape.
- `internal/sessions/pool.go:122-167` — `Pool` struct. `registryPath` (line 129) and `claudeSessionsDir` (line 130) are the unexported mirrors. Two new unexported fields land here.
- `internal/sessions/pool.go:249-362` — `New`. The `cfg.RegistryPath != ""` and `cfg.ClaudeSessionsDir` plumbing on lines 340-341 is the assignment shape. New fields copy in identically.
- `internal/sessions/pool.go:707-760` — `(*Pool).Run`. Lines 735-757 are the rotation watcher's conditional registration on `dir != ""`: build the watcher's config, log+skip on construction error, otherwise `g.Go(func() error { return w.Run(gctx) })`. The sweep goroutine mirrors this shape but is even simpler — no construction step, just one `g.Go`.
- `internal/sessions/rotation/watcher.go:117-140` — `Watcher.Run` for reference: the canonical "long-running goroutine inside Pool.Run's errgroup" shape this ticket adds a sibling for.
- `internal/conversations/sweep_loop.go` — `RunSweepLoop(ctx, reg, path, interval, log) error` and `SweepInterval = time.Hour`. The function this ticket calls. Pre-conditions: `reg` non-nil, `path` non-empty, `log` non-nil, `interval > 0`. Returns `nil` on ctx cancellation; Save errors are logged + swallowed.
- `internal/conversations/registry.go:39-62` — `Load(path)`. Missing file returns `&Registry{}, nil` (benign cold start). Zero-byte file returns `&Registry{}, nil`. Malformed JSON returns `nil, error` — wrapped as `registry: parse <path>: %w`. The daemon-side error wrap (`loading conversations: %w`) layers on top.
- `internal/sessions/pool_test.go:25-79` — `helperPool` and `helperPoolWithSleepArgs`. The existing test-Pool builders. The new integration test reuses `helperPoolWithSleepArgs`'s `/bin/sleep 3600` shape so `Pool.Run` can actually exit cleanly on ctx cancel without needing a real claude.
- `docs/specs/architecture/242-conv-sweep-loop.md` — sibling spec just merged. Pins what `RunSweepLoop` does, what it does NOT do (no clock injection, no internal errgroup, no final on-shutdown sweep), and the call-site sketch (§ "Call site sketch (sibling #243, NOT this ticket)") this spec implements.
- `docs/knowledge/features/conversations-registry.md:46-60` — the registry path is `~/.pyry/<sanitized-name>/conversations.json`. "Resolving … is the consumer's job" — sibling-of-sessions discipline pinned by the feature doc.
- `docs/knowledge/features/conversations-auto-archive.md:239-241` — the "out of scope of #242, into scope of #243" boundary. Pins what this ticket owns and confirms the design constraints carried forward from #242.

## Context

Phase 3 auto-archive's last slice. The pure pieces have all landed: predicate (#219), `Sweep` + `Registry.Delete` (#237), `RunSweepLoop` + `SweepInterval` (#242). The daemon never loads `conversations.json` and the sweep loop has no caller.

This ticket adds the daemon-side glue:

1. Resolve `~/.pyry/<sanitized-name>/conversations.json` at startup (mirror of `resolveRegistryPath`).
2. Call `conversations.Load(path)`; missing file is benign cold start, malformed JSON fails startup.
3. Plumb the loaded `*Registry` + path through `sessions.Config` into `*Pool`.
4. In `Pool.Run`, register `conversations.RunSweepLoop` on the existing errgroup as a sibling of the rotation watcher.

This is the conversations registry's first daemon-side runtime consumer. The plumbing precedent set here will be reused by future consumers (CLI promotion verbs, channel routing). The shape is deliberately identical to the sessions registry's plumbing (Config field → Pool field → conditional Run-time wiring) so there is one pattern, not two.

The split between #242 (loop semantics) and this ticket (daemon wiring) was deliberate. #242's tests pin tick / Save / log behaviour without daemon scaffolding. This ticket's test only pins composition: that `Config.ConversationsRegistry` non-nil + `Pool.Run` actually causes the loop to run. No re-testing of loop internals.

## Design

### File layout

Three files touched, one new test file.

```
cmd/pyry/main.go                       (edit: +~10 LOC)
internal/sessions/pool.go              (edit: +~20 LOC)
internal/sessions/pool_conv_sweep_test.go   (new, ~120 LOC)
```

No new production files. The wiring is small enough — and concentrated enough at two sites — that introducing a helper file would obscure rather than clarify.

### `cmd/pyry/main.go` — path resolver + Load + plumb

#### New helper

```go
// resolveConversationsRegistryPath returns
// ~/.pyry/<sanitized-name>/conversations.json. Falls back to a CWD-relative
// path if $HOME can't be resolved (matches resolveRegistryPath's contract).
func resolveConversationsRegistryPath(name string) string {
    home, err := os.UserHomeDir()
    if err != nil || home == "" {
        return filepath.Join(sanitizeName(name), "conversations.json")
    }
    return filepath.Join(home, ".pyry", sanitizeName(name), "conversations.json")
}
```

Lives directly below `resolveRegistryPath` in main.go — co-locating the two `~/.pyry/<name>/` resolvers makes the symmetry obvious. **Not** refactored into a generic `resolveDataPath(name, filename string)` helper: pyrycode's working-principle #1 ("simplicity first; don't refactor adjacent code while you're there") wins. Two file-shaped resolvers is fine; the third one is when extraction becomes interesting.

#### `runSupervisor` edits

Three lines added to the path-resolution block (around line 348):

```go
socketPath := resolveSocketPath(*socketFlag, *name)
registryPath := resolveRegistryPath(*name)
convRegistryPath := resolveConversationsRegistryPath(*name)   // NEW
claudeSessionsDir := resolveClaudeSessionsDir(*workdir)
```

Five lines added between `tryAutoAttach` and the `slog` setup (so a malformed-JSON error fails before the daemon prints `pyrycode starting`):

```go
convReg, err := conversations.Load(convRegistryPath)
if err != nil {
    return fmt.Errorf("loading conversations: %w", err)
}
```

(Place this AFTER `tryAutoAttach` returns: auto-attach short-circuits the supervisor entirely; loading the conversations registry on the auto-attach path is wasted work and would surface a startup-style error in a foreground attach context.)

Two lines added to the `sessions.Config` literal:

```go
pool, err := sessions.New(sessions.Config{
    Logger:                       logger,
    RegistryPath:                 registryPath,
    ClaudeSessionsDir:            claudeSessionsDir,
    IdleTimeout:                  *idleTimeout,
    ActiveCap:                    *activeCap,
    ConversationsRegistry:        convReg,           // NEW
    ConversationsRegistryPath:    convRegistryPath,  // NEW
    Bootstrap: sessions.SessionConfig{ ... },
})
```

Imports: add `github.com/pyrycode/pyrycode/internal/conversations` to the import block.

#### Why `Load` happens in `cmd/pyry`, not in `sessions.New`

`sessions.New` could in principle take `ConversationsRegistryPath` and call `Load` itself, mirroring how it loads its own sessions registry. **But** the pyrycode convention (per `docs/knowledge/features/conversations-registry.md` and the sessions-registry pair) is that `internal/sessions` owns the sessions registry's lifecycle and `internal/conversations` is path-agnostic — the consumer (cmd/pyry) does the I/O. Putting `conversations.Load` inside `sessions.New` would couple the two registries' lifecycles in `internal/sessions`, blurring a boundary the package layout deliberately enforces. Keep the `*Registry` constructed in cmd/pyry; sessions only sees the already-loaded handle.

Side benefit: `internal/sessions`'s test surface (which already uses `helperPool` + `helperPoolWithSleepArgs` extensively) doesn't have to deal with on-disk conversations files unless the test explicitly cares.

#### Why no flag for the conversations registry path

The sessions registry path is also not flag-controlled — it's derived from `-pyry-name`. Consistent precedent. Operators who want isolation between pyry instances rely on `-pyry-name` (or `PYRY_NAME`) which gives both registries their own subdirectory atomically.

### `internal/sessions/pool.go` — Config + Pool fields + Run wiring

#### Config additions

```go
type Config struct {
    Bootstrap SessionConfig
    Logger    *slog.Logger

    RegistryPath      string
    ClaudeSessionsDir string

    // ConversationsRegistry, when non-nil, enables the periodic auto-archive
    // sweep goroutine inside Pool.Run. The registry must already be Loaded
    // by the caller (cmd/pyry handles the conversations.Load call so the
    // sessions package stays path-agnostic about conversations.json — see
    // docs/knowledge/features/conversations-registry.md).
    //
    // nil disables the sweep goroutine entirely (test default; existing
    // pool tests construct Config without this field and remain unchanged).
    ConversationsRegistry *conversations.Registry

    // ConversationsRegistryPath is the on-disk path of conversations.json
    // — passed to RunSweepLoop's Save call after each non-empty tick. In
    // production this is always ~/.pyry/<sanitized-name>/conversations.json
    // — see cmd/pyry resolveConversationsRegistryPath.
    //
    // Required when ConversationsRegistry is non-nil; ignored when nil.
    ConversationsRegistryPath string

    IdleTimeout time.Duration
    ActiveCap   int
}
```

Imports: add `github.com/pyrycode/pyrycode/internal/conversations` to `internal/sessions/pool.go`'s import block. (No package cycle — `internal/conversations` does not import `internal/sessions`; verified at architecture time.)

#### Pool struct additions

```go
type Pool struct {
    // ...existing fields...
    registryPath      string
    claudeSessionsDir string

    // convReg and convRegistryPath mirror Config.ConversationsRegistry /
    // .ConversationsRegistryPath. Read-only after New — set once,
    // consulted only by Pool.Run to decide whether to register the sweep
    // goroutine. No lock needed.
    convReg          *conversations.Registry
    convRegistryPath string

    // ...existing runGroup / runCtx / sessionTpl / idleTimeoutDefault...
}
```

#### `New` assignment

In `New`'s `&Pool{...}` literal (around line 336), add:

```go
p := &Pool{
    sessions:           map[SessionID]*Session{bootstrapID: sess},
    bootstrap:          bootstrapID,
    log:                cfg.Logger,
    registryPath:       cfg.RegistryPath,
    claudeSessionsDir:  cfg.ClaudeSessionsDir,
    convReg:            cfg.ConversationsRegistry,
    convRegistryPath:   cfg.ConversationsRegistryPath,
    // ...
}
```

No validation (nil-reg-with-non-empty-path or vice versa). The contract is documented in the doc-comment; misuse is a programmer error caught at the first `Pool.Run` invocation, not a runtime failure mode worth defending against.

#### `Pool.Run` wiring

Sibling registration to the existing rotation watcher block. Add after the rotation block (lines 735-757), before `return g.Wait()`:

```go
if p.convReg != nil {
    interval := convSweepInterval
    g.Go(func() error {
        return conversations.RunSweepLoop(gctx, p.convReg, p.convRegistryPath, interval, p.log)
    })
}
```

Where `convSweepInterval` is a package-level test seam (next subsection).

Order within `Pool.Run`: bootstrap supervise → rotation watcher (conditional) → sweep loop (conditional) → `g.Wait()`. The order is irrelevant for correctness (errgroup composes them); it follows the logical "session lifecycle → on-disk reconciliation → hygiene tasks" ordering that cmd/pyry's startup logs read in.

#### Test seam: `convSweepInterval`

```go
// convSweepInterval is the interval Pool.Run passes to
// conversations.RunSweepLoop. Test-overridable; production callers do not
// touch it — it stays at conversations.SweepInterval (one hour) for the
// life of the process.
//
// Lives at the sessions-package level (rather than as a Config field) so it
// is invisible to production callers. The integration test in this package
// swaps it via t.Cleanup-driven save/restore around a Pool.Run exercise.
var convSweepInterval = conversations.SweepInterval
```

#### Why a package-level seam, not a `Config.ConversationsSweepInterval` field

A Config field would expand the production-facing surface for a purely test-only concern. There is no operator use case for tuning the sweep interval — `time.Hour` is well-justified relative to the 30-day archive threshold (#219), and exposing it as a knob would create a documentation obligation and a "what does 5 minutes mean" support question for no production benefit. Test seams that have no production callers belong inside the package; package vars in Go are the canonical shape (rotation tests use `newProbe = rotation.DefaultProbe` at line 29 of pool.go for the same reason).

The seam lives in `pool.go` next to `newProbe` so the existing "test-overridable knob" precedent is co-located.

### Why no Pool.Run docstring rewrite

The existing doc-comment (lines 707-713) says `Run blocks until ctx is cancelled, supervising every session in the pool and (when ClaudeSessionsDir is set) running the rotation watcher alongside it.` Update to:

```go
// Run blocks until ctx is cancelled, supervising every session in the pool,
// running the rotation watcher (when ClaudeSessionsDir is set) and the
// conversations auto-archive sweep loop (when ConversationsRegistry is set)
// alongside it. errgroup ties the goroutines together: cancellation
// propagates, and Wait returns the first non-nil error.
```

Two-line patch; keeps the existing structure verbatim.

### Why no `internal/e2e` test

The AC explicitly permits in-package integration tests. An `internal/e2e` test would need full daemon scaffolding (control socket, fakeclaude binary, real `pyry` build) to exercise a code path that the in-package test exercises in ~120 lines using `helperPoolWithSleepArgs`. The shorter test pins the same wiring contract.

Future consumers of the same plumbing pattern (CLI promotion, channel routing) will introduce their own e2e coverage when they ship the operator-visible feature; at that point the e2e harness has reason to know about conversations.json. Today there is no operator-visible feature — only the hygiene goroutine — so the test scope matches.

## Concurrency model

No new goroutine *types*. The only addition is one extra `g.Go` call inside `Pool.Run`'s existing `errgroup`. The goroutine's body is `conversations.RunSweepLoop`, whose concurrency contract is fully pinned by spec #242:

- Runs a `time.Ticker` exclusively in this goroutine.
- Calls `Sweep(reg, time.Now())` per tick. `Sweep` takes `reg.mu` (briefly) per `List` call and per `Delete` call. Concurrency-safe with any other goroutine (existing or future) that mutates the registry through its public API.
- Calls `reg.Save(path)` after a non-zero-count tick. `Save` snapshots under `reg.mu` and releases before disk I/O.
- Returns nil on `gctx.Done()`. The `errgroup`'s `Wait` returns `g`'s first non-nil error; nil-on-cancel from this goroutine never wins the race against whatever real error caused the cancellation, which is the desired post-mortem property.
- Save errors are non-fatal — logged at ERROR, loop continues.

No new locks. `Pool.convReg` and `Pool.convRegistryPath` are read-only after `New`; the `Pool.Run` goroutine reads them before spawning the inner goroutine, no lock needed (matches the `claudeSessionsDir` access at line 717).

Shutdown order on `ctx` cancellation:

1. `gctx` cancels (errgroup propagation).
2. `RunSweepLoop`'s `<-ctx.Done()` arm fires; deferred `t.Stop()` releases the ticker; returns nil.
3. The bootstrap supervisor's `sup.Run` returns (its own ctx-cancel path).
4. The rotation watcher (when active) returns its `ctx.Err()`.
5. `g.Wait()` returns the first non-nil error among them — typically `context.Canceled` from the rotation watcher; the sweep loop's nil-return is a no-op for the post-mortem.

No final on-shutdown sweep. Pinned by #242's AC; restated here so the wiring ticket cannot accidentally re-introduce one (e.g. by calling `Sweep` + `Save` after the loop returns).

## Error handling

Failure modes the wiring introduces:

1. **`conversations.Load` returns an error at startup.** A missing file is `nil` per `Load`'s contract — no error, empty registry. A zero-byte file is also `nil`. The only error path is malformed JSON. Wrap as `loading conversations: %w` and return — startup fails, the operator sees `pyry: loading conversations: registry: parse /home/.../conversations.json: invalid character '...'`. This is fatal-at-startup (matches `pool init: %w` for the sessions registry's parallel failure mode at line 398-399).

2. **`Pool.Run` invariants violated.** `convReg != nil && convRegistryPath == ""` is technically reachable from a misuse of `sessions.Config`. The loop's first non-zero-count tick will fail Save with `registry: mkdir : ...` and log at ERROR; the loop survives. **No defensive validation in `New`.** Reasoning: the cmd/pyry call site is the only production caller and always sets both fields atomically; an `internal/sessions` test that wants to exercise nil-reg + "" path doesn't trigger this code path; a hypothetical future caller that misuses the API is a programmer error caught immediately on first archive-eligible tick. Adding validation here would be defensive code without an observed failure mode (Belt-and-Suspenders principle: skip).

3. **Save fails mid-life.** Owned by `RunSweepLoop`, not by this ticket. Logged + swallowed; loop continues. Stale-disk note (carried over from #242 spec): if Save never recovers before daemon exit, the next startup re-reads the unmutated on-disk file, re-archives in memory on the first tick, and the cycle is idempotent.

Failures the wiring **cannot** cause:

- The sweep goroutine cannot bring down the daemon's errgroup. `RunSweepLoop` returns nil on cancellation and never returns non-nil otherwise (its only error source — Save — is swallowed at the call site).
- Adding the sweep goroutine cannot affect the rotation watcher's lifecycle. They are siblings under the same errgroup; the errgroup's first-error-wins semantics already cover the cross-goroutine cancellation path.

## Testing strategy

One new test file: `internal/sessions/pool_conv_sweep_test.go`. Three tests; the integration test required by the AC plus two small contract pins.

All tests use stdlib `testing` only. No new dependencies.

### Helpers in the new file

```go
// withConvSweepInterval temporarily overrides convSweepInterval for the
// duration of t. Restored via t.Cleanup.
func withConvSweepInterval(t *testing.T, d time.Duration) {
    t.Helper()
    prev := convSweepInterval
    convSweepInterval = d
    t.Cleanup(func() { convSweepInterval = prev })
}

// seedConvRegistry writes a conversations.json file with `archivable`
// archive-eligible entries (LastUsedAt set 60 days in the past) and `fresh`
// non-archivable entries (LastUsedAt set to time.Now()). Returns the loaded
// *conversations.Registry and the on-disk path.
//
// Reuses the seedSpec/mk pattern from internal/conversations/sweep_test.go
// — the `time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)`-style literal stays
// in the conversations-package tests; here we use time.Now() relatives so
// ShouldArchive's predicate compares against the real wall-clock the
// production loop will use.
func seedConvRegistry(t *testing.T, archivable, fresh int) (*conversations.Registry, string) { ... }
```

Co-located in the test file (one consumer); not promoted to package-level helpers.

### `TestPool_Run_RegistersSweepLoop_HappyPath` — primary AC test

Goal: prove the wiring path actually invokes the sweep when `ConversationsRegistry` is set, and that Save lands on disk.

Shape:

1. `withConvSweepInterval(t, 5*time.Millisecond)`.
2. `reg, path := seedConvRegistry(t, 2, 1)` — 2 archivables (`LastUsedAt` 60 days ago), 1 fresh (now). Initial on-disk file has 3 entries.
3. Build `Pool` via `sessions.New(Config{...})` directly, modelled on `helperPoolWithSleepArgs`'s shape:
   - `Bootstrap.ClaudeBin = /bin/sleep`, `ClaudeArgs = []string{"3600"}`, `Bridge = supervisor.NewBridge(logger)`.
   - `BackoffInitial/Max/Reset` = small (10ms / 10ms / 1s) so any unexpected restart surfaces fast.
   - `Logger = slog.New(slog.NewTextHandler(io.Discard, nil))`.
   - `ConversationsRegistry = reg`, `ConversationsRegistryPath = path`.
   - **No** `RegistryPath` (skip sessions.json persistence — out of scope here).
   - **No** `ClaudeSessionsDir` (skip rotation watcher — same).
4. `ctx, cancel := context.WithCancel(context.Background()); defer cancel()`.
5. Spawn `done := make(chan error, 1); go func() { done <- pool.Run(ctx) }()`.
6. Poll the on-disk file's mtime / contents until at least one tick has fired and Save has landed:
   - Repeated `os.Stat(path)` followed by `conversations.Load(path)` and `len(reg2.List()) == 1` check.
   - 2-second deadline. The 5ms interval gives the test ~400 ticks of headroom under normal CI; even a 100× slowdown on a slow runner is well within budget.
7. `cancel()`. Read from `done` with a 5-second deadline.
8. Assert: `done`-channel error is `nil` or `context.Canceled` (treat both as clean — the bootstrap's `sup.Run` may surface `context.Canceled` through the errgroup; that is not a failure of this test). Use `errors.Is` against `context.Canceled` to decide.
9. Re-`Load(path)` and assert exactly one entry survives, with `LastUsedAt` matching the `fresh` seed.

The 5-second `done`-deadline is defence-in-depth: if `Pool.Run` doesn't exit on cancel (e.g. a bootstrap supervisor goroutine deadlock surfaced by a future regression), the test fails fast rather than hanging.

### `TestPool_Run_NoSweepLoopWhenRegistryNil`

Goal: pin the negative — when `ConversationsRegistry == nil`, no sweep goroutine is registered.

Shape:

1. `withConvSweepInterval(t, 1*time.Millisecond)` — short enough that a goroutine, if registered, would fire many ticks during the test window.
2. Build `Pool` via `helperPool(t, false)` — no conversations config.
3. Note a path that does NOT exist on disk: `path := filepath.Join(t.TempDir(), "shouldnotexist.json")`.
4. Run the pool briefly: `ctx, cancel := context.WithCancel(context.Background()); go pool.Run(ctx); time.Sleep(50*time.Millisecond); cancel()`.
5. Wait for `Run` to return.
6. Assert `os.Stat(path)` returns `errors.Is(err, fs.ErrNotExist)` — proves no sweep goroutine attempted Save anywhere.

This is a weak negative — a regression that registered the goroutine with a *different* path would not be caught — but combined with the happy-path test's "sweep produces an on-disk file at the configured path", a bidirectional regression is well-covered. The intent of this test is to assert "the conditional `if p.convReg != nil` actually gates the goroutine"; the strongest available signal under that gate is "nothing happens."

(Existing `pool_test.go` tests already cover "Pool.Run with no conversations config exits cleanly" implicitly. This test pins the gate explicitly so a regression that flipped the gate's polarity would surface here rather than as a confusing failure in an unrelated test.)

### `TestResolveConversationsRegistryPath` — `cmd/pyry` unit pin

Goal: pin the path-resolver shape symmetrically with the existing `resolveRegistryPath` precedent.

Lives in `cmd/pyry/main_test.go` (NOT in `internal/sessions`). Table-driven, two rows:

- `name = "pyry"` → `~/.pyry/pyry/conversations.json` (verified via `filepath.Join(homeDir, ".pyry", "pyry", "conversations.json")`).
- `name = "weird/name"` → `~/.pyry/weird_name/conversations.json` (sanitization applied).

Reuses the test idioms in `cmd/pyry/main_test.go` (which, per a quick scan at architect time, follows the standard table-driven shape). If `cmd/pyry/main_test.go` does not exist yet, create it with a minimal scaffold; one stand-alone test does not justify a separate file but this is a valid place to start. If a tests file already exists, add the new test as a sibling function.

Rationale for testing the resolver: it has a small forking branch (`os.UserHomeDir` failure → CWD fallback) that the home-success path tested by `TestResolveRegistryPath` (if it exists) would not catch on its own. The two-row table covers home-success on both clean and sanitized names. The CWD-fallback path is left untested at this layer — `os.UserHomeDir` returning an error is not reliably reproducible without subprocess-env tricks; the parallel `resolveRegistryPath` does not test it either, and consistency with that precedent is the right call.

### What NOT to test

- Loop tick / Save / log behaviour — pinned by `internal/conversations/sweep_loop_test.go` (#242).
- `Sweep` correctness — pinned by `internal/conversations/sweep_test.go` (#237).
- `ShouldArchive` boundary semantics — pinned by `internal/conversations/archive_test.go` (#219).
- `conversations.Load` on missing / zero-byte / malformed input — pinned by `internal/conversations/registry_test.go` (#217).
- `os.UserHomeDir`-fallback path in `resolveConversationsRegistryPath` — same as `resolveRegistryPath` precedent (untested; not reliably reproducible without env-injection tricks).
- `cmd/pyry` startup integration (boot daemon, mutate file, kill, re-boot) — out of scope; would be an `internal/e2e` test, and the AC permits in-package coverage when Pool.Run is awkward, which it is here.
- `conversations.Load` failure surfacing through `cmd/pyry` startup — straightforward error-wrap, no behaviour worth pinning beyond eyeball verification of the wrap string.

### Why not test the actual error wrap on `Load` failure

The error chain `cmd/pyry: loading conversations: registry: parse <path>: <json error>` is built mechanically from a single `fmt.Errorf("loading conversations: %w", err)` line. Stdlib testing of `fmt.Errorf("...: %w", ...)` is testing the stdlib. The wrap string `"loading conversations: "` is a one-line value worth eyeball-checking in review and worth not duplicating into a test that exists only to assert it.

## Open questions

None. Every AC corresponds to an unambiguous code path:

- Path resolution → mirror of `resolveRegistryPath`, pinned line-by-line.
- Load + wrap → 5 lines, contract pinned by `Load`'s doc-comment.
- Plumbing → 2 Config fields + 2 Pool fields, pinned by the sessions registry's parallel.
- Errgroup wiring → 1 `g.Go` call modelled on the rotation watcher's, pinned by line 755.
- Nil-reg gate → `if p.convReg != nil`, pinned by AC.
- Test interval injection → package-level seam pinned by `newProbe` precedent at pool.go:29.
- Integration test → `helperPoolWithSleepArgs`-shaped Pool with `/bin/sleep 3600` bootstrap, file-on-disk assertion, pinned by `TestRunSweepLoop_TicksAndCancels`'s polling shape from #242.

## Out of scope (do not implement here)

- Adding `--no-conversations` / `--ephemeral` flags to disable the conversations registry. None exist today; the AC explicitly says auto-archive runs regardless of `--no-resume`/`--ephemeral` (which themselves don't exist as separate flags either — `-pyry-resume=false` is the only related toggle, and it controls claude's `--continue`, not pyry's persistence layer).
- A configurable sweep interval flag or env var. Pinned out of scope by #242's spec; restated here.
- Metrics emission for the sweep goroutine. No metrics surface exists in pyrycode yet; defer until one does.
- An `internal/e2e` test. AC permits in-package; the in-package test pins the same wiring contract at a fraction of the line count.
- Refactoring `resolveRegistryPath` and `resolveConversationsRegistryPath` into a generic `resolveDataPath(name, filename string)`. Two file-shaped resolvers is fine; the third one is when extraction becomes interesting (working-principle #1: simplicity first; don't refactor adjacent code while you're there).
- Wiring `conversations.Save` to fire on registry mutation events (Create/Update/Promote/Delete) outside the sweep loop. The sweep is the only daemon-side mutation source today; non-sweep mutations land when the CLI promotion verb (#218 successor) ships, and that ticket owns its own Save discipline.
- Validating `Config.ConversationsRegistry != nil` requires `Config.ConversationsRegistryPath != ""`. Defensive code without an observed failure mode; cmd/pyry sets both atomically (the only production call site).
- Exercising `Pool.Run` shutdown on a real claude binary. `helperPoolWithSleepArgs`'s `/bin/sleep 3600` shape is the test precedent for "Pool.Run blocks, ctx cancellation tears down" without involving claude.
