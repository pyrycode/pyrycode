# Publish the confirmed running permission mode

## Files read

- `cmd/pyry/main.go` → `boundRunSettings`, `sessionSettingsReader`, `resolveBoundRunSettings`, `settingsOf`, `resolveBoundRunner`, and `runSupervisor` — owns conversation-to-session resolution, stored settings reads, and the composition-root closure used by `session_settings`.
- `cmd/pyry/streamsup_runner.go` → `streamRunner`, `SetPermissionMode`, and the compile-time `sessions.Runner` assertion — the production adapter that must forward the concrete stream runner's new informational read without widening `sessions.Runner`.
- `cmd/pyry/run_config_test.go` → `settingsReaderDouble`, `TestResolveBoundRunSettings`, `TestResolveBoundRunSettings_RealPool`, and `TestResolveBoundRunSettings_DormantRealPool` — pins exact-id resolution, live-before-dormant ordering, dormant non-materialisation, and the values handed to `runConfigFor`.
- `cmd/pyry/relay.go` → `runConfigFor` — copies the resolved values into the existing `relay.RunConfig` and gates context usage on a live session; its wire mapping remains unchanged.
- `internal/sessions/pool.go` → `Pool.Lookup`, `Pool.SettingsFor`, `Pool.DormantSettingsFor`, and `Pool.UpdateSettings` — provides the exact live session, the two stored-setting halves, and the no-respawn live permission update exercised by the regression.
- `internal/sessions/session.go` → `Session.Runner` and `SessionSettings` — exposes the runner behind an exact live session while keeping stored launch intent distinct from runtime confirmation.
- `internal/streamsup/runner.go` → `Runner.ConfirmedPermissionMode` and `Runner.SetPermissionMode` — supplies the concurrency-safe last confirmation for the current child and the in-band posture switch.
- `internal/streamsup/parser.go` → `confirmedPermissionMode` and `confirmedPermissionModes` — confirms that availability is child-scoped, cleared at boundaries, and never derived from argv or requested settings.
- `internal/streamsup/confirmed_permission_mode_test.go` → `TestRunner_ConfirmedPermissionMode_InitIsBoundedAndInformational`, `TestRunner_ConfirmedPermissionMode_WithoutParserIsUnavailable`, and `TestRunner_ConfirmedPermissionMode_ChildBoundaryClearsStateAndPending` — the prerequisite's focused contract and unavailable-state proof.
- `internal/e2e/realclaude/interactive_stream_default_posture_read_test.go` → `writeHandoffShapedNote`, `driveOutsideWorkspaceRead`, and `startDefaultPostureReadProbe` — the established outside-workspace `Read` modal proof this ticket reuses after observing the refreshed settings reply.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `startObservedPermissionHarness` — the authenticated daemon, paired phone, stored-default bootstrap, and stdio permission bridge used by the live regression.
- `internal/e2e/realclaude/harness_modal_test.go` → `spawnPermissionDaemon` — the daemon spawn seam that can receive a compile-time operator bypass flag for this regression while preserving existing callers.
- `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` → `liveChildPID` — reads the daemon's exact supervised child PID so the live test can prove the switch did not respawn.
- `internal/protocol/settings.go` → `RequestSessionSettingsPayload`, `SessionSettingsPayload`, and `SetSessionSettingsPayload` — the unchanged request, reply, and update wire vocabulary.
- `docs/knowledge/features/sessions-package-key-types-pool-settingsfor.md` → `Pool.SettingsFor` and `Pool.DormantSettingsFor` — records why live and dormant reads stay separate and why a settings read must not materialise a dormant session.
- `docs/knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md` → the `Session.Runner` capability pattern — requires a `streamRunner` forward for every concrete capability asserted from `cmd/pyry` and records the prior silent-adapter failure.
- `docs/knowledge/features/streamsup-package.md` → `Runner.ConfirmedPermissionMode` — distinguishes stored posture, turn admission, and Claude's last current-child confirmation.
- `docs/knowledge/features/development-verification.md` → “Prove that tests distinguish the change” and “Test execution and artifact survival” — requires a failing pre-change settings assertion and an executed, non-skipped live gate.
- `docs/specs/architecture/2511-confirmed-permission-mode.md` → design, security review, and revision — defines the child-generation and informational-read contract this publisher consumes.
- `docs/specs/architecture/2474-default-posture-outside-read-probe.md` → live probe design — fixes the posture witness and outside-workspace `Read` modal scenario reused for enforcement proof.
- `CODING-STYLE.md` → interface design, concurrency, testing, and symbol citations — keeps the capability consumer-owned, race-tested, and free of line-number references.

## Context

