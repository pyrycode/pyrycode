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

### Cursor-stamping only needs the ack, not the drain (#1898)

The test above drains all the way to `turn_state{idle}` because proving
delivery of the turn is its whole subject. A sibling whose precondition is
narrower — only that `sessionRouter.Route`'s success path has stamped
`activeConversation` — doesn't need that drain: `handlers.SendMessage` calls
`Route` and only *then* enqueues and acks, so an observed ack is already
proof the cursor is non-empty. #1898's attachment-upload e2e (which needs a
routed conversation before it can upload, not a completed turn) stops at the
ack for exactly this reason, saving the drain's ~20s of deadline for an
assertion that ticket never makes. Any e2e that needs the cursor stamped as
a *precondition* — rather than as the thing under test — should copy this
shorter stop, not the full drain.

### `relay_v2_stream_model_list_test.go` — `TestRelayV2_StreamModelListReachesConnectedPhone` (#1845)

A spawn-time frame (here, `model_list`) can't be observed by simply
connecting and waiting: the live lane emits once per child spawn, and the
daemon's bootstrap child spawns eagerly at startup — before any test can
pair, dial and handshake. Proving delivery means making a *second* spawn
happen while a client is already connected.

Two routes force that second spawn. A child kill + respawn keeps the
runner's construction-time session id, so the drain's active-session gate
(`boundSessionIDForActive` in `cmd/pyry/relay.go`, feeding
`startStreamTurnDrainV2`) still matches and lets the fresh child's events
through. `driveModelListRespawn` is built on `killChild` +
`waitForRunnerStatus` — the first helper in this package to combine a
phone-side frame observation with a kill.

**A `new_session` rotation used to be the route that couldn't reach a
phone-side frame here — #1133 closed that gap, and this helper was never
revisited to use it.** Before #1133 a rotation rebound the conversation to a
new id while the runner's sink tag stayed on the outgoing one, so the gate
dropped every event the fresh child produced; `relay_v2_stream_new_session_test.go`
asserted the post-rotation child's stdin instead of a phone-side frame for
exactly that reason. `RestartFresh` now moves the sink tag with the rotation
(see [streamsup-package.md § Session rotation
notification](streamsup-package-session-rotation-notification-onsessionrotate.md)),
so a rotation-driven respawn's frames now reach the gate as themselves. Kill
+ respawn still works and nothing forced this helper to change, but a future
reader should not treat "a `new_session` rotation can't produce an
observable phone-side frame" as still true.

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

### `relay_v2_stream_slash_command_list_reconcile_test.go` — `TestRelayV2_StreamSlashCommandListReachesLateConnectingPhone` (#2009)

The inverse of the sibling directly above, built on `relay_v2_stream_model_list_reconcile_test.go`'s
two-conn sequencing (#1868) but needing a **third** conn — not a stylistic choice, but forced by
`fakephone.Client.ReceiveBytes`'s documented close-on-timeout semantics (see
[fakephone-harness.md § Library trade-off](fakephone-harness.md)): proving an absence means running
a window to its deadline, and the conn that does that is dead afterward, so it cannot also be the
conn that mints the conversation the way the model-list twin's phone A does double duty. Phone-empty
handshakes while the registry is still empty and is asserted to receive nothing (and to complete its
handshake normally regardless); phone-a mints and drives a turn; phone-b connects only afterward and
is the sole conn read for the frame.

