# Conversation routing for supplied thread live state

## Files read
- `internal/protocol/envelope.go` → `Envelope`: optional metadata and omission semantics; existing constructors need no migration.
- `internal/relay/v2session_livestate.go` → `validateLiveState`, `PushLiveState`, `liveStateClear`, `forwardLiveState`: admission, detached queue ownership, source gates and per-conversation ordering.
- `internal/relay/v2session_push.go` → `forwardEnvelopeSource`, `mergedForConn`: final recipient projection and pre-encryption envelope bound.
- `internal/relay/v2session_livestate_test.go` → `liveFixture`, `liveDrain`, ordering and clear-bound tests: authenticated deterministic Run passes.
- `internal/relay/v2session_live_replies_test.go` → `suppliedWire`, supplied reply fixtures: actual asynchronous provider replies and request/source independence.
- `docs/knowledge/features/protocol-package.md` and `protocol-package-types-envelope.md`: additive metadata preserves legacy omission.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` § Supplied live-state ordering: source identity, not correlation, authorizes delivery; production readiness remains deferred.
- `docs/knowledge/features/development-verification.md`: inspect decrypted bytes and key presence, not only typed zero values.
- `docs/specs/architecture/3082-thread-live-state.md` and `3163-daemon-live-relay-delivery.md`: generated-clear bounds and canonical model projection.
- `docs/protocol-mobile.md` § Security model: metadata stays inside authenticated encryption.

## Context
Empty family clears and session tags cannot distinguish two conversations on one host. Retained readings already carry authoritative conversation identity, but delivery loses it. This change adds routing metadata without adding history semantics or production thread activation. No new decision record is needed.

## Design
Add optional `Envelope.ConversationID` as `json.RawMessage`, preserving absent versus explicitly malformed/null values for consumer validation. No unrelated constructor changes.
`validateLiveState` accepts an omitted envelope or payload identity, and requires every supplied identity to decode to a nonempty string equal to `LiveState.ConversationID`. Validate before any generated clear, queue admission or delivery watermark change. Use the stamped identity when measuring both the supplied envelope and generated clear.
`PushLiveState` clones the new raw metadata with existing detached buffers. Thread delivery stamps the retained source on both readings and clears, strips history/replay evidence as before, and normalizes supplied clears to empty uncorrelated envelopes. Non-thread delivery omits the new metadata and retains existing projection and payload bytes.
Keep source membership/agent gates, clear-before-fresh and revision/generation suppression intact. `forwardEnvelopeSource` continues bounding the final projected envelope before encryption.
Size: approximately 500 written lines including tests and plan; zero new exported types/interfaces, zero required constructor migrations, three acceptance criteria, fewer than ten new reject branches. One deliverable: independently routable live-state delivery. Refreshed remote feature branches have no overlap with the planned existing files.

## Concurrency model
No new goroutines or locks. Producers validate and clone before existing push queue admission; Run remains the only delivery/watermark/Noise writer. Detached snapshots are revalidated on delivery.

## State transitions and identity reuse
| Event | Race-enabled proof |
|---|---|
| Two conversations with equal, null or absent sessions; detached connect then queued reply | `TestLiveStateConversationRouting` |
| Generated clear, supplied clear with correlation, repeated clear and stale correlated reply | `TestLiveStateConversationRouting`, `TestLiveStateOrdering` |
| Malformed/conflicting snapshot or push, then valid reading of same identity | `TestLiveStateConversationIdentity` |
| Captured metadata buffer reused after push | `TestLiveStateConversationIdentity` |
| Generation advance and reused prompt/family identities | existing `TestLiveStateOrdering`, `TestLiveStateWire` |

## Error handling
Invalid identities and oversized admission/final projection return the existing content-free invalid-live-state error. Rejected readings emit no clear/fresh state and do not advance watermarks. Existing delivery logging and teardown remain unchanged.

## Testing strategy
Write tests first and observe meaningful failures. Inspect authenticated decrypted JSON for routing key presence, empty uncorrelated clears, original payload/session/correlation and absent history/replay/internal ordering metadata. Cover omitted/matching/malformed/conflicting identities at both envelope and payload, source gates, snapshot/queued delivery, duplicate/stale suppression, exact legacy bytes, and boundaries including metadata and model projection. Correct valid reply fixtures whose payload/source identities currently disagree; request identity may differ.
Run `go test -race -json ./internal/relay/... ./internal/protocol/...` with executed/failed/skipped counts, `go vet ./...`, and `go build -o /tmp/builder-3195/pyry ./cmd/pyry`. The verifier owns the full-module suite. No real Claude turn is required.

## Open questions
None.

## Documentation handoff
Pending documentation stage:
- `docs/protocol-mobile.md`, “Message envelope” and “Session-scoped live state”: required top-level `conversation_id` on delivered supplied thread live state; stamping absent identity; rejecting malformed/conflicting identity; empty uncorrelated clears; session-tag independence; legacy compatibility. Correct provider-installation claims: #3163 is merged; production negotiation awaits #3164.
- `docs/knowledge/decisions/042-daemon-built-thread.md`, “Live state is never an item”: add conversation routing identity alongside session tags without making live state an item.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: MUST FIX addressed in design: `validateLiveState` rejects envelope/payload identity disagreement with retained source before clear delivery; `liveWithheld` continues checking that source, never request correlation or session binding.
- Tokens/secrets: no credentials introduced; routing identity is encrypted application metadata and must not appear in rejection diagnostics.
- Files/subprocesses: no production file operations or subprocesses added.
- Cryptography: existing authenticated Noise path and single Run-owned nonce writer remain unchanged.
- Network/I/O: SHOULD FIX addressed by tests: both admission envelopes include routing overhead, and final recipient projection is bounded before encryption; exact-cap and cap-plus-one cases must exercise this.
- Errors/logs: retain `errInvalidLiveState` and content-free fixed delivery diagnostics.
- Concurrency: SHOULD FIX addressed by tests: clone `ConversationID` before queueing so caller buffer reuse cannot change routing or cause races.
- Threat model: paired callers cannot reroute one retained reading by supplying another identity; hostile relay cannot inspect/alter authenticated metadata. Production negotiation remains OUT OF SCOPE under #3164; history/catch-up under #2963. Existing prompt-injection and server-identity protections are unchanged.
**Reviewer:** builder (self-review)
**Date:** 2026-10-11
