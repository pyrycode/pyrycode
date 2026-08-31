# `internal/acp` transport + `pyry acp` subcommand (#755, #756, #757, #761, #762, #747, #750, #753, #752, #749, #796, #751, #801, #754)

The **bidirectional transport** for the Agent Client Protocol (ACP,
Zed-stewarded): line-delimited JSON-RPC 2.0 over one `io.Reader` (inbound) plus
one `io.Writer` (outbound), a method dispatch table for host→agent requests, and
an **outbound-request primitive** for agent→client calls. Part of epic
[#600](https://github.com/pyrycode/pyrycode/issues/600) — the `pyry acp`
subcommand that lets an ACP host (e.g. Zed) drive a supervised claude session.
As of #761 the subcommand hosts an **embedded `internal/sessions` pool** and
serves its first real method, [`session/new`](#sessionnew-and-the-embedded-pool-761),
which spawns exactly one supervised interactive claude per ACP session. #762 adds
the two **lifecycle** verbs that address a session the host already opened —
[`session/load`](#sessionload--sessioncancel-762) (resume by id) and
[`session/cancel`](#sessionload--sessioncancel-762) (a notification that resolves
onto the session's supervisor for the future abort keystroke). #747 adds the two
**handshake** methods every connection opens with —
[`initialize`](#initialize--authenticate-handshake-747) (negotiate protocol
version, declare pyry's minimal capabilities) and `authenticate` (a no-op stub). #750 adds the **outbound streaming half** — [`Transport.Notify`](#outbound-notification-primitive-transportnotify-750)
(the write-only sibling of `Call`) and the stateless
[`acpTurnStream`](#outbound-streaming-adapter-acpturnstream-750) sink that turns a
session's neutral turn-event stream into `session/update` notifications. #753
finishes the inbound **control** surface —
[`session/cancel`](#sessioncancel-actuation--modeconfig-pin-753) now *actuates*
the interrupt (`SendEsc`) onto #762's resolution seam, and
[`session/set_mode` / `session/set_config_option`](#sessioncancel-actuation--modeconfig-pin-753)
get defined answers under a **single-mode pin** (ADR 027 Open Item #1, resolved).

ACP is bidirectional. Inbound: the host writes one JSON object per line to the
agent's stdin; the agent replies and streams notifications one JSON object per
line to stdout. Outbound: the agent issues its own request to the client —
`session/request_permission`, the held `session/prompt` return path — and blocks
for the id-correlated response ([`Transport.Call`](#outbound-request-primitive-transportcall-757)).
stderr carries human-readable diagnostics only, **never protocol frames**.

\#755 built the inbound floor: the package, the framing, the dispatch table, the
diagnostics. #756 added the [`pyry acp` subcommand](#subcommand-pyry-acp-756)
that serves this transport over real stdio. #757 added the
[outbound direction](#outbound-request-primitive-transportcall-757): id
generation, a pending-call registry, the blocking `Call`, and routing of inbound
*response* lines to their waiters — replacing #755's response-frame log-and-drop. #761 delivered the **first real ACP method** and the composition root it needs:
[`session/new`](#sessionnew-and-the-embedded-pool-761) over an embedded
`internal/sessions` pool — the subcommand now *drives claude*. #762 added the
[`session/load` + `session/cancel`](#sessionload--sessioncancel-762) lifecycle
routing (resume-by-id and the cancel→supervisor resolution seam); #753 dropped the
**abort keystroke** onto that seam and added the mode/config pin (below). #749 adds
the **main call** — [`session/prompt`](#sessionprompt--the-held-turn-call-749)
delivers the host's content blocks into claude as a user turn and **holds the
in-flight call open** for the whole turn via #765's deferral primitive. #751 closes
that loop: on `TurnEnd` the outbound stream **resolves** the held `session/prompt`
call with its ACP `stopReason` (divergence 1, the event-stream-to-RPC-return join —
see the [streaming adapter](#outbound-streaming-adapter-acpturnstream-750)). #752
built the [permission proxy](#acp-permission-proxy-sessionrequest_permission-752)
(the outbound `session/request_permission` round-trip) and #801
[live-wired it](#permission-proxy-wiring-801) into the session, so divergence 2 is
observable in a live run. #754 is the epic's
[**conformance capstone**](#conformance-capstone-754): one scripted host drives the
whole surface in a single session and asserts the emitted wire shape is the generic
ADR 027 dialect, observing all six divergences composing at once.

Greenfield, stdlib-only package: imports `bufio`, `context`, `encoding/json`,
`errors`, `fmt`, `io`, `log/slog`, `sync`, `sync/atomic` — **no repo packages**.
Deliberately **not** coupled to the mobile-wire envelope types in
`internal/protocol`: ACP requires the JSON-RPC 2.0 shape
(`{"jsonrpc":"2.0","method":…,"params":…,"id":…}`), which is not the mobile
custom envelope, so `internal/acp` defines its own wire types.

## Hard cost invariant (stated, not enforced here)

The package doc comment records it so the constraint stays visible at the
transport layer: **when a later ticket wires claude behind an ACP method,
`pyry acp` MUST drive a real *interactive* `claude` session billed under the
interactive subscription — never the non-interactive `claude -p` path and never
the metered Agent SDK.** #755's transport ships no claude driving and cannot
violate it. **#761 is the first ticket that honours it in code:**
[`session/new`](#sessionnew-and-the-embedded-pool-761)'s `Pool.Create` spawns
`claude --session-id <uuid>` through the tui-driver (no `-p`/`--print`, no SDK),
and a test asserts the spawn argv is exactly the interactive path.

## Concurrency model

- **The read loop is one goroutine.** `Serve` runs the read loop and dispatches
  each handler **serially, inline** on that goroutine, and (since #757) runs
  `routeResponse` to deliver an inbound response. Simplest correct model for a
  single host connection — no handler-scheduling machinery. A slow handler blocks
  the loop; acceptable for the transport floor (no real handlers yet), revisited
  only on an observed need.
- **Outbound `Call` runs on a *separate* caller goroutine** (#757) — it must, or
  it would block the read loop that reads the response it awaits. This makes the
  transport's first cross-goroutine state necessary: the pending-call registry.
  See [Outbound-request primitive](#outbound-request-primitive-transportcall-757).
- **Writer serialization.** All writes go through one unexported
  `writeMessage(v any) error` guarded by a leaf `sync.Mutex` (`writeMu`), using a
  `*json.Encoder` bound to `w` (one compact object + `\n` per `Encode`). The
  mutex was redundant under #755's single-goroutine dispatch but is **load-bearing
  since #757**: a `Call` and an inbound reply can now race on `w`. Both already
  funnel through `writeMessage`, so no change was needed — this is exactly the
  seam #755 pre-built. Leaf lock: never held across a handler call, a read, or
  `pendingMu`.
- **Register/Serve ordering.** An `atomic.Bool` `started` flag: `Serve` sets it;
  `Register` panics if it is set. The handler map is therefore
  **write-once-before-Serve, read-only during Serve** — no map mutex needed.
  Mirrors [`internal/dispatch`](dispatch-package.md#register-before-run-is-enforced-not-advisory).

## Read loop & framing

`Serve` uses a `bufio.Scanner` over `r` with a raised buffer —
`scanner.Buffer(make([]byte, 0, initialBufCap), maxLineBytes)` — reusing
`internal/agentrun/jsonl` sizing (`initialBufCap = 8192`,
`maxLineBytes = 16 << 20`). A scanner (over `json.Decoder`) is chosen because
line-delimited framing **is** the protocol contract: one `[]byte` line per
iteration, consumed directly by the classifier. Between lines `Serve` checks
`ctx.Err()` and stops if cancelled. On `scanner.Err()` (includes
`bufio.ErrTooLong` for a pathological over-long line) it logs the error **kind**
only and returns it wrapped (`fmt.Errorf("acp: serve: %w", err)`) — the stream is
structurally broken. EOF → `Serve` returns nil.

`scanner.Bytes()` is only valid until the next `Scan`. Retention is safe because
`json.Unmarshal` **copies** `json.RawMessage` fields out of the scanner buffer
(so a handler's `params` never aliases the reused buffer); handlers also run
inline before the next `Scan` as a secondary guard.

> **ctx vs a blocked `Read`.** A `Read` already blocked on a quiet reader cannot
> be unblocked by ctx alone (Go limitation) — the ctx check happens between
> frames. In-memory callers terminate naturally at EOF; the subcommand
> ([#756](https://github.com/pyrycode/pyrycode/issues/756)) unblocks a real
> blocked stdin by **closing the reader** on shutdown. It does **not** close
> `os.Stdin` directly (a `Read` parked in-kernel on the non-pollable stdin fd
> can't be woken by `Close` — lesson #78); it bridges real stdin through an
> in-memory `io.Pipe` and calls `pr.CloseWithError` on cancel. See the
> [`pyry acp` subcommand](#subcommand-pyry-acp-756) section.

## Diagnostics discipline

The transport writes **protocol frames only to `w`** and **diagnostics only to
`log`**. It logs *method names, error kinds, and structural facts — never the
line bytes or `params` content*: an ACP request's `params` may carry user prompt
material (mirrors `internal/agentrun/jsonl`'s no-content-logging rule). Handler
errors surfaced as `-32603` log the real detail to stderr and put only a generic
message on the wire.

## Outbound-notification primitive (`Transport.Notify`) (#750)

`session/update` is a JSON-RPC **notification** — a method + params with **no
`id`** and no response — so `Call`'s request/pending machinery does not fit. #750
adds `Notify`, `Call`'s write-only sibling, in a **new file** `internal/acp/notify.go`
(zero edits to `acp.go`, keeping the package conflict-free with the in-flight #765 branch — the same "new outbound concern → new file" precedent as `Call`'s
future `responder.go`).

```go
func (t *Transport) Notify(method string, params any) error
```

- **Marshal `params` first**: nil → the `params` key is omitted; a marshal failure
  returns a wrapped error and **writes nothing** (no partial frame). Then one
  `writeMessage` under `writeMu` — the same leaf lock serialising `reply` and
  `Call`. Adding a third outbound writer is safe by the existing discipline
  (`writeMu` is never held across a handler call or a read).
- **A distinct unexported `notification` wire struct** (`{jsonrpc, method,
  params omitempty}`) is required because `request.ID` is a **non-omitempty
  `uint64`** and would always serialise an `id` — which a notification must never
  carry. `request` cannot be reused.
- Never awaits a response; safe from any goroutine. **Zero new exported types**
  (`Notify` is a method; `notification` is unexported).

The sole caller is the streaming adapter below; the unit tests
(`notify_test.go`) are the contract — a generic-map decode asserts `id` is
**absent** (not `0`), nil-params omission, and no partial frame on marshal
failure.

## Subcommand: `pyry acp` (#756)

The composition root that serves this transport over real stdio ships in
[#756](https://github.com/pyrycode/pyrycode/issues/756) — `cmd/pyry/acp.go`. It
registers the `pyry acp` verb (no flags, no positional args), constructs
`acp.New(os.Stdin, os.Stdout, logger)` and calls `Serve(ctx)`, and exits **zero**
on both EOF (host closes stdin) and SIGINT/SIGTERM. **As shipped in #756** it
registered **no** handlers and drove **no** claude — a bare transport floor.
[#761](#sessionnew-and-the-embedded-pool-761) extended it: `runACP` now stands up
an embedded pool and registers `session/new`, and `serveACP` gained a
`register func(*acp.Transport)` seam (nil ⇒ register nothing, preserving #756's
behaviour). The `io.Pipe` stdin bridge / closer / clean-exit machinery below is
unchanged by #761.

- **Plain stderr logger, no ring buffer.** `slog.NewTextHandler(os.Stderr, …)` —
  deliberately **not** `runSupervisor`'s `control.SlogTee` + `NewRingBuffer`. The
  ACP host captures this subprocess's stderr and keeps its own diagnostics ring;
  there is no `pyry logs` consumer for `pyry acp`, so the tee/ring is dead weight.
- **The blocked-read closer (AC#4).** The ticket's suggested `os.Stdin.Close()` is
  a trap — a `Read` parked in-kernel on the non-pollable stdin fd can't be woken
  by `Close` (lesson #78). The subcommand instead **bridges real stdin through an
  in-memory `io.Pipe`**: a closer goroutine (`<-ctx.Done()` →
  `pr.CloseWithError`) synchronously unblocks the blocked `PipeReader.Read`, and a
  bridge goroutine (`io.Copy(pw, os.Stdin)`) surfaces host EOF as a pipe close.
  `Serve` reads the in-memory pipe, never `os.Stdin` directly.
- **Clean-exit guard is `ctx.Err() != nil`**, not `errors.Is(err, context.Canceled)`
  — the forced-pipe-close path surfaces a wrapped `os.ErrClosed`, so guarding on
  "did we deliberately cancel?" absorbs both shutdown error shapes as exit-0 while
  still surfacing a genuine stream break (only reachable with `ctx.Err() == nil`).

Full detail, concurrency model, and the one-shot bridge-goroutine-leak rationale
in [`codebase/756.md`](../codebase/756.md).

## Conformance capstone (#754)

The epic's **proof-of-correctness capstone** — one new test file,
`cmd/pyry/acp_conformance_test.go`, zero production files
([codebase/754.md](../codebase/754.md)). `TestACPConformance_FullSessionDrive` drives one
scripted ACP host through the **whole surface in a single session** — `initialize` →
`session/new` → `session/prompt` + a scripted turn → a permission round-trip →
`session/cancel` — over the **real** transport against a deterministic fake claude, and
asserts the emitted wire shape is the generic [ADR 027](../decisions/027-acp-mapping.md)
dialect. It observes all six divergences *composing at once*: end-of-turn as the
`session/prompt` **return** (1), permission as a blocking agent→client request (2),
**no** stall/busy/queue frame on the wire (3, 4), no `fs/*`/`terminal/*` in `initialize`
(5), one interactive claude per session (6).

The conformance harness is a **scripted ACP host**: it mirrors `serveACPWithPool`'s
register-closure with the **same handler constructors** (a compile-time-checked mirror),
substituting `dir == ""` (no-ops `streams.start`, disabling the real outbound) and a
`newRecordingDeliverer(false)` (delivery commits, the hold stays held until the scripted
`TurnEnd` resolves it) so a scripted turn and a scripted permission modal can drive the
surface behind a sleeping fake claude. The real fake-claude pool still backs
`session/new`/`session/cancel`, so the interactive-spawn argv proof is genuine. A
**continuous frame classifier** goroutine — the real host read loop — drains the
agent→host stream, routing replies to the test, recording `session/update` notifications,
and **auto-answering** the inbound `session/request_permission` (the continuous drain is
mandatory: the unbuffered `io.Pipe` deadlocks a send-all-then-read host, since the proxy
`Call` and producer `Notify` write asynchronously).

`TestACPConformance_DialectLock` is the **dialect lock** — the drift guard the per-ticket
tests **cannot** be. They compare emitted frames against the `acpbridge.SessionUpdate*` /
`turnevent.*` *constants*, so renaming a constant's *value* to an opencode alias would not
fail them. The lock asserts those constants equal the **literal** ADR 027 strings
(`session/update` discriminants, `stopReason` values, permission-option kinds, the one
`session/request_permission` agent→client method), so any drift of a value fails there.

## Deferred / scope boundaries

- **Live wiring of the permission proxy** — **landed in [#801](../codebase/801.md)** (see
  [Permission-proxy wiring](#permission-proxy-wiring-801) above): the adapter shipped unwired in #752;
  #801 subscribes it to the raw modal-event stream and drives `Handle` per event inside the
  `acpTurnStreams` manager, the ACP twin of the mobile leg's #798 (`interactiveModalEmitterV2`) wiring.
  Remaining sibling deferral: `toolCall` correlation (the gating tool-call id from the turn stream's
  `ToolStart` is still `omitempty`-reserved).
- **Serve-exit pending-call drain** ([#757](https://github.com/pyrycode/pyrycode/issues/757)) —
  `Serve`-exit does not fail-fast blocked `Call`s; the caller's `ctx` liberates
  them. Additive if a later ticket needs it (range `pending`, deliver a
  sentinel). See [Outbound-request primitive § Shutdown](#shutdown).
- **Handler re-entrancy** — whether an inbound handler may itself issue a blocking
  `Call` (it runs inline on the read loop → deadlock) is a T8 consumer concern;
  `Call` documents the "call from a different goroutine" constraint but does not
  enforce it.
- **Concurrent dispatch** — serial-inline chosen for simplicity. Moving dispatch
  to a worker (if a future long-running ACP method must not block inbound
  framing) needs no wire-contract change — the `writeMu` seam already serializes
  concurrent writers. Deferred until observed need.
- **Richer `data` on transport-generated errors** — the `-32700/-32600/-32601`
  frames omit `data`; only handler-returned `*Error` values may carry it. No
  requirement drives a richer transport-side `data` today.


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Exported surface (3 types — under the 5-type sizing line)](acp-package-exported-surface-3-types-under-the-5-type-sizing.md) — \#757 added **one new exported method** (`Call`) but **zero new exported types**: it reuses the existing `*Error` for the mapped error…
- [Classification decision table (the core mechanism)](acp-package-classification-decision-table-the-core-mechanism.md) — `handleLine([]byte)` classifies each line, after trimming JSON's four insignificant whitespace bytes (space/tab/CR/LF). 
- [Outbound-request primitive (`Transport.Call`) (#757)](acp-package-outbound-request-primitive-transport-call.md) — ACP is bidirectional: besides answering host requests, the agent issues its own request to the client. 
- [`initialize` + `authenticate` handshake (#747)](acp-package-initialize-authenticate-handshake.md) — The two methods every ACP connection opens with, before any session exists.
- [`session/new` and the embedded pool (#761)](acp-package-session-new-and-the-embedded-pool.md) — The first ACP method that does real work, plus the composition root it needs: an **embedded `internal/sessions` pool** inside the `pyry…
- [`session/load` + `session/cancel` (#762)](acp-package-session-load-session-cancel.md) — The two **lifecycle** verbs that address a session the host already opened. 
- [Outbound streaming adapter (`acpTurnStream`) (#750)](acp-package-outbound-streaming-adapter-acpturnstream.md) — The **streaming consumer** (epic #600 T6) that turns a session's neutral [`turnevent.Event`](turnevent-package.md) stream into…
- [`session/cancel` actuation + mode/config pin (#753)](acp-package-session-cancel-actuation-mode-config-pin.md) — The inbound **control** surface (epic #600 T9). 
- [`session/prompt` — the held turn call (#749)](acp-package-session-prompt-the-held-turn-call.md) — The **main call** (epic #600). 
- [ACP permission proxy (`session/request_permission`) (#752)](acp-package-acp-permission-proxy-session-request-permission.md) — The **highest-risk seam in the epic** (T8, [ADR 027](../decisions/027-acp-mapping.md) divergence 2).
- [Test surface (`internal/acp/acp_test.go`)](acp-package-test-surface-internal-acp-acp-test-go.md) — Same-package, stdlib `testing` only, `t.Parallel()`, `-race`-clean. 
- [Related](acp-package-related.md) — [`codebase/756.md`](../codebase/756.md) (`pyry acp` subcommand), [`codebase/757.md`](../codebase/757.md) (outbound-request primitive),…
