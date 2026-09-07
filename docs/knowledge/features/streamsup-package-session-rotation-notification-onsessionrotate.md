# Session rotation notification — `Config.OnSessionRotate` (#1133)

Closes the gap recorded against this package since #1081 shipped production wiring: the turn-event sink
tagged each event with the runner's *construction-time* `SessionID`, and `RestartFresh` (a stream-mode
`new_session`) rebound the conversation to a fresh id without retagging the Parser — so the drain's
active-session gate (`startStreamTurnDrainV2`, see [Draining turnevents into the interactive
emitter](streamsup-package-draining-turnevents-into-the-interactive-emitter.md)) dropped every event for
that conversation until the daemon restarted. Fail-closed throughout (unmatched tag ⇒ dropped, never
misdelivered) — an availability gap, not a disclosure one.

```go
// OnSessionRotate is called when RestartFresh accepts a rotation, with the id
// it rotated onto.
OnSessionRotate func(newID string)
```

One optional `Config` field, nil-checked at the fire site, mirroring [`OnChildExit`'s
shape](streamsup-package-per-conversation-turn-busy-track-per-child-exit-seam.md). Fires **once per
accepted rotation** — never for the empty id `RestartFresh` itself already refuses with a Warn, since that
early return sits above the fire site.

**Fire site: below the `restartMu` unlock, above the teardown hint and iteration cancel — both halves
matter.** `restartMu` is a documented leaf (`beginSpawn`'s doc): nothing under it may take another lock or
block, and a `Config` callback is an arbitrary consumer-supplied function, so it fires after the unlock
rather than under the leaf. It fires *before* the hint and cancel because the reverse order — cancel
first, notify after — lets the outgoing child's teardown and the successor's spawn race the callback: the
successor could then emit its first events under the still-stale tag, reproducing this exact bug on the
rotation meant to fix it. Rotating the tag first makes "the fresh child's events are never tagged stale"
structural rather than a race the respawn latency happens to win.

**Two windows around the rotation, and they are not symmetric.** Window A (rotate → tag rotation): the
conversation rebinds before the tag moves, inside `startFreshRunner`'s `rotate(oldID)` step, so the
outgoing child's tail is tagged with the old id and dropped — correct, since the client has already
received `session_transition{clear}`. Window B (tag rotation → fresh child bound): the Parser is the
runner's `Config.Stdout` for *every* spawn, so residual stdout from the *outgoing* child parsed after the
tag rotates is forwarded under the fresh id. Accepted as a fidelity bound, not a disclosure one — same
runner, same conversation, same client, same turn that client already sent and was already receiving
deltas for — and left unmeasured and unguarded, per evidence-based fix selection: no failure of this shape
has been observed.

**Consumer: `streamSessionTag` (`cmd/pyry/stream_turn_drain.go`).** An `atomic.Pointer[string]`, not a
mutex — deliberately, since the reader is claude's stdout forwarder goroutine on the per-event path and
the writer is this callback, and a mutex here would put a lock on a path `restartMu`'s leaf rule exists to
keep lock-free. `Config.OnSessionRotate` is no longer this tag's only writer: since #2135,
[`sessionResetFollower`](streamsup-package-announced-reset-follower.md) rotates it too, on the path
claude's own announced `/clear` takes rather than `RestartFresh`'s daemon-driven one. The type's own
doc comment is corrected in the same change rather than left to rot, per the standing rule that a
stale line-cite is worse than a missing one. The two writers are not peers: this callback still
calls the tag's plain, unconditional `Rotate` — it drove its own rotation and already re-keyed the
registry, so it is the authority — while `sessionResetFollower`'s writes became conditional
`CompareAndSwap`s in #2176, because a follower racing this callback must decline rather than
overwrite it. `Rotate("")` is a no-op: the tag can never go empty, because an empty tag matches no bound
session (`boundSessionIDForActive` reports `ok == false` for an empty `CurrentSessionID`) and would
black-hole the conversation for the runner's life — enforced at the tag itself even though
`RestartFresh`'s own empty-id refusal means production never reaches the guard. `sinkFor`/`exitFor` keep
their frozen-tag signatures as one-line delegates over `sinkForTag`/`exitForTag`, so none of the 27
existing call sites changed and the class-aware drop policy still has exactly one implementation.
`newStreamRunnerFactory` mints one tag per runner and binds it to both fan-in lanes and to this field in
three adjacent lines — see [Constructing a
streamRunner](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md).

**Testing note: a factory-tier assertion of this field by inspection isn't reachable.** The factory returns
`sessions.Runner` over an unexported `*streamsup.Runner`, so the `streamsup.Config` it built cannot be read
back to check `OnSessionRotate != nil` — the same limitation `TestSessionParser_MintsOneStablePostureGate`
already hit for `Config.Stdout`. `TestStreamRunnerFactory_RestartFreshRetagsInstalledLanes` proves it
behaviourally instead: drive a real spawned child through the real factory, read the pre-rotation envelope
off the fan-in, call `RestartFresh`, and assert the child-exit envelope carries the rotated id. That is
also the only test that exercises the exit lane at all, since nothing else fires a `Config` field that
only a real spawn reaches.

See [`1133-rotate-stream-turn-sink-tag.md`](../../specs/architecture/1133-rotate-stream-turn-sink-tag.md)
for the full design and security review.
