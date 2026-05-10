# Spec — #263: e2e test for the conversations sweep loop

## Files to read first

- `internal/e2e/restart_test.go:31-49` — `newRegistryHome(t)`. Creates a short-named temp HOME (sun_path-safe), mkdirs `<home>/.pyry/test/`, registers cleanup, returns `(home, regPath)` where `regPath = <home>/.pyry/test/sessions.json`. Reuse verbatim — the parent dir is exactly where `conversations.json` also belongs.
- `internal/e2e/restart_test.go:117-148` — `writeRegistry` / `readRegistry` / `mustReadFile` helpers. The `mustReadFile(t, path)` failure-message helper is what to attach to the polling-timeout error message; the `writeRegistry` shape is **not** reused (this test seeds via the canonical `conversations.Registry` writer, not raw JSON).
- `internal/e2e/harness.go:184-220` — `StartIn(t, home, extraFlags...)`. Extra pyry-flags are appended after the standard set and before the `--` claude-arg sentinel; last-wins semantics. This is the seam for `-pyry-conv-sweep-interval=100ms`. The harness auto-injects `-pyry-name=test`, `-pyry-claude=/bin/sleep`, `-pyry-idle-timeout=0` — leave those alone.
- `internal/e2e/harness.go:601-628` — `Stop` / `teardown`. SIGTERM → 3s grace → SIGKILL → 1s grace. Stop returns once `doneCh` fires (or after the full escalation with a `t.Logf` warning). The 4s upper bound comfortably satisfies AC#4's ~5s budget; no custom shutdown helper needed.
- `internal/e2e/harness_test.go:124-132` — `processAlive(pid)`. Zero-signal POSIX probe; reuse for the post-Stop "daemon is gone" assertion.
- `internal/e2e/cli_verbs_test.go:60-72` — the panic/`runtime/`/`goroutine ` stderr scan pattern. Lift verbatim for AC#4's "no panic" half.
- `internal/sessions/pool_conv_sweep_test.go:19-51` — `seedConvRegistry` shape. Cannot import (in-package test); recipe is the same: build a `*conversations.Registry`, `reg.Create(...)`, `reg.Save(path)`, with `LastUsedAt = time.Now().UTC().Add(-60 * 24 * time.Hour)` for archive-eligible entries. The 8-digit-prefix UUID literal style (`"11111111-1111-4111-8111-111111111111"`) is the project convention; keep it.
- `internal/conversations/conversation.go` — `Conversation` field shape. All exported. `Name` is `*string` (nil = never named); promoted entries set it to a non-nil pointer. `IsPromoted` is a plain bool. `Cwd` is required.
- `internal/conversations/registry.go:46-62` — `Load(path)`. Missing file → `&Registry{}, nil` (cold start). Zero-byte → `&Registry{}, nil`. Malformed JSON → wrapped error, nil registry. The poll loop calls Load on every tick; intermediate atomic-rename writes by the daemon are race-clean (rename is the commit point).
- `internal/conversations/sweep_loop.go:9-48` — `RunSweepLoop` tick semantics. `time.NewTicker` does NOT fire immediately — first tick is at +interval. At 100ms interval the first sweep happens ~100ms after `Pool.Run` registers the goroutine, well inside AC#2's 5s budget. Save is called only on non-zero archive count; zero-count ticks do not touch disk. No final on-shutdown sweep.
- `internal/conversations/archive.go` — `archiveIdleThreshold = 30 * 24 * time.Hour`; `ShouldArchive` returns false unconditionally for promoted entries. The seeded promoted entry persists by virtue of `IsPromoted: true`; its 60-day-past `LastUsedAt` is irrelevant.
- `cmd/pyry/main.go:401-455` — flag declaration + path resolution + `Load` + `Config` wiring. Confirms `-pyry-name=test` produces `<home>/.pyry/test/conversations.json` (same parent dir `newRegistryHome` already creates for sessions.json). Confirms the flag's zero-value default = production interval.
- `docs/specs/architecture/262-pyry-conv-sweep-interval-flag.md` § "CLI flag" — pins `-pyry-conv-sweep-interval=100ms` as the production-shipped flag. The flag is the only available out-of-process seam; the in-package `withConvSweepInterval` helper was deleted in #262.
- `internal/sessions/pool_conv_sweep_test.go:53-110` — `TestPool_Run_RegistersSweepLoop_HappyPath`. The in-package counterpart of this e2e. Same poll-on-disk strategy, same 5ms-vs-100ms tick choice scaled up for an out-of-process test. This e2e closes the gap that test cannot reach: `cmd/pyry/main.go`'s Config-construction wiring + the full process lifecycle including signal-driven shutdown.

