# `internal/thread` — isolation and lifecycle

Part of the [background store](thread-package-background-store.md) overview.

## Isolation and lifecycle

One worker owns each conversation's fold, completed progress and checkpoint and
certificate writes. Replay runs synchronously; the same reader's `Tail` then
runs in one forwarding goroutine. It borrows at most one bounded chunk, sends
it over an unbuffered channel and awaits the worker's acknowledgement before
returning from the callback. Both waits honor cancellation. Only the worker
feeds, captures, publishes and exports the fold, processing checkpoint deadlines
between completed feed steps. Retirement cancels and joins the forwarder and
stops the timer before final persistence and completion. Items, pending reports,
unresolved parents and join state are private to that conversation. The store
mutex protects worker identities, publications and scalar lifecycle records;
history/checkpoint/certificate I/O, folding, snapshot copying and joining happen
outside it. Paused replay therefore leaves
appends and other conversations' processing and snapshot lookup free to progress.

`threadShadow.follow` polls `Store.Status` every 50 ms for readiness, consumed
version and private activity without traversing or copying items/content.
`Status.Active` includes private unresolved work omitted from public `Items`;
nil items do not establish inactivity. Load/retry and quiescent unload decisions
use the captured status, taken before publication can wait on delivery. Retirement
retains that consumed boundary so a later append still triggers reload. Activity
transitions do not flush. After initial replay a 30-second timer persists changed
consumed versions, including version-only progress or a change followed by
silence. Publications cannot postpone its deadline; unchanged intervals perform
no write. Failed attempts retain pending latest internal records independently
of publication capture and retry after another interval without catch-up bursts.
They emit one fixed content-free warning per attempt and leave usability and
publication intact. Periodic checkpoint state remains dirty; scalar certificates
are written at load and joined retirement. Only item-record appends are
incremental; complete state-file replacements may grow with the manifest and
private maps. See [checkpoint cadence](thread-package-cache-and-epochs.md#worker-checkpoint-cadence-and-retained-changes).

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
cancels and joins it, persisting its last fully consumed eligible fold boundary
before removing its identity. History, index and complete checkpoint state must
be durable before the scalar certificate is written; successful retirement
certifies both surfaces `Clean: true`. Unchanged V and empty version zero still
require final persistence. A final transaction or scalar-certificate failure
returns `ErrPersistence` and invalidates both clean claims with contained
best-effort removal and directory sync; failed invalidation never authorizes
trust. Tail consumption unregisters on exit; items and private continuation
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
cannot authorize a clean write. Eligibility checked before final persistence
can become stale during I/O, so `workerCheckpoint.commit` rechecks the original
parent around scalar certification and invalidates both surfaces if cancellation
wins. `TestStoreCheckpointFinalCancellation` cancels during final state
replacement to exercise that race. Replacement hooks observe both checkpoint
state and scalar writes; a retirement test barrier must gate only once to avoid
closing the same channel twice. Interrupted recovery, source/render failure and
failed or partially consumed feeds never certify clean. A failed feed may have
mutated the fold without completing its boundary; exporting it with the previous
version would falsely certify that progress. Lifecycle persistence failures
remain `ErrPersistence`, including after the worker has been unloaded.

`Shutdown() error` closes admission, cancels and joins all workers, and clears
loaded state after independently certifying each remaining worker's complete
progress and both clean surfaces, including unchanged V and empty version zero.
Already-unloaded checkpoints and certificates are never rewritten,
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
draining](thread-package-shadow-lifecycle-and-evidence-coordination.md#idle-retirement-query-demand-and-evidence). `Store.Shutdown`
cancels workers; callers must finish producer draining and tail catch-up before
invoking it to preserve final commits. Status-only polling preserves this order:
after producers finish, shadow shutdown discovers final activity and joins
coordinators once their usable consumed versions reach the final history
boundaries. History/fold failures end final waiting without certifying complete
draining; successful persistence alone cannot establish a clean daemon exit.
