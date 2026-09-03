# #2064 — write the stored permission mode on every spawn, and hold turns until claude acks it

Every spawned child is told the session's stored permission posture in-band, and no
user turn reaches that child until claude's `control_response` for the daemon's own
minted `request_id` comes back success. This is the first slice on this repo that
*reads* a control ack; the three existing writers all write and stop.

## Files read

- `internal/streamsup/runner.go` → `Config.RequestInitializeOnSpawn`, `spawnAndWait`,
  `setStdin`, `beginSpawn`, `WriteUserTurn`, `turnTarget`, `SetPermissionMode`,
  `RequestInitialize`, `nextControlID`, `WaitForPTY`, `Runner.mu` / `Runner.restartMu`.
  The whole spawn-and-turn surface. `RequestInitializeOnSpawn`'s doc is the shelf this
  write joins and states the once-per-child-by-construction argument verbatim;
  `setStdin`'s doc is where the "turns become writable at this instant" fact lives;
  `Runner.mu`'s charter forbids nesting the two leaf mutexes, which decides how the
  gate is read.
- `internal/streamsup/envelope.go` → `WritePermissionMode`, `permissionModeAllowed`,
  `marshalPermissionModeEnvelope`, `permissionModeDefault`,
  `ErrUnsupportedPermissionMode`, `WriteTurn`. The closed allow-list that refuses
  `bypassPermissions` by non-membership, the refusal ORDER (vocabulary before
  no-live-child) which this ticket now depends on, and the PIPE_BUF length argument.
- `internal/streamsup/parser.go` → `consumeLine`'s `control_response` arm,
  `emitModelList`, `controlResponseLine`, `logControlResponse`, `streamLine`,
  `NewParser`, `Parser`. The arm already classifies a mode ack onto
  `controlResponseAck`; `controlResponseLine`'s "request_id is deliberately absent"
  paragraph and `emitModelList`'s provenance paragraph are the two doc comments the
  ticket names, both restated below rather than deleted. `logControlResponse` is
  where "nothing from the payload is logged" is decided.
- `internal/sessions/runnerstate.go` → `RunnerConfig`. Carries no posture today; the
  doc argues that a field here must correspond to behaviour something implements.
- `internal/sessions/pool.go` → `Pool.New`'s and `Pool.buildSession`'s `RunnerConfig`
  literals (both below a `canonicalSettings(settings)` call), `Pool.UpdateSettings`,
  `inBandDeliverable`, `Pool.deliverSettingsInBand`. `UpdateSettings`' *"Both branches
  install newArgs; only one kills. Swap BEFORE the write"* comment is the precedent
  for where the runner's spawn posture is kept in step — and reading both branches is
  what surfaced the staleness hazard § Design records.
- `internal/sessions/session.go` → `claudeSettingsArgs`, `canonicalSettings`,
  `canonicalPermissionMode`, `permissionModeInBand`, `permissionModeKnown`,
  `SessionSettings`. The stored posture's shape, the `YOLO == (PermissionMode ==
  bypass)` invariant, and the argv the ticket says makes this a no-op today.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory`, `mapStreamsupConfig`,
  `withApprovalArgs`, `streamRunner`. The pure-mapper / runtime-object dividing line
  the new fields land on either side of, and `withApprovalArgs`' record that
  `--dangerously-skip-permissions` is *"the single deterministic yolo signal"*.
- `cmd/pyry/session_model_hold.go` → `newSessionParser`, `sessionModelHold`. The local
  precedent for a per-session hold minted together with the parser and read by the
  adapter on the next line, and for nil-receiver-safe reads.
