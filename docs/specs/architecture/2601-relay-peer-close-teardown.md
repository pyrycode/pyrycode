# #2601 — Tear down a phone's v2 session on the relay's close notice

## Files read

- `internal/relay/v2session.go` → `handleFrame` — lazily creates a session for an unknown `conn_id` and stamps `lastActivityAt` before decoding; the new branch goes ahead of both.
- `internal/relay/v2session.go` → `closeWith` — the one teardown path (timers, `s.done`, `AttachmentIntake.ReleaseConn`, `replayQueue`, `m.sessions` delete, `m.queues` delete), ending in the binary→relay close publish.
- `internal/relay/v2session.go` → `handleWake` (`wakeIdleTimeout` arm) — the idle-sweep fallback, unchanged.
- `internal/relay/connection.go` → `forwardFrames` — unmarshals every inbound envelope into `protocol.RoutingEnvelope` and forwards it as is, so a frameless `{conn_id, close_code}` reaches `handleFrame` with no transport change.
- `internal/protocol/envelope.go` → `RoutingEnvelope.CloseCode` — doc comment says inbound `CloseCode` is ignored; rewrite it.
- `cmd/pyry/push_wake.go` → `pushWaker.wakeAbsent` — wakes any device absent from `ActiveConns`; covered by its own tests, not touched.
- `internal/relay/v2session_test.go` → `handshakeConnToOpen`, `TestV2Session_IdleTeardown_FullTeardown`, `TestV2Session_IdleChurn_ReturnsToBaseline` — the patterns the new tests mirror.

In-flight overlap: only `origin/feature/449` touches these files, a stale branch of a closed ticket. No dependency.

## Change

Split `closeWith` into a publish-free `teardown(s) bool` (all of today's cleanup, returning false when `s` is already closed) and a `closeWith` that calls it and then publishes the close envelope exactly as before, so no existing caller changes. In `handleFrame`, directly after the empty-`ConnID` guard, an envelope with `CloseCode != 0` goes to a new `handlePeerClose(env)` and returns: it looks the conn up in `m.sessions`, drops the notice silently (debug log) when there is no session, and otherwise logs `v2.peer_close.teardown` with the relay's close code and calls `teardown`. It never decodes `Frame`, never creates a session, never stamps activity and never publishes. It does not check session state beyond `teardown`'s closed guard, so any live state is torn down. A relay that never sends the notice leaves every path as today; the idle sweep remains the fallback. Rewrite the `CloseCode` doc comment in `internal/protocol/envelope.go` to describe the inbound meaning.

## Testing strategy

New tests in `internal/relay/v2session_test.go`, beside the idle-teardown tests, using an unbuffered `Frames` channel so a send is processed by `Run` before the next `ActiveConns` snapshot:

- Two conns opened with `handshakeConnToOpen`; a close notice for one (with a junk `frame` attached) drops it from `ActiveConns` while the other stays; no envelope is recorded beyond the two `noise_resp`s (no close envelope, and no 4421 that decoding the junk frame would produce); after an idle window elapses, still nothing new; after stopping `Run`, `m.sessions` and `m.queues` lack the conn.
- A close notice for an unknown `conn_id` records nothing, leaves `ActiveConns` empty and `m.sessions` without that key.

Existing idle-sweep tests stay green unchanged and cover `closeWith`'s publish half.

"In any session state": a session only persists in `V2StateOpen` (the handshake states either reach open or close within one frame), so the open-session test is the reachable case; the branch has no state check to exercise beyond `teardown`'s closed guard.

## Documentation handoff (pending — documentation stage)

- `docs/protocol-mobile.md` § Routing envelope (binary↔relay leg): define the relay→binary close notice — a `{conn_id, close_code}` envelope with no `frame`, sent when a phone's WebSocket ends; the binary tears down that connection's session without replying; an older binary ignores it and falls back to the idle sweep. Link pyrycode/pyrycode-relay#152.
