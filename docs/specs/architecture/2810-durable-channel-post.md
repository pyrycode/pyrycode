# #2810: Durably accept and recover whole channel posts

## Files read

- `cmd/pyry/channel.go` → `channelPoster`: lookup, fresh turn identity and bounded delta chunking.
- `cmd/pyry/channel_post_test.go` → `newTestPosterCarrying`, maximum-envelope tests: preserve lookup/chunk contracts while changing acceptance failure tests.
- `cmd/pyry/channel_carry.go` → `record`, `carryPending`, `clearDelivered`: best-effort Claude carry is independent of client delivery.
- `cmd/pyry/main.go` → `runSupervisor`: single history store, queue construction before relay setup, no-relay lifecycle.
- `internal/history/log.go` → `Append`, `Page`: failed writes preserve prior entries; pagination is newest-first and restart durability excludes machine crashes.
- `internal/conversations/registry.go` → `Save`: same-directory temporary file and atomic rename pattern.
- `docs/knowledge/features/control-plane.md`, `control-plane-channel-post-live-delivery.md`: lone deltas render; carry clearing must preserve concurrent posts.
- `docs/knowledge/features/history-package.md`: reuse the one Store; never confuse history and event-ring identities.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: content-free errors, behavior tests, scoped checks.

## Context

A later append failure currently refuses a post after recording its prefix. Acceptance must instead atomically preserve the entire post before delivery. This is one deliverable: durable posted messages, including their idle consumer and recovery. No decision record is needed. No concurrent remote feature branches overlap these files after fetch.

Sizing: approximately 750 written lines including tests and this plan, zero exported types, two existing constructor consumers, four acceptance criteria, fewer than ten reject/retry branches. Recounted against this plan before commit; all limits hold.

## Design

Add private `channelDelivery` and its persisted post records in `cmd/pyry/channel_delivery.go`. Its fixed instance-local file stores ordered whole-text posts with conversation ID, fresh turn ID and acceptance timestamp. `accept` saves a candidate queue atomically before publishing it in memory, recording carry once, and waking the consumer. Save refusal leaves the old queue intact and reaches the poster as a static content-free error. The poster retains lookup and identity minting, delegating acceptance to this seam.

One consumer visits queue heads in durable acceptance order. A failed conversation head suppresses later posts for that conversation during that pass; other conversations continue. Each attempt walks every history page, recognizing matching conversation/turn/sequence/text deltas, and appends only missing chunks in ascending sequence. All history chunks precede any announcement. A fully recorded recovered post is cleaned up without append or announcement. In-memory completed records suppress repeat announcements if cleanup persistence fails. Only assistant_delta is served.

Load pending state before msgqueue construction. Wrap its inbound Deliver seam with a conversation-scoped wait for pending history delivery, before carry composition and user-turn writing. Recovery can therefore wait for the consumer started after relay construction without letting inbound delivery overtake it. Start the consumer independently of relay configuration and join it on every return. Existing active-turn delivery behavior stays unchanged; #2811 owns holding posts during active turns.

Carry is called once immediately after new acceptance; retries and load never call it. It remains best effort and bounded. Its clear cannot remove pending delivery, and pending cleanup cannot remove registry carry. Process-restart durability matches history; missed live pushes remain available through history.

## Concurrency model

A queue mutex serializes acceptance, disk snapshots and the sole consumer's per-pass work. No delivery/carry lock is held while waiting for a user turn. The inbound wrapper checks only its conversation and waits with context cancellation. Consumer wake signals and a retry timer trigger work; daemon cancellation stops it, with a done channel joined by the composition root.

## Error handling

Atomic acceptance failures refuse without appending, announcing or recording carry. History read/write and cleanup failures leave durable work pending and retry automatically. Malformed pending state fails startup before inbound delivery. Errors/logs omit content, raw I/O errors, chunk counts and sequences. A history prefix may be visible until retry completes, as the ticket permits.

## Testing strategy

Write failing tests before production changes. Preserve existing lookup, next-turn carry, maximum size and envelope assertions. Add on-disk tests for atomic refusal preserving accepted work; concurrent FIFO and byte identity; recording before first announcement; later-chunk failure with nil announcer and automatic retry; failed head with another conversation progressing; reload with zero, partial and complete history prefixes hidden behind newer pages; startup user-turn precedence and unaffected conversation progress; both directions of carry independence and no re-add on retry/recovery. Run race tests for cmd/pyry, go vet ./..., and go build ./cmd/pyry; the verifier owns the full-module gate.

## Open questions

None. Recovery deliberately does not re-add carry across the acceptance/carry crash window: carry retains its existing best-effort contract.

## Documentation handoff

