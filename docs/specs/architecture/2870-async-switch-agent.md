# Asynchronous relay agent switch

## Files read
- `internal/relay/v2session.go` → `dispatchAppFrame`, `Run`: control intercept and single-owner reply path.
- `internal/relay/v2session_seams.go` → `V2SessionConfig`, `LateSessionStarter`: consumer-defined seams and asynchronous ownership contract.
- `internal/relay/v2session_modal.go` → `deferNewSessionOutcome`, `handleNewSessionDone`: teardown-safe completion handoff.
- `internal/relay/v2session_settings.go` → `validModel`, `validEffort`, `handleSetSessionSettings`: grammar and fixed vocabulary refusals.
- `internal/protocol/settings.go` → `SwitchAgentPayload`: required model presence and optional effort semantics.
- `internal/relay/v2session_newsession_test.go` → `openModalConn`, `awaitReplyForConn`: encrypted-frame test patterns.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`: dispatch coverage guard.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md` § The wrap-up turn, and the reply's tense: completion must be answered only when its outcome is known.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: capability precedence needs distinguishable malformed inputs and asynchronous completion needs its own barrier.
- `docs/knowledge/features/protocol-package.md`, `docs/knowledge/decisions/039-capability-belongs-to-agent-and-model-together.md`: validation stays at the consumer; settings remain agent-scoped.

## Context
#2870 joins the merged #2869 wire contract to an injectable relay seam. #2871 owns production adaptation and event publication. No concurrent feature branch overlaps the planned files. No new decision record is needed.

## Design
Intercept `TypeSwitchAgent` beside `TypeNewSession`. Check multi_agent first, then make capable non-interactive requests inert. Decode only object payloads into presence-aware string pointers; reject wrong non-null types as malformed. Missing/null/empty/unsupported agent is unsupported. Require nonempty conversation and present model; preserve explicit empty model and optional/empty effort while applying existing grammars.
Define `AgentSwitcher.SwitchAgent(ctx, payload) AgentSwitchOutcome` at the consumer. The outcome has a state (preflight refusal, uncommitted failure, committed) and closed failure classification: conversation missing, invalid/same-agent, model not offered, effort not offered, vocabulary unavailable, busy, workspace refused, other failure. It carries no downstream error or path. Zero/unknown uncommitted outcomes fail offline. Committed outcomes never reply, even with a cleanup failure classification. Relay chooses the exact fixed messages/codes in the issue; effort uses the existing settings malformed message.
A nil seam returns retryable binary_offline. Add switch completion channel and Run arm. Reclassify the relay guard entry as inbound. Estimated written work: 650 lines including plan/tests; four exported types, no updated consumer calls, four acceptance criteria, ten distinct refusal categories including wire grammar/agent refusals.

## Concurrency model
Run validates and copies the request, then launches one worker calling the blocking seam with Run's context, never a requester-cancelled context. The seam must honor manager cancellation and is responsible for switch admission/serialization. Worker completion selects on the buffered result channel, requester done and manager context. Only Run reads session state or seals replies. Capture the original session pointer and request ID; closed/replaced sessions cannot receive stale replies. No callback can duplicate completion.

## Error handling
All refusals use one correlated static error, guarded by `dropInlineReplyIfDown`. Diagnostics contain only event and connection ID. No payload, model, effort, decode error or downstream error reaches logs or replies. Unknown/unbound conversations are merged. Accepted uncommitted failures may follow wrap-up; only committed state suppresses refusal.

## Testing strategy
Write encrypted-frame table tests first and observe failures before implementation. Cover both agents, capability precedence, interactive inertness, object/required/presence/type/grammar shapes, unwired dispatch and every late classification. Hold a switch while another connection opens and receives a reply. Exercise requester close/replacement, manager shutdown, silent committed clean/cleanup-error completions, and transport-down nonce guard. Assert exact error cardinality, correlation and fixed messages; inspect diagnostic fields for privacy. Run race tests for relay and cmd/pyry, vet all packages and build cmd/pyry. Full-module gate belongs to the verifier.

## Open questions
None. Conversation ID canonical validation, resolution and target vocabulary membership belong to #2871, not this relay boundary.

## Documentation handoff
Pending for #2871/documentation stage: `docs/protocol-mobile.md` § Session settings / switch_agent: production adapter, committed event publication and protocol reference update as assigned to #2871. This ticket has no separate documentation acceptance criterion.

## Security review
**Verdict:** PASS
**Findings:**
- [Trust boundaries] `handleSwitchAgent` gates capabilities before decoding, validates object/presence/types/grammars, and passes the named conversation without cursor fallback. The adapter must still treat its ID as untrusted.
- [Tokens, errors/logs] No credentials are introduced. Outcome classification excludes arbitrary error text; switch diagnostics admit only event and connection ID. Fixed messages and correlation IDs carry no submitted content.
- [File operations/subprocesses] OUT OF SCOPE: resolution, workspace confinement, argv and target vocabulary validation belong to #2871; this seam performs no filesystem or process operations.
- [Cryptography/network] Reuse the existing authenticated Noise frame route, AEAD seal owner and transport limits; no new sockets, keys, nonces or framing. No new application size allowance is introduced. Existing protocol security-model protections for malicious relay, device token interception and revocation remain the handshake's responsibility.
- [Concurrency] Worker owns immutable request values only; shutdown cancels the seam and releases blocked handoff. Requester teardown releases handoff but cannot cancel the switch. Run checks the captured session before sealing. Admission/busy classification belongs to #2871.
- [Threat model] Paired devices can request this mutation, as the issue contract requires; capability advertisement is not extra authorization. Untrusted conversation IDs remain lookup inputs for #2871, never paths in relay.
**Reviewer:** builder (self-review)
**Date:** 2026-10-06
