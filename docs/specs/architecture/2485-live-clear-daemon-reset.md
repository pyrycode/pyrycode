# 2485 — the live `/clear` case asserts the daemon-run reset and the note it hands the successor

## Files read

- `internal/e2e/realclaude/interactive_stream_announced_reset_test.go` → `TestInteractiveStreamAnnouncedResetFollowsLiveClaude`, `resetWindow.next`, `resetWindow.awaitTransition`, `resetWindow.awaitCompletedTurn` — the case to update. Its drain is the window's only frame reader, so every new assertion has to be collected there rather than beside it.
- `internal/relay/handlers/send_message.go` → `isClearCommand`, `SendMessage`'s intercept arm, `ConversationResetter` — what a client `/clear` does since #2456: the ack goes out first, then `StartNewSession`, and the text never reaches claude. The intercept sits below `Route`, so the id crossing the seam is a registry key with a live binding.
- `cmd/pyry/main.go` → `activeSessionStarter.start`, `activeSessionStarter.resetThenRotate` — the edge sequence, the live-child gate (`State().ChildPID != 0`, which is why turn 1 is load-bearing), and the deferred falling edge.
- `cmd/pyry/resetting_v2.go` → `resettingEmitterV2.wrappingUp`, `restarting`, `done`, `emit` — the three edges, the synchronous per-conn Push, and the `c.Interactive` gate the phone clears via `driveHandshakeInteractive`.
- `cmd/pyry/session_reset.go` → `wrapUpDeadline`, `wrapUpPromptText`, `conversationReset.wrapUp`, `storeNote` — the 90 s bound the test's own budget must clear, the prompt clause that carries a planted fact into the note, and the three ways `wrapUp` reports `skipped`.
- `internal/sessions/handoff.go` → `FencedHandoffNote`, `admissibleHandoffNote` — what `storeNote` judges a reply by: non-empty, valid UTF-8, no control runes, no fence line. Ordinary prose passes, so a live `skipped` is a real finding rather than expected noise.
- `internal/protocol/interactive.go` → `ResettingPayload`, `ResetPhaseWrappingUp`, `ResetPhaseRestarting`, `ResetHandoffPending`/`Written`/`Skipped` — the wire shape, the closed sets, and the standing statement that the note's content does not cross this frame.
- `internal/sessions/transition.go` → `ReasonClear`, `Pool.RotateForNewSession` — the transition the rotation fires, and the ids it carries.
- `internal/e2e/realclaude/interactive_stream_multiturn_continuity_test.go` → `TestInteractiveStreamMultiTurnContinuity`, `drainForCompletedTurnText` — the plant-and-recall precedent: a per-run `PYRY<nonce>` hex token matched with `strings.Contains` over `strings.ToUpper`.
- `internal/e2e/realclaude/harness_session_control_test.go` → `rotateBudget`, `idSettleTimeout`, `uuidStemPattern`, `waitBootstrapID` — the budgets this case reuses, and the one (`rotateBudget`, 45 s) it cannot.
- `docs/knowledge/features/e2e-realclaude.md` → the #2416 budget-constant lesson (a live gate's budget constant states its margin against the production timeout it is measured against) and the receive-nonce discipline for Noise frames.

## Context

