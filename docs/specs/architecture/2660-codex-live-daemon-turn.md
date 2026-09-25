# #2660 — A live Codex turn and approval round-trip through the daemon

## Files read

- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `startPerConversationHarnessSeeded`, `createConversationViaPhone`, `drainForReply`, `sealEnvelope` — the harness shape the new one mirrors; `createConversationViaPhone` cannot carry an agent, so the create is sealed inline.
- `internal/e2e/realclaude/harness_daemon_test.go` → `driveHandshakeInteractive`, `buildHelloEarlyInteractive`, `spawnBootstrapDaemon`, `seedBootstrapRegistry`, `bootstrapDaemon` — the handshake advertises only `interactive`; the spawn puts extra args after `--`, so `-pyry-codex` cannot ride it.
- `internal/e2e/realclaude/harness_modal_test.go` → `startModalResolutionHarness`, `spawnPermissionDaemon` — `AllowRemotePermissions: true` precedent and the spawn shape without `--dangerously-skip-permissions`.
- `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` → `denyModalsUntilIdle`, `requireTriggerFileAbsent` — answering every retry modal until the turn resolves, and the remote-source attribution check.
- `internal/e2e/realclaude/interactive_stream_running_permission_settings_test.go` → `requestSessionSettings` — how a client learns a conversation's session id before `set_session_settings`.
- `internal/e2e/realclaude/fixtures.go` → `WithWorktreeAuthenticated` — pins HOME to a temp dir; the operator's real HOME must be captured before it runs.
- `cmd/pyry/codex_approval_test.go` → `TestCodexApprovalLive` — env var names, the `touch` prompt shape, the 3-minute turn budget.
- `cmd/pyry/codex_runner.go` → `codexMinVersion`, `checkCodexVersion`, `newCodexRunnerFactory`, `probeCodex` — version floor and the factory's `prepareCodexHome` on every construction.
- `cmd/pyry/codex_home.go` → `codexHomePath`, `prepareCodexHome` — `<instance dir>/codex-home`; MkdirAll/Chmod/rename follow a symlinked directory, so the link is honoured.
- `cmd/pyry/main.go` → `resolveInstanceDirPath`, the `pyry-codex` flag, the `codexHarness{...}` composition — the home is `~/.pyry/test/codex-home` under the harness HOME.
- `cmd/pyry/codex_settings.go` → `codexTurnOverrides` — the posture table.
- `internal/sessions/session.go` → `canonicalSettings`, `canonicalPermissionMode` — an unset mode is stored as `default`.
- `internal/relay/handlers/create_conversation.go` → `CreateConversation` — codex needs a `multi_agent` conn.
- `internal/protocol` → `CreateConversationPayload.Agent`, `ConversationCreatedPayload.Agent`, `SetSessionSettingsPayload`, `SessionSettingsPayload`, `ModalShownPayload`, `ModalAnswerPayload`, `ModalDismissedPayload`, `TurnEndPayload`, `CapabilityMultiAgent`, `AgentCodex`.

## Context

Nothing runs a real Codex turn through the whole daemon. This adds one live test that does it the way a multi-agent app will: Noise v2 handshake advertising `multi_agent`, `create_conversation{agent: codex}`, `set_session_settings` for model and effort, a plain turn, then a declined and an accepted file write via the permission modal.

**Predicted finding (static reading, must be confirmed by the operator run).** The ticket says an unset `permission_mode` makes `codexTurnOverrides` send granular approval with a read-only sandbox. The code disagrees: `buildSessionAs` runs `canonicalSettings`, and `canonicalPermissionMode("")` returns `default`. The runner's initial mode is that value (`newCodexRunnerFactory` passes `cfg.PermissionMode`), and `codexTurnOverrides` maps `default` to `workspaceWrite`. A `touch` inside the workspace then runs without a modal. Per the ticket, the assertion is not weakened: the test is built as specified, and the decline turn is expected to fail with a diagnostic naming this. The finding is posted on the ticket. Whether `default` should map to workspace-write for Codex is a product decision outside this ticket; no production code changes here.

## Design

One new test file, one additive harness change. Both under the `e2e_realclaude` build tag, so `make e2e-realclaude` (and `make preship`) compile it with no Makefile change; it skips wherever Codex is not configured.

### Harness change — `harness_daemon_test.go`

- `driveHandshake(t, phone, pubKey, token, caps ...string) (send, recv *noise.CipherState)` — today's `driveHandshakeInteractive` body with the hello's capability list parameterised; it fails unless `hello_ack` grants every requested capability.
- `driveHandshakeInteractive` becomes a one-line call to `driveHandshake(..., protocol.CapabilityInteractive)`. Its 18 callers are unchanged.
- `buildHelloEarlyInteractive(t, token)` gains a `caps []string` parameter; its only caller is `driveHandshake`.

