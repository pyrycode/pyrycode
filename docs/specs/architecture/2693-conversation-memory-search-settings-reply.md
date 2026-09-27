# Conversation memory search in settings replies (#2693)

## Files read

- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings`: interactive read path, conversation resolution, and correlated reply.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.RunConfigFor`, `EffectiveEffortFor`: resolver contract and optional provider pattern.
- `internal/relay/v2session_settings_read_test.go` → `readManagerFor`, `waitSessionSettingsReply`, `assertSessionSettingsPayload`: encrypted request-to-reply harness and existing settings assertions.
- `internal/protocol/settings.go` → `SessionSettingsPayload`, `MemorySearchReport`: optional wire field and array encoding already supplied by #2692.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md` → inbound read control flow: fail-closed resolution and worker ownership.
- `docs/knowledge/features/protocol-package-types-session-settings-read-payloads.md` → memory search omission versus present empty provider array.
- `docs/knowledge/features/memorysearch-package.md` → detector vocabulary; #2694 adapts its result into this provider's wire-ready report.
- `docs/knowledge/features/development-verification.md` → compare decoded payloads and raw optional-field presence at protocol boundaries.

## Context

#2689 provides search detection and #2692 supplies the wire report. This ticket adds a consumer seam to publish a supplied report for the conversation and bound session selected by `RunConfigFor`. #2694 owns the daemon's live provider and mapping from detector results. No other in-flight feature branch currently edits the target relay files.

## Design

- Add optional `V2SessionConfig.MemorySearchFor func(context.Context, conversationID, sessionID string) (protocol.MemorySearchReport, error)`. Its caller passes the request's nonempty conversation ID and the accepted `RunConfig.SessionID`, never an ID from the request as a session ID.
- `handleRequestSessionSettings` preserves the existing zero-valued reply on missing, malformed, nil-resolver, or refused requests. It recognizes full payload decode success separately from the existing tolerant decode path; a partially decoded ID cannot trigger the new provider.
- After successful resolution, call a non-nil provider exactly once. On success attach its report unchanged, including aggregate `unknown` with provider rows. On failure attach `unknown` with an empty provider list. Nil provider omits `memory_search`.
- Keep the report in the request-local reply assembly only. The reply's original fields and correlation remain sourced as before. No relay cache or session state is added.

## Concurrency model

The existing `appFrameWorker` calls the provider with its context; the provider must honor cancellation and finish in bounded time. The Run goroutine retains the interactive gate and sends the finished reply. No new goroutine or shared mutable state is introduced.

## Error handling

Provider errors do not fail settings reads. The reply carries `unknown` and `providers: []`; logs, if any, exclude the provider error and conversation ID. A payload decode error prevents this provider call even if the decoder filled `ConversationID` before returning an error. Resolver refusal leaves the optional field omitted.

## Testing strategy

- Drive two distinct conversation/session bindings through encrypted requests; assert exact provider arguments, one call per request, original settings fields, optional-field presence, and `in_reply_to`.
- Repeat a request after the provider result changes, and after one conversation's binding changes; assert the later result and new session ID, proving no retained report.
- Cover returned `unknown` with an installed provider, provider error's present empty list, nil provider's omitted field, refused and malformed payloads (including partial decode), and non-interactive inertness.
- Follow the builder gate: `go test -race ./internal/relay/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

None. #2694 owns how detector evidence becomes a wire report.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md`, describe when the supplied memory search provider is called and the omission versus provider-failure reply. #2692 owns the wire shape.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `handleRequestSessionSettings` accepts only an interactive, fully decoded, nonempty conversation ID that `RunConfigFor` resolves. The session ID comes from that resolver. A partially decoded ID must never reach `MemorySearchFor`.
- [Tokens, secrets, credentials] No token is read or created. The provider receives only the conversation and resolved session IDs; neither ID nor any provider error enters a log or error reply.
- [File operations] No file is opened by this relay change. The later daemon provider is #2694's scope.
- [Subprocess execution] No command is run by this relay change. The later daemon provider is #2694's scope.
- [Cryptography] The existing Noise channel seals the reply in `forwardSessionSettingsReadReply`; this change does not handle keys or nonces.
- [Network and I/O] Existing frame caps and authenticated connection handling precede `handleRequestSessionSettings`; no new socket read or listener is added. The provider runs on a worker, not Run.
- [Errors and logs] Provider failure sends the static `unknown` report and no error detail. No client ID, provider error, or payload bytes are logged.
- [Concurrency] The report is local to one handler call. Existing worker cancellation supplies the shutdown path; the provider contract requires it to honor context.
- [Threat model] The protocol's Noise authenticated channel and interactive capability gate remain in force. The resolver blocks cross-conversation report leakage; unrelated remote-control threats remain outside #2693.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-27
