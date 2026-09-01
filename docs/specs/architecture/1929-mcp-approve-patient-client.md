# #1929 — stop the `mcp-approve` client cutting short an approval the daemon is still holding

## Files to read first

- `cmd/pyry/mcp_approve.go` → `approveServer`, `newMCPApproveServer`, `toolsCall`, `mcpApproveClientMargin` — the whole surface this ticket edits. `toolsCall`'s `cctx` block is the one behavioural change here; everything else in the file is doc.
- `internal/control/client.go` → `Approve`, `request`, `DialTimeout` — `Approve`'s doc comment is a *contract* that has to change, not be reworded. `request` is shared by ten sub-second verbs and must keep its bound.
- `internal/control/dial.go` → `dial`, `dialWithRetry`, `dialRetryBudget` — read this to confirm the dial's bound is independent of the ctx deadline (`dialWithRetry` wraps an undeadlined ctx with `DialTimeout` *for the dial only*). This is what keeps AC 3's first half reachable after the read goes deadline-free.
- `internal/control/server.go` → `handleApprove`, `watchApproveConn` — the daemon half. `handleApprove` clears its own conn deadline before `Await`; `watchApproveConn` maps a client disconnect to a deny. Read these to see why a client-side give-up *terminates* the approval rather than merely losing a message.
- `cmd/pyry/mcp_approve_test.go` → `startApprovePeer`, `replyPeer`, `holdPeer`, `newApproveServer`, `invokeToolsCall`, `assertVerdict`, `TestMCPApproveServer_ClientDeadline`, `TestControlApprove_UndeadlinedCtxReturns` — the fake-peer kit to build on, and the two tests whose premises this ticket invalidates.
- `cmd/pyry/approval_timeout_test.go` → `TestApprovalTimeout` — confirm for yourself that the numeric default-window pin lives here (`approvalTimeout() != 10*time.Minute`) before retiring the table in `mcp_approve_test.go`. Nothing may be kept alive in that table merely to host that pin.
- `cmd/pyry/main.go` → `approvalTimeout`, `mcpApprovalTimeout`, `envApprovalTimeout` — the accessor keeps its daemon-side caller (`SetApprovalRegistry` in the composition root). This ticket removes only the *client's* use of it.
- `internal/e2e/internal/fakeclaude/main.go` → `approveDialTimeout` — its doc comment asserts `control.Approve` *requires* a ctx deadline ≥ the daemon window. That clause becomes false; correct it.
- `docs/knowledge/features/pyry-mcp-approve-command.md` § "The fail-closed core" and § "`internal/control.Approve` client helper" — the written statement of the coupling being removed. **Read-only for you**; the documentation phase folds this ticket in.
- `docs/knowledge/features/control-plane-approve-mcp-approve-verb-forward-to-permbridge.md` § "`handleApprove`: guard order, then block" — the daemon's fail-closed structure (allow is reachable only through the trusted in-process resolver). The security argument in this spec rests on it.

## Context

`pyry mcp-approve` is the `--permission-prompt-tool` MCP server claude blocks on for every non-allowlisted tool use. Each `tools/call` becomes one control-socket `mcp.approve` request; the daemon parks it in `permbridge`, and the daemon's verdict becomes the tool result.

Today `toolsCall` bounds that call at `approvalTimeout() + mcpApproveClientMargin` — a fixed thirty seconds past the daemon's own approval window, both ends derived from the one env-aware accessor so the ordering holds at every window (#1507). The margin exists to settle a *message race*: both ends expire at nearly the same moment, and the margin only decides whether claude sees the daemon's informative "approval request timed out" or the client's generic "approval unavailable".

That is correct only while the daemon's window is the real bound. When the client's deadline fires it does two things, and the second is damaging:

1. `control.Approve`'s ctx deadline errors → the client returns its own deny to claude.
2. The connection closes (`request` sets the conn deadline from the ctx and closes on return) → `watchApproveConn` reads that as a lost caller and resolves the registry entry to a deny.

So a client-side give-up does not lose a race for the message — it **terminates the daemon's approval**, and `permbridge`'s one-shot resolution discards every later answer. #1912 wants the daemon to hold an approval past its window; it cannot, while the client is the shorter bound.

This slice removes the client as a bound. **Nothing about the daemon's window moves**: it still denies on the existing window with the existing fixed message, and the client still fails closed on every path it does today. Until #1912 lands the daemon's timer resolves first every time, so this is pure headroom with no observable behaviour change.