Since #2456 a client's `/clear` is intercepted in `SendMessage` and runs the conversation reset instead of reaching claude. The live case still on disk,
`TestInteractiveStreamAnnouncedResetFollowsLiveClaude` (#2138), asserts the retired premise: that a live claude announced a `conversation_reset` on its own stdout for that input. It cannot go red for the right reason any more, and the shipped path — three `resetting` edges, one `clear` transition, and a handoff note composed into the successor's appended system prompt — has no live coverage at all. The `resetting` frame has no end-to-end coverage anywhere, fake or live, at `11c46d68`.

Both halves ride one live reset, so this is one round trip: the edges and the note-reaching-the-successor proof come out of the same 90-second wrap-up.

No ADR is warranted. This is a test-only change against contracts #2453, #2475, #2477 and #2478 already recorded.

## Design

No production change. One test file is rewritten: `internal/e2e/realclaude/interactive_stream_announced_reset_test.go`.

**The file keeps its path.** `docs/knowledge/features/e2e-realclaude.md`'s entry links it and names a child page after it, and #2486 extends the same drain; renaming would push doc churn into a stage this ticket hands off to. The test FUNCTION is renamed to `TestInteractiveStreamClearRunsDaemonReset`, because the old name asserts the premise this ticket retires and a test name is prose (AC 3).

**Spine, unchanged.** The setup through `driveHandshakeInteractive` is transcribed from the current file with no deltas: `WithWorktreeAuthenticated`, the isolated workdir, `writeStreamInteractiveConfig`, `fakerelay` + `paireddevice.Setup`, `seedBootstrapRegistry`/`seedBoundConversation` over the existing `clearBootstrapUUID` / `clearConvID`, `spawnBootstrapDaemon`, one phone, one reader. No `t.Parallel` (`t.Setenv`).

**Shape of the case.**

1. **Turn 1 — the plant.** One ordinary `send_message` telling the session a distinctive per-run fact as something *the user* says, then `drainForCompletedTurn`. Load-bearing three ways: `activeSessionStarter.start` refuses a named conversation with no live child, the plant has to be a user statement for `wrapUpPromptText`'s last clause to carry it into the note, and this is the one turn whose shape is not in question, so it takes the shared drain's standing alarms.
2. `waitBootstrapID` for the pre-reset id, asserted equal to the seeded stem, exactly as today — the transition's `previous_session_id` anchor.
3. **The `/clear` send.** Ordinary `send_message` text, `"/clear"`. The window opens here.
4. **The reset window.** One wait reads forward until three `resetting` edges and one `session_transition` have arrived, then the case asserts them.
5. **Turn 2 — the recall.** Sent after the window closes, asking for the planted fact back. Its first reply must carry the fact, which the test never repeated to the successor.
6. The window-wide counts are re-asserted after turn 2 closes.

**The drain.** `resetWindow` stays the single frame reader; its `next` keeps the receive-nonce discipline (decrypt every `noise_msg` in arrival order, skip non-`noise_msg` control frames without decrypting) and the two window-wide negatives, and gains three collections:

```go
type resetWindow struct {
	edges       []protocol.ResettingPayload
	transitions []windowTransition   // payload + how many edges preceded it
	turnIDs     map[string]struct{}  // every assistant_delta turn id seen so far
	acked       map[uint64]struct{}  // ack in_reply_to ids, for the diagnostics
}
```

Two waits over it:

- `awaitReset(t, phone, cs, timeout) ` — reads until three edges and one transition have arrived; on the deadline, a Fatal naming which of the four is missing and whether the `/clear` was acked.
- `awaitRecallReply(t, phone, cs, convID, reqID, prior, timeout) string` — ack correlated on `reqID`, then the first non-empty `assistant_delta` for `convID` whose turn id is NOT in `prior`, accumulating that turn's text, closing on the terminal `turn_state{idle}` for `convID`.

**The turn-id exclusion is new, and it reverses the current file's decision deliberately.** Today's `awaitCompletedTurn` documents having no exclusion set, on the grounds that any post-reset delta is already the property under test. That reasoning does not survive this ticket: the predecessor's wrap-up reply reaches the client too (`wrapUpCapture.Sink` forwards downstream unchanged) and it *contains the planted fact by design*, so a late wrap-up delta counted as the successor's would pass the recall assertion vacuously. The set is taken as a snapshot immediately before the recall send.

**Ordering, per the ticket's measurement.** The three edges are asserted in order, because they are pushed synchronously from `resetThenRotate`'s own goroutine onto a per-conn FIFO. The transition is asserted only to arrive *after* the `restarting` edge (`edgesBefore >= 2`); nothing asserts an order between the transition and the falling edge, because `Pool.notifyTransition`'s observer is `sessionTransitionEmitterV2.Enqueue`, a non-blocking send into a buffer drained on another goroutine. The emitter is not to be made synchronous to tidy this up — the non-blocking hand-off is #659's requirement.

**Budget.** A new file-local constant, sized against the daemon's own bound rather than reused:

```go
// The whole reset, not a turn: wrapUpDeadline bounds the idle wait, the wrap-up
// turn and the note write at 90 s, and the rotation follows it.
const clearResetWindowBudget = 150 * time.Second
```

`rotateBudget` (45 s) expires before `wrapUpDeadline` does, so a slow but correct wrap-up would redden as a daemon fault that is really the test's budget. Turn 1 and the recall keep `perTurnReplyBudget` (120 s), which is the cold-spawn-plus-first-reply figure the recall needs — the successor's child spawns on its first message.

**Out-of-band prose repair, two comments.** `compaction_capture_test.go` and `compacting_edges_test.go` each cite this case as evidence that "a slash command sent as ordinary message text is honoured on this input path". Their conclusion about `/compact` stands (`/clear` is the only intercepted literal), but the cited evidence is retired by this ticket. Each gets a one-sentence repair, no behaviour change.

## Concurrency model

The test spawns no goroutines. One reader for the whole run: the receive nonce is sequential and a second concurrent reader would desync the `CipherState`, surfacing as an unrelated decrypt error many frames later. Everything the case observes crosses one phone conn in wire order.

Daemon-side concurrency the test must tolerate rather than control: the three edges on `resetThenRotate`'s goroutine (ordered), the transition on the emitter's `Run` goroutine (unordered against the falling edge), and the predecessor's wrap-up deltas on the turn-event fan-out. The exclusion set above is what makes the last of those harmless.

## Error handling

- **No `resetting` edge at all** → the `/clear` was not intercepted, or the reset was refused inert (no live child, a reset already in progress, an unwired seam). The Fatal names those readings and reports whether the `/clear` ack was seen, which separates "the frame never landed" from "the frame landed and nothing followed".
- **`restarting` carrying `skipped`** → a finding about the wrap-up prompt or the 90 s bound, not something to accept. AC 1 is not relaxed to take either token; the failure message says to route it, and names #2486 as the ticket that owns the `skipped` proof.
- **Recall miss** → two readings this test structurally cannot separate, because `handoff: written` already proves a note was stored: either the fact never entered the note, or the successor did not use it. Both are named in the failure message. The note's bytes are not reached for.
- **`unrecognized_message` inside the window** → kept as a window-wide Fatal, reframed. Since `/clear` is no longer delivered, one naming `conversation_reset` now means some *other* input path still produces that line — a new ticket, not this one.
- **`error` envelope inside the window** → Fatal, unchanged.
- Absent `claude` or absent credentials skip cleanly (exit 0) through the reused setup, as every sibling does. Green is read from the count of executed tests, never from the exit code.

## Testing strategy

The deliverable is the test. It is proven by the live gate (`make e2e-realclaude`, plain invocation, no flag of its own — AC 4). Builder-side offline verification is `go vet ./...`, `go build ./cmd/pyry`, and a tagged compile of the suite (`go test -tags e2e_realclaude -run XXX ./internal/e2e/realclaude/`), which is what catches the failure mode `make check` structurally cannot: the package is behind `e2e_realclaude` and the standard gate never compiles it.

Non-vacuity: the recall cannot pass on a respawned, memory-less child — the successor's only route to the fact is the note in its appended system prompt — and the per-run nonce defeats any cross-run caching. The token is contiguous uppercase hex (`PYRY%X`) with no internal separators for claude to reformat, matched case-insensitively as a substring so surrounding words and case-folding cannot produce a false red.

## Open questions

1. **Does a live wrap-up reliably produce `written`?** `admissibleHandoffNote` is permissive (non-empty, valid UTF-8, no control runes, no fence line), so ordinary prose passes; the residual risks are the 90 s bound and a disabled note store. Resolve on the first live run; a `skipped` is routed, not accommodated.
2. **Does the wrap-up turn produce any `unrecognized_message`?** The prompt forbids tool calls and questions, and no delegation is involved, so the window-wide negative should hold. If a live run says otherwise, the negative is what reports it.

## Revisions

**2026-09-16 — implementation.** The design landed as planned; two notes.

- **The exclusion set turned out to be the second of two guards, not the only one.** The recall drain already refuses any delta arriving before its own ack, and that guard rests on wire order: the ack is pushed when the daemon receives the recall frame, so every delta already in flight — the predecessor's wrap-up reply included — precedes it. The turn-id exclusion rests on turn identity instead and holds even where that ordering does not. Both are kept and both are stated at the code; they are different fabric, not the same check twice.
- **Both Open Questions stay open by construction, and are the live gate's to answer.** Neither is decidable offline: whether a live wrap-up produces `written` and whether the wrap-up turn emits any `unrecognized_message` are observations of a real claude, and the case is built so that each one reddens with a message naming its readings rather than passing quietly. Nothing in the design waits on them.

**2026-09-16 — first live gate: AC 1 green, AC 2 red, recall prompt repaired.** The gate ran the suite (1433 executed, 1 failed — this case) and the run is decisive about where the fault was.

- **Open Question 1 is ANSWERED: a live wrap-up produces `written`.** All four frames landed in the designed order — `wrapping_up`/`pending`, `restarting`/`written`, the `clear` transition after 2 edges, then the falling edge — and the daemon logged `reset.wrapup.note_written`. The 150 s window was ample: the whole reset took 3.3 s against a 90 s `wrapUpDeadline`. AC 1, AC 3 and AC 4 are met as designed, and no part of the reset path is implicated.
- **Open Question 2 is ANSWERED for this run: no `unrecognized_message` fired.** The window-wide negatives held.
- **AC 2 failed on the test's own recall phrasing, not on production.** The successor answered *"I don't see a build tag mentioned earlier in this conversation. This appears to be the first message."* That is a correct answer to what was asked. The recall said "Earlier in this conversation I told you my build tag", the successor's transcript is genuinely empty, so the reference resolved against the transcript, came up empty, and the premise read as the user's mistake. `handoffNoteLead` directs the successor to consult the note "when the user refers to earlier work, **or when you lack context the conversation seems to assume**" and not to act on it "unprompted" — and the old phrasing produced neither state. The repair names the previous session and concedes the transcript is empty, which reaches the second trigger without naming the note or supplying the token. AC 2 is not relaxed and the non-vacuity argument is unchanged: the only place the tag exists for that child is the note in its appended system prompt.
- **"The note never reached the child" was excluded, not assumed.** `Pool.writeComposedPrompt` targets `sess.systemPromptPath` VERBATIM across a re-key, and `RotateForNewSession` recomposes via `refreshSystemPromptForRotation` **before** `notifyTransition`. The gate's own argv record confirms it: predecessor and successor both spawned with the same `--append-system-prompt-file`, and the successor spawned ~0.9 s after the note was written. So the failure is one of the two readings the plan already named, and the failure message now says so — a future red should not go chasing a third.
- **The model is haiku and stays haiku.** The reliability lever used here is the prompt's alignment with `handoffNoteLead`'s documented triggers, not a stronger model: the gate's token cost is a standing constraint, and a prompt that only works on a larger model would hide the same latent mismatch.

## Documentation handoff

Owned by the documentation stage; pending, not done here.

- `docs/knowledge/features/e2e-realclaude.md` — the entry for `interactive_stream_announced_reset_test.go` currently describes the case as proving that a live `/clear` draws the *announced*-reset delimiter. It must describe what the case asserts after this ticket: the daemon-run reset's three `resetting` edges, the `clear` transition, and the handoff note reaching the successor.
- The child page that entry links (`docs/knowledge/features/e2e-realclaude-interactive-stream-announced-reset-test-go.md`) — same correction, at length. The file path is unchanged by this ticket, so the page keeps its name; the test function is now `TestInteractiveStreamClearRunsDaemonReset`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The boundary this ticket sits on is claude-authored note text crossing into the successor's appended system prompt, and the design does not move it: admission stays `sessions.FencedHandoffNote` via `conversationReset.storeNote`, and the test adds no second predicate over the same bytes. The test's own untrusted input is the two claude replies it reads off the wire; both are handled as opaque text — length-logged, substring-matched, never parsed, never written to disk, never used to build a path or a command.
- **[Tokens, secrets, credentials]** No finding, and the category is live rather than N/A: the run uses the operator's real credentials via `WithWorktreeAuthenticated`, and the pairing token and Noise static key come from `paireddevice.Setup` into a throwaway HOME. The test mints one value of its own, `PYRY%X` over `time.Now().UnixNano()` — a cache-defeating run marker, not a secret, so `math/rand`-grade entropy is adequate and `crypto/rand` would imply a property it does not have. It is printed in the prompt and in failure messages, which is correct for a value with no confidentiality.
- **[File operations]** The test writes nothing. This is the deliberate continuation of #2138's posture: the capture goes to the gate's log and never to disk, so this package's redaction apparatus (`newInitControlRedactor`, `newDropcapScanner`) is not pulled in for a value that does not need it. The only paths built are `filepath.Join(home, "work")` under a `t.TempDir`-rooted HOME, from no client-supplied component.
- **[Subprocess / external command execution]** `claude` is resolved through `exec.LookPath` and spawned by the daemon under test, not by the case; no `sh -c`, and no test-authored value reaches an argv. The planted token enters the child as message TEXT through the sealed relay path, which is the same channel every sibling's prompt uses.
- **[Cryptographic primitives]** No hand-rolled crypto. The Noise session is `internal/noise`'s, driven through `driveHandshakeInteractive`; the one discipline the test owes it is decrypting every `noise_msg` in arrival order from a single reader, which `resetWindow.next` keeps and which the new waits inherit rather than re-implement.
- **[Network & I/O]** All I/O is loopback against `fakerelay`. Every read is deadline-bounded — `clearResetWindowBudget` for the window, `perTurnReplyBudget` for each turn — so no wait is unbounded and a stalled daemon fails loud instead of hanging the gate.
- **[Error messages, logs, telemetry]** This is the category with the ticket's sharpest constraint, and the design meets it two ways. The capture `t.Logf` prints only daemon-authored, closed-set values: the three edges' `active`/`phase`/`handoff`, the transition's reason, ids and timestamp. `ResettingPayload` structurally cannot carry note bytes (`resettingEmitterV2` holds no note store, no reply text and no `conversationReset`), so the frames are safe to log verbatim. Claude-authored text — the wrap-up reply and the recall reply — is logged as a byte COUNT on the green path, and printed in full only inside a failure message, where a red run needs it and a human is already reading. **SHOULD FIX, carried into Phase B:** do not let the recall's green-path log echo the reply; the verifier can check that the only full-text print sits under a `t.Fatalf`.
- **[Concurrency]** The test takes no locks and spawns no goroutines, so there is no lock order and no leak to reason about. The one concurrency hazard it must respect is the receive-nonce single-reader rule above. The daemon-side race between the transition and the falling edge is accommodated in the assertion (`edgesBefore >= 2`) rather than removed by making `sessionTransitionEmitterV2.Enqueue` synchronous, which would undo #659's non-blocking hand-off to buy a tidier test.
- **[Threat model alignment]** `docs/protocol-mobile.md` § resetting's standing promise — that the frame reports only WHETHER a note was made, never its bytes — is exactly what this case asserts against; a future field carrying note text would fail the `handoff`-token assertions rather than pass them silently. Out of scope and named: whether any remaining input path can still produce a claude `conversation_reset` line (the scope fence keeps `streamsup`'s parser arm and the follower as they are), and the `skipped` outcome's live proof, which is #2486's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
