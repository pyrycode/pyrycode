# Concurrent capture transaction diagnosis and repair

## Files read

- `internal/memorycapture/storage.go` → `transaction`, `lock`, `publish`, `privateFile`, `readRecord`, `checkDirectory`, `openDirectory`: filesystem boundaries, pinned descriptors, flock and substitution fences.
- `internal/memorycapture/batches.go` → `Publish`, `Pending`, `Report`: nil executable results on error, immutable input and evidence union.
- `internal/memorycapture/batches_test.go` → `TestConcurrentStores`, `TestCrossProcess`, `TestHelperProcess`, `TestPublicationFailures`, `TestEvidenceFailures`: publication/report phases, subprocess harness and durable recovery checks.
- `internal/memorycapture/storage_test.go` → `TestFilesystemAttacks`, `TestPostRenameSubstitution`, `TestCancelledLockAndPublication`: storage permissions, identity fences and cancellation.
- `docs/knowledge/features/memorycapture-package.md` → Concurrency and durable publication; Private records and validation: retain validated descriptors through rename; sync surviving complete records before execution.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change; Test execution and artifact survival: require a controlled red/green regression and equivalent baseline suite/fixture state.
- `CODING-STYLE.md`: contextual wrapped errors, bounded I/O and joined test goroutines.
- `.github/workflows/self-check-daily.yml` and `release.yml`: current workflows are Ubuntu based; the self-check uses live credentials and is unsuitable for hermetic diagnosis.

## Context

The reported macOS failure at `63aabec7` contains only `capture transaction: no such file or directory`. Current memorycapture source at `7a1daa57` is identical to that baseline. Linux stress passes do not locate the failing macOS syscall. Concurrent first creation of `.lock` before flock is a hypothesis, not an established diagnosis. Do not select or introduce a transaction retry until native evidence identifies the operation and interleaving.

This plan first makes failures actionable and prepares ordinary package tests for native diagnosis. A causal repair requires a revision recording the evidence and regression design before implementation. No decision record or documentation change is currently needed because storage semantics are unchanged.

Overlap: #3178 / PR #3190 changes receipt validation in `readRecord`; keep diagnostics local and preserve that validation when integrated. #3210 changes CLI fixtures independently and is not a dependency.

Sizing: one deliverable (reliable concurrent transactions), three acceptance criteria, zero exported additions, zero consumer migrations. Initial diagnostic work is estimated at 450 written lines including tests and this plan, below 800. Existing state-machine rejects remain unchanged; fault injection covers at most nine operation boundaries. The conditional repair must be re-sized once native evidence is available.

## Design

- Wrap filesystem errors with static operation names and `%w`, retaining `errors.Is` / `errors.As`, including errno, without including identity, payload or changed paths. Distinguish directory traversal/checking, lock open/validation/flock, record open/read/recovery sync, temporary creation/write/sync/close, rename and parent sync. Keep existing sentinel rejection and absence semantics.
- Reuse per-store `fault` for lock-open, lock-flock and temp-create boundary injections. These are test-only hooks configured before operations start; production executes the same syscall and validation path with no hook.
- Release all 12 `TestConcurrentStores` handles at the lock-open boundary against fresh storage with `.lock` absent. Publication and reporting remain separate joined phases. Report phase, worker and extracted errno on failure. Verify exact reopened sorted/deduplicated evidence and immutable publication convergence.
- Retain a combined, bounded 16 KiB stdout/stderr buffer for each process helper and include it on start/wait errors, marking truncation. Keep the process deadline and join every started child.
- Diagnostic tests inject operation failures and check the label, original errno, no payload text, nil executable batch and complete reopened state. Real record substitution additionally checks syscall provenance without a synthetic read hook. A deliberately failing process checks captured output and its bound.

## Concurrency model

