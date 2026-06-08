# Spec #626 — Capability negotiation on the v2 Noise handshake

**Part of EPIC #596** (Phase 2 structured streaming). Implements the daemon-side
trust decision that #607 deferred: intersect the phone's advertised capabilities
with the daemon's authoritative supported set, echo the intersection in
`hello_ack`, record the negotiated `interactive` flag per conn, and expose a
capability-aware open-conn enumeration the downstream structured-stream fan-out
will route on.

**Security-sensitive.** This is the trust-boundary decision on the
internet-exposed mobile handshake. The security-review pass is at the end of this
spec (verdict: **PASS**).

**Single production file:** `internal/relay/v2session.go`. No wire-type change, no
emitter change, no new goroutine, no new lock. Purely additive — same
ship-the-primitive-ahead-of-its-consumer pattern as #571/#607.

---

## Files to read first

- `internal/relay/v2session.go:625-809` — `handleNoiseInit`. The hello_ack is
  built at **712-732** (the literal to extend with `Capabilities`); the token
  check is at **772**; the token-OK tail (`s.device = &device` → `s.state =
  V2StateOpen`) is at **798-808** (where the `interactive` flag is recorded,
  *before* line 807). **Extract:** the ack is sealed via `WriteResp` *before* the
  token check, so the intersection must be computed before line 712 and is echoed
  on every handshake; the flag is recorded only on the authenticated token-OK
  branch.
- `internal/relay/v2session.go:156-223` — `V2Session` struct. **Extract:** the
  set-once / single-owner-goroutine field discipline (`device`, `peerStatic`) the
  new `interactive bool` mirrors; re-key (`handleRekeyInit`) preserves these
  fields by never touching them.
- `internal/relay/v2session.go:116-123, 391-398, 444-466, 1599-1634` —
  `snapshotReq`, the `m.snapshot` field, the `Run` select arms, `ActiveConnIDs`,
  `handleActiveConnIDs`. **Extract:** the snapshot funnel this slice widens from
  `[]string` to `[]ActiveConn`; `ActiveConnIDs` becomes a thin projection over the
  richer reply.
