# #1860 — Correct § `model_list`'s published claim that nothing emits the frame

**Type:** documentation only. One file: `docs/protocol-mobile.md`. No behaviour, no production logic, no new tests.

**Size:** `s`. Six boundaries re-checked against this spec: 0 production source files (`.md` is excluded by the counting rule), ~20 changed lines, 0 new exported types, 0 consumer call sites, 5 acceptance criteria, 0 reject branches.

---

## Files to read first

Read these in order. The first two are the whole job; the rest are the evidence you need so the replacement prose is true rather than merely different.

**The file being edited**

- `docs/protocol-mobile.md` § `model_list` (the `#### model_list` heading through the `**SECURITY.**` paragraph) — the section you are correcting. Read all of it: the two paragraphs below the field tables are the load-bearing edits, and the four numbered client-facing properties below them must survive untouched.
- `docs/protocol-mobile.md` § Changelog — the `2026-08-27` (#1848), `2026-08-24` (#1718) and `2026-08-22` (#1705) entries. Extract the **supersede-by-annotation convention**: the `2026-08-22` entry already opens with `**Superseded by the \`2026-08-27\` entry above** in its two inbound-validator claims — …  The rest of this entry still holds: …`. That two-clause shape — name which claims went false, then affirm what still holds — is the convention every annotation in this ticket copies.
- `docs/protocol-mobile.md` § Reconnect / Backfill semantics, the **Mode B** bullet list — read it to confirm it names only the modal (#877) and queue (#878) reconciles, and **leave it exactly as it is**. See § Non-goals.
- `docs/protocol-mobile.md` § Application message types, the `model_list` row — the summary-table row carrying the same claim.

**The code that falsified the claims** (read for truth, cite nothing by line)

- `cmd/pyry/interactive_turn_v2.go` → `Handle`, its `turnevent.ModelList` arm — #1849's emit. The arm's own comment is the single best source for this ticket's delivery-window paragraph: it states the droppable classification, the two-queues-two-answers split, and the once-per-`initialize`-exchange cadence in the daemon's own words. Read the whole comment block, not the code under it.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor`, `turnMarkNone` — the classification that makes the frame droppable at the fan-in.
- `cmd/pyry/stream_turn_drain.go` → `droppableCap` — the high-water mark a droppable envelope may not cross. Extract only that it exists and refuses under load; the arithmetic is not client-facing.
- `internal/turnevent/event.go` → `ModelList.Models`, `ModelList.DroppedModels` — extract three facts for the `dropped_models` rewrite: the entry count is bounded by `maxModelListEntries`, the list is **truncated from the tail**, and the true size **is** recoverable as `len(Models) + DroppedModels`.
- `internal/streamsup/parser.go` → `maxModelListEntries` — the cap constant. Its value is **10**; the ticket asks for that number in the prose.
- `internal/turnbridge/outbound.go` → `MapEvent`, its `turnevent.ModelList` arm — #1848's carry. Extract that `DroppedModels` is **carried verbatim, never recomputed from `len(models)`** — that is what makes the wire number trustworthy.
- `internal/e2e/relay_v2_stream_model_list_test.go` — #1845's end-to-end proof that the frame reaches a connected client. You need only that it exists and what it proves.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.RetainedModelLists`, and `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists` — #1863's connect-time seam. Extract the decisive fact: **the seam is nil in production today**, so nothing observable reaches a client through it. This is what keeps "no connect-time snapshot today" true. See § The #1863 correction.

---

## Context

`docs/protocol-mobile.md` is the client-facing wire contract. A client author reading only § `model_list` is the audience these sentences were written for, and three of its statements are false at `c7940541`:

1. **"Nothing emits this frame yet."** It is emitted. #1848 added `turnbridge.MapEvent`'s arm, #1849 added `cmd/pyry`'s `Handle` arm on the interactive turn lane, and #1845 proves it reaches a client end to end.
2. **"Nothing counts it" / "no producer enforces one yet"**, about `dropped_models`. False since #1812, and now false all the way to the wire.
3. **The delivery window is unstated** — and stating it carelessly replaces one false claim with another. What shipped is the live interactive turn lane only, and it is best-effort by construction.

Separately, the producer attribution is stale throughout: **#1693** and **#1690** are both `CLOSED / NOT_PLANNED` — split tickets, not landed work.

The precedent for this exact shape is **#1639** (`d27ec837`), which corrected the twelve announced-model "nothing emits it" claims after #1638 shipped that producer. Its `docs/protocol-mobile.md` half was 30 lines. This is the same correction for the sibling frame.

**No ADR.** This is a correction to a published contract, not a decision. Nothing here needs a decision record.

---

## The #1863 correction — read this before writing any ticket number

**The ticket body is stale on one point, and it is the point the body itself warned would go stale.** The body says #1863 and #1864 are "both **open** in Backlog". At this worktree's HEAD (`e951d9b5`):

| Ticket | State at HEAD | What landed |
|---|---|---|
| #1863 | **CLOSED / COMPLETED** — merged `540475e2` via PR #1865 | Relay-side seam only: `V2SessionConfig.RetainedModelLists` (optional, **nil in production**) and `reconcileModelLists`, drained from `handleNoiseInit`'s interactive-open tail |
| #1864 | **OPEN** | Enumeration, wiring, classification, end-to-end proof — "wires the producer" |

**The load-bearing claim survives.** #1863's own commit message: *"With the seam nil this changes no observable behaviour; #1864 wires the producer."* So a client that missed the frame still gets **nothing** on connect today. "No connect-time snapshot today" is true at HEAD.

**What changes is the attribution.** Writing "#1863 and #1864 are open" would be false the moment it was committed — reproducing precisely the `#1693` rot this ticket exists to correct.

**Decision: write the property, not the numbers.** The delivery-window paragraph states *"no connect-time snapshot today"* as a property of the wire. If a number is included at all, it is **#1864 alone**, and it must be re-derived at write time (`gh issue view 1864 --json state`) — that chain has now been re-cut three times (#1846 → #1857/#1858, #1858 → #1863/#1864, #1863 landed). The AC makes naming the slices explicitly optional; prefer omitting them.

---

## Design — the eight edit sites

Locate each site by grepping its quoted phrase, not by line number; sibling merges shift lines and the cites in the ticket body were true only at `c7940541`.

### A. Live prose — the three "nothing emits" sites (AC 1)

**A1. § Application message types, the `model_list` row.** Currently ends `…fixtures and section by #1705; nothing emits it yet (#1693).`

Replace the trailing clause so the row names the slices that shipped it. Required: the frame **is** emitted; the producer credit goes to **#1848** (the mapping) and **#1849** (the emit), with **#1845** as the end-to-end proof. Keep the row's existing structure — the declare-then-emit credits to #1704 and #1705 stay, because they are true. Keep the `model_announced` contrast sentence and the § Interactive events link.

**A2. § `model_list`, the paragraph beginning `**Nothing emits this frame yet.**`** This paragraph is rewritten, not annotated — it is live prose. The replacement must:

- Open by stating the frame is emitted, reversing the current lead sentence.
- Keep the declare-then-emit lineage intact: shape #1704, fixtures and section #1705, mapping #1848, emit #1849, proof #1845.
- Keep the two sibling precedents (`rate_limited` #1405/#1410, `model_announced` #1616/#1638) — both are still true, and they are what makes this frame's own sequencing legible. Note that `model_list` has now **completed** that sequence, where the two precedents are cited as having completed theirs.
- **Carry the delivery window** — see § B, which is the same paragraph's second half or its immediate successor. Whether it is one paragraph or two is the developer's call; both must be adjacent and both must be in § `model_list`.

**A3. § `slash_command_list`, the sequencing sentence.** Currently reads `…and `model_list` used (#1704 declared, #1693 emitting); this is the fourth instance.`

Only the **embedded `model_list` precedent citation** changes: `#1693 emitting` becomes the slices that actually emitted it. The sentence's own claim — `**Nothing emits this frame yet.**` about `slash_command_list` — **stays**, and so does its producer #1720, verified `OPEN` at HEAD.

> **Count discipline.** `this is the fourth instance` must still read **fourth**. Re-pointing a precedent's ticket numbers does not add or remove a precedent: the list stays #1405/#1410, #1616/#1638, #1704/#1848+#1849, with `slash_command_list` itself fourth. This file is count-sensitive throughout — do not let a re-pointed citation move a numeral.

### B. The delivery window (AC 2) — the part that is easiest to get wrong

§ `model_list` must state what a client actually gets. Four claims, the middle two load-bearing:

1. **Where.** Emitted on the **live interactive turn lane** — `cmd/pyry`'s `Handle` arm, #1849. It arrives on a `control_response`, as the section already says; that framing is unchanged.
2. **When.** **Once per `initialize` exchange.** The daemon's cadence is one such exchange per claude child, so a client sees one frame per child — and a session rotation starts a new child, hence a new exchange. Nothing bounds the rate on either side, deliberately.
3. **Best-effort rather than guaranteed.** *(load-bearing)* The frame is classed **droppable at the fan-in**: `turnMarkFor` answers `turnMarkNone`, so `streamTurnSink`'s `sinkFor` may refuse it at `droppableCap` under load. State this as a client-facing consequence — **a client can miss the frame entirely on a busy session, and missing it is not an error** — not as an internals tour. Do not name `droppableCap` or `sinkFor` in the published prose; they are daemon internals a client cannot observe. Name the *property*.
4. **No connect-time snapshot today.** *(load-bearing)* A client that missed the frame — dropped under load, or connected after the exchange — has **no way to ask for it** and will not be sent one. Say so plainly, because the absence is the actionable part: a client must not block its model menu on a frame that may never arrive, and must render a usable UI without one.

**Do not** promise the gap will close, name a date, or imply a client should wait. Naming #1864 is optional (§ The #1863 correction); the property is mandatory.

### C. `dropped_models` (AC 3) — two sites

**C1. The field-table row.** Currently `…`0` when nothing was dropped. **Nothing counts it yet** — see below.` Replace the bolded clause with the positive statement: the count is real, and the paragraph below explains the cap. Keep the row's first sentence and the `see below` pointer.

**C2. The paragraph beginning `**\`dropped_models\` does not yet mean what its name promises…`.** Rewritten wholesale. It currently makes three claims that are all now false — nothing counts it, `#1690`/`#1693` own the future work, and no producer enforces a cap. The replacement states:

- The entry count **is** bounded: `maxModelListEntries = 10` in `internal/streamsup`, landed by **#1812**. Do not name the Go constant in client-facing prose *as a constant a client can read* — name the number (**10**) and note it is a daemon-side producer cap, subject to change without a wire change, so a client must never hardcode it.
- The list is truncated **from the tail**, so the entries a client receives are claude's first ten in claude's own order.
- The path to the wire is complete: the decode counts the overflow, `turnbridge.MapEvent` carries the count **verbatim rather than recomputing it**, and `cmd/pyry` emits it.
- Therefore the sentence that currently forbids it is **inverted**: `len(models) + dropped_models` **is** the menu's true size. This is the single most useful correction in the ticket for a client author — the old text told them not to do the one arithmetic that now works.

Both #1690 and #1693 disappear from this paragraph. Neither exists as work; both are `CLOSED / NOT_PLANNED` splits.

### D. Changelog annotations (AC 4) — three annotations plus one new entry

Follow the existing convention exactly: **annotate, never rewrite.** A dated entry is a record of what was true when written; it is corrected by a leading supersede clause naming which claims went false and affirming what still holds.

| Entry | What went false | Annotation must name |
|---|---|---|
| `2026-08-27` (#1848) | *"…so **nothing emits this frame yet** remains true; that's the sibling transport slice."* | That sentence specifically. This is the file's own most recent record of the claim, and it was written in the window between #1848 landing and #1849 landing — the annotation should say so, because the entry was **correct when written**. The rest of the entry (the `truncated_fields` enumeration, the 1:1 carry) still holds. |
| `2026-08-24` (#1718) | Its `model_list` precedent citation, *"#1704 ahead of #1693"* | The precedent citation only. Everything else in this very long entry still holds — including its `dropped_commands` clause, which is about the **sibling** frame and remains true. |
| `2026-08-22` (#1705) | *"the producer is #1693"* and *"nothing counts it yet"* | Both. **This entry already carries a supersede annotation** for its two inbound-validator claims — **extend that existing annotation**, do not add a second one beside it. Its "the rest of this entry still holds" list must be adjusted so it no longer affirms the two claims now being superseded. |

**A new dated entry records this correction.** It goes at the **top** of § Changelog (newest first) as a **third** `2026-08-27` entry. It should read in the file's house style: what was false, what is true now, which slices shipped it, the delivery window with its two load-bearing properties, and the `dropped_models` inversion. State explicitly that **no live count moved** and that **`model_list` was deliberately not added to Mode B** — a future reader needs to know that absence is a decision, not an oversight.

---

## Non-goals — things a careful reader will be tempted to fix

Each of these is excluded **by design**, not by oversight. Touching any one of them fails review.

- **Do not add `model_list` to § Reconnect / Backfill's Mode B list.** #1863's commit message describes itself as the *"Third Mode B instance, in the shape of `reconcileModals` (#877) and `reconcileQueues` (#878)"* — that is a statement about relay internals, and its seam is nil, so **nothing reaches a client**. Mode B in this document is a published client contract. It gains a third bullet when #1864 makes it observable, and that is #1864's edit, not this one.
- **Do not touch the `dropped_commands` row or paragraph in § `slash_command_list`.** They make the identical "nothing counts it" argument for the sibling frame and a grep for the phrasing hits them. Verified at HEAD: `internal/streamsup/parser.go` has **no** slash-command cap constant and `internal/turnevent` has **no** `DroppedCommands` field. Nothing counts it — the claim is **true**.
- **Do not "fix" #1719's attribution there either.** #1719 shows `CLOSED`, which is a split and not a landing (`not_planned` → #1824/#1825/#1826, #1824 split again); the bound now lives in **#1826**, `OPEN` at HEAD. That attribution is stale in the same way #1693 was, but it is **out of this ticket's scope** — see § Open questions.
- **Do not touch § `slash_command_list`'s or § `attachment_chunk`'s own "nothing emits it yet" statements.** Both frames genuinely have no producer (#1720; #1741/#1743/#1744/#1746). Only the `model_list` precedent citations embedded in them are re-pointed.
- **Do not touch the second `2026-08-27` entry** (#1838, `set_session_settings`). It carries no `model_list` emission claim. It does reference the `2026-08-22` entry, but on the inbound-validator axis — unrelated.
- **Do not move any count.** "fifteen" turn-stream events, "five" `system` subtypes, "fourth instance" — none of them change. This frame is not a `turnevent` variant on the turn stream and nothing about this correction adds or removes a documented frame.
- **This file only.** `docs/knowledge/features/*.md` carry matching `#1693` references and "no producer" prose. They belong to the documentation phase, which folds this work in after code review. `docs/specs/` is likewise out of scope, and the in-tree Go comments carrying the same claims are separate open slices (**#1861** `internal/protocol`, **#1862** `internal/streamsup` + `internal/turnevent`).
- **Do not touch the four numbered client-facing properties or the `**SECURITY.**` paragraph** in § `model_list`. They are unaffected by emission.

---

## Concurrency model

None. Documentation only — no goroutines, no shutdown sequence, no runtime behaviour.

The prose does *describe* a concurrency property (the fan-in's droppable classification), but it describes existing behaviour rather than introducing any.

## Error handling

None. No failure modes and no recovery paths — nothing executes.

---

## Testing strategy

**No new tests.** No production code changes, so no test can observe this ticket. Do not add one; a test asserting on markdown prose would be the wrong instrument and is not a pattern this repo uses.

Verification is by inspection, in three passes:

**Pass 1 — the claims are gone from live prose.** After the edit, a grep for the falsified phrasings across the file returns **only** annotated changelog entries and the genuinely-true sibling-frame sites:

```bash
grep -n "1693\|1690\|nothing emits\|Nothing emits\|Nothing counts\|nothing counts\|no producer enforces" docs/protocol-mobile.md
```

Expected surviving hits, and nothing else:
- § `slash_command_list`'s and § `attachment_chunk`'s own "nothing emits"/"nothing emits, accepts or enforces" statements (summary rows and section prose) — true.
- § `slash_command_list`'s `dropped_commands` row and paragraph ("Nothing counts it", "no producer enforces one yet") — true for the sibling frame.
- Changelog entries carrying `#1693`/`#1690`, each now under a supersede annotation.

Any live-prose hit in § `model_list` or in the `model_list` summary row is a failure.

**Pass 2 — nothing else moved.** `git diff` should touch **only** `docs/protocol-mobile.md`, and within it only the eight sites above plus the new changelog entry. Confirm § Reconnect / Backfill semantics is **absent from the diff entirely**.

**Pass 3 — the counts and links still hold.** Confirm "fifteen", "five" and "fourth instance" are unchanged, and that every markdown anchor the edited passages link to (`#model_list`, `#slash_command_list`, `#model_announced`, `#set_session_settings`, `#interactive-events-v2-capability-gated`) still resolves — the `#### model_list` heading itself must not be renamed.

**Gate.** Run `make check`. It is unaffected by a docs-only change, but it is the pipeline's standard gate and a green run is the evidence that nothing was touched that compiles. `qmd update && qmd embed` is **not** this ticket's job — the documentation phase runs it.

---

## Open questions

1. **One paragraph or two for the delivery window?** § B's four claims can land as an extension of the rewritten "nothing emits" paragraph or as its own paragraph immediately after. Developer's call — the AC constrains the content, not the paragraphing. A separate paragraph is probably clearer, since "where and when you get it" is a different question from "who built it".

2. **Should the new changelog entry name #1864?** § The #1863 correction recommends **no** — state the property, omit the number. If the developer includes it, `gh issue view 1864 --json state,stateReason` must be re-run immediately before writing, and the number dropped if it has been split or closed. This chain has been re-cut three times.

3. **§ `slash_command_list`'s stale #1719 attribution is a real defect, deliberately out of scope here.** The `dropped_commands` paragraph credits #1719 and #1720 with owning the bound; #1719 is a `not_planned` split and the bound now lives in #1826. The *claim* it supports ("nothing counts it") is true, so the paragraph is not misleading about behaviour — only about ownership. It is the exact rot class this ticket exists to correct, one frame over. **Worth filing as a follow-up ticket after this lands**; folding it in here would widen a single-frame correction into a two-frame one and put the developer inside the "do not fix the sibling" guardrail they were just told to respect.

4. **The in-tree `cmd/pyry` comment naming #1846 as "the reliable path" is now stale too** (#1846 closed, the work re-cut into #1863/#1864). It is not covered by #1861 (`internal/protocol`) or #1862 (`internal/streamsup`, `internal/turnevent`), so no open slice owns it. Out of scope here — this ticket is `docs/protocol-mobile.md` only — but worth a follow-up alongside question 3.
