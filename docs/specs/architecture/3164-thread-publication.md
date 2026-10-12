# Negotiated history-fold publication (#3164)

## Files read
- `cmd/pyry/thread_shadow.go` → `follow`, `remove`, `shutdown`, `shadowQuiescent`: joined per-conversation coordination and unload boundaries.
- `cmd/pyry/relay.go` → `startRelayV2`: install before manager admission; stop before manager join.
- `cmd/pyry/daemon_live_relay.go` → `installDaemonLiveRelay`, `startDaemonLivePublisher`: preserve supplied source-bound readings.
- `internal/thread/observations_store.go` → `Observe`, `Changes`: complete baselines and bounded retained continuity; epoch reuse does not prove ranges.
- `internal/thread/observations.go` → `Change`: apply replacements before text, including combined updates.
- `internal/protocol/thread.go`, `thread_encoding.go` → DTOs and `EncodeThreadUpdate`: wire names and atomic continuation assembly.
- `internal/relay/v2session_thread.go`, `v2session_push.go`, `v2session_livestate.go` → delivery gates, final projection, source admission and `RetireLiveState`; merged #3195 preserves conversation routing.
- `cmd/pyry/thread_shadow_legacy_test.go`, `daemon_live_relay_test.go`, `agent_switch_frames_test.go`: authenticated offline fixtures.
- `docs/knowledge/features/thread-package.md` § Shadow lifecycle and evidence; `thread-package-background-store.md` § Loading and replay readiness: unload destroys retained ranges even with unchanged epoch.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, `streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md`: supplied state ordering/access and producer independence.
- `CODING-STYLE.md`, `docs/knowledge/features/protocol-package.md`, `development-verification.md`, ADR 042 and `docs/protocol-mobile.md` § Security model: inert display content and proof boundaries.

## Context
Activate ADR 042's live fold publication using merged observations, encoder and supplied live-state wiring. Catch-up, pages and reconnect initialization remain #2963. No new ADR. No concurrent feature branch touches the planned existing files.
Sizing: one deliverable; approximately 760 written lines (270 production, 410 tests, 80 plan), zero exported types, two composition sites, four acceptance criteria, at most eight reject branches. This reuses existing encrypted fixtures rather than adding a harness.

## Design
Install an optional daemon publisher on the shadow owner before coordination starts. Readiness attests the installed publisher, usable watermark provider and supplied live snapshot. Missing any provider keeps negotiation disabled. Watermarks return true only for registered usable observations, including zero.
Each coordinator owns its publication cursor and recipients. Start with `Observe` full additions, then request `Changes` only when usable progress exists. Never derive changes from emitter facts. On nonusable state, epoch/range loss or encoding failure, retire prior recipients, forget the dependent cursor, and obtain a fresh usable baseline. Unload retains the cursor so reload must prove continuity or retire.
Translate fold field names through `protocol.ThreadItem`'s JSON tags. For combined fields/text emit replacements with the final revision first, followed by text with that same revision as base and result; no history ID is invented. Encode all logical updates with `EncodeThreadUpdate`, preserving consecutive continuation parts.
Discovery and follow each wait at most 50 ms; no additional text timer. Store batches supply coalescing, and every ordered change is flushed immediately before any later non-text change. Total scheduled waiting is at most 100 ms, inside 250 ms. Peer delivery and encoding run only in coordinators, outside history writers and producer locks.
Relay rechecks membership immediately before thread item delivery, along with existing authentication, interactive/thread and agent gates, so queued output cannot leak after removal.

## Concurrency model
Existing discovery and per-conversation goroutines own folding coordination and publication. Publisher admission uses the shadow mutex, releases it before observation/encoding/delivery, and tracks admitted operations for joined stop. Operation cancellation follows both publisher and conversation retirement. Stop seals admission, cancels operations, joins, then frees the installation slot; readiness becomes false before teardown. History commits never call delivery. Deletion cancels and joins the coordinator before unloading.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Baseline then repeated updates, combined replacements/text | `TestDaemonThreadPublication` |
| Multiple conversations/peers, legacy and current readings | `TestDaemonThreadPublication` |
| Unusable recovery and lost retained range/epoch | `TestDaemonThreadContinuity` |
| Idle unload/reload, commit racing unload | `TestDaemonThreadRetirement` plus existing `TestThreadShadowReload` |
| Delete with delivery held; shutdown and duplicate installation | `TestDaemonThreadRetirement` |
| Removed/agent-ineligible conversation queued delivery | `TestDaemonThreadPublication` |

