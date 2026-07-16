# Spec #1023 — relay: move modal lifecycle + queue reconcile out of v2session.go

**Ticket:** [#1023](https://github.com/pyrycode/pyrycode/issues/1023) — refactor(relay): move modal lifecycle + queue reconcile out of v2session.go
**Size:** S (pure mechanical move; no behaviour change, no exported-API change, no call-site change)
**Security-sensitive:** No — a pure same-package relocation introduces no new design, trust boundary, or data flow to audit. The modal/queue handlers' security posture (untrusted `modal_id`/payload decoded into typed structs, used only as registry keys, never logged/echoed) is preserved byte-for-byte by the move; there is nothing new to review. (Matches PO's refinement; consistent with the #1021/#1022 slices.)

This is the **3rd slice of #964**, continuing the `v2session.go` carve-out after the Noise handshake/hello move (#1021) and the rekey machinery move (#1022). The file is **2875 lines on `main`**; this slice lifts the modal lifecycle + queue-reconcile handlers into a new file in the same `package relay`.

---

## Files to read first

- `internal/relay/v2session.go:52–228` — the header decls. Which are **moved** (`modalDenyTimeout` 52–60, `queuedEnv` 124–133, `pushQueue` 135–143, `enqueue` 145–197, `pushQueueCap` 218–228) versus the **staying interleaved neighbours** (`idleTimeout` 62–75, the `Err*` sentinels 77–101, `wakeKind`/`wakeSignal` 103–122, `snapshotReq` 199–208, `wakeBufferSize` 210–216, `handlerOutboundBuf` 230–236). **Extract:** every moved decl is a *standalone* declaration with its own keyword + doc comment — none shares a `const (…)`/`var (…)`/`type (…)` block with a staying neighbour, so each moves independently without splitting a block.
- `internal/relay/v2session.go:459–512` — the `ModalDismissal` (461–467, **moves**) vs `ModalResolver` (469–499, **STAYS**) boundary, with `ErrSessionUnknown` (459) and `V2SessionConfig` (501+) staying. **Extract:** the cut is between line 467 and line 469; `ModalResolver`'s method signatures reference `ModalDismissal`, which resolves in-package after the move (do **not** move `ModalResolver`).
- `internal/relay/v2session.go:1035–1061` — `ArmModalTimeout` (doc from ~1035, func 1052–1061), a standalone method sandwiched between `armIdleTimer` (1026, stays) and `handleFrame` (1063, stays). **Extract:** it moves alone; nothing around it moves.
- `internal/relay/v2session.go:1550–2010` — `bundleInFlight` ends at 1564 (**stays**) → the **9-method contiguous block 1566–2000** (**moves**) → the settings `const` block at 2002 (**stays**). **Extract:** the whole block from `handleModalCancel`'s doc comment (1566) through `handleDequeueMessage`'s closing brace (2000) is one clean cut — nothing that stays is interleaved.
- `internal/relay/v2session_handshake.go:330–345` — the in-package call sites `m.reconcileModals(ctx, s)` (333) and `m.reconcileQueues(ctx, s)` (343). **Extract:** these call moved methods but **must NOT be edited** — Go resolves same-package identifiers across files, so the reference binds to the new file automatically (this is why AC#3 holds).
- `internal/relay/v2session_rekey.go` (whole file) — the **#1022 prior slice**; use it as the structural template for the new file: `package relay` header + a focused import block containing only what the moved code references.
- `docs/specs/architecture/1022-relay-move-rekey-machinery.md` — the prior slice's spec; same pure-move pattern and conventions.

---

## Context

`internal/relay/v2session.go` was 3744 lines when the 2026-07-15 full-repo review flagged it as the largest readability liability. #1021 (handshake) and #1022 (rekey) have since landed, bringing it to 2875 lines. This slice continues the carve-out by lifting two cohesive concerns that both **reconcile per-session state on reconnect** — the modal lifecycle and the queue reconcile/dequeue handlers (`reconcileModals` and `reconcileQueues` are adjacent in the file and share the reconnect trigger). No behaviour changes; the goal is purely that this reconnect/reconcile machinery reads in isolation and the core file shrinks.

---

## Design

### Target

Create **`internal/relay/v2session_modal.go`** in `package relay`. Relocate the declarations below out of `v2session.go` into it, verbatim (bodies + doc comments unchanged). Because both files are `package relay`, every cross-reference between moved and staying code resolves identically regardless of which file each symbol lives in — this is what makes the move safe and call-site-free.

### Move manifest

Move each of the following from `v2session.go` into `v2session_modal.go`. Line ranges are on current `main`; move the **doc comment with its declaration**.

| # | Declaration | Kind | Lines (incl. doc) |
|---|---|---|---|
| 1 | `modalDenyTimeout` | package var | 52–60 |
| 2 | `queuedEnv` | struct type | 124–133 |
| 3 | `pushQueue` | struct type | 135–143 |
| 4 | `(*pushQueue) enqueue` | method | 145–197 |
| 5 | `pushQueueCap` | const | 218–228 |
| 6 | `ModalDismissal` | struct type | 461–467 |
| 7 | `(*V2SessionManager) ArmModalTimeout` | method | 1035–1061 |
| 8 | `handleModalCancel` | method | 1566–1607 |
| 9 | `handleModalAnswer` | method | 1608–1633 |
| 10 | `handleModalTimeout` | method | 1634–1664 |
| 11 | `broadcastModalDismissed` | method | 1665–1728 |
| 12 | `reconcileModals` | method | 1729–1777 |
| 13 | `reconcileQueues` | method | 1778–1875 |
| 14 | `handleInterrupt` | method | 1876–1918 |
| 15 | `handleNewSession` | method | 1919–1968 |
| 16 | `handleDequeueMessage` | method | 1969–2000 |

Rows 8–16 form **one contiguous block (1566–2000)** — lift it as a single unit. Rows 1, 2–4, 5, 6, 7 are individually standalone; lift each without disturbing its staying neighbours.

Suggested ordering inside the new file (readability, not required): the queue types/const (`queuedEnv`, `pushQueue`, `enqueue`, `pushQueueCap`), then `modalDenyTimeout` + `ModalDismissal`, then the methods grouped modal-first (`ArmModalTimeout`, `handleModalCancel`, `handleModalAnswer`, `handleModalTimeout`, `broadcastModalDismissed`, `reconcileModals`) then queue (`reconcileQueues`, `handleInterrupt`, `handleNewSession`, `handleDequeueMessage`).

### What STAYS in `v2session.go` (do not move)

- **`ModalResolver` interface (469–499)** and the **`V2SessionConfig.ModalResolver` field** — the seam interfaces are their own later slice (per ticket Technical Notes). `ModalResolver`'s signatures reference the moved `ModalDismissal`; that resolves in-package, so leaving the interface in core is correct and requires no edit to it.
- All other seam interfaces and their config fields (`ScreenSnapshotter`, `Interrupter`, `SessionStarter`, `QueueRemover`, `SettingsUpdater`, `SettingsUpdate`, `ErrSessionUnknown`).
- All interleaved neighbours listed in *Files to read first* (close-code consts, `idleTimeout`, `Err*` sentinels, `wakeKind`/`wakeSignal`, `snapshotReq`, `wakeBufferSize`, `handlerOutboundBuf`, `V2SessionState`, the `V2Session` struct, `V2SessionManager`, etc.).
- **Every call site of the moved symbols** — all resolve in-package, none is edited:
  - `dispatchAppFrame` intercepts (`handleModalCancel`/`handleModalAnswer`/`handleInterrupt`/`handleNewSession`/`handleDequeueMessage` at 1159–1171) — in `v2session.go`.
  - `Run` calls `handleModalTimeout` (925) — in `v2session.go`.
  - `Push` calls `(*pushQueue).enqueue` — in `v2session.go`.
  - `reconcileModals`/`reconcileQueues` calls at **`v2session_handshake.go:333,343`** — different file, same package; **not edited** (AC#3 hinges on this).

### Import handling

The new file gets `package relay` + an import block covering exactly what its moved code references. **Determine the exact set by building, not by hand-copying `v2session.go`'s block.** Known-required imports (from the moved code):

- `context` — ctx params on `ArmModalTimeout`, `handleModalCancel`, the reconcile/broadcast methods.
- `encoding/json` — `json.Unmarshal` in `handleModalCancel`/`handleModalAnswer`/`handleDequeueMessage`.
- `slices` — `slices.Delete` in `enqueue` (the **only** `slices.` use in the whole file — see below).
- `time` — `modalDenyTimeout = 2 * time.Minute`; `time.AfterFunc` in `ArmModalTimeout`.
- `github.com/pyrycode/pyrycode/internal/protocol` — `protocol.Envelope`, `protocol.ModalCancelPayload`, `protocol.DequeueMessagePayload`, `protocol.TypeAssistantDelta`, etc.

Add any others the build reveals; `goimports` on the new file is the authority.

**Compiler-forced removal from core (AC#2-permitted):** after the move, `slices` has **no remaining code use** in `v2session.go` (its sole caller `enqueue` moves — verified: exactly one `slices.` occurrence, line 181). `go build ./internal/relay/` will fail on the now-unused `slices` import in `v2session.go` — **remove it**. No other core import becomes unused (`base64`'s only user, `marshalInnerFrameV2` at 2399, stays; every other import has many staying users).

**Import discipline (learned on #1022):** decide by **code** usage, not doc-comment prose. A doc comment that names a package (e.g. mentions "bytes" or "protocol" in prose) is *not* a use — grep `\b<pkg>\.` for real references before concluding an import is or isn't needed. Let the compiler be the final arbiter on both files.

---

## Concurrency model

Unchanged. The moved methods keep their existing goroutine discipline verbatim: `ArmModalTimeout` arms an off-Run `time.AfterFunc` that pushes `modalID` onto `m.modalTimeout` (consumed by Run → `handleModalTimeout`); every other moved method runs on the manager's single Run dispatch goroutine (the `dispatchAppFrame` intercept path or the reconnect reconcile path). `enqueue` runs under `m.pushMu` held by its caller. Relocating a method's source text does not change which goroutine invokes it.

## Error handling

Unchanged. No failure mode is added, removed, or reordered. The fail-closed modal deny-on-timeout, the nil-seam inert guards, and the tolerated-decode-failure paths all move verbatim.

## Testing strategy

- The move is within `package relay`, so **no test file changes** — the existing `v2session_modal_test.go`, `v2session_modalreconcile_test.go`, `v2session_dequeue_test.go`, `v2session_interrupt_test.go`, `v2session_newsession_test.go`, `v2session_queuereconcile_test.go` (and the cross-package `cmd/pyry` modal tests) exercise the moved symbols through the same in-package/exported surface and keep compiling and passing unchanged.
- Build incrementally: `go build ./internal/relay/` after the move to catch the `slices` import removal and any missed import on the new file.
- Gate: **`make check` green** (vet, race test, staticcheck, substrate-guard, e2e) — AC#4. This is the single end-of-work verification.
- `gofmt`/`goimports` both files.

## Acceptance criteria (from ticket)

1. Modal-lifecycle + queue-reconcile decls relocated from `v2session.go` into new `internal/relay/v2session_modal.go`; those decls no longer appear in `v2session.go`. → **Move manifest** above.
2. No exported identifier renamed/added/removed; no logic change beyond compiler-forced import add/removal. → verbatim move; only mechanical delta is the `slices` import (added to new file, removed from core).
3. Only `v2session.go` and the one new file change; no other file touched. → verified: every call site (incl. `v2session_handshake.go`) resolves in-package and needs no edit.
4. `make check` green. → **Testing strategy** above.

## Open questions

None. The cut points, the stay/move boundary (`ModalDismissal` moves, `ModalResolver` stays), the single interleaved-import consequence (`slices`), and the in-package call-site resolution are all verified against `main`.

---

## Scope self-check

Production source files with new/modified content: `internal/relay/v2session.go` (modified) + `internal/relay/v2session_modal.go` (created) = **2**. Under the ≥5 gate. No test/`.md`/spec-file production edits. Confirmed **S**.
