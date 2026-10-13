# Join cancelled memory credential helpers

## Files read

- `cmd/pyry/memory_credential_os.go` → `memoryOSCommand`, `memoryOSWrite`, `memoryOSRead`, `memoryOSDelete`: helper lifetime and its three unchanged callers.
- `cmd/pyry/memory_credential_os_test.go` → `TestMemoryOSTool`, `testMemoryOS`, `TestMemoryOSCancellation`: exec launcher, blocked backend and fixture teardown.
- `cmd/pyry/memory_credential_file.go` → `set`, `selected`, `directory`: cancellation before atomic selection publication and prior-credential preservation.
- `cmd/pyry/memory_credential_test.go` → `testMemoryInput`, `testMemoryReject`, `testMemoryStore`: fixture ownership and diagnostic suppression assertions.
- `cmd/pyry/claude_account.go` → `opWaitDelay`, `cappedBuffer`, `withoutAccountToken`: retain bounded pipe shutdown, capped read bytes and environment scrubbing.
- `docs/knowledge/features/cli-verb-dispatch.md` § Memory credential persistence → Cancellation and persistence tests: cancel only after the target operation starts; compare selection bytes as well as prior resolution.
- `docs/specs/architecture/3113-memory-os-credentials.md` § Revisions: current native API vectors and noninteractive storage boundary.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: channel synchronization, resource ownership and proof that the regression distinguishes the old behavior.

## Context

The cancellation family intermittently races helper writes against temporary-root
removal. `memoryOSCommand` currently returns on context cancellation before its
`cmd.Wait` goroutine joins. The fixture releases the helper after that return,
and its assertion-failure paths do not join the store worker. Repair this single
lifecycle contract without claiming established production impact.

Sizing: approximately 300 written lines including this plan and tests; no new
exported types, no consumer signature changes, four acceptance criteria and no
new credential rejection categories. Three callers stay unchanged. No other
remote feature branch touches either target file. No decision record is needed.

## Design

`memoryOSCommand` waits synchronously after successful `Start`, then gives the
context error precedence over any command error. `CommandContext` still cancels
the dedicated process group with SIGKILL; keep `memoryCredentialDeadline` and
`opWaitDelay`. Successful bounded stdout and static storage errors retain their
existing contracts. Remove the detached wait goroutine and early-return select.

Cancellation fixtures register cleanup before starting the store worker. Cleanup
cancels idempotently and joins the worker before input files and temporary roots
are removed. Stop releasing a killed helper to perform synthetic backend work.
The controlled hung helper records its PID before its started marker, allowing
the test to assert that it has been reaped when the operation returns.

A focused command regression uses a channel-gated stdin reader to hold the exec
copy worker during cancellation. The command must remain pending until that
reader is released and must observe reader completion before returning. Channels
control progression; timers only bound test assertions and cleanup diagnostics.
Both cancellation causes are covered. Cleanup releases and joins all work even
after an assertion failure. The production callers supply finite readers.

## Concurrency model

Production uses the exec package's context watcher and pipe-copy workers;
`cmd.Wait` joins them before return. No application wait goroutine remains.
Tests own each launched store/command worker and join it in LIFO cleanup before
fixture descriptors, environment and roots are torn down.

## State transitions and identity reuse

| Event | Race-enabled coverage |
| --- | --- |
| Cancel or expire a started probe; no selection publication | `TestMemoryOSCancellation` |
| Cancel or expire a prior read or candidate write; retain exact selection and old resolution | `TestMemoryOSCancellation` |
| Reap a hung helper before operation return; no post-return fixture write | `TestMemoryOSCancellation` |
| Cancel with a gated stdin copy; release and join before return/cleanup | `TestMemoryOSCommandCancellationJoin` |
| Assertion failure before worker result is consumed | Cleanup registered by both cancellation tests before worker launch |

## Error handling

Return `context.Canceled` or `context.DeadlineExceeded` after joining. Suppress
tool diagnostics and cancelled output. Other command failures remain
`errMemoryStorage`. Test cleanup uses nonfatal diagnostics so joining is not
skipped by another assertion failure.

## Testing strategy

Write the regression first and demonstrate failure against the existing command
lifecycle. Retain both platform vectors and probe/read/write combinations, with
exact selection preservation and metadata/argv secret checks. Run the focused
join test, `go test -race -json -count=20 -run '^TestMemoryOSCancellation$'
./cmd/pyry`, `go test -race ./cmd/pyry/...`, `go vet ./...` and
`go build -o /tmp/builder-3311/pyry ./cmd/pyry`. Record executed/pass/fail/skip
counts in the PR. The verifier owns the complete `make check`, including
staticcheck, guards and fake-daemon e2e; its full-gate result is pending.

## Open questions

None.

## Documentation handoff

- Pending documentation stage: update `docs/knowledge/features/cli-verb-dispatch.md`, “Memory credential persistence” → “Cancellation and persistence tests”, to describe the resulting helper join and fixture cleanup ordering while retaining bounded cancellation and diagnostic suppression.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `memoryOSCommand` discards cancelled output; successful reads remain capped and validated by `memoryOSRead`. `memorySelected` still validates canonical metadata and generated item identities.
- Tokens: streamed input, random immutable generations, exact prior selection preservation and backend byte validation remain intact; add cancellation metadata/argv leak assertions. No new credential storage or exposure channel.
- File operations: production keeps pinned no-follow descriptors, EUID checks, 0700 roots, 0600 files and atomic publication. Test-only PID/started markers contain no secrets; cleanup joins before root removal.
- Subprocesses: retain fixed isolated Python vectors, environment scrubbing and process-group SIGKILL. Joining `cmd.Wait` closes the lifetime gap. The gated-reader regression uses a finite, explicitly released test reader; production callers provide finite readers.
- Cryptography: no primitives or secret comparisons change; `memoryID` continues using `crypto/rand`.
- Network/I/O: no network surface changes. Preserve capped stdout and `opWaitDelay`; no terminal is attached and native API interaction suppression is unchanged.
- Errors/logs/telemetry: context errors take precedence after joining, all other tool failures remain static; no logging or telemetry is introduced.
- Concurrency: each started command and test worker has an explicit join; cancellation still prevents selection publication under the existing directory lock.
- Threat model: local storage protects against other users and metadata redirection; compromised service-UID processes remain outside the existing boundary. No broader threat-model change is introduced.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-13
