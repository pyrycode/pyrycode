# Publish initial thinking as a running interactive turn

## Files read

- `internal/streamsup/parser.go` → `Parser`, `emitStreamEvent`, `resetStreamEventBlock`, `emitThinkingProgress`, `emitAssistant`, and the `result` arm of `consumeLine` — owns both live thinking observations, completed assistant thinking, parser cross-line state, and the unconditional terminal `TurnEnd` emission.
- `internal/streamsup/parser_stream_event_test.go` → `TestParser_StreamEventCapture`, `TestParser_StreamEventRejectsUnsafeInput`, and `TestParser_StreamEventTextIsNotLogged` — nearest partial-message parser coverage and the existing content-free logging pattern.
- `internal/streamsup/parser_test.go` → `thinkingTokensLineFixture`, `TestParser_ThinkingProgressMapsFromCapture`, and `TestParser_ThinkingProgressDropIsLoggedContentFree` — pins the progress event's rate-bound producer and its content-free contract.
- `internal/turnevent/event.go` → `ThoughtChunk` and `ThinkingProgress` — the existing internal signals that can express observed thinking without adding protocol vocabulary.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`, `startTurnIfNeeded`, `transitionTo`, `endTurn`, `emit`, and `eventKind` — owns turn identity, wire ordering, lifecycle state publication, interactive capability filtering, and content-free diagnostics.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_TransitionOrder`, `TestInteractiveTurnEmitterV2_ThinkingProgressNoLifecycleMutation`, `TestInteractiveTurnEmitterV2_ThinkingProgressMidTurnDoesNotDisturbOpenTurn`, and the thought-text wire/log guard — existing lifecycle and suppression proofs that the new initial-progress behavior must extend.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker.observe`, `turnMarkFor`, `clearForSession`, and `clearForExit` — owns per-conversation delivery exclusion and the three independent close feeds that prevent an observed turn from wedging queued delivery.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant` — exhaustive event classification that currently records `ThinkingProgress` as lifecycle-neutral.
- `cmd/pyry/stream_turn_drain.go` → `startStreamTurnDrainV2` — proves busy observation precedes the active-session gate and emitter handling on the single drain goroutine.
- `cmd/pyry/active_interrupter_test.go` → `TestActiveInterrupter_NamedConversationInterruptsThatOne` — existing deterministic proof that a named interrupt resolves and actuates only the named conversation's runner.
- `internal/e2e/relay_v2_stream_interrupt_test.go` → `TestRelayV2_StreamInterruptNamedConversationStopsThatOne` — existing fake-daemon proof that addressed interruption leaves the other conversation's running state unaffected.
- `internal/e2e/realclaude/interactive_stream_interrupt_test.go` → `TestInteractiveStreamInterruptStopsRunningTurn` and `drainForCancelledTurnEnd` — live analogue for interrupting a published running state and requiring the first terminal event to be cancelled.
- `internal/e2e/realclaude/interactive_stream_running_turn_test.go` → `startStreamRunningTurnHarness`, `driveRunningTurn`, and `drainForResponding` — isolated real-Claude harness and bounded foreground-tool prompt reused by the live check.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” and “Per-conversation turn-busy tracking” — documents the partial-message contract, message-delivery exclusion, and close-feed argument that this ticket extends.
- `docs/knowledge/features/turnbridge-package-outbound-adapter.md` → `MapEvent` and `BuildTurnState` — confirms `ThoughtChunk` has no wire representation while lifecycle state has a dedicated bounded payload.
- `docs/knowledge/features/development-verification.md` → “Prove that tests distinguish the change”, “Captures and live evidence”, and “Test execution and artifact survival” — requires idle and mid-turn assertions, non-vacuous live evidence, and executed-test counts rather than exit status.
- `docs/knowledge/features/e2e-realclaude.md` → “Build tag”, “Test infrastructure”, and “Make target” — defines the credentialed suite's isolation and later dispatcher-owned gate.
- `CODING-STYLE.md` → “Logging”, “Concurrency”, “Testing”, and “Comments — Citing Other Code” — requires content-safe structured logs, race-safe state, behavior-focused tests, and symbol citations.

