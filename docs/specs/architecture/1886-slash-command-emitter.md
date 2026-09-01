# #1886 — Give the slash-command construction its own emitter

**Size:** xs (confirmed — see § Size check)
**Scope:** `internal/streamsup` (production + test), `internal/turnevent` (two comment sentences)

## Files to read first

Read these before touching anything. This is the turn-1 data load; the block being moved is short but the
comment prose around it is what this ticket is actually about.

- `internal/streamsup/parser.go` → `emitModelList` — the four-rung classification and, at its tail, the
  block being moved. Read the whole function doc: the paragraph split in § Design rests on which
  sentences are about the RUNG and which about the CONSTRUCTION.
- `internal/streamsup/parser.go` → `commandEntryLine` — the decode struct the new emitter's parameter
  carries, and the home of two of AC 3's five cites plus two of the four that must NOT move. The
  sentence-level discipline this ticket needs is visible here: two *adjacent* sentences, one moves and
  one does not.
- `internal/streamsup/parser.go` → `maxSlashCommandName` — the cap, and the doc that already owns "there
  is no `sessionModelHold` analogue for this array". AC 4 cites this rather than restating it.
- `internal/streamsup/parser.go` → `logControlResponse` — the six-attribute record. AC 1's "same record,
  same keyword, same six attributes" is structural only as long as the record stays written by
  `emitModelList` before the call. Nothing here changes.
- `internal/streamsup/parser.go` → `truncateField` — the `<=` boundary the cap rides on. Unchanged; read
  it so you can tell that the moved `truncateField(entry.Name, maxSlashCommandName)` call is untouched.
- `internal/turnevent/event.go` → `SlashCommandList` — the "THE PRODUCER HAS SINCE ARRIVED" paragraph
  (cite that moves) and the "IT IS PUBLISHED BY NO PATH TODAY" paragraph (explicitly out of scope, see
  § Do not fix). `SlashCommandList.Commands` holds the second cite that moves.
- `internal/turnevent/event.go` → `SlashCommand` — the element type. Its `Name` doc states the verbatim
  rule at the consumer; the producer's restatement travels with the block unchanged.
- `internal/streamsup/parser_test.go` → `TestParser_SlashCommandListIsSuppressed` — AC 2's placement
  sentence and AC 3's fifth cite, both in this one doc comment.
- `internal/streamsup/parser_test.go` → `TestParser_SlashCommandFieldsAreCapped`,
  `TestParser_InitializeControlResponseCountsTheCapturedCommands`,
  `TestParser_InitializeControlResponseAckReportsTheCommandCount`,
  `TestParser_ModelListIsLoggedContentFree` — the four tables that between them are this ticket's whole
  regression net. Read them to confirm none needs an assertion edit.
- `cmd/pyry/interactive_turn_v2.go` → `eventKind`, its `turnevent.SlashCommandList` arm — the
  enumeration AC 4 must POINT AT rather than copy. Also read `Handle` and `emitMapped` in the same file:
  AC 4's factual claim depends on the call graph, not on this spec's prose (see § The trap in AC 4).
- `docs/knowledge/features/streamsup-package.md` § "Producing `turnevent.SlashCommandList` (#1877)" and
  the three lessons under it — in particular *"a sink-reachability enumeration carried over from the spec
  still needs checking against the call graph"*. That lesson is about the exact paragraph AC 4 asks you
  to write. Read it before writing the new doc, not after.

## Context

`emitModelList` classifies one top-level `control_response` line onto four rungs. #1877 made rung 4
two-dimensional: below the `ModelList` emit, under a second gate decided on the `commands` array alone,
it caps each slash-command name at construction, builds `turnevent.SlashCommand` entries and emits one
`turnevent.SlashCommandList`. That construction is an inline block at the tail of the function.

