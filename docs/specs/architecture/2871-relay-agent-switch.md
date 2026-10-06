# Relay agent switch

## Files read

- `cmd/pyry/conversation_agent_switch.go` → `Switch`: admission, mint/rebind and nonempty-ID commitment contract.
- `cmd/pyry/conversation_handover.go` → `storeHandover`: reuse existing history and first-activation handover proof.
- `cmd/pyry/session_reset.go` → `conversationReset.begin`: exclusion shared with ordinary reset, daemon-owned wrap-up.
- `cmd/pyry/main.go` → `runSupervisor`, vocabulary validators: composition root and retained target-agent menus.
- `cmd/pyry/relay.go` → `startRelayV2`: manager, announcer and transition observer installation.
- `cmd/pyry/resetting_v2.go` → `emit`: synchronous status edges.
- `cmd/pyry/session_transition_v2.go` → `broadcast`: asynchronous transition fanout and single history append.
- `cmd/pyry/conversation_update_v2.go` → `announce`: committed-row publication.
- `internal/sessions/transition.go` → `PublishSwitchTransition`: signal without a second rebind.
- `internal/relay/v2session_switchagent.go` → `handleSwitchAgent`: worker lifecycle and fixed correlated errors.
- `internal/relay/v2session_agentgate.go` → `withheldFromConn`, `agentTaggedForConn`: existing Codex visibility and agent tags.
- `docs/knowledge/features/conversation-session-binding.md` § Switching to the other agent: dormant sessions must not activate for handover.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: completion channels require their own observation.
- `docs/knowledge/features/relay-package.md`, `sessions-package.md`, `history-package.md`: existing transport, lifecycle and shared store boundaries.
- `CODING-STYLE.md`, ADR 039 and `docs/protocol-mobile.md` § Security model: errors as values, agent/model validation and relay threat boundary.

## Context

The asynchronous relay verb has no production switcher. Wire the existing primitive without changing cheap current-agent settings or successor activation. No decision record is needed. No overlapping feature branches were found.

## Design

A daemon adapter implements `relay.AgentSwitcher.SwitchAgent(ctx, request) AgentSwitchOutcome`. Validate the canonical UUID before resolving the named row; no active-cursor fallback. Empty wire model becomes nil so the primitive chooses MintDefaults. Effort preserves nil versus explicit empty. Known admission errors become static seam classifications; all failures after reset starts remain failed, and every nonempty returned ID is committed regardless of cleanup error.

Construct one conversationReset in runSupervisor and share it with activeSessionStarter and conversationAgentSwitcher, along with the existing pool, registry, retained vocabulary, history store and resetting emitter. Install the adapter in startRelayV2.

Mark pool switch transitions with a boolean on SessionTransition. Switch closes its reset sequence before publishing the pool signal, still under exclusion, including a committed persistence/rollback error. The single existing transition consumer appends history and fans out the clear transition, then uses an optional committed-row callback only for switch transitions. The callback reads the current registry row and publishes conversation_updated through the existing announcer. Ordinary clear/eviction retain their existing behavior. No extra acknowledgement or settings update is added.

Sizing: one deliverable; approximately 700 written lines (180 production, 450 tests/helpers, 70 plan), zero new exported types, fewer than ten simultaneous consumer updates, five acceptance criteria and nine refusal/failure classifications. Rechecked against this plan before commitment.

## Concurrency model

The relay worker receives Run's lifetime context; requester disconnect never cancels it. The reset's daemon context owns wrap-up. Shared exclusion serializes resets and switches per conversation. Reset pushes enqueue synchronously before the transition observer queues its signal. The transition goroutine serially enqueues transition then row update. Existing manager capability gates remain the final sealing boundary. No new goroutine is introduced; existing producer goroutines terminate on daemon cancellation.

## Error handling

Canonical-ID/same-agent, missing row/binding, model, effort, unavailable vocabulary, competing reset and workspace rejection are inert refusals. Codex nonempty effort without vocabulary requires unavailable classification. Mint and persistence failures after wrap-up are offline failures even if a joined cleanup error matches a refusal sentinel. Nonempty ID always publishes commitment. Every started status sequence closes. No downstream error text is logged or returned over the relay.

## Testing strategy

Write failing tests before implementation. Fake pool/runner tests exercise adapter classification, reset exclusion, template model selection, absent/cleared effort, persistence and construction failures, committed cleanup failure and content-bearing failure markers. Encrypted fake-relay coverage drives Claude → Codex → Claude, decrypts status/transition/update ordering, checks persisted settings and history cardinality, capability visibility and requester disconnect. Reuse TestConversationAgentSwitch_HandoverFirstSpawn. Run race tests on cmd/pyry, internal/relay and internal/sessions, then go vet ./... and go build ./cmd/pyry. Dispatcher owns the full-module gate.

## Open questions

None; implementation may refine the publication callback shape while preserving the ordering contract.

## Documentation handoff

- Pending documentation stage: `docs/protocol-mobile.md`, message-type table and **switch_agent**: replace pending status with operational semantics, required/presence rules, capability gates, correlated refusal codes/retryability, reset/transition/update order, disconnect behavior and failure/commit distinction. State that a connection without multi_agent keeps its previous row until its next list after a switch to Codex. Under **Changelog**, add an entry describing the operational request and ordered committed outcome.
- Pending documentation stage: `docs/knowledge/features/conversation-session-binding.md`, **Switching to the other agent (#2672)**: replace the no-relay-caller statement with the production request and shared reset/history/status wiring.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] UUID validation in the adapter precedes registry lookup; Switch validates the target's retained model/effort vocabulary. Row publications originate in the registry.
- [Tokens and cryptography] Existing authenticated Noise transport and keys are reused unchanged; this work creates no credentials or crypto.
- [Files] Switch reuses resolveSpawnDir confinement and atomic SwitchSession persistence; client IDs never become unchecked paths. Existing file permissions and symlink resolution stay with those owners.
- [Subprocesses] Existing MintWith runner construction and first-message activation remain; no shell is introduced and no unvalidated model is passed to a child.
- [Network and I/O] Existing bounded encrypted-frame parsing, deadlines and connection management remain the input boundary; no endpoint or read is added.
- [Errors and telemetry] SHOULD FIX: joined post-wrap cleanup errors could match an admission sentinel. Mark post-wrap failures before adaptation; never print wrapped errors or request values. Tests use distinctive private markers.
- [Concurrency] Shared begin exclusion covers status closure and transition signalling. Context cancellation ends existing workers/producers; disconnected requesters cannot suppress publication.
- [Threat model] Capability filtering remains in forwardEnvelope, protecting non-multi_agent peers after Codex commitment. Untrusted content rendering remains the existing client's security-model responsibility.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