## Context

`internal/sessions/pool_conv_sweep_test.go` already drives `Pool.Run` directly with a 5ms interval and asserts that archive-eligible conversations are removed on tick. That covers the in-package wiring (Pool.Run's `if p.convReg != nil` branch). It does NOT cover:

1. **Daemon-side Config construction.** `cmd/pyry/main.go` resolves `convRegistryPath`, calls `conversations.Load`, and passes `ConversationsRegistry` / `ConversationsRegistryPath` into `sessions.Config`. A regression there (e.g. forgetting to wire one of those Config fields, or breaking the path resolver) would silently leave `p.convReg == nil`, the in-package test would still pass, and production would lose auto-archive without any test signal.

2. **Full-process shutdown.** `Pool.Run`'s errgroup cancellation propagates to `RunSweepLoop` via context. The supervisor then needs to exit cleanly within a bounded window. An in-package test exercises the goroutine but not the surrounding process lifecycle.

This is the same regression class as v0.10.1's hang: daemon-wiring bugs that unit tests cannot see. The fix is one out-of-process test that runs the real `pyry` binary against a real on-disk registry file, drives the sweep on a 100ms tick, and exits the process cleanly. The 100ms tick is enabled by the `-pyry-conv-sweep-interval` flag from #262 (the in-package `withConvSweepInterval` package-var seam was removed in that ticket and is unreachable from a subprocess by design).

## Design

### File layout

One new file. No edits to existing files.

```
internal/e2e/conv_sweep_test.go    (new, ~140 LOC)
```

Build tag: `//go:build e2e` (matches every other file in this package). Package: `e2e`.

### Imports

```go
import (
    "bytes"
    "path/filepath"
    "testing"
    "time"

    "github.com/pyrycode/pyrycode/internal/conversations"
)
```

The `internal/conversations` package's `Registry`, `Conversation`, `Load`, `Save`, `Create`, `List` are all exported. Importing it directly (rather than defining a local mirror struct as `restart_test.go` does for the unexported sessions registry) gives the test the canonical writer/reader, ensuring the on-disk envelope is byte-identical to what production produces. `bytes` is for the stderr-panic scan; `path/filepath` is for the convPath derivation; `time` is for LastUsedAt seeding and poll deadlines. No `os` import needed — the file is written via `reg.Save(path)`, not raw `os.WriteFile`.

### Test layout

One test function, one direction, no subtests:

```go
func TestE2E_ConvSweep_RemovesUnpromotedKeepsPromoted(t *testing.T) { ... }
```

Subtests are not justified — there's one daemon spawn, one set of assertions. AC#2, #3, #4, #5 are all checked against the same run.

### Body sketch

```go
func TestE2E_ConvSweep_RemovesUnpromotedKeepsPromoted(t *testing.T) {
    home, _ := newRegistryHome(t)   // sessions.json path returned but unused;
                                    // we want only the parent dir setup.
    convPath := filepath.Join(home, ".pyry", "test", "conversations.json")

    now := time.Now().UTC()
    sixtyDaysAgo := now.Add(-60 * 24 * time.Hour)

    promotedName := "kept-channel"
    reg := &conversations.Registry{}
    reg.Create(conversations.Conversation{
        ID:         "11111111-1111-4111-8111-111111111111",
        Name:       &promotedName,
        Cwd:        "/seed-promoted",
        IsPromoted: true,
        LastUsedAt: sixtyDaysAgo,
    })
    reg.Create(conversations.Conversation{
        ID:         "22222222-2222-4222-8222-222222222222",
        Cwd:        "/seed-unpromoted",
        IsPromoted: false,
        LastUsedAt: sixtyDaysAgo,
    })
    if err := reg.Save(convPath); err != nil {
        t.Fatalf("seed Save: %v", err)
    }

    h := StartIn(t, home, "-pyry-conv-sweep-interval=100ms")
    pid := h.PID

    // AC#2 + AC#3 + AC#5: poll the on-disk file until the unpromoted
    // entry has been swept. Read via conversations.Load so the assertion
    // exercises the same envelope production reads through.
    var swept *conversations.Registry
    deadline := time.Now().Add(5 * time.Second)
    for time.Now().Before(deadline) {
        loaded, err := conversations.Load(convPath)
        if err != nil {
            t.Fatalf("Load while polling: %v", err)
        }
        if len(loaded.List()) == 1 {
            swept = loaded
            break
        }
        time.Sleep(50 * time.Millisecond)
    }
    if swept == nil {
        t.Fatalf("sweep did not remove unpromoted entry within 5s; current file:\n%s",
            mustReadFile(t, convPath))
    }

    survivors := swept.List()
    if got := string(survivors[0].ID); got != "11111111-1111-4111-8111-111111111111" {
        t.Errorf("survivor ID = %q, want promoted entry", got)
    }
    if !survivors[0].IsPromoted {
        t.Errorf("survivor IsPromoted = false, want true: %+v", survivors[0])
    }

    // AC#4: clean shutdown within a bounded timeout, no panic. Stop's
    // SIGTERM→3s→SIGKILL→1s escalation gives a 4s upper bound, well
    // inside AC#4's 5s budget. processAlive afterwards catches the
    // "killGrace exceeded" case (Stop only t.Logf's that path).
    h.Stop(t)
    if processAlive(pid) {
        t.Errorf("daemon pid=%d still alive after Stop", pid)
    }
    for _, bad := range [][]byte{[]byte("panic"), []byte("runtime/"), []byte("goroutine ")} {
        if bytes.Contains(h.Stderr.Bytes(), bad) {
            t.Errorf("stderr contains %q — expected clean exit, not crash:\n%s",
                bad, h.Stderr.Bytes())
        }
    }
}
```

### Why this shape

**`newRegistryHome` reuse.** The helper is in `restart_test.go` (same package, same build tag), so it's directly callable. Its role here is the temp-HOME + `<home>/.pyry/test/` mkdir; the sessions.json path it returns is irrelevant for this test (the daemon comes up cold for sessions, which is fine — `internal/sessions.Load` on missing returns empty + nil). Re-deriving the convPath off `home` rather than threading a second return value through the helper keeps `restart_test.go` untouched.

**Seeding via `conversations.Registry` rather than raw JSON.** `restart_test.go` mirrors the (unexported) sessions registry shape locally because it has no other choice. This test does have a choice — the conversations types are exported — and using the canonical writer kills two bugs at once: (a) field-tag drift between this test's mirror struct and production, (b) atomic-write semantics (the seed file lands via the same temp+rename rename the daemon will use, not via a raw `os.WriteFile`). Side benefit: the seed file exercises the same `Save` path that the sweep itself will exercise on tick — the test's "before" and "after" use the same on-disk codec.

**Polling cadence.** 50ms poll gap against a 100ms tick gives ~10 chances inside the 5s budget. Larger gaps risk flaky misses on a slow CI runner; smaller gaps add no signal. Match the harness's existing `readyPollGap` cadence rather than inventing a new constant.

**Single survivor count assertion + ID check + IsPromoted check.** AC#2 is "unpromoted entry removed", AC#3 is "promoted entry persists". The two are inseparable — both are facts about the post-sweep file. One `len(loaded.List()) == 1` poll-exit gate plus two field assertions on the survivor is the minimum that pins both ACs. Asserting on `survivors[0].Name` would over-couple to an implementation detail (the seed sets it; production sweep doesn't touch it; the test would still pass without that assertion if Save accidentally dropped Name in transit, which is an `internal/conversations` concern not an `internal/e2e` one).

**No assertion on a "fresh" survivor.** AC asks for two seeded entries — one promoted, one unpromoted, both 60 days idle. The promoted entry IS the survivor; an additional fresh-and-unpromoted "control" would prove the predicate is `IsPromoted`-aware and `LastUsedAt`-aware in the same test. That's already covered by the in-package `pool_conv_sweep_test.go` (which seeds `archivable=2, fresh=1` against the same predicate). This e2e exists to cover the daemon-wiring gap, not to re-assert predicate semantics.

**`h.Stop(t)` rather than a custom signal helper.** Stop is the harness's blessed "graceful shutdown" path: SIGTERM, wait on `doneCh`, escalate to SIGKILL after termGrace. The 4s upper bound (3s + 1s) sits comfortably under AC#4's 5s budget. The two follow-up assertions (processAlive + stderr scan) cover the two failure modes Stop alone doesn't fail on: (a) Stop hit the killGrace path with `doneCh` still open (Stop logs but doesn't fail), and (b) the daemon panicked on its way down (Stop doesn't inspect stderr). Neither is hypothetical — (a) is the regression class this whole test exists for; (b) is the v0.10.1 incident shape.

**No goroutine-leak assertion.** AC#4 explicitly says "a clean process exit is sufficient evidence." Adding `runtime.NumGoroutine()` checks would require `t.Helper()` racy probing inside the daemon process, which we don't have access to from out-of-process. Out of scope.

## Concurrency model

Single goroutine inside the test process: `for { Load + sleep }` on the polling timer. The daemon process owns its own concurrency (Pool.Run errgroup, sweep goroutine, supervisor); this test observes it from outside via the filesystem and POSIX signals.

The atomic-rename Save semantics (see `internal/conversations/registry.go:62-115`: temp file + Chmod + Encode + Sync + Close + Rename) make the cross-process read race-clean: each `Load` either sees the pre-Save bytes or the post-Save bytes, never a torn write. The poll loop just retries until it sees the post-Save state.

Shutdown ordering: Stop sends SIGTERM → daemon's `signal.NotifyContext` cancels the parent ctx → `Pool.Run`'s errgroup ctx cancels → `RunSweepLoop`'s `<-ctx.Done()` returns nil → errgroup `Wait` returns → supervisor `Run` returns → `cmd.Process` exits → `cmd.Wait` returns → `doneCh` closes → Stop returns. Any non-clean step would either panic (caught by stderr scan) or fail to terminate within 4s (caught by Stop's escalation + processAlive).

