# Spec #831 — `new_session` wire type + `/clear` relay routing

**Ticket:** feat(wire): new_session (startNewSession / `/clear`) v2 wire message + relay routing
**Size:** S (held; no split — the design is the additive `interrupt` shape, one seam narrower)
**Labels:** `security-sensitive` (security review at the end of this spec)
**Epic:** #597 Phase 3 (interactive modals, permissions, remote control) — split from #824
**Consumes:** #830's `Supervisor.StartNewSession()` `/clear` keystroke seam.

## Files to read first

The developer's turn-1 data load. Read these before writing code. **This ticket is #707 (`interrupt`) with the verb swapped from Esc→`/clear` and the neutral-`turnevent` step dropped.** Read #707's spec + its diff first; then read the current code below (the code has since consolidated into `V2SessionManager`, so the line numbers here supersede #707's spec prose).

- `docs/specs/architecture/707-interrupt-wire-type-esc-routing.md` — the direct template. Every section of this spec maps 1:1 onto a section there. The one deletion: #707 §3 added `turnevent.Cancel` to the `Inbound` sum type for the #600 ACP adapter; **`new_session` needs no such neutral command** — it drives the supervisor seam directly (see § *What this ticket deliberately does NOT do*).
- `docs/specs/architecture/830-start-new-session-seam.md` — the seam this consumes. `Supervisor.StartNewSession() error` types `/clear` (ClearInputLine + `TypePrompt("/clear")`), fire-and-forget, `ErrNoLiveSession` when detached, loud wrapped error on PTY failure, no `context.Context`. **This ticket reads the shipped method name, not a paragraph** — confirmed below.
- `internal/supervisor/modal.go:81` — `func (s *Supervisor) StartNewSession() error` (#830). The sealed keystroke surface. `*supervisor.Supervisor` already satisfies the new `SessionStarter` seam (below) with **zero new supervisor code**.
- `internal/protocol/codes.go:249-306` — the `TypeInterrupt` block (`:249-255`) and `TypeRequestDebugBundle` block (`:280-306`): the single-const-block form + the standing **"MUST NOT be added to v1TypeSet"** doc convention. `TypeNewSession` is a new block in this same style, appended after `:306`.
- `internal/protocol/compat_test.go` — the v1/v2 partition drift detector. Three edits: add `TypeNewSession: true` to `v2OnlyTypes` (`:122-149`, `TypeInterrupt` is at `:143`); add `TypeNewSession` to `TestTypeConstants_V1V2Partition`'s `all` slice (`:158-204`, `TypeInterrupt` at `:184`); add `{"new_session-rejected", TypeNewSession, false, ErrUnknownType}` to `TestIsV1Compatible`'s cases (`:8-87`, `interrupt-rejected` at `:69`). Do **not** touch `TestV1TypeSet_CoversAllExportedTypeConstants` (`:89-110`) — its `len(all), 16` count enumerates v1 types only.
- `internal/protocol/envelope.go` — `v1TypeSet`. **Stays unchanged.** The drift detector forces the `v2OnlyTypes` edit; production `v1TypeSet` never gains `new_session`.
- `internal/relay/v2session.go:437-445` — the `Interrupter` interface: the consumer-side-seam pattern (`internal/relay` declares the seam so it imports neither `internal/supervisor` nor tui-driver). `SessionStarter` is a new sibling here.
- `internal/relay/v2session.go:584-588` — the `Interrupter` field in `V2SessionConfig` (optional-seam doc style, nil-behaviour note). The new `SessionStarter` field goes beside it.
- `internal/relay/v2session.go:1493-1527` — `dispatchAppFrame`: the v2 control-envelope discriminator switch. The `new_session` intercept case slots in beside `case protocol.TypeInterrupt` (`:1517`).
- `internal/relay/v2session.go:1975-2014` — `handleInterrupt`: the exact handler template (interactive gate first, nil-seam guard, best-effort warn-log, never-echo, no reply, `(s *V2Session)` signature — no `ctx`, no `env`). `handleNewSession` mirrors it line-for-line with `StartNewSession()` in place of `SendEsc()`.
- `internal/relay/v2session.go:287-294` — `V2Session.interactive`: the negotiated capability flag the gate reads. `:1246-1250` — its set site (`s.interactive = slices.Contains(negotiated, …)`, fail-closed from the **daemon's** negotiated caps, Run-goroutine-owned, no lock).
- `cmd/pyry/relay.go:340-343` — the `Interrupter: sup` line in the production `V2SessionConfig{…}` literal. `sup` (`*supervisor.Supervisor`) is already in scope; add one sibling line: `SessionStarter: sup`.
- `internal/relay/v2session_interrupt_test.go` (whole file, 158 lines) — the test idioms to mirror **verbatim**: `fakeInterrupter` (mutex-guarded call counter + injectable error), `startManager(t, V2SessionConfig{…})`, `openModalConn(t, mgr, frames, rec, respPub, connID, caps)` (pass `[]string{protocol.CapabilityInteractive}` vs `nil`), `sealAppFrameConn(t, send, connID, protocol.Envelope{Type: …})`, and the **barrier-conn** pattern that makes the Run-goroutine effect observable without a sleep.
- `docs/protocol-mobile.md:405-444` — the Application message types table (add a `new_session` row after the `interrupt` row at `:444`). `:560-573` — the `session_transition` subsection (the observable outcome the new prose points to; note its `reason` closed set already includes `clear`). `:697-705` — the `### Interrupt (v2)` subsection: the model for the new `### New session (v2)` subsection.

## Context

Phase 3 of epic #597 adds the **remote start-new-session** control: the paired phone's equivalent of typing `/clear` at the local terminal. A phone sends a bare `new_session` frame; the daemon gates it on the negotiated `interactive` capability and routes it to the supervised claude as a `/clear` — starting a fresh session remotely. Mobile stubs this action today because the daemon has no wire message for it; landing it in the shared daemon lets both mobile and desktop inherit it.

This is the direct analog of `interrupt` (#707): same inbound-control shape (intercepted in `dispatchAppFrame` before `dispatch.Route`, capability-gated, fire-and-forget), one verb over. The only structural differences from `interrupt`:

1. **The terminal effect is `/clear`, not Esc.** The seam method is `StartNewSession()` (#830), not `SendEsc()` (#726).
2. **No neutral `turnevent` command.** #707 added `turnevent.Cancel` so #600's ACP `session/cancel` adapter would have a target. `new_session` has no announced ACP counterpart requiring a neutral form; it drives the supervisor seam directly, exactly as the mobile `modal_cancel` frame routes to `ModalResolver.ResolveCancel` without constructing a `turnevent` value. If a future ACP `session/new` needs a neutral form, that is an additive later ticket — out of scope here.

Every dependency already exists on `main`:

- **The keystroke seam is sealed** — `supervisor.StartNewSession()` (#830) ships unwired, waiting for exactly this consumer. This ticket does **not** touch tui-driver or supervisor.
- **The observable outcome is already built.** When `/clear` rotates claude's session UUID, the rotation watcher fires `notifyTransition(ReasonClear)` and the existing `session_transition` emitter (#656/#657) fans a boundary marker — `reason: "clear"` (`protocol-mobile.md:569`) — to every interactive conn. **This ticket builds NO new emitter and NO ack path.** Like `interrupt`, `new_session` is fire-and-forget; the client-observable break is the pre-existing `session_transition` marker.
- **The inbound v2 control-frame interception pattern** (`dispatchAppFrame` before `dispatch.Route`) is established by `rekey_request`, `request_snapshot`, `modal_cancel`, `modal_answer`, `interrupt`, `dequeue_message`, `request_debug_bundle`.
- **The inbound `interactive` capability gate** is established by `interrupt` (#707) and `dequeue_message` (#723). `new_session` reuses the same one-line `if !s.interactive` check — not a new abstraction.

## Design

**Three production files, two packages, plus the protocol doc and one test file.** All changes are additive — no signature changes, no consumer cascade, no new supervisor code.

### 1. Wire vocabulary — `internal/protocol/codes.go`

Add one v2-only control const in its own block, appended after the `TypeRequestDebugBundle` block (`:306`):

```go
const (
    TypeNewSession = "new_session" // phone → binary, inbound v2 control (intercepted pre-dispatch.Route)
)
```

The doc comment mirrors the `TypeInterrupt` block's form (`:238-252`): (a) direction + trust posture, (b) the standing **"MUST NOT be added to v1TypeSet … the drift detector in compat_test.go partitions Type\* constants"** boilerplate. Call out the differences from the modal frames and the salient facts:

- Bare control frame: no `conversation_id`, no `modal_id` nonce, no `answer_token`, no idempotency key (like `interrupt` / `request_debug_bundle`).
- Interactive-capability-gated; **exempt from the per-device permission gate (#702)** — starting a fresh session in one's own paired session is a normal paired-phone action, not a tool-permission decision.
- Drives claude's `/clear` via `Supervisor.StartNewSession()` (#830). The client observes the resulting break through the **existing** `session_transition` marker (`reason: "clear"`, #656/#657) — **there is no synchronous ack** (fire-and-forget).
- No neutral `turnevent` command (unlike `interrupt`→`turnevent.Cancel`) — it routes to the seam directly.

### 2. Partition enforcement — `internal/protocol/compat_test.go` (test only)

Three edits, each modelled on the adjacent `TypeInterrupt` entry:

- `v2OnlyTypes` (`:122-149`): add `TypeNewSession: true` under a `// v2 new_session control` comment.
- `TestTypeConstants_V1V2Partition`'s `all` slice (`:158-204`): add `TypeNewSession` under a `// v2 new_session control` comment. The `len(v1TypeSet)+len(v2OnlyTypes) == len(all)` assertion then auto-verifies the disjoint partition.
- `TestIsV1Compatible`'s `cases`: add `{"new_session-rejected", TypeNewSession, false, ErrUnknownType}` — an old (v1) phone never receives or accepts it (AC #2).

`v1TypeSet` in `envelope.go` is **not** edited; `TestV1TypeSet_CoversAllExportedTypeConstants` (`len(all), 16`) is **not** edited (it counts v1 types only; `new_session` is v2-only).

### 3. Routing seam + handler — `internal/relay/v2session.go`

**Consumer-side interface** — a new sibling of `Interrupter` (`:437-445`), placed beside it:

```go
// SessionStarter drives the supervised claude's /clear — the remote equivalent
// of a local `/clear`, starting a fresh session. *supervisor.Supervisor
// satisfies it via StartNewSession (#830), so the supervisor needs no new
// method. Declared here (consumer side), beside Interrupter, so internal/relay
// imports neither internal/supervisor nor tui-driver.
type SessionStarter interface{ StartNewSession() error }
```

Named for its relay-domain role (matching `Interrupter` / `ScreenSnapshotter`), method keeps the sealed surface's name `StartNewSession`. (Naming is a minor developer call; `SessionStarter` is the recommendation — a one-method `-er` interface, unambiguous against any pool/session lifecycle "start" because the doc comment fixes the `/clear` semantics.)

**Config field** — optional seam in `V2SessionConfig`, beside `Interrupter` (`:584-588`), mirroring its doc style:

```go
// SessionStarter routes an inbound interactive `new_session` control frame to
// the supervised claude as a /clear (#831). Optional: nil ⇒ new_session is
// inert (no /clear) — the foreground / unwired case. Production wires
// *supervisor.Supervisor.
SessionStarter SessionStarter
```

**Intercept case** in `dispatchAppFrame`'s switch, beside `case protocol.TypeInterrupt` (`:1517-1519`):

```go
case protocol.TypeNewSession:
    m.handleNewSession(s)
    return
```

**Handler** — the only new logic, a line-for-line mirror of `handleInterrupt` (`:1975-2014`):

```go
// handleNewSession routes an inbound new_session to the supervised claude as a
// /clear, gated on the conn's interactive capability. No payload, no reply, no
// broadcast — the client observes the break via the existing session_transition
// marker (#656/#657). Runs on the manager's single Run dispatch goroutine.
func (m *V2SessionManager) handleNewSession(s *V2Session)
```

Behaviour, in order (order is load-bearing — capability gate first):

1. **`if !s.interactive` → return** (AC #4: a non-interactive conn is inert; no `/clear`). The inbound capability gate — copy `handleInterrupt`'s one-line check and its comment (a shared one-liner does not warrant a helper abstraction; `new_session` / `interrupt` / `dequeue_message` each keep their own bare check, per CODING-STYLE over-DRY).
2. **`if m.cfg.SessionStarter == nil`** → debug-log `v2.new_session.inert` (`conn_id` only) and return (foreground / pre-wire; mirrors `handleInterrupt`'s nil guard). AC #5 (nil seam ⇒ inert).
3. **`m.cfg.SessionStarter.StartNewSession()`** — best-effort. An error (no live session / mid-teardown → `ErrNoLiveSession`) is `Warn`-logged with event `v2.new_session.keystroke_err`, `conn_id`, and the supervisor sentinel **only** (never payload bytes — there are none — and never the rendered screen), then tolerated. Nothing to roll back; no reply owed (AC #5).

Signature takes only `s` (no `ctx`, no `env`): the frame carries no payload to decode and does no cancellable work — the intentional, documented deviation `handleInterrupt` already established. Even if a future client attaches a payload, the probe decode in `dispatchAppFrame` matches `type` only; the handler never reads or echoes it.

### 4. Production wiring — `cmd/pyry/relay.go`

Add one field to the `V2SessionConfig{…}` literal, beside `Interrupter: sup` (`:343`):

```go
// Inbound new_session seam (#831): an interactive `new_session` frame routes a
// /clear through the sealed supervisor keystroke surface. sup
// (*supervisor.Supervisor) satisfies SessionStarter via StartNewSession (#830).
SessionStarter: sup,
```

`sup` is already in scope (passed as `Snapshotter`, `Interrupter`, and into `newModalResolverV2`). No new import, no new construction.

### 5. Protocol doc — `docs/protocol-mobile.md`

- **Application message types table** (`:444`, after the `interrupt` row): add
  `| **`new_session`** | phone → binary | no | **New in v2.** Inbound control — phone starts a fresh session (remote `/clear`). Interactive-capability-gated; exempt from the permission gate. See [New session](#new-session-v2). |`
- **New `### New session (v2)` subsection** (insert after `### Interrupt (v2)` at `:705`, before `### Debug bundle (v2)`), modelled on the Interrupt subsection. State:
  - Direction **phone → binary** (inbound v2 control). Intercepted before `dispatch.Route` (not a `dispatch.Route` handler), like `interrupt` / `modal_cancel`.
  - Carries **no payload** — a bare control frame (no `conversation_id`, no nonce, no idempotency key). Drives claude's `/clear` via the sealed supervisor `StartNewSession` seam (#830).
  - **Gated on the `interactive` capability** (a non-interactive conn's `new_session` is inert) and **exempt from the per-device permission gate** (#702) — starting a fresh session in one's own paired session is a normal paired-phone action.
  - **The client observes the break via the existing [`session_transition`](#interactive-events-v2-capability-gated) marker** (`reason: clear`, #656/#657) — **there is no synchronous ack.** `new_session` is fire-and-forget, like `interrupt`; it is not part of the reconnect-replay ring and needs no correlation key.
  - Any interactive paired phone can start a new session on the single live claude — no per-connection binding, consistent with the broadcast fan-out model (a user's paired devices are one trust domain).

### Data flow

```
phone ── new_session (noise_msg, AEAD) ──> relay ── RoutingEnvelope ──> V2SessionManager.Run
  └─ handleFrame → handleNoiseMsg → dispatchAppFrame (probe decode, type-only)
       └─ case TypeNewSession → handleNewSession(s)
            ├─ !s.interactive              → return (inert)             [AC #4 negative path]
            ├─ cfg.SessionStarter == nil   → debug log, return          [foreground/unwired, AC #5]
            └─ cfg.SessionStarter.StartNewSession() → supervisor.StartNewSession()  [AC #3]
                 └─ ClearInputLine + TypePrompt("/clear") → claude /clear (#830)
                      └─ (later, async, pre-existing) rotation watcher → notifyTransition(ReasonClear)
                           └─ session_transition emitter → interactive conns   [client-observable outcome]
```

The dashed lower half is **pre-existing machinery this ticket does not touch** — drawn only to show where the client-observable break comes from.

## Concurrency model

Unchanged from `interrupt`. No new goroutines, locks, channels, or timers. `handleNewSession` runs on the manager's **single Run dispatch goroutine** — the same goroutine that owns `s.interactive` and runs every other `dispatchAppFrame` handler. The `s.interactive` read is therefore lock-free under the package's single-owner invariant (`:287-294`, `:1246-1250`). `StartNewSession` on the supervisor is safe to call from any goroutine (it is the same sealed `sendModalKey` seam `SendEsc` / `ResolveCancel` use). The handler is synchronous and returns before `dispatchAppFrame` proceeds.

## Error handling

| Failure mode | Behaviour |
|---|---|
| Conn not interactive | Return, no `/clear`. The AC #4 negative path — fail-closed, the security property. |
| `SessionStarter` nil (foreground / pre-wire) | Debug-log `v2.new_session.inert` (`conn_id`), return. No crash, no reply (AC #5). |
| `StartNewSession()` returns error (no live claude / mid-teardown → `ErrNoLiveSession`) | Best-effort: `Warn`-log `v2.new_session.keystroke_err` with the supervisor sentinel + `conn_id`, then return. Nothing to roll back; no reply owed (AC #5). |
| Malformed / duplicate `new_session` frame | A bad outer frame is rejected upstream (`decodeInnerFrameV2` / AEAD) before `dispatchAppFrame`. The probe decode matches `type` only; the frame carries no payload, so there is no payload-decode failure mode. A replayed `new_session` simply drives another `/clear` — starting a fresh session again is harmless; no nonce/dedup needed. |

## Testing strategy

One test file — a new `internal/relay/v2session_newsession_test.go` mirroring `v2session_interrupt_test.go` verbatim (same package, so it reuses `startManager` / `openModalConn` / `sealAppFrameConn` / `v2Recorder` / `genV2Keypair` / the barrier-conn idiom without re-export).

**Fake** — a package-local `fakeSessionStarter` mirroring `fakeInterrupter` (`v2session_interrupt_test.go:18-35`): a mutex-guarded `startCalls int` counter incremented by `StartNewSession()`, plus an injectable `err`. Mutex because the Run goroutine writes and the test goroutine reads.

**Tests** (bullet-pointed scenarios; the developer writes the assertions in the project idiom, copying the interrupt file's structure):

- **Routing by capability (AC #3, AC #4)** — table-driven, mirror `TestV2Session_Interrupt_RoutesEscByCapability`: `{interactive → 1, non-interactive → 0}`. `startManager` with `SessionStarter: fake`; `openModalConn(…, caps)`; send `sealAppFrameConn(send, connID, Envelope{Type: TypeNewSession, TS: …})`; open a barrier conn; assert `fake.startCount() == wantStart`.
- **Nil `SessionStarter` inert (AC #5)** — mirror `TestV2Session_Interrupt_NilInterrupterInert`: `SessionStarter` omitted; an interactive `new_session` is inert (no panic, no hang); the barrier conn opening proves the Run goroutine survived the nil seam.
- **`StartNewSession` error tolerated (AC #5)** — mirror `TestV2Session_Interrupt_SendEscErrorTolerated`: `fake` with `err: errors.New("no live session")`; assert `startCount() == 1` (attempted despite error) and the barrier conn still opens (manager did not crash / close the conn).

**Protocol partition (AC #1, AC #2)** is covered by the three `compat_test.go` edits — `TestTypeConstants_V1V2Partition` and `TestIsV1Compatible` fail if `new_session` is mis-partitioned or leaks into `v1TypeSet`.

Run `go test -race ./internal/protocol/... ./internal/relay/... ./cmd/pyry/...` and `go vet ./...`.

## Open questions

- **Multi-phone / multi-session scoping.** Any interactive paired phone can start a new session on the single live claude — no per-conversation/per-connection binding, consistent with the existing broadcast fan-out model and identical to `interrupt`'s open question. If a future multi-session world needs `new_session` scoped to a specific conversation, that is a later ticket; flagged, not designed here.
- **Confirmation semantics.** `new_session` is fire-and-forget and the client learns of success via `session_transition`. If a client ever needs a synchronous "your `/clear` was accepted" ack (distinct from the rotation marker), that is an additive later ticket — the ACs here deliberately reuse the existing marker with no new ack path.

## Sizing

Held at **S** (no split, no downgrade). Tally against the red lines:

| Red line | This ticket |
|---|---|
| New files | 1 (`v2session_newsession_test.go`) — under 3 |
| Total written LOC | ~15 (codes.go block) + ~8 (interface) + ~6 (config field) + ~3 (intercept case) + ~16 (handler) + ~4 (wiring) + ~6 (3 compat_test edits) + ~120 (test file, mostly copy of 3 fns + fake) + ~15 (doc) ≈ **~195** — far under 600 |
| New exported types/interfaces | 1 (`SessionStarter`) — under 5 |
| Consumer call sites updated simultaneously | 0 — purely additive, unwired-until-`cmd/pyry` |
| Acceptance criteria | 7, but they are facets of one control-verb concern (wire const + partition + interactive-positive + interactive-negative + nil/error + wiring + doc), not seven concerns — matches the `interrupt` precedent (`[[po-v2-control-verb-ac-count-is-facets-not-concerns]]`) |
| Error / reject branches | 3 (non-interactive gate, nil seam, seam error) — not a state machine |

**§4 production-file self-check:** production source files (non-test `.go`) with new/modified content — `internal/protocol/codes.go`, `internal/relay/v2session.go`, `cmd/pyry/relay.go` = **3**. Well under the ≥5 gate.

**Edit fan-out check:** `codegraph_impact` is unnecessary — the change adds a new const, a new interface, a new optional config field, one switch case, one handler, and one wiring line. Zero existing call sites change signature; the `keystrokeFn`/`SessionStarter` seams are additive. No cascade.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The `new_session` frame is untrusted remote input, but it crosses the boundary already-authenticated (AEAD-sealed under the per-session Noise channel; only a paired device that completed the IK handshake can produce a `noise_msg` — an unpaired device is refused with 4401 and never reaches `dispatchAppFrame`). The single new trust decision is the capability gate `if !s.interactive` in `handleNewSession` — one named check, on the Run goroutine, reading the **server-authoritative** `s.interactive` flag. That flag is set fail-closed in the token-OK path from the daemon's `negotiateCapabilities` output (`v2session.go:1246-1250`), never from the phone's raw `capabilities` advertisement, so a spoofed/over-broad advertisement can never flip it. Downstream the handler holds no untrusted data (the frame has no payload; the handler signature takes only `s`, so even an attached payload is never decoded or forwarded).
- **[Tokens, secrets, credentials]** Not applicable — this path mints, stores, compares no token. The pairing token was validated at handshake; `new_session` rides the established session. The frame and the `StartNewSession()` call carry no secret argument.
- **[File operations]** Not applicable — no path, no file I/O on this path. (`/clear` causes claude to rotate its own JSONL, but that is claude's behaviour, observed by the pre-existing rotation watcher, not a file op this code performs.)
- **[Subprocess / external command execution]** No MUST FIX, and this is the finding to state explicitly. The terminal effect is a **fixed `/clear` keystroke sequence** typed into the **already-running** supervised claude via the sealed `supervisor.StartNewSession` → tui-driver seam (#830). No `exec.Command`, no argument construction, no env handling — the subprocess pre-exists. **No attacker-controlled value reaches the PTY:** the `/clear` literal is hard-coded in #830's `sendModalKeystroke`; `handleNewSession` passes no caller data, and the handler never reads the frame's bytes (bare control frame, `type`-only probe). A malicious phone cannot inject arbitrary keystrokes or a different slash-command through this verb.
- **[Cryptographic primitives]** Not applicable — no new crypto. The frame's confidentiality/integrity is the existing Noise AEAD transport, unchanged.
- **[Network & I/O]** No MUST FIX. No new socket, listener, or read loop. The frame is bounded by the existing `maxNoisePayloadBytes` cap at `decodeInnerFrameV2` before it reaches `dispatchAppFrame`; `new_session` adds no unbounded read.
- **[Error messages, logs, telemetry]** No MUST FIX. `handleNewSession` logs only `event`, `conn_id`, and the supervisor error sentinel (`v2.new_session.inert` / `v2.new_session.keystroke_err`). No payload (there is none), no token, no rendered screen, no device secret — consistent with the package's no-secrets-in-logs discipline and AC #5's explicit "never payload bytes or the rendered screen" requirement.
- **[Concurrency]** No MUST FIX. No new goroutine, lock, channel, or timer. `handleNewSession` runs on the single Run dispatch goroutine; the `s.interactive` read is lock-free under the established single-owner invariant. No TOCTOU: read-and-act happen in one synchronous handler on the owning goroutine. A best-effort `StartNewSession` error during teardown is logged and tolerated, leaving no partial state.
- **[Threat model alignment]** Addressed. The relevant question (ADR 025 § Security model) is whether a remote start-new-session is a privileged action. Starting a fresh session is a **higher-blast-radius** action than `interrupt` — it discards the current conversation context — but it is still a normal thing a local user does by typing `/clear`, and it acts only on the requester's **own** paired session within a single trust domain (a user's paired devices). It is therefore correctly **capability-gated (`interactive`)** and **exempt from the per-device permission gate (#702)**, which guards tool-permission *answers*, not session lifecycle. An old / non-interactive phone is inert (the gate + the v1/v2 partition). There is no cross-tenant or cross-user surface: the daemon supervises one live claude for one operator. OUT OF SCOPE: per-conversation `new_session` scoping in a multi-session world (a future ticket — see Open questions); not a vulnerability today.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-08
