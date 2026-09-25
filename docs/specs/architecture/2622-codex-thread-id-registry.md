# #2622 — store a Codex session's thread id in the registry and resume it on rebuild

## Files read

- `cmd/pyry/codex_runner.go` → `newCodexRunnerFactory`, `codexRunnerConfig`, `codexRunner.runOnce`, `codexRunner.RestartFresh` — where a thread is started or resumed, and where the fresh restart rotates the tag and drops the held thread.
- `cmd/pyry/codex_runner_test.go` → `fakeCodexBin`, `codexHarnessT.bound` — the fake app-server build and the bound-client probe the new test reuses.
- `cmd/pyry/stream_turn_drain.go` → `streamSessionTag`, `streamTurnSink.sinkForTag`, `streamTurnSink.ch` — the live id the runner reports under, and the fan-in the test reads events from.
- `cmd/pyry/session_revive_test.go` → `runPoolReady`, `stubRunner` usage — how a `cmd/pyry` test runs a real pool and revives a dormant entry.
- `internal/sessions/registry.go` → `registryEntry`, `canonicalHarness`, `harnessForDisk`, `dormantEntries` — the omitempty field pattern #2593 set.
- `internal/sessions/runnerstate.go` → `RunnerConfig.AdoptAnnouncedReset`, `RunnerConfig.Harness` — the runner-to-pool callback precedent: it carries no identity of its own, the caller supplies the live id.
- `internal/sessions/get_or_create.go` → `materialise` — the one read of the dormant entry before `buildSessionAs`; the harness already rides it.
- `internal/sessions/pool.go` → `buildSession`, `buildSessionAs`, `rekeyLocked`, `saveLocked` — construction, the shared re-key, and the single registry write point.
- `internal/sessions/transition.go` → `RotateForNewSession` — re-keys the pool (under `Pool.mu`) BEFORE the caller hands the new id to `RestartFresh`.
- `internal/sessions/pool_harness_test.go`, `pool_revive_settings_race_test.go` → `helperPoolInjectedFactory`, `forceSave`, `harnessCapture` — the in-package seeding pattern.
- `docs/knowledge/features/sessions-package.md` — lock order `Pool.mu → Session.lcMu`; saves happen under `Pool.mu`.

## Context

Codex slice S3a. The Codex runner holds its thread id in memory only; a session rebuilt from the registry (daemon restart or revive from dormant — the same path, `materialise`) gets a new runner and starts a new thread. Codex mints its thread ids, so the pool session id stays the key and the thread id is persisted beside it in the session's registry entry.

Overlap: `origin/feature/2586` also edits `cmd/pyry/codex_runner.go`, `codex_runner_test.go` and `internal/sessions/pool.go` (per-turn posture). Not a dependency — it touches `WriteUserTurn`, the config struct and `UpdateSettings`; this ticket touches `runOnce`, `RestartFresh`, the factory literal and construction/re-key. Edits here stay additive; a later merge may touch those files.

Size: six production files, one over the line, as the refiner's estimate stated. Kept whole under the floor rule: the pool-side field has one consumer (the Codex runner), and neither half is verifiable alone.

## Design

### Registry (`registry.go`)

`registryEntry` gains `ThreadID string \`json:"thread_id,omitempty"\`` — the harness's own conversation id for a harness that mints one (Codex). Empty for every Claude session and for every entry written before the key existed, so those load and save back byte-identical. A dormant entry carries it verbatim (it is part of the entry `dormantEntries` copies and `saveLocked` writes back).

### Session (`session.go`)

`Session.threadID string`, guarded by `Pool.mu` (the same discipline as `label` and `settings`): set at construction by `buildSessionAs`, written by `Pool.recordThread`, cleared by `rekeyLocked`, read by `saveLocked` — all under `Pool.mu`.

### RunnerConfig (`runnerstate.go`)

- `ThreadID string` — the stored thread this session resumes; empty means start one. Set only by `buildSessionAs` from the dormant entry.
- `RecordThread func(sessionID, threadID string) error` — the runner reports a thread it STARTED. Like `AdoptAnnouncedReset` it carries no identity: the caller passes the live session id, so it stays correct after any number of rotations. nil from any pool path that does not set it (the bootstrap); the caller skips it.

### Pool (`pool.go`, `get_or_create.go`)

