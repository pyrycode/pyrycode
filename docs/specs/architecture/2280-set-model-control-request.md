# Spec — Ticket #2280: deliver live model changes as `set_model`

## Files read

- `docs/knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md` → `Runner` — explains why live-setting methods consumed by `internal/sessions` belong on the compile-checked interface and enumerates its production adapter and test doubles.
- `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md` → `Pool.UpdateSettings`, `deliverSettingsInBand` — defines the persistence-before-delivery contract, the in-band/restart partition, and the no-settings-value logging rule.
- `docs/knowledge/features/streamsup-package-content-blocks-are-held-as-json-rawmessage.md` → `WritePermissionMode`, `SetPermissionMode` — provides the structured control-request encoder, held-open-stdin writer, and locally minted correlation-id pattern.
- `docs/knowledge/features/streamsup-package-posture-gate-spawn-permission-mode-ack.md` → `PostureGate` — establishes that only permission posture acknowledgements gate turns and that unrelated control requests must not close or retarget it.
- `docs/knowledge/features/e2e-realclaude-interactive-stream-question-answer-test-go.md` → `TestInteractiveStreamQuestionAnswer`, `raiseRealQuestionBatch`, `drainAnsweredQuestionTurn` — supplies the existing live question surfacing and answer-completion path this ticket extends.
- `docs/knowledge/features/development-verification.md` → “Protocol boundaries”, “Captures and live evidence”, “Test execution and artifact survival” — requires byte-exact protocol assertions, non-vacuous live evidence, and actual executed-test counts.
- `internal/sessions/runner.go` → `Runner` — the consumer-owned seam that must gain `SetModel(model string) error` because a missing production capability must fail at compile time rather than silently skip delivery.
- `internal/sessions/pool.go` → `Pool.UpdateSettings`, `inBandDeliverable`, `deliverSettingsInBand` — the model delivery call changes here while effort, posture, empty-value restart, persistence, and argv installation remain unchanged.
- `internal/sessions/pool_update_settings_inband_test.go` → `TestInBandDeliverable`, the `Pool.UpdateSettings` live-apply tests — existing model/effort/posture assertions and the lifecycle runner records are the hermetic proof surface.
- `internal/sessions/runner_test.go` → `fakeRunner`, `lifecycleRunner` — shared doubles that must implement and record the widened runner seam.
- `cmd/pyry/streamsup_runner.go` → `streamRunner` — the sole production `sessions.Runner` adapter and therefore the production forward to `streamsup.Runner.SetModel`.
- `cmd/pyry/inbound_deliver_rotation_test.go` → `baseRunner`; `cmd/pyry/session_router_test.go` → `stubRunner` — reusable test implementations that must receive inert `SetModel` methods after the interface widens.
- `internal/streamsup/envelope.go` → `controlRequestInner`, `WritePermissionMode` — owns control-request encoding and can add the plain-string `model,omitempty` field plus the `set_model` encoder, one-write primitive, and runner method without creating another production file.
- `internal/streamsup/interface_test.go` and `internal/streamsup/envelope_test.go` → `TestRunner_SetPermissionMode_NoLiveChild`, `TestMarshalPermissionModeEnvelope` — patterns for no-child/error behavior and byte-exact newline-terminated encoding.
- `internal/streamsup/runner.go` → `PostureGate.retarget`, `Runner.nextControlID` — confirms `SetModel` may share the atomic correlation sequence but must not retarget or close the posture gate; its comment needs to name effort turns as the remaining setting-turn sink.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON`, `decodeControlRequest`, `writeSetPermissionModeAck` — fake stream dispatch and correlated success-response pattern to extend for `set_model`.
- `internal/e2e/internal/fakeclaude/set_permission_mode_control_test.go` → `TestRunStreamJSON_SetPermissionModeAnswer` — provides independent request and expected-response construction for the fake-child test.
- `internal/relay/v2session_settings.go` → `validModel` — its injection argument must change from turn-text safety to structured-JSON safety while retaining argv protection and the existing closed byte grammar.
- `internal/e2e/realclaude/interactive_stream_inband_model_test.go` → `TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel`, `inbandTapRecorder` — existing live model/PID/spawn proof to retarget from an extra `/model` turn to a correlated `set_model` success response.
- `internal/e2e/realclaude/interactive_stream_question_answer_test.go` → `TestInteractiveStreamQuestionAnswer` — existing daemon/phone question round trip to extend with a model update while the question batch is parked.
- `internal/e2e/realclaude/set_model_probe_test.go` → `setModelControlLine` and the committed `set_model_v2.1.259_*` fixtures — measured authority for a plain string request field, success-response shape, and resolved-model reporting.

## Context

`Pool.deliverSettingsInBand` currently turns a live model change into `/model <value>` through `Runner.WriteUserTurn`. That occupies the child’s ordinary turn stream, can interfere with a parked question, and produces an intermediate `system/init` reporting the old model. Claude 2.1.259 has now been measured accepting a `set_model` control request on the same held-open stdin. This change moves only non-empty model delivery onto that mechanism.

The persisted setting and recomposed next-spawn argv remain the durable half of `Pool.UpdateSettings`. An empty model or effort remains a restart request; non-empty effort remains `/effort <value>`; posture remains `set_permission_mode`. Model servability validation and client-visible refusals are explicitly deferred to #2281. This is an extension of an established control-request family and does not warrant a new ADR.

### Size boundary

- Deliverables: one behavior — live non-empty model changes use `set_model`, proved hermetically and in the two required live scenarios.
- Production source files: 6. Five carry behavior; `internal/relay/v2session_settings.go` is a comment-only correction that cannot be verified or consumed independently from the mechanism change, so the one-consumer floor keeps it in this ticket.
- Total written work: estimated 650–780 lines including this plan, tests, helpers, and comment corrections.
- New exported types or interfaces: 0. One existing interface gains one method.
- Simultaneous consumer/implementation updates: 6 explicit `sessions.Runner` implementations, below 10; `inbandRunner` inherits the concrete method by embedding.
- Acceptance criteria: 5.
- Distinct new error/reject branches: 2 in the stream writer (`nil` writer and marshal/write error propagation), mirroring existing control writers; no new state machine.

Ticket #2280 has parent #2203 and no grandparent. The nominal five-production-file ceiling and the one-consumer floor disagree only on the relay comment correction; per the floor rule the inseparable comment stays and the ticket proceeds. The required branch comparison found no in-flight feature branch touching the prospective files.

## Design

### Stream supervisor control primitive

Extend `controlRequestInner` with `Model string` tagged `json:"model,omitempty"`. `Mode` retains its existing tag and behavior. The zero values ensure interrupt, initialize, and permission-mode envelopes do not gain subtype-inapplicable fields.

Add these contracts in `internal/streamsup/envelope.go`:

```go
func marshalModelEnvelope(requestID, model string) ([]byte, error)
func WriteModel(w io.Writer, requestID, model string) error
func (r *Runner) SetModel(model string) error
```

`marshalModelEnvelope` uses `encoding/json` and appends exactly one newline. `WriteModel` rejects a nil writer as `ErrNoLiveChild`, encodes, and performs exactly one `Write`; it does not close stdin, validate servability, or log the model. `Runner.SetModel` mints one id through `nextControlID` and writes through the current held-open stdin.

Unlike `SetPermissionMode`, `SetModel` does not call `PostureGate.retarget`. A model acknowledgement is consumed by the existing parser but is not a prerequisite for admitting turns. This preserves the measured fail-open behavior requested by the ticket and avoids turning a rejected or slow model acknowledgement into a session-wide turn refusal.

### Sessions seam and delivery

Add `SetModel(model string) error` to `sessions.Runner` and forward it through `cmd/pyry`’s `streamRunner`. Every explicit test implementation gains either an inert method or, for `lifecycleRunner`, a recorded model-call sequence.

In `Pool.deliverSettingsInBand`, a present non-empty model invokes `sup.SetModel(*update.Model)` once and reports an error through the existing fixed-field `notDelivered("model", err)` path. It never calls `WriteUserTurn` for the model. The effort clause continues to send `/effort <value>` through `WriteUserTurn`, and posture delivery remains after it through `SetPermissionMode`.

`inBandDeliverable` and the restart branch do not change: an explicitly empty model or effort never reaches `deliverSettingsInBand`; it recomposes argv and calls `Restart`. `SetSpawnArgs` still runs before every in-band delivery, so a failed control write is repaired by the next child spawn.

For a combined update, model is written first as a control request, effort second as an ordinary turn, and posture last as its existing control request. No acknowledgement gate is added, so this order does not serialize child application; it provides deterministic call order for tests while each setting remains independent.

### Fake Claude

Add `set_model` to fakeclaude’s named control subtypes. A field-selecting decoder returns the request id and plain string model. Both stream-json loops recognize it independently of optional interrupt behavior and write the measured success response:

```json
{"type":"control_response","response":{"subtype":"success","request_id":"<same id>","response":{}}}
```

The fake response intentionally does not echo the requested alias: the captured success response does not use an echoed alias to prove application, and real Claude reports the resolved model only on the following `system/init`.

### Live-Claude evidence

Retarget `TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel` so its recorder retains success control-response request ids as well as init models and results. The test’s first phase expects no result and no extra init for the settings change itself, observes the matching success response, sends the next application turn, and asserts its resolved target model, unchanged PID, and spawn count one. The bracketed model phase uses the same no-turn mechanism and no longer waits for a `/model` result.

Extend `TestInteractiveStreamQuestionAnswer` after `raiseRealQuestionBatch` returns but before `chooseQuestionAnswers`: send a `set_session_settings` frame for a model different from the launch model, wait for its `session_settings_updated` acknowledgement, and then answer the original batch id through the existing path. The existing dismissal attribution and continuation-choice assertion prove the same question remained answerable and completed normally. No extra model-generated chat turn can satisfy them because the settings frame carries no prompt and the control response is not mapped as a turn.

```text
phone                  daemon / Pool                 live claude
  | question trigger         |                           |
  |------------------------->| user turn                 |
  | question_shown <---------|<------ AskUserQuestion ---|
  | set_session_settings --->| set_model control ------->|
  | settings_updated <-------|<------ success ack -------|
  | question_answer -------->| tool verdict ------------>|
  | question_dismissed/text <|<------ continuation ------|
