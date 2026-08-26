# #1812 — Bound the decoded model-entry count and report the drops

## Files to read first

Symbols, not lines — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/streamsup/parser.go` → `maxTaskRosterEntries` — **the shape this ticket copies.** The cardinality-cap doc form: multiplicand × count = a stated product, the product's relation to `maxUnrecognizedRaw`, the multiple-of-observation argument, and the after-`json.Unmarshal` transient paragraph. Read it before writing a single line of the new constant's doc.
- `internal/streamsup/parser.go` → `emitBackgroundTaskRoster` — the six lines that implement it: the count bound placed **before** the entry loop, truncation **from the tail**, `dropped` computed as the difference. The new code is this block with different names.
- `internal/streamsup/parser.go` → `maxModelResolved` — the paragraph beginning "THE ENVELOPE ARITHMETIC IS PARTIAL". It states the 768-byte multiplicand this ticket inherits and the one constraint on the count (six observed entries must fit with room). It is also **an edit target**: it names this ticket as someone else's work.
- `internal/streamsup/parser.go` → `emitModelList` — the four rungs, the per-entry `bound` closure, and the loop-head comment that says no count bound runs. Edit target.
- `internal/streamsup/parser.go` → `logControlResponse`, `controlResponseMsg` — the one record this path writes, its "three attributes and NOTHING else" rule, and the content-free discipline behind it. Both docs are edit targets (see § The log record).
- `internal/turnevent/event.go` → `BackgroundTaskRoster` (the `DroppedTasks` field doc) — the count-on-the-aggregate doc style, including why the count does not report as a name in a `TruncatedFields` list. The new field's doc is this one re-argued for models.
- `internal/turnevent/event.go` → `ModelList` (the `Models` field doc) — carries the "COUNT is NOT bounded in this slice" statement. Edit target.
- `internal/protocol/interactive.go` → `ModelListPayload` — `DroppedModels`' doc, the reason the count exists. Its "NOTHING COUNTS IT YET" sentence becomes false when this lands (see § Doc sweep). **No field, no marshalling, no behaviour change in this package.**
- `internal/streamsup/parser_test.go` → `taskRosterEntriesCapFixture` (and the const block's doc above it) — why a count fixture is a **literal** and never the production constant. Also `rosterEntriesFixture` and `TestParser_BackgroundTaskRosterBounds` — the table shape the new test mirrors.
- `internal/streamsup/parser_test.go` → `modelListLineFixture`, `modelEntryFixture`, `capturedInitializeLine`, `collectEvents` — the fixture builders already in place; the new test needs no new line-building machinery, only a multi-entry generator.
- `internal/streamsup/parser_test.go` → `TestParser_InitializeControlResponseDecodesTheCapturedModels`, `TestParser_InitializeControlResponseRejectBranches`, `TestParser_ModelListIsLoggedContentFree` — the three existing tests this ticket amends.
- `internal/streamsup/initialize_capture_test.go` → `capturedInitializePayload` — the in-package capture reader (#1810). No `e2e_realclaude` tag, so every criterion here is provable under `make check`. Its `#1812` mentions are a list of decodes riding the reader and stay as they are.
- `docs/knowledge/features/streamsup-package.md` § "Decoding the initialize ack into `turnevent.ModelList` (#1811)" — carries the "entry count is not capped in this slice" claim. **Read-only. The documentation phase folds this slice's outcome in after code review; do not edit it.**

## Context

`emitModelList` bounds each decoded entry's three strings (`maxModelResolved` / `maxModelValue` / `maxModelDisplayName`, 768 bytes per entry worst case) and bounds nothing about **how many** entries it keeps. A per-field text cap alone leaves the value's total size a function of a number claude chooses, which is `maxTaskRosterEntries`' doctrine stated in the tree already.

The cap and the count ship together because a silent cardinality drop is the one bound that is worse than no bound: `protocol.ModelListPayload.DroppedModels` already exists on the wire and a permanent `0` there reads as "nothing was dropped". A client would then show a six-model menu as complete when claude sent forty. This decode is that field's only honest source.

Nothing in this slice publishes either value — `turnbridge.MapEvent`'s `default` still drops the variant until #1693.

**No ADR.** This is the second application of a doctrine `maxTaskRosterEntries`' doc already records in full; the decision record is that constant's doc comment, and this ticket's contribution to it is one derived number.

## Design

Three edits to production code, one of them doc-only.

### 1. The constant — `maxModelListEntries = 10`

New constant in `internal/streamsup/parser.go`, placed immediately after `maxModelDisplayName` and before `controlResponseSuccess`, so the model family stays contiguous exactly as `maxTaskRosterEntries` sits inside the task family.