**`listsOnMinter` converged on 1 here, not the 0 the model-list twin's original prediction stated —
and that divergence is itself informative rather than a discrepancy to chase.** The mechanism is the
same unordered race #1868 found (see
[v2-session-manager.md's slash-command-list reconcile section](v2-session-manager.md)): whether the
live lane also delivers to the minting conn depends on ack-parse-vs-cursor-stamp ordering, which this
variant's own mutation run resolved as 0-or-1 rather than always-0. The value is logged, never
asserted, for the same reason as its twin — but the AC-4 mutant run is what made the divergence
visible: the mutant read `minter=1, observer=0`, which is the two producers of this frame told apart
by data rather than assumed apart by code reading.

**A dead conn's discarded closure is a compile-time guard, not a comment.** Phone-empty's
seal-and-send closure is thrown away after its window closes, the same as phone-b's is in the
model-list twin — so a later edit that tries to reuse either conn fails to build instead of failing
as a decrypt error that reads like a daemon bug. Worth copying into any future third-conn design in
this family: discard the closure, don't just note in prose that the conn shouldn't be reused.

### `relay_v2_stream_background_task_roster_reconcile_test.go` — `TestRelayV2_StreamBackgroundTaskRosterReachesLateConnectingPhone` (#2080)

Structurally #2009's twin (same three-conn sequencing, same nil-seam mutant as the acceptance
criterion), with one precondition the prior two reconciles didn't have: `model_list` and
`slash_command_list` both ride claude's `initialize` control reply, which fakeclaude already
answers unconditionally. A background-task roster has no such source — it is claude's mid-turn
`system`/`background_tasks_changed` line, which stream mode never emits — so this ticket had to add
a rider (`PYRY_FAKE_CLAUDE_STREAM_ROSTER`) before the reconcile could be proven at all. Any future
frame in this family whose live-lane source isn't part of `initialize` should expect to pay the same
cost: check what claude event produces it before assuming the fake can already speak it.

