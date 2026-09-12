# Query the current live child for MCP status (#2382)

## Files read

- `docs/knowledge/INDEX.md` — startup map for the stream supervision, relay, protocol, sessions, and e2e owners.
- `CODING-STYLE.md` — concurrency, error, test, and symbol-citation conventions.
- `docs/knowledge/features/development-verification.md` → `Establish the change surface`, `Prove that tests distinguish the change`, `Protocol boundaries` — construction-site tracing, interleaved writer/response proof, and mapper-boundary requirements.
- `docs/knowledge/features/streamsup-package.md` → `Held-open stdin`, `Testing`, `Turn I/O` — exact-child handle lifetime, the existing context-usage correlator, and the automatic MCP-status policy.
- `docs/knowledge/features/relay-package.md` → `startRelayV2`, requester-only handlers, logging discipline — daemon wiring and private reply ownership.
- `docs/knowledge/features/v2-session-manager-concurrency.md` → `appFrameWorker`, `forwardToRun` — the off-`Run` resolver and manager-owned Noise sealing supplied by #2381.
- `docs/knowledge/features/e2e-harness.md` → `StartStreamInteractiveWithRelay`, spawned-binary mutation guidance — the hermetic daemon and two-phone proof surface.
- `docs/specs/architecture/2381-relay-mcp-status-request.md` → `MCPStatusFor`, `handleMCPStatusRequest` — the merged resolver contract, unavailable mapping, worker context, and requester-only reply path this ticket must wire.
- `docs/specs/architecture/2374-strict-child-mcp-status.md` → `mcpStatusEligible`, `beginMCPStatusChild`, `Parser.emit` — exact-spawn eligibility and automatic response admission.
- `docs/specs/architecture/2375-publish-mcp-status-interactive-wire.md` → `interactiveTurnEmitterV2.Handle`, fake-child MCP rider — the automatic broadcast/event-ring producer that must remain unchanged.
- `internal/streamsup/runner.go` → `Runner`, `RequestMCPStatus`, `spawnAndWait`, `setStdin`, `takeStdin`, `nextControlID` — live writer ownership, per-spawn argv snapshot, and shared control-id sequence.
- `internal/streamsup/parser.go` → `Parser`, `contextUsageRequests`, `consumeLine`, `decodeMCPStatus`, `emit` — response parsing, correlation precedent, existing mapping, and shared-sink boundary.
- `internal/streamsup/context_usage_event_test.go` → `TestRunner_ContextUsageWriteFailureCannotEmit`, `TestRunner_ContextUsageRegistersBeforeWrite` — the nearest interleaved write/result proof.
- `internal/streamsup/mcp_status_policy_test.go` → `TestRunner_MCPStatusPolicy_SpawnSnapshotAndReplacement` — automatic per-child policy and next-spawn mutation proof that must stay green.
- `cmd/pyry/main.go` → `runSupervisor`, `resolveBoundRunner` — the sole composition root and conversation-to-session binding lookup.
- `cmd/pyry/streamsup_runner.go` → `streamRunner` — the production adapter exposing concrete stream-only operations outside `sessions.Runner`.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2` — the named wiring bundle and the sole `V2SessionConfig` construction site.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.MCPStatus` arm — the existing field-for-field ordered mapping this on-demand path must reuse.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON`, `writeMCPStatusAck` — opt-in fake status replies and the stateful process boundary needed for a fresh-query proof.
- `internal/e2e/relay_v2_stream_mcp_status_test.go` → `TestRelayV2_StreamMCPStatusReachesConnectedPhone` — automatic status synchronization and fakephone handshake precedent.

## Context

#2381 consumes `mcp_status_request` on a connection worker and calls an optional
conversation resolver, but production deliberately leaves that resolver nil. The
stream runner can already send an MCP status request, while `Parser.consumeLine`
recognizes successful replies only by payload shape and sends them into the shared
turn sink. Wiring the seam directly to that method would therefore return no value
to the requester and would broadcast the same response through the automatic
interactive/event-ring path.

This ticket adds a solicited-response lane owned by the exact running child. It
correlates the child response before any generic control-response consumers, maps the
claimed event through the existing `turnbridge.MapEvent` arm, and returns it only to
#2381's resolver caller. The existing numeric automatic request and shape-based
publication remain intact.

The refined estimate is about 1,100 written lines and the implementation sketch now
identifies six production source files, exceeding the 800-line and five-file
boundaries. The recorded ancestry is #2202 → #2276 → #2382, so the split-depth
gate forbids another agent split and the floor requires this one-consumer round trip
to remain together. The issue already carries `needs-human:sizing`; implementation
continues in place. The refreshed remote-branch scan found no overlap on the planned
files.

No ADR is warranted. The change extends the established request-id correlator and
off-`Run` resolver patterns without moving package ownership.

## Design

### Exact-child query and eligibility

Add `QueryMCPStatus(ctx)` beside `RequestMCPStatus` on `streamsup.Runner`.
`RequestMCPStatus` remains the best-effort automatic writer and keeps its numeric id
and parser/event behavior unchanged. `QueryMCPStatus` mints an id from the same
runner-wide sequence with a fixed on-demand prefix, registers it with the runner's
concrete parser, writes one request, and waits for that one pending result.

The runner stores the current child's MCP eligibility beside `stdin` under `Runner.mu`.
`spawnAndWait` computes the value once from its immutable argv snapshot with
`mcpStatusEligible`, passes that same value both to `beginMCPStatusChild` and
`setStdin`, and `takeStdin` clears it with the writer. Query registration and the
writer/eligibility snapshot occur under the same runner acquisition. A missing
parser, missing writer, rotation window, or false exact-child eligibility returns
unavailable before registering or writing.

### Correlation and response claiming

Add a dedicated mutex-protected MCP query registry to `Parser`, following
`contextUsageRequests` but returning a `turnevent.MCPStatus` to the waiter instead of
emitting it. Registration precedes the child write. A small write-result latch covers
the response-before-writer-return race: the parser may claim early, but cannot report
success until the writer has reported a successful write.

`consumeLine` consults the MCP query claimant first in its `control_response` arm.
It narrowly decodes the nested request id, then:

- an exact pending id is removed before subtype or payload decode, making the first
  response terminal;
- a valid success uses `decodeMCPStatus` unchanged and completes only that waiter;
- an error or malformed exact-id response completes that waiter as unavailable;
- an id bearing the on-demand prefix but no pending waiter is a canceled, failed,
  late, or duplicate query response and is consumed without reaching any generic
  control-response consumer;
- every other id is unclaimed and follows the existing context-usage, posture,
  initialize/model-list, and automatic MCP-status paths.

The prefix is the deterministic duplicate/late-response safety net. It avoids an
unbounded tombstone set while keeping automatic `RequestMCPStatus` responses outside
the private class. The pending map contains only active waits and is cleared on child
exit/replacement. Clearing completes every waiter as unavailable.

Write failure removes the exact pending registration and publishes the failed write
result before returning unavailable. Context cancellation removes the registration
and returns unavailable. In both cases the prefix continues to consume a late reply,
so it cannot enter the shared sink or satisfy a future query.

```text
relay conn worker -> conversation binding -> streamRunner.QueryMCPStatus
                                            | exact live writer + eligible snapshot
                                            v
                                   child mcp_status request
                                            |
