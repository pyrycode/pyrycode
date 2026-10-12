# `internal/apps` — concurrency and persistence

See the [package overview](apps-package.md) for the topic map.

## Concurrency and persistence

Stable preparation subscribes before committed identity resolution to close the
registration race. Registry callbacks only cancel incompatible work; taking a
result lock there would invert the result → registry → owner checks. Its watcher
observes caller cancellation, generation cancellation and process exit, closes
the source before waiting for the staging mutex, then drops chunks, releases
capacity and unsubscribes. Waiting for that mutex before source closure deadlocks
when a staging read needs `Close` to unblock. `Complete`/`Close` join the watcher;
independent once guards serialize source closure and disposal.

Capture/cancel ownership and commit stopped intent under registry-then-owner
locks, also used by start admission and final spawn. Capturing ownership before
the intent write leaves an admission gap: an absent or retired generation can be
replaced while stop commits stopped/stopped and misses its child. Release both
locks before joining workers. Registry callbacks must not call owner methods
either, since owner operations can hold its mutex across registry notifications.

Concurrent stops can share the stopped-intent revision while only the first final
observation advances it. `Owner.Stop` accepts that stale observation only after
checking the latest record already has stopped intent and state; treating every
`ErrStale` as success would hide a newer available intent.

`Registry.change` holds one mutex across validation, detached candidate creation,
persistence, the committed snapshot swap and synchronous notifications. Even the
unchanging host ID must be read under that lock: reading `snapshot.ServerID`
outside it races with replacement of the whole snapshot. Constancy of the value
does not make its containing storage
immutable. `TestPersistenceAndVisibility` exercises concurrent registrations and
readers, blocks a writer before commit and checks that readers cannot pass it.

Detect semantic no-ops before revision exhaustion. An identical registration,
manifest or lifecycle update, or repeated removal of a tombstone, still succeeds
without a write or change at the maximum sequence. Compare lifecycle errors by
value, not pointer identity. Each changed mutation advances the durable host
sequence once and stamps its record or tombstone. A failed write never swaps
committed memory or notifies consumers.
`Registry.List` returns records and host revision from one detached snapshot,
including independent last-error values. Discovery must preserve this atomic
pair: reading the revision separately could label records from another commit.
The relay maps committed `Record` fields, never candidate manifest title/release
metadata, and rechecks the snapshot revision on each continuation. Equal app IDs
on different host roots remain separate registrations; connection-owned cursors
cannot select a host. See [discovery paging](../../protocol-mobile.md#paging-cancellation-and-validation).
Use one registry owner per root;
independent owners and cross-process writes to the same root are unsupported.
Reopening can commit newer revisions, so transfer ownership to the reopened
registry and stop using its predecessor. Mutating the predecessor's stale
snapshot could overwrite normalization and move durable revisions backwards.

`Registry.Subscribe(func(Change))` delivers future changes synchronously under
the same mutex in increasing host revision order across concurrent mutations.
Each consumer owns a detached whole `Record`, including its own `LastError`;
removal carries server/app identity and revision with nil `Record`. Later
mutations or another consumer's edits cannot alter earlier deliveries. No-op,
rejected and failed mutations produce no notifications. Subscriptions persist
neither events nor consumers and replay no baseline or previous-process changes.

Callbacks must return promptly and never re-enter any registry method, including
the returned idempotent unsubscribe: doing so would deadlock on the held mutex.
Slow callbacks also block mutations and readers. Consumers own later transport
work; the registry adds no delivery goroutines or queues.
`V2SessionManager.HostedAppsChanged` provides the relay consumer, with bounded
handoff and no peer I/O, worker creation or registry re-entry. Handoff and delivery
share a 16-envelope capacity bound per connection; separate bounds would retain
more than the promised total when a peer stalls. Overflow retires that generation
so fresh discovery recovers committed state, instead of silently dropping a
revision on a current connection. Daemon subscription ownership belongs to
`newDaemonAppsManager` and relay cleanup: install only after successful
manager construction, before `Run`, and detach before manager drain. Channel
stop, clear and eviction cannot detach this daemon-lifetime subscription.
Installing discovery and `HostedAppsSend` must preserve `HostedAppsReady` as the
complete registration/supervision/resource attestation; treating a registry or
cancellable sender as readiness would advertise services that do not exist.

Because `List` can wait on this mutex, relay cancellation retires its result
without releasing read capacity until the
original reader returns. Starting a replacement reader on every cancel would
accumulate blocked goroutines. See [hosted discovery concurrency](v2-session-manager-concurrency.md#hosted-discovery-and-ordered-delivery).
