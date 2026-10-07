# #2914 — CLI confinement and transcript resolver migration

## Files read

- `internal/canonicalpath/path.go` → `Resolve`, `canonicalCase`, `matchEntry`: merged #2911 preserves absolute, symlink and on-disk-case resolution, including empty input and wrapped errors.
- `internal/agentrun/workdir.go` → `ResolveWorkdir`: confirms the replacement uses the same recipe.
- `cmd/pyry/attach_file.go` → `confineFile`, `confineToRoot`, `readChecked`: resolve both ends, enforce `withinDir`, and preserve checked-file identity and static errors.
- `cmd/pyry/workspace_file.go` → `resolveReadFolders`, `withWorkdirReadFolder`, `confineToAnyRoot`: startup-fixed roots, workspace-relative targets and home/root guards.
- `cmd/pyry/streamsup_runner.go` → `streamClaudeSessionsDir`, `ClaudeSessionsDir`, `SetSpawnWorkDir`: transcript derivation and related resolver comments.
- `cmd/pyry/session_transcript_dir.go` → `sessionTranscriptDir`: explain canonical resolution without re-deriving the runner's folder.
- `cmd/pyry/streamsup_runner_test.go` → `TestMapStreamsupConfig_ClaudeSessionsDir`, `TestMapStreamsupConfig_ClaudeSessionsDirDegrades`, `TestStreamClaudeSessionsDir_NoHome`: existing results and refusal arms.
- `cmd/pyry/workspace_file_test.go` → `TestResolveReadFolders`, `TestWithWorkdirReadFolder`, configured-folder reader tests: rejection, deduplication, guards and root-swap coverage.
- `cmd/pyry/attach_file_test.go` → `TestFileAttacher_Confinement`, `TestConfineFile_SwapBetweenCheckAndRead`, `TestReadChecked_ByteBound`: containment, static errors, regular files and identity checks.
- `internal/streamsup/runner.go` → `New`, `SetSpawnWorkDir`: the runner currently uses the old equivalent resolver; its migration is a separate ticket.
- `docs/knowledge/features/canonicalpath-package.md` § Path and error contract: case correction follows successful symlink resolution; canonical paths are metadata, not stable handles.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md` § Operator-named folders and The daemon's own working folder: never re-resolve startup roots or broaden relative lookup.
- `docs/knowledge/features/control-plane-attachment-file-confine-and-store-a-claude-named-path.md` § TOCTOU and Refusal reasons: retain `SameFile` and content-free failures.
- `docs/knowledge/features/cli-verb-dispatch.md`, `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: CLI conventions and existing behavioral evidence.
- `docs/protocol-mobile.md` § Security model: paired clients must not escape filesystem or disclosure boundaries.

## Change

Replace the six production and three test calls to `agentrun.ResolveWorkdir` in `cmd/pyry` with `canonicalpath.Resolve`, replace their imports, and update affected resolver comments. The runner's own resolution is described as the same recipe while its separate migration is pending. This is one consumer migration with no new types, state, errors or goroutines. Keep all guards, joining, containment, startup-root capture, stat/read checks, logging and transcript refusal behavior intact. Fetching and comparing other feature branches found no overlap in these files.

Sizing before commit: approximately 160 written lines including this plan, six production consumers and three test consumers, zero new exported types/interfaces, two acceptance criteria, and zero new reject branches. All five limits hold.

## Testing strategy

Migrate the existing test helpers first and run the confinement, workspace-reader and transcript-directory tests as a baseline. This behavior-preserving migration has no new behavior requiring a new failing assertion. Repeat those assertions after switching production consumers, then run `go test -race ./cmd/pyry/...`, `go vet ./...` and `go build ./cmd/pyry` with output/artifacts outside the worktree. Confirm a source search finds no old resolver calls in `cmd/pyry`. The dispatcher/verifier owns `make check` and the full-module suite.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/v2-session-manager-state-machine-inbound-read-workspace-file-workspacefileread.md`, the “Operator-named folders” and “The daemon's own working folder” sections must name `canonicalpath.Resolve` instead of `agentrun.ResolveWorkdir`, preserving the confinement rules.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. Client/model paths still pass `confineFile` or `confineToRoot` and `withinDir`; configured roots remain operator-controlled startup state.
- [Tokens, secrets, credentials] No findings. No credential handling changes; `workspaceFileReader` keeps requested/resolved secret-leaf checks and content-free refusals.
- [File operations] No findings. Both root and target use the equivalent resolver; relative targets join the workspace, startup roots stay fixed, regular-file checks precede opening, and `readChecked` retains `O_NOFOLLOW`, `O_NONBLOCK` and `SameFile`. No writes or permission changes are introduced.
- [Subprocesses] No findings. Only transcript path derivation changes its resolver dependency; argv, environment and child shutdown are untouched.
- [Cryptography] No findings. No primitives, keys, nonces or comparisons change; existing transfer IDs continue through `conversations.NewID`.
- [Network and I/O] No findings. No socket boundary changes; the existing `maxBytes` stat/read bounds remain in `readChecked`.
- [Errors, logs, telemetry] No findings. Resolver errors retain path-bearing context but are discarded in favor of the existing static errors/reasons. No new logs or client-visible paths.
- [Concurrency] No findings. Resolution is synchronous; no goroutines, locks or shutdown paths change.
- [Threat model] No findings. Paired-client symlink escape, sibling containment and post-check inode swaps retain their existing checks; this migration introduces no new access policy.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07

## Revisions

- 2026-10-07: The Files read entry overstates existing configured-folder tests: they cover configured-root results and refusals, but do not simulate a post-startup root swap. The fixed-root contract is preserved by leaving `confineToAnyRoot` and its direct `confineToRoot` delegation unchanged; no root re-resolution is introduced.
