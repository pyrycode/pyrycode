# Spec #707 — `interrupt` wire type + Esc routing

**Ticket:** feat(protocol): interrupt wire type + Esc routing
**Size:** S (held; no split — design surface matched the body)
**Labels:** `security-sensitive` (security review at the end of this spec)
**Epic:** #597 Phase 3 (interactive modals, permissions, queued backlog)

## Files to read first

The developer's turn-1 data load. Read these before writing code.

- `internal/protocol/codes.go:122-160` — the `TypeResync` / `TypeSessionTransition` single-const-block pattern and the **"MUST NOT be added to v1TypeSet"** doc convention. `TypeInterrupt` is a new block in this same style, adjacent to the modal cluster (`:162-193`).
- `internal/protocol/compat_test.go:101-181` — the v1/v2 partition drift detector. Three edits land here: a `TypeInterrupt: true` entry in `v2OnlyTypes` (`:111`), a `TypeInterrupt` entry in `TestTypeConstants_V1V2Partition`'s `all` list (`:140-165`), and an `{"interrupt-rejected", TypeInterrupt, false, ErrUnknownType}` case in `TestIsV1Compatible` (`:27-67`). Do **not** touch `TestV1TypeSet_CoversAllExportedTypeConstants` (`:78-99`) — its `len(all), 16` count enumerates v1 types only; `interrupt` is v2-only.
- `internal/protocol/envelope.go:111-135` — `v1TypeSet`. **Stays unchanged.** The drift detector forces the v2OnlyTypes edit; production `v1TypeSet` never gains `interrupt`.
- `internal/turnevent/permission.go:40-66` — the `Inbound` sealed sum type, its unexported `isInbound()` marker, `PermissionResponse`, and the `var _ Inbound = …` compile-time assertion block. The comment at `:43-44` literally reserves "the deferred inbound-commands ticket adds Prompt / Cancel / DropQueued here." This ticket adds `Cancel`.
- `internal/turnevent/permission_test.go:74-81` — `TestPermissionResponse_IsInbound`'s `var _ Inbound = …` compile-time-membership idiom; mirror it for `Cancel`.
- `internal/relay/v2session.go:1294-1350` — `dispatchAppFrame`: the v2 control-envelope discriminator switch. The `interrupt` intercept case slots in beside `TypeModalCancel`.
- `internal/relay/v2session.go:1520-1552` — `handleModalCancel`: the closest handler template (nil-seam guard, never-echo discipline, fire-and-forget, no reply). `handleInterrupt` mirrors its *shape* but is simpler (no payload decode, no broadcast) and adds the interactive gate.
- `internal/relay/v2session.go:367-414` — `ScreenSnapshotter` / `ModalResolver`: the consumer-side-interface pattern (`internal/relay` declares the seam so it imports neither `internal/supervisor` nor tui-driver). `Interrupter` is a new sibling here.
- `internal/relay/v2session.go:416-489` — `V2SessionConfig`: where the optional `Interrupter` field is added (mirror the `Snapshotter` / `ModalResolver` optional-seam doc style + nil-behaviour note).
- `internal/relay/v2session.go:248-269` — `V2Session.interactive`: the negotiated capability flag the gate reads (set fail-closed in `handleNoiseInit`'s token-OK path, Run-goroutine-owned, no lock).
- `internal/supervisor/modal.go:64` — `func (s *Supervisor) SendEsc() error`. The sealed keystroke surface (#726). `*supervisor.Supervisor` already satisfies `Interrupter` with **zero new supervisor code**.
- `cmd/pyry/relay.go:305-334` — the production `V2SessionConfig{…}` literal. `sup` (`*supervisor.Supervisor`) is already in scope and already passed as `Snapshotter` and into `newModalResolverV2`; add one line: `Interrupter: sup`.
- `internal/relay/v2session_modal_test.go:94-136` — the test idioms the new test reuses verbatim: `sealAppFrameConn(t, cs, connID, env)` (seal an app envelope into a routing frame) and `openModalConn(t, mgr, frames, rec, respPub, connID, caps)` (drive a handshake; pass `[]string{protocol.CapabilityInteractive}` for the interactive conn, `nil` for non-interactive — see `:266-268`).
- `internal/relay/v2session_test.go:98` — `startManager(t, V2SessionConfig{…})` → `(mgr, stop)`. The new test builds its config with an `Interrupter: &fakeInterrupter{}`.
- `docs/protocol-mobile.md:402-441` — the Application message types table (add the `interrupt` row) and `:618-687` the Modal/Queue sections (model an `### Interrupt (v2)` subsection on `#### dequeue_message` at `:680`, an ungated/payload-light inbound control).

## Context

Phase 3 of epic #597 adds the **remote interrupt** control: the paired phone's equivalent of pressing **Esc** at the local terminal. A phone sends a bare `interrupt` frame; the daemon maps it to the internal neutral `Cancel` command and routes it to the supervised claude as a single Esc keystroke — claude's own interrupt.

Every dependency already exists in `main`:
- The keystroke surface is sealed end-to-end: `supervisor.SendEsc()` (#726) is already wired through `cmd/pyry`'s `modalKeystroker` seam and consumed by `modal_cancel` since #727. **This ticket does not touch tui-driver or supervisor** — it reuses `SendEsc`.
- The neutral `Cancel` slot is the already-reserved next member of `turnevent.Inbound` (`permission.go:43-44`). It is adapter-neutral: epic #600 maps ACP `session/cancel` onto the same `Cancel` shape later.
- The inbound v2 control-frame interception pattern (`dispatchAppFrame` before `dispatch.Route`) is established by `rekey_request`, `request_snapshot`, `modal_cancel`, `modal_answer`.

One thing here is genuinely **new**: the **inbound `interactive` capability gate**. Existing inbound control frames gate *outbound* emission on `s.interactive` (e.g. `broadcastModalDismissed`) but do not gate the inbound frame itself — `modal_cancel` resolves a one-time nonce, `dequeue_message` (#723) is deliberately ungated, `modal_answer` is gated by the per-device answer gate (#702), not by capability. `interrupt` is the first inbound frame whose authorization *is* the `interactive` capability. Interrupting your own paired session is a normal paired-phone action, not a privileged tool-permission decision, so it is **capability-gated on `interactive` and exempt from the permission gate** (#702).

## Design

Four production files, three packages, plus the protocol doc and one test file. All changes are additive — no signature changes, no consumer cascade.

### 1. Wire vocabulary — `internal/protocol/codes.go`

Add one v2-only control const in its own block adjacent to the modal cluster:

```go
// TypeInterrupt = "interrupt"  — phone → binary, inbound v2 control
//   intercepted pre-dispatch.Route; carries NO payload; interactive-gated;
//   exempt from the permission gate (#702). Maps to the neutral
//   turnevent.Cancel; #600 maps ACP session/cancel onto the same shape.
```

The doc comment mirrors the modal cluster's two-paragraph form: (a) what it is and its trust posture, (b) the standing **"MUST NOT be added to v1TypeSet … the drift detector in compat_test.go partitions Type\* constants"** boilerplate. Note it is unlike the modal frames in three ways: no `conversation_id`, no `modal_id` nonce, no `answer_token` — a bare control frame.

### 2. Partition enforcement — `internal/protocol/compat_test.go` (test only)

- `v2OnlyTypes`: add `TypeInterrupt: true`.
- `TestTypeConstants_V1V2Partition`'s `all` slice: add `TypeInterrupt` (under a `// v2 interrupt control` comment). The `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` assertion then auto-verifies the disjoint partition.
- `TestIsV1Compatible`'s `cases`: add `{"interrupt-rejected", TypeInterrupt, false, ErrUnknownType}` — an old phone never sees `interrupt`.

`v1TypeSet` in `envelope.go` is **not** edited; `TestV1TypeSet_CoversAllExportedTypeConstants` (`len(all), 16`) is **not** edited.

### 3. Neutral command — `internal/turnevent/permission.go`

Add `Cancel` to the `Inbound` sum type, beside `PermissionResponse`:

```go
// Cancel is an inbound command: stop the current turn (the neutral form of a
// remote Esc / ACP session/cancel, #600). Fieldless — the daemon has one live
// turn context, so no correlation id is needed yet.
type Cancel struct{}

func (Cancel) isInbound() {}

var _ Inbound = Cancel{}   // alongside the existing _ Inbound = PermissionResponse{}
```

Contract notes for the developer:
- Keep the marker in `permission.go` (do not relocate to a new file — `Cancel` joins its sibling member; the relocation hinted at `permission.go:44` is optional churn this slice declines).
- **`Cancel` is declared vocabulary in this slice; the mobile `interrupt` path routes to Esc directly via the relay seam (§4), not through a `Cancel` value.** This mirrors the established precedent: the mobile `modal_cancel` frame routes to `ModalResolver.ResolveCancel`, it does not construct a `turnevent.PermissionResponse`. The mobile-wire → neutral-`Cancel` translator is future work (the ACP adapter, #600); `Cancel` exists now so #600 has its target.

### 4. Routing seam + handler — `internal/relay/v2session.go`

**Consumer-side interface** (new sibling of `ScreenSnapshotter` / `ModalResolver`, ~`:374`):

```go
// Interrupter delivers a single Esc to the supervised claude — the remote
// equivalent of a local Esc, claude's own interrupt. *supervisor.Supervisor
// satisfies it via SendEsc (#726). Declared here (consumer side) so
// internal/relay imports neither internal/supervisor nor tui-driver.
type Interrupter interface{ SendEsc() error }
```

Method name is the existing sealed surface `SendEsc` (so the supervisor satisfies it with no new method); the interface is named for its relay-domain role, matching `ScreenSnapshotter.ScreenSnapshot` / `ModalResolver.Resolve*`.

**Config field** (optional seam, in `V2SessionConfig`, mirror the `Snapshotter` doc style):

```go
// Interrupter routes an inbound interactive `interrupt` control frame to the
// supervised claude as one Esc. Optional: nil ⇒ interrupt is inert (no Esc) —
// the foreground / unwired case. Production wires *supervisor.Supervisor.
Interrupter Interrupter
```

**Intercept case** in `dispatchAppFrame`'s switch (beside `TypeModalCancel`):

```go
case protocol.TypeInterrupt:
    m.handleInterrupt(s)
    return
```

**Handler** — the only new logic:

```go
// handleInterrupt routes an inbound interrupt to the supervised claude as one
// Esc, gated on the conn's interactive capability. No payload, no reply, no
// broadcast. Runs on the manager's single Run dispatch goroutine.
func (m *V2SessionManager) handleInterrupt(s *V2Session)
```

Behaviour, in order (the order is load-bearing — capability gate first):
1. **`if !s.interactive` → return** (AC #2: a non-interactive conn is inert; no Esc). This is the new inbound capability gate. A one-line check, **not** a reusable inbound-gate abstraction — `interrupt` is its only consumer (dequeue is ungated, modal_answer uses the per-device gate, modal_cancel uses a nonce), so a shared gate would be a one-consumer abstraction (YAGNI). Document the choice in the handler comment.
2. **`if m.cfg.Interrupter == nil`** → debug-log `v2.interrupt.inert` and return (foreground / pre-wire; mirrors `handleModalCancel`'s nil-resolver guard).
3. **`m.cfg.Interrupter.SendEsc()`** — best-effort. An error (no live session / teardown) is `Warn`-logged with the transport sentinel only (event `v2.interrupt.keystroke_err`, `conn_id`) and tolerated — there is nothing to roll back. **Never log payload bytes** (there are none) or the rendered screen.

Signature takes only `s` (no `ctx`, no `env`): the frame carries no payload to decode and does no cancellable work. This is an intentional, documented deviation from the `(ctx, s, env)` sibling handlers.

### 5. Production wiring — `cmd/pyry/relay.go`

Add one field to the `V2SessionConfig{…}` literal (`:305-334`), beside `ModalResolver`:

```go
// Inbound interrupt seam (#707): an interactive `interrupt` frame routes one
// Esc through the sealed supervisor keystroke surface. sup
// (*supervisor.Supervisor) satisfies Interrupter via SendEsc (#726).
Interrupter: sup,
```

`sup` is already in scope (passed as `Snapshotter` and into `newModalResolverV2`). No new import, no new construction.

### 6. Protocol doc — `docs/protocol-mobile.md`

- **Application message types table** (`:440`, after the `dequeue_message` row): add
  `| **`interrupt`** | phone → binary | no | **New in v2.** Inbound control — phone interrupts the running turn (remote Esc). Interactive-capability-gated; exempt from the permission gate. See [Interrupt](#interrupt-v2). |`
- **New `### Interrupt (v2)` subsection** (model on `#### dequeue_message` at `:680`): direction phone → binary, inbound v2 control, intercepted before `dispatch.Route` (no `dispatch.Route` handler). State: it carries **no payload** (a bare control frame — no `modal_id`, no nonce, no idempotency token); it is **gated on the `interactive` capability** (a non-interactive conn's `interrupt` is inert) and **exempt from the per-device permission gate** (#702) because interrupting one's own paired session is a normal paired-phone action, not a tool-permission decision; the daemon maps it to a single Esc to the supervised claude (claude's own interrupt). Note it is **not** part of the reconnect-replay ring and needs no correlation key.

### Data flow

```
phone ── interrupt (noise_msg, AEAD) ──> relay ── RoutingEnvelope ──> V2SessionManager.Run
  └─ handleFrame → handleNoiseMsg → dispatchAppFrame (probe decode)
       └─ case TypeInterrupt → handleInterrupt(s)
            ├─ !s.interactive            → return (inert)              [AC #2 negative path]
            ├─ cfg.Interrupter == nil    → debug log, return          [foreground/unwired]
            └─ cfg.Interrupter.SendEsc() → supervisor.SendEsc()        [AC #2 positive path]
                 └─ tui-driver Session.SendEsc() → one Esc to claude
```

## Concurrency model

No new goroutines, locks, channels, or timers. `handleInterrupt` runs on the manager's **single Run dispatch goroutine** — the same goroutine that reads/writes `s.interactive` and that every other `dispatchAppFrame` handler runs on. The `s.interactive` read is therefore lock-free by the package's single-owner invariant (`v2session.go:248-269`, `:491-505`). `SendEsc` on the supervisor is itself safe to call from any goroutine (it is the same seam `ResolveCancel` / `ResolveTimeout` already call). The handler is synchronous and returns before `dispatchAppFrame` proceeds, honouring the `V2SessionConfig.Handlers` synchronous-handler invariant (`:461-468`).

## Error handling

| Failure mode | Behaviour |
|---|---|
| Conn not interactive | Return, no Esc. The AC #2 negative path — fail-closed, the security property. |
| `Interrupter` nil (foreground / pre-wire) | Debug-log `v2.interrupt.inert`, return. No crash. |
| `SendEsc()` returns error (no live claude / mid-teardown) | Best-effort: `Warn`-log `v2.interrupt.keystroke_err` with the supervisor sentinel + `conn_id`, then return. Nothing to roll back; no reply owed (fire-and-forget). |
| Malformed/duplicate `interrupt` frame | A bad outer frame is already rejected upstream (`decodeInnerFrameV2` / AEAD). The probe decode in `dispatchAppFrame` matches `type` only; `interrupt` carries no payload, so there is no payload-decode failure mode. A replayed `interrupt` simply sends another Esc — idempotent in effect (Esc with no running turn is a no-op in claude); no nonce/dedup needed (per the body). |

## Testing strategy

One test file (extend `internal/relay/v2session_modal_test.go` or add `v2session_interrupt_test.go` — developer's choice; the modal file already holds the `openModalConn` / `sealAppFrameConn` helpers, so co-locating avoids re-exporting them).

**Fake** — a relay-package `fakeInterrupter` mirroring `cmd/pyry/modal_resolve_v2_test.go:32`'s `fakeKeystroker`: a mutex-guarded `escCalls int` counter incremented by `SendEsc()`, with an optional canned error. Mutex because the Run goroutine writes and the test goroutine reads (the `fakeModalResolver` precedent, `:33-44`).

**Test (AC #4) — both routing paths in one test**, table-driven or two `t.Run`s:
- *Interactive interrupt → exactly one Esc.* `startManager` with `Interrupter: fake`; `openModalConn(…, caps=[]string{protocol.CapabilityInteractive})`; send `sealAppFrameConn(initSend, connID, protocol.Envelope{Type: protocol.TypeInterrupt})`; assert `fake.escCalls == 1`. (Use the existing `waitConnOpen` + a short poll/sync barrier the modal tests use to observe the Run-goroutine effect.)
- *Non-interactive interrupt → zero Esc.* Same, but `openModalConn(…, caps=nil)`; assert `fake.escCalls == 0`.

Additional cheap cases worth including (cover the reject branches, not just the happy path):
- *Nil `Interrupter`, interactive interrupt → zero Esc, no panic* (foreground/unwired inert path).
- *`SendEsc` returns error, interactive interrupt → still counts as one call, manager does not crash or close the conn* (best-effort tolerance).

**Protocol partition (AC #1)** is covered by the three `compat_test.go` edits — `TestTypeConstants_V1V2Partition` and `TestIsV1Compatible` fail if `interrupt` is mis-partitioned or leaks into `v1TypeSet`.

**Neutral model (AC #1)** is covered by the `var _ Inbound = Cancel{}` compile-time assertion in `permission.go` plus a one-line membership assertion mirroring `permission_test.go:79` (`var _ Inbound = Cancel{}` inside a `TestCancel_IsInbound`).

Run `go test -race ./internal/protocol/... ./internal/turnevent/... ./internal/relay/... ./cmd/pyry/...` and `go vet ./...`.

## Open questions

- **Multi-phone concurrency.** Any interactive paired phone can interrupt the single live claude — there is no per-conversation/per-connection binding for interrupt, consistent with the existing broadcast fan-out model (a user's paired devices are one trust domain, per the [Security model](../../protocol-mobile.md#security-model)). If a future multi-session world needs interrupt scoped to a specific conversation, that is a later ticket; flagged, not designed here.
- **`Cancel` payload for #600.** `Cancel` is fieldless now. If ACP `session/cancel` needs a session/turn correlation id, #600 adds the field additively. Out of scope here.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The `interrupt` frame is untrusted remote input, but it crosses the boundary already-authenticated (AEAD-sealed under the per-session Noise channel; only a paired device that completed the IK handshake can produce a `noise_msg`). The single new trust decision is the capability gate `if !s.interactive` in `handleInterrupt` — a single named function, on the Run goroutine, reading the server-authoritative `s.interactive` flag. That flag is set fail-closed in `handleNoiseInit`'s token-OK path from the **daemon's** `negotiateCapabilities` output (`v2session.go:1053-1057`), never from the phone's raw advertisement, so a spoofed/over-broad `capabilities` advertisement can never flip it. Downstream the handler holds no untrusted data (the frame has no payload).
- **[Tokens, secrets, credentials]** Not applicable — this path mints, stores, compares no token. The pairing token was validated at handshake; `interrupt` rides the established session. `Cancel` and the `interrupt` frame carry no secret. SendEsc takes no secret argument.
- **[File operations]** Not applicable — no path, no file I/O on this path.
- **[Subprocess / external command execution]** No MUST FIX. The terminal effect is one Esc keystroke into the **already-running** supervised claude via the sealed `supervisor.SendEsc` → tui-driver seam (#726). No `exec.Command`, no argument construction, no env handling — the subprocess pre-exists and no attacker-controlled value reaches it (Esc is a fixed keystroke; the frame carries nothing).
- **[Cryptographic primitives]** Not applicable — no new crypto. The frame's confidentiality/integrity is the existing Noise AEAD transport, unchanged.
- **[Network & I/O]** No MUST FIX. No new socket, listener, or read loop. The frame is bounded by the existing `maxNoisePayloadBytes` cap at `decodeInnerFrameV2` (`v2session.go:797-820`) before it ever reaches `dispatchAppFrame`. `interrupt` adds no unbounded read.
- **[Error messages, logs, telemetry]** No MUST FIX. The handler logs only `event`, `conn_id`, and a supervisor error sentinel (`v2.interrupt.inert` / `v2.interrupt.keystroke_err`). No payload (there is none), no token, no rendered screen, no device secret — consistent with the package's no-secrets-in-logs discipline (`v2session.go:280-283`, `416-426`).
- **[Concurrency]** No MUST FIX. No new goroutine, lock, channel, or timer. `handleInterrupt` runs on the single Run dispatch goroutine; the `s.interactive` read is lock-free under the established single-owner invariant. No TOCTOU: read-and-act happen in one synchronous handler on the owning goroutine. No shutdown hazard: a best-effort `SendEsc` error during teardown is logged and tolerated, leaving no partial state.
- **[Threat model alignment]** Addressed. The relevant [Security model](../../protocol-mobile.md#security-model) decision is whether a remote interrupt is a privileged action. Per the ticket and ADR 025, interrupting one's own paired session is a normal paired-phone action — so it is correctly **capability-gated (`interactive`)** and **exempt from the per-device permission gate (#702)**, which guards tool-permission *answers*, not turn interruption. An old / non-interactive phone is inert (the gate). OUT OF SCOPE: per-conversation interrupt scoping in a multi-session world (a future ticket — see Open questions); not a vulnerability today because the daemon supervises one live claude and a user's paired devices are one trust domain.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-23
