# Posture gate — hold turns until claude acks the spawn-time mode (#2064)

Every spawn that writes a permission-mode `set_permission_mode` request now also arms a
`PostureGate` (`runner.go`, beside the rotation gate it is a sibling to) on the `request_id`
it minted for that write. `WriteUserTurn` refuses with the existing retryable
`ErrNoLiveChild` — zero bytes written — until a `control_response` carrying that exact id and
a `success` subtype releases it. This is the first slice in the package that *reads* a control
ack; `Interrupt`, `RequestInitialize` and `SetPermissionMode` mint an id and never look at the
reply (see [content-blocks-are-held-as-json-rawmessage.md § Permission-mode send
primitive](streamsup-package-content-blocks-are-held-as-json-rawmessage.md)). The correlation
decodes the **same top-level line bytes** `consumeLine` already switches on, into a second,
separate target (`controlAckLine`, holding only `subtype`/`request_id`) — not a field added to
the existing `controlResponseLine` used for `#1811`'s model-list rung, because a `request_id`
that isn't a JSON string would otherwise fail that whole-line decode and move a genuine
model-list line to the undecodable rung. `noteControlAck` runs above `emitModelList` and
changes which of that arm's five outcomes a line lands on **not at all** — see [Send half —
`WriteTurn`](streamsup-package-send-half-writeturn.md).

A `bypassPermissions` spawn, or a spawn whose own argv already names
`--dangerously-skip-permissions`, writes nothing and installs the **OPEN** state
unconditionally rather than skipping the gate call — `arm("")` is the zero value, and calling
it only inside the "should write" branch turns out to matter (below).

## Three things that would have shipped wrong

**A gate armed only inside the write branch bricks the one session AC 1 says must never be
gated.** The first cut nested `postureGate.arm(id)` inside `if permissionModeAllowed(mode)`, so
a spawn that wrote nothing installed *nothing* rather than OPEN. The gate outlives the child —
`Restart` reuses the same `*Runner` and `*PostureGate` — so an un-acked `default` child
followed by an operator escalation to `bypassPermissions` left the replacement child armed on
its predecessor's id, which nothing could ever ack: every turn on that session refused forever
under the *retryable* classification. Both pre-fix spawn tests judged the bypass case against a
freshly-constructed gate, where OPEN is the zero value and the assertion was vacuous by
construction — a residual-arm test (drive the sequence with the first gate deliberately left
unreleased) is what catches this shape; a fresh-gate test cannot. Fix: hoist the `arm` call out
of the branch, leave only the *id* conditional.

**A fail-closed gate needs a working exit, not just a closed door.** `arm`'s only production
call site is a spawn; `release`'s only one is a `success` ack. A NAK left nothing to open the
gate: the child is healthy (nothing respawns it), and the documented operator remedy — change
the mode again — is in-band-deliverable for every in-band mode, so it never restarts and never
re-arms. `PostureGate` now records a NAK as *refused* (a bool beside the armed id, cleared by
`arm`/`release`/`retarget`) so the turn refusal logs a distinct Warn instead of repeating "not
yet confirmed" at Info forever, and `SetPermissionMode` **re-targets a closed gate onto its own
id** after a successful write — never an open one, since closing an open gate would drop the
`/model`/`/effort` sends `deliverSettingsInBand` issues right after it. The general shape: a
reject branch has to answer not just "what happens to this line" but "what state does the
session land in, and what gets it out" — fail-closed still needs a documented exit.

**The stored posture is not the only path bypass takes into a spawn's argv.** The live-claude
gate reddened four pre-existing specs that spawn through the operator's bootstrap
`--dangerously-skip-permissions` pass-through (`install-service -- --dangerously-skip-permissions`,
documented at `withApprovalArgs` — see [Constructing a
`streamRunner`](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md)) rather
than through `sessions.claudeSettingsArgs`'s per-session YOLO bit. That pass-through never
touches `SessionSettings`, so the stored posture read `default` at a child actually running in
bypass, and the spawn-time write silently downgraded a bypass the operator asked for outside
the daemon's own knowledge. Fixed, originally, by evaluating admissibility **per spawn** against
that spawn's own `args`: a spawn whose argv already named the escalation wrote nothing and armed
OPEN.

