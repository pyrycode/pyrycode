# #2428 — Bound the mapped context_usage frame to the v2 application-envelope cap

## Files read

- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.ContextUsage` arm — the code
  this slice amends. Its `NO FRAME BYTE BUDGET` paragraph and the `NOTHING IS RE-CAPPED`
  wording are the two the change falsifies.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.SlashCommandList` arm and
  `maxSlashCommandListBytes` — the sibling bound: its measured-not-counted cut, its
  `break`-never-`continue` rule, its additive dropped count, and the itemised-reserve shape
  its constant doc carries. This slice copies the shape and departs on apportionment.
- `internal/protocol/interactive.go` → `ContextUsagePayload`, `ContextUsagePayload.MarshalJSON`,
  `ContextUsageCategory`, `ContextUsageMCPTool`, `ContextUsageMemoryFile` — what the wire bytes
  actually are, and the type doc whose "copied verbatim … by a later mapper" sentence this
  commit falsifies. The three row types carry no `MarshalJSON` of their own, so a row's cost is
  its plain struct encoding; the payload's normaliser turns each nil list into `[]`, which is
  why every list costs 2 B whether or not a row is admitted.
- `internal/protocol/interactive.go` → `SlashCommandListPayload`'s `DroppedCommands` doc — the
  exact shape of the correction #2002 already made for the sibling.
- `internal/streamsup/context_usage_bound.go` → `boundContextUsageEntries`,
  `maxContextUsageEntries`, `maxContextUsageStringBytes` — the producer's caps (32 entries per
  list, 256 B per designated string, whole entry rejected over it), the descending-token order
  the cut must not disturb, and the generic-helper idiom the mapper-side cut mirrors.
- `internal/turnevent/event.go` → `ContextUsage` and its three row types — the source event. Its
  doc scopes "neither recomputes nor normalizes" to *claude's* scalars and says nothing about the
  wire, so nothing there needs correcting; **not edited**.
