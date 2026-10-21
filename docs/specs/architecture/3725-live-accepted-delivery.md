# Preserve live publication across accepted delivery

## Files read
- `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: message commits before outcome.
- `cmd/pyry/queued_send_history.go` → `beforeDelivery`, `delivered`: readiness proves acceptance append success; leaf lock orders outcomes.
- `internal/history/forward.go` → `ForwardReader.Walk`, `Tail`: each chunk/segment can publish separately.
- `internal/thread/fold.go` → `feedEntry`, `addItem`: message validation, text closure and receiving provenance.
- `internal/thread/sends.go` → `sendFact`, `deliveredSendContent`: permanent identity, ownership and first terminal.
- `internal/thread/render.go` → `renderShared`: delivery recipes preserve source-backed content.
- `internal/thread/store.go` → `run`: production source-backed follower and per-chunk publication.
- `internal/thread/publication.go` → `publicationCapture.capture`: withdrawal requires a baseline.
- `cmd/pyry/daemon_thread_relay.go` → `publish`: gap retirement prevents unsafe unsolicited replay.
- `cmd/pyry/daemon_thread_gap_test.go`, `cmd/pyry/queued_send_history_test.go`, `internal/thread/sends_test.go`: existing fixtures and safety assertions.
- `docs/knowledge/features/history-package-producers.md` § Accepted sends and linked outcomes (#2972): failure cannot mint a durable outcome.
- `docs/knowledge/features/thread-package-main-thread-folding-sources.md` § Accepted sends and durable outcomes: sender ownership, source validation and stable internal indexes.
- `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence.md` § Shadow lifecycle and evidence: reconnect history cannot become a fresh refusal.
- `docs/protocol-mobile.md` § Security model: authenticated conversation boundaries and history replay remain distinct from live events.

## Context
An operator message published before its linked outcome is later withdrawn when
the fold reconciles it with an acceptance. That legitimate safety reset retires
the peer and loses a fresh refusal. Coalescing producer callbacks cannot cover
ForwardReader chunk/segment boundaries. The fix establishes identity from the
delivery message itself. No new decision record is needed.

One useful behaviour: preserve the joined peer across accepted delivery. The
producer field and its sole fold consumer cannot ship independently. Estimated
written work: 150 production, 440 test/helper and 100 plan lines, under 800;
zero exported types, zero signature migrations, four criteria, at most eight
rejection categories. Concurrent #3693 edits acceptance attachment recording;
its additions are independent of the delivery reference. No dependency.

## Design
`operatorMessageHistory` records an optional `accepted_entry_id` in the durable
message payload only after `beforeDelivery` proves the acceptance append
completed successfully. Legacy publication retains its existing safe payload.
No reference is emitted after failed acceptance, and no outcome after failed
message append. A failed outcome leaves a self-contained committed delivery.

After ordinary user-message validation and text closure, the fold validates
the optional reference: strict nonzero backward integer, consistent case
variants, same conversation, existing valid acceptance still queued/written.
It merges delivered content/provenance into the acceptance before the message
can become a public row, retaining stable internal indexes and acceptance
visibility. The committed message proves delivered/inactive status and order;
it does not fabricate an outcome object. The later matching outcome enriches
that same item once. Conflicting terminals and reused references remain inert.
Messages carrying invalid explicit references cannot be rescued by a legacy
outcome to claim a different owner. Unlinked older messages retain the existing
legacy join/suppression and genuine-withdrawal safety behaviour.

Use existing accepted/message maps, item order/revision and content recipes;
do not introduce another lifecycle map or change public item/wire types.
Inline and reference-backed folds must render identical content at every prefix.

## Concurrency model
No new production goroutine or lock. Acceptance readiness remains the producer
barrier. Existing per-reference mutex serializes outcomes; folding stays owned
by one Store worker. Tests use the leaf mutex to hold outcome appends after a
real message commit, and the Store fold barrier to control consumption. Every
test worker exits via cancellation and Shutdown/join.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Acceptance, coalesced message/outcome/refusal | `TestAcceptedDeliveryLiveRefusal` |
| Message prefix published before outcome/refusal across reader segments | `TestAcceptedDeliveryLiveRefusal` |
| Interrupted prefix, full replay/reopening and replacement-peer catch-up | `TestAcceptedDeliveryLiveRefusal`, `TestSendEarlyDelivery` |
| Duplicate/conflicting terminal and already-claimed acceptance/message | `TestSendEarlyDeliveryReferences` |
| Equal app IDs across devices and reused queue handle | `TestSendEarlyDeliveryReferences` |
| Acceptance/message/outcome append failure | `TestAcceptedDeliveryAppendFailures` |

## Error handling
Invalid references never reserve an owner. Unavailable source content returns
the existing generic fold error before claiming either identity. No waiting for
a companion fact: each committed prefix publishes independently. Existing gap,
suppression, catch-up and retirement paths remain unchanged.

## Testing strategy
Write the real producer/follower/publisher regression first and retain failing
Go JSON on the pre-repair production revision. Publish an accepted baseline to
a named joined peer, force coalesced and separated schedules with barriers, and
count refusal additions, resets and retirement. Force an actual segment cut
with bounded inert padding; inspect reader chunk boundaries. Compare inline
replay, source-backed reopen/catch-up and immutable retained observations,
including message-only prefixes. Add table-driven invalid-reference and append
failure coverage. Retain post-repair JSON, Linux revision and counted results
outside the worktree for dispatcher evidence capture. Run affected package race
checks through shared admission, plus vet and build; full gates belong to the
dispatcher. Core uses zero real Claude turns. Mobile #2374 owns its exact
scripted refusal scenario after merge, with original 90000 ms assertion, one
clickable Switch back, disappearance on tap and fresh settings returning haiku.

## Open questions
None. Prefix delivery is terminal evidence; only a committed matching outcome
may supply the `outcome` object and its revision.

## Documentation handoff
Pending for the documentation stage:
- `docs/knowledge/features/history-package-producers.md`, **Accepted sends and linked outcomes (#2972)**: describe the implemented durable identity/append-failure contract and compatibility with older facts.
- `docs/knowledge/features/thread-package-main-thread-folding-sources.md`, **Accepted sends and durable outcomes**: distinguish new delivery identity establishment from legacy message/outcome suppression; document validation, prefix replay and unchanged genuine-gap safety.
- `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence.md`, **Shadow lifecycle and evidence**: replace the normal-delivery retirement explanation with the repaired live-boundary guarantee and regression pointer; retain genuine-gap retirement and history-only refusal semantics.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `operatorMessageHistory` constructs the link from daemon-owned append results, never client message/queue identity. Fold validation checks an earlier acceptance in its conversation and prevents owner reuse; explicit invalid links cannot enter legacy joins.
- Tokens/secrets: no credential handling added; durable messages retain the safe MessagePayload projection, and acceptance sender fields remain acceptance-owned.
- File operations: existing history append, lookup and cache contracts retain their path/size/permission checks; no new path or write mechanism.
- Subprocesses/cryptography: no new execution, keys, randomness or cryptographic operations.
- Network/I/O: existing authenticated peer selection, bounded history reader and gap retirement remain authoritative; no transport bypass or unbounded buffer.
- Errors/logs/telemetry: source failures use generic existing errors; no message payload, sender identity or filesystem path is logged by new code.
- Concurrency: readiness publishes immutable acceptance ID; no additional lock ordering or goroutine; interrupted message/outcome prefixes reconstruct from committed facts.
- Threat model: preserves conversation isolation and refusal history/live separation under the protocol Security model. No auth or replay protection is weakened; broader existing protocol threats remain owned by their existing mechanisms.

## Revisions
- 2026-10-21: `internal/thread/cache.go` requires `foldingRules` to change when item semantics change. Increment it so saved old interpretations cannot bypass the new reference validation. Existing cache compatibility tests cover invalidation. The regression copies only raw segments into an independent instance for reopen proof; simultaneous Store owners must not share derived checkpoint files. This preserves the design and adds one literal edit, keeping the estimate below 800 lines.
- 2026-10-21: Compatibility checks found that `testQueuedReceiptIdentity` requires identical durable/live/replay message bytes. Publish the same safe stored message, including its inert durable acceptance reference, instead of excluding that field from the legacy callback. `TestQueuedSendHandleActions` also assumed a single terminal patch; it must consume every retained change now that delivery and outcome can both update the same permanent item without retirement. These are compatibility consequences of the planned identity change; no public DTO or action contract changes.
- 2026-10-21: Verifier finding 1 exposed a written-prefix mismatch in `deliverSend`. Linking a delivery must discard the intermediate `send_written` outcome in both fold modes; only a committed matching `send_delivered` supplies the final outcome. Clone recipe fields before removal to preserve retained views, and advance `foldingRules` to invalidate the old interpretation. `TestSendWrittenDeliveryPrefixes` covers incremental/full replay at every prefix and retained observations; `TestAcceptedDeliveryAppendFailures/written_outcome` covers the real failed final append, source-backed reopening and catch-up. This repairs the existing prefix-equivalence invariant, with no independent behaviour to split: about 90 written lines, no exported types, consumer migrations or reject branches. No feature branch overlaps the repair files. Security review remains PASS: ownership validation, source checks, goroutine lifetimes and trust boundaries are unchanged. Documentation handoff for **Accepted sends and durable outcomes** must include this intermediate-outcome removal policy.