## Error handling

Test-level. Three forms:

- **Setup errors** (`reg.Save`, `conversations.Load` while polling) → `t.Fatalf` with the wrapped error. These indicate the seed or the assertion machinery itself is broken; no point continuing.
- **Polling timeout** → `t.Fatalf` with the on-disk file dumped via `mustReadFile(t, convPath)`. This is the most common failure mode if the daemon-side wiring regresses; the file dump is critical for diagnosing whether the daemon never wrote the file (wiring regression, file unchanged) vs. wrote a wrong file shape (envelope regression).
- **Post-Stop assertions** (`processAlive`, stderr scan) → `t.Errorf` (not `Fatalf`) so a panic-on-shutdown failure still reports the still-alive case alongside, instead of one masking the other.

No retry logic. The poll loop is the only retry; everything else is single-attempt.

## Testing strategy

This file IS the test. There is no companion unit test — the in-package `pool_conv_sweep_test.go` already covers the predicate / sweep / Save semantics; this e2e exists exclusively to cover the out-of-process daemon-wiring gap.

CI invocation: this test runs under the existing `go test -tags=e2e ./internal/e2e/...` job. No new tag, no new job, no new make target. The 100ms tick + 5s budget keeps the test cost in the same order as the existing restart e2e (which also spawns one `pyry` process per test).

