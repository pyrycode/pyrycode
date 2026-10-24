# Idle eviction completion wait (#3852)

## Files read
- `internal/e2e/idle_eviction_turn_busy_test.go` → `TestE2E_IdleEviction_DefersWhileStreamTurnOpen`: closure is checked only on fresh state frames; preserve all eviction assertions and deadlines.
- `internal/e2e/internal/scenario/source_observer.go` → `Next`, `Ended`: every poll joins source completion with retained actual live idle.
- `internal/e2e/internal/scenario/source_turn.go` → `feed`, `idle`: conversation, producing session, main-turn identity and execution opening constrain completion.
- `internal/e2e/internal/scenario/source_ordering_test.go` → `TestConsumerIdleBeforeHistory`: controlled ordering pattern.
- `internal/history/log.go` → `AppendWithMetadata`: append attributed fixture facts to the real observer's isolated history.
- `docs/knowledge/features/e2e-harness-stream-interactive-harness-pattern-startstreamin.md` § Source and canonical observation: consumers must reevaluate completion before payload filters.
- `docs/knowledge/features/development-verification-prove-that-tests-distinguish-the-change.md`: demonstrate the caller defect with controlled red/green evidence.

## Change
Extract the E2E's interrupted-turn wait into a local test helper returning whether
completion was observed before its existing deadline. Check `SourceObserver.Ended`
after every successful poll, including empty envelopes. The observer already owns
the required actual idle and matching source end; no second state-frame predicate
is needed. Keep its ownership rules, marker guard, two full idle windows without
additional eviction records, active-session assertion, and subsequent eviction
plus a new record within three windows unchanged.

Historical evidence: #3849's `094cefc540` Linux gate failed the closure wait at
2026-10-24T05:47:35.260Z after 25.38s, then its same-tree named rerun passed in
16.54s. Bundle `ea1c82ee0b0db7c9f34bcddc8f0d852148386e58d09bdf822630c24dde6795aa`
(digest `ab38bafba81af64e2d3693316bb1702d6c292b9dd4988ccfeb619afa5be7a902`)
retains the verdict and rerun; the full local failure log confirms the timeout.
It does not capture frame/history ordering, so deterministic caller proof is
required. No production changes or new lifecycle state. No overlapping feature
branches touch the planned files at `368e1580653d29140beb5887a874b45a1a0f4656`.
One behaviour; estimate ~190 written lines, zero exported types, one consumer,
three acceptance criteria, no new state-machine reject branches (within limits).

## Testing strategy
First extract the unchanged predicate and add `TestIdleEvictionTurnClosed` in a
companion test file. Drive the same helper with the real observer, controlled
history appends and finite polls. Prove source-first and idle-first ordering,
including completion on an empty poll; reject source-only, idle-only, foreign
conversation/session idle and a mismatched source turn end. The idle-first case
must fail before the repair and pass after it. Run this focused regression under
Linux `-tags e2e -race`, then ten consecutive executions of the original E2E via
test admission. Run `go vet ./...` and build `./cmd/pyry` to scratch. Complete
suite acceptance belongs to the dispatcher; macOS is outside this ticket's scope.
