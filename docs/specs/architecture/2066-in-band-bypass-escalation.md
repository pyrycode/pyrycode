# #2066 — Route a bypass escalation in-band

Enabling `bypassPermissions` on a running session stops respawning it. The
escalation becomes the sixth posture the daemon delivers as a `set_permission_mode`
control request on the stream it already holds open.

## Files read

- `internal/sessions/pool.go` → `inBandDeliverable`, `deliverSettingsInBand`,
  `UpdateSettings`, `validatePermissionUpdate` — the routing split, the delivery
  site and its two escalation stops; `UpdateSettings`' branch comment claims the
  enable is the surviving `Restart` caller.
- `internal/sessions/session.go` → `permissionModeInBand`, `permissionModeKnown`,
  `canonicalPermissionMode`, `claudeSettingsArgs`, `operatorBypass` — four readers
  of one predicate, three of which must NOT see the escalation.
- `internal/sessions/registry.go` → `permissionModeForDisk`, `settingsFromEntry`,
  `registryEntry` — the on-disk vocabulary; reads `permissionModeInBand`.
- `internal/sessions/runner.go` → the `Runner` interface's `SetPermissionMode` and
  `SetSpawnPermissionMode` doc blocks — both argue from "the escalation is not
  in-band-deliverable".
- `internal/sessions/runnerstate.go` → `RunnerConfig.PermissionMode`,
  `RunnerConfig.OperatorBypass` — the first claims re-granting bypass stays on the
  respawn path; the second is the provenance bit the spawn interlock reads.
- `internal/streamsup/envelope.go` → `permissionModeAllowed`, `WritePermissionMode`,
  `marshalPermissionModeEnvelope`, `permissionModeDefault` — the closed writer
  allow-list, its length bound, and the encoder it gates.
- `internal/streamsup/runner.go` → `Config.SpawnPermissionMode`, `Config.OperatorBypass`,
  `SetPermissionMode`, `SetSpawnPermissionMode`, `RevokeBypass`, `spawnAndWait`,
  `bypassPermissionsFlag` — the second reader of the allow-list is the spawn-time
  posture decision, which must not move.
- `internal/relay/v2session_settings.go` → `validPermissionMode` — READ ONLY,
  confirmed it must stay closed; its doc's escalation bullet is untouched by this
  change.
- `internal/e2e/internal/fakeclaude/main.go` → `writeSetPermissionModeAck` — the
  fake echoes any requested mode and deliberately does not apply the daemon's
  allow-list, so a hermetic run can observe a bypass ack.
- `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go` →
  `TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode`,
  `seedBypassRegistry`, `revokeTap`, `newRevokeLogRecorder` — the live rig AC 5
  extends; its assertion set (init mode, control_response count, pid, spawn count)
  is the shape the escalation arm mirrors.
- `internal/streamsup/posture_gate_test.go` →
  `TestRunner_SpawnPermissionMode_ResidualArmDoesNotBrickABypassRespawn`,
  `TestRunner_SpawnPermissionMode_ProvenanceDecidesTheWrite` — the two specs that
  already redden if the spawn-time decision is widened in place.
- `internal/sessions/pool_update_settings_inband_test.go` → `TestInBandDeliverable`,
  `TestPool_DeliverSettingsInBand_EnableWritesNothing` — the partition table and
  the guard test this ticket inverts.
- `docs/knowledge/features/streamsup-package-posture-gate-spawn-permission-mode-ack.md`
  — the posture gate's contract: a gate no write can release is a bricked session.

## Context

Five of six postures already change in band. `bypassPermissions` alone kills and
relaunches the session, because claude gated the escalation on the launch argv.
#2065 removed that gate — every child now launches with
`--dangerously-skip-permissions` and is walked back in band — and #2060 measured
claude accepting the re-escalation on such a child at 2.1.239
(`internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_reescalate.json`).
This ticket turns that measurement into a routed verb.

No ADR is warranted. The decisions here restate existing ones for a widened
vocabulary; they belong in the doc comments the acceptance criteria already name.

### Size

