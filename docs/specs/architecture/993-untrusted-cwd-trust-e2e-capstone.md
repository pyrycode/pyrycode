# Spec: e2e — untrusted-cwd trust prompt forwards, accepts to run the turn, denies to a typed error (#993)

**Size:** S · **Security-sensitive:** yes · Split from #988 · Capstone over the shipped #1013 + #1014 seams.

## Files to read first

- `internal/e2e/relay_v2_modal_answer_test.go` — **the harness to copy.** `bringUpModalHarness` (pair with `--allow-remote-permissions`, align sessions dir, pre-create `<initialUUID>.jsonl`, `StartRotationWithRelay` + `PYRY_MOBILE_V2=1`, dial, `driveHandshakeToOpenDaemonInteractive`, `sealSend`/`nextEnv` helpers), `awaitModalShown` (drop trigger → read `TypeModalShown` off the wire non-vacuously), and the two test bodies (answer via `TypeModalAnswer`, assert on the decrypted stream + stdin log). Copy the structure verbatim; swap the permission trigger for the trust trigger and the assertions for this ticket's.
- `internal/e2e/relay_v2_queue_drain_test.go:60-132, 280-345` — `seedBoundConversation` usage (binds a conversation to the bootstrap session so `send_message` resolves — this is AC-1's "creates a conversation"), the busy→free release pattern, and how **drain is observed**: `queue_state` shrinks to empty only on a **confirmed commit** (transcript growth), and the delivered prompt then appears in the stdin log. Both are the AC-3 "the turn ran" signals.
- `internal/e2e/internal/fakeclaude/main.go:207-261` (`modalScreen`, `modalClearScrollRows`, `modalClearScreen` — the permission-modal fixture + clear machinery to mirror for trust), `:321-460` (`main` loop: startup idle glyph gate, raw-mode gate, stdin-reader gate, the one-shot trigger gates, `turnPending`→`appendTurnGrowth`), `:640-720` (`containsBareESC`, `appendTurnGrowth`, `appendTurnEnd`). The trust simulation is a sibling of the existing modal-trigger + clear-on-answer machinery.
- `internal/e2e/internal/fakeclaude/modal_detect_test.go` — the **untagged fast de-risk** pattern (imports `tui-driver`, renders the fixture through `DetectModalClass`, asserts the class). Mirror it for the trust fixture and the trust-clear fixture.
- `cmd/pyry/modal_resolve_v2.go:82-198, 214-338` — the shipped deny/timeout emit: `emitFolderNotTrusted` → `notifyBlocked(activeConv(), "folder not trusted")`; `ResolveTimeout` emits when `out.Class == classTrust`; `ResolveAnswer` emits when `out.Class == classTrust && outcome == OutcomeDeny` (`optExit`); trust `optProceed` → `AcceptTrust` (no emit). Read-only — do not modify.
- `cmd/pyry/session_error_v2.go:116-171` — the `session_error` broadcast: fans `TypeSessionError{ConversationID, Code: CodeSessionBlocked, Message}` to every **interactive** conn. Confirms the harness's interactive phone receives it.
- `internal/protocol/messaging.go:247-251` (`SessionErrorPayload`), `internal/protocol/codes.go:34` (`CodeSessionBlocked = "session.blocked"`), `:476` (`TypeSessionError = "session_error"`), `:275` (`TypeModalShown = "modal_shown"`) — the wire types to decode and assert.
- tui-driver (module cache, pinned `v1.10.0`): `pkg/tuidriver/trust.go` (`gridHasTrustDialog`: header `"Quick safety check"` + a `❯`-marked numbered option row within 3 rows below), `pkg/tuidriver/permission.go:45` (`modalOptionRe`), `:54` (`anchorTrustHeaderSpaced`), `pkg/tuidriver/ready.go:115` (`Readiness.TrustModal = gridHasTrustDialog(g)`). This is the single screen shape that makes **both** `WaitReady` hold the turn **and** `DetectModalClass` classify trust.
- `docs/knowledge/codebase/1013.md`, `docs/knowledge/codebase/1014.md` — the two shipped tickets this capstone certifies (hold-while-pending; give-up exemption + typed error). Read for the exact seams and the "no auto-trust" property under test.

## Context

#988 forbids auto-trusting a remote-driven interactive session's cwd: claude's startup "Quick
safety check" trust dialog must be **forwarded** to the phone, not silently accepted, and a
queued (untrusted) turn must not be typed into that consent gate. The behaviour landed in two
tickets — **#1013** (`readyForDelivery` returns `ErrTrustModalPending` before `DeliverPrompt`, so
the queued turn is held while the modal is up) and **#1014** (the msgqueue give-up bound exempts
the pending window; a trust deny/timeout emits a typed `session_error{session.blocked, "folder not
trusted"}`). Both are unit-tested and merged.

