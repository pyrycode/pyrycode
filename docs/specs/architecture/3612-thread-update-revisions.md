# Atomic thread update revisions

## Files read

- `cmd/pyry/daemon_thread_relay.go` → `daemonThreadUpdates`, `daemonThreadPublisher.deliver`: the adapter splits one fold transition into an invalid equal-revision pair; delivery already uses bounded encoding.
- `cmd/pyry/daemon_thread_relay_test.go` → `TestDaemonThreadChanges`: currently asserts the invalid pair; retain addition and field-only coverage.
- `internal/thread/observations.go` → `Change`, `itemDifference`, `copyObservation`: replacements precede suffix application, and public observations detach retained bytes.
- `internal/thread/publication.go` → `publicationCapture.capture`: store publication uses the same differ with immutable shared content.
- `internal/thread/observations_test.go` → `testApplyObservation`, `TestObservationReplacement`: independent application of the existing fold contract.
- `internal/thread/observations_store.go` → `Store.Changes`: retained changes cross the ownership boundary through `copyObservation`.
- `internal/protocol/thread.go`, `thread_encoding.go` → changed/append DTOs and bounded logical encoding: whole-content replacements already fit the client contract.
- `docs/knowledge/features/thread-package.md`, `thread-package-main-thread-folding.md`, `thread-package-shadow-lifecycle-and-evidence.md`: fold revisions and captured history remain authoritative; wire tests must verify actual content, not merely frame presence.
- `docs/knowledge/features/protocol-package.md`, `development-verification.md`: inert JSON, bounded transport and independent reconstruction expectations.
- `docs/protocol-mobile.md` → bounded assembly, changed/append updates and Security model; current prose explicitly permits the defective pair.

## Context

Strict clients reject a text append after its companion metadata patch has already advanced the item to the append's target revision. This repair is one behaviour: every logical update applies atomically from the actual previous fold revision to its resulting revision. The regression and documentation belong with that behaviour. No new decision record is needed.

Sizing before and after this plan: approximately 350–450 total written lines, zero new exported types/interfaces, one production consumer updated, three acceptance criteria, no new reject branches. This fits the refiner's ~350-line analogue with additional ownership checks. No other fetched feature branch touches the planned files.

## Design

Add optional `Change.ResultContent` containing complete resulting content only for combined field/text changes. `itemDifference` supplies it from the final item; public results and `copyObservation` detach its bytes. Existing `Fields` followed by `TextAppend` semantics remain intact for current consumers, including when `Fields["Content"]` holds the previous text or is absent.

`daemonThreadUpdates` emits one `ThreadItemChangedPayload` for a combined change, replacing `content` with `ResultContent` along with all other fields. A suffix-only change emits one `ThreadTextAppendPayload`. Both use `PreviousRev` and `Rev` unchanged. Additions and field-only changes retain their existing shapes. Unknown content and explicit clears survive whole-content replacement. No synthetic revision, new wire schema, or store lookup is required. Existing `EncodeThreadUpdate` and assembly handle oversized replacements.

## Concurrency model

No new goroutines, locks or I/O. The fold remains serialized by its owner. Shared publication content is immutable; public observations own their new result-content bytes.

## State transitions and identity reuse

| Event | Race-enabled coverage |
| --- | --- |
| Repeated assistant deltas with changing Summary, then stable Summary and suffix-only updates | `TestDaemonThreadStreamingRevisions`: strict application after every delta, full-fold equality, nonzero counts for both frame types |
| Subsequent metadata-only completion under the same item ID | `TestDaemonThreadStreamingRevisions`: final content/metadata/revision match a full read |
| Repeated reads and caller mutation of retained combined result content | `TestObservationResultContent`: detached fold results and copied observations preserve the authoritative result |

## Error handling

No new errors or input rejection. Result content comes from the already rendered fold item, avoiding a second ad hoc JSON reconstruction in the relay. Existing encoding failures retire peers and require catch-up; bounded assembly still advances state only after complete logical application.

