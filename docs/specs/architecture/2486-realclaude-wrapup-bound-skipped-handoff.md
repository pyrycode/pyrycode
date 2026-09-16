# Spec — #2486: a testing bound on the wrap-up, and the live proof that missing it yields `handoff: skipped`

## Files read

- `cmd/pyry/session_reset.go` → `wrapUpDeadline`, `conversationReset` (its `deadline` field), `conversationReset.bound`, `newConversationReset`, `conversationReset.wrapUp`, `composeWrapUpPrompt`, `storeNote` — the bound's definition, the seam that already documents zero as the production default, and the closed set of exits that report `skipped`.
- `cmd/pyry/main.go` → `pyryFlagValues`, `splitArgs`, `runSupervisor`, `printHelp`, `resetThenRotate`, `resolveBoundSession` — the argv split a new value flag must be named in, the flag declarations, and the tail that emits the three edges around `wrapUp`.
- `internal/streamsup/runner.go` → `Runner.WriteUserTurn`; `internal/streamsup/envelope.go` → `WriteTurn` — the measurement behind § *Where the expiry actually lands*: neither consults the context's deadline.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker.WaitIdle` — returns nil at once when the conversation is not busy, without consulting `ctx`. The other half of that measurement.
- `internal/relay/handlers/send_message.go` → `isClearCommand` and the intercept around it — sits above the enqueue and above `touchConversation`, so a `/clear` never opens a turn; this is why the conversation is idle when `wrapUp` runs.
- `internal/e2e/realclaude/interactive_stream_announced_reset_test.go` → `newResetWindow`, `resetWindow.next`, `resetWindow.awaitReset`, `assertResetEdge`, `windowTransition`, `clearResetWindowBudget` — #2485's drain, reused verbatim; and its `edges[1].Handoff == skipped` guard, which names this ticket as the owner of the inverse proof.
- `internal/e2e/realclaude/harness_daemon_test.go` → `spawnBootstrapDaemon`, `bootstrapDaemon`, `seedBootstrapRegistry`, `seedBoundConversation`, `readPersistedServerID`, `waitBinaryHello`, `shortSocketPath`, `ensurePyryBuilt`, `lockedBuffer` — the spine and the seeding helpers.
- `internal/e2e/realclaude/interactive_stream_resume_after_eviction_test.go` → `spawnBootstrapDaemonWithIdle` — the precedent for a self-contained spawn variant carrying one extra *pyry* flag, and the reason it is a fork rather than a widening of the shared helper.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` → `writeStreamInteractiveConfig`; `interactive_per_conversation_liveness_test.go` → `perTurnReplyBudget`; `harness_session_control_test.go` → `rotateBudget` — the runner toggle and the two budgets this file sizes against.
- `internal/sessions/handoff.go` → `handoffNotesDir`, `handoffNotePathFor`, `Pool.HandoffNote`, `MaxHandoffNoteBytes` — the derivation the test composes by hand, both names being unexported.
- `internal/sessions/systemprompt.go` → `FencedHandoffNote`, `admissibleHandoffNote` — the admissibility the seeded note has to satisfy so it composes into the wrap-up prompt.
- `internal/conversations/id.go` → `ValidID` — the gate `handoffNotePathFor` applies to the conversation id, which fixes the shape of the id this file may seed.
- `docs/specs/architecture/262-pyry-conv-sweep-interval-flag.md` — the shape the new flag copies: default `0`, zero means production, the `(testing)` annotation in the help text.

## Context

`conversationReset.wrapUp` bounds the whole wrap-up at `wrapUpDeadline` — ninety seconds, fixed in the daemon and deliberately not operator-editable. Crossing it returns false, the `restarting` edge carries `skipped`, the previous note stands, and the rotation still completes. #2485 proved the happy path of that reset live and guarded its own assertion against accepting `skipped`, naming this ticket as the owner of the other side.

Nothing in production sets `conversationReset.deadline`, so a daemon the live suite spawns always has the full ninety seconds — longer than a live case should sit and not a bound the case can force. The suite needs a bound it can arm itself, under the plain `make e2e-realclaude` invocation, with no environment variable the make target would have to set: a case gated on a variable the target never sets skips on every run while the suite still exits 0, which is the false-green #1168 shipped through.

No ADR is warranted. This is a testing override in the shape `-pyry-conv-sweep-interval` already established, not a new boundary.

## Design

### 1. `-pyry-wrapup-deadline`

A `flag.Duration` on the daemon, default `0`, in three places in `cmd/pyry/main.go`:

- `pyryFlagValues` gains `"pyry-wrapup-deadline": true`, so `splitArgs` consumes the flag **and its value** before the split tips into claude territory. Omitting this entry is the failure the ticket names: the flag reaches claude and the daemon never sees it.
- `runSupervisor` declares it beside `convSweepInterval`: `"shorten the conversation reset's wrap-up bound (testing; 0 or above the 90s default = production default)"`.
- `printHelp` gains one line in the pyry-flags block, next to `-pyry-conv-sweep-interval`, carrying the same `(testing)` annotation and naming the ceiling § 2a adds.

Default `0` rather than `wrapUpDeadline`, for #262's reason: the production-default path is *"the operator did not set the flag"*, not *"the operator set it to ninety seconds"*, so one definition of "use the default" exists and it is `conversationReset.bound`.

### 2. The wiring site

`newConversationReset` gains a `deadline time.Duration` parameter between `queue` and `log`, assigned straight to the struct's `deadline` field. One production caller (`runSupervisor`'s `activeSessionStarter` literal); the unit tests build `conversationReset` literals directly and set `deadline` themselves, so none of them changes.

**No resolution rule at the wiring site.** `conversationReset.bound` already answers `wrapUpDeadline` for anything `<= 0`, so zero *and* a negative both mean production — a second rule here is how the two drift apart. This is the same division `sessions.New` keeps for `Config.SweepInterval`, except that here the resolution already had a home before the flag existed: the `deadline` field's own doc says zero means `wrapUpDeadline`, and it says so because a unit test needed to cross the bound without sleeping ninety real seconds. The flag is that seam reaching a spawned daemon; it is not a second seam.

### 2a. The flag may only SHORTEN — the ceiling stays where it was

`bound()` gains a ceiling alongside its existing floor: a `deadline` above `wrapUpDeadline` answers `wrapUpDeadline`, exactly as a zero one does.

This is the security pass's one MUST FIX against the first draft of this plan, and it is the ticket's own words that make it one. `wrapUpDeadline`'s doc states the invariant in the negative: the figure is *"fixed in the daemon with the prompt and not operator-editable, for the reason the prompt is: an operator-supplied bound would be a way to hold a child open."* A bare override flag repeals that sentence — `-pyry-wrapup-deadline=24h` would put every `/clear` into a day-long wrap-up, with the conversation claimed by `begin` (so no second reset can recover it) and the outgoing child held for the duration.

A shorten-only flag gives the live suite everything it needs while leaving the invariant true as written: no value of this flag lets any wrap-up outlive ninety seconds. Naming it `-pyry-wrapup-deadline` and documenting it as *"shorten the … bound (testing; 0 = production default of 90s)"* keeps the help text honest about that.

The ceiling lives in `bound()` and not in `runSupervisor`, deliberately: `bound()` is the single function that answers what bounds a wrap-up, and it is total over every construction path — including the struct literals `cmd/pyry`'s unit tests build directly, which `newConversationReset` never sees. A ceiling anywhere above it would be a ceiling with a way around it. It changes no existing test: every `deadline` those fixtures set is 2 s or below.

It does depart from `-pyry-conv-sweep-interval`, which is uncapped in both directions. The difference is that its knob carries no such documented invariant, and this one does — in its own doc comment, in words, with the reason attached.

### 3. Where the expiry actually lands — a correction to the ticket's forecast

The ticket's technical notes say the case "need not spend a wrap-up turn … with the bound short enough, the expiry lands before the prompt is ever written." **Measured at `201deaa2`, that does not hold, and the design does not depend on it.** Walking `wrapUp` with an already-expired `ctx` and a live idle child:

- `dropBacklog` and `resolve` do not consult `ctx`.
- `interruptRunner` does not take one.
- `turnBusyTracker.WaitIdle` returns `nil` **immediately** when the conversation is not busy — the membership test precedes the select, so an expired `ctx` is never reached. And the conversation *is* idle: the `/clear` intercept in `send_message.go` sits above the enqueue and above `touchConversation`, so it opens no turn.
- `Runner.WriteUserTurn` → `WriteTurn` reads `ctx` only for the `turncommit` gate, which is nil on this path. The prompt is written.

So the expiry lands in `reply.wait(ctx)`, logs `reset.wrapup.deadline`, and returns false. That is the same exit the production ninety-second bound takes against a live child — the write cannot fail on a deadline, so `reply.wait` is the *only* place a timed-out wrap-up can land when a child is live. The case therefore proves production's actual `skipped` shape rather than a synthetic one, and it costs a partial wrap-up turn that `startFreshRunner`'s teardown cuts short milliseconds later.

Every other `skipped` exit converges on the same observable outcome, which is why the assertions name the outcome and not the step (the ticket's own instruction): `storeNote` is not reached, so the seeded note stands.

### 4. The live case

New file, `internal/e2e/realclaude/interactive_stream_clear_wrapup_skipped_test.go`, holding `TestInteractiveStreamClearWrapUpSkipped`. A sibling file rather than a second case in #2485's, so the two do not share identifiers and neither file's merge surface grows; the drain it reuses is package-level by #2485's own design.

Identifiers follow the package's ticket-stem convention (`20390000-…`, `16560000-…`), not the repeated-digit one #2485 exhausted:

```
skipBootstrapUUID = "24860000-0000-4000-8000-000000000001"
skipSessUUID      = "24860000-0000-4000-8000-000000000002"
skipConvID        = "24860000-0000-4000-8000-000000000003"
```

All three satisfy `conversations.ValidID` (version nibble `4`, variant nibble `8`), which `handoffNotePathFor` gates the note path on.

The spine is #2485's down to the handshake: `WithWorktreeAuthenticated`, an isolated workdir, `writeStreamInteractiveConfig` before the spawn (only `streamRunner` implements `wrapUpCapturer`), `fakerelay`, `paireddevice.Setup`, `seedBootstrapRegistry`, `seedBoundConversation` binding `skipConvID` to `skipSessUUID` — an id absent from `sessions.json`, so the first message mints the conversation its own session rather than talking to the bootstrap. One reader for the whole run; no `t.Parallel`.

Two deltas from #2485:

- **The daemon is spawned with the new flag.** A local `spawnBootstrapDaemonWithWrapUpBound` — `spawnBootstrapDaemon`'s argv plus `-pyry-wrapup-deadline=<bound>` before the `--`. A fork rather than a widening, exactly as `spawnBootstrapDaemonWithIdle`'s doc argues: `spawnBootstrapDaemon`'s variadic tail appends *after* the `--` and would send a pyry flag to claude, and a signature change to the shared helper fans out to every live caller in a package `make check` never compiles.
- **A handoff note is seeded before the daemon starts**, at `<home>/.pyry/test/handoff-notes/<skipConvID>.txt`, directory `0700`, file `0600` — `handoffNotePathFor`'s derivation composed by hand, since both it and `handoffNotesDir` are unexported. Its text is plain ASCII prose admissible to `sessions.FencedHandoffNote` (no control characters, no fence marker, non-blank), because a seeded note is also composed into the wrap-up prompt by `composeWrapUpPrompt` and an inadmissible one would silently take a different branch there.

`assertPerSessionPrompt` is deliberately **not** re-run. It is the deterministic precondition for #2485's note-*recall* half, which this case drops; the note store is keyed by conversation id, not by session, so AC 3 does not rest on which prompt file the successor gets.

**The bound.** `wrapUpSkipBound = 1 * time.Millisecond`, passed as the flag's value. Deterministic rather than merely likely: the one way to reach `storeNote` is for claude to answer a whole turn within a millisecond of `wrapUp` entry, which no network round trip does.

**The window budget.** A named `skippedResetWindowBudget = 60 * time.Second`, and it inverts `clearResetWindowBudget`'s argument rather than copying it. That constant must stay *well above* `wrapUpDeadline` because the daemon's own ninety seconds sit inside the window it measures. Here the wrap-up is bounded at a millisecond by construction, so what remains between the `/clear` send and the last of the four frames is the rotation and the fresh spawn — `rotateBudget` (45 s) plus slack. Sizing this at 150 s would make a red mean nothing; sizing it at 60 s means a window that misses it missed the rotation, which is the only thing left in it.

### 5. What is asserted

Over #2485's drain (`newResetWindow`, `awaitReset`, `assertResetEdge`), which waits on exactly the three-edges-and-one-transition shape a skipped wrap-up produces:

| AC | Assertion |
|----|-----------|
| 1 | Implicit and load-bearing: the daemon is spawned by `make e2e-realclaude`'s own `go test` with the flag in its argv and no environment variable involved. The reset reaching `skipped` at all is the proof the flag crossed `splitArgs` into the daemon rather than into claude — a flag missing from `pyryFlagValues` yields the ninety-second bound, a `written` edge, and a named failure. |
| 2 | `edges[0] == {conv, true, wrapping_up, pending}`, `edges[1] == {conv, true, restarting, skipped}`, `edges[2] == {conv, false, "", ""}`, each by whole-payload equality through `assertResetEdge`. One `session_transition` with `reason == "clear"` and `conversation_id == skipConvID`, arriving with `edgesBefore >= 2`. **No order is asserted between the transition and the falling edge** — the header restates #2485's measured argument for why, and the non-blocking hand-off in `sessionTransitionEmitterV2` is #659's requirement that must not be traded for a tidier assertion. |
| 3 | The note file's bytes after the window equal the seeded bytes, by `bytes.Equal` on `os.ReadFile`. |

Plus the window-wide negatives: exactly 3 `resetting` frames and exactly 1 `session_transition` across the whole window. To keep those non-vacuous — `awaitReset` returns the instant those counts are met — the window is settled for a short quiet period after the reset by a local helper that reads frames until a deadline passes and asserts nothing further arrived. That settle is also what puts the killed wrap-up turn's tail and the fresh spawn's frames through `resetWindow.next`'s two standing negatives (`unrecognized_message`, `error`) rather than leaving them unread.

`resetWindow.next` fatals on `protocol.TypeError`, which is a correlated *reply* type throughout `internal/relay`; an unsolicited terminal error is `protocol.TypeSessionError`, which the switch ignores. So a child torn down mid-turn does not trip that negative.

### 6. Security posture of the case

The seeded note is the test's own literal — not claude's words and not the operator's. It is still read back and compared without being logged on the green path: the green log carries a byte count and the equality verdict, and the bytes appear only inside a failure message, where the *observed* side may be claude's wrap-up reply if the store were wrongly overwritten. That is #2485's posture kept for #2485's reason. Nothing is written to disk outside the test's own `HOME`, and no capture is committed.

## Concurrency model

Unchanged. `conversationReset.deadline` is set once at construction in `runSupervisor` and read only through `bound()` from the reset goroutine, which is the field's existing discipline. No new goroutine, no new lock, no change to `resetThenRotate`'s single-goroutine emission of the three edges.

On the test side, the one-reader discipline is #2485's and is preserved: the receive nonce is sequential, so every `noise_msg` is decrypted in arrival order by `resetWindow.next` and non-`noise_msg` control frames are skipped without decrypting. The settle helper reads through the same `next`, so it does not introduce a second reader.

## Error handling

No new error paths in production. `flag.Duration` rejects a malformed value at parse time and `runSupervisor` already returns that error. A negative value is a valid `time.Duration` and degrades to the production default through `bound()`'s `<= 0` test, the same way `-pyry-conv-sweep-interval`'s does.

The case's own failure modes are named in its assertion messages: a `written` edge means the flag did not reach the daemon (most likely the `pyryFlagValues` entry) or the bound was not honoured; a window timeout means the rotation did not complete, since the wrap-up cannot be what is slow; changed note bytes mean `storeNote` was reached on a path the bound should have closed.

## Testing strategy

- **The live case above** is the whole of it, and it is `needs-real-claude`: it is compiled only under `-tags e2e_realclaude` and is read from the count of executed tests, never from an exit code.
- **No new unit test for the flag.** `cmd/pyry`'s existing `conversationReset` unit tests already pin the resolution rule this flag feeds (`bound()` answering `wrapUpDeadline` for zero, and the honoured non-zero deadline across the `resetOptions{deadline: …}` fixtures), and `runSupervisor`'s flag-to-field plumbing has no in-process test harness — the same boundary #262 drew for `-pyry-conv-sweep-interval` and for the same reason.
- **Offline gate for this branch:** `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`, plus a tag-enabled compile of the live package (`go vet -tags e2e_realclaude ./internal/e2e/realclaude/`) — `make check` never compiles that package, so a mistake there is otherwise invisible until a live run.

## Open questions

1. **Does a millisecond bound leave any window in which the wrap-up could still store a note?** Resolved in § 3 by reading `WaitIdle`, `WriteUserTurn` and `WriteTurn`: no, and the expiry's landing site is `reply.wait` rather than the write. Recorded as a correction to the ticket's forecast rather than a change of design.
2. **Should the case bind to the bootstrap session instead, since the recall half is dropped?** Resolved in § 4: no. Minting the conversation its own session keeps this case and #2485 differing in exactly one variable — the bound — and keeps the daemon's shared bootstrap out of a reset.

## Documentation handoff

Owned by the documentation stage; **pending**, not done here.

- **Path:** `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`
- **Section:** *The wrap-up turn, and the reply's tense (#2477)*
- **Requirement:** a line recording that `-pyry-wrapup-deadline` exists, that it is a testing override that can only SHORTEN the bound, and that zero (its default, and the value every production daemon runs with) means `wrapUpDeadline`'s ninety seconds — so an operator learns the flag exists, that it defaults to production, and that it cannot be used to hold a child open past the ceiling. The fold of `-pyry-conv-sweep-interval` into `docs/knowledge/features/conversations-auto-archive.md` is the shape to copy.

## Security review

**Verdict:** PASS (after the revision in § 2a; the first draft of this plan failed on the finding below).

**Findings:**

- [Trust boundaries] **MUST FIX — fixed in § 2a before this plan was committed.** The first draft added a bare override of `wrapUpDeadline`, whose own doc fixes the value *"in the daemon with the prompt and not operator-editable … an operator-supplied bound would be a way to hold a child open."* An uncapped flag repeals that sentence: `-pyry-wrapup-deadline=24h` holds the outgoing child for a day per `/clear`, with `conversationReset.begin` refusing any second reset that could recover the conversation. The fix is a shorten-only ceiling in `bound()`, which is total over every construction path. The containment that remains is the one that already held: the bound is reachable **only** from the daemon's own argv — there is no control verb, no relay handler and no config-file key that sets it, so no paired client can influence a wrap-up's duration.
- [Tokens, secrets, credentials] No findings — nothing in this change mints, stores, compares or transports a credential. The live case reuses `WithWorktreeAuthenticated`'s existing isolated `HOME` and adds no secret of its own; unlike #2485 it plants no per-run token, the recall half having been dropped.
- [File operations] No findings, and one decision worth naming. Production adds no file operation at all. The test composes the note path by hand — `handoffNotePathFor` and `handoffNotesDir` are both unexported — from a **compile-time constant** conversation id, so there is no caller-controlled segment and no traversal to canonicalise; the id is additionally of the shape `conversations.ValidID` admits, which is what the production derivation gates on. The seeded file is written `0600` inside a `0700` directory, matching `writeHandoffNoteFile`'s own modes so the fixture cannot leave the store's posture weaker than the daemon would.
- [Subprocess / external command execution] No findings. The flag reaches the daemon as one element of an explicit `exec.Command` argv — no shell, no interpolation — and `spawnBootstrapDaemonWithWrapUpBound` takes a `time.Duration` rather than `spawnBootstrapDaemonWithIdle`'s string, so an unparsable literal cannot be constructed at the call site at all. `flag.Duration` rejects a malformed value at parse time on the daemon side.
- [Cryptographic primitives] No findings, one invariant preserved. The case adds no crypto and inherits the Noise channel. The property that had to survive is the single-reader discipline: the receive nonce is sequential, so the settle helper in § 5 reads through `resetWindow.next` rather than opening a second reader, which would desync the `CipherState` and surface as an unrelated decrypt error many frames later.
- [Network & I/O] No findings — no listener, no new frame type, no new read from a socket, and therefore no new size cap or timeout owed. The one *reduction* in an existing bound is the ceiling in § 2a.
- [Error messages, logs, telemetry] No findings. The production record on the path this case drives is `reset.wrapup.deadline`, which already carries an event key and the conversation id and no reply bytes — the rule `storeNote` states absolutely rather than per call site. Nothing in this change adds a record, and the flag's value is never logged (it is visible in `ps`, where a duration is not a secret). Test-side, the ticket's own security note is honoured: the seeded note's bytes reach no green-path log line — the green log carries a byte count and the equality verdict — and appear only inside a failure message, where the *observed* side may be claude's wrap-up reply if the store were wrongly overwritten and a human is already reading. Nothing is written outside the test's `HOME` and no capture is committed.
- [Concurrency] No findings, one check recorded. `deadline` is set once at construction and read only through `bound()` on the reset goroutine; no lock, no goroutine, no shared mutable state is added, and `resetThenRotate`'s single-goroutine emission of the three edges is untouched. The state an early `reply.wait` exit could have leaked is the armed reply capture, and `wrapUp` releases it through `defer stop()` on every exit below the arm — so a skipped wrap-up does not leave a capture armed that would make `wrapUpCapture.begin` refuse the next reset on the same runner.
- [Threat model alignment] No findings. `docs/protocol-mobile.md` § Security model's relevant threat is a paired client influencing daemon-side resource holds; § 2a's containment answers it — the bound is argv-only and, after the ceiling, cannot exceed what an unflagged daemon already permits. Nothing is deferred to a future ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