The sibling slice this was split from (#1876) adds a rung on which a success payload carrying a non-empty
`commands` and **no** `models` emits the same list — a second call site for the same construction.
Extracting it first is behaviour-identical, falsifies no claim about the classification, and keeps that
change to the classification rather than mixing a mechanical move into it.

Nothing about the classification moves here. Four rungs stay four, rung 3 still acks a commands-only
payload and emits nothing, the record still carries six attributes with the same keyword on every rung,
and the two emits keep their order.

**No ADR.** This is an intra-package extraction with no decision that outlives the ticket. The
documentation phase folds the outcome into `docs/knowledge/features/streamsup-package.md`.

## Design

### The new emitter

```go
// emitSlashCommandList emits AT MOST ONE turnevent.SlashCommandList for one decoded
// `commands` array. <doc — see § What the new doc must say>
func (p *Parser) emitSlashCommandList(entries []commandEntryLine)
```

Method on `*Parser` because it calls `p.emit`. Named for the variant it emits, matching every sibling in
this file (`emitModelList`, `emitModelAnnounced`, `emitBackgroundTaskRoster`).

- **Parameter, not `line []byte`.** The siblings take the raw line because they own the decode. This one
  does not: `emitModelList` has already decoded, and re-decoding would be a second `json.Unmarshal` of
  the same bytes with a second undecodable outcome to classify — which is exactly the classification
  change the ticket forbids. It takes the decoded slice. Name the parameter `entries`, mirroring
  `emitModelList`'s own name for the decoded models array.
- **No return value.** Nothing reads one. The sibling emitters that return `bool` do so because
  `emitSystemSubtype` asks "did you handle it?"; this one is called unconditionally from a rung that has
  already matched.
- **It logs nothing, on any path.** The record is `logControlResponse`'s and stays written by
  `emitModelList` before the call. This is what keeps AC 1's "same record, same keyword, same six
  attributes" structural rather than intended. A `p.log` call anywhere in the new emitter is a defect
  regardless of what it logs.
- **Placement:** immediately after `emitModelList` and above `logControlResponse`, so the callee sits
  next to its only caller in the same order the file already puts `logControlResponse` and
  `truncateField` after theirs.

### The gate moves in — decided, do not re-litigate

The `len(...) == 0` early return becomes the new emitter's **first statement**, a precondition of the
emitter rather than a guard at the call site. `emitModelList`'s last statement becomes an unconditional
`p.emitSlashCommandList(cr.Response.Response.Commands)`.

This is behaviour-identical: the gate's `return` returned from the tail of `emitModelList` with nothing
after it, so returning from the callee instead is the same control flow. Three reasons it goes in rather
than staying out:

1. The emitter becomes total over any `[]commandEntryLine` — "at most one `SlashCommandList` for this
   array" — so #1876's second call site inherits the suppression instead of repeating it. A gate a second
   caller can forget is the duplication the extraction exists to prevent.
2. The SUPPRESSION-RATHER-THAN-AN-EMPTY-EMIT argument is about the commands ARRAY, not about rung 4, so
   it travels with the construction. A gate left behind would leave that paragraph describing a
   statement in another function.
3. It makes "no rung gains or loses an emit" a property of the callee rather than of each caller.

**Consequence, and it is AC 2's:** `TestParser_SlashCommandListIsSuppressed`'s "`emitModelList`'s gate is
a len == 0 test" **moves** and becomes `emitSlashCommandList`'s. That is the "whichever of AC 3's cites
lands in this file" clause resolved.

### What travels and what stays — the doc split

The block carries ~74 lines of comment. Splitting it correctly is the substance of this ticket; a
mechanical cut-and-paste of all of it is wrong in both directions.

**Stays in `emitModelList`, at the call site:**

- The first half of the THE SECOND GATE paragraph — "INDEPENDENT of the models one: this rung was reached
  because the models array is non-empty, and whether a `SlashCommandList` joins the `ModelList` is decided
  … on the `commands` array alone". This is a statement about the RUNG.
- THE ORDER IS DELIBERATE — the two emits' order on rung 4, observable in the captured-line assertions.
  It is about where the CALL sits relative to the `ModelList` emit.
- "The whole block sits BELOW `logControlResponse` …" — reword to say the CALL sits below it. The claim
  it carries (the record is unchanged whatever happens below) is what AC 1 rests on and it survives the
  move verbatim in substance.

**Travels into `emitSlashCommandList`'s doc:**

- The second half of THE SECOND GATE paragraph — "an absent `commands`, a null one, an empty array and a
  response object carrying no such key are ONE reading and all return here — `controlResponseLine.Commands`
  is a plain slice precisely so that they are". This is about the GATE and the gate is now here.
- SUPPRESSION RATHER THAN AN EMPTY EMIT, whole and unchanged.
- The in-loop comments: the local-name note, the "no `bound` closure" note, the per-entry `cut` scope
  note, the snake_case-name note, the verbatim-name note, "nil when nothing was cut".
- NO ENTRY-COUNT CAP and no `DroppedCommands`.

### Relocation rot inside the moved block — the part `make check` cannot catch

Five sentences inside the moved block are **deictic**: they point at their surroundings rather than at a
symbol, and the move falsifies them silently. None is in AC 3's five, because none names `emitModelList`.
A green `make check` with no assertion edited proves nothing about any of them. Fix each as you move it:

| Sentence in the moved block | Why the move falsifies it | Fix |
|---|---|---|
| "`slashCommands` rather than `commands`, which is already taken by the int the record reports and would be shadowed here" | The record's `commands` int stays in `emitModelList`, so there is nothing left to shadow. The rationale evaporates. | Keep the local named `slashCommands` and restate the reason: the parameter holds the DECODED entries and the local the CONSTRUCTED `turnevent.SlashCommand` values, so two names keep the two apart. Say that the shadowing hazard is gone with the move. |
| "The models loop **above** needs both; a one-field entry needs neither" | The models loop is no longer above — it is in the caller. | "`emitModelList`'s models loop needs both". |
| "The models loop declares its own `cut` and `droppedLevels` **here** for the same reason" | Same: the loop is in another function now. | Name `emitModelList`'s models loop. |
| "The precedent is exact and **in this same function** — #1811 emitted `turnevent.ModelList` with no entry-count cap and #1812 added `maxModelListEntries` …" | The `ModelList` precedent is in `emitModelList`, which is no longer this function. | "in this emitter's caller, `emitModelList`". |
| "The DAEMON's snake_case name, **the models loop's rule**" | Reads as a reference to a loop in view; it no longer is. | Name `emitModelList`'s models loop. Lowest-severity of the five; fix it anyway while the block is in your hands. |

Everything else in the moved block cites by symbol (`maxSlashCommandName`, `turnevent.SlashCommand.Name`,
`protocol.SlashCommand.TruncatedFields`, `emitModelAnnounced`, `logControlResponse`,
`turnevent.SlashCommandList`, `protocol.SlashCommandListPayload.MarshalJSON`) and survives the move
untouched. Do not rewrite what does not need rewriting — AC 2's "no assertion edited" has a production
analogue: no sentence edited that the move did not falsify.

### The five cites that move (AC 3)

Sentence-level, not symbol-level. Each of these has an unfalsified neighbour naming the same symbol.

**`internal/streamsup/parser.go`, `commandEntryLine.Name`'s doc — two:**

1. "A byte-capped COPY of Name now leaves `emitModelList` inside a `turnevent.SlashCommand` (#1877)" →
   `emitSlashCommandList`. The sink enumeration that follows in the same paragraph is **out of scope**
   (§ Do not fix).
2. "`maxSlashCommandName` bounds Name at CONSTRUCTION in `emitModelList` (#1877), where every cap in this
   package is applied" → `emitSlashCommandList`.

   The **very next sentence** — "The transience is unchanged — this slice still lives from
   `json.Unmarshal` until `emitModelList` returns" — **stays**. It is true after the move: the decode and
   the frame holding the slice both stay in `emitModelList`, and the emitter it now passes the slice to
   returns before its caller does. Two adjacent sentences, one moves and one does not. This is the pair
   that makes a symbol-level `sed` wrong.

**`internal/turnevent/event.go` — two:**

3. `SlashCommandList`'s THE PRODUCER HAS SINCE ARRIVED paragraph: "its `emitModelList` applies the BYTE
   CAP, constructs the entries and EMITS this list (#1877, in the tree)" → `emitSlashCommandList`. Keep
   the four-way attribution split (decode / cap+construct+emit / entry-count bound / publish) intact and
   keep #1877's attribution — note the extraction as #1886's so the paragraph stays honest about which
   slice did what.
4. `SlashCommandList.Commands`: "streamsup's `emitModelList` SUPPRESSES the empty list (#1877)" →
   `emitSlashCommandList`. The rest of that paragraph — #1811's sequencing, the wire's `[]` position —
   is untouched.

**`internal/streamsup/parser_test.go` — one:**

5. `TestParser_SlashCommandListIsSuppressed`'s "`emitModelList`'s gate is a len == 0 test" →
   `emitSlashCommandList`'s, per § The gate moves in. The two sentences around it — "the decoded struct
   does not collapse them the way the gate does", and the account of what the rows pin — are unchanged.

### The four mentions that must NOT move (AC 3)

- `commandEntryLine.Name`: "this slice still lives from `json.Unmarshal` until `emitModelList` returns" —
  lifetime claim, still true.
- `commandEntryLine.Name`: "take `emitModelList`'s undecodable rung" — classification, unchanged.
- `turnevent.ModelList.Models`' cite of `emitModelList`'s `boundEach` closure — about the MODEL list,
  which does not move.
- `TestParser_ModelListIsLoggedContentFree`'s "`emitModelList`'s undecodable arm" — classification.

### The sixth mention — decided, do not edit

`cmd/pyry/interactive_turn_v2.go`'s `eventKind`, `turnevent.SlashCommandList` arm, reads "#1854 declares
it, internal/streamsup's `emitModelList` produces it (#1877)". It names `emitModelList` and it is in
neither of the ticket's two lists. **Leave it alone.** Three reasons, stated here once so nobody
re-litigates it mid-implementation:

1. AC 3 enumerates five cites across three files and this is not one of them. The AC list is the
   contract; the scope boundary says no classification change and two production files.
2. The sentence names the streamsup function that produces the variant on this lane, at the granularity
   of "a production producer exists". `emitModelList` remains that function: the new emitter has exactly
   one caller in this slice, and #1876's second call site is also inside `emitModelList`. The five that
   move are finer — they name which statement caps, constructs, emits and suppresses, and those
   statements physically relocate.
3. Editing it makes a streamsup refactor touch a `cmd/pyry` paragraph whose subject is #1720's open work.

If code review disagrees, the correction is one sentence and belongs to whoever re-points it, not to this
slice.

### What the new doc must say (AC 4)

State where the constructed value goes and where it does not, at symbol level. Four facts, and the
constraints on stating them are as load-bearing as the facts:

- It reaches `cmd/pyry`'s `interactiveTurnEmitterV2.Handle`, which has **no case for this variant**, so it
  lands on that function's own `default`, which logs it by kind through `eventKind` and returns.
- Because `Handle`'s default returns, `emitMapped` is never called for this variant — and `emitMapped` is
  `turnbridge.MapEvent`'s only caller **on this lane**. So the value never reaches `MapEvent` at all.
  "It falls to `MapEvent`'s `default`" is the wrong reason for the right conclusion.
- **Nothing retains it.** There is no `sessionModelHold` analogue for this array; the retention is the
  event's own lifetime. `maxSlashCommandName`'s doc already owns that statement — cite it, do not restate
  the argument.
- Both `Handle`'s case and the `MapEvent` arm are #1720's and still open. What the value DOES reach is
  `eventKind`, whose arm returns the variant NAME only.

Three prohibitions:

- **Do not copy `emitModelList`'s WHERE A MIS-READ INVENTORY NOW GOES paragraph.** A mis-read command list
  reaches none of its three destinations. The asymmetry is true today, so it is this slice's to state.
- **Do not restate the drop-site enumeration.** `eventKind`'s `SlashCommandList` arm carries it and owns
  it, including "`emitMapped`'s unmapped drop, which nothing routes it to". Point at that arm by symbol. A
  second copy rots the moment #1720's `Handle` case lands.
- **Say nothing about WHICH LINES produce the variant.** `eventKind`'s arm says "every initialize reply
  carrying a commands array beside its models one", and that sentence stops being the whole set on
  #1876. A doc about DESTINATIONS survives that slice; one about producers does not.

### The trap in AC 4

This exact paragraph has been got wrong once, on #1877, and the package overview records it: the spec
handed the developer a sink enumeration, the developer transcribed it, and it credited
`turnbridge.MapEvent`'s default with reach it does not have. Code review caught it by reading the call
graph — `MapEvent` has two production call sites, `emitMapped` (reached only from `Handle`'s typed arms,
none of which is `SlashCommandList`) and `resolveBoundModelList` (handed a `ModelList` explicitly).

**Verify the four facts above against the call graph before writing them, not after.** An enumeration
transcribed from a spec is not verified by transcription — including from this spec. `codegraph_callers`
on `MapEvent` and on `emitMapped`, plus a read of `Handle`'s type switch, is the check.

Note the precision required on the second bullet: `resolveBoundModelList` in `cmd/pyry/session_model_list.go`
is a `MapEvent` call site too. "on this lane" is what makes the sentence true; do not drop it.

## Do not fix

Two pre-existing sentences assert a `MapEvent` reach this variant does not have. Neither names
`emitModelList` as the constructing symbol, so neither is falsified by this move.

- `commandEntryLine.Name`'s sink enumeration: "…and from there `turnbridge.MapEvent`'s default, which has
  no arm for the variant and drops it".
- `turnevent.SlashCommandList`'s IT IS PUBLISHED BY NO PATH TODAY paragraph: "`turnbridge.MapEvent` has no
  arm for it, so its default drops it".

Correcting them is a separate concern and #1720 revisits both when it adds the arm and the case. **Do not
fix them here — and do not copy either phrasing into the new emitter's doc**, since the first is the
nearest existing enumeration to the one AC 4 asks for and copying it reproduces the #1877 defect exactly.

Also unchanged: `emitModelList`'s name (23 mentions across 6 Go files; a rename would take the production
file count from 2 to 6), the four rungs, the closed reason keyword set at `controlResponseMsg`,
`logControlResponse`'s six attributes and its doc, the entry-count cap and `DroppedCommands` (#1826),
`description` (#1833), every wire type, every protocol shape.

## Concurrency model

None. `Parser` is single-goroutine over its input; `emitModelList` and the new emitter both run on the
scan loop and hold no state across calls. `p.emit` is the existing sink hand-off and is untouched. No
goroutine is spawned, no lock is taken, no shutdown sequence changes.

## Error handling

No new failure mode. The decode, its error path and the undecodable rung all stay in `emitModelList`.
The new emitter is total over its input: it cannot fail, cannot panic (a nil or empty slice returns on
the precondition; `truncateField` is total over any string) and returns nothing. It logs nothing, so no
error path can leak workspace-authored bytes into the record — the property `logControlResponse`'s
content-free rule and `TestParser_ModelListIsLoggedContentFree` both exist to hold.

## Testing strategy

**No new test, and no assertion edited.** The move is behaviour-identical by construction, and the
existing suite is the regression net. A new test here would pin an internal call structure rather than
behaviour — and would be the wrong shape anyway, since no mutant separates gate-in-callee from
gate-at-caller.

The proof is `make check` green with these four unchanged:

- `TestParser_SlashCommandListIsSuppressed` — all four suppressing shapes plus the nak and undecodable
  rows. This is the table that would redden if the gate moved wrong or if the call site drifted above
  `logControlResponse`, the `ModelList` emit or rung 3's return.
- `TestParser_SlashCommandFieldsAreCapped` — the cap boundary at, either side of, and mid-rune around
  `maxSlashCommandName`, plus the per-entry `TruncatedFields` report. Reddens if the cap call or the
  per-entry `cut` scope moved wrong.
- `TestParser_InitializeControlResponseCountsTheCapturedCommands` — the committed capture's fifty-one
  names verbatim, two events in order.
- `TestParser_InitializeControlResponseAckReportsTheCommandCount` and
  `TestParser_ModelListIsLoggedContentFree` — the record's six attributes on every rung, and the
  content-free sweep. These are what hold AC 1's "same record, same keyword, same six attributes".

Every proof runs inside `make check`; `internal/streamsup` carries no build tag. `make check` also runs
`make cite-guard`, which fails any line-number citation added to a `//` comment — every citation in this
ticket names a symbol for that reason, and because this family's cites went stale twice in three days as
siblings merged.

The only test-file changes are two `//` comment sentences, both in
`TestParser_SlashCommandListIsSuppressed`'s doc:

- the placement sentence — "the emit block sits below `logControlResponse`, below the `ModelList` emit and
  below rung 3's return" — corrected to describe the CALL to `emitSlashCommandList` sitting there. The
  claim it carries (no non-emitting rung can reach it) is unchanged and still what the last two rows pin.
- AC 3's fifth cite, per § The five cites that move.

## Size check

Re-counted against this spec, not against the sketch:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** (`internal/streamsup/parser.go`, `internal/turnevent/event.go`) |
| Total written work | ≤ 400 | **~250** (~148 of it the 74-line block deleted and re-inserted, mostly comment lines relocating unchanged; ~35 new doc; ~10 signature/call site/gate; ~25 across the five cites, the five deictic fixes and the two test sentences) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **1** (`emitModelList`'s tail) |
| Acceptance criteria | ≤ 5 | **4** |
| Distinct error/reject branches in a state machine | ≤ 10 | **4**, unchanged — no rung gains or loses one |

XS confirmed. Branch-overlap check (`git fetch origin --prune` then every `origin/feature/<n>` diffed
against `origin/main`) found no other in-flight branch touching `internal/streamsup/parser.go`,
`internal/streamsup/parser_test.go` or `internal/turnevent/event.go`.

## Open questions

- **None blocking.** The two the ticket left to the architect are decided above and stated once: the gate
  moves into the emitter as a precondition (§ The gate moves in), and `cmd/pyry`'s `eventKind` arm is not
  edited (§ The sixth mention).
- **For #1876, not this slice:** whether the second call site passes the same `[]commandEntryLine` or
  whether rung 3 splits first. The emitter's signature is chosen to make either work — it depends on
  nothing but the decoded array.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the boundary is what this ticket moves. Everything in
  `commandEntryLine` is workspace-authored — a slash command defined in a repository was written by
  whoever wrote that repository, a lower-trust origin than claude's own strings. The boundary is the
  per-name byte cut at `truncateField(entry.Name, maxSlashCommandName)`, and it stays a SINGLE explicit
  site: the extraction moves it from inside `emitModelList` to inside `emitSlashCommandList` without
  duplicating it, which is the point of the ticket — #1876's second caller reaches the same cap rather
  than copying it. Nothing is sanitized here and the design does not start: `turnevent.SlashCommandList`'s
  SECURITY paragraph assigns the render boundary to the CLIENT, and `commandEntryLine.Name`'s re-open
  trigger names the first slice that gives the value a syntax sink. This slice gives it none.
- **[Trust boundaries — the untrusted/trusted signal]** No findings. Downstream holders learn the value is
  workspace-authored from `turnevent.SlashCommand.Name` and `SlashCommandList`'s SECURITY paragraph, both
  untouched. AC 4's new doc adds a DESTINATION statement and is explicitly forbidden from restating the
  render-boundary or retention arguments, so no second, driftable copy of the posture is created.
- **[Tokens, secrets, credentials]** Not applicable by design: no token, secret or credential is on any
  path this ticket touches. The values are claude's command names and a daemon-computed int.
- **[File operations]** Not applicable: no path is constructed, no file opened, created or renamed. The
  re-open trigger in `commandEntryLine.Name` names `filepath.Join` / `filepath.Match` as sinks that would
  reopen the validation question — the moved block reaches neither, and the extraction adds no caller
  that could.
- **[Subprocess / external command execution]** Not applicable: no `exec.Command`, no argv element, no
  environment variable. `turnevent.SlashCommandList`'s "IT IS A REPORT, NEVER A CONTROL INPUT" clause is
  the standing rule and this slice adds no path that could violate it.
- **[Cryptographic primitives]** Not applicable: no randomness, no comparison against a secret, no key.
- **[Network & I/O]** No findings, and the input bound is unchanged rather than absent: `defaultMaxParseBuf`
  caps the whole line at 4 MiB before the decoder sees it, and `maxSlashCommandName` bounds each retained
  name at 256 bytes. Both stay where they are — the extraction moves the per-name cap's call site, not the
  cap. No socket, no header, no timeout, no TLS config is in scope.
- **[Resource exhaustion]** SHOULD FIX at the ticket level, and it is already assigned: there is still no
  ENTRY-COUNT cap on this array, so a hostile 4 MiB line yields an unbounded-in-count list of
  ≤ 256-byte names. That is #1826's, fixed by `turnevent.SlashCommandList`'s own sequencing, and the moved
  NO-ENTRY-COUNT-CAP comment says so. This slice must not add the field — doing so would also add a
  seventh attribute question to `logControlResponse`. The extraction neither widens nor narrows the
  exposure: same decode, same cap, same single emit.
- **[Error messages, logs, telemetry]** No findings, and this is the category the design most had to
  protect. The new emitter logs NOTHING on any path — stated as a hard constraint in § Design, not left to
  be inferred — so the six-attribute record stays `logControlResponse`'s and stays written by
  `emitModelList` before the call. The `json.Unmarshal` error is still deliberately not logged (it quotes
  workspace-authored command names into its error text) and that arm does not move.
  `TestParser_ModelListIsLoggedContentFree` and
  `TestParser_InitializeControlResponseAckReportsTheCommandCount` are the deterministic net, unchanged.
- **[Concurrency]** Not applicable: single-goroutine, no lock, no shared state, no goroutine spawned. See
  § Concurrency model.
- **[Threat model alignment]** No findings. The relevant posture is #833's — "model / effort / YOLO values
  are NEVER logged at any level", restated in `internal/relay`'s `v2session_settings.go` and
  `internal/sessions`' `pool.go` — which `eventKind`'s `SlashCommandList` arm already argues covers
  workspace-authored strings a fortiori. Unchanged here. `docs/protocol-mobile.md` § Security model is not
  engaged: nothing crosses the wire on this path, since publishing is #1720's and still open.
- **[Documentation accuracy as a security property]** SHOULD FIX, and § The trap in AC 4 is the mitigation.
  The one security-relevant defect this ticket can realistically produce is a WRONG sink enumeration in
  the new emitter's doc — a reader who trusts "it reaches `turnbridge.MapEvent`'s default" will reason
  about a sink the value never touches, which is how #1877's version of this paragraph shipped wrong. The
  spec directs the developer to verify against the call graph (`codegraph_callers` on `MapEvent` and
  `emitMapped`, plus `Handle`'s type switch) rather than transcribe, and to point at `eventKind`'s arm
  instead of copying its enumeration. Code review should re-derive the claim rather than read it.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
