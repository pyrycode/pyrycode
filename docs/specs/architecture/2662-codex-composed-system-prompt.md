# #2662 — Codex sessions start with the conversation's composed system prompt

## Files read

- `internal/codexsup/client.go` → `Config`, `Client.StartThread`, `Client.ResumeThread` — the two thread-open requests; their params are anonymous structs with `omitempty` fields.
- `internal/codexsup/client_test.go` → `TestRequestParamShapes` — the pin for thread-open params; today it checks `thread/resume` with `strings.Contains`.
- `internal/codexsup/schema_params_test.go` → the schema-validation test driving every client request through `paramsSchema`/`schemaCheck` against the 0.156.1 bundle.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `ThreadStartParams.developerInstructions`, `ThreadResumeParams.developerInstructions`: both `["string","null"]`.
- `cmd/pyry/codex_runner.go` → `newCodexRunnerFactory`, `codexTurnSettings`, `codexRunnerConfig`, `codexRunner.runOnce`, `codexRunner.readModels` (the failure-logs-never-fails pattern to mirror), `probeCodex` (opens no thread; stays untouched).
- `cmd/pyry/codex_runner_test.go` → `newTestCodexRunner`, `codexHarnessT.bound`, `readTurnLog`, `TestCodexRunner_LiveSettingsReachNextTurn` — the pool-plus-factory test shape the new end-to-end test mirrors.
- `cmd/pyry/main.go` → `startFreshRunner` — production `new_session` path: `BeginRotation` → rotate (which recomposes the file) → `RestartFresh`.
- `internal/sessions/pool.go` → `Pool.New`, `Pool.buildSession` argv composition: every session's base argv carries `--append-system-prompt-file <path>`, Codex sessions included, and the path is fixed for the session's life.
- `internal/sessions/systemprompt.go` → `Pool.writeComposedPrompt`, `Pool.refreshSystemPromptForRotation`, `Pool.refreshSystemPrompt`, `Pool.handoffNoteFor` — the existing compose path; this ticket adds no second one.
- `internal/sessions/pool_rotate_system_prompt_test.go` → the pool-level rotation pattern (`reg.SetSystemPrompt` then `RotateForNewSession`) the cmd/pyry test copies at the Codex level.
- `internal/e2e/internal/fakecodex/main.go` → `threadStart`, `threadResume`, `server.logTurn`, `FAKECODEX_TURN_LOG` — the log the new thread log mirrors.
- Size caps: `conversations.MaxSystemPromptBytes` (8 KiB), `sessions.MaxHandoffNoteBytes` (16 KiB) — a composed file sits far below the 256 KiB read bound.

## Context

A Claude child reads the pool's composed prompt file through `--append-system-prompt-file`. The Codex runner ignores that argv pair, so neither `set_system_prompt` nor a handoff note reaches a Codex thread, while `Pool.SystemPromptFor` reports the prompt as applied. Codex 0.156.1 accepts `developerInstructions` on `thread/start` and `thread/resume`. The pool already rewrites the file before every spawn that should see a new composition (rotation, re-activate), so the fix is purely on the reading side: read the file on every spawn and send it on the thread open.

No ADR needed.

## Design

### `internal/codexsup`

Add one field to `Config`:

```go
// DeveloperInstructions, when non-empty, is sent as developerInstructions on
// every thread/start and thread/resume this client sends. Empty omits the key.
DeveloperInstructions string
```

`StartThread` and `ResumeThread` each gain a `DeveloperInstructions string \`json:"developerInstructions,omitempty"\`` field in their params struct, filled from the config. Signatures are unchanged, so the ~12 test callers do not move. Carrying it on `Config` matches `Dir`, which is already a per-client value both thread opens read; `runOnce` builds a new client per spawn, so a per-spawn read lands on a per-spawn config.

Empty string → key omitted → params byte-identical to today.

### `cmd/pyry/codex_runner.go`

