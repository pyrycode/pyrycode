# Spec: WriteUserTurn delivers via Session.DeliverPrompt (ready-gate + commit-confirm + recovery)

**Ticket:** #594 — Phase 5 / Phase 1 foundation (T3). See [ADR 025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md).
**Size:** S (3 production files, 5 trivial ctx-threading sites, no edit fan-out, no branch overlap).
**Label:** `security-sensitive` — security-review pass appended at the end.

## Files to read first

Read these before touching code; they are the turn-1 data load.

- `internal/supervisor/supervisor.go:147-181` — current `WriteUserTurn` (the method to rewrite): validate → stamp cursor → `sess == nil → return nil` (the silent drop to kill) → `AttachInput`.
- `internal/supervisor/supervisor.go:193-243` — `setSession` / `WaitForPTY` / `sessReadyCh` choreography and the `sessMu`/`convMu` leaf-lock discipline. The cursor-stamp-before-write ordering and the lock-leaf rules are invariants you must preserve.
- `internal/supervisor/supervisor.go:356-509` — `runOnce`: how `setSession(sess)` / `setSession(nil)` bracket each iteration and why `setSession(nil)` runs **before** `sess.Close()`. Establishes the teardown ordering the new locking relies on.
- `internal/sessions/session.go:109-113` — `Session.WriteUserTurn` delegation (ctx-threading site #1).
- `internal/relay/handlers/send_message.go:38-118` — `TurnWriter` interface (ctx-threading site #2), the `Activate → WriteUserTurn` sequence, the error switch, and the **existing `server.binary_offline` retryable precedent** (the model for the new loud-failure reply).
- `internal/relay/handlers/send_message_test.go:27-53` — `stubTurnWriter` (ctx-threading site #3) and the handler tests to update.
- `internal/agentrun/ptyrunner/runner.go:382-432` — **reference implementation** of `WaitReady → DeliverPrompt`. Note: ptyrunner treats `DeliverResult.Committed` as *advisory* (ignores it, has a downstream JSONL/watchdog net). Our path is *stricter* — it has no downstream net, so it gates the return value on `Committed`.
- `internal/sessions/pool.go:125-137` — `SessionConfig` comment: the bootstrap session runs `--continue`, and `--session-id` is "deliberately NOT introduced" until Phase 1.1+. This is *why* `JSONLPath` is left empty (see Design § JSONLPath).
- `internal/dispatch/dispatch.go:543-586` — `Route`: a handler that returns a non-nil error is **logged at WARN, no reply synthesised, conn NOT torn down**. This is why "report failure to the phone" requires the handler to emit a wire reply, not just `return err`.
- tui-driver `pkg/tuidriver/deliver.go:29-110` — `DeliverOpts` / `DeliverResult` / `DeliverPrompt` contract. Key: `JSONLPath` is optional ("pass `""` to rely on the spinner signal alone"); `Committed == false` is the "may still be wedged" signal.
- tui-driver `pkg/tuidriver/ready.go` — `WaitReady(ctx) (Readiness, error)`: the ready-gate. Blocks until claude's TUI reaches idle; error is the ctx cause on cancel.
- tui-driver `pkg/tuidriver/keys.go:77-94` — `AttachInput` contract: "Returns the first non-nil PTY write error (e.g. the closed-file error after Close). **No panic.**" Confirms Session methods are teardown-safe (error, never crash) — the basis for the capture-then-unlock locking.
- `internal/supervisor/supervisor_test.go:551-701` — the existing `WriteUserTurn` tests (`HappyPath`, `CursorReadBack`, `UnknownIDDoesNotMutateCursor`, `NilValidatorSkips`, `NoPTYDrops`) that this change updates.

## Context

`Supervisor.WriteUserTurn` is the delivery path for untrusted, phone-originated turn payloads: relay `send_message` handler → `sessions.Session.WriteUserTurn` → `supervisor.WriteUserTurn` → live claude. Today it is fire-and-forget: it validates, stamps the cursor, then either **drops the turn silently** (`s.sess == nil → return nil`) or does a raw `Session.AttachInput(payload)` with no ready-gate and no commit-confirm. Both outcomes are reported back to the phone as a successful ack. A short, human-timed message typed while claude is busy or mid-restart can vanish into a false success.

T2 / #593 deliberately kept the raw `AttachInput` byte-for-byte when it migrated the supervisor onto a `tuidriver.Session`, deferring the reliable-delivery upgrade to this ticket. tui-driver `v1.2.0` now exports `Session.DeliverPrompt` (deliver + commit-confirm + corrupted-paste recovery, absorbed from ptyrunner at the v1.0.0 seal) and `Session.WaitReady` (the idle ready-gate). This ticket wires `WriteUserTurn` onto them and makes a non-confirmed delivery a **loud failure** instead of a silent ack.

## Design

### Shape of the rewrite

`WriteUserTurn` keeps its first two steps unchanged (validate, stamp cursor) and replaces the tail (`sess == nil → nil`; raw `AttachInput`) with: capture the live session, gate on readiness, deliver-and-confirm, and return `nil` **only** on a confirmed commit.

New signature (ctx threaded — see § Call-site cascade):

```
func (s *Supervisor) WriteUserTurn(ctx context.Context, id string, payload []byte) error
```

Contract sketch (full body is the developer's; this fixes the control flow, not the code):

- `ValidateConversation` runs first, result propagated verbatim, cursor **not** stamped on refusal — unchanged.
- Stamp `currentConvID = id` under `convMu` — unchanged, still **before** any delivery.
- Capture the session: `sessMu.Lock(); sess := s.sess; sessMu.Unlock()`. **Release the lock before delivering** (see § Concurrency).
- `sess == nil` → `return fmt.Errorf("supervisor: write user turn: %w", ErrNoLiveSession)`. *(Was: `return nil`.)*
- Otherwise `s.deliverFn(ctx, sess, payload)`; on error → `return fmt.Errorf("supervisor: write user turn: %w", err)`.
- Success → `return nil`.

### The delivery seam (`deliverFn`) — required for testability under the substrate seal

`WriteUserTurn` does not call `WaitReady`/`DeliverPrompt` directly. It calls through an unexported field:

```
deliverFn func(ctx context.Context, sess *tuidriver.Session, payload []byte) error
```

set once in `New` to the real method `(*Supervisor).deliverViaSession` and overridden in tests. This mirrors the existing `helperEnv` unexported-test-injection field; it is immutable post-`New` (production never mutates it), so `WriteUserTurn` reads it lock-free.

**Why a seam and not a direct call:** the happy path (`Committed == true → nil`) and the recovery branches are driven by claude's *screen* (idle prompt, thinking spinner, `[Pasted text]` chip). Those literals live inside tui-driver and **must not enter pyrycode** (substrate seal, `cmd/substrate-guard`, AC #5). A test cannot fake a claude screen in a pyrycode test file without smuggling a screen literal past the guard. The seam fakes one level *above* the screen-parsing layer — it returns the already-classified `error` / success — so every `WriteUserTurn` branch is unit-testable with zero claude-screen literals in pyrycode. This is the testing need that justifies the seam (per CODING-STYLE: "Don't define interfaces preemptively. Wait until you have… a testing need").

`deliverViaSession` contract sketch:

```
func (s *Supervisor) deliverViaSession(ctx, sess, payload) error:
    WaitReady(ctx)          → err: return fmt.Errorf("wait ready: %w", err)
    DeliverPrompt(ctx, DeliverOpts{Prompt: string(payload), Logger: s.log})
                            → err: return err   // PTY write failure, already prefixed by tui-driver
    !DeliverResult.Committed → return ErrTurnNotCommitted
    return nil
```

- **Ready-gate = `WaitReady`.** It blocks until claude's TUI is idle (input prompt visible, not thinking). This is the load-bearing piece: it is what makes `DeliverPrompt`'s "committed-but-slow" heuristic (no commit signal, no chip → assume committed) *trustworthy*. Without a prior idle gate, that heuristic would fire for an attached-but-not-ready claude (still booting MCP) and report a false `Committed == true` — exactly AC #2's silent-success case. With the gate, "no chip after delivery" genuinely means the idle claude accepted the turn. A blocking trust/network condition that prevents idle naturally surfaces as a `WaitReady` timeout → loud failure (no explicit trust/mcp/network policy branch needed here; that policy is ptyrunner's concern for a fresh spawn, not a long-lived supervised session — and adding it is unobserved-failure-mode defense).
- **Commit-confirm + recovery = `DeliverPrompt`.** Per its contract: deliver (Type vs paste method auto-selected by prompt shape), poll for the thinking-spinner commit signal up to `CommitTimeout`, and recover a corrupted bracketed paste by clear-and-re-deliver. Defaults (3 attempts × 3 s) are fine; pass `Logger: s.log` so its decision markers route through the supervisor logger (the marker *strings* live in tui-driver — no screen literal enters pyrycode).
- **Boundary conversion:** `payload []byte` → `DeliverOpts.Prompt string` via `string(payload)`.

### JSONLPath: pass `""` (evidence-based)

`DeliverOpts.JSONLPath` is an *optional* second commit signal (file-appearance alongside the spinner). We leave it empty and rely on the spinner signal alone, because:

1. The bootstrap session — the one the relay drives — spawns with `--continue`, **not** `claude --session-id <uuid>` (pool.go:125-137 defers `--session-id` to "Phase 1.1+"). The supervisor therefore holds **no stable claude session UUID** to build `SessionJSONLPath(home, cwd, sessionID)` from. (ptyrunner *can* use `JSONLPath` precisely because it spawns with a pinned `cfg.SessionID`.)
2. Even a spawn-time UUID would go stale: claude rotates its on-disk UUID on `/clear` (the rotation watcher tracks this in the `Pool`, not in the supervisor).
3. With the `WaitReady` idle gate in front, the spinner signal is sufficient and trustworthy; the JSONL file is redundant here.

Sourcing the UUID would mean cross-package plumbing (supervisor ← Pool's current bootstrap UUID) that is fragile and out of this ticket's scope. `JSONLPath: ""` is the simplest correct choice and is explicitly supported by the tui-driver contract.

### Call-site cascade (ctx threading) — 5 sites, all in files that already import `context`

1. `supervisor.WriteUserTurn(id, payload)` → `(ctx, id, payload)` — method rewrite.
2. `sessions.Session.WriteUserTurn(conversationID, payload)` → `(ctx, conversationID, payload)`; delegate `ctx` to the supervisor.
3. `handlers.TurnWriter.WriteUserTurn(conversationID, payload)` → `(ctx, conversationID, payload)` (interface decl, send_message.go:50).
4. `send_message.go:100` call site → pass a bounded `deliverCtx` (below).
5. `send_message_test.go:47` `stubTurnWriter.WriteUserTurn` → add the `ctx context.Context` parameter.

`cmd/pyry/assistant_turn.go`'s `cursorReader` interface only declares `CurrentConversation()` — **not** a ctx-threading site. No other callers exist (verified via codegraph impact + grep).

### Handler: report failure to the phone (not a false ack, not silence)

The `send_message` handler changes in two ways beyond ctx threading:

1. **Bound the delivery.** Mirror the existing Activate timeout: wrap the call in `deliverCtx, cancelDeliver := context.WithTimeout(ctx, sendMessageDeliverTimeout)` (new const; default `30 * time.Second`, matching `sendMessageActivateTimeout`). `WaitReady` blocks while claude is busy, so an unbounded ctx would hang the per-conn goroutine on a long claude turn; the bound turns "claude busy past budget" into a retryable failure instead.
2. **Map loud failure → retryable `server.binary_offline`.** `Route` does not reply on a handler error (dispatch.go:582-585), so a bare `return err` is *silent* to the phone — it does not satisfy "reports failure to the phone." Reuse the existing retryable code/message (no new protocol constant). The error switch becomes:

   - `err == nil` → ack (unchanged).
   - `errors.Is(err, conversations.ErrConversationNotFound)` → `conversation.not_found`, non-retryable (unchanged).
   - `errors.Is(err, context.Canceled) && ctx.Err() != nil` → `return err` (conn is closing — propagate for the per-conn unwind, no doomed wire reply). Mirrors the existing Activate-block check.
   - `default` → `replyError(…, protocol.CodeServerBinaryOffline, msgServerBinaryOffline, true)` *(was: `return err`)*. Every `WriteUserTurn` failure mode is transient (no live session, claude not idle within budget, wedged delivery, PTY closing) → retryable is correct.

The handler matches only error values it **already imports** (`context`, `conversations`); the supervisor's new sentinels fall into `default`. This keeps `handlers/` free of the `internal/supervisor` import (the boundary pinned in PROJECT-MEMORY).

> A deliver-timeout returns `context.DeadlineExceeded` (from `deliverCtx`), which is *not* `context.Canceled`, so it correctly lands in `default → binary_offline`. A parent-conn cancel returns `context.Canceled` with `ctx.Err() != nil`, so it propagates. The `WithTimeout` parent/child relationship gives this split for free.

## Concurrency model

**Capture-then-unlock.** The old code held `sessMu` across the (fast) `AttachInput` write. `WaitReady + DeliverPrompt` can run for seconds (idle-wait + up to 3 × 3 s commit retries); holding `sessMu` that long would block `runOnce`'s teardown `setSession(nil)`. So `WriteUserTurn` captures the `sess` pointer under `sessMu`, **releases the lock**, then delivers on the captured pointer — the same "capture the channel reference under `sessMu`, await it unlocked" pattern `WaitForPTY` already uses.

**Teardown safety without the lock.** Releasing the lock means a concurrent `runOnce` can `setSession(nil)` then `sess.Close()` while delivery is in flight on the captured pointer. This is safe and correct:

- tui-driver Session methods are teardown-safe by contract: `AttachInput`/`writeRaw` "Returns the first non-nil PTY write error (e.g. the closed-file error after Close). **No panic.**" (keys.go:77-94); `Snapshot()` reads an internal buffer (frozen, never a dangling fd). The #593 `Resize` EBADF defusal established the same property for the resize path.
- So a mid-flight `Close` makes `WaitReady`/`DeliverPrompt` return errors (or `WaitReady` simply never re-reaches idle on a frozen buffer and times out on ctx) → `WriteUserTurn` wraps and returns non-nil → **loud failure**. A session torn down mid-delivery *is* a failed delivery; reporting it as a failure (not a false ack) is the desired behavior.

This relaxes — but does not break — the #593 `setSession(nil)`-before-`Close()` ordering. That ordering previously guaranteed an in-flight `WriteUserTurn` saw `nil` and dropped. Now a racing `WriteUserTurn` may capture the pointer just before the clear and deliver against a closing session — but only into the teardown-safe error path, never a crash. Document this shift in the `WriteUserTurn`/`setSession` doc comments.

**Lock discipline unchanged.** `convMu` and `sessMu` remain leaf-only and are never held simultaneously (each is acquired and released independently). `deliverFn` is read lock-free (set-once in `New`). No new lock, no new ordering edge.

**Cursor survival across restarts unchanged.** `currentConvID` lives under `convMu` on the supervisor, independent of `sess`; child restarts (which re-key `sess`) never touch it. Preserved.

## Error handling

Two new exported sentinels in `internal/supervisor` (the package convention is to export error sentinels — cf. `ErrConversationNotFound` consumption, `ErrAttachUnavailable`, `ErrInvalidSessionID`):

- `ErrNoLiveSession` — no session registered when the turn arrives (the former silent-drop case).
- `ErrTurnNotCommitted` — `DeliverResult.Committed == false` after the bounded recovery.

`WaitReady` and `DeliverPrompt` errors are wrapped, not replaced (`wait ready: %w`, and tui-driver's own `tuidriver: write prompt: %w`), so `errors.Is(err, context.DeadlineExceeded)` / `context.Canceled` remain matchable through the chain.

**Wrap contract preserved.** Every non-nil return from `WriteUserTurn` carries the stable `"supervisor: write user turn:"` prefix — unchanged from today, now applied to the new failure modes too. The handler test that asserted a wrapped write error propagates with *no* wire reply (`TestSendMessage_WrappedError_PassesThroughNoWireReply`) is updated to assert the new `binary_offline` reply — that test encoded the old silent-propagate posture, which AC #2 deliberately changes; the *wrap prefix* (the item AC #3 protects) is untouched.

## Testing strategy

stdlib `testing`, table-driven, `-race`. The seam (`deliverFn`) makes the supervisor-side branches deterministic without a live claude or any claude-screen literal.

**Supervisor — new tests (RED → GREEN, AC #4):**

- *No live session (was silent success):* call `WriteUserTurn` with no session registered → expect non-nil, `errors.Is(err, ErrNoLiveSession)`, and the `"supervisor: write user turn:"` prefix. Assert the cursor *was* stamped (stamping precedes the session check). No spawn — fast.
- *Attached but not ready (was silent success):* register a non-nil session (`&tuidriver.Session{}`), override `deliverFn` to return a `wait ready: context.DeadlineExceeded` error → expect non-nil loud failure, `errors.Is(err, context.DeadlineExceeded)`.

**Supervisor — happy + branch coverage via the seam:**

- *Committed → nil:* `deliverFn` returns `nil` → `WriteUserTurn` returns `nil`.
- *Not committed → loud:* `deliverFn` returns `ErrTurnNotCommitted` → wrapped non-nil; `errors.Is(err, ErrTurnNotCommitted)`.
- *Deliver/PTY error → loud:* `deliverFn` returns a plain write error → wrapped non-nil.

**Supervisor — regression updates (AC #3), behaviour the tests assert is unchanged except the documented return-value flip:**

- `CursorReadBack`: cursor still reflects the most recent accepted id. The two no-session calls now return `ErrNoLiveSession` instead of `nil`; update the return-value assertion, keep the cursor assertions (the cursor is still stamped before the session check).
- `UnknownIDDoesNotMutateCursor`: the known-id call now returns `ErrNoLiveSession` (no session) while *still* stamping the cursor; the unknown-id ("ghost") call still returns the validator error and leaves the cursor untouched. Update the known-id return-value assertion; keep the non-mutation assertion (the load-bearing one).
- `NilValidatorSkips`: with a nil validator the cursor is stamped for any id; the no-session call now returns `ErrNoLiveSession`. Update the return-value assertion.
- `NoPTYDrops` → **invert** to `NoSessionFailsLoud`: the whole point of the test flips — assert `errors.Is(err, ErrNoLiveSession)` (no silent drop). This is the RED→GREEN anchor for the no-child case.
- `HappyPath`: the old assertion (payload reaches a `TestHelperProcess` child's stdin via `AttachInput`) is no longer how delivery works — a generic fake child never reaches claude-idle, so the real path correctly fails. Repurpose its spawn-and-deliver shape into a real-machinery *not-ready* assertion (spawn the fake child, call with a short `deliverCtx`, expect a loud failure because the fake child never renders claude-idle), **or** retire it in favour of the seam-driven committed test above. Either keeps `make check` green; the seam-driven test gives deterministic happy-path coverage.

**Handler tests (`send_message_test.go`):**

- Update `stubTurnWriter.WriteUserTurn` to the `(ctx, id, payload)` signature; existing call sites pass `context.Background()`.
- Invert `WrappedError_PassesThroughNoWireReply` → assert a `default`-class delivery error now produces a `server.binary_offline` **retryable** error envelope (no false ack, explicit failure to phone).
- Keep `Success_EmitsAck`, `ConversationNotFound`, `ActivateTimeout`, `MalformedPayload`, `HandlerCtxCanceled` — verify they still pass with the new signature (ConversationNotFound is matched before `default`; ctx-cancel still propagates).

**Real-claude integration** (`internal/e2e/realclaude/…`, build-tagged, NOT in `make check`): the genuine `WaitReady → DeliverPrompt → Committed` path against a live claude is the e2e harness's territory. Note as a follow-up; do not add a real-claude dependency to `make check`.

**`make check`** must be green including `cmd/substrate-guard`: no claude-screen literal (`Pasted text`, spinner glyph, idle-prompt marker) appears anywhere in pyrycode — the seam returns classified errors/success, and all screen knowledge stays inside tui-driver's `WaitReady`/`DeliverPrompt`.

## Open questions

- **Busy-claude policy.** `WaitReady` waits for idle, so a turn sent while claude is mid-turn blocks until claude returns to idle or `sendMessageDeliverTimeout` elapses (→ retryable `binary_offline`). This spec treats "deliver while busy" as out of scope; if product wants queue-behind-current-turn semantics, that is a separate ticket. `sendMessageDeliverTimeout = 30s` is a tuning knob, not a contract.
- **Shared-session multi-writer races.** On a foreground/operator-attached session, the operator may type concurrently. A spinner raised by the operator's activity between `WaitReady` returning idle and `DeliverPrompt`'s poll could read as our turn's commit (false positive). Inherent to screen-based commit detection on a shared interactive head; out of scope for the silent-drop fix.
- **Finer-grained wire codes.** All loud failures map to `server.binary_offline` (retryable). If the phone later needs to distinguish "claude busy" from "session torn down," that needs a neutral sentinel package (to keep `handlers/` free of the supervisor import) and a new protocol code — deferred until an observed need.

## Security review

**Verdict:** PASS

**Findings:**

- **[1 Trust boundaries]** No MUST FIX. The untrusted→trusted boundary is unchanged and explicit: phone `p.Text` → `[]byte(p.Text)` → `WriteUserTurn`'s `payload` → `DeliverOpts.Prompt`, delivered verbatim to claude's PTY. This ticket adds *no new parsing* of the payload — the only transformation is `string(payload)`, and the bytes still reach claude untouched (claude's own permission model governs their effect, per `AttachInput`'s SECURITY note and ADR 025). The new control-flow reads only tui-driver-internal screen state (idle/spinner/chip), never the payload, to decide commit — so payload content cannot steer the ready-gate or recovery logic.
- **[2 Tokens/secrets]** N/A — no tokens, secrets, or credentials are created, stored, compared, or logged on this path.
- **[3 File operations]** No findings. We deliberately do **not** construct a filesystem path from any session-derived value (`JSONLPath: ""`); the path-traversal / TOCTOU surface that `SessionJSONLPath(home, cwd, sessionID)` would introduce is avoided entirely. (Had we sourced a UUID, it would need `uuid.Parse`-shape validation before joining — moot here.)
- **[4 Subprocess]** N/A — no new `exec.Command`; claude is already spawned by `runOnce`. The payload is delivered as PTY *input*, never as a process argument or shell string, so there is no argument-injection or `sh -c` surface.
- **[5 Crypto]** N/A — no cryptographic primitives on this path.
- **[6 Network & I/O]** No MUST FIX. Payload size is already capped upstream by the transport's 1 MiB WS read ceiling (send_message.go SECURITY note) — unchanged. New bound added: `sendMessageDeliverTimeout` caps how long an untrusted message can occupy the per-conn goroutine (`WaitReady` would otherwise block indefinitely on a busy/wedged claude). DeliverPrompt's own retry loop is internally bounded (3 × 3 s). No unbounded read or wait is introduced. **DoS check:** a hostile phone spamming `send_message` cannot amplify resource use — `Route` does not tear down the conn on handler error, delivery is serialized per conn, and each attempt is time-bounded; the blast radius is one bounded delivery attempt per inbound frame, same as today.
- **[7 Error messages / logs]** No MUST FIX. The payload is never logged (existing handler discipline preserved; the new failure path logs no payload). New wire replies reuse the static `msgServerBinaryOffline` string — no internal state, path, or payload byte is echoed to the phone. `WaitReady`/`DeliverPrompt` decision markers route to `s.log` (operator-side, structured) and contain no payload. Confirm during implementation that no new log line interpolates `payload`/`id` beyond the existing `conversation_id`/`message_id` opaque ids.
- **[8 Concurrency]** No MUST FIX. Lock discipline is preserved: `convMu`/`sessMu` stay leaf-only and are never co-held; `deliverFn` is set-once in `New` and read lock-free. The new capture-then-unlock pattern is shown safe in § Concurrency — a mid-delivery `Close` lands in the teardown-safe PTY error path (no panic, no dangling fd, per the `AttachInput`/#593-`Resize` contracts), surfacing as a loud failure rather than a crash or a false ack. No goroutine is spawned by this change. Shutdown mid-delivery → bounded loud failure, no partial on-disk state (nothing is written to disk on this path).
- **[9 Threat model]** Aligned with ADR 025 (mobile remote head): authenticating *who* may send is the relay auth layer's job (unchanged); this ticket governs *reliable delivery* of an already-authenticated turn. The relevant threat it closes is the **false-ack / silent-drop** failure mode — a turn reported as delivered that never reached claude. Out of scope (named): turn-content authorization (claude's permission model owns it) and multi-writer arbitration on a shared head (Open Questions).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-07
