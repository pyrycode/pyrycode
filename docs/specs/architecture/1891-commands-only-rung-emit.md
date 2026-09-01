# #1891 — emit the slash-command list when no model list rides with it

`internal/streamsup`'s `emitModelList` classifies one top-level `control_response` line
onto five rungs. Rung 4 — a success carrying a non-empty `commands` array and no
`models` — logs `controlResponseCommandsOnly` and emits nothing. This slice gives it
the emit.

Both prerequisites have landed. #1886 moved the construction into `emitSlashCommandList`,
whose empty-input gate is the emitter's own precondition, so a second call site inherits
the suppression. #1890 gave the rung its keyword. **What is left is one call and the
classification's prose.**

---

## Files to read first

| Read | Symbol | What to extract |
|---|---|---|
| `internal/streamsup/parser.go` | `emitModelList` | The whole doc and body. `FIVE RUNGS`, `THE DISCRIMINANT`, the header sentence, rung 4's inline `Still no event` comment, and rung 5's `THE SECOND GATE` / `THE ORDER IS DELIBERATE` — this is most of the change. |
| `internal/streamsup/parser.go` | `emitSlashCommandList` | The emitter you call. Read `THE GATE IS THIS EMITTER'S PRECONDITION`, `IT LOGS NOTHING`, `SUPPRESSION RATHER THAN AN EMPTY EMIT`, `WHERE THE CONSTRUCTED LIST GOES`. **You point at these; you do not copy or re-decide them.** |
| `internal/streamsup/parser.go` | `logControlResponse` | Its `commands` paragraph, plus the two sentences that partition rungs by the words *non-emitting rung* / *the non-emitting ones*. |
| `internal/streamsup/parser.go` | `controlResponseMsg` | The keyword `const` block above it: `WHAT DECIDES THE NEW KEYWORD'S NAME`, and `controlResponseCommandsOnly`'s trailing one-line gloss. |
| `internal/streamsup/parser.go` | `commandEntryLine` | Its sink enumeration and `A PER-FIELD CAP DOES EXIST`. **Verify, then leave alone** — see § Checked non-items. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseAckReportsTheCommandCount` | AC 4's home. Doc, three rows, and the table-wide `len(events) != 0`. Renamed and restructured here. |
| `internal/streamsup/parser_test.go` | `TestParser_ModelListIsLoggedContentFree` | Its `lines` slice, the `FIVE:` assertion, and the `leaks` sweep. |
| `internal/streamsup/parser_test.go` | `TestParser_SlashCommandListIsSuppressed` | Its opening paragraph and `THE COMMANDS-ONLY RUNG IS THIS TABLE'S SIXTH ROW AND IT LIVES ELSEWHERE`. |
| `internal/streamsup/parser_test.go` | `TestParser_SlashCommandFieldsAreCapped` | The `models array is carried by every row` paragraph only. Rows untouched. |
| `internal/streamsup/parser_test.go` | `TestParser_InitializeControlResponseRejectBranches` | Read to confirm it needs nothing — see § Checked non-items. |
| `internal/streamsup/parser_test.go` | `TestParser_ControlResponseAckIsConsumedSilently` | Same: read to confirm, change nothing. |
| `cmd/pyry/interactive_turn_v2.go` | `eventKind` | Its `turnevent.SlashCommandList` arm, `A PRODUCTION PRODUCER NOW EMITS THE VARIANT` paragraph. |
| `internal/turnevent/event.go` | `SlashCommandList` | Read `THE PRODUCER HAS SINCE ARRIVED` to confirm it already says *"more than one call site reaches it"*. It does. Change nothing. |
| `docs/knowledge/features/streamsup-package.md` | § *The commands-only success gets its own keyword (#1890)* | The lesson that drives § The sweep you must run: an arity claim rots by its **class word**, not by the rung's name. |

---

## Context

`emitModelList` reads two independent arrays off one line but decides both from one
conjunction. #1877 added the `commands` construction on rung 5, under a second gate
reachable only because `models` was non-empty; #1886 extracted it; #1890 gave the
models-less case its own rung and keyword but left it non-emitting. So today a payload
carrying a slash-command inventory and no model list is classified, counted and logged —
and dropped.

**No ADR is warranted.** The decisions this touches were taken in #1877 (verbatim names,
per-field cap), #1886 (the emitter's precondition-as-gate), and #1890 (the keyword set),
and each is already argued at the symbol that owns it. This slice takes no new decision;
it makes an existing one reachable from a second rung.

