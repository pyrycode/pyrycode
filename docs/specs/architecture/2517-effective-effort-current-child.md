# Resolve effective effort from the addressed live child (#2517)

## Files read

- `docs/knowledge/INDEX.md` — startup map for the sessions, stream supervision, and verification topics.
- `CODING-STYLE.md` → interface, concurrency, testing, and citation conventions — keeps the child query as a consumer-owned optional capability with context cancellation and race-enabled proof.
- `docs/knowledge/features/development-verification.md` → `Establish the change surface`, `Verify inherited premises`, `Prove that tests distinguish the change`, `Protocol boundaries` — requires construction-site checks, poisoned refusal results, and separate proof for string/null/unavailable states.
- `docs/knowledge/features/sessions-package.md` and `sessions-package-key-types-runner-interface-runnerfactory.md` → `Pool`, `Runner interface + RunnerFactory` — define live pool lookup and explain why one-purpose child capabilities stay off `sessions.Runner`.
- `docs/knowledge/features/streamsup-package.md` → `Public API`, child lifecycle — establishes that a runner can exist before its child and that child availability is reported separately from runner availability.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md` → `RunConfigFor`, `EffectiveEffortFor`, control flow, test design — fixes the relay seam's nullable/availability contract and its saved-settings-first authorization order.
- `docs/protocol-mobile.md` → `Security model` — supplies the authenticated interactive relay threat model and its denial-of-service posture.
- `docs/specs/architecture/2516-relay-effective-effort-session-settings.md` → `Provider contract`, `Preserved resolution behavior`, `Concurrency model` — defines the already-landed relay consumer and explicitly leaves production exact-child resolution to this ticket.
- `cmd/pyry/main.go` → `resolveBoundRunner`, `resolveBoundMCPStatus`, `mcpStatusFor`, `runSupervisor` — provides the no-bootstrap binding resolver, the optional-capability pattern, and the production construction site.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2`, `runConfigFor` — owns the narrow values carried from the composition root into `relay.V2SessionConfig`.
- `cmd/pyry/streamsup_runner.go` → `streamRunner.QueryAppliedSettings` — confirms the production adapter already exposes the optional child-query capability without widening `sessions.Runner`.
- `cmd/pyry/session_mcp_status_test.go` → `mcpStatusQueryRunner`, `newMCPStatusQueryTestPool`, `TestResolveBoundMCPStatus_QueriesBoundRunnerAndReusesMapping` — nearest focused registry/pool/runner resolver fixture and no-bootstrap assertion pattern.
- `internal/conversations/registry.go` → `Registry.Get`, `Registry.RebindSession` — supplies a synchronized fresh binding read and the production rebind primitive used to prove successor resolution between calls.
- `internal/sessions/pool.go` → `Pool.Lookup`, `Pool.Mint` — supplies exact-id live runner lookup and test session creation without activating a child.
- `internal/streamsup/runner.go` → `AppliedSettings`, `Runner.QueryAppliedSettings` — defines the bounded model/nullable-effort result, mandatory caller deadline, live-child and generation checks, and unified unavailable outcome.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.EffectiveEffortFor` — defines the primitive relay boundary: nullable applied effort plus an independent availability bit.
- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings` — confirms the provider runs only after `RunConfigFor` accepts the same conversation and cannot replace saved fields.

## Context

The relay can now render `effective_effort`, but production leaves its provider nil. The missing producer must answer from the child currently reached by the requested conversation's registry binding, not from saved configuration, a bootstrap runner, a retained reading, or a previously resolved session. A fresh child round trip is intentionally distinct from `RunConfigFor`: that existing seam remains the only source of session id, saved model/effort, permissions, and usage.

The refined estimate is about 650 written lines across two production files. The implementation sketch is approximately 600–650 lines including this plan and focused tests: two production files, one new test file, no new exported type or interface, two keyed construction-site additions, four acceptance criteria, and no new state-machine reject branch. The closest shipped daemon-side analogue is #2382's `resolveBoundMCPStatus`/`mcpStatusFor` resolver and relay wiring; this ticket is smaller because #2505 already landed the runner query and #2516 already landed the relay consumer. The remote feature-branch scan found no overlap on the planned files.

No ADR is needed. This is the production installation of contracts already decided by #2505 and #2516.

