# #2885: conversation.post user-message-by-id contract

## Files read

- `internal/control/protocol.go` → `Request`, `Response`, `ChannelPostPayload`, `MaxChannelPostBytes`: additive payload and result-free response precedent.
- `internal/control/server.go` → `SetChannelPoster`, `handleChannelPost`, `handle`, `SetConversationCreator`: synchronized late binding, guard order and response deadline.
- `internal/control/client.go` → `ChannelPost`, `request`: one-shot transport, refusal precedence and required OK.
- `internal/control/channel_post_test.go` → `TestChannelPost_AcceptsContentAtTheCap`, `fakeChannelPoster`: unchanged forwarding and boundary checks.
- `internal/control/conversation_new_test.go` → `startConversationServer`, `TestConversationNew_Refusals`: raw socket tests, callback clearing and concurrent installation.
- `internal/control/client_test.go` → `startMisbehavingServer`: hermetic client refusal and transport fixtures.
- `internal/control/channel_new_test.go` → `channelRoundTrip`: existing socket round-trip helper.
- `docs/knowledge/INDEX.md`, `CODING-STYLE.md`: knowledge ownership, Go and test conventions.
- `docs/knowledge/features/control-plane.md` → “Conversation: create” and “Channel: post a message into an existing channel”: independent seams and static/daemon-derived refusal obligation; channel.post is host-authored assistant content.
- `docs/knowledge/features/development-verification.md` → “Protocol boundaries” and “Prove that tests distinguish the change”: remarshal decoded DTOs and test guard ordering with independently invalid input.
- `docs/protocol-mobile.md` → “Security model”: local control is separate from the remote Noise transport.

## Context

Shell-driven submission needs a user-message contract addressing an existing conversation id. This ticket provides only that control contract and Go client; #2886 owns the production submitter and CLI wiring. Unlike channel.post, this API neither resolves labels nor creates a conversation on a miss. No decision record is needed for this additive use of established control conventions.

One deliverable: the independently testable contract. Sketch and final-plan sizing: about 400 written lines including tests and plan, one new exported payload type, zero existing consumer updates, three acceptance criteria and five handler refusal branches. This stays within every builder limit and is consistent with the refiner's ~350-line forecast. Remote feature branches were checked after fetch; none overlaps the planned control files.

## Design

- `protocol.go`: add `VerbConversationPost` and optional `Request.ConversationPost` with JSON key `conversationPost`. `ConversationPostPayload` has required string fields `ConversationID` (`conversationID`) and `Text` (`text`), neither with omitempty. Reuse `MaxChannelPostBytes`; introduce no result type or response field.
- `server.go`: add independent `conversationSubmitter func(conversationID, text string) error`, installed by `SetConversationSubmitter`. Nil clears it without changing `NewServer` or the channel APIs. The setter documents static or daemon-derived refusal text, no id/text logging, and the callback's obligation to bound its work and own existing-id submission.
- Dispatch `conversation.post` to `handleConversationPost`. Snapshot the submitter, reject an absent one first, then absent/null payload or empty id, empty text, and decoded text exceeding `MaxChannelPostBytes` using Go string byte length. Whitespace-only values are nonempty and are forwarded unchanged.
- Invoke the callback exactly once outside the lock. Control performs no registry lookup, creation, trimming, or id resolution. Extend the connection deadline to `sessionOpTimeout + sessionOpConnGrace` before invoking it, following `handleChannelPost`. This bounds response I/O, not callback execution.
- Success emits only `Response{OK: true}`: queue acceptance, not completed model output. A refusal emits only `Response.Error`, prefixed with `conversation.post:`.
- `client.go`: `ConversationPost(ctx, socketPath, conversationID, text string) error` uses `request`, returns transport errors and any wire refusal before inspecting OK, and rejects missing/false OK. It preserves caller values and leaves validation to the server.

## Concurrency model

Reuse the existing per-connection handler goroutine and server shutdown join. No new production goroutines or locks. Installation and snapshot use `Server.mu`; submission runs after unlocking. An in-flight request uses its captured callback even if installation changes. Tests join installation goroutines and release any callback gate before server teardown.

## Error handling

Fixed content-free diagnostics: `conversation.post: no conversation submitter configured`, `conversation.post: missing conversation id`, `conversation.post: empty message`, and `conversation.post: message too large`. Callback errors use its static/daemon-derived text with the verb prefix; unknown-id and full-backlog examples reveal neither supplied id nor text. No result body accompanies a failure. Client missing-OK error: `control: conversation.post response missing ok flag`.

## Testing strategy

Write tests first and observe failure before implementation. Use existing hermetic server/socket helpers and table-driven cases:

- Decode and remarshal the new request, including required empty keys. Pin existing channel.post request and response encodings and retain its existing API/behavior tests.
- Exact-once unchanged forwarding through the Go client and raw socket, including whitespace, escaped Unicode and distinct id/text values. Assert exact success JSON.
- Accept ASCII and multibyte text at the byte cap; reject one byte above it, including JSON-escaped multibyte content whose character count is below the cap.
- Unwired valid and invalid payloads; installed absent/null payload, missing/empty/null fields and wrong JSON types. Verify exact refusal-only responses and no callback for shape failures.
- Unknown-id and full-backlog callback refusals. Verify no control log contains distinctive id/text sentinels.
- Malformed, empty, missing/false-OK and refusal-with-OK client responses, plus dial failure.
- Callback can clear itself and respond after the handshake window; concurrent installation passes the race detector.

Checks: `go test -race ./internal/control/...`, `go vet ./...`, and `go build -o /tmp/builder-2885/pyry ./cmd/pyry`. The verifier owns the full-module gate.

## Open questions

None. Production behavior and CLI wiring remain assigned to #2886.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/control-plane.md`, add “Conversation: post a user message by id (conversation.post)”: request fields, 64 KiB decoded UTF-8 byte cap, fail-closed unwired behavior, callback refusals and OK meaning acceptance. State that production submission is delegated to the installed callback and remains pending #2886. Explicitly distinguish this contract from the existing “Channel: post a message into an existing channel” behavior.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `handleConversationPost` checks required shape and decoded byte length before submission. The opaque id remains untrusted; control performs no lookup or creation. OUT OF SCOPE: existing-id resolution and queue admission are owned by #2886.
- [Tokens, secrets, credentials] No credentials are generated, stored or read. Message text and raw id remain sensitive; neither appears in control logs or validation diagnostics.
- [File operations] No request value becomes a filesystem path and no file is written by this contract. Existing `Listen` keeps the socket at 0600.
- [Subprocesses] No child process or shell invocation is added; callback execution is the only submission effect.
- [Cryptography] No cryptographic primitives, keys, nonces or comparisons change. The verb is local control only.
- [Network and I/O] Reuse the handshake read deadline and extended bounded response I/O. `MaxChannelPostBytes` bounds decoded text before callback entry. The existing shared JSON decoder has no aggregate envelope-size cap; this ticket preserves the established same-user local-socket trust model, not an Internet-facing parser. Callback execution must be independently bounded by its implementation in #2886.
- [Errors, logs, telemetry] `SetConversationSubmitter` obliges static or daemon-derived refusal text with no raw id/text. The handler adds only the verb prefix and emits no content log. Tests assert refusal-only envelopes and sentinel-free logs.
- [Concurrency] Callback publication and snapshots share `Server.mu`; invocation is unlocked. No detached submission goroutine or new shutdown path is introduced.
- [Threat model] The existing 0600 socket restricts access to processes running as the operator. This contract adds no remote transport path; production submission and its queue/history effects remain #2886's responsibility.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
