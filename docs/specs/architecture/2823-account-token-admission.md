# #2823 — Per-attempt account token admission

## Files read

- `internal/streamsup/runner.go` → `Config`, `beginSpawn`, `Run`, `spawnAndWait`, `spawnEnv`, `Restart`, `RestartFresh`: iteration cancellation, session snapshots, child environment and setup-failure lifecycle.
- `internal/streamsup/helper_test.go` → `TestMain`, `helperChild`: actual-child harness dispatched before flag parsing.
- `internal/streamsup/runner_test.go` → `helperRunCfg`, `runInBackground`, `safeBuffer`: hermetic lifecycle scaffolding.
- `internal/streamsup/parser_test.go` → `logRecorder`: scan record messages and attribute keys/values with a positive diagnostic assertion.
- `docs/knowledge/features/streamsup-package.md` and `streamsup-package-supervise-loop-run.md` → cancellation publication and the successful-start-only `firstRun` gate.
- `docs/knowledge/features/streamsup-package-testing.md` → wait for the child's own witness before restarting; `onSpawn` does not prove child code ran.
- `docs/knowledge/features/development-verification.md` → source and execution evidence boundaries.
- `CODING-STYLE.md`: stdlib tests, structured logs and cancellable operations.

## Context

Configured account reads must fail closed rather than launch Claude with inherited credentials. This slice defines launch admission; #2824 owns instance configuration/file reading and #2825 owns 1Password. No decision record is needed.

## Design

Add optional `Config.AccountTokenProvider`, a function type accepting `context.Context` and returning token text, an `AccountTokenFailure` category and a private error. Its contract requires cancellation cooperation and forbids the reader from logging secrets. A non-nil error or nonempty failure category rejects, even alongside a token. Categories are an allowlisted string type; provider-supplied unknown strings become a generic safe rejection. No provider retains the existing environment/launch behavior.

`spawnAndWait` resolves before allocating subprocess resources, after `beginSpawn` has published cancellation and released its locks. A read-only child context has a ten-second timeout and is cancelled immediately after the read. Check its error before accepting any returned token, including late success. The admitted process continues under the iteration context, so the read deadline cannot kill it.

Remove at most one trailing LF or CRLF; reject empty text, NUL and any Unicode whitespace. Successful admission creates a fresh environment slice, removes all exact `CLAUDE_CODE_OAUTH_TOKEN=` entries from inherited plus per-spawn additions, and appends exactly one token entry. Preserve other values and the snapshotted session environment. Neither caller configuration nor parent environment changes. Token text is never stored in runner state.

Return only daemon-authored errors for rejection (read failure, empty output, invalid output, timeout, cancellation or generic rejection). Reuse `Run`'s existing safe error log, exit callback, backoff and crash-episode accounting. Report `started=false`, leaving the create/resume latch unchanged.

No concurrent feature branch overlaps the proposed existing files at planning time. Estimated total written work: 650 lines, two exported types, zero consumer updates, five acceptance criteria, six admission rejection branches. All fit the builder limits; the #2169 analogue is smaller because it has no cancellable reader or rejection matrix.

## Concurrency model

The provider runs synchronously on the `Run` goroutine with no runner lock held. Existing iteration cancellation handles `Restart`, `RestartFresh` and parent shutdown. No new goroutine or token cache. Provider implementations must return when context cancellation occurs; the runner also rejects any result returned after cancellation/deadline. Existing subprocess teardown remains intact.

## Error handling

Never inspect, format or wrap private provider errors. Never put raw output or unknown category strings in errors/logs/events/configuration. Cancellation/expiry takes precedence over reader results. Ordinary rejection retries as setup failure; parent shutdown returns the parent context error. A successful retry uses the current spawn inputs, and no refused attempt counts as a child start.

## Testing strategy

Write tests first and observe the missing admission behavior. Extend `helperChild` with a JSON environment/argv witness and stdin-controlled crash; wait for complete child-written records before lifecycle operations.

- Table-driven normalization/rejection with private-error/token/category sentinels; positive safe-category checks and scan captured record keys/values/messages and exposed errors for leaks.
- Actual children prove absent provider inheritance, configured/inherited replacement, exactly one token, preserved session/unrelated entries and parent/config immutability.
- Crash and deliberate restart prove fresh tokens and create/resume forms; rejection/recovery proves backoff/callback behavior, no child witness for rejection and first admitted create form.
- Blocked reads exercise restart, fresh restart and shutdown, including late success after cancellation. Check deadlines and expiration; prove the admitted child's lifetime is independent of the read deadline.
- Run `go test -race ./internal/streamsup`, `go vet ./...` and `go build ./cmd/pyry` (output outside the worktree). The dispatcher owns `make check`, including the full-module suite requested by the issue.

## Open questions

None. File/1Password source selection remains outside this package.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/streamsup-package-supervise-loop-run.md`, under “Supervise loop (`Run`)” and its “`firstRun` gate” subsection, describe optional per-attempt token admission, ten-second read-only deadline, cancellation, safe rejection diagnostics and rejection preserving the create/resume latch. Source configuration belongs to the later tickets.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `spawnAndWait` admits only normalized tokens after checking reader status and context; safe category allowlisting prevents untrusted text crossing to diagnostics.
- [Tokens] Tokens remain local to each attempt and its fresh child environment; no runner cache. Failed reads cannot fall back to inherited credentials. Reader error/output is private even when returned with a token.
- [File operations] OUT OF SCOPE: file paths, modes and symlink policy belong to #2824; this package does no credential file I/O.
- [Subprocesses] Existing direct `exec.CommandContext` and teardown are preserved; successful admission replaces every inherited/configured token entry before `Start`.
- [Cryptography] No generation, comparison against a stored secret or cryptographic primitive is introduced.
- [Network and I/O] The reader gets a ten-second cancellable context; source-specific size bounds/network behavior are owned by #2824/#2825.
- [Errors, logs, telemetry] Only fixed category text enters existing rejection logs. Tests scan messages plus keys/values with positive controls; private errors are neither wrapped nor formatted.
- [Concurrency] Read occurs after `beginSpawn` releases locks and publishes cancellation. No new goroutine; late results are checked against the read context before admission.
- [Threat model] This slice prevents accidental default-account use on credential rejection; source trust and account selection are deferred to #2824/#2825. No relay/mobile contract changes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05