This ticket is the **end-to-end capstone**: over one spawned daemon + a `fakeclaude` that raises the
startup trust dialog, driven from a paired interactive phone over the real v2 Noise wire, prove the
whole pipeline (`WaitReady` hold → `TypeModalShown` forward → `AcceptTrust`/`SendEsc` resolution →
run-on-accept / typed-error-on-deny) so the #988 wedge can never silently regress. It **rides the
shipped seams and introduces no production behaviour** — the only non-test code is a `fakeclaude`
trust-dialog simulation used solely by this test.

The real-claude PTY path is blocked by a claude/tui-driver version mismatch (see
[[env-realclaude-pty-blocked-claude2-vs-tuidriver19]]), so a `fakeclaude`-simulated startup trust
dialog is the CI-faithful runnable path — same disposition as #791/#792 for the permission modal.

## Design

Three artifacts. One production `.go` file (a test-only harness binary), two `_test.go` files.

### 1. `fakeclaude` trust-dialog simulation (`internal/e2e/internal/fakeclaude/main.go`)

A sibling of the existing `PYRY_FAKE_CLAUDE_MODAL_TRIGGER` (permission) machinery, behind a new env
var `PYRY_FAKE_CLAUDE_TRUST_TRIGGER`. Additive and off-by-default (byte-identical when unset), like
`envModalTrigger`/`envEscEndsTurn`.

**New fixtures (contract, not full code):**

- `trustScreen` — the startup trust dialog, rendered once on the trigger's first appearance. Must
  satisfy `tuidriver.gridHasTrustDialog`: a row containing `"Quick safety check:"` **and**, within
  3 rows below, a `❯`-marked numbered option row. Shape:
  ```
  Quick safety check: Is this a project you trust?\r\n
  ❯ 1. Yes, I trust this folder\r\n
    2. No, take me back\r\n
  ```
  This single screen makes `WaitReady` return `Readiness{TrustModal: true}` (supervisor holds the
  turn) **and** `DetectModalClass` return `ModalClassTrustFolder` (producer surfaces
  `modal_shown{Class:"trust", Options:[proceed, exit]}`). The `❯` glyph and the header phrase are
  already covered by this file's `cmd/substrate-guard` allowlist entry — no new exemption needed.
- `trustClearScreen` — scrolls the `"Quick safety check"` header out of the **entire** rendered grid
  (trust detection scans the whole grid via `NewGrid`, not the bottom-12 window permission uses), then
  a trailing idle glyph as the non-blank last row. Mirror `modalClearScreen`/`modalClearScrollRows`
  but size the scroll to clear the full `DefaultPtyRows` grid; the exact count is **pinned by the unit
  test below**, not guessed here.

**New `main`-loop wiring (mirror the `modalTrig` gates, one-shot each):**

- Extend the startup-idle-glyph gate (`if tui || modalTrig != ""`) and the raw-mode gate
  (`if modalTrig != "" || escEndsTurn`) and the stdin-reader-start predicate to include
  `trustTrig != ""`. Raw mode is required so the accept `"1\r"` (from `AcceptTrust`) and the deny bare
  ESC (`0x1b`) reach the reader verbatim/unbuffered.
- On the trust-trigger file's first appearance: write `trustScreen` once (Unknown→TrustFolder
  transition), one-shot via a `trustShown` bool.
