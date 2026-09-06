# #2134 — `streamsup`: map claude's `conversation_reset` announcement onto a turn event

Decode half only. The daemon acting on the event — re-keying the session
registry, drawing the delimiter, following the transcript — is #2135 and
consumes what lands here.

## Files read

- `internal/streamsup/parser.go` → `consumeLine` — the switch this arm joins, and
  the `default` branch whose `emitUnrecognized` call is the noise row being
  removed.
- `internal/streamsup/parser.go` → `ignoredLineTypes` — the list this type does
  NOT join, and the doc that states why (top-level types only, MEASURED
  2026-07-27; that census never ran `/clear`).
- `internal/streamsup/parser.go` → `consumeToolProgress`'s arm in `consumeLine` —
  the exact arm SHAPE this one copies: a matcher that returns on consume and
  otherwise falls through to `emitUnrecognized`, written out rather than reached
  by `fallthrough`.
- `internal/streamsup/parser.go` → `emitModelAnnounced`, `systemInitLine` — the
  closest payload shape: one field, decoded from the TOP-LEVEL line bytes, with a
  documented argument for every key omitted from the decode target.
- `internal/streamsup/parser.go` → `emitRateLimit`, `rateLimitEventLine` — the
  precedent for the "absent / present-but-empty / no such key are answered
  identically" formulation, and for a per-rung drop-reason vocabulary this arm
  deliberately does not need.
- `internal/streamsup/parser.go` → `streamLine` — states the property the decode
  input preserves: control shapes are read from the top level only, so a tool
  result whose text is literally `{"type":"conversation_reset"}` cannot forge one.
- `internal/streamsup/parser.go` → `emitUnrecognized`, `truncateRaw` — where AC2's
  declines land, and what bounds the bytes they carry.
- `internal/streamsup/runner.go` → `useCreateForm` — the existing
  `internal/transcript` consumer in this package; confirms the import is already
  present and that `internal/transcript` imports no internal package (no cycle).
- `internal/transcript/transcript.go` → `ValidStem`, `uuidStemPattern` — the
  predicate that gates the id, and the alphabet/length it fixes.
- `internal/turnevent/event.go` → `ModelAnnounced` and the `isTurnEvent` marker
  block — the variant template, and the sealing mechanism the totality guard
  reads.
- `internal/turnevent/boundary_test.go` → `TestImportBoundary_StdlibOnly` — why
  the validation cannot live in `internal/turnevent`.
- `cmd/pyry/interactive_turn_v2.go` → `eventKind`, `interactiveTurnEmitterV2.Handle`
  — the arm AC4 adds, and the `default` whose Debug becomes reachable for a
  production-producer variant again.
- `cmd/pyry/stream_turn_busy_test.go` → `turnEventVariants`,
  `TestTurnMarkFor_TotalOverEveryVariant` — the marker-derived totality table AC5
  adds a row to. Confirmed the SOLE such guard in the tree.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — read to confirm its whitelist
  default already answers `turnMarkNone`, so no arm is added there.
- `internal/turnbridge/outbound.go` → `MapEvent` — read to confirm its `default`
  drops the variant correctly and no wire shape is owed.
- `docs/knowledge/features/streamsup-package-tool-progress-consumed-by-matching.md`
  — the lesson behind the matcher-arm posture, and the distinction this ticket
  needs: `tool_progress` falls through because one VARIETY is unmeasured; this arm
  falls through because one payload is UNUSABLE. Same shape, different reason,
  and the reason is what a later reader will otherwise "simplify" away.
- `docs/knowledge/features/turnevent-package.md` — the package's stdlib-only
  posture and the sum-type seam.

## Context

Desktop's **Reset session** sends the literal text `/clear`; claude runs it as a
command and announces the reset on its own stdout:

```json
{"type":"conversation_reset","new_conversation_id":"<uuid>","uuid":"<uuid>"}
```

Nothing in the parser knows the type. `ignoredLineTypes` has exactly one member
(`system`) and `consumeLine` has no arm, so the line falls to the `default` branch
and reaches `emitUnrecognized`. The operator asks for a reset and gets a noise row
for it.

