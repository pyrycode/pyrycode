# `internal/thread` — reusable fold regression corpus

Part of the [thread package](thread-package.md).

## Reusable fold regression corpus

`testFoldCorpus` and `testFoldGoldenCheck` are same-package `internal/thread`
test helpers for the [agent](https://github.com/pyrycode/pyrycode/issues/3351),
[visibility](https://github.com/pyrycode/pyrycode/issues/3352) and
[text](https://github.com/pyrycode/pyrycode/issues/3353) optimizations, and the
[publication](https://github.com/pyrycode/pyrycode/issues/3328) and
[rendering](https://github.com/pyrycode/pyrycode/issues/3333) work that consumes
their evidence. `fold-scenarios-v1/100-entries` repeats fixed 100-entry scenarios;
requesting a larger count preserves the smaller prefix and adds scenarios with
distinct turn/call/task identities and bounded payloads. Each scenario has its
own producer lifetime, with multiple calls/tasks inside its agent group. Sharing
one lifetime across repetitions made cross-scenario group recomputation dominate
the gate; separate lifetimes keep the corpus practical while retaining group
joins and long-history visibility work.

The scenarios retain genuine Agent/Task child work, late links and parent
enrichment, nested closure/reopening, mapped/durable companions, queued-send
delivery/suppression and escaped/Unicode deltas. Bash/local_bash remain ordinary
tools and cannot parent children. `TestFoldCorpus` independently checks lifecycle
boundaries, prefix stability and bounded payloads; digest equality supplements
these assertions.

`TestFoldBaselines` pins generated checkpoints at versions 6, 10, 11, 12, 14,
15, 18, 21, 22, 23, 28, 33, 100, 1,000 and 30,000 in
[`corpus_golden.json`](../../../internal/thread/testdata/corpus_golden.json).
Early checkpoints surround child appearance, closure/reopening, links, endings
and send delivery/suppression; the sparse long-history checkpoints avoid testing
every split point as `testMainReplay` does for small fixtures. It also consumes
the unchanged raw shadow history through checkpoints in
[`shadow_golden.json`](../../../internal/thread/testdata/shadow_golden.json).
`cmd/pyry.TestThreadRecordedBaselines` derives the three recordings through the
same `testRecordedFrames` helper as the independent recorded replay tests and
retains every emitted version. All checkpoints use fresh whole-prefix feeds,
deterministic chunks cycling through 7/31/113 entries and single-entry feeds.
These default-build offline gates require no credentials or recapture; preserve
the separate [provenance and raw/legacy semantic assertions](thread-package-shadow-lifecycle-and-evidence-coordination.md#idle-retirement-query-demand-and-evidence). See
[baseline writing and digest conventions](thread-package-agents-and-background-work.md#agent-validation-and-testing).
Canonical item hashing streams individually encoded rows in sorted field order.
Only their Content requires generic JSON normalization, avoiding a second
complete metadata object tree. `TestFoldItemsDigestCanonical` compares this
encoding with the independent generic encoder for every Item field, nested
content, nil and empty slices. Existing golden bytes and the separate exact
Content-byte hash remain unchanged.

`TestThreadRecordedBaselines` additionally proves checkpoint resume equivalence
for all three committed histories: `parent_tool_use_v2.1.259.json`,
`task_notification_v2.1.259.json` and `roster_after_finish_v2.1.280.json`.
`testRecordedCheckpointResume` covers every emitted entry cut from zero through
full, including hidden facts and companion emissions within frames, then compares
immediately and after every suffix entry with uninterrupted reference folding.
Both export parts cross JSON separately; import uses the same owner and a
prefix-limited history source, makes no history lookups and receives only suffix
feeds. Direct comparisons retain exact Content bytes, every Item field/revision,
the complete internal metadata/recipe inventory and exported continuation state.
Rendered equality alone misses lost ever-published membership; JSON-normalized
digests can conceal Content byte differences. See the
[comparison contract and negative controls](thread-package-main-thread-folding-validation-checkpoints.md#checkpoint-validation-and-resume-proof).
The retained synthetic shell creation, launch-result and turn-end context is
explicitly test-supplied. This offline gate uses the committed frames without
credentials or recapture and adds no live-capture evidence. Existing recorded
semantic assertions and golden expectations remain unchanged. The
[persisted reader and durable transaction](thread-package-main-thread-folding-validation-checkpoints.md#durable-transaction-round-trip-and-fault-proof)
have separate round-trip and interruption/retry proofs, supplemented by
[worker cadence and joined certification](thread-package-cache-and-epochs.md#worker-checkpoint-cadence-and-retained-changes).
Store startup restoration remains #3337; loading still fully replays history.
