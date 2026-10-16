# #3535 — Persist stable channel pin timestamps

## Files read

- `internal/conversations/conversation.go` → `Conversation`: optional host metadata and JSON tags.
- `internal/conversations/registry.go` → `SetArchived`, `SetMuted`, `Promote`, `Get`, `snapshotLocked`, `Save`, `Load`: locked mutations, static errors and caller-owned persistence.
- `internal/conversations/registry_test.go` → `TestRegistry_SetArchived_Idempotent`, `TestRegistry_SetArchived_RoundTrip`: timestamp and persistence patterns; new tests go in a separate file because this file exceeds the size guideline.
- `docs/knowledge/features/conversations-package.md` § Types → Conversation: absent optional fields need no migration.
- `docs/knowledge/features/conversations-registry-crud.md` § SetArchived and SetMuted: metadata-only mutations must not bump activity or change session bindings.
- `docs/knowledge/features/conversations-registry.md` § Save concurrency: snapshots share immutable pointer values; never mutate a timestamp pointee.
- `CODING-STYLE.md` § Persistent data conventions: package errors contain no transport vocabulary; serialized timestamps use `Equal`.
- `docs/knowledge/features/development-verification.md`: source and validation scope.

No other fetched feature branch touches the planned files.

## Context

Each host needs a durable pin time for client-side cross-host ordering. This ticket provides the storage contract; #3536, #3537 and #3538 own wire declarations, relay consumers and activation. No decision record is needed.

## Design

Add `Conversation.PinnedAt *time.Time` with `json:"pinned_at,omitempty"`; nil or absent means unpinned. Add `Registry.SetPinned(id ConversationID, pinned bool, now time.Time) error` beside the metadata setters. Under `r.mu`, locate the row, reject pinning an unpromoted row, then change only `PinnedAt`. Pinning a nil timestamp allocates `now.UTC()`; repeated pinning keeps the existing timestamp. Unpinning always clears it on an existing row, regardless of promotion or archive state. Archived channels may be pinned.

Unknown IDs return existing `ErrConversationNotFound`. Unpromoted pin attempts return new static `ErrConversationNotPromoted`, matchable with `errors.Is`. The method never saves. Commit `internal/conversations/testdata/conversations.json` as a legacy fixture without `pinned_at`. Add focused tests in `internal/conversations/pin_test.go`.

Sizing: one storage deliverable, approximately 300 total written lines, zero new exported types/interfaces, zero existing consumer updates, three acceptance criteria, two reject branches. All five limits remain within bounds.

## Concurrency model

No production goroutines or new locks. Lookup, promotion check and write share `r.mu`. Allocate a timestamp on each new pin and never mutate its pointee, preserving shallow snapshot safety. Tests join every goroutine they start.

## State transitions and identity reuse

| Event | Race-enabled test |
| --- | --- |
| Initial pin, repeated pin, clear, repeated clear, pin again on active/archived channel | `TestRegistry_SetPinned_Transitions` |
| Unknown ID and unpromoted pin rejection; unpin unpromoted rows with/without a timestamp | `TestRegistry_SetPinned_Eligibility` |
| Save/load pinned, then clear/save/load under the same ID; legacy row loads absent | `TestRegistry_Pinned_RoundTrip` |
| Concurrent pin/clear, promotion changes, reads and snapshots | `TestRegistry_SetPinned_Concurrent` |

## Error handling

Both rejections return before writing any row. Static sentinels contain no IDs or user content. Existing Save/Load errors and file-write behavior remain unchanged.

## Testing strategy

Write the tests first and confirm failure before implementation. Assert UTC normalization, stable timestamps, metadata isolation including another row, pointer snapshot stability, distinguishable errors, legacy omission and persistence using `time.Time.Equal`. Run `go test -race ./internal/conversations/...`, `go vet ./...` and `go build -o /tmp/builder-3535/pyry ./cmd/pyry` on this macOS host. The other OS is outside this ticket's verification scope; the dispatcher owns the full-module gate.

## Open questions

None. The current issue explicitly requires existing not-found and a distinct matchable unpromoted error; choose `ErrConversationNotPromoted` for the latter.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/conversations-package.md`, under `Types` → `Conversation`, to document `PinnedAt`'s optional storage field, absent/nil meaning unpinned, stable UTC pin time, promoted-only pinning, any-existing-row unpinning and caller-owned saving.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `SetPinned` checks lookup and promotion under `r.mu` before mutation. Stored JSON is parsed by existing `Load`; this slice exposes no network entry point.
- [Tokens, secrets, credentials] Pin timestamps contain no credentials; no credential lifecycle changes.
- [File operations] No new caller-derived paths. Existing `Save` uses a same-directory temporary file, mode 0600, parent mode 0700, sync and rename; no file-operation changes.
- [Subprocesses] The feature launches no processes or shell commands.
- [Cryptography] No cryptographic operations or key material enter this design.
- [Network and I/O] OUT OF SCOPE: wire input validation and authenticated relay exposure belong to #3536, #3537 and #3538; this setter performs no I/O.
- [Errors, logs, telemetry] Static sentinels expose no supplied values. Add no logging or telemetry; storage errors remain independent of protocol codes.
- [Concurrency] Check and mutation use the same mutex; timestamps are immutable once published, including to Save snapshots. No goroutine lifecycle or additional lock order is introduced.
- [Threat model] Host-local metadata only. Remote authorization, malformed payloads and reply projection are deferred to #3536, #3537 and #3538; none is activated here.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
