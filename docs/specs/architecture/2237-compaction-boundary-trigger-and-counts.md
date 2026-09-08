# #2237 — publish the compaction boundary's trigger and token counts

Ticket: <https://github.com/pyrycode/pyrycode/issues/2237>. Parent #2228, grandparent #2188.
Labels that change how this is built: `security-sensitive` (§ Security review below is mandatory
and was run before this file was committed) and `needs-real-claude` (AC 5 is a live claim; the
dispatcher's gate runs it, this session cannot).

## Sizing — over the boundary, built anyway

Re-counted against this written plan, not against the sketch: **7 production source files**
(`internal/turnevent/event.go`, `internal/streamsup/parser.go`, `internal/turnbridge/outbound.go`,
`internal/protocol/codes.go`, `internal/protocol/interactive.go`, `cmd/pyry/interactive_turn_v2.go`,
and a comment-only edit to `cmd/pyry/stream_turn_busy.go`) against a ceiling of 5, and roughly 1500
lines of total written work against a ceiling of 800. The other four rows hold: 1 new exported type
per layer (4 total, under 5), no consumer cascade at all (every change is additive — no signature
moves, no rename, so `codegraph_impact` has nothing to fan out over), 5 acceptance criteria, and no
state machine.

**Not split, and the reason is the split-depth gate rather than a judgement about the work.** The
parent chain reads `parent 2228 grandparent 2188`, so a further split is refused outright;
`needs-human:sizing` is already on the ticket, applied by the refiner, and the correct action at this
gate is to build. The refiner's own note reaches the same place from the floor rule: the only seam
inside this ticket is layer-shaped, and cutting there yields a child whose sole consumer is its
sibling — a `turnevent` variant no bridge maps, or a payload nothing emits — which the floor rule
merges back even at the cost of the ceiling.

## Files read

- `internal/streamsup/parser.go` → `emitSystemSubtype`, `emitCompactingStatus`, `emitThinkingProgress`,
  `systemStatusLine`, `consumeLine`'s `ignoredLineTypes` branch, `truncateField`, `boundStopField`,
  `maxCompactField`, `maxTurnEndStopField`, `jsonKey` — the six-arm dispatch this ticket adds a
  seventh to, the undecodable precedent it copies, the two bound shapes (cut vs drop) it chooses
  between, and the presence-only decode idiom.
- `internal/turnevent/event.go` → `Event`, `Compacting`, `ThinkingProgress`, `ConversationReset`,
  `RateLimited` — the sealed sum type, the sibling this frame is deliberately NOT an extension of,
  and the two variants whose field docs state the "claude's number, not clamped" and "no
  TruncatedFields where nothing can be cut" rules.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.Compacting`, `ThinkingProgress` and
  `RateLimited` arms — the field-by-field-not-passthrough construction, and the standing rule that
  this adapter re-caps nothing the producer already bounded.
- `internal/protocol/interactive.go` → `CompactingPayload`, `ThinkingProgressPayload`,
  `ToolResultPayload`, `UnrecognizedMessagePayload` — the file's no-omitempty rule and the exact
  wording of the SECURITY paragraph a claude-authored field owes.
- `internal/protocol/messaging.go` → `SessionTransitionPayload.WorkspaceCwd` — the closest neighbour
  in shape for this ticket's presence-versus-zero problem: a pointer with NO omitempty, emitting a
  literal `null`. It is why the counts do not take `SessionSettingsPayload`'s omitempty form.
- `internal/protocol/codes.go` → `TypeCompacting`, `TypeThinkingProgress` and their const blocks —
  the "the NAME is the daemon's, not claude's" rule and the grouped-alone convention.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition` — the drift
  detector that fails the build if a new `Type*` constant is not partitioned.
- `cmd/pyry/interactive_turn_v2.go` → `Handle`, `eventKind`, `emitMapped` — the consumer arms, and
  `ConversationReset`'s worked example of a deliberate missing `Handle` arm.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — its `default` already answers `turnMarkNone`, the
  right answer here, so only its comment moves.
- `internal/streamsup/parser_compacting_test.go` → `compactBoundaryLine` (invented counts, replaced
  here), `compactingTrace`, `compactingEvents`, the `compactSummaryUserLine` comment whose
  `compactionPinnedShapes` rationale #2236 falsified.
- `internal/streamsup/compaction_capture_test.go` → `compactionCapturePath`, `compactionPinnedShapes`,
  `compactionReaderGate`, `TestCompactionFixtureReplayReachesBothEdges` — the committed-capture
  reader AC 2's replay assertion joins.
- `internal/e2e/realclaude/compacting_edges_test.go` → `TestRealClaude_CompactingEdges`, `cedgeTrace`,
  `cedgeCollector` — the existing live lap AC 5 extends rather than adding one.
- `internal/e2e/realclaude/testdata/compaction_v2.1.259.json` → frame index 16 — the observed
  `compact_boundary` line, whole. Every literal in this ticket is transcribed from THIS file, not
  from the ticket body and not from a recovered `$TMPDIR` record: those are different laps and their
  numbers disagree.
- `docs/protocol-mobile.md` § Application message types, § `compacting`, § Changelog — the frame's
  two written records, and the 2026-07-27 entry recording a missing table row as a defect.
- `docs/knowledge/features/e2e-realclaude-compaction-capture-test-go.md` — records that #2236 landed
  the fixture and filled the pin, which is what makes the stale comment above correctable.

## Context

Claude announces a finished compaction on a `system/compact_boundary` line carrying a
`compact_metadata` object. `emitSystemSubtype` has no arm for it, so the trigger and the token counts
reach nothing: #2227 mapped `system/status` into the `turnevent.Compacting` edge pair and left this
subtype as the one measured-and-dropped member, naming this ticket its owner.

**The line arrives after the falling edge has already fired**, which decides the shape of the whole
ticket. The committed capture's compact turn runs: `status:"compacting"` → `status:null` +
`compact_result` (**the falling edge**) → `system/init` → `system/compact_boundary` → two `user`
lines → `result/success`. So the counts cannot extend `Compacting`'s payload — by the time claude
states them that frame has shipped. They need a frame of their own, conversation-scoped exactly as
`compacting` is, that a client applies to the divider it has already drawn.

That is also why the boundary arm is a pure per-line map that reads and writes **no parser state at
all**. AC 3 and AC 4 both rest on that one property.

No ADR is warranted. This is #1385's and #1386's layer pattern applied a fifth time; the design
decisions specific to it (the allowlist decode, the presence-preserving counts) belong in the field
docs where a reader meets them, and the wire contract belongs in `docs/protocol-mobile.md`.

## Design

Five layers, following #1385/#1386 verbatim in structure.

### `internal/turnevent` — the variant

`CompactionBoundary`, a new `Event` implementation. Three fields:

- `Trigger string` — claude's `compact_metadata.trigger`. An OPEN SET (`"manual"` observed;
  `"auto"` documented) carried verbatim, on `TurnEnd.Outcome`'s rule.
- `PreTokens *int`, `PostTokens *int` — claude's before/after counts. **Pointers, and this is the
  one place this ticket departs from `Compacting`'s field shape.** `Compacting.Result` is a plain
  string because absent, empty and daemon-reset are one reading; here they are not. A count claude
  omitted and a count of zero are different facts, and a client that collapses them renders
  "24k → 0 tokens" for a boundary claude reported without a post count. That is the failure AC 1
  names.

The variant carries no `TruncatedFields` and owes none: `Trigger` is DROPPED past its bound rather
than cut (below), and a dropped scalar is directly observable as the empty value, which is
`maxTurnEndStopField`'s stated reason for the same omission.

Deliberately absent, and each for a reason stated at the field: `session_id` and `uuid` (claude's
identities, never the daemon's — `BackgroundTaskStarted`'s rule), and every remaining
`compact_metadata` key. `cumulative_dropped_tokens` and `duration_ms` are excluded because the
ticket does not ask for them and an unused field is a claim nobody checks; `preserved_segment`,
`preserved_messages` and `logical_parent_uuid` are excluded because they name entries in the
operator's own transcript.

### `internal/streamsup` — the producer

`emitSystemSubtype` gains a seventh arm, `case "compact_boundary"`, calling a new
`emitCompactionBoundary`, which returns `true` on every path (consuming the line, so
`emitUnrecognized` stays structurally unreachable for it — the same property #2227's AC 2 rests on).

Decode target: a new `systemCompactBoundaryLine` whose `CompactMetadata` field is a **pointer** to a
new `compactMetadata` struct declaring **exactly three fields** and nothing else.

- The pointer is the presence discriminator: nil metadata means the line said nothing to publish.
- The three-field struct is the **allowlist**, and it is structural rather than a filter step:
  `encoding/json` discards every key the target does not declare, so no uuid can survive the decode
  regardless of what claude adds to the object later. This is what makes AC 2's "nothing else from
  `compact_metadata`" a property of the decode rather than of a scrubber somebody has to maintain.

Three consuming paths, in order:

1. **Undecodable line** → `Debug("streamsup: dropping undecodable system line", "subtype",
   "compact_boundary")`, no event. Copied from `emitCompactingStatus`'s undecodable arm on its stated
   ground: the subtype is a message-name keyword, not payload, and no claude-authored field was
   decoded on this path.
2. **Decodable, `compact_metadata` absent** → no event, nothing logged.
3. **Metadata present** → bound the trigger, emit one `CompactionBoundary`.

The trigger's bound is a new `maxCompactTrigger = 256` and it **DROPS rather than cuts**, which is
`boundStopField`'s shape and not `truncateField`'s. The reason is `maxTurnEndStopField`'s verbatim:
this is a token a client matches against known values, so a cut token is indistinguishable from a
token the client has never heard of — a state it must already handle, because the set is open.
Carrying the empty value says exactly that and invents nothing. `boundStopField` itself is not
reused: it hardcodes `maxTurnEndStopField`, whose doc names the three `turn_end` strings it caps, and
widening either would make that doc false for a saving of two lines.

The counts are **not clamped, not range-checked and not ordered**, on `RateLimited.ResetsAt`'s stated
rule: they are claude's numbers, not the daemon's. A value `encoding/json` cannot fit in an `int`
fails the whole line's decode and takes path 1 — fail-closed, and worth stating because it means one
absurd field costs the whole frame rather than half of it.

**Nothing is logged on the emitting path**, unlike `emitCompactingStatus`. That function's Debug is
an argued exception (#2227 needed a diagnostic for values that did not reach the wire, #2236 kept it
as a bounded copy of something already published). Here `emitThinkingProgress`'s posture applies
instead: everything decoded reaches the wire, so a log would be a second sink to keep in step with
the first for no diagnostic gain.

**No parser state is read or written**, and no arm of `consumeLine`'s turn-boundary reset touches
this variant. `p.compacting` in particular is neither read nor set, which is AC 3's second half and
AC 4 entire: a boundary line that follows no compacting edge produces exactly the same frame as one
that follows an edge.

### `internal/turnbridge` — the wire mapping

`MapEvent` gains a `case turnevent.CompactionBoundary` returning
`protocol.TypeCompactionBoundary` and a `CompactionBoundaryPayload` built field by field, in the
manner every arm around it is: conversation identity from the bridge, `tc.TurnID` and `tc.Seq`
ignored (not turn-scoped — a compaction boundary is a fact about the conversation's context, and the
turn that happened to contain it neither owns nor bounds it). Nothing is re-capped here; the producer
bounded the trigger at construction.

The two count pointers cross verbatim rather than being deep-copied. The producer allocates a fresh
`int` per line, retains neither, and nothing downstream mutates a payload, so the aliasing is
observable to no one — stated at the payload rather than defended with a copy.

### `internal/protocol` — the wire vocabulary

- `TypeCompactionBoundary = "compaction_boundary"`, in its own const block with the reasoning, and
  added to `v2OnlyTypes` in `compat_test.go` (`TestTypeConstants_V1V2Partition` fails the build
  otherwise). It is outbound binary → phone and MUST NOT reach `inboundAppTypeSet`.

  **The name is the daemon's.** claude's subtype is `compact_boundary`; the daemon's word for the
  process throughout this package and `internal/streamsup` is "compaction", so the frame is
  `compaction_boundary`. The discriminating word is "boundary" — what the frame marks — where
  `compacting` beside it names a state with two edges. The two are not merged and not renamed for
  the reason `codes.go` gives generally: a claude rename must land in one place.

- `CompactionBoundaryPayload` with `conversation_id`, `trigger`, `pre_tokens`, `post_tokens`.

  The counts are `*int` with **NO omitempty**, so an absent count marshals as a literal `null`. That
  is `SessionTransitionPayload.WorkspaceCwd`'s exact shape and it is chosen over
  `SessionSettingsPayload`'s pointer-plus-omitempty because this file's rule is that every key is
  always present: the testdata fixture pins the full shape, and `omitempty` would drop the key
  instead of stating the absence. `null` versus `0` is the distinction AC 1 asks to survive to the
  client, and it survives here as a wire value a client cannot mistake.

### `cmd/pyry` — the consumer

- `Handle` gains its own `case turnevent.CompactionBoundary`, body identical to the status peers
  (`flushDelta` then `emitMapped`) and kept a separate arm rather than merged, on that switch's own
  stated rule: it merges arms that share a REASON, and this one's reason is its own — a compaction
  boundary is a mark IN the conversation's history, not a sub-state of a live turn, and it may
  legitimately arrive with no turn open. Opening a turn on it would wedge the conversation exactly as
  opening one on an unrecognized message would.
- `eventKind` gains an arm returning `"compaction_boundary"`. The variant NAME only: `Trigger` is
  claude-authored and neither count is returned.
- `turnMarkFor` gains **no arm** — its `default` already answers `turnMarkNone`, which is correct
  here. Only the comment enumerating what lands in that default changes.

## Concurrency model

No goroutine is added, started or stopped. `emitCompactionBoundary` runs on `Parser.Write`'s caller
goroutine like every sibling emitter, holds no lock, and — the property that makes this the smallest
concurrency story in the family — touches no field of `Parser`. `Handle`'s arm runs on the same
single-threaded emitter path as its neighbours and mutates no turn-lifecycle state.

The one place two goroutines meet is in the live test, and it is existing machinery:
`cedgeCollector`'s mutex already guards appends from the forwarder goroutine against the test
goroutine's polls.

## Error handling

| Failure | Behaviour | Precedent |
|---|---|---|
| Line is not valid JSON, or a field's type/range does not fit the target | Consume, `Debug` naming the subtype keyword only, no event, no state touched | `emitCompactingStatus`'s undecodable arm |
| `compact_metadata` absent or `null` | Consume, no event, nothing logged | new; the pointer is the discriminator |
| `trigger` over `maxCompactTrigger` | Trigger dropped to `""`, counts still published | `boundStopField` / `maxTurnEndStopField` |
| A count absent | `null` on the wire, distinguishable from `0` | `SessionTransitionPayload.WorkspaceCwd` |
| A count negative or implausible | Carried verbatim, unclamped | `RateLimited.ResetsAt` |
| Boundary line with no compacting edge open | Frame produced identically; `p.compacting` untouched | AC 4; no precedent needed, the arm is stateless |

No path returns an error, no path panics, and no path can leave the compacting edge in a state it
was not already in.

## Testing strategy

Scenarios, by criterion. Hermetic rows sit beside #2227's and #2236's in
`internal/streamsup/parser_compacting_test.go` unless named otherwise.

- **AC 1** — a table over `emitCompactionBoundary`: the observed line publishes trigger + both
  counts; `post_tokens` absent publishes a nil pointer while `pre_tokens` still crosses; `post_tokens`
  explicitly `0` publishes a non-nil pointer to zero. The absent-versus-zero pair is the ticket's
  whole point and is two rows precisely so neither can pass without the other. At the wire, two
  `internal/protocol` round-trips over two fixtures — one with both counts, one with both `null`.
- **AC 2** — `internal/streamsup/compaction_capture_test.go` gains a replay test joining the existing
  `compactionReaderGate`: feed every frame of the committed capture through a real `Parser`, assert
  exactly one `CompactionBoundary` carrying `manual` / 23600 / 2612 (transcribed from the committed
  file), then marshal the event and assert **none of the capture's uuids appears in it**, with the
  uuids read out of the capture's own bytes rather than retyped. A key-set assertion on
  `CompactionBoundaryPayload` in `internal/turnbridge` closes the wire half.
- **AC 3** — rows for an undecodable boundary line and for one carrying no metadata: no event, no
  `Unrecognized`, and — asserted explicitly by feeding the line BETWEEN a rising and a falling edge —
  the edge pair still fires normally, which is what proves `p.compacting` was untouched.
- **AC 4** — a boundary line fed with no preceding edge at all, asserting the same frame is produced.
- **AC 5** — extends `TestRealClaude_CompactingEdges` rather than adding a lap: `cedgeTrace` gains an
  arm rendering the boundary's SHAPE (trigger presence, each count's presence), and the test asserts
  exactly one such frame on the compact turn with both counts present, alongside the existing
  zero-unrecognized assertion. Shape, never values — the numbers differ every lap. This runs only in
  the dispatcher's `make e2e-realclaude` gate; no agent session on this machine can sign claude in.
- **Regression** — `compactBoundaryLine`'s invented counts are replaced with the observed shape, so
  every existing row that feeds it now feeds real bytes; `compactingTrace` gains a boundary arm so
  the "compact_boundary is not an edge" row keeps saying something true; `TestTurnMarkFor_TotalOverEveryVariant`
  and `eventKind`'s coverage gain a row each.

## Open questions

1. **Does an `auto` compaction announce itself on this same line?** Unmeasured — the capture drove a
   manual `/compact`. AC 4 is what makes the answer not matter for publishing: the arm is stateless,
   so a boundary line is mapped whatever status lines did or did not precede it.
2. **Can `compact_boundary` arrive with no `compact_metadata` at all?** Unobserved. Path 2 above is
   the answer either way; if the live gate ever shows it happening, the silence is already correct.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No MUST FIX; one property is load-bearing and one statement is missing.**
  The boundary is claude's stdout → daemon state and it is explicit and singular:
  `emitCompactionBoundary` is the only function that reads this line, `systemCompactBoundaryLine` is
  the only decode target, and `compactMetadata` declares exactly three fields. Everything else on the
  line — `session_id`, `uuid`, `logical_parent_uuid`, `preserved_segment`, `preserved_messages`,
  `cumulative_dropped_tokens`, `duration_ms` — is discarded by `encoding/json` because the target does
  not declare it. That is what makes AC 2 structural rather than a scrubber somebody maintains, and
  it holds for keys claude has not shipped yet. Downstream is told the data is untrusted by field
  doc, not by type, which is this codebase's uniform answer (`Compacting.ErrorText`,
  `UnrecognizedMessagePayload.Raw`).

  **SHOULD FIX — the plan's Design does not state the class change this frame makes, and it is
  larger than `Compacting`'s.** `CompactingPayload.active` is the daemon's own bool, computed from a
  string comparison the daemon makes; #2236 noted that adding two claude strings beside it was a
  class change. Here **every field is claude's, including the fact of the boundary itself** — AC 4
  requires the frame be produced with no daemon-observed edge preceding it, so this frame can be the
  only evidence a compaction occurred. A fabricated line reading `pre_tokens: 999999, post_tokens: 1`
  draws a plausible compaction mark where nothing was compacted. It is a misleading label rather than
  an actuator (see the Concurrency/state finding below), but the payload doc must say so: render it
  as **claude's assertion**, attributed to claude, never as the daemon's own finding. Add that
  paragraph to `CompactionBoundaryPayload` and the matching one to
  `turnevent.CompactionBoundary` in Phase B; the verifier should check it landed.

- **[Tokens, secrets, credentials] Not applicable, by a design decision rather than by luck.** No
  token, key or credential is read, written, compared or derived on any path this ticket adds. The
  one identity-shaped thing on the source line is claude's `session_id`, and it is excluded from the
  decode target on `BackgroundTaskStarted`'s stated rule — claude's session identity is not the
  daemon's conversation identity. The daemon's own `conversation_id` is injected by
  `MapEvent` from the bridge's cursor and never read off claude's line.

- **[File operations] Not applicable in production; the one file read is test-only and constant.**
  `emitCompactionBoundary` opens nothing. The replay test reads `compactionCapturePath`, which is a
  package constant composed of two package constants with no caller-supplied component, so there is
  no path to traverse and no check-then-use gap. No file is created, so no mode question arises.

- **[Subprocess / external command execution] Not applicable.** Nothing here spawns, signals or
  argument-builds. The claude child is spawned by `streamsup.New` from configuration this ticket does
  not touch, and no decoded value reaches an `exec.Command` — `Trigger` is compared against nothing
  in the daemon and passed to nothing.

- **[Cryptographic primitives] Not applicable.** No randomness, no hashing, no comparison against a
  secret, so no `crypto/subtle` question. `Trigger`'s bound is a length test, not a comparison, and
  is not attacker-timing-relevant: nothing secret is on either side of it.

- **[Network & I/O] No findings, and the frame-size question is answered rather than assumed.** The
  payload is a conversation id, a token bounded at 256 bytes, and two integers — order 300 bytes
  against the v2 application-envelope cap of 65519. It cannot approach it, which is the opposite of
  `SlashCommandList`, where #2002 had to bound the mapped frame. Input size upstream is already
  capped: `defaultMaxParseBuf` bounds the whole line at 4 MiB before the decoder sees it. **Rate:**
  one frame per `compact_boundary` line, no accumulator, no dedup, and none owed. A stream emitting
  the line in a loop produces small frames that `turnMarkFor`'s `default` classifies `turnMarkNone`,
  which makes every one of them droppable at the sink under saturation — the same posture
  `Unrecognized` carries, and the reason a second, differently-shaped filter in `Handle` would be a
  hazard rather than a defence.

- **[Error messages, logs, telemetry] No MUST FIX; the emitting path logs nothing at all, which is
  the tightest available posture.** The undecodable path logs the subtype keyword only — a
  daemon-authored literal, never a byte of the line — copied from `emitCompactingStatus`. No count and
  no trigger reaches any log at any level, and `eventKind` returns the variant name only, so the
  `#833` posture holds. **The one real exposure this ticket could have introduced is in the live
  test**: `cedgeTrace`'s token lands in CI output on failure, so rendering `Trigger`'s VALUE there
  would route claude-authored text into a build log. The plan already forecloses it by asserting on
  shape (presence of the trigger, presence of each count) rather than on values; that is now a
  security requirement and not only an AC-5 convenience, because the values differ every lap AND
  because a hostile trigger would otherwise be printed. Phase B must keep the trace token free of
  `Trigger`'s bytes.

- **[Concurrency] No findings, and the strongest property in the design is here.**
  `emitCompactionBoundary` reads and writes **no field of `Parser`** — not `compacting`, not the
  thinking accumulator, nothing. So there is no lock to order, no check-then-mutate window, and no
  cross-turn residual for `consumeLine`'s `result` reset to bound. Shutdown mid-line loses the line
  and nothing else: no state is persisted and none is half-written. No goroutine is spawned, so no
  lifecycle or leak question arises. `Handle`'s arm runs on the existing single-threaded emitter path
  and mutates no turn-lifecycle field. **Nothing in the daemon is keyed on any value this frame
  carries** — no retry, no backoff, no teardown, no routing — which is what keeps a fabricated line a
  misleading label rather than an actuator, and whoever first changes that owes the review that turns
  claude-authored data into one.

- **[Threat model alignment] `docs/protocol-mobile.md` § Security model threat 1 lands in the
  OUTWARD direction, exactly as it does for `compacting`.** claude-authored data flows out to a
  client; the daemon bounds and does not sanitize; the render boundary owing control-character and
  terminal-escape stripping is the client's. `trigger` takes `turn_end`'s `outcome` rule rather than
  `compact_error`'s: it is a short token from an open set, so a client switches on it and treats an
  unrecognised value as unknown — it is NOT prose and must not be rendered with `unrecognized_message`'s
  `raw` latitude. That distinction goes in the protocol doc. Capability gating is not re-answered
  here and must not be: the interactive grant is filtered once in `emit()` for every frame type, and
  a second gate in `Handle`'s arm is how that single gate stops being single.

- **[Out of scope]** `cumulative_dropped_tokens` and `duration_ms` are observed on the line and
  deliberately not published; no ticket owns them and none should until a consumer asks. The
  desktop's rendering of the trigger and counts is pyrycode-desktop#1240's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08

## Revisions

*(none yet — Phase B appends here if the design moves)*
