# #2767 — Daemon-wide host system prompt frames

## Files read

- `internal/protocol/envelope.go` → `Envelope`, `IsKnownAppType`, `inboundAppTypeSet`: envelope correlation and map-dispatch admission.
- `internal/protocol/codes.go` → `TypeSetSystemPrompt`, `TypeRequestSystemPrompt`, `TypeSystemPrompt`: existing conversation vocabulary stays separate.
- `internal/protocol/conversations_write.go` → `SetSystemPromptPayload`: pointer decoding distinguishes explicit strings from absent/null input.
- `internal/protocol/system_prompt.go` → `RequestSystemPromptPayload`, `SystemPromptPayload`: conversation lookup and session verdict do not belong to the host frames.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `TestTypeConstants_V1V2Partition`, `TestErrorCode_Constants_MatchSpec`: all vocabulary partitions need entries.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `maxV2AppEnvelope`: re-encode typed payloads and measure the full application envelope.
- `internal/protocol/envelope_test.go` → `readFixture`: committed JSON is required evidence.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestEveryInboundV2TypeHasHandler`: new verbs are pending handlers; reply is outbound.
- `internal/sessions/daemoninstructions.go` → `Pool.DefaultDaemonInstructions`: shipped reset text for the test-only fit check.
- `internal/conversations/registry.go` → `MaxSystemPromptBytes`: inclusive byte bound, referenced directly by the fit test.
- `docs/knowledge/features/protocol-package.md`, `protocol-package-types-system-prompt-payloads.md`, `protocol-package-types-conversations-write-payloads.md`, `protocol-package-drift-detectors.md`: pure DTOs and independent wire-key/fixture assertions.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: untouched RawMessage round trips cannot prove payload serialization.
- `docs/knowledge/features/sessions-package.md`, `sessions-package-key-types-writesystemprompt-systemprompttext.md` § Durable daemon-wide instructions: explicit empty survives restart; reset writes the returned default.
- `docs/protocol-mobile.md` § Security model: paired authority, private text, existing Noise transport and deferred rate limits.

## Context

Clients need to read and set the connected daemon's host prompt independently of a conversation. Persistence/composition landed in #2766; handlers and routing belong to #2768. This slice publishes the data contract and committed examples. No new decision record is needed.

Sizing: one deliverable; approximately 430 total written lines including plan, fixtures, tests and production comments; three new exported types, zero consumer call-site updates, three acceptance criteria, zero new state-machine rejection branches. All five limits hold. No overlapping feature branches found after fetching origin.

## Design

Add `internal/protocol/host_system_prompt.go` with three pure DTOs:

- `RequestHostSystemPromptPayload`: empty struct encoding `{}`.
- `SetHostSystemPromptPayload`: required `system_prompt` represented by `*string` without `omitempty`. Missing/null decode to nil (invalid input for the handler); any string including empty decodes to a nonnil pointer. Non-string JSON values fail decoding.
- `HostSystemPromptPayload`: `system_prompt` and `default_system_prompt` as strings without `omitempty`. Both keys always serialize, including an empty current value.

Add `TypeRequestHostSystemPrompt`, `TypeSetHostSystemPrompt`, `TypeHostSystemPrompt` and `CodeHostSystemPromptUnavailable` to `codes.go`. Admit both verbs in `inboundAppTypeSet`; exclude the outbound reply and classify it in the test-local v2 partition. Classify both verbs as `pending handler (#2768)` and the reply as `reply` in the relay guard. Preserve conversation-prompt partitions unchanged.

Consumer comments declare authenticated paired-client map dispatch with no conversation/session lookup or interactive-capability gate. Successful reads and durable writes return exactly one unicast `host_system_prompt`, correlated by envelope `in_reply_to`, never broadcast. Only the empty string clears; reset is an ordinary set of the returned default. Runtime validation and I/O remain #2768's responsibility.

## Concurrency model

No goroutines, locks or mutable shared state are added. Payloads are values; inbound membership remains immutable after initialization. #2768 consumes the pool's existing synchronized persistence API.

## Error handling

Comments require non-retryable `protocol.malformed` for malformed payloads, missing/null/non-string writes and values over the inclusive `conversations.MaxSystemPromptBytes` bound (8192 bytes). Storage failure maps to retryable `host_system_prompt.unavailable`. Consumer errors have static messages without submitted text. No protocol validators or custom decoders are introduced.

## Testing strategy

- Write tests first, observe a failing build for missing vocabulary, then implement.
- Five committed envelope examples cover empty reads, populated/empty writes, populated/empty-current replies with distinct nonempty default text and `in_reply_to`.
- Assert exact decoded structs and envelope type/correlation; marshal the decoded payload back through `roundTripEnvelope`.
- Independently marshal constructed DTOs and pin their complete key sets and required string values, including zero-value replies, so fixture regeneration cannot hide extra identifiers or omitted empty keys.
- Table-test missing/null, populated/empty/whitespace strings and each non-string JSON kind on writes.
- Measure a full correlated reply containing `MaxSystemPromptBytes` copies of a byte requiring six-byte JSON escaping plus the actual `Pool.DefaultDaemonInstructions`. Use a test-only sessions import and the existing `maxV2AppEnvelope` cap; assert values survive decoding.
- Run protocol and cmd/pyry race tests, `go vet ./...` and `go build ./cmd/pyry`; the verifier owns the full-module gate.

## Open questions

None. Handler wiring, access enforcement, runtime length validation, durability and response routing are explicitly pending #2768.

## Documentation handoff

Pending for the documentation stage: in `docs/protocol-mobile.md`, add “Daemon-wide host system prompt” beside “Setting a conversation's system prompt” and “Reading a conversation's system prompt”: frame names/directions, payload tables, correlated examples, empty/missing/null rules, inclusive 8192-byte bound, error codes/retryability, paired-client access and unicast/no-broadcast scope. Describe one-time default seeding, durable explicit empty, reset by ordinary set, and next-session-start activation with per-conversation/channel instructions last. Mark handlers/routing pending #2768 in the section and application-message table; #2768 removes that wording after wiring.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `SetHostSystemPromptPayload.SystemPrompt` preserves absent/null as nil; successful decoding is structural, not authorization or validation. OUT OF SCOPE (#2768): handlers must enforce paired-client authority and reject nil before using the text. Comments declare that consumer contract.
- [Tokens, secrets, credentials] Host text can contain sensitive instructions. Reply DTOs carry only current/default strings; complete-key tests prohibit accidental conversation/session metadata. No token generation, credential storage or lifecycle changes.
- [File operations] DTOs perform no filesystem operations. Persistence is the existing #2766 pool API; durable-write-before-reply enforcement is OUT OF SCOPE (#2768).
- [Subprocesses] No command execution or child environment changes; prompt activation stays with #2766 composition.
- [Cryptography] No cryptographic changes. Frames use the existing paired Noise transport; no new keys, nonces or comparisons.
- [Network and I/O] No sockets or reads added. Envelope-fit proof covers worst-case escaped current text and the shipped default within 65519 bytes. Runtime payload-size validation remains OUT OF SCOPE (#2768); comments specify the inclusive byte bound and malformed error.
- [Errors, logs, telemetry] Add static error vocabulary only, no logs. OUT OF SCOPE (#2768): static messages must omit submitted text and storage paths; comments require this.
- [Concurrency] No goroutines or mutable state added. The pure-data contract cannot race storage publication; #2768 must use the existing pool API.
- [Threat model] Paired clients have daemon-wide write authority; text remains untrusted despite authenticated transport (prompt injection is unchanged). OUT OF SCOPE (#2768): prove paired access and unicast/no-broadcast behavior. Relay MITM/replay/static-key protections remain in existing Noise/auth code; rate limiting remains deferred by the protocol security model.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
