# 2080 — Prove the retained background-task roster reaches a late-connecting client

## Files read

- `internal/e2e/relay_v2_stream_slash_command_list_reconcile_test.go` → `driveLateConnectSlashCommandList`,
  `TestRelayV2_StreamSlashCommandListReachesLateConnectingPhone` — the twin (#2009). Its three-conn
  sequencing, its happens-before argument and its "two producers ⇒ the mutant is the AC" header are
  what this spec copies.
- `internal/relay/v2session_rosterreconcile.go` → `reconcileBackgroundTaskRosters` — the seam under
  test: nil-`RetainedBackgroundTaskRosters` early return (the mutant), `len(retained) == 0` return
  (AC4's mechanism), `EventID` left nil, no per-payload emptiness filter.
- `internal/relay/v2session_rosterreconcile_test.go` → the reconcile's unit gate. It already pins the
  empty-roster frame (`"tasks":[]`) and the reconciled frame's absent `event_id`; neither needs an
  e2e arm.
- `cmd/pyry/relay.go` → `startRelayV2`'s `RetainedBackgroundTaskRosters` assignment — the one line the
  mutant unsets.
- `cmd/pyry/session_background_task_list.go` → `retainedBackgroundTaskRosters`,
  `resolveBoundBackgroundTaskRoster` — the enumeration walks the CONVERSATION registry, which is why
  a conversation must be minted over the wire before anything is enumerable.
- `cmd/pyry/interactive_turn_v2.go` → the `BackgroundTaskStarted, BackgroundTaskUpdated,
  BackgroundTaskRoster` arm of `interactiveTurnEmitterV2.Handle` — the SECOND producer, and the one
  that makes "the frame arrived" vacuous on its own.
- `internal/streamsup/parser.go` → `emitBackgroundTaskRoster`, `systemBackgroundTaskEntry`,
  `maxTaskRosterEntries` (8), `maxTaskRosterDescription` (512), `maxTaskFieldID` (256) — the decode
  the rider must satisfy and the cap that makes `dropped_tasks` non-zero.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON`, `writeRateLimitEvent`,
  `writeJSONLine`, the top-of-file knob doc block — the rider precedent and its documentation duty.
- `internal/e2e/relay_v2_stream_rate_limit_test.go` → `driveRateLimitTurn`, `rateLimitRiderEnv` — how
  a rider env reaches the child and how its fed values are re-declared test-side across the
  main-package boundary.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay` — takes `extraEnv ...string`, so the
  rider needs no harness change at all.
- `internal/protocol/interactive.go` → `BackgroundTaskRosterPayload`, `BackgroundTask` — the decode
  target and its field set.
- `docs/protocol-mobile.md` → § `background_task_roster`, § Reconnect / Backfill semantics,
  § `slash_command_list`'s **Reconcile on (re)connect** note — the two edit sites and the note's shape.
- `docs/knowledge/features/e2e-harness-stream-interactive-harness-pattern-startstreamin.md` §§ #1868,
  #2009 — three lessons carried below: the minter-side counter is a race and must be logged rather
  than asserted; a first-arrival window has no positive terminator, so the MUTANT run costs the full
  deadline; a dead conn's discarded closure is a compile-time guard.

## Context

#2077 retains the roster, #2078 reconciles it on connect, #2079 wires the enumeration seam. Each is
proven against its own seam or fake; nothing proves the chain across process boundaries for the
client the reconcile exists for. `internal/e2e` has no background-task coverage at all today.

Two producers emit `background_task_roster`: the live turn lane (`interactiveTurnEmitterV2.Handle`)
and `reconcileBackgroundTaskRosters`. A test that waits for the frame and nothing else is satisfied
by the live-lane emit and stays green against a dead reconcile — so the non-vacuity proof (AC2) is
the acceptance criterion, not a nicety.

No ADR is warranted: this ticket adds a proof and a rider, and every design rule it relies on is
already published.

## Design

Three deliverables, one commit each is not required — one feature commit plus the spec commit.

### 1. The fakeclaude roster rider

`internal/e2e/internal/fakeclaude/main.go`.

- New knob `PYRY_FAKE_CLAUDE_STREAM_ROSTER` (const `envStreamRoster`), documented in the top-of-file
  doc block beside the other knobs — that block is not optional in this file.
- New writer `writeBackgroundTaskRoster(w io.Writer, entries int) error`, a `map[string]any` line in
  `writeRateLimitEvent`'s shape (sorted keys ⇒ deterministic without declaring a struct for a shape
  nothing else reads). It writes `{"type":"system","subtype":"background_tasks_changed","tasks":[…],
  "uuid":…,"session_id":…}`.
- `runStreamJSON` gains a trailing `rosterTasks int`. When `> 0` it writes the roster line **before**
  the reply, exactly where the bogus and rate-limit riders write theirs and for their stated reason:
  a `turn_end` reaching a client implies the fed line has already been through the parser, so no
  sleep, no poll and no ordering race to tune. `0` ⟹ off ⟹ byte-identical.

**Why the knob carries a COUNT rather than a bool.** `runStreamJSON`'s own doc states that
`withholdModeAck` was appended rather than grouped *because* the resulting `string, bool` tail makes
a mis-slotted call-site edit a compile error, "which a third adjacent bool would not". An `int`
keeps that property (`bool, int`), and the value is load-bearing rather than decorative: it is the
number of entries the rider cans, which is how the test drives the roster **over**
`maxTaskRosterEntries`.

**The fixture.** Entry 0 is the committed capture's own row transcribed verbatim
(`internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json`, its `background_tasks_changed`
record): `task_id` `bybi8g8i8`, `task_type` `local_bash`, `description` `cat $FIFO`. The capture
holds exactly one row, so entries 1..N-1 are synthetic siblings with distinct ids under the same
key set — invented VALUES over a transcribed SHAPE, stated as such in the writer's doc. `uuid` and
`session_id` are carried (every real line has them) even though the parser's decode target declares
neither; carrying them is the point, as `TestNewSessionParser_DecodesAndRetainsBackgroundTaskRoster`
already argues for the same line.

**Call-site fan-out.** The new parameter touches 17 call sites (1 production, 16 in the fakeclaude
package's own tests), all `, 0` appends. That is over the size table's 10-call-site line and is
recorded as an overage rather than routed for a split — see § Sizing below.

### 2. The e2e proof

New file `internal/e2e/relay_v2_stream_background_task_roster_reconcile_test.go`, `//go:build e2e`,
structurally the twin with the payload type substituted and one fixture difference.

`driveLateConnectBackgroundTaskRoster(t) lateRosterObservation` — three paired devices, all paired
before the daemon starts; one `StartStreamInteractiveWithRelay(…, rosterRiderEnv+"=9")`; one
`dialInteractive` factory so each conn gets its own CipherState pair.

1. **phone-empty** handshakes interactive while the conversation registry is still empty, drains a
   2s window and COUNTS `background_task_roster` frames — never decodes one. Its seal-and-send
   closure is discarded on the floor: the conn is dead after its own window
   (`fakephone.Client.ReceiveBytes` closes on a cancelled read context), and discarding the closure
   makes a later reuse a compile error rather than a decrypt error that reads like a daemon bug.
2. **phone-a** handshakes, mints a conversation over the wire (`create_conversation`, all-null),
   then drives one `send_message` and drains to `turn_end`. Both windows count
   `background_task_roster` frames into `rostersOnMinter`.
3. **phone-b** dials only now, handshakes, and sends nothing else — its closure is discarded too. It
   drains to the first `background_task_roster`, then runs a 2s settle window counting extras and
   any turn frame.

`lateRosterObservation` fields: `emptyConnRosters int`, `convID string`, `sawEcho bool`,
`sawTurnEnd bool`, `rostersOnMinter int` (diagnostic only), `roster
protocol.BackgroundTaskRosterPayload`, `found bool`, `extraOnObserver int`,
`turnFramesOnObserver int`. Milestones are VALUES rather than helper-side assertions, the twin's
reason: a frame count read off a run whose turn never completed proves nothing.

`TestRelayV2_StreamBackgroundTaskRosterReachesLateConnectingPhone` asserts, in order:

- AC4 first — `emptyConnRosters == 0`. Its "handshake completes normally" half is discharged by
  `driveHandshakeToOpenDaemonInteractive` having returned at all.
- Milestones `sawEcho` / `sawTurnEnd`, fatally: without a completed turn the happens-before never
  engaged and an AC1 result is uninterpretable rather than failed.
- AC1 — `found`; `roster.ConversationID == convID`; `len(roster.Tasks) == 8`;
  `roster.DroppedTasks == 1`; row 0 verbatim against the captured row; each row's
  `TruncatedFields == nil`; the two over-cap ids present in no delivered row.
- AC2's cardinality half — `extraOnObserver == 0` (exactly one conversation is retained ⇒ exactly one
  envelope).
- AC3 — `turnFramesOnObserver == 0` (`turn_state` and `turn_end` counted together).

Every failure message carries `rostersOnMinter` alongside the observer-side count, which is AC2's
"a miss names which of the two producers failed".

**Why `dropped_tasks` is driven non-zero.** A fixture whose `dropped_tasks` is always 0 cannot
distinguish "the count was carried" from "the field was never populated" — the count decodes to 0
either way. Driving the rider at 9 against `maxTaskRosterEntries`'s 8 makes the assertion a
pass-through claim on a distinctive value. It is NOT a cap test: the truncation itself is the
parser's, pinned in `internal/streamsup`, and the coupling to that constant is deliberate — if the
cap moves, this test SHOULD go red, and its message names the constant.

**`rostersOnMinter` is logged, never asserted.** #1868/#2009 measured the spawn-time twins' minter
counter as a genuine unordered race. This frame's live-lane emit is mid-turn rather than at spawn,
so the race argument does not transfer and the count should be ≥ 1 — but asserting it would redden
this spec for a change in a lane it does not own.

### 3. `docs/protocol-mobile.md`

- § Reconnect / Backfill semantics, Mode B: add this frame to the "Reconcile on connect" bullet and
  to the match-and-replace bullet, keyed on `conversation_id`, citing #2078/#2079 for the mechanism
  and #2080 for the proof. The list goes to five named against six running; `model_list`'s absence
  is left standing deliberately (sibling family, twice deferred).
- § `background_task_roster`: add a **Reconcile on (re)connect** paragraph in the shape its Mode B
  neighbours carry, stating (a) the empty-case rule — a reported-empty roster arrives as an explicit
  empty snapshot, an unreported one is simply ABSENT, which is the reverse of `slash_command_list`'s
  rule in this same document — and (b) the qualification of the section's existing `event_id` claim:
  the live-lane frame carries one, the reconciled frame deliberately does not.
- A changelog entry naming what was added and what was left standing.

## Concurrency model

None added. The test is one goroutine driving three conns strictly sequentially; whatever the daemon
sends phone-a after its windows close buffers unread on phone-a's socket. The rider writes on
`runStreamJSON`'s single reader goroutine. No sleeps, no polls, no retry loops.

## Error handling

The driver `t.Fatalf`s only on transport, seal and decode faults, on an error envelope, and on the
mint never completing — never on an acceptance criterion; on a window deadline it returns what it
has and logs counts. A decode failure of the observed roster IS the defect and fatals, without
printing the raw bytes.

**Never log a decoded payload.** All four strings a row carries (`task_id`, `task_type`,
`description`, every `truncated_fields` entry) are claude-authored untrusted text and `description`
is a literal command line for `local_bash` (#833). Deadline diagnostics report counts and ids only;
the empty-registry and settle windows COUNT and never decode. Row assertions name a got and a want
field by field — both are canned literals — and dump no payload.

## Testing strategy

- `go test -tags e2e -race ./internal/e2e/... -run BackgroundTaskRoster` green.
- `go test -race ./internal/e2e/internal/fakeclaude/...` green (the rider's own package tests).
- **The mandated RED**, via `go test -overlay` over a copy of `cmd/pyry/relay.go` with the
  `RetainedBackgroundTaskRosters` assignment removed: the reconcile takes its nil-seam early return,
  phone-b receives nothing, the test fails. Expect the red run to cost the observer window's full
  deadline — a first-arrival window has no positive terminator (#1868's lesson). The mutation is
  never committed.
- `go vet ./...`, `go build ./cmd/pyry`.

## Sizing

The 10-call-site line is exceeded (17). The split it would imply is the one the ticket's own
`Estimate:` line already refused, and correctly: the rider's only consumer is this test, so a rider
slice would ship wired to nothing and could not be verified on its own. The floor beats the ceiling,
the overage is stated here, and the work is built as one ticket. Every other line of the table holds
— 1 production source file, 0 new exported types, 5 acceptance criteria, no reject branches, ~800
lines of total written work.

## Open questions

- Whether `rostersOnMinter` converges on 1 or on a higher count once the mid-turn lane is measured
  rather than reasoned about. Resolved by the mutant run; recorded under Revisions if it surprises.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. The subprocess-stdout → parent-state crossing is
  `streamsup.Parser`'s, already owning the caps, the UTF-8 replacement and the no-log rule; this
  ticket only feeds it. What IS new: `internal/e2e` becomes the first package in the suite to hold
  decoded roster rows, so its assertion helpers are a new **sink** for the four untrusted strings.
  SHOULD FIX — the field-by-field `got`/`want` shape is safe here only because both sides are
  fakeclaude's own canned bytes; the test header must say so and must forbid copying the shape into
  `internal/e2e/realclaude`, where the same payload carries the operator's real workspace.
- **[Tokens]** SHOULD FIX — three real pairing tokens are minted and held in memory by the driver,
  and the natural instinct on a handshake failure is to dump what was sent. No failure path in this
  file may echo a token: dial and handshake failures name the phone and the error only. The static
  pubkey is public and may be printed.
- **[File operations]** Not applicable, by construction rather than by luck: the ticket introduces
  exactly one new caller-supplied value (the rider env) and it is parsed with `strconv.Atoi` into a
  count, never used as a path. A malformed or empty value must fail **closed** — `0`, rider off —
  never a panic and never a default-on.
- **[Subprocess execution]** No finding, and the fixture is chosen to prove it: the captured
  `description` is `cat $FIFO`, a literal shell command line with a metacharacter. It is built into
  the line by `json.Marshal` over a `map[string]any` (never a shell, never `os.Expand`) and asserted
  verbatim four layers later, so the whole path is shown to treat it as inert bytes. This is the
  family's central security property (#833 / `local_bash`), and it is now covered end to end.
- **[Cryptographic primitives]** No finding, made structural rather than promised: the Noise receive
  nonce is a lockstep counter, so three conns sharing one `CipherState` pair — or any filtering
  before decrypting — desyncs it and surfaces as a misleading decrypt error rather than a clean
  failure. The single `dialInteractive` factory pairs each phone with its own states at one call
  site. No send-nonce is burned for an undeliverable frame either; `reconcileBackgroundTaskRosters`
  enqueues rather than seals (#874), which this ticket does not change.
- **[Network & I/O]** No finding on caps: the rider's 9 short rows sit far under every producer cap
  and under the frame's byte bound, and this ticket adds no cap of its own. Timeout discipline is a
  real constraint here — every window carries an explicit deadline, and the observer's must be sized
  against the MUTANT run rather than the green one, because a first-arrival window has no positive
  terminator and the red path costs its whole budget (#1868).
- **[Error messages, logs, telemetry]** No finding in the test's own output — deadline diagnostics
  carry counts and ids only, and the empty-registry and settle windows count without ever decoding.
  OUT OF SCOPE: nothing here reads `h.Stderr`, so a future regression that logged a task description
  from the daemon would not redden this spec. That sweep belongs where the emit path is, not in a
  delivery proof, and the reconcile's and parser's own packages already pin their content-free logs.
- **[Concurrency]** No finding. One goroutine drives three conns strictly sequentially; the rider
  writes on `runStreamJSON`'s single reader goroutine; no goroutine is spawned and every conn has a
  `t.Cleanup` close. The happens-before that replaces a sleep rests on two facts worth stating in
  the driver's doc rather than assuming: the parser processes lines in order on one goroutine, and
  the roster line precedes the turn's result line — so `turn_end` on the minter implies the roster
  line's work is complete, whichever side of the hold's delegation the record happens on.
- **[Threat model alignment]** OUT OF SCOPE and it must be named as a NON-CLAIM in the test header,
  because it is the misreading a green run most invites: three paired devices here are a
  **sequencing device, not a confinement proof**. The seam is enumerate-all —
  `RetainedBackgroundTaskRosters` takes no argument — so the late observer legitimately reads a
  conversation minted by a different device. Per-device confinement belongs to the Mode B umbrella
  (#829), as the slash-command twin also records.
- **[Threat model alignment, second]** SHOULD FIX — AC4's soundness must be argued, not assumed. A
  roster reaching the empty-registry conn would redden it, and two independent facts prevent that:
  the rider fires per user turn and no turn is driven before that window closes, and the bootstrap
  session carries no conversation record so the enumeration cannot see it even if one were retained.
  Both belong in the test header so a later change that breaks either shows up as a broken argument
  rather than as a flake.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05

## Revisions

### 2026-09-05 — implementation

- **Open question resolved.** `rostersOnMinter` measured **1**, on both the green run and the mutant
  run. The mid-turn lane does not reproduce the spawn-time twins' unordered race (#1868/#2009): the
  cursor is stamped by the routing of the very turn that produces the roster line, so the live-lane
  delivery to the minting conn is deterministic here. It is still logged rather than asserted, for
  the stated reason — asserting it would redden this spec for a change in a lane it does not own —
  but the mutant run's `minter=1, observer=0` is what tells the two producers apart by data rather
  than by assumption, and that reading is now recorded in the field's own doc comment.
- **Mutant run, as designed.** `go build -overlay` over a copy of `cmd/pyry/relay.go` with
  `RetainedBackgroundTaskRosters` set to nil, the resulting binary handed to the suite via
  `PYRY_E2E_BIN`. The daemon binary is what must carry the mutation — the harness shells out to its
  own `go build`, so an overlay on the *test* process alone would have left the daemon unmutated and
  the run green, which is the trap worth naming for whoever repeats this. RED at 22.66s (the full
  observer deadline, exactly #1868's prediction); green at 6.1s. Nothing was committed mutated.
- **Line count over the plan's estimate.** ~1130 lines of total written work against the stated
  ~800: the spec's mandated `## Security review` section (~55 lines, which the ticket's `Estimate:`
  line did not budget) and the fake's own rider unit tests (~140 lines, where the estimate allowed
  ~65 for the whole rider). No boundary of the size table other than the already-declared call-site
  line was crossed, and the shape of the work did not change — the overage is fixture and prose, not
  a second deliverable.
- **One addition beyond the plan's assertion list.** The observer's row loop also checks that every
  row after the first carries the rider's synthetic id prefix and that no id repeats. The plan
  listed only row 0 verbatim, which a payload that duplicated one row eight times would have
  satisfied; attribution across rows is the claim the AC's "with its tasks … intact" actually needs,
  and it costs one map and one loop.
