# #2420 — Per-device gate, audit and fresh-status answer for `mcp_reconnect` / `mcp_toggle`

The `cmd/pyry` implementation of `relay.MCPActuator`, and the wiring that makes
`V2SessionConfig.MCPActuator` non-nil for the first time.

## Files read

- `cmd/pyry/question_resolve_v2.go` → `questionResolverV2`, `admit`, `auditQuestion` —
  the per-device gate shape this slice copies: fail-closed eligibility, the allow
  conjunction as defence in depth, one audit record per decision, no log record of its
  own. The one place this slice departs from it is ordering, and the ticket says why.
- `internal/relay/v2session_seams.go` → `MCPActuator`, `V2SessionConfig.MCPActuator` —
  the contract being implemented: comma-ok is the whole refusal vocabulary, the
  post-acknowledgement status crosses back, `ServerName` reaches the implementation
  validated by nothing, the interface-nil trap, and the ordering obligation this ticket
  discharges.
- `internal/relay/v2session_mcpactuate.go` → `handleMCPReconnect`, `handleMCPToggle`,
  `finishMCPActuation`, `rejectMCPActuation` — the caller: membership of the
  conversation is already enforced above the seam, the payload is not read on `false`,
  and every refusal becomes one `CodeMCPActuationRefused`.
- `internal/streamsup/runner.go` → `ReconnectMCPServer`, `SetMCPServerEnabled`,
  `actuateMCP`, `QueryMCPStatus` — the actuation below this slice. `actuateMCP`'s block
  states that the caller's context is the only bound when a child stays alive and never
  answers, which is why this design derives a deadline.
- `cmd/pyry/main.go` → `resolveBoundRunner`, `resolveBoundMCPStatus`, `mcpStatusFor`,
  `mcpStatusQuerier` — the existing bound-child resolution, and the narrow
  consumer-side assertion pattern this slice mirrors for the actuation methods.
- `internal/audit/audit.go` → `Entry`, `Log`, `Outcome` — the forensic sink and its
  fixed attribute set.
- `docs/knowledge/features/audit-package.md` § "Exported surface" — the documented
  precedent that a new caller should reuse `ModalID`/`ModalClass` rather than grow a
  field. Read before departing from it; see **Context** for why its own reasoning does
  not reach this caller.
- `internal/devices/auth.go` → `MayAnswerRemotePermission`, `AuthorizeRemotePermission`
  — the fail-closed predicates; both are total over a nil device.
- `internal/protocol/interactive.go` → `MCPReconnectPayload`, `MCPTogglePayload`,
  `MCPStatusPayload`, `MCPServerStatus` — the wire shapes. `Enabled` is a plain bool, so
  an absent key decodes as `false`, the non-escalating direction.
- `internal/e2e/relay_v2_mcp_status_request_test.go` → the two-phone
  requester-only daemon proof this slice's e2e copies.
- `internal/e2e/internal/fakeclaude/main.go` → `decodeControlRequest`,
  `controlRequestID`, `writeMCPStatusAck`, `subtypeMCPStatus` — the fake child's
  control-request dispatch and its canned MCP report.
- `internal/streamsup/envelope.go` → `WriteMCPReconnect`, `WriteMCPToggle`,
  `controlResponseSuccessSubtype` — the request shape the fake must recognise and the
  ack shape `claimMCPActuation` accepts.

## Context

`#2419` declared `relay.MCPActuator` and left `V2SessionConfig.MCPActuator` nil at every
construction site; `#2418` landed `streamsup.Runner`'s two actuation primitives, which
own neither membership nor authorization. This slice is the only sanctioned
implementation of the seam and the wiring that makes the field non-nil, so it is the
authorization boundary for the first inbound verb pair that changes a running child's
configuration.

**Size.** Re-measured against the one-ticket boundary: 5 production files, ~7 reject
branches, 0 new exported types, 0 consumer call sites needing simultaneous update, 5
acceptance criteria — all within the table. Total written work is over the 800-line
line (estimated ~1,250). The split-depth gate closes the split: the ticket's grandparent
is `#2202`, the issue already carries `needs-human:sizing`, and per the builder's depth
rule the run continues rather than re-proposing. The split I would otherwise have made
is recorded as a comment on the ticket.