```

### Comment corrections

Update `PostureGate.retarget`’s explanation so effort is the remaining setting delivered as an ordinary turn and model is explicitly excluded from gate retargeting. Update `validModel`’s sink discussion so its second sink is a structured JSON string in `set_model`, where the closed byte grammar is defense in depth for a network-originated value rather than the mechanism preventing line injection. The argv leading-dash defense remains unchanged.

## Concurrency model

No goroutine, channel, mutex, or shutdown path is added. `Runner.SetModel` snapshots `Stdin()` after its leaf mutex is released, then performs one newline-terminated write just like the existing control primitives. Relay-validated models are at most 64 bytes, keeping the control line well below the POSIX `PIPE_BUF` floor so it cannot interleave with a concurrent turn write on the child pipe.

`nextControlID` remains the single atomic sequence shared by all control subtypes, preventing same-runner correlation collisions. The parser already consumes control responses on its stdout goroutine. The model response does not mutate `PostureGate`, so there is no new lock ordering or turn-admission state.

## Error handling

- No live child: `WriteModel` returns the existing retryable `ErrNoLiveChild`; `deliverSettingsInBand` logs only session id, fixed setting name, and the error, then returns success because persistence and next-spawn argv already hold the requested value.
- JSON marshal or stdin write failure: wrap with `streamsup: marshal model` or `streamsup: write model`; the error never includes the model value.
- Unsupported/unservable model: no local control-layer validation is added. Existing relay shape validation still rejects unsafe wire values; servability and client-visible refusal reporting remain #2281.
- Empty model/effort: unreachable from the new writer because `inBandDeliverable` routes the update to `Restart`.
- Missing or mismatched control acknowledgement: consumed normally and does not gate turns. The live test fails if the expected matching success is absent, but production keeps the already-persisted value and next-spawn argv.
- Fake-child decode mismatch: the line falls through without a response, matching fakeclaude’s existing per-line resilience.

## Testing strategy

- RED in `internal/streamsup`: byte-exact `marshalModelEnvelope` output, special-character JSON escaping with one physical newline, absence of `mode` on `set_model` and absence of `model` on existing subtypes, nil-writer behavior, one-write behavior, and locally increasing ids through `Runner.SetModel`.
- RED in `internal/sessions`: direct delivery and `Pool.UpdateSettings` tests assert one recorded `SetModel` call, zero model `WriteUserTurn` calls, no restart, and unchanged effort/empty-value behavior. Combined model+effort coverage distinguishes the two seams.
- RED in fakeclaude: feed independently constructed `set_model` requests through both stream-json modes and assert exactly one independently constructed matching success response, including hostile string escaping without line injection.
- Build-tagged deterministic compilation/tests: update all `sessions.Runner` doubles and run offline-safe named real-Claude helper tests where possible; do not run a live Claude test locally.
- Dispatcher live gate: the updated model test proves matching success, resolved next-turn model, same PID, and one spawn; the question-answer test proves a settings change while parked leaves the original batch answerable and lets it complete.
- Required touched-scope gate: `go test -race ./internal/sessions/... ./internal/streamsup/... ./internal/e2e/internal/fakeclaude/... ./internal/relay/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. #2279 settled the request field representation and response semantics. The ticket explicitly settles posture-gate behavior, empty reset behavior, and the #2281 validation boundary.

