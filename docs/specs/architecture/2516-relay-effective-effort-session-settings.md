# Relay effective effort in session-settings replies (#2516)

## Files read

- `docs/knowledge/INDEX.md` — startup map for protocol, relay, and verification ownership.
- `CODING-STYLE.md` → interface, concurrency, testing, and citation conventions — establishes consumer-owned narrow seams, context cancellation, and race-enabled proof.
- `docs/knowledge/features/development-verification.md` → `Establish the change surface`, `Prove that tests distinguish the change`, `Protocol boundaries` — requires construction-site checks, poisoned refusals, and raw JSON presence assertions.
- `docs/knowledge/features/v2-session-manager.md` → `State machine`, `Sections` — identifies the control-frame interception boundary and the owning split topic.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md` → `handleRequestSessionSettings`, `RunConfigFor`, test design — defines the existing conversation-scoped saved-settings contract, fail-closed resolution, and no-bootstrap-fallback invariant.
- `docs/knowledge/features/v2-session-manager-concurrency.md` → `appFrameWorker`, `forwardToRun`, `forwardAppReply` — defines per-connection blocking-work placement and Run-owned Noise sealing.
- `docs/protocol-mobile.md` → `Security model` — supplies the relay threat model for the label-gated review.
- `docs/specs/architecture/2381-relay-mcp-status-request.md` → `Resolver contract`, `Concurrency model`, `Testing strategy` — nearest shipped provider/worker/reply-path analogue.
- `internal/protocol/settings.go` → `SessionSettingsPayload`, `NullableString`, `NewNullableString` — supplies the already-landed optional nullable wire field and its omitted/string/null states.
- `internal/protocol/settings_test.go` → `TestSessionSettingsPayload_EffectiveEffortStatesRoundTrip` — proves the wire type itself already distinguishes saved effort from applied effort.
- `internal/relay/v2session_seams.go` → `RunConfig`, `V2SessionConfig.RunConfigFor` — owns the saved-settings source and the new consumer-side provider seam.
- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings`, `settingsReplyError` — current inline composition and the write path that must remain on Run.
- `internal/relay/v2session.go` → `dispatchAppFrame`, `appFrameKind`, `appFrameWorker`, `forwardToRun`, `forwardAppReply` — interception, worker routing, and Run-owned reply sealing.
- `internal/relay/v2session_settings_read_test.go` → `readSeams`, `countingReadSeams`, `readManagerFor`, `TestV2Session_RequestSessionSettings_ConversationGate` — existing encrypted-loop fixtures and poisoned `RunConfigFor` refusal proof.
- `internal/relay/v2session_mcpstatus_test.go` → `TestV2Session_MCPStatusRequest_BlockedResolverDoesNotStallOtherConnection` — cross-connection liveness pattern for a provider that can block.
- `cmd/pyry/relay.go` → `runConfigFor` — confirms production continues to supply saved fields through the existing resolver and that this ticket adds no production effective-effort wiring.

## Context

Ticket #2515 added `SessionSettingsPayload.EffectiveEffort` as an optional nullable wire field but deliberately left the relay reply producer unchanged. This ticket lets the existing conversation-scoped `request_session_settings` path populate that field without conflating it with `RunConfig.Effort`: `RunConfigFor` remains the only authority for session id, saved model/effort, permission posture, and usage, while a second provider reports only Claude's applied nullable effort.

The provider may wait on a child round trip. The handler therefore can no longer run inline on the manager's `Run` goroutine; it must reuse the addressed connection's existing worker and return its unsealed reply through `forwardToRun`. Production exact-child selection and provider wiring remain #2517.

The refined estimate is about 700 total written lines across three production files. The implementation sketch remains within that boundary: three production files, one existing test file, no new exported type, no consumer signature cascade, four acceptance criteria, and no new wire reject branch. The refreshed branch scan found only `origin/feature/449` touching a planned file; issue #449 is closed and has no PR, so it is stale rather than in flight.

No ADR is needed. The change reuses the established optional provider, per-connection worker, and Run-owned reply patterns.

## Design

### Provider contract

Extend `V2SessionConfig` with one optional conversation-keyed function field:

```go
EffectiveEffortFor func(context.Context, string) (*string, bool)
```

The pointer is the nullable result: non-nil means a confirmed string and nil with `true` means confirmed JSON null. The boolean is availability: `false` means the pointer must be ignored and the wire key omitted. This two-part shape keeps the provider independent of the full Claude settings response and avoids giving `internal/relay` any child-protocol type.