`resolveBoundRunSettings` currently copies `SessionSettings.YOLO` and `SessionSettings.PermissionMode` into every resolved reply. Those values are durable launch intent, not evidence about the child running now. An operator-provided bypass flag can deliberately keep a stored-default child in `bypassPermissions`, while an accepted in-band write can later move that same child to `default`. Reporting the persisted pair therefore makes a remote client display the opposite of the current posture.

The prerequisite shipped `Runner.ConfirmedPermissionMode`, a read-only, child-scoped observation that is unavailable until Claude confirms a mode and becomes unavailable again at a child boundary. This ticket publishes that observation through the existing fields. Model, effort, identity, context usage, settings writes, spawn policy, and operator-bypass provenance remain untouched. No ADR is warranted: this corrects the source of two existing reply fields without changing a package boundary or wire contract.

The work remains one deliverable and within the one-ticket boundary: two production files, about 650–700 total written lines including this plan and tests, no new exported type or interface, five resolver call sites, four acceptance criteria, and fewer than ten refusal branches. The nearest analogue remains #1857's conversation-keyed runner capability, with less payload work but an added live security proof.

## Design

### Exact-session runtime read

`cmd/pyry` will define a consumer-owned `confirmedPermissionModeReader` capability with the same `ConfirmedPermissionMode() (string, bool)` contract as `*streamsup.Runner`. `streamRunner` will forward that method to its concrete runner and carry a compile-time assertion that the forward exists. The capability remains off `sessions.Runner`: its only consumer is the composition root, and widening the sessions interface would force unrelated package doubles to implement a read they do not use.

A small `runSettingsPool` adapter will embed `*sessions.Pool`, preserving its existing `SettingsFor` and `DormantSettingsFor` methods, and add `ConfirmedPermissionModeFor(id)`. That method will refuse an empty id, call `Pool.Lookup` for the exact non-empty id, assert the returned session's runner against `confirmedPermissionModeReader`, and return unavailable for a miss, an unsupported runner, or an unconfirmed child. It performs no activation, persistence, command write, or fallback.

`sessionSettingsReader` will require all three reads, making the production adapter compile-checked at `resolveBoundRunSettings` rather than relying on an optional assertion at the wiring site. `runSupervisor` will pass `runSettingsPool{Pool: pool}` into the existing closure.

### Reply derivation

Conversation lookup and the empty-binding guard remain first. On a live `SettingsFor` hit, `resolveBoundRunSettings` will copy only session id, model, effort, and the live marker from stored settings, then ask `ConfirmedPermissionModeFor` with that same registry-owned id. When available, it will set `permissionMode` to the confirmed value and derive `yolo` solely as `mode == sessions.PermissionModeBypass`. When unavailable, both fields retain their zero values.

On a live miss, the dormant read remains exactly the second and final settings lookup. Its answer keeps the resolved session id, stored model, and stored effort, but `settingsOf` will no longer copy either stored permission spelling, so dormant replies structurally carry `permissionMode == ""` and `yolo == false`. An id absent from both halves remains unresolved and produces the existing all-zero no-session reply.

`runConfigFor`, relay mapping, payload types, usage lookup, and settings-update paths need no code change. They already copy the primitive fields they receive; changing the source inside the resolver is sufficient and adds no field or frame.

### Live regression

An `e2e_realclaude` test will reuse the authenticated stdio-permission harness with compile-time operator arguments `--dangerously-skip-permissions` and `--permission-prompt-tool stdio`, while leaving the seeded session's stored posture at `default`. The prompt-tool argument is necessary because `withApprovalArgs` correctly does not inject an approval gate for a child whose bypass provenance is operator-owned; including it at launch lets the same child surface the enforcement modal after the explicit in-band downgrade. A tool-free first turn starts the child and lets its `system/init` confirmation reach the runner. The test then requests `session_settings` over the paired phone and requires the bound session id plus `bypassPermissions` / `true`.

To make the stored-default update non-noop while still naming `default`, the test will send one `set_session_settings` frame that stores the already-selected `haiku` model and explicitly names the default permission mode. Both operations are in-band deliverable. It will poll correlated `session_settings` replies until the current child reports `default` / `false`, and compare the control-plane PID before and after to prove no respawn.

Finally, the test will write the established handoff-shaped witness outside the workspace, ask the same child to use `Read`, and reuse `driveOutsideWorkspaceRead` to require a real permission modal before allowing the read to finish. The helper will accept the caller's next envelope id so this longer sequence preserves monotonic client ids; the #2474 test keeps its current id through an explicit argument.

The test name will be stable and called out in the PR so the dispatcher-owned real-Claude gate can record that it ran. The builder will not run the live suite or claim its result.

## Concurrency model