## Context

The daemon already knows how to publish `turn_state: thinking`, mint one turn ID, keep later text and tool events in that turn, and suppress thought text from the wire. The missing link is timing: `emitStreamEvent` discards partial `thinking_delta`, and `interactiveTurnEmitterV2.Handle` publishes `ThinkingProgress` as a numeric status reading without opening a lifecycle. A client therefore sees evidence that Claude is thinking while the authoritative server phase is absent, so its Stop and Escape controls remain disabled.

This change makes either live observation sufficient evidence that the server has accepted an interruptible turn. It does not infer a turn from a client's optimistic message echo, change the addressed interrupt contract, add a protocol type, or expose reasoning text. It deserves no ADR because it corrects the producer timing inside the existing turn-state and interrupt architecture rather than choosing a new cross-package contract.

The work is one deliverable: the parser signal is useful only to the emitter/busy lifecycle that consumes it, while opening the lifecycle is safe only with its terminal proof. The written scope is three production files, no exported types, no changed consumer signatures or call sites, four acceptance criteria, and no new reject branch in a state machine. Total written work is estimated at roughly 800–880 lines including this plan, deterministic coverage, and the live test. That is marginally above the nominal 800-line ceiling, matching the ticket's estimate; splitting would either separate the behavior from its liveness proof or leave a parser signal with exactly one sibling consumer, so the one-consumer floor requires one ticket.

## Design

### Partial-message thinking signal

`Parser.emitStreamEvent` will recognize a `content_block_delta` whose delta type is `thinking_delta` only when it belongs to the current valid message, matching open block index, and a block whose type is `thinking`. It will emit `turnevent.ThoughtChunk` with the current message ID and an empty `Text` field.

The decode target remains `streamEventDelta`, which has no field for Claude's thinking bytes. `encoding/json` therefore discards those bytes before the event is constructed; the parser neither retains nor forwards them. Invalid or unattributed thinking deltas stay silent rather than becoming `Unrecognized`, because routing their raw line through that diagnostic would create a reasoning-text lane. `signature_delta` and `input_json_delta` remain lifecycle-neutral and silent.

Repeated thinking deltas may repeat the empty internal opener. That is safe and simpler than new parser state: `startTurnIfNeeded` keeps one ID, `transitionTo` suppresses duplicate state frames, and `turnBusyTracker` stores membership rather than a count. The parser retains no reasoning text and no unbounded collection.

### Thinking-progress lifecycle

The `ThinkingProgress` arm in `interactiveTurnEmitterV2.Handle` will:

1. call `startTurnIfNeeded` for the cursor's conversation;
2. flush any prior assistant delta to preserve arrival order;
3. transition to `thinking`, which emits the opening state only when it is new;
4. emit the unchanged numeric `thinking_progress` frame.

Thus an idle conversation produces `turn_state: thinking` before its first progress reading. A progress reading inside an already thinking turn emits no duplicate state. A progress reading after text preserves the existing same-turn ID and sequence while legitimately transitions the phase back to thinking for a new inference request.

`turnMarkFor` will classify `ThinkingProgress` with the existing opener set. Because `startStreamTurnDrainV2` calls `turnBusyTracker.observe` before the active-session gate and before the emitter, the same event marks its daemon-resolved conversation busy before the published state is handled. This is intentional: a message arriving during initial thinking must park in `msgqueue` rather than race into the child's stdin.

### Closure and interruption

No new close path is needed. The closure is total across the relevant outcomes:

- every decoded `result` subtype emits one `TurnEnd` from the single `consumeLine` result arm;
- `error_during_execution` maps to cancelled, while success and other error subtypes map to the existing end-turn reason;
- `turnBusyTracker.observe` consumes that `TurnEnd` before active-session filtering and clears the conversation mark;
- the active emitter publishes `turn_end`, then `turn_state: idle`, then `endTurn` clears its scalar lifecycle state;
- pool teardown and child exit remain independent recovery feeds for a turn whose child produces no result.

