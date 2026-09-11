# #2365 — Grant offered permission rules for the current session

## Files read

- `internal/protocol/messaging.go` → `ModalAnswerPayload` — owns the phone-to-daemon answer vocabulary and currently carries only daemon correlation, option selection, and the client idempotency token.
- `internal/protocol/messaging_test.go` → `TestModalAnswerPayload_RoundTrip` — canonical populated fixture round-trip; nearby wire-key tests establish the reflection-based field-census style needed to pin an additive optional field.
- `internal/protocol/testdata/modal_answer.json` — cross-language populated `modal_answer` contract fixture.
- `internal/relay/v2session_seams.go` → `ModalResolver.ResolveAnswer` — consumer-declared seam that must carry the decoded Boolean without admitting rule or destination bytes.
- `internal/relay/v2session_modal.go` → `V2SessionManager.handleModalAnswer` — the sole inbound decode point and the route from the authenticated v2 session to the resolver.
- `internal/relay/v2session_modal_test.go` → `TestV2Session_ModalAnswer_FanOut`, `fakeModalResolver` — proves exact answer fields cross the relay seam and preserves no-op dismissal behavior.
- `cmd/pyry/modal_resolve_v2.go` → `modalResolverV2.ResolveAnswer`, `streamApprovalBridge.ResolveStream` — owns authorization, option classification, modal one-shot consumption, and conversion of an allowed stream answer into a `permbridge.Verdict`.
- `cmd/pyry/modal_resolve_v2_test.go` → `TestModalResolverV2_Answer_Authorized` and the unauthorized, stale, replay, invalid-option, and interaction-required siblings — pins the gate-before-consume ordering and unchanged non-stream behavior.
- `cmd/pyry/stream_approval_test.go` → `TestStreamApproval_RoundTrip` — component proof from parked permission offer through `modal_shown`, `modal_answer`, and the verdict returned to Claude.
- `internal/permbridge/permbridge.go` → `AlwaysAllow`, `PermissionUpdate`, `Verdict`, `Allow` — owns the immutable fully validated offer and the only verdict vocabulary accepted by both stdio and approval-MCP consumers.
- `internal/permbridge/permbridge_test.go` → `TestVerdict_MarshalShape`, `TestParseAlwaysAllow_Valid` — pins allow/deny wire disjointness and source-order retention.
- `cmd/pyry/streamsup_runner.go` → `stdioPermissionHandler.await` — turns a resolved `permbridge.Verdict` into Claude's correlated stdio `control_response`.
- `cmd/pyry/stdio_permission_test.go` → `TestStdioPermissionHandler_CarriesAskContextFromCorrespondingFields` — direct stdio adapter proof, including interaction-required refusal and the one-response invariant.
- `internal/streamsup/envelope.go` → `WriteCanUseToolAllow`, `marshalCanUseToolAllow` — already accepts optional `updatedPermissions` raw JSON and omits it when nil; no change is needed here.
- `internal/e2e/relay_v2_stream_modal_test.go` → `TestRelayV2_StreamModalPermissionRoundTrip` — fake-daemon proof covering stdio offers, suppressed offers, approval MCP, stale answers, and exact child-stdin response bytes.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `TestInteractiveStreamStdioModalAllow`, `startObservedPermissionHarness`, `raiseRealPermissionModalPayload` — existing live stdio harness and plain-allow proof to extend with repeated-command session scoping.
- `docs/knowledge/features/permbridge-package.md` → “The one-shot: `resolve`” and “Trust boundary” — the modal and approval registries' delete-under-lock one-shots are the security boundary; `AlwaysAllow` is daemon-internal and cloned on access.
- `docs/knowledge/features/protocol-package.md` → “Security posture” — inbound protocol structs are carriers; authorization remains at the consuming handler.
- `docs/knowledge/features/development-verification.md` → “Protocol boundaries” and “Test execution and artifact survival” — populated fixture values, explicit optional-field assertions, and tagged-suite compilation must be non-vacuous.
- `docs/knowledge/features/e2e-realclaude.md` → “Permission protocol behavior” — live stdio permission behavior is version-dependent and requires explicit modal and execution cardinality checks.
- `docs/specs/architecture/2364-permbridge-bounded-always-allow-rules.md` → `AlwaysAllow`, stored modal truth, and security review — preceding slice defines the all-or-nothing validated offer consumed here.

## Context

Ticket #2364 retains only a fully validated Claude-authored `addRules`/`allow` batch, publishes a bounded display form, and parks the immutable value with both the approval and outstanding modal. The remaining gap is answer-time use: a phone can currently approve only once even when it selects a “don't ask again” choice.

