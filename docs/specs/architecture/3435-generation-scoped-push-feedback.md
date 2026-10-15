# #3435 — generation-scoped push feedback

## Files read
- `internal/relay/connection.go` → `SendPushWake`, `forwardFrames`, `run`: legacy wire path, raw receive boundary and sole reconnect observer.
- `internal/transport/wssclient.go` → `serve`, `enqueueSend`, `recvPump`, `sendPump`, `Receive`: ordered completion and socket ownership.
- `internal/relay/connection_test.go` → `newTestConnection`: loopback connection precedent.
- `internal/relay/v2session_hosted_transport_test.go` → `TestClientSendContextDelivery`, `TestClientSendContextPumpShutdown`: held-write barriers.
- `docs/knowledge/features/relay-package.md`, `relay-package-push-wake.md` § Correlated feedback and connection lifetime: shared frame channels cannot establish generation ownership.
- `docs/knowledge/features/transport-package.md`: pump shutdown and sanitized transport errors.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: failed decoding can retain valid earlier fields; test against live bindings.
- `docs/protocol-mobile.md` § Correlated wake opt-in, push_wake_result, Security model: canonical bounds, syntax and trust contract.
- `CODING-STYLE.md`: concurrency, comments and file-size conventions.

## Context
Expose opt-in invalid-token feedback without adopting it in the daemon or mutating registrations. The relay is trusted only to classify a request it received; caller identity/context never comes from its response. No new decision record is needed. No overlapping feature branches touched the two existing production files at planning time.

## Design
- Add opaque `transport.Generation`, with its cancellation context, captured socket and owning client. `Generation()` snapshots the live generation; `SendGeneration(ctx, generation, bytes)` uses the existing ordered pump and completion contract, rejecting replacement before admission. `ReceiveGeneration` returns each frame's pump-stamped generation. Existing APIs remain adapters with their current contracts.
- Install generation identity before starting pumps, publish connectivity after pumps start, and cancel generation lifetime on pump exit, DropConn and Close. Never infer ownership from reconnect notifications.
- `SendPushWakeFeedback(ctx, platform, token, requestContext string) (<-chan string, error)` retains an immutable caller string locally. Callers may use an opaque handle to their exact paired-identity/registration snapshot. A buffered request-local channel emits that original string once on valid feedback, then closes; retirement closes silently. Callers must check receive's boolean. No callback can stall frame receipt.
- Allocate fresh monotonic token-independent ASCII IDs under the feedback mutex; never reuse IDs during the Connection lifetime. Bind before send; store immediate valid replies until successful write completion. On send failure discard silently.
- At most 128 pending bindings per generation; deadline is submission plus 60 seconds. At overflow send the unchanged legacy envelope through the captured generation and return an already-closed feedback channel. Prune retired/expired entries before admission. Timers and cancellation callbacks remove idle entries and release caller context without further traffic.
- Parse raw outer objects before `RoutingEnvelope`. A present result key is consumed exclusively, including malformed results. Validate the entire required-field decode, ID allowlist and exact invalid_token code before looking up a binding. Unknown fields are ignored. Malformed routing diagnostics use fixed text rather than decoder errors.

## Concurrency model
The existing lifecycle and three transport pumps remain. One relay feedback mutex guards bindings, sequence, completion and one-shot retirement. No lock spans transport I/O. Each retained binding has bounded timer/request/generation cancellation registrations, stopped on retirement; their callbacks take only the feedback mutex. Generation context cancellation is synchronous; asynchronous cleanup cannot make a retired generation eligible. Ordinary Frames order and Reconnected's single observer remain unchanged.

## State transitions and identity reuse
| Event | Race test |
|---|---|
| Immediate result before write success/failure | `TestWakeFeedbackWriteOrdering` |
| Replacement during admission / old generation send | `TestGenerationAdmission` |
| Replacement during receive, equal IDs on independent A/B legs | `TestWakeFeedbackGenerations` |
| Duplicate, invalid result then valid result | `TestWakeFeedbackResults` |
| Expiry before/exactly at/after 60 seconds, idle cleanup | `TestWakeFeedbackBounds` |
| 128/129, overflow, no-result old peer | `TestWakeFeedbackBounds` |
| Request cancellation, Close, lifecycle cancellation | `TestWakeFeedbackRetirement` |

## Error handling
Transport sentinels and sanitized write errors propagate; local failures use fixed text. Failed/cancelled/expired requests are never notifications. Silence is never invalidity. Result parse failures are dropped without logging supplied bytes. Existing legacy submission behavior remains unchanged.

## Testing strategy
Write focused tests first, observe failure, then implement. Use literal JSON via real loopback sockets and production forwardFrames, held writes using the transport acceptance barrier, request-local channel closure and ordinary frames as bounded receive barriers. Controlled clock/timer seams verify deadlines and idle cleanup. Capture logs and returned errors with distinctive synthetic secret/provider/malformed/inner-content markers. Run race tests for relay and transport, go vet ./..., and go build ./cmd/pyry. Full make check belongs to the verifier gate per builder instructions.

## Open questions
None. Immutable string context deliberately leaves registration representation to #3436.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/relay-package-push-wake.md`, **Push-wake surface** / **Correlated feedback and connection lifetime**: document implemented opt-in API, caller-context ownership, 128/60-second bounds, overflow and generation retirement. State daemon registry consumer (#3436) and coordinated operator activation remain pending.

## Security review
**Verdict:** PASS
- Trust boundaries: `forwardFrames` consumes result objects before phone routing; complete decoding and exact generation/context checks precede notification. Unknown fields never supply identity.
- Tokens/secrets: no token-derived IDs; local immutable context never serialized/logged. Fixed parser diagnostics and existing sanitized transport errors prevent reflected content. Marker tests cover decoding failures.
- Files/subprocesses: none added; no registry, pairing, filesystem or process operations.
- Cryptography: sequence IDs are correlation, not authentication or secrets; existing TLS remains unchanged. No new cryptographic primitive.
- Network/I/O: existing 1 MiB read cap and write timeout apply; pending cap and immutable expiry bound retained state. Buffered single-result channels cannot stall readers.
- Errors/logs/telemetry: no new metrics or secret-bearing logs; malformed routing logs omit decoder errors.
- Concurrency: socket identity captured before binding, pinned admission, pump-stamped receive; request and generation cancellation checked synchronously before publishing. Callback cleanup is bounded and stopped.
- Threat model: relay can classify only its matching live submitted request; registry compare-and-clear and daemon adoption are OUT OF SCOPE in #3436. Provider validation/FCM are relay #213.
**Reviewer:** builder self-review. **Date:** 2026-10-15.

Sizing: one deliverable; forecast ~790 written lines, one new exported type, no existing consumer migration, five criteria, ten lifecycle/reject classes. Transport plumbing has only this consumer and stays with its proof under the sizing floor.

## Revisions
- 2026-10-15: Protocol rejection tests exposed JSON null outer envelopes reaching phone dispatch. `forwardFrames` now rejects non-object/null outer input with fixed diagnostics. Required fields use pointer decoding so duplicate keys ending in null cannot preserve an earlier valid binding identity.
- 2026-10-15: Full admission/receive replacement proof and peer-write-failure cases bring written work above the 800-line forecast (about 870 lines). Keep transport generation plumbing with its sole consuming relay API under the explicit sizing floor. A dedicated feedback file keeps the existing connection dispatcher edits local. `TestGenerationReceiveOwnership` adds held real-pump output across replacement; expiry during held completion proves deadlines are not renewed.
