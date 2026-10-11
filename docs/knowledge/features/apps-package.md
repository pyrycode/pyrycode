# `internal/apps` — durable host-app registrations

This package owns strict manifest validation, durable registrations and callable
health-confirmed committed-release process ownership independent of conversations.
The [hosted-app reference](../../hosted-apps.md#manifest-and-identity)
owns the field tables and registration behavior; the
[v1 contract](../../specs/architecture/3120-hosted-app-contract.md) owns the broader
hosting design. Registration and stored `running` prove neither publication nor
readiness. The registry commits lifecycle observations and normalizes restart
state. Process spawn proves only starting; the owner confirms health before
exposing private targets. Daemon wiring (#3204), automatic recovery (#3207),
publication (#3125) and encrypted resource activation (#3123) remain separate
responsibilities.

## Validation and stored identity

Exact JSON schema validation must precede Go struct decoding. A decoder using
`DisallowUnknownFields` still accepts case variants of struct keys, so a later
`APP_ID` or `REVISION` can mask an invalid canonical value. `strictJSON` rejects
duplicates, nulls and trailing values; `validStorageSchema` and `storageObject`
then require exact mandatory/optional keys at snapshot, record, tombstone and
last-error levels. `ValidateManifest` separately enforces exact manifest keys and
the fixed v1 values. Do not relax storage validation to the wire contract's
additive unknown-field policy.

Existing storage must decode into a zero-value snapshot. Prefilling it with the
caller's host ID makes a missing stored `server_id` appear local and can open the
same corrupt file under different hosts. `Open` initializes the caller identity
only when storage is missing, validates the stored host otherwise, and leaves
rejected bytes untouched. The canonical UUIDv4 check reuses
[`identity.ParseServerID`](identity-package.md#parseserverid--validation).
Identity is `(server_id, app_id)`; separate hosts require separate roots.

## Template toolchain

The [standalone source scaffold](../../hosted-apps.md#template-and-saved-preferences)
shares the manifest contract with `ValidateManifest`; generation and a successful
build establish neither registration nor service readiness.

An npm `preinstall` hook runs too late to reject an unsupported runtime before
dependency installation. Keep the template's npm `devEngines` runtime error
policy, `engines` range and `.npmrc` `engine-strict=true` together with
`assertRuntime` checks in command/compiled entries. The check concerns the actual
executable and must not silently choose another Node version.

Portable compiler/bundler choices keep the scaffold's dependency closure small
across Linux and macOS. TypeScript 7's native platform packages expanded the
lockfile substantially; the template pins the JavaScript TypeScript 6 compiler
and `esbuild-wasm` instead. TypeScript 6's side-effect import checking needs the
explicit `declare module '*.css'` in `assets.d.ts` for the frontend stylesheet
import; removing it breaks frontend typechecking.

`scaffold` copies an explicit `files` inventory rather than the whole source
tree. New shared service modules, migration SQL, browser scripts and tests must
enter that inventory; testing the maintained source tree alone can pass while generated
apps lack dependencies or service coverage. Verify a fresh generated app through
install, frontend/service typecheck, tests and build. `npm test` compiles its
service dependencies itself, so a prior build cannot mask missing test setup.

## Template SQL and readiness

`schema` must validate the immutable preference table definition as well as
columns, saved values and integrity. Matching column names/types alone lets a
table stripped of CHECK constraints or STRICT mode report ready. Startup and
health also require the complete checksum ledger; they cannot repair/migrate
storage as a readiness side effect. The [template reference](../../hosted-apps.md#migrations)
owns migration commands and persistent-data placement; [ADR 043](../decisions/043-hosted-app-runtime-and-isolation.md)
owns the trusted-service isolation boundary.

SQLite identifiers are case-insensitive, and unqualified object lookup searches
the temporary schema before `main`. A case-sensitive ledger guard therefore
lets a temporary uppercase/mixed-case ledger capture runner writes: migration
can exit successfully with committed schema but no durable checksum, then fail
on rerun. `migrationSQL` reserves ledger identifiers case-insensitively and
rejects non-main operations; runner ledger, schema and preference API SQL target
`main` explicitly. Keep both protections so validation cannot accept temporary
shadows as durable state.

Migration authorization lasts only while the SQL file executes. Temporary or
persistent triggers could defer ledger mutation until runner SQL executes after
authorization is removed. Deny both trigger forms, alongside transaction control,
connection changes and external effects, so all pending schema/ledger work stays
within the runner's single transaction.

## Manifest updates and retention

The stored manifest can describe a candidate title/release while the committed
record still describes the previous one. `Registry.UpdateManifest` changes only
the manifest and record revision; it preserves committed title, active/pending
release, desired/state and last error. Treating the manifest as an activated
release would bypass publication and health confirmation.

Registration equality compares manifests, not whole records. Re-registering an
identical validated manifest after lifecycle changes must leave committed title,
releases, state and errors intact. Comparing it with the initial registration
defaults would turn that no-op into a conflict or overwrite confirmed state.

`Registry.Remove` retains source/build/data and permanently tombstones the ID.
Tombstones survive reopen and are excluded from the 256 live-registration limit;
discarding them to bound storage would permit removed identities to be reused.

## Lifecycle and restart state

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
to an absolute path without fallback. `Owner.Start` durably resumes available
intent, clears old errors and sets starting for a published record; an unpublished
record remains stopped and launches nothing. It runs only `Record.ActiveRelease`,
not the candidate `Record.Manifest`: `prepareRelease` finds exactly one matching
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

`Owner.Start` coalesces concurrent calls and reuses the current owned generation
without another child. For an already spawned generation, an explicit start after
committed release/intent changes retires the old generation before replacement. Each new launch has fresh private
cancellation, process-group and port ownership. The loopback listener reserves an
ephemeral port unique across the owner's live/in-flight generations; it closes
before spawn for service binding, while private port ownership lasts until
retirement. Failed launch releases it and the next explicit attempt excludes
the app's immediately previous reservation. This cannot exclude unrelated OS
processes during handoff; a bind failure or failed health confirmation retires
ownership and cleans up the group. Automatic retries remain downstream. Ports
and generations never enter registry/discovery.

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
group. Save the returned running record for subsequent failure/exit/shutdown
observations: using the starting revision would reject a real exit as stale and
leave discovery running. Metadata-only commits after confirmation can retain
target validity, but subsequent observations still cannot overwrite newer commits.

While running, probes occur every five seconds. Success resets consecutive
failures; one or two failures alone preserve established readiness. The third
retires it, conditionally commits failed if still current, and stops the owned
group. `supervise` measures intervals from probe start rather than adding request
duration to the polling delay, which would stretch the specified cadence. The
absolute 15-second startup bound covers the readiness decision; cleanup retains
its separate five-second grace. Startup failure/timeout, unexpected exit and bind
collision also retire targets and reservations; a later explicit launch gets a
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
use `Service exited unexpectedly`, only while their app revision is current.
Child output is discarded. Local diagnostics carry operation context and preserve
`errors.Is`, but no output, paths or endpoints enter metadata. Cleanup still runs
when lifecycle persistence fails; `Stop` returns synchronous failures and
`Shutdown` also surfaces retained asynchronous observation/cleanup failures.

`Owner.Stop` cancels in-flight starts, durably sets stopped intent, retires only
its owned group, then conditionally commits observed stopped. SIGTERM allows up
to five seconds before SIGKILL of surviving group members and direct-child wait,
including when descendants outlive the leader. Repeated stop is harmless; a failed
intent write still cancels/cleans up and returns failure without claiming changed
intent. Targets are retired before further use even when persistence fails.
A final observation persistence failure leaves durable intent intact
without claiming observed stopped. `Owner.Shutdown` closes launch admission, cancels all generations
and joins workers, conditionally observing stopped without changing desired intent.
Cancelling the initiating start context retires its generation; a coalesced caller
can cancel its own wait independently. Callable health supervision adds no startup
scan, subscriptions or automatic retries (#3207). Daemon wiring (#3204),
publication/migration/activation/rollback (#3125) and encrypted resource capability
activation (#3123) remain downstream; exclusive cutover is publisher-owned. See
[startup and health](../../hosted-apps.md#startup-and-health) for downstream boundaries.

`Open` validates storage before `normalizeSnapshot`. Every available record with
an active release becomes starting; every unpublished or intentionally stopped
record becomes stopped, regardless of stored state. All other fields and
tombstones are preserved. Only changed states receive revisions, in stable app-ID
order. The total revision budget is checked before one snapshot write; exhaustion
or persistence failure returns no registry and preserves the committed bytes.
An unchanged reopen neither writes nor advances revisions, even at the ceiling.
Opening exposes this committed normalized baseline, not earlier-process events.

## Concurrency and persistence

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

## Testing

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
objects. See [SQL validation](#template-sql-and-readiness).

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
