# #2419 — relay: declare `mcp_reconnect` / `mcp_toggle` and route them to a gated seam

## Files read

- `internal/relay/v2session_mcpstatus.go` → `handleMCPStatusRequest`, `rejectMCPStatusRequest`,
  `forwardMCPStatusReply` — the decode → membership → seam ordering this ticket repeats, and the
  requester-only reply lane it reuses verbatim rather than rebuilding.
- `internal/relay/v2session.go` → `dispatchAppFrame`, `enqueueAppFrame`, `appFrameJob`,
  `appFrameKind`, `appFrameWorker` — the interception switch, the tagged hand-off and the
  per-connection worker the accepted actuation runs on.
- `internal/relay/v2session_seams.go` → `QuestionResolver`, `MCPStatusFor`, `AttachmentIntake`,
  `V2SessionConfig` — the seam shape (whole typed payload plus `*devices.Device`), the written
  ordering obligation this ticket copies, and the nil-inert posture every optional seam documents.
- `internal/relay/v2session_mint_pairing.go` → `handleMintPairing` — the precedent for reading
  `s.device` from the `appFrameWorker` goroutine rather than from `Run`.
- `internal/protocol/interactive.go` → `MCPStatusRequestPayload`, `MCPStatusPayload`,
  `MCPServerStatus` — the payload doc-block conventions and the answer shape this verb reuses.
- `internal/protocol/codes.go` → `TypeMCPStatus`, `TypeMCPStatusRequest`,
  `CodeMCPStatusUnavailable` — the v2-only MCP type block and the reject-code neighbourhood.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `v2OnlyTypes`,
  `TestTypeConstants_V1V2Partition`, `TestErrorCode_Constants_MatchSpec` — the four drift
  detectors a new type and a new code must each be added to.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `TestEveryInboundV2TypeHasHandler` — the
  structural guard that reads `dispatchAppFrame`'s case selectors as the dispatch registry.
- `internal/streamsup/runner.go` → `ReconnectMCPServer`, `SetMCPServerEnabled`, `actuateMCP` —
  #2418's landed actuators, and the written statement that their single bool collapses refusal
  and unavailability *because* this ticket publishes one merged reject.