This ships the decode: the type gets an arm, a canonical id becomes a turn event,
and a non-canonical one still rings the bell.

**No ADR is warranted.** Every posture this arm takes is already stated in-file by
`rate_limit_event` (#1404), `control_response` (#1500) and `tool_progress` (#2089),
and this arm's one departure from them is local enough to belong at the arm.

**Provenance is thinner than it looks and the plan says so where it matters.** The
announcement's SHAPE is a real capture (parent #2088, claude 2.1.259, observed
2026-09-04), but the recorded bytes elide both id values (`c43fbe8d-…`,
`a2a0b27a-…`) and no capture file in the tree holds a `conversation_reset` line —
verified, a tree-wide grep returns zero files. So the fixtures here are written
from a captured SHAPE with reconstructed id VALUES, which is weaker than the
`emitModelAnnounced` family's "field mapping comes from the committed capture,
never from a hand-built payload". Live confirmation is #2138's job; this ticket
does not need it and does not claim it.

## Sizing — over the line ceiling, and still one ticket

Re-counted against this written plan, not against the pre-plan sketch. Five of
the six size-S boundaries hold comfortably: **3** production source files (≤ 5),
**1** new exported type (≤ 5), **0** consumer call sites needing simultaneous
update (≤ 10 — the change is additive, no signature moves), **5** acceptance
criteria (≤ 5), and **2** decline branches (≤ 10).

The sixth is exceeded. Total written work projects to **≈1000 lines** — this spec
at 469, ~215 of production code across the three files, and ~320 of tests —
against a ceiling of 800. The spec is the single largest line item and carries a
mandated `## Security review` the nearest analogue (#1600, 340 spec lines) did not
owe.

**It builds anyway, because the floor beats the ceiling.** The only cut this work
admits is *declare the variant* | *map the line onto it*. The first child's sole
consumer is its sibling: a variant no producer emits has no caller outside the
family, and neither AC4 nor AC5 stands alone without the producer that makes the
diagnostic reachable. That is precisely the shape the size floor forbids, and the
floor governs when the two disagree — the ceiling protects against a budget miss,
which costs one continuation leg, while the floor protects against a ticket that
cannot be verified on its own, which no resume fixes. The refiner reached the same
conclusion independently in the ticket's Size Estimate.

Split depth is not what decides it here and is recorded so the next reader does
not re-derive it: parent #2088, grandparent none — a split would have been legal
by depth. It is the floor, not the depth cap, that closes the question, so no
`needs-human:sizing` marker is warranted.

## Design

Three production files, all additive. No signature changes anywhere, so no
consumer call site needs a simultaneous update.

### 1. `internal/turnevent/event.go` — the variant

```go
// ConversationReset reports that claude reset the conversation and mounted a
// fresh transcript under a new id.
type ConversationReset struct {
    NewConversationID string
}
```

Plus the `isTurnEvent` value-receiver marker and the `var _ Event` assertion,
both in the existing blocks.

Three properties the doc must carry, because none of them is obvious from the
four-line declaration:

- **Canonical BY CONSTRUCTION, and never empty.** The producer runs the id
  through `transcript.ValidStem` before construction and emits nothing when it
  fails, so a consumer holds a 36-char lowercase hex-and-hyphen stem or holds no
  event. A downstream resolver of `<dir>/<id>.jsonl` does not re-derive that
  question. `ModelAnnounced.Model`'s "never empty — the producer's gate does not
  emit on an empty model" is the precedent for the phrasing.
- **No cap, and no `Truncated` report — a first for a claude-derived string in
  this package, and the reason is the gate, not an oversight.** `ValidStem` fixes
  the length at exactly 36 and the alphabet at `[0-9a-f-]`, which is a strictly
  stronger bound than `truncateField` gives any sibling. A `Truncated bool`
  alongside a fixed-length field would be permanently false, and `ModelAnnounced.Truncated`'s
  own doc is the argument against carrying a bit that cannot vary.
- **It carries CLAUDE's identity on purpose, inverting the family rule.**
  `systemTaskStartedLine` and `turnevent.BackgroundTaskStarted` both omit
  `session_id` precisely because claude's session identity is NOT the daemon's
  conversation identity. Here that identity is the entire payload: following it
  is what the event exists for. State the inversion at the field, or a later
  reader applying the family rule deletes the only thing on the struct.

It opens and closes no turn (§ AC5).

### 2. `internal/streamsup/parser.go` — the decode

A bounded decode target beside its siblings:

```go
type conversationResetLine struct {
    NewConversationID string `json:"new_conversation_id"`
}
```

`uuid` is deliberately absent: nothing in the daemon reads it, and a field never
declared cannot reach a log or an event — `systemInitLine`'s argument for its own
omissions, applied to the one other key the captured shape carries.

One emitter, contract only:

- `func (p *Parser) emitConversationReset(line []byte) bool` — decodes the
  TOP-LEVEL line bytes, emits AT MOST ONE `turnevent.ConversationReset`, and
  reports whether it CONSUMED the line. Returns `true` only when the decoded id
  satisfies `transcript.ValidStem`.

And the arm in `consumeLine`, shaped exactly like `consumeToolProgress`'s: call
the matcher, `return` on `true`, otherwise fall to
`emitUnrecognized(turnevent.UnrecognizedLineType, sl.Type, line)` — written out
rather than reached by `fallthrough`, for that arm's stated reason (the `default`
is not the next case in source order, and a `fallthrough` would also run the
`ignoredLineTypes` test, which this type must never pass).

