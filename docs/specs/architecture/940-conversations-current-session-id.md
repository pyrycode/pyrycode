# #940 — Current session binding in conversations snapshots

## Files read

- `internal/protocol/conversations_read.go` → `ConversationSummary`: row DTO and required JSON keys.
- `internal/protocol/conversations_read_test.go` → `TestConversationsPayload_RoundTrip`: existing raw-envelope round trip cannot prove DTO serialization.
- `internal/relay/handlers/list_conversations.go` → `ListConversationsWithAgents`: sole row projection, after agent filtering; `ListConversations` delegates here.
- `internal/relay/handlers/list_conversations_test.go` → `newListConvConn`, `decodeConversationsResponse`: existing first-request test harness.
- `internal/conversations/conversation.go` → `Conversation.CurrentSessionID`: stored binding and unbound empty-string sentinel.
- `internal/conversations/registry.go` → `Registry.List`: copies rows under its mutex.
- `cmd/pyry/relay.go` → `wireRelay`: registers the agent-aware list handler.
- `internal/relay/v2session.go` → `dispatchAppFrame`, `forwardAppReply`: authenticated encrypted application path and reply sealing.
- `docs/knowledge/features/protocol-package.md` and `protocol-package-types-conversations-read-payloads.md`: DTO posture and the raw-payload round-trip trap.
- `docs/knowledge/features/relay-package.md` and `relay-package-handlers.md` → list-handler sections: retain filtering and history-error behavior.
- `docs/knowledge/features/development-verification.md` → Protocol boundaries: marshal decoded DTOs and inspect JSON key presence.
- `docs/protocol-mobile.md` → Security model: the relay must remain unable to read application contents.
- `CODING-STYLE.md`: table-driven stdlib tests and race checks.

## Change

Add `ConversationSummary.CurrentSessionID string` with the always-present JSON key `current_session_id` (no `omitempty`). Copy each emitted conversation's `CurrentSessionID` directly in `ListConversationsWithAgents`. An empty binding emits `""`; a nonempty binding identifies the stored session without asserting process liveness. This is one snapshot-contract deliverable with no new types, signature changes, goroutines, state, or failure modes. Existing registry snapshot locking, filtering, history lookups, JSON error propagation and encrypted reply routing remain in use.

No other fetched feature branch touches the four target code/test files. Estimated total written work is approximately 160 lines including this plan and security review, within the 800-line limit; zero new exported types/interfaces, zero consumer updates, two acceptance criteria, and zero new reject branches. The snapshot slice of #2698 is the analogue; this change also needs focused tests distinguishing both nonempty bindings and a mandatory empty key.

## Testing strategy

- Protocol: decode a three-row payload with two distinct nonempty session IDs and one unbound row; assert decoded values, marshal the decoded DTO, and inspect each row's raw JSON for an exact string value including `""`.
- Handler: create those three stored bindings and issue the first list request without any session transition or live pool. Assert each decoded row's binding and inspect the emitted payload for the same always-present string values.
- Write these tests first and observe failures before adding the field and projection.
- Run `go test -race ./internal/protocol/... ./internal/relay/handlers/...`, `go vet ./...`, and `go build -o /tmp/builder-940/pyry ./cmd/pyry`. The dispatcher owns the full-module verifier gate.

## Documentation handoff

- Pending for the documentation stage: update `docs/protocol-mobile.md`, “Application message types” → `conversations`: `current_session_id` is an always-present string containing the stored binding, empty when unbound; supports initial ID discovery and is not a liveness guarantee.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `ListConversationsWithAgents` reads the binding from `Registry.List`, never from request payload or the session-harness result. Existing agent filtering still determines which rows may be emitted.
- [Tokens, secrets, credentials] No findings. The session binding is an identifier, not an authentication credential; disclosure stays in the same authenticated application reply as the row's ID and workspace metadata.
- [File operations] No findings. The projection consumes an in-memory copy and adds no file reads, writes, path resolution or permission changes.
- [Subprocesses] No findings. The handler neither looks up nor starts a process; a stored ID deliberately provides no liveness guarantee.
- [Cryptography] No findings. `forwardAppReply` seals the application reply using the existing Noise path; no new key, nonce or primitive is introduced.
- [Network and I/O] No findings. The existing `Conn.Reply` route carries the added string inside the application payload. This adds no socket read or transport configuration.
- [Errors, logs, telemetry] No findings. No new log or error output contains the binding or payload; existing marshal/send errors propagate unchanged.
- [Concurrency] No findings. `Registry.List` copies the string under its mutex and projection remains synchronous; no lock, goroutine or shared mutation is added.
- [Threat model] No findings. The field remains inside encrypted application content, addressing relay disclosure threat 3 through existing sealing. It introduces no prompt input, authentication, replay or remote actuation path.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