- `internal/protocol/handshake.go:40-65` — `HelloClientPayload.Capabilities`
  (phone's advertisement, input) and `HelloAckPayload.Capabilities`
  (`omitempty`, the echo, output) + `CapabilityInteractive = "interactive"`.
  **Extract:** both fields + the constant already exist (#607); this slice only
  *populates* the ack field and *reads* the client field. The `omitempty` on the
  ack field is the AC#5 byte-stability lever (nil/empty → key absent).
- `internal/relay/v2session_test.go:116-140` — `buildHelloEarlyData(t, token)`
  builds a **no-capabilities** hello (5 callers — **do not change its
  signature**; add a capabilities-bearing variant instead). `:707-744` —
  `driveToOpen` (30 callers — **do not change its signature**) **discards** the
  hello_ack early-data: `_, initSend, initRecv, err := initiator.ReadResp(...)`.
  Capability tests must capture that early-data to decode `HelloAckPayload`.
- `internal/noise/noise.go:194` — `Initiator.ReadResp(respMsg) (earlyData []byte,
  send, recv *CipherState, err error)`. **Extract:** the hello_ack envelope is
  `earlyData`; a capability test recovers it here, then unmarshals
  `Envelope`→`HelloAckPayload` to assert `.Capabilities`.
- `cmd/pyry/assistant_turn_v2.go:14-22, 139-161` — the `v2Broadcaster` interface
  (`ActiveConnIDs(ctx) []string` + `Push`) and the #589 coarse fan-out loop.
  **Extract:** this consumer must stay compiling untouched (AC#5) — the new
  `ActiveConns` is additive; `ActiveConnIDs` keeps its `[]string` signature.
- `docs/knowledge/codebase/607.md` — the wire vocabulary this consumes, and the
  explicit "intersection/enforcement is the consumer's job" deferral this closes.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md`
  § "Wire-protocol extension (v2-additive, capability-negotiated)" — the
  authoritative negotiation contract.

---

## Context

ADR 025 § Phase 2 chose v2-additive + capability negotiation. #607 landed the wire
vocabulary (`CapabilityInteractive`, the two `Capabilities []string` fields) but
deferred the daemon-side trust decision. Today `handleNoiseInit` **decodes**
`helloPayload.Capabilities` and then **ignores** it: the `hello_ack` is built
without echoing anything, the `V2Session` records no capability state, and
`ActiveConnIDs` returns all open conns with no capability filter.

This slice closes that trust boundary. It is the enforcement half of a
deliberately split design: #607 = vocabulary (no trust), #626 = trust decision.
The structured-stream fan-out (a later #596 child, replacing #589's coarse
`message` broadcast) will call the new capability-aware enumeration to route
interactive events only to conns the daemon decided are interactive.

---

## Design

All changes are in `internal/relay/v2session.go`, on the manager's single `Run`
dispatch goroutine — the package's established no-mutex, single-owner concurrency
model. Five pieces.

### 1. The authoritative supported set + the intersection function

The supported set is the daemon's own constant — **never** a mirror of the
phone's claims. The intersection iterates the *supported* set and filters by the
*advertised* set, so the result is a subset of supported **by construction**: a
spoofed/unsupported advertisement cannot appear in the output, duplicates
collapse, and order is the daemon's echo order.

```go
// supportedV2Capabilities is the daemon's authoritative capability set. The
// negotiation output is built from THESE entries only — never from the phone's
// advertised set — so an unsupported/spoofed advertisement can never be echoed
// or flagged.
var supportedV2Capabilities = []string{protocol.CapabilityInteractive}

// negotiateCapabilities returns advertised ∩ supportedV2Capabilities, in
// supported-set order. Iterates supported (not advertised): dedups, drops the
// unsupported, and yields nil for advertise-nothing/only-unsupported (omitempty
// then drops the ack key, preserving v1 byte-stability).
func negotiateCapabilities(advertised []string) []string
```

- Body is a single loop over `supportedV2Capabilities` appending each entry that
  `slices.Contains(advertised, …)` (add the `slices` import; the test file
  already uses it). Avoid shadowing the `cap` builtin — name the loop var
  `name`/`c`.
- Pure function, no receiver — directly table-testable for every negotiation case
  (this is where the AC#2/#3 matrix lives, cheaply).

### 2. Compute the intersection + echo it in `hello_ack`

In `handleNoiseInit`, after `helloPayload` is decoded (currently line 706) and
**before** the ack literal (712):

```go
negotiated := negotiateCapabilities(helloPayload.Capabilities)
```

Add the field to the existing ack literal:

```go
ackPayload, err := json.Marshal(protocol.HelloAckPayload{
    ProtocolVersion: "v2",
    ServerID:        m.cfg.ServerID,
    ConnID:          s.connID,
    Capabilities:    negotiated, // omitempty: nil/empty → key absent (AC#5)
})
```

The ack is sealed via `WriteResp` before the token check, so it is built on every
handshake. On a token-fail handshake the ack rides the `noise_resp` to a
cryptographically-authenticated-but-unauthorized peer that is then closed at 4401
and removed from the map — see § Security review for why this echo grants
nothing. `negotiated` stays in scope (one function) for the flag-record below.

### 3. Record the `interactive` flag on the session (token-OK only, before open)

Add one field to `V2Session` (alongside `device`/`peerStatic`), with the same
set-once / single-owner-goroutine doc discipline:

```go
// interactive is the negotiated interactive-capability decision (advertised ∩
// supported contained CapabilityInteractive). Set exactly once in
// handleNoiseInit's token-OK path BEFORE s.state advances to V2StateOpen; the
// zero value (false) is the fail-closed default for every other path. Re-key
// (handleRekeyInit) preserves it by never touching it, like device/peerStatic.
// Read by handleActiveConns on the same dispatch goroutine — no lock/atomic.
interactive bool
```

In the token-OK tail (798-808), insert between `s.device = &device` (806) and
`s.state = V2StateOpen` (807):

```go
s.interactive = slices.Contains(negotiated, protocol.CapabilityInteractive)
```

Single source of truth: the flag is derived from the same `negotiated` slice the
ack echoed, so ack and flag can never disagree. Recording before `V2StateOpen`
satisfies AC#1's ordering requirement (the enumeration filters on `V2StateOpen`,
so the flag is always set by the time a conn becomes enumerable).

### 4. The capability-aware enumeration — widen the existing snapshot funnel

Add one exported tuple type:

```go
// ActiveConn is one open v2 session in the capability-aware enumeration: its
// routing conn-id and its negotiated interactive flag. Holds only non-secret
// routing/decision data — never a *V2Session, key, or plaintext.
type ActiveConn struct {
    ConnID      string
    Interactive bool
}
```

**One funnel, not two.** The existing `m.snapshot` funnel is widened to carry the
richer reply; `ActiveConnIDs` becomes a thin projection. This is the elegant
choice over a parallel funnel: `ActiveConns` and `ActiveConnIDs` are the *same*
map read at two granularities, not two distinct operations (contrast #571's push
vs snapshot, which genuinely differ). No test references the funnel internals
(`snapshotReq`/`handleActiveConnIDs`) by name — only the public `ActiveConnIDs(ctx)
[]string`, whose signature is preserved — so this is safe.

Changes:
- `snapshotReq.reply` type: `chan []string` → `chan []ActiveConn`.
- `handleActiveConnIDs` → `handleActiveConns() []ActiveConn`: same loop, same
  `V2StateOpen` gate, append `ActiveConn{ConnID: connID, Interactive: s.interactive}`.
- `Run` select arm: `req.reply <- m.handleActiveConns()`.
- New public method `ActiveConns(ctx context.Context) []ActiveConn` — structural
  twin of today's `ActiveConnIDs` (funnel send + reply receive, both with the
  `ctx.Done` escape arm; `nil` on cancel or post-`Run`-exit).
- `ActiveConnIDs(ctx) []string` reimplemented as a projection over `ActiveConns`:
  `nil` in → `nil` out (preserves the cancel contract); otherwise map each
  `ActiveConn` to its `ConnID`. Signature and observable contract (nil-on-cancel,
  non-nil-empty-on-empty, unordered) unchanged.

### Data flow

```
phone hello.capabilities ──┐
                           ▼
        negotiateCapabilities(advertised)  =  advertised ∩ supportedV2Capabilities
                           │
            ┌──────────────┴───────────────┐
            ▼                               ▼
   hello_ack.capabilities          token-OK?  s.interactive = contains(…, interactive)
   (echoed every handshake,                   (set once, before V2StateOpen)
    omitempty)                                          │
                                                        ▼
                              ActiveConns(ctx) → [{conn_id, interactive}, …]  (V2StateOpen only)
                                       │
                                       ├── ActiveConnIDs(ctx) []string  (projection; #589 consumer)
                                       └── future #596 fan-out: filter Interactive == true/false
```

---

## Concurrency model

No change to the model. `handleNoiseInit` (writes `s.interactive`) and
`handleActiveConns` (reads it) both run on the single `Run` dispatch goroutine,
serialised by `Run`'s `select` — the same property that makes `s.state`,
`s.device`, and the re-key CipherState swap safe without a lock. `ActiveConns`
funnels through `m.snapshot` exactly as `ActiveConnIDs` does today; the widened
reply type is the only difference. No new channel, no new goroutine, no atomic.

---

## Error handling

No new failure modes. The intersection is a pure in-memory slice operation that
cannot fail. The ack-marshal / `WriteResp` / token-validation error paths are
unchanged (the additive `Capabilities` field cannot make a well-typed
`HelloAckPayload` fail to marshal). Fail-closed posture throughout: any path that
does not reach the token-OK branch leaves `s.interactive` at its `false`
zero-value, and the enumeration's `V2StateOpen` filter excludes every
non-authenticated session regardless of the flag.

---

## Testing strategy

stdlib `testing` only, `-race`-clean, reuse the existing harness. **Do not change
the signatures of `driveToOpen` (30 callers) or `buildHelloEarlyData` (5
callers)** — add a capabilities-bearing variant and capture the ack early-data in
the new tests.

**Unit — `negotiateCapabilities` (table-driven, the AC#2/#3 matrix):**
- `[interactive]` → `[interactive]`.
- `[interactive, "snapshot-unknown"]` → `[interactive]` (drop unsupported).
- `["snapshot-unknown"]` → `nil` (only-unsupported / spoof).
- `nil` and `[]` → `nil` (advertise-nothing).
- `[interactive, interactive]` → `[interactive]` (dedup, single entry).

**Handshake-level (drive to open, capture & decode the hello_ack early-data):**
- Advertise `[interactive]` + valid token → decode `HelloAckPayload`, assert
  `Capabilities == [interactive]`; `ActiveConns` reports the conn with
  `Interactive: true`.
- Advertise nothing (today's `buildHelloEarlyData`) → ack has **no** `capabilities`
  key (assert `len(Capabilities) == 0` / byte-stable); `ActiveConns` reports
  `Interactive: false` (AC#5).
- Advertise `[interactive, "god-mode"]` (spoof) → ack echoes only `[interactive]`,
  `"god-mode"` absent; `Interactive: true` for interactive only (AC#3).
- Advertise `["god-mode"]` only → ack has no capabilities, `Interactive: false`
  (AC#3).

**Enumeration / regression:**
- Mixed open conns (one interactive, one not) → `ActiveConns` reports the correct
  flag per conn; `ActiveConnIDs` still returns **all** open conn-ids (projection
  unchanged).
- **Security:** a phone that advertises `[interactive]` but fails the token never
  appears in `ActiveConns` (closed at 4401, not `V2StateOpen`) — the flag is never
  observable for an unauthenticated peer.
- The existing `TestV2Session_ActiveConnIDs_*` suite passes unchanged (the
  `[]string` contract, including nil-on-cancel and empty-manager, is preserved).

---

## Security review

**Trust boundary.** Internet-exposed: the phone's `hello.payload.capabilities` is
fully attacker-controlled (`internal/relay/v2session.go:697-706`, decoded from IK
early-data). The daemon's job is to decide what the phone is *granted*. "Granted
interactive" = the session reaches `V2StateOpen` **and** `s.interactive == true`
**and** is therefore returned by `ActiveConns` for the future fan-out to push
interactive events to.

**Threat 1 — capability spoofing (advertise an unsupported capability).** Defeated
by construction: `negotiateCapabilities` builds its output by iterating
`supportedV2Capabilities` and filtering by the advertised set, so the output is a
subset of supported. An advertised `"god-mode"` is never a candidate — it cannot
appear in the ack, and (since the flag is `slices.Contains(negotiated, …)`) cannot
flag the session. This is the AC#3 / ticket-focus property. Deterministic, not
stochastic — enforced by the loop shape, pinned by the unit + spoof handshake
tests.

**Threat 2 — granting an unauthenticated peer.** The `interactive` flag is written
**only** in the token-OK branch, after `Devices.Validate` succeeds, before
`V2StateOpen`. Every other path (token fail → 4401 + map delete; handshake/state
reject → close) leaves the flag at its `false` zero-value. Belt-and-suspenders of
*different fabric*: even if the flag were mis-set, `handleActiveConns` filters on
`V2StateOpen`, so a non-open session is never enumerated — two independent
deterministic gates, the same gate `handlePush`/`ActiveConnIDs` already enforce.

**Threat 3 — the ack echo on the token-fail path leaks/grants.** On token-fail the
`noise_resp` (carrying the ack with `negotiated`) is sent, then the session is
closed at 4401 and deleted from `m.sessions`. The peer is cryptographically
authenticated (it completed IK against the daemon's static key) but unauthorized
(no valid device token). The echo (a) grants nothing — the session never opens,
never gets flagged, is never enumerated or pushed to; and (b) leaks nothing —
`CapabilityInteractive` is public protocol vocabulary, not secret. Restructuring
to build the ack after the token check would break the pinned "state →
HandshakeComplete before token validation" invariant for zero security gain.
**Accepted, non-issue.**

**Threat 4 — fail-open default.** Zero-value of `interactive` is `false`; a phone
that omits the field, sends `null`/`[]`, or advertises only unsupported
capabilities yields `negotiated == nil` → flag `false`. Default-deny. The
`omitempty` ack field also keeps the absent-key v1 shape, so a v1/no-capability
phone is byte-identical to today (AC#5).

**Threat 5 — DoS via a huge advertised array.** Bounded: the hello rides inside
the ≤65519-byte AEAD-sealed envelope, and the intersection is O(len(advertised))
linear scans (supported set size 1) on the dispatch goroutine — no amplification,
no allocation blowup.

**Logging.** No new secret-bearing field. `CapabilityInteractive`/the negotiated
set are non-secret; the existing no-token/no-key/no-payload log discipline is
unaffected (this slice adds no log lines that carry the advertised set, and need
not).

**Verdict: PASS.** The grant is built from the daemon's authoritative set
(spoofing impossible by construction), gated on authentication (token-OK only) and
on `V2StateOpen` (two independent deterministic gates), and fail-closed by
default. No revision required.

---

## Open questions

- **`ActiveConns` naming / future capabilities.** The enumeration exposes a single
  `Interactive bool`, the right shape while `supportedV2Capabilities` has one
  member (YAGNI per CLAUDE.md "don't add abstraction it doesn't need yet"). When a
  second capability lands, that ticket either adds a sibling bool or migrates
  `ActiveConn` to a capability set — a deliberate, separately-reviewed change, not
  pre-built here.
- **`docs/protocol-mobile.md`.** No wire-format change, so no spec amendment is
  required for correctness; #607's `### Capability negotiation (v2)` note already
  describes the contract. Refreshing that note's "enforcement is the consumer's
  (#608)" line to "implemented in #626" is a documentation-phase touch, **not** a
  developer AC (keeps the dev worktree to code + tests + this spec).

---

## Out of scope (deferred to siblings)

- The structured-stream fan-out that *consumes* `ActiveConns` to route interactive
  events (a later #596 child, replacing #589's coarse `message` broadcast).
- Any new wire type, emitter, or `Push` change (#607 / #571 own those).
- Per-device permission gating of *answering* modals (Phase 3 / #597) — orthogonal
  to capability negotiation.