Four design statements belong at the arm:

- **Not an `ignoredLineTypes` member**, for #1404's and #1500's reasons verbatim:
  that list is top-level types only and MEASURED, and the 2026-07-27 census never
  ran `/clear`. Membership would ALSO swallow the announcement in silence, which
  is the defect this ticket fixes rather than a cost it tolerates.
- **This arm deliberately does NOT take the neighbours' stronger guarantee.**
  `rate_limit_event` and `control_response` each document that `emitUnrecognized`
  is unreachable for their type "BY MATCHING rather than by list membership", and
  call that the stronger of the two. This arm declines it: AC2 routes a reset the
  daemon cannot act on to the unrecognized lane ON PURPOSE. Say so here, or the
  next reader "fixes" the matcher into an unconditional consume and restores the
  silent swallow.
- **`tool_progress` is the shape precedent, not the reason precedent.** That arm
  falls through because one VARIETY is unmeasured. This one falls through because
  one payload is UNUSABLE. Copying the shape without the reason is what turns two
  different decisions into one rule nobody can re-derive.
- **One decline rule, no rungs, and no drop-reason vocabulary.** Undecodable,
  absent, empty, and non-canonical all return `false` and are answered identically
  — `emitRateLimit`'s formulation for absent/empty/no-such-key, widened by one
  case. No `p.log.Debug` is added on any decline path, and that DIVERGES from
  every sibling for a reason worth stating: those arms CONSUME their declines, so
  a Debug is the only trace they can leave, whereas here the decline is SURFACED
  as an `Unrecognized` carrying the offending bytes to a client — strictly more
  visible than a Debug the production daemon does not print. A Debug beside it
  would be a second, weaker record of the same fact.

Note that malformed JSON never reaches the emitter — `consumeLine` has already
decoded the line into `streamLine` — so the only reachable undecodable case is a
non-string `new_conversation_id`, exactly as `systemInitLine`'s doc records for
its own field. That case takes the same decline path as an invalid stem, which is
the correct answer for it: a numeric id is not a canonical stem either.

### 3. `cmd/pyry/interactive_turn_v2.go` — the diagnostic, and two corrections

- A new `eventKind` arm returning the variant NAME only, `"conversation_reset"`.
  `NewConversationID` is claude-authored and is not returned, per the package rule
  the neighbouring arms state.
- **The arm's own note is the inverse of its five neighbours'.** Each of those
  records that its variant is now claimed by a `Handle` case, so the
  `interactive_turn.unknown` Debug is no longer live for it. This variant has NO
  `Handle` case, deliberately: that Debug being reachable is what AC4 tests. Do
  not add a `Handle` arm to tidy this.
