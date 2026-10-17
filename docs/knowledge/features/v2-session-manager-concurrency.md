# Concurrency

**One owner goroutine + workers and transient callbacks.** `Run` owns mutable session state and Noise ciphers. It reads `cfg.Frames`, looks up (or lazily creates) `m.sessions[env.ConnID]`, dispatches the frame, and ALSO pops `wakeSignal` values from a per-manager buffered channel `m.wake` and dispatches them via `handleWake`. `m.sessions` is mutated exclusively by `Run`; no mutex.

| Section | Contents |
| --- | --- |
| [Workers and reply processing](v2-session-manager-concurrency-workers.md) | Run ownership, synchronous reply completion, bounded backpressure, cancellation and request-worker isolation. |
| [Hosted discovery and ordered delivery](v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md) | Plaintext admission versus transport receipt, FIFO ordering, capacity, rekey and retirement. |

## Connection-scoped reading cancellation

Cancellation must be independent of FIFO worker progress. Settings enrichment
calls `EffectiveEffortFor` and `MemorySearchFor` synchronously; a provider
waiting on `ctx.Done()` prevents `appFrameWorker` from returning. Cancelling
only in the worker's defer would therefore strand that provider after requester
teardown, along with outstanding context/MCP asks sharing its context.

`appFrameWorker` derives `connCtx` from Run's context and starts one watcher
selecting on the stable `s.done` channel and `connCtx.Done()`. Requester teardown
cancels immediately through the watcher; manager shutdown cancels through the
parent. Worker return cancels the context and joins the watcher. Neither the
watcher nor provider goroutines read negotiated session fields or seal replies:
`appFrameJob` carries the copied `thread`/`multiAgent` decisions, and supplied
readings return through `PushLiveState` to Run-owned gates, ordering and sealing.

`TestSuppliedSettingsEnrichmentTeardown` blocks each enrichment provider while
both context-usage and MCP asks are outstanding, closes the authenticated
requester, and requires all three contexts to cancel before releasing the
worker. It then checks removal and absence of readings or clears. Merely checking
connection removal or reply silence would pass while providers still survived.
See [settings enrichment](v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md#supplied-settings-reads).

## Supplied current-state delivery

A connect snapshot must not enter the bounded push/outbox queue as a batch:
one snapshot can contain more readings than either queue holds, and blocking
or sealing from its producer would violate `Run`'s single-writer ownership.
`ThreadLiveState` instead returns a detached, nonblocking cursor with no resource
needing closure. `drainLiveOnce` consumes at most one reading per pass; the
generated family clear and its fresh reading take separate passes. `Run` owns
the cursor, pending fresh state and delivered watermarks. Transport-down and
ordered-lane capacity holds apply before consuming a cursor or popping queued
input; reconnect or capacity release re-signals the pump, and
cancellation/teardown end delivery. `TestLiveStatePacing` and
`TestLiveStateRun` exercise a snapshot larger than push capacity without overflow.

Queued live updates drain before the next snapshot reading, so connect state
can be overtaken. Snapshot and live delivery must use the same
[source ordering](v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md#supplied-live-state-ordering)
to keep that snapshot from restoring stale state. `PushLiveState` copies payload,
session tag and correlation values beside the unsealed queue entry; looking up
the current binding later could retag old output as fresh. A fresh reading is
parked in `livePending` only after its clear forwards successfully into the
delivery lane. If inline delivery instead defers the clear upstream, its fresh
reading follows in the same bounded FIFO. A capacity-held `livePending` gates
that connection's queued heads, including flush markers, until admission;
otherwise a fence could report completion between clear and fresh. Retaining
fresh state after a failed clear would create an endlessly re-signaled pending
read, blocking unrelated queued and snapshot state; see
[complete-envelope validation](protocol-package-types-envelope.md#live-state-session-metadata).

[verification-tests]: development-verification-prove-that-tests-distinguish-the-change.md#prove-that-tests-distinguish-the-change
