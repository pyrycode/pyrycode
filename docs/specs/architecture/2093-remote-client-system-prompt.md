# #2093 — tell claude its replies are rendered by a remote client, not a terminal

Every interactive claude the session pool spawns is handed a daemon-written file
via `--append-system-prompt-file`. The file holds one constant paragraph stating
the architecture: no terminal, a separate client renders the replies, possibly on
another machine, and more than one client may attach.

## Files read

- `internal/sessions/pool.go` → `Pool.New` — the bootstrap argv composition site;
  writes the per-session `--settings` file, appends it to `base`, and carries the
  `built`-flag cleanup defer this ticket extends.
- `internal/sessions/pool.go` → `Pool.buildSession` — the second composition site,
  reached by mint and by revive. Same shape, reads `p.registryPath` instead of a
  `Config`.
- `internal/sessions/pool.go` → `Pool.Run` — holds the shutdown defer that removes
  the bootstrap's settings file. The daemon-scoped prompt file's removal joins it.
- `internal/sessions/pool.go` → `Pool.Remove` — removes the *per-session* settings
  file after `Evict` confirms the child is dead. The prompt file must **not** be
  removed here; it is shared by every live session.
- `internal/sessions/pool.go` → `Pool` — the struct that gains one unexported
  field holding the prompt path so `buildSession` can read it.
- `internal/sessions/settings.go` → `writeMCPSettings` — the atomic-write recipe
  (`CreateTemp` in the target dir → encode → fsync → close → rename), the
  `registryPath == ""` temp-dir branch, the 0600 rationale, and the "caller
  removes the file" contract. The new writer mirrors it structurally.
- `internal/sessions/session.go` → `Session.spawnArgs`, `Session.spawnBase` — why
  the flag must join `spawnBase`: `spawnArgs` recomposes as
  `spawnBase + claudeSettingsArgs(settings)` on every settings change, so a flag
  appended anywhere else is dropped on the first `Pool.UpdateSettings`.
- `internal/agentrun/streamrunner/args.go` → `BuildArgs` — the proven precedent:
  `--append-system-prompt-file` beside `--input-format stream-json`.
- `internal/streamsup/runner.go` → `Runner.Run` — logs `"spawning claude"` at Info
  with the composed argv under the `args` key. This is the live proof's read.
- `internal/sessions/pool_mcp_settings_test.go` → `waitArgv`, `stripMCPSettings`,
  `settingsArgPath`, `assertMCPSettingsFile` — #943's test pattern, including the
  strip helper that kept ~30 pre-existing exact-argv assertions green when a pair
  was added to `base`. This ticket adds a second strip in the same place.
- `internal/sessions/pool_update_settings_inband_test.go` → `installedArgv` — the
  second consumer of `stripMCPSettings`, on the in-band/restart path.
- `internal/sessions/pool_settings_test.go` → `recordingRunnerFactory`,
  `waitArgvRaw`, `spawnMintedWithSettings` — the argv-capture harness the unit
  tests drive.
- `internal/e2e/realclaude/interactive_stream_model_announced_test.go` — the live
  shape to mirror: a real daemon over a fake relay, one live turn driven from a
  fake phone, then `d.stderr.String()` read for the daemon's own log records.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` →
  `startPerConversationHarness`, `createConversationViaPhone`,
  `sealSendMessage`, `drainForAssistantReply` — the turn-driving helpers.
- `internal/e2e/realclaude/harness_daemon_test.go` → `spawnBootstrapDaemon`,
  `bootstrapDaemon` — the daemon handle whose `stderr` field captures the log.
- `internal/e2e/realclaude/teardown_liveness_probe_test.go` → `tdnClaudeCommand`,
  `tdnClaudeNeedle`, `tdnRunnerFromArgv` — the one reader the new flag touches.
  Verified below.
- `docs/knowledge/features/sessions-package.md` § the `--settings` file — #943's
  and #1518's discipline: the file lives in the daemon data dir where nothing
  reaps it, so every error return past the write must remove it.

## Context

An interactive claude is told nothing about where its words land, so it assumes
the surface is the machine it executes on. Observed 2026-09-04: a markdown
rendering fault in the desktop client was diagnosed as Claude Code's own
renderer, a terminal-width problem, and a library "not ours to switch" — all
about the wrong process.

This ships the half that cannot rot: a constant statement of the architecture,
true of every client that will ever connect, asserting nothing about what any
client can display. The client's name and version are #2148's.

No ADR: this adds no new pattern, it reuses #943's daemon-written-file discipline
verbatim.

## Design

### One daemon-scoped file, not one per session

The text is identical for every session, so a single file serves the whole
daemon. That is the only structural difference from #943's per-session
`--settings` file, and it moves the cleanup: written once in `Pool.New`, removed
once in `Pool.Run`'s shutdown defer, and **never** removed by `Pool.Remove` —
removing it there would delete the file out from under every other live session.

### `internal/sessions/systemprompt.go` (new)

Two package-level declarations, neither exported:

- `systemPromptText` — a `const string`. The whole contract of AC #2: pinned
  byte-for-byte by a test, so any future capability claim is a visible,
  deliberate diff. Ships the ticket's suggested wording, with a trailing newline.
