# interactive_stream_family_alias_test.go (#2447)

`TestInteractiveStreamFullIDRowFollowsItsFamily` is #2447's live arm, and the one
that can void the whole ticket: only a real claude can say whether it honours a
family alias (`fable[1m]`) as the model a full-id row's picker actually meant. A
`set_model` control response cannot answer this — the
`set_model_v2.1.259_refuse.json` capture shows claude replying `success` even for
`claude-no-such-model-2279` — so the test spends two live turns and reads the
`model_announced` frame, the only witness that names what actually served a turn.

Mechanics: request the live model menu, send a baseline turn, pick a published
row `familyAlias` (`internal/sessions/modelfamily.go`) would rewrite, send
`set_session_settings` naming that row, send a second turn, and assert the
announced model both moved off the baseline (A1) and landed on the picked row's
own `resolved_model` (A2). Nothing is hard-coded — the row and the comparand both
come from the menu this run's own child published, so a claude release cannot
turn either assertion into a false pass. See
[`SessionSettings` / `claudeSettingsArgs`](sessions-package-key-types-sessionsettings-claudesettingsargs.md)
for the production rewrite this test is proving.

**Lessons that outlive this ticket:**

- **A live arm that asked the production rewrite which inputs it rewrites would
  agree with itself by construction.** `familyAlias` is unexported, so this file
  restates the shape rule independently (`rewritableFamily2447`) rather than
  calling into `internal/sessions`. That is the correct shape regardless of
  visibility — the same reasoning that made `interactive_stream_model_rejection_test.go`
  derive its candidate from the live menu instead of a literal.
- **Known test gap, not yet fixed as of this ticket's landing: the fallback
  row can make A1/A2 misread a working rewrite as a voided design.**
  `pickRewritableRow2447` prefers a Fable-family row (the ticket's named case)
  and falls back to any other rewritable row when none publishes. The fallback
  can select `claude-haiku-4-5` — the ticket's own "intended consequence" row,
  whose alias (`haiku`) resolves to a *different* model than the row's own
  `resolved_model` (`claude-haiku-4-5-20251001` vs `claude-haiku-4-5`). Both
  assertions compare against the picked row's own `resolved_model`, which is the
  wrong comparand for that one row: the sound comparand is the `resolved_model`
  of whichever published row's `value` equals the alias, when one exists, and
  the picked row's own `resolved_model` only when none does. Caught at code
  review (PASS, SHOULD FIX — not blocking, since the preferred Fable branch is
  reachable and still published at last capture). `pickRewritableRow2447` logs
  `"no usable fable-family row; falling back to %q"` when the fallback fires —
  **check that line before reading a red run here as the alias rewrite being
  unsafe to ship.**
- **A negative live-witness needs its own instrument check.** The test sends a
  baseline turn before touching settings so the assertion is "the announced
  model moved, and moved to the picked row's resolution" rather than "the
  announced model matches" — a tree that ignored the settings change entirely
  would still pass the latter whenever the picked row happened to resolve to
  whatever the daemon already ran.

### Related

- [sessions-package-key-types-sessionsettings-claudesettingsargs.md](sessions-package-key-types-sessionsettings-claudesettingsargs.md)
  — the `familyAlias` rewrite this test proves, and the "intended consequence"
  paragraph this file's known gap collides with.
- [interactive_stream_model_announced_test.go](e2e-realclaude-interactive-stream-model-announced-test-go.md)
  — `drainForAnnouncedModel`, the frame reader this file reuses.
