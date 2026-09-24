# #2620 — Codex sessions through the pool on a supervised app-server runner

## Files read

- `internal/sessions/runner.go` → `Runner`, `RunnerFactory` — the thirteen-method seam the Codex runner satisfies.
- `internal/sessions/runnerstate.go` → `RunnerConfig` (`SessionID`, `WorkDir`, `ClaudeArgs`, `Harness`, `Logger`), `State`/`Phase` — what the factory receives and what `State()` reports.
- `internal/sessions/session.go` → `runActive` — eviction is `BeginTeardown` then a cancel of `Run`'s ctx; a later activation calls `Run` again **on the same runner**, so the Codex thread id must live on the runner, not in `Run`'s frame. `claudeSettingsArgs` — `--model <id>` / `--effort <level>` pairs.
- `internal/sessions/pool.go` → `deliverSettingsInBand` — calls `SetSpawnArgs`, `SetModel`, writes `/effort <level>` as a user turn, and `SetPermissionMode`.
- `cmd/pyry/main.go` → `harnessRunnerFactory`, `selectInteractiveRunner`, `runSupervisor` (`-pyry-claude`), `pyryFlagValues`, `resolveInstanceDirPath`, the usage text.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` — the tag / `sinkForTag` / `exitForTag` binding the Codex factory mirrors.
- `cmd/pyry/stream_turn_drain.go` → `streamTurnSink.sinkForTag`, `exitForTag`, `newStreamSessionTag` / `Rotate`.
- `internal/streamsup/runner.go` → `Run`, `BeginTeardown`, `BeginRotation`, `RestartFresh`, `turnTargetWithGate`, `WriteUserTurn` — the supervision semantics copied in shape (gates refuse with the retryable `streamsup.ErrNoLiveChild`, rotation gate released only by a spawn newer than the arm).
- `internal/streamsup/envelope.go` → `WriteTurn` — the `turncommit` claim before any write.
- `internal/codexsup/client.go` → `Start`, `Config`, `StartThread`, `ResumeThread`, `StartTurn`, `Interrupt`, `Stop`, `Done`, `Err`, `ErrExited`.
- `internal/codexsup/translate.go` → `NewTranslator`, `SetModel`, `Translate` (not safe for concurrent use).
- `internal/codexsup/serverrequest.go` → `declineFor` — nil `OnServerRequest` declines every request; no second refusal path.
- `internal/e2e/internal/fakecodex/main.go` — `[fakecodex:hold]`, `[fakecodex:approval]`; `thread/resume` returns the id given, `thread/start` mints a new one.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `v2.Config` (`approval_policy`, `sandbox_mode`, `approvals_reviewer`), `AskForApproval`, `SandboxMode`, `ApprovalsReviewer` — posture keys verified at 0.156.1.
- `docs/knowledge/features/codexsup-package.md` — the translator is caller-goroutine only; `call` can report `ErrExited` for a request that ran.
- Vault "Codex CLI Support - Research" § S0 / § Documentation check — kebab-case values, `approvals_reviewer = "user"`, isolation is mandatory.

## Context

Slice S3a of Codex support. `harnessRunnerFactory` refuses `codex` today. This ticket adds a Codex `sessions.Runner` in `cmd/pyry` that supervises one `codexsup.Client` at a time and routes the `codex` harness to it. No client can create a Codex session yet, so the running daemon is unchanged.

**Size.** The refiner's estimate is ~950 lines, over the 800 ceiling on purpose: the daemon-owned `CODEX_HOME` has one consumer, this runner, so the floor rule keeps it here. Three production files (two new, `main.go` modified). Overlap: `origin/feature/2569` also edits `runSupervisor` in `main.go`, a different block; my edits there are additive.

**ADR candidate.** "Codex runs in a daemon-owned `CODEX_HOME` with a daemon-written read-only config; every server request is declined until the approvals ticket" is worth a decision record; the documentation phase writes it.

## Design

### `cmd/pyry/codex_home.go`

- `codexHomePath(instanceDir string) string` → `<instanceDir>/codex-home`.
- `prepareCodexHome(dir string) error` — `MkdirAll(dir, 0o700)`, then writes `config.toml` (0600, temp file + rename) with exactly:
  - `approval_policy = "on-request"`
  - `sandbox_mode = "read-only"`
  - `approvals_reviewer = "user"`
  It touches no other file: the operator's sign-in (`auth.json`, created by `CODEX_HOME=<dir> codex login`) is never read, copied, linked or removed. Called by the Codex factory on every construction, so the daemon's posture is restored even if the file was edited. Nothing is created at daemon start.

### `cmd/pyry/codex_runner.go`

- `codexHarness{bin, home string; sink *streamTurnSink}` — daemon-wide values; `newCodexRunnerFactory(h codexHarness) sessions.RunnerFactory`.
- Factory: `prepareCodexHome(h.home)`; resolve `WorkDir` (empty → `os.Getwd`, since `codexsup` requires a dir); mint `newStreamSessionTag(cfg.SessionID)`; bind `h.sink.sinkForTag(tag.ID)` and `h.sink.exitForTag(tag.ID)`; read model/effort with `codexTurnSettings(cfg.ClaudeArgs)`; return `newCodexRunner(codexRunnerConfig{...})`.
- `codexRunnerConfig{Binary, Home, Dir string; Tag *streamSessionTag; Sink func(turnevent.Event); OnExit func(); Model, Effort string; Backoff time.Duration; Log *slog.Logger}` — the pure constructor input, so tests build a runner against the fake without a `streamTurnSink`.
- `codexTurnSettings(args []string) (model, effort string)` — last `--model`/`--effort` value, space or `=` form; every other flag ignored.
- `*codexRunner` implements `sessions.Runner` (compile-time assertion). State under one leaf mutex `mu`: `client`, `threadID`, `turnID`, `fresh`, `freshSeq`, `tearingDown`, `rotating`/`rotateGen`/`armFreshSeq`, `iterCancel`, `model`, `effort`, `state`. `restartCh` buffered(1). The translator is per spawn and guarded by its own `trMu`.

Method contracts:

| Method | Behaviour |
|---|---|
| `Run(ctx)` | Supervise loop, below. Returns `ctx.Err()` on cancel. Callable again after return (eviction → activation). |
| `WriteUserTurn(ctx, _, payload)` | Refuses with `streamsup.ErrNoLiveChild` (retryable, nothing sent) while rotating, tearing down, or with no bound client. Claims the `turncommit` gate (`turncommit.ErrDropped` on a deny). Then `StartTurn{Text: payload, Model, Effort}`. Never logs payload or conversation id. |
| `Interrupt()` | `ErrNoLiveChild` with no client; nil with no running turn; else `client.Interrupt(turnID)` under a 5 s bound. |
| `Restart(args)` | Installs model/effort from args, then ends the live process so `Run` respawns at once (no backoff) and **resumes** the thread. Posture untouched: it lives in the config file. |
| `SetSpawnArgs(args)` | Installs model/effort only; the live process is left alone. |
| `SetModel(m)` | Stores the model; the next turn carries it. Returns nil because the change does take effect then. |
| `SetSpawnPermissionMode(_)` | No-op: the posture is the daemon's read-only config, not the stored mode. |
| `SetPermissionMode(_)` | Always an error (`codex sessions keep the daemon's read-only posture`): nothing changes live. |
| `BeginTeardown()` | Arms the teardown gate; released when the next client binds. |
| `BeginRotation()` | Arms the rotation gate, snapshotting `freshSeq`; released only by a bind whose spawn saw a newer `freshSeq`; the returned abort clears it if still the same arm. |
| `RestartFresh(newID)` | Rotates the tag to `newID` (the pool's stable key moves), drops the held thread id, marks the next spawn fresh, bumps `freshSeq`, ends the live process. Empty id is ignored. |
| `WaitForPTY` | nil. |
| `State()` | Phase starting/running/backoff/stopped, restart count, uptime, backoff. `ChildPID` stays 0: `codexsup.Client` exposes no pid. |

### Supervise loop (`Run`)

Each iteration: snapshot `fresh`, `threadID`, `freshSeq` and publish `iterCancel` in one `mu` section → `codexsup.Start(iterCtx, Config{Binary, Dir, CodexHome: Home, OnNotification})` with `OnServerRequest` nil → `ResumeThread(threadID)` when a thread is held and the spawn is not fresh, else `StartThread` (record the minted id only if `freshSeq` is unchanged) → bind the client (clears teardown, releases a qualifying rotation arm, Phase running) → wait on `client.Done()` or `iterCtx.Done()` → unbind (client and turn id cleared) → `Stop` the client if we cancelled → `OnExit()` on every path, above the shutdown return, like `streamsup`'s `OnChildExit` → return on parent cancel; relaunch at once on a drained restart hint; else exponential backoff (`Backoff` doubling to 30 s, reset after a minute's uptime), which a restart hint cuts short.