**The fixture is hand-built and that is settled.** All three responding arms of
`initialize_control_v2.1.239.json` and its two siblings carry both arrays; the fourth
recorded no `control_response`. The commands-only half cannot be pinned from committed
bytes.

---

## Design

### The production change is one statement

Inside `emitModelList`'s rung 4 — the `commands != 0` branch of the empty-models block —
add one call between the existing `logControlResponse` and the existing `return`:

```go
p.logControlResponse(controlResponseCommandsOnly, 0, 0, 0, commands)
p.emitSlashCommandList(cr.Response.Response.Commands)
return
```

Three properties, each load-bearing and each verifiable by reading those three lines:

- **Below the log.** This is AC 2's structural half. `emitSlashCommandList`'s
  `IT LOGS NOTHING` paragraph rests on the record being written by `emitModelList`
  *before* the call; placing the call above the log falsifies it at the new site.
  **Check this rather than assume it** — then leave that paragraph unedited, because
  "this call" already reads correctly for two call sites.
- **Inside rung 4, not below the block.** Falling through to the shared tail would run
  the models loop and emit a `ModelList`, which AC 1 forbids.
- **`cr.Response.Response.Commands`, not a re-decode.** Same expression rung 5 passes.
  No second `json.Unmarshal`, no second loop, no second cap.

**Nothing else in the body moves.** The `commands := len(...)` placement, the
empty-models block's structure, the entry-count cap's absence and the models loop are
all unchanged.

### The classification's shape — what the prose must now assert

This is the bulk of the work. Each item below states the **contract** the corrected
paragraph must satisfy; the wording is yours.

#### 1. `THE DISCRIMINANT` — restate as a two-array lattice

The current paragraph says the gate is CONJUNCTIVE: `subtype == controlResponseSuccess`
AND a non-empty decoded `models`. That stops being the function's discriminant the
moment either array can reach an emit on its own. The replacement must assert:

- **One shared precondition, two independent decisions.** `subtype == controlResponseSuccess`
  is the precondition of *both* emits. Below it the arrays are read independently: a
  non-empty `models` decides the `ModelList`, a non-empty `commands` decides the
  `SlashCommandList`, and neither gates the other.
- **The subtype half's existing argument survives verbatim** and both halves of it still
  hold — it stops a payload being read out of a reply that reported FAILURE, and it is
  what makes the classification total, every non-success subtype including an absent one
  landing on the nak rung.
- **The models half's exclusion argument must be scoped and then re-stated per array.**
  *"already excludes all three sibling shapes on record"* is a claim about which KEYS the
  recorded shapes carry, so it does not transfer by inheritance. Scope it to the models
  emit, then state the commands equivalent explicitly: the `set_permission_mode` success
  (`{"mode":"default"}`), the `set_permission_mode` NAK (an `error` string, no inner
  response) and the interrupt ack (no inner response at all) carry no `commands` key
  either, so the same exclusion holds for the second emit — established per key, not
  borrowed.
- **The emit/non-emit partition of the rungs moved.** Rungs 1–3 emit nothing; rung 4
  emits one `SlashCommandList`; rung 5 emits one `ModelList` and, under its second gate,
  at most one `SlashCommandList` beside it. So exactly **one** rung can emit a `ModelList`
  and **two** rungs can emit a `SlashCommandList`.

Keep this to roughly the length it is today. It is the item most likely to balloon; if
it is growing past the paragraph it replaces, the surplus is argument that already lives
at `emitSlashCommandList` and should be a pointer instead.

#### 2. The function header sentence

*"emits AT MOST ONE `turnevent.ModelList` and, on that same rung and under a SECOND
independent gate, AT MOST ONE `turnevent.SlashCommandList`"* — *"that same rung"* is
false. It must say: at most one `ModelList`, from one rung; at most one
`SlashCommandList`, from either of two rungs; the two decided independently under one
shared subtype gate.

#### 3. `FIVE RUNGS` — outcome clauses only

**#1890 already re-authored the enumeration's structure. Do not re-do it.** The header
stays `FIVE RUNGS`; rungs 1, 2, 3 and their arguments are untouched.