Production still holds the handle read lock for descriptor lifetime, then kernel flock across the whole transaction. No new goroutines, caches, mutexes or retries in production. Tests use cancellable barrier waits and join all workers; a bounded test context breaks a stalled barrier. Process output is synchronized and processes are bounded by `exec.CommandContext`.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Concurrent first publication with absent shared lock | `TestConcurrentStores` |
| Competing payloads for one identity | `TestConcurrentStores`, `TestCrossProcess` |
| Concurrent duplicate reports and reopen | `TestConcurrentStores`, `TestCrossProcess`, `TestEvidence` |
| Failed pre-rename transaction, then reopen/retry | `TestTransactionDiagnostics`, `TestPublicationFailures`, `TestEvidenceFailures` |
| Rename succeeds but directory sync fails | `TestTransactionDiagnostics`, `TestPublicationFailures`, `TestEvidenceFailures` |
| Cancellation while lock/barrier waits | `TestCancelledLockAndPublication`; barrier bounded by test context |
| Directory/lock/record/source substitution | `TestFilesystemAttacks`, `TestPostRenameSubstitution`, `TestRecordOpenDiagnostic` |
| Reused identity and immutable retry | `TestImmutableRetry`, `TestIdentityIsolation` |
| Helper fails before or during transaction | `TestProcessDiagnostics` |

## Error handling

All existing failures remain errors. No retry, ignored transaction error, partial successful evidence update or executable batch on error is introduced by diagnostics. An absent record remains an empty store, while lock absence must not be mistaken for transaction success. Keep failed native runs, including JSON output and metadata, as evidence.

## Testing strategy

Write diagnostic assertions first and observe failures against unchanged storage. Run `go test -race -json -count=100 -run '^TestConcurrentStores$' ./internal/memorycapture` on unchanged baseline and instrumented source, then the complete race package, `go vet ./...` and `go build ./cmd/pyry` on Linux. Record commit, OS/build, architecture, Go version, counts and exact skips.

Native macOS continuation must run the same baseline/instrumented concurrency commands with ordinary test admission, preserve failures, identify syscall/interleaving, then implement a controlled pre-fix-failing behavioral regression and repair. If lock creation is confirmed, only bounded cancellable recovery of that open against the same validated parent descriptor is in scope; retain all flags, file validation, flock and fences. Exercise persistent errors, cancellation, substitution, immutable convergence and the full reopened evidence union. Run complete macOS/Linux race packages and at least 20 macOS executions each of regression and `TestConcurrentStores`. Dispatcher/verifier owns full macOS `make check`; attribute unrelated CLI failures with equivalent baseline suite and fixture state including producers while #3210 remains open.

## Open questions

- Which macOS syscall and interleaving caused the original ENOENT? Unresolved until native operation-level evidence; no causal repair claimed yet.
- Native execution route: this host is Linux and no native route is exposed. Recovery requests a scoped PR workflow on a native macOS runner with read-only repository permissions and no live credentials. Builder role permits writes only under `cmd/`, `internal/` and this plan, so `.github/workflows/` is outside this run's allowed scope. The operator must authorize another role to add/run the job, or return this ticket on a native Mac. Required evidence is current-role work, not a documentation handoff.

## Security review

**Verdict:** PASS for diagnostic changes; re-review the causal repair when designed.

**Findings:**

- [Trust boundaries / files] No validation changes: `privateFile`, `openDirectory`, `sameFile` and `readRecord` still enforce descriptor-relative no-follow traversal, account ownership, exact 0700/0600 modes, regular type, one link and complete record validation. Preserve errno alongside `ErrUnsafe` when a directory fstat fails.
- [Tokens / errors] SHOULD FIX: diagnostics must use static operation names and never append identity, payload, metadata or evidence. Tests use a distinctive payload and verify it cannot enter error text. No credentials are read or generated.
- [Subprocesses / I/O] SHOULD FIX: helper output must be capped at 16 KiB and mark truncation. Tests invoke only their own binary with fixed arguments and controlled fixture environment; `CommandContext` limits lifetime. No shell or live service involved.
- [Cryptography / network] No cryptographic changes or network I/O: existing digest and random temporary names remain intact. Native workflow, if separately authorized, must use read-only repository permissions and no live-service secrets.
- [Concurrency] No production synchronization changes: hooks are configured before workers; every barrier is bounded and every worker/child is joined. Immutable input and evidence remain protected by kernel flock across the transaction.
- [Threat model] No widening of private storage or symlink acceptance. Same-account malicious rewriting remains outside the existing integrity-check threat model. A hypothetical transient-open retry is deliberately deferred pending evidence and separate security review.

**Reviewer:** builder (self-review)
**Date:** 2026-10-11
