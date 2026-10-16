# `internal/thread` — Store verification

Part of the [background store](thread-package-background-store.md) overview.

## Store verification

`TestReferenceSourceOrder` checks feed/recent/Lookup precedence, deduplicated
miss-only requests and the all-hit no-I/O path. `TestReferenceSourceBatch` uses
36,000 all-hit IDs with duplicate/unordered requests and stale recent data to
exercise the batch sweep and feed precedence. `TestReferenceSourceBound` checks
detached payload capacity and pointed metadata, eviction and oversized-entry
bypass. `TestReferenceMissingAtomic` requires no partial render for incomplete
or unreadable lookup results.

`TestStoreReferenceRetry`, `TestStoreReferenceWorkRetry`,
`TestStoreReferenceSendRetry` and `TestStoreReferenceEndingRetry` share
`testStoreReferenceRetry` for missing/unreadable dependencies and repaired
replay. Its 256 real ignored appends evict the recent FIFO without depending on
`history.MaxPageEntries`. A decorator of the real reader's `Walk` splits replay
callbacks into at most 256 entries, leaving the target outside the last borrowed
feed chunk too. Both tiers must miss before the injected authoritative lookup
can fail. Reducing padding alone leaves replay usable: the negative control
without chunk splitting stayed usable at version 257. The authoritative-target
failure counter prevents a cached hit from masquerading as failure coverage.
Production reader/cache limits and history durability remain unchanged.

Tail arms first load successfully, arm lookup failure and use `Status` to wait
for the exact usable padding version with zero failed authoritative reads. Only
then do they append the delivery join, deferred send terminal or ancestor closure
that revisits the evicted recipe. Ancestor closure also revisits a completed
ordinary child tool whose intrinsic result has not changed. Ignored history
alone reuses immutable slots and need not reread a broken source. Send outcome
references have replay-only failure coverage: their terminal tail publication
can resolve them from the current chunk.

Wait through `Status` for worker unavailability before reading `Snapshot`:
polling a rendered snapshot can withdraw the worker itself and conceal missing
event-driven coverage. Failure must expose only `StateUnavailable` and generic
`ErrUnavailable`, with nil items and zero version, last-shown watermark and epoch.
Join the failed worker before disarming lookup and calling `Retry`; repaired
output must equal fresh legacy replay, including complete items/revisions,
consumed version and last-shown watermark.