**Liveness replaces duration.** Once #1912 lands, no client-side duration can separate "wedged" from "legitimately held" — any ceiling picked here is the same truncation, just later. The client's bound therefore becomes: the dial's own budget, ctx cancellation, and the connection ending. A daemon that is alive, holding the conn open, and will never resolve is knowingly not covered — the daemon owns its own resolution guarantees (`permbridge`'s registry-owned timer, `watchApproveConn`'s disconnect and shutdown denies).

**No ADR.** This narrows an existing contract inside one client helper; the change belongs in the two package overviews the documentation phase already owns.

### A correction this makes, not merely preserves

`toolsCall`'s current comment claims "a mid-approval SIGTERM unblocks the control read → deny", and the package overview's § Concurrency repeats it. **There is no mechanism for that today.** `request` installs a conn deadline and then never watches `ctx.Done()`; `dial` honours the ctx, but nothing after the dial does. `serveJSONRPCStdio`'s ctx watcher closes the *stdin* pipe, which wakes the read loop only between frames — a handler already parked in `json.Decoder.Decode` on the control socket is not woken. So a SIGTERM arriving mid-approval today leaves the process parked until the conn deadline (up to the window plus margin).

AC 4 says the signal "still" unblocks the call. The ctx watcher this spec adds is what makes that mechanically true for the first time. Treat AC 4 as a fix, not a regression guard, and expect its test to be red against the pre-change tree.

## Design

Two production edits plus one stale-comment correction. No new exported surface.

### 1. `internal/control` — a patient request path, for the approve verb only

`request` keeps its ctx-deadline-or-`DialTimeout` conn deadline: its other nine callers are sub-second round-trips and their bound must not move as a side effect.

Add a sibling that differs only in deadline policy, and factor the shared frame exchange so the difference is the only thing that reads as different:

```go
// exchange encodes req and decodes one Response on an already-dialled conn.
func exchange(conn net.Conn, req Request) (*Response, error)

// requestPatient sends one Request and blocks for the Response with NO conn
// read deadline. Bounded by liveness, not duration: the dial (dialRetryBudget),
// ctx cancellation, and the conn ending. Approve is its only caller.
func requestPatient(ctx context.Context, socketPath string, req Request) (*Response, error)
```

`requestPatient`'s body, in order:

- `dial(ctx, socketPath)` — unchanged, and still bounded by `dialRetryBudget` regardless of the ctx's deadline (or absence of one), because `dialWithRetry` installs its own `DialTimeout` for an undeadlined ctx.
- `defer conn.Close()`.
- `stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })` / `defer stop()` — the ctx watcher. A deadline in the past expires parked and future I/O immediately, so a cancelled ctx wakes a blocked `Decode` with `os.ErrDeadlineExceeded`. `net.Conn`'s deadline methods are safe for concurrent use, and an already-cancelled ctx makes `AfterFunc` fire before the first write, which fails closed.
- `return exchange(conn, req)` — **no `SetDeadline` call on the success path at all.**

`context.AfterFunc` has no other use in this tree; it is the stdlib idiom for exactly this (register on cancel, deregister on return) and replaces the goroutine-plus-`done`-channel shape it existed to remove. Keep it — do not hand-roll the goroutine.

**No write deadline.** The request is one small JSON object into a unix socket buffer; a blocked write means a peer that has stopped reading, which is the wedged-daemon case this ticket knowingly does not cover, and the ctx watcher wakes it anyway.

`Approve` switches its one `request` call to `requestPatient`. Its doc comment is **rewritten**, not reworded: the "Callers MUST pass a ctx whose deadline is >= the daemon's approval window" requirement is deleted, and what replaces it states the new contract — no read deadline is derived from the ctx; the caller's ctx cancellation and the conn ending are the only bounds; the dial still fails fast; an undeadlined ctx is now genuinely patient rather than truncated at `DialTimeout`. `Approve`'s error mapping does not change (no `ErrorCode` mapping — the caller fail-closes on any error).

After the edit, `requestPatient` must have exactly two references in `internal/control` (its definition and `Approve`); every other verb still routes through `request`.

### 2. `cmd/pyry` — the client stops carrying a bound

