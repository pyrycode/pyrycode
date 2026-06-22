# `internal/modalbridge` + the modal surfacer — outbound permission/trust modals to phones

The **outbound half** of the daemon-side modal bridge (EPIC #597 Phase 3,
[ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — no-raw-bytes
invariant). When tui-driver detects a permission or trust modal on claude's screen,
the daemon turns it into a typed `modal_shown` event and pushes it to
interactive-capable phones — **never raw PTY bytes**. This slice (#716, split from
#703) establishes the **outstanding-modal registry**, keyed by a one-time `modal_id`
nonce, that #717's inbound resolution half consumes to route answers back and to
reject stale/replayed answers.

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

#717 intercepts an inbound `modal_answer` at `(*V2SessionManager).dispatchAppFrame`
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
// correlation nonce, not a credential).
type Outstanding struct {
    ModalID, Class, Title, Prompt string
    Options                       []protocol.ModalOption
    DefaultOptionID               string
}

type Registry struct { /* sync.Mutex + map[string]Outstanding */ }
func New() *Registry

// PermissionRequestForClass maps a detected modal class → the internal
// PermissionRequest, the wire class string, and ok. ok == false for every class
// that is not permission/trust (those produce no modal_shown). screenText (trimmed)
// becomes PermissionRequest.Title (the human-readable body).
func PermissionRequestForClass(class tuidriver.ModalClass, screenText string) (turnevent.PermissionRequest, string, bool)

// Record is the SINGLE nonce-mint site. Builds the marshal-ready ModalShownPayload,
// mints exactly one fresh modal_id, stamps it, records the Outstanding, returns the
// id-stamped payload. The only error path is RNG failure.
func (r *Registry) Record(req turnevent.PermissionRequest, wireClass string) (protocol.ModalShownPayload, error)

func (r *Registry) Lookup(modalID string) (Outstanding, bool)  // #717's read seam
func (r *Registry) Resolve(modalID string) (Outstanding, bool) // #717's look-up-and-delete one-shot
```

`Lookup`/`Resolve` are **defined now, exercised by #717** — they belong with the
type's contract even though #716 only calls `Record`.

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
and #717 owns deny-on-timeout.

## `modal_id` nonce — the single-writer security primitive

`newModalID` draws a canonical UUIDv4 string from `crypto/rand` (122 bits ⇒ opaque +
unguessable), mirroring `conversations.NewID` (`internal/conversations/id.go`). **Not**
`math/rand`. It is minted **only** inside `Registry.Record`, called **only** from the
surfacer's single goroutine, **exactly once** per `EventKindPtyModalShown` event —
tui-driver's modal axis is rising-edge (one `Shown` per appearance), so one event ⇒
one modal ⇒ one mint, no per-modal de-dup machinery. This is the primitive #717 relies
on to reject stale/replayed answers. `Record` mints **and** stores atomically under the
mutex: no `modal_shown` is ever emitted without a recorded registry entry, and no entry
without a successfully-minted id.

## The surfacer (`cmd/pyry/interactiveModalEmitterV2`)

A **passive state machine** — spawns no goroutine, owns no queue (the `Registry` mutex
is its only synchronisation), same posture as `interactiveTurnEmitterV2`. The single
entry point:

```go
func (e *interactiveModalEmitterV2) Handle(ctx context.Context, ev tuidriver.Event, screenText string)
```

1. `ev.Kind != EventKindPtyModalShown` → no-op.
2. `PermissionRequestForClass(ev.Modal, screenText)`; `!ok` → no-op (non-permission/trust
   class; AC1).
3. `reg.Record(...)` mints the `modal_id` + records the `Outstanding`. RNG failure →
   drop (never push an id-less payload), `Warn` with no payload bytes.
4. Marshal the payload **once**; build a `protocol.Envelope{Type: TypeModalShown, TS: now}`
   with **`EventID` left nil** — a `modal_shown` is a *control* event, not part of the
   turn-event replay ring (`forwardEnvelope` never drops `EventID==nil` envelopes, so
   delivery is real).
5. **Capability-gated fan-out** (exactly `interactiveTurnEmitterV2.emit`'s shape): for
   each `ActiveConns(ctx)`, skip `!c.Interactive` (`modal_shown` rides the `interactive`
   capability, #607), assign a per-conn monotonic `env.ID = ++nextID`, `Push`. A `Push`
   error debug-logs the transport sentinel and **continues** to the next conn (a
   slow/closed conn never blocks the others); `ctx.Err()` → return (teardown).

### Defensive prompt bound

`Prompt` is grid-bounded by construction (a terminal render, KB-scale), but a
`modal_shown` is an **un-droppable control frame** (control frames bypass the push
queue's soft-overflow drop). So `boundPrompt` trims surrounding whitespace and caps the
body at `maxPromptBytes` (4096), backing up to a rune boundary so a multi-byte rune is
never split — a pathological screen render can't inflate the control frame.

## Plain-text / no-raw-bytes (AC3, ADR 025)

The phone receives **typed events only**. `screenText` arrives already rendered to plain
text (the live wiring will feed `Supervisor.ScreenSnapshot()` → `tuidriver.Render`,
ANSI/OSC-free, inside the ADR-025 seal), and `encoding/json` escapes any residual control
byte — so no raw terminal bytes reach the phone. There is exactly one structured path;
**no coarse/raw-byte fallback exists**.

## Concurrency model

- **Surfacer is single-goroutine.** `nextID` is unguarded (no atomic/mutex) — `Handle`
  runs only on the producer's single Run goroutine, the same invariant
  `interactiveTurnEmitterV2` documents.
- **The `Registry` mutex is a deterministic safety net, not goroutine-confinement.** The
  registry is the one piece touched by **two real goroutines**: the surfacer goroutine
  (`Record`) and — in #717 — the relay dispatch goroutine (`Lookup`/`Resolve`). So it
  carries a `sync.Mutex` (a **leaf lock**: O(1) holds, never nested with any other lock).
  Single-writer-nonce (only the surfacer *mints*) does **not** by itself make the map safe
  against #717's concurrent reads, so a real mutex is the right fabric (belt-and-suspenders:
  the safety net is deterministic code).

## Security

`security-sensitive`. Architect's security-review (in the spec) is PASS; both its findings
— the fail-safe deny default and the defensive `Prompt` bound — are implemented and tested.

- **Outbound only.** This slice mints + records; the **inbound boundary** (an untrusted
  phone-asserted `modal_id`) is #717's, out of scope.
- **`modal_id`** = `crypto/rand` UUIDv4, in-memory only, an opaque correlation nonce (not a
  credential). Unbounded-registry-growth is bounded by #717's resolve/timeout; until #717
  lands there is no live producer feeding the registry, so no live growth path exists.
- **The modal body (`title`/`prompt`/`screenText`) is application content and is NEVER
  logged** at any level. Logs carry only content-free discriminants (`event`, `class` (a
  closed set), `conn_id`, `env_id`) + the transport-sentinel `err`.
- **Cross-conversation confidentiality** — *which* screen the deferred live wiring resolves
  as `screenText` is the wiring slice's concern (the property #679 protects). The surfacer
  treats `screenText` as opaque and chooses no screen.

## Shipped unwired — live daemon wiring is deferred (#708)

Per AC4 this slice ships the producer + registry + class mapping with a **unit test
driving a scripted modal through a fake interactive push surface**; there is **no live
`Session.Events()` subscription**. This is the [#632 emitter → #633 wiring] precedent — a
clean, self-contained, unit-tested component. The wiring slice (#708's capstone, or a thin
wiring slice) must: (1) construct the surfacer with the daemon-singleton `*Registry` (the
same instance #717 wires into `dispatchAppFrame`), (2) feed it `EventKindPtyModalShown`
events from the **follow-active** `Session.Events()` stream, and (3) supply the active
session's rendered screen as `screenText`. The named seam: add an `OnModal
func(tuidriver.ModalClass)` callback to `turnbridge.Config` (fired from `Producer.drain`),
and resolve the active bound supervisor's `ScreenSnapshot()` in the wiring closure. "Which
screen / timing of the snapshot vs the modal edge" is the wiring slice's to resolve.

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

No live tui-driver, no relay, no PTY — the producer is exercised through fakes. The live
two-phone path is #708.

## Related

- [codebase/716.md](../codebase/716.md) — ticket record (patterns + lessons).
- [turnevent-package.md](turnevent-package.md) — the internal `PermissionRequest` /
  `PermissionOption` / `PermissionOptionKind` this maps a modal class *into*.
- [codebase/702.md](../codebase/702.md) — the per-device remote-permission **answer gate**
  (the separate authorization #717 enforces; viewing here is ungated beyond `interactive`).
- [turnbridge-package.md](turnbridge-package.md) — the "shipped unwired, injected-seam,
  follow-active producer" template, and the future `OnModal` wiring seam.
- [v2-session-manager.md](v2-session-manager.md) — `Push` / `ActiveConns` / `forwardEnvelope`
  (the push surface this fans out over; `EventID==nil` control envelopes are never dropped).
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — no-raw-bytes
  invariant; `docs/protocol-mobile.md` § Modal — the wire field table + security contract.
- **Consumer (deferred — not in #716):** #717 (inbound `modal_answer` resolution; reads
  `Registry.Lookup`/`Resolve`) and #708 (live wiring + two-phone e2e).
</content>
</invoke>
