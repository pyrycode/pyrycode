# Legacy runtime-fact receipts (#3026)

## Files read

- `cmd/pyry/history_projection.go` → `legacyHistoryType`, `historyEntryShown`: transport eligibility differs from raw visibility.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`, `newHistoryPager`: append identity and bounded raw paging.
- `cmd/pyry/interactive_turn_v2.go` → `startTurnIfNeeded`, `emit`: serial source lifecycle and legacy publication.
- `cmd/pyry/runtime_history.go` → `recordRuntimeFact`, `closeRuntimeSource`: openings and interruptions bypass emit.
- `cmd/pyry/session_transition_v2.go` → `broadcast`: dividers bypass emit and legacy transitions skip replay.
- `cmd/pyry/runtime_boundary.go`, `cmd/pyry/stream_turn_drain.go` → `installRuntimeHistory`, `streamTurnSink`: installation and serial boundary lane.
- `cmd/pyry/relay.go` → `startRelayV2`: shared list/read reader wiring and replay installation.
- `cmd/pyry/startup_history.go` → `reconcileStartupHistory`: restart dividers have occurrence/conversation identity without a predecessor session.
- `internal/history/log.go` → `Page`, `displayableEntry`: absent visibility fallback, durable IDs and cursor contract.
- `internal/e2e/relay_v2_history_test.go` → `TestRelayV2_ConversationHistory`: encrypted bounded walks, startup receipt identity and request rejection/interrupt deadlines.
- `internal/relay/handlers/list_conversations.go`, `mark_conversation_read.go` → `historyLatestReader`, `MarkConversationRead`: narrow reader seam and persistent monotonic clamp.
- `docs/knowledge/features/history-package.md` § Reader, `history-package-producers.md` § Legacy eligibility and explicit visibility, `history-package-watermarks.md` § Unread state: bounded projection and raw metadata contracts.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: check serialized required fields and execution counts.
- Mobile `HistoryPageReducer.kt` → `reduceOrderedHistoryPage`, `ThreadReadEvidence.kt` → `understoodNonvisualEntry`, `received`, `checkpoint`: known nonvisual facts account IDs without granting sight; numeric holes remain barriers.
- Mobile `InteractivePayloads.kt` → `BannerPayloadDto`, `ThreadProjection.kt` → `applyBanner`, `ThreadBannerLevelTest.kt`: required banner fields, lifecycle-neutral live fold and info banners draw nothing.

## Context

Runtime-enabled turns leave unsupported durable IDs between legacy content. Mobile correctly refuses to cross that hole. Known shown interruptions also raise the raw unread watermark beyond legacy presentation. This is a legacy compatibility repair; raw thread facts and metadata remain unchanged. No decision record is required.

#3014 / PR #3016 is merged and included in this checkout. No other fetched feature branch overlaps the planned production files.

## Design

Project only `main_turn_opened`, `main_tool_interrupted`, `main_turn_interrupted` and `session_divider` into `banner` with payload `{conversation_id, level:"info", text:"", truncated:false, stops_turn:false}`. Keep original durable ID and timestamp. Validate known facts' ownership, occurrence and required identities before projection; malformed facts stay holes. Arbitrary unknown types stay excluded. Existing eligible payloads remain byte-preserved regardless of Shown.

Separate append from legacy publication. The runtime writer appends once and publishes the same receipt only with successful durable identity. The interactive publication helper retains the existing interactive gate and ring semantics. Runtime dividers use that helper and the same ring installed on the runtime sink; legacy session transitions retain their current replay behavior. No second durable append or receipt ID allocator.

A cmd-local reader implementing `LatestDisplayableEntryID` walks bounded raw pages newest-first, skipping the four known runtime-only types regardless of Shown. Other entries retain the store's explicit visibility and absent-metadata fallback (including conservative unknown content). Both legacy list and mark handlers receive this view. Raw Store APIs remain unchanged. No cache whose invalidation could race append or reopen.

## Concurrency model

No new goroutine or lock. Interactive publication stays on the existing drain; runtime boundary publication executes there too. The replay ring is installed before workers start, and remains safe for concurrent reconnect reads. The transition producer's own envelope counter remains confined to its publication lane. Storage maintains its existing mutex and filesystem checks; cancellation stops fanout through the existing context.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Runtime-disabled/enabled normal text and completion | `TestLegacyRuntimeReceipts_CompletedTurn` |
| Repeated and overlapping history receipts, facts at edges/middle | `TestLegacyRuntimeReceipts_BoundedPages` |
| Runtime opening, tool/turn interruption, divider and replay overlap | `TestLegacyRuntimeReceipts_LiveReplay` |
| Startup reconciliation and restart divider without session identity | `TestLegacyRuntimeReceipts_StartupDivider` |
| Reopen store, append after watermark lookup | `TestLegacyRuntimeReceipts_Watermark` |
| Confirmed mark already above lower watermark, repeated mark and host/conversation isolation | `TestLegacyRuntimeReceipts_ReadMarks` |
| Nil/failed storage while eligible events still deliver | `TestLegacyRuntimeReceipts_StorageFailure` |

## Error handling

Missing storage or append failure yields no runtime receipt; eligible legacy delivery still proceeds without history identity. Invalid fact identity/shape cannot become harmless accounting. Pager preserves existing error outcomes and one raw cursor/AtStart. Reader errors propagate to existing handler validation/persistence responses. No payload/path logs are added.

## Testing strategy

Write regression first using real Store, HandleFor TextChunk and normal TurnEnd, disabled negative control and unchanged mobile receipt/checkpoint rules. Confirm enabled case fails before production edits. Pin banner required JSON fields, numeric-hole/null/unidentified barriers, receipt-only no-sight, overlap idempotence, all four types, raw metadata preservation, bounded all-runtime pages and warm/reopened stores. Verify live/ring/history share identity and payload and recipient gates; exercise list/mark reader and persistent monotonic registry with host/conversation isolation.

Run focused `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`. The verifier owns `make check` and the full-module gate. Mobile #1989 owns the fresh unchanged full live scenario `InteractiveStreamE2ETest.interactiveTurn_attentionDot_followsARealTurn`; its phone/peer read, isolation, permission waiting/answer assertions and deadlines remain unchanged and pending. No focused authenticated run is required here. PR includes serialized enabled/disabled examples and expected checkpoints.

## Open questions

None. Info-banner compatibility was confirmed against unchanged mobile DTO, reducer, live projection and display test before planning.

## Sizing

One deliverable: legacy read compatibility across known runtime facts. Forecast ≤750 written lines including tests and plan; 0 new exported APIs; ≤10 signature consumer sites (seven opening calls and three runtime recording calls); four acceptance criteria; fewer than ten reject branches. Rechecked against this plan before commit.

## Documentation handoff

Pending documentation stage:
- `docs/protocol-mobile.md`, “A history entry”, “Joining a page to the live stream”, “Marking a conversation read”: shipped receipt shape, exact known-fact scope, shared identity and legacy watermark. Accounting grants no presentation; unknown/malformed/unidentified receipts or holes remain barriers.
- `docs/knowledge/features/history-package.md`, “Reader (#2116)”; `docs/knowledge/features/history-package-producers.md`, “Legacy eligibility and explicit visibility (#2965)”; `docs/knowledge/features/history-package-watermarks.md`, “Unread state uses a separate, lazily recovered watermark (#2954)”: distinguish raw facts/watermarks from the legacy view and retain bounded cursor/AtStart semantics.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] Receipt projection uses a closed four-type classifier and validated fact ownership/identity. Unknown or malformed facts cannot certify a hole harmless. Unchanged mobile required banner fields and read barriers are the compatibility boundary.
- [Tokens, cryptography] No credential/key/nonce operations; receipts remain inside existing authenticated Noise envelopes. No raw runtime payload is forwarded.
- [File operations] Reader delegates cursor, UUID and containment checks to Store.Page; no new path construction or disk mutation. Read persistence remains the existing registry atomic write.
- [Subprocesses] No new subprocess execution or environment access.
- [Network and I/O] History response remains exactly one bounded raw page; watermark scans retain bounded memory. Existing transport limits, membership and interactive gates are reused.
- [Errors and telemetry] No payload, credential, cursor or filesystem-path logging is added; existing content-free append/pager failures retained.
- [Concurrency] Ring installation precedes workers; no goroutine or lock introduced. Failed writes cannot mint receipts. Reconnect uses copied durable IDs under the existing ring mutex.
- [Threat model] Protocol Security model prompt injection/relay MITM/device revocation protections are unchanged: receipts contain no executable input or credentials and cannot grant presentation or permissions. This ticket adds no authority or authentication surface.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-09

## Revisions

2026-10-09: The merged `reconcileStartupHistory` emits a `daemon_restart` divider without predecessor/successor session IDs. Accept that captured occurrence and conversation identity in `legacyRuntimeReceipt`; other divider causes still require a predecessor. `TestLegacyRuntimeReceipts_StartupDivider` covers the real startup writer and reopened pager. The unchanged mobile checkpoint rules are modeled in Go; the actual mobile full live proof remains pending.

2026-10-09: Verifier finding 1 identified stale `TestRelayV2_ConversationHistory` expectations. Its three walks now require exactly one startup receipt with all five nonvisual payload fields and the original durable ID/timestamp, alongside exactly-once seeded content. Raw cursor/AtStart, foreign-conversation and invalid-cursor rejection, interrupts and deadlines remain asserted; unknown-fact exclusion tests remain unchanged. No production contract changed.

2026-10-09: The live gate attributed `TestInteractiveStreamHookBlockedBannerReachesTheClient` to this branch after reproducing it here and passing it on main. The refusal arrived and the following unblocked turn completed; the obsolete one-banner count rejected that turn's opening receipt. Retain every banner and require exactly one refusal plus one opening receipt, including all five required nonnull payload fields, durable/replay identity, timestamp and placement after the unblocked ack and before assistant text. Hook witnesses, refused-turn lifecycle absence, subsequent completion and existing deadlines remain asserted. No production contract changed; rerun the named live test through the dispatcher launcher.

2026-10-09: Completed the interrupted live-gate repair. The dispatcher launcher ran `^TestInteractiveStreamHookBlockedBannerReachesTheClient$`: 1 executed, 1 passed, 0 failed, 0 skipped. Tagged offline `TestSourceCitation*` race checks also passed (131 leaf tests). Updated the window comments to describe exact banner counts. The dispatcher's fresh full live gate and mobile #1989 proof remain pending.
