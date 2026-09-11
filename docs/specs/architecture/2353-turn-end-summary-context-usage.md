# Request summary context usage after completed turns (#2353)

## Files read

- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`, `streamRunner`: constructs one parser and one concrete `streamsup.Runner` per session, and is the only place that can bind a parser-side decorator to that same runner.
- `cmd/pyry/session_model_hold.go` → `newSessionParser`, `sessionRetentions`: defines the existing unconditional parser-sink decorator chain that the new trigger must join without changing event forwarding.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor`, `turnBusyTracker.observe`: establishes that `turnevent.TurnEnd` is the sole closing variant, while confirming the tracker has no runner dependency and is not the request owner.
- `cmd/pyry/stream_turn_drain.go` → `streamTurnSink.sinkForTag`, `startStreamTurnDrainV2`: shows the downstream fan-in is non-blocking and session-tagged; the request must happen upstream so inactive sessions retain their own cadence.
- `internal/streamsup/runner.go` → `Runner.RequestContextUsage`, `Runner.nextControlID`: supplies the fixed-detail request primitive, correlation registration, shared ID sequence, and value-free errors.
- `internal/streamsup/parser.go` → `Parser.consumeLine`, `Parser.emitContextUsage`: the production `control_response` path consumes matched context-usage replies and never routes the recognized top-level type to `emitUnrecognized`.
- `internal/streamsup/context_usage_event_test.go` → `TestParser_ContextUsageCaptureReplay`: already replays the committed #2287 summary and full answers through a correlated production parser and proves the only emitted event is `turnevent.ContextUsage`.
- `internal/e2e/realclaude/testdata/context_usage_v2.1.259.json` → `arms`: committed successful summary/full evidence used by the hermetic production-parser replay.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O” and “Per-conversation turn-busy tracking”: records parser serialization, the correlated request/reply boundary, and why the busy tracker remains downstream lifecycle evidence only.
- `docs/knowledge/features/streamsup-package-firing-the-ask-at-spawn-time.md` → “Firing the ask at spawn time”: provides the closest absorbed-error and cardinality precedent, while distinguishing per-spawn initialization from this ticket’s per-turn request.
- `docs/knowledge/features/development-verification.md` → “Prove that tests distinguish the change” and “Captures and live evidence”: requires explicit event cardinality and reuse of committed capture evidence rather than a new live run.

## Context

`streamsup.Runner.RequestContextUsage` can already send and correlate a `get_context_usage` request. The interactive daemon does not yet invoke it after a completed turn. This change adds daemon policy at the only seam that owns both relevant identities: the per-session parser sink sees the ordered `TurnEnd`, and `newStreamRunnerFactory` constructs the exact runner whose stdin and parser correlation registry must serve the follow-up request.

The change does not alter the `sessions.Runner` interface, `turnBusyTracker`, stream fan-in policy, child supervision, backoff, or context-usage decoding. No ADR is warranted: this applies the established parser-sink decorator and absorbed control-request patterns.

Size re-check: two production files, approximately 450 total written lines including tests and this plan, zero exported types, one construction-site edit, three acceptance criteria, and one error branch. All one-ticket boundaries hold. The ticket is a grandchild, but no sizing exception is needed.

## Design

Add an unexported `turnEndContextUsageRequester` in `cmd/pyry`. It decorates a `func(turnevent.Event)` sink and holds a late-bound `func(string) error` request callback plus a logger.

`Sink` will:

1. Forward every event to the existing downstream sink synchronously and unchanged.
2. Return for every variant except `turnevent.TurnEnd`.
3. For each `TurnEnd`, invoke the callback exactly once with the constant `"summary"` after forwarding.
4. Absorb any returned error and emit exactly one content-free Debug record. The record carries a daemon-authored event name only; it deliberately omits the error, request ID, response, event payload, and session ID.

`newStreamRunnerFactory` inserts this decorator between `newSessionParser` and `sessionResetFollower`. After `streamsup.New` succeeds, it binds the decorator callback to that concrete runner’s `RequestContextUsage` method. The parser cannot receive child stdout until the returned runner’s `Run` starts, so the existing goroutine-start publication edge makes the late binding complete before the first event. The reset follower and all retention holds continue to receive every event before the fan-in exactly as today.