- **Rung 4's bullet** gains the emit, loses `no event`, and loses its closing clause
  *"this rung is where a slice adding the slash-command emit lands without moving a
  keyword"* — that slice is this one. Its `KEYWORD alone` sentence needs care: the rung
  no longer differs from rung 3 in the keyword alone, it differs in the keyword **and**
  the emit. What survives is that the **models half is read identically**, which is what
  lets rung 3's false-negative asymmetry stay a single copy.
- **Rung 5's bullet**: the trailing sentence *"A non-empty `commands` with NO models does
  not reach here at all — it is the controlResponseCommandsOnly rung above"* stays true
  about where the payload lands and must stop implying that rung emits nothing.
- Point rung 4 at `emitSlashCommandList`'s `SUPPRESSION RATHER THAN AN EMPTY EMIT`
  paragraph rather than restating it — it travelled there with the construction, declines
  to inherit rung 3's asymmetry, and already covers this rung.
- **Do NOT copy `WHERE A MIS-READ INVENTORY NOW GOES` onto rung 4.** It names three
  destinations for a mis-read MODEL inventory and a command list reaches none of them.
  `emitSlashCommandList`'s `WHERE THE CONSTRUCTED LIST GOES` is the command-list answer
  and this rung inherits it by calling that emitter.

#### 4. Rung 4's inline `Still no event` comment

Directly overturned; the new call lands here. What it should say: the rung reached the
emitter with its own discriminant, the suppression is the emitter's precondition and not
repeated here, and the call sits below the record for `IT LOGS NOTHING`'s reason.

#### 5. Rung 5's `THE SECOND GATE` / `THE ORDER IS DELIBERATE`, both halves

- `THE SECOND GATE`'s *"which is what lets a second call site inherit the suppression
  instead of repeating it"* is written prospectively. That call site now exists; name it.
- `THE ORDER IS DELIBERATE` currently argues rung 5 alone, from the models array being
  that rung's own discriminant. Re-author it as **one rule covering both call sites**:
  each rung emits its own discriminant's event first, then any independently-gated one —
  which on rung 4 is satisfied trivially because there is one event — and on both rungs
  `logControlResponse` runs before any emit. One rule, two sites, one order a reader
  checks once.

#### 6. The keyword `const` block

- `WHAT DECIDES THE NEW KEYWORD'S NAME`: *"A name for the emit — `command_list`, parallel
  to `model_list` — is false today, where that rung emits nothing"* is no longer the
  reason `commands_only` was chosen, and *"the slice that adds the emit re-authors a
  trailing comment rather than moving a keyword"* names this slice in the future tense.
  The paragraph must now record that the two-sided constraint was met: `commands_only`
  names the payload's SHAPE, so it survived the state change that `command_list` and any
  "ack" spelling would each have failed on one side of. **This paragraph is what makes
  the no-new-keyword scope boundary legible, so correcting it is what keeps the boundary
  readable rather than merely observed.**
- The trailing comment that moves is `controlResponseCommandsOnly`'s own one-line gloss —
  *"success, a commands inventory and no model list"*. The keyword stays; the gloss gains
  the emit. `ack`'s, `nak`'s, `undecodable`'s and `model_list`'s glosses do not move.

#### 7. `logControlResponse`'s doc

- **In the churn list:** *"the non-emitting half no longer rests on this count at all"*
  scopes the commands-only rung into "the non-emitting half". Correct that sentence.
- **Beyond the churn list — two class-word rots the ticket's list does not name.** The
  doc partitions rungs twice by whether they emit: *"all three 0 on every non-emitting
  rung"* and *"the attribute set is FIXED at six on every rung, which is why the
  non-emitting ones pass 0 rather than omitting the key"*. Both are arithmetically
  correct about the model trio and both use a label whose extension just changed — rung 4
  emits now. Re-word the **quantifier only**, to something that does not partition rungs
  by emit (naming the model-list rung as the exception works). **Do not re-argue** the
  six-attribute claim or the swap pin: the pin still shows on the
  `controlResponseCommandsOnly` rung, where the model trio is all-zero and this count is
  not, and that is unchanged.

#### 8. `eventKind`'s `turnevent.SlashCommandList` arm — `cmd/pyry`

