# Spec: ACP permission proxy via `session/request_permission` (#752)

**Epic #600 — `pyry acp` as a thin adapter. T8 (divergence 2).** Ticket #752.
**security-sensitive** — the highest-risk seam in the epic. Security review pass is at the end of this spec (verdict: PASS).

## Context

When claude hits a tool that needs approval, the supervised interactive session raises an on-screen permission modal and blocks. ACP models a permission decision as a *blocking agent→client call* (`session/request_permission`): pyry calls the host, blocks until the host answers, then routes the host's choice back into claude's on-screen prompt as the correct keystroke. That keystroke path is the interactive path — this ticket introduces **no** `claude -p` and **no** Agent SDK permission mechanism (hard cost invariant, ADR 027 divergence 5).

The permission modal is a **tui-driver PTY-state event** (`tuidriver.EventKindPtyModalShown` / `…Hidden`), **not** on the `turnevent.Event` stream that T6/T7 consume — `turnbridge.mapEvent` (`internal/turnbridge/mapper.go:20-38`) drops modal kinds to `(nil, false)`. So this adapter takes its **own** modal-event subscription off the live session. `tuidriver.Session.Events(...)` mints a fresh channel + merge goroutine per call (`pkg/tuidriver/events.go:130-170`), so that own subscription does not contend with T6/T7's turn-event subscription.