A failed thread open counts as a crashed iteration (the client is stopped, backoff applies). A resume keeps the thread id across a kill, a crash and an eviction.

### Notifications

The per-spawn `OnNotification` closure records `turn/started`'s `turn.id` as the running turn and clears it on `turn/completed` (read-loop ordered, so no race with `StartTurn`'s return), then feeds `Translate` under `trMu` and forwards each event to `Sink`. It never blocks: `sinkForTag` is non-blocking.

### Wiring (`main.go`)

- `-pyry-codex` flag, default `codex`, beside `-pyry-claude`; added to `pyryFlagValues` and the usage text.
- `harnessRunnerFactory(claude, codex sessions.RunnerFactory)` — `codex` case calls the codex factory; unknown harnesses stay refused.
- `selectInteractiveRunner` gains a `codexHarness` argument and sets its `sink` to the one it builds, so Codex events and exits reach the same drain as Claude's.
- `runSupervisor` passes `codexHarness{bin: *codexBin, home: codexHomePath(resolveInstanceDirPath(*name))}`.

## Concurrency model

Goroutines: `Run`'s (one per activation), `codexsup`'s read loop per client (calls `OnNotification`), and callers of the methods above. `mu` is a leaf; no Codex call is made while holding it. `trMu` guards the per-spawn translator between the read loop and `WriteUserTurn`'s `SetModel`. Shutdown: parent cancel → iteration cancel → `client.Stop` (stdin close, SIGTERM then SIGKILL after `codexsup`'s grace) → `Run` returns. No goroutine outlives `Run`.

