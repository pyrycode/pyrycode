# Spec #994 — Auto-continue claude's Settings Warning startup dialog + log it

**Ticket:** #994 — Auto-continue claude's Settings Warning dialog at interactive startup and log it (split from #988)
**Size:** S (confirmed downward-stable; 2 production files, 0 new exported types)
**Security-sensitive:** No. Local default-acceptance of a startup warning over the operator's OWN host config (a trusted party) — not a remote-consent decision. The trust dialog (a consent gate forwarded to a remote client) is a separate ticket. See § Substrate boundary for the one screen-literal this introduces and why the `substrate-guard` gate stays green.

---

## Context

Observed live (#988, 2026-07-15): the operator's daemon wedged **every** UI-created conversation on claude's **Settings Warning** startup dialog. The workdir's `.claude/settings.json` carried malformed permission rules; claude stops at startup with `Settings Warning … 1. Continue / 2. Fix with Claude / 3. Exit and fix manually`, and every queued turn parked in the silent `msgqueue` retry loop until the operator hand-deleted the rules.

The Settings Warning is **informational** — claude's own semantics are "the values listed above were skipped; the rest of the file is in effect," and Continue is the default. The interactive supervisor should auto-answer Continue (option `1`) and log the warning, **not** forward it as a remote modal and **not** wedge.

**Why this is a bug today.** When claude shows the Settings Warning, `Session.WaitReady` (tui-driver) reaches idle — the dialog's `❯` pointer *is* the idle glyph (U+276F), so `IsIdle` matches — then classifies the screen. The Settings Warning matches no known `ModalClass` anchor, so it classifies as `ModalClassUnknown`, and the #224 structural probe (`gridHasSelectionDialog`) fires on the `❯ 1. …` row. `WaitReady` therefore returns `*tuidriver.UnexpectedModalError{Class: ModalClassUnknown}` (#173). In `deliverViaSession` that surfaces as a loud `wait ready: …` error → `WriteUserTurn` fails → `msgqueue` retries forever → wedge.

The fix refines that already-trusted "an unexpected startup dialog is blocking" signal into "is it specifically the Settings Warning?", and if so auto-answers Continue instead of failing loud.

---

## Files to read first

- `internal/supervisor/supervisor.go:339-373` — `deliverViaSession`, the production `deliverFn`. **The two `sess.WaitReady(ctx)` call sites to wrap** are line 349 (direct path, `ResolveTranscript == nil`) and the `waitReady` closure at 363-366 (growth path). Both must route through the new gate.
- `internal/supervisor/supervisor.go:382-465` — `deliverGrowthDeps` + `confirmViaTranscriptGrowth`. **Mirror this exact pattern**: a seam-struct + free function whose timeout/poll are fields so parallel `-race` tests can shrink them without shared globals. The new gate copies this idiom.
- `internal/supervisor/supervisor.go:477-499` — `ScreenSnapshot`. **Mirror its one-expression render discipline** (`tuidriver.Render(sess.Snapshot(), 0, 0)` consumed inline, never named/stored) so the detector adds no rendered-text variable — only the anchor const.
- `internal/supervisor/supervisor.go:575-596` — `New`. Add the `settingsWarningFn` seam wiring right after `s.keystrokeFn = sendModalKeystroke` (line ~594), same set-once-immutable pattern.
- `internal/supervisor/modal.go:96-144` — `sendModalKey` / `sendModalKeystroke` / the `modalKey` enum. The auto-continue keystroke reuses `keyAnswer` with choice `"1"` through the existing `keystrokeFn` seam. `sendModalKeystroke` is the precedent for a thin, unit-untestable-without-live-claude seam impl (the detector mirrors it).
- `internal/supervisor/modal_test.go:15-111` — the `keystrokeFn`-fake pattern (`sup.keystrokeFn = func(...) error {…}` + `sup.setSession(&tuidriver.Session{})`). The new tests fake `settingsWarningFn` the same way.
- `github.com/pyrycode/tui-driver@v1.10.0/pkg/tuidriver/ready.go` — `WaitReady` contract + `UnexpectedModalError{Class ModalClass}` (exported; match with `errors.As`). Confirms the Settings Warning returns `Class: ModalClassUnknown`.
- `github.com/pyrycode/tui-driver@v1.10.0/pkg/tuidriver/answer.go:38-134` — `AnswerModal`/`modalDismissed`. **Read this to understand why we cannot reuse it**: `AnswerModal` confirms dismissal but supports only `ModalClassPermission`/`ModalClassTrustFolder`, not `Unknown`. We send the raw `Answer("1")` keystroke and must implement our own dismissal poll (mirroring `modalDismissed`).
- `cmd/substrate-guard/main.go:36-129` — the banned-literal allowlist. Confirms `"Settings Warning"` is **not** a banned token (tui-driver owns none of this dialog), so the anchor const is guard-green.
- `internal/sessions/pool.go:478` — the daemon sets `supCfg.ResolveTranscript`, so the **daemon's readiness gate is the growth-path closure** (line 363). Wrapping both call sites covers daemon + foreground uniformly.
- `internal/agentrun/ptyrunner/runner.go:400-424` — the agent-run path's separate `WaitReady` handling (`ready.TrustModal` etc.). **Do not touch it** — it keeps its fail-loud readiness (#173, AC4). Scoping the fix to `internal/supervisor` leaves it untouched by construction.

---

## Design

All changes live in `internal/supervisor`. No new exported symbols; no cross-package edits.

### New file: `internal/supervisor/settings_warning.go`

Holds the one claude-screen anchor and the auto-continue readiness gate, isolated for reviewability.

**Anchor + detector (thin seam impl, mirrors `sendModalKeystroke`):**

```go
// settingsWarningAnchor is the sole claude-screen literal in internal/supervisor.
// See § Substrate boundary for why this is guard-green and boundary-consistent.
const settingsWarningAnchor = "Settings Warning" // dialog title; verify exact literal — see Open Questions

// detectSettingsWarning is the production settingsWarningFn: it renders the live
// snapshot and reports whether the Settings Warning title is present. Render+match
// in one expression (no named rendered var), mirroring ScreenSnapshot. Nil-derefs
// a zero-value Session like sendModalKeystroke, so it is overridden in tests.
func detectSettingsWarning(sess *tuidriver.Session) bool
```

Behavior: `strings.Contains(tuidriver.Render(sess.Snapshot(), 0, 0), settingsWarningAnchor)`.

**Gate seams + free function (mirrors `deliverGrowthDeps` / `confirmViaTranscriptGrowth`):**

```go
type waitReadyDeps struct {
	waitReady         func(ctx context.Context) error // wraps Session.WaitReady; Readiness is discarded (deliverViaSession already ignores it)
	isSettingsWarning func() bool                     // wraps settingsWarningFn(sess)
	answerContinue    func() error                    // wraps keystrokeFn(sess, keyAnswer, "1")
	log               *slog.Logger
	workDir           string
	dismissTimeout    time.Duration // post-answer dismissal-poll bound; 0 → settingsWarningDismissTimeout
	dismissPoll       time.Duration // dismissal poll cadence;          0 → settingsWarningDismissPoll
}

// waitReadyAutoContinue gates readiness, auto-answering Continue past a Settings
// Warning exactly once. Returns nil once claude is ready; returns the underlying
// error unchanged for every non-Settings-Warning outcome (fail-loud preserved).
func waitReadyAutoContinue(ctx context.Context, d waitReadyDeps) error
```

**`waitReadyAutoContinue` contract (the whole algorithm, ≤20 lines):**

1. `err := d.waitReady(ctx)`; if `nil` → return `nil` (common case: ready, no modal).
2. If `err` is not `*tuidriver.UnexpectedModalError` (via `errors.As`) **or** `!d.isSettingsWarning()` → return `err` unchanged. This preserves fail-loud for ctx cancel/timeout, `*ProcessExitedError`, and every *other* unexpected modal (trust-forward, a future consent gate, the MCP-enablement dialog). **We never blindly type `1` into an unrecognized dialog.**
3. `d.log.Warn("supervisor: auto-continuing claude Settings Warning at startup", "workdir", d.workDir)`.
4. `if kerr := d.answerContinue(); kerr != nil { return fmt.Errorf("auto-continue settings warning: %w", kerr) }`.
5. Wait for the dialog to actually clear (§ dismissal poll below). If it never clears within the bound → return `fmt.Errorf("auto-continue settings warning: dialog still present after answering: %w", err)` (retryable — `msgqueue` re-drives later).
6. `return d.waitReady(ctx)` — re-gate readiness **once**. If the screen is now ready → `nil` (turn proceeds). If a *new* unexpected modal is up → its error surfaces (fail-loud). No recursion — the auto-continue is attempted at most once per delivery.

**Dismissal poll (mirrors `modalDismissed`, ≤12 lines):** a bounded synchronous loop that returns `true` once `!d.isSettingsWarning()`, or `false` on `dismissTimeout`/`ctx.Done()`. Reuses the same detector seam. This step is **load-bearing**: `Answer("1")` only writes the keystroke; the dialog stays rendered (and reads as idle) for a few hundred ms until claude re-renders, so an immediate re-`WaitReady` would re-catch the same dialog and fail loud. Package-level defaults `settingsWarningDismissTimeout` (~2s, matching tui-driver's `DefaultAnswerConfirmTimeout`) and `settingsWarningDismissPoll` (~150ms).

**Wiring helper (`readyDeps`):** `func (s *Supervisor) readyDeps(sess *tuidriver.Session) waitReadyDeps` builds the struct with the real seams — `waitReady` = `func(ctx){ _, err := sess.WaitReady(ctx); return err }`, `isSettingsWarning` = `func() bool { return s.settingsWarningFn(sess) }`, `answerContinue` = `func() error { return s.keystrokeFn(sess, keyAnswer, "1") }`, `log`/`workDir` from `s`, timeout/poll left zero (defaults applied). The keystroke actuates on the **captured `sess`** (not a re-capture), same discipline as `deliverViaSession`.

### Modified: `internal/supervisor/supervisor.go`

- **New field** on `Supervisor`, next to `keystrokeFn`: `settingsWarningFn func(sess *tuidriver.Session) bool` — same set-once-in-`New`, immutable-post-`New`, faked-in-tests seam. Doc it identically to `keystrokeFn`.
- **In `New`**, after `s.keystrokeFn = sendModalKeystroke`: `s.settingsWarningFn = detectSettingsWarning`.
- **`deliverViaSession` two call sites:**
  - Direct path (349): `if err := waitReadyAutoContinue(ctx, s.readyDeps(sess)); err != nil { return fmt.Errorf("wait ready: %w", err) }`.
  - Growth closure (363): `waitReady: func(ctx context.Context) error { return waitReadyAutoContinue(ctx, s.readyDeps(sess)) }`.

The existing `wait ready:` wrap and `confirmViaTranscriptGrowth`'s wrap are preserved for the fail-loud paths.

---

## Concurrency model

No new goroutines, no new locks. The gate runs synchronously on the delivery goroutine (`WriteUserTurn → deliverFn → deliverViaSession → waitReadyAutoContinue`). The dismissal poll is a bounded synchronous loop on that same goroutine, fully bounded by `dismissTimeout` and `ctx` — identical shape to `confirmViaTranscriptGrowth`'s poll. `settingsWarningFn`/`keystrokeFn` are immutable-post-`New`, read lock-free; the captured `sess` pointer follows `deliverViaSession`'s existing capture-then-release discipline (a concurrent teardown lands in tui-driver's teardown-safe PTY-error path → loud error, never a crash).

---

## Error handling

| Outcome | Result |
|---|---|
| `WaitReady` nil | Ready — proceed (unchanged). |
| ctx cancel/timeout, `*ProcessExitedError` | Returned unchanged → fail-loud, retryable (unchanged). |
| `UnexpectedModalError`, **not** Settings Warning | Returned unchanged → fail-loud (#173 preserved for trust/consent/MCP/novel dialogs). No keystroke sent. |
| Settings Warning, keystroke PTY error | `auto-continue settings warning: %w` → fail-loud, retryable. |
| Settings Warning, answered, dialog never clears | `auto-continue settings warning: dialog still present…: %w` → fail-loud, retryable (`msgqueue` re-drives; #1000 give-up still applies as the ultimate backstop). |
| Settings Warning, answered, cleared, ready | `nil` — queued turn delivered. |
| Settings Warning, answered, cleared, **new** unexpected modal | New modal's error surfaces → fail-loud (bounded; no recursion). |

The auto-continue never surfaces a `modal_shown` to the relay/modalbridge — it logs and answers locally, so no client sees it (AC2's "without surfacing it as a client-facing modal", by construction).

---

## Testing strategy

Unit tests in a new `internal/supervisor/settings_warning_test.go`, all at the seam level (no live claude, no screen literals — the anchor stays in production `settings_warning.go`). Fake `waitReady`/`isSettingsWarning`/`answerContinue`; shrink `dismissTimeout`/`dismissPoll` for `-race` speed. Scenarios:

- **Happy (AC1+AC3):** `waitReady` → `UnexpectedModalError{Unknown}` then `nil`; `isSettingsWarning` → true, then false after the answer fires; `answerContinue` records + nil. Assert: returns `nil` (turn proceeds, no wedge), keystroke fired as `(keyAnswer, "1")`, warning logged.
- **Other unexpected modal (AC1 negative / AC4-adjacent):** `UnexpectedModalError` + `isSettingsWarning` false → returns the error unchanged, `answerContinue` **not** called.
- **Non-modal error passthrough:** `waitReady` → `context.DeadlineExceeded` → returned unchanged, no keystroke.
- **First `waitReady` nil:** passthrough → `nil`, no detector/keystroke calls.
- **Keystroke error:** `answerContinue` → boom → error wraps boom with `auto-continue settings warning:` prefix (`errors.Is`).
- **Dialog stuck after answer:** `isSettingsWarning` stays true; tiny `dismissTimeout` → returns error, **exactly one** `answerContinue`, second `waitReady` not reached.
- **Log assertion (AC2):** capture via a `slog` handler writing to a buffer (or the existing test-logger pattern); assert the warning record + `workdir` field.

The production `detectSettingsWarning` is a thin render+substring seam like `sendModalKeystroke`; it is not unit-tested without a live claude (constructing raw claude-screen bytes in a non-allowlisted test file would trip `substrate-guard`). A live/e2e Settings-Warning fixture is **optional** and out of scope for S (it would double CI); the seam-level tests satisfy AC3. If added later, the fake screen must live in the allowlisted `internal/e2e/internal/fakeclaude/main.go`.

Run: `go test -race ./internal/supervisor/... && go vet ./... && staticcheck ./... && make substrate-guard`.

---

## Substrate boundary (why the in-repo anchor is acceptable)

`internal/supervisor` today holds **no** claude-screen literal (see the SECURITY note at `supervisor.go:485`). This spec adds exactly one: `settingsWarningAnchor = "Settings Warning"`. That is a deliberate, contained exception, justified as follows:

1. **`substrate-guard` stays green.** The guard bans a fixed enumerated list of tokens **tui-driver owns** (trust modal, spinner, paste chip, network-failure). tui-driver owns *nothing* about the Settings Warning — it has no `ModalClass` for it and no handling. So a pyrycode anchor for a dialog tui-driver doesn't handle is not the re-coupling the guard exists to prevent.
2. **It refines an already-trusted structural signal.** The anchor is checked **only after** `WaitReady` has already returned `UnexpectedModalError` — i.e. tui-driver has already structurally confirmed (via `gridHasSelectionDialog`, #224) that a genuine blocking selection dialog is up at pre-first-prompt idle. The supervisor adds a *content* signal (title) to a tui-driver *structural* signal — belt-and-suspenders of different fabric, across the layer boundary. The #219/#223 anchor-forgery concern (transcript content quoting an anchor) barely applies here: there is no transcript on screen before the first prompt.
3. **Isolated + documented.** The literal lives in one small file with this rationale, mirroring `ScreenSnapshot`'s one-expression render discipline so no rendered-text variable is stored.

**Long-term home / migration note (out of scope, cross-repo):** the architecturally ideal owner of this anchor is tui-driver — a `ModalClassSettingsWarning` with a structural co-signal + a #221 negative-forgery test — after which the supervisor would key off the typed `err.Class` and drop the literal entirely. That is a tui-driver release + `go.mod` bump (cross-repo), which #994 deliberately avoids per the PO scope decision. Recommend filing a tui-driver follow-up; not a blocker for this ticket.

---

## Open questions

- **Exact anchor literal.** `"Settings Warning"` is the dialog title from the #988 observation. The developer should confirm the exact rendered substring against a real capture before finalizing — the operator hit it live on 2026-07-15, and a `.cast` may exist under the daemon's `RecordDir` (`Config.RecordDir`, #802). If the title renders with box-drawing padding, `strings.Contains` tolerates it; if the title proves unstable, fall back to a distinctive body phrase. A single distinctive substring is sufficient (the structural co-signal already comes from tui-driver).
- **Dismissal bound tuning.** Defaults mirror tui-driver's `DefaultAnswerConfirmTimeout` (2s) / `answerConfirmPoll` (150ms). Confirm these are comfortably inside the daemon's per-turn readiness budget (the growth path already tolerates multi-second `WaitReady`); shrink freely in tests.

---

## Acceptance criteria mapping

- **AC1** (auto-answer Continue instead of parking readiness) → `waitReadyAutoContinue` steps 2-6; happy + "other modal" tests.
- **AC2** (structured `slog` warning, not a client-facing modal) → step 3 `log.Warn` with `workdir`; no `modal_shown` emitted (by construction); log-assertion test.
- **AC3** (readiness proceeds, queued turn delivered — a test drives a Settings-Warning session and asserts the turn runs) → step 6 returns `nil`; happy-path seam test.
- **AC4** (scoped to interactive/daemon path; agent-run path unchanged, #173) → all edits in `internal/supervisor`; `internal/agentrun/ptyrunner` untouched.