**ADR.** No new decision record is warranted; this realises ADR 025 §6's audit
requirement for a new decision family rather than changing a boundary.

### The one departure from the `audit-package.md` precedent

That overview records, twice, that a new caller earns the `ModalID`/`ModalClass` pair by
what it already means rather than growing a field — `#1986`'s reasoning being that *"the
existing id/class pair already says which batch and what kind"*. That reasoning does not
reach this caller: an MCP actuation record has to name **three** things — the
conversation, the server and the kind — and there are two slots. `pairingMinterV2`'s own
block states the rule that forbids the alternative: filling `ModalID` with a different
kind of value "would put a different kind of value in a field an operator reads as one
thing", and a conversation id is not a one-time nonce.

So `internal/audit.Entry` gains exactly two fields, `ConversationID` and `Target`, both
empty at all five existing call sites, and `ModalID` stays empty here for the reason
`auditMint` leaves it empty. This is a change to a shared forensic format and the
documentation stage should fold the reasoning into `audit-package.md`.

## Design

### New file: `cmd/pyry/mcp_actuate_v2.go`

```go
// The narrow consumer-side view of *streamsup.Runner's two actuation methods,
// asserted at the call site exactly as mcpStatusQuerier is.
type mcpChildActuator interface {
    ReconnectMCPServer(ctx context.Context, serverName string) bool
    SetMCPServerEnabled(ctx context.Context, serverName string, enabled bool) bool
}

type mcpActuatorV2 struct {
    statusFor   func(ctx context.Context, convID string) (protocol.MCPStatusPayload, bool)
    actuatorFor func(convID string) (mcpChildActuator, bool)
    logger      *slog.Logger
}

func newMCPActuatorV2(statusFor …, actuatorFor …, logger *slog.Logger) *mcpActuatorV2

func (a *mcpActuatorV2) Reconnect(ctx, protocol.MCPReconnectPayload, *devices.Device) (protocol.MCPStatusPayload, bool)
func (a *mcpActuatorV2) SetEnabled(ctx, protocol.MCPTogglePayload, *devices.Device) (protocol.MCPStatusPayload, bool)
```

Both arms are one-line adapters over a shared `actuate`, which carries the whole
ordering. The per-arm differences are the audit class constant
(`classMCPReconnect` / `classMCPToggle`, valued as the wire type constants) and a
closure that performs the one child call.

`boundMCPChildActuator(convReg, pool)` returns the `actuatorFor` closure: it calls the
existing package-level `resolveBoundRunner` and asserts `mcpChildActuator` on the
result, returning `(nil, false)` for every non-resolvable state. It is declared in this
file and called from `main.go`, where `convReg` and `pool` are in scope, beside
`mcpStatusFor(convReg, pool)`.

### The ordering — this is the substance

1. **Gate, first, before anything else.** `dev.MayAnswerRemotePermission()`, then the
   `devices.AuthorizeRemotePermission(dev, devices.OutcomeAllow)` conjunction as defence
   in depth. A refusal audits `denied_unauthorized` and returns; **no seam is touched, so
   no control request of any kind reaches a child**, membership read included. This is
   the deliberate departure from `questionResolverV2.admit`'s lookup-then-gate order:
   that resolver's look-up is a free map read, where this one's is a control request
   written to the child's stdin, so ordering it first would let any paired device make a
   child work on demand.
2. **Seams.** Either seam nil (foreground / PTY, no live-child resolution) audits
   `denied` and returns. It sits *below* the gate rather than above it — the inverse of
   `admit`'s step 1 — so that every refusal past the gate audits and AC-3 is total.
3. **Derive the deadline.** One `context.WithTimeout(ctx, mcpActuationTimeout)` bounds
   the whole actuation; every child wait below inherits it. `actuateMCP`'s block states
   that a child which stays alive and never answers is bounded by the caller's context
   alone, and an accepted action makes three round trips.
