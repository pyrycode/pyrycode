# Memory OS credential storage

## Files read

- `cmd/pyry/memory_credential.go` → `runMemoryCredential`, `readMemoryInput`: bounded stdin and JSON-only command contract.
- `cmd/pyry/memory_credential_file.go` → `memorySelected`, `set`, `directory`: canonical selection, immutable generations, pinned owner-only storage and locking.
- `cmd/pyry/memory_credential_test.go` → lifecycle, refusal, cancellation and fresh-process tests: preserve file behavior.
- `cmd/pyry/claude_account.go` → `keychainCommand`, `runTokenCommand`: fixed diagnostics, bounded output and process-group cancellation.
- `cmd/pyry/claude_account_keychain_test.go` → `TestReadKeychainItem`: controlled tool and leak checks.
- `docs/knowledge/features/cli-verb-dispatch.md` § Memory credential persistence: selection must commit before the old secret changes.
- `docs/knowledge/features/claude-account-source.md` § The selection file needed the same trust check as the secret it points to: metadata is an access-control boundary.
- `CODING-STYLE.md` § Persistent data conventions and `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change.

## Context

Previously unconfigured memory credentials should prefer usable host OS storage.
Existing file selections remain file-backed. No consumer migration is needed.
No overlapping feature branch touches the existing credential files.
Sizing: one credential-lifecycle deliverable, about 740 written lines including
tests and this plan; no exports, two internal selection call sites, four acceptance
criteria and fewer than ten distinct lifecycle reject categories.

## Design

Extend canonical selection to `file`, `keychain` and `secret-service`. Keep the
stable random reference and randomly generated immutable secret generation.
Only validated metadata generates item names under `pyry.memory.openai.`;
callers cannot supply OS item names or paths. Reject a foreign reference before
reading a secret. Selected backends never fall back or cache a token.

Keep adapters together in `cmd/pyry/memory_credential_os.go`. Stream writes to
`security -i` on macOS and `secret-tool store` on Linux. macOS uses hexadecimal
`-X` password framing, with a 1900-byte token cap (stdin still has the 4096-byte
cap including LF/CRLF). Its single framed command stays below the tool's
4096-byte command-line buffer; no quoting changes token bytes. Read/delete
commands contain only generated names. No shell or terminal is attached.
Initial preference requires a synthetic random-item write/read/delete round trip
within the operation deadline. Ordinary probe failures select file before the
user token is submitted; probe cancellation or timeout aborts. Write failures
after selection never cause fallback. Verify staged OS values by reading them
before committing metadata, then publish only by the existing atomic rename.
Cleanup of uncommitted and superseded generations is best effort and bounded;
it cannot alter the committed generation on failure.

## Concurrency model

Hold the existing credential-directory flock across metadata and backend work.
Subprocesses run in their own process group, killed on cancellation. One Wait
goroutine per command exits when the bounded process/pipes close; it never
publishes metadata. All commands derive a ten-second maximum context, also
bounded by the caller's overall operation deadline.

## State transitions and identity reuse

| Event | Race-enabled coverage |
| --- | --- |
| First set chooses OS or file; probe fails/cancels | `TestMemoryOSPreference` |
| Replacement reuses reference but changes generation; repeated replacement | `TestMemoryOSReplacement` |
| Backend write or selection commit fails after completion | `TestMemoryOSReplacement` |
| Read is denied/locked/missing/invalid then recovers | `TestMemoryOSReplacement` |
| Cancelled/timed-out backend work is released later | `TestMemoryOSCancellation` |
| Fresh process retains backend despite changed availability; existing file retention | `TestMemoryOSProcess`, `TestMemoryOSPreference` |

## Error handling

Use fixed memory errors only; discard write/delete stdout and all tool stderr.
Cap read stdout, validate locally, and never wrap tool/OS errors containing
secrets. Cancellation returns the context error. Unsafe local metadata/storage
fails before any backend operation. Failed publication preserves prior metadata
and secret; unreachable orphan generations cannot become selected later.

## Testing strategy

Write controlled executable/backend tests first and observe failure before
implementation. Both platform vectors are tested on the same host. Plant old
and new values, inspect argv and metadata for raw/hex leaks, and assert old
resolution after each cleared fault. Test metacharacters, macOS framing cap,
stdin boundaries, configured-only output, and subprocess persistence without
credentials. Run `go test -race ./cmd/pyry/...`, `go vet ./...` and
`go build -o /tmp/builder-3113/pyry ./cmd/pyry`. The verifier owns the full gate.

## Open questions

None.

## Documentation handoff

- Pending documentation stage: `docs/guide.md`, “Memory credentials”: extend the existing section with initial OS-store preference, persisted backend selection, existing-file retention, streamed writes, the Keychain 1900-byte token cap within 4096-byte stdin including newline/framing constraints, and replacement-failure preservation with no fallback after a failed write.
- Pending documentation stage: `docs/deployment.md`, “Memory credentials”: explain usable unattended Keychain/Secret Service access and locked/denied-store failures; retain the headless file fallback path, service-user ownership, 0700 directories/0600 files and separation from Claude account/login credentials. Replace the statements deferring OS preference to #3113.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `memorySelected` validates canonical metadata and IDs; only the committed reference can resolve. OS names are generated in a dedicated memory namespace.
- Tokens: immutable random generations preserve the selected secret through failures; write adapters consume streams, never argv. OS storage and owner-only file fallback address other local users, not a compromised service UID.
- File operations: preserve pinned no-follow descriptors, EUID checks, 0700 directories, 0600 files from creation and atomic selection rename.
- Subprocesses: fixed tool/vector, no shell, no terminal; scrub inherited Claude token, discard tool diagnostics, kill process groups and bound Wait pipe shutdown.
- Cryptography: reuse `crypto/rand` identifiers; no encryption implementation or secret comparison is introduced.
- Network/I/O: no network validation; stdin and command output retain 4096-byte bounds. Hex framing is capped before submission.
- Errors/logs/telemetry: fixed errors only; no secret-bearing logs or metrics.
- Concurrency: one directory lock order; cancellation checked before commit; subprocess completion cannot independently publish.
- Threat model: reject another user's metadata redirection and accidental Claude-entry reuse; hostile service-user processes already control the same secrets and remain outside this local-storage boundary.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10

## Revisions

- 2026-10-10: the full race run exposed a test-clock error: a short wall timer
  expired during the prior credential read before the intended blocked write
  started. `TestMemoryOSCancellation` now signals caller cancellation/deadline
  after the controlled tool reports the target operation has started, asserts
  the exact context failure, and disables the race runtime's artificial child
  exit delay. The production contract is unchanged.
