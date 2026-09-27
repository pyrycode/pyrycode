# Conversation agent switch (#2672)

## Files read

- `cmd/pyry/main.go` → `sessionMinter.Create`, `validateModelVocabulary`, `validateEffortVocabulary`, `effortLevelsFor`, `resolveSpawnDir`: existing target-agent vocabulary and workspace boundaries.
- `cmd/pyry/session_reset.go` → `conversationReset.begin`: per-conversation operation guard shared with reset.
- `internal/sessions/pool.go` → `MintWith`, `HarnessFor`, `SettingsFor`, `DormantSettingsFor`, `Remove`: mint, settings, and live removal contracts.
- `internal/sessions/transition.go` → `notifyTransition`, `rebindConversation`: clear fan-out and its swallowed save error, which the switch must avoid.
- `internal/conversations/registry.go` → `RebindSession`, `Save`: binding/history mutation and atomic persistence.
- `docs/knowledge/features/conversation-session-binding-create.md` → create path: mint validates vocabulary before workspace; Codex build can probe synchronously.
- `docs/knowledge/features/development-verification.md` → change surface and ordering proof requirements.

## Context

A clear rotation retains its runner. A switch needs a new runner while preserving the conversation row and transcript history. `notifyTransition` cannot commit this operation because it swallows conversation-save errors. There is no protocol change here; #2674 supplies the relay caller and runs it off the relay Run goroutine. #2673 supplies hand-over.

## Design

Expose a daemon-side switch coordinator in `cmd/pyry` taking conversation ID, target agent, optional model and effort, pool, registry and the reset guard. It returns a new session ID plus an error; a nonempty ID means the rebind committed, even if post-commit removal failed. A nil ID with error means the original binding remains. The caller must not describe a nonempty-ID result as an unchanged switch.

After `conversationReset.begin`, read the bound conversation and old harness/settings from the live reader or dormant reader. Reject unknown/unbound rows, same-agent targets and unsupported target values before any mint. Resolve the recorded workspace through `resolveSpawnDir` (including its empty-path exception only when the recorded workspace is empty). Mint with the conversation ID label, resolved workspace, requested model/effort and carried posture. Carry effort only when the target model's vocabulary permits it; turn an old bypass posture into `default` and never grant bypass on the successor. The previous conversation metadata, prompt and message history stay on the same row.

Use `Pool.MintWith`. On any mint error, including `(id, err)`, remove the returned ID with `JSONLLeave` and preserve the old binding. Rebind through `Registry.RebindSession`, then `Save`. On save failure, undo only that rebind and remove the minted session. On successful save the switch is committed. Remove the old session with `JSONLLeave` (including a dormant-only entry). Publish one clear transition directly through the pool observer after the binding is durable, without `notifyTransition`'s second rebind. A post-commit removal error returns the committed new ID and error; the observer still sees one transition. The old entry remains for retryable cleanup, while new message routing follows the new binding.

Extend `Pool.Remove` to remove a dormant-only entry under its existing lock and save rollback, then apply the selected transcript disposition. `JSONLLeave` preserves transcripts. Add a narrow registry undo for a failed rebind save: it compares the new binding and history tail before truncation, preserving unrelated metadata.

## Concurrency model

`conversationReset.begin` excludes a simultaneous reset or switch for the same conversation. Pool and registry locks remain separate; no callback occurs while either is held. No new goroutine is created here. #2674 must invoke this synchronous operation off the relay Run goroutine because Codex mint may probe synchronously. The return contract distinguishes precommit from postcommit failure.

## Error handling

All refusal errors are static or wrap existing sentinels without model, effort, permission mode, prompt, or workspace values. Validation precedes mint. Precommit failures clean the minted ID, including an ID returned with an error. A cleanup failure is joined with the primary error. A conversation save failure restores the in-memory binding/history. Postcommit old removal failure cannot roll back a published, durable binding; report the committed ID with the error and emit the transition.

## Testing strategy

- RED test for live, evicted and dormant-only old sessions: new agent, label, workspace, settings, one history append, persistent registries, transcript retention, one transition and routing through the new ID.
- Rejection table for unknown/unbound, same-agent, target model/effort, and refused workspace; assert unchanged binding/registries and no transition.
- Mint error with returned ID cleans it. Inject conversation-save and old-removal failures to assert the ID/error commit distinction and transition count.
- Pool test for dormant-only `Remove` with leave policy and save rollback.