- `writeSystemPrompt(registryPath, text string) (string, error)` — writes `text`
  and returns the absolute path.

  `text` is a **parameter, not a read of the const**. That is the join seam
  #2094 and #2148 both need: whichever lands second changes what its caller
  passes, not this function.

  Path selection mirrors `writeMCPSettings`'s two branches. `registryPath != ""`
  → `<dataDir>/system-prompt.txt`, a fixed name because the file is
  daemon-scoped, not id-derived; `MkdirAll(dataDir, 0700)` first, since on a cold
  start nothing has created the data dir yet. `registryPath == ""` (persistence
  disabled, the mode most of this package's tests use) → `os.CreateTemp` in
  `os.TempDir` with a random name.

  Deliberately **not** placed under `session-settings/`: `writeMCPSettings`'s doc
  states a `*.json` glob of that directory counts sessions exactly, and a
  daemon-scoped file has no business inside a per-session directory.

  Same atomic recipe as `writeMCPSettings` — `CreateTemp` in the target dir,
  write, fsync, close, rename — and the same 0600 mode. The confidentiality
  argument is nil (the payload is a public constant) but the integrity argument
  is #943's verbatim: anyone who could write this file controls text pyry hands
  claude as a system prompt.

### `internal/sessions/pool.go` (modified)

- `Pool` gains `systemPromptPath string` — read-only after `New`, so no lock.
- `Pool.New` calls `writeSystemPrompt` next to its `writeMCPSettings` call. A
  failure returns an error, which is fatal at daemon start (AC #3): a daemon that
  started anyway would silently spawn every session without the prompt. The
  existing `built`-flag defer gains the prompt file, so an error return between
  the write and the successful one takes it with them — in the data dir an
  orphan is permanent.
- Both composition sites append `"--append-system-prompt-file", <path>` to
  `base`, immediately after the `--settings` pair. On `base` and not in
  `claudeSettingsArgs` for exactly the reason the `--settings` pair is there:
  `Session.spawnArgs` recomposes `base + claudeSettingsArgs(settings)` on every
  settings change, so the flag survives an in-place settings change and a backoff
  restart (AC #1).
- `Pool.Run`'s existing shutdown defer removes the prompt file alongside the
  bootstrap's settings file (AC #3).
- `operatorBypass(base)` reads `base` for a pass-through escalation flag. Adding a
  path token cannot make it answer differently — verify by reading the predicate.

### What is deliberately not changed

`Pool.Remove` — see above. `claudeSettingsArgs` — the flag is not settings-derived.
`streamsup.Config.Args` and the `cmd/pyry` mapper — a flag added there is dropped
on the first settings change. `docs/protocol-mobile.md` — no wire field.

## Concurrency model

None added. The path is written once in `New`, before the `*Pool` literal exists,
and thereafter read-only — the same lifetime as `registryPath`. No goroutine, no
channel, no lock. Two daemons sharing a data dir would collide on the fixed name,
which is exactly the exposure the bootstrap's id-derived settings file already
has on a warm start; the control socket is what prevents it.

## Error handling

| Failure | Behaviour |
|---|---|
| `MkdirAll` / `CreateTemp` / write / fsync / close / rename fails | `writeSystemPrompt` returns a wrapped error; `Pool.New` fails; the daemon does not start |
| a later `Pool.New` error return (`newRunner`, `saveLocked`) | the `built` defer removes both the settings file and the prompt file |
| `Pool.Run` returns (ctx cancel, shutdown) | the defer removes the prompt file; a `SIGKILL` still leaks it, and the fixed name means the next start overwrites rather than accumulating |
| `os.Remove` fails at shutdown | ignored, as the settings-file removal beside it is |

## Testing strategy

Unit — `internal/sessions/pool_system_prompt_test.go`:

- the constant, pinned byte-for-byte (AC #2)
- the bootstrap's argv carries exactly one `--append-system-prompt-file <path>`
  pair, the file exists, and its bytes equal the constant (AC #1, #2)
- a minted session's argv carries the pair, at the **same path** as the
  bootstrap's — the daemon-scoped claim, which a per-session file would fail
- the pair survives a settings change that restarts the child, at an unchanged
  path (AC #1), mirroring the existing restart case in `pool_mcp_settings_test.go`
- the file is gone after `Pool.Run` returns (AC #3)
- a write failure makes `sessions.New` fail (AC #3). Forced by pre-creating a
  **directory** at the file's path, so the rename fails while `writeMCPSettings`,
  which writes into a different directory, still succeeds — the failure is
  attributed to the prompt writer and not to a shared cause.

Existing assertions: `waitArgv` and `installedArgv` gain a second strip pass, the
same move #943 made for `--settings`. That keeps every pre-existing exact-argv
assertion comparing only the flags it owns, and — because the strip fatals when
the pair is absent — turns each of them into a regression check that the flag is
present on that path.

Live (AC #4) — `internal/e2e/realclaude/interactive_system_prompt_test.go`,
behind `e2e_realclaude`: start the real daemon against a real claude, drive one
interactive turn to an assistant reply, then read the daemon's own
`"spawning claude"` record out of its captured stderr and assert the argv carries
the flag, and that the file it names is readable and non-empty. The turn's
completion is the load-bearing half — it is what proves claude accepts the flag
beside the interactive stream-json argv. Read the executed-test count, never the
exit code.

## Open questions

1. **Does `tdnClaudeCommand` degrade as the technical notes predict?** The notes
   say its `ps`-wide pin by `tdnClaudeNeedle` stops pinning a unique row once
   every interactive session carries the flag, that the probe is opt-in behind
   `PYRY_PROBE_TEARDOWN_LIVENESS`, and that the degraded value gates no verdict.
   Verify by reading those symbols rather than trusting the note. If it gates a
   verdict, that is a finding for the ticket comment — not an in-scope fix.
2. **Does the live suite already assert an exact interactive spawn argv
   anywhere?** A fixture pinning the interactive argv verbatim would redden. The
   notes name only `reachRunnerPathFromArgv` and `tdnRunnerFromArgv` as readers;
   confirm no equality assertion exists.
3. **Trailing newline in the constant.** Ships with one, as a text file should.
   The byte pin makes it explicit either way.
