# #2869 — switch_agent request contract

## Files read

- `internal/protocol/settings.go` → `SetSessionSettingsPayload`: pointer presence precedent and adjacent home for the new DTO.
- `internal/protocol/settings_test.go` → `TestSetSessionSettingsPayload_RoundTrip`: existing envelope round-trip conventions.
- `internal/protocol/codes.go` → `TypeSetSessionSettings`: adjacent v2 request discriminator.
- `internal/protocol/envelope.go` → `Envelope`, `IsKnownAppType`: raw payload transport and v1-only membership boundary; unchanged.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`: all three compatibility registries must classify the request.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestEveryInboundV2TypeHasHandler`: AST-based totality guard requires a pending-handler entry.
- `docs/knowledge/features/protocol-package.md` and its session-settings and drift-detector sections: optional pointers preserve explicit empty values; the protocol partition alone cannot detect a wholly unclassified new constant.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: re-marshal the decoded DTO and check raw key presence.
- `docs/knowledge/features/cli-verb-dispatch.md`: command package conventions; no CLI behavior changes.
- `docs/knowledge/decisions/039-capability-belongs-to-agent-and-model-together.md` § Decision: the agent/model pair determines capabilities.
- `docs/protocol-mobile.md` § Security model: remote control threats; this slice declares vocabulary only.
- `CODING-STYLE.md`: table-driven stdlib tests, gofmt, no new dependencies.

## Context

A multi_agent client needs a distinct conversation-addressed request naming a confirmed agent/model switch. This does not replace set_session_settings. Validation and dispatch follow in #2870 and production wiring in #2871. No new decision record is needed; ADR 039 owns the agent/model boundary.

## Design

Add `TypeSwitchAgent = "switch_agent"` beside the settings discriminators and `SwitchAgentPayload` beside `SetSessionSettingsPayload`. Required `ConversationID`, `Agent`, and `Model` are strings without omitempty; `Model == ""` selects the target agent's template default. `Effort` is `*string` with omitempty: omitted/null decode to nil and serialize omitted, explicit empty means clear and stays present, and nonempty values survive unchanged. These are shape declarations, not semantic validation.

Keep `inboundAppTypeSet` unchanged. Add the discriminator to all three compatibility test registries and to `excludedTypes` as `pending handler (#2870)`.

No overlapping feature branch touches the five planned code/test files after fetching origin. One deliverable: the v2 request contract. Estimated total written work is about 180 lines (including this plan), one new exported type, zero consumer call-site updates, two acceptance criteria, and zero state-machine reject branches; all sizing limits hold.

## Concurrency model

Pure DTO and constant additions; no goroutines, shared mutable state, or shutdown behavior.

## Error handling

Standard encoding/json type errors remain unchanged. `IsKnownAppType` must return `ErrUnknownType` for this unencrypted v2 request on v1. Required-key validation, agent/model membership, capability checks, and dispatch are deferred to #2870/#2871.

## Testing strategy

Add a table-driven Envelope decode/DTO decode/DTO encode/Envelope encode round-trip for both claude and codex, named and empty models, and all four effort inputs (omitted, null, empty, nonempty). Compare the entire decoded DTO, then inspect the re-encoded payload map for every required key and the effort presence/value. Pin the literal discriminator independently. Existing compatibility and relay totality tests cover classification. Write tests first and observe the missing declarations fail compilation, then implement. Run race tests for internal/protocol and cmd/pyry, go vet ./..., and go build ./cmd/pyry (output to scratch).

## Open questions

None; the issue supplies the complete presence contract.

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md`, message-type table and new **switch_agent** section: declare required conversation_id, agent, model (including empty model selecting the target-agent template default), and optional effort with omitted/null unspecified and re-encoded omitted, explicit empty clear and retained, nonempty retained. Mark handling pending #2870/#2871 in both locations.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] OUT OF SCOPE: `SwitchAgentPayload` remains untrusted decoded data. Required fields, supported agents/models, capability and conversation ownership checks belong to #2870; #2871 wires them into production. Declaring the DTO does not authorize a switch.
- [Tokens, secrets, credentials] No credential fields, generation, storage, or lifecycle added; the DTO carries only request identifiers/settings.
- [File operations] No request field is used as a path and no file I/O is added.
- [Subprocesses] No request field reaches command execution; switching execution is deferred to #2870/#2871.
- [Cryptography] No primitive, nonce, comparison, or key use changes.
- [Network and I/O] No reader, listener, or frame-size behavior changes. The declaration adds no processing path.
- [Errors, logs, telemetry] No logs, telemetry, or error formatting added; `IsKnownAppType` continues returning its fixed sentinel.
- [Concurrency] No shared state, locks, or goroutines added.
- [Threat model] Existing authentication, Noise transport, replay protections, and resource limits are unaffected. Agent-switch authorization and hostile request validation are deliberately deferred to #2870/#2871.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
