# #2250 — forward the `allowed` reading after a non-allowed one

Give `emitRateLimit`'s gate a falling edge, so a client that lit a quota banner on
an `allowed_warning` reading gets a frame that takes it down.

## Files read

- `internal/streamsup/parser.go` → `emitRateLimit` — the three-rung gate this slice
  adds a fourth reading to; its doc is the first of the seven prose sites.
- `internal/streamsup/parser.go` → `benignRateLimitStatus` — the one silent value,
  and the doc that states the tally rule this slice applies ("a tally in a comment
  goes stale on the next capture while the shape does not").
- `internal/streamsup/parser.go` → `maxRateLimitField` — its rate paragraph asserts
  "under the gate below a healthy run emits ZERO", which the latch qualifies.
- `internal/streamsup/parser.go` → `Parser` (the type doc's AMENDED chain) and its
  fields `compacting`, `taskProgressToolUses` — the near analogue and the last field
  added. `compacting` is a published falling edge; its RESET does not transfer.
- `internal/streamsup/parser.go` → `emitCompactingStatus`, `compactingStatus` — the
  edge-pair state machine this one is shaped after, including its rising-edge
  suppression.
- `internal/streamsup/parser.go` → `consumeLine`'s `result` arm — the ONE reset
  point, which this field is deliberately absent from. Reading it is what shows the
  latch would be cleared before the reading that needs it ever arrives.
- `internal/streamsup/rate_limit_capture_test.go` → `capturedInitializeStdoutLines`,
  `replayInitializeCapture`, `rateLimitCapturePinnedEvents`,
  `capturedRateLimitInfos` — #2249's helpers; the multi-arm replay is a variant of
  the second and reuses the first and fourth unchanged.
- `internal/streamsup/initialize_capture_test.go` → `capturedInitialize`,
  `initCaptureArms`, `initCapturePath` — the closed arm selector that stops a
  hand-built file being swapped in behind the provenance assertions.
- `internal/streamsup/parser_test.go` → `TestParser_RateLimitSilentRungsLogTheirReason`,
  `TestParser_RateLimitEmitsForNonBenignStatus`,
  `TestParser_RateLimitCapturedBenignLineIsSilent` — every row builds a FRESH parser,
  which is why a latch defaulting closed leaves all of them green.
- `internal/turnevent/event.go` → `RateLimited` and its `Status`, `Utilization` —
  the variant doc that defines the event as a window "in a state other than the one
  measured-benign one" and says it "fires only when the producer's gate says a limit
  is in force". Both sentences go stale.
- `internal/protocol/interactive.go` → `RateLimitedPayload` — the wire shape, whose
  doc restates the gate as "the one measured-benign status is silent and any other
  non-empty status emits".
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.RateLimited` arm — "a
  benign status produces no event at all".
- `cmd/pyry/interactive_turn_v2.go` → the interactive emitter's
  `turnevent.RateLimited` arm — "emitRateLimit's gate fires at most once per run".
- `docs/protocol-mobile.md` § `rate_limited` — the gate sentence, the
  `status`-is-an-opaque-label rule the falling edge sits against, and the two client
  readings the table below adds.
- `docs/knowledge/features/streamsup-package.md` — read for prior lessons in this
  area; nothing there constrains this slice, and this ticket writes none of it.

Re-measured at `25cfaa70` over `internal/e2e/realclaude/testdata/initialize_control*`:
the base arm carries the `allowed_warning`/`seven_day`/`0.94` reading as its second
stdout line and a `result` line later in the same record; each of the three benign
arms carries exactly one `allowed`/`five_hour` reading with no `utilization` key.

## Context

`emitRateLimit`'s silence on the benign status is load-bearing — claude reports the
usage window once per run whatever its state, so a 1:1 mapping puts a row on every
healthy run. But the silence is unconditional, so the reading that would CLEAR a
warning is dropped by the same rung that suppresses the routine one. A client that
showed a banner on `allowed_warning` has nothing that ever takes it down.

This slice asks for a falling edge, not a relaxation. Forward an `allowed` reading
only when a non-allowed one preceded it on the same parser; stay silent otherwise.

No ADR is warranted: the design is an instance of a pattern this package already
carries (`compacting`'s published falling edge), and what is new about it is argued
at the field rather than across packages.

### Sizing

Five production files, 0 new exported types, 0 consumer call sites, 5 acceptance
criteria, one added switch arm. **Total written work runs to roughly 850 lines,
over the 800-line ceiling by about 50.** It is not split: the only candidate cut is
"the falling edge" against "the six other doc sites", and the second slice has
exactly one consumer, is observable nowhere on its own, and shipping the first alone
lands code whose own documentation says it does the opposite. The floor rule wins
over the ceiling; the overage is comment density over one function, not surface area.

## Design

### The state

One unexported bool on `Parser`, named for the condition it holds rather than for
the event it produces:

```go
rateLimitNonBenign bool // true between a non-benign reading and the benign one that clears it
```

Written and read ONLY by `emitRateLimit`. It is the first piece of cross-line state
on `Parser` that `consumeLine`'s single reset point does not clear, and that is the
substance of the slice rather than an omission — see "The reset" below.

It is also the first parser state DERIVED FROM CLAUDE'S BYTES that is designed to
outlive the run, so two constraints on it are stated rather than inherited. Nothing
in the daemon may key a behaviour on it: it is unexported, has no accessor, and is
read at exactly one place, to decide whether one display frame is published.
`turnevent.RateLimited`'s "a REPORT, never a control input" rule reaches inside the
producer here, and a slice that wants to read this field from anywhere else is
re-opening that analysis. And it holds no VALUE claude sent — a bool, so no input
can grow it and nothing forward-carries the remembered reading's text, which is what
makes "never from the remembered non-allowed one" a property of the shape rather
than a rule to enforce.

### The gate

`emitRateLimit`'s switch on `rl.Info.Status` grows a fourth reading. The three rungs
its doc names are unchanged in ORDER and in what they answer; what changes is that
rung 1 becomes conditional and rung 2 records that it fired.

| reading | latch before | action | latch after |
|---|---|---|---|
| non-benign, non-empty | either | emit (unchanged) | open |
| benign | closed | Debug `benign`, silent (unchanged) | closed |
| benign | open | **emit, claude's values verbatim** | closed |
| empty / absent container | either | Debug `no_rate_limit_info`, silent | unchanged |
| undecodable line | either | Debug `undecodable`, silent | unchanged |

Three properties fall out of that table and each answers one acceptance criterion.
The falling edge fires ONCE, because the emitting benign arm closes the latch before
it emits. A turn boundary does not clear it, because no `result` arm touches the
field. And a reading the gate silences for want of a report leaves it alone, because
both silent-for-want-of-a-report paths return before the switch can write.

The emit itself is UNCHANGED code: the same `bound` closure, the same two capped
strings, the same passthrough of `ResetsAt` and the `*float64` `Utilization`. Every
field on the clearing frame is read from the `allowed` line that produced it and
none from the remembered non-benign one. That is a property of where the write sits
(the latch is a bool; it stores no values to leak forward), not a rule to enforce.

No new log constant. `compactingEndedMsg` exists because that falling edge's Debug
record predates the frame that publishes it; here the emit IS the record, and a
second one would be a sink to keep in step for no observed need.

No daemon-computed boolean and no new payload field. The client's discriminator is
`status` carrying claude's benign value — every field on this frame is claude's, and
`Compacting.Active` is a daemon bool precisely because claude reports no equivalent
there.

### The reset

`compacting` is the near analogue in shape and NOT in reset. It is cleared at
`consumeLine`'s one `result` reset, and a usage-limit latch cleared there would be
gone before the reading that would have used it ever arrives: claude emits
`rate_limit_event` once per run, the parser outlives the run (`cmd/pyry` builds one
per session and `streamsup` rebinds the same one across child respawns, the
invariant `assistantErrorCategory`'s doc states), and in every realistic sequence the
two readings are separated by at least one `result` line and usually by a whole child.

So the field is absent from the reset point on purpose, and `Parser`'s doc chain and
the field's own doc say so out loud rather than let a reader infer it from
resemblance. The residual this leaves is bounded and fails in the safe direction: a
latch left open by a child that dies costs at most ONE extra benign frame on the next
`allowed` reading, which is a true statement about the window claude just reported.

`Parser`'s AMENDED chain currently ends at #2234's "FOUR pieces of cross-line
state"; there are five fields today, #2246's task-progress map having taken every
argument above it by inheritance and added no entry. This slice's entry says SIX and
names that gap, because a chain reading FOUR → SIX with nothing between it is a
reader's error to make.

### Scope boundary named in the doc, not engineered around

A `/clear` rotation or a session eviction mints a new parser and therefore a fresh
latch, so a warning shown before a rotation is not cleared by this. That is a
limitation the wire doc names. Reaching for session-level state would pull
`internal/sessions` into a slice that is otherwise one function's gate.

### The two client readings the wire doc must add

Both are measured, and neither is derivable from the user story:

- **The clearing frame names a DIFFERENT limit than the warning it clears.**
  `limit_type` reads `five_hour` on every benign reading on file against `seven_day`
  on every warning, so a client pairing a banner to its `limit_type` never matches
  the frame that takes it down.
- **The clearing frame reports no utilization.** Every benign reading omits the key,
  so it crosses as `null`. A client expecting "now 40% spent" gets nothing to render.

That also bounds what the frame may CLAIM. claude reports one window per line and
chooses which, so a return to `allowed`/`five_hour` is claude declining to report a
non-benign window, not evidence that the seven-day limit lifted. The honest reading
is "claude's latest reading of the usage window is benign"; the doc must not upgrade
it into "the limit is over".

The doc also has to resolve the falling edge against § `rate_limited`'s existing rule
that `status` is an opaque label a client must not treat as a closed set, rather than
leave both standing: the benign value is the ONE value a client compares against, and
it is the only one measured stable on every claude version on file.

### The stale tally, corrected while here

`benignRateLimitStatus`, `emitRateLimit` and `turnevent.RateLimited` each carry a
record tally already wrong at `25cfaa70` ("EXCEPT ONE, across four claude versions",
"every committed record but one"). Eight records now read `allowed_warning`, on two
of five versions. `benignRateLimitStatus`'s own doc states the rule: drop the count
rather than update it, since a tally goes stale on the next capture while a shape
does not. The value-set claim stays — `allowed_warning` is still the only non-benign
value on record and no capture shows a limit actually in force.

## Concurrency model

No goroutine, no channel, no shutdown path. The field is read and written on the
os/exec forwarder goroutine only, under `Parser`'s existing single-writer invariant,
exactly as `compacting` is. A future concurrent reader guards six fields rather than
five. No mutex is added; `postureGate` remains the only parser state legitimately
touched from another goroutine.

## Error handling

The gate's failure modes are the readings above, and every one of them is a silence
rather than an error return — `emitRateLimit` returns nothing, because `consumeLine`
consumes the line by matching. The two that matter for the latch are stated as
design rather than left to fall out: an undecodable line and a line carrying no
usable `rate_limit_info` both leave the latch UNCHANGED, so a malformed line
interleaved between the two readings does not swallow the falling edge.

The pre-existing rung-3 cost is unchanged and is not re-argued here: a container
RENAME goes undetected by any automatic test, and the live drop census is the
detector.

## Testing strategy

Hermetic only. No live-claude run: no live run can be made to cross a warning band
on demand, and `internal/e2e/realclaude`'s stream-liveness drain sees one reading per
run so it cannot observe a falling edge. No new relay e2e: the
`TestRelayV2_StreamRateLimit*` family already proves a `turnevent.RateLimited`
reaches a connected phone, and the falling edge is that same event on that same path.

**The ordered replay, from committed bytes.** A multi-arm variant of
`replayInitializeCapture` feeds several arms' lines through ONE parser in order.
Composed from the base arm (the `allowed_warning` reading, plus a `result` line later
in the same record) followed by two benign arms, it replays warning → `result` →
`allowed` → `allowed` and covers the first three criteria in one pass over nothing
but claude's own bytes. The composition stays inside the `capturedInitialize` family,
whose closed arm selector is what stops a hand-built file being swapped in behind the
provenance assertions; the test names which records it was composed from, and
re-derives each arm's readings through `capturedRateLimitInfos` so a re-capture that
changed a status reddens the premise rather than the conclusion.

Scenarios:

- Two events, in order: the warning verbatim, then one whose `Status` is claude's
  `allowed`, `LimitType` `five_hour`, `ResetsAt` and `Utilization` (nil) taken from
  the `allowed` line. Assert the second's fields against the SECOND arm's re-derived
  reading, so a mapping that republished the remembered warning fails.
- The fourth line yields nothing: the second benign arm adds no third event.
- The `result` line between them is real, re-derived from the base arm's own bytes
  rather than assumed, so the turn-boundary criterion is not vacuous.
- Zero `turnevent.Unrecognized` across the whole composition.

**Hermetic rows for what no capture holds.** A table over one parser, since the
latch is per-parser state: a non-benign reading, then a `rate_limit_event` carrying
no usable `rate_limit_info`, then a benign one — the falling edge still fires on the
third. Its siblings: an undecodable line in the same position; a benign reading with
no prior non-benign one on a fresh parser, silent; two consecutive falling edges
requiring two non-benign readings.

**The falling edge is a FOURTH path through the log sweep.**
`TestParser_RateLimitIsLoggedContentFree` sweeps the emit, benign and undecodable
paths with marker values and asserts none reaches a log record. The new path — a
benign reading that EMITS — is the one a future edit is most likely to explain
itself with, so it gets a row there carrying its own markers rather than riding the
existing emit row, which runs on a fresh parser and a non-benign status.

**The silence proofs stay green unchanged.** Every row of
`TestParser_RateLimitSilentRungsLogTheirReason`,
`TestParser_RateLimitCapturedBenignLineIsSilent` and
`TestParser_RateLimitBenignCaptureArmsReportNoUtilization` builds a fresh parser, so
a latch defaulting closed leaves all of them asserting exactly what they did.
`TestRelayV2_StreamRateLimitBenignReachesNoPhone` and `internal/e2e/realclaude`'s
healthy-run assertion are untouched for the same reason.

Gate: `go test -race ./internal/streamsup/... ./internal/turnbridge/... ./internal/e2e/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Does the falling-edge arm want its own drop-reason constant for the case where
   the latch is closed?** Provisional answer: no — the reason is still `benign` and
   nothing distinguishes the two for a reader of the log. Settle by writing the
   silence test and checking whether the existing reason is still the honest one.
2. **Does `Parser`'s AMENDED entry say SIX, or does the chain's skipped fifth entry
   need naming first?** Provisional answer above: say SIX and name the gap in one
   clause. Confirm while writing that the sentence stays about THIS field rather than
   becoming a correction of #2246's.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] The one category with real content, and the finding is a
  capability the slice adds.** The boundary is subprocess stdout → parser state, and
  it stays where it was: `emitRateLimit` decodes the top-level line into
  `rateLimitEventLine` and reads `rl.Info.Status`, never a nested field, so
  `streamLine`'s rule that control shapes are read from the top level only continues
  to stop a tool result forging a usage-limit report. What is NEW is that claude's
  byte now WRITES daemon state that survives the run and the child. Three things
  bound it, and the plan states all three at the field: the value retained is a bool,
  so nothing claude sends can grow it and no remembered text can be republished; it
  is unexported with no accessor and one reader; and no daemon behaviour keys on it.
  A hostile claude's reachable effect is one extra display frame carrying its own
  values on a lane that already carries them.
- **[Trust boundaries] The event-per-line ceiling is UNCHANGED at one, which is the
  amplification question answered rather than waved at.** Each arm of the gate emits
  at most once and the latch adds no second emit to any arm, so N lines still yield
  at most N events. The alternating-status sequence a hostile claude would reach for
  produces exactly what N all-non-benign lines already produce today. That leaves
  `maxRateLimitField`'s accepted rate exposure — once-per-run is measured, not
  enforced, bounded by existing backpressure — at its current size rather than
  widening it, which is why this slice mints no rate bound.
- **[Error messages, logs, telemetry] SHOULD FIX, folded into the plan.**
  `emitRateLimit`'s "NOTHING FROM THE PAYLOAD IS LOGGED, on any path" is the
  invariant most at risk here: the falling edge is the path a future edit is most
  tempted to explain itself with, and the tempting value is exactly the status. The
  design mints no log for it, keeps the closed-latch benign path's content-free
  `reason` keyword, and does not log the latch either. Phase B adds the fourth row to
  `TestParser_RateLimitIsLoggedContentFree`; the verifier should check it landed.
- **[Concurrency] No finding, but the happens-before edge is load-bearing HERE in a
  way it was not for the five fields above.** The latch's read-modify-write sits in
  one switch arm on the os/exec forwarder goroutine with no yield between the read
  and the write, so there is no TOCTOU. Retaining it across a child respawn is safe
  because `cmd.Wait` returns only after the previous forwarder goroutine finished,
  the edge `assistantErrorCategory`'s doc states — and this is the first field that
  DEPENDS on surviving a respawn rather than merely tolerating it, so the plan names
  the edge instead of inheriting it. No goroutine is spawned, so no lifecycle
  question arises.
- **[Network & I/O] No finding, and the reason is the field's type.** The input cap
  (`defaultMaxParseBuf`) and the published caps (`maxRateLimitField`) are unchanged,
  and the falling-edge frame is the same shape with the same two bounded strings, so
  no new byte reaches the wire. No cardinality cap is owed the way `deniedThisTurn`
  and `taskProgressToolUses` each needed one: a bool cannot grow.
- **[File operations] No finding, and one decision keeps it that way.** The slice
  constructs, opens and stats no path. The multi-arm replay helper takes ARM NAMES
  from `initCaptureArms`, never a path — `capturedInitialize` mints its own path from
  that closed set, and growing the new helper a path parameter for composition
  convenience is the specific mistake that would put a hand-built file behind the
  provenance assertions.
- **[Cryptographic primitives] Not applicable, and the near-miss is worth naming.**
  No randomness and no key. The byte-exact `==` against `benignRateLimitStatus`
  superficially resembles the constant-time question and is not one:
  `benignRateLimitStatus` is a published claude vocabulary word, not a secret, so
  `crypto/subtle` is not owed and using it would misdescribe the value.
- **[Tokens, secrets, credentials] Not applicable.** The slice creates, stores,
  compares, rotates and revokes no credential material, and reads no keyring. The
  state it adds is one boolean.
- **[Subprocess / external command execution] Not applicable.** Nothing is spawned
  and no argv, environment variable or signal is touched. Reading a subprocess's
  stdout is the trust-boundary category above.
- **[Threat model alignment] The relevant threat is a hostile or compromised claude
  child reaching a phone, and this frame's posture under it is unchanged.** Both
  strings stay untrusted, bounded at construction and unsanitized, with
  `RateLimitedPayload`'s render rule carrying to the client; no field is added, so no
  new untrusted value crosses. What IS new for a client is a `rate_limited` frame
  whose `status` is the BENIGN value, which a client assuming "any such frame means
  trouble" reads backwards — a correctness hazard rather than a security one, and the
  reason the wire doc states both client readings rather than only announcing the
  edge. Session-scoped clearing across a `/clear` rotation stays out of scope and is
  named as a limitation in the wire doc, not engineered around.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-10
