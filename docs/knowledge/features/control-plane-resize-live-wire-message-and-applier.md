# Resize: Live Wire Message and Applier (#137)

`VerbResize` carries a live window-size update for an already-attached
session on a **separate, one-shot control connection** — independent of
the (long-lived) attach conn. Same lifecycle as `VerbStatus` / `VerbStop` /
`VerbLogs`: client dials, sends one JSON request, reads one JSON ack,
closes. See [ADR 009](../decisions/009-resize-wire-shape.md) for why
side-channel beat framed-escape-in-byte-stream.

### Wire shape

```go
const VerbResize Verb = "resize"

type Request struct {
    Verb   Verb           `json:"verb"`
    Attach *AttachPayload `json:"attach,omitempty"`
    Resize *ResizePayload `json:"resize,omitempty"` // populated for VerbResize
}

type ResizePayload struct {
    SessionID string `json:"sessionID,omitempty"`
    Cols      int    `json:"cols,omitempty"`
    Rows      int    `json:"rows,omitempty"`
}
```

`ResizePayload` mirrors `AttachPayload`'s field shape: empty `SessionID`
selects bootstrap; full UUID or unique prefix selects a specific session;
`Cols`/`Rows` are wire ints, narrowed + swapped at the seam boundary.
Either dimension being zero is the "unknown / don't touch" sentinel — same
rule as the handshake path.

### Server applier

`handleResize` mirrors the resolve-then-lookup-then-seam shape of
`handleAttach`'s handshake-geometry block (cf. [§ Attach: Handshake
Geometry](#attach-handshake-geometry-136) above), minus the
connection-handoff machinery:

```go
func (s *Server) handleResize(enc *json.Encoder, payload *ResizePayload) {
    if payload == nil {
        _ = enc.Encode(Response{Error: "resize: missing payload"})
        return
    }
    id, err := s.sessions.ResolveID(payload.SessionID)
    if err != nil { /* encode "resize: <err>"; return */ }
    sess, err := s.sessions.Lookup(id)
    if err != nil { /* encode "resize: <err>"; return */ }
    if payload.Cols > 0 && payload.Rows > 0 {
        rows := clampUint16(payload.Rows)
        cols := clampUint16(payload.Cols)
        if err := sess.Resize(rows, cols); err != nil &&
            !errors.Is(err, sessions.ErrAttachUnavailable) {
            s.log.Warn("control: resize failed", ...)
        }
    }
    _ = enc.Encode(Response{OK: true})
}
```

### Error posture: OK ack even when the seam errors

The same posture as the handshake-geometry block. Geometry is best-effort.
The client gets `OK: true` whenever the request itself was processable;
seam failures (transient `EBADF` on a closed fd, `ErrAttachUnavailable`
from a foreground session) are logged at Warn and swallowed. Two reasons:

- The client has nothing useful to do with a "resize failed" response.
  The next SIGWINCH will re-emit; the user's terminal already shows the
  new size locally.
- Symmetry with handshake. Inventing a new error contract here would mean
  client code has to handle resize failures differently from handshake
  failures, with no operational benefit.

The only error responses are pre-seam routing failures, which signal a
malformed or routing-broken request:

| Failure | Wire `Response.Error` |
|---|---|
| `payload == nil` | `"resize: missing payload"` |
| `ResolveID` returns an error | `"resize: <err>"` (e.g. `"resize: sessions: ambiguous session id: ..."`) |
| `Lookup` returns an error | `"resize: <err>"` (e.g. `"resize: sessions: session not found"`) |

Decode failures on the request body land in `handle`'s existing
decode-error branch and never reach `handleResize`.

### Why "session must currently be attached" is NOT enforced

A resize can arrive between `pyry attach` and the actual bridge handshake
completing, or after a transient detach during a SIGWINCH burst. Requiring
`sess.Attached()` would introduce a race window with no upside —
`Bridge.Resize` already silently no-ops when `ptmx` is nil. Letting the
resize through unconditionally is simpler and matches `pty.Setsize`'s own
semantics (the kernel doesn't care whether anyone is currently reading the
master).

### Decoding errors cannot tear down the attach session

This property (an explicit AC#2 of #137) is **structural**, not coded.
The resize conn and the attach conn are independent `net.Conn`s; a
malformed JSON body, a bogus session id, or a seam error all live entirely
on the resize conn and never propagate to the attach conn. No
error-handling code is needed to honour the contract — the topology
honours it. `handleResize`'s godoc records this for the next reader.

### Concurrency

No new mutexes, channels, or goroutines. Three surfaces interact, all
already in place:

- Per-connection handler goroutines from `Server.Serve`'s accept loop. A
  resize request gets its own conn → its own handler. Handlers complete
  in ~1ms (one resolver lookup + one `pty.Setsize`).
- Cross-conn ordering: an attach conn (handler #1, blocked in the bridge
  input pump) and one or more resize conns (handlers #2..#N) run
  concurrently and independently.
- `Bridge.ptyMu` serialisation: two concurrent resize handlers contend
  briefly on the leaf-only mutex; last-write-wins is the only meaningful
  semantic for window-size, and the kernel's `ioctl(TIOCSWINSZ)` honours
  exactly that.

Race scenarios audited in [#137's spec](../../specs/architecture/137-resize-wire-message.md);
representative cases include resize-during-child-restart (silent nil from
`Bridge.Resize`), simultaneous resizes (both ioctls succeed; second wins),
resize for an evicted session (silent nil; next handshake reapplies), and
resize for a foreground session (`ErrAttachUnavailable` swallowed —
foreground has its own SIGWINCH watcher in `winsize.go`).

### Client helper

```go
func SendResize(ctx context.Context, socketPath, sessionID string, cols, rows int) error
```

Sibling of `Status` / `Stop` / `Logs`, reusing the same `request()` helper
(one-shot dial → encode → decode → close). A successful return means the
server received and dispatched the request — the seam's own success is
best-effort and not visible to the client. Callers (e.g. a SIGWINCH
handler) should not retry on transient failure; the next SIGWINCH will
re-emit.

`SendResize` is defined now and unused until #133's SIGWINCH handler in
`pyry attach` lands. Defining it now keeps that ticket trivially small.

### Caveat status

| File:lines | Status after #137 + #133 |
|---|---|
| `internal/control/protocol.go` | After #137: rewritten to point at `VerbResize`/`ResizePayload`. After #133: parenthetical updated to identify `startWinsizeWatcher` as the producer (no longer "deferred to #133"). |
| `internal/control/attach_client.go:25-27` | After #133: rewritten — drops the Phase-0 "detach and reattach to update" sentence; replaced with one identifying the SIGWINCH watcher as the live-resize producer and `Bridge.Resize` as the seam that applies it. |

### Tests

`internal/control/resize_test.go` (~290 LOC, single new file) covers the
end-to-end server path against the existing `fakeSession` + `fakeResolver`
test doubles (which already record `resizeCalls` from #136 — no test infra
changes). Coverage: applies-to-seam, zero-dim no-op (table-driven over
`{0,0}, {0,40}, {120,0}` plus `nil` payload), unknown-session error,
seam-error-returns-OK, foreground-session-silent (`ErrAttachUnavailable`
swallowed), oversize-dim clamp at the boundary, plus two
`SendResize` round-trip tests (handshake server stand-up vs. error wire
string) using the same hand-rolled `net.Listen` shape as
`TestAttach_ClientSendsSessionID`. The structural "decoding errors do not
tear down the attach session" property is intentionally **not** unit-tested
through a dedicated integration test; it falls out of conn independence
and is documented in `handleResize`'s godoc rather than asserted.
