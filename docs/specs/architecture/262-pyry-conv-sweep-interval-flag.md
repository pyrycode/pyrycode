# Spec — #262: `-pyry-conv-sweep-interval` flag + `Config.SweepInterval` plumbing

## Files to read first

- `internal/sessions/pool.go:23-31` — current `var convSweepInterval = conversations.SweepInterval` package-level seam being replaced.
- `internal/sessions/pool.go:62-122` — `sessions.Config` struct; new `SweepInterval` field lands here, alongside the existing `ConversationsRegistry` / `ConversationsRegistryPath` fields it semantically pairs with.
- `internal/sessions/pool.go:151-204` — `Pool` struct; new `convSweepInterval time.Duration` field lands here next to `convReg` / `convRegistryPath` (same lifecycle: read-only after `New`, no lock).
- `internal/sessions/pool.go:286-401` — `New(cfg Config)`; the `&Pool{...}` literal at 373 is where `convSweepInterval` gets initialized from `cfg.SweepInterval`, with the zero-value fallback to `conversations.SweepInterval`.
- `internal/sessions/pool.go:798-804` — the `if p.convReg != nil` block in `Pool.Run` that currently reads the package var; switches to `p.convSweepInterval`.
- `internal/sessions/pool_conv_sweep_test.go` — the existing in-package tests; the `withConvSweepInterval` helper goes away, callers set `pool.convSweepInterval = ...` directly (symmetric with the existing `pool.convReg = reg` / `pool.convRegistryPath = path` assignments at lines 69-70).
- `internal/sessions/pool_test.go:46-79` — `helperPoolWithSleepArgs`; constructs the pool the conv-sweep tests reuse. Not modified — the new field defaults to `conversations.SweepInterval` when the helper doesn't set it.
- `cmd/pyry/main.go:200-207` — `pyryFlagValues` map; new `pyry-conv-sweep-interval` key gets added so `splitArgs` consumes the flag and its value before the claude-args split.
- `cmd/pyry/main.go:391-460` — `runSupervisor`'s flag declarations and the `sessions.New(sessions.Config{...})` call site; new `convSweepInterval := fs.Duration(...)` and a corresponding `SweepInterval: *convSweepInterval` field in the Config literal.
- `cmd/pyry/main.go:1293-1303` — `printHelp()`'s pyry-flags block; one line added for the new flag with the "(testing)" annotation.
- `internal/conversations/sweep_loop.go:9-37` — `SweepInterval` constant + `RunSweepLoop` signature; confirms the zero-value semantics ("interval > 0" precondition) so the New-time fallback can't accidentally hand `0` downstream.

## Context

Ticket #251 (split-source) needs an out-of-process e2e test that drives the conversations sweep loop deterministically. The current seam — package-private `var convSweepInterval = conversations.SweepInterval` swapped via `withConvSweepInterval` in `pool_conv_sweep_test.go` — only works for in-package tests because it mutates a package-level variable in the test process's address space. An e2e test that spawns `pyry` as a subprocess can't reach it.

This ticket adds a real flag plumbed through `sessions.Config` so e2e tests can pass `-pyry-conv-sweep-interval=100ms` to the daemon. Default behaviour is unchanged when the flag is absent — production keeps `conversations.SweepInterval` (one hour).

## Design

### 1. Replace the package var with a `Config` field + `Pool` field

Decision (note in PR): **replace, don't keep both.** The `withConvSweepInterval` helper and the `var convSweepInterval = ...` package-level seam are both removed. Two seams expressing the same thing ("the interval Pool.Run hands to RunSweepLoop") is one too many — if Config has it, that's the only path. The in-package tests switch to setting the new `pool.convSweepInterval` field directly after construction, exactly as they already set `pool.convReg` and `pool.convRegistryPath`.

Trade-off considered: keeping the package var as a fallback would let the existing helper continue to work without touching `pool_conv_sweep_test.go`. Rejected — the dual seam invites future drift (e.g. setting one but not the other in a new test) and the test-side migration is two-line per test (`pool.convSweepInterval = 5*time.Millisecond`).

### 2. `sessions.Config.SweepInterval`

