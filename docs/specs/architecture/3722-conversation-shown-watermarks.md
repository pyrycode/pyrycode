# Retire legacy conversation-list watermark scans

## Files read

- `internal/relay/handlers/list_conversations.go` → `ListConversationsWithAgents`, `ListConversations`: registry projection, ordering and agent filtering; remove the history argument and refusal.
- `internal/protocol/conversations_read.go` → `ConversationSummary`: remove `LatestEntryID`, retaining optional `LastShownVersion` and durable `ReadUpTo`.
- `cmd/pyry/relay.go` → `startRelayV2`: list registration and shown-provider installation before read-handler construction.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyHistoryReader`, `publishLegacyHistory`: retire only the list scan, retaining receipt publication.
- `internal/relay/v2session_thread.go` → `threadSummary`, `threadProjected`: delivery-time authority and negotiated thread gating remain unchanged.
- `cmd/pyry/daemon_thread_relay.go` → `installDaemonThreadRelay`; `cmd/pyry/daemon_thread_summary.go` → `refreshThreadSummary`: loaded/certified-unloaded authority and recovery notification.
- `cmd/pyry/daemon_thread_watermark_test.go` → `TestDaemonThreadLastShownCertificates`, `TestDaemonThreadLastShownLifecycle`, `TestDaemonThreadLastShownUnavailable`: exact consumed/raw progress, no worker startup or segment scan, and unavailable-state guards.
- `cmd/pyry/daemon_thread_offscreen_test.go` → `TestDaemonThreadOffscreenShownRecovery`; `cmd/pyry/unread_visibility_test.go` → hidden-tail and restart tests: preserve lifecycle, isolation, durable marks and producer-driven recovery.
- `cmd/pyry/legacy_runtime_receipts_test.go` and the agent, queue, prompt and history-projection tests: separate surviving source/receipt assertions from obsolete checkpoint and watermark oracles.
- `internal/relay/handlers/list_read_state_test.go`, `displayable_read_state_test.go`, list/agent/metadata projection tests; protocol read-state tests and `internal/relay/v2session_thread_test.go`: migrate constructor calls and serialized assertions.
- `docs/knowledge/INDEX.md`, `CODING-STYLE.md`, protocol, relay-handler and history package overviews: wire presence and source metadata contracts.
- `docs/knowledge/features/thread-package-cache-and-epochs.md` § Cache and epochs: clean certificates require independently known exact V = H; lookup must never warm history or start a worker.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Sessions, agents, messages, read marks: shown-thread authority defines unread.

## Context

The list currently refuses on unavailable legacy history before shown-thread projection can run. Remove that obsolete authority and wire field so otherwise eligible rows survive with unknown shown evidence. Read clamping already uses the authoritative provider from merged #3721. No new decision record is needed.

One observable retirement includes its endpoint migration and regression coverage. CodeGraph counts 27 list-constructor caller edges and 13 legacy-reader constructing symbols (one production). This exceeds ten callers; actual lineage #3714 → #3716 → #3722 and the verification floor require building in place. `needs-human:sizing` and the refiner's split proposal are already recorded. Forecast: approximately 500 written lines including plan/tests and local migrations, plus deletions; zero new exported types, two acceptance criteria, zero new reject branches. Recount before implementation. Feature-branch inventory has no overlap on the intended production and mixed-test files; no unmerged prerequisite is needed.

## Design

`ListConversations(reg)` and `ListConversationsWithAgents(reg, harnessFor)` return the same registry-based handler without a history dependency. Remove `historyLatestReader`, its nil/error refusals and `legacyHistoryReader`. Preserve every existing metadata assignment, filtering rule, stable order, correlation and `ReadUpTo`.

Remove `ConversationSummary.LatestEntryID`. Keep `LastShownVersion` optional: unavailable evidence omits it and known zero emits numeric zero. `V2SessionManager.threadSummary` continues to query the existing provider independently for each row and update, replacing stale supplied evidence and preserving thread-capability gating. No catch-up, raw watermark or maximum item revision substitutes for authority.

Keep `historyVisibilityMetadata`, source readers, internal history APIs and receipt/projection publication used by surviving consumers. Delete only legacy watermark/checkpoint tests and helpers. Mixed tests retain source metadata, durable read marks, conversation isolation, hidden-tail/restart semantics and the offscreen quiet-retirement recovery proof. No compatibility adapter replaces the retired reader.

## Concurrency model

No goroutines, locks or worker admission change. Listing reads registry snapshots; delivery resolves existing metadata authority. Existing followers and publisher retain cancellation/join paths, and listing never opens every conversation.

## State transitions and identity reuse

| Event | Race coverage retained or migrated |
| --- | --- |
| Repeated lists, filtered peers, empty registry and absent shown evidence | `TestListConversationsDurableReadState`, list/agent tests and summary wire projection tests |
| Loaded zero, rebuilding/unavailable, unloaded clean certificate, stale/cold progress | `TestDaemonThreadLastShownLoadedZero`, `TestDaemonThreadLastShownCertificates`, `TestDaemonThreadLastShownLifecycle`, `TestDaemonThreadLastShownUnavailable` |
| Hidden suffix followed by reload/unload, repeated read and unavailable refusal | `TestUnreadThreadHiddenTails`, `TestConversationReadDisplayableRoundTrip`, `TestMarkConversationReadThreadRestart` |
| Repeated restart under the same conversation ID and durable held mark | `TestUnreadRestartReconciliation` |
| Quiet retirement racing hidden append, producer recovery without opening offscreen rows, distinct conversation IDs | `TestDaemonThreadOffscreenShownRecovery` |

## Error handling

Listing no longer has a history-unavailable refusal. Existing marshal/send errors propagate. An unavailable shown lookup omits only that row's shown field. `MarkConversationRead` retains retryable `history.unavailable` without any in-memory or durable mark advance.

## Testing strategy

Write serialized regressions before production edits and observe the old history refusal/field. Migrate constructor callers directly. Cover absent/zero/nonzero evidence on lists and updates, correlated list delivery, durable marks and metadata/filter/order preservation. Run focused Linux Go race tests for the changed handlers, protocol and daemon/relay families through test admission, recording executed/passed/failed/skipped counts. Compile affected tagged real-Claude tests after helper deletion. Dispatcher/verifier own full unit/race, integration and release/live gates; macOS is outside this ticket's verification scope.

## Open questions

None. Keep receipt publication and thread-capability negotiation until their owning sibling retirements.

## Documentation handoff

- Pending documentation stage: `docs/protocol-mobile.md` §§ conversations, conversation_updated, Marking a conversation read, related application-message/error rows and examples: remove `latest_entry_id` guidance and whole-list legacy-history refusals; unread is `last_shown_version > read_up_to` only with present evidence; absence means unknown, not zero. Retain #3721's retryable read refusal when authoritative evidence is unavailable.
- Pending documentation stage: `docs/knowledge/features/thread-package-cache-and-epochs.md` § Cache and epochs: retain certified offscreen watermark authority and quiet-retirement recovery.
- Pending documentation stage: `docs/knowledge/features/history-package-watermarks.md` § Unread state uses a separate, lazily recovered watermark (#2954): replace legacy daemon list-scan and obsolete list/read test guidance with authoritative shown-thread summaries; retain raw internal history APIs and durable read-mark contract.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: registry rows remain the list disclosure boundary; `AgentOf` filtering runs before projection. `threadSummary` uses the daemon provider per row and never trusts supplied shown evidence. Preserve capability gating and serialized stale-evidence removal tests.
- Tokens/secrets: no credential or token data enters the list projection; no new logging or credential handling.
- File operations: removing `legacyHistoryReader` removes list segment traversal. Existing certificate containment and exact progress checks remain unchanged; durable read storage retains its atomic contract.
- Subprocesses: no subprocess or shell is started by listing or summary lookup.
- Cryptography: authenticated relay transport and encryption remain unchanged; no new primitives or identity uses.
- Network/I/O: existing authenticated framing and frame limits remain; metadata listing adds no source reads or remote input-dependent worker startup.
- Errors/logs/telemetry: no raw source, file paths or payloads are introduced into list errors/logs. Read evidence refusal retains static retryable messaging.
- Concurrency: no lock order, shared mutable authority or shutdown path changes; lifecycle race tests retain retirement and repeated-recovery proof.
- Threat model: paired clients continue to receive permitted registry metadata through existing authenticated dispatch. Mandatory thread admission is OUT OF SCOPE, owned by #3720; content/receipt retirement is owned by #3718.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-21

## Revisions

- 2026-10-21: Added `TestDaemonThreadListShownAuthority` in `cmd/pyry/daemon_thread_list_test.go` to exercise actual list delivery with corrupt source segments, an eligible nonzero certificate, certified zero and a cold unknown row together. Repeated negotiated/non-thread lists must preserve every row and held mark, start no workers and leave unknown raw progress cold. Receipt validators formerly reached through checkpoint oracles now assert receipt acceptance directly; their source/identity coverage remains. No authority or read-clamping contract changes.
