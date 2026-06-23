# Spec #706 — two-heads modal ownership: first-answer-wins, local attach dismisses

**Size:** S (PO-sized S; architect holds S — XS-by-LOC but held S for the mandatory `security-sensitive` review pass + the two-ordering cross-head test matrix + the cross-goroutine race argument, mirroring sibling #712's reasoning). **2 production files modified, 0 new files, 1 new exported const, 0 new types/interfaces, 0 consumer cascade, no constructor/wiring change** — see § Scope & sizing.

**Epic:** #597 (ADR 025) Phase 3 — the *two-heads coexistence* leg. This ticket adds the **local** resolution arm and makes modal resolution single-shot across both heads (local `pyry attach` TTY + paired phone). The live producer wiring that feeds the emitter real tui-driver events is **#708** (the Phase 3 capstone, blocked-by this ticket); this ticket is unit-testable against the emitter and the registry directly, no live claude.

---

## Files to read first

- `cmd/pyry/interactive_modal_v2.go:30-137` — `interactiveModalEmitterV2`, the outbound surfacer. **The whole change lives here.** Extract its body into a `handleModalShown`; add a `handleModalHidden`; the `ActiveConns → Interactive → nextID → Push` fan-out loop (lines 113-136) is the shape the local dismissal reuses (factor it into a private helper). Note the struct doc comment's single-goroutine invariant (lines 21-24) — the two new tracking fields must be added to it.
- `internal/modalbridge/modal.go:74-186` — `Outstanding` (75-82), `Registry` + its leaf mutex (84-93), and the one-shot `Resolve` (178-186) **which is the first-answer-wins arbiter** (first caller deletes + gets `ok`; second gets `!ok`). Also `Lookup` (168-173) and `Record` (145-164, the nonce mint site). Read but **do not modify** — the registry contract is complete.
- `internal/relay/v2session.go:376-413` — `ModalDismissal` struct (379) + `ModalResolver` interface (391): the remote arm's source/outcome vocabulary.
- `internal/relay/v2session.go:1601-1656` — `broadcastModalDismissed`: the **relay sibling broadcast site**. The local dismissal must produce a byte-identical `ModalDismissedPayload` envelope shape (`{ModalID, Outcome, Source}`, `Type: TypeModalDismissed`). Read for shape-parity; **do not** call it from the emitter (it iterates `m.sessions` on the Run goroutine and must not be reached from the producer goroutine).
- `cmd/pyry/modal_resolve_v2.go:56-150` — `ResolveCancel` / `ResolveTimeout`: the **precedent pattern** this ticket mirrors on the local side — `Resolve` (consume) → best-effort actuation → `audit.Log` → return a `{Outcome, Source}` dismissal, with **one vocabulary feeding both the wire dismissal and the audit entry**. The no-device-timeout audit shape (empty `DeviceHash`/`DeviceLabel`) is exactly what the local path uses.
- `internal/audit/audit.go:36-81` — `Outcome` consts (43-49, **add `OutcomeDismissedLocal` here**), `Source` consts (57-61, **`SourceLocal` already exists, reserved for #706** at line 60), `Entry` (27-34), `Log` (69-81).
- `internal/protocol/messaging.go:149-162` — `ModalDismissedPayload`: the wire struct; its doc already names the closed `source` set `{remote, local, timeout}` and the producer-defined-sentinel `outcome`.
- tui-driver `pkg/tuidriver/events.go:36-44, 101-128, 263-287` — `EventKindPtyModalHidden` semantics: **`Modal` carries the just-hidden class**; on a class→class change the merge loop emits **`Hidden(old)` immediately before `Shown(new)`**; only one modal is active at a time (the transition only fires on `cur.modal != prev.modal`). `Event.Modal` is a `tuidriver.ModalClass`.
- `cmd/pyry/interactive_modal_v2_test.go` (whole, 205 lines) + `cmd/pyry/interactive_turn_v2_test.go:28-63` — `fakeInteractiveBcast` (records every `Push` into `pushes`; **`ActiveConns` reuses the last snapshot when calls exceed the scripted list**, so one snapshot serves a Shown-then-Hidden sequence), `fakeArmer`, `newModalEmitterTestDeps`, `pushesFor`, `recordedPush`. Reuse all of these.
- `internal/protocol/testdata/modal_dismissed.json` — golden dismissal envelope; the test decodes a pushed envelope as `protocol.ModalDismissedPayload` and asserts `{outcome: "dismissed_local", source: "local"}`.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:134-140` — Security model §4: *"If the local `pyry attach` terminal answers a modal, the binary emits `modal_dismissed{local}`; the phone's pending answer becomes stale and is rejected."* This ticket implements §4.

---

## Context

A surfaced permission/trust modal can be resolved by **two heads**: the local `pyry attach` TTY (operator types `1`/Esc directly into claude) or a paired phone (`modal_answer`/`modal_cancel`). Three failure modes must be closed:

1. **Local resolution is invisible to the phone.** When the operator answers at the TTY, claude's modal vanishes but the phone still shows its prompt — and a later phone tap would route a keystroke into claude's *next* state. The phone must learn the modal is gone.
2. **Double-answer.** A single modal must never be answered twice (e.g. operator denies locally, phone then allows remotely → claude gets two keystrokes).
3. **Internet-sourced action on an already-handled modal.** A remote answer must never act on a modal the operator already resolved locally.

Two facts make this small:

- `modalbridge.Registry.Resolve(modalID)` is already a **mutex-guarded one-shot consume**. Routing *every* resolution — remote answer (#717), remote cancel (#727), timeout (#725), and local (this ticket) — through `Resolve` makes the registry the single first-answer-wins arbiter. This ticket adds **one more `Resolve` consumer**; it introduces no new arbitration mechanism.
- The local TTY answering a modal makes it vanish; tui-driver fires `EventKindPtyModalHidden` for the just-hidden class. The outbound surfacer (`interactiveModalEmitterV2.Handle`) already consumes tui-driver events but currently ignores `Hidden`.

---

## Design

### The shape in one sentence

Teach `interactiveModalEmitterV2` to react to `EventKindPtyModalHidden`: correlate the just-hidden modal to the `modal_id` it minted on the preceding `Shown` (an emitter-tracked outstanding id), `Resolve` it through the shared registry, and — **only if this head won the race** — audit the local resolution and broadcast one `modal_dismissed{source: local}` to every interactive-capable connection.

### Correlation mechanism — emitter-tracked outstanding id (the architect's call)

`EventKindPtyModalHidden` carries the modal **class**, not the `modal_id`. The id was minted in `Registry.Record` on the preceding `Shown`. Two mechanisms were on the table (Technical Notes left it to the architect):

**Chosen: the emitter tracks the outstanding id + class as two single-goroutine fields.** Rejected alternative: a registry "resolve-the-current-entry" method. Rationale:

- The `Registry` is shared across two real goroutines (producer + relay dispatch); its mutex-guarded **keyed-map** contract is minimal and clean. A "resolve whatever is current" method would force the registry to track recency/insertion-order — state it does not need and that muddies a type whose whole value is being a precise keyed one-shot.
- The emitter is the natural owner of *"which `modal_id` did I most recently surface"* and `Handle` is single-goroutine, so the tracking needs **no lock**. The precise `modal_id` is the correlation key; resolving by that id through the shared `Resolve` keeps all first-answer-wins arbitration in exactly one place.
- The single-modal + `Hidden(old)`-before-`Shown(new)` invariants (tui-driver `events.go:37-44, 263-287`) guarantee the tracked id always names the currently-showing modal when a `Hidden` arrives.

Add two fields to `interactiveModalEmitterV2` (and to its single-goroutine doc-comment invariant, lines 21-24 — *both new fields are touched only on the `Handle` goroutine*):

```go
// outstandingID / outstandingClass track the modal whose modal_shown this
// emitter most recently surfaced, so a later EventKindPtyModalHidden can be
// correlated back to its modal_id (Hidden carries only the class). Single-
// goroutine like nextID; "" / zero when nothing is outstanding.
outstandingID    string
outstandingClass tuidriver.ModalClass
```

**Set point.** In `handleModalShown`, immediately after `Record` succeeds (the same point `ArmModalTimeout` is called, `interactive_modal_v2.go:98`): `e.outstandingID = payload.ModalID; e.outstandingClass = ev.Modal`. Setting it here — not after the broadcast — keeps the tracking consistent with the registry entry and the armed timeout even on the (defensive, unreachable) marshal-failure return.

### `handleModalHidden` — the local arm

Behavioural contract (full code is ≤25 lines; the developer writes it in-idiom):

1. If `e.outstandingID == ""` → **return** (nothing this emitter surfaced is outstanding; covers a `Hidden` for a non-permission modal we never recorded). AC1's implicit "for the outstanding modal" gate.
2. If `ev.Modal != e.outstandingClass` → **defensive return without clearing** (warn-log content-free: `event`, hidden class, outstanding class). Unreachable under the single-modal invariant — the showing modal's class always matches — but if tui-driver ever drifts, leaving the tracking intact lets the correct `Hidden` still resolve it. Mark as such.
3. Capture `id := e.outstandingID`; **clear `e.outstandingID = ""` and `e.outstandingClass`** (the modal is gone from the local screen regardless of who consumes the registry entry — clear unconditionally once the class matches, so no return path leaks stale tracking).
4. `out, ok := e.reg.Resolve(id)`. If **`!ok`** → **return** (a remote `modal_answer`/`modal_cancel` (#717/#727) or the timeout (#725) already consumed this `modal_id` — this head is the first-answer-wins **loser**: **no audit, no broadcast, no second `modal_dismissed`**). AC2 / AC3-case-(b).
5. **Winner path** (`ok`): write exactly one audit record, then broadcast one `modal_dismissed{local}` to every interactive conn.

### Local outcome sentinel + audit — one vocabulary

The daemon **cannot observe which option the operator picked** locally — only that the modal vanished. So `modal_dismissed{local}` carries a **producer-defined sentinel** `outcome` (the phone uses it only to clear its prompt), not a real `option_id`.

Add one constant to `internal/audit/audit.go` beside the existing `Outcome` consts:

```go
OutcomeDismissedLocal Outcome = "dismissed_local" // resolved at the desktop TTY; the picked choice is not observable by the daemon (#706)
```

`SourceLocal` (= `"local"`) **already exists** (`audit.go:60`, explicitly reserved for #706). Following the `ResolveCancel`/`ResolveTimeout` precedent, **one value feeds both** the wire dismissal and the audit entry — no second, divergent vocabulary:

- **Wire:** `protocol.ModalDismissedPayload{ModalID: id, Outcome: string(audit.OutcomeDismissedLocal), Source: string(audit.SourceLocal)}`.
- **Audit:** `audit.Log(e.logger, audit.Entry{ModalID: id, ModalClass: out.Class, Outcome: audit.OutcomeDismissedLocal, Source: audit.SourceLocal})` — **no device** (a local TTY resolution has no answering device, so `DeviceHash`/`DeviceLabel` are empty by construction, exactly the no-device case `ResolveTimeout` uses). `ModalClass` comes from the resolved `Outstanding.Class`.

**Why audit the local arm** (not in the AC's literal text, but in scope): ADR 025 §6 requires every modal resolution to be logged, the `internal/audit` feature doc explicitly names *"#706 — the `SourceLocal` path a local resolution would record,"* and all three remote arms audit. A local resolution **retires a one-time nonce and pre-empts a remote answer** — it is exactly as security-relevant as a remote cancel. The emitter already holds `e.logger`; `cmd/pyry` already imports `internal/audit` (in `modal_resolve_v2.go`). Auditing only on the **winner** path (step 5) avoids a phantom `dismissed_local` record for a modal the phone actually answered.

### Two broadcast sites — accepted cross-package duplication, shared helper within the emitter

The remote/cancel/timeout dismissal fans out via `internal/relay`'s `broadcastModalDismissed` (iterates `m.sessions` directly on the Run goroutine). The local dismissal originates on the **producer goroutine** and must reuse the emitter's existing `interactiveBroadcaster` (`ActiveConns` + `Push`) — the exact path `modal_shown` already uses and which is proven safe from the producer goroutine.

- **Across packages: accepted duplication.** The two loops are structurally different (`ActiveConns`+`Push` vs `m.sessions` direct) with different envelope-`ID` conventions (the emitter's per-conn monotonic `nextID` vs the relay's `ID: 1`); `ID` is **non-load-bearing for modal events** (the phone correlates `modal_dismissed` on `modal_id`, per `messaging.go:153-157` and the relay comment at `v2session.go:1641`). The shared contract — the `protocol.ModalDismissedPayload{ModalID, Outcome, Source}` shape — is already a single type in `internal/protocol` and is pinned by `testdata/modal_dismissed.json`. Extracting a cross-package helper would force an awkward dependency for ~3 saved lines; **do not**. Per the project's "Resist over-DRY on duplicated registry/broadcast primitives" guidance.
- **Within the emitter: extract one private fan-out helper.** The `Shown` arm and the new `Hidden` arm would otherwise duplicate the `ts := time.Now().UTC(); for c := range ActiveConns { if !Interactive continue; nextID++; build Envelope; Push }` loop. Factor it:

  ```go
  // broadcastInteractive fans one control envelope of envType+payloadJSON to
  // every interactive-capable conn, mirroring the modal_shown loop: per-conn
  // nextID, capability gate, Push-error-tolerant. Producer-goroutine only.
  func (e *interactiveModalEmitterV2) broadcastInteractive(ctx context.Context, envType string, payloadJSON []byte, pushErrEvent string)
  ```

  Both arms call it (`TypeModalShown` / `TypeModalDismissed`). The push-error log loses the `Shown`-specific `class` field in favour of the generic `pushErrEvent`/`conn_id`/`env_id`/`err` set — acceptable (class is non-essential debug context). The existing `Shown` tests (`TestModalEmitter_PermissionFanout`, `_PushErrorContinues`, the nextID-monotonicity / non-interactive-filter assertions) guard this refactor. *If extraction proves awkward, inline both loops — the hard contract is the identical envelope shape + capability gate + per-conn `nextID`, all already test-covered.*

### `Handle` dispatch

Replace the single `if ev.Kind != EventKindPtyModalShown { return }` guard with a kind switch that delegates to the two arms (default no-op for every other event):

```go
func (e *interactiveModalEmitterV2) Handle(ctx context.Context, ev tuidriver.Event, screenText string) {
    switch ev.Kind {
    case tuidriver.EventKindPtyModalShown:
        e.handleModalShown(ctx, ev, screenText)
    case tuidriver.EventKindPtyModalHidden:
        e.handleModalHidden(ctx, ev) // screenText unused on the hidden arm
    }
}
```

### No wiring / constructor change

`interactiveModalEmitterV2` already has every dependency the local arm needs (`reg`, `bcast`, `logger`). `newInteractiveModalEmitterV2`'s signature is **unchanged**. The live producer that calls `Handle` with real events is **#708**; this ticket does not touch `cmd/pyry/main.go` or any wiring. The same daemon-singleton `*modalbridge.Registry` instance is shared by this emitter and `modalResolverV2` (#708 wires both) — that shared instance is what makes the cross-head `Resolve` arbitration real.

---

## Concurrency model

This is the load-bearing section for a `security-sensitive` cross-goroutine race.

- **Two goroutines, one shared registry.** The local arm runs `e.reg.Resolve` on the **producer Run goroutine** (#708 feeds `Handle`). The remote arms run `Lookup`/`Resolve` on the **relay dispatch goroutine** (`modalResolverV2`, called from `dispatchAppFrame`). They share one `*modalbridge.Registry`; its `sync.Mutex` (a leaf lock taken alone, `modal.go:90`) serializes all map access — **no data race on `outstanding`**.
- **First-answer-wins is structural, not added here.** `Resolve` is a mutex-guarded delete-and-report-presence. Whichever goroutine's `Resolve(id)` runs first deletes the entry and gets `ok`; the other gets `!ok`. Each arm broadcasts/audits **only on `ok`**, so the same `modal_id` is resolved, broadcast, and audited **at most once**, regardless of interleaving.
- **No new keystroke-after-resolution window.** The remote answer path (`ResolveAnswer`, `modal_resolve_v2.go:166-235`) routes its claude keystroke **strictly after** a successful `Resolve` (its "consume FIRST, then route best-effort" commit ordering, Step 4/5). So if the local `Hidden`'s `Resolve` won first, the remote's `Resolve` misses and the remote path **returns before any keystroke** — an internet-sourced answer can never drive a keystroke into a modal the operator already resolved locally (AC2/AC3, ADR 025 §4). The local arm adds no keystroke of its own (the operator already pressed the key at the TTY; the modal is already gone) — it only consumes + notifies. This is a *narrower* surface than the remote arm.
- **Emitter-local fields stay single-goroutine.** `nextID`, `outstandingID`, `outstandingClass` are read/written **only** inside `Handle` (one goroutine), so they need no synchronisation — preserving the emitter's documented invariant. The only field a second goroutine touches remains `reg`, which carries its own mutex.
- **Push from the producer goroutine is safe.** `e.bcast.ActiveConns`/`Push` are the same calls the `modal_shown` path already makes from this goroutine; `Push` is `pushMu`-guarded and Run-safe (`v2session.go` `Push` doc). The local arm does **not** call `broadcastModalDismissed`/`m.sessions` (Run-goroutine-only).

---

## Error handling

- **Loser (`Resolve` → `!ok`):** silent no-op return — no audit, no broadcast, no keystroke. The winning head already notified the phone. This *is* the AC2/AC3-(b) contract.
- **Class mismatch (defensive):** warn-log content-free, return without clearing tracking. Unreachable under the single-modal invariant.
- **Marshal failure (defensive):** `ModalDismissedPayload` is a closed three-string struct and cannot fail in practice; on the defensive branch, drop the broadcast (warn-log, no payload bytes) — the modal is already `Resolve`d/audited, so the registry stays consistent; the missed phone re-syncs on reconnect (mirrors `broadcastModalDismissed`'s marshal-fail handling).
- **Per-conn `Push` error:** tolerated — the fan-out continues to the next conn (the existing `Shown` loop behaviour, preserved by the shared helper; `TestModalEmitter_PushErrorContinues` guards it). A torn-down conn re-syncs on reconnect.
- **Zero interactive conns:** the modal is still `Resolve`d + audited; the broadcast loop simply pushes nothing. Correct (the resolution happened locally regardless of who is watching).
- **`ctx` teardown during `Push`:** return early (the `Shown` loop's existing `ctx.Err()` check, preserved).

---

## Testing strategy

All in `cmd/pyry/interactive_modal_v2_test.go`, reusing `newModalEmitterTestDeps`, `fakeInteractiveBcast` (its `ActiveConns` reuses the last snapshot across the Shown→Hidden call sequence), `fakeArmer`, `pushesFor`. Add a `decodeModalDismissed(t, env) protocol.ModalDismissedPayload` helper mirroring `decodeModalShown`. Tests as scenarios (developer writes them in the table/idiom of the file):

- **AC3-(a) local-first wins, then remote rejected.** One interactive conn. `Handle(Shown, permission)` → assert exactly one `modal_shown` recorded its `modal_id` (capture it from the registry/push). Then `Handle(Hidden, permission)` → assert exactly one `modal_dismissed` pushed with `{outcome: "dismissed_local", source: "local", modal_id: <same id>}`, and the registry no longer holds the id (`reg.Lookup(id)` → `!ok`). Then drive a **remote** `modalResolverV2.ResolveAnswer(id, <opt>, <tok>, gatedDevice)` against the **same registry** → assert `ok == false`, the `fakeKeystroker` recorded **no** keystroke, and **no** second `modal_dismissed` was pushed. *(Reuse the keystroker/device fakes from `modal_resolve_v2_test.go`.)*
- **AC3-(b) remote-first wins, then local emits nothing.** Same setup. `Handle(Shown, permission)`; capture `id`. Drive remote `ResolveAnswer(id, allow_once, tok, gatedDevice)` (or `ResolveCancel`) → consumes + returns a dismissal. Then `Handle(Hidden, permission)` → assert **no** `modal_dismissed` was pushed by the emitter for this id (the emitter's `Resolve(id)` misses), and **no** audit record for `source=local` was emitted.
- **Local Hidden with nothing outstanding = no-op.** `Handle(Hidden, permission)` with no preceding `Shown` → zero pushes, no audit.
- **Hidden for a non-permission modal we never surfaced = no-op.** `Handle(Shown, slash-picker)` (already a no-op), then `Handle(Hidden, slash-picker)` → zero pushes (`outstandingID == ""`).
- **Envelope shape parity.** The pushed `modal_dismissed` payload decodes to a `protocol.ModalDismissedPayload` with all three fields populated, matching `testdata/modal_dismissed.json`'s shape (modulo the `local` source / `dismissed_local` outcome). Non-interactive conns receive zero `modal_dismissed` (capability gate, mirror `TestModalEmitter_PermissionFanout`'s c2 assertion).
- **Audit on the winner only.** Capture `e.logger` output (a `slog` test handler over a buffer); the local-first case emits exactly one `audit: remote permission decision` line with `outcome=dismissed_local source=local modal_class=permission device_hash=""`; the remote-first case emits **no** `source=local` audit line from the emitter.
- **Whole-suite:** `go test -race ./...`, `go vet ./...`, `staticcheck ./...`, `gofmt -l` clean. (Heed the `[[pyrycode-gofmt-dirty-at-head-go1.26]]` lesson: check `git show HEAD:<f> | gofmt -l` before "fixing" any untouched-file formatting.)

---

## Scope & sizing

Production source files (new or modified, excluding `*_test.go` / `*.md` / the spec):

1. `cmd/pyry/interactive_modal_v2.go` — modified (the whole change: dispatch switch, two tracking fields, `handleModalShown` extraction, `handleModalHidden`, shared fan-out helper, audit call).
2. `internal/audit/audit.go` — modified (one new `OutcomeDismissedLocal` const + doc).

**Count: 2** (well under the §4 ≥5-file gate). New exported types/interfaces: **0** (one new const). New files: **0**. Consumer cascade: **0** (no signature/wiring change; the const has one use site). Reject/error branches in the new arm: **~4** (no-outstanding, class-mismatch, resolve-miss/loser, winner). ACs: **3**. Total LOC (production ~55 + audit ~2 + tests ~150 + this spec): **~well under 600**. No red line tripped — **no split**.

---

## Open questions

- **Outcome sentinel string.** `"dismissed_local"` is the producer's choice (the wire `outcome` for cancel/timeout is likewise a producer sentinel, "documented not enforced," `messaging.go:153-155`). The phone treats any unrecognised `outcome` as "clear the prompt," so the exact spelling is non-load-bearing; chosen for unambiguous standalone audit-log filtering. If #708's live e2e or a mobile-side contract pins a different spelling, it is a one-line change in `internal/audit`.
- **Live two-phone e2e** (operator answers at TTY → phone prompt clears) is **out of scope**, deferred to **#708** (the capstone this ticket blocks), per the ticket. This ticket proves the daemon-side arbitration + the local broadcast/audit by unit test only.

---

## Security review

**Verdict:** PASS

*(Note: the canonical `architect/security-review.md` procedure file is not present in this worktree/environment; this self-review follows the established 9-category structure from sibling security-sensitive specs #725 / #712 / #717.)*

**Findings:**

- **[1. Trust boundaries]** No MUST FIX. This slice consumes **no network bytes**. Its only input is a `tuidriver.Event` produced in-process by the local PTY merge loop (#708 wiring) — a trusted, daemon-internal source. The `modal_id` it resolves was **minted internally** by `reg.Record` (crypto/rand nonce) on the preceding `Shown` and tracked in an emitter-local field; it never crosses a network boundary inbound. The untrusted inbound `modal_id` (on `modal_answer`/`modal_cancel`) is handled by #717/#727 on a *different* goroutine — this slice only shares the `Registry` with them, through the mutex-guarded `Resolve`.
- **[2. Tokens, secrets, credentials]** No findings. The local arm takes **no device** (a TTY resolution has no answering device), so the audit `DeviceHash`/`DeviceLabel` are empty by construction (the documented no-device case). No token, key, or secret is read, compared, derived, or logged. The `modal_id` is an opaque correlation nonce, not a credential.
- **[3. File operations]** N/A — no filesystem path is constructed, opened, or written. The audit sink is `log/slog` (write-only, in-process; `audit.go` SECURITY note).
- **[4. Subprocess / external command execution]** N/A — no `exec`, and **no keystroke** is routed by this arm (unlike the remote arms). The operator already pressed the key at the TTY; the local arm only *observes* the resulting `Hidden` and notifies. This is strictly less actuation surface than #717/#725/#727.
- **[5. Cryptographic primitives]** N/A — introduces no crypto. The `modal_id` nonce is minted by `modalbridge` (crypto/rand, unchanged). No new key, nonce, or comparison.
- **[6. Network & I/O]** No findings. No new wire **type** (`modal_dismissed`/`TypeModalDismissed` exist, #701) and no new inbound parse. The outbound `ModalDismissedPayload` is a closed three-string struct carrying only the opaque `modal_id` + sentinel `outcome` + `source` — **no modal body/title/prompt**, so it discloses nothing to a conn that never saw this modal (it ignores an unknown `modal_id`). The dismissal carries no body, so the un-droppable-control-frame size concern (`modalbridge.maxPromptBytes`) does not apply. The capability gate (`c.Interactive`, #607) keeps a non-interactive phone from receiving the event, identical to `modal_shown`.
- **[7. Error messages, logs, telemetry]** No findings. Every log line carries only content-free discriminants — `event`, `modal_id` (opaque nonce), `modal_class` (closed wire set), `conn_id`, `env_id`, and the transport `err` sentinel. **No modal body/prompt/title and no payload bytes are logged** (the emitter's existing SECURITY contract, `interactive_modal_v2.go:25-29`, extended verbatim to the hidden arm). The audit entry carries only non-secret identity (empty device) + opaque `modal_id` + outcome/source; `internal/audit`'s no-leak drift test is a deterministic backstop.
- **[8. Concurrency]** No MUST FIX — the load-bearing category. **Exactly-once resolution across heads is structural** (§ Concurrency): the local `Resolve` and the remote `Lookup→…→Resolve` both serialize on the registry's leaf mutex; the one-shot `Resolve` is the single arbiter, so a race cannot double-resolve, double-broadcast, or double-audit. **No keystroke-after-resolution window:** the remote arm routes its keystroke strictly *after* a successful `Resolve`, so a local resolution that won first makes the remote `Resolve` miss and the remote path return before any keystroke — closing the exact threat the ticket flags ("a post-resolution remote answer routes a keystroke into claude's permission prompt"). The local arm itself routes **no** keystroke. Emitter-local tracking fields stay single-goroutine (no new shared mutable state); no second lock is introduced (no lock-ordering risk); no goroutine is spawned (the emitter remains a passive state machine).
- **[9. Threat model alignment]** Implements ADR 025 §Security model **#4 first-answer-wins across two heads** directly: a local TTY resolution emits `modal_dismissed{local}` and makes a pending remote answer stale (rejected by the one-shot `Resolve`). Reinforces **#3 idempotent + fresh** (a remote answer after a local resolution misses on the consumed nonce) and **#6 audit** (every resolution, now including local, leaves exactly one forensic record). The per-device answer gate (#702) is **correctly not consulted** here — a local resolution is the trusted operator acting at the physical TTY, not a gated remote grant. Live two-phone e2e validation is out of scope, deferred to **#708** (the capstone this ticket blocks).

**Reviewer:** architect (self-review; canonical procedure file absent — structure mirrors #725/#712/#717)
**Date:** 2026-06-23
