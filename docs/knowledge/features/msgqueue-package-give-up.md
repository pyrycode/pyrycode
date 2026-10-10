# Message queue persistent-failure give-up

Part of [`internal/msgqueue`](msgqueue-package.md).

## Bounded give-up on persistent delivery failure (#1000)

The drain's retry loop is deliberately lossless: a `Deliver` error retries the
**same** FIFO head after `RetryInterval`, forever, by design — that's what
bridges a claude-**child** respawn. Before #1000 that loop had no upper bound,
so a claude session parked at startup (an unanswerable dialog, a wedged
readiness gate, a network stall) looped on a head that never drained and never
failed, leaving the client with a message that neither ran nor errored. #1000
adds a **bounded** give-up while preserving losslessness for the case it exists
to bridge.

- **The bound is elapsed-time, not attempt-count or error-identity.** `Deliver`'s
  errors (`ErrNoLiveSession`, `ErrTurnNotCommitted`, PTY write errors) are
  **indistinguishable by type** between an ordinary respawn and a persistent
  wedge — the drain cannot tell "retry, this will clear" from "give up, this is
  stuck" by inspecting `err`. `Config.GiveUpAfter` (`<= 0` ⇒
  `defaultGiveUpAfter`, 2 minutes) is instead a per-head elapsed-wall-clock
  deadline, measured from the head's **first** consecutive delivery failure via
  a `drain`-local `firstFailedAt`. `defaultGiveUpAfter` is chosen to exceed
  `internal/supervisor`'s max backoff window with margin (`BackoffMax=30s`,
  `BackoffReset=60s`) — a transient failure spanning a full claude-child
  respawn/backoff cycle clears well inside the bound and never trips give-up.
- **Per-head, not per-session.** A successful delivery resets `firstFailedAt` to
  zero for the next head, so a wedge that clears for one message doesn't poison
  the bound for the next — each head gets a fresh give-up window. A head swapped
  mid-retry (a `Remove` during the sleep) restarts it too (#1485).
- **Give-up drops one head and exits the drain, it does not skip to the next
  head — when there is still a head to abandon.** Because a startup wedge would
  fail every subsequent head identically, continuing would burn a full
  `GiveUpAfter` window per head and emit one give-up event per queued message for
  a single wedge. So when the front of the FIFO is still the head that failed,
  `giveUp` drops only that head (`advanceLocked`), claims `TerminalGiveUp` under
  the same lock and clears `draining`. Off-lock it publishes the terminal fact
  if acceptance has completed, then `notify(OnChange)` and `notifyGiveUp(OnGiveUp)`;
  otherwise acceptance completion publishes the deferred terminal fact.
  Clearing `draining` first means an observer can safely re-`Enqueue` without
  racing a still-`true` flag, and the drain goroutine **returns**. Any items
  still behind the dropped head stay queued; `draining == false` with
  `len(items) > 0` is exactly `maybeSpawnDrainLocked`'s respawn precondition, so
  the next `Enqueue` respawns the drain — the same lifecycle the pre-existing
  empty-exit path already uses.
- **A head dequeued inside the failure window is cancelled, not abandoned
  (#1484).** `Remove` can take a merely-waiting head at any point in the window,
  including after the drain's post-delivery `dropped` read. When it has, the
  id-checked `advanceLocked` drops **nothing**, and nothing is reported either:
  the advance decides **before** the `Warn`, so no `TerminalGiveUp` or `OnGiveUp`
  fires, no
  give-up line names that id, and the backlog behind the cancelled head is
  untouched. `draining` stays set and the drain **continues** with a fresh
  give-up clock — the same treatment the loop already gives a head dropped before
  it committed ("a clean cancellation, not a delivery failure"). The
  exit-don't-skip rule above bounds *abandonment*; here nothing was abandoned.
  `giveUp`'s bool return is welded to that: `true` ⟺ `draining` was cleared ⟺ the
  drain returns. Decoupling them would either strand the conversation (a live
  drain with `draining == false` lets `maybeSpawnDrainLocked` start a second one)
  or block respawn forever.
- **`GiveUpFunc` mirrors `ChangeFunc`'s seam contract** (never under `q.mu`, must
  not block, concurrency-safe, nil ⇒ disabled) and carries a daemon-generated
  `reason` (the elapsed window + `err.Error()`) that **never** contains
  `head.text` — extending the package's `NEVER log head.text` discipline (see
  [Error handling](msgqueue-package.md#error-handling)) to both the give-up log
  line and the seam payload.
- **Shipped unwired, now live.** `OnGiveUp` was `nil` in production at this
  ticket's landing — same engine-first rhythm as the package's original #704
  landing (mechanism first, wired later). Split from #1001 into wire vocabulary
  (#1007 — `TypeSessionError`/`CodeSessionBlocked`/`SessionErrorPayload`, shipped
  unwired) and its blocked-by-#1007 producer sibling (#1008, shipped), which set
  `OnGiveUp` to a live `session_error` producer (`cmd/pyry/session_error_v2.go`)
  exactly as `OnChange` routes to the `queue_state` producer — a persistent
  delivery failure now surfaces as a typed, client-visible frame instead of a
  silently dropped head.

- **The bound is conditionally exempt, gated per conversation, not per error type
  (#1911).** `Config.Pending` was left unset from #1348 (which deleted the only
  producer of `#1014`'s `supervisor.ErrTrustModalPending`) until the stream path
  needed the same exemption for a head held behind an approval parked on a
  person. Because `PendingFunc` is blind to the conversation, the composition
  root cannot ask "is a person deciding on this conversation?" inside it — that
  question is answered at the delivery seam, which re-marks *only* the hold
  error (not every error the seam can return) before it reaches `Pending`. That
  precision is what keeps the bound satisfiable: exempting every delivery error
  during a parked approval would let a conversation that is separately and
  genuinely wedged (a failing `resolve`, a dead `Activate`) ride along on
  somebody else's decision time indefinitely. See
  `cmd/pyry`'s `approvalParkedReport`/`markApprovalHolds`/`approvalHoldPending`
  and [features/streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md](streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md).

## Testing

Stale give-up tests must establish removal before exercising the expired
attempt. Removing on attempt two with a 1 ms deadline is scheduling-sensitive:
synchronous retry-warning logging can exhaust the window on attempt one and
legitimately abandon the head first. `TestQueue_Lifecycle_RetryGiveUpAndShutdown`
removes during the first failure, explicitly calls `giveUp` with an expired
duration, and checks rejection plus exactly one removed terminal fact. The
`giveUpRow` cases in `head_advance_test.go` retain real retry-window coverage;
the stale-guard proof does not depend on reaching another attempt in time.

See [codebase/1000.md](../codebase/1000.md), [codebase/1007.md](../codebase/1007.md),
[codebase/1008.md](../codebase/1008.md).