*"every initialize reply carrying a commands array beside its models one puts one of
these on this lane"* is no longer the whole set. It must now cover any reply carrying a
non-empty `commands` array, whether or not a `models` array rides with it. This
**widens** the arm's own point — the four drop sites become reachable for strictly more
inputs — so it strengthens rather than weakens the argument already there.

**Not to be touched in this arm:** the WORKSPACE-authored-strings discipline, and the
enumeration of which drop sites are reachable and which are not. Neither changes.

---

## Concurrency model

Unchanged, and nothing here introduces one. `emitModelList` runs synchronously inside
`consumeLine` on the caller's `Write`; the new statement is a direct method call on the
same goroutine. No goroutine, channel, lock, context or shutdown path is added or
touched.

---

## Error handling

No new failure mode. The five-rung classification stays total over the input, no rung can
panic, and none surfaces an `Unrecognized`. `emitSlashCommandList` has no error return
and no error path: its only guard is the empty-input precondition, and rung 4 cannot
reach it with an empty slice (the rung's own discriminant is `commands != 0`). Decode
failures still return on rung 1, before either emit exists.

---

## Testing strategy

Every proof runs inside `make check`: `internal/streamsup` carries no build tag and the
fixtures are synthetic or read from committed capture bytes. No live claude.

### AC 4's liveness proof — rename and restructure `TestParser_InitializeControlResponseAckReportsTheCommandCount`

Its doc premise — that a commands-carrying payload with no models emits nothing — is
exactly what this ticket overturns, so this is surgery, not a row edit.

**Rename to `TestParser_InitializeControlResponseCommandsOnlyRungEmits`.** "Ack" stops
describing what it covers, and #1890 deliberately left the name so it is renamed once,
here. Two in-file citations re-point with it — the docs of
`TestParser_SlashCommandFieldsAreCapped` and `TestParser_SlashCommandListIsSuppressed`,
both already being edited below, so the rename rides along for free. There are no
citations outside this file.

**The blocker is structural:** the table asserts `len(events) != 0` **once for the whole
table**, so an emitting row cannot be expressed by editing a row field. Replace it with a
per-row expectation:

- Add a `wantEvents int` row field. Rows: `commands and no models` → 1;
  `an entry's name is null, beside a well-formed sibling` → 1; `neither array` → 0.
- On an emitting row, assert the single event **type-asserts to `turnevent.SlashCommandList`**
  and to nothing else. AC 1 requires no `ModelList` rides along, and the type assertion is
  what catches a mutant that fell through to the models tail.
- Assert the emitted `len(list.Commands)` equals the row's existing `wantCommands`.
  These two are computed from different places — the record's count is
  `len(cr.Response.Response.Commands)` taken before construction, the emitted count comes
  out of the emitter's loop — so asserting both against one literal is a genuine
  cross-check, not a restatement. Keep them as two assertions; do not collapse them.
- On the first row only, assert the three entry names verbatim: `deep-research`, `design`,
  `__remote-workflow`. Three lines, and `__remote-workflow` is the committed proof that no
  name charset may be assumed. This is what pins "same construction, same verbatim name"
  at the new call site.
- **Do not add cap-boundary rows.** That matrix is `TestParser_SlashCommandFieldsAreCapped`'s
  and the ticket forbids moving it.

**The record assertions are unchanged.** All three rows keep the exact six-attribute
`reflect.DeepEqual`, the same `wantReason` values (`commands_only`, `commands_only`,
`ack`) and the same `wantCommands`. Nothing about the record moves in this slice.

**Re-author the doc.** What must survive: the hand-built-fixture justification, the
THREE-entries argument (a mutant reporting a bool, a literal 1, or `len(models)` reads
right against one entry and wrong against three), the null carve-out row's decode
explanation and its relocation history, and the swap-pin sentence. What must change: the
opening premise, and the `neither array` row's job — it is now the paired negative for
**both** the keyword split and the emit, since without it a mutant that emitted on every
success would go green.

**Liveness:** the `wantEvents: 1` rows fail on this ticket's base commit (which emits 0)
and pass after. That is AC 4.

### `TestParser_ModelListIsLoggedContentFree`

- Emitted-event count **5 → 6**: the ninth line is a commands-only success with one entry
  and now emits.
- The `FIVE:` paragraph on the assertion, and its clause *"The three command-carrying
  lines on the NON-emitting rungs add nothing"* — two of the three, after this slice.
- The `lines` slice's *"The non-emitting rungs, each paired"* block: its class word no
  longer covers the last pair. Its closing sentence about that pair covering two rungs
  stays true and is #1890's; what goes false is calling all four pairs' rungs
  non-emitting.
- **Unchanged, verified rather than assumed:** `wantReasons`, `wantCounts` and
  `wantCommandCounts` all describe records, which do not move. The `leaks` sweep iterates
  `rec.all()` — records only — so `commandsOnlyCommandSentinel` riding an emitted event as
  well as a decode does not touch it. The doc's *"The narrowed ack rung (#1890) carries
  none, and cannot"* sentence stays true.

### `TestParser_SlashCommandListIsSuppressed` — doc only, five rows untouched

- Opening paragraph: *"below BOTH returns in the empty-models block — the narrowed ack's
  and the commands_only rung's (#1890) — so no non-emitting rung can reach it"*. After
  this slice rung 4 has its **own** call, so the rung-5 call sits below one such return,
  not two, and the reach claim narrows to rungs 1–3.
- `THE COMMANDS-ONLY RUNG IS THIS TABLE'S SIXTH ROW AND IT LIVES ELSEWHERE` borrows a
  zero-events proof from the renamed test's first row. **That borrowing ends here** — the
  row now emits. Replace it with the structural reason the table needs no sixth row:
  **rung 4 cannot reach the emitter with an empty array by construction**, because the
  rung's own discriminant is `commands != 0`. Rung 3 never calls the emitter at all. So
  rung 5's call is the only one that can reach the precondition with an empty slice, and
  this table's three empty-`commands` rows are therefore complete rather than merely
  representative. #1890 already corrected this paragraph's rung-naming half and its
  heading — do not re-do that.
- **Rows 4 and 5 keep their force, verified:** the nak and undecodable ordering pins still
  redden a hoisted emit, because rung 4's new call sits inside the empty-models block
  below the success gate and cannot be reached by either.

### `TestParser_SlashCommandFieldsAreCapped` — doc only, rows untouched

The load-bearing clause *"a line with no models emits nothing at all"* and *"the emit half
is the same on both"* go false. The honest replacement: the construction is now reached
from two rungs and is **identical** on both — one emitter, one loop, one cap — so the
matrix pins the bound wherever it is exercised. Every row keeps its models array because
that is the rung the matrix was measured on and moving it would buy nothing. Re-point the
citation to the renamed test. **The rows themselves are not this slice's to change.**

### The sweep you must run

`docs/knowledge/features/streamsup-package.md` records #1890's lesson: *a closed-set arity
claim rots by its **class word**, not by the rung's name, and a name-only sweep misses it.*
That ticket's own sweep found every claim naming `ack` and missed five that described the
classification's cardinality instead.

This slice's equivalent dimension is **emit arity**. After the edits above, sweep
`internal/streamsup/parser.go` and `internal/streamsup/parser_test.go` for
`non-emitting`, `emits nothing`, `no event`, `zero events`, `emitting rung` and rung-count
words, and check each hit against the new partition (rungs 1–3 silent, rung 4 emits one,
rung 5 emits one or two). The two `logControlResponse` items in § 7 above were found this
way and are **not** in the ticket's churn list; assume the sweep can find more.

### Checked non-items — verified against the tree, do not re-derive

- **`TestParser_InitializeControlResponseRejectBranches` keeps its shared
  `len(events) != 0`.** Verified row by row: its `commands` rows are either undecodable
  (return on rung 1), `null` / `[]` (which read `commands == 0` and land on rung 3), or a
  nak. **No row reaches rung 4 with a non-empty array**, so nothing here emits. Do not
  touch it.
- **`TestParser_ControlResponseAckIsConsumedSilently` is unaffected.** None of its rows
  carries a `commands` array; its `an ack carrying an inner response payload` row's `why`
  already says a commands-and-no-models payload is its own rung. Read it, change nothing.
- **`commandEntryLine`'s doc is unaffected.** Its sink enumeration is of **sinks reached**,
  and the sinks do not change — only the set of input lines that reaches them widens.
  `A PER-FIELD CAP DOES EXIST`'s transience and retention claims hold at both call sites.
- **`turnevent.SlashCommandList`'s doc is unaffected.** `THE PRODUCER HAS SINCE ARRIVED`
  already reads *"more than one call site reaches it"*, `IT IS PUBLISHED BY NO PATH TODAY`
  is #1720's and still true. Leaving it alone is what keeps the production-file count at 2.
- **`maxSlashCommandName`'s doc** owns the per-entry budget and the no-retention statement;
  both survive a second call site unchanged.
- **`eventKind`'s *"emitModelList produces it (#1877)"*** is imprecise since #1886 but not
  falsified by this change. Out of scope.
- **`protocol.SlashCommandListPayload`'s "alongside the models array"** describes claude's
  observed reply, not the daemon's classification. Out of scope.
- **`docs/knowledge/features/streamsup-package.md`** is the documentation phase's. Do not
  edit it.

---

## Conventions and scope

- **Cite by symbol, never by line.** `make cite-guard` runs inside `make check` and fails
  any line-number citation added to a `//` comment, at any depth, ranges included.
- **Scope boundary:** no new reason keyword and none moved, no wire frame, no protocol
  change, no client, no new cap or constant, no new exported type. The construction, the
  byte cap and the verbatim rule are #1886's and #1877's and are reused rather than
  re-decided. The entry-count cap and `DroppedCommands` stay #1826's; the proof matrix
  stays #1885's.
- **Production files: exactly two** — `internal/streamsup/parser.go` and one comment in
  `cmd/pyry/interactive_turn_v2.go`. If a third becomes necessary, stop and say so rather
  than widening.

---

## Open questions

- **The new test name.** `TestParser_InitializeControlResponseCommandsOnlyRungEmits` is
  the recommendation — it keeps the family's `InitializeControlResponse` prefix (three
  siblings use it) and names the rung the test is now organised around. A shorter variant
  is acceptable if it still names the rung and the emit; do not spend turns on it.
