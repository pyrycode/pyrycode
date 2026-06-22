# Spec #727 — Inbound modal control interception + `ModalResolver` seam + `modal_dismissed` broadcast (resolve `modal_cancel`)

**Ticket:** #727 — `feat(bridge): intercept modal_answer/modal_cancel + dismiss-broadcast seam; resolve modal_cancel`
**Size:** S (additive; optional nil-safe config field → no consumer cascade)
**Epic:** #597 Phase 3 — daemon-side modal bridge. Seam-introduction slice, proven via `modal_cancel`.
**security-sensitive:** yes — an inbound untrusted v2 frame mutates the modal lifecycle and fans out a broadcast on the internet-exposed relay surface. Security-review pass at the end of this doc.

---

## Context

The daemon already **surfaces** modals to phones: `interactiveModalEmitterV2` (`cmd/pyry/interactive_modal_v2.go`) turns a detected tui-driver permission/trust modal into a `modal_shown` envelope, mints a one-time `modal_id` nonce, and records it in the `modalbridge.Registry` (#716). What's missing is the **inbound** half: a phone telling the daemon to *resolve* that modal.

`modal_answer` / `modal_cancel` are inbound v2 **control** frames. Like `rekey_request` / `request_snapshot`, the v2 session manager intercepts them in `dispatchAppFrame` **before** `dispatch.Route` — there is **no** `dispatch.Route` handler for them (`internal/protocol/codes.go:172-176`).

This slice introduces two net-new seams that #717 (gated `modal_answer`) and #725 (deny-on-timeout) both consume, and proves them end-to-end via the **simplest terminal decision** — `modal_cancel` (dismiss = fail-safe deny):

1. A consumer-declared **`relay.ModalResolver`** interface (mirroring the `ScreenSnapshotter` precedent) so `internal/relay` imports neither `internal/supervisor` nor `cmd/pyry`. The `cmd/pyry` resolver implements it (registry consume + keystroke + audit) and is wired in `cmd/pyry/relay.go`.
2. The **`modal_dismissed` broadcast** — a manager-internal fan-out to every interactive-capable connection (multiple phones may show the same modal).

`modal_answer` is intercepted and routed through the same seam **but its resolution is a deferred no-op** in this slice: no keystroke, no modal mutation, no dismissal, no audit. #717 fills the gated answer arm. This keeps the potentially-escalating ALLOW path **inert** until the per-device gate (#702/#717) exists — the central fail-safe property of this slice.

### Concurrency constraint (the load-bearing design fact)

The broadcast fires from inside `dispatchAppFrame`, which runs on the manager's **single `Run` goroutine**. The public `ActiveConns` funnels its request *back* onto that same goroutine via `m.snapshot` (`v2session.go:1995-2034`, serviced in `Run`'s select). **Calling `ActiveConns` from inside the interception would deadlock.** The dismiss-broadcast must therefore be a **net-new manager-internal helper that reads `m.sessions` directly on the `Run` goroutine** (exactly like `handleActiveConns`, `v2session.go:2074`) and `Push`es per interactive-capable connection. `Push` (`v2session.go:1840`) is safe to call from the `Run` goroutine: it touches only `m.queues` under `pushMu`, never `m.sessions` / `s.send` / `Outbound`, and returns immediately (the actual seal+forward happens on a later `Run` iteration via `drainOnce`).

---

## Files to read first