- **Two prose claims go false on this change and are corrected in the same
  commit.** Nothing reddens when they rot — they are comments — so they survive
  unless edited deliberately:
  - the `ModelAnnounced` arm's claim that the Debug "is now a live call site for
    nothing the production producer emits", supported by "Handle has an arm for 17
    of `turnevent.Event`'s 18 implementations, and the one without is
    PermissionRequest". After this change the count is **17 of 19, with two
    missing**, and only one of them is `PermissionRequest`.
  - the `SlashCommandList` arm's claim that it "was the last variant a production
    producer emitted without a case, which is what lets the ModelAnnounced arm
    above generalise its own claim".

  Corrected in the house `CORRECTED <date> (#2134):` style — the falsified
  sentence is left legible and the correction sits under it, as `ignoredLineTypes`
  and `emitModelList` both do. A sweep confirms these are the only two sites: the
  count appears once in the tree and the totality table carries no equivalent
  number.

### Not touched, each for a checked reason

- `turnMarkFor` — its opener set is a whitelist whose `default` already returns
  `turnMarkNone`. #1600 and #1854 are the precedents for a variant that needs the
  table row and no arm.
- `turnbridge.MapEvent` — its `default` drops the variant, which is correct: the
  boundary a client draws comes from the `session_transition` frame, so this event
  owes no wire shape.
- `interactiveTurnEmitterV2.Handle` — no arm, per AC4 above.
- `internal/e2e/realclaude` — `dropped_line_capture_test.go`'s `dropcapClassify`
  classifies from the SHIPPED parser rather than a list of mapped types, and
  `interactive_stream_unrecognized_test.go`'s type list is prose in a header
  comment. Neither reddens; extending the comment is the documentation phase's
  call. Nothing here compiles that package, so no `-tags e2e_realclaude` vet is
  owed.

## Concurrency model

None added. `consumeLine` runs on the single stdout-reading goroutine that owns
`*Parser`; the emitter adds no state to the parser, no goroutine, no channel and
no lock. The parser's one accumulator (`thinkingSinceEmit`) keeps its single
boundary at the `result` arm and is untouched — mapping a line changes what is
SENT, not where that boundary is.

The event rides the existing `p.emit` path into whatever sink is attached, under
that path's existing back-pressure. Because the variant is `turnMarkNone` it is
DROPPABLE under sink saturation in `stream_turn_drain.go`; that is #2135's AC5 to
reason about, not this ticket's, and it is named here so the next reader does not
take this ticket's silence for a claim of delivery.

## Error handling

| Input | Outcome |
|---|---|
| `new_conversation_id` is a canonical lowercase UUID stem | one `ConversationReset` carrying it; no `Unrecognized`; line consumed |
| key absent | no event; `Unrecognized{Site: UnrecognizedLineType, Kind: "conversation_reset"}` |
| key present but empty | as above |
| key present, not a canonical stem (wrong charset, wrong length, uppercase, path-ish) | as above |
| key present, not a JSON string | as above (whole-payload decode fails; same decline path) |
| a top-level type that is neither mapped nor on `ignoredLineTypes` | unchanged — still reaches the unrecognized lane |

No panic path: `json.Unmarshal` into a value target, one string comparison, one
regexp match. Nothing here can fail in a way that poisons a later line — the
existing per-line isolation is unchanged.

## Testing strategy

Scenarios, not bodies. `internal/streamsup/parser_test.go` unless noted.

- **AC1** — a captured-shape reset line with a canonical id produces exactly one
  event, `turnevent.ConversationReset{NewConversationID: <id>}` by
  `reflect.DeepEqual`, and the emitted slice contains no `Unrecognized`. The
  no-`Unrecognized` half is asserted over the whole slice, not just element 0:
  "exactly one event and it is the right one" and "no noise row" are two claims
  and AC1 makes both.
- **AC1, verbatim half** — the emitted id is byte-identical to the input's, so a
  future normalisation (lowercasing, trimming, re-formatting) reddens.
