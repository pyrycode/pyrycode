# #2883 — Conversation creation control contract

## Files read

- `internal/control/protocol.go` → `Request`, `Response`, `ChannelPayload`, `ChannelNewResult`: additive wire fields preserve existing encodings.
- `internal/control/server.go` → `SetChannelCreator`, `handleChannelNew`, `handle`: late-bound synchronized seam, guard order and response deadline.
- `internal/control/client.go` → `ChannelNew`, `request`: one-shot exchange and error precedence.
- `internal/control/channel_new_test.go` → `channelRoundTrip`, `startServerWithChannelCreator`: hermetic socket precedent and channel compatibility coverage.
- `internal/control/client_test.go` → `startMisbehavingServer`: client defensive response tests.
- `internal/control/wire_compat_test.go` → `TestServer_UnknownVerbWithUndeclaredPayload`: raw request precedent.
- `internal/protocol/conversations_write.go` → `CreateConversationPayload`: pointer model/effort distinguish unset from explicit empty.
- `docs/knowledge/features/control-plane.md` § Channel: new verb: empty cwd must be rejected before reaching the creator; creator error text must be static.
- `docs/knowledge/features/protocol-package-types-conversations-write-payloads.md`: optional pointer wire contracts.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: assert decoded values and remarshal to prove omission semantics.
- `CODING-STYLE.md`: Go testing, errors and concurrency conventions.

## Context

Shell clients need an independently testable conversation-create contract carrying chat/channel and requested settings. This ticket provides the control contract only; #2884 installs the daemon creator and CLI. Existing channel creation remains compatible. No decision record is needed.

Sizing: one deliverable, three acceptance criteria, approximately 500 total written lines, two new exported structs, no existing consumer updates and five handler rejection branches. This fits all builder limits and the #2155 control-socket analogue. Recount before committing: same limits remain satisfied. Remote feature branches were fetched and checked; none overlap the three production files.

## Design

- Add `VerbConversationNew`, optional `Request.Conversation *ConversationPayload` (JSON `conversation`) and optional `Response.ConversationNew *ConversationNewResult` (JSON `conversationNew`). Result carries required `conversationID`.
- `ConversationPayload` has required `Cwd string`, optional `Name string`, `Type *string`, `Model *string`, and `Effort *string`. Pointer fields use `omitempty`; absent/null settings decode to nil, explicit empty strings remain present. An absent/null type defaults to chat; a supplied empty string is invalid, as are values other than chat/channel.
- `SetConversationCreator(func(cwd, name, conversationType string, model, effort *string) (string, error))` installs an independent seam. Nil clears it. `NewServer` and `SetChannelCreator` keep their signatures.
- `handleConversationNew` checks the creator first, then payload/cwd, then type; invokes once with the effective type and every other value unchanged. It introduces no paths, settings vocabulary or defaults for name/model/effort. An empty returned id is a failure.
- `ConversationNew(ctx context.Context, socketPath string, payload ConversationPayload) (string, error)` uses `request` and returns an empty id on every error. Server errors take precedence over a result; missing/empty ids are rejected.

## Concurrency model

Setter and handler snapshot use `Server.mu`; creation runs after unlocking. No new goroutines. Existing per-connection goroutines close their connections and drain through `Serve`. Extend the connection deadline to `sessionOpTimeout + sessionOpConnGrace` after validation and before invoking the creator, as `handleChannelNew` does. This bounds response I/O, not creator execution; the future creator owns bounded work.

## Error handling

Fixed diagnostics distinguish absent creator, missing cwd, invalid type and empty creator id. Creator refusals receive only the conversation.new prefix, following `SetChannelCreator`'s static-message obligation. Every error omits the success payload, including an id returned alongside an error. The creator owns workspace confinement, symlink resolution before trust-marking, settings validation and name defaults. No control-layer logging of request values is added.

## Testing strategy

Write tests first and observe the missing API failure before implementing. Table-driven raw wire/socket cases cover chat/channel, omitted type, unchanged unusual cwd/name/settings, absent/null versus explicit empty model/effort, nil creator before malformed payload validation, absent payload, missing/empty cwd, invalid/empty type, creator refusal and empty id. Remarshal decoded requests to pin settings presence and old channel request/result encodings. Client tests cover a real successful exchange, server refusal with a misleading success payload, absent/empty ids, dial failure and invalid response JSON. A creator that calls the setter proves invocation happens outside the lock; concurrent installation/request tests run under the race detector. A short handshake window proves the response deadline is extended before the creator runs.

Run `go test -race ./internal/control/...`, `go vet ./...`, and `go build ./cmd/pyry` (output outside the worktree). The dispatcher owns the full-module verifier gate.

## Open questions

None. Type uses a pointer to enforce rejection of a supplied empty string while preserving omitted defaults.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/control-plane.md`, add `Conversation: create (conversation.new)` describing request fields, omission semantics, id response, fail-closed unwired behavior, and validation delegated to the installed creator. Describe this as a control API pending daemon/CLI wiring, not an already usable CLI command.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `handleConversationNew` refuses missing cwd and unknown type before invoking the creator. All other values remain untrusted at the documented creator seam; workspace and settings validation are owned by #2884.
- [Tokens, secrets, credentials] No credentials are generated or stored. Request cwd/name/settings must not enter logs or added diagnostics; only the creator's nonempty id is returned.
- [File operations] No file operations or path handling are introduced. #2884 must confine and resolve cwd before trust-marking, following `SetChannelCreator`.
- [Subprocesses] No subprocess execution is introduced; #2884 owns daemon creation and settings policy.
- [Cryptography] No crypto primitives, randomness or secret comparisons are introduced.
- [Network and I/O] Reuse the local 0600 socket and finite handshake deadline in `handle`, then extend the response deadline as for channel creation. The existing decoder has no byte cap; this additive contract does not widen socket exposure or alter the shared decoder. Creator execution remains synchronous and must bound its own work in #2884.
- [Errors, logs, telemetry] Creator error text must be static under `SetConversationCreator`'s documented obligation. Fixed guards echo no input. Creator error with an id discards that id; the client also prioritizes wire refusal over any success field.
- [Concurrency] Snapshot the creator under one mutex and invoke after unlocking; nil/replacement affects subsequent snapshots. No new goroutine or lock order is added.
- [Threat model] Same-user processes can dial the local socket. Control validation does not establish workspace trust; #2884 must implement that boundary. This stage deliberately leaves creation unwired and fails closed.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
