# Concurrency

**One owner goroutine + transient `time.AfterFunc` callbacks routed through a wake channel.** `Run` is the only goroutine the manager owns long-term. It reads `cfg.Frames`, looks up (or lazily creates) `m.sessions[env.ConnID]`, processes the frame synchronously, and ALSO pops `wakeSignal` values from a per-manager buffered channel `m.wake` and dispatches them via `handleWake`. `m.sessions` is mutated exclusively by `Run`; no mutex.

The #450 timer plumbing introduces transient `time.AfterFunc`-spawned goroutines (one per fire, never per session — `time.AfterFunc` only spawns when the timer fires). The callbacks DO NOT touch session state directly: they push a `wakeSignal{s, kind}` onto `m.wake` and exit. The single-owner-goroutine invariant for `s.send` / `s.recv` / `s.state` / `s.device` / `s.peerStatic` / `s.interactive` / `s.awaitingRekeyReply` / `s.rekeyTimer` / `s.rekeyReplyTimer` / `s.idleTimer` (#774) / `s.lastActivityAt` (#774) / `s.replayThrough` (#647/#663) / `s.replayQueue` (#777, the paced-replay tail drained one frame per `Run` pass by `drainReplayOnce`) is structurally preserved — only `Run` reads or writes those fields. The callback closure selects on `(m.wake <- signal, <-runCtx.Done())` so a fired-but-undelivered wake on a shutting-down `Run` exits via the ctx branch without leaking; pinned by `TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak`. `Run` derives `runCtx, cancelRun := context.WithCancel(ctx); defer cancelRun()` so a `Frames`-channel-close exit (which doesn't cancel `ctx`) still cancels `runCtx` and unblocks any pending callback.

`V2Session` carries no lock. The package contract is "one goroutine per `conn_id` mutates the session"; today that goroutine is `Run` itself. flynn/noise's `CipherState` carries a mutable 64-bit nonce counter; concurrent access would be UB — the serialisation point IS the lock.

