# #2246 — report background-task progress on the wire, rate-bounded

Map claude's `system/task_progress` line onto a new `turnevent` variant and a new
phone-facing frame, gated by a rate bound keyed on the counter that was measured to
track the line count. A long-running background task shows something moving between
the opening frame `task_started` produces and the terminal one #2245 added.

## Files read

- `internal/streamsup/task_progress_capture_test.go` → `taskProgressPinnedKeys`,
  `taskProgressPinnedUsageKeys`, `taskProgressDocumentedNotObserved` — the committed
  evidence this mapping declares its decode target from, and the pin that records
  `summary` as documented-and-unobserved so this ticket does not declare it.
- `internal/e2e/realclaude/testdata/parent_tool_use_v2.1.259.json` → the two
  `system/task_progress` frames themselves. Both counters and the tool-call
  relationship the rate bound is argued from come from these bytes.
- `internal/streamsup/parser.go` → `emitThinkingProgress`, `minThinkingTokensPerEvent`
  — the whole-shape analogue: one repeating subtype, its own decode target, one emit
  function, one rate bound. Its subtracted-crossing-test rule and its `d <= 0` guard
  are the two things this ticket must not get wrong in a computed-delta form.
- `internal/streamsup/parser.go` → `deniedThisTurn`, `maxTurnDenials`,
  `maxTaskRosterEntries` — the family's precedent for state keyed by ids claude
  chooses: lazily allocated, cardinality-capped, per-key length-capped before it is
  recorded, cleared at one reset.
- `internal/streamsup/parser.go` → `emitSystemSubtype`, `emitBackgroundTaskNotification`,
  `systemTaskNotificationLine`, `emitBackgroundTaskStarted`, `ignoredLineTypes` — the
  arm this ticket adds, the sibling to mirror, and the drop rule the new arm inherits.
- `internal/turnevent/event.go` → `BackgroundTaskUpdated`, `ThinkingProgress` — the
  "deliberately NOT fields here" paragraph that decides the omitted key set, and the
  RATE paragraph a repeating variant owes a consumer.
- `internal/protocol/interactive.go` → `BackgroundTaskUpdatedPayload`,
  `ThinkingProgressPayload` — the SECURITY paragraphs that govern any frame carrying
  these fields, and the shape a conversation-scoped payload takes.
- `internal/protocol/codes.go` → the background-task type block and
  `TypeThinkingProgress` — the two grouping precedents, and the drift-detector notes
  that say a new outbound-only constant must be classified from the moment it exists.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `BackgroundTaskUpdated` and
  `ThinkingProgress` arms — the pure-adapter posture and the no-second-filter rule.
- `cmd/pyry/interactive_turn_v2.go` → the grouped background-task arm of
  `interactiveTurnEmitterV2.Handle`, and `eventKind`. The ticket's five-file estimate
  misses this file; without an arm here the frame stops at the daemon boundary, which
  is the exact defect #1386 had to open a second ticket to close.
- `cmd/pyry/relay_guard_test.go` → the Type-constant routing map, which reports an
  unclassified constant rather than an unemitted one.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `TestDropcapClassification`,
  which carries a per-subtype mapped-or-dropped row.

## Context

`internal/streamsup` maps four background-task subtypes today. `task_progress` has no
arm in `emitSystemSubtype`, so it falls through to that switch's silent drop and a
client sees a task open and close with nothing in between.

Two committed frames, one task, sonnet, claude 2.1.259. `tool_uses` reads 1 then 2,
`total_tokens` 16207 then 16246, `duration_ms` 3839 then 4546, and `description` is the
current activity ("Reading alpha.txt", then "Reading beta.txt") rather than the task's
opening description. The subagent made two `Read` calls and claude sent two lines, so on
this sample the line count tracks the subagent's own tool-call count — which is what
makes a naive one-frame-per-line mapping unacceptable and a rate bound the design's
centre of gravity.

This design warrants no ADR: it applies the family's existing doctrine rather than
setting new doctrine. The one genuinely new thing — a rate bound whose delta is
computed rather than given — is argued at the constant and at the field, which is where
this package keeps that class of argument.

## Design

### A new event and a new frame, not a widening

