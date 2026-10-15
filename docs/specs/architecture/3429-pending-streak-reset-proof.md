# #3429 — Decisive pending streak reset proof

## Files read

- `internal/msgqueue/queue.go` → `Queue`, `New`, `drain`, `giveUp`: elapsed failure tracking, retry ordering and callbacks.
- `internal/msgqueue/giveup_exempt_test.go` → `TestGiveUpExempt_PendingResetsStreak`, `scriptDeliver` and sibling exemption tests: existing timing weakness and retained coverage.
- `internal/msgqueue/lifecycle.go` → `notifyTerminal`: delivery observation follows confirmed advancement.
- `docs/knowledge/features/msgqueue-package.md` and `msgqueue-package-give-up.md` § Bounded give-up / Testing: elapsed-time semantics and scheduling-sensitive failure windows.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: mutation overlays and bounded contention comparisons.
- `CODING-STYLE.md`: concurrency, tests, contract comments and file limits.

## Context

The current phase sleeps neither acknowledge drain progress nor constrain failure
elapsed time under contention. Its arithmetic also incorrectly claims 30+30 ms
exceeds 80 ms. One deliverable replaces that proof without changing production
give-up policy. No decision record is needed.

Sizing: approximately 250 written lines including this plan, no exported types,
no consumer updates, three acceptance criteria, no new production reject branches.
This remains within all five builder limits after planning. The #3364 analogue
used an evidence comparison too; this ticket additionally controls elapsed time.
Fetched feature branches have no overlap with the two intended Go files.

## Design

Add a private `Queue.failureNow func() time.Time`, initialized by `New` to
`time.Now`, configured only before `Run`. Use it for failure-streak start and
elapsed subtraction in `drain`; retain real retry timers and enqueue timestamps.
Default timestamps retain Go's monotonic elapsed-time measurement.

The named test scripts delivery results through channels. The drain alone owns
the fake time, updating it inside delivery before returning the scripted result.
Entry into the next delivery acknowledges complete processing of the previous
result, including pending classification, reset and retry. Check the same queued
head at each entry. Use nonzero base time and these elapsed offsets:

| Offset | Result | Evaluated streak |
| --- | --- | --- |
| 0 ms | real failure | 0 ms |
| 30 ms | real failure | 30 ms < 80 ms |
| 40 ms | pending | reset |
| 100 ms | real failure | 0 ms; original start already exceeds 80 ms |
| 130 ms | real failure | 30 ms < 80 ms; original elapsed 130 ms |
| 140 ms | recovery | one confirmed delivery |

Each phase wait also observes give-up, failing on abandonment rather than a
phase timeout when the reset is removed. After delivery, cancel/join and assert
exactly one delivery and no give-up. Emit acknowledged phase offsets in test logs.

## Concurrency model

Keep the existing Run/drain goroutines. Delivery channel sends/receives select
on cancellation. A finite test context bounds phase waits; cleanup always cancels
and joins Run with a finite deadline, including after fatal assertions. No global
clock or shared fake-time writer is introduced.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Repeated real failure on the same head | `TestGiveUpExempt_PendingResetsStreak` |
| Pending processed, then another real-failure streak | `TestGiveUpExempt_PendingResetsStreak` |
| Recovery resolves the retained head exactly once | `TestGiveUpExempt_PendingResetsStreak` |
| Cancellation while delivery waits / after recovery | `TestGiveUpExempt_PendingResetsStreak` cleanup |

No identifier is removed or reused in this proof.

## Error handling

Test timeout and give-up are distinct assertion failures. Production errors,
pending classification, elapsed threshold, give-up notifications and recovery
remain unchanged. Existing genuine-failure, nil-Pending, held-head acceptance
and content-free logging tests remain intact.

## Testing strategy

Write the scripted proof first and observe its failure before adding the seam.
Run a scratch Go overlay deleting only the pending reset; require the named
give-up/delivery assertion to fail. Compare base and candidate named tests with
the same bounded CPU contention, affinity, Go concurrency and repetition settings.
Record revisions, exact commands, phase logs and JSON run/pass/fail/skip counts
in the PR. An old pass does not disprove the historical failure.

Run the exemption family ten times and the complete msgqueue package once with
`-race -json -timeout=10m`, then `go vet ./...` and `go build ./cmd/pyry` (output
outside the worktree). The verifier owns the full-module race gate.

## Open questions

None. Contention comparison is evidence about this experiment only; historical
causation remains unproven.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `failureNow` is private, initialized in `New`; no caller or untrusted payload chooses production time.
- Tokens/secrets: no credential handling changes; only synthetic phase data enters evidence.
- File operations: production adds none; mutation overlays stay outside the checkout.
- Subprocesses: production adds none; bounded test commands use fixed arguments.
- Cryptography: no keys, randomness or cryptographic operations change.
- Network/I/O: no production I/O changes; finite context bounds test coordination.
- Errors/logs: `drain` retains content-free logs and existing secret sentinel coverage; no payload enters new phase logs.
- Concurrency: configure the seam before Run; fake time belongs only to the drain; cancelable delivery and joined cleanup avoid leaked goroutines on failure.
- Threat model: no relay/mobile/CLI boundary changes; elapsed retry policy and head retention remain the existing contract.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15
