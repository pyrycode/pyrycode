# #137 — Resize wire message + server applier (consumes `Bridge.Resize`)

## Files to read first

- `internal/control/protocol.go:11-60` — `Verb` consts, `Request`, `AttachPayload`. Add `VerbResize`, `ResizePayload`, and `Request.Resize` here. Lines 49-55 are the Phase-0 caveat being rewritten by AC#3.
- `internal/control/server.go:283-314` — `handle` and its switch dispatch. Add a `case VerbResize` branch that delegates to a new `handleResize` method.
- `internal/control/server.go:389-401` — the existing handshake-resize block in `handleAttach`. The new `handleResize` follows the same swap/clamp/swallow pattern; reuse the same posture (silent on `ErrAttachUnavailable`, log-and-continue on other seam errors).
- `internal/control/server.go:432-442` — `clampUint16`. Reuse verbatim; no need to duplicate.
- `internal/control/server_test.go:20-75` — `fakeSession` (already records `resizeCalls`) and `fakeResolver`. Both reusable as-is for the new tests; no new test infra required.
- `internal/control/attach_test.go:286-431` — handshake-geometry tests. The new resize tests mirror their structure (server stand-up, fake session, assert against `recordedResizeCalls()`).
- `internal/control/attach_test.go:746-864` — `TestAttach_ClientSendsSessionID` and `TestAttach_EmptySessionIDOmittedOnWire` — the pattern for asserting raw wire bytes / client-side request shape via a hand-rolled `net.Listen`. The `SendResize` round-trip test follows the same shape.
- `internal/control/client.go` (whole file, ~100 lines) — `Status`, `Stop`, `Logs`, and the shared `request()` helper. Add `SendResize` next to them, reusing `request()`.
- `internal/supervisor/bridge.go:242-271` — `SetPTY` / `Resize`. The seam this ticket consumes; no changes to `Bridge` itself.
- `internal/sessions/session.go:156-169` — `Session.Resize` and the `ErrAttachUnavailable` swallow contract. The handler treats foreground sessions the same as the handshake path does.
- `internal/control/attach_client.go:22-27` — the caveat that **stays** (it covers the client-side SIGWINCH emitter, which #133 lands).
- `docs/specs/architecture/136-bridge-resize-seam.md` (whole spec) — the Bridge.Resize seam this ticket consumes. Reread the "Concurrency model" and "Error handling" sections; the resize-applier inherits both contracts unchanged.

## Context

The `Bridge.Resize(rows, cols uint16) error` seam landed in #136 along with handshake-time application. Two pieces of the live-resize story remain:

1. **Wire** — the control protocol has no message that carries `(rows, cols)` from client to server while attached.
2. **Applier** — the control server has no path that decodes such a message and calls the seam.

This ticket lands both halves. The third piece (client SIGWINCH handler that emits the message from `pyry attach`) is deferred to #133.

The Phase-0 caveat at `internal/control/protocol.go:49-55` documents the "live SIGWINCH propagation while attached is still out of scope here" gap. After this ticket: the gap is closed on the wire and on the server. The remaining caveat is on the client (`attach_client.go:25-27`), cleared by #133.

## Design

### Wire shape: side-channel verb on a fresh connection (chosen over framed escape)

A new one-shot verb `VerbResize` carried by a new `ResizePayload` field on `Request`. The client dials a fresh control socket connection per resize event, sends one JSON request, reads one JSON ack, closes. **Same lifecycle as `VerbStatus` / `VerbStop` / `VerbLogs`** — no persistent control channel, no second long-lived conn alongside the attach.

```go
// internal/control/protocol.go
const VerbResize Verb = "resize"

type Request struct {
    Verb   Verb           `json:"verb"`
    Attach *AttachPayload `json:"attach,omitempty"`
    Resize *ResizePayload `json:"resize,omitempty"` // populated for VerbResize
}

// ResizePayload carries a live window-size update for an attached session.
// SessionID resolution mirrors AttachPayload — empty selects bootstrap, full
// UUID or unique prefix selects a specific session. Cols/Rows are wire ints
// for symmetry with AttachPayload; the server narrows + swaps at the seam
// boundary. Either dimension being zero is the "unknown / don't touch"
// sentinel — no resize is issued (same rule as the handshake path).
type ResizePayload struct {
    SessionID string `json:"sessionID,omitempty"`
    Cols      int    `json:"cols,omitempty"`
    Rows      int    `json:"rows,omitempty"`
}
```

#### Why side-channel, not framed escape

The AC presents two options. The trade-off is concentrated:

| Concern | Side-channel one-shot conn (chosen) | Framed escape in byte stream |
|---|---|---|
| Raw byte purity | Untouched. `Bridge.Read` returns input bytes verbatim. | Bridge input pump becomes a byte-by-byte demux state machine; an escape byte (0xFF or similar) needs in-stream escaping for legitimate occurrences. |
| Public surface change | None. `Bridge.Attach(in, out)` contract unchanged. | Bridge.Attach grows a frame decoder, OR a wrapper reader is introduced upstream. Either reshapes the public boundary recently stabilised by #136. |
| Decoding-error robustness (AC#2) | **Structural** — malformed resize JSON only affects its own conn; the attach conn is independent. | Engineered — input pump must distinguish "invalid frame, drop" from "abort attach"; one bug here corrupts the byte stream. |
| Concurrency cost | One handler goroutine per resize, lives ~1ms. | One demux state per attach, lives the whole session. |
| Latency per resize | Unix-socket dial + tiny JSON roundtrip — sub-millisecond, well below SIGWINCH cadence. | Zero (already-open conn). |
| Ordering vs input bytes | Independent. Acceptable — kernel `pty.Setsize` is independent of input bytes anyway; the child sees `SIGWINCH` either way. | Strict ordering with input bytes preserved. Not a real benefit since geometry isn't sequenced relative to keystrokes. |

Side-channel wins on every dimension that matters here. The "extra dial per resize" cost is the only thing framed-escape avoids, and SIGWINCH events from a human resizing a terminal arrive at single-digit Hz at peak — the per-resize Unix-socket dial cost is invisible.

A *persistent* second connection (open at attach time, hold for the duration) was also considered and rejected: it adds a dual-conn lifecycle (coordinated teardown if either side disconnects) for a feature whose absolute event rate doesn't justify it.

### Server applier

A new method on `*Server`, dispatched from the existing `handle` switch. Mirrors `handleAttach`'s resolve-then-lookup-then-seam shape minus the connection-handoff machinery:

```go
// internal/control/server.go (new method)

// handleResize serves a VerbResize request. Geometry is best-effort: any
// failure inside the seam (transient EBADF on a closed fd, foreground
// session with no bridge) is logged and the client gets an OK ack. The
// only error responses are pre-seam routing failures (resolver lookup
// failure, missing payload). Decoding errors on the request itself land
// in handle's existing decode-error branch and never reach this method.
func (s *Server) handleResize(enc *json.Encoder, payload *ResizePayload) {
    if payload == nil {
        _ = enc.Encode(Response{Error: "resize: missing payload"})
        return
    }
    id, err := s.sessions.ResolveID(payload.SessionID)
    if err != nil {
        _ = enc.Encode(Response{Error: fmt.Sprintf("resize: %v", err)})
        return
    }
    sess, err := s.sessions.Lookup(id)
    if err != nil {
        _ = enc.Encode(Response{Error: fmt.Sprintf("resize: %v", err)})
        return
    }
    // Zero in either dim is the "unknown / don't touch" sentinel — see
    // ResizePayload omitempty tags. Cols-then-rows on the wire, swapped
    // here to match Bridge.Resize's rows-then-cols (mirroring pty.Winsize).
    if payload.Cols > 0 && payload.Rows > 0 {
        rows := clampUint16(payload.Rows)
        cols := clampUint16(payload.Cols)
        if err := sess.Resize(rows, cols); err != nil &&
            !errors.Is(err, sessions.ErrAttachUnavailable) {
            s.log.Warn("control: resize failed",
                "err", err, "rows", rows, "cols", cols, "session", id)
        }
    }
    _ = enc.Encode(Response{OK: true})
}
```

Switch dispatch in `handle`:

```go
case VerbResize:
    s.handleResize(enc, req.Resize)
```

#### Why OK is returned even when the seam errors

The same posture `handleAttach` already takes for its handshake-geometry block (`server.go:394-401`): geometry is best-effort, the operator gets a `Warn` log, the client gets no signal. Two reasons:

1. The client has nothing useful to do with a "resize failed" response — it can't tell *why*, can't retry sensibly (the next SIGWINCH will re-emit), and the user's terminal already shows the new size locally.
2. Symmetry with handshake. The Phase-0 caveat reads "live resize is missing" — the cure should look like the existing handshake path, not invent a new error contract.

The two cases that *do* return errors (`payload == nil`, `Resolve/Lookup` failure) are pre-seam — they signal a malformed or routing-broken request, which is operator-actionable. The client can log them at debug level and move on.

#### Why we do NOT enforce "session must be currently attached"

A resize can arrive between `pyry attach` and the actual bridge handshake completing, or after a transient detach during a SIGWINCH burst. Requiring `sess.Attached()` introduces a race window with no upside — `Bridge.Resize` already silently no-ops when `ptmx` is nil. Letting the resize through unconditionally is simpler and matches `pty.Setsize`'s own semantics (the kernel doesn't care whether anyone is currently reading the master).

### Client helper

A new exported function next to `Status`, `Stop`, `Logs`, reusing the same `request()` helper:

```go
// internal/control/client.go

// SendResize asks the daemon to apply a window-size update to the named
// session. Empty sessionID selects the bootstrap session. cols/rows are
// the client's local terminal dimensions; either being zero is treated by
// the server as "no change". A successful return means the server received
// and dispatched the request — the seam's own success is best-effort and
// not visible to the client.
func SendResize(ctx context.Context, socketPath, sessionID string, cols, rows int) error {
    resp, err := request(ctx, socketPath, Request{
        Verb:   VerbResize,
        Resize: &ResizePayload{SessionID: sessionID, Cols: cols, Rows: rows},
    })
    if err != nil {
        return err
    }
    if resp.Error != "" {
        return errors.New(resp.Error)
    }
    if !resp.OK {
        return errors.New("control: resize response missing ok flag")
    }
    return nil
}
```

#133 will call this from a SIGWINCH handler installed by `Attach` (the function in `attach_client.go`). Defining it now keeps that ticket trivially small.

### Caveat rewrites

| File:lines | Action |
|---|---|
| `internal/control/protocol.go:49-55` | Drop the "live SIGWINCH propagation while attached is still out of scope here" paragraph and the `Tracked by #137` line. Replace with one sentence: live resize updates are carried by the `VerbResize` request shape (see `ResizePayload`), emitted from the client by the SIGWINCH handler in `pyry attach` (deferred to #133). |
| `internal/control/attach_client.go:25-27` | **Unchanged** — cleared by #133. |

## Data flow

```
Client (e.g. pyry attach SIGWINCH handler — landed by #133)

   user resizes terminal → kernel raises SIGWINCH
            │
            ▼
   handler gets new (cols, rows) via term.GetSize / TIOCGWINSZ
            │
            ▼
   control.SendResize(ctx, sock, sessionID, cols, rows)
            │
            ▼
   dial ──► control socket ──► fresh connection, separate from attach conn
            │
            ▼
   encode Request{Verb: "resize", Resize: {SessionID, Cols, Rows}}

────────────────────────── network boundary ──────────────────────────

Server

   handle(conn): decode Request
            │
            ▼
   case VerbResize → handleResize(enc, req.Resize)
            │
            ├──► resolver.ResolveID(payload.SessionID)
            │
            ├──► resolver.Lookup(id)
            │
            ├──► (Cols, Rows guards: skip if either zero)
            │
            ├──► swap+clamp: rows = clampUint16(Rows), cols = clampUint16(Cols)
            │
            ├──► sess.Resize(rows, cols)              ──► Session.Resize
            │                                                │
            │                                                ▼
            │                                          Bridge.Resize(rows, cols)
            │                                                │
            │                                                ▼
            │                                          ptyMu.Lock; pty.Setsize(ptmx, …); Unlock
            │                                                │
            │                                                ▼
            │                                          kernel: child receives SIGWINCH
            │
            ▼
   encode Response{OK: true}
            │
            ▼
   conn closes (handle's deferred close runs — closeConn stays true)

Client

   decode Response → return nil if OK, else Error string
   conn closed
```

The attach connection (a separate `net.Conn`) is **not touched** by any of this. Decoding failures, routing failures, and seam errors all live entirely on the resize conn and never propagate to the attach conn. This is the structural answer to AC#2's "decoding errors on the resize path do not tear down the attach session" — it is satisfied by the topology, not by error-handling code.

## Concurrency model

Three concurrency surfaces interact:

1. **Per-connection handler goroutines.** The existing `Server.Serve` accept loop spawns one goroutine per incoming connection (`server.go:225-229`). A resize request arrives on its own conn → its own handler. No new goroutine plumbing.
2. **Cross-conn ordering.** A client may have an attach conn open (handler #1, blocked in the bridge input pump) and emit one or more resize conns concurrently (handlers #2…#N). Each handler is independent. Resize handlers complete in ~1ms (one resolver lookup + one `pty.Setsize`); attach handlers live for the session's bound duration.
3. **`Bridge.ptyMu` serialisation.** `Bridge.Resize` holds `ptyMu` briefly across `pty.Setsize` (#136 design). Two concurrent resize handlers contend on this lock, applied in handler-arrival order. Last-write-wins is the only meaningful semantic for window-size; no fairness or ordering guarantee is needed beyond what the kernel provides for `ioctl(TIOCSWINSZ)`.

No new mutexes, channels, or goroutines are introduced by this ticket.

### Race scenarios audited

| Race | Outcome |
|---|---|
| Resize arrives during child restart (`ptmx == nil` between iterations) | `Bridge.Resize` returns nil silently. Handler encodes OK. The next handshake's geometry will reapply when the next iteration's PTY is registered. Acceptable (matches #136). |
| Resize arrives concurrently with `Bridge.SetPTY(nil)` during iteration teardown | `ptyMu` serialises. Either (a) Resize wins the lock and applies to the still-open `ptmx` (`SetPTY(nil)` waits) — `pty.Setsize` succeeds because `runOnce` does `ptmx.Close → SetPTY(nil)` (`#136` ordering), so the fd is closed by the time `SetPTY(nil)` would acquire — meaning Resize observes EBADF on a closed fd in this exact micro-window. The handler logs Warn and returns OK. Or (b) `SetPTY(nil)` wins and Resize observes nil → silent nil. Either way the attach session is unharmed. |
| Two simultaneous resize requests | Each handler takes `ptyMu` in turn. Both `pty.Setsize` calls succeed; the kernel sees the second one as the final state. No data race. |
| Resize for a session that is currently evicted (no claude running) | `Session.Resize` delegates to `Bridge.Resize`; `ptmx` is nil between iterations of an evicted session → silent nil. Handler encodes OK. The next `Activate` + handshake will set geometry afresh. |
| Resize for a foreground-mode session (`s.bridge == nil`) | `Session.Resize` returns `sessions.ErrAttachUnavailable`. Handler swallows (matches handshake path). Handler encodes OK. (Foreground mode has its own SIGWINCH watcher in `winsize.go`.) |
| Malformed JSON resize body | `handle`'s top-level `dec.Decode` fails before the verb switch fires. Existing branch (`server.go:286-289`) encodes `Response{Error: "decode request: ..."}` and closes the conn. The attach conn is independent and unaffected. |

## Error handling

| Scenario | Wire response | Server log | Effect on attach |
|---|---|---|---|
| Decode failure on the resize request body | `Error: "decode request: <err>"` | none (handle's existing path) | none — separate conn |
| `payload == nil` (well-formed JSON, missing `Resize` field for `VerbResize`) | `Error: "resize: missing payload"` | none | none |
| `ResolveID` returns an error (e.g. ambiguous prefix) | `Error: "resize: <err>"` | none | none |
| `Lookup` returns an error (e.g. session removed) | `Error: "resize: <err>"` | none | none |
| Either `Cols` or `Rows` is 0 | `OK: true` | none | none — silent no-op |
| `Cols` or `Rows` > 65535 | `OK: true` | none | clamped to `math.MaxUint16` (silent — see #136 reasoning) |
| `Bridge.Resize` returns wrapped `pty.Setsize` error (e.g. transient EBADF) | `OK: true` | `Warn`: "control: resize failed" with rows/cols/session | none |
| `Session.Resize` returns `ErrAttachUnavailable` (foreground session) | `OK: true` | none (silent — same as handshake) | none |

The only error path that surfaces to the client is "the request itself was unprocessable" (decode / resolve / lookup). Application-level outcomes (zero-dim, clamp, seam error, foreground session) all return OK because the client cannot act on them differently — and a client-visible failure here would discourage future `pyry attach` from emitting resizes confidently.

## Testing strategy

New file: `internal/control/resize_test.go`. The existing `fakeSession` already records resize calls (`server_test.go:60-65`); no test infra changes needed.

| Test | What it pins |
|---|---|
| `TestServer_Resize_AppliesToSeam` | End-to-end: dial → `Request{Verb:VerbResize, Resize:{Cols:120, Rows:40}}` → assert `OK: true` ack and `fakeSession.recordedResizeCalls() == [{Rows:40, Cols:120}]` (cols-rows wire → rows-cols seam swap). |
| `TestServer_Resize_ZeroDimNoOp` | Table-driven `{0,0}, {0,40}, {120,0}` → assert `OK: true` and `len(recordedResizeCalls()) == 0`. The "missing payload" case (`Request{Verb:VerbResize, Resize:nil}`) is its own subtest asserting `Error == "resize: missing payload"`. |
| `TestServer_Resize_UnknownSessionError` | `fakeResolver{lookupErr: errors.New("no such session")}` → assert `Error == "resize: no such session"`, no Resize call. |
| `TestServer_Resize_SeamErrorReturnsOK` | `fakeSession{resizeErr: errors.New("synthetic setsize failure")}` → assert `OK: true` ack, Resize *was* called (seam error swallowed). |
| `TestServer_Resize_ForegroundSessionSilent` | `fakeSession{resizeErr: sessions.ErrAttachUnavailable}` → assert `OK: true` ack and **no** Warn-level log line (use `slogtest`/`slog.NewTextHandler` into a buffer if convenient; otherwise omit the log assertion — the wire ack is the load-bearing observable). |
| `TestServer_Resize_ClampsOversizeDims` | `Cols:70000, Rows:40` → assert `recordedResizeCalls() == [{Rows:40, Cols:65535}]`. (Pins `clampUint16` at the boundary.) |
| `TestSendResize_RoundTrip` | Hand-rolled `net.Listen` (mirrors `TestAttach_ClientSendsSessionID` shape, `attach_test.go:746-820`): client `SendResize(ctx, sock, "abc", 100, 30)` → server reads raw bytes → assert decoded `Request.Verb == VerbResize`, `Resize == &ResizePayload{SessionID:"abc", Cols:100, Rows:30}`. Server returns `Response{OK:true}`; client returns nil. |
| `TestSendResize_ServerError` | Hand-rolled server returns `Response{Error:"resize: synthetic"}` → assert `SendResize` returns the error. |

All tests use the existing `shortTempDir(t)` + `startServer(t, resolver)` helpers from `server_test.go`. Total ~120 lines.

The "decoding errors do not tear down the attach session" requirement (AC#2) is **structural** in this design — the resize conn is independent of the attach conn. A dedicated test would have to stand up a real attach (via `supervisor.NewBridge` as in `TestServer_BridgeAttach`, `attach_test.go:868-923`), open a second conn for malformed JSON, and assert the attach conn still streams. This is plausible but adds ~80 lines of integration scaffolding for a property that's already covered by:
- The handle-level decode-error branch (existing, covered by `TestServer_HandshakeTimeout`'s parallel shape).
- The attach conn's separation (every existing attach test asserts cross-conn isolation incidentally).

**Recommendation: skip the dedicated structural test**; document the property in `handleResize`'s godoc and let `TestServer_BridgeAttach` + the new `TestServer_Resize_*` tests cover both halves. If a regression is ever observed, add the integration test then. (Aligns with CLAUDE.md "evidence-based fix selection".)

## Open questions

1. **Should `SendResize` retry on transient failure?** No — the next SIGWINCH will re-emit. The client should drop the error and let the next event correct it. Document on `SendResize`. (Resolution: leave to `pyry attach`'s SIGWINCH handler — out of scope for this ticket; #133 picks the policy.)
2. **Should `ResizePayload` get a `Force` field for "apply even if zero"?** No. Zero-as-sentinel is consistent with the handshake path (`AttachPayload`). A real terminal never reports 0×N or N×0. If a future client wants to clear geometry, that's a separate verb (`VerbResetGeometry` or similar) and not worth designing for today.
3. **Should the server expose a dedicated `Response.Resize` typed payload (e.g. `{AppliedRows, AppliedCols}` after clamp)?** Not today. The OK ack is sufficient. If a debug surface for "what did the daemon actually apply" is wanted later, it's an additive field.
4. **Latency under bursty SIGWINCH (user dragging window corner)?** Each SIGWINCH = one fresh dial + JSON roundtrip on a Unix socket = sub-millisecond. The user perceives the *kernel-side* `pty.Setsize` cost, not the IPC. If perceptible lag is ever observed, the upgrade path is a persistent control conn alongside the attach — but that's a #200-series ticket, not a Phase-0 concern.