- **Accept vs. deny discrimination (load-bearing).** Watch stdin. On the **accept** keystroke — the
  first post-trust-modal stdin bytes that are **not** a bare ESC (i.e. the `AcceptTrust` `"1"`) —
  write `trustClearScreen` once (`trustCleared` bool), so `HasTrustModal` goes false, `WaitReady`
  returns clean, and the held turn delivers. On a **bare ESC** (deny, via `SendEsc`): do **not** clear
  — leave the dialog up so the turn stays held forever and no reply is produced. Reuse the existing
  `containsBareESC` to distinguish; signal from the stdin reader to the main loop via a new
  `atomic.Bool` (e.g. `trustAcceptPending`), exactly like `clearPending`/`escPending` (reader signals,
  main goroutine is the sole writer of `f`/stdout — preserve the single-writer invariant).
- The held turn, once delivered after clear, grows the JSONL via the existing `turnPending` →
  `appendTurnGrowth` path, so the daemon's `#668` transcript-growth commit-confirm acks and the queue
  drains. No new turn-emit code — `appendTurnGrowth` is sufficient for the drain/commit signal.

### 2. e2e test (`internal/e2e/relay_v2_trust_test.go`, `//go:build e2e`)

A `bringUpTrustModalHarness` copied from `bringUpModalHarness` (`relay_v2_modal_answer_test.go`) with
two deltas: (a) forward `PYRY_FAKE_CLAUDE_TRUST_TRIGGER=<trig>` instead of the permission trigger via
`StartRotationWithRelay`'s `extraEnv`; (b) `seedBoundConversation(t, home, knownConvID, initialUUID)`
before the daemon starts so `send_message` resolves to the bootstrap session (the untrusted-cwd
conversation of AC-1). Same interactive pairing (`--allow-remote-permissions`), same `sealSend` /
`nextEnv` single-long-deadline decrypt-drain helpers, same `awaitTrustModalShown` (drop trigger → read
`TypeModalShown` off the wire non-vacuously, assert `Class == "trust"` and option IDs
`[proceed, exit]`).

**`TestRelayV2_UntrustedCwdTrustAccepted` (AC-1, AC-2, AC-3):**

1. Bring up; `awaitTrustModalShown` → assert `TypeModalShown{Class:"trust", Options:[proceed,exit]}`
   read off the wire (AC-2, non-vacuous — `t.Fatal` if no modal before the ~20 s guard). Capture
   `modalID`.
