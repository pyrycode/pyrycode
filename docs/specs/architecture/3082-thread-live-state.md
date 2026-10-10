# Thread session live state

## Files read
- `internal/relay/v2session_seams.go` → `V2SessionConfig`: optional providers, conversation membership and thread readiness.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: authenticated admission and legacy connect snapshots.
- `internal/relay/v2session_push.go` → `Push`, `drainOnce`, `forwardEnvelope`: bounded unsealed queue and single-writer sealing.
- `internal/relay/v2session_modal.go` → `queuedEnv`, `pushQueue`: FIFO accounting and outstanding prompt reconciliation.
- `internal/relay/v2session_appframe.go` → `forwardAppReply`: worker replies return to Run before sealing.
- `internal/relay/v2session_thread.go` → `threadWithheld`, `threadReply`: per-connection projection and correlation-independent access checks.
- `internal/relay/v2session_replysuggestionreconcile.go` → `replySuggestionStale`: existing legacy revision guard must not override supplied ordering.
- `internal/relay/v2session.go` → `Run`, `teardown`: drain scheduling and connection lifetime.
- `internal/relay/v2session_thread_test.go` → `threadPlain`, `threadConfig`: authenticated decrypted-wire fixtures.
- `internal/turnbridge/outbound.go` → `maxSlashCommandListBytes`: measured 64000-byte list budget leaves metadata headroom.
- `docs/knowledge/features/relay-package.md`, `v2-session-manager-concurrency.md`, `v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`: Run owns crypto; correlated thread state must retain access gates.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: prove omission on decrypted original bytes and measure escaping.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: current live state is separate from thread items and history.

## Context
Supplied source state must survive asynchronous delivery without resolving a newer conversation binding at seal time. Production readiness remains unwired until #3077; daemon provenance and retention remain #3076. This implements one relay delivery contract using fakes. No overlapping remote feature branches touch the planned files.

## Design
Add one exported `LiveState` value carrying an envelope, source conversation, monotonically increasing session generation, reading identity and reading revision. Generations order transitions, never session ID strings. Reading identity is scoped by conversation and message family, with an empty identity for singleton readings and distinct IDs for prompts/tool/task progress. Both correlated replies and unsolicited pushes use `PushLiveState(ctx, connID, reading) error`; existing Push and handler signatures remain compatible.

Add `ThreadLiveState` to config: a nonblocking factory returning a detached snapshot cursor, each call yielding one reading or exhaustion. The source supplies only outstanding prompts/questions, all current families including inactive retry/compaction, and preserves answer IDs. The cursor holds no resource needing closure. Legacy providers are skipped on thread connect. Legacy replay remains in place, but scoped live-state replay is withheld on thread connections.

Retain source values beside the unsealed queue entry. Run validates/filter-orders supplied readings using source conversation even for `{}`. Session generation watermarks apply across families; revisions apply only to independently addressable readings. For a family's first reading in a supplied session, send its tagged `{}` clear and park its fresh reading for a subsequent Run pass. Explicit supplied clears are idempotent and cannot erase a newer reading. Omission/null/string tags are preserved without inference; payload session fields and correlation remain intact. Supplied state loses ring/history identities and never affects thread watermarks.

Measure complete tagged envelopes before admission and before sealing against `protocol.MaxThreadEnvelopeBytes`. Invalid/oversized source input returns a content-free error rather than silently losing a reading. Supported payload budgets retain their existing headroom; tests cover near-cap payloads and worst escaping without using item continuation encoding.

## Concurrency model
Producer calls copy payload/tag values and enqueue under pushMu; they do no crypto. All cursors, pending fresh readings and delivered watermarks are Run-owned. Snapshot delivery shares drainCh and sends at most one envelope per pass without filling the push queue. Transport-down holds before advancing cursors or popping queues; reconnect re-signals the pump. Teardown releases cursor/pending/watermarks; cancellation stops the pump. No new goroutines or locks.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Reconnect with inactive retry/compaction and outstanding prompts | `TestLiveStateConnect` |
| Snapshot overtaken by live revision; distinct reading IDs and conversations | `TestLiveStateOrdering` |
| Repeated session transitions; delayed old-session reply; clear arriving late | `TestLiveStateOrdering` |
| Transport down/up and oversized snapshot; teardown/cancellation | `TestLiveStatePacing` |

## Error handling
Reject unsupported types, malformed tags/payloads, nonempty clears or invalid source order using content-free errors. Missing connection/cancelled context retain existing errors. Missing interactive access, unknown supplied conversations and withheld Codex conversations drop before sealing and do not advance delivery state. Snapshot errors are logged without payloads and the pump continues to other readings.

## Testing strategy
Write tests first and observe failure. Hermetic authenticated decrypted-wire tests cover every scoped family for pushes/replies and omitted/null/string tags; literal legacy bytes; empty-payload clear gates; connect reconciliation; interleaving ordering; one-send pacing and hold/teardown. Run `go test -race ./internal/relay/...`, `go vet ./...`, `go build ./cmd/pyry`. The verifier owns the full-module gate.

## Open questions
None. Recount: approximately 760 written lines, one exported type, no mandatory consumer migrations, four acceptance criteria and at most ten reject branches.

## Documentation handoff
Pending documentation stage: `docs/protocol-mobile.md`, “Message envelope” and the scoped live-state message sections: document thread-only session tags; omitted/null/string meanings; `{}` clear-before-fresh behavior; stale-state suppression; current connect reconciliation including inactive retry/compaction. State daemon provenance/retention and provider installation/production activation remain pending #3076/#3077, and this work does not replace legacy replay/resync with catch-up.

## Security review
**Verdict:** PASS
- Trust boundaries / network: MUST FIX addressed in design: supplied conversation identity gates clears and correlated readings independently of payload and correlation. Validate tag shape, clear payload and complete envelope size before crypto.
- Concurrency: SHOULD FIX addressed in design: do not enqueue complete connect snapshots on Run; pace cursors and hold before consuming while transport is down.
- Errors/logs: content-free validation errors; no source strings, payloads, keys or ciphertext in new logs.
- Tokens / cryptography: reuse authenticated session admission and Run-owned Noise sealing; no new credentials or primitives.
- Files / subprocesses: no production filesystem or subprocess operations.
- Threat model: hostile content remains client-sanitized; provenance is attribution, never authorization. Actual daemon source attribution is intentionally #3076, readiness installation #3077.
**Reviewer:** builder (self-review)
**Date:** 2026-10-10

## Revisions
+- 2026-10-10: shown/dismissed prompt types and session-settings counterparts share family ordering; their independently addressable reading IDs remain separate. Correlated replies at the already delivered revision still answer the request; strictly older replies are suppressed. Supported size fixtures use `turnbridge.MapEvent` so producer budget changes remain visible. Final recount: approximately 800 inserted lines, one exported type, no mandatory call-site migrations.
