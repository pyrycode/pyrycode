# Retire relay event replay admission and delivery

## Files read

- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: authenticated admission and connect reconciliation.
- `internal/relay/v2session.go` → `V2Session`, `V2SessionManager`, `Run`, `teardown`: replay ownership and lifecycle.
- `internal/relay/v2session_replay.go` → replay pump, snapshot refusals and inline transport guard: separate retired delivery from supported replies.
- `internal/relay/v2session_push.go`, `v2session_livestate.go` → `drainOnce`, `forwardEnvelopeSource`, `signalLiveDrain`: retain pending-reading and hosted capacity gates.
- `internal/protocol/handshake.go`, `codes.go` → `HelloClientPayload`, `TypeResync`: remove obsolete wire vocabulary.
- Relay replay, catch-up, thread, agent-gate, inline-reply and hosted-capacity tests: migrate supported assertions and remove exclusive replay fixtures.
- `cmd/pyry/thread_shadow_legacy_test.go`, `relay_guard_test.go`: legacy source-history independence and protocol inventory.
- `internal/e2e/handshake_interactive_helpers_test.go`, channel-post and modal integration tests: fresh-key reconnect and durable source assertions.
- `docs/knowledge/features/v2-session-manager.md` and its reconnect-replay and thread-delivery sections: thread queries are explicit; current-state reconciliation is separate from content recovery.
- `docs/knowledge/features/protocol-package.md`, `thread-package.md`, `e2e-harness.md`, `development-verification.md`: reuse bounded query and fake-daemon harnesses; compilation is not live proof.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Update messages: per-conversation epoch/version recovery.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` → Security model: single crypto owner, static refusals and authenticated routing.

## Context

One behaviour retires the transitional event-ring recovery path: authenticated
hellos treat `last_event_id` as an unknown key, and content recovery requires an
explicit conversation thread query. #3718 already retired content publication.
#3739 owns remaining ring storage/configuration and setter wiring; #3719 owns
final integration acceptance. No deployment or data migration is involved.

Sizing: approximately 550–700 written lines including tests and this plan,
plus replay-only deletions; no new exported types; six payload construction
consumers; four acceptance criteria; no new reject branches. The #3479 analogue
added 407 lines but did not migrate handshake fixtures. Branches #3240 and #3296
overlap locally; neither supplies a required interface or forces redesign.

## Design

Remove `HelloClientPayload.LastEventID`, replay classification, raw `resync`,
session watermark/tail, manager wake channel and all pump lifecycle hooks.
`SetReplaySource` remains inert for existing wiring until #3739. Retain snapshot
refusals and `dropInlineReplyIfDown` in a reply-focused file. Remove `TypeResync`
and its inventories once no supported producer remains.

`drainOnce` and `signalLiveDrain` continue respecting `livePending` and hosted
capacity. `forwardEnvelopeSource` retains capability, thread and revision filters,
but never filters on ring event IDs. Authentication, device binding, rekey,
teardown and current prompt/state reconciliation keep their existing contracts.
Replace positive replay tests with authenticated unknown-key and thread recovery
scenarios; retain source-history, nonce, host-scope and lifecycle assertions.

## Concurrency model

Run remains the sole session and Noise cipher owner. Remove the replay wake arm;
add no goroutines or locks. Push queues retain `pushMu` and existing bounds;
hosted delivery retains its admission reservation. Reconnect wakes the ordinary
drain; transport-down paths leave queued heads unsealed. Workers still terminate
through session teardown or Run cancellation.

## State transitions and identity reuse

| Event | Race coverage |
| --- | --- |
| Fresh authenticated hello with arbitrary obsolete cursor shapes | `TestV2Session_LastEventIDIgnored` |
| Reconnect under a fresh connection and fresh cipher pair | `TestV2Session_LastEventIDIgnored`; existing rekey/reconnect tests |
| Delivery for the same and a different conversation after hello | `TestV2Session_LastEventIDIgnored` |
| Explicit recovery, unusable checkpoint, newest window and older page | `TestThreadRecoveryAfterReconnect`; existing catch-up/page tests |
| Transport loss and reconnect flush | existing push reconnect tests and `TestV2Session_InlineReply_TransportDown_BurnsNoNonce` |
| Pending fresh reading and full hosted lane | `TestHostedPushCapacityPendingFence`, `TestHostedPushCapacityDelivery` |
| Teardown and repeated rekey | existing session/rekey tests; `TestHostedPushCapacityTeardown` |

## Error handling

Unknown `last_event_id` values cannot fail payload decoding. Other malformed
hello fields, authentication refusals and snapshot errors remain unchanged.
Thread queries retain generic failures and `thread_reset` for unusable
checkpoints. No new error branch or log payload is introduced.

## Testing strategy

Write and run the unknown-key regression before production edits. Assert a
handshake barrier receives no replay/resync and later thread updates arrive in
order for two conversations, with the setter installed but unconsulted. Verify
explicit thread recovery through the existing authenticated harness and scoped
providers, including reset/window/page. Preserve hosted capacity and inline nonce
oracles while migrating their retired fixtures. Remove replay-exclusive skipped
cases/helpers. Run affected Linux race packages through ordinary admission,
focused fake-daemon cases where fixtures change, `go vet ./...`, and
`go build -o /tmp/builder-3738/pyry ./cmd/pyry`. Compile the affected tagged `e2e`,
`e2e_realclaude` and `e2e_liverelay` packages. Retain commands, candidate and counts
in the PR. Dispatcher/verifier complete acceptance and release/live gates remain
pending; tagged compilation is not live acceptance.

## Open questions

None. Keep unused ring storage and surviving deletion observers for #3739.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` → **hello (v2-specific note)**, **Reconnect replay & resync**, **Reconnect / Backfill semantics**, **Compatibility** and associated recovery wording: `last_event_id` is ignored; no event-ring replay or raw `resync` occurs; clients explicitly catch up each open conversation by epoch/version and recover from `thread_reset` by loading its newest window. Preserve prompt/current-state reconciliation and durable source-history distinctions.
- Reconcile replay/pump claims in `docs/knowledge/features/v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md` and its parent `v2-session-manager.md`. #3739 owns ring-storage documentation; #3719 integrates the handoffs.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `handleNoiseInit` still authenticates Noise early data, validates the token/static-key binding and client version before opening. Ignoring one unknown JSON key grants no capability. Test string/object/array/null and maximum integer inputs.
- [Tokens/secrets] No credential generation, retention or logging changes; fixtures use synthetic pairing tokens. Existing refused-token/device-binding tests remain required.
- [File operations] No production file I/O is added; ring storage ownership and persistence cleanup belong to #3739.
- [Subprocesses] No process invocation or environment inheritance changes; reuse fake-daemon integration fixtures.
- [Cryptography] SHOULD FIX: removing replay fixtures must retain the inline no-seal/no-nonce-consumption oracle and fresh cipher pairs on reconnect. Preserve those tests and ordinary transport hold.
- [Network/I/O] SHOULD FIX: deleting replay gates must retain `livePending`, hosted admission and bounded queues. Existing capacity/fence tests must still pass. Network frame caps and deadlines remain at existing transport/admission boundaries.
- [Errors/logs] Retain static snapshot refusal messages and generic thread query errors; do not log cursor contents, tokens or decrypted payloads.
- [Concurrency] Run remains sole cipher/session owner; removing the pump adds no off-owner access. Retain teardown, reconnect and rekey race coverage.
- [Threat model] Per-host conversation validation and requested-conversation scoping remain in thread query admission; test both. Relay confidentiality/authenticity remain Noise protected. No live deployment/data migration; ring cleanup is OUT OF SCOPE under #3739.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-23
