# Background-task roster launching tool call (#2753)

## Files read

- `cmd/pyry/session_background_task_hold.go` → `Sink`, `BackgroundTaskRoster`, `cloneBackgroundTaskRoster`: upstream retention, leaf mutex and deep-copy ownership.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`: child-exit composition in both permission branches.
- `cmd/pyry/session_background_task_list.go` → `resolveBoundBackgroundTaskRoster`: bound-session isolation and shared bridge mapping.
- `internal/turnevent/event.go` → `BackgroundTaskStarted`, `BackgroundTask`: bounded source identifier and neutral roster row.
- `internal/protocol/interactive.go`, `interactive_test.go`, `testdata/background_task_roster.json` → `BackgroundTask`, roster golden round trip and worst-case envelope test.
- `internal/turnbridge/outbound.go`, `outbound_test.go` → `MapEvent`: live and connect-time projection.
- `internal/streamsup/parser.go` → `systemBackgroundTaskEntry`, `maxTaskRosterEntries`: three-field Claude input and eight-row cap.
- `internal/e2e/realclaude/testdata/{dropped_lines_v2.1.220,roster_after_finish_v2.1.280}.json`: adjacent start/roster frames in opposite orders.
- `CODING-STYLE.md`, `docs/knowledge/features/{protocol-package,turnevent-package,turnbridge-package,streamsup-package,sessions-package}.md`: package boundaries and lifecycle conventions.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: marshal decoded values back, pin empty-key presence and measure hostile escaping.
- `docs/knowledge/features/protocol-package-background-task-event-payloads.md`: row truncation and empty roster encoding.
- `docs/protocol-mobile.md` § Compatibility / Security model: additive field and untrusted rendered text.

## Context

Clients attaching mid-run need the launching tool call to join a roster row to older start history. Claude's roster carries no such identifier. This is one additive enrichment, not a new request or capability. No decision record is needed. No overlapping feature branches were found after fetching origin.

## Design

Add `ToolCallID string` to neutral and wire rows, always serializing `tool_call_id`, and forward it through `MapEvent`. Keep the parser's three-field input unchanged; update its cap commentary to distinguish the enriched event's 1280 field bytes per row, 10 KiB for eight rows (~15.6% of 65519), from escaped-wire measurement.

The per-session hold keeps a raw roster and an insertion-ordered slice of compact joins (task id, already-bounded tool call id, truncation bool). An independent `maxBackgroundTaskJoins = 16` bounds all joins: eight current-roster matches plus room for pending starts. Existing starts replace their join in place. At capacity, evict the oldest join not listed by the current roster; never evict a current-roster match. If all slots are protected, forget the incoming unmatched start. Each next roster prunes every join absent from its capped rows, including pending starts. This is housekeeping and never infers completion or synthesizes an event.

`Sink` records starts but forwards them unchanged and emits no extra roster. Roster emission and `BackgroundTaskRoster()` both enrich fresh deep copies of the raw roster, adding the start's truncation marker once after existing markers. Therefore roster-before-start emits empty but later reads gain the identifier without another roster. `childExited()` clears joins; since enriched rows are never cached, stale identifiers and markers disappear immediately. Compose this reset into the existing exit callback before both permission branches, preserving their callbacks.

## Concurrency model

The existing mutex protects roster and joins, including exit reset, concurrent writes and reads. Enrichment completes under the lock; downstream sinks run after unlocking. No new goroutines or lock-order edges. Tests join all workers.

## Error handling

No new error branch or I/O. Unseen, pruned, overflow-forgotten and previous-child starts yield an empty identifier. Source text remains producer-bounded; only the tool-call truncation bool is retained, not other start fields or slices.

## Testing strategy

Write failing tests before implementation. Replay committed start/roster bytes through `newSessionParser` in both observed orders and check live mapping, unchanged starts, exact event counts and bound-session resolver reads. Exercise bounded/truncated ids, repeated independent reads, marker preservation, pending overflow with all eight roster matches protected, pruning and task-id reuse, session isolation, and concurrent writes/reads/reset under race. Drive real factory child exit in both stdio and non-stdio modes, preserving the exit lane and testing replacement reuse. Widen bridge assertions and the golden fixture to populated and present-empty identifiers; fill the added field and marker in the envelope-cap test. Run touched-package race tests, `go vet ./...`, and `go build ./cmd/pyry` with output binary outside the worktree. Full-module gates belong to the verifier; no live capture is required.

## Open questions

None. Estimated total written work 650–750 lines, zero exported types/interfaces, three production mapping/wiring consumers, four acceptance criteria, and three retention decisions. All sizing limits pass.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` § `background_task_roster`, its element table and example, with `tool_call_id` and its row `truncated_fields` name. Explain the same-task started-event join; `""` means no retained match, including unseen or forgotten starts. Explain that roster-before-start can emit empty while a later connect-time read gains the id without a new roster line. Describe the 16-entry oldest-pending-first retention, next-roster pruning, protected current roster and child-lifetime reset. Add a dated § Changelog entry describing additive enrichment with no new request verb or capability.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `systemBackgroundTaskEntry` remains three fields; `emitBackgroundTaskStarted` bounds the only joined id. Joins describe provenance, not authority.
- [Tokens, errors/logs/telemetry] Identifiers remain untrusted text. The hold has no logger, retains only two bounded strings and a bool per join, and adds no errors or telemetry.
- [File operations, subprocesses] Production performs no new file access or child command construction; capture paths are fixed hermetic test inputs.
- [Cryptography] No new crypto or credential handling; existing encrypted frame transport is unchanged.
- [Network/I/O] The independent 16-entry limit bounds retained joins; hostile six-byte escaping is measured against the existing 65519-byte envelope cap.
- [Concurrency] One leaf mutex covers all updates and reset; sinks execute outside it, with independently owned emitted/read slices.
- [Threat model] Session-local ownership and child-exit reset prevent cross-session and stale-child attribution. Claude-authored descriptions stay render-only as required by the protocol security model; no new execution or authorization surface.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-04