Over the size-S table on two lines, deliberately: 6 production source files
(ceiling 5) and ~1140 lines of total written work (ceiling 800). The constraint
that predicts turns holds — 7 production call sites need simultaneous review
against a ceiling of 10, and two of the six files are doc-comment-only.

The floor is why it is not split. There is one deliverable and both candidate
slices are one-consumer slices: sessions-side routing without the writer leaves
the escalation refused at `WritePermissionMode`, logged and swallowed, so the
posture silently does not change — worse than today's respawn; the writer without
the routing has exactly one consumer, the other half. Split depth is 1 (parent
#1686, no grandparent), so a split was available and was declined on the floor,
not blocked. Estimate re-derived independently and it agrees with the refiner's
~1100; the reduction is one file — `internal/sessions/registry.go` needs no edit
at all (see Design § 5).

## Design

Nothing changes shape. Two predicates gain one member's worth of reach each, and
each widening is confined to its intended consumer by changing the CALL, never the
predicate other consumers read.

### 1. The routing open — `inBandDeliverable` (`internal/sessions/pool.go`)

Two clauses refuse the escalation and both must go, because the two spellings take
different clauses and `internal/relay`'s `validPermissionMode` means a mobile client
can only ever send the bit:

- The `update.YOLO != nil && *update.YOLO` clause is DELETED outright. With it gone
  a `yolo:true` update falls through to the remaining rejects, so a `yolo:true`
  beside a cleared Model still takes the restart, unchanged.
- The mode clause changes predicate: `!permissionModeInBand(...)` →
  `!permissionModeKnown(...)`.

`permissionModeKnown` is already exactly "the in-band five plus the escalation",
i.e. every posture this daemon can store — and after this ticket every posture it
can store is one it can deliver in band. No new predicate is minted for a set that
already has a name and one definition; a duplicate would be a second copy of one
membership rule rather than a second defence. Its doc gains the sentence naming the
condition under which the two questions would diverge (a future mode that stores
but cannot be delivered), and that a mode outside it is still refused by
NON-MEMBERSHIP, so every unanticipated spelling is refused with it.

`permissionModeInBand` itself is NOT touched. It stays the five NON-ESCALATING
modes, which is the property its other three readers depend on, and that is the
whole containment mechanism for the collateral consumers the ticket enumerates:

| Reader | Why it is safe |
|---|---|
| `canonicalPermissionMode` | reads the unchanged five → `(bypassPermissions, yolo:false)` still degrades to default |
| `claudeSettingsArgs` | reads the unchanged five → still never emits `--permission-mode bypassPermissions` |
| `permissionModeForDisk` | reads the unchanged five → escalation still serialises to `""`, carried by the yolo key |
| `permissionModeKnown` | already names the escalation explicitly; unchanged |

### 2. The delivery open — `deliverSettingsInBand` (`internal/sessions/pool.go`)

The `merged.PermissionMode == permissionModeBypass` early return is deleted, so an
escalation reaches `sup.SetPermissionMode(merged.PermissionMode)` like any other
posture. The arithmetic the guard sat inside is unchanged: still EXACTLY ONE send
for an update naming either posture field.

### 3. The writer open — `permissionModeAllowed` (`internal/streamsup/envelope.go`)

The switch keeps its shape and gains exactly one member. The package needs a
`permissionModeBypass = "bypassPermissions"` const beside `permissionModeDefault`,
so the mode literal has one spelling here, as `bypassPermissionsFlag` does for the
flag. #1603's "this package's production source names no escalating mode" property
ends here by design; what replaces it is that the escalation has one named constant
and two readers, and the doc says so.

The length bound is restated against the new longest member: `bypassPermissions` is
six bytes longer than `acceptEdits`, so the envelope grows by six and stays far
under `PIPE_BUF`. The exact byte count is measured in Phase B and pinned by a test
rather than asserted in prose.

### 4. The spawn-time decision does NOT widen — `spawnAndWait` (`internal/streamsup/runner.go`)

`permissionModeAllowed` has a second production reader, and it must keep answering
"no" for the escalation. A new one-line predicate carries the carve-out:

```go
func permissionModeSpawnWritable(mode string) bool  // permissionModeAllowed(mode) && mode != permissionModeBypass
```

`spawnAndWait`'s `postureID` condition reads it instead. Subtraction from the
allow-list, not a second switch, so a seventh mode added to the vocabulary becomes
spawn-writable automatically — which is the right default — and the two lists
cannot drift.

Why the escalation is excluded is not merely conservatism. Since #2065 the launch
argv already asserts the escalation on every child, so a bypass session's fresh
child is ALREADY in the posture its stored settings ask for: there is nothing to
walk it back to, and the write would be pure redundancy. It would also be the first
control request on a fresh stream, which nothing has measured — #2060 captured the
escalation on an ESTABLISHED stream after two turns
(`init_permission_modes: ["bypassPermissions", "default", "bypassPermissions"]`).
Keeping the exclusion keeps `arm("")` for a bypass spawn, so its turns flow with no
ack dependence, which is what AC 3's "unchanged, not assumed" asks for.

### 5. Doc restatements

Each of these argues today from "the escalation is not in-band-deliverable" or
"re-granting bypass stays on the respawn path". Each is restated for the new
posture, never deleted:

- `internal/sessions/pool.go` — `inBandDeliverable`'s first bullet (the split is on
  the posture asked for; the escalation now goes in band too), `deliverSettingsInBand`'s
  "second of three independent stops" paragraph (now one stop: the writer's
  allow-list admits it and this site delivers it), `UpdateSettings`' two-mechanism
  list and its `Restart` non-deletion argument — the enable no longer reaches
  `Restart`; a Model or Effort cleared to `""` still does, and that is what keeps
  the call alive.
