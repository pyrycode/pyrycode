# `session/prompt` — the held turn call (#749)

The **main call** (epic #600). A `session/prompt` request carries a user turn as
an ordered list of content blocks; the handler resolves the target session from
the [#761](#sessionnew-and-the-embedded-pool-761) pool, maps the blocks to a
user-turn payload, delivers it into the supervised **interactive** claude via
`Session.WriteUserTurn` → `Supervisor.WriteUserTurn` → the tui-driver
`DeliverPrompt` path (the hard cost invariant — no `claude -p`, no SDK), and
**holds its own JSON-RPC call open** for the whole turn via #765's deferral
primitive. An ACP turn is one `session/prompt` request that streams `session/update`
notifications and then returns a `stopReason`, so deliver-in and hold-open are
**one handler** — a `session/prompt` that returned synchronously would violate the
turn model. One new file `cmd/pyry/acp_prompt.go` (package `main`) + a ~15-line
edit to `serveACPWithPool`, **0 new exported types**. **security-sensitive**
(host content reaches claude) — architect security pass **PASS**.

### The handler (read loop → deferral → delivery goroutine)

`promptHandler(holds, resolve, logger)` runs inline on the read loop and does only
cheap, race-free work before deferring:

1. `decodeSessionID(params)` — the shared #762 guard; empty/missing id →
   `CodeInvalidParams` **before** `Pool.Lookup` (the bootstrap trap: `Lookup("")`
   resolves the parked bootstrap, not an error).
2. `mapPromptContent(params)` — content blocks → payload bytes (below).
3. `resolve(id)` = `pool.Lookup(id)` — `ErrSessionNotFound` → `CodeInvalidParams
   "unknown session"` (mirrors `loadSessionHandler`); other error → `CodeInternalError`.
4. `acp.ResponderFrom(ctx)` — a `session/prompt` sent as a **notification** carries
   no responder → plain error, no hold, no spawn (a turn whose `stopReason` can
   never be returned must not start).
5. `holds.begin(sessionId, resp)` — the per-session in-flight guard. A **concurrent
   second prompt** for the same session while one is pending → `CodeInvalidRequest`
   ("a prompt is already in flight"), no hold registered, no goroutine spawned.
6. `go deliverPrompt(...)` then **`return acp.ErrDeferred`** — the transport writes
   nothing and the read loop scans the next frame immediately.

`WriteUserTurn` blocks for seconds (WaitReady + commit-confirm, ~10s), so it runs
on a spawned goroutine bounded by `promptDeliverTimeout = 15s` (deliberately >
the supervisor budget, so a genuine supervisor failure surfaces first; the timeout
is only the outer backstop). Running it inline would stall classification of the
very concurrent-second-prompt reject and `session/cancel` this handler must keep
live — **this is the core reason #765 exists.** On success the call stays held
(T7 resolves it); on a non-nil `WriteUserTurn` error, `deliverPrompt` logs a
sentinel (never the payload) and calls `holds.fail`, resolving the held call with a
generic `CodeInternalError "prompt delivery failed"` and freeing the slot.

### `promptHolds` — per-session in-flight registry

A `sync.Mutex`-guarded `map[string]*acp.Responder`, at most one held call per
session id, the **sole owner** of held-call resolution:

| Method | Role |
|---|---|
| `begin(id, resp) bool` | Atomic check-and-insert inline on the read loop; `false` if one is already in flight (caller rejects, registers nothing). |
| `end(id, stopReason)` | Resolve with `{"stopReason":…}` and free the slot — wired to the outbound stream's `onTurnEnd` by [#751](https://github.com/pyrycode/pyrycode/issues/751), so a turn's `TurnEnd` returns the held call. |
| `fail(id, *acp.Error)` | Delivery failed — resolve with an error frame and free the slot (next prompt for that session is accepted). |

`end`/`fail` **capture the responder under `mu`, delete, then release `mu` before**
calling `Reply`/`ReplyError` (those take the transport `writeMu`) — so `mu` is a
pure leaf and no `mu → writeMu` nesting exists; the store is never touched by the
transport write path, so no cycle is possible. A missing entry is a **no-op**
(idempotent), so `end` is safe even if `fail` already fired and vice-versa.
Exactly-one-frame across every resolve race is #765's `Responder.done` CAS;
`promptHolds` adds slot bookkeeping, not a second frame guard. The store is
constructed once per `pyry acp` process in `serveACPWithPool` and closed over by
the handler (same pattern as `newSessionHandler(pool)`).

### `mapPromptContent` — content blocks → payload (fail-closed, AC-3)

Decodes `{prompt:[{type,text}]}`, joins `text` with `"\n"` **between** blocks
(bracketed-paste-safe per tui-driver v1.3.0 — an embedded newline does not submit
the turn early), then passes the text through **verbatim** (no escaping, no
transformation). No length cap — the transport's 16 MiB per-line bound already caps
it. It **fails closed** with `CodeInvalidParams` on:

- a **non-text block** (`image`/`audio`/`resource`/…), naming the kind —
  `promptCapabilities` is all-false, so any non-text block is a host protocol
  violation; rejecting never drops host content silently and never reinterprets it;
- a **disallowed control byte** — see the security boundary below;
- an **empty** assembled payload / absent `prompt` → `"empty prompt"`;
- **malformed** params JSON → `"invalid params"`.

### Security boundary — content delivered strictly as a user turn

The security property: `prompt[].text` is delivered as a user turn into claude and
**never** interpreted as pyry control input, shell input, a path, or a terminal
control sequence. `mapPromptContent`'s `[]byte` has exactly one sink
(`WriteUserTurn` → `DeliverPrompt`, a bracketed paste into claude's PTY) — the bytes
never reach `os/exec`, the control-socket verb dispatch, or a filesystem path.
Shell injection is **structurally absent** (fixed argv set at spawn, no `sh -c`,
content never an argv), so `$(…)`, backticks, `;`, `../` are inert.

**The MUST-FIX vector was terminal-escape injection.** Because delivery frames the
payload as a bracketed paste, a raw `ESC` — e.g. an embedded paste terminator
`ESC[201~` — could break out of paste framing and reach claude's TUI as
keystrokes / control sequences. `hasDisallowedControl` rejects any C0
(`0x00–0x1f`) or DEL (`0x7f`) byte **except** `\t \n \r`. It scans **bytes, not
runes**, which is both correct and UTF-8-safe: valid multi-byte UTF-8 uses only
bytes ≥ 0x80, so a byte < 0x20 or == 0x7f is never a continuation byte → no false
positive on international text. A text-only host never legitimately sends a raw
control byte, so fail-closed rejection loses no valid content. This is the
deterministic code guard at the trust boundary (belt of different fabric — not a
second stochastic layer). No host content is logged (method / session-id /
sentinel only); `conversationID` passed to `WriteUserTurn` is the **resolved pool
id**, not a host string (and isn't consulted — the ACP pool sets
`ValidateConversation == nil`).

### The concurrent-second error code (a judgment call)

ACP defines no dedicated "session busy" code, so the second-prompt reject uses
`CodeInvalidRequest` (`-32600`, "invalid in current state") — the params were
valid, the *state* was not, so `CodeInvalidParams` would misdescribe it. The
binding constraint is *a well-formed ACP error that is not `CodeInvalidParams`*; if
a later ACP revision adds an idiomatic busy code, swapping it is a one-line change.

### Out of scope

Resolving the held call on real `TurnEnd` with the mapped `stopReason` was #749's
deferred item, now landed as
[#751](https://github.com/pyrycode/pyrycode/issues/751): the outbound stream's
`onTurnEnd` seam is wired to `holds.end`, so a turn's terminal `TurnEnd` returns the
held `session/prompt` with its `stopReason` (see
[held-call resolution](#turnend--held-call-resolution-751)). The producer that drives
it (a `turnbridge` producer + the #750 `acpTurnStream` sink for ACP sessions) is
[#796](#producer-wiring-796). #749's tests originally drove `end` through a
**placeholder-end** path; #751 pins the mapped values over the wired stream. Full
per-ticket detail — including #749's two test-harness lessons (resolve a held call
off the reader goroutine; build `ESC[201~` at runtime to satisfy `substrate-guard`) —
in [`codebase/749.md`](../codebase/749.md); the join itself in
[`codebase/751.md`](../codebase/751.md).