- **AC2** — table over the decline set: key absent, `""`, uppercase hex, 35 and 37
  chars, hyphens misplaced, a non-hex character, `../../etc/passwd`, a stem with a
  `/`, a stem with a `.`, and a numeric (non-string) id. Each row asserts BOTH
  halves: zero `ConversationReset` in the slice, and exactly one `Unrecognized`
  whose `Site` is `UnrecognizedLineType` and whose `Kind` is `conversation_reset`.
  The traversal-shaped rows are the ones that matter to the security review and
  are labelled as such in the table.
- **AC2, silence half** — the decline path writes no Debug record. Asserted
  against a captured log, because "no drop log" is a design decision here rather
  than an omission, and the sibling arms all log.
- **AC3** — a top-level type with no arm and no list membership still reaches the
  unrecognized lane. An existing test already covers the general case; this adds
  the neighbour row that would catch a mis-typed case label silently widening the
  new arm.
- **AC4** — `cmd/pyry/interactive_turn_v2_test.go`, modelled on
  `TestInteractiveTurnEmitterV2_ModelAnnouncedEventKindNamesTheVariant`, with one
  structural difference: that test needs an EMPTY cursor to reach an `eventKind`
  call site, because a live cursor is claimed by the `Handle` arm. This variant
  has no arm, so the test drives it through `Handle` with a LIVE cursor and lands
  on the `default` — which is the call site AC4 is actually about. Assertions:
  the log contains `kind=conversation_reset`, does not contain `kind=unknown`, and
  does not contain the fixture id.
  **The fixture is the trap.** It must satisfy `ValidStem` or nothing is emitted,
  and it must share no substring with the log's own text or the leak assertion is
  vacuous. A full 36-char hex stem declared as a named constant beside the file's
  other sentinels satisfies both; slog's `time=` attr is dropped in the handler,
  as the rate-limited test's measured reason requires.
- **AC5** — one row in `TestTurnMarkFor_TotalOverEveryVariant`'s table,
  `turnMarkNone`, with the note that the whitelist's `default` already answers it,
  so the row asserts an existing answer rather than a new arm. Adding the
  `isTurnEvent` marker reddens this test until the row lands; that RED is the
  first thing to observe in Phase B.

RED before GREEN throughout: the marker-derived totality test is the cheapest
guaranteed red, and the parser tests fail on a missing arm before the arm exists.

