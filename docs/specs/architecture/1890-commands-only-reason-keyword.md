# #1890 — Give the commands-only success its own reason keyword

## Files to read first

Everything in this ticket lives in one package. Read these symbols, in this order.

- `internal/streamsup/parser.go` → the `const` block headed by `controlResponseMsg` — the closed reason set (`controlResponseNAK` / `Ack` / `Undecodable` / `ModelList`), each keyword's trailing comment, and the block doc that calls the set closed. This is where the one new constant lands.
- `internal/streamsup/parser.go` → `emitModelList` — the whole function AND its doc. The doc's `FOUR RUNGS` enumeration is the thing this ticket re-authors; the function body's `commands := len(...)` statement, its count-placement comment, and rung 3's `len(entries) == 0` block are the only executable lines that move.
- `internal/streamsup/parser.go` → `logControlResponse` — the doc's `commands` paragraph (the sentence that hands this decision over) and its swap-hazard paragraph. The function body does not change; the attribute set stays six.
- `internal/streamsup/parser.go` → `emitSlashCommandList` — read the doc only, for the cite it makes into `emitModelList`'s enumeration by ORDINAL. See § The renumbering constraint; this is the one item not in the ticket's list.
- `internal/streamsup/parser_test.go` → `TestParser_InitializeControlResponseAckReportsTheCommandCount` — the table whose `wantAttrs` hardcodes `"reason": "ack"` for both rows. This is where the new keyword's proof lives and where the relocated row lands.
- `internal/streamsup/parser_test.go` → `TestParser_InitializeControlResponseRejectBranches` — the `wantCommands` struct field, its doc comment, and the `an entry's name is null, beside a well-formed sibling` row. Note the table asserts `len(events) != 0` ONCE for the whole table, not per row.
- `internal/streamsup/parser_test.go` → `TestParser_ModelListIsLoggedContentFree` — the `wantReasons` slice; the ninth element is the commands-only line.
- `internal/streamsup/parser_test.go` → `TestParser_ControlResponseAckIsConsumedSilently`, `TestParser_SlashCommandListIsSuppressed`, `TestParser_SlashCommandFieldsAreCapped` — read the DOC of each. None of their `wantAttrs` move; each carries a `//` claim that does.
- `docs/knowledge/features/streamsup-package.md` § "Declaring `commands` alongside `models` (#1853)" — the gate-placement testing lesson (*a placement claim needs a fixture where the two placements disagree*). It is the reason the `THE SUBTYPE HALF: a NAK carrying a well-formed commands array` row must survive this change untouched.

## Context

`emitModelList` classifies one top-level `control_response` onto four rungs. Rung 3's `ack` currently answers two different payloads: a success carrying neither array, and a success carrying a non-empty `commands` and no `models`. The `commands` count is what separates them on the record today; the keyword is not.

A sibling ticket makes the commands-only payload reach an emitting outcome. A rung that emits while its record still says `ack` is a record that lies. This slice takes the keyword decision first, while nothing yet emits, so the closed reason set is decided once and the sibling inherits it.

**The set decided here is final.** The sibling adds no keyword and moves none; it re-authors this keyword's trailing comment when the rung starts emitting.

No ADR is warranted — this is one constant inside an existing closed set, and the package overview is the right home for the lesson.

## Design

### The keyword

One new constant in the closed set, named the way its four siblings are:

```go
controlResponseCommandsOnly = "commands_only" // success, a commands inventory and no model list
```

**Why this name, and why the constraint is two-sided.** The keyword must name the payload OUTCOME — neither the emit nor the non-emit:

- A name for the emit (`command_list`, parallel to `model_list`, whose comment reads *"one turnevent.ModelList emitted"*) is FALSE in this slice, where nothing is emitted.
- A name for the non-emit (anything spelling "ack") goes false the moment the sibling lands.

`commands_only` describes the shape of the payload, so it survives both states. The trailing comment above deliberately says what the payload IS, not what the daemon did about it — that clause is the sibling's to add.

`controlResponseAck` narrows to "success, neither array" and its own trailing comment (*"success, no model list on the line"*) must narrow with it: it no longer answers every non-emitting success.

