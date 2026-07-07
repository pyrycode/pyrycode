# Spec #791 — Live e2e capstone: remote permission answered (nonce + gate + deny-on-timeout)

**Part of EPIC #597** (Phase 3 live exit gate). Split from #708. Mirrors the Phase-2 capstone #642 and the sibling #792 (queue-drain). `security-sensitive`.

## Verdict up front: reuse the harness, one small fakeclaude extension, ship as one S ticket

This is a **test/harness-only** capstone. The permission control loop already shipped and was deterministically proved upstream: #703 (surface), #716 (modal surfacer + producer), #717 (gated resolution incl. nonce anti-replay), #725 (deny-on-timeout), #706 (first-answer-wins), plus the mobile modal UI + tui-driver safe-answer slice. The **producer** was live-wired into the daemon by **#798** (merged, PR #800): `newInteractiveModalEmitterV2` now has a non-test caller — `startInteractiveModalStreamV2` (`cmd/pyry/interactive_modal_stream_v2.go:45`) ← `startRelayV2` (`relay.go:378`). **This is what makes #791 buildable** — the earlier architect pass correctly routed it back because that producer had zero non-test callers; with #798 landed the "test/harness only" premise now holds. This ticket lands the **live** confirmation over one daemon + a gated phone + a claude (fake) that raises a real permission prompt. It confirms; it does not re-prove — the correctness oracle stays upstream (#716/#717/#725).

**Harness = the fakeclaude two-phone relay (option b), one small additive fakeclaude mode.** Same choice #642/#792 made: reuse `StartRotationWithRelay` + `fakephone`/`fakerelay`, add a ~20-LOC fakeclaude "raise a permission modal on trigger" mode rather than fuse the flaky non-CI `e2e_realclaude` suite. The operator live-stack variant (real claude, real prompt) is where **AC5** (flight recorder) lands — CI/dev asserts AC1–AC4.

**Two test functions, one flow each — do NOT split the ticket.** The four loop assertions decompose into exactly two single-modal scenarios that share the harness bring-up: (1) *answered* (AC1 surface, AC2 answer+keystroke, AC3 nonce-replay) and (2) *deny-on-timeout* (AC4). Each raises **one** modal and ends — so the fake never needs to re-arm or clear a modal, and neither actuation path needs a dismissal re-read (see § Design). This is the [[po-plus-title-capstone-is-one-flow]] shape; splitting the *ticket* would duplicate the harness bring-up for zero coverage gain.

## Files to read first

