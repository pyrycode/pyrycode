# #2138 — realclaude: prove a live `/clear` draws the delimiter and keeps the stream alive

## Files read

- `internal/e2e/realclaude/interactive_stream_new_session_test.go` → `TestInteractiveStreamNewSessionRotatesAndSpawnsFresh` — the spine this test copies: same package, same seeded-bootstrap + bound-conversation setup, same "the fake proves the routing, not that real claude does the thing the fake imitates" reason for existing. Its header also states why its own second turn asserts only an ack — the constraint #2135 lifted and this ticket's AC 2 depends on being lifted.
- `internal/e2e/relay_v2_stream_announced_reset_test.go` → `driveTurn` — the counting-inside-the-drain precedent. Its header states why the count must happen inside the drain: the transitions arrive unsolicited, interleaved with the turn's own frames, and a later read has already discarded them.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` → `drainForCompletedTurn`, `writeStreamInteractiveConfig`, `warnRateLimitStatus` — the pre-turn drain this test reuses verbatim for turn 1, the stream-json toggle, and the two standing alarms (`unrecognized_message`, `rate_limited`) that turn 1 gets for free.
- `internal/e2e/realclaude/harness_daemon_test.go` → `sealSendMessage`, `driveHandshakeInteractive`, `spawnBootstrapDaemon`, `seedBootstrapRegistry`, `seedBoundConversation`, `readPersistedServerID`, `waitBinaryHello`, `runPyry`, `decodePairPayload` — every wire/spawn/seed helper, reused unchanged.
- `internal/e2e/realclaude/harness_session_control_test.go` → `waitBootstrapID`, `readBootstrapRowIfPresent`, `uuidStemPattern`, `rotateBudget`, `idSettleTimeout` — the on-disk id read that supplies AC 1's `previous_session_id` anchor, and the rotation budgets calibrated for real claude on `/clear`.
- `internal/e2e/realclaude/harness_modal_test.go` → `drainForControlEvent` — the drain this test deliberately does NOT use, and why: it returns the first `session_transition` but silently consumes every frame before it, which makes "exactly one" and "no `unrecognized_message`" unassertable after the fact.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `drainForReply`, `sealEnvelope`, `perTurnReplyBudget` — the reply correlator's shape and the 120 s per-turn budget.
- `cmd/pyry/stream_turn_busy.go` → `waitIdleForDelivery`, `openForDelivery` — **the mechanism that decides whether this test can exist at all.** A second `send_message` on the same conversation blocks at the delivery seam until that conversation has no open turn. Whether the `/clear` turn ever closes on its own is exactly this ticket's unknown, so AC 2's second turn hangs unless something else clears the mark.
- `cmd/pyry/session_transition_v2.go` → `transitionClearsTurn` — the something else. A `ReasonClear` transition yields the successor id and clears that session's turn mark, so the announced reset itself unblocks the delivery seam. This is why the test sends turn 2 on the transition rather than on a terminal frame of the `/clear` turn.
- `internal/msgqueue/queue.go` → `Enqueue`, `EnqueueDelivery` — `Enqueue` is `EnqueueDelivery` with the two halves equal, so a `send_message` with no attachment delivers its text verbatim. `/clear` reaches claude's stdin as `/clear`, unwrapped.
- `internal/sessions/transition.go` → `AdoptAnnouncedID`, `notifyTransition` — the transition is emitted only after `rekeyLocked` and `saveLocked`, which is why AC 1's frame already implies the re-key and a second assertion for it would restate.
- `internal/streamsup/parser.go` → `emitConversationReset`, `ignoredLineTypes` — the producer, and the reason a changed announcement shape surfaces as an `unrecognized_message` naming `conversation_reset` rather than as silence.
- `internal/protocol/messaging.go` → `SessionTransitionPayload`; `internal/protocol/interactive.go` → `AssistantDeltaPayload`, `TurnStatePayload`, `UnrecognizedMessagePayload` — the wire shapes asserted.
- `docs/specs/architecture/2135-follow-announced-reset.md` — the ordering contract this test observes from outside, and the `## Revisions` entry recording that the follower's tag rotation is what makes AC 2 assertable.
- `docs/specs/architecture/2134-conversation-reset-turn-event.md` — the provenance paragraph naming this ticket as the live confirmation the family rests on, and stating that no capture file in the tree holds a `conversation_reset` line.
- `docs/knowledge/features/e2e-realclaude.md` — the suite's conventions: single build tag, no `-race`, opt-in target, "read the count of executed tests, never the exit code".