## Documentation handoff

Pending for the documentation stage:

- Update `docs/knowledge/features/sessions-package-key-types-runner-interface-runnerfactory.md` section “Runner interface + RunnerFactory” with `SetModel` and the widened implementation set.
- Update `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md` section “Which branch, and why” so non-empty model delivery is `set_model`, while non-empty effort alone remains an ordinary turn.
- Update `docs/knowledge/features/streamsup-package-content-blocks-are-held-as-json-rawmessage.md` section “Permission-mode send primitive” or add its sibling model-control section describing `WriteModel` and the deliberate absence of posture gating.
- Update `docs/knowledge/features/e2e-realclaude.md` entries for `interactive_stream_inband_model_test.go` and `interactive_stream_question_answer_test.go` with the new control-request and open-question evidence.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the network-to-settings boundary remains `validModel`, including its 64-byte and closed-byte grammar; `marshalModelEnvelope` treats the accepted value as a JSON string and downstream code never treats it as syntax.
- [Tokens, secrets, credentials] No findings — `nextControlID` mints a correlation id, not a capability or secret. It remains process-local, is sent only to the child, is not logged, and shares the existing atomic lifecycle.
- [File operations] No findings — the change adds no filesystem operation. Existing atomic settings persistence completes before live delivery and remains unchanged.
- [Subprocess / external command execution] No findings — the model remains a discrete argv element on future spawns and a JSON string on live delivery; no shell is introduced. The existing leading-dash refusal in `validModel` remains intact.
- [Cryptographic primitives] No findings — no cryptographic primitive, key, nonce, or security randomness is introduced or changed.
- [Network & I/O] No findings — relay-originated model strings remain capped at 64 bytes, keeping the emitted line below `PIPE_BUF`; `WriteModel` performs one write to the existing held-open child pipe and adds no socket read or network listener.
- [Error messages, logs, telemetry] No findings — `deliverSettingsInBand` continues to log only the session id, fixed `"model"` field name, and an error whose new wrappers contain no model value. Payload bytes, aliases, and response contents are never logged.
- [Concurrency] No findings — one atomic request-id sequence remains shared across control subtypes; no new goroutine or lock is added; `SetModel` deliberately does not mutate `PostureGate`, avoiding a new cross-request turn gate.
- [Threat model alignment] OUT OF SCOPE — determining whether a syntactically valid model is servable and producing a client-visible refusal is owned by #2281. This ticket preserves the existing authenticated relay authorization and `validModel` boundary and changes only daemon-to-child delivery.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

- 2026-09-11: The written file list contains seven production `.go` files, not
  the six stated in the initial size count: both `PostureGate.retarget` and
  `validModel` require corrections in separate production files, while the five
  behavioral files remain as designed. Neither comment-only edit is a standalone
  deliverable or independently verifiable, so the one-consumer floor keeps both
  with the mechanism despite the production-file ceiling. Final written work is
  597 added lines including this plan, below the 800-line
  boundary. No interface, data flow, or behavior changed from the committed plan.
- 2026-09-11: The committed #2279 success capture omits an inner response payload;
  the exact response is `response{subtype:"success", request_id}` rather than the
  draft design's `response{subtype:"success", request_id, response:{}}`. Fakeclaude
  emits the captured key set, and its byte-exact test pins that corrected contract.
