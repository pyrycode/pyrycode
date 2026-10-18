# #3593 — ignore duplicate and bookkeeping Codex notifications

## Files read

- `internal/codexsup/translate.go` → `ignoredMethods`, `unrecognizedMethods`, `Translate`: exact method membership controls suppression; preserve mapped methods and item lanes.
- `internal/codexsup/translate_test.go` → `TestMethodListsPartitionServerNotifications`, `TestTranslateUnrecognizedLanes`: existing partition and fallback coverage; no moved-method expectations need updating.
- `internal/codexsup/translate_moderation_test.go` → existing selective metadata coverage remains applicable.
- `internal/codexsup/methods.go` → `serverNotifications`: registered notification vocabulary.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `ServerNotification`: confirms the 43 names, including exactly 11 realtime methods, and representative params.
- `docs/knowledge/features/codexsup-package.md` § The translator's three-list partition: preserve exact schema partition and unknown visibility; completed tool items already supply output.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification-protocol-boundaries.md`: focused table-driven behavior tests and untrusted protocol boundaries.

## Change

Move the ticket's 43 notifications from `unrecognizedMethods` to `ignoredMethods`, with one short comment for each of the four groups: completed-item duplicates, unrequested thread bookkeeping, output of unmade requests, and features outside text chat. `Translate` already returns no events for exact ignored membership, so no dispatch logic changes. Enumerate the 11 pinned realtime methods; unknown realtime methods retain the existing bounded Unrecognized fallback. Keep `deprecationNotice`, `guardianWarning`, selective moderation metadata and all other method/item lanes as they stand.

Concurrent branches #3583, #3584 and #3585 touch `translate.go` (and #3583 touches `translate_test.go`); their web-search, error and MCP mappings are independent. Keep edits local to the lists and put the focused tests in `translate_ignored_test.go` to reduce overlap. None of these branches is required by this design.

Sizing: one behavior, two acceptance criteria, approximately 180 written lines including plan/tests/list moves; zero new exported types, consumer updates or error branches. All five builder limits remain below their boundaries.

## Testing strategy

Write `TestTranslateIgnoredNotificationGroups` first and observe failures before the list move. Pin all 43 names independently of production, assert exclusive ignored membership and no events, and exercise a populated representative payload from each group. Add unknown ordinary/realtime method fallback assertions. Run the affected `internal/codexsup` package under the race detector on Linux, including the existing partition, moderation, warning, item and capture replay assertions; then run `go vet ./...` and `go build ./cmd/pyry` (output outside the worktree). Full acceptance gates belong to the dispatcher; no live capture is needed. macOS is outside this ticket's verification scope.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/codexsup-package.md`, under **The translator's three-list partition**, describe the four newly ignored groups and state that they emit no transcript events. Explain that realtime suppression enumerates the pinned schema methods and leaves unknown methods visible.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `Translate` matches exact constant names through `ignoredSet`; ignored peer params are never interpreted or promoted to trusted state. Tests must pin unknown realtime visibility to prevent accidentally suppressing future security-relevant notifications.
- [Tokens, secrets, credentials] The list move adds no credential handling. Ignored payloads produce no transcript events; no payload is newly logged or persisted.
- [File operations] `fs/changed` and file-change notifications are ignored as data; the change adds no filesystem operations or path interpretation.
- [Subprocesses] Output and terminal-interaction notifications invoke no command; no process or environment handling changes.
- [Cryptography] No key, randomness, nonce or comparison code changes.
- [Network and I/O] Suppression operates on already-delivered params and introduces no reads, connections or buffering; existing transport bounds remain applicable.
- [Errors, logs, telemetry] The exact 43 methods become silent without logging. Unknown methods retain `unrecognized` and its bounded raw payload; moderation and warning behavior remain covered by existing tests.
- [Concurrency] The list move adds no mutable lifecycle state or goroutines; `Translator` continues to run on its caller's goroutine. Repeated ignored notifications remain inert by exact membership.
- [Threat model] This slice changes transcript visibility only. Approval request handling remains separate from notification classification; warning-banner work belongs to #3594.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