### New file — `internal/e2e/realclaude/codex_conversation_live_test.go`

- `TestCodexConversationLive(t)` — the scenario below.
- `requireCodexCapture(t) (bin, home string)` — skip gates and the operator-home guard, run before any HOME pinning:
  - skip naming the variable when `PYRY_CODEX_CAPTURE_BIN` or `PYRY_CODEX_CAPTURE_HOME` is unset;
  - runs `<bin> --version` with `CODEX_HOME` set to an empty temp dir (so the version read cannot touch the sign-in), parses the last field, and skips naming both versions when it is below `codexMinVersionForTest = "0.156.1"` (transcribed from `codexMinVersion`; package `main` cannot be imported). A version the parser cannot read, or a binary that fails to run, is a `t.Fatalf` — the operator configured it, so a skip would hide a broken setup;
  - resolves the capture home with `filepath.EvalSymlinks` and fails if it is inside the operator's real `$HOME/.pyry` (also resolved), so the daemon's `prepareCodexHome` can never rewrite a production instance's Codex home.
- `startCodexConversationHarness(t, bin, captureHome) *perConvHarness` — `startPerConversationHarnessSeeded` with four deltas: pairs with `AllowRemotePermissions: true`; creates `<home>/.pyry/test` (0700) and symlinks `codex-home` inside it to `captureHome` before the daemon starts; spawns through `spawnCodexDaemon`; handshakes with `driveHandshake(..., CapabilityInteractive, CapabilityMultiAgent)`. Uses `WithWorktreeAuthenticated` like every daemon test in the package, because the daemon still supervises a Claude bootstrap session.
- `spawnCodexDaemon(t, home, workdir, claudeBin, codexBin, relayURL) *bootstrapDaemon` — `spawnPermissionDaemon`'s argv plus `-pyry-codex=<bin>` before `--`, and only `--model haiku` after it (no bypass flag, so `OperatorBypass` stays false and cannot loosen the Codex posture).
- `runCodexTurn(t, h, convID, reqID, text, answer turnevent.PermissionOptionKind, budget) codexTurn` — seals a `send_message`, then drains in receive order until `turn_end` for `convID`. On each `modal_shown` for `convID` it asserts class `permission`, that `answer` is among the offered option ids, and seals a `modal_answer` with that option. On `modal_dismissed` for a modal it answered it requires `Source == "remote"`. Records whether a non-empty `assistant_delta` for `convID` arrived and how many modals it answered. An `error` envelope is fatal. Returns `codexTurn{modals int, sawDelta bool}`. The modal answer request ids continue from `reqID`; the function returns the next free id in the result.

### Scenario (`TestCodexConversationLive`)

1. `requireCodexCapture`, then `startCodexConversationHarness`.
2. `create_conversation{agent: "codex"}`; the `conversation_created` reply's `Agent` must be `codex`.
3. `request_session_settings{conversation_id}` → session id.
4. `set_session_settings{session_id, model: "gpt-6-luna", effort: "low"}` → wait for `session_settings_updated` correlated to it.
5. `request_session_settings` again; model must read `gpt-6-luna` and effort `low`. This is the deterministic guard that no turn runs on the account default model.
6. Turn 1, a one-word reply: `sawDelta` must be true (turn end is implied by the return). Any modal is rejected.
7. Turn 2, `touch declined-<nonce>.txt` answered `reject_once`: `modals >= 1`, and `<workdir>/declined-<nonce>.txt` absent after turn end. If `turn_end` arrives with zero modals, the failure message names the file's presence and the `default` → workspace-write mapping.
8. Turn 3, `touch accepted-<nonce>.txt` answered `allow_once`: `modals >= 1`, and the file present.

Codex's words are never asserted. Order of checks within a turn is fixed by `turn_end`, so file checks run after the tool phase is over.

## Concurrency model

Single test goroutine drives the wire; frames are decrypted strictly in arrival order to keep the Noise receive nonce in step (the package-wide rule). The daemon runs as a child process torn down by `bootstrapDaemon.stop` in `t.Cleanup`. No `t.Parallel` (`WithWorktreeAuthenticated` uses `t.Setenv`, and the one sign-in must not be shared by concurrent runs).

## Error handling

- Missing env / old Codex → `t.Skip` naming what is missing.
- Unrunnable binary, unparseable version, capture home inside real `~/.pyry` → `t.Fatalf`.
- Each wait is bounded (`codexTurnBudget = 3 * time.Minute`, settings replies 30s); a timeout is a `t.Fatalf` naming the step and including nothing from the sign-in.
- The symlink is removed with the temp HOME by `t.TempDir` cleanup; `os.RemoveAll` on the HOME removes the link, not its target.