**Scope of this ticket = the adapter, unwired, unit-tested in isolation** — mirroring how `interactiveModalEmitterV2` (`cmd/pyry/interactive_modal_v2.go`) ships as a passive event-driven unit whose live wiring is a separate deferred ticket (#708). The live subscription that drives this adapter from `Session.Events()` in the `pyry acp` composition root belongs to T7 (#751) / the modal-event wiring follow-up (see § Deferred wiring). No existing file is modified; the adapter is two new files.

## Files to read first

- `cmd/pyry/modal_resolve_v2.go:23-27, 255-300` — `modalKeystroker` interface (reuse it) + `classifyAnswer` / `routeAnswerKeystroke` — the exact optionId→(verb, 1-based digit) mapping template. `*supervisor.Supervisor` satisfies `modalKeystroker`.
- `internal/supervisor/modal.go:52-91` — the keystroke seam contract: `Answer(choice)` sends `choice+"\r"`, `SendEsc()` sends ESC; `ErrNoLiveSession` (wrapped) when no child, **nothing written** in that case; safe from any goroutine.
- `internal/acp/acp.go:260-305` — `Transport.Call(ctx, method, params) (json.RawMessage, error)`: blocking outbound request; **MUST be issued off the Serve read loop** (its own doc: a Call on that goroutine deadlocks); ctx cancel reclaims the pending slot; a reply for a reclaimed/unknown id is dropped (transport-owned correlation).
- `internal/modalbridge/modal.go:100-136` — `PermissionRequestForClass(class, screenText) (turnevent.PermissionRequest, wireClass string, ok bool)` — reuse for the option set: options carry the four `PermissionOptionKind`s in claude's display (allow-first) order.
- `internal/turnevent/permission.go:12-38` — `PermissionRequest` / `PermissionOption{ID, Label, Kind}`; `internal/turnevent/taxonomy.go:45-54` — the four `PermissionOptionKind` string values (they **are** the ACP wire strings).
- `cmd/pyry/acp_turn_stream.go` (whole file) — the sibling outbound adapter: consumer owns its own params-wrapper types locally (not in `acpbridge`); content-free logging discipline; `nil`-tolerant seam pattern.
- `cmd/pyry/interactive_modal_v2.go:80-212` — the mobile modal surfacer: the Shown/Hidden `Handle` dispatch shape and the first-answer-wins one-shot via `Registry.Resolve`. This adapter is the ACP analogue but far thinner (blocking Call in place of broadcast+registry).
- `internal/acp/notify.go` — `Transport.Notify` (sibling of `Call`) shows the marshal-first / write-under-`writeMu` pattern; **not used here** (we use `Call`), read only to confirm the transport seam idioms.
- `cmd/pyry/acp.go:106-130` — the composition root where the deferred wiring will register/drive this adapter (context only; not edited by this ticket).
- `docs/knowledge/decisions/027-acp-mapping.md:42-88` — the authoritative outbound row: `PermissionRequest → session/request_permission` (divergence 2); response is `outcome: "selected"` + `optionId` or `outcome: "cancelled"`; `ToolCallID` is empty from the modal-event path.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:134-186` — the default-safe security model: unanswered/errored/cancelled ⇒ deny/ESC, never a silent grant; one-shot replay-safe.

## Design

### Placement

Two new files in `cmd/pyry` (package `main`), alongside `acp_turn_stream.go` and `interactive_modal_v2.go`:

- `cmd/pyry/acp_permission.go` — the adapter + local ACP wire types.
- `cmd/pyry/acp_permission_test.go` — unit tests.

No new package, no new **exported** type (all types unexported, matching `acpTurnStream` / `interactiveModalEmitterV2`).

### Seams (interfaces at the consumer)

```go
// permissionCaller is the outbound-request seam; *acp.Transport satisfies it.
type permissionCaller interface {
    Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}
```

Reuse the existing `modalKeystroker` (`cmd/pyry/modal_resolve_v2.go:23-27`) for keystroke routing — the adapter uses only `Answer` and `SendEsc` (see § Trust class for why `AcceptTrust` is unused). `*supervisor.Supervisor` satisfies it.

### The adapter type

```go
type acpPermissionProxy struct {
    caller    permissionCaller
    kb        modalKeystroker
    sessionID string           // the ACP session id (from pool.Create; a constructor param — adapter is unwired)
    timeout   time.Duration    // Call deadline; the AC-3 "timeout ⇒ deny" source
    logger    *slog.Logger

    // inflight is the single outstanding round-trip, or nil. Touched ONLY by the
    // drain goroutine (Handle), never by the round-trip goroutine — so no lock.
    inflight  *permissionRoundTrip
}
```

Constructor: `newACPPermissionProxy(caller, kb, sessionID, timeout, logger) *acpPermissionProxy`.

`Handle(ctx context.Context, ev tuidriver.Event)` is the event sink — the deferred wiring drains `Session.Events()` and calls it serially on one goroutine (mirrors `interactiveModalEmitterV2.Handle`). It dispatches:

- `EventKindPtyModalShown` **and** `ev.Modal == tuidriver.ModalClassPermission` → `handleShown` (start a round-trip).
- `EventKindPtyModalHidden` → `handleHidden` (retire the outstanding round-trip).
- everything else (including a non-permission `ModalShown`) → no-op.

`Handle` does **not** take `screenText`: ACP's `session/request_permission` carries no prompt body, and the option set is class-fixed, so the rendered screen text is unused (contrast the mobile surfacer, which sends the body). This keeps the deferred wiring from having to source `Supervisor.ScreenSnapshot`.

### The round-trip

```go
type permissionRoundTrip struct {
    resolved atomic.Bool          // one-shot arbiter; CompareAndSwap(false,true) wins exactly once
    cancel   context.CancelFunc   // cancels the Call ctx on retirement (set before the goroutine is spawned)
    options  []turnevent.PermissionOption // the surfaced set; membership + index→digit source of truth
}
```

`handleShown` (on the drain goroutine):

1. `req, _, ok := modalbridge.PermissionRequestForClass(ev.Modal, "")` — reuse for the four-option set. (`screenText` "" ⇒ `req.Title` empty, unused.) `ok` is always true here (gated on `ModalClassPermission`).
2. If `inflight != nil`, retire it first (defensive; tui-driver's single-modal invariant means this is normally already nil).
3. Build the ACP params (below). Create `cctx, cancel := context.WithTimeout(ctx, timeout)`; construct the `permissionRoundTrip{cancel, options: req.Options}`; store as `inflight`.
4. Spawn one goroutine: `go p.runRoundTrip(cctx, rt, params)`. **This is the "off the read loop" guarantee** (AC-5): the Call runs on a goroutine spawned from the drain goroutine, itself distinct from the transport Serve read loop.

`runRoundTrip(cctx, rt, params)` (the round-trip goroutine) — the resolution is a single one-shot claim, then a default-safe switch:

```go
resp, err := p.caller.Call(cctx, methodSessionRequestPermission, params)
if !rt.resolved.CompareAndSwap(false, true) {
    return // retirement (Hidden) already claimed this round-trip → route nothing
}
// this goroutine now owns the sole resolution:
//   err != nil (timeout / transport / teardown-cancel) → deny (SendEsc)
//   outcome "selected" + optionId maps to a surfaced option → Answer(digit)
//   anything else (cancelled / forged optionId / decode failure) → deny (SendEsc)
```

`handleHidden` (on the drain goroutine) retires the outstanding round-trip:

```go
rt := p.inflight
p.inflight = nil
if rt != nil && rt.resolved.CompareAndSwap(false, true) {
    rt.cancel() // we won the one-shot: unblock the in-flight Call; its goroutine sees
                // the failed CompareAndSwap and routes nothing (the modal is already gone)
}
```

The `atomic.Bool` one-shot is the sole arbiter across the two goroutines. `inflight` is drain-goroutine-only; the round-trip goroutine touches only `rt` (its own pointer), the atomic, and the (goroutine-safe) `caller`/`kb`. **No mutex is needed.** See § Concurrency model for the race analysis.

### Resolution decision table (after the round-trip goroutine wins the one-shot)

| Call outcome | Keystroke routed | Rationale |
|---|---|---|
| `outcome:"selected"`, `optionId` ∈ surfaced options | `Answer(digit)` — `digit` = 1-based index in display order | the host's choice; allow-or-reject is whichever option the host picked |
| `outcome:"selected"`, `optionId` **not** a surfaced option | `SendEsc()` (deny) | forged / wrong-class option id → safe default |
| `outcome:"cancelled"` | `SendEsc()` (deny) | ACP cancellation ⇒ dismiss |
| response fails to decode | `SendEsc()` (deny) | any decode ambiguity fails safe (never a grant) |
| `err != nil` (timeout / transport error / teardown-cancel) | `SendEsc()` (deny) | AC-3 default-safe; deny is the failure mode, not a branch |

Membership is the single validation: a forged `optionId` is not locatable in the surfaced set, so it can never route an allow keystroke — it denies. `digit` derives from the index (`strconv.Itoa(idx+1)`), so option order and keystroke digit share one source of truth (`PermissionRequestForClass`'s display order). This is exactly `classifyAnswer`'s discipline, over the neutral `[]turnevent.PermissionOption` instead of `modalbridge.Outstanding` (the ACP adapter stays free of the mobile `protocol` types).

### ACP wire types (local to `acp_permission.go`)

Consumer-owned, mirroring `acp_turn_stream.go`'s locally-owned `sessionUpdateParams`:

```go
const methodSessionRequestPermission = "session/request_permission"

type requestPermissionParams struct {
    SessionID string             `json:"sessionId"`
    ToolCall  *permissionToolCall `json:"toolCall,omitempty"` // omitted: modal event carries no tool-call id
    Options   []permissionOption `json:"options"`
}
type permissionOption struct {
    OptionID string `json:"optionId"`
    Name     string `json:"name"`
    Kind     string `json:"kind"`
}
type permissionToolCall struct{ ToolCallID string `json:"toolCallId"` }

// Response: ACP nests the tagged union under an outer "outcome" field.
type requestPermissionResponse struct {
    Outcome permissionOutcome `json:"outcome"`
}
type permissionOutcome struct {
    Outcome  string `json:"outcome"`            // "selected" | "cancelled"
    OptionID string `json:"optionId,omitempty"`
}
```

Option build: for each `req.Options[i]` (a `turnevent.PermissionOption{ID, Label, Kind}`) → `permissionOption{OptionID: o.ID, Name: o.Label, Kind: string(o.Kind)}`. For the permission class all four `Kind`s are the valid ACP strings (`taxonomy.go`), so every emitted option carries a valid kind.

**`toolCall` is omitted** (see § Open questions #1): the modal-event path yields no tool-call id (`PermissionRequestForClass` leaves `ToolCallID` empty; ADR 027:59). The field is `omitempty` and reserved for a future ticket that correlates the gating `ToolStart` from the turn stream.

### Trust class

`pyry acp`'s composition root pre-trusts the workdir (`runACP` → `trustMark`, `cmd/pyry/acp.go:54-57`) precisely "so the supervised claude never wedges on the workspace-trust modal." A trust modal therefore does not surface in ACP mode. The adapter gates on `ModalClassPermission` only; a `ModalClassTrustFolder` (or any other class) `ModalShown` is a no-op. This keeps every emitted ACP option carrying one of the four valid kinds (a trust option has an empty `Kind`, which is not a valid ACP `PermissionOptionKind`) and avoids shipping an untested trust→ACP mapping for a modal that cannot appear (Evidence-Based Fix Selection). If a trust modal is ever observed in ACP mode, handling it is an additive follow-up (see § Open questions #2).

### Deferred wiring (not this ticket)

The live path — subscribe this adapter to the session's `Session.Events()` and drive `Handle` per modal event — is the modal-event wiring follow-up (T7 #751 territory; the ticket authorises routing back `needs-rework:po` if the two subscriptions must be one slice, but they need not be: `Events()` fans out). When wired, the resolution seam to reuse is `turnbridge.NewTargetSubscriber` (`internal/turnbridge/producer.go:191`), which yields the raw `<-chan tuidriver.Event` after `WaitForPTY` + JSONL resolve; the wiring drains it, filters for modal kinds, and calls `Handle`. `*supervisor.Supervisor` satisfies the `SessionHost` seam (`Session()` at `supervisor.go:466`, `WaitForPTY` at `:517`); `sessions.Session.Supervisor()` (`session.go:122`) is the resolution root. This ticket ships and tests the adapter without that wiring, exactly as #708 defers `interactiveModalEmitterV2`'s wiring.

## Concurrency model

- **One drain goroutine** (the caller of `Handle`, provided by the deferred wiring; a test goroutine here) owns `inflight` and processes `ModalShown`/`ModalHidden` serially. Because tui-driver emits `Hidden(old)` before `Shown(new)` and shows one modal at a time (`events.go:263-287`), `inflight` is set before any `Hidden` that could retire it — single-goroutine ordering guarantees it.
- **One round-trip goroutine per surfaced modal**, spawned by `handleShown`. It blocks in `Transport.Call` (off the read loop, AC-5), then claims the one-shot and routes at most one keystroke. It never touches `inflight`.
- **Shared state between the two goroutines:** only `rt.resolved` (`atomic.Bool`) and `rt.cancel` (written before the goroutine is spawned, read by `handleHidden`). `caller` and `kb` are documented safe from any goroutine (`Transport.Call` / `Supervisor` keystrokes).
- **Race analysis (first-answer-wins):**
  - Host answers, then `Hidden` fires (claude dismissed the modal after our keystroke): round-trip goroutine wins the CAS, routes the answer; `handleHidden`'s CAS fails, routes nothing. ✓
  - `Hidden` fires first (external retirement — e.g. a `session/cancel` ESC, #753), then the host's late reply arrives: `handleHidden` wins the CAS + cancels `cctx`; the Call returns `ctx.Err`; the round-trip goroutine's CAS fails → routes nothing (no stray keystroke into a gone modal). ✓ This is AC-4's "reply for an already-retired modal falls back to deny."
  - Simultaneous: the CAS serialises; whichever wins resolves; the loser no-ops. Both outcomes are safe (route the answer into a still-showing modal, or route nothing). ✓
- **Teardown:** the wiring's ctx cancels the round-trip `cctx`; the Call returns `context.Canceled`; the round-trip goroutine (if it wins the CAS) routes `SendEsc` — best-effort, harmless (a torn-down session returns `ErrNoLiveSession` and writes nothing, `modal.go:84-86`). No goroutine outlives its round-trip: the goroutine ends when the Call returns, and the Call is bounded by `timeout` and by `cctx` cancellation.

## Error handling

- **Default-safe is the only failure mode.** Every non-`selected` path denies (ESC): cancelled, transport error, ctx deadline (timeout), teardown-cancel, decode failure, forged/unknown `optionId`. There is no path that routes an allow keystroke except a decoded `selected` with a membership-valid `optionId`. (AC-3.)
- **Correlation** is transport-owned: `Transport.Call`'s pending map (`acp.go:284-343`) matches reply↔request by outbound id and drops a reply with no waiter (a stale/replayed/mismatched id never reaches this adapter). The adapter's `atomic.Bool` one-shot adds the modal-retirement guarantee on top. (AC-4.)
- **No held `session/prompt` interference** (AC-5): the `session/request_permission` Call takes its own outbound id (`nextID`); the held inbound `session/prompt` (#749/#765) is a separate inbound request id held via the `Responder`. Different direction, different id space — the permission round-trip cannot resolve or touch the held prompt. Distinct from #765's inbound-response primitive, so no code overlap (that ticket touches only `internal/acp/*`).
- **`Answer`/`SendEsc` errors** are best-effort: a keystroke error (no live session / mid-teardown) is content-free `Warn`-logged and tolerated — the round-trip is already resolved; there is nothing to roll back (mirrors `modalResolverV2`'s best-effort actuation).
- **Content discipline (security):** no modal body / prompt / screen text and no `optionId` payload is logged at any level. Logs carry only content-free discriminants: `event`, `session_id`, the outcome discriminant (`selected`/`cancelled`/`denied`), and, on a keystroke failure, the supervisor sentinel `err`. An attacker-controlled `optionId` that reaches a log must be length-bounded (reuse `truncateForLog`, `modal_resolve_v2.go:327`) — though the default path logs only the discriminant, not the id.

## Testing strategy

Same-package (`package main`) table-driven tests with a `fakePermissionCaller` (scriptable `Call` result: a response body, an error, or a block-until-signalled to exercise timeout/retirement) and a `fakeKeystroker` (records `Answer`/`SendEsc` calls — reuse the existing `fakeKeystroker` shape from `modal_resolve_v2_test.go`). Synchronise on the round-trip goroutine via the fake keystroker signalling a channel (or a fake caller that signals when `Call` is invoked). Scenarios:

- **AC-1 (request shape, full four-value kind set):** feed a `ModalShown`/`ModalClassPermission`; assert the `Call` received method `session/request_permission` with `sessionId` set, `toolCall` omitted, and exactly four `options` carrying `optionId`/`name`/`kind` = `allow_once, allow_always, reject_once, reject_always` in display order.
- **AC-2 (selected → keystroke):** scripted response `outcome:"selected"` for each of the four `optionId`s; assert `Answer("1".."4")` reached the keystroker (digit = display index+1) and `SendEsc` did not.
- **AC-3 (default-safe):** three sub-cases — `outcome:"cancelled"`, a `Call` error, and a `Call` that blocks past a short `timeout` (ctx deadline) — each asserts exactly `SendEsc` (deny) and no `Answer`. A fourth: an undecodable response body ⇒ `SendEsc`. Assert no scenario routes an allow keystroke ("no silent grant").
- **AC-4 (correlation / retired):** (a) drive `ModalShown` then `ModalHidden` before releasing the scripted `Call` response; on release, assert **no** keystroke routed (retired won the one-shot); (b) a "mismatched correlation is rejected" assertion driven at the transport level — either exercise `Transport.Call` against a live paired-pipe transport that receives a reply with a non-matching id and assert the Call still blocks/does not resolve (reuse the `liveTransport` harness from `internal/acp/acp_test.go`), or assert the adapter's one-shot rejects a second resolution attempt.
- **AC-5 (off read loop / no held-prompt interference):** assert the round-trip goroutine issues the `Call` without the drain goroutine blocking (the drain goroutine can process a subsequent `Hidden` while the `Call` is outstanding — the retirement test already exercises this); and that the adapter never calls anything on a held-prompt seam (it has no such seam — structural).
- **Non-permission no-op:** a `ModalShown` with `ModalClassTrustFolder` (and other kinds) ⇒ no `Call`, no keystroke.
- **Content discipline:** assert (or code-review) that no test log line carries the prompt/optionId body.

Run `go test -race` — the two-goroutine one-shot is a race-detector target.

## Security review pass

*Adversarial self-review of the design against the ADR 025/027 default-safe posture. This ticket is `security-sensitive`; the pass is mandatory.*

**Trust boundary.** The host is untrusted (may be remote/malicious). Host-controlled inputs into this adapter: the `session/request_permission` **response** (`outcome`, `optionId`) and its JSON-RPC reply id. Everything else (the option set, the session id, the keystroke mapping) is daemon-owned. The one place a host value becomes an action is `optionId → Answer(digit)`, gated by membership in the daemon-built surfaced option set (`acp_permission.go`, the resolution switch). No host value reaches the daemon's disk, terminal path, or logs as content.

Categories walked:

1. **Silent grant (the top risk).** A wrong answer must never grant a tool claude would otherwise deny. *Finding:* the only path to an allow keystroke is a decoded `outcome:"selected"` with an `optionId` **locatable in the daemon-built option set**; every other path (cancelled, error, timeout, teardown, decode failure, forged id) routes ESC (deny). A forged/unknown `optionId` cannot route an allow because it is not in the set. **Default-safe holds; deny is the failure mode, not a branch.** ✓
2. **Replay / stale answer.** *Finding:* two independent, deterministic mechanisms (belt-and-suspenders, different fabric): (a) transport-owned id correlation drops a reply whose id has no pending call (`acp.go:322-343`); (b) the adapter's `atomic.Bool` one-shot ensures a modal resolves exactly once and a reply for a retired modal routes nothing. A replayed reply after resolution is dropped at (a); a late reply after external retirement is neutralised at (b). ✓
3. **Cross-prompt confusion.** Could an answer resolve a *different* prompt than the one it was issued for? *Finding:* the round-trip's `optionId` membership is checked against **that round-trip's** captured option set (`rt.options`), and the id correlation binds the reply to the specific outbound Call. A reply cannot carry a keystroke into a later modal because retirement (Hidden) cancels the outstanding Call and claims the one-shot before a new modal's round-trip is created (single-goroutine `inflight` ordering). ✓
4. **Deadlock / wedge (availability as a safety property).** claude is blocked on the modal (billed interactive session). *Finding:* the Call is bounded by `timeout` (ctx deadline ⇒ deny). A never-answering host denies-and-unblocks after the window rather than wedging claude forever. The Call is issued off the Serve read loop (AC-5), so it cannot deadlock the transport. ✓
5. **Content leakage.** *Finding:* no modal body / prompt / screen text and no host `optionId` body is logged at any level; logs carry only content-free discriminants + supervisor sentinels. `screenText` is not even threaded into the adapter. An attacker-controlled `optionId` reaching a log (only on a defensive path) is length-bounded via `truncateForLog`. ✓
6. **Interference with the held `session/prompt` (T5/T7).** *Finding:* the outbound Call uses its own outbound id space; the held inbound `session/prompt` uses an inbound request id held by #765's `Responder`. No shared state; the permission round-trip cannot resolve or touch the held prompt. ✓

**Residual / accepted:** `toolCall` is omitted from the request (the modal event carries no tool-call id). This is a fidelity gap, **not** a security gap — it cannot cause a grant; at worst a host renders a less-specific prompt. Tracked as Open question #1. A misbehaving host that both cancels (dismissing the modal) and answers `selected` cannot escalate: the retirement one-shot neutralises the late `selected`, and even absent that, a stray keystroke into a non-modal claude injects input (undesirable) but does **not** re-grant a dismissed permission — and the one-shot prevents even the stray keystroke.

**Verdict: PASS.** The design is default-safe end to end, correlation is deterministic and doubly-guarded, and no host content crosses into logs or actions beyond the membership-gated keystroke.

## Open questions

1. **`toolCall` omission.** The modal-event path yields no gating tool-call id, so the request omits `toolCall`. Confirm real ACP hosts tolerate a `session/request_permission` without `toolCall` (the ACP schema marks it required in some revisions). If not tolerated, a follow-up correlates the outstanding `ToolStart` from the turn stream (a cross-subscription concern, out of scope here). The adapter's field is `omitempty`-reserved for that.
2. **Trust modal in ACP mode.** The workdir is pre-trusted (`trustMark`), so a trust modal should not surface. If one is observed, add a trust arm (proceed→`AcceptTrust`, exit→`SendEsc`) and decide the ACP option `kind` mapping for `proceed`/`exit` (they have no native ACP kind). Additive; deferred.
3. **Timeout value.** `timeout` is a constructor param; the AC-3 "timeout ⇒ deny" path needs a non-zero value to fire. A human host may need minutes; an automated host answers instantly. Pick a generous default at wiring time (suggest ~2 minutes as a starting point) and treat it as a tunable — this ticket only requires the path to exist and default to deny.
4. **Exact response nesting.** ADR 027:88 describes the response as `outcome: "selected" + optionId`; the ACP schema nests it (`outcome: { outcome, optionId }`). This spec decodes the nested form. Verify against the ACP JSON schema / a reference host during implementation; the decode is default-safe (any mismatch ⇒ deny), so a wrong guess fails closed (denies all), never open.