## Design

### Exact-child provider

Add an unexported optional-capability interface in `cmd/pyry` with the existing `QueryAppliedSettings(context.Context) (streamsup.AppliedSettings, bool)` signature. `resolveBoundEffectiveEffort` will:

1. Resolve `conversation id → registry CurrentSessionID → Pool.Lookup → Session.Runner` through `resolveBoundRunner`, preserving its empty-binding guard and no-bootstrap refusal.
2. Assert only the narrow query capability on that resolved runner. A runner without it is unavailable; `sessions.Runner` does not widen.
3. Derive a 30-second deadline from the caller context and call that runner once. The bound matches the established full context-usage query and live applied-settings probe budgets while guaranteeing a silent child cannot retain a connection worker indefinitely. Earlier caller cancellation or deadline still wins.
4. On an available result, return only `AppliedSettings.Effort`. Its pointer preserves a confirmed string versus confirmed explicit null. `AppliedSettings.Model` and every other child setting remain below the boundary. Every failed resolution, unsupported runner, cancellation, timeout, or query refusal returns `(nil, false)`.

The resolver holds no cache and performs no activation, mint, settings update, registry update, message delivery, permission actuation, or fallback. A lifecycle change inside `QueryAppliedSettings` is governed by that capability's existing child-generation contract; a later provider call starts again at the registry.

### Nil-preserving construction and relay wiring

`effectiveEffortFor` will build the relay-shaped closure only when both `*conversations.Registry` and `*sessions.Pool` are non-nil. A missing dependency returns a literal nil function rather than a non-nil wrapper that can only refuse. The closure delegates every call to `resolveBoundEffectiveEffort`, so rebinding between calls is observed.

Add that primitive-typed closure to `relayWiring` and assign it directly to `relay.V2SessionConfig.EffectiveEffortFor` in `startRelayV2`. `runSupervisor` constructs it beside `mcpStatusFor` and `contextUsageResolve` from the same registry and pool. A zero `relayWiring` used by foreground, disabled, or isolated configurations keeps the field nil; `handleRequestSessionSettings` therefore preserves its existing saved reply while omitting `effective_effort`.

```text
request_session_settings worker
  -> RunConfigFor accepts addressed conversation
  -> EffectiveEffortFor (optional)
       -> fresh Registry.Get
       -> guarded Pool.Lookup of CurrentSessionID
       -> optional QueryAppliedSettings under 30s deadline
       -> project Effort only: string | explicit null | unavailable
  -> relay composes requester-only reply
```

## Concurrency model

No goroutine, channel, lock, shared map, or cache is added. The provider runs synchronously on the requesting connection's existing `appFrameWorker`. Registry, pool, and runner each synchronize their own snapshots; their locks are acquired sequentially through existing methods and are never nested by the provider.

The relay's manager context remains the parent. `context.WithTimeout` adds the query deadline, and its cancel function is always deferred. Manager cancellation, an earlier request deadline, or the 30-second provider deadline releases the child query and worker through `QueryAppliedSettings`'s existing cancellation path. Separate calls can run concurrently and independently, including for distinct conversations.

## Error handling

- Nil registry or pool builds no provider.
- Empty, unknown, unbound, dormant/pool-missing, or dangling conversation bindings fail in `resolveBoundRunner`; none reaches a runner and none falls through to bootstrap.
- A bound runner without the narrow query interface is unavailable.
- A not-yet-started child, child replacement, write failure, malformed/failed child response, cancellation, or timeout is collapsed by `QueryAppliedSettings` to unavailable and crosses no detail into relay.
- A successful response returns `Effort` exactly: non-nil pointer for a string and nil pointer with `true` for explicit null.
- The resolver logs nothing. In particular it never logs the caller's conversation id or the child-authored applied value.

## Testing strategy

Write focused `cmd/pyry` tests first against an injected pool runner whose query plan records exact session ids, observes deadline/cancellation, and can return a string, explicit null, poisoned unavailable value, or block until its context ends. Before production code, the provider tests must fail because the resolver and wiring do not exist.

Scenarios:

