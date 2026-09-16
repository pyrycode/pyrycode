# 2480 — Close the nil-next store in TestModelVocabularyStore_SinkForForwardsEveryVariant

Test-only flake fix. One `Close` on a store the test already mints.

## Files read

- `cmd/pyry/model_vocabulary_store_test.go` → `TestModelVocabularyStore_SinkForForwardsEveryVariant`,
  `storePath`, `retainAndClose` — the failing test, the `t.TempDir` helper it calls twice,
  and the helper that documents `Close` as the writer's join point.
- `cmd/pyry/model_vocabulary_store.go` → `sinkFor`, `Retain`, `drain`, `Close`,
  `writeModelVocabularyFile` — the mechanism: `sinkFor` retains on a `ModelList`
  regardless of whether `next` is nil, `Retain` starts `drain`, `drain` calls
  `writeModelVocabularyFile`, which `MkdirAll`s the instance directory and creates a
  dotted scratch file inside it. `Close` is the only join point.

## Change

`TestModelVocabularyStore_SinkForForwardsEveryVariant` currently discards the second
store: it chains `newModelVocabularyStore(storePath(t))` straight into `sinkFor(nil)`
and keeps only the closure. Feeding that closure a `ModelList` runs `Retain`, which
starts the store's `drain` goroutine, and nothing ever joins it — so `drain` can
`MkdirAll` the `instance/` directory and drop its scratch file into the test's second
`t.TempDir()` after cleanup's `RemoveAll` has already walked that tree, producing the
reported `directory not empty`.

The fix binds the store to a variable and defers `Close` on it, matching what the same
test already does for its first store and what `retainAndClose` does for every store it
drives. A deferred `Close` runs while the test function is unwinding, which is before
the `t.Cleanup` that `t.TempDir` registered, so the writer is joined before `RemoveAll`
starts. The assertion itself is untouched: the closure is still built by `sinkFor(nil)`
and still called with the sentinel list, so the "must not panic" proof is unchanged.

Nothing else moves. Every other store in the file is either load-only — it never calls
`Retain`, so no `drain` goroutine exists to join — or already `Close`d: `retainAndClose`,
`TestModelVocabularyStore_ModelListIsADeepCopy`, `TestModelVocabularyStore_IdenticalRetentionSkipsTheWrite`,
`TestModelVocabularyStore_ConcurrentRetainAndRead` and
`TestStreamRunnerFactory_PersistsTheModelVocabulary` all close theirs. Production code is
correct as written — `runSupervisor` closes the daemon's store — so no production file is
touched.

## Testing strategy

No new test. The existing assertions in
`TestModelVocabularyStore_SinkForForwardsEveryVariant` are the coverage; the defect is
the test's own missing join, not a gap in what it proves.

The proof is the repeat run named in AC 2:
`go test -race -count=500 -timeout 600s -run TestModelVocabularyStore ./cmd/pyry`.

RED, captured on the base commit `fed1b972` before any edit: 11 failures in 500
iterations (2.2%, matching the ticket's ~2.4% estimate), every one
`TempDir RemoveAll cleanup: unlinkat .../002/instance: directory not empty` on
`TestModelVocabularyStore_SinkForForwardsEveryVariant` — always TempDir `002`, the one the
`sinkFor(nil)` store owns, which is what pins the diagnosis to this store rather than to
the test's first one. GREEN is the same command over the fixed tree.

## Documentation handoff

None. The ticket carries no documentation acceptance criterion and no shared-doc
requirement; the change is one line inside one test function.
