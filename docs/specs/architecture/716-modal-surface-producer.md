# Spec #716 — Surface a tui-driver modal to interactive phones (`modal_shown` + outstanding-modal registry)

**Part of EPIC #597** (Phase 3 / Phase 5: interactive modals). Anchor: [ADR 025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md) (no-raw-bytes invariant) and `docs/protocol-mobile.md` § Modal (v2). Split from #703 (the integration capstone); this is the **surface/outbound** half. The inbound resolution half is **#717** (blocked-by #716), which consumes the outstanding-modal registry this slice introduces.

`security-sensitive` — the security-review pass at the end of this spec is mandatory and PASS-gated.

## Files to read first

- `internal/protocol/messaging.go:88-118` — `ModalOption` + `ModalShownPayload`: the exact wire struct to fill (`ModalID`, `Class`, `Title`, `Prompt`, `Options []ModalOption`, `DefaultOptionID`). Note: no `omitempty` on any field.
- `internal/turnevent/permission.go:1-38` — `PermissionRequest` / `PermissionOption` / `NewPermissionRequest`: the internal type AC1 says to build before serializing. Construct-then-validate-downstream convention.
- `internal/turnevent/taxonomy.go:45-76,112` — `PermissionOptionKind` values (`allow_once`/`allow_always`/`reject_once`/`reject_always`) + `permissionOptionKinds` ordered slice + `.Valid()`. These are the four permission options.
- `cmd/pyry/interactive_turn_v2.go:25-34` — `interactiveBroadcaster` interface (`ActiveConns`+`Push`); **reuse it verbatim** (same package). `:302-362` — `emit()`: the ~25-LOC capability-gated fan-out (marshal once, snapshot conns, filter `Interactive`, per-conn monotonic `env.ID`, `Push`). The surfacer mirrors this.
- `internal/relay/v2session.go:1810-1869` — `(*V2SessionManager).Push` contract (non-blocking, `ErrConnNotFound`, drop-policy). `:1984-2034` — `ActiveConn{ConnID, Interactive}` + `ActiveConns`. `:1947-1982` — `forwardEnvelope`: confirms a control envelope with `EventID == nil` is **never** dropped by the replay-dedup guard.
- `internal/agentrun/ptyrunner/runner.go:513-539` — the existing `for ev := range ch { switch ev.Kind { case EventKindPtyModalShown: … } }` drain shape (detect-and-abort only; reads no snapshot, extracts no options — option/title extraction is net-new).
- `internal/conversations/id.go:7-19` — `NewID`: the `crypto/rand` → UUIDv4 nonce idiom to mirror for `modal_id`.
- `internal/supervisor/supervisor.go:424-446` — `ScreenSnapshot() (text string, live bool)`: renders the live screen to **plain text inside the tui-driver seal** (the substrate-guard-safe source the deferred live wiring will feed as `screenText`).
- tui-driver `pkg/tuidriver/modal.go:19-30` — `ModalClass` constants (`ModalClassPermission = "permission"`, `ModalClassTrustFolder = "trust-folder"`, plus `mcp`/`agents`/`slash-picker`/`ask-user-question`/`model-select`/`permissions-config`). `events.go:84-107,122-127` — modal axis is **rising-edge** (`EventKindPtyModalShown` fires once when a class becomes active; `Event.Modal` carries the class, no title/options).
- `docs/protocol-mobile.md:614-659` — § Modal: the `modal_shown` field table + the security & validation contract (`modal_id` is the sole correlation key; no `conversation_id`).

## Context

Phase 3's outbound modal bridge. When tui-driver detects a permission/trust modal on claude's screen, the daemon turns it into a typed `modal_shown` event and pushes it to interactive phones — **never raw PTY bytes** (ADR 025). This slice establishes the **outstanding-modal registry**, keyed by a one-time `modal_id` nonce, that #717's inbound resolution half consumes to route answers back and to reject stale/replayed answers.

