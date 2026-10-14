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