```go
// SweepInterval, when > 0, overrides the conversations sweep tick
// interval Pool.Run passes to conversations.RunSweepLoop. Zero (the
// default) means use conversations.SweepInterval (one hour). Production
// callers leave this zero; the cmd/pyry -pyry-conv-sweep-interval flag
// (intended for e2e tests) plumbs a small duration here so the sweep
// loop can be exercised without waiting an hour.
//
// Ignored when ConversationsRegistry is nil (the sweep goroutine
// doesn't run at all in that case).
SweepInterval time.Duration
```

Lands in `Config` immediately after `ConversationsRegistryPath` — same lifecycle, same nil-registry coupling.

### 3. `Pool.convSweepInterval`

```go
// convSweepInterval is the resolved interval Pool.Run passes to
// conversations.RunSweepLoop. Set in New from cfg.SweepInterval, with
// conversations.SweepInterval as the zero-value fallback. Read-only
// after New — set once, consulted only by Pool.Run. No lock needed.
//
// In-package tests may overwrite this field directly after construction
// (mirrors the existing convReg / convRegistryPath pattern in
// pool_conv_sweep_test.go).
convSweepInterval time.Duration
```

Lands in `Pool` next to `convReg` / `convRegistryPath`.

### 4. `New` wiring

In the `&Pool{...}` literal at `internal/sessions/pool.go:373`:

```go
convSweepInterval: func() time.Duration {
    if cfg.SweepInterval > 0 {
        return cfg.SweepInterval
    }
    return conversations.SweepInterval
}(),
```

Or, equivalently, hoist to a local before the literal:

```go
sweepInterval := cfg.SweepInterval
if sweepInterval <= 0 {
    sweepInterval = conversations.SweepInterval
}
```

Implementer's choice. The hoisted form reads marginally more naturally.

### 5. `Pool.Run` consumption

At `internal/sessions/pool.go:798-804`, replace the package-var read:

```go
if p.convReg != nil {
    interval := convSweepInterval                            // OLD
    interval := p.convSweepInterval                          // NEW
    g.Go(func() error {
        return conversations.RunSweepLoop(gctx, p.convReg, p.convRegistryPath, interval, p.log)
    })
}
```

The `interval` local is retained only because the closure captures it; could equally be inlined.

### 6. Remove the package-level seam

Delete `internal/sessions/pool.go:23-31` (the comment + `var convSweepInterval = conversations.SweepInterval`). After this change, the `conversations.SweepInterval` constant has exactly one consumer in the sessions package: the zero-value fallback inside `New`.

Delete `internal/sessions/pool_conv_sweep_test.go:16-23` (`withConvSweepInterval` helper). The two callers (lines 64, 123) become:

```go
pool := helperPoolWithSleepArgs(t)
pool.convReg = reg
pool.convRegistryPath = path
pool.convSweepInterval = 5 * time.Millisecond   // was: withConvSweepInterval(t, 5*time.Millisecond)
```

For `TestPool_Run_NoSweepLoopWhenRegistryNil` (line 123), the override is similarly `pool.convSweepInterval = 1 * time.Millisecond` — but note this test exercises the `convReg == nil` branch, so the interval is never read; the assignment exists only to mirror the prior helper call's intent. Implementer may drop it (the assignment serves no behavioural purpose when `convReg` is nil) — note the choice in the PR.

### 7. CLI flag

In `cmd/pyry/main.go`:

**`pyryFlagValues` (line ~200):** add `"pyry-conv-sweep-interval": true` so `splitArgs` consumes both the flag and its value before tipping into claude-args territory.

**`runSupervisor` flag declarations (line ~398):** add

```go
convSweepInterval := fs.Duration("pyry-conv-sweep-interval", 0,
    "override conversations sweep tick interval (testing; 0 = production default)")
```

Default `0` (not `conversations.SweepInterval`) so the production-default path is "user did not set the flag", not "user set the flag to one hour". This keeps the flag's *meaning* aligned with `Config.SweepInterval`'s zero-value semantics — one definition of "use the default", lives in `sessions.New`.

**`sessions.Config` literal (line ~445):** add

```go
SweepInterval: *convSweepInterval,
```

next to `ConversationsRegistry` / `ConversationsRegistryPath`.

**`printHelp()` (line ~1303):** add one line in the pyry-flags block:

```
-pyry-conv-sweep-interval duration  override conversations sweep tick interval
                                    (testing; 0 = production default of 1h)
```