The number is derived, not chosen, and the doc must carry the derivation in `maxTaskRosterEntries`' form. The four facts and what they force:

- **Multiplicand: 768 bytes per entry**, inherited verbatim from `maxModelResolved`'s doc rather than re-derived.
- **Ceiling.** `maxTaskRosterEntries`' product is 8192 bytes, and that number is load-bearing: it is exactly half of `maxUnrecognizedRaw`'s whole-line 16 KiB, which is how the package keeps its ordering one level up — a whole KNOWN event must not approach the cap on an entire UNKNOWN line. 8192 / 768 = 10.67, so **10 is the largest integer count whose product stays under that landmark.**
- **Floor.** The observed list is six entries (the committed capture, claude 2.1.239). `maxModelResolved`'s doc requires checking that six fits "with room left"; 10 leaves four slots.
- **Product.** 10 × 768 = **7680 bytes**, 11.7% of the 65519-byte v2 application-envelope cap (`docs/protocol-mobile.md` § Application-envelope size cap), and 46.9% of `maxUnrecognizedRaw` — just under the roster's half, so the ordering holds with the roster's own margin. Escaping is mild for `maxUnrecognizedRaw`'s reason verbatim (these are JSON string values, so the growth is quotes and backslashes rather than a `\u00XX` expansion); pathological all-quote content roughly doubles it to ~15 KB, ~23% of the envelope.

Two points the doc must state because a reader will ask:

- **Why not 8, borrowed from `maxTaskRosterEntries`.** Two slots above an observation of six is not room. `maxModelResolved`'s doc flagged this exact temptation, and the failure it buys is a cap firing on claude's *ordinary* output — the menu everyone sees, cut, on every child.
- **Why not a power of two, unlike every other constant in this family.** 768 = 3 × 256, so no power of two lands near the ceiling: 8 is the too-tight case above, and 16 puts the product at 12 KiB — 75% of the whole-line UNKNOWN budget, which is the ordering this package keeps. The decimal is what satisfies both binding constraints at once, and saying so is cheaper than a reader re-deriving it.
- **The multiple is thinner than the text caps' 10x, deliberately.** A text cap's overflow mangles an identifier in place; this one shortens a list and says by how many, so a client can render "6 of 40" rather than a wrong menu. The reported failure mode is what buys the thinner margin.
- **What 768 excludes.** It counts claude-derived text only. An entry can also carry up to three DAEMON-authored names in `TruncatedFields` (~40 bytes), which claude cannot inflate and which `maxTaskRosterEntries`' arithmetic likewise excludes. One sentence, so the omission reads as deliberate.

The fixture literal in the test mirrors the number and is **not** written as the constant — `taskRosterEntriesCapFixture`'s doc is the standing reason.

### 2. The bound in `emitModelList`

Placed after rung 3 (`len(entries) == 0`) and before the entry loop, replacing the loop-head comment that currently says no count bound runs. The rung order is unchanged: undecodable → nak → empty → **cap** → loop. Capping cannot create an empty list (the cap is ≥ 1) and cannot rescue one, so no rung's classification moves.

The block is `emitBackgroundTaskRoster`'s, with the roster's names swapped:

```go
// entries is claude's array; the count bound runs BEFORE the loop and
// truncation is FROM THE TAIL, preserving claude's order.
var dropped int
if len(entries) > maxModelListEntries {
    dropped = len(entries) - maxModelListEntries
    entries = entries[:maxModelListEntries]
}
```

Then `make([]turnevent.ModelOption, 0, len(entries))` over the shortened slice, and `p.emit(turnevent.ModelList{Models: models, DroppedModels: dropped})`.

**Tail truncation, not head or ranking.** `turnevent.ModelList.Models`' doc already claims claude's own order with no ranking invented, and `BackgroundTaskRoster.Tasks`' doc states the same rule for the same reason (ordering semantics unobserved). Keeping the first N is the only choice consistent with both.

**The transient array stays materialised.** The cap runs after `json.Unmarshal`, so a hostile line still builds the whole array in transient memory before it is shortened — `maxTaskRosterEntries`' accepted trade, and `maxModelResolved`'s amplification paragraph already bounds the exposure (`defaultMaxParseBuf` caps the line at 4 MiB; the densest legal entry is `{}`, so one pathological line is order 100 MB of transient, freed when the event is dropped). **Do not introduce a streaming `json.Decoder` token loop to bound the transient too** — that is a different design with a different proof, it has no precedent in this package, and this ticket bounds what is RETAINED and what crosses the wire.

