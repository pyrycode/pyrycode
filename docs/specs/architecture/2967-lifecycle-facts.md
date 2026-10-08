# #2967: lifecycle facts with captured conversation ownership

## Files read

- `internal/sessions/transition.go` → `SessionTransition`, `notifyTransition`, `RotateForNewSession`, `AdoptAnnouncedID`: legacy vocabulary, fan-out and rotation contracts.
- `internal/sessions/pool_identity.go` → `RotateBootstrapForSelfHeal`, `rekeyLocked`: recovery identity mutation and lock order.
- `internal/sessions/session.go` → `runActive`, `beginEvict`: idle/cap decisions, silent shutdown/crash and pre-teardown notification.
- `internal/sessions/systemprompt.go` → `refreshSystemPromptForRotation`, `writeComposedPrompt`: off-lock reset composition can delay notification.
- `internal/conversations/registry.go` → `RebindSession`, `SwitchSession`: atomic binding mutation, rollback and ownership.
- `cmd/pyry/conversation_agent_switch.go` → `conversationAgentSwitcher.Switch`: known owner/agents and deferred committed publication after inactive status.
- `cmd/pyry/session_transition_v2.go` → `toWirePayload`, `transitionClearsTurn`: closed legacy reason mapping rejects recovery without a wire/history boundary.
- `internal/sessions/transition_test.go`, `selfheal_test.go`, `cmd/pyry/conversation_agent_switch_test.go`, `session_transition_v2_test.go`: existing suppression, delimiter and switch ordering assertions.
- `docs/knowledge/features/sessions-package.md` and `sessions-package-key-types-transition-observer.md`: ordinary synchronous nonblocking observer and dedicated cancellable switch publication contracts.
- `docs/knowledge/features/conversations-package.md`, `conversations-registry.md`, `conversation-session-binding.md`: owning binding versus historical lookup; unknown ownership must not use bootstrap fallback.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: behavioral race tests and evidence boundaries.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Sessions, agents, messages, read marks: distinct lifecycle facts; history adoption is #2968.

## Context

Legacy reasons collapse reset/clear and idle/cap, recovery is silent, and observers receive no captured owner. Off-lock rebinding allows successive rotations to rebind out of order. This ticket exposes facts for #2968 while leaving legacy consumers unchanged. ADR 042 already records the decision; no new ADR is needed. No overlapping feature branches touch the production files as of planning.

## Design

Add `LifecycleCause` vocabulary for operator reset, Claude clear, agent switch, recovery, workspace change, idle sleep and capacity eviction. Extend `SessionTransition` with cause, captured conversation ID, previous/next agents and optional `*string` reset handoff outcome. Empty ownership/agents and nil outcome explicitly mean unknown; derive none from bootstrap, labels or session IDs. Keep `TransitionReason`, `AgentSwitch`, both observer setters and existing rotation signatures compatible.

Add `Registry.RebindSessionOwner(oldID, newID) (ConversationID, bool)` for owner capture with mutation; existing `RebindSession` delegates and retains its boolean contract. Add current-binding-only `SessionOwner` for eviction. Rotation producers rekey and capture/rebind under `Pool.mu`, before releasing it. Conversation persistence and fan-out happen after unlocking. Capture UTC occurrence time with the mutation. Save failures remain best effort and do not suppress notifications. `RotateID` remains its existing silent compatibility primitive.

`RotateForNewSession` delegates to additive `RotateForNewSessionWithHandoff(oldID, outcome)`; copy the optional caller value so later caller mutation cannot rewrite a fact. No daemon outcome source is wired here. Reset/clear keep `ReasonClear`; idle/cap keep `ReasonEviction`; recovery has no legacy reason and reaches observers only. Workspace change has no producer and recovery gains no production caller/policy.

