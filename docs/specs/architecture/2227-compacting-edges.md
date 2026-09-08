# #2227 — map claude's compaction lines to the compacting on/off edges

## Files read

- `internal/streamsup/parser.go` → `Parser` (the doc comment's cross-line-state argument),
  `Parser.consumeLine` (`result` arm = the ONE reset point; the `default`/`ignoredLineTypes` branch
  and its drop-site comment), `emitSystemSubtype` (the one enumeration of the mapped set),
  `emitThinkingProgress` (the shape of a subtype arm: own decode target, `bool` return, content-free
  Debug on an undecodable line), `emitRateLimit` (the "nothing from the payload is logged" doctrine
  and the three-rung gate), `resultStopLine` (why a second decode target rather than widening
  `streamLine`), `systemThinkingTokensLine` (plain scalar over pointer when absent/null/zero are one
  reading), `truncateField` / `maxRateLimitField` / `maxModelField` (the per-field 256-byte cap and
  its house style), `emitUnrecognized` (the "never the content itself" standing rule this ticket
  takes one bounded exception to).
- `internal/turnevent/event.go` → `Compacting` — the `Active` field this maps onto, and the
  "PTY-derived" claim the doc still makes; `ApiRetry` — the rising/falling-edge sibling whose
  phrasing `Compacting`'s corrected doc mirrors.
- `internal/e2e/realclaude/compaction_capture_test.go` → `ccapPrimePrompt`, `ccapCompactPrompt`,
  `ccapArgs`, `ccapModel`, `ccapSessionID`, `ccapPrimeTurns`, `ccapPrimeSpan`, `ccapPrimeBudget`,
  `ccapQuiet`, `ccapCompactBudget`, `ccapRunExitWait` — #2229's staging rig, reused whole so this
  ticket's live proof drives the same three-turn shape that already fired once; `ccapRecord.stagingVerdict`
  — the "which reading is a zero-compaction run" separation this test copies in prose.
- `internal/e2e/realclaude/resilience_test.go` → `resolveClaudeBin`; `fixtures.go` →
  `WithWorktreeAuthenticated` — the credential skip every live test in the package opens with.
- `internal/streamsup/compaction_capture_test.go` → `compactionReaderGate`, `compactionCapturePath`,
  `compactionCaptureVersion`, `compactionPinnedShapes` — the four-quadrant fixture gate whose
  "absent fixture, empty pin" skip this ticket's replay test rides on, unchanged.
- `internal/e2e/realclaude/interactive_stream_unrecognized_test.go` (file docblock) and
  `dropped_line_capture_test.go` (`dropcapLimitations`, the `dropcapClassify` row table) — the two
  prose enumerations of the mapped/dropped split that carry a CORRECTED entry per mapping ticket.
- `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` — the
  package overview's record of #2229's live finding: compaction arrives as `system/status` +
  `system/compact_boundary`, both falling through `emitSystemSubtype`'s `default` today.
- `docs/knowledge/features/e2e-realclaude-compaction-capture-test-go.md` — **the lesson that changes
  how this ticket is built.** #2229's fixture fired on the dispatcher's gate-only run and was never
  committed; the document's own instruction is that #2227 "should check the tree before assuming it
  exists". Checked: `internal/e2e/realclaude/testdata/compaction_v2.1.259.json` is absent, and
  `compactionPinnedShapes` is still `[]string{}`.

## Context

`protocol.CompactingPayload` / `protocol.TypeCompacting` (#1074), `turnbridge.MapEvent`'s
`turnevent.Compacting` arm, `cmd/pyry`'s `interactiveTurnEmitterV2` fan-out and the desktop's
status-row label are all shipped. Nothing constructs the event: the only producer was the terminal
path #1348 deleted. This ticket supplies the producer, which is the whole of what lights the shipped
banner — no client change, no wire change, no new protocol type.

#2229 settled the seam by live capture against claude 2.1.259: `system/status` carries
`status:"compacting"` while compaction runs and `status:null` plus `compact_result`/`compact_error`
when it ends; a separate `system/compact_boundary` carries `compact_metadata`. Both are `system`
subtypes, which is the reading that makes AC 2 structural rather than earned — a `system` subtype
`emitSystemSubtype` does not map is dropped silently through `ignoredLineTypes`, never surfaced as
`unrecognized_message`. Had the lines arrived as a top-level type of their own they would reach
`emitUnrecognized`, and AC 2 would have been the harder half of this ticket. It is not.

No ADR is warranted: this adds a sixth case arm to an enumeration whose design ADR already exists,
and the one genuinely new decision (a cross-line field whose reset must be OBSERVABLE) is argued at
the field and in the `result` arm, where the two existing fields' arguments already live.

### What the lost fixture changes, and what it does not

AC 3 names "#2229's committed fixture bytes". **Those bytes do not exist in the tree.** The capture
ran, fired, and reported `shapes=[system/compact_boundary system/status]`, but the run was the
dispatcher's own real-claude gate, which verifies from a detached worktree and never runs `git add`.

This does not block the mapping, because what a mapper needs from a capture is the SHAPE, and the
shape is on record in the package overview in field-level detail. It does change three things, all
recorded here rather than absorbed:

1. The hermetic proof (AC 3's substance) is written against hand-authored lines in the observed
   shapes, in this package's established style — every mapping arm in this file is tested that way.
2. The fixture-backed replay is written anyway and **arms on the fixture's presence**, riding
   `compactionReaderGate`'s existing "absent fixture, empty pin → skip" quadrant unchanged. The
   assertion exists and reddens the moment an operator commits the bytes; nothing about it has to be
   remembered later.
3. AC 1 and AC 2 are proved by a **live assertion** instead of by replay — a new
   `internal/e2e/realclaude` test that drives #2229's own three-turn staging and asserts on the
   parser's event stream. This is strictly better evidence than a replay for those two criteria
   (they are claims about a live turn), and it needs no fixture, so it cannot be lost the way the
   capture was.

## Design

Two production files. No new exported type, no signature change, no consumer update.

### `internal/streamsup/parser.go`

**A sixth arm on `emitSystemSubtype`:** `case "status": return p.emitCompactingStatus(line)`.
`compact_boundary` is deliberately NOT given an arm — it carries `compact_metadata` (`trigger`,
`pre_tokens`, `post_tokens`, `duration_ms`), which is #2228's payload, and an arm here would emit a
duplicate edge with nothing to add. It continues to drop silently, and inherits `status`'s former
title as the one measured-and-dropped subtype left standing; the drop-site comment gets that
CORRECTED entry.

**`systemStatusLine`** — a third decode target beside `resultStopLine` and
`systemThinkingTokensLine`, for `resultStopLine`'s stated reason (failure isolation: two targets
fail independently and neither can disturb segmentation) and NOT by widening `streamLine`, which
`TestStreamLine_StaysSegmentationOnly` pins as segmentation-only. Three fields: `Status`,
`CompactResult`, `CompactError` — all plain `string`, per `systemThinkingTokensLine`'s
absent/null/zero-are-one-reading rule. `status:null`, an absent `status` and `status:""` are the same
reading here (not-compacting), so a `*string` would buy a distinction nothing acts on.

**`Parser.compacting bool`** — the third piece of cross-line state, and the first whose value is
MIRRORED ON A CLIENT.

**`emitCompactingStatus(line []byte) bool`** — a two-state machine, always consuming the line:

| edge open | `status == "compacting"` | action |
|---|---|---|
| no | yes | set open, emit `Compacting{Active: true}` |
| yes | yes | nothing (AC 4: no second rising edge) |
| yes | no | set closed, emit `Compacting{Active: false}` |
| no | no | nothing (AC 4: no falling edge with none open) |

An undecodable line returns `true` with a content-free Debug and leaves the edge untouched, per
`emitThinkingProgress`'s precedent; the `result` reset bounds the residual either way.

**The falling edge is deliberately WIDE** — any `status` value that is not `"compacting"` closes it,
not just `null`. The failure directions are asymmetric and that is the whole argument: a missed
falling edge leaves a banner asserting "claude is compacting" for the rest of the turn, which is a
false statement to the operator; a spurious one ends a banner early, which is a smaller and
self-correcting error. AC 5 is literally "the banner cannot stick", and `compact_result:"failed"`
closing it is one instance of the general rule rather than a second matcher.

**Logging.** On the falling edge only, one Debug carrying `compact_result` and `compact_error`, each
through `truncateField` at a new `maxCompactField = 256`. This is a **bounded, deliberate exception**
to `emitUnrecognized`'s "never the content itself" standing rule, and it gets a CORRECTED note there
so that claim does not go quietly false. The governing precedent is #2224, not `emitRateLimit`:
`decodeAssistantError` already bounds claude-authored error text at 256 and puts it ON THE WIRE, so a
256-bounded copy in a Debug the production daemon does not print is a strictly smaller exposure than
one the package already ships. The wire carries nothing but `Active`.

**The `result` arm gains a CONDITIONAL, OBSERVABLE reset**, and this is the one place the plan
departs from the two existing fields rather than following them. `thinkingSinceEmit`'s reset is
unobservable (an internal counter) and `assistantErrorCategory`'s is published by the very emit it
precedes. `compacting` is neither: the client holds a copy, so a reset that emits nothing
desynchronises the banner from the parser and the banner sticks until the client is reloaded. The
arm therefore reads: if the edge is open, close it and emit `Compacting{Active: false}` BEFORE the
`TurnEnd` emit. Still one reset point, still unconditional in the sense that matters (every `result`
subtype passes through it and no other line resets), but the reset is now visible on the wire.

**The residual** is stated rather than inherited by resemblance, as #2224's doc demands. A child that
dies mid-compaction with no `result` leaves `compacting == true` on a long-lived parser. Unlike
`assistantErrorCategory` there is no per-line write to launder it — but unlike that field the
residual is not a false claim about a later turn: the banner is already on at the client, the stale
`true` correctly suppresses a duplicate rising edge, and the next `result` line closes it. The cost
is a banner that outlives its compaction by at most one turn boundary. That is bounded by the same
single reset point the design already has, and it is the fail-safe direction relative to the
alternative (a second reset path on child exit, which buys a second boundary to keep correct).

### `internal/turnevent/event.go`

`Compacting`'s doc comment is corrected, not extended. "PTY-derived status peer of Stall" is false in
both directions now: #1348 deleted the producer that made it true, and this ticket makes stream-json
the producer. The `Active` semantics and the no-conversation-identity rule are unchanged; only the
provenance sentence and the "tui-driver streams no progress payload" parenthetical move, the latter
to a forward reference to #2228 as the ticket that would add fields.

## Concurrency model

None added. `emitCompactingStatus` and the `result` arm both run on the single os/exec forwarder
goroutine that `Parser`'s single-writer invariant already covers; `compacting` is read and written
only there, exactly as `thinkingSinceEmit` and `assistantErrorCategory` are. The field's doc says so
and names itself as the third field a future concurrent reader's guard has to cover.

The live test owns one `streamsup.Runner` goroutine, cancelled from `t.Cleanup` with a bounded wait
on its exit, and one mutex guarding the event sink (the forwarder goroutine appends; the test
goroutine polls). No `sync.Once`, and no unbounded wait inside a cleanup — #2089's suite-kill shape
is avoided by construction.

## Error handling

- Undecodable `system/status` line → consumed, content-free Debug, edge untouched. Never an
  `Unrecognized`: the family's standing answer is that surfacing a malformed line of a type we
  already claim is worth less than the guarantee that the subtype never reaches the unrecognized
  lane, and here that guarantee is what AC 2 rests on.
- `compact_result:"failed"` / a non-empty `compact_error` → the same falling edge as success, plus
  the bounded Debug. Compaction failing is not the parser's failure and produces no error path.
- A `result` line with the edge open → falling edge then `TurnEnd`, in that order.
- Child death with the edge open → residual bounded by the next `result`, argued above.

## Testing strategy

Hermetic, inside `make check`:

- `internal/streamsup/parser_compacting_test.go`, table-driven over line SEQUENCES with the expected
  event sequence per row: rising→falling; two `status:"compacting"` lines (AC 4, one edge);
  `status:null` with no edge open (AC 4, silence); `compact_result:"failed"` closing (AC 5);
  `result` closing an open edge, asserting the `Compacting{false}` precedes the `TurnEnd` (AC 5);
  `result` with no edge open emitting no `Compacting`; `system/compact_boundary` emitting nothing;
  an undecodable `status` line consumed silently with the edge intact; and a cross-turn row proving
  no residual survives a completed boundary.
- A zero-`Unrecognized` assertion over the whole sequence corpus (AC 2, hermetic half).
- `internal/streamsup/compaction_capture_test.go` gains a replay test armed on the fixture's
  presence, reusing `compactionReaderGate`'s existing quadrants: it decodes the committed frames,
  feeds each `payload` back through a real `Parser`, and asserts the same two edges (AC 3). Skips
  today, on the one legal skip that gate already defines.

Live, `make e2e-realclaude` (the ticket carries `needs-real-claude`):

- `TestRealClaude_CompactingEdges` drives #2229's staging — two rig-authored priming turns, then
  `/compact` — with a real `streamsup.Parser` as `Config.Stdout`, and asserts exactly one
  `Compacting{true}` then exactly one `Compacting{false}` (AC 1) and zero `turnevent.Unrecognized`
  across the compact turn (AC 2). It needs no fixture, so unlike #2229's capture it cannot be lost
  by running in a throwaway worktree. A zero-edge run fatals with the staging counts and says which
  of the two readings it is — the rig failed to stage a compactable conversation, or claude declined
  to compact — copying `stagingVerdict`'s separation so a red is diagnosable rather than merely red.

Prose enumerations (`interactive_stream_unrecognized_test.go`'s docblock, `dropped_line_capture_test.go`'s
`dropcapLimitations` and its classifier row table) get a CORRECTED entry naming `system/status` as
mapped, in #1404's and #1600's established shape.

## Open questions

1. **Whether `compact_boundary` ever arrives WITHOUT a closing `status` line.** Unmeasured — the
   capture recorded three compaction lines but the ordering was lost with the fixture. Handled by not
   depending on it: the `result` reset closes the edge regardless, so the worst case is a banner
   that runs to the end of the turn rather than one that sticks. If a future capture shows the
   `status:null` line is absent, the answer is an arm on `compact_boundary`, not a change here.
2. **Whether a second `system/status` subtype value exists** (`status:"thinking"`, say) that would
   trip the wide falling edge. Unmeasured, and the wide edge is chosen knowing it: a spurious close
   is the cheaper error, argued above.
3. **Whether `compact_result` is a closed keyword set.** Unmeasured; nothing branches on its value,
   so the question is confined to the Debug field and answered by the 256-byte cap.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The boundary is claude's subprocess stdout → `Parser.consumeLine` →
  `emitCompactingStatus` → (a) `turnevent.Compacting{Active bool}` on the wire, (b) a Debug log line.
  Arm (a) is the strongest half of this ticket's security posture and is worth stating rather than
  assuming: **the event carries no bytes claude authored.** `Active` is a Go bool the parser computes
  from a comparison; no `compact_result`, no `compact_error`, no `compact_metadata`, no token count
  reaches `protocol.CompactingPayload`. A hostile line can move a boolean and nothing else, which
  bounds the whole wire-facing threat to "the banner shows the wrong state" — the same class of harm
  a hostile `status` value could already do to any status surface, and not an injection of any kind.
- [Trust boundaries] SHOULD FIX — arm (b) is where the real decision is, and it must be recorded in
  the code rather than left to a reader. `emitUnrecognized`'s doc states a package-wide rule ("never
  the content itself, which is the package's standing rule") that this ticket takes an exception to.
  An unstated exception is the defect; the exception itself is defensible on #2224's precedent (a
  256-bounded claude-authored error string is ALREADY published on the wire by
  `decodeAssistantError`, so a 256-bounded copy in a Debug is strictly less exposure). Design now
  requires the CORRECTED note at `emitUnrecognized` and the argument at `emitCompactingStatus`.
- [Tokens] No findings — nothing on this path reads or compares a credential, and no value from the
  line is used as a key, an id, or a routing decision. The live test inherits
  `WithWorktreeAuthenticated`, so it skips rather than runs unauthenticated, and it records nothing
  to disk: unlike #2229's probe it writes no record and no fixture, so there is no artifact for a
  deny-scan to protect and no path by which a token could reach one.
- [Input validation & injection] No findings, and the reason is structural rather than diligent. The
  decode target is the TOP-LEVEL line bytes, never a nested field — `streamLine`'s stated property,
  which is what stops a tool result whose text is literally `{"subtype":"status","status":"compacting"}`
  from forging an edge. `systemStatusLine` declares three fields and nothing else; absence from a
  decode target is a stronger guarantee than a sweep, and it is what keeps `compact_metadata`'s
  contents structurally unreachable from this ticket's code.
- [Resource exhaustion] No findings. The state added is one bool, so no input can grow it and no
  sequence of lines can make the parser accumulate. The two logged fields are capped at 256 bytes
  each by `truncateField` before they reach the logger, on the falling edge only — at most once per
  edge, and an edge closes at most once per open. The line-level `maxBuf` bound is unchanged. A
  flood of `status:"compacting"` lines produces exactly one event by the AC-4 idempotency rule, which
  is a rate bound on the wire that falls out of the state machine rather than needing its own guard.
- [Error messages, logs, telemetry] The one Debug is the exception argued above; it names
  `compact_result` and `compact_error` and NOTHING else from the line — no session id, no token
  counts, no raw line. The undecodable path logs the subtype keyword only, matching
  `emitThinkingProgress` exactly. The live test's fatals carry counts and event-kind names, never a
  captured payload. No telemetry.
- [Concurrency] No findings — one new field on a struct the single-writer invariant already covers,
  written from the one forwarder goroutine and read from nowhere else. The invariant holds across a
  child respawn by the happens-before edge `Parser`'s doc already names (`cmd.Wait` returns only
  after the previous forwarder finished). The field's doc names itself as the third field a future
  concurrent reader must guard, so the guard cannot be added covering two of three.