- `cmd/pyry/relay_context_usage.go` → `contextUsageResolver.Get` — the **second** production path
  to this frame (#2431). It calls `turnbridge.MapEvent`, so one bound in the arm covers both
  producers. This is the fact that makes the arm the right place rather than either consumer.
- `internal/turnbridge/outbound_test.go` → `TestMapEventContextUsageWorstCaseAgainstV2EnvelopeCap`,
  `TestSlashCommandListPayload_FitV2EnvelopeCap`, `maxV2AppEnvelope`,
  `TestToolResultPayload_FitV2EnvelopeCap` — the test to invert, the shape AC1 names, and the
  test-local cap constant whose doc already reconciles a production frame bound existing.
- `docs/specs/architecture/2371-turnbridge-publish-context-usage.md` § Security review
  → the Network & I/O finding that deferred here, with its measured arithmetic.
- `docs/specs/architecture/2002-slash-command-list-frame-budget.md` → the sibling's Revisions:
  the reserve is *measured in Phase B*, not guessed in the plan; and `TestMapEvent…CutEndsTheWalk`
  exists because a uniform-row fixture cannot tell `break` from `continue`. Both bind here.
- `docs/knowledge/features/turnbridge-package.md`, `…-outbound-adapter.md` — the package overview.
  Read only; the documentation phase owns it.

## Context

`MapEvent`'s `ContextUsage` arm is a verbatim translation with no size decision in it. The
producer bounds every *content* dimension, and the product of those bounds does not compose into
a frame guarantee: three 32-entry lists of 256-byte strings measure **45568 B raw** against the
65519-byte v2 application-envelope cap, but `encoding/json` has `SetEscapeHTML` on, so `&`, `<`
and `>` each cost six bytes and the same reading measures **251648 B escaped** — 3.8× the cap.
Roughly 4000 such characters, about 9% of the string content, exhausts the 19951 B of raw
headroom. An over-cap envelope is **rejected by the transport with `message.too_long`**, so the
frame is lost whole rather than truncated.

#2371 measured this and deferred it here rather than fixing it, because its AC1 forbade the
mapper re-capping and #2370's committed contract assigns bounds to the producer. That deferral is
what this ticket revisits, and the reasoning that reopens it is the sibling's: **the frame axis is
a different dimension from the producer's content dimensions.** No cap streamsup applies bounds how
many *bytes* a list costs on the wire, and nothing downstream of the mapper can apply one either —
both production paths (#2371's post-turn publication and #2431's on-demand reply) pass through
`MapEvent`, so one bound here covers both where a bound at either consumer would be the second
place the limit is decided.

No ADR is warranted: this is a constant and a loop inside an existing arm, decided by the
reasoning `maxSlashCommandListBytes` already records for the sibling frame.

## Design

### The constant

`maxContextUsageListBytes`, unexported, in `internal/turnbridge/outbound.go`, value **60000**.

It bounds the **sum of the three serialised inventory arrays**, each array's own `[` and `]`
included — not the envelope, and not an entry count. Two things it deliberately is not:

- **Not `maxV2AppEnvelope`'s 65519.** `maxDeltaTextBytes`' and `maxSlashCommandListBytes`' rule
  applies unchanged: the constant bounds the bounded fields, and the difference is the reserve.
- **Not an entry count.** The producer's 32-per-list cap is already a count cap and it is the
  thing that does not close the gap — 96 entries at their escaped per-entry terms is 3.8× the cap.
  The cut must be measured against marshalled bytes.

**The reserve is 65519 − 60000 = 5519 B**, and this frame needs a larger one than the sibling's
1519 B for a concrete reason: `Model` is a producer-bounded 256-byte string that lives *outside*
the budgeted lists, so at the `&` worst case it alone costs 1536 B of reserve where the sibling's
payload has no such field. Itemised worst case, all six integers at their widest and a hostile
64-rune all-escaping `conversation_id`: `conversation_id` ≈ 405 B, `model` ≈ 1547 B, the three
claude scalars ≈ 104 B, the three `dropped_*` keys ≈ 126 B, the three list keys and braces ≈ 45 B,
the envelope frame (max-uint64 `id` and `event_id`, `type`, `ts`, `payload`) ≈ 121 B — about
**2350 B**, so 5519 is roughly 2.3× a pessimistic worst case. The only genuinely unbounded
contributor left outside the measurement is `conversation_id`, the sibling's stated assumption
unchanged; the 3169 B of spare would take another 528 escaped runes of it to spend.

Its branch is **unreachable on claude's ordinary output**, which is what makes it a bound rather
than dead code: the measured fake-Claude reading drops 1 MCP tool and 1 memory file and serialises
to a fraction of this. The Phase B measurement replaces every figure above that the test can
report; if the fit test ever fails, **LOWER** this constant, never raise it.

### The cut — round-robin admission, one shared budget

This is the design call the ticket exists for: three lists, one envelope. The scheme is
**round-robin across the three lists against a single shared budget**, in the fixed lane order
`categories`, `mcp_tools`, `memory_files`. Round *n* offers each still-open lane its entry at
index *n*; a lane whose entry does not fit is **closed** and contributes nothing further; the walk
ends on the first round that admits nothing.

Why it beats the two alternatives:

- **Against fixed thirds** (20000 B per list, cut independently): thirds satisfy AC2 and AC3 but
  waste the envelope. A reading with no categories and a full, escape-heavy MCP inventory would be
  cut at a third of the budget with 40000 B unspent — a shorter inventory for no gain. Round-robin
  spends everything that is there.
- **Against sequential with a reserved floor** (walk `categories` to exhaustion holding back one
  row's worth for each unstarted lane): comparable code, worse outcome. It satisfies AC3's letter
  and defeats its stated purpose — 32 categories, one MCP tool, one memory file is exactly the
  lopsided breakdown AC3 calls worse for a client than a shorter list of each.

Round-robin gives each list **equal turns, not equal bytes**, which is the right currency: it
charges a list for how expensive its rows are without ever starving a list that has rows left.

Contract:

- Rows are built exactly as today, one `protocol.*` row per source entry, element-wise into fresh
  outer slices.
- A row's cost is `len(json.Marshal(row))` plus one byte for its separating comma when its own
  lane has already kept a row. The running total starts at **6** — three empty arrays' brackets,
  which cost 2 B each on the wire because `ContextUsagePayload.MarshalJSON` normalises nil to `[]`.
  Marshalling the **protocol** row rather than the `turnevent` one is what makes the measurement
  the wire's: the JSON key names are inside those bytes.
- A lane closes — never skips — on its first row that does not fit, so each kept list is a
  **prefix** of its input in the producer's descending-token order. Skipping to fit a smaller row
  behind it would hole-punch the ranking and make the retained set order-dependent.
- Each dropped count is `e.Dropped<List> + (len(e.<List>) - len(kept<List>))`. Added to its **own**
  base, never recomputed from a retained length, never cross-read, so `len(list) + dropped`
  recovers that list's pre-cut size independently for all three pairs.

A generic package-level helper owns the cost arithmetic so the three lanes do not triplicate it —
the shape `internal/streamsup`'s `boundContextUsageEntries` already establishes for this event:

```go
// admitContextUsageRow charges one marshalled row against the shared frame
// budget and appends it when it fits. Reports whether the row was admitted;
// false closes that lane's walk.
func admitContextUsageRow[T any](row T, dst *[]T, size *int) bool
```

Everything else in the arm is unchanged: order is the producer's, no field is re-capped, no
charset check, the memory path is untouched, nil lists still forwarded rather than pre-allocated,
no suppression branch, no log line.

### The two doc corrections

- `MapEvent`'s arm: the `NO FRAME BYTE BUDGET` paragraph is replaced by the budget's own
  reasoning, and the `NOTHING IS RE-CAPPED…` paragraph gains the sibling's scoping sentence — the
  producer's *content* dimensions are still not re-decided here; the *frame* axis is, and is
  decided here because both wire consumers pass through this function.
- `protocol.ContextUsagePayload`'s doc: "Each is copied verbatim from its `turnevent.ContextUsage`
  counterpart by a later mapper" becomes the sibling's shape — the event's count is the *base* and
  the mapper adds its own frame cut, so the wire field is the sum of two cuts. The sentences that
  survive unchanged are the ones this ticket must not weaken: the counts stay independent, are
  never inferred from a retained length, are never cross-read, and `len(list) + its own count`
  still recovers the pre-cut size.

## Concurrency model

No goroutines. `MapEvent` stays a pure value-to-value adapter.

The arm's carry-never-mutate rule **tightens**: the cut builds fresh outer slices and stops early,
so it never sorts, dedupes, filters in place, or reslices `e.Categories` / `e.MCPTools` /
`e.MemoryFiles`. The three row types are flat `string`/`int` structs with no slice field, so the
element-wise copy shares no mutable backing array at all and the shared string headers are safe
because Go strings are immutable — the arm's existing "already total" reasoning is unchanged by
the cut. This binds now for a reason #2371 did not have: #2431's `contextUsageResolver` retains a
mapped payload in a `contextUsageFlight` and hands it to several waiting askers.

Determinism is a concurrency-adjacent property worth stating: same input, same frame. The lane
order is fixed, the walk is index-ordered, and no map is iterated.

## Error handling

`json.Marshal` of a flat string/int struct cannot fail in practice — `encoding/json` coerces
invalid UTF-8 to U+FFFD rather than rejecting it — but Go forces the branch. It is handled
**fail-closed**: the helper reports not-admitted, which closes that lane and counts the row and
every row behind it as dropped. An entry that cannot be measured cannot be admitted to a measured
budget, and the frame stays sendable.

That same close covers the state this ticket must not invent a path for: a row larger than the
whole budget is unreachable under the producer's caps (~3.1 KB escaped worst case against a 60 KB
budget), and were a future producer to hand one over, its lane closes like any other and the frame
still fits. No "nothing fits" branch is written.

The branch builds **no error value and writes no log line** — see § Security review.

## Testing strategy

All in `internal/turnbridge/outbound_test.go`. Every fill is synthetic; the producer's caps are
restated as local literals because they are unexported in another package, the discipline the
sibling fit tests already follow.

- **`TestMapEventContextUsageWorstCaseAgainstV2EnvelopeCap`** — updated, not deleted, and
  reshaped to `TestSlashCommandListPayload_FitV2EnvelopeCap`'s form: it now marshals the mapped
  payload into a worst-case `protocol.Envelope` (max-uint64 ids, populated `EventID`) beside a
  hostile 64-rune all-escaping `conversation_id`, and measures **that**. Two cases, both
  inverted from measurement into assertion:
  - *raw* — asserts the envelope fits **and that no cut fired**: all 32 entries of each list
    survive with their dropped counts unchanged. This is the tripwire the ticket asks to keep,
    strengthened: the old size-only assertion would stay green forever once a budget exists, where
    a cut firing on the raw worst case is exactly the signal that the producer's caps have risen
    past what this frame carries whole.
  - *escaped* — asserts the envelope now **fits**, that the cut fired, and that all three lists
    kept at least one entry. This is the assertion the ticket says inverts.
  It supplies the measured reserve figure the constant's doc quotes.
- **Nothing-cut, with a headroom guard** — an under-budget reading maps exactly as the unbounded
  arm did: every entry present, in order, all three counts carried unchanged. Preceded by a
  `t.Fatalf` precondition asserting the fixture's measured array bytes are comfortably under the
  budget, so a fixture that grows to meet the budget reddens and names itself. (The equality alone
  holds at any budget and proves no headroom — `streamsup-package-producing-turnevent-slashcommandlist.md`
  records that lesson.)
- **Prefix per list** (AC2) — an over-budget reading whose kept lists are each a prefix of their
  input, in order, with no hole.
- **Round-robin floor** (AC3) — three non-empty lists and a budget that fires: each kept list is
  non-empty. Decorrelated so a sequential implementation fails it — the categories alone would
  exhaust the budget.
- **Additive, per-pair dropped counts** (AC4) — mutually distinct non-zero bases (3/5/7) on an
  over-budget reading; each `len(list) + its own dropped count` equals its input length plus its
  own base, so a cross-wired pair reddens.
- **A lane closes rather than skips** — decorrelated row sizes within one lane (large rows then
  tiny ones) so a `continue` implementation admits a small row behind the cut and the prefix
  assertion names it. #2002's Revisions record that a uniform-row fixture cannot tell the two
  apart; both this and the additive-count assertion are mutation-checked the same way.
- **Standing tests that must stay green**: the `ContextUsage` rows of `TestMapEventOutbound`
  (order preserved, string not re-capped, path not normalised, counts independent) and the
  non-mutation test — their fixtures are far under the budget, so no cut fires on them.

Gate (§ B2): `go test -race ./internal/turnbridge/... ./internal/protocol/...`, `go vet ./...`,
`go build ./cmd/pyry`. The full-module race suite is the verifier's.

## Open questions

1. **What reserve does 60000 actually leave?** Fixed in Phase B from the fit test's measured
   output rather than guessed here. Record the number in the constant's doc, and a `## Revisions`
   entry if the measurement moves the constant.
2. **Does the escaped worst case keep more than the AC3 floor?** The arithmetic says roughly 20
   entries fit across the three lanes at ~3 KB each, but the exact split is a measurement. If it
   turns out to be at or near the floor of one per lane, that is worth recording in the constant's
   doc as the realistic degraded shape rather than leaving a reader to assume the budget is roomy.

## Documentation handoff

Owned by the documentation stage, not this builder. Carried forward verbatim from the ticket:

- `docs/protocol-mobile.md` § `context_usage`: the paragraph beginning **"Each dropped count is
  independent and is never inferable from its list's length"** states that each count is copied
  verbatim from `turnevent.ContextUsage` by the mapper and that none is derived from a retained
  list. After this change **two cuts feed each count**. Correct that paragraph and the three
  `dropped_*` rows of the field table, and state that a non-zero dropped count can now arrive
  beside any number of entries — § `slash_command_list`'s "the sibling's shortcut does not
  transfer" paragraph is the precedent to follow.
- Add a changelog entry to the same file.

**Status: pending.** No `docs/` file outside this spec is edited by this slice.

## Revisions

**2026-09-14 — both Open questions resolved, and two fixture defects that mutation-checking
found and the plan's testing strategy would have shipped.**

1. **The constant stays 60000 and the reserve is measured, not guessed** (Open question 1).
   `TestMapEventContextUsageWorstCaseAgainstV2EnvelopeCap` reports a **2284 B** reserve outside
   the three arrays against the 5519 the constant leaves — roughly 2.4× — with the escaped
   worst-case envelope at **61240 B, 93.5%** of the cap, and the raw worst case at **44995 B** of
   arrays, crossing uncut. The plan predicted ~2350 B and ~62350 B, so its arithmetic stands. The
   constant's doc carries the measured figures and, more importantly, the **structural** bound
   they are evidence for: the arrays cannot exceed 60000 whatever the input, so no envelope can
   exceed 60000 + reserve — a tighter-packing reading than the worst case exists, and the
   measurement alone would not cover it.

2. **The degraded shape is short, and the doc says so** (Open question 2). At the escaped worst
   case the frame keeps **8 categories, 8 MCP tools and 7 memory files of 32 each**. That is the
   design working — every list keeps its heaviest entries and its own honest count — but a reader
   should not infer the budget is roomy, so the constant's doc states the number.

3. **A single-list fixture cannot test close-versus-skip in this arm, and the plan's did not.**
   The plan carried #2002's lesson and still landed a fixture that a mutation removing all three
   close flags left green. The reason is specific to this arm's control flow rather than to the
   fixture's row sizes: the walk already ends on the first round that admits nothing, so with one
   populated list a rejection ends it whether or not the list is marked closed — the flag is dead
   weight there. It is load-bearing only while ANOTHER list is still admitting, which is when the
   round counts as progress and an unclosed list would be offered its next entry. The fixture is
   now two-list: 32 cheap categories keeping the walk alive past the cut, and 28 fat MCP rows
   followed by 4 tiny ones. Three preconditions guard the window rather than assume it — the cut
   must land inside the fat block, the cheap rows must outlive it, and the leftover must still
   cover a tiny row — because that window is an arithmetic coincidence between two row sizes and a
   budget. Measured: the cut lands after 24 of 28 fat rows with all 32 categories kept and 556 B
   left against a 54 B row, and the mutation now names the four rows it wrongly admits.

4. **Equal-length lists cannot catch a cross-wired dropped count.** The plan's additive fixture
   used three 32-entry lists, which the budget cuts by 24/24/25 — so a mapper adding the
   categories' cut to `dropped_mcp_tools` lands on the number the correct mapper produces, and
   that mutation survived. The fixture is now **32/20/12**, giving three distinct cuts (24/12/5)
   over three distinct bases (3/5/7); the cross-wiring mutation reports 29 against a wanted 17.
   Five mutations are now checked in total — lanes never closing, a cross-wired count, a count
   recomputed from its base alone, a budget charged on unescaped length, and sequential rather
   than round-robin apportionment — and each reddens in the assertion written for it.

5. **One production-doc correction beyond the two the plan named.**
   `protocol.ContextUsagePayload`'s "Bounds are not re-decided here" paragraph sat directly above
   a field that is now cut elsewhere, which invites the wrong read; it gains a clause naming the
   frame's byte axis as the one bound decided outside the producer and saying why it lives in the
   mapper. The same paragraph now states the **prefix** guarantee, which is a client-facing
   property no type doc carried. No file count moved — both edits are in a file the plan already
   names.

## Security review

**Verdict:** PASS

This slice's security character is that it **takes an attacker-influenced input and lets it
decide what reaches the wire**. Before it, escape-heavy workspace text cost the operator the whole
frame; after it, the same text costs a tail. Every finding below is about whether that trade
introduces a new lever.

**Findings:**

- **[Trust boundaries]** No MUST FIX, and the boundary does not move: `emitContextUsage` is still
  where claude's stdout becomes a bounded `turnevent.ContextUsage`, and this arm remains
  downstream of it, re-deciding no content dimension. What the slice adds is one **transient**
  consumer of those untrusted strings — the `json.Marshal` inside `admitContextUsageRow`, whose
  output is used for its **length only** and discarded. It is not the bytes that ship, not
  compared against anything, and not retained. The helper is package-private and instantiated at
  exactly the three `protocol.ContextUsage*` row types, all flat `string`/`int` structs with no
  `MarshalJSON` of their own, so no side-effecting encoder can be reached through its `T any`.
- **[Trust boundaries — the new lever, and why it is a net reduction]** The finding worth stating
  plainly rather than assuming away. A workspace author who controls MCP tool names or memory-file
  paths can inflate a row and cause a later entry to be dropped — a capability they did not have
  before. It is a **strict decrease** in their power: the same input previously suppressed the
  *entire* frame via `message.too_long`, where now it suppresses a tail. They cannot reorder — the
  producer ranks descending by claude's token arithmetic, and this arm neither sorts nor
  re-weighs. The one genuinely new sub-lever is **cross-lane**: a shared budget means a fat
  category costs the MCP and memory lanes some entries, which fixed thirds would have prevented.
  Bounded three ways, and deliberately accepted: round-robin gives every lane a turn *per round*,
  so a fat lane cannot starve the others the way a sequential walk would; the producer's caps put
  a single row at ~3.1 KB escaped worst case against a 60000 B budget; and the AC3 floor — at
  least one entry per non-empty lane — **holds by arithmetic, not by observation**, since round 0
  offers all three lanes their first row at a combined worst case of ~7.8 KB. Pinned by the
  escaped-case assertion in the fit test.
- **[Tokens, secrets, credentials]** Not applicable by construction. The event carries no
  credential material: `Model` is a claude-authored label, the integers are token *counts*, and
  the three inventories name categories, MCP tools and memory files. Nothing is generated, stored,
  rotated, revoked, compared or logged. The frame rides the existing Noise-sealed lane; this arm
  mints no key material and no identifier.
- **[File operations]** No findings, and this stays the category most exposed to a well-meaning
  regression. `ContextUsageMemoryFile.Path` is path-shaped and is still **never** joined, cleaned,
  resolved, stat'ed, opened or matched — the cut adds zero filesystem operations, and the only
  thing it does with a `Path` is encode it to measure its length. The existing traversal-shaped
  fixture must stay green: an *admitted* row's path still crosses byte-for-byte. A row being
  dropped weakens nothing. One correctness trap adjacent to this category, named so it is not
  reached for: a lane's cost must be the **marshalled row's** length, never `len(path)` — a
  content-only sum silently omits the escaping the whole bound exists to charge for.
- **[Subprocess / external command execution]** No findings. The enumeration of sinks for these
  strings grows by exactly one, and it is the transient measurement marshal above. No
  `exec.Command` argument, no argv element, no `sh -c`, no `filepath.Join`, no `regexp`, no
  environment variable, no log attribute.
- **[Cryptographic primitives]** No randomness, and here that is a required design property
  rather than an absence. The cut must be **deterministic** — fixed lane order, index-ordered
  walk, no map iteration, no randomised tie-break — and the reason is sharper for this frame than
  for the sibling: #2431's `contextUsageFlight` hands **one** mapped payload to every asker inside
  its collapse window, so a non-deterministic cut would be invisible within a window and produce a
  breakdown that flickers between windows for an unchanged reading.
- **[Network & I/O]** This category is the deliverable. The substantive finding is the one #2371
  deferred: **counting entries is not a byte bound.** `encoding/json` escapes `&`, `<` and `>` at
  six bytes each, so the producer's 32-per-list cap admits a 251648 B frame against a 65519 B cap
  — 3.8× — and the transport rejects it whole with `message.too_long`. The cut is therefore
  charged against marshalled, post-escaping bytes. Two consequences taken on: the reserve is
  validated against a **hostile 64-rune all-escaping `conversation_id`** rather than a real one
  (SHOULD FIX — the fit test must fill it that way; the verifier should check that landed), and
  *that fixture is ~10× pessimistic rather than optimistic*, which is the answer to the obvious
  attack on the reserve: `internal/conversations`' `NewConversationID` mints a 36-character
  `crypto/rand` hex UUID, no byte of which escapes, and #2431's `RequestContextUsagePayload` doc
  pins that the reported id comes from the **resolved record** rather than being echoed from the
  client's string, so a remote caller cannot grow it. Nothing in `turnbridge` *enforces* that — it
  is the sibling's inherited assumption, unchanged — so what would break it is a future
  human-authored or client-supplied conversation identifier, and the 3169 B of spare absorbs 528
  escaped runes before it does. Work is bounded too: the walk performs at most one marshal per
  source entry plus one per closing lane, so ≤ 99 under the producer's caps regardless of what a
  producer hands over — no amplification, and no unbounded read is introduced.
- **[Error messages, logs, telemetry]** SHOULD FIX, and it is the trap this arm is likeliest to
  fall into — sharper than the sibling's, because one of these strings is a filesystem path and a
  developer's instinct to log a path is stronger than to log a command name. The arm writes no log
  line today, by rule, because every string in it is claude- or workspace-authored. The
  fail-closed branch must not become the exception: it constructs **no error value wrapping the
  row and no log attribute** — it closes the lane and counts a drop. A
  `fmt.Errorf("marshalling %q: %w", file.Path, err)` would put workspace-authored text on the
  exact path the arm exists to keep it off, in the one place a reader would call it good practice.
  The three drop counts are daemon-derived and are the only values here safe to log, if a caller
  ever wants one. No telemetry or metric is added.
- **[Concurrency]** No findings, and one inherited claim needed re-checking rather than
  inheriting. The arm's comment says no analogue retains a `ContextUsage`; #2431 landed after
  #2371 and does retain — but it retains the **mapped payload** in a `contextUsageFlight`, not the
  event, and that payload's slices are fresh outer slices of flat value copies, so it shares no
  backing array with the event or with any other asker's read. The claim as written is still true
  and the cut does not weaken it: fresh outer slices, early close, no in-place sort, dedupe or
  filter, and **no reslice of `e.Categories` / `e.MCPTools` / `e.MemoryFiles`** — which is why the
  cut must not be implemented as a reslice, since the flight hands one payload to several waiting
  goroutines. No goroutine is spawned and no lock is taken; `MapEvent` stays pure.
- **[Threat model alignment]** Addressed for the relevant threat in `docs/protocol-mobile.md`
  § Security model: untrusted workspace text reaching a client, bounded but not sanitized, with
  the client owning the render boundary — and now, additionally, unable to cost the operator the
  whole frame. Named out of scope: `conversation_id`'s absent bound in this package (the sibling's
  standing assumption, argued above rather than fixed here), and the `docs/protocol-mobile.md`
  corrections, which belong to the documentation stage per § Documentation handoff.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14