## Testing strategy

This is a live test; it cannot RED/GREEN in the dispatch environment (no Codex sign-in). Builder-side proof: `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` and `go test -tags e2e_realclaude -run TestCodexConversationLive ./internal/e2e/realclaude/` with the env unset, which must report a skip naming the variables. The operator gate (AC 5) is the live run: `PYRY_CODEX_CAPTURE_BIN=… PYRY_CODEX_CAPTURE_HOME=… go test -tags e2e_realclaude -count=1 -run TestCodexConversationLive -v ./internal/e2e/realclaude/`, with Claude credentials exported as for `make e2e-realclaude`.

## Open questions

- Does a Codex modal list `allow_once` / `reject_once` as option ids? The surface is shared with Claude's, so expected yes; `runCodexTurn` asserts it rather than assuming.
- Is `modal_dismissed` emitted for a Codex modal? Checked only when it arrives; its absence is not a failure.

## Documentation handoff

The ticket carries no documentation requirement. Suggested, pending for the documentation stage: `docs/knowledge/features/e2e-realclaude.md` could name `TestCodexConversationLive`, its two env vars and the one-sign-in-one-run rule.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the only external inputs are two operator env vars and the Codex binary's `--version` output. The version string is parsed by `requireCodexCapture` into three integers and otherwise only appears in a skip message; nothing from it reaches an argv or a path.
- [Tokens, secrets, credentials] No findings — the test never opens, stats, copies or moves any file in the capture home; it creates one symlink pointing at the directory. The daemon's `prepareCodexHome` writes only `config.toml` there (existing behaviour, same as `TestCodexApprovalLive`). The `--version` call runs with `CODEX_HOME` pointed at an empty temp dir, so it cannot read or refresh the sign-in. No log line prints the capture home's contents.
- [File operations] SHOULD FIX (addressed in design) — a capture home that is the operator's production Codex home would be rewritten by `prepareCodexHome`. `requireCodexCapture` resolves both paths with `EvalSymlinks` and fails when the capture home is at or under the real `~/.pyry`. The link itself lives under the `t.TempDir` HOME, created with `os.Symlink` into a freshly made 0700 dir, so there is no pre-existing path to race. Temp-dir cleanup removes the link, never its target (`os.RemoveAll` does not follow symlinks).
- [Subprocess] No findings — `exec.Command` with explicit argv; the binary path comes from the operator's env var, no shell. The only daemon flags added are `-pyry-codex=<bin>` and constants. The daemon runs without `--dangerously-skip-permissions`, so `OperatorBypass` is false and cannot widen the Codex sandbox.
- [Cryptographic primitives] No findings — reuses the package's Noise initiator with `crypto/rand` keys; nothing new.
- [Network & I/O] No findings — hermetic loopback fake relay; every receive is deadline-bounded.
- [Error messages, logs] No findings — failure messages name steps, file basenames under the temp workdir, and versions; never the sign-in file or account details.
- [Concurrency] OUT OF SCOPE — two concurrent runs sharing the one sign-in can log each other out (single-use renewal keys). The test does not use `t.Parallel`, and the Makefile target runs one package; cross-package concurrency with `TestCaptureLive` / `TestCodexApprovalLive` is an operator rule stated in the test's doc comment, not enforceable in code.
- [Threat model alignment] Remote permission answering requires `AllowRemotePermissions` on the paired device, which the harness sets for its own temp pairing only; the operator's real devices are untouched.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-25

## Revisions

**2026-09-25, during implementation.**

- `buildHelloEarlyInteractive` is renamed `buildHelloEarly` rather than only gaining a parameter: its sole caller is now `driveHandshake`, and the old name would claim an interactive-only hello it no longer builds.
- `runCodexTurn` does not filter `modal_shown` on `ConversationID`. The daemon holds one conversation, and a Codex modal whose scope arrived empty would otherwise be left unanswered until the approval window expired, hiding the real result behind a timeout. The scope is logged with each answer.
- `spawnCodexDaemon` takes no `home` parameter: the HOME reaches the daemon through the environment `WithWorktreeAuthenticated` pins, as in `spawnPermissionDaemon`.
- Added `TestCodexVersionBelow`, an offline table under the same tag, because the version comparison is new logic and a mismatch with `checkCodexVersion` would either run a Codex the daemon refuses or skip one it accepts.
- Open questions: both remain for the operator run to answer (option ids are asserted, `modal_dismissed` is checked when it arrives).
