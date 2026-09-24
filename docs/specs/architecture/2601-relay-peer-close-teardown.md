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

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The boundary is the relay leg. `transport` reads it and `forwardFrames` hands each parsed `protocol.RoutingEnvelope` to `V2SessionManager.handleFrame`. `ConnID` and `CloseCode` come from the relay, which is not authenticated beyond that leg's WSS and is not trusted with plaintext. The notice gives the relay, or anyone who can inject on that leg, no new capability. Today such a party can already end any open session: an envelope for that `conn_id` whose `frame` fails AEAD reaches the 4421 close in `v2session_handshake.go`'s open-state decrypt path and runs the same cleanup. It can also stop forwarding the phone's frames and let the idle sweep reap the session, or close the phone's WebSocket itself. The notice does the same teardown with less noise, and the phone recovers by reconnecting, as it does after any drop. A forged notice for another phone's `conn_id` can therefore only end that session, which is already reachable. A replayed or late notice cannot hit a newer session unless the relay reuses a `conn_id`. Conn ids are relay-assigned per WebSocket, and the one leg keeps envelopes in FIFO order, so a notice cannot overtake frames sent after it. The notice grants no access: it never creates a session, never skips the handshake, and never touches `Devices` or any key material.
- [Trust boundaries] OUT OF SCOPE: a phone must not be able to set `close_code` on the envelope the relay builds for it. The relay builds `{conn_id, frame}` from the phone's bytes and sets `close_code` only when its own WebSocket ends. That rule is enforced in the relay, pyrycode/pyrycode-relay#152, not in this binary. If a phone could set it anyway, it could only end its own session, which it can already do by disconnecting.
- [Frame handling] No findings. The `CloseCode != 0` branch in `handleFrame` runs before the lazy `m.sessions` create and before the `lastActivityAt` stamp, then returns. `handlePeerClose` reads only `ConnID` and `CloseCode`. It never decodes, decrypts or allocates from `Frame`. `TestV2Session_PeerClose_TearsDownWithoutReply` attaches a junk frame and checks that it causes no 4421. `TestV2Session_PeerClose_UnknownConnIgnored` checks that no session is created.
- [Resource release] No findings. `teardown` is `closeWith`'s cleanup moved as is, minus the publish at the end. It sets `V2StateClosed`, stops `rekeyTimer`, `rekeyReplyTimer` and `idleTimer`, closes `s.done` (the app-frame worker), calls `AttachmentIntake.ReleaseConn`, nils `replayQueue`, deletes from `m.sessions`, and deletes `m.queues` under `pushMu`. `closeWith` now calls `teardown`, so the two paths cannot drift apart. The `V2StateClosed` guard makes repeat teardowns no-ops, so a second notice or a later idle sweep cannot close `s.done` twice.
- [Network & I/O] No findings. The inbound envelope size is capped by `maxFrameBytes` on the relay WebSocket in `transport`, unchanged. A notice for an unknown conn costs one map lookup and one debug log line. It allocates nothing that persists, writes nothing to the relay, and a flood of them is no more expensive than a flood of ordinary envelopes.
- [Error messages, logs] No findings. The teardown logs at Info and the unknown-conn case at Debug, each with `event`, `conn_id` and `close_code` only. Neither logs `Frame`, keys or tokens. `slog` escapes the relay-controlled `conn_id` in structured fields.
- [Concurrency] No findings. `handlePeerClose` runs on the `Run` goroutine, like every other `m.sessions` access. `teardown` keeps `closeWith`'s locking unchanged: `m.queues` is touched only under `pushMu`, and no new goroutine is spawned. The app-frame worker exits when `s.done` closes, as on today's close paths.
- [Tokens, File operations, Subprocess, Crypto] Not applicable. The change reads two envelope fields and releases in-memory state. It generates, stores and compares no secrets, touches no files, spawns no processes and adds no crypto.
- [Threat model] No findings. Under `docs/protocol-mobile.md` § Security model the relay is honest-but-curious for confidentiality and fully able to deny service. This ticket gives it no plaintext and no new way to deny service, as the trust-boundary finding shows. A forged notice can cause a spurious push wake, because the phone drops out of `ActiveConns`. The 4421 injection path causes that already, and push content stays under `pushWaker`'s own policy.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24

## Revisions

- 2026-09-24, rework: added `## Security review`. The verifier found the section missing although #2601 carries `security-sensitive`, which was present for the whole build. The review found no MUST FIX and no SHOULD FIX, and the implementation already matches every finding, so no code changes.
