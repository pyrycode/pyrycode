# Canonical path runtime repair (#3525)

## Files read

- `internal/canonicalpath/path.go` → `Resolve`, `canonicalCase`, `matchEntry`: existing errors and portable casing rules.
- `internal/canonicalpath/path_test.go` → `TestResolve_Permissions`, `TestMatchEntry`: permission fallback and portable ambiguous selection.
- `docs/knowledge/features/canonicalpath-package.md` → Path and error contract, Testing: resolution is not confinement or a stable handle; preserve portable selection tests.
- `docs/knowledge/features/development-verification-test-execution-and-artifact-survival.md` → Diagnosing startup recovery runtime: matched complete-suite timings, active interval versus cumulative cost.
- `cmd/pyry/memory_transcript_reply_ownership_test.go` → `testReplyOwnership`: retain all terminal/ownership/chunk partitions unchanged.
- `cmd/pyry/reply_fallback_timing_test.go`, `reply_fallback_cancellation_test.go` → fallback deadlines, evidence and joined child cleanup remain unchanged.
- `Makefile`, `scripts/test-admission.py`, `scripts/check-restoration.py`: finite budgets, host admission, mandatory restoration policy and deliberate slow separation.
- External `tui-driver/pkg/tuidriver/cwd_darwin.go` → `canonicalisePath`: F_GETPATH analogue, but ordinary blocking open and unchecked close are unsuitable here.

## Context

Repeated sorted ancestor enumeration amplifies filesystem fanout on macOS. The retained unchanged-source diagnostic scanned a private ancestor 16 times, returning 560,016 entries after adding 35,000 siblings. A package timeout stack corroborates sorting a wide ancestor. This establishes avoidable repeated work, not complete attribution for every historical timeout. Required apps repair #3524 is merged in base `70c295a2`. No decision record is needed.

## Design

Keep the public `Resolve(string) (string, error)` contract. After Abs/EvalSymlinks succeeds, macOS asks the kernel for the current descriptor path using F_GETPATH. Open with O_EVTONLY and O_CLOEXEC: metadata access must neither wait on FIFO peers nor read file contents. Check close even when the probe fails; any native failure falls back to the existing component walk. Other platforms always use that portable walk. No cache or exported API is added. A private read-directory parameter allows operation-count tests without mutable global hooks. Selection remains exact first, then unique EqualFold, otherwise unchanged.

Fetch/feature diff review found no unmerged canonicalpath changes. #3504 consumes this repair; its checkpoint work is not a dependency or part of this diff.

## Concurrency model

Synchronous per-call descriptors, closed before return. No shared cache, locks, goroutines or cwd changes. Existing command child-process cleanup remains untouched.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Repeated resolution before/after adding 35,000 siblings | `TestResolveAncestorFanout` |
| Case-only rename | `TestResolveFreshIdentity` |
| Ancestor replacement under reused pathname | `TestResolveFreshIdentity` |
| Symlink retargeting and missing target | `TestResolveFreshIdentity` |
| Repeated successful and failing descriptor probes | `TestNativePathDescriptors` |
| FIFO/socket leaves without peer | `TestResolveSpecialLeaves` |

## Error handling

Abs/EvalSymlinks preserve wrapped error identities. Native open, descriptor query, invalid/empty returned path or close failure declines the optimization, retaining best-effort casing and permission behavior. No new resolution errors or logging.

## Testing strategy

First retain the old operation-count failure with the unchanged-source overlay, then require the same wide fixture to pass with the repair. Existing portable casing and permissions tests remain mandatory. Add freshness, special-leaf and descriptor-lifecycle coverage, including injected native failures and close errors. Compare complete ordinary command suites with identical Go/environment, fanout and admission; retain JSON per-test timings and finite package budgets. Run race checks on canonicalpath and cmd/pyry, vet and build. The dispatcher owns full-module gates; retain required full-corpus/full-gate acceptance as a handoff when not run in this builder stage. No live Claude or Linux checks.

## Open questions

- Measure the full-suite contribution separately from the demonstrated fanout amplification; record historical-attribution limits even if candidate passes.
- Confirm native metadata-open behavior and descriptor closure with macOS tests before handoff.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/canonicalpath-package.md`, “Path and error contract” and “Testing”, to describe the implemented macOS resolution path, fallback and descriptor lifecycle, rename/replacement freshness, wide-directory operation-count proof and filesystem-dependent skip limits. Preserve the exact/unique/ambiguous casing rules and the statement that resolution does not enforce confinement or provide a stable file handle.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `Resolve` consumes caller paths and returns metadata only; consumer trust/confinement checks stay in place. Existing Abs/EvalSymlinks errors remain authoritative.
- [Tokens] No credentials generated, stored or logged.
- [File operations] SHOULD FIX: native opens must use O_EVTONLY/O_CLOEXEC, validate absolute nonempty output, and check close on every opened descriptor; tests cover this. No path cache can retain stale identity. Resolution remains subject to filesystem changes after return, as before.
- [Subprocesses] No production subprocess changes; existing command secrecy and process-reaping assertions remain intact.
- [Cryptography] No cryptography changes.
- [Network and I/O] No network changes; special leaves require metadata-only opens so FIFO/socket resolution cannot wait on a peer.
- [Errors/logs/telemetry] Probe failure must be silent fallback, without exposing a new error or path log.
- [Concurrency] No mutable global test hooks, shared descriptor ownership or goroutines.
- [Threat model] This operation supplies path spelling, not authorization or a stable file handle. Existing consumer confinement remains required.

**Reviewer:** builder (self-review)
**Date:** 2026-10-17

Sizing: one deliverable; forecast <=650 written lines, 0 exported types, 0 consumer updates, 5 acceptance criteria, fewer than 10 native reject branches.