## Open questions

- Whether existing pool test scaffolding can inject a `(id, err)` mint outcome without a new seam. Resolve during Phase B and record any design change below.

## Documentation handoff

Pending for the documentation stage: add the daemon switch contract and failure/commit distinction to `docs/knowledge/features/conversation-session-binding.md` under `## Maintaining the binding across rotation (#739)`. No protocol reference change is required in this ticket; #2674 owns its wire contract.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The recorded workspace crosses from registry data into a subprocess through `resolveSpawnDir`; the coordinator validates it before `MintWith`. The target agent is checked against the two known harness values, and model/effort use the existing vocabulary checks.
- [Tokens and cryptography] `MintWith` delegates ID generation to `NewID` and its `crypto/rand` path. No new token, key, or cryptographic primitive is introduced.
- [File operations] `resolveSpawnDir` owns confinement, symlink resolution and trust marking. `Registry.Save` and the pool registry writer retain atomic, mode-restricted writes. `JSONLLeave` preserves transcripts. The existing accepted workspace TOCTOU window remains.
- [Subprocess] No shell is added. `MintWith` builds the selected runner through its existing factory. `Remove` terminates a live old child; dormant removal has no process.
- [Network and I/O] No new network reader or protocol payload is introduced. #2674 owns the bounded relay request and off-Run invocation.
- [Errors and logs] Values for model, effort, posture, prompt and workspace are excluded from switch logs and errors; static rejection sentinels and IDs suffice.
- [Concurrency] The reset guard serializes per conversation. Persistence and fan-out run with no pool or registry lock held; no new goroutine needs shutdown.
- [Threat model] The paired relay threat boundary remains with #2674's handler. This daemon primitive refuses unbound IDs and untrusted workspaces before mint.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-27

## Revisions

- Phase B: `DormantSettingsFor` deliberately reports the revoked posture that `Revive` would use, including for a persisted non-bypass mode. The switch now reads persisted posture separately through `DormantStoredPostureFor`, then revokes bypass before minting. The existing read contract stays intact.
- Phase B: `Registry.SwitchSession` now owns the rebind/save pair under `saveMu`, replacing the planned separate `RebindSession`, `Save`, and undo calls. It returns whether the new binding remains if a save fails, so the coordinator never reports that state as an unchanged switch.
- Phase B: live `Pool.Remove` emitted an eviction during old-child teardown. `notifyTransition` now ignores eviction of an ID already removed from the pool, allowing the committed switch to emit exactly one clear transition.
- Verifier rework: the Security review's errors-and-logs finding assumed `resolveSpawnDir` returned path-free errors. It can include the recorded workspace and home path in a refusal or trust-mark failure. `conversationAgentSwitcher.Switch` now translates a refused workspace to the path-free `ErrSpawnDirRejected` sentinel and other resolver failures to a path-free workspace-unavailable error; neither error wraps the path-bearing cause.
- Verifier rework: `Registry.SwitchSession` now holds the registry mutex through the disk write and any rollback. The original design released it after taking a snapshot, allowing `Delete` or `RebindSession` to change the binding during persistence. The write invokes no registry callback, and competing mutations resume after the commit or rollback. `activeSessionStarter.start` now claims `conversationReset.begin` before resolving a bound runner for both live and childless resets; the live tail keeps that claim until its rotation finishes. This closes the childless `new_session` route that could re-key the freshly minted ID while a switch was saving. The security review's concurrency finding is amended accordingly: the registry lock is held during switch persistence, but fan-out remains off-lock.
- Verifier rework: the Security review's errors-and-logs finding also overlooked path-bearing errors from `MintWith` after a workspace was accepted. A Codex runner probe can fail with a `chdir` error that names that workspace. `conversationAgentSwitcher.Switch` now returns the static `ErrAgentSwitchMintFailed` classification instead of wrapping a mint error, retains the static `ErrPoolNotRunning` classification where applicable, and reports cleanup failure with a separate path-free sentinel. The review's path-exclusion claim applies at both workspace resolution and mint boundaries.
