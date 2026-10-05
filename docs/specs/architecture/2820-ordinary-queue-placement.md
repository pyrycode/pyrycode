# #2820 — place ordinary queued Claude messages at their echoes

## Files read

- `internal/msgqueue/queue.go` → `drain`, `SendNow`, `notifyDelivered`: attempt contexts and the delivery-free queued projection.
- `cmd/pyry/main.go` → `newInboundDeliver`, `runSupervisor`: activation, idle gate, composition decorators and production wiring.
- `cmd/pyry/send_now.go` → `sendNowPlacement`, `newSendNowDeliver`: echo matching and send-now grace fallback.
- `cmd/pyry/channel_delivery.go` → `beginDelivery`, `lockPostBoundary`: write reservations and publication lock ordering.
- `cmd/pyry/operator_message_history.go` → `newOperatorMessageHistory`: safe message payload and one commit timestamp.
- `cmd/pyry/operator_message_v2.go` → `broadcast`, `Run`: asynchronous pushes currently race stream frames.
- `cmd/pyry/stream_turn_drain.go` → `startStreamTurnDrainV2`, `observeEcho`: single stream publisher and ordered completion.
- `cmd/pyry/relay.go` → `startRelay`, `startRelayV2`: relay-disabled history drain and live replay wiring.
- `cmd/pyry/send_now_placement_test.go`, `stream_turn_drain_test.go`, `operator_message_v2_test.go`: placement and race-safe broadcaster harnesses.
- `internal/e2e/realclaude/send_queued_now_placement_test.go` → `TestRealClaude_SendQueuedNowPlacement`: running-turn/fake-phone harness.
- `docs/knowledge/features/history-package.md` § Producers: safe projection is mandatory; asynchronous producer timing does not establish stream order.
- `docs/knowledge/features/msgqueue-package.md`: confirmed deliveries, retry/drop semantics and context propagation.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: per-conversation attribution and history without relay.
- `docs/knowledge/features/development-verification.md`: check production wiring and compile build-tagged live tests.
- `docs/specs/architecture/2730-send-now-placement.md`, `CODING-STYLE.md`: existing placement contract and concurrency conventions.

## Context

Queue confirmations and the operator emitter run independently of Claude's stream. A late confirmation can put an ordinary user message after its reply, even when the echo was observed first. #2819 supplies identity; this change supplies placement for Claude. No new wire event or turn identifier is needed.

Sizing: one delivery-placement behavior; approximately 760 written lines including plan, production and tests, no new exported types/interfaces, four production wiring consumers, four acceptance criteria and fewer than ten reject/failure cases. Existing delivery signatures remain compatible. Queue metadata has one placement consumer and stays in this ticket. No other `origin/feature/<number>` branches are present in the fetched branch list.

## Design

Expose the safe `QueuedMessage` projection on queue attempt contexts through a read-only accessor, for ordinary and send-now writes. Keep `DeliverFunc` and decorators unchanged. Register at the final write boundary after composition, only for stream Claude sessions. Serialize placement registration and writes per conversation so ordinary/send-now matching follows actual write order, including equal payloads and duplicate client message ids.

Extend `sendNowPlacement` to prepare a safe commit before writing. Match only the digest of final delivered bytes; no composed bytes enter the commit. Echo processing waits for the write outcome, then commits synchronously on the stream drain without waiting for `OnDelivered`. Failed attempts cancel registrations. Successful attempts start the existing idle fallback independently of the queue callback. Retain only confirmation bookkeeping until the callback arrives; callbacks acknowledge already managed entries rather than rebuilding them.

The stream sink owns a late-bound synchronous operator publisher and a cancellation-aware command lane for fallback commits. Relay wiring binds the publisher to `operatorMessageEmitterV2.broadcast` with the interactive emitter's ring before starting the drain. History-only wiring needs no live publisher. Serialize the operator emitter's own counter against its existing Run goroutine. On turn completion the drain places confirmed ordinary entries without echoes before releasing idle; send-now entries retain the grace waiter. Echo commits and fallback commands run on the same drain as interactive frames.

Codex/no-stream and callers without queue metadata retain the existing confirmation path. Send-now refusal and carry behavior remain unchanged.

## Concurrency model

Queue attempts retain their existing cancellation contexts. Per-conversation write mutexes cover registration and the final writer call, not history or broadcasting. Placement state has one leaf mutex; no writer, commit or channel operation runs while it is held. Each registered attempt has an outcome signal, released before the inbound write reservation cleanup. Echo/idle may wait on that signal or daemon cancellation. Idle waiter goroutines exit on idle or daemon cancellation and post back through the stream sink; the drain remains the sole publisher for placed entries. The operator emitter's broadcast mutex protects its counter and fan-out from concurrent Run calls.

## Error handling

Resolve/activate/hold failures register nothing. A failed or dropped write removes its registration and records nothing; retry registers afresh. Unknown sessions or unmatched echoes cannot place another conversation's message. A successful write without an echo places once at completion or idle fallback. Late callback/echo after placement produces no second entry. History append failure keeps the existing content-free diagnostic and does not suppress live publication.

## Testing strategy

Write deterministic queue/drain/emitter tests before implementation. Withhold `OnDelivered` after echo and after the answering end is produced, verify user placement before the first reply frame and compare live payloads/timestamps/IDs against history and replay. Cover equal delivered payloads and duplicate client ids, cross-conversation echoes, composed delivery versus safe text, ordinary/send-now interleaving, no echo, retry and non-Claude confirmation timing. Keep existing send-now tests green. Add a live daemon/fake-phone ordinary queue placement test and compile the live package offline; dispatcher runs it and `TestRealClaude_SendQueuedNowPlacement`, reporting executed counts and skip reasons.

Run race tests for touched packages, `go vet ./...` and `go build ./cmd/pyry`; full-module race gate belongs to verifier.

## Open questions

None; live behavior is verified by the dispatcher-owned live gate.

## Documentation handoff

Pending for documentation stage: update `docs/protocol-mobile.md`, the `message` description under **Application message types** and **Queue (v2)**. State that an echoed ordinary Claude delivery opens its answering turn in stream/history/replay order, with an exactly-once idle fallback when no echo arrives. Preserve the send-now guarantee and document Codex/no-stream commit-at-write timing. Older daemons omit `queued_msg_id`, so clients retain current inference as fallback; field presence is an identity claim, not proof of timing on Codex or the no-echo path. Reconcile `send_queued_now`'s `sent_now` paragraph about late ordinary confirmations with the new guarantee. Add a dated **Changelog** entry.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Safe queued projection is the only payload source in `newOperatorMessageHistory`; final composed bytes supply only a private digest. Conversation identity comes from daemon session resolution.
- [Tokens/secrets/cryptography] No credential changes. SHA-256 is equality metadata, never authentication, and never emitted or logged.
- [File operations] Existing history containment and persistence are reused; no new caller-controlled paths.
- [Subprocesses] Existing writer receives opaque composed bytes; no new process, shell or environment handling.
- [Network/I/O] Existing admitted queue/frame bounds and broadcaster remain; command-lane sends observe daemon cancellation.
- [Errors/logs] Message text, composed prompts, host paths and digests must not be logged; existing content-free diagnostics are retained.
- [Concurrency] SHOULD FIX: release the write-outcome signal before reservation cleanup, since the drain holds the post-publication mutex when observing an echo. Never wait on queue callbacks from that drain.
- [Threat model] Paired-device transport and conversation authorization remain at their existing handlers; placement changes only the timing of already admitted messages.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-05
