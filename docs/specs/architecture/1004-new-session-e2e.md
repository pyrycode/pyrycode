# Spec — #1004 test(e2e): `new_session` over the v2 wire — rotation observable on disk

**Size:** S. One production-ish file modified (`internal/e2e/internal/fakeclaude/main.go` — a new env-gated mode, purely additive, byte-identical when unset), plus one new e2e `*_test.go` and one small fakeclaude detector `*_test.go`. Zero new exported types, zero consumer cascade, ~340 LOC total. **Not security-sensitive** (per PO and confirmed: a phone-driven `new_session` routes a *fixed* `/clear` keystroke through the already-shipped, already-wired `SessionStarter` seam — no attacker-content injection, no new path, no crypto; the only observable is an on-disk registry id rotation).

## Context

The v2 control verbs handled inside `V2SessionManager.dispatchAppFrame` have method-level unit tests but uneven fake-daemon e2e coverage. `new_session` (the phone-driven `/clear`-and-rotate verb, #831) is one of four verbs from the 2026-07-15 review gap; siblings `interrupt`, `modal_cancel`, `dequeue_message` now have e2e. This ticket adds the `new_session` slice only (split from the oversized four-verb #962).

`handleNewSession` (`internal/relay/v2session.go:2621`) is interactive-gated and fire-and-forget: it calls `SessionStarter.StartNewSession()`, which types `/clear` into the supervised session. `/clear` rotates claude's session UUID; pyry's rotation watcher follows the most-recently-modified JSONL and updates the registry id on disk (`docs/lessons.md` § "Claude session storage on disk"). There is **no reply and no broadcast** — the only observable is the on-disk registry id change.

**Two facts drove the design and MUST be understood before implementing (both verified against merged code):**

1. **`SessionStarter` is already wired in production.** `cmd/pyry/relay.go:488` sets `SessionStarter: w.sup` (`*supervisor.Supervisor` satisfies the interface via `StartNewSession`). So the daemon `StartRotationWithRelay` spawns has a *live, non-nil* `SessionStarter` — `handleNewSession` is NOT inert. No wiring prerequisite; this ticket is pure test coverage over a shipped path.

2. **fakeclaude does NOT currently rotate on `/clear`.** The ticket's Technical Notes assume "a fakeclaude that actually rotates its JSONL on `/clear`", but the real stand-in rotates its JSONL **only on a file trigger** (`PYRY_FAKE_CLAUDE_TRIGGER`, see `main.go:337-346`), never on a keystroke. For the registry-rotation assertion to be *caused by* the `new_session` frame (and not vacuous), fakeclaude must rotate in response to the `/clear` keystroke. **The developer adds a new keystroke-triggered rotation mode** — a small, purely-additive change that mirrors the existing `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` mode exactly.

## Files to read first

- `internal/e2e/relay_v2_interrupt_test.go:68-142` — **the v2 bring-up template.** The exact pair → decode pubkey → compute `sessionsDir` → pre-create `<initialUUID>.jsonl` → `fakerelay.New` → `StartRotationWithRelay(..., fr.URL()+"/v2/server", "PYRY_MOBILE_V2=1", <modes>)` → `readPersistedServerID` → `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeToOpenDaemonInteractive` skeleton. Clone this bring-up; drop everything after it (the send_message/turn/kicker machinery is NOT needed — new_session needs no bound conversation, no TUI mode, no cursor stamp).
- `internal/e2e/relay_v2_interrupt_test.go:323-362` — the stdinLog keystroke oracle (bounded poll + `hasBareESC`). Adapt the *shape* for the direct `/clear` oracle (scan the on-disk stdin log for the `/clear` bytes).
- `internal/e2e/rotation_test.go:44-173` — **the on-disk rotation assertion shape (AC-2).** Reuse verbatim (same package, same `e2e` build tag): `waitForBootstrapID`, `waitForBootstrapIDChange`, `readBootstrap`, `readBootstrapIfPresent`, `uuidStemPattern`, `claudeSessionsDir`, `encodeWorkdir`. Do NOT redefine any of them (duplicate-symbol compile error).
- `internal/e2e/harness.go:309-366` — `StartRotationWithRelay` signature + the env vars it sets and its `extraEnv ...string` tail (where `PYRY_FAKE_CLAUDE_CLEAR_ROTATES=1` goes). `trigger` "need not refer to an existing file" — point it at a never-created path so the file-trigger rotation stays dormant and only `/clear` rotates.
- `internal/e2e/internal/fakeclaude/main.go:91-113` — the `envEscEndsTurn` doc block: **the exact pattern the new mode mirrors** (raw/verbatim keystroke detection → signal main goroutine → one-shot file mutation).
- `internal/e2e/internal/fakeclaude/main.go:279-402` — `main()` loop + the `rotated`/`escEnded` one-shot gates + `startStdinReader` invocation condition; `:616-642` `containsBareESC`; `:644-657` `openSession`; `:659-723` `startStdinReader` (the stdin-reader + `escPending`/`turnPending` signal-only discipline). These are the exact seams the new mode extends.
- `internal/e2e/internal/fakeclaude/esc_detect_test.go` — the untagged table-driven detector test to mirror for the new `/clear` detector.
- `internal/relay/v2session.go:2596-2637` — `handleNewSession`: interactive gate → nil-`SessionStarter` guard → best-effort `StartNewSession()`. Confirms no reply / no broadcast (on-disk observable only) and the AC-3 non-interactive inert path (`if !s.interactive { return }`).
- `internal/relay/v2session.go:1801-1832` — `dispatchAppFrame` with `case protocol.TypeNewSession: m.handleNewSession(s)` (confirms the frame is intercepted pre-`dispatch.Route`).
- `internal/supervisor/modal.go:73-144` — `StartNewSession` → `sendModalKeystroke`'s `keyStartNewSession` arm: `sess.ClearInputLine()` (Ctrl-U, 0x15) then `sess.TypePrompt("/clear")` byte-by-byte + trailing `\r`. **These are the exact bytes fakeclaude must detect.**
- `cmd/pyry/relay.go:483-494` — production wiring (`SessionStarter: w.sup`). Confirms fact 1 above: the observable path is live in the spawned daemon.
- `internal/protocol/codes.go:422` — `TypeNewSession = "new_session"`, phone→binary inbound v2 control, no payload.
- `docs/lessons.md:50-56` (`/clear` rotates the UUID; the registry self-heals by following the most-recently-modified JSONL) and `docs/lessons.md:256-261` (the `--session-id` stand-in gotcha — why this **bootstrap-only** test is safe: `Pool.New`'s bootstrap path uses `tpl.ClaudeArgs` verbatim with no `--session-id` append, and the rotation is in-session, so fakeclaude never sees `--session-id`).

## Design

Two deliverables, no new shared infra (per the ticket's "ride the existing harness").

### A. fakeclaude clear-rotate mode (`internal/e2e/internal/fakeclaude/main.go`)

A new env-gated mode, `PYRY_FAKE_CLAUDE_CLEAR_ROTATES`, that rotates the live JSONL once when the `/clear` slash-command bytes are observed on stdin. Purely additive: when the env is unset the binary is byte-identical to today (the same "Default off" discipline every prior mode documents). It mirrors `envEscEndsTurn` (`main.go:91-113`, `:373-382`, `:673-704`) end-to-end:

- **Env const** `envClearRotates = "PYRY_FAKE_CLAUDE_CLEAR_ROTATES"`; parsed in `main()` as `clearRotates := os.Getenv(envClearRotates) != ""`.
- **Start the stdin reader when the mode is on**: add `|| clearRotates` to the `startStdinReader` gating condition (`main.go:311`). Pass a new `clearRotates bool` param to `startStdinReader` (the sole caller updates).
- **Detection (signal-only, single-writer-of-`f`)**: the reader goroutine accumulates the bytes it reads (the `/clear` command may span reads under raw discipline, and under the default canonical discipline arrives as a single `/clear\n` line once the trailing `\r` commits) and, when the accumulated buffer `containsClearCommand`, sets a new `var clearRotatePending atomic.Bool` — exactly like `escPending`. The reader NEVER touches `f`.
- **Rotation (main goroutine, one-shot)**: in the main poll loop, alongside the `escEnded` block, add `if clearRotates && !rotated && clearRotatePending.Swap(false) { <rotate> }`. **Reuse the existing `rotated` one-shot gate** so a test can never double-rotate and the file-trigger + clear-rotate paths are mutually exclusive in practice. The `<rotate>` is the same close-old-then-open-new-uuid the file-trigger branch performs (`main.go:340-343`): `f.Close()`; `f = openSession(dir, uuidV4())`. Optionally extract a `rotateSession(f *os.File, dir string) *os.File` helper shared by both branches (two call sites) — developer's call; duplicating two lines is also acceptable per the file's tolerance for small duplication.
- **Detector as a pure function** for a table test: `containsClearCommand(buf []byte) bool { return bytes.Contains(buf, []byte("/clear")) }`. In this mode the only stdin is the `/clear` keystroke(s), so a substring match cannot false-positive.
- **Doc comment**: add an `envClearRotates` block to the file header in the same style as the `envEscEndsTurn` block, stating: raw/canonical-agnostic detection, one-shot (a second `/clear` is inert, matching claude's own behaviour), signal-only from the reader, default off / byte-identical when unset, and that it drives the #1004 rotation e2e.

*Raw vs. canonical:* the mode does **not** need `enterRawMode()`. The `/clear` keystroke ends with `\r`; under the PTY's default canonical discipline the line commits as `/clear\n` in one read (the leading Ctrl-U from `ClearInputLine` is consumed by the line discipline as VKILL on an empty line — a no-op), so a single-read scan already matches. Accumulating across reads is the robust superset (handles a future raw-mode caller and any chunking) and is what the spec recommends. Do not set `enterRawMode()` for this mode — it would only matter if paired with modal/esc mode, which this test does not do.

### B. The e2e tests (`internal/e2e/relay_v2_new_session_test.go`, build tag `//go:build e2e`, package `e2e`)

Two test functions. Every helper they need already exists in-package under the `e2e` build tag — reuse, never redefine.

#### Data flow under test (happy path)

```
phone new_session frame → Noise decrypt → dispatchAppFrame intercept →
  handleNewSession (interactive ✓) → SessionStarter.StartNewSession() →
    ClearInputLine (Ctrl-U) + TypePrompt("/clear") + "\r" → fakeclaude stdin →
  clear-rotate mode: close old <initialUUID>.jsonl, open fresh <uuid>.jsonl →
  rotation watcher follows most-recent JSONL → RotateID → registry id := <uuid> (on disk)
```

Because the file trigger points at a never-created path, **the ONLY thing that rotates the JSONL in this test is the `/clear` keystroke** — so an observed registry id change ⟺ the `new_session` frame drove `/clear` to the child (the structural-causality guard, mirroring the interrupt test's "fakeclaude's ESC handler is the only source of a turn_end").

#### Test 1 — `TestRelayV2_NewSessionRotatesOnDisk` (AC-1 + AC-2)

Bring-up mirrors `relay_v2_interrupt_test.go:77-142`, minus the turn machinery:

1. `home := shortHome(t)`; pair one device (`RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")`); `decodePairPayload`; decode `ServerStaticPubkey`.
2. `sessionsDir := claudeSessionsDir(home)`; `os.MkdirAll`; pre-create `<initialUUID>.jsonl` with `{}\n` **before** the daemon starts (so `reconcileBootstrapOnNew` adopts it and the bootstrap id == `initialUUID`).
3. `neverCreated := filepath.Join(t.TempDir(), "rotate.trigger.never-created")`; `stdinLog := filepath.Join(t.TempDir(), "fakeclaude-stdin.log")`.
4. `fr := fakerelay.New(relayTestLogger())`; `h := StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverCreated, stdinLog, fr.URL()+"/v2/server", "PYRY_MOBILE_V2=1", "PYRY_FAKE_CLAUDE_CLEAR_ROTATES=1")`.
5. `serverID := readPersistedServerID(t, home)`; `waitBinaryHello(t, fr, serverID)`.
6. `phoneA, _ := fakephone.Dial(...)`; `sendA, _ := driveHandshakeToOpenDaemonInteractive(t, phoneA, pubKey, payloadA.Token)` (the `recv` half is unused — new_session has no reply).

Assertions (ordered, each with its own `t.Fatal` naming its failure mode — the sibling non-vacuity discipline):

7. **Baseline (precondition).** `waitForBootstrapID(t, regPath, initialUUID, 5*time.Second)` — confirms the daemon reconciled the bootstrap id to `initialUUID` *before* any rotation, so the "id changed" assertion below is non-vacuous.
8. **Drive new_session until it rotates (AC-1).** Send a sealed `new_session` frame — `Envelope{ID: <n>, Type: protocol.TypeNewSession, TS: now}` (no payload), `sendA.Encrypt`, `sendNoiseMsg(t, phoneA, cipher)` — inside a bounded send-poll loop: send, then check the registry; re-send every ~250 ms until the id rotates or a ~10 s deadline elapses. **Rationale (determinism, not speculative defense):** `new_session` is fire-and-forget and drops silently (`ErrNoLiveSession`, Warn-logged) if the tui-driver session is not yet attached to the supervisor at the instant the frame is processed. The sibling interrupt test avoids this by establishing a confirmed round-trip (send_message + ack) before its keystroke; here the cheaper equivalent is a bounded re-send — fakeclaude's clear-rotate is one-shot, so once the session is live the first `/clear` rotates and every later frame is inert. Use fresh envelope IDs per send (the `sendA` CipherState nonce increments per `Encrypt`; the daemon's recv nonce follows). Single-threaded: the main goroutine is the only writer of the phone conn (no reply to read).
9. **Assert the rotation on disk (AC-2).** `post := waitForBootstrapIDChange(t, regPath, initialUUID, ...)` (the loop in step 8 IS this wait — structure it so the loop's success condition is "id changed away from `initialUUID`"). Then assert `uuidStemPattern.MatchString(post.ID)` and `post.LastActiveAt.After(pre.LastActiveAt)` — the exact shape `rotation_test.go:89-96` uses.
10. **Direct keystroke oracle (belt-and-suspenders, different fabric).** Bounded-poll `stdinLog` (~2 s, mirroring `interrupt_test.go:331-343`) and assert it contains the `/clear` bytes — fakeclaude's own byte record that the frame routed `/clear`, independent of the daemon's registry write. `t.Fatalf` naming "new_session frame never routed /clear to the child" if absent.
11. *(Optional, cheap)* stable-state check à la `rotation_test.go:98-106`: `time.Sleep(200ms)` then assert the id did not revert.

#### Test 2 — `TestRelayV2_NewSessionNonInteractiveInert` (AC-3)

Same bring-up as Test 1 (its own daemon, fresh `initialUUID`, `PYRY_FAKE_CLAUDE_CLEAR_ROTATES=1`), except the phone handshake uses the **non-interactive** helper `driveHandshakeToOpenDaemon` (`relay_v2_daemon_test.go:44`).

1. `waitForBootstrapID(t, regPath, initialUUID, ...)` — baseline.
2. Send one `new_session` frame on the non-interactive conn.
3. Assert **inert** over a bounded window (~1.5–2 s): the registry id stays `initialUUID` (`readBootstrap`), AND `stdinLog` never gains the `/clear` bytes. The stdin-log-absence is the direct proof the interactive gate short-circuited before `StartNewSession` (the keystroke was never routed); the registry-unchanged is the downstream proof.

**Non-vacuity of the negative:** Test 2 alone cannot prove "nothing happened" is meaningful; its non-vacuity comes from Test 1 proving the *same* harness + mode *does* rotate for an interactive conn. A broken interactive gate (non-interactive also rotates) would surface within the window as a `/clear` in the stdin log and an id change — so the 2 s window and the stdin-log oracle catch a real regression, not just idle time. Document this pairing in Test 2's doc comment. AC-3 is cheap here (the non-interactive helper exists); implement it. If the second daemon spawn proves too slow under `-race`, folding a non-interactive probe into Test 1's daemon before the interactive drive is an acceptable fallback, but separate functions are the clean default.

### C. fakeclaude detector test (`internal/e2e/internal/fakeclaude/*_test.go`)

Add an **untagged** (no `//go:build e2e`) table-driven test for `containsClearCommand`, mirroring `esc_detect_test.go`'s `TestContainsBareESC`: cases for the full `/clear\n` line, `/clear` split across two buffers when the caller accumulates (i.e. test the accumulation contract at the call boundary if the detector takes the accumulated buffer), plain ASCII with no command (false), empty (false). Keep it to the pure function; the live rotation is exercised by the e2e.

## Concurrency model

None new in the e2e. The tests drive the daemon subprocess over one Noise session; new_session has no reply, so the main test goroutine is the sole writer of the phone conn and reads only registry/stdin-log **files** (never the conn concurrently). fakeclaude's clear-rotate detection reuses the existing single-stdin-reader + `atomic.Bool` signal + main-goroutine-performs-the-mutation discipline (single-writer-of-`f`), identical to `escPending`/`turnPending`. No goroutines, channels, or locks are added.

## Error handling

- Harness/precondition failures (`shortHome`, pairing, `Dial`, `driveHandshake*`, `waitForBootstrapID`) already `t.Fatal` with context — reused as-is.
- The send-poll loop `t.Fatal`s on its deadline naming the specific failure ("registry id never rotated away from `initialUUID` after new_session — the `/clear` keystroke did not drive a rotation; the frame may have been dropped on a detached session or the clear-rotate mode is not wired").
- fakeclaude stays best-effort/silent-error (its established posture); a persistently-failing rotation surfaces as the e2e timeout, never a false pass.

## Testing strategy

- The files ARE the tests. `make e2e` (runs `-tags e2e -race`) must be green — the ticket deliverable (AC-4).
- Iterate with `go test -tags e2e -race -run 'TestRelayV2_NewSession' -count=1 ./internal/e2e/` and the untagged detector with `go test -race -run TestContainsClearCommand ./internal/e2e/internal/fakeclaude/`.
- **Non-vacuity is the acceptance bar** (test-only PR): baseline id == `initialUUID` → rotation observed (id changes to a valid UUIDv4 stem, `last_active_at` advances) → `/clear` present in the stdin log; Test 2 isolates the interactive gate. Each precondition `t.Fatal`s loudly if its predecessor didn't happen.
- Sanity-check the additive fakeclaude change did not perturb siblings: `go test -tags e2e -race -run 'TestRelayV2_Interrupt|TestE2E_RotationWatcher' -count=1 ./internal/e2e/` should stay green (the new mode is off for them).

## Open questions

- **Session-attach timing.** The send-poll loop (step 8) makes the test deterministic regardless of whether `setSession` precedes control-socket readiness. If the developer confirms (via `internal/supervisor/supervisor.go` `Run`/`setSession` ordering) that the session is always attached by the time the handshake completes, a single blind send + a generous `waitForBootstrapIDChange` deadline suffices and the loop can collapse to one send. The loop is the safe default; simplifying it is optional and not blocking.
- **Envelope IDs.** `new_session` has no ack, so request IDs are cosmetic — reuse the sibling convention (small arbitrary `uint64`s), bumping per re-send. Not blocking.
- **`rotateSession` extraction vs. two-line duplication** in fakeclaude — developer's call; both satisfy CODING-STYLE. Not blocking.