child control_response -> Parser exact-id claimant -> waiting conn worker only
                       \-> unclaimed numeric auto reply -> shared turn sink/ring
```

### Daemon resolver and existing mapping

Expose `QueryMCPStatus` as another concrete method on `streamRunner`, deliberately
off `sessions.Runner`; its only consumer lives in `cmd/pyry`, so widening the session
interface would cascade through unrelated test doubles.

Add a conversation-keyed resolver at the composition root. It uses
`resolveBoundRunner`, rejects an absent, unbound, dangling, or non-query-capable
session, invokes the bound runner with the relay-supplied context, and returns false
for every unavailable outcome. A successful `turnevent.MCPStatus` goes through
`turnbridge.MapEvent` with the requested conversation id. The resolver accepts only
the resulting `protocol.TypeMCPStatus` and `protocol.MCPStatusPayload`, thereby
reusing the single existing order/field/dropped-count mapping rather than duplicating
it.

Thread this function through a named `relayWiring.mcpStatusFor` field and assign it
directly to `V2SessionConfig.MCPStatusFor`. There is no retained list or fallback in
the adapter. The existing relay handler remains the owner of capability and registry
membership gates, correlated wire errors, requester addressing, and Run-owned Noise
sealing.

### Hermetic state change

Keep `PYRY_FAKE_CLAUDE_MCP_STATUS` opt-in. Within one `runStreamJSON` child, count
status requests: the first returns the existing automatic startup fixture and later
requests return a distinct fully populated server row. Each replacement process
starts the count at zero, preserving #2375's once-per-child automatic proof.

Add a daemon e2e that seeds one bound conversation, waits until the automatic report
has been consumed, opens two interactive phones, sends `mcp_status_request` from one,
and reads through a bounded deadline. The requester must receive one correlated fresh
status carrying the second fixture; the observer must receive no status. A following
ordinary turn supplies a causal boundary proving no delayed broadcast or ring replay
appeared.

## Concurrency model

No new goroutine is added in production. Relay resolution stays on the requesting
connection's existing `appFrameWorker`; `QueryMCPStatus` blocks only that worker while
the manager `Run` goroutine and other connection workers continue. The parser's stdout
forwarder claims responses and delivers one value through a buffered per-query channel.

`Runner.mu` orders writer, eligibility, and child teardown. The query-registry mutex
orders registrations and claims; neither lock is held during the child write, parser
decode, channel receive, or relay send. The runner may acquire its own mutex and then
the registry mutex only during fast registration; teardown clears the runner fields,
releases `Runner.mu`, and only then fails registry entries, so no reverse lock order
exists.

Runner-context cancellation, relay-manager cancellation, and child exit all remove
or fail the pending wait. The buffered result channel lets the parser finish if
cancellation wins a select after the response was already claimed; no producer
goroutine can leak.

## Error handling

- No bound session, missing child, missing parser, non-query-capable runner, or
  ineligible exact child: return false and write nothing.
- Child-stdin write failure: remove the pending entry, mark the write failed, and
  return false; a response racing the writer is consumed but cannot become success.
- Resolver context cancellation: remove the pending entry and return false; a late
  same-id response remains private and cannot be reused.
- Child exit or replacement: clear exact-child eligibility, fail all current waits,
  and reset private correlations before the next child becomes live.
- Exact-id error or malformed response: retire the id and return false.
- Exact-id valid success: return the bounded `turnevent.MCPStatus` once.
- Duplicate or late private-id response: consume silently.
- Unknown or automatic numeric response: preserve the existing parser behavior and
  automatic strict-child admission policy.
- Mapping mismatch: return false without logging payload content.

New code logs no request id, conversation id, server value, decoder error, write
error, or raw line. #2381's fixed external unavailable error remains the only client
failure detail.

## Testing strategy

Stream-supervision tests are written and run RED before production changes:

- eligible query writes exactly one prefixed request, accepts only its exact id, and
  returns all bounded status fields while emitting nothing to the sink;
- two overlapping queries receive distinct ids and can complete independently in
  reverse response order;
- unknown and automatic ids do not complete a pending query, while the automatic
  response retains its existing sink behavior;
- exact-id error and malformed replies are terminal; a later valid duplicate remains
  consumed and cannot emit or complete anything;
- missing child, ineligible child, and missing parser write nothing;
- cancellation removes a pending wait and a late response remains consumed;
- a writer exposes a matching response before returning an error, proving the early
  claimant cannot report success and the response cannot leak to the sink;
- child teardown fails an outstanding wait and clears exact-child eligibility.

Daemon resolver tests use real conversation and pool bindings with narrow runner
doubles. They cover absent/unbound/dangling/non-query-capable sessions, context
cancellation, exact bound-runner selection, and successful reuse of
`turnbridge.MapEvent` with two ordered rows, all five fields, and a non-zero dropped
count.

Fake-child tests send two requests through one `runStreamJSON` call and prove the
second response differs while preserving request ids. The e2e test proves fresh
state, exact relay correlation, requester-only delivery, no second-client fan-out,
and no delayed event-ring copy.

Touched-scope verification:

- `go test -race ./internal/streamsup/... ./cmd/pyry/...`
- `go test -race -tags=e2e ./internal/e2e/...`
- `go vet ./...`
- `go build ./cmd/pyry`

## Open questions

None. The ticket and merged dependencies fix the resolver owner, exact-child policy,
private response class, mapper, error collapse, and requester-only relay path.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md`, specifically
the `mcp_status` table row and `Asking for MCP status on demand` section, from the
declaration-only state to the live query contract. State that eligible children are
queried afresh only when their exact spawn used the daemon MCP config with
`--strict-mcp-config`; successful replies are requester-only and do not enter the
interactive fan-out or event ring; and no bound/live/eligible child, a write failure,
cancelation, or an unusable reply produces #2381's `mcp_status.unavailable` error.
Preserve the existing bypass-child privacy and inert-rendering guidance.

