# Complete channel posts before waking devices

## Files read

- `cmd/pyry/channel_delivery.go` → `deliver`, `drain`, `lockPostBoundary`: durable reconciliation and serialized publication boundary.
- `cmd/pyry/channel_delivery_test.go`, `channel_delivery_hold_test.go`: retry, carry independence, startup and successor precedence.
- `cmd/pyry/channel_post_v2.go` → `announce`: capability-gated live fan-out.
- `cmd/pyry/relay.go` → `startRelayV2`; `main.go` → `runSupervisor`: shared replay/waker ownership and hook installation before consumer startup.
- `cmd/pyry/interactive_turn_v2.go` → `emit`, TurnEnd handling: event-id and wake ordering; best-effort history is unsuitable for posts.
- `internal/eventring/ring.go` → `Append`, `After`: bounded, connection-independent replay ids.
- `cmd/pyry/push_wake.go` → `Trigger`, `wakeAbsent`: eligibility, suppression and coalescing remain owned here.
- `internal/e2e/channel_post_live_test.go`, `internal/e2e/internal/fakerelay/fakerelay.go` → held-turn proof and `binaryRecvPump` routing.
- `docs/knowledge/features/control-plane-channel-post-live-delivery.md`: complete-page reconciliation, hold deadline and carry separation must survive this change.
- `docs/knowledge/features/control-plane.md`, `e2e-harness.md`, `development-verification.md`; `docs/protocol-mobile.md` → Security model: integration, validation and trust boundaries.

## Context

A lone post delta renders but cannot finish its bubble or notify a device. This adds one completed host-authored post at the existing safe delivery boundary. Historic posts are untouched; tail catch-up remains #2744. No decision record is required. No overlapping remote feature branch touches the planned files.

## Design

Use a private host-post completion type containing exactly `conversation_id`, `turn_id`, `stop_reason: "end_turn"`, and `producer: "channel_post"`. Leave Claude's `TurnEndPayload` unchanged.

`deliver` reconciles every durable history page for matching chunks and the exact completion. Append missing chunks, then missing completion; publish only after all writes succeed. Existing complete pending records are cleanup-only. Delta-only records now need completion and publication. The post emitter retains its delta hook and adds a completion hook; relay wiring installs the latter and shares the interactive emitter's ring and existing waker before the delivery consumer starts. Each event enters the shared ring before its live fan-out, with its assigned id on every envelope. Completion fan-out precedes `pushWakeTurnEnd` for the post's own conversation, including with no clients. Replay only reads retained events and cannot trigger a wake.

Sizing: one deliverable, four acceptance criteria, approximately 700 written lines including tests/plan, no new exports, two production wiring consumers, under ten error/reject branches. Keep harness observation local to actual outbound wake requests.

## Concurrency model

All history, replay, live publication and completion trigger calls stay inside the existing delivery mutex shared with the stream boundary gate. Ring and waker retain their internal locks. No new production goroutine; the existing delivery consumer and waker exit on daemon cancellation and are joined. Child writes continue outside the delivery gate.

## Error handling

History failures retain accepted pending work and suppress all publication/wake. Retry scans matching identities without duplicate durable events, including completion-only retry. Cleanup failures retain the existing in-memory delivered flag; reload recognizes complete history and performs cleanup without publication. Fan-out failures retain history/replay recovery and do not fail acceptance. Hold deadline still diagnoses and keeps holding.

## Testing strategy

Extend reload scenarios through all deltas/no completion and complete/no cleanup; fail completion recording and prove automatic retry with no premature live/wake. Inspect exact raw completion keys in history, replay and live; compare replay ids across connections and observe recording/fan-out/trigger order. Preserve existing carry, startup and gate tests, maximum-byte reconstruction and Claude payload assertions. Extend the held fake-daemon test through post completion and successor ordering. Add disconnected eligible-device outbound-wake proof, then reconnect using a cursor predating the post in the daemon's routed active conversation and inspect replay and served history. Run scoped race tests, tagged fake-daemon channel tests, vet and binary build; full-module gate belongs to the verifier.

## Open questions

None; use existing routing to establish the replay conversation and existing wake eligibility.

## Documentation handoff

Pending for documentation stage:
- Revise **A fresh `turn_id` per post** and **Live-only announcements and durable recovery** in `docs/knowledge/features/control-plane-channel-post-live-delivery.md`, and the channel-post summary in `docs/knowledge/features/control-plane.md`, for completion, shared replay, recording-before-wake and safe delivery boundary; state cleanup/restart and bounded replay limits.
- Under **Revisions** in `docs/specs/architecture/2498-channel-post-live-delivery.md`, supersede no-lifecycle/no-ring decisions; record the exact four completion fields, daemon provenance and omitted Claude fields.
- In `docs/protocol-mobile.md`, **Interactive events (v2, capability-gated)** / **`assistant_delta`**, **`turn_end`**, and **Replay cursor**, document host-post producer, completion exception and replay event ids, preserving Claude's contract.
- Preserve the premise that a lone delta renders live/from a served page but does not finish/notify; tail catch-up is #2744. Ten historic posts retain recoverable text and missed alerts without migration or retroactive notification.

## Security review

**Verdict:** PASS

**Findings:**
- Trust boundaries: `channelPoster` keeps validated channel input and fresh daemon ids; private completion explicitly identifies daemon provenance and cannot claim Claude results.
- Tokens/secrets: reuse `pushWaker` eligibility and send path; no credential lifecycle changes or token logging. Wake observation is test-only and never logs tokens.
- Files: reuse composition-root history and existing private pending file validation, `O_NOFOLLOW`, 0600 temporary files and atomic rename; no caller-selected paths.
- Subprocesses: no command or environment changes; held-turn tests use the existing fake process and shutdown hooks.
- Cryptography: reuse fresh `conversations.NewID` and authenticated encrypted relay transport; no new primitive or nonce use.
- Network/I/O: reuse bounded delta splitter, envelopes, replay ring, nonblocking manager Push and content-free wake request. No new listener/read path.
- Errors/logs: preserve fixed errors/content-free identity logs, never raw post text or storage errors.
- Concurrency: delivery gate spans reconciliation through wake trigger; interruption is recoverable; no new goroutine or check/write race.
- Threat model: relay sees ciphertext for post events and content-free wake metadata; remote display sanitization and tail catch-up remain existing client responsibilities/#2744.

**Reviewer:** builder (self-review)
**Date:** 2026-10-05