`ThinkingProgress` and the empty partial-message `ThoughtChunk` both join that existing turn. Later text or tool use calls `startTurnIfNeeded`, finds the turn already open, and preserves its ID and sequence ordering. No optimistic client echo enters this path.

Conversation-addressed interruption remains `activeInterrupter.SendEsc` to the named bound runner and `Runner.Interrupt`. Existing deterministic and fake-daemon tests already pin routing isolation. The new live check establishes the missing temporal fact: interruption is sent after `thinking` but before `responding` or tool use, and the first terminal event for that conversation is cancelled.

### Data flow

```text
partial thinking_delta ─> empty ThoughtChunk ─┐
                                              ├─> busy opener + turn_state(thinking)
thinking_tokens reading ─> ThinkingProgress ─┘
                                                       │
named interrupt ─> bound Runner.Interrupt ─> result ─> TurnEnd ─> turn_end + idle
                                                       └────────> busy clear
```

`turnbridge.MapEvent` continues to drop `ThoughtChunk`; only `BuildTurnState` publishes the lifecycle. `ThinkingProgress` continues to expose only its two numeric fields. No mapper, logger, or payload receives thought text.

## Concurrency model

No goroutine, channel, mutex, or lock order is added. `Parser.Write` remains single-writer. `startStreamTurnDrainV2` remains the single reader that serially calls busy observation and emitter handling. `interactiveTurnEmitterV2` remains confined to that drain goroutine, while `turnBusyTracker` retains its mutex-protected per-conversation membership and generation channel for concurrent delivery waiters.

Opening the busy mark before emitter publication is load-bearing: a client can react to the state frame immediately, while queued delivery already observes the conversation as busy. Terminal `TurnEnd`, teardown, and exit clears remain idempotent under the tracker's existing lock.

## Error handling

- A malformed outer stream event keeps the existing bounded `Unrecognized` behavior unless it has already been identified as a thinking delta; recognized but unattributed thinking content is dropped content-free so reasoning cannot enter the raw diagnostic lane.
- Failure to mint a turn ID keeps `startTurnIfNeeded`'s content-free warning and drops both lifecycle and progress publication; it never emits a state with an empty identifier.
- A `TurnEnd` received without an emitter turn remains a content-free debug drop. The busy close is still applied independently before the active gate, so a stale emitter cannot keep message delivery wedged.
- Push failures retain the current debug record, which contains event and daemon identifiers but no event payload or reasoning text.
- Interruption errors and routing refusals keep their existing caller and log contracts; this ticket changes only when the runner is known to be in an interruptible turn.

## Testing strategy

- RED first: extend partial-stream parser coverage with a valid thinking block carrying a distinctive secret. Require an empty `ThoughtChunk` with the current message ID, no text-bearing event, and no secret in logs. Add unattributed/mismatched controls that remain silent rather than emitting raw reasoning.
- Replace the idle `ThinkingProgress` lifecycle-neutral assertion with the new exact order: `turn_state: thinking` precedes `thinking_progress`, one turn ID is minted, and a terminal event returns it to idle. Keep the existing mid-turn test, updating it to require same-turn transitions rather than neutrality.
- Change the exhaustive `turnMarkFor` row for `ThinkingProgress` to `turnMarkOpen`, and add busy-state coverage showing a second conversation remains unchanged while the thinking conversation opens and closes.
- Add a deterministic parser-to-emitter harness in `cmd/pyry` that feeds real JSON lines through `streamsup.Parser`, then synchronously through `turnBusyTracker.observe` and `interactiveTurnEmitterV2.Handle`. Cover both openers and three terminal rows: success, interrupted `error_during_execution`, and non-interrupt error. Assert exact envelope order, no assistant/tool content before termination, correct stop reason, idle state, busy clear, and no thought secret in any payload or structured log.
- Reuse the existing thought-to-text/tool ordering tests to pin one turn ID and sequence; add only the discriminator needed for the progress opener so coverage does not duplicate settled behavior.
- Add a focused tagged real-Claude check beside `TestInteractiveStreamInterruptStopsRunningTurn`. Reuse `startStreamRunningTurnHarness`, `driveRunningTurn`, and `drainForCancelledTurnEnd`, but wait specifically for `turn_state: thinking`, fail if responding, tool use, assistant content, or a terminal state arrives first, then immediately send the named interrupt. The dispatcher-owned live gate must report the count of executed tests and its skip reasons; exit status alone is not evidence.
- Builder-owned verification: `go test -race ./internal/streamsup/...`, `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry`. Compile the tagged real-Claude package without running live tests if needed to catch build-tag-only errors. Do not run the credentialed suite in this stage.

