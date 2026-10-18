# codex_conversation_live_test.go

`codex_conversation_live_test.go` (#2660) — `TestCodexConversationLive`, the
first test to drive a real Codex conversation through the **whole daemon**:
a client handshakes over Noise v2 advertising `multi_agent`, creates a
conversation with `agent: codex`, sets model and effort through
`set_session_settings` with explicit low effort, and runs a plain streamed
turn. It then selects `plan`, completes a tool-free read-only staging turn,
and requires correlated `session_settings` readback naming the exact session
and confirmed `plan` before either write. `waitCodexPlanApplied` polls within
`codexSettingsBudget` to let the turn response and notification join; it
never sends another turn or accepts an empty mode or a different session.
The two write turns explicitly ask the shell tool to request approval and
await the decision. Both must surface real client permission modals:
rejecting leaves the declined file absent; accepting creates the accepted
file. `runCodexTurn` answers repeated modals and requires remote dismissal
attribution. `TestCodexApprovalLive`
(#2587, `cmd/pyry/codex_approval_test.go`) already proves decline/accept at
the runner level; this test's new ground is the pool and the wire path a
`multi_agent` client actually uses.

Shares one Codex sign-in with `TestCaptureLive`
([codexsup-package.md](codexsup-package.md)) and `TestCodexApprovalLive`, so
none of the three may run concurrently. Same two env vars as both:
`PYRY_CODEX_CAPTURE_BIN` and `PYRY_CODEX_CAPTURE_HOME`; below Codex
`codexMinVersion` (0.156.1) or either var unset, the test skips naming what's
missing. It refuses (`t.Fatalf`, not skip) a capture home that resolves
inside the operator's real `~/.pyry` — the daemon's `prepareCodexHome` would
otherwise rewrite a production Codex home's `config.toml`.

## Lessons that outlive this ticket

- **Unset means `default`/workspace-write through the pool.**
  `canonicalSettings` applies `canonicalPermissionMode` before the runner
  sees settings; the empty/read-only fallback in `codexTurnOverrides` is
  unreachable for an unset pool session. The original #3589 branch and base
  live failures observed the declined file present with no modal, confirming
  the earlier static forecast. Preserve the production mapping and explicitly
  select `plan` for an approval scenario. See the
  [posture table](codexsup-package-production-wiring.md#production-wiring--the-cmdpyry-codex-runner-2620).
- **A settings acknowledgement confirms intent, not the current child.**
  Codex applies overrides at `turn/start`. Selecting `plan` alone left the
  exact-session wire mode empty in the first #3603 live repair. Complete a
  short staging turn before reading confirmed posture; never substitute saved
  settings or allow an empty-mode bypass. Confirmation requires a nonempty
  successful `StartTurn` reply and matching root-thread `turn/started`, in
  either order, on the exact current child and attempt. Old-child and
  old-attempt evidence, incomplete or failed starts, and teardown cannot
  supply a reading. See [runner confirmation](codexsup-package-production-wiring.md#accepted-turn-permission-confirmation-3603)
  and the [wire contract](protocol-package-types-session-settings-read-payloads.md).
- **Scope the tool-free instruction to the staging turn.** A later live
  attempt had confirmed read-only posture but made no tool call, showed no
  modal and created no file; why the model made no tool call remains
  unknown. `touchPrompt` explicitly requests shell approval, waits for the
  decision and stops on denial. Prompt wording does not prove enforcement:
  retain both real-modal assertions, declined-absent/accepted-present file
  checks and remote dismissal attribution, with no model retry or new skip.
- **A permission-modal wait must not filter on `conversation_id` when the
  daemon holds only one conversation.** The plan's `runCodexTurn` was
  revised during implementation to drop that filter: a Codex `modal_shown`
  whose scope arrived empty would otherwise sit unanswered until the
  approval window expired, and the resulting timeout looks exactly like "no
  modal was raised" — hiding the real result behind an unrelated failure
  mode. The scope is logged with each answer instead, so a future empty
  scope is visible rather than silently swallowed by a filter.

The repaired scenario passed native macOS proof at `cb18f2b2`: 1 executed,
1 passed, 0 failed, 0 skipped. The later dispatcher gate at `641ed3ab`
merged with main `13e0d511` failed this test initially, then passed its
same-tree rerun; acceptance was after that rerun. These passes do not erase
the earlier failures. Linux is outside this ticket's live acceptance scope.

See the preserved [original design](../../specs/architecture/2660-codex-live-daemon-turn.md)
and the [accepted-turn repair and security review](../../specs/architecture/3603-codex-confirmed-posture.md).