No goroutine or lock is added. `Pool.SettingsFor` and `Pool.Lookup` remain separate read-lock acquisitions. The latter selects only the same non-empty session id read from the matched conversation. If eviction or revival occurs between them, the permission read observes either the currently held runner or unavailable; it can never fall through to bootstrap or another conversation. A successor child on the same runner cannot inherit its predecessor's value because `Runner.ConfirmedPermissionMode` clears and generation-binds the hold at child boundaries.

The confirmation read is concurrency-safe inside `streamsup`; the adapter adds no lock and does no I/O while a pool lock is held. The reply is a snapshot, not a lease: a later child transition can change availability after the request, exactly like existing model and context snapshots can change after their read.

## Error handling

- Unknown, empty, and unbound conversation ids retain the existing `(zero, false)` result without touching the pool.
- A live settings hit with no live session lookup, no capability, or no current-child confirmation remains a successful resolved answer with id/model/effort preserved and the permission pair unavailable.
- A dormant settings hit remains successful without querying any runner or reviving the session.
- Only an id missing from both stored-settings halves remains a no-session answer.
- The read returns no new error, logs nothing, sends no control request, and reflects no caller-supplied id or Claude-authored value into an error message.

## Testing strategy

- RED first in `cmd/pyry`: extend the resolver table with per-session confirmed answers and call recording. Stored-default plus confirmed bypass must report bypass/true; stored-bypass plus confirmed default must report default/false; an unconfirmed live session and a dormant session must preserve id/model/effort while reporting the zero permission pair. Exact call order must show that only the named live session's confirmation reader was consulted.
- Add a real-pool focused test whose runner implements the confirmation capability, plus the compile-time `streamRunner` capability assertion, so a missing production adapter forward cannot stay green behind a direct test double.
- Keep the existing `runConfigFor` assertions to prove context usage, identity, model, and effort do not move; update only expectations that previously treated stored permission intent as runtime evidence.
- Add the real-Claude regression described above behind `e2e_realclaude`. Its first two settings replies prove refresh on the same child, the PID comparison proves no respawn, and the outside-workspace `Read` modal proves the reported default posture enforces permissions.
- Builder-owned verification: `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry`. The dispatcher later owns the full-module race gate and the labelled real-Claude run; a skip is not acceptance.

## Open questions

None. The prerequisite fixes the runtime read contract, the existing reply fixes the unavailable representation, and the established live harness fixes the enforcement scenario.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` under **Session settings (v2)** / **`session_settings`** to state that `permission_mode` and `yolo` describe the last confirmed posture of the current child, not stored intent or argv. Document that a non-empty `session_id` may be paired with `permission_mode: ""` and `yolo: false` when no current-child confirmation is available, so `yolo: false` alone is not proof that permissions are enforced. For dormant sessions, preserve the stored `session_id`, model, and effort while documenting the permission pair as unavailable because there is no current child.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — untrusted `conversation_id` remains confined to `resolveBoundRunSettings`'s registry lookup and empty-binding guard. The new value crosses from Claude stdout through the bounded, generation-scoped hold behind `Runner.ConfirmedPermissionMode`; it is published as Claude's informational confirmation, never treated as an authorization grant.
- [Tokens, secrets, credentials] No findings — production creates, reads, persists, compares, or logs no token or credential. The live test uses the existing authenticated worktree and paired-device harness without exposing either credential in a new record.
- [File operations] No findings — production adds no file operation. The live regression reuses `writeHandoffShapedNote`, which writes a test-owned file beneath the isolated temporary home with fixed directory and file modes; no caller-controlled component or durable capture is added.
- [Subprocess execution] No findings — production command construction and child lifecycle are unchanged. The test passes only compile-time literal bypass and stdio prompt-tool arguments through the existing daemon spawn seam; no runtime or network value reaches argv.
- [Cryptographic primitives] No findings — the change adds no randomness, key, nonce, hash, encryption, or secret comparison. The existing Noise channel and witness-token helper are reused unchanged.
- [Network & I/O] No findings — no socket read, size limit, timeout, field, or frame changes. The request uses the existing bounded encrypted protocol path, and the new production work is an in-memory runner read after existing validation.
- [Error messages, logs, telemetry] No findings — the resolver and adapter add no logger or telemetry and return only availability. Caller ids, confirmed modes, payloads, and stored settings never enter a new error or log line.
- [Concurrency] No findings — no lock or goroutine is added. `Pool.Lookup` selects the exact registry-owned non-empty id under its existing read lock, and `Runner.ConfirmedPermissionMode` supplies the prerequisite's mutex- and child-generation-safe snapshot; a lifecycle race degrades to unavailable rather than another session's posture.
- [Threat model alignment] No findings — an authenticated interactive client learns the posture of the conversation it names through an existing reply, but gains no ability to grant, revoke, spawn, revive, or bypass permission checks. Pairing authorization, remote modal authorization, and relay encryption remain unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-20