## Open questions

None. Current code answers the lifecycle signal, text-suppression boundary, queueing posture, close feeds, and addressed interrupt route. The credentialed gate owns only whether the installed real Claude produces a sufficiently early thinking window for the live check.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/streamsup-package.md` in “Turn I/O — envelope write + stdout parser” and “Per-conversation turn-busy tracking” to record that an observed partial `thinking_delta` or emitted `ThinkingProgress` opens an interactive turn, that inbound delivery intentionally queues during this phase, and that result, pool teardown, and child exit make closure total. The file is already at the package-overview size cap, so split it at `##` headings following the “Package overview size” rule in `CLAUDE.md` before folding this material; do not append past the cap.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — untrusted child stdout crosses through `Parser.emitStreamEvent` or `Parser.emitThinkingProgress`. The partial path validates message, block index, and block type while its decode target has no thinking-text field; the progress path exposes only integers. Downstream busy identity comes from the daemon's session tag and registry resolver, while wire identity comes from the daemon cursor, never from reasoning bytes.
- [Tokens, secrets, credentials] No findings — no credential or bearer token is created, read, persisted, compared, or logged. `startTurnIfNeeded` retains the existing cryptographically minted opaque turn ID and does not derive it from child or client content.
- [File operations] No findings — production adds no file access. The live harness continues to use its isolated authenticated home and work directory through existing helpers; it neither touches nor stops the daily application daemon.
- [Subprocess execution] No findings — child command construction, arguments, environment, signals, and teardown are unchanged. The named interrupt continues through `Runner.Interrupt`; the test prompt is data sent through the existing harness, not shell-interpreted by the daemon.
- [Cryptographic primitives] No findings — no primitive, key, nonce, or secret comparison changes. Existing turn-ID minting remains in `conversations.NewID`.
- [Network & I/O] No findings — the parser's existing maximum line buffer remains the input cap; repeated partial thinking produces empty membership openers with no retained collection, duplicate state frames are suppressed, and progress keeps its existing producer rate bound. Interactive capability filtering and relay framing are unchanged.
- [Error messages, logs, telemetry] No findings — `streamEventDelta` cannot hold thinking bytes, empty `ThoughtChunk` has no reasoning content, `eventKind` returns only variant names, `turnbridge.MapEvent` has no thought mapping, and the new tests exercise log-heavy paths with distinctive sentinels. Recognized invalid thinking content is not routed through raw `Unrecognized` diagnostics.
- [Concurrency] No findings — no new goroutine or lock is introduced. Parser and emitter stay single-writer, the busy membership mutation remains under its existing mutex, and the drain orders the busy open before publication. All three existing close feeds are idempotent and retain their shutdown paths.
- [Threat model alignment] No findings — the relay-visible change publishes only the existing bounded `turn_state` and numeric progress frame to already interactive-authorized connections. It does not alter authentication, pairing, routing authorization, or interrupt target resolution; hostile child content cannot select another conversation because `turnBusyTracker.observe` resolves the producing daemon session and `activeInterrupter.SendEsc` resolves the client-named conversation independently.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-20

## Revisions

None.