Gate: `go test -race ./internal/streamsup/... ./internal/turnevent/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Does a decline deserve a Debug after all?** Resolved in Design: no. The
   `Unrecognized` event is the record, and it is more visible than a Debug the
   production daemon does not print. Revisit only if #2135 finds it needs to
   distinguish decline causes, which it has no reason to — it consumes emitted
   events, not declines.
2. **Should the id be length-capped as well as shape-validated?** Resolved: no.
   `ValidStem` is an anchored full-match against a fixed-length pattern, so it IS
   the length cap; a `truncateField` beside it would be dead code.
3. **Does `uuid` belong on the decode target for future use?** Resolved: no.
   Nothing reads it, and `systemInitLine`'s twenty-one omissions are the standing
   answer — a field never declared cannot leak. #2135 can add it if it finds a
   consumer, which it is not expected to.

## Security review

**Verdict:** PASS

Four load-bearing claims below were verified empirically against a throwaway
probe rather than reasoned from memory, because each one decides whether a
traversal or injection row is actually closed. Results are quoted at the finding
that rests on them.

**Findings:**

- **[Trust boundaries] No findings — the boundary is the point of the ticket, and
  it is a single named predicate at a single call site.** claude's stdout is
  untrusted; `emitConversationReset` is where one field of it becomes a daemon
  fact, gated by `transcript.ValidStem` and nothing else. Downstream holds a
  canonical stem or holds no event.

  Two limits on what that gate MEANS, written here so #2135 inherits the claim
  and not more than the claim:

  - **It guarantees WELL-FORMEDNESS, not AUTHENTICITY.** A compromised or buggy
    claude can name any well-formed stem, including another session's. The gate
    stops a malformed id from reaching a path resolver; it does not establish
    that claude was entitled to name this id. That is the same trust the daemon
    already extends to claude for session ids generally, so the gate adds
    protection without adding authority — but selling it as authentication would
    be an overclaim, and the next ticket is the one that would act on it.
  - **"Canonical by construction" is a claim about the PRODUCTION producer, not a
    type-level seal.** `ConversationReset` has an exported field, so a
    hand-constructed value inside the module can carry anything. That is the
    package's stated posture rather than an oversight: `NewPermissionRequest`'s
    doc names the "construct-then-validate-downstream convention" explicitly, and
    sealing this one variant would break the uniform shape the sum type and its
    marker-derived totality guard depend on. Documented at the field instead.

- **[File operations] No findings in this ticket, and the gate's alphabet is what
  keeps the downstream resolution safe.** Nothing here opens, stats, or resolves a
  path. The threat is inherited: #2135 resolves `<dir>/<id>.jsonl` from this
  field. `uuidStemPattern` is an anchored full match over `[0-9a-f]` and `-` at a
  fixed length of 36, which excludes `/`, `\`, `.`, NUL and every other path
  metacharacter by construction — traversal cannot survive it.

  The one non-obvious way an anchored pattern leaks is a trailing newline, since
  some regexp dialects let `$` match before it. **Verified: Go's `$` is `\z`
  semantics.** Probe results — `"…e1f0"` → true; `"…e1f0\n"` → **false**;
  `"…e1f0\n../../etc/passwd"` → **false**; `"0F1E…"` (uppercase) → false;
  `"../../etc/passwd"` → false; `""` → false; a 35-char stem → false. No
  newline-injected path can pass, so the resolver downstream cannot receive an id
  with an embedded line break.

- **[Trust boundaries — forgery] No findings; the forge vector is closed
  structurally, and it was checked rather than assumed.** A tool result whose text
  is literally `{"type":"conversation_reset",…}` cannot forge a reset, because
  `consumeLine` switches on the TOP-LEVEL `streamLine.Type` and never re-scans
  nested content — the property that struct's doc already states. The new decode
  keeps it: `conversationResetLine` is unmarshalled from the top-level line bytes
  and reads only the top-level key. **Verified:** a line carrying
  `message.new_conversation_id` and no top-level key decodes to `""` with no
  error, so a nested id is invisible to the arm and takes the decline path.

  A duplicate top-level key resolves last-wins (**verified**). Not exploitable —
  claude authors the whole line, so there is no second writer to smuggle a value
  past a first — and the behaviour is deterministic, so it is recorded rather than
  guarded.

- **[Error messages, logs, telemetry] No findings, and the strongest form of the
  guarantee is available here.** `emitConversationReset` contains no log call on
  any path, so `NewConversationID` has no logging surface at all — stronger than a
  sibling's "we chose not to log it". The decline path's `emitUnrecognized` logs
  `site`, `type`, `bytes` and `truncated` only; the raw bytes are NOT logged
  (verified by reading it). `eventKind`'s new arm returns the variant name only,
  and AC4's test asserts the fixture id is absent from the whole captured log.

- **[Network & I/O] No findings — every dimension is already bounded, none of them
  by new code.** Line length is capped upstream by the parser's `maxBuf` in
  `Write`; the accepted field is capped at exactly 36 by the gate; the declined
  line's bytes are capped by `maxUnrecognizedRaw` in `truncateRaw` and scrubbed to
  valid UTF-8.

  **Considered and accepted:** a rejected id DOES reach a client, inside
  `Unrecognized.Raw`. That is not new exposure — it is what the unrecognized lane
  already does for every unrecognized line, the bytes are claude's own
  announcement on a stream the client is already receiving, and they are bounded
  and scrubbed. Accepting it is also what AC2 asks for; suppressing it would
  reinstate the silent swallow this ticket exists to remove.

  **Rate is unbounded here, deliberately and consistently.** Nothing throttles
  `conversation_reset`, exactly as nothing throttles `system/init` (once per turn)
  or `tool_progress` (every 30s). The event stream's own back-pressure bounds the
  cost, and this variant is droppable under saturation because it is
  `turnMarkNone`. See the OUT OF SCOPE note below for the downstream
  amplification.

- **[Concurrency] No findings — nothing is added to reason about.** No goroutine,
  no channel, no lock, no new parser state. `consumeLine` runs on the single
  stdout-reading goroutine that owns `*Parser`, and the parser's one accumulator
  keeps its single boundary at the `result` arm, untouched. There is no lock
  ordering to document because there is no lock.

- **[Tokens, secrets, credentials] Not applicable, stated rather than skipped.**
  The id is neither a credential nor a capability — it names a local transcript
  file and grants nothing. No token is generated, stored, compared or rotated on
  this path. The one adjacent rule that DOES apply is the logging one, discharged
  above.

- **[Cryptographic primitives] Not applicable.** No randomness is generated:
  the id is claude's, not minted here. `ValidStem` is a shape predicate, not an
  authentication check, and nothing on this path compares attacker-controlled
  bytes against a secret — so `crypto/subtle` has no role. Naming this matters
  only because a shape check adjacent to a trust boundary is easy to mistake for
  a security comparison; it is not one, and the Trust-boundaries finding says so.

- **[Subprocess / external command execution] Not applicable in this ticket.** The
  id reaches no `exec.Command` argument and no shell. It is READ from a subprocess
  rather than passed to one, and the read side is the boundary audited above.

- **[Testing] SHOULD FIX — AC2's hostile rows must exercise the PREDICATE, not the
  decoder, or the traversal coverage is vacuous.** Every traversal- and
  injection-shaped row (`../../etc/passwd`, a stem with `/`, a stem with `.`, the
  newline-suffixed stem) must be a well-formed JSON string so the decode SUCCEEDS
  and `ValidStem` is what rejects it. Written carelessly — as a raw unquoted
  payload — the row fails at `json.Unmarshal` instead, lands on the same decline
  path, passes green, and proves nothing about the gate. The numeric-id row is the
  one that is SUPPOSED to fail the decode (**verified:** it errors the whole
  unmarshal), and it is the only one. Phase B must keep those two groups distinct;
  the verifier can check it by reading the fixtures.

- **[Threat model alignment] OUT OF SCOPE, named and handed over.** Two questions
  belong to **#2135**, which is the ticket that acts on this event:
  1. **Authorization** — whether the session claude names is one this daemon
     should re-key to, per the authenticity limit under Trust boundaries.
  2. **Amplification** — a repeated announcement drives a repeated registry
     re-key. Bounded here by sink back-pressure and droppability; whether the
     actuator needs its own idempotence or rate discipline is a property of the
     actuator, not of the decode.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06

## Revisions

### 2026-09-06 — implementation

Three departures from the plan as committed. None changes a contract; each
sharpens a claim the plan stated too strongly or too loosely.

1. **"The decline path writes no Debug record" was wrong as written, and the
   corrected claim is narrower.** A declined reset produces exactly ONE record —
   `emitUnrecognized`'s own content-free `streamsup: unrecognized payload` — because
   the arm falls through to it. What the arm must not add is a SECOND, weaker record
   of the same fact, and `emitConversationReset` contains no log call on any path.
   The design decision is unchanged (no drop-reason vocabulary); only the
   observable it is asserted through moved. `TestParser_ConversationResetDeclineAddsNoDropRecord`
   pins the corrected form: exactly one record, it is `emitUnrecognized`'s, and no
   attr on it carries the id.

2. **The security review's SHOULD FIX is discharged, and mechanically rather than
   by care.** Every hostile row in
   `TestParser_ConversationResetDeclinesReachTheUnrecognizedLane` carries the value
   the decode must yield, and the loop re-decodes each row's payload and fails if it
   does not come back intact — so a fixture that quietly became malformed cannot
   pass the test by failing at `json.Unmarshal` and landing on the same decline
   path. The numeric-id row is the single row exempted, because failing the decode
   is what it is for.

3. **One test beyond the plan's list.**
   `TestInteractiveTurnEmitterV2_ConversationResetEmitsNoFrame` asserts the other
   half of the variant's contract on the interactive lane: no frame pushed and no
   turn opened. The plan covered the diagnostic (AC4) and the classification (AC5)
   but not the "deliberately unhandled" behaviour that sits between them, which is
   the property a future `Handle` arm would silently break.

Open questions 1–3 were resolved in the plan as written and none reopened during
implementation.