- `codexPromptFile(args []string) string` — the last `--append-system-prompt-file` value, spaced or `=` form, beside `codexTurnSettings` and in its style. Empty when absent.
- `codexRunnerConfig.PromptFile string` — set by the factory from `cfg.ClaudeArgs`. Fixed for the runner's life: `Restart(args)`/`SetSpawnArgs` do not touch it (the pool's path is on the base argv and never changes).
- `codexMaxPrompt = 256 << 10`.
- `(*codexRunner).readPrompt() string` — empty `PromptFile` → `""` silently. Otherwise opens the file non-blocking (`O_RDONLY|O_NONBLOCK`), refuses anything `Stat` on the open file does not report as a regular file, and reads at most `codexMaxPrompt+1` bytes. On an open/read error, or a file over the bound, it logs one Warn (`"codex: system prompt not read"`, `session`, and `err` or a size-only reason) and returns `""`. Never logs content. A missing file is logged too: the pool writes the file before building the runner, so absence is a fault worth one line. An empty file returns `""` silently.
- `runOnce` calls `readPrompt()` before `codexsup.Start` and sets `Config.DeveloperInstructions`. It never fails the spawn on a read problem.

Over-bound is treated as a read failure (send nothing) rather than truncating: a cut could split the fenced handoff-note section and leave an unterminated fence in the instructions.

### Fake Codex

New env `FAKECODEX_THREAD_LOG`: each `thread/start` and `thread/resume` appends one line `{"method":"<method>","params":<raw params>}` before the response, like `FAKECODEX_TURN_LOG`. `threadParams` needs no new field; the raw params are logged. The shared append becomes `appendLog(path, line)` used by both logs. Header doc updated.

### Data flow

```
Pool.buildSession ── argv: --append-system-prompt-file P ──► factory ──► codexRunnerConfig.PromptFile = P
Pool.writeComposedPrompt / refreshSystemPrompt / refreshSystemPromptForRotation ── writes P (existing)
codexRunner.runOnce ── readPrompt(P) ──► codexsup.Config.DeveloperInstructions ──► thread/start | thread/resume
```

## Concurrency model

No new goroutines. `readPrompt` runs on the `Run` goroutine inside `runOnce`, before the client starts, reading only `r.cfg.PromptFile` (immutable after construction) and `r.log`. The file is written by the pool through a rename (`writeSystemPromptFile`), so a read sees a complete old or new composition, never a torn one. The read is ordered after the pool's rewrite on every path the ticket names: rotation rewrites before `RestartFresh` cancels the iteration; re-activate rewrites before the runner's `Run` starts.

## Error handling

| Case | Behaviour |
|---|---|
| No `--append-system-prompt-file` in argv | no read, no log, no key |
| File missing / unreadable | Warn without content, no key, spawn proceeds |
| File > 256 KiB | Warn with the bound, no key, spawn proceeds |
| Not a regular file (FIFO, directory, device) | Warn, no key, spawn proceeds; the non-blocking open means a FIFO cannot hang the spawn |
| File empty | no key, no log |