Place it after `-pyry-idle-timeout` to keep the testing-oriented flag visually adjacent to the other duration knobs.

## Concurrency model

Unchanged. The new field `Pool.convSweepInterval` is set once in `New` (single-threaded constructor) and read once in `Pool.Run` (which runs before the errgroup goroutines start consuming it). The "read-only after New, no lock" invariant holds — same shape as `convReg`, `convRegistryPath`, and `activeCap`.

The in-package test pattern (set the field directly between `helperPoolWithSleepArgs(t)` and `pool.Run(ctx)`) is also race-clean because the assignment happens before any goroutine reads it. This matches how the existing tests already mutate `pool.convReg` and `pool.convRegistryPath`.

## Error handling

No new error paths. `flag.Duration` rejects malformed values at parse time (e.g. `-pyry-conv-sweep-interval=banana` produces a `flag.Parse` error that `runSupervisor` already returns). Negative values pass through `flag.Duration` (they're valid `time.Duration` values) but `cfg.SweepInterval > 0` in step 4 treats `<= 0` as "use the default" — so negative inputs degrade gracefully to the production default rather than feeding a negative interval into `RunSweepLoop` (whose precondition is `interval > 0`).

## Testing strategy

### Unit test (AC#5)

Refactor the existing in-package `TestPool_Run_RegistersSweepLoop_HappyPath` (`pool_conv_sweep_test.go:63`) to drive the override via `pool.convSweepInterval = 5 * time.Millisecond` instead of `withConvSweepInterval(t, 5*time.Millisecond)`. The behavioural assertions (sweep runs, archive-eligible entries are dropped, on-disk file ends with only survivors) are unchanged — only the seam used to inject the small interval changes. This satisfies the AC's "refactor the existing pool sweep test to drive via `Config.SweepInterval`" wording.

Add **one** new test asserting the New-time wiring:

```go
func TestPool_New_HonoursConfigSweepInterval(t *testing.T) {
    cfg := /* minimal Config — Bridge bootstrap as in helperPoolWithSleepArgs */
    cfg.SweepInterval = 7 * time.Millisecond
    pool, err := New(cfg)
    if err != nil { t.Fatalf("New: %v", err) }
    if got := pool.convSweepInterval; got != 7*time.Millisecond {
        t.Errorf("convSweepInterval = %v, want 7ms", got)
    }
}
```

And one for the zero-value fallback:

```go
func TestPool_New_DefaultSweepIntervalWhenConfigZero(t *testing.T) {
    cfg := /* minimal Config — SweepInterval omitted */
    pool, err := New(cfg)
    if err != nil { t.Fatalf("New: %v", err) }
    if got := pool.convSweepInterval; got != conversations.SweepInterval {
        t.Errorf("convSweepInterval = %v, want %v (default)", got, conversations.SweepInterval)
    }
}
```

These are pure constructor tests — no `Pool.Run`, no goroutines, no timing. They pin the resolution rule (Config wins, zero falls back to constant) so future changes to the resolution path can't accidentally regress.

### What is NOT tested at this layer

- The `cmd/pyry` flag-to-Config plumbing. `runSupervisor` is invoked only by integration tests that already exist (e2e harness in `internal/e2e/`), and the AC for #262 is the production-side wiring. The e2e ticket downstream of #251 will exercise the end-to-end path.
- `pyry --help` text. No existing test asserts on `printHelp()` output.

## Open questions

None blocking. Two implementer-choice points are noted inline (hoisted local vs. inline IIFE in `New`; whether to keep the no-op `pool.convSweepInterval = ...` assignment in `TestPool_Run_NoSweepLoopWhenRegistryNil`); both are stylistic and either choice is correct.

## Acceptance-criteria mapping

| AC | Where it's satisfied |
|----|----------------------|
| #1 New `Config.SweepInterval` field, zero = default | § 2 |
| #2 `Pool.Run` honours `Config.SweepInterval` when non-zero; document the seam decision | § 4–6; PR notes "package var + helper removed" |
| #3 `-pyry-conv-sweep-interval` flag, visible in `--help` with "for testing" annotation | § 7 |
| #4 No behavioural change when flag absent | § 4 zero-value fallback + § 7 default `0` |
| #5 Unit test asserts `Pool.Run` uses `Config.SweepInterval` when set | "Testing strategy" — refactored happy-path test + new constructor tests |