## Error handling

- Missing binary / handshake failure / thread open failure → logged, backoff, retry; `Run` never returns for them (the pool's crash contract).
- `WriteUserTurn` after the process died mid-call → the wrapped `codexsup` error (may be `ErrExited`); the gate refusals stay `ErrNoLiveChild`.
- `prepareCodexHome` failure → construction error; the pool leaves the session dormant.
- Server requests → `codexsup`'s default decline. An approval completes its item as declined.

## Testing strategy

`cmd/pyry/codex_runner_test.go`, building `fakecodex` once per test binary (`sync.Once`, like `codexsup`'s `TestMain`):

- Turn: a written user turn produces a `TextChunk` and a `TurnEnd` through `Sink`.
- Interrupt: a `[fakecodex:hold]` turn ends with a cancelled `TurnEnd` after `Interrupt`.
- Crash: stopping the live client from the test (an exit `Run` did not order) leads to a new client on the **same** thread id, `OnExit` fired, and a later turn works.
- Eviction: `BeginTeardown` + cancel → `Run` returns; `WriteUserTurn` refuses with `ErrNoLiveChild`; `Run` again → same thread id.
- `RestartFresh("s-2")` → a different thread id and the tag reads `s-2`; `BeginRotation` refuses turns until that fresh bind.
- Approval: `[fakecodex:approval]` → a `ToolUpdate` with `ResultDetail` `declined`.
- `codexTurnSettings` table test; `SetPermissionMode` errors; `SetModel` returns nil and is carried on the next turn's input.
- `prepareCodexHome`: dir 0700, `config.toml` 0600 with the three keys, a pre-existing `auth.json` left byte-identical, re-run overwrites an edited config.
- `harness_runner_test.go`: `codex` reaches the codex factory with the config; unknown harnesses still refused.

## Open questions

1. `approval_policy = "on-request"` lets the model decide whether to ask after a sandbox denial; the granular policy asks every time but its thread-param form needs `experimentalApi`. Is granular in `config.toml` accepted without the capability? Deferred to the approvals ticket (#2587); on-request is what this ticket's AC names.
2. `deliverSettingsInBand` sends an effort change as a `/effort <level>` user turn, which would reach Codex as text. No client can create a Codex session, and the settings mapping is #2586; noted, not handled here.

## Documentation handoff

Pending for the documentation stage: fold the Codex runner (supervise loop, gates, daemon-owned `CODEX_HOME` and its config) into `docs/knowledge/features/codexsup-package.md` (its "Nothing in `cmd/pyry` imports it yet" lines are now stale) and consider the ADR named in Context.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. Everything the app-server sends is untrusted and crosses at one place, the per-spawn `OnNotification` closure: its only decode of its own is `turn/started`'s `turn.id`, which is stored and echoed back only to the same peer as `turn/interrupt`'s `turnId`, never logged or forwarded. All other bytes go through `codexsup.Translator`, which already bounds and classifies them. The thread id Codex mints is held in memory and sent back only to Codex.
- [Tokens, secrets, credentials] No findings for this design. No credential is read, copied, linked or written. `prepareCodexHome` writes `config.toml` only, and a test proves an existing `auth.json` stays byte-identical. `CODEX_HOME` is the only variable the runner sets. OUT OF SCOPE: `codexsup.Start` inherits the daemon's full environment, so an `OPENAI_API_KEY` in launchd's environment would reach Codex. That is codexsup's policy, and #2621 owns the sign-in check at construction.
- [File operations] SHOULD FIX, done in Phase B. `MkdirAll` does not tighten an existing directory, so `prepareCodexHome` also chmods the home to 0700. `config.toml` goes through `os.CreateTemp` in the same directory, which opens with O_EXCL, then 0600 and a rename. The rename replaces a planted symlink rather than following it. No path contains caller input: the path is `resolveInstanceDirPath(<sanitized name>)` + a constant.
- [Subprocess] No findings. The binary is the operator's `-pyry-codex` flag and the argv is the constant `app-server`. There is no shell. User text travels inside the JSON-RPC body, never in argv. Teardown is `codexsup.Stop`: stdin close, then SIGTERM, then SIGKILL after its grace. OUT OF SCOPE: unlike streamsup, nothing reaps descendant process groups, and whether app-server orphans helpers on SIGTERM is unmeasured (#2621 / a live check).
- [Cryptographic primitives] No findings. The runner uses no cryptographic primitives.
- [Network & I/O] No findings. There is no network I/O in the runner. The stdio framing and its line cap are `internal/acp`'s and are inherited unchanged.
- [Errors, logs] SHOULD FIX, done in Phase B. Log records carry the session id and the error only, never the payload, conversation id, thread id, turn id or model value. `SetPermissionMode`'s error does not echo the mode.
- [Concurrency] No findings. `mu` and `trMu` are each leaves and are never held together or across a Codex call. `Run` is the only goroutine the runner adds, and it exits on the parent cancel. The client's read loop is drained by `codexsup` before `Done` closes, so two translators never run at once for one runner.
- [Threat model: posture] No MUST FIX. The read-only posture has one source, the daemon-written config, and no runner method can loosen it. `Restart`, `SetSpawnArgs` and `SetSpawnPermissionMode` never touch the config, and `SetPermissionMode` always refuses. Every server request takes `codexsup`'s default decline, and no path accepts. OUT OF SCOPE: system-wide or managed Codex config (for example under `/etc/codex`) can still apply beside `CODEX_HOME`. Detecting a looser effective posture at construction belongs with #2621's construction checks. Asserting the posture again on `thread/start` params would need a `codexsup` change, and no failure has been observed that justifies it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
