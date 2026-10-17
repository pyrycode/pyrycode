# `internal/thread` — isolation and lifecycle

Part of the [background store](thread-package-background-store.md) overview.

## Isolation and lifecycle

One worker owns each conversation's fold, completed progress and certificate
writes. It runs replay and the same reader's `Tail` synchronously, serializing
feed callbacks, capture and publication; no tail helper goroutine or checkpoint
timer remains. Items, pending reports, unresolved parents and join state are
private to that conversation. The store mutex protects worker identities,
publications and scalar lifecycle records; history/certificate I/O, folding,
snapshot copying and joining happen outside it. Paused replay therefore leaves
appends and other conversations' processing and snapshot lookup free to progress.

`threadShadow.follow` polls `Store.Status` every 50 ms for readiness, consumed
version and private activity without traversing or copying items/content.
`Status.Active` includes private unresolved work omitted from public `Items`;
nil items do not establish inactivity. Load/retry and quiescent unload decisions
use the captured status, taken before publication can wait on delivery. Retirement
retains that consumed boundary so a later append still triggers reload. Activity
transitions and elapsed time do not write certificates.

`Changes` captures the immutable publication, retained ranges and notification
channel under the same mutex used by publication. Publication closes/replaces
that channel, so a commit between acquiring a baseline and registering a wait
is found by the version lookup. Waiting creates no worker goroutine, performs no
history I/O and holds no lock. Cancellation returns the consumer's context error
without stopping the conversation worker. Failure, unload and shutdown notify
waiters and discard ranges; retirement returns `StateNotLoaded`, while failure
returns `StateUnavailable`. Slow consumers never hold back history writers or
other conversations.

Each conversation worker retains at most 64 publication batches and 1 MiB of
JSON-encoded changes. Older batches are evicted until both limits hold; an
oversize batch clears retained ranges while the complete baseline remains
usable. A count limit alone cannot bound large additions, and JSON escaping can
make a change exceed the byte limit even when its raw history payload fits.
Baselines, additions, replacement maps and content bytes are detached from
later observations; differences, encoding and returned copies run outside the
publication lock. `itemDifference` clones each text suffix into its own allocation:
encoded byte accounting cannot bound a substring that pins an accumulated
message's backing allocation.

`Unload(conversationID) error` marks the worker retiring, clears its publication,
cancels and joins it, certifying its last complete captured progress with
`Clean: true` before removing its identity. Unchanged V still requires this
replacement. Tail consumption unregisters on exit; items and private continuation
state are released while scalar lifecycle results remain. Other conversations
continue independently. `Load`/`Retry` return `ErrUnloading` during retirement.
After unload returns, lookup is not loaded unless explicitly reopened
concurrently. Reopening with `Load` replays history, including commits made while
unloaded. **Public items cannot seed a resumed fold:** `Fold.Items` omits
unresolved children and private pending evidence needed by later joins. Full
replay restores that state before following the tail again.
Compatible recovery metadata preserves the epoch on reload in the same lifetime;
reload retains no earlier change batches, so epoch equality alone cannot prove
continuity from a pre-unload baseline. After usable recovery, `CatchUp` can
prove an in-bound saved version through reconstructed history evidence even
without those batches. Live `Changes` still needs retained ranges or a new
complete `Observe` baseline. Successful unload independently certifies this
conversation; another loaded conversation's unclean stop cannot invalidate it.
`Unload` validates IDs, returns
`history.ErrInvalidID` for invalid ones and reports generic persistence/recovery
errors; repeated calls preserve the recorded result until another load.

Cancellation during `beforeFold` must retain `context.Canceled` internally:
mapping it to generic fold failure would prevent retirement from certifying the
preceding complete progress. `TestStoreCheckpointCancelledFold` checks the saved
version excludes the interrupted chunk. Certification also requires the original
load context to remain uncancelled: parent cancellation racing unload/shutdown
cannot authorize a clean write. Interrupted recovery and other fold failures
remain unavailable. Lifecycle persistence failures remain `ErrPersistence`,
including after the worker has been unloaded.

`Shutdown() error` closes admission, cancels and joins all workers, and clears
loaded state after independently certifying each remaining worker's complete
progress, including unchanged V. Already-unloaded certificates are never rewritten,
and history committed while unloaded is not folded until reopening. Shutdown
attempts healthy workers even when another final write fails: the failed
conversation stays uncertified while healthy ones become clean and retain their
epochs on reopen. Incomplete recovery or any recorded retirement/persistence
failure returns `ErrPersistence`; there is no final shared-run write.
New `Load`/`Retry` work is rejected with `ErrClosed` for
valid IDs. Repeated unloads and repeated/concurrent shutdowns are safe; shutdown
callers join the same operation and receive the same result. Publication checks
worker identity, retirement, shutdown and cancellation, preventing late callbacks
from restoring released state. Cancelling a worker context alone leaves an
unavailable view until retry or unload; it does not shut down other conversations.

The daemon owns [startup discovery, retirement and final committed-version
draining](thread-package-shadow-lifecycle-and-evidence.md#shadow-lifecycle-and-evidence). `Store.Shutdown`
cancels workers; callers must finish producer draining and tail catch-up before
invoking it to preserve final commits. Status-only polling preserves this order:
after producers finish, shadow shutdown discovers final activity and joins
coordinators once their usable consumed versions reach the final history
boundaries. History/fold failures end final waiting without certifying complete
draining; successful persistence alone cannot establish a clean daemon exit.
