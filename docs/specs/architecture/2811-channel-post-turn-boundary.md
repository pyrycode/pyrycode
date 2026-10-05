# #2811 — hold durable channel posts until the published turn boundary

## Files read

- `cmd/pyry/channel_delivery.go` → `accept`, `drain`, `deliver`, `beforeInbound`: durable FIFO, prefix reconciliation and the current snapshot race.
- `cmd/pyry/channel_delivery_test.go` → reload, retry, carry and FIFO tests: reuse these proofs and helpers.
- `cmd/pyry/stream_turn_busy.go` → `observe`, `clearForExit`, `clearForSession`, `openForDelivery`, `openForSendNow`: preserve exit epochs and send-now carry/grace.
- `cmd/pyry/stream_turn_drain.go` → `startStreamTurnDrainV2`: sole emitter writer and queued teardown closes.
- `cmd/pyry/interactive_turn_v2.go` → `HandleFor`, `closeForConversation`: completion publication and buffered-text flushing precede release.
- `cmd/pyry/main.go` → `runSupervisor`, `newInboundDeliver`: startup ownership, consumer installation and normal turn writes.
- `cmd/pyry/send_now.go` → `newSendNowDeliver`: busy check, carry and write cancellation.
- `cmd/pyry/session_reset.go` → wrap-up delivery: the other normal turn-start seam.
- `cmd/pyry/relay.go` → `startRelay`, `startRelayV2`: no-URL early return currently leaves the stream undrained.
- `internal/e2e/channel_post_live_test.go` and `internal/e2e/relay_v2_stream_background_conversation_test.go`: attached phone, served history and release-file scaffolding.
- `internal/e2e/internal/fakeclaude/main.go` → `streamReplay`: existing two-fragment gate needs no changes.
- `docs/knowledge/features/control-plane-channel-post-live-delivery.md`: preserve delta-only delivery, all-pages reconciliation and carry independence.
- `docs/knowledge/features/streamsup-package.md`, `docs/knowledge/features/e2e-harness.md`, `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: existing lifecycle, harness and testing contracts; gates must prove actual publication rather than a busy snapshot.

## Context

#2810 supplies durable whole-post acceptance. Its consumer currently inserts posts inside active Claude output, and its inbound snapshot can race acceptance. Clients require posts between turns. This extends that one delivery contract without adding post completion, replay or wake (#2809), or tail catch-up (#2744). No new decision record is needed.

Sizing: one deliverable, approximately 730 written lines (260 production, 390 tests, 80 spec), zero exported types, three production start callers, four acceptance criteria and fewer than ten new reject/error branches. No overlapping feature branches were found for the planned files.

## Design

Bind the existing tracker and consumer together before publishing inbound delivery. The consumer mutex becomes the boundary gate: acceptance, a post's complete record/announcement, and a turn's busy mark plus write reservation are serialized under it. Never hold that mutex across a child write or an idle wait.

The consumer retains a per-conversation published-active mark, separate from the tracker's busy membership, and an in-flight-write count. Openers and normal inbound/reset starts set the published mark. Only real completion after `HandleFor`, accepted exit after `closeForConversation`, or drained pool teardown clears it. This prevents an internal idle snapshot or asynchronous teardown clear from releasing a post early. The existing epoch check decides whether an exit is accepted.

`beginDelivery(ctx, conversationID)` reserves an ordinary/reset turn only when that conversation has no pending post, active published turn, busy mark or in-flight write. It returns undo and write-finished callbacks; cancellation writes nothing. The check and existing `openForDelivery` mark occur under the consumer gate. `beginSendNow` makes the existing busy check/carry and a write reservation atomic with post delivery; its finished callback releases the reservation on every path. The tracker remains responsible for send-now carry/grace and real busy state.

The drain gates each event's tracker observation and emitter handling together, then releases the published mark after completion publication. Accepted exits and queued teardown closes use the same gate and flush/close first. The consumer skips held conversations while continuing FIFO work in idle ones; failed history writes retain pending precedence over all successor starts. Recovery starts with no live marks and preserves durable identities and existing prefix reconciliation before any new turn reservation succeeds.

Without a relay URL, `startRelay` still constructs a history-backed interactive emitter with an empty broadcaster and starts the same stream drain. Pool teardown retains its busy/lifecycle notification in this configuration. No client is required for publication into history.

Hold deadline: **five minutes from durable acceptance**. Once crossed while held, emit one content-free diagnostic per post per process, then keep holding. Inject the clock for deterministic tests. No expiry path clears busy, interrupts, delivers or discards the post.

## Concurrency model

Keep the existing consumer and stream-drain goroutines and their cancellation/join paths. All emitter calls remain on the stream-drain goroutine. Lock order is handler gate → consumer mutex → tracker mutex; tracker callbacks must not acquire the consumer mutex while holding the tracker mutex. Writes run outside the consumer mutex, with reservations preventing delivery until their return. Waits poll with a cancellable ticker and hold no mutex while parked. Pool callbacks only enqueue closes; the stream drain applies publication closes under the gate.

## Error handling

Acceptance and retry retain existing fixed, content-free refusals/logs. History failure keeps the whole post pending and blocks successor turns even after a recorded prefix. Failed child writes invoke the existing undo and always finish their reservation. Cleanup failure preserves delivered markers. Shutdown leaves durable pending work for startup reconciliation.

## Testing strategy

Write failing unit scenarios first: completion publication blocked behind a test gate; exit/teardown flush before release; stale exit behind a newer mark; independent idle delivery; deadline exceeded with a live turn; competing starts/acceptance; partial-history retry blocks successor starts; in-flight send-now even through a carried close; on-disk reload with competing inbound delivery and unchanged identity.

Reuse existing identity, maximum-size/envelope, FIFO, all-recorded-before-announcement and carry-independence assertions. Add one release-file-gated fake-Claude daemon regression verifying successful acceptance while held, absence from live frames and served history, then intact Claude reply and real completion before the separate post on both paths. Exercise no-relay production wiring with unit coverage.

Run race tests on `cmd/pyry` and the targeted `internal/e2e` test with its build tag, then `go vet ./...` and `go build ./cmd/pyry` (output outside the worktree). The verifier owns the full-module gate.

## Open questions

None. Completion/replay/wake and history tail catch-up remain explicitly deferred.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/control-plane-channel-post-live-delivery.md` § **A fresh `turn_id` per post** and **Live-only announcements and durable recovery**: durable busy acceptance, FIFO whole-post delivery before successor turns, completion/exit/teardown release, safe startup recovery and five-minute diagnostic deadline. Expiry never delivers or ends a live turn. Preserve immediate idle delivery, channel-carry separation and lone-delta rendering live/history; completion/replay/wake belong to #2809.
- Pending documentation stage: update the channel-post link summary in `docs/knowledge/features/control-plane.md`.
- Pending documentation stage: `docs/specs/architecture/2498-channel-post-live-delivery.md` § **Revisions**: replace unconditional immediate delivery with this contract.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Existing `channelPoster` and control validation retain validated channel names, 64-KiB text cap and daemon-minted identities. Child routing remains daemon-resolved; stale exits use `clearForExit` epochs.
- [Tokens and cryptography] No new credentials or crypto. Post IDs retain `crypto/rand`; posted text never becomes a diagnostic field.
- [Files] Reuse the fixed private pending path, `O_NOFOLLOW` reload, 0700 directory, 0600 temporary file and atomic rename. No new caller-derived path or persistence format.
- [Subprocesses] No new subprocess invocation or environment change; child writes use existing typed writers.
- [Network and I/O] Existing control size caps and relay envelope splitting remain. No new listener or network authority.
- [Errors/logs] SHOULD FIX: deadline diagnostic must contain only fixed event text and conversation/post identities, never storage errors, posted text, chunk counts or sequences; test with secret-bearing text/errors.
- [Concurrency] SHOULD FIX: use one gate across final pending check and busy/write reservation, and across tracker observation and published completion. Reserve in-flight send-now writes even if grace expires before the write returns. Tests force these interleavings.
- [Threat model] Existing authenticated relay and local socket boundaries remain; client replay/wake (#2809) and tail catch-up (#2744) are OUT OF SCOPE. No expiry action may mutate a still-live turn.

**Reviewer:** builder (self-review)
**Date:** 2026-10-05

## Revisions

- 2026-10-05: pool teardown can arrive while its child's tail is still queued in the fan-in. With the post gate bound, drain those queued events before consuming the teardown close, then close publication and retire any busy mark those old events reopened. Successor writes remain gated throughout. This extends the planned buffered-text flush to queued text as well, without changing the legacy unbound tracker path.
