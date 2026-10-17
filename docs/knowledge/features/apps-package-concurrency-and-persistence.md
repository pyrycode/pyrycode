# `internal/apps` — concurrency and persistence

See the [package overview](apps-package.md) for the topic map.

## Concurrency and persistence

### Checked candidate recording

`Registry.PrepareCandidate` builds outside the registry mutex, then
`recordCandidate` holds it across the current live-registration check, byte/version
decision, installation/reverification and `changeLocked` persistence. A read then
`RecordRelease` would allow competing candidates to decide against obsolete
history. New versions compare numeric components against all recorded versions
and any legacy active release; recorded-byte retries bypass progression so an
older identical candidate can be prepared after newer history exists.
`Owner.Publish` must separately reject a historical downgrade against the active
release; preparation's retry success does not authorize cutover. Only an identical
healthy active candidate may return publication success without migration,
restart or an activation revision.

Verify a recorded build before calling `InstallBuild`: installation can create
a missing destination, which would silently repair a damaged recorded candidate.
Retries freshly verify retained contents and the recorded lockfile digest,
returning original metadata without revision/history changes. Missing, partial
or changed recorded builds fail unchanged. A failed durable write leaves no
history entry or revision, although an unrecorded sealed build can remain and
be verified/reused on retry. Preparation preserves the latest committed
manifest/display/lifecycle/journal fields and acquires no Owner lock; build
success establishes no activation or persistent-data mutation. See
[checked preparation](../../hosted-apps.md#checked-candidate-preparation).

`candidateCommand` joins each leader and confirms no live owned process-group
members remain before the next phase or artifact validation. Leader exit alone
does not establish cleanup: descendants can continue changing build output.
Cancellation stops the whole group and later phases; cleanup failure also
stops preparation. Linux orphan zombies are inert bookkeeping rather than live
build work. The [controlled fixtures](apps-package-testing.md#checked-candidate-fixtures)
prove the ordering, not merely the absence of children after return.

### Publication admission

Acquire registry then owner locks before `PrepareCandidate` to reserve one
publisher per app. Explicitly request available intent, but use operation
ownership to suppress Start/reconciliation; toggling intent as a lock can erase a
real stop. Admission uses conditional `commitObservationLocked` to request
available intent without normalizing observed state: `intentLocked` would turn a
healthy old service into starting during checks and add a revision on an identical
active retry. Install `subscribeLocked` cancellation during admission: subscribing
later misses stopped → available reuse during a parked build. Remember incompatible
notifications in `publication.invalidated`: checking only the final available
intent lets an obsolete recovery generation restart after stop → available reuse,
even though recovery deliberately ignores candidate-context cancellation.
Preparation and pending commits legitimately advance this operation's expected
app revision;
other-app commits do not invalidate it. Guard activation/finalization against
current ownership, manifest, intent and revision. Join work outside both locks.
Removal blocks admission, calls Stop outside registry locks and tombstones only
after joined recovery with no outstanding recovery evidence. See
[publication and recovery](../../hosted-apps.md#publication-and-recovery).

Persist `Record.RetirementPending` with exclusive starting/pending state before
signalling the predecessor, then atomically replace it with the freshly verified
backup journal. A joined leader can leave a live descendant when group retirement
fails, before any backup exists. In-memory retry suppression is insufficient:
Start can reset it and owner replacement loses it. `Record.recoveryPending`
therefore bars Start, Publish, reconciliation and removal on either retirement
evidence or a journal, including after reopen; `usableLocked` also rejects the
retirement flag. Clear it only after positive retirement and safe failure
completion or health-confirmed recovery. No flag claims a verified backup.
Conversely, a verified backup does not prove retired writers: persist this flag
alongside the journal when service or stopped-validator cleanup is uncertain.
Cancellation can race a newer stop, so preserve uncertainty against the latest
matching journal without replacing its newer lifecycle fields. Otherwise a fresh
owner could restore beneath a surviving writer. Stop/Shutdown must surface the
cleanup error; `NewOwner` skips recovery while the flag remains set.

### Registry durability

`snapshotOps.write` must sync the file, rename it and sync the containing directory
before swapping committed memory or announcing changes. Rename alone does not
establish crash durability. If directory sync fails after rename,
`errSnapshotUncertain` quarantines `Registry.changeLocked`, including no-ops: a
later write derived from the old memory snapshot could overwrite the new on-disk
decision. Keep the last durable memory snapshot, retain journal/backup evidence,
bar target exposure and forbid destructive restoration. An uncertain activation
cannot claim committed activation; uncertain finalization retains the previously
durable committed decision. Before-activation startup recovery is implemented;
reopened committed-journal resolution remains #3404. See
[durability fixtures](apps-package-testing.md#publication-fixtures).

### Registry and process ownership

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
without a write or change at the maximum sequence while registry durability is
established; an uncertain registry rejects even no-ops. Compare lifecycle errors by
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
accumulate blocked goroutines. See [hosted discovery concurrency](v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md#hosted-discovery-and-ordered-delivery).
