# Spec — #1031: realclaude `new_session` + `set_session_settings` respawn a live session, reply bridge rebinds

**Size:** S (test-only, ONE new file, 0 production changes). Not security-sensitive (no `security-sensitive` label; a test-only liveness gate — the fake tiers #1004/#1005 own the verbs' security properties). Last child of #963.

## Files to read first

- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go:82-154` — the two-turn liveness spine and **every shared harness helper this test reuses verbatim**: `spawnBootstrapDaemon`, `driveHandshakeInteractive`, `seedBootstrapRegistry`, `seedBoundConversation`, `sealSendMessage`, `drainForAssistantReply`, `runPyry`, `decodePairPayload`, `readPersistedServerID`, `waitBinaryHello`, `mustJSON`, `sendNoiseMsg`, `relayTestLogger`. The new test's setup is a **superset of this file's `TestInteractiveBootstrapLiveness` setup** (pair → seed bootstrap id → seed bound conversation → spawn daemon → handshake interactive). Note `liveBootstrapUUID`/`liveConvID` constants — the new test needs its OWN distinct fixed ids (same-package collision rule, see the `livePerConvBootstrapUUID` comment).
- `internal/e2e/realclaude/interactive_conversation_lifecycle_test.go` — the **single-daemon sequential-verb spine** pattern (#1028: create→rename→…→delete on ONE conversation over ONE channel, request/reply strictly alternating so the Noise receive nonce stays in lockstep). This is the exact structural template. Also `readConversationIDsOnDisk:176-199` — the **on-disk registry reader shape to mirror** for the new `readBootstrapRow` helper.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go:206-320` — `sealEnvelope` (generalised seal for control verbs) and `drainForReply` (InReplyTo-correlated reply drain that skips interleaved stream frames) — **both reused as-is** for the `session_settings_updated` reply. Also `perTurnReplyBudget` (120s, the cold real-claude turn budget).
- `internal/e2e/relay_v2_new_session_test.go:49-178, 267-288` — fake-tier `new_session` (#1004): the **baseline-id-read-then-changed non-vacuity design** to mirror, the fire-and-forget bounded re-send loop (new_session drops silently on a not-yet-attached session), and the payload-less `TypeNewSession` envelope shape (`sendNewSessionFrame`). `uuidStemPattern` (transcribe this regex).
- `internal/e2e/relay_v2_settings_test.go:130-157, 232-347` — fake-tier `set_session_settings` (#1005): the valid-phase send → `session_settings_updated` reply → **persisted-settings read-back matched by `Bootstrap==true`** (`settingsRow`, `readBootstrapSettings`, `waitBootstrapSettings`, `sendSettingsFrame`, `ptr`). The realclaude test transcribes these shapes but picks a **credential-safe** model value (see Design). Note `SetSessionSettingsPayload{SessionID, Model *string, Effort *string, YOLO *bool}` (pointer = presence) and `SessionSettingsUpdatedPayload{SessionID}`.
- `internal/sessions/transition.go:57-106` — `onRotate` → `RotateID` + `rebindConversation` (and `notifyTransition`). **This is why the post-control liveness sends are buildable:** a `/clear` re-keys the pool entry (`RotateID`, same live `*Session`, `p.bootstrap` flips) AND re-points the bound conversation's `CurrentSessionID` (`RebindSession`) so the seeded conversation still routes. `UpdateSettings` live-restarts the same-keyed session (id unchanged unless the restart itself rotates). Read this to understand the mechanism the test exercises; no code changes here.
- `internal/sessions/session.go:98-122` — `claudeSettingsArgs`/`spawnArgs`: a session `Model` setting **appends** `--model <v>` after the daemon's base `--model haiku`, last-wins. This is why `Model:"haiku"` is the credential-safe change (see Design § set_session_settings).
- `internal/sessions/pool.go:573-593` (`RotateID`) and `:885-896` (`Lookup`) — confirm the re-key + why a stale literal binding would fail `Lookup` absent the rebind. Context only.
- `internal/e2e/realclaude/fixtures.go:96-146` — `WithWorktreeAuthenticated` (the creds/PATH skip-clean gate, AC #3) and `ReadJSONL`. Note: fixtures.go provides a *transcript* JSONL reader but **no `sessions.json` registry reader** — the new bootstrap-row reader is genuinely new (small).

## Context

The `e2e_realclaude` interactive tier has ONE session-control gap: neither `new_session` (rotate) nor `set_session_settings` (respawn) has ever executed against real `claude`. Both verbs tear down and re-establish the live claude child; the operator-facing risk is that **the reply bridge fails to rebind past the respawn** — the same class of failure #854 guards on a cold bootstrap session. The fake tiers close the deterministic shape (#1004 `new_session`, #1005 `set_session_settings`); per the 2026-07-08 "real-claude e2e in a pre-ship gate, always" policy, each operator-facing happy path also needs a real-claude gate in `make preship`.

**The load-bearing subtlety (why each liveness facet is paired with an on-disk anchor).** A pure-liveness assertion is *vacuous* for both verbs: a silently no-op'd rotate/respawn would still answer from the un-rotated/un-restarted session and pass. So each verb pairs its liveness send with a **deterministic on-disk observable** proving the respawn genuinely happened:

- `new_session`: the `sessions.json` bootstrap-row id changed to a new value (baseline read *before* the frame → non-vacuous, mirrors #1004).
- `set_session_settings`: the `sessions.json` bootstrap-row settings reflect the new value (mirrors #1005).

## Design

**ONE new file, ONE test, ONE daemon, ONE conversation — a sequential spine (the #1028 shape).** Both verbs run against the same freshly-spawned daemon on the same seeded bound conversation over the same encrypted channel, requests and replies strictly alternating so the Noise receive nonce stays in lockstep. This saves a second daemon spawn + handshake + creds setup, exactly as #1028 ran five verbs on one daemon.

New file: `internal/e2e/realclaude/interactive_session_control_liveness_test.go` (build tag `e2e_realclaude`, package `realclaude`). Placement alone wires it into `make e2e-realclaude` → `make preship` (no Makefile change, AC #3), mirroring #854/#997/#1028.

### Setup (superset of `TestInteractiveBootstrapLiveness`)

Reuse verbatim: `runPyry("pair", …)` → `seedBootstrapRegistry(home, <newBootstrapUUID>)` → `seedBoundConversation(home, <newConvID>, <newBootstrapUUID>, workdir)` → `spawnBootstrapDaemon` → `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeInteractive`. Use **distinct fixed ids** (e.g. `sessionCtrlBootstrapUUID`, `sessionCtrlConvID`) so this file never shares a fixed identifier with the sibling gates in the same package.

### Spine (each step's AC in brackets)

1. **Turn 1 — pre-control liveness.** `sealSendMessage` + `drainForAssistantReply(convID, 1, perTurnReplyBudget)`. Establishes the reply bridge is BOUND, the tui-driver session is ATTACHED (so `new_session` won't silently drop), and claude has an established transcript (so `/clear` produces a genuine rotation). **This is load-bearing for non-vacuity of both later liveness sends: without a pre-bound bridge, a post-control send is a first-bind, not a rebind.**
2. **Baseline id read.** `idBefore := readBootstrapRow(t, home).ID` — read after Turn 1 fully settles. Poll until non-empty. [AC #1a non-vacuity anchor]
3. **`new_session` rotate — bounded fire-and-forget re-send loop.** Mirror #1004: seal a payload-less `protocol.TypeNewSession` envelope via `sealEnvelope`, re-send every ~250 ms (bump the envelope id each send) until `readBootstrapRow(...).ID` changes away from `idBefore`, bounded by a generous deadline (~45 s — real claude `/clear` + mint + fsnotify watcher rotate, not fakeclaude ms). Assert `idAfter != idBefore` **and** `idAfter` matches `uuidStemPattern`. [AC #1a] Structural causality: real claude only rotates its JSONL on `/clear` (normal turns append), so an id change during this window ⟺ the driven `/clear` — the realclaude analogue of #1004's never-created-file-trigger guard. Document this.
4. **Turn 2 — post-rotation liveness.** `sealSendMessage` + `drainForAssistantReply(convID, 2, perTurnReplyBudget)`. Proves the reply bridge rebound past the rotation. Buildable because `rebindConversation` re-pointed `convID`'s `CurrentSessionID` to the rotated id (see transition.go). [AC #1b]
5. **Current id read.** `sid := readBootstrapRow(t, home).ID` — the settings frame targets the *current* bootstrap id (Turn-2 activity or the restart may have rotated it again; match-by-flag downstream keeps this robust).
6. **`set_session_settings` respawn — request/reply.** `sealEnvelope` a `protocol.TypeSetSessionSettings` frame with `SetSessionSettingsPayload{SessionID: sid, Model: ptr("haiku")}`, then `drainForReply(protocol.TypeSessionSettingsUpdated, reqID, …)` (InReplyTo-correlated; drops interleaved `session_transition`/stream frames). Assert reply `SessionID` non-empty.
7. **Persisted-change assertion.** Poll `readBootstrapRow(t, home)` (matched by `Bootstrap==true`) until `Model == "haiku"`. [AC #2a non-vacuity anchor]
8. **Turn 3 — post-respawn liveness.** `sealSendMessage` + `drainForAssistantReply(convID, 3, perTurnReplyBudget)`. Proves the reply bridge rebound past the settings-restart. [AC #2b]

### Why `Model: "haiku"` (credential-safe, and still a genuine change)

The initial seeded bootstrap settings have `Model == ""` (seed row carries no model field). `UpdateSettings` sees `"haiku" != ""` → a **genuine** change → persist + live-restart (a no-op update writes nothing and does not restart). So `"" → "haiku"` is non-vacuous on disk AND triggers a real respawn.

`claudeSettingsArgs` appends `--model haiku` after the daemon's base `--model haiku` (`spawnBootstrapDaemon` args) → `--model haiku … --model haiku`, last-wins → effective model **haiku**, the one model this harness has already proven live. This deliberately avoids the credential/rate-limit risk of spawning a *different* real model (opus/sonnet) in a pre-ship gate: the fake tier (#1005) already proves the settings mechanism with `opus`; the realclaude gate must not gamble on a model the test creds may not serve. The duplicate `--model` is last-wins-safe (see Open Questions for the single verify point).

`readBootstrapRow` matches the `Bootstrap==true` row (not a literal id), so the persisted assertion is robust even if the restart rotated the bootstrap id — mirroring #1005's `readBootstrapSettings` flag-match.

### New helper (the only genuinely new code)

`readBootstrapRow(t *testing.T, home string) bootstrapRow` — read `<home>/.pyry/test/sessions.json`, return the `Bootstrap==true` row's `{ID, Model, Effort, YOLO}` (Fatal if the file/row is absent when required; callers poll). One small struct + one reader, modelled on #1005's `settingsRow`/`readBootstrapSettings` and #1028's `readConversationIDsOnDisk`. Transcribe `uuidStemPattern` (the one-line regex from `rotation/watcher.go:19`). Everything else is existing helper reuse.

## Concurrency model

No new concurrency. The phone conn is single-writer on the test goroutine; the verb spine is strict request→reply / send→drain, so the Noise send/recv nonce sequences stay aligned (every `noise_msg` is decrypted in arrival order by the drain helpers; non-`noise_msg` control frames are skipped without advancing the nonce). `new_session` is the only fire-and-forget verb — its bounded re-send loop is the sole writer, and it reads no reply (the on-disk id change is its observable). The daemon-side rotation (fsnotify watcher goroutine → `onRotate`) and settings-restart run inside the daemon process; the test observes them only through `sessions.json` and the reply stream.

## Error handling

- **Skip-clean (AC #3):** the reused setup calls `WithWorktreeAuthenticated` / `exec.LookPath("claude")`, which `t.Skipf` when claude is off PATH or creds are absent — inherited, no new code.
- **Bounded budgets, clear failure messages:** the rotate re-send loop and each `drainFor*` carry an explicit deadline; a timeout Fatals with a message naming which facet failed (id-never-rotated vs no-assistant_delta vs no-settings-reply) so a red is diagnosable. Reuse the existing helpers' messages; add a specific one for the rotate loop (mirror #1004's).
- **Non-fatal daemon-side saves:** `rebindConversation` and `RotateID` persist best-effort (Warn-logged on failure); the in-memory rebind is authoritative, so a transient save error does not break routing. The test asserts the effect (id changed / model persisted / send answers), not the save path.

## Testing strategy

This IS the test. Verification is running it: `make e2e-realclaude` (or `go test -tags e2e_realclaude -run TestInteractiveSessionControlLiveness ./internal/e2e/realclaude/`) on a host with real `claude` + creds; it must go green and skip cleanly without them. Wall-clock (AC #4): 3 real haiku turns + 1 rotate + 1 restart, realistically ~40–70 s, worst-case bounded by the per-step deadlines (only on failure). Comparable to the existing `interactive_per_conversation_liveness` gate (3 turns). No unit tests — a liveness gate has no hermetic surface; the deterministic RED/GREEN for these verbs lives at the fake tier (#1004/#1005) and the pool unit tests.

## Open questions

- **Duplicate `--model haiku` acceptance (the one thing to verify live during implementation).** The respawn argv becomes `--model haiku … --model haiku`. Standard CLI last-wins makes this benign, but confirm real `claude` 2.1.199 does not reject repeated `--model`. If it does, the fallback is a value NOT already in the base argv — `Effort: ptr("low")` (appends `--effort low`, no duplication) — provided haiku accepts `--effort`; failing both, `Model: ptr("sonnet")` (Claude Code default model, credential-safe on any standard auth) is the last resort. The persisted-change + liveness structure is identical for any of these; only the payload value changes.
- **First-turn rotation timing.** Whether Turn 1 leaves the bootstrap id at the seeded value or at a claude-minted stem does not affect correctness: `idBefore` is read fresh after Turn 1 settles, and the persisted-settings/id assertions match by the `Bootstrap==true` flag, so both facets are robust either way. Noted so the developer does not add a brittle "id == seeded value" precondition.