- `buildSessionAs(id, label, spawnDir, settings, harness, threadID string)` — one new parameter, carried onto `RunnerConfig.ThreadID` and `Session.threadID`; sets `RecordThread` to a closure over `Pool.recordThread`. `buildSession` passes `""`.
- `materialise` reads `p.dormant[id].ThreadID` in the same RLock as the harness and passes it on. Same no-re-read argument as the harness: nothing writes a dormant entry's thread id.
- `(p *Pool) recordThread(id SessionID, threadID string) error` — under `Pool.mu`: `ErrSessionNotFound` if id is not live; no-op if unchanged; else set `s.threadID` and `saveLocked`.
- `rekeyLocked` clears `sess.threadID`: a new pool id is a new conversation, so the rotated entry never inherits the old thread. All four re-key paths share it; for Claude sessions the field is always empty, so nothing changes for them.

### Codex runner (`cmd/pyry/codex_runner.go`)

- `codexRunnerConfig` gains `ThreadID string` (seeds `codexRunner.threadID`) and `OnThread func(sessionID, threadID string)`.
- The factory passes `cfg.ThreadID` and an `OnThread` that calls `cfg.RecordThread` when non-nil, logging a failure at Warn except `sessions.ErrSessionNotFound` (a stale report after a rotation — expected).
- `runOnce`: when the iteration STARTED a thread (it was given `""`) and still owns it (`freshSeq == seq`, the existing adopt guard), it reads the tag's id in the same `r.mu` section that adopts the thread, and calls `OnThread(id, threadID)` after unlocking. A resume reports nothing.
- `RestartFresh` moves `r.cfg.Tag.Rotate(newID)` inside its `r.mu` section, beside `freshSeq++`. That makes (seq, tag id) one atomic pair, so a report can never pair an old thread with a new id.

### Why the report cannot land on the wrong entry

The fresh restart is `RotateForNewSession` (re-key + clear under `Pool.mu`) then `RestartFresh` (seq bump + tag rotate under `r.mu`). An old-seq report either sees the old tag id — the pool no longer has it, `ErrSessionNotFound`, dropped — or fails the seq check and is not sent. A new-seq report carries the new id, which the pool already holds.

## Concurrency model

No new goroutines. `OnThread` runs on the runner's `Run` goroutine with no runner lock held (`r.mu` is a leaf and is never held across a pool call). `recordThread` takes `Pool.mu` and writes the file, the same cost `AdoptAnnouncedReset` already pays on a runner goroutine.

## Error handling

- `recordThread` on an unknown id → `ErrSessionNotFound`, nothing written.
- Save failure → returned; the runner logs it; the in-memory thread still resumes within this daemon's life.
- A stored thread Codex no longer knows: out of scope; the runner's existing resume-failure backoff stands.

## Testing strategy

`internal/sessions/pool_thread_test.go` (seeded registry, injected capture factory, running pool):
- A registry with a dormant codex entry carrying `thread_id` and a Claude entry round-trips byte-identical across load + save (the pre-key shape is already covered by `TestPool_Harness_LegacyRegistryRoundTripsByteIdentical`).
- Revive of that entry hands the factory `RunnerConfig.ThreadID` = the stored id.
- `RecordThread(id, t2)` writes `t2` to the entry on disk; `RotateForNewSession` → the new id's entry has no `thread_id`; `RecordThread(oldID, …)` → `ErrSessionNotFound`; `RecordThread(newID, t3)` → `t3` on disk.

`cmd/pyry/codex_thread_registry_test.go` (real pool, fake Codex, `make check`): seed a dormant codex entry; pool A with `harnessRunnerFactory(stub claude, newCodexRunnerFactory(fake))`; Revive + Activate; the started thread lands on disk; send a turn and read text off the sink; interrupt a held turn; stop the client and see the same thread resumed; `RotateForNewSession` + `RestartFresh` → a new thread on the new entry, the old id gone; stop pool A, start pool B on the same registry, revive the new id and see the bound thread equal the stored one.

Existing `codex_runner_test.go` runner tests keep passing unchanged (nil `OnThread`, empty `ThreadID`).

## Documentation handoff

None in the ticket. The documentation stage may record `thread_id` in the sessions registry schema section of `docs/knowledge/features/sessions-package.md` and the persistence note in the codexsup/Codex runner overview — pending for the documentation stage.

## Open questions

- Key name: `thread_id` (harness-neutral) rather than `codex_thread_id`; the harness key beside it says whose thread it is.