## Scope re-check

- Deliverables: 1 — one current-child MCP status round trip from relay request to
  requester-only reply, with unit and hermetic process-boundary proof.
- Production source files: 6 — `internal/streamsup/parser.go`,
  `internal/streamsup/runner.go`, `cmd/pyry/streamsup_runner.go`, `cmd/pyry/main.go`,
  `cmd/pyry/relay.go`, and `internal/e2e/internal/fakeclaude/main.go`.
- Total written work: approximately 1,100 lines including tests and this plan.
- New exported types or interfaces: 0; one exported concrete runner method is added.
- Consumer call sites requiring simultaneous update: 1 (`setStdin`), below ten.
- Acceptance criteria: 5.
- Distinct error/reject branches in the new query: 6, below ten.

Both the production-file and total-line ceilings are exceeded. The parent/grandparent
chain forbids another split, the one-consumer floor prevents separating the correlator
from its resolver, and `needs-human:sizing` records the overage while the build
continues.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `claimMCPStatusQuery` is the single subprocess
  response-claim boundary; `decodeMCPStatus` remains the single field/bounds mapping,
  and `resolveBoundMCPStatus` treats every result as report-only untrusted text.
- [Tokens, secrets, credentials] No findings — no credential lifecycle changes. The
  query id is a daemon-minted correlation label, not authority, is never logged, and
  is echoed only across the existing child pipe.
