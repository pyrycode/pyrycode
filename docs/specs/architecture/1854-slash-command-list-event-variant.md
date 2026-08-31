# #1854 — Declare the daemon-internal slash-command list event variant

**Size:** `s` (confirmed; see § Size check)
**Labels:** `enhancement`, `size:s`, `security-sensitive`
**Split from:** #1832

## Files to read first

Read these before writing anything. Every entry names a symbol; resolve it with
`codegraph_search` / `codegraph_node` rather than opening the file at a line.

| File | Symbol | What to extract |
|---|---|---|
| `internal/turnevent/event.go` | `ModelList`, `ModelOption` | The shape and the doc register this slice mirrors: an aggregate variant plus its non-`Event` element type. `ModelOption`'s practice of documenting AT THE TYPE what an absent field reads as. |
| `internal/turnevent/event.go` | `BackgroundTask`, `BackgroundTaskRoster` | `BackgroundTask.TruncatedFields` is the single source of the nil-vs-empty-slice convention every sibling cites. `BackgroundTaskRoster.DroppedTasks` is where the "each dimension reports where it happens" argument was first made. |
| `internal/turnevent/event.go` | the `isTurnEvent` marker block, and the `var _ Event = …` block under it | Where the new marker line goes (value receiver, aligned with its neighbours). Note the assertion block is already short by one — see § Open questions. |
| `internal/protocol/interactive.go` | `SlashCommand` | The mirrored declaration order, the measured per-entry key set, the `__remote-workflow` charset fact, and the SECURITY paragraph the daemon-side doc restates for its own consumers. |
| `internal/protocol/interactive.go` | `SlashCommandListPayload` | The count measurement (51 entries at claude 2.1.239 here, 74 at 2.1.220 elsewhere), the "declared ahead of its producer" sequencing argument, and `DroppedCommands`' "NOTHING COUNTS IT YET" honesty. |
| `internal/protocol/interactive.go` | `SlashCommand.MarshalJSON` | The aliases absent-vs-empty collapse — and the sentence that says the DAEMON-internal alias reading is #1825's call. This slice declares no alias field and settles nothing there. |
| `cmd/pyry/interactive_turn_v2.go` | `eventKind` | The arm to add and the register to write it in. Its `ModelList` and `ModelAnnounced` arms carry the name-alone argument; the `ModelAnnounced` arm also carries the stale variant count this slice must correct. |
| `cmd/pyry/interactive_turn_v2.go` | `Handle`, `emitMapped` | Why `Handle`'s `default` arm is a LIVE `eventKind` site for this variant (no case claims it) and `emitMapped`'s unmapped drop is not (nothing routes the variant there). |
| `cmd/pyry/stream_turn_busy.go` | `turnMarkFor` | The opener WHITELIST and its `default`. It needs no production arm — read the DISCHARGED note to see the same thing happening for `RateLimited`. |
| `cmd/pyry/stream_turn_busy_test.go` | `TestTurnMarkFor_TotalOverEveryVariant`, `turnEventVariants` | The AST-derived totality gate that reddens the moment the marker lands, the row format, and the `ModelList` row (#1811) whose comment is this row's model. |
| `cmd/pyry/interactive_turn_v2_test.go` | `TestInteractiveTurnEmitterV2_ModelListEventKindNamesTheVariant`, `emitterModelListFixture`, `emitterModelListSentinels` | The rig to copy verbatim in structure: the `ReplaceAttr` that drops slog's time attr, the empty cursor, the sentinel-naming rule, and the derive-negatives-from-the-fixture helper. |
| `cmd/pyry/stream_turn_drain.go` | `sinkFor`, `startStreamTurnDrainV2` | The two other reachable drop-logging sites and their content-free SECURITY notes — the reason the kind function must stay name-only. |
| `internal/turnbridge/outbound.go` | `MapEvent` | Read its `default` (returns `("", nil, false)`). Confirm for yourself that an unmapped variant is dropped. **Add no arm here** — that is a later slice's. |
| `docs/knowledge/features/turnevent-package.md` | § "The three sum-type seams", § "Why no errors, and no construction-time validation" | The value-receiver marker rule, the one-assertion-per-variant convention, and the package's standing refusal to validate at construction — which is why neither new type gets a constructor or a `Valid()`. |

## Context

`protocol.SlashCommand` and `protocol.SlashCommandListPayload` landed in #1727 as
the mobile wire shape for claude's slash-command inventory. The daemon carries one
neutral event variant per thing claude reports, so the inventory needs a
daemon-internal peer before any producer can construct into one. This slice
declares that peer and answers the turn-lifecycle question for it. No producer, no
decode, no cap, no emit, no wire mapping.

Declaring ahead of the producer is this family's own sequencing — #1616 ahead of
#1638, #1704 ahead of #1848, #1727 ahead of #1720. The type declaration is where
the field set and the security posture get decided, and deciding those in the same
slice that also writes the decode, the byte caps and the emit is what makes such a
slice oversized.

**This slice is not merely declarative, and the proof is build-enforced.**
`turnevent.Event` is a closed sum sealed by the unexported `isTurnEvent` marker.
`TestTurnMarkFor_TotalOverEveryVariant` derives the variant set by parsing every
declaration of that marker out of the package source with the AST — deliberately
not from a hand-kept list — and asserts the fan-in's totality table covers exactly
that set. The moment the marker method lands, `make check` is red until the table
gains a row. The row asserts the answer `turnMarkFor`'s whitelist default already
gives, so **no production arm changes** — the same shape `ModelList`'s row had in
#1811.

**No ADR.** This is a variant added inside an existing, already-documented sum
type; the decisions it makes (partial field set, no dropped-count field, the
workspace-authored trust note) are type-local and belong in the type's doc
comment, which is where every sibling records the same class of decision.

## Design

### The two new types

`internal/turnevent/event.go`, declared immediately after `ModelList` and before
`Stall`, so the file's ordering — content variants, then the claude-report family,
then the internal-only status peers — holds.

```go
// SlashCommand is one entry of a SlashCommandList: the list's element type, NOT
// an Event, so it carries no marker.
type SlashCommand struct {
	Name            string
	TruncatedFields []string
}

// SlashCommandList is claude's inventory of slash commands for this session in
// this working directory.
type SlashCommandList struct {
	Commands []SlashCommand
}

func (SlashCommandList) isTurnEvent() {}
```

Plus `_ Event = SlashCommandList{}` in the assertion block below the markers, per
the one-per-variant convention `turnevent-package.md` § "The three sum-type seams"
records.

**Names.** `SlashCommandList` mirrors `protocol.SlashCommandListPayload` with the
`Payload` suffix dropped, exactly as `ModelList` mirrors `ModelListPayload`. The
entry keeps the wire type's own name, exactly as `turnevent.ModelOption` keeps
`protocol.ModelOption`'s. There is no existing `SlashCommand` symbol in
`internal/turnevent` to collide with.

**Field set is partial by design, and the order is chosen now.** `Name` first and
`TruncatedFields` last is `protocol.SlashCommand`'s own declaration order, so the
three fields arriving later — argument hint, description, aliases — each land in
their mirrored position rather than being appended. That is `ModelOption`'s growth
path across #1819 / #1827 / #1828, and it is the whole reason to fix the order
before the second field exists.

**No `DroppedCommands` field, and the asymmetry with the wire type is
deliberate.** `protocol.SlashCommandListPayload` declared its count ahead of any
counter because a WIRE with nowhere to put a drop discards it silently, and adding
a key later is a compatibility event. A daemon-internal struct is not a
compatibility surface: adding a field to it is a local change. So the count
arrives with the entry-count bound that produces it, which is how
`ModelList.DroppedModels` arrived with `maxModelListEntries` and
`BackgroundTaskRoster.DroppedTasks` with `maxTaskRosterEntries`. Record that
reasoning in the type's doc — a reader who knows the wire type will otherwise
read the absence as an oversight.

**No constructor, no `Valid()`, no construction-time validation.** The package
returns no errors and validates nothing at construction; `turnevent-package.md`
§ "Why no errors" is the standing rule and `NewPermissionRequest` is its single
AC-mandated exception. Both types are built as struct literals.

### What the doc comments must carry

The doc comments ARE the deliverable here — this is a declaration slice, so the
prose is where the decisions live. Write them in the register of `ModelList` and
`ModelOption`: argue the choice, name what was rejected and why, and state the
hazard a consumer would otherwise walk into.

`SlashCommandList` must state:

- What it is and where it comes from: the `commands` array of claude's
  `initialize` reply, the same exchange `ModelList` carries the `models` array of.
- **Declared ahead of its producer**, naming the family's sequencing. Nothing in
  the tree constructs it; `internal/streamsup` (#1719 / #1720) is what will.
- **Not published by any path.** `turnbridge.MapEvent` has no arm, so its
  `default` drops it. Say explicitly that `ModelList` is no longer the example of
  a variant the default drops — it grew its own arm in #1848 — so the precedent is
  read off `MapEvent` itself.
- **Turn lifecycle:** it opens and closes no turn, and is not even per-turn — one
  `initialize` exchange per child produces one. `turnMarkFor` answers it correctly
  by construction, its opener set being a whitelist and its default `turnMarkNone`.
- No conversation identity — the bridge injects that. claude's `session_id` is
  deliberately not a field, for `BackgroundTaskStarted`'s reason: claude's session
  identity is not the daemon's conversation identity.
- **The count is workspace- and version-dependent** and no consumer may assume
  one; cite `protocol.SlashCommandListPayload`'s measurement rather than
  re-deriving it. This is why the list is per session and per working directory,
  and why a client must not cache one across working directories.
- **SECURITY** — the paragraph this ticket exists to place. Every string this type
  will carry is **workspace-authored** and crossed the subprocess trust boundary: a
  command defined in a repository was written by whoever wrote that repository,
  which is a LOWER-trust origin than claude's own strings. That STRENGTHENS
  `ModelOption`'s claude-authored warning rather than restating it. The strings are
  safe to RENDER as inert text and must never be fed to an HTML sink, an attribute,
  or a URL. The daemon bounds them and does not sanitize them — no control-character
  or terminal-escape stripping happens on this path — so they stay untrusted text
  all the way out, and the render boundary owing the sanitization is the CLIENT's.
- The bound is the PRODUCER's and is not decided here, so this type declares no
  maximum and no charset check — a second cap would be a second place the limit is
  decided and the two could disagree silently. That is
  `protocol.SlashCommand`'s own stated reason, and it holds identically one layer in.
- **It is a REPORT, never a control input**, with `protocol.SlashCommand`'s
  amendment: a client is meant to send a `Name` back, as the text of an ordinary
  message, because sending the slash command IS the feature. Publishing a name does
  not make it trusted. Nothing in the daemon may treat a value from this type as a
  command vocabulary, and no field here may reach a child as an argv element.

`SlashCommandList.Commands` must state:

- claude's own order, unchanged; no ranking is invented, its ordering semantics
  being unobserved.
- `nil` for a zero-length list, never an empty non-nil slice —
  `BackgroundTask.TruncatedFields` is the convention's single source.
- **Whether an empty list is EMITTED at all is the producer's gate and is
  deliberately NOT decided here.** Contrast `ModelList.Models`, which can claim
  "never empty" because #1811 declared the type and wrote the producer in one
  slice. The WIRE's position on an empty list is already declared
  (`protocol.SlashCommandListPayload.MarshalJSON`: `[]` is a positive statement —
  claude offered nothing) and is what a producer slice will read.

`SlashCommand.Name` must state:

- claude's command name, VERBATIM, per `ModelAnnounced.Model`'s rule: no
  lowercasing, no canonicalisation, no prefix stripping.
- **It is NOT an identifier.** One name in the committed capture is
  `__remote-workflow`, so no charset assumption belongs in this struct or in a
  client — `protocol.SlashCommand`'s measured fact, carried to the daemon side
  because this is the type a daemon-side consumer reads.
- Bounded by the producer at construction and never sanitized (point at the type's
  SECURITY paragraph rather than restating it).

`SlashCommand.TruncatedFields` must state:

- It names THIS entry's fields the producer cut to fit their caps, in declaration
  order, using the DAEMON's snake_case names. The names agree with
  `protocol.SlashCommand.TruncatedFields`' wire names, so a later mapping is a
  copy rather than a translation.
- **Today the only name it can carry is `"name"`, and the enumeration GROWS with
  the field set** — each field-adding slice extends it, exactly as
  `ModelOption.TruncatedFields` grew to include `"effort_levels"` in #1827.
- **A name for a field this type does not declare must never appear.** A producer
  that cut a value this type does not carry has nothing to report here, because
  the value is not on the type. That is the one way this partial field set could
  produce a lie, and stating it is how the producer slice avoids it.
- `nil` when nothing was cut, never an empty non-nil slice.

Two anti-instructions, both worth a moment because each looks like tidy-up:

- **Do not extend the `Event` interface's own doc comment**, which lists variants
  and is already stale (it names neither `RateLimited`, `ModelAnnounced`,
  `ModelList` nor `PermissionRequest`). Adding one name to a list that is short by
  four makes it no more true and is not this slice's job.
- **Do not add `_ Event = ModelList{}`** to close the assertion block's existing
  gap. See § Open questions.

### `eventKind` — the drop-logging arm

`cmd/pyry/interactive_turn_v2.go`. One `case turnevent.SlashCommandList:`
returning `"slash_command_list"`, placed after the `ModelList` arm and before
`default`. The returned string matches `protocol.TypeSlashCommandList`'s value,
which is the family's convention (`model_list`, `model_announced`).

**The variant name alone. Never the entry count, never a string from any entry.**
The arm's comment must say why the discipline matters MORE here than in the arms
above rather than less: every string this variant carries is workspace-authored,
so the #833 posture that keeps model values out of a log — restated across
`internal/relay`'s v2 session-settings surface and `internal/sessions`' pool as
"model / effort / YOLO values are NEVER logged at any level" — covers these a
fortiori.

**Name the live call sites accurately, and derive them from the MARK rather than
from control flow.** For a variant whose `turnMarkFor` answer is `turnMarkNone`
and which no `Handle` case claims, the reachable sites are:

- `Handle`'s no-cursor drop (`interactive_turn.no_cursor`), which returns before
  the type switch;
- `Handle`'s `default` arm (`interactive_turn.unknown`) — **live for this
  variant**, which is the opposite of what the `ModelAnnounced` and `ModelList`
  arms say about themselves, because their `Handle` cases claim them and this
  variant has none;
- `sinkFor`'s sink-full droppable drop and `startStreamTurnDrainV2`'s
  not-active-session drop, both in `cmd/pyry/stream_turn_drain.go`.

Not reachable for this variant: `observe`'s unbound-session drop and `sinkFor`'s
close drop, both gated on a non-`None` mark; and `emitMapped`'s unmapped drop,
which nothing routes this variant to.

**Do NOT copy the existing arms' file list.** They name `acp_turn_stream.go`,
which was deleted with the terminal-driving path in #1348.

The arm's honest framing: no production producer emits this variant yet, so no
production path reaches any of those sites today. The arm lands with the
declaration anyway, because the alternative is a window in which the daemon
recognises the variant and every drop log calls it `kind=unknown`.

### The stale variant count — a required correction

`eventKind`'s `ModelAnnounced` arm asserts *"Handle now has an arm for 16 of
turnevent.Event's 17 implementations, and the one without is PermissionRequest"*.
Adding a marker makes that false in both numerals and in the exclusion list. It is
a `//` comment in a file this slice already modifies, and leaving it is exactly the
class of silent-wrong-comment the cite rules exist for.

Correct it to: an arm for 16 of **18** implementations, the two without being
`PermissionRequest` — PTY/modalbridge-only, never produced by `streamsup.Parser` —
and `SlashCommandList`, which nothing produces yet (#1719 / #1720 own the
producer). The surrounding argument stands unchanged. Re-derive the count yourself
from the marker declarations before writing it; do not trust this paragraph's
arithmetic.

This is the only occurrence in Go source. `docs/specs/architecture/1849-*.md`
carries the same arithmetic historically and is a frozen build artifact — leave it.

### The totality row

`cmd/pyry/stream_turn_busy_test.go`, one row in
`TestTurnMarkFor_TotalOverEveryVariant`'s table, after the `ModelList` row:

```go
{turnevent.SlashCommandList{Commands: []turnevent.SlashCommand{{Name: "clear"}}}, turnMarkNone},
```

A non-zero-value fixture, so the row exercises a populated event. Table position
is cosmetic — the test sorts `covered` before comparing — but keeping it beside
`ModelList` keeps the family readable.

Extend the test's doc comment with a short paragraph in the register of the
`ModelList` sentence already there: neither an opener nor a closer, because the
inventory is reported once per `initialize` exchange, which is not a turn boundary
and not even per-turn; the whitelist's default already returns it, so the row
asserts an existing answer rather than a new arm. The wedge argument is one step
stronger than `ModelAnnounced`'s: an announcement at least rides a turn some
`TurnEnd` will close, while a turn opened on an inventory has no turn end anywhere
in its future to clear the mark.

## Concurrency model

**None introduced.** Both new types are pure value types in a package with no
goroutines, channels, mutexes or I/O — `turnevent-package.md` § Concurrency is the
standing statement and this slice does not change it.

One consequence inherited from the family and worth knowing before the producer
slice: `Commands` is a slice header, and the `ModelList` arms downstream establish
that an event's slices are CARRIED, never mutated in place — a sort, an in-place
dedupe or an append into a slice the event owns is a data race once a holder
retains the value across goroutines. Nothing in this slice retains or mutates
anything; the rule is recorded so a later slice does not have to rediscover it.

## Error handling

**No failure modes are introduced.** No parsing, no I/O, no validation, no
constructor that could reject. Both types are struct literals whose zero value is
meaningful: a `SlashCommandList{}` is an inventory with no entries, which is
representable and which no code path in this slice produces.

The one behaviour worth naming as deliberate rather than missing: an event of this
variant reaching the fan-in or the emitter today is DROPPED with a debug log naming
the kind — `turnbridge.MapEvent`'s default returns not-ok, and `Handle` has no
case. That is correct for a variant with no producer and no wire mapping, and the
tests below pin it.

## Testing strategy

`make check` must be green. Two test files change; no production behaviour does.

**1. `cmd/pyry/stream_turn_busy_test.go` — the totality gate (AC#2).** The row
above. The gate is the AST-derived `turnEventVariants`, so this is not a test you
choose to write: without the row, `make check` is red the moment the marker method
lands. Verify by transiently deleting the row and confirming the failure names
`SlashCommandList` — that is the deterministic proof the gate is doing its job.

**2. `cmd/pyry/interactive_turn_v2_test.go` — the kind function (AC#3).** Copy
`TestInteractiveTurnEmitterV2_ModelListEventKindNamesTheVariant`'s rig in
structure. Two of its choices are load-bearing, not stylistic:

- The `slog.HandlerOptions.ReplaceAttr` that drops slog's own time attr. It leaves
  the captured record with no digits at all, which is what makes a numeric needle
  safe in a whole-log `strings.Contains`.
- Conspicuous sentinel strings in the fixture rather than realistic command names.
  The leak negative is a whole-log `strings.Contains` and the log itself carries
  `kind=slash_command_list` and `event=interactive_turn.no_cursor`, so a realistic
  value — `clear`, `compact`, anything containing `slash`, `command`, `list`,
  `turn`, `event`, `cursor`, `drop`, `relay` or `kind` — would be a substring of
  the log's own text and would redden the test against a CORRECT implementation.
  Use `emitterModelListFixture`'s `qq-` / `zz-` shape.

Fixture requirements, mirroring `emitterModelListFixture`:

- Package-level and read-only; the tests around it run in parallel and share it.
- At least two entries, distinguishable in every field — `turnbridge-package.md`'s
  rule that identical rows let a swapped-index bug pass.
- One entry with a non-nil `TruncatedFields`, one with `nil`, so the family's
  load-bearing absence is present in the fixture from the start.
- A `slashCommandListSentinels()`-style helper deriving the negatives FROM the
  fixture rather than re-listing them beside it, so a sentinel added to an entry
  cannot silently drop out of the assertions.

Scenarios, as bullets — write them in the project's idiom, no code is prescribed:

- *Empty cursor.* A `stubCursor` with no conversation routed, a
  `fakeInteractiveBcast` with one interactive conn, `Handle` the fixture once.
  Assert the log is non-empty; that it contains `kind=slash_command_list`; that it
  does NOT contain `kind=unknown`; that no fixture sentinel appears anywhere in it;
  and that the decimal spelling of `len(fixture.Commands)` does not appear (AC#3's
  "no entry count"). Then assert zero envelopes pushed and `inTurn` still false —
  the pre-routing drop is unchanged and no turn is opened on the way to it.
- *Live cursor.* The same fixture through a `stubCursor` set to `testConvID`. This
  reaches `Handle`'s `default` arm instead, and pins the slice's scope boundary
  deterministically: assert the drop log again names `kind=slash_command_list` and
  leaks nothing, that zero envelopes were pushed, and that `inTurn` is false. This
  is what proves nothing in this slice produces or publishes the variant.

The live-cursor case is a deliberate tripwire: the slice that finally gives
`Handle` an arm for this variant WILL redden it, and updating it is that slice's
work — the same lifecycle every arm-claiming ticket in this family has had.

**Do not touch `internal/turnevent/event_test.go`'s package-local `eventKind`.**
It is a partial hand list that stopped at `Compacting` and was not extended by
`RateLimited`, `ModelAnnounced` or `ModelList`. It looks like a call site and is
not one; extending it is scope creep and would misrepresent it as maintained.

**No new test in `internal/turnbridge`.** `MapEvent`'s coverage is a hand table
with no exhaustiveness gate, so nothing there breaks and nothing there needs a row
for a variant that is deliberately unmapped.

## Scope boundary

Out of scope, restated so it survives contact with a tempting adjacent edit:

- Nothing in `internal/streamsup` — no decode target, no cap constant, no emit, no
  parser arm.
- No `turnbridge.MapEvent` arm and no `protocol` change.
- No `Handle` case, no emitter wiring, no eventring, no relay, no client.
- No `ArgumentHint`, `Description` or `Aliases` field — each arrives with its own
  slice, and the aliases absent-vs-empty reading is #1825's call, which this slice
  must not pre-empt.
- No `DroppedCommands` field.
- No knowledge-base doc. The documentation phase owns
  `docs/knowledge/features/turnevent-package.md` and will fold this variant into
  its variant table after code review.

## Size check

Re-applied against this written spec, not against the sketch:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** — `internal/turnevent/event.go`, `cmd/pyry/interactive_turn_v2.go` |
| Total written work (production + tests + helpers + log calls + spec edits) | ≤ 400 | **~270** — ~120 turnevent (doc-dominated), ~20 `eventKind`, ~15 the totality row and its comment, ~115 the emitter test file |
| New exported types or interfaces | ≤ 5 | **2** — `SlashCommand`, `SlashCommandList` |
| Consumer call sites needing simultaneous update | ≤ 10 | **2** — the `eventKind` arm, the `turnMarkFor` totality row |
| Acceptance criteria | ≤ 5 | **3** |
| Distinct error/reject branches | ≤ 10 | **0** |

Calibrated against the nearest analogue commit rather than by feel: #1811's
non-`streamsup` half — the same declaration plus the same two consumer touches —
measured `internal/turnevent/event.go` +96, `cmd/pyry/interactive_turn_v2.go` +14,
`cmd/pyry/stream_turn_busy_test.go` +5, i.e. 115 lines. This slice's types carry
two fields where `ModelList`/`ModelOption` carried seven, and it adds the
emitter-side kind test that #1811 did not (that arrived with #1849). #1727's pure
protocol declaration measured 375 total lines across two files with four fields and
two `MarshalJSON` methods, which brackets the estimate from above.

## Open questions

1. **`var _ Event = ModelList{}` is missing from the assertion block** (since
   #1811; every other variant in `event.go` has one, and
   `turnevent-package.md` documents the convention as one per variant). Observed
   and deliberately left: the block is not the sealing mechanism — the value-receiver
   marker method is, and the AST gate reads markers, not assertions — so the gap is
   cosmetic and repairing it is a different ticket's line. This slice adds its OWN
   assertion so the omission is not institutionalised. If code review would rather
   close it here, it is a one-line change.
2. **Whether an empty `Commands` is emitted at all** is the producer's gate
   (#1719 / #1720) and is deliberately unanswered by this type. The wire's position
   is already declared; the daemon's is not, and inventing one here would be a claim
   with no producer behind it.
3. **`Name`'s emptiness** is likewise the producer's. Nothing here rejects an entry
   with an empty name, consistent with the package's no-validation rule.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and making the boundary legible is this
  slice's deliverable.**
  The boundary is `internal/streamsup`'s stream-json decode (subprocess stdout →
  parent state), which this slice does not write. What it does is make the boundary
  EXPLICIT at the type a downstream consumer holds: the `SlashCommandList` doc
  records that every string is workspace-authored — a strictly lower-trust origin
  than claude's own strings, because a command defined in a repository was written
  by whoever wrote that repository — and that the daemon bounds but never sanitizes
  it. Downstream callers therefore know they hold untrusted text from the type they
  are reading, which is the only signal Go's type system will not give them. The
  spec requires the same statement at `Name`, pointing back at the type-level
  paragraph rather than diluting it into a restatement. One integrity hazard the
  partial field set creates is closed in the same place: `TruncatedFields` may
  never name a field this type does not declare, because a producer that cut a
  value the type does not carry has nothing to report here. Without that rule the
  report could tell a consumer that text it holds is incomplete when the type never
  held the text at all.
- **[Tokens, secrets, credentials] Not applicable by construction.** Neither type
  carries, derives, or is derivable into a credential. The one identity-shaped value
  in claude's `initialize` reply — `session_id` — is deliberately not a field, for
  `BackgroundTaskStarted`'s stated reason, and the spec requires that omission to be
  documented rather than merely present. A field never declared cannot leak.
- **[File operations] Not applicable.** No path, no filesystem access, no
  serialization to disk. The package's `boundary_test.go` mechanically forbids any
  non-stdlib import, and `event.go` imports only `encoding/json` (for
  `ToolStart.RawInput`, untouched here).
- **[Subprocess / external command execution] The category the ticket's label is
  really about, and the answer is a documented prohibition rather than a mechanism.**
  This type will carry command NAMES that a client is invited to send back — that is
  the feature. The realistic exploit is a consumer treating a published `Name` as a
  command vocabulary or handing it to `exec.Command` as an argv element. Nothing in
  this slice does either (no `exec`, no argv, no dispatch), and the spec requires the
  type's doc to carry `protocol.SlashCommand`'s amendment verbatim in substance: a
  name arrives inbound as ordinary message text, publishing it does not make it
  trusted, and no field here may reach a child as an argv element. That is the
  strongest instrument a declaration slice has; a code-level enforcement with no
  producer and no consumer to enforce against would be a defense for a failure mode
  that cannot yet occur.
- **[Cryptographic primitives] Not applicable.** No randomness, no comparison
  against a secret, no key material.
- **[Network & I/O] No findings, and one bound is deliberately absent.** No socket,
  no reader, no size cap here: the byte and entry bounds are the producer's, decided
  at construction (#1719 / #1720), and the spec requires the type to say so rather
  than declare a second maximum. That is not a gap — it is
  `protocol.SlashCommand`'s stated reason applied one layer in: a second cap is a
  second place the limit is decided, and the two could disagree silently. The
  wire-side consequence is already bounded independently (`SlashCommandListPayload`
  measures 14,277 bytes for 51 entries against the 65519-byte v2 envelope cap), so
  no unbounded value can reach a client without crossing a cap someone else owns.
  **SHOULD FIX, downstream:** the producer slice must land the entry-count bound and
  the per-string cap together — a per-string cap alone leaves the payload size a
  function of a count claude chooses. Flagged for #1719 / #1720; not this slice's to
  fix, and the spec's "no `DroppedCommands` until the bound that produces it" rule
  keeps the two coupled.
- **[Error messages, logs, telemetry] No findings — this is the category with a
  concrete, pinned control.** The realistic leak is `eventKind` returning anything
  derived from the event: a command name, an entry count, or a truncation report.
  The spec requires the arm to return the variant name alone and requires a test
  whose negatives are a whole-log `strings.Contains` over every fixture sentinel
  PLUS the decimal spelling of the entry count — so an arm returning
  `"slash_command_list:" + Name`, or `+ len(Commands)`, is red. The three other
  drop-logging sites (`sinkFor`, `startStreamTurnDrainV2`, `Handle`) already carry
  content-free SECURITY notes and consume only what `eventKind` returns, so pinning
  the function pins every site. The workspace-authored provenance makes this
  strictly stronger than the #833 model-value posture, not equal to it, and the spec
  requires the arm's comment to say so.
- **[Concurrency] No findings.** No goroutine, lock, or shared state is introduced;
  the package has none. The one live hazard for a value type with a slice field —
  in-place mutation of a carried slice by a downstream holder — is named in
  § Concurrency model and is inherited from the `ModelList` arms rather than created
  here. Nothing in this slice retains or mutates a value.
- **[Threat model alignment] Aligned, and the relevant threat is deferred by name.**
  `docs/protocol-mobile.md` § Security model treats subprocess-authored strings
  reaching a client as text the CLIENT must render safely. This slice restates that
  ownership at the daemon type — including the newline fact's home: the measured
  control-character observation belongs with `Description`, which this slice does not
  declare, so it is not claimed here. **OUT OF SCOPE:** client-side render
  sanitization, owned by the consuming clients (pyrycode-desktop#681, #694); and the
  producer-side bound, owned by #1719 / #1720.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