- Delete `mcpApproveClientMargin` and the `timeout` field on `approveServer`. `approveServer` becomes `{socketPath string; log *slog.Logger}` — still read-only after construction, still no locks.
- `newMCPApproveServer` stays and stays the sole construction site (`grep -n 'approveServer{' cmd/pyry/mcp_approve.go` must still return exactly one hit). Its doc comment is rewritten: it no longer derives anything from `approvalTimeout()`; what it now documents is that the client carries no per-call bound at all, and why (the daemon owns the window; a client duration cannot separate held from wedged). The old rationale for the constructor — "make the derived deadline observable" — is replaced by a live one: it is the seam the at-every-window test builds through, and it is what keeps the composite literal out of the blocking `runMCPApprove`.
- `toolsCall` drops `cctx, cancel := context.WithTimeout(ctx, s.timeout)` and calls `control.Approve(ctx, s.socketPath, payload)` directly — `ctx` is the `Serve` ctx, which carries the signal cancellation and now actually reaches the control read via the watcher. Its comment says so.
- The error/verdict mapping is **unchanged**: `err != nil` → `deny("approval unavailable")`, `res == nil` → `deny("no verdict")`, otherwise the daemon's verdict marshalled verbatim. The "client read deadline" row of the error table simply stops being reachable; nothing else moves.
- `approvalTimeout()` keeps its daemon-side caller in the composition root. Do not touch it, `mcpApprovalTimeout`, or `envApprovalTimeout`.
- **Nothing new in the logs.** `logVerdict` stays the sole decision log, emitting the correlation key and the behaviour only. Do not add a "still waiting" diagnostic — `tool_name` and the raw arguments are model-controlled on every branch, and this would be the one place they could leak.

### 3. `internal/e2e/internal/fakeclaude` — one stale clause

`approveDialTimeout`'s doc comment ends with "`control.Approve` also requires a ctx deadline >= the daemon window (client.go), so it doubles as that patient-read line." That requirement no longer exists. Correct that clause only; the constant, its value, and the load-bearing reason for it (30s sits far above the shrunk 2s window, so the deny the fake receives is the *daemon's* timer and not a self-timeout) are all still true and must survive the edit intact.

### What the caller now supplies

| Caller of `control.Approve` | ctx it passes | Effective bound after this change |
|---|---|---|
| `toolsCall` (`pyry mcp-approve`) | the `Serve` ctx — signal cancellation, no deadline | dial budget; SIGTERM/SIGINT; conn end |
| `dialApproval` (fakeclaude rider) | a 30s-deadlined ctx | unchanged — the watcher fires at that deadline |

## Concurrency model

One goroutine is added per patient request, and it is `context.AfterFunc`'s, not one this code writes:

- **Spawned:** by `AfterFunc` when `ctx` is done, at most once per `requestPatient` call.
- **Work:** a single `conn.SetDeadline(time.Now())`. It never blocks and never touches shared state beyond the conn.
- **Exit:** it returns immediately after the one call. `defer stop()` deregisters it on every return path so a never-cancelled ctx leaves nothing parked — the leak shape the hand-rolled `select { <-ctx.Done(); <-done }` version would have needed a second channel to avoid.
- **Ordering with `Close`:** `defer stop()` is registered after `defer conn.Close()`, so it runs first (LIFO). If the watcher is already mid-call, `SetDeadline` on a closing conn returns an error that is deliberately discarded; `net.Conn`'s deadline and close methods are documented safe for concurrent use.

Unchanged elsewhere: `acp.Transport.Serve` still dispatches inline on one read-loop goroutine, so a blocking `tools/call` still blocks that loop. That remains safe because claude's permission path calls `approve` synchronously and blocks on the result — at most one in-flight approval per process — and the handler issues no outbound `Transport.Call`. What changes is the *duration* of that block: previously capped at the window plus margin, now bounded by the daemon resolving, the daemon's conn ending, or a signal.

Daemon side is untouched. `handleApprove` still clears its conn deadline before `Await`; `watchApproveConn` still maps client disconnect and daemon shutdown to fixed-message denies. The client simply stops producing a spurious disconnect.

## Error handling

Every path still terminates in a well-formed tool result with `isError:false` whose first text block is a verdict JSON. The self-originated verdict is still only ever deny.

