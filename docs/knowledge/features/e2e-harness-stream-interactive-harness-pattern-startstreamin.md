# Stream Interactive Harness Pattern (`StartStreamInteractiveWithRelay`, #1141)

First e2e opt-in to the stream-json `interactive_runner` (#1081 shipped the
production toggle; #1140 shipped the stream-json fakeclaude mode this helper
drives). `StartStreamInteractiveWithRelay` establishes a pattern this file's
existing helpers didn't need: **config-file injection**, not a flag. The
production toggle (`selectInteractiveRunner`, `cmd/pyry/main.go`) reads
`config.Config.InteractiveRunner` off `<home>/.pyry/config.json`
(`resolveConfigPath`), so the helper `os.MkdirAll`s `<home>/.pyry` and writes
`{"interactive_runner":"stream-json"}` as a raw JSON string literal —
*before* spawn, since the daemon reads config once at startup. A raw literal
(not an `internal/config` import) keeps `harness.go` import-lean under its
`e2e || e2e_install` build tag, mirroring `seedBootstrapRegistry`'s existing
raw-JSON convention. A partial config leaves every other field at its
`config.Load` overlay default; `-pyry-relay` still overrides `relay_url`.

Otherwise the helper is `StartRotationWithRelay` minus the sessions-dir /
trigger / stdin-log machinery: `ensureFakeClaudeBuilt` + `seedBootstrapRegistry`
+ `spawnWith` with `PYRY_ALLOW_INSECURE_RELAY=1`, `PYRY_MOBILE_V2=1`,
`PYRY_FAKE_CLAUDE_STREAM_JSON=1`. `ClaudeSessionsDir` is left unset — stream
mode opens no transcript, and the daemon's rotation watcher was confirmed
(empirically, on first green) to tolerate the dir's absence, so the spec's
defensive `os.MkdirAll(claudeSessionsDir(home))` was dropped rather than kept
as a belt line.

### Why the bootstrap pool id and the bound conversation id must be equal

