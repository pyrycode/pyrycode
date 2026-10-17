# Reply before subsequent broadcast

## Files read

- `internal/dispatch/dispatch.go` → `Conn.Send`, `Conn.Reply`, `Route`: channel admission currently releases handlers too early.
- `internal/relay/v2session_appframe.go` → `routeAppFrame`, `forwardToRun`, `forwardAppReply`: worker-to-Run boundary and drop gates.
- `internal/relay/v2session.go` → `Run`, `appReplyMsg`, `teardown`: sole cipher owner and session retirement.
- `internal/relay/v2session_hosted.go` → `hostedEnvelope`, `hostedDeliver`: bounded plaintext admission precedes transport receipt.
- `internal/relay/v2session_hosted_changes.go` → `reserveHostedPush`: full lanes retain ordinary output upstream.
- `internal/relay/v2session_push.go` → `drainOnce`, `pushSource`: FIFO upstream of ordered delivery.
- `internal/relay/v2session_appframe_test.go` → prolific, FIFO, slow-handler and transport-down regressions.
- `internal/relay/v2session_hosted_delivery_test.go` → ordered delivery and repeated rekey proofs.
- `docs/knowledge/features/dispatch-package.md` § Concurrency model: dispatch itself owns no goroutines.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § Hosted discovery and ordered delivery: admission is not delivery receipt; never seal outside Run.
- `docs/knowledge/features/development-verification-prove-that-tests-distinguish-the-change.md`: gate the contested handoff and decrypt in receive order.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: cancellation, contract comments and authenticated transport boundaries.

## Context

A handler's buffered Reply can be overtaken by its own synchronous announcer.
Repair the shared boundary without activating pins or depending on #3538.
No new architecture decision record is needed. Concurrent #3071 adds dispatch
observation and #3240 adds permission observation in different blocks; build
through both with local edits.

## Design

Add `dispatch.NewConnWithSender` accepting a synchronous, context-aware routing
sender. Retain channel constructors for existing consumers and fixtures.
The real manager routes synchronously on its existing per-connection worker,
using a sender that hands plaintext to Run and waits for a per-message receipt.
Run closes that receipt only after `forwardAppReply` returns. Completion means
legacy forwarding, ordered plaintext admission (including bounded upstream
deferral), or the existing deliberate drop decision; it does not promise peer
receipt. Subsequent handler announcements therefore cannot enter Push before
the reply crosses that boundary. Hosted replies and announcements retain the
same ordered lane, payloads and correlation behavior.

## Concurrency model

Remove the per-frame Route goroutine and buffered drainer from the real manager.
Each connection's existing worker runs Route; only Run reads session state or
uses Noise ciphers. The unbuffered handoff and receipt wait bound outstanding
work. Both waits escape on session teardown or manager cancellation. Pass the
worker's connection-scoped context to Route so blocked handlers also unwind.
Emit a content-free pre-handoff debug event used by a cancellable test logger
to hold reply progress without holding Run or locks.

## State transitions and identity reuse

| Event | Race regression |
| --- | --- |
| Reply then synchronous fanout, legacy and ordered lane | `TestV2Session_ReplyBeforeAnnouncement` |
| Teardown during held reply, same-ID replacement | `TestV2Session_HeldReplyRetirement` |
| Manager cancellation during held reply | `TestV2Session_HeldReplyRetirement` |
| Repeated requests and bursts beyond handlerOutboundBuf | existing prolific/FIFO regressions |
| Ordered lane held then resumed and repeated rekey | existing hosted ordered/rekey regressions |

## Error handling

Keep transport-down drops before encryption. Reject stale replies using original
session identity and open-state checks on Run. Cancellation ends sender waits;
no reply is redirected by connection ID. Existing sealing/marshal error handling
and hosted capacity/overflow policies remain authoritative.

## Testing strategy

Write a reply-and-announcer fixture through authenticated real managers, holding
the pre-handoff event. Authenticate/decrypt requester and sibling frames in
arrival order and check exact payloads and correlation. Exercise the hosted lane
as well. Hold reply progress during teardown/cancel and require bounded worker
completion; a replacement's first decrypted reply must have no nonce gap.
Run focused race checks through test-admission plus vet/build on the ticket OS.
The current host is macOS; the managed recipe inventory currently provides no
Linux relay-package check (platform-probe explicitly executes no candidate tests).
Do not claim a platform probe as Linux validation.

Sizing rechecked: one deliverable, approximately 600 written lines, no new
exported types, one constructor consumer, four acceptance criteria, fewer than
ten rejection branches. Within all five builder limits.

## Open questions

None in the product contract. Linux validation availability must be reported
accurately if no applicable managed recipe is available.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/dispatch-package.md`,
“Concurrency model”, and `docs/knowledge/features/v2-session-manager-concurrency.md`,
“Concurrency” / “Hosted discovery and ordered delivery”: describe the repaired
reply-before-subsequent-announcement boundary, its completion meaning,
cancellation/backpressure and Run-owned Noise invariant. Pin activation and its
protocol wording remain in #3538.

## Security review

**Verdict:** PASS

- Trust boundaries: `dispatchAppFrame` retains authenticated admission; sender replacement adds no inbound authority.
- Tokens/secrets: no credential changes; pre-handoff logs contain only an event and connection ID, never payloads or device identity.
- Files/subprocesses: this change performs no filesystem or subprocess operations.
- Cryptography: `forwardAppReply` and hosted delivery remain Run-owned. Drop before encryption for transport-down and stale sessions; test same-ID replacement without nonce gaps.
- Network/I/O: retain bounded queues and existing transport deadlines; receipt acknowledges processing, not remote consumption.
- Errors/logs: cancellation maps to context/session errors internally; no downstream error or plaintext is newly exposed on wire.
- Concurrency: no locks across receipt waits; connection watcher and manager context release waits. Removing Route's extra goroutine reduces lifecycle work.
- Threat model: preserve Noise authentication and ordering against relay tampering/replay. Prompt injection and relay availability policy are unchanged and outside this transport repair.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-17