`PublishSwitchTransition` accepts optional `SwitchTransitionMetadata` for captured owner and actual agents. Existing two-argument callers remain valid with unknown metadata. `Switch` supplies its known conversation and old/target agents in the deferred publication, including committed cleanup failure. Empty/equal session pairs are suppressed. Dedicated publication still follows inactive reset status and delivers once.

Eviction captures ID, current owner and membership together under pool lock without rebinding, then invokes the observer off-lock. Removed sessions remain suppressed; crash/shutdown paths stay silent.

## Concurrency model

No new goroutines. Pool mutation serializes A→B→C binding updates; registry scanning/mutation uses its own lock. Lock order is pool then registry; rekey's session lock is released before registry access. Save and callbacks hold no pool, session or capacity lock. Existing synchronous observers must return without waiting; only the dedicated switch publisher may wait with daemon cancellation. Facts are value snapshots, independent of later binding changes.

## Error handling

Existing absent/colliding/equal rotation guards remain before mutation and notification. Pool and conversation persistence failures log and retain authoritative in-memory changes. Committed switch cleanup failures still publish with captured metadata; failed precommit switches remain silent. Unknown metadata remains unknown rather than inferred.

## Testing strategy

Write focused tests first and observe failure. Cover each cause, UTC stamps, session pairs, captured/unknown ownership, optional outcome copy, recovery notification with no legacy mapping, equal/refused/creation/removal/shutdown/crash suppression, and unchanged reasons/flags. Delay reset notification deterministically by holding the prompt composition mutex after rekey, rotate B→C meanwhile, and verify final C binding plus original provenance on both facts. Exercise unowned rotation beside a newly bound foreign conversation. Extend existing idle/cap and switch cleanup tests with owner/cause/agent assertions. Observer callbacks acquire pool/session/capacity locks to prove off-lock delivery. Persistence-failure scenarios must still notify.

Run race-enabled tests for `internal/sessions`, `internal/conversations` and `cmd/pyry`, then `go vet ./...` and `go build ./cmd/pyry` with output outside the worktree. The dispatcher owns the full hermetic `make check` gate after PR creation.

## Open questions

None. String handoff outcomes are caller-authored optional facts; #2968 owns the daemon vocabulary/source and history adoption.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/sessions-package-key-types-transition-observer.md`, update “Transition observer” to describe the distinct lifecycle vocabulary, captured versus unknown ownership, optional caller-supplied handoff outcome, binding-before-notification ordering and unchanged first-creation rule. State that recovery is observable internally but produces no legacy delimiter, workspace change has no producer, and self-heal remains uncalled in production.

## Sizing

One deliverable; approximately 650–750 written lines including tests and this plan. Two new exported types; one consumer call site requiring update; four acceptance criteria; no new state-machine reject branches. All five limits remain within the builder ceiling.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `AdoptAnnouncedID` retains its collision guard and upstream validated transcript stem. `Switch` supplies trusted registry ownership and validated actual agent names; facts never infer ownership from labels or historical session lookup.
- [Tokens/secrets] No credential handling; IDs still come from existing `NewID` crypto/rand generation. Handoff outcome contains classification only, never handoff text.
- [File operations] Existing atomic registry Save and prompt persistence remain unchanged; no new path from lifecycle metadata.
- [Subprocesses] No new spawn, shell command or environment propagation. Recovery remains an uncalled primitive.
- [Cryptography] No primitive/key/nonce changes; existing session ID generation is reused.
- [Network/I/O] No new wire payload or endpoint; `toWirePayload` rejects recovery, so it creates neither legacy wire nor history boundary.
- [Errors/logs] Existing failure logging is retained; new owner/agent/outcome fields are not logged or sent over the network.
- [Concurrency] Owner capture/rebind must occur under pool mutation serialization, with callbacks and Save outside locks. Deterministic delayed-notification tests guard wrong-conversation attribution and reordered binding.
- [Threat model] This changes an internal fact contract, not relay authorization or isolation. History adoption and its transport projection remain out of scope in #2968.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08
