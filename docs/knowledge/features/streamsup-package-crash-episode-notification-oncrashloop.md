# Crash episode notification — `Config.OnCrashLoop` (#2724)

Closes the gap recorded against `internal/streamsup`'s backoff loop: the supervisor knows within a second
that a conversation's claude child is crash-looping at startup (`streamsup.State` shows `PhaseBackoff` and
a rising `RestartCount`), but until this ticket no client heard anything until msgqueue's `OnGiveUp` seam
gave up on the queued head after its two-minute `GiveUpAfter` window — by which point the queued message
had already been dropped. See [Supervise loop (`Run`) § Crash episode detection](streamsup-package-supervise-loop-run.md#crash-episode-detection-configoncrashloop-2724)
for the `streamsup`-side detector (`crashEpisode`, `Config.OnCrashLoop`). This document covers the
`cmd/pyry` wiring that turns a fired hook into a `session_error{session.child_crashing}` frame.

## Why the hook is hung on `streamTurnSink`, not threaded as a factory parameter

The obvious shape — a new parameter on `newStreamRunnerFactory` — touches that constructor's ten existing
test call sites plus `selectInteractiveRunner`'s three, past what a one-field addition should cost.
`streamTurnSink` is the per-daemon object the factory already captures and binds per-runner lanes from by
tag (the same object `sinkForTag`/`exitForTag` work through for the ordinary event and exit lanes — see
[Session rotation notification](streamsup-package-session-rotation-notification-onsessionrotate.md) for the
sibling late-bound field, `OnSessionRotate`'s consumer). Hanging the new producer there instead keeps every
existing signature untouched:

```go
// on streamTurnSink (cmd/pyry/stream_turn_drain.go)
crashLoop atomic.Pointer[func(sessionID string)]

func (s *streamTurnSink) setCrashLoopNotify(fn func(sessionID string))
func (s *streamTurnSink) crashLoopForTag(tag func() string) func()
```

`crashLoopForTag` returns the closure `newStreamRunnerFactory` assigns to `streamsup.Config.OnCrashLoop`;
it reads the tag **at fire time**, not at bind time, for the same reason `exitForTag` does — `RestartFresh`
rotates the live id, and the conversation lookup downstream has to match the id that's actually live when
the episode is detected, not the one the runner was constructed with.

## Why it has to be late-bound (an atomic, not a constructor argument)

`sessions.New` builds the bootstrap runner — and with it, calls the factory, which calls `crashLoopForTag`
— before `cmd/pyry/main.go`'s `giveUps` channel exists; the channel can only be built after
`sessionErrorQueueSize` and the `sessionErrorNotify` closure are in scope, which happens later in
`runSupervisor`. So the producer the hook should eventually call cannot be known at the point the hook
itself is wired. `setCrashLoopNotify` installs it afterward, and `crashLoopForTag`'s returned closure loads
the pointer **at the moment it fires**, not once at construction — an unset pointer is a silent no-op
(every test sink, and the brief window between pool construction and `main.go`'s `setCrashLoopNotify` call).
The atomic, rather than a mutex, is what makes "install races a fire" well-defined without putting a lock on
a path that must not block (`OnCrashLoop`'s contract, inherited from `OnChildExit`'s). In `main.go` the
install happens before `pool.Run`, so in practice no runner goroutine can fire before it's set — but the
atomic makes that ordering a nice-to-have, not a correctness requirement.

## The producer: `childCrashingNotify`

```go
// cmd/pyry/session_error_v2.go
func childCrashingNotify(ch chan<- giveUpNotice, resolve func(sessionID string) (string, bool), logger *slog.Logger) func(sessionID string)
```

Resolves the live session id to its conversation via `conversationForSession` (`cmd/pyry/relay.go`), then
does a non-blocking send of `giveUpNotice{convID, code: protocol.CodeSessionChildCrashing, reason:
childCrashingMessage}` into the same `giveUps` channel `sessionErrorNotify` (msgqueue's `OnGiveUp` producer)
already sends into. Two outcomes drop the notice, each with a content-free Warn:

- **Session not bound to a conversation** (e.g. an unbound bootstrap session) — `event:
  "session_error.child_crashing_unbound"`, `session_id` only. There is no conversation to attach the frame
  to, so nothing is sent; a crash loop on a session nobody is watching through still needs to be visible to
  an operator at the default log level, which is why this is Warn rather than Debug.
- **Full channel** — the same `event: "session_error.queue_full"` shape `sessionErrorNotify` already logs
  on its own drop.

`childCrashingMessage` is a **fixed package constant**, not composed from anything the hook receives —
`OnCrashLoop` itself carries no arguments, so there is nothing to compose from. That structural absence is
what guarantees the frame's `message` can never carry the child's stderr, its argv, or queued text, without
relying on a convention a future edit could break.

## `giveUpNotice` gains a `code` field

```go
type giveUpNotice struct{ convID, code, reason string }
```

Before this ticket the struct had no code at all — `sessionErrorEmitterV2.broadcast` stamped the single
fixed `CodeSessionBlocked` for every notice, because there was only ever one producer. With a second
producer now feeding the same channel, each stamps its own constant at its own send site
(`sessionErrorNotify` sets `CodeSessionBlocked` explicitly where it used to rely on `broadcast`'s default;
`childCrashingNotify` sets `CodeSessionChildCrashing`), and `broadcast` just forwards `n.code` into
`SessionErrorPayload.Code` unchanged. Neither producer takes the code from its caller — msgqueue's
`GiveUpFunc` signature and `OnCrashLoop`'s are both unchanged by this, so the code is a property of *which
producer fired*, not of anything that crossed either seam.

## Production wiring (`cmd/pyry/main.go`)

Immediately after `blocked := sessionErrorNotify(giveUps, logger)`:

```go
streamSink.setCrashLoopNotify(childCrashingNotify(giveUps, func(sid string) (string, bool) {
    return conversationForSession(convReg, sid)
}, logger))
```

— before `pool.Run`, alongside the comment explaining the construction-order chicken-and-egg (the sink was
captured by the factory before this channel existed). `newStreamRunnerFactory` sets
`scfg.OnCrashLoop = sink.crashLoopForTag(tag.ID)` right next to `scfg.OnChildExit`/`scfg.OnSessionRotate`,
reading off the same live tag the exit lane uses — see [Constructing a
streamRunner](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md).

## What this does not touch

`msgqueue`'s `OnGiveUp` seam, its two-minute `GiveUpAfter` window, and `CodeSessionBlocked`'s terminal
meaning are all unchanged — this ticket adds a second, independent, faster-firing producer into the same
fan-out rather than changing the existing one. A conversation can still receive both frames for one
incident: `session.child_crashing` within seconds, and `session.blocked` afterward if delivery never
recovers.

## Testing notes

- `crashEpisode.observe`'s table (N−1 fast → no fire, Nth → fire, slow exit mid-sequence resets the count)
  lives with the detector; see the supervise-loop doc linked above.
- `broadcast`'s existing tests asserted the literal `CodeSessionBlocked`; one case now drives the new code
  through to confirm `broadcast` forwards whatever `n.code` carries rather than stamping a constant itself.
- `childCrashingNotify`: resolved session → exactly one notice with `CodeSessionChildCrashing` and
  `childCrashingMessage`; unresolved → nothing sent; full channel → does not block.
- `crashLoopForTag`: unset → no-op; set → called with the tag's *live* id, including after a rotation.
- No end-to-end test drives a crashing fake claude through the whole factory-to-broadcast path: the factory
  line is one assignment and each half (the detector, the producer) is covered on its own.