4. **Bound child.** `actuatorFor(convID)` — a daemon-local registry and pool read, no
   child contact. A miss audits `denied`.
5. **Membership read.** `statusFor(ctx, convID)` — the first control request. A failed
   read, and a `ServerName` the returned report does not contain, each audit `denied`.
6. **Actuate.** The per-arm closure. `false` audits `denied`.
7. **Audit `allowed` exactly once**, then the **post-acknowledgement read**:
   `statusFor(ctx, convID)` again, and its payload is the answer. A failed post-ack read
   returns the zero payload and `false` — a wire refusal for an action that happened,
   which is recorded as `allowed` because that is what the daemon decided. The record is
   written before the read so a read that burns the remaining budget cannot delay it.

### `ServerName` validation

Exact membership in the child's current report is the sole validator, and it is an
allow-list rather than a shape guess: a charset or length rule would refuse a
legitimately-named server, where equality against names the child itself just reported
cannot. There is no separate empty-name branch — an absent key decodes to `""`, which no
real report contains, so membership already refuses it.

The string handed to the child is the **matched row's `Name`**, not `p.ServerName`. They
are equal by construction, so this is a statement about provenance rather than a
transformation: the value written to the child's control stream is one the child
authored. `p.ServerName` — remote-authored, bounded only by the transport frame cap —
reaches the audit record and nothing else.

**It reaches that record bounded**, through the existing `truncateForLog` in
`cmd/pyry`, at `mcpAuditTargetMax` = 128 bytes: MCP server names are short identifiers,
and that helper's own block already states this exact threat — a hostile gated device
padding a logged field. Without the bound, any paired device, *including one the gate
refuses*, can write frame-cap-sized text into a record on every attempt; the log is
teed into `internal/control`'s bounded ring, so that is an eviction attack on the
operator's own forensic history rather than mere noise. The injection half of the same
threat is already closed structurally — the daemon's handler is `slog.TextHandler`,
which quotes any value carrying a space, an `=` or a control character, so a name
cannot forge a second attribute.

### Wiring

- `cmd/pyry/relay.go`: a new `relayWorld` field `mcpActuatorFor func(convID string) (mcpChildActuator, bool)`;
  `newMCPActuatorV2(w.mcpStatusFor, w.mcpActuatorFor, logger)` constructed beside
  `questionResolver` and assigned to `V2SessionConfig.MCPActuator` **unconditionally**.
  Unconditional is `questionResolverV2`'s shape and for its stated reason: building it
  only under non-nil seams and assigning the typed-nil pointer is exactly the
  interface-nil trap the field's own block forbids. Inertness lives inside the resolver,
  where a nil seam is a plain field compare. The observable consequence on an unwired
  daemon changes from silence to the one merged reject, which discloses nothing.
- `cmd/pyry/main.go`: one field assignment, `mcpActuatorFor: boundMCPChildActuator(convReg, pool)`.
- `cmd/pyry/relay_guard_test.go`: the assertion that `MCPActuator` stays unwired is now
  false and is inverted.

### `internal/audit/audit.go`

Two fields on `Entry` and two attributes on `Log`'s fixed set:

| field | key | meaning |
|---|---|---|
| `ConversationID` | `conversation_id` | the daemon-owned conversation the decision was scoped to; empty where the decision names none |
| `Target` | `target` | the object the decision was about, **as the asking device named it** — remote-authored, validated only by the decision that recorded it; never identity, never a secret, never a path component |

The package still imports only `log/slog` and still cannot reach a plain token.

### `internal/e2e/internal/fakeclaude/main.go`

Two new recognised subtypes, `mcp_reconnect` and `mcp_toggle`, each answered
unconditionally with the success ack `claimMCPActuation` reads — the posture the
`initialize` and `get_context_usage` arms record, since nothing else sends these
requests, so no existing suite's bytes change. Answering sets a sticky
`mcpActuated` flag, after which `writeMCPStatusAck` reports its server row with
`status: "pending"`. That is what makes a post-acknowledgement read observably
different from the membership read on the wire, and it is claude's real behaviour: the
committed `mcp_status_v2.1.259.json` capture shows a just-reconnected server commonly
reading `pending`.

