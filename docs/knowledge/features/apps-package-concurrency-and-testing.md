# `internal/apps` — concurrency and testing

Part of the [apps package overview](apps-package.md).

## Concurrency and persistence

See [publication admission and registry durability](apps-package-concurrency-and-persistence.md#publication-admission)
for operation currentness and post-rename uncertainty, and
[publication fixtures](apps-package-testing.md#publication-fixtures) for their proofs.

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
accumulate blocked goroutines. See [hosted discovery concurrency](v2-session-manager-concurrency.md#hosted-discovery-and-ordered-delivery).

## Testing

See [retained-build fixtures](apps-package-testing.md#retained-build-fixtures)
for independent canonical-byte and rejection proofs, and
[health-failure observations](apps-package-testing.md#health-failure-observations)
for controlled runtime probes that cannot skip transient counts.
See [reconciliation cancellation](apps-package-testing.md#reconciliation-cancellation)
for withheld-response retirement, frozen-clock retry admission and release-before-join
cleanup; elapsed time or child readiness alone cannot establish those phases.

`TestBackupSQLiteWAL` keeps a real supported-Node writer open with automatic
checkpointing disabled and compares an isolated main-file copy first: WAL
existence alone cannot prove the saved preference still depends on WAL. It then
checks preference/ledger/nested-file preservation, removes source data, repeats
fresh verification and asserts unchanged sealed database bytes, DELETE mode and
no sidecars. The [final verifier review](https://github.com/pyrycode/pyrycode/pull/3294#issuecomment-6134101653)
records actual Node v24.15.0 with a controlled aliased temporary root: 86 snapshot
checks executed/passed, 0 failed/skipped; the WAL arm executed/passed once with
0 failures/skips. A runtime skip supplies no SQL evidence.

`TestBackupUninitializedSidecars` also supplies matching altered evidence after
source removal; rejecting an ordinary inventory mismatch alone would miss the
standalone-state invariant. `TestBackupStorageErrors` and
`TestBackupInvalidationErrors` check sealing/cleanup error aggregation and errno
preservation, while `TestBackupCancellation` and
`TestBackupSQLiteCancellation` cover traversal/sealing and started-child joins.
Tests retain source/sealed-byte assertions so cancellation cannot hide mutation.

A no-follow fixture must resolve `t.TempDir()` before deriving app paths and the
exact-root reference. An aliased temporary ancestor otherwise rejects valid
states before snapshotting, while containment refusals can pass for that same
wrong reason. `backupFixture` canonicalizes only the test-owned root;
`TestBackupAliasedTempRoot` runs valid states alongside containment beneath a
controlled `TMPDIR` alias. Production still rejects symlink components. See
[fixture discrimination](development-verification.md#prove-that-tests-distinguish-the-change).

Inspect the compiled bundle itself, not only generated output.
`TestBundleInventory` requires exactly the shared inventory plus the manifest
and inventory JSON; `TestBundleExcludesWorkingFiles` recompiles the production
embed declaration in a temporary package seeded with synthetic private files.
An output-only assertion stays green with recursive embedding because generation
filters the inventory after those extra bytes have entered the binary.

`TestCreateAndInspect` compares generated inventory/content with maintained
assets and preserves edited bytes across reopen and repeated inspection.
`TestCreateOutsideCheckout` runs the compiled test binary from another directory
with empty `PATH`, proving bundled creation needs neither checkout reads nor
commands. Bundle exclusion and generated-content checks prove separate boundaries.

`TestResponseIOAtChunkBoundary` returns bytes together with raw
`io.ErrUnexpectedEOF` or a private source error at known-size and incremental
chunk boundaries. `io.ReadFull` discards even sanitized source errors when those
bytes fill its buffer; error sanitization alone therefore allowed failed sources
to produce successful responses while the original tests stayed green.
`PreparedResponse.stage` records non-EOF source failure independently and rejects
the chunk before retaining it. EOF-derived short reads remain valid; raw source
`io.ErrUnexpectedEOF` remains I/O failure. Each regression checks no result, no
further read, one source close and complete reservation release.

`TestResponseBounds` checks reserved EOF probing and refusal before unreserved
I/O. `TestResponseCancellation` and `TestResponseRetirement` use a source whose
read blocks until concurrent close, proving caller cancellation and generation
retirement unblock staging. The retirement test's stop arm restarts the same
release and rejects old partially consumed results. `TestResponseDisposalRace`
races reading, metadata, delivery-failure completion, close and cancellation,
checking exactly-once closure and zero retained capacity.

Prepare a fixture's committed build before making its release available.
Construction-time reconciliation can execute immediately after the lifecycle
commit; writing the build afterward creates a real missing-release failure and
backoff instead of the intended test setup. `TestOwnerRetryTimings` advances a
private clock while asserting production delays; `TestOwnerRecoveryCleanupBackoff`
blocks actual cleanup so duplicate starts cannot conceal premature scheduling.
`TestOwnerReopenRecovery` checks fresh health, subscription detachment, preserved
intent/data and unlaunched stopped/unpublished registrations after ownership transfer.

Retained-handle rejection before a request does not exercise retirement during
an in-flight response-body read. `TestOwnerTargetRetirement` covers the former;
the stalled-body cancellation and before/after-read guards in `targetBody.Read`
need a separate controlled-response regression. Likewise, endpoint reuse in
`TestOwnerTargetReuse` does not force a delayed target dial across retirement;
connection-admission validation remains essential even when those tests pass.

The template's `React renders caller title as literal text` test calls
`App({title})` directly before server rendering. Keep `App` hook-free and put
state in its rendered `DisplayPreferences` child; hooks in the directly invoked
wrapper would run outside React's renderer. The test bundle uses
`packages: 'external'` so the component and `react-dom/server` share React.
Bundling another React copy breaks hook rendering even when the browser bundle
works. See the [display settings reference](../../hosted-apps.md#display-settings).

`startup and readiness failures` must derive a mismatched release from the
generated manifest. A fixed `1.0.1` rejection value becomes valid when the
[replacement-release browser proof](../../hosted-apps.md#built-ui-smoke-check)
generates 1.0.1 with the same identities/data. Run the deterministic suite for
both generated releases to verify the rejection case remains invalid for each
build.

Controlled-child tests must wait for child-owned signals written after signal
handlers and descendants are initialized. Spawn success alone cannot prove those
exist. `TestOwnerGroupCleanup` checks descendants independently of leader wait;
on Linux, killed orphan zombies retained by container init are distinguished from
live processes. The owner joins its direct child and cleanup targets only its
group; `TestOwnerDuplicateAndIsolation` verifies a sibling remains alive.

PID-bearing readiness markers must publish complete contents atomically.
`publishOwnerPID` writes a temporary file before renaming it into view for the
controlled child's ready/descendant signals. Polling file existence after a
direct `os.WriteFile` can observe its empty create-before-write window and fail
PID parsing before cleanup assertions run. Requiring nonempty bytes in shared
`waitFile` would instead break intentional empty existence-only handshake
markers. `TestOwnerCleanupErrorContext` and `TestOwnerGroupCleanup` consume the
published PIDs; delaying the temporary write must leave no partial PID visible.

Inspect ownership immediately after the original concurrent start/stop operations.
A second cleanup stop can conceal the child missed by a successful first stop.
`TestOwnerStartStopRaces` asserts retirement without that extra operation;
`TestOwnerStopAdmission` checks the child is gone before stopped observation, and
`TestOwnerStopCommitAdmission` covers absent/retired ownership during intent commit.
`TestOwnerPostSpawnPersistenceFailure` checks both returned write errors and actual
child retirement, rather than inferring cleanup from failed persistence.

`TestOwnerSupportedNode` requires an actual supported executable, selected with
`HOSTED_APP_NODE` when the default Node is unavailable or unsupported. A skipped
arm supplies no launch evidence; controlled executable doubles cover failures and
concurrency but cannot replace this check of committed-versus-candidate selection,
real cwd/argv, exact environment, listener handoff and retained database bytes/modes.
Tests shorten the private cleanup grace while `TestOwnerStartStopRaces` asserts
the production default remains five seconds.

The template's `case-variant ledger shadows reject and roll back all pending migrations`
and `migration authorization reserves case-variant durable ledger names` checks
inspect `main.pyry_schema_migrations` and preserve its applied checksums after
rejection. Assert rollback of earlier pending files as well as the offending
file, then retry corrected SQL and rerun to prove idempotence. A nonzero exit
alone cannot establish atomicity; the original green service tests missed a
successful schema commit without its durable ledger entry. The
`runner ledger and schema checks use durable main tables despite temporary shadows`
test checks validation against missing durable state even with matching temporary
objects. See [SQL validation](apps-package.md#template-sql-and-readiness).

The scaffold's `fresh generated app passes the toolchain` test preserves a
sentinel in existing `node_modules` while invoking `npm ci` with an explicitly
selected unsupported Node executable. A nonzero exit alone could pass with a
late `preinstall` rejection after dependencies were replaced; unchanged sentinel
bytes prove rejection precedes that mutation. This arm requires
`HOSTED_APP_UNSUPPORTED_NODE` and `HOSTED_APP_NPM_CLI`; the default run does not
exercise an actual alternate executable. Generated `npm test` checks version
strings separately. Frontend and service type-error injections likewise prove
both trees are checked, rather than trusting an exit-zero typecheck on valid files.

`TestStorageExactKeys` distinguishes required omissions from optional omissions
and rejects aliases at each storage level. `TestStorageAliasShadowing` supplies
raw JSON with an invalid canonical value followed by a valid case alias, including
missing-host inputs checked against two hosts. A normal struct round-trip cannot
produce these corrupt documents. Both tests check byte-for-byte preservation of
rejected storage and exposed the prior decoder bug; broad green gates alone had
missed it. See [development verification](development-verification.md#protocol-boundaries)
for missing/null/type and serialization checks.

Valid-storage fixtures used for byte-for-byte unchanged-open assertions must
already have normalized observed state. An available active record stored as
stopped legitimately becomes starting and writes on reopen; that is not schema
repair. `testStorageDocument` uses stopped intent/state so optional-field checks
do not accidentally test normalization. Rejected storage still remains unchanged.
Restart tests must transfer exclusive ownership between stopped-daemon seeding
and startup, rather than mutating the previous registry and overwriting newer
revisions. Inspect daemon-written JSON before any test-side `Open`: that call
normalizes state itself and could conceal missing daemon initialization. See the
[harness restart proof](e2e-harness-restart-pattern-startin-stop.md#host-app-storage-ownership-and-readback).

`TestRestartNormalization` covers mixed states and reverses disk order to verify
stable revision assignment; `TestNormalizationFailure` checks multiple changes
against insufficient/exact revision budgets and injected write failure.
`TestCommittedChanges` checks disk and committed memory before callbacks, exact
whole records across concurrent mutation/removal, consumer isolation and no replay.
`TestLifecycleFailureAndExhaustion` checks that no-ops and failed mutations leave
file bytes, revisions and notifications unchanged.

Lowercase UUID rejection fixtures need hexadecimal letters: uppercasing a
digits-only UUID changes nothing. `TestManifestBoundaries` uses `AAAAAAAA` to
exercise this boundary, alongside UTF-8 byte limits and exact manifest constraints.
