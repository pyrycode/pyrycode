# Spec — #395: default `--pyry-idle-timeout` to `0` (idle eviction opt-in)

## Files to read first

- `cmd/pyry/main.go:395-470` — `runSupervisor`: the flag-set construction at line 405 and the `sessions.Config{IdleTimeout: *idleTimeout, …}` wiring at line 462. The only production change to behaviour lives in this region.
- `cmd/pyry/main.go:1320-1335` — long help block (`printHelp`). Contains the human-facing description of `-pyry-idle-timeout` that must be re-worded.
- `internal/sessions/pool.go:95-105`, `pool.go:140-155`, `pool.go:360-410` — `Config.IdleTimeout` / `SessionConfig.IdleTimeout` semantics. Confirms the pool already treats `0` as "disabled" via `idleTimeoutDefault`. Read so the developer is certain no plumbing change is required downstream of `cmd/pyry`.
- `internal/sessions/pool_test.go:1020-1054` — `TestPool_ParityWhenIdleDisabled`. Existing coverage that `IdleTimeout==0` does not schedule eviction. The new flag-default test does NOT need to re-prove this invariant; it only needs to prove the flag's default value.
- `internal/e2e/idle_test.go:18-50` — both e2e idle-eviction tests pass `-pyry-idle-timeout=1s` explicitly. No fixture update needed.
- `internal/sessions/session_test.go:120-135` and `internal/sessions/session_persist_test.go:25-35` — confirm package-level tests always set `IdleTimeout` explicitly on `SessionConfig`. No implicit-default dependents.
- `docs/knowledge/features/idle-eviction.md:56` — operator-facing default mentioned as `15m`; must change.
- `docs/knowledge/architecture/system-overview.md:225` — same.
- `docs/knowledge/decisions/005-idle-eviction-state-machine.md:63` — the original ADR cites `15m` as a "sensible production default". ADRs are append-only — add a dated footer noting the 2026-05-15 reversal and the daemon-mode rationale; do not edit the original consequence line.

## Context

`--pyry-idle-timeout` defaulted to `15m` since #40 (ADR 005). That default optimises for short-lived interactive sessions where a warm `claude` between attaches has negligible upside and amortised memory cost matters. Under `systemd --user pyry.service` / launchd daemon mode (#202, #190, install-service flows), the same default produces a silent bot outage exactly 15 minutes after every supervisor restart — the bootstrap session is evicted on schedule, and the companion respawn-on-attach bug (separate ticket) prevents recovery.

The ticket body documents the failure mode confirmed on pyrybox 2026-05-15. The fix direction is settled and not architectural: flip the global CLI default from `15m` to `0` (eviction opt-in). The amortised-cost case for the old default never applied to the install-service path, and the daemon-aware alternative (special-case the default inside `install-service`) was rejected in the ticket for being more code paths than the problem warrants.

## Design

### Single production change

`cmd/pyry/main.go:405`:

```
idleTimeout := fs.Duration("pyry-idle-timeout", 0, "evict idle claudes after this duration (0 disables; pass e.g. 15m to enable)")
```

That is the only behavioural delta. Pool plumbing is already zero-aware (`pool.go:367-369`, `pool.go:963`, and the never-armed timer pattern documented in `docs/lessons.md:88`). `sessions.Config{IdleTimeout: 0, …}` flows through unchanged, the per-session `idleTimer` is constructed but never armed, and `runActive`'s `select` reads from a nil channel for the eviction case.

### Help-text alignment

`cmd/pyry/main.go:1329-1330` (`printHelp` long block):

```
  -pyry-idle-timeout    evict idle claudes after this duration
                        (default 0 / disabled; pass e.g. 15m to enable)
```

Wording rationale: matches the AC literally ("default 0; pass a duration like 15m to enable") while keeping the existing two-line shape used by the surrounding flags. Drop the "respawn latency 2-15s on next attach" parenthetical — it described post-eviction behaviour relevant only when eviction is on, and the companion respawn bug makes the literal "2-15s" claim wrong today. The respawn-latency detail belongs in `docs/knowledge/features/idle-eviction.md`, not the CLI help.

The short flag-help string (the third argument to `fs.Duration` above) and the long help block must stay consistent. Both updated in this change.

### No refactor, no plumbing change, no new types

Resist the temptation to extract a `pyryFlags` struct or a `registerPyryFlags(fs)` helper while we're here. The bug is a one-literal regression of a value chosen in 2025 for a use case (interactive sessions) that doesn't match the install-service path. The fix should look like that — a literal flip and a help-text re-word. The unit test that asserts the new default works fine without a refactor (see below).

## Concurrency model

