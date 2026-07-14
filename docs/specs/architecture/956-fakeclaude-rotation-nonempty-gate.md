# Spec #956 — Close the fakeclaude JSONL-rotation create-before-content race

**Ticket:** [#956](https://github.com/pyrycode/pyrycode/issues/956)
**Size:** XS — one test-harness file, ~5 lines, zero production code.
**Security-sensitive:** No (test-harness timing; no auth/crypto/untrusted-input surface). No security-review pass.

## Files to read first

- `internal/e2e/fakeclaude_test.go:79-104` — `waitForRotatedJSONL`, the polling consumer. This is the **only file to change.** Extract: the poll loop matches a `<uuid>.jsonl` **name** and returns immediately (line 94-95) with no content check — that early return is the defect.
- `internal/e2e/fakeclaude_test.go:49-60` — the caller + the line-59 `Size()==0` sanity assertion the race trips. After the fix this assertion becomes deterministically satisfied; keep it as documentation of the invariant.
- `internal/e2e/internal/fakeclaude/main.go:586-599` — `openSession`, the producer. Confirms the three-syscall gap (`OpenFile` → `WriteString("{}\n")` → `Sync`) and that the file is opened `O_WRONLY|O_APPEND|O_CREATE` and **never truncated** afterward. This append-only fact is what makes a "size > 0" latch monotonic. Read-only reference — **do not modify.**
- `internal/e2e/harness.go:271-321` — `StartRotation`, the harness constructor the test uses. Read-only reference for context; unmodified.
- `docs/knowledge/features/fakeclaude-binary.md` — producer writer inventory (per the ticket); confirms `{}\n` is the only rotation payload.

## Context

`TestE2E_StartRotation_PrimitiveWiresFakeClaude` intermittently fails under the full-suite `-race` leg of `make check` with:

```
fakeclaude_test.go:59: rotated jsonl …/<uuid>.jsonl is empty; fake-claude payload missing
```

**Root cause (confirmed, test-harness-only):** the producer's `openSession` makes the rotated `<uuid>.jsonl` **name** visible via `os.OpenFile` (syscall 1) *before* it writes the `{}\n` payload (syscall 2). The consumer `waitForRotatedJSONL` returns the instant a matching name appears in `os.ReadDir` — with no content check. When the poll wins the race between syscalls 1 and 2, the test body's `os.Stat(rotated).Size()` at line 54 observes a zero-byte file and the line-59 assertion fails. `-race` does not catch it: it's a filesystem create/write ordering gap, not a shared-memory data race.

This is a pre-existing flake (proved by the #950 / PR #955 attribution work), not a regression, and no `pyry` production code is implicated — the test exercises the harness primitive without touching pyry's live rotation watcher.

## Design

**Chosen seam: consumer-side non-empty gate in `waitForRotatedJSONL`.**

Fold a content check into the poll's success condition so the function returns a path **only once that file is non-empty**. The empty create→write window is then closed *by construction*: the wait provably cannot return on a zero-byte match, so the downstream `Size()==0` assertion becomes deterministic rather than racy.

Concretely, in the poll loop (`fakeclaude_test.go:82-98`), where a `<uuid>.jsonl` stem currently matches and returns immediately, add: stat the candidate path and require `Size() > 0` before returning. If the name matches but the payload has not yet landed (`Size()==0` or a transient stat error), **do not return** — fall through to the next 50 ms poll tick. No `time.Sleep` beyond the existing poll interval, no timeout change.

Contract for the modified helper (signature unchanged):

```
waitForRotatedJSONL(t, sessionsDir, initialUUID string, deadline, stderrFn) string
```

- Returns the absolute path of a rotated `<uuid>.jsonl` whose stem is a v4 UUID ≠ `initialUUID` **and whose size is > 0**.
- On deadline expiry, `t.Fatalf` with the existing message (unchanged) plus `stderrFn()`.

**Why size > 0 is a sufficient and monotonic gate.** `openSession` opens the file `O_APPEND` and only ever grows it (`{}\n` at open, later `appendTurnGrowth`/`emitStructuredJSONL` appends); the rotated file is never truncated or removed under its final name. So "size > 0" is a one-way latch: once true it stays true, guaranteeing the test body's subsequent `os.Stat` at line 54 also observes non-empty. The producer fsyncs the `{}\n` before `openSession` returns, so the non-empty size is cross-process visible well within the 5 s / 50 ms poll budget — no false-timeout risk.

**Alternative considered — producer write-then-rename (rejected):** publish the rotated file atomically (`CreateTemp` → write → `Sync` → `Close` → `Rename`) so it's never observable empty under its final name. Rejected: `openSession` returns an open append fd that `main.go`'s loop keeps writing to, so an atomic rename would force a reopen-for-append after the rename, doubling the syscall count and complicating the fd lifecycle for both the initial and rotated opens — heavier than warranted for a test primitive, and it would touch production-adjacent harness-producer code the consumer fix leaves untouched. The consumer gate is strictly local to the failing test file.

## Concurrency model

No goroutines added or changed. The fix is a stricter success predicate in an existing single-threaded polling loop. The cross-process producer/consumer relationship is unchanged; the gate only adds a per-poll `os.Stat` size read on the candidate path.

## Error handling

- Transient `os.Stat` error on the candidate path (should not occur — the file is `O_CREATE`d and never removed under its final name): treat as "not ready yet," keep polling. Never `t.Fatalf` on a per-poll stat error; only the existing deadline expiry fatals.
- Deadline path unchanged: same `t.Fatalf` message and `stderrFn()` inclusion.

## Testing strategy

- **Primary proof is by construction, not by reproduction.** The flake needs concurrent full-suite `-race` load and does not reproduce reliably in isolation, so no red-on-main liveness test is added. Correctness is argued deterministically: `waitForRotatedJSONL` cannot return on a zero-byte match (the `Size() > 0` predicate gates the return), and the file is append-only so the return-time non-empty observation persists to the line-54 stat.
- **Regression guard:** `go test -tags e2e -race -count=1 ./internal/e2e/...` stays green (AC4). Run it a few times to confirm no new flake and no false timeout on the rotated-file wait.
- **Behavioural assertions preserved:** after the trigger drops, a fresh `<uuid>.jsonl` with a v4-UUID stem distinct from `initialUUID` still appears, and the line-51-60 non-empty sanity block still runs (now deterministically green). Do **not** delete the line-59 assertion — it documents the invariant the fix now guarantees.

## Scope guard

- **Confined to `internal/e2e/fakeclaude_test.go`.** No production code, no `internal/sessions`, no live rotation watcher, no change to `internal/e2e/internal/fakeclaude/main.go`.
- **Out of scope — do not bundle:** the `e2e` relay structured-stream RED-on-main pair (`TestTwoPhoneStructured_InteractiveReceivesStream` / `TestRelayV2_InterruptStopsRunningTurn`). Those go through pyry's live fsnotify-tailing producer — a different mechanism from this harness primitive, which bypasses the watcher. Even if this fix generalises, that pair stays its own (unfiled) ticket.

## Open questions

None. The seam, the predicate, and the append-only monotonicity argument are fully determined by the read files above.