| Path | Bound | Result |
|---|---|---|
| Socket absent / unreachable | `dialRetryBudget` (~1.5s), independent of the ctx | `deny("approval unavailable")` |
| Daemon `Response.Error` (nil registry, missing id) | round-trip | `deny("approval unavailable")` |
| Conn ends without a verdict (daemon exits or closes mid-approval) | immediate — `Decode` returns EOF | `deny("approval unavailable")` |
| Signal mid-approval | watcher fires on ctx cancel | `deny("approval unavailable")` |
| Daemon denies on its window | the daemon's window | the **daemon's** message, verbatim |
| Daemon allows | — | allow, verbatim, `updatedInput` byte-preserved |
| `params` / `arguments` malformed | pre-dial | `deny("malformed approval request")` |
| `name != "approve"` | pre-dial | `deny("unknown tool")` |
| Daemon alive, conn open, never resolves | **none** | blocks until signal — knowingly out of scope, see § Context |

The last row is the deliberate trade named in the ticket. It is an availability outcome, not a permission one: claude's turn stalls, and a stall is not an allow.

## Testing strategy

All in `cmd/pyry/mcp_approve_test.go` — the file that already owns both the fake-peer kit and the existing `control.Approve` property test. No new test file, no e2e addition. `make check` is the gate.

**Helpers**

- `newApproveServer(sock)` now delegates to `newMCPApproveServer(sock, testLogger(io.Discard))`. The old reason it deliberately did not (delegating would make eleven parallel tests read the ambient `PYRY_APPROVAL_TIMEOUT`) dies with the env read. Do the same for the raw `&approveServer{...}` literal in `TestMCPApprove_MalformedInput_NoByteLeak`, so the "exactly one composite literal" property survives.
- `delayReplyPeer(d, resp)` — decode one request, sleep `d`, encode `resp`. Mirrors `replyPeer`'s error discipline (peer-side errors ignored; never call `t.Errorf` from the handler goroutine).
- `hangUpPeer()` — decode one request, then close without replying. Decoding first is what makes it "the daemon exited mid-approval" rather than "the dial lost".
- `holdPeer`'s doc comment is now wrong about *why* the handler ends (it says the client closes at its read deadline). Rewrite it: the client now closes only when its ctx is cancelled, so **every test using `holdPeer` must cancel its ctx** — an uncancelled one hangs the test and deadlocks `startApprovePeer`'s `wg.Wait` cleanup.
- `invokeToolsCall` hardcodes `context.Background()`. Add a ctx-taking variant and have the existing one delegate, rather than editing its five call sites.

**Retire**

- `TestMCPApproveServer_ClientDeadline` — its subject (`s.timeout`) no longer exists. Its default-window numeric pin is not orphaned: `TestApprovalTimeout` asserts `approvalTimeout() != 10*time.Minute` directly, in the file that owns the accessor. Do not keep any field alive to host it.
- `TestControlApprove_UndeadlinedCtxReturns` — it asserts the *old* contract (an undeadlined ctx falls back to `DialTimeout` rather than blocking). Replace it; do not weaken it.

**New / reshaped scenarios**