- [File operations] No findings — this ticket opens, reads and writes no file. The replay test reads
  one compile-time-constant path under `testdata/`.
- [Subprocess] No findings in production code (none spawned). The live test reuses #2229's spawn
  shape verbatim (`--model haiku --dangerously-skip-permissions`) with rig-authored prompts in a
  fresh empty non-git workdir under a temp `$HOME`; keeping the shape identical is deliberate, since
  a differing one would be a confound against the capture this ticket maps from.
- [Cryptographic primitives] No findings — no crypto, no secret comparison, no randomness with a
  security role.
- [Network & I/O] No findings — no sockets, no server, no new I/O surface. The one wire effect is an
  additional `compacting` frame on an existing interactive lane whose fan-out (`interactiveTurnEmitterV2`)
  and mapping (`turnbridge.MapEvent`) are both shipped and unchanged by this ticket.
- [Threat model alignment] OUT OF SCOPE — every threat in `docs/protocol-mobile.md` § Security model.
  This ticket adds no protocol type, touches no relay, transport or Noise code, and changes no
  authentication or authorisation decision; it supplies a producer for a frame the threat model
  already covers.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08

## Revisions

### 2026-09-08 — sized above the 800-line row, built as one ticket

Estimated total written work is ~950 lines: ~150 in `parser.go`, ~20 in `event.go`, ~300 hermetic
tests, ~70 in the fixture-armed replay, ~180 in the live test, ~40 across the two prose enumerations,
and this plan. That trips the one-ticket boundary's 800-line row by roughly 20%. Every other row
holds with room: 2 production files (≤ 5), 0 new exported types (≤ 5), 0 consumer call sites (≤ 10),
5 acceptance criteria (≤ 5), 4 branches in the state machine (≤ 10).