**#2065 could not keep that fix — the argv reading it depended on stopped existing.** Once
`sessions.claudeSettingsArgs` puts the escalation flag on every argv (#2065's whole point — the
posture is decided in-band, not by what launches), reading `args` for the flag answers "yes" for
every spawn, and the predicate above inverts into its permissive arm for everyone: no child is
ever downgraded, every gate stays open, the exact inverse of #2065's ticket, shipped green. There
is no argv shape left to read that still tells the two cases apart, because after #2065 a bypass
the daemon composed from the stored posture and a bypass the operator handed it on the
pass-through look byte-identical on the assembled argv — both are just the flag.

The fix is `Config.OperatorBypass bool` — a construction-time field, not a per-spawn argv read.
`spawnAndWait`'s clause became `permissionModeAllowed(spawnMode) && !r.cfg.OperatorBypass`, and
the id stays conditional while the `arm` call stays unconditional (the residual-arm fix above is
not reintroduced). `OperatorBypass` is set once, in `internal/sessions`, from
`operatorBypass(base)` — `base` is `Session.spawnBase`, the *settings-free* argv that carries the
operator's pass-through and nothing `claudeSettingsArgs` ever appends — so it is exactly the
signal the per-spawn `args` read used to approximate, computed from the one place that still
separates the two provenances instead of from the place that stopped being able to. See
[`SessionSettings` / `claudeSettingsArgs`](sessions-package-key-types-sessionsettings-claudesettingsargs.md)
for the provenance bit's full derivation and its `cmd/pyry` twin.

Neither the fake-daemon suite nor this package's own tests could see the original gap — both
drive the stored posture, neither spawns through the pass-through — so it needed the live-claude
proof to surface at all the first time. #2065's live arm
(`TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows`, extended) re-proves the
closed launch→downgrade window on the daemon's *own* spawn path rather than on a hand-driven
argv, but as of #2065 landing it is written and unexecuted in this environment — no Claude
credentials, so the arm reports `--- SKIP` and exits 0. Until an operator's `make
e2e-realclaude` run reads a nonzero `=== RUN` count for that arm, the re-proof rests on #2060's
hand-driven measurement plus the gate itself, not on this path.

## Keeping the spawn posture in step

`Pool.UpdateSettings` changes a session's stored posture without reconstructing the runner —
neither branch rebuilds it — so a construction-time-only spawn posture would go stale the
moment an operator changed it after startup, and a later crash-respawn would silently re-assert
the old one (able to *loosen* a posture just tightened). `sessions.Runner` gained
`SetSpawnPermissionMode(mode string)`, called unconditionally on the line **above**
`UpdateSettings`' branch split, so every posture update — including the one that takes the
restart branch, `X → bypassPermissions`, which never reaches `SetPermissionMode` — keeps the
next spawn's write current. See [Runner interface +
`RunnerFactory`](sessions-package-key-types-runner-interface-runnerfactory.md) and
[`Pool.UpdateSettings`](sessions-package-key-types-pool-updatesettings.md).

## Fake-daemon regression this ticket exposed, not introduced

`internal/e2e/internal/fakeclaude`'s `runStreamJSONApprove` is a **separate, duplicated** read
loop from `runStreamJSON`, and its own doc comment claimed non-user lines are ignored "exactly
like runStreamJSON" — untrue since #1692 and more since #2067, which taught `runStreamJSON`
(not its duplicate) to answer `initialize` and `set_permission_mode`. Nothing caught it while
nothing sent the request. The moment this ticket's spawn-time write went live, every child
spawned with the approve rider on (`relay_v2_stream_modal_test.go`) could never ack its
posture, and the modal round-trip test failed on its deadline reporting no modal — a failure
that reads as approval-wiring breakage and is not one. Fixed by adding the same
`set_permission_mode` arm to the duplicate loop (not `initialize`, which nothing gates a turn
on). General lesson: a doc comment asserting two code paths behave "exactly like" each other is
a claim that rots the moment either path gets taught something new — grep for *every* read
loop a new dispatch arm needs, not the one the ticket names. See [fakeclaude-binary-stream-json-mode.md
§ Approve rider](fakeclaude-binary-stream-json-mode.md#approve-rider-pyry_fake_claude_stream_approve-1139).