- **`TestControlApprove_PatientPastDialTimeout`** (AC 1, the direct pin). Peer: `delayReplyPeer(control.DialTimeout + 1*time.Second, <daemon allow>)`. Call `control.Approve(context.Background(), …)`. Expect the daemon's verdict, not a read-deadline error. Non-vacuous by construction: the delay is expressed *as* `control.DialTimeout + 1s` so it tracks the constant, and the pre-change tree truncates at exactly that constant. This test costs ~6s of wall clock; `t.Parallel()`, and it replaces a test that already burned ~5s, so the file's total barely moves.
- **`TestControlApprove_CtxCancelUnblocks`** (AC 4, the direct pin on the watcher). Peer: `holdPeer()`. ctx: cancellable, **no deadline**; cancel after ~100ms. Expect `Approve` to return a non-nil error well under `control.DialTimeout`. Red against the pre-change tree, which has no mechanism to wake the parked `Decode` and returns only at the 5s fallback.
- **`TestMCPApprove_SignalCancelMidApproval_Deny`** (AC 4, at the surface claude sees). Same peer and ctx shape, driven through `toolsCall`. Expect `{"behavior":"deny","message":"approval unavailable"}` promptly. This is the only test that would catch a `toolsCall` that re-wraps the ctx in a way that drops cancellation.
- **`TestMCPApprove_ConnEndsWithoutVerdict_Deny`** (AC 3, second half). Peer: `hangUpPeer()`. Expect the generic deny, returned promptly rather than after any window. Assert the elapsed time is small — the point of the criterion is *when*, not only *what*.
- **`TestMCPApprove_SocketUnreachable_Deny`** (AC 3, first half) — keep the test, fix its comment. It currently justifies its bound as "well under the 5s timeout"; there is no client timeout now, and the real justification is the dial's own `dialRetryBudget`. The 3s assertion still holds.
- **`TestMCPApprove_DaemonMessageWinsAtEveryWindow`** (AC 2). Serial (`t.Setenv` forbids a parallel ancestor), replacing the retired serial table so the file's serial/parallel structure is unchanged. Server built by `newMCPApproveServer` — the production constructor is the subject. Peer: `delayReplyPeer(~250ms, <daemon deny with the daemon's own timeout message>)`. Rows vary `PYRY_APPROVAL_TIMEOUT`:
  - unset (the default window),
  - a short override far *below* the peer's delay, so the reply lands several multiples past the daemon's window,
  - one further distinct override.

  Each row asserts the result text is byte-exactly the daemon's message and never `"approval unavailable"`. **Assert content only, never latency** — there is no upper bound to assert, so machine load cannot flake it. Carry forward the guard from the retired table: `t.Fatalf` on any override row whose parsed duration equals `mcpApprovalTimeout`, since such a row passes even against a seam that ignores the env. Use the same `t.Setenv`-then-`os.Unsetenv` trick for the genuine unset row.

**What no test pins, stated plainly.** No cheap test kills a mutant that re-introduces a *large* client-side bound in `toolsCall` (say, thirty seconds or more) — catching it would require a test that sits out that bound. The enforcement for that is structural: `approveServer` carries no duration field, `mcpApproveClientMargin` no longer exists, and `newMCPApproveServer` reads no duration source. A reviewer checking this ticket should verify those three absences rather than look for an assertion. What *is* pinned behaviourally is every bound small enough to matter: the at-every-window table kills any bound derived from `approvalTimeout()` without a margin, and the two `control` tests kill the `DialTimeout` fallback and the missing watcher.

**Unchanged coverage that must stay green.** `TestRelayV2_StreamModalPermissionRoundTrip`'s timeout arm (`internal/e2e`) is the live proof that the daemon's own window still denies with its own message; the fake's rider passes a 30s-deadlined ctx, which the watcher honours exactly as the old conn deadline did. `TestInteractiveStreamPermissionDeny` (`internal/e2e/realclaude`, hence `needs-real-claude`) drives the real `--permission-prompt-tool` client at the default window. Neither needs an edit, and neither should gain one — the label is about existing live coverage continuing to hold, not an invitation to add a test that sits out a ten-minute window.

## Scope check

| Limit | Boundary | This spec |
|---|---|---|
| Production source files | ≤ 3 | 3 — `internal/control/client.go`, `cmd/pyry/mcp_approve.go`, `internal/e2e/internal/fakeclaude/main.go` |
| Total written work | ≤ 400 | ~285 (≈90 production, ≈195 test) |
| New exported types/interfaces | ≤ 5 | 0 |
| Consumer call sites | ≤ 10 | 2 `control.Approve` callers; `approveServer.timeout` referenced in 6 places, all in the two edited `cmd/pyry` files |
| Acceptance criteria | ≤ 5 | 4 |
| Error/reject branches | ≤ 10 | 8, unchanged |

Nearest analogue: #1507 (`8419001`), which introduced the margin — 78 insertions across the same two `cmd/pyry` files. This is ~3.5× that, because it adds a function in `internal/control` and reshapes two tests rather than adding one.

## Open questions

- **Does claude itself bound the MCP `tools/call`?** If its client applies a request timeout, that becomes the real ceiling on a held approval regardless of what pyry does, and #1912's "as long as someone can still answer" has a limit neither ticket controls. Nothing here depends on the answer; #1912 should establish it before promising an unbounded hold.
- **#1912 inherits the wedge.** With the client no longer bounding anything, the daemon's registry-owned timer is the sole guarantee that an approval resolves. #1912 extends exactly that timer, so whatever it replaces the window with has to carry the resolution guarantee with it — a resolver-less hold would turn the last row of the error table from "knowingly out of scope" into the normal case.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — the boundary does not move.** The untrusted inputs are claude's `tools/call` `params`/`arguments` (model-controlled) and the control-socket peer's `Response`. Both cross at the same two points as before: `toolsCall`'s two `json.Unmarshal` calls, and `Approve`'s `resp.Approve` check. This ticket changes *when* the second one is reached, never *what* is trusted. `input` remains `json.RawMessage` end to end, never parsed or dispatched on.
- **[Trust boundaries] No findings — fail-open is still structurally unreachable.** The only verdict this subcommand self-originates is deny (`deny`/`denyResult`, both fixed constants); an `allow` reaches claude solely by passing through `resp.Approve`, which the daemon produces only via the trusted in-process resolver gated behind `MayAnswerRemotePermission` (see the control-plane overview's § `handleApprove`). Removing a client-side deadline deletes a *deny* path; it creates no allow path. Verify this by mutation: no edit in this spec touches `denyResult`'s behaviour constant or the `switch` in `toolsCall` that selects between pass-through and deny.
- **[Network & I/O] SHOULD FIX — an unbounded read is a deliberate DoS trade, and the developer must not "improve" it.** Removing the read deadline is exactly the resource-exhaustion pattern this category exists to flag, and it is accepted here with a named justification: the process is a short-lived one-approval-at-a-time subprocess claude spawns, the peer is the local daemon, and the daemon bounds the hold with `permbridge`'s registry-owned timer. Cost of a wedge is one parked goroutine, one unix conn, and a stalled turn in a process claude owns — not unbounded growth. **The failure mode to guard against in review is a developer reintroducing a "safety" ceiling**, which silently restores the truncation this ticket removes. If #1912 later removes the daemon-side timer without replacing the guarantee, this trade must be re-evaluated there.
- **[Network & I/O] No findings — the dial is still bounded.** `dialWithRetry` caps the dial at `dialRetryBudget` and installs its own `DialTimeout` for an undeadlined ctx, independent of the read policy. An absent or unreachable socket still errors in ~1.5s, which is what keeps AC 3's first half reachable and prevents "no read deadline" from degenerating into "no bound at all".
- **[Network & I/O] SHOULD FIX — the rogue-socket scenario changes shape but is dominated.** An attacker who can bind the daemon's control-socket path could previously stall claude for at most the window plus margin and then get a deny; now they can stall it indefinitely (accept, never reply, hold the conn). This is a strict availability regression in that scenario — but the same attacker can instead reply `{"behavior":"allow"}` to every request and obtain a full permission bypass, which strictly dominates a stall. The boundary is therefore not widened by this change, and hardening it (authenticating the control socket) is a different ticket. Named, not fixed.
- **[Error messages, logs, telemetry] No findings, and one prohibition.** Every deny reason stays a fixed constant; `logVerdict` remains the sole decision log and emits only `tool_use_id` and `behavior`. The spec explicitly forbids adding a "still waiting" diagnostic on the newly long-lived path — that would be the one place `tool_name` or the raw arguments could leak — and `TestMCPApprove_MalformedInput_NoByteLeak` continues to pin the no-leak property. `Approve`'s errors are still returned verbatim to a caller that discards them into a fixed-constant deny, so no peer-controlled string reaches claude or the log.
- **[Concurrency] No findings — the added goroutine has a named exit on every path.** `context.AfterFunc`'s goroutine performs one non-blocking `SetDeadline` and returns; `defer stop()` deregisters it when the ctx is never cancelled. `SetDeadline` racing the deferred `Close` is safe (`net.Conn` methods are documented safe for concurrent use) and its error is deliberately discarded. No locks are taken, so there is no ordering to document; `approveServer` remains read-only after construction. The one shared-state hazard worth stating: the watcher must poke the *deadline*, never call `conn.Close()` — a close racing `request`'s deferred close is a double-close on a conn another goroutine may still be reading.
- **[Concurrency] No findings — shutdown is now safer than before.** A SIGTERM mid-approval previously left the handler parked on the control read until the conn deadline (up to the window plus margin) because nothing watched `ctx.Done()` after the dial; the watcher makes the documented behaviour real. Partial state is not a concern: the approval is resolved daemon-side, and an interrupted client conn is exactly the disconnect `watchApproveConn` already maps to a fixed-message deny.
- **[Threat model alignment] No findings.** This is a CLI/control-socket ticket with no relay-plane surface: no new wire format, no crypto, no device gating, nothing reaching `internal/relay` or `protocol-mobile.md` § Security model. The permission-bypass threat it sits nearest is covered by the fail-open finding above.
- **[Tokens / File operations / Subprocess / Cryptographic primitives] Not applicable by design.** The ticket creates no credential, touches no filesystem path (the socket path is resolved by the untouched `parseClientFlags`), spawns no subprocess, and introduces no randomness or comparison against a secret. The only new stdlib dependency is `context.AfterFunc`.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