- [File operations] Not applicable — the design opens, constructs, persists, or
  resolves no path. It reads the in-memory conversation binding through existing
  registry and pool APIs.
- [Subprocess / external command execution] No findings — no argv, environment,
  executable, or signal behavior changes. Eligibility is derived from the immutable
  argv of the already-running child, and server strings never become arguments.
- [Cryptographic primitives] No findings — the change adds no primitive, key, nonce,
  RNG, or comparison. #2381's reply returns through `forwardToRun`, retaining the
  manager's single-owner Noise cipher discipline.
- [Network & I/O] No findings — the existing encrypted application-envelope and
  per-connection queue bounds remain the network limits. One outstanding query can
  occupy only its requesting connection worker; the pending registry holds active
  waits only, and deterministic prefix classification avoids attacker-driven
  tombstone growth.
- [Error messages, logs, telemetry] No findings — new correlation, decode, mapping,
  write, cancellation, and teardown paths log nothing. Claude-authored names,
  statuses, errors, scopes, versions, raw lines, ids, and counts cannot enter logs;
  clients receive only #2381's fixed unavailable message.
- [Concurrency] No findings — no goroutine is created; lock order is explicit;
  registration precedes write; writer outcome gates an early response; child teardown
  and context cancellation fail/remove waits; buffered one-shot delivery cannot block
  the stdout parser when the requester departs.
- [Threat model alignment] No findings — pairing and interactive-capability gates are
  unchanged; ineligible bypass children are refused before write; a claimed reply is
  requester-only and never retained; all Claude-authored strings remain inert display
  data; late and duplicate private replies cannot widen their audience.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-12

## Revisions

None.