- **Whether `emitSlashCommandList`'s `IT LOGS NOTHING` paragraph needs a word.** It reads
  *"written by `emitModelList` BEFORE this call"*, which is already correct for two call
  sites. Recommendation: leave it. Change it only if placing the call reveals otherwise.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX — state the widening in the rung 4 comment.** This
  slice moves the boundary in one direction only: it **widens the set of inputs that reach
  the constructing-and-retaining path**. Before it, only a payload carrying *both* arrays
  had its `commands` entries copied into a `turnevent.SlashCommand` and retained for the
  event's lifetime; after it, a commands-only payload does too. The boundary itself is
  unmoved and remains explicit and single — one decode target (`commandEntryLine`), one
  construction site (`emitSlashCommandList`), one cap (`maxSlashCommandName`) — and the
  spec's design forbids a second copy of any of the three. The ask is live, not latent:
  `RequestInitializeOnSpawn` fires once per spawned child and `mapStreamsupConfig` sets it
  true. The strings are **workspace-authored** — a command defined in a repository was
  written by whoever wrote that repository, and the daemon reads it in whatever directory
  the operator points a session at — which is a *lower*-trust origin than claude's own
  strings. Rung 4's new comment should say the rung reaches the constructing path, so a
  later reader sees the widening at the site rather than inferring it.