- `internal/e2e/internal/fakeclaude/main.go` → `writeSetPermissionModeAck`,
  `runStreamJSON`, `envStreamWithholdMode`
  (`PYRY_FAKE_CLAUDE_STREAM_WITHHOLD_MODE_ACK`). #2067's ack and its withheld rider —
  the switch AC 3 rides.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay`, `Harness.Stderr`. The
  rider is passed as `extraEnv`; the daemon's stderr is captured and greppable, which
  is what makes AC 3's e2e assert a positive rather than only an absence.
- `internal/e2e/relay_v2_stream_queue_drain_test.go` →
  `TestRelayV2_StreamMidTurnHoldDropAndDrainInOrder`. The nearest fake-daemon
  analogue: a startup rider that makes the child not consume stdin, a sealed
  `send_message`, and the vacuity discipline AC 3's test copies.
- `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go` →
  `seedBypassRegistry`, `revokeTap`, `newRevokeLogRecorder`, `revokeControlBudget`,
  `TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode`. The live rig
  AC 5 extends. Its § "The response cannot be correlated by id" paragraph is exactly
  the gap this ticket closes, and `seedBypassRegistry` already takes a `yolo bool`, so
  a non-bypass seed — the one posture that arms the gate — needs no new seeder.
- `internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_reescalate.json` →
  the committed ack shape (`request_id` beside `subtype` on the inner wrapper,
  `response.mode` carrying the landed mode).
- `docs/knowledge/features/streamsup-package.md` and
  `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md` — the package
  overviews for the two packages this touches.

## Context

The daemon writes control requests and never reads their acks:
`(*streamsup.Runner).SetPermissionMode` and `RequestInitialize` each write a line and
stop, and `nextControlID`'s doc records that the minted id *"is write-only here"*.
`RequestInitialize`'s doc names the missing piece — *"the reader slice that correlates
the ack is the first thing that needs it"*. This is that slice plus its first
consumer.

On today's launch argv the spawn-time write re-asserts a posture
`claudeSettingsArgs` has already composed into the argv, so nothing about the daemon's
current safety changes. It becomes load-bearing in #2065, which takes the posture out
of the launch argv entirely.

`default` is written like any other mode. Skipping it on the grounds that claude is
already there is exactly wrong under #2065, where every child launches in bypass and
`default` is the mode the daemon's safety rests on.

Blocked by #2067, which landed first: fakeclaude now answers `set_permission_mode`
unconditionally and withholds the answer under
`PYRY_FAKE_CLAUDE_STREAM_WITHHOLD_MODE_ACK`. Without it, arming this gate would refuse
every turn in the fake-daemon suite forever.

**No ADR is warranted.** The correlation decision is a reversal of a bounded
paragraph inside `emitModelList`, not a new cross-cutting posture; § Design records
the reversal at the two doc comments it belongs to.

## Size

Two lines of the size-S table are exceeded — total written work (~1,250 against 800)
and production source files (6 against 5). **Both overages are stated rather than
worked around, and the ticket is built.**

The line overage is the one the ticket body pre-accepts and forbids routing back for.
The fake's half is already cut out to #2067, and both remaining cuts fail the FLOOR
rule — the spawn-time write alone ships a line no one reads (the
seam-with-nothing-on-the-far-side `RequestInitialize`'s own doc refuses), and the
correlator-plus-gate alone refuses every turn forever, gating on an ack for a request
nothing sends. Neither half is verifiable on its own.

The file overage is **one interface method** in `internal/sessions/runner.go`, and it
was not in the refiner's count because the staleness it closes is not visible from the
ticket body — it only surfaces on reading that `Pool.UpdateSettings` rebuilds no
runner. It fails the floor for the same reason: a slice whose only deliverable is
"keep the runner's spawn posture in step" has exactly one consumer, this ticket, and
splitting it out would ship this ticket with a posture that can silently loosen. The
floor wins over the ceiling, so it is merged in and the overage is declared.

| Limit | Boundary | This plan |
|---|---|---|
| Production source files | ≤ 5 | **6** — `streamsup/runner.go`, `streamsup/parser.go`, `sessions/runnerstate.go`, `sessions/runner.go`, `sessions/pool.go`, `cmd/pyry/streamsup_runner.go` |
| Total written work | ≤ 800 | **~1,250** — accepted, see above |
| New exported types/interfaces | ≤ 5 | **1** — `streamsup.PostureGate` |
| Consumer call sites needing simultaneous update | ≤ 10 | **9** — 6 `sessions.Runner` implementations (1 adapter + 5 test doubles), 1 `Pool.UpdateSettings` call, `beginSpawn`+`spawnAndWait` threading. The two `RunnerConfig` literals and `mapStreamsupConfig` take additive fields and need no simultaneous change. |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **7** — see § Error handling |

## Design

### The gate — `streamsup.PostureGate`

One new exported type, declared in `runner.go` beside the rotation gate it is a
sibling to, so a reader comparing the two gates finds both in one file. Its whole
state is one string:

```go
type PostureGate struct { mu sync.Mutex; armedID string }