### The classification

Rung 3's body splits on the already-computed `commands` count. Nothing else in the function's control flow moves — no new decode, no new bound, no reordering.

```go
if len(entries) == 0 {
    if commands == 0 {
        p.logControlResponse(controlResponseAck, 0, 0, 0, 0)
        return
    }
    p.logControlResponse(controlResponseCommandsOnly, 0, 0, 0, commands)
    return
}
```

**Pass the literal `0` on the ack rung, not `commands`.** The two are provably identical under the guard, so no test can tell them apart — this is a readability decision, and stating it here is what stops the developer hunting for a pin that cannot exist. The literal is right because the ack rung now MEANS "neither array": its record is all-zero by definition, exactly as the undecodable and nak rungs' are, and `commands` then appears as an argument only on the rungs where it can be non-zero.

### The count's placement argument gets stronger, not just re-worded

This is the crux of the comment re-authoring, and getting it right is most of the work.

Today the count-placement comment above `commands := len(...)` argues its lower bound from what the RECORD would say: *"taking it below rung 3 would leave the ack rung — the one rung where this is the record's only non-zero number — reporting 0."* After the split that sentence names the wrong rung (`ack` now reads 0 by definition; the non-zero rung is the new one).

But the replacement is not a rename. **After this change `commands` stops being only a number the record reports and becomes the rung DISCRIMINANT.** So the lower bound no longer binds because a record would read 0 — it binds because the branch could not be taken at all. Re-state it that way; it is strictly the stronger argument and it is the one that survives the sibling.

The upper bound is unchanged: the count stays BELOW the subtype comparison, so a response announcing FAILURE reports 0 however many entries it carried. That half is pinned by `TestParser_InitializeControlResponseRejectBranches`' `THE SUBTYPE HALF: a NAK carrying a well-formed commands array` row, which must survive this ticket untouched — per the package overview's gate-placement lesson, it is the ONLY row where the two placements disagree.

### The renumbering constraint — the one item outside the ticket's list

`emitModelList`'s `FOUR RUNGS` header becomes FIVE, and the ordinals shift:

| # | Rung | Change |
|---|---|---|
| 1 | undecodable | untouched |
| 2 | nak | untouched |
| 3 | **ack — neither array** | bullet narrows; **keeps the false-negative asymmetry argument** |
| 4 | **commands_only** (new) | new bullet; inherits rung 3's models-empty reading by reference |
| 5 | model_list emit | was rung 4; trailing sentence re-pointed |

**`emitSlashCommandList`'s doc cites this enumeration by ORDINAL:** *"Rung 3's false-negative asymmetry is NOT the argument, and was weighed rather than inherited."* That cite keeps resolving only if the false-negative asymmetry argument stays on the bullet that stays numbered 3. Splitting the other way round — commands-only as 3, ack as 4 — rots it SILENTLY, with no test and no gate to catch it.

So: **ack stays rung 3 and keeps the asymmetry paragraph.** Rung 4's new bullet must NOT re-argue the asymmetry; it is the same models-empty reading and differs only in the keyword, so it says so by reference. Re-arguing it would create a second copy to correct.

**Where a sentence is being re-authored anyway, prefer naming a rung by its keyword constant over its ordinal.** Ordinals renumber on the next split; `controlResponseCommandsOnly` does not, and that is this repo's own cite rule. This applies to the sentences already in scope below — it is NOT a licence to sweep the file converting untouched ordinals.

### Not in this slice

No emit, no call to `emitSlashCommandList`, no wire frame, no protocol change, no client, no new constant beyond the one keyword. `emitModelList` is NOT renamed. The attribute set stays **six, and no seventh** — the name cap is per FIELD, this family has no COUNT cap until #1826, so the decoded count still equals the emitted count and `commands` remains an unambiguous decode count.