`background_task_updated` is not widened, and the captured field set is what decides it.
`description` on a progress line is the CURRENT ACTIVITY, where `description` everywhere
else in this family is the task's OPENING description — a field this family has already
seen carry a literal operator command line. Putting both meanings under one name on one
task row is a wire-contract trap that no later ticket can undo. And `subagent_type` and
`last_tool_name` describe the agent doing the work, not what happened to the task, which
is what `BackgroundTaskUpdated`'s doc says that frame reports.

Nothing synthesizes a `Patch`, for the reason `emitBackgroundTaskNotification`'s doc
gives: both `Patch` doc comments promise a consumer that the field holds claude's own
bytes alone, and a manufactured patch would make them false.

- `turnevent.BackgroundTaskProgress` — the internal variant.
- `protocol.TypeBackgroundTaskProgress = "background_task_progress"` — joins the
  existing background-task constant block rather than getting one of its own. That
  block's stated criterion is the frame's SUBJECT (turn-independent work), which this
  frame shares; `TypeThinkingProgress` is grouped alone because its subject is neither
  the turn's lifecycle nor turn-independent work. The block's doc says "these three" in
  two places and enumerates three claude subtypes; both are amended, and the amendment
  states that the block admits a periodic READING beside three lifecycle frames because
  the criterion is subject rather than shape.
- `protocol.BackgroundTaskProgressPayload` — the wire form.
- `internal/turnbridge`'s `MapEvent` gains a pure arm; `cmd/pyry`'s emitter gains the
  variant on its existing grouped background-task case and an `eventKind` arm.

### Declared field set

| Event field | claude's key | Bound |
|---|---|---|
| `TaskID` | `task_id` | `maxTaskFieldID` |
| `Description` | `description` | `maxTaskDescription` |
| `SubagentType` | `subagent_type` | `maxTaskFieldID` |
| `LastToolName` | `last_tool_name` | `maxTaskFieldID` |
| `TotalTokens` | `usage.total_tokens` | none — an int cannot grow |
| `ToolUses` | `usage.tool_uses` | none |
| `DurationMS` | `usage.duration_ms` | none |
| `TruncatedFields` | — | daemon-authored |

Three of claude's ten keys are deliberately not fields, on the set
`turnevent.BackgroundTaskUpdated`'s doc already states: `session_id`, which is claude's
session identity and not the daemon's conversation identity; `uuid`, which nothing in
the daemon reads; and `tool_use_id`, whose join key is `task_id` and whose tool call the
opening frame already published. `type` and `subtype` are segmentation and belong to
`streamLine`.

`summary` is NOT declared. The pin records it as documented-and-unobserved, and that
absence is a fact about this capture's staging (a local agent without the
progress-summaries option) rather than about the subtype. Declaring it would be a field
taken from a docs page, which is the one thing this family's rule forbids.

`Description` reuses `maxTaskDescription` rather than earning a constant. It is the same
field name, the same model-authored class, and it is carried ONCE per event —
`maxTaskRosterDescription`'s multiplication argument is about a budget multiplied by a
count claude chooses WITHIN one event, which does not arise here. `subagent_type` and
`last_tool_name` are short tokens from sets claude owns, which is exactly the shape
`maxTaskFieldID`'s amendments already cover.

Envelope arithmetic, in `maxUnrecognizedRaw`'s style: worst case one event carries
256 + 4096 + 256 + 256 = 4864 bytes of claude-derived text, 7.4% of the v2
application-envelope cap of 65519 bytes. That is `BackgroundTaskStarted`'s number to the
byte, so the family keeps ONE scalar worst case a reader can hold. The three ints
contribute no term.

### The rate bound

`minTaskToolCallsPerEvent = 2`, keyed on `usage.tool_uses`.

**Why that counter and not the other two.** `tool_uses` is the only one of the three
whose absent baseline is genuinely zero: a task begins having made no tool calls, so a
first line's advance against nothing is a true reading. `total_tokens` reads 16207 on
the FIRST captured line — the subagent's whole context, already counted — so a first
delta against an absent baseline would be 16207, a number that says nothing about
progress and would emit on every task's first line whatever bound was chosen.
`duration_ms` is wall clock, so a bound on it would key emission on the subagent's speed
rather than on its activity, and a subagent blocked inside one long tool call would
report nothing while doing the most work. `tool_uses` is also the counter measured to
track the line count 1:1, which is what makes a bound on it a bound on the frame rate.

**Why 2, and the argument is thinner than `minThinkingTokensPerEvent`'s — stated, not
papered over.** That constant had four bursts of 126-197 to derive a ceiling and a
halving margin from. Here there is one task and two lines.

