# #2593 — Record each session's coding agent (harness) and select its runner by it

## Files read

- `internal/sessions/registry.go` → `registryEntry`, `permissionModeForDisk`, `dormantEntries` — the on-disk schema, the "omit the default on disk" pattern this mirrors, and the verbatim dormant carry that makes AC 2 hold for dormant entries.
- `internal/sessions/runnerstate.go` → `RunnerConfig` — gains the `Harness` field.
- `internal/sessions/session.go` → `Session` (`settings`, `bootstrap`, `spawnBase` fields) — gains a construction-fixed `harness`.
- `internal/sessions/pool.go` → `New` (bootstrap `RunnerConfig` literal), `buildSession`, `CreateIn`, `saveLocked`, `UpdateDormantSettings` — both construction sites, the write-back, and the one dormant mutator (reads the entry and writes the same struct back, so a harness it did not touch survives).
- `internal/sessions/get_or_create.go` → `materialise` — the provisional read under RLock before `buildSession`; the harness joins it.
- `internal/sessions/revive.go` → `Revive` — the dormant-entry route to the factory.
- `cmd/pyry/main.go` → `selectInteractiveRunner`, the two `pool.Revive` callers (`sessionRouter` and the rotation wiring) — errors propagate to the caller, nothing fatal.
- `internal/sessions/runner_test.go`, `pool_revive_settings_race_test.go` → `fakeRunner`, `helperPoolInjectedFactory` — the doubles and warm-start helper the new tests reuse. Five existing tests call `buildSession(id, label, spawnDir, settings)` directly, which is why its signature stays.

In-flight overlap check: no other `feature/*` branch touches these files.

## Context

Codex support (#2585) needs a second runner behind the pool. Today nothing records which agent a session runs, so the stream runner is the only possible factory output. This ticket adds that record ("harness"), persisted per session, carried into `RunnerConfig`, and selected on by the factory the daemon wires into the pool. Its only valid value is `claude`; behaviour for Claude is unchanged.

## Design

**Vocabulary** (`runnerstate.go`): exported `const HarnessClaude = "claude"`. `RunnerConfig.Harness string` — always canonical at both pool construction sites (never empty). The factory is the one place that decides which values have a runner; the sessions package does not validate the value, it only carries it.

**Disk** (`registry.go`): `registryEntry.Harness string \`json:"harness,omitempty"\``. Two helpers beside `permissionModeForDisk`:
- `canonicalHarness(h string) string` — `""` → `HarnessClaude`, else `h` verbatim.
- `harnessForDisk(h string) string` — `HarnessClaude` → `""`, else `h`. Keeps every Claude session's entry byte-identical to the pre-change shape.

Dormant entries are carried verbatim by `dormantEntries` and written verbatim by `saveLocked`, so a non-`claude` dormant entry round-trips with no further code (AC 2). `UpdateDormantSettings` rewrites the whole struct it read, so it preserves the harness too.

**Live session** (`session.go`): `Session.harness string`, canonical, construction-fixed (no mutator). `saveLocked` writes `Harness: harnessForDisk(s.harness)`.

**Construction sites** (`pool.go`):
- `New` (bootstrap): always `HarnessClaude`, on both `RunnerConfig.Harness` and `Session.harness`. The bootstrap entry's own harness key is not read: the bootstrap is the daemon's auto-spawned Claude session by definition, and a hand-edited non-claude value there would otherwise refuse daemon startup.
- `buildSession(id, label, spawnDir, settings)` keeps its signature and becomes a one-line wrapper over `buildSessionAs(id, label, spawnDir, settings, harness string)` with `HarnessClaude`. `CreateIn` (and thus `Create`/`Mint`) keep calling `buildSession`: nothing exposes the harness to clients, so a fresh session is Claude.
- `buildSessionAs` canonicalises the harness and sets it on `RunnerConfig` and `Session`. Everything else is today's `buildSession` body.

**Materialise** (`get_or_create.go`): inside the existing provisional RLock, also read `harness := canonicalHarness(p.dormant[id].Harness)` (a map miss yields `""` → claude) and pass it to `buildSessionAs`. This is the "early route" the ticket names — the dormant entry is otherwise looked up only after the factory ran. It applies to both callers (Revive and GetOrCreateIn landing on a dormant id), like the timestamp carry, because the harness is the session's identity, not a caller choice.

No authoritative re-read is needed: nothing writes a dormant entry's harness, and `p.dormant` never gains an id it did not hold at `New` (the only writes are `UpdateDormantSettings` rewriting an existing entry and `materialise`'s rollbacks restoring the entry it retired). So under the write lock the entry is either the one the provisional read saw or gone; its harness cannot differ.

**Selecting factory** (`cmd/pyry/main.go`): `harnessRunnerFactory(claude sessions.RunnerFactory) sessions.RunnerFactory` — returns a factory that switches on `cfg.Harness`: `""` or `HarnessClaude` → `claude(cfg)`; anything else → `fmt.Errorf("no runner for harness %q", cfg.Harness)` without calling `claude`. `selectInteractiveRunner`'s stream arm wraps `newStreamRunnerFactory(...)` in it. `""` is accepted as Claude so a `RunnerConfig` built outside the pool (tests) keeps working.

Data flow: `sessions.json harness` → `dormantEntries` → `p.dormant` → `materialise` provisional read → `buildSessionAs` → `RunnerConfig.Harness` → `harnessRunnerFactory` → stream runner | error. Live: `Session.harness` → `saveLocked` → disk.

## Concurrency model

No new goroutines or locks. The harness read joins the existing `p.mu.RLock` in `materialise`. `Session.harness` is construction-fixed, so `saveLocked` reads it under `Pool.mu` without further discipline.

## Error handling

A non-`claude` harness makes the factory return an error. `buildSessionAs` wraps it (`sessions: create runner: …`), removes its two files via the existing `built` defer, and `materialise` returns before touching `p.sessions` or `p.dormant` — so the entry stays dormant with its harness intact, nothing is saved, and the error propagates to the Revive caller (`sessionRouter`), which returns it to the client. The daemon keeps running; no Claude runner is built because the selector never calls the stream factory.

## Testing strategy

New `internal/sessions/pool_harness_test.go`:
- Legacy registry (bootstrap + a dormant entry, no `harness` key) written with `saveRegistryLocked`; `New` + a forced `saveLocked` → file bytes identical; the captured bootstrap `RunnerConfig.Harness` is `claude`.
- Dormant entry with `harness: "codex"` survives `New` + `saveLocked`; the reloaded file still carries `codex`.
- `Revive` of a dormant `codex` id with a factory that refuses non-claude → error; the factory saw `codex`; `p.dormant[id].Harness` is still `codex`; the id is not in `p.sessions`; the file on disk still carries it.
- `Revive` of a dormant entry with no harness → factory sees `claude`.
- `buildSession` (the mint funnel) → factory sees `claude`.

New `cmd/pyry/harness_runner_test.go`: `harnessRunnerFactory` delegates for `""` and `claude`, returns an error and never calls the delegate for `codex`.

Existing suites run untouched (AC 4).

## Open questions

None.

## Documentation handoff

None required by the ticket. Pending for the documentation stage: the sessions package overview may note the `harness` registry key and the selecting factory.
