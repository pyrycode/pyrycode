# Spec #794 — Live e2e capstone: phone interrupt stops the running turn

**Part of EPIC #597** (Phase 3 live exit gate). Split from #708. Mirrors the Phase 2 capstone #642 and the sibling #792 (queue-drain) / #791 (modal) live capstones. `security-sensitive`.

## Verdict up front: reuse the harness, one small fakeclaude extension, ship as one S ticket

This is a **test/harness-only** capstone. The remote-interrupt production code already shipped and was deterministically proved upstream: #707 (the `interrupt` wire type + `Interrupter` seam + Esc routing), #726 (the sealed `supervisor.SendEsc()` keystroke surface), and the mobile send + busy-affordance UI. The interrupt path is **wired live** — `cmd/pyry/relay.go:341` sets `Interrupter: sup` on the `V2SessionConfig`, so an interactive phone's `interrupt` frame routes through `V2SessionManager.handleInterrupt` → `supervisor.SendEsc()` → one Esc into the supervised claude. This ticket lands the **live** confirmation over one daemon + a real (fake) claude that is **mid-turn** when the interrupt arrives and whose turn **ends as a direct result of the Esc**. It confirms; it does not re-prove — the correctness oracle stays upstream (#707's unit tests).

**The one real design decision (the PO flagged it):** the sibling #792 harness flips claude busy→idle on a *file* trigger, not on the Esc byte. If #794 reused that shape, an "interrupt stopped the turn" assertion could pass **vacuously** — the idle came from a file, not the interrupt. So this spec makes **the Esc byte itself the cause of the turn ending**: fakeclaude detects the interrupt's bare ESC on its stdin and, only then, appends a canned end-of-turn line to its session JSONL. The daemon's structured-turn producer tails that line and emits the `turn_end` envelope (the daemon's "turn stopped" signal, AC3). No Esc ⇒ no end-of-turn line ⇒ no `turn_end`. Causality is structural, not temporal.

**Single flow — no split.** AC1 (mid-turn), AC2 (Esc received), AC3 (turn stopped by the interrupt) are one causal chain in one run; splitting them produces vacuous halves (a "turn stopped" with no proof it was running, or an "Esc received" with no proof it did anything). This is the [[po-plus-title-capstone-is-one-flow]] shape.

## Files to read first

The developer's turn-1 data load. Read these before writing code.