| File / lines | What to extract |
|---|---|
| `internal/relay/v2session.go:1200-1250` | `dispatchAppFrame` — the interception switch (`rekey_request` / `request_snapshot` arms) + reply-drain loop. The two modal arms slot into this switch. |
| `internal/relay/v2session.go:1301-1418` | `handleRequestSnapshot` + `snapshotReplyError` — the precedent for a control handler that consumes a consumer-declared seam and forwards/pushes. Mirror its structure (incl. `ID: 1` non-load-bearing envelope id, never-log-payload discipline). |
| `internal/relay/v2session.go:357-364` | `ScreenSnapshotter` interface — the consumer-declared-seam precedent. `ModalResolver` is declared the same way, right beside it. |
| `internal/relay/v2session.go:404-432` | `V2SessionConfig.Handlers` / `Snapshotter` / `KnownConversation` — the **optional, nil-safe** config-field doc pattern. `ModalResolver` mirrors the nil contract (nil ⇒ modal control frames are inert no-ops). |
| `internal/relay/v2session.go:2061-2082` | `handleActiveConns` — the read-`m.sessions`-on-the-`Run`-goroutine precedent + the `s.state == V2StateOpen && s.interactive` filter. `broadcastModalDismissed` mirrors the enumeration but `Push`es instead of returning a slice. |
| `internal/relay/v2session.go:1810-1869` | `Push` — bounded non-blocking primitive; the broadcast's per-conn send. Note: safe from the `Run` goroutine; returns `ErrConnNotFound` for a torn-down conn. |
| `internal/relay/v2session.go:1995-2034` | `ActiveConns` + `m.snapshot` funnel — **why** calling it from the `Run` goroutine deadlocks (the constraint that forces the internal helper). Do **not** call this from the interception. |
| `internal/relay/v2session.go:238-260` | `V2Session.device` (`*devices.Device`, set pre-`V2StateOpen`) + `interactive` (negotiated capability) — what the resolver receives and what the broadcast filters on. |
| `cmd/pyry/interactive_modal_v2.go:60-130` | `interactiveModalEmitterV2.Handle` — the producer-side fan-out + capability gate + never-log-payload discipline to mirror. **Shares the daemon-singleton `modalbridge.Registry`** the resolver consumes (live producer wiring deferred to #708). |
| `internal/modalbridge/modal.go:166-186` | `Registry.Lookup` / `Resolve` — `Resolve` is the **atomic consume-and-retire** the cancel resolver calls; it is the AC-4 idempotency gate (returns `ok=false` for an unknown/already-consumed id). |
| `internal/supervisor/modal.go:34-66` | safe-answer seam (#726): `AcceptTrust` / `Answer` / `SendEsc`. Cancel routes `SendEsc()`. Carries **no** trust decision; ESC is the fail-safe dismiss. |
| `internal/audit/audit.go:27-81` | `audit.Entry` / `Log` / `Outcome` / `Source`. Cancel writes `OutcomeCancelled` + `SourceRemote`. The `Source` set deliberately mirrors the wire vocabulary — pass **one** value to both the wire dismissal and the audit entry. |
| `internal/protocol/messaging.go:120-162` | `ModalAnswerPayload` / `ModalCancelPayload` / `ModalDismissedPayload` — the wire structs decoded/emitted. |
| `internal/protocol/codes.go:188-193` | `TypeModalAnswer` / `TypeModalCancel` / `TypeModalDismissed` constants (the switch cases + the broadcast envelope type). |
| `cmd/pyry/relay.go:294-320` | `V2SessionConfig` construction site — where `ModalResolver` is wired and where the `modalbridge.New()` singleton is constructed (`startRelayV2` is called once per daemon). |
| `internal/relay/v2session_test.go:699-790` | `driveToOpen` / `driveToOpenCaps` / `v2Recorder` — the handshake-to-`V2StateOpen` harness the fan-out test reuses to stand up ≥2 interactive heads and capture outbound frames. |
| `internal/devices/device.go:24-28` | `Device.TokenHash` / `Device.Name` — the non-secret identity fields the audit entry reads. |

---

## Design

### Package & dependency direction

```
internal/relay   (declares ModalResolver + ModalDismissal; intercepts; broadcasts)
      ▲  consumer-declared seam — no import of supervisor / cmd/pyry / modalbridge
      │
cmd/pyry         (modalResolverV2 implements relay.ModalResolver:
                  Registry.Resolve + supervisor safe-answer seam + audit.Log)
      │  imports: internal/relay, internal/modalbridge, internal/supervisor,
      │           internal/audit, internal/devices
      ▼
internal/{modalbridge, supervisor, audit, devices, protocol}   (leaf primitives)
```

This preserves the established direction: the relay declares the seam at the consumer (CODING-STYLE: "define interfaces where consumed"); the composition root (`cmd/pyry`) supplies the implementation. The relay never learns about tui-driver, the registry, or the audit sink.

### 1. `relay.ModalResolver` seam (consumer-declared, `internal/relay/v2session.go`)

Declared beside `ScreenSnapshotter`. Two methods — one per inbound control type — so #717 fills the answer arm without touching the manager's interception code:

```go
// ModalDismissal is the wire outcome+source the manager broadcasts after a
// resolver consumes an outstanding modal. The manager already holds modal_id
// (from the inbound control payload), so it is not repeated here.
type ModalDismissal struct {
	Outcome string // e.g. "cancelled" (cancel) — #717 uses the answered option_id
	Source  string // closed set {remote, local, timeout}; cancel ⇒ "remote"
}

// ModalResolver resolves an inbound modal control frame against the daemon's
// outstanding-modal state. Declared here (consumer side) so internal/relay
// imports neither supervisor nor cmd/pyry. *cmd/pyry resolver satisfies it.
// Both methods run on the manager's single Run dispatch goroutine.
type ModalResolver interface {
	// ResolveCancel consumes modalID (registry Resolve), routes a cancel/ESC
	// keystroke, audits outcome=cancelled, and returns the dismissal to
	// broadcast with ok=true. An unknown/already-resolved id ⇒ (zero, false):
	// no keystroke, no audit, no dismissal (AC-4).
	ResolveCancel(modalID string, dev *devices.Device) (ModalDismissal, bool)

	// ResolveAnswer resolves an inbound modal_answer. In this slice it is a
	// deferred no-op — always (zero, false): no keystroke, no mutation, no
	// audit (AC-3). #717 fills the gated answer arm; the manager code is
	// already general (broadcasts on ok=true) so #717 changes only the impl.
	ResolveAnswer(modalID, optionID, answerToken string, dev *devices.Device) (ModalDismissal, bool)
}
```

`*devices.Device` crosses the seam (the per-conn `s.device`); `internal/relay` already imports `internal/devices` (`V2SessionConfig.Devices`), so no new import.

Add the optional field to `V2SessionConfig` (mirroring `Snapshotter`'s nil doc):

```go
// ModalResolver resolves inbound modal_answer / modal_cancel control frames.
// Optional: when nil, both are inert no-ops (the modal bridge is simply
// unwired — foreground, or pre-#708 before the producer is live). Production
// wires the cmd/pyry resolver.
ModalResolver ModalResolver
```

### 2. Interception (`dispatchAppFrame`, `internal/relay/v2session.go:1210-1218`)

Two new arms in the existing control-discriminator switch:

```go
case protocol.TypeModalCancel:
	m.handleModalCancel(ctx, s, probeEnv)
	return
case protocol.TypeModalAnswer:
	m.handleModalAnswer(ctx, s, probeEnv)
	return
```

Two thin handlers, symmetric, modeled on `handleRequestSnapshot`. Behavior summary (full bodies are the developer's; each is <20 lines):

- **`handleModalCancel(ctx, s, env)`** — nil-`ModalResolver` ⇒ debug-log + return (inert). Else `json.Unmarshal` the payload into `protocol.ModalCancelPayload` (a decode failure tolerated → empty `modal_id` → resolves to the unknown-id no-op, never echoed back). `d, ok := m.cfg.ModalResolver.ResolveCancel(payload.ModalID, s.device)`. If `!ok` return (AC-4). Else `m.broadcastModalDismissed(ctx, payload.ModalID, d)`.
- **`handleModalAnswer(ctx, s, env)`** — nil-`ModalResolver` ⇒ debug-log + return. Else decode `protocol.ModalAnswerPayload`, `d, ok := m.cfg.ModalResolver.ResolveAnswer(payload.ModalID, payload.OptionID, payload.AnswerToken, s.device)`. If `!ok` return. Else broadcast. In this slice `ok` is always `false` (deferred no-op), so the broadcast line is **unreachable until #717** — but present, so #717 needs no manager change.

The nil guard makes the new `V2SessionConfig` field genuinely optional: every existing `V2SessionConfig{...}` construction (tests, and any other call site) keeps compiling and behaves identically (modal frames inert). **No consumer cascade** — this is why the slice stays S.

### 3. `broadcastModalDismissed` (manager-internal helper, `internal/relay/v2session.go`)

```go
// broadcastModalDismissed fans a modal_dismissed envelope to every
// interactive-capable open session. Runs on the Run goroutine; reads
// m.sessions directly (like handleActiveConns) and Pushes per conn — it MUST
// NOT call ActiveConns (which funnels onto this same goroutine via m.snapshot
// → deadlock). Push is non-blocking and Run-goroutine-safe.
func (m *V2SessionManager) broadcastModalDismissed(ctx context.Context, modalID string, d ModalDismissal)
```

Behavior: marshal `protocol.ModalDismissedPayload{ModalID: modalID, Outcome: d.Outcome, Source: d.Source}` (marshal-failure ⇒ debug-warn + return; closed struct, unreachable in practice, never echo payload). One shared `time.Now().UTC()`. For each `connID, s := range m.sessions` where `s.state == V2StateOpen && s.interactive`, build `protocol.Envelope{ID: 1, Type: protocol.TypeModalDismissed, TS: ts, Payload: payload}` and `m.Push(ctx, connID, env)`; on a non-nil `Push` error (ctx teardown or `ErrConnNotFound` from a raced teardown) debug-log and continue — never echo payload bytes.

**Envelope `ID: 1` is non-load-bearing**, matching `handleRequestSnapshot`'s manager-internal pushes: the phone correlates `modal_dismissed` on `modal_id` (not envelope id), and `modal_dismissed` is a control event (`EventID == nil`, never in the #647 replay ring). This deliberately avoids adding a per-session outbound counter to `V2Session` (Simplicity First; the producer-side `interactiveModalEmitterV2.nextID` counter is a separate, producer-owned concern and is not reused here).

The capability filter `s.interactive` is the same #607 gate `modal_shown` rides — a non-interactive (old) phone never receives v2 modal events. The fan-out reaches *every* interactive conn (including ones that never saw this modal's `modal_shown`); the payload carries only the opaque `modal_id` + `cancelled`/`remote`, no modal body, so this discloses nothing (a conn with no matching outstanding modal ignores it).

### 4. `modalResolverV2` (the implementation, new file `cmd/pyry/modal_resolve_v2.go`)

```go
// modalKeystroker routes one abstract modal-resolution keystroke to the live
// claude session. *supervisor.Supervisor satisfies it (#726). Cancel needs only
// SendEsc; #717 extends this interface with Answer/AcceptTrust for the gated arm.
type modalKeystroker interface {
	SendEsc() error
}

type modalResolverV2 struct {
	reg    *modalbridge.Registry
	kb     modalKeystroker
	logger *slog.Logger
}

func newModalResolverV2(reg *modalbridge.Registry, kb modalKeystroker, logger *slog.Logger) *modalResolverV2
```

- **`ResolveCancel(modalID, dev) (relay.ModalDismissal, bool)`** (behavior, <20 lines):
  1. `out, ok := r.reg.Resolve(modalID)`. If `!ok` ⇒ `return relay.ModalDismissal{}, false` (AC-4 — the registry consume is the single idempotency gate; a replayed/unknown id never gets past here).
  2. `r.kb.SendEsc()` — **best-effort actuation**. On error (only `ErrNoLiveSession` or a teardown PTY-write error → claude is gone/going, the modal is moot), `Warn`-log `event=modal_cancel.keystroke_err`, `modal_id`, `err` (a supervisor sentinel/transport error — never a secret) and **continue**: the modal is already consumed (idempotency committed at step 1), so the phone must still learn the dismissal and the forensic record must still exist. Do **not** abort the audit/broadcast on a keystroke error (evidence-based: aborting would orphan the consumed modal and is an unobserved-mode defense).
  3. `audit.Log(r.logger, audit.Entry{DeviceHash: dev.TokenHash, DeviceLabel: dev.Name, ModalID: modalID, ModalClass: out.Class, Outcome: audit.OutcomeCancelled, Source: audit.SourceRemote})` (tolerate `dev == nil` → empty hash/label; on an open session `dev` is non-nil).
  4. `return relay.ModalDismissal{Outcome: string(audit.OutcomeCancelled), Source: string(audit.SourceRemote)}, true` — **one** source vocabulary feeds both the wire dismissal and the audit entry (audit.go's documented contract).

- **`ResolveAnswer(modalID, optionID, answerToken, dev) (relay.ModalDismissal, bool)`** — deferred no-op: optionally `Debug`-log `event=modal_answer.deferred` (a debug log is **not** an audit entry, so AC-3 holds), then `return relay.ModalDismissal{}, false`. It does **not** call `Resolve` (no mutation), routes no keystroke, writes no audit. #717 replaces this body.

### 5. Wiring (`cmd/pyry/relay.go`, in `startRelayV2`)

```go
// daemon-singleton outstanding-modal registry. #708 wires the producer/emitter
// (interactiveModalEmitterV2) to this same instance; until then nothing Records,
// so every production modal_cancel takes the unknown-id no-op path (harmless).
modalReg := modalbridge.New()
```

Add to the `relay.V2SessionConfig{...}` literal:

```go
ModalResolver: newModalResolverV2(modalReg, sup, logger),
```

`sup` (`*supervisor.Supervisor`) satisfies `modalKeystroker` (it has `SendEsc`). `startRelayV2`'s signature is unchanged (the registry is a local). New imports: `internal/modalbridge` in `relay.go` (the package already imports it via `interactive_modal_v2.go`); `internal/audit` + `internal/modalbridge` + `internal/devices` + `internal/relay` + `log/slog` in the new `modal_resolve_v2.go`.

---

## Data flow

```
phone ── modal_cancel{modal_id} ──▶ relay WSS ──▶ V2Session (V2StateOpen, authenticated, interactive)
                                                        │  noise decrypt
                                                        ▼
                          dispatchAppFrame  (Run goroutine)
                                                        │  probe Type == modal_cancel
                                                        ▼
                          handleModalCancel ──▶ ModalResolver.ResolveCancel(modal_id, s.device)
                                                        │            (cmd/pyry/modalResolverV2)
                                                        │   1. Registry.Resolve(modal_id)  ── ok? ──no──▶ no-op (AC-4)
                                                        │   2. supervisor.SendEsc()  (best-effort ESC into claude)
                                                        │   3. audit.Log(cancelled, remote)
                                                        │   4. return {cancelled, remote}, true
                                                        ▼
                          broadcastModalDismissed (Run goroutine, reads m.sessions directly)
                                                        │  for each open && interactive conn:
                                                        ▼
                          Push(connID, modal_dismissed{modal_id, cancelled, remote})  ──▶ all interactive phones
```

`modal_answer` follows the same path into `handleModalAnswer → ResolveAnswer`, which returns `(zero, false)` ⇒ the flow stops at the resolver: no ESC, no audit, no broadcast.

---

## Concurrency model

- **One goroutine.** Everything here runs on the manager's existing single `Run` goroutine: `dispatchAppFrame` → `handleModalCancel`/`handleModalAnswer` → `ResolveCancel`/`ResolveAnswer` → `broadcastModalDismissed`. No new goroutine, no new lock on the manager. Inbound frames are serialized, so two `modal_cancel`s for the same id can't race; `Registry.Resolve`'s own mutex + atomic delete is the belt-and-suspenders dedup.
- **Deadlock avoidance (the whole point).** The broadcast reads `m.sessions` directly (`handleActiveConns` pattern) and uses `Push` (touches only `m.queues`/`pushMu`). It must never call `ActiveConns`/`ActiveConnIDs` (they send on `m.snapshot`, serviced by *this same* goroutine → deadlock). The test proves no-deadlock structurally: it drives the `modal_cancel` through the real `Frames` channel and the real `Run` loop, so an accidental `ActiveConns` call would hang the test.
- **Push timing.** `Push` enqueues + signals `drainCh` (cap-1, non-blocking) and returns; the actual seal+forward runs on a later `Run` iteration via `drainOnce`. So `modal_dismissed` delivery is slightly deferred (the intended ADR-025 push-buffer behavior), never inline-blocking the dispatch goroutine.
- **Resolver registry mutex** is a leaf lock (O(1) map op), never nested with another lock — consistent with `modalbridge`'s documented contract.

---

## Error handling & failure modes

| Failure | Handling |
|---|---|
| `ModalResolver == nil` (foreground / pre-#708) | Both handlers debug-log and return — modal control frames inert. Keeps the config field optional (no consumer cascade). |
| Malformed `modal_cancel` / `modal_answer` JSON | Decode error tolerated; empty `modal_id` → `Resolve` miss → no-op. Never echoed to the phone (no reply at all for modal control — fire-and-broadcast, not request/reply). |
| Unknown / already-resolved `modal_id` (cancel) | `Registry.Resolve` returns `ok=false` ⇒ no keystroke, no audit, no broadcast (AC-4). The atomic delete makes the *first* cancel win; every subsequent one no-ops. |
| `SendEsc()` error (no live session / teardown) | `Warn`-log (`err` is a non-secret sentinel); audit + broadcast still proceed — the modal is consumed and moot. |
| `modal_answer` (this slice) | `ResolveAnswer` no-op ⇒ inert (AC-3). The escalating ALLOW path stays dead until #717's gate. |
| `Push` returns `ErrConnNotFound` / ctx err during broadcast | Debug-log per conn, continue the fan-out; never echo payload. A conn torn down between enumeration and `Push` simply misses the dismissal (it's gone anyway). |
| `ModalDismissedPayload` marshal failure | Defensive (closed struct; unreachable). Debug-warn + return; the broadcast is skipped, never a crash, never echoes payload. |

No new sentinel errors are introduced: `ResolveCancel`/`ResolveAnswer` signal "nothing to do" with the `(zero, false)` tuple, not an error (mirroring `Snapshotter.ScreenSnapshot`'s `(text, live)` shape).

---

## Testing strategy

The seam boundary splits the AC-5 assertions across two test files (the relay can't import the supervisor/audit/registry; `cmd/pyry` is the composition root). Each clause maps to the side that owns it.

### `internal/relay/v2session_modal_test.go` (new) — interception + fan-out

Uses a **fake `ModalResolver`** (records calls; returns a canned `ModalDismissal{"cancelled","remote"}` with `ok=true` for cancel, `(zero,false)` for answer) and the existing `driveToOpen`/`driveToOpenCaps`/`v2Recorder` harness.

- **Fan-out to ≥2 interactive heads (AC-2):** drive two conns to `V2StateOpen` with the interactive capability; send a `modal_cancel` frame on one through the real `Frames`/`Run` loop; assert `ResolveCancel` was called with the right `modal_id` + device, and a `modal_dismissed{modal_id, cancelled, remote}` envelope is decrypted at **both** recorders. (Running through the real `Run` loop is the no-deadlock proof — a hang fails the test.)
- **Capability gate:** add a third conn negotiated **non**-interactive; assert it receives **no** `modal_dismissed`.
- **`modal_answer` no-op (AC-3):** send a `modal_answer`; assert `ResolveAnswer` was called (routed through the seam) and **no** `modal_dismissed` was emitted to any conn.
- **Unknown-id no-op (AC-4):** fake resolver returns `(zero,false)`; assert no `modal_dismissed`.
- **Nil resolver:** `V2SessionConfig` with `ModalResolver: nil`; send `modal_cancel`; assert no panic, no outbound frame, session stays `V2StateOpen` (mirrors the rekey-request inert tests).

### `cmd/pyry/modal_resolve_v2_test.go` (new) — resolver: consume + keystroke + audit

Uses a **fake `modalKeystroker`** (records `SendEsc` calls; injectable error), a **real `modalbridge.Registry`** (scripted via `Record`/direct insert), a `*devices.Device`, and a captured `slog` logger.

- **Cancel happy path (AC-1, AC-5):** script an outstanding modal; `ResolveCancel(id, dev)` ⇒ returns `({"cancelled","remote"}, true)`; assert `SendEsc` routed exactly once; assert the registry entry is **consumed** (a second `Resolve` misses); assert one audit record at `Info` with `outcome=cancelled`, `source=remote`, `modal_id`, `modal_class`, `device_hash`, `device_label` — and **no** modal body/prompt/title in any log field.
- **Unknown id (AC-4):** `ResolveCancel("does-not-exist", dev)` ⇒ `(zero,false)`; assert **no** `SendEsc`, **no** audit record.
- **Already-resolved id (AC-4):** `Resolve` once, then `ResolveCancel` the same id ⇒ `(zero,false)`; no keystroke, no audit.
- **Keystroke error:** inject `SendEsc` → `ErrNoLiveSession`; assert the modal is still consumed, a `Warn` is logged (err present, no secret), and the audit record + `(…, true)` return still happen.
- **`modal_answer` no-op (AC-3, AC-5):** `ResolveAnswer(id, opt, tok, dev)` ⇒ `(zero,false)`; assert **no** `SendEsc`, **no** audit record, and the registry entry is **untouched** (still resolvable afterward — proves "modal is not mutated").

All tests stdlib-only, table-driven where natural, `t.Parallel()` where safe, no live claude / no real PTY.

---

## Open questions / sequencing

- **Producer wiring is deferred to #708.** This slice constructs `modalReg := modalbridge.New()` and wires only the *consumer* (resolver). Nothing `Record`s into it in production until #708 live-wires `interactiveModalEmitterV2`. Consequence: until #708 lands, every production `modal_cancel` takes the unknown-id no-op path. This is the intended phased rollout (seam first, proven by tests; producer next). The registry instance is shared by construction — #708 passes the same `modalReg` to the emitter.
- **#717 layers on this seam** by replacing `ResolveAnswer`'s body (validate → per-device gate (#702) → route `Answer`/`AcceptTrust` → audit → return a real `ModalDismissal`) and extending `modalKeystroker` with `Answer`/`AcceptTrust`. The relay manager code (`handleModalAnswer` + `broadcastModalDismissed`) needs **no** change — it already broadcasts on `ok=true`. That is the "proven seam" this slice delivers.
- **#725 (deny-on-timeout)** reuses `broadcastModalDismissed` + the audit path with `{denied_timeout, timeout}`, driven by the daemon's own timer rather than an inbound frame.
- **Cancel is intentionally ungated** (see security review below). If a future ticket decides cancel must also pass a gate, it slots into `handleModalCancel` the same way #717's gate slots into the answer arm.

---

## Security review

**Verdict:** PASS (no MUST FIX). Adversarial self-review per `architect/security-review.md`, walking all nine categories against the design above. Default-FAIL mindset: the central question per category was "what's the worst a hostile phone, a buggy caller, or a confused developer could trigger?"

**Findings:**

- **[1 Trust boundaries]** No findings. The single explicit boundary is `V2StateOpen`: `modal_cancel` / `modal_answer` reach `dispatchAppFrame` **only** after the Noise_IK handshake completes *and* the device token validates (`s.device` non-nil, set once pre-open). Authentication is entirely upstream; the interception never runs for an un-authenticated peer, and the drain side re-checks `V2StateOpen` (`forwardEnvelope`/`Push`). The untrusted `modal_id` is decoded into a typed `protocol.ModalCancelPayload` and used **only as a `Registry` map key** — never interpolated into a path, command, or query — so the membership check (`Resolve`) is the sole trust decision and there is no injection surface. Downstream code (resolver, broadcast) holds typed, validated data.

- **[2 Tokens, secrets, credentials]** No findings (this slice mints/stores nothing). The `modal_id` correlation nonce is `crypto/rand` 122-bit (minted in #716, not here). The device token never crosses the seam — only `Device.TokenHash` (SHA-256) + `Device.Name` reach the audit entry, which is *structurally* incapable of holding a plaintext token (`audit.go` imports only `log/slog`). No secret is logged, wrapped into an error, or put on the wire.

- **[3 File operations]** N/A — this slice performs no filesystem I/O. `modal_id` is a map key, never a path component; no `os.Open`/`Stat`/path concatenation exists in the changed code.

- **[4 Subprocess / external command]** No findings for this slice; one OUT OF SCOPE note. The only keystroke a remote frame can drive here is the **constant ESC** (`supervisor.SendEsc`) — no user-controlled bytes are routed to claude's PTY. The escalating path — routing a phone-influenced `option_id` → `Answer()` keystroke — is the inert `ResolveAnswer` no-op (AC-3). **OUT OF SCOPE → #717:** that gate must validate the `option_id` → choice mapping (against the recorded `Outstanding.Options`) before routing any `Answer`/`AcceptTrust`, so a phone cannot inject an arbitrary keystroke. Named and deferred; the seam is introduced *dead* so the consideration cannot bite until #717 wires it live.

- **[5 Cryptographic primitives]** No findings. No new crypto in this slice; the Noise AEAD path and `StaticPriv`/CipherStates are untouched. The one security primitive relied on (`modal_id` unguessability) is `crypto/rand` in #716. No hand-rolled crypto, no key/nonce reuse introduced.

- **[6 Network & I/O]** No findings. This slice adds **no** new socket Read — the inbound frame is already size-capped and decrypted by the upstream noise/transport layer before `dispatchAppFrame` sees the plaintext. `ModalCancelPayload`/`ModalAnswerPayload` are tiny fixed-shape structs; an oversized body is bounded by the existing frame cap (no new cap needed). Outbound, the broadcast uses the bounded, drop-policy `Push` buffer. **Enumeration-oracle check:** a successful cancel broadcasts `modal_dismissed` (the canceling phone, being interactive, observes it) while a miss is silent — an observable hit/miss signal. But a "hit" requires already knowing a valid 122-bit `modal_id` (only obtainable from the `modal_shown` the phone received), each guess is independent with no partial-match leak, and brute-force is 2⁻¹²² per attempt — the oracle reveals nothing the holder didn't already have. Not exploitable.

- **[7 Error messages, logs, telemetry]** No findings — the discipline is baked into the design. MUST-NOT-log (enforced): modal body/prompt/title, payload bytes, tokens. MUST-log (present): content-free `event` discriminants, `conn_id`, the opaque `modal_id`, `device_hash`, `class`, `outcome`, `source`. The `SendEsc` error logged on the keystroke-error path is a supervisor sentinel/transport error, never a secret. **No reply is ever sent for a modal control frame** (fire-and-broadcast, not request/reply), so no decode error or attacker-controlled byte is echoed back to the phone. Audit at `Info`, one record per resolved decision.

- **[8 Concurrency]** No findings. Everything runs on the manager's existing single `Run` goroutine — no new manager lock, no spawned goroutine (so no leak is possible). The deadlock-avoidance *is* the central design: the broadcast reads `m.sessions` directly (`handleActiveConns` pattern) and never calls `ActiveConns` (which would funnel onto this same goroutine via `m.snapshot` → deadlock). `Registry.Resolve`'s mutex is a leaf lock (one O(1) map op, atomic return-and-delete), never nested. Shutdown-mid-broadcast: `Push` returns `ctx.Err()`/`ErrConnNotFound`, handled per-conn (debug-log, continue) — no partial-state corruption (the registry consume already committed atomically; an undelivered dismissal just means that conn re-syncs on reconnect).

- **[9 Threat model alignment]** Addressed against `docs/protocol-mobile.md` § Modal / Security model + ADR 025 §6:
  - *Unauthorized ALLOW* — the answer arm is inert until the #702 per-device gate (#717). **OUT OF SCOPE → #717**, named.
  - *`modal_id` replay / duplicate* — `Registry.Resolve` atomic consume; first cancel wins, replays no-op (AC-4). Addressed.
  - *Confused-deputy / cross-device cancel* — the 122-bit nonce is the per-modal capability; a phone can only cancel a modal it received via `modal_shown`. Addressed.
  - *Forensic audit* — `audit.Log` writes exactly one `{cancelled, remote}` record per resolved cancel (ADR 025 §6 "Audit"). Addressed.
  - *Deny-on-timeout* — the daemon-internal safe-deny. **OUT OF SCOPE → #725**, named (reuses this slice's broadcast + audit path).
  - *Cancel gating* — design decision (not a gap): cancel is the fail-safe terminal action (dismiss = deny; worst case is nuisance dismissal, never escalation), so it is intentionally **not** behind the #702 answer gate. Documented in § Design and § Open questions.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-23