The request lives on the parser side of `streamTurnSink`, not in `startStreamTurnDrainV2`. This makes each runner request against itself, preserves cadence for background sessions whose events the active-session gate drops, and prevents sink saturation from suppressing a request after the parser has observed a terminal event.

## Concurrency model

No goroutines, channels, or locks are added. `streamsup.Parser` invokes the decorator serially on its stdout-forwarder goroutine, preserving stream order. The request is written synchronously only after downstream observation of `TurnEnd`. Existing runner synchronization protects stdin selection and request-ID minting. Request failure returns on the same callback and cannot reach the supervision loop.

## Error handling

- A non-terminal event never calls the request callback.
- A `TurnEnd` with a successful request forwards normally and adds no log.
- A `TurnEnd` whose request returns `ErrNoLiveChild`, a pipe error, or another value-free writer error still returns normally from `Sink`; it produces one Debug record with no dynamic error or protocol content.
- Factory construction errors retain the existing wrapped `streamsup.New` error path; the trigger is never active on a runner that was not constructed.

## Testing strategy

- Add a table-driven decorator test covering opener, neutral/non-terminal, and `TurnEnd` variants. Assert every event reaches the next sink unchanged, only each `TurnEnd` calls the requester, each call uses `summary`, and forwarding is recorded before requesting.
- Add a failure test with a deterministic requester error. Assert `Sink` does not expose an error, downstream still receives the terminal event, exactly one Debug record is emitted, and its message/attributes contain neither distinctive request-ID nor response-content sentinels (nor the injected error text).
- Add a production-factory test using a deterministic fake child: consume the existing spawn-time initialize request, emit a completed result, observe exactly one subsequent `get_context_usage` request with `detail:"summary"`, and send a correlated successful response. Assert the shared sink sees the original `TurnEnd` unchanged and no `Unrecognized`; the runner remains running, demonstrating no restart/backoff transition.
- Keep `TestParser_ContextUsageCaptureReplay` as the hermetic captured-answer proof: it already correlates and replays the committed #2287 response through `streamsup.NewParser`, asserts one `ContextUsage`, and therefore pins a zero `Unrecognized` increase.
- Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The ticket fixes the detail vocabulary, trigger boundary, event order, and failure policy.

## Documentation handoff

No shared documentation change is requested by the ticket. The later documentation stage may update the stream-supervision package overview to record the new automatic per-turn cadence.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the decorator receives only typed `turnevent.Event` values after `streamsup.Parser` has bounded and decoded subprocess stdout. The only outbound value it selects is the daemon-authored constant `summary`; `Runner.RequestContextUsage` retains the closed vocabulary and correlated-response boundary.
- [Tokens, secrets, credentials] No findings — no credential or externally supplied token is created, retained, or logged. Request IDs remain locally minted and are explicitly excluded from the new record.
- [File operations] No findings — the design performs no filesystem operation and reuses the committed capture read by existing tests without modifying it.
- [Subprocess / external command execution] No findings — the existing child argv, environment, lifecycle, and signal handling are unchanged. The added action is one structured control request to the already-running child.
- [Cryptographic primitives] No findings — the design adds no randomness, keys, comparisons, or cryptography.
- [Network & I/O] No findings — no network reader or connection is added. The outbound request inherits `WriteContextUsage`’s fixed vocabulary and one-write JSON envelope; the response inherits the parser’s existing bounded decode.
- [Error messages, logs, telemetry] No findings — the failure record is Debug and wholly daemon-authored. It omits the returned error as well as request IDs, response bytes, event payloads, and session identifiers, so a future widening of an error cannot turn this call site into a content leak.
- [Concurrency] No findings — no goroutine or lock is added. Parser serialization orders forwarding before requesting, runner synchronization owns stdin/ID state, and factory completion precedes the goroutine that can invoke the late-bound callback.
- [Threat model alignment] No findings — relay authorization and protocol framing are unchanged; this daemon-internal cadence publishes only through the already-authorized downstream event path.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11
