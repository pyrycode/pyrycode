# interactive_stream_announced_reset_test.go

- `interactive_stream_announced_reset_test.go` — `TestInteractiveStreamClearRunsDaemonReset`
  (#2485, renamed from `TestInteractiveStreamAnnouncedResetFollowsLiveClaude`, #2138). Since
  #2456 a client's `/clear` is intercepted in `SendMessage` and runs the daemon's own
  conversation reset instead of ever reaching claude, so the announced-reset premise this file
  used to assert — that a live claude prints a `conversation_reset` line for that input — can no
  longer happen on this path. This case asserts what the daemon actually runs instead, in one
  live round trip: a per-run fact is planted with the predecessor, `/clear` is sent as ordinary
  `send_message` text, and the client is asserted to see, in order, `resetting{wrapping_up,
  pending}`, `resetting{restarting, written}`, one `session_transition{reason:"clear"}` (only
  ordered after the `restarting` edge — see below), then `resetting{active:false}`. A turn sent
  after the window closes must recall the planted fact from the successor's first reply, proving
  the handoff note composed by `refreshSystemPromptForRotation` during rotation actually reached
  the new child's appended system prompt. Zero production files touched.

  The `session_transition` is asserted only to follow the `restarting` edge, not to precede or
  follow the falling `resetting{active:false}` edge: the three `resetting` edges are pushed
  synchronously on `resetThenRotate`'s own goroutine, but the transition reaches the client via
  `sessionTransitionEmitterV2.Enqueue`, a non-blocking send drained on a separate goroutine, so it
  can race the falling edge. That race is accommodated in the assertion, not removed — making the
  emitter synchronous would undo #659's non-blocking hand-off to buy a tidier test.

  **Lessons that outlive this ticket:**
  - **A content assertion downstream of a reset needs a turn-id exclusion set; a liveness
    assertion doesn't, and the two don't share reasoning.** The retired version of this case
    asserted only that a second turn completed after the transition, and needed no exclusion set
    because any post-reset delta already proved liveness. Recalling a specific fact is a
    different claim: the predecessor's wrap-up reply reaches the client too (`wrapUpCapture`'s
    sink forwards every event downstream unchanged) and *contains the planted fact by design*,
    since putting it there is what the wrap-up prompt asks for. The recall drain snapshots every
    turn id seen before the recall send and refuses deltas on any of them — a drain that
    naively took "the first delta after `/clear`" would go green on the wrong session's reply.
  - **A handoff note only composes into a per-session appended prompt, never into the daemon's
    bootstrap prompt — and `handoff:"written"` cannot tell you which one you got.** A wrap-up
    that reports `written` proves the note reached the note *store*; it says nothing about
    whether a later compose actually happened. `refreshSystemPromptForRotation` returns early
    when `sess.systemPromptPath` is empty, which `systemPromptPathFor` guarantees for the
    bootstrap by construction (the bootstrap "can never become a conversation's bound session"),
    and separately `handoffNoteFor` refuses the bootstrap's empty label via `conversations.
    ValidID`. A case that seeds its conversation onto the bootstrap session (this suite's usual
    `seedBoundConversation` shape) can never recall a note, and every wire frame still reports
    success throughout — both live reds here were exactly this, diagnosed as a prompt-wording
    problem on the first pass because the successor's answer was, correctly, "no such fact in
    this transcript." The fix binds the conversation to an id absent from the seeded
    `sessions.json` (`clearSessUUID`) so the first message mints it a real session through the
    revive seam (`sessionRouter.revive` → `Pool.Revive` → `materialise` → `buildSession`), the
    same seam `TestInteractiveConversationSystemPrompt_ReachesTheReply` uses.
  - **The safety net under a live-model assertion has to be a deterministic check on the
    daemon's own state, not a second prompt.** Once a live recall comes back empty, "the fact
    never entered the note" and "the successor didn't use it" are indistinguishable from the
    wire — `ResettingPayload` never carries note bytes, by design. Pairing the recall with a
    better-worded prompt is the same fabric as the thing being tested and can't rule either
    reading out. `assertPerSessionPrompt` reads the daemon's own `spawning claude` log record
    and fails immediately if the prompt file isn't under `session-prompts/`, seconds after the
    plant turn and well before the 90s wrap-up runs — turning a seeding mistake into a failure
    that names its own cause instead of one more ambiguous recall miss.
  - **Argv evidence has to be read for what kind of value it is, not just whether it matches.**
    An earlier pass excluded "the note never reached the child" partly because predecessor and
    successor spawned with the *same* `--append-system-prompt-file` — treated as reassurance.
    That sameness was the tell: the shared path was the daemon-scoped bootstrap prompt, not a
    per-session one. Matching paths prove nothing about which prompt file is on either end of it.
  - **A live budget constant can owe its margin in either direction.** This suite's standing
    lesson is that a budget should sit comfortably *under* the production timeout it's meant to
    catch early. This window is the inverse: `wrapUpDeadline` (90s) bounds the idle wait, the
    wrap-up turn, and the note write, so reusing the existing `rotateBudget` (45s) would expire
    before the daemon's own bound does and misreport a slow-but-correct wrap-up as a daemon
    fault. `clearResetWindowBudget` (150s) is sized against `wrapUpDeadline`, not borrowed.
  - **A retired premise leaves citations behind in files this kind of ticket doesn't otherwise
    touch.** `compaction_capture_test.go` and `compacting_edges_test.go` cited this file's old
    behavior as evidence that a slash command sent as ordinary message text is honoured on the
    stream-json input path. Their conclusion about `/compact` still holds (`/clear` is the only
    intercepted literal), but the cited evidence was gone — worth grepping for the retired claim
    itself, not just the retired test name, when a premise underneath a test changes.
  - **When a test's next step is gated on a state machine, find what else writes that state
    before assuming the obvious frame is the trigger.** (Carried from #2138, still true of the
    reworked case.) `transitionClearsTurn` answers `(successor, true)` for `ReasonClear` and
    clears the turn mark, so the recall turn can go out the moment the `session_transition`
    arrives — never gated on a terminal frame of the `/clear` turn itself, which under the
    daemon-run reset never produces a turn to close.
  - **A "returns the first X" drain can't be retrofitted into a counting one.** (Also carried
    from #2138.) Counting — three ordered `resetting` edges, one transition, zero
    `unrecognized_message` — has to happen inside the one primitive every frame passes through
    (`resetWindow.next`), because the Noise receive-nonce discipline forces a single reader for
    the whole window; a second concurrent reader desyncs the `CipherState` and the failure
    surfaces as an unrelated decrypt error many frames later.

**Related:**
[interactive_stream_hook_blocked_banner_test.go](e2e-realclaude-interactive-stream-hook-blocked-banner-test-go.md)
copies this file's window-primitive shape (`resetWindow`-style single-reader drain) for a turn
that can't satisfy the shared drain either, but sends its second turn on a different signal — a
hook-refused turn has no closing boundary to race a transition against, unlike `/clear`.
