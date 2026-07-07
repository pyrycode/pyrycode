# Spec #793 — Live e2e capstone: two-head first-answer-wins (phone + `pyry attach`)

**Part of EPIC #597** (Phase 3 live exit gate). Split from #708. Sibling of the #791 remote-permission capstone (this reuses its harness + fakeclaude modal trigger) and #642/#792. `security-sensitive`.

## Verdict up front: reuse #791's harness + one additive fakeclaude mode + a local attach head, ship as one S ticket

This is a **test/harness-only** capstone. The two-heads modal-ownership control loop already shipped and was proved deterministically upstream: **#706** (first-answer-wins across the local `pyry attach` TTY head + the paired phone; the local `handleModalHidden` arm that `Resolve`s the shared registry and broadcasts `modal_dismissed{source: local}`), on top of #716/#717/#725 (surface / gated answer / deny-on-timeout). The **producer** was live-wired into the daemon by **#798** (merged, PR #805): `startInteractiveModalStreamV2` (`cmd/pyry/interactive_modal_stream_v2.go`) drives `runModalStream`, which handles **both** `EventKindPtyModalShown` **and** `EventKindPtyModalHidden` through the #706 emitter, over **the same `modalReg` the inbound resolver consumes** — so the cross-head `Resolve` arbitration is live on `main`. This ticket lands the **live** confirmation that a permission prompt observed by both a phone and a local `pyry attach` is resolved first-answer-wins: the local head answers at the TTY, the phone's `modal_shown` is dismissed, and the phone's late answer is a no-op. It confirms; it does not re-prove — the correctness oracle stays upstream (#706).

**Block cleared and the live trigger is genuinely on `main` (not scripted-inject).** The blocker #791 is CLOSED/merged (PR #807, `c380808`), which brought the shared permission-raising fakeclaude trigger (`PYRY_FAKE_CLAUDE_MODAL_TRIGGER`) onto `main`. The verified-necessary second live trigger — the **local-dismiss** path (`Modal Hidden → handleModalHidden → modal_dismissed{local}`) — is wired by #798 + #706, confirmed by reading `runModalStream` (it calls `emitter.Handle` on `EventKindPtyModalHidden`) and the shared-registry construction (`newInteractiveModalEmitterV2(modalReg, mgr, mgr, logger)`, same `modalReg` as `newModalResolverV2`). [[po-cleared-block-reverify-next-gate-shared-harness-trigger]] re-verify: the trigger this capstone needs exists live on `main`; no re-block.

**Harness = #791's fakeclaude two-phone relay bring-up, PLUS a local `pyry attach` head, PLUS one additive fakeclaude mode.** The one piece #791's harness does not provide: #791's `MODAL_TRIGGER` fake **never clears the modal** (each #791 test answers via the *phone*, and the daemon's remote arm broadcasts the dismissal regardless of whether claude's screen changes). But #793's first-answer-wins runs through the **local** arm (`handleModalHidden`), which fires **only** on `EventKindPtyModalHidden` — which fires **only** when claude's modal screen actually vanishes. So the fake must **clear its modal in response to the local answer keystroke**, making that keystroke the *cause* of the Hidden event (the [[architect-interrupt-live-capstone-esc-drives-turnend]] structural-causality pattern, mirroring #794's Esc→turn_end). This is one additive, env-gated fakeclaude mode; **do not** change `MODAL_TRIGGER`'s default (that would break #791 — see § Part 1).

**One test function, one flow — do NOT split the ticket.** The four ACs decompose into exactly one single-modal scenario (both surfaces observe → local answers → phone dismissed → phone late answer is a no-op). This is the [[po-plus-title-capstone-is-one-flow]] shape.

## Files to read first