- `internal/relay/v2session_mcpstatus_test.go` → `mcpStatusManagerFor`, `sendMCPStatusRequest`,
  `waitMCPStatusReply`, `assertMCPStatusError` — the relay test harness shape to mirror.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § the `mcp_status_request`
  paragraph — a blocked-seam test must prove BOTH halves (another connection replies before
  release AND the original connection's reply decrypts after release); either alone misses one
  side of the ownership boundary.
- `docs/knowledge/features/v2-session-manager-test-surface-same-package-unit-tests-internal-relay.md`
  § the `mcp_status_request` paragraph — two proof traps carried forward here: a success fixture
  whose requested id equals the result's `ConversationID` stays green when the handler wrongly
  overwrites the daemon-authored result, and searching logs for payload sentinels alone does not
  prove decoder-error text is absent.
- `docs/protocol-mobile.md` § Security model, threat 4 — per-device revocation is the standing
  control over which paired device may actuate; the gate that consumes it is #2420's.

## Context

#2381 declared `mcp_status_request` and built two reusable pieces: the off-`Run` worker route and
the requester-only reply lane in `internal/relay/v2session_mcpstatus.go`. #2418 landed the
daemon-side actuation — `ReconnectMCPServer` and `SetMCPServerEnabled` on `streamsup.Runner`, each
reporting whether the live child accepted.

Nothing joins the two yet. This slice declares the wire contract for both actuation verbs and
routes them to a seam that is **nil at every construction site**, so both frames are consumed but
inert. The gate slice (#2420) is the only thing that may ever be wired behind it.

The nil seam is not a placeholder — it is the security control. The relay handler applies no
authorization of its own, exactly as `handleModalAnswer` and the `QuestionResolver` path apply
none, so what makes the interception fail-safe before its gate exists is structural: a nil seam
means no actuation can reach a live child. `QuestionResolver`'s doc block already carries this
written ordering obligation for the same reason; the new seam carries its own copy.

**No ADR is warranted.** The merged-refusal contract is a consequence of decisions already
recorded (the relay judges nothing; the audit record lives in `cmd/pyry`), and the seam follows
`QuestionResolver`'s established shape rather than introducing a new one.

## Design

### Protocol declarations — `internal/protocol/interactive.go`

Two inbound payload types, declared beside `MCPStatusRequestPayload`:

```go
type MCPReconnectPayload struct {
    ConversationID string `json:"conversation_id"`
    ServerName     string `json:"server_name"`
}

type MCPTogglePayload struct {
    ConversationID string `json:"conversation_id"`
    ServerName     string `json:"server_name"`
    Enabled        bool   `json:"enabled"`
}
```

Flat rather than `MCPTogglePayload` embedding `MCPReconnectPayload`: embedding promotes the fields
for JSON and would work, but it makes the toggle's key-set assertion read through a type whose own
doc block describes a different verb, and it couples two wire shapes that are free to diverge.

`Enabled` is a plain `bool`, not `*bool`, and the doc block states why. An absent key decodes as
`false` — "disable" — which is the **non-escalating** direction, so an omission can never turn a
server on. A pointer would hand the relay a third state (nil) it would then have to interpret, and
interpreting it is precisely the judgement this package is forbidden from making. A plain bool also
marshals the key unconditionally, which is what lets the committed fixture pin a complete key set.

Both doc blocks record that `ConversationID` and `ServerName` are unverified remote-authored
strings: the relay decodes them, hands them across the seam, and never logs, returns or path-joins
either.

### Type and code constants — `internal/protocol/codes.go`

Added to the existing v2-only MCP block:

- `TypeMCPReconnect = "mcp_reconnect"` — phone → binary, switch-intercepted.
- `TypeMCPToggle = "mcp_toggle"` — phone → binary, switch-intercepted.

Bare verb names, not `*_request`: `mcp_status_request` carries its suffix only because its answer
reuses the `mcp_status` type name and the two would collide. These two have no such collision, and
they match the child's own control protocol (`WriteMCPReconnect` / `WriteMCPToggle`).

Neither goes in `inboundAppTypeSet`, so `IsKnownAppType` rejects both and a v1 client never admits
them.

One new error code, beside `CodeMCPStatusUnavailable`:

- `CodeMCPActuationRefused = "mcp_actuation.refused"` — **non-retryable**.

Distinct from `mcp_status.unavailable` (which is retryable and belongs to the read path), and named
for #2418's own "actuation" vocabulary. It is the single answer to every seam refusal.

### The merged refusal

Gate denial, unknown server, no live child and failed actuation all produce **one** reject:
`mcp_actuation.refused`, `retryable: false`, static message, no server name, no Claude-authored
text. This is a contract decision, not a simplification:

- Distinguishing "not authorized" from "no such server" would turn the verb into an oracle for
  which MCP servers exist on the host.
- Distinguishing "not authorized" from "actuation failed" would tell an unprivileged device that it
  is unprivileged, which is a fact about the asking device that the request must not be able to
  learn.
- `retryable` must be `false` for all of them, or the flag itself re-splits what the code merged: a
  retryable refusal would read as "transient, i.e. not the gate."

The reasons stay apart in the daemon-side audit record, which is #2420's.

### Seam — `internal/relay/v2session_seams.go`

```go
type MCPActuator interface {
    Reconnect(ctx context.Context, p protocol.MCPReconnectPayload, dev *devices.Device) (protocol.MCPStatusPayload, bool)
    SetEnabled(ctx context.Context, p protocol.MCPTogglePayload, dev *devices.Device) (protocol.MCPStatusPayload, bool)
}
```

plus `V2SessionConfig.MCPActuator MCPActuator`, optional, nil at every construction site.

Six properties the doc block carries:

1. **The whole typed payload crosses**, with `*devices.Device` alongside — `QuestionResolver`'s
   shape. The relay judges nothing about identity and writes no audit record.
2. **The accepted answer's payload crosses back.** The seam reports accept-or-refuse and hands back
   the `MCPStatusPayload` to send. The relay performs no second read and never reaches
   `MCPStatusFor` on this path: #2420 owes a status read taken *after* the child acknowledged, and
   a read the relay issued for itself could not be that one.
3. **Comma-ok is the whole refusal vocabulary.** `false` means refused, for any reason; a caller
   MUST NOT read the payload on `false`. `true` with an empty `Servers` is a real answer.
4. **Ordering obligation**, copied from `QuestionResolver`: nothing may be wired here until #2420's
   per-device gate exists. The handler applies no authorization, so the nil seam is what makes the
   interception fail-safe.
5. **`ServerName` is validated by NOTHING and the implementation is its sole validator** — the
   obligation `Interrupter` and `SessionStarter` state for `conversationID`, restated here because
   this is where #2420's implementer meets it and Go's type system cannot say "untrusted". It
   arrives as a bare string authored by a paired client. An implementation MUST shape-check it
   before any use, MUST NOT let it become a path component, and MUST NOT let it reach a shell.
   `ConversationID` arrives having passed `KnownConversation`, so it names a conversation the
   daemon's own registry holds; `ServerName` has passed nothing. Length is bounded only
   transitively, by the transport's AEAD frame cap — one name per frame, so the cap does bind, but
   an implementation that sizes a buffer from it is relying on a bound stated elsewhere.
   (Below the seam, #2418's `marshalMCPReconnectEnvelope` / `marshalMCPToggleEnvelope` `json.Marshal`
   the name into the child's control request rather than concatenating it, so the child's control
   stream is not injectable through this field — verified, and a property of those functions rather
   than of this contract.)
6. **A TYPED-NIL ASSIGNMENT DEFEATS THE NIL GATE.** `MCPActuator` is an interface, so
   `cfg.MCPActuator = (*someActuator)(nil)` is non-nil to `dispatchAppFrame`'s `== nil` check: the
   frame is admitted, the handler calls a method on a nil pointer, and `internal/relay` has no
   `recover()` anywhere, so the process dies. That is fail-closed for authorization — no actuation
   reaches a child — but it is a remote-triggerable crash reachable only after a wiring bug. Every
   optional interface seam on this struct shares the shape; it is called out on THIS one because
   here the nil check is a security gate rather than a convenience. Wire a concrete non-nil
   implementation or leave the field unset; never assign a nil-valued concrete type.

Nil behaviour: the frame is **consumed but inert** — not one byte of its payload parsed, no reply.
Mirrors `HistoryPage` / `AttachmentIntake` / `PairingMint`, and is what leaves every existing
construction site compiling and behaving unchanged.

Bounded time on the worker, not `Run`: a live implementation waits for a child round trip, so it
MUST honour `ctx`. It must never touch `V2Session` or Noise state.

### Interception — `internal/relay/v2session.go`

Two new arms in `dispatchAppFrame`'s switch, placed beside the `TypeMCPStatusRequest` arm:

```go
case protocol.TypeMCPReconnect:
    if m.cfg.MCPActuator == nil || !s.interactive {
        return
    }
    m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameMCPReconnect})
    return
```

and the mirror for `TypeMCPToggle` / `appFrameMCPToggle`. Both gates stay on `Run`: neither posture
decodes the payload or consults membership, which is what buys "an unwired daemon parses zero
remote-authored bytes."

Two new `appFrameKind` members and two new `appFrameWorker` arms dispatching to the handlers below.
The case selectors are what `TestEveryInboundV2TypeHasHandler` reads, so handing off satisfies it.

### Handlers — `internal/relay/v2session_mcpactuate.go` (new)

Two entry points sharing four helpers, so the two verbs cannot drift in their ordering or in their
reject vocabulary:

| Symbol | Contract |
|---|---|
| `handleMCPReconnect(ctx, s, plaintext)` | Decode envelope → decode `MCPReconnectPayload` → membership → `MCPActuator.Reconnect` → answer. |
| `handleMCPToggle(ctx, s, plaintext)` | Same four steps against `MCPTogglePayload` / `SetEnabled`. |
| `decodeMCPActuationEnvelope(s, plaintext, verb) (protocol.Envelope, bool)` | Second and last decode of the frame; the failure arm is unreachable after `dispatchAppFrame` matched these bytes, logs no decoder error, and replies nothing (no id to correlate to). |
| `mcpActuationConversationHosted(ctx, s, id, verb, conversationID) bool` | `KnownConversation`, else one `conversation.not_found`. |
| `finishMCPActuation(ctx, s, id, verb, payload, ok)` | `ok` → one `mcp_status` envelope carrying the seam's payload; `!ok` → the single merged reject. |
| `rejectMCPActuation(ctx, s, id, verb, reject, reason)` | One coded error through the reused reply lane; `reason` is a daemon-authored static string used only in the local log record. |

**Ordering is the design**, and it is the same ordering `handleMCPStatusRequest` states: decode
must precede membership, and membership must precede the seam, so each earlier rejection stops the
later dependency from ever learning an invalid or unhosted id.

Every reply — success and both rejects — returns through the existing
`forwardMCPStatusReply`, reused verbatim rather than copied. That helper builds the envelope with
`InReplyTo` set, no `EventID`, and forwards through `forwardToRun`, so the seal happens on `Run`
under `s.send` and the worker never touches a `CipherState`. Its own diagnostic strings name the
status path because that is where the lane lives; the new file's header records that, so a reader
who sees a `v2.mcp_status.*` event on an actuation marshal failure is not misled.

`s.device` is read on the worker goroutine. That is `handleMintPairing`'s established precedent in
this package rather than a new liberty; a conn with no authenticated device passes `nil`, and the
gate below is nil-receiver-safe by the same convention `MayAnswerRemotePermission` follows.

### Fixtures

`internal/protocol/testdata/mcp_reconnect.json` and `mcp_toggle.json`, each one complete envelope,
pinning the full key set and every JSON value type.

## Concurrency model

No new goroutine. Both verbs reuse the existing per-connection `appFrameWorker`:

- The two nil/capability gates run on `Run` and return immediately.
- An accepted frame is handed to the addressed connection's worker through the bounded
  `s.appFrames` FIFO, so one blocked actuation stalls that connection's later frames and **no
  other connection's**. Starting a free-standing goroutine per actuation would discard both the
  per-connection serialization and the queue bound.
- The seam may block on a child round trip; it receives the worker's `ctx` and must honour it, so
  manager shutdown terminates the wait.
- Every reply crosses back through `forwardToRun`, so `s.send.Encrypt` stays on `Run` — the
  single-owner-cipher invariant, untouched.
- Queue overflow policy is `enqueueAppFrame`'s existing one (tear down at 4421), inherited
  unchanged.

## Error handling

| Condition | Reply | Retryable | Seam consulted | Membership consulted |
|---|---|---|---|---|
| Seam nil, or connection not interactive | none (consumed, inert) | — | no | no |
| Envelope does not decode (unreachable) | none (no id to correlate) | — | no | no |
| Payload does not decode | `protocol.malformed` | false | no | no |
| Conversation not hosted | `conversation.not_found` | false | no | yes |
| Seam refuses, any reason | `mcp_actuation.refused` | false | yes | yes |
| Seam accepts | one `mcp_status` | — | yes | yes |

Exactly one frame leaves the relay per request on every non-inert row.

**Never logged:** decoder errors (`encoding/json` quotes the offending remote-authored input),
`conversation_id`, `server_name`, `enabled`, and every string in the returned payload (all
Claude-authored). **Logged:** the static verb name, `conn_id`, the mapped code, and a
daemon-authored static reason.

## Testing strategy

RED first: the protocol fixtures and the relay tests both fail before the declarations exist.

`internal/protocol/interactive_test.go` — per payload: a round-trip against the committed fixture
and a wire-shape test asserting the exact key set, each key's JSON value type, and the zero-value
encoding (which is what pins `enabled` as always-present).

`internal/protocol/compat_test.go` — both types rejected by `IsKnownAppType`; both added to
`v2OnlyTypes` and the v1/v2 partition list; `CodeMCPActuationRefused` added to the code map.

`cmd/pyry/relay_guard_test.go` — both classified `switch-intercepted`.

`internal/relay/v2session_mcpactuate_test.go` (new), scenarios, each run against **both** verbs so
neither can drift:

- Nil seam → frame consumed, zero replies, `KnownConversation` never consulted, and an array-valued
  `conversation_id` proves no payload decode was attempted.
- Non-interactive connection → same, with the seam also never consulted.
- Malformed payload (type mismatch, and a non-object body) → exactly one `protocol.malformed`,
  non-retryable; membership and seam both at zero calls.
- Unknown conversation → exactly one `conversation.not_found`, non-retryable; seam at zero calls.
- Seam refusal → exactly one `mcp_actuation.refused`, non-retryable, and the reply body contains
  neither a server name nor any string from a **poisoned** payload the refusing seam also returns
  (carrying the second trap forward: a refusal arm returning a zero payload cannot prove the
  handler ignores the payload on `false`).
- Accepted → exactly one `mcp_status`, `in_reply_to` = request id, `event_id` nil, payload equal to
  the seam's result where the result's `ConversationID` **differs from the requested id** (the
  first trap: equal values stay green when the handler overwrites the daemon-authored answer with
  the remote-authored key); a second open connection receives nothing.
- Toggle only: `enabled` true and false each reach the seam verbatim, and an omitted key reaches it
  as `false`.
- Blocked seam → both halves per the concurrency overview: a second connection is answered while
  the first is parked, and the first connection's reply decrypts after release.
- Interception precedes routing → a sentinel `dispatch.Handler` registered under each type never
  fires.
- Logs → a buffer logger over a success, a malformed frame, an unknown conversation and a refusal;
  assert absence of every remote-authored sentinel **and** of the decoder-error attribute/text
  (the known gap the test-surface overview names).

Verification gate: `go test -race ./internal/protocol/... ./internal/relay/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Documentation handoff

Pending for the documentation stage — **not** done in this ticket.

- `docs/protocol-mobile.md`, beside the existing `mcp_status` and on-demand-request sections:
  document both verbs' payloads; that the answer to an accepted actuation is a fresh `mcp_status`
  correlated by `in_reply_to` rather than a separate ack; and the single reject code
  `mcp_actuation.refused` with its non-retryable flag.
- Record in the same section that the declaration ships ahead of the gate, and that nothing is
  wired behind `MCPActuator` until #2420 lands.

## Open questions

1. **Is `Enabled` a `bool` or a `*bool`?** Resolved in Design above: plain `bool`. Revisit only if
   #2420's gate turns out to need "client did not say" as a distinct state — it does not today,
   since `SetMCPServerEnabled` takes a bare bool.
2. **Does the merged reject need its own message per verb?** Provisionally no — one static message
   for both. If implementation shows a client cannot correlate without it, `in_reply_to` already
   carries the correlation, so the answer stays no.
3. **Does reusing `forwardMCPStatusReply` mislead on the actuation path?** Its only log fires on an
   unreachable marshal failure. Confirm during implementation that no reachable arm emits a
   status-named event for an actuation; if one does, the fix is a verb-neutral lane, which would
   touch a sixth production file and is therefore called out here rather than taken silently.

## Sizing

Boundary re-count against this plan:

| Limit | Boundary | This ticket |
|---|---|---|
| Production source files created or modified | ≤ 5 | 5 — `internal/protocol/interactive.go`, `internal/protocol/codes.go`, `internal/relay/v2session.go`, `internal/relay/v2session_seams.go`, `internal/relay/v2session_mcpactuate.go` (new) |
| Total written work | ≤ 800 | ~1,000 — **over** |
| New exported types or interfaces | ≤ 5 | 3 — `MCPReconnectPayload`, `MCPTogglePayload`, `MCPActuator` |
| Consumer call sites needing simultaneous update | ≤ 10 | 0 — every change is additive; the seam is nil at every existing construction site |
| Acceptance criteria | ≤ 5 | 5 |
| Distinct error/reject branches | ≤ 10 | 3 |

One line is exceeded: total written work, by about a quarter, which is the overage the refiner
already measured and published on the estimate line.

**Not split, and the reason is structural rather than a judgement about the work.** This ticket is
a grandchild of #2202 (parent #2277), confirmed against the GitHub parent chain, so the
split-depth cap applies: `needs-human:sizing` is the marker, already on the issue, and the ticket
gets built as it stands. Had a split been available, the only seam is protocol-declarations vs
relay-interception — and the declarations half would have exactly one consumer, this ticket's other
half, which the floor rule says is part of it rather than a ticket of its own.

**File-overlap check (§ A2) run and clear.** `git fetch origin --prune` then a scan of every
`origin/feature/<n>` branch found one nominal hit: `origin/feature/449` touches
`internal/protocol/codes.go` and `internal/relay/v2session.go`. Issue #449 closed 2026-05-17 and
its branch tip dates from the same day, so it is a stale post-merge remnant rather than in-flight
work; no blocker set, no rework routed.

## Security review

**Verdict:** PASS (second pass — the first returned FAIL on the `ServerName` finding below, which
the Design section was revised to address before this section was written or the plan committed).

**Findings:**

- [Trust boundaries] **MUST FIX — addressed in this plan before commit.** The first draft said the
  relay "never logs, returns or path-joins" `ConversationID` and `ServerName`, which states the
  relay's own discipline and says nothing to the implementer below the seam. The two strings are
  not equivalent: `ConversationID` reaches `MCPActuator` only after `KnownConversation`, so it
  names a conversation the daemon's own registry holds, while `ServerName` passes no check
  anywhere. A seam documented without that asymmetry hands #2420's implementer an unvalidated
  string with no stated contract — the `Interrupter` / `SessionStarter` hazard exactly. Seam
  property 5 now carries the obligation: shape-check before use, never a path component, never
  shell-interpreted, length bounded only transitively by the AEAD frame cap.
- [Trust boundaries] SHOULD FIX — `MCPActuator` is an interface, so a typed-nil assignment
  (`cfg.MCPActuator = (*impl)(nil)`) reads as non-nil at `dispatchAppFrame`'s gate, admits the
  frame, and calls a method on a nil pointer; `internal/relay` contains no `recover()`, so the
  daemon dies. Fail-closed for authorization — nothing reaches a child — but a remote-triggerable
  crash once a wiring bug exists. Recorded as seam property 6. Not escalated to a build-level
  guard: no premature or nil wiring has been observed on any seam on this struct, and the
  identical shape has shipped on `QuestionResolver`, `ModalResolver` and `AttachmentIntake`.
- [Tokens, secrets, credentials] No findings — this path mints, stores and compares nothing.
  `s.device` crosses the seam as a pointer to the record `handleNoiseInit` already bound after the
  presented token validated; the handler reads no field of it, and no reply derived from it
  reaches the wire. The merged reject's body is a compile-time constant.
- [File operations] No findings for this slice — the relay performs no filesystem operation on
  this path. The obligation that keeps it that way below the seam is the first finding above.
- [Subprocess / external command] No findings — nothing is executed. Below the seam #2418 writes to
  an already-running child's stdin, and `marshalMCPReconnectEnvelope` / `marshalMCPToggleEnvelope`
  `json.Marshal` the server name into a struct rather than concatenating it, so the child's
  control stream is not injectable through this field. Checked rather than assumed.
- [Cryptographic primitives] No findings — no primitive is introduced. The one crypto-adjacent
  property is that the worker never touches a `CipherState`: every reply crosses `forwardToRun` and
  is sealed on `Run`, so no concurrent `Encrypt` can burn or gap a send nonce (the #874 failure, a
  4421 teardown of a live session).
- [Network & I/O] No findings — frame size is capped upstream by the AEAD transport, the
  per-connection `appFrames` FIFO bound and its 4421 overflow teardown are inherited unchanged, and
  a device that parks the seam parks only its own connection's worker.
- [Error messages, logs, telemetry] No findings — the merged code, the `retryable: false` on every
  refusal, the static per-code message and the never-log list are the ticket's substance rather
  than an addition to it. The `reason` argument reaches the local log record only; the wire message
  is always `reject.message`.
- [Concurrency] No findings — no goroutine is spawned, no lock is taken. The
  `KnownConversation`-then-seam window is a genuine TOCTOU (a conversation can be torn down between
  them) and is fail-safe: the seam's own lookup then fails and the request earns the same merged
  refusal. Reading `s.device` off `Run` follows `handleMintPairing`'s precedent rather than opening
  a new one.
- [Threat model] `docs/protocol-mobile.md` § Security model. Threat 1 (prompt injection) —
  unchanged; neither verb carries prompt text. Threat 3 (relay operator MITM) — the answer is
  AEAD-sealed and unicast to the asking connection, with no `event_id`, no broadcast and no event-
  ring append, so a misrouted frame fails AEAD rather than reaching another phone. Threat 4 (token
  leak via phone, mitigated by per-device revocation) — the threat this verb is most exposed to,
  and the reason `*devices.Device` crosses the seam at all; today a leaked token yields nothing
  here because the seam is nil at every construction site.
- [Error messages / threat model] OUT OF SCOPE → #2420 — **timing distinguishes what the code
  merges.** A gate denial and a "no live child" refusal return immediately, while a failed
  actuation returns only after the child round trip resolves, so a device that times its replies
  can infer which refusal it earned and thereby learn whether it is privileged. The relay adds no
  timing of its own and cannot close this from here; the gate slice owns the decision, along with
  whether an actuation wait needs a bound of its own (#2418's `actuateMCP` takes a ctx and the
  relay passes the worker's, which today is cancelled only at manager shutdown).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14