This slice adds a Boolean choice without transferring authority over permission bytes to the network peer. The daemon selects the already parked offer, rewrites every destination to the non-persistent `session` destination, and returns those updates only with an authorized allow verdict. The ticket prescribes six production files, one above the normal boundary, but is already a grandchild (#2204 → #2286 → #2365); the split-depth rule therefore requires the existing `needs-human:sizing` marker and implementation in place.

## Design

### Additive inbound protocol field

Add `AlwaysAllow bool \`json:"always_allow,omitempty"\`` to `protocol.ModalAnswerPayload`. The payload gains no rule, update, or destination field. `V2SessionManager.handleModalAnswer` forwards the decoded Boolean across `ModalResolver.ResolveAnswer`; the resolver test double records it so the relay package proves both true carriage and false defaulting.

The `omitempty` tag is load-bearing compatibility: absent and explicit false decode to false and marshal without the key, preserving the existing control payload bytes. The populated fixture carries `always_allow:true`. A reflection census pins the complete four-field Go type, each JSON tag, and the Boolean type so later rule or destination inputs cannot enter unnoticed.

### Session-only verdict construction

Extend `permbridge.PermissionUpdate` with a destination field that remains empty in the retained validated offer. Add a package constructor with this contract:

```go
func AllowAlways(updatedInput json.RawMessage, offer AlwaysAllow) Verdict
```

For a non-empty validated offer, `AllowAlways` clones the retained updates, sets every clone's destination to the literal `session`, serializes them in existing update/rule order, and places those bytes in a new optional `Verdict.UpdatedPermissions`. The original `AlwaysAllow` stays immutable. A zero/unoffered value returns the same shape as `Allow`, so it cannot manufacture a grant. `Deny` never carries updated permissions.

`modalResolverV2.ResolveAnswer` continues its current order: daemon lookup, device gate, interaction-required gate, option classification, then modal one-shot consume. It computes the existing `allow` decision independently of `always_allow`. The stream arm receives the Boolean, but uses `AllowAlways` only when both values are true. Thus a deny stays deny; an unoffered or suppressed permission becomes a plain allow; non-stream/TUI modals retain their keystroke behavior; and unknown, stale, or consumed modal IDs never reach the grant arm.

`streamApprovalBridge.ResolveStream` looks up the correlated live `permbridge.Request`, so the offer used for the verdict is daemon-retained approval truth rather than any network value. The approval registry's one-shot still resolves exactly once. `stdioPermissionHandler.await` passes `Verdict.UpdatedPermissions` to the existing `WriteCanUseToolAllow`; nil continues to omit the key byte-for-byte. Approval MCP keeps constructing zero offers and keeps its existing plain verdict JSON.

```text
phone modal_answer {ids, always_allow:true}
  → authenticated v2 session
  → outstanding modal lookup + eligibility + allow-option classification
  → consume modal one-shot
  → correlated parked permbridge.Request.AlwaysAllow
  → clone updates + destination="session"
  → permbridge verdict one-shot
  → stdio control_response.response.updatedPermissions
```

### Live session proof

Add a live test beside `TestInteractiveStreamStdioModalAllow`, using `startObservedPermissionHarness` with stdio permissions enabled. In one daemon session it requests the same distinctive Bash command twice. The test requires the first `modal_shown` to advertise an offer, answers it with `always_allow:true`, and then observes both command executions and terminal completion while rejecting any second permission modal. A newly started isolated harness repeats the command and must surface a new permission modal before execution, proving the grant did not persist beyond the session.

The test is a standing `e2e_realclaude` gate with no internal optional skip; only the shared environment/credential prerequisites may skip before execution, matching the existing live suite contract. Existing plain stdio allow and approval-MCP tests remain untouched and continue to prove false/absent compatibility on the real stacks.

## Concurrency model

No goroutine or lock is added. `handleModalAnswer` and `modalResolverV2.ResolveAnswer` run on the relay manager's single dispatch goroutine. The modal registry consume remains the first-answer-wins boundary. `streamApprovalBridge.ResolveStream` snapshots its correlation under its existing mutex, then resolves the permission registry after releasing that lock. The buffered permission verdict channel delivers one value to `stdioPermissionHandler.await`, which remains the sole child-stdin writer for that request.

The validated offer is immutable and accessors clone its slices. Destination rewriting occurs only on the clone used for one verdict, so concurrent snapshots and reconnect publication cannot observe mutation.

## Error handling

- Unknown, stale, consumed, unauthorized, invalid-option, interaction-required, and non-permission paths retain their current early returns before permission grant construction.
- A true Boolean with a deny option resolves through `permbridge.Deny`; it cannot influence the allow classification.
- An unavailable offer makes `AllowAlways` fall back to the existing plain `Allow` shape.
- Permission-update serialization uses only bounded validated string fields. If serialization nevertheless fails, fail closed by returning a plain allow without `updatedPermissions`; the requested tool remains explicitly authorized but no session rule is granted.
- Child-stdin marshal or write failures retain `WriteCanUseToolAllow`'s existing wrapped-error behavior and retirement sequence; payload bytes are never logged.

## Testing strategy

- Protocol: update the populated `modal_answer` fixture, assert `AlwaysAllow == true` on round-trip, add a complete field/type/tag census, and prove absent/false both marshal to the exact legacy payload while explicit true adds only `always_allow`.
- Relay: extend `fakeModalResolver` and `TestV2Session_ModalAnswer_FanOut` to prove true crosses the seam; keep the no-op row false by absence.
- Permbridge: table-test `AllowAlways` for multiple updates and rules, exact order, unconditional `session` destinations, source-offer immutability, zero-offer fallback, and disjoint deny/plain-allow shapes.
- Resolver/bridge: extend `TestStreamApproval_RoundTrip` with true allow, false allow, true deny, offered/unoffered/suppressed-style zero offers, and replay. Assert exact `UpdatedPermissions` bytes and that the second answer cannot resolve another verdict. Existing unauthorized, interaction-required, stale, invalid-option, and non-stream tests remain regression coverage.
- Stdio/fake e2e: assert true/offered writes `updatedPermissions`, every destination is `session`, and false/absent, deny, approval-MCP, suppressed, late, and child-exit cases preserve current response shapes and cardinality.
- Live: compile through the dispatcher's `needs-real-claude` gate and run the repeated-command session/fresh-session proof there; do not invoke live Claude from the builder session.
- Builder gate: `go test -race ./internal/protocol/... ./internal/relay/... ./internal/permbridge/... ./internal/e2e/... ./cmd/pyry/...`, then `go vet ./...`, then `go build ./cmd/pyry`.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md`, section “Modal (v2)” → `modal_answer`, with the optional `always_allow` field, false/absent byte compatibility, allow-only use with a daemon-retained offered value, and unconditional rewriting of every granted destination to `session`.

## Open questions

None. The preceding slice fixes offer validation and retention; this ticket fixes allow-only semantics, session-only destination policy, compatibility, and the live proof shape.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No finding — `V2SessionManager.handleModalAnswer` decodes only a Boolean from the network, while `modalResolverV2.ResolveAnswer` gates authorization and option semantics before `streamApprovalBridge.ResolveStream` reads the daemon-retained `AlwaysAllow`. No client-authored rule, update, behavior, or destination crosses the seam.
- [Tokens, secrets, credentials] No finding — `answer_token` generation, secrecy, and lifecycle are unchanged; it remains unused and unlogged, while the modal and permission registry one-shots provide deduplication. The design adds no token or credential storage.
- [File operations] No finding — production code performs no file operation. The live proof uses isolated test-owned paths and the existing authenticated-harness lifecycle.
- [Subprocess / external command execution] No finding — the production child command and environment are unchanged. The live test supplies a fixed test-authored Bash command through the existing prompt harness and proves execution cardinality without introducing `sh -c` in production.
- [Cryptographic primitives] No finding — v2 authentication, encryption, randomness, and comparison are unchanged; the feature begins after the existing authenticated session has produced a paired `devices.Device`.
- [Network & I/O] No finding — the new inbound data is one decoded Boolean inside the existing bounded encrypted envelope. `WriteCanUseToolAllow` continues structured JSON serialization and a single newline-terminated write, so retained strings cannot inject another child-input frame.
- [Error messages, logs, telemetry] No finding — rule, destination, update, and tool-input bytes are not logged. Existing content-free audit and bounded invalid-option logging remain unchanged.
- [Concurrency] No finding — both registry one-shots and existing lock ordering are preserved. Rewriting operates on `AlwaysAllow.Updates` clones after locks are released and adds no shared mutable state or goroutine.
- [Threat model alignment] No finding — the paired-device authorization gate remains mandatory, the daemon remains the sole permission authority, a deny cannot escalate, persistent destinations cannot be selected, and replay cannot reapply a consumed modal's updates. Persistent grants and client-authored permission rules are explicitly outside this ticket's contract and structurally absent from `ModalAnswerPayload`.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11