- CEILING, from the data, in exactly that constant's form: the observed task's whole
  tool-call advance is 2. Any bound above 2 emits nothing at all for the only task ever
  measured — the "a whole burst goes silent" failure, one order of magnitude thinner.
- FLOOR: 1 is not a bound. At 1 every line emits and the mapping is the 1:1 one the
  ticket exists to refuse.

So the observation pins the value exactly rather than leaving a range to take a margin
inside, and the honest statement is that this measurement is too thin to supply both a
ceiling and a safety margin. It is a real reduction: the captured turn's 2 lines become
1 frame, and a subagent making fifty tool calls produces twenty-five frames rather than
fifty. Not a power of two by coincidence — 2 is the only value the two bounds admit.

**Why no second cap downstream.** `MapEvent` and the emitter impose none, on the rule
`MapEvent`'s `ThinkingProgress` arm states: a second, differently-shaped filter would
silently diverge from the producer's. These frames are not droppable deltas (the
droppable set is `assistant_delta` only, #610), so they hold queue slots — which is why
the producer's bound is the one that has to be right.

### The keyed state

`Parser.taskProgressToolUses map[string]int` — per task, the `tool_uses` value carried
by the last line that PRODUCED an event. Not an accumulator: nothing is summed, so
AC 4 is structural rather than careful.

It follows `deniedThisTurn` in every dimension, which is the family's precedent for state
keyed by ids claude chooses:

- **Lazily allocated**, cleared by setting nil, so a session that never delegates pays no
  allocation. A nil map reads as empty and no call site distinguishes the two.
- **Cardinality-bounded** by `maxTaskProgressTasks = 8`. Derived the way
  `maxTaskRosterEntries` derives its own: one task observed, 8 is 8x the observation. It
  lands on the family's existing figure for concurrent background tasks, which is the
  right neighbour rather than a coincidence.
- **Per-key size-bounded** before the key is recorded: the key is the value the event
  PUBLISHED, after `maxTaskFieldID`'s cut — `emitPermissionDenied`'s rule, so an id too
  long to publish is also too long to remember. Retained state is at most
  8 * (256 + one int).
- **Released at consumeLine's one reset**, unconditionally, beside the four fields
  already there. Nothing deletes an entry when a task ends: a delete on the
  `task_notification` arm would couple two arms and buy a second boundary to keep
  correct, which is what having one boundary avoids. A residual across a dead child is
  bounded by the same reset and fails in the safe direction — one extra report for a
  task whose counter is already past the bound, never silence.

**Past the cardinality cap the line is consumed with no event.** The alternative — treat
an untracked task's previous value as zero and emit — restores the 1:1 rate for exactly
the tasks a hostile or runaway input arranges to be past the cap, which inverts the
bound. Silence for a ninth concurrent task is the cheaper failure here because progress
is a liveness decoration rather than a state-machine edge: it silences one task, not the
turn, and the task's own opening and terminal frames are untouched. `deniedThisTurn`
stops growing for the same reason and accepts its own analogous cost.

**An empty `task_id` is a bucket like any other**, not a drop. The family's rule is that
absence is claude's to choose and the field lands empty; keying on `""` keeps that rule
AND keeps the bound, where dropping would depart from the rule and treating unkeyed
lines as unbounded would bypass it. The cost is that two unkeyed tasks share one counter,
degrading a report a client could not attach to a row anyway.

### The decode reads the TOP-LEVEL bytes, never a nested field

`emitSystemSubtype`'s arm hands this function `line` — the whole line — and the decode
target is unmarshalled from those bytes and nowhere else. That is not a call-shape
convention: `streamLine`'s doc states the property it preserves, that control shapes are
read from the top level only and nested content is never re-scanned, which is what stops
a tool result whose text is literally `{"type":"result"}` from forging a turn boundary.
Decoding this payload from anywhere else would let claude's own tool output forge a
progress report — a liveness claim about a task, on a frame a remote client draws a row
from. Lower value to forge than a task's death or a whole roster, and refused on the same
rule rather than on a judgement about its value.

### What a consumer is told about these fields

