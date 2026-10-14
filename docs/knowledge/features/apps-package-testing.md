# `internal/apps` — testing

See the [package overview](apps-package.md) for the topic map.

## Testing

### Checked candidate fixtures

Checking descendants only after preparation returns cannot prove artifact
validation waited for cleanup. `TestCandidateDescendants` makes the build
descendant create required output only in its termination handler, so premature
validation fails. `TestCandidateCancellationDescendants` cancels with both leader
and descendant live and checks no later phase. `TestCandidateSourceExecutable`
checks the copied owner-executable bit; ordinary nonexecutable fixtures missed
the 0600-copy regression. `TestCandidateBuildMutation` changes module metadata
during compilation, proving input validation must run again before retention.

A controlled child that never loads npm configuration or rewrites lifecycle
PATH misses real npm failures. `TestCandidateNPMEnvironment` models pathname-keyed
config loading; `TestCandidateNPMPinning` models stage-to-root binary-path
insertion with an alternate Node and env-node probe, including spaces/quotes in
paths. `TestCandidateRealNPM` exercises actual npm with an offline local tarball,
disabled audit, root/dependency install hooks and all four commands. Without the
lifecycle shell it picks the fixture's Node 20 even when the incoming PATH starts
with selected Node. This environment regression can run under an unsupported
real Node and does not establish a complete supported-Node candidate run;
`TestCandidateValidation` separately covers runtime admission with controlled
Node versions. See [input/runtime boundaries](apps-package-validation-and-stored-identity.md#checked-preparation-inputs).

Temporary-data cleanup alone cannot prove safe placement: staging cleanup removed
the old TMPDIR even while the template rejected its service data there.
`TestCandidateTemporaryRejectsAppTrees` rejects lexical and symlink-resolved app
trees; `TestCandidateTemporaryCleanup` checks private storage during commands and
removal after success, failure and cancellation. `TestCandidateSupportedTemplate`
creates a fresh maintained app through `Registry.Create` and runs the actual
`PrepareCandidate` environment with supported Node and its npm CLI, selected with
`HOSTED_APP_NODE` and `HOSTED_APP_NPM_CLI`. It requires nonzero maintained test
counts, all passed with zero failures/skips, before build and recording, then
checks `VerifyBuild`, matching history after reopen and unchanged source/persistent
data. An ordinary-shell template run or a skipped supported-Node arm misses this
environment boundary; record executed/pass/fail/skip counts from the acceptance run.

`TestCandidateDamagedRetry` reseals partial/changed builds so permission failure
cannot mask fresh integrity checks, then compares retained snapshots and registry
bytes to prove refusal without repair. `TestCandidatePreservesCommittedFields`
seeds nondefault display/lifecycle/journal values; default-state fixtures could
miss accidental clearing. `TestCandidateExistingService` checks actual HTTP
availability, unchanged process generation and saved-data bytes after failure,
rather than inferring service survival from an unchanged active-release string.

### Publication fixtures

A shared tiny migration/startup budget makes a finalization test fail before its
injected write while appearing to cover finalization. Healthy `publishFixture`
arms retain production migration/startup/request budgets; shorten only the
specific expiry arm and restore the budget before retry. The finalization arm of
`TestPublishFailures` delays both compiled-entry launches and requires the
post-activation injection plus exact phase/recovery feedback before inspecting the
committed on-disk journal. A generic publication error is insufficient evidence.

`TestPublishOrdering` inspects the persisted decision and absence of data before
the journal. `TestPublishJournalDirectorySyncOrdering` parks directory sync before
data preparation. `TestSnapshotDurabilityBoundary` executes real file
sync/rename/directory ordering with injected failures;
`TestPublishDurabilityUncertain` inspects retained decisions, unavailable targets
and unchanged data rather than treating a renamed file as durable activation.
`TestPublishPreparationRefusal` corrupts recorded/sealed evidence only after
preparation/snapshot, so it proves fresh verification rather than cached success.

`TestPublishMigrationRetirementUncertain` lets the leader exit and injects EPERM
with a real descendant still live; it must retain the uncommitted journal and
refuse restoration/removal. `TestPublishServiceRetirementUncertain` covers
preactivation Stop/Remove/Shutdown. `TestGenerationRetirementErrors` covers
ordinary/published completion, replacement, repeated Shutdown and rejected then
successful removal. Assertions inspect actual leader/descendant retirement,
retired targets and persisted decisions; an active-release string or leader wait
alone would miss unsafe restoration and lost cleanup failures.

`TestPublishTemplate` requires actual supported Node/npm through `HOSTED_APP_NODE`
and `HOSTED_APP_NPM_CLI`, compiles the maintained template via checked preparation,
runs migration and matching live health, checks durable activation/cleared journal
and requests migrated preferences through the committed `Target.Do`. A skipped
arm supplies no publication evidence. The
[final verifier review](https://github.com/pyrycode/pyrycode/pull/3366#issuecomment-6176315679)
inspected Node v24.15.0 builder results: 901 named apps checks executed/passed,
0 failed/skipped, publication family 64/64 and this template arm 1/1. Its independent
retirement regression run executed/passed 17 checks with 0 failures/skips. These
counts support the specified arms; host power-loss and startup recovery were not
exercised. Make retained builds writable only for fixture cleanup after owner
shutdown; changing permissions earlier weakens the immutable-build proof.

An unconditional template failure injection runs during the candidate's own
service tests and rejects preparation, proving no publication rollback.
`TestUpdateTemplateRollback` scopes migration/startup effects to the real
publication data directory and keeps baseline migrations inside template unit
fixtures while checked publication still builds and executes the new candidate
SQL. External SQL evidence distinguishes transactional failure after an earlier
pending statement from successful schema migration followed by startup failure.
Compare the complete restored schema/preferences/version-checksum ledger, remove
candidate-only SQL and non-SQL data, and probe matching old-release health; a saved
preference or migration exit code alone misses incomplete restoration.

The [final update verifier review](https://github.com/pyrycode/pyrycode/pull/3381#issuecomment-6178367014)
inspected supported Node v24.15.0 builder evidence: 944 named apps checks executed,
944 passed, 0 failed, 0 skipped. Both `TestUpdateTemplateRollback/transaction` and
`/startup` executed and passed: 2 arms executed, 2 passed, 0 failed, 0 skipped.
It independently ran 12 retirement/storage/recovered-completion checks and the
three original verifier regressions: all passed, none failed/skipped. No live
Claude was required; startup recovery and physical crash/power-loss remain
unproven by these runs. Skipped real Node/SQL arms supply no rollback evidence.

`TestUpdatePredecessorRetirementUncertain` must cover ordinary and published
predecessors before any verified-backup journal exists. Inject denied group
retirement with a joined ordinary leader and a still-live descendant; inspect
durable `retirement_pending` before signalling and barred Start/Publish,
reconciliation, targets and removal after reopening. Candidate-migrator-only
uncertainty coverage would leave this earlier writer gap untested.
`TestUpdateExclusiveRetirement` parks both headers and body reads and inspects
their cancellation and old-group completion at snapshot time, rather than only
after Publish returns. `TestUpdateCancellationRecovery` includes stopped →
available intent reuse; final available intent alone would hide obsolete recovery.

A same-owner rollback does not prove restart recovery. Startup fixtures must
join the old owner, transfer exclusive filesystem ownership and exercise real
`Open`/`NewOwner` with retained evidence. `TestStartupRecoveryRetry` interrupts
partial replacement, restored-before-finalization and final persistence twice
before successful reopen; clearing the journal after restoration alone would lose
that retry path. `TestStartupRecoveryInvalidation` checks absent API/asset targets
while parked, a usable sibling and stale completion after stop/intent reuse,
metadata changes, removal and shutdown. Constructor-time backup hooks must be
installed before workers start; mutating hooks after `NewOwner` races admission.

`TestStartupTemplateRecovery` compares complete restored schema/preferences/ledger
and removes candidate-only SQL/files before previous-release health; its previous
service guard rejects either leftover before health can pass. It also checks stopped
validation without a service. An invalid-preferences snapshot cannot simply insert
values violating the maintained schema's CHECK constraints: SQLite integrity rejects
that fixture before release validation runs. Use a permissive schema with invalid
saved data so snapshot verification succeeds and restored-release validation must
reject it before journal completion. SQLite integrity alone proves neither schema
nor saved-preference/complete migration-checksum compatibility. Supported Node/npm
arms must execute and pass; skips supply no restart evidence. See the
[startup recovery matrix](apps-package-lifecycle-and-restart-state.md#startup-publication-recovery).

### Retained-build fixtures

`TestRetainedCanonicalInventory` and `TestRetainedRealInventory` author expected
inventory bytes independently of the production serializer and any JSON encoder.
A normal JSON round-trip cannot prove fixed key order, byte sorting, lowercase
C0 escaping or literal non-ASCII/HTML-sensitive characters and Unicode separators.
`TestRetainedVerification` changes a valid link from `pkg` to `./pkg`: equivalent
resolution must still fail because identity includes exact target spelling.
It reseals changed fixtures before verification so writable-mode rejection cannot
mask byte, inventory or digest checks, and compares rejected destinations before
and after verification/reuse to prove no repair occurred.

`TestRetainedLongLinks` distinguishes finite chains from cycles;
`TestRetainedRepeatedExpansion` exercises repeated acyclic expansion through
validation, installation and verification. Ordinary short-link fixtures stayed
green while recursive expansion ignored deadlines. `TestRetainedResolutionCancellation`
forces cancellation during traversal and after a cached resolution, then checks
safe retry. `TestRetainedPinnedAccess` replaces the staging pathname after its
descriptor is acquired: verification must read the pinned original bytes and
fresh public validation must reject the replacement symlink. These local fixtures
require neither live Claude nor dependency-network access. See
[artifact boundaries](apps-package-validation-and-stored-identity.md#retained-artifact-boundaries).

### Health-failure observations

Polling exact transient failure counts against a freely advancing timer can
permanently miss a count after the third failure retires the generation.
`TestOwnerHealthFailures` releases each runtime probe only after observing the
preceding completed result; `TestOwnerHealthFailuresDelayedObserver` pauses beyond
three ordinary intervals and proves one actual probe per release. Both retain
the 1/2/0/1/2/3 reset/retirement sequence and target availability checks. The optional
pre-start `Owner.healthTicks` clock controls only runtime scheduling; production
and startup retain real timers, and probe transport/deadlines remain real.

### Reconciliation cancellation

A child-ready marker proves neither probe entry nor cancellation of a blocked
body read. Merely widening an elapsed assertion can let natural request/startup
expiry satisfy broken cancellation. `TestOwnerReconcileCancellation` enables
`healthReply.HoldHealth`: `TestHealthHelperProcess` flushes headers and publishes
`health-blocked` before withholding a valid health body until request cancellation
or `health-release`. Startup stop/removal first require that marker and a live,
unhealthy generation, then prove generation retirement and child exit while the
release marker remains absent and natural expiry is still in the future.

The startup arms use one-minute test-only startup/request and helper-write escape
deadlines, with five-second marker/completion waits. The
[controlled experiments](https://github.com/pyrycode/pyrycode/pull/3380)
showed that disabling request cancellation in `Owner.supervise` fails both startup
completion proofs while the body is withheld; restoring cancellation with a
two-second event-dispatch delay passes in about 2.14 seconds per arm. The finite
observation allowance accommodates dispatch and cleanup, including the fixture's
100 ms termination grace. It defines no production latency policy and changes
no production health, retry or stop budget.

Generation completion alone does not establish pending backoff if retry time
keeps advancing. Freeze `Owner.now` under its lock before launch; after failed
retirement and child exit, require an unsuppressed retry due strictly after that
time, failed registry state and unchanged generation after reconciliation.
Both startup and backoff stop/removal arms capture the pre-event snapshot, then
advance retry time beyond eligibility and replay that stale snapshot through
`Owner.reconcileRecords` followed by current-state reconciliation. Retaining the
same completed generation and checking its child is gone proves no resurrection.

Dispatch stop/removal asynchronously so a blocking event cannot prevent the
bounded completion assertion from running. Join event work and inspect its
error. Register release/cancel/join cleanup after the owner fixture cleanup so
reverse `t.Cleanup` order releases blocked work before owner shutdown, even after
an assertion failure. See
[cancellation fixture cleanup](development-verification.md#prove-that-tests-distinguish-the-change).

Separate fixture/phase setup, event dispatch, persistence, cancellation observation
and SIGTERM-to-retirement observation when diagnosing timing. Observer scheduling
is included in those durations; backoff's context is already cancelled before
the event, so observing it measures no new cancellation latency. The historical
1.78-second startup failure remains unattributed: its host log was unavailable
and ten baseline focused race repetitions passed. The phase and measurement gaps
justify this proof repair without establishing a historical scheduler cause or
queue-handle regression.

### Other package fixtures

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

JSON service fixtures cannot prove rejection of malformed UTF-8 header strings:
encoding replaces invalid bytes, silently turning the negative case into valid
metadata. `TestResponseMetadata` injects invalid UTF-8 directly into preparation;
`TestAPIResponses` uses a preserved Unicode control value for the backend rejection
case. Keep those proofs separate. `TestAPIForwarding` compares exact captured
RequestURI/body bytes, including mixed-case escapes and duplicate/empty query
values, rather than comparing parsed URLs that conceal normalization.

An empty backend response has no body read to notice a stale generation.
`TestAPIEmptyReplacement` stops and restarts the same release between backend
response and preparation for GET/204 and HEAD, proving the retained target rejects
rebinding without reservation leaks or replay. `TestAPICancellation` stalls headers
and bodies under caller cancellation/deadlines and counts mutation requests;
`TestAPIRetirement` checks both stalled staging and retained partially consumed
responses. These are backend proofs; they do not establish encrypted routing.
See [resource ownership](apps-package-resource-serving.md#private-api-forwarding).

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
