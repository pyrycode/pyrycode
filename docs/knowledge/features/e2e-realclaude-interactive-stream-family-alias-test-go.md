# interactive_stream_family_alias_test.go (#2447)

`TestInteractiveStreamPinnedModelFollowsItsFamily` is the live arm of the family
rule: a pyry session follows the latest model of its family. It was #2447's
`TestInteractiveStreamFullIDRowFollowsItsFamily` until 2026-10-07, when the menu
became one row per family and the pinned rows that test picked from stopped
reaching clients.

It answers two things only a real claude can:

- **The menu reaches the client as one row per family.** Claude Code 2.1.289
  publishes pinned rows (`claude-opus-5`, `claude-opus-4-7`) beside its family
  rows; `assertOneRowPerFamily2447` fails if any pinned row survives beside its
  family row.
- **A pinned id a client sends is run as its family.** A `set_model` control
  response cannot answer this (the `set_model_v2.1.259_refuse.json` capture shows
  claude replying `success` even for `claude-no-such-model-2279`), so the test
  spends two live turns and reads the `model_announced` frame, the only witness
  that names what actually served a turn.

Mechanics: request the live model menu, send a baseline turn, pick a family row
whose `resolved_model` is `claude-<family>-<major>-<minor>` (Fable preferred),
send `set_session_settings` naming the OLDER pinned id made by dropping the minor
(`claude-fable-5-1` gives `claude-fable-5`), send a second turn, and assert the
announced model both moved off the baseline (A1) and landed on the family row's
own `resolved_model` (A2). Sent verbatim, that id would run the older model or
fail, so A2 tells the family resolution from a pass-through. Nothing is
hard-coded: the row, the pinned id and the comparand all come from the menu this
run's own child published. See
[`SessionSettings` / `claudeSettingsArgs`](sessions-package-key-types-sessionsettings-claudesettingsargs.md)
for the production rule this test is proving.

**Lessons that outlive this ticket:**

- **A live arm that asked the production rule which inputs it rewrites would
  agree with itself by construction.** This file restates the shape rule
  independently (`rewritableFamily2447`) rather than calling
  `internal/modelfamily`, the same reasoning that made
  `interactive_stream_model_rejection_test.go` derive its candidate from the
  live menu instead of a literal.
- **The comparand is the family row's resolution, not a pinned row's.** The old
  test's known gap was a fallback that could pick `claude-haiku-4-5`, whose own
  `resolved_model` differs from what its alias `haiku` resolves to. Driving a
  family row and deriving the pinned id from it removes that ambiguity: the row
  that names the family is also the row that names its current model.
- **A negative live-witness needs its own instrument check.** The test sends a
  baseline turn before touching settings so the assertion is "the announced
  model moved, and moved to the family's resolution" rather than "the announced
  model matches", which a tree that ignored the settings change would pass
  whenever the row happened to resolve to what the daemon already ran.

### Related

- [sessions-package-key-types-sessionsettings-claudesettingsargs.md](sessions-package-key-types-sessionsettings-claudesettingsargs.md)
  : the family rule this test proves.
- [interactive_stream_model_announced_test.go](e2e-realclaude-interactive-stream-model-announced-test-go.md)
  : `drainForAnnouncedModel`, the frame reader this file reuses.
