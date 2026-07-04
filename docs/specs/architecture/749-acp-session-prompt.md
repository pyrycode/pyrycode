# Spec #749 — `session/prompt`: content blocks → `DeliverPrompt`, call held open

**Ticket:** #749 — feat(acp): prompt path — session/prompt content blocks to DeliverPrompt
**Size:** S (confirmed — 2 production files, 0 new exported types, ~460 LOC total; see § Sizing)
**Security-sensitive:** **Yes** (label present). Host-sourced prompt text reaches claude here. Security pass at § Security review (verdict: PASS).
**Package:** `cmd/pyry` (package `main`) only. Consumes existing `internal/acp` (#765 deferral), `internal/sessions` (#761 pool), `internal/supervisor` seams. No cross-package coordination, no new package.

---

## Files to read first

- `cmd/pyry/acp.go:106-130` — `serveACPWithPool` register closure + pool teardown. The **one edit site**: construct the hold store here and add the `session/prompt` registration alongside the other four `t.Register(...)` calls. Extract the closure-wiring pattern.
- `cmd/pyry/acp.go:162-210` — `decodeSessionID` (reuse verbatim — the empty/missing-id rejection that dodges the `Lookup("")` bootstrap trap) + `loadSessionHandler` (mirror its `errors.Is(err, sessions.ErrSessionNotFound)` → `CodeInvalidParams "unknown session"` mapping).
- `cmd/pyry/acp.go:212-255` — `resolveCancelTarget` + `interrupter` + `cancelSessionHandler`. The **exact pattern this spec mirrors**: a consumer-defined 1-method interface, an injected `resolve` closure wired at the composition root, and the nil-interface trap (`pool.Lookup` error must return a true `nil` interface, not a typed nil pointer).
- `internal/acp/responder.go:1-89` — the #765 deferral primitive: `ErrDeferred`, `ResponderFrom(ctx) *Responder`, `Responder.Reply(result)` / `Responder.ReplyError(*Error)` (both once-guarded via `done` CAS → `ErrAlreadyResolved`). This is the whole "hold the call open" mechanism.
- `internal/acp/acp.go:221-262` — `dispatchRequest`. Shows how returning `ErrDeferred` suppresses the synchronous write, and that the `*Responder` is injected into the handler's `ctx` (so the handler is reached only for `method+id` requests — a `session/prompt` sent as a notification gets **no** responder).
- `internal/acp/jsonrpc.go:11-40` — `Code*` constants + `NewError(code, message)` + `Error`. Codes used here: `CodeInvalidParams` (bad content / unknown session), `CodeInvalidRequest` (second concurrent prompt), `CodeInternalError` (delivery failure, generic message).
- `internal/sessions/pool.go:731-745` — `Pool.Lookup(id) (*Session, error)`: a **non-empty** unknown id → `ErrSessionNotFound`; an **empty** id → the parked bootstrap (why `decodeSessionID` must reject empty *before* Lookup).
- `internal/sessions/session.go:119-132` — `Session.WriteUserTurn(ctx, conversationID string, payload []byte) error` (delegates to `Supervisor.WriteUserTurn`). `*sessions.Session` is the production `promptDeliverer`.
- `docs/knowledge/architecture/system-overview.md` § `Supervisor.WriteUserTurn` (#312/#594/#668) — the delivery contract: blocks for **WaitReady + commit-confirm (~10 s)**, returns non-nil on failure (`ErrNoLiveSession` / `ErrTurnNotCommitted` / wrapped deadline). Two facts drive this spec: (a) it **must not** run on the read loop; (b) with `ValidateConversation == nil` (the ACP pool) the `conversationID` arg is not load-bearing — pass the session id.
- `cmd/pyry/acp_handshake.go:51-58` — `promptCapabilities` (image/audio/embeddedContext all `false`). The advertised text-only contract that justifies **rejecting** non-text content blocks.
- `cmd/pyry/acp_turn_stream.go:36-52` — `acpTurnStream.onTurnEnd func(reason string)`. The T7 (#751) join seam: T7 will wire `onTurnEnd` for a session to `holds.end(sessionID, reason)`. This pins the store's `end(sessionID, reason string)` signature. **Not wired by this ticket** — the store's `end` is exercised only by tests here (the placeholder-end path).
- `cmd/pyry/inbound_deliver_test.go:22-59` — `gatingWriter`, a fake `TurnWriter` that records delivered payloads and gates commit on a channel. Mirror it for the fake `promptDeliverer` (recorder + optional error injection).
- `cmd/pyry/acp_test.go:277-400` — `serveACP(ctx, r, w, logger, register)` (the pool-free serve entry — the prompt tests reuse it with a **custom register**, no fake-claude pool needed) + `newFakeClaudePool` / `acpHarness.send/read/shutdown` (the pipe-driven live-transport harness pattern to extend).
- `tui-driver@v1.3.0/pkg/tuidriver/deliver.go` (doc header, lines 68-80) — `DeliverPrompt` uses **bracketed paste** for multi-line/long prompts, so a `\n` in the payload does not submit prematurely. This is why joining text blocks with `\n` is safe and matches the mobile multi-line-message path.

---

## Context

**Epic #600 — `pyry acp` as a thin adapter over the shared remote-head core.** `session/prompt` is the main call: the host sends a user turn, pyry drives claude, and the call returns a `stopReason` when the turn ends. An ACP turn is **one** `session/prompt` request that streams `session/update` notifications and then resolves. So the handler must do two things that a normal request handler cannot: deliver the prompt into the supervised interactive claude, **and hold its own JSON-RPC call open** for the duration of the turn.

The two prerequisites are both merged:

- **#761** landed the embedded `*sessions.Pool` + `session/new` / `session/load`. This handler resolves its target with `pool.Lookup(sessionId)`.
- **#765** landed the transport deferral primitive (`ErrDeferred` + context-injected `*Responder`). Before it, the transport wrote a response the instant a handler returned — "hold open" was unbuildable without either blocking the read loop (breaks the second-prompt reject and `session/cancel`) or resolving early (not held). This ticket **consumes** that primitive.

**Non-goals / boundaries (owned by siblings):**
- Outbound `session/update` streaming is T6 (#750, `acpTurnStream`) — already built, still unwired.
- Resolving the held call on real `TurnEnd` with the mapped `stopReason` is T7 (#751). This ticket exposes the `holds.end(sessionID, reason)` seam T7 will drive; until T7 lands, only tests call it (the "placeholder end" path). In a live `pyry acp` process with only #749 wired, a delivered prompt holds and stays held until host disconnect — a safe, incomplete interim, consistent with the rest of the epic building up unwired.

---

## Design

All new code lands in one new file `cmd/pyry/acp_prompt.go` (package `main`), plus a ~8-line edit to `serveACPWithPool` in `cmd/pyry/acp.go`.

### Data flow

```
host: {"id":N,"method":"session/prompt","params":{"sessionId":S,"prompt":[blocks]}}
  │
  ▼ read loop (Serve goroutine — single-threaded, race-free)
promptHandler:
  1. id  ← decodeSessionID(params)        empty/missing → CodeInvalidParams (pre-Lookup)
  2. buf ← mapPromptContent(params)        non-text / empty → CodeInvalidParams
  3. dlv ← resolve(id) = pool.Lookup(id)   ErrSessionNotFound → CodeInvalidParams "unknown session"
  4. resp ← acp.ResponderFrom(ctx)         nil (notification misuse) → plain error, no hold
  5. holds.begin(S, resp)                  already in flight → CodeInvalidRequest (no hold, no spawn)
  6. go deliverPrompt(ctx, holds, id, dlv, buf, logger)
  7. return ErrDeferred                     → transport writes NOTHING; read loop scans next frame
  │
  ├─▼ delivery goroutine (bounded ≤ promptDeliverTimeout; cancelled on Serve shutdown)
  │   dlv.WriteUserTurn(timeoutCtx, S, buf)   WaitReady + commit-confirm (~10 s)
  │     ├─ nil  → turn committed & running; LEAVE the call held (do nothing)
  │     └─ err  → no turn started; holds.fail(S, CodeInternalError) — resolve + free slot, log sentinel
  │
  └─▼ resolver goroutine (T7 turnbridge Run / test placeholder)
      holds.end(S, stopReason) → resp.Reply({stopReason}) — one frame, echoes id N; free slot
```

### New surface (all `cmd/pyry`, package `main`, unexported)

```go
// promptDeliverer is the 1-method delivery seam (accept-interfaces-at-consumer,
// mirrors `interrupter`). *sessions.Session satisfies it via WriteUserTurn.
type promptDeliverer interface {
    WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
}

// promptResult is the session/prompt success payload: {"stopReason": "..."}.
type promptResult struct { StopReason string `json:"stopReason"` }

// promptHolds is the per-session in-flight registry: at most one held prompt call
// per session id. Leaf-locked; the sole owner of held-call resolution.
type promptHolds struct { /* mu sync.Mutex; held map[string]*acp.Responder; logger *slog.Logger */ }

func newPromptHolds(logger *slog.Logger) *promptHolds
func (h *promptHolds) begin(sessionID string, resp *acp.Responder) bool         // false if one already in flight
func (h *promptHolds) end(sessionID, stopReason string)                          // T7 seam: resp.Reply({stopReason}); free
func (h *promptHolds) fail(sessionID string, e *acp.Error)                       // delivery failed: resp.ReplyError(e); free

func mapPromptContent(params json.RawMessage) ([]byte, *acp.Error)               // content blocks → payload bytes
func promptHandler(holds *promptHolds, resolve func(sessions.SessionID) (promptDeliverer, error), logger *slog.Logger) acp.Handler

const promptDeliverTimeout = 15 * time.Second   // > supervisor's ~10s so its own error surfaces first
```

`deliverPrompt(ctx, holds, id, dlv, buf, logger)` is the goroutine body (package-level func or closure): derive `context.WithTimeout(ctx, promptDeliverTimeout)`, call `dlv.WriteUserTurn(tctx, string(id), buf)`, and on a non-nil error call `holds.fail(string(id), acp.NewError(acp.CodeInternalError, "prompt delivery failed"))` after logging the sentinel at Warn (never the payload). On success it returns; the call stays held.

### `promptHolds` semantics (contract, not implementation)

- `begin` — under `mu`: if `held[sessionID]` present, return `false` (caller rejects, no state change); else store `resp`, return `true`. Called inline on the read loop, so the check-and-insert is atomic against itself; `mu` additionally guards `end`/`fail` from other goroutines.
- `end` / `fail` — under `mu`: look up + delete the entry, capturing `resp`; **release `mu` before** calling `resp.Reply` / `resp.ReplyError` (those take the transport's `writeMu`, so resolving under `mu` would nest `mu → writeMu`; capture-then-resolve keeps `mu` a pure leaf). A missing entry is a **no-op** (idempotent) — so `end` is safe for T7 to call even if `fail` already fired, and vice-versa.
- Exactly-one-frame across every race (double-`end`, `end` vs `fail`, resolve-after-teardown) is guaranteed by the `Responder.done` CAS in #765 — `promptHolds` adds the "free the slot" bookkeeping, not a second frame-guard.

### `mapPromptContent` — content-block → payload (contract)

Decode `params` into `{ prompt []struct{ Type, Text string } }`. Then:
- Iterate blocks in order. `Type == "text"` → append `Text` to a `strings.Builder`, inserting `"\n"` **between** blocks (bracketed-paste-safe per tui-driver v1.3.0).
- **First non-text block** (`image` / `audio` / `resource` / `resource_link` / anything else) → return `nil, CodeInvalidParams` with message `unsupported content block type "<type>"`. Fail-closed: pyry advertises `promptCapabilities` all-false, so any non-text block is a host protocol violation; rejecting (vs silently dropping) never loses host content silently and never reinterprets it. **Documented behaviour** (AC-3).
- **Control-byte guard (security MUST — see § Security review, finding [Subprocess/injection]).** If any text block contains a C0/DEL control byte **other than** `\t` (0x09), `\n` (0x0a), `\r` (0x0d) — i.e. any byte in `0x00–0x08`, `0x0b`, `0x0c`, `0x0e–0x1f`, or `0x7f` — return `nil, CodeInvalidParams "control character in prompt text"`. This is the deterministic boundary check that blocks terminal-escape injection: the delivery path frames the payload as a **bracketed paste**, so a raw `ESC` (0x1b) — e.g. an embedded paste terminator `ESC[201~` — could break out of paste framing and be interpreted by claude's TUI as keystrokes / control sequences rather than message text. A text-only host never legitimately sends a raw control byte in a `text` block, so fail-closed rejection loses no valid content. (Belt-and-suspenders, different fabric: a deterministic code guard at the trust boundary, not a second stochastic layer.)
- After iteration, if the assembled payload is empty (no text blocks, or `prompt` absent/empty) → return `nil, CodeInvalidParams "empty prompt"`.
- Any JSON decode failure → `nil, CodeInvalidParams "invalid params"`.

Returns raw `[]byte` — otherwise **no transformation, no escaping**. Prompt size is bounded upstream by the transport's 16 MiB per-line cap (`internal/acp/acp.go` `maxLineBytes`), so no additional length cap is added here (matching the mobile `send_message` "no length cap" contract, but with a real frame-level bound). The only content the mapper rejects is a non-text block or a disallowed control byte; everything else passes verbatim.

### `serveACPWithPool` edit (`cmd/pyry/acp.go`)

Inside the existing function, before/within the `register` closure:

```go
holds := newPromptHolds(logger)
// ... inside register(t *acp.Transport):
t.Register("session/prompt", promptHandler(holds, func(id sessions.SessionID) (promptDeliverer, error) {
    sess, err := pool.Lookup(id)
    if err != nil {
        return nil, err                    // true nil interface — NOT a typed nil *Session (cancel-handler trap)
    }
    return sess, nil
}, logger))
```

The handler maps the `resolve` error: `errors.Is(err, sessions.ErrSessionNotFound)` → `acp.NewError(CodeInvalidParams, "unknown session")`; any other error → `fmt.Errorf("session/prompt: %w", err)` (→ `CodeInternalError`, detail logged, never leaked). Identical to `loadSessionHandler`.

### Why a spawned goroutine (not the read loop)

`WriteUserTurn` blocks for seconds (WaitReady + commit-confirm). Running it inline on the read loop would stall classification of every later frame — including the concurrent-second-prompt this ticket must *reject* and the `session/cancel` that aborts the turn. The guard check (`begin`) is cheap and stays inline (race-free on the single read-loop goroutine); only the blocking delivery moves to the goroutine, which returns `ErrDeferred` so the read loop proceeds immediately. This is the core reason #765 exists.

---

## Concurrency model

- **Goroutines:** one short-lived delivery goroutine per accepted prompt. Lifetime bounded by `promptDeliverTimeout` (WriteUserTurn's ctx), and it derives from the handler ctx (the Serve loop ctx), so Serve shutdown cancels in-flight deliveries. No goroutine outlives its bound. No new long-lived goroutine, no new clock/ticker.
- **Locks:** `promptHolds.mu` is a leaf lock — `begin` holds it across only a map read+write; `end`/`fail` capture the responder under it and release **before** resolving (so the transport's `writeMu`, taken inside `Reply`/`ReplyError`, is never nested under `mu`). The store is never touched by the transport write path, so no `writeMu → mu` edge exists; no lock-order cycle.
- **Read-loop liveness:** `begin` + spawn + `return ErrDeferred` are all non-blocking; the read loop keeps classifying while a call is held (the #765 property this ticket relies on and tests directly).
- **Exactly-one-frame:** `Responder.done` CAS (#765) elects the single resolver across `end` / `fail` / double-resolve / post-teardown. `promptHolds` delete-on-first-resolve means at most one of `end`/`fail` even finds the entry.

**Shutdown:** Serve ctx cancels → delivery goroutines' `tctx` cancels → `WriteUserTurn` returns `ctx.Err()` → `holds.fail` resolves with an error (the write no-ops on the torn-down writer, logged-and-dropped per #765) and the goroutine exits. A call that was delivered-successfully and is still "running" (never `end`ed, since T7 isn't wired) is simply abandoned: its `Responder` is GC'd with the store, its goroutine already returned, no frame is owed on a closing transport. No leak.

---

## Error handling

| Condition | Result | Code |
|---|---|---|
| Empty / missing `sessionId` | reject before `Lookup` (bootstrap trap) via `decodeSessionID` | `CodeInvalidParams` |
| Non-text content block | reject, naming the kind | `CodeInvalidParams` |
| Disallowed control byte in text (ESC/NUL/etc., not `\t\n\r`) | reject — blocks paste-framing escape (§ Security review) | `CodeInvalidParams` |
| Empty prompt (no text) / absent `prompt` | reject | `CodeInvalidParams` |
| Malformed params JSON | reject | `CodeInvalidParams` |
| Non-empty unknown `sessionId` | map `ErrSessionNotFound` | `CodeInvalidParams` "unknown session" |
| Other `Lookup` error | wrap, detail logged not leaked | `CodeInternalError` |
| Second concurrent prompt, same session, one in flight | reject; **no** hold registered, **no** delivery spawned | `CodeInvalidRequest` |
| `WriteUserTurn` failure (no live session / not committed / deadline) | `holds.fail` resolves the held call; slot freed so the next prompt is accepted; sentinel logged | `CodeInternalError` "prompt delivery failed" |
| `session/prompt` sent as a notification (no id → nil responder) | plain error (logged; no frame owed); no hold, no spawn | — |

`CodeInvalidRequest` for the concurrent-second case is a judgment call — ACP defines no dedicated "busy" code; `-32600` ("invalid in current state") is the closest standard fit and carries a clear message. Documented so a reviewer sees it was chosen, not defaulted.

---

## Security boundary (design)

`session/prompt` params originate from the ACP host, which the ticket treats as **potentially untrusted** (may be remote). The sensitive datum is the prompt content (`prompt[].text`). The security property this design guarantees: host content is delivered **strictly as a user turn** into claude and is **never** interpreted as pyry control input, shell input, a path, or — after the § Security review fix — a terminal control sequence. The formal adversarial walk and verdict are at the end of this spec (§ Security review). Design-level summary:

- **Single narrow sink.** `mapPromptContent` produces raw `[]byte` whose *only* sink is `promptDeliverer.WriteUserTurn` → `Supervisor.WriteUserTurn` → tui-driver `DeliverPrompt` (bracketed paste into claude's PTY). The bytes never reach `os/exec`, the control-socket verb dispatch, or a filesystem path.
- **Content-as-command is structurally absent.** No shell is in the path; the claude subprocess argv is fixed (`--session-id <uuid>`, #761) and set at spawn, long before any prompt arrives — host content is never an argv. `$(...)`, backticks, `;`, `../` are inert *shell* metacharacters here.
- **Content-as-terminal-control is closed by the control-byte guard** (§ Security review MUST FIX): raw `ESC`/C0 bytes that could break bracketed-paste framing are rejected at `mapPromptContent`.
- **No host content is logged** (method/session-id/sentinel only), and `conversationID` is the resolved pool id, not a host string.

---

## Testing strategy

Add `cmd/pyry/acp_prompt_test.go`. Table-driven where natural; `t.Parallel()`; package already runs under `-race` in `make check`. Two harness pieces: (a) a `recordingDeliverer` fake (mirror `gatingWriter`: records payloads, optional injected error, optional commit gate); (b) a pool-free live-transport driver reusing `serveACP(ctx, r, w, logger, register)` over `io.Pipe`s with a **custom register** that wires `session/prompt` over a test-owned `promptHolds` + fake deliverer (no fake-claude pool needed — the delivery seam is the interface). Scenarios (write bodies in the project idiom; do **not** paste as code):

1. **`mapPromptContent` (pure, table-driven).** single text → bytes; two text blocks → joined with `\n` (assert `\t`/`\n`/`\r` inside a block are **preserved**, not rejected); a non-text block (`image`) → `CodeInvalidParams` naming the kind; a text block containing `ESC[201~` (and separately a bare `\x00`) → `CodeInvalidParams "control character in prompt text"` (the paste-framing-escape guard); empty `prompt` array / absent `prompt` → `CodeInvalidParams "empty prompt"`; malformed params → `CodeInvalidParams`. No transport needed.
2. **Delivery + hold (AC-1, AC-2).** Feed one `session/prompt`; assert the fake deliverer received a payload equal to the mapped text; assert **no** response frame is on the wire yet (call held). Then call `holds.end(id, "placeholder_end")`; read one frame; assert it echoes the request id and carries `{"stopReason":"placeholder_end"}`.
3. **Concurrent-second reject + read-loop liveness (AC-2).** Feed prompt #1 (gated deliverer, so it stays mid-delivery / held); feed prompt #2 for the same session; assert #2 gets an **error** frame (`CodeInvalidRequest`) *before* #1 is resolved — proving the read loop stayed live while #1 was held. Then `holds.end(id, ...)` #1 and assert its frame.
4. **Unknown / empty session (AC-4).** Unknown non-empty `sessionId` → `CodeInvalidParams` "unknown session", fake deliverer never called, no hold registered. Empty / missing `sessionId` → `CodeInvalidParams` (rejected pre-Lookup). Reuse the `decodeSessionID` table shape.
5. **Security boundary (AC-5).** Feed a prompt whose text is shell-dangerous — e.g. `` $(touch pwned); rm -rf ~ `whoami` `` — and assert the fake deliverer recorded byte-identical bytes (no expansion / no stripping), and that it is the only sink (no side effect: e.g. the `pwned` file does not exist). Documents "delivered strictly as a user turn."
6. **Delivery failure frees the slot.** Fake deliverer returns an error → the held call resolves with an error frame (`CodeInternalError`) → a *subsequent* `session/prompt` for the same session is **accepted** (not rejected as in-flight), proving `holds.fail` freed the slot.

An optional integration check may extend `acpHarness` (real fake-claude pool) to assert `session/prompt` delivers through the interactive path, but scenarios 2/5 over the interface already pin delivery+security; the argv/interactive-path proof is inherited from the #761/#762 session tests.

---

## Sizing

- **Production:** new `cmd/pyry/acp_prompt.go` — `promptDeliverer` (3) + `promptParams`/`contentBlock`/`promptResult` (~12) + `promptHolds` + `newPromptHolds`/`begin`/`end`/`fail` (~55) + `mapPromptContent` (~30) + `promptHandler` + `deliverPrompt` (~55) + doc comments (~30) ≈ **~165 lines**. Plus `cmd/pyry/acp.go` `serveACPWithPool` edit ≈ **~10 lines**. **2 production files, 0 new exported types.**
- **Tests:** ~2 harness helpers (~70) + 6 scenarios (~220) ≈ **~290 lines**.
- **Total:** ~465 lines. Under S (≤400 *production*; production here is ~175) and well under the 600 total-work split line.
- **Reject branches:** 8 (empty-id, non-text, control-byte, empty-prompt, malformed, unknown-session, in-flight, delivery-fail) — under the 10-branch red line.
- **Edit fan-out:** none. `codegraph_impact serveACPWithPool` → callers are `runACP` + tests only; the change is one added registration in an existing closure. No signature change, no consumer cascade. Size by lines → **S, no split.**
- **File-overlap (§1.5):** `git fetch --prune` + branch scan — no `origin/feature/*` branch touches `cmd/pyry/acp.go` or the new files. No block needed.

---

## Open questions

- **Concurrent-second-prompt error code.** Spec picks `CodeInvalidRequest`. If the developer finds an ACP-idiomatic "session busy" code in a later spec revision, swapping it is a one-line change; the binding constraint is *a well-formed ACP error that is not `CodeInvalidParams`* (the params were valid; the state was not).
- **Text-block join separator.** Spec picks `\n` between blocks (bracketed-paste-safe). If the developer confirms hosts only ever send a single text block in practice, the separator is inert; `""` (verbatim concat) is an acceptable alternative. Not load-bearing.
- **`deliverPrompt` as free func vs closure.** Either satisfies the design; a free func with explicit params is easier to unit-test in isolation, but the handler-driven scenarios already cover it.

---

## Security review

**Verdict:** PASS *(after one MUST FIX, now folded into § Design `mapPromptContent` — the control-byte guard. First pass was FAIL; this is the re-run.)*

**Findings:**

- **[Trust boundaries]** No findings. Single explicit boundary: `mapPromptContent` (the only decoder of host `prompt` content) and the single sink `promptDeliverer.WriteUserTurn`. Downstream code holds opaque `[]byte`; nothing re-parses it. `sessionId` is validated via `decodeSessionID` (canonical, non-empty) + `Pool.Lookup` (known pool id) before use; the empty-id pre-`Lookup` rejection is load-bearing (bootstrap trap). ACP stdio is single-host, so no cross-tenant session confusion.
- **[Subprocess / injection]** **MUST FIX → fixed.** The delivery path frames the payload as a **bracketed paste** (`DeliverPrompt`, tui-driver v1.3.0). A payload containing a raw `ESC` — notably an embedded paste terminator `ESC[201~` — could break out of paste framing so the remaining bytes reach claude's TUI as **keystrokes / terminal control sequences**, directly violating "never interpreted as control input." Shell injection is structurally absent (fixed argv set at spawn, no `sh -c`, content never an argv), but terminal-escape injection is real for a paste sink. **Fix:** `mapPromptContent` rejects any C0/DEL control byte other than `\t\n\r` → `CodeInvalidParams`. Deterministic guard at the trust boundary (belt of different fabric). Pinned by a test (§ Testing scenario 1).
- **[Tokens / secrets / credentials]** N/A. This handler carries no auth material; host authentication is the `authenticate` handshake's concern (#747), and the `security-sensitive` label here tracks the host-content→claude delivery boundary, not credentials.
- **[File operations]** No findings. No host string is used to build a filesystem path. `conversationID` passed to `WriteUserTurn` is the **resolved** pool session id, not a host field, and is not consulted anyway (`ValidateConversation == nil` on the ACP pool). Prompt bytes are typed into a PTY, never used as a path.
- **[Cryptographic primitives]** N/A. No crypto in this ticket.
- **[Network & I/O]** No findings. Prompt size is bounded by the transport's 16 MiB per-line cap (`maxLineBytes`, #755); a single frame cannot exceed it, so the payload is bounded without a new cap. `WriteUserTurn` is bounded by `promptDeliverTimeout` (15 s) and runs off the read loop, so a slow/wedged claude cannot stall frame processing (no slow-loris on the read path).
- **[Error messages / logs]** No findings. Handler / `promptHolds` / `deliverPrompt` log only method name, session id, and error sentinels — never `params`, payload, or `stopReason`-adjacent host content (matches the `internal/acp` package-doc diagnostics discipline). The `WriteUserTurn` error logged on failure is a supervisor sentinel (stable `"supervisor: write user turn:"` wrap), carrying no payload. Host-facing errors are generic (`"prompt delivery failed"`), detail logged not leaked.
- **[Concurrency]** No findings. `promptHolds.mu` is a documented leaf lock; `begin` is an atomic check-and-insert; `end`/`fail` capture-then-release-then-resolve, so `writeMu` is never nested under `mu` and no lock-order cycle exists (the store is never touched by the transport write path). Exactly-one-frame is guaranteed by the #765 `Responder.done` CAS across every resolve race. Delivery goroutines are bounded (≤1 per session via the in-flight guard; lifetime ≤ `promptDeliverTimeout`; cancelled on Serve shutdown) — no leak, no unbounded growth.
- **[Threat model alignment]** The addressed threat is a hostile/compromised ACP host escaping the "user turn" framing: shell injection (structurally absent), terminal-control injection (closed by the control-byte guard), path traversal (no host path). **Out of scope, named:** host authentication (#747), per-host session-count / spawn DoS (owned by the pool, #761), outbound content redaction (T6 #750), and resolving the held call on real `TurnEnd` (T7 #751).
- **[Cost invariant]** Delivery reaches **interactive** claude via `WriteUserTurn` → `DeliverPrompt` on the #761 spawn (`claude --session-id <uuid>`), never `claude -p` / Agent SDK. Holds by reuse — no new spawn path introduced.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-04
