# interactive_stream_hook_blocked_banner_test.go (#2320)

The live rung under the `system/informational` → `turnevent.Banner` mapping #2319 shipped and
proved only against a committed capture: `TestInteractiveStreamHookBlockedBannerReachesTheClient`
spawns a session through the running daemon with a `UserPromptSubmit` hook installed, asserts the
block reason reaches the connected client as a `banner` frame, and that the session still answers
the next, unmarked prompt. Test-only, zero production files. Three live gate laps and one review
pass each reddened on a different assumption before this file's premise held; each is recorded in
the spec's `## Revisions` and the durable half of each is folded into the package overview it
actually belongs to rather than repeated here.

**Lessons that outlive this ticket:**

- **A hook-refused prompt is fully proven live: it reaches the client as a `banner` and opens or
  closes no turn on the wire.** See
  [turnevent-package-outbound-event-variants.md](turnevent-package-outbound-event-variants.md)'s
  `Banner` row for the production mechanics (`interactiveTurnEmitterV2.Handle`'s lifecycle-inert
  arm and its `TurnEnd` peer's drop-outside-an-open-turn behaviour) — this file is what turned that
  reading of the source into a measurement.
- **A rig cannot reach a daemon-spawned child through a flag the daemon also composes, and the
  failure is silent, not an error.** The first live lap spent a full run learning that pyry's own
  `--settings` always wins a collision with an operator pass-through one. See
  [sessions-package-key-types-writemcpsettings-session-settingspath.md](sessions-package-key-types-writemcpsettings-session-settingspath.md)
  for the argv-composition mechanics and the fix (plant the hook as user settings under the child's
  own `HOME` instead of fighting the flag).
- **A receive timeout is the success path of a negative assertion, and that kills the client.**
  The third live lap held its full quiet-window budget with `ReceiveBytes` correctly proving no
  frame arrived — and then couldn't send the next prompt, because the timed-out read had already
  closed the connection. See
  [fakephone-harness.md](fakephone-harness.md#library-trade-off-timed-out-receive) for the
  general form and why a fresh-client-per-attempt fix doesn't fit a shared-window primitive; the
  fix here is `letRefusedTurnSettle` sleeping the budget before the next send, unread, so anything
  the daemon emits queues in arrival order ahead of that send's ack instead of being missed.
- **A terminal guard placed before its own `return` protects nothing — it re-enters itself.** The
  fourth pass (a review finding, not a live lap) caught `hookBannerWindow.next`'s timeout guard
  setting `readTimedOut` and then `continue`-ing back into the same guard at the top of its own
  loop, instead of returning. Every wait that timed out died on the generic "read past a timeout"
  message on the very call that recorded the timeout — the three named-reading diagnostics this
  file exists to make legible (`awaitBanner`'s four-way hook-witness split, `awaitCompletedTurn`'s
  wedged-conversation and acked-but-silent arms) were unreachable, so a rig failure and the finding
  the ticket commissions would have read identically. `resetWindow`
  ([e2e-realclaude-interactive-stream-announced-reset-test-go.md](e2e-realclaude-interactive-stream-announced-reset-test-go.md)),
  the primitive this one copies, has no such guard and returns correctly — the defect was exactly
  the divergence from the precedent. Any future one-reader-per-window primitive with a terminal
  flag needs the same check: does the flag's branch return, or does it fall back into the loop that
  set it?
- **Logging claude-authored prose in a test failure is a render boundary, same posture as
  production.** `banner.text` can carry terminal escapes inside its 4 KiB bound, and `t.Logf`
  writes to the operator's terminal — this file uses `%q` at every site that touches it (and
  `level`, the spawn-record argv, and the hook witness verdicts), logging a summary rather than the
  full text on the success path. Not a new rule — the same one
  [e2e-harness-stream-interactive-harness-pattern-startstreamin.md](e2e-harness-stream-interactive-harness-pattern-startstreamin.md)
  states for workspace-authored command strings — but this is the first file in the family whose
  subject is claude-authored operator-facing prose rather than a structured field, so the trap was
  live rather than theoretical.

### Related

- [operator_system_lines_capture_test.go](e2e-realclaude-operator-system-lines-capture-test-go.md)
  — source of the four reused trigger helpers (`oslcapWriteHookRig`, `oslcapBlockPrompt`,
  `oslcapLivenessPrompt`, `oslcapHookVerdicts`) and the committed capture whose `block` phase first
  observed the informational line this file proves reaches a client.
- [interactive_stream_announced_reset_test.go](e2e-realclaude-interactive-stream-announced-reset-test-go.md)
  — the window-primitive precedent this file's `hookBannerWindow` copies; diverges from it on
  ordering, since a refused turn has no boundary to race a transition against, unlike `/clear`.
