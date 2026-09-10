# interactive_stream_announced_reset_test.go

- `interactive_stream_announced_reset_test.go` (#2138) — the real-`claude` rung
  under the announced-reset family (#2134 parser arm, #2135 follower, #2136
  runner id adoption): all three are proven only against fakeclaude, which
  emits whatever shape it's scripted to emit, and the premise underneath them
  — that a real claude announces a `conversation_reset` when `/clear` goes
  through the stream-json input — rested on one hand-observed capture with
  both id values elided. `TestInteractiveStreamAnnouncedResetFollowsLiveClaude`
  sends `/clear` as ordinary `send_message` text and asserts the client sees
  exactly one `session_transition{reason:"clear"}` followed by a completed
  second turn, with no `unrecognized_message` anywhere in the window. Zero
  production files touched.

  **Lessons that outlive this ticket:**
  - **When a test's next step is gated on a state machine, find what else
    writes that state before assuming the obvious frame is the trigger.**
    `waitIdleForDelivery` blocks a second message on the same conversation
    until it has no open turn, and whether a `/clear` turn ever closes on its
    own is unknowable in advance — a slash command replying with nothing is
    plausible. Waiting for a terminal frame of that turn before sending turn 2
    would hang on exactly the shape under test. What actually unblocks it is
    the reset itself: `transitionClearsTurn` answers `(successor, true)` for
    `ReasonClear` and clears the turn mark, so turn 2 goes out the moment the
    `session_transition` arrives, never after a terminal frame of the
    `/clear` turn.
  - **A "returns the first X" drain can't be retrofitted into a counting
    one.** `drainForControlEvent` silently consumes every frame before the
    one it wants, so both "exactly one transition" and "no
    `unrecognized_message` anywhere in the window" become unassertable after
    the fact — the evidence is already gone by the time you look. Counting
    has to happen inside the primitive every frame passes through, so this
    file uses one local drain with two thin waits over it instead of two
    independent drains. The Noise receive-nonce discipline forces the same
    shape from the other side: one reader for the whole window, or the
    `CipherState` desyncs and the failure surfaces as an unrelated decrypt
    error many frames later.
  - **An ack and its delivery can sit on opposite sides of an async
    boundary, so a stuck delivery seam shows up one symptom later than
    expected.** `SendMessage`'s ack fires synchronously off `router.Route` on
    the handler goroutine; `waitIdleForDelivery` runs later, on the async
    queue-drain path. A held delivery seam therefore surfaces as
    ack-but-no-delta, never as a missing ack — "no ack" specifically means
    `Route` failed to resolve the conversation against the re-keyed pool
    entry.

**Related:**
[interactive_stream_hook_blocked_banner_test.go](e2e-realclaude-interactive-stream-hook-blocked-banner-test-go.md)
copies this file's window-primitive shape for a turn that can't satisfy the shared drain either,
but sends its second turn on a different signal — a hook-refused turn has no closing boundary to
race a transition against, unlike `/clear`.
