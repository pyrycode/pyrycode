# #2260 — carry the result line's duration, turn count and running cost on `turn_end`

## Files read

- `internal/streamsup/parser.go` → `resultLine`, `resultStopLine`, `resultDenialsLine`,
  `decodeStopShape`, `decodeModelWindows`, and the `result` arm of the line switch that
  builds `turnevent.TurnEnd`. The three existing targets are the isolation precedent this
  ticket must not break; the arm is where the ordering of resets, decodes and the emit lives.
- `internal/turnevent/event.go` → `TurnEnd`, `ModelWindow`. `ModelWindow.WindowTokens`'
  signed-`int` argument is the one reused for the three counts here.
- `internal/protocol/interactive.go` → `TurnEndPayload` (the wire shape and its stated
  no-`omitempty` rule) and `RateLimitedPayload` (the **no-clamp** posture the Technical Notes
  point at, and the only prior `float64` on this wire, with its "a float64 cannot grow" bound
  argument).
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.TurnEnd` arm — a field-by-field
  build, which is what makes "what reaches the wire is what is named here" true.
- `internal/streamsup/result_stop_shape_test.go` → `stopLineWith`, `capturedStopShapeLine`,
  `TestParser_ResultStopShape_CapturePin`. The synthetic-plus-capture pairing this ticket copies.
- `internal/streamsup/parser_test.go` → `TestStreamLine_StaysSegmentationOnly` — the boundary
  the Technical Notes name as non-negotiable.
- `internal/protocol/interactive_test.go` → `TestTurnEndPayload_RoundTrip`, and
  `internal/protocol/testdata/turn_end.json`. The round-trip compares marshalled bytes against
  the fixture, so the fixture must gain all four keys or AC 4 reddens.
- `docs/protocol-mobile.md` § `turn_end` — the section AC 3 extends and sibling #2261 extends after it.
- `docs/knowledge/features/streamsup-package-result-stop-shape-second-decode-target-and-dr.md`
  — the family's ruling that decode-isolation is argued per *line*, and that widening an
  existing field's readership owes the same review a new decode does.

## Context

`claude`'s `result` line is the turn boundary and carries four numbers the daemon decodes
none of: how long the turn took, how long the API spent, how many round-trips it made, and
what the session has cost. A client has no time-per-turn and no cost reading at all, which is
what pyrycode-desktop#1245 is parked on.

**Two of the four are running totals and two are per turn, and the pair that looks most alike
disagrees.** Re-derived independently for this plan across the 32 committed captures under
`internal/e2e/realclaude/testdata/`: `duration_api_ms` is strictly monotonic across every
multi-turn capture and *larger than `duration_ms`* on 53 of 57 lines, while `duration_ms`
itself is non-monotonic; `total_cost_usd` is strictly monotonic; `num_turns` holds at 2 across
all three lines of each `bypass_reescalation_*` capture where a cumulative counter would read
2, 4, 6. Foreclosing the reading the two duration names invite is half of what this ticket is
for, and it lives in the published doc rather than in code.

**The design deserves no ADR.** It adds four fields to a shape three prior tickets already
settled the rules for; the documentation phase folds the lesson into the package overviews.

**The overage is stated rather than split.** The one-ticket boundary holds on five of its six
lines — 4 production files, 0 new exported types, 0 consumer call sites needing simultaneous
update, 4 acceptance criteria, one decode failure path — and is expected to exceed the
800-line total by roughly 50–100. The refiner's three analogue figures were re-derived from
the commits themselves rather than taken on trust: #2234 at 834 lines (`30ead897` + `ef91aeae`),
#2223 at 989 (`3ce919fb` + `94c373d7`), #2224 at 1111 (`f43b26a9` + `0d61299c`). All three
cleared 800 and field count does not predict the total — the *one-field* ticket wrote the
most. Splitting the four fields buys no child under the ceiling, because the cost is fixed per
ticket (a streamsup test file, a spec, the wire prose, the golden fixture, four production
files' doc paragraphs) and would be paid twice. It also cannot be split *coherently*: AC 3's
deliverable is the four-way per-turn/running-total distinction, and a child carrying two of the
four fields publishes half a table that its sibling then has to rewrite. That is the floor rule
— a slice that cannot be verified on its own is worse than an overage — so this builds as one
ticket. The reduction taken instead is on the fixed cost: this plan is deliberately shorter
than #2224's 303-line one.

## Design

### The decode target

A **fourth** decode target on the `result` line, beside `resultLine`, `resultStopLine` and
`resultDenialsLine`:

```go
type resultTurnTotalsLine struct {
    DurationMS    int     `json:"duration_ms"`
    DurationAPIMS int     `json:"duration_api_ms"`
    NumTurns      int     `json:"num_turns"`
    TotalCostUSD  float64 `json:"total_cost_usd"`
}
```

**A fourth target rather than four fields on a sibling, and the reason is the ticket's stated
isolation property held in four directions instead of three.** `resultLine` decodes a map whose
value shape `claude` controls, `resultDenialsLine` an array whose element shape it controls;
folded into either, a hostile shape there would also zero these four, and a hostile number here
would erase the denials the daemon could otherwise recover or the windows `decodeModelWindows`
reports. Four targets fail independently, and none can disturb the turn boundary, which comes
from the already-decoded `streamLine`.

**Inside the target the four fail as a unit, deliberately.** `resultStopLine` states this
posture for its pair and `resultDenialEntry` for its two strings: a value of any other JSON type
fails the whole decode and takes the everything-absent path, which is the fail-closed direction.
`userLine`'s `json.RawMessage`-per-field alternative — which would make the decode infallible and
isolate the four from each other — was considered and is declined: that property exists there to
protect a field that carries a whole file and an `IsSynthetic` flag whose loss is a disclosure
regression, and nothing of that weight rides here. The entire cost of the unit failure is four
informational numbers reading zero, which AC 2 already documents as indistinguishable from the
zeros `claude` itself sends.

**Signed `int`, not unsigned, on `ModelWindow.WindowTokens`' argument**: a negative reading has to
stay observable rather than wrapping into an enormous positive one. Unlike that field nothing
rejects it here — see the no-clamp rule below — so it is published as `claude` sent it.

**Plain values, not pointers.** All four keys are present and numeric on all 57 observed lines,
so an absent one means a decode failure or a future `claude` rather than an ordinary shape.
`CompactionBoundaryPayload`'s `*int` answers a different question and is not copied; only its
no-clamp posture is.

### The decode function

`decodeTurnTotals(line []byte) (durationMS, durationAPIMS, numTurns int, costUSDTotal float64)`
— a pure function of the bytes, on `decodeStopShape`'s shape and taking its three stated
properties unchanged: it cannot disturb the turn boundary, every failure returns a value rather
than an error, and the decode error is **discarded rather than logged**, because `encoding/json`
quotes the offending input bytes into its error text. `(0, 0, 0, 0)` is a complete answer to
"`claude` said nothing usable".

**No bound, no clamp, no ordering check** — `RateLimitedPayload`'s posture, which the Technical
Notes name. In particular no `duration_api_ms <= duration_ms` consistency check: it would reject
53 of the 57 observed lines. There is no cap to apply and none is needed, on `ResetsAt`'s
argument: the bound here is over the *type's range* rather than over `claude`'s input length, so
an `int` formats to at most 20 bytes and a `float64` to at most 24 — no hostile number can grow
the frame.

### The published fields

`turnevent.TurnEnd` gains `DurationMS`, `DurationAPIMS`, `NumTurns` (ints) and `CostUSDTotal`
(float64), documented per field with which two are running totals, that the daemon differences
neither, and that a zero is a number `claude` sends.

`protocol.TurnEndPayload` gains the same four as `duration_ms`, `duration_api_ms`, `num_turns`
and **`cost_usd_total`** — the one deliberate respelling, per #2199's shape. No `omitempty` on
any of them, per this file's rule as stated at `ToolResultPayload`.

`MapEvent`'s `turnevent.TurnEnd` arm maps all four straight through, undifferenced.

### Provenance: `claude`'s numbers, and threat 1 lands differently here

All four are `claude`-authored values crossing outward to a render surface, so § Security
model's threat 1 lands — but **not** in the shape the three strings beside them carry, and
borrowing their SECURITY paragraph would be wrong. A number cannot hold a control character, a
terminal escape, markup or a URL, so there is no sanitization obligation to hand the client and
no bound to state over `claude`'s input length. What lands instead is threat 1's *misattribution*
half: the daemon verifies none of these numbers, and `cost_usd_total` in particular is a **spend
figure an operator may act on**. A surface rendering it as its own accounting presents
model-authored data as trusted chrome, which is `question_dismissed`'s `outcome` trap and the one
`error_category` names for its own account-shaped values. Every field doc and the wire section
therefore attribute all four to `claude` explicitly, and say the cost is `claude`'s estimate
rather than a billing statement.

**Nothing in the daemon acts on any of the four**, deliberately and structurally: no retry, no
backoff, no teardown, no routing — and, the one a future reader will reach for, **no budget or
spend enforcement** keyed on `cost_usd_total`. That is what keeps a fabricated cost a misleading
label rather than an actuator, and whoever first makes the daemon behave differently on one owes
the review that turns it into one. Stated at the fields, on #2224's precedent.

None of the four flows back toward `claude`: no argument, no environment variable, no file path
and no control-socket reply is derived from any of them.

### Call ordering

`decodeTurnTotals` is called in the `result` arm beside `decodeModelWindows` and
`decodeStopShape`, above `emitRecoveredDenials` and the `p.emit(turnevent.TurnEnd{…})`. The
existing ordering comment already states why every decode sits above the emit; this adds a
fourth decode to that group and changes nothing about the order of the two emits.

## Concurrency model

None introduced. `decodeTurnTotals` reads and writes no parser state and spawns nothing; the
`result` arm runs on the parser's existing single reader goroutine, and the emit path is
unchanged.

## Error handling

One failure mode: the unmarshal fails, on a malformed line or on any of the four keys carrying a
non-numeric JSON type, a number out of `int`/`float64` range, or `null` typed against a scalar.
Every one yields `(0, 0, 0, 0)`; nothing is logged, nothing is returned as an error, and the turn
boundary, `stop_reason`, `outcome`, `is_error`, `terminal_reason`, `error_category` and the
recovered denials are all reached through other decodes and are untouched. A `null` and an absent
key decode to the zero value without failing at all.

The marshal side cannot be poisoned into dropping the frame: JSON has no `NaN` or `Inf` literal
and `encoding/json` rejects a number outside `float64`'s range at decode, so the parser can never
hold a value `json.Marshal` would refuse.

## Testing strategy

`internal/streamsup/result_turn_totals_test.go`, on `result_stop_shape_test.go`'s
synthetic-plus-capture pairing:

- **Discrimination.** One line carrying four mutually distinct values, so a mapping that crossed
  two keys, read one off another, or returned a constant reddens rather than passing three rows.
- **Absent, null and hostile types**, one row per key per shape (string, bool, object, array,
  out-of-range number): the field reads zero, and `stop_reason`, `outcome`, `is_error`,
  `terminal_reason` and the turn boundary are asserted intact on the same row.
- **The reverse direction**: a hostile `modelUsage`, a hostile `permission_denials` and a hostile
  `is_error` on a line whose four numbers are good — the numbers survive. This is the four-way
  isolation claim, and it is the assertion a single widened target would fail.
- **Capture pin, `stdout_events` shape**: `bypass_reescalation_v2.1.239_control_bypass.json`,
  whose three `result` lines carry the whole per-turn/running-total discrimination in one file
  (`duration_ms` falling, `duration_api_ms` and `total_cost_usd` rising, `num_turns` constant).
  Plus `permission_mode_switch_v2.1.239_plan.json`'s `error_max_turns` line for a `num_turns`
  that is not 2.
- **Capture pin, `frames` shape**: `compaction_v2.1.259.json`, the only capture carrying the
  observed `duration_api_ms: 0` / `num_turns: 0` beside a non-zero `duration_ms` and cost. Its
  lines sit under `frames` with each line's JSON inside a **string** payload, so it needs its own
  reader — a helper reading only `stdout_events` would not see the zeros at all, which is exactly
  the vacuity this pin exists to prevent.
- **Float literals are copied byte-for-byte from the capture.** `0.037524600000000005` is the
  shortest round-trip spelling of that `float64`; writing `0.0375246` in Go source parses to a
  *different* value and the assertion would fail for a reason that has nothing to do with the code.
- `TestStreamLine_StaysSegmentationOnly` is left unchanged and must stay green.

`internal/turnbridge/outbound_test.go`: the four map through onto the payload, with four distinct
values so a crossed assignment reddens.

`internal/protocol/`: `turn_end.json` gains the four keys — values chosen so
`duration_api_ms > duration_ms`, which is the reading the ticket exists to foreclose, and a
fixture where they agreed would pin nothing. `TestTurnEndPayload_RoundTrip` asserts all four. A
second test decodes the pre-#2260 frame and checks the four read zero while the existing seven
fields survive, which is the "optional means no `omitempty`, not a pointer" contract.

## Documentation

`docs/protocol-mobile.md` § `turn_end` gains four table rows, a paragraph stating per field which
two are per turn and which two are running totals, that `duration_api_ms` is routinely the larger
of the two durations and must not be read as this turn's API time, and that a zero is a number
`claude` sent rather than a field the daemon failed to read. It also carries the provenance
paragraph above — `claude`'s numbers, unverified, the cost an estimate and not a billing
statement, nothing in the daemon keyed on any of them — and says explicitly that these four do
**not** take `outcome`'s sanitization rule, because a reader who assumes they do will look for a
bound that is not there. The § Application message types row and a changelog entry follow the
section's convention.

## Open questions

1. Does `num_turns` reaching a client as a *per-turn* count survive a future `claude` that makes
   it cumulative? Nothing detects the change, and the daemon publishes what it reads either way.
   Resolved by documenting the reading as measured-at-a-date rather than as a `claude` guarantee.
2. Whether the four numbers belong on one target or four. Settled above in favour of one, on the
   family's fail-closed precedent; recorded here because the Technical Notes leave it to the
   builder and the verifier should see the choice was made rather than defaulted.

## Security review

**Verdict:** PASS (first pass FAILED on one MUST FIX, now addressed in § Provenance and § Documentation)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed.** The first draft named the boundary correctly —
  `decodeTurnTotals` is the single explicit crossing for all four values, and every downstream
  hop (`turnevent.TurnEnd` → `MapEvent` → `protocol.TurnEndPayload`) is a field-by-field
  carry — but it stated **no provenance obligation**. `error_category` set this frame's
  precedent that provenance is declared per field, and `cost_usd_total` is the strongest case
  on the frame for it: a spend figure the daemon never verified, which a client will render as
  the operator's actual cost. Published without attribution that is model-authored data
  presented as daemon chrome, `question_dismissed`'s trap. The plan now carries § Provenance,
  requires the attribution in all four field docs and in the wire section, and states that
  nothing in the daemon acts on any of the four — naming **budget/spend enforcement keyed on
  `cost_usd_total`** as the actuator a future reader will reach for first.
- **[Threat model alignment] No findings, and one borrowed sentence rejected.** § Security
  model's threat 1 lands outward, as it does for `outcome`, `terminal_reason` and
  `error_category` — but *only its misattribution half*. Carrying those fields' SECURITY
  paragraph onto these four would assert a sanitization obligation and a 256-byte bound that
  neither exist nor are needed, since a JSON number cannot hold a control character, a terminal
  escape, markup or a URL. The plan says so explicitly rather than leaving a reader to discover
  the borrowed sentence does not fit.
- **[Network & I/O] No findings — the growth is bounded by the types, and the arithmetic is
  stated rather than assumed.** `turn_end` is a never-droppable control frame, and a frame past
  the 65519-byte application-envelope cap is *lost*, not truncated. The worst case here is three
  `int`s at ≤20 bytes each, one `float64` at ≤24 (17 significant digits, sign, point, exponent),
  plus ~60 bytes of key names: under 150 bytes against that cap, and independent of anything
  `claude` sends, because the bound is over the type's range rather than over input length. That
  is `ResetsAt`'s argument and it is why no cap is applied. No socket read, no header, no
  timeout and no connection accounting is touched.
- **[Error messages, logs, telemetry] No findings.** `decodeTurnTotals` logs nothing on any
  path and **discards** the decode error rather than wrapping it, on `decodeStopShape`'s stated
  reason: `encoding/json` quotes the offending input bytes into its error text, so `"err", err`
  would route `claude`'s own bytes into the daemon log through a channel no per-attribute check
  can see. No `slog` attribute is added anywhere on this path. The values do reach the
  operator's own on-disk history log as part of the `turn_end` payload, which is unchanged
  behaviour for this frame and stays on the operator's own machine.
- **[Concurrency] No findings, and the reason is structural rather than careful.** Unlike #2224's
  `error_category`, these four are decoded and published **within one line**, so there is no
  parser state to latch, no residual to fail closed at a turn boundary, and no cross-line
  staleness window of the kind § `turn_end` documents for that field. `decodeTurnTotals` is a
  pure function of the bytes with no receiver: no goroutine, no lock, no shared mutable state.
- **[Subprocess / external command execution] Not applicable, by direction.** These values flow
  *out of* the `claude` child and back toward no argument, environment variable, file path or
  control-socket reply — stated in § Provenance rather than left to be inferred, since the
  category's real risk here would be a later reader feeding the cost into a spawn decision.
- **[File operations] Not applicable.** No path in this design is built from any value `claude`
  or a client supplies. The only file reads are the tests' own, on `filepath.Join` of package
  constants naming committed captures.
- **[Tokens, secrets, credentials] Not applicable, with one disclosure noted and settled.** No
  token, credential or key material is read, stored, compared or minted. `cost_usd_total` is
  account-adjacent data newly crossing to every attached client; that is not a widening, because
  every attached client is a paired device and **authorization is pairing** — the same reading
  `attachment_offered` states for its own outward disclosure.
- **[Cryptographic primitives] Not applicable.** No randomness, no hashing, no key derivation,
  no comparison against a secret, so no `crypto/rand`-versus-`math/rand` or constant-time
  question arises.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09