## Error handling
Nonusable storage publishes neither partial items nor watermark. Lost continuity retires affected consumers through `RetireLiveState` before reacquisition. Push/retirement failures use content-free diagnostics; cancelled operations exit. Existing store retry/cache failure behavior remains intact. Encoding failure cannot leave a cursor claiming a fully published range.

## Testing strategy
Write failing focused tests before implementation. Decrypt authenticated frames from simultaneous thread and legacy peers; apply full additions, field changes and appends and compare to store items. Verify retained source readings, withheld content, wire names, dropped items, continuation reconstruction/envelope caps, readiness and zero watermarks, range/epoch loss, cancellation joins and writer progress during held publication. Use focused race tests, then `go test -race ./cmd/pyry/... ./internal/relay/... ./internal/protocol/...`, `go vet ./...`, `go build ./cmd/pyry`. Full module gate belongs to verifier; no live Claude run.

## Open questions
None; combined updates use consecutive same-result-revision messages under the existing DTO contract.

## Documentation handoff
Pending documentation stage:
- `docs/protocol-mobile.md`, “Capability negotiation”, thread message sections/table and live-state sections: production emitted publication, total coalescing bound, actual continuation assembly, continuity-loss retirement and legacy fallback. Keep catch-up/pages and reconnect initialization pending #2963.
- `docs/knowledge/features/thread-package.md`, “Shadow lifecycle and evidence”, and `docs/knowledge/features/thread-package-background-store.md`, “Loading and replay readiness”: installed publication, complete usable baselines, proven ranges and retirement/recovery on continuity loss.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, “Thread delivery and summary projection” and “Supplied live-state ordering”, and closing relay handoff of `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md`: production negotiation/publication versus pending #2963 catch-up/pages/replay replacement.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: inert history content enters only reviewed DTO encoding; relay authentication, membership and agent gates remain authoritative. SHOULD FIX: recheck membership for queued thread items in `threadWithheld`.
- Tokens/cryptography: no new credential/key/nonce handling; existing Noise manager seals each admitted recipient frame.
- File operations: no new paths or I/O; store retains existing containment and atomic private cache writes.
- Subprocesses: none added; tests use hermetic authenticated fixtures.
- Network/I/O: encoder reserves worst-case relay stamps and escaping under 65519 bytes; existing queue ceilings retire slow consumers.
- Errors/logs: fixed event/reason diagnostics only; no payloads, secrets or host paths.
- Concurrency: no I/O under publisher admission mutex; cancellation and joins prevent duplicate/removed publishers. Held delivery must honor cancellation.
- Threat model: output remains untrusted display data; no new actuation or privileges. #2963 owns catch-up and reconnect protocol work; existing prompt-injection and relay availability residual risks remain unchanged.
**Reviewer:** builder (self-review per checklist)
**Date:** 2026-10-12

## Revisions
2026-10-12: Retirement captures its boundary before publication. A commit while delivery is held must exceed that saved boundary and reload, rather than being marked consumed without publication; `TestDaemonThreadIdleCommit` covers the race. Recipient tracking is refreshed on each publication to avoid accumulating disconnected peers. Focused lifecycle coverage brings written work slightly over 800 lines; these checks belong to the single publisher deliverable and have no independent slice, so the handback floor rule keeps them here.

2026-10-12, verifier finding 1: `drainOnce`, `drainLiveOnce` and `signalLiveDrain` quarantine output for a latched queue before Run handles its existing gap teardown. `TestRetiredThreadQueueDoesNotDrain` authenticates a held connection and deliberately drains before processing the ready teardown, proving dependent deltas cannot reach the wire.

2026-10-12, verifier finding 2: relay-owned `ActiveThreadConns(ctx, conversation)` enumerates consumers on Run with delivery's shared membership/agent gates and excludes latched queues. Publication retains only eligible prior consumers and records successful enqueue recipients. New `handleConnSnapshot` and the optional `snapshotReq` conversation filter preserve ordinary `ActiveConns`. `TestDaemonThreadGapPreservesAgentIneligiblePeer` proves a Codex continuity gap retires the multi-agent consumer while the basic peer continues authenticated Claude publication. Security review: PASS; no additional access, I/O or goroutines, and negotiated session flags remain Run-owned.

2026-10-12, verifier finding 3: `TestDaemonThreadPublication` explicitly checks the actual combined summary/text fold change after authenticated delivery to both thread peers: replacement precedes append, replacement uses the prior revision, append uses the replacement revision, and reconstructed complete items equal the usable fold observation.
