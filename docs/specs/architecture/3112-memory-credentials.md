# Protected file-backed memory credentials (#3112)

## Files read

- `cmd/pyry/main.go` → `runArgs`, `helpText`: local verb dispatch and help.
- `cmd/pyry/claude_account.go` → `readTokenFile`, `parseTokenBytes`, `ownedByEUID`: bounded ASCII tokens and descriptor ownership conventions; Claude storage stays independent.
- `CODING-STYLE.md` → Persistent data conventions: sync, close, atomic rename.
- `docs/knowledge/features/claude-account-source.md` → The selection file needed the same trust check as the secret it points to: protect metadata as strictly as credentials.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change: block operations explicitly for cancellation proof.
- `docs/knowledge/INDEX.md`: owning-topic map; QMD searched for memory credentials and selection trust.

## Context

Memory setup needs local credentials independent of vault/settings and Claude login.
This supplies one independently usable lifecycle; OS-store preference is deferred to #3113.
The persisted file backend and stable opaque reference let later callers retain selection.
One deliverable, four criteria, zero exported types, one existing caller update,
approximately 740 written lines including tests and plan, and no new state machine.
Overlap with #3089 in `main.go` is in daemon composition, separate from local dispatch/help.

## Design

Add `memory_credential.go` for local command and context-bound operations and
`memory_credential_file.go` for protected persistence, plus lifecycle tests.
`runMemoryCredential(ctx, args, stdin, stdout)` accepts exactly credential set/status openai.
`setMemoryCredential(ctx, stdin)` returns an opaque reference; `memoryCredentialStatus(ctx)`
validates the current credential; `resolveMemoryCredential(ctx, reference)` reads it afresh.
All operations impose a ten-second deadline, shortened by caller cancellation/deadline.
Stdin is an OS file: poll in short context-aware intervals, with no background reader.
Read at most 4097 bytes, reject over 4096 before `parseTokenBytes` strips a newline.

Open service-user HOME and traverse `.pyry/memory/credentials` through pinned directory
descriptors with no-follow opens. HOME must be user-owned and not writable by others;
storage directories must be owned directories with exactly 0700 permissions.
Creation uses 0700 immediately; existing unsafe storage is refused without chmod.
Files are opened no-follow/nonblocking, then checked on the descriptor for owner,
regular type and exactly 0600 mode. Selection is bounded canonical JSON containing
only backend `file`, a random reference and a random generation, never a path/token.
Only `memory:openai:<random hex>` matching committed selection can resolve; generation
has a strict hex shape and selects a lifecycle-owned filename in the pinned directory.

Set locks the credentials directory with a cancellable nonblocking advisory lock.
Validate existing selection and secret before replacement. Write a new immutable
generation at 0600 using exclusive creation, sync and close, then stage protected
selection metadata, sync and close. Check context before renaming selection: that
single rename commits both credential choice and stable reference. Failure removes
uncommitted files; successful replacement removes the previous generation. No worker
can publish after the caller returns. No logs or errors include caller values/OS errors.

## Concurrency model

No goroutines. Polling stdin and directory flock retry observe cancellation.
Concurrent writers serialize under flock; readers see either complete selection,
and a reader holds the same lock while resolving so cleanup cannot remove its generation.
All file work is synchronous on protected local regular files, as in `readTokenFile`.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Initial selection and fresh process resolution | `TestMemoryCredentialLifecycle`, `TestMemoryCredentialProcess` |
| Repeated replacement under stable reference | `TestMemoryCredentialLifecycle` |
| Invalid input or unsafe/missing selected storage then recovery | `TestMemoryCredentialRefusals` |
| Failed publication preserves selection | `TestMemoryCredentialFailedSave` |
| Cancel/timeout while stdin or writer lock blocked | `TestMemoryCredentialCancellation` |

## Error handling

Use fixed sanitized errors for arguments, invalid input, unsafe storage, malformed
selection, unavailable credential and failed save. Context errors contain no values.
Absent selection alone means unconfigured; selected missing/invalid credentials fail.

## Testing strategy

Write tests first and observe missing implementation failures. Table-drive byte/newline
boundaries, bad arguments, storage type/mode/owner and metadata validation. Plant distinct
tokens and inspect output/errors/metadata. Exercise blocked stdin, lock cancellation,
atomic-save failure, restored-storage recovery and subprocess resolution. Run focused
tests then `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

None. References and generation IDs use crypto/rand; no service calls or new dependencies.

## Documentation handoff

- Pending documentation stage: `docs/guide.md`, new “Memory credentials” section: document set/status commands, stdin secret input and the 4096-byte cap including the trailing newline, the stable non-secret reference, configured-only status, ten-second deadline and replacement-failure preservation. Describe this slice's protected file storage.
- Pending documentation stage: `docs/deployment.md`, new “Memory credentials” section: document `~/.pyry/memory/credentials/`, running commands as the service user, service-user ownership, 0700 storage directories/0600 files, and separation from Claude account/login credentials. OS-store access and preference will be added by #3113.

## Security review

**Verdict:** PASS

- Trust boundaries: command allowlist, canonical selection and reference equality prevent path/account redirection.
- Tokens: printable bounded stdin only; opaque random identifiers; secrets confined to 0600 generation files. No secret-printing operation.
- File operations: pinned no-follow directories, descriptor type/owner/mode checks, exclusive 0600 creation and one atomic selection rename; no repair of unsafe storage.
- Subprocesses: none in production; test helper receives only the non-secret reference in argv.
- Cryptography: crypto/rand for identifiers; no cryptographic protocol or secret comparison.
- Network and I/O: no network; capped stdin and selection reads, context-aware polling for blocked stdin.
- Errors/logs: fixed diagnostics; no token/path/argument interpolation and no operational logs.
- Concurrency: directory flock serializes publication and cleanup; no detached save/read goroutines.
- Threat model: protects against other local users, not the service UID itself or privileged administrators. OS stores deferred to #3113.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-10