- **[Trust boundaries] No further findings — the sink set does not change.** Verified
  against `commandEntryLine`'s enumeration: a Name reaches one field of a daemon-internal
  struct, the parser's emit callback, `turnbridge.MapEvent`'s default (no arm, drops it)
  and `interactiveTurnEmitterV2.Handle`'s default (no case, logs it BY KIND through
  `eventKind`). It reaches no `exec.Command` argument, no `filepath.Join`, no
  `filepath.Match`, no `regexp`, no log attribute, no eventring append and no wire frame.
  Widening which inputs arrive does not add a sink. `maxSlashCommandName`'s
  `THE RE-OPEN TRIGGER` names what would: the first slice giving the value a syntax sink or
  an HTML/attribute/URL render. That is #1720's and is not this slice.
- **[Error messages, logs, telemetry] No findings, and this is the category with the most
  to lose.** The spec adds **no log call**: `emitSlashCommandList` logs nothing on any path,
  the one record is `logControlResponse`'s, and its attribute set stays fixed at six
  daemon-authored values — a keyword from a closed set plus four integers derived from slice
  lengths. The design explicitly places the new call **below** the record so that stays
  structural. Two live guards, both verified present: the exact six-attribute
  `reflect.DeepEqual` in the renamed test reddens on any attribute added beside the
  counter, and `TestParser_ModelListIsLoggedContentFree`'s `leaks` sweep searches every
  record — message and every attribute value — for `commandsOnlyCommandSentinel`, which is
  planted on exactly this rung. The `json.Unmarshal` error is still not logged on rung 1,
  which is the highest-value case: `encoding/json` quotes offending input bytes into its
  error text, so `"err", err` there would route workspace-authored names into the daemon
  log through a channel no per-attribute check can see. This slice touches neither.