### 3. `turnevent.ModelList.DroppedModels int`

One new field on the existing struct; no new exported type. Name matches both `BackgroundTaskRoster.DroppedTasks` and the wire field `protocol.ModelListPayload.DroppedModels` it will eventually feed.

Doc content, in `DroppedTasks`' register: how many entries claude sent beyond the producer's cap that this event does NOT carry; 0 when nothing was dropped; the list's true size is `len(Models) + DroppedModels`; the count reports on the aggregate rather than as a name in a top-level `TruncatedFields` (which is why this variant has none) because a name-only report loses how many were lost, and each dimension reports at the level where it happens.

No consumer needs updating: `turnbridge.MapEvent`'s `default` still drops the variant, `cmd/pyry`'s `eventKind` and `turnMarkFor` arms match on the type and read no field, and no test reflects over the struct's fields.

### 4. The log record — the open question, resolved: log the drop

`logControlResponse` gains a fourth attribute. Signature becomes `logControlResponse(reason string, models, dropped int)`; the three non-emitting rungs pass `0, 0` and the emit rung passes `len(models), dropped`.

The reasoning, since the ticket asks for it deliberately:

- **The lie is the same shape one layer down.** `models=6` on a reply that carried forty reads as "claude offers six models" to whoever is reading the log, which is precisely the argument `DroppedModels`' wire doc makes about a permanent zero.
- **Until #1693 this record is the only observable at all.** Nothing publishes the event, nothing retains it. A cap that fires in production with no observable anywhere is a cap nobody can know fired — and the first evidence that 10 is the wrong number would arrive as a user's short menu rather than as a log line.
- **The roster's contrast does not oppose this.** `emitBackgroundTaskRoster` logs no count because its only record is the *undecodable* drop; it has no success record to complete. This path has one by #1811's deliberate decision, and completing it is consistent rather than a departure.
- **It costs no content.** The content-free rule (#833's posture) bans claude-authored strings, not daemon-authored integers; `models` is already one. A fixed four-attribute set on every record also keeps the record parseable — the same reason `models` is `0` rather than absent on the non-emitting rungs.

Two docs move with it: `logControlResponse`'s "Three attributes and NOTHING else" becomes four, naming `dropped` as the second integer and restating that it is daemon-authored; `controlResponseMsg`'s "what #1811 added is `reason` and a `models` count" gains a clause for this slice.

### 5. Doc sweep (AC 4)

Every Go doc comment that says the count is unbounded or defers it to this ticket. These are the complete set; a `grep` for `1812` and for `NOT bounded` across `cmd/` and `internal/` confirms it.

| Symbol | What must change |
|---|---|
| `maxModelResolved` (`parser.go`) | "THE ENVELOPE ARITHMETIC IS PARTIAL" → the arithmetic is complete; state the aggregate product and point at `maxModelListEntries` for it. Drop "is #1812's deliverable rather than this slice's". Keep the 768 sentence as the multiplicand the cap consumes. In the amplification paragraph, drop "minus the count bound that shortens it there" — the count bound now shortens it here too — and keep the transient exposure as stated. Last sentence's "#1693, which lands after #1812" loses the stale ordering clause. |
| `emitModelList` loop head (`parser.go`) | "No count bound runs before this loop, unlike `emitBackgroundTaskRoster`'s" → the count bound that now runs, in the roster's own words (before the loop, from the tail). |
| `ModelList.Models` (`event.go`) | "The COUNT is NOT bounded in this slice … are #1812's deliverable. Until it lands, `len(Models)` is claude's to choose." → both dimensions are now bounded at construction, naming `maxModelListEntries`, with the true size recoverable as `len(Models) + DroppedModels`. |
| `ModelListPayload` (`protocol/interactive.go`) | "NOTHING COUNTS IT YET … #1690 owns making the decode record it" → the decode counts it (`turnevent.ModelList.DroppedModels`); #1693 is still where the field and that counter meet. **Doc only** — no field, no `MarshalJSON`, no test in that package changes. |
| `TestModelListPayload_RoundTrip` doc (`protocol/interactive_test.go`) | Its `dropped_models` bullet repeats "Nothing counts it yet: #1690 owns …". Same one-clause correction; the fixture's chosen `2` and every assertion stay. |

**Leave alone:** `SlashCommandListPayload.DroppedCommands`' identical-sounding paragraph (`protocol/interactive.go`) — that is #1719/#1720's list and its claim is still true; the `#1812` mentions in `initialize_capture_test.go`, which list the decodes riding the capture reader and remain accurate; `docs/knowledge/features/streamsup-package.md`, which belongs to the documentation phase.

## Concurrency model

Unchanged, and nothing here introduces state. `emitModelList` runs on the single goroutine driving `Parser.Write`; `dropped` is a local computed and consumed inside one call. The parser's only cross-line state remains `thinkingSinceEmit` (see `emitBackgroundTaskRoster`'s doc on what the parser deliberately does not remember). No goroutine, no channel, no lock, no shutdown interaction.

