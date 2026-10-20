# Safe private recording artifact reads

## Files read

- `internal/e2e/internal/claudetape/runner_artifacts.go` → `runnerPrivateRead`: inspection, bounded read and shared generic rejection contract; runner audits consume it.
- `internal/e2e/internal/claudetape/lifecycle_artifacts.go` → `ReadLifecycleControl`: closed JSON decoding above the private reader.
- `internal/e2e/internal/claudetape/runner_test.go` → `TestRunnerArtifactsRejectLateChanges`: complete artifact control and ownership rejection coverage.
- `internal/e2e/internal/claudetape/lifecycle_test.go` → `TestLifecycleControlPublicationIsExclusiveAndClosed`: private control publication/read round trip.
- `internal/e2e/internal/claudetape/physical_owned.go` → `physicalReadScript`: local no-follow/nonblocking open pattern.
- `docs/knowledge/features/e2e-harness.md` → artifact audits remain required independently of settlement.
- `docs/knowledge/features/development-verification-prove-that-tests-distinguish-the-change.md` → refusal alone cannot prove ordering; observe reads themselves.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: focused race checks, bounded child helpers and symbol-based citations.

## Context

An artifact replacement between inspection and open can block on a FIFO or
expose bytes before the current identity rejection. This ticket fixes one
observable behavior: reject unsafe opened artifacts before any read. Its
regressions belong with the fix. No new architecture decision record is needed.

Sizing: approximately 300 written lines including this plan, tests and code;
zero exported types/interfaces, zero consumer signature updates, two acceptance
criteria and at most five rejection branches. All are within the role ceilings.
The estimate is above the ticket's 200-line hypothesis to include direct read
ordering and descriptor-close assertions. The same limits hold for this plan.
Remote feature branches do not touch the planned files. #3673 edits separate
observation-reader files in the same package and is not a dependency.

## Design

Keep `runnerPrivateRead(path, limit)` unchanged for all consumers. Delegate to
an unexported per-call opener variant accepting the `os.OpenFile` signature;
production passes `os.OpenFile`. Tests replace the leaf inside that opener,
after the real `Lstat`, without global hooks or scheduler races.

Open read-only with `O_NOFOLLOW|O_NONBLOCK`. Before reading, stat the descriptor
and require a regular file with mode exactly 0600, size within the caller's
limit, and `os.SameFile` identity with the inspected inode. Close on every
descriptor rejection. Preserve the limit-plus-one bounded read, close-error
rejection, and content-free `runner artifact changed` error.

## Concurrency model

The reader remains synchronous and starts no goroutines. Blocking regression
controls run in child test processes with context deadlines, kill and Wait;
their files belong to the parent temporary directory and are removed afterward.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Leaf replaced after inspection by FIFO, symlink to foreign bytes, symlink to saved original inode, different regular inode or directory | `TestRunnerPrivateReadReplacement` |
| Same inode becomes public or oversized after inspection | `TestRunnerPrivateReadReplacement` |
| Repeated valid reads and exact size boundary | `TestRunnerPrivateReadBoundaries` |

## Error handling

Inspection/open/stat/metadata/identity/read/close failures retain the same generic
error. A rejected opened file is closed before returning. No artifact content or
path is added to errors or logs.

## Testing strategy

Write regression tests first and extract only the opener seam to run them
against the unsafe behavior. Observe shared descriptor offsets through a
duplicate to prove zero bytes read on rejected regular files, and assert the
opened descriptor is closed. Bound FIFO tests with a parent process deadline.
Symlinks must fail at open, including links to the inspected original inode.
Test valid/empty/exact-limit reads, oversized/public/missing inputs, and closed
JSON controls. Then run focused macOS race tests for the affected package,
`go vet ./...` and `go build ./cmd/pyry` (output outside the worktree).
Full gates belong to the dispatcher; Linux is outside verification scope.

## Open questions

None.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `runnerPrivateRead` admits disk bytes only after validating the opened descriptor against inspected identity and private metadata.
- [Tokens] No credential lifecycle changes; synthetic test bytes only. The generic error exposes no file content.
- [File operations] No-follow open rejects even a symlink back to the original inode. Nonblocking open prevents FIFO-open waits; regular/mode/size/identity checks precede reads. Writes and caller path construction stay unchanged.
- [Subprocesses] Only test binaries are re-executed with fixed test selectors, bounded contexts and Wait; no shell commands or artifact-derived arguments.
- [Cryptography] No cryptographic operations change; artifact digest consumers retain their existing checks.
- [Network and I/O] Retain the caller size cap plus one-byte overflow detection. No network surface is introduced.
- [Errors/logs] Preserve the existing content-free failure without new logging.
- [Concurrency] Per-call injection avoids mutable global hooks. Duplicate descriptors used to observe offsets are closed by tests; timed-out children are killed and reaped.
- [Threat model] This closes the inspected-leaf replacement boundary. Existing parent-directory/path ownership contracts remain with the existing harness; no new path traversal or authorization behavior is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-20