- **[Network & I/O — input size limits] No findings; both bounds pre-exist and neither is
  weakened.** `defaultMaxParseBuf` caps the whole line at 4 MiB before the decoder sees it,
  and `maxSlashCommandName` caps each retained Name at construction. **Neither is
  reachable-only-on-rung-5**: the line cap is upstream of the decode and the name cap is
  inside the emitter this rung calls, so both apply to the new path by construction rather
  than by a second check.
- **[Network & I/O — resource exhaustion] SHOULD FIX at the design level, and it is
  already the shape chosen.** There is **no entry-count cap** on `commands` (that bound is
  #1826's), so a hostile workspace defining very many commands produces a proportionally
  large `[]turnevent.SlashCommand` retained for the event's lifetime. This slice does not
  worsen the per-line worst case — the 4 MiB line cap is the same ceiling on both rungs,
  and `commandEntryLine` is **one** field where `modelOptionLine` is five, so per densest
  legal element the transient is a fraction of the already-accepted models one. What it
  does change is **frequency**: a commands-only reply now allocates where it previously
  did not. That is a bounded, already-accepted quantity arriving on one more rung, not a
  new class of exposure, and #1826 owns the count bound. Explicitly **not** worked around
  here — adding a cap in this slice would put the limit in two places, which is exactly
  what `turnevent.SlashCommandList`'s `THE BOUND IS THE PRODUCER'S` paragraph and the
  ticket's scope boundary both forbid.
- **[Subprocess / external command execution] Not applicable, by the sink enumeration
  above rather than by assumption.** No value on this path becomes an argv element, and
  `turnevent.SlashCommandList`'s `IT IS A REPORT, NEVER A CONTROL INPUT` states the rule at
  the type: nothing in the daemon may treat a value from it as a command vocabulary. This
  slice adds no consumer at all, so it cannot breach that.
- **[File operations] Not applicable — no path is constructed, opened or written. The one
  fixture consideration is a test-side capture read as bytes.** Worth naming because `[`,
  `*` and `?` are syntax to `filepath.Match` and `regexp` with no shell in sight, and one
  captured name is `__remote-workflow`: "these look like identifiers" would be the wrong
  argument. The right one is that there is no path sink.
- **[Cryptographic primitives] Not applicable — no randomness, no key material, no
  comparison against a secret.**
- **[Concurrency] Not applicable, and stated rather than assumed.** The new statement is a
  synchronous method call on the goroutine already running `emitModelList` inside
  `consumeLine`. No lock is taken, no shared state is read or mutated, no goroutine is
  spawned, and there is no check-then-mutate. `go test -race` covers the path via `make
  check`.
- **[Tokens, secrets, credentials] Not applicable — this path carries no credential. The
  adjacent posture is #833's, restated across `internal/relay`'s `v2session_settings.go`
  and `internal/sessions`' `pool.go` as "model / effort / YOLO values are NEVER logged at
  any level", and the logs finding above is where it is enforced for this slice.**
- **[Threat model alignment] The client-side render boundary is OUT OF SCOPE and already
  assigned.** These strings are safe to render as inert text and must never reach an HTML
  sink, an attribute or a URL; the daemon bounds them but does not sanitize them — no
  control-character or terminal-escape stripping happens on this path — so they stay
  untrusted text all the way out. `turnevent.SlashCommandList`'s SECURITY paragraph assigns
  that boundary to the CLIENT, and #1720 is the slice that owes it. This ticket publishes
  nothing, so it does not advance that exposure; it only makes the daemon-internal event
  reachable from one more rung.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