## Concurrency model

No goroutines and no locks are introduced, so this slice establishes no lock ordering.
Both methods run on the addressed connection's `appFrameWorker`, which is where the
relay dispatches them precisely because they block on a child round trip. The only
shared state read is the conversations registry and the session pool, each behind its
own mutex, through the two injected closures.

The check-then-act between the membership read and the actuation is deliberately not
atomic, and the gap is safe: a child that dies or is replaced in it makes `actuateMCP`
refuse (it re-checks `mcpStatusEligible` and the child generation under the runner's
leaf mutex), and a server that disappears from the child's inventory in it makes the
child's own answer the arbiter. Nothing here can be made atomic across a process
boundary, and the failure direction is refusal.

## Error handling

| failure | wire | audit |
|---|---|---|
| gate: nil / ineligible device | merged reject | `denied_unauthorized` |
| seam unwired (foreground / PTY) | merged reject | `denied` |
| no bound eligible child | merged reject | `denied` |
| membership read failed or timed out | merged reject | `denied` |
| server not in the child's current report | merged reject | `denied` |
| child refused the actuation, errored, or never answered | merged reject | `denied` |
| accepted, post-acknowledgement read failed | merged reject | `allowed` |
| accepted | fresh `mcp_status` | `allowed` |

Exactly one record per call. The wire merges every row; the audit keeps apart the split
that matters — *the daemon refused to let this device decide* versus *the daemon could
not do it* — which is what stops the verb reporting whether a named server exists or
whether the asker is privileged. Finer per-reason vocabulary is deliberately not
recorded: it would be a third shared-contract field for a reader nobody has identified.

The timing edge the seam's block flags is narrowed rather than widened by this ordering:
an ineligible device is always refused immediately, so the latency it can observe is
constant and tells it nothing about any server.

Nothing on this path is logged outside the audit record. Server names, status, scope,
version and error text are claude-authored, and the asked-for name is remote-authored.

## Testing strategy

`cmd/pyry/mcp_actuate_v2_test.go`, table-driven over both verbs with a recording fake
for each seam:

- Gate refusal (nil device; paired but ineligible device) — both seam counters stay at
  zero, one `denied_unauthorized` record, `false`. **This is AC-1's proof.**
- Unwired seams — `denied`, no panic.
- No bound child; failed membership read; a server name absent from the report; a child
  that refuses — one `denied` record each, and no actuation and no second read past the
  point each fails. **AC-2.**
- Accepted — the returned payload is the **second** read's, not the membership read's;
  one `allowed` record; the actuation received the name from the child's report. **AC-4.**
- Accepted, post-ack read fails — one `allowed` record, `(zero, false)`.
- A `pending` row in the post-ack read is returned unchanged. **AC-4.**
- `SetEnabled` passes `Enabled` through in both directions, with no read of current
  state.
- The actuation's context carries a deadline, asserted inside the fake child.
- Record shape: `conversation_id`, `target` and the device's hash and label present;
  exactly one record per call; the serialised record contains neither the device's plain
  token nor any server config, argv, environment or version string. **AC-3.**
- A frame-cap-sized `ServerName` from a device the gate refuses produces a record whose
  `target` is bounded at `mcpAuditTargetMax` and whose rune boundaries are intact.

`internal/audit/audit_test.go`: the two keys join `TestLog_ExactKeySet` and
`TestLog_FieldCompleteness`.

`internal/e2e/relay_v2_mcp_actuation_test.go` (`e2e` tag), the shape
`relay_v2_mcp_status_request_test.go` establishes — two phones, one privileged and one
not, against a fake child that answers both verbs:

- The privileged phone sends `mcp_reconnect`, then `mcp_toggle`, and receives exactly
  one correlated `mcp_status` for each, each carrying the post-acknowledgement
  `pending` row.