## Error handling

No new error path and no new failure mode; the cap is a total function of the decoded slice length.

- **Over-cap array:** entries beyond the cap are dropped from the tail, `DroppedModels` carries the count, one `ModelList` is emitted, one debug record names both numbers. Degraded but honest and self-describing.
- **Exactly at the cap:** nothing dropped, `DroppedModels == 0`. The boundary is `>`, matching `truncateField`'s `<=` convention and the roster's.
- **Under the cap, including the six-entry live capture:** untouched, `DroppedModels == 0`.
- **The three non-emitting rungs:** unreachable by the cap — it sits below them. `dropped` is `0` on their records because the attribute set is fixed, not because anything was measured.
- **Hostile line:** bounded by `defaultMaxParseBuf` before the decoder, then by the cap before retention. Transient exposure is `maxModelResolved`'s existing accepted trade, unchanged.

## Testing strategy

All of it runs under `make check`. Nothing needs `e2e_realclaude`, live claude, or a new fixture file on disk — the over-cap shape is synthesized in the test (no live claude has produced it, and a run that writes an artifact it does not `git add` throws it away with the worktree).

**New fixture material in `internal/streamsup/parser_test.go`:**

- `modelListEntriesCapFixture = 10`, a literal beside the three existing model cap fixtures, for `taskRosterEntriesCapFixture`'s stated reason: a fixture written as the constant follows a halved constant green.
- A generator mirroring `rosterEntriesFixture`: n entries built through `modelEntryFixture`, each carrying its index in `resolvedModel` so survivor identity is assertable per position.

**New test — the count bound and its report.** Table over `modelListLineFixture(t, "success", entries)` + `collectEvents`, asserting `len(list.Models)` and `list.DroppedModels`:

- one over the cap → 10 kept, 1 dropped
- exactly at the cap → 10 kept, 0 dropped (the `<=`/`>` boundary; this row is what reddens a `>=` mutant)
- a large array, 100 entries → 10 kept, 90 dropped (the row that makes the report a COUNT rather than a flag — a flag cannot tell 1 lost from 90, and it also reddens a clamped-to-1 mutant)
- survivors are claude's **first** N in order, asserted per index against the generated `resolvedModel` (this is what reddens a head-truncation mutant, `entries[len(entries)-cap:]`, which a length-and-count assertion alone passes)
- one entry under the cap (9) → 9 kept, 0 dropped, so the cap is not proven only at its own boundary

**Not** a row here: empty and absent arrays. Those return at rung 3 before the cap and are already covered by `TestParser_InitializeControlResponseRejectBranches`; duplicating them would assert the cap against inputs it never sees.

**The log assertion.** In the over-cap test, assert the emitted record's attrs are exactly `{type: control_response, reason: model_list, models: "10", dropped: "90"}`. This is the only place `dropped` is non-zero, and without it the new attribute is decorative — a mutant hard-coding `0` would stay green everywhere else.

**Amendments to existing tests:**

- `TestParser_InitializeControlResponseDecodesTheCapturedModels` — add `list.DroppedModels != 0` per arm. This is AC 3's second half, and it lands in the test that already pins the capture's six-entry shape, so a re-capture that pushed claude's list past ten would fail loudly at that existing count guard first.
- `TestParser_InitializeControlResponseRejectBranches` — each `wantAttrs` map gains `"dropped": "0"`. The map is compared with `reflect.DeepEqual`, so this is required, not optional.
- `TestParser_ModelListIsLoggedContentFree` — same one-key addition to its `wantAttrs`. Its sentinel sweep already iterates every attr, so the new attribute is swept for leaks with no further change.

**Not tested, deliberately:** the transient-memory bound (no observable to assert against, and `maxTaskRosterEntries` sets the precedent of arguing it in prose), and anything downstream of the event — no wire frame, no `turnbridge` mapping, no client.

## Open questions

