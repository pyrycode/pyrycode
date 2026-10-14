# `internal/apps` — lifecycle and restart state

Part of the [apps package overview](apps-package.md).

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
derives those fields from the journal nor performs recovery itself. Callable first
publication is implemented; updates (#3360) and interrupted-publication startup
recovery (#3263) remain downstream. Stored references and decisions alone prove
neither readiness nor verified or restored data. See
[first publication](../../hosted-apps.md#callable-first-publication).

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
available intent for restart. First publication owns migration/private health and
atomic activation; updates (#3360), interrupted-publication startup recovery
(#3263), publishing control/daemon wiring (#3301/#3266) and encrypted resource
capability activation (#3123) remain downstream. Production `HostedAppsReady`
remains unsatisfied and `hosted_apps_v1` withheld until resource routing completes
the attestation. Daemon startup installs no dependencies and runs no migrations. See
[startup and health](../../hosted-apps.md#startup-and-health) for downstream boundaries.

See [publication writers and completion](apps-package-lifecycle-and-restart-state.md#publication-writers-and-completion)
for restoration prerequisites and shared retirement-error retention.

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
