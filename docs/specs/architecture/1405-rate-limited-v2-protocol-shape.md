# #1405 — Give the usage-limit event its v2 protocol shape

**Size:** S (PO's `size:s` confirmed, not overridden). Two production files
(`internal/protocol/codes.go`, `internal/protocol/interactive.go`), two new
exported symbols, zero consumer call sites. Purely additive — the four
registration edits are single-row table appends, not a cascade. #1393 shipped
this same insertion set for **three** payloads in 550 insertions across 9 files
(`fe03b66`); this is one payload against the same set plus a doc section.

**Security-sensitive:** yes — see § Security review at the end. Verdict PASS.

---

## Files to read first

Load these before writing anything. Everything below assumes them.

- `internal/turnevent/event.go:358-450` — `RateLimited`, **the source of truth for
  this payload**. Read all four field doc comments, not the struct alone: they
  carry the decisions this wire shape inherits (unmeasured status set, `ResetsAt`
  unvalidated in both directions, `TruncatedFields` nil-never-`[]`).
- `internal/protocol/interactive.go:149-185` — `BackgroundTaskStartedPayload`. The
  model for `TruncatedFields` and for the "this struct re-decides no maximum"
  rule. Extract the doc-comment structure; you will write the same shape.
- `internal/protocol/interactive.go:274-280` — `BackgroundTaskRosterPayload.MarshalJSON`,
  the file's only custom marshaller. Read the comment above it (`:255-273`) for
  why `truncated_fields` is deliberately **not** normalised. **You are not adding
  one of these.** See § The nil-slice trap.
- `internal/protocol/interactive.go:282-311` — `ThinkingProgressPayload`. The closer
  model for *scoping*: conversation-scoped, no `turn_id`, opens and closes no
  turn, bridge supplies `ConversationID` (`:291`), `session_id` deliberately
  absent (`:292`).
- `internal/protocol/codes.go:255-287` — the background-task const block and the
  `TypeThinkingProgress` block. Extract: block-per-cluster convention, the
  trailing direction comment, the `MUST NOT be added to inboundAppTypeSet`
  paragraph, and #1393's closing "nothing emits these yet" paragraph.
- `internal/streamsup/parser.go:1252-1269` — `emitRateLimit`'s construction. The two
  `bound()` calls name the truncation report's members `"status"` and
  `"limit_type"`, in that order. **This forces two of your wire names** — see
  § Wire names are forced, not chosen.
- `internal/streamsup/parser.go:211-256` — `maxRateLimitField = 256`. The producer's
  cap; the input to the envelope-cap arithmetic in § Testing strategy.
- `internal/protocol/interactive_test.go:16-30` — `roundTripEnvelope`. The helper both
  new tests end with.
- `internal/protocol/interactive_test.go:306-348` — `TestBackgroundTaskStartedPayload_RoundTrip`.
  The shape of test 1.
- `internal/protocol/interactive_test.go:443-472` — `TestBackgroundTaskRosterPayload_Empty_RoundTrip`.
  The shape of test 2, including the `bytes.Contains(canonical(t, raw), …)` guard
  on the fixture itself.
- `internal/protocol/interactive_test.go:545-570` — `TestThinkingProgressType_IsNotClaudesSubtype`.
  The shape of test 3; note it is a substring check, not a literal restatement.
- `internal/protocol/envelope_test.go:20` — `readFixture`, in a different file from
  the tests that call it.
- `internal/protocol/testdata/background_task_started.json` — one line, with the
  shell metacharacters carried as `\u003c` / `\u0026`, never raw. Your populated
  fixture carries escapes too.
- `internal/protocol/compat_test.go:59-64`, `:158-204`, `:255-265` — the **three**
  sites to append to. And `:127-135` — the list you must **not** touch.
- `cmd/pyry/relay_guard_test.go:135-147` — the `excludedTypes` tail, and `:197-208`
  for Assertion #3, the gate that makes the entry mandatory.
- `docs/protocol-mobile.md:444-447`, `:522`, `:640-642`, `:693-733`, `:839-911`, `:912-914`, `:1569-1572` —
  the frame table rows, the count literal, the ignored-tier bullet, the two model
  `####` sections, the second count literal, and the changelog head.
- `docs/knowledge/codebase/1404.md` — the blocker, merged as PR #1408. § Implementation
  for the gate's three rungs; § Lessons learned for why a spec's own cite can be
  stale on the commit that lands it.
- `docs/knowledge/codebase/1393.md` — § Lessons learned, the hand-authored-escape
  lesson and the "a fixture pins the type's marshalling, never the producer's
  construction" lesson. Both are load-bearing here, the second one **inverted**.

## Context

`#1404` landed `turnevent.RateLimited` (PR #1408, merged). The daemon can now
represent claude's usage-limit report internally, but the variant is internal: no
wire type, no payload struct, so nothing can carry it to a phone.

This slice declares the v2 wire shape and nothing else. **Nothing emits this frame
when this ticket lands** — `internal/turnbridge`'s `MapEvent` has no
`turnevent.RateLimited` case and this ticket does not add one. That is deliberate
and matches `#1393`, which shipped the three background-task shapes one ticket
ahead of `#1394`'s mapping. `#1406` does the mapping and the live e2e.

The payload's field set comes from `turnevent.RateLimited`, **not** from claude's
captured `rate_limit_event` JSON. `#1404` already decided which of claude's keys
survive translation and excluded more than a naive reading would: `session_id`
and `uuid` (the family's #1380 precedent) and all four `overage*` keys, two of
which are *measured version-variable* across the three captures. Re-deriving the
shape from one capture would re-open a decision taken against three.

### Out of scope, explicitly

- **`internal/turnbridge`** — the `MapEvent` case is #1406. Do not open
  `outbound.go`.
- **`cmd/pyry/interactive_turn_v2_test.go`** — #1404's code review flagged the
  untested `eventKind` arm at `interactive_turn_v2.go:501-509` as a SHOULD FIX and
  named this ticket. It belongs in **#1406**: both sibling tests
  (`…_BackgroundTasksEventKindNamesTheVariant`, `…_ThinkingProgressEventKindNamesTheVariant`)
  shipped in the *emit* ticket (`9ca9b77` for #1394, `43f5131` for #1386), not in
  the shape ticket #1393. Do not open that file.
- **#1404's Open Question 1**, the live "healthy turn emits zero `RateLimited`"
  sentinel. It reads wire envelopes, so it is vacuous until something emits the
  frame; #1406's AC2 already asks for it end-to-end.
- **`docs/knowledge/codebase/1405.md`** — the documentation phase writes it from
  this spec plus the merged diff. Not a developer deliverable.
- **`docs/knowledge/features/protocol-package.md`** — same; the documentation
  phase owns it.

## Design

### The wire type

One new constant in `internal/protocol/codes.go`:

```go
const (
	TypeRateLimited = "rate_limited" // binary → phone, outbound v2 usage-limit report
)
```

Placement: a **new single-member const block** immediately after the
`TypeThinkingProgress` block (after `codes.go:287`), before the screen-snapshot
pair. Not folded into an existing block — every block's doc comment hand-counts
its members ("these six", "these two"), a new block costs nothing, and an edit
risks miscounting. This is #1393's stated reason and it still holds.

The doc comment above it must cover, in the shape the two blocks above it use:

1. What the frame is and why it exists (claude's usage-limit window is in a state
   other than the one measured-benign one; before #1404 that information existed
   nowhere in pyrycode).
2. Why grouped alone: it is neither a turn sub-state, nor turn-independent work,
   nor a periodic reading. It is a condition report about a window that is
   orthogonal to any turn.
3. **Why the name is the daemon's, not claude's.** claude's line type is
   `rate_limit_event`; the wire follows the *variant* (`turnevent.RateLimited`),
   so a claude rename lands in one place instead of breaking every client at
   once. State that `rate_limited` is already the daemon's own name for this
   variant on its logging surface (`cmd/pyry/interactive_turn_v2.go:509` returns
   exactly this string from `eventKind`) — the wire agrees with the daemon rather
   than inventing a third vocabulary word. `internal/protocol` cannot import
   `cmd/pyry`, so that agreement is prose here, not a test.
4. The standing `MUST NOT be added to inboundAppTypeSet in
   internal/protocol/envelope.go` paragraph, verbatim in shape: this is an
   outbound binary → phone event an old phone never receives, and the drift
   detector in `compat_test.go` puts it in `v2OnlyTypes`.
5. #1393's closing note, adapted: this ticket is wire vocabulary only;
   `internal/turnbridge`'s `MapEvent` has no case for the variant, so nothing
   emits this frame yet — the mapping is **#1406**. Unlike #1393, the
   `docs/protocol-mobile.md` section **does** land here (see § The doc's
   provenance).

### The payload

One new struct at the **end of** `internal/protocol/interactive.go` (after
`BackgroundTask`; appending is cleaner than splitting the roster payload from its
row type any further than #1386 already did):

```go
type RateLimitedPayload struct {
	ConversationID  string   `json:"conversation_id"`
	Status          string   `json:"status"`
	LimitType       string   `json:"limit_type"`
	ResetsAt        int64    `json:"resets_at"`
	TruncatedFields []string `json:"truncated_fields"`
}
```

Five fields, no `omitempty` on any of them (the file's standing doctrine, and a
protocol rule for this stream — `docs/protocol-mobile.md:522` states it), **no
`MarshalJSON`**.

Field order: `conversation_id` first, per every payload in the family. Then the
three content fields in `turnevent.RateLimited`'s own declaration order. Then
`TruncatedFields` last, per every sibling that carries one.

Type choices, all inherited rather than re-decided:

- `ResetsAt int64`, not `time.Time`. Converting would invent a claim the bytes do
  not make, and would drag in the project's `time.Time` round-trip discipline for
  a number that is only ever a number on a wire. `turnevent.RateLimited.ResetsAt`
  states this at length; do not restate the argument, cite it.
- `Status` and `LimitType` plain `string`, not closed enums. The value set beyond
  the one benign status is **unmeasured**; a closed enum would be an invention.
- `TruncatedFields []string`, nil-not-empty. See below.

The doc comment must follow `BackgroundTaskStartedPayload`'s structure and cover:
the type constant + doc section it belongs to + `#1405`; binary → phone; the wire
form of `turnevent.RateLimited`; conversation-scoped, no `turn_id`, opens and
closes no turn (`turnevent.RateLimited`'s doc says a usage-limit window is
orthogonal to whichever turn observed it); the bridge supplies `ConversationID`
because the internal event carries none; `session_id` and `uuid` deliberately
absent for `BackgroundTaskStartedPayload`'s reason plus #1380's; `TruncatedFields`
names the cut fields **using these wire names** and is null when nothing was cut;
the producer's cap is `internal/streamsup/parser.go`'s `maxRateLimitField`, so
**this struct re-decides no maximum** — a second cap here would be a second place
the limit is decided and the two could disagree silently.

Plus a `SECURITY:` paragraph — see § Security review, finding [Trust boundaries].

### Wire names are forced, not chosen

`status` and `limit_type` are **not preferences**. `internal/streamsup/parser.go:1258-1259`
constructs the truncation report with the literal names `"status"` and
`"limit_type"`, in that order. `TruncatedFields` names *wire* fields — that is the
contract `BackgroundTaskStartedPayload`'s doc states ("using these wire names").
Any other JSON tag on those two fields would make the truncation report name
fields that do not exist on the wire. Deterministic, not stylistic.

`resets_at` is free — an `int64` cannot be cut, so it never appears in the report.
It is the snake_case of the daemon's own Go field name, chosen in #1404.

On AC1's "no claude key name appears on the wire": the test is whether a wire name
tracks *claude's key* or *the daemon's field*. claude's keys are `status`,
`rateLimitType` and `resetsAt`, nested under `rate_limit_info`. Ours are the
daemon's: `limit_type` is the translated form (`RateLimitType` inside a type
called `RateLimited` stutters), and `resets_at` is `turnevent.RateLimited.ResetsAt`
in snake_case — if claude renamed `resetsAt` tomorrow, our wire name would not
move. `status` coincides with claude's key spelling, but it is the daemon's chosen
name for the field (it is what `bound()` reports it as) and it is a generic
English word, not a vocabulary import. Say this in the payload doc so a reviewer
does not have to re-derive it.

### The nil-slice trap

**Do not give `RateLimitedPayload` a `MarshalJSON` nil-slice guard.**

The guard at `interactive.go:274-280` is real but it exists for
`BackgroundTaskRosterPayload.Tasks`, a *different* field where an empty roster is
a meaningful positive statement ("nothing is alive") and so must serialise as
`[]`. `truncated_fields` is the opposite: it is meant to be `null` when nothing
was cut. That is what `BackgroundTaskStartedPayload` does, what the shipped golden
`internal/protocol/testdata/background_task_roster.json` pins
(`"truncated_fields":null`), and what `turnevent.RateLimited.TruncatedFields`'s
own doc asks for ("nil when nothing was cut, never an empty non-nil slice, so a
consumer can emit it as absent rather than `[]`"). A guard here would diverge from
every sibling, and because the zero-value fixture pins the bytes it would lock the
divergence in.

**#1393's fixture lesson inverts here, and that is why no extra
construct-and-marshal test is owed.** There, the empty-roster fixture could not
reach the nil path: unmarshalling `"tasks":[]` yields a non-nil empty slice, so a
separate `TestBackgroundTaskRosterPayload_NilTasksNormalises` was needed. Here the
fixture reaches exactly the path that matters — unmarshalling
`"truncated_fields":null` yields **nil**, marshalling nil yields **`null`**, and a
`MarshalJSON` normalising nil→`[]` would make the round-trip bytes differ and go
red. The zero-value fixture *is* the enforcement mechanism for this decision.

### Registration — four sites, and one list to leave alone

The type must be classified everywhere `make check` enumerates `Type*` constants,
or the build gate fails on a type it cannot classify.

`internal/protocol/compat_test.go`, **three** appends:

1. `TestIsKnownAppType`'s `cases` table, after the `thinking_progress` row
   (`:64`): `{"rate_limited-rejected", TypeRateLimited, false, ErrUnknownType}`,
   with a one-line comment in the siblings' voice. This is AC3 — and it is a
   security control, not a compatibility nicety; see § Security review.
2. `v2OnlyTypes`, after `TypeThinkingProgress: true` (`:203`), under a
   `// v2 usage-limit report.` comment.
3. `TestTypeConstants_V1V2Partition`'s `all` list, after `TypeThinkingProgress`
   (`:264`), same comment. No count literal to update — the test derives its
   expected size from `len(all)`.

**Do NOT add it to `TestInboundAppTypeSet_CoversAllExportedTypeConstants`'s `all`
list (`:128-134`).** That list is v1 application types only and carries a hard
`len(all) == 23` assertion; a developer pattern-matching "append the constant to
every `all` list" turns that test red for the wrong reason. Named here because it
is the one trap in an otherwise mechanical set.

`cmd/pyry/relay_guard_test.go`, one append (AC2): `"TypeRateLimited": "push"` in
`excludedTypes`, after the `TypeThinkingProgress` entry (`:145`), with a comment
in the two preceding entries' voice — outbound-only, nothing emits it yet (#1406),
and Assertion #3 (`:197-208`) enumerates every `Type*` constant in `codes.go` and
fails any name in neither `inboundTypes` nor `excludedTypes`, so the entry is
mandatory **from the moment the constant exists**, not from the moment something
emits it.

### Concurrency model

None, and that is the design. `internal/protocol` is a stdlib-only leaf: pure data
types, no goroutines, no channels, no shared mutable state, no I/O. This ticket
adds a struct with value semantics and no methods — notably **no `MarshalJSON`**,
so unlike `BackgroundTaskRosterPayload` there is not even a value-receiver-copy
question to reason about. Nothing here needs a shutdown sequence because nothing
here starts.

### Error handling

Also none, in the runtime sense, and worth stating so the absence reads as a
decision. This package neither validates nor rejects: `IsKnownAppType` is the only
predicate in the package and it is a *type* check that this constant must fail
(AC3). The payload performs no validation of `Status`, `LimitType` or `ResetsAt`,
by design:

- The strings are bounded **at construction** by the producer
  (`maxRateLimitField`), so an oversized value never reaches this type. A second
  cap here would be a second place the limit is decided.
- `ResetsAt` is unvalidated **in both directions**, deliberately. Negative, zero,
  and year-40000 values are all representable and none is rejected, because
  rejecting one would be a validation rule with no captured negative case behind
  it (`turnevent.RateLimited.ResetsAt`'s doc states this). The mitigation is a
  documented consumer hazard, not a daemon-side check — see § Security review.

The only failure modes this ticket can have are encoding ones, and every one of
them is caught by the two round-trip tests: a missing or misspelled JSON tag, an
`omitempty` slipped onto a field, a `MarshalJSON` added, a field reordered.

## Testing strategy

Two fixtures in `internal/protocol/testdata/`, three tests appended to
`internal/protocol/interactive_test.go`. Scenarios, not code — write them in the
file's existing idiom.

### Fixture 1 — `rate_limited.json` (populated)

One line, envelope-wrapped like every sibling. `conversation_id: "c1"`,
`limit_type: "five_hour"` (the value in all three captures), a plausible unix
second for `resets_at`, and `truncated_fields: ["status","limit_type"]` — both
members, in the producer's order, so the fixture pins the report's vocabulary
*and* the order `parser.go:1256-1259` promises.

`status` must be a value **no reader can mistake for a measurement**. No capture
of a non-benign status exists — every capture reads `"allowed"`, which is the one
value the gate silences, so any realistic-looking alternative is an invention that
a client author could copy as if it were real. Use the string `<unmeasured>`,
which lands in the fixture in its escaped form: each angle bracket becomes the
six-byte `\u003c` / `\u003e` sequence, exactly as `background_task_started.json`
already carries them.
Two benefits beyond the honesty: it
also proves a claude-authored string survives `encoding/json`'s HTML escaping
byte-exactly (the property `background_task_started.json` already exercises), and
it makes the "the value set is unmeasured" statement visible at the fixture rather
than only in prose.

Per #1393's lesson: **do not hand-type the escape sequences.** Write the file with
the raw characters and run a small replace pass over it; `json.Compact` (what
`canonical()` uses) does not re-escape, so a fixture typed with raw `<` fails the
round trip.

### Fixture 2 — `rate_limited_zero.json` (the zero value)

The payload's zero value in **every** field:
`{"conversation_id":"","status":"","limit_type":"","resets_at":0,"truncated_fields":null}`,
envelope-wrapped.

This frame is not one the bridge will ever emit — `Status` is never empty (the
producer's gate does not emit on an empty status) and the bridge always supplies a
conversation id. Say so in the test's doc comment. The fixture exists to pin the
*encoding* of every field's zero value, which is exactly what the stream's
no-omitempty rule is: adding `omitempty` to any one of the five fields turns this
round trip red, and no realistic fixture can make that claim for all five.

### Test 1 — `TestRateLimitedPayload_RoundTrip`

Modelled on `TestBackgroundTaskStartedPayload_RoundTrip` (`:306`).

- Read `rate_limited.json`, unmarshal the envelope, assert `env.Type == TypeRateLimited`.
- Unmarshal the payload; assert each of the five fields against its fixture value.
  `status` and `limit_type` must carry **different** values (they do) so a struct
  wiring both wire keys to one field cannot pass — the reason
  `TestThinkingProgressPayload_RoundTrip` gives for its two distinct integers.
- Assert `TruncatedFields` joined is `"status,limit_type"` — the joined form, so a
  swapped order goes red rather than a set comparison that would not.
- End with `roundTripEnvelope`.

### Test 2 — `TestRateLimitedPayload_ZeroValue_RoundTrip`

Modelled on `TestBackgroundTaskRosterPayload_Empty_RoundTrip` (`:443`), whose
`bytes.Contains(canonical(t, raw), []byte("\"tasks\":[]"))` guard is the shape to
mirror — inverted.

- Two guards on the fixture bytes before anything else: it must carry
  `"truncated_fields":null` and `"resets_at":0`. These are load-bearing, not
  decoration: if the fixture were edited to `"truncated_fields":[]` the round trip
  alone would still pass (unmarshalling `[]` yields a non-nil empty slice that
  marshals back to `[]`), so the round trip pins the *type's* behaviour only once
  the fixture is pinned to `null`. Together they are what makes "no `MarshalJSON`
  guard" an invariant `make check` enforces rather than a convention #1406 could
  silently violate.
- Assert `TruncatedFields == nil` explicitly, not `len(…) == 0` — `len` is 0 for
  both nil and `[]`.
- Assert the other four fields are their zero values.
- End with `roundTripEnvelope`.

### Test 3 — `TestRateLimitedType_IsNotClaudesVocabulary`

Modelled on `TestThinkingProgressType_IsNotClaudesSubtype` (`:545`), which is a
substring check rather than a restatement of the constant. AC1's second sentence
is the assertable claim here.

- `TypeRateLimited != "rate_limit_event"` — claude's line type.
- `!strings.Contains(TypeRateLimited, "event")` — the discriminating word. **Note
  the sibling's exact form does not transfer**: `strings.Contains("rate_limited",
  "rate_limit")` is *true*, so a check on `"rate_limit"` would be red against the
  correct name. `event` is what separates claude's vocabulary from the daemon's
  here.
- `TypeRateLimited == "rate_limited"` — the exact pin, and the string
  `cmd/pyry/interactive_turn_v2.go:509` already returns.
- Marshal a fully-populated `RateLimitedPayload` and assert the **payload** bytes
  (not the envelope's — the envelope has its own `id`/`ts`) contain none of
  `rateLimitType`, `resetsAt`, `rate_limit_info`, `session_id`, `uuid`. Use a
  `conversation_id` that is not itself uuid-shaped (`"c1"`). These are regression
  pins, cheap and non-discriminating today by construction; their job is to go red
  the day someone "helpfully" adds claude's keys back. Say that in the comment
  rather than dressing them up as discriminating.

### No envelope-cap test is owed — here is the arithmetic

#1393 shipped `TestBackgroundTaskPayloads_FitV2EnvelopeCap` because per-field caps
do not compose into an envelope guarantee and its roster case measured 50 557 B,
77% of the 65519 B v2 application-envelope cap. This payload cannot get close.
Worst case: two strings at `maxRateLimitField` = 256 B each, every byte a `<`/`>`/`&`
costing six bytes escaped, so 512 × 6 = 3 072 B; plus `resets_at` at most 20
characters, `truncated_fields` a **closed two-member set of daemon-authored
names** (`bound()` appends nothing else, so the array cannot grow), a
conversation id, and envelope overhead — call it 3.3 KB, about **5%** of the cap.

A test here would have no failure mode reachable by any producer change short of
raising `maxRateLimitField` by twentyfold. Stating the number is the useful part;
the test would be ceremony. If #1406 ever raises the cap, that ticket owns the
re-measurement.

### Gate

`make check` must be green. It covers all four registration sites (`compat_test.go`
×3, `relay_guard_test.go` Assertion #3) plus the three new tests. No opt-in tier is
touched — this ticket adds no realclaude fixture and no e2e.

## `docs/protocol-mobile.md`

### The doc's provenance — do not "verify against #1393" and delete this work

`#1393` did **not** touch `docs/protocol-mobile.md`. Its own `codes.go` comment
says so ("the mapping and the `docs/protocol-mobile.md` section are #1394"), and
`fe03b66` touches nine files, none of them that doc — verified against the tree.
This ticket deliberately diverges, because `#1406`'s Technical Notes disclaim the
file ("this ticket consumes them and should not edit them"). **If the doc does not
land here it lands nowhere.**

### Five edits, one non-edit, and one clause beyond the AC's letter

1. **Frame table** — new row immediately after the `thinking_progress` row
   (`:447`), before `request_snapshot`. Follow the siblings' column shape exactly,
   including `**New in v2** (interactive, capability-gated)` — the gate is not
   optional prose, it is what every row in this stream carries, and `:522` sits
   under `### Interactive events (v2, capability-gated)`. Say the frame is
   declared by #1405 and emitted from #1406, and that nothing produces it today.
2. **`:522`** — "These **thirteen** envelope types" → **fourteen**. I counted the
   `####` sections between `### Interactive events` and `#### session_transition`
   rather than trusting the literal: turn_state, assistant_delta, tool_use,
   tool_result, turn_end, stall, api_retry, compacting, unrecognized_message,
   background_task_started, background_task_updated, background_task_roster,
   thinking_progress = 13. This frame is the fourteenth.
3. **Its own `#### \`rate_limited\`` section**, inserted after the
   `thinking_progress` section ends and **before** `#### \`session_transition\``
   (currently `:912`), so that section's "the thirteen turn-stream events above"
   stays an "above". Content below.
4. **The second count literal** in the `session_transition` section (currently
   `:914`) — "thirteen" → **fourteen**. Locate it by the section heading, not by
   line number: edit 3 shifts it. Both `#1394` and `#1386` had to correct exactly
   these two literals and both recorded doing so; it has gone stale twice running,
   so this is an observed failure, not a hypothetical.
5. **Changelog** — a new first bullet under `## Changelog` (`:1569`), dated
   `2026-08-09`, in the two 2026-08-09 entries' voice. It must state that the
   shape is declared ahead of its producer (nothing emits it until #1406) and, as
   both predecessors did, that **two stale counts were corrected** from thirteen
   to fourteen.

**Non-edit, stated so nobody helpfully fixes it:** `:651`'s "the daemon parser
maps **four** `system` subtypes … all four are documented below" stays **four**.
That count is scoped to `system` *subtypes*, and `rate_limit_event` is a
**top-level line type**, not a `system` subtype — which is the whole point of
#1404 (it is the family's fifth mapping and the first that is not a `system`
subtype). Touching it would make a true sentence false.

**One clause beyond the AC's letter, with its reason.** `:640-642` reads
"**Known and deliberately ignored, from this frame's perspective** — `system` and
`rate_limit_event` never produce `unrecognized_message`." That claim is still
**true** post-#1404 (`consumeLine` matches `case "rate_limit_event"` before
`default:`, and `emitUnrecognized` is only reachable from `default`), so it must
not be rewritten. But this ticket documents a `rate_limited` frame two sections
later while that bullet still files its source line under "deliberately ignored".
Add **one clause** noting that as of #1404 the daemon maps the line to
`turnevent.RateLimited` and it reaches this wire as `rate_limited`, documented
below — exactly the treatment the `system` half of the same bullet already got
from #1394/#1386. One sentence, not a rewrite; the "never produce
`unrecognized_message`" claim stays verbatim.

### What the `#### \`rate_limited\`` section says

Model it on `#### \`background_task_started\`` (`:693`) — field table, then the
standing "like every frame in this section" sentence, then the prose. Required
content:

- **Field table**, five rows, wire names and types. `resets_at` must say `0` means
  claude did not report it, **not** the epoch. `truncated_fields` must say
  "array of string | null", name its two possible members using the table's own
  field names, and say `null` when nothing was cut.
- **Not emitted yet.** The shape is declared by #1405 so a client can be written
  against it; #1406 wires the producer. Do not let the section imply live traffic.
- **Why the frame exists**: claude reports the usage-limit window **once per run
  whatever its state**, so a 1:1 translation would put a row on every healthy
  turn. The daemon gates it: the one measured-benign status is silent, and any
  other non-empty status emits. State the direction of that choice — an
  unrecognised status surfaces and a human looks, rather than a real limit
  vanishing.
- **Why `status` is on the wire and what a client may do with it.** Its value set
  beyond the benign one is **unmeasured** — no capture of a limit actually in
  force exists. It is claude's raw string, carried so the set gets measured the
  first time a real limit fires. A client **MUST NOT branch security-relevant
  behaviour on it**, and should render it as an opaque label; it is an open
  string, not a closed set, and treating it as one is a bug waiting for claude's
  next release.
- **The consumer hazard on `resets_at`**, stated plainly because a client gets it
  wrong by default: it is **claude's number, not the daemon's clock**, and it is
  unvalidated in **both** directions. A consumer must not assume it lies in the
  future, and must not assume it lies in a sane range at all. Formatting it as a
  date without a range check is the realistic bug.
- **Conversation-scoped**: no `turn_id`, opens and closes no turn. A usage-limit
  window is orthogonal to whichever turn observed it.
- **`truncated_fields` is load-bearing, not decoration** — a client that ignores
  it presents claude's cut text as complete. Both strings were bounded by the
  daemon at construction, so an oversized value never reaches this wire; the
  report is how a client knows which of them lost characters.
- **SECURITY paragraph**, in the siblings' voice: `status` and `limit_type` are
  claude-authored strings that crossed the subprocess trust boundary. Safe to
  **render as inert text**; never feed to an HTML sink, an attribute, or a URL.
  The daemon bounds them but does not sanitize them — they stay untrusted,
  model-influenced text all the way to the client. And the constraint that follows
  the data onto the wire: this frame is a **report, never a control input**.

## Open questions

1. **What non-benign `status` values exist?** Unmeasured — no capture of a limit
   actually in force is on record, so the wire carries claude's raw string
   precisely so the set gets measured the first time a real limit fires. #1406's
   live e2e is the first opportunity; if it observes a real limit, the value
   belongs in that ticket's notes. Nothing here should pretend to know the set.
2. **Does a client want a "limit lifted" edge?** No such event exists — claude
   reports the window's state once per run and the daemon has never observed a
   clearing transition. The family's standing rule (#1394: "no terminal event
   exists, deliberately — that transition has never been observed") applies. Not a
   gap to fill here; noted so it is not read as an oversight.
3. **Will #1406 need the envelope-cap measurement?** Only if it raises
   `maxRateLimitField`. At 256 B the worst case is ~5% of the cap (arithmetic
   above). Flagged for #1406, not deferred work for this ticket.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX; one required spec element, already in the
  design. Two claude-authored strings (`Status`, `LimitType`) cross
  subprocess → daemon at `internal/streamsup/parser.go:1258-1259`, bounded there at
  256 B each, and this ticket declares their second hop, daemon → phone. The
  boundary is explicit and single: `emitRateLimit` is the only constructor and
  `internal/protocol` is a pure data leaf that adds no parsing. The downstream
  signal is a `SECURITY:` doc comment on the payload plus the doc section's
  SECURITY paragraph — both **required** by this spec, matching
  `UnrecognizedMessagePayload.Raw` and `BackgroundTaskStartedPayload.Description`.
  Actively required *not* to happen: no validation, no sanitisation, and no second
  cap in `internal/protocol`. A second cap would be a second place the limit is
  decided and the two could disagree silently (`interactive.go:149-185` states the
  rule). The strings stay untrusted, model-influenced text all the way to the
  client, and the mitigation is render-as-inert-text, stated at both layers.
- **[Trust boundaries — identity leak]** No findings, by inheritance. `session_id`
  (claude's session identity, not the daemon's conversation identity) and `uuid`
  (claude's per-line message id) are absent from `turnevent.RateLimited` itself —
  they never enter the decode target (#1380's precedent), so this payload cannot
  leak them even by accident. So are the four `overage*` keys, two of which are
  measured version-variable. AC1's "neither `session_id` nor `uuid`" is pinned by
  test 3's byte check.
- **[Tokens, secrets, credentials]** Not applicable, by design rather than by
  luck: this frame carries no credential material, no device token, no key, and no
  path. The one identity-adjacent decision (excluding `session_id`/`uuid`) is
  covered above.
- **[File operations]** Not applicable. The only files this ticket creates are two
  test fixtures under `internal/protocol/testdata/`; nothing in `internal/protocol`
  performs runtime file I/O, and no path is constructed from any input.
- **[Subprocess / external command execution]** No new surface — this ticket execs
  nothing. The relevant inherited constraint is the one that follows the data:
  `turnevent.RateLimited` is a **report, never a control input**, and nothing in
  the daemon may key a behaviour on it (no backoff, throttle, retry, turn
  suspension, or reconnect delay). This spec carries that constraint onto the wire
  in the doc section and forbids a client from branching security-relevant
  behaviour on `status`. That is what keeps a wrong — or hostile — status value
  costing at most one misleading row rather than a resource action.
- **[Cryptographic primitives]** Not applicable. No RNG, no key material, no
  comparison against a secret. The frame rides the existing Noise_IK v2 path
  unchanged; declaring a payload type touches no handshake, no CipherState, and no
  nonce.
- **[Network & I/O]** No findings, measured rather than argued. Input size is
  bounded upstream and composes: 2 × `maxRateLimitField` (256 B) at the worst-case
  6-bytes-per-byte JSON escape multiplier = 3 072 B, plus an `int64`, a
  conversation id, and a `truncated_fields` array that is a **closed two-member
  set of daemon-authored names** — `bound()` appends nothing else, so it cannot
  grow — for ~3.3 KB, about 5% of the 65519 B v2 application-envelope cap. No
  fragmentation risk, no unbounded field, no DoS vector via frame size. Timeouts,
  read deadlines and connection caps live in `internal/transport` /
  `internal/relay` and are untouched.
- **[Error messages, logs, telemetry]** No findings, structurally.
  `internal/protocol` contains no `slog` calls at all, so the constraint #1404
  established — `Status` must **never** reach a daemon log, and the drop site's
  reasons are daemon-authored keywords precisely because `Status` is the field a
  log line is most tempted to explain itself with (`parser.go:281-284`) — cannot be
  violated by this ticket without introducing logging into a package that has
  none. Named here so it stays true: the developer must not add a log line
  carrying `Status` or `LimitType`.
- **[Concurrency]** Not applicable, and the absence is a design property rather
  than an omission. Pure value types, no methods, no goroutines, no channels, no
  shared state, no locks. Because this payload deliberately has **no
  `MarshalJSON`**, it does not even inherit `BackgroundTaskRosterPayload`'s
  value-receiver-copy consideration.
- **[Threat model alignment]** Addressed, no new exposure.
  `docs/protocol-mobile.md` § Security model threat #1 (**Prompt injection —
  severity: high, mitigation: partial**) is the relevant one: this frame carries
  model-influenced text to a client, exactly as `unrecognized_message` and the
  background-task frames do, and it inherits their posture — bounded at
  construction, rendered as inert text, no daemon behaviour keyed on it. Severity
  and mitigation are unchanged; this adds one more field pair to an existing
  category rather than opening a new one. No other threat in that section changes.
- **[Threat model — inbound surface, worth naming]** No finding, but the control
  that prevents one is easy to under-read. `rate_limited` must be **outbound
  only**; if it were classified into `inboundAppTypeSet`, a phone could send a
  frame of this type into `dispatch.Route`. The guard is AC3's `TestIsKnownAppType`
  rejection row, which asserts `ErrUnknownType` — that row is a **security
  control**, not a compatibility nicety, and the same misclassification is caught
  a second time by `TestInboundAppTypeSet_CoversAllExportedTypeConstants`'s hard
  `len(all) == 23` assertion. Both are required by this spec; the second is also
  the list the developer is explicitly told not to append to.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