- `internal/sessions/session.go` — `permissionModeInBand`'s doc gains the reason it
  stayed at five while claude's in-band vocabulary grew to six, and names its three
  non-routing readers; `claudeSettingsArgs`' "exactly one spelling" bullet is
  restated as a live invariant with #2066's non-widening as its mechanism;
  `permissionModeKnown` and `canonicalPermissionMode` per § 1.
- `internal/sessions/runner.go` — `SetPermissionMode`'s allow-list paragraph (the
  set is now six and the escalation is delivered here, not by respawn);
  `SetSpawnPermissionMode`'s rejected-shape bullet keeps its verdict on a NEW
  reason: the piggyback still misses the restart branch, which a cleared Model or
  Effort still takes and which never calls `SetPermissionMode`.
- `internal/sessions/runnerstate.go` — `RunnerConfig.PermissionMode`'s bypass
  paragraph: the writer admits the escalation now, but the SPAWN path still writes
  nothing for it (`permissionModeSpawnWritable`), so a bypass session's turns are
  still never gated; re-granting bypass is in band.
- `internal/streamsup/envelope.go` — `permissionModeAllowed` per § 3;
  `WritePermissionMode`'s "no escalation reachable from this surface" paragraph,
  restated as what the surface actually guarantees now (a vocabulary gate, with
  authorisation owned upstream by `Pool.UpdateSettings` and `validPermissionMode`).
- `internal/streamsup/runner.go` — `Config.SpawnPermissionMode`, `SetPermissionMode`,
  `SetSpawnPermissionMode`'s rejected-shape list, and `spawnAndWait`'s
  unconditional-arm comment, whose worked example is replaced: an un-acked default
  child followed by an in-band escalation and then a CRASH-respawn takes exactly the
  path that needs `arm("")`, since `SetSpawnPermissionMode` has installed
  `bypassPermissions` as the spawn posture by then.

`internal/sessions/registry.go` needs NO edit, contrary to the ticket's estimate:
`permissionModeForDisk` reads the unchanged `permissionModeInBand`, so its third
clause alone already maps the escalation to `""` and its doc's claim ("only the four
non-default in-band modes are ever written") stays literally true.

`internal/relay/v2session_settings.go` is not edited. Its `validPermissionMode`
stays closed and its recorded reasoning survives verbatim: the wire keeps one
spelling for the escalation.