The ticket is at split depth 1 (parent #2188, no grandparent), so a split is not forbidden — it is
**refused on the floor rule**, which wins when it and the ceiling disagree. The candidate seam is
"the mapping" / "the live proof", and the second slice's only consumer is the first: a live test
asserting `Compacting` edges has nothing to assert until the arm exists, and the arm alone ships a
producer no live evidence covers, which is precisely the state #1074 left behind and this ticket
exists to end. Cutting there produces two children neither of which can be verified alone. The
overage is stated and absorbed rather than paid for with a ticket that cannot stand up.

### 2026-09-08 — AC 2 was not structural, and the live gate proved it

The plan's Context argued that AC 2 followed from the seam: every compaction line arrives as a
`system` subtype, an unmapped `system` subtype is dropped by `ignoredLineTypes` rather than surfaced,
so zero `unrecognized_message` frames came free and "AC 2 is structural rather than earned". The
real-claude gate falsified that on the first lap. `TestRealClaude_CompactingEdges` passed AC 1 —
exactly one `compacting:true` then one `compacting:false`, in order, on a live `/compact` turn — and
failed AC 2 with two frames.

**What they were.** The census of the same lap (recovered from the capture probe's artifact
directory, since the fixture itself went out with the worktree again) puts the compact turn at seven
lines: `system/status: 2`, `system/compact_boundary: 1`, `system/init: 1`, `user: 2`,
`result/success: 1`. The two `user` lines are the frames. They are compaction's CONSEQUENCES rather
than compaction lines by subtype — claude's conversation summary, re-seeded as a message the person
never wrote, and the harness echoing the slash command's stdout as
`<local-command-stdout>Compacted </local-command-stdout>` — so a reading of the seam that enumerated
subtypes could not have found them. The plan's error was not the seam; it was treating a claim about
subtypes as a claim about a turn.

**Why they reached the wire, which is the part worth keeping.** Both carry `message.content` as a
JSON **string**, not the block array `streamMessage` declares. So `json.Unmarshal` fails for the
whole line and `consumeLine`'s undecodable branch fires BEFORE `emitUser` is reached — and the
summary line already carried `isSynthetic: true`, the flag `emitUser` has suppressed harness prose on
since #2087. The suppression that covered this line existed and was correct; a decode that never got
that far is the only reason it never ran. The frame that went out carried the line's own bytes as
`Raw`, so a compact turn put 3182 bytes of conversation summary into a client noise row.

**The fix**, `dropHarnessProseLine`: a second decode target on the decode-failure path only, matching
type `user` + string content + `isSynthetic || isReplay`, dropped with a content-free Debug. Two
flags rather than one because the two observed lines carry different ones, and neither subsumes the
other — the same OR'd-triggers shape `harnessNoOutputNudge` argues for at block level. This is the
parser's second suppression tier and its first at line level; the block-level tier is unchanged and
the two cannot overlap. A string-content `user` line carrying NEITHER flag still surfaces, which is
what keeps the arm from becoming a blanket over the alarm `emitUnrecognized` exists to raise.

**Scope.** This is production behaviour the plan did not prescribe, and it is in scope rather than a
§ Scope Discipline violation: AC 2 is this ticket's own criterion, the ticket's Context says in terms
that the zero-unrecognized criterion "is not a formality", and the lines being suppressed are the
compact turn's own output. It lands in `internal/streamsup/parser.go`, already one of the plan's two
production files, so the Files-read set is unchanged.

**One thing that is not a fix.** The live test's red said "2 unrecognized_message frame(s)" and its
trace rendered every `Unrecognized` as the same token, so the failure named a criterion and nothing
about which lines broke it; diagnosing it took a census from a different probe's leftovers. The trace
now carries `Site` and `Kind` — both bounded, neither the line — because a gate this session cannot
re-run at will has to spend its one lap saying something actionable.