2. `send_message` the queued turn (a fixed ASCII marker, e.g. `"e2e-993-turn"`). Observe the
   `queue_state` showing it queued. **Held-fence:** assert the stdin log does **not** contain the
   turn marker (the turn was held before `DeliverPrompt`, per #1013) — this proves the hold is real,
   not that delivery merely hasn't happened yet.
3. Answer `TypeModalAnswer{modalID, OptionID: "proceed"}`. This routes `AcceptTrust` → `"1\r"`, which
   the fakeclaude sees → clears the trust dialog → the held turn delivers + commits.
4. **AC-3 observe (the turn ran / a reply is produced):** drain `queue_state` until empty (the
   confirmed-commit fence), then assert the stdin log now contains the turn marker (delivered to
   claude) **and** the accept `"1\r"` (proves the accept keystroke routed). Order the checks so the
   drain/delivery positive gates any negative — mirror the modal-answer test's vacuity discipline.

**`TestRelayV2_UntrustedCwdTrustDenied` (AC-1, AC-2, AC-4):**

1–2. Same bring-up + `awaitTrustModalShown` + `send_message` + held-fence as above.
3. Answer `TypeModalAnswer{modalID, OptionID: "exit"}` (deny). This routes `SendEsc` → bare ESC
   (fakeclaude leaves the dialog up) **and** `emitFolderNotTrusted` → `session_error`.
4. **AC-4 observe (typed error, no retry loop, no reply, non-vacuous):**
   - Read a `TypeSessionError` off the wire; assert `Code == CodeSessionBlocked` (`"session.blocked"`,
     the terminal give-up code — **not** the transient `server.binary_busy`) and
     `Message == "folder not trusted"`.
   - **Non-vacuity (the security property under test):** assert the turn marker **never** appears in
     the stdin log within a bounded window after the deny — a denied turn must **not** be delivered.
     Gate this with a positive first: assert the deny ESC (`0x1b`) reached the stdin log (proves the
     deny routed, so "no delivery" is not vacuously true because nothing happened). Reuse
     `assertNoAnswerDigit`-style helpers from the modal-answer test where applicable.
   - Assert `queue_state` stays non-empty (the turn is still held, never drained) — the deny genuinely
     blocks the turn.

Deny-by-**answer** (`optExit`) is the required fast path (deterministic, seconds). Deny-**on-timeout**
(waiting out the real ~2-min `modalDenyTimeout`) exercises `ResolveTimeout`'s identical
`emitFolderNotTrusted`; it is **out of scope** here to keep CI fast (the trust-class timeout emit is
already unit-covered by #1014's `modal_resolve_v2_test.go`, and `TestRelayV2_RemotePermissionDeniedOnTimeout`
already carries one ~2-min e2e for the timeout timer). Note this deferral in the test's file comment.

### 3. `fakeclaude` fast de-risk unit test (`internal/e2e/internal/fakeclaude/trust_detect_test.go`, untagged)

Mirror `modal_detect_test.go` (imports `tui-driver`, no harness, runs in ms):

- `trustScreen` → `DetectModalClass == ModalClassTrustFolder` **and** `HasTrustModal(trustScreen) == true`.
- `trustScreen + trustClearScreen` → `DetectModalClass != ModalClassTrustFolder` **and**
  `HasTrustModal(...) == false`.

This pins `trustClearScreen`'s scroll count and catches a mis-detecting fixture in milliseconds
rather than inside a slow live e2e — the "reference the test that asserts the invariant" contract for
the two fixtures above.

## Concurrency model

No new production goroutines. Inside `fakeclaude`, the trust-accept signal follows the existing
reader→main `atomic.Bool` pattern (`clearPending`/`escPending`/`turnPending`): the stdin reader is the
only stdin consumer and never writes `f`/stdout; the single main poll goroutine performs every
`f`/stdout mutation (render `trustScreen`, render `trustClearScreen`, `appendTurnGrowth`). `Swap(false)`
collapses chunked stdin per poll cycle and re-arms. The daemon-side hold/retry is the shipped
msgqueue loop (1 s cadence, give-up exemption on `ErrTrustModalPending`) — unchanged. The test's
`nextEnv` uses one long deadline with back-to-back decrypts so the receive nonce stays in sequence
(never a short poll on a phone conn — `fakephone` closes the WS on a timed-out `Receive`).

## Error handling

Test-tier only. Each assertion has a dedicated `t.Fatal` naming its failure mode (harness-produced-no-
modal, no drain, denied-turn-delivered, wrong code) so no positive can pass vacuously. `fakeclaude`
writes stay best-effort + fsync (mirroring `appendTurnGrowth`) — the e2e asserts downstream (the phone
receives the frames / the stdin log grows), never on the write itself. Deny-path robustness: the
fakeclaude ignoring the ESC (dialog stays up) is deliberate — the daemon's `session_error` is emitted
deterministically at the resolver regardless of child behaviour, and "no reply" follows from the modal
never clearing.

## Testing strategy

- `go test ./internal/e2e/internal/fakeclaude/...` (untagged) — the two fast fixture de-risk asserts,
  run first; a mis-classifying fixture fails here in ms.
- `make e2e` (or `go test -tags e2e -run TestRelayV2_UntrustedCwdTrust ./internal/e2e/...`) — the two
  capstone tests. Run `-race`. The accept test is seconds; the deny-by-answer test is seconds.
- The whole file is `//go:build e2e` so it never runs in the default unit suite (AC-5). The fakeclaude
  de-risk test is intentionally untagged (like `modal_detect_test.go`).

## Open questions

- **`session_error` `ConversationID`.** The load-bearing AC-4 assertions are `Type`, `Code`, and
  `Message`. The payload also carries `ConversationID = activeConv()` (`w.active.CurrentConversation`).
  Verify empirically during implementation whether it is stamped with `knownConvID` in this flow (the
  held-turn `WriteUserTurn` stamps the supervisor cursor per #312/#1013, but `activeConversation`'s
  cursor is a relay-side value); if reliably populated, assert it equals `knownConvID` as a bonus, but
  do **not** make the test hinge on it — keep the hard gate on `Code`+`Message`.
- **`trustClearScreen` scroll count.** Determined by the untagged de-risk test, not this spec. If a
  pure-scroll clear proves finicky against the whole-grid trust scan, an equivalent all-idle-glyph
  screen that renders no trust anchor is acceptable — the invariant (`HasTrustModal == false` after)
  is what matters.

## Security review

**Verdict:** PASS

This ticket is `security-sensitive` because it **certifies the #988 no-auto-trust consent gate holds
end-to-end over the wire** — the same design surface that made #1013/#1014 security-sensitive. Per
[[security-sensitive-label-tracks-design-not-lineage]], the label tracks the design property, not a
guess about blast radius. The security *value* is that a regression which auto-trusts, or delivers a
denied turn, or leaks queued/modal content, **cannot ship green**. Walking the categories:

**Trust boundaries / the consent gate (the core property).** No new production trust surface. The
test constructs an **untrusted** cwd (the fakeclaude raises the startup trust dialog; no
`trust.MarkWorkdirTrusted` anywhere) and proves trust is granted **only** by an authorized remote
`modal_answer{proceed}` behind the shipped `Device.MayAnswerRemotePermission` gate. `readyForDelivery`
(#1013, unchanged) still returns `ErrTrustModalPending` before `DeliverPrompt`, so the queued
(untrusted) turn never reaches the consent gate — the **held-fence** assertion (turn marker absent
from the stdin log while pending) certifies exactly this structural property over the wire.

**Non-vacuity of the deny assertion (load-bearing).** AC-4's security worth is that a denied turn is
**never delivered**. The design makes the check non-vacuous three ways: (a) the turn is really queued
and held first (`queue_state` shows it, held-fence shows it undelivered), so "no delivery after deny"
is not vacuously true; (b) a positive gates the negative — the deny ESC must be observed in the stdin
log (the deny routed) before asserting the turn marker's absence; (c) the code must be exactly the
**terminal** `CodeSessionBlocked`, not the transient `CodeServerBinaryBusy` — a regression that
surfaced a retry hint would fail. A fakeclaude that wrongly cleared the dialog on ESC (unblocking the
turn) flips the delivered-turn check red. This is the containment property the ticket exists to prove.

**Confidentiality / info leak.** The forwarded `modal_shown` body and the `session_error` reason are
the only content on the wire. The reason is the shipped compile-time constant `"folder not trusted"`
(#1014) — it cannot carry `head.text`, the modal body, or any secret; the emitter holds no `*Queue`
handle by construction. All test markers are fixed ASCII constants (`"e2e-993-turn"`, etc.), never
secrets — the "Do NOT paste secrets; the stdin log is echoed in failure messages" discipline from the
copied harness carries over. No new logging: the `fakeclaude` writes are content-free session-JSONL
growth + screen renders.

**Injection / untrusted content into a control surface.** N/A. No untrusted data reaches the
classifier (`errors.Is` on a static sentinel), the reason (static const), the emit (daemon-resolved
convID), or the fixture (test-authored). The `fakeclaude` trust simulation is gated behind an env var
set only by this test and is absent from every production path.

**Tokens / secrets / DoS / crypto / file ops.** N/A / addressed. Pairing + `answerToken` + device
gating are the shipped path, unexercised for new logic here. Frames ride the manager's established
Noise_IK sealed transport. The deny-by-answer path is a single bounded frame; deny-on-timeout (the
unbounded-retry-until-2-min window) is deferred out of scope and already bounded by
`MaxQueuedPerConversation` + the #725 deny-on-timeout in production. No filesystem/exec/crypto in the
changed code beyond the test-only session-JSONL growth `fakeclaude` already performs.