The one non-obvious invariant a caller must get right. The stream turn drain
(`startStreamTurnDrainV2`, #1098) forwards an event only when the event's sink
tag equals `activeSession()`:

- The **sink tag** is the runner's construction-time `cfg.SessionID` — the
  bootstrap **pool id**, pinned by `seedBootstrapRegistry(t, home, initialUUID)`.
- `activeSession()` resolves the active conversation → its bound session id —
  set by `seedBoundConversation(t, home, knownConvID, initialUUID)`.

Both must be seeded with the *same* `initialUUID` before
`StartStreamInteractiveWithRelay` is called. A mismatch drops every event at
the gate: the drain hangs, and the first observable symptom is a timeout
waiting for `assistant_delta` — not a clean failure at the seed call. This is
the single most likely mistake for a new caller (the rider specs #1136–#1139
and the real-claude capstone #1083 all repeat this pairing).

### `relay_v2_stream_send_test.go` — `TestRelayV2_StreamSendMessageDrainsTurn`

The first live, integrated proof of the stream-interactive path: every leg
(config toggle, runner factory, turn drain, fakeclaude stream mode) had been
proven in isolation, but never run together against a real spawned
fakeclaude. The spec pairs, handshakes an interactive phone, drives one
`send_message`, awaits the sealed ack (accept-into-backlog — not delivery:
`streamRunner.WriteUserTurn` can return the retryable `ErrNoLiveChild` while
the child is between spawn and stdin-ready), then drains two ordered
milestones mirroring `relay_v2_interrupt_test.go`'s shape: `assistant_delta`
carrying the **echoed** prompt (the non-vacuity guard — proves the full
round-trip, not just "some text"), then a terminal `turn_state{idle}`. Details
and the full wire diagram: [codebase/1141.md](../codebase/1141.md).

### `relay_v2_stream_model_list_test.go` — `TestRelayV2_StreamModelListReachesConnectedPhone` (#1845)

A spawn-time frame (here, `model_list`) can't be observed by simply
connecting and waiting: the live lane emits once per child spawn, and the
daemon's bootstrap child spawns eagerly at startup — before any test can
pair, dial and handshake. Proving delivery means making a *second* spawn
happen while a client is already connected.

Two routes force that second spawn and only one delivers. A child kill +
respawn keeps the runner's construction-time session id, so the drain's
active-session gate (`boundSessionIDForActive` in `cmd/pyry/relay.go`,
feeding `startStreamTurnDrainV2`) still matches and lets the fresh child's
events through. A `new_session` rotation does not: the rotation rebinds the
conversation to a new id while the runner's sink tag stays on the outgoing
one, so the gate drops every event the fresh child produces (see
`relay_v2_stream_new_session_test.go`, which for exactly this reason asserts
the post-rotation child's stdin rather than a phone-side frame). Building a
frame observation on the rotation route asserts into a lane that is
dropping. `driveModelListRespawn` is built on `killChild` +
`waitForRunnerStatus` instead — the first helper in this package to combine
a phone-side frame observation with a kill.

The gate also has nothing to compare against until a turn has been driven —
`activeConversation.set` is stamped only from `sessionRouter.Route`'s success
path — so the helper drives one `send_message` and waits for `turn_end`
*before* killing the child. That pre-kill turn isn't incidental scaffolding;
it's what makes the post-kill respawn observable at all, and any e2e that
needs a spawn-time frame after the bootstrap child should expect to pay the
same "drive a turn, then force a respawn that keeps the session id" shape.
See § Build Helper for the mutation-testing methodology this spec's AC
required (`PYRY_E2E_BIN` + a control run, not `-overlay` alone).

### `relay_v2_stream_model_list_reconcile_test.go` — `TestRelayV2_StreamModelListReachesLateConnectingPhone` (#1868)

The deliberate inverse of the sibling above: that test forces a *second
spawn* while a client is already connected, because the live lane only
fires at spawn. This one must NOT do that — it proves the connect-time
reconcile (`reconcileModelLists`), so the list has to exist *first* and the
observing client (phone B) has to connect *after*, sending nothing but its
handshake. Two paired phones, driven strictly sequentially: phone A mints a
conversation over the wire and drives one turn (the only way to put a
retained list behind a real conversation record — the bootstrap session has
none), then phone B dials and handshakes, observing the frame its own
handshake's success tail pushes.

**`listsOnMinter` — a diagnostic counter, and the value it converges on is
the opposite of what the original design predicted.** The spec assumed the
live lane could never also deliver the list to phone A (the minting conn),
reasoning that the conversation cursor `interactiveTurnEmitterV2.Handle`
checks is still empty when the `initialize` ack is parsed. Measurement
during code review found otherwise: both are real outcomes across runs. See
[v2-session-manager.md's model-list reconcile section](v2-session-manager.md)
for the mechanism — it's a genuine unordered race between two goroutines,
not a bug in this test. Practical upshot for this file and any sibling:
**a spawn-time-emitter diagnostic on the spawning conn itself cannot be
asserted to either value** — log it, don't assert it, and say so in the
field's doc comment (this file's own comment on `listsOnMinter` still
reads as though zero were guaranteed; the corrected account lives in
v2-session-manager.md rather than in a frozen test file).

**Budget for a first-arrival window has to be sized against the mutant, not
the happy path.** AC-1 here ("a frame arrived") has no positive terminator
the way `turn_end` terminates a turn — the collection loop can only end by
finding the frame or by exhausting its deadline. The green run costs about
1s; the AC-3 mutation run (unwiring `RetainedModelLists`) is honestly red
only at the full ~21s deadline, because there is nothing for it to notice
early. Any test in this family built the same way — assert a frame arrives,
nothing more — should expect its red path to cost its whole budget, and
should size that budget (and CI expectations around it) accordingly rather
than against how fast the test runs when it passes.

### `relay_v2_stream_slash_command_list_test.go` — `TestRelayV2_StreamSlashCommandListReachesConnectedPhone` (#2008)

The direct twin of `relay_v2_stream_model_list_test.go` above, reusing its kill/respawn
shape (`driveModelListRespawn`'s route, `modelListObservation`'s value-not-assertion
collection shape) unchanged. Nothing about the route needed rederiving because
`SlashCommandList` is gated independently of `ModelList` in `streamsup.Parser` —
`emitSlashCommandList` suppresses a zero-length list on its own, unrelated to whether
`models` was present on the same reply — so the two lanes are provably separate proofs,
not one proof standing in for both. The open question this spec carried — whether the
bootstrap child's own `initialize` reply puts a frame on the wire before the pre-kill
turn stamps the active-conversation cursor — resolved as predicted: the RED run measured
`pre-kill slash_command_list=0`, confirming `Handle`'s no-cursor return takes the
bootstrap child's report unconditionally for this lane too, the same as it does for
`model_list`.

**A failure-message shape that's safe for one wire type isn't automatically safe for its
sibling.** The model-list spec's assertion failures dump the whole decoded payload with
`%+v` — harmless there, since a model list is claude-authored. A command's name,
description, argument hint and aliases are WORKSPACE-authored — a lower-trust origin than
claude's own strings (#833's posture), and the production path forwards them unsanitised
by design, with the render boundary owing sanitisation left to the client. A `%+v` dump
in this file's failure messages would be harmless against fakeclaude but would print an
operator's real command strings if the same shape were copied into
`internal/e2e/realclaude`. This spec asserts per field instead, comparing against the
fake's own committed literals by name, and states why in the test's own header rather
than leaving the next reader to infer it from the model-list sibling. #2009 and any
later realclaude sibling proving this lane should copy this file's assertion shape, not
the model-list file's.
