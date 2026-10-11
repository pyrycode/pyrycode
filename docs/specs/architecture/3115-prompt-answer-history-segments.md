# #3115 — Read durable prompt answers from history segments

## Files read
- `internal/e2e/realclaude/prompt_answer_history_test.go` → `requirePromptAnswerHistory`: existing raw-fact checks, five-second deadline and partial-line tolerance.
- `internal/history/segment.go` → `segmentName`, `parseSegmentName`, `listSegments`: exact 20-character uint64 decimal names and regular-file eligibility.
- `internal/history/log.go` → `Entry`, `Metadata`, `AppendWithMetadata`: real committed regression data and session/shown metadata.
- `internal/thread/cache.go` → `Store.writeCacheFile`; `internal/thread/store.go` → `Store.checkpoint`: metadata publication renames `.thread-*` beside history segments.
- `internal/e2e/realclaude/interactive_stream_question_answer_test.go` → `TestInteractiveStreamQuestionAnswer`: retain question, parked model update, dismissal, chosen continuation and subsequent model-announcement proof.
- `internal/e2e/realclaude/interactive_stream_question_refusal_test.go`, `interactive_stream_modal_resolution_test.go`, `interactive_stream_permission_deny_test.go` → the other three helper callers: no caller migration.
- `docs/knowledge/features/e2e-realclaude.md`, `e2e-realclaude-interactive-stream-question-answer-test-go.md`: tagged offline checks do not prove live execution; the existing event drain must remain unchanged.
- `docs/knowledge/features/history-package.md`, `thread-package.md`: regular segment leaves are durable input; cache/recovery/run metadata shares their directory.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: demonstrate the regression against the old behavior, preserve evidence boundaries and use deterministic stdlib tests.

## Context
The live question-answer flow succeeds, then its durable assertion intermittently opens a vanished thread publication file. The reader globs all history-directory entries. Repair this single test-reader behavior; no production or protocol change and no decision record are needed.

Sizing: approximately 300 written lines including this plan, zero exported types/interfaces, zero consumer updates, four acceptance criteria, no new state machine. The original helper from #2973 is 68 lines; most added work is deterministic coverage. Recount before committing: all five builder limits remain below their boundaries. No other fetched feature branch touches either planned test file.

## Design
Keep `requirePromptAnswerHistory` and all four callers compatible. Extract only enumeration, reads and entry decoding into `readPromptAnswerHistory(fsys fs.FS) ([]history.Entry, error)`, rooted with `os.DirFS` at the same harness history directory. Accept only regular entries whose names satisfy `parseSegmentName`'s exact prefix, suffix, width and uint64 parsing contract. Keep this private selection in the test package rather than exporting production internals.

The reader returns decoded `prompt_answered` entries. The existing helper retains correlation selection, ownership/decision/source/timestamp/shown/context checks, optional asking-session checks, exactly-one counting, duplicate failure, five-second deadline and 20 ms retry. Malformed/partial entry lines retain their current retry behavior. A missing directory yields no entries as the old glob did; any other listing failure and every selected-segment read failure is returned and fails the helper.

Add `internal/e2e/realclaude/prompt_answer_history_reader_test.go` with the existing build tag. Use a real history store to commit a valid answer with known provenance. A private filesystem wrapper returns an actual directory snapshot, then synchronously renames/removes a listed leaf before the reader opens anything. No goroutine, global seam, timing sleep, credential, daemon or fixture migration is needed.

## Concurrency model
No new goroutines or shared mutable state. Directory snapshots can outlive thread metadata publication; selection prevents opening those temporary leaves. The selected segment read remains fallible and is never retried by ignoring its error. Each regression owns its filesystem and mutation callback.

## State transitions and identity reuse
| Event | Race-detector regression |
| --- | --- |
| `.thread-*` renamed to cache metadata or removed after enumeration | `TestPromptAnswerHistoryReaderPublication` |
| Selected segment removed after enumeration | `TestPromptAnswerHistoryReaderSegmentReadFailure` |
| Partial trailing answer later completed; repeated directory reads | `TestPromptAnswerHistoryReaderPartialTail` |
The reader owns no session/correlation lifecycle. Existing resolution and duplicate-counting assertions remain unchanged.

## Error handling
Non-segment entries, directories and symlinks are excluded before opening and cannot contribute facts. Invalid-width, nondecimal and overflowing numeric fields are excluded. Preserve errors from eligible files, including disappearance, with their filename as context. Preserve other directory errors. Decoder tolerance remains limited to the existing entry-line behavior.

## Testing strategy
Write the regressions first and show the publication case failing with unfiltered enumeration before implementing selection. Assert real answer entry bytes/metadata survive reading; excluded files containing that same valid answer cannot supply one. Cover segment-name boundaries, nonregular leaves, multi-segment duplicate preservation, absent directories, directory errors, selected read errors and partial-tail rereads. Exercise the unchanged assertion helper on the valid committed fixture.

Run credential-free tagged tests with `go test -tags e2e_realclaude -race -count=1 -v -run '^TestPromptAnswerHistoryReader' ./internal/e2e/realclaude/...`, scoped default package race tests, tagged source-citation checks/vet, `go vet ./...` and `go build -o /tmp/builder-3115/pyry ./cmd/pyry`. The verifier owns full `make check`, including the full-module race suite. The dispatcher owns normal complete `make e2e-realclaude`; pending proof must report actual question-answer execution plus executed/pass/fail/skip counts and skip reasons. Keep `needs-real-claude` on the issue.

## Open questions
None.

## Documentation handoff
Pending documentation stage: in `docs/knowledge/features/e2e-realclaude-interactive-stream-question-answer-test-go.md`, under “Lessons that outlive this ticket,” explain that durable-answer assertions inspect only history segments: thread cache/recovery/run metadata shares the directory, and `.thread-*` publication files can disappear between enumeration and opening. State that excluding those entries preserves the answer assertions and deadline rather than masking segment read errors.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `readPromptAnswerHistory` restricts filesystem input to regular committed segment names; `requirePromptAnswerHistory` retains all durable ownership, remote-source, provenance and visibility checks.
- Tokens/credentials: offline fixtures contain synthetic answer data; no login, token generation/storage or credential access is added.
- File operations: read only within the existing isolated harness directory. Exact leaf naming excludes traversal; directory-entry types exclude symlinks/directories. Regression files use private temp directories and `0600` writes. No change to production publication or modes.
- Subprocesses/cryptography: no new child process, shell, key, nonce or comparison with secrets.
- Network/I/O: no new network input or serving path. Existing live budgets and 16 KiB answer-context assertion remain; the test reader keeps existing whole-file reads of local bounded history segments.
- Errors/logs: file errors remain local test failures; no new operational logs, payload logging, metrics or telemetry.
- Concurrency: no new locks/goroutines. Deterministic snapshot mutation models publication without timing races; eligible segment disappearance stays an error.
- Threat model: production authorization and encrypted relay contracts are untouched; no new security capability is claimed by the test reader.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11