`BackgroundTaskProgressPayload` carries a SECURITY paragraph in
`BackgroundTaskUpdatedPayload`'s form, because three of its four strings are model- and
tool-authored. `Description` is FREE TEXT with no documented bound; on this subtype it is
the current activity, and both captured values name a file the subagent is reading, so it
is path-BEARING in practice even though claude's documented path field is not a key here.
`SubagentType` and `LastToolName` are short tokens today under the same authorship. All
three are to be rendered as INERT TEXT: never executed, re-shelled, or fed to an HTML
sink, an attribute, or a URL. The bounds are the producer's, decided at construction, so
the payload re-decides no maximum.

### Error handling

The emit function's rule, stated in `emitThinkingProgress`'s form, with `seen` the
line's `usage.tool_uses` and `prev` the map's value (0 when untracked):

```
undecodable                        -> content-free Debug, consume, no event
seen <= 0                          -> consume, no event                  (guard)
untracked and map is full          -> consume, no event                  (cardinality)
seen <= prev                       -> store seen, consume, no event      (re-baseline)
seen - prev < bound                -> consume, no event                  (accumulate)
otherwise                          -> store seen, emit
```

Five reject branches, none of them logged with content. Only the undecodable branch logs
at all, and it logs the SUBTYPE — a message-name keyword, the same class as `sl.Type` in
the existing drop logs. The four silent branches log nothing, and the cardinality drop is
where that rule is most tempting to bend, because a drop with no diagnostic invites "just
the task id" or "just the count": `emitThinkingProgress` refuses the identical bend for a
token number and `emitBackgroundTaskRoster` for an entry count. The arm always reports
that it consumed the line, so no `task_progress` line ever reaches the unrecognized lane
— keeping `system` whole on `ignoredLineTypes` is what makes that structural.

**INVARIANT: every value stored in the map is > 0.** The `seen <= 0` guard runs before
any store, and the re-baseline branch stores a `seen` that already passed it. Two things
rest on this and neither survives it being relaxed.

**The crossing test is written SUBTRACTED, `seen - prev >= bound`, never
`prev + bound <= seen`.** Both operands are in `[0, MaxInt]` by the invariant, so the
difference is representable and no expression here can overflow whatever claude sends —
the failure is unrepresentable rather than guarded against. The additive form overflows
on a large `prev`, and the consequence is the one `emitThinkingProgress` documents in its
own terms: a comparison that reads false forever and a task silenced for the rest of the
turn from a single version-drifted line. Here the pressure is HIGHER, because `prev` is
the daemon's own retained number rather than one claude supplied fresh on each line.

**A counter that goes BACKWARDS re-baselines down rather than being ignored.** This is
the computed-delta analogue of `emitThinkingProgress`'s `d <= 0` guard and the design
question the ticket names. There, claude supplies a non-negative delta and a bad one is
refused. Here the delta is arithmetic against a value the daemon kept, so a claude that
restarts a per-task counter (as it already does for thinking tokens at every inference
request) would otherwise leave a stale high-water mark no realistic advance climbs out
of — silence for that task for the rest of the turn, which is the outcome this family
refuses. The re-baseline stores only a value already > 0 and only for a key already
present, so it can neither break the invariant nor grow the map, and an alternating
counter emits at most every other line.

`usage` absent or malformed: a non-numeric counter fails the whole decode and takes the
undecodable path; an absent `usage` object decodes to the zero struct, so `seen` is 0 and
the guard consumes it. Absence is claude's to choose and no validation rule is invented.

## Concurrency model

No goroutines are added. The new map is `Parser` state under the existing single-writer
invariant: `os/exec` drives `Write` from exactly one goroutine and nothing else reads
parser state, so the read-modify-write needs no mutex. It is the FIFTH field that
invariant covers, and the second holding a collection of claude's bytes; a future
concurrent reader guards all five, not four. Nothing formats a `*Parser` and no debug
bundle reaches parser state, so the retained ids are not reachable by any dump.

## Testing strategy

Unit, table-driven, stdlib only.

- **`internal/streamsup`** — replay the committed capture's own two frames through
  `Parser.Write` and assert one `BackgroundTaskProgress` with the SECOND line's values,
  no `Unrecognized`, and that the first line emitted nothing. This is AC 1 and AC 2 on
  claude's real bytes rather than a hand-built payload.
