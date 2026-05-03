# ADR 009 — Live-resize wire shape: side-channel verb on a fresh connection

**Status:** Accepted (ticket #137)

## Context

#136 landed `Bridge.Resize` as the supervisor-side resize seam and applied
client geometry at attach handshake time. The remaining piece for keeping
the supervised PTY's size in sync with the client terminal across a session
is **live SIGWINCH propagation while attached**: the client must be able to
deliver a `(rows, cols)` update to the server mid-attach without tearing
the attach down.

The acceptance criteria for #137 explicitly listed two candidate wire
shapes and asked the architect to pick:

1. **Side-channel verb on the JSON channel** — a new `VerbResize` request
   on a fresh control connection, separate from the attach conn.
2. **Framed escape inside the byte stream** — multiplex resize events into
   the existing attach byte stream using an escape byte and unescaping on
   both ends.

Either is acceptable; the AC asked for the trade-off to be recorded.

## Decision

Side-channel verb on a fresh connection. `VerbResize` carries a
`ResizePayload{SessionID, Cols, Rows}` on a one-shot dial → encode → decode
→ close exchange. Same lifecycle as `VerbStatus` / `VerbStop` / `VerbLogs`.

Rejected: framed escape in the attach byte stream. Also rejected: a
*persistent* second connection held open alongside the attach (a halfway
house between the two AC options).

## Rationale

The trade-off concentrates on five axes:

| Concern | Side-channel one-shot conn (chosen) | Framed escape in byte stream |
|---|---|---|
| Raw byte purity | Untouched. `Bridge.Read` returns input bytes verbatim. | Bridge input pump becomes a byte-by-byte demux state machine; an escape byte (0xFF or similar) needs in-stream escaping for legitimate occurrences. |
| Public surface change | None. `Bridge.Attach(in, out)` contract unchanged from #136. | `Bridge.Attach` grows a frame decoder, OR a wrapper reader is introduced upstream — reshaping the public boundary recently stabilised by #136. |
| Decoding-error robustness (AC#2) | **Structural** — malformed resize JSON only affects its own conn; the attach conn is independent. | Engineered — input pump must distinguish "invalid frame, drop" from "abort attach"; one bug here corrupts the byte stream. |
| Concurrency cost | One handler goroutine per resize, lives ~1ms. | One demux state per attach, lives the whole session. |
| Latency per resize | Unix-socket dial + tiny JSON roundtrip — sub-millisecond, well below SIGWINCH cadence. | Zero (already-open conn). |

Side-channel wins on every axis that matters here. The "extra dial per
resize" cost is the only thing framed-escape avoids, and SIGWINCH events
from a human resizing a terminal arrive at single-digit Hz at peak — the
per-resize Unix-socket dial cost is invisible under that workload.

A *persistent* second connection (open at attach time, hold for the
duration of the session) was also considered and rejected: it adds a
dual-conn lifecycle (coordinated teardown if either side disconnects) for
a feature whose absolute event rate doesn't justify the complexity.

The cross-conn ordering question — "do resize events ordered relative to
input bytes matter?" — has a clean negative answer. The kernel's
`ioctl(TIOCSWINSZ)` is independent of input bytes; the child sees
`SIGWINCH` either way. Strict ordering with input bytes is not a real
property to preserve.

## Consequences

- **AC#2 ("decoding errors on the resize path do not tear down the attach
  session") is satisfied by the topology, not by error-handling code.**
  Two `net.Conn`s, two independent handler goroutines. Documented in
  `handleResize`'s godoc; not unit-tested through a dedicated integration
  test, because the property falls out of conn independence and the unit
  shape would add ~80 lines of scaffolding for ground already covered by
  the rest of the suite (cf. CLAUDE.md "evidence-based fix selection").
- **`SendResize` joins `Status` / `Stop` / `Logs` as a one-shot client
  helper.** Reuses the existing `request()` helper. No new client lifecycle
  surface; trivially callable from a SIGWINCH handler.
- **No new goroutines, mutexes, or channels.** The accept loop already
  spawns one goroutine per incoming connection; resize handlers drop into
  that pattern unchanged.
- **`Bridge.Attach` contract is unchanged.** Future tickets can revisit
  the wire shape without disturbing the bridge surface.
- **Upgrade path if perceptible lag is ever observed under bursty
  SIGWINCH** (e.g. user dragging a window corner): a persistent control
  conn alongside the attach is the natural escalation. Not designed for
  today; deferred until evidence.

## Related

- ADR 008 — `Bridge.Resize` is the supervisor-side resize seam (the API
  this verb consumes).
- `docs/knowledge/features/control-plane.md` § Resize: Live Wire Message
  and Applier (#137) — the consumer-side documentation.
- `internal/control/attach_client.go:25-27` — the remaining caveat about
  the client-side SIGWINCH handler, cleared by #133.