- The observer phone receives no copy of either, proven against the same later-turn
  causal read boundary that test uses.
- The daemon log carries one audit record per accepted action.
- The unprivileged phone's `mcp_reconnect` is refused. **AC-5, plus AC-1 end to end.**

## Open questions

Both resolved during implementation; recorded here rather than deleted, because the
verifier reads this section for questions that were ignored rather than answered.

- ~~The deadline constant's value.~~ **Settled at `30 * time.Second` for the whole
  actuation, unchanged from the plan's proposal.** The hermetic daemon spec completes
  both verbs — six child round trips — in about a second, so nothing observed argues for
  a longer bound or for splitting it per round trip.
- ~~Whether the e2e's unprivileged-phone arm fits inside this slice's budget.~~ **It
  fit and it landed.** `assertMCPActuationRefused` proves AC-1 at the daemon level: an
  unprivileged device's `mcp_reconnect` is answered with the single merged,
  non-retryable reject and reaches no child.

## Documentation handoff

**Pending — owned by the documentation stage. Not touched by this slice.**

- `docs/protocol-mobile.md` § `Actuating MCP servers on demand`, and the
  `mcp_reconnect` / `mcp_toggle` rows of the inbound verb table: move from the
  declaration-only contract to the live one — an accepted actuation is gated per device,
  audited, and answered with a status read taken after the child's acknowledgement; a
  just-reconnected server may legitimately read `pending`; every refusal returns the
  single coded reject `#2419` declares. Drop the "consumed but inert until the
  per-device gate lands" posture from those rows and that section. **Preserve** the
  existing bypass-privacy guidance and the render-claude-authored-values-as-inert-text
  rule — that second use of "inert" is not the nil-seam posture being dropped.
- `docs/knowledge/features/audit-package.md`: `Entry` gains `ConversationID` and
  `Target`, and the overview's twice-stated "reuse the id/class pair rather than grow a
  field" precedent needs the boundary of its own reasoning recorded — see **Context**.

## Revisions

### 2026-09-14 — a sixth production file: the `streamRunner` adapter

**What changed.** `cmd/pyry/streamsup_runner.go` gains `ReconnectMCPServer` and
`SetMCPServerEnabled` forwards. The plan's file list did not have it, and the ticket's
estimate explicitly counted three production files.

**What drove it.** `Session.Runner()` does not return `*streamsup.Runner`. It returns
`sessions.Runner`, whose single production implementation is `cmd/pyry`'s `streamRunner`
adapter — a value type that forwards each method explicitly, because
`(*streamsup.Runner).State` has a covariant return the interface cannot accept. So the
`mcpChildActuator` assertion in `boundMCPChildActuator` failed against the adapter, and
every actuation refused. The e2e daemon test caught it on its first run; no unit test
could have, because every unit test injects the interface directly.

The same reasoning governs `resolveBoundMCPStatus`'s `mcpStatusQuerier`, which the plan
read and copied without noticing that `QueryMCPStatus` has a matching forward in that
adapter. That forward was the evidence, and reading it as "the concrete runner satisfies
the assertion" rather than "an adapter was widened for it" is the miss.

**Consequence for the design:** none. The forwards add no check — membership and
authorization are settled above the adapter, in the only place that can audit them, and
the concrete methods say the same from below.

**Consequence for the size:** the production file count is 6, one over the one-ticket
boundary's five. It is stated rather than acted on, for the reason **Context** gives:
the split-depth gate is closed and the run continues.

## Security review

**Verdict:** PASS (after one MUST FIX, applied to the plan above before this commit)

**Findings:**

- **[Trust boundaries]** No finding. The design has one boundary and it is a single
  function: `actuate` holds the whole ordering, and both wire arms are one-line
  adapters over it, so there is no second place a payload is judged. The three
  untrusted inputs are named with their differing provenance — `ConversationID` arrives
  having passed `KnownConversation` and stays a lookup key, `ServerName` has passed
  nothing and is validated here or nowhere, `Enabled` is a bool with nothing to
  validate. The claude-authored return payload crosses outward untouched and unlogged.
