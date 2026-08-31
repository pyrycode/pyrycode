# ACP permission proxy (`session/request_permission`) (#752)

The **highest-risk seam in the epic** (T8, [ADR 027](../decisions/027-acp-mapping.md) divergence 2).
`acpPermissionProxy` (`cmd/pyry/acp_permission.go`, two new files; shipped unwired, **live-wired by #801**
— see [Permission-proxy wiring](#permission-proxy-wiring-801) below) answers claude's on-screen
permission modal by asking the ACP host: on a permission-class modal it issues a blocking
`session/request_permission` `Call` to the host, then routes the host's choice back into claude's live
prompt as the correct keystroke — the interactive path, **no** `claude -p` / Agent SDK mechanism (the
hard cost invariant). **security-sensitive**; an architect security pass (spec § Security review pass,
verdict PASS) and code-review security goggles both cleared it. It is the ACP analogue of the mobile
`interactiveModalEmitterV2` but far thinner — one blocking `Call` in place of the broadcast + nonce
registry.

### Where permission surfaces — its own modal-event subscription

A permission modal is a **tui-driver PTY-state event** (`tuidriver.EventKindPtyModalShown` / `…Hidden`
with `ev.Modal == ModalClassPermission`), **not** a `turnevent.Event` (`turnbridge.mapEvent` drops
modal kinds). So the adapter takes its **own** modal-event subscription off the live session;
`Session.Events(...)` mints a fresh channel + merge goroutine per call, so it does not contend with the
T6/T7 turn-event subscription ([outbound streaming adapter](#outbound-streaming-adapter-acpturnstream-750)).

### Two goroutines, one atomic arbiter

`Handle(ctx, ev)` is the modal-event sink, called serially on the **drain goroutine** (the deferred
wiring's). A permission `ModalShown` → `handleShown`; any `ModalHidden` → `retireInflight`; everything
else (a non-permission `ModalShown` included) → no-op. It takes no `screenText` — `session/request_permission`
carries no prompt body and the option set is class-fixed, so the wiring never sources `ScreenSnapshot`.

- **`handleShown`** builds the four-option set via `modalbridge.PermissionRequestForClass(ev.Modal, "")`
  (option order + ACP `kind`s share one source of truth), stores a `permissionRoundTrip`, and **spawns
  one round-trip goroutine** — the "off the Serve read loop" guarantee (AC-5; a `Call` on the read loop
  deadlocks, since only it reads the response).
- **`runRoundTrip`** blocks in `Call`, then claims the one-shot (`rt.resolved.CompareAndSwap(false,true)`)
  and routes **at most one** keystroke; `defer rt.cancel()` releases the deadline timer.
- **`retireInflight`** (drain goroutine) claims + clears the round-trip; if it wins the CAS it cancels
  the in-flight `Call`, so `runRoundTrip` unblocks, sees the failed CAS, and routes nothing (AC-4).

`inflight` is drain-goroutine-confined (no lock); the only cross-goroutine state is the `atomic.Bool`
one-shot + `rt.cancel` (written before spawn). Retiring on **any** `ModalHidden` (vs the mobile
surfacer's class-match) is safe: an `inflight` round-trip only ever exists for a `ModalClassPermission`
modal, and tui-driver's single-modal invariant means no unrelated `Hidden` fires while it shows.

### Default-safe resolution (`route`) — deny is the failure mode, not a branch

The **only** path to an allow keystroke is a decoded `outcome:"selected"` whose `optionId` is a
**member of the daemon-built option set** → `Answer(digit)`, `digit = strconv.Itoa(idx+1)` (the index
is the single source of truth for wire option order and the keystroke digit — `classifyAnswer`'s
discipline over the neutral `[]turnevent.PermissionOption`). Every other outcome routes deny
(`SendEsc`): call error / ctx-deadline timeout / teardown-cancel, undecodable body, forged/unknown
`optionId`, `outcome:"cancelled"`, unknown/empty outcome. A forged id is simply not locatable in the
set, so it can never route an allow.

### Routing seam — keystrokes, not `PermissionResponse`

The host's selection routes through the supervisor **keystroke seam** (`modalKeystroker`:
`Answer`/`SendEsc`; `*supervisor.Supervisor` satisfies it, #726). There is **no** `turnevent.PermissionResponse`
consumer — the neutral type stays unwired and the adapter does not construct one. This refines
[ADR 027](../decisions/027-acp-mapping.md) divergence 2, whose prose sketched a `PermissionResponse`
feed-back; the built adapter routes the keystroke directly.

### Correlation — transport-owned, doubly guarded

`Transport.Call`'s pending map matches reply↔request by outbound id and drops a no-waiter reply, so a
stale/replayed/mismatched id never reaches the adapter (AC-4). The `atomic.Bool` one-shot adds the
modal-retirement guarantee on top — belt-and-suspenders, both deterministic. The `Call` takes its own
outbound id space and cannot resolve or touch the held inbound `session/prompt` (#749/#765), which
resolves only on `TurnEnd` (AC-5).

### Wire types + trust class

Consumer-owned `requestPermissionParams{sessionId, toolCall?, options[]}` mirroring
`acp_turn_stream.go`'s local params. `toolCall` is **omitted** (`omitempty`) — the modal-event path
yields no gating tool-call id; reserved for a future `ToolStart`-correlation ticket. The response
decodes the **nested** `{"outcome":{"outcome","optionId"}}` form; the decode is default-safe (shape
mismatch ⇒ `Outcome` zero ⇒ deny), so a wrong guess fails **closed**. The composition root pre-trusts
the workdir (`trustMark`), so a **trust** modal does not surface; the adapter gates on
`ModalClassPermission` only (`AcceptTrust` unused) — evidence-based, avoiding an untested trust→ACP
mapping for a modal that cannot appear. Full per-ticket detail in [`codebase/752.md`](../codebase/752.md).

### Permission-proxy wiring (#801)

`acpPermissionProxy` shipped in #752 with **zero non-test callers**. #801 is the
composition-root wiring that gives it its first — the exact twin of the [Producer
wiring (#796)](#producer-wiring-796), and folded into the **same** `acpTurnStreams`
manager rather than a parallel one (that manager already owns the ctx / transport /
dir / `started` map / `wg` the proxy needs). `start(id)` now spawns **two** outbound
adapters per session — the turn producer **and** the permission drain — both parented
on `runCtx`, both under the one `started` idempotency mark, both joined by `wait()`,
both disabled when `dir == ""`. `acp.go` is untouched and no constructor signature
changes.

New file `cmd/pyry/acp_permission_streams.go` supplies three things: a package const
`acpPermissionTimeout = 2 * time.Minute` (the outbound `Call` deadline — a
never-answering host denies-and-unblocks after it; mirrors the daemon's
`modalDenyTimeout`, `internal/relay/v2session.go:81`); `startPermissionProxy(host, sessionID)`,
the manager glue that constructs `newACPPermissionProxy(m.transport, host, sessionID, acpPermissionTimeout, m.logger)`
(where `m.transport` is the `permissionCaller` and `host = sess.Supervisor()` is
**both** the `modalKeystroker` and the `turnbridge.SessionHost`) over a fixed-target
subscriber **identical in shape** to the turn stream's — `resolveBoundSessionJSONL(m.dir, sessionID)`,
`Switch: nil`, a **fresh** `Tracker` per session; and `runPermissionModalStream(ctx, sub, proxy)`,
the drain. The drain is the ACP sibling of the mobile `runModalStream`
([#798](../codebase/798.md)) but **simpler** — no `screenText`, no per-kind pre-filter:
it hands *every* raw `tuidriver.Event` to `proxy.Handle`, which owns the filter
(no-ops every non-permission event). It is the **sole** caller of `Handle`, so the
proxy's single-goroutine `inflight` invariant holds. This drain is also the
deterministic **test seam #754 consumes** to observe divergence 2 with no live claude
modal.

The detached `runRoundTrip` (`cctx = context.WithTimeout(m.ctx, acpPermissionTimeout)`)
is **not** `wg`-joined — the frozen proxy owns its spawn — but is ctx-bounded: because
`cctx` descends from `m.ctx`, teardown-cancel unblocks any in-flight `Call` and it
exits in microseconds, denying (ESC) on the way out. The **drain** is the joined unit,
matching the turn stream's discipline. The drain needs a valid JSONL `path` even though
modal events are PTY-sourced (the subscriber surfaces them *through* the JSONL-gated
`sess.Events`), which is why `resolveBoundSessionJSONL` is reused and why `dir == ""`
disables the permission drain by the same `start()` guard as the turn stream.

**security-sensitive** (inherited from #752): the wiring drives an outbound
tool-permission **policy** surface with default-safe-deny. The coerced-allow trust
boundary lives entirely in the frozen proxy's `route()` — the wiring supplies the
transport as caller and drives `Handle`, never inspecting the host reply, so the
boundary is **inherited intact**. Default-safe-deny preserved end to end; content-free
logging preserved (the new drain and glue log nothing). Full per-ticket detail in
[`codebase/801.md`](../codebase/801.md).