- The rate rule's table: below-bound accumulates; a first line at or above the bound
  emits at once; a backwards counter re-baselines and the NEXT advance still emits (the
  discriminating assertion is the second event, following
  `TestParser_ThinkingAccumulatorSurvivesAnExtremeDelta`'s shape); an extreme
  `tool_uses` cannot wedge the task, again asserted on the SECOND event.
- Two tasks interleaved: each keeps its own counter, so one task's lines cannot advance
  another's bound.
- Cardinality: a turn carrying more than `maxTaskProgressTasks` ids tracks 8 and consumes
  the rest content-free; a `result` line releases the state and the next turn tracks
  again. AC 3.
- AC 4: the emitted values equal the producing line's, asserted against a fixture whose
  counters differ per line so an accumulated total would read differently — a
  same-value fixture would pass vacuously.
- Caps: an over-long `description`, `task_id`, `subagent_type` and `last_tool_name` are
  cut and named in `TruncatedFields`, in call order.
- Drops: undecodable line, `tool_uses` absent, `usage` absent — each consumed, no event,
  nothing content-bearing logged.
- **`internal/protocol`** — envelope round-trip through a golden
  `testdata/background_task_progress.json`, plus the name assertions
  `TypeThinkingProgress` carries (the wire name is the daemon's, not claude's subtype).
  `compat_test.go`'s `v2OnlyTypes` and rejected-by-v1 rows gain the constant.
- **`internal/turnbridge`** — the arm maps every field verbatim and injects only
  `ConversationID`; `TruncatedFields` rides across.
- **`cmd/pyry`** — the frame reaches an interactive v2 client, drives no turn lifecycle,
  and reaches no non-interactive connection. `relay_guard_test.go` classifies the
  constant as outbound push.
- **`internal/e2e/realclaude`** — a `TestDropcapClassification` row recording the subtype
  as MAPPED, in the shape #2245 added. The package is behind the `e2e_realclaude` tag,
  which `make check` never compiles, so `go vet -tags e2e_realclaude ./internal/e2e/realclaude/`
  is run explicitly before finishing.

## Size — over the line ceiling, stated rather than split

Two of the six boundaries are exceeded, and one of them by more than the ticket's own
estimate said.

| Boundary | Limit | This ticket |
|---|---|---|
| Production source files | 5 | **6** |
| Total written work | 800 | **~1300** |
| New exported types | 5 | 2 |
| Consumer call sites | 10 | 0 — additive |
| Acceptance criteria | 5 | 4 |
| Reject branches | 10 | 5 |

The file count is 6 rather than the estimate's 5: `cmd/pyry/interactive_turn_v2.go`
needs the variant on its grouped background-task case and an `eventKind` arm, which the
estimate missed. Re-derived from #1386, the nearest new-frame analogue, whose second
commit touched that file for exactly this reason.

**Not split, because every available split trips the floor rule.** Two cuts exist and
both produce a child whose sole consumer is its sibling:

- Parser-and-event | wire. The first child ships an event `MapEvent` returns `ok == false`
  for and the emitter logs as `kind="unknown"` — dead on arrival, verifiable by nobody.
  That is not a hypothetical: it is the state #1385 left behind, and #1386's commit
  message describes it as a defect it had to open a second ticket to fix.
- Wire vocabulary | everything else, the shape #1386 itself took. The first child is the
  constant plus the payload plus a round-trip test — 128 lines when #1386 did it — whose
  only consumer is the second child, which stays over 800 lines anyway. It buys nothing.

Split depth is not the reason: the chain reads parent #2193, no grandparent, so a split
is structurally available and is declined on the merits. When the floor and the ceiling
disagree the floor wins, and the ceiling's failure costs a continuation leg where the
floor's costs a ticket nobody can verify. #2245 is the evidence the whole shape is one
run's work: it landed both halves at 1101 lines on 2026-09-09.

**File-overlap check: clear.** `origin/feature/449` touches
`internal/protocol/codes.go`, but issue #449 closed on 2026-05-17, the branch's tip is
from that date, and it has no pull request. It is a stale leftover that will never merge,
not in-flight work, so no blocker is set.

## Open questions

- Whether `2` survives a second capture. It is the tightest value the one observed task
  admits, and a staging with a subagent making many more tool calls could support a
  larger one. Resolve on a NEW measurement, never by raising it to feel safer — the
  ceiling argument is what the value rests on.
- Whether `summary` ever arrives. It needs an MCP task or the progress-summaries option,
  a staging nobody has run. That is a new capture and a new ticket, not a field added
  here.

## Security review

**Verdict:** PASS (one MUST FIX found and fixed inline before this verdict; the checklist
was re-walked from the top afterwards)

**Findings:**

- **[Trust boundaries] MUST FIX — FIXED IN THIS PLAN.** The first draft specified the
  emit function's decode without saying WHERE the bytes come from. Every sibling's doc
  states it because it is the family's load-bearing property: decoding from a nested
  field would let a tool result whose text is literally a `task_progress` line forge a
  progress report out of claude's own tool output — a liveness claim about a task, on a
  frame a remote client draws a row from. The plan now carries § "The decode reads the
  TOP-LEVEL bytes, never a nested field". With that stated the boundary is a single named
  function, `emitBackgroundTaskProgress`, and the trusted side holds a declared struct
  rather than raw bytes.
- **[Trust boundaries] No further findings.** The decode target declares seven of
  claude's ten keys; a field never declared cannot leak, which is why `session_id`,
  `uuid` and `tool_use_id` are absent from the STRUCT and not merely unset.
- **[Tokens, secrets, credentials] SHOULD FIX — state the path-bearing reading of
  `description`.** No token, credential or key is generated, stored, compared or logged
  anywhere in this design. What is credential-ADJACENT is `session_id`, claude's session
  identity, which is not declared. The residual worth naming is that `description` on
  this subtype names what the subagent is reading right now, so a stream of these frames
  is a stream of filename fragments from the operator's host reaching a remote device.
  That is inside the existing model rather than new — `BackgroundTaskStartedPayload.Description`
  already carries a literal operator command line, which is strictly more, and the
  receiving device is the operator's own, authenticated and Noise-encrypted — but a
  client must not read this field as a safe label. Carried into the payload's SECURITY
  paragraph in Phase B.
- **[File operations] Not applicable, by design rather than by luck.** No path is
  constructed, canonicalised, opened, created or written. claude's documented path field
  `output_file` is not a key on this subtype and is declared nowhere in the daemon, so
  there is no TOCTOU window, no traversal surface, no file mode to choose and no symlink
  decision to make.
- **[Subprocess / external command execution] Not applicable.** Nothing here reaches
  `exec.Command`. The parser reads a subprocess's stdout and spawns nothing; no decoded
  value becomes an argument, an environment variable or a signal target. The
  render-never-re-shell rule on `Description` is the client-side half of the same
  concern and is stated on the payload.
- **[Cryptographic primitives] Not applicable.** No randomness is drawn, no primitive is
  selected, no key or nonce exists, and nothing attacker-controlled is compared against a
  secret, so no constant-time comparison is owed. The new map is read by point lookup
  only and never iterated, so Go's randomised map order reaches no output.
- **[Network & I/O] SHOULD FIX — state at the constant that the bound is on the COUNTER,
  not on the line count.** Memory is bounded in both dimensions: 8 keys, each at most
  `maxTaskFieldID` bytes plus one int, released at one reset. Envelope is bounded at 4864
  bytes worst case, 7.4%, matching the family's existing scalar number. The whole line is
  capped at `defaultMaxParseBuf` before the decoder sees it. The residual is the frame
  RATE: the bound fires on `seen - prev >= 2`, so a `tool_uses` advancing by two or more
  per line emits on every line and the halving does not hold. `minThinkingTokensPerEvent`
  has the identical property — a delta of 64 on every line emits on every line — and no
  sibling in this family has any rate bound at all, so this ticket strictly improves the
  family's posture rather than regressing it. The frames are not droppable deltas, so a
  burst holds queue slots; that is the cost `maxRateLimitField`'s doc already names as
  accepted across the three background-task variants, bounded by the same existing
  backpressure. Named rather than mechanised, per evidence-based fix selection: revisit on
  an OBSERVED rate, as #1385 did. The property is recorded at the constant in Phase B so
  it is a stated limit rather than a surprise.
- **[Error messages, logs, telemetry] No findings.** One branch logs, and it logs the
  subtype keyword only. The four silent branches log nothing, and the plan now names the
  cardinality drop as the place that rule is most tempting to bend. No counter, no task
  id and no description reaches a log or an error string. The retained ids live only in
  parser state, which nothing formats and no debug bundle reaches.
- **[Concurrency] No findings.** No goroutine is spawned, so none can leak. No lock is
  taken, so there is no ordering to keep consistent. The map's lookup-then-store is a
  read-modify-write on shared state, which is safe only because of `Parser`'s
  single-writer invariant — `os/exec` drives `Write` from one goroutine and nothing else
  reads parser state, including across a child respawn, where `cmd.Wait` returning after
  the previous forwarder goroutine finished is the happens-before edge. The plan states
  that this is the FIFTH field that invariant covers and that a future concurrent reader
  guards all five. Mid-write shutdown leaves nothing partial: no disk state is touched
  and the map is process-local.
- **[Threat model alignment] No findings; one item named out of scope.** The frame is
  outbound binary → phone, capability-gated on `interactive`, and rides the existing
  Noise-encrypted v2 path, so the relay sees ciphertext and an old phone never receives
  it — the drift detector in `compat_test.go` is what enforces the last part, and the
  constant is added to `v2OnlyTypes` for exactly that. The only daemon-authored content
  is `ConversationID`. OUT OF SCOPE: honouring the render-as-inert-text obligation in a
  client is the desktop counterpart's, pyrycode-desktop#1246 on board #7; this ticket
  discharges its half by stating the obligation on the payload.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10

## Revisions

### 2026-09-10 — the arm gates, so its dropcap row is a PAIR rather than a mapped row

**What changed.** The Testing strategy said `TestDropcapClassification` gains "a row
recording the subtype as MAPPED, in the shape #2245 added". It gains two rows, and the
bare one records a DROP.

**Why.** Implementing the guard made a divergence explicit that the plan's rule table
implied without naming. `emitBackgroundTaskNotification` GATES ON NOTHING — a bare
`{"type":"system","subtype":"task_notification"}` still maps, because absence of a field
is claude's to choose — and #2245's dropcap row records exactly that. This arm cannot
take that posture: the counter is what its rate bound reads, and a line carrying none
cannot be placed against a previous value, so a bare line is consumed silently. That is
`system/init`'s and `system/status`'s shape rather than `task_notification`'s: both are
mapped subtypes whose bare fixture still classifies as a drop because of the arm's own
gate, and both rows say so.

**The contract.** The bare row asserts the drop and names the guard; a second row
carrying `usage.tool_uses` at the bound asserts the map. The pair is what makes it a pin
on the GUARD rather than on the subtype — and the control row is what stops the first
passing vacuously against a parser with no arm for the subtype at all, which is the state
this ticket changed.

### 2026-09-10 — independent fixture literals for both new constants

**What changed.** The Testing strategy did not say where the tests take the bound and
the cardinality cap from. They take them from `taskToolCallsPerEventFixture` and
`taskProgressTasksCapFixture`, two new literals beside the package's existing six.

**Why.** A test written against `minTaskToolCallsPerEvent` follows the constant green in
either direction, which is `taskStartedCapCheat`'s stated rule and the precedent
`taskRosterEntriesCapFixture` set for a cardinality. It matters more for the rate bound
than for any cap in that block: the value rests on an argument — a ceiling of 2 from the
observed task's whole advance, a floor of 2 because 1 is not a bound — that only holds
while the number does, so a self-referential table would let the number move and spend
the argument silently.

**Verified rather than reasoned about.** Both constants were mutated over a scratch
overlay (`minTaskToolCallsPerEvent` 2 → 3, `maxTaskProgressTasks` 8 → 4, one run, no
worktree writes). Every rate, cardinality, capture-replay and cap test in the set turns
red.

### 2026-09-10 — documentation counts

Four live sentences reading "seventeen turn-stream events" now read eighteen, this being
a new `turnevent` variant that reaches the wire. § `unrecognized_message`'s "five
`system` subtypes" is deliberately NOT re-counted: it was already stale before this
ticket — five arms have landed since it was written — so a sixth number would swap one
wrong count for another. A pointer to `emitSystemSubtype`'s case arms is added there
instead, and the changelog says the repair is documentation work of its own.

## Open questions — resolved

- **Whether `2` survives a second capture.** Unresolved by design and left as a standing
  condition rather than a question this ticket could answer: no second capture exists.
  What changed is that the argument is now enforced rather than merely written down —
  `taskToolCallsPerEventFixture` reddens a whole test set if the number moves, so raising
  it is a deliberate act with the ceiling argument in front of the person doing it. The
  constant's doc states the only admissible reason: a NEW capture whose task advances
  further.
- **Whether `summary` ever arrives.** Unchanged, and now enforced from two sides. It is
  absent from `systemTaskProgressLine` and from `BackgroundTaskProgressPayload`, and both
  omissions are asserted structurally over the json tags rather than by sweeping a value
  — the only form of assertion available for a key no captured line carries.
