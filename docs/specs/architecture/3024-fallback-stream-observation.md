# #3024: bounded fallback stream observation

## Files read

- `cmd/pyry/reply_fallback.go` → `run`, `decodeReplyFallback`, `replyFallbackOutput`: isolation, deadlines and struct result semantics.
- `cmd/pyry/reply_fallback_test.go` → process helpers and output predicates: existing execution/privacy witnesses.
- `cmd/pyry/reply_suggestion.go` → `startFallbackLocked`: publication eligibility and generation guard remain intact.
- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `installSuggestCLI`, `readSuggestSource`, `suggestLifecycle`: independent readers and typed evidence gates.
- `internal/e2e/realclaude/reply_suggestion_staging_test.go` → wrapper staging/presence tests: surviving witnesses and exact forwarding.
- `docs/knowledge/features/e2e-realclaude.md` § “A suggestion frame alone cannot prove its source”: receipt does not establish publication or authentication.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` § “One isolated Haiku attempt”: preserve process/account isolation and timeout.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: bounded evidence and race checks.

## Context

The fallback currently retains a single JSON document. Stream progress must not spend the independent 4096-byte final-result allowance. The retained #2882 branch overlaps these files but adds no needed dependency; do not merge it.
Historical evidence remains unresolved: [PR #2896](https://github.com/pyrycode/pyrycode/pull/2896) and its [complete spec](https://github.com/pyrycode/pyrycode/blob/25d778fbd0e54753bd82a6d4eca9a097c996f6f4/docs/specs/architecture/2882-fallback-stdout-evidence.md). E/P/F/S: earlier #2882 6/4/2/0, #2873 6/5/1/0, #2896 6/3/3/0 (runs 1, 3, 6 failed). None is passing proof for this ticket.

## Design

Use a mutex-owned incremental NDJSON writer, retaining at most 4096 bytes of one envelope and discarding its remainder until the next newline. Complete bounded UTF-8 JSON envelopes recognize only `system/init`, `system/api_retry`, and `result`. EOF after observed Wait may finish a complete final envelope without a newline. Unknown, malformed, partial and oversized frames cannot create progress. A later bounded result remains decodable.
The production result uses `decodeReplyFallback` unchanged for result-field null/duplicate semantics, plus an explicit result type gate. Only successful, trimmed, valid text is returned. Whole-stream receipt saturates at 4097 independently of final-result bytes and predicates. The wrapper mirrors the bounded decoder while forwarding each original chunk unchanged; its persistent producer path remains separate.
Recognized progress tracks a fixed event label, timestamp, init boolean, validated source category and counted decoded retry events. `none` and `ANTHROPIC_API_KEY` are the only reported non-unknown sources. Init/source never prove authentication or readiness. Zero observed retries differs from absent retry metadata. Lifecycle logs carry both current observations and a frozen pre-cancellation snapshot. Consumer metadata requires typed presence and consistent applicability; missing/invalid evidence stays unknown.

## Concurrency model

One writer mutex protects parsing, incremental observations and cancellation snapshots. `cmd.Cancel` freezes once under that mutex before sending the first process-group signal; both the exec context watcher and explicit cancellation use it. Subsequent observations affect only current state. Existing account lookup and Wait goroutines retain their cancellation/shutdown paths; no new goroutine. Process state and completion-dependent predicates remain unread/unknown without Wait receipt.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Chunk split/coalesced frames, skipped frame then later result | `TestReplyFallbackStream` |
| Cancellation before any event then late input/repeated cancellation | `TestReplyFallbackStreamFreeze` |
| Cancellation after progress then late retry/result/repeated cancellation | `TestReplyFallbackStreamFreeze` |
| Unobserved Wait with known incremental progress | `TestReplyFallbackOutputUnknownWait` |
| Wrapper/child correlation and repeated/malformed metadata | `TestSuggestSourceCapturePresence` |

## Error handling

Reject unusable/oversized result envelopes without truncation. Preserve generic fallback failure, 9.8-second context, 100-ms cancellation Wait grace and group kill. Neither incomplete Wait nor result without wire publication proves forwarding loss. Stderr capture (#3025), cancellation stabilization (#3020), and missing-suggestion correction (#2923) remain outside scope.

## Testing strategy

Write synthetic stream/freeze/privacy tests first; adapt existing production and wrapper fixtures to typed stream results. Check result null/duplicate/type errors, source allowlist, long progress, zero/missing retries, metadata gates and unobserved Wait. Run `go test -race ./cmd/pyry/...`, tagged offline suggestion race tests, `go vet ./...`, and `go build ./cmd/pyry`. Verifier owns full hermetic gate. After fixed instrumentation/checks, separately commit and push the declaration before exactly six sequential authenticated targeted launches on its unchanged checkout. Record every outcome and safe correlated observations here; no retries/replacements. Dispatcher owns the separate full live gate.

## Open questions

None. Final-result bounds are independent of receipt saturation; source is reported evidence only.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/e2e-realclaude.md`, “A suggestion frame alone cannot prove its source”, and `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`, “One isolated Haiku attempt”. State stream format and independent final-result bounds, wrapper versus daemon readers, reported source versus authenticated readiness, frozen pre-cancellation versus post-Wait observations, and unknown versus observed zero. Link this counted batch alongside every unresolved historical failure; all-pass means non-reproduction only. Never include raw output or credentials.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: bounded UTF-8/JSON type gates precede progress/result recognition; wrapper evidence separately requires typed presence and PID correlation.
- Tokens: no new credential handling; selected-account isolation remains in `run`. Only validated source categories leave the decoder.
- Files: preserve private temporary cwd and 0600 metadata, fixed harness paths and 64-KiB evidence limit; no new externally supplied paths.
- Subprocesses: literal CLI arguments and JSON stdin, existing scrubbed environment and process-group cancellation; no shell interpretation added.
- Cryptography/network: no cryptographic or network changes; existing authenticated harness remains responsible for transport.
- I/O: discard oversized envelopes incrementally; memory is bounded independently of stream length.
- Logs: MUST FIX in implementation: never emit envelope contents, raw errors or arbitrary event/source strings; fixed labels/scalars only.
- Concurrency: freeze and writer use one mutex; freeze precedes signal, repeat cancellation cannot replace the snapshot.
- Threat model: child output is untrusted and observation proves neither authentication nor readiness. Missing-suggestion correction belongs to #2923; stderr capture to #3025.

The logging requirement is incorporated into Design, resolving the finding before commit.
**Reviewer:** builder (self-review). **Date:** 2026-10-09.
