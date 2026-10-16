# Revision-scoped Store tool bodies

## Files read

- `internal/thread/queries.go` → `captureQuery`, `queryIndex`: atomic immutable publication and public membership.
- `internal/thread/query_tree.go` → `lookup`: constant-time membership and query-work instrumentation.
- `internal/thread/store_render.go` → `renderItems`, `withdraw`: captured recipes, cancellation and worker-identity protection.
- `internal/thread/render.go` → `renderSharedLookup`: selected-reference closure and complete content parity.
- `internal/thread/store_render_test.go` → `TestStoreCapturedRecipes`, `TestStoreSelectedRecipeClosure`, `TestStoreOldRenderFailure`: barriers and inherited-reference fixtures.
- `internal/thread/store_test.go` → `testThreadStore`, `testStoreAppend`, `testStoreWait`: real-history Store fixtures.
- `docs/knowledge/features/thread-package-background-store-queries.md` → Bounded queries: hidden/dropped membership, no loading, detached output and selected-reference bounds.
- `docs/knowledge/features/thread-package.md`, `thread-package-background-store.md`, `development-verification.md` and `CODING-STYLE.md`: package ownership and verification conventions.

## Context

Expansion needs one rendered tool/agent body without selecting thread windows,
active extras or descendants. This is one reusable Store capability; adapters,
authorization and demand/loading remain in #3522/#3523. No decision record needed.

Sizing: approximately 100 production + 480 tests/helpers + 90 plan lines,
one exported result type, zero migrated consumers, four acceptance criteria,
and fewer than ten rejection branches. No overlapping feature branch edits the
planned files as of the initial origin fetch.

## Design

Add `Store.ToolBody(ctx, conversationID, expectedEpoch, itemID, expectedRev)
(ToolBodyResult, error)` in `internal/thread/tool_body.go`. The result carries
State, ID, Rev, Content, Epoch, consumed Version and Err. Success has usable
state and detached complete rendered Content, including empty input/output and
unknown fields. It returns no parent/child rows.

Validate canonical conversation ID first (existing `history.ErrInvalidID`), then
require nonempty epoch and nonzero item/revision (`ErrInvalidBodyScope`). Capture
once with `captureQuery`; nonusable states match existing query semantics.
For usable publications reject mismatched epoch/revision (`ErrStaleBody`), absent
public ID (`ErrBodyNotFound`) and non-tool/agent kind (`ErrBodyKind`). These errors
return a zero result. Consumed version is reporting metadata, not a condition.

Use `queryIndex.lookup` for the requested row and `renderItems` for that row only.
Inherited recipe ancestors can render internally, using only the captured root.
Instrument recipe lookup through `queryIndex.lookup` so exact metadata visits
include dependency discovery. No scanning, sorting or rendering unrelated rows.

## Concurrency model

No new goroutines or locks. Capture holds Store.mu briefly; metadata selection,
history lookup and rendering run outside it. The request owns its context and
temporary recipes/content. Existing worker goroutines retain their shutdown paths.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Feed/publication advances selected revision during held lookup | `TestStoreToolBodyCapture` |
| Unrelated consumed-version advance with stable revision | `TestStoreToolBodyParity` |
| Unload and reload with same item ID; old success/failure | `TestStoreToolBodyCapture` |
| Cancel held lookup while another reader succeeds; worker stays usable | `TestStoreToolBodyCapture` |
| Overlapping item IDs in independent conversations | `TestStoreToolBodyParity` |
| Required references disappear after capture | `TestStoreToolBodyUnavailable` |

## Error handling

Validation/refusal and cancellation use the separate Go error and zero result.
Rebuilding/unloaded/unavailable states carry only State/Err. Rendering failure
uses StateUnavailable/ErrUnavailable, with no source error or partial body;
reuse `withdraw` unchanged so cancelled reads and old workers cannot retire a
healthy replacement. An already captured read can finish after unload if its
references remain available. No loading, recovery waits or peer authorization.

## Testing strategy

Write focused tests first and observe missing API failure. Compare tool/agent
content with complete Snapshot and legacy Fold output, assert unknown/empty and
large payloads, public/hidden/private/claimed/dropped membership and refusals.
Hold request-tagged lookup across lifecycle changes. After preparation, assert
exact history IDs and metadata visits while unrelated/descendant inventory and
bodies grow. Run focused tests, `go test -race ./internal/thread`, `go vet ./...`
and `go build -o /tmp/builder-3528/pyry ./cmd/pyry` on macOS. The verifier owns
`make check` and the full-module race gate.

## Open questions

None.

## Documentation handoff

Pending documentation stage: `docs/knowledge/features/thread-package-background-store-queries.md`,
“Bounded queries”: document the body API, public membership, epoch/revision
validation, capture identity, selected-reference bounds and lifecycle/error
behavior. State that usable bodies preserve existing rendered content and this
API adds no peer authorization or loading.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `ToolBody` validates canonical IDs and public captured membership; private unresolved children cannot be selected. Caller authorization remains required.
- Tokens/secrets: no generation/storage; content is intentionally returned only to an already authorized caller and is never logged.
- File operations: reuses history lookup with a canonical conversation ID; no new paths or writes.
- Subprocesses and cryptography: no subprocess or cryptographic operations introduced.
- Network/I/O: no sockets; reference discovery stays selected-only. Selected-body size may scale with existing complete content, as required.
- Errors/logs: fixed sentinel errors; source failures return only ErrUnavailable and no partial payload.
- Concurrency: immutable capture; `withdraw` keeps identity/cancellation guards; tests cover old readers and independent cancellation.
- Threat model: OUT OF SCOPE peer authorization/framing/demand ownership belongs to #3522/#3523. This API cannot authorize or load peers/conversations.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17

## Revisions

- 2026-10-17: Read `internal/thread/render_endings_test.go` → `testEndingEntries`
  and `TestStoreCapturedInheritedEnding` for the inherited-reference proof.
  A completed ordinary child uses its own result without an inherited ending;
  bounded tests instead select a nested Task interrupted by its root's ending.
  Measure requests after preparation over 10/120 descendants and 16/4096-byte
  unrelated bodies; a larger preparation fixture exceeded its readiness deadline.
  The API and capture contracts are unchanged.