func (g *PostureGate) arm(requestID string)     // runner side, at spawn
func (g *PostureGate) release(requestID string) // parser side, on a success ack
func (g *PostureGate) ready() bool              // runner side, per turn
```

`armedID == ""` is the OPEN state, so a gate nobody armed is open and today's
behaviour is the zero value. `arm` closes it against exactly one id; `release` opens
it only on an exact match; `ready` reports open. All three are **nil-receiver-safe**,
following `sessionModelHold.ModelList`'s precedent, so neither the runner nor the
parser needs a nil branch at its call site.

`release` needs no `id != ""` guard and deliberately has none: `nextControlID` formats
a `uint64` counter that has already been incremented, so an armed gate's id is never
empty, and an ack carrying no `request_id` therefore cannot match one. Against an
*unarmed* gate the comparison succeeds and the assignment is a no-op. A guard here
would be a defence against a state that cannot exist.

A fresh `arm` overwrites the previous id, which is AC 4's second layer: a
predecessor's late ack is refused by id mismatch even though its child is gone.

**Who mints it.** The parser does, in `NewParser`, and exposes it through
`(*Parser).PostureGate()`. `newStreamRunnerFactory` builds the Parser *before*
`streamsup.New`, so the gate has to exist ahead of both; minting it inside the parser
means the two halves cannot be bound to different gates — `newSessionParser`'s own
argument for minting the parser and its holds together. It also leaves `NewParser`'s
signature alone, which nine call sites depend on. A Parser used with no runner mints
an inert gate; nothing arms it.

### The write — one per child, on `RequestInitializeOnSpawn`'s shelf

`Config` gains `SpawnPermissionMode string` (the plumbed stored posture) and
`PostureGate *PostureGate` (the runtime object). Cardinality is per spawn, which is
per child by construction — `RequestInitializeOnSpawn`'s doc argues that in full and
is not restated here.

In `spawnAndWait`, **above** `setStdin`:

- if the spawn's posture is admissible, mint one id off `nextControlID` and `arm` the
  gate with it.

Then `setStdin`, then the write. **The arm must precede `setStdin`**, because
`setStdin` publishes the live handle and disarms the rotation gate in one
acquisition, so turns become writable at that instant; arming after it leaves a
window in which a turn is delivered before the write.

The write itself goes through `WritePermissionMode` unchanged — same allow-list, same
envelope, no new writer. Its error is **absorbed** with one Debug record, exactly as
the initialize ask's is, so it can never become `waitErr` and enter the backoff
ladder. **Absorbing the error does not release the gate** — that is AC 4, and it is
structural rather than a rule to remember: nothing on the error path touches the
gate, and only a matching success ack opens it.

The write is placed **above** the initialize ask so the gate-releasing round trip
starts first.

#### What decides whether a spawn writes at all

`permissionModeAllowed(mode)` — the closed allow-list, unchanged and not widened.
`bypassPermissions` fails by non-membership, so a bypass session is sent nothing and
**its gate is never armed**: its turns flow exactly as they do today. A gate no write
can ever release is a bricked session, not a fail-closed one. That one check is the
whole gate, and it is load-bearing only because the stored posture it reads is kept
accurate — see the next section.

**An argv-derived interlock was designed and then rejected**, and the rejection is
recorded because it looks like free defence in depth. Refusing the write whenever the
spawn's argv named `--dangerously-skip-permissions` would be exact today and would
brick #2065 outright: that ticket launches *every* child in bypass and delivers the
posture in-band, so an argv-keyed refusal would refuse every downgrade it exists to
deliver. A guard the next ticket must delete is worse than no guard.

A `SpawnPermissionMode` set with a nil `PostureGate` would write a line nobody reads.
`streamsup.New` rejects that pairing outright rather than degrading — a misconfigured
composition is a startup failure, not a silent unenforced write.

### Keeping the spawn posture in step with the stored one

`Config.SpawnPermissionMode` is construction-time. `Pool.UpdateSettings` changes a
session's stored posture **without reconstructing the runner** — both branches install
`newArgs` and neither rebuilds it — so a construction-fixed value goes stale, and a
crash-respawn would then assert a posture the operator has already changed. That is
not a future concern: it makes the ticket's own "no-op in effect today" premise false
for any session updated after startup, and it can *loosen* a posture the operator
tightened.

**`sessions.Runner` gains one method, `SetSpawnPermissionMode(mode string)`**, called
from `Pool.UpdateSettings` on the line **above** the branch split — the one place both
branches pass through, right where its own comment already says *"Both branches
install newArgs."* One call covers both, and it is called unconditionally rather than
only for posture updates: `merged.PermissionMode` is the stored posture whatever the
update named, so an unconditional install is idempotent and cannot be forgotten by a
future branch.

**Two shapes were tried first and rejected**, because the obvious cheap ones do not
cover the escalation:

- Piggybacking the install on `SetPermissionMode`, which the in-band branch already
  calls. It covers every `X → Y` mode change but **not `X → bypass`**: that update is
  not in-band-deliverable, takes the restart branch, and never reaches
  `SetPermissionMode` — which could not carry it anyway, since the allow-list refuses
  the escalation by non-membership and naming it here would put the literal back into
  streamsup production source that #1603 deliberately emptied of it.
- Deriving the posture from the installed argv. Same objection as the interlock above:
  it dies with #2065.

The method is **on the interface, not reached by type assertion**, for the rule
`SetPermissionMode`'s own doc states: its consumer sits inside `internal/sessions`,
where a structural assertion fails OPEN — and failing open here means a respawned
child silently asserting a posture the operator has already changed, which is the
exact defect this section exists to close. It costs one line in
`internal/sessions/runner.go`, one forward on `streamRunner`, and a no-op stub on each
of the five test doubles.

The mutable value lives beside `args` and `sessionID` under `restartMu`, and is read
in `beginSpawn`'s single section and threaded to `spawnAndWait`, so the
one-acquisition-per-spawn-setup charter holds with no second acquisition. Seeded in
`New` from `Config.SpawnPermissionMode`, exactly as `sessionID` is seeded from
`Config.SessionID`.

### The read — correlation in the parser

`consumeLine`'s `control_response` arm calls a new `(*Parser).noteControlAck(line)`
**above** the existing `emitModelList(line)`, so the gate opens without waiting behind
an emit into a downstream sink.

It decodes into a **new, separate** target declaring `subtype` and `request_id` and
nothing else:

```go
type controlAckLine struct {
    Response struct {
        Subtype   string `json:"subtype"`
        RequestID string `json:"request_id"`
    } `json:"response"`
}
```

**A separate decode target rather than a field added to `controlResponseLine`, and
that is the substance of this half.** The arm's own doc requires that the correlation
*not disturb which rung a models or commands payload lands on*. Adding `RequestID
string` to `controlResponseLine` would break that: a `request_id` that is not a JSON
string fails the whole-line decode, so a payload that reaches `emitModelList`'s
model-list rung today would newly land on its undecodable rung. A second target makes
the non-disturbance **structural** — `emitModelList` is byte-for-byte unchanged — and
costs one extra `json.Unmarshal` on a line type that arrives about twice per spawn.

The decode input is the **top-level line bytes**, never a nested field, for
`streamLine`'s stated reason: it is what stops a tool result whose text is literally a
control shape from forging one.

The target declares no payload key at all — not `mode`, not `models`, not `account` —
so nothing from the payload can reach this path, its decision, or a log.
`response.mode` is deliberately not read: the id correlation already answers *which
request this answered*, and the mode echo is claude's claim about itself, which AC 5
reads through the next turn's `init.permissionMode` instead.

**Nothing here logs, on any path.** Not the decode error either — `encoding/json`
quotes the offending input bytes into its error text, which is the channel
`emitModelList`'s undecodable arm already refuses.

#### The two doc comments the ticket names, restated

- `controlResponseLine`'s *"request_id is deliberately absent"* stays true of **that
  type** and its paragraph is kept: nothing correlates the id *for content
  provenance*, and `emitModelList`'s longer paragraph — *"recognise the shape, hold no
  cross-object parser state for provenance"* — is about whether a models array can be
  attributed to the daemon's own `initialize` ask. Both get a sentence pointing at
  `controlAckLine` and stating the distinction: a gate on a locally-minted request the
  daemon is **actively waiting for** is a different question, decided on a different
  type, and it moves neither the content bound nor which rung anything lands on.
- `consumeLine`'s arm keeps its record that a mode ack is *"consumed content-free, one
  record and no event"* — still exactly true; the gate release is neither a record nor
  an event.

### The turn gate

`WriteUserTurn` reads `turnTarget()` first and the posture gate **second**, in two
separate acquisitions, and the ORDER is the correctness argument rather than a
detail. The arm strictly precedes `setStdin`, so a handle observed as live has already
had its child's gate armed; reading the gate first and the handle second would allow
reading "open" for child N and then writing into child N+1.

Two separate acquisitions rather than one, because `Runner.mu` is a leaf and its
charter forbids holding it across a call into another object's lock. Unlike the
rotation gate this needs no single-acquisition treatment: the rotation gate's two
reads answer one question about the *same* instant, whereas the posture gate's answer
for a given handle can only become *more* permissive.

A closed gate refuses by nilling the target, so `WriteTurn` returns `ErrNoLiveChild`
with **zero bytes written** — the same retryable no-live-child classification the
rotation gate already reuses, and no new envelope construction. One record at
**Info**, outside every acquisition, naming only the session — Info and not Debug for
the rotation refusal's stated reason: a Debug record anywhere in the daemon's stderr
defeats #1330's e2e instrument guard.

### Plumbing

`sessions.RunnerConfig` gains `PermissionMode string`; both literals in
`internal/sessions/pool.go` set it from `settings.PermissionMode`, and both sit below
a `canonicalSettings(settings)` call, so the value is always a real mode.
`mapStreamsupConfig` maps it to `SpawnPermissionMode` — a plain string, so it belongs
in the pure mapper, on `ClaudeSessionsDir`'s side of the line. `newStreamRunnerFactory`
installs `PostureGate` from `parser.PostureGate()` — a runtime object, so it belongs
in the factory, on `Stdout`'s side.

## Concurrency model

No new goroutines, no new channels, nothing to shut down.

Three mutexes are involved and **none is ever nested inside another**, which preserves
the package's two-leaf-mutex charter:

- `PostureGate.mu` — a new leaf. Held across one string compare and one assignment;
  never across a log call, a write, or another lock.
- `Runner.mu` — untouched. `WriteUserTurn` releases it (inside `turnTarget`) before
  touching the gate.
- `Runner.restartMu` — gains the spawn posture beside `args`. Read once per spawn in
  `beginSpawn`'s existing section; written by `SetPermissionMode` in its own
  acquisition, never while `mu` is held.

Writers of the gate: the Run goroutine (`arm`, at spawn) and the parser goroutine
(`release`, from `os/exec`'s single stdout writer). Reader: any turn goroutine
(`ready`). All serialised by the gate's own mutex.

## Error handling

Seven reject branches, each with a stated direction:

| # | Branch | Outcome |
|---|---|---|
| 1 | Spawn posture outside `permissionModeAllowed` (incl. `bypassPermissions`, incl. empty) | no write, **no arm** — turns flow as today |
| 2 | `SpawnPermissionMode` set with a nil `PostureGate` | `streamsup.New` returns an error |
| 3 | Spawn-time write fails (no live child, EPIPE) | absorbed, one Debug record with no mode and no id; **gate stays closed** |
| 4 | Ack line will not decode into `controlAckLine` | no release, nothing logged |
| 5 | Ack subtype is not `success` | no release |
| 6 | Ack `request_id` does not match the armed id (an `initialize` ack, a stale child's ack) | no release |
| 7 | Turn arrives with the gate closed | `ErrNoLiveChild`, zero bytes, one Info record naming only the session |

A child that exits before acking leaves the gate closed and needs no teardown hook:
`takeStdin` already nils the handle, so turns refuse on the older ground, and the next
spawn arms a fresh gate against a fresh id.

## Testing strategy

Unit, in `internal/streamsup`:

- `PostureGate`: zero value is open; `arm` closes; `release` with a mismatched id
  leaves it closed; `release` with the matching id opens it; a second `arm` refuses
  the first id's ack (AC 4); nil receiver is open and its mutators are no-ops.
- `noteControlAck` table: the committed capture's line releases a gate armed on its
  id; a non-success subtype does not; a malformed line does not; a line whose
  `request_id` is a JSON number does not (and — the paired assertion — the same line
  still classifies identically in `emitModelList`, which is the non-disturbance claim
  made testable); an `initialize` ack does not release a posture gate armed on a
  different id.
- `emitModelList` regression: the existing rung table still classifies every fixture
  identically with a gate installed.
- `spawnAndWait` against the package's fake child: exactly one `set_permission_mode`
  line per spawn carrying the configured mode; none for `bypassPermissions`; the id it
  carries is not the id the initialize ask carries; a second spawn writes a fresh id.
- `WriteUserTurn`: refuses with `ErrNoLiveChild` and writes **zero bytes** while
  armed; writes after a matching release; the count of bytes reaching the fake stdin
  is the oracle, not the error alone.
- `SetSpawnPermissionMode` changes what the **next** spawn writes and leaves the live
  child untouched (no bytes written by the call itself) — the two halves that
  distinguish it from `SetPermissionMode`.
- `New` rejects `SpawnPermissionMode` without a gate.

Unit, in `internal/sessions`: `Pool.UpdateSettings` calls `SetSpawnPermissionMode`
with the merged posture on **both** branches — the in-band one and the
escalation-to-bypass restart one. The second row is the one that reds the rejected
piggyback design, so it is not optional.

Unit, in `cmd/pyry`: `mapStreamsupConfig` carries `PermissionMode` through, and the
factory binds the parser's own gate onto the runner's config (bound to the same
object, not merely non-nil).

Fake-daemon e2e (AC 3), `internal/e2e`: one stream-interactive harness started with
`PYRY_FAKE_CLAUDE_STREAM_WITHHOLD_MODE_ACK=1`, one sealed `send_message`, then two
assertions that together are non-vacuous — the daemon's captured stderr carries the
posture refusal record (proving the gate armed and fired, a positive), and no
`assistant_delta` arrives within the deadline (proving the turn did not reach the
child). Deterministic because the ack is withheld permanently, not slow. **The
positive control is the rest of the suite**: every other fake-daemon stream test now
arms this gate on a `default` posture and still drives turns to completion, so a gate
that never opened would redden them wholesale rather than showing up as one skipped
assertion.

Live-claude (AC 5), extending
`internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go`: a second
test on the same rig with `seedBypassRegistry(t, path, false)` — the non-bypass seed
that arms the gate — driving one turn to completion. With the gate armed a completed
turn **is** the released gate, which is the whole assertion; the tap's captured
`control_response` gives the id and subtype for a reader, and the next turn's
`init.permissionMode` echo is the second confirmation #1595 and #2041 both read. It
must be an **executed** test: the evidence recorded in the file is the count of
`=== RUN` lines, never the exit code.

## Open questions

1. Does the daemon's own approval-flag injection (`withApprovalArgs`) put
   `--permission-mode default` into the argv of a session storing `default`, making
   the spawn-time write doubly redundant today? Expected yes, and it changes nothing —
   but confirm, because it decides whether AC 5's live child is one the argv already
   put in `default`.
2. Does any existing `internal/streamsup` test assert the exact byte stream a spawn
   writes, such that an added `set_permission_mode` line reddens it? Expected no —
   `SpawnPermissionMode` defaults to empty and branch 1 refuses it, so every existing
   construction site stays byte-identical — but confirm rather than assume.
3. Does the fake-daemon suite contain a session whose stored posture is not `default`,
   for which the withheld-ack rider would change behaviour outside AC 3's own test?

## Security review

**Verdict:** PASS (second pass — the first FAILED, see below)

**First pass — one MUST FIX, now resolved.** The plan carried a second refusal on the
spawn write: refuse whenever the spawn's argv named `--dangerously-skip-permissions`.
It reads as free defence in depth and is exact on today's argv. It also **bricks
#2065**, the ticket this one exists to unblock: that ticket launches every child in
bypass and delivers the posture in-band, so an argv-keyed refusal would refuse every
downgrade it is there to deliver — a fail-open on the one path where the gate is the
only defence left. Removing it re-opened the staleness the interlock had been covering
(`X → bypass` never reaches `SetPermissionMode`), which is what forced
`SetSpawnPermissionMode` onto `sessions.Runner` and the file-count overage above. Both
sections record the rejected shapes rather than only the chosen one.

**Findings:**

- **[Trust boundaries] SHOULD FIX — a new flow, bounded but real.** Until this ticket
  the child's stdout could only produce events; `noteControlAck` lets it **open a
  safety gate**. The boundary is one function on one decode target, and the target
  declares `subtype` and `request_id` and no payload key at all — not `mode`, not
  `models`, not `account` — so nothing from the payload can reach the decision. What
  the gate proves is *"claude acked the request the daemon minted"*, **not** *"claude
  enforces that posture"*; an echoed posture is the child's claim about itself
  (`interactive_stream_inband_bypass_revoke_test.go` § "Scope boundary" already says
  so for the ack it reads). A child that lies about its posture defeats the gate — and
  a child that lies is one that could equally ignore the mode, so the gate buys
  protection against *benign* failure (an unsupported mode, a dropped line, a slow
  child), not against a hostile one. Phase B must state that limit at `PostureGate`
  itself, not only here.
- **[Trust boundaries] No finding — forged control shapes.** The decode input is the
  top-level line bytes, and `consumeLine` dispatches on the top-level `type`, so a
  tool result whose text is literally a control shape is never re-scanned.
  `streamLine`'s doc states the property; this ticket inherits it by reusing that
  input and adds no nested read.
- **[Tokens] No finding.** `request_id` is a correlation token, not a capability, and
  deliberately not `crypto/rand`: the daemon hands it to the child in cleartext in the
  request itself, so unpredictability buys nothing, and `permissionModeAllowed`'s
  PIPE_BUF length argument wants it short. It is compared and dropped — never
  retained, never persisted, never logged.
- **[File operations] Not applicable.** No path is constructed, opened, or created.
- **[Subprocess execution] No finding.** No argv token is added by this ticket; the
  mode reaches the child only as a JSON string value through the **existing**
  `WritePermissionMode`, whose allow-list is neither widened nor bypassed, and
  `marshalPermissionModeEnvelope` gains no second caller — the one that would force
  its gate to move down. Membership bounds the line under PIPE_BUF, so the write stays
  one atomic `write(2)` and cannot tear against a concurrent `WriteTurn`.
- **[Cryptographic primitives] Not applicable, and deliberately so.** `armedID` is
  compared with `==`. It is not a secret and the comparand is not attacker-chosen in
  any way that matters, so `crypto/subtle` here would signal a threat model that does
  not exist.
- **[Network & I/O] No finding.** Input is already bounded before the new decode:
  `Parser.maxBuf` caps a line, and `consumeLine` has already decoded it once. The new
  target allocates two strings, retains neither, and adds no unbounded growth.
- **[Errors, logs, telemetry] SHOULD FIX — enforce in Phase B, three places.** MUST
  NOT reach any log: the permission mode (#833 keeps settings values out of the daemon
  log), the `request_id`, the payload, and the ack decode error — whose text quotes
  the offending input bytes, the channel `emitModelList`'s undecodable arm already
  refuses. Concretely: `noteControlAck` logs **nothing at all**, on any path; the
  absorbed spawn-write error is logged at **Debug** with `"err"` alone (safe because
  `WritePermissionMode` documents that no wrap carries the mode and
  `ErrUnsupportedPermissionMode` is bare); and the turn refusal is logged at **Info**
  with the session alone — Info rather than Debug because a Debug record anywhere in
  the daemon's stderr defeats #1330's e2e instrument guard.
- **[Concurrency] No finding, and the ordering is the argument.** Three leaf mutexes
  (`PostureGate.mu`, `Runner.mu`, `Runner.restartMu`), **never nested** — the
  package's charter. The turn path reads the stdin handle **first** and the gate
  **second**; since `arm` strictly precedes `setStdin`, a handle observed live has
  already had its child's gate armed, so the reverse order (gate then handle) is the
  one that could pass child N's release to child N+1 and is structurally excluded. A
  fresh `arm` overwrites the id, so a long-lived per-session parser draining a dead
  child's buffered stdout cannot release its successor's gate. No goroutine is
  spawned, so none can leak.
- **[Threat model alignment] No finding.** This ticket adds **no authorisation
  decision**. `permissionModeAllowed` stays a vocabulary check — *"is this a mode
  claude will parse?"* — exactly as its doc insists; who may change a session's posture
  remains `Pool.UpdateSettings`' `validatePermissionUpdate`, untouched. No wire
  surface, so `docs/protocol-mobile.md` § Security model has nothing to answer here.
- **[Out of scope]** Behavioural enforcement of the acked posture — proving the child
  *obeys* the mode rather than reporting it — is not attempted, matching the live
  rig's existing scope boundary. Removing `RevokeBypass`, whose last production caller
  went with #2043, stays out per the ticket. Taking the posture off the launch argv is
  #2065.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03

## Revisions

### 2026-09-03 — implementation

**Open questions, resolved.**

1. *Does `withApprovalArgs` put `--permission-mode default` into a default session's
   argv?* **Yes**, confirmed from a fake-daemon spawn record — the composed argv is
   `… --permission-prompt-tool … --strict-mcp-config --permission-mode default
   --session-id …`. So the spawn-time write is doubly redundant today exactly as the
   ticket says, and AC 5's live child was one the argv had already put in `default`.
   Nothing in the design changes; it is what makes the live measurement's
   `response.mode` echo agree with the init echo.
2. *Does any existing `internal/streamsup` test assert a spawn's exact byte stream?*
   **No.** `SpawnPermissionMode` defaults to empty and the allow-list refuses it, so
   every pre-existing construction site is byte-identical; the package's suite is green
   with `-race` and needed only the mechanical `beginSpawn` arity update at two test
   call sites.
3. *Does the fake-daemon suite hold a non-default session the withheld rider would
   disturb?* **No** — but the question found a different, real defect. See below.

**One defect found and fixed: `runStreamJSONApprove` was never taught to answer.**
#2067 added the `set_permission_mode` arm to `runStreamJSON`. The approve rider runs a
**separate, duplicated read loop**, `runStreamJSONApprove`, whose doc claimed non-user
lines are ignored *"exactly like runStreamJSON"* — untrue since #1692 and more so since
#2067, because the duplication was of the LOOP and not of the DISPATCH. Nothing caught
it while nothing sent the request.

This ticket made it fatal: `TestRelayV2_StreamModalPermissionRoundTrip` failed on its
deadline reporting no modal — a failure that reads as an approval-wiring fault and is
not one. Measured: `origin/main` passes in 9.3 s, this branch without the arm fails in
81 s, this branch with it passes in 9.5 s. Fixed in the fake (test-support code, not
production) with a regression test that also gates non-vacuity by asserting the loop
still emits its gated `tool_use`. It is in scope rather than a separate ticket: the
red is this branch's own regression, not a pre-existing bug found alongside it.

**Actual size.** 1,483 lines across 16 files on top of the 538-line spec commit —
against the ~1,250 estimated. **6 production files** as planned. The seventh file
touched, `internal/e2e/internal/fakeclaude/main.go`, is the e2e fixture binary and not
production source, so the declared file overage is unchanged.

**Design unchanged from the committed plan.** The rejected argv interlock and the
`SetSpawnPermissionMode` decision were both settled in the security-review pass before
the plan was committed; nothing was re-decided during implementation.

### 2026-09-03 — rework, verifier MUST FIX: the arm is unconditional

**The defect.** § Design said a bypass spawn's *"gate is never armed"*, and the code
implemented that literally — the `arm` call sat inside the `permissionModeAllowed`
branch, so a spawn that writes nothing installed **nothing**. But the gate outlives the
child: `Restart` reuses the same `*Runner` and the same `*PostureGate`, so "install
nothing" means "inherit the predecessor's id". An un-acked `default` child followed by
an escalation to `bypassPermissions` — which is not in-band-deliverable and so takes
`Pool.UpdateSettings`' restart branch — left the replacement child armed on an id
nothing could ever ack, refusing every turn on that session forever under the
*retryable* classification. That contradicts AC 1's bypass clause verbatim, on the one
posture the AC says must never be gated.

**The fix**, one line: hoist `r.postureGate.arm(postureID)` out of the branch, leaving
only the id conditional. `arm("")` installs the OPEN state, which is the statement
`armedID`'s own doc already makes — nesting the call merely failed to reach the branch
that needed it. Direction: the gate errs *closed*, so nothing was ever loosened; this
was an availability defect, not a privilege one.

**Why the suite missed it, and what now covers it.** Both existing spawn tests judge the
bypass state against a **fresh** gate, where open is the zero value and the assertion is
vacuous — `TestRunner_SetSpawnPermissionMode_TakesEffectOnTheNextSpawn` releases before
each respawn, which is precisely what hid this.
`TestRunner_SpawnPermissionMode_ResidualArmDoesNotBrickABypassRespawn` drives the
operator sequence with the first gate deliberately **not** released, and asserts
non-vacuity first (the residual arm is real) before asserting the respawn's gate is
open, the turn's bytes reach the child, and bypass was still sent nothing. Confirmed RED
against the pre-fix shape by `go test -overlay` (`armed on "1" (the predecessor armed
"1")`) and green with the fix.

§ Design's "its gate is never armed" is superseded by the code comment at the arm site:
every spawn publishes its own gate state, and only the id it publishes is conditional.

### 2026-09-03 — rework, verifier MUST FIX: a NAK wedged the session permanently

**The defect.** § Error handling's row 5 recorded only the local behaviour — *"Ack
subtype is not `success` → no release"* — and never asked what happens to the session
NEXT. A live child that answers the spawn-time write with a non-success subtype left
the gate closed with **no opener left**: `arm` had exactly one production call site
(`spawnAndWait`) and `release` exactly one (`noteControlAck`), so a spawn was the only
opener, and the child is healthy, so nothing triggers one. § Design's *"a child that
exits before acking … needs no teardown hook"* argument covers only a child that
**dies**; a NAK is a child that answers.

The operator's documented remedy provably did not recover it. `inBandDeliverable` is
true for every `permissionModeInBand` mode, so a corrective mode change takes
`Pool.UpdateSettings`' in-band branch, never restarts, and reaches `SetPermissionMode`
— which minted a **fresh** id that nothing armed. Claude acked that id, `release`
compared it against the still-armed spawn id, they did not match, and the gate stayed
closed. Only an escalation to `bypassPermissions` reached `Restart`, so the sole
in-product recovery was to grant bypass and then downgrade again. Meanwhile every turn
refused under the **retryable** classification, so msgqueue burned its full give-up
window against a state that could never change, and the only evidence was one repeated
Info record naming neither the cause nor the mode.

A NAK is documented claude behaviour rather than a hypothetical.
`turnevent.ModelOption.SupportsAutoMode` records that claude refuses `auto` **per
model**, `validatePermissionUpdate` accepts `auto` for any session on any model, and
mode and model are independent settings, so nothing re-checks the pairing when either
one changes.

**The policy, settled here.** Fail-closed stays. Releasing on a NAK would be actively
wrong under #2065, where the child launches in bypass and a NAK'd downgrade must not
admit turns. What was missing is a *terminating* path — the daemon held a definitive
answer and discarded it. Two changes, and neither loosens the gate:

1. **The NAK is recorded rather than dropped.** `PostureGate` gains one bool beside the
   id, set by `refuse(id)` when a non-success ack carries the id the gate is armed
   against. `arm`, `release` and `retarget` all clear it, so the verdict never outlives
   the request it answers. Unlike `release`, `refuse` **needs** the `armedID != ""`
   guard that `release`'s doc argues against having: an open gate's id is `""`, so a NAK
   carrying no `request_id` would otherwise mark an OPEN gate refused. The flag changes
   no decision — the gate was already closed — it changes what the operator is told.
   `WriteUserTurn` writes a distinct record at **Warn** naming the cause instead of
   repeating "not yet confirmed by claude" at Info forever.
2. **`SetPermissionMode` re-targets a CLOSED gate onto its own id**, after a successful
   write, so the documented remedy works: change the mode, the in-band write gets
   confirmed, the gate opens. An **OPEN** gate is never closed by it, and that half is
   as load-bearing as the first — this ticket gates *spawns*, and closing the gate on an
   in-band change would drop the `/model` and `/effort` sends `deliverSettingsInBand`
   issues immediately afterwards through `WriteUserTurn`. A write that fails, or a mode
   the allow-list refuses, does not re-target either: the pending spawn id stays armed
   so its own ack can still open the gate.

**The classification is unchanged, deliberately.** Turns still refuse with the retryable
`ErrNoLiveChild`. With (2) the state is operator-recoverable without a daemon restart,
which is exactly what retryable means, and a fourth error out of `WriteUserTurn` would
break the contract check that stream `WriteUserTurn` returns only `ErrNoLiveChild`,
`turncommit.ErrDropped` or nil (`TestRelayV2_StreamNewSessionRotatesAndRestartsFresh`).
The refusal of an in-band `/model` send while the gate is closed is not new and is not
made worse: the rotation gate already refuses those sends in its own window. What this
fix removes is the PERMANENCE, not the refusal.

**§ Error handling, amended.** Row 5 gains its terminating half, and one row is added —
8 of 10:

| # | Branch | Outcome |
|---|---|---|
| 5 | Ack subtype is not `success`, carrying the ARMED id | no release; the gate is marked refused and every turn refused from then on is recorded at Warn naming the cause. Recovered by an in-band posture change (which re-targets it) or by the next spawn |
| 8 | `SetPermissionMode`'s write fails, or its mode is refused by the allow-list | no re-target — the pending spawn id stays armed, so its own ack can still open the gate |

**§ Security review, amended for the NAK path** (it did not consider it):

- The **trust-boundary** finding is unchanged in kind and narrower in fact. The child's
  stdout can open the gate on a success and can now also MARK it refused — but `refuse`
  is strictly less permissive than doing nothing: it cannot open a gate, and the
  `armedID != ""` guard makes "cannot close an open one" structural rather than
  argued. A hostile child gains no capability it did not already have by staying
  silent.
- The **logging** obligation is preserved rather than relaxed. `noteControlAck` still
  logs NOTHING on any path, including the decode error. The new Warn is written at the
  turn site from gate state alone and carries the session id and no other field — not
  the mode, not the request id, not the subtype text, not the payload. The single bit
  that crosses from the child's answer into the log is the record's existence, and that
  bit is already observable as "this session's turns are being refused".
- **Availability, not privilege**, in both directions. (2) can only open a gate that a
  spawn armed, and only on the ack for a write the daemon itself just made off its own
  counter; it adds no authorisation decision and touches neither the allow-list nor
  `validatePermissionUpdate`.

**Testing.** Neither suite could reach a NAK-then-turn sequence — the fake acks
unconditionally and the live rig drives `default` only — so the coverage sits at the
level the state machine lives on, in `internal/streamsup`, where the whole path
(parser → gate → runner) is real and deterministic:
`TestPostureGate_NakIsRecordedAndRetargetRecovers` walks every new transition including
the two guards; `TestParser_NoteControlAck_NakMarksTheArmedRequestRefused` proves
`consumeLine`'s arm reaches `refuse` and that a sibling's NAK does not;
`TestRunner_NakThenInBandModeChange_RecoversTheSession` drives the operator sequence end
to end against the fake child — spawn, NAK, turn refused with zero bytes and the Warn
record, `SetPermissionMode`, ack, turn flows;
`TestRunner_SetPermissionMode_LeavesAnOpenGateOpen` is the `/model`-drop guard; and
`TestRunner_SetPermissionMode_FailedWriteDoesNotRetarget` pins row 8. An e2e was
considered and rejected: the recovery path is entirely inside `internal/streamsup` with
no cross-package seam, so a second withheld-ack-style rider on the fake would prove
nothing the runner-level test does not.

**Doc comments retired in the same push** (verifier SHOULD FIX). `nextControlID` still
claimed *"this slice does not read the control_response ack, so the id is write-only
here"*, `WritePermissionMode` and `WriteInitialize` still claimed *"nothing correlates
the request_id"*, `Config.SpawnPermissionMode` still claimed a bypass spawn's *"gate is
never armed"* (superseded by the previous entry's hoist), `WriteUserTurn` enumerated two
refusal causes where there are now three, and fakeclaude's `runStreamJSONApprove` header
still claimed non-user lines are *"ignored … exactly like runStreamJSON"* with the
correction stranded forty lines down inside the arm that falsified it.