- `internal/e2e/relay_v2_modal_answer_test.go` (whole file, #791) — **the primary structural template.** `bringUpModalHarness` (align sessions dir, pre-create `<initialUUID>.jsonl`, `StartRotationWithRelay` with `PYRY_FAKE_CLAUDE_MODAL_TRIGGER`, pair `--allow-remote-permissions`, `fakephone.Dial`, `driveHandshakeToOpenDaemonInteractive`, the `sealSend`/`nextEnv` closures), `awaitModalShown` (the vacuous-pass surface guard), and `assertNoAnswerDigit`. #793 writes its own richer bring-up (adds the attach head; needs the `*Harness` for `SocketPath`/`Stderr`) but **reuses the freestanding helpers verbatim** — do not perturb `bringUpModalHarness`/`modalHarness` (they are frozen for #791).
- `internal/e2e/attach_pty.go:114-149` (`StartAttach`'s attach-spawn block) + `:66-95` (pty-probe/skip + short-home) — **the local-attach-head pattern to lift.** `pty.Open()` → `t.Skipf` on failure → `exec.Command(bin, "attach", "-pyry-socket="+socket)` with `Stdin/Stdout/Stderr = slave`, `SysProcAttr{Setsid:true, Setctty:true}`, `childEnv(home)` → the early-death `select`. #793 spawns this **against the relay daemon's `h.SocketPath`** (not a fresh bridge daemon); the test reads/writes the pty **Master** to observe the modal and type the answer.
- `internal/e2e/internal/fakeclaude/main.go` — **the file to extend.** Read the `MODAL_TRIGGER` mode (`emitModalIfTriggered` :409, the `modalScreen` const :162, the `modalShown` gate in `main` :295), the `ESC_ENDS_TURN` precedent (`escPending atomic.Bool` :199 + its reader `Store` :543 + its one-shot main-loop drain :313 — **the exact signal shape #793's clear mirrors**), `startStdinReader` :521, `writeStdout` :213, the env-const block :134, and the package doc. **Already on the substrate-guard allowlist** (`cmd/substrate-guard/main.go`).
- `cmd/pyry/interactive_modal_stream_v2.go` (#798, merged) — the **producer under test.** `runModalStream` handles `EventKindPtyModalHidden → emitter.Handle(ctx, ev, "")`; `startInteractiveModalStreamV2` constructs the emitter over `modalReg` "the same modalReg the inbound resolver consumes". Read to confirm the live local-dismiss path #793 exercises end to end.
- `docs/specs/architecture/706-two-heads-modal-ownership.md` § Design (`handleModalHidden` — the local arm) + § Concurrency (first-answer-wins is the one-shot `Resolve`; the winner audits `{outcome: dismissed_local, source: local}` and broadcasts, the loser is a silent no-op). **The correctness oracle this capstone confirms live.**
- `internal/audit/audit.go:49` (`OutcomeDismissedLocal = "dismissed_local"`), `:61` (`SourceLocal = "local"`), `:69-81` (`Log` → slog `"audit: remote permission decision"`, fields incl. `outcome`/`source`) — the **live audit-trail oracle**: the local arm emits this to the daemon slog; `dismissed_local` is a unique, format-agnostic marker greppable in `h.Stderr`.
- tui-driver `pkg/tuidriver/events.go:308-324` — `cur.modal != prev.modal` fires `Hidden(prev)` then `Shown(cur)`; a `Permission→Unknown` transition yields `EventKindPtyModalHidden{Modal: Permission}` (what the local answer must cause). `pkg/tuidriver/modal.go:139-169` (`DetectModalClass`, `ContainsInLastRows(anchorPermissionSpaced, permissionRegionRows=12)`) + `grid.go:88-99` (`ContainsInLastRows` scans the last `min(12, len(rows))` rows) — **why the clear-screen must push "Do you want to proceed" out of the bottom 12 rendered rows to return to Unknown.**
- `internal/protocol/messaging.go` — `ModalShownPayload{ModalID, Class, Options[]{ID,Label}, ...}`, `ModalAnswerPayload{ModalID, OptionID, AnswerToken}`, `ModalDismissedPayload{ModalID, Outcome, Source}` — the wire shapes the phone decodes/sends.
- `internal/e2e/harness.go:318-360` (`StartRotationWithRelay`, the `extraEnv ...string` seam), `:429-520` (`spawnWith` — daemon stdin defaults to `/dev/null` → `IsTerminal` false → **bridge mode**, which is what makes the relay daemon attachable) — pass the new clear-on-answer env via `extraEnv`; **no harness.go change.**
- `docs/specs/architecture/791-remote-permission-answered-e2e-capstone.md` § AC5 — the flight-recorder deferral this ticket's **AC4** mirrors one-for-one.

## Context

Phase 3 of the mobile structured stream. A surfaced permission/trust modal can be resolved by **two heads**: the local `pyry attach` TTY (the operator types a choice directly into claude) or a paired phone (`modal_answer`). #706 made resolution single-shot across both heads by routing every arm through the registry's one-shot `Resolve`, and added the local arm: when the local TTY answers, claude's modal vanishes, tui-driver fires `EventKindPtyModalHidden`, the emitter `Resolve`s the tracked `modal_id`, and — if it won the race — broadcasts one `modal_dismissed{source: local}` so the phone learns the modal is gone; a subsequent phone answer misses the consumed nonce and is a no-op. #706 proved this by unit test; #798 live-wired the producer. What is **not** yet proved is the **live** end-to-end behaviour over one running daemon hosting a real supervised child, a connected phone, **and** a connected local attach. This capstone is that confirmation. **Test/harness code only** — a surfaced production gap is a separate ticket.

## Design

### Part 1 — fakeclaude: clear the modal on the local answer keystroke (additive, ~25-35 LOC)

**Why this is required.** #793's first-answer-wins runs through the **local** arm, which is `EventKindPtyModalHidden`-driven. That event fires only on a real `Permission→Unknown` screen transition. #791's `MODAL_TRIGGER` fake raises the modal and never clears it — sufficient for #791 (the phone answers and the *remote* arm broadcasts regardless of the screen), but the local arm would never fire. So the fake must clear its modal when the local head answers, making the keystroke the *cause* of the Hidden event.

**Why a new env, not a change to `MODAL_TRIGGER`.** If `MODAL_TRIGGER` cleared the modal on *any* answer keystroke, #791 Test A would break: there the phone's answer routes `"2\r"` to the fake's stdin, and clearing on it would fire the local `Resolve` **racing** the remote `ResolveAnswer` for the same `modal_id` — a coin-flip between `modal_dismissed{source: remote}` (what #791 asserts) and `{source: local}`. So the clear behaviour is gated behind a **new** env and is **byte-identical to today when unset** — the [[architect-additive-entry-point-when-golden-tests-back-back-compat-ac]] discipline. #793 sets both `PYRY_FAKE_CLAUDE_MODAL_TRIGGER` (raise) and the new env (clear-on-answer); #791 sets only the former and stays frozen.

New env `PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER` (any non-empty value; only meaningful in modal mode). Contract:

- **Unset (default): byte-identical to today.** Every existing caller is unperturbed.
- **Set (with `MODAL_TRIGGER`):** after the modal is shown (the existing `modalShown` gate has fired), the **first stdin bytes** the reader observes are the local head's answer keystroke — the reader already `Store`s a signal on every read (`turnPending`). Add a sibling one-shot signal (mirror `escPending`): a `clearPending atomic.Bool` the reader sets alongside `turnPending`, and a **one-shot main-loop drain** gated on `modalShown && !modalCleared && clearPending.Swap(false)` that writes a **clear screen** (`writeStdout`, fsync) transitioning the detected class `Permission→Unknown`, then sets `modalCleared`. Only the main goroutine writes stdout (modal mode emits no spinner), preserving the single-writer discipline; the reader only signals, never writes `f` or stdout (the single-writer-of-`f` invariant is untouched).

New helper — **signature + behaviour only** (developer writes the body, mirroring `appendTurnEnd`/`emitModalIfTriggered`):

```
// clearModalScreen writes modalClearScreen to os.Stdout once (fsync'd) so
// tui-driver's detected class transitions Permission->Unknown and the merge
// loop fires EventKindPtyModalHidden — the daemon's #706 local arm then
// Resolve()s the modal and broadcasts modal_dismissed{local}. Main goroutine only.
func clearModalScreen()
```

`modalClearScreen` is a package const in the fake (allowlisted file). Its **only hard requirement**: rendered *after* `modalScreen`, the combined screen must classify as **not-Permission** — i.e. it must push `"Do you want to proceed"` out of the bottom `permissionRegionRows` (12) rendered rows. A run of ≥13 `\r\n` newlines followed by the idle glyph (`❯`) does this (the anchor scrolls above the 12-row window; the idle glyph re-establishes the idle baseline). The exact form is the developer's to author, guided by the mandatory detection assertion below.

**De-risk the fixture before the e2e (required, extends #791's assertion).** In `internal/e2e/internal/fakeclaude/modal_detect_test.go` add one harness-free assertion:
- `DetectModalClass(modalScreen) == ModalClassPermission` (the #791 assertion, unchanged — the raised modal still classifies).
- `DetectModalClass(modalScreen + modalClearScreen) != ModalClassPermission` (the new one — the cleared screen returns to non-Permission). Feeding the concatenated bytes to a fresh `DetectModalClass` reproduces the daemon's sequential-write vt10x state deterministically, so this predicts the live `Permission→Unknown` transition in milliseconds — the developer must not discover a non-clearing screen inside a live-daemon run.

Add the env const to the const block and a package-doc paragraph mirroring the `PYRY_FAKE_CLAUDE_MODAL_TRIGGER` doc; note it **extends** modal mode (requires `MODAL_TRIGGER`) and is byte-identical when unset.

### Part 2 — the local `pyry attach` head (e2e harness, in the new `_test.go` file)

The relay daemon `StartRotationWithRelay` spawns is in **bridge mode** (its stdin is `/dev/null` → `IsTerminal` false) and exposes `h.SocketPath`, so a local `pyry attach -pyry-socket=<h.SocketPath>` binds the **same** daemon's bootstrap session — the ADR-025 two-heads model (local `output` head + phone observer coexist). Lift `StartAttach`'s attach-spawn block: `pty.Open()` (`t.Skipf` if unavailable), `exec.Command(bin, "attach", "-pyry-socket="+h.SocketPath)` with the slave on stdio and `SysProcAttr{Setsid:true, Setctty:true}` and `childEnv(h.HomeDir)`, the early-death `select`, and a `t.Cleanup` that closes Master/slave and kills the attach process (reuse `killSpawned`). The helper returns the pty **Master**; the test writes the answer keystroke to Master and reads Master to observe the modal on the local head's screen.

Two facts make the input/output faithful:
- **Input is raw passthrough.** Attach bridges keystrokes straight to claude's PTY via `Session.AttachInput` (not `DeliverPrompt`, which bracketed-pastes phone turns) — so a byte written to Master reaches the fake's stdin verbatim and is logged to `PYRY_FAKE_CLAUDE_STDIN_LOG` (the "winning keystroke delivered exactly once" oracle).
- **Output includes the modal.** The Bridge fans claude's PTY output to the local `output` head, so the attach's screen carries `modalScreen` (the `"Do you want to proceed?"` anchor) once it is raised — provided the attach is bound **before** the modal is raised (the test connects the attach first, then drops the trigger).

**Ordering: attach connects before the modal is raised.** The bring-up brings up the relay daemon + phone (as #791), then spawns the attach head and confirms it is live, then the test raises the modal — so both heads observe it.

### Part 3 — the test (`internal/e2e/relay_v2_two_head_modal_test.go`, new, `//go:build e2e`)

`bringUpTwoHeadModalHarness(t)` — a local bring-up mirroring #791's `bringUpModalHarness` but (a) adding `"PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER=1"` to the `StartRotationWithRelay` `extraEnv`, (b) spawning the local attach head (§ Part 2), and (c) returning the phone helpers (`sealSend`/`nextEnv`), the attach `Master`, `modalTrig`, `stdinLog`, and the `*Harness` (for `SocketPath`/`Stderr`). It reuses the freestanding #791 helpers (`decodePairPayload`, `readPersistedServerID`, `waitBinaryHello`, `driveHandshakeToOpenDaemonInteractive`, `encodeWorkdir`) verbatim; it does **not** mutate `bringUpModalHarness`.

**`TestRelayV2_TwoHeadFirstAnswerWins` (AC1, AC2, AC3, + AC4 audit-trail evidence):**

1. **[Vacuous-pass positive #1 — phone observes]** (AC1 phone side). Drop `modalTrig`; drain the phone under one long deadline until `modal_shown`, `t.Fatal` if none before ~20 s. Assert `Class == "permission"` and the four fixed option IDs (`allow_once..reject_always`); capture `modalID`. (Reuse `awaitModalShown` / its assertions.)
2. **[Vacuous-pass positive #2 — local attach observes]** (AC1 attach side). Read the attach Master under a deadline until the output contains the `"Do you want to proceed?"` anchor; `t.Fatal` if absent (the "the local head never saw the prompt" failure mode — without it the loser-no-op is vacuous). Both surfaces have now provably observed the same prompt.
3. **[Winner — local answers first]** (AC2). Write the local answer keystroke to Master (e.g. `"1"`, allow_once at the TTY; the fake clears on the first post-modal byte). The fake clears the modal → `EventKindPtyModalHidden` → the local arm `Resolve`s and broadcasts. Await `modal_dismissed` on the phone; assert `ModalID == modalID`, `Source == "local"`, `Outcome == "dismissed_local"`. This is the phone learning its `modal_shown` is gone (AC3 "the phone modal clears").
4. **[Winning keystroke delivered exactly once]** (AC2). The dismissal is a happens-after fence (the fake fsyncs stdin per write). Read `stdinLog`; assert it contains the local answer keystroke exactly once. Snapshot the log length for step 6.
5. **[Live audit-trail evidence]** (AC4, in lieu of the deferred flight recorder — see § AC4). Assert `h.Stderr` contains `dismissed_local` (the local arm's `audit: remote permission decision` line with `source=local`), proving the resolution is recorded to the audit trail live.
6. **[Loser — phone's late answer is a no-op]** (AC3). Send `modal_answer{modalID, "allow_once", <token>}` from the phone **after** the local dismissal. It misses at `Lookup` (already consumed by the local `Resolve`) → no keystroke, no dismissal. Assert **no** further `modal_dismissed` within a short deadline, **and** `stdinLog` is unchanged (no additional keystroke routed — `assertNoAnswerDigit`-style plus a length-unchanged check).

Assertion order is load-bearing: the two positives (both surfaces observed) and the winner dismissal gate the loser-no-op negative — a "late answer ignored" over a prompt a surface never saw, or over a modal never resolved locally, cannot pass vacuously.

### Data flow

```
[bring-up] phone connected (interactive) + local pyry attach bound to bootstrap session

os.WriteFile(modalTrig)
  → fakeclaude emitModalIfTriggered → modalScreen ("Do you want to proceed?")
  → supervisor tui-driver Session: DetectModalClass Unknown->Permission
  → EventKindPtyModalShown{Permission}
  → runModalStream (#798) → emitter.handleModalShown → Record (mint modal_id)
      → ArmModalTimeout (2-min, harmless here) → broadcast modal_shown → phone   [AC1 phone]
  → Bridge output head → attach Master shows "Do you want to proceed?"           [AC1 attach]

local attach types "1" → Master → Bridge.Read → Session.AttachInput → fake stdin (logged) [AC2 exactly once]
  → clearPending → fakeclaude clearModalScreen → modalClearScreen
  → DetectModalClass Permission->Unknown → EventKindPtyModalHidden{Permission}
  → runModalStream → emitter.handleModalHidden
      → reg.Resolve(modal_id) WINS → audit{outcome:dismissed_local, source:local}  [AC4 audit line → h.Stderr]
      → broadcast modal_dismissed{Outcome:"dismissed_local", Source:"local"} → phone [AC2/AC3 phone clears]

phone → modal_answer{modal_id, allow_once, token}   (LATE)
  → handleModalAnswer → ResolveAnswer → Lookup MISS (consumed) → no keystroke, no dismissal [AC3 no-op]
```

## Concurrency / timing model

No new production goroutines (test-only). Pre-existing actors and fences:

- **First-answer-wins is structural** (#706 § Concurrency): the local `Resolve` (producer/Run goroutine) and the phone `ResolveAnswer` (relay dispatch goroutine) both serialize on the shared registry's leaf mutex; the one-shot `Resolve` is the single arbiter. #793 sequences the two deterministically (the local answer completes — the test waits for `modal_dismissed{local}` — **before** the phone's late answer is sent), so there is no live race to flake on; the capstone confirms the *sequenced* first-answer-wins #706 proved.
- **Modal detection is poll-based** (tui-driver merge loop, `pollInterval`): `modal_shown` and `modal_hidden` each appear within one tick of the causing write. Use deadlines, not fixed sleeps.
- **Attach input is fire-and-forget raw passthrough:** the local keystroke is a single non-blocking PTY write; nothing blocks on the modal clearing.
- **fsync visibility:** the fake fsyncs the modal-screen write, the clear-screen write, and each stdin-log write; read `stdinLog` only **after** observing `modal_dismissed{local}` (a happens-after fence) and `h.Stderr` after the same signal.
- **Phone read discipline (603.md/634.md):** single long deadline, back-to-back in-order decrypts; never a short poll on a phone conn. The `ArmModalTimeout` 2-min deny never fires in this test (the local answer resolves the modal within seconds; a later timeout `Resolve` would miss) — #793 does **not** wait the 2-min window (unlike #791 Test B), so it is a fast test.

## Error handling

- **Harness produced no modal** → the phone-surface `t.Fatal` (step 1).
- **Local head never saw the modal** → the attach-anchor `t.Fatal` (step 2) — catches an attach that bound after the modal write, or a Bridge that did not fan output to the local head.
- **Local answer did not clear / did not resolve** → the `modal_dismissed{local}` `t.Fatal` (step 3) fires before the loser-no-op is evaluated.
- **pty unavailable** (sandboxed CI) → `t.Skipf` up front (the `StartAttach` pattern), not a failure.
- Handshake / seal / decrypt / pair-exit errors → `t.Fatalf`, consistent with #791/#792/#642.

## Testing strategy — AC mapping

One `//go:build e2e` test + one extended harness-free detection assertion:

- **AC1** (both surfaces observe): steps 1-2 — phone `modal_shown{permission, 4 options}` **and** the attach Master carries the `"Do you want to proceed?"` anchor; each with a dedicated `t.Fatal`.
- **AC2** (whichever answers first wins; winning keystroke exactly once): steps 3-4 — the local attach answers first; `modal_dismissed{source: local}` reaches the phone and claude proceeds (modal cleared); `stdinLog` contains the local keystroke exactly once.
- **AC3** (loser dismissed + late answer no-op): steps 3 + 6 — the phone's `modal_shown` is dismissed by the local answer; the phone's subsequent `modal_answer` yields no second `modal_dismissed` and routes no keystroke (`stdinLog` unchanged).
- **AC4** (recorded for the audit trail): step 5 — `h.Stderr` contains the local arm's `dismissed_local` audit line (live audit-trail evidence). The **flight recorder proper** (`PYRY_RECORD_DIR`) is deferred to the operator live-stack run, mirroring #791/#792 (see § AC4).
- **Vacuous-pass guard:** both observe-positives + the winner dismissal are hard preconditions of the loser-no-op negative, each with a `t.Fatal` naming its failure mode (mirrors #642/#791/#792).
- **Fixture detection assertion:** `modalScreen == Permission` (unchanged) and `modalScreen+modalClearScreen != Permission` (new), harness-free, in `modal_detect_test.go`.

Gates the developer runs green: `go build ./cmd/pyry`, `go vet ./...`, `staticcheck ./...`, `go test -race ./...`, `make substrate-guard`, and `go test -tags=e2e -run 'TestRelayV2_TwoHeadFirstAnswerWins' ./internal/e2e/...`. Run under `-count=3` for determinism (#603); the deterministic sequencing (no live race) makes it stable. The `e2e_realclaude` column is untouched.

## AC4 — flight recorder (deferred to operator live-stack; audit line asserted live)

AC4 ("recorded by the flight recorder for the audit trail") is the **same wording and disposition as #791's AC5 / #792's AC4**. The flight recorder lives in `internal/agentrun/ptyrunner` (reached from `pyry agent-run`), while the mobile-relay flow hosts claude under `internal/supervisor`, which wires no `PYRY_RECORD_DIR`/`RecordTo`. The two paths are disjoint — the fakeclaude-replay variant cannot exercise the flight recorder. **Disposition:** the flight-recorder assertion is deferred to the operator live run (`docs/knowledge/features/mobile-live-e2e-runbook.md`) and is **not** a developer deliverable — no test code for it.

**What #793 *does* assert live for AC4's intent:** the local arm's `internal/audit` record (`audit: remote permission decision`, `source=local`, `outcome=dismissed_local`) **is** wired (the emitter holds the daemon logger) and lands in `h.Stderr`. #793 asserts it (step 5) as the live audit-trail evidence — a *stronger* confirmation for this ticket specifically, since auditing `source=local` is the distinguishing security-relevant behaviour of the local arm. (Deeper gap, per #792's sharpening: the supervisor-hosted mobile session is not PTY-recorded in any variant today; genuine live flight-recorder capture = a separate production ticket to wire `RecordTo` into the supervisor — surfaced, not folded.)

## Scope (S confirmed)

Production source files (`.go`, non-test, excluding `*_test.go` / `*.md` / the spec):

1. `internal/e2e/internal/fakeclaude/main.go` — modified (~25-35 LOC: one env const, one `clearPending atomic.Bool` + its reader `Store`, one one-shot main-loop drain branch, the `clearModalScreen` helper + `modalClearScreen` const, one package-doc paragraph). Purely additive; byte-identical when the new env is unset; existing modes untouched.

**Count: 1** (the §4 ≥5-file gate is far off). New files: **1** — the e2e test (`relay_v2_two_head_modal_test.go`, one test + the local bring-up + the attach-head helper, all in the `_test.go` file). New exported types: **0**. Consumer call sites updated: **0** (additive env; zero cascade). State-machine reject branches: **0** (test-only). ACs of work: **3 live (AC1-AC3) + AC4 audit-line + AC4 flight-recorder deferred** — under 5.

Total LOC: ~30 (fake) + ~15 (detection assertion) + ~280 (test + bring-up + attach helper) ≈ **~325**. Under ~600. **No harness.go change** (`extraEnv` seam), **no substrate-guard change** (fake is allowlisted; the clear-screen is plain newlines + the already-allowlisted idle glyph), **no handshake-helper change**, **no production (`cmd/pyry`, `internal/*`) change** (the two-heads control loop shipped in #706 and is live-wired by #798).

All red lines clear with margin. **Single ticket — no split.**

## Open questions

- **Clear-screen fixture form.** The spec fixes the *requirement* (`DetectModalClass(modalScreen+modalClearScreen) != Permission`) and mandates the harness-free assertion; the exact newline count / trailing content is the developer's to author. If a minimal form still classifies as Permission at the harness PTY size, add newlines until the anchor clears the bottom-12 window — the unit assertion catches this in milliseconds, never inside a live run.
- **Local keystroke choice.** `"1"` (allow_once at the TTY) is the natural faithful answer; the fake clears on the first post-modal byte regardless, so the exact byte is non-load-bearing for the clear, only for the stdin-log oracle. The developer picks a byte and asserts it appears exactly once.
- **Live flight-recorder capture on the mobile path** (wire `RecordTo`/`PYRY_RECORD_DIR` into `internal/supervisor`) is the production ticket AC4 implicitly needs; surfaced, not folded (matches #791/#792).

## Security review (label `security-sensitive`, mandatory; `agents/architect/security-review.md` absent — inline, following the 9-category structure of #706/#791/#792)

**Verdict: PASS.**

Test/harness-only; the two-heads production code (#706/#716/#717/#725) shipped and was security-reviewed there, and #798 (security-sensitive) reviewed the live wiring. The adversarial question is **"could this test PASS while the guarantee is broken?"** The guarantee: a permission observed by two heads is resolved **exactly once** (first-answer-wins across the local TTY and the phone), the loser's prompt is dismissed, and the loser's late answer routes **no** keystroke and is audited.

- **[Vacuous pass — the headline requirement] No MUST FIX.** Ordered architect-owned guards: (1) the phone must observe `modal_shown` (`t.Fatal`); (2) the local attach must observe the anchor on its own screen (`t.Fatal`) — **both surfaces provably saw the same prompt** before any no-op is asserted; (3) `modal_dismissed{source: local}` must reach the phone (`t.Fatal`) before the loser-no-op. A "late answer ignored" over a prompt a surface never saw, or over a modal never resolved locally, cannot pass vacuously. This directly answers the ticket's own vacuous-pass Technical Note.
- **[Trust boundaries] No MUST FIX.** The boundary is exactly what the test drives, unchanged: the local TTY is the trusted operator acting at the physical terminal (no device gate — correctly, per #706 § Threat model), the phone is a paired interactive device, and the one-shot `Resolve` on the shared `modalReg` is the single cross-head arbiter. The test adds no surface; it exercises the shipped local + remote arms over the live #798 wiring.
- **[First-answer-wins / exactly-once — AC2/AC3] No findings.** The winner is asserted to route its keystroke **exactly once** (stdin log) and the loser's late `modal_answer` to route **zero** (stdin log unchanged) with **no** second `modal_dismissed`. A regressed arbiter (e.g. a non-consuming `Resolve`, or a remote path that routes a keystroke after a local resolution) would fail the loser-no-op assertion. The local answer is sequenced strictly before the phone's late answer, so the test confirms the deterministic #706 arbitration, not a flaky race.
- **[Structural causality — the live trigger] No findings.** The local `modal_dismissed{local}` is caused by a **real** `EventKindPtyModalHidden`, which is caused by the fake genuinely clearing its modal screen in response to the local keystroke (verified by the `modalScreen+modalClearScreen != Permission` detection assertion). The test cannot pass on a scripted dismissal that bypasses the live Hidden→local-arm path.
- **[Output redaction] No findings.** No new production logging. The fake emits a synthetic prompt (`"Do you want to proceed?"`, no secrets) and a newline/glyph clear screen; the audit line carries only the opaque `modal_id`, the closed `modal_class`, and the sentinel `outcome`/`source` (empty device — a local resolution has no answering device). The emitter/audit never-log-body discipline is inherited unchanged.
- **[File operations] No findings.** The modal-trigger, clear-screen (in-process stdout), and stdin-log paths are **test-controlled** temp files under the test `home`, never phone/network-controlled — the sanctioned shape of the existing fake triggers.
- **[Cryptographic primitives] No findings.** Exercises the shipped Noise_IK path; the phone decrypts with its own session `CipherState`. No new crypto; the `modal_id` is an opaque correlation nonce.
- **[Subprocess / external command] No findings.** The local `pyry attach` is the shipped attach client (the sanctioned `StartAttach` pattern), spawned against the test daemon's own socket with a test-owned pty; no user-controlled args. The new fake mode adds no `exec`, emits fixed literals, and reacts only to test-driven stdin.
- **[Concurrency] No findings.** No new production goroutines. The cross-head exactly-once is the shipped registry's one-shot `Resolve` (leaf-mutex serialized); the emitter's tracking fields stay single-goroutine (the sole caller of `Handle` is `runModalStream`). The fake's clear write is on its single main goroutine (reader signals only, never writes stdout or `f`) — the single-writer discipline is preserved.
- **[Threat-model alignment] No findings.** Confirms epic-#597's ADR-025 §4 first-answer-wins across two heads **live**: a local TTY resolution emits `modal_dismissed{local}`, the phone's pending answer becomes stale and is rejected on the consumed nonce, and the resolution leaves exactly one audit record. The deterministic oracle stays upstream (#706).

**Reviewer:** architect (self-review; `security-sensitive` gate; canonical procedure file absent). **Date:** 2026-07-07.