**The spawn-time "minter counter is an unordered race" finding does not transfer here.**
\#1868/#2009 measured `listsOnMinter` as a genuine 0-or-1 race, because that family's live-lane
producer fires at spawn from the `initialize` reply, racing the cursor stamp `sessionRouter.Route`
sets from the first turn. This frame's live-lane producer fires **mid-turn**, off the very turn
whose routing already stamped the cursor — so `rostersOnMinter` converged on a deterministic **1**
on both the green run and the mutant run here. Still logged, never asserted, for the same reason as
its twins (asserting it would redden this spec for a change in a lane it doesn't own) — but don't
assume a new roster-family reconcile inherits the race just because its predecessors had one; check
whether its emitter fires at spawn or mid-turn first. See
[the family overview](v2-session-manager-state-machine-connect-time-background-task-roster-reconcile-retain.md)
for the fuller mechanism.

**Row 0 verbatim isn't enough to prove "tasks intact" — attribution across every row is.** The
twins' single-row-focus assertion (a spawn-time list has no per-row identity to duplicate) doesn't
carry over to an aggregate whose rows are independently identified: a payload that duplicated one
row eight times, or that spliced rows from two different rosters, would satisfy a row-0-verbatim
check and a row-count check both. The test's row loop also asserts every row after the first carries
the rider's synthetic id prefix and that no id repeats — the one addition this spec made beyond its
own plan, and the one that actually backs the acceptance criterion's "with its tasks … intact"
clause rather than a payload that merely has the right shape and length.

### `relay_v2_stream_session_facts_test.go` — `TestRelayV2_StreamSessionFactsReachesConnectedPhone` (#2315)

Proves `session_facts` reaches a connected phone from a fake claude's `system`/`init`
line — the fake wrote no form of that line before this ticket, so the rider
(`PYRY_FAKE_CLAUDE_STREAM_SESSION_FACTS`, see
[fakeclaude-binary-stream-json-mode.md § Session-facts init line rider](fakeclaude-binary-stream-json-mode.md))
ships in the same commit as the spec. One fed line necessarily produces two frames —
`emitInitLine` decodes it once and calls both `emitModelAnnounced` and
`emitSessionFacts` — so the drain filters by frame type rather than asserting a frame
total, the same discipline the roster reconcile's row loop uses for a wire that isn't
the only thing on it. Paired against a rider-off run receiving zero frames of either
kind, the non-vacuity guard this family's rate-limit ancestor established.

**A frame-arrival assertion that decodes into the payload type cannot prove non-leak —
hold the raw bytes too.** `SessionFactsPayload` declares four keys; decoding a wire
frame into that struct silently drops every key the struct doesn't declare, which is
exactly the property under test here — the fed line carries 24 keys, four of them
naming the operator's filesystem or claude's session identity, and none of the other
twenty-one may reach the client. The spec's self-review caught this as the plan's one
MUST FIX: a version asserting only the three expected values would go green on a frame
that had grown a `cwd`. Generalizes past this ticket — any e2e proving "frame X carries
fact Y and nothing else" needs the raw payload's key set asserted beside the decoded
values, not the decoded struct alone.

### `RestartStreamInteractiveWithRelay` and the dormant-reset specs (#2521)

A restart-shaped spec needs a *second* daemon on the *same* `HOME`, and the
plain constructor is wrong for that second call: `seedBootstrapRegistry`
writes a fresh `sessions.json` holding only the bootstrap, so a restart
driven through `StartStreamInteractiveWithRelay` silently deletes every
per-conversation session the first daemon minted, while `conversations.json`
is untouched by either constructor — the daemon comes up, the conversation
resolves, and only its session is gone. The first run of
`relay_v2_stream_new_session_dormant_test.go` failed on exactly that.
`RestartStreamInteractiveWithRelay` is the same constructor with the seed
skipped; `registrySeed` (`seedFreshRegistry` / `keepExistingRegistry`) is a
named `bool` rather than a second bare one beside `stdioPermissionPrompt`,
so a transposed call fails to compile instead of silently re-seeding the
registry a restart spec exists to preserve.

Two specs drive the [`new_session` dormant reset](v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md#dormant-reset-previously-used-no-live-child-2521)
end to end, sharing an `awaitConversationTransition` helper so the two routes
required to behave identically (#2456) are asserted by one piece of code
rather than two that can drift: `TestRelayV2_StreamNewSessionDormant...`
restarts the daemon after one conversation has run, sends a **named**
`new_session` before any message, and proves the first post-reset turn uses
the fresh id; `TestRelayV2_StreamClearAfterDaemonRestartResetsDormantConversation`
pins the same outcome for a typed `/clear`, restarting and clearing before
any other message.

**"The retired id appears nowhere in the spawn line" is the wrong assertion
for "the rotation used the fresh identity."** The per-session `--settings`
and `--append-system-prompt-file` paths are named for the id the session was
*built* under, and a rotation does not re-key them — asserting their absence
would report every correct spawn as a violation. Match the id flags instead
(`--session-id`, `--resume`): the id reaching claude as an identity is what
"cannot resume the retired transcript" actually forbids.

**A transcript-existence probe looks like the natural "has this conversation
ever run?" signal and is a trap in this harness specifically.** The stream
path deliberately watches no `<uuid>.jsonl` (#2137), and stream-mode
fakeclaude writes none at all — a transcript-based discriminator could only
have been proven here against a file the test itself planted, which is why
[`Pool.EverActivated`](sessions-package-key-types-reviving-a-dropped-session-pool-revive.md)
reads persisted timestamps instead and needs no filesystem access to prove
in this suite.

### `relay_v2_attachment_destination_test.go` — three attachment-destination flows (#2144)

Proves the property #2143 shipped — the bytes land where the client named the
conversation, not where the follow-active cursor points — over a real Noise
session: a never-messaged conversation's stored attachment resolves for a
following `send_message` (the ack is the assertion, since `resolveAttachments`
only acks when `ResolvePath` found the bytes under the named conversation); an
A-B-A misfile with the cursor left on B; and an unknown conversation refused
before any byte is admitted. It needs its own `StartStreamInteractiveWithRelay`
call rather than riding `relay_v2_attachment_upload_test.go`'s transfer — that
test's own quiet-window AC deliberately runs a receive deadline out to prove an
absence, which is the conn-killing trade-off [fakephone-harness.md § Library
trade-off](fakephone-harness.md) already documents, so nothing durable is left
on that conn to extend afterward.

**A subtree sweep for "nothing here" cannot tell a correct negative from a
broken walker.** Asserting "no attachment file under B" is satisfied
identically by a correct daemon, a mistyped root, a walker that silently
swallows an error, or a B directory that never existed — none of those are
distinguishable from outside. `attachmentFilesUnder` instead sweeps the whole
`conversations/` tree once; the A-B-A flow asserts the single hit it finds
sits under A (the positive hit is the walker's own liveness proof, so "nothing
under B" follows a fortiori), and the unknown-conversation flow — which has no
legitimate hit anywhere — asserts the walk's own visited-file count is
non-zero, so "found nothing" stays a claim about a directory that was actually
walked rather than one the sweep never reached. Any e2e negative shaped as
"this specific subtree is empty" should restate as "the whole tree's one hit
is elsewhere" or "N files visited, 0 matched" instead.