As of [#965](../codebase/965.md), each open session also owns a long-lived per-conn worker goroutine (`appFrameWorker`) plus at most one short-lived `Route` goroutine at a time (spawned by `routeAppFrame`, one per application frame — [#909](../codebase/909.md)'s concurrent-drain shape, unchanged), so this is now structurally similar to [`internal/dispatch.Dispatcher`](dispatch-package.md)'s one-goroutine-per-conn model, but narrower in scope: only handler execution (`Route` → handler → `c.Send`, a marshal + channel push, no AEAD) moves off `Run`. `dispatchAppFrame`'s control-envelope arms (rekey/modal/question/interrupt/new_session/dequeue/snapshot/settings) stay on `Run` — fast, and they touch `s.send`/session state/timers directly. `debug_bundle` is the one exception: `handleDebugBundleRequest` itself is still fast and stays on `Run`, but only because [#1491](../../specs/architecture/1491-assemble-debug-bundle-off-run.md) pulled the one control-arm step that *isn't* fast — the `DebugBundler` seam's uncapped recording read + in-memory gzip — onto a short-lived per-request goroutine, funnelled back to `Run` via `m.bundleReady`/`handleBundleReady` for the stream and every seal (see [§ Inbound debug-bundle request](#inbound-debug-bundle-request-813--request_debug_bundle--debugbundler--streambundle)). An application frame is instead enqueued non-blocking onto a new per-conn `V2Session.appFrames` FIFO (capacity `appFrameQueueDepth = 16`; overflow tears the conn down at 4421 rather than block `Run`, which would reintroduce the stall), and `dispatchAppFrame` returns immediately

`attachment_chunk` (#1897) is a second control-class arm that also runs off `Run`, but through this same `appFrameWorker` queue rather than a new per-request goroutine like `debug_bundle`'s — its per-chunk work (hash + write, on the completing chunk, up to the per-upload byte bound) is exactly the class #1491 had to move off `Run`, but it recurs per-chunk rather than once per request, which fits the existing FIFO worker better than a fresh goroutine per frame would. This is the first type to make `V2Session.appFrames` carry more than one shape: the channel's element widened from `[]byte` to a two-field `appFrameJob{plaintext []byte; attachment bool}`, and the worker branched on the tag to send the frame to `handleAttachmentChunk` instead of `dispatch.Route`. The routing decision is made exactly once, in `dispatchAppFrame`'s case selector — the alternative considered and rejected was re-probing the envelope type inside the worker instead of tagging the job, which would let the routing decision live in two places that could silently disagree, and would let a future edit delete the `dispatchAppFrame` case (breaking `TestEveryInboundV2TypeHasHandler`, which reads case selectors as the registry of these decisions) while the worker-side probe kept the frame working anyway, masking the guard's whole point. See [Inbound attachment_chunk](v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md). — freeing `Run` to service every other select arm (other conns' frames, `m.wake`, `m.modalTimeout`, `m.manualRekey`) while the worker executes the handler. The worker drains `s.appFrames` in strict FIFO order — one frame fully routed (its `Route` call returned and every reply forwarded) before the next is dequeued — so no two handlers for the same conn ever run concurrently, and each reply crosses back to `Run` via a new unbuffered `V2SessionManager.appReply` channel (`chan appReplyMsg`), where `forwardAppReply` (now gated on `s.state == V2StateOpen`, dropping replies for a session torn down mid-handler) seals it under `s.send`. Every Noise cipher operation — `s.send.Encrypt`, `s.recv.Decrypt` — and every mutation of `s.state`/timers/`m.sessions`/`m.queues` keys stays exclusively on `Run`; the worker only ever touches its own conn's `s.appFrames`/`s.done` (safe unsynchronized channel ops), never `s.send`/`s.recv`/`s.state` directly, so `V2Session` still carries no lock. This closes the reply-*latency* head-of-line stall #909 explicitly deferred (#909 fixed the reply-*count* deadlock — a handler emitting more than `handlerOutboundBuf` replies — not this one). (Since #721 `send_message` is no longer the worst case for this reason alone: it now enqueues non-blocking and acks, so its `Activate`/`WriteUserTurn` blocking moved off the dispatch goroutine onto the daemon's `msgqueue` drain — see [features/msgqueue-package.md](msgqueue-package.md). `create_conversation`'s 30s `Activate` wait remains the concrete slow-handler case #965 targets.)

`request_attachment` (#2054) is the third such arm and the second to run off `Run` through `appFrameWorker`, joining `attachment_chunk` for the same reason — answering it reads a stored file, hashes it and base64-marshals one envelope per chunk. Its arrival widened the lone `attachment bool` into a typed `appFrameKind` (`appFrameRoute` / `appFrameAttachmentChunk` / `appFrameAttachmentRequest`), because a second bool would let both flags be true at once — a state the type can express but the worker cannot correctly route — where the enum's members are mutually exclusive by construction. The worker now `switch`es on `job.kind`, with `appFrameRoute` (the zero value) taking the v1 `dispatch.Route` chain and an explicit `default:` arm doing the same for any future kind added without an arm, since Go does not check a `switch` for exhaustiveness.

`mcp_status_request` (#2381) joins that same worker because `MCPStatusFor` may
wait on a child round trip. Running it inline would stall every connection on the
manager, while starting a free-standing goroutine would discard the worker's
per-connection serialization and queue bound. The reply still cannot be sealed by
the worker: success and all three rejects cross `forwardToRun`, keeping
`CipherState` mutation on `Run`. A blocked-resolver test must therefore prove both
halves — another connection replies before release, and the original connection's
eventual reply decrypts after release — because either assertion alone misses one
side of the ownership boundary.

`mcp_reconnect` / `mcp_toggle` (#2419) join the same worker for the identical
reason — the `MCPActuator` seam that would eventually back them may wait on a
child round trip — but their nil/capability gates run on `Run` and return before
either envelope is even decoded, so an unwired seam or a non-interactive
connection never enqueues a job at all. Every reply, accepted or refused, crosses
`forwardMCPStatusReply`, reused verbatim from #2381 rather than duplicated, so
`CipherState` mutation stays on `Run` for these two verbs exactly as it does for
`mcp_status_request`.

`request_context_usage` (#2431) joins the same worker for a stronger version of
`mcp_status_request`'s reason: answering it can wait **twice** — first on
`turnBusyTracker.WaitIdle` for an open turn to end (unbounded by design, since
AC-4 requires the answer to land only after the turn closes), then on the child
round trip. Every reply, success or either reject, still crosses `forwardToRun`,
reusing `mcp_status_request`'s ownership boundary rather than adding a new one.

**#2461 adds a remembered-answer fallback to that same resolver, and it
deliberately never becomes a flight.** When no fresh reading can be taken — no
bound runner, a runner that isn't a `contextUsageQuerier`, or a flight that
settled not-ok — `contextUsageResolver.Get` answers from the conversation's
stored `LastContextUsage` instead of refusing. That answer is one
`Registry.Get`, never a collapsed flight: `fly`'s deferred settle re-records
whatever the flight produced, so a fallback expressed as a flight settled `ok`
would hand the remembered reading straight back to the recorder with a
freshly-stamped time — a stale figure that renews its own staleness marker on
every ask and can never look stale again. **The nil-querier guard has to be
the first statement of the fresh-reading path, ahead of `r.mu.Lock()`, not a
condition somewhere inside it** — `fly` dereferences the querier
unconditionally, and before this ticket a nil querier only ever arrived paired
with `ok == false`, so nothing downstream expected one to reach the flight
map. Widening the resolve step to return `ok == true` with a nil querier for
the two hosted-but-unaskable cases made that invariant load-bearing for the
first time, and the RED run against the unmodified resolver reproduced the
predicted nil-pointer panic inside `fly` before the guard was added — a
demonstrated failure, not a hypothesized one. The regression test asserts the
structural invariant directly (`len(flights) == 0` after a
hosted-but-unaskable ask) rather than the reply, because the reply looks
identical whether or not a flight was installed and then panicked on a
goroutine.

**This is the first handler on `appFrameWorker` that can park for an entire
turn, and that turns `appFrameQueueDepth` overflow from a flood protection into
an ordinary-use teardown — found on code review, not in production.** Every
earlier occupant (`mcp_status_request`, `mcp_reconnect`/`mcp_toggle`,
`request_attachment`, `attachment_chunk`) waits only on a child round trip, so
the worst case is one slow response. While this handler is parked on
`WaitIdle`, later frames from the **same conn** queue behind it in
`s.appFrames` (depth 16) rather than being serviced, and `enqueueAppFrame`
answers overflow by tearing the conn down at 4421 — the guard that exists to
stop a flood instead fires on a well-behaved client. The concrete trigger: one
admitted attachment transfer may span up to 373 chunks (`protocol.
MaxAttachmentChunkBytes` is 45000; `internal/attachments/accumulator.go`'s
per-upload byte bound divided by it), each its own app frame through this same
queue, so a ~1 MB photo alone is ~23 frames — nearly 1.5× the queue depth.
Asking for a reading mid-turn (the ticket's own headline use case) and then
attaching a photo before the turn ends is enough to hit the ceiling and drop
the connection. **Record the trigger this way, not as added latency**: the
first-cut phrasing described the cost as "stalls that conn's later frames" and
filed queue overflow separately as a protection against flooding, without
noticing the two compose into a teardown of a client that did nothing wrong.
Deliberately not fixed here (Evidence-Based Fix Selection — nothing reaches
this path until a client adopts the verb); the follow-up this residual should
trigger is a disconnect, not a latency complaint. Related and sharing the same
root cause: `appFrameWorker` checks `s.done` only between jobs and is handed
`runCtx` rather than a per-session context, so a parked worker is not released
by its own conn's teardown either — bounded by the same two waits rather than a
leak, and worth the same sentence wherever the bounded-hold fix for the queue
lands.

**Moving shared work off the caller's context is only half a fix for a
collapsing seam; it has to come off the caller's goroutine too — the security
pass on #2431's `cmd/pyry`-side resolver behind `ContextUsageFor` caught both
halves as two separate findings.** The resolver collapses closely-spaced asks
for one conversation into a single round trip shared by every waiting caller
("a flight"). Deriving the flight's context from the daemon's own run context
rather than the first caller's — so one client disconnecting mid-flight can't
cancel a round trip others are waiting on, nor settle a cached refusal for
every joiner — fixed the concurrency finding the review raised. It was not
enough on its own: with the round trip still running **inline on the
installing caller's goroutine**, that caller alone could not leave early while
every joiner could, so the same client's disconnect was answered differently
depending on whether it happened to ask first. The fix generalises to: every
caller, installer included, spawns nothing and only awaits — the flight itself
runs on its own goroutine, bounded by the daemon context for `WaitIdle` and by
a request-scoped timeout for the child round trip, so it cannot outlive
shutdown or a silently unresponsive child. **The test written for the
context-only fix passed on the incomplete version** — it was written against
the behaviour the finding named ("the caller that stays connected gets its
answer"), which the context change alone already satisfied, not against the
asymmetry the fix was supposed to remove. Only a test aimed at the mechanism
(the *installer* specifically must be able to leave early too) caught the gap.
The same review also relocated *when* the collapse map is consulted: the
resolver now resolves the conversation to its registry-canonical id **before**
touching the map, and keys the map on that id rather than the client's string
— consulting the map first, keyed on the request string, would let any paired
client mint unbounded map entries by naming conversations the daemon doesn't
host, and the fact that `conversations.Registry.Get` happens to match
byte-exactly today would have made that safe only by borrowing a property this
package doesn't own.

**A nil-checked seam declared as an interface, not a func field, is a weaker
gate — worth recording here because on `MCPActuator` the nil check is a security
control, not a convenience.** `MCPActuator` is an interface, so
`cfg.MCPActuator = (*someImpl)(nil)` is non-nil to `dispatchAppFrame`'s `== nil`
test: the frame is admitted and the worker calls a method on a nil pointer.
`internal/relay` has no `recover()` anywhere, so that crashes the daemon —
fail-closed for authorization (nothing reaches a child) but a remote-triggerable
crash once a wiring bug exists. Every optional seam on `V2SessionConfig`
(`QuestionResolver`, `ModalResolver`, `AttachmentIntake`, `HistoryPager`, …)
shares this exact shape; it is called out on `MCPActuator` specifically because
here a mis-wire turns a stop-the-world crash into the fail path for an
authorization gate rather than for a convenience feature. Not escalated to a
build-time guard — no premature or nil wiring has been observed on any of these
seams (Evidence-Based Fix Selection); revisit if one ever ships.

**A zero-value enum member needs a producer that names it, or staticcheck flags it dead even though the type "handles" it by default.** The first cut of this widening leaned on `appFrameRoute`'s zero value implicitly — the v1 `enqueueAppFrame` call built `appFrameJob{plaintext: plaintext}` with no `kind`, and the worker caught it with a bare `default:` rather than a `case appFrameRoute:`. `staticcheck`'s `U1000` correctly called `appFrameRoute` unused: nothing in the source named the identifier, so the compiler couldn't see that the zero value was meaningful rather than accidental, and the doc-block claim that "the routing decision lives in one place" rested on a member neither producer nor consumer mentioned. The fix is to name it at both ends — the v1 call passes `kind: appFrameRoute` explicitly and the worker gains its own `case appFrameRoute:` — which makes `default:` genuinely unreachable in normal operation while still being the safe catch for a kind added without an arm. The general lesson: when a tagged-dispatch enum's zero value is meant to be a real, load-bearing case (not just "unset"), give it an explicit producer and an explicit consumer arm rather than trusting Go's zero-value default to stand in for both — the compiler cannot verify that a default arm and an elided zero-value case actually agree on what they mean.

`V2Session.State()` is a plain field read. Safe today because no cross-goroutine reads exist. Both the push surface (#571, rewritten #610) and the #588 enumeration surface deliberately keep it that way: the #610 `Push` reads only `m.queues` under `pushMu` (never `s.state`), while `forwardEnvelope` reads `s.state` **on the `Run` goroutine** during the drain, and `handleActiveConns` reads `s.state` (and `s.interactive`, #626) **on the `Run` goroutine** (funneled through `m.snapshot`) — neither via a cross-goroutine `State()` call. So the broadcast/enumeration layer that this comment once anticipated (the [#589](../codebase/589.md) assistant-turn fan-out, built on #571's `Push` + #588's `ActiveConns`) introduces **no** new reader of `s.state` off the owner goroutine, and `State()` still needs no `atomic.Int32`/mutex. Should a future slice read `s.state` directly from a producer goroutine *outside* the funnel, that accessor will need the atomic/mutex then — not pre-emptively refactored.
