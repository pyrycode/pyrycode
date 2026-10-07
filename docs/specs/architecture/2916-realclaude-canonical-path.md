# #2916: real-Claude canonical cwd and trust-control resolution

## Files read

- `internal/e2e/realclaude/session_transcript_probe_test.go` → `TestRealClaude_TurnlessSessionIDTranscript`: canonical child cwd and empirical transcript-directory comparison.
- `internal/e2e/realclaude/resume_absent_transcript_probe_test.go` → `TestRealClaude_ResumeAbsentTranscript`: shared canonical cwd, empirical lookup and pre/post absence evidence.
- `internal/e2e/realclaude/session_error_recovery_test.go` → `testSessionErrorRecovery`: resolved workspace for retained/dropped recovery arms.
- `internal/e2e/realclaude/claude_md_external_includes_test.go` → `TestClaudeMdExternalIncludes_SubfolderChildGetsRootImports`: filesystem-form control key and differential include assertions.
- `internal/canonicalpath/path.go` → `Resolve`; `internal/agentrun/workdir.go` → `ResolveWorkdir`: identical absolute, symlink-resolved, best-effort on-disk-cased paths and error identity.
- `internal/streamsup/runner.go` → `New`; `internal/agentrun/trust/trust.go` → `markWorkdirTrustedIn`: production already resolves through `canonicalpath.Resolve`.
- `docs/knowledge/features/e2e-realclaude.md`, `e2e-realclaude-interactive-stream-model-announced-test-go.md` and `session-transcript-and-resume-probe.md`: preserve error checks and empirically located transcripts; forced termination must not manufacture verdicts.
- `docs/knowledge/features/development-verification.md` → “Test execution and artifact survival”: compilation and offline success cannot stand in for executed live arms.

## Change

Replace the four `agentrun.ResolveWorkdir` calls and imports with `canonicalpath.Resolve`, updating the associated comments and control-arm diagnostic. Preserve all setup, skip/opt-in policies, trust state, empirical lookup, differential assertions and retained/dropped recovery assertions. This matches the merged production migration and prepares for #2917's resolver deletion. No new types, state, error branches or production files. One deliverable, four consumer sites, two acceptance criteria and approximately 60 written lines including this plan, below all builder limits. No overlapping target-file edits on fetched feature branches #1424, #2873 or #2882.

## Testing strategy

Use existing `TestTurnlessTranscriptVerdict`, `TestResumeAbsentVerdict` and `TestResumeProbeArgsIsRespawnShape` under `-race -tags e2e_realclaude`; compile the entire tagged package through that run and tagged vet. Confirm no old-resolver references remain in the package and review the diff for preserved evidence. Run `go vet ./...` and `go build` with output outside the worktree. The verifier owns `make check` and the full-module suite. The dispatcher owns the pending live-Claude gate: report executed/passed counts and skip reasons, require both `marked_root_expands_external_import` and `unapproved_root_drops_external_import` in `TestClaudeMdExternalIncludes_SubfolderChildGetsRootImports` to execute and pass, and reject all-skipped success. Keep `needs-real-claude` on the issue.