Pending for the documentation stage:
- `docs/knowledge/features/control-plane-channel-post-live-delivery.md`, **A fresh turn_id** and **Live-only**: durable whole-post acceptance, immediate idle delivery, FIFO ordering, retry/prefix reconciliation and process-restart recovery; separate pending delivery from carry; preserve lone-delta rendering and #2809 completion/replay/wake ownership; #2811 owns active-turn holding/deadline.
- `docs/knowledge/features/control-plane.md`: update the channel-post link summary and **Lifecycle** to describe retained socket ownership through writer quiescence.
- `docs/knowledge/features/control-plane-channel-post-live-delivery.md`: refresh the introduction's producer/wiring descriptions to match the sole consumer.
- `docs/specs/architecture/2498-channel-post-live-delivery.md`, **Revisions**: new acceptance/recovery contract, leaving turn-boundary changes to #2811.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] `handleChannelPost` keeps its existing input bound; `channelPoster` resolves registry identities and mints turn IDs. Loaded records validate IDs and text bounds before use.
- [Tokens and cryptography] No credentials added; IDs continue using `conversations.NewID` and crypto/rand.
- [File operations] Private content is written at 0600 beneath the daemon-owned instance directory, with same-directory temporary file, sync, close and rename. Read refuses a symlink final component using O_NOFOLLOW; rename replaces rather than follows it. No caller-authored path is constructed. Host-owner directory replacement remains outside the existing history threat model.
- [Subprocesses] No new subprocess or argv path; existing carry composition remains unchanged.
- [Network and I/O] Reuse existing bounded control input and delta envelope sizing. No new socket or network receipt guarantees.
- [Errors and telemetry] Static refusal and event/reason logs only; never raw storage errors, content, chunk count or sequence.
- [Concurrency] One mutex and consumer protect durable ordering; cancellation and join cover every worker lifetime; reconciliation covers interruption at each append and cleanup.
- [Threat model] Posted text remains untrusted render input under protocol Security model threat 1; client sanitization stays client-owned. No new relay/auth boundary. Completion/replay/wake deferred to #2809, active-turn holding to #2811.

Rework review (2026-10-05): The verifier exposed a missing instance-ownership boundary in the original file-operations/concurrency review. `control.Server.Listen` must succeed before pending state is loaded and before relay or delivery consumers start. `runSupervisor` now enforces that ordering, closes its listener on early failure, and retains cancel-then-join shutdown. Rejected startup tests cover unchanged pending bytes/history and ownership refusal ahead of malformed-state loading. Verdict after this correction: PASS.

Shutdown rework review (2026-10-05): PASS. Socket ownership now spans pending-state load through writer quiescence. The control context is detached from daemon cancellation and is cancelled only after post callbacks are sealed/joined and the delivery consumer has stopped; late acceptance cannot overwrite replacement state. The dated shutdown revision records the lock order and two failing-before-fix writer probes. All other security categories retain the decisions above.

**Reviewer:** builder (self-review)
**Date:** 2026-10-05

## Revisions

- 2026-10-05: Read `cmd/pyry/channel_post_v2.go` → `channelPostEmitterV2` during wiring review and refresh its producer/recovery comments. Its fan-out contract is unchanged. Final written-work recount is approximately 770 lines, with zero exports and two updated constructor consumers.
- 2026-10-05 (verifier rework): The MUST FIX startup probe showed that a rejected second daemon could drain another daemon's pending snapshot before `control.Server.Listen` refused ownership. `runSupervisor` now constructs and binds the control server after pool construction, before loading pending delivery or starting queue/relay consumers. Pending state is read fresh only after ownership; control `Serve` still starts after all hooks are installed. An early startup failure closes the claimed socket. This supersedes the original startup ordering in Design and strengthens the Concurrency model: only the socket-owning daemon may load or consume pending delivery. Security re-review of file operations and concurrency: PASS with this ownership boundary; the content, permissions and recovery contracts remain unchanged. `TestChannelDelivery_RejectedDaemonLeavesPendingUntouched` checks unchanged pending bytes and history across repeated rejected starts, including malformed pending state that proves ownership precedes load. Read `internal/control/server.go` → `Listen`, `Serve`, `Close` for the bind/serve split and cleanup, and `docs/specs/architecture/1492-cancel-daemon-ctx-before-relay-cleanup.md` for cancel-before-join behavior. No concurrent feature branch overlaps the changed files. The emitter NIT is resolved by naming `channelDelivery.deliver` and its sole consumer in the fan-out comments.

- 2026-10-05 (shutdown verifier rework): The MUST FIX replacement-start probe showed that `control.Server.Serve` releases the instance socket on daemon cancellation while delivery cleanup or an in-flight acceptance can still persist an old snapshot. `runSupervisor` now gives control serving a separately cancelled context and retains its listener until all writer teardown defers finish. `channelDelivery.guardPoster` holds a read gate for the entire post callback; `stopAccepting` takes the write gate, joins callbacks and seals acceptance under the queue mutex. Lock order is handler gate then queue mutex; the consumer uses only the queue mutex. Normal shutdown seals after daemon cancellation, and error returns seal through defers. Consumer cancel/seal/join precedes control cancellation, listener close and response-handler join on every return. Late callbacks and direct acceptance receive the existing static refusal and perform no storage/carry work. This supersedes the shutdown ordering in the original Concurrency model and startup rework review. `runSupervisor` accepts a private optional delivery constructor for composition-root tests; production still constructs exactly one delivery consumer over the existing history store. `TestChannelDelivery_ShutdownRetainsOwnershipUntilWritersStop` pauses both an append and an acceptance save, proving replacement startup is refused until the writer finishes and a replacement's new durable post survives. Both scenarios failed before this correction with a lost replacement post; `TestChannelDelivery_ShutdownRefusesLateAcceptance` covers the sealed gate. Security re-review: PASS after fixing the file-ownership/concurrency boundary through teardown; trust validation, credentials/crypto, file modes/symlinks, subprocesses, bounded I/O, content-free telemetry and deferred protocol threats retain their reviewed contracts. No exported API or schema change. Documentation stage should also refresh the producer/wiring introduction of `docs/knowledge/features/control-plane-channel-post-live-delivery.md` and the **Lifecycle** section of `docs/knowledge/features/control-plane.md` to describe socket ownership through writer quiescence. This required verifier repair adds approximately 230 written lines beyond the original build estimate; no additional deliverable is introduced.
