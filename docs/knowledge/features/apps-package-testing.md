# `internal/apps` — testing

See the [package overview](apps-package.md) for the topic map.

## Testing

### Initialized SQLite subprocess proof

Absent-data or uninitialized publications bypass SQLite snapshot subprocesses;
their success cannot establish portable initialized-data updates. On macOS
arm64, execute `TestCommittedTemplateUpdateSnapshot` without a platform skip,
`TestBackupSQLiteWAL` and `TestUpdateTemplateSnapshot` using real supported Node
and npm selected through `HOSTED_APP_NODE` and `HOSTED_APP_NPM_CLI`. The retained
assertion calls `SnapshotData` and fresh `VerifyBackup` on an initialized database.
The WAL arm proves committed WAL-only preferences and ledger preservation,
standalone DELETE mode without sidecars, and repeated unchanged verification
after removing source data. The maintained-template update must activate a
healthy candidate, retain saved preferences and its new migration, and freshly
verify a backup matching the complete pre-migration schema/preferences/ledger.
See the [snapshot fixtures](apps-package-concurrency-and-testing.md#testing).

`TestBackupNodeDirectoryIdentity` moves the selected directory to another parent
at launch, then replaces its old pathname with either a directory or symlink in
both snapshot and verify modes. It checks selected data, outside/replacement
sentinels and unchanged parent cwd. Use canonical `testRoot` paths: macOS `/var`
aliases otherwise fail no-follow selection before reaching the launch hook.
`TestBackupNodeFailures` and `TestBackupNodeBootstrapRejectsInvalidDirectory`
cover runtime/version/exec/SQL failures and missing/non-directory descriptors;
`TestBackupSQLiteCancellation` and `TestBackupSQLiteStorageErrors` retain joined
child, unchanged data and failed-snapshot evidence checks.

Record revision, OS/architecture, Node/npm versions, exact commands and
executed/pass/fail/skip counts for each required arm; skips, cross-compilation
and platform probes supply no execution proof. The
[native evidence](../../../internal/apps/testdata/3415-macos-results.json) and
[focused events](../../../internal/apps/testdata/3415-macos-focused.jsonl)
record macOS 26.6.2/arm64 with Node v24.15.0/npm 11.12.1: the focused family
executed/passed 23 checks, failed/skipped 0. The final package run executed 999,
passed 995, failed 0 and skipped 4 filename arms because the filesystem rejected
invalid UTF-8. Every required SQL/update arm passed without skips. Exact commands
and earlier Linux proof are in the
[repair spec](../../specs/architecture/3415-confined-sqlite-cwd.md#validation-evidence).
No live Claude is needed for this proof.

### Checked candidate fixtures

Checking descendants only after preparation returns cannot prove artifact
validation waited for cleanup. `TestCandidateDescendants` makes the build
descendant create required output only in its termination handler, so premature
validation fails. `TestCandidateCancellationDescendants` cancels with both leader
and descendant live and checks no later phase. `TestCandidateSourceExecutable`
checks the copied owner-executable bit; ordinary nonexecutable fixtures missed
the 0600-copy regression. `TestCandidateBuildMutation` changes module metadata
during compilation, proving input validation must run again before retention.

A successful npm build wait can still produce public build-failure feedback:
the native diagnostic baseline captured `TestPublishTemplate`, snapshot and both
rollback arms returning inspection EPERM after TERM, with successful build waits.
Immediate rejection of that uncertain probe caused these preparation failures.
EPERM proves neither live membership nor retirement. `candidateCleanupWithProbe`
reinspects within the existing TERM/KILL windows and accepts only subsequent
positive absence. Denied signals, persistent uncertainty and still-live groups
remain errors; joining leaders, retiring descendants before artifact validation,
immutable-build checks and publication recovery barriers remain required.

Keep command and cleanup outcomes separately inspectable. The private per-context
`candidateDiagnostic` observer records a fixed command label, PID/exit code and
separate wait/start, cleanup and cancellation outcomes; it collects no arbitrary
arguments, environment values or child output. Public `CandidateFailure`,
`PublishFailure` and lifecycle feedback retain static messages and do not unwrap
diagnostics. `TestCandidateCommandDiagnostics` distinguishes command, cleanup and
combined failures; `TestCandidateDiagnosticPublicBoundary` checks private evidence
and safe public feedback, including the child-output secret sentinel.
`TestCandidateDiagnosticBeforeStart` distinguishes launch failure from cancellation.

`TestCandidateBuildProbeRecovery` injects inspection EPERM after a successful
controlled build, then requires real group absence before artifact validation.
`TestCandidateCleanupProbeRecovery` covers recovery before/after forced termination,
persistent uncertainty, denied TERM/KILL and live groups. The retained pre-repair
run executed eight leaves: four failed (build plus three recovery sequences),
four safety arms passed, none skipped. After repair the focused regression family
passed 22/22 leaves and final diagnostic-boundary checks passed 3/3, without skips.

The [counted macOS evidence](../../../internal/apps/testdata/3447-macos-results.json)
retains all six attempts, including failed runs and the initial isolated diagnostic
non-reproduction. The diagnostic overlay demonstrably executed: its matching PID,
selected test argv, wait and TERM-probe records identify the failed builds. It
preserved child stdout/stderr handling and captured no child output; parent logging
changes scheduling. Neither that initial isolated pass nor reinspection establishes
kernel membership/state at the instant of EPERM. Earlier historical failures from
\#3418 lack effective command traces and cannot all be attributed retrospectively.

The clean `e3623103` complete uncached `go test -race -json ./internal/apps -count=1`
ran on macOS 26.6.2/arm64, Go 1.26.2 and real Node 24.15.0/npm 11.12.1 selected via
`HOSTED_APP_NODE`/`HOSTED_APP_NPM_CLI`, at normal concurrency. Snapshot and both
rollback leaves executed/passed 3/3 without skips, proving initialized activation,
retained preferences, verified pre-migration backup and the intended transaction/
startup failures followed by full schema/preferences/checksum-ledger restoration
and old-release health. That package gate **failed**: parents 105 executed,
104 passed, 1 failed, 0 skipped; leaves 912 executed, 906 passed, 2 failed,
4 skipped because the filesystem rejects invalid UTF-8 names. Remaining failures
are `TestPublishRetirement/health/remove` (denied group signal) and
`TestPublishRetirement/health/intent_cycle` (retained retirement metadata), tracked
by [#3418](https://github.com/pyrycode/pyrycode/issues/3418). The intent-cycle record
alone does not identify its precise signal/probe cause. These target passes do
not certify a green full-package gate; the later combined native acceptance is
recorded under [publication fixtures](#publication-fixtures). No live Claude is needed.

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

`publishFixture` shortens owner termination grace to 30 ms; increasing it or
serializing tests would not prove post-SIGKILL retirement. With the real forced
descendant from `TestHealthHelperProcess`, `TestPublishRetirementProbeSequence`
requires a live descendant before Stop, records native group/individual PID
probes, and controls EPERM until forced kill, across the post-kill probes or
persistently. It requires the injected post-kill window to execute, joins Publish
and checks both processes gone. Persistent uncertainty still requires
`app.io_failed`, retained journal/`RetirementPending`, unavailable targets and
rejected retry/removal, even when the real child has exited.
`TestOwnerCleanupProbeRecovery` separately retains denied-termination and
persistent-probe assertions. `TestOwnerCleanupErrorContext` keeps its inspection
injection active after real ESRCH; ending it there would exercise recoverable
uncertainty rather than the intended persistent error/context contract.

The [original evidence](../../../internal/apps/testdata/3418-macos-results.json)
retains the failing unchanged baseline, failing pre-repair controlled regressions,
earlier batches and failed package/diagnostic attempts. Its
[native pre-repair observations](../../../internal/apps/testdata/3418-controlled-before.jsonl.gz)
show `live → EPERM → ESRCH` after SIGKILL; the
[next controlled run](../../../internal/apps/testdata/3418-controlled-after.jsonl.gz)
also observed leader ESRCH while the individual descendant probe still succeeded
during group EPERM. A later empty `ps` sample cannot identify the earlier atomic
kernel state. These observations establish a transient inspection window and
the regression distinguishes the missing post-kill proof; they do not attribute
every historical failure to exited-group bookkeeping. A passing isolated arm or
diagnostic overlay supplies no complete-package stability proof.

The [resumed evidence](../../../internal/apps/testdata/3418-macos-resumed-results.json)
records exact commands/environment, revisions, checksums, disjoint parent/leaf
counts and every resumed attempt after the separate candidate cleanup repair
in [#3447](https://github.com/pyrycode/pyrycode/issues/3447). On clean `72a5d169`,
macOS 26.6.2/arm64, Go 1.26.2 and real Node 24.15.0/npm 11.12.1 selected through
`HOSTED_APP_NODE`/`HOSTED_APP_NPM_CLI`, normal concurrency was retained without
`-p`/`-parallel` overrides. Two consecutive
`go test -race -json ./internal/apps -run '^TestPublishRetirement$' -count=5`
batches ([first](../../../internal/apps/testdata/3418-batch-5.jsonl.gz),
[second](../../../internal/apps/testdata/3418-batch-6.jsonl.gz)) each executed/passed
90 leaves and 5 parents, with zero failures/skips: all 18 build/migration/health ×
stop/remove/shutdown/cancel/intent/intent-cycle arms ran five times.
The uncached `go test -race -json ./internal/apps -count=1`
[complete package run](../../../internal/apps/testdata/3418-package-resumed.jsonl.gz)
executed 917 leaves: 913 passed, 0 failed, 4 skipped; all 106 parents passed.
Every retirement and supported real-tool publication/update arm passed without
skips. The four skipped leaves were `TestBackupAliasedTempRoot/states/nested`,
`TestBackupStates/nested`, `TestRestoreStates/nested` and
`TestRetainedValidation/invalid_UTF8`: the filesystem rejected invalid-UTF8 names
with EILSEQ. Three preceding sandbox/cache setup attempts executed zero tests
and remain failures in the resumed evidence. Earlier failed package gates remain
failures in the original evidence; the later complete combined run establishes
the package pass. No live Claude is required.

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
`TestRetainedRepeatedExpansion` keeps a parallel 20-level repeated acyclic
symlink-expansion fixture on disk. After `artifactScan.walk` reads the fixture,
it resolves `node_modules/use` first with a cold cache, then every symlink under
one 512-checkpoint `testResolutionContext` budget. Visiting intermediate links
first can warm the cache and conceal exponential cold-resolution work. The proof
also requires the consumer to resolve to `node_modules/pkg/index.js`. Memoized
resolution used 133 checkpoints in the retained experiment; disabling completed
reuse in `artifactScan.resolveParts` cancels at 512 before integration begins.

The test separately times the filesystem walk, in-memory resolution,
`ValidateArtifacts`, `InstallBuild` and independent `VerifyBuild`. One 30-second
overall context bounds these phases, while the checkpoint budget bounds graph
work independently of I/O latency and scheduling. Validation, installation and
verification must return equal build IDs; cleanup makes the sealed build writable
before removal. Installation's copies, syncs and three scans make a short elapsed
deadline unsuitable as the complexity assertion. See the
[measurement and mutation guidance](development-verification-prove-that-tests-distinguish-the-change.md).

Ordinary short-link fixtures stayed green while recursive expansion ignored
deadlines. `TestRetainedResolutionCancellation` still forces cancellation during
traversal, retries safely on the same scan, and rejects an already-canceled
context even with a cached resolution. Long-chain/cycle and containment rejection
fixtures remain separate assertions. `TestRetainedPinnedAccess` replaces the
staging pathname after its descriptor is acquired: verification must read the
pinned original bytes and fresh public validation must reject the replacement
symlink. These local fixtures
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
[cancellation fixture cleanup](development-verification-prove-that-tests-distinguish-the-change.md#prove-that-tests-distinguish-the-change).

Separate fixture/phase setup, event dispatch, persistence, cancellation observation
and SIGTERM-to-retirement observation when diagnosing timing. Observer scheduling
is included in those durations; backoff's context is already cancelled before
the event, so observing it measures no new cancellation latency. The historical
1.78-second startup failure remains unattributed: its host log was unavailable
and ten baseline focused race repetitions passed. The phase and measurement gaps
justify this proof repair without establishing a historical scheduler cause or
queue-handle regression.

### Daemon composition fixtures

Parallel test-process forks can inherit a writable executable descriptor even
after its creator closes it, making another sibling's launch fail with Linux
`ETXTBSY`. `testWriteDaemonAppsRuntime` writes and closes the shell executable in
a joined child, keeping writable descriptors out of sibling forks. Use it for
initial fixtures and post-attestation replacements in both
`TestDaemonAppsRuntimeReadiness` and `TestDaemonAppsComposition`: cached readiness
would otherwise preserve a transient fixture launch failure as unsupported hosting.
Preserve all supported/degraded version arms and expose the underlying probe error.
Cancellation assertions must require the context
deadline to expire as well as bounded return; an immediate launch error otherwise
passes without exercising cancellation. See the
[runtime readiness contract](apps-package-lifecycle-and-restart-state.md#lifecycle-and-restart-state).

Installing the actual owner also introduces asynchronous committed lifecycle
observations. `TestDaemonAppsComposition` compares discovery/tombstone revisions
with committed registry snapshots and drains intervening updates; assuming
registration/removal consume exactly two revisions would reject valid owner
observations or correlate the wrong notification.

### Paired resource fixtures

A late-response test can stay green with broken request-ID fencing if it releases
the old response after the replacement read finishes. `TestLiveRelay_HostedResources`
holds the replacement at its own backend barrier, releases the retired response,
and asserts no output before allowing the replacement to return. Count the uncertain
write relative to the preceding successful mutation: two total invocations here
mean the uncertain write added exactly one. See
[resource retirement](apps-package-resource-serving.md#resource-serving).

Worker exit after the five-second resource inactivity deadline could prove timeout
cleanup rather than disconnect retirement. The same test observes
`v2.hosted.resource_exited` within three seconds after closure. Keep that observation
bound shorter than inactivity expiry and retain service PID/data/identity checks;
connection cleanup must not terminate the daemon-owned service.

Typed unsigned decoding alone accepts absent/null wire fields as zero. Raw-field
presence and numeric-type assertions are needed to distinguish required zero
`size_bytes`, `total_chunks` and first-chunk `index` from missing fields. The
[#3441 verifier](https://github.com/pyrycode/pyrycode/pull/3445#issuecomment-6189748734)
identified this remaining assertion gap in `hostedWire.response`; its passing
204/first-chunk checks establish decoded values, not strict raw-field presence.

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

Sharing one live target across response-policy arms lets readiness loss mask
later rejection assertions with `ErrUnavailable`. `apiResponseOwner` installs
the manual `Owner.healthTicks` channel before startup and still waits for real
startup health confirmation. `TestAPIResponses` supplies no runtime ticks;
`apiOwner` and shared health defaults retain their existing behavior. The response
proof keeps 1048576-byte exact success, 1048577-byte `ErrResponseLimit`,
zero-capacity `ErrResponseCapacity`, all status/body/header assertions, exactly
one forwarded request per arm and zero budget after cleanup, including repeated
close. Unexpected errors report elapsed time, lookup/lifecycle state, request
count and budget. See [runtime probe control](#health-failure-observations).

`TestAPIResponseFailureClasses` exercises the private HTTP backend with a fresh
response fixture per scenario. Overflow reserves and reads before
`ErrResponseLimit`; zero capacity attempts reservation without a positive read
before `ErrResponseCapacity`. Closing before headers returns `ErrResponseIO`
without reservation or positive read; truncating a body returns the same error
after both. Each requires a valid target, nil result, one request and zero
retained budget. Keep the header-loss setup at valid status 200: status zero
made an early control pass through an invalid-status helper failure, without
proving the intended disconnect. Reservation/read observations distinguish the
failure phases even when their public error is identical.

The invalidation arm withholds headers, waits for the backend request, then
delivers three failed health probes, observing each completed failure. It requires
`ErrUnavailable`, an invalid target and zero retained budget; a subsequent
zero-capacity request must return unavailable without another backend request.
The forwarding worker has a four-second caller deadline and is joined before
cleanup. Probe and helper I/O deadlines remain finite and unchanged. This
reproduces an invalidation error pattern, not the initiating cause of a historical
failure; physical/symlink reruns cannot establish that cause. The
[diagnosis and two-root evidence](../../specs/architecture/3399-api-response-verification.md#revisions)
retain exact errors, timings, commands and assertion coverage; the
[aggregate outcome](development-verification-test-execution-and-artifact-survival.md#diagnosing-api-response-gate-failures)
has a separate verification boundary.

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
macOS may synthesize `__CF_USER_TEXT_ENCODING` even with an explicitly empty
launch environment. Seed a hostile parent value and reject its inheritance;
allow a different synthesized value only on Darwin before checking the remaining
exact environment. A raw key count would reject a sanitized real launch.
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
missed it. See [development verification](development-verification-protocol-boundaries.md#protocol-boundaries)
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