The error string from `os.Open` names the path; the path is already public (it is in the argv record, as `writeComposedPrompt`'s doc says). No prompt or note bytes reach any log line.

## Testing strategy

`internal/codexsup`:
- `TestRequestParamShapes`: tighten the existing `thread/resume` check to an exact literal (`{"threadId":"th-1","excludeTurns":true}`) — the pin for the omitted case — and add a `thread/start` exact literal (`{}`).
- New `TestThreadOpenInstructions`: a client with `DeveloperInstructions` set sends it on both `thread/start` and `thread/resume` (exact params literals).
- Schema-params test: the client there is built with a non-empty `DeveloperInstructions`, so both thread-open params pass the bundle's schema with the key present. (The omitted form is a subset and needs no second pass.)

`cmd/pyry`:
- `TestCodexPromptFile`: table over spaced, `=`, last-wins, absent, dangling flag.
- `TestCodexRunner_PromptFileReadEachSpawn` (runner + fake, no pool): thread/start carries the file bytes; the file is rewritten and `Restart` respawns → thread/resume carries the new bytes; the file is emptied → next open has no key; the file is removed → no key and the spawn still binds; an oversize file → no key. Log captured and asserted free of prompt bytes.
- `TestCodexRunner_ConversationPromptReachesThread` (pool + production factory + conversations registry): a Codex session minted on a conversation whose prompt was set before the first message opens `thread/start` with instructions equal to the file and containing the prompt; after `reg.SetSystemPrompt` and `WriteHandoffNote`, `startFreshRunner(… pool.RotateForNewSession …)` → the fresh `thread/start` carries the new prompt and the note; after evict and re-activate with a third prompt, `thread/resume` carries it. The pool's logger is captured and asserted free of every prompt and note string.
- Fake Codex: extend its own test to check the thread log records both methods.

## Open questions

- Does `Session.Evict` + `Pool.Activate` on a Codex session go through a fresh runner Run in the cmd/pyry test harness, and does the runner resume the held thread? Expected yes (`codexRunner.threadID` survives in memory); resolve when writing the test.

## Documentation handoff

Pending for the documentation stage:
- `docs/knowledge/features/codexsup-package.md` § Production wiring: the Codex runner sends the composed prompt as `developerInstructions` on every thread open.
- `docs/protocol-mobile.md` § Setting a conversation's system prompt, and the `set_system_prompt` table row: applies to Codex conversations too, from the next session start or resume.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No new boundary. The bytes sent are the file `Pool.writeComposedPrompt` composes for a Claude child: the operator prompt (validated and capped at 8 KiB by `conversations.Registry.SetSystemPrompt`), admitted client names (`admittedClients`), and the handoff note framed by the fenced section. Codex gets them at the same authority Claude does (developer instructions ≈ appended system prompt). The runner never parses them; it passes an opaque string to `json.Marshal`, which escapes it, over the app-server's stdin. No argv or shell sees it.
- [Tokens, secrets] No findings — no credentials are read, created or sent. The composed prompt holds no secret.
- [File operations] SHOULD FIX, taken into the design: the path comes only from the pool's own argv (`buildSession`, `Pool.New`), never from a remote frame, so traversal does not apply. A FIFO planted at the path would block a plain `open(2)` forever on the `Run` goroutine, outside any context; `readPrompt` opens `O_NONBLOCK` and refuses a non-regular file via `Stat` on the open descriptor (no Lstat-then-open window). A symlink at the path is followed; planting one needs the daemon's uid, which can already rewrite the file outright, the same argument `Pool.handoffNoteFor` makes. Size is bounded by a `LimitReader` at `codexMaxPrompt` + 1; over the bound sends nothing rather than a truncated fence.
- [Subprocess] No findings — the Codex argv and environment are unchanged; instructions travel as a JSON-RPC param.
- [Crypto] N/A — no randomness or primitives involved.
- [Network & I/O] Input bounded at 256 KiB (above); the file is local and read once per spawn.
- [Logs] MUST-NOT-log: prompt text, note text, client names. `readPrompt` logs only the session id and the error (which names the path, already public in the argv record) or a size reason. The pool-level test captures the daemon logger and asserts no prompt or note string appears; the runner-level test does the same for the runner's logger. The fake's `FAKECODEX_THREAD_LOG` writes content, test-only, at 0600.
- [Concurrency] No findings — no new goroutine or lock; `PromptFile` is immutable after construction; the pool's writes are renames, so a read sees a whole composition.
- [Threat model] `docs/protocol-mobile.md` § Security model: `set_system_prompt` from a paired device now also shapes Codex conversations, which is the feature; the per-prompt cap and UTF-8 check already bound it. OUT OF SCOPE / accepted: Codex may persist thread instructions in its rollout files under the daemon-owned `CODEX_HOME`, which `prepareCodexHome` keeps at 0700; no new exposure beyond the daemon's uid.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25
