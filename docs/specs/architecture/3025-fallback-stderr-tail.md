# Bounded daemon-only fallback stderr (#3025)

## Files read
- `cmd/pyry/reply_fallback.go`: `replyFallback.run`, `replyFallbackOutput`, and `replyFallbackStream` own launch, Wait and frozen stdout observations.
- `cmd/pyry/reply_fallback_test.go`: helper-process fixtures and cancellation/output checks preserve isolation and publication contracts.
- `internal/streamsup/stderr_tail.go`: `stderrTail`, `captureStderr`, and `daemonLogOnly` supply retention, private-pipe and quoting contracts, but not fallback drain timing.
- `internal/control/logs.go`: `SlogTee` replaces marked attribute values in the ring, the source for logs and phone bundles.
- `internal/debugbundle/bundle.go`: `Assemble` packages the ring snapshot without further redaction.
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go`: `suggestLifecycle` needs quote-aware field selection without changing typed-presence/consistency gates.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go`: offline producer and consumer fixtures must never print captured values.
- `internal/e2e/realclaude/harness_daemon_test.go` and daemon-launch variants: `lockedBuffer.String` and direct stderr tees currently export primary logs; remove tees and omit the final fallback tail field in report snapshots.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`, “One isolated Haiku attempt”; `control-plane.md`, “Keeping a value out of the log ring”; `e2e-realclaude-test-infrastructure.md`, “Test infrastructure”; `debugbundle-package.md`; `development-verification.md`: independent observation boundaries, destination-specific raw exception, and content-free failure assertions.

## Context
Retain local error evidence without changing #3024's result/progress behavior. Raw stderr may contain sensitive values: the owner's exception permits it only in the primary local daemon log. Missing suggestions (#2923) and cancellation-test stabilization (#3020) remain separate.
Historical evidence stays in the [original spec](https://github.com/pyrycode/pyrycode/blob/25d778fbd0e54753bd82a6d4eca9a097c996f6f4/docs/specs/architecture/2882-fallback-stdout-evidence.md) and [#3024 Six-run results](3024-fallback-stream-observation.md#six-run-results), including operator disposition and unresolved failures. No historical or new authenticated batch is rerun here. Original branches/spec remain untouched; current main already contains the needed #2882 stdout work through #3024.

## Design
One local private capture owns an `os.Pipe`, a bounded end-weighted tail, and reader completion. Give the child the write `*os.File` directly so Wait has no stderr copier to await. Setup failure leaves stderr discarded and records only unavailable metadata. Retain the last 1024 bytes, trim trailing CR/LF, then keep the last five newline-delimited lines.
`started(bool)` closes parent write ownership and starts the reader only on successful launch. `finish(ctx)` joins an already completed reader or allows at most 100 ms after normal Wait, selected against the attempt context; cancellation adds no drain grace. Close/join any remaining reader before snapshot/logging. Reader EOF is independently observed, never inferred from Wait or forced closure. Available empty/nonempty tails are distinguished locally; unavailable capture has unknown reader predicates. Export only availability/reader booleans, with raw tail as the final marked structured value using the streamsup `LogDaemonOnly`/`MarshalText` contract.
`suggestLifecycle` scans complete text-handler records with quote/escape awareness; quoted values cannot supply PID or normalized scalar fields. Preserve all current applicability and consistency gates and add independently validated stderr-reader booleans. Live harnesses retain logs in memory but never tee primary output into reports; report snapshots omit the final tail attribute, including incomplete records.
No new public type, shared production API, dependency or result classification. Sketch/plan sizing: approximately 650 written lines, zero exported types/interfaces, seven harness wiring sites, four acceptance criteria, fewer than ten capture/error branches. Historical #2882 overlaps these files but supplies no needed unmerged behavior; #3031 does not overlap.

## Concurrency model
One reader per successful capture, one existing Wait goroutine, and the existing context watcher. A mutex protects bounded tail writes; reader completion synchronizes EOF/error receipt. Forced close unblocks the pipe and cleanup joins the reader even when Wait remains unobserved. Wait alone owns process state; no new goroutine reads it.

## State transitions and identity reuse
| Event | Race check |
| --- | --- |
| Successful/nonzero helper exit, empty or large stderr | `TestReplyFallbackStderrProcess` |
| Cancellation and repeated context cleanup, unobserved Wait | `TestReplyFallbackStderrProcess` |
| Descendant retains stderr after helper exit | `TestReplyFallbackStderrProcess` |
| Setup failure and failed Start | `TestReplyFallbackStderrCleanup` |
| EOF, forced closure, read failure and concurrent writes | `TestReplyFallbackStderrCleanup` / `TestReplyFallbackStderrTail` |
| Quoted forged PID/scalars and incomplete records | `TestSuggestLifecycleQuotedTail` |
No capture or PID is reused across attempts.

## Error handling
Pipe setup failure cannot reject or alter launch. Read failure/forced close yields an observed partial snapshot; raw errors are never logged. Cancellation may return without reaping, leaving completion predicates unknown. All readers and file ownership are cleaned up on failed Start, normal return, and cancellation.

## Testing strategy
Write synthetic tests first and observe their failure. Drive actual fallback capture through `control.SlogTee`; inspect primary text attributes only in memory, check ring-backed logs and decompressed `debugbundle.Assemble` output for exclusion. Cover byte/line suffix retention, empty/unavailable/partial snapshots, hostile quoting/field shapes, success/nonzero/cancel/unobserved Wait and stderr-holding descendants. Verify closed descriptors and joined readers with content-free assertions. Run production package race tests, tagged offline suggestion/harness race tests, `go vet ./...`, and a scratch-output build. Verifier owns the full hermetic gate; dispatcher owns the counted full live gate under `needs-real-claude`.

## Open questions
None. The local exception is explicit; stderr EOF and Wait receipt are separate observations, not a complete child error report.

## Documentation handoff
Satisfied by the documentation stage:
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`, “One isolated Haiku attempt”.
- `docs/knowledge/features/control-plane.md`, “Keeping a value out of the log ring”.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`, “A suggestion frame alone cannot prove its source”, linked from `e2e-realclaude.md`.
Each must state the end-weighted 1024-byte/five-line local raw-stderr exception, possible sensitive values, quoting and mandatory ring/phone/debug/report exclusion; explain unavailable/empty/partial capture, independently observed reader EOF versus Wait, and frozen pre-cancellation stdout versus post-Wait reader boundaries. Preserve historical links/counts, operator disposition and unresolved failures; never copy raw stderr or credentials.

## Security review
**Verdict:** PASS
- Trust boundaries/logs: raw subprocess data stays solely in a marked, text-marshaled attribute value; quote-aware consumer ignores its contents. MUST FIX resolved in design: existing live-harness tees and failure snapshots must exclude this newly captured field.
- Tokens: no credential changes; stderr can contain secrets, hence mandatory destination and report exclusion. No text-derived category/hash is exported.
- File operations: private anonymous pipe only; existing temporary-directory policy remains.
- Subprocesses: preserve fixed argv, JSON stdin, account/isolation flags, process-group cancellation and Wait ownership. Descendant-held pipes cannot change classification.
- Cryptography/network: no changes to keys, randomness, wire input or network operations.
- I/O/concurrency: fixed 1024-byte retention; normal drainage at most 100 ms inside the existing context, zero cancellation drain grace, forced closure/join on all paths.
- Threat model: no phone-visible stderr or new remote surface. #2923 and #3020 remain out of scope.
**Reviewer:** builder self-review. **Date:** 2026-10-09.

## Revisions
- 2026-10-09: final consumer map comprises `harness_daemon_test.go` (`spawnBootstrapDaemonBinary`, `lockedBuffer.String`), `harness_modal_test.go`, `codex_conversation_live_test.go`, `interactive_stream_resume_after_eviction_test.go`, `interactive_stream_clear_wrapup_skipped_test.go`, and `interactive_stream_model_announced_test.go`. Six daemon-launch tees become memory-only; report snapshots omit the final `stderr_tail` suffix even before a complete record arrives. The production final-attribute ordering is part of this report-exclusion contract.
- 2026-10-09: add per-instance pipe/Wait test seams to `replyFallback`; defaults remain `os.Pipe` and `cmd.Wait`. Delayed Wait receipt exercises unknown process/output predicates while the capture reader is joined. `TestReplyFallbackStderrFailedStart` covers actual launch failure; `TestSuggestStderrObservationGates` preserves unknown/typed/consistency gates independently of Wait. The escaped-descendant cancellation arm proves no added drain grace. No product behavior expands.
- Final sizing: approximately 650 added/removed lines including this plan, zero exported types/interfaces, six wiring sites, four criteria, and fewer than ten capture/error branches. `runSupervisor` remains the single production constructor, with its original logger/account provider and fallback publication wiring.
