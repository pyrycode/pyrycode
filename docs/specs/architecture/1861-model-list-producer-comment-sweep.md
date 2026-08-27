# #1861 — Re-point `internal/protocol`'s `model_list` "nothing constructs it" claims at the slices that shipped

**Size:** `s` (2 production source files, comments only, no behaviour and no assertion changes)

## Files to read first

Read these before editing. The first three are the evidence for every claim this spec makes about what shipped; the rest are the sites themselves.

- `internal/turnbridge/outbound.go` → `MapEvent`, its `turnevent.ModelList` arm — **the only site that fills the payload's fields.** Extract: it builds a fresh outer `[]protocol.ModelOption`, carries `DroppedModels` verbatim from `e.DroppedModels`, and supplies `ConversationID` from `tc.ConversationID`. This is #1848.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`, its `turnevent.ModelList` arm — **the emission.** Extract: it flushes the pending delta and calls `emitMapped` (so the wire type comes from `MapEvent`'s return, not from a `protocol.TypeModelList` literal in `cmd/pyry`), with no turn-lifecycle mutation. This is #1849.
- `cmd/pyry/session_model_list.go` → `resolveBoundModelList` — **the second production path that hands a caller the type.** Extract: it resolves a conversation's bound session, reads the retained `turnevent.ModelList`, and routes it **through `turnbridge.MapEvent`**; every non-zero return comes from that same arm. This is #1857. Read the body before writing the word "constructor" — see § Design, "One filler, two production paths".
- `internal/protocol/codes.go` → the `TypeModelList` const block's doc comment, plus the `TypeSlashCommandList` and `TypeAttachmentChunk` blocks' closing precedent sentences — 4 of the 15 sites.
- `internal/protocol/interactive.go` → `ModelListPayload` (type doc, incl. the `DroppedModels` paragraph), `ModelOption.MarshalJSON` (doc), `SlashCommandListPayload` (type doc) — 5 sites.
- `internal/protocol/interactive_test.go` → `TestModelListPayload_NilModelsNormalises` (doc), `TestModelListPayload_RoundTrip` (doc + the inline comment above its `DroppedModels` assertion) — 4 sites.
- `cmd/pyry/relay_guard_test.go` → the `excludedTypes` `"TypeModelList"` entry's comment — 2 sites.
- `docs/knowledge/features/protocol-package.md` § "Model-list payload" and its `TypeModelList` entry — **read-only, do not edit.** Extract: the corrected producer story already written out (#1848 maps, #1849 emits, `len(Models) + DroppedModels` **is** the menu's true size on the wire), and the settled push wording the overview already uses: *"if a future ticket picks request/reply it declares the verb with its handler and this constant's `excludedTypes` classification moves from `push` to `reply`"*. That sentence is the model for AC 3.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — no new line citations in any rewritten comment. `make cite-guard` is diff-scoped and will fail on one you add.

**Precedent commit, worth reading in full:** `d27ec837` (#1639) did this exact sweep for `model_announced` after #1638 shipped that producer. Its `internal/protocol` hunks show the target register — *"The declaring ticket (#1616) **was** wire vocabulary only; #1638 added … so this frame now reaches an interactive v2 mobile client"* — and it left the `#1405→#1410` style arrow lists intact in shape, changing only the numbers that were wrong.

## Context

`internal/protocol` declares the `model_list` wire vocabulary and payload. Its comments were written when the frame had no producer, and 15 `#1693` references across four files still describe that world. `#1693` was split and no longer exists as work; three separate slices shipped what it was going to do, and three of the sites are not attributions at all but statements about the present that are now false.

The failure mode this closes is concrete: an engineer reading `ModelListPayload` today is told nothing constructs it and plans work around a frame that already reaches clients.

**No ADR is warranted.** This is a correction sweep with no new decision. The documentation phase should note in `docs/knowledge/features/protocol-package.md` that the Go doc comments now agree with the overview — the overview is already correct and is out of scope here by design.

### The three shipped slices, verified at `b9731050`

| Slice | Commit | What it ships | The word for it |
|---|---|---|---|
| **#1848** | `45374455` | `turnbridge.MapEvent`'s `turnevent.ModelList` arm | the **mapping** — the only site that fills the payload's fields |
| **#1849** | `dd6abb67` | `interactiveTurnEmitterV2.Handle`'s arm, via `emitMapped` | the **emission** — and what settled `TypeModelList` as a push |
| **#1857** | `e7e90d0d` | `resolveBoundModelList` | a second **production path** that yields the type, delegating the fill to #1848 |

Verified absent at `b9731050`: no `"model_list"` entry in `cmd/pyry/relay.go`'s `Handlers` map and none in `internal/relay/v2session.go`'s `dispatchAppFrame` switch. The push classification is correct and stays.

## Design

### One filler, two production paths

The ticket body calls `resolveBoundModelList` "a second production constructor". Read literally that over-claims, and writing it into a comment would commit this ticket's own bug: `resolveBoundModelList`'s only non-zero return comes from `turnbridge.MapEvent`, and its other six returns are `protocol.ModelListPayload{}` refusals. Exactly one site fills the fields.

Both facts are true and both matter to a reader of `internal/protocol`, so state them as they are. The shape that holds:

> `turnbridge.MapEvent`'s `turnevent.ModelList` arm constructs it (#1848), and `cmd/pyry`'s `resolveBoundModelList` (#1857) is a second production path that routes a session's retained list back through that same arm.

Do **not** write "two constructors". Do **not** write "#1848 is the only production site" either — that erases #1857 and under-counts the same way the old comment did.

### Site inventory

Fifteen references, four classes. Anchors are the declaration whose doc comment carries the text; find each by grepping the quoted phrase. Class **F** (false claim about the present) and class **N** (no owner) require rewriting the sentence; class **A** (attribution) and class **P** (precedent arrow) need the number changed and the tense checked.

#### `internal/protocol/codes.go`

| # | Anchor | Phrase to find | Class | Target |
|---|---|---|---|---|
| 1 | `TypeModelList` block | *"If #1693 picks request/reply it declares the verb together with its handler and moves this constant from push to reply"* | **F** (AC 3) | Settled — see § "The settled push proposition". Keep the preceding *"filing it under excludedTypes to dodge that would be a lie to the guard"* argument **verbatim**; it is still true. |
| 2 | `TypeModelList` block | *"The declaring ticket (#1704) is wire vocabulary only: #1693 produces and emits the frame"* | **A** | #1848 maps and #1849 emits — one clause each. #1639's rewritten form is the register to copy. |
| 3 | `TypeSlashCommandList` block | *"…#1616→#1638 and #1704→#1693."* | **P** | `#1704→#1848`. |
| 4 | `TypeAttachmentChunk` block | *"…#1616→#1638 and #1704→#1693."* | **P** | `#1704→#1848`. |

On sites 3 and 4: the arrow means declare → the slice that added the turnbridge mapping. That is what `#1405→#1410` and `#1616→#1638` both are (`relay_guard_test.go`'s neighbouring entries spell them out as *"the turnbridge mapping is #1410"* / *"#1638"*), so #1848 is the structural analogue, not #1849.

On site 2: the same sentence continues *"and #1705 adds the encoding fixtures and the docs/protocol-mobile.md § model_list section"*. #1705 has shipped — `internal/protocol/testdata/model_list.json` exists and `TestModelListPayload_RoundTrip` reads it. Recasting that clause to past tense as the sentence gets rewritten is fine; hunting for other tense slips elsewhere is scope creep.

#### `internal/protocol/interactive.go`

| # | Anchor | Phrase to find | Class | Target |
|---|---|---|---|---|
| 5 | `ModelListPayload` (type doc) | *"Nothing in the tree constructs this type yet; #1693 is what will."* | **F** (AC 1) | Both production paths, per § "One filler, two production paths". The paragraph is about declaring ahead of a producer, so naming #1849's emission here is permitted but not required. |
| 6 | `ModelListPayload` (`DroppedModels` para) | *"Nothing joins the two yet — #1693 is where the field and that counter meet, this type still having no constructor."* | **F** (AC 1) | #1848 joined them: `MapEvent`'s arm carries `turnevent.ModelList.DroppedModels` verbatim and never recomputes it from `len(Models)`. Both halves of the old sentence are false — fix both. |
| 7 | `ModelOption.MarshalJSON` (doc) | *"an undeclared position is one #1693 would have to invent"* | **A** | #1848. Tense: the mapping shipped, so *"would have had to invent"*. |
| 8 | `ModelOption.MarshalJSON` (doc) | *"#1693's mapping has no fork to bridge"* | **A** | #1848, same sentence as site 7. The mapping exists now, so the claim is a measured fact rather than a prediction — *"#1848's mapping needed no fork to bridge"* or equivalent. |
| 9 | `SlashCommandListPayload` (type doc) | *"#1616 ahead of #1638 and #1704 ahead of #1693."* | **P** (AC 5) | `#1704 ahead of #1848`. **The very next sentence — *"Nothing in the tree constructs this type yet; #1720 is what will"* — stays untouched.** It is about `SlashCommandListPayload`, and it is still true. |

#### `internal/protocol/interactive_test.go`

| # | Anchor | Phrase to find | Class | Target |
|---|---|---|---|---|
| 10 | `TestModelListPayload_NilModelsNormalises` (doc) | *"A producer (#1693) mapping an empty or absent claude models array would hand this type a nil slice"* | **A** | #1848 — and the claim is now literally true of the shipped arm, which builds `models` by `append` into a nil slice, so an empty `e.Models` leaves it nil. Making it concrete is welcome; keeping the conditional shape is acceptable. The same paragraph's *"this slice (#1704) ships no fixture at all"* is a fact about what #1704 shipped and stays. |
| 11 | `TestModelListPayload_RoundTrip` (doc, `resolved_model` bullet) | *"#1693 measures the initialize reply's own per-entry keys and replaces the four sentinels."* | **N** (AC 4) | **No live owner.** The four `<unmeasured>` sentinels are still in `internal/protocol/testdata/model_list.json` (stored with both angle brackets `\u`-escaped by Go's encoder, so a grep for the literal `<unmeasured>` reports the fixture clean when it is not — grep `unmeasured` on its own), and none of #1848/#1849/#1857 touched testdata. Say the measurement has not happened and no slice owns it. **Naming #1848, #1849, #1857 or #1858 here fails AC 4.** |
| 12 | `TestModelListPayload_RoundTrip` (doc, `dropped_models` bullet) | *"#1693 is where the field and that counter meet."* | **A** | #1848. Same fact as site 6; keep the two consistent. The *"counts it since #1812"* clause is correct and stays. |
| 13 | `TestModelListPayload_RoundTrip` (inline, above the `DroppedModels` assertion) | *"Nothing fills this field on a real frame yet — no producer builds this type until #1693 — so a client cannot read len(models) + dropped\_models as the menu's true size today"* | **F** (AC 1) | False on every clause. #1848 fills it from the decode and #1849 puts it on the wire, so `len(models) + dropped_models` **is** the menu's true size on a real frame. The surrounding bullet's *"2 is chosen rather than captured"* framing stays, and the assertion below is untouched. |

#### `cmd/pyry/relay_guard_test.go`

| # | Anchor | Phrase to find | Class | Target |
|---|---|---|---|---|
| 14 | `excludedTypes`, `"TypeModelList"` entry | *"from the moment something emits it (the producer is #1693)"* | **A** | Name the shipped slices in the neighbours' idiom (*"the turnbridge mapping is #1410"* / *"#1638"*). Mapping and emission are split here, so both belong: the mapping is #1848, the emission #1849. |
| 15 | same entry | *"If #1693 picks request/reply, the verb and its handler land together and this entry becomes \"reply\"."* | **F** (AC 3) | Settled — the same proposition as site 1, said the same way. |

Untouched in that file: the map value `"TypeModelList": "push"` (an assertion input — changing it is a behaviour change), and the whole `"TypeSlashCommandList"` entry below it, whose *"the producer is #1720"* and *"If #1720 picks request/reply"* are still open and still true.

### The settled push proposition (sites 1 and 15)

AC 3 requires both sites to read as settled and to say the same thing. The shared proposition, all of it verified at `b9731050`:

> `TypeModelList` shipped as a **push**. #1849 emits it from `interactiveTurnEmitterV2.Handle` on the interactive turn lane; no inbound request verb was ever declared for it, and there is no `"model_list"` entry in `cmd/pyry/relay.go`'s `Handlers` map or `internal/relay/v2session.go`'s `dispatchAppFrame` switch — so the `excludedTypes` entry stays `"push"`.

Wording constraints:

- The outcome is a **fact**, not an open question. A trailing note that a future request/reply verb would still have to land its handler alongside it is allowed — `protocol-package.md` phrases exactly that and reads as settled — but it must name **no ticket number** and must not present the classification as undecided.
- `codes.go`'s site closes with *"a client's decode path is the same frame either way, which is what declaring the shape now exists to freeze."* The "either way" belongs to the conditional being removed; recast it (the decode path is the same frame whatever a later verb does, which is what declaring the shape ahead of the producer bought) rather than leaving a dangling half-conditional.
- The two need not be word-identical — one is a const doc, one a map-entry comment — but a reader must not be able to find daylight between them.

## Concurrency model

None. No code executes differently after this change.

## Error handling

None. No error path is touched.

## Testing strategy

No new tests, no changed assertions, no changed fixtures. `make check` is the gate — it proves the four files still compile and vet clean, which is the whole risk surface of a comment edit.

The ACs are the real verification, and each is a deterministic check rather than a judgement:

- **Comments only** (the money check). `git diff -U0 origin/main...HEAD -- <the four files>`, keep only `+`/`-` lines that are not `+++`/`---`, drop every line matching `^[+-][[:space:]]*//`. The remainder must be **empty**. A non-empty remainder means a code line moved and the ticket's central constraint broke.
- **Scope.** `git diff --name-only origin/main...HEAD` lists exactly the four in-scope files plus this spec. Nothing under `docs/knowledge/`, nothing in `docs/protocol-mobile.md`, nothing in `internal/streamsup`, `internal/turnevent` or `internal/e2e` (those `#1693` references are #1862's), nothing in `internal/protocol/testdata/`.
- **AC 2.** `git grep -F '#1693'` over the four files → zero hits. `git grep -F '#1858'` over the four files → zero hits (it is clean today; keep it that way).
- **AC 1.** Grep the four files for `Nothing in the tree constructs`, `no producer builds`, `Nothing joins the two`, `having no constructor`, `Nothing fills this field`. **Exactly one hit may survive** — `SlashCommandListPayload`'s in `interactive.go`. Any hit in a `model_list` context is a failure.
- **AC 3.** `"TypeModelList": "push"` still present in `relay_guard_test.go`. Read sites 1 and 15 side by side.
- **AC 4.** `internal/protocol/testdata/model_list.json` unchanged, and the four `<unmeasured>` entries still in it. Site 11's note names none of #1848/#1849/#1857/#1858.
- **AC 5.** `NOTHING COUNTS IT YET` and `#1720 is what will` both still present in `interactive.go`.
- **`make cite-guard`** (folded into `make check`) — green. Add no `file.go:NNN`, no range, no bare `:NNN` to any rewritten comment. Name the symbol.

`make preship` is not required: no test file is deleted or moved, and nothing here reaches `internal/e2e/realclaude`.

## Open questions

- **`cmd/pyry/session_model_list.go` carries a live `#1858` reference** in `resolveBoundModelList`'s body comment (*"#1858 names the type itself when it builds the envelope"*), and #1858 was closed as `not planned`. That is the same defect class this ticket fixes, in a file outside the four in scope. **Do not fix it here** — it is not in any AC and the file is not in scope. Worth a follow-up ticket; flagging it in the PR description is enough.
- **Site 2's `#1705` clause** and `ModelListPayload`'s opening *"that section lands with the fixtures in #1705"* are future-tense about work that shipped. Neither carries `#1693` and neither is in an AC. Correcting the one inside a sentence you are already rewriting is fine; going after the other is not.
- **Register for the class-**F** rewrites.** #1639's hunks are the house style and land on "X used to be true; #N changed it, so Y is true now" rather than deleting the history. Prefer that to a terse replacement — these comments carry argument, and the argument is what a reader needs.
