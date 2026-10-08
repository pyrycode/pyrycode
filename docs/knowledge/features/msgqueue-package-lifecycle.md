# Message queue confirmation and lifecycle concurrency

Part of [`internal/msgqueue`](msgqueue-package.md).

## Delivered notification (#2115)

An optional seam beside `OnChange`/`OnGiveUp`/`Pending`, added so the
operator's own typed message could get a producer into the durable
per-conversation log ([history-package.md § Producers](history-package-producers.md#producers-2114-2115))
without touching `DeliverFunc`. `DeliveredFunc(convID string, msg QueuedMessage)`
fires after `q.mu` is released, once per **confirmed**
delivery — unconditional on `advanced`, unlike the neighbouring `q.notify`:
that guard is about the backlog, and once the write is confirmed the text has
reached claude's stdin and will be answered, so a `Remove` racing the commit
cancels nothing that already happened.

With `OnAccepted` configured (#2970), its callback must finish before either
`OnDelivered` or `OnTerminal` begins for that message, including immediate
delivery, enqueue-before-`Run` and concurrent removal. For delivered outcomes,
`OnDelivered` completes before `OnTerminal`; each gets its own attachment-id
copy. These observers retain the same queue id, device identity, app message id,
client-readable text, attachment ids, enqueue time and sender metadata as the
acceptance. Successful `SendNow` also preserves `SentNow: true`. See the
[exported API](msgqueue-package-api.md#exported-surface) for the full projection
and the empty device identity supplied by legacy callers.

`OnTerminal` reports exactly one outcome for each resolved acceptance:

| Outcome | Resolution |
| --- | --- |
| `TerminalDelivered` (`"delivered"`) | Confirmed ordinary or successful send-now delivery. |
| `TerminalRemoved` (`"removed"`) | Explicit removal without confirmed delivery. |
| `TerminalGiveUp` (`"give_up"`) | A persistently failing head actually abandoned after its retry window. |

Retries, failed/refused `SendNow`, refused `Remove` and stale give-up attempts
whose head was already taken emit no terminal fact. A successful removal with
no outstanding attempt resolves directly. Removal of a waiting head cancels
the outstanding attempt and removes the FIFO item immediately, but defers its
outcome until the attempt returns: an error resolves removal, while confirmed
delivery wins even if the seam confirmed without claiming `commitGate`.
Never report both removed and delivered for that message. A committing head
still refuses removal. This confirmation rule is pinned independently of FIFO
advance by `TestQueue_OnDelivered_FiresWhenRemoveRacedConfirmedDelivery` and by
`TestQueue_Lifecycle_RemoveAttemptArbitration`.

Shutdown alone resolves nothing: waiting acceptances stay unresolved, with no
invented delivery or drop. The confirmed-write observation gap below still
applies. Queue ids are per conversation and daemon run; durable linkage belongs
to the history writer's own acceptance records (#2972), as specified in
[ADR 042](../decisions/042-daemon-built-thread.md).

**Why `QueuedMessage` and not a bespoke parameter list.** `DeliverFunc`'s 64
`Deliver:` literals made a signature widening unsplittable, which forced an
additive seam — but the parameter *type* was still a choice, and reusing the
existing `Snapshot`/`SnapshotAll` projection rather than inventing
`func(convID, messageID, text string)` was the one that mattered. `QueuedMessage`
declares no `delivery` field, so this seam is *structurally* incapable of
handing a consumer the daemon-composed payload that may name an on-host path
([§ Security](msgqueue-package-security.md), `delivery`) — a consumer cannot leak it by forgetting a filter,
where an all-strings signature would have compiled just as well with `text`
and `messageID` transposed. **A projection type shaped to omit a field for one
consumer's sake is a leak barrier that composes to a new consumer for free —
reach for it before inventing a parameter list.** #2596 confirmed the payoff:
widening `QueuedMessage` with `AttachmentIDs` (so the history producer could
store them) needed no `DeliveredFunc` signature change and no new seam —
just a field the delivered projection populates and `Snapshot`/`SnapshotAll`
don't.

**Placement needs the safe projection before confirmation (#2820).**
`DeliveryMessage(ctx)` exposes a value copy for ordinary and send-now attempts,
cloning attachment ids and omitting delivery bytes. Decorators preserve the
context instead of widening `DeliverFunc`. The sole placement consumer can
register final composed bytes privately while preparing client/history content
from this projection. `OnDelivered` is still a confirmation notification, not
a stream-order barrier: placed Claude entries acknowledge it without committing
again. Codex/no-stream recording retains confirmation-at-write timing. See
[history producers](history-package-producers.md#producers-2114-2115).

**A negative-only test proves nothing until its fire site exists.** Built RED
in two steps (declare the seam, then wire the fire site), the give-up and
removed-before-commit assertions here were green from the moment the seam was
declared and stayed green with no fire site wired at all — nothing
distinguished "correctly silent" from "cannot possibly fire yet" except a
paired positive test's timeout. A suite of purely negative assertions gives no
RED signal; pair every "does not fire" case with a "does fire" case exercising
the same path.

`QueuedMessage.TS` on the delivered value is the **enqueue** timestamp, not the
confirmation time — a durable-record consumer wanting ordering must mint its
own (`internal/history`'s producers all hoist `time.Now().UTC()` at their own
commit point, never from this field).

## Concurrency model

- **Goroutines:** one `Run` goroutine (the daemon spawns it in `runSupervisor` and
  joins it via a buffered `qDone` channel after `pool.Run` returns, #721) + **one
  drain goroutine per active conversation**, spawned lazily
  and exiting when its FIFO empties. `Snapshot`/`Remove` add **no** goroutine —
  they run on the caller's goroutine; `notify` runs on whichever goroutine
  performed the mutation (Enqueue caller, a drain, or a Remove caller).
  Lifecycle observation adds no goroutine or observer lock. `OnAccepted` runs
  synchronously on the enqueue caller. Terminal notification normally runs on
  the resolving drain, remove or send-now caller; if resolution occurs during
  acceptance, the enqueue caller publishes the deferred terminal and any
  `OnDelivered` notification after acceptance returns. An outstanding removed
  head resolves on its drain. Observers may run concurrently across messages,
  must return promptly and can re-enter `Snapshot`/`SnapshotAll`.
- **Shared state:** a single `sync.Mutex` (`q.mu`) guards `convs`, each
  `convQueue`, `started`, `closed`, `q.ctx`, and each message's shared lifecycle
  state. **Leaf lock** — never held across
  the blocking `deliver` (which can block for a whole claude turn), and (since
  #719) never held across `onChange` either, never nested. All observer callbacks
  and both write seams run off-lock. This is the same "release before the
  seconds-long delivery" discipline `WriteUserTurn` itself uses.
- **TOCTOU on the FIFO head:** the drain **peeks** under the lock and advances only
  **after** a confirmed commit, under the lock again — and it advances **by id**.
  A concurrent `Enqueue` can only append (FIFO tail), but a concurrent `Remove`
  **can** take the head, deliberately: a merely-waiting head is droppable for the
  whole idle-gate wait and for the drain's post-delivery bookkeeping (#487,
  #1085). So `advanceLocked(head.id)` drops the front only when the FIFO is
  non-empty **and** its front still carries the id the drain attempted — the same
  predicate `commitGate` uses to accept or refuse the write, now asked in the
  lock hold that actually splices. That makes check-and-splice atomic against
  `Remove` under the one leaf lock, **closing** the window rather than narrowing
  it: a `Remove` of the in-flight head turns the advance into a **no-op** instead
  of dropping the message queued behind it, or panicking on the empty slice the
  removal left (`shrinkLocked` sets `items = nil`). Both advance sites carry the
  check — the confirmed-delivery one and `giveUp`'s (#1484).
- **Acceptance and outcome ownership (#2970):** FIFO copies and outstanding
  attempts share `messageLifecycle`, protected by `q.mu`: acceptance completion,
  one immutable claimed outcome, send-now status, removal intent and outstanding
  attempt state. Clearing an attempt and claiming its result share a lock hold,
  so removal cannot claim a false drop between confirmation and resolution.
  Confirmation advances the FIFO in that same critical section. Otherwise a
  concurrently finishing acceptance could publish deferred `OnDelivered` while
  its confirmed item was still visible in a re-entrant snapshot. Terminal counts
  alone would miss that regression; `TestQueue_Lifecycle_AcceptanceCompletesBeforeObservers`
  also checks that the delivered id is absent from `Snapshot`. The resolver
  owns publication after completed acceptance; otherwise the enqueue caller
  owns it when acceptance returns. No registry retains resolved lifecycle state.
- **Lazy-spawn / exit-on-empty race** is closed under a single lock hold: the
  empty-check and the `draining = false` write are atomic w.r.t. a concurrent
  `Enqueue`, which either appends before the drain takes the lock (drain sees
  `len > 0`, keeps going) or after it released (sees `draining == false`, respawns).
  No interleave loses a message.
- **Shutdown:** parent ctx cancel → `deliver`'s `WaitReady` returns ctx error (or
  `sleepCtx` returns false) → each drain clears `draining` and returns → `wg.Done`
  → `Run`'s `wg.Wait` unblocks → `Run` returns `ctx.Err()`. No drain outlives `Run`.
  A delivery that confirms in the same instant shutdown begins can land in the
  window `drain` checks `ctx.Err()` under `q.mu` **before** advancing or claiming
  confirmed delivery — that head is left queued and fires none of `q.notify`,
  `OnDelivered` or `OnTerminal`: the message reached claude's stdin but
  callback-only consumers receive no record. Stream Claude placement can already have recorded it
  independently; shutdown still offers no gap-free persistence guarantee.
  Inherited from #487/#1484's ordering, not introduced by any one seam;
  a consumer that persists what a seam reports (`internal/history`) is not
  gap-free across a daemon restart for this reason.
  `TestQueue_Lifecycle_RetryGiveUpAndShutdown` covers both ordinary shutdown
  leaving an acceptance unresolved and this confirmed-write gap. `Run` still
  joins every drain; it does not join callbacks deferred to an enqueue caller.
