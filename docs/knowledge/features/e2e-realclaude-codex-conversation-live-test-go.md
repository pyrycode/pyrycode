# codex_conversation_live_test.go

`codex_conversation_live_test.go` (#2660) — `TestCodexConversationLive`, the
first test to drive a real Codex conversation through the **whole daemon**:
a client handshakes over Noise v2 advertising `multi_agent`, creates a
conversation with `agent: codex`, sets model and effort through
`set_session_settings`, runs a plain turn, then answers a file-write
permission modal twice (declined, then accepted). `TestCodexApprovalLive`
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

- **An unset `permission_mode` never reaches `codexTurnOverrides` as `""`.**
  The ticket's acceptance criterion assumed leaving the mode unset gives
  Codex a read-only sandbox where the declined-file turn is guaranteed a
  modal. Reading `internal/sessions`' `canonicalSettings` says otherwise: it
  runs at both session-construction sites and turns every minted or revived
  session's empty mode into `default` *before* a Codex runner ever sees it.
  `codexTurnOverrides`' `anything else, including empty` row — the one the
  criterion relies on — is therefore unreachable through the pool; `default`
  maps to `workspaceWrite`, where a `touch` can run with no prompt. See
  [codexsup-package.md § Production wiring](codexsup-package-production-wiring.md#production-wiring--the-cmdpyry-codex-runner-2620)
  for the corrected posture table. Any reasoning about an "unset" Codex posture has
  to start from `canonicalPermissionMode`, not from the overrides table —
  the table only ever sees what construction already normalised.
- **A permission-modal wait must not filter on `conversation_id` when the
  daemon holds only one conversation.** The plan's `runCodexTurn` was
  revised during implementation to drop that filter: a Codex `modal_shown`
  whose scope arrived empty would otherwise sit unanswered until the
  approval window expired, and the resulting timeout looks exactly like "no
  modal was raised" — hiding the real result behind an unrelated failure
  mode. The scope is logged with each answer instead, so a future empty
  scope is visible rather than silently swallowed by a filter.

The predicted posture finding was static (read from `canonicalSettings` and
`codexTurnOverrides`, not yet confirmed against a live Codex run as of this
writing); whether Codex's `default` posture should map to workspace-write is
a product decision outside this ticket's scope.

See `docs/specs/architecture/2660-codex-live-daemon-turn.md` for the full
design and security review.
