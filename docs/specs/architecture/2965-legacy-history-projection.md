# Legacy history projection and explicit visibility (#2965)

## Files read

- `cmd/pyry/conversation_history.go` → `appendConversationHistory`, `newHistoryPager`: shared append identity, content-free errors and opaque cursor adaptation.
- `cmd/pyry/interactive_turn_v2.go` → `emit`: append once before ring publication and interactive-only fan-out, including no recipients.
- `cmd/pyry/session_transition_v2.go` → `broadcast`, `toWirePayload`: legacy clear/idle delimiters and existing recipient gates; recovery is suppressed.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory`: safe queued text projection and successful identity handoff.
- `cmd/pyry/channel_delivery.go` → `channelDeliveryHistory`, `deliver`: separate writer with raw-page deduplication and existing post completion semantics.
- `internal/turnbridge/outbound.go` → `MapEvent`, `MapState`: complete currently emitted legacy type vocabulary; task updates carry terminal status in a nonempty status field.
- `internal/history/log.go` → `AppendWithMetadata`, `Page`, `LatestDisplayableEntryID`: opaque storage, explicit visibility and unchanged nil-metadata fallback.
- `cmd/pyry/interactive_turn_v2_history_test.go`, `relay_history_seam_test.go`, `live_history_id_test.go`, `channel_delivery_test.go`: real-store fixtures, identity comparisons and narrow-interface doubles.
- `docs/knowledge/features/history-package.md` → Producers, Reader, unread watermark: separate ID sequences to prove provenance, append before publication, terminate raw paging on AtStart.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Compatibility: new facts must not reach existing clients.
- `docs/knowledge/features/development-verification.md` → Protocol boundaries and test execution: compare actual emitted bytes and non-vacuous test results.
- `docs/protocol-mobile.md` → Security model: encrypted paired transport and untrusted replayed content remain unchanged.

## Context

New thread facts must remain durable without reaching legacy history, live streams or replay. Visibility is a separate unread-state decision, not permission to publish. Existing producers now declare visibility without rewriting old entries or modifying payloads. No new producer, capability, attribution contract or decision record is needed. Dependencies #2964 and #2967 are merged and present. Remote feature-branch inspection found no overlapping files.

## Design

Add a cmd-local fixed allowlist containing only the types produced today by MapEvent/MapState, session transitions and operator messages. Raw history accepts any type. `newHistoryPager` filters exactly one raw page with the allowlist, forwarding that page's Cursor and AtStart even if all entries are removed; consumers terminate on AtStart.

`interactiveTurnEmitterV2.emit` appends first, then returns for a non-allowlisted type before ring publication or recipient enumeration. This supports durable synthetic facts with no clients and leaves replay free of new types. Existing types keep their current publication and recipient gates regardless of visibility or append success.

The common append seam uses AppendWithMetadata with explicit Shown classified from the existing marshalled payload. Channel delivery adopts AppendWithMetadata in its narrow interface and uses the same classifier for both deltas and completion; its existing deduplication and storage-error behavior remain intact. Metadata stays outside payloads and Session remains absent.

Classification: content is shown; the named live-reading types are hidden. Turn ends are hidden only for end_turn, no error flag/category, absent/success outcome and absent/completed terminal reason. Info banners are hidden only when non-stopping. Task updates are shown with a nonempty terminal status or summary, hidden for patches alone. Clear transitions are shown and idle_evict hidden. Unknown fact types default hidden in the common seam and remain ineligible independently of any explicitly supplied Shown metadata. Malformed legacy conditional payloads conservatively classify as shown.

Sizing after design: one deliverable (legacy-compatible history projection), approximately 650 written lines including tests/plan, zero exported types/interfaces, four producer seams (three common-seam consumers plus channel delivery), four acceptance criteria and no new state machine/reject branches. All five limits remain within bounds.

## Concurrency model

No goroutines or locks added. Existing emitter serialization and Store locking remain authoritative. History append finishes before eligible events enter the ring. Channel delivery retains its existing lock order, cancellation and shutdown paths.

## Error handling

Keep append/page outcome mapping and content-free logs unchanged. Nil or failed storage cannot suppress eligible interactive, transition or operator publication. New types are suppressed even if append fails. Invalid/foreign/stale cursors still reach Store.Page unchanged and retain existing outcomes. Classification never rewrites or rejects payloads.

## Testing strategy

Write failing hermetic tests first. Use real stores for bounded filtered walks through mixed and entirely excluded logs, consecutive empty pages, nil/false/true Shown metadata, durable identity/payload/timestamp retention and reopen. Synthetic emitter facts prove one raw append and zero live/ring publication with and without clients. Drive actual interactive emission for the full type/classification table; compare storage, wire and ring bytes/identities and recipient gates. Drive transition, operator and channel-post writers to prove explicit metadata and warm/reopened unread watermarks, preserving nil-metadata older entries. Existing absent/failed-storage and cursor-outcome tests continue to run.

Run focused tests, race tests for cmd/pyry, go vet ./... and go build ./cmd/pyry (binary in scratch). The dispatcher owns make check and the full-module gate.

## Open questions

None. A nonempty task-update status is the existing terminal-status contract; no new status vocabulary is inferred.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/history-package.md`, “Reader (#2116)” and “Producers (#2114, #2115)”, describe the default exclusion of new history-only types, the separation of legacy eligibility from explicit visibility, the shipped producer classification including channel posts, and bounded filtering that preserves raw cursors through empty pages. State that consumers terminate on `AtStart`, not an empty entry list.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The fixed allowlist at `newHistoryPager` and `emit` excludes unknown types regardless of stored visibility or connection attributes. Metadata cannot authorize transport.
- [Tokens/secrets] No credential changes. The operator writer keeps its safe queued-text projection, excluding delivery paths; metadata introduces no new wire fields.
- [File operations] Existing Store containment, permissions and append recovery remain responsible for all disk operations; raw cursors are forwarded, never decoded here.
- [Subprocesses] No new subprocess operations or environment handling.
- [Cryptography] No primitive, key, nonce or authentication changes.
- [Network/I/O] One bounded Store.Page result is filtered; no scan-ahead loop or remote-limit allocation is introduced. Existing encrypted Push and recipient gates remain in place.
- [Errors/logs] Existing discriminant-only logs and generic page outcomes remain; classification failures do not log payloads or decode errors.
- [Concurrency] No new shared state or goroutines; append remains before ring publication and channel lock order is unchanged.
- [Threat model] Stored payloads remain untrusted content rendered by the client under the existing protocol security model. This slice prevents new fact leakage without changing pairing, transport authentication or rendering responsibilities.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08