## Testing strategy

Write the strict multi-delta regression and focused combined/field-only/addition cases first and observe failure. Stream at least three deltas through a real `Fold.FeedChanges`; apply each encoded logical update independently with held-revision equality and strict advancement, comparing all item fields and content with the fold after every step. Include a long prefix to stabilize Summary and force real suffix-only appends. Exercise escaped Unicode, unknown nested content and explicit false/zero/null/empty clears. Prove the fold still applies replacements before appending and detaches the new bytes.

Run affected daemon/thread tests with `-race` on Linux through test admission, `go vet ./...` and `go build ./cmd/pyry` (output outside the worktree). Complete module gates belong to the verifier; no live Claude check is required.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md`, “Bounded thread encoding and assembly”, “thread_item_changed” and “thread_text_append”: state that logical changed/append updates require the held revision to equal `base_rev` and `rev` to be strictly greater; describe atomic combined replacements and suffix-only appends; remove the same-revision companion exception. Continuation parts remain fragments of one logical update and advance state only after complete assembly.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `itemDifference` supplies final rendered content rather than parsing untrusted replacement JSON in the relay. Content remains inert JSON across `daemonThreadUpdates`; unknown fields and explicit clears must be retained in regression coverage.
- [Tokens, secrets, credentials] No generation, storage or credential flow changes. Do not log content; existing delivery errors log event metadata only.
- [File operations] No new production filesystem operations or paths.
- [Subprocesses] No subprocess creation or environment changes.
- [Cryptography] `daemonThreadUpdates` remains upstream of the existing Noise sealing path; no keys, nonces or authentication rules change.
- [Network and I/O] Full replacements can exceed an ordinary frame. Keep `daemonThreadPublisher.deliver` using `EncodeThreadUpdate` and prove oversized combined payloads survive bounded encoding/assembly; never bypass the envelope cap.
- [Errors, logs, telemetry] No new logs, metrics or external error messages; JSON content cannot flow into diagnostics added by this repair.
- [Concurrency] `ResultContent` must detach in public observations and `copyObservation`; ownership regression coverage prevents callers corrupting retained publication state. No new goroutine or lock ordering.
- [Threat model] Strict revision advancement improves logical replay rejection. Noise remains responsible for authentication and cryptographic replay protection; this repair does not introduce executable content, new remote permissions or authentication changes. Existing protocol denial-of-service policy and client assembly limits remain under their current owners.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-19

## Revisions

- 2026-10-19: The focused daemon suite exposed `TestDaemonThreadQueriesJoin` in `cmd/pyry/daemon_thread_queries_test.go` still waiting for two frames per combined transition. Update its racing-query and post-retry live expectations to one atomic update, retaining recipient/correlation, catch-up checkpoint and final-row equality checks. Require strict revision advancement in both application branches. This existing lifecycle test proves query failure/retry followed by further updates under the same item identity; rerun it with race detection. Production design is unchanged.
- 2026-10-19: The same run found companion-frame assumptions in `TestDaemonThreadQueryBoundary` (`cmd/pyry/daemon_thread_recovery_test.go`) and `TestDaemonThreadPublication` (`cmd/pyry/daemon_thread_relay_test.go`). Observe one combined changed frame, retain the query/publication barrier and check complete content at the source revision. The new streaming regression also needs a per-ID row map because turn completion adds a separate item; compare every row while checking accumulated assistant text independently. Rerun all four failed tests with race detection. These are verification updates for the same revision invariant, with total written work still below 500 lines.
- 2026-10-19: The six-test rerun passed all existing integration checks but exposed that a long text prefix does not stabilize Summary: `appendSummary` in `internal/thread/text.go` accumulates normalized text without truncation. Use trailing whitespace and control-only deltas to leave Summary unchanged while extending exact content, exercising two genuine suffix-only appends. Keep the nonzero append count and accumulated-text assertions; rerun the streaming regression with race detection. Production design is unchanged.
