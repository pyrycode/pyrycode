# `internal/apps` — lifecycle and restart state

See the [package overview](apps-package.md) for the topic map.

## Lifecycle and restart state

`runSupervisor` constructs one `apps.Owner` through `newDaemonAppsOwner` from
discovery's registry, persisted host identity and
`~/.pyry/<sanitized-instance>/apps` root, including with relay disabled. Its
reconciliation waits for neither spawn nor health before control/conversation
readiness. No channel, client or relay context owns it: client closure, relay
reconnect, channel stop, `/clear` and idle eviction leave app services running.
Committed owner observations feed the existing discovery subscription. Unusable
storage, missing/unsupported Node and app-specific start/health failures leave
normal daemon/channel operation available without cancelling the session pool;
composition logs only static reason codes.

Hosting capability readiness is an infrastructure attestation, distinct from
per-app health. `NewOwner` selects an absolute executable without establishing
runtime support; probing a separate `node` from `PATH` could attest a runtime
the owner never uses. `newDaemonAppsManager` installs discovery's registry and
the same owner as both resource providers, with cancellable delivery, then calls
`Owner.CheckRuntime` before message processing. The probe uses that selected
executable's `--version`, an empty environment, bounded output and a cancellable
five-second context, requiring Node `>=24.15.0 <25.0.0`. `HostedAppsReady` caches
the result without handshake/request I/O. Empty registries and stopped/unhealthy
apps permit negotiation; missing infrastructure or missing/unsupported runtime
withholds `hosted_apps_v1` while normal channels remain usable. Conflating app
health with capability readiness would hide discovery precisely when a client
needs to inspect an unavailable app. Resource lookup still requires current
health-confirmed ownership and returns `app.unavailable` for stopped/unhealthy apps.

Claim the instance with `ctrl.Listen` before `openDaemonApps` or owner
construction, and retain it until `Owner.Shutdown` has joined workers/descendants
and finished durable observations on every exit. `Open` can persist normalized
state and `NewOwner` immediately admits launches: an unsuccessful second daemon
could otherwise launch a competing generation and overwrite an incumbent's
observations. The detached control context and defer ordering in `runSupervisor`
hold this boundary through normal shutdown and post-owner startup failures.
`TestDaemonAppsRejectedDuplicateStartup` and
`TestDaemonAppsShutdownRetainsOwnership` cover both rejected claims and a child
cleanup barrier before final observations/claim release.

`Registry.UpdateLifecycle(serverID, appID, Lifecycle)` replaces committed title,
desired/state, active/pending releases and optional `LastError` atomically. Empty
release strings and nil errors mean unset; identity, manifest and revision remain
registry-owned. `validateSnapshot` enforces existing title/release bounds,
`available`/`stopped` intent, `starting`/`running`/`stopped`/`failed` observations
and an active release for `running`. Invalid fields return `ErrInvalidLifecycle`;
foreign, unknown and tombstoned identities cannot be updated or revived.