The flake budget is the polling loop's 5s ceiling. On a heavily-loaded macOS CI runner the first tick could be delayed past 100ms; ~10 polls inside 5s is comfortable headroom even at a 10x slowdown. If the test starts flaking, the right fix is to raise the budget (e.g. 10s), not to lower the tick interval — the daemon's `time.NewTicker` cadence is what's being measured, and a sub-100ms interval starts to interact with the runner's scheduler granularity.

## Open questions

None blocking.

Two implementer-choice points are noted inline:

1. The convPath can be derived from `home` (the sketch above) or `newRegistryHome` could be extended to also return it. The sketch chooses the lighter touch — no edit to `restart_test.go`. Either is correct.
2. The promoted entry's `Cwd` is set to `/seed-promoted`; the unpromoted's to `/seed-unpromoted`. Could equally be empty strings. The non-empty values are mildly more useful in failure diagnostics (the dumped file shows which seeded entry survived); not load-bearing.

## Acceptance-criteria mapping

| AC | Where it's satisfied |
|----|----------------------|
| #1 New e2e seeds promoted + unpromoted with 60-day-past LastUsedAt; spawns pyry with -pyry-conv-sweep-interval=100ms | "Body sketch" — seed block + StartIn call |
| #2 Within 5s budget, on-disk file shows unpromoted entry removed | Polling loop + 5s deadline + len-1 exit gate |
| #3 Across the same poll window, promoted conversation persists | Survivor[0] ID + IsPromoted assertions |
| #4 After shutdown signal, process exits within bounded timeout, no panic | h.Stop + processAlive + stderr scan |
| #5 Sweep-effect assertions read conversations.json from disk | `conversations.Load(convPath)` inside the poll, not Pool internals |
