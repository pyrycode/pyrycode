# Spec — Rewire `pyry attach` onto the tui-driver mirror; phone + local terminal coexist (#595)

**Phase 5 / Phase 1, task T4. ADR 025. Size: XS** (architect override S→XS — #593/#594 left only
verification + a thin coexistence guarantee; the production delta is near-zero).
Not `security-sensitive` (confirmed: no label; the security-relevant boundaries are downstream in #596/#597).

## TL;DR for the implementer

The output path is **already wired** (#593) and reliable delivery is **already done** (#594). Nothing in
the production I/O plumbing needs re-plumbing. This ticket **locks the two-heads coexistence invariant behind
deterministic tests** and **documents the Phase-1 input expectation**. The deliverable is tests + one short
doc-comment — no new types, no new files, no signature changes, no consumer cascade.

The one design decision to record (and the reason this is XS): **the existing at-most-one-attacher
(`b.output`) + observer-tap (`b.outputObserver`) shape IS the intended Phase-1 two-heads model.** Attach does
NOT re-seat onto a separate mirror seam. See § Design.

## Files to read first

- `internal/supervisor/bridge.go:53-75` — `Bridge` struct: the two independent output seams (`output` =
  local attach head; `outputObserver` = phone head) and the `attached`/`ErrBridgeBusy` at-most-one guard.
- `internal/supervisor/bridge.go:172-202` — `Write` (fans to BOTH `outputObserver` then `output`; never
  errors) + `SetOutputObserver` (the phone tap). This is the exact fan-out the AC2 test pins.
- `internal/supervisor/bridge.go:204-256` — `Attach` (sets `output`, starts the input pump `in.Read → b.in`)
  and its detach cleanup (`if b.output == out { b.output = nil }`). The local-input + local-output seam.
- `internal/supervisor/supervisor.go:432-444` — `sessionWriter` (`AttachInput → pty.Write`): the local raw-input
  terminus.
- `internal/supervisor/supervisor.go:474-518` — service-mode `runOnce`: `io.Copy(sessionWriter{sess}, Bridge)`
  (local input) and `MirrorOutput() → Bridge.Write` (output). Both heads converge here.
- `internal/supervisor/supervisor.go:199-220` — `WriteUserTurn`: stamps `currentConvID` under `convMu`,
  captures `sess` under `sessMu`, then calls the injectable `deliverFn`. The phone-input terminus.
- `internal/supervisor/supervisor.go:145-154, 350` — the `deliverFn` set-once seam (default
  `deliverViaSession`). AC3 injects a fake here to script a phone turn **without** a live-claude `WaitReady`.
- `internal/supervisor/bridge_test.go:74-199` — existing `OutputObserver_InvokedOnWrite` (observer, no attach)
  and `OutputForwardsWhenAttached` (attach, no observer). **The gap AC2 fills: no test sets BOTH at once.**
  Reuse the `io.Pipe()` "keep the input pump parked so `b.output` stays bound" idiom from these tests.
- `internal/supervisor/supervisor_test.go:145-205` — `TestHelperProcess` modes, esp. **`stdin_to_file`**
  (copies child PTY stdin to a file): AC3's turn-integrity oracle. `helperConfig(...)` builds the `Config`.
- `cmd/pyry/assistant_turn_v2.go:68-78, 181-206` — the phone observer (`Enqueue` copies `p` then drop-on-full;
  `startAssistantTurnBridgeV2` registers it via `SetOutputObserver`). Confirms the observer obeys the
  "copy, don't block, don't retain p" contract the AC2 test relies on.
- `internal/e2e/attach_pty_test.go:140-203` — `TestE2E_Attach_RoundTripsBytes` + `readUntilContains`: the AC1
  local-attach regression oracle (already green against the #593 mirror surface; extend, don't rewrite).
- `internal/e2e/relay_v2_daemon_test.go:285-300` — the `t.Skip("blocked on #603 …")` phone-echo oracle.
  **Read the skip reason; it defines this spec's #603 boundary** (§ The #603 boundary).
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` § Architecture, § Phasing — the
  two-heads diagram, the Phase-1 gate (manual, live-claude), and the Phase-2/3 scope fence.

## Context

After #593 the supervisor hosts claude through a `tuidriver.Session` and the existing `Bridge` already routes
`Session.MirrorOutput()` → `Bridge.Write` → **both** the local attach head (`b.output`) **and** the phone tap
(`b.outputObserver`, set by `assistant_turn_v2.go`). Local attach already renders mirror output and forwards
raw keystrokes (`Bridge.Read → sessionWriter → Session.AttachInput`). After #594 the phone's input path
(`send_message → WriteUserTurn → Session.DeliverPrompt`) is reliable. So **both heads already function**; #595's
job is to lock the coexistence invariant behind tests and make the Phase-1 input expectation explicit — not to
re-plumb anything.

Why now: this is the Phase-1 T4 gate ("a paired phone sees claude's reply stream back AND `pyry attach` works
concurrently"). The gate itself is verified **manually against live claude** (ADR 025 § Verification — "live
claude is the only honest oracle"); the tests this spec adds are the deterministic regression net beneath that
gate.

## Design

### Decision: the existing two-seam shape IS the Phase-1 two-heads model

The ticket asks whether attach should re-seat onto a dedicated mirror seam separate from the phone observer.
**It should not.** The two seams are already independent by construction:

- `Bridge.Attach(in, out)` sets `b.output` — the **local** head, at-most-one (`ErrBridgeBusy` on a second
  attach). Owns the local input pump too.
- `Bridge.SetOutputObserver(fn)` sets `b.outputObserver` — the **phone** head, a non-owning tap.
- `Bridge.Write` snapshots both under `b.mu`, invokes `obs(p)` first, then `out.Write(p)`. Same bytes to both;
  neither seam reads or mutates the other's field.

Re-seating attach onto a "separate mirror seam" would be a refactor with **zero behavioural change** — output
already reaches both heads. Per Simplicity-First + Evidence-Based Fix Selection, there is no observed failure
mode that justifies the re-plumb, and the ticket explicitly scopes #595 as "not to re-plumb the output path."
**Recorded decision, no code.**

The only production artifact: a short doc-comment on the `Bridge` struct (near the existing
`output`/`outputObserver` fields, `bridge.go:64-68`) naming the model — "`output` = local attach head
(at-most-one); `outputObserver` = phone observer head (non-owning tap); `Write` fans to both; the two seams are
independent — neither corrupts the other (Phase-1 two-heads model, ADR 025)." ~5 lines, no logic. This is the
discoverable home for the invariant the tests pin.

### Output coexistence (AC2)

The invariant: with **both** `b.output` (attach) and `b.outputObserver` (phone) set, `Bridge.Write(p)` delivers
the same bytes to both, and a fault on one sink cannot starve or corrupt the other.

- Both healthy → both receive `p` verbatim, in order.
- Local `out.Write` erroring (the mid-detach case) → `Write` swallows it and still returns success; the observer
  already ran. Robust by the `Write` ordering (`obs(p)` precedes `out.Write(p)`) + error-swallow contract.
- The observer obeys "copy `p`, don't block, don't retain past return" (`assistant_turn_v2.go` Enqueue already
  does), so it cannot corrupt the buffer the subsequent `out.Write` reads.

This is a **pure `Bridge`-level invariant** — no claude, no relay, no `WriteUserTurn`. Deterministic unit test.

### Input coexistence (AC3) — the Phase-1 expectation, made explicit

Two writers reach the one `Session`:

- **Local:** `Bridge.Read → sessionWriter.Write → Session.AttachInput(p)` → one `pty.Write(p)`.
- **Phone:** `WriteUserTurn` → (stamp `currentConvID` under `convMu`) → (capture `sess` under `sessMu`, release)
  → `deliverFn` → `DeliverPrompt` → `pty.Write(...)`.

**They share no supervisor-level mutable state.** The local path never touches `convMu`/`sessMu`; the phone path
never touches the `Bridge` input channel. Their only convergence is `sess.pty` — an `*os.File`, whose `Write` is
serialized per call by the runtime's `fdMutex`. So:

> **Phase-1 input expectation (the recorded contract):** the two heads share one input stream with **no
> arbitration and no echo ownership**. Each *single* PTY write (one `AttachInput` chunk; one
> bracketed-paste `WritePrompt`) is atomic against the other. Multi-write delivery sequences (`TypePrompt`
> per-byte; the clear+re-deliver retry) may interleave with concurrent local keystrokes at sub-turn
> granularity — this is acceptable in Phase 1 (one human, not two heads typing the same instant) and is
> **arbitrated in Phase 3 (#597, first-answer-wins / modal ownership), explicitly out of scope here.**

No PTY-write mutex is added. Rationale (Evidence-Based + Belt-and-Suspenders-different-fabric): no interleaving
failure has been observed; the only correct place for such a lock is tui-driver (it owns `s.pty`), not pyrycode;
and #597 owns the two-heads arbitration. Shipping a lock now is a defense for an unobserved failure mode.

### The #603 boundary (load-bearing scope fence)

The ticket names `internal/e2e/relay_v2_daemon_test.go` as the "concurrent-phone path" oracle. That test —
and four siblings — are **skipped, `blocked on #603`**: `internal/e2e/internal/fakeclaude` renders no claude
TUI (`❯` idle / `✻` thinking), so #594's `WaitReady`-gated `WriteUserTurn` never confirms a commit against it
and `send_message` replies offline instead of `ack`. Teaching fakeclaude those glyphs requires a
`cmd/substrate-guard` allowlist exemption — a security-relevant seal decision **owned by #603**, not #595.

**Consequence for #595:** the full daemon-level two-heads e2e (real fan-out + real `send_message` ack, both
heads live) is **deferred to #603** and is NOT a #595 deliverable. #595 instead proves the coexistence
invariants **deterministically at the seam where the fan-out actually happens** (the `Bridge`) and at the
supervisor (the `deliverFn` seam lets a phone turn commit without a live-claude `WaitReady`). This is the
better fabric: a deterministic unit/integration test, not a flaky live-claude e2e. #595 does **not** un-skip,
re-enable, or modify the #603-blocked tests; leave the skips as-is.

This also keeps AC4 (substrate-guard) green by construction: every #595 test uses **synthetic opaque payloads**
(e.g. `pyry-attach-<nonce>`), never a claude-screen literal. The very reason the phone-echo e2e is #603's job
is that it needs claude's literal glyphs — which #595 deliberately avoids.

### What #595 does NOT touch (do not regress these)

- `cmd/pyry/assistant_turn_v2.go` fan-out content (raw chunks → `message` text). Replacing it with typed events
  is #596. Removing/altering it here makes the phone go dark and breaks the Phase-1 gate.
- The `deliverFn` production path (`deliverViaSession`) and #594's commit-confirm contract.
- Any tui-driver code (sealed v1.2.0).

## Concurrency model

- **Output fan-out** runs on the supervisor's single PTY-drain goroutine (`for chunk := range
  sess.MirrorOutput()` → `Bridge.Write`). `Write` snapshots `output`+`outputObserver` under `b.mu`; both sinks
  see each chunk sequentially. No new goroutines.
- **Local input pump:** the `Bridge.Attach` goroutine (`in.Read → b.in`) feeds `Bridge.Read`, copied by
  `io.Copy(sessionWriter{sess}, Bridge)` on the supervisor's input goroutine → `AttachInput`.
- **Phone input:** the relay per-conn goroutine calls `Session.WriteUserTurn` → captures `sess` under `sessMu`,
  releases, delivers. Independent of the input pump.
- **Shared point:** `sess.pty.Write`, serialized per call by `*os.File`. No pyrycode-level lock spans the two
  input paths (by design — see § Input coexistence).
- All AC2/AC3 tests run under `go test -race` (AC4). The race detector IS part of the AC3 evidence: it proves
  the two input paths' supervisor-level bookkeeping (`convMu`, `sessMu`, the `Bridge` channel) has no data race.

## Error handling

- `Bridge.Write` never returns an error (load-bearing: a returning drain goroutine wedges claude). A faulting
  local `out.Write` is swallowed; the observer is unaffected. AC2 pins this with an erroring attached writer +
  a healthy observer.
- `WriteUserTurn` failure modes are #594's contract (`ErrNoLiveSession`, `ErrTurnNotCommitted`, wrapped
  `DeadlineExceeded`) and are unchanged. AC3 uses an **injected `deliverFn`** that returns `nil` (scripted
  commit) so the test exercises coexistence, not delivery semantics (those are #594's tests).

## Testing strategy

Test-first (RED → GREEN, AC5). All under `-race`. Synthetic opaque payloads only (no claude glyphs → AC4 green).

### AC1 — local attach round-trips against the mirror surface (e2e, already green; lock it in)

Extend `internal/e2e/attach_pty_test.go` / `attach_restart_test.go` (do not rewrite; these already run against
the #593-hosted supervisor and pass):

- Confirm `TestE2E_Attach_RoundTripsBytes` and `TestE2E_Attach_SurvivesClaudeRestart` still pass post-#594.
- Add a focused assertion (or a thin new test) that explicitly documents the regression contract: a byte
  written to the attach client's PTY master is forwarded as raw input to claude AND claude's mirror output is
  rendered back to the same master — the two-direction local round-trip, named as the #595 regression lock.

### AC2 — output coexistence (unit, `internal/supervisor/bridge_test.go`)

New table-driven / focused tests pinning the **both-seams-set** invariant the existing tests miss. Scenarios
(inputs → expected), each driving `Bridge.Write` with both `b.output` (via `Attach`, input pump parked on an
`io.Pipe` per the existing idiom) and `b.outputObserver` (via `SetOutputObserver`) set:

- **Both healthy:** `Write("alpha")`, `Write("beta")` → the attached writer receives `"alphabeta"` AND the
  observer is invoked with `"alpha"` then `"beta"`, byte-identical. (Local attach unaffected by an active phone
  observer; phone fan-out keeps delivering while a local attach is active — both directions of AC2.)
- **Faulting local sink, healthy observer:** attached `out` is an always-erroring writer; observer healthy →
  `Write` returns success (n == len) AND the observer still receives every chunk intact. (A mid-detach attach
  cannot starve the phone.)
- **Observer copies, attach consumes same buffer:** assert the observer's recorded bytes are unaffected by the
  subsequent `out.Write` reading the same `p` (the emitter's copy contract). A single scenario with a reused
  caller buffer suffices.

### AC3 — input coexistence (integration, `internal/supervisor/supervisor_test.go`)

Drive a `Supervisor` in service mode (a `Bridge` + a fake-claude child via `helperConfig` + `TestHelperProcess`
mode `stdin_to_file`, which records the child's PTY input to a file). Inject a `deliverFn` that writes a
recognizable phone-turn marker to the captured `Session` (bypassing the live-claude `WaitReady`). Concurrently
feed a recognizable local keystroke marker through the attach path (`Attach`'s input reader → `Bridge.Read →
sessionWriter → AttachInput`). Scenarios:

- **Turn integrity (core):** local marker `L<nonce>\n` (one `AttachInput`) and phone marker `P<nonce>\n` (one
  `deliverFn` write) → the child's `stdin_to_file` log contains **both markers, each contiguous** (neither split
  by the other). Documents the Phase-1 "single PTY writes are atomic" contract.
- **Bookkeeping under race:** run the two paths concurrently under `-race` → no data race; `CurrentConversation()`
  reflects the phone turn's `conversation_id` after `WriteUserTurn` returns; both calls complete without panic
  or deadlock. Proves the `convMu`/`sessMu`/`Bridge`-channel independence.
- **Phase-1 expectation, asserted as a comment + a no-arbitration check:** the test explicitly does NOT assert
  any ordering between the two heads' turns or any echo ownership — a code comment names this as the Phase-1
  contract with the deferral to #597. (This is the "made explicit" half of AC3.)

If the `stdin_to_file` mode needs a trivial tweak to suit a two-marker assertion, that is a test-only change in
`supervisor_test.go` (no production code, no substrate concern).

### AC4 — `make check` green incl. `cmd/substrate-guard`

`gofmt`, `go vet`, `staticcheck`, `go test -race ./...`, and `cmd/substrate-guard` all green. The guard is a
source-literal check; #595 introduces no claude-screen literal (synthetic payloads only). Run
`go test -tags=e2e ./internal/e2e/...` to confirm the AC1 attach tests pass and the #603-blocked tests remain
skipped (untouched).

## Open questions

- **AC1 phrasing — "extend `attach_*`":** the existing tests likely already satisfy AC1's intent post-#593. The
  developer may find the regression lock is a single added assertion rather than a new test. Either is fine;
  prefer the smaller change. If `TestE2E_Attach_SurvivesClaudeRestart` already exercises the mirror across a
  restart, cite it as the AC1 evidence rather than duplicating it.
- **`stdin_to_file` two-marker ergonomics:** if interleaving the two markers in one log file proves awkward to
  assert deterministically, an acceptable fallback is two separate single-head sub-tests (local-only, phone-only)
  plus the `-race` concurrency test — the union still establishes the Phase-1 contract. Decide at implementation
  time; do not add production code to make it easier.

## Out of scope (enforced boundaries)

- Typed event stream / phone-confidentiality conversion (raw chunks → typed wire events) — **#596 (Phase 2)**.
- Two-heads permission/modal ownership, first-answer-wins, `interrupt`/`SendEsc` — **#597 (Phase 3)**.
- Restoring the #603-blocked e2e `send_message` coverage (fakeclaude TUI glyphs / seal allowlist / real-claude
  suite) — **#603**. Do not modify those skipped tests.
- The full daemon-level live-claude two-heads gate — verified **manually** per ADR 025 § Verification, not by a
  fakeclaude e2e in this ticket.