Keep padding append time separate from initial replay, padding catch-up,
event-triggered failure and repaired retry/equality waits. Large ignored tails
can make setup expensive even with few public items: `ForwardReader.Tail`
repeatedly decodes a growing active segment through `forwardSegment` while
holding the history mutex also used by `AppendWithMetadata`. In the contended
Linux race measurements for [the retry fixture investigation](../../specs/architecture/3471-reference-retry-fixture.md#revisions),
one original unreadable tail arm spent 13.504s appending 4,096 padding entries,
then only 60.420ms catching up and 75.260ms reaching failure after delivery.
Bounded values were 111.775ms, 5.434ms and 4.260ms respectively; CPU and mutex
profiles corroborated repeated decoding and append contention. The four-family
retry matrix fell from 111.939s to 3.714s. These overlapping Linux runs diagnose
the expensive fixture dependency; they do not reproduce the MacBook's ten-minute
timeout or establish its phase distribution. A blocked append stack identifies
an active operation, so use phase timings and profiles to distinguish setup
contention from worker waits and cumulative package cost.

`TestStoreSelectedRecipeClosure` and `TestStoreCapturedRecipes` check selected
dependencies and recipes surviving concurrent feeds. `TestStoreOutputFailure`
checks atomic failure/repair; `TestStoreOldRenderFailure` protects replacement
workers. `TestStoreOutputCancellationWhileWithdrawing` and
`TestStoreConcurrentRetryWithdrawal` establish lock/join contention; cancellation
during lookup alone cannot prove those boundaries.

`TestAffectedPublicationOracle` compares affected capture with whole-thread
`difference` after single-entry and chunked corpus feeds, including private
activity, late children, reparenting, delivery/suppression and no-op entries.
Each of the eight batch sizes owns a separate reference fold, source and capture.
An independent reference fold renders the complete expected snapshot once at
each entry boundary, shared by batch comparisons ending there. The generated
and late-child scenarios retain all 2270 and 27 batch comparisons respectively.
Expected metadata and exact content are compared directly without serializing
or copying the captured payloads. No capture result supplies expected rows.
`TestAffectedPublicationReplacement` checks clearing, null and mixed content
replacement/text append. `TestAffectedPublicationOwnership` retains old
publications, changes and query results across later feeds and concurrent reads;
it checks pointer reuse, borrowed content and mutation of detached results.
Retain internal pointers after text-buffer initialization to prove borrowed
Content ownership; detached `Fold.Items` snapshots can stay correct while
reusable buffers corrupt old publications. See the
[text-buffer ownership checks](thread-package-main-thread-folding-validation.md#validation-and-testing).
`BenchmarkPublicationPhases` isolates capture/comparison from replay, checkpoint
I/O, query preparation and output encoding; see the
[bounded comparison evidence](../../specs/architecture/3388-immutable-publication.md#performance-evidence).

`TestIncrementalQueryOracle` compares incremental indexes with fresh construction
after every generated and retained shadow entry, including late children,
delivery suppression and no-item progress. `TestThreadRecordedBaselines` reuses
its recorded-frame derivation and normalization for per-entry tail versus fresh
replay comparisons across all three histories. Compare full item states and
result metadata, disregarding only item order (align independently generated
Store epochs). `TestIncrementalTreeOwnership` retains lookup buckets and roots
through concurrent reads and later membership/order updates, checking AVL
ordering, balance and subtree sizes.

`BenchmarkStorePublication` measures state-changing additions and fixed appends
with 50,000 prepared public items, including affected capture, Changes
construction/encoding/retention and immutable index/scalar installation. Fixture
setup, initial replay, checkpoint serialization and caller output copies are
excluded. `TestPublicationPreparationGrowth` separately counts semantic visits
and pointer/index-position copies at 12,500 and 50,000 public items: low
allocation alone cannot rule out whole-thread preparation. See the
[complete-publication measurements](https://github.com/pyrycode/pyrycode/pull/3426)
for before/after commits, environment and measured allocation/work counts.

Restart fixtures must rebuild the thread store when reopening its history
provider. Changing only the daemon's history field leaves the old `thread.Store`
attached to the former provider, whose cached high-water mark can remain behind
new acceptances and stall recovery. `TestBoundQueueActionsRestart` shuts down
the old store and constructs `thread.NewStore` with the reopened history before
loading item 205. This proves queue-handle reuse against the new run's acceptance
rather than against stale provider state; see
[current-run queue binding](msgqueue-package-api.md#exported-surface).

Certificate proofs count actual replacements through `Store.replace`, synchronized
with publication and completion. `TestStoreCertificateWrites` checks one dirty
write per load, no tail/no-item/active-to-idle replacements and one clean write
per retirement; `TestStoreCertificateDirectorySync` holds dirty directory sync
before usability and tests failure/retry. `TestStoreCheckpointLifecycle` checks
unchanged retirement and preservation of unloaded progress through shutdown;
`TestStoreCheckpointInitialReadiness` holds the dirty replacement before
usability. `TestStoreCheckpointFailures` checks generic snapshot errors and
retained lifecycle persistence failures. `TestStoreCertificateIsolation` requires
healthy conversations to retain their epochs after another final write fails.
`TestStoreCertificateMigration` covers legacy cleanup ordering, unsafe leaves
and preservation of unrelated files.

**Rename failure alone cannot prove failed clean certification.** A successful
rename followed by failed directory sync can leave readable clean evidence.
`TestStoreCertificateFinalSyncFailure` reopens after the injected failure and
requires epoch rotation; checking only `ErrPersistence` would miss unsafe reuse.
`TestChannelDelivery_SessionDaemonWiring` checks `thread-certificate.json`, `Clean`
and consumed version 2 both after startup replay/idle retirement and after joined
shutdown. Package-local tests can stay green while a daemon wait still polls a
removed metadata leaf, so both integration assertions must follow the format.
`TestStoreCheckpointUncleanContinuation` compares reopen and subsequent tailing
with fresh replay across pending child/report cut points, including revisions,
consumed version and `LastShownVersion`; epoch rotation is asserted separately.

`TestCatchUpPublications` spans more than 64 separate publications after live
range eviction, including no-item progress, arbitrary saved versions and an
immediate H read. `TestCatchUpContracts` checks exact/above gap boundaries,
epochs, future versions, missing query evidence and zero-version window parity.
`TestCatchUpEvidence` checks late public appearance despite older revisions,
hidden/dropped membership, detached content and suppression across clean restart
with whole/single-entry replay. `TestCatchUpMissingHistory` distinguishes crossing
a missing ID from starting at or beyond it.

`TestCatchUpBoundedRecovery` constrains selected-reference reads and index visits
over 36,000 committed entries, measuring recovery/preparation separately.
It repeats queries after clean restart and compatible
unload/reload with a previously returned epoch/version and no retained live
ranges. Its reference includes repeated touches, old active work, shared/multi-level
parents and a late-publication child: revision-only selection would silently
miss that child. `TestCatchUpReadiness` and `TestCatchUpIsolation` check unavailable
recovery/retry and cancellation during actual index work, holding a captured
publication through replacement, retirement and shutdown while writers and
another conversation progress.

`TestQuerySelection` and `TestQueryLimits` constrain continuous ties, empty and
oldest bounds, hidden/dropped membership, shared/multi-level parents, active
unordered extras, detached content and exact/clamped/nonpositive limits.
`TestStoreQueriesUpdates` checks delivery order placement and row suppression,
active work settling and newly public children after each committed publication.
To prove late child resolution, repair attribution to an existing older parent:
a newly created newer parent cannot join under the
[parent identity rule](thread-package-agents-and-background-work.md#parent-repair).

`TestStoreQueriesBounded` uses 36,000 committed entries across initial load,
clean reopen and unload/reload. It holds the real reader at Tail, then checks
early/middle/newest pages and newest windows against full items, constraining
selected-reference reads and index visits without index rebuilding. Measure both
APIs and keep preparation counters/timing separate from requests.
Nested children near the beginning force `resolveChildren` to rebuild public
visibility for subsequent facts; putting active nested extras late in history,
but still outside the newest window, preserves query coverage without making
replay dominate the fixture. `TestStoreQueriesLifecycle` and
`TestStoreQueriesRetirementIsolation` force cancellation during index access and
hold captured results across publication/retirement while writers and another
conversation progress.

`TestFoldObservations`, `TestFoldObservationChildren` and
`TestObservationReplacement` independently apply additions, replacements and
suffixes and compare with full items, including clearing, private joins and
entry/chunk watermark equality. `TestStoreObservations` checks the commit before
wait-registration case, detached changes and progress with no item changes.
`TestStoreObservationRetention`, `TestStoreObservationRecovery` and
`TestStoreObservationLifecycle` constrain eviction/oversize outcomes, watermark
reconstruction, epoch reuse without old ranges, cancellation and retirement.
Retention counters alone would stay green while substring suffixes pinned full
messages. `TestStoreObservationRetainedSuffixMemory` checks actual live heap
growth for 64 single-byte appends to a 1 MiB message in a cancellable helper
process; process isolation keeps unrelated tests' allocations out of the proof.

`TestStoreReplayTailIsolation` retains 4,098 committed raw replay facts
(`history.MaxPageEntries + 2`) through real history readers, with four message
items at the beginning and across the chunk boundary; unsupported durable facts
fill the remaining entries. It pauses after the first successful chunk feed,
before the callback returns, proving a partially folded replay remains rebuilding
with no published items/version. Duplicate `Load` must preserve the worker's
identity. While A is paused, appends finish and B reaches usable publication.
The Tail handoff snapshot equals only the captured replay bound. Messages
committed during replay, handoff and later tail match independent full replay;
successful delivery consumes every raw ID exactly once in order, with at least
two replay chunks and no chunk exceeding `history.MaxPageEntries`. Mutating
returned item fields/content must leave subsequent snapshots unchanged.

Keep raw reader-bound coverage separate from growing-item folding cost.
`resolveChildren` and `recordShown` traverse item collections per fact; an
all-message fixture makes an isolation proof pay for thousands of growing rows
and repeated full comparisons. Original-fixture measurements found folding
dominated handoff work: 1.52s of 1.67s focused, and 2.24s of 2.37s under the
unchanged parallel thread/history suite. The historical five-second expiry was
not reproduced, so these measurements identify the dominant measured phase,
not the exact cause of that occurrence. Bounding public items preserves raw
delivery coverage without increasing the deadline or changing production
folding. See the [phase evidence and synchronization revisions](../../specs/architecture/3094-store-replay-tail-isolation.md#revisions).

The test's local channel waits retain five-second deadlines and name each
barrier. They distinguish worker completion from a deadline with the last
observed replay/checkpoint/publication phase, consumed count, chunks and snapshot
state/version. Verbose output separates release-to-handoff folding, walk,
checkpoint and publication timing; snapshot waits have named log context.
Append/load work returns errors and its appended entry through a buffered result
channel for the test goroutine to check. Failure cleanup releases both gates,
cancels the test-owned context and joins that operation before the earlier
`testThreadStore` cleanup joins store workers through `Store.Shutdown`.

Published Tail progress does not establish completion of test-owned delivery
bookkeeping: `Store.run` publishes before returning from the tail feed callback,
while the wrapper records successful IDs after that callback returns. A snapshot can already
match full replay while the wrapper's count is short. A mutex protects recorded
data but cannot await bookkeeping that has yet to acquire it. Close/replace a
notification under the same mutex as the successful-ID count; capture the count
and current notification atomically after final publication. If incomplete, use
the finite worker-aware `final tail delivery bookkeeping` barrier before exact
count/order assertions. The serial reader completes earlier callbacks before
publishing the final chunk, so the next notification covers remaining delivery;
the shared mutex prevents a missed wakeup.

`TestStoreContinuationReopen` compares replay cut points and tails with a fresh
fold, including hidden pending reports and unresolved parents.
`TestStoreLifecycle` gates cancellation to prove joins, retirement, absence of
late publication and replay of commits made while unloaded.

`TestStoreFailureRetry` must commit a new tail entry before injecting failure:
an empty tail cannot prove partial fold advancement or fresh replay.
`TestStoreReadSecurity` checks containment, corruption, generic errors and byte
preservation. It pauses tailing before moving history, isolating snapshot
reference-read failure from background I/O.
Run the offline checks with `go test -race ./internal/thread ./internal/history`.

`TestStoreCacheContinuation` compares every reopen cut point and subsequent tail
publication with fresh replay, checking epoch retention separately from item
equality. `TestStoreCacheEpochs` covers clean/unclean exits and incompatible,
missing, corrupt, partial and ahead certificates. `TestStoreCacheInterrupted`,
`TestStoreCacheShutdown` and `TestStoreCacheFinalCertificate` cover incomplete
recovery, independent final failure and unloaded certificates surviving another
conversation's unclean stop.
`TestStoreCacheSecurity` checks unsafe leaves, containment, interrupted atomic
replacement, history byte preservation and repair/retry.

**Keep the supplied history store warm in truncation tests.** Constructing a
fresh history store hides a stale append cursor: recovery can pass while daemon
reopen/retry remains unavailable. `TestStoreCacheWarmHistoryTruncation` keeps the
same store across clean reopen, unload/reload and retry at zero/nonzero surviving
versions, proving epoch rotation and continuation/tail equality. Keep the
`Fold.Version() == H` guard too: a successful walk can still be incomplete.
`TestStoreCacheZeroHistoryValidation` gates the tail so a later read failure
cannot mask a premature empty usable publication over unreadable history;
nonempty replay tests alone cannot catch that path.