## Context

The announced-reset path (#2134 parser arm, #2135 follower, #2136 runner id adoption) is shipped and green — hermetically. Every assertion in that family rests on fakeclaude emitting a line we told it to emit, and underneath that on one hand-observed capture from 2026-09-04 (claude 2.1.259) whose two id values were not even recorded. If claude changes the announcement's shape or stops announcing on the stream-json input path, the fake keeps passing forever, the feature is dead, and the operator gets the frozen usage gauge and the `unrecognized_message` noise row back with no test to say so.

This ticket adds the rung that would redden: one real claude, one real `/clear` sent as ordinary `send_message` text, and the client-side observables the operator actually depends on.

**No ADR is warranted.** Every posture here is an application of an existing decision — the always-a-real-claude-gate policy (2026-07-08), the package's sequential-spine / no-`t.Parallel` rule, and #2135's own ordering contract.

**This ticket adds no production code.** If proving the criteria turns out to require a production change, that is a finding to route back rather than to absorb.

## Sizing — inside every line of the table

Re-counted against this written plan, not the pre-plan sketch. Production source files created or modified: **0** (the deliverable is one `_test.go` file, which the count excludes by definition, plus this spec). Total written work: this spec at ~185 lines plus ~350 test lines ≈ **535**, against 800. New exported types: **0**. Consumer call sites needing simultaneous update: **0** — the file is additive and calls existing helpers unchanged. Acceptance criteria: **4**. Distinct error/reject branches: **0** — there is no state machine, only a drain with two negative arms.

Not refactor-shaped: no signature moves, no type replacement, no cross-package import flip. The refiner's `Estimate:` line said ~500 lines and named #1174 (525 actual, zero production changes) as the analogue; re-derived against the same shape here, I agree with it.

## Design

One new file, `internal/e2e/realclaude/interactive_stream_announced_reset_test.go`, build tag `e2e_realclaude`. The name matches its fakeclaude twin `relay_v2_stream_announced_reset_test.go` so a tree-wide sweep finds both ends of the family together.

### The spine

Transcribed from `TestInteractiveStreamNewSessionRotatesAndSpawnsFresh` down to the handshake, with no deltas: `claude` on PATH check → `WithWorktreeAuthenticated` → isolated `<home>/work` → `writeStreamInteractiveConfig` → `pyry pair` → `seedBootstrapRegistry` + `seedBoundConversation` → `fakerelay` → `spawnBootstrapDaemon` → `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeInteractive`. Sequential, no `t.Parallel` (`WithWorktreeAuthenticated` calls `t.Setenv`), skipping cleanly when `claude` or credentials are absent.

Fixed ids, per the package's repeated-digit convention and the two stems the ticket reserves (`1` and `3`–`f` are taken by siblings):

```go
clearBootstrapUUID = "22222222-2222-4222-8222-222222222222"
clearConvID        = "00000000-0000-4000-8000-000000000000"
```

Both are valid UUIDv4 stems (version nibble 4, variant nibble 8) and both match `uuidStemPattern`. Neither collides inside `internal/e2e/realclaude`; reuse across `internal/e2e` is harmless.

### The four steps

**Turn 1 — establish a live child and the pre-reset id.** One ordinary turn through `sealSendMessage` + `drainForCompletedTurn`. It is load-bearing three ways: `/clear` must reach a *running* claude, the reply bridge must be bound so the reset's transition resolves to a conversation, and it gives the file the shared drain's two standing alarms (`unrecognized_message`, `rate_limited`) on the one turn whose shape is not in question. Then `idBefore := waitBootstrapID(...)` — the registry read that supplies AC 1's `previous_session_id` anchor. Asserted `== clearBootstrapUUID` so a seed drift is caught here rather than as a confusing AC 1 mismatch.

**The `/clear` send.** `sealSendMessage` with the text `/clear` and nothing else. `Enqueue` delivers text verbatim, so claude receives exactly `/clear` on stdin. The window opens at this seal.

**The transition (AC 1).** Drain to the first `session_transition`. Assert `Reason == "clear"`, `PreviousSessionID == idBefore`, `ConversationID == clearConvID`, and `NewSessionID` a `uuidStemPattern` match distinct from `idBefore`. `t.Logf` the full observed frame — this is the capture the whole family rests on, and the gate's log is where it lands (see § the capture, below). Per the ticket, **no** second assertion for the re-key: `AdoptAnnouncedID` calls `notifyTransition` only after `rekeyLocked` and `saveLocked`, so the frame already implies the registry moved and persisted, and against a real claude the two ids' token figures are both small and non-discriminating.

**Turn 2 (AC 2).** Sent immediately on the transition, not on a terminal frame of the `/clear` turn. That ordering is the design's one non-obvious decision and it is forced: `waitIdleForDelivery` blocks a second message on the same conversation until that conversation has no open turn, and whether the `/clear` turn ever closes on its own is precisely this ticket's unknown. `transitionClearsTurn` answers `(successor, true)` for `ReasonClear`, so the announced reset *itself* clears the mark and releases the delivery. Waiting for a terminal frame instead would hang on exactly the shape the ticket says is plausible — a slash command that replies with nothing.

Then, in the same drain: the `ack` correlated on `InReplyTo == turn2ReqID`, then a non-empty `assistant_delta` for `clearConvID`, then the terminal `turn_state{idle}`. The ack is not decoration — it proves `Route` resolved `clearConvID` to the re-keyed pool entry, which is a distinct claim from the delta's.

At the terminal frame the window closes: assert exactly one transition across it.

### The local drain — one primitive, two waits

Neither shared drain fits, for the reasons the ticket states. The shape is a single frame-reading primitive that owns the whole window, with the test body doing two waits over it:

```go
type resetWindow struct { transitions []protocol.SessionTransitionPayload }

// next reads one binary→phone envelope, applying the window-wide negatives and
// counting transitions. Reports false at the deadline.
func (w *resetWindow) next(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
    deadline time.Time) (protocol.Envelope, bool)
```

Every frame in the window passes through `next`, which is what makes "exactly one" and "no `unrecognized_message`" structurally true rather than true of whatever a later read happened not to have discarded. Its behaviour, in order:

- Non-`noise_msg` inner frames are skipped **without decrypting** — they do not advance the receive nonce. Every `noise_msg` is decrypted in arrival order. There is exactly one reader for the whole window; a second concurrent one would desync the `CipherState`.
- `unrecognized_message` → `t.Fatalf` with `Site`, `MessageType`, `Truncated` and `Raw`. This is the arm that carries reading (b): a changed announcement shape surfaces here naming `conversation_reset`.
- `error` → `t.Fatalf` with the payload.
- `session_transition` → decode, append to `w.transitions`, return it like any other frame.
- Everything else → returned unexamined.

`rate_limited` deliberately gets **no arm here**. Turn 1 runs `drainForCompletedTurn`, which owns that alarm and its measured tolerance (`warnRateLimitStatus`); a second copy in this file would be a second place to update when claude's status vocabulary moves next, and the alarm has already fired or not by the time the window opens.

The two waits are thin loops over `next`: `awaitTransition` returns on the first `session_transition`; `awaitCompletedTurn` walks ack → non-empty delta → `turn_state{idle}`, ignoring `turn_state` frames until the delta is seen (mirroring `drainForCompletedTurn`, so a leading `responding` — or an `idle` the transition's own turn-clear may produce — cannot close the turn early).

**Why the delta cannot be attributed to the wrong turn in any way that matters.** In principle a `/clear` turn that streams text could have its tail arrive after turn 2's ack. It would not weaken the assertion: every event after the re-key crosses the rotated sink tag, and before #2135 *every* post-reset event was dropped at the drain's active-session gate. Any delta arriving after the announced reset is already the property under test. So no turn-id exclusion set and no content assertion is bought here; the ticket's own reading is that a slash command most likely replies with nothing anyway.

### The capture

The ticket asks for the observed line to be recorded in a ticket comment. No agent session on this machine can sign claude in, so the live gate — which runs with the fork's token after the PR opens — is the only place that observation exists. The test therefore `t.Logf`s the decoded transition (both ids, reason, occurred_at) at the point it asserts it, so the gate's own output carries the capture and it can be lifted into a comment. It writes **no file**: see the trust-boundary finding below for why a committed capture would pull in the package's redaction apparatus for no gain.

## Concurrency model

No goroutines of this test's own. `spawnBootstrapDaemon` spawns one `cmd.Wait` watcher, joined by `d.stop`. Cleanups run LIFO: phone close → daemon stop → relay close. The single-reader rule above is the only concurrency invariant, and it is enforced structurally by having one drain primitive rather than by discipline.

## Error handling

| Failure | Handling |
|---|---|
| `claude` absent / no credentials | `t.Skipf` before any state, via the existing PATH check and `WithWorktreeAuthenticated`. Exit 0, zero `=== RUN` lines added — the vacuous-green shape AC 4 exists to make visible in the count. |
| Turn 1 never drains | `drainForCompletedTurn`'s own milestone-specific Fatal (most likely a seed-UUID mismatch). |
| No `session_transition` within the budget | Fatal naming the three readings the ticket enumerates: claude no longer announces on this input path (premise dead — route back, do not weaken), the daemon regressed, or the `/clear` never reached the child. |
| `unrecognized_message` naming `conversation_reset` | Fatal printing the raw line. This is reading (b): a parser change and a new ticket, not this one. |
| More than one transition | Fatal with all of them — a second writer was reintroduced (#2137 retired the rotation watcher, which was the first). |
| Turn 2 never completes | Fatal distinguishing "no ack" (the send never routed to the re-keyed pool) from "ack but no delta" (the rotated tag is not admitting events — the dark-conversation regression #2135's follower exists to prevent) from "delta but no terminal idle". |

Budgets: `perTurnReplyBudget` (120 s) for each real turn, `rotateBudget` (45 s) for the transition — the value already calibrated for real claude on `/clear` in `harness_session_control_test.go`.

## Testing strategy

The deliverable *is* a test, so this section is about how it is proven to be non-vacuous rather than about further tests.

- `go vet ./...` and `go build ./cmd/pyry` — the module-wide gates.
- **`go test -tags e2e_realclaude -run TestInteractiveStreamAnnouncedResetFollowsLiveClaude -c -o /dev/null ./internal/e2e/realclaude/`** — compiles the tagged package. `make check` never compiles it, and the package failing to build is one of the two ways this suite reports success while proving nothing.
- **`go test -tags e2e_realclaude -run <name> -v ./internal/e2e/realclaude/`** locally — expected to print one `=== RUN` and skip at the credential check. That skip is the *correct* local outcome and is not evidence of anything except that the test is reachable and the package builds.
- The live green is the dispatcher's gate (`needs-real-claude`), read by its `=== RUN` count, never its exit code.

## Open questions

- **Does the `/clear` turn produce a terminal frame of its own?** Unknown, and deliberately left unknown: the design routes around it by sending turn 2 on the transition rather than on a terminal frame. If the gate's log shows the `/clear` turn's own `turn_state{idle}` arriving before turn 2's delta, that is new information worth a ticket comment, but it changes nothing here — `awaitCompletedTurn` ignores `turn_state` until the delta.
- **Does claude emit a `conversation_reset` anywhere outside the `/clear` turn?** The ticket asks only that this be reported if the run happens to surface it. The "exactly one transition" assertion across the window is what would notice, and its Fatal prints every transition seen.
- **Does the announced id's stem convention hold against a live claude?** #2134's fixtures reconstructed the id values because the capture elided them. AC 1's `uuidStemPattern` assertion on `NewSessionID` is the first live check of that, and the `t.Logf` records the real value.

Each is resolved in Phase B where it can be, and any design change it forces is recorded under `## Revisions`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No new boundary; one SHOULD FIX on not widening the existing one.** The boundary here is the one #2135's review named and audited: a line on the supervised child's stdout mutates the daemon's session registry. This ticket adds an *observer* on the client side of it and no production code, so it neither moves the boundary nor adds a decision to it. The one claude-authored value the test handles is the announced UUID stem, which reaches it inside `SessionTransitionPayload.NewSessionID` already gated by `emitConversationReset`'s `transcript.ValidStem`; asserting `uuidStemPattern` on it at the observation point is defence in depth as well as AC 1. **SHOULD FIX, and it is a design constraint rather than a code change: this test must write no capture file.** The `unrecognized_message` arm prints claude-authored raw bytes into a Fatal, and the test runs under `WithWorktreeAuthenticated` — the operator's real credentials, a real model, real output. Printing to a test log on the operator's own machine is precedented (`drainForCompletedTurn`'s identical arm) and bounded by the daemon's own `Truncated` cap. *Committing* those bytes would be a different act: every committed capture in this package goes through the redaction apparatus (`newInitControlRedactor`, `newDropcapScanner`) precisely because claude-authored output can carry host paths and credential-shaped text. This test buys nothing from a committed fixture — its deliverable is an assertion, not an artifact — so the capture goes into a ticket comment lifted from the gate's log, and the redactor stays out of scope.
- **[Tokens, secrets, credentials] SHOULD FIX — never log `payload.Token`.** `pyry pair` mints a bearer token and a responder static keypair into a per-test temp HOME destroyed with the test; the token is decoded from `pyry pair`'s stdout and handed to `fakephone.Dial` and the handshake. Nothing new is minted, stored, rotated or compared, and the credential `WithWorktreeAuthenticated` sets reaches only the spawned daemon's environment. The concrete hazard is the Fatal messages this ticket adds: the drain's failure paths must name frame types, ids and counts, and must not dump the handshake material or the pair payload. Enumerated positively in Phase B — the Fatals print `Site`/`MessageType`/`Truncated`/`Raw`, UUID stems, reasons and counts, and nothing else.
- **[File operations] No findings, and one absence worth stating.** The test creates `<home>/work` at `0700` and `<home>/.pyry/config.json` at `0600`, both through helpers that already set those modes. **No path in this test is built from claude-authored input.** That is a real divergence from its spine: #1174 resolves `<id>.jsonl` under the projects tree, making the rotated id a filename component. This ticket is forbidden a second re-key assertion, so it reads no transcript and constructs no such path — the announced id is compared against a regexp and printed, never joined onto a directory. No TOCTOU, no traversal surface, no symlink question.
- **[Subprocess / external command execution] No findings.** `spawnBootstrapDaemon` is reused verbatim: `exec.Command` with a fixed argv, no `sh -c`, environment inherited from the `t.Setenv`-isolated HOME plus two explicit switches. The one new user-controlled-looking value is the literal `/clear`, which travels as a JSON payload field inside the Noise channel and reaches claude's stdin — never argv. `--dangerously-skip-permissions` is inherited from the sibling and is correct here for the same reason: a permission modal would block the turns this test drives. Its blast radius is a temp HOME, and neither prompt asks for tool use.
- **[Cryptographic primitives] No findings, by construction — and the construction is the finding.** The receive nonce is sequential: every `noise_msg` must be decrypted in arrival order and every non-`noise_msg` inner frame skipped *without* decrypting, or the `CipherState` desyncs and the failure surfaces as an unrelated decrypt error many frames later. This is why the whole window has exactly one reader and why the design is a single `next` primitive with two thin waits over it rather than two independent drains — and why the test is sequential with no `t.Parallel`. Nothing is minted, derived or compared against a secret.
- **[Network & I/O] No findings.** No new socket, listener, deadline or size cap. `fakerelay` is loopback under `PYRY_ALLOW_INSECURE_RELAY=1`, confined to the test process. Every wait in the window is bounded by an existing budget (`perTurnReplyBudget`, `rotateBudget`); there is no unbounded read and no path that blocks forever — a hung daemon surfaces as a bounded Fatal naming what was expected.
- **[Error messages, logs, telemetry] No findings beyond the token bullet above.** No telemetry, no metrics, no record written. The logs are `t.Logf`/`t.Fatalf` into the gate's own output on the operator's machine.
- **[Concurrency] No findings.** The test spawns no goroutine; `spawnBootstrapDaemon`'s single `cmd.Wait` watcher is joined by `d.stop` in a `t.Cleanup`, and cleanups unwind LIFO (phone → daemon → relay) so the phone is closed before the daemon it is talking to. No locks. The one shared-state invariant is the single-reader rule, enforced structurally rather than by convention.
- **[Threat model alignment] No findings.** No new wire verb, frame type, field or recipient class; `session_transition` already travels the existing v2 push path to interactive conns. No inbound relay verb reaches the re-key seam — the only writer is the supervised child's stdout — so nothing here makes a paired device able to induce a session re-key.

**Out of scope, named:** the production behaviour this observes (#2135, merged), runner-side id adoption (#2136, merged), and retiring the rotation watcher (#2137, merged) — all three are the subjects under test, not this ticket's changes. A committed `conversation_reset` capture fixture, with the redaction pass it would require, is deferred: no ticket exists and none is warranted unless a consumer needs the bytes offline.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