The wire vocabulary already exists (`internal/protocol/messaging.go`, #607/#703-era). The internal `PermissionRequest` already exists (`internal/turnevent`). What is net-new: the **producer** that maps a detected modal class → `PermissionRequest` → `ModalShownPayload`, mints + records the nonce, and fans out to interactive conns.

**Scope boundary (why no live daemon wiring here).** Per AC4, this slice ships the producer + registry + class mapping with a **unit test driving a scripted modal through a fake interactive push surface**; the live two-phone end-to-end run is **#708's capstone**. Subscribing the surfacer to the real follow-active `Session.Events()` stream (which session is active, bound-session snapshot resolution — the full #679 machinery) is the wiring concern and is **deferred** — exactly the [#632 emitter → #633 wiring] precedent. This keeps #716 a clean, self-contained, unit-tested component. See § Open questions for the wiring path #708 (or a thin wiring slice) will take.

## Design

### Package structure

```
internal/modalbridge/         NEW — relay-free modal domain (importable by #717's relay-side resolver, no cycle)
  modal.go                    Registry + Outstanding entry + modal_id nonce + class→PermissionRequest mapping + payload build
  modal_test.go               registry + mapping + payload-invariant tests
cmd/pyry/
  interactive_modal_v2.go     NEW — the surfacer (interactiveModalEmitterV2): drain-one-modal → record → fan out
  interactive_modal_v2_test.go  AC4: scripted modal → fake interactiveBroadcaster
```

**Why `internal/modalbridge` is relay-free.** #717 intercepts `modal_answer` at `(*V2SessionManager).dispatchAppFrame` (in `internal/relay`) and must look the nonce up in this registry. So `internal/relay` will import `internal/modalbridge`. Therefore `internal/modalbridge` MUST NOT import `internal/relay` (would cycle). It imports only `internal/protocol`, `internal/turnevent`, and `pkg/tuidriver` (typed API only — no raw-byte surface). The fan-out (which needs `relay.ActiveConn`) lives in `cmd/pyry`, mirroring the existing emitter — `package main` imports both freely.

### `internal/modalbridge` contracts

**Outstanding-modal registry** (mutex-guarded; shared between the surfacer goroutine and #717's relay dispatch goroutine — see § Concurrency):

- `type Outstanding struct { ModalID, Class, Title, Prompt string; Options []protocol.ModalOption; DefaultOptionID string }` — the recorded entry. Holds **at least the option list** so #717 can map an inbound `option_id` against it (AC2).
- `type Registry struct { … }` + `func New() *Registry` — in-memory `map[string]Outstanding` under a `sync.Mutex`.
- `func (r *Registry) Record(class, title, prompt string, options []protocol.ModalOption, defaultID string) (protocol.ModalShownPayload, error)` — **the single nonce mint site.** Mints a fresh `modal_id` (see below), stores the `Outstanding`, returns the id-stamped `ModalShownPayload`. The only error path is RNG failure (propagate; the surfacer drops + warns without payload bytes).
- `func (r *Registry) Lookup(modalID string) (Outstanding, bool)` — #717's read seam (defined now, exercised by #717).
- `func (r *Registry) Resolve(modalID string) (Outstanding, bool)` — look-up-and-delete, for #717's one-shot consumption (defined now; #716 only needs `Record`/`Lookup`, but `Resolve` belongs with the type's contract — keep it minimal, one method).

**`modal_id` nonce** — `func newModalID() (string, error)`: `crypto/rand` → canonical UUIDv4 string, mirroring `conversations.NewID` (`internal/conversations/id.go:7-19`). 122 bits of entropy ⇒ opaque + unguessable. **Not** `math/rand`. This is the security primitive #717 relies on to reject stale/replayed answers.

**Class → `PermissionRequest` mapping** (pure; AC1's "build an internal `PermissionRequest`"):

- `func PermissionRequestForClass(class tuidriver.ModalClass, screenText string) (turnevent.PermissionRequest, string, bool)` — returns the request, the **wire class string**, and `ok`. `ok == false` for every class that is not permission/trust (AC1: "Non-permission/trust classes produce no `modal_shown`"). Mapping (option (a), the minimal fixed-option-set design the ticket sizes against — **not** screen-scraping labels):

  | `tuidriver.ModalClass` | wire `class` | options (ordered, claude's display order) — `id` = `PermissionOptionKind` string | `default_option_id` (fail-safe) |
  |---|---|---|---|
  | `ModalClassPermission` | `"permission"` | `allow_once`, `allow_always`, `reject_once`, `reject_always` | `reject_once` |
  | `ModalClassTrustFolder` | `"trust"` | `proceed`, `exit` | `exit` |
  | all others (`mcp`/`agents`/`slash-picker`/`ask-user-question`/`model-select`/`permissions-config`/`""`) | — | — | `ok=false` |

  - `PermissionRequest.Title` carries the rendered modal body (`screenText`, trimmed) — its doc says it is "human-readable prompt text". `RequestID`/`ToolCallID` stay `""` (the `modal_id` is the wire correlation key, minted separately).
  - Trust uses two literal options with `Kind` left at the closest ACP kind (or unset — `Valid()` is advisory here, mirroring `NewPermissionRequest`'s no-validate contract); the wire `id`s are `proceed`/`exit`.
  - **`default_option_id` is the DENY option (`reject_once` / `exit`), NOT `options[0]`** — fail-safe for a *remote* security surface (security-review MUST-FIX). `options` stays in claude's display order (allow-first), but the phone's pre-highlighted default is the deny choice so a careless confirm denies rather than allows. It is UI pre-selection only — not an auto-answer (the human still confirms; #702 gates answering; #717 owns deny-on-timeout). Decoupling display order from the default keeps both AC-valid (`default_option_id ∈ Options[].ID`).

**Payload serialization** (AC1's "serializes it into a valid `ModalShownPayload`"):

- `func ModalShownPayload(req turnevent.PermissionRequest, class, title, modalID string) protocol.ModalShownPayload` — maps each `PermissionOption{ID,Label}` → `ModalOption{ID,Label}` (drops `Kind` — not on the wire), sets `Title` (fixed per-class short label, e.g. `"Permission required"` / `"Trust this folder?"`), `Prompt = req.Title` (the rendered body, defensively bounded — see below), `DefaultOptionID` = the fail-safe deny option per the mapping table (`reject_once` / `exit`). **Invariant the producer enforces:** `DefaultOptionID` equals one of `Options[].ID` (true by construction — the deny option is always in the set). Keep this and `Record` consistent: `Record` is the natural home for the marshal-ready payload, so fold this serialization into `Record`'s inputs or call it just before `Record`. Pick one mint+serialize path (one place builds the final payload), to honour single-writer-nonce.
- **Defensive prompt bound (SHOULD).** `Prompt` is grid-bounded by construction (a terminal render, KB-scale), but a `modal_shown` is a *control* envelope and control frames are never dropped by the push queue (soft-overflow admit, `internal/relay/v2session.go:166-174`). Trim/cap `Prompt` to a sane bound (e.g. a few KB) so a pathological screen can't inflate an un-droppable control frame.

> Implementer's note: the cleanest shape is `Record(class, title, prompt, options, defaultID)` → mints id → stores `Outstanding` → returns the finished `ModalShownPayload`. Then `PermissionRequestForClass` + a small options-builder feed `Record`. Exact function split is the developer's call provided (a) the nonce is minted exactly once per surfaced modal and (b) `DefaultOptionID ∈ Options[].ID` holds on every returned payload.

### `cmd/pyry` surfacer contract

`type interactiveModalEmitterV2 struct { reg *modalbridge.Registry; bcast interactiveBroadcaster; logger *slog.Logger; nextID uint64 }`

- `func newInteractiveModalEmitterV2(reg *modalbridge.Registry, bcast interactiveBroadcaster, logger *slog.Logger) *interactiveModalEmitterV2`.
- `func (e *interactiveModalEmitterV2) Handle(ctx context.Context, ev tuidriver.Event, screenText string)` — the single-goroutine entry (mirrors `interactiveTurnEmitterV2.Handle`):
  1. `ev.Kind != tuidriver.EventKindPtyModalShown` → return (no-op).
  2. `req, class, ok := modalbridge.PermissionRequestForClass(ev.Modal, screenText)`; `!ok` → return (non-permission/trust no-op, AC1).
  3. `payload, err := e.reg.Record(...)` — mints `modal_id`, records the `Outstanding`. On `err` (RNG): debug/warn `"relay: modal surface drop; id mint"` (no payload bytes) and return.
  4. Marshal `payload` → `protocol.Envelope{ID: <per-conn>, Type: protocol.TypeModalShown, TS: now, Payload: payloadJSON}` — **`EventID` left nil** (a control event, not part of the turn-event replay ring; `forwardEnvelope`'s dedup never touches `EventID==nil` envelopes).
  5. Fan out exactly like `interactiveTurnEmitterV2.emit` (`cmd/pyry/interactive_turn_v2.go:333-361`): `for c := range e.bcast.ActiveConns(ctx) { if !c.Interactive { continue }; e.nextID++; env.ID = e.nextID; e.bcast.Push(ctx, c.ConnID, env) }`. Marshal the payload **once** before the loop; per-conn monotonic `env.ID`; a `Push` error debug-logs (transport sentinel only) and continues; `ctx.Err()` → return (teardown).

The surfacer spawns **no goroutine** and owns no queue (the `Registry` mutex is its only synchronisation) — same passive-state-machine posture as `interactiveTurnEmitterV2`.

### Data flow

```
tui-driver Session.Events()  ──(deferred live wiring, #708)──>  [single producer goroutine]
                                                                       │
   ev.Kind==EventKindPtyModalShown, ev.Modal∈{permission,trust-folder} │
                                                                       ▼
                          interactiveModalEmitterV2.Handle(ctx, ev, screenText)
                                                                       │
            PermissionRequestForClass(class, screenText) ── ok? ──────┤ (else drop: AC1)
                                                                       ▼
                 Registry.Record(...)  ── mint modal_id (crypto/rand UUIDv4, once) ──> store Outstanding{options,…}
                                                                       │  returns ModalShownPayload (id-stamped, default∈options)
                                                                       ▼
        Envelope{Type: modal_shown, EventID: nil}  ──>  ActiveConns ∩ Interactive  ──>  Push (sealed noise_msg)
                                                                                              │
                                                                       #717 reads Registry.Lookup(modal_id) on modal_answer
```

## Concurrency model

- **Surfacer**: `Handle` runs on a **single goroutine** (the producer's drain loop, once wired). All surfacer fields except `reg` are single-goroutine (`nextID`, no atomic/mutex) — the same invariant `interactiveTurnEmitterV2` documents. For the unit test, the test calls `Handle` serially (no concurrency), matching the production single-goroutine contract.
- **Registry mutex (deterministic safety net, not goroutine-confinement).** The `Registry` is the one piece touched by **two real goroutines**: the surfacer goroutine (`Record`) and — in #717 — the relay dispatch goroutine (`Lookup`/`Resolve`). So it carries a `sync.Mutex` guarding the map; it is **not** confined to one goroutine by convention. Held only around map ops (O(1)); never across the RNG read's failure path in a way that blocks, never nested with any other lock (leaf lock). This satisfies the [belt-and-suspenders: the safety net is deterministic code] principle — a real mutex, because the single-writer-nonce invariant (only the surfacer *mints*) does not by itself make the map safe against #717's concurrent reads.
- **Single-writer nonce (AC2).** `modal_id` is minted **only** in `Registry.Record`, called **only** from the surfacer's single goroutine, **once** per `EventKindPtyModalShown` event. tui-driver's modal axis is rising-edge (`events.go:84-107`: `cur.modal != prev.modal` ⇒ one `Shown` per appearance, `Hidden` on change/dismiss), so one event ⇒ one modal ⇒ one mint. No second writer, no per-modal de-dup machinery needed.
- **Goroutine lifecycle**: the surfacer spawns nothing. (The deferred live producer goroutine's lifecycle is #708/wiring's concern — it will ride the existing `turnbridge.Producer.Run` re-subscribe loop, which already exits cleanly on ctx/Frames-close.)

## Error handling

| Failure | Behaviour |
|---|---|
| `ev.Kind != EventKindPtyModalShown` | silent no-op (not our event). |
| permission/trust class not matched (other class or `""`) | no-op; AC1 ("Non-permission/trust classes produce no `modal_shown`"). Optional debug log with `class` only. |
| `newModalID` RNG failure | drop the modal (do **not** push an id-less payload); `Warn` `"relay: modal surface drop; id mint"` with no payload/screen bytes. Realistically unreachable (crypto/rand). |
| payload marshal failure | drop + debug log (no payload bytes); defensive — `ModalShownPayload` is a closed string/[]struct and does not fail in practice (mirrors `emit`'s posture). |
| `Push` returns `ErrConnNotFound`/`ErrSessionNotOpen`/seal error | per-conn debug log (transport sentinel only), continue to the next conn — a slow/closed conn never blocks the others (Push is non-blocking by contract). |
| `ctx` cancelled mid-fan-out | return (teardown). |

No `modal_shown` is ever emitted without a recorded registry entry, and no registry entry is recorded without a successfully-minted id — `Record` does both atomically under the mutex (AC2's "minted exactly once … and recorded").

## Testing strategy

**`internal/modalbridge/modal_test.go`** (table-driven, stdlib only, `t.Parallel()`):

- `Record` mints a non-empty, canonical-UUIDv4 `modal_id`; the returned `ModalShownPayload.ModalID` matches; a subsequent `Lookup(id)` returns the stored `Outstanding` with the **same option list**; `Lookup(<other>)` → `false`.
- **Nonce uniqueness**: N (≥1000) `Record` calls yield N distinct `modal_id`s (no collisions; opaque/unguessable proxy).
- `PermissionRequestForClass` mapping table: `ModalClassPermission` → `ok=true`, class `"permission"`, 4 options in order `allow_once,allow_always,reject_once,reject_always` (ids == kind strings); `ModalClassTrustFolder` → `ok=true`, class `"trust"`, options `proceed,exit`; `ModalClassMCP`/`SlashPicker`/`ModelSelect`/`AskUserQuestion`/`Agents`/`PermissionsConfig`/`Unknown` → `ok=false`.
- **Payload invariant**: for both matched classes, `DefaultOptionID` equals one of `Options[].ID`, `Options` is non-empty + ordered (claude's display order, allow-first), and `DefaultOptionID` is specifically the **deny** option (`reject_once` for permission, `exit` for trust) — the fail-safe default, not `options[0]`.
- Plain-text: a `screenText` containing only printable text round-trips into `Prompt` unchanged (the source is already Render output; the mapping does not corrupt it). Trimming behaviour asserted.

**`cmd/pyry/interactive_modal_v2_test.go`** (AC4 — the headline test; fake interactive push surface):

- A `fakeBroadcaster` implementing `interactiveBroadcaster`: returns a scripted `[]relay.ActiveConn` (mix of `Interactive: true`/`false`) and records every `(connID, env)` passed to `Push`.
- Drive a **scripted `tuidriver.Event{Kind: EventKindPtyModalShown, Modal: ModalClassPermission}`** + a canned plain-text `screenText` through `Handle`. Assert:
  - Exactly one `modal_shown` envelope (`env.Type == TypeModalShown`) pushed **per interactive conn**, **zero** to non-interactive conns.
  - The pushed `ModalShownPayload.ModalID` is non-empty, equals the surfacer's recorded id, and `registry.Lookup(id)` succeeds with the recorded option list (AC2/AC4).
  - `Payload.Prompt` is the plain-text body — no ANSI/OSC/control bytes (assert no `\x1b`), never raw terminal bytes (AC3).
  - `env.EventID == nil` (control event, not in the replay ring).
  - Per-conn `env.ID` is monotonic.
- A `ModalClassTrustFolder` event → `modal_shown` with class `"trust"` and `proceed`/`exit` options.
- A non-permission/trust event (`ModalClassSlashPicker`) → **no** `Push`, **no** registry entry (AC1).
- A non-`EventKindPtyModalShown` event → no-op.

No live tui-driver, no relay, no PTY — the producer is exercised through fakes, satisfying AC4. The live two-phone path is #708.

## Open questions

- **Live daemon wiring (deferred — #708 capstone or a thin wiring slice, mirrors #633).** The surfacer must be (1) constructed with a daemon-singleton `*modalbridge.Registry` (the same instance #717 wires into `dispatchAppFrame`), (2) fed `EventKindPtyModalShown` events from the **follow-active** `Session.Events()` stream, and (3) given the active session's rendered screen as `screenText`. The natural seam: add an `OnModal func(tuidriver.ModalClass)` callback to `turnbridge.Config` (fired from `Producer.drain` on `EventKindPtyModalShown`), and in the wiring closure resolve the active bound supervisor's `ScreenSnapshot()` (already plain text, seal-safe) before calling `Handle`. The "which screen / timing of the snapshot vs the modal edge" question is the wiring slice's to resolve; it is out of scope here and named so #708 picks it up.
- **`class` wire vocabulary.** This slice introduces `"permission"` and `"trust"`. `docs/protocol-mobile.md:625` says the exhaustive vocabulary is the producer's to finalize — documented, not enforced. Updating the protocol doc's class set is a doc-phase concern, not a developer deliverable here.
- **Trust-option `Kind`.** `proceed`/`exit` have no exact `PermissionOptionKind`. Left unset/closest-kind; `Valid()` is advisory (the wire carries only `id`/`label`). If #717 needs a kind for trust, it can extend the taxonomy then.

## Scope self-check

Production source files (new): `internal/modalbridge/modal.go`, `cmd/pyry/interactive_modal_v2.go` — **2** (well under the 5-file gate; modal.go may split into `registry.go`+`permission.go` within `modalbridge` and stay ≤3). Touches **zero** existing production files (additive; reuses the existing `interactiveBroadcaster` interface without editing it). No interface rename / consumer cascade (edit fan-out = 0). Reject branches: ~5 (under 10). New exported types: `modalbridge.Registry`, `modalbridge.Outstanding` (2). 4 ACs. Comfortable **S**.

## Security review

**Verdict:** PASS (after one MUST-FIX applied inline before commit — see Tokens/Threat-model below).

**Findings:**

- **[Trust boundaries]** This slice is **outbound only** (daemon → authenticated phone). `screenText` (claude's modal body, a subprocess-stdout-derived value) crosses into `Prompt` and onto the wire. It is plain text by construction (the source is `Supervisor.ScreenSnapshot` → `tuidriver.Render`, ANSI/OSC-free, ADR-025 seal — `supervisor.go:432-445`) and `encoding/json` escapes any residual control byte, so no raw terminal bytes reach the phone (AC3). `internal/modalbridge` and the surfacer never name raw screen bytes — `screenText` arrives pre-rendered — so `cmd/substrate-guard` stays green. **The inbound boundary (untrusted phone-asserted `modal_id`) is #717's**, named OUT OF SCOPE; this slice only mints + records.
- **[Trust boundaries — cross-conversation confidentiality]** OUT OF SCOPE — *which* screen the deferred live wiring resolves as `screenText` (active vs another conversation) is the wiring slice's confidentiality concern, the same property #679 protects. The surfacer treats `screenText` as an opaque input and chooses no screen.
- **[Tokens]** `modal_id` = `crypto/rand` UUIDv4 (122 bits), minted **once** per modal in `Registry.Record` — opaque + unguessable, the security primitive #717 relies on. Not `math/rand`. In-memory only (no disk, no log of the body; `modal_id` is an opaque correlation nonce, not a credential). **Lifecycle:** creation here; consumption/removal (`Resolve`) + deny-on-timeout = #717's (OUT OF SCOPE). Unbounded-registry-growth if entries are recorded but never resolved → bounded by #717's resolve/timeout; until #717 lands there is no live producer feeding the registry, so no live growth path exists — SHOULD-FIX in #717, named.
- **[Tokens / Threat model] — MUST-FIX (applied inline).** The first draft set `default_option_id = options[0]` (= `allow`/`proceed`). For a *remote* permission/trust surface the highlighted default must fail **safe**: changed to the **deny** option (`reject_once` / `exit`) while keeping `options` in claude's display order. This is UI pre-selection only (not an auto-answer — #702 gates answering default-OFF, #717 owns deny-on-timeout), but a careless confirm now denies rather than allows.
- **[File operations]** N/A — in-memory registry; no paths, no `os.Stat`/`Open`, no symlinks. `modal_id` is never used as a filesystem path in this slice.
- **[Subprocess execution]** N/A — the surfacer execs nothing. Driving the answer back into claude is #717/#147.
- **[Cryptographic primitives]** `crypto/rand` for the nonce (only crypto here); no hand-rolled crypto, no key/nonce reuse (single-purpose correlation nonce). Constant-time compare is not needed in this slice (no secret comparison); whether #717's inbound `modal_id` match should be constant-time is a #717 note (122-bit entropy + hash-map lookup makes a timing oracle impractical) — named for #717.
- **[Network & I/O]** Reuses the #571 `Push` surface (non-blocking, bounded, Noise-sealed). No new socket / size cap needed. SHOULD-FIX applied: `Prompt` is grid-bounded but a `modal_shown` is an un-droppable *control* frame, so the spec mandates a defensive `Prompt` bound so a pathological screen can't inflate the control frame.
- **[Error messages, logs, telemetry]** The modal body (`title`/`prompt`/`screenText`) is application content and inherits the emitter's no-log discipline — **never logged** at any level. Logs carry only content-free discriminants (`event`, `class` (closed set), `conn_id`) + transport-sentinel `err`. Pinned in § Error handling.
- **[Concurrency]** Surfacer is single-goroutine (`nextID` unguarded, documented contract, mirrors `interactiveTurnEmitterV2`). `Registry` carries a `sync.Mutex` (leaf lock, O(1) holds, never nested) because two real goroutines touch it (surfacer `Record` + #717 dispatch `Lookup`/`Resolve`) — a deterministic safety net, not goroutine-confinement-by-convention. `Record` mints+stores atomically under the lock (no TOCTOU gap). Surfacer spawns no goroutine (no leak).
- **[Threat model alignment]** Aligned with `docs/protocol-mobile.md` § Modal: `modal_id` is the sole correlation key (no `conversation_id`; daemon resolves against its own state); viewing is ungated beyond the `interactive` capability while answering stays gated per-device default-OFF (#702) — this slice only surfaces (viewing), takes no allow/deny decision (no fail-open path), and provides the nonce that lets #717 reject stale/replayed answers.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-22