The provider receives the worker context and the same non-empty conversation id that `RunConfigFor` has already accepted. It is never called for an empty/malformed request, a nil or refusing `RunConfigFor`, a nil provider, or a non-interactive connection. It is called once for every fresh resolvable request; the relay adds no cache or shared state.

### Interception and composition

`dispatchAppFrame` keeps the non-interactive capability gate on Run, then enqueues an interactive `request_session_settings` as a new `appFrameSessionSettingsRequest` job. Unlike providers whose nil seam makes the whole verb inert, a nil `EffectiveEffortFor` must not suppress the existing saved-settings reply, so provider availability is not a dispatch gate.

`appFrameWorker` routes the new kind to `handleRequestSessionSettings`. The handler changes its input from the pre-decoded envelope to the immutable plaintext bytes, re-decodes the envelope to recover its correlation id, and otherwise retains the current tolerated payload-decode behavior. It resolves `RunConfigFor` first. Only an accepted run configuration is copied into the reply and allowed to reach `EffectiveEffortFor`.

On an available provider result, the handler converts `*string` into `protocol.NewNullableString`, preserving a string or explicit null. On nil/unavailable provider results it leaves `EffectiveEffort` at the `NullableString` zero value so `omitzero` omits the key. Every original reply field is always sourced from the accepted `RunConfig`, never from the provider.

```text
Run: decrypt -> identify request -> capability gate -> addressed worker queue
worker: decode -> RunConfigFor -> optional EffectiveEffortFor -> unsealed reply
Run: forwardToRun/appReply -> Noise seal -> requesting connection only
```

The worker marshals the payload and a correlated envelope, then hands a `RoutingEnvelope` to `forwardToRun`. It does not call `forwardEnvelope`, `Push`, a broadcaster, `SettingsUpdater`, or `dispatch.Route`. The inline write handler and its shared `settingsReplyError` helper remain on Run unchanged.

### Preserved resolution behavior

- Non-interactive sessions are inert before decode or dependency access.
- Empty, absent, malformed, unknown, and nil-`RunConfigFor` requests retain their current zero-valued reply behavior; none consults the provider.
- A dormant conversation that `RunConfigFor` resolves from persisted settings remains an ordinary accepted `RunConfig`: all saved fields survive and the provider is consulted once. `internal/relay` deliberately has no live/dormant flag; the provider alone may report that no applied value is available, which then omits the optional key.
- A poisoned `RunConfig` returned with `false` remains unread and cannot enable a provider call.
- A live or dormant accepted `RunConfig` continues to populate all existing fields exactly as returned. The provider cannot replace saved `Effort`.
- Nil provider, explicit provider refusal, and a poisoned non-nil pointer returned with `false` all omit `effective_effort` while preserving the accepted run configuration.
- Available non-nil and nil pointers produce, respectively, an `effective_effort` string and explicit JSON null.

## Concurrency model

No new goroutine, channel, lock, or shared cache is introduced. Each open connection already has one `appFrameWorker`; the provider runs synchronously on that worker and therefore serializes only later frames for the addressed connection. A provider blocked for connection A cannot stall Run or connection B's worker.

Moving the composition off Run also allows simultaneous `RunConfigFor` calls from different connection workers. Its production registry, pool, permission-confirmation, and usage readers already synchronize their own state and are safe for concurrent reads; test doubles use atomics or channels rather than borrowing the former Run serialization.

The provider receives the manager's Run-derived context. On manager cancellation a compliant provider returns, and the worker exits or `forwardToRun` selects the cancelled context without handing a reply to Run. Connection teardown likewise closes `s.done`, causing `forwardToRun` to abandon a late result. Every Noise cipher operation remains on Run through `forwardAppReply`.

## Error handling

- Outer-envelope re-decode failure is unreachable after `dispatchAppFrame` matched the same immutable bytes; record a content-free warning and emit nothing because no trustworthy correlation id remains.
- Payload decode failure remains tolerated and produces the existing zero-valued settings reply without consulting either resolver.
- `RunConfigFor` nil or false produces the existing zero-valued reply and does not consult `EffectiveEffortFor`.
- `EffectiveEffortFor` nil or false omits the optional field; a false result's pointer is ignored.
- Payload or envelope marshal failures retain deterministic content-free logging. The defensive read-side error reply, if buildable, also crosses `forwardToRun`; no worker calls the Run-only sealing path.
- Cancellation or connection teardown drops an unsealed pending reply through the existing `forwardToRun` escape arms.

