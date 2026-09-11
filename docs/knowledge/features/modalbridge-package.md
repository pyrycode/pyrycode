# `internal/modalbridge` + the modal surfacer — outbound permission/trust modals to phones

The **outbound half** of the daemon-side modal bridge (EPIC #597 Phase 3,
[ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — no-raw-bytes
invariant). When tui-driver detects a permission or trust modal on claude's screen,
the daemon turns it into a typed `modal_shown` event and pushes it to
interactive-capable phones — **never raw PTY bytes**. This slice (#716, split from #703) establishes the **outstanding-modal registry**, keyed by a one-time `modal_id`
nonce, that the inbound resolution half consumes to route answers/cancels back and
to reject stale/replayed answers — `Resolve` (the consume-and-retire idempotency
gate) is wired by #727's `modal_cancel` resolver; `Lookup` by #717's gated
`modal_answer`. The inbound relay seam itself is documented in
[v2-session-manager.md § Inbound modal control](v2-session-manager.md#inbound-modal-control-727--modalresolver-seam--modal_dismissed-broadcast).

The surfacer also owns the **local resolution arm** (#706): when the operator answers a
modal at the local `pyry attach` TTY it reacts to `EventKindPtyModalHidden`, `Resolve`s
the outstanding `modal_id`, and broadcasts `modal_dismissed{source: local}` — making
resolution single-shot **across both heads** (local TTY + paired phone). See
[§ The local resolution arm](#the-local-resolution-arm--handlemodalhidden-706-first-answer-wins).

`ModalShownPayload` carries an outbound `conversation_id` scoping key (#1065): the daemon
stamps the conversation whose bound session raised the modal so a client viewing a
*different* conversation can filter it out — see
[§ Cross-conversation confidentiality](#security).

Two new files, in two packages:

- `internal/modalbridge/modal.go` — the **relay-free modal domain**: the `Registry`
  + `Outstanding` entry + `modal_id` nonce + class→`PermissionRequest` mapping +
  payload build.
- `cmd/pyry/interactive_modal_v2.go` — the **surfacer** (`interactiveModalEmitterV2`):
  drain-one-modal → record → fan out, mirroring `interactiveTurnEmitterV2`.

- Spec: [`specs/architecture/716-modal-surface-producer.md`](../../specs/architecture/716-modal-surface-producer.md).
- Ticket record: [codebase/716.md](../codebase/716.md).
- Wire vocabulary: [`docs/protocol-mobile.md` § Modal (v2)](../../protocol-mobile.md).

## Why `internal/modalbridge` is relay-free

\#717 intercepts an inbound `modal_answer` at `(*V2SessionManager).dispatchAppFrame`
(in `internal/relay`) and must look the nonce up in this registry — so
`internal/relay` will import `internal/modalbridge`. Therefore `internal/modalbridge`
**MUST NOT import `internal/relay`** (it would cycle). It imports only
`internal/protocol`, `internal/turnevent`, and `pkg/tuidriver` (the **typed**
`ModalClass` API only — never a raw-byte surface), so no claude-screen substrate
literal enters the package and `cmd/substrate-guard` stays green; `screenText` arrives
already rendered to plain text by the caller. The fan-out — which needs
`relay.ActiveConn` — lives in `cmd/pyry` (`package main` imports both freely),
mirroring the existing `interactiveTurnEmitterV2`.

## `internal/modalbridge` surface

```go
// One recorded surfaced modal. Holds at least the option list so #717 can map an
// inbound option_id against it. Carries no secret (modal_id is an opaque
// correlation nonce, not a credential). ConversationID is the outbound scoping
// key (#1065), stored so Snapshot re-emits it and a reconnect reconcile replays
// the same scope as the initial broadcast.
type Outstanding struct {
    ConversationID                string
    ModalID, Class, Title, Prompt string
    Options                       []protocol.ModalOption
    DefaultOptionID               string
    Reason                        json.RawMessage
    ReasonType, BlockedPath       string
    Description                   string
    DefaultToNo                   bool
}

type PermissionContext struct {
    Reason                        json.RawMessage
    ReasonType, BlockedPath       string
    Description                   string
    DefaultToNo                   bool
}

type Registry struct { /* sync.Mutex + map[string]Outstanding */ }
func New() *Registry

// PermissionRequestForClass maps a detected modal class → the internal
// PermissionRequest, the wire class string, and ok. ok == false for every class
// that is not permission/trust (those produce no modal_shown). screenText (trimmed)
// becomes PermissionRequest.Title (the human-readable body).
func PermissionRequestForClass(class tuidriver.ModalClass, screenText string) (turnevent.PermissionRequest, string, bool)

// Record is the SINGLE nonce-mint site. Builds the marshal-ready ModalShownPayload,
// mints exactly one fresh modal_id, stamps it plus the #1065 conversation_id scoping
// key (convID), records the Outstanding (with ConversationID) in the SAME critical
// section, returns the stamped payload. The only error path is RNG failure.
func (r *Registry) Record(req turnevent.PermissionRequest, wireClass, convID string) (protocol.ModalShownPayload, error)
func (r *Registry) RecordWithContext(req turnevent.PermissionRequest, wireClass, convID string, context PermissionContext) (protocol.ModalShownPayload, error)

func (r *Registry) Lookup(modalID string) (Outstanding, bool)  // #717's read seam
func (r *Registry) Resolve(modalID string) (Outstanding, bool) // #727's consume-and-retire one-shot
func (r *Registry) Snapshot() []protocol.ModalShownPayload     // #876's current-truth read seam
```

`Lookup`/`Resolve` were **defined in #716, exercised downstream** — `Resolve` by #727's `modal_cancel` resolver (the atomic consume-and-retire that makes the first
cancel win and every replay/unknown id a no-op), `Lookup` by #717's gated
`modal_answer`. They belong with the type's contract even though #716 only calls
`Record`.

`RecordWithContext` (#2346) is the additive permission path. It stores the five
Claude-authored display fields in the same operation that returns the stamped
initial payload, so `Snapshot` can replay exactly the same context after a
reconnect. `Record` delegates with zero context, preserving the older approval
MCP payload shape. `Reason` is cloned on write and snapshot because
`json.RawMessage` is a mutable byte slice; copying only the struct would let an
initial-broadcast or reconnect caller mutate outstanding registry truth.

## Class → option mapping (the minimal fixed-option-set, design option (a))

tui-driver v1.3.0 has **no** permission/trust option extractor (`ParseAskUserQuestion`
exists only for the *different* `ask-user-question` class), and `EventKindPtyModalShown`
carries **only** the appearing `ModalClass` — no parsed title/options. So this slice
maps the detected class to a **known fixed option set** and takes the body from the
rendered plain text. (A robust screen-scraping label parser is a separable
surface-vs-parse concern, deliberately *not* built here.)

| `tuidriver.ModalClass` | wire `class` | options (ordered, claude's display order; `id` = `PermissionOptionKind` string) | `default_option_id` (fail-safe) |
|---|---|---|---|
| `ModalClassPermission` | `"permission"` | `allow_once`, `allow_always`, `reject_once`, `reject_always` | **`reject_once`** |
| `ModalClassTrustFolder` | `"trust"` | `proceed`, `exit` | **`exit`** |
| all others (`mcp`/`agents`/`slash-picker`/`ask-user-question`/`model-select`/`permissions-config`/`""`) | — | — | `ok=false` |

Permission option ids reuse [`internal/turnevent`](turnevent-package.md)'s four
`PermissionOptionKind` strings. Trust uses two literal ids (`proceed`/`exit`) with
`Kind` left unset — `Valid()` is advisory here and only `id`/`label` reach the wire;
if #717 needs a kind for trust it can extend the taxonomy then.

### Fail-safe deny default (security-review MUST-FIX)

`default_option_id` is the **DENY** option (`reject_once` / `exit`), **not**
`options[0]`. For a *remote* permission/trust surface the phone's pre-highlighted
default must fail safe, so a careless confirm **denies rather than allows**. The
`options` array stays in claude's display order (allow-first) — display order and the
highlighted default are deliberately **decoupled**, which keeps both AC-valid
(`default_option_id ∈ options[].id`, true by construction since the deny option is
always in the set). This is **UI pre-selection only**, not an auto-answer: the human
still confirms, [#702](../codebase/702.md) gates answering (per-device, default OFF),
and [#725](../codebase/725.md) owns deny-on-timeout (safe-deny ESC on a bounded window).

## `modal_id` nonce — the single-writer security primitive

`newModalID` draws a canonical UUIDv4 string from `crypto/rand` (122 bits ⇒ opaque +
unguessable), mirroring `conversations.NewID` (`internal/conversations/id.go`). **Not**
`math/rand`. It is minted **only** inside `Registry.Record`, called **only** from the
surfacer's single goroutine, **exactly once** per `EventKindPtyModalShown` event —
tui-driver's modal axis is rising-edge (one `Shown` per appearance), so one event ⇒
one modal ⇒ one mint, no per-modal de-dup machinery. This is the primitive the inbound
resolvers rely on to reject stale/replayed answers (#727's `Resolve`, #717's `Lookup`). `Record` mints **and** stores atomically under the
mutex: no `modal_shown` is ever emitted without a recorded registry entry, and no entry
without a successfully-minted id.

## The surfacer (`cmd/pyry/interactiveModalEmitterV2`) — deleted by #1348

**No longer in the tree.** #1348 ("delete the terminal-driving interactive path and
everything on it") removed `cmd/pyry/interactive_modal_stream_v2.go` and
`interactiveModalEmitterV2` along with the rest of the PTY-driven interactive path;
`tuidriver.EventKindPtyModalShown`/`EventKindPtyModalHidden` and this emitter no
longer exist in production. This also silently removed the sole production caller of
`(*relay.V2SessionManager).ArmModalTimeout` — nothing arms the relay-side
`modalDenyTimeout` deny-on-timeout in production anymore (only three test files call
it; see [the deny-on-timeout doc](v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md)).
Found while investigating #1909 (unrelated ticket, different constant — it raises
`mcpApprovalTimeout`, the stream-json path's own timeout, which this deletion does
not touch). The design below is kept as a historical record of how the arming worked
while it was live; treat every present-tense claim in this section and in § Live
daemon wiring below as **pre-#1348**.

A **passive state machine** — spawns no goroutine, owns no queue (the `Registry` mutex
is its only synchronisation), same posture as `interactiveTurnEmitterV2`. The single
entry point dispatches on event kind to two arms (#706 added the `Hidden` arm; every
other event is a no-op):

```go
func (e *interactiveModalEmitterV2) Handle(ctx context.Context, ev tuidriver.Event, convID, screenText string) {
    switch ev.Kind {
    case tuidriver.EventKindPtyModalShown:  e.handleModalShown(ctx, ev, convID, screenText)
    case tuidriver.EventKindPtyModalHidden: e.handleModalHidden(ctx, ev) // local resolution (#706); convID unused — modal_dismissed carries no conversation_id
    }
}
```

`convID` (#1065) is sourced from the **same single** `active.CurrentConversation()`
read that selected `screenText` (`cmd/pyry/interactive_modal_stream_v2.go`
`boundScreenText`, now returning the paired `(convID, screen)`) — not read
independently inside the emitter, so the modal's content and its scope stamp
cannot diverge.

### `handleModalShown` — surface a modal to phones

1. `PermissionRequestForClass(ev.Modal, screenText)`; `!ok` → no-op (non-permission/trust
   class; AC1).
2. `reg.Record(req, class, convID)` mints the `modal_id`, stamps `conversation_id`,
   and records the `Outstanding` (with its `ConversationID`). RNG failure →
   drop (never push an id-less payload), `Warn` with no payload bytes.
3. `armer.ArmModalTimeout(ctx, modalID)` arms the deny-on-timeout (#725), then **track
   the just-surfaced modal** (`outstandingID = modalID; outstandingClass = ev.Modal`) so
   a later `Hidden` can correlate back to this id (#706, below). Set here — consistent
   with the registry entry + armed timeout — even on the defensive marshal-fail return.
4. Marshal the payload **once**; build a `protocol.Envelope{Type: TypeModalShown, TS: now}`
   with **`EventID` left nil** — a `modal_shown` is a *control* event, not part of the
   turn-event replay ring (`forwardEnvelope` never drops `EventID==nil` envelopes, so
   delivery is real).
5. **Capability-gated fan-out** via the shared `broadcastInteractive` helper (below).

### `broadcastInteractive` — the shared fan-out helper (#706)

Both arms fan one control envelope to every interactive-capable conn through one private
helper (exactly `interactiveTurnEmitterV2.emit`'s shape): one shared timestamp, skip
`!c.Interactive` (v2 modal events ride the `interactive` capability, #607), assign a
per-conn monotonic `env.ID = ++nextID`, **`EventID` nil**, `Push`. A `Push` error
debug-logs the transport sentinel (tagged by the caller's `pushErrEvent`) and
**continues** to the next conn (a slow/closed conn never blocks the others); `ctx.Err()`
→ return (teardown). Factored out so `Shown` (`TypeModalShown`) and `Hidden`
(`TypeModalDismissed`) share one tested loop; the existing `Shown` fan-out tests guard
it. The cross-package `relay.broadcastModalDismissed` is deliberately **not** reused — it
iterates `m.sessions` on the Run goroutine and must not be reached from this producer
goroutine (see § The local resolution arm).

### The local resolution arm — `handleModalHidden` (#706, first-answer-wins)

When the operator answers a modal at the local `pyry attach` TTY, claude's modal
vanishes and tui-driver fires `EventKindPtyModalHidden` for the just-hidden **class**
([ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) § Security model #4). The arm correlates that class back to the `modal_id` this emitter surfaced,
`Resolve`s it through the shared registry, and — **only if this head wins the race**
against a remote answer/cancel (#717/#727) or the deny-on-timeout (#725) — audits the
local resolution and broadcasts one `modal_dismissed{source: local}`.

```
handleModalHidden(ctx, ev):
  if outstandingID == ""          → return  // nothing we surfaced is outstanding (AC1 gate)
  if ev.Modal != outstandingClass → warn + return WITHOUT clearing  // defensive, unreachable
  id := outstandingID; clear outstandingID/Class   // modal gone regardless of who consumes
  out, ok := reg.Resolve(id)
  if !ok → return     // first-answer-wins LOSER: a remote/timeout arm already consumed it
                      //   — no audit, no broadcast, no second modal_dismissed (AC2 / AC3-b)
  // WINNER:
  audit.Log({id, out.Class, OutcomeDismissedLocal, SourceLocal})         // no answering device
  broadcastInteractive(ctx, TypeModalDismissed, {id, dismissed_local, local}, …)
```

- **Correlation = emitter-tracked outstanding id + class** (the architect's call over a
  registry "resolve-the-current" method, which would force the registry to track
  recency/insertion-order — state a precise keyed one-shot must not carry). tui-driver's
  single-modal + `Hidden(old)`-before-`Shown(new)` invariants guarantee the tracked id
  always names the showing modal when a `Hidden` arrives, and `Handle` is single-goroutine
  so the tracking (`outstandingID`/`outstandingClass`) needs **no lock** — same posture as
  `nextID`.
- **First-answer-wins is structural in `Registry.Resolve`**, not added here. All four
  resolution paths (remote answer #717, remote cancel #727, deny-on-timeout #725, and this
  local arm) route through the one mutex-guarded one-shot `Resolve`. Each broadcasts/audits
  **only on `ok`**, so a `modal_id` is resolved/broadcast/audited **at most once** across
  the two real goroutines (producer Run vs relay dispatch).
- **No keystroke-after-resolution window.** The remote answer path consumes via `Resolve`
  *strictly before* routing its keystroke, so a local resolution that won first makes the
  remote `Resolve` miss and the remote path return **before any keystroke** — an
  internet-sourced answer can never act on a modal the operator already resolved locally.
  The local arm itself routes **no** keystroke (the operator already pressed the key; the
  modal is already gone) — strictly less actuation surface than the remote arms.
- **Local outcome is a producer-defined sentinel, not an `option_id`.** The daemon cannot
  observe *which* option the operator picked locally — only that the modal vanished — so
  `modal_dismissed{local}` carries `outcome: dismissed_local` (the phone uses it only to
  clear its prompt). One `audit`/`protocol` vocabulary feeds both the wire dismissal and
  the [audit](audit-package.md) entry; the local resolution audits with **no answering
  device** (empty `DeviceHash`/`DeviceLabel`, the no-device case), winner-only.

### Defensive prompt bound

`Prompt` is grid-bounded by construction (a terminal render, KB-scale), but a
`modal_shown` is an **un-droppable control frame** (control frames bypass the push
queue's soft-overflow drop). So `boundPrompt` trims surrounding whitespace and caps the
body at `maxPromptBytes` (4096), backing up to a rune boundary so a multi-byte rune is
never split — a pathological screen render can't inflate the control frame.

## Plain-text / no-raw-bytes (AC3, ADR 025)

The phone receives **typed events only**. `screenText` arrives already rendered to plain
text (the [live wiring (#798)](#live-daemon-wiring-798) feeds
`Supervisor.ScreenSnapshot()` → `tuidriver.Render`, ANSI/OSC-free, inside the ADR-025
seal), and `encoding/json` escapes any residual control byte — so no raw terminal bytes
reach the phone. There is exactly one structured path; **no coarse/raw-byte fallback
exists**.

## Concurrency model

- **Surfacer is single-goroutine.** `nextID`, `outstandingID`, `outstandingClass` are
  unguarded (no atomic/mutex) — `Handle` runs only on the producer's single Run goroutine,
  the same invariant `interactiveTurnEmitterV2` documents.
- **The `Registry` mutex is a deterministic safety net, not goroutine-confinement.** The
  registry is the one piece touched by **two real goroutines**: the surfacer/producer
  goroutine (`Record` on `Shown`, `Resolve` on a local `Hidden` #706) and the relay
  dispatch goroutine (`Lookup`/`Resolve` for remote answer/cancel #717/#727, and the
  deny-on-timeout #725). So it carries a `sync.Mutex` (a **leaf lock**: O(1) holds, never
  nested with any other lock). Single-writer-nonce (only the surfacer *mints*) does **not**
  by itself make the map safe against the relay's concurrent reads/resolves, so a real
  mutex is the right fabric (belt-and-suspenders: the safety net is deterministic code).
- **First-answer-wins is structural in `Resolve`** (#706). All four resolution paths route
  through the one mutex-guarded one-shot `Resolve`; each broadcasts/audits only on `ok`, so
  a `modal_id` is resolved at most once regardless of which goroutine wins the race.

## Security

`security-sensitive`. Architect's security-review (in the spec) is PASS; both its findings
— the fail-safe deny default and the defensive `Prompt` bound — are implemented and tested.

- **Outbound only.** This slice mints + records; the **inbound boundary** (an untrusted
  phone-asserted `modal_id`) is #717's, out of scope.
- **`modal_id`** = `crypto/rand` UUIDv4, in-memory only, an opaque correlation nonce (not a
  credential). Unbounded-registry-growth is bounded by the inbound resolvers' consume
  (#727's `modal_cancel` `Resolve`, #717's gated `modal_answer`, #725's deny-on-timeout);
  the live producer ([#798](#live-daemon-wiring-798)) feeds one `Record` per surfaced modal
  and each entry is consumed on resolve/timeout, so growth is bounded to outstanding modals.
- **The modal body (`title`/`prompt`/`screenText`) is application content and is NEVER
  logged** at any level. Logs carry only content-free discriminants (`event`, `class` (a
  closed set), `conn_id`, `env_id`) + the transport-sentinel `err`.
- **Cross-conversation confidentiality — `conversation_id` outbound scoping stamp (#1065).**
  `ModalShownPayload` carries a daemon-asserted `conversation_id`, joining the client-side
  scoping model `TurnStatePayload`/`QueueStatePayload` already use — the daemon still fans
  every `modal_shown` to every interactive conn; the stamp is what confines display to the
  conn(s) following that conversation. `Record` stamps the payload and stores
  `Outstanding.ConversationID` **atomically** in one critical section, so a stored modal is
  never `Snapshot`-able without its scope key. The security-critical property is that the
  screen content and its scope label **cannot diverge**: both derive from the *same single*
  `active.CurrentConversation()` read in `boundScreenText`
  (`cmd/pyry/interactive_modal_stream_v2.go`) — a re-read inside the emitter would open a
  window where conversation A's screen is stamped conversation B, which is the exact leak
  the ticket exists to close. `convID` is threaded into `Handle` as a parameter for this
  reason, not read independently. **Inbound is unchanged**: `ModalAnswerPayload`/
  `ModalCancelPayload` still carry no `conversation_id` — an answer is authorized by
  resolving its `ModalID` server-side against the registry (§ `modal_id` above), never a
  phone-asserted conversation. See [codebase/1065.md](../codebase/1065.md).

## Current-truth enumeration — `Registry.Snapshot()` (#876)

`Lookup`/`Resolve` are **id-keyed**: a caller must already know the `modal_id` it wants.
Nothing could ask *"what is outstanding right now?"* — the question the
reconnect-reliability direction (#829) needs answered on every (re)connection, so a
reconnecting client can be reconciled to the daemon's **current control truth**
(a still-pending modal re-sent under its original stable id; an already-resolved one
silently absent) instead of replaying past events. `Snapshot()` is that one read seam.

```go
func (r *Registry) Snapshot() []protocol.ModalShownPayload
```

- **Direct field copy, not a re-derivation.** `Outstanding` already *is* the flattened
  fields of the payload `Record` built (`Record` stores `p.Class`/`p.Title`/… from the
  already-built payload) — so `Snapshot` copies straight from `Outstanding`, restamping
  the stored `o.ModalID` **and `o.ConversationID`** (#1065). It does **not** call
  `buildPayload`: that assembles a payload *from* a `PermissionRequest`+class, which would
  require un-mapping `Outstanding` back into a `PermissionRequest` — lossy and unnecessary
  re-derivation of data the registry already stores verbatim. Because `reconcileModals`
  (`internal/relay/v2session_modal.go`) marshals each `Snapshot()` payload **verbatim**
  into the replay envelope, restamping `ConversationID` here is what makes a reconnecting
  conn's replayed `modal_shown` scoped identically to the initial broadcast, at zero cost
  to the replay path itself.
- **Pure read.** Walks `r.outstanding` under the one leaf `r.mu` (same O(n)-map-walk,
  no-nested-locks discipline as `Lookup`/`Resolve`). Mints no id (`newModalID` untouched),
  retires nothing — a modal in a snapshot is still `Lookup`/`Resolve`-able afterward. A
  resolved modal never re-surfaces, since `Resolve` already removed it from `r.outstanding`.
- **`Options` and the open-shape JSON `Reason` are cloned per payload**
  (`slices.Clone`, mirroring `Record`'s clone-on-write) so a mutating consumer
  can't corrupt the stored `Outstanding` through an aliased slice —
  the property that keeps "leaves registry state unchanged" true even under a careless
  caller.
- **Map-walk order is unspecified.** #877's consumer reconciles by `modal_id`, never by
  position; an empty registry yields a non-nil, zero-length slice (`len==0`, no
  nil-vs-empty ambiguity for the consumer).
- **Not security-sensitive.** No input, no wire traffic, no id minted — a registry-internal
  enumeration of state the package already owns. The one-time-nonce and #717's
  deny-on-timeout semantics are unaffected; this is read-only.

Registry-only: no producer, no wire traffic, no session-manager change here. The
connect-time producer that calls `Snapshot()` and re-sends the payloads over the relay
**landed in #877** — see
[`v2-session-manager.md` § Connect-time modal reconcile](v2-session-manager.md#connect-time-modal-reconcile-877--outstandingmodals-seam--reconcilemodals)
and [codebase/877.md](../codebase/877.md).

## Daemon wiring (#798, deleted by #1348)

**Pre-#1348 history, not current wiring** — see the note at § The surfacer above.

The producer + registry + class mapping ship with a **unit test driving a scripted modal
through a fake interactive push surface** (the [#632 emitter → #633 wiring] precedent — a
clean, self-contained, unit-tested component). [#798](../codebase/798.md) then live-wires
the surfacer into `startRelayV2` — `cmd/pyry/interactive_modal_stream_v2.go`
(`startInteractiveModalStreamV2`), co-located with the turn stream inside the shared
`bridge != nil && claudeSessionsDir != ""` gate. It: (1) constructs the surfacer with the
daemon-singleton `*Registry` (the same instance #717/#725 wire into the relay — the shared
instance is what makes the cross-head `Resolve` arbitration real in production), with the
`V2SessionManager` as both the capability-gated broadcaster **and** the deny-on-timeout
armer; (2) feeds it both `EventKindPtyModalShown` **and** `EventKindPtyModalHidden` events
(#706's local arm) from the **follow-active** `Session.Events()` stream; and (3) supplies
the active bound host's rendered `ScreenSnapshot()` as `screenText`.

The wiring reuses the turn stream's follow-active machinery **wholesale** —
`resolveTarget` + `turnbridge.NewTargetSubscriber`, which yield **raw** `tuidriver.Event`s
(the modal-dropping mapper lives downstream in `turnbridge.Producer.drain`, which the modal
stream does **not** use). So it is a *second, independent* `Session.Events()` subscription
(blessed by tui-driver `events.go:159`) driven by a bespoke **mapper-free** drain loop, **not**
an `OnModal` callback on `turnbridge.Config` and **not** a second `turnbridge.Producer`. The
paired screen is reached by type-asserting the follow-active host to a `cmd/pyry`-local
`screenSnapshotter` interface (satisfied by `*supervisor.Supervisor`), keeping the shared
`turnbridge.SessionHost` contract screen-free. The screen is re-resolved at handle time (a
narrow, within-operator, cosmetic switch-race is accepted — see [codebase/798.md](../codebase/798.md)),
only on a `Shown` (no render on idle/thinking ticks). Full detail:
[codebase/798.md](../codebase/798.md).

## Testing

- `internal/modalbridge/modal_test.go` (table-driven, stdlib, `t.Parallel()`): `Record`
  mints a canonical UUIDv4 + round-trips through `Lookup`; **nonce uniqueness** over ≥1000
  `Record` calls (no collisions); the full `PermissionRequestForClass` mapping table incl.
  every non-matching class → `ok=false`; the **payload invariant** (`default_option_id ∈
  options[].id`, ordered allow-first, default is specifically the **deny** option); prompt
  trim + rune-boundary bound.
- `cmd/pyry/interactive_modal_v2_test.go` (AC4 headline; fake `interactiveBroadcaster`):
  one `modal_shown` per interactive conn, **zero** to non-interactive; the pushed `ModalID`
  is non-empty, equals the recorded id, and `registry.Lookup(id)` succeeds with the option
  list; `Prompt` is plain text (no ESC byte); `env.EventID == nil`; per-conn `env.ID`
  monotonic; trust class → `proceed`/`exit` + `exit` default; non-permission class and
  non-modal event → no push, no registry entry; a `Push` error on one conn does not stop the
  fan-out.

No live tui-driver, no relay, no PTY — the producer is exercised through fakes. The
[live wiring (#798)](#live-daemon-wiring-798) adds its own construction + event-routing +
cleanup wiring tests (also fake-driven, no live supervisor/JSONL); the live two-phone e2e
path is #791/#793 (EPIC #597 Phase 3).

## Related

- [codebase/716.md](../codebase/716.md) — ticket record (patterns + lessons);
  [codebase/706.md](../codebase/706.md) — the local resolution arm + cross-head
  first-answer-wins (this surfacer's `handleModalHidden`).
- [codebase/1065.md](../codebase/1065.md) — adds the `conversation_id` outbound
  scoping stamp (§ Security above); the `#1062`-shaped fix applied to this payload.
- [turnevent-package.md](turnevent-package.md) — the internal `PermissionRequest` /
  `PermissionOption` / `PermissionOptionKind` this maps a modal class *into*.
- [codebase/702.md](../codebase/702.md) — the per-device remote-permission **answer gate**
  (the separate authorization #717 enforces; viewing here is ungated beyond `interactive`).
- [turnbridge-package.md](turnbridge-package.md) — the follow-active `resolveTarget` +
  `NewTargetSubscriber` (raw `tuidriver.Event`s) that #798's modal stream reuses wholesale.
- [v2-session-manager.md](v2-session-manager.md) — `Push` / `ActiveConns` / `forwardEnvelope`
  (the push surface this fans out over; `EventID==nil` control envelopes are never dropped).
- [codebase/726.md](../codebase/726.md) — the **inbound actuator** half: the supervisor's
  `AcceptTrust`/`Answer`/`SendEsc` safe-answer seam that turns an abstract modal choice into
  the tui-driver keystroke #717's gated `modal_answer` eventually drives against claude (this
  doc is the outbound `modal_shown` half).
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — no-raw-bytes
  invariant; `docs/protocol-mobile.md` § Modal — the wire field table + security contract.
- **Inbound resolution seam — #727** (landed): introduces the relay-side
  `ModalResolver` seam + `modal_dismissed` broadcast and wires `Resolve` via the
  `cmd/pyry` `modalResolverV2` to resolve `modal_cancel`. See
  [v2-session-manager.md § Inbound modal control](v2-session-manager.md#inbound-modal-control-727--modalresolver-seam--modal_dismissed-broadcast)
  and [codebase/727.md](../codebase/727.md).
- **Gated `modal_answer` — #717** (landed): fills the answer arm of the
  `ModalResolver`. Reads `Registry.Lookup` (no consume) to gate, then `Resolve`
  (consume) only for a fully-authorized answer; maps `option_id` to a keystroke by
  its **1-based position in `Outstanding.Options`** (the surfaced order this package
  records is the single source of truth, doubling as membership validation). See
  [codebase/717.md](../codebase/717.md).
- **Local resolution arm — #706** (landed): the surfacer's `handleModalHidden` resolves a
  locally-answered modal through the shared `Resolve` and broadcasts
  `modal_dismissed{local}`, making resolution single-shot across both heads. See
  [codebase/706.md](../codebase/706.md) and [§ The local resolution arm](#the-local-resolution-arm--handlemodalhidden-706-first-answer-wins).
- **Live producer wiring — #798** (landed): [codebase/798.md](../codebase/798.md) live-wires
  the surfacer into `startRelayV2`, feeding it live `Shown`/`Hidden` events from the
  follow-active stream + the bound host's `ScreenSnapshot`. This is the first non-test caller
  of `newInteractiveModalEmitterV2`; before it, every production modal path (surface + #706
  local resolution + remote `modal_answer`/`modal_cancel` + #725 deny-on-timeout) was inert —
  the registry was never `Record`ed into. The live two-phone e2e capstones are #791/#793.
  See [§ Live daemon wiring (#798)](#live-daemon-wiring-798).
- **Current-truth enumeration — `Registry.Snapshot()` (#876)** (landed): a read seam
  that answers "what is outstanding right now?" for the reconnect-reliability direction
  (#829). See [§ Current-truth enumeration](#current-truth-enumeration--registrysnapshot-876)
  and [codebase/876.md](../codebase/876.md).
- **Connect-time modal reconcile — #877** (landed, `security-sensitive`): the
  `internal/relay` consumer of `Snapshot()` — unicasts the outstanding `modal_shown` set
  to a freshly interactive-open v2 conn via an optional `V2SessionConfig.OutstandingModals`
  seam + `reconcileModals` helper, so a phone that (re)connects while a permission prompt
  is pending is brought to current modal truth. See
  [`v2-session-manager.md` § Connect-time modal reconcile](v2-session-manager.md#connect-time-modal-reconcile-877--outstandingmodals-seam--reconcilemodals)
  and [codebase/877.md](../codebase/877.md).
- **Trust-class e2e capstone — #993** (landed, `security-sensitive`): the trust-class sibling of
  the #791/#793 permission-modal e2e capstones — a live daemon + `fakeclaude`-simulated startup
  trust dialog, driven from a gated interactive phone over the real v2 Noise wire, certifying the
  untrusted-cwd flow end to end: `modal_shown{Class:"trust"}` forwards non-vacuously, the queued
  turn is held (never typed into the consent gate) until an authorized `proceed` answer runs it,
  and a `deny` yields the terminal `session_error{session.blocked, "folder not trusted"}` with the
  turn genuinely never delivered. Rides [#1013](../codebase/1013.md) and [#1014](../codebase/1014.md)
  wholesale; introduces no production behaviour beyond the test-only `fakeclaude` simulation. See
  [codebase/993.md](../codebase/993.md).
