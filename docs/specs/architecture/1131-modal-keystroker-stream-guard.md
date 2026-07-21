# Spec #1131 — Nil-safe modal keystroker for stream mode

**Ticket:** [#1131](https://github.com/pyrycode/pyrycode/issues/1131) — Guard the modal-resolution keystroker for stream mode (nil-safe `modalKeystroker`; no panic on remote `modal_cancel`)
**Size:** S (really XS: one composition-root helper + one no-op type + one construction-site change + a guard test; single production caller)
**Labels:** `security-sensitive` — this spec ends with a formal `## Security review` section.

This is the **fourth and final** typed-nil `w.sup` reader in `relay.NewV2SessionManager`'s `V2SessionConfig` to be guarded, mirroring the three landed siblings:

| Verb | Guard | Mechanism |
|------|-------|-----------|
| snapshot (#1101) | `screenSnapshotterOrNil` | nil `*supervisor.Supervisor` → **genuine nil** interface (handler has a nil-check arm) |
| interrupt (#1121) | `activeInterrupter` | re-routed off `w.sup` to the **bound runner** (main.go) |
| new_session (#1125) | `activeSessionStarter` | re-routed off `w.sup` to the **bound runner** (main.go) |
| **modal cancel/timeout (#1131)** | **`modalKeystrokerOrNoop`** (this spec) | nil `*supervisor.Supervisor` → **non-nil no-op** keystroker |

## Files to read first

- `cmd/pyry/relay.go:386-401` — `screenSnapshotterOrNil`: the established typed-nil guard to mirror. Note it takes the **concrete** `*supervisor.Supervisor` (so `== nil` works before boxing) and returns a **genuine nil** interface. This spec's helper takes the same concrete arg but returns a **non-nil no-op** value — see § Design for why the return shape differs.
- `cmd/pyry/relay.go:442-456` — the construction site: `modalReg := modalbridge.New()` then `modalResolver := newModalResolverV2(modalReg, w.sup, logger)`. This is the **only** line that changes in production wiring.
- `cmd/pyry/relay.go:564-569` — the `ModalResolver:` field comment ("`sup (*supervisor.Supervisor)` satisfies `modalKeystroker`…"); update it to note the nil-safe wrap.
- `cmd/pyry/relay.go:192-195` — `relayWiring.sup` is `*supervisor.Supervisor`; typed-nil on the stream-json bootstrap path (#1077).
- `cmd/pyry/modal_resolve_v2.go:24-33` — the `modalKeystroker` interface (`SendEsc`/`Answer`/`AcceptTrust`). The no-op type implements all three.
- `cmd/pyry/modal_resolve_v2.go:126-170` — `ResolveCancel`: consumes the modal via `r.reg.Resolve`, then calls `r.kb.SendEsc()` **unconditionally** as best-effort (a keystroke error is Warn-logged and tolerated). This is why the guard must be a non-nil no-op, not nil.
- `cmd/pyry/modal_resolve_v2.go:172-228` — `ResolveTimeout`: same shape, `denied_timeout`/`timeout` classification; the reachable-in-stream-mode deny-on-timeout path.
- `cmd/pyry/modal_resolve_v2.go:230-332` — `ResolveAnswer`: the **untouched** verdict/keystroke arm. The stream verdict is routed via `streamApprovals.ResolveStream` (relay.go), **not** the keystroker. Read this to confirm the no-op keystroker cannot reach the allow path.
- `cmd/pyry/modal_resolve_v2.go:527-555` — `streamApprovalBridge.ResolveStream`: the **only** path that resolves a permbridge completer to **allow** (and only for `allow==true` from a gated device). Confirms the cancel/timeout paths cannot allow.
- `cmd/pyry/modal_resolve_v2.go:557-599` — `streamApprovalBridge.retire`: the deny-on-timeout backstop. On `modal.Resolve` miss (already consumed by `ResolveCancel`) it returns early — no double-dismissal; the permbridge completer's own #1103 timer is the fail-closed deny.
- `internal/supervisor/modal.go:57-71` — `AcceptTrust`/`Answer`/`SendEsc` all delegate to `sendModalKey`, which dereferences `s` (the PTY driver state). A nil `*Supervisor` receiver panics here — the crash this ticket prevents.
- `cmd/pyry/modal_resolve_v2_test.go:22-124` — existing test scaffolding to reuse: `fakeKeystroker`, `recordPermissionModal`, `recordTrustModal`, `auditLogger`, `auditRecords`. The guard test appends here.
- `cmd/pyry/modal_resolve_v2_test.go:126-260` — `TestModalResolverV2_Cancel_HappyPath` / `_KeystrokeError`: the PTY-path reference behaviour that must stay byte-identical; the guard test mirrors their shape with a typed-nil supervisor.

## Context

The daemon builds one of two interactive runners as its bootstrap session: the terminal-driven `*supervisor.Supervisor` (PTY mode) or the stream-json runner (`internal/streamsup`). In stream mode `Session.Supervisor()` returns a **typed-nil** `*supervisor.Supervisor` (#1077, `internal/sessions/session.go`), and every relay seam that reads `w.sup` as an interface must be typed-nil-safe or it panics on a nil-receiver dereference. Three sibling seams were guarded as their verbs landed; the inbound modal-resolution keystroker is the last one.

`newModalResolverV2(modalReg, w.sup, logger)` (`cmd/pyry/relay.go:454`) passes `w.sup` as the `modalKeystroker`. `modalResolverV2.ResolveCancel` and `ResolveTimeout` call `kb.SendEsc()` **unconditionally**. In stream mode `w.sup` is a typed-nil interface, so `SendEsc()` dereferences a nil `*supervisor.Supervisor` receiver inside `sendModalKey` → **daemon panic**.

**This is reachable, not hypothetical**, once stream mode is selectable. The stream-json approval bridge (#1080, merged) Records stream-json permission prompts into the **same** `modalReg` the resolver consumes; `w.approvals` is constructed unconditionally and its surfacer is installed on the control server. A remote client that receives a stream-json approval `modal_shown` and sends a `modal_cancel` drives `ResolveCancel → SendEsc()` on a typed-nil supervisor → crash. The **answer** path is already safe (it dispatches through `streamApprovals.ResolveStream`, the verdict arm, not the keystroke); **cancel** and **deny-on-timeout** are not.

This must land **dormant-correct in PTY mode** before the config toggle (#1081) makes stream mode selectable — exactly as the three sibling guards did for their verbs. #1131 is a prerequisite for #1081; it is **not** blocked by any open ticket (feature/1081 does not touch these files — verified at architect time).

## Design

### Two new symbols in `cmd/pyry/modal_resolve_v2.go`

Placed next to the `modalKeystroker` interface (the no-op type is that interface's second implementer, and the "keystroke is best-effort / modal already consumed" contract `ResolveCancel`/`ResolveTimeout` document lives here). The construction site in relay.go calls the helper.

**1. `noopKeystroker` — the stream-mode keystroker.** A zero-size struct implementing `modalKeystroker` with three tolerated no-ops. There is genuinely no keystroke to route in stream mode: the stream-json approval is a permbridge-parked completer resolved through the verdict arm, not an on-screen PTY modal. Contract sketch:

```go
// noopKeystroker is the stream-json bootstrap's modal keystroker: there is no PTY
// to dismiss a modal on, so every actuation is a tolerated no-op. The permbridge
// completer's own deny-on-timeout (#1103) is the fail-closed backstop that denies
// the underlying stream-json approval; this type routes NO keystroke and NEVER
// touches the permbridge, so it cannot resolve an approval to allow.
type noopKeystroker struct{}

func (noopKeystroker) SendEsc() error      { return nil }
func (noopKeystroker) Answer(string) error { return nil }
func (noopKeystroker) AcceptTrust() error  { return nil }
```

**2. `modalKeystrokerOrNoop` — the composition-root guard.** Mirrors `screenSnapshotterOrNil`'s shape (concrete `*supervisor.Supervisor` in; interface out) with one deliberate difference in the return, documented inline. Contract sketch:

```go
// modalKeystrokerOrNoop returns sup as the modal keystroker when it is a live
// *supervisor.Supervisor, and a noopKeystroker when sup is nil — the stream-json
// bootstrap path where Session.Supervisor() returns a typed-nil pointer (#1077).
//
// It takes the CONCRETE *supervisor.Supervisor (not modalKeystroker) so the == nil
// test happens BEFORE boxing: assigning the typed-nil straight into the interface
// would leave a non-nil interface holding a nil pointer, and ResolveCancel /
// ResolveTimeout call kb.SendEsc() UNCONDITIONALLY, so that nil pointer would be
// dereferenced → panic. Unlike screenSnapshotterOrNil we must return a NON-NIL
// no-op (not a genuine nil interface): the resolver has no nil-kb arm, and a nil
// interface method call panics just the same. No-op on the PTY path: a non-nil sup
// passes straight through.
func modalKeystrokerOrNoop(sup *supervisor.Supervisor) modalKeystroker {
	if sup == nil {
		return noopKeystroker{}
	}
	return sup
}
```

### The one production wiring change (`cmd/pyry/relay.go:454`)

```go
modalResolver := newModalResolverV2(modalReg, modalKeystrokerOrNoop(w.sup), logger)
```

Update the `ModalResolver:` field comment (relay.go:566) from "`sup (*supervisor.Supervisor)` satisfies `modalKeystroker`" to note that the keystroker is nil-safe-wrapped: PTY passes `w.sup` straight through; stream-json gets a no-op keystroker whose ESC is moot (permbridge timeout denies).

### Why a no-op keystroker, not a bound-runner re-route (unlike #1121/#1125)

Interrupt and new_session were re-routed off `w.sup` to the **active bound runner** because those verbs must reach the conversation's *own* runner (a stream-json runner has a real `Interrupt`/`RestartFresh`). Modal cancel/timeout is different: it is a **PTY-modal dismissal** keystroke. In stream mode there is no on-screen modal to dismiss — the stream-json approval is resolved through the permbridge verdict arm (`ResolveAnswer → ResolveStream`, already wired, #1080), and cancel/timeout deny it via the permbridge completer's own timeout. So the semantically correct stream-mode keystroker is **nothing** — a no-op — not a re-route. This is why the ticket prescribes the `screenSnapshotterOrNil` shape rather than the `activeInterrupter` shape.

### Data flow — remote `modal_cancel` for a stream-json approval, stream mode

```
phone → modal_cancel(modalID) → dispatchAppFrame → ModalResolver.ResolveCancel
  ├─ r.reg.Resolve(modalID)      consume the modalbridge entry (idempotency gate)
  ├─ r.kb.SendEsc()  ── NO-OP ── (was: nil-deref panic; now: returns nil)
  ├─ audit.Log{cancelled, remote}
  └─ return ModalDismissal{cancelled, remote}, true  → relay broadcasts modal_dismissed

meanwhile, the permbridge-parked completer for this approval is untouched by the
above; its #1103 AfterFunc timer fires → Deny → control server's Await returns →
retire(modalID): delete byModal, modal.Resolve(modalID) MISSES (already consumed
above) → retire returns early, no second dismissal. claude receives DENY.
```

Net: the client sees an immediate `{cancelled, remote}` dismissal; claude is denied by the permbridge timeout. Fail-closed, no double-dismissal, no panic. `ResolveTimeout` is the same minus the device (audit `{denied_timeout, timeout}`).

## Concurrency model

Unchanged. `ResolveCancel`/`ResolveTimeout`/`ResolveAnswer` still run on the v2 manager's single Run dispatch goroutine; `retire` and the permbridge timer run on their existing goroutines (control-server handler / `time.AfterFunc`). The no-op keystroker holds no state and takes no lock — it introduces no new goroutine, channel, or lock-ordering edge. `modalKeystrokerOrNoop` runs once at construction on the relay-startup goroutine.

## Error handling

- **Nil-deref panic → non-panic no-op.** The single failure mode this ticket fixes. `noopKeystroker.SendEsc/Answer/AcceptTrust` return `nil`, so the resolver's best-effort actuation takes the success branch (no Warn log — there is no error to log; the no-op is expected, not a failure).
- **Underlying approval still denies fail-closed.** The no-op keystroke leaves the permbridge completer parked; its #1103 deny-on-timeout is the deterministic backstop (belt-and-suspenders: deterministic timer, not a stochastic path). A no-op keystroke can never leave an approval un-denied and can never resolve it to allow.
- **PTY path byte-identical.** A non-nil `w.sup` passes straight through `modalKeystrokerOrNoop`, so `ResolveCancel`/`ResolveTimeout`/`ResolveAnswer` route the real `*supervisor.Supervisor` keystroke exactly as today, including the existing keystroke-error Warn path.

## Testing strategy

Append to `cmd/pyry/modal_resolve_v2_test.go` (reuse `recordPermissionModal`, `recordTrustModal`, `auditLogger`, `auditRecords`, `testDevice`). Bullet scenarios (developer writes them in the table-driven stdlib idiom):

**Helper-level property (mirrors #1101's `got == nil` property test):**
- `modalKeystrokerOrNoop((*supervisor.Supervisor)(nil))` returns a keystroker that is **not** a `*supervisor.Supervisor` (type-assert fails) and whose `SendEsc()`/`Answer("1")`/`AcceptTrust()` each return `nil` without panicking. A naive `return sup` (no guard) fails this: it would box the typed-nil and panic on `SendEsc()`.
- `modalKeystrokerOrNoop(&supervisor.Supervisor{})` returns the **same** `*supervisor.Supervisor` (pass-through arm: type-assert succeeds, pointer identity holds). This is the companion assertion proving the PTY path still routes the real keystroke (AC-4).

**Resolver-level non-panic (AC-1, AC-3, AC-5):** construct `newModalResolverV2(reg, modalKeystrokerOrNoop((*supervisor.Supervisor)(nil)), logger)` with the emit seams left nil, then:
- Record a permission modal → `ResolveCancel(modalID, dev)` returns `(ModalDismissal{cancelled, remote}, true)` and **does not panic**; the modal is consumed (a second `ResolveCancel` returns `(zero, false)`); audit shows exactly one `{cancelled, remote}` record; no keystroke reaches a PTY (proven by construction — the keystroker is `noopKeystroker`, not the supervisor).
- Record a permission modal → `ResolveTimeout(modalID)` returns `(ModalDismissal{denied_timeout, timeout}, true)` and does not panic; one `{denied_timeout, timeout}` audit record.
- (Optional, for parity) a trust-class `ResolveTimeout` with nil emit seams stays a no-op emitter (existing `emitFolderNotTrusted` nil-guard), so no panic on the trust branch either.

**Fail-closed invariant (AC-2, security):** the guard test asserts, at minimum by construction, that neither `ResolveCancel` nor `ResolveTimeout` calls into `streamApprovals` (the resolver is built with `streamApprovals == nil` here and the two methods never reference it) — so no cancel/timeout path can resolve a permbridge completer to **allow**. The existing `ResolveAnswer` gated-verdict tests remain the coverage for the allow path; they are untouched.

**PTY-path regression:** the existing `TestModalResolverV2_Cancel_HappyPath` / `_Timeout_HappyPath` / `_Answer_*` tests (all using `fakeKeystroker`) continue to prove real-keystroke routing; nothing about them changes, since the construction-site wrap is transparent for a non-nil supervisor.

## Open questions

- **Immediate deny-on-cancel (deferred, not required for safety).** Today a cancelled stream-json approval is denied only when the permbridge completer's #1103 timer fires, so claude stays blocked for up to the timeout window after an explicit cancel — a latency degradation, not a safety gap (the permission is never allowed). Making `ResolveCancel` *also* resolve the permbridge completer to deny immediately would require threading a deny-capable seam into `ResolveCancel` and restructuring the resolver, which the ticket explicitly scopes out ("do not restructure `modalResolverV2`… only the keystroker argument changes"). Evidence-Based Fix Selection: no observed failure demands it; the deterministic timeout backstop suffices. Defer to a follow-up if the latency is ever observed to matter.
- **File placement of the two new symbols.** Recommended in `modal_resolve_v2.go` (cohesion with the `modalKeystroker` interface). Placing them in `relay.go` next to `screenSnapshotterOrNil` is equally acceptable; both are package `main`. Developer's minor choice; either keeps the relay.go wiring change to a single line.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary; this spec *removes a crash* at an existing one. The untrusted input is a remote client's `modal_cancel`/`modal_answer` frame crossing network → daemon at `dispatchAppFrame → ModalResolver`. The security-relevant boundary — a remote answer resolving a permission gate — is `ResolveAnswer`'s fail-closed gate (`dev.MayAnswerRemotePermission()` before consume, `devices.AuthorizeRemotePermission` before verdict, `modal_resolve_v2.go:261,293`) and is **untouched**. The guard only changes what the *cancel/timeout* keystroke actuates (a no-op vs a nil-deref panic); cancel/timeout carry no allow decision.
- **[Tokens, secrets, credentials]** N/A — the helper and no-op type hold no state, touch no token/secret, and log nothing. `answerToken` handling in `ResolveAnswer` is unchanged.
- **[Subprocess / external command execution]** N/A — no `exec.Command`; the whole point is that in stream mode there is no PTY subprocess to keystroke, hence the no-op.
- **[Cryptographic primitives]** N/A — no RNG, no comparison, no key. (`streamApprovalBridge.Surface`'s `crypto/rand` modal-id mint is upstream and unchanged.)
- **[Error messages, logs, telemetry]** No findings — `noopKeystroker` returns `nil`, so the resolver's best-effort Warn (`modal_cancel.keystroke_err`) is not emitted in stream mode; no new field, no body/prompt/payload byte reaches any log. The existing content-free audit record (`modal_id`, class, outcome, source, non-secret device identity) is unchanged.
- **[Concurrency]** No findings — the no-op keystroker is stateless and lock-free; introduces no goroutine, channel, or lock-ordering edge. The permbridge completer's deny-on-timeout runs on its existing `time.AfterFunc` (#1103); `retire`'s `modal.Resolve` miss-on-already-consumed is the single-arbiter no-double-dismissal path (unchanged).
- **[Threat model alignment — fail-closed, the centerpiece]** The ticket's core security requirement: a remote `modal_cancel`/timeout in stream mode must leave the underlying stream-json approval to resolve as **deny**, never allow, and must not bypass the gate.
  - *Cannot resolve to allow:* the **only** code path that resolves a permbridge completer to allow is `streamApprovalBridge.ResolveStream(modalID, allow=true, …)` (`modal_resolve_v2.go:547-550`), reached **only** from `ResolveAnswer`'s verdict arm after the gate computes `allow = devices.AuthorizeRemotePermission(dev, outcome)` (`:293,305`). `ResolveCancel`/`ResolveTimeout` never reference `streamApprovals` and the no-op keystroker never touches the permbridge — so no cancel/timeout path can produce an allow. **Verified by reading `ResolveCancel` (`:133-170`), `ResolveTimeout` (`:190-228`), and `ResolveStream` (`:539-555`) end-to-end.**
  - *Stays denied (not un-denied):* consuming the modalbridge entry in `ResolveCancel` does not touch the permbridge completer; the completer's #1103 deny-on-timeout is the deterministic fail-closed backstop, and `retire`'s early return on the already-consumed modal (`:580-583`) prevents a double-dismissal without un-denying. Belt-and-suspenders with different fabric: the safety net is a deterministic timer, not another stochastic keystroke.
  - *Immediate-deny-on-cancel* is OUT OF SCOPE (latency, not safety) — see Open questions; a follow-up ticket, not this one.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