## Testing strategy

Tests first extend the encrypted-loop fixtures with provider configuration and atomic call counts, then run against the unchanged handler to establish RED: the optional key is absent and a blocking provider is never entered.

Focused relay tests will cover:

- Available string: saved `Effort` and a deliberately different applied effort both survive in the same correlated reply, with the provider called exactly once.
- Available nil pointer: raw reply JSON contains `"effective_effort": null`, distinct from omission.
- Nil provider and unavailable provider: all existing fields equal the accepted `RunConfig`, while raw reply JSON omits `effective_effort`.
- Poisoned refusal: a non-nil sentinel pointer returned with `false` is omitted.
- Resolution ordering: the existing empty, absent, malformed, unknown, nil-resolver, and non-interactive cases assert zero provider calls; `RunConfigFor` refusal continues to ignore its poisoned value.
- Fresh-read behavior: two resolvable requests for the same conversation invoke the provider twice and receive the two call-specific values, proving no relay cache.
- Conversation isolation and worker placement: block provider A, serve provider B on another connection with a distinct saved/applied pair, then release A and verify both correlated replies stayed on their requesting connection.
- Manager cancellation: a provider waiting on `ctx.Done()` observes cancellation and no late reply is sealed.
- Read-only routing: configured mutation and ordinary-handler spies remain untouched by `request_session_settings`.

The pre-existing settings tests continue to prove zero/full reply behavior, lack of bootstrap fallback, nil-seam degradation, and preserved correlation. The new raw-key checks distinguish omission from explicit null rather than relying only on decoded values.

Touched-scope verification:

- `go test -race ./internal/relay/...`
- `go vet ./...`
- `go build ./cmd/pyry`

## Open questions

None. The nullable/availability shape, provider ordering, worker ownership, cancellation path, and omission posture are fixed by the acceptance criteria. Exact live-child resolution and production wiring remain #2517.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md`, especially its `RunConfigFor`, control-flow, reply table, concurrency, and test-design sections, with the `EffectiveEffortFor` contract, placement on `appFrameWorker`, Run-owned `forwardToRun` reply path, string/null wire states, and omission on nil or unavailable provider results. Preserve the statement that `RunConfigFor` alone supplies every saved field and note that production wiring is deferred to #2517.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `handleRequestSessionSettings` remains the single remote-payload decode boundary. `RunConfigFor` must accept the non-empty conversation before the same id reaches `EffectiveEffortFor`; the provider's nullable string is display data only and never becomes authority, storage, or an execution input.
- [Tokens, secrets, credentials] Not applicable — the read adds no credential creation, storage, comparison, rotation, or revocation. Existing Noise authentication and the interactive capability gate remain unchanged.
- [File operations] Not applicable — this relay slice accepts, constructs, reads, or writes no path. Exact child lookup behind the provider is deferred to #2517.
- [Subprocess / external command execution] OUT OF SCOPE — this ticket defines the provider boundary but does not contact Claude or pass data to a command. Ticket #2517 owns the child round trip and its response validation.
- [Cryptographic primitives] No findings — no primitive, key, or nonce logic changes. The worker emits only an unsealed routing envelope through `forwardToRun`; `forwardAppReply` keeps Noise encryption on the single Run owner.
- [Network & I/O] No findings — inbound bytes retain the existing encrypted application-frame cap and bounded per-connection queue. One blocking provider occupies only one authenticated interactive connection's worker, and the context contract supplies manager-shutdown cancellation.
- [Error messages, logs, telemetry] No findings — the requested conversation id, saved settings, effective effort, raw payload, and decode error are excluded from logs and errors. JSON encoding safely quotes provider display data; external reply shape gains only the optional field from #2515.
- [Concurrency] No findings — no new goroutine, channel, lock, cache, or lock ordering is added. The provider runs on one connection's FIFO worker, other workers stay serviceable, and all replies return to Run before sealing. Cancellation and connection teardown have explicit escape paths.
- [Threat model alignment] No findings — unpaired peers remain outside the Noise-open path; non-interactive peers remain inert; a paired interactive peer can read effective effort only after the existing conversation-scoped `RunConfigFor` authority succeeds. The value is requester-only, never broadcast or replayed, and cannot execute a prompt or mutate settings. Protocol denial-of-service remains bounded to the existing per-connection worker/queue posture.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-20

## Revisions

None.