- **[Errors, logs, telemetry]** **MUST FIX — applied.** `p.ServerName` was to reach
  `audit.Entry.Target` unbounded. It is remote-authored and capped only by the transport
  AEAD frame, the audit path is reachable *before* the gate's refusal is recorded, and
  `control.SlogTee` tees every record into `internal/control`'s bounded ring — so any
  paired device, privileged or not, could evict the operator's recent forensic history
  by padding the field and retrying. Bounded through the existing `truncateForLog` at
  `mcpAuditTargetMax`; the plan's **`ServerName` validation** section carries the
  reasoning and the testing strategy asserts it. The sibling injection threat is closed
  without code: the daemon's handler is `slog.TextHandler`, which quotes any value
  carrying a space, `=` or control character, so the field cannot forge a second
  attribute. Otherwise the path writes no log of its own, and claude-authored status,
  scope, version and error text reach no record at all.
- **[Tokens, secrets, credentials]** No finding. Nothing is minted, stored or rotated
  here. Only `dev.TokenHash` and `dev.Name` reach the record, the `auditQuestion`
  pattern, and `internal/audit` cannot reach a plain token because it imports only
  `log/slog`. Revocation is upstream: a revoked device fails the first-frame auth gate
  and never reaches this seam.
- **[File operations]** Not applicable by design, and the design decision is the
  substance rather than the absence: `ServerName` must never become a path component,
  and this path opens, stats and writes nothing. Its only sink is a log record.
- **[Subprocess / external command execution]** No finding. Nothing here execs. The
  name reaches the child's stdin control stream JSON-marshalled by
  `marshalMCPReconnectEnvelope` / `marshalMCPToggleEnvelope` — never argv, never a
  shell — and under this design the string written is the child's own reported name, so
  no remote byte reaches that stream at all. Whether the child then spawns a server
  process is claude's boundary, driven by its own config rather than by our bytes.
- **[Cryptographic primitives]** Not applicable: nothing random, hashed or compared
  against a secret. The membership comparison is plain string equality and correctly so
  — server names are not secrets. Its timing is observable only to a device that has
  already passed the gate, because the gate runs first.
- **[Network & I/O]** No finding. The payload is frame-capped upstream; the one
  remaining unbounded field is the MUST FIX above. Timeout discipline is the reason the
  design derives a single deadline rather than inheriting the worker's: `actuateMCP`
  states that a child which stays alive and never answers is bounded by the caller's
  context alone. Occupancy: the worker is per-connection and serial, so one connection
  holds at most one actuation in flight; concurrent privileged connections can each hold
  one, which is bounded by the paired-device count the operator controls and is no
  escalation over the turns those devices can already send.
- **[Concurrency]** SHOULD FIX, to be checked in Phase B: the deadline must be derived
  from the caller's `ctx`, never from `context.Background()`, or manager shutdown cannot
  terminate the wait — the seam's block makes honouring `ctx` a MUST. `defer cancel()`
  on every arm. Otherwise no finding: no locks are taken so no ordering is established,
  no goroutine is spawned so none can leak, and the one check-then-act gap (between the
  membership read and the actuation) fails toward refusal because `actuateMCP` re-checks
  eligibility and the child generation under the runner's leaf mutex. The audit record
  is written after the child accepted and before the post-acknowledgement read, so a
  crash in that microsecond-wide window loses a record for an action that happened; the
  alternative ordering would claim an action that never did, which is the worse lie.
- **[Threat model alignment]** No finding. The threat this slice exists to close —
  an unprivileged paired device escalating to a configuration change on a running child
  — is closed by the gate-first ordering, which also narrows rather than widens the
  merged reject's timing edge that `MCPActuator`'s own block flags. Per-conversation
  device scoping is explicitly **out of scope and not a new exposure**: a privileged
  device may already act in any hosted conversation through `send_message` and
  `MCPStatusFor`, and narrowing that is a property of the whole remote head rather than
  of this verb. The `protocol-mobile.md` update is the documentation stage's, recorded
  under **Documentation handoff**.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14