## Concurrency model

Unchanged; no goroutine is added, removed or re-ordered.

`Pool.UpdateSettings` releases `p.mu` before the live-apply and both branches stay
where they are. `deliverSettingsInBand` now calls `Runner.SetPermissionMode` on one
more input value; that method takes `restartMu`-free `Stdin()` and no Pool lock, so
the lock order (`Pool.mu` → `Session.lcMu`, never inverted) is untouched.

The one behavioural coupling is the posture gate. `(*streamsup.Runner).SetPermissionMode`
retargets `postureGate` after a successful write (#2064), and until now an escalation
never reached that method. So an in-band escalation now CLOSES the gate until claude
acks the new request id, and turns are held in the interim — the same window every
other in-band posture change already has.

## Error handling

- **Claude NAKs the escalation.** The write succeeded, so the gate is retargeted at
  the escalation's id; a NAK marks it refused and the session holds turns until
  another in-band posture change retargets the gate. #2060 measured the ack at
  2.1.239 on a flag-launched child and #2065 makes every child flag-launched, so
  the NAK is not expected — but if it happens, the operator's remedy is a posture
  change to any other mode, which is in-band-deliverable and reaches
  `SetPermissionMode`, so the recovery path documented at #2064 covers it unchanged.
  Nothing is coded for this case; it is recorded because the ticket asks.
- **The write fails (no live child, EPIPE mid-teardown).** Fire-and-forget, exactly
  as today: logged at Info via `notDelivered`, swallowed, the caller still sees
  success. The retarget is skipped on error, so a line that never reached the child
  cannot point the gate at an id nothing can ack.
- **A contradictory `(mode, yolo)` frame.** Rejected before anything mutates, by
  `validatePermissionUpdate` → `ErrPermissionModeConflict`. Unchanged.
- **An unrecognised mode.** Still refused twice by non-membership: at
  `validatePermissionUpdate` (`permissionModeKnown`), and again at the writer
  (`permissionModeAllowed`). Neither error echoes the rejected value (#833).
- **The pair invariant.** `merged.YOLO == (merged.PermissionMode == permissionModeBypass)`
  holds through the in-band branch as it does through the restart branch, because the
  derivation runs in `UpdateSettings` above the split and neither branch re-derives
  it. Two collateral consumers are safe only while it holds, and the tests below pin
  both halves.

## Testing strategy

RED first in every case: the escalation rows of the existing tables are inverted
before any production line moves.

**`internal/sessions`**

- `TestInBandDeliverable` — the four escalation rows flip to `true` (`yolo enable
  only`, `model with yolo grant`, `escalation as a mode`, `escalation as a mode
  beside a model`). The garbage rows (`unknown mode`, `empty mode`, `Plan`) stay
  `false`, so the widening is by membership and not by opening the clause.
- `TestPool_DeliverSettingsInBand_EnableWritesNothing` → renamed and inverted: both
  spellings now produce exactly one `SetPermissionMode("bypassPermissions")` and no
  user turn.
- New `TestPool_UpdateSettings_InBand_Escalation_NoRespawn` — **AC 1**, table over
  both spellings against a live double: zero `Restart` calls, exactly one
  `SetSpawnArgs`, `permissionModes() == ["bypassPermissions"]`, and the persisted
  entry carries `yolo:true`.
- **AC 3 containment pins**, each reddening if `permissionModeInBand` is widened in
  place rather than the call being changed: `canonicalPermissionMode("bypassPermissions",
  false) == "default"`, and `claudeSettingsArgs` emitting no `--permission-mode
  bypassPermissions` for a hand-built `{YOLO:false, PermissionMode:"bypassPermissions"}`.
  Existing coverage is checked first and extended only where a row is missing.

**`internal/streamsup`**

- `WritePermissionMode` accepts `bypassPermissions` and emits the measured envelope;
  the refusal rows for near-misses and unknown spellings stay.
- A length pin on the envelope for the new longest member, so the `PIPE_BUF`
  argument is a measurement and not a claim.
- A direct table for `permissionModeSpawnWritable`: the five in band true, the
  escalation false, garbage false.
- `TestRunner_SpawnPermissionMode_ResidualArmDoesNotBrickABypassRespawn` and
  `TestRunner_SpawnPermissionMode_ProvenanceDecidesTheWrite` are left ALONE and must
  stay green — they are the existing red for a spawn-site widening, which is AC 3's
  "unchanged" half.

**AC 4** needs no new test: `SetPermissionMode` stays on `sessions.Runner` and
`cmd/pyry`'s `var _ sessions.Runner = streamRunner{}` is the compile-time proof.

**`internal/e2e/realclaude` — AC 5**

A new arm in `interactive_stream_inband_bypass_revoke_test.go`, mirroring the revoke
test it sits beside: seed the registry at `yolo:false`, drive one turn, call
`Pool.UpdateSettings(YOLO=true)`, wait for the `control_response`, drive a second
turn, then assert from claude's own per-turn `system/init` that the LAST reported
`permissionMode` is `bypassPermissions` (first is `default`), the pid is unchanged
and the spawn count is 1. The posture gate is deliberately left unwired in this arm,
as it is in the revoke arm: a closed gate would convert a NAK into a turn-2 timeout
and destroy the diagnostic the arm exists to produce. Verified by counting `=== RUN`
lines, never the exit code.

**Gate**: `go test -race` on `internal/sessions`, `internal/streamsup` and
`internal/e2e/...`, plus `go vet ./...` and `go build ./cmd/pyry`. The full-module
race suite and the live gate are the verifier's.

## Open questions

1. The exact byte length of the `set_permission_mode` envelope for the new longest
   member — measured in Phase B and pinned by the length test, not estimated here.
2. Whether `claudeSettingsArgs` and `canonicalPermissionMode` already have an
   explicit `(bypassPermissions, yolo:false)` row. If they do, AC 3's containment
   pins are existing tests referenced rather than new ones written.
3. `RevokeBypass` (`internal/streamsup/runner.go`) is still callerless and its doc
   still marks it a deletion candidate. Out of scope here per the ticket; it should
   be its own change.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] **No findings**, and the reachable set was enumerated rather
  than assumed. `sessions.SettingsUpdate` has exactly ONE production producer —
  `cmd/pyry`'s `settingsUpdaterAdapter`, fed only by `internal/relay`'s
  `handleSetSessionSettings`, which validates BEFORE constructing: `validPermissionMode`
  refuses `bypassPermissions`, and a frame carrying a mode AND a YOLO bit is refused
  outright as malformed. So the wire's only spelling of the escalation stays
  `yolo:true` on an already-paired, already-authenticated session. This ticket adds
  no principal, no spelling and no new boundary — only a delivery mechanism. Both
  daemon-side gates (`permissionModeKnown` inside `validatePermissionUpdate`,
  `validPermissionMode` at the wire) are untouched.
- [Trust boundaries] **SHOULD FIX — accepted defence-in-depth reduction, with a
  Phase B constraint.** `WritePermissionMode`'s "no escalation reachable from this
  surface" property ends by design here: after the widening the escalation's
  structural stops are the wire validator and `Pool.UpdateSettings`, not the writer.
  That is only safe while three functions keep the caller counts they have today —
  `marshalPermissionModeEnvelope` one caller (`WritePermissionMode`, which holds the
  gate; a second caller is the moment the gate must move down into the encoder),
  `WritePermissionMode` two callers inside `internal/streamsup` (`(*Runner).SetPermissionMode`
  and `spawnAndWait`), and `Runner.SetPermissionMode` one caller inside
  `internal/sessions` (`deliverSettingsInBand`). Phase B adds a caller to none of
  them. `WritePermissionMode`'s own doc currently miscounts this, claiming
  `SetPermissionMode` is its "only in-repo caller" while `spawnAndWait` calls it too;
  the count is corrected as part of that block's restatement, since the security
  argument above rests on it.
- [Subprocess / external command execution] **No findings**, by a named mechanism
  rather than by inspection. No caller-supplied string reaches `exec.Command`:
  `claudeSettingsArgs` reads the UNCHANGED five-member `permissionModeInBand`, so
  `--permission-mode` can only ever carry one of five fixed literals and never the
  escalation, and `permissionModeKnown` bounds what the registry can hold so a warm
  start cannot compose an argv from an unrecognised value either. The escalation flag
  itself has been unconditional since #2065; this ticket changes no argv byte. No
  `sh -c`, no environment change.
- [Network & I/O] **SHOULD FIX — must land in Phase B.** Allow-list membership is
  what bounds the emitted control line's LENGTH, and that bound is a real safety
  property: one `write(2)` under `PIPE_BUF` is what stops a control line tearing and
  interleaving with a concurrent `WriteTurn` on the same fd. Measured, not estimated:
  the envelope is 109 bytes for `acceptEdits` and 115 bytes for `bypassPermissions`
  (newline included), against a POSIX `PIPE_BUF` floor of 512. Safe — but it must be
  PINNED BY A TEST, not restated in prose, because the next widening inherits the
  constraint and prose does not redden. The verifier should check the test landed.
- [Concurrency] **No findings, and this is the security rationale for Design § 4.**
  An in-band escalation now retargets `postureGate`, so a NAK holds the session's
  turns until another in-band change retargets it. That failure class is pre-existing
  — every other in-band posture change already carries it, with the same recovery
  path — so the escalation joins it rather than creating it. What WOULD create a new
  and unrecoverable case is widening the SPAWN-time decision alongside the writer: a
  fresh child's gate would arm closed on a write nothing has measured as a first
  control request, and a NAK there refuses turns from the session's very first one.
  `permissionModeSpawnWritable` is the carve-out that prevents it and
  `TestRunner_SpawnPermissionMode_ResidualArmDoesNotBrickABypassRespawn` is the red
  that catches its removal. No lock is added and no lock order changes: the delivery
  runs after `p.mu` is released and takes only runner-internal locks. No goroutine is
  spawned.
- [Concurrency / crash safety] **No findings.** `UpdateSettings` persists before the
  branch split, so a crash between persist and delivery leaves disk escalated and the
  child NOT escalated — less privilege than stored, the fail-safe direction — and the
  next spawn's launch argv grants it. The mirror-image window belongs to the revoke
  direction and is unchanged by this ticket.
- [Error messages, logs, telemetry] **No findings**, structurally rather than by
  discipline. The change adds no log call and no error that echoes a value:
  `notDelivered` records the constant field NAME `"permission_mode"`,
  `WritePermissionMode`'s wraps carry no mode, and both `ErrUnsupportedPermissionMode`
  sentinels stay bare — which is what makes logging them verbatim safe. #833 holds.
  Phase B adds no record naming a mode value.
- [Tokens, secrets, credentials] **Not applicable**, with the reason: this ticket
  touches no credential material, no token lifecycle and no revocation surface. The
  control-request `request_id` is a correlation id minted from an atomic counter on a
  pipe the daemon owns, not a capability; the escalation does not change that, and
  the ack it correlates arrives on the child's own stdout.
- [Cryptographic primitives] **Not applicable** — no randomness is generated, no key
  is derived or reused, and nothing is compared against a secret, so there is no
  constant-time-comparison surface.
- [File operations] **Not applicable** — no filesystem path is composed from any
  value this ticket touches. The registry write is the existing atomic
  temp-file-plus-rename at `0600`, and `permissionModeForDisk` still maps the
  escalation to `""`, so the `yolo` key remains its one on-disk spelling and the disk
  cannot hold a mode contradicting it.
- [Threat model alignment] **No findings.** `docs/protocol-mobile.md` § Security
  model's relevant threat is a paired-but-hostile client escalating the daemon's
  permissions; the mitigation of record — exactly one spelling on the wire, the mode
  string refused — is preserved verbatim. The local-operator escalation path
  (pass-through claude args) is out of scope and already modelled as
  `RunnerConfig.OperatorBypass` provenance, which this ticket does not touch.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
