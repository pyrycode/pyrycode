# interactive_stream_clear_wrapup_skipped_test.go

- `interactive_stream_clear_wrapup_skipped_test.go` — `TestInteractiveStreamClearWrapUpSkipped`
  (#2486), the inverse of
  [interactive_stream_announced_reset_test.go](e2e-realclaude-interactive-stream-announced-reset-test-go.md)'s
  happy path: proves that a wrap-up which cannot finish still completes the reset as
  `handoff: skipped` rather than failing it. The daemon is spawned with
  `-pyry-wrapup-deadline=1ms` — see [`v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`
  § *The wrap-up turn, and the reply's tense*](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#the-wrap-up-turn-and-the-replys-tense-2477)
  for the flag itself — and a handoff note is seeded for the conversation before the daemon
  starts. The case reuses #2485's `resetWindow` drain verbatim and asserts the three `resetting`
  edges in phase order (`wrapping_up/pending` → `restarting/skipped` → `active:false`), one
  `session_transition{reason:"clear"}` ordered after the `restarting` edge only, and that the
  seeded note's bytes are unchanged on disk afterward. No order is asserted between the
  transition and the falling edge, for the same structural reason as the sibling case. Zero
  production files touched by the test itself; the flag and its clamp are the production change.

  **Lessons that outlive this ticket:**
  - **A ticket can ask for an override of a documented invariant without either side noticing.**
    `wrapUpDeadline`'s doc fixes ninety seconds as not operator-editable, on the stated ground
    that a longer bound is a way to hold a child open. A bare override flag satisfies every
    acceptance criterion here and still repeals that sentence; only the adversarial security pass
    caught it. The fix is a shorten-only clamp in `bound()`, the one function total over every
    construction path (including the struct literals this package's unit tests build directly) —
    see § *The wrap-up turn* above. When a ticket asks to make a constant configurable, read the
    constant's own doc for a stated refusal before designing the seam.
  - **"An expired context stops the write" is an assumption worth measuring, not assuming.** A
    short-enough bound was expected to expire before the wrap-up prompt was ever written, sparing
    the case a turn. It does not: `turnBusyTracker.WaitIdle` tests queue membership before its
    select and returns immediately on an idle conversation, and `streamsup.WriteTurn` reads the
    context only for the (here nil) `turncommit` gate. The prompt is written regardless, and the
    expiry lands in the reply wait — which is also the *only* place production's own ninety-second
    bound can land against a live child, since the write itself cannot fail on a deadline. That
    makes the case more faithful than the original forecast, not less, but a design that assumed
    the forecast would have asserted a step that never happens.
  - **A drain that returns as soon as its target counts are met needs its own settle period to
    make a window-wide negative count non-vacuous.** #2485's file keeps reading past its
    `awaitReset` return because a later recall turn is still coming; this case drops that half, so
    asserting "exactly 3 `resetting` frames, exactly 1 transition" immediately after `awaitReset`
    would just restate what the drain already waited for. Reading the window for a short quiet
    period first is what actually exercises those negatives, and it also routes the killed
    wrap-up turn's tail and the fresh child's first frames through `resetWindow.next`'s standing
    negatives instead of leaving them unread.
  - **`protocol.TypeError` is a correlated reply type; the unsolicited terminal shape is
    `TypeSessionError`.** Worth knowing before writing a case that tears a child down mid-turn —
    `resetWindow.next` fatals on the former, so a mid-turn teardown (this case's own wrap-up turn,
    cut short by the fresh spawn) does not trip that guard.

**Related:** [`v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`
§ *The wrap-up turn, and the reply's tense*](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#the-wrap-up-turn-and-the-replys-tense-2477)
— the flag, the clamp, and why the ceiling lives in `bound()`.