- **Is 10 the right number when claude next grows its menu?** Four slots of headroom over the observed six. If a later capture shows eight or nine entries, the cap should be revisited before it starts cutting an ordinary menu — and by then `DroppedModels` is the evidence that says so, which is the point of shipping the two together. No action in this slice.
- **Whether #1693 surfaces the drop to a client as a count or as a "list is short" flag** is that ticket's call. This slice only guarantees the count exists and is true.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is unchanged and remains a single one: claude's stdout line → `Parser.Write` → `emitModelList`, decoded from the TOP-LEVEL bytes only (`streamLine`'s stated property, which is what stops a tool result whose text is literally a control_response from forging one). This ticket adds a bound *inside* the existing boundary and moves nothing across it. Downstream still holds `turnevent.ModelOption` strings that are untrusted, model-influenced text — `ModelOption.DisplayName`'s doc keeps saying so, and the render-time sanitization obligation stays the client's. The new `DroppedModels` is the first field on this variant that is **daemon-derived rather than claude-derived**, which strictly reduces the untrusted surface fraction; it is an `int` computed from a slice length, so it carries no claude bytes and cannot be steered beyond its magnitude.
- **[Network & I/O — resource exhaustion]** This is the category the ticket exists for, and the finding is the design itself: an unbounded entry count let one claude line put an arbitrary number of 768-byte entries into a value destined for a 65519-byte envelope. After the change, a whole `ModelList` is ≤ 7680 bytes of claude text (~15 KB under pathological escaping), 11.7% of the envelope. **Residual, accepted, unchanged from `maxTaskRosterEntries`:** the cap runs after `json.Unmarshal`, so the full array is materialised transiently before being shortened — order 100 MB for one pathological 4 MiB line of `{}` entries, freed immediately because `turnbridge.MapEvent` drops the event and one line is in flight at a time. `defaultMaxParseBuf` is the bound above it. This is `maxModelResolved`'s existing written trade, not a new exposure, and the spec explicitly forbids "fixing" it with a streaming decoder in this slice.
- **[Error messages, logs, telemetry]** SHOULD FIX, addressed in the spec rather than deferred: the new log attribute is the one place this change could break #833's posture ("model / effort / YOLO values are NEVER logged at any level"). The design admits **one daemon-computed integer** and no claude-authored string, on a record whose message and reason keyword are already daemon-authored constants. The enforcement is not the reviewer's care but `TestParser_ModelListIsLoggedContentFree`, whose sentinel sweep runs over *every* attribute of *every* record — including, with no edit, the new one. Code review should confirm the added attribute is `dropped` (an `int`) and not, say, the names of the dropped entries: a drop site explaining itself with the value it dropped is how this rule usually breaks, and it is the specific defect to look for in this diff.
- **[Cryptographic primitives]** Not applicable, and the reason is structural rather than incidental: this path performs no comparison against a secret and consumes no randomness. The one equality it does perform, `response.subtype == controlResponseSuccess`, compares claude's string to a daemon constant where neither side is secret, so constant-time comparison has nothing to protect.
- **[File operations]** Not applicable to the production change — no path is constructed, opened, or written. The tests read the committed captures through `capturedInitialize`, which takes an **arm selector from a closed set and mints the path itself from package constants**; the new tests must keep using that reader rather than a path, which is the property that makes a traversal impossible here by construction rather than by validation.
- **[Subprocess execution]** Not applicable. Nothing in this change reaches `exec.Command`, argv, or the child's environment; the decode is read-only with respect to the child.
- **[Concurrency]** No findings. No new state, no lock, no goroutine; `dropped` is a call-local `int`. The one thing that could have gone wrong — a shared accumulator across entries — is the per-entry `cut` closure, which this ticket does not touch and whose isolation is already pinned (and whose test only reddens with the clean entry placed *after* the cut one).
- **[Tokens, secrets, credentials]** Not applicable. No credential is read, minted, stored, or compared on this path. Worth stating rather than skipping because the payload arrives from the same child process that holds claude's session credentials: the decode target declares exactly three string fields, so a credential-shaped key appearing in a future `models` entry is dropped at the decode rather than bounded and carried — absence from the decode target being the stronger guarantee (`systemInitLine`'s argument, #1600).
- **[Threat model alignment]** The relevant threat is `docs/protocol-mobile.md`'s application-envelope size cap, which this closes for the `model_list` shape at the producer. **Explicitly out of scope and named:** the provenance gap #1811 recorded — the gate recognises a *shape* (`subtype == success` AND a non-empty `models` array), not a *correlated reply*, so a future claude putting a `models` array in some other successful control response would have it read as an inventory. This cap makes that gap's consequence strictly smaller (a bounded, reported list rather than an unbounded one) and does not close it; #1693 is where provenance starts to matter, being the first slice where a client can read the value.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
