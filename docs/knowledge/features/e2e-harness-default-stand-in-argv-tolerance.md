# Default Stand-In Argv Tolerance (#918)

The zero-value `spawnWith` path (used by `Start` / `StartIn` /
`StartInWithEnv`, and by `StartExpectingFailureIn` via `spawn`) invoked
`/bin/sleep` directly until #918. #839 wired `Config.ResolveSessionID` on
**every** bootstrap spawn (`internal/supervisor/supervisor.go`'s
`buildClaudeArgs` appends `--session-id <uuid>`, driven from
`internal/sessions/pool.go`), not just `Pool.Create`'s new-session path
that #116 already worked around. A bare `/bin/sleep 99999 --session-id
<uuid>` gets rejected by both BSD and GNU `sleep(1)`, crash-looping the
bootstrap child under backoff — three default-path tests that need the
child to reach and hold `running` went red: `TestE2E_BootstrapWarmStart_
IgnoresEvictedOnDisk`, `TestE2E_IdleEviction_LazyRespawn`, and
`TestRelayV2_Daemon/v2_enabled_request_snapshot_round_trip`.

Fix: point `spawnWith`'s default at the same argv-ignoring wrapper #116
already proved (`writeSleepClaude` / `sleepClaudeScript`, see § Active-Cap
Eviction Pattern), instead of introducing a second wrapper. The helper
moved verbatim from `cap_test.go` (`//go:build e2e`) into `harness.go`
(`//go:build e2e || e2e_install`) — `spawnWith` lives in `harness.go`, and
referencing an `e2e`-only symbol from an `e2e || e2e_install` file would
have broken the `e2e_install` build. Zero call-site changes: every
existing `writeSleepClaude` caller resolves to the same package symbol.

```go
if o.claudeBin == "" {
    o.claudeBin = writeSleepClaude(t, home)   // was: "/bin/sleep"
}
```

`claudeArgs`'s `{"99999"}` default is unchanged — the wrapper ignores it
identically to any appended flag, so no per-test argv reasoning is needed.

**No-regression argument.** Tests that thread `-pyry-claude=` as an
extraFlag (the `sessions_*` suite) still have `spawnOpts.claudeBin == ""`,
so `spawnWith` now also writes `home/sleep-claude.sh` before their extra
flag overrides it with the identical path and content — an idempotent
double-write, last-flag-wins per Go's `flag` package, no behavioural
change. Tests that set `claudeBin` directly (`StartRotation*` →
fakeclaude) have `claudeBin != ""`, so the wrapper is never written.
`StartExpectingFailureIn`: the daemon fails before ever exec'ing
`-pyry-claude` (corrupt registry / workdir confinement), so the wrapper
is written but never run; its assertions target `.pyry/test/sessions.json`
and stderr, not `home`'s file listing.

**Scope.** This fixes only the default `/bin/sleep` stand-in. Two other
red e2e mechanisms found in the same sweep are disjoint and tracked
separately, with no file overlap: `spawnAttachableDaemon`'s Go-test-binary
stand-in (4 `TestE2E_Attach_*` tests, rejects `--session-id` via
`flag.Parse()`) was #257 (landed — see § Attach PTY Harness Pattern below,
[codebase/257.md](../codebase/257.md)); `TestRelayV2_InterruptStopsRunningTurn`'s
argv-immune-fakeclaude failure ("turn never started") is a daemon-side #839 regression, #929. A **ninth** red test outside the original
enumeration, `TestTwoPhoneStructured_InteractiveReceivesStream`, fails
identically on `main` (not a regression) via yet another path
(`StartRotationWithRelay` sets `claudeBin` directly, bypassing the
default-stand-in fix entirely) — filed as #930, left red. See
`docs/knowledge/codebase/918.md` for the full incident/lesson writeup.

`bootstrap_warm_start_test.go:64`'s narrative comment ("Supervisor.Run
spawns `/bin/sleep`") is now mildly stale (the default stand-in is a
`#!/bin/sh` wrapper over `sleep`, not a bare `/bin/sleep`) — left
untouched deliberately, since #918's AC required no test-body changes.