Leave alone, deliberately: the CONJUNCTIVE-gate paragraph above the enumeration (about `models` being the discriminant — true until either array can reach an emitting outcome, which is the sibling's), and rung 5's `THE SECOND GATE` / `THE ORDER IS DELIBERATE` paragraph (written prospectively, still accurate).

## Data flow

Unchanged end to end. One `control_response` line in, exactly one Debug record out, six attributes, zero decoded content on every rung. The only thing that moves is which keyword one branch writes into the `reason` attribute — plus one new branch on a count that was already being computed.

## Concurrency model

None. `emitModelList` and `logControlResponse` run on the parser's single `Write` path; this ticket adds no goroutine, no lock, no shared state, no channel. The new branch reads a local `int` computed earlier in the same function.

## Error handling

No new failure mode. The new rung is a success-subtype outcome that emits nothing and returns, exactly as the ack rung it splits from. Nothing new can panic: `len()` on a nil slice is 0, and the branch is total over the two cases of an `int` compared to 0.

The undecodable rung's rule is untouched and stays load-bearing: the unmarshal `err` is never logged, because `encoding/json` quotes offending input bytes into its error text and those bytes are workspace-authored command names.

## The churn — the complete item list

Re-derived against `d7ccd381`. **Re-derive before starting if a sibling landed first** — this family's cites went stale twice in three days.

### Dead cites (3, all in Go files)

`git grep '#1876' -- '*.go'` returns exactly three. #1876 is closed (split into this family), so all three defer to a ticket that will never act. This ticket IS the split they defer, so all three die here:

1. `emitModelList`'s rung-4 bullet — *"…which is #1876's to split."*
2. `logControlResponse`'s doc — *"how legibly the KEYWORD says so is #1876's to revisit, not this record's shape."*
3. `TestParser_SlashCommandListIsSuppressed`'s paragraph — carried as a test item below.

### Production `//` claims falsified (5)

- **`emitModelList`'s rung-3 BODY comment**, attached to the `logControlResponse` call being split: *"An absent `models`, a null one, an empty array, and a response object carrying no such key all land here and are answered identically — including a payload carrying a non-empty `commands` and no models, which is an ack whose record now tells it apart from one carrying neither array."* BOTH clauses go false — those payloads are no longer answered identically, and the commands-only one is no longer an ack. It sits AT the edit point.
- **`logControlResponse`'s doc**: *"on the `model_list` rung a non-empty `commands` now always means emitted and on the ack rung always not, so the existing six already separate the two."* The second half stops holding. This is the sentence that HANDS the decision over, so it goes false in the same edit that takes it.
- **`controlResponseAck`'s trailing comment** (*"success, no model list on the line"*) — narrows to "success, neither array".
- **The closed-set block's doc** — describes a set whose `ack` answered every non-emitting success.
- **`emitModelList`'s count-placement comment** — see § The count's placement argument above; this one is a re-argument, not a rename.
- **`logControlResponse`'s swap-hazard paragraph**: *"a swap shows on exactly one rung, the ack rung, where the model trio is all-zero and this count is not."* Same falsification. The rung where the trio is all-zero and the count is not is now `commands_only`; on the narrowed ack rung all four ints are 0 and a swap is invisible. This paragraph is the pin for the four-adjacent-int argument, so it cannot be left standing.

### Test-side items (6)

Seven `wantAttrs` maps compare the record with `reflect.DeepEqual`. **Three move:**

- **`TestParser_InitializeControlResponseAckReportsTheCommandCount`** — the largest single item.
  - Its `wantAttrs` hardcodes `"reason": "ack"` for both rows, so the split **adds a `wantReason` field to the row struct** rather than editing a value. That is a table change, not a one-line one.
  - The `commands and no models` row moves to the new keyword; its `why` (*"nothing new is emitted, so the rung is still ack"*) moves with it.
  - The `neither array` row's `wantAttrs` does NOT move, but its `why` (*"which is what makes the row above distinguishable"*) now understates the separation — worth one clause.
  - **Its own doc paragraph**: *"the ACK rung reports the decoded command count, so a payload carrying commands and no models is distinguishable in the log from one carrying neither array"* names a rung that payload no longer lands on.
  - Its later swap sentence (*"the one rung where a swap … shows: the trio is all-zero here and this count is not"*) says "here" rather than naming the rung, and SURVIVES. Check the two as a pair — this is the test-side twin of the production swap-hazard pin above, and only one of them moves.
  - **Do not rename this test.** The sibling makes it the liveness proof for the emit and renames it then.
- **`TestParser_ModelListIsLoggedContentFree`** — the ninth element of `wantReasons` (the commands-only line, carrying `ackCommandSentinel`). Its `wantCommandCounts` ninth element stays `"1"`, and **the emitted-event count stays 5** — no event moves in this slice.
- **`TestParser_InitializeControlResponseRejectBranches`' `an entry's name is null, beside a well-formed sibling` row — RELOCATE it, do not edit its `wantReason` in place.** A JSON null decodes into a non-pointer Go string as a no-op, so this row decodes, counts 2, and lands on the new keyword. Move it into `...AckReportsTheCommandCount`, whose two rows are the paired fixtures for exactly this outcome. Its `line` is built by `initializeLineFixture(t, "success", …)` and the destination table supplies exactly that `inner` map, so the fixture transfers unchanged.
  - **Carry the row's `THE NULL CARVE-OUT` comment with it verbatim** — that comment is the substance of the row, and leaving it behind loses the decode-shape knowledge the move is otherwise neutral about.
  - It is the ONLY row in `RejectBranches` that sets `wantCommands` (verified — no other row does), so the move retires the field. **Delete the field and its doc comment** (*"its zero value is the right answer for every row that predates #1853"*) and hardcode `"commands": "0"` in that table's `wantAttrs`. `strconv` stays imported — the destination table still uses it.
  - Why the move belongs in THIS slice: `RejectBranches` asserts `len(events) == 0` ONCE for the whole table, not per row. Moving the row while it still emits nothing keeps this a pure relocation and spares the sibling a structural break on a shared assertion.

**Three `//` claims go false in tests whose `wantAttrs` do NOT move** — reachable only by reading, never by running:

- **`TestParser_SlashCommandFieldsAreCapped`'s doc** — the item no list-driven developer opens the file for, because the test is named nowhere else in the ticket: *"The models array is carried by every row because the commands emit rides the MODEL-LIST rung: a line with no models lands on the ack rung and emits nothing at all, which TestParser_InitializeControlResponseAckReportsTheCommandCount pins."* The middle clause is false for exactly the line class this test's rows are about. Its trailing half (*"emits nothing at all"*) stays true and is the sibling's. No row moves; every row here carries `models`.
- **`TestParser_ControlResponseAckIsConsumedSilently`'s `an ack carrying an inner response payload` row**, whose `why` reads *"an inner payload carrying no models array is still an ack."* That is a claim about the CLASS, not the row — the row's own fixture carries neither array, so its `wantAttrs` does NOT move. That is what makes it easy to miss. The test's own doc paragraph (*"the rows still assert that no ack produces an EVENT"*) stays true and is not an item.
- **`TestParser_SlashCommandListIsSuppressed`'s `THE ACK RUNG IS THIS TABLE'S SIXTH ROW AND IT LIVES ELSEWHERE` paragraph — the naming half only.** The row it points at is no longer on "the ack rung", and *"If it reddens, the ack rung's classification changed, which is #1876's scope and not this table's"* is false in BOTH clauses: the classification change is THIS ticket's. The paragraph's other half (*"asserts zero events"*) stays true and is the sibling's. Its rows all carry `models` and are otherwise untouched.

### Discovered gap — not in the ticket's list, verified by an independent sweep

AC 3 makes a claim found falsified outside the list still in scope. I swept every `//` claim naming the ack rung across all Go files. The ticket's enumeration is otherwise complete. Two additions:

- **`emitSlashCommandList`'s doc — an ORDINAL cite into the renumbered enumeration.** Covered in full under § The renumbering constraint. It is not falsified IF ack stays rung 3; it rots silently if the split is numbered the other way. **This is a constraint on the design, not an edit** — no change to the sentence is needed once ack stays 3.
- **`TestParser_SlashCommandListIsSuppressed`'s FIRST paragraph** (distinct from the item above, same doc): *"the CALL to emitSlashCommandList sits below `logControlResponse`, below the ModelList emit and below rung 3's return — so no non-emitting rung can reach it."* After the split there are TWO non-emitting returns in that block, so naming only rung 3's under-supports the paragraph's own conclusion. **Not falsified — the call IS still below both — so this is optional.** One clause naming both returns (or naming them by keyword) fixes it. Flagged so it is not re-decided mid-implementation; do not expand it into a rewrite.

### Settled non-items — do not touch

Named so they are not mid-implementation discoveries:

- The `#1876` cites under `docs/specs/architecture/` — per-ticket build artifacts, not maintained after their ticket ships.
- `docs/knowledge/features/streamsup-package.md`'s `#1876` cite — the documentation phase owns that file and re-points it. **It is not a deliverable of this ticket.**
- `turnevent.SlashCommandList`'s producer-attribution paragraph — #1886 already rewrote it.
- `consumeLine`'s `control_response` arm — scoped to the three responses that are NOT the initialize reply.
- `protocol.SlashCommandListPayload`'s "alongside the models array" sentence — describes claude's observed reply, not the daemon's classification.
- `cmd/pyry`'s `eventKind` arm — about what reaches the lane; the sibling's.
- **`TestParser_ModelListEntryCountIsBounded`'s** *"Empty and absent arrays … return at the ack rung before the cap ever runs"* and **`TestParser_ModelListEffortLevelCountIsBounded`'s** *"Same exclusion … states for the ack rung, one dimension down"* — **verified NOT items.** I re-checked both: each is scoped to an empty-or-absent MODELS array on a line carrying no commands, which still lands on `ack` after the split. They stay true. Leave them.

## Testing strategy

Every proof runs inside `make check`. `internal/streamsup` carries no build tag and `capturedInitializePayload` reads the committed captures as bytes, so **no fixture in this slice needs a live claude.**

**AC 4's red-on-base proof already exists — do not write a new test function.** Three independent assertions fail on the base commit and pass after:

- `...AckReportsTheCommandCount`'s `commands and no models` row, once its `wantReason` is the new keyword (base logs `ack`).
- `...ModelListIsLoggedContentFree`'s ninth `wantReasons` element.
- The relocated `an entry's name is null, beside a well-formed sibling` row, in its new home.

The first is the strongest and is the one to cite as AC 4's proof: three entries, all six attributes compared with `reflect.DeepEqual`, and zero events asserted.

**What each surviving fixture is still for** — state these rather than rediscover them:

- The `commands and no models` row keeps THREE entries (not one). A mutant reporting a bare present/absent bool, a literal `1`, or `len(models)` reads right against a one-entry fixture and wrong against three.
- The `neither array` row is the paired negative: it is what proves the new keyword is a SPLIT rather than a rename of `ack`. Losing it would let a mutant that logs `commands_only` on every success go green.
- `RejectBranches`' `THE SUBTYPE HALF: a NAK carrying a well-formed commands array` row stays exactly as it is — the sole detector for the count being taken below the subtype gate.

**No new fixture is needed.** No captured arm carries `commands` without `models` — all three responding arms of `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` and its two siblings carry both, and the fourth arm recorded no `control_response` at all. The commands-only fixtures are hand-built, which is what `...AckReportsTheCommandCount` already does.

**Deliberately NOT a new test:** that the new rung emits nothing. Every table above already asserts zero events on it, and `...SlashCommandListIsSuppressed` states why the call site is unreachable from it. Adding a fourth assertion of the same fact is churn the sibling would have to unpick.

## Open questions

None blocking. Two decisions taken in this spec that the developer should NOT re-open:

1. **The keyword spelling is `commands_only`.** The ticket admits any string naming the payload shape; this spec picks one so the developer does not spend a turn choosing. The two exclusions (no emit-name, no ack-name) are the real constraint.
2. **ack stays rung 3.** Forced by `emitSlashCommandList`'s ordinal cite, per § The renumbering constraint.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the boundary is worth naming rather than waving through: the strings this classification reads are **workspace-authored** and crossed the subprocess trust boundary — a command defined in a repository was written by whoever wrote that repository, and the daemon reads it in whatever directory the operator points a session at. The ask is live rather than latent: `RequestInitializeOnSpawn` fires it once per spawned child and `mapStreamsupConfig` sets it true. The boundary is explicit and single: `emitModelList` decodes the TOP-LEVEL `line` bytes into `controlResponseLine` and nothing below reaches for a nested field. This ticket adds no decode, no new field, and no new reader — it branches on an `int` that was already being computed from a slice length.
- **[Error messages, logs, telemetry]** No findings, and this is the property the ticket exists to preserve, so it gets the closest look. **No decoded content reaches the log on any rung, before or after this change.** `logControlResponse` remains the single place that is decided, its attribute set stays fixed at six, and all four integers stay DAEMON-computed from slice lengths. The new keyword is a daemon-authored constant from the closed set at `controlResponseMsg` — it carries none of claude's bytes and cannot, because it is a compile-time literal. Specifically checked and unchanged: the undecodable rung still does NOT log the unmarshal `err` (`encoding/json` quotes offending input bytes into its error text, and since `commandEntryLine` was declared those bytes are workspace-authored command names). The one new branch adds no `%w`, no `%q`, no error value and no string argument.
- **[Error messages, logs, telemetry — second-order]** SHOULD FIX, and it is the one place a hostile workspace gains anything at all: the new keyword makes a commands-only payload **distinguishable in the daemon log** where it previously read as `ack`. This is the ticket's purpose, and the disclosure is one bit of shape information — "this reply carried a command inventory and no model list" — to an operator reading their own daemon's log about their own subprocess. No name, no `argumentHint`, no `description`, no alias, and no count beyond the one already logged since #1853. Not exploitable; noted so code review does not have to re-derive that the bit is intentional.
- **[Subprocess / external command execution]** No findings. This ticket executes nothing. The values in question are parsed FROM a subprocess's stdout, never passed back TO one as arguments; no `exec.Command`, no `sh -c`, no environment change, no signal handling is in scope.
- **[Network & I/O]** No findings. Nothing reaches a socket. The values classified here are not published: `turnbridge.MapEvent` has no arm and `interactiveTurnEmitterV2.Handle` has no case for the slash-command variant (#1720 owns both, still open), so the value is logged by kind through `eventKind` and dropped. This slice emits nothing at all, which is strictly less exposure than the rung it splits from.
- **[Resource exhaustion]** No findings in scope, with the bound named explicitly. The per-name byte cap `maxSlashCommandName` and #1600's verbatim rule are #1877's and #1886's and are reused unchanged. **There is deliberately no ENTRY-COUNT cap on the `commands` array** — that is #1826's, together with `DroppedCommands`. This slice neither adds nor needs one: it retains nothing, constructs nothing, and emits nothing; it reads `len()` on an already-decoded slice and discards it. An unbounded `commands` array is bounded upstream by the decode itself, and its retention cost is #1826's to cap when the entries are retained.
- **[Tokens, secrets, credentials]** Not applicable by design — no credential, token or key is read, written, compared or logged on any path this ticket touches.
- **[File operations]** Not applicable by design — no filesystem access. The only file bytes in scope are committed test captures read by `capturedInitializePayload` inside the test binary.
- **[Cryptographic primitives]** Not applicable by design — no randomness, no hashing, no comparison against a secret. The one new comparison is `commands == 0` on a daemon-computed slice length, where constant-time behaviour is meaningless.
- **[Concurrency]** No findings. No goroutine, no lock, no shared mutable state, no channel. The new branch reads a function-local `int` on the parser's single `Write` path. Nothing is spawned, so nothing can leak.
- **[Threat model alignment]** No findings. #833's posture — *"model / effort / YOLO values are NEVER logged at any level"*, restated across `internal/relay`'s `v2session_settings.go` and `internal/sessions`' `pool.go` — is what `logControlResponse`'s content-free rule enforces, and this ticket preserves it verbatim by adding only a daemon-authored keyword. The client-side render boundary stays `protocol.ModelOption`'s to own and is untouched.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