Unchanged. No goroutines, channels, or context lifetimes touched. The idle timer in `Session.runActive` is constructed lazily from `s.idleTimeout`; when the value is `0`, the timer's channel is a nil `<-chan time.Time` and the eviction case is unreachable. This is the pattern pinned in `docs/lessons.md:88` ("`time.Timer` with a nil channel placeholder for 'disabled.'").

## Error handling

Unchanged. The flag parser accepts `0`, `0s`, and any other zero-value duration spelling; all flow through `time.Duration` arithmetic identically.

Operators who explicitly pass `-pyry-idle-timeout=15m` (or any non-zero duration) get exactly the prior behaviour — Acceptance Criterion #3 is satisfied by the fact that pool plumbing is unchanged.

## Testing strategy

Two tests cover the AC:

1. **New: flag-default regression guard.** Add a unit test in `cmd/pyry/` (e.g. extend an existing `*_test.go` in this package; do NOT create a new file just for this) that:
    - Constructs a fresh `flag.FlagSet` with `flag.ContinueOnError`.
    - Registers `-pyry-idle-timeout` with default `0` and parses an empty argv.
    - Asserts the parsed value equals `time.Duration(0)`.

   This test only guards "the literal `0` we just wrote stays `0`." It does not need to invoke `runSupervisor` or assemble a real `sessions.Pool`. The reason it suffices: the existing `TestPool_ParityWhenIdleDisabled` already proves that an `IdleTimeout==0` pool does not schedule eviction, and the wiring at `cmd/pyry/main.go:462` (`IdleTimeout: *idleTimeout`) is direct field assignment. A regression that flipped the literal back to `15m` would be caught by this test; a regression that broke pool zero-handling would be caught by the existing pool test. The two together cover the AC's "and the resulting pool does not schedule eviction" rider without duplicating the pool's coverage in the cmd-level test.

   *Do not* test by spawning the `pyry` binary with `-h` and grepping help text — that conflates two assertions (the literal and the help string), is far slower, and isn't necessary.

2. **Existing tests are already self-sufficient.** Audited:
    - `internal/sessions/session_test.go`, `session_persist_test.go`, `pool_test.go` — all sites set `IdleTimeout` explicitly on `SessionConfig` / `Config`. No fixture changes required.
    - `internal/e2e/idle_test.go` — both tests pass `-pyry-idle-timeout=1s` explicitly to `StartIn`. No fixture changes.
    - `internal/e2e/harness.go`, `auto_attach.go`, `attach_pty.go`, `install_*_test.go`, `cap_test.go` — every spawn site that needs eviction disabled already passes `-pyry-idle-timeout=0` explicitly; every site that needs eviction enabled passes a non-zero value. Audited at the grep hits in the ticket's technical notes; no implicit-default dependents.
    - `cmd/pyry/update_e2e_test.go:271` already passes `-pyry-idle-timeout=0` explicitly.

   No existing test needs to be modified.

## Documentation updates

The developer updates three knowledge docs as part of this ticket (they are operator-facing and will mislead readers the moment the default flips):

- `docs/knowledge/features/idle-eviction.md:56` — change `(default 15m)` to `(default 0 / disabled; opt in with e.g. 30s)` and add a one-sentence rationale referencing the daemon-mode failure mode.
- `docs/knowledge/architecture/system-overview.md:225` — same wording flip in the inline CLI summary.
- `docs/knowledge/decisions/005-idle-eviction-state-machine.md` — ADRs are append-only. Add a `## Update — 2026-05-15` section at the end of the file noting that the production default changed from `15m` to `0` because the original rationale ("sensible production default") didn't account for `install-service` / daemon-mode use, where 15-minute eviction is a silent bot outage rather than an amortised-cost win. Reference ticket #395 and the companion respawn-on-attach ticket. Do not edit the original "Consequences" line — the historical reasoning stays intact; the footer marks the reversal.

These three doc edits do not count against the production-source self-check.

`docs/PROJECT-MEMORY.md`, `docs/lessons.md`, and `docs/knowledge/INDEX.md` are NOT touched — they are off-limits per pipeline rules. INDEX.md does not need a new entry; this is an amendment to existing docs.

## Open questions

None. The fix direction is settled in the ticket body; the only judgement calls (don't refactor; assert the default with a minimal flagset test; keep the ADR's original reasoning intact and append) are documented above.

## Size

XS. Production-source files modified: 1 (`cmd/pyry/main.go`). Production-source LOC delta: ~3 (one literal + two help strings). Test LOC: ~15 (one new unit test). Doc edits: 3 files (not counted). No new exported types, no consumer call-site cascade, no concurrency surface changed.
