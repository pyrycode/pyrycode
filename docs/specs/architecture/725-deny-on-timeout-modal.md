# Spec #725 — deny-on-timeout for an unanswered modal

**Ticket:** #725 — feat(bridge): deny-on-timeout for an unanswered modal — safe-deny, dismiss{timeout}, audit
**Size:** S (held — see § Sizing)
**Labels:** `security-sensitive` (security-review pass at end — required)
**Blocked by:** #727 (transitively #726) — both closed.
**Blocks:** #708 (live-wiring capstone).

---

## Files to read first

The developer's turn-1 data load. Read these before writing a line.

- `internal/relay/v2session.go:80-107` — `wakeKind` / `wakeSignal` / `manualRekeyReq`: the per-session timer-callback → `wake` channel → `Run` precedent this slice mirrors with a daemon-global modal channel.
- `internal/relay/v2session.go:599-676` — `Run` select loop, `handleWake`, `armRekeyTimer` / `armRekeyReplyTimer`. **The exact shape to copy:** a `time.AfterFunc` callback does `select { case m.<chan> <- sig: case <-ctx.Done(): }`; `Run` services it on its own goroutine. The new modal-timeout arm/fire reuses this verbatim.
- `internal/relay/v2session.go:366-393` — `ModalDismissal` + `ModalResolver` interface. You add **one** method (`ResolveTimeout`) here.
- `internal/relay/v2session.go:1461-1575` — `handleModalCancel` + `broadcastModalDismissed`. `handleModalTimeout` is a near-copy of `handleModalCancel` (resolve → if ok, broadcast). The broadcast is **unchanged** and reused as-is.
- `internal/relay/v2session.go:555-588` — `NewV2SessionManager`: where the new `modalTimeout` channel is allocated (mirror `wake`/`snapshot`).
- `cmd/pyry/modal_resolve_v2.go:56-100` — `ResolveCancel`: the **template** for `ResolveTimeout` (Resolve one-shot → best-effort keystroke → audit → return dismissal). Copy its structure; swap the keystroke source/outcome.
- `cmd/pyry/modal_resolve_v2.go:18-27` — `modalKeystroker`: already has `SendEsc`/`Answer`/`AcceptTrust`. **No interface change needed** — the ticket's note that it "currently surfaces only SendEsc" is stale (#726/#727 already widened it).
- `cmd/pyry/interactive_modal_v2.go:30-120` — the surfacer (`interactiveModalEmitterV2.Handle`). You add the timeout-arm call after `reg.Record` and a one-method `modalTimeoutArmer` field/param.
- `internal/audit/audit.go:27-61` — `Entry`, `OutcomeDeniedTimeout` (`"denied_timeout"`), `SourceTimeout` (`"timeout"`). The "no-device timeout ⇒ empty `DeviceHash`/`DeviceLabel`" case is documented at line 28.
- `cmd/pyry/relay.go:297-343` — production wiring: `modalReg` daemon-singleton, `mgr.Run(ctx)` under the **daemon ctx**, resolver wired, surfacer **not** wired (deferred to #708). This is why arming off the `Run` goroutine with the daemon ctx is leak-free (§ Concurrency).
- `internal/relay/v2session_modal_test.go:32-72, 96-200, 209-290` — `fakeModalResolver`, `openModalConn`, `waitForResolverCall`, the `modal_dismissed` assertion helper. The relay-side AC-3 test reuses all of these.
- `cmd/pyry/modal_resolve_v2_test.go:83-103, 126-266` — `auditLogger()` / `auditRecords()` capture helpers + the `ResolveCancel` test shapes. The `ResolveTimeout` tests reuse them directly.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:55,117,137,166-182` — the deny-on-timeout policy: "unanswered prompt is answered with the SAFE default (deny / ESC) after a bounded window. Never auto-grant."
- `docs/protocol-mobile.md:655-661` — `modal_dismissed` wire fields: `outcome` is "a producer-defined sentinel for cancel/timeout"; `source` closed set includes `timeout`.

---

## Context

**What problem this solves.** A surfaced permission/trust modal blocks claude until resolved. The remote-modal bridge already lets a phone *answer* (#717 gated arm, deferred) or *cancel* (#727) a modal. But if **no authorized device answers** — phone offline, push missed, user asleep — the modal must not linger forever, and must **never** be silently granted. This slice is the fail-closed safety net: a bounded timeout window elapses with no resolution ⇒ the daemon **safe-denies** the modal (the deny keystroke), broadcasts `modal_dismissed{source: timeout}` to every interactive connection, and audits exactly one `denied_timeout` decision.

**Why now.** It is the last leg of #717's 3-way split (#726 safe-answer seam, #727 dismiss/broadcast/interception, #725 deny-on-timeout). It blocks #708, the live-wiring capstone. It reuses machinery already built — it introduces **no new wire type, no new package, no new crypto**.

**Production-inert until #708.** Nothing `Record`s a modal in production until #708 live-wires the surfacer into the PTY event stream, so no timer arms in production today (the same harmless state as #727's interception). The scripted AC-3 tests are the proof of correctness; **no live two-phone e2e is required here** (that is #708's capstone).

---

## Design

Three production files change. No new files, no new wire types, no new exported types.

### The shape in one sentence

The surfacer **arms** a daemon-global timeout when it records a modal; the timer's `AfterFunc` callback **funnels** the modal_id onto the manager's `Run` goroutine; `Run` **fires** the safe-deny by calling a new `ResolveTimeout` resolver method (one-shot consume → ESC keystroke → `denied_timeout` audit) and then **broadcasts** `modal_dismissed{timeout}` — reusing the existing `broadcastModalDismissed` unchanged. Exactly-once is guaranteed structurally because answer-resolution and timeout-resolution are **both** `Run`-goroutine arms of the same `select`, gated by the registry's one-shot `Resolve`.

### Data flow

```
SURFACE (producer goroutine, cmd/pyry — live in #708)
  interactiveModalEmitterV2.Handle
    reg.Record(req, class)            -> mints modal_id, records Outstanding
    armer.ArmModalTimeout(ctx, id)    -> time.AfterFunc(modalDenyTimeout, cb)   [NEW]
    bcast.Push(modal_shown) per conn  (unchanged)

FIRE (timer callback goroutine -> Run goroutine, internal/relay)
  AfterFunc cb: select { case m.modalTimeout <- id: case <-ctx.Done(): }        [NEW]
  Run select arm: case id := <-m.modalTimeout: m.handleModalTimeout(runCtx, id) [NEW]
  handleModalTimeout:
    d, ok := cfg.ModalResolver.ResolveTimeout(id)   [NEW resolver method]
    if !ok { return }                               // already answered/cancelled: no-op
    m.broadcastModalDismissed(runCtx, id, d)        // UNCHANGED (#727)

RESOLVE-TIMEOUT (Run goroutine, cmd/pyry resolver)
  modalResolverV2.ResolveTimeout(id):               [NEW, mirrors ResolveCancel]
    out, ok := reg.Resolve(id)                       // one-shot consume = idempotency gate
    if !ok { return zero, false }                    // AC-2 loser path
    _ = kb.SendEsc()                                 // safe-deny, best-effort
    audit.Log({modal_id, out.Class, OutcomeDeniedTimeout, SourceTimeout})  // empty device
    return {Outcome:"denied_timeout", Source:"timeout"}, true
```

### Package: `internal/relay` (`v2session.go`)

**New package var** (mirrors `rekeyReplyTimeout` at line 62 — lowercase, test-overridable via save/restore, not public API):

```go
// modalDenyTimeout is the bounded window between a modal being surfaced and the
// fail-closed safe-deny. Test-overridable. See spec § Open questions for the
// default-value policy decision.
var modalDenyTimeout = 2 * time.Minute
```

**New field on `V2SessionManager`** (allocate in `NewV2SessionManager` alongside `wake`/`snapshot`; reuse `wakeBufferSize` = 16 for the capacity, same rare-concurrent-fire reasoning):

```go
// modalTimeout carries a surfaced modal's id from its AfterFunc callback
// goroutine to the Run goroutine on deny-on-timeout. Daemon-global (a modal is
// not bound to one conn). Mirrors `wake` but keyed by modal_id, not *V2Session.
modalTimeout chan string
```

**New `Run` select arm** (one line, beside `case w := <-m.wake:`):

```go
case modalID := <-m.modalTimeout:
    m.handleModalTimeout(runCtx, modalID)
```

**New method `ArmModalTimeout(ctx context.Context, modalID string)`** — public seam the surfacer calls. Behavior: `time.AfterFunc(modalDenyTimeout, cb)` where `cb` does the blocking-send-with-ctx-escape onto `m.modalTimeout` (identical to `armRekeyTimer`'s callback). **The `*time.Timer` is deliberately discarded — not stored, never `Stop`ped** (§ Concurrency explains why the one-shot `Resolve` makes timer cancellation unnecessary, and why an un-stopped `AfterFunc` is heap-only with no parked goroutine until it fires).

**New method `handleModalTimeout(ctx context.Context, modalID string)`** — runs on `Run`. A near-copy of `handleModalCancel` (lines 1476-1493): nil-resolver guard (debug-log inert, return) → `d, ok := m.cfg.ModalResolver.ResolveTimeout(modalID)` → `if !ok { return }` → `m.broadcastModalDismissed(ctx, modalID, d)`.

**Extend `ModalResolver` interface** (one method added beside `ResolveCancel`/`ResolveAnswer`):

```go
// ResolveTimeout safe-denies an unanswered modal: consumes modalID (registry
// Resolve), routes the fail-closed deny keystroke, audits outcome=denied_timeout
// / source=timeout with an empty device (no answering device), and returns the
// dismissal to broadcast with ok=true. An already-resolved id ⇒ (zero, false):
// no keystroke, no audit, no broadcast (the AC-2 loser path).
ResolveTimeout(modalID string) (ModalDismissal, bool)
```

> Edit fan-out from this interface change: **2 impls** — the production `modalResolverV2` (cmd/pyry, below) and the relay-side `fakeModalResolver` (test). Well under the 10-call-site line.

### Package: `cmd/pyry` (`modal_resolve_v2.go`)

**New method `ResolveTimeout(modalID string) (relay.ModalDismissal, bool)`** on `modalResolverV2`. Structurally identical to `ResolveCancel` (lines 63-100), with three differences:

1. **No `dev` param.** A timeout has no answering device → `audit.Entry.DeviceHash`/`DeviceLabel` are empty (the documented no-device-timeout case, `audit.go:28`).
2. **Outcome/Source** = `audit.OutcomeDeniedTimeout` / `audit.SourceTimeout` (not `Cancelled`/`Remote`).
3. **Wire dismissal** = `{Outcome: string(audit.OutcomeDeniedTimeout), Source: string(audit.SourceTimeout)}` ⇒ `{"denied_timeout", "timeout"}` — the same "one source vocabulary feeds both wire dismissal and audit" pattern `ResolveCancel` documents.

The keystroke and the best-effort discipline are **identical to `ResolveCancel`**: `r.kb.SendEsc()`, errors warn-logged and tolerated (the modal is already consumed and moot — the dismissal must still broadcast and the audit must still be written; aborting would orphan a consumed modal). See § "Safe-deny keystroke choice".

### Package: `cmd/pyry` (`interactive_modal_v2.go`)

**New one-method consumer-side interface** (defined here, beside the surfacer; `*relay.V2SessionManager` satisfies it structurally — do **not** widen the shared `interactiveBroadcaster`, which three emitters implement):

```go
// modalTimeoutArmer arms the daemon-side deny-on-timeout for a surfaced modal.
// *relay.V2SessionManager satisfies it; #708 wires the live manager here.
type modalTimeoutArmer interface {
    ArmModalTimeout(ctx context.Context, modalID string)
}
```

**New `armer modalTimeoutArmer` field + constructor param** on `interactiveModalEmitterV2`. **Arm call in `Handle`** — placed **immediately after a successful `reg.Record`** (before the payload marshal + broadcast loop), so a modal that records but fails to marshal/broadcast still gets a pending safe-deny, and a modal surfaced with **zero** connected interactive conns is still safe-denied on timeout (the core fail-closed guarantee — see § Error handling):

```go
payload, err := e.reg.Record(req, class)
if err != nil { /* existing rand-failure drop, unchanged */ return }
e.armer.ArmModalTimeout(ctx, payload.ModalID)   // [NEW] arm before broadcast
// ... existing marshal + broadcast loop, unchanged ...
```

### Safe-deny keystroke choice — the architect's call

**ESC (`SendEsc`) for both classes.** Rationale:

- For a **permission** modal, ESC dismisses the prompt — claude treats it as a deny/cancel. This is the exact keystroke #727's `ResolveCancel` already routes; cancel proved ESC is a valid resolution of a permission modal.
- For a **trust** modal, the deny option is `exit`, which `classifyAnswer` (modal_resolve_v2.go:227-228) already maps to `verbEsc`. So ESC = "exit" = deny.

ESC is therefore the uniform, **layout-independent** fail-closed deny for both classes, requires **no** `modalKeystroker` change (it already has `SendEsc`), and matches the keystroke cancel uses — only the audit classification differs (`cancelled`/`remote` vs `denied_timeout`/`timeout`).

*Considered and rejected:* routing the explicit reject digit (`Answer("3")`/`("4")`) for permission modals. It depends on the option's 1-based position in claude's render and adds a class-conditional branch with no security benefit over ESC. ESC is strictly simpler and strictly safe.

---

## Concurrency model

The crux the ticket flags ("the design wrinkle is the timer lifecycle"). Three goroutines touch this path; here is who owns what.

**1. The surfacer goroutine (producer, cmd/pyry — live in #708).** Calls `reg.Record` (registry mutex) and `ArmModalTimeout`. `ArmModalTimeout` only calls `time.AfterFunc` — it touches **no** `Run`-owned state and stores nothing, so it is safe to call off the `Run` goroutine with zero new synchronization.

**2. The `AfterFunc` callback goroutine.** Spawned by the runtime only **when the timer fires** (an un-fired `AfterFunc` is a heap entry, not a parked goroutine — so "not stopping the timer" leaks nothing). The callback does a single `select { case m.modalTimeout <- modalID: case <-ctx.Done(): }` and exits. The `ctx` is the surfacer's ctx, which in production **is the daemon ctx** (`cmd/pyry/relay.go:340` runs `mgr.Run(ctx)` under the same daemon ctx; #708 feeds the surfacer under that same ctx). So the callback's escape arm fires at exactly the moment `Run` stops — leak-free, equivalent in practice to `armRekeyTimer`'s `runCtx`. (`m.modalTimeout` is buffered at 16, so a callback almost never blocks even momentarily.)

**3. The `Run` goroutine (internal/relay).** Owns `m.sessions` and the resolution. `handleModalTimeout` and `broadcastModalDismissed` both run here. `ResolveTimeout` (called from `handleModalTimeout`) touches only independently-safe surfaces: `reg.Resolve` (registry mutex), `kb.SendEsc` (supervisor capture-then-release, safe from any goroutine), `audit.Log` (slog). So no second concurrent writer to any state.

**Exactly-once is structural (AC-2).** The decisive observation: **answer-resolution and timeout-resolution are both arms of the same `Run` `select`.** A `modal_answer`/`modal_cancel` arrives via `m.cfg.Frames` → `handleFrame` → `dispatchAppFrame` → `handleModalAnswer`/`handleModalCancel` (on `Run`). A timeout arrives via `m.modalTimeout` → `handleModalTimeout` (on `Run`). A `select` services **one arm at a time to completion**, so the two never run concurrently — whichever `Run` services first calls `reg.Resolve` and consumes the modal; the other sees `ok == false` and takes the no-op path (no keystroke, no audit, no broadcast). The registry's one-shot `Resolve` (delete-on-resolve under mutex) is the idempotency gate; the `Run` serialization means there is no actual answer-vs-timeout data race to lose. The registry mutex still matters — it guards `Record` (surfacer goroutine) against `Resolve` (`Run`).

**Why no timer tracking / `Stop` on resolve.** Cancelling the timer when an answer wins would require a `map[modalID]*time.Timer` mutated by both the surfacer (arm) and `Run` (cancel) — new cross-goroutine state and a new lock — for **zero** correctness gain: an un-stopped timer that fires after the modal was already answered simply runs `handleModalTimeout` → `ResolveTimeout` → `reg.Resolve` miss → no-op. The cost is one `Run` iteration + one registry lock per already-resolved modal, after the window. That is strictly cheaper and simpler than tracking timers, and the `AfterFunc` holds no goroutine until it fires. **Decision: do not track or stop timers.**

**Goroutine lifecycle.** Every `AfterFunc` callback exits after one `select` (delivery or ctx-cancel). No goroutine outlives `Run` (its escape arm is the daemon ctx, cancelled at shutdown). No leak.

---

## Error handling

| Failure mode | Behavior |
|---|---|
| Timeout fires but modal already answered/cancelled | `reg.Resolve` ⇒ `ok=false`. No keystroke, no audit, no broadcast. The intended AC-2 loser path. |
| `nil` `ModalResolver` (foreground / pre-#708) | `handleModalTimeout` debug-logs "inert; no resolver wired" and returns — mirrors `handleModalCancel`'s nil guard. |
| `SendEsc` keystroke error (no live session / teardown) | Warn-logged with the supervisor sentinel and **tolerated**. The modal is already consumed (idempotency committed) and moot — the dismissal must still broadcast and the audit must still be written. Aborting would orphan a consumed modal. (Identical to `ResolveCancel`.) |
| Modal surfaced with **zero** interactive conns connected | Timer still armed (arm is unconditional on `Record`, independent of the broadcast loop). On fire: claude is safe-denied (ESC) and audited; the broadcast fans to zero conns (no-op). This is the **core fail-closed guarantee** — claude is blocked on the modal regardless of who is watching, so it must be denied if nothing resolves it. |
| `Record` succeeds but payload marshal fails (existing drop path) | Modal is already recorded + a timeout is already armed (arm precedes marshal), so the outstanding modal is still safe-denied on the window — no eternal-lingering modal. |
| `Run` exits (daemon shutdown) with a timer pending | The `AfterFunc` callback's `<-ctx.Done()` arm fires (daemon ctx cancelled), callback exits, no delivery, no leak. The modal is moot — the relay connection is gone. |

---

## Testing strategy

AC-3's three assertions (safe-deny keystroke, `modal_dismissed{timeout}` broadcast, single `denied_timeout` audit) span two packages by the same seam-split #727 established: keystroke + audit live in the **cmd/pyry resolver**; the broadcast lives in the **relay manager**. So AC-3 is satisfied by the combination of the tests below (each scripted, deterministic, no live e2e). Tests are **scenarios**, not code — write them in the project idiom, reusing the cited helpers.

### `cmd/pyry/modal_resolve_v2_test.go` — `ResolveTimeout` (real registry + `fakeKeystroker` + audit capture)

Reuse `auditLogger()` / `auditRecords()` (lines 83-103) and the `ResolveCancel` test shapes (lines 126-266).

- **`ResolveTimeout` safe-denies + audits once.** Record a modal (real `modalbridge.Registry`) → `ResolveTimeout(id)` ⇒ returns `{"denied_timeout","timeout"}, true`; `fakeKeystroker` recorded exactly one `SendEsc` (and nothing else); exactly one audit record with `outcome=denied_timeout`, `source=timeout`, empty `device_hash`/`device_label`, `modal_class` = the recorded class, and **no modal body** in any field. (Covers AC-1 keystroke + audit, AC-3.)
- **Already-consumed ⇒ no-op (AC-2 loser path).** Record → consume via `ResolveCancel` (or `ResolveAnswer`) → `ResolveTimeout(id)` ⇒ `ok=false`; `fakeKeystroker.routedNothing()`; **zero** audit records. Proves the answer-before-timeout case writes no `denied_timeout` and routes no keystroke.
- **Keystroke error tolerated.** `fakeKeystroker{err: supervisor.ErrNoLiveSession}` → `ResolveTimeout` still returns `ok=true` and writes exactly one audit record (mirrors the `ResolveCancel` keystroke-error test at lines 243-266).
- **Unknown id ⇒ no-op.** `ResolveTimeout("nonexistent")` ⇒ `ok=false`, no keystroke, no audit.

### `internal/relay/v2session_modal_test.go` — manager arm → fire → broadcast

Extend `fakeModalResolver` (lines 32-72) with `ResolveTimeout` (record calls; return a canned dismissal `{"denied_timeout","timeout"}` for a configured `timeoutOKFor` id, else the no-op). Reuse `openModalConn`, `waitForResolverCall`, the `modal_dismissed` assertion helper. Shrink `modalDenyTimeout` to a sub-second value via save/restore (mirror the `rekeyReplyTimeout` test idiom).

- **Arm → fire → safe-deny broadcast (AC-1, AC-3 broadcast).** Stand up ≥1 interactive conn via `openModalConn`. Call `mgr.ArmModalTimeout(ctx, id)`. After the shrunk window: `fakeModalResolver.ResolveTimeout` called **exactly once** with `id`; each interactive conn received **exactly one** `modal_dismissed` carrying `outcome=denied_timeout`, `source=timeout`, `modal_id=id`. Assert a **non-interactive** conn (if stood up) receives nothing (capability gate).
- **Timeout no-ops when already resolved (AC-2 no second dismissal).** `fakeModalResolver` returns `ok=false` for the armed id (simulating an answer that already consumed it). After the window: `ResolveTimeout` called once, **no** `modal_dismissed` pushed to any conn.
- **Nil resolver is inert.** Manager with `ModalResolver: nil`, `ArmModalTimeout` → window elapses → no panic, no broadcast (mirrors `TestV2Session_ModalControl_NilResolver`).

### `cmd/pyry/interactive_modal_v2_test.go` — surfacer arms on surface

Add a `fakeArmer` (records `ArmModalTimeout` calls). Update the two construction sites (`newModalEmitterTestDeps` line ~23 and line ~168) to pass it.

- **Handle arms a timeout for the surfaced modal_id.** Drive a permission `EventKindPtyModalShown` through `Handle` → `fakeArmer` recorded exactly one `ArmModalTimeout` whose `modalID` equals the surfaced modal's id (recover the id from the recorded `modal_shown` push, as the existing fan-out tests do).
- **Non-permission / non-modal events arm nothing.** The existing `NonPermissionClass_NoOp` / `NonModalEvent_NoOp` scenarios additionally assert `fakeArmer` recorded zero calls.

### Whole-suite

`go test -race ./...` green; `go vet ./...`; `staticcheck ./...`. The `-race` run is the real proof the off-`Run` arm + on-`Run` fire have no data race.

---

## Sizing

**Held at S** (the ticket authorizes S→XS "if the lifecycle proves thin"; it does not — the cross-goroutine timer reconciliation, two interface/seam extensions, and the multi-package test matrix justify S).

Red-line check (all clear):

- **New files:** 0 (all edits to existing files). ✓
- **Total LOC:** production ~90-110 (relay var+field+arm+2 methods+iface line ≈ 55; resolver `ResolveTimeout` ≈ 30; surfacer iface+field+param+call ≈ 15) + tests ~230 + spec. ~350-450 total < 600. ✓
- **New exported types/interfaces:** 0 new exported *types*; the additions are one method on the existing exported `ModalResolver` interface and one exported method `ArmModalTimeout`. New `modalTimeoutArmer` is unexported. < 5. ✓
- **Consumer call sites updated simultaneously:** `ResolveTimeout` → 2 impls; `modalTimeoutArmer` → 1 prod impl (structural) + 1 fake + 2 test construction sites ≈ 4. < 10. ✓
- **Acceptance criteria:** 3. < 5. ✓
- **Distinct reject/error branches in the timer state machine:** `handleModalTimeout` (nil-resolver, resolve-ok, resolve-not-ok) + `ResolveTimeout` (resolve-miss, keystroke-err-tolerated) ≈ 5. < 10. ✓

§4 production-source-file count (`.go`, non-test): `internal/relay/v2session.go`, `cmd/pyry/modal_resolve_v2.go`, `cmd/pyry/interactive_modal_v2.go` = **3**. < 5. ✓

---

## Open questions

1. **The timeout-window default value.** ADR 025 specifies "a bounded window" but no number. This spec proposes `modalDenyTimeout = 2 * time.Minute` as a balance between "long enough for a human to react to a push notification and tap" and "short enough not to leave claude blocked." It is a **policy knob**, not load-bearing for this slice (tests shrink it). Resolve at implementation/operator-review time; making it config-driven (vs. the package var) is a deferred concern for #708 or a follow-up — out of scope here.
2. **`ResolveTimeout` no `conversation_id`.** `modal_dismissed` carries only `modal_id` + `outcome` + `source` (the protocol's opaque-nonce-is-the-sole-key contract). No change needed; noted so the developer does not try to thread a conversation id.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[1. Trust boundaries]** No MUST FIX. The one untrusted input that reaches this path is `modal_id` on an inbound `modal_answer`/`modal_cancel` — but that is consumed by #727's existing `handleModalCancel`/`handleModalAnswer`, **not** by this slice. The deny-on-timeout path's `modalID` originates **internally**: minted by `reg.Record` (crypto/rand nonce, `modalbridge`), passed verbatim through `ArmModalTimeout` → `m.modalTimeout` → `ResolveTimeout`. No network bytes cross into the timeout path. The boundary is explicit and inbound-only, and this slice does not touch it.
- **[2. Tokens, secrets, credentials]** No findings. `ResolveTimeout` takes **no device**, so the audit `DeviceHash`/`DeviceLabel` are empty by construction — the no-device-timeout case `audit.Entry` documents (`audit.go:28`). No token, key, or secret is read, compared, or logged anywhere on this path. The `modal_id` is an opaque correlation nonce, not a credential.
- **[3. File operations]** N/A — no filesystem path is constructed, opened, or written. The audit sink is `log/slog` (write-only, in-process; `audit.go` SECURITY note).
- **[4. Subprocess / external command execution]** N/A — no `exec`. The only "external" actuation is `kb.SendEsc()` (a single non-blocking PTY write through the supervisor capture-then-release seam, #726). It carries no user-controlled bytes — ESC is a fixed keystroke.
- **[5. Cryptographic primitives]** N/A — this slice introduces no crypto. The `modal_id` nonce is minted by `modalbridge` (crypto/rand, unchanged). No new key, nonce, or comparison.
- **[6. Network & I/O]** No findings. This slice emits no new wire type and adds no new inbound parse. `broadcastModalDismissed` (reused unchanged) marshals a closed three-string `ModalDismissedPayload`; the un-droppable-control-frame size concern is already bounded upstream (`modalbridge.maxPromptBytes`, and the dismissal carries no body). `m.modalTimeout` is bounded (cap 16); a flood of timeouts cannot exhaust memory.
- **[7. Error messages, logs, telemetry]** No findings. Every log line on this path carries only content-free discriminants — `event`, `modal_id` (opaque nonce), `modal_class` (closed wire set), and the supervisor sentinel `err`. **No modal body/prompt/title and no payload bytes are logged** (the `modalResolverV2` SECURITY contract, extended verbatim to `ResolveTimeout`). The audit entry carries only non-secret identity (here: empty device) + opaque `modal_id` + outcome/source — and `internal/audit`'s no-leak test (`audit_test.go:156`) is a deterministic backstop against a future field that could hold a secret.
- **[8. Concurrency]** No MUST FIX — and this is the load-bearing category for a security-sensitive timer. **Exactly-once safe-deny is structural** (§ Concurrency): answer-resolution and timeout-resolution are both `Run`-goroutine `select` arms, serialized, gated by the registry's one-shot `Resolve` — so a race cannot double-deny, double-broadcast, or double-audit. **Fail-closed under every failure** (§ Error handling): keystroke error ⇒ still consumed + audited + broadcast; zero connected conns ⇒ still denied; marshal-failure drop ⇒ still armed; `Run` shutdown ⇒ callback escapes via daemon ctx, no leak. The default for any surfaced modal is DENY by construction — the timeout leg **only ever drives the deny keystroke**, never a grant. No lock-ordering risk: the registry mutex is a leaf lock taken alone (`modalbridge.go:90`); this slice adds no second lock (the deliberate "no timer-tracking map" decision in § Concurrency avoids introducing one). No goroutine leak: every `AfterFunc` callback exits after one `select`.
- **[9. Threat model alignment]** Addresses ADR 025 § Security model's **deny-on-timeout** invariant directly: "An unanswered prompt is answered with the SAFE default (deny / ESC) after a bounded window. Never auto-grant." (`025-...md:137`). The "network reorder replays an answer onto a later modal" threat is handled by the one-shot nonce `Resolve` (a replayed/late answer after a timeout consumed the modal misses ⇒ no second action). The per-device answer gate (#717/#702) is **correctly not consulted here** — a timeout has no answering device to gate, and the action is unconditionally DENY, so there is nothing to authorize. The live two-phone e2e validation of the end-to-end path is **out of scope**, deferred to **#708** (the capstone this ticket blocks), as the ticket states.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-23