- `internal/e2e/relay_v2_daemon_test.go` (whole file, ~360 lines) — **the primary bring-up template.** The simplest interactive-session daemon test: `RunBareIn(... "pair" ...)` → `decodePairPayload` → `fakephone.Dial(ctx, fr.URL(), serverID, payload.Token, "phone-a")` → handshake → bootstrap fake up. #791 mirrors this setup, then adds the modal trigger + control frames. Lift the pair/dial/serverID plumbing verbatim.
- `internal/e2e/relay_two_phone_structured_test.go` — `driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, token)` (**reuse verbatim** — grants `interactive`, the capability the modal broadcast rides), `buildHelloEarlyInteractive`, `sendNoiseInit`, `readInnerFrame`, and the sealed inner-frame send + single-deadline decrypt helpers. The device gate rides the pairing token passed here (see § Design Part 2).
- `internal/e2e/relay_v2_queue_drain_test.go` (#792, the sibling capstone) — the sessions-dir-alignment + pre-create-`<initialUUID>.jsonl` bootstrap recipe, the sealed-send + single-deadline decrypt-drain (`nextEnv`-style: skip non-target frame types behind one long deadline; never a short poll on a phone conn, per 603.md/634.md), and the vacuous-pass `t.Fatal` ordering to mirror.
- `internal/e2e/internal/fakeclaude/main.go` — **the file to extend.** Read `emitIdleIfTriggered` (#792) / `emitAssistantIfTriggered` (the one-shot `os.Stat(trig)` + `writeStdout` + `f.Sync` pattern to mirror), the `main()` poll loop and its trigger-check block, the startup idle-glyph emission (`writeStdout(idleGlyph)`), the env-const block, and the package doc. **Already on the substrate-guard allowlist** (`cmd/substrate-guard/main.go:37`) — it may emit the modal literal freely.
- `internal/e2e/harness.go:304-360` — `StartRotationWithRelay`: spawns the **`pyry` binary as a subprocess** (`spawnWith`, `cmd.Process.Pid`), wires fakeclaude as the child, sets `PYRY_FAKE_CLAUDE_STDIN_LOG` (the keystroke oracle), forwards `-pyry-relay`, and **appends `extraEnv ...string` verbatim** (`:332`). Pass the new modal-trigger env via `extraEnv`; **no harness.go change needed** (the #642 seam). The subprocess boundary is why `modalDenyTimeout` can't be shrunk from the test (see § AC4).
- `cmd/pyry/modal_resolve_v2.go` — **the answer path (#717).** `ResolveAnswer`: `Lookup`(nonce/modal_id) → `dev.MayAnswerRemotePermission()`(device gate) → `classifyAnswer`(option_id → keystroke) → `Resolve`(consume, one-shot) → `routeAnswerKeystroke` → `kb.Answer(choice)`. `classifyAnswer` (`:265`) maps option_id → 1-based index in the surfaced Options (allow_once=1 … reject_always=4). `ResolveTimeout` (`:120`) → `kb.SendEsc()` (the deny keystroke). Replay-after-resolve misses at `Lookup` → no keystroke, no audit (AC3).
- `internal/supervisor/modal.go` — `Answer(choice)` = a single **non-blocking** `choice+"\r"` PTY write; `SendEsc()` = single `0x1b`. **Neither does a dismissal re-read** (that is tui-driver's `AnswerModal`, which the supervisor deliberately does not use). This is why the fake never needs to clear the modal for actuation, and what the stdin-log assertion sees (`"2\r"` for the answer, a bare `0x1b` for the timeout deny).
- `internal/relay/v2session.go:1743-1811` — `handleModalAnswer` (`dev` = the connection's `s.device`), `handleModalTimeout`, `broadcastModalDismissed` (fans to every `interactive` conn; reads `m.sessions` on-Run, must not call `ActiveConns`), and `:867-890` `ArmModalTimeout` + `:81` `var modalDenyTimeout = 2 * time.Minute`.
- `internal/modalbridge/modal.go:110-165` — `PermissionRequestForClass`: for `ModalClassPermission` it surfaces a **FIXED four-option set** (`allow_once/allow_always/reject_once/reject_always`, allow-first) that is **screen-independent**; only `Title = strings.TrimSpace(screenText)`. `Record` (`:145`) mints the `modal_id` nonce. **This is the load-bearing fact:** the fake's modal screen needs only to *classify* as `Permission` — it need not carry parseable numbered options.
- tui-driver `pkg/tuidriver/modal.go` (`DetectModalClass`, `permissionRegionRows = 12`, `anchorPermissionSpaced = "Do you want to proceed"`) + `grid.go` (`NewGrid(snap, 0, 0)` renders a **content-height** grid; `ContainsInLastRows`) + `events.go:253-283` (Modal Shown/Hidden fire on the `cur.modal != prev.modal` transition, polled every `pollInterval`). Why a minimal plaintext "Do you want to proceed?" screen, emitted after a baseline idle glyph, transitions Unknown→Permission and is detected.
- `cmd/pyry/interactive_modal_stream_v2.go` (#798, merged) — the **producer under test**: `startInteractiveModalStreamV2` → `runModalStream` → on `EventKindPtyModalShown` `emitter.Handle(ctx, ev, screenText())` → `PermissionRequestForClass` → `Record` (mint) → `ArmModalTimeout` → broadcast `modal_shown`. Read to confirm the live path the test exercises end-to-end.
- `internal/protocol/messaging.go:127-170` — `ModalAnswerPayload{ModalID, OptionID, AnswerToken}`, `ModalShownPayload{ModalID, Class, Title, Prompt, Options []ModalOption{ID,Label}, DefaultOptionID}`, `ModalDismissedPayload{ModalID, Outcome, Source}`. The exact wire shapes the phone sends/decodes.
- `internal/audit/audit.go:44-61` — the `modal_dismissed` vocab: answer → `Outcome=<option_id>`, `Source="remote"`; timeout → `Outcome="denied_timeout"`, `Source="timeout"`.
- `internal/devices/auth.go:48-90` + `cmd/pyry/pair.go:90,212` — `MayAnswerRemotePermission()` == `Device.AllowRemotePermissions`; `pyry pair --allow-remote-permissions` (default OFF) sets it. **The gated phone MUST pair with this flag** or `ResolveAnswer` denies at the gate.
- `docs/specs/architecture/792-queue-drain-two-phone-e2e-capstone.md` § AC4 — the flight-recorder reachability scoping this ticket's **AC5** mirrors one-for-one.

## Context

Phase 3 of the mobile structured stream. When claude raises a permission/trust prompt, the daemon surfaces it to interactive phones as `modal_shown`; a **gated** phone (paired with `--allow-remote-permissions`) may answer with `modal_answer`, which the daemon validates (nonce + per-device gate) and routes into claude as the option's keystroke; an unanswered prompt is safe-denied (ESC) after `modalDenyTimeout`, with a `modal_dismissed{timeout}` to the phone. Every leg is shipped and unit/integration-proved; #798 live-wired the producer. What is **not** yet proved is the **live** end-to-end behaviour over one running daemon + a real supervised child. This capstone is that confirmation. **Test/harness code only** — a surfaced production gap is a separate ticket.

**Scope note (operator, 2026-07-04):** the read-only / ungranted-device path is dropped — all paired devices are granted. The device gate is exercised **positively**: only the gated phone answers, and its answer is accepted. No ungated-phone negative.

## Design

### Part 1 — fakeclaude: a "raise a permission modal on trigger" mode (~20-25 LOC, additive)

The daemon hosts the fake under a tui-driver `Session` that polls the child PTY and classifies each snapshot (`DetectModalClass`). To make the daemon emit `modal_shown`, the fake must produce PTY output that renders (via vt10x) as a **permission** modal. Because `PermissionRequestForClass` uses a **fixed, screen-independent** option set (allow_once/allow_always/reject_once/reject_always) and only lifts `Title` from the screen, the fake needs **only** a screen that `DetectModalClass` classifies as `ModalClassPermission` — i.e. one containing the exact anchor `"Do you want to proceed?"` within the **bottom 12 rendered rows**. `NewGrid(snap, 0, 0)` renders a content-height grid, so a compact screen (a few lines) places the anchor within that window naturally. **No ANSI capture, no separator, no numbered options, no `❯` marker are required.**

New env `PYRY_FAKE_CLAUDE_MODAL_TRIGGER` (a path). Contract:

- **When unset (default): byte-identical to today.** Every existing caller is unperturbed.
- **When set:** at startup the fake emits the idle glyph `❯` **once** (baseline non-modal screen → `prev.modal == Unknown`), exactly as TUI/idle modes establish an idle baseline, and does **not** emit the thinking spinner. It watches the trigger path in the existing poll loop; on the file's **first appearance** it writes the permission-modal screen **once** (fsync) and removes the trigger. tui-driver's merge loop detects Unknown→Permission on the next `pollInterval` tick → `EventKindPtyModalShown{Permission}` → the #798 producer surfaces `modal_shown`.
- **The fake never clears the modal and never reads the answer keystroke for coordination.** Each test raises exactly one modal and ends (§ Part 3). The answer/deny keystrokes (`Answer`/`SendEsc`) are fire-and-forget PTY writes with no dismissal re-read, so nothing blocks on the modal disappearing. The existing stdin reader continues to append stdin bytes to `PYRY_FAKE_CLAUDE_STDIN_LOG` (the keystroke oracle) unchanged.

New helper — **signature + behaviour only** (developer writes the body, mirroring `emitIdleIfTriggered`):

```
// emitModalIfTriggered: when path exists, writeStdout the permission-modal
// screen once and remove the trigger; report whether it fired. Gated in main
// by a one-shot `modalShown` bool exactly like the existing `idled`/`rotated`
// gates, so it emits at most once.
func emitModalIfTriggered(path string) bool
```

The modal-screen literal is a package const in the fake (allowlisted file). Minimal sufficient form — a couple of context lines ending in the exact anchor within the bottom region, e.g.:

```
<tool/context line>\r\n … \r\n Do you want to proceed?\r\n
```

The only hard requirement is that `DetectModalClass(<rendered screen>) == ModalClassPermission` and no earlier-priority anchor (mcp / "Agents"+tab / "Enter to select" / "Quick safety check" / slash-picker) is present. **Do NOT combine with `PYRY_FAKE_CLAUDE_TUI` or `PYRY_FAKE_CLAUDE_IDLE_TRIGGER`** (the stdin spinner / busy window would perturb the baseline) — the modes are mutually exclusive; this test sets only the modal trigger. Add the env const to the const block and a package-doc paragraph mirroring the `PYRY_FAKE_CLAUDE_IDLE_TRIGGER` doc.

**De-risk the fixture before the e2e (required).** Add one harness-free unit assertion in the fakeclaude package test (or the e2e package) that renders the modal-screen const and asserts `tuidriver.DetectModalClass(screen) == ModalClassPermission`. This validates the fixture in milliseconds — the developer must not discover a mis-detecting screen inside a live-daemon run.

### Part 2 — the gated phone (device gate, AC2)

`ResolveAnswer` gates on `s.device.MayAnswerRemotePermission()` (== `AllowRemotePermissions`), where `s.device` is the connection's paired device. Pairing defaults the flag OFF, so the answering phone MUST be paired **with `--allow-remote-permissions`**:

```
RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a", "--allow-remote-permissions")
```

`decodePairPayload` yields the token; `driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)` opens the interactive session bound to that granted device. The handshake helper is reused **unchanged** — the grant rides the pairing, not the handshake. (The modal broadcast is operator-global — it fans to every `interactive` conn regardless of conversation — so no bound conversation is needed to *receive* `modal_shown`; only the `interactive` capability, which the helper asserts.)

### Part 3 — the two tests (`internal/e2e/relay_v2_modal_answer_test.go`, new, `//go:build e2e`)

Shared bring-up (a local helper): align `sessionsDir = filepath.Join(home, ".claude", "projects", encodeWorkdir(home))`, `MkdirAll`, pre-create `<initialUUID>.jsonl` (`{}\n`) so `reconcileBootstrapOnNew` rotates the bootstrap (mirrors #792); `fakerelay.New`; `StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverRotate, stdinLog, fr.URL()+"/v2/server", "PYRY_MOBILE_V2=1", "PYRY_FAKE_CLAUDE_MODAL_TRIGGER="+modalTrig)`; pair the gated phone (§ Part 2); `fakephone.Dial`; `driveHandshakeToOpenDaemonInteractive`. This brings up the bootstrap fake whose `Session.Events()` the #798 modal stream subscribes to.

**Test A — `TestRelayV2_RemotePermissionAnswered` (AC1, AC2, AC3):**

1. **[Vacuous-pass positive #1 — surface]** `os.WriteFile(modalTrig, …)`. Drain the phone's inbound envelopes (single long deadline) until a `modal_shown` arrives. **`t.Fatal` if none before ~20 s** (the "harness produced no modal" failure mode). Assert `Class == "permission"`, `Options` carry the four IDs (`allow_once…reject_always`), and capture `modalID := payload.ModalID`. (AC1.)
2. **[Vacuous-pass positive #2 — answer routes the chosen keystroke]** Send `modal_answer{ModalID: modalID, OptionID: "allow_always", AnswerToken: <fresh>}`. **Choose `allow_always` deliberately** — it is option index 2, so the routed keystroke is `"2"`, distinct from the always-"1" default, proving the keystroke tracks the *chosen* option. Await `modal_dismissed`; assert `Outcome == "allow_always"`, `Source == "remote"`. Read `stdinLog`; **require it contains `"2\r"` — `t.Fatal` if absent** (the answer did not route → AC3 would be vacuous). (AC2 — "the delivered keystroke matches the chosen option.")
3. **[Negative — nonce anti-replay]** Re-send the **identical** `modal_answer` (same modal_id + option + token). Assert: **no second `modal_dismissed`** arrives within a short deadline, **and** `stdinLog` still contains exactly **one** `"2\r"` (no second keystroke). The replay missed at `Lookup` (already consumed). (AC3.)

Assertion order is load-bearing: the two positives (surfaced; answered with the correct keystroke) gate the replay negative.

**Test B — `TestRelayV2_RemotePermissionDeniedOnTimeout` (AC4):**

1. **[Vacuous-pass positive]** Raise one modal (`modalTrig`), await `modal_shown`, capture `modalID`. `t.Fatal` if not surfaced (~20 s). Do **not** answer.
2. **[Deny-on-timeout]** Wait for `modal_dismissed` with a deadline just over `modalDenyTimeout` (**2 min** in the production binary — see § AC4; use e.g. 2 min 20 s). Assert `Outcome == "denied_timeout"`, `Source == "timeout"`. Read `stdinLog`; require it contains a bare `0x1b` (the ESC deny keystroke) and **no** answer digit. (AC4 — safe-denied; deny keystroke reached claude.)
3. **[Late answer is a no-op]** Send `modal_answer{modalID, "allow_once", <token>}` *after* the timeout dismissal. Assert **no** `modal_dismissed` follows and `stdinLog` gains **no** answer digit — the modal was already consumed by the timeout `Resolve`. (AC4 no-op.)

Keep markers/asserts ASCII: the answer digit (`"2\r"`) and bare ESC (`0x1b`) are checked with `bytes.Contains` on the decoded stdin-log bytes; the delivered prompt is bracketed-paste-wrapped (#749) but a keystroke is not, so match the exact bytes. The test file stays substrate-clean (only `0x1b '['` is banned; a bare `0x1b` is fine).

### Data flow

```
os.WriteFile(modalTrig)
  → fakeclaude emitModalIfTriggered → "Do you want to proceed?" to stdout
  → supervisor tui-driver Session: DetectModalClass → Permission (transition)
  → EventKindPtyModalShown{Permission}
  → runModalStream (#798) → emitter.Handle(ev, screenText)
      → PermissionRequestForClass(Permission, screen)  (fixed 4 options; Title=screen)
      → modalReg.Record → mint modal_id
      → mgr.ArmModalTimeout(ctx, modal_id)             (2-min fail-closed deny)
      → broadcast modal_shown → gated phone           [AC1]

gated phone → modal_answer{modal_id, "allow_always", token}
  → handleModalAnswer → ResolveAnswer(dev=s.device)
      Lookup ok → MayAnswerRemotePermission() true → classifyAnswer→"2"
      → Resolve (consume) → kb.Answer("2") → "2\r" to fake stdin  [AC2 keystroke]
      → broadcastModalDismissed{Outcome="allow_always", Source="remote"}  [AC2]
  → replay same modal_answer → Lookup miss → no keystroke, no dismissal   [AC3]

(Test B) no answer → after modalDenyTimeout
  → handleModalTimeout → ResolveTimeout → kb.SendEsc() → 0x1b to fake stdin
  → broadcastModalDismissed{Outcome="denied_timeout", Source="timeout"}   [AC4]
  → late modal_answer → Lookup miss → no-op                               [AC4]
```

## Concurrency / timing model

No new production goroutines (test-only). Pre-existing actors and fences:

- **Modal detection is poll-based** (tui-driver merge loop, `pollInterval`) — a `modal_shown` appears within one tick of the trigger write; use deadlines, not fixed sleeps.
- **`modal_answer` / `handleModalTimeout` are serialized on the manager's single Run goroutine** — the answer-vs-timeout race cannot double-act (the registry one-shot `Resolve` is the single idempotency gate). #791 never races the two within one modal (Test A answers, Test B times out).
- **Actuation is fire-and-forget:** `Answer`/`SendEsc` are single non-blocking PTY writes (no dismissal re-read), so the Run goroutine never blocks on the modal clearing.
- **fsync visibility:** the fake fsyncs the modal-screen write and the stdin reader fsyncs the stdin log per write (existing) — cross-process APFS visibility is already handled; read `stdinLog` only **after** observing the resolving `modal_dismissed` (a happens-after fence: the dismissal means the keystroke was routed).
- **Phone read discipline (603.md/634.md):** single long deadline with back-to-back in-order decrypts; never a short poll on a phone conn. The gated phone stays connected across the 2-min timeout window (well under `idleTimeout` = 15 min, so no teardown).

## Error handling

- **Harness produced no modal** → the surface `t.Fatal` fires (vacuous-pass guard, both tests).
- **Answer did not route** → the `"2\r"`-present `t.Fatal` fires before the replay negative is evaluated (Test A vacuous-pass guard).
- Handshake / seal / decrypt / pair-exit errors → `t.Fatalf`, consistent with #792/#642.

## Testing strategy — AC mapping

Two `//go:build e2e` tests + one harness-free detection assertion:

- **AC1** (surface with options): Test A step 1 — `modal_shown{Class="permission", Options=[allow_once…reject_always]}` observed; `t.Fatal` on absence.
- **AC2** (answer + keystroke, positive): Test A step 2 — gated phone answers `allow_always`; `stdinLog` contains `"2\r"` (the chosen option's keystroke, ≠ default "1"); `modal_dismissed{Outcome="allow_always", Source="remote"}`.
- **AC3** (nonce anti-replay): Test A step 3 — identical replay yields no second dismissal and exactly one `"2\r"`.
- **AC4** (deny-on-timeout): Test B — after `modalDenyTimeout`, `stdinLog` gains a bare `0x1b`, `modal_dismissed{Outcome="denied_timeout", Source="timeout"}`; a late answer is a no-op.
- **AC5** (audit capture): **not asserted here** — deferred to the operator live-stack run; see § AC5.
- **Vacuous-pass guard:** each test's positive(s) are hard preconditions of its negative, each with a dedicated `t.Fatal` naming its failure mode (mirrors #642/#792).

Gates the developer runs green: `go build ./cmd/pyry`, `go vet ./...`, `staticcheck ./...`, `go test -race ./...`, `make substrate-guard`, and `go test -tags=e2e -run 'TestRelayV2_RemotePermission' ./internal/e2e/...`. Run **Test A** under `-count=3` for determinism (#603); **Test B** may run `-count=1` — its only latency is the deterministic 2-min timer, not a race. The `e2e_realclaude` column is untouched.

## AC4 — the 2-minute deny window (why no production change)

`modalDenyTimeout` is a package `var = 2 * time.Minute` in `internal/relay`, documented "test-overridable (lowercase, save/restore in tests); not yet config-driven (a deferred #708 concern)." The in-package unit tests shrink it directly, but **the e2e drives the `pyry` binary as a subprocess** (`StartRotationWithRelay` → `spawnWith`), so the test cannot reach that var. AC4 therefore waits the real **2-minute** window in Test B.

This is acceptable and is the correct call for this ticket:

- **AC4 is buildable today** — the deny-on-timeout mechanism shipped (#725) and is armed live (#798 wires `ArmModalTimeout` via the emitter). This is *slow*, not *unbuildable* — unlike the producer gap that legitimately routed #791 back to #798. Routing back again for a knob that only makes a working test faster would be over-process.
- **The e2e lane is developer/operator-run, not per-PR CI** — no GitHub workflow runs `-tags e2e` (only `make` runs `-tags e2e_realclaude`; `.github/workflows/` has just `release.yml` + `self-check-daily.yml`). So the 2-min cost is a once-per-verify developer/operator cost, confined to Test B (Test A, the fast path, is proved first and iterated under `-count=3`), not a CI regression.
- **Keeping it test-only honours the ticket premise.** An env-override (`PYRY_MODAL_DENY_TIMEOUT`) read by the daemon would be a **production** change in `security-sensitive` relay code (thread the window through `V2SessionManagerConfig` + wire an env read in `cmd/pyry`), pulling #791 past "test/harness only" and adding a shrinkable-safety-net surface to review. Per Simplicity-First + Evidence-Based-Fix-Selection, that knob is **not** built here; it is surfaced as a follow-up (§ Open questions) to build only if the 2-min wait proves painful in practice.

## AC5 — flight-recorder reachability (deferred to operator live-stack)

AC5 ("recorded by the flight recorder when `PYRY_RECORD_DIR` is set") is **not assertable by the CI/dev fakeclaude variant**, and the ticket already scopes it to "live-stack variant only … not applicable to the fakeclaude-replay variant." The flight recorder lives in `internal/agentrun/ptyrunner` (reached from `pyry agent-run`), while the mobile-relay flow hosts claude under `internal/supervisor`, which wires no `PYRY_RECORD_DIR`/`RecordTo`. The two paths are disjoint — same finding the sibling #792 recorded (§ AC4 there). **Disposition:** CI/dev asserts AC1–AC4; AC5 is satisfied on the separate operator live run (`docs/knowledge/features/mobile-live-e2e-runbook.md`) and is **not** a developer deliverable — no test code. (Deeper gap, per #792's sharpening: the supervisor-hosted mobile session is not recorded in *any* variant today; genuine live audit capture = a separate production ticket to wire `RecordTo` into the supervisor — surfaced, not folded.)

## Scope (S confirmed)

- **Production source files (`.go`, non-test):** **1** — `internal/e2e/internal/fakeclaude/main.go` (~20-25 LOC: one env const, one `emitModalIfTriggered` helper, the modal-screen const, one gated call + `modalShown` bool in `main`, startup idle glyph reuse, one package-doc paragraph). Purely additive; the existing modes are untouched. Far under the ≥5-file gate.
- **New files:** **1** — the e2e test (two functions + shared bring-up). Under the >3 gate.
- **No production change** (no `modalDenyTimeout` knob — § AC4), **no harness.go change** (`extraEnv` seam), **no substrate-guard change** (fake is allowlisted; test stays ASCII + bare-ESC), **no handshake-helper change** (grant rides the pairing).
- **Total LOC:** ~25 (fake) + ~5 (detection assertion) + ~320 (two tests + bring-up) ≈ **350**. Under ~600.
- **New exported types:** 0. **Consumer call sites updated:** 0. **State-machine reject branches:** 0. **ACs of work:** 4 asserted in CI/dev (AC1-AC4) + AC5 deferred. Under 5.

All red lines clear with margin. **Single ticket — no split.**

## Open questions

- **Config-driven `modalDenyTimeout`** (env or `V2SessionManagerConfig` field) would let the e2e exercise AC4 in sub-second time and close the documented "deferred #708 concern." **Not built here** (evidence-based: no observed pain yet; the 2-min Test-B wait is confined to a dev/operator lane). Recommend PO track it as an epic-#597 follow-up if the wait bites.
- **Live audit capture on the mobile path** (wire `RecordTo`/`PYRY_RECORD_DIR` into `internal/supervisor`) is the production ticket AC5 implicitly needs; surfaced, not folded (matches #792's disposition).
- **Modal-screen fixture form.** The spec fixes the *requirement* (`DetectModalClass == Permission`, anchor in the bottom 12 rows) and mandates the harness-free detection assertion; the exact context lines are the developer's to author. If a minimal one-anchor screen mis-detects at the harness PTY size, add a couple of leading blank/context lines so the anchor sits in the bottom region — the unit assertion catches this in milliseconds, never inside a 2-min run.

## Security review (label `security-sensitive`, mandatory; `agents/architect/security-review.md` absent — inline, as #798/#792)

**Verdict: PASS.**

Test/harness-only; the permission-control production code (#703/#716/#717/#725/#706) shipped and was security-reviewed there, and #798 (security-sensitive) reviewed the live wiring. The adversarial question is **"could this test PASS while the guarantee is broken?"** The guarantee: a phone-raised permission is surfaced, answered **only** by a gated device with a valid nonce, routed as the **chosen** option's keystroke, replay-proof, and safe-denied on timeout.

- **[Vacuous pass — the headline requirement] No MUST FIX.** Ordered architect-owned guards in each test: (1) the modal must be observed surfaced (`t.Fatal` on the harness-produced-no-modal mode); (2) in Test A the chosen keystroke `"2\r"` must be observed in the stdin log (`t.Fatal`) **before** the replay negative is evaluated. A "no second keystroke" over a modal that never surfaced or was never answered cannot pass vacuously. Choosing `allow_always` (keystroke `"2"`, not the default `"1"`) makes AC2 prove option→keystroke fidelity, not a coincidental default.
- **[Trust boundaries] No MUST FIX.** The boundary is exactly what the test drives, unchanged: the per-device gate (`MayAnswerRemotePermission`, pinned by pairing the answerer with `--allow-remote-permissions`), the nonce (`modal_id` one-shot `Lookup`/`Resolve`), and the fixed screen-independent option set. The test adds no surface; it exercises the shipped `ResolveAnswer`/`ResolveTimeout`. The dropped ungated-device negative is an operator scope decision, not a gap this test can restore.
- **[Nonce / anti-replay — AC3] No findings.** The replay is refused at `Lookup` (already consumed by the first answer) — no keystroke, no audit, no second dismissal. The test asserts *exactly one* `"2\r"` and no second `modal_dismissed`, so a regressed dedup (e.g. a server-side token store re-broadcasting) would fail the test.
- **[Deny-on-timeout — AC4] No findings.** The fail-closed ESC + `modal_dismissed{denied_timeout,timeout}` is asserted against the **real** 2-min window (no shrink shortcut that could mask a mis-wired timer), and the post-deadline answer is asserted a no-op — proving the timeout consumed the modal.
- **[Output redaction] No findings.** No new production logging. The fake emits a synthetic prompt (`"Do you want to proceed?"`, no secrets); the test's diagnostics echo only ASCII markers and the opaque `modal_id`. The emitter/resolver never-log-body discipline is inherited unchanged.
- **[File operations] No findings.** The modal-trigger and stdin-log paths are **test-controlled** temp files under the test `home`, never phone/network-controlled (same shape as the sanctioned `emitIdleIfTriggered`/rotation triggers). `emitModalIfTriggered` writes a fixed literal and removes a test-owned path.
- **[Cryptographic primitives] No findings.** Exercises the shipped Noise_IK path; the gated phone decrypts with its own session `CipherState`. No new crypto.
- **[Subprocess / external command] No findings.** fakeclaude is the shipped e2e stand-in; the new mode adds no `exec`, no user-controlled args, emits a fixed literal, reads/removes a test-owned trigger.
- **[Concurrency] No findings.** No new production goroutines. The answer-vs-timeout single-owner serialization is the shipped engine's; #791 never races them within a modal. The fake's modal write is on its single main goroutine; the stdin reader still only appends the log.
- **[Threat-model alignment] No findings.** Confirms the epic-#597 remote-permission guarantee live; the deterministic oracle stays upstream (#717/#725). The `modalDenyTimeout` config gap is surfaced, not silently accepted (§ Open questions).

**Reviewer:** architect (self-review; `security-sensitive` gate). **Date:** 2026-07-07.