Callers supply confirmed observations and safe static errors. `validErrorCode`
accepts the [contract vocabulary](../../hosted-apps.md#errors); messages are
1–160 UTF-8 bytes with no controls or leading/trailing whitespace. Keep parser,
process and I/O diagnostics out of metadata; package errors are returned separately.
Validation cannot establish safe provenance of text. Health confirmation belongs
to callers; the registry runs no processes or probes. A cutover supplies new
title/active release, running state and unset pending/error in one mutation,
avoiding an observable mix of old and new release fields.

`Registry.UpdatePublish(serverID, appID, journal, fields)` replaces or clears the
one outstanding journal and optionally replaces all `Lifecycle` fields in the
same durable snapshot. Nil `fields` preserves lifecycle; nil `journal` clears
the journal. A different publish ID returns `ErrConflict` until the current
journal is explicitly cleared. Ordinary registration, manifest, lifecycle and
observation operations preserve release history and journal metadata.

Before activation, `ActivationCommitted == false` directs recovery to restore
previous data/release; first publish has no `Previous` and no prior release to
reactivate. After activation, `ActivationCommitted == true` selects `Candidate`.
`Owner.Publish` commits that decision together with candidate active release,
title/state and cleared pending release/error through `changePublicationLocked`,
which guards the operation's current revision/ownership before `changeLocked`.
Unconditional `UpdatePublish` alone cannot reject obsolete health/finalization.
Recovery callers clear the journal together with their resulting lifecycle
fields. The registry validates and persists caller-supplied results; it neither
derives those fields from the journal nor performs recovery itself. `NewOwner`
implements [before-activation startup recovery](#startup-publication-recovery);
after-activation resolution remains #3404. Stored references and decisions alone
prove neither readiness nor verified or restored data. See
[publication and recovery](../../hosted-apps.md#publication-and-recovery).

Both metadata mutations use `changeLocked`: validation, persistence, snapshot
swap and ordered notifications share the registry mutex. Failed validation or
persistence changes neither committed memory, revisions nor notifications;
identical mutations are no-ops. `cloneRecord` detaches the history slice, journal,
optional previous reference and last error for mutation inputs, read results and
each subscription delivery. Caller/subscriber edits cannot change committed
metadata, another consumer's delivery or another app's metadata. This local
storage addition does not widen discovery wire records.

`Registry.CommitObservation` checks the expected app record revision under the
registry lock before applying `Observation`. Preserve identity, manifest, title
and active/pending releases; empty `Desired` preserves intent and empty `State`
preserves state/error. A supplied state replaces `LastError`, including clearing
it with nil. Success returns a detached committed record and change flag, even
for a no-op; input and returned error values cannot mutate storage. Zero or stale
revisions return `ErrStale` without writes, revisions or notifications. Compare
the app revision, not the host sequence: an unrelated app commit must not invalidate
the observation. Detached `List` followed by unconditional `UpdateLifecycle`
can overwrite newer fields, so asynchronous process observations use this
conditional path. Unconditional callers retain their complete replacement and
persistence-before-notification behavior.

Use one `Owner` per registry. `NewOwner` resolves one selected Node executable
to an absolute path without fallback and starts background reconciliation without
waiting for spawn or health. It subscribes before the initial `Registry.List`;
the callback only sends a nonblocking coalesced wake, calling neither registry
nor owner methods, including unsubscribe. `reconcileRecords` checks the listed
host revision under registry-then-owner locks and discards stale snapshots before
applying them. This closes the baseline/subscription race without replaying an
older record over a stop, release change or removal. Each wake reads current
committed state; the owner's own observations cannot duplicate launches.

Initial reconciliation and committed registry changes automatically launch only
available registrations with an active release. Admission rechecks that intent
and release; automatic attempts conditionally observe starting without resuming
stopped intent. `Owner.Start` durably resumes available intent when stopped or
locally suppressed, clears old errors and sets starting for a published record;
an unpublished record remains stopped and launches nothing. It runs only
`Record.ActiveRelease`, not the candidate `Record.Manifest`: `prepareRelease` finds exactly one matching
validated local manifest under `builds/<build_id>/app.json`, with lowercase
64-hex directory names. Missing/ambiguous matches, invalid or foreign identities,
nonregular manifests/entries and symlink components/path escapes reject execution.
The actual executable version must satisfy `>=24.15.0 <25.0.0`; service argv is
`node dist/service/main.js`, with cwd at that immutable build. Ordinary start runs
no source/staging code, install, migration or dev server. Inventory verification
and continued build immutability belong to publication; start does not hash the
inventory or create a persisted build index.

Runtime preparation retains existing `data/app.sqlite` bytes while enforcing
data directory 0700 and database 0600. No-follow directory/file descriptors reject
symlink escapes and nonregular databases; creating a missing database file does
not migrate its schema. The environment contains exactly `PYRY_APP_ID`,
`PYRY_SERVER_ID`, `PYRY_APP_RELEASE` (committed active release), `PYRY_APP_DATA_DIR`,
`PYRY_APP_DB_PATH`, `PYRY_APP_BIND_HOST=127.0.0.1`, `PYRY_APP_PORT` and
`NODE_ENV=production`, with no additional or inherited variables. The Node version
check also inherits nothing. See [runtime and layout](../../hosted-apps.md#runtime-and-layout)
for the complete allowlist and paths.

Explicit starts, reconciliation and retries coalesce per app and reuse compatible
current ownership without another child. Committed stop/removal or active-release
changes cancel incompatible generations before replacement. Healthy metadata-only
updates, including candidate manifest edits, retain the current generation and
cannot activate a release or trigger migrations/cutover. Each new launch has fresh
private cancellation, process-group and port ownership. The loopback listener
reserves an ephemeral port unique across the owner's live/in-flight generations; it closes
before spawn for service binding, while private port ownership lasts until
retirement. Failed launch releases it and the next attempt excludes
the app's immediately previous reservation. This cannot exclude unrelated OS
processes during handoff; a bind failure or failed health confirmation retires
ownership and cleans up the group. Ports and generations never enter registry/discovery.

Failed launch, unexpected exit, bind collision and sustained health failure retry
after 1, 2, 4, 8, 16, then 30 seconds, continuing at 30 seconds. `completed`
schedules the delay only after retirement, reservation release, group cleanup and
direct-child wait. A reconciler-owned timer wakes at the earliest deadline;
per-app workers do preparation, probing and cleanup independently, so a failed or
stopping app cannot hold up a sibling's launch or recovery. Duplicate starts and
metadata/observation notifications neither reset nor bypass backoff; an explicit
start during a scheduled delay returns the last launch failure or `ErrUnavailable`.
Sixty seconds of uninterrupted health-confirmed running under the existing
three-failure policy resets the next delay to one second. Check that duration at
retirement too: an exit at the boundary can precede the next successful probe.
Late timers, probes, exits and retained targets cannot affect replacements.

Failure cleanup owns retry scheduling. A duplicate start waiting for cleanup must
not mark a cancelled failed generation explicitly retired: that would suppress its
backoff. Likewise, retain the admitted release record when a conditional starting
write fails; the failed write returns no record, and storing that empty result
would look like a release replacement and skip the delay. Admission-write failures
enter backoff without spawning or spinning.

Start returns after spawn/failure without waiting for health confirmation. Failed
intent/starting writes launch nothing; a final revision/cancellation check and
spawn share the registry lock, preventing a newer commit from slipping between
them. The launch worker starts health supervision after spawn: immediately probe
`GET /_pyry/health`, then every 250 ms for at most 15 seconds from spawn. Each
request, including reply reads, has a one-second timeout bounded by the remaining
startup deadline and a 4096-byte reply cap. `probeHealth` requires HTTP 200 and
JSON with `ready:true`, exact `app_id` and the committed `ActiveRelease` as
`release_version`. Missing/foreign identity, false or malformed readiness,
duplicate/trailing JSON, oversized replies, non-200 status and timeouts never
confirm health. Proxies, redirects and connection reuse are disabled.

`confirmRunning` checks generation/context/liveness under registry-then-owner
locks and durably commits running with a cleared error through the shared
`Registry.CommitObservation` implementation before marking the generation healthy.
Title, desired intent and active/pending releases remain committed values.
A newer app revision, even a title or pending-release change during startup,
rejects confirmation; stale generation results cannot publish readiness either.
Rejected or failed running persistence retires the generation and cleans up its
group. Save the returned running record for shutdown observations. Failure/exit
observations cannot rely on its revision after metadata changes: `observeFailure`
rechecks generation, available intent and active release, then conditionally
updates the latest matching record; newer title, manifest and pending-release
commits survive. Metadata-only commits after
confirmation retain target validity; old-release failures cannot alter replacements.

While running, probes occur every five seconds. Success resets consecutive
failures; one or two failures alone preserve established readiness. The third
retires it, conditionally commits failed if still current, and stops the owned
group. `supervise` measures intervals from probe start rather than adding request
duration to the polling delay, which would stretch the specified cadence. The
absolute 15-second startup bound covers the readiness decision; cleanup retains
its separate five-second grace. Startup failure/timeout, unexpected exit and bind
collision also retire targets and reservations; each retry gets a
fresh endpoint. Late probes/exits cannot affect a replacement.

`Owner.Lookup(serverID, appID)` returns an opaque `Target` only for a local,
still-registered app committed available/running with a matching active release
and a live, health-confirmed owned generation. Persisted running alone supplies
no target. `Target.Do` preserves the caller's path/query but replaces its authority
with the private endpoint. Validity is checked on use, again at connection
admission, after response headers and before/after body reads. A retained handle
checked only at lookup could reach a replacement when app/release/port recur;
private generation ownership, not endpoint equality, prevents that. Post-read
validation prevents newly read bytes being returned after retirement. Removal,
stopped intent/state, active-release changes and retirement reject use. Requests
and response-body reads share caller and generation cancellation; callers must
close response bodies. Proxies, redirects and connection reuse remain disabled;
resource authorization and transfer limits belong to consumers (#3123).

Current validation/spawn failures persist `app.unavailable` / `Service could not start`;
health failures use `Service health check failed`, and unexpected exits
use `Service exited unexpectedly`, only while generation, available intent and
active release still match the latest record.
Child output is discarded. Local diagnostics carry operation context and preserve
`errors.Is`, but no output, paths or endpoints enter metadata. Cleanup still runs
when lifecycle persistence fails; `Stop` returns synchronous failures and
`Shutdown` also surfaces retained asynchronous observation/cleanup failures.

`Owner.Stop` cancels in-flight starts/backoff, durably sets stopped intent, retires
only its owned group, then conditionally commits observed stopped. SIGTERM allows up
to five seconds before SIGKILL of surviving group members and direct-child wait,
including when descendants outlive the leader. Repeated stop is harmless; a failed
intent write still cancels/cleans up, locally suppresses retries until explicit
start, and returns failure without claiming changed intent. Targets are retired
before further use even when persistence fails. Committed stopped intent or removal
wakes reconciliation and cancels startup/backoff without waiting for a probe or
retry deadline; removal retains source/build/data and cannot resurrect ownership.
A final observation persistence failure leaves durable intent intact
without claiming observed stopped. `Owner.Shutdown` closes launch admission,
detaches its subscription outside owner locks, cancels reconciliation, retries and
all generations, and joins every worker and owned process, conditionally observing
stopped without changing desired intent.
Cancelling the initiating start context retires its generation; a coalesced caller
can cancel its own wait independently. The daemon invokes joined shutdown on
normal exit and every startup failure after owner construction. Local stop
preserves sibling services and the session pool; daemon shutdown preserves
available intent for restart. Publication owns migration/private health, atomic
activation, verified update recovery and before-activation startup recovery.
After-activation resolution (#3404), publishing control/daemon wiring
(#3301/#3266) remain downstream. Encrypted resource capability activation is
implemented (#3440), with counted actual Node/template and real-relay acceptance
complete (#3441).
Daemon startup installs no dependencies and runs no migrations. See
[startup and health](../../hosted-apps.md#startup-and-health) for downstream boundaries.

The [#3440 evidence](../../hosted-apps.md#implementation-and-validation-ownership)
at `79e90c4bc36bcd93d12ff8524c94b7c825d54321` records uncached race proofs:
composition **600 executed/600 passed**, runtime readiness **400/400**, and focused
fake-relay resources/readiness/runtime isolation **13/13**, all **0 failed,
0 skipped**, counting parents and subtests. The fake-relay proof uses a race-built
daemon via `PYRY_E2E_BIN` and a synthetic `node` reporting `v24.15.0` and executing
`TestHostedAppProcess`. Connection retirement leaves the service, saved data and
host/app identity intact while fresh negotiation/discovery and reused request IDs
reject old results; the commit/response barrier checks the uncertain write is not
replayed. Runtime/fake-relay counts are builder-recorded; the final verifier
independently repeated composition 600/600.

The [#3441 evidence](../../hosted-apps.md#implementation-and-validation-ownership)
adds real-loopback-relay `TestLiveRelay_HostedResources` and
`TestLiveRelay_HostedTemplate`: each **1 executed/1 passed, 0 failed, 0 skipped**,
independently repeated by the verifier with race-instrumented tests and daemon;
the complete package is **5/5**, including a backend helper entry point.
Partial-read and committed-write disconnects retire connection workers while
preserving service PID, saved data and host/app identity. Reconnect refreshes
negotiation/discovery and reads with retired IDs without replaying the uncertain
write. The separate unchanged template runs on actual Node **v24.15.0** and proves
encrypted preferences POST/204/GET with nondefault readback. Its releases,
migrations and seed data are prepared before daemon ownership; ordinary daemon
startup still performs no migration. Native-viewer, publisher and live-channel
acceptance remain downstream; no live Claude was required. See the
[paired resource fixtures](apps-package-testing.md#paired-resource-fixtures)
for barriers and timing bounds that distinguish request-ID fencing from cleanup.

`Open` validates storage before `normalizeSnapshot`. Every available record with
an active release becomes starting; every unpublished or intentionally stopped
record becomes stopped, regardless of stored state. All other fields and
tombstones are preserved. Only changed states receive revisions, in stable app-ID
order. The total revision budget is checked before one snapshot write; exhaustion
or persistence failure returns no registry and preserves the committed bytes.
An unchanged reopen neither writes nor advances revisions, even at the ceiling.
Opening exposes this committed normalized baseline, not earlier-process events.
`Open` alone launches nothing. After shutdown, transfer exclusive root ownership
to a reopened registry and construct a new owner: available committed releases get
fresh processes/generations and must pass fresh matching health and conditional
running persistence. Stopped/unpublished records stay unlaunched and existing data
bytes survive; stored running never establishes readiness.

The [final #3204 verifier review](https://github.com/pyrycode/pyrycode/pull/3237#issuecomment-6117617232)
records `TestHostedAppsRecoverySQL` on actual **Node v24.15.0** with **1 executed,
1 passed, 0 failed, 0 skipped**, using a race-built daemon and the freshly compiled
maintained template service. Migration/SQL seeding happened while stopped.
HTTP save returned 204 for
`{"version":1,"selected_tab":"preferences","period":"7d"}`; service readback
and direct `node:sqlite` queries retained it after app crash/recovery and daemon
restart, with unchanged host/app identity, committed release selection and
monotonic revisions. The proof also observed failed → retry delay → starting →
fresh health-confirmed running, a healthy sibling, an unrelated fake-Claude turn,
unlaunched stopped/unpublished apps and clean shutdown descendant cleanup.
Backoff is a delay, never another registry state. No live Claude was used or
required; seeded persistence supplies no resource-routing or publisher
migration/rollback acceptance. See the [runtime reference](../../hosted-apps.md#runtime-and-layout).

### Startup publication recovery

`Open` preserves legacy loading and strict storage validation before recovery;
malformed storage is rejected without rewriting it. After exclusive ownership
transfers from a joined previous owner, `NewOwner` admits one asynchronous worker
per uncommitted journal without `RetirementPending`, before ordinary reconciliation
or `prepareRelease` can write data. Construction waits for neither restoration nor validation/health.
Until conditional durable completion, that app's Start, reconciliation, Publish,
API and asset targets remain barred. Restoration and validation waits hold neither
registry nor owner locks, so another app's reconciliation, targets and registry
operations remain usable.

| Restart evidence / intent | Recovery and admission outcome |
|---|---|
| Uncommitted update, available | Freshly verify/restore the entire snapshot, freshly `VerifyBuild` the previous release, then privately confirm matching previous-release health before exposing targets. |
| Uncommitted first publication, available | Restore data; durably finish failed and unpublished, without a target. |
| Uncommitted update, stopped | Restore data, freshly verify the previous build and validate its restored schema, saved preferences and complete migration-version/checksum ledger; remain stopped without a service or target. |
| Uncommitted first publication, stopped | Restore data; durably remain stopped and unpublished, without a target. |
| Committed activation journal | Keep the candidate decision; never restore pre-publication data. Reopened targets remain barred pending #3404. |
| `RetirementPending`, with or without a journal | Remain barred; retained backup evidence does not establish positive writer retirement. |

Recovery runs no migrations. Stopped validation uses the verified release's
`schema`, `ledger` and `migrationFiles` helpers against read-only SQLite, without
starting its HTTP service. Successful before-activation completion clears pending
release/journal atomically with the resulting state and safe interrupted-publication
error; it retains committed title, previous active release (if any), manifest and
intent. Candidate manifest metadata never becomes committed display metadata.
`usableLocked` permits a committed journal only for its still-live health-confirmed
activated publication generation; that exception supplies no readiness on reopen.

Stop/removal/shutdown and incompatible newer commits cancel stale recovery.
Completion checks exact app revision, journal and ownership, preserving newer
fields/intent and never reviving a tombstone; even stopped → available reuse cannot
validate old work. Stop/Shutdown join workers and private processes. Unresolved
recovery rejects removal without discarding registration/evidence. A newer stop
cannot be undone by late health or finalization.

Keep the journal, previous build and immutable backup until completion is durable.
Partial replacement, interruption after restoration and failed/uncertain finalization
remain retryable from the retained snapshot on a fresh exclusive reopen, with fresh
verification each time; failures do not trigger a busy automatic retry loop.
Missing/invalid snapshots, restoration I/O, damaged previous builds and incompatible
or unhealthy restored data leave no target or successful-rollback claim. Safe errors
use `app.io_failed` for restoration failures where persistence permits and
`app.unavailable` for previous-build/compatibility/health failures. Evidence remains
when persistence fails. Host/app revisions never reset, decrease or wrap; exhaustion
refuses completion. Publishing composition/integration remains #3266 and local
publishing control remains #3301. See the
[reference matrix](../../hosted-apps.md#update-and-startup-recovery-contract).

### Publication writers and completion

`prepareRelease` can create data/database files even when launch later fails, so
backup verification and durable journal completion must precede it, not just the
migrator. After possible mutation, `finishPublicationFailure` restores only after
positive whole-group retirement. Leader exit leaves descendants able to write;
`candidateRetirementError` or generation cleanup uncertainty retains the journal
and forbids restoration/removal. Cancellation-independent restoration is joined
by Stop/Shutdown. A committed or uncertain activation also forbids destructive
restoration; see [durability](apps-package-concurrency-and-persistence.md#registry-durability).

SIGKILL delivery is not retirement proof. `Owner.cleanup` polls for up to one
second after forced termination, accepting only positive group absence. Transient
inspection errors such as macOS EPERM can resolve through a later ESRCH; EPERM
itself never establishes absence. After a successful group probe,
`candidateGroupLive` also permits Linux groups containing only nonexecuting
zombies; this fallback cannot bypass a denied probe. Denied TERM/KILL signals
remain errors even after confirmed absence. Persistent inspection uncertainty or
live members at the deadline return unconfirmed retirement. The publication
supervisor joins both cleanup helpers and the asynchronous leader wait before
`completed` closes `g.done`; completion carries cleanup failure and is not by
itself a successful-retirement claim.

Retirement uncertainty must remain distinct from the health sentinel
`ErrUnavailable`: `finishUpdateFailure` interprets that sentinel as unconfirmed
previous-service health. Reusing it for cleanup timeout would misclassify unsafe
writers as `app.unavailable`. Uncertain retirement retains static `app.io_failed`
metadata, the journal/`RetirementPending` as applicable, unavailable targets and
barriers against restoration, removal, reuse and retry. See the
[controlled retirement proofs](apps-package-testing.md#publication-fixtures).

Previous-release health is a recovery decision, not candidate activation.
`confirmRunning` dispatches recovery to `confirmRecoveryLocked`, which clears
evidence and exposes the previous release only after verified restoration (when
mutation was possible), fresh previous-build verification and matching live
health, with current available intent/ownership. Reusing ordinary running
confirmation would clear the update error; reusing candidate activation would
announce a failed release as successful. `Owner.Publish` always returns candidate
failure when `finishUpdateFailure` runs, distinguishing healthy retention, unchanged-data restart,
confirmed restored health and failed/unconfirmed recovery. A restored database
alone cannot prove an older service accepts its schema. Failed restoration,
previous-build/health or recovery-finalization checks retain evidence and suppress
automatic reconciliation. See [update feedback](../../hosted-apps.md#update-and-startup-recovery-contract).

Recovered generations set `publication.recovered`, not `publication.activated`.
`completed` and `Owner.Shutdown` must recognize both when observing stopped, and
retain the safe update error for recovered generations. Otherwise successful
shutdown joins the process but leaves durable running state. Checking only
shutdown's return value misses that mismatch;
`TestUpdateRecoveredCompletion` inspects disk state/intent/release/error and
actual retirement for shutdown and stopped intent.

A generation-local `cleanupErr` alone loses failures when ownership is replaced
and lets Shutdown report success. Both ordinary and publication supervisors must
finish group cleanup and leader join before `completed` aggregates that error
once into the owner, before closing `g.done`. Owner failure retention survives
generation completion/replacement and repeated Shutdown. Regressions must inspect
actual descendants, persisted removal/recovery decisions and retired targets;
see [publication fixtures](apps-package-testing.md#publication-fixtures).