- `internal/e2e/relay_two_phone_structured_test.go` (whole file, ~500 lines) — **THE primary template.** #794 is this test, trimmed and re-pointed: pair ONE interactive phone (drop phone B / the non-interactive negative — not relevant here), drop only a **single mid-turn assistant-text line** via the JSONL trigger (not the full 4-line fixture), then send a sealed `interrupt` frame and observe the `turn_end` the interrupt causes. Lift verbatim: `RunBareIn(... "pair" ...)` + `decodePairPayload`, `seedBoundConversation`, the sessions-dir alignment + pre-create-`<initialUUID>.jsonl` recipe (`:130-154`), `driveHandshakeToOpenDaemonInteractive` + `buildHelloEarlyInteractive`, the `send_message` + sealed-ack fence (`:204-247`), the single-deadline decrypt-drain loop over `phoneA.ReceiveBytes` / `InnerFrameV2` / `decryptInnerEnvelope` (`:286-326`), and the JSONL-trigger drop (`:260-269`).
- `internal/e2e/internal/fakeclaude/main.go` — **the file to extend.** Read: `startStdinReader` (`:315-352`, the single stdin loop — where the bare-ESC scan lands; note it already sets `turnPending` on any bytes and emits the TUI spinner once); the `turnPending atomic.Bool` → main-loop `appendTurnGrowth(f)` signal→append pattern (`:118-125`, `:192-194`, `:284-289` — the exact shape the new `escPending`/`appendTurnEnd` mirrors); `emitStructuredJSONLIfTriggered` (`:236-249`, the write+fsync recipe `appendTurnEnd` copies); the env-const block (`:86-97`) and package doc (`:9-67`). Already on the `cmd/substrate-guard` allowlist (#603); this change adds no new glyph.
- `internal/relay/v2session.go` — the shipped `handleInterrupt` + the `dispatchAppFrame` `TypeInterrupt` intercept (per #707 spec §4, near the `handleModalCancel` cluster). **Read-only** — confirm the live path (interactive-gated, `SendEsc`, no payload, no reply). Nothing here changes.
- `internal/supervisor/modal.go:62-66` (`SendEsc → sendModalKey(keyEsc, "")`) + tui-driver `pkg/tuidriver/keys.go:56-60` (module cache: `func (s *Session) SendEsc()` = `writeRaw([]byte{0x1b})`). **The load-bearing fact:** the interrupt writes exactly **one lone ESC byte** (`0x1b`), nothing after it.
- tui-driver `pkg/tuidriver/session.go:288-313` (module cache) — `WritePrompt` wraps the delivered prompt as `\x1b[200~<text>\x1b[201~\r`; **every paste-marker ESC is `0x1b` immediately followed by `[` (`0x5b`)**. Combined with #749 (the delivered prompt content is raw-ESC/C0-free), this is why a `0x1b` **not** followed by `0x5b` is unambiguously the interrupt — the bare-ESC oracle (AC2) and fakeclaude's detector both key off it.
- `cmd/pyry/interactive_turn_v2.go:130-195` — the emitter state machine. **Read-only, but it pins AC1 and AC3:** `TextChunk → transitionTo(StateResponding)` (`:147-160`) is the `turn_state(responding)` AC1 signal; `TurnEnd → emit turn_end` then `transitionTo(StateIdle)` (`:185-193`) is the AC3 signal. Proves which JSONL line produces which wire envelope.
- `internal/turnbridge/mapper.go:20-62` — the JSONL→turnevent mapping that dictates the two fakeclaude lines' exact shapes: `type:"assistant"` text block → `TextChunk`; `EventKindJsonlEndOfTurn` (fires only on assistant + `stop_reason=="end_turn"` + non-empty text) → `TurnEnd{Reason: end_turn}`.
- `internal/protocol/interactive.go:16-77` — `TurnStatePayload{ConversationID, State ∈ "thinking"/"responding"/"idle"}` and `TurnEndPayload{ConversationID, TurnID, StopReason}` — the wire shapes the test decodes.
- `internal/e2e/harness.go:304-360` — `StartRotationWithRelay`: sets `PYRY_FAKE_CLAUDE_STDIN_LOG`, forwards `-pyry-relay`, and appends `extraEnv ...string` verbatim (`:332`). Pass the three env vars (§ Design Part 2) through `extraEnv`; **no harness.go change needed** (the #642 seam).
- `docs/specs/architecture/792-queue-drain-two-phone-e2e-capstone.md` — the sibling capstone: the fakeclaude-extension shape (env const + one-shot signal + main-loop append), the single-deadline fakephone read discipline, and the **AC4-scoped-out** disposition #794 mirrors exactly.
- `docs/specs/architecture/707-interrupt-wire-type-esc-routing.md` — the shipped interrupt path. Nothing to build in production; this is context for what the frame does on arrival.

## Context

Phase 3 of the mobile structured stream. A paired interactive phone can interrupt the running claude turn — the remote equivalent of pressing **Esc** at the local terminal. Every production piece shipped and was unit/integration-proved: the `interrupt` wire type, the `interactive`-capability gate, the `Interrupter` seam, and its live wiring at `cmd/pyry/relay.go:341`. What is **not** yet proved is the **live** end-to-end behaviour: over one running daemon and a real supervised child that is genuinely mid-turn, an `interrupt` sent from the phone reaches claude as an Esc, the running turn ends **because of that Esc**, and the daemon reports the turn stopped to the phone. This capstone is that live confirmation. **Test/harness code only** — a surfaced production gap is a separate ticket.

## Design

Two files: extend the fakeclaude stand-in (1 production file, ~40 LOC additive) and add one `//go:build e2e` test (1 new file).

### Part 1 — fakeclaude: an "Esc-ends-the-turn" mode (~40 LOC, additive)

The mechanism that makes the interrupt the **cause** of the turn ending. New env `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` (a boolean flag — any non-empty value, mirroring `PYRY_FAKE_CLAUDE_TUI`; **not** a path). Contract:

- **When unset (default): byte-identical to today.** Every existing caller is unperturbed.
- **When set:** the stdin reader watches the byte stream for a **bare ESC** — a `0x1b` that is **not** the lead byte of a CSI/paste escape sequence (i.e. not immediately followed by `0x5b` `[`). On the first bare ESC it signals the main poll loop, which appends **one canned assistant end-of-turn line** to the live session JSONL and fsyncs. The daemon's structured-turn producer tails that line → `turnevent.TurnEnd` → `turn_end` envelope to the interactive phone. One-shot (a second Esc is inert — a re-interrupt of an already-ended turn is a no-op, matching claude's own behaviour).

**Bare-ESC detection — the crux.** In this harness the stdin stream carries exactly three ESC sources, and only one is bare:

| Source | Bytes | Bare? |
|---|---|---|
| Bracketed-paste open (prompt delivery) | `0x1b 0x5b 32 30 30 7e` (`ESC[200~`) | no — ESC is followed by `[` |
| Bracketed-paste close (prompt delivery) | `0x1b 0x5b 32 30 31 7e` (`ESC[201~`) | no — ESC is followed by `[` |
| The interrupt (`SendEsc`) | `0x1b` alone | **yes** |

The delivered prompt **content** between the markers is raw-ESC/C0-free (guaranteed by #749's paste-content guard), so it contributes no `0x1b`. Therefore **a `0x1b` not immediately followed by `0x5b` is the interrupt, and nothing else.** Detection rule, applied over each `os.Stdin.Read` buffer: a `0x1b` at index `i` is a bare ESC iff `i+1 < n && buf[i+1] != 0x5b`, **or** `i == n-1` (the ESC is the buffer's last byte). The `i == n-1` arm is safe because the paste markers are written by tui-driver as a single `writeRaw` of the whole `ESC[200~…ESC[201~\r` unit (a sub-buffer-sized prompt), so a paste marker's `0x1b` is never the last byte of a read — its `[` always follows in the same read; whereas `SendEsc` writes the lone `0x1b` as its own PTY write, so it arrives as a standalone (or trailing) byte. Document this invariant in the detector comment; the test keeps the prompt short and plain-ASCII to guarantee paste atomicity (§ Part 2, step 4).

**Single-writer-of-`f` preserved.** The stdin reader must **never** write `f` (the session JSONL) — that is the main goroutine's job (`turnPending` doc, `:118-125`). So the reader sets a new `escPending atomic.Bool`; the main poll loop, gated on the mode, does `if escPending.Swap(false) { appendTurnEnd(f) }` next to the existing `turnPending`/`appendTurnGrowth` line. The bare ESC also trips `turnPending` (any stdin bytes do) → an extra inert `{}\n` growth line — harmless (maps to `(nil,false)`, invisible to every assertion) and it satisfies no pending commit (the interrupt is not a `WriteUserTurn`).

New symbols, **signatures + behaviour only** (developer writes the bodies, mirroring `appendTurnGrowth` / the `rotated` one-shot gate):

```
// escPending signals — from the stdin reader to the main poll loop — that a
// bare ESC (the remote interrupt keystroke) was read, so the main goroutine can
// append the canned end-of-turn line. Signal only: the reader never writes f
// (single-writer-of-f, like turnPending). Gated by envEscEndsTurn.
var escPending atomic.Bool

// appendTurnEnd grows f by one canned claude-format assistant end_turn line so
// the daemon's structured-turn producer maps it to turnevent.TurnEnd -> a
// turn_end envelope: the daemon's "turn stopped" signal. Runs ONLY on the main
// goroutine (single-writer-of-f). Best-effort + fsync, mirroring
// emitStructuredJSONLIfTriggered. The line is a fixed literal (see below).
func appendTurnEnd(f *os.File)
```

The canned line is the shape the mapper requires for `EventKindJsonlEndOfTurn` (mirror `relay_two_phone_structured_test.go:264`): `{"type":"assistant","message":{"id":"<fixed>","stop_reason":"end_turn","content":[{"type":"text","text":"<fixed>"}]}}`. It is inert JSONL data, **not** a TUI substrate glyph, so the `cmd/substrate-guard` allowlist is unaffected (the guard flags the `❯`/`✻` runes, not JSON literals — but the developer should run `make substrate-guard` to confirm).

**Coexists with `PYRY_FAKE_CLAUDE_TUI`** (unlike #792's idle-trigger, which is mutually exclusive with TUI). This test runs TUI mode ON (so `WaitReady` confirms fast and the `send_message` ack is prompt, exactly as #642) **and** `ESC_ENDS_TURN`. The two touch different bytes: TUI mode emits the startup `❯` + one spinner on first stdin; the ESC detector scans for the bare ESC. No conflict.

Add the env const to the const block and a package-doc paragraph mirroring the `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` doc. To keep the eventual merge with any sibling fakeclaude change trivial, confine the diff to: one new const, one new `atomic.Bool`, the scan inside `startStdinReader`, one gated main-loop call, one new helper — do not reflow the existing const block or `main`.

### Part 2 — the test (`internal/e2e/relay_v2_interrupt_test.go`, new, `//go:build e2e`)

Test name e.g. `TestRelayV2_InterruptStopsRunningTurn`. Lift #642's structure. One interactive phone. Choreography:

1. **Seed the harness exactly as #642:** pair one device (interactive), `decodePairPayload`; `seedBoundConversation(home, knownConvID, initialUUID)`; align `sessionsDir = filepath.Join(home, ".claude", "projects", encodeWorkdir(home))`, `MkdirAll`, and **pre-create `<initialUUID>.jsonl`** (`{}\n`) so the producer resolves at a tiny offset at startup and every appended line lands in the tailed range.
2. **Start fakeclaude in TUI + Esc-ends-turn mode:** `StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog, fr.URL()+"/v2/server", "PYRY_MOBILE_V2=1", "PYRY_FAKE_CLAUDE_TUI=1", "PYRY_FAKE_CLAUDE_JSONL_TRIGGER="+jsonlTrig, "PYRY_FAKE_CLAUDE_ESC_ENDS_TURN=1")` — `neverRotate` is a path never created; `jsonlTrig`/`stdinLog` distinct paths under `t.TempDir()`.
3. `waitBinaryHello`; `fakephone.Dial`; `driveHandshakeToOpenDaemonInteractive` (grants `interactive`, the capability both the structured stream **and** `handleInterrupt` require).
4. **Start the turn:** send one `send_message{knownConvID, plain-ASCII marker text}` and drain to its sealed ack (the #642 fence — WriteUserTurn ran → active cursor stamped on `knownConvID` → the producer follows the bound session's JSONL; the prompt is delivered as bracketed paste, now in the stdin log). Keep the prompt **short and plain-ASCII** (no ESC) so the paste is atomic and contributes no bare ESC.
5. **Establish + observe the mid-turn window (AC1):** drop the JSONL trigger with a **single** assistant-text line (`relay_two_phone_structured_test.go:261` line-1 shape). fakeclaude appends it → producer → `TextChunk` → emitter emits `turn_state{State:"responding"}` (+ an `assistant_delta`). **Drain phone A until a `turn_state` with `State != "idle"` (expect `"responding"`) is decoded** → AC1: the turn is provably running / had **not** already ended. **Vacuous-pass guard #1:** `t.Fatal` (naming the "harness never started a turn" mode) if no non-idle `turn_state` is seen before a ~20 s deadline.
6. **Interrupt (AC2 setup):** seal `protocol.Envelope{Type: protocol.TypeInterrupt}` (no payload; an `ID`) with phone A's send CipherState and `sendNoiseMsg`. The daemon's `dispatchAppFrame` intercepts it → `handleInterrupt` (interactive ✓) → `SendEsc()` → lone `0x1b` to fakeclaude's stdin.
7. **Observe the turn stop caused by the interrupt (AC3):** fakeclaude's detector fires on the bare ESC → `appendTurnEnd` → producer → `TurnEnd` → emitter emits `turn_end`. **Drain phone A until a `turn_end` envelope (`ConversationID == knownConvID`) is decoded** → AC3: the daemon reports the turn stopped. **Vacuous-pass guard #2 (structural causality):** the test **never** drops an end-of-turn line via the JSONL trigger — the *only* source of an end-of-turn JSONL line, hence of `turn_end`, is fakeclaude's bare-ESC handler. Observing `turn_end` therefore proves the Esc was received and processed. `t.Fatal` (naming the "interrupt did not stop the turn" mode) if no `turn_end` before a ~10 s deadline.
8. **Direct keystroke oracle (AC2), belt-and-suspenders with different fabric:** read `stdinLog` and assert it contains a **bare ESC** — a `0x1b` byte not immediately followed by `0x5b` (scan the decoded bytes with the same rule as the detector). Every paste-marker ESC is `0x1b 0x5b`, so a bare `0x1b` is unambiguously the interrupt. This is fakeclaude's own record of what it received; the `turn_end` (step 7) is the daemon's independent wire report — two oracles, different sources.

Assertion order is load-bearing: AC1 (turn running) is a precondition of the interrupt being sent, and AC3's `turn_end` can only come from the Esc, so the positives gate the causal claim. Steps 7 and 8 are both post-interrupt and mutually reinforcing.

## Concurrency / timing model

No new production goroutines (test-only). Pre-existing actors and the deterministic fences:

- **Single-writer-of-`f`.** The stdin reader only *signals* (`turnPending`, new `escPending`); the main poll goroutine performs every `f` write (`appendTurnGrowth`, `emitStructuredJSONLIfTriggered`, new `appendTurnEnd`). The new path adds no writer of `f` and no lock — identical to the shipped `turnPending` discipline.
- **File-order determinism.** All three appenders run on the one main goroutine, sequentially per poll cycle. The mid-turn text line is appended (JSONL trigger, step 5) strictly before the end-of-turn line (bare ESC, step 7) because the test only sends the interrupt after observing `turn_state(responding)`. The producer tails `f` in write order, so `turn_state(responding)` precedes `turn_end` on the wire.
- **fakephone read discipline (603.md/634.md):** reuse #642's single-deadline decrypt-drain (`ReceiveBytes` under one long deadline, back-to-back reads); never a short-timeout poll on a phone conn.
- **fsync visibility:** fakeclaude fsyncs the stdin log per write and `f` per append (existing behaviour + `appendTurnEnd` mirrors it) — cross-process APFS visibility handled. The step-8 stdin-log read happens-after the step-7 `turn_end` (which implies the Esc was read and the end-of-turn line committed), so the bare ESC is on disk by then.
- **Timing budget:** send/ack and each envelope push are sub-second. No busy-wait timer: the turn's lifetime is bounded by the test sending the interrupt (deterministic, not a race). ~20 s AC1 deadline and ~10 s AC3 deadline mirror #642/#792; `-count=3` for determinism (#603).

## Error handling

- **Harness never started a turn** (no non-idle `turn_state`) → step-5 `t.Fatal` (vacuous-pass guard #1).
- **Interrupt did not stop the turn** (no `turn_end` after the interrupt) → step-7 `t.Fatal` (vacuous-pass guard #2): either the Esc never reached claude or the detector didn't fire. The step-8 bare-ESC assertion then localizes which.
- **Bare ESC absent from the stdin log** → step-8 `t.Fatal`: the interrupt frame never routed to `SendEsc` (e.g. a capability-gate regression). Distinct from guard #2, which is the daemon-side wire report.
- Handshake / seal / decrypt errors → `t.Fatalf`, consistent with #642.

## Testing strategy — AC mapping

One new `//go:build e2e` test asserting AC1-AC3 in a single run:

- **AC1** (mid-turn positive check): step 5 — a `turn_state` with `State != "idle"` decoded on phone A **before** the interrupt is sent; `t.Fatal` on absence.
- **AC2** (Esc reaches claude): step 8 — a bare ESC (`0x1b` not followed by `0x5b`) present in the stdin log; the direct keystroke oracle.
- **AC3** (turn ends *because of* the interrupt, daemon reports stopped): step 7 — a `turn_end` envelope on phone A, whose only possible source is fakeclaude's bare-ESC handler (structural causality); `t.Fatal` on absence.
- **AC4** (flight-recorder audit): **not asserted in this CI test** — see § AC4.
- **Vacuous-pass guard:** the two ordered `t.Fatal`s (turn running; then turn ended via the Esc-only path) make an "interrupt stopped the turn" pass impossible over a turn that never ran or a `turn_end` that came from anywhere but the Esc.

**Stop-reason fidelity caveat (honest scope).** The `turn_end` carries `StopReason == "end_turn"`, not `"cancelled"`: tui-driver v1.3.0's `EventKindJsonlEndOfTurn` cannot distinguish an interrupt-stop from a normal end (documented at `internal/turnbridge/mapper.go:25-28`), so the mapper always yields `Reason: end_turn`. This is orthogonal to what the test proves — **causality** (the `turn_end` exists only because the Esc was received), not stop-reason semantics. The daemon's "turn stopped" signal *is* `turn_end` regardless of reason. The test asserts `turn_end` is received; it does **not** assert `StopReason == "cancelled"` (that would fail on a tui-driver limitation, not a real defect). A future tui-driver that surfaces a distinct interrupt reason is a separate follow-up (§ Open questions).

Gates the developer runs green: `go build ./cmd/pyry`, `go vet ./...`, `staticcheck ./...`, `go test -race ./...`, `make substrate-guard`, and `go test -tags=e2e -run TestRelayV2_InterruptStops ./internal/e2e/...` (ideally `-count=3`). The `e2e_realclaude` column is untouched.

## AC4 — flight-recorder reachability (scoped out of CI, mirrors #792 AC4 / #791 AC5)

AC4 ("The run is recorded by the flight recorder for the audit trail") is **not assertable by this test in any variant**, for the identical reason #792 and #791 already resolved: the flight recorder (`.cast` capture) lives in `internal/agentrun/ptyrunner`, gated on `PYRY_RECORD_DIR`, and is reached **only** from the `pyry agent-run` / dispatcher path — not the mobile-relay path, where claude is hosted by `internal/supervisor` (which wires no `RecordTo`/`PYRY_RECORD_DIR`). The two code paths are disjoint; no single run is both "a mobile interrupt" and "recorded by the flight recorder." Per [[po-audit-artifact-ac-check-harness-reachability]], the architect scopes the AC to the variant whose import reaches the artifact — here, none on the mobile path.

**Disposition:** the CI test asserts AC1-AC3. AC4 is deferred to the operator live run (`docs/knowledge/features/mobile-live-e2e-runbook.md`), consistent with the siblings. It is **not** a developer deliverable and requires no test code. This is an architect clarification, not PO rework — the siblings resolved the identical AC this way.

## Scope (S confirmed)

- **Production source files (`.go`, non-test):** **1** — `internal/e2e/internal/fakeclaude/main.go` (~40 LOC: one env const, one `atomic.Bool`, the bare-ESC scan in `startStdinReader`, one gated `main`-loop call + one-shot bool, one `appendTurnEnd` helper, one package-doc paragraph). Purely additive: unset env ⇒ byte-identical. Far under the §4 ≥5-file gate.
- **New files:** **1** — the e2e test. Under the >3 gate. **No harness.go change** (the `extraEnv` seam already exists).
- **Total LOC:** ~40 (fakeclaude) + ~280 (test) ≈ **320**. Under ~600.
- **New exported types:** 0. **Consumer call sites updated:** 0 (additive env — no signature change, no refactor cascade). **State-machine reject branches:** 0.
- **ACs of work:** 3 asserted in CI (AC1-AC3) + AC4 deferred. Under 5.

All red lines clear with margin. **Single ticket — no split.** Same shape and size as the shipped sibling #792.

## Open questions

- **Bare-ESC detection atomicity.** The `i == n-1`-is-bare arm relies on tui-driver writing each bracketed-paste marker as one sub-buffer `writeRaw` (true for the short prompt this test sends). If a future test drives a multi-kilobyte prompt whose paste could split across `os.Stdin.Read` buffers exactly on a marker's `0x1b`, the detector would need a one-byte cross-buffer carry (defer a trailing `0x1b`; on the next read, `0x5b`-first ⇒ CSI/clear, else ⇒ bare/fire). Not built — the test controls the prompt size, so the simple rule is correct and deterministic here. Flagged so a future larger-prompt reuse adds the carry rather than rediscovering the edge.
- **Interrupt-distinct stop reason.** When tui-driver can distinguish an interrupt-stop from a normal end-of-turn, `turn_end.StopReason == "cancelled"` (already a legal `TurnEndPayload` value, `internal/protocol/interactive.go:68-72`) becomes assertable — tightening AC3 from "a turn_end" to "a *cancelled* turn_end." That is a tui-driver + mapper change (production), tracked as an epic-#597 follow-up, **not** folded into this test/harness ticket.
- **fakeclaude/main.go coordination with #791.** The sibling modal capstone (`origin/feature/791`) may also extend fakeclaude. The branch-overlap check is **clean at architect time** (no sibling branch touches this file yet — verified 2026-07-07 via `git diff origin/main...origin/feature/791`). Both changes are additive (new const + new gated poll-loop call); keeping this diff confined (§ Part 1) keeps the eventual merge trivial.

## Security review

**Verdict:** PASS

Test/harness-only; the remote-interrupt production code (#707/#726) already shipped and was security-reviewed there (see `docs/specs/architecture/707-*.md` § Security review). The adversarial question here is not "does this introduce a vuln" but **"could this test PASS while the guarantee is broken?"** The guarantee under live confirmation: a phone-originated `interrupt`, authenticated over the Noise session and gated on `interactive`, reaches the supervised claude as an Esc and **causes** the running turn to stop.

**Findings (categories walked):**

- **[Vacuous pass — the headline requirement] No MUST FIX.** This is the category the PO hoisted to AC1 and flagged as the architect's design call. The reused sibling harness (#792) flips busy→idle on a *file* trigger; naively reused, "the interrupt stopped the turn" would pass even though the file, not the Esc, ended it. The design defeats this at the mechanism level: the **only** source of an end-of-turn JSONL line — hence of the `turn_end` envelope AC3 asserts — is fakeclaude's bare-ESC handler; the test never drops an end-of-turn fixture. So `turn_end` ⟺ the Esc was received and processed (structural, not temporal, causality). Two ordered `t.Fatal` guards enforce it: (1) a non-idle `turn_state` must be observed *before* the interrupt (the turn was running); (2) the `turn_end` must be observed *after* it. A separate direct oracle (AC2, a bare ESC in the stdin log) is belt-and-suspenders with **different fabric** — fakeclaude's own byte record versus the daemon's wire report. A pass over a turn that never ran, or a `turn_end` from any source but the Esc, is impossible.
- **[Trust boundaries] No MUST FIX.** The boundary is unchanged and exercised, not modified: an untrusted phone `interrupt` frame crosses into the trusted supervised process only after AEAD decryption on the per-session Noise channel **and** the server-authoritative `s.interactive` capability gate in the shipped `handleInterrupt` (#707). This test drives that boundary through the real stack (`driveHandshakeToOpenDaemonInteractive` pins the grant); it adds no new boundary and no new trust decision. The frame carries no payload, so no untrusted data flows past the gate.
- **[Subprocess / external command] No findings.** The terminal effect is one fixed Esc keystroke (`[]byte{0x1b}`) into the already-running fakeclaude via the sealed `SendEsc` → tui-driver seam. No `exec`, no argument construction, no attacker-controlled value reaches the child. fakeclaude's new mode adds no subprocess and no new stdin *echo* to stdout (it appends a fixed JSONL literal to its own session file, and only in response to a byte the trusted supervisor wrote).
- **[File operations] No findings.** The new writes target the **test-owned** session JSONL (`f`, mode `0o600`, under the test `home`) and the append is a fixed literal — never a phone/network-controlled path or content. `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` is a flag, not a path (no traversal surface). The stdin-log and JSONL-trigger paths are test-controlled temp files, same posture as the sanctioned `emitStructuredJSONLIfTriggered` / rotation trigger.
- **[Concurrency] No findings.** No new production goroutine, lock, channel, or timer. The bare-ESC detection runs on the existing single stdin-reader goroutine and only *signals* `escPending atomic.Bool`; the append runs on the main poll goroutine — the single-writer-of-`f` invariant is preserved unchanged (identical to the shipped `turnPending`/`appendTurnGrowth` split). The one-shot flag bounds the append to at most one end-of-turn line; no re-entrancy, no TOCTOU (`atomic.Swap`).
- **[Error messages, logs, telemetry] No findings.** No new production logging. fakeclaude's append is a fixed literal (`[interrupted]`-style text, no phone/secret data); the test's diagnostics echo only synthetic markers and the fixed ESC byte. The shipped `handleInterrupt` logging discipline (event + `conn_id` only, never payload/screen — #707) is inherited unchanged.
- **[Cryptographic primitives] No findings.** Exercises the shipped Noise_IK path; no new crypto. The interrupt frame's confidentiality/integrity is the established per-session AEAD; a key mix-up fails loudly at decrypt.
- **[Tokens / secrets] N/A.** The interrupt frame mints, stores, and compares no token; the pairing token was validated at handshake and rides the established session. Fixtures are synthetic ASCII markers — no secret material.
- **[Network & I/O] No findings.** The test is a client of the shipped server; the `interrupt` frame is bounded by the existing `maxNoisePayloadBytes` cap before `dispatchAppFrame`. fakeclaude's stdin read is the existing 4 KiB-buffer loop; the appended end-of-turn line is a fixed inert literal.
- **[Threat model alignment] No findings.** Addresses the epic-#597 remote-interrupt guarantee as the **live** confirmation; the deterministic correctness oracle stays upstream (#707). Per ADR 025 / the mobile [Security model], interrupting one's own paired session is a normal paired-phone action — correctly `interactive`-gated and permission-gate-exempt in the shipped path, which this test exercises but does not alter. OUT OF SCOPE (unchanged, not touched here): per-conversation interrupt scoping in a multi-session world (#707 Open questions).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-07