- Two conversations have distinct bound sessions, saved effort values, and applied effort results. The existing production saved-settings resolver returns each saved choice while the new provider returns only that conversation's differently valued applied choice; neither runner is cross-queried and bootstrap is never queried.
- String, explicit null, and unavailable remain three distinct `(pointer, bool)` outcomes. A poisoned pointer with `false` is discarded.
- Empty, unknown, unbound, dangling/pool-missing, unsupported runner, query failure, already-cancelled request, caller timeout, and not-yet-started query refusal all return unavailable without consulting bootstrap or a sibling runner.
- A query begun from a background context receives a finite deadline. A shorter parent deadline wins and releases a blocked fake.
- One conversation is read, rebound with `Registry.RebindSession`, and read again. The first call records only the predecessor runner and the second only the successor, proving the closure resolves registry and pool afresh rather than capturing a runner.
- `effectiveEffortFor` returns nil if either registry or pool is absent and delegates when both exist. The `relayWiring` production assignment is covered by compiling the keyed construction and the existing relay saved-settings tests retain nil-provider behavior.
- Call counts on the fake runner and snapshots of registry/pool-visible state prove the provider performs only the one child query: no activation, rebinding, persisted setting change, permission action, or ordinary message path is available to it.

Touched-scope verification:

- `go test -race ./cmd/pyry`
- `go vet ./...`
- `go build ./cmd/pyry`

## Open questions

None. The query result shape, no-bootstrap resolver, deadline requirement, nil-preserving construction, and relay placement are fixed by #2505, #2516, and this ticket's acceptance criteria.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md`, especially the `EffectiveEffortFor` and scope sections, to record the exact-current-child source, 30-second finite query wait, no-bootstrap fallback posture, fresh registry/pool resolution on every read, effort-only projection, and nil production wiring when either registry or pool is unavailable.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — remote `conversation_id` reaches this provider only after `handleRequestSessionSettings` has accepted the same id through `RunConfigFor`, and `resolveBoundRunner` independently treats it only as a lookup key into daemon-owned registry state. Child-authored data crosses at `QueryAppliedSettings`, whose bounded parser returns `streamsup.AppliedSettings`; the new resolver projects only inert `Effort` display text and never treats it as authority, a path, an identifier, or an execution input.
- [Tokens, secrets, credentials] Not applicable — the resolver creates, reads, compares, logs, and transmits no credential or key. Existing Noise authentication and per-device lifecycle are unchanged.
- [File operations] Not applicable — the provider opens, creates, joins, resolves, or persists no path. Registry and pool access use their existing synchronized in-memory read APIs.
- [Subprocess / external command execution] No findings — the provider neither spawns nor constructs argv. `QueryAppliedSettings` writes its fixed control envelope to an already-live child; neither the remote conversation id nor returned effort becomes a command, environment value, prompt, or settings change.
- [Cryptographic primitives] Not applicable — no cryptographic primitive, key, nonce, random value, or comparison changes. The existing Noise-sealed request/reply path is untouched.
- [Network & I/O] No findings — the request has already passed the existing authenticated interactive gate and application-frame size cap. One fixed child query runs on the addressed connection's bounded worker, inherits caller cancellation, and gains a 30-second deadline, so a silent child cannot retain it indefinitely. The ticket adds no listener, header, TLS, socket-read, or connection-count behavior.
- [Error messages, logs, telemetry] No findings — every refusal is the content-free `(nil, false)` result and the resolver emits no log. The conversation id, saved values, full `AppliedSettings`, child failure detail, and effective effort never enter an error or telemetry record; downstream JSON encoding keeps the one projected string inert.
- [Concurrency] No findings — no new goroutine, shared cache, or lock ordering is introduced. Registry, pool, and runner snapshots are acquired through separate existing methods; a call queries only the runner from its fresh binding snapshot, and the next call re-resolves. Cancellation and timeout follow `QueryAppliedSettings`'s generation-bound cleanup and cannot target a successor child.
- [Threat model alignment] No findings — unpaired and non-interactive peers remain outside the handler, the relay still sees ciphertext only, and a paired interactive peer can obtain only requester-private display data for a conversation already accepted by `RunConfigFor`. Existing protocol-wide rate limiting remains deferred, while this provider adds no unbounded wait or global goroutine and preserves the per-connection worker/queue containment described by the v2 threat model.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-20

## Revisions

None.
